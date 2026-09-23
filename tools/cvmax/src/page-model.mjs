import {createHash} from 'node:crypto';
import {validateRules,mergeRules,applyRules} from './interpretation-rules.mjs';
const clean = value => String(value ?? '').replace(/\s+/g, ' ').trim();

export const CANONICAL_SECTIONS = new Set(['基本信息','个人信息','简历信息','求职意向','教育经历','实习经历','工作经历','项目经历','科研经历','校园经历','培训经历','作品','竞赛','荣誉奖励','论文期刊','发明专利','证书','语言能力','自我评价','社交账号','其他信息','确认与提交']);
export const REPEATABLE_SECTIONS = new Set(['教育经历','实习经历','工作经历','项目经历','科研经历','校园经历','培训经历','作品','竞赛','荣誉奖励','论文期刊','发明专利','证书','语言能力','社交账号']);

export function publicCandidateKey(value) {
  if(/^sha256:[a-f0-9]{64}$/.test(value))return value;
  const key=clean(value).toLowerCase();
  if (!/^(?:heading|attribute|group|navigation):[^\r\n]{1,80}$/u.test(key)) throw Error('page_model_key_invalid');
  if (/(?:https?:\/\/|@)|\d{5,}/iu.test(key)) throw Error('page_model_key_private_or_volatile');
  return "sha256:"+createHash("sha256").update(key).digest("hex");
}

export function publicPageModel(model = {formatVersion:1,mappings:{}}) {
  if (model.formatVersion !== 1 || !model.mappings || typeof model.mappings !== 'object' || Array.isArray(model.mappings)) throw Error('page_model_invalid');
  const mappings={};
  for (const [rawKey,section] of Object.entries(model.mappings)) {
    const key=publicCandidateKey(rawKey),canonical=clean(section);
    if (!CANONICAL_SECTIONS.has(canonical)) throw Error('page_model_section_invalid');
    mappings[key]=canonical;
  }
  if (Object.keys(mappings).length>300) throw Error('page_model_too_large');
  return {formatVersion:1,mappings,...model.rules?.length&&{rules:validateRules(model.rules)}};
}

export function mergePageModels(first = {formatVersion:1,mappings:{}}, second = {formatVersion:1,mappings:{}}) {
  const a=publicPageModel(first),b=publicPageModel(second),mappings={...a.mappings},conflicts=new Set([...(first.conflicts??[]),...(second.conflicts??[])]);
  for (const [key,section] of Object.entries(b.mappings)) {
    if (conflicts.has(key)) continue;
    if (mappings[key] && mappings[key]!==section) { delete mappings[key];conflicts.add(key); }
    else mappings[key]=section;
  }
  return {formatVersion:1,mappings,...(a.rules?.length||b.rules?.length)&&{rules:mergeRules(a.rules,b.rules)},...conflicts.size&&{conflicts:[...conflicts].sort()}};
}

export function applyPageModel(snapshot, model) {
  const validated=publicPageModel(model),base=snapshot.rawFields?{...snapshot,fields:structuredClone(snapshot.rawFields)}:snapshot,result=applyRules(base,validated.rules),mappings=validated.mappings;
  result.controls=(result.controls??[]).map(control=>{
    if(!['add','section'].includes(control.kind)||!Array.isArray(control.sectionCandidates))return control;
    let match=null,matchKey=null;
    for(const candidate of control.sectionCandidates){
      try{const key=publicCandidateKey(candidate.key);if(mappings[key]){match=candidate;matchKey=key;break}}
      catch(error){if(!/^page_model_/.test(String(error.message??'')))throw error}
    }
    if(!match)return{...control,section:null,sectionKey:null,safe:false};
    const section=mappings[matchKey];
    return{...control,section,sectionKey:clean(match.key).toLowerCase(),safe:control.kind==='add'?REPEATABLE_SECTIONS.has(section):CANONICAL_SECTIONS.has(section)};
  });
  const active=result.controls.find(control=>control.kind==='section'&&control.active&&control.safe);result.activeSection=active?.section??null;result.activeSectionKey=active?.sectionKey??null;
  return result;
}

export function learnedPageModel(snapshot, actions = []) {
  const mappings={};
  for (const action of actions) {
    if(!['add','section'].includes(action.kind))continue;
    const control=(snapshot.controls??[]).find(item=>item.ref===action.controlRef);if(!action.sectionKey&&!control?.sectionCandidates?.length)continue;const key=publicCandidateKey(action.sectionKey),section=clean(action.section);
    if(!control?.sectionCandidates?.some(candidate=>clean(candidate.key).toLowerCase()===clean(action.sectionKey).toLowerCase())||!CANONICAL_SECTIONS.has(section)||action.kind==='add'&&!REPEATABLE_SECTIONS.has(section))throw Error('page_model_learning_evidence_invalid');
    if(mappings[key]&&mappings[key]!==section)throw Error('page_model_learning_conflict');
    mappings[key]=section;
  }
  return {formatVersion:1,mappings};
}
