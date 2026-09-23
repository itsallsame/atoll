import test from 'node:test';
import assert from 'node:assert/strict';
import {learningFor,retainObservation,beginEpisode,settleEpisode} from '../src/learning.mjs';
import {verifyEffect,buildCoverage} from '../src/verification.mjs';
import {interpret,inspectEvidence} from '../src/interpretation.mjs';
const page=()=>({url:'https://jobs.example/detail',documentEpoch:'d1',fields:[],controls:[],evidence:{schemaVersion:1,complete:true,interactionDigest:'entry',nodes:[],layers:[]}});
test('obsolete learning schemas are rejected instead of migrated',()=>{assert.throws(()=>learningFor({learning:{schemaVersion:3,mode:'user',activeMs:0,episodes:[],observations:[],candidates:{},unresolved:[]}}),/learning_schema_invalid/)});
test('unknown evidence is preserved and cannot be read across runs',()=>{
 const a={runId:'r1'},s=page();s.evidence.complete=false;
 const o=retainObservation(a,s);assert.equal(interpret(s,o).unresolved[0],'observation_incomplete');
 assert.equal(inspectEvidence(a,o.id).complete,false);a.runId='r2';assert.throws(()=>inspectEvidence(a,o.id),/current_run/);
});
test('independent ambiguous fields can share one bounded evidence inspection',()=>{
 const a={runId:'r1'},s=page();
 s.evidence.nodes=[{ref:'root',parentRef:null},{ref:'a',parentRef:'root'},{ref:'a-child',parentRef:'a'},{ref:'b',parentRef:'root'},{ref:'c',parentRef:'root'}];
 const o=retainObservation(a,s),batch=inspectEvidence(a,o.id,null,['a','b']);
 assert.deepEqual(batch.nodes.map(node=>node.ref),['root','a','a-child','b']);
 assert.throws(()=>inspectEvidence(a,o.id,null,['missing']),/evidence_ref_unknown/);
 assert.throws(()=>inspectEvidence(a,o.id,null,[]),/evidence_refs_invalid/);
});
test('learning survives serialization and scopes active-time limits to one intent',()=>{
 let a={runId:'r1',snapshot:page()};const o=retainObservation(a,a.snapshot);
 const e=beginEpisode(a,[{kind:'start',controlRef:'b'}],{question:'入口结果？',hypothesis:'打开表单',expected:'表单出现',observationId:o.id});
 settleEpisode(a,{episodeId:e.id,learningActiveMs:30_001},{status:'passed',reason:'observed_transition'});
 a=JSON.parse(JSON.stringify(a));
 assert.throws(()=>beginEpisode(a,[{kind:'start',controlRef:'b'}],{question:'入口结果？',hypothesis:'再点',expected:'表单出现',observationId:o.id}),/budget_exhausted/);
 assert.doesNotThrow(()=>beginEpisode(a,[{kind:'start',controlRef:'c'}],{question:'另一个？',hypothesis:'打开',expected:'表单',observationId:o.id}));
});
test('adding successive records has a new bounded intent after each verified field-count increase',()=>{
 const a={runId:'r1'},action={kind:'add',controlRef:'add',section:'校园经历',sectionKey:'heading:校园经历'},proposal=()=>beginEpisode(a,[action],{question:'新增记录？',hypothesis:'出现下一条',expected:'字段增加',observationId:a.observationId});
 for(const count of [5,10,15]){a.snapshot={...page(),fields:Array.from({length:count},(_,index)=>({ref:`f${index}`,group:'section:校园经历',label:'字段',type:'text',value:''})),controls:[{ref:'add',label:'+ 添加',role:'button',tag:'button',type:'button'}]};retainObservation(a,a.snapshot);const episode=proposal();settleEpisode(a,{episodeId:episode.id,learningActiveMs:100},{status:'passed',reason:'fields_added'})}
 assert.equal(a.learning.episodes.length,3);
});
test('an executor protocol upgrade is a new bounded strategy, not a blind retry',()=>{
 const a={runId:'r1',snapshot:{...page(),extensionVersion:'0.2.6'}};let o=retainObservation(a,a.snapshot);
 const first=beginEpisode(a,[{kind:'fill',fieldRef:'name',factId:'name'}],{question:'填写？',hypothesis:'旧执行器',expected:'保留',observationId:o.id});settleEpisode(a,{episodeId:first.id},{status:'unknown',reason:'transport_unknown'});
 a.snapshot={...a.snapshot,extensionVersion:'0.2.7'};o=retainObservation(a,a.snapshot);
 assert.doesNotThrow(()=>beginEpisode(a,[{kind:'fill',fieldRef:'name',factId:'name'}],{question:'填写？',hypothesis:'新执行器',expected:'保留',observationId:o.id}));
});
test('a different bounded activation primitive is a distinct learning strategy',()=>{
 const snapshot={...page(),activeDialog:{ref:'dialog'},controls:[],interactions:[{ref:'edit',label:'编辑',role:'',tag:'p',type:'',surface:'dialog',safe:true}]},a={runId:'r1',snapshot};const o=retainObservation(a,snapshot),base={kind:'activate',controlRef:'edit',expectedPostcondition:'surface_changed',purpose:'resume_entry'};
 const first=beginEpisode(a,[{...base,activationMode:'main_world_pointer'}],{question:'编辑？',hypothesis:'代理点击',expected:'进入编辑页',observationId:o.id});settleEpisode(a,{episodeId:first.id},{status:'failed',reason:'no_effect'});
 assert.doesNotThrow(()=>beginEpisode(a,[{...base,activationMode:'isolated_pointer'}],{question:'编辑？',hypothesis:'隔离指针',expected:'进入编辑页',observationId:o.id}));
});
test('the first learning hypothesis is not consumed by agent queue or reasoning time',()=>{
 const a={runId:'r1',snapshot:page(),budget:{activeMs:120000,activeSince:1,limits:{activeMs:600000}}};
 const o=retainObservation(a,a.snapshot);
 const e=beginEpisode(a,[{kind:'start',controlRef:'b'}],{question:'入口结果？',hypothesis:'打开表单',expected:'表单出现',observationId:o.id});
 assert.equal(e.verdict,'pending');assert.equal(a.learning.activeMs,0);
 settleEpisode(a,{episodeId:e.id,learningActiveMs:50},{status:'passed',reason:'observed_transition'});
 assert.ok(a.learning.activeMs>=40);assert.ok(a.learning.activeMs<1000);
});
test('an unknown browser outcome blocks the same hypothesis without consuming learning time',()=>{
 const a={runId:'r1',snapshot:page()};const o=retainObservation(a,a.snapshot),e=beginEpisode(a,[{kind:'fill',fieldRef:'name',factId:'name'}],{question:'姓名能否保留？',hypothesis:'字段接受姓名',expected:'回读一致',observationId:o.id});
 e.createdAt=new Date(Date.now()-31_000).toISOString();settleEpisode(a,{episodeId:e.id},{status:'unknown',reason:'execution_error'});
 assert.equal(a.learning.activeMs,0);assert.equal(e.verdict,'unknown');
 assert.throws(()=>beginEpisode(a,[{kind:'fill',fieldRef:'name',factId:'name'}],{question:'重试？',hypothesis:'再次填写',expected:'回读一致',observationId:o.id}),/no_progress/);
});
test('a cancelled pending hypothesis does not consume learning time',()=>{
 const a={runId:'r1',snapshot:page()};const o=retainObservation(a,a.snapshot),proposal=()=>beginEpisode(a,[{kind:'fill',fieldRef:'name',factId:'name'}],{question:'姓名能否保留？',hypothesis:'字段接受姓名',expected:'回读一致',observationId:o.id});
 for(let index=0;index<3;index++){const e=proposal();e.createdAt=new Date(Date.now()-31_000).toISOString();e.completedAt=new Date().toISOString();e.verdict='cancelled'}
 assert.equal(learningFor(a).activeMs,0);assert.doesNotThrow(proposal);
});
test('agent delay between browser receipt and verification does not consume learning time',()=>{
 const a={runId:'r1',snapshot:page()};const o=retainObservation(a,a.snapshot),e=beginEpisode(a,[{kind:'fill',fieldRef:'name',factId:'name'}],{question:'姓名能否保留？',hypothesis:'字段接受姓名',expected:'回读一致',observationId:o.id});
 e.createdAt=new Date(Date.now()-31_000).toISOString();settleEpisode(a,{episodeId:e.id,learningActiveMs:40},{status:'passed',reason:'retained_value'});
 assert.equal(learningFor(a).activeMs,40);
});
test('document reload alone and truncated observations cannot prove progress',()=>{
 const before=page(),after={...page(),documentEpoch:'d2'};
 assert.equal(verifyEffect(before,after,[{kind:'start'}]).reason,'no_effect');
 after.evidence={...after.evidence,complete:false};assert.equal(verifyEffect(before,after,[{kind:'start'}]).status,'unknown');
});
test('an action-specific postcondition can prove progress on a structurally incomplete large page',()=>{
 const before=page(),after=page();before.activeDialog={ref:'replace-resume'};after.evidence={...after.evidence,complete:false};
 assert.equal(verifyEffect(before,after,[{kind:'next',resumeReplacement:true}],()=>true).reason,'action_postcondition_verified');
 assert.equal(verifyEffect(before,after,[{kind:'next',resumeReplacement:true}],()=>false).reason,'incomplete_evidence');
});
test('custom new interaction is distinguished from unchanged structure',()=>{
 const before=page(),after=page();after.evidence.interactionDigest='certification';
 assert.equal(verifyEffect(before,after,[{kind:'start'}]).reason,'observed_transition');
 after.activeDialog={ref:'surface'};assert.equal(verifyEffect(before,after,[{kind:'start'}]).reason,'interaction_surface_observed');
});
test('retained values prove a same-structure effect and coverage discovers unplanned fields',()=>{
 const before=page(),after=page();after.fields=[{ref:'name',semanticId:'name',label:'姓名',type:'text',value:'测试'},{ref:'edu',label:'教育经历',type:'text',value:''}];
 assert.equal(verifyEffect(before,after,[{kind:'fill'}],()=>true).status,'passed');
 const coverage=buildCoverage(after,[{id:'name',value:'测试'}]);assert.equal(coverage.length,2);assert.equal(coverage[1].disposition,'unreviewed');
});

test('coverage keeps repeated fields distinct even without a detected group',()=>{
 const s=page();s.fields=[{ref:'one',label:'学校',type:'text',value:''},{ref:'two',label:'学校',type:'text',value:''}];
 assert.equal(buildCoverage(s,[]).length,2);
});

test('coverage does not retain a fact mapping after interpretation is withdrawn',()=>{
 const mapped={url:'https://jobs.example.test/apply',fields:[{ref:'f1',label:'籍贯',group:'基本信息',type:'search',semanticId:'native_place',value:''}]};
 const prior=buildCoverage(mapped,[{id:'native_place',value:'测试'}]);
 const withdrawn=buildCoverage({...mapped,fields:[{...mapped.fields[0],semanticId:null}]},[{id:'native_place',value:'测试'}],prior);
 assert.equal(withdrawn[0].semanticId,null);assert.equal(withdrawn[0].factId,null);assert.equal(withdrawn[0].disposition,'unreviewed');
});

test('a reviewed website-only field remains reviewed across observations',()=>{const snapshot={url:'https://jobs.example.test/apply',fields:[{ref:'f1',label:'请输入',group:'section:基本信息',type:'text',semanticId:'idNo',value:''}]},prior=buildCoverage(snapshot,[]);prior[0].disposition='no_resume_fact';const repeated=buildCoverage({...snapshot,fields:[{...snapshot.fields[0],ref:'f2'}]},[],prior);assert.equal(repeated[0].disposition,'no_resume_fact');const changed=buildCoverage({...snapshot,fields:[{...snapshot.fields[0],semanticId:'differentQuestion'}]},[],prior);assert.equal(changed.at(-1).disposition,'unreviewed')});

test('an unsupported invented action cannot leave a pending learning episode',()=>{
 const a={runId:'r1',snapshot:page()};const o=retainObservation(a,a.snapshot);
 assert.throws(()=>beginEpisode(a,[{kind:'scroll_into_view',controlRef:'button'}],{question:'检查？',hypothesis:'滚动',expected:'可见',observationId:o.id}),/learning_action_unsupported/);
 assert.equal(a.learning.episodes.length,0);
});

test('new runtime refs and unrelated navigation do not prove the expected effect',()=>{
 const before=page();before.controls=[{ref:'old',label:'下一步',kind:'next'}];const after=structuredClone(before);after.documentEpoch='new';after.controls[0].ref='new';
 assert.equal(verifyEffect(before,after,[{kind:'next'}]).reason,'no_effect');
 before.activeDialog={ref:'dialog'};after.activeDialog={ref:'changed-dialog'};after.evidence.interactionDigest='other';
 assert.equal(verifyEffect(before,after,[{kind:'activate',expectedPostcondition:'dialog_closed'}]).status,'unknown');
 const destination={...page(),url:'https://jobs.example/error'};
 assert.equal(verifyEffect(page(),destination,[{kind:'start'}]).reason,'uninterpreted_destination');
});
