const FIELD_ACTIONS = new Set(['fill', 'select', 'choose', 'check']);
const ATTENTION_PHASES = new Set(['waiting_user', 'needs_reconcile', 'needs_attention', 'error']);

const STEP_LABELS = Object.freeze({
  open: '打开申请页',
  enter: '进入申请流程',
  choose: '选择填写方式',
  native: '上传并解析简历',
  fill: '填写或纠正信息',
  verify: '核验填写结果',
});

export function workflowProgress({
  phase = 'observing',
  terminal = false,
  hasTab = false,
  formStage = null,
  actionKind = null,
  resumePhase = 'not_started',
  hasNativeAttempt = false,
  hasFieldActions = false,
  outcome = null,
} = {}) {
  const entered = formStage === 'apply' || formStage === 'resume' || formStage === 'resume_prerequisite';
  const nativePending = ['confirmation_required', 'parse_pending'].includes(resumePhase);
  const nativeParsed = resumePhase === 'parsed';
  const directPath = resumePhase === 'unavailable' || !hasNativeAttempt && hasFieldActions;
  const nativePath = !directPath && (hasNativeAttempt || nativePending || nativeParsed || ['uploading', 'native_parse'].includes(phase));
  const path = directPath ? 'direct' : nativePath ? 'native' : 'undecided';
  const stoppedEarly = terminal && ['failed', 'skipped', 'cancelled'].includes(outcome);
  let current = !hasTab ? 'open' : !entered ? 'enter' : 'choose';
  if (terminal && !stoppedEarly) current = 'verify';
  else if (actionKind === 'upload' || actionKind === 'native_parse' || nativePending || phase === 'uploading' || phase === 'native_parse') current = 'native';
  else if (FIELD_ACTIONS.has(actionKind) || phase === 'filling' && entered) current = 'fill';
  else if (nativeParsed || hasFieldActions && phase === 'verifying') current = 'verify';

  const ids = Object.keys(STEP_LABELS), currentIndex = ids.indexOf(current);
  const steps = ids.map((id, index) => {
    let status = index < currentIndex ? 'done' : index === currentIndex ? 'current' : 'pending';
    if (id === 'native' && path === 'direct' && currentIndex >= ids.indexOf('fill')) status = 'skipped';
    if (id === 'fill' && path === 'native' && !hasFieldActions && currentIndex >= ids.indexOf('verify')) status = 'skipped';
    if (stoppedEarly && index > 0 && index !== currentIndex) status = 'skipped';
    if (index === currentIndex && ATTENTION_PHASES.has(phase)) status = 'attention';
    return {id, label: STEP_LABELS[id], status};
  });
  return {version: 1, path, current, steps};
}
