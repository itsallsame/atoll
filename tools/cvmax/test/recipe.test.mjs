import test from 'node:test';
import assert from 'node:assert/strict';
import {markRecipeTerminal,mergeRecipes,publicRecipe,recipeTerminal,recordVerifiedTransition,routeTemplate,selectRecipe,stateFor,systemFor} from '../src/recipe.mjs';

const page=(id,value='')=>({url:`https://jobs.example.test/position/${id}/apply?spread=tracking`,formStage:'apply',pageState:'normal',fields:[{ref:`runtime-${id}`,label:'姓名 *',semanticId:`formily-${id}`,group:'基本信息',type:'text',value}],controls:[],interactions:[]});

test('recipe milestone removes job ids and ignores field values',()=>{
 assert.equal(routeTemplate(page('123456').url),'/position/:id/apply');
 assert.equal(systemFor(page('123456')).key,systemFor(page('987654')).key);
 assert.equal(stateFor(page('123456')).key,stateFor(page('123456','已填写')).key);
});

test('Moka recipes use one engine identity and normalized tenant route across company domains',()=>{
 const catl={...page('123456'),url:'https://app.mokahr.com/campus-recruitment/catlhr/148948#/job/b1d44938-44ef-4614-a829-aff7b07f5d75'};
 const anta={...page('987654'),url:'https://jobs.anta.com/campus-recruitment/antahr/142914#/job/42449b0f-37f3-4c47-b5bf-231c44e08b81'};
 assert.equal(systemFor(catl).engine,'moka');assert.equal(systemFor(catl).key,systemFor(anta).key);
 assert.equal(routeTemplate(catl.url),'/campus-recruitment/:tenant/:id#/job/:id');
 assert.equal(stateFor(catl).key,stateFor(anta).key);
 const recipes={},target={...structuredClone(catl),fields:[{...catl.fields[0],value:'测试姓名'}]};
 recordVerifiedTransition(recipes,{sourceSnapshot:catl,targetSnapshot:target,runId:'catl-run',actions:[{kind:'fill',fieldRef:catl.fields[0].ref,factId:'candidate.name'}]});
 const match=selectRecipe(recipes,anta,{facts:[{id:'candidate.name',value:'另一姓名'}]});
 assert.equal(match.status,'reusable');assert.equal(match.actions[0].fieldRef,anta.fields[0].ref);
});

test('runtime interaction text does not split one durable workflow state',()=>{
 const choosing=page('123456'),uploaded=page('987654');
 choosing.interactions=[{ref:'choose',label:'选择文件',role:'button',tag:'button',type:'button',surface:'page',safe:true}];
 uploaded.interactions=[{ref:'uploaded',label:'2.pdf 上次上传: 2026-09-11 更新 删除',role:'button',tag:'span',surface:'page',safe:true}];
 assert.equal(stateFor(choosing).key,stateFor(uploaded).key);
});

test('volatile and private dialog copy does not split or leak a workflow state',()=>{
 const first={...page('123456'),formStage:'detail',pageState:'resume_prerequisite',fields:[],activeDialog:{role:'dialog',title:'更新时间：2026-09-11 12:14:05',text:'张三的简历，完整度 55%，请编辑'},controls:[{ref:'edit-a',kind:'start',label:'编辑',safe:true}]};
 const second={...page('987654'),formStage:'detail',pageState:'resume_prerequisite',fields:[],activeDialog:{role:'dialog',title:'更新时间：2026-09-12 09:30:11',text:'李四的简历，完整度 55%，请编辑'},controls:[{ref:'edit-b',kind:'start',label:'编辑',safe:true}]};
 const a=stateFor(first),b=stateFor(second),encoded=JSON.stringify(a.descriptor);
 assert.equal(a.key,b.key);assert.equal(a.descriptor.dialog.kind,'resume_prerequisite');assert.equal(encoded.includes('张三'),false);assert.equal(encoded.includes('2026-09-11'),false);
});

test('one verified transition is immediately reusable on another job in the same system',()=>{
 const recipes={},source=page('123456'),target=page('123456','测试姓名');
 recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'run-one',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name'}]});
 const current=page('987654'),match=selectRecipe(recipes,current,{facts:[{id:'candidate.name',value:'另一姓名'}]});
 assert.equal(match.status,'reusable');
 assert.deepEqual(match.actions.map(action=>[action.kind,action.fieldRef,action.factId]),[['fill','runtime-987654','candidate.name']]);
 assert.equal('similarity' in match,false);
});

test('repeated structural peers bind by their recorded ordinal even when semantics make each exact match unique',()=>{
 const source=page('123456');source.fields=[
  {ref:'start-a',label:'起止时间*',semanticId:'education_1_start',group:'education',type:'text',value:''},
  {ref:'end-a',label:'起止时间*',semanticId:'education_1_end',group:'education',type:'text',value:''}
 ];
 const target=structuredClone(source);target.fields[0].value='2020-09';target.fields[1].value='2024-06';
 const recipes={};recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'dates-one',actions:[{kind:'fill',fieldRef:'start-a',factId:'education_1_start'},{kind:'fill',fieldRef:'end-a',factId:'education_1_end'}]});
 const current=structuredClone(source);current.url=page('987654').url;current.fields[0].ref='start-b';current.fields[1].ref='end-b';
 const match=selectRecipe(recipes,current,{facts:[{id:'education_1_start',value:'2021-09'},{id:'education_1_end',value:'2025-06'}]});
 assert.equal(match.status,'reusable');assert.deepEqual(match.actions.map(action=>action.fieldRef),['start-b','end-b']);
});

test('runtime guards remove an inapplicable outgoing transition before deterministic selection',()=>{
 const recipes={},source=page('123456'),target=page('123456','测试姓名');
 recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'run-one',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name'}]});
 const blocked=selectRecipe(recipes,source,{facts:[{id:'candidate.name',value:'测试姓名'}],transitionAllowed:()=>false});
 assert.equal(blocked.status,'guarded_transition_unavailable');assert.deepEqual(blocked.actions,[]);
});

test('an unrelated site field cannot invalidate a locally guarded step',()=>{
 const recipes={},source=page('123456'),target=page('123456','测试姓名');
 recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'run-one',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name'}]});
 const changed=page('987654');changed.fields.push({ref:'city',label:'城市',semanticId:'city',type:'text',value:''});
 assert.equal(selectRecipe(recipes,changed,{facts:[{id:'candidate.name',value:'姓名'}]}).status,'reusable');
});

test('semantic interpretation changes cannot invalidate the same structural step',()=>{
 const recipes={},source=page('123456'),target=page('123456','测试姓名');
 recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'raw-id',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'name'}]});
 const interpreted=page('987654');interpreted.fields[0].semanticId='name';
 const match=selectRecipe(recipes,interpreted,{facts:[{id:'name',value:'另一姓名'}]});
 assert.equal(match.status,'reusable');
 assert.equal(match.actions[0].fieldRef,'runtime-987654');
 const recipe=Object.values(recipes)[0],encoded=JSON.stringify({states:recipe.states,transitions:Object.values(recipe.transitions).map(({guard,actions})=>({guard,actions}))});
 assert.equal(encoded.includes('semanticId'),false);
});

test('a state-changing workflow edge takes precedence over a corrective self-loop',()=>{
 const recipes={},source=page('123456'),target=page('123456','测试姓名');
 recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'seed',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name'}]});
 const recipe=recipes[systemFor(source).key],selfLoop=Object.values(recipe.transitions)[0];selfLoop.to=selfLoop.from;
 const progress={...structuredClone(selfLoop),id:'progress-transition',to:'different-state'};recipe.transitions[progress.id]=progress;
 const match=selectRecipe(recipes,source,{facts:[{id:'candidate.name',value:'测试姓名'}]});
 assert.equal(match.status,'reusable');assert.equal(match.transition.id,progress.id);
});

test('distinct field-only self-loops replay in their first verified order',()=>{
 const recipes={},source=page('123456');source.fields.push({ref:'runtime-email',label:'邮箱 *',semanticId:'candidate.email',group:'基本信息',type:'text',value:''});
 const named=structuredClone(source);named.fields[0].value='测试姓名';
 const emailed=structuredClone(source);emailed.fields[1].value='test@example.com';
 recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:named,runId:'name-first',predecessorKind:'fill',actions:[{kind:'fill',fieldRef:'runtime-123456',factId:'candidate.name'}]});
 recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:emailed,runId:'email-second',predecessorKind:'fill',actions:[{kind:'fill',fieldRef:'runtime-email',factId:'candidate.email'}]});
 const recipe=recipes[systemFor(source).key],transitions=Object.values(recipe.transitions),name=transitions.find(item=>item.actions[0].factId==='candidate.name'),email=transitions.find(item=>item.actions[0].factId==='candidate.email');
 name.firstVerifiedAt='2026-09-20T01:00:00.000Z';email.firstVerifiedAt='2026-09-20T01:00:01.000Z';
 const facts=[{id:'candidate.name',value:'另一姓名'},{id:'candidate.email',value:'other@example.com'}];
 let match=selectRecipe(recipes,source,{predecessorKind:'fill',facts,actionNeeded:action=>action.factId!=='nothing'});
 assert.equal(match.status,'reusable');assert.equal(match.transition.id,name.id);
 match=selectRecipe(recipes,named,{predecessorKind:'fill',facts,actionNeeded:action=>action.factId!=='candidate.name'});
 assert.equal(match.status,'reusable');assert.equal(match.transition.id,email.id);
});

test('terminal states end a recipe without inventing another action',()=>{
 const recipes={},source=page('123456'),target=page('123456','测试姓名');
 recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'run-one',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name'}]});
 markRecipeTerminal(recipes,target,{coverageFactIds:['candidate.name']});
 const recipe=recipes[systemFor(source).key],terminal=Object.values(recipe.terminals)[0];assert.equal(terminal.coverageVersion,3);
 assert.deepEqual(terminal.coverageFactIds,['candidate.name']);assert.ok(recipeTerminal(recipes,target,{processedFactIds:['candidate.name']}));
});

test('terminal identity ignores value population and transient control focus',()=>{
 const recipes={},source=page('123456'),target=page('123456','测试姓名');
 target.controls=[{ref:'section-a',kind:'section',label:'基本信息',section:'基本信息',safe:true,active:true}];
 recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'run-one',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name'}]});
 markRecipeTerminal(recipes,target,{coverageFactIds:['candidate.name']});
 const revisited=structuredClone(target);revisited.fields[0].value='另一个人的姓名';revisited.controls[0].active=false;
 assert.equal(recipeTerminal(recipes,revisited),null);assert.ok(recipeTerminal(recipes,revisited,{processedFactIds:['candidate.name']}));
});

test('terminal identity does not depend on the action used to reach the verified page',()=>{
 const recipes={},source=page('123456'),target=page('123456','测试姓名');
 recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'run-one',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name'}]});
 markRecipeTerminal(recipes,target,{predecessorKind:'fill',coverageFactIds:['candidate.name']});
 assert.ok(recipeTerminal(recipes,target,{predecessorKind:'upload',processedFactIds:['candidate.name']}));
 assert.ok(recipeTerminal(recipes,target,{predecessorKind:'native_parse',processedFactIds:['candidate.name']}));
 const recipe=recipes[systemFor(target).key],canonicalKey=Object.keys(recipe.terminals)[0],historicalKey='f'.repeat(64);
 recipe.states[historicalKey]={...recipe.states[canonicalKey],predecessorKind:'fill'};recipe.terminals[historicalKey]=recipe.terminals[canonicalKey];delete recipe.states[canonicalKey];delete recipe.terminals[canonicalKey];
 assert.ok(recipeTerminal(recipes,target,{predecessorKind:'upload',processedFactIds:['candidate.name']}));
});

test('merge preserves terminal states from a newly learned public bundle',()=>{
 const base={},incoming={},source=page('123456'),target=page('123456','测试姓名');
 recordVerifiedTransition(base,{sourceSnapshot:source,targetSnapshot:target,runId:'one',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name'}]});
 recordVerifiedTransition(incoming,{sourceSnapshot:source,targetSnapshot:target,runId:'two',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name'}]});markRecipeTerminal(incoming,target,{coverageFactIds:['candidate.name']});
 const merged=mergeRecipes(base,incoming);assert.ok(recipeTerminal(merged,target,{processedFactIds:['candidate.name']}));
});

test('terminal markers without a coverage review are ignored',()=>{
 const recipes={},source=page('123456'),target=page('123456','测试姓名');
 recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'run-one',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name'}]});
 const recipe=recipes[systemFor(source).key],state=stateFor(target);recipe.terminals={[state.key]:{status:'ready_for_review'}};
 assert.equal(recipeTerminal(recipes,target),null);
 assert.equal(recipe.terminals[state.key].coverageVersion,undefined);
});

test('public recipes contain anchors and fact ids but no private values',()=>{
 const recipes={},source=page('123456'),target=page('123456','绝密姓名');
 const recipe=recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:target,runId:'run-one',actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'candidate.name',value:'绝密姓名',before:''}]});
 const published=publicRecipe(recipe),encoded=JSON.stringify(published);
 assert.equal(encoded.includes('绝密姓名'),false);assert.equal(encoded.includes('candidate.name'),true);
});

test('recipe refresh merges graph nodes and cannot erase an earlier transition',()=>{
 const first={},second={},a=page('123456'),b=page('123456','姓名');
 recordVerifiedTransition(first,{sourceSnapshot:a,targetSnapshot:b,runId:'one',actions:[{kind:'fill',fieldRef:a.fields[0].ref,factId:'candidate.name'}]});
 const b2=structuredClone(b);b2.controls=[{ref:'education',kind:'section',label:'教育经历',section:'教育经历',safe:true,active:false}];
 const c=structuredClone(b2);c.controls[0].active=true;c.activeSection='教育经历';
 recordVerifiedTransition(second,{sourceSnapshot:b2,targetSnapshot:c,runId:'two',actions:[{kind:'section',controlRef:'education',section:'教育经历'}]});
 const merged=mergeRecipes(first,second),recipe=Object.values(merged)[0];assert.equal(Object.keys(recipe.transitions).length,2);
});

test('one action can retain distinct verified result branches without overwriting',()=>{
 const recipes={},source={...page('123456'),fields:[],formStage:'detail',controls:[{ref:'apply',kind:'start',label:'申请职位',safe:true}]};
 const login={...source,url:'https://jobs.example.test/login',loginRequired:true};
 const form=page('123456');
 const actions=[{kind:'start',controlRef:'apply'}];
 recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:login,actions,runId:'login-run'});
 recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:form,actions,runId:'form-run'});
 const match=selectRecipe(recipes,source);
 assert.equal(match.status,'reusable');assert.equal(match.transition.expectedStates.length,2);
 assert.equal(Object.keys(match.recipe.transitions).length,2);
});
test('a duplicated runtime anchor cannot reuse a transition recorded with one target',()=>{
 const recipes={},source=page('123456');recordVerifiedTransition(recipes,{sourceSnapshot:source,targetSnapshot:page('123456','姓名'),actions:[{kind:'fill',fieldRef:source.fields[0].ref,factId:'name'}],runId:'r'});
 const duplicate=structuredClone(source);duplicate.fields.push({...duplicate.fields[0],ref:'duplicate'});
 assert.notEqual(selectRecipe(recipes,duplicate,{facts:[{id:'name',value:'姓名'}]}).status,'reusable');
});

test('public projection rejects extra private payload instead of blindly signing it',()=>{
 const recipe=recordVerifiedTransition({}, {sourceSnapshot:page('123456'),targetSnapshot:page('123456','姓名'),runId:'private',actions:[{kind:'fill',fieldRef:'runtime-123456',factId:'name'}]});
 recipe.privateObservation={text:'私人资料'};
 assert.throws(()=>publicRecipe(recipe),/private_recipe_value/);
});

test('nested anchors and verification metadata cannot smuggle private observation data',()=>{
 const recipe=recordVerifiedTransition({}, {sourceSnapshot:page('123456'),targetSnapshot:page('123456','姓名'),runId:'private',actions:[{kind:'fill',fieldRef:'runtime-123456',factId:'name'}]});
 const edge=Object.values(recipe.transitions)[0];edge.actions[0].fieldAnchor.note='私人内容';assert.throws(()=>publicRecipe(recipe),/private_recipe_value/);
 delete edge.actions[0].fieldAnchor.note;edge.verification={version:3,rawDOM:'私人内容'};assert.throws(()=>publicRecipe(recipe),/private_recipe_value/);
});
