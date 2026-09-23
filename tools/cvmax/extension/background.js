import {applicationStartObserved, applicationStartSettled, DEFAULT_ENDPOINT, detectBrowserFamily, EXTENSION_PROTOCOL, normalizeEndpoint, normalizeOrigin, normalizeProgress, normalizeSiteBoundary, validateBridgeCommand, validateCurrentCommand, validateOpenCommand, validateProtocol, validateToken} from './protocol.js';

const BROWSER_FAMILY = detectBrowserFamily(navigator.userAgent, navigator.userAgentData?.brands);

const state = {
  phase: 'not_paired',
  endpoint: DEFAULT_ENDPOINT,
  active: null,
  progress: null,
  lastError: null,
  browserFamily: BROWSER_FAMILY,
  selected: false,
};
const blockedRuns = new Set();
let socket = null;
let reconnectTimer = null;
let keepaliveTimer = null;
let userDisconnected = false;
const RECONNECT_ALARM = 'cvmax-reconnect';
const CONTENT_GENERATION = crypto.randomUUID();

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

async function installWindowOpenCapture(tabId, key, startedAt) {
  await chrome.scripting.executeScript({
    target: {tabId},
    world: 'MAIN',
    func: (captureKey, captureStartedAt) => {
      const old = globalThis.__cvmaxWindowOpenCapture;
      if (old?.original) globalThis.open = old.original;
      const original = globalThis.open;
      const capture = {key: captureKey, startedAt: captureStartedAt, original, urls: []};
      globalThis.__cvmaxWindowOpenCapture = capture;
      globalThis.open = function(url, target, features) {
        try { capture.urls.push(new URL(String(url), location.href).href); } catch {}
        return original.call(this, url, target, features);
      };
    },
    args: [key, startedAt],
  });
}

async function readWindowOpenCapture(tabId, key, startedAt, restore = false) {
  await chrome.scripting.executeScript({target:{tabId},world:'MAIN',files:['evidence.js']});
  const [receipt] = await chrome.scripting.executeScript({
    target: {tabId},
    world: 'MAIN',
    func: (captureKey, captureStartedAt, shouldRestore) => {
      const capture = globalThis.__cvmaxWindowOpenCapture;
      const early = globalThis.__cvmaxEarlyOpenCapture;
      // Some recruitment sites call window.open only after an asynchronous
      // eligibility request. Keep a bounded grace period so a URL produced at
      // the end of the previous start lease is not lost before the controlled
      // retry can adopt it.
      const earlyURLs = early?.records?.filter(item => item?.at >= captureStartedAt - 30_000 && typeof item.url === 'string').map(item => item.url) ?? [];
      const urls = [...new Set([...(capture?.key === captureKey ? capture.urls : []), ...earlyURLs])];
      if (shouldRestore && capture?.key === captureKey) {
        globalThis.open = capture.original;
        delete globalThis.__cvmaxWindowOpenCapture;
      }
      return urls;
    },
    args: [key, startedAt, restore],
  });
  return receipt?.result ?? [];
}

async function activateInMainWorld(tabId, anchor) {
  if (!anchor || !['dialog', 'page'].includes(anchor.surface) || typeof anchor.label !== 'string' || !anchor.label || typeof anchor.role !== 'string' || typeof anchor.tag !== 'string' || typeof anchor.type !== 'string' || !Number.isSafeInteger(anchor.ordinal) || anchor.ordinal < 0) {
    throw new Error('main_world_anchor_invalid');
  }
  await chrome.scripting.executeScript({target:{tabId},world:'MAIN',files:['evidence.js']});
  const [receipt] = await chrome.scripting.executeScript({
    target: {tabId},
    world: 'MAIN',
    func: async expected => {
      const clean = value => String(value ?? '').replace(/\s+/g, ' ').trim();
      const visible = element => !!element.getClientRects().length && getComputedStyle(element).visibility !== 'hidden' && getComputedStyle(element).display !== 'none';
      const detectedSurface=globalThis.CvMaxEvidence?.activeSurface();
      const surfaces=expected.surface==='dialog'?(detectedSurface?[detectedSurface]:[]):expected.surface==='page'?[document]:[];
      if (!surfaces.length) return {activated: false, reason: 'interaction_surface_not_found'};
      const candidates=surfaces.flatMap(surface=>{
        const target=globalThis.CvMaxEvidence.resolveInteraction(surface,expected);
        return target?[{surface,target}]:[];
      });
      if(candidates.length!==1)return{activated:false,reason:'interaction_control_not_unique'};
      const {surface,target}=candidates[0];
      target.scrollIntoView({block:"nearest",inline:"nearest"});
      if(!globalThis.CvMaxEvidence.topHit(target))return{activated:false,reason:"interaction_target_occluded"};
      const rect=target.getBoundingClientRect(),x=rect.left+rect.width/2,y=rect.top+rect.height/2,init={bubbles:true,cancelable:true,button:0,buttons:1,clientX:x,clientY:y,pointerId:1,pointerType:'mouse',isPrimary:true};
      target.focus?.({preventScroll:true});
      if(globalThis.PointerEvent)target.dispatchEvent(new PointerEvent('pointerdown',init));
      target.dispatchEvent(new MouseEvent('mousedown',init));
      if(globalThis.PointerEvent)target.dispatchEvent(new PointerEvent('pointerup',{...init,buttons:0}));
      target.dispatchEvent(new MouseEvent('mouseup',{...init,buttons:0}));
      HTMLElement.prototype.click.call(target);
      await new Promise(resolve => setTimeout(resolve, 150));
      return {activated: true, surfaceChanged: expected.surface==='dialog'?!visible(surface):false, mode: 'main_world_pointer'};
    },
    args: [anchor],
  });
  return receipt?.result ?? {activated: false, reason: 'main_world_receipt_missing'};
}

async function routeCommand(message) {
  if (message.action === 'current') {
    validateCurrentCommand(message);
    const [tab] = await chrome.tabs.query({active: true, lastFocusedWindow: true});
    if (!tab?.id || !tab.url) throw new Error('active_page_not_found');
    const {cvmaxActionDiagnostic:diagnostic}=await chrome.storage.local.get('cvmaxActionDiagnostic');
    return {tabId: tab.id, url: tab.url, diagnostic:diagnostic??null};
  }
  if (message.action === 'open') {
    validateOpenCommand(message);
    const created = await chrome.tabs.create({url: message.targetURL, active: true});
    const tab = await waitForLoad(created.id, message.expiresAt);
    if (normalizeOrigin(tab.url) === normalizeOrigin(message.targetURL)) {
      await chrome.scripting.executeScript({target: {tabId: tab.id}, files: ['evidence.js', 'content-logic.js', 'content.js']});
      const progress = normalizeProgress(message.progress ?? {taskId: message.taskId, phase: 'observing', title: 'CvMax 已打开申请页', detail: '正在建立任务并识别页面大区块。', fieldLabel: '扫描申请内容', sectionLabel: '整份申请', nextOwner: 'CvMax'});
      await chrome.tabs.sendMessage(tab.id, {cvmax: true, kind: 'command', id: crypto.randomUUID(), action: 'progress', targetURL: tab.url, expiresAt: Date.now() + 2500, progress});
      state.progress = progress;
      await publish();
    }
    return {tabId: tab.id, url: tab.url, requestedURL: message.targetURL};
  }
  const tab = await chrome.tabs.get(message.tabId);
  validateBridgeCommand(message, {tab, blockedRuns});
  if (message.action === 'screenshot') {
    const [previous] = await chrome.tabs.query({active: true, windowId: tab.windowId});
    if (previous?.id !== tab.id) {
      await chrome.tabs.update(tab.id, {active: true});
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    let dataUrl;
    try { dataUrl = await chrome.tabs.captureVisibleTab(tab.windowId, {format: 'jpeg', quality: 45}); }
    finally { if (previous?.id && previous.id !== tab.id) await chrome.tabs.update(previous.id, {active: true}).catch(() => {}); }
    if (typeof dataUrl !== 'string' || !dataUrl.startsWith('data:image/jpeg;base64,') || dataUrl.length > 900_000) throw new Error('screenshot_receipt_invalid');
    return {dataUrl, tabId: tab.id, url: tab.url, capturedAt: Date.now(), scope: 'private_task'};
  }
  if (message.action === 'refresh') {
    state.active = {taskId: message.taskId, tabId: message.tabId, targetURL: message.targetURL, runId: message.runId};
    if (message.progress) state.progress = normalizeProgress(message.progress);
    await chrome.tabs.reload(message.tabId);
    const refreshed = await waitForLoad(message.tabId, message.expiresAt);
    if (normalizeOrigin(refreshed.url) !== normalizeOrigin(message.targetURL)) throw new Error('refresh_redirect_outside_origin');
    await chrome.scripting.executeScript({target: {tabId: refreshed.id}, files: ['evidence.js', 'content-logic.js', 'content.js']});
    state.active = {taskId: message.taskId, tabId: refreshed.id, targetURL: refreshed.url, runId: message.runId};
    await publish();
    return {accepted: true, tabId: refreshed.id, url: refreshed.url, refreshed: true};
  }
  if (message.action === 'inspect' && message.sectionSpec?.sectionKey) {
    const [receipt] = await chrome.scripting.executeScript({target:{tabId:message.tabId},func:expectedKey=>{
      const clean=value=>String(value??'').replace(/\s+/g,' ').trim(),marker=value=>clean(value).replace(/[＊*：:]+$/g,'').toLowerCase(),current=new URL(location.href),describe=element=>{const label=clean(element.textContent||element.value||element.getAttribute('aria-label')),role=clean(element.getAttribute('role')||(element.tagName==='BUTTON'?'button':'')).toLowerCase();if(!label||label.length>80)return null;if(role==='tab')return{sectionKey:`navigation:${marker(label)}`,target:null};const href=element.getAttribute('href');if(!href)return null;try{const target=new URL(href,current);if(target.origin!==current.origin||target.pathname!==current.pathname||target.search!==current.search||!target.hash||target.hash.startsWith('#/'))return null;return{sectionKey:`navigation:${marker(label)}`,target:target.hash}}catch{return null}},visible=element=>!!element.getClientRects().length&&getComputedStyle(element).visibility!=='hidden'&&getComputedStyle(element).display!=='none',candidates=[...document.querySelectorAll('button,a,[role="tab"]')].map(element=>({element,descriptor:describe(element)})).filter(item=>visible(item.element)&&item.descriptor?.sectionKey===expectedKey);if(candidates.length!==1)return{documentEpoch:globalThis.__cvmaxDocumentEpoch??null,sectionState:{found:false,ambiguous:candidates.length>1}};const {element,descriptor}=candidates[0],active=element.getAttribute('aria-selected')==='true'||element.getAttribute('aria-current')==='true'||element.matches('.active,[class*="active"]')||!!element.closest('.active,[class*="anchor-link-active"],[class*="tabs-tab-active"]')||descriptor.target&&location.hash===descriptor.target;return{documentEpoch:globalThis.__cvmaxDocumentEpoch??null,sectionState:{found:true,active:!!active,sectionKey:descriptor.sectionKey}}},args:[message.sectionSpec.sectionKey]});
    if(receipt?.error)throw Error(typeof receipt.error==='string'?receipt.error:receipt.error?.message||'section_inspection_failed');
    return receipt?.result;
  }
  const startingApplication = message.action === 'act' && message.actionSpec?.kind === 'start';
  const knownTabIds = startingApplication ? new Set((await chrome.tabs.query({windowId: tab.windowId})).map(item => item.id)) : null;
  const openCaptureKey = startingApplication ? crypto.randomUUID() : null;
  const openCaptureStartedAt = startingApplication ? Date.now() : null;
  if (startingApplication) await installWindowOpenCapture(message.tabId, openCaptureKey, openCaptureStartedAt);
  const injectContent = async () => {
    await chrome.scripting.executeScript({target:{tabId:message.tabId},func:generation=>{globalThis.__cvmaxRequestedGeneration=generation},args:[CONTENT_GENERATION]});
    await chrome.scripting.executeScript({target: {tabId: message.tabId}, files: ['evidence.js', 'content-logic.js', 'content.js']});
  };
  // Observations establish the content runtime. Actions use it directly so a
  // large form does not spend its short execution lease reinjecting the full
  // observer. Retry injection only when Chrome proves there was no receiver;
  // an ambiguous/closed response must never replay a possibly accepted action.
  if (message.action !== 'act') await injectContent();
  if (message.action === 'stop') blockedRuns.add(message.runId);
  else if (message.runId) state.active = {taskId: message.taskId, tabId: message.tabId, targetURL: message.targetURL, runId: message.runId};
  if (message.progress) state.progress = normalizeProgress(message.progress);
  if (state.progress?.terminal) state.active = null;
  const mainWorldActivation = message.action === 'act' && ['activate','start'].includes(message.actionSpec?.kind) && message.actionSpec?.activationMode === 'main_world_pointer';
  const contentMessage = mainWorldActivation ? {...message, actionSpec: {...message.actionSpec, activationMode: 'validate_only'}} : message;
  const invokeContent = async () => {
    if(message.action!=='act')return chrome.tabs.sendMessage(message.tabId,{...contentMessage,cvmax:true});
    const [receipt]=await chrome.scripting.executeScript({target:{tabId:message.tabId},func:async payload=>{try{if(typeof globalThis.__cvmaxCommand!=='function')throw Error('content_runtime_missing');return{cvmaxResult:await globalThis.__cvmaxCommand(payload)}}catch(error){return{cvmaxError:String(error?.message??error)}}},args:[{...contentMessage,cvmax:true}]});
    if(receipt?.error){const detail=typeof receipt.error==='string'?receipt.error:receipt.error?.message;throw Error(detail||'content_execution_failed')}
    if(receipt?.result?.cvmaxError)throw Error(receipt.result.cvmaxError);
    return receipt?.result?.cvmaxResult;
  };
  let result;
  try { result = await invokeContent(); }
  catch (error) {
    const noReceiver=/Receiving end does not exist|Could not establish connection|content_runtime_missing/i.test(String(error?.message??error));
    if(message.action==='act'&&noReceiver){await injectContent();result=await invokeContent()}
    else if (!startingApplication) throw error;
  }
  if (mainWorldActivation) {
    if (!result || result.error) throw new Error(result?.error ?? "activation_validation_missing");
    const activation=await activateInMainWorld(message.tabId,message.actionSpec.interactionAnchor??message.actionSpec.startAnchor);
    if(activation.activated!==true)throw new Error(activation.reason??'main_world_activation_failed');
    result={...result,accepted:true,mainWorldActivation:activation};
  }
  if (startingApplication) {
    const before = {url: message.targetURL, documentEpoch: message.actionSpec.beforeDocumentEpoch, fieldCount: message.actionSpec.beforeFieldCount, pageState: message.actionSpec.beforePageState, interactionDigest: message.actionSpec.beforeInteractionDigest};
    let settled = await chrome.tabs.get(message.tabId), observed = null, stableStart = null, stableSince = 0;
    try {
      while (Date.now() + 350 < message.expiresAt) {
        let opened = (await chrome.tabs.query({windowId: tab.windowId})).filter(item => !knownTabIds.has(item.id) && item.url && normalizeSiteBoundary(item.url) === normalizeSiteBoundary(message.targetURL));
        if (opened.length > 1) throw new Error('application_start_opened_ambiguous_tabs');
        if (!opened.length) {
          const captured = (await readWindowOpenCapture(message.tabId, openCaptureKey, openCaptureStartedAt)).filter(url => normalizeSiteBoundary(url) === normalizeSiteBoundary(message.targetURL));
          if (captured.length > 1) throw new Error('application_start_captured_ambiguous_urls');
          if (captured.length === 1) {
            const created = await chrome.tabs.create({url: captured[0], active: true, windowId: tab.windowId});
            opened = [await waitForLoad(created.id, message.expiresAt)];
          }
        }
        if (opened.length === 1) settled = opened[0];
        else settled = await chrome.tabs.get(message.tabId);
        if (normalizeSiteBoundary(settled.url) !== normalizeSiteBoundary(message.targetURL)) throw new Error('application_start_redirect_outside_site');
        if (settled.status === 'complete') {
          await chrome.scripting.executeScript({target: {tabId: settled.id}, files: ['evidence.js', 'content-logic.js', 'content.js']}).catch(() => {});
          observed = await chrome.tabs.sendMessage(settled.id, {...message, id: crypto.randomUUID(), action: 'observe', targetURL: settled.url, expiresAt: message.expiresAt, progress: undefined}).catch(() => null);
          if (applicationStartSettled(before, observed, {originalTabId: message.tabId, settledTabId: settled.id})) {
            const signature = JSON.stringify([settled.id, settled.url, observed?.documentEpoch ?? null, observed?.formStage ?? null, observed?.pageState ?? null]);
            if (signature === stableStart && Date.now() - stableSince >= 500) break;
            if (signature !== stableStart) { stableStart = signature; stableSince = Date.now(); }
          } else { stableStart = null; stableSince = 0; }
        }
        await new Promise(resolve => setTimeout(resolve, 250));
        settled = await chrome.tabs.get(settled.id);
      }
    } finally {
      await readWindowOpenCapture(message.tabId, openCaptureKey, openCaptureStartedAt, true).catch(() => {});
    }
    result = {...result, accepted: true, tabId: settled.id, url: settled.url, navigated: settled.id !== message.tabId || settled.url !== message.targetURL, openedNewTab: settled.id !== message.tabId, startObserved: applicationStartSettled(before, observed, {originalTabId: message.tabId, settledTabId: settled.id})};
    state.active = {taskId: message.taskId, tabId: settled.id, targetURL: settled.url, runId: message.runId};
  }
  if (result?.error) throw new Error(result.error);
  // The browser action receipt is the state-machine commit boundary. Progress
  // persistence is advisory and must never delay or erase that receipt.
  publish().catch(() => {});
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
    validateProtocol(message.protocol);
    state.phase = 'paired';
    state.lastError = null;
    state.selected = message.selected === true;
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
    send({kind: 'pair', token: validateToken(saved.cvmaxToken), protocol: EXTENSION_PROTOCOL, browser: {family: BROWSER_FAMILY}});
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

chrome.alarms.create(RECONNECT_ALARM, {periodInMinutes: 0.5});
chrome.alarms.onAlarm.addListener(alarm => {
  if (alarm.name !== RECONNECT_ALARM || userDisconnected || socket?.readyState === WebSocket.OPEN || socket?.readyState === WebSocket.CONNECTING) return;
  connect().catch(fail);
});
chrome.runtime.onStartup.addListener(() => connect().catch(fail));

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
      state.selected = false;
      await publish();
      return publicState();
    }
    throw new Error('unknown_popup_message');
  };
  action().then(value => reply({ok: true, value})).catch(error => reply({ok: false, error: error.message}));
  return true;
});

connect().catch(fail);
