import test from 'node:test';
import assert from 'node:assert/strict';
import {workflowProgress} from '../src/progress.mjs';

const byId = workflow => Object.fromEntries(workflow.steps.map(step => [step.id, step.status]));

test('workflow progress exposes the complete native parsing path', () => {
  const parsing = workflowProgress({hasTab:true, formStage:'apply', phase:'native_parse', resumePhase:'parse_pending', hasNativeAttempt:true});
  assert.equal(parsing.path, 'native');
  assert.equal(parsing.current, 'native');
  assert.deepEqual(byId(parsing), {open:'done', enter:'done', choose:'done', native:'current', fill:'pending', verify:'pending'});

  const verifying = workflowProgress({hasTab:true, formStage:'apply', phase:'verifying', resumePhase:'parsed', hasNativeAttempt:true});
  assert.equal(verifying.current, 'verify');
  assert.equal(byId(verifying).fill, 'skipped');
});

test('workflow progress marks native parsing skipped on the direct-fill path', () => {
  const workflow = workflowProgress({hasTab:true, formStage:'apply', phase:'filling', actionKind:'fill', resumePhase:'unavailable', hasNativeAttempt:true, hasFieldActions:true});
  assert.equal(workflow.path, 'direct');
  assert.equal(workflow.current, 'fill');
  assert.equal(byId(workflow).native, 'skipped');
  assert.equal(byId(workflow).fill, 'current');
});

test('terminal workflow counts only CvMax automatic steps', () => {
  const workflow = workflowProgress({hasTab:true, formStage:'apply', phase:'partial', terminal:true, resumePhase:'parsed', hasNativeAttempt:true});
  assert.equal(workflow.current, 'verify');
  assert.equal(workflow.steps.length, 6);
  assert.equal(byId(workflow).verify, 'current');
  assert.equal(byId(workflow).fill, 'skipped');
  assert.equal(byId(workflow).handoff, undefined);
});

test('native parsing followed by conflict correction remains the native path', () => {
  const workflow = workflowProgress({hasTab:true, formStage:'apply', phase:'filling', actionKind:'fill', resumePhase:'parsed', hasNativeAttempt:true, hasFieldActions:true});
  assert.equal(workflow.path, 'native');
  assert.equal(workflow.current, 'fill');
  assert.equal(byId(workflow).native, 'done');
});

test('an interruption is shown at the actual business step', () => {
  const workflow = workflowProgress({hasTab:true, formStage:'detail', phase:'waiting_user'});
  assert.equal(workflow.current, 'enter');
  assert.equal(byId(workflow).enter, 'attention');
});
