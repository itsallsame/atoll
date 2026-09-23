import {createHash} from 'node:crypto';
import {interactionProtected} from './interaction-policy.mjs';
const clean=v=>String(v??'').replace(/\s+/g,' ').trim().toLowerCase();
export function evidenceAnchor(node,kind){return createHash('sha256').update(JSON.stringify(kind==='field'?[kind,clean(node.label),clean(node.type),clean(node.group),clean(node.anchorContext)]:[kind,clean(node.label),clean(node.role),clean(node.tag),clean(node.type),clean(node.surface)])).digest('hex')}
export function validateRules(rules=[]){
 if(!Array.isArray(rules)||rules.length>300)throw Error('interpretation_rules_invalid');
 return rules.map(r=>{if(!r||Object.keys(r).some(k=>!['kind','anchor','semanticId','ordinal','cardinality'].includes(k))||!['field','interaction'].includes(r.kind)||!/^[a-f0-9]{64}$/.test(r.anchor))throw Error('interpretation_rule_invalid');
 if(r.kind==='field'&&(!/^[a-z][a-z0-9_.-]{0,100}$/i.test(r.semanticId??'')||/@|\d{5}/.test(r.semanticId)))throw Error('interpretation_semantic_invalid');
 const repeated=r.ordinal!=null||r.cardinality!=null;if(repeated&&(r.kind!=='field'||!Number.isSafeInteger(r.ordinal)||!Number.isSafeInteger(r.cardinality)||r.ordinal<0||r.cardinality<2||r.ordinal>=r.cardinality))throw Error('interpretation_rule_invalid');
 if(r.kind==='interaction'&&r.semanticId!=null)throw Error('interpretation_rule_invalid');return structuredClone(r)});
}
export function proposeRules(snapshot,proposals,facts){
 const revision=proposeRuleRevision(snapshot,proposals,facts);if(revision.removals.length)throw Error('interpretation_removal_not_supported');return revision.rules;
}
export function proposeRuleRevision(snapshot,proposals,facts){
 if(!Array.isArray(proposals)||!proposals.length||proposals.length>100)throw Error('interpretation_proposal_invalid');
 const revisions=proposals.map((p,index)=>{if(!p||typeof p!=='object')throw Error(`interpretation_proposal_invalid:${index}:object_required`);if(!['field','interaction'].includes(p.kind))throw Error(`interpretation_proposal_invalid:${index}:kind_required`);if(typeof p.ref!=='string'||!p.ref)throw Error(`interpretation_proposal_invalid:${index}:ref_required`);if(p.remove!=null&&p.remove!==true)throw Error(`interpretation_proposal_invalid:${index}:remove_invalid`);const pool=p.kind==='field'?snapshot.rawFields??snapshot.fields??[]:snapshot.interactions??[],node=pool.find(n=>n.ref===p.ref);if(!node||node.manual||node.sensitive||p.kind==='interaction'&&interactionProtected(node))throw Error('interpretation_target_forbidden');
 const anchor=evidenceAnchor(node,p.kind),matches=pool.filter(n=>evidenceAnchor(n,p.kind)===anchor);if(matches.length!==1&&p.kind!=='field')throw Error('interpretation_target_ambiguous');
 const selector={kind:p.kind,anchor,...p.kind==='field'&&matches.length>1&&{ordinal:matches.indexOf(node),cardinality:matches.length}};if(p.remove===true){if(p.semanticId!=null)throw Error('interpretation_proposal_invalid');return{remove:selector,ref:p.ref}}
 if(p.kind==='field'&&!facts.some(f=>f.id===p.semanticId))throw Error('interpretation_fact_unknown');
 return{rule:{...selector,...p.kind==='field'&&{semanticId:p.semanticId}},ref:p.ref}});
 return{rules:validateRules(revisions.flatMap(item=>item.rule?[item.rule]:[])),removals:revisions.flatMap(item=>item.remove?[{...item.remove,ref:item.ref}]:[])};
}
export function mergeRules(a=[],b=[]){const map=new Map();for(const r of [...validateRules(a),...validateRules(b)]){const key=[r.kind,r.anchor,r.ordinal??null].join(':'),old=map.get(key);if(old&&JSON.stringify(old)!==JSON.stringify(r))throw Error('interpretation_rule_conflict');map.set(key,r)}return [...map.values()]}
export function extendRulesForVerifiedAppend(before,after,rules=[]){
 const current=validateRules(rules);if(!before?.documentEpoch||before.documentEpoch!==after?.documentEpoch)return current;
 const prior=before.rawFields??before.fields??[],next=after.rawFields??after.fields??[],counts=new Map();
 for(const rule of current){if(rule.kind!=='field'||counts.has(rule.anchor))continue;const oldPeers=prior.filter(field=>evidenceAnchor(field,'field')===rule.anchor),newPeers=next.filter(field=>evidenceAnchor(field,'field')===rule.anchor),expected=rule.cardinality??1;if(oldPeers.length!==expected||newPeers.length<=oldPeers.length||!oldPeers.every((field,index)=>field.ref===newPeers[index].ref&&clean(field.value)===clean(newPeers[index].value))||!newPeers.slice(oldPeers.length).every(field=>!clean(field.value)))continue;counts.set(rule.anchor,{from:expected,to:newPeers.length})}
 return counts.size?validateRules(current.map(rule=>{const count=counts.get(rule.anchor);return count&&(rule.cardinality??1)===count.from?{...rule,ordinal:rule.ordinal??0,cardinality:count.to}:rule})):current;
}
export function applyRules(snapshot,rules=[]){
 const result=structuredClone(snapshot),raw=result.rawFields??result.fields??[];
 for(const rule of validateRules(rules)){const pool=rule.kind==='field'?raw:result.interactions??[],matches=pool.filter(n=>evidenceAnchor(n,rule.kind)===rule.anchor),node=rule.cardinality!=null&&matches.length===rule.cardinality?matches[rule.ordinal]:matches.length===1&&rule.ordinal==null?matches[0]:null;if(!node)continue;
 if(rule.kind==='field'){if(node.sensitive||node.manual)continue;const mapped={...node,semanticId:rule.semanticId};result.fields??=[];const index=result.fields.findIndex(n=>n.ref===node.ref);if(index<0)result.fields.push(mapped);else result.fields[index]=mapped;
 }else if(!interactionProtected(node))node.interpreted=true;
 }
 if(result.fields?.length&&rules.some(r=>r.kind==='field')&&!result.loginRequired&&!result.captcha){result.formStage='apply';result.applicationFieldCount=result.fields.filter(f=>!f.manual).length;}
 return result;
}
