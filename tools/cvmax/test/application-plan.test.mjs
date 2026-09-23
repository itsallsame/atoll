import test from 'node:test';
import assert from 'node:assert/strict';
import {assessApplicationPlan,createApplicationPlan,createObligationLedger,factRecord,factSection,recordObligation} from '../src/application-plan.mjs';

const facts = [
  {id:'candidate.name',value:'甲',sourceRef:'synthetic:test'},
  ...Array.from({length:6},(_,index)=>({id:`project_${index+1}_name`,value:`项目${index+1}`,sourceRef:'synthetic:test'})),
  ...Array.from({length:3},(_,index)=>({id:`campus_${index+1}_role`,value:`校园${index+1}`,sourceRef:'synthetic:test'}))
];

test('application plan keeps every frozen fact and attachment independent of a Recipe',()=>{
 const plan=createApplicationPlan({facts,attachment:{resource:'saved:resume'}}),ledger=createObligationLedger(plan),assessment=assessApplicationPlan(plan,ledger);
 assert.equal(plan.obligations.length,11);
 assert.equal(assessment.total,11);assert.equal(assessment.resolved,0);assert.equal(assessment.complete,false);
 assert.equal(assessment.pending.filter(item=>item.section==='项目经历').length,6);
 assert.equal(assessment.pending.filter(item=>item.section==='校园经历').length,3);
 assert.equal(plan.obligations.some(item=>item.obligationId==='attachment:resume'),true);
});

test('record grouping supports variable-length resume collections',()=>{
 assert.equal(factSection('project_6_description'),'项目经历');assert.equal(factRecord('project_6_description'),'project:6');
 assert.equal(factSection('campus.3.role'),'校园经历');assert.equal(factRecord('campus.3.role'),'campus:3');
 assert.equal(factSection('candidate.name'),'基本信息');assert.equal(factRecord('candidate.name'),null);
 assert.equal(factSection('skills'),'语言能力');assert.equal(factRecord('skills'),null);
});

test('a plan completes only after every obligation has an evidence-backed conclusion',()=>{
 const plan=createApplicationPlan({facts:facts.slice(0,3)}),ledger=createObligationLedger(plan);
 for(const obligation of plan.obligations.slice(0,2))recordObligation(ledger,obligation.obligationId,'verified',{kind:'field_readback',observationId:'one'});
 assert.equal(assessApplicationPlan(plan,ledger).complete,false);
 const last=plan.obligations.at(-1);assert.throws(()=>recordObligation(ledger,last.obligationId,'site_no_field',null),/obligation_evidence_required/);
 recordObligation(ledger,last.obligationId,'site_no_field',{kind:'section_exhausted',section:last.section,observationId:'two'});
 assert.equal(assessApplicationPlan(plan,ledger).complete,true);
});

test('verified obligations cannot silently regress',()=>{
 const plan=createApplicationPlan({facts:facts.slice(0,1)}),ledger=createObligationLedger(plan),id=plan.obligations[0].obligationId;
 recordObligation(ledger,id,'verified',{kind:'field_readback',observationId:'one'});
 assert.throws(()=>recordObligation(ledger,id,'unsupported',{kind:'control_rejected',operationId:'two'}),/cannot_regress/);
 recordObligation(ledger,id,'pending',{kind:'readback_lost',observationId:'three'});
 assert.equal(ledger.entries[id].status,'pending');
});
