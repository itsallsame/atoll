import {digest} from './learning.mjs';
// Unknown observations stay accessible to the learner rather than being discarded.
export function interpret(snapshot, observation) {
  const evidence=snapshot.evidence;
  const blockers=(evidence?.layers??[]).filter(layer=>layer.blocking!==false&&layer.controls?.length);
  const unresolved=[];
  if(!evidence?.complete)unresolved.push('observation_incomplete');
  if(blockers.length&&!snapshot.activeDialog)unresolved.push('unclassified_interaction_surface');
  return {schemaVersion:1,observationId:observation.id,mappingVersion:3,
    surface:snapshot.loginRequired?'login':snapshot.captcha?'captcha':snapshot.activeDialog?'interaction':snapshot.formStage,
    evidenceRefs:blockers.map(b=>b.ref),unresolved,
    signature:digest([snapshot.formStage,snapshot.pageState,blockers.map(b=>[b.explicit,b.position,b.controls.length])])};
}
export function inspectEvidence(application, observationId, ref=null, requestedRefs=null) {
  const observation=application.learning?.observations.find(o=>o.id===observationId&&o.runId===application.runId);
  if(!observation)throw Error('observation_not_in_current_run');
  const evidence=observation.evidence;
  if(!evidence)return {observationId,complete:false,nodes:[],reason:'evidence_unavailable'};
  const targets=requestedRefs??(ref?[ref]:null);
  if(targets&&(!Array.isArray(targets)||!targets.length||targets.length>40||targets.some(value=>typeof value!=='string'||!value)))throw Error('evidence_refs_invalid');
  if(targets?.some(value=>!evidence.nodes.some(n=>n.ref===value||n.parentRef===value)&&!evidence.layers.some(l=>l.ref===value)))throw Error('evidence_ref_unknown');
  const refs=new Set(targets??[]);
  if(targets){
    for(let depth=0;depth<12;depth++)for(const node of evidence.nodes)if(refs.has(node.parentRef))refs.add(node.ref);
    const nodes=new Map(evidence.nodes.map(node=>[node.ref,node]));for(const target of targets)for(let parent=nodes.get(target)?.parentRef,depth=0;parent&&depth<12;depth++){refs.add(parent);parent=nodes.get(parent)?.parentRef}
  }
  return {observationId,complete:observation.complete,nodes:evidence.nodes.filter(n=>!targets||refs.has(n.ref)),layers:evidence.layers.filter(l=>!targets||targets.includes(l.ref))};
}
