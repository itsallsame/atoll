import mysql from 'mysql2/promise';
import {createHash} from 'node:crypto';
import {publicExperienceBundle} from './public-experience.mjs';
export {publicExperienceBundle} from './public-experience.mjs';

function required(env, name) {
  const value = env[name];
  if (!value) throw new Error(`${name}_required`);
  return value;
}

export function mysqlRegistryConfig(env = process.env) {
  const sslMode = env.CVMAX_DB_SSL ?? 'required';
  return {
    host: required(env, 'CVMAX_DB_HOST'),
    port: Number.parseInt(env.CVMAX_DB_PORT ?? '3306', 10),
    database: required(env, 'CVMAX_DB_NAME'),
    user: required(env, 'CVMAX_DB_USER'),
    password: required(env, 'CVMAX_DB_PASSWORD'),
    ssl: sslMode === 'disabled' ? undefined : {rejectUnauthorized: sslMode === 'verify_identity'},
    charset: 'utf8mb4',
    connectionLimit: 8,
    queueLimit: 32,
    connectTimeout: 10_000,
    enableKeepAlive: true,
  };
}

export class MysqlExperienceRegistry {
  constructor(config) {
    this.pool = mysql.createPool(config);
  }

  static fromEnv(env = process.env) {
    return new MysqlExperienceRegistry(mysqlRegistryConfig(env));
  }

  async health() {
    const [rows] = await this.pool.query('SELECT DATABASE() AS db, CURRENT_USER() AS user');
    return {ready: true, database: rows[0].db, user: rows[0].user};
  }

  async releasedBundle(familyKey) {
    const [rows] = await this.pool.execute(
      `SELECT er.bundle_json AS bundle
         FROM experience_releases er
         JOIN schema_versions sv ON sv.schema_version_id = er.schema_version_id
        WHERE sv.family_key = ? AND er.status = 'released'
        ORDER BY er.released_at DESC, er.release_version DESC
        LIMIT 1`,
      [familyKey],
    );
    if (!rows.length) return null;
    return typeof rows[0].bundle === 'string' ? JSON.parse(rows[0].bundle) : rows[0].bundle;
  }

  async releasedRecord(familyKey) {
    const [rows] = await this.pool.execute(
      `SELECT er.release_id, er.release_version, er.status, er.bundle_json, er.bundle_sha256,
              er.signing_key_id, er.signature, er.released_at
         FROM experience_releases er
         JOIN schema_versions sv ON sv.schema_version_id = er.schema_version_id
        WHERE sv.family_key = ? AND er.status = 'released'
        ORDER BY er.released_at DESC, er.release_version DESC
        LIMIT 1`,
      [familyKey],
    );
    if (!rows.length) return null;
    const row=rows[0];
    return {releaseId:Number(row.release_id),releaseVersion:row.release_version,status:row.status,bundle:typeof row.bundle_json==='string'?JSON.parse(row.bundle_json):row.bundle_json,bundleSha256:row.bundle_sha256,signingKeyId:row.signing_key_id,signature:Buffer.from(row.signature).toString('base64'),releasedAt:row.released_at};
  }

  async saveCandidate(item) {
    const bundle = publicExperienceBundle(item);
    const family = bundle.family;
    const fingerprints = Object.keys(bundle.versions);
    if (fingerprints.length !== 1) throw new Error('candidate_requires_one_schema_version');
    const fingerprint = fingerprints[0];
    const version = bundle.versions[fingerprint];
    const connection = await this.pool.getConnection();
    try {
      await connection.beginTransaction();
      await connection.execute(
        `INSERT INTO page_families
         (family_key, origin, host, route_template, form_stage, qualifiers_json, status)
         VALUES (?, ?, ?, ?, ?, ?, 'active')
         ON DUPLICATE KEY UPDATE origin = VALUES(origin), host = VALUES(host),
           route_template = VALUES(route_template), form_stage = VALUES(form_stage), updated_at = CURRENT_TIMESTAMP(3)`,
        [family.key, family.origin, family.host, family.routeTemplate, family.stage, JSON.stringify(family.qualifiers ?? {})],
      );
      await connection.execute(
        `INSERT INTO schema_versions (family_key, fingerprint, field_shape_json)
         VALUES (?, ?, ?)
         ON DUPLICATE KEY UPDATE field_shape_json = VALUES(field_shape_json), last_seen_at = CURRENT_TIMESTAMP(3)`,
        [family.key, fingerprint, JSON.stringify(version.fieldShape ?? [])],
      );
      const [schemaRows] = await connection.execute(
        'SELECT schema_version_id FROM schema_versions WHERE family_key = ? AND fingerprint = ? FOR UPDATE',
        [family.key, fingerprint],
      );
      const schemaVersionId = schemaRows[0].schema_version_id;
      const [releaseRows] = await connection.execute(
        'SELECT COALESCE(MAX(release_version), 0) + 1 AS next_version FROM experience_releases WHERE schema_version_id = ?',
        [schemaVersionId],
      );
      const releaseVersion = Number(releaseRows[0].next_version);
      const serialized = JSON.stringify(bundle);
      const bundleSha256 = createHash('sha256').update(serialized).digest('hex');
      const [existing] = await connection.execute(
        `SELECT release_id, release_version, status FROM experience_releases
          WHERE schema_version_id = ? AND bundle_sha256 = ? AND status IN ('candidate','canary','released')
          ORDER BY release_version DESC LIMIT 1`,
        [schemaVersionId, bundleSha256],
      );
      if (existing.length) {
        await connection.commit();
        return {releaseId:Number(existing[0].release_id),releaseVersion:existing[0].release_version,status:existing[0].status};
      }
      const [release] = await connection.execute(
        `INSERT INTO experience_releases
         (schema_version_id, release_version, status, bundle_json, bundle_sha256)
         VALUES (?, ?, 'candidate', ?, ?)`,
        [schemaVersionId, releaseVersion, serialized, bundleSha256],
      );
      for (const strategy of Object.values(bundle.strategies)) {
        await connection.execute(
          `INSERT INTO field_strategies
           (release_id, strategy_id, action_kind, fact_id, anchor_json, status, verified_runs, failed_runs)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
          [release.insertId, strategy.id, strategy.kind, strategy.factId ?? null, JSON.stringify(strategy.anchor), strategy.status, strategy.successCount ?? 0, strategy.failureCount ?? 0],
        );
      }
      await connection.commit();
      return {releaseId: release.insertId, releaseVersion, status: 'candidate'};
    } catch (error) {
      await connection.rollback();
      throw error;
    } finally {
      connection.release();
    }
  }

  async recordFeedback(feedback) {
    const values = [
      feedback.feedbackId,
      feedback.receiptDigest,
      feedback.installationHash,
      feedback.familyKey,
      feedback.schemaFingerprint,
      feedback.releaseId ?? null,
      feedback.strategyId ?? null,
      feedback.outcome,
      feedback.reasonCode ?? null,
      feedback.evidenceDigest,
    ];
    const [result] = await this.pool.execute(
      `INSERT INTO experience_feedback
       (feedback_id, receipt_digest, installation_hash, family_key, schema_fingerprint,
        release_id, strategy_id, outcome, reason_code, evidence_digest)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON DUPLICATE KEY UPDATE feedback_id = feedback_id`,
      values,
    );
    return {accepted: result.affectedRows === 1, duplicate: result.affectedRows !== 1};
  }

  async releaseBundle(releaseId) {
    const [rows]=await this.pool.execute('SELECT bundle_json FROM experience_releases WHERE release_id = ?', [releaseId]);
    if (!rows.length) throw new Error('experience_release_not_found');
    return typeof rows[0].bundle_json==='string'?JSON.parse(rows[0].bundle_json):rows[0].bundle_json;
  }

  async releaseStats(releaseId) {
    const [releaseRows]=await this.pool.execute('SELECT status FROM experience_releases WHERE release_id = ?', [releaseId]);
    if (!releaseRows.length) return null;
    const [rows]=await this.pool.execute(
      `SELECT COUNT(DISTINCT installation_hash) AS independent_installations,
              SUM(outcome = 'verified') AS verified_runs,
              SUM(outcome IN ('failed','corrected','drift')) AS failed_runs
         FROM experience_feedback WHERE release_id = ?`,
      [releaseId],
    );
    return {status:releaseRows[0].status,independentInstallations:Number(rows[0].independent_installations??0),verifiedRuns:Number(rows[0].verified_runs??0),failedRuns:Number(rows[0].failed_runs??0)};
  }

  updateReleaseStats(releaseId, stats) {
    return this.pool.execute(
      'UPDATE experience_releases SET independent_installations = ?, verified_runs = ?, failed_runs = ? WHERE release_id = ?',
      [stats.independentInstallations, stats.verifiedRuns, stats.failedRuns, releaseId],
    );
  }

  setReleaseStatus(releaseId, status, stats) {
    return this.pool.execute(
      'UPDATE experience_releases SET status = ?, independent_installations = ?, verified_runs = ?, failed_runs = ? WHERE release_id = ?',
      [status, stats.independentInstallations, stats.verifiedRuns, stats.failedRuns, releaseId],
    );
  }

  async publishRelease(releaseId, bundle, signed, stats) {
    await this.pool.execute(
      `UPDATE experience_releases SET status='released', bundle_json=?, bundle_sha256=?, signing_key_id=?, signature=?,
       independent_installations=?, verified_runs=?, failed_runs=?, released_at=CURRENT_TIMESTAMP(3) WHERE release_id=? AND status IN ('candidate','canary')`,
      [JSON.stringify(bundle),signed.bundleSha256,signed.signingKeyId,Buffer.from(signed.signature,'base64'),stats.independentInstallations,stats.verifiedRuns,stats.failedRuns,releaseId],
    );
  }

  async close() {
    await this.pool.end();
  }
}
