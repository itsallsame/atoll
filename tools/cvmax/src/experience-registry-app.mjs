import {createHash, randomUUID} from 'node:crypto';
import {publicExperienceBundle} from './public-experience.mjs';
import {releaseDigest, signExperienceRelease} from './experience-release.mjs';

const sha = value => createHash('sha256').update(String(value)).digest('hex');
const hex64 = value => typeof value === 'string' && /^[a-f0-9]{64}$/i.test(value);

export class ExperienceRegistryApp {
  constructor({repository, privateKey, signingKeyId, installationPepper, policy = {canaryInstallations: 3, releaseInstallations: 5, suspendFailures: 2, maximumFailureRate: 0.1}}) {
    if (!repository || !privateKey || !signingKeyId || !installationPepper) throw new Error('experience_registry_app_config_invalid');
    this.repository = repository;
    this.privateKey = privateKey;
    this.signingKeyId = signingKeyId;
    this.installationPepper = installationPepper;
    this.policy = policy;
  }

  installationHash(id) {
    if (typeof id !== 'string' || id.length < 16 || id.length > 200) throw new Error('installation_id_invalid');
    return sha(`${this.installationPepper}:${id}`);
  }

  health() { return this.repository.health(); }

  async candidate({installationId, bundle, evidenceDigest, receiptId}) {
    if (!hex64(evidenceDigest) || typeof receiptId !== 'string' || !receiptId || receiptId.length > 150) throw new Error('candidate_receipt_invalid');
    bundle = publicExperienceBundle(bundle);
    const saved = await this.repository.saveCandidate(bundle);
    const fingerprint = Object.keys(bundle.versions)[0];
    await this.repository.recordFeedback({
      feedbackId: randomUUID(), receiptDigest: sha(`candidate:${receiptId}`), installationHash:this.installationHash(installationId),
      familyKey:bundle.family.key, schemaFingerprint:fingerprint, releaseId:saved.releaseId, outcome:'verified', evidenceDigest,
    });
    return this.evaluate(saved.releaseId);
  }

  async feedback({installationId, receiptId, familyKey, schemaFingerprint, releaseId, strategyId = null, outcome, reasonCode = null, evidenceDigest}) {
    if (typeof receiptId !== 'string' || !receiptId || receiptId.length > 150 || !hex64(familyKey) || !hex64(schemaFingerprint) || !hex64(evidenceDigest) || !['verified','failed','corrected','drift'].includes(outcome)) throw new Error('feedback_invalid');
    await this.repository.recordFeedback({feedbackId:randomUUID(), receiptDigest:sha(`feedback:${receiptId}`), installationHash:this.installationHash(installationId), familyKey, schemaFingerprint, releaseId, strategyId, outcome, reasonCode, evidenceDigest});
    return this.evaluate(releaseId);
  }

  async evaluate(releaseId) {
    const stats = await this.repository.releaseStats(releaseId);
    if (!stats) throw new Error('experience_release_not_found');
    const total = stats.verifiedRuns + stats.failedRuns;
    const failureRate = total ? stats.failedRuns / total : 0;
    if (['canary','released'].includes(stats.status) && stats.failedRuns >= this.policy.suspendFailures && failureRate > this.policy.maximumFailureRate) {
      await this.repository.setReleaseStatus(releaseId, 'suspended', stats);
      return {releaseId, status:'suspended', stats};
    }
    let status=stats.status;
    if (status==='candidate'&&stats.independentInstallations>=this.policy.canaryInstallations&&failureRate<=this.policy.maximumFailureRate){await this.repository.setReleaseStatus(releaseId,'canary',stats);status='canary'}
    if (['candidate','canary'].includes(status) && stats.independentInstallations >= this.policy.releaseInstallations && failureRate <= this.policy.maximumFailureRate) {
      const bundle = publicExperienceBundle(await this.repository.releaseBundle(releaseId));
      bundle.status = 'reusable';
      const signed = signExperienceRelease(bundle, this.privateKey, this.signingKeyId);
      await this.repository.publishRelease(releaseId, bundle, signed, stats);
      return {releaseId, status:'released', stats, bundleSha256:signed.bundleSha256};
    }
    await this.repository.updateReleaseStats(releaseId, stats);
    return {releaseId, status, stats};
  }

  async release(familyKey) {
    if (!hex64(familyKey)) throw new Error('family_key_invalid');
    return this.repository.releasedRecord(familyKey);
  }
}
