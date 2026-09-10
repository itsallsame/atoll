importScripts('content-logic.js');

const logic = globalThis.AtollRecruitingCaptureLogic;
const state = {phase: 'not_configured', connected: false, captureId: '', pageURL: '', marks: {}, lastError: '', result: null};
const pending = new Map();
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
  const saved = await chrome.storage.local.get(['bridgeEndpoint', 'bridgeToken']);
  if (!saved.bridgeEndpoint || !saved.bridgeToken) throw new Error('bridge_not_configured');
  return {endpoint: logic.bridgeEndpoint(saved.bridgeEndpoint), token: String(saved.bridgeToken)};
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
  const current = new WebSocket(`${config.endpoint}?token=${encodeURIComponent(config.token)}`);
  socket = current;
  current.addEventListener('message', event => {
    let response;
    try { response = JSON.parse(event.data); } catch { return fail('invalid_bridge_response'); }
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
      if (token.length < 32 || token.length > 256) throw new Error('bridge_token_invalid');
      socket?.close();
      socket = null;
      await chrome.storage.local.set({bridgeEndpoint: endpoint, bridgeToken: token});
      state.phase = 'ready'; state.lastError = ''; state.connected = false;
      await connect();
      return publicState();
    }
    case 'capture.begin.request': return beginCapture(message.config || {});
    case 'capture.arm.request': return arm(message.role);
    case 'capture.submit.request': return snapshotAndSubmit(message.userConfirmed === true);
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
