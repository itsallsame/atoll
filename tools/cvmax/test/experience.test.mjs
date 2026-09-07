import test from 'node:test';
import assert from 'node:assert/strict';
import {matchExperience,pageFamilyFor,recordVerifiedExperience,resolveExperiencedTarget,schemaFor,suspendExperience} from '../src/experience.mjs';

const fields = (extra = []) => [
  {ref: 'name-ref', label: '姓名 *', semanticId: 'candidate.name', group: 'basic', type: 'text', value: 'First Private Name'},
  {ref: 'email-ref', label: '邮箱', semanticId: 'candidate.email', group: 'basic', type: 'email', value: 'first@example.test'},
  ...extra
];

const snapshot = (positionId, extra = []) => ({
  url: `https://jobs.bytedance.com/campus/resume/${positionId}/apply?spread=private`,
  fields: fields(extra),
  controls: []
});

function application(runId, currentSnapshot, evidence = null) {
  return {
    runId,
    snapshot: currentSnapshot,
    outcome: 'partial',
    evidence: evidence ?? [
      {action: {kind: 'fill', fieldRef: 'name-ref', semanticId: 'candidate.name', group: 'basic', semanticOrdinal: 0, factId: 'name', value: currentSnapshot.fields[0].value}},
      {action: {kind: 'fill', fieldRef: 'email-ref', semanticId: 'candidate.email', group: 'basic', semanticOrdinal: 0, factId: 'email', value: currentSnapshot.fields[1].value}}
    ]
  };
}

const verified = (currentSnapshot, action) => currentSnapshot.fields.find(field => field.ref === action.fieldRef)?.value === action.value;

test('page family ignores job ids and query tracking while keeping the application stage', () => {
  const first = pageFamilyFor(snapshot('7613237638787270917'));
  const second = pageFamilyFor(snapshot('7622996922623297797'));
  assert.equal(first.key, second.key);
  assert.equal(first.routeTemplate, '/campus/resume/:id/apply');
  assert.equal(first.stage, 'apply');
});

test('partial applications promote each independently verified field after two distinct runs', () => {
  const experiences = {};
  const first = snapshot('7613237638787270917');
  first.fields[1].value = '';
  const firstEvidence = [
    {action: {kind: 'fill', fieldRef: 'name-ref', semanticId: 'candidate.name', group: 'basic', semanticOrdinal: 0, factId: 'name', value: first.fields[0].value}},
    {action: {kind: 'fill', fieldRef: 'email-ref', semanticId: 'candidate.email', group: 'basic', semanticOrdinal: 0, factId: 'email', value: 'not-retained@example.test'}}
  ];
  recordVerifiedExperience(experiences, application('run-one', first, firstEvidence), verified);
  recordVerifiedExperience(experiences, application('run-two', snapshot('7622996922623297797')), verified);

  const stored = Object.values(experiences)[0];
  const name = Object.values(stored.strategies).find(strategy => strategy.factId === 'name');
  const email = Object.values(stored.strategies).find(strategy => strategy.factId === 'email');
  assert.equal(name.status, 'reusable');
  assert.equal(email.status, 'candidate');
  assert.equal(JSON.stringify(stored).includes('Private Name'), false);
  assert.equal(JSON.stringify(stored).includes('example.test'), false);
});

function reusableFixture() {
  const experiences = {};
  recordVerifiedExperience(experiences, application('run-one', snapshot('7613237638787270917')), verified);
  recordVerifiedExperience(experiences, application('run-two', snapshot('7622996922623297797')), verified);
  return experiences;
}

test('a known application route resolves a supplied detail URL without copying tracking data', () => {
  const experiences = reusableFixture();
  const result = resolveExperiencedTarget(experiences, 'https://jobs.bytedance.com/campus/position/7999999999999999999/detail?spread=private');
  assert.equal(result.url, 'https://jobs.bytedance.com/campus/resume/7999999999999999999/apply');
  assert.equal(JSON.stringify(result).includes('private'), false);
  assert.equal(resolveExperiencedTarget(experiences, 'https://jobs.bytedance.com/campus/position/detail'), null);
});

test('route resolution refuses competing application templates', () => {
  const experiences = reusableFixture(), first = Object.values(experiences)[0];
  experiences.competing = structuredClone(first);
  experiences.competing.family = {...first.family, routeTemplate: '/campus/application/:id/apply'};
  assert.equal(resolveExperiencedTarget(experiences, 'https://jobs.bytedance.com/campus/position/7999999999999999999/detail'), null);
});

test('a different job in the same page family receives deterministic value-free actions', () => {
  const experiences = reusableFixture();
  const current = snapshot('7999999999999999999');
  current.fields = current.fields.map((field, index) => ({...field, ref: `new-${index}`, value: ''}));
  const match = matchExperience(experiences, current, [
    {id: 'name', value: 'Second Private Name', sourceRef: 'file:resume.pdf#p=1'},
    {id: 'email', value: 'second@example.test', sourceRef: 'file:resume.pdf#p=1'}
  ]);
  assert.equal(match.match, 'exact');
  assert.deepEqual(match.suggestedActions.map(action => [action.kind, action.fieldRef, action.factId]), [
    ['fill', 'new-0', 'name'],
    ['fill', 'new-1', 'email']
  ]);
  assert.equal(JSON.stringify(match).includes('Second Private Name'), false);
  assert.equal(JSON.stringify(match).includes('second@example.test'), false);
});

test('framework-generated semantic ids may change without losing a stable field mapping', () => {
  const experiences = reusableFixture();
  const current = snapshot('7999999999999999999');
  current.fields = current.fields.map((field, index) => ({...field, ref: `new-${index}`, semanticId: `formily-runtime-${Date.now()}-${index}`, value: ''}));
  const match = matchExperience(experiences, current, [
    {id: 'name', value: 'Name', sourceRef: 'user:answer'},
    {id: 'email', value: 'mail@example.test', sourceRef: 'user:answer'}
  ]);
  assert.equal(match.match, 'exact');
  assert.deepEqual(match.suggestedActions.map(action => action.factId), ['name', 'email']);
});

test('a compatible page variant reuses stable fields and reports only structural drift', () => {
  const experiences = reusableFixture();
  const current = snapshot('7888888888888888888', [
    {ref: 'city-ref', label: '期望城市', semanticId: 'candidate.city', group: 'preference', type: 'text', value: ''}
  ]);
  current.fields[0].value = '';
  current.fields[1].value = '';
  const match = matchExperience(experiences, current, [
    {id: 'name', value: 'Another Name', sourceRef: 'user:answer'},
    {id: 'email', value: 'another@example.test', sourceRef: 'user:answer'},
    {id: 'city', value: '北京', sourceRef: 'user:answer'}
  ]);
  assert.equal(match.match, 'compatible');
  assert.equal(match.suggestedActions.length, 2);
  assert.equal(match.drift.addedFieldKinds.length, 1);
  assert.equal(match.drift.missingFieldKinds.length, 0);
});

test('a collapsed initial form reuses fields learned from its expanded post-parser form', () => {
  const experiences = {}, expandedExtra = [
    {ref: 'city-ref', label: '期望城市', semanticId: 'candidate.city', group: 'preference', type: 'text', value: '北京'},
    {ref: 'school-ref', label: '学校', semanticId: 'education.school', group: 'education', type: 'text', value: '测试大学'},
    {ref: 'major-ref', label: '专业', semanticId: 'education.major', group: 'education', type: 'text', value: '计算机'}
  ];
  const first = snapshot('7613237638787270917', expandedExtra), second = snapshot('7622996922623297797', expandedExtra);
  recordVerifiedExperience(experiences, application('expanded-one', first), verified);
  recordVerifiedExperience(experiences, application('expanded-two', second), verified);
  const collapsed = snapshot('7999999999999999999');
  collapsed.fields.forEach(field => { field.value = ''; });
  const match = matchExperience(experiences, collapsed, [
    {id: 'name', value: 'Name', sourceRef: 'user:answer'},
    {id: 'email', value: 'mail@example.test', sourceRef: 'user:answer'}
  ]);
  assert.equal(match.match, 'compatible');
  assert.equal(match.similarity, 1);
  assert.deepEqual(match.suggestedActions.map(action => action.factId), ['name', 'email']);
  assert.equal(match.drift.missingFieldKinds.length, 3);
});

test('equally compatible versions prefer the one with reusable strategies', () => {
  const experiences = reusableFixture(), expanded = snapshot('7333333333333333333', [
    {ref: 'new-only', label: '新字段', semanticId: 'new.field', group: 'new', type: 'text', value: 'new value'}
  ]);
  recordVerifiedExperience(experiences, application('new-candidate-run', expanded, [
    {action: {kind: 'fill', fieldRef: 'new-only', semanticId: 'new.field', group: 'new', semanticOrdinal: 0, factId: 'new_field', value: 'new value'}}
  ]), verified);
  const current = snapshot('7444444444444444444');
  current.fields.forEach(field => { field.value = ''; });
  const match = matchExperience(experiences, current, [
    {id: 'name', value: 'Name', sourceRef: 'user:answer'},
    {id: 'email', value: 'mail@example.test', sourceRef: 'user:answer'}
  ]);
  assert.deepEqual(match.suggestedActions.map(action => action.factId), ['name', 'email']);
});

test('an exact candidate-only version falls back to a reusable compatible version', () => {
  const experiences = reusableFixture(), expanded = snapshot('7333333333333333333', [
    {ref: 'new-only', label: '新字段', semanticId: 'new.field', group: 'new', type: 'text', value: 'new value'}
  ]);
  recordVerifiedExperience(experiences, application('new-candidate-run', expanded, [
    {action: {kind: 'fill', fieldRef: 'new-only', semanticId: 'new.field', group: 'new', semanticOrdinal: 0, factId: 'new_field', value: 'new value'}}
  ]), verified);
  expanded.fields.forEach(field => { field.value = ''; });
  const match = matchExperience(experiences, expanded, [
    {id: 'name', value: 'Name', sourceRef: 'user:answer'},
    {id: 'email', value: 'mail@example.test', sourceRef: 'user:answer'}
  ]);
  assert.equal(match.match, 'compatible');
  assert.deepEqual(match.suggestedActions.map(action => action.factId), ['name', 'email']);
});

test('conflicting reusable meanings for one field are blocked instead of guessed', () => {
  const experiences = {}, current = snapshot('7111111111111111111');
  current.fields = [current.fields[0]];
  const evidence = [
    {action: {kind: 'fill', fieldRef: 'name-ref', semanticId: 'candidate.name', group: 'basic', semanticOrdinal: 0, factId: 'name', value: current.fields[0].value}},
    {action: {kind: 'fill', fieldRef: 'name-ref', semanticId: 'candidate.name', group: 'basic', semanticOrdinal: 0, factId: 'legal_name', value: current.fields[0].value}}
  ];
  recordVerifiedExperience(experiences, application('ambiguous-one', current, evidence), verified);
  recordVerifiedExperience(experiences, application('ambiguous-two', current, evidence), verified);
  current.fields[0].value = '';
  const match = matchExperience(experiences, current, [
    {id: 'name', value: 'Name', sourceRef: 'user:answer'},
    {id: 'legal_name', value: 'Legal Name', sourceRef: 'user:answer'}
  ]);
  assert.deepEqual(match.suggestedActions, []);
  assert.equal(match.blocked.length, 2);
  assert.ok(match.blocked.every(entry => entry.reason === 'experience_mapping_ambiguous'));
});

test('a repeated-section strategy cannot bind to a basic field that reused its framework id', () => {
  const experiences = {}, expanded = snapshot('7111111111111111111', [
    {ref: 'project-name', label: '项目名称', semanticId: 'formily-item-name', group: 'formily-item-project_list', type: 'text', value: 'Project'}
  ]), evidence = [{action: {kind: 'fill', fieldRef: 'project-name', semanticId: 'formily-item-name', group: 'formily-item-project_list', semanticOrdinal: 0, factId: 'project_1_name', value: 'Project'}}];
  recordVerifiedExperience(experiences, application('project-one', expanded, evidence), verified);
  recordVerifiedExperience(experiences, application('project-two', expanded, evidence), verified);
  const collapsed = snapshot('7222222222222222222');
  collapsed.fields[0].semanticId = 'formily-item-name';
  collapsed.fields[0].value = '';
  const match = matchExperience(experiences, collapsed, [{id: 'project_1_name', value: 'Project', sourceRef: 'file:resume.pdf#p=2'}]);
  assert.equal(match, null);
});

test('field feedback suspends only the corrected mapping and retains other reusable fields', () => {
  const experiences = reusableFixture(), current = snapshot('7999999999999999999');
  current.fields.forEach(field => { field.value = ''; });
  let match = matchExperience(experiences, current, [
    {id: 'name', value: 'Name', sourceRef: 'user:answer'},
    {id: 'email', value: 'mail@example.test', sourceRef: 'user:answer'}
  ]);
  const rejected = match.suggestedActions.find(action => action.factId === 'name');
  const feedback = suspendExperience(experiences, current, {reason: '用户纠正字段语义', strategyId: rejected.experienceStrategyId});
  assert.equal(feedback.scope, 'field_strategy');
  assert.equal(JSON.stringify(feedback).includes('用户纠正字段语义'), false);
  match = matchExperience(experiences, current, [
    {id: 'name', value: 'Name', sourceRef: 'user:answer'},
    {id: 'email', value: 'mail@example.test', sourceRef: 'user:answer'}
  ]);
  assert.deepEqual(match.suggestedActions.map(action => action.factId), ['email']);
});

test('an anchor containing an authorized private value is not persisted', () => {
  const experiences = {}, current = snapshot('7613237638787270917');
  current.fields = [{ref: 'private-label', label: 'First Private Name', semanticId: null, group: null, type: 'text', value: 'First Private Name'}];
  const evidence = [{action: {kind: 'fill', fieldRef: 'private-label', factId: 'name', value: 'First Private Name'}}];
  const result = recordVerifiedExperience(experiences, application('private-anchor-run', current, evidence), verified, ['First Private Name']);
  assert.equal(result, null);
  assert.deepEqual(experiences, {});
});

test('schema fingerprint is insensitive to values but detects field structure changes', () => {
  const first = snapshot('7613237638787270917'), second = snapshot('7622996922623297797');
  second.fields[0].value = 'Completely Different';
  assert.equal(schemaFor(first).fingerprint, schemaFor(second).fingerprint);
  second.fields.push({ref: 'extra', label: '作品链接', semanticId: 'portfolio', group: 'extra', type: 'url', value: ''});
  assert.notEqual(schemaFor(first).fingerprint, schemaFor(second).fingerprint);
});
