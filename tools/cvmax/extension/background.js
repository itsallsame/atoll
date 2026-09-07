import {applicationStartObserved, DEFAULT_ENDPOINT, normalizeEndpoint, normalizeOrigin, normalizeProgress, validateBridgeCommand, validateCurrentCommand, validateOpenCommand, validateToken} from './protocol.js';

const state = {
  phase: 'not_paired',
  endpoint: DEFAULT_ENDPOINT,
  active: null,
  progress: null,
  lastError: null,
};
const blockedRuns = new Set();
let socket = null;
let reconnectTimer = null;
let keepaliveTimer = null;
let userDisconnected = false;

function stopKeepalive() {
  clearInterval(keepaliveTimer);
  keepaliveTimer = null;
}

function startKeepalive() {
  stopKeepalive();
  keepaliveTimer = setInterval(() => send({kind: 'ping', at: Date.now()}), 20_000);
}

function publicState() {
  return {...state, connected: socket?.readyState === WebSocket.OPEN};
}

async function publish() {
  await chrome.storage.local.set({cvmaxState: publicState()});
  chrome.runtime.sendMessage({type: 'stateChanged', state: publicState()}).catch(() => {});
}

function fail(error) {
  state.phase = 'error';
  state.lastError = error instanceof Error ? error.message : String(error);
  publish();
}

function send(message) {
  if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(message));
}

async function routeCommand(message) {
  if (message.action === 'current') {
    validateCurrentCommand(message);
    const [tab] = await chrome.tabs.query({active: true, lastFocusedWindow: true});
    if (!tab?.id || !tab.url) throw new Error('active_page_not_found');
    return {tabId: tab.id, url: tab.url};
  }
  if (message.action === 'open') {
    validateOpenCommand(message);
    const created = await chrome.tabs.create({url: message.targetURL, active: true});
    const tab = await waitForLoad(created.id, message.expiresAt);
    if (normalizeOrigin(tab.url) === normalizeOrigin(message.targetURL)) {
      await chrome.scripting.executeScript({target: {tabId: tab.id}, files: ['content-logic.js', 'content.js']});
      const progress = normalizeProgress({phase: 'observing', title: 'CvMax 已打开申请页', detail: '正在建立任务并识别页面大区块。', fieldLabel: '扫描申请内容', sectionLabel: '整份申请', nextOwner: 'CvMax'});
      await chrome.tabs.sendMessage(tab.id, {cvmax: true, kind: 'command', id: crypto.randomUUID(), action: 'progress', targetURL: tab.url, expiresAt: Date.now() + 2500, progress});
      state.progress = progress;
      await publish();
    }
    return {tabId: tab.id, url: tab.url, requestedURL: message.targetURL};
  }
  const tab = await chrome.tabs.get(message.tabId);
  validateBridgeCommand(message, {tab, blockedRuns});
  const startingApplication = message.action === 'act' && message.actionSpec?.kind === 'start';
  const knownTabIds = startingApplication ? new Set((await chrome.tabs.query({windowId: tab.windowId})).map(item => item.id)) : null;
  await chrome.scripting.executeScript({target: {tabId: message.tabId}, files: ['content-logic.js', 'content.js']});
  if (message.action === 'stop') blockedRuns.add(message.runId);
  else if (message.runId) state.active = {tabId: message.tabId, targetURL: message.targetURL, runId: message.runId};
  if (message.progress) state.progress = normalizeProgress(message.progress);
  if (state.progress?.terminal) state.active = null;
  let result;
  try { result = await chrome.tabs.sendMessage(message.tabId, {...message, cvmax: true}); }
  catch (error) { if (!startingApplication) throw error; }
  if (startingApplication) {
    const before = {url: message.targetURL, documentEpoch: message.actionSpec.beforeDocumentEpoch, fieldCount: message.actionSpec.beforeFieldCount, pageState: message.actionSpec.beforePageState};
    let settled = await chrome.tabs.get(message.tabId), observed = null;
    while (Date.now() + 350 < message.expiresAt) {
      const opened = (await chrome.tabs.query({windowId: tab.windowId})).filter(item => !knownTabIds.has(item.id) && item.url && normalizeOrigin(item.url) === normalizeOrigin(message.targetURL));
      if (opened.length > 1) throw new Error('application_start_opened_ambiguous_tabs');
      if (opened.length === 1) settled = opened[0];
      else settled = await chrome.tabs.get(message.tabId);
      if (normalizeOrigin(settled.url) !== normalizeOrigin(message.targetURL)) throw new Error('application_start_redirect_outside_origin');
      if (settled.status === 'complete') {
        await chrome.scripting.executeScript({target: {tabId: settled.id}, files: ['content-logic.js', 'content.js']}).catch(() => {});
        observed = await chrome.tabs.sendMessage(settled.id, {...message, id: crypto.randomUUID(), action: 'observe', targetURL: settled.url, expiresAt: message.expiresAt, progress: undefined}).catch(() => null);
        if (applicationStartObserved(before, observed)) break;
      }
      await new Promise(resolve => setTimeout(resolve, 250));
      settled = await chrome.tabs.get(settled.id);
    }
    result = {...result, accepted: true, tabId: settled.id, url: settled.url, navigated: settled.id !== message.tabId || settled.url !== message.targetURL, openedNewTab: settled.id !== message.tabId, startObserved: applicationStartObserved(before, observed)};
    state.active = {tabId: settled.id, targetURL: settled.url, runId: message.runId};
  }
  if (result?.error) throw new Error(result.error);
  await publish();
  return result;
}

async function waitForLoad(tabId, expiresAt) {
  const current = await chrome.tabs.get(tabId);
  if (current.status === 'complete') return current;
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => finish(new Error('page_open_timeout')), Math.max(0, expiresAt - Date.now()));
    const changed = (id, info) => { if (id === tabId && info.status === 'complete') chrome.tabs.get(tabId).then(tab => finish(null, tab), finish); };
    const removed = id => { if (id === tabId) finish(new Error('opened_tab_closed')); };
    function finish(error, tab) {
      clearTimeout(timeout);
      chrome.tabs.onUpdated.removeListener(changed);
      chrome.tabs.onRemoved.removeListener(removed);
      error ? reject(error) : resolve(tab);
    }
    chrome.tabs.onUpdated.addListener(changed);
    chrome.tabs.onRemoved.addListener(removed);
  });
}

async function handleBridgeMessage(raw) {
  let message;
  try { message = JSON.parse(raw.data); } catch { throw new Error('invalid_bridge_message'); }
  if (message.kind === 'paired') {
    state.phase = 'paired';
    state.lastError = null;
    await publish();
    return;
  }
  if (message.kind !== 'command') throw new Error('unexpected_bridge_message');
  try {
    const result = await routeCommand(message);
    send({kind: 'result', id: message.id, result});
  } catch (error) {
    send({kind: 'result', id: message.id, error: error.message});
    throw error;
  }
}

async function connect() {
  clearTimeout(reconnectTimer);
  reconnectTimer = null;
  socket?.close();
  const saved = await chrome.storage.local.get(['cvmaxEndpoint', 'cvmaxToken']);
  if (!saved.cvmaxToken) {
    state.phase = 'not_paired';
    state.lastError = null;
    await publish();
    return;
  }
  state.endpoint = normalizeEndpoint(saved.cvmaxEndpoint || DEFAULT_ENDPOINT);
  userDisconnected = false;
  state.phase = 'connecting';
  await publish();
  const current = new WebSocket(state.endpoint);
  socket = current;
  current.addEventListener('open', () => {
    send({kind: 'pair', token: validateToken(saved.cvmaxToken)});
    startKeepalive();
  });
  current.addEventListener('message', event => handleBridgeMessage(event).catch(fail));
  current.addEventListener('error', () => fail('local_service_unreachable'));
  current.addEventListener('close', () => {
    if (socket !== current) return;
    stopKeepalive();
    socket = null;
    state.phase = userDisconnected ? 'not_paired' : 'disconnected';
    publish();
    if (!userDisconnected) reconnectTimer = setTimeout(() => connect().catch(fail), 1500);
  });
}

async function emergencyStop() {
  const active = state.active;
  if (!active) return publicState();
  blockedRuns.add(active.runId);
  await chrome.tabs.sendMessage(active.tabId, {
    cvmax: true,
    kind: 'command',
    id: crypto.randomUUID(),
    action: 'stop',
    ...active,
    expiresAt: Date.now() + 2500,
  }).catch(() => {});
  send({kind: 'event', event: 'emergency_stop', ...active, at: Date.now()});
  state.phase = 'stopped';
  state.active = null;
  state.progress = state.progress ? {...state.progress, phase: 'stopped', title: '已停止', detail: '不会再执行新的页面动作。', nextOwner: '你', terminal: true} : null;
  state.lastError = null;
  await publish();
  return publicState();
}

chrome.runtime.onMessage.addListener((message, sender, reply) => {
  const internal = sender.id === chrome.runtime.id && sender.url?.startsWith(chrome.runtime.getURL(''));
  const activePage = message?.type === 'pageEmergencyStop' && sender.id === chrome.runtime.id && Number.isSafeInteger(sender.tab?.id) && sender.tab.id === state.active?.tabId && sender.url === state.active?.targetURL;
  if (!internal && !activePage) return false;
  const action = async () => {
    if (message.type === 'status') return publicState();
    if (message.type === 'pair') {
      const endpoint = normalizeEndpoint(message.endpoint || DEFAULT_ENDPOINT);
      const token = validateToken(message.token);
      await chrome.storage.local.set({cvmaxEndpoint: endpoint, cvmaxToken: token});
      await connect();
      return publicState();
    }
    if (message.type === 'emergencyStop' || message.type === 'pageEmergencyStop') return emergencyStop();
    if (message.type === 'disconnect') {
      userDisconnected = true;
      clearTimeout(reconnectTimer);
      stopKeepalive();
      await chrome.storage.local.remove('cvmaxToken');
      socket?.close();
      socket = null;
      state.phase = 'not_paired';
      state.active = null;
      state.progress = null;
      await publish();
      return publicState();
    }
    throw new Error('unknown_popup_message');
  };
  action().then(value => reply({ok: true, value})).catch(error => reply({ok: false, error: error.message}));
  return true;
});

connect().catch(fail);
