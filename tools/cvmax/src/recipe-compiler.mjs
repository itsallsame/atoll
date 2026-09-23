import {digest} from './learning.mjs';

export function candidateFrom(operation, verification, observationId) {
  return {schemaVersion:3,id:digest([operation.id,verification.evidenceDigest]),operationId:operation.id,
    status:verification.status==='passed'?'verified':'rejected',observationId,
    verification:structuredClone(verification),rebound:false};
}
export function compileCandidate(candidate, operation, target, {bind,record,recipes,runId}) {
  if(candidate.status!=='verified'||candidate.verification.status!=='passed')return null;
  // Re-bind every anchor against the source; a runtime ref alone is not portable knowledge.
  const staged=structuredClone(recipes);
  const result=record(staged,{sourceSnapshot:operation.sourceSnapshot,targetSnapshot:target,
    actions:operation.actions,runId,predecessorKind:operation.predecessorKind});
  if(!result)return null;
  const transition=Object.values(result.transitions).find(t=>t.successfulRuns?.includes(runId)&&t.lastVerifiedAt===result.updatedAt);
  if(!transition)return null;
  if(transition.actions.some(template=>!bind(template,operation.sourceSnapshot,
    operation.actions.filter(a=>a.factId).map(a=>({id:a.factId})),operation.actions.some(a=>['upload','native_parse'].includes(a.kind))?{}:null))){
    return null;
  }
  const source=operation.sourceSnapshot,facts=operation.actions.filter(a=>a.factId).map(a=>({id:a.factId})),attachment=operation.actions.some(a=>['upload','native_parse'].includes(a.kind))?{}:null;
  const negativePassed=transition.actions.every(template=>{const bound=bind(template,source,facts,attachment);if(!bound||!bound.fieldRef&&!bound.controlRef)return false;const absent=structuredClone(source);for(const key of ['fields','controls','interactions'])absent[key]=(absent[key]??[]).filter(node=>node.ref!==(bound.fieldRef??bound.controlRef));return !bind(template,absent,facts,attachment)});
  if(!negativePassed)return null;
  candidate.rebound=true;candidate.status='compiled';
  const live=staged[result.system.key].transitions[transition.id];
  live.successfulVariants=[...new Set([...(recipes[result.system.key]?.transitions?.[transition.id]?.successfulVariants??[]),...(operation.resumeVariant?[operation.resumeVariant]:[])])];
  live.prelearnRuns=[...new Set([...(recipes[result.system.key]?.transitions?.[transition.id]?.prelearnRuns??[]),...(operation.learningMode==='prelearn'?[runId]:[])])];
  live.verification={counterexamplePassed:negativePassed,independentVariants:live.successfulVariants.length,prelearnRuns:live.prelearnRuns.length,version:3,effect:candidate.verification.reason,anchorRebound:true,replayCount:Math.max(0,(live.successCount??1)-1)};
  recipes[result.system.key]=staged[result.system.key];
  return structuredClone(recipes[result.system.key]);
}

export function releaseCoverage(bundle) {
  const transitions=Object.values(bundle.transitions??{});
  const missing=transitions.filter(t=>t.status==='reusable'&&(!t.verification?.anchorRebound||t.verification.version!==3||!t.verification.effect||!(Number.isInteger(t.verification.replayCount)&&t.verification.replayCount>=1)||t.verification.counterexamplePassed!==true||!(Number.isInteger(t.verification.independentVariants)&&t.verification.independentVariants>=2)||!(Number.isInteger(t.verification.prelearnRuns)&&t.verification.prelearnRuns>=2))).map(t=>t.id);
  return {eligible:transitions.some(t=>t.status==='reusable')&&Object.keys(bundle.terminals??{}).length>0&&missing.length===0,missing};
}
