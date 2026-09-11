const $ = selector => document.querySelector(selector);

async function request(message) {
  const response = await chrome.runtime.sendMessage(message);
  if (!response?.ok) throw new Error(response?.error || 'extension_request_failed');
  return response.value;
}

function phaseLabel(phase) {
  if (phase === 'completed') return '已提交';
  if (phase === 'capturing' || phase?.startsWith('mark_')) return '标注中';
  if (phase === 'ready') return '已连接';
	if (phase === 'profile_repair') return '等待登录';
	if (phase === 'profile_verifying') return '正在验证';
  if (phase === 'error') return '需要处理';
  return '未连接';
}

function render(state) {
  $('#phase').textContent = phaseLabel(state.phase);
  $('#message').textContent = state.lastError || (state.phase?.startsWith('mark_') ? '请回到网页并点击目标元素' : '');
  for (const button of document.querySelectorAll('[data-role]')) {
    button.classList.toggle('done', Boolean(state.marks?.[button.dataset.role]));
  }
  $('#result').textContent = state.result ? JSON.stringify(state.result, null, 2) : '';
	const repairing = state.profileTask?.kind === 'profile_repair';
	$('#profile-repair').hidden = !repairing;
	$('#capture').hidden = repairing || state.profileTask?.kind === 'profile_verification';
	$('#profile-domain').textContent = repairing ? state.profileTask.canary_url : '';
}

function renderKind() {
  const kind = $('#kind').value;
  $('#sample-job-row').hidden = kind !== 'detail';
  for (const button of document.querySelectorAll('[data-role]')) {
    button.hidden = Boolean(button.dataset.kind) && button.dataset.kind !== kind;
  }
  $('#capture-hint').textContent = kind === 'detail'
    ? '先打开已入库样本 Job 的确切详情 URL，再标注标题、描述和可选字段。'
    : '依次点按钮，再回到页面点击对应元素。字段必须位于选中的岗位卡片内。';
  $('#confirm-copy').textContent = kind === 'detail'
    ? '我确认当前页面就是该样本 Job 的详情页，并确认所选字段含义。'
    : '我确认列表按最新活动时间倒序，历史岗位更新后会重新置顶；置顶项已由该选择排除。';
  $('#confirm').checked = false;
}

async function act(action) {
  $('#message').textContent = '';
  try { render(await action()); }
  catch (error) { $('#message').textContent = error.message; }
}

$('#pair').addEventListener('click', () => act(() => request({type: 'capture.configure',
  endpoint: $('#endpoint').value.trim(), token: $('#token').value.trim(),
  profileId: $('#profile-id').value.trim(), profileToken: $('#profile-token').value.trim()})));

$('#begin').addEventListener('click', () => act(() => request({type: 'capture.begin.request', config: {
  sourceId: $('#source').value.trim(), recipeId: $('#recipe').value.trim(), recipeVersion: Number($('#version').value),
  recipeKind: $('#kind').value, sampleJobId: $('#sample-job').value.trim()}})));

for (const button of document.querySelectorAll('[data-role]')) {
  button.addEventListener('click', () => act(() => request({type: 'capture.arm.request', role: button.dataset.role})));
}

$('#submit').addEventListener('click', () => act(() => request({type: 'capture.submit.request',
  userConfirmed: $('#confirm').checked})));

$('#profile-complete').addEventListener('click', () => act(() => request({type: 'profile.complete.request',
	userConfirmed: $('#profile-confirm').checked})));

chrome.runtime.onMessage.addListener(message => {
  if (message.type === 'capture.state') render(message.state);
});

request({type: 'capture.status'}).then(render).catch(error => { $('#message').textContent = error.message; });
$('#kind').addEventListener('change', renderKind);
renderKind();
