export const SCENARIOS = { fill: '资料补充', connection: '连接修复', login: '登录继续', stop: '停止接管' };
export const STORAGE_KEY = 'cvmax-ux-prototype-v1';
const stages = new Set(['missing','checking','service_wait','connection_verify','login_wait','login_verify','filling','verifying','complete','uploading','stopping','stopped','manual','manual_verify','partial','stopped_done','paused']);
export function initialState(scenario = 'fill') {
  if (!SCENARIOS[scenario]) scenario = 'fill';
  return { version: 1, scenario, stage: {fill:'missing',connection:'checking',login:'login_wait',stop:'uploading'}[scenario],
    epoch: 0, revision: 1, at: Date.now(), stopped: false, resumeStage: null, service: false, loggedIn: false, account: '林知远',
    salary: scenario === 'stop' ? '30000' : '', relocation: scenario === 'stop' ? '可以讨论' : '', save: false,
    school: scenario === 'stop' ? '上海大学' : '上海交通大学', upload: scenario === 'stop' ? 'pending' : 'confirmed',
    error: '', note: '', fields: { name:'林知远', email:'lin@example.com', phone:'138 0000 0000', company:'星河软件' },
    history: [{text: scenario === 'connection' ? '正在检查演示连接；申请尚未开始' : scenario === 'login' ? '等待登录；申请尚未开始' : scenario === 'stop' ? '演示上传已发出，尚未收到回执' : '基本信息与工作经历已核验', time:Date.now()}] };
}
function editedStage(s) {
  if(['complete','verifying'].includes(s.stage)) return 'missing';
  if(['stopped_done','manual_verify'].includes(s.stage)) return 'manual';
  return s.stage;
}
function validBasics(s) {return s.fields.name.trim() && /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(s.fields.email) && s.fields.phone.trim() && s.fields.company.trim();}
function update(s, values, message) {
  return { ...s, ...values, revision:s.revision+1, at:Date.now(), error:values.error ?? '',
    history: message ? [...s.history, {text:message,time:Date.now()}].slice(-12) : s.history };
}
export function reducer(s, a) {
  if (a.type === 'RESET') return {...initialState(a.scenario || s.scenario),epoch:s.epoch+1};
  // Delayed effects are bound to the observed incarnation of this demo run.
  if (a.epoch !== undefined && a.epoch !== s.epoch) return s;
  switch (a.type) {
    case 'FIELD': {
      if (!['name','email','phone','company'].includes(a.key)) return s;
      return update(s,{fields:{...s.fields,[a.key]:a.value},stage:editedStage(s)},'保留你的修改；完成状态需要重新核验');
    }
    case 'DRAFT':
      if (!['salary','relocation','save','school','account'].includes(a.key)) return s;
      return update(s,{[a.key]:a.value,stage:editedStage(s)});
    case 'CHECK_DONE': return s.stage==='checking' ? update(s,{stage:'service_wait'},'已确认：演示连接未开启。现在需要你开启连接') : s;
    case 'ENABLE_SERVICE': return update(s,{service:true,stage:s.stage==='service_wait'?'connection_verify':s.stage},'演示连接已开启；正在重新核验');
    case 'CHECK_CONNECTION': if(s.stage!=='service_wait'||s.stopped)return s; return s.service ? update(s,{stage:'connection_verify'},'重新检查连接') : update(s,{error:'还未检测到连接。现在只需在左侧点击“开启演示连接”。'});
    case 'CONNECTION_OK': return s.stage==='connection_verify'&&!s.stopped ? update(s,{stage:'filling'},'连接已核验，助手继续填写') : s;
    case 'LOGIN': {
      if (s.account!=='林知远') return update(s,{loggedIn:false,error:'当前是另一个演示账号。请先在左侧切换为林知远，再登录。'},'账号不匹配，未写入申请资料');
      return update(s,{loggedIn:true,stage:s.stage==='login_wait'&&!s.stopped?'login_verify':s.stage},s.stage==='paused'?'登录已完成；你已暂停，等待你继续':'登录完成，正在确认原岗位与表单');
    }
    case 'CHECK_LOGIN':
      if(s.stage!=='login_wait') return s;
      return s.loggedIn ? update(s,{stage:'login_verify'}) : update(s,{error:'还没有检测到登录。请先在左侧演示页面完成登录；无需在助手里输入密码。'});
    case 'LOGIN_OK': return s.stage==='login_verify'&&!s.stopped ? update(s,{stage:'filling'},'已确认账号及原申请表单，继续填写') : s;
    case 'FILL_DONE': return s.stage==='filling'&&!s.stopped ? update(s,{stage:'missing'},'可独立填写的内容已完成，还需两个答案') : s;
    case 'APPLY': {
      if(!['missing','partial'].includes(s.stage)||s.stopped) return s;
      if(!s.salary || !Number.isFinite(Number(s.salary)) || Number(s.salary)<=0 || Number(s.salary)>10000000) return update(s,{error:'请填写有效的税前月薪金额，例如 30000。'});
      if(!['接受','不接受','可以讨论'].includes(s.relocation)) return update(s,{error:'请选择是否接受异地办公。'});
      if(!validBasics(s)) return update(s,{error:'左侧基本信息还有空缺或邮箱格式不正确，请先修改。'});
      if(s.school!=='上海交通大学') return update(s,{error:'学校与演示资料不一致。请先在左侧选择“上海交通大学”，再核验。'});
      return update(s,{stage:'verifying'},'正在写入并核验补充答案');
    }
    case 'VERIFY_DONE': return s.stage==='verifying'&&!s.stopped ? update(s,{stage:'complete'},'前端核验通过；填写完成，尚未提交') : s;
    case 'UPLOAD_DONE': return s.stage==='uploading'&&!s.stopped ? update(s,{stage:'manual',upload:'confirmed',stopped:true,epoch:s.epoch+1},'附件已确认；学校需你核对，已暂停自动填写') : s;
    case 'PAUSE':
      if(s.stopped||['paused','complete','stopped','stopping','stopped_done'].includes(s.stage)) return s;
      return update(s,{stage:'paused',resumeStage:s.stage,epoch:s.epoch+1},'你已暂停本申请；不会自动继续');
    case 'RESUME':
      if(s.stage!=='paused'||s.stopped) return s;
      return update(s,{stage:s.resumeStage==='login_wait'?(s.loggedIn?'login_verify':'login_wait'):s.resumeStage==='service_wait'?(s.service?'connection_verify':'service_wait'):s.resumeStage||'missing',epoch:s.epoch+1},'你已选择继续，先重新核验当前状态');
    case 'STOP':
      if(s.stopped) return s;
      return update(s,{stopped:true,stage:s.upload==='pending'?'stopping':'stopped',epoch:s.epoch+1},s.upload==='pending'?'已阻止后续动作；只读核对最后一次上传':'已停止本申请；已填写的内容保留');
    case 'STOP_CHECK_DONE': return s.stage==='stopping' ? update(s,{stage:'stopped',upload:'unknown'},'检查已结束：附件结果仍未知，不会重新上传') : s;
    case 'TAKEOVER': return s.stopped&&s.stage==='stopped' ? update(s,{stage:'manual'},'已定位学校字段；现在由你修改') : s;
    case 'VERIFY_MANUAL':
      if(s.stage!=='manual') return s;
      if(!validBasics(s) || !Number.isFinite(Number(s.salary)) || Number(s.salary)<=0 || !['接受','不接受','可以讨论'].includes(s.relocation)) return update(s,{error:'网页上的基本信息或补充答案有空缺，请补齐后再核验。'});
      if(s.school!=='上海交通大学') return update(s,{error:'当前仍不是资料中的“上海交通大学”。请从左侧学校选项中选择，再核验。'});
      return update(s,{stage:'manual_verify'},'学校已选择，助手只读核验，不继续自动填写');
    case 'MANUAL_DONE': return s.stage==='manual_verify' ? update(s,{stage:s.upload==='confirmed'?'stopped_done':'partial'},s.upload==='confirmed'?'学校与附件已核验；自动填写仍保持停止':'学校已核验；附件结果未知，需在网页确认') : s;
    case 'REVEAL_UPLOAD': return ['partial','stopped','manual'].includes(s.stage) ? update(s,{upload:'confirmed',stage:s.stage==='partial'?'stopped_done':s.stage},'演示站点回执已显示：附件上传完成；未再次上传') : s;
    default: return s;
  }
}
export function restoreState(raw) {
  try {
    let s = JSON.parse(raw);
    if(!s || !Number.isFinite(s.epoch) || !Number.isFinite(s.at) || !['name','email','phone','company'].every(k=>typeof s.fields?.[k]==='string') || typeof s.salary!=='string' || typeof s.school!=='string' || !s.history?.every(h=>typeof h.text==='string' && Number.isFinite(h.time)) || s.version!==1 || !SCENARIOS[s.scenario] || !stages.has(s.stage) || !Array.isArray(s.history) || !s.fields) return initialState();
    s={...initialState(s.scenario),...s,epoch:s.epoch+1};
    if(s.stage==='stopping') return update(s,{stage:'stopped',upload:'unknown',stopped:true},'页面已恢复；停止状态保留，附件结果未知');
    if(['checking','connection_verify','login_verify','filling','verifying','uploading','manual_verify'].includes(s.stage)) {
      if(s.stopped) return update(s,{stage:'manual'},'已恢复停止记录，请先重新核验');
      return update(s,{stage:'paused',resumeStage:s.stage},'页面已恢复；自动执行已暂停，等待你继续');
    }
    return s;
  } catch {return initialState();}
}
export const effects = {
  checking:[700,'CHECK_DONE'], connection_verify:[700,'CONNECTION_OK'], login_verify:[700,'LOGIN_OK'],
  filling:[1100,'FILL_DONE'], verifying:[850,'VERIFY_DONE'], uploading:[25000,'UPLOAD_DONE'],
  stopping:[1800,'STOP_CHECK_DONE'], manual_verify:[750,'MANUAL_DONE']
};
export function view(s) {
  const v={
    missing:['还差 2 项，就填好了','等你补充','请补充下面两个答案，其他能填写的内容已完成。',1],
    checking:['先把连接恢复好','助手处理中','我正在检查演示连接，你暂时无需操作。',0],
    service_wait:['现在，请开启连接','等你操作','申请尚未开始。在左侧演示窗口开启连接后，我会自动重新检查。',1],
    connection_verify:['连接已开启，正在核验','助手处理中','我来确认连接状态，暂时无需操作。',2],
    login_wait:['这个岗位需要先登录','等你登录','我已定位左侧登录页。现在请完成演示登录，我会检测后继续。',1],
    login_verify:['登录完成，我来核验','助手处理中','正在确认账号与原申请表单，你暂时无需操作。',2],
    filling:['正在继续填写','助手处理中','我正在填写基本信息与工作经历。',2],
    verifying:['正在核验你的答案','助手处理中','我来确认页面已接受填写；你暂时无需操作。',2],
    complete:['填好了，交给你检查','待你检查提交','20 项前端核验通过。请检查左侧内容，最终提交由你完成。',2],
    uploading:['正在上传简历','助手处理中','上传已经发出，尚未收到回执。你可以随时暂停或停止。',0],
    stopping:['已停止后续操作','助手只读核对','我只核对在途上传，不会派发新动作。稍后会给出明确结果。',0],
    stopped:['已停止，不会自动继续','已停止','已填内容保留。附件结果未确认，停止不代表撤销了上传。',0],
    manual:['现在，由你修改学校','等你修改','自动填写保持停止。请在左侧选择“上海交通大学”，我再核验。',1],
    manual_verify:['我来核验你的修改','助手只读核验','正在检查学校选中状态，不会重新上传附件。',2],
    partial:['学校已核验，还差附件','部分完成','附件结果仍未知。请查看左侧站点回执，自动填写保持停止。',2],
    stopped_done:['核验完成，仍保持停止','待你检查提交','学校和附件已核验。没有重新上传，最终提交由你完成。',2],
    paused:['已暂停，等你继续','已暂停','不会自动继续。已填写内容保留；重新打开或完成登录也不会解除暂停。',1]
  }[s.stage];
  const steps=s.scenario==='connection'?['检查连接','你来开启','我来继续']:s.scenario==='login'?['打开页面','你来登录','我来继续']:s.scenario==='stop'?['停止操作','你来修改','我来核验']:['自动填写','补充信息','检查结果'];
  return {title:s.stage==='missing'&&s.salary&&s.relocation?'答案已补充，等待核验':v[0],owner:v[1],description:s.stage==='stopped'&&s.upload==='confirmed'?'已填内容保留。自动填写已终止，你可以在网页继续手动修改。':v[2],step:v[3],steps,busy:!!effects[s.stage],done:['complete','stopped_done'].includes(s.stage)};
}
