import {useEffect, useMemo, useState} from 'react';
import {
  ArrowRight, Browser, CheckCircle, CircleNotch, Desktop, FilePdf, LockKey,
  PlugsConnected, ShieldCheck, Stop, TerminalWindow, WarningCircle
} from '@phosphor-icons/react';

const phaseCopy={
  login:{state:'正在等你',fact:'登录页已打开，任务已保留',owner:'你：只在招聘网站完成登录',revision:12},
  verifying:{state:'正在核验',fact:'已收到“登录完成”，正在核对原页面',owner:'CvMax：核验账号、岗位和表单',revision:13},
  question:{state:'还缺 1 项事实',fact:'已核验 11 项；简历没有到岗日期',owner:'你：在对话里回答到岗日期',revision:14},
  answer_verifying:{state:'正在核验答案',fact:'已接收“一个月内”，正在写入并回读页面',owner:'CvMax：完成页面核验',revision:15},
  review:{state:'当前岗位待检查',fact:'12 项已核验，2 项保留网站原值',owner:'你：回原网站检查并自行提交',revision:16},
  stopped:{state:'任务已停止',fact:'新动作已阻断；旧回复不会恢复执行',owner:'无需操作；再次填写请新建任务',revision:17},
};

function StatusPanel({phase,mode}){
  const c=phaseCopy[phase];
  return <aside className="ai-status panel" aria-label="任务实时状态">
    <header><div><small>同一任务</small><strong>CV-240905-018</strong></div><span>rev.{c.revision}</span></header>
    <div className={`truth ${phase}`} role="status" aria-live="polite" tabIndex={-1}><i/><div><strong>{c.state}</strong><p>{c.fact} · 刚刚</p><b>下一步：{c.owner}</b></div></div>
    <section className="readonly-section"><small>处理范围</small><strong>{mode==='batch'?'3 个已确认岗位 · 严格串行':'字节跳动 · 数据分析师'}</strong><p>{mode==='batch'?'当前 1 / 3；其余岗位尚未开始':'jobs.bytedance.com · 当前申请页'}</p></section>
    <section className="readonly-section"><small>本次资料</small><strong>示例候选人_数据分析师.pdf</strong><p>版本 …8f21 · 当前任务使用 · 终态 7 天后删除草稿副本</p></section>
    <section className="readonly-section"><small>页面经验</small><strong>2 条已验证方法可用</strong><p>页面变化会立即停用旧方法；用户答案不进入经验。</p></section>
    <footer><ShieldCheck/> CvMax 在提交按钮前停止，最终提交由你完成。</footer>
  </aside>;
}

function Message({who,children,current=false}){return <div className={`chat-message ${who} ${current?'current':''}`}>{who==='agent'&&<span className="mini-mark">CV</span>}<div>{children}</div></div>}

function Composer({placeholder,onSend}){const [text,setText]=useState('');return <form className="conversation-compose" onSubmit={e=>{e.preventDefault();if(text.trim()){onSend(text.trim());setText('')}}}><textarea aria-label="给 AI 发送消息" rows="1" value={text} placeholder={placeholder} onChange={e=>setText(e.target.value)}/><button aria-label="发送消息"><ArrowRight/></button></form>}

function PhaseChips({active,labels=['准备','执行','交付']}){return <div className="phase-chips">{labels.map((x,i)=><span className={i<active?'done':i===active?'active':''} key={x}>{i+1} {x}</span>)}</div>}

function ApplyConversation({host,mode,phase,setPhase}){
  const external=host==='ai';
  return <section className="conversation-v2 panel">
    <header className="conversation-head">{external?<Desktop/>:<span className="brand-mark">CV</span>}<div><strong>{external?'你的 AI 桌面':'CvMax 私有频道'}</strong><p>{external?'CvMax Skill 已连接 Atoll':'你与获准的 Atoll Agent / 工具可处理'}</p></div><span><PlugsConnected/> 服务在线</span></header>
    <div className="chat-stream">
      <Message who="user"><p>{mode==='batch'?'用下载目录里的示例简历，把这三个已选岗位按顺序填好，不要提交。':mode==='link'?'用下载目录里的示例简历填这个字节岗位链接，不要提交。':'用下载目录里的示例简历填我现在打开的字节岗位，不要提交。'}</p><div className="inline-file"><FilePdf/><span>示例候选人_数据分析师.pdf<small>/Users/demo/Downloads · 发送给 CvMax 私有频道 · 提取姓名/联系方式/教育经历 · 仅本任务</small></span></div></Message>
      <Message who="agent"><strong>{external?'我已把委托交给 Atoll 内的 CvMax。':'任务已建立 · CV-240905-018'}</strong><p>{mode==='batch'?'已冻结 3 个岗位并去重，接下来严格串行。':'已确认唯一配对页面：字节跳动 · 数据分析师。'} 最终提交仍由你完成。</p><PhaseChips active={1}/></Message>
      <Message who="agent" current={phase==='login'}><strong>现在需要你完成登录</strong><p>我已打开原招聘页并保留任务。账号、密码和验证码只输入在招聘网站。</p>{phase==='login'&&<button onClick={()=>setPhase('verifying')}>我已登录 <ArrowRight/></button>}</Message>
      {['verifying','question','answer_verifying','review'].includes(phase)&&<Message who="user"><p>已登录。</p></Message>}
      {['verifying','question','answer_verifying','review'].includes(phase)&&<Message who="agent" current={phase==='verifying'}><strong>{phase==='verifying'?'我正在核验，不需要你操作。':'登录与原岗位页面已经核验。'}</strong><p>{phase==='verifying'?'收到的是你的完成声明；我会读取页面事实后再继续，不会直接当成成功。':'已确认登录态、岗位和表单一致，没有读取密码或验证码。'}</p></Message>}
      {['question','answer_verifying','review'].includes(phase)&&<Message who="agent" current={phase==='question'}><strong>Q-104 · 还缺一个事实：你预计多久可以到岗？</strong><p>简历里没有这项。回答默认只用于当前字节岗位，不写入长期资料。</p>{phase==='question'&&<div className="suggested-replies"><button onClick={()=>setPhase('answer_verifying')}>一个月内</button><button>我暂时不回答</button></div>}</Message>}
      {['answer_verifying','review'].includes(phase)&&<Message who="user"><p>一个月内，只用于这个岗位。</p></Message>}
      {phase==='answer_verifying'&&<Message who="agent" current><strong>答案已接收，正在重新核验</strong><p>我会写入当前岗位并读取网站回执；在页面确认前不会报告完成。</p></Message>}
      {phase==='review'&&<Message who="agent" current><strong>填写与核验完成，请回原网站检查</strong><p>12 项已核验，2 项保留网站原值；没有未确认动作。CvMax 已停在最终提交前。</p><PhaseChips active={2}/></Message>}
      {phase==='stopped'&&<Message who="agent" current><strong>本次任务已经停止</strong><p>已确认 5 项，0 项效果未知；后续页面动作和未开始岗位已取消。旧回复不会恢复执行。</p></Message>}
    </div>
    <div className="chat-controls"><button onClick={()=>setPhase('stopped')}><Stop/> 在对话中停止整个任务</button></div><Composer placeholder="直接告诉 AI 你的目标或当前问题…" onSend={text=>{if(text.includes('停止'))setPhase('stopped');else if(phase==='login')setPhase('verifying');else if(phase==='question')setPhase('answer_verifying')}}/>
  </section>;
}

function ExtensionView({phase,setPhase}){const c=phaseCopy[phase];return <div className="browser-view"><section className="job-site panel"><div className="browser-bar"><i/><i/><i/><span><LockKey/> jobs.bytedance.com / 数据分析师</span></div><div className="job-placeholder"><small>字节跳动校园招聘</small><h2>数据分析师申请表</h2><div><span>姓名</span><b>示例候选人</b><CheckCircle/></div><div><span>手机</span><b>138 **** 0000</b><CheckCircle/></div><div><span>到岗日期</span><b>{phase==='review'?'一个月内':'等待 CvMax'}</b>{phase==='review'?<CheckCircle/>:<CircleNotch/>}</div></div></section><aside className="plugin-card"><header><span className="brand-mark">CV</span><div><strong>CvMax</strong><p>CV-240905-018 · rev.{c.revision}</p></div><em>不提交</em></header><div className={`truth ${phase}`}><i/><div><strong>{c.state}</strong><p>{c.fact}</p><b>下一步：{c.owner}</b></div></div><section><small>当前岗位</small><strong>字节跳动 · 数据分析师</strong><p>插件只显示现场状态并执行 Agent 下发的受限动作。</p></section>{phase!=='stopped'&&phase!=='review'&&<button className="stop-button" onClick={()=>setPhase('stopped')}><Stop/> 立即停止页面动作</button>}<footer><ShieldCheck/> 不创建任务、不管理资料、不执行最终提交。</footer></aside></div>}

function InstallConversation({host,installStep,setInstallStep}){
  return <div className="surface-grid-v2"><section className="conversation-v2 panel">
    <header className="conversation-head">{host==='ai'?<Desktop/>:<span className="brand-mark">CV</span>}<div><strong>{host==='ai'?'你的 AI 桌面':'Atoll 助手'}</strong><p>AI-first 安装引导</p></div></header>
    <div className="chat-stream"><Message who="user"><p>帮我装好 CvMax。我主要用 {host==='ai'?'Codex 和 Chrome':'Atoll Web 和 Chrome'}。</p></Message><Message who="agent"><strong>可以，大致三步：</strong><PhaseChips active={installStep} labels={['检查服务','安装插件','连接验证']}/><p>{host==='ai'?'我会检查当前宿主并安装/验证 Skill 与 CLI；':'Atoll 对话无需 Skill/CLI；'}只有 Chrome 插件和站点权限需要你亲自确认。</p></Message><Message who="agent" current><strong>{installStep===0?'正在检查并配置服务':installStep===1?'现在请安装并打开 Chrome 插件':'连接检查完成'}</strong><p>{installStep===0?'当前由我完成，不需要你操作。':installStep===1?'配对码 CV-4821（10 分钟有效）。本次授权 jobs.bytedance.com 的表单读取与获准字段写入，任务结束可撤销。安装后在对话里告诉我“已完成”。':'身份、频道、Agent、应用服务、Skill/CLI、插件与合成页面已逐项核验。'}</p>{installStep===1&&<button onClick={()=>setInstallStep(2)}>我已完成 <ArrowRight/></button>}</Message></div>
    <Composer placeholder="遇到问题，直接描述你看到的状态…" onSend={text=>{if(text.includes('完成'))setInstallStep(2)}}/>
  </section><aside className="ai-status panel"><header><div><small>安装状态</small><strong>{installStep===2?'可以开始':'正在配置'}</strong></div></header><section className="readonly-section"><small>当前一步</small><strong>{installStep===0?'AI 检查服务与宿主':installStep===1?'你安装 Chrome 插件':'AI 完成 doctor'}</strong><p>{installStep===1?'完成这一项后，我再继续。':'最近确认：刚刚'}</p></section><section className="readonly-section"><small>访问范围</small><strong>Atoll 身份 · CvMax 私有频道</strong><p>jobs.bytedance.com · 当前任务授权 · 任务结束可撤销。</p></section><footer><ShieldCheck/> 这里只读展示状态；安装与排障通过对话逐步完成。</footer></aside></div>
}

export function App(){
  const [scene,setScene]=useState('apply'),[host,setHost]=useState('web'),[mode,setMode]=useState('current'),[phase,setPhase]=useState('login'),[installStep,setInstallStep]=useState(0);
  useEffect(()=>{const frame=requestAnimationFrame(()=>document.querySelector('[role="status"]')?.focus());return()=>cancelAnimationFrame(frame)},[phase]);
  useEffect(()=>{if(!['verifying','answer_verifying'].includes(phase))return;const timer=setTimeout(()=>setPhase(phase==='verifying'?'question':'review'),1100);return()=>clearTimeout(timer)},[phase]);
  useEffect(()=>{if(scene!=='install'||installStep!==0)return;const timer=setTimeout(()=>setInstallStep(1),1100);return()=>clearTimeout(timer)},[scene,installStep]);
  const body=useMemo(()=>scene==='install'?<InstallConversation host={host} installStep={installStep} setInstallStep={setInstallStep}/>:host==='extension'?<ExtensionView phase={phase} setPhase={setPhase}/>:<div className="surface-grid-v2"><ApplyConversation host={host} mode={mode} phase={phase} setPhase={setPhase}/><StatusPanel phase={phase} mode={mode}/></div>,[scene,host,mode,phase,installStep]);
  return <div className="ai-first-shell"><header className="review-bar"><div className="product"><span className="brand-mark">CV</span><div><strong>CvMax</strong><small>Atoll 上的 AI-first 求职申请服务</small></div></div><div className="demo-controls"><span>原型场景</span><button aria-pressed={scene==='apply'} onClick={()=>setScene('apply')}>申请任务</button><button aria-pressed={scene==='install'} onClick={()=>setScene('install')}>对话式安装</button><span>查看界面</span><button aria-pressed={host==='web'} onClick={()=>setHost('web')}><Browser/> Atoll 对话</button><button aria-pressed={host==='ai'} onClick={()=>setHost('ai')}><Desktop/> 外部 AI</button><button aria-pressed={host==='extension'} onClick={()=>{setHost('extension');setScene('apply')}}><PlugsConnected/> 招聘页插件</button></div><span className="prototype-tag">设计原型</span></header><main className="ai-first-content"><div className="context-line"><div><span className="section-kicker">AI-FIRST</span><h1>{scene==='install'?'安装也通过对话完成':'用户只说目标，系统自己执行'}</h1></div>{scene==='apply'&&<div className="mode-toggle"><button aria-pressed={mode==='current'} onClick={()=>{setMode('current');setPhase('login')}}>当前页面</button><button aria-pressed={mode==='link'} onClick={()=>{setMode('link');setPhase('login')}}>单个链接</button><button aria-pressed={mode==='batch'} onClick={()=>{setMode('batch');setPhase('login')}}>多个岗位</button></div>}<p><ShieldCheck/> 远端 Web 只展示对话、事实和结果。</p></div>{body}</main></div>;
}
