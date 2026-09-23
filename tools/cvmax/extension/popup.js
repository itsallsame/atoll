import {DEFAULT_ENDPOINT} from './protocol.js';

const $ = selector => document.querySelector(selector);
const status = $('.status');
$('#version').textContent = ` v${chrome.runtime.getManifest().version}`;
const idleWorkflow = ['打开申请页','进入简历流程','选择填写方式','网站上传并解析简历','填写或纠正简历事实','核验简历事实'].map((label,index)=>({id:`idle_${index}`,label,status:'pending'}));

function renderWorkflow(value, terminal=false) {
  const projected=value?.steps?.length?value.steps:idleWorkflow,current=value?.current??null,currentIndex=projected.findIndex(step=>step.id===current),list=$('#workflow');
  list.replaceChildren(...projected.map((step,index)=>{const item=document.createElement('li'),mark=document.createElement('span'),label=document.createElement('span'),stepStatus=terminal&&step.status!=='skipped'?'done':step.status;item.className=stepStatus;mark.className='mark';mark.textContent=stepStatus==='done'?'✓':stepStatus==='skipped'?'–':String(index+1);label.textContent=step.label;item.append(mark,label);if(!terminal&&step.id===current)item.setAttribute('aria-current','step');return item}));
  $('#workflow-count').textContent=terminal?`${projected.length} / ${projected.length} 已完成`:currentIndex>=0?`${currentIndex+1} / ${projected.length}`:'等待任务';
}

async function request(message) {
  const response = await chrome.runtime.sendMessage(message);
  if (!response?.ok) throw new Error(response?.error || 'request_failed');
  return response.value;
}

function explain(phase) {
  return {
    not_paired: ['等待配对', '输入 CvMax 对话中提供的配对码'],
    connecting: ['正在连接', '连接本机 CvMax 服务'],
    paired: ['服务已连接', 'CvMax 可以自动打开并处理岗位链接'],
    disconnected: ['连接已断开', 'CvMax 会重新连接；页面不会自行续跑'],
    stopped: ['页面动作已停止', '返回 CvMax 对话查看任务最新状态'],
    error: ['连接出现问题', '返回 CvMax 对话进行诊断'],
  }[phase] || ['状态未知', '返回 CvMax 对话检查'];
}

async function render(value) {
  const [title, defaultDetail] = explain(value.phase);
  const detail = value.phase === 'paired' && value.selected === false ? `已连接，当前由 ${value.browserFamily || '其他浏览器'} 备用` : defaultDetail;
  $('#state').textContent = title;
  $('#detail').textContent = value.lastError || detail;
  status.className = `status ${value.connected ? 'connected' : ''} ${value.phase === 'error' ? 'error' : ''}`;
  $('#pairing').hidden = value.phase !== 'not_paired' && value.phase !== 'error';
  $('#running').hidden = !value.active && !value.progress && value.phase !== 'stopped';
  const progress = value.progress;
  const terminal=progress?.terminal===true,complete=terminal&&['complete','partial'].includes(progress?.phase);renderWorkflow(progress?.workflow,complete);
  $('#task').textContent = progress?.title || (value.active ? 'CvMax 正在处理当前页面' : '当前没有执行中的动作');
  $('#field').textContent = progress?.fieldLabel || '';
  $('#operation').textContent = progress?.current != null && progress?.total != null ? `本组 ${progress.current} / ${progress.total}` : '正在读取进度';
  $('#verified').textContent = `已核验 ${progress?.verified ?? 0} 项`;
  $('#owner').textContent = progress?.nextOwner ? `下一步：${progress.nextOwner}` : '';
  $('#running').classList.toggle('terminal',terminal);$('#handoff').hidden=!complete;$('#ended').hidden=!terminal;$('#stop').disabled=terminal;
  if(complete){$('#task').textContent='简历已处理';$('#handoff strong').textContent='下一步：检查剩余问题并手动提交';$('#handoff small').hidden=true;$('#ended').hidden=true}
}

async function load() {
  await render(await request({type: 'status'}));
}

$('#pair').addEventListener('click', async () => {
  $('#message').textContent = '';
  try { await render(await request({type: 'pair', endpoint: DEFAULT_ENDPOINT, token: $('#token').value.trim()})); }
  catch (error) { $('#message').textContent = error.message; }
});

$('#stop').addEventListener('click', async () => {
  $('#message').textContent = '';
  try { await render(await request({type: 'emergencyStop'})); }
  catch (error) { $('#message').textContent = error.message; }
});

chrome.runtime.onMessage.addListener(message => {
  if (message.type !== 'stateChanged') return;
  render(message.state).catch(error => { $('#message').textContent = error.message; });
});
load().catch(error => { $('#message').textContent = error.message; });
