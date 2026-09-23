import {publicCandidateKey} from '../src/page-model.mjs';
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {CvMaxService} from '../src/service.mjs';
import {evidenceAnchor} from '../src/interpretation-rules.mjs';
import {markRecipeTerminal,recordVerifiedTransition,stateFor,systemFor} from '../src/recipe.mjs';

const wait=()=>new Promise(resolve=>setTimeout(resolve,25));

test('service carries repeated field bindings across a verified append but not an unverified change',()=>{
 const oldFields=[{ref:'one',label:'项目名称',type:'text',value:'项目一'},{ref:'two',label:'项目名称',type:'text',value:'项目二'}],rule={kind:'field',anchor:evidenceAnchor(oldFields[0],'field'),ordinal:0,cardinality:2,semanticId:'project_1_name'},before={documentEpoch:'doc',rawFields:oldFields},after={documentEpoch:'doc',rawFields:[...oldFields,{ref:'three',label:'项目名称',type:'text',value:''}]},app={operation:{state:'verified',verification:{status:'passed'},actions:[{kind:'add'}],sourceSnapshot:before},interpretationModel:{formatVersion:1,mappings:{},rules:[rule]}};
 const actual=CvMaxService.prototype.appendedInterpretationModel(app,after);assert.equal(actual.rules[0].cardinality,3);app.operation.verification.status='failed';assert.equal(CvMaxService.prototype.appendedInterpretationModel(app,after),null);
});

function fixture(fn){return async()=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cvmax-recipe-service-'));
 const snapshot={url:'https://jobs.example.test/position/123456/apply',formStage:'apply',pageState:'normal',documentEpoch:'doc-1',fields:[{ref:'runtime-name',label:'姓名',semanticId:'candidate.name',group:'基本信息',value:'',type:'text'}],controls:[],interactions:[],canAdvance:false,completionVerifiable:false};
 const calls=[];let browser=async(action,args)=>{calls.push({action,args:structuredClone(args)});if(action==='observe')return structuredClone(snapshot);if(action==='act'){const field=snapshot.fields.find(item=>item.ref===args.actionSpec.fieldRef);if(field)field.value=args.actionSpec.value;const raw=snapshot.rawFields?.find(item=>item.ref===args.actionSpec.fieldRef);if(raw)raw.value=args.actionSpec.value;return{accepted:true}}if(action==='stop')return{stopped:true};throw Error(`unexpected_browser_${action}`)};
 const config={file:path.join(dir,'state.json'),browser:(...args)=>browser(...args),browserStatus:()=>true,recipeTransitionTimeoutMs:100,recipeTransitionIntervalMs:5,attachments:{root:path.join(dir,'attachments'),prefix:'cvmax:',read(){return{filename:'resume.pdf',mime:'application/pdf',sha256:'a'.repeat(64),bytes:Buffer.from('resume')}},persistResume(_ref,id,revision){return{resource:`saved:${id}/${revision}.pdf`,sha256:'a'.repeat(64),size:6,mime:'application/pdf',scope:'resume'}}}};
 let service=new CvMaxService(config);
 try{await fn({get service(){return service},restart(overrides={}){service=new CvMaxService({...config,...overrides})},snapshot,calls,setBrowser(value){browser=value}})}finally{fs.rmSync(dir,{recursive:true,force:true})}
}}

function create(service,id='one',url='https://jobs.example.test/position/123456/apply'){
 return service.create({clientRequestId:id,targets:[{url,tabId:1}],facts:[{id:'candidate.name',value:'测试姓名',sourceRef:'synthetic:test'}]});
}
function createWithResume(service,id='resume',url='https://jobs.example.test/position/123456/apply'){
 return service.create({clientRequestId:id,targets:[{url,tabId:1}],facts:[{id:'candidate.name',value:'测试姓名',sourceRef:'synthetic:test'}],attachment:{resource:'test'}});
}
function markNativeParsed(service,application,snapshot,fields=snapshot.fields){
 const beforeFields=fields.filter(field=>field.type!=='file').map(field=>({key:service.fieldKey(snapshot,field),value:'',checked:null})),operation={id:`native-${Math.random()}`,state:'verified',actions:[{kind:'native_parse',fieldRef:'resume-file',beforeFields}],index:1,evidence:[{action:{kind:'native_parse'},evidence:{accepted:true}}],createdAt:new Date().toISOString(),sourceSnapshot:structuredClone(snapshot)};
 application.operation=operation;application.operations=[operation];application.resumeObservations=[];
 service.recordResumeObservation(application,snapshot,{observationId:'native-parsed-1'});service.recordResumeObservation(application,snapshot,{observationId:'native-parsed-2'});
}
async function ready(service,id='one'){let task=create(service,id);task=service.claim({taskId:task.taskId,expectedRevision:task.revision});return(await service.observe({taskId:task.taskId})).task}

async function learnOneField(f){
 let task=await ready(f.service,'learn');
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});await wait();
 task=(await f.service.observe({taskId:task.taskId})).task;
 f.service.finish({taskId:task.taskId,expectedRevision:task.revision,outcome:'partial'});
 return task.taskId;
}

test('obsolete service state is rejected instead of migrated',()=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cvmax-obsolete-state-')),file=path.join(dir,'state.json');
 try{fs.writeFileSync(file,JSON.stringify({version:21,tasks:{},requests:{},recipes:{},resumes:{},resumeRequests:{},intakes:{},metrics:{events:[],aggregates:{}}}));assert.throws(()=>new CvMaxService({file,browser:async()=>{},attachments:{}}),/state_schema_invalid/)}finally{fs.rmSync(dir,{recursive:true,force:true})}
});

test('a cold page returns to the hosted agent without browser writes',fixture(async f=>{
 const task=create(f.service),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'adaptive');assert.equal(result.reason,'recipe_cold');assert.equal(f.calls.some(item=>item.action==='act'),false);
}));

test('removed learner leaves an unknown page untouched and reports the gap',fixture(async f=>{
 const task=create(f.service,'learner-removed');
 const result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'learning_unavailable');
 assert.equal(result.task.state,'needs_attention');
 assert.equal(f.calls.some(item=>item.action==='act'),false);
 assert.equal(result.finalSubmitAllowed,false);
}));

test('a cold learning handoff forbids repeated recipe observation until a hypothesis advances it',fixture(async f=>{
 const task=create(f.service,'cold-handoff'),first=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision}),reads=f.calls.filter(item=>item.action==='observe').length;
 assert.equal(first.learningHandoff.repeatRunRecipeAllowed,false);assert.equal(first.learningHandoff.required,'propose');
 const [{controlRef,...coverageRow}]=first.learningContext.coverage;assert.match(controlRef,/^[a-f0-9]{64}$/);assert.deepEqual(coverageRow,{fieldRef:'runtime-name',label:'姓名',semanticId:'candidate.name',factId:'candidate.name',disposition:'mapped',type:'text',verified:false});assert.equal(first.learningContext.fields[0].ref,'runtime-name');assert.equal(Object.hasOwn(first.learningContext.coverage[0],'key'),false);
 assert.deepEqual(first.learningContext.suggestedActions,[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]);
 const second=await f.service.run_recipe({taskId:task.taskId,expectedRevision:first.task.revision});
 assert.equal(second.path,'adaptive');assert.equal(second.reason,'learning_step_required');assert.deepEqual(second.learningHandoff,first.learningHandoff);assert.deepEqual(second.learningContext,first.learningContext);assert.equal(second.task.revision,first.task.revision);assert.equal(f.calls.filter(item=>item.action==='observe').length,reads);
 const live=f.service.get(task.taskId),actions=[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}],proposal=f.service.learning_propose({taskId:task.taskId,expectedRevision:second.task.revision,observationId:first.learningHandoff.observationId,question:'姓名字段可否填写？',hypothesis:'该字段映射到简历姓名。',expected:'字段回读为简历姓名。',actions});
 assert.equal(f.service.get(task.taskId).applications[0].learningHandoff,undefined);assert.ok(proposal.episodeId);assert.equal(live.taskId,task.taskId);
}));

test('an upload-only handoff after native parsing re-observes a hydrating application once',fixture(async f=>{
 const task=create(f.service,'hydrating-native-result'),first=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision}),application=f.service.get(task.taskId).applications[0],reads=f.calls.filter(item=>item.action==='observe').length;
 application.snapshot={...structuredClone(application.snapshot),applicationFieldCount:1,fields:[{ref:'resume-file',label:'上传简历',type:'file',manual:false}],controls:[]};
 application.observationId='hydrating-observation';
 application.learningHandoff={...first.learningHandoff,observationId:application.observationId,required:'propose',resumeStrategy:{kind:'native_parse_complete',reason:'key_resume_facts_verified'}};
 const result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:first.task.revision});
 assert.equal(f.calls.filter(item=>item.action==='observe').length,reads+1);
 assert.equal(result.path,'adaptive');
 assert.deepEqual(result.learningContext.suggestedActions,[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]);
 assert.equal(application.hydrationProbeDocumentEpoch,'doc-1');
}));

test('recipe execution retries one read-only browser timeout inside the same claimed run',fixture(async f=>{
 let reads=0;f.service.sleep=async()=>{};f.setBrowser(async action=>{if(action!=='observe')throw Error(action);reads++;if(reads===1)throw Error('browser_timeout');return structuredClone(f.snapshot)});
 const task=create(f.service,'read-timeout-retry'),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'adaptive');assert.equal(result.reason,'recipe_cold');assert.equal(reads>=2,true);assert.equal(result.task.state,'running');
}));

test('a missing browser tab rebinds and retries one empty first observation',fixture(async f=>{
 let reads=0;
 f.setBrowser(async(action,args)=>{
  f.calls.push({action,args:structuredClone(args)});
  if(action==='observe'){
   reads++;
   if(reads===1)throw Error('No tab with id: 1.');
   if(reads===2)return undefined;
   assert.equal(args.tabId,2);
   return {...structuredClone(f.snapshot),url:args.targetURL};
  }
  if(action==='open')return{tabId:2,url:args.targetURL};
  throw Error(`unexpected_browser_${action}`);
 });
 let task=create(f.service,'rebind-empty-observation');
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});
 const observed=await f.service.observe({taskId:task.taskId});
 assert.equal(observed.snapshot.url,f.snapshot.url);
 assert.equal(observed.task.applications[0].tabId,2);
 assert.equal(reads,3);
}));

test('observations for one task are serialized under one state-machine writer',fixture(async f=>{
 let active=0,maxActive=0,reads=0;
 f.setBrowser(async action=>{if(action!=='observe')throw Error(action);reads++;active++;maxActive=Math.max(maxActive,active);await new Promise(resolve=>setTimeout(resolve,10));active--;return structuredClone(f.snapshot)});
 let task=create(f.service,'serialized-observe');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});
 await Promise.all([f.service.observe({taskId:task.taskId}),f.service.observe({taskId:task.taskId})]);
 assert.equal(maxActive,1);assert.equal(reads,2);assert.ok(f.service.get(task.taskId).applications[0].observationId);
}));

test('visual learning evidence is task-bound, private, bounded, and excluded from public recipes',fixture(async f=>{
 const task=await ready(f.service,'private-visual-evidence'),application=f.service.get(task.taskId).applications[0],jpeg=Buffer.from([0xff,0xd8,0xff,0xd9]);
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='screenshot')return{dataUrl:`data:image/jpeg;base64,${jpeg.toString('base64')}`,capturedAt:Date.now()};throw Error(`unexpected_browser_${action}`)});
 await assert.rejects(f.service.learning_screenshot({taskId:task.taskId,observationId:'stale'}),/visual_evidence_not_allowed/);
 const evidence=await f.service.learning_screenshot({taskId:task.taskId,observationId:application.observationId});
 assert.equal(evidence.scope,'private_task');assert.equal(evidence.publicRecipeEligible,false);assert.equal(evidence.mimeType,'image/jpeg');assert.equal(evidence.size,4);assert.equal(fs.readFileSync(evidence.path).equals(jpeg),true);assert.equal(typeof evidence.imageBase64,'string');
 assert.equal(f.calls.at(-1).args.taskId,task.taskId);assert.equal(f.calls.at(-1).args.tabId,1);assert.equal(f.calls.at(-1).args.runId,application.runId);
}));

test('learning context includes bounded nearby labels without observed values',fixture(async f=>{
 f.snapshot.fields[0]={ref:'runtime-name',label:'请输入',semanticId:null,group:null,value:'测试姓名',type:'text'};
 f.snapshot.evidence={schemaVersion:1,complete:true,interactionDigest:'generic-field',layers:[],nodes:[
  {ref:'field-wrap',parentRef:'form',interactive:false,text:'公司名称 测试姓名'},
  {ref:'field-label',parentRef:'field-wrap',interactive:false,text:'公司名称'},
  {ref:'runtime-name',parentRef:'field-wrap',interactive:true,text:''}
 ]};
 const task=await ready(f.service,'nearby-labels'),application=f.service.get(task.taskId).applications[0],field=f.service.learningContextFor(application,f.service.get(task.taskId)).fields[0];
 assert.deepEqual(field.nearbyLabels,['公司名称']);
 assert.equal(JSON.stringify(field.nearbyLabels).includes('测试姓名'),false);
}));

test('read-only form inputs remain visible for learning but cannot be filled as native inputs',fixture(async f=>{
 f.snapshot.fields[0]={...f.snapshot.fields[0],type:'date',readOnly:true,value:'',semanticId:'candidate.name'};
 const task=await ready(f.service,'read-only-learning'),live=f.service.get(task.taskId),application=live.applications[0],context=f.service.learningContextFor(application,live);
 assert.equal(context.fields[0].readOnly,true);
 assert.deepEqual(context.suggestedActions,[]);
 await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]}),/read_only_field_requires_site_control/);
 assert.equal(f.calls.filter(call=>call.action==='act').length,0);
}));

test('a mapped read-only calendar field is selected through the site picker and month readback is normalized',fixture(async f=>{
 f.snapshot.fields=[{ref:'education-start',label:'开始日期',semanticId:'education_1_start',group:'教育经历',value:'2011-09-01',type:'text',readOnly:true}];
 let task=f.service.create({clientRequestId:'date-picker-selection',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'education_1_start',value:'2020-09',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const live=f.service.get(task.taskId),context=f.service.learningContextFor(live.applications[0],live);
 assert.deepEqual(context.suggestedActions,[{kind:'select',fieldRef:'education-start',factId:'education_1_start'}]);
 assert.equal(f.service.fieldValueSatisfied({value:'2020-09-01',type:'text'},'2020-09','education_1_start'),true);
 await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'education-start',factId:'education_1_start'}]}),/read_only_field_requires_site_control/);
}));

test('month-only end dates require the last day in a day-granular site picker',fixture(async f=>{const end={label:'结束日期',type:'text',readOnly:true,value:'2023-04-01'};assert.equal(f.service.fieldValue(end,'2023-04'),'2023-04-30');assert.equal(f.service.fieldValueSatisfied(end,'2023-04','campus_1_end'),false);end.value='2023-04-30';assert.equal(f.service.fieldValueSatisfied(end,'2023-04','campus_1_end'),true);assert.equal(f.service.fieldValue({label:'开始日期',type:'text',readOnly:true},'2023-04'),'2023-04-01')}));

test('an old accepted date action cannot override a conflicting current field readback',fixture(async f=>{f.snapshot.fields=[{ref:'campus-end',label:'结束日期',semanticId:'campus_1_end',group:'section:校园经历',value:'2023-04-01',type:'text',readOnly:true}];let task=f.service.create({clientRequestId:'stale-calendar-receipt',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'campus_1_end',value:'2023-04',sourceRef:'synthetic:test'}]});task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;const application=f.service.get(task.taskId).applications[0],row=application.coverage.find(item=>item.fieldRef==='campus-end'),fact=f.service.get(task.taskId).factSnapshot.facts[0];row.disposition='mapped';row.factId=fact.id;application.operations.push({id:'old-calendar',sourceSnapshot:structuredClone(application.snapshot)});application.evidence.push({operationId:'old-calendar',action:{kind:'select',fieldRef:'campus-end',factId:fact.id,value:'2023-04'},evidence:{accepted:true}});assert.equal(f.service.coverageRowVerified(application,row,fact),false)}));

test('field anchors use stable structural labels instead of mutable values or expanded options',fixture(async f=>{
 const base={url:f.snapshot.url,fields:[{ref:'desc',label:'请填写工作描述，可用数据说明成果。',type:'textarea',value:'旧内容'},{ref:'language',label:'未标注字段',type:'search',value:'英语'}],evidence:{schemaVersion:1,complete:true,interactionDigest:'contexts',layers:[],nodes:[
  {ref:'desc',parentRef:'desc-wrap',interactive:true,text:''},{ref:'desc-wrap',parentRef:'desc-row',interactive:false,text:'旧内容请填写工作描述'},{ref:'desc-row',parentRef:'form',interactive:false,text:'*工作描述:旧内容请填写工作描述'},
  {ref:'language',parentRef:'language-inner',interactive:true,text:'英语'},{ref:'language-inner',parentRef:'language-row',interactive:false,text:'英语'},{ref:'language-row',parentRef:'form',interactive:false,text:'*外语语种:英语请选择外语语种英语日语韩语德语法语'}
 ]}};
 const before=f.service.withFieldContexts(structuredClone(base));base.fields[0].value='新内容';base.fields[1].value='日语';base.evidence.nodes.find(node=>node.ref==='desc-wrap').text='新内容请填写工作描述';base.evidence.nodes.find(node=>node.ref==='desc-row').text='*工作描述:新内容请填写工作描述';base.evidence.nodes.find(node=>node.ref==='language-inner').text='日语';base.evidence.nodes.find(node=>node.ref==='language-row').text='*外语语种:日语请选择外语语种英语日语韩语德语法语阿拉伯语';
 const after=f.service.withFieldContexts(structuredClone(base));
 assert.deepEqual(before.fields.map(field=>field.anchorContext),['工作描述','外语语种']);assert.deepEqual(after.fields.map(field=>field.anchorContext),['工作描述','外语语种']);
}));

test('refreshed inspection preserves the observation identity and merges evidence',fixture(async f=>{
 f.snapshot.fields[0].semanticId=null;f.snapshot.rawFields=structuredClone(f.snapshot.fields);f.snapshot.evidence={schemaVersion:1,complete:true,interactionDigest:'initial',layers:[],nodes:[{ref:'runtime-name',parentRef:'field-wrap',interactive:true,text:''}]};
 const task=await ready(f.service,'stable-inspection'),application=f.service.get(task.taskId).applications[0],observationId=application.observationId;
 f.setBrowser(async(action,args)=>{if(action==='inspect')return{documentEpoch:args.documentEpoch,evidence:{schemaVersion:1,complete:true,interactionDigest:'refreshed',layers:[],nodes:[{ref:'field-wrap',parentRef:'form',interactive:false,text:'姓名'},{ref:'runtime-name',parentRef:'field-wrap',interactive:true,text:''}]}};throw Error(action)});
 const inspected=await f.service.learning_inspect({taskId:task.taskId,observationId,ref:'runtime-name',refresh:true});
 assert.equal(inspected.observationId,observationId);assert.equal(f.service.get(task.taskId).applications[0].observationId,observationId);assert.equal(inspected.nodes.some(node=>node.ref==='field-wrap'),true);
 const interpreted=f.service.learning_interpret({taskId:task.taskId,expectedRevision:inspected.task.revision,observationId,mappings:[{kind:'field',ref:'runtime-name',semanticId:'candidate.name'}]});assert.equal(interpreted.observationId,observationId);
}));

test('a fieldless workflow step with a safe next control stays available',fixture(async f=>{
 const application={};
 const snapshot={url:'https://jobs.example.test/volunteer',title:'志愿选择',pageState:'normal',activeDialog:null,fields:[],controls:[{ref:'next',kind:'next',label:'下一步',safe:true}],interactions:[],loginRequired:false,captcha:false};
 const result=f.service.applicationAvailability(snapshot,application,{track:true});
 assert.equal(result.status,'available');
 assert.equal(result.eligible,false);
 assert.equal(application.emptyApplicationSurface,undefined);
}));

test('a duplicated visual application entry exposes only the service-approved safe action',fixture(async f=>{
 const snapshot={url:'https://jobs.example.test/detail',formStage:'detail',pageState:'normal',fields:[],controls:[{ref:'covered',kind:'start',intent:'application',label:'立即投递',safe:false},{ref:'visible',kind:'start',intent:'application',label:'立即投递',safe:true}],interactions:[]};
 assert.deepEqual(f.service.applicationEntryFor(snapshot),{status:'ready',reason:'unique_safe_application_start',controlRef:'visible',label:'立即投递',suggestedAction:{kind:'start',controlRef:'visible'}});
 assert.equal(f.service.applicationEntryFor({...snapshot,controls:snapshot.controls.map(control=>({...control,safe:true}))}).status,'ambiguous');
}));

test('a resume prerequisite exposes its unique safe editor as the next state-machine action',fixture(async f=>{
 const snapshot={url:'https://jobs.example.test/detail',formStage:'detail',pageState:'resume_prerequisite',fields:[],controls:[{ref:'edit',kind:'start',intent:'resume',label:'编辑',safe:true,role:'',tag:'p',type:''},{ref:'submit',kind:'start',intent:'application',label:'确认提交',safe:false}],interactions:[],evidence:{nodes:[{ref:'edit',hit:true,disabled:false}]}};
 assert.deepEqual(f.service.applicationEntryFor(snapshot),{status:'ready',reason:'unique_safe_resume_start',controlRef:'edit',label:'编辑',suggestedAction:{kind:'start',controlRef:'edit'}});
}));

test('an occluded canonical application entry rebinds only to one exact exposed interaction',fixture(async f=>{
 const controls=[{ref:'occluded',kind:'start',intent:'application',label:'立即投递',safe:true,role:'button',tag:'button',type:''},{ref:'exposed',kind:'start',intent:'application',label:'立即投递',safe:false,role:'button',tag:'button',type:''}],interactions=[{ref:'exposed',label:'立即投递',safe:true,role:'button',tag:'button',type:'',surface:'page'}],snapshot={url:'https://jobs.example.test/detail',formStage:'detail',pageState:'normal',fields:[],controls,interactions,evidence:{nodes:[{ref:'occluded',hit:false,disabled:false},{ref:'exposed',hit:true,disabled:false}]}};
 assert.deepEqual(f.service.applicationEntryFor(snapshot),{status:'ready',reason:'exposed_duplicate_rebound',controlRef:'exposed',label:'立即投递',suggestedAction:{kind:'activate',controlRef:'exposed',activationMode:'main_world_pointer',expectedPostcondition:'surface_changed',purpose:'application_start'}});
 assert.equal(controls[0].safe,true);assert.equal(controls[1].safe,false);assert.equal(interactions[0].interpreted,true);
 const ambiguous=structuredClone(snapshot);ambiguous.controls.push({...ambiguous.controls[1],ref:'another',safe:false});ambiguous.interactions.push({...ambiguous.interactions[0],ref:'another'});ambiguous.evidence.nodes.push({ref:'another',hit:true,disabled:false});
 assert.equal(f.service.applicationEntryFor(ambiguous).status,'missing');
}));

test('the service learns and verifies one unique exposed application entry before agent takeover',fixture(async f=>{
 f.snapshot.url='https://jobs.example.test/detail';f.snapshot.formStage='detail';f.snapshot.fields=[];f.snapshot.controls=[{ref:'occluded',kind:'start',intent:'application',label:'立即投递',safe:true,role:'button',tag:'button',type:''},{ref:'exposed',kind:'start',intent:'application',label:'立即投递',safe:false,role:'button',tag:'button',type:''}];f.snapshot.interactions=[{ref:'exposed',label:'立即投递',safe:true,role:'button',tag:'button',type:'',surface:'page'}];f.snapshot.evidence={schemaVersion:1,complete:true,interactionDigest:'entry',nodes:[{ref:'occluded',hit:false,disabled:false},{ref:'exposed',hit:true,disabled:false}],layers:[]};
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe')return structuredClone(f.snapshot);if(action==='act'){assert.equal(args.actionSpec.controlRef,'exposed');assert.equal(args.actionSpec.interactionAnchor.ordinal,0);f.snapshot.formStage='apply';f.snapshot.documentEpoch='doc-2';f.snapshot.fields=[{ref:'name',label:'姓名',semanticId:'candidate.name',group:'基本信息',value:'',type:'text'}];f.snapshot.controls=[];return{accepted:true}}if(action==='stop')return{stopped:true};throw Error(`unexpected_browser_${action}`)});
 const task=create(f.service,'service-entry-bootstrap',f.snapshot.url),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision}),application=f.service.get(task.taskId).applications[0];
 assert.equal(result.path,'adaptive');assert.equal(result.actionCount,1);assert.equal(f.calls.filter(item=>item.action==='act').length,1);assert.equal(application.learning.episodes.length,1);assert.equal(application.learning.episodes[0].verdict,'supported');
}));

test('a direct start anchor uses the actionable interaction ordinal, not the visual control ordinal',fixture(async f=>{
 f.snapshot.url='https://jobs.example.test/detail';f.snapshot.formStage='detail';f.snapshot.fields=[];f.snapshot.controls=[{ref:'covered',kind:'start',intent:'application',label:'立即投递',safe:false,role:'button',tag:'button',type:'',ordinal:0},{ref:'visible',kind:'start',intent:'application',label:'立即投递',safe:true,role:'button',tag:'button',type:'',ordinal:1}];f.snapshot.interactions=[{ref:'visible',label:'立即投递',safe:true,role:'button',tag:'button',type:'',surface:'page'}];f.snapshot.evidence={schemaVersion:1,complete:true,interactionDigest:'entry',nodes:[{ref:'covered',hit:false,disabled:false},{ref:'visible',hit:true,disabled:false}],layers:[]};
 f.setBrowser(async(action,args)=>{if(action==='observe')return structuredClone(f.snapshot);if(action==='act'){assert.equal(args.actionSpec.kind,'start');assert.equal(args.actionSpec.controlRef,'visible');assert.equal(args.actionSpec.startAnchor.ordinal,0);f.snapshot.formStage='apply';f.snapshot.documentEpoch='doc-2';f.snapshot.fields=[{ref:'name',label:'姓名',semanticId:'candidate.name',group:'基本信息',value:'',type:'text'}];f.snapshot.controls=[];return{accepted:true}}if(action==='stop')return{stopped:true};throw Error(action)});
 const task=create(f.service,'direct-start-actionable-ordinal',f.snapshot.url),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'adaptive');assert.equal(result.actionCount,1);
}));

test('a persistent empty loading overlay permits one controlled page refresh',fixture(async f=>{
 f.snapshot.formStage='apply';f.snapshot.pageState='unknown';f.snapshot.fields=[];f.snapshot.controls=[];
 f.snapshot.activeDialog={ref:'loading-layer',role:'dialog',title:'',text:''};
 f.snapshot.interactions=[{ref:'spinner',label:'animation',role:'button',tag:'div',type:'',surface:'dialog',safe:true,formSubmit:false}];
 f.snapshot.evidence={layers:[{ref:'loading-layer',blocking:true,controls:['spinner']}],nodes:[]};
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe')return structuredClone(f.snapshot);if(action==='refresh')return{accepted:true,refreshed:true,tabId:args.tabId,url:args.targetURL};if(action==='stop')return{stopped:true};throw Error(`unexpected_browser_${action}`)});
 let task=create(f.service,'persistent-loader');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});
 let observed=await f.service.observe({taskId:task.taskId});assert.equal(observed.recovery.status,'checking');
 const application=f.service.get(task.taskId).applications[0];application.loadingSurface.firstObservedAt=new Date(Date.now()-11_000).toISOString();f.service.store.write();
 observed=await f.service.observe({taskId:task.taskId});assert.equal(observed.recovery.reason,'persistent_loading_surface');
 const refreshed=await f.service.refresh({taskId:task.taskId,expectedRevision:observed.task.revision});
 assert.equal(refreshed.applications[0].budget.actions,1);assert.equal(application.refreshCount,1);assert.equal(f.calls.filter(item=>item.action==='refresh').length,1);
 observed=await f.service.observe({taskId:task.taskId});assert.equal(observed.recovery.status,'checking');application.loadingSurface.firstObservedAt=new Date(Date.now()-11_000).toISOString();f.service.store.write();
 observed=await f.service.observe({taskId:task.taskId});assert.equal(observed.recovery.reason,'persistent_loading_after_refresh');assert.equal(observed.task.state,'finished');assert.deepEqual(observed.task.applications[0].unsupported,['application_loading_failed']);
 await assert.rejects(f.service.refresh({taskId:task.taskId,expectedRevision:observed.task.revision}),/task_terminal/);
}));

test('unavailable terminal routes cannot erase a previously observed application form',fixture(async f=>{
 let task=create(f.service,'unavailable-after-form');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const application=f.service.get(task.taskId).applications[0],previousForm=structuredClone(f.snapshot);
 assert.equal(application.formObserved,true);
 application.sectionSnapshots={basic:previousForm};
 application.snapshot={...structuredClone(f.snapshot),fields:[],controls:[],interactions:[]};
 f.service.applicationAvailability(application.snapshot,application,{track:true});application.emptyApplicationSurface.observations=2;application.emptyApplicationSurface.firstObservedAt=new Date(Date.now()-30_000).toISOString();
 assert.equal(f.service.autonomousCompletionBoundary(application,f.service.get(task.taskId),[],'surface_unavailable').reason,'application_form_previously_observed');
 application.sectionSnapshots={};
 assert.throws(()=>f.service.finish_unavailable({taskId:task.taskId,expectedRevision:task.revision}),/application_form_previously_observed/);
 application.snapshot={...structuredClone(f.snapshot),fields:[],controls:[],pageState:'unknown',activeDialog:{ref:'loading',role:'dialog',title:'',text:''},interactions:[{ref:'spinner',label:'animation',role:'button',tag:'div',type:'',surface:'dialog',safe:true}],evidence:{layers:[{ref:'loading',blocking:true,controls:['spinner']}],nodes:[]}};
 application.refreshCount=1;f.service.loadingRecovery(application.snapshot,application,{track:true});application.loadingSurface.observations=2;application.loadingSurface.firstObservedAt=new Date(Date.now()-120_000).toISOString();
 assert.equal(f.service.autonomousCompletionBoundary(application,f.service.get(task.taskId),[],'loading_unavailable').reason,'application_form_previously_observed');
 assert.throws(()=>f.service.finish_loading_unavailable({taskId:task.taskId,expectedRevision:task.revision}),/application_form_previously_observed/);
 assert.equal(f.service.status({taskId:task.taskId}).state,'running');
}));

test('a late user answer creates a new immutable fact snapshot without exposing old private values',fixture(async f=>{
 let task=create(f.service,'answered-snapshot');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});
 const original=structuredClone(f.service.get(task.taskId).factSnapshot);
 f.service.get(task.taskId).applications[0].snapshot=structuredClone(f.snapshot);
 task=f.service.question({taskId:task.taskId,expectedRevision:task.revision,reason:'login',text:'请补充当前申请资料。',missing:['candidate.height'],fieldRefs:[]});
 const answer=f.service.answer({taskId:task.taskId,questionId:task.question.id,questionRevision:task.question.revision,commandId:'answer-snapshot-one',facts:[{id:'candidate.height',value:'180 cm',sourceRef:'user:explicit'}]});
 const stored=f.service.get(task.taskId);
 assert.notEqual(stored.factSnapshot.id,original.id);
 assert.equal(stored.factSnapshot.parentId,original.id);
 assert.equal(stored.factSnapshot.revision,2);
 assert.deepEqual(stored.factSnapshotHistory,[original]);
 assert.equal(stored.applications[0].applicationPlan.obligations.length,2);
 assert.equal(answer.task.factSnapshotHistory,undefined);
 assert.equal(JSON.stringify(answer.task).includes('180 cm'),false);
 f.restart();assert.deepEqual(f.service.get(task.taskId).factSnapshotHistory,[original]);
}));

test('a later application answer does not reopen an earlier completed application plan',fixture(async f=>{
 let task=f.service.create({clientRequestId:'later-answer',targets:[{url:'https://jobs.example.test/first',tabId:1},{url:'https://jobs.example.test/second',tabId:2}],facts:[{id:'candidate.name',value:'测试姓名',sourceRef:'synthetic:test'}]});
 const stored=f.service.get(task.taskId),first=stored.applications[0];first.outcome='skipped';stored.current=1;stored.state='running';stored.applications[1].snapshot=structuredClone(f.snapshot);const originalPlan=structuredClone(first.applicationPlan);
 task=f.service.question({taskId:task.taskId,expectedRevision:task.revision,reason:'login',text:'请补充当前申请资料。',missing:['candidate.height'],fieldRefs:[]});
 f.service.answer({taskId:task.taskId,questionId:task.question.id,questionRevision:task.question.revision,commandId:'later-answer-one',facts:[{id:'candidate.height',value:'180 cm',sourceRef:'user:explicit'}]});
 assert.deepEqual(first.applicationPlan,originalPlan);
 assert.equal(f.service.status({taskId:task.taskId}).applications[0].applicationPlan.total,1);
 assert.equal(f.service.status({taskId:task.taskId}).applications[1].applicationPlan.total,2);
}));

test('a section readback from an old document cannot settle or preserve a fact after reload',fixture(async f=>{
 const task=create(f.service,'stale-section-readback'),stored=f.service.get(task.taskId),application=stored.applications[0];
 const section={...structuredClone(f.snapshot),fields:[{ref:'name',label:'姓名',semanticId:'candidate.name',group:'基本信息',value:'测试姓名',type:'text'}]};
 application.snapshot={...structuredClone(f.snapshot),fields:[]};application.sectionSnapshots={basic:section};
 f.service.syncObligationLedger(application,stored);
 assert.equal(application.obligationLedger.entries['fact:candidate.name'].status,'verified');
 application.snapshot.documentEpoch='doc-2';f.service.syncObligationLedger(application,stored);
 assert.equal(f.service.aggregateSnapshot(application).fields.length,0);
 assert.equal(application.obligationLedger.entries['fact:candidate.name'].status,'pending');
}));

test('an unexplained dialog with text is never treated as a loading overlay',fixture(async f=>{
 const application={};f.snapshot.formStage='apply';f.snapshot.pageState='unknown';f.snapshot.fields=[];f.snapshot.controls=[];
 f.snapshot.activeDialog={ref:'dialog',role:'dialog',title:'提示',text:'请选择投递意愿'};
 f.snapshot.interactions=[{ref:'choice',label:'选择',role:'button',tag:'button',type:'button',surface:'dialog',safe:true}];
 f.snapshot.evidence={layers:[{ref:'dialog',blocking:true,controls:['choice']}],nodes:[]};
 assert.equal(f.service.loadingRecovery(f.snapshot,application,{track:true}).status,'not_applicable');
 assert.equal(application.loadingSurface,undefined);
}));

test('an invalid public page model fails closed without blocking observation',fixture(async f=>{
 const key=systemFor(f.snapshot).key;
 f.service.recipeRegistry={getReleased:async()=>({releaseId:'bad-release',releaseVersion:1,bundle:{formatVersion:2,recipeId:key,pageModel:{formatVersion:1,mappings:{invalid:'基本信息'}},states:{},transitions:{}}})};
 let task=create(f.service,'invalid-public-model');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});const observed=await f.service.observe({taskId:task.taskId});
 assert.equal(observed.snapshot.fields.length,1);assert.equal(f.service.publicRecipes[key],undefined);assert.equal(f.service.registryError,'page_model_key_invalid');
}));

test('an invalid cached public page model is removed before standardization',fixture(async f=>{
 const key=systemFor(f.snapshot).key;f.service.recipeRegistry={};f.service.publicRecipes[key]={formatVersion:2,recipeId:key,pageModel:{formatVersion:1,mappings:{invalid:'基本信息'}},states:{},transitions:{}};
 const standardized=f.service.standardizeSnapshot(structuredClone(f.snapshot));assert.equal(standardized.fields.length,1);assert.equal(f.service.publicRecipes[key],undefined);assert.equal(f.service.registryError,'page_model_key_invalid');
}));

test('an invalid public model for another system cannot poison the current recipe source',fixture(async f=>{
 const current=systemFor(f.snapshot).key,bad='f'.repeat(64);f.service.recipeRegistry={};f.service.publicRecipes[bad]={formatVersion:2,recipeId:bad,pageModel:{formatVersion:1,mappings:{invalid:'基本信息'}},states:{},transitions:{}};
 f.service.publicRecipes[current]={formatVersion:2,recipeId:current,pageModel:{formatVersion:1,mappings:{}},states:{},transitions:{}};
 const standardized=f.service.standardizeSnapshot(structuredClone(f.snapshot));assert.equal(standardized.fields.length,1);assert.equal(f.service.publicRecipes[bad],undefined);assert.ok(f.service.publicRecipes[current]);
}));

test('an invalid local page model is isolated before a public recipe is merged',fixture(async f=>{
 const key=systemFor(f.snapshot).key;
 f.service.store.data.recipes[key]={formatVersion:2,recipeId:key,system:systemFor(f.snapshot),status:'learning',pageModel:{formatVersion:1,mappings:{legacy:'基本信息'}},states:{},transitions:{},terminals:{}};
 f.service.recipeRegistry={};f.service.publicRecipes[key]={formatVersion:2,recipeId:key,system:systemFor(f.snapshot),status:'learning',pageModel:{formatVersion:1,mappings:{}},states:{},transitions:{},terminals:{}};
 const source=f.service.recipeSource();assert.ok(source[key]);assert.deepEqual(f.service.store.data.recipes[key].pageModel,{formatVersion:1,mappings:{}});assert.equal(f.service.registryError,'page_model_key_invalid');
}));

test('a public interpretation conflict is quarantined without blocking a local learning action',fixture(async f=>{
 const key=systemFor(f.snapshot).key,anchor='a'.repeat(64),base={formatVersion:2,recipeId:key,system:systemFor(f.snapshot),status:'learning',states:{},transitions:{},terminals:{}};
 f.service.store.data.recipes[key]={...structuredClone(base),pageModel:{formatVersion:1,mappings:{},rules:[{kind:'field',anchor,semanticId:'candidate.name'}]}};
 f.service.recipeRegistry={};f.service.publicRecipes[key]={...structuredClone(base),pageModel:{formatVersion:1,mappings:{},rules:[{kind:'field',anchor,semanticId:'candidate.email'}]}};f.service.publicReleaseMeta[key]={releaseId:'stale'};
 const source=f.service.recipeSource();assert.deepEqual(source[key].pageModel.rules,[{kind:'field',anchor,semanticId:'candidate.name'}]);assert.equal(f.service.publicRecipes[key],undefined);assert.equal(f.service.publicReleaseMeta[key],undefined);assert.equal(f.service.registryError,'interpretation_rule_conflict');
}));

test('an invalid local page model for another system cannot poison current selection',fixture(async f=>{
 const current=systemFor(f.snapshot).key,bad='e'.repeat(64);
 f.service.store.data.recipes[bad]={formatVersion:2,recipeId:bad,system:{key:bad,origin:'https://other.example.test',markers:[],engine:'unknown'},status:'learning',pageModel:{formatVersion:1,mappings:{legacy:'教育经历'}},states:{},transitions:{},terminals:{}};
 f.service.store.data.recipes[current]={formatVersion:2,recipeId:current,system:systemFor(f.snapshot),status:'learning',pageModel:{formatVersion:1,mappings:{}},states:{},transitions:{},terminals:{}};
 const standardized=f.service.standardizeSnapshot(structuredClone(f.snapshot));assert.equal(standardized.fields.length,1);assert.deepEqual(f.service.store.data.recipes[bad].pageModel,{formatVersion:1,mappings:{}});
}));

test('recipe execution owns loading recovery without handing timing to the agent',fixture(async f=>{
 f.snapshot.formStage='apply';f.snapshot.pageState='unknown';f.snapshot.fields=[];f.snapshot.controls=[];
 f.snapshot.activeDialog={ref:'loading-layer',role:'dialog',title:'',text:''};
 f.snapshot.interactions=[{ref:'spinner',label:'animation',role:'button',tag:'div',type:'',surface:'dialog',safe:true,formSubmit:false}];
 f.snapshot.evidence={layers:[{ref:'loading-layer',blocking:true,controls:['spinner']}],nodes:[]};
 f.service.loadingRecoveryWindowMs=20;f.service.loadingRecoveryIntervalMs=5;
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe')return structuredClone(f.snapshot);if(action==='refresh')return{accepted:true,refreshed:true,tabId:args.tabId,url:args.targetURL};if(action==='stop')return{stopped:true};throw Error(`unexpected_browser_${action}`)});
 const task=create(f.service,'recipe-loading-recovery'),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'deferred');assert.equal(result.task.state,'running');assert.equal(result.actionCount,0);await new Promise(resolve=>setTimeout(resolve,300));
 const finished=f.service.status({taskId:task.taskId});assert.equal(finished.state,'finished');assert.deepEqual(finished.applications[0].unsupported,['application_loading_failed']);
 assert.equal(f.calls.filter(item=>item.action==='refresh').length,1);
}));

test('a transient loading overlay can be neither a reusable nor a learned Recipe state',fixture(async f=>{
 const detail={...structuredClone(f.snapshot),url:'https://jobs.example.test/position/123456/detail',formStage:'detail',fields:[],controls:[{ref:'apply-now',kind:'start',intent:'application',label:'立即投递',safe:true}],interactions:[]};
 const loading={...structuredClone(f.snapshot),formStage:'apply',pageState:'unknown',fields:[],controls:[],activeDialog:{ref:'loader',role:'dialog',title:'',text:''},interactions:[{ref:'spinner',label:'animation',role:'button',tag:'div',type:'',surface:'dialog',safe:true}],evidence:{layers:[{ref:'loader',blocking:true,controls:['spinner']}],nodes:[]}};
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:detail,targetSnapshot:loading,actions:[{kind:'start',controlRef:'apply-now'}],runId:'invalid-loader-target'});f.service.store.write();
 const task=create(f.service,'reject-loader-recipe',detail.url),live=f.service.get(task.taskId),application=live.applications[0],selected=f.service.recipeFor(detail,live,application);
 assert.equal(selected.status,'repairable_transition');assert.equal(selected.reason,'transient_recipe_target_rejected');assert.equal(selected.actions.length,1);assert.equal(selected.actions[0].recipeTransitionId,null);
 const count=Object.keys(f.service.store.data.recipes[systemFor(detail).key].transitions).length;
 application.operation={id:'cold-loader',state:'verified',index:1,actions:[{kind:'start',controlRef:'apply-now',beforeURL:detail.url,beforeDocumentEpoch:detail.documentEpoch,beforeFieldCount:0}],evidence:[{evidence:{accepted:true}}],sourceSnapshot:detail,createdAt:new Date(0).toISOString()};
 assert.equal(f.service.recordOperationRecipe(application,loading),null);assert.equal(Object.keys(f.service.store.data.recipes[systemFor(detail).key].transitions).length,count);assert.notEqual(application.operation.recipeRecorded,true);
}));

test('cold entry learning uses the same transition settlement and skips loading frames',fixture(async f=>{
 f.service.recipeTransitionTimeoutMs=500;
 const detail={...structuredClone(f.snapshot),url:'https://jobs.example.test/position/123456/detail',formStage:'detail',fields:[],controls:[{ref:'apply-now',kind:'start',intent:'application',label:'立即投递',role:'button',tag:'button',type:'button',safe:true}],interactions:[],evidence:{schemaVersion:1,complete:true,layers:[],nodes:[{ref:'apply-now',role:'button',tag:'button'}],interactionDigest:'detail'}};
 const loading={...structuredClone(f.snapshot),formStage:'apply',pageState:'unknown',fields:[],controls:[],activeDialog:{ref:'loader',role:'dialog',title:'',text:''},interactions:[{ref:'spinner',label:'animation',role:'button',tag:'div',type:'',surface:'dialog',safe:true}],evidence:{schemaVersion:1,complete:true,layers:[{ref:'loader',blocking:true,controls:['spinner']}],nodes:[],interactionDigest:'loading'}},form={...structuredClone(f.snapshot),documentEpoch:'doc-2',activeDialog:null,interactions:[],evidence:{schemaVersion:1,complete:true,layers:[],nodes:[{ref:'runtime-name',tag:'input'}],interactionDigest:'form'}};
 Object.assign(f.snapshot,structuredClone(detail));let dispatched=false,reads=0;
 f.setBrowser(async(action,args)=>{if(action==='observe'){if(dispatched&&++reads>=3)Object.assign(f.snapshot,structuredClone(form));return structuredClone(f.snapshot)}if(action==='act'){dispatched=true;Object.assign(f.snapshot,structuredClone(loading));return{accepted:true}}if(action==='stop')return{stopped:true};throw Error(action)});
 let task=create(f.service,'cold-entry-settlement',detail.url);task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});const observed=await f.service.observe({taskId:task.taskId});
 const proposal=f.service.learning_propose({taskId:task.taskId,expectedRevision:observed.task.revision,observationId:observed.observationId,question:'申请入口是否进入申请表？',hypothesis:'唯一申请入口进入表单',expected:'出现申请表',actions:[{kind:'start',controlRef:'apply-now'}]});
 await f.service.apply({taskId:task.taskId,expectedRevision:proposal.task.revision,actions:[{kind:'start',controlRef:'apply-now'}]});await wait();
 const operation=f.service.get(task.taskId).applications[0].operation;assert.equal(operation.postcondition.kind,'learning_entry');assert.equal(operation.postcondition.state,'settling');
 const settled=await f.service.settleTransition(task.taskId,null,operation);assert.equal(settled.status,'matched');assert.ok(reads>=3);assert.equal(operation.postcondition.expectedStates[0],stateFor(form,{predecessorKind:'start'}).key);
 const recipe=Object.values(f.service.store.data.recipes)[0];assert.ok(recipe,JSON.stringify({operation,learning:f.service.get(task.taskId).applications[0].learning}));const edge=Object.values(recipe.transitions)[0];assert.equal(f.service.transientRecipeState(recipe.states[edge.to]),false);
}));

test('an ordinary observation settles an Agent-owned learning entry without another orchestration decision',fixture(async f=>{
 const detail={...structuredClone(f.snapshot),url:'https://jobs.example.test/position/123456/detail',formStage:'detail',fields:[],controls:[{ref:'edit-resume',kind:'start',intent:'resume',label:'编辑简历',role:'button',tag:'button',type:'button',safe:true}],interactions:[{ref:'edit-resume',label:'编辑简历',role:'button',tag:'button',type:'button',safe:true,surface:'page'}]};
 const form={...structuredClone(f.snapshot),url:'https://jobs.example.test/resume/edit',documentEpoch:'doc-2'};Object.assign(f.snapshot,structuredClone(detail));
 f.setBrowser(async(action,args)=>{if(action==='observe')return structuredClone(f.snapshot);if(action==='act'){Object.assign(f.snapshot,structuredClone(form));return{accepted:true}}if(action==='stop')return{stopped:true};throw Error(action)});
 let task=create(f.service,'agent-entry-auto-settlement',detail.url);task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});let observed=await f.service.observe({taskId:task.taskId});
 const actions=[{kind:'start',controlRef:'edit-resume'}],proposal=f.service.learning_propose({taskId:task.taskId,expectedRevision:observed.task.revision,observationId:observed.observationId,question:'编辑简历是否进入表单？',hypothesis:'唯一入口打开站内简历表单',expected:'出现简历字段',actions});
 await f.service.apply({taskId:task.taskId,expectedRevision:proposal.task.revision,actions});await wait();observed=await f.service.observe({taskId:task.taskId});
 const operation=f.service.get(task.taskId).applications[0].operation;assert.equal(operation.postcondition.state,'matched');assert.equal(operation.postcondition.expectedStates.length,1);assert.equal(observed.task.state,'running');
}));

test('a verified action with a persisted transient target repairs the Recipe before agent takeover',fixture(async f=>{
 f.service.recipeTransitionTimeoutMs=500;
 const detail={...structuredClone(f.snapshot),url:'https://jobs.example.test/position/123456/detail',formStage:'detail',fields:[],controls:[{ref:'apply-now',kind:'start',intent:'application',label:'立即投递',role:'button',tag:'button',type:'button',safe:true}],interactions:[],evidence:{schemaVersion:1,complete:true,layers:[],nodes:[{ref:'apply-now',role:'button',tag:'button'}],interactionDigest:'detail'}};
 const loading={...structuredClone(f.snapshot),formStage:'apply',pageState:'unknown',fields:[],controls:[],activeDialog:{ref:'loader',role:'dialog',title:'',text:''},interactions:[{ref:'spinner',label:'animation',role:'button',tag:'div',type:'',surface:'dialog',safe:true}],evidence:{schemaVersion:1,complete:true,layers:[{ref:'loader',blocking:true,controls:['spinner']}],nodes:[],interactionDigest:'loading'}},form={...structuredClone(f.snapshot),documentEpoch:'doc-2',activeDialog:null,interactions:[],evidence:{schemaVersion:1,complete:true,layers:[],nodes:[{ref:'runtime-name',tag:'input'}],interactionDigest:'form'}};
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:detail,targetSnapshot:loading,actions:[{kind:'start',controlRef:'apply-now'}],runId:'historical-loader'});f.service.store.write();Object.assign(f.snapshot,structuredClone(detail));let dispatched=false,reads=0,writes=0;
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe'){if(dispatched&&++reads>=3)Object.assign(f.snapshot,structuredClone(form));return structuredClone(f.snapshot)}if(action==='act'){writes++;dispatched=true;Object.assign(f.snapshot,structuredClone(loading));return{accepted:true}}if(action==='stop')return{stopped:true};throw Error(action)});
 const task=create(f.service,'repair-loader-transition',detail.url),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision}),recipe=Object.values(f.service.store.data.recipes)[0],valid=Object.values(recipe.transitions).filter(edge=>!f.service.transientRecipeState(recipe.states[edge.to]));
 assert.equal(result.path,'adaptive');assert.equal(result.reason,'recipe_missing_transition');assert.equal(result.actionCount,1);assert.equal(writes,1);assert.ok(reads>=3);assert.equal(valid.length,1);
}));

test('a structural action requires the explicit observed controlRef',fixture(async f=>{
 f.snapshot.fields=[];f.snapshot.controls=[{ref:'runtime-next',kind:'next',label:'下一步',safe:true}];f.snapshot.canAdvance=true;
 let task=await ready(f.service,'observed-control-ref-conflict'),view=f.service.status({taskId:task.taskId}),ref=f.service.get(task.taskId).applications[0].snapshot.controls[0].ref;
 await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:view.revision,actions:[{kind:'next',ref}]}),/action_invalid:0:controlRef_required/);
 await f.service.control({taskId:task.taskId,kind:'stop'});
 task=await ready(f.service,'observed-control-ref');view=f.service.status({taskId:task.taskId});ref=f.service.get(task.taskId).applications[0].snapshot.controls[0].ref;
 const accepted=await f.service.apply({taskId:task.taskId,expectedRevision:view.revision,actions:[{kind:'next',controlRef:ref}]});
 assert.equal(accepted.applications[0].operation.total,1);assert.equal(f.service.get(task.taskId).applications[0].operation.actions[0].controlRef,ref);
}));

test('learning proposals reject ambiguous action addresses before creating an episode',fixture(async f=>{
 const task=await ready(f.service,'proposal-action-contract'),application=f.service.get(task.taskId).applications[0],before=application.learning.episodes.length;
 assert.throws(()=>f.service.learning_propose({taskId:task.taskId,expectedRevision:task.revision,observationId:application.observationId,question:'填写？',hypothesis:'字段是姓名',expected:'姓名保留',actions:[{kind:'fill',ref:'runtime-name',factId:'candidate.name'}]}),/action_invalid:0:fieldRef_required/);
 assert.equal(application.learning.episodes.length,before);
}));

test('a fieldless workflow state cannot be marked terminal before its safe next step is exhausted',fixture(async f=>{
 f.snapshot.fields=[{ref:'referral',label:'内推码',semanticId:null,group:null,value:'',type:'text',required:false,requiredKnown:true}];f.snapshot.controls=[{ref:'runtime-next',kind:'next',label:'下一步',safe:true,active:false}];f.snapshot.canAdvance=false;f.snapshot.completionVerifiable=false;
 const task=await ready(f.service,'workflow-not-exhausted');
 assert.throws(()=>f.service.finish({taskId:task.taskId,expectedRevision:task.revision,outcome:'partial'}),/application_workflow_not_exhausted/);
 assert.throws(()=>f.service.finish({taskId:task.taskId,expectedRevision:task.revision,outcome:'partial',unsupported:['内推码']}),/application_workflow_not_exhausted/);
 const application=f.service.get(task.taskId).applications[0];markRecipeTerminal(f.service.store.data.recipes,application.snapshot,{predecessorKind:f.service.predecessorKind(application),coverageFactIds:[]});
 assert.equal(f.service.terminalFor(application.snapshot,f.service.get(task.taskId),application),null);
}));

test('explicit field units normalize sourced measurements before writing and verification',fixture(async f=>{
 f.snapshot.fields[0]={ref:'runtime-height',label:'身高(cm)',semanticId:'candidate.height',group:'基本信息',value:'',type:'search'};
 const task=await ready(f.service,'measurement-normalization');
 f.service.get(task.taskId).factSnapshot.facts.push({id:'candidate.height',value:'175cm',sourceRef:'synthetic:test'});
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-height',factId:'candidate.height'}]});await wait();
 const application=f.service.get(task.taskId).applications[0],action=application.operation.actions[0];
 assert.equal(action.value,'175');assert.equal(f.snapshot.fields[0].value,'175');assert.equal(f.service.actionVerified(f.snapshot,{...action,value:'175cm'}),true);
 assert.equal(f.service.fieldValue({label:'体重（kg）'},'60kg'),'60');assert.equal(f.service.fieldValue({label:'身高(cm)'},'60kg'),'60kg');
}));

test('an ATS parenthetical major classification satisfies the sourced major without rewriting',fixture(async f=>{
 f.snapshot.fields[0]={ref:'runtime-major',label:'专业*',semanticId:'education_1_major',group:'教育经历',value:'人工智能（电子信息类）',type:'text'};
 let task=f.service.create({clientRequestId:'major-classification',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'education_1_major',value:'人工智能',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const application=f.service.get(task.taskId).applications[0],context=f.service.learningContextFor(application,f.service.get(task.taskId));
 assert.equal(context.coverage[0].verified,true);assert.deepEqual(context.suggestedActions,[]);
 assert.equal(f.service.fieldValueSatisfied({...f.snapshot.fields[0],value:'人工智能工程'},'人工智能','education_1_major'),false);
}));

test('autonomous suggestions fill empty facts before correcting nonempty conflicts',fixture(async f=>{
 f.snapshot.fields=[
  {ref:'conflict',label:'姓名',semanticId:'candidate.name',group:null,value:'旧姓名',type:'text'},
  {ref:'empty',label:'邮箱',semanticId:'candidate.email',group:null,value:'',type:'text'}
 ];
 let task=f.service.create({clientRequestId:'empty-first',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'candidate.name',value:'测试姓名',sourceRef:'synthetic:test'},{id:'candidate.email',value:'test@example.com',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const application=f.service.get(task.taskId).applications[0],context=f.service.learningContextFor(application,f.service.get(task.taskId));
 assert.deepEqual(context.suggestedActions.map(action=>action.fieldRef),['empty','conflict']);
}));

test('autonomous suggestions reject a stale coverage mapping on a repurposed field',fixture(async f=>{
 let task=await ready(f.service,'stale-suggestion'),live=f.service.get(task.taskId),application=live.applications[0],row=application.coverage[0];live.factSnapshot.facts.push({id:'campus_1_start',value:'2024-01',sourceRef:'synthetic:test'});Object.assign(row,{disposition:'mapped',factId:'campus_1_start'});
 const context=f.service.learningContextFor(application,live);assert.deepEqual(context.suggestedActions,[]);
}));

test('negative checkboxes invert positive has-facts in compilation and verification',fixture(async f=>{
 f.snapshot.fields=[{ref:'no-internship',label:'没有实习经历',semanticId:'has_internship',group:'实习经历',checked:false,type:'checkbox'}];
 let task=f.service.create({clientRequestId:'negative-checkbox',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'has_internship',value:'是',sourceRef:'synthetic:test'}]});task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 let application=f.service.get(task.taskId).applications[0],context=f.service.learningContextFor(application,f.service.get(task.taskId));assert.equal(context.coverage[0].verified,true);assert.deepEqual(context.suggestedActions,[]);
 f.service.get(task.taskId).factSnapshot.facts[0].value='否';context=f.service.learningContextFor(application,f.service.get(task.taskId));assert.deepEqual(context.suggestedActions,[{kind:'check',fieldRef:'no-internship',factId:'has_internship'}]);f.setBrowser(async(action,args)=>{if(action==='act'){f.snapshot.fields[0].checked=args.actionSpec.value==='true';return{accepted:true}}if(action==='progress')return{accepted:true};if(action==='stop')return{stopped:true};throw Error(action)});
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'check',fieldRef:'no-internship',factId:'has_internship'}]});application=f.service.get(task.taskId).applications[0];assert.equal(application.operation.actions[0].value,'true');await wait();assert.equal(application.operation.state,'verified');assert.equal(f.snapshot.fields[0].checked,true);
}));

test('a cold learning action owns the transition until page effect verification',fixture(async f=>{
 let task=await ready(f.service,'cold-transition-owner'),proposal=f.service.learning_propose({taskId:task.taskId,expectedRevision:task.revision,observationId:f.service.get(task.taskId).applications[0].observationId,question:'姓名写入是否生效？',hypothesis:'页面接受有来源的姓名',expected:'姓名字段回读一致',actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});
 await f.service.apply({taskId:task.taskId,expectedRevision:proposal.task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});await wait();let live=f.service.get(task.taskId),operation=live.applications[0].operation;
 assert.equal(operation.state,'verified');assert.equal(operation.postcondition.kind,'learning');assert.equal(operation.postcondition.state,'settling');
 await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:live.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]}),/not_ready_for_actions/);
 task=(await f.service.observe({taskId:task.taskId})).task;live=f.service.get(task.taskId);operation=live.applications[0].operation;assert.equal(operation.postcondition.state,'matched');assert.equal(operation.postcondition.expectedStates.length,1);assert.equal(Object.keys(f.service.store.data.recipes).length,1);assert.equal(task.state,'running');
}));

test('a settled failed Recipe transition cannot retain the next-action lock',fixture(async f=>{
 const task=await ready(f.service,'failed-transition-unlocks'),application=f.service.get(task.taskId).applications[0];application.operation={id:'historical-failure',state:'failed',error:'partial_operation_settled',index:1,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}],postcondition:{kind:'recipe',state:'unknown',transitionId:'old-edge',sourceState:'old-state',expectedStates:[]}};application.operations=[application.operation];
 const accepted=await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});assert.equal(accepted.state,'executing');assert.notEqual(f.service.get(task.taskId).applications[0].operation.id,'historical-failure');
}));

test('a timed-out verified Recipe transition cannot retain the next-action lock',fixture(async f=>{
 const task=await ready(f.service,'timed-out-transition-unlocks'),application=f.service.get(task.taskId).applications[0];application.operation={id:'historical-timeout',state:'verified',index:1,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}],postcondition:{kind:'learning',state:'timeout',transitionId:'old-edge',sourceState:'old-state',expectedStates:[]}};application.operations=[application.operation];
 assert.equal(f.service.operationSettled(application.operation),true);
 const accepted=await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});assert.equal(accepted.state,'executing');assert.notEqual(f.service.get(task.taskId).applications[0].operation.id,'historical-timeout');
}));

test('a proven no-effect replay can suspend its exact Recipe transition',fixture(async f=>{
 await learnOneField(f);const recipe=Object.values(f.service.store.data.recipes)[0],transition=Object.values(recipe.transitions)[0],task=await ready(f.service,'no-effect-feedback'),application=f.service.get(task.taskId).applications[0];
 application.operations.push({id:'failed-replay',state:'failed',error:'operation_no_effect',actions:[{kind:'fill',factId:'candidate.name',recipeTransitionId:transition.id}],postcondition:{kind:'recipe',transitionId:transition.id}});
 assert.throws(()=>f.service.recipe_feedback({taskId:task.taskId,reason:'fresh_replay_no_effect',transitionId:'missing'}),/recipe_feedback_unproven/);
 const feedback=f.service.recipe_feedback({taskId:task.taskId,reason:'fresh_replay_no_effect',transitionId:transition.id});assert.equal(feedback.accepted,true);assert.equal(Object.values(f.service.store.data.recipes)[0].transitions[transition.id].status,'suspended');
 assert.equal(f.service.semanticRecipeFailure('operation_no_effect'),true);
}));

test('one verified adaptive pass becomes an immediate deterministic recipe',fixture(async f=>{
 await learnOneField(f);assert.equal(Object.keys(f.service.store.data.recipes).length,1);
 f.snapshot.url='https://jobs.example.test/position/987654/apply';f.snapshot.documentEpoch='doc-2';f.snapshot.fields[0]={...f.snapshot.fields[0],ref:'new-runtime-ref',value:''};f.calls.length=0;
 const task=create(f.service,'reuse',f.snapshot.url),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'recipe');assert.equal(result.task.state,'finished');assert.equal(f.snapshot.fields[0].value,'测试姓名');assert.equal(f.calls.filter(item=>item.action==='act').length,1);
}));

test('a verified autonomous batch ends immediately at the resume boundary',fixture(async f=>{
 const task=await ready(f.service,'adaptive-batch-open'),result=await f.service.run_adaptive_plan({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});
 assert.equal(result.outcome,'partial');assert.equal(result.reason,'resume_boundary_complete');assert.equal(result.task.state,'finished');assert.equal(f.snapshot.fields[0].value,'测试姓名');
 assert.equal(f.service.get(task.taskId).applications[0].outcome,'partial');assert.equal(result.finalSubmitAllowed,false);
}));

test('autonomous filling may finish once the full ApplicationPlan is complete even with website-specific fields',fixture(async f=>{
 f.snapshot.fields.push({ref:'source',label:'你从哪里获知招聘信息？',semanticId:null,group:'网站问题',value:'',type:'text',required:true,requiredKnown:true});
 const task=await ready(f.service,'autonomous-responsibility-boundary'),result=await f.service.run_adaptive_plan({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});
 assert.equal(result.outcome,'partial');assert.equal(result.reason,'resume_boundary_complete');assert.equal(result.task.state,'finished');
 const finished=result.task;assert.equal(finished.applications[0].outcome,'partial');assert.deepEqual(finished.applications[0].missing,['你从哪里获知招聘信息']);assert.equal(f.calls.filter(item=>item.action==='act').length,1);
 await wait();const progress=f.calls.filter(item=>item.action==='progress').at(-1).args.progress;
 assert.equal(progress.title,'简历内容处理完成');assert.match(progress.detail,/页面其余内容由你处理/);assert.equal(progress.nextOwner,'你');assert.equal(progress.terminal,true);assert.equal(progress.finalSubmitAllowed,false);
}));

test('coverage review cannot hide a known safe resume mapping',fixture(async f=>{
 const task=await ready(f.service,'known-coverage-cannot-downgrade'),row=f.service.get(task.taskId).applications[0].coverage[0];
 assert.throws(()=>f.service.coverage_review({taskId:task.taskId,expectedRevision:task.revision,items:[{controlRef:row.key,disposition:'no_resume_fact'}]}),/coverage_known_mapping_conflict/);
 assert.throws(()=>f.service.finish({taskId:task.taskId,expectedRevision:task.revision,outcome:'partial'}),/application_plan_incomplete/);
}));

test('a partially settled batch keeps every planned resume fact in the completion boundary',fixture(async f=>{
 f.snapshot.fields.push({ref:'runtime-email',label:'电子邮箱',semanticId:'candidate.email',group:'基本信息',value:'',type:'text'});
 const task=await ready(f.service,'partial-batch-coverage'),live=f.service.get(task.taskId),application=live.applications[0];
 live.factSnapshot.facts.push({id:'candidate.email',value:'test@example.com',sourceRef:'synthetic:test'});
 application.snapshot.fields.find(field=>field.ref==='runtime-name').value='测试姓名';
 application.operation={id:'partial-op',state:'failed',error:'partial_operation_settled',index:1,actions:[{kind:'fill',fieldRef:'runtime-name',semanticId:'candidate.name',group:'基本信息',value:'测试姓名',before:'',factId:'candidate.name'},{kind:'fill',fieldRef:'runtime-email',semanticId:'candidate.email',group:'基本信息',value:'test@example.com',before:'',factId:'candidate.email'}],evidence:[],sourceSnapshot:structuredClone(application.snapshot)};
 f.service.syncObligationLedger(application,live);
 const projection=f.service.resumeProjection(application,live);
 assert.deepEqual(projection.expected.sort(),['candidate.email','candidate.name']);assert.deepEqual(projection.processed,['candidate.name']);assert.deepEqual(projection.missing,['candidate.email']);
 assert.throws(()=>f.service.finish({taskId:task.taskId,expectedRevision:task.revision,outcome:'partial'}),/application_plan_incomplete/);
}));

test('an early batch failure does not spend action or strategy budget on the undispatched tail',fixture(async f=>{
 f.snapshot.fields.push({ref:'runtime-email',label:'电子邮箱',semanticId:'candidate.email',group:'基本信息',value:'',type:'text'});
 const task=await ready(f.service,'dispatch-budget'),live=f.service.get(task.taskId);
 live.factSnapshot.facts.push({id:'candidate.email',value:'test@example.com',sourceRef:'synthetic:test'});
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='act')throw Error('site_rejected_value');if(action==='progress')return{accepted:true};if(action==='observe')return structuredClone(f.snapshot);throw Error(`unexpected_browser_${action}`)});
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'},{kind:'fill',fieldRef:'runtime-email',factId:'candidate.email'}]});await wait();
 const budget=f.service.get(task.taskId).applications[0].budget;
 assert.equal(budget.actions,1);assert.equal(Object.keys(budget.strategies).length,1);assert.equal(f.calls.filter(item=>item.action==='act').length,1);
}));

test('a missing custom-select option is settled as a retryable pre-dispatch failure',fixture(async f=>{
 f.snapshot.fields[0]={ref:'runtime-school',label:'学校名称',semanticId:'education_1_school',group:'教育经历',value:'',type:'select'};
 let task=await ready(f.service,'select-option-missing'),stored=f.service.get(task.taskId);
 stored.factSnapshot.facts.push({id:'education_1_school',value:'西安电子科技大学',sourceRef:'synthetic:test'});
 const diagnostic='option_not_found:{"phase":"after_search","desired":"西安电子科技大学"}';
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='act')throw Error(diagnostic);if(action==='progress')return{accepted:true};if(action==='observe')return structuredClone(f.snapshot);throw Error(`unexpected_browser_${action}`)});
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'select',fieldRef:'runtime-school',factId:'education_1_school'}]});await wait();
 const application=f.service.get(task.taskId).applications[0];
 assert.equal(f.service.get(task.taskId).state,'running');assert.equal(application.operation.state,'failed');assert.equal(application.operation.error,diagnostic);assert.equal(application.operation.reconciliation,undefined);assert.equal(application.budget.activeSince,null);
}));

test('claiming work does not charge external idle time and completed actions close their active span',fixture(async f=>{
 let task=create(f.service,'execution-time-only'),stored=f.service.get(task.taskId),application=stored.applications[0];task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});assert.equal(application.budget.activeSince,null);
 task=(await f.service.observe({taskId:task.taskId})).task;await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});await wait();assert.equal(application.operation.state,'verified');assert.equal(application.budget.activeSince,null);
}));

test('visible verified progress counts resolved obligations instead of operation receipts',fixture(async f=>{
 const task=await ready(f.service,'progress-obligation-count'),live=f.service.get(task.taskId),application=live.applications[0];
 application.evidence.push({action:{kind:'fill',fieldRef:'runtime-name'}},{action:{kind:'fill',fieldRef:'runtime-name'}});
 assert.equal(f.service.progressFor(live,application).verified,0);
 application.obligationLedger.entries['fact:candidate.name'].status='verified';
 assert.equal(f.service.progressFor(live,application).verified,1);
}));

test('a pre-dispatch budget failure settles the operation instead of waiting for the tool timeout',fixture(async f=>{
 const task=await ready(f.service,'budget-before-dispatch'),application=f.service.get(task.taskId).applications[0];application.budget.schemaVersion=2;application.budget.activeMs=application.budget.limits.activeMs;
 const started=Date.now(),result=await f.service.run_adaptive_plan({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});
 const current=f.service.get(task.taskId).applications[0];assert.equal(result.reason,'batch_failed');assert.equal(current.operation.state,'failed');assert.equal(current.operation.error,'budget_exhausted_before_dispatch');assert.ok(Date.now()-started<1000);
}));

test('an exhausted queued application reports attention instead of false progress',fixture(async f=>{
 const task=create(f.service,'exhausted-queued'),stored=f.service.get(task.taskId),application=stored.applications[0];application.budget.activeMs=application.budget.limits.activeMs;
 const status=f.service.status({taskId:task.taskId});assert.equal(status.state,'needs_attention');assert.equal(status.nextActionOwner,'user');assert.match(status.nextAction,/预算已耗尽/);
 f.service.get(task.taskId).state='paused';assert.equal(f.service.status({taskId:task.taskId}).state,'paused');
}));

test('a selected resume fact replaces a different prefilled contact value without identity approval',fixture(async f=>{
 f.snapshot.fields[0]={ref:'runtime-email',label:'电子邮箱',semanticId:'candidate.email',group:'基本信息',value:'login@example.com',type:'text'};
 const task=await ready(f.service,'independent-login-email'),live=f.service.get(task.taskId);
 live.factSnapshot.facts.push({id:'candidate.email',value:'resume@example.com',sourceRef:'synthetic:test'});
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-email',factId:'candidate.email'}]});await wait();
 assert.equal(f.snapshot.fields[0].value,'resume@example.com');
 assert.equal(f.service.get(task.taskId).applications[0].operation.actions[0].before,'login@example.com');
 assert.equal(f.service.get(task.taskId).applications[0].operation.actions[0].replaceNativeParsed,true);
 assert.equal(f.service.status({taskId:task.taskId}).question,null);
}));

test('an agent-reviewed generic field may replace an older site-resume value',fixture(async f=>{
 f.snapshot.fields[0]={ref:'runtime-company',label:'请输入',semanticId:null,group:null,value:'旧公司',type:'text'};
 const task=await ready(f.service,'reviewed-generic-replacement'),live=f.service.get(task.taskId),application=live.applications[0];
 application.coverage=[{key:'company-control',fieldRef:'runtime-company',label:'请输入',semanticId:null,factId:'candidate.name',disposition:'mapped',value:'旧公司',type:'text'}];
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-company',factId:'candidate.name'}]});await wait();
 assert.equal(f.snapshot.fields[0].value,'测试姓名');
 assert.equal(application.operation.actions[0].before,'旧公司');
 assert.equal(application.operation.actions[0].replaceNativeParsed,true);
}));

test('identity conflict is not an accepted user-question reason',fixture(async f=>{
 const task=await ready(f.service,'identity-question-removed');
 assert.throws(()=>f.service.question({taskId:task.taskId,expectedRevision:task.revision,reason:'identity_conflict',text:'账号与简历邮箱不同',fieldRefs:['runtime-name']}),/invalid_question/);
}));

test('the selected resume parser takes precedence over values from an older site resume',fixture(async f=>{
 f.snapshot.fields=[{ref:'resume-file',label:'简历附件',semanticId:'resume.file',value:'',type:'file',nativeParseCandidate:true,filePurpose:'resume'},{ref:'preference',label:'期望工作城市',semanticId:null,value:'上海',type:'text'}];
 f.snapshot.nativeResumeParse={available:true,candidates:[{fieldRef:'resume-file'}]};
 let task=createWithResume(f.service,'replace-older-site-resume');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;const live=f.service.get(task.taskId);
 assert.deepEqual(f.service.resumeStrategyFor(f.snapshot,live,live.applications[0]),{kind:'native_parse',fieldRef:'resume-file',isolated:true});
}));

test('a unique resume upload is preferred even when an older site resume prefilled the page',fixture(async f=>{
 f.snapshot.fields=[{ref:'resume-file',label:'上传文件简历',semanticId:null,value:'',type:'file',nativeParseCandidate:false,filePurpose:'resume'},{ref:'school',label:'毕业院校',semanticId:null,value:'旧学校',type:'text'}];
 f.snapshot.nativeResumeParse={available:false,candidates:[]};
 let task=createWithResume(f.service,'upload-over-older-site-resume');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;const live=f.service.get(task.taskId);
 assert.deepEqual(f.service.resumeStrategyFor(f.snapshot,live,live.applications[0]),{kind:'upload_then_reobserve',fieldRef:'resume-file',isolated:true});
}));

test('an explicit native parser revealed after upload preempts an autonomous fill Recipe',fixture(async f=>{
 const source=structuredClone(f.snapshot),target=structuredClone(f.snapshot);source.fields.unshift({ref:'resume-file',label:'简历附件',semanticId:'resume.file',value:'',receiptText:'resume.pdf 将简历内容解析到下方表单？解析并覆盖',type:'file',filePurpose:'resume',nativeParseCandidate:true,nativeParseMode:'explicit_confirm'});source.nativeResumeParse={available:true,candidates:[{fieldRef:'resume-file',label:'简历附件',reason:'file_context'}],inProgress:false};target.fields=structuredClone(source.fields);target.fields.find(field=>field.ref==='runtime-name').value='测试姓名';
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'parser-before-fill',predecessorKind:'upload',actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});Object.assign(f.snapshot,structuredClone(source));f.service.store.write();
 let task=createWithResume(f.service,'parser-revealed-after-upload'),live=f.service.get(task.taskId),application=live.applications[0],createdAt=new Date().toISOString();application.operation={id:'accepted-upload',state:'verified',actions:[{kind:'upload',fieldRef:'resume-file',filename:'resume.pdf',beforeFields:f.service.formState(source)}],index:1,evidence:[{action:{kind:'upload'},evidence:{accepted:true}}],createdAt,sourceSnapshot:structuredClone(source)};application.operations=[application.operation];task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});const observed=await f.service.observe({taskId:task.taskId});
 assert.deepEqual(observed.resumeStrategy,{kind:'native_parse',fieldRef:'resume-file',isolated:true,reuseUploaded:true});
 assert.notEqual(observed.recipe.status,'reusable');
 assert.equal(f.service.terminalFor(source,f.service.get(task.taskId),application),null);
 assert.equal(f.service.autonomousCompletionBoundary(application,f.service.get(task.taskId),[],{allowUnreviewed:true}).reason,'native_resume_parser_pending');
 const actions=[{kind:'native_parse',fieldRef:'resume-file'}],proposal=f.service.learning_propose({taskId:task.taskId,expectedRevision:observed.task.revision,observationId:observed.observationId,question:'是否应使用网站原生解析？',hypothesis:'已上传附件后出现唯一的解析并覆盖入口。',expected:'网站解析简历并更新表单。',actions});
 await f.service.apply({taskId:task.taskId,expectedRevision:proposal.task.revision,actions});
 application=f.service.get(task.taskId).applications[0];assert.equal(application.operation.actions[0].kind,'native_parse');assert.equal(application.operation.actions[0].reuseUploaded,true);
}));

test('run_recipe directly advances a uniquely observed native parser without an Agent handoff',fixture(async f=>{
 const source={...structuredClone(f.snapshot),fields:[
  {ref:'resume-file',label:'简历附件',semanticId:'resume.file',value:'',receiptText:'resume.pdf 将简历内容解析到下方表单？解析并覆盖',type:'file',filePurpose:'resume',nativeParseCandidate:true,nativeParseMode:'explicit_confirm'},
  {ref:'runtime-name',label:'姓名',semanticId:'candidate.name',group:'基本信息',value:'',type:'text'},
  {ref:'runtime-email',label:'邮箱',semanticId:'candidate.email',group:'基本信息',value:'',type:'text'}
 ],nativeResumeParse:{available:true,candidates:[{fieldRef:'resume-file',label:'简历附件',reason:'file_context'}],inProgress:false}};
 Object.assign(f.snapshot,structuredClone(source));
 f.setBrowser(async(action,args)=>{
  f.calls.push({action,args:structuredClone(args)});
  if(action==='observe')return structuredClone(f.snapshot);
  if(action==='act'){
   assert.equal(args.actionSpec.kind,'native_parse');
   f.snapshot.fields.find(field=>field.ref==='runtime-name').value='测试姓名';
   f.snapshot.fields.find(field=>field.ref==='runtime-email').value='test@example.com';
   f.snapshot.fields[0].nativeParseCandidate=false;f.snapshot.fields[0].nativeParseMode=null;f.snapshot.nativeResumeParse={available:false,candidates:[],inProgress:false};
   return{accepted:true,scope:'native_parser_triggered_requires_new_observation'};
  }
  throw Error(`unexpected_browser_${action}`)
 });
 const task=createWithResume(f.service,'direct-native-parser'),live=f.service.get(task.taskId),application=live.applications[0],createdAt=new Date().toISOString();
 live.factSnapshot.facts.push({id:'candidate.email',value:'test@example.com',sourceRef:'synthetic:test'});
 application.operation={id:'accepted-upload',state:'verified',actions:[{kind:'upload',fieldRef:'resume-file',filename:'resume.pdf',beforeFields:f.service.formState(source)}],index:1,evidence:[{action:{kind:'upload'},evidence:{accepted:true}}],createdAt,sourceSnapshot:structuredClone(source)};
 application.operations=[application.operation];live.state='waiting_native_parse';
 const result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'recipe',JSON.stringify(result));assert.equal(result.task.state,'finished');assert.equal(result.actionCount,1);
 assert.deepEqual(f.calls.filter(item=>item.action==='act').map(item=>item.args.actionSpec.kind),['native_parse']);
 assert.equal(f.service.get(task.taskId).applications[0].operations.length,2);
 Object.assign(f.snapshot,structuredClone(source));const replay=createWithResume(f.service,'direct-native-parser-replay'),replayLive=f.service.get(replay.taskId),replayApplication=replayLive.applications[0];
 replayLive.factSnapshot.facts.push({id:'candidate.email',value:'test@example.com',sourceRef:'synthetic:test'});replayApplication.operation={id:'accepted-upload-replay',state:'verified',actions:[{kind:'upload',fieldRef:'resume-file',filename:'resume.pdf',beforeFields:f.service.formState(source)}],index:1,evidence:[{action:{kind:'upload'},evidence:{accepted:true}}],createdAt:new Date().toISOString(),sourceSnapshot:structuredClone(source)};replayApplication.operations=[replayApplication.operation];replayLive.state='waiting_native_parse';const priorActs=f.calls.filter(item=>item.action==='act').length;
 const replayResult=await f.service.run_recipe({taskId:replay.taskId,expectedRevision:replay.revision});assert.equal(replayResult.path,'recipe',JSON.stringify(replayResult));assert.equal(replayResult.task.state,'finished');assert.equal(replayResult.actionCount,1);assert.equal(f.service.get(replay.taskId).applications[0].learning.episodes.length,0);assert.deepEqual(f.calls.filter(item=>item.action==='act').slice(priorActs).map(item=>item.args.actionSpec.kind),['native_parse']);
}));

test('an exact learned terminal cannot skip the selected resume native parser',fixture(async f=>{
 const source={...structuredClone(f.snapshot),fields:[
  {ref:'resume-file',label:'简历附件',semanticId:'resume.file',value:'',type:'file',filePurpose:'resume',nativeParseCandidate:true,nativeParseMode:'explicit_confirm'},
  {ref:'runtime-name',label:'姓名',semanticId:'candidate.name',group:'基本信息',value:'',type:'text'}
 ],nativeResumeParse:{available:true,candidates:[{fieldRef:'resume-file',label:'简历附件',reason:'file_context'}],inProgress:false}};
 const target=structuredClone(source);target.fields[1].value='测试姓名';
 target.fields.push({ref:'site-only',label:'网站专属问题',semanticId:null,group:'其他',value:'',type:'text'});
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'learned-direct-fill',actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});
 markRecipeTerminal(f.service.store.data.recipes,target,{predecessorKind:'fill',coverageFactIds:['candidate.name']});
 Object.assign(f.snapshot,structuredClone(target));f.service.store.write();
 const task=createWithResume(f.service,'satisfied-terminal-before-native'),live=f.service.get(task.taskId),application=live.applications[0];application.snapshot=structuredClone(target);f.service.syncObligationLedger(application,live);
 assert.equal(f.service.resumeStrategyFor(target,live,application).kind,'native_parse');assert.equal(f.service.terminalFor(target,live,application),null);
 assert.equal(f.service.applicationPlanAssessment(application,live).pending.some(item=>item.obligationId==='attachment:resume'),true);
}));

test('a newly exposed resume upload reopens an earlier no-field attachment exception',fixture(async f=>{
 f.snapshot.extensionVersion='0.2.45';
 let task=createWithResume(f.service,'attachment-capability-reappears');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const live=f.service.get(task.taskId),application=live.applications[0];assert.equal(application.obligationLedger.entries['attachment:resume'].status,'site_no_field');
 application.snapshot.fields.push({ref:'resume-file',label:'简历附件',type:'file',filePurpose:'resume',value:''});f.service.syncObligationLedger(application,live);
 assert.equal(application.obligationLedger.entries['attachment:resume'].status,'pending');
 application.sectionSnapshots={'简历信息':structuredClone(application.snapshot)};application.snapshot.fields=application.snapshot.fields.filter(field=>field.ref!=='resume-file');f.service.syncObligationLedger(application,live);
 assert.equal(application.obligationLedger.entries['attachment:resume'].status,'pending');
 application.operations.push({id:'late-upload',state:'verified',createdAt:new Date().toISOString(),actions:[{kind:'upload',fieldRef:'resume-file'}],evidence:[{evidence:{accepted:true}}]});f.service.syncObligationLedger(application,live);
 assert.equal(application.obligationLedger.entries['attachment:resume'].status,'verified');
}));

test('a parser confirmation outranks a verified direct-fill Recipe whenever this task selected an attachment',fixture(async f=>{
 const source={...structuredClone(f.snapshot),fields:[
  {ref:'resume-file',label:'简历附件',semanticId:'resume.file',value:'',type:'file',filePurpose:'resume',nativeParseCandidate:true,nativeParseMode:'explicit_confirm'},
  {ref:'runtime-name',label:'姓名',semanticId:'candidate.name',group:'基本信息',value:'',type:'text'}
 ],nativeResumeParse:{available:true,candidates:[{fieldRef:'resume-file',label:'简历附件',reason:'generic_candidate'}],inProgress:false}};
 const target=structuredClone(source);target.fields[1].value='测试姓名';
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'verified-direct-fill',actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});
 markRecipeTerminal(f.service.store.data.recipes,target,{predecessorKind:'fill',coverageFactIds:['candidate.name']});
 Object.assign(f.snapshot,structuredClone(source));f.service.store.write();
 const task=createWithResume(f.service,'direct-fill-before-generic-parser'),live=f.service.get(task.taskId),application=live.applications[0];application.snapshot=structuredClone(source);
 const strategy=f.service.resumeStrategyFor(source,live,application),selected=f.service.recipeFor(source,live,application);
 assert.equal(strategy.kind,'native_parse');assert.notEqual(selected.status,'reusable');
 assert.throws(()=>f.service.enforceResumeStrategy(source,live,application,[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name',recipeTransitionId:'old'}]),/native_resume_strategy_required/);
}));

test('run_recipe directly confirms the unique safe resume replacement step without an Agent handoff',fixture(async f=>{
 const source={...structuredClone(f.snapshot),activeDialog:{ref:'resume-dialog',role:'dialog',title:'',text:'上传新简历将替换现有简历内容，请确认是否继续',blocking:true},interactions:[{ref:'resume-continue',label:'继续',role:'button',tag:'button',type:'button',surface:'dialog',safe:true,formSubmit:false}],controls:[],nativeResumeParse:{available:false,candidates:[],inProgress:false}};
 Object.assign(f.snapshot,structuredClone(source));
 f.setBrowser(async(action,args)=>{
  f.calls.push({action,args:structuredClone(args)});
  if(action==='observe')return structuredClone(f.snapshot);
  if(action==='act'){
   assert.equal(args.actionSpec.kind,'activate');assert.equal(args.actionSpec.purpose,'resume_replacement');assert.equal(args.actionSpec.controlRef,'resume-continue');
   f.snapshot.activeDialog=null;f.snapshot.interactions=[];f.snapshot.nativeResumeParse={available:false,candidates:[],inProgress:true};
   return{accepted:true,scope:'resume_replacement_confirmed'};
  }
  throw Error(`unexpected_browser_${action}`)
 });
 const task=createWithResume(f.service,'direct-resume-replacement'),live=f.service.get(task.taskId),application=live.applications[0],createdAt=new Date().toISOString();
 application.operation={id:'accepted-upload',state:'verified',actions:[{kind:'upload',fieldRef:'resume-file',filename:'resume.pdf',beforeFields:f.service.formState(source)}],index:1,evidence:[{action:{kind:'upload'},evidence:{accepted:true}}],createdAt,verifyUntil:new Date(Date.now()+30_000).toISOString(),sourceSnapshot:structuredClone(source)};application.operations=[application.operation];live.state='waiting_native_parse';
 const result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'deferred',JSON.stringify(result));assert.equal(result.reason,'parse_pending');assert.equal(result.actionCount,1);
 assert.deepEqual(f.calls.filter(item=>item.action==='act').map(item=>item.args.actionSpec.kind),['activate']);
 assert.equal(application.operations.length,2);assert.equal(application.operations[1].actions[0].purpose,'resume_replacement');
}));

test('a detail page without an upload control does not prove native resume capability absent',fixture(async f=>{
 f.snapshot.formStage='detail';f.snapshot.fields=[];f.snapshot.controls=[{ref:'apply',kind:'start',intent:'application',safe:true,label:'立即申请'}];
 const task=createWithResume(f.service,'detail-native-discovery'),live=f.service.get(task.taskId),strategy=f.service.resumeStrategyFor(f.snapshot,live,live.applications[0]);
 assert.equal(strategy.kind,'native_resume_discovery');assert.equal(strategy.reason,'resume_capability_not_exhausted');
}));

test('native resume discovery still permits the verified application-entry transition',fixture(async f=>{
 const detail={...structuredClone(f.snapshot),formStage:'detail',fields:[],controls:[{ref:'apply',kind:'start',intent:'application',safe:true,label:'立即申请',role:'button',tag:'button',type:'button',surface:'page'}]},apply=structuredClone(f.snapshot);recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:detail,targetSnapshot:apply,runId:'resume-entry',actions:[{kind:'start',controlRef:'apply'}]});f.service.store.write();
 const task=createWithResume(f.service,'resume-entry-reuse'),live=f.service.get(task.taskId),application=live.applications[0];
 assert.equal(f.service.resumeStrategyFor(detail,live,application).kind,'native_resume_discovery');assert.equal(f.service.recipeFor(detail,live,application).status,'reusable');
}));

test('native parse deadline remains bounded while a site keeps reporting parser activity',fixture(async f=>{
 const task=createWithResume(f.service,'bounded-parser'),live=f.service.get(task.taskId),application=live.applications[0],beforeFields=f.service.formState(f.snapshot),createdAt=new Date(Date.now()-31_000).toISOString();
 application.operation={id:'bounded-native',state:'awaiting_verification',actions:[{kind:'native_parse',beforeFields}],index:1,evidence:[{action:{kind:'native_parse'},evidence:{accepted:true}}],createdAt,sourceSnapshot:structuredClone(f.snapshot)};application.operations=[application.operation];f.snapshot.nativeResumeParse={available:true,candidates:[],inProgress:true};
 f.service.recordResumeObservation(application,f.snapshot,{observationId:'busy-1'});f.service.recordResumeObservation(application,f.snapshot,{observationId:'busy-2'});
 assert.equal(f.service.resumeExecutionFor(application,f.snapshot).phase,'unavailable');assert.equal(f.service.resumeStrategyFor(f.snapshot,live,application).kind,'autonomous_fill');assert.equal(f.service.recipeTransitionAllowed(f.snapshot,live,application,{actions:[{kind:'native_parse'}]}),false);
}));

test('a reconciled no-effect native parse exits waiting without replaying the upload',fixture(async f=>{
 const task=createWithResume(f.service,'native-no-effect'),live=f.service.get(task.taskId),application=live.applications[0],beforeFields=f.service.formState(f.snapshot);
 application.operation={id:'native-no-effect-op',state:'failed',error:'operation_no_effect',actions:[{kind:'native_parse',fieldRef:'resume-file',beforeFields}],index:0,evidence:[],createdAt:new Date().toISOString(),sourceSnapshot:structuredClone(f.snapshot)};application.operations=[application.operation];
 const execution=f.service.resumeExecutionFor(application,f.snapshot),strategy=f.service.resumeStrategyFor(f.snapshot,live,application);
 assert.equal(execution.phase,'unavailable');assert.equal(strategy.kind,'autonomous_fill');assert.equal(strategy.reason,'native_parse_unavailable');assert.equal(application.resumeObservations.length,0);
}));

test('a uniquely observed parse confirmation recovers a failed native action without reuploading',fixture(async f=>{
 const task=createWithResume(f.service,'recover-native-confirmation'),live=f.service.get(task.taskId),application=live.applications[0],beforeFields=f.service.formState(f.snapshot),createdAt=new Date().toISOString();
 application.operation={id:'failed-native-confirmation',state:'failed',error:'native_parse_no_effect',actions:[{kind:'native_parse',fieldRef:'resume-file',beforeFields}],index:0,evidence:[],createdAt,sourceSnapshot:structuredClone(f.snapshot)};application.operations=[application.operation];
 f.snapshot.activeDialog={ref:'resume-dialog',role:'dialog',title:'简历绑定',text:'是否根据您上传的附件信息，自动填写至简历详情中？信息自动填写后，原来的信息将丢失。',blocking:true};
 f.snapshot.interactions=[{ref:'upload-only',label:'否，仅上传附件',role:'button',tag:'button',type:'button',surface:'dialog',safe:true,formSubmit:false},{ref:'upload-parse',label:'是，上传并解析',role:'button',tag:'button',type:'button',surface:'dialog',safe:true,formSubmit:false}];
 let execution=f.service.resumeExecutionFor(application,f.snapshot),strategy=f.service.resumeStrategyFor(f.snapshot,live,application),continuation=f.service.resumeUploadSuccessContinuation(f.snapshot);
 assert.equal(execution.phase,'confirmation_required');assert.equal(strategy.kind,'native_parse_pending');assert.equal(continuation.controlRef,'upload-parse');assert.equal(continuation.purpose,'resume_parse_confirmation');
 const confirmed={id:'parse-confirmed',state:'verified',actions:[continuation],index:1,evidence:[{action:continuation,evidence:{accepted:true}}],createdAt:new Date().toISOString(),sourceSnapshot:structuredClone(f.snapshot)};application.operations.push(confirmed);application.operation=confirmed;f.snapshot.activeDialog=null;f.snapshot.interactions=[];f.snapshot.fields[0].value='测试姓名';
 f.service.recordResumeObservation(application,f.snapshot,{observationId:'confirmed-parse-1'});f.service.recordResumeObservation(application,f.snapshot,{observationId:'confirmed-parse-2'});execution=f.service.resumeExecutionFor(application,f.snapshot);
 assert.equal(execution.phase,'parsed');assert.equal(application.resumeObservations.length,2);
}));

test('a first nonempty parse result arriving after the base deadline gets one bounded confirmation window',fixture(async f=>{
 const task=createWithResume(f.service,'late-native-result'),live=f.service.get(task.taskId),application=live.applications[0],beforeFields=f.service.formState(f.snapshot),createdAt=new Date(Date.now()-31_000).toISOString();application.operation={id:'late-native',state:'awaiting_verification',actions:[{kind:'native_parse',beforeFields}],index:1,evidence:[{action:{kind:'native_parse'},evidence:{accepted:true}}],createdAt,sourceSnapshot:structuredClone(f.snapshot)};application.operations=[application.operation];application.resumeObservations=[{attemptOperationId:'late-native',at:new Date(Date.now()-20_000).toISOString(),parsedValues:[],digest:'empty'}];f.snapshot.fields[0].value='测试姓名';
 f.service.recordResumeObservation(application,f.snapshot,{observationId:'late-result-1'});let execution=f.service.resumeExecutionFor(application,f.snapshot);assert.equal(execution.phase,'parse_pending');assert.ok(Date.parse(execution.deadlineAt)>Date.now());
 f.service.recordResumeObservation(application,f.snapshot,{observationId:'late-result-2'});execution=f.service.resumeExecutionFor(application,f.snapshot);assert.equal(execution.phase,'parsed');
}));

test('an explicit upload-success acknowledgement verifies unchanged prefilled critical facts',fixture(async f=>{
 f.snapshot.fields=[{ref:'name',label:'姓名',semanticId:'candidate.name',group:'基本信息',value:'测试姓名',type:'text'},{ref:'email',label:'邮箱',semanticId:'candidate.email',group:'基本信息',value:'test@example.com',type:'text'}];
 const task=createWithResume(f.service,'acknowledged-upload'),live=f.service.get(task.taskId),application=live.applications[0];live.factSnapshot.facts.push({id:'candidate.email',value:'test@example.com',sourceRef:'synthetic:test'});const beforeFields=f.service.formState(f.snapshot),createdAt=new Date().toISOString(),upload={id:'upload-op',state:'verified',actions:[{kind:'upload',beforeFields}],index:1,evidence:[{action:{kind:'upload'},evidence:{accepted:true}}],createdAt,sourceSnapshot:structuredClone(f.snapshot)},ack={id:'ack-op',state:'verified',actions:[{kind:'activate',purpose:'resume_upload_acknowledgement'}],index:1,evidence:[{action:{kind:'activate'},evidence:{accepted:true}}],createdAt,sourceSnapshot:structuredClone(f.snapshot)};
 application.operations=[upload,ack];application.operation=ack;application.snapshot=structuredClone(f.snapshot);f.service.recordResumeObservation(application,f.snapshot,{observationId:'ack-1'});f.service.recordResumeObservation(application,f.snapshot,{observationId:'ack-2'});
 const execution=f.service.resumeExecutionFor(application,f.snapshot),boundary=f.service.nativeParseBoundary(application,live);assert.equal(execution.phase,'parsed');assert.equal(boundary.eligible,true,JSON.stringify({execution,boundary}));
}));

test('the service derives the native-parse gate from operation evidence and retains operation history',fixture(async f=>{
 f.snapshot.fields=[{ref:'resume-file',label:'上传文件简历',semanticId:'resume.file',value:'旧简历.pdf',type:'file',filePurpose:'resume'},{ref:'runtime-name',label:'姓名',semanticId:'candidate.name',group:'基本信息',value:'旧姓名',type:'text'}];
 let task=createWithResume(f.service,'resume-workflow-gate');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'upload',fieldRef:'resume-file'}]});await wait();
 let live=f.service.get(task.taskId),application=live.applications[0];
 assert.equal(f.service.resumeExecutionFor(application,f.snapshot).phase,'parse_pending');assert.equal('workflow' in application,false);
 await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:live.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]}),/not_ready_for_actions/);
 f.snapshot.fields[0].value='resume.pdf';f.snapshot.attachmentReceipt=[{fieldRef:'resume-file',filename:'resume.pdf'}];let observed=await f.service.observe({taskId:task.taskId});live=f.service.get(task.taskId);application=live.applications[0];assert.equal(application.operation.postcondition.state,'matched');application.operations[0].verifyUntil=new Date(Date.now()-1).toISOString();f.service.recordResumeObservation(application,f.snapshot,{observationId:'deadline-1'});f.service.recordResumeObservation(application,f.snapshot,{observationId:'deadline-2'});observed=await f.service.observe({taskId:task.taskId});
 await f.service.apply({taskId:task.taskId,expectedRevision:observed.task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});await wait();
 live=f.service.get(task.taskId);application=live.applications[0];
 assert.equal(application.operations.length,2);assert.equal(application.operations[0].actions[0].kind,'upload');assert.equal(application.operations[1].actions[0].kind,'fill');
 const count=application.resumeObservations.length,afterFill=new Date(Date.parse(application.operations[1].createdAt)+1).toISOString(),key=f.service.fieldKey(f.snapshot,f.snapshot.fields[1]),parsedValues=[{key,value:'测试姓名',checked:null}],digest='self-fill-digest';
 assert.equal(f.service.recordResumeObservation(application,f.snapshot,{at:afterFill,observationId:'after-autofill'}),null);assert.equal(application.resumeObservations.length,count);
 application.resumeObservations.push({attemptOperationId:application.operations[0].id,at:afterFill,parsedValues,digest},{attemptOperationId:application.operations[0].id,at:new Date(Date.parse(afterFill)+1).toISOString(),parsedValues,digest});
 assert.equal(f.service.resumeExecutionFor(application,f.snapshot).phase,'unavailable');assert.equal(f.service.nativeParseBoundary(application,live).eligible,false);
}));

test('a verified Recipe branch skips repeated no-effect parse waiting after upload',fixture(async f=>{
 const source=structuredClone(f.snapshot),target=structuredClone(f.snapshot);source.fields.unshift({ref:'resume-file',label:'上传简历',semanticId:'resume.file',value:'resume.pdf',type:'file',filePurpose:'resume'});source.attachmentReceipt=[{fieldRef:'resume-file',filename:'resume.pdf'}];target.fields=structuredClone(source.fields);target.attachmentReceipt=structuredClone(source.attachmentReceipt);target.fields.find(field=>field.ref==='runtime-name').value='测试姓名';
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'verified-upload-fallback',predecessorKind:'upload',actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});
 markRecipeTerminal(f.service.store.data.recipes,target,{predecessorKind:'fill',coverageFactIds:['candidate.name']});Object.assign(f.snapshot,structuredClone(source));f.service.store.write();
 const task=createWithResume(f.service,'verified-upload-fallback'),live=f.service.get(task.taskId),application=live.applications[0],createdAt=new Date().toISOString();
 application.operation={id:'accepted-upload',state:'verified',actions:[{kind:'upload',fieldRef:'resume-file',filename:'resume.pdf',beforeFields:f.service.formState(source)}],index:1,evidence:[{action:{kind:'upload'},evidence:{accepted:true}}],createdAt,verifyUntil:new Date(Date.now()+30_000).toISOString(),sourceSnapshot:structuredClone(source)};application.operations=[application.operation];application.snapshot=structuredClone(source);live.state='waiting_native_parse';
 const strategy=f.service.resumeStrategyFor(source,live,application);assert.equal(strategy.kind,'autonomous_fill');assert.equal(strategy.reason,'recipe_verified_upload_without_parse');assert.ok(strategy.recipeTransitionId);
 const result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'recipe',JSON.stringify(result));assert.equal(result.task.state,'finished');assert.equal(result.actionCount,1);assert.ok(result.elapsedMs<4_000);assert.equal(application.resumeObservations.length<3,true);
}));

test('a stable partial parser change can follow a verified autonomous Recipe branch',fixture(async f=>{
 const baseline=structuredClone(f.snapshot),source=structuredClone(f.snapshot),target=structuredClone(f.snapshot);baseline.fields.push({ref:'site-note',label:'站点状态',semanticId:null,value:'',type:'text'});source.fields.push({ref:'site-note',label:'站点状态',semanticId:null,value:'已读取',type:'text'});target.fields=structuredClone(source.fields);target.fields.find(field=>field.ref==='runtime-name').value='测试姓名';
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'stable-partial-fallback',predecessorKind:'upload',actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});
 const task=createWithResume(f.service,'stable-partial-fallback'),live=f.service.get(task.taskId),application=live.applications[0],createdAt=new Date().toISOString();application.operation={id:'accepted-upload',state:'verified',actions:[{kind:'upload',fieldRef:'resume-file',beforeFields:f.service.formState(baseline)}],index:1,evidence:[{action:{kind:'upload'},evidence:{accepted:true}}],createdAt,sourceSnapshot:structuredClone(baseline)};application.operations=[application.operation];f.service.recordResumeObservation(application,source,{observationId:'partial-1'});f.service.recordResumeObservation(application,source,{observationId:'partial-2'});
 assert.equal(f.service.resumeExecutionFor(application,source).phase,'parsed');const strategy=f.service.resumeStrategyFor(source,live,application);assert.equal(strategy.kind,'autonomous_fill');assert.equal(strategy.reason,'recipe_verified_partial_parse_fallback');
}));

test('live native-parser evidence takes priority over an upload fallback Recipe',fixture(async f=>{
 const source=structuredClone(f.snapshot),target=structuredClone(f.snapshot);source.fields.unshift({ref:'resume-file',label:'上传简历',semanticId:'resume.file',value:'resume.pdf',type:'file',filePurpose:'resume'});target.fields=structuredClone(source.fields);target.fields.find(field=>field.ref==='runtime-name').value='测试姓名';
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'parser-priority',predecessorKind:'upload',actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});
 const task=createWithResume(f.service,'parser-priority'),live=f.service.get(task.taskId),application=live.applications[0],createdAt=new Date().toISOString();application.operation={id:'accepted-upload',state:'verified',actions:[{kind:'upload',fieldRef:'resume-file',beforeFields:f.service.formState(source)}],index:1,evidence:[{action:{kind:'upload'},evidence:{accepted:true}}],createdAt,sourceSnapshot:structuredClone(source)};application.operations=[application.operation];source.nativeResumeParse={available:true,candidates:[],inProgress:true};
 const strategy=f.service.resumeStrategyFor(source,live,application);assert.equal(strategy.kind,'native_parse_pending');assert.equal(strategy.reason,'parse_pending');
}));

test('without native resume capability the service opens autofill and ends at the safe fact boundary',fixture(async f=>{
 let task=createWithResume(f.service,'resume-no-native-parser');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});const observed=await f.service.observe({taskId:task.taskId});
 assert.equal('workflow' in observed.task.applications[0],false);assert.equal(observed.resumeStrategy.kind,'autonomous_fill');
 const result=await f.service.run_adaptive_plan({taskId:task.taskId,expectedRevision:observed.task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});
 assert.equal(result.task.state,'finished');assert.equal(result.reason,'resume_boundary_complete');assert.equal('workflow' in result.task.applications[0],false);
}));

test('verified native parsing ends at the resume boundary and leaves site-specific blanks to the user',fixture(async f=>{
 f.snapshot.fields=[
  {ref:'name',label:'姓名*',semanticId:'candidate.name',group:'基本信息',value:'测试姓名',type:'text',required:true,requiredKnown:true},
  {ref:'email',label:'邮箱*',semanticId:'candidate.email',group:'基本信息',value:'test@example.com',type:'text',required:true,requiredKnown:true},
  {ref:'school',label:'学校名称*',semanticId:'education.school',group:'教育经历',value:'测试大学',type:'text',required:true,requiredKnown:true},
  {ref:'source',label:'你从哪里获知招聘信息？*',semanticId:null,group:'网站问题',value:'',type:'search',required:true,requiredKnown:true}
 ];
 const task=createWithResume(f.service,'native-boundary'),live=f.service.get(task.taskId),application=live.applications[0];
 live.factSnapshot.facts.push({id:'candidate.email',value:'test@example.com',sourceRef:'synthetic:test'},{id:'education.school',value:'测试大学',sourceRef:'synthetic:test'});
 markNativeParsed(f.service,application,f.snapshot,f.snapshot.fields.slice(0,3));application.budget.activeMs=application.budget.limits.activeMs;
 const result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.task.state,'finished');assert.equal(result.task.applications[0].outcome,'partial');assert.deepEqual(result.task.applications[0].missing,['你从哪里获知招聘信息']);assert.equal(result.actionCount,0);
 assert.equal(f.calls.some(item=>item.action==='act'),false);
}));

test('native parsing verifies the complete frozen plan from explicit ATS field identities without agent coverage mapping',fixture(async f=>{
 f.snapshot.fields=[
  {ref:'name',label:'姓名*',semanticId:'formily-item-name',group:null,value:'测试姓名',type:'text'},
  {ref:'country',label:'手机号码*',semanticId:'formily-item-mobile',group:null,value:'+86',type:'search'},
  {ref:'mobile',label:'手机号码*',semanticId:'formily-item-mobile',group:null,value:'15200000000',type:'text'},
  {ref:'email',label:'邮箱*',semanticId:'formily-item-email',group:null,value:'test@example.com',type:'text'},
  {ref:'school',label:'学校名称*',semanticId:'formily-item-school',group:'formily-item-education_list',value:'测试大学',type:'text'}
 ];
 let task=f.service.create({clientRequestId:'native-formal-facts',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'name',value:'测试姓名',sourceRef:'synthetic:test'},{id:'mobile',value:'15200000000',sourceRef:'synthetic:test'},{id:'email',value:'test@example.com',sourceRef:'synthetic:test'},{id:'education_1_school',value:'测试大学',sourceRef:'synthetic:test'}],attachment:{resource:'test'}});const live=f.service.get(task.taskId),application=live.applications[0];
 markNativeParsed(f.service,application,f.snapshot,f.snapshot.fields.filter(field=>field.ref!=='country'));
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});const result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.task.state,'finished');assert.equal(result.path,'recipe');assert.equal(result.outcome,'partial');assert.equal(result.actionCount,0);assert.equal(result.task.applications[0].performance.executionPath,'recipe');assert.equal(f.calls.some(item=>item.action==='act'),false);
 assert.deepEqual(application.coverage.map(row=>row.disposition),Array(application.coverage.length).fill('unreviewed'));
 assert.deepEqual(f.service.nativeParseBoundary(application,live).verifiedFactIds.sort(),['education_1_school','email','mobile','name']);
}));

test('a conflicting key fact prevents native-parse termination and permits correction',fixture(async f=>{
 f.snapshot.fields=[
  {ref:'name',label:'姓名*',semanticId:'candidate.name',group:'基本信息',value:'错误姓名',type:'text'},
  {ref:'email',label:'邮箱*',semanticId:'candidate.email',group:'基本信息',value:'test@example.com',type:'text'},
  {ref:'school',label:'学校名称*',semanticId:'education.school',group:'教育经历',value:'测试大学',type:'text'}
 ];
 const task=createWithResume(f.service,'native-conflict'),live=f.service.get(task.taskId),application=live.applications[0];
 live.factSnapshot.facts.push({id:'candidate.email',value:'test@example.com',sourceRef:'synthetic:test'},{id:'education.school',value:'测试大学',sourceRef:'synthetic:test'});
 markNativeParsed(f.service,application,f.snapshot);
 f.service.claim({taskId:task.taskId,expectedRevision:task.revision});const observed=await f.service.observe({taskId:task.taskId});
 assert.equal(observed.task.state,'running');assert.equal(observed.resumeStrategy.reason,'native_parse_requires_correction');assert.deepEqual(f.service.nativeParseBoundary(application,live).conflicts,['candidate.name']);
 const context=f.service.learningContextFor(application,live);
 assert.deepEqual(context.resumeStrategy,{kind:'autonomous_fill',reason:'native_parse_requires_correction',conflictFactIds:['candidate.name']});
 assert.deepEqual(context.suggestedActions,[{kind:'fill',fieldRef:'name',factId:'candidate.name'}]);
 await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:observed.task.revision,actions:[{kind:'fill',fieldRef:'email',factId:'candidate.email'}]}),/native_parse_correction_scope_forbidden/);
 await f.service.apply({taskId:task.taskId,expectedRevision:observed.task.revision,actions:[{kind:'fill',fieldRef:'name',factId:'candidate.name'}]});await wait();await f.service.observe({taskId:task.taskId});
 const current=f.service.get(task.taskId),corrected=f.service.nativeParseBoundary(current.applications[0],current);assert.equal(corrected.eligible,true,JSON.stringify(corrected));assert.deepEqual(corrected.conflicts,[]);
 current.applications[0].coverage.unshift({key:'stale-previous-page-row',fieldRef:'name',label:'姓名*',type:'text',value:'错误姓名',disposition:'mapped',factId:'candidate.name'});
 const withStaleRow=f.service.nativeParseBoundary(current.applications[0],current);
 assert.equal(withStaleRow.eligible,true,JSON.stringify(withStaleRow));assert.deepEqual(withStaleRow.conflicts,[]);
}));

test('native parsing compares a month-only resume date with the same normalized readback rule',fixture(async f=>{
 f.snapshot.fields=[
  {ref:'name',label:'姓名',semanticId:'name',group:'基本信息',value:'测试姓名',type:'text'},
  {ref:'education-start',label:'开始日期',semanticId:'education_1_start',group:'教育经历',value:'2020-09-01',type:'text',readOnly:true}
 ];
 const task=f.service.create({clientRequestId:'native-month-date',targets:[{url:f.snapshot.url,tabId:1}],facts:[
  {id:'name',value:'测试姓名',sourceRef:'synthetic:test'},
  {id:'education_1_start',value:'2020-09',sourceRef:'synthetic:test'}
 ],attachment:{resource:'test'}}),live=f.service.get(task.taskId),application=live.applications[0];
 markNativeParsed(f.service,application,f.snapshot);
 application.coverage=f.snapshot.fields.map(field=>({key:field.ref,fieldRef:field.ref,label:field.label,type:field.type,value:field.value,disposition:'mapped',factId:field.semanticId}));
 const boundary=f.service.nativeParseBoundary(application,live);
 assert.equal(boundary.eligible,true,JSON.stringify(boundary));
 assert.deepEqual(boundary.conflicts,[]);
 assert.ok(boundary.verifiedFactIds.includes('education_1_start'));
}));

test('persisted complete-plan proof can finish a resumed task while the browser is unavailable',fixture(async f=>{
 f.snapshot.fields=[
  {ref:'name',label:'姓名',semanticId:'name',group:'基本信息',value:'测试姓名',type:'text'},
  {ref:'email',label:'邮箱',semanticId:'email',group:'基本信息',value:'test@example.com',type:'text'},
  {ref:'school',label:'学校',semanticId:'education_1_school',group:'教育经历',value:'测试大学',type:'text'}
 ];
 const task=f.service.create({clientRequestId:'persisted-native-boundary',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'name',value:'测试姓名',sourceRef:'synthetic:test'},{id:'email',value:'test@example.com',sourceRef:'synthetic:test'},{id:'education_1_school',value:'测试大学',sourceRef:'synthetic:test'}],attachment:{resource:'test'}}),live=f.service.get(task.taskId),application=live.applications[0];
 application.snapshot=structuredClone(f.snapshot);application.sectionSnapshots={};application.coverage=f.snapshot.fields.map((field,index)=>({key:String(index),fieldRef:field.ref,label:field.label,type:field.type,value:field.value,disposition:'mapped',factId:field.semanticId}));markNativeParsed(f.service,application,f.snapshot);f.service.syncObligationLedger(application,live);
 f.setBrowser(async()=>{throw Error('browser_timeout')});const claimed=f.service.claim({taskId:task.taskId,expectedRevision:task.revision}),result=f.service.finish({taskId:task.taskId,expectedRevision:claimed.revision,outcome:'partial'});
 assert.equal(result.state,'finished');assert.equal(result.applications[0].outcome,'partial');assert.equal(f.calls.length,0);
}));

test('service restart resumes a pending native parse without replaying the resume action',fixture(async f=>{
 const task=createWithResume(f.service,'restart-pending-native'),live=f.service.get(task.taskId),application=live.applications[0],beforeFields=f.service.formState(f.snapshot);
 application.operation={id:'pending-native-op',state:'awaiting_verification',actions:[{kind:'native_parse',beforeFields}],index:1,evidence:[{action:{kind:'native_parse'},evidence:{accepted:true}}],createdAt:new Date().toISOString(),sourceSnapshot:structuredClone(f.snapshot)};application.operations=[application.operation];live.state='waiting_native_parse';f.service.store.write();
 f.restart();const restored=f.service.get(task.taskId),restoredApplication=restored.applications[0];
 assert.equal(restored.state,'queued');assert.equal(restoredApplication.operation.state,'awaiting_verification');assert.equal(f.service.resumeExecutionFor(restoredApplication).phase,'parse_pending');assert.equal(restoredApplication.operations.length,1);assert.equal('workflow' in restoredApplication,false);
}));

test('complete-plan native parsing waits for its owned transition to match before finishing',fixture(async f=>{
 f.snapshot.fields=[
  {ref:'name',label:'姓名',semanticId:'name',group:'基本信息',value:'',type:'text'},
  {ref:'email',label:'邮箱',semanticId:'email',group:'基本信息',value:'',type:'text'},
  {ref:'school',label:'学校',semanticId:'education_1_school',group:'教育经历',value:'',type:'text'}
 ];
 let task=f.service.create({clientRequestId:'native-transition-gate',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'name',value:'测试姓名',sourceRef:'synthetic:test'},{id:'email',value:'test@example.com',sourceRef:'synthetic:test'},{id:'education_1_school',value:'测试大学',sourceRef:'synthetic:test'}]});task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;const live=f.service.get(task.taskId),application=live.applications[0];
 const sourceSnapshot=structuredClone(application.snapshot),beforeFields=sourceSnapshot.fields.map(field=>({key:f.service.fieldKey(sourceSnapshot,field),value:'',checked:null}));f.snapshot.fields[0].value='测试姓名';f.snapshot.fields[1].value='test@example.com';f.snapshot.fields[2].value='测试大学';const expectedState=stateFor(f.snapshot,{predecessorKind:'native_parse'}).key,startedAt=new Date().toISOString();
 application.operation={id:'native-transition',state:'verified',actions:[{kind:'native_parse',beforeFields}],index:1,evidence:[{action:{kind:'native_parse'},evidence:{accepted:true}}],createdAt:startedAt,sourceSnapshot,predecessorKind:'native_parse',postcondition:{kind:'recipe',state:'settling',transitionId:'native-transition-edge',sourceState:'source',expectedStates:[expectedState],lockedAt:startedAt,receiptConfirmedAt:startedAt,startedAt,deadlineAt:new Date(Date.now()+500).toISOString()}};application.operations=[application.operation];application.resumeObservations=[];f.service.recordResumeObservation(application,f.snapshot,{observationId:'transition-parse-1'});f.service.recordResumeObservation(application,f.snapshot,{observationId:'transition-parse-2'});
 f.service.store.write();
 const readsBefore=f.calls.filter(item=>item.action==='observe').length,observed=await f.service.observe({taskId:task.taskId});assert.equal(observed.task.state,'running');assert.equal(f.service.get(task.taskId).applications[0].operation.postcondition.state,'matched');const finished=await f.service.run_recipe({taskId:task.taskId,expectedRevision:observed.task.revision});assert.equal(finished.task.state,'finished');assert.equal('workflow' in finished.task.applications[0],false);
 const readsAfter=f.calls.filter(item=>item.action==='observe').length;assert.equal(readsAfter,readsBefore+2);await f.service.observe({taskId:task.taskId});assert.equal(f.calls.filter(item=>item.action==='observe').length,readsAfter);
}));

test('an exhausted write cannot bypass the plan with a caller-supplied fact exception',fixture(async f=>{
 f.snapshot.fields[0]={ref:'runtime-phone',label:'手机号码',semanticId:'candidate.mobile',group:'基本信息',value:'13300000000',type:'text'};
 let task=f.service.create({clientRequestId:'deferred-identity',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'candidate.mobile',value:'15200000000',sourceRef:'synthetic:test'}]});task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;const live=f.service.get(task.taskId),application=live.applications[0];
 application.coverage=[{key:'phone',fieldRef:'runtime-phone',label:'手机号码',semanticId:'candidate.mobile',factId:'candidate.mobile',disposition:'mapped',value:'13300000000',type:'text'}];
 application.operation={id:'locked-phone',state:'failed',error:'user_value_or_field_changed',index:0,actions:[{kind:'fill',fieldRef:'runtime-phone',semanticId:'candidate.mobile',group:'基本信息',value:'15200000000',before:'13300000000',factId:'candidate.mobile',budgetField:'phone-key'}],evidence:[],sourceSnapshot:structuredClone(application.snapshot)};
 application.budget.strategies['phone-key']=2;
 assert.throws(()=>f.service.finish({taskId:task.taskId,expectedRevision:task.revision,outcome:'partial',unsupported:['fact:candidate.mobile']}),/application_plan_exception_requires_review/);
 const current=f.service.get(task.taskId);
 assert.equal(current.state,'running');assert.equal(f.service.applicationPlanAssessment(current.applications[0],current).pending.length,1);
}));

test('a terminal transition executes required fact corrections before finishing',fixture(async f=>{
 const source=structuredClone(f.snapshot),target=structuredClone(f.snapshot);target.fields[0].value='测试姓名';
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'terminal-learn',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name'}]});markRecipeTerminal(f.service.store.data.recipes,target,{predecessorKind:'fill',coverageFactIds:['candidate.name']});f.service.store.write();
 const task=create(f.service,'terminal-transition'),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'recipe');assert.equal(result.actionCount,1);assert.equal(result.task.state,'finished');assert.equal(result.task.applications[0].recipe.status,'terminal');assert.equal(f.snapshot.fields[0].value,'测试姓名');
}));

test('an already-correct terminal page finishes after upload without replaying the learned fill edge',fixture(async f=>{
 const source=structuredClone(f.snapshot),target=structuredClone(f.snapshot);target.fields[0].value='测试姓名';
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'terminal-learn',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name'}]});
 markRecipeTerminal(f.service.store.data.recipes,target,{predecessorKind:'fill',coverageFactIds:['candidate.name']});
 Object.assign(f.snapshot,structuredClone(target));f.service.store.write();
 const task=create(f.service,'terminal-after-upload'),application=f.service.get(task.taskId).applications[0];
 application.operation={id:'prior-upload',state:'verified',actions:[{kind:'upload',beforeFields:f.service.formState(target)}],index:1,evidence:[{action:{kind:'upload'},evidence:{accepted:true}}],createdAt:new Date(Date.now()-31_000).toISOString(),verifyUntil:new Date(Date.now()-1_000).toISOString(),sourceSnapshot:structuredClone(source)};
 application.operations=[application.operation];
 application.resumeObservations=[];f.service.recordResumeObservation(application,target,{observationId:'unchanged-1'});f.service.recordResumeObservation(application,target,{observationId:'unchanged-2'});
 const result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'recipe');assert.equal(result.actionCount,0);assert.equal(result.task.state,'finished');assert.equal(result.task.applications[0].recipe.status,'terminal');
 assert.equal(f.calls.some(item=>item.action==='act'),false);
}));

test('a shared ATS Recipe path cannot hide another frozen fact',fixture(async f=>{
 const source=structuredClone(f.snapshot),target=structuredClone(f.snapshot);target.fields[0].value='测试姓名';
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'name-path',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name'}]});markRecipeTerminal(f.service.store.data.recipes,target,{predecessorKind:'fill',coverageFactIds:['candidate.name']});
 const other=structuredClone(source);other.fields.push({ref:'runtime-height',label:'身高',semanticId:'candidate.height',group:'其他公司字段',value:'',type:'text'});const otherTarget=structuredClone(other);otherTarget.fields[1].value='175';
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:other,targetSnapshot:otherTarget,runId:'other-company-path',actions:[{kind:'fill',fieldRef:'runtime-height',factId:'candidate.height'}]});f.service.store.write();
 const task=create(f.service,'scoped-path');f.service.get(task.taskId).factSnapshot.facts.push({id:'candidate.height',value:'175',sourceRef:'synthetic:test'});
 const result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'adaptive');assert.equal(result.task.state,'running');assert.equal(f.snapshot.fields[0].value,'测试姓名');assert.equal(result.actionCount,1);assert.deepEqual(result.learningContext.applicationPlan.pending.map(item=>item.factId),['candidate.height']);
}));

test('an unrelated site field does not invalidate an exact local Recipe action',fixture(async f=>{
 await learnOneField(f);f.snapshot.url='https://jobs.example.test/position/777777/apply';f.snapshot.documentEpoch='doc-3';f.snapshot.fields[0].value='';f.snapshot.fields.push({ref:'city',label:'城市',semanticId:'candidate.city',type:'text',value:''});f.calls.length=0;
 const task=create(f.service,'drift',f.snapshot.url),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'recipe');assert.equal(result.task.state,'finished');assert.ok(result.elapsedMs<4_000);assert.equal(f.calls.some(item=>item.action==='act'),true);
}));

test('an incomplete surface gets one read-only re-observation before learning handoff',fixture(async f=>{
 const stable={...structuredClone(f.snapshot),url:'https://jobs.example.test/position/123456/detail',formStage:'detail',fields:[],applicationFieldCount:0,controls:[{ref:'stable-start',kind:'start',label:'立即申请',role:'button',tag:'button',type:'button',safe:true}],interactions:[]};
 const early={...structuredClone(stable),formStage:'unknown',pageState:'unknown',controls:[],activeDialog:{ref:'loading',role:'dialog',title:'',text:'',blocking:true}},target={...structuredClone(f.snapshot),url:'https://jobs.example.test/position/123456/apply',documentEpoch:'doc-2'};
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:stable,targetSnapshot:target,actions:[{kind:'start',controlRef:'stable-start'}],runId:'seed-run'});f.service.store.write();
 let reads=0;f.service.sleep=async()=>{};f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe'){reads++;return structuredClone(reads===1?early:stable)}throw Error(`unexpected_browser_${action}`)});
 let task=create(f.service,'stable-detail',stable.url);task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});const first=await f.service.observe({taskId:task.taskId});assert.equal(f.service.recipeFor(first.snapshot,f.service.get(task.taskId),f.service.get(task.taskId).applications[0]).status,'missing_transition');
 const settled=await f.service.awaitIncompleteSurface(task.taskId,first),live=f.service.get(task.taskId),selected=f.service.recipeFor(settled.snapshot,live,live.applications[0]);
 assert.equal(selected.status,'reusable');assert.equal(selected.actions[0].kind,'start');assert.equal(reads,2);
}));

test('a known local Recipe step does not wait on unrelated partial form structure',fixture(async f=>{
 const stable=structuredClone(f.snapshot),target=structuredClone(f.snapshot);target.fields[0].value='测试姓名';
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:stable,targetSnapshot:target,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}],runId:'known-form'});f.service.store.write();
 const partial=structuredClone(stable);partial.fields.push({ref:'loading-field',label:'加载中的临时字段',semanticId:'loading',type:'text',value:''});
 let reads=0;f.service.sleep=async()=>{};f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe'){reads++;return structuredClone(reads<=3?partial:stable)}throw Error(`unexpected_browser_${action}`)});
 let task=create(f.service,'known-partial-form');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});const first=await f.service.observe({taskId:task.taskId});
 const settled=await f.service.awaitIncompleteSurface(task.taskId,first),live=f.service.get(task.taskId),selected=f.service.recipeFor(settled.snapshot,live,live.applications[0]);
 assert.equal(selected.status,'reusable');assert.equal(reads,1);
}));

test('recipe execution observes native parsing until it is verified instead of handing off early',fixture(async f=>{
 let task=await ready(f.service,'native-wait');const live=f.service.get(task.taskId),application=live.applications[0];live.factSnapshot.attachment={resource:'test'};
 const beforeFields=f.service.formState(f.snapshot);
 application.operation={id:'native-op',state:'awaiting_verification',actions:[{kind:'native_parse',beforeFields}],index:1,evidence:[{action:{kind:'native_parse'},evidence:{accepted:true}}],createdAt:new Date().toISOString(),sourceSnapshot:structuredClone(f.snapshot),predecessorKind:'upload'};application.operations=[application.operation];
 f.service.get(task.taskId).state='waiting_native_parse';let reads=0;f.service.sleep=async()=>{};f.setBrowser(async action=>{if(action!=='observe')throw Error(`unexpected_browser_${action}`);reads++;const next=structuredClone(f.snapshot);if(reads>=2)next.fields[0].value='网站解析姓名';return next});
 const result=await f.service.waitForOperation(task.taskId,'native-op',{allowNativeWait:true,timeoutMs:600});
 const finalApplication=f.service.get(task.taskId).applications[0];assert.equal(result.state,'verified',JSON.stringify({reads,resumeExecution:f.service.resumeExecutionFor(finalApplication),operation:finalApplication.operation}));assert.ok(reads>=3);assert.equal(f.service.resumeExecutionFor(finalApplication).phase,'parsed');assert.equal('workflow' in finalApplication,false);
}));

test('recipe predecessor follows every verified transition kind',fixture(async f=>{
 const application={evidence:[{action:{kind:'start'}}]};assert.equal(f.service.predecessorKind(application),'start');
 application.operation={state:'sent',predecessorKind:'start',actions:[{kind:'upload'}]};assert.equal(f.service.predecessorKind(application),'start');
 application.operation={state:'verified',predecessorKind:'native_parse',actions:[{kind:'fill'},{kind:'fill'}]};assert.equal(f.service.predecessorKind(application),'fill');
}));

test('every public task view hides resume values and final submit remains impossible',fixture(async f=>{
 const task=await ready(f.service);const view=f.service.status({taskId:task.taskId});assert.equal(JSON.stringify(view).includes('测试姓名'),false);assert.equal(view.finalSubmitAllowed,false);
 await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:view.revision,actions:[{kind:'submit'}]}),/action_invalid:0:kind_forbidden/);
}));

test('main-world dialog activation is compiled with a serializable stable anchor',fixture(async f=>{
 f.snapshot.pageState='resume_prerequisite';f.snapshot.activeDialog={ref:'dialog-1',role:'dialog',title:'申请职位',text:'请编辑简历'};
 f.snapshot.fields=[];f.snapshot.controls=[];f.snapshot.interactions=[
  {ref:'edit-1',label:'编辑',role:'button',tag:'button',type:'button',surface:'dialog',safe:true,formSubmit:false},
  {ref:'edit-2',label:'编辑',role:'button',tag:'button',type:'button',surface:'dialog',safe:true,formSubmit:false},
 ];
 let task=await ready(f.service,'main-world-anchor');
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'activate',controlRef:'edit-2',activationMode:'main_world_pointer',expectedPostcondition:'surface_changed',purpose:'resume_replacement'}]});
 await wait();
 const action=f.calls.find(item=>item.action==='act').args.actionSpec;
 assert.deepEqual(action.interactionAnchor,{surface:'dialog',label:'编辑',role:'button',tag:'button',type:'button',ordinal:1});
 assert.doesNotThrow(()=>structuredClone(action.interactionAnchor));
}));

test('an application entry is verified when it opens a same-page prerequisite dialog',fixture(async f=>{
 const action={kind:'start',beforeURL:f.snapshot.url,beforeDocumentEpoch:f.snapshot.documentEpoch,beforeFieldCount:0,beforePageState:'normal'};
 const next={...structuredClone(f.snapshot),formStage:'unknown',pageState:'unknown',activeDialog:{ref:'dialog-1',role:'dialog',title:'隐私提示',text:'我已阅读并同意'},fields:[]};
 assert.equal(f.service.actionVerified(next,action),true);
 assert.equal(f.service.actionUnchanged(next,action),false);
}));

test('a verified same-document field receipt survives control detachment but never a conflict or reload',fixture(async f=>{
 const action={kind:'fill',fieldRef:'runtime-name',value:'测试姓名'},evidence={action:{kind:'fill',fieldRef:'runtime-name'},evidence:{accepted:true,fieldRef:'runtime-name',value:'测试姓名',documentEpoch:'doc-1'}};
 const detached={...structuredClone(f.snapshot),fields:[]};
 assert.equal(f.service.actionVerified(detached,action,evidence),true);
 const conflicting=structuredClone(f.snapshot);conflicting.fields[0].value='其他值';
 assert.equal(f.service.actionVerified(conflicting,action,evidence),false);
 assert.equal(f.service.actionVerified({...detached,documentEpoch:'doc-2'},action,evidence),false);
 assert.equal(f.service.actionVerified(detached,action,{...evidence,action:{kind:'select',fieldRef:'runtime-name'}}),false);
}));

test('cold learning records a stable target when a verified field leaves the observable DOM',fixture(async f=>{
 f.snapshot.fields.push({ref:'optional-note',label:'补充说明',semanticId:null,group:'基本信息',value:'',type:'text'});
 let task=await ready(f.service,'detached-after-fill'),writes=0;
 f.setBrowser(async(action,args)=>{if(action==='observe')return structuredClone(f.snapshot);if(action==='act'){writes++;const field=f.snapshot.fields.find(item=>item.ref===args.actionSpec.fieldRef);field.value=args.actionSpec.value;f.snapshot.fields=f.snapshot.fields.filter(item=>item.ref!==field.ref);return{accepted:true,kind:args.actionSpec.kind,fieldRef:args.actionSpec.fieldRef,value:args.actionSpec.value,documentEpoch:f.snapshot.documentEpoch}}if(action==='progress')return{accepted:true};if(action==='stop')return{stopped:true};throw Error(action)});
 const result=await f.service.run_adaptive_plan({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]}),application=f.service.get(task.taskId).applications[0],recipe=Object.values(f.service.store.data.recipes)[0];
 assert.equal(writes,1);assert.equal(application.operation.state,'verified');assert.equal(application.operation.postcondition.state,'matched');
 assert.equal(application.operation.verification.status,'passed');assert.equal(Object.values(recipe.transitions).length,1);assert.equal(result.resumeProjection.missingCount,0);
}));

test('a resume replacement continuation carries its strict postcondition into verification',fixture(async f=>{
 f.snapshot.pageState='unknown';f.snapshot.activeDialog={ref:'replace-dialog',role:'dialog',title:'',text:'上传新简历将替换现有简历内容，请确认是否继续 取消 继续'};f.snapshot.canAdvance=true;
 f.snapshot.controls=[{ref:'continue',kind:'next',label:'继续',safe:true}];
 f.snapshot.interactions=[{ref:'continue',label:'继续',role:'button',tag:'button',type:'button',surface:'dialog',safe:true,formSubmit:false},{ref:'cancel',label:'取消',role:'button',tag:'button',type:'button',surface:'dialog',safe:true,formSubmit:false}];
 const task=await ready(f.service,'resume-replacement-continuation');
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'next',controlRef:'continue'}]});
 const action=f.service.get(task.taskId).applications[0].operation.actions[0],after=structuredClone(f.snapshot);
 assert.equal(action.resumeReplacement,true);assert.equal(typeof action.beforeInteractionState,'string');
 after.activeDialog=null;after.interactions=[];after.controls=[];after.evidence={complete:false};
 assert.equal(f.service.actionVerified(after,action),true);
 assert.equal(f.service.actionUnchanged(after,action),false);
}));

test('closing one dialog is verified even when an unrelated overlay remains',fixture(async f=>{
 const before=structuredClone(f.snapshot);before.activeDialog={ref:'recruiting-help',role:'dialog',title:'',text:'校招问题，随时来问',blocking:true};before.interactions=[{ref:'close-help',label:'关闭公告',role:'button',tag:'button',type:'button',surface:'dialog',safe:true,interpreted:true}];
 const task=await ready(f.service,'dialog-replaced'),stored=f.service.get(task.taskId),application=stored.applications[0];application.snapshot=before;stored.revision++;f.service.store.write();
 await f.service.apply({taskId:task.taskId,expectedRevision:stored.revision,actions:[{kind:'activate',controlRef:'close-help',activationMode:'isolated_pointer',expectedPostcondition:'dialog_closed'}]});
 const action=application.operation.actions[0],after={...structuredClone(before),activeDialog:{ref:'assistant-shell',role:'dialog',title:'',text:'',blocking:true},interactions:[]};
 assert.equal(action.beforeDialogRef,'recruiting-help');assert.equal(f.service.actionVerified(after,action),true);
}));

test('a manual gate may move the same job from detail to a new apply tab',fixture(async f=>{
 const detail='https://jobs.example.test/position/123456',apply=`${detail}/apply`;
 assert.equal(f.service.applicationContinuationURL(detail,apply),true);
 assert.equal(f.service.applicationContinuationURL(detail,'https://jobs.example.test/position/999999/apply'),false);
 assert.equal(f.service.applicationContinuationURL(detail,'https://other.example.test/position/123456/apply'),false);
 const task=f.service.create({clientRequestId:'manual-new-tab',targets:[{url:detail,tabId:7}],facts:[{id:'candidate.name',value:'测试姓名',sourceRef:'synthetic:test'}]}),stored=f.service.get(task.taskId),application=stored.applications[0];
 application.operation={id:'start-op',state:'verified',actions:[{kind:'start'}],index:1,evidence:[{action:{kind:'start'},evidence:{accepted:true}}],createdAt:new Date().toISOString()};
 f.setBrowser(async(action,args)=>{if(action==='observe'&&args.targetURL===detail)throw Error('target_url_mismatch');if(action==='current')return{tabId:8,url:apply};if(action==='observe'&&args.tabId===8&&args.targetURL===apply)return{...structuredClone(f.snapshot),url:apply};throw Error(`unexpected_browser_${action}`)});
 const observed=await f.service.observeApplicationPage(stored,application,{});
 assert.equal(observed.adoptedURL,apply);assert.equal(observed.adoptedTabId,8);assert.equal(observed.snapshot.formStage,'apply');
}));

test('a verified next step may adopt an arbitrary same-origin route in the same tab',fixture(async f=>{
 const before='https://jobs.example.test/volunteer',after='https://jobs.example.test/resume/choose?step=2';
 const task=f.service.create({clientRequestId:'same-tab-next-route',targets:[{url:before,tabId:7}],facts:[{id:'candidate.name',value:'测试姓名',sourceRef:'synthetic:test'}]}),stored=f.service.get(task.taskId),application=stored.applications[0];
 application.operation={id:'next-op',state:'verified',actions:[{kind:'next'}],index:1,evidence:[{action:{kind:'next'},evidence:{accepted:true}}],createdAt:new Date().toISOString()};
 application.evidence=[{operationId:'next-op',action:{kind:'next'},evidence:{accepted:true}}];
 f.setBrowser(async(action,args)=>{if(action==='observe'&&args.targetURL===before)throw Error('target_url_mismatch');if(action==='current')return{tabId:7,url:after};if(action==='observe'&&args.tabId===7&&args.targetURL===after)return{...structuredClone(f.snapshot),url:after};throw Error(`unexpected_browser_${action}`)});
 const observed=await f.service.observeApplicationPage(stored,application,{});
 assert.equal(observed.adoptedURL,after);assert.equal(observed.adoptedTabId,null);assert.equal(observed.snapshot.url,after);
}));

test('a verified next step may cross subdomains inside one recruitment site family',fixture(async f=>{
 const before='https://xiaoyuan.zhaopin.com/volunteer',after='https://i.zhaopin.com/resume/choose?step=2';
 assert.equal(f.service.applicationContinuationSite(before,after),true);
 assert.equal(f.service.applicationContinuationSite(before,'https://accounts.example.com/login'),false);
 const task=f.service.create({clientRequestId:'cross-subdomain-next',targets:[{url:before,tabId:7}],facts:[{id:'candidate.name',value:'测试姓名',sourceRef:'synthetic:test'}]}),stored=f.service.get(task.taskId),application=stored.applications[0];
 application.operation={id:'next-subdomain-op',state:'verified',actions:[{kind:'next'}],index:1,evidence:[{action:{kind:'next'},evidence:{accepted:true}}],createdAt:new Date().toISOString()};
 application.evidence=[{operationId:'next-subdomain-op',action:{kind:'next'},evidence:{accepted:true}}];
 f.setBrowser(async(action,args)=>{if(action==='observe'&&args.targetURL===before)throw Error('target_url_mismatch');if(action==='current')return{tabId:8,url:after};if(action==='observe'&&args.tabId===8&&args.targetURL===after)return{...structuredClone(f.snapshot),url:after};throw Error(`unexpected_browser_${action}`)});
 const observed=await f.service.observeApplicationPage(stored,application,{});
 assert.equal(observed.adoptedURL,after);assert.equal(observed.adoptedTabId,8);
}));

test('an unknown application start reconciles a same-site login redirect without replay',fixture(async f=>{
 const before='https://campus.pingan.com/positionDetail?positionId=one',after='https://talent.pingan.com/recruit/login.html';
 const task=f.service.create({clientRequestId:'same-site-login-reconcile',targets:[{url:before,tabId:7}],facts:[{id:'candidate.name',value:'测试姓名',sourceRef:'synthetic:test'}]}),stored=f.service.get(task.taskId),application=stored.applications[0];
 application.operation={id:'start-unknown',state:'unknown',error:'application_start_redirect_outside_origin',actions:[{kind:'start',beforeURL:before,beforeDocumentEpoch:'doc-1',beforeFieldCount:0,beforePageState:'normal'}],index:0,evidence:[],createdAt:new Date().toISOString()};
 f.setBrowser(async(action,args)=>{if(action==='observe'&&args.targetURL===before)throw Error('target_url_mismatch');if(action==='current')return{tabId:7,url:after};if(action==='observe'&&args.targetURL===after)return{...structuredClone(f.snapshot),url:after,loginRequired:true,formStage:'unknown',fields:[]};throw Error(`unexpected_browser_${action}`)});
 const observed=await f.service.observeApplicationPage(stored,application,{});
 assert.equal(observed.adoptedURL,after);assert.equal(observed.adoptedTabId,null);assert.equal(observed.snapshot.loginRequired,true);assert.equal(application.operation.index,0);
}));

test('an unknown section action reconciles from a bounded control inspection without scanning the whole page',fixture(async f=>{
 let task=await ready(f.service,'section-local-reconcile');const stored=f.service.get(task.taskId),application=stored.applications[0],sectionKey='navigation:项目经历';
 application.snapshot.activeSection=null;application.snapshot.activeSectionKey=null;application.operation={id:'section-unknown',state:'unknown',actions:[{kind:'section',controlRef:'project-nav',section:'项目经历',sectionKey,beforeActiveSection:null}],index:0,evidence:[],createdAt:new Date().toISOString(),sourceSnapshot:structuredClone(application.snapshot)};stored.state='needs_reconcile';f.service.store.write();
 f.setBrowser(async(action,args)=>{if(action==='inspect'){assert.equal(args.sectionSpec.sectionKey,sectionKey);return{documentEpoch:application.snapshot.documentEpoch,sectionState:{found:true,active:true,sectionKey}}}throw Error(`whole_page_read_forbidden:${action}`)});
 task=await f.service.reconcile({taskId:task.taskId});
 assert.equal(task.state,'running');assert.equal(application.operation.state,'verified');assert.equal(application.operation.evidence[0].evidence.activeSection,'项目经历');
}));

test('an accepted same-document section receipt survives a missing visual active marker',fixture(async f=>{
 const task=await ready(f.service,'section-receipt-marker'),application=f.service.get(task.taskId).applications[0],sectionKey='navigation:基本信息',snapshot=structuredClone(application.snapshot);
 snapshot.controls=[{ref:'basic-nav',kind:'section',label:'基本信息',active:false,safe:false,section:null,sectionKey:null,sectionCandidates:[{key:sectionKey,label:'基本信息',source:'navigation'}]}];snapshot.activeSection=null;snapshot.activeSectionKey=null;
 application.operation={id:'section-verified',state:'verified',actions:[{kind:'section',controlRef:'basic-nav',section:'基本信息',sectionKey}],index:1,evidence:[{action:{kind:'section',controlRef:'basic-nav',section:'基本信息',sectionKey},evidence:{accepted:true,activeSectionKey:sectionKey,documentEpoch:snapshot.documentEpoch}}],createdAt:new Date().toISOString()};
 const standardized=f.service.standardizeSnapshot(snapshot,application);assert.equal(standardized.activeSection,'基本信息');assert.equal(standardized.activeSectionKey,sectionKey);assert.equal(standardized.controls[0].active,true);
}));

test('late same-document section proof heals a timed-out learning transition without replay',fixture(async f=>{
 const sectionKey='navigation:基本信息';f.snapshot.controls=[{ref:'basic-nav',kind:'section',label:'基本信息',active:false,safe:false,section:null,sectionKey:null,sectionCandidates:[{key:sectionKey,label:'基本信息',source:'navigation'}]}];
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe')return structuredClone(f.snapshot);if(action==='act')return{accepted:true,kind:'section',section:'基本信息',sectionKey,activeSectionKey:sectionKey,documentEpoch:f.snapshot.documentEpoch};throw Error(action)});
 let task=await ready(f.service,'late-section-proof'),application=f.service.get(task.taskId).applications[0],actions=[{kind:'section',controlRef:'basic-nav',section:'基本信息',sectionKey}],proposal=f.service.learning_propose({taskId:task.taskId,expectedRevision:task.revision,observationId:application.observationId,question:'进入基本信息？',hypothesis:'导航会激活基本信息',expected:'基本信息成为当前区块',actions});
 await f.service.apply({taskId:task.taskId,expectedRevision:proposal.task.revision,actions});await wait();application.operation.postcondition.state='timeout';application.operation.postcondition.reason='expected_state_not_reached';
 await f.service.observe({taskId:task.taskId});assert.equal(application.operation.postcondition.state,'matched');assert.equal(application.operation.verification.status,'passed');assert.equal(application.learning.episodes.at(-1).verdict,'supported');assert.equal(f.calls.filter(call=>call.action==='act').length,1);
}));

test('reconciliation accepts a newer retained post-action observation only while the live field is detached',fixture(async f=>{
 let task=await ready(f.service,'retained-reconcile');const stored=f.service.get(task.taskId),application=stored.applications[0],createdAt=new Date(Date.now()-1000).toISOString();
 application.snapshot.fields[0].value='测试姓名';application.lastObservedAt=new Date().toISOString();application.operation={id:'unknown-fill',state:'unknown',actions:[{kind:'fill',fieldRef:'runtime-name',semanticId:'candidate.name',semanticOrdinal:0,group:'基本信息',value:'测试姓名',before:'',factId:'candidate.name'}],index:0,evidence:[],createdAt,sourceSnapshot:structuredClone(f.snapshot)};stored.state='needs_reconcile';f.service.store.write();
 f.snapshot.fields=[];f.setBrowser(async action=>{if(action==='observe')return structuredClone(f.snapshot);throw Error(action)});
 task=await f.service.reconcile({taskId:task.taskId});
 assert.equal(task.state,'running');assert.equal(application.operation.state,'verified');assert.equal(application.operation.reconciliation.priorObservationAccepted,true);
}));

test('reconciliation rejects a transient success when the live field reverted to its prior value',fixture(async f=>{
 let task=await ready(f.service,'reverted-reconcile');const stored=f.service.get(task.taskId),application=stored.applications[0],createdAt=new Date(Date.now()-1000).toISOString();
 application.snapshot.fields[0].value='测试姓名';application.lastObservedAt=new Date().toISOString();application.operation={id:'reverted-fill',state:'unknown',actions:[{kind:'fill',fieldRef:'runtime-name',semanticId:'candidate.name',semanticOrdinal:0,group:'基本信息',value:'测试姓名',before:'',factId:'candidate.name'}],index:0,evidence:[],createdAt,sourceSnapshot:structuredClone(f.snapshot)};stored.state='needs_reconcile';f.service.store.write();
 f.setBrowser(async action=>{if(action==='observe')return structuredClone(f.snapshot);throw Error(action)});
 task=await f.service.reconcile({taskId:task.taskId});
 assert.equal(task.state,'running');assert.equal(application.operation.state,'failed');assert.equal(application.operation.error,'operation_no_effect');assert.equal(application.operation.reconciliation.priorObservationAccepted,false);
}));

test('reconciliation settles a currently proven partial batch across a new document epoch',fixture(async f=>{
 let task=await ready(f.service,'cross-epoch-partial'),stored=f.service.get(task.taskId),application=stored.applications[0],source=structuredClone(application.snapshot);
 source.documentEpoch='doc-1';source.fields=[
  {ref:'field-one',label:'字段一',type:'text',value:'',required:false,requiredKnown:false,sensitive:false,manual:false},
  {ref:'field-two',label:'字段二',type:'text',value:'',required:false,requiredKnown:false,sensitive:false,manual:false},
  {ref:'field-three',label:'字段三',type:'text',value:'旧值',required:false,requiredKnown:false,sensitive:false,manual:false},
 ];
 const actions=[
  {kind:'fill',fieldRef:'field-one',value:'新值一',before:'',factId:'candidate.name'},
  {kind:'fill',fieldRef:'field-two',value:'新值二',before:'',factId:'candidate.name'},
  {kind:'fill',fieldRef:'field-three',value:'新值三',before:'旧值',factId:'candidate.name',replaceNativeParsed:true},
 ];
 application.snapshot=structuredClone(source);application.operation={id:'partial-new-epoch',state:'unknown',error:'page_acceptance_unknown',actions,index:2,evidence:actions.slice(0,2).map(action=>({action,evidence:{accepted:true,kind:'fill',fieldRef:action.fieldRef,value:action.value,documentEpoch:'doc-1'}})),createdAt:new Date(Date.now()-1000).toISOString(),sourceSnapshot:structuredClone(source)};stored.state='needs_attention';
 const currentFields=source.fields.map((field,index)=>({...field,value:index===0?'新值一':index===1?'新值二':'旧值'}));Object.assign(f.snapshot,{...structuredClone(source),documentEpoch:'doc-2',fields:structuredClone(currentFields),rawFields:structuredClone(currentFields)});f.service.store.write();
 task=await f.service.reconcile({taskId:task.taskId});
 assert.equal(task.state,'running',JSON.stringify({task,operation:application.operation,snapshot:application.snapshot}));assert.equal(application.operation.state,'failed');assert.equal(application.operation.index,2);assert.equal(application.operation.error,'partial_operation_settled');assert.equal(application.operation.reconciliation.documentChanged,true);assert.equal(application.operation.reconciliation.attemptedVerified,false);
}));

test('reconciliation preserves a proven pre-dispatch select failure across a new document epoch',fixture(async f=>{
 let task=await ready(f.service,'cross-epoch-option-missing'),stored=f.service.get(task.taskId),application=stored.applications[0],source=structuredClone(application.snapshot);
 source.documentEpoch='doc-1';source.fields=[{ref:'runtime-school',label:'学校名称',semanticId:'education_1_school',group:'教育经历',value:'',type:'select'}];
 const diagnostic='option_not_found:{"phase":"before_click","desired":"西安电子科技大学"}',action={kind:'select',fieldRef:'runtime-school',semanticId:'education_1_school',group:'教育经历',value:'西安电子科技大学',before:'',factId:'education_1_school'};
 application.snapshot=structuredClone(source);application.operation={id:'option-missing-new-epoch',state:'unknown',error:diagnostic,actions:[action],index:0,evidence:[],createdAt:new Date(Date.now()-1000).toISOString(),sourceSnapshot:structuredClone(source),postcondition:{kind:'learning',state:'unknown',transitionId:'learning-option-missing',sourceState:'source',expectedStates:[],expectedOutcome:'verified_effect'}};stored.state='needs_reconcile';
 const current={...source.fields[0],value:'页面中的其他值'};Object.assign(f.snapshot,{...structuredClone(source),documentEpoch:'doc-2',fields:[current],rawFields:[structuredClone(current)]});f.service.store.write();
 task=await f.service.reconcile({taskId:task.taskId});
 assert.equal(task.state,'running');assert.equal(application.operation.state,'failed');assert.equal(application.operation.error,diagnostic);assert.equal(application.operation.reconciliation.documentChanged,true);assert.equal(application.operation.reconciliation.noEffect,true);assert.equal(f.service.operationSettled(application.operation),true);
}));

test('reconciliation rejects a settled operation before reading a cleared snapshot',fixture(async f=>{
 let task=await ready(f.service,'settled-reconcile-guard'),application=f.service.get(task.taskId).applications[0];application.operation={id:'settled',state:'failed',actions:[],index:0,evidence:[],createdAt:new Date().toISOString()};application.snapshot=null;f.calls.length=0;
 await assert.rejects(f.service.reconcile({taskId:task.taskId}),/reconciliation_not_required/);assert.equal(f.calls.length,0);
}));

test('reconciliation of an unknown operation tolerates a missing current snapshot',fixture(async f=>{
 let task=await ready(f.service,'snapshotless-reconcile'),stored=f.service.get(task.taskId),application=stored.applications[0];application.operation={id:'unknown',state:'unknown',actions:[{kind:'fill',fieldRef:'runtime-name',semanticId:'candidate.name',semanticOrdinal:0,group:'基本信息',value:'测试姓名',before:'',factId:'candidate.name'}],index:0,evidence:[],createdAt:new Date(Date.now()-1000).toISOString(),sourceSnapshot:structuredClone(application.snapshot)};application.snapshot=null;stored.state='needs_reconcile';f.snapshot.fields[0].value='测试姓名';
 task=await f.service.reconcile({taskId:task.taskId});assert.equal(task.state,'running');assert.equal(application.operation.state,'verified');assert.equal(application.operation.reconciliation.documentChanged,false);
}));

test('reconciliation standardizes raw browser fields and settles a proven timeout with no effect',fixture(async f=>{
 f.snapshot.fields[0].semanticId=null;f.snapshot.rawFields=structuredClone(f.snapshot.fields);
 let task=await ready(f.service,'standardized-reconcile'),application=f.service.get(task.taskId).applications[0];
 const interpreted=f.service.learning_interpret({taskId:task.taskId,expectedRevision:task.revision,observationId:application.observationId,mappings:[{kind:'field',ref:'runtime-name',semanticId:'candidate.name'}]});
 application.operation={id:'unknown-raw-fill',state:'unknown',actions:[{kind:'fill',fieldRef:'runtime-name',semanticId:'candidate.name',semanticOrdinal:0,group:null,value:'测试姓名',before:'',factId:'candidate.name'}],index:0,evidence:[],createdAt:new Date(Date.now()-1000).toISOString(),sourceSnapshot:structuredClone(application.snapshot)};application.lastObservedAt=new Date().toISOString();f.service.get(task.taskId).state='needs_reconcile';
 task=await f.service.reconcile({taskId:task.taskId});
 assert.equal(task.state,'running');assert.equal(application.operation.state,'failed');assert.equal(application.operation.error,'operation_no_effect');assert.equal(application.operation.reconciliation.noEffect,true);assert.equal(interpreted.snapshot.fields[0].semanticId,'candidate.name');
}));

test('a fieldless apply step may open its safe resume editor without false verification',fixture(async f=>{
 f.snapshot.formStage='apply';f.snapshot.fields=[];f.snapshot.controls=[{ref:'edit-resume',label:'编辑',kind:'start',intent:'resume',safe:true,role:'control',tag:'span',type:''}];
 let task=await ready(f.service,'resume-editor-start');
 const result=await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'start',controlRef:'edit-resume'}]});
 const action=f.service.get(task.taskId).applications[0].operation.actions[0];
 assert.equal(result.applications[0].operation.total,1);assert.equal(action.startIntent,'resume');
 assert.equal(f.service.actionVerified(structuredClone(f.snapshot),action),false);
 const changed={...structuredClone(f.snapshot),documentEpoch:'doc-resume-editor',fields:[{ref:'name',label:'姓名',semanticId:'candidate.name',type:'text',value:''}]};
 assert.equal(f.service.actionVerified(changed,action),true);
}));

test('a uniquely observed legal consent dialog waits only when it blocks resume entry',fixture(async f=>{
 f.snapshot.formStage='unknown';f.snapshot.pageState='unknown';f.snapshot.fields=[];f.snapshot.controls=[];
 f.snapshot.activeDialog={ref:'dialog-1',role:'dialog',title:'',text:'我已阅读并同意'};
 f.snapshot.interactions=[{ref:'consent-1',label:'我已阅读并同意',role:'button',tag:'button',type:'button',surface:'dialog',safe:true,formSubmit:false}];
 const task=await ready(f.service,'consent-gate'),observed=await f.service.observe({taskId:task.taskId});
 assert.equal(observed.userGate.reason,'consent_gate');assert.equal(observed.userGate.controlRef,'consent-1');
 assert.equal(observed.task.state,'waiting_user');assert.equal(observed.task.question.reason,'consent_gate');
 await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:observed.task.revision,actions:[{kind:'activate',controlRef:'consent-1',activationMode:'isolated_pointer',expectedPostcondition:'dialog_closed'}]}),/not_ready_for_actions/);
}));

test('final-page legal consent is reported as manual and does not create a waiting question',fixture(async f=>{
 f.snapshot.fields.push({ref:'privacy',label:'我已阅读并同意隐私政策',semanticId:null,group:'确认与提交',value:'',checked:false,type:'checkbox',manual:true,required:true,requiredKnown:true});
 const task=await ready(f.service,'final-consent'),observed=await f.service.observe({taskId:task.taskId});
 assert.equal(observed.userGate,null);assert.equal(observed.task.state,'running');assert.equal(observed.task.question,null);
 assert.deepEqual(observed.completion.manualFinalSteps.map(item=>item.ref),['privacy']);
}));

test('page-specific required preferences can pause once when they block the resume workflow',fixture(async f=>{
 f.snapshot.formStage='apply';f.snapshot.pageState='normal';f.snapshot.canAdvance=false;
 f.snapshot.fields=[{ref:'referral',label:'内推码',semanticId:null,group:null,value:'',type:'text',required:false,requiredKnown:false}];
 f.snapshot.controls=[{ref:'next-1',label:'下一步',kind:'next',safe:true,active:false}];
 f.snapshot.errors=['意向工作城市一必填','面试城市必填','投递意愿必填'];
 const task=await ready(f.service,'manual-prerequisite');
 const waiting=f.service.question({taskId:task.taskId,expectedRevision:task.revision,reason:'manual_prerequisite',text:'请完成当前页的投递偏好并点击下一步，完成后回复“已完成”。',missing:['意向工作城市一','面试城市','投递意愿'],fieldRefs:[]});
 assert.equal(waiting.state,'waiting_user');assert.equal(waiting.question.reason,'manual_prerequisite');
}));

test('a unique informational acknowledgement after application entry is service-approved',fixture(async f=>{
 f.snapshot.formStage='unknown';f.snapshot.pageState='unknown';f.snapshot.fields=[];f.snapshot.controls=[];
 f.snapshot.activeDialog={ref:'dialog-1',role:'dialog',title:'',text:'投递意愿有上限，请谨慎选择后投递 我知道了'};
 f.snapshot.interactions=[{ref:'close-1',label:'Close',role:'button',tag:'button',type:'button',surface:'dialog',safe:true,formSubmit:false},{ref:'ack-1',label:'我知道了',role:'button',tag:'button',type:'button',surface:'dialog',safe:true,formSubmit:false}];
 const task=create(f.service,'workflow-acknowledgement'),stored=f.service.get(task.taskId),application=stored.applications[0];
 application.operation={id:'start-op',state:'verified',actions:[{kind:'start'}],index:1,evidence:[{action:{kind:'start'},evidence:{accepted:true}}],createdAt:new Date().toISOString()};
 stored.state='running';stored.revision++;f.service.store.write();
 const observed=await f.service.observe({taskId:task.taskId});
 assert.equal(observed.interaction.status,'bootstrap');assert.equal(observed.interaction.suggestedAction.controlRef,'ack-1');assert.equal(observed.interaction.suggestedAction.purpose,'workflow_acknowledgement');assert.equal(observed.interaction.suggestedAction.activationMode,'main_world_pointer');assert.equal(observed.interaction.suggestedAction.expectedPostcondition,'dialog_closed');
}));

test('a destructive or legal dialog is never treated as an informational acknowledgement',fixture(async f=>{
 const task=create(f.service,'protected-workflow-dialog'),stored=f.service.get(task.taskId),application=stored.applications[0];
 application.operation={id:'start-op',state:'verified',actions:[{kind:'start'}],index:1,evidence:[{action:{kind:'start'},evidence:{accepted:true}}],createdAt:new Date().toISOString()};
 stored.state='running';
 f.snapshot.formStage='unknown';f.snapshot.pageState='unknown';f.snapshot.fields=[];f.snapshot.controls=[];f.snapshot.activeDialog={ref:'dialog-1',role:'dialog',title:'',text:'注意：确认后将提交申请'};f.snapshot.interactions=[{ref:'confirm-1',label:'确认',role:'button',tag:'button',type:'button',surface:'dialog',safe:true,formSubmit:false}];f.service.store.write();
 const observed=await f.service.observe({taskId:task.taskId});assert.equal(observed.interaction,null);
}));

test('a resume prerequisite confirmation is never treated as legal consent',fixture(async f=>{
 f.snapshot.formStage='detail';f.snapshot.pageState='resume_prerequisite';f.snapshot.fields=[];f.snapshot.controls=[{ref:'edit-1',label:'编辑',kind:'start',intent:'resume',safe:true}];
 f.snapshot.activeDialog={ref:'dialog-1',role:'dialog',title:'简历完整度 55%',text:'当前职位投递要求：中文简历完整度达到100%。 编辑 确认提交'};
 f.snapshot.interactions=[{ref:'submit-1',label:'确认提交',role:'button',tag:'button',type:'button',surface:'dialog',safe:true,formSubmit:null}];
 const task=await ready(f.service,'resume-prerequisite-not-consent'),observed=await f.service.observe({taskId:task.taskId});
 assert.equal(observed.userGate,null);assert.equal(observed.task.state,'running');assert.equal(observed.task.question,null);
}));

test('service restart invalidates a stale false consent gate',fixture(async f=>{
 f.snapshot.formStage='detail';f.snapshot.pageState='resume_prerequisite';f.snapshot.fields=[];f.snapshot.activeDialog={ref:'dialog-1',role:'dialog',title:'简历完整度 55%',text:'编辑 确认提交'};f.snapshot.interactions=[{ref:'submit-1',label:'确认提交',surface:'dialog',safe:true,formSubmit:null}];
 const task=create(f.service,'stale-consent');const stored=f.service.get(task.taskId);stored.state='waiting_user';stored.question={id:'stale',revision:1,status:'open',reason:'consent_gate',text:'错误等待',missing:[],fieldRefs:[],scope:stored.applications[0].applicationId};stored.applications[0].snapshot=structuredClone(f.snapshot);f.service.store.write();f.restart();
 const recovered=f.service.status({taskId:task.taskId});assert.equal(recovered.state,'queued');assert.equal(recovered.question,null);
}));

test('add controls require one explicit repeatable resume section',fixture(async f=>{
 f.snapshot.controls=[{ref:'add-personal',label:'添加',kind:'add',section:'个人信息',sectionKey:'heading:个人信息',group:null,safe:true}];
 let task=await ready(f.service,'unsafe-add-section');
 await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'add',controlRef:'add-personal',section:'个人信息',sectionKey:'heading:个人信息'}]}),/add_section_not_unique/);

 f.snapshot.controls=[{ref:'add-internship-a',label:'添加',kind:'add',section:'实习经历',sectionKey:'heading:实习经历',group:null,safe:true},{ref:'add-internship-b',label:'添加',kind:'add',section:'实习经历',sectionKey:'heading:实习经历',group:null,safe:true}];
 task=(await f.service.observe({taskId:task.taskId})).task;
 await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'add',controlRef:'add-internship-a',section:'实习经历',sectionKey:'heading:实习经历'}]}),/add_section_not_unique/);

 f.snapshot.controls=[{ref:'add-project-a',label:'添加',kind:'add',section:'项目经历',sectionKey:'heading:项目经验',group:null,safe:true},{ref:'add-project-b',label:'添加',kind:'add',section:'项目经历',sectionKey:'heading:是否有项目经验',group:null,safe:true}];
 task=(await f.service.observe({taskId:task.taskId})).task;
 await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'add',controlRef:'add-project-a',section:'项目经历',sectionKey:'heading:项目经验'}]}),/add_section_not_unique/);

 f.snapshot.controls=[{ref:'add-internship',label:'添加',kind:'add',section:'实习经历',sectionKey:'heading:实习经历',group:null,safe:true}];
 task=(await f.service.observe({taskId:task.taskId})).task;
 await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'add',controlRef:'add-internship'}]}),/action_invalid:0:section_identity_required/);
 const accepted=await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'add',controlRef:'add-internship',section:'实习经历',sectionKey:'heading:实习经历'}]});
 assert.equal(accepted.applications[0].operation.total,1);
 assert.equal(f.service.get(task.taskId).applications[0].operation.actions[0].section,'实习经历');
 assert.equal(f.service.get(task.taskId).applications[0].operation.actions[0].sectionKey,'heading:实习经历');
}));

test('a cold add learns a page model mapping and reuses it without extension code',fixture(async f=>{
 f.snapshot.controls=[{ref:'add-education',label:'添加',kind:'add',section:null,sectionKey:null,sectionCandidates:[{key:'heading:教育背景',label:'教育背景',source:'heading'}],group:null,safe:true}];
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe')return structuredClone(f.snapshot);if(action==='act'){assert.equal(args.actionSpec.sectionKey,'heading:教育背景');f.snapshot.fields.push({ref:'school',label:'学校',semanticId:'education.school',group:'education-1',value:'',type:'text'});return{accepted:true}}if(action==='stop')return{stopped:true};throw Error(`unexpected_browser_${action}`)});
 let task=await ready(f.service,'cold-page-model');const before=f.service.get(task.taskId).applications[0].snapshot.controls[0];assert.equal(before.safe,false);
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'add',controlRef:'add-education',section:'教育经历',sectionKey:'heading:教育背景'}]});await wait();
 task=(await f.service.observe({taskId:task.taskId})).task;
 const recipe=Object.values(f.service.store.data.recipes)[0];assert.equal(recipe.pageModel.mappings[publicCandidateKey('heading:教育背景')],'教育经历');
 const mapped=f.service.get(task.taskId).applications[0].snapshot.controls[0];assert.deepEqual([mapped.section,mapped.sectionKey,mapped.safe],['教育经历','heading:教育背景',true]);
}));

test('stopping closes the dispatch gate and an old run cannot write',fixture(async f=>{
 const task=await ready(f.service);await f.service.control({taskId:task.taskId,kind:'stop'});
 assert.equal(f.service.status({taskId:task.taskId}).state,'cancelled');await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:f.service.status({taskId:task.taskId}).revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]}),/task_terminal|not_ready_for_actions/);
}));

test('resuming clears a persisted pause gate before the agent continues',fixture(async f=>{
 let task=await ready(f.service,'resume-clears-control');
 task=await f.service.control({taskId:task.taskId,kind:'pause'});
 assert.equal(task.state,'paused');assert.equal(task.control.kind,'pause');assert.equal(task.control.browser,'confirmed');
 task=f.service.resume({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(task.state,'queued');assert.equal(Object.hasOwn(task,'control'),false);
}));

test('a paused task with a settled operation can resume after its browser tab disappeared',fixture(async f=>{
 let task=await ready(f.service,'resume-missing-settled-tab');
 const application=f.service.get(task.taskId).applications[0];
 application.operation={id:'settled-failure',state:'failed',actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}],index:1,evidence:[],error:'partial_operation_settled'};
 application.operations=[application.operation];
 task=await f.service.control({taskId:task.taskId,kind:'pause'});
 const live=f.service.get(task.taskId);
 live.control.browser='unconfirmed';
 live.control.detail='No tab with id: 1.';
 const resumed=f.service.resume({taskId:task.taskId,expectedRevision:live.revision});
 assert.equal(resumed.state,'queued');
 assert.equal(resumed.control,undefined);
 assert.equal(f.service.get(task.taskId).applications[0].snapshot,null);
}));

test('loading recovery can yield while the previous operation is settled failed',fixture(async f=>{
 let task=await ready(f.service,'yield-after-settled-failure');
 const application=f.service.get(task.taskId).applications[0];
 application.operation={id:'settled-failure',state:'failed',actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}],index:1,evidence:[],error:'partial_operation_settled'};
 application.operations=[application.operation];
 const yielded=f.service.yield_task({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(yielded.task.state,'queued');
 assert.equal(typeof yielded.commandId,'string');
 assert.equal(yielded.commandId.length>0,true);
}));

test('service restart keeps learned recipes',fixture(async f=>{
 await learnOneField(f);f.restart();
 assert.equal(Object.keys(f.service.store.data.recipes).length,1);
}));

test('service restart recompiles an exact persisted verified candidate after a compiler fix',fixture(async f=>{
 const taskId=await learnOneField(f),application=f.service.get(taskId).applications[0],candidate=Object.values(application.learning.candidates).find(item=>item.operationId===application.operation.id),system=systemFor(application.snapshot).key;
 assert.equal(candidate.status,'compiled');
 f.service.store.data.recipes[system].transitions={};delete application.operation.recipeRecorded;candidate.status='verified';candidate.rebound=false;f.service.store.write();
 f.restart();
 const restored=f.service.get(taskId).applications[0],compiled=Object.values(restored.learning.candidates).find(item=>item.operationId===restored.operation.id);
 assert.equal(compiled.status,'compiled');assert.equal(compiled.rebound,true);assert.equal(restored.operation.recipeRecorded,true);assert.equal(Object.keys(f.service.store.data.recipes[system].transitions).length,1);
}));

test('one saved resume remains an isolated fact aggregate',fixture(async f=>{
 const a=f.service.resume_save({saveRequestId:'a',name:'数据版',facts:[{id:'candidate.name',value:'甲',sourceRef:'synthetic:a'}],attachment:{resource:'test'}}),b=f.service.resume_save({saveRequestId:'b',name:'产品版',facts:[{id:'candidate.name',value:'乙',sourceRef:'synthetic:b'}],attachment:{resource:'test'}});
 const ta=f.service.create({clientRequestId:'ta',targets:[{url:f.snapshot.url,tabId:1}],resume:{id:a.resumeId,revision:1}}),tb=f.service.create({clientRequestId:'tb',targets:[{url:f.snapshot.url,tabId:2}],resume:{id:b.resumeId,revision:1}});
 assert.equal(f.service.task_context({taskId:ta.taskId}).facts[0].value,'甲');assert.equal(f.service.task_context({taskId:tb.taskId}).facts[0].value,'乙');assert.equal(JSON.stringify(f.service.resume_list()).includes('甲'),false);
}));

test('a verified action is learned during observation before task finish',fixture(async f=>{
 let task=await ready(f.service);await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});await wait();await f.service.observe({taskId:task.taskId});
 const transition=Object.values(Object.values(f.service.store.data.recipes)[0].transitions)[0];assert.equal(transition.status,'reusable');assert.equal(transition.successCount,1);
}));

test('cold learning requires a hypothesis and ApplicationPlan completion ignores unrelated website coverage',fixture(async f=>{
 f.snapshot.evidence={schemaVersion:1,complete:true,nodes:[],layers:[],interactionDigest:'form'};
 let task=await ready(f.service,'v3-learn');const actions=[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}];
 await assert.rejects(f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions}),/learning_hypothesis_required/);
 const a=f.service.get(task.taskId).applications[0];
 const proposed=f.service.learning_propose({taskId:task.taskId,expectedRevision:task.revision,observationId:a.observationId,question:'姓名能否填写？',hypothesis:'文本控件保留姓名',expected:'姓名与所选事实一致',actions});
 await f.service.apply({taskId:task.taskId,expectedRevision:proposed.task.revision,actions});await wait();
 task=(await f.service.observe({taskId:task.taskId})).task;
 assert.equal(a.learning.episodes[0].verdict,'supported');
 assert.equal(Object.values(a.learning.candidates)[0].status,'compiled');
 f.snapshot.fields.push({ref:'unplanned',label:'备注',type:'text',value:''});
 task=(await f.service.observe({taskId:task.taskId})).task;
 const done=f.service.finish({taskId:task.taskId,expectedRevision:task.revision,outcome:'partial'});
 assert.equal(done.state,'finished');f.restart();
 assert.equal(f.service.get(task.taskId).applications[0].learning.episodes[0].verdict,'supported');
}));

test('a known multi-step recipe stays on its execution path through an intermediate page state',fixture(async f=>{
 f.service.recipeTransitionTimeoutMs=500;
 f.snapshot.fields.push({ref:'runtime-email',label:'电子邮箱',semanticId:'candidate.email',group:'基本信息',value:'',type:'text'});
 const source=structuredClone(f.snapshot),middle=structuredClone(source),target=structuredClone(source);
 middle.fields[0].value='测试姓名';target.fields[0].value='测试姓名';target.fields[1].value='test@example.com';
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:source,targetSnapshot:middle,runId:'multi-step-name',actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:middle,targetSnapshot:target,runId:'multi-step-email',predecessorKind:'fill',actions:[{kind:'fill',fieldRef:'runtime-email',factId:'candidate.email'}]});
 markRecipeTerminal(f.service.store.data.recipes,target,{predecessorKind:'fill',coverageFactIds:['candidate.name','candidate.email']});f.service.store.write();
 let firstDispatched=false,transientReads=0;
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe'){if(firstDispatched&&!f.snapshot.fields[1].value&&f.snapshot.controls.length){transientReads++;if(transientReads>=3)f.snapshot.controls=[]}return structuredClone(f.snapshot)}if(action==='act'){const field=f.snapshot.fields.find(item=>item.ref===args.actionSpec.fieldRef);field.value=args.actionSpec.value;if(args.actionSpec.factId==='candidate.name'){firstDispatched=true;f.snapshot.controls=[{ref:'saving',kind:'next',label:'保存中',safe:true,active:true}]}return{accepted:true}}if(action==='stop')return{stopped:true};throw Error(action)});
 const task=create(f.service,'multi-step-replay');f.service.get(task.taskId).factSnapshot.facts.push({id:'candidate.email',value:'test@example.com',sourceRef:'synthetic:test'});
 const result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision}),recipe=Object.values(f.service.store.data.recipes)[0];
 const dispatched=f.calls.filter(item=>item.action==='act');assert.equal(result.path,'recipe',JSON.stringify(result));assert.equal(result.task.state,'finished');assert.equal(result.actionCount,2,JSON.stringify({result,acts:dispatched.map(item=>item.args.actionSpec),transientReads,fields:f.snapshot.fields}));assert.equal(dispatched.length,2);assert.ok(transientReads>=3);
 assert.equal(Object.values(recipe.transitions).length,2);assert.equal(f.snapshot.fields[0].value,'测试姓名');assert.equal(f.snapshot.fields[1].value,'test@example.com');
}));

test('a recipe transition owns transient observations immediately after a slow browser receipt',fixture(async f=>{
 f.service.recipeTransitionTimeoutMs=500;
 const detail={...structuredClone(f.snapshot),url:'https://jobs.example.test/position/123456/detail',formStage:'detail',fields:[],applicationFieldCount:0,controls:[{ref:'apply-now',kind:'start',intent:'application',label:'立即投递',role:'button',tag:'button',type:'button',safe:true}],interactions:[]};
 const form={...structuredClone(f.snapshot),url:'https://jobs.example.test/position/123456/apply',documentEpoch:'doc-2'},filled=structuredClone(form);filled.fields[0].value='测试姓名';
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:detail,targetSnapshot:form,runId:'slow-entry',actions:[{kind:'start',controlRef:'apply-now'}]});
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:form,targetSnapshot:filled,runId:'fill-after-entry',predecessorKind:'start',actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});
 markRecipeTerminal(f.service.store.data.recipes,filled,{predecessorKind:'fill',coverageFactIds:['candidate.name']});f.service.store.write();
 Object.assign(f.snapshot,structuredClone(detail));let entryAccepted=false,formReached=false,transientReads=0,writes=0,dispatchCursor=null;
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe'){if(entryAccepted&&!formReached){transientReads++;if(transientReads>=3){Object.assign(f.snapshot,structuredClone(form));formReached=true}}return structuredClone(f.snapshot)}if(action==='act'){writes++;if(args.actionSpec.kind==='start'){entryAccepted=true;const operation=f.service.get(args.taskId).applications[0].operation;dispatchCursor=structuredClone(operation.postcondition);operation.createdAt=new Date(Date.now()-5_000).toISOString()}else f.snapshot.fields[0].value=args.actionSpec.value;return{accepted:true}}if(action==='stop')return{stopped:true};throw Error(action)});
 const task=create(f.service,'slow-receipt-transition',detail.url),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'recipe',JSON.stringify(result));assert.equal(result.task.state,'finished');assert.equal(result.actionCount,2);assert.equal(writes,2);assert.ok(transientReads>=3);
 assert.equal(dispatchCursor.state,'dispatching');assert.equal(dispatchCursor.sourceState,stateFor(detail).key);assert.equal(dispatchCursor.expectedStates.length,1);
 assert.equal(f.service.get(task.taskId).applications[0].operation.error,undefined);
}));

test('a learned application entry accepts and records a new legitimate result branch without waiting or replaying',fixture(async f=>{
 f.service.recipeTransitionTimeoutMs=500;
 const detail={...structuredClone(f.snapshot),url:'https://jobs.example.test/position/123456/detail',formStage:'detail',pageState:'normal',fields:[],applicationFieldCount:0,controls:[{ref:'covered',kind:'start',intent:'application',label:'立即投递',role:'button',tag:'button',type:'button',safe:true},{ref:'apply-now',kind:'start',intent:'application',label:'立即投递',role:'button',tag:'button',type:'button',safe:false}],interactions:[{ref:'apply-now',label:'立即投递',safe:true,role:'button',tag:'button',type:'button',surface:'page'}],evidence:{schemaVersion:1,complete:true,nodes:[{ref:'covered',hit:false,disabled:false},{ref:'apply-now',hit:true,disabled:false}],layers:[],interactionDigest:'detail'}};
 const login={...structuredClone(detail),documentEpoch:'login-doc',formStage:'apply',pageState:'login',loginRequired:true,controls:[],interactions:[],evidence:{schemaVersion:1,complete:true,nodes:[],layers:[],interactionDigest:'login'}};
 const prerequisite={...structuredClone(detail),documentEpoch:'resume-doc',pageState:'resume_prerequisite',activeDialog:{ref:'resume-dialog',role:'dialog',title:'申请职位',text:'请编辑简历'},controls:[{ref:'edit-resume',kind:'start',intent:'resume',label:'编辑',role:'button',tag:'button',type:'button',safe:true}],interactions:[{ref:'edit-resume',label:'编辑',safe:true,role:'button',tag:'button',type:'button',surface:'dialog'}],evidence:{schemaVersion:1,complete:true,nodes:[{ref:'edit-resume',hit:true,disabled:false}],layers:[{ref:'resume-dialog',blocking:true,controls:['edit-resume']}],interactionDigest:'resume'}};
 const filled={...structuredClone(f.snapshot),url:detail.url,documentEpoch:'form-doc',formStage:'apply',pageState:'normal',activeDialog:null,controls:[],interactions:[],evidence:{schemaVersion:1,complete:true,nodes:[{ref:'runtime-name',hit:true,disabled:false}],layers:[],interactionDigest:'form'}};filled.fields[0].value='测试姓名';
 const entryAction={kind:'activate',controlRef:'apply-now',activationMode:'main_world_pointer',expectedPostcondition:'surface_changed',purpose:'application_start'};
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:detail,targetSnapshot:login,runId:'known-login-result',actions:[entryAction]});
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:prerequisite,targetSnapshot:filled,runId:'known-resume-editor',predecessorKind:'activate',actions:[{kind:'start',controlRef:'edit-resume'}]});
 markRecipeTerminal(f.service.store.data.recipes,filled,{predecessorKind:'start',coverageFactIds:['candidate.name']});f.service.store.write();Object.assign(f.snapshot,structuredClone(detail));
 let entryWrites=0,editorWrites=0;
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe')return structuredClone(f.snapshot);if(action==='act'){if(args.actionSpec.kind==='activate'){entryWrites++;Object.assign(f.snapshot,structuredClone(prerequisite))}else if(args.actionSpec.kind==='start'){editorWrites++;Object.assign(f.snapshot,structuredClone(filled))}return{accepted:true}}if(action==='stop')return{stopped:true};throw Error(action)});
 const task=create(f.service,'new-application-result-branch',detail.url),started=Date.now(),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision}),recipe=Object.values(f.service.store.data.recipes)[0],source=stateFor(detail).key,entryEdges=Object.values(recipe.transitions).filter(edge=>edge.from===source&&edge.actions[0]?.purpose==='application_start');
 assert.equal(result.path,'recipe',JSON.stringify(result));assert.equal(result.task.state,'finished');assert.equal(result.actionCount,2);assert.equal(entryWrites,1);assert.equal(editorWrites,1);assert.ok(Date.now()-started<500);
 assert.equal(entryEdges.length,2);assert.deepEqual(new Set(entryEdges.map(edge=>edge.to)),new Set([stateFor(login,{predecessorKind:'activate'}).key,stateFor(prerequisite,{predecessorKind:'activate'}).key]));
}));

test('Recipe replay does not accept an unexplained dialog as a legitimate application-entry result',fixture(async f=>{
 const unexplained={...structuredClone(f.snapshot),formStage:'detail',pageState:'unknown',fields:[],controls:[],activeDialog:{ref:'dialog',role:'dialog',title:'提示',text:'系统繁忙，请稍后再试'}};
 assert.equal(f.service.applicationEntryOutcome(unexplained),true);
 assert.equal(f.service.recipeApplicationEntryOutcome(unexplained),false);
}));

test('a Recipe transition performs one controlled loading refresh and continues to its next edge',fixture(async f=>{
 f.service.recipeTransitionTimeoutMs=40;f.service.recipeTransitionIntervalMs=5;f.service.loadingRecoveryWindowMs=20;
 const detail={...structuredClone(f.snapshot),url:'https://jobs.example.test/position/123456/detail',formStage:'detail',fields:[],applicationFieldCount:0,controls:[{ref:'apply-now',kind:'start',intent:'application',label:'立即投递',role:'button',tag:'button',type:'button',safe:true}],interactions:[]};
 const loading={...structuredClone(f.snapshot),url:'https://jobs.example.test/position/123456/apply',formStage:'apply',pageState:'unknown',fields:[],controls:[],activeDialog:{ref:'loader',role:'dialog',title:'',text:''},interactions:[{ref:'spinner',label:'animation',role:'button',tag:'div',type:'',surface:'dialog',safe:true}],evidence:{schemaVersion:1,complete:true,layers:[{ref:'loader',blocking:true,controls:['spinner']}],nodes:[],interactionDigest:'loading'}};
 const form={...structuredClone(f.snapshot),url:loading.url,documentEpoch:'doc-2',activeDialog:null,interactions:[],evidence:{schemaVersion:1,complete:true,layers:[],nodes:[{ref:'runtime-name',tag:'input'}],interactionDigest:'form'}},filled=structuredClone(form);filled.fields[0].value='测试姓名';
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:detail,targetSnapshot:form,runId:'refresh-entry',actions:[{kind:'start',controlRef:'apply-now'}]});
 recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:form,targetSnapshot:filled,runId:'refresh-fill',predecessorKind:'start',actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});
 markRecipeTerminal(f.service.store.data.recipes,filled,{predecessorKind:'fill',coverageFactIds:['candidate.name']});f.service.store.write();Object.assign(f.snapshot,structuredClone(detail));
 let writes=0,refreshes=0;
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe')return structuredClone(f.snapshot);if(action==='act'){writes++;if(args.actionSpec.kind==='start')Object.assign(f.snapshot,structuredClone(loading));else f.snapshot.fields[0].value=args.actionSpec.value;return{accepted:true}}if(action==='refresh'){refreshes++;Object.assign(f.snapshot,structuredClone(form));return{accepted:true,refreshed:true,tabId:args.tabId,url:args.targetURL}}if(action==='stop')return{stopped:true};throw Error(action)});
 const task=create(f.service,'transition-loading-refresh',detail.url),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 const internal=f.service.get(task.taskId).applications[0];assert.equal(refreshes,1,JSON.stringify({recovery:f.service.loadingRecovery(internal.snapshot,internal),loadingSurface:internal.loadingSurface,postcondition:internal.operation.postcondition}));
 assert.equal(result.path,'recipe',JSON.stringify({result,writes,refreshes,loadingSurface:f.service.get(task.taskId).applications[0].loadingSurface,refreshCount:f.service.get(task.taskId).applications[0].refreshCount}));assert.equal(result.task.state,'finished');assert.equal(result.actionCount,3);assert.equal(writes,2);assert.equal(refreshes,1);assert.equal(result.task.applications[0].performance.agentTakeover,false);assert.equal(f.snapshot.fields[0].value,'测试姓名');
}));

test('a Recipe transition ends a persistently loading page after one refresh without Agent takeover',fixture(async f=>{
 f.service.recipeTransitionTimeoutMs=40;f.service.recipeTransitionIntervalMs=5;f.service.loadingRecoveryWindowMs=20;
 const detail={...structuredClone(f.snapshot),url:'https://jobs.example.test/position/123456/detail',formStage:'detail',fields:[],applicationFieldCount:0,controls:[{ref:'apply-now',kind:'start',intent:'application',label:'立即投递',role:'button',tag:'button',type:'button',safe:true}],interactions:[]};
 const loading={...structuredClone(f.snapshot),url:'https://jobs.example.test/position/123456/apply',formStage:'apply',pageState:'unknown',fields:[],controls:[],activeDialog:{ref:'loader',role:'dialog',title:'',text:''},interactions:[{ref:'spinner',label:'animation',role:'button',tag:'div',type:'',surface:'dialog',safe:true}],evidence:{schemaVersion:1,complete:true,layers:[{ref:'loader',blocking:true,controls:['spinner']}],nodes:[],interactionDigest:'loading'}};
 const form={...structuredClone(f.snapshot),url:loading.url,documentEpoch:'doc-2'};recordVerifiedTransition(f.service.store.data.recipes,{sourceSnapshot:detail,targetSnapshot:form,runId:'failed-refresh-entry',actions:[{kind:'start',controlRef:'apply-now'}]});f.service.store.write();Object.assign(f.snapshot,structuredClone(detail));let writes=0,refreshes=0;
 f.setBrowser(async(action,args)=>{if(action==='observe')return structuredClone(f.snapshot);if(action==='act'){writes++;Object.assign(f.snapshot,structuredClone(loading));return{accepted:true}}if(action==='refresh'){refreshes++;return{accepted:true,refreshed:true,tabId:args.tabId,url:args.targetURL}}if(action==='progress')return{accepted:true};if(action==='stop')return{stopped:true};throw Error(action)});
 const task=create(f.service,'transition-loading-exhausted',detail.url),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'unavailable',JSON.stringify({result,writes,refreshes,loadingSurface:f.service.get(task.taskId).applications[0].loadingSurface,refreshCount:f.service.get(task.taskId).applications[0].refreshCount}));assert.equal(result.task.state,'finished');assert.equal(result.outcome,'skipped');assert.equal(result.actionCount,2);assert.equal(writes,1);assert.equal(refreshes,1);assert.equal(result.task.applications[0].performance.agentTakeover,false);assert.deepEqual(result.task.applications[0].unsupported,['application_loading_failed']);
}));

test('a verified recipe transition resumes after service restart without redispatch',fixture(async f=>{
 await learnOneField(f);f.service.recipeTransitionTimeoutMs=500;f.snapshot.fields[0].value='';f.snapshot.controls=[];let writes=0,reads=0;
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe'){if(writes&&f.snapshot.controls.length&&++reads>=3){f.snapshot.controls=[];f.snapshot.documentEpoch='doc-2'}return structuredClone(f.snapshot)}if(action==='act'){writes++;f.snapshot.fields[0].value=args.actionSpec.value;f.snapshot.controls=[{ref:'saving',kind:'next',label:'保存中',safe:true,active:true}];return{accepted:true}}if(action==='stop')return{stopped:true};throw Error(action)});
 let task=create(f.service,'restart-transition');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});const observed=await f.service.observe({taskId:task.taskId}),live=f.service.get(task.taskId),selected=f.service.recipeFor(observed.snapshot,live,live.applications[0]);
 await f.service.apply({taskId:task.taskId,expectedRevision:observed.task.revision,actions:selected.actions});await wait();assert.equal(f.service.get(task.taskId).applications[0].operation.state,'verified');
 f.restart({recipeTransitionTimeoutMs:500});task=f.service.status({taskId:task.taskId});assert.equal(task.state,'queued');const result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision}),operation=f.service.get(task.taskId).applications[0].operation;
 assert.equal(result.path,'recipe',JSON.stringify({result,operation,reads,writes}));assert.equal(result.task.state,'finished');assert.equal(writes,1);assert.equal(operation.postcondition.state,'matched');assert.ok(reads>=3);
}));

test('an unknown recipe receipt reconciles into settlement without replay',fixture(async f=>{
 await learnOneField(f);f.snapshot.fields[0].value='';let writes=0;
 f.setBrowser(async(action,args)=>{f.calls.push({action,args:structuredClone(args)});if(action==='observe')return structuredClone(f.snapshot);if(action==='act'){writes++;f.snapshot.fields[0].value=args.actionSpec.value;throw Error('transport_lost_after_dispatch')}if(action==='progress')return{accepted:true};if(action==='stop')return{stopped:true};throw Error(action)});
 const task=create(f.service,'reconcile-transition'),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision}),operation=f.service.get(task.taskId).applications[0].operation;
 assert.equal(result.path,'recipe',JSON.stringify(result));assert.equal(result.task.state,'finished');assert.equal(writes,1);assert.equal(operation.reconciliation.attemptedVerified,true);assert.equal(operation.postcondition.state,'matched');
}));

test('a verified Recipe result closes at the resume boundary without extending its transition',fixture(async f=>{
 await learnOneField(f);f.snapshot.fields[0].value='';
 const before=structuredClone(f.service.store.data.recipes),beforeRecipe=Object.values(before)[0];let writes=0;
 f.setBrowser(async(action,args)=>{if(action==='observe')return structuredClone(f.snapshot);if(action==='act'){writes++;f.snapshot.fields[0].value=args.actionSpec.value;f.snapshot.fields.push({ref:'new-field',label:'新要求',type:'text',value:''});return{accepted:true}}if(action==='stop')return{stopped:true};throw Error(action)});
 const task=create(f.service,'changed-result'),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'recipe');assert.equal(result.outcome,'partial');assert.equal(result.task.state,'finished');assert.equal(writes,1);assert.equal(result.finalSubmitAllowed,false);
 const operation=f.service.get(task.taskId).applications[0].operation,recipe=Object.values(f.service.store.data.recipes)[0];
 assert.equal(operation.postcondition.state,'matched');assert.equal(Object.keys(recipe.transitions).length,Object.keys(beforeRecipe.transitions).length);assert.equal(Object.keys(recipe.terminals).length,Object.keys(beforeRecipe.terminals).length);
 assert.match(result.task.notice,/页面其余内容由你处理/);
}));

test('unexpected replay outcomes with an unfilled known resume fact return to learning without extending the recipe',fixture(async f=>{
 await learnOneField(f);f.snapshot.fields[0].value='';
 const before=structuredClone(f.service.store.data.recipes);let writes=0;
 f.setBrowser(async(action,args)=>{if(action==='observe')return structuredClone(f.snapshot);if(action==='act'){writes++;f.snapshot.fields[0].value=args.actionSpec.value;f.snapshot.fields.push({ref:'new-field',label:'邮箱',semanticId:'candidate.email',type:'email',value:''});return{accepted:true}}if(action==='stop')return{stopped:true};throw Error(action)});
 const task=f.service.create({clientRequestId:'changed-known-result',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'candidate.name',value:'测试姓名',sourceRef:'synthetic:test'},{id:'candidate.email',value:'test@example.test',sourceRef:'synthetic:test'}]}),result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'adaptive');assert.equal(result.reason,'recipe_missing_transition');assert.equal(writes,1);
 assert.equal(f.service.get(task.taskId).applications[0].operation.postcondition.state,'matched');
 assert.equal(Object.keys(Object.values(f.service.store.data.recipes)[0].transitions).length,Object.keys(Object.values(before)[0].transitions).length);
}));

test('a previously reported Recipe timeout re-observes the current page instead of failing forever',fixture(async f=>{
 await learnOneField(f);f.snapshot.fields[0].value='测试姓名';f.calls.length=0;
 let task=create(f.service,'resume-after-timeout');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});const application=f.service.get(task.taskId).applications[0],startedAt=new Date(Date.now()-5_000).toISOString();
 application.operation={id:'settled-timeout',state:'verified',actions:[{kind:'fill',fieldRef:'runtime-name',semanticId:'candidate.name',factId:'candidate.name',value:'测试姓名'}],index:1,evidence:[{action:{kind:'fill'},evidence:{accepted:true}}],createdAt:startedAt,sourceSnapshot:{...structuredClone(f.snapshot),fields:[{...f.snapshot.fields[0],value:''}]},postcondition:{kind:'recipe',state:'timeout',transitionId:'old-edge',sourceState:'old-state',expectedStates:['0'.repeat(64)],lockedAt:startedAt,settledAt:startedAt,reason:'expected_state_not_reached'}};application.operations=[application.operation];f.service.store.write();
 const result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});
 assert.equal(result.path,'recipe');assert.equal(result.task.state,'finished');assert.equal(result.actionCount,0);assert.equal(f.calls.filter(item=>item.action==='observe').length,1);assert.equal(f.calls.some(item=>item.action==='act'),false);
}));

test('raw field interpretation learns a portable mapping through the same guarded service',fixture(async f=>{
 f.snapshot.fields[0].semanticId=null;f.snapshot.rawFields=structuredClone(f.snapshot.fields);
 f.snapshot.evidence={schemaVersion:1,complete:true,nodes:[],layers:[],interactionDigest:'unclassified'};
 let task=await ready(f.service,'interpret-raw');let a=f.service.get(task.taskId).applications[0];
 const interpreted=f.service.learning_interpret({taskId:task.taskId,expectedRevision:task.revision,observationId:a.observationId,mappings:[{kind:'field',ref:'runtime-name',semanticId:'candidate.name'}]});
 assert.equal(interpreted.snapshot.fields[0].semanticId,'candidate.name');
 assert.equal(interpreted.observationId,a.observationId);const [{controlRef,...coverageRow}]=interpreted.coverage;assert.match(controlRef,/^[a-f0-9]{64}$/);assert.deepEqual(coverageRow,{fieldRef:'runtime-name',label:'姓名',semanticId:'candidate.name',factId:'candidate.name',disposition:'mapped',type:'text',verified:false});
 const actions=[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}];
 const plan=f.service.learning_propose({taskId:task.taskId,expectedRevision:interpreted.task.revision,observationId:a.observationId,question:'核验字段映射',hypothesis:'该字段承载姓名',expected:'姓名保留',actions});
 await f.service.apply({taskId:task.taskId,expectedRevision:plan.task.revision,actions});await wait();f.snapshot.rawFields[0].value=f.snapshot.fields[0].value;
 task=(await f.service.observe({taskId:task.taskId})).task;
 const learned=Object.values(f.service.store.data.recipes)[0];assert.equal(learned.pageModel.rules[0].semanticId,'candidate.name');
 f.service.finish({taskId:task.taskId,expectedRevision:task.revision,outcome:'partial'});
 f.snapshot.documentEpoch='new-doc';f.snapshot.fields[0]={...f.snapshot.fields[0],ref:'new-ref',value:''};f.snapshot.rawFields=structuredClone(f.snapshot.fields);
 const next=create(f.service,'interpret-replay'),replayed=await f.service.run_recipe({taskId:next.taskId,expectedRevision:next.revision});
 assert.equal(replayed.path,'recipe');assert.equal(replayed.task.state,'finished');assert.equal(f.snapshot.fields[0].value,'测试姓名');
}));

test('coverage review addresses durable rows independently from browser field refs',fixture(async f=>{
 f.snapshot.fields.push({ref:'website-question',label:'补充问题',semanticId:null,group:'网站问题',value:'',type:'text'});
 const task=await ready(f.service,'coverage-control-ref'),application=f.service.get(task.taskId).applications[0],unknown=application.coverage.find(row=>row.fieldRef==='website-question');
 assert.ok(unknown);assert.throws(()=>f.service.coverage_review({taskId:task.taskId,expectedRevision:task.revision,items:[{controlRef:'missing-ref',disposition:'no_resume_fact'}]}),/coverage_item_invalid:0:control_ref_unknown/);
 assert.throws(()=>f.service.coverage_review({taskId:task.taskId,expectedRevision:task.revision,items:[]}),/coverage_review_unreviewed_items_required/);
 const context=f.service.learningContextFor(application,f.service.get(task.taskId)),reviewRef=context.coverage.find(row=>row.fieldRef==='website-question').controlRef;
 assert.notEqual(reviewRef,'website-question');
 const pending=f.service.coverage_pending({taskId:task.taskId});assert.equal(pending.revision,task.revision);assert.ok(pending.unreviewed.some(row=>row.controlRef===reviewRef&&row.fieldRef==='website-question'));
 const reviewed=f.service.coverage_review({taskId:task.taskId,expectedRevision:task.revision,items:[{controlRef:reviewRef,disposition:'no_resume_fact'}]});
 assert.equal(reviewed.state,'running');assert.deepEqual(reviewed.learningContext.suggestedActions,[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]);assert.equal(f.service.get(task.taskId).applications[0].coverage.find(row=>row.fieldRef==='website-question').disposition,'no_resume_fact');
}));

test('a calendar popover value cannot satisfy a resume obligation until the main form retains it',fixture(async f=>{
 const task=await ready(f.service,'calendar-popover-readback'),application=f.service.get(task.taskId).applications[0],main=structuredClone(application.snapshot),dialog={...structuredClone(main),activeDialog:{ref:'calendar',role:'dialog',blocking:true},fields:[{ref:'calendar-input',label:'开始日期',semanticId:'candidate.name',value:'测试姓名',type:'text'}]};
 application.sectionSnapshots={'基本信息':main};application.snapshot=dialog;
 assert.equal(f.service.aggregateSnapshot(application).fields.some(field=>field.ref==='calendar-input'),false);
 assert.equal(f.service.syncObligationLedger(application,f.service.get(task.taskId)).pending.length,1);
 const entry=application.obligationLedger.entries['fact:candidate.name'];entry.status='verified';entry.evidence=[{kind:'field_readback',observationId:'calendar-readback',controlRefs:['calendar-input']}];
 application.operations.push({id:'calendar-write',sourceSnapshot:dialog,actions:[{kind:'fill',fieldRef:'calendar-input',factId:'candidate.name'}]});
 assert.equal(f.service.syncObligationLedger(application,f.service.get(task.taskId)).pending.length,1);
 assert.equal(entry.evidence.at(-1).reason,'dialog_value_not_committed');
 application.snapshot={...main,fields:[{...main.fields[0],value:'测试姓名'}]};
 assert.equal(f.service.syncObligationLedger(application,f.service.get(task.taskId)).complete,true);
}));

test('a previously verified project is reopened when its exposed section loses the retained value',fixture(async f=>{
 f.snapshot.fields=[{ref:'project-name',label:'项目名称',semanticId:'project_1_name',group:'项目经历',value:'测试项目',type:'text'}];
 f.snapshot.controls=[{ref:'project-add',label:'添加',kind:'add',section:'项目经历',sectionKey:'heading:项目经历',sectionCandidates:[{key:'heading:项目经历',label:'项目经历',source:'heading'}],safe:true}];
 let task=f.service.create({clientRequestId:'lost-project-value',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'project_1_name',value:'测试项目',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 assert.equal(task.applications[0].applicationPlan.verified,1);
 f.snapshot.fields=[];task=(await f.service.observe({taskId:task.taskId})).task;
 assert.equal(task.applications[0].applicationPlan.pending,1);
 assert.equal(f.service.get(task.taskId).applications[0].obligationLedger.entries['fact:project_1_name'].evidence.at(-1).kind,'readback_lost');
}));

test('learning coverage excludes stale rows when runtime refs are reused across page states',fixture(async f=>{
 const task=await ready(f.service,'coverage-reused-runtime-ref'),live=f.service.get(task.taskId),application=live.applications[0];
 application.coverage.push({key:'a'.repeat(64),fieldRef:'runtime-name',label:'旧页面字段',semanticId:null,factId:null,disposition:'unreviewed',value:'',checked:null,type:'text'});
 const rows=f.service.learningContextFor(application,live).coverage.filter(row=>row.fieldRef==='runtime-name');
 assert.equal(rows.length,1);assert.equal(rows[0].label,'姓名');
 assert.throws(()=>f.service.coverage_review({taskId:task.taskId,expectedRevision:task.revision,items:[{controlRef:'a'.repeat(64),disposition:'no_resume_fact'}]}),/coverage_item_invalid:0:control_ref_unknown/);
}));

test('coverage mapping requires a current interpreted field',fixture(async f=>{
 f.snapshot.fields.push({ref:'website-question',label:'补充问题',semanticId:null,group:'网站问题',value:'',type:'text'});
 const task=await ready(f.service,'coverage-mapping-proof'),live=f.service.get(task.taskId),application=live.applications[0],row=f.service.learningContextFor(application,live).coverage.find(item=>item.fieldRef==='website-question');
 assert.throws(()=>f.service.coverage_review({taskId:task.taskId,expectedRevision:task.revision,items:[{controlRef:row.controlRef,disposition:'mapped',factId:'candidate.name'}]}),/coverage_mapping_unproven:0/);
}));

test('safe mapped fact actions take precedence over reviewing unrelated page fields',fixture(async f=>{
 f.snapshot.fields.push({ref:'unrelated',label:'网站问题',semanticId:null,group:'网站问题',value:'',type:'text'});
 const task=await ready(f.service,'mapped-before-coverage'),live=f.service.get(task.taskId),application=live.applications[0];
 assert.ok(f.service.learningContextFor(application,live).coverage.some(row=>row.disposition==='unreviewed'));
 assert.ok(f.service.learningContextFor(application,live).suggestedActions.length);
 assert.equal(f.service.learningHandoffRequirement(application,live),'propose');
}));

test('an empty date in a repeatable section cannot be dismissed while its resume date remains pending',fixture(async f=>{
 f.snapshot.fields=[{ref:'project-start',label:'开始日期',semanticId:null,group:'section:项目经历',value:'',type:'text',readOnly:true}];
 let task=f.service.create({clientRequestId:'project-date-not-extra',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'project_1_start',value:'2022-01',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const row=f.service.coverage_pending({taskId:task.taskId}).unreviewed[0];
 assert.throws(()=>f.service.coverage_review({taskId:task.taskId,expectedRevision:task.revision,items:[{controlRef:row.controlRef,disposition:'no_resume_fact'}]}),/coverage_pending_section_fact_requires_mapping/);
}));

test('an extra record date may be reviewed after every pending resume date has its own mapped field',fixture(async f=>{
 f.snapshot.fields=[
  {ref:'campus-start',label:'开始日期',semanticId:'campus_1_start',group:'section:校园经历',value:'',type:'text'},
  {ref:'campus-end',label:'结束日期',semanticId:'campus_1_end',group:'section:校园经历',value:'',type:'text'},
  {ref:'extra-start',label:'开始日期',semanticId:null,group:'section:校园经历',value:'',type:'text'},
  {ref:'extra-end',label:'结束日期',semanticId:null,group:'section:校园经历',value:'',type:'text'}
 ];
 let task=f.service.create({clientRequestId:'extra-campus-date',targets:[{url:f.snapshot.url,tabId:1}],facts:[
  {id:'campus_1_start',value:'2022-01',sourceRef:'synthetic:test'},
  {id:'campus_1_end',value:'2022-07',sourceRef:'synthetic:test'}
 ]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const pending=f.service.coverage_pending({taskId:task.taskId});
 assert.throws(()=>f.service.coverage_review({taskId:task.taskId,expectedRevision:task.revision,observationId:'stale',items:[{fieldRef:'extra-start',disposition:'no_resume_fact'}]}),/coverage_observation_stale/);
 const reviewed=f.service.coverage_review({taskId:task.taskId,expectedRevision:task.revision,observationId:pending.observationId,items:[
  {fieldRef:'extra-start',disposition:'no_resume_fact'},
  {fieldRef:'extra-end',disposition:'no_resume_fact'}
 ]});
 assert.equal(reviewed.learningContext.coverage.find(row=>row.fieldRef==='extra-start').disposition,'no_resume_fact');
 assert.equal(reviewed.applications[0].applicationPlan.pending,2);
}));

test('an ongoing end date can probe a read-only site picker without fabricating a calendar date',fixture(async f=>{
 f.snapshot.fields=[{ref:'project-end',label:'结束日期',semanticId:'project_1_end',group:'section:项目经历',value:'',type:'text',readOnly:true}];
 let task=f.service.create({clientRequestId:'ongoing-project-date',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'project_1_end',value:'至今',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const application=f.service.get(task.taskId).applications[0],action={kind:'select',fieldRef:'project-end',factId:'project_1_end'};
 assert.deepEqual(f.service.learningContextFor(application,f.service.get(task.taskId)).suggestedActions,[action]);
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[action]});
 await wait();
 assert.equal(f.calls.find(call=>call.action==='act').args.actionSpec.value,'至今');
}));

test('a read-only choose placeholder exposes a guarded site option probe',fixture(async f=>{
 f.snapshot.fields=[{ref:'native-place',label:'请选择',placeholder:'请选择',semanticId:'native_place',group:'section:基本信息',value:'',type:'text',readOnly:true}];
 let task=f.service.create({clientRequestId:'readonly-choice-picker',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'native_place',value:'北京',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const application=f.service.get(task.taskId).applications[0],action={kind:'select',fieldRef:'native-place',factId:'native_place'};
 assert.deepEqual(f.service.learningContextFor(application,f.service.get(task.taskId)).suggestedActions,[action]);
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[action]});
 await wait();
 assert.equal(f.calls.find(call=>call.action==='act').args.actionSpec.value,'北京');
}));

test('pending repeatable records with no site fields expose a concrete add action',fixture(async f=>{
 f.snapshot.fields=[];f.snapshot.controls=[{ref:'project-add',label:'添加',kind:'add',safe:false,section:null,sectionKey:null,sectionCandidates:[{key:'heading:项目经历',label:'项目经历',source:'heading'}]}];
 let task=f.service.create({clientRequestId:'project-structural-next',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'project_1_name',value:'测试项目',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const live=f.service.get(task.taskId),context=f.service.learningContextFor(live.applications[0],live);
 assert.deepEqual(context.suggestedStructuralActions,[{kind:'add',controlRef:'project-add',section:'项目经历',sectionKey:'heading:项目经历'}]);
 assert.equal(context.structuralWork.find(row=>row.section==='项目经历').observedFields,0);
}));

test('overlapping wrapper and native input are one logical field',fixture(async f=>{
 f.snapshot.fields=[
  {ref:'date-wrapper',label:'毕业时间*',semanticId:null,group:null,value:'',type:'search',tag:'DIV'},
  {ref:'date-input',label:'毕业时间*',semanticId:'candidate.name',group:null,value:'',type:'text',tag:'INPUT'}
 ];
 f.snapshot.evidence={schemaVersion:1,complete:true,interactionDigest:'date',nodes:[
  {ref:'date-wrapper',interactive:true,hit:true,rect:{left:100,top:100,width:500,height:40}},
  {ref:'date-input',interactive:true,hit:true,rect:{left:110,top:100,width:480,height:40}}
 ],layers:[]};
 const task=await ready(f.service,'composite-field'),application=f.service.get(task.taskId).applications[0],context=f.service.learningContextFor(application,f.service.get(task.taskId));
 assert.deepEqual(application.snapshot.fields.map(field=>field.ref),['date-input']);
 assert.deepEqual(context.suggestedActions,[{kind:'fill',fieldRef:'date-input',factId:'candidate.name'}]);
 assert.equal(application.coverage.some(row=>row.fieldRef==='date-wrapper'),false);
}));

test('a verified normalized selection is not suggested again by coverage',fixture(async f=>{
 f.snapshot.fields[0].type='search';
 f.setBrowser(async(action,args)=>{
  if(action==='observe')return structuredClone(f.snapshot);
  if(action==='act'){
   const field=f.snapshot.fields.find(item=>item.ref===args.actionSpec.fieldRef),accepted=`${args.actionSpec.value}省`;
   field.value=accepted;
   return{accepted:true,fieldRef:field.ref,documentEpoch:f.snapshot.documentEpoch,normalized:true,value:accepted};
  }
  throw Error(`unexpected_browser_${action}`);
 });
 let task=await ready(f.service,'normalized-coverage');
 await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'select',fieldRef:'runtime-name',factId:'candidate.name'}]});await wait();
 const observed=await f.service.observe({taskId:task.taskId}),application=f.service.get(task.taskId).applications[0],context=f.service.learningContextFor(application,f.service.get(task.taskId));
 assert.equal(observed.task.state,'running');
 assert.equal(context.coverage[0].verified,true);
 assert.deepEqual(context.suggestedActions,[]);
}));

test('a late accepted browser receipt cannot revive a stopped task or publish learning',fixture(async f=>{
 let complete,entered;const started=new Promise(resolve=>entered=resolve);
 f.setBrowser(async(action)=>{if(action==='observe')return structuredClone(f.snapshot);if(action==='act'){entered();return new Promise(resolve=>complete=resolve)}if(action==='stop')return{stopped:true};throw Error(action)});
 const task=await ready(f.service,'late-receipt');await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}]});await started;
 await f.service.control({taskId:task.taskId,kind:'stop'});complete({accepted:true});await wait();
 const final=f.service.get(task.taskId);assert.equal(final.state,'cancelled');assert.notEqual(final.applications[0].operation.state,'verified');assert.equal(Object.keys(f.service.store.data.recipes).length,0);
}));

test('prelearning requires independent resume variants and verified replay before release eligibility',fixture(async f=>{
 const {releaseCoverage}=await import('../src/recipe-compiler.mjs');
 f.snapshot.evidence={schemaVersion:1,complete:true,nodes:[],layers:[],interactionDigest:'form'};
 function intake(id,value){const facts=[{id:'candidate.name',value,sourceRef:'synthetic:'+id}];f.service.request_save({clientRequestId:id,learningMode:'prelearn',currentPage:true,facts});return f.service.create({clientRequestId:id,targets:[{url:f.snapshot.url,tabId:1}]})}
 let task=intake('prelearn-a','测试甲');task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});let observed=await f.service.observe({taskId:task.taskId});const actions=[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}];
 const proposal=f.service.learning_propose({taskId:task.taskId,expectedRevision:observed.task.revision,observationId:observed.observationId,question:'字段事实对应？',hypothesis:'姓名可保留',expected:'回读一致',actions});
 await f.service.run_adaptive_plan({taskId:task.taskId,expectedRevision:proposal.task.revision,actions});task=f.service.status({taskId:task.taskId});
 assert.equal(releaseCoverage(Object.values(f.service.store.data.recipes)[0]).eligible,false);
 f.snapshot.fields[0].value='';f.snapshot.documentEpoch='independent-document';task=intake('prelearn-b','测试乙');
 const result=await f.service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});assert.equal(result.task.state,'finished');assert.equal(f.snapshot.fields[0].value,'测试乙');
 const bundle=Object.values(f.service.store.data.recipes)[0];assert.equal(releaseCoverage(bundle).eligible,true,JSON.stringify(releaseCoverage(bundle)));
 const report=f.service.learning_report({taskId:task.taskId});assert.equal(report.applications[0].publication.eligible,true);
}));

test('an explicit interpretation revision corrects the draft and retains its history',fixture(async f=>{
 let task=await ready(f.service,'revise-map');const a=f.service.get(task.taskId).applications[0];f.service.get(task.taskId).factSnapshot.facts.push({id:'candidate.alias',value:'测试别名',sourceRef:'synthetic:alias'});
 let result=f.service.learning_interpret({taskId:task.taskId,expectedRevision:task.revision,observationId:a.observationId,mappings:[{kind:'field',ref:'runtime-name',semanticId:'candidate.alias'}]});
 result=f.service.learning_interpret({taskId:task.taskId,expectedRevision:result.task.revision,observationId:a.observationId,mappings:[{kind:'field',ref:'runtime-name',semanticId:'candidate.name'}]});
 assert.equal(result.snapshot.fields[0].semanticId,'candidate.name');assert.equal(a.interpretationHistory.length,2);assert.equal(a.interpretationHistory[0].rules[0].semanticId,'candidate.alias');
}));

test('an interpretation cardinality revision replaces the persisted recipe model',fixture(async f=>{
 f.snapshot.fields=[{ref:'runtime-name',label:'请输入',value:'',type:'text'},{ref:'peer',label:'请输入',value:'',type:'text'}];f.snapshot.rawFields=structuredClone(f.snapshot.fields);
 let task=await ready(f.service,'revise-cardinality'),a=f.service.get(task.taskId).applications[0];let result=f.service.learning_interpret({taskId:task.taskId,expectedRevision:task.revision,observationId:a.observationId,mappings:[{kind:'field',ref:'runtime-name',semanticId:'candidate.name'}]});
 const key=systemFor(a.snapshot).key,prior=structuredClone(a.interpretationModel);f.service.store.data.recipes[key]={formatVersion:2,recipeId:key,system:systemFor(a.snapshot),status:'learning',pageModel:structuredClone(prior),states:{},transitions:{},terminals:{}};
 a.snapshot.rawFields.push({ref:'peer-2',label:'请输入',value:'',type:'text'});a.snapshot.fields.push({ref:'peer-2',label:'请输入',value:'',type:'text'});
 result=f.service.learning_interpret({taskId:task.taskId,expectedRevision:result.task.revision,observationId:a.observationId,mappings:[{kind:'field',ref:'runtime-name',semanticId:'candidate.name'}]});
 assert.equal(prior.rules[0].cardinality,2);assert.equal(a.interpretationModel.rules[0].cardinality,3);assert.deepEqual(f.service.store.data.recipes[key].pageModel,a.interpretationModel);
}));

test('withdrawing a disproven interpretation settles its unknown action without replaying the batch tail',fixture(async f=>{
 f.snapshot.fields[0].semanticId=null;f.snapshot.rawFields=structuredClone(f.snapshot.fields);
 let task=await ready(f.service,'withdraw-unknown-map'),a=f.service.get(task.taskId).applications[0];
 let result=f.service.learning_interpret({taskId:task.taskId,expectedRevision:task.revision,observationId:a.observationId,mappings:[{kind:'field',ref:'runtime-name',semanticId:'candidate.name'}]});
 const stored=f.service.get(task.taskId);a=stored.applications[0];a.operation={id:'unknown-map-action',state:'unknown',index:0,actions:[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'},{kind:'next',controlRef:'tail'}],evidence:[],createdAt:new Date().toISOString()};stored.state='needs_attention';
 result=f.service.learning_interpret({taskId:task.taskId,expectedRevision:result.task.revision,observationId:a.observationId,mappings:[{kind:'field',ref:'runtime-name',remove:true}]});
 assert.equal(result.task.state,'running');assert.equal(a.operation.state,'failed');assert.equal(a.operation.error,'interpretation_withdrawn');assert.equal(a.operation.index,0);assert.equal(result.snapshot.fields[0].semanticId,null);assert.equal(a.interpretationHistory.at(-1).removals.length,1);
 assert.equal(f.calls.filter(call=>call.action==='act').length,0);
}));

test('reconciliation abandons an unknown batch when its learned field mapping is no longer valid',fixture(async f=>{
 let task=await ready(f.service,'reconcile-invalidated-map'),stored=f.service.get(task.taskId),application=stored.applications[0];
 application.operation={id:'invalidated-map-action',state:'unknown',index:0,actions:[{kind:'fill',fieldRef:'runtime-name',semanticId:'candidate.name',semanticOrdinal:0,group:null,value:'测试姓名',before:'',factId:'candidate.name'},{kind:'fill',fieldRef:'tail',semanticId:'candidate.alias',semanticOrdinal:0,group:null,value:'不应执行',before:'',factId:'candidate.alias'}],evidence:[],createdAt:new Date(Date.now()-1000).toISOString(),sourceSnapshot:structuredClone(application.snapshot)};stored.state='needs_reconcile';
 application.interpretationModel={formatVersion:1,mappings:{},rules:[]};f.snapshot.fields[0].semanticId=null;f.snapshot.rawFields=structuredClone(f.snapshot.fields);f.snapshot.fields[0].value='测试姓名';
 task=await f.service.reconcile({taskId:task.taskId});
 assert.equal(task.state,'running');assert.equal(application.operation.state,'failed');assert.equal(application.operation.error,'interpretation_invalidated_during_reconciliation');assert.equal(application.operation.index,0);assert.equal(application.operation.reconciliation.mappingInvalidated,true);assert.equal(f.service.operationSettled(application.operation),true);assert.equal(f.calls.filter(call=>call.action==='act').length,0);
}));

test('a new observation cancels an undispatched learning proposal without spending its budget',fixture(async f=>{
 let task=await ready(f.service,'stale-proposal'),a=f.service.get(task.taskId).applications[0];const actions=[{kind:'fill',fieldRef:'runtime-name',factId:'candidate.name'}];
 const first=f.service.learning_propose({taskId:task.taskId,expectedRevision:task.revision,observationId:a.observationId,question:'填写？',hypothesis:'当前引用',expected:'保留',actions});
 task=(await f.service.observe({taskId:task.taskId})).task;a=f.service.get(task.taskId).applications[0];
 const second=f.service.learning_propose({taskId:task.taskId,expectedRevision:task.revision,observationId:a.observationId,question:'填写？',hypothesis:'新观察引用',expected:'保留',actions});
 const old=a.learning.episodes.find(item=>item.id===first.episodeId);assert.equal(old.verdict,'cancelled');assert.equal(old.verification.reason,'proposal_observation_changed');assert.equal(a.learning.activeMs,0);assert.notEqual(second.episodeId,first.episodeId);
}));

test('ApplicationPlan keeps project and campus facts pending when a Recipe covers only basic information',fixture(async f=>{
 f.snapshot.fields[0].value='测试姓名';
 let task=f.service.create({clientRequestId:'full-plan-not-recipe-subset',targets:[{url:f.snapshot.url,tabId:1}],facts:[
  {id:'candidate.name',value:'测试姓名',sourceRef:'synthetic:test'},
  {id:'project_1_name',value:'项目一',sourceRef:'synthetic:test'},
  {id:'campus_1_role',value:'负责人',sourceRef:'synthetic:test'}
 ]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const live=f.service.get(task.taskId),plan=live.applications[0].applicationPlan,assessment=f.service.applicationPlanAssessment(live.applications[0],live);
 assert.equal(plan.obligations.length,3);assert.deepEqual(assessment.pending.map(item=>item.factId).sort(),['campus_1_role','project_1_name']);
 markRecipeTerminal(f.service.store.data.recipes,f.snapshot,{predecessorKind:null,coverageFactIds:['candidate.name']});
 assert.throws(()=>f.service.finish({taskId:task.taskId,expectedRevision:task.revision,outcome:'partial'}),/application_plan_incomplete/);
}));

test('cold learning sees unmapped structural controls needed to reach pending resume sections',fixture(async f=>{
 f.snapshot.controls=[
  {ref:'projects-tab',label:'项目经历',kind:'section',safe:false,section:null,sectionKey:null,sectionCandidates:[{key:'heading:项目经历',label:'项目经历',source:'heading'}]},
  {ref:'projects-add',label:'添加',kind:'add',safe:false,section:null,sectionKey:null,sectionCandidates:[{key:'heading:项目经历',label:'项目经历',source:'heading'}]}
 ];
 let task=f.service.create({clientRequestId:'structural-plan-context',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'project_1_name',value:'项目一',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});await f.service.observe({taskId:task.taskId});
 const live=f.service.get(task.taskId),context=f.service.learningContextFor(live.applications[0],live);
 assert.deepEqual(context.controls.map(item=>[item.ref,item.kind,item.safe]),[['projects-tab','section',false],['projects-add','add',false]]);
 assert.deepEqual(context.applicationPlan.pending.map(item=>item.factId),['project_1_name']);
}));

test('supplemental profile facts are reused while resume versions remain isolated',fixture(async f=>{
 const profile=f.service.profile_save({saveRequestId:'profile-one',facts:[{id:'candidate.height',value:'180 cm',sourceRef:'profile:explicit'}]});assert.equal(profile.revision,1);
 const first=f.service.resume_save({saveRequestId:'resume-one',name:'产品简历',facts:[{id:'candidate.name',value:'甲',sourceRef:'resume:first'}],attachment:{resource:'test'}}),second=f.service.resume_save({saveRequestId:'resume-two',name:'研发简历',facts:[{id:'candidate.name',value:'乙',sourceRef:'resume:second'}],attachment:{resource:'test'}});
 f.service.request_save({clientRequestId:'profile-task-one',currentPage:true,resume:{id:first.resumeId,revision:first.revision},taskAnswers:[],profileRevision:1});
 f.service.request_save({clientRequestId:'profile-task-two',currentPage:true,resume:{id:second.resumeId,revision:second.revision},taskAnswers:[],profileRevision:1});
 const one=f.service.create({clientRequestId:'profile-task-one',targets:[{url:f.snapshot.url,tabId:1}]}),two=f.service.create({clientRequestId:'profile-task-two',targets:[{url:'https://jobs.example.test/position/654321/apply',tabId:2}]});
 const factsOne=new Map(f.service.task_context({taskId:one.taskId}).facts.map(item=>[item.id,item.value])),factsTwo=new Map(f.service.task_context({taskId:two.taskId}).facts.map(item=>[item.id,item.value]));
 assert.equal(factsOne.get('candidate.height'),'180 cm');assert.equal(factsTwo.get('candidate.height'),'180 cm');assert.equal(factsOne.get('candidate.name'),'甲');assert.equal(factsTwo.get('candidate.name'),'乙');
}));

test('repeatable project and campus records are added and verified before the plan can finish',fixture(async f=>{
 f.snapshot.fields=[
  {ref:'project-1',label:'项目名称',semanticId:'project_1_name',group:'project',value:'项目甲',type:'text'},
  {ref:'campus-1',label:'校园职务',semanticId:'campus_1_role',group:'campus',value:'社长',type:'text'}
 ];
 f.snapshot.controls=[
  {ref:'add-project',label:'添加项目经历',kind:'add',section:'项目经历',sectionKey:'heading:项目经历',sectionCandidates:[{key:'heading:项目经历',label:'项目经历',source:'heading'}],group:'project',safe:true},
  {ref:'add-campus',label:'添加校园经历',kind:'add',section:'校园经历',sectionKey:'heading:校园经历',sectionCandidates:[{key:'heading:校园经历',label:'校园经历',source:'heading'}],group:'campus',safe:true}
 ];
 f.setBrowser(async(action,args)=>{
  f.calls.push({action,args:structuredClone(args)});
  if(action==='observe')return structuredClone(f.snapshot);
  if(action==='act'){
   const spec=args.actionSpec;
   if(spec.kind==='add'&&spec.controlRef==='add-project')f.snapshot.fields.push({ref:'project-2',label:'项目名称',semanticId:'project_2_name',group:'project',value:'',type:'text'});
   else if(spec.kind==='add'&&spec.controlRef==='add-campus')f.snapshot.fields.push({ref:'campus-2',label:'校园职务',semanticId:'campus_2_role',group:'campus',value:'',type:'text'});
   else{const field=f.snapshot.fields.find(item=>item.ref===spec.fieldRef);if(field)field.value=spec.value}
   return{accepted:true};
  }
  if(action==='stop')return{stopped:true};
  throw Error(`unexpected_browser_${action}`);
 });
 let task=f.service.create({clientRequestId:'repeatable-plan',targets:[{url:f.snapshot.url,tabId:1}],facts:[
  {id:'project_1_name',value:'项目甲',sourceRef:'synthetic:test'},
  {id:'project_2_name',value:'项目乙',sourceRef:'synthetic:test'},
  {id:'campus_1_role',value:'社长',sourceRef:'synthetic:test'},
  {id:'campus_2_role',value:'部长',sourceRef:'synthetic:test'}
 ]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 assert.deepEqual(f.service.applicationPlanAssessment(f.service.get(task.taskId).applications[0],f.service.get(task.taskId)).pending.map(item=>item.factId).sort(),['campus_2_role','project_2_name']);
 const act=async actions=>{await f.service.apply({taskId:task.taskId,expectedRevision:task.revision,actions});await wait();task=(await f.service.observe({taskId:task.taskId})).task};
 await act([{kind:'add',controlRef:'add-project',section:'项目经历',sectionKey:'heading:项目经历'}]);
 await act([{kind:'fill',fieldRef:'project-2',factId:'project_2_name'}]);
 await act([{kind:'add',controlRef:'add-campus',section:'校园经历',sectionKey:'heading:校园经历'}]);
 await act([{kind:'fill',fieldRef:'campus-2',factId:'campus_2_role'}]);
 const live=f.service.get(task.taskId),assessment=f.service.applicationPlanAssessment(live.applications[0],live);
 assert.equal(assessment.complete,true);assert.equal(assessment.verified.length,4);assert.equal(f.calls.filter(item=>item.action==='act'&&item.args.actionSpec.kind==='add').length,2);
 const finished=f.service.finish({taskId:task.taskId,expectedRevision:task.revision,outcome:'partial'});assert.equal(finished.state,'finished');
}));

test('site-no-field exceptions require a complete section observation and exhausted add capacity',fixture(async f=>{
 let task=f.service.create({clientRequestId:'plan-site-no-field',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'project_2_name',value:'项目乙',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const live=f.service.get(task.taskId),application=live.applications[0];
 application.sectionSnapshots={'项目经历':{...structuredClone(f.snapshot),activeSection:'项目经历',activeSectionKey:'navigation:项目经历',fields:[],controls:[{ref:'add-project',kind:'add',section:'项目经历',safe:true}],evidence:{complete:true,nodes:[],layers:[]}}};f.service.store.write();
 assert.throws(()=>f.service.application_plan_review({taskId:task.taskId,expectedRevision:task.revision,items:[{factId:'project_2_name',disposition:'site_no_field'}]}),/application_plan_repeatable_capacity_available/);
 application.sectionSnapshots['项目经历'].controls=[{ref:'unmapped-add',kind:'add',section:null,sectionCandidates:[{key:'heading:项目经历',label:'项目经历'}]}];f.service.store.write();
 assert.throws(()=>f.service.application_plan_review({taskId:task.taskId,expectedRevision:task.revision,items:[{factId:'project_2_name',disposition:'site_no_field'}]}),/application_plan_repeatable_capacity_available/);
 let current=f.service.get(task.taskId).applications[0];current.sectionSnapshots['项目经历'].controls=[];current.sectionSnapshots['项目经历'].evidence.complete=false;f.service.store.write();
 assert.throws(()=>f.service.application_plan_review({taskId:task.taskId,expectedRevision:task.revision,items:[{factId:'project_2_name',disposition:'site_no_field'}]}),/application_plan_section_not_observed/);
 current=f.service.get(task.taskId).applications[0];current.sectionSnapshots['项目经历'].evidence.fieldInventoryComplete=true;f.service.store.write();
 current.sectionSnapshots['项目经历'].fields=[{ref:'project-2',label:'项目名称',type:'text',semanticId:'project_2_name',group:'section:项目经历',value:''}];f.service.store.write();
 assert.throws(()=>f.service.application_plan_review({taskId:task.taskId,expectedRevision:task.revision,items:[{factId:'project_2_name',disposition:'site_no_field'}]}),/application_plan_field_present/);
 current=f.service.get(task.taskId).applications[0];current.sectionSnapshots['项目经历'].fields=[];f.service.store.write();
 const reviewed=f.service.application_plan_review({taskId:task.taskId,expectedRevision:task.revision,items:[{factId:'project_2_name',disposition:'site_no_field'}]});
 assert.equal(reviewed.task.state,'finished');assert.equal(reviewed.applicationPlan.complete,true);assert.equal(reviewed.applicationPlan.exceptions[0].evidence[0].kind,'section_exhausted');
}));

test('site-no-field proof reopens when the section changes or its observation becomes stale',fixture(async f=>{
 let task=f.service.create({clientRequestId:'site-no-field-invalidation',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'project_2_name',value:'项目乙',sourceRef:'synthetic:test'},{id:'candidate.name',value:'测试姓名',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const live=f.service.get(task.taskId),application=live.applications[0];
 application.sectionSnapshots={'项目经历':{...structuredClone(application.snapshot),activeSection:'项目经历',activeSectionKey:'navigation:项目经历',fields:[],controls:[],evidence:{fieldInventoryComplete:true}}};f.service.store.write();
 const reviewed=f.service.application_plan_review({taskId:task.taskId,expectedRevision:task.revision,items:[{factId:'project_2_name',disposition:'site_no_field'}]});
 assert.equal(reviewed.task.state,'running');assert.equal(application.obligationLedger.entries['fact:project_2_name'].status,'site_no_field');
 f.service.syncObligationLedger(application,live);
 assert.equal(application.obligationLedger.entries['fact:project_2_name'].status,'site_no_field');
 application.sectionSnapshots['项目经历'].fields.push({ref:'new-project',label:'项目名称',semanticId:'project_2_name',type:'text',value:''});
 f.service.syncObligationLedger(application,live);
 assert.equal(application.obligationLedger.entries['fact:project_2_name'].status,'pending');
 application.sectionSnapshots['项目经历'].fields=[];
 const again=f.service.application_plan_review({taskId:task.taskId,expectedRevision:reviewed.task.revision,items:[{factId:'project_2_name',disposition:'site_no_field'}]});
 assert.equal(again.task.state,'running');
 application.snapshot.documentEpoch='doc-2';
 f.service.syncObligationLedger(application,live);
 assert.equal(application.obligationLedger.entries['fact:project_2_name'].status,'pending');
}));

test('visiting other information does not prove that a self-evaluation field is absent',fixture(async f=>{
 let task=f.service.create({clientRequestId:'section-identity-proof',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'self_evaluation',value:'简要自评',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const application=f.service.get(task.taskId).applications[0];
 application.sectionSnapshots={'自我评价':{...structuredClone(f.snapshot),activeSection:'自我评价',activeSectionKey:'navigation:其他信息',fields:[],controls:[],evidence:{fieldInventoryComplete:true}}};
 f.service.store.write();
 assert.throws(()=>f.service.application_plan_review({taskId:task.taskId,expectedRevision:task.revision,items:[{factId:'self_evaluation',disposition:'site_no_field'}]}),/application_plan_section_identity_unproven/);
 assert.equal(f.service.applicationPlanAssessment(application,f.service.get(task.taskId)).pending.length,1);
}));

test('an unsupported coverage label alone cannot clear a resume obligation',fixture(async f=>{
 const task=await ready(f.service,'unsupported-needs-failed-action');
 const application=f.service.get(task.taskId).applications[0];
 application.coverage[0].disposition='unsupported';
 f.service.store.write();
 assert.throws(()=>f.service.application_plan_review({taskId:task.taskId,expectedRevision:task.revision,items:[{factId:'candidate.name',disposition:'unsupported'}]}),/application_plan_unsupported_unproven/);
 assert.equal(f.service.applicationPlanAssessment(application,f.service.get(task.taskId)).pending.length,1);
}));

test('a stale no-effect write is reopened, while a proven missing picker option can be reported unsupported',fixture(async f=>{
 f.snapshot.extensionVersion='0.2.43';
 f.snapshot.fields=[{ref:'project-end',label:'结束日期',semanticId:'project_1_end',group:'section:项目经历',value:'',type:'text',readOnly:true}];
 let task=f.service.create({clientRequestId:'unsupported-proven-picker',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'project_1_end',value:'至今',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const application=f.service.get(task.taskId).applications[0],action={kind:'select',fieldRef:'project-end',factId:'project_1_end'};
 application.operations.push({id:'old-no-effect',state:'failed',error:'operation_no_effect',actions:[action],sourceSnapshot:{...structuredClone(application.snapshot),extensionVersion:'0.2.29'}});
 application.obligationLedger.entries['fact:project_1_end'].status='unsupported';
 f.service.syncObligationLedger(application,f.service.get(task.taskId));
 assert.equal(application.obligationLedger.entries['fact:project_1_end'].status,'pending');
 assert.throws(()=>f.service.application_plan_review({taskId:task.taskId,expectedRevision:task.revision,items:[{factId:'project_1_end',disposition:'unsupported'}]}),/application_plan_unsupported_unproven/);
 const current=f.service.get(task.taskId).applications[0];
 current.operations.push({id:'current-missing-option',state:'failed',index:0,error:'option_not_found:ongoing_date',evidence:[],actions:[action],sourceSnapshot:structuredClone(current.snapshot)});
 f.service.store.write();
 const reviewed=f.service.application_plan_review({taskId:task.taskId,expectedRevision:task.revision,items:[{factId:'project_1_end',disposition:'unsupported'}]});
 assert.equal(reviewed.applicationPlan.exceptions[0].evidence.find(item=>item.kind==='control_unsupported').operationId,'current-missing-option');
}));

test('a failed first action in a batch proves only that field unsupported',fixture(async f=>{
 f.snapshot.extensionVersion='0.2.45';f.snapshot.fields=[{ref:'end-4',label:'结束日期',semanticId:'project_4_end',group:'section:项目经历',value:'',type:'text',readOnly:true},{ref:'end-5',label:'结束日期',semanticId:'project_5_end',group:'section:项目经历',value:'',type:'text',readOnly:true}];
 let task=f.service.create({clientRequestId:'batch-unsupported-first-only',targets:[{url:f.snapshot.url,tabId:1}],facts:[{id:'project_4_end',value:'至今',sourceRef:'synthetic:test'},{id:'project_5_end',value:'至今',sourceRef:'synthetic:test'}]});
 task=f.service.claim({taskId:task.taskId,expectedRevision:task.revision});task=(await f.service.observe({taskId:task.taskId})).task;
 const application=f.service.get(task.taskId).applications[0];application.operations.push({id:'first-picker-missing',state:'failed',index:0,error:'option_not_found:ongoing_date',evidence:[],actions:[{kind:'select',fieldRef:'end-4',factId:'project_4_end'},{kind:'select',fieldRef:'end-5',factId:'project_5_end'}],sourceSnapshot:structuredClone(application.snapshot)});f.service.store.write();
 assert.throws(()=>f.service.application_plan_review({taskId:task.taskId,expectedRevision:task.revision,items:[{factId:'project_5_end',disposition:'unsupported'}]}),/application_plan_unsupported_unproven/);
 const reviewed=f.service.application_plan_review({taskId:task.taskId,expectedRevision:task.revision,items:[{factId:'project_4_end',disposition:'unsupported'}]});
 const current=f.service.get(task.taskId).applications[0];assert.equal(reviewed.task.state,'running');assert.equal(current.obligationLedger.entries['fact:project_4_end'].status,'unsupported');assert.equal(current.obligationLedger.entries['fact:project_5_end'].status,'pending');
}));
