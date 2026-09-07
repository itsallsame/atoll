import fs from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createHash} from 'node:crypto';
import {AtollClient} from './atoll-client.mjs';

const here=path.dirname(fileURLToPath(import.meta.url));
const defaults={agentName:'cvmax-agent',toolName:'cvmax-service',toolNamespace:'cvmax'};

export class CvMaxInstaller{
 constructor(config,{transport}={}){
  if(!config||typeof config.base!=='string'||typeof config.channel!=='string'||typeof config.serviceConfig!=='string')throw Error('installer_config_invalid');
  this.config={...defaults,...config};
  this.client=transport??new AtollClient(config.base,{cookie:config.cookie,authorization:config.authorization});
 }
 declarationMatches(existing,declaration){
  if(existing.description!==declaration.description||(existing.default_class??existing.class)!==declaration.class||existing.visibility!==declaration.visibility||existing.singleton!==declaration.singleton)return false;
  return Object.entries(declaration.config).every(([key,value])=>JSON.stringify(existing.config?.[key])===JSON.stringify(value));
 }
 async runtimeRevision(){
  const files=['attachments.mjs','browser-bridge.mjs','budget.mjs','client.mjs','experience.mjs','experience-registry-client.mjs','experience-release.mjs','facts.mjs','public-experience.mjs','resume-fact.mjs','server.mjs','service.mjs','store.mjs'];
  const digest=createHash('sha256');
  for(const file of files){digest.update(file);digest.update(await fs.readFile(path.join(here,file)));}
  return digest.digest('hex').slice(0,16);
 }
 async ensureDeclaration(declarations,declaration){
  const existing=declarations.find(item=>item.name===declaration.name);
  if(existing){if(this.declarationMatches(existing,declaration))return{created:false,updated:false,id:existing.id};const response=await this.client.request('system.actor.template.set',{id:existing.id,description:declaration.description,config:declaration.config},'system','c0');if(!response.ok)throw Error(`declaration_update_failed:${declaration.name}`);return{created:false,updated:true,id:existing.id};}
  const response=await this.client.request('system.actor.template.create',declaration,'system','c0');
  if(!response.ok)throw Error(`declaration_create_failed:${declaration.name}`);
  return{created:true,id:declaration.id};
 }
 async install(){
  await this.client.open(this.config.channel);
  const [membersResponse,declarationsResponse]=await Promise.all([
   this.client.request('system.member.list',{},'system',this.config.channel),
   this.client.request('system.actor.template.list',{},'system','c0')
  ]);
  if(!membersResponse.ok||!declarationsResponse.ok)throw Error('installation_inventory_failed');
  const members=membersResponse.body.actors??[],declarations=declarationsResponse.body.value??[];
  const prompt=await fs.readFile(path.join(here,'../agent/instructions.md'),'utf8');
  const serviceConfig=path.resolve(this.config.serviceConfig),server=path.join(here,'server.mjs'),runtimeRevision=await this.runtimeRevision();
  await fs.access(serviceConfig);
  const serviceDeclaration={name:this.config.toolNamespace,transport:'stdio',command:process.execPath,args:[server,serviceConfig,'--runtime-revision',runtimeRevision],cwd:path.dirname(here),call_timeout_ms:60000};
  const agent=await this.ensureDeclaration(declarations,{id:this.config.agentName,name:this.config.agentName,description:'CvMax hosted application agent.',class:'codex',visibility:'private',singleton:true,config:{prompt}});
  const tool=await this.ensureDeclaration(declarations,{id:this.config.toolName,name:this.config.toolName,description:'CvMax fill-and-verify application service.',class:'mcp',visibility:'private',singleton:true,config:serviceDeclaration});
  const created=[],restarted=[];
  for(const item of [{name:this.config.agentName,id:agent.id,updated:agent.updated},{name:this.config.toolName,id:tool.id,updated:tool.updated}]){
   const existing=members.find(member=>member.present&&member.name===item.name);
   if(existing){if(item.updated){const declaration=item.name===this.config.agentName?{prompt}:serviceDeclaration;const overlay=await this.client.request('system.actor.overlay.set',{decl_id:item.id,channel_id:this.config.channel,config:declaration},'system','c0');if(!overlay.ok)throw Error(`member_overlay_failed:${item.name}`);const response=await this.client.request('system.member.restart',{member:existing.id},'system',this.config.channel);if(!response.ok)throw Error(`member_restart_failed:${item.name}`);restarted.push(item.name);}continue;}
   const response=await this.client.request('system.member.create',{decl_id:item.id},'system',this.config.channel);
   if(!response.ok)throw Error(`member_create_failed:${item.name}`);
   created.push(item.name);
  }
  const verified=await this.client.request('system.member.list',{},'system',this.config.channel);
  if(!verified.ok)throw Error('installation_verification_failed');
  const active=verified.body.actors??[];
  for(const name of [this.config.agentName,this.config.toolName])if(!active.some(member=>member.present&&member.name===name))throw Error(`member_not_ready:${name}`);
  return{channel:this.config.channel,agent:this.config.agentName,service:this.config.toolName,created,restarted,ready:true,finalSubmitAllowed:false};
 }
 close(){this.client.close()}
}
