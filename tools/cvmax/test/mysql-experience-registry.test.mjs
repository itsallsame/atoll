import assert from 'node:assert/strict';
import test from 'node:test';
import {mysqlRegistryConfig} from '../src/mysql-experience-registry.mjs';
import {publicExperienceBundle} from '../src/public-experience.mjs';

test('mysql registry requires a dedicated complete connection config', () => {
  assert.throws(() => mysqlRegistryConfig({}), /CVMAX_DB_HOST_required/);
  const config = mysqlRegistryConfig({
    CVMAX_DB_HOST: 'db.test',
    CVMAX_DB_PORT: '3307',
    CVMAX_DB_NAME: 'cvmax',
    CVMAX_DB_USER: 'cvmax_app',
    CVMAX_DB_PASSWORD: 'secret',
    CVMAX_DB_SSL: 'verify_identity',
  });
  assert.equal(config.database, 'cvmax');
  assert.equal(config.port, 3307);
  assert.deepEqual(config.ssl, {rejectUnauthorized: true});
  assert.equal('multipleStatements' in config, false);
});

test('public experience strips run identifiers and rejects fact values', () => {
  const bundle = publicExperienceBundle({
    family: {key: 'a'.repeat(64), origin: 'https://jobs.test', host: 'jobs.test', routeTemplate: '/apply', stage: 'apply'},
    versions: {['b'.repeat(64)]: {successfulRuns: ['private-run'], fieldShape: []}},
    strategies: {['c'.repeat(64)]: {id: 'c'.repeat(64), kind: 'fill', factId: 'name', anchor: {label: '姓名'}, successfulRuns: ['private-run']}},
  });
  assert.equal(JSON.stringify(bundle).includes('private-run'), false);
  assert.throws(() => publicExperienceBundle({
    family: {key: 'a'.repeat(64)}, versions: {['b'.repeat(64)]: {}}, strategies: {bad: {value: 'private'}},
  }), /private_experience_key/);
});
