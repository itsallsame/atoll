const FRAME_VERSION = 5;
const CHANNEL_ID = 'c0';
const $ = (id) => document.getElementById(id);
const state = {
  view: 'overview', connected: false, wire: null, actor: '', steward: '', session: '',
  data: null, sources: new Map(), selectedCompany: '', filter: '', refreshTimer: 0, systemLoading: false,
};

class AtollWire {
  constructor() {
    this.socket = null;
    this.generation = 0;
    this.counter = 0;
    this.attached = false;
    this.pending = new Map();
    this.receipts = new Map();
    this.attachRef = '';
    this.reconnectTimer = 0;
  }

  connect() {
    window.clearTimeout(this.reconnectTimer);
    this.generation += 1;
    const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:';
    this.socket = new WebSocket(`${scheme}//${location.host}/ws`);
    setConnection('connecting');
    this.socket.addEventListener('open', () => {
      this.attached = false;
      this.attachRef = this.sendFrame('attach', {
        since: {}, focus: CHANNEL_ID, history_protocol: FRAME_VERSION,
        generation: this.generation, label: 'Staircase 招聘数据指挥台',
      }, true);
    });
    this.socket.addEventListener('message', (event) => this.receive(event));
    this.socket.addEventListener('close', () => {
      this.attached = false;
      setConnection('error');
      rejectPending('连接已经断开');
      this.reconnectTimer = window.setTimeout(() => this.connect(), 1800);
    });
    this.socket.addEventListener('error', () => setConnection('error'));
  }

  sendFrame(type, payload, beforeAttach = false) {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN || (!this.attached && !beforeAttach)) throw new Error('节点连接尚未就绪');
    const ref = `${type}-${++this.counter}`;
    this.socket.send(JSON.stringify({ v: FRAME_VERSION, frame_type: type, ref, payload }));
    return ref;
  }

  async receive(event) {
    let frame;
    try { frame = JSON.parse(event.data); } catch { return; }
    if (frame.v !== FRAME_VERSION) return;
    if (frame.frame_type === 'receipt') {
      if (frame.ref === this.attachRef) {
        this.attached = true;
        state.session = frame.payload?.session || '';
        setConnection('connected');
        try {
		  // The attach receipt is deliberately written before the server launches
		  // its feed pump. Yield briefly so the first request response cannot land
		  // in the narrow receipt-to-subscription hand-off window.
		  await new Promise((resolve) => window.setTimeout(resolve, 250));
          await discoverActors();
        } catch (error) { showFatal('无法进入招聘数据指挥台', error.message); }
        return;
      }
      const receipt = this.receipts.get(frame.ref);
      if (receipt) {
        this.receipts.delete(frame.ref);
        window.clearTimeout(receipt.timer);
        receipt.resolve(frame.payload || {});
      }
      return;
    }
    if (frame.frame_type === 'error') {
      const pending = [...this.pending.values()].find((item) => item.ref === frame.ref);
      if (pending) {
        this.pending.delete(pending.id);
        window.clearTimeout(pending.timer);
        pending.reject(new Error(frame.payload?.detail || frame.payload?.code || '请求失败'));
      } else if (this.receipts.has(frame.ref)) {
        const receipt = this.receipts.get(frame.ref);
        this.receipts.delete(frame.ref);
        window.clearTimeout(receipt.timer);
        receipt.reject(new Error(frame.payload?.detail || frame.payload?.code || '请求失败'));
      } else if (!this.attached) {
        showFatal('需要先登录', frame.payload?.detail || '当前浏览器还没有 Atoll 登录会话。');
      }
      return;
    }
    if (frame.frame_type !== 'feed' || Number(frame.payload?.generation) !== this.generation) return;
    const envelope = frame.payload?.envelope;
    if (!envelope || envelope.kind !== 'response' || !envelope.parent_id) return;
    const pending = this.pending.get(envelope.parent_id);
    if (!pending) return;
    const status = envelope.payload?.status;
    if (status !== 'completed' && status !== 'failed' && status !== 'cancelled') return;
    this.pending.delete(envelope.parent_id);
    window.clearTimeout(pending.timer);
    if (status === 'completed') pending.resolve(envelope.payload || {});
    else pending.reject(new Error(envelope.payload?.detail || envelope.payload?.reason || '业务请求失败'));
  }

  request(msgType, payload, audience, timeout = 30000) {
    if (!audience) return Promise.reject(new Error('未找到可处理该请求的系统成员'));
    const id = crypto.randomUUID();
    const body = msgType === 'agent.ask' && state.session && payload && payload.origin === undefined
      ? { ...payload, origin: { session: state.session, label: 'Staircase 招聘数据指挥台' } } : payload;
    return new Promise((resolve, reject) => {
      let ref;
      try {
        ref = this.sendFrame('submit', {
          channel_id: CHANNEL_ID, id, msg_type: msgType, kind: 'request', payload: body,
          audience: [audience], visibility: 'public',
        });
      } catch (error) { reject(error); return; }
      const timer = window.setTimeout(() => { this.pending.delete(id); reject(new Error('请求超时，请稍后重试')); }, timeout);
      this.pending.set(id, { id, ref, resolve, reject, timer });
    });
  }

  control(type, payload, timeout = 10000) {
    return new Promise((resolve, reject) => {
      let ref;
      try { ref = this.sendFrame(type, payload); } catch (error) { reject(error); return; }
      const timer = window.setTimeout(() => { this.receipts.delete(ref); reject(new Error(`${type} 回执超时`)); }, timeout);
      this.receipts.set(ref, { resolve, reject, timer });
    });
  }
}

function rejectPending(detail) {
  if (!state.wire) return;
  for (const pending of state.wire.pending.values()) {
    window.clearTimeout(pending.timer);
    pending.reject(new Error(detail));
  }
  state.wire.pending.clear();
}

async function discoverActors() {
  try {
    const catalog = await state.wire.request('system.member.list', {}, 'system');
    const actors = Array.isArray(catalog.actors) ? catalog.actors : [];
    state.actor = actors.find((actor) => actor.id?.startsWith('tool:recruiting-control:'))?.id ||
      actors.find((actor) => /recruiting/i.test(`${actor.name || ''} ${actor.description || ''}`))?.id || '';
    state.steward = actors.find((actor) => actor.kind === 'agent' && actor.present !== false)?.id || '';
    if (!state.actor) throw new Error('当前频道没有运行中的 Recruiting Actor');
    await refresh();
    window.clearInterval(state.refreshTimer);
    state.refreshTimer = window.setInterval(() => refresh(true), 30000);
  } catch (error) {
    showFatal('无法进入招聘数据指挥台', error.message);
  }
}

async function refresh(silent = false) {
  if (!silent) showLoading();
  try {
    const result = await state.wire.request('recruiting.console.snapshot', { limit: 100 }, state.actor);
    const previous = state.data;
    state.data = {
      ...result,
      ...(previous?.system_status ? { system_status: previous.system_status } : {}),
      ...(previous?.capacity ? { capacity: previous.capacity } : {}),
      ...(previous?.executor_fleet_by_capability ? { executor_fleet_by_capability: previous.executor_fleet_by_capability } : {}),
      ...(previous?.budget_policy ? { budget_policy: previous.budget_policy } : {}),
    };
    $('loading-view').classList.add('hidden');
    $('error-view').classList.add('hidden');
    $('view-root').classList.remove('hidden');
    updateChrome();
    render();
    if (!silent) toast('业务状态已同步');
  } catch (error) {
    if (!silent || !state.data) showFatal('无法读取业务状态', error.message);
    else toast(`自动刷新失败：${error.message}`);
  }
}

function updateChrome() {
  const snapshot = state.data?.snapshot || {};
  const count = snapshot.attention?.length || 0;
  $('attention-badge').textContent = count;
  $('attention-badge').classList.toggle('hidden', count === 0);
  $('freshness').textContent = snapshot.as_of ? `数据更新于 ${relativeTime(snapshot.as_of)}` : '刚刚同步';
}

function render() {
  const titles = {
    overview: ['业务总览', greetingTitle()], companies: ['公司与招聘源', '每家公司走到了哪一步'],
    daily: ['今日运行', '今天的增量采集进展'], attention: ['待处理', '需要系统或你介入的事项'],
    outcomes: ['数据结果', '采集带来了什么结果'], system: ['系统状态', '容量、队列与执行健康'],
  };
  const [eyebrow, title] = titles[state.view];
  $('eyebrow').textContent = eyebrow; $('page-title').textContent = title;
  document.querySelectorAll('.nav-item').forEach((item) => item.classList.toggle('active', item.dataset.view === state.view));
  const root = $('view-root');
  root.innerHTML = ({ overview: overviewView, companies: companiesView, daily: dailyView, attention: attentionView, outcomes: outcomesView, system: systemView })[state.view]();
  bindViewActions();
}

function overviewView() {
  const d = state.data; const s = d.snapshot; const status = d.system_status || {}; const daily = s.daily;
  const attention = s.attention || []; const companies = s.companies || [];
  const healthy = attention.length === 0 && Number(status.deadline_misses || 0) === 0;
  const counts = dailyCounts(daily);
  return `
    <section class="status-banner ${healthy ? '' : 'warning'}">
      <div><h2>${healthy ? '业务运行正常' : `${attention.length || status.deadline_misses || 0} 项需要关注`}</h2><p>${healthy ? '没有发现需要人工介入的阻塞，系统会按计划继续。' : '优先处理等待人工或已经超时的工作，其他采集不受影响。'}</p></div>
      <div><strong>${num(s.outcomes?.jobs_total)}</strong><span>当前岗位记录</span></div>
    </section>
    <section class="panel"><div class="panel-head"><h2>今日任务进度</h2><span>${daily ? esc(daily.run.schedule_date) : '今天尚未生成运行批次'}</span></div>${runHTML(daily, counts)}</section>
    <section class="panel"><div class="panel-head"><h2>业务流水线</h2><button data-go="companies">查看全部公司</button></div>${pipelineHTML(s.pipeline || {})}</section>
    <div class="dashboard-grid">
      <section class="panel"><div class="panel-head"><h2>需要关注的事项（${attention.length}）</h2><button data-go="attention">查看全部待处理</button></div>${attentionHTML(attention.slice(0, 5))}</section>
      <section class="panel"><div class="panel-head"><h2>今日数据成果</h2><button data-go="outcomes">查看数据结果</button></div>${outcomeMiniHTML(s.outcomes || {})}</section>
    </div>
    <section class="panel"><div class="panel-head"><h2>最近活动</h2><span>最新 ${Math.min(8, s.activity?.length || 0)} 条</span></div>${activityHTML((s.activity || []).slice(0, 8))}</section>`;
}

function companiesView() {
  const companies = (state.data.snapshot.companies || []).filter((item) => !state.filter || item.company.name.toLowerCase().includes(state.filter.toLowerCase()));
  return `<div class="toolbar"><input id="company-search" class="search" type="search" value="${esc(state.filter)}" placeholder="搜索公司名称"><button class="primary-button" data-ask="请帮我新增一家公司，并引导我提供公司名称。">新增公司</button></div>
    <div class="table-wrap"><table><thead><tr><th>公司</th><th>阶段</th><th>招聘源</th><th>Recipe</th><th>岗位数据</th><th>需要关注</th><th>最近成功</th></tr></thead><tbody>
    ${companies.map((item) => `<tr data-company="${esc(item.company.company_id)}" tabindex="0" role="button" aria-label="查看 ${esc(item.company.name)} 详情"><td><strong>${esc(item.company.name)}</strong><span class="subtle">${esc(item.company.website || '未记录官网')}</span></td><td>${pill(onboardingLabel(item.company.onboarding_status), statusTone(item.company.onboarding_status))}</td><td>${num(item.source_ready)} / ${num(item.source_total)} 可运行<br><span class="subtle">${(item.categories || []).map(categoryLabel).join(' · ') || '尚未分类'}</span></td><td>列表 ${num(item.listing_assignments)} · 详情 ${num(item.detail_assignments)}</td><td>${num(item.jobs_total)} 个<br><span class="subtle">${num(item.details_complete)} 个详情完整</span></td><td>${item.waiting_human || item.source_attention ? pill(`${num((item.waiting_human || 0) + (item.source_attention || 0))} 项`, 'warn') : pill('无', '')}</td><td>${item.last_successful_at ? relativeTime(item.last_successful_at) : '—'}</td></tr>`).join('') || `<tr><td colspan="7"><div class="empty">没有匹配的公司</div></td></tr>`}
    </tbody></table></div><div id="company-detail"></div>`;
}

function dailyView() {
  const daily = state.data.snapshot.daily; const counts = dailyCounts(daily); const run = daily?.run;
  if (!run) return `<div class="empty"><h2>今天尚未生成运行批次</h2><p>系统会在调度时间创建当日增量采集；初始化和人工修复任务仍可独立运行。</p><button class="primary-button" data-ask="请检查今天的招聘数据定时任务为什么还没有生成，并告诉我是否需要处理。">让系统检查</button></div>`;
  const summary = run.summary || {};
  return `<section class="panel"><div class="panel-head"><h2>${esc(run.schedule_date)} 增量采集</h2>${pill(dailyLabel(run.daily_run_status), statusTone(run.daily_run_status))}</div>${runHTML(daily, counts)}</section>
    <section class="metric-blocks">${metricBlock('预计招聘源', run.expected_sources)}${metricBlock('列表成功', summary.listing_succeeded)}${metricBlock('列表异常', summary.listing_exceptions)}${metricBlock('待抓详情', summary.detail_expected)}${metricBlock('详情成功', summary.detail_succeeded)}${metricBlock('已接受缺口', summary.detail_accepted_gap)}</section>
    <div class="definition-note">今日运行只从每个列表顶部向后读取，遇到已知边界即停止；不会每天重抓全部历史岗位。岗位删除不在当前业务范围内。</div>
    <details class="technical"><summary>查看调度与审计信息</summary><pre>${esc(JSON.stringify(run, null, 2))}</pre></details>`;
}

function attentionView() {
  const items = state.data.snapshot.attention || [];
  return `<section class="panel"><div class="panel-head"><h2>人工协作队列</h2><span>${items.length} 项</span></div>${attentionHTML(items)}</section>
    <div class="definition-note">这里仅展示确实等待人工，或已超过截止时间仍未完成的工作。点击“对话处理”，系统会带上公司和问题上下文继续推进。</div>`;
}

function outcomesView() {
  const o = state.data.snapshot.outcomes || {};
  const completeRate = Number(o.jobs_total) ? Math.round(Number(o.details_complete || 0) / Number(o.jobs_total) * 100) : 0;
  return `<section class="metric-blocks">${metricBlock('岗位总量', o.jobs_total)}${metricBlock('今日新发现', o.jobs_new_today)}${metricBlock('今日更新', o.jobs_updated_today)}${metricBlock('详情完整', o.details_complete)}${metricBlock('详情待补齐', o.details_pending)}${metricBlock('详情完整率', `${completeRate}%`)}</section>
    <section class="panel" style="margin-top:18px"><div class="panel-head"><h2>结果口径</h2><span>面向业务，不混淆运行次数</span></div><div class="detail-grid"><div><span>列表层</span><strong>发现岗位 URL 与基础字段</strong></div><div><span>详情层</span><strong>补齐岗位正文与详情版本</strong></div><div><span>增量层</span><strong>遇到已知边界停止</strong></div><div><span>历史变化</span><strong>全量校准时覆盖</strong></div></div><div class="definition-note">当前不判断岗位删除。历史岗位若更新但没有重新置顶，只有周期性全量校准才能发现。</div></section>`;
}

function systemView() {
  const d = state.data; const s = d.system_status || {}; const c = d.capacity || {}; const fleet = d.executor_fleet_by_capability || {}; const budget = d.budget_policy || {};
  if (!d.system_status || !d.capacity) return `<section class="state-panel"><div class="spinner" aria-hidden="true"></div><h2>正在读取系统运行状态</h2><p>业务总览已可用；容量与执行健康按需加载。</p></section>`;
  return `<section class="summary-grid">${summaryCard('可执行工作', num(s.runnable_works), `截止超时 ${num(s.deadline_misses)}`, '队列')}${summaryCard('事件待投递', num(s.event_outbox?.pending), `已耗尽 ${num(s.event_outbox?.exhausted)}`, '事件')}${summaryCard('执行待投递', num(s.execution_dispatches?.pending), `已耗尽 ${num(s.execution_dispatches?.exhausted)}`, '调度')}${summaryCard('全局并发上限', num(budget.max_active), `当前扫描 ${num(c.runnable_scanned)}`, '容量')}</section>
    <div class="dashboard-grid"><section class="panel"><div class="panel-head"><h2>工作状态</h2><span>控制面实时统计</span></div>${keyValues(s.work_counts || {}, workLabel)}</section><section class="panel"><div class="panel-head"><h2>执行能力</h2><span>当前 Actor 成员</span></div>${keyValues(fleet, capabilityLabel)}</section></div>
    <section class="panel"><div class="panel-head"><h2>容量维度</h2><span>${c.runnable_counts_exact === false ? '有界扫描，显示下限' : '精确统计'}</span></div>${capacityTable(c.dimensions || [])}</section>
    <details class="technical"><summary>查看完整技术证据</summary><pre>${esc(JSON.stringify({ system_status: s, capacity: c, executor_fleet: fleet, budget_policy: budget }, null, 2))}</pre></details>`;
}

function summaryCard(label, value, note, flag) { return `<article class="summary-card"><div class="label"><span>${esc(label)}</span><b>${esc(flag)}</b></div><strong>${esc(value)}</strong><small>${esc(note)}</small></article>`; }
function metricBlock(label, value) { return `<article class="metric-block"><span>${esc(label)}</span><strong>${esc(num(value))}</strong></article>`; }
function pipelineHTML(p) {
  const steps = [
    ['公司纳管', p.companies, '登记业务对象'], ['招聘源发现', p.companies_sourced, '深度探索与分类'],
    ['列表 Recipe', p.listing_recipes, '固定可复用流程'], ['首次全量', p.baselines, '建立增量边界'],
    ['每日增量', p.ready_sources, '按倒序读取顶部'], ['详情 Recipe', p.detail_recipes, '补齐岗位详情'],
  ];
  return `<div class="pipeline">${steps.map(([label, value, note], i) => `<div class="pipeline-step ${Number(value) > 0 ? 'done' : i === 0 ? 'active' : ''}"><span>${esc(label)}</span><b>${num(value)}</b><small>${esc(note)}</small></div>`).join('')}</div>`;
}
function runHTML(daily, counts) {
  if (!daily) return `<div class="empty">今天尚未创建增量运行批次。初始化、修复等独立任务不受影响。</div>`;
  const total = Math.max(1, Object.values(counts).reduce((a,b) => a + Number(b || 0), 0));
  const parts = [['completed','已完成'],['running','运行中'],['waiting','等待'],['failed','失败'],['excluded','已排除']];
  return `<div class="run-track">${parts.map(([key]) => `<i class="seg-${key}" style="width:${Number(counts[key] || 0) / total * 100}%"></i>`).join('')}</div><div class="legend-row">${parts.map(([key,label]) => `<span><b>${num(counts[key])}</b>${label}</span>`).join('')}</div>`;
}
function companyMiniHTML(items) {
  if (!items.length) return `<div class="empty">还没有纳管公司。可以在下方对话框直接说“新增字节跳动”。</div>`;
  return `<div class="table-wrap"><table><thead><tr><th>公司</th><th>阶段</th><th>招聘源</th><th>岗位</th><th>关注</th></tr></thead><tbody>${items.map((item) => `<tr data-company="${esc(item.company.company_id)}" tabindex="0" role="button" aria-label="查看 ${esc(item.company.name)} 详情"><td><strong>${esc(item.company.name)}</strong></td><td>${pill(onboardingLabel(item.company.onboarding_status), statusTone(item.company.onboarding_status))}</td><td>${num(item.source_ready)} / ${num(item.source_total)}</td><td>${num(item.jobs_total)}</td><td>${num((item.waiting_human || 0) + (item.source_attention || 0))}</td></tr>`).join('')}</tbody></table></div>`;
}
function attentionHTML(items) {
  if (!items.length) return `<div class="empty">没有需要人工介入的事项。系统会继续自动运行。</div>`;
  return `<div class="attention-list">${items.map((item) => { const w=item.work||{}; const prompt=`请处理${item.company_name ? item.company_name + '的' : ''}${purposeLabel(w.purpose)}问题。先说明现状、影响和原因，再按系统规则安全推进；如必须由我决定，请只问必要的问题。`; return `<article class="attention-item"><div><h3>${esc(item.company_name || targetLabel(w.target_type))} · ${esc(purposeLabel(w.purpose))}</h3><p>${esc(waitingLabel(w.waiting_reason || w.last_failure_class))} · 更新于 ${relativeTime(item.updated_at)}</p></div><button data-ask="${esc(prompt)}">对话处理</button></article>`; }).join('')}</div>`;
}
function outcomeMiniHTML(o) {
  return `<div class="detail-grid"><div><span>新增岗位</span><strong>${num(o.jobs_new_today)}</strong></div><div><span>更新岗位</span><strong>${num(o.jobs_updated_today)}</strong></div><div><span>列表岗位总量</span><strong>${num(o.jobs_total)}</strong></div><div><span>详情完整</span><strong>${num(o.details_complete)}</strong></div></div>`;
}
function activityHTML(items) {
  if (!items.length) return `<div class="empty">暂时没有运行记录</div>`;
  return `<div class="activity-list">${items.map((item) => `<div class="activity-item"><i></i><div><b>${esc(item.company_name || targetLabel(item.work?.target_type))} · ${esc(purposeLabel(item.work?.purpose))}</b><small>${esc(workLabel(item.work?.work_status))}${item.work?.resolution ? ` · ${esc(resolutionLabel(item.work.resolution))}` : ''}</small></div><span>${relativeTime(item.updated_at)}</span></div>`).join('')}</div>`;
}
function capacityTable(rows) {
  if (!rows.length) return `<div class="empty">当前没有活跃容量维度</div>`;
  return `<div class="table-wrap"><table><thead><tr><th>维度</th><th>对象</th><th>运行中</th><th>可执行</th><th>最早等待</th></tr></thead><tbody>${rows.map((row) => `<tr><td>${esc(dimensionLabel(row.dimension_type))}</td><td>${esc(row.dimension_key)}</td><td>${num(row.active)}</td><td>${num(row.runnable)}</td><td>${row.oldest_runnable_at ? relativeTime(row.oldest_runnable_at) : '—'}</td></tr>`).join('')}</tbody></table></div>`;
}
function keyValues(values, labeler) {
  const rows = Object.entries(values);
  if (!rows.length) return `<div class="empty">暂无数据</div>`;
  return `<div class="detail-grid">${rows.map(([key,value]) => `<div><span>${esc(labeler(key))}</span><strong>${num(value)}</strong></div>`).join('')}</div>`;
}

async function openCompany(companyID) {
  state.selectedCompany = companyID;
  const item = state.data.snapshot.companies.find((candidate) => candidate.company.company_id === companyID);
  if (!item) return;
  const drawer = $('company-detail'); if (!drawer) { state.view='companies'; render(); return openCompany(companyID); }
  drawer.innerHTML = `<section class="detail-drawer"><h2>${esc(item.company.name)}</h2><p class="subtle">正在读取招聘源详情……</p></section>`;
  try {
    let sources = state.sources.get(companyID);
    if (!sources) {
      const result = await state.wire.request('recruiting.source.list', { company_id: companyID, limit: 100 }, state.actor);
      sources = result.sources || []; state.sources.set(companyID, sources);
    }
    drawer.innerHTML = `<section class="detail-drawer"><div class="section-heading"><div><h2>${esc(item.company.name)}</h2><p>${esc(item.company.website || '尚未记录官网')}</p></div><button data-ask="请告诉我${esc(item.company.name)}当前的完整业务状态、阻塞和下一步。">对话处理</button></div>
      <div class="detail-grid"><div><span>业务阶段</span><strong>${esc(onboardingLabel(item.company.onboarding_status))}</strong></div><div><span>招聘源</span><strong>${num(item.source_ready)} / ${num(item.source_total)} 可运行</strong></div><div><span>岗位记录</span><strong>${num(item.jobs_total)}</strong></div><div><span>详情完整</span><strong>${num(item.details_complete)}</strong></div></div>
      <h3>岗位列表来源</h3><div class="source-list">${sources.length ? sources.map(sourceHTML).join('') : `<div class="empty">还没有已物化的招聘源</div>`}</div>
      <details class="technical"><summary>查看公司技术证据</summary><pre>${esc(JSON.stringify(item, null, 2))}</pre></details></section>`;
    drawer.scrollIntoView({ behavior: 'smooth', block: 'nearest' }); bindViewActions();
  } catch (error) { drawer.innerHTML = `<div class="detail-drawer"><div class="empty">招聘源详情读取失败：${esc(error.message)}</div></div>`; }
}

function sourceHTML(source) {
  const endpoint = source.active_endpoint || source.candidate_endpoint || {};
  return `<article class="source-card"><header><div><strong>${esc(categoryLabel(endpoint.category || 'unknown'))}</strong><a href="${esc(endpoint.url || '#')}" target="_blank" rel="noopener">${esc(endpoint.url || '尚无地址')}</a></div>${pill(sourceLabel(source.readiness_status), statusTone(source.readiness_status))}</header><div class="source-meta"><span>健康：${esc(healthLabel(source.health_status))}</span><span>列表 Recipe：${source.listing_assignment ? '已绑定' : '未绑定'}</span><span>详情 Recipe：${source.detail_assignment ? '已绑定' : '未绑定'}</span><span>控制：${esc(controlLabel(source.control_status))}</span></div></article>`;
}

function bindViewActions() {
  document.querySelectorAll('[data-go]').forEach((button) => button.addEventListener('click', () => changeView(button.dataset.go)));
  document.querySelectorAll('[data-ask]').forEach((button) => button.addEventListener('click', () => openAssistant(button.dataset.ask)));
  document.querySelectorAll('[data-company]').forEach((row) => {
    row.addEventListener('click', () => openCompany(row.dataset.company));
    row.addEventListener('keydown', (event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); openCompany(row.dataset.company); } });
  });
  const search = $('company-search'); if (search) search.addEventListener('input', () => { state.filter=search.value; render(); $('company-search')?.focus(); });
}

function changeView(view) {
  state.view=view; render(); $('content').focus(); document.querySelector('.sidebar').classList.remove('open'); $('menu-button').setAttribute('aria-expanded','false');
  if (view === 'system' && state.data && (!state.data.system_status || !state.data.capacity)) loadSystemStatus();
}
async function loadSystemStatus() {
  if (state.systemLoading) return;
  state.systemLoading = true;
  try {
    const [status, capacity] = await Promise.all([
      state.wire.request('recruiting.system.status', { limit: 20 }, state.actor),
      state.wire.request('recruiting.capacity.status', { limit: 50 }, state.actor),
    ]);
    Object.assign(state.data, status, capacity);
    if (state.view === 'system') render();
  } catch (error) { toast(`系统状态读取失败：${error.message}`); }
  finally { state.systemLoading = false; }
}
function openAssistant(prompt='') { $('assistant-panel').classList.add('open'); if (prompt) $('assistant-input').value=prompt; $('assistant-input').focus(); }
async function askAgent(text) {
  if (!state.steward) throw new Error('当前频道没有可用的运营 Agent');
  $('assistant-panel').classList.add('open');
  addAssistantMessage(text, 'user'); $('assistant-input').value='';
  const waiting=addAssistantMessage('正在理解现场并处理……','system');
  try { const response=await state.wire.request('agent.ask',{text},state.steward,180000); waiting.textContent=response.text || response.result || '请求已完成；正在刷新业务状态。'; await refresh(true); }
  catch(error){ waiting.className='assistant-message error'; waiting.textContent=error.message; }
}
function addAssistantMessage(text,kind){ const box=document.createElement('div'); box.className=`assistant-message ${kind}`; box.textContent=text; $('assistant-thread').append(box); $('assistant-thread').scrollTop=$('assistant-thread').scrollHeight; return box; }

function dailyCounts(daily) {
  const raw=daily?.occurrence_counts||{}; const result={completed:0,running:0,waiting:0,failed:0,excluded:0};
  for(const [key,value] of Object.entries(raw)){ if(['completed','succeeded'].includes(key)) result.completed+=Number(value); else if(['running','offered','accepted'].includes(key)) result.running+=Number(value); else if(['failed'].includes(key)) result.failed+=Number(value); else if(['excluded','skipped','canceled'].includes(key)) result.excluded+=Number(value); else result.waiting+=Number(value); }
  return result;
}
function pill(label,tone=''){return `<span class="pill ${tone}">${esc(label)}</span>`;}
function num(value){ if(typeof value==='string' && value.endsWith('%')) return value; return new Intl.NumberFormat('zh-CN').format(Number(value||0)); }
function sumMap(value){return Object.values(value||{}).reduce((a,b)=>a+Number(b||0),0);}
function esc(value){return String(value??'').replace(/[&<>'"]/g,(ch)=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[ch]));}
function relativeTime(value){const time=new Date(value).getTime();if(!Number.isFinite(time))return '未知';const seconds=Math.max(0,(Date.now()-time)/1000);if(seconds<60)return '刚刚';if(seconds<3600)return `${Math.floor(seconds/60)} 分钟前`;if(seconds<86400)return `${Math.floor(seconds/3600)} 小时前`;return `${Math.floor(seconds/86400)} 天前`;}
function greetingTitle(){const h=new Date().getHours();return `${h<11?'早上':h<14?'中午':h<19?'下午':'晚上'}好，招聘数据${(state.data?.snapshot?.attention?.length||0)>0?'有事项需要关注':'正在运行'}`;}
const labels={
  onboarding:{new:'待开始',discovering_sources:'发现招聘源',blocked_no_sources:'未发现招聘源',initializing:'初始化',blocked:'已阻塞',ready:'持续运行'},
  work:{open:'待执行',running:'运行中',waiting_retry:'等待重试',waiting_human:'等待人工',paused:'已暂停',completed:'已完成',failed:'失败',canceled:'已取消'},
  daily:{planned:'已计划',running:'运行中',completed:'已完成',completed_with_exceptions:'有异常完成'},
  source:{candidate:'候选',validating:'验证中',ready:'可运行',repairing:'修复中',invalid:'无效',rejected:'已拒绝'},
  category:{social:'社会招聘',campus:'校园招聘',intern:'实习',special:'专项计划',all:'综合招聘',unknown:'待分类'},
  purpose:{source_discovery:'招聘源发现',deep_discovery:'深度发现',listing:'岗位列表采集',detail:'岗位详情采集',baseline:'首次全量',calibration:'全量校准',repair:'修复',backfill:'历史补齐',source_validation:'招聘源验证',recipe_validation:'Recipe 验证'},
};
const label=(group,key,fallback)=>labels[group]?.[key]||fallback||key||'未知';
const onboardingLabel=(v)=>label('onboarding',v); const workLabel=(v)=>label('work',v); const dailyLabel=(v)=>label('daily',v); const sourceLabel=(v)=>label('source',v); const categoryLabel=(v)=>String(v||'').startsWith('special:')?`专项计划：${String(v).slice(8)}`:label('category',v); const purposeLabel=(v)=>label('purpose',v,String(v||'业务工作').replaceAll('_',' '));
function statusTone(value){return ['blocked','blocked_no_sources','invalid','failed'].includes(value)?'danger':['discovering_sources','initializing','validating','repairing','running','planned','completed_with_exceptions'].includes(value)?'warn':['archived','paused','candidate'].includes(value)?'neutral':'';}
function waitingLabel(v){return ({auth_required:'需要登录环境',contract_drift:'页面结构发生变化',rate_limited:'访问频率受限',network:'网络异常',quality_rejected:'结果质量未通过'}[v]||String(v||'等待处理').replaceAll('_',' '));}
function resolutionLabel(v){return ({succeeded:'成功',accepted_gap:'已接受缺口',skipped:'已跳过',terminated:'已终止'}[v]||v);}
function targetLabel(v){return ({company:'公司',source:'招聘源',job:'岗位',daily_run:'每日运行'}[v]||'业务任务');}
function healthLabel(v){return ({healthy:'正常',degraded:'降级',unhealthy:'异常'}[v]||v||'未知');}
function controlLabel(v){return ({active:'运行',paused:'暂停',archived:'归档'}[v]||v||'未知');}
function capabilityLabel(v){return ({'http.fetch':'网页请求','browser.navigate':'浏览器访问','browser.capture':'页面采集'}[v]||v);}
function dimensionLabel(v){return ({capability:'执行能力',origin:'网站来源',company:'公司',profile:'浏览器环境',purpose:'工作类型'}[v]||v);}
function setConnection(kind){state.connected=kind==='connected';const box=document.querySelector('.connection');box.classList.toggle('connected',kind==='connected');box.classList.toggle('error',kind==='error');$('connection-label').textContent=kind==='connected'?'节点已连接':kind==='error'?'连接中断':'正在连接';}
function showLoading(){$('loading-view').classList.remove('hidden');$('error-view').classList.add('hidden');$('view-root').classList.add('hidden');}
function showFatal(title,detail){$('loading-view').classList.add('hidden');$('view-root').classList.add('hidden');$('error-view').classList.remove('hidden');$('error-title').textContent=title;$('error-detail').textContent=detail;}
let toastTimer=0;function toast(message){$('toast').textContent=message;$('toast').classList.add('show');window.clearTimeout(toastTimer);toastTimer=window.setTimeout(()=>$('toast').classList.remove('show'),2600);}

document.querySelectorAll('.nav-item').forEach((button)=>button.addEventListener('click',()=>changeView(button.dataset.view)));
$('refresh-button').addEventListener('click',()=>refresh()); $('retry-button').addEventListener('click',()=>location.reload());
$('ask-top').addEventListener('click',()=>openAssistant()); $('close-assistant').addEventListener('click',()=>$('assistant-panel').classList.remove('open'));
$('menu-button').addEventListener('click',()=>{const sidebar=document.querySelector('.sidebar');const open=sidebar.classList.toggle('open');$('menu-button').setAttribute('aria-expanded',String(open));});
$('assistant-form').addEventListener('submit',(event)=>{event.preventDefault();const text=$('assistant-input').value.trim();if(text)askAgent(text);});
$('assistant-input').addEventListener('keydown',(event)=>{if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();$('assistant-form').requestSubmit();}});
document.querySelectorAll('.prompt-chip').forEach((button)=>button.addEventListener('click',()=>askAgent(button.textContent)));

state.wire=new AtollWire(); state.wire.connect();
