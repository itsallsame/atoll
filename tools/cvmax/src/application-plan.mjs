import {createHash, randomUUID} from 'node:crypto';

const SECTION_RULES = [
  ['教育经历', /^(?:education|edu)(?:[._-]|$)/],
  ['实习经历', /^(?:internship|intern)(?:[._-]|$)/],
  ['工作经历', /^(?:employment|work|career)(?:[._-]|$)/],
  ['项目经历', /^(?:project)(?:[._-]|$)/],
  ['科研经历', /^(?:research)(?:[._-]|$)/],
  ['论文期刊', /^(?:publication|paper)(?:[._-]|$)/],
  ['发明专利', /^(?:patent|invention)(?:[._-]|$)/],
  ['校园经历', /^(?:campus|school_activity|student_activity)(?:[._-]|$)/],
  ['培训经历', /^(?:training)(?:[._-]|$)/],
  ['作品', /^(?:portfolio|work_sample)(?:[._-]|$)/],
  ['竞赛', /^(?:competition|contest)(?:[._-]|$)/],
  ['证书', /^(?:certificate|certification|license)(?:[._-]|$)/],
  ['语言能力', /^(?:language|skills?)(?:[._-]|$)/],
  ['自我评价', /^(?:summary|self_evaluation|self_assessment|objective)(?:[._-]|$)/],
  ['社交账号', /^(?:social|website|github|linkedin)(?:[._-]|$)/],
  ['荣誉奖励', /^(?:honor|award)(?:[._-]|$)/],
  ['其他信息', /^(?:other|additional)(?:[._-]|$)/]
];

const RECORD_PREFIX = /^(education|edu|internship|intern|employment|work|career|project|research|publication|paper|patent|campus|school_activity|student_activity|training|portfolio|work_sample|competition|contest|certificate|certification|license|language|skill|social|website|github|linkedin|honor|award)[._-](\d+)(?:[._-]|$)/;
const digest = value => createHash('sha256').update(JSON.stringify(value)).digest('hex');
const normalizedId = id => String(id ?? '').toLowerCase().replace(/^candidate[._-]/, '');

export function factSection(id) {
  const value = normalizedId(id);
  return SECTION_RULES.find(([, pattern]) => pattern.test(value))?.[0] ?? '基本信息';
}

export function factRecord(id) {
  const value = normalizedId(id), match = value.match(RECORD_PREFIX);
  return match ? `${match[1]}:${Number(match[2])}` : null;
}

export function createApplicationPlan({facts = [], attachment = null} = {}) {
  const obligations = facts.map(fact => ({
    obligationId: `fact:${fact.id}`,
    kind: 'fact',
    factId: fact.id,
    section: factSection(fact.id),
    record: factRecord(fact.id)
  }));
  if (attachment) obligations.unshift({obligationId:'attachment:resume', kind:'attachment', purpose:'resume', section:'简历信息', record:null});
  const scopeDigest = digest(obligations.map(({obligationId,kind,factId,purpose,section,record}) => ({obligationId,kind,factId,purpose,section,record})));
  return {schemaVersion:1, planId:randomUUID(), scopeDigest, obligations};
}

export function createObligationLedger(plan) {
  if (!plan || plan.schemaVersion !== 1 || !Array.isArray(plan.obligations)) throw Error('application_plan_invalid');
  return {schemaVersion:1, planId:plan.planId, scopeDigest:plan.scopeDigest, entries:Object.fromEntries(plan.obligations.map(item => [item.obligationId, {obligationId:item.obligationId, status:'pending', evidence:[]}]))};
}

export function extendApplicationPlan(plan, ledger, snapshot = {}) {
  if (!plan || !ledger || ledger.planId !== plan.planId) throw Error('application_plan_ledger_mismatch');
  const candidate=createApplicationPlan(snapshot),known=new Set(plan.obligations.map(item=>item.obligationId)),added=candidate.obligations.filter(item=>!known.has(item.obligationId));
  if(!added.length)return false;
  plan.obligations.push(...added);
  plan.scopeDigest=digest(plan.obligations.map(({obligationId,kind,factId,purpose,section,record})=>({obligationId,kind,factId,purpose,section,record})));
  ledger.scopeDigest=plan.scopeDigest;
  for(const item of added)ledger.entries[item.obligationId]={obligationId:item.obligationId,status:'pending',evidence:[]};
  return true;
}

export function recordObligation(ledger, obligationId, status, evidence) {
  const allowed = new Set(['pending','verified','site_no_field','unsupported','not_applicable']);
  if (!ledger?.entries?.[obligationId] || !allowed.has(status)) throw Error('obligation_update_invalid');
  if (status !== 'pending' && (!evidence || typeof evidence.kind !== 'string' || !evidence.kind)) throw Error('obligation_evidence_required');
  const entry = ledger.entries[obligationId];
  if (entry.status === 'verified' && status !== 'verified' && !(status === 'pending' && evidence?.kind === 'readback_lost')) throw Error('verified_obligation_cannot_regress');
  entry.status = status;
  if (evidence) {
    const item = structuredClone(evidence);
    if (!entry.evidence.some(existing => digest(existing) === digest(item))) entry.evidence.push(item);
  }
  return entry;
}

export function assessApplicationPlan(plan, ledger) {
  if (!plan || !ledger || ledger.planId !== plan.planId || ledger.scopeDigest !== plan.scopeDigest) throw Error('application_plan_ledger_mismatch');
  const entries = plan.obligations.map(obligation => ({...obligation, ...(ledger.entries[obligation.obligationId] ?? {status:'pending',evidence:[]})}));
  const resolved = entries.filter(item => item.status !== 'pending'), pending = entries.filter(item => item.status === 'pending');
  return {
    complete: pending.length === 0,
    total: entries.length,
    resolved: resolved.length,
    pending,
    verified: entries.filter(item => item.status === 'verified'),
    exceptions: entries.filter(item => ['site_no_field','unsupported','not_applicable'].includes(item.status)),
    sections: [...new Set(entries.map(item => item.section))]
  };
}
