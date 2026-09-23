import {digest} from './learning.mjs';
const fieldKinds = new Set(['fill','select','choose','check']);
const postconditionKinds = new Set(['start','next','section','add','activate']);
const shape = s => [s.url,s.loginRequired,s.captcha,!!s.activeDialog,s.activeSection,
  (s.fields??[]).map(f=>[f.label,f.type,f.group,f.semanticId]),(s.controls??[]).map(c=>[c.label,c.kind,c.active])];

export function verifyEffect(before, after, actions, valueVerifier) {
  const result = (status, reason) => ({schemaVersion:1,status,reason,
    evidenceDigest:digest([before?.evidence?.interactionDigest,after?.evidence?.interactionDigest,reason]),
    fromDocument:before?.documentEpoch,toDocument:after?.documentEpoch});
  if (!before || !after || !actions?.length) return result('unknown','missing_observation');
  // Concrete retained values prove effects even when the page structure is identical.
  if (actions.every(a => fieldKinds.has(a.kind))) {
    return actions.every(a => valueVerifier(a, after)) ? result('passed','retained_values') : result('failed','values_not_retained');
  }
  if (actions.every(a => ['upload','native_parse'].includes(a.kind))) {
    return actions.every(a => valueVerifier(a, after)) ? result('passed','attachment_or_parse_verified') : result('unknown','attachment_pending');
  }
  if(actions.some(a=>a.expectedPostcondition==='dialog_closed'&&after.activeDialog&&(!a.beforeDialogRef||after.activeDialog.ref===a.beforeDialogRef)))return result('unknown','expected_dialog_close_pending');
  if(actions.some(a=>a.expectedPostcondition==='fields_increased')&&(after.fields?.length??0)<=(before.fields?.length??0))return result('unknown','expected_fields_pending');
  if(actions.every(a=>a.kind==='section')&&actions.some(a=>a.section&&a.section!==after.activeSection))return result('unknown','expected_section_not_observed');
  if (actions.every(a=>a.kind==='add') && (after.fields?.length??0)>(before.fields?.length??0)) return result('passed','fields_added');
  if (actions.some(a => a.kind === 'section') && after.activeSection && after.activeSection !== before.activeSection)
    return result('passed','section_changed');
  if (after.loginRequired && !before.loginRequired || after.captcha && !before.captcha)
    return result('passed','user_gate_observed');
  if (after.activeDialog && !before.activeDialog)
    return result('passed','interaction_surface_observed');
  if (actions.every(a=>a.kind==='activate') && before.activeDialog && !after.activeDialog && after.evidence?.complete)
    return result('passed','dialog_closed');
  // The service's action verifier owns action-specific business postconditions.
  // These remain valid when a large page makes generic structural evidence incomplete.
  if(typeof valueVerifier==='function'&&actions.every(a=>postconditionKinds.has(a.kind))&&actions.every(a=>valueVerifier(a,after)))
    return result('passed','action_postcondition_verified');
  if (after.evidence?.complete !== true || before.evidence?.complete !== true) return result('unknown','incomplete_evidence');
  if(after.url!==before.url){if(!after.fields?.length&&!after.controls?.length&&!after.activeDialog)return result('unknown','uninterpreted_destination');return result('passed','route_changed');}
  if (after.evidence.interactionDigest !== before.evidence.interactionDigest || digest(shape(before)) !== digest(shape(after)))
    return result('passed','observed_transition');
  // A new document with the same contents is not evidence of business progress.
  return result('failed','no_effect');
}

export function coverageKey(snapshot, field) {
  const peers=(snapshot.fields??[]).filter(f=>f.label===field.label&&f.group===field.group&&f.type===field.type);
  return digest([snapshot.url.split('?')[0],field.label,field.group,field.type,peers.indexOf(field)]);
}

export function buildCoverage(snapshot, facts, previous = []) {
  const entries = new Map(previous.map(item => [item.key,item]));
  for (const field of snapshot.fields ?? []) {
    if (field.manual || field.sensitive || field.type === 'file') continue;
    const matches = facts.filter(f => f.id === field.semanticId);
    const key = coverageKey(snapshot,field);
    const old = entries.get(key);
    const sameUnmapped=!!old&&old.semanticId===field.semanticId&&old.disposition!=='mapped';
    entries.set(key,{key,fieldRef:field.ref,label:field.label,semanticId:field.semanticId,
      factId:matches.length===1?matches[0].id:sameUnmapped?old?.factId??null:null,
      disposition:matches.length===1?'mapped':sameUnmapped?old?.disposition??'unreviewed':'unreviewed',
      value:field.value,checked:field.checked,type:field.type});
  }
  return [...entries.values()];
}
