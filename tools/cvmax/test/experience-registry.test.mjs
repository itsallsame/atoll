import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import {generateKeyPairSync} from 'node:crypto';
import {ExperienceRegistryApp} from '../src/experience-registry-app.mjs';
import {ExperienceRegistryClient} from '../src/experience-registry-client.mjs';
import {signExperienceRelease} from '../src/experience-release.mjs';

const familyKey='a'.repeat(64),fingerprint='b'.repeat(64),strategyId='c'.repeat(64);
const candidate=()=>({formatVersion:2,family:{key:familyKey,origin:'https://jobs.test',host:'jobs.test',routeTemplate:'/jobs/:id/apply',stage:'apply'},status:'candidate',versions:{[fingerprint]:{fieldShape:[],strategyIds:[strategyId],successfulRuns:['private-run']}},strategies:{[strategyId]:{id:strategyId,kind:'fill',factId:'name',anchor:{label:'姓名',type:'text'},status:'reusable',successfulRuns:['private-run']}}});

test('registry promotes only after independent installations and signs the public release',async()=>{
 const {privateKey,publicKey}=generateKeyPairSync('ed25519'),feedback=[],published=[];
 let releaseStatus='candidate';const repository={async saveCandidate(){return{releaseId:7,status:releaseStatus}},async recordFeedback(value){feedback.push(value);return{accepted:true}},async releaseStats(){return{status:releaseStatus,independentInstallations:new Set(feedback.map(x=>x.installationHash)).size,verifiedRuns:feedback.length,failedRuns:0}},async updateReleaseStats(){},async setReleaseStatus(_id,status){releaseStatus=status},async releaseBundle(){return candidate()},async publishRelease(id,bundle,signed,stats){releaseStatus='released';published.push({id,bundle,signed,stats})},async releasedRecord(){return null}};
 const app=new ExperienceRegistryApp({repository,privateKey,signingKeyId:'test-key',installationPepper:'pepper-value',policy:{canaryInstallations:1,releaseInstallations:2,suspendFailures:2,maximumFailureRate:.1}});
 let result=await app.candidate({installationId:'installation-one',bundle:candidate(),evidenceDigest:'d'.repeat(64),receiptId:'one'});assert.equal(result.status,'canary');
 result=await app.candidate({installationId:'installation-two',bundle:candidate(),evidenceDigest:'e'.repeat(64),receiptId:'two'});assert.equal(result.status,'released');assert.equal(published.length,1);assert.equal(JSON.stringify(published[0].bundle).includes('private-run'),false);
 const signed={...published[0].signed,bundle:published[0].bundle,status:'released'};
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cvmax-registry-'));try{const client=new ExperienceRegistryClient({baseUrl:'https://registry.test',writeToken:'token',installationId:'installation-three',publicKeys:{'test-key':publicKey},cacheFile:path.join(dir,'cache.json'),fetchImpl:async()=>new Response(JSON.stringify(signed),{status:200})});const release=await client.getReleased(familyKey);assert.equal(release.bundle.family.key,familyKey);assert.equal(client.cachedBundles()[familyKey].status,'reusable')}finally{fs.rmSync(dir,{recursive:true,force:true})}
});

test('client rejects tampered releases and never falls back to an invalid cache',async()=>{
 const {privateKey,publicKey}=generateKeyPairSync('ed25519'),bundle=candidate(),signed=signExperienceRelease(bundle,privateKey,'test-key'),dir=fs.mkdtempSync(path.join(os.tmpdir(),'cvmax-registry-'));
 try{const release={...signed,status:'released',bundle:{...bundle,status:'tampered'}},client=new ExperienceRegistryClient({baseUrl:'https://registry.test',writeToken:'token',installationId:'installation-one',publicKeys:{'test-key':publicKey},cacheFile:path.join(dir,'cache.json'),fetchImpl:async()=>new Response(JSON.stringify(release),{status:200})});await assert.rejects(client.getReleased(familyKey),/digest_mismatch/)}finally{fs.rmSync(dir,{recursive:true,force:true})}
});

test('offline writes persist a sanitized bounded queue and flush after recovery',async()=>{const {publicKey}=generateKeyPairSync('ed25519'),dir=fs.mkdtempSync(path.join(os.tmpdir(),'cvmax-registry-'));let online=false,calls=0;try{const cacheFile=path.join(dir,'cache.json'),client=new ExperienceRegistryClient({baseUrl:'https://registry.test',writeToken:'token',installationId:'installation-one',publicKeys:{key:publicKey},cacheFile,writeTimeoutMs:20,fetchImpl:async()=>{calls++;if(!online)throw Error('offline');return new Response(JSON.stringify({accepted:true}),{status:200})}}),queued=await client.submitCandidate({bundle:candidate(),evidenceDigest:'d'.repeat(64),receiptId:'offline-one'});assert.equal(queued.queued,true);const saved=JSON.parse(fs.readFileSync(cacheFile));assert.equal(saved.queue.length,1);assert.equal(JSON.stringify(saved).includes('private-run'),false);online=true;assert.deepEqual(await client.flushQueue(),{flushed:1,remaining:0});assert.equal(JSON.parse(fs.readFileSync(cacheFile)).queue.length,0);assert.equal(calls,2)}finally{fs.rmSync(dir,{recursive:true,force:true})}});
