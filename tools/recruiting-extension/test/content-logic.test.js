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

function element(textContent = '', attributes = {}) {
  return {textContent, getAttribute: name => attributes[name] ?? null};
}

function record(selectors) {
  return {querySelector: selector => selectors[selector] ?? null};
}

function browserRecipe(kind = 'detail') {
  return {
    abi_version: logic.RECIPE_VERSION,
    kind,
    required_capability: 'browser.profile.repair',
    transport: 'browser',
    request: {method: 'GET'},
    extraction: {fields: {identity: '[data-user-id]'}},
  };
}

function probeEnvironment(documentRoot, href = 'https://account.example.test/private/jobs') {
  const location = new URL(href);
  return {documentRoot, location};
}

test('profile canary runs a frozen Detail Recipe and returns counts without extracted values', () => {
  const documentRoot = record({'[data-user-id]': element('private-user-42')});
  const result = logic.profileRecipeProbe({securityDomain: 'account.example.test',
    canaryURL: 'https://account.example.test/private/jobs', recipe: browserRecipe(), minimumRecords: 1},
  probeEnvironment(documentRoot));
  assert.deepEqual(result, {authenticated: true, recordCount: 1});
  assert.equal(JSON.stringify(result).includes('private-user-42'), false);
});

test('profile canary requires enough complete Listing records', () => {
  const recipe = browserRecipe('listing');
  recipe.extraction.collection = 'article.private-job';
  recipe.extraction.fields.title = 'h2';
  recipe.extraction.attributes = {identity: 'data-user-id'};
  const records = [
    record({'[data-user-id]': element('', {'data-user-id': '1'}), h2: element('Engineer')}),
    record({'[data-user-id]': element('', {'data-user-id': '2'}), h2: element('Designer')}),
    record({'[data-user-id]': element('', {'data-user-id': '3'}), h2: element('')}),
  ];
  const documentRoot = {querySelectorAll: selector => selector === 'article.private-job' ? records : []};
  assert.deepEqual(logic.profileRecipeProbe({securityDomain: 'account.example.test',
    canaryURL: 'https://account.example.test/private/jobs', recipe, minimumRecords: 2},
  probeEnvironment(documentRoot)), {authenticated: true, recordCount: 2});
  assert.deepEqual(logic.profileRecipeProbe({securityDomain: 'account.example.test',
    canaryURL: 'https://account.example.test/private/jobs', recipe, minimumRecords: 3},
  probeEnvironment(documentRoot)), {authenticated: false, recordCount: 2});
});

test('profile canary rejects a different page or security domain', () => {
  const documentRoot = record({'[data-user-id]': element('private-user-42')});
  const input = {securityDomain: 'account.example.test', canaryURL: 'https://account.example.test/private/jobs',
    recipe: browserRecipe(), minimumRecords: 1};
  assert.throws(() => logic.profileRecipeProbe(input,
    probeEnvironment(documentRoot, 'https://account.example.test/public/jobs')), /profile_canary_location_mismatch/);
  assert.throws(() => logic.profileRecipeProbe({...input, securityDomain: 'other.example.test'},
    probeEnvironment(documentRoot)), /profile_canary_location_mismatch/);
});

test('profile canary treats missing required fields as unauthenticated', () => {
  const result = logic.profileRecipeProbe({securityDomain: 'account.example.test',
    canaryURL: 'https://account.example.test/private/jobs', recipe: browserRecipe(), minimumRecords: 1},
  probeEnvironment(record({})));
  assert.deepEqual(result, {authenticated: false, recordCount: 0});
});

test('profile canary rejects invalid selectors and non-browser Recipes', () => {
  const recipe = browserRecipe('listing');
  recipe.extraction.collection = '[';
  const documentRoot = {querySelectorAll: () => { throw new Error('bad selector'); }};
  assert.throws(() => logic.profileRecipeProbe({securityDomain: 'account.example.test',
    canaryURL: 'https://account.example.test/private/jobs', recipe, minimumRecords: 1},
  probeEnvironment(documentRoot)), /profile_canary_selector_invalid/);
  assert.throws(() => logic.profileRecipeProbe({securityDomain: 'account.example.test',
    canaryURL: 'https://account.example.test/private/jobs', recipe: {...browserRecipe(), transport: 'http_html'},
    minimumRecords: 1}, probeEnvironment(record({}))), /profile_canary_recipe_invalid/);
});

test('manifest grants no blanket recruitment-site host access', () => {
  const manifest = JSON.parse(fs.readFileSync(path.join(__dirname, '../extension/manifest.json'), 'utf8'));
  assert.equal(manifest.manifest_version, 3);
  assert.deepEqual(manifest.host_permissions.sort(), ['http://127.0.0.1/*', 'http://[::1]/*'].sort());
  assert.equal(manifest.permissions.includes('activeTab'), true);
  assert.equal(manifest.permissions.includes('tabs'), false);
});
