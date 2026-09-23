import {createHash} from 'node:crypto';
import {resumeParseConfirmationSignal,resumeReplacementSignal,resumeUploadAcknowledgementSignal} from './interaction-policy.mjs';
import {learnedPageModel,mergePageModels,publicPageModel} from './page-model.mjs';

const now = () => new Date().toISOString();
const hash = value => createHash('sha256').update(JSON.stringify(value)).digest('hex');
const clean = value => String(value ?? '').replace(/\s+/g, ' ').trim().toLowerCase();

function dynamicSegment(segment) {
  return /^\d{5,}$/.test(segment)
    || /^[0-9a-f]{8}-[0-9a-f-]{27,}$/i.test(segment)
    || /^[0-9a-f]{20,}$/i.test(segment);
}

export function routeTemplate(value) {
  const url = new URL(value);
  const parts = url.pathname.split('/');
  const mokaTenant = /^(?:campus|social)-recruitment$/i.test(parts[1] ?? '') && parts.length >= 4 && /^\d{5,}$/.test(parts[3] ?? '');
  const pathname = parts.map((part,index) => mokaTenant && index === 2 ? ':tenant' : dynamicSegment(part) ? ':id' : part).join('/');
  // Normalize whole hash-route segments. A substring replacement can corrupt
  // UUIDs whose first group happens to contain five consecutive digits.
  const hashRoute = url.hash
    ? url.hash.split('/').map(part => dynamicSegment(part) ? ':id' : part).join('/')
    : '';
  return `${pathname}${hashRoute}` || '/';
}

function fieldAnchor(field, fields) {
  const base = {
    label: clean(field.label).replace(/[＊*?？]+$/g, '').trim(),
    type: clean(field.type),
    group: clean(field.group),
  };
  const peers = fields.filter(item => clean(item.label).replace(/[＊*?？]+$/g, '').trim() === base.label
    && clean(item.type) === base.type && clean(item.group) === base.group);
  return {...base,label:hash(base.label),group:hash(base.group),cardinality:peers.length,ordinal:Math.max(0,peers.indexOf(field))};
}

function controlAnchor(control, controls) {
  const base = {
    kind: clean(control.kind), label: clean(control.label), section: clean(control.section), sectionKey:clean(control.sectionKey),
    target: clean(control.target), role: clean(control.role), tag: clean(control.tag),
    type: clean(control.type), surface: clean(control.surface), active:control.active === true,
  };
  const peers = controls.filter(item => clean(item.kind) === base.kind && clean(item.label) === base.label
    && clean(item.section) === base.section && clean(item.sectionKey) === base.sectionKey && clean(item.surface) === base.surface);
  return {...base,label:hash(base.label),role:hash(base.role),type:hash(base.type),target:hash(base.target),sectionKey:hash(base.sectionKey),cardinality:peers.length,ordinal:Math.max(0,peers.indexOf(control))};
}

function dialogShape(snapshot) {
  if (!snapshot?.activeDialog) return null;
  const text = `${snapshot.activeDialog.title ?? ''} ${snapshot.activeDialog.text ?? ''}`;
  const kind = snapshot.pageState === 'resume_prerequisite' ? 'resume_prerequisite'
    : resumeReplacementSignal(text) ? 'resume_replacement'
      : resumeUploadAcknowledgementSignal(text) ? 'resume_upload_acknowledgement'
        : resumeParseConfirmationSignal(text) ? 'resume_parse_confirmation'
        : 'unknown';
  return {
    role: clean(snapshot.activeDialog.role),
    kind,
  };
}

export function systemFor(snapshot) {
  const url = new URL(snapshot.url);
  const markers = [...new Set((snapshot.systemMarkers ?? []).map(clean).filter(Boolean).map(hash))].sort();
  const mokaRoute = /^\/(?:campus|social)-recruitment\/[^/]+\/\d{5,}\/?$/i.test(url.pathname)
    && /^#\/(?:job|jobs|page|resume|profile|personalCenter|apply)(?:\/|$)/i.test(url.hash);
  const engine = /(?:^|\.)mokahr\.com$/i.test(url.hostname) || mokaRoute ? 'moka' : null;
  const scope = engine ? `ats:${engine}` : url.origin;
  const key = hash(['recipe-system-learning-v3', scope, markers]);
  return {key, origin:url.origin, markers, ...engine && {engine}};
}

export function stateFor(snapshot, {predecessorKind = null} = {}) {
  const fields = snapshot.fields ?? [], controls = snapshot.controls ?? [];
  const surface = snapshot.loginRequired ? 'login'
    : snapshot.captcha ? 'captcha'
      : snapshot.activeDialog ? 'dialog'
        : fields.length || snapshot.formStage === 'apply' ? 'resume'
          : controls.some(control => control.kind === 'start') || snapshot.formStage === 'detail' ? 'entry'
            : 'unknown';
  // This is deliberately a small business milestone, not a standardized page.
  // Field meanings, values, arbitrary site questions and volatile DOM controls
  // cannot change workflow identity. Exact applicability is proven separately
  // by each transition's local action anchors.
  const descriptor = {
    route: hash(routeTemplate(snapshot.url)), surface, pageState:clean(snapshot.pageState),
    formStage:clean(snapshot.formStage), activeSection:clean(snapshot.activeSection),
    predecessorKind:clean(predecessorKind), dialog:dialogShape(snapshot),
    capabilities:{
      resumeUpload:fields.some(field=>clean(field.type)==='file'),
      finalSubmit:controls.some(control=>control.kind==='submit'),
      safeStart:controls.some(control=>control.kind==='start'&&control.safe!==false),
      safeNext:controls.some(control=>control.kind==='next'&&control.safe!==false),
    },
  };
  return {key:hash(['recipe-milestone-v1', descriptor]), descriptor};
}

function transitionGuard(snapshot, actions, predecessorKind = null) {
  const milestone=stateFor(snapshot,{predecessorKind});
  return {version:1,milestone:milestone.descriptor,requires:actions.map(action=>({
    kind:action.kind,
    ...action.fieldAnchor&&{fieldAnchor:action.fieldAnchor},
    ...action.controlAnchor&&{controlAnchor:action.controlAnchor},
  }))};
}

function guardMatches(guard, snapshot, options = {}) {
  if(!guard||guard.version!==1)return false;
  if(stateFor(snapshot,options).key!==hash(['recipe-milestone-v1',guard.milestone]))return false;
  const fields=snapshot.fields??[],controls=[...(snapshot.controls??[]),...(snapshot.interactions??[])];
  return (guard.requires??[]).every(requirement=>{
    if(requirement.fieldAnchor)return !!matchAnchor(fields,requirement.fieldAnchor,fieldAnchor);
    if(requirement.controlAnchor)return !!matchAnchor(controls,requirement.controlAnchor,controlAnchor);
    return ['navigate'].includes(requirement.kind);
  });
}

function terminalDescriptor(descriptor) {
  return {
    ...structuredClone(descriptor),
    // A terminal is a property of the verified current page and fact coverage,
    // not of the action that happened to reach it. The predecessor remains part
    // of ordinary transition state identity because it disambiguates workflow
    // dialogs, but carrying it into a terminal makes an already-correct form
    // miss solely because it was reached by upload instead of fill (or vice
    // versa).
    predecessorKind:'',
  };
}

function terminalStateFor(snapshot, options = {}) {
  const state=stateFor(snapshot,options),descriptor=terminalDescriptor(state.descriptor);
  return {key:hash(['recipe-terminal-state-v3',descriptor]),descriptor};
}

function publicAction(action, sourceSnapshot) {
  const fields = sourceSnapshot.fields ?? [], controls = [...(sourceSnapshot.controls ?? []), ...(sourceSnapshot.interactions ?? [])];
  const field = fields.find(item => item.ref === action.fieldRef);
  const control = controls.find(item => item.ref === action.controlRef);
  const result = {
    kind:action.kind, factId:action.factId ?? null, section:action.section ?? null,
    activationMode:action.activationMode ?? null, expectedPostcondition:action.expectedPostcondition ?? null,
    purpose:action.purpose ?? null,
    fieldAnchor:field ? fieldAnchor(field, fields) : null,
    controlAnchor:control ? controlAnchor(control, controls) : null,
    targetRoute:action.kind === 'navigate' && action.targetURL ? routeTemplate(action.targetURL) : null,
  };
  return Object.fromEntries(Object.entries(result).filter(([,value]) => value != null));
}

function matchAnchor(items, anchor, create) {
  return items.find(item => {
    const candidate=create(item,items);
    return Object.entries(anchor).every(([key,value]) => candidate[key] === value);
  }) ?? null;
}

export function bindAction(template, snapshot, facts = [], attachment = null) {
  const action = {kind:template.kind, ...template.factId && {factId:template.factId}, ...template.section && {section:template.section},
    ...template.activationMode && {activationMode:template.activationMode},
    ...template.expectedPostcondition && {expectedPostcondition:template.expectedPostcondition}, ...template.purpose && {purpose:template.purpose}};
  if (template.fieldAnchor) {
    const field=matchAnchor(snapshot.fields ?? [],template.fieldAnchor,fieldAnchor);
    if (!field) return null;
    action.fieldRef=field.ref;
  }
  if (template.controlAnchor) {
    const controls=[...(snapshot.controls ?? []), ...(snapshot.interactions ?? [])];
    const control=matchAnchor(controls,template.controlAnchor,controlAnchor);
    if (!control) return null;
    action.controlRef=control.ref;
  }
  if (template.factId && !facts.some(fact => fact.id === template.factId)) return null;
  if (['upload','native_parse'].includes(template.kind) && !attachment) return null;
  if (template.kind === 'navigate') {
    if (!template.targetRoute || template.targetRoute.includes(':id')) return null;
    action.targetURL=new URL(template.targetRoute,new URL(snapshot.url).origin).href;
  }
  return action;
}

export function selectRecipe(recipes, snapshot, options = {}) {
  const system=systemFor(snapshot), state=stateFor(snapshot,options), recipe=recipes?.[system.key];
  if (!recipe || recipe.status === 'suspended') return {status:'cold',system,state,recipe:null,transition:null,actions:[]};
  const outgoing=Object.values(recipe.transitions ?? {}).filter(item => item.status === 'reusable' && guardMatches(item.guard,snapshot,options));
  const candidates=outgoing.filter(item => typeof options.transitionAllowed!=='function'||options.transitionAllowed(item)!==false);
  const rebound=candidates.map(transition=>({transition,actions:transition.actions.map(action=>bindAction(action,snapshot,options.facts,options.attachment))}));
  const bound=rebound.filter(item=>item.actions.every(Boolean));
  const actionable=typeof options.actionNeeded==='function'?bound.map(item=>({...item,actions:item.actions.filter(action=>options.actionNeeded(action)!==false)})).filter(item=>item.actions.length):bound;
  const transition=deterministicOutgoing(actionable.map(item=>item.transition));
  if (!transition) {
    const status=actionable.length?'ambiguous':bound.length?'transition_satisfied':rebound.length?'binding_failed':outgoing.length?'guarded_transition_unavailable':'missing_transition';
    return {status,system,state,recipe,transition:null,actions:[]};
  }
  const actions=actionable.find(item=>item.transition.id===transition.id)?.actions??[];
  return {status:'reusable',system,state,recipe,transition,actions:actions.map(action => ({...action,recipeTransitionId:transition.id}))};
}

export function deterministicOutgoing(candidates = []) {
  const progressing=candidates.filter(item=>item.to!==item.from),pool=progressing.length?progressing:candidates;
  if(pool.length===1)return pool[0];
  if(pool.length&&new Set(pool.map(item=>JSON.stringify(item.actions))).size===1)return{...pool[0],expectedStates:[...new Set(pool.map(item=>item.to))],branchIds:pool.map(item=>item.id)};
  // Form values deliberately do not participate in milestone identity, so a
  // learned sequence of field edits appears as several distinct self-loops.
  // Replay those safe edits in the order in which they were first verified.
  // Once an edit is satisfied actionNeeded removes it, exposing the next one.
  // Structural/page-changing branches remain ambiguous and still require an
  // exact guard or fresh learning.
  const fieldKinds=new Set(['fill','select','choose','check']);
  if(pool.length&&pool.every(item=>item.from===item.to&&(item.actions??[]).length>0&&item.actions.every(action=>fieldKinds.has(action.kind)))){
    return [...pool].sort((a,b)=>String(a.firstVerifiedAt??'').localeCompare(String(b.firstVerifiedAt??''))||String(a.id).localeCompare(String(b.id)))[0];
  }
  return null;
}

export function recordVerifiedTransition(recipes, {sourceSnapshot, targetSnapshot, actions, runId, predecessorKind = null}) {
  if (!sourceSnapshot || !targetSnapshot || !Array.isArray(actions) || !actions.length || !runId) return null;
  const learned=mergePageModels(sourceSnapshot.interpretationModel??{formatVersion:1,mappings:{}},learnedPageModel(sourceSnapshot,actions)),source=sourceSnapshot,target=targetSnapshot,system=systemFor(source);
  if (new URL(targetSnapshot.url).origin !== system.origin) return null;
  const from=stateFor(source,{predecessorKind}), to=stateFor(target,{predecessorKind:actions.at(-1)?.kind});
  const templates=actions.map(action => publicAction(action,source));
  const guard=transitionGuard(source,templates,predecessorKind);
  const id=hash(['recipe-transition-v4',system.key,guard,templates,to.key]);
  const timestamp=now(), recipe=recipes[system.key] ?? {formatVersion:2,recipeId:system.key,system,status:'learning',pageModel:{formatVersion:1,mappings:{}},states:{},transitions:{},createdAt:timestamp,updatedAt:timestamp};
  recipe.pageModel=mergePageModels(recipe.pageModel??{formatVersion:1,mappings:{}},learned);
  recipe.states[from.key]=from.descriptor;recipe.states[to.key]=to.descriptor;
  const previous=recipe.transitions[id], successfulRuns=[...new Set([...(previous?.successfulRuns ?? []),runId])].slice(-20);
  recipe.formatVersion=2;
  recipe.transitions[id]={id,from:from.key,to:to.key,guard,actions:templates,successfulRuns,successCount:successfulRuns.length,
    failureCount:previous?.failureCount ?? 0,status:'reusable',firstVerifiedAt:previous?.firstVerifiedAt ?? timestamp,lastVerifiedAt:timestamp};
  recipe.status=Object.values(recipe.transitions).some(item => item.status === 'reusable') ? 'reusable' : 'learning';
  recipe.updatedAt=timestamp;recipes[system.key]=recipe;return structuredClone(recipe);
}

export function suspendRecipeTransition(recipes, transitionId, reason = 'transition_failed') {
  for (const recipe of Object.values(recipes ?? {})) {
    const transition=recipe.transitions?.[transitionId];if(!transition)continue;
    transition.status='suspended';transition.failureCount=(transition.failureCount ?? 0)+1;
    transition.lastFailure={code:String(reason).slice(0,96),at:now()};
    recipe.status=Object.values(recipe.transitions).some(item=>item.status==='reusable')?'reusable':'suspended';recipe.updatedAt=now();
    return {accepted:true,recipeId:recipe.recipeId,transitionId};
  }
  return {accepted:false,reason:'recipe_transition_not_found'};
}

export function markRecipeTerminal(recipes, snapshot, {predecessorKind = null, coverageFactIds = []} = {}) {
  const system=systemFor(snapshot),recipe=recipes?.[system.key];if(!recipe)return null;
  const coverage=[...new Set(coverageFactIds.filter(value=>typeof value==='string'&&value))].sort();if(!coverage.length)return null;
  const state=terminalStateFor(snapshot,{predecessorKind});recipe.states[state.key]=state.descriptor;
  recipe.terminals??={};recipe.terminals[state.key]={status:'ready_for_review',coverageVersion:3,coverageFactIds:coverage,verifiedAt:now()};recipe.updatedAt=now();
  return structuredClone(recipe);
}

export function recipeTerminal(recipes, snapshot, options = {}) {
  const system=systemFor(snapshot),state=terminalStateFor(snapshot,options),recipe=recipes?.[system.key];
  const processed=new Set(options.processedFactIds??[]),eligible=terminal=>terminal?.coverageVersion===3&&terminal.coverageFactIds?.length&&terminal.coverageFactIds.every(id=>processed.has(id));
  const direct=recipe?.terminals?.[state.key];if(eligible(direct))return{recipe,state,terminal:direct};
  // Terminal identity is the exact canonical current-state descriptor. The map
  // key is only an index and may have been produced before the page was reached
  // through another valid predecessor. Compare canonical descriptors exactly;
  // never fall back to similarity or partial-field matching.
  const descriptor=JSON.stringify(state.descriptor);
  for(const [key,terminal] of Object.entries(recipe?.terminals??{})){
    const stored=recipe.states?.[key];
    if(stored&&JSON.stringify(terminalDescriptor(stored))===descriptor&&eligible(terminal))return{recipe,state,terminal};
  }
  return null;
}

export function mergeRecipes(base = {}, incoming = {}) {
  const result=structuredClone(base);
  for(const [key,next] of Object.entries(incoming)){
    if(next?.formatVersion!==2)continue;
    const current=result[key];if(!current){result[key]=structuredClone(next);continue}
    current.pageModel=mergePageModels(current.pageModel??{formatVersion:1,mappings:{}},next.pageModel??{formatVersion:1,mappings:{}});current.states={...current.states,...structuredClone(next.states ?? {})};
    for(const [id,transition] of Object.entries(next.transitions ?? {})){
      const previous=current.transitions[id];
      if(!previous){current.transitions[id]=structuredClone(transition);continue}
      const successfulRuns=[...new Set([...(previous.successfulRuns ?? []),...(transition.successfulRuns ?? [])])].slice(-20);
      current.transitions[id]={...previous,...transition,successfulRuns,successCount:Math.max(previous.successCount ?? 0,transition.successCount ?? 0,successfulRuns.length),failureCount:Math.max(previous.failureCount ?? 0,transition.failureCount ?? 0),status:previous.status==='suspended'||transition.status==='suspended'?'suspended':previous.status==='reusable'||transition.status==='reusable'?'reusable':'candidate'};
    }
    current.terminals={...(current.terminals??{}),...structuredClone(next.terminals??{})};
    current.status=Object.values(current.transitions).some(item=>item.status==='reusable')?'reusable':'learning';current.updatedAt=[current.updatedAt,next.updatedAt].sort().at(-1);
  }
  return result;
}

export function publicRecipe(recipe) {
  if (recipe?.formatVersion!==2 || !recipe?.recipeId || !recipe?.system?.key || !recipe?.transitions) throw Error('recipe_invalid');
  const allowed=['formatVersion','recipeId','system','status','pageModel','states','transitions','terminals','createdAt','updatedAt'];
  if(Object.keys(recipe).some(key=>!allowed.includes(key)))throw Error('private_recipe_value');
  for(const table of [recipe.states??{},recipe.transitions??{},recipe.terminals??{}])if(Object.keys(table).some(key=>!/^[a-f0-9]{64}$/.test(key)))throw Error('recipe_key_invalid');
  const result=structuredClone(recipe);
  const checks=[
    [result.system,['key','origin','markers','engine']],
    ...Object.values(result.transitions).map(t=>[t,['id','from','to','guard','actions','successfulRuns','successfulVariants','prelearnRuns','successCount','failureCount','status','firstVerifiedAt','lastVerifiedAt','lastFailure','verification']]),
    ...Object.values(result.transitions).flatMap(t=>t.actions.map(a=>[a,['kind','factId','section','activationMode','expectedPostcondition','purpose','fieldAnchor','controlAnchor','targetRoute']])),
    ...Object.values(result.states??{}).map(st=>[st,['route','surface','pageState','formStage','activeSection','predecessorKind','dialog','capabilities']])
  ];
  const fieldKeys=['label','type','group','cardinality','ordinal'],controlKeys=['kind','label','section','sectionKey','target','role','tag','type','surface','active','cardinality','ordinal'];
  for(const state of Object.values(result.states??{})){if(state.dialog)checks.push([state.dialog,['role','kind']]);if(state.capabilities)checks.push([state.capabilities,['resumeUpload','finalSubmit','safeStart','safeNext']]);}
  for(const transition of Object.values(result.transitions)){if(transition.verification)checks.push([transition.verification,['counterexamplePassed','independentVariants','prelearnRuns','version','effect','anchorRebound','replayCount']]);if(transition.lastFailure)checks.push([transition.lastFailure,['code','at']]);for(const action of transition.actions){if(action.fieldAnchor)checks.push([action.fieldAnchor,fieldKeys]);if(action.controlAnchor)checks.push([action.controlAnchor,controlKeys]);}}
  for(const transition of Object.values(result.transitions)){if(transition.guard){checks.push([transition.guard,['version','milestone','requires']]);checks.push([transition.guard.milestone,['route','surface','pageState','formStage','activeSection','predecessorKind','dialog','capabilities']]);if(transition.guard.milestone.dialog)checks.push([transition.guard.milestone.dialog,['role','kind']]);if(transition.guard.milestone.capabilities)checks.push([transition.guard.milestone.capabilities,['resumeUpload','finalSubmit','safeStart','safeNext']]);for(const requirement of transition.guard.requires??[]){checks.push([requirement,['kind','fieldAnchor','controlAnchor']]);if(requirement.fieldAnchor)checks.push([requirement.fieldAnchor,fieldKeys]);if(requirement.controlAnchor)checks.push([requirement.controlAnchor,controlKeys]);}}}
  for(const term of Object.values(result.terminals??{}))checks.push([term,['status','coverageVersion','coverageFactIds','verifiedAt']]);
  for(const [object,keys] of checks)if(Object.keys(object).some(key=>!keys.includes(key)))throw Error('private_recipe_value');
  result.pageModel=publicPageModel(result.pageModel??{formatVersion:1,mappings:{}});
  for(const transition of Object.values(result.transitions)){delete transition.successfulRuns;delete transition.successfulVariants;delete transition.prelearnRuns;}
  const encoded=JSON.stringify(result);
  for(const key of ['"value"','"before"','"filename"','"facts"','"attachment"'])if(encoded.includes(key))throw Error('private_recipe_value');
  return result;
}
