import fs from 'node:fs';
import path from 'node:path';
import {createHash} from 'node:crypto';
import mysql from 'mysql2/promise';
import {fileURLToPath} from 'node:url';
import {loadEnv} from '../src/registry-server.mjs';

const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
const env=loadEnv(process.argv[2]);
const required=name=>{if(!env[name])throw new Error(`${name}_required`);return env[name]};
const migrations=fs.readdirSync(path.join(root,'db/migrations')).filter(name=>/^\d+.*\.sql$/u.test(name)).sort();
const connection=await mysql.createConnection({
  host:required('CVMAX_DB_HOST'),port:Number(env.CVMAX_DB_PORT??3306),database:required('CVMAX_DB_NAME'),
  user:required('CVMAX_DB_ADMIN_USER'),password:required('CVMAX_DB_ADMIN_PASSWORD'),
  ssl:env.CVMAX_DB_SSL==='disabled'?undefined:{rejectUnauthorized:env.CVMAX_DB_SSL==='verify_identity'},
  charset:'utf8mb4',connectTimeout:10_000,
});

try {
  await connection.query(`CREATE TABLE IF NOT EXISTS cvmax_schema_migrations (
    migration_name VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
    migration_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    applied_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`);
  for (const name of migrations) {
    const sql=fs.readFileSync(path.join(root,'db/migrations',name),'utf8');
    const digest=createHash('sha256').update(sql).digest('hex');
    const [rows]=await connection.execute('SELECT migration_sha256 FROM cvmax_schema_migrations WHERE migration_name=?',[name]);
    if(rows.length){if(rows[0].migration_sha256!==digest)throw new Error(`migration_checksum_mismatch:${name}`);console.log(`[cvmax-registry] migration already applied: ${name}`);continue}
    const statements=sql.split(/;\s*(?:\r?\n|$)/u).map(value=>value.replace(/^\s*--.*$/gmu,'').trim()).filter(Boolean);
    for(const statement of statements) await connection.query(statement);
    await connection.execute('INSERT INTO cvmax_schema_migrations (migration_name,migration_sha256) VALUES (?,?)',[name,digest]);
    console.log(`[cvmax-registry] migration applied: ${name}`);
  }
} finally { await connection.end(); }
