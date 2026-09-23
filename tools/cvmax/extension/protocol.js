export const DEFAULT_ENDPOINT = 'ws://127.0.0.1:18833/';
export const EXTENSION_PROTOCOL = Object.freeze({current: 6, minimum: 6});

export function detectBrowserFamily(userAgent = '', brands = []) {
  const brandText = Array.isArray(brands) ? brands.map(item => item?.brand).filter(Boolean).join(' ') : '';
  const evidence = `${brandText} ${userAgent}`;
  if (/Microsoft Edge|Edg(?:A|iOS)?\//i.test(evidence)) return 'edge';
  if (/Google Chrome|Chrome\/|CriOS\//i.test(evidence)) return 'chrome';
  if (/Chromium/i.test(evidence)) return 'chromium';
  return 'unknown';
}

export function validateProtocol(peer) {
  if(!peer||!Number.isSafeInteger(peer.current)||!Number.isSafeInteger(peer.minimum))throw new Error('protocol_extension_missing');
  if(peer.current<EXTENSION_PROTOCOL.minimum||EXTENSION_PROTOCOL.current<peer.minimum)throw new Error('protocol_extension_incompatible');
  return true;
}

export function normalizeEndpoint(value = DEFAULT_ENDPOINT) {
  const url = new URL(value);
  const loopback = url.hostname === '127.0.0.1' || url.hostname === 'localhost' || url.hostname === '[::1]';
  if (url.protocol !== 'ws:' || !loopback || url.username || url.password || url.search || url.hash) {
    throw new Error('endpoint_must_be_loopback_ws');
  }
  if (url.pathname !== '/') throw new Error('endpoint_path_not_allowed');
  return url.href;
}

export function normalizeOrigin(value) {
  const url = new URL(value);
  if (!['http:', 'https:'].includes(url.protocol)) throw new Error('unsupported_page_origin');
  return url.origin;
}

export function normalizeSiteBoundary(value) {
  const url = new URL(value);
  if (!['http:', 'https:'].includes(url.protocol)) throw new Error('unsupported_page_origin');
  const host = url.hostname.toLowerCase();
  if (host === 'localhost' || /^\d+(?:\.\d+){3}$/.test(host) || host.includes(':')) return `${url.protocol}//${host}`;
  const parts = host.split('.');
  const compound = new Set(['com.cn','net.cn','org.cn','gov.cn','co.uk','org.uk','com.au','net.au','co.jp','com.sg']);
  const tail = parts.slice(-2).join('.');
  const size = compound.has(tail) ? 3 : 2;
  return `${url.protocol}//${parts.slice(-size).join('.')}`;
}

export function applicationStartObserved(before, after) {
  if (!before || !after) return false;
  return after.url !== before.url ||
    before.pageState !== 'resume_prerequisite' && after.documentEpoch !== before.documentEpoch ||
    (after.fields?.length ?? 0) > (before.fieldCount ?? 0) ||
    before.pageState !== 'resume_prerequisite' && after.pageState === 'resume_prerequisite' ||
    !!after.activeDialog ||
    (before.interactionDigest != null && after.evidence?.complete === true && after.evidence.interactionDigest !== before.interactionDigest) ||
    after.formStage === 'apply' ||
    after.loginRequired === true;
}

export function applicationStartSettled(before, after, {originalTabId, settledTabId} = {}) {
  if (!before || !after) return false;
  if (before.pageState !== 'resume_prerequisite') return applicationStartObserved(before, after);
  return settledTabId !== originalTabId ||
    after.url !== before.url ||
    after.formStage === 'apply' && (after.fields?.length ?? 0) > (before.fieldCount ?? 0);
}

export function validateToken(value) {
  if (typeof value !== 'string' || value.length < 6 || value.length > 256 || /\s/.test(value)) {
    throw new Error('invalid_pairing_token');
  }
  return value;
}

const PROGRESS_PHASES = new Set(['observing', 'uploading', 'native_parse', 'filling', 'verifying', 'waiting_user', 'paused', 'needs_reconcile', 'needs_attention', 'complete', 'partial', 'error', 'stopped']);
const NEXT_OWNERS = new Set(['CvMax', '你']);
const WORKFLOW_PATHS = new Set(['undecided', 'native', 'direct']);
const WORKFLOW_STATUSES = new Set(['done', 'current', 'pending', 'skipped', 'attention']);
const boundedText = (value, name, max) => {
  if (value == null) return null;
  if (typeof value !== 'string' || value.length > max) throw new Error(`invalid_progress_${name}`);
  return value;
};
const boundedCount = (value, name) => {
  if (value == null) return null;
  if (!Number.isSafeInteger(value) || value < 0 || value > 10_000) throw new Error(`invalid_progress_${name}`);
  return value;
};
const normalizeWorkflow = value => {
  if (!value || typeof value !== 'object' || value.version !== 1 || !WORKFLOW_PATHS.has(value.path) || typeof value.current !== 'string' || !Array.isArray(value.steps) || value.steps.length < 1 || value.steps.length > 8) throw new Error('invalid_progress_workflow');
  const ids = new Set(), steps = value.steps.map((step, index) => {
    if (!step || typeof step !== 'object' || typeof step.id !== 'string' || !/^[a-z][a-z_]{0,31}$/.test(step.id) || ids.has(step.id) || !WORKFLOW_STATUSES.has(step.status)) throw new Error(`invalid_progress_workflow_step:${index}`);
    ids.add(step.id);
    return {id: step.id, label: boundedText(step.label, `workflow_label_${index}`, 60), status: step.status};
  });
  if (!ids.has(value.current) || steps.filter(step => ['current', 'attention'].includes(step.status)).length !== 1 || !['current', 'attention'].includes(steps.find(step => step.id === value.current)?.status)) throw new Error('invalid_progress_workflow_current');
  return {version: 1, path: value.path, current: value.current, steps};
};

export function normalizeProgress(value) {
  if (!value || typeof value !== 'object' || !PROGRESS_PHASES.has(value.phase)) throw new Error('invalid_progress_phase');
  const progress = {
    taskId: boundedText(value.taskId, 'task_id', 100),
    revision: boundedCount(value.revision, 'revision'),
    phase: value.phase,
    title: boundedText(value.title, 'title', 80),
    detail: boundedText(value.detail, 'detail', 180),
    fieldLabel: boundedText(value.fieldLabel, 'field_label', 100),
    sectionLabel: boundedText(value.sectionLabel, 'section_label', 80),
    targetRef: boundedText(value.targetRef, 'target_ref', 100),
    current: boundedCount(value.current, 'current'),
    total: boundedCount(value.total, 'total'),
    verified: boundedCount(value.verified, 'verified'),
    nextOwner: value.nextOwner == null ? null : value.nextOwner,
    lastConfirmed: boundedText(value.lastConfirmed, 'last_confirmed', 120),
    workflow: normalizeWorkflow(value.workflow),
    terminal: value.terminal === true,
    finalSubmitAllowed: false,
  };
  if (progress.nextOwner && !NEXT_OWNERS.has(progress.nextOwner)) throw new Error('invalid_progress_next_owner');
  if (progress.current != null && progress.total != null && progress.current > progress.total) throw new Error('invalid_progress_range');
  return progress;
}

export function validateOpenCommand(message, now = Date.now()) {
  if (!message || message.kind !== 'command' || message.action !== 'open' || typeof message.id !== 'string' || !message.id) throw new Error('invalid_open_command');
  if (typeof message.targetURL !== 'string') throw new Error('invalid_open_target');
  if (typeof message.taskId !== 'string' || !message.taskId || typeof message.runId !== 'string' || !message.runId) throw new Error('task_capability_required');
  validateProtocol(message.protocol);
  const origin = normalizeOrigin(message.targetURL);
  if (!Number.isSafeInteger(message.expiresAt) || message.expiresAt < now || message.expiresAt > now + 30_000) throw new Error('command_lease_invalid');
  return {origin};
}

export function validateCurrentCommand(message, now = Date.now()) {
  if (!message || message.kind !== 'command' || message.action !== 'current' || typeof message.id !== 'string' || !message.id) throw new Error('invalid_current_command');
  if (!Number.isSafeInteger(message.expiresAt) || message.expiresAt < now || message.expiresAt > now + 10_000) throw new Error('command_lease_invalid');
  validateProtocol(message.protocol);
  return true;
}

export function validateBridgeCommand(message, {tab, blockedRuns, now = Date.now()}) {
  if (!message || message.kind !== 'command' || typeof message.id !== 'string' || !message.id) throw new Error('invalid_command');
  if (!Number.isSafeInteger(message.tabId) || message.tabId < 1 || tab?.id !== message.tabId) throw new Error('target_tab_mismatch');
  if (typeof message.targetURL !== 'string' || tab.url !== message.targetURL) throw new Error('target_url_mismatch');
  if (typeof message.taskId !== 'string' || !message.taskId) throw new Error('task_capability_required');
  validateProtocol(message.protocol);
  const origin = normalizeOrigin(message.targetURL);
  const maximumLease = message.action === 'refresh' || message.action === 'act' && message.actionSpec?.kind === 'start' ? 30_000 : 10_000;
  if (!Number.isSafeInteger(message.expiresAt) || message.expiresAt < now || message.expiresAt > now + maximumLease) throw new Error('command_lease_invalid');
  if (!['inspect','observe','screenshot','act','progress','refresh','stop'].includes(message.action)) throw new Error('unsupported_bridge_action');
  if (message.action !== 'observe' && (typeof message.runId !== 'string' || !message.runId)) throw new Error('run_required');
  if (message.progress != null) normalizeProgress(message.progress);
  if (message.action === 'progress' && message.progress == null) throw new Error('progress_required');
  if (message.action === 'act' && blockedRuns.has(message.runId)) throw new Error('run_stopped');
  return {origin};
}
