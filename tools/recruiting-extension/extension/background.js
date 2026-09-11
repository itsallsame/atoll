importScripts('content-logic.js');

const logic = globalThis.AtollRecruitingCaptureLogic;
const state = {phase: 'not_configured', connected: false, captureId: '', pageURL: '', marks: {}, lastError: '', result: null,
  profileTask: null, profileTabId: null};
const pending = new Map();
const completedProfileResults = new Map();
let socket = null;
const initialized = chrome.storage.local.get(['atollRecruitingCaptureState']).then(saved => {
  if (saved.atollRecruitingCaptureState) {
    Object.assign(state, saved.atollRecruitingCaptureState, {connected: false});
  }
});

async function publish() {
  await chrome.storage.local.set({atollRecruitingCaptureState: state});
  chrome.runtime.sendMessage({type: 'capture.state', state}).catch(() => {});
}

function publicState() { return JSON.parse(JSON.stringify(state)); }

function fail(error) {
  state.phase = 'error';
  state.lastError = error instanceof Error ? error.message : String(error);
  publish();
}

async function savedConfig() {
  const saved = await chrome.storage.local.get(['bridgeEndpoint', 'bridgeToken', 'profileId', 'profileBindingToken']);
  if (!saved.bridgeEndpoint || !saved.bridgeToken) throw new Error('bridge_not_configured');
  return {endpoint: logic.bridgeEndpoint(saved.bridgeEndpoint), token: String(saved.bridgeToken),
    profileId: String(saved.profileId || ''), profileToken: String(saved.profileBindingToken || '')};
}

async function connect() {
  if (socket?.readyState === WebSocket.OPEN) return socket;
  if (socket?.readyState === WebSocket.CONNECTING) {
    await new Promise((resolve, reject) => {
      socket.addEventListener('open', resolve, {once: true});
      socket.addEventListener('error', () => reject(new Error('bridge_unreachable')), {once: true});
    });
    return socket;
  }
  const config = await savedConfig();
  if (config.token.length < 32 || config.token.length > 256) throw new Error('bridge_token_invalid');
  if ((config.profileId === '') !== (config.profileToken === '')) throw new Error('profile_binding_incomplete');
  if (config.profileToken && (config.profileToken.length < 32 || config.profileToken.length > 256)) {
    throw new Error('profile_binding_token_invalid');
  }
  const binding = config.profileId
    ? `&profile_id=${encodeURIComponent(config.profileId)}&profile_token=${encodeURIComponent(config.profileToken)}` : '';
  const current = new WebSocket(`${config.endpoint}?token=${encodeURIComponent(config.token)}${binding}`);
  socket = current;
  current.addEventListener('message', event => {
    let response;
    try { response = JSON.parse(event.data); } catch { return fail('invalid_bridge_response'); }
    if (response.kind === 'profile.task') {
      receiveProfileTask(response.task).catch(fail);
      return;
    }
    const waiter = pending.get(response.request_id);
    if (!waiter) return;
    pending.delete(response.request_id);
    response.status === 'completed' ? waiter.resolve(response.result) : waiter.reject(new Error(response.error || 'bridge_submission_failed'));
  });
  current.addEventListener('close', () => {
    if (socket === current) socket = null;
    state.connected = false;
    publish();
    for (const waiter of pending.values()) waiter.reject(new Error('bridge_disconnected'));
    pending.clear();
  });
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('bridge_connect_timeout')), 5000);
    current.addEventListener('open', () => { clearTimeout(timer); resolve(); }, {once: true});
    current.addEventListener('error', () => { clearTimeout(timer); reject(new Error('bridge_unreachable')); }, {once: true});
  });
  state.connected = true;
  state.phase = state.captureId ? 'capturing' : 'ready';
  state.lastError = '';
  await publish();
  return current;
}

function validProfileTask(task) {
  try {
    const canary = new URL(String(task?.canary_url || ''));
    const minimum = Number(task?.canary_minimum_records);
    const hash = /^sha256:[0-9a-f]{64}$/;
    return task?.version === 'recruiting.browser-broker.v2' &&
      (task.kind === 'profile_repair' || task.kind === 'profile_verification') &&
      String(task.request_id || '').length > 0 && String(task.request_id).length <= 191 &&
      String(task.session_id || '').length > 0 && String(task.profile_id || '').length > 0 &&
	  Number.isSafeInteger(Number(task.profile_version)) && Number(task.profile_version) >= 2 &&
      canary.protocol === 'https:' && canary.hostname === task.security_domain && !canary.port &&
      !canary.username && !canary.password && !canary.hash && Number.isInteger(minimum) && minimum >= 1 && minimum <= 100 &&
      task.canary_recipe?.abi_version === 'recruiting.recipe.v1' && task.canary_recipe.transport === 'browser' &&
      task.canary_recipe.required_capability === 'browser.profile.repair' &&
      ['listing', 'detail'].includes(task.canary_recipe.kind) &&
      String(task.canary_recipe.request?.method || '').toUpperCase() === 'GET' &&
      Object.keys(task.canary_recipe.extraction?.fields || {}).length > 0 &&
      String(task.canary_recipe_id || '').length > 0 && Number.isSafeInteger(Number(task.canary_recipe_version)) &&
      Number(task.canary_recipe_version) >= 1 && hash.test(String(task.canary_content_hash || '')) &&
      hash.test(String(task.canary_contract_hash || '')) && Date.parse(task.expires_at) > Date.now();
  } catch {
    return false;
  }
}

async function sendProfileResult(result) {
  const current = await connect();
  completedProfileResults.set(result.request_id, result);
  if (completedProfileResults.size > 20) completedProfileResults.delete(completedProfileResults.keys().next().value);
  current.send(JSON.stringify(result));
}

async function receiveProfileTask(task) {
  if (!validProfileTask(task)) throw new Error('invalid_profile_task');
  const replay = completedProfileResults.get(task.request_id);
  if (replay) return sendProfileResult(replay);
  state.profileTask = task;
  state.lastError = '';
  state.phase = task.kind === 'profile_repair' ? 'profile_repair' : 'profile_verifying';
  await publish();
  if (task.kind === 'profile_verification') {
    try {
      await executeProfileProbe(task, false);
    } catch (error) {
      await sendProfileResult(profileFailureResult(task, 'profile_verification_failed'));
      state.profileTask = null;
      state.profileTabId = null;
      state.phase = 'error';
      state.lastError = error instanceof Error ? error.message : String(error);
      await publish();
    }
  }
}

function profileFailureResult(task, failureCode, recordCount = 0) {
  return {version: 'recruiting.extension-bridge.v1', kind: 'profile.result', request_id: task.request_id,
    status: 'failed', failure_code: failureCode,
    evidence: {version: 'recruiting.browser-broker.v2', task_kind: task.kind,
      security_domain: task.security_domain, observed_at: new Date().toISOString(), probe: 'failed',
      canary_recipe_id: task.canary_recipe_id, canary_recipe_version: task.canary_recipe_version,
      canary_content_hash: task.canary_content_hash, canary_contract_hash: task.canary_contract_hash,
      record_count: recordCount}};
}

async function executeProfileProbe(task, userConfirmed) {
  if (!validProfileTask(task)) throw new Error('profile_task_expired');
  if (task.kind === 'profile_repair' && !userConfirmed) throw new Error('login_confirmation_required');
  let tab = task.kind === 'profile_verification' && state.profileTabId
    ? await chrome.tabs.get(state.profileTabId).catch(() => activeTab()) : await activeTab();
  const canary = new URL(task.canary_url);
  if (canary.hostname !== task.security_domain || canary.protocol !== 'https:') throw new Error('profile_canary_invalid');
  const current = new URL(tab.url);
  if (current.protocol !== 'https:' || current.hostname !== task.security_domain) {
    throw new Error('open_profile_security_domain');
  }
  if (task.kind === 'profile_repair') state.profileTabId = tab.id;
  if (tab.url !== canary.href) {
    await navigateAndWait(tab.id, canary.href);
    tab = await chrome.tabs.get(tab.id);
  }
  if (tab.url !== canary.href) throw new Error('open_exact_profile_canary_page');
  await installContent(tab.id);
  const probe = await chrome.tabs.sendMessage(tab.id, {type: 'profile.recipe_probe', securityDomain: task.security_domain,
    canaryURL: task.canary_url, recipe: task.canary_recipe, minimumRecords: task.canary_minimum_records});
  if (!probe?.ok) throw new Error(probe?.error || 'profile_probe_failed');
  const authenticated = probe.authenticated === true;
  const isRepair = task.kind === 'profile_repair';
  const result = authenticated
    ? {version: 'recruiting.extension-bridge.v1', kind: 'profile.result', request_id: task.request_id,
      status: 'completed', ...(isRepair ? {} : {authenticated: true}),
      evidence: {version: 'recruiting.browser-broker.v2', task_kind: task.kind,
        security_domain: task.security_domain, observed_at: new Date().toISOString(),
        probe: isRepair ? 'interactive_login_completed' : 'authenticated_canary',
        canary_recipe_id: task.canary_recipe_id, canary_recipe_version: task.canary_recipe_version,
        canary_content_hash: task.canary_content_hash, canary_contract_hash: task.canary_contract_hash,
        record_count: Number(probe.recordCount || 0)}}
    : profileFailureResult(task, isRepair ? 'interactive_login_probe_failed' : 'authenticated_canary_failed',
      Number(probe.recordCount || 0));
  await sendProfileResult(result);
  state.profileTask = null;
	if (!isRepair) state.profileTabId = null;
  state.phase = 'ready';
  state.result = {profile_request_id: task.request_id, status: result.status};
  await publish();
}

function navigateAndWait(tabId, targetURL) {
  return new Promise((resolve, reject) => {
    let settled = false;
    const finish = error => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      chrome.tabs.onUpdated.removeListener(listener);
      error ? reject(error) : resolve();
    };
    const timer = setTimeout(() => finish(new Error('profile_canary_navigation_timeout')), 30000);
    const listener = (updatedId, changeInfo) => {
      if (updatedId !== tabId || changeInfo.status !== 'complete') return;
      finish();
    };
    chrome.tabs.onUpdated.addListener(listener);
    chrome.tabs.update(tabId, {url: targetURL}).catch(() => finish(new Error('profile_canary_navigation_failed')));
  });
}

async function activeTab() {
  const [tab] = await chrome.tabs.query({active: true, lastFocusedWindow: true});
  if (!tab?.id || !/^https:\/\//.test(tab.url || '')) throw new Error('active_https_page_required');
  return tab;
}

async function installContent(tabId) {
  await chrome.scripting.executeScript({target: {tabId}, files: ['content-logic.js', 'content.js']});
}

async function beginCapture(config) {
  await connect();
  const tab = await activeTab();
  await installContent(tab.id);
  const recipeKind = config.recipeKind === 'detail' ? 'detail' : 'listing';
  if (recipeKind === 'detail' && !String(config.sampleJobId || '').trim()) throw new Error('sample_job_id_required');
  const response = await chrome.tabs.sendMessage(tab.id, {type: 'capture.begin', recipeKind});
  if (!response?.ok) throw new Error(response?.error || 'capture_begin_failed');
  state.captureId = crypto.randomUUID();
  state.pageURL = response.pageURL;
  state.marks = {};
  state.result = null;
  state.lastError = '';
  state.phase = 'capturing';
  state.config = {sourceId: String(config.sourceId || '').trim(), recipeId: String(config.recipeId || '').trim(),
    recipeVersion: Number(config.recipeVersion), recipeKind, sampleJobId: String(config.sampleJobId || '').trim()};
  await publish();
  return publicState();
}

async function arm(role) {
  const tab = await activeTab();
  if (!state.captureId || tab.url !== state.pageURL) throw new Error('capture_page_changed');
  const response = await chrome.tabs.sendMessage(tab.id, {type: 'capture.arm', role});
  if (!response?.ok) throw new Error(response?.error || 'capture_arm_failed');
  state.phase = `mark_${role}`;
  await publish();
  return publicState();
}

async function snapshotAndSubmit(userConfirmed) {
  if (!userConfirmed) throw new Error('ordering_contract_confirmation_required');
  const tab = await activeTab();
  if (!state.captureId || tab.url !== state.pageURL) throw new Error('capture_page_changed');
  const config = {captureId: state.captureId, ...state.config, userConfirmed: true};
  const response = await chrome.tabs.sendMessage(tab.id, {type: 'capture.snapshot', config});
  if (!response?.ok) throw new Error(response?.error || 'capture_snapshot_failed');
  const current = await connect();
  const requestId = crypto.randomUUID();
  const result = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => { pending.delete(requestId); reject(new Error('bridge_submission_timeout')); }, 60000);
    pending.set(requestId, {resolve: value => { clearTimeout(timer); resolve(value); },
      reject: error => { clearTimeout(timer); reject(error); }});
    current.send(JSON.stringify({version: 'recruiting.extension-bridge.v1', kind: 'capture.propose',
      request_id: requestId, draft: response.draft}));
  });
  state.phase = 'completed';
  state.result = result;
  state.lastError = '';
  await publish();
  return publicState();
}

chrome.runtime.onMessage.addListener((message, sender, reply) => {
  if (sender.id !== chrome.runtime.id) return false;
  const run = async () => {
    await initialized;
    switch (message?.type) {
    case 'capture.status': return publicState();
    case 'capture.configure': {
      const endpoint = logic.bridgeEndpoint(message.endpoint);
      const token = String(message.token || '').trim();
      const profileId = String(message.profileId || '').trim();
      const profileBindingToken = String(message.profileToken || '').trim();
      if (token.length < 32 || token.length > 256) throw new Error('bridge_token_invalid');
      if ((profileId === '') !== (profileBindingToken === '') ||
        (profileBindingToken && (profileBindingToken.length < 32 || profileBindingToken.length > 256))) {
        throw new Error('profile_binding_invalid');
      }
      socket?.close();
      socket = null;
      await chrome.storage.local.set({bridgeEndpoint: endpoint, bridgeToken: token, profileId, profileBindingToken});
      state.phase = 'ready'; state.lastError = ''; state.connected = false;
      await connect();
      return publicState();
    }
    case 'capture.begin.request': return beginCapture(message.config || {});
    case 'capture.arm.request': return arm(message.role);
    case 'capture.submit.request': return snapshotAndSubmit(message.userConfirmed === true);
    case 'profile.complete.request':
      if (!state.profileTask || state.profileTask.kind !== 'profile_repair') throw new Error('no_profile_repair_task');
      await executeProfileProbe(state.profileTask, message.userConfirmed === true);
      return publicState();
    case 'capture.marked':
      state.marks[message.role] = message.mark;
      state.phase = 'capturing';
      await publish();
      return publicState();
    case 'capture.error': throw new Error(message.error || 'capture_page_error');
    default: throw new Error('unknown_extension_message');
    }
  };
  run().then(value => reply({ok: true, value})).catch(error => { fail(error); reply({ok: false, error: error.message}); });
  return true;
});

initialized.then(publish).catch(fail);
