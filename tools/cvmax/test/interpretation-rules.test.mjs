import test from 'node:test';import assert from 'node:assert/strict';
import {proposeRuleRevision,proposeRules,applyRules,mergeRules,validateRules,extendRulesForVerifiedAppend} from '../src/interpretation-rules.mjs';
const field={ref:'x',label:'姓名',type:'text',value:'',semanticId:null};
test('interpretation cannot grant permissions, invent facts, or resolve ambiguous targets by guessing',()=>{
 assert.throws(()=>proposeRules({rawFields:[field]},[{kind:'field',ref:'x',semanticId:'name'}],[]),/fact_unknown/);
 assert.throws(()=>proposeRules({interactions:[{ref:'one',label:'继续',safe:true},{ref:'two',label:'继续',safe:true}]},[{kind:'interaction',ref:'one'}],[]),/ambiguous/);
 assert.throws(()=>proposeRules({interactions:[{ref:'pay',label:'支付',safe:true}]},[{kind:'interaction',ref:'pay'}],[]),/forbidden/);
 assert.throws(()=>validateRules([{kind:'interaction',anchor:'a'.repeat(64),javascript:'alert(1)'}]),/invalid/);
});
test('repeated fields bind by exact cardinality and ordinal without weakening interaction safety',()=>{
 const repeated=[{...field,ref:'first'},{...field,ref:'second'}],rules=proposeRules({rawFields:repeated},[{kind:'field',ref:'first',semanticId:'name'},{kind:'field',ref:'second',semanticId:'email'}],[{id:'name'},{id:'email'}]);
 assert.deepEqual(rules.map(rule=>[rule.ordinal,rule.cardinality]),[[0,2],[1,2]]);const rebound=applyRules({rawFields:repeated.map((item,index)=>({...item,ref:`new-${index}`})),fields:[]},rules);
 assert.deepEqual(rebound.fields.map(item=>item.semanticId),['name','email']);assert.equal(applyRules({rawFields:[...repeated,{...field,ref:'third'}],fields:[]},rules).fields.length,0);
 assert.equal(mergeRules([rules[0]],[rules[1]]).length,2);
});
test('verified same-document append carries prior repeated bindings only when existing peers stay in place',()=>{
 const peers=[{...field,ref:'first',value:'甲'},{...field,ref:'second',value:'乙'}],before={documentEpoch:'same',rawFields:peers},rules=proposeRules(before,[{kind:'field',ref:'first',semanticId:'name'},{kind:'field',ref:'second',semanticId:'email'}],[{id:'name'},{id:'email'}]),after={documentEpoch:'same',rawFields:[...peers,{...field,ref:'third'}]},extended=extendRulesForVerifiedAppend(before,after,rules);
 assert.deepEqual(extended.map(rule=>rule.cardinality),[3,3]);assert.deepEqual(applyRules({...after,fields:[]},extended).fields.slice(0,2).map(item=>item.semanticId),['name','email']);
 assert.deepEqual(extendRulesForVerifiedAppend(before,{...after,rawFields:[{...field,ref:'third'},...peers]},rules),rules);
 assert.deepEqual(extendRulesForVerifiedAppend(before,{...after,documentEpoch:'new'},rules),rules);
 assert.deepEqual(extendRulesForVerifiedAppend(before,{...after,rawFields:[...peers,{...field,ref:'third',value:'已存在'}]},rules),rules);
});
test('verified append promotes a formerly unique field without changing stale rules for the same anchor',()=>{
 const first={...field,ref:'first',value:'已有内容'},before={documentEpoch:'doc',rawFields:[first]},after={documentEpoch:'doc',rawFields:[first,{...field,ref:'second'}]},unique=proposeRules(before,[{kind:'field',ref:'first',semanticId:'name'}],[{id:'name'}])[0],stale={...unique,ordinal:3,cardinality:4,semanticId:'email'},expanded=extendRulesForVerifiedAppend(before,after,[unique,stale]);
 assert.deepEqual(expanded.map(rule=>[rule.ordinal,rule.cardinality]),[[0,2],[3,4]]);assert.equal(applyRules({...after,fields:[]},expanded).fields[0].semanticId,'name');
});
test('nearby structural context separates generic fields without storing the label in a rule',()=>{
 const contextual=[{...field,ref:'school',label:'请输入',anchorContext:'学校名称'},{...field,ref:'skill',label:'请输入',anchorContext:'技能类型'}],rules=proposeRules({rawFields:contextual},[{kind:'field',ref:'school',semanticId:'school'},{kind:'field',ref:'skill',semanticId:'skill'}],[{id:'school'},{id:'skill'}]);
 assert.equal(rules.every(rule=>rule.ordinal==null&&rule.cardinality==null),true);assert.equal(JSON.stringify(rules).includes('学校名称'),false);const rebound=applyRules({rawFields:contextual.map(item=>({...item,ref:`new-${item.ref}`})),fields:[]},rules);assert.deepEqual(rebound.fields.map(item=>item.semanticId),['school','skill']);
});
test('mapping data rebinds only unique matching evidence and rejects semantic conflicts',()=>{
 const rules=proposeRules({rawFields:[field]},[{kind:'field',ref:'x',semanticId:'name'}],[{id:'name'}]);
 assert.equal(applyRules({rawFields:[{...field,ref:'another'}],fields:[]},rules).fields[0].semanticId,'name');
 assert.equal(applyRules({rawFields:[field,{...field,ref:'y'}],fields:[]},rules).fields.length,0);
 assert.throws(()=>mergeRules(rules,[{...rules[0],semanticId:'email'}]),/conflict/);
});
test('an explicit removal identifies the exact evidence rule and rematerializes raw fields',()=>{
 const snapshot={rawFields:[field],fields:[{...field,semanticId:'name'}]},rules=proposeRules(snapshot,[{kind:'field',ref:'x',semanticId:'name'}],[{id:'name'}]);
 const revision=proposeRuleRevision(snapshot,[{kind:'field',ref:'x',remove:true}],[{id:'name'}]);
 assert.equal(revision.rules.length,0);assert.deepEqual(revision.removals.map(({ref,...rule})=>rule),rules.map(({semanticId,...rule})=>rule));
 assert.equal(applyRules({...snapshot,fields:structuredClone(snapshot.rawFields)},[]).fields[0].semanticId,null);
});
