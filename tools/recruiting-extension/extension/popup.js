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
}

async function act(action) {
  $('#message').textContent = '';
  try { render(await action()); }
  catch (error) { $('#message').textContent = error.message; }
}

$('#pair').addEventListener('click', () => act(() => request({type: 'capture.configure',
  endpoint: $('#endpoint').value.trim(), token: $('#token').value.trim()})));

$('#begin').addEventListener('click', () => act(() => request({type: 'capture.begin.request', config: {
  sourceId: $('#source').value.trim(), recipeId: $('#recipe').value.trim(), recipeVersion: Number($('#version').value)}})));

for (const button of document.querySelectorAll('[data-role]')) {
  button.addEventListener('click', () => act(() => request({type: 'capture.arm.request', role: button.dataset.role})));
}

$('#submit').addEventListener('click', () => act(() => request({type: 'capture.submit.request',
  userConfirmed: $('#confirm').checked})));

chrome.runtime.onMessage.addListener(message => {
  if (message.type === 'capture.state') render(message.state);
});

request({type: 'capture.status'}).then(render).catch(error => { $('#message').textContent = error.message; });
