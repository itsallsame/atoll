import {publicCandidateKey} from '../src/page-model.mjs';
import test from 'node:test';
import assert from 'node:assert/strict';
import {applyPageModel,learnedPageModel,mergePageModels,publicPageModel} from '../src/page-model.mjs';

const snapshot=()=>({url:'https://app.mokahr.com/campus-recruitment/acme/12345#/job/abc/apply',controls:[
 {ref:'education',kind:'add',label:'添加',safe:true,section:null,sectionKey:null,sectionCandidates:[{key:'heading:教育背景',label:'教育背景',source:'heading'}]},
 {ref:'project',kind:'add',label:'添加',safe:true,section:null,sectionKey:null,sectionCandidates:[{key:'heading:是否有项目经验',label:'是否有项目经验',source:'heading'},{key:'heading:项目经验',label:'项目经验',source:'heading'}]},
]});

test('a signed page model maps raw structure outside the extension',()=>{
 const source=snapshot();source.controls.push({ref:'basic-tab',kind:'section',label:'基本信息',active:true,safe:true,section:null,sectionKey:null,sectionCandidates:[{key:'navigation:基本信息',label:'基本信息',source:'navigation'}]});
 const model={formatVersion:1,mappings:{'heading:教育背景':'教育经历','heading:项目经验':'项目经历','navigation:基本信息':'基本信息'}},mapped=applyPageModel(source,model);
 assert.deepEqual(mapped.controls.map(item=>[item.ref,item.section,item.sectionKey,item.safe]),[
  ['education','教育经历','heading:教育背景',true],['project','项目经历','heading:项目经验',true],['basic-tab','基本信息','navigation:基本信息',true],
 ]);
 assert.equal(mapped.activeSection,'基本信息');
 assert.equal(mapped.activeSectionKey,'navigation:基本信息');
});

test('a verified cold add produces a public mapping candidate',()=>{
 const learned=learnedPageModel(snapshot(),[{kind:'add',controlRef:'education',section:'教育经历',sectionKey:'heading:教育背景'}]);
 assert.deepEqual(learned,{formatVersion:1,mappings:{[publicCandidateKey('heading:教育背景')]:'教育经历'}});
 assert.deepEqual(publicPageModel(learned),learned);
 assert.throws(()=>publicPageModel({formatVersion:1,mappings:{'heading:13800138000':'教育经历'}}),/private_or_volatile/);
});

test('conflicting public mappings fail closed',()=>{
 const merged=mergePageModels({formatVersion:1,mappings:{'heading:项目经验':'项目经历'}},{formatVersion:1,mappings:{'heading:项目经验':'实习经历'}});
 assert.deepEqual(merged.mappings,{});assert.deepEqual(merged.conflicts,[publicCandidateKey('heading:项目经验')]);
});

test('untrusted runtime section candidates with unknown key formats are ignored',()=>{
 const model={formatVersion:1,mappings:{'heading:教育经历':'教育经历'}};
 const snapshot={controls:[{ref:'add-1',kind:'add',safe:true,sectionCandidates:[{key:'runtime-layout-candidate',label:'噪声'},{key:'heading:教育经历',label:'教育经历'}]}]};
 const result=applyPageModel(snapshot,model);
 assert.deepEqual([result.controls[0].section,result.controls[0].sectionKey,result.controls[0].safe],['教育经历','heading:教育经历',true]);
 const onlyUnknown=applyPageModel({controls:[{ref:'add-2',kind:'add',safe:true,sectionCandidates:[{key:'runtime-layout-candidate',label:'噪声'}]}]},model);
 assert.deepEqual([onlyUnknown.controls[0].section,onlyUnknown.controls[0].sectionKey,onlyUnknown.controls[0].safe],[null,null,false]);
});
