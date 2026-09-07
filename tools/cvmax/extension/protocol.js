export const DEFAULT_ENDPOINT = 'ws://127.0.0.1:18833/';

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

export function applicationStartObserved(before, after) {
  if (!before || !after) return false;
  return after.url !== before.url ||
    after.documentEpoch !== before.documentEpoch ||
    (after.fields?.length ?? 0) > (before.fieldCount ?? 0) ||
    after.pageState != null && after.pageState !== before.pageState ||
    after.formStage === 'apply' ||
    after.loginRequired === true;
}

export function validateToken(value) {
  if (typeof value !== 'string' || value.length < 6 || value.length > 256 || /\s/.test(value)) {
    throw new Error('invalid_pairing_token');
  }
  return value;
}

const PROGRESS_PHASES = new Set(['observing', 'uploading', 'native_parse', 'filling', 'verifying', 'waiting_user', 'paused', 'needs_reconcile', 'needs_attention', 'complete', 'partial', 'error', 'stopped']);
const NEXT_OWNERS = new Set(['CvMax', '你']);
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
  const origin = normalizeOrigin(message.targetURL);
  if (!Number.isSafeInteger(message.expiresAt) || message.expiresAt < now || message.expiresAt > now + 30_000) throw new Error('command_lease_invalid');
  return {origin};
}

export function validateCurrentCommand(message, now = Date.now()) {
  if (!message || message.kind !== 'command' || message.action !== 'current' || typeof message.id !== 'string' || !message.id) throw new Error('invalid_current_command');
  if (!Number.isSafeInteger(message.expiresAt) || message.expiresAt < now || message.expiresAt > now + 10_000) throw new Error('command_lease_invalid');
  return true;
}

export function validateBridgeCommand(message, {tab, allowedOrigins, blockedRuns, now = Date.now()}) {
  if (!message || message.kind !== 'command' || typeof message.id !== 'string' || !message.id) throw new Error('invalid_command');
  if (!Number.isSafeInteger(message.tabId) || message.tabId < 1 || tab?.id !== message.tabId) throw new Error('target_tab_mismatch');
  if (typeof message.targetURL !== 'string' || tab.url !== message.targetURL) throw new Error('target_url_mismatch');
  const origin = normalizeOrigin(message.targetURL);
  if (Array.isArray(allowedOrigins) && !allowedOrigins.includes(origin)) throw new Error('site_not_authorized');
  const maximumLease = message.action === 'act' && message.actionSpec?.kind === 'start' ? 15_000 : 10_000;
  if (!Number.isSafeInteger(message.expiresAt) || message.expiresAt < now || message.expiresAt > now + maximumLease) throw new Error('command_lease_invalid');
  if (!['observe', 'act', 'progress', 'stop'].includes(message.action)) throw new Error('unsupported_bridge_action');
  if (message.action !== 'observe' && (typeof message.runId !== 'string' || !message.runId)) throw new Error('run_required');
  if (message.progress != null) normalizeProgress(message.progress);
  if (message.action === 'progress' && message.progress == null) throw new Error('progress_required');
  if (message.action === 'act' && blockedRuns.has(message.runId)) throw new Error('run_stopped');
  return {origin};
}
