#!/usr/bin/env node
import fs from 'node:fs/promises';
import {CvMaxClient} from './client.mjs';
import {CvMaxInstaller} from './install.mjs';

const commands=new Set(['install','doctor','resume-save','resume-list','resume-get','submit','status','report','answer','pause','stop','skip','resume','continue']);
const args=process.argv.slice(2),configIndex=args.indexOf('--config'),command=args[0];

function output(value,exitCode=0){process.stdout.write(`${JSON.stringify(value)}\n`);process.exitCode=exitCode}
function fail(message){output({schemaVersion:1,ok:false,command:command??null,error:{code:message,message},nextAction:'query_latest_state_before_retry'},1)}

let client;
try{
 if(!commands.has(command)||configIndex<0||!args[configIndex+1]||args.length!==3)throw Error('usage: cvmax <install|doctor|resume-save|resume-list|resume-get|submit|status|report|answer|pause|stop|skip|resume|continue> --config <private-client.json>');
 const config=JSON.parse(await fs.readFile(args[configIndex+1],'utf8'));
 let raw='';for await(const chunk of process.stdin){raw+=chunk;if(raw.length>1024*1024)throw Error('input_too_large')}
 const input=raw.trim()?JSON.parse(raw):{};
 if(command==='install'){client=new CvMaxInstaller(config);const result=await client.install();output({schemaVersion:1,ok:true,command,result});}
 else {client=new CvMaxClient(config);await client.open();
 let result;
 if(command==='doctor')result=await client.doctor();
 else if(command==='resume-save')result=await client.saveResume(input);
 else if(command==='resume-list')result=await client.listResumes();
 else if(command==='resume-get')result=await client.getResume(input);
 else if(['submit','answer','continue'].includes(command))result=await client[command](input);
 else if(['pause','stop','skip'].includes(command))result=await client.invoke('control',{...input,kind:command});
 else if(command==='resume'){result=await client.invoke('resume',input);result.wake=await client.wake(input.taskId)}
 else result=await client.invoke(command,input);
 output({schemaVersion:1,ok:true,command,result});}
}catch(error){fail(error.message)}finally{client?.close()}
