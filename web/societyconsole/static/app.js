const $ = (id) => document.getElementById(id);
const SVG = 'http://www.w3.org/2000/svg';
const state = { wire: null, snapshot: null, issue: null, population: [], firms: [], networkEdges: [], inspectedCitizen: null, history: [], events: [], selectedCitizen: '', trace: null, poll: null, refreshing: false, ballot: { roundId: 0, assignments: Array(10).fill(null), submitted: false } };

class PublicSociety {
  async submit(type, payload = {}) {
    const response = await fetch('/api/society', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ type, payload }),
    });
    const result = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(userMessage(result.error));
    return result;
  }
}

async function boot() {
  setConnection('connecting');
  state.wire = new PublicSociety();
  try {
    await refresh(true);
    $('lab').classList.remove('hidden');
    $('workspace').classList.remove('hidden');
    setConnection('connected');
    state.poll = setInterval(() => refresh(false), 2000);
  } catch (error) { setConnection('disconnected'); showError(error); }
}

async function refresh(includeHistory) {
  if (state.refreshing) return;
  state.refreshing = true;
  try {
    const [snapshot, issue] = await Promise.all([
      state.wire.submit('society.experiment.status'),
      state.wire.submit('society.experiment.issue'),
    ]);
    state.snapshot = normalizeSnapshot(snapshot);
    renderIssue(issue);
    if (includeHistory || !state.population.length) await refreshWorldView();
    renderSnapshot();
    if (includeHistory || !snapshot.clock?.paused) await refreshHistory();
  } catch (error) { showError(error); }
  finally { state.refreshing = false; }
}

async function refreshWorldView() {
  const [population, network] = await Promise.all([
    state.wire.submit('society.experiment.population'),
    state.wire.submit('society.experiment.network'),
  ]);
  state.population = asArray(population.citizens).map((row) => ({ id: row[0], alive: Boolean(row[1]), employer_id: row[2] || '', balance: Number(row[3] || 0), household_id: row[4] || '', community_id: row[5] || '' }));
  state.firms = asArray(population.firms).map((row) => ({ id: row[0], active: Boolean(row[1]), employee_count: Number(row[2] || 0), inventory: Number(row[3] || 0), balance: Number(row[4] || 0) }));
  state.networkEdges = asArray(network.edges).map((row) => ({ a: Number(row[0]), b: Number(row[1]), trust_bps: Number(row[2] || 0), kind: Number(row[3]) === 1 ? 'family' : 'social' }));
  if (state.selectedCitizen) {
    const selected = state.selectedCitizen;
    const detail = await loadCitizen(selected);
    if (state.selectedCitizen === selected) state.inspectedCitizen = detail;
  }
}

async function refreshHistory() {
  const day = state.snapshot?.clock?.day || 0;
  if (!day) { state.history = []; renderChart(); return; }
  const stride = Math.max(1, Math.ceil(day / 360));
  const result = await state.wire.submit('society.experiment.history', { from_day: 1, to_day: day, stride });
  state.history = asArray(result.metrics);
  const recent = await state.wire.submit('society.experiment.recent', { limit: 80 });
  state.events = asArray(recent.events).reverse();
  renderTimeline();
  renderChart();
}

function renderSnapshot() {
  const s = state.snapshot;
  const m = s?.latest_metrics || {};
  const day = s?.clock?.day || 0;
  const year = Math.floor(day / 365);
  const yearDay = day % 365;
  $('clock').textContent = `第 ${year} 年 · ${String(yearDay).padStart(3, '0')} 日`;
  $('run-state').textContent = s.clock?.stopped ? '已停止' : s.clock?.paused ? '已暂停' : '运行中';
  setMetric('m-population', m.population ?? countAlive(state.population));
  setMetric('m-employment', percent(m.employment_rate));
  setMetric('m-output', m.total_output ?? 0);
  setMetric('m-gini', decimal(m.gini));
  setMetric('m-poverty', percent(m.poverty_rate));
  setMetric('m-trust', decimal(m.average_trust));
  setMetric('m-support', percent(m.government_support));
  setMetric('m-polarization', decimal(m.polarization));
  setMetric('m-firms', `${m.active_firms ?? state.firms.filter((f) => f.active).length} 家`);
  $('m-treasury').textContent = `财政 ${money(m.treasury ?? s.balances?.government ?? 0)}`;
  setMetric('m-housing', percent(m.housing_secure_rate));
  $('m-debt').textContent = `社会负债 ${money(m.total_debt)}`;
  setMetric('m-clustering', decimal(m.clustering_coefficient));
  $('m-isolated').textContent = `孤立率 ${percent(m.isolated_rate)}`;
  setMetric('m-service', percent(m.public_service));
  setMetric('m-lifecycle', `${m.births || 0} / ${m.deaths || 0}`);
  setMetric('m-recovery', Number(m.recovery_days) >= 0 ? `${m.recovery_days} 日` : '未恢复');
  $('m-policy').textContent = `税 ${percent((s.government?.policy?.income_tax_bps || 0) / 10000)} · 福利 ${money(s.government?.policy?.daily_benefit || 0)}/日`;
  setMetric('m-tax', `税收 ${money(m.tax_collected)}`);
  $('m-benefits').textContent = `福利 ${money(m.benefits_paid)}`;
  renderEditorialOutcomes(m);
  renderNetwork(); renderCitizenPicker(); renderCitizen(); renderWealthDistribution(); renderFirms();
  renderPostVote();
}

function renderEditorialOutcomes(metrics) {
  const employment = Number(metrics.employment_rate || 0);
  const gini = Number(metrics.gini || 0);
  const poverty = Number(metrics.poverty_rate || 0);
  const trust = Number(metrics.average_trust || 0);
  $('a-employment').textContent = `当前就业率 ${percent(employment)}`;
  $('a-gini').textContent = `当前基尼系数 ${decimal(gini)}`;
  $('b-poverty').textContent = `当前贫困率 ${percent(poverty)}`;
  $('b-trust').textContent = `当前信任 ${decimal(trust)}`;
}

function renderChart() {
  const svg = $('history-chart');
  svg.replaceChildren();
  const width = 1000, height = 300, left = 48, right = 18, top = 18, bottom = 34;
  for (let i = 0; i <= 4; i++) {
    const y = top + (height - top - bottom) * i / 4;
    svg.append(svgEl('line', { x1: left, x2: width - right, y1: y, y2: y, class: 'chart-grid' }));
    const label = svgEl('text', { x: 6, y: y + 4, class: 'chart-axis' }); label.textContent = `${100 - i * 25}%`; svg.append(label);
  }
  if (!state.history.length) return;
  const maxDay = state.history.at(-1).day || 1;
  const series = [
    ['population', '#40e0d0', (m) => (m.population || 0) / 100],
    ['trust', '#55a7ff', (m) => m.average_trust || 0],
    ['support', '#f2bd55', (m) => m.government_support || 0],
    ['gini', '#ff6961', (m) => m.gini || 0],
  ];
  for (const [, color, value] of series) {
    const points = state.history.map((m) => {
      const x = left + (width - left - right) * m.day / maxDay;
      const y = top + (height - top - bottom) * (1 - Math.max(0, Math.min(1, value(m))));
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    }).join(' ');
    svg.append(svgEl('polyline', { points, class: 'chart-line', stroke: color }));
  }
  for (const day of [1, Math.round(maxDay / 2), maxDay]) {
    const x = left + (width - left - right) * day / maxDay;
    const label = svgEl('text', { x, y: height - 10, 'text-anchor': day === 1 ? 'start' : day === maxDay ? 'end' : 'middle', class: 'chart-axis' });
    label.textContent = `第${day}日`; svg.append(label);
  }
  $('history-range').textContent = `第1—${maxDay}日 · ${state.history.length} 个记录点`;
  $('chart-legend').replaceChildren(...legendItems([['人口', '#40e0d0'], ['信任', '#55a7ff'], ['支持', '#f2bd55'], ['基尼系数', '#ff6961']]));
}

function legendItems(items) {
  return items.map(([label, color]) => { const span = document.createElement('span'); span.textContent = label; span.style.color = color; return span; });
}

function renderNetwork() {
  const svg = $('network'); svg.replaceChildren();
  const citizens = state.population;
  if (!citizens.length) return;
  const visible = citizens.slice(0, 100), positions = new Map();
  visible.forEach((citizen, index) => {
    const ring = Math.floor(index / 25), within = index % 25, radius = 70 + ring * 47;
    const angle = within / Math.min(25, visible.length - ring * 25) * Math.PI * 2 - Math.PI / 2;
    positions.set(citizen.id, { x: 340 + Math.cos(angle) * radius, y: 215 + Math.sin(angle) * radius });
  });
  for (const edge of state.networkEdges) {
    const aCitizen = citizens[edge.a], bCitizen = citizens[edge.b];
    const a = positions.get(aCitizen?.id), b = positions.get(bCitizen?.id); if (!a || !b) continue;
    svg.append(svgEl('line', { x1: a.x, y1: a.y, x2: b.x, y2: b.y, class: `network-edge ${edge.kind === 'family' ? 'family' : ''}`, opacity: .2 + (edge.trust_bps || 0) / 15000 }));
  }
  for (const citizen of visible) {
    const p = positions.get(citizen.id), balance = citizen.balance;
    const node = svgEl('circle', { cx: p.x, cy: p.y, r: citizen.alive ? 6 : 3.5, fill: citizen.alive ? citizen.employer_id ? '#40e0d0' : '#f2bd55' : '#48535d', class: `network-node ${state.selectedCitizen === citizen.id ? 'selected' : ''}`, tabindex: 0, role: 'button', 'aria-label': `${displayID(citizen.id)}，${citizen.alive ? '存活' : '死亡'}，余额${balance}分` });
    node.addEventListener('click', () => selectCitizen(citizen.id));
    node.addEventListener('keydown', (event) => { if (event.key === 'Enter' || event.key === ' ') node.dispatchEvent(new Event('click')); });
    svg.append(node);
  }
}

function renderCitizen() {
  const detail = state.inspectedCitizen;
  const citizen = detail?.citizen?.id === state.selectedCitizen ? detail.citizen : null;
  if (!citizen) { $('citizen-name').textContent = '选择一名居民'; $('citizen-detail').className = 'detail-empty'; $('citizen-detail').textContent = '从居民名单选择一个名字，或点击关系图上的圆点，看看他最近过得怎样。'; return; }
  $('citizen-name').textContent = displayID(citizen.id);
  const balance = detail.balance || 0;
  const relationCount = asArray(detail.relationships).length;
  const values = [
    ['生活状态', citizen.alive ? '平安生活中' : '已经离世'], ['年龄', `${(citizen.age_days / 365).toFixed(1)} 岁`],
    ['家庭', displayID(citizen.household_id)], ['居住地', displayID(citizen.community_id) || '—'],
    ['职业', occupationLabel(citizen)], ['工作单位', citizen.employer_id ? displayID(citizen.employer_id) : '目前待业'],
    ['手头存款', money(balance)], ['欠款', citizen.debt > 0 ? money(citizen.debt) : '没有欠款'],
    ['居住情况', housingLabel(citizen.housing_bps)], ['今天收入', citizen.daily_income > 0 ? money(citizen.daily_income) : '今天还没有收入'],
    ['眼下打算', strategyLabel(citizen.current_strategy)], ['公共消息', exposureLabel(citizen.information_exposure_bps)],
    ['身体状况', healthLabel(citizen.health_bps)], ['对政府的信任', supportLabel(citizen.government_support_bps)],
    ['对社会分配的看法', opinionLabel(citizen.opinion)], ['生活近况', `${relationCount} 位常联系的人 · ${needLabel(citizen.unmet_need_days)}`],
  ];
  const root = $('citizen-detail'); root.className = ''; root.replaceChildren();
  const grid = document.createElement('div'); grid.className = 'detail-grid';
  for (const [label, value] of values) { const box = document.createElement('div'), span = document.createElement('span'), strong = document.createElement('strong'); span.textContent = label; strong.textContent = value; box.append(span, strong); grid.append(box); }
  const decision = document.createElement('div'); decision.className = 'decision'; decision.textContent = `最近决策：${reasonLabel(citizen.last_decision_reason)}`;
  root.append(grid, decision);
  if (citizen.recent_experiences?.length) {
    const memories = document.createElement('div'); memories.className = 'decision';
    memories.textContent = `最近发生：${citizen.recent_experiences.slice(-4).reverse().map((item) => `${eventLabel(item.type)}（第${item.day}日）`).join('；')}`;
    root.append(memories);
  }
  if (state.trace?.subject_id === citizen.id) {
    const trace = document.createElement('div'); trace.className = 'decision';
    const event = state.trace.events?.[0], transaction = state.trace.transactions?.[0];
    trace.textContent = event || transaction ? `最近一笔记录：${event ? `${eventLabel(event.type)}（第${event.day}日）` : ''}${event && transaction ? '；' : ''}${transaction ? `${transactionLabel(transaction.kind)} ${money(transaction.amount)}（第${transaction.day}日）` : ''}` : '最近一段时间没有记录到新的生活变化。';
    root.append(trace);
  }
}

function renderCitizenPicker() {
  const select = $('resident-select');
  const citizens = state.population.filter((citizen) => citizen?.id);
  const signature = citizens.map((citizen) => `${citizen.id}:${citizen.alive}`).join('|');
  if (select.dataset.signature !== signature) {
    const placeholder = document.createElement('option');
    placeholder.value = ''; placeholder.textContent = citizens.length ? `选择居民（${citizens.length} 人）` : '暂无居民';
    const options = citizens.map((citizen) => {
      const option = document.createElement('option');
      option.value = citizen.id;
      option.textContent = `${displayID(citizen.id)} · ${citizen.alive ? `${occupationLabel(citizen)}${citizen.employer_id ? ` · ${displayID(citizen.employer_id)}` : ''}` : '已经离世'}`;
      return option;
    });
    select.replaceChildren(placeholder, ...options);
    select.dataset.signature = signature;
  }
  select.value = state.selectedCitizen || '';
}

async function selectCitizen(id) {
  state.selectedCitizen = id; state.trace = null; state.inspectedCitizen = null; renderNetwork(); renderCitizenPicker(); renderCitizen();
  try {
    const detail = await loadCitizen(id);
    const trace = await state.wire.submit('society.experiment.trace', { subject_id: id, limit: 20 });
    if (state.selectedCitizen !== id) return;
    state.inspectedCitizen = detail; state.trace = trace; renderCitizen();
    requestAnimationFrame(() => $('citizen-panel').scrollIntoView({ behavior: 'smooth', block: 'center' }));
  }
  catch (error) { showError(error); }
}

async function loadCitizen(id) {
  return state.wire.submit('society.experiment.inspect', { subject_id: id });
}

function renderFirms() {
  const root = $('firm-list'); root.replaceChildren();
  const firms = state.firms, maxInventory = Math.max(1, ...firms.map((f) => f.inventory || 0));
  for (const firm of firms) {
    const row = document.createElement('div'); row.className = `firm-row ${firm.active ? '' : 'inactive'}`;
    const name = document.createElement('strong'); name.textContent = displayID(firm.id);
    const bar = document.createElement('div'); bar.className = 'bar'; const fill = document.createElement('i'); fill.style.width = `${Math.max(2, (firm.inventory || 0) / maxInventory * 100)}%`; bar.append(fill);
    const employees = document.createElement('span'); employees.textContent = `${firm.employee_count || 0} 人`;
    const balance = document.createElement('span'); balance.textContent = money(firm.balance);
    row.append(name, bar, employees, balance); root.append(row);
  }
}

function renderWealthDistribution() {
  const root = $('wealth-distribution'); root.replaceChildren();
  const balances = state.population.filter((c) => c.alive).map((c) => c.balance);
  if (!balances.length) return;
  const bins = Array(10).fill(0), max = Math.max(1, ...balances);
  for (const balance of balances) bins[Math.min(9, Math.floor(Math.max(0, balance) / max * 10))]++;
  const peak = Math.max(1, ...bins);
  bins.forEach((count, index) => {
    const bar = document.createElement('i');
    bar.style.height = `${Math.max(3, count / peak * 100)}%`;
    bar.title = `${money(max * index / 10)}–${money(max * (index + 1) / 10)}：${count} 人`;
    bar.setAttribute('aria-label', bar.title);
    root.append(bar);
  });
}

function renderTimeline() {
  const root = $('timeline'); root.replaceChildren(); $('event-count').textContent = `${state.events.length} 条`;
  for (const event of state.events) {
    const li = document.createElement('li'), time = document.createElement('time'), body = document.createElement('div'), title = document.createElement('strong'), detail = document.createElement('small');
    time.textContent = `第${event.day}日`; title.textContent = eventLabel(event.type); detail.textContent = `${(event.actors || []).map(displayID).join(' · ')}${event.reason ? ` — ${reasonLabel(event.reason)}` : ''}`;
    body.append(title, detail); li.append(time, body); root.append(li);
  }
}

function unique(prefix) { return `${prefix}-${crypto.randomUUID()}`; }
function asArray(value) {
  let current = value;
  for (let depth = 0; depth < 6; depth++) {
    if (Array.isArray(current)) return current;
    if (!current || typeof current !== 'object') return [];
    for (const key of ['items', 'values', 'entries', 'data']) {
      if (Array.isArray(current[key])) return current[key];
    }
    const values = Object.values(current);
    if (values.length !== 1) return values;
    current = values[0];
  }
  return Array.isArray(current) ? current : [];
}
function normalizeSnapshot(snapshot) {
  return {
    ...snapshot,
    citizens: asArray(snapshot?.citizens),
    firms: asArray(snapshot?.firms),
    households: asArray(snapshot?.households),
    communities: asArray(snapshot?.communities),
    relationships: asArray(snapshot?.relationships),
    information_edges: asArray(snapshot?.information_edges),
  };
}
function setBusy(busy) { document.querySelectorAll('button').forEach((button) => { if (busy) { button.dataset.wasDisabled = String(button.disabled); button.disabled = true; } else if (button.dataset.wasDisabled != null) { button.disabled = button.dataset.wasDisabled === 'true'; delete button.dataset.wasDisabled; } }); }
function setConnection(status) { const root = document.querySelector('.connection'); root.className = `connection ${status === 'connected' ? 'connected' : status === 'disconnected' ? 'error' : ''}`; $('connection-label').textContent = status === 'connected' ? '社会实验节点已连接' : status === 'disconnected' ? '节点连接已断开' : '正在连接节点'; }
function setMetric(id, value) { $(id).textContent = value ?? '—'; }
function percent(value) { return Number.isFinite(Number(value)) ? `${(Number(value) * 100).toFixed(1)}%` : '—'; }
function decimal(value) { return Number.isFinite(Number(value)) ? Number(value).toFixed(3) : '—'; }
function money(cents) { return `¥${(Number(cents || 0) / 100).toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`; }
function countAlive(citizens = []) { return citizens.filter((c) => c.alive).length; }
function svgEl(name, attrs) { const element = document.createElementNS(SVG, name); for (const [key, value] of Object.entries(attrs)) element.setAttribute(key, value); return element; }
function eventLabel(type = '') { return ({ 'society.day.completed': '今天的账算完了', 'society.wage.paid': '收到工资', 'society.benefit.paid': '收到生活补助', 'society.food.purchased': '买了今天的食物', 'society.need.unmet': '生活所需没有凑齐', 'society.household.supported': '家人帮了一把', 'society.credit.extended': '获得应急借款', 'society.credit.repaid': '还了一部分欠款', 'society.employment.started': '找到了新工作', 'society.employment.ended': '失去了工作', 'society.firm.closed': '公司停业', 'society.policy.changed': '新政策开始执行', 'society.shock.changed': '生活成本发生变化', 'society.citizen.born': '家庭迎来新成员', 'society.citizen.died': '居民离世', 'society.citizen.migrated': '搬到了新的社区' })[type] || '生活发生了变化'; }

function displayID(value = '') {
  const text = String(value || '');
  const citizen = text.match(/^citizen-(\d+)$/); if (citizen) return residentName(Number(citizen[1]));
  const firm = text.match(/^firm-(\d+)$/); if (firm) return firmName(Number(firm[1]));
  const household = text.match(/^household-(\d+)$/); if (household) return householdName(text, Number(household[1]));
  const community = text.match(/^community-(\d+)$/); if (community) return communityName(Number(community[1]));
  return ({ government: '公共财政', media: '公共媒体', bank: '公共银行' })[text] || (/[A-Za-z]/.test(text) ? '社会参与者' : text);
}

const givenResidentNames = ['周诚', '林晓', '陈启明'];
const residentSurnames = ['王', '李', '张', '刘', '陈', '杨', '赵', '黄', '周', '吴', '徐', '孙', '胡', '朱', '高', '林', '何', '郭', '马', '罗', '梁', '宋', '郑', '谢', '韩', '唐', '冯', '于', '董', '萧'];
const residentGivenNames = ['子安', '雨桐', '明远', '思齐', '嘉禾', '若溪', '景行', '语晨', '知夏', '书宁', '亦航', '清越', '安然', '沐阳', '嘉宁', '星遥', '以宁', '向晚', '予安', '舒言', '云舟', '念初', '可心', '泽宇', '清和', '佳音', '怀瑾', '乐川', '静姝', '文昊', '知意', '南乔', '修远', '锦书', '映雪', '天佑', '悦然', '星野', '芷晴', '嘉树'];
const firmNames = ['星河智造', '云帆物流', '远山能源', '春禾食品', '明川数据', '海岬医疗', '同路教育', '启新建筑', '青桥服务', '恒光电子'];
const communityNames = ['榕树里', '云桥坊', '青禾街区', '星港社区'];

function residentName(number) {
  if (number >= 1 && number <= givenResidentNames.length) return givenResidentNames[number - 1];
  const index = number - givenResidentNames.length - 1;
  if (index >= 0 && index < 117) return residentSurnames[index % residentSurnames.length] + residentGivenNames[(index * 7) % residentGivenNames.length];
  return `居民${String(number).padStart(3, '0')}`;
}

function firmName(number) { return firmNames[number - 1] || `第${number}企业`; }

function householdName(id, number) {
  const firstMember = state.population.find((citizen) => citizen.household_id === id);
  if (firstMember) return `${displayID(firstMember.id).slice(0, 1)}家`;
  return `和睦之家${number}`;
}

function communityName(number) { return communityNames[number - 1] || `新城第${number}街区`; }

function strategyLabel(strategy = '') {
  return ({ inactive: '安静度过生命最后阶段', dependent: '依靠家人照顾', 'secure-basic-needs': '先把吃住安排好', 'repay-debt': '先把欠款慢慢还上', 'seek-work': '正在找一份稳定工作', 'work-and-save': '安心工作，攒些钱' })[strategy] || (strategy ? '根据生活情况作打算' : '暂时没有特别打算');
}

const firmOccupations = {
  'firm-001': ['智能设备操作员', '质量检验员', '生产排程员', '机器维修员'],
  'firm-002': ['仓储调度员', '配送路线规划员', '分拣员', '货运司机'],
  'firm-003': ['电网巡检员', '储能设备管理员', '电气维修员', '用能顾问'],
  'firm-004': ['食品加工员', '食品检验员', '门店配货员', '采购员'],
  'firm-005': ['数据标注员', '系统维护员', '客户服务员', '数据审核员'],
  'firm-006': ['护理员', '药房管理员', '医疗设备维修员', '挂号员'],
  'firm-007': ['职业培训教师', '课程顾问', '教务员', '学习辅导员'],
  'firm-008': ['施工员', '建筑绘图员', '工地安全员', '材料管理员'],
  'firm-009': ['社区服务员', '家政协调员', '物业管理员', '老人照护员'],
  'firm-010': ['电子装配员', '电路检测员', '售后维修员', '生产组长'],
};
const betweenJobs = ['仓储调度员', '社区护理员', '餐馆帮厨', '网约车司机', '家电维修员', '小学教师', '平面设计师', '会计', '商店销售员', '建筑工人'];

function occupationLabel(citizen = {}) {
  const number = Number(String(citizen.id || '').match(/\d+/)?.[0] || 1);
  if (citizen.employer_id && firmOccupations[citizen.employer_id]) return firmOccupations[citizen.employer_id][(number - 1) % 4];
  return `${betweenJobs[(number - 1) % betweenJobs.length]}（目前待业）`;
}
function housingLabel(value) { const score = Number(value || 0); return score >= 8000 ? '住得稳定' : score >= 6000 ? '住处基本稳定' : score >= 4000 ? '住处有些不安稳' : '正为住处发愁'; }
function exposureLabel(value) { const score = Number(value || 0); return score >= 8000 ? '平时消息很灵通' : score >= 6000 ? '经常知道公共消息' : score >= 4000 ? '偶尔听到公共消息' : '很少收到公共消息'; }
function healthLabel(value) { const score = Number(value || 0); return score >= 8500 ? '身体很好' : score >= 7000 ? '总体健康' : score >= 5000 ? '偶尔有些不舒服' : score >= 2500 ? '健康状况欠佳' : '身体状况很危险'; }
function supportLabel(value) { const score = Number(value || 0); return score >= 7000 ? '比较信任' : score >= 5500 ? '基本认可' : score >= 4000 ? '有所保留' : '不太信任'; }
function opinionLabel(value) { const score = Number(value || 0); return score >= 2500 ? '希望增加公共保障' : score >= 500 ? '倾向把更多资源用于公共服务' : score > -500 ? '暂时没有明显倾向' : score > -2500 ? '更看重个人选择' : '反对扩大公共干预'; }
function needLabel(days) { return Number(days || 0) > 0 ? `已经 ${days} 天没能满足基本生活需要` : '最近吃住没有困难'; }

function reasonLabel(reason = '') {
  if (!reason) return '尚无记录';
  return ({
    'fixed daily settlement order completed': '今天的工资、消费和补助已经结算',
    'runtime intervention': '新的公共安排开始执行',
    'employment contract': '按照约定收到了今天的工资',
    'eligible unemployed resident': '目前没有工作，符合生活补助条件',
    'employed resident below the configured poverty line': '虽然有工作，但收入仍不够基本生活',
    'no seller had sufficient inventory': '附近商家都没有足够的食物',
    'available money was below the daily food cost': '手头的钱不够买今天的食物',
    'purchased from the least expensive available seller': '选择了有货且价格最低的商家',
    'basic-needs account lacked the staple-food price': '手头的钱不够买主食，因此获得应急借款',
    'employed resident repaid one tenth of net daily wage': '拿出今天工资的一成还了欠款',
    'household member covered a staple-food shortfall': '家人帮忙补上了买食物的钱',
    'health reached zero or age exceeded the simplified lifecycle': '健康归零或超过简化生命周期',
    'annual simplified lifecycle condition was met': '满足年度简化生育条件',
    'persistent unmet need triggered a move to the next community': '长期需求未获满足，迁往下一社区',
    'firm had capacity and resident was first eligible candidate': '企业有空缺岗位，他正好符合要求',
    'no employees and liquidity below ten daily wages': '公司没有员工，账上的钱也快用完了',
    'firm could not meet daily payroll': '公司发不出今天的工资',
    'resident lifecycle ended': '这位居民已经离世',
    'firm liquidity fell below one third of monthly payroll': '公司现金紧张，难以维持现有员工',
  })[reason] || (/[A-Za-z]/.test(reason) ? '模型规则触发' : reason);
}

function transactionLabel(kind = '') {
  return ({ wage: '工资', income_tax: '个人所得税', benefit: '生活福利', food: '食物消费', emergency_credit: '应急信贷', debt_repayment: '债务偿还', household_support: '家庭互助' })[kind] || '资金流动';
}
let toastTimer; function toast(message) { $('toast').textContent = message; $('toast').classList.add('show'); clearTimeout(toastTimer); toastTimer = setTimeout(() => $('toast').classList.remove('show'), 3200); }
function userMessage(message = '') { const text = String(message || ''); return text && !/[A-Za-z]/.test(text) ? text : '社会实验节点暂时不可用，请稍后再试'; }
function showError(error) { console.error(error); toast(userMessage(error?.message || error)); }

function renderBallot() {
  const counts = { public: 0, transition: 0, dividend: 0 };
  state.ballot.assignments.forEach((policy) => { if (policy) counts[policy] += 1; });
  for (const policy of Object.keys(counts)) $('allocation-' + policy).textContent = `${counts[policy]} 票`;
  const used = state.ballot.assignments.filter(Boolean).length;
  $('remaining-votes').textContent = used === 10 ? '十票已经分配' : `尚余 ${10 - used} 票`;
  $('submit-vote').disabled = used !== 10 || state.ballot.submitted;
  $('submit-vote').textContent = state.ballot.submitted ? '本期选票已封存' : '投下我的十票';
  document.querySelectorAll('[data-vote-action]').forEach((button) => {
    const policy = button.dataset.policy;
    button.disabled = state.ballot.submitted || (button.dataset.voteAction === 'add' ? used >= 10 : counts[policy] === 0);
  });
  renderPostVote();
}

function renderPostVote() {
  const root = $('post-vote');
  if (!state.ballot.submitted) { root.classList.add('hidden'); return; }
  root.classList.remove('hidden');
  const counts = { public: 0, transition: 0, dividend: 0 };
  state.ballot.assignments.forEach((policy) => { if (policy) counts[policy] += 1; });
  $('vote-receipt').textContent = `你的选择：公共人工智能基础设施 ${counts.public} 票，职业转型基金 ${counts.transition} 票，全民人工智能分红 ${counts.dividend} 票。`;
  const issue = state.issue || {};
  const totals = issue.totals || {};
  const options = [
    ['public', Number(totals.public_ai || 0)], ['transition', Number(totals.transition_fund || 0)], ['dividend', Number(totals.ai_dividend || 0)],
  ];
  const highest = Math.max(...options.map(([, votes]) => votes));
  const leaders = options.filter(([, votes]) => votes === highest && highest > 0).map(([policy]) => policyLabel(policy));
  const leadText = leaders.length === 1 ? `${leaders[0]}暂时领先` : leaders.length > 1 ? `${leaders.join('与')}暂时并列` : '目前还没有领先方案';
  $('vote-live').textContent = `本期仍在计票：${leadText}，已有 ${Number(issue.voter_count || 0).toLocaleString('zh-CN')} 人参与。票数还会继续变化。`;

  const residents = featuredResidents();
  const links = $('resident-links'); links.replaceChildren();
  residents.forEach((citizen, index) => {
    const button = document.createElement('button'); button.type = 'button'; button.className = 'resident-link-card';
    const eyebrow = document.createElement('span'); eyebrow.textContent = index === 0 ? '生活压力较大' : index === 1 ? '正在工作' : '家庭储蓄较多';
    const name = document.createElement('strong'); name.textContent = displayID(citizen.id);
    const situation = document.createElement('small'); situation.textContent = `${occupationLabel(citizen)}${citizen.employer_id ? ` · ${displayID(citizen.employer_id)}` : ''} · 手头存款 ${money(citizen.balance)}`;
    const action = document.createElement('b'); action.textContent = '查看他的生活';
    button.append(eyebrow, name, situation, action);
    button.addEventListener('click', async () => { openEvidence(false); await selectCitizen(citizen.id); });
    links.append(button);
  });
}

function featuredResidents() {
  const alive = state.population.filter((citizen) => citizen.alive).sort((a, b) => a.balance - b.balance);
  if (!alive.length) return [];
  const picks = [alive.find((citizen) => !citizen.employer_id), alive.find((citizen) => citizen.employer_id), [...alive].reverse().find((citizen) => citizen.employer_id)];
  const unique = [];
  for (const citizen of [...picks, ...alive]) {
    if (citizen && !unique.some((item) => item.id === citizen.id)) unique.push(citizen);
    if (unique.length === 3) break;
  }
  return unique;
}

function adjustBallot(policy, direction) {
  if (state.ballot.submitted) return;
  if (direction === 'add') {
    const empty = state.ballot.assignments.indexOf(null);
    if (empty >= 0) state.ballot.assignments[empty] = policy;
  } else {
    const assigned = state.ballot.assignments.lastIndexOf(policy);
    if (assigned >= 0) state.ballot.assignments[assigned] = null;
  }
  renderBallot();
}

function policyLabel(policy) { return ({ public: '公共人工智能基础设施', transition: '职业转型基金', dividend: '全民人工智能分红' })[policy] || ''; }

function renderIssue(issue) {
  if (!issue?.round_id) return;
  const roundChanged = state.ballot.roundId !== issue.round_id;
  state.issue = issue;
  if (roundChanged) {
    state.ballot.roundId = issue.round_id;
    state.ballot.assignments = Array(10).fill(null);
    state.ballot.submitted = false;
  }
  if (issue.my_ballot) {
    state.ballot.assignments = [
      ...Array(Number(issue.my_ballot.public_ai || 0)).fill('public'),
      ...Array(Number(issue.my_ballot.transition_fund || 0)).fill('transition'),
      ...Array(Number(issue.my_ballot.ai_dividend || 0)).fill('dividend'),
    ];
    state.ballot.submitted = true;
  }
  $('round-label').textContent = `第 ${issue.round_id} 期 · 第 ${issue.start_day}—${issue.end_day} 日`;
  $('countdown').textContent = `${Math.max(0, Number(issue.days_remaining || 0))} 日`;
  const totals = issue.totals || {};
  const total = Number(issue.total_votes || 0);
  const percentages = {
    public: total ? Math.round(Number(totals.public_ai || 0) / total * 100) : 0,
    transition: total ? Math.round(Number(totals.transition_fund || 0) / total * 100) : 0,
  };
  percentages.dividend = total ? Math.max(0, 100 - percentages.public - percentages.transition) : 0;
  for (const policy of Object.keys(percentages)) {
    $('pct-' + policy).textContent = `${percentages[policy]}%`;
    $('op-' + policy).style.width = `${percentages[policy]}%`;
  }
  $('public-count').textContent = `${Number(issue.voter_count || 0).toLocaleString('zh-CN')} 人参与`;
  const latest = issue.latest_result;
  $('latest-result').textContent = latest
    ? `上期结果：${latest.winner ? policyLabel(({ public_ai: 'public', transition_fund: 'transition', ai_dividend: 'dividend' })[latest.winner]) + '胜出并已生效' : '无有效票，政策维持不变'}。`
    : '首期议事进行中；封票后胜出方案将直接进入模拟。';
  renderBallot();
}

async function submitBallot() {
  if (state.ballot.assignments.some((policy) => !policy)) return;
  const counts = { public: 0, transition: 0, dividend: 0 };
  state.ballot.assignments.forEach((policy) => { counts[policy] += 1; });
  setBusy(true);
  let issue;
  try {
    issue = await state.wire.submit('society.experiment.vote', { command_id: unique('civic-vote'), public_ai: counts.public, transition_fund: counts.transition, ai_dividend: counts.dividend });
  } finally { setBusy(false); }
  renderIssue(issue);
  drawAttentionToPostVote();
  toast('选票已由实验节点封存，并进入本轮统一计票');
}

let attentionTimer;
function drawAttentionToPostVote() {
  const root = $('post-vote');
  clearTimeout(attentionTimer);
  root.classList.remove('attention');
  void root.offsetWidth;
  root.classList.add('attention');
  root.focus({ preventScroll: true });
  root.scrollIntoView({ behavior: 'smooth', block: 'center' });
  attentionTimer = setTimeout(() => root.classList.remove('attention'), 1800);
}

function openEvidence(shouldScroll = true) {
  $('evidence-room').classList.remove('hidden');
  if (shouldScroll) $('evidence-room').scrollIntoView({ behavior: 'smooth', block: 'start' });
  setTimeout(() => { renderChart(); renderNetwork(); }, 350);
}

document.querySelectorAll('[data-vote-action]').forEach((button) => button.addEventListener('click', () => adjustBallot(button.dataset.policy, button.dataset.voteAction)));
$('submit-vote').addEventListener('click', () => submitBallot().catch(showError));
$('show-evidence').addEventListener('click', () => openEvidence());
$('close-evidence').addEventListener('click', () => $('evidence-room').classList.add('hidden'));
$('open-resident').addEventListener('click', async () => {
  openEvidence(false);
  const resident = state.population.find((citizen) => citizen.id === 'citizen-001') || state.population.find((citizen) => citizen.alive);
  if (resident) await selectCitizen(resident.id);
});
$('resident-select').addEventListener('change', () => { if ($('resident-select').value) selectCitizen($('resident-select').value); });

boot();
renderBallot();
