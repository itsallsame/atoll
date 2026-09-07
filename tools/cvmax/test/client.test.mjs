import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {CvMaxClient} from '../src/client.mjs';
import {CvMaxInstaller} from '../src/install.mjs';

function transport({agent=true,taskState=null}={}){
 const calls=[];
 return {calls,async open(channel){calls.push(['open',channel])},async request(type,args,audience){calls.push(['request',type,args,audience]);if(type==='system.member.list')return{ok:true,body:{actors:[{id:'tool:1',name:'cvmax-service',present:true},...(agent?[{id:'agent:1',name:'cvmax-agent',present:true}]:[])]}};if(type==='cvmax.status'&&taskState)return{ok:true,requestId:'request-1',body:{structured_content:{state:taskState,taskId:args.taskId}}};return{ok:true,requestId:'request-1',body:{structured_content:{service:'ready',browser:'connected'}}}},async frame(type,payload){calls.push(['frame',type,payload]);return{frame_type:'receipt',payload:{message_id:'message-1'}}},close(){calls.push(['close'])}};
}

test('client uses the configured Atoll channel and product namespace',async()=>{
 const fake=transport(),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();
 const doctor=await client.doctor();assert.equal(doctor.browser,'connected');assert.equal(doctor.finalSubmitAllowed,false);
 assert.deepEqual(fake.calls[0],['open','private-channel']);assert.equal(fake.calls.find(call=>call[1]==='cvmax.health')[3],'tool:1');
 client.close();
});

test('delegation is an agent.ask from the existing human client, not a browser command',async()=>{
 const fake=transport(),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();
 const receipt=await client.submit({clientRequestId:'one',links:['https://jobs.example/position/1'],facts:[]});assert.equal(receipt.businessTaskPersisted,false);
 const call=fake.calls.find(item=>item[0]==='frame');assert.equal(call[1],'submit');assert.equal(call[2].msg_type,'agent.ask');assert.deepEqual(call[2].audience,['agent:1']);
 assert.equal(fake.calls.some(item=>JSON.stringify(item).includes('browser')),false);
});

test('missing hosted agent is reported without creating a second executor',async()=>{
 const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:transport({agent:false})});await client.open();
 await assert.rejects(client.submit({clientRequestId:'one',currentPage:true}),/agent_unavailable/);
});
test('client refuses uncertain or conflicting facts before agent delegation',async()=>{
 const fake=transport(),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();
 await assert.rejects(client.submit({clientRequestId:'one',currentPage:true,facts:[{id:'name',value:'Guess',sourceRef:'model:guess',uncertain:true}]}),/missing_or_conflicting/);
 assert.equal(fake.calls.some(item=>item[0]==='frame'),false);
});

test('client requires exactly one user-facing entry mode',async()=>{
 const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:transport()});await client.open();
 await assert.rejects(client.submit({clientRequestId:'one',facts:[]}),/one_entry_mode_required/);
 await assert.rejects(client.submit({clientRequestId:'one',currentPage:true,links:['https://jobs.example/one'],facts:[]}),/one_entry_mode_required/);
});

test('client de-duplicates a multi-link request before delegating it',async()=>{
 const fake=transport(),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();
 await client.submit({clientRequestId:'batch',links:['https://jobs.example/one','https://jobs.example/one','https://jobs.example/two'],facts:[]});
 const request=fake.calls.find(item=>item[1]==='cvmax.request_save')[2],text=fake.calls.find(item=>item[0]==='frame')[2].payload.text;assert.deepEqual(request.links,['https://jobs.example/one','https://jobs.example/two']);assert.equal(text.includes('jobs.example'),false);
});

test('a stored resume request is staged by id and revision without putting answers in the agent message',async()=>{const fake=transport(),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();await client.submit({clientRequestId:'stored-resume',currentPage:true,resumeId:'resume-one',resumeRevision:3,taskAnswers:[{id:'availability',value:'两周',sourceRef:'user:answer'}]});const request=fake.calls.find(item=>item[1]==='cvmax.request_save')[2],text=fake.calls.find(item=>item[0]==='frame')[2].payload.text;assert.deepEqual(request.resume,{id:'resume-one',revision:3});assert.equal(request.taskAnswers[0].id,'availability');assert.equal('facts'in request,false);assert.equal('attachment'in request,false);assert.equal(text.includes('两周'),false);assert.match(text,/clientRequestId=stored-resume/);await assert.rejects(client.submit({clientRequestId:'bad-mix',currentPage:true,resumeId:'resume-one',resumeRevision:3,resumePath:'/tmp/resume.pdf'}),/conflict/)});

test('resume save sends one document projection to the CvMax service',async()=>{const fake=transport(),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();await client.saveResume({saveRequestId:'save-one',name:'数据分析版',projectionFacts:[{id:'name',value:'Applicant',sourceRef:'file:resume.pdf#page=1'}],attachment:{resource:'private:resume.pdf'}});const call=fake.calls.find(item=>item[1]==='cvmax.resume_save');assert.equal(call[2].name,'数据分析版');assert.equal(call[2].facts[0].value,'Applicant');assert.equal(call[3],'tool:1')});

test('authorized resume staging creates a private expiring attachment reference',async()=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cvmax-client-'));try{const source=path.join(dir,'resume.pdf'),root=path.join(dir,'attachments'),serviceConfig=path.join(dir,'service.json');fs.writeFileSync(source,Buffer.from('%PDF-test'));fs.writeFileSync(serviceConfig,JSON.stringify({attachments:{root,resourcePrefix:'private:',maxBytes:1024}}));const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel',serviceConfig},{transport:transport()});const attachment=await client.stageAttachment(source),staged=path.join(root,decodeURIComponent(attachment.resource.slice('private:'.length))),mode=fs.statSync(staged).mode&0o777;assert.equal(attachment.mime,'application/pdf');assert.equal(mode,0o600);assert.ok(attachment.expiresAt>Date.now());assert.deepEqual(fs.readFileSync(staged),Buffer.from('%PDF-test'))}finally{fs.rmSync(dir,{recursive:true,force:true})}
});

test('continue wakes a task that requires read-only reconciliation after restart',async()=>{
 const fake=transport({taskState:'needs_reconcile'}),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();
 const result=await client.continue({taskId:'task-1'});assert.equal(result.wake,'accepted');assert.equal(fake.calls.filter(item=>item[0]==='frame').length,1);
});

test('installer uses Atoll declarations and membership without creating another runtime',async()=>{
 const calls=[],members=[];
 const fake={async open(channel){calls.push(['open',channel])},async request(type,args,audience,channel){calls.push(['request',type,args,audience,channel]);if(type==='system.actor.template.list')return{ok:true,body:{value:[]}};if(type==='system.member.list')return{ok:true,body:{actors:members.map(name=>({id:`actor:${name}`,name,present:true}))}};if(type==='system.member.create'){members.push(args.decl_id);return{ok:true,body:{status:'completed'}}}return{ok:true,body:{status:'completed'}}},close(){}};
 const installer=new CvMaxInstaller({base:'http://atoll.test',channel:'private-channel',serviceConfig:new URL('../package.json',import.meta.url).pathname},{transport:fake});
 const result=await installer.install();
 assert.equal(result.ready,true);assert.deepEqual(result.created,['cvmax-agent','cvmax-service']);
 const declarations=calls.filter(call=>call[1]==='system.actor.template.create');
 assert.deepEqual(declarations.map(call=>call[2].class),['codex','mcp']);
 assert.equal(declarations[1][2].config.transport,'stdio');
 assert.match(declarations[0][2].config.prompt,/single business decision maker/);
 assert.equal(calls.some(call=>JSON.stringify(call).includes('system.channel.create')),false);
});

test('installer updates and restarts existing application members',async()=>{
 const calls=[],actors=[{id:'agent:cvmax-agent:1',name:'cvmax-agent',present:true},{id:'tool:cvmax-service:1',name:'cvmax-service',present:true}],declarations=[{id:'cvmax-agent',name:'cvmax-agent'},{id:'cvmax-service',name:'cvmax-service'}];
 const fake={async open(){},async request(type,args){calls.push([type,args]);if(type==='system.actor.template.list')return{ok:true,body:{value:declarations}};if(type==='system.member.list')return{ok:true,body:{actors}};return{ok:true,body:{status:'completed'}}},close(){}};
 const installer=new CvMaxInstaller({base:'http://atoll.test',channel:'private-channel',serviceConfig:new URL('../package.json',import.meta.url).pathname},{transport:fake});
 const result=await installer.install();
 assert.deepEqual(result.restarted,['cvmax-agent','cvmax-service']);
 assert.equal(calls.filter(call=>call[0]==='system.actor.template.set').length,2);
 assert.equal(calls.filter(call=>call[0]==='system.actor.overlay.set').length,2);
 assert.equal(calls.filter(call=>call[0]==='system.member.restart').length,2);
});

test('installer leaves matching normalized declarations and running members untouched',async()=>{
 const calls=[],instructions=new URL('../agent/instructions.md',import.meta.url),prompt=fs.readFileSync(instructions,'utf8'),serviceConfig=new URL('../package.json',import.meta.url).pathname,server=new URL('../src/server.mjs',import.meta.url).pathname,cwd=path.dirname(new URL('../src',import.meta.url).pathname);const actors=[{id:'agent:cvmax-agent:1',name:'cvmax-agent',present:true},{id:'tool:cvmax-service:1',name:'cvmax-service',present:true}];const fake={async open(){},async request(type,args){calls.push([type,args]);if(type==='system.actor.template.list')return{ok:true,body:{value:declarations}};if(type==='system.member.list')return{ok:true,body:{actors}};return{ok:true,body:{status:'completed'}}},close(){}};const installer=new CvMaxInstaller({base:'http://atoll.test',channel:'private-channel',serviceConfig},{transport:fake}),runtimeRevision=await installer.runtimeRevision(),declarations=[{id:'cvmax-agent',name:'cvmax-agent',description:'CvMax hosted application agent.',default_class:'codex',visibility:'private',singleton:true,config:{prompt,default:{},selections:{}}},{id:'cvmax-service',name:'cvmax-service',description:'CvMax fill-and-verify application service.',default_class:'mcp',visibility:'private',singleton:true,config:{name:'cvmax',transport:'stdio',command:process.execPath,args:[server,serviceConfig,'--runtime-revision',runtimeRevision],cwd,call_timeout_ms:60000}}],result=await installer.install();assert.deepEqual(result.created,[]);assert.deepEqual(result.restarted,[]);assert.equal(calls.some(call=>['system.actor.template.set','system.actor.overlay.set','system.member.restart'].includes(call[0])),false)
});
