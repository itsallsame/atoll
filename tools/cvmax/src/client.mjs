import {AtollClient} from './atoll-client.mjs';
import {requireAuthorizedFacts} from './facts.mjs';
import {requireResumeName,requireResumeRef} from './resume-fact.mjs';
import fs from 'node:fs/promises';
import path from 'node:path';
import {createHash,randomUUID} from 'node:crypto';
import {assertProtocolCompatible} from './protocol.mjs';

const DEFAULTS={toolName:'cvmax-service',agentName:'cvmax-agent',toolNamespace:'cvmax'};
const SUBMIT_KEYS=new Set(['clientRequestId','links','currentPage','resume','resumePath','attachment','taskAnswers','facts','learningMode','profileRevision']);

export class CvMaxClient {
 constructor(config,{transport}={}){
  if(!config||typeof config.base!=='string'||typeof config.channel!=='string')throw Error('client_config_invalid');
  this.config={...DEFAULTS,...config};
  this.deferredRecoveryWaitMs=Number.isSafeInteger(config.deferredRecoveryWaitMs)?Math.max(0,Math.min(config.deferredRecoveryWaitMs,60_000)):30_000;
  this.deferredRecoveryPollMs=Number.isSafeInteger(config.deferredRecoveryPollMs)?Math.max(10,Math.min(config.deferredRecoveryPollMs,5_000)):1_000;
  this.client=transport??new AtollClient(config.base,{cookie:config.cookie,authorization:config.authorization});
 }
 async open(){
  await this.client.open(this.config.channel);
  const response=await this.client.request('system.member.list');
  if(!response.ok)throw Error('member_list_failed');
  this.bindMembers(response.body.actors??[]);
  if(!this.tool)throw Error('service_unavailable');
  return this;
 }
 bindMembers(members){
  this.members=members;
  this.tool=members.find(actor=>actor.present&&actor.name===this.config.toolName);
  this.agent=members.find(actor=>actor.present&&actor.name===this.config.agentName);
 }
 async invoke(name,args={}){
  const response=await this.client.request(`${this.config.toolNamespace}.${name}`,args,this.tool.id);
  if(!response.ok)throw Object.assign(Error('service_request_failed'),{receipt:response});
  return {requestId:response.requestId,data:response.body.structured_content};
 }
 executionProgressToken(task){
  const application=task?.applications?.[task.current??0]??task?.applications?.at(-1),operation=application?.operation;
  return JSON.stringify([task?.state??null,task?.current??null,application?.applicationId??null,application?.outcome??null,operation?.id??null,operation?.state??null,operation?.transition?.state??null]);
 }
 async doctor(){
  const health=await this.invoke('health');
  assertProtocolCompatible('service',health.data.protocols?.service);
  return {atoll:'connected',channel:'attached',service:'available',agent:this.agent?'available':'unavailable',browser:health.data.browser,...health.data.browserSelection&&{browserSelection:health.data.browserSelection},...health.data.publicRecipe&&{publicRecipe:health.data.publicRecipe},finalSubmitAllowed:false};
 }
 async stageAttachment(resumePath){
  if(typeof this.config.serviceConfig!=='string')throw Error('service_config_required_for_resume');
  const service=JSON.parse(await fs.readFile(this.config.serviceConfig,'utf8')),settings=service.attachments;
  if(!settings?.root||!settings.resourcePrefix)throw Error('attachment_config_invalid');
  const source=await fs.realpath(resumePath),bytes=await fs.readFile(source),ext=path.extname(source).toLowerCase(),mime=ext==='.pdf'?'application/pdf':ext==='.docx'?'application/vnd.openxmlformats-officedocument.wordprocessingml.document':ext==='.txt'?'text/plain':null;
  if(!mime||!bytes.length||bytes.length>(settings.maxBytes??10*1024*1024))throw Error('attachment_type_or_size_invalid');
  const valid=mime==='application/pdf'?bytes.subarray(0,5).toString()==='%PDF-':mime==='application/vnd.openxmlformats-officedocument.wordprocessingml.document'?bytes[0]===80&&bytes[1]===75:!bytes.includes(0);
  if(!valid)throw Error('attachment_type_unsupported');
  await fs.mkdir(settings.root,{recursive:true,mode:0o700});const name=randomUUID()+ext,destination=path.join(settings.root,name);await fs.writeFile(destination,bytes,{flag:'wx',mode:0o600});
  return{resource:settings.resourcePrefix+encodeURIComponent(name),sha256:createHash('sha256').update(bytes).digest('hex'),size:bytes.length,mime,expiresAt:Date.now()+60*60*1000};
 }
 async submit(args){
  if(!args||typeof args.clientRequestId!=='string'||!args.clientRequestId)throw Error('client_request_id_required');
  const unknown=Object.keys(args).filter(key=>!SUBMIT_KEYS.has(key));if(unknown.length)throw Error(`submit_input_unknown:${unknown.join(',')}`);
  const links=Array.isArray(args.links)?[...new Set(args.links.map(value=>new URL(value).href))]:[],currentPage=args.currentPage===true,modes=Number(links.length>0)+Number(currentPage);
  if(modes!==1||links.length>20)throw Error('one_entry_mode_required');
  const storedResume=Object.hasOwn(args,'resume')?requireResumeRef(args.resume):null;
  if(storedResume&&(args.resumePath||args.attachment))throw Error('stored_resume_and_inline_resume_conflict');
  const attachment=storedResume?null:args.resumePath?await this.stageAttachment(args.resumePath):args.attachment??null,taskAnswers=requireAuthorizedFacts(args.taskAnswers??args.facts??[]);
  if(args.learningMode!=null&&!['user','prelearn'].includes(args.learningMode))throw Error('learning_mode_invalid');
  const request={clientRequestId:args.clientRequestId,...args.learningMode&&{learningMode:args.learningMode},...args.profileRevision!=null&&{profileRevision:args.profileRevision},...links.length?{links}:{currentPage:true},...storedResume?{resume:storedResume,taskAnswers}:{facts:taskAnswers,...attachment&&{attachment}}};
  const staged=(await this.invoke('request_save',request)).data;if(storedResume&&(!staged?.resume||staged.resume.id!==storedResume.id||staged.resume.revision!==storedResume.revision||!Number.isSafeInteger(staged.factCount)||typeof staged.hasAttachment!=='boolean'||staged.factCount===0&&!staged.hasAttachment))throw Error('stored_resume_preflight_failed');
  const targets=links.length?[]:[(await this.invoke('current',{})).data],created=(await this.invoke('create',{clientRequestId:args.clientRequestId,targets})).data;let fast;
  try{fast=(await this.invoke('run_recipe',{taskId:created.taskId,expectedRevision:created.revision})).data}catch(error){if(!/service_request_failed|outcome unknown/i.test(error.message))throw error;const recovered=(await this.invoke('status',{taskId:created.taskId})).data,application=recovered?.applications?.at(-1);if(recovered?.state==='finished')return{businessTaskPersisted:true,taskId:created.taskId,path:application?.performance?.executionPath==='recipe'?'recipe':'recovered',outcome:application?.outcome??null,elapsedMs:application?.performance?.openToCompleteMs??null,actionCount:application?.budget?.actions??null,recoveredAfterTimeout:true,finalSubmitAllowed:false};return{businessTaskPersisted:true,taskId:created.taskId,path:'pending',state:recovered?.state??'unknown',reason:'recipe_outcome_unknown',recoveredAfterTimeout:true,finalSubmitAllowed:false}}
  if(fast.path==='deferred'&&this.deferredRecoveryWaitMs>0){
   let idleDeadline=Date.now()+this.deferredRecoveryWaitMs,progressToken=this.executionProgressToken(fast.task??created);
   while(fast.path==='deferred'&&Date.now()<idleDeadline){
    await new Promise(resolve=>setTimeout(resolve,this.deferredRecoveryPollMs));
    const current=(await this.invoke('status',{taskId:created.taskId})).data,application=current?.applications?.[current.current??0]??current?.applications?.at(-1);
    if(current?.state==='finished')return{businessTaskPersisted:true,taskId:created.taskId,path:application?.performance?.executionPath==='recipe'?'recipe':'unavailable',outcome:application?.outcome??null,elapsedMs:application?.performance?.openToCompleteMs??null,actionCount:application?.budget?.actions??null,finalSubmitAllowed:false};
    const observedToken=this.executionProgressToken(current);if(observedToken!==progressToken){progressToken=observedToken;idleDeadline=Date.now()+this.deferredRecoveryWaitMs}
    if(!['running','waiting_native_parse'].includes(current?.state))break;
    try{fast=(await this.invoke('run_recipe',{taskId:created.taskId,expectedRevision:current.revision})).data}catch(error){if(!/service_request_failed|outcome unknown/i.test(error.message))throw error}
    const drivenToken=this.executionProgressToken(fast.task);if(drivenToken!==progressToken){progressToken=drivenToken;idleDeadline=Date.now()+this.deferredRecoveryWaitMs}
   }
  }
  if(['recipe','unavailable','deferred'].includes(fast.path))return{businessTaskPersisted:true,taskId:created.taskId,path:fast.path,outcome:fast.outcome??null,reason:fast.reason??null,elapsedMs:fast.elapsedMs,actionCount:fast.actionCount,finalSubmitAllowed:false};
  return {businessTaskPersisted:true,taskId:created.taskId,path:fast.path,reason:fast.reason??null,finalSubmitAllowed:false};
 }
 async saveResume(args){
  if(!args||typeof args.saveRequestId!=='string'||!args.saveRequestId)throw Error('resume_save_request_required');
  const attachment=args.resumePath?await this.stageAttachment(args.resumePath):args.attachment??null;
  const body={saveRequestId:args.saveRequestId,name:requireResumeName(args.name),facts:requireAuthorizedFacts(args.projectionFacts??[]),...args.resumeId&&{resumeId:args.resumeId},...args.expectedRevision!=null&&{expectedRevision:args.expectedRevision},...attachment&&{attachment}};
  return(await this.invoke('resume_save',body)).data;
 }
 async listResumes(){return(await this.invoke('resume_list',{})).data}
 async getResume({resumeId}){return(await this.invoke('resume_get',{resumeId})).data}
 async saveProfile(args){if(!args||typeof args.saveRequestId!=='string'||!args.saveRequestId)throw Error('profile_save_request_required');return(await this.invoke('profile_save',{saveRequestId:args.saveRequestId,...args.expectedRevision!=null&&{expectedRevision:args.expectedRevision},facts:requireAuthorizedFacts(args.facts??[])})).data}
 async getProfile(){return(await this.invoke('profile_get',{})).data}
 async deleteData({kind,id}){return(await this.invoke('data_delete',{kind,id})).data}
 async drive(taskId,current){
  if(!['queued','running','waiting_native_parse'].includes(current.state))return{task:current,wake:'not_applicable'};
  const fast=(await this.invoke('run_recipe',{taskId,expectedRevision:current.revision})).data;
  return{task:fast.task??current,taskId,path:fast.path,outcome:fast.outcome??null,reason:fast.reason??null,wake:'not_sent',finalSubmitAllowed:false};
 }
 async answer(args){const {data}=await this.invoke('answer',args);return data.duplicate?{...data,wake:'not_repeated'}:{duplicate:false,...await this.drive(args.taskId,data.task,args.commandId)}}
 async resume({taskId}){let current=(await this.invoke('status',{taskId})).data;if(current.state!=='paused')return{task:current,wake:'not_applicable'};const application=current.applications?.[current.current],unknown=application?.operation?.state==='unknown';if(unknown)current=(await this.invoke('reconcile',{taskId})).data;const resumed=(await this.invoke('resume',{taskId,expectedRevision:current.revision})).data;return this.drive(taskId,resumed)}
 async continue({taskId}){
  let {data}=await this.invoke('status',{taskId});
  if(data.state==='needs_attention'&&String(data.notice??'').includes('旧学习流程已移除'))return{task:data,taskId,path:'learning_unavailable',wake:'not_sent',finalSubmitAllowed:false};
  const current=data.applications?.[data.current],unknown=current?.operation?.state==='unknown';
  if(data.state==='needs_reconcile'||data.state==='needs_attention'&&unknown)({data}=await this.invoke('reconcile',{taskId}));
  else if(data.state==='needs_attention')({data}=await this.invoke('claim',{taskId,expectedRevision:data.revision}));
  return this.drive(taskId,data);
 }
 close(){this.client.close()}
}
