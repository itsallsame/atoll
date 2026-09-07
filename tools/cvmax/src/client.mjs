import {AtollClient} from './atoll-client.mjs';
import {requireAuthorizedFacts} from './facts.mjs';
import {requireResumeName,requireResumeRef} from './resume-fact.mjs';
import fs from 'node:fs/promises';
import path from 'node:path';
import {createHash,randomUUID} from 'node:crypto';

const DEFAULTS={toolName:'cvmax-service',agentName:'cvmax-agent',toolNamespace:'cvmax'};

export class CvMaxClient {
 constructor(config,{transport}={}){
  if(!config||typeof config.base!=='string'||typeof config.channel!=='string')throw Error('client_config_invalid');
  this.config={...DEFAULTS,...config};
  this.client=transport??new AtollClient(config.base,{cookie:config.cookie,authorization:config.authorization});
 }
 async open(){
  await this.client.open(this.config.channel);
  const response=await this.client.request('system.member.list');
  if(!response.ok)throw Error('member_list_failed');
  this.members=response.body.actors??[];
  this.tool=this.members.find(actor=>actor.present&&actor.name===this.config.toolName);
  this.agent=this.members.find(actor=>actor.present&&actor.name===this.config.agentName);
  if(!this.tool)throw Error('service_unavailable');
  return this;
 }
 async invoke(name,args={}){
  const response=await this.client.request(`${this.config.toolNamespace}.${name}`,args,this.tool.id);
  if(!response.ok)throw Object.assign(Error('service_request_failed'),{receipt:response});
  return {requestId:response.requestId,data:response.body.structured_content};
 }
 async doctor(){
  const health=await this.invoke('health');
  return {atoll:'connected',channel:'attached',service:'available',agent:this.agent?'available':'unavailable',browser:health.data.browser,...health.data.publicExperience&&{publicExperience:health.data.publicExperience},finalSubmitAllowed:false};
 }
 async delegate(text){
  if(!this.agent)throw Error('agent_unavailable');
  const ack=await this.client.frame('submit',{channel_id:this.config.channel,msg_type:'agent.ask',kind:'request',visibility:'public',audience:[this.agent.id],payload:{text}});
  if(ack.frame_type==='error')throw Error('delegate_rejected');
  return {messageId:ack.payload.message_id,receipt:'atoll_message_accepted',businessTaskPersisted:false};
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
  const links=Array.isArray(args.links)?[...new Set(args.links.map(value=>new URL(value).href))]:[],currentPage=args.currentPage===true,modes=Number(links.length>0)+Number(currentPage);
  if(modes!==1||links.length>20)throw Error('one_entry_mode_required');
  const storedResume=args.resumeId?requireResumeRef({id:args.resumeId,revision:args.resumeRevision}):null;
  if(storedResume&&(args.resumePath||args.attachment))throw Error('stored_resume_and_inline_resume_conflict');
  const attachment=storedResume?null:args.resumePath?await this.stageAttachment(args.resumePath):args.attachment??null,taskAnswers=requireAuthorizedFacts(args.taskAnswers??args.facts??[]);
  const request={clientRequestId:args.clientRequestId,...links.length?{links}:{currentPage:true},...storedResume?{resume:storedResume,taskAnswers}:{facts:taskAnswers,...attachment&&{attachment}}};
  await this.invoke('request_save',request);
  return this.delegate(`CvMax 申请填写委托已由应用服务保存，clientRequestId=${args.clientRequestId}。先查询 status；任务不存在时调用 request_get 读取公开范围，解析目标后仅以 clientRequestId 和 targets 调用 create。不要要求或复述事实值。优先 run_experience；异常才进入自适应流程。填写并核验，不最终提交。`)
 }
 async saveResume(args){
  if(!args||typeof args.saveRequestId!=='string'||!args.saveRequestId)throw Error('resume_save_request_required');
  const attachment=args.resumePath?await this.stageAttachment(args.resumePath):args.attachment??null;
  const body={saveRequestId:args.saveRequestId,name:requireResumeName(args.name),facts:requireAuthorizedFacts(args.projectionFacts??[]),...args.resumeId&&{resumeId:args.resumeId},...args.expectedRevision!=null&&{expectedRevision:args.expectedRevision},...attachment&&{attachment}};
  return(await this.invoke('resume_save',body)).data;
 }
 async listResumes(){return(await this.invoke('resume_list',{})).data}
 async getResume({resumeId}){return(await this.invoke('resume_get',{resumeId})).data}
 async wake(taskId,commandId){
  try{
   const ack=await this.delegate(`接续 CvMax 已存在任务 ${taskId}，先查询最新状态；已结束或取消则仅报告，不新建任务。`);
   if(commandId)await this.invoke('wake_receipt',{taskId,commandId,outcome:'accepted',messageId:ack.messageId});
   return {taskId,wake:'accepted',messageId:ack.messageId};
  }catch(error){
   const outcome=['agent_unavailable','delegate_rejected'].includes(error.message)?'failed':'unknown';
   if(commandId)await this.invoke('wake_receipt',{taskId,commandId,outcome}).catch(()=>{});
   return {taskId,wake:outcome,nextAction:'保留同一任务并查询最新状态；不要重新提交资料。'};
  }
 }
 async answer(args){const {data}=await this.invoke('answer',args);return data.duplicate?{...data,wake:'not_repeated'}:{...data,...await this.wake(args.taskId,args.commandId)}}
 async continue({taskId}){const {data}=await this.invoke('status',{taskId});return ['queued','needs_attention','needs_reconcile'].includes(data.state)?this.wake(taskId):{task:data,wake:'not_applicable'}}
 close(){this.client.close()}
}
