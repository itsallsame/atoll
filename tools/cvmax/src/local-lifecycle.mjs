import fs from 'node:fs';
import path from 'node:path';

const timestamp=value=>{const parsed=Date.parse(value??'');return Number.isFinite(parsed)?parsed:0};
const oldest=(a,b)=>timestamp(a[1].updatedAt??a[1].createdAt)-timestamp(b[1].updatedAt??b[1].createdAt);

export const DEFAULT_LOCAL_POLICY=Object.freeze({
  terminalTaskTtlMs:90*24*60*60*1000,
  intakeTtlMs:24*60*60*1000,
  requestTtlMs:90*24*60*60*1000,
  stagingFileTtlMs:24*60*60*1000,
  orphanResumeGraceMs:7*24*60*60*1000,
  maxTasks:1000,
  maxIntakes:500,
  maxResumes:100,
});

export function referencedResources(state){
  const result=new Set();
  for(const resume of Object.values(state.resumes??{}))if(resume.sourceDocument?.resource)result.add(resume.sourceDocument.resource);
  for(const task of Object.values(state.tasks??{}))if(task.factSnapshot?.attachment?.resource)result.add(task.factSnapshot.attachment.resource);
  for(const intake of Object.values(state.intakes??{}))if(intake.attachment?.resource)result.add(intake.attachment.resource);
  return result;
}

export function applyLocalLifecycle(state,{nowMs=Date.now(),policy={}}={}){
  const p={...DEFAULT_LOCAL_POLICY,...policy},removed={tasks:[],intakes:[],requests:[],resumeRequests:[]};
  const expired=(value,ttl)=>{const at=timestamp(value?.updatedAt??value?.createdAt);return at>0&&nowMs-at>ttl};
  for(const [id,task]of Object.entries(state.tasks??{}))if(['finished','cancelled'].includes(task.state)&&expired(task,p.terminalTaskTtlMs)){delete state.tasks[id];removed.tasks.push(id)}
  for(const [id,intake]of Object.entries(state.intakes??{}))if(expired(intake,p.intakeTtlMs)){delete state.intakes[id];removed.intakes.push(id)}
  for(const [id,request]of Object.entries(state.requests??{}))if(!state.tasks?.[request.id]||expired(state.tasks[request.id],p.requestTtlMs)){delete state.requests[id];removed.requests.push(id)}
  for(const [id,request]of Object.entries(state.resumeRequests??{}))if(request.createdAt&&nowMs-timestamp(request.createdAt)>p.requestTtlMs){delete state.resumeRequests[id];removed.resumeRequests.push(id)}
  const trim=(collection,max,eligible,label)=>{const entries=Object.entries(collection??{});if(entries.length<=max)return;for(const [id]of entries.filter(([,value])=>eligible(value)).sort(oldest).slice(0,entries.length-max)){delete collection[id];removed[label].push(id)}if(Object.keys(collection??{}).length>max)throw Error(`local_${label}_capacity_exceeded`)};
  trim(state.tasks,p.maxTasks,task=>['finished','cancelled'].includes(task.state),'tasks');
  trim(state.intakes,p.maxIntakes,()=>true,'intakes');
  if(Object.keys(state.resumes??{}).length>p.maxResumes)throw Error('local_resumes_capacity_exceeded');
  state.maintenance={lastRunAt:new Date(nowMs).toISOString(),removedCounts:Object.fromEntries(Object.entries(removed).map(([key,value])=>[key,value.length])),policy:{...p}};
  return removed;
}

export function cleanupAttachmentFiles({root,resourcePrefix},state,{nowMs=Date.now(),policy={}}={}){
  const p={...DEFAULT_LOCAL_POLICY,...policy},referenced=referencedResources(state),removed=[];
  if(!root||!fs.existsSync(root))return removed;
  const walk=directory=>{for(const entry of fs.readdirSync(directory,{withFileTypes:true})){const file=path.join(directory,entry.name);if(entry.isDirectory()){walk(file);continue}if(!entry.isFile())continue;const relative=path.relative(root,file).split(path.sep).join('/'),resource=resourcePrefix+encodeURIComponent(relative),age=nowMs-fs.statSync(file).mtimeMs,isResume=relative.startsWith('resumes/'),ttl=isResume?p.orphanResumeGraceMs:p.stagingFileTtlMs;if(!referenced.has(resource)&&age>ttl){fs.unlinkSync(file);removed.push(relative)}}};
  walk(root);
  return removed;
}
