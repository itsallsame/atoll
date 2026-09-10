const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const logic = require('../extension/content-logic.js');

function input() {
  return {
    version: logic.VERSION,
    captureId: 'capture-01',
    sourceId: 'source-01',
    recipeId: 'listing-example',
    recipeVersion: 2,
    pageURL: 'https://jobs.example.test/openings',
    userConfirmed: true,
    capturedAt: '2026-09-10T12:00:00.000Z',
    matchedCount: 12,
    sampleHTML: '<article class="job"><a href="https://jobs.example.test/1">Role</a></article>',
    marks: {
      collection: {selector: 'article.job'},
      job_key: {selector: 'a', attribute: 'href'},
      title: {selector: 'a'},
      detail_url: {selector: 'a', attribute: 'href'},
    },
  };
}

test('buildDraft emits the shared declarative Recipe ABI and evidence-only trace', () => {
  const draft = logic.buildDraft(input());
  assert.equal(draft.candidate.abi_version, 'recruiting.recipe.v1');
  assert.equal(draft.candidate.kind, 'listing');
  assert.equal(draft.candidate.transport, 'http_html');
  assert.equal(draft.candidate.extraction.fields.job_key, 'a');
  assert.equal(draft.candidate.extraction.attributes.detail_url, 'href');
  assert.equal(draft.candidate.listing.boundary_mode, 'frontier_keys');
  assert.equal(draft.trace.at(-1).kind, 'capture_artifact');
  assert.equal(draft.evidence.matched_count, 12);
  const wire = JSON.stringify(draft).toLowerCase();
  for (const forbidden of ['cookie', 'password', 'authorization', 'assignment_version', 'checkpoint']) {
    assert.equal(wire.includes(forbidden), false, forbidden);
  }
});

test('buildDraft requires explicit user confirmation and all identity fields', () => {
  const unconfirmed = input();
  unconfirmed.userConfirmed = false;
  assert.throws(() => logic.buildDraft(unconfirmed), /capture_identity_invalid/);
  const missing = input();
  delete missing.marks.detail_url;
  assert.throws(() => logic.buildDraft(missing), /required_marks_missing/);
});

test('buildDraft emits a Detail Recipe bound to a Job sample without Listing semantics', () => {
  const detail = input();
  detail.recipeKind = 'detail';
  detail.sampleJobId = 'job-01';
  detail.pageURL = 'https://jobs.example.test/openings/123';
  detail.marks = {
    title: {selector: 'h1'},
    description: {selector: 'main .description'},
    location: {selector: '[data-field="location"]'},
  };
  const draft = logic.buildDraft(detail);
  assert.equal(draft.sample_job_id, 'job-01');
  assert.equal(draft.candidate.kind, 'detail');
  assert.equal(draft.candidate.extraction.fields.description, 'main .description');
  assert.equal(draft.candidate.listing, undefined);
  assert.equal(draft.trace.some(step => step.kind === 'mark_collection'), false);
});

test('bridge endpoint is loopback websocket only', () => {
  assert.equal(logic.bridgeEndpoint('ws://127.0.0.1:4321/capture'), 'ws://127.0.0.1:4321/capture');
  for (const endpoint of ['wss://example.test/capture', 'ws://0.0.0.0:4321/capture',
    'ws://127.0.0.1:4321/other', 'ws://user:secret@127.0.0.1:4321/capture']) {
    assert.throws(() => logic.bridgeEndpoint(endpoint), /bridge_endpoint_invalid/);
  }
});

test('manifest grants no blanket recruitment-site host access', () => {
  const manifest = JSON.parse(fs.readFileSync(path.join(__dirname, '../extension/manifest.json'), 'utf8'));
  assert.equal(manifest.manifest_version, 3);
  assert.deepEqual(manifest.host_permissions.sort(), ['http://127.0.0.1/*', 'http://[::1]/*'].sort());
  assert.equal(manifest.permissions.includes('activeTab'), true);
  assert.equal(manifest.permissions.includes('tabs'), false);
});
