import test from 'node:test';
import assert from 'node:assert/strict';
import {prepareFacts,requireAuthorizedFacts} from '../src/facts.mjs';

test('facts keep exact sourced values and deduplicate identical extraction',()=>{
 const result=prepareFacts([{id:'profile.name',value:'  Lin Test  ',sourceRef:'file:resume.pdf#page=1'},{id:'profile.name',value:'Lin Test',sourceRef:'file:resume.pdf#page=1'}]);
 assert.deepEqual(result,{facts:[{id:'profile.name',value:'Lin Test',sourceRef:'file:resume.pdf#page=1'}],missing:[],conflicts:[]});
});

test('uncertain, empty and conflicting facts never enter the authorized snapshot',()=>{
 const result=prepareFacts([{id:'phone',value:'',sourceRef:'file:resume.pdf'},{id:'city',value:'Beijing',sourceRef:'file:resume.pdf'},{id:'city',value:'Shanghai',sourceRef:'user:answer'},{id:'salary',value:'20k',sourceRef:'model:guess',uncertain:true}]);
 assert.deepEqual(result.facts,[]);assert.deepEqual(result.missing.map(item=>item.id),['phone','salary']);assert.deepEqual(result.conflicts.map(item=>item.id),['city']);
 assert.throws(()=>requireAuthorizedFacts([{id:'salary',value:'20k',sourceRef:'model:guess',uncertain:true}]),/missing_or_conflicting/);
});

test('fact identifiers and provenance must be bounded and explicit',()=>{
 for(const record of [{id:'Invalid Name',value:'x',sourceRef:'user:answer'},{id:'name',value:'x',sourceRef:'unsourced'},{id:'name',value:'x'.repeat(10001),sourceRef:'user:answer'}])assert.throws(()=>prepareFacts([record]));
});
