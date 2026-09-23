import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {CvMaxClient} from '../src/client.mjs';
import {CvMaxInstaller} from '../src/install.mjs';

function transport({agent=true,taskState=null,fastPath='adaptive'}={}){
 const calls=[];
 return {calls,async open(channel){calls.push(['open',channel])},async request(type,args,audience,channel){calls.push(['request',type,args,audience,channel]);if(type==='system.member.list')return{ok:true,body:{actors:[{id:'tool:1',name:'cvmax-service',present:true},...(agent?[{id:'agent:1',name:'cvmax-agent',present:true}]:[])]}};if(['agent.interrupt','agent.unhold','agent.new'].includes(type))return{ok:true,body:{status:'completed'}};if(type==='system.member.restart')return{ok:true,body:{status:'completed'}};if(type==='cvmax.status'&&taskState)return{ok:true,requestId:'request-1',body:{structured_content:{state:taskState,taskId:args.taskId,revision:4}}};if(type==='cvmax.claim')return{ok:true,requestId:'request-1',body:{structured_content:{state:'running',taskId:args.taskId,revision:5}}};const data=type==='cvmax.request_save'?{clientRequestId:args.clientRequestId,factCount:args.resume?1:(args.facts??[]).length,hasAttachment:args.resume?true:!!args.attachment,...args.resume&&{resume:args.resume}}:type==='cvmax.current'?{url:'https://jobs.example/current',tabId:1}:type==='cvmax.open'?{url:args.url,tabId:1}:type==='cvmax.create'?{taskId:'task-fast',revision:1,state:'queued'}:type==='cvmax.run_recipe'?fastPath==='recipe'?{path:'recipe',outcome:'partial',elapsedMs:120,actionCount:2}:fastPath==='deferred'?{path:'deferred',reason:'loading_surface_stabilizing',elapsedMs:1,actionCount:0}:{path:'adaptive',reason:'recipe_cold',task:{state:'running'}}:{service:'ready',browser:'connected',protocols:{service:{current:2,minimum:2}}};return{ok:true,requestId:'request-1',body:{structured_content:data}}},async frame(type,payload){calls.push(['frame',type,payload]);return{frame_type:'receipt',payload:{message_id:'message-1'}}},close(){calls.push(['close'])}};
}

test('client uses the configured Atoll channel and product namespace',async()=>{
 const fake=transport(),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();
 const doctor=await client.doctor();assert.equal(doctor.browser,'connected');assert.equal(doctor.finalSubmitAllowed,false);
 assert.deepEqual(fake.calls[0],['open','private-channel']);assert.equal(fake.calls.find(call=>call[1]==='cvmax.health')[3],'tool:1');
 client.close();
});

test('unknown page does not wake the removed learner',async()=>{
 const fake=transport({agent:false});
 const request=fake.request.bind(fake);
 fake.request=async(type,args,audience,channel)=>type==='cvmax.run_recipe'
  ?{ok:true,requestId:'recipe-unknown',body:{structured_content:{path:'learning_unavailable',reason:'recipe_cold',task:{state:'needs_attention'},elapsedMs:1,actionCount:0}}}
  :request(type,args,audience,channel);
 const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});
 await client.open();
 const result=await client.submit({clientRequestId:'unknown-no-learner',currentPage:true,facts:[]});
 assert.equal(result.path,'learning_unavailable');
 assert.equal(result.businessTaskPersisted,true);
 assert.equal(fake.calls.some(item=>item[0]==='frame'),false);
});

test('an adaptive result starts a fresh provider conversation before delegation',async()=>{
 const fake=transport(),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();
 const receipt=await client.submit({clientRequestId:'one',links:['https://jobs.example/position/1'],facts:[]});assert.equal(receipt.businessTaskPersisted,true);assert.equal(receipt.path,'adaptive');
 const call=fake.calls.find(item=>item[0]==='frame');assert.equal(call[1],'submit');assert.equal(call[2].msg_type,'agent.ask');assert.deepEqual(call[2].audience,['agent:1']);
 const interrupt=fake.calls.findIndex(item=>item[1]==='agent.interrupt'),unhold=fake.calls.findIndex(item=>item[1]==='agent.unhold'),reset=fake.calls.findIndex(item=>item[1]==='agent.new'),delegation=fake.calls.findIndex(item=>item[0]==='frame');assert.ok(interrupt>=0&&interrupt<unhold&&unhold<reset&&reset<delegation);assert.equal(fake.calls[reset][3],'agent:1');
 assert.equal(fake.calls.some(item=>JSON.stringify(item).includes('browser')),false);
});

test('a known page completes through the service without waking an agent',async()=>{const fake=transport({fastPath:'recipe'}),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();const result=await client.submit({clientRequestId:'known',currentPage:true,facts:[]});assert.equal(result.path,'recipe');assert.equal(result.taskId,'task-fast');assert.equal(result.businessTaskPersisted,true);assert.equal(fake.calls.some(item=>item[0]==='frame'),false);assert.deepEqual(fake.calls.filter(item=>item[0]==='request').map(item=>item[1]).slice(-4),['cvmax.request_save','cvmax.current','cvmax.create','cvmax.run_recipe'])});

test('a deferred service recovery does not wake the hosted agent while it remains deferred',async()=>{const fake=transport({fastPath:'deferred'}),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel',deferredRecoveryWaitMs:0},{transport:fake});await client.open();const result=await client.submit({clientRequestId:'deferred',currentPage:true,facts:[]});assert.equal(result.path,'deferred');assert.equal(result.reason,'loading_surface_stabilizing');assert.equal(result.businessTaskPersisted,true);assert.equal(fake.calls.some(item=>item[0]==='frame'),false)});

test('the client automatically hands a recovered cold page to learning',async()=>{const fake=transport({taskState:'running'}),base=fake.request.bind(fake);let recipes=0;fake.request=async(type,args,audience,channel)=>{if(type==='cvmax.run_recipe'){fake.calls.push(['request',type,args,audience,channel]);recipes++;return{ok:true,requestId:'request-'+recipes,body:{structured_content:recipes===1?{path:'deferred',reason:'loading_surface_stabilizing'}:{path:'adaptive',reason:'recipe_missing_transition',task:{state:'running'}}}}}return base(type,args,audience,channel)};const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel',deferredRecoveryWaitMs:100,deferredRecoveryPollMs:10},{transport:fake});await client.open();const result=await client.submit({clientRequestId:'recovered-cold',currentPage:true,facts:[]});assert.equal(result.path,'adaptive');assert.equal(recipes,2);assert.equal(fake.calls.filter(item=>item[0]==='frame').length,1)});

test('the client renews its recovery lease when deferred execution changes business state',async()=>{
 const fake=transport(),base=fake.request.bind(fake);let recipes=0,statuses=0;
 fake.request=async(type,args,audience,channel)=>{
  if(type==='cvmax.status'){fake.calls.push(['request',type,args,audience,channel]);statuses++;return{ok:true,requestId:`status-${statuses}`,body:{structured_content:{taskId:args.taskId,revision:statuses+1,state:statuses===1?'waiting_native_parse':'running',current:0,applications:[{applicationId:'application-1',outcome:'pending',operation:{id:'upload-1',state:'verified',transition:{state:'matched'}}}]}}}}
  if(type==='cvmax.run_recipe'){fake.calls.push(['request',type,args,audience,channel]);recipes++;const data=recipes<3?{path:'deferred',reason:'native_parse_pending',task:{state:recipes===1?'waiting_native_parse':'running',current:0,applications:[{applicationId:'application-1',outcome:'pending',operation:{id:'upload-1',state:'verified',transition:{state:'matched'}}}]}}:{path:'recipe',outcome:'partial',elapsedMs:7,actionCount:2};return{ok:true,requestId:`recipe-${recipes}`,body:{structured_content:data}}}
  return base(type,args,audience,channel)
 };
 const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel',deferredRecoveryWaitMs:25,deferredRecoveryPollMs:10},{transport:fake});await client.open();
 const result=await client.submit({clientRequestId:'native-timeout-progress',currentPage:true,facts:[]});
 assert.equal(result.path,'recipe');assert.equal(result.outcome,'partial');assert.equal(recipes,3);assert.equal(fake.calls.some(item=>item[0]==='frame'),false)
});

test('submit recovers a completed recipe after an unknown transport outcome',async()=>{const fake=transport(),base=fake.request.bind(fake);fake.request=async(type,args,audience,channel)=>{if(type==='cvmax.run_recipe')throw Error('Atoll response timeout; outcome unknown');if(type==='cvmax.status')return{ok:true,requestId:'status',body:{structured_content:{state:'finished',applications:[{outcome:'partial',performance:{executionPath:'recipe',openToCompleteMs:18650},budget:{actions:18}}]}}};return base(type,args,audience,channel)};const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();const result=await client.submit({clientRequestId:'timeout-known',currentPage:true,facts:[]});assert.equal(result.path,'recipe');assert.equal(result.recoveredAfterTimeout,true);assert.equal(result.elapsedMs,18650);assert.equal(result.actionCount,18);assert.equal(fake.calls.some(item=>item[0]==='frame'),false)});

test('submit recovers a persisted terminal result after a generic service failure receipt',async()=>{const fake=transport(),base=fake.request.bind(fake);fake.request=async(type,args,audience,channel)=>{if(type==='cvmax.run_recipe')return{ok:false,requestId:'lost-result'};if(type==='cvmax.status')return{ok:true,requestId:'status',body:{structured_content:{state:'finished',applications:[{outcome:'partial',performance:{executionPath:'recipe',openToCompleteMs:21477},budget:{actions:3}}]}}};return base(type,args,audience,channel)};const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();const result=await client.submit({clientRequestId:'service-failure-known',currentPage:true,facts:[]});assert.equal(result.path,'recipe');assert.equal(result.outcome,'partial');assert.equal(result.recoveredAfterTimeout,true);assert.equal(result.elapsedMs,21477);assert.equal(result.actionCount,3);assert.equal(fake.calls.some(item=>item[0]==='frame'),false)});

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
 const request=fake.calls.find(item=>item[1]==='cvmax.request_save')[2],create=fake.calls.find(item=>item[1]==='cvmax.create')[2],text=fake.calls.find(item=>item[0]==='frame')[2].payload.text;assert.deepEqual(request.links,['https://jobs.example/one','https://jobs.example/two']);assert.deepEqual(create.targets,[]);assert.equal(fake.calls.some(item=>item[1]==='cvmax.open'),false);assert.equal(fake.calls.some(item=>item[1]==='cvmax.current'),false);assert.equal(text.includes('jobs.example'),false);
});

test('a stored resume request uses one normalized reference without putting answers in the agent message',async()=>{const fake=transport(),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();await client.submit({clientRequestId:'stored-resume',currentPage:true,resume:{id:'resume-one',revision:3},taskAnswers:[{id:'availability',value:'两周',sourceRef:'user:answer'}]});const request=fake.calls.find(item=>item[1]==='cvmax.request_save')[2],text=fake.calls.find(item=>item[0]==='frame')[2].payload.text;assert.deepEqual(request.resume,{id:'resume-one',revision:3});assert.equal(request.taskAnswers[0].id,'availability');assert.equal('facts'in request,false);assert.equal('attachment'in request,false);assert.equal(text.includes('两周'),false);assert.match(text,/已存在任务 task-fast/);await assert.rejects(client.submit({clientRequestId:'bad-mix',currentPage:true,resume:{id:'resume-one',revision:3},resumePath:'/tmp/resume.pdf'}),/conflict/)});
test('submit rejects unknown or malformed resume shapes before staging or opening a page',async()=>{const fake=transport(),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();await assert.rejects(client.submit({clientRequestId:'typo',currentPage:true,resum:{id:'resume-one',revision:3}}),/submit_input_unknown:resum/);await assert.rejects(client.submit({clientRequestId:'flat',currentPage:true,resumeId:'resume-one',resumeRevision:3}),/submit_input_unknown:resumeId,resumeRevision/);await assert.rejects(client.submit({clientRequestId:'malformed',currentPage:true,resume:{id:'resume-one'}}),/resume_ref_invalid/);assert.equal(fake.calls.some(item=>item[1]==='cvmax.request_save'),false);assert.equal(fake.calls.some(item=>item[1]==='cvmax.open'),false)});
test('a malformed resume preflight receipt blocks page opening and task creation',async()=>{const fake=transport();const original=fake.request.bind(fake);fake.request=async(type,args,audience)=>type==='cvmax.request_save'?{ok:true,requestId:'request-1',body:{structured_content:{clientRequestId:args.clientRequestId,factCount:0,hasAttachment:false,resume:args.resume}}}:original(type,args,audience);const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();await assert.rejects(client.submit({clientRequestId:'empty-resume',currentPage:true,resume:{id:'resume-one',revision:3}}),/stored_resume_preflight_failed/);assert.equal(fake.calls.some(item=>item[1]==='cvmax.current'),false);assert.equal(fake.calls.some(item=>item[1]==='cvmax.create'),false)});

test('resume save sends one document projection to the CvMax service',async()=>{const fake=transport(),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();await client.saveResume({saveRequestId:'save-one',name:'数据分析版',projectionFacts:[{id:'name',value:'Applicant',sourceRef:'file:resume.pdf#page=1'}],attachment:{resource:'private:resume.pdf'}});const call=fake.calls.find(item=>item[1]==='cvmax.resume_save');assert.equal(call[2].name,'数据分析版');assert.equal(call[2].facts[0].value,'Applicant');assert.equal(call[3],'tool:1')});

test('authorized resume staging creates a private expiring attachment reference',async()=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cvmax-client-'));try{const source=path.join(dir,'resume.pdf'),root=path.join(dir,'attachments'),serviceConfig=path.join(dir,'service.json');fs.writeFileSync(source,Buffer.from('%PDF-test'));fs.writeFileSync(serviceConfig,JSON.stringify({attachments:{root,resourcePrefix:'private:',maxBytes:1024}}));const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel',serviceConfig},{transport:transport()});const attachment=await client.stageAttachment(source),staged=path.join(root,decodeURIComponent(attachment.resource.slice('private:'.length))),mode=fs.statSync(staged).mode&0o777;assert.equal(attachment.mime,'application/pdf');assert.equal(mode,0o600);assert.ok(attachment.expiresAt>Date.now());assert.deepEqual(fs.readFileSync(staged),Buffer.from('%PDF-test'))}finally{fs.rmSync(dir,{recursive:true,force:true})}
});

test('continue reconciles after restart and completes a known Recipe without waking the agent',async()=>{
 const fake=transport({taskState:'needs_reconcile',fastPath:'recipe'}),base=fake.request.bind(fake);fake.request=async(type,args,audience,channel)=>{if(type==='cvmax.reconcile'){fake.calls.push(['request',type,args,audience,channel]);return{ok:true,requestId:'request-2',body:{structured_content:{state:'running',taskId:args.taskId,revision:5,current:0,applications:[{operation:{state:'verified'}}]}}}}return base(type,args,audience,channel)};const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();
 const result=await client.continue({taskId:'task-1'});assert.equal(result.wake,'not_needed');assert.equal(result.path,'recipe');assert.equal(fake.calls.filter(item=>item[0]==='frame').length,0);const reconcile=fake.calls.findIndex(item=>item[1]==='cvmax.reconcile'),recipe=fake.calls.findIndex(item=>item[1]==='cvmax.run_recipe');assert.ok(reconcile>=0&&reconcile<recipe);
});

test('continue claims a safely failed task before waking the hosted agent',async()=>{const fake=transport({taskState:'needs_attention'}),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();const result=await client.continue({taskId:'task-1'});assert.equal(result.wake,'accepted');const claim=fake.calls.findIndex(item=>item[1]==='cvmax.claim'),frame=fake.calls.findIndex(item=>item[0]==='frame');assert.ok(claim>=0&&claim<frame);assert.deepEqual(fake.calls[claim][2],{taskId:'task-1',expectedRevision:4})});

test('same-task continuation reuses the hosted agent conversation',async()=>{const fake=transport({taskState:'running'}),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();const result=await client.continue({taskId:'task-1'});assert.equal(result.wake,'accepted');assert.equal(fake.calls.filter(item=>['agent.interrupt','agent.unhold','agent.new'].includes(item[1])).length,0);assert.equal(fake.calls.filter(item=>item[0]==='frame').length,1)});

test('continue reconciles an unknown effect before waking and never claims it',async()=>{const fake=transport(),base=fake.request.bind(fake);fake.request=async(type,args,audience,channel)=>{if(type==='cvmax.status'){fake.calls.push(['request',type,args,audience,channel]);return{ok:true,requestId:'request-1',body:{structured_content:{state:'needs_attention',taskId:'task-1',revision:4,current:0,applications:[{operation:{state:'unknown'}}]}}}}if(type==='cvmax.reconcile'){fake.calls.push(['request',type,args,audience,channel]);return{ok:true,requestId:'request-2',body:{structured_content:{state:'running',taskId:'task-1',revision:5,current:0,applications:[{operation:{state:'failed'}}]}}}}return base(type,args,audience,channel)};const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();const result=await client.continue({taskId:'task-1'});assert.equal(result.wake,'accepted');assert.equal(fake.calls.some(item=>item[1]==='cvmax.claim'),false);const reconcile=fake.calls.findIndex(item=>item[1]==='cvmax.reconcile'),frame=fake.calls.findIndex(item=>item[0]==='frame');assert.ok(reconcile>=0&&reconcile<frame)});

test('continue tells the hosted agent that login and resume identities are independent',async()=>{const fake=transport({taskState:'running'}),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();const result=await client.continue({taskId:'task-1'}),message=fake.calls.find(item=>item[0]==='frame')[2].payload.text;assert.equal(result.wake,'accepted');assert.equal(fake.calls.filter(item=>item[0]==='frame').length,1);assert.match(message,/登录账号与所选简历/);assert.match(message,/不得比较它们或据此暂停/);assert.doesNotMatch(message,/identity_conflict/)});
test('continue tells the hosted agent to bind run_recipe to the latest revision',async()=>{const fake=transport({taskState:'running'}),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();await client.continue({taskId:'task-1'});const message=fake.calls.find(item=>item[0]==='frame')[2].payload.text;assert.match(message,/status/);assert.match(message,/latest revision|最新 revision/);assert.match(message,/expectedRevision/);assert.match(message,/revision_conflict/)});
test('continue tells the hosted agent to recover mappings before an empty proposal',async()=>{const fake=transport({taskState:'running'}),client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();await client.continue({taskId:'task-1'});const message=fake.calls.find(item=>item[0]==='frame')[2].payload.text;assert.match(message,/task_context/);assert.match(message,/no_resume_fact/);assert.match(message,/learning_interpret/);assert.match(message,/禁止向 learning_propose 发送空 actions/);assert.match(message,/coverage_review/)});
test('resume reads the latest revision and tries the service before waking the agent',async()=>{const fake=transport({taskState:'paused'}),base=fake.request.bind(fake);fake.request=async(type,args,audience,channel)=>{if(type==='cvmax.resume'){fake.calls.push(['request',type,args,audience,channel]);return{ok:true,requestId:'request-2',body:{structured_content:{state:'queued',taskId:args.taskId,revision:5}}}}return base(type,args,audience,channel)};const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();const result=await client.resume({taskId:'task-1'});assert.equal(result.wake,'accepted');const call=fake.calls.find(item=>item[1]==='cvmax.resume'),recipe=fake.calls.findIndex(item=>item[1]==='cvmax.run_recipe'),frame=fake.calls.findIndex(item=>item[0]==='frame');assert.deepEqual(call[2],{taskId:'task-1',expectedRevision:4});assert.deepEqual(fake.calls[recipe][2],{taskId:'task-1',expectedRevision:5});assert.ok(recipe>=0&&recipe<frame)});

test('resume reconciles an unknown paused operation before clearing the pause gate',async()=>{const fake=transport({taskState:'paused'}),base=fake.request.bind(fake);fake.request=async(type,args,audience,channel)=>{if(type==='cvmax.status'){fake.calls.push(['request',type,args,audience,channel]);return{ok:true,requestId:'request-1',body:{structured_content:{state:'paused',taskId:'task-1',revision:4,current:0,applications:[{operation:{state:'unknown'}}]}}}}if(type==='cvmax.reconcile'){fake.calls.push(['request',type,args,audience,channel]);return{ok:true,requestId:'request-2',body:{structured_content:{state:'paused',taskId:args.taskId,revision:5,current:0,applications:[{operation:{state:'failed'}}]}}}}if(type==='cvmax.resume'){fake.calls.push(['request',type,args,audience,channel]);return{ok:true,requestId:'request-3',body:{structured_content:{state:'queued',taskId:args.taskId,revision:6}}}}return base(type,args,audience,channel)};const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();await client.resume({taskId:'task-1'});const reconcile=fake.calls.findIndex(item=>item[1]==='cvmax.reconcile'),resume=fake.calls.findIndex(item=>item[1]==='cvmax.resume');assert.ok(reconcile>=0&&reconcile<resume);assert.deepEqual(fake.calls[resume][2],{taskId:'task-1',expectedRevision:5})});

test('resume completes a known Recipe in the service without queueing the agent',async()=>{const fake=transport({taskState:'paused',fastPath:'recipe'}),base=fake.request.bind(fake);fake.request=async(type,args,audience,channel)=>{if(type==='cvmax.resume'){fake.calls.push(['request',type,args,audience,channel]);return{ok:true,requestId:'request-2',body:{structured_content:{state:'queued',taskId:args.taskId,revision:5}}}}return base(type,args,audience,channel)};const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();const result=await client.resume({taskId:'task-1'});assert.equal(result.path,'recipe');assert.equal(result.wake,'not_needed');assert.equal(fake.calls.some(item=>item[0]==='frame'),false)});

test('answer resumes through the service and wakes the agent only for adaptive work',async()=>{const fake=transport({fastPath:'recipe'}),base=fake.request.bind(fake);fake.request=async(type,args,audience,channel)=>{if(type==='cvmax.answer'){fake.calls.push(['request',type,args,audience,channel]);return{ok:true,requestId:'request-2',body:{structured_content:{duplicate:false,task:{state:'queued',taskId:args.taskId,revision:5}}}}}return base(type,args,audience,channel)};const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();const result=await client.answer({taskId:'task-1',questionId:'q1',questionRevision:1,commandId:'c1',acknowledged:true});assert.equal(result.path,'recipe');assert.equal(result.wake,'not_needed');assert.equal(fake.calls.some(item=>item[0]==='frame'),false)});

test('installer uses Atoll declarations and membership without creating another runtime',async()=>{
 const calls=[],members=[];
 const fake={async open(channel){calls.push(['open',channel])},async request(type,args,audience,channel){calls.push(['request',type,args,audience,channel]);if(type==='system.actor.template.list')return{ok:true,body:{value:[]}};if(type==='system.member.list')return{ok:true,body:{actors:members.map(name=>({id:`actor:${name}`,name,present:true}))}};if(type==='system.member.create'){members.push(args.decl_id);return{ok:true,body:{status:'completed'}}}return{ok:true,body:{status:'completed'}}},close(){}};
 const installer=new CvMaxInstaller({base:'http://atoll.test',channel:'private-channel',serviceConfig:new URL('../package.json',import.meta.url).pathname},{transport:fake});
 const result=await installer.install();
 assert.equal(result.ready,true);assert.deepEqual(result.created,['cvmax-agent','cvmax-service']);
 const declarations=calls.filter(call=>call[1]==='system.actor.template.create');
 assert.deepEqual(declarations.map(call=>call[2].class),['codex','mcp']);
 assert.equal(declarations[1][2].config.transport,'stdio');
 assert.match(declarations[0][2].config.prompt,/handle only the unknown step/);
 assert.match(declarations[0][2].config.prompt,/Service and its Recipe cursor own the workflow/);
 assert.match(declarations[0][2].config.prompt,/Prefer the site's native resume upload\/parse path/);
 assert.match(declarations[0][2].config.prompt,/call `task_context` once/);
 assert.match(declarations[0][2].config.prompt,/Never call `learning_propose` with an empty action list/);
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
 const calls=[],instructions=new URL('../agent/instructions.md',import.meta.url),prompt=fs.readFileSync(instructions,'utf8'),serviceConfig=new URL('../package.json',import.meta.url).pathname,server=new URL('../src/server.mjs',import.meta.url).pathname,cwd=path.dirname(new URL('../src',import.meta.url).pathname);const actors=[{id:'agent:cvmax-agent:1',name:'cvmax-agent',present:true},{id:'tool:cvmax-service:1',name:'cvmax-service',present:true}];const fake={async open(){},async request(type,args){calls.push([type,args]);if(type==='system.actor.template.list')return{ok:true,body:{value:declarations}};if(type==='system.member.list')return{ok:true,body:{actors}};return{ok:true,body:{status:'completed'}}},close(){}};const installer=new CvMaxInstaller({base:'http://atoll.test',channel:'private-channel',serviceConfig},{transport:fake}),runtimeRevision=await installer.runtimeRevision(fs.readFileSync(serviceConfig)),declarations=[{id:'cvmax-agent',name:'cvmax-agent',description:'CvMax hosted application agent.',default_class:'codex',visibility:'private',singleton:true,config:{prompt,default:{},selections:{}}},{id:'cvmax-service',name:'cvmax-service',description:'CvMax fill-and-verify application service.',default_class:'mcp',visibility:'private',singleton:true,config:{name:'cvmax',transport:'stdio',command:process.execPath,args:[server,serviceConfig,'--runtime-revision',runtimeRevision],cwd,call_timeout_ms:60000}}],result=await installer.install();assert.deepEqual(result.created,[]);assert.deepEqual(result.restarted,[]);assert.equal(calls.some(call=>['system.actor.template.set','system.actor.overlay.set','system.member.restart'].includes(call[0])),false)
});

test('installer creates private runtime directories before starting the service',async()=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cvmax-install-runtime-'));
 try{
  const attachments=path.join(dir,'private','attachments'),cacheFile=path.join(dir,'cache','recipe.json'),stateFile=path.join(dir,'state','tasks.json'),serviceConfig=path.join(dir,'service.json');
  fs.writeFileSync(serviceConfig,JSON.stringify({attachments:{root:attachments},recipeRegistry:{cacheFile},stateFile}));
  const members=[],fake={async open(){},async request(type,args){if(type==='system.actor.template.list')return{ok:true,body:{value:[]}};if(type==='system.member.list')return{ok:true,body:{actors:members.map(name=>({id:`actor:${name}`,name,present:true}))}};if(type==='system.member.create'){members.push(args.decl_id);return{ok:true,body:{status:'completed'}}}return{ok:true,body:{status:'completed'}}},close(){}};
  await new CvMaxInstaller({base:'http://atoll.test',channel:'private-channel',serviceConfig},{transport:fake}).install();
  assert.equal(fs.statSync(attachments).isDirectory(),true);assert.equal(fs.statSync(path.dirname(cacheFile)).isDirectory(),true);assert.equal(fs.statSync(path.dirname(stateFile)).isDirectory(),true);
 }finally{fs.rmSync(dir,{recursive:true,force:true})}
});

test('a failed learning handoff pauses the persisted task and returns its identity',async()=>{
 const fake=transport(),original=fake.request.bind(fake);fake.request=async(type,...args)=>type==='agent.new'?{ok:false,body:{code:'provider_unavailable'}}:original(type,...args);
 const client=new CvMaxClient({base:'http://atoll.test',channel:'private-channel'},{transport:fake});await client.open();
 const result=await client.submit({clientRequestId:'handoff-failed',currentPage:true});
 assert.equal(result.path,'paused');assert.equal(result.taskId,'task-fast');assert.equal(result.businessTaskPersisted,true);
 assert.deepEqual(fake.calls.find(c=>c[1]==='cvmax.control')[2],{taskId:'task-fast',kind:'pause'});
 assert.equal(fake.calls.filter(c=>c[1]==='cvmax.create').length,1);assert.equal(fake.calls.some(c=>c[0]==='frame'),false);
});
