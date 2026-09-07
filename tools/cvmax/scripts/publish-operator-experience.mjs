#!/usr/bin/env node
import fs from 'node:fs';
import {MysqlExperienceRegistry} from '../src/mysql-experience-registry.mjs';
import {publicExperienceBundle} from '../src/public-experience.mjs';
import {signExperienceRelease} from '../src/experience-release.mjs';

function envFile(file) {
  const env={};for(const line of fs.readFileSync(file,'utf8').split(/\r?\n/)){const match=line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);if(match)env[match[1]]=match[2]}return env;
}
function argument(name) { const index=process.argv.indexOf(name); return index<0?null:process.argv[index+1]; }
const stateFile=argument('--state'),configFile=argument('--env');if(!stateFile||!configFile)throw Error('usage: publish-operator-experience --state <state.json> --env <registry.env>');
const state=JSON.parse(fs.readFileSync(stateFile,'utf8')),env=envFile(configFile),repository=MysqlExperienceRegistry.fromEnv(env),privateValues=Object.values(state.tasks??{}).flatMap(task=>task.factSnapshot?.facts??[]).map(fact=>String(fact.value??'').trim()).filter(value=>value.length>=4),published=[];
try{
 for(const item of Object.values(state.experiences??{})){
  const choices=Object.entries(item.versions??{}).map(([fingerprint,version])=>({fingerprint,version,ids:(version.strategyIds??[]).filter(id=>item.strategies?.[id]?.status==='reusable')})).filter(choice=>choice.ids.length).sort((a,b)=>b.ids.length-a.ids.length||String(b.version.lastSeenAt??'').localeCompare(String(a.version.lastSeenAt??'')));
  if(!choices.length)continue;const choice=choices[0],strategies=Object.fromEntries(choice.ids.map(id=>[id,item.strategies[id]])),bundle=publicExperienceBundle({...item,status:'reusable',releaseBasis:'operator_verified_real_site',versions:{[choice.fingerprint]:{...choice.version,strategyIds:choice.ids}},strategies});
  const serialized=JSON.stringify(bundle).toLowerCase();if(privateValues.some(value=>serialized.includes(value.toLowerCase())))throw Error(`private_value_detected:${item.family.host}`);
  const saved=await repository.saveCandidate(bundle),stats={independentInstallations:1,verifiedRuns:new Set(choice.version.successfulRuns??[]).size,failedRuns:0};if(saved.status!=='released'){const signed=signExperienceRelease(bundle,env.CVMAX_REGISTRY_PRIVATE_KEY.replaceAll('\\n','\n'),env.CVMAX_REGISTRY_SIGNING_KEY_ID);await repository.publishRelease(saved.releaseId,bundle,signed,stats)}
  const release=await repository.releasedRecord(item.family.key);published.push({host:item.family.host,routeTemplate:item.family.routeTemplate,schemaFingerprint:choice.fingerprint,strategyCount:choice.ids.length,releaseId:release.releaseId,signaturePresent:!!release.signature});
 }
}finally{await repository.close()}
console.log(JSON.stringify({published},null,2));
