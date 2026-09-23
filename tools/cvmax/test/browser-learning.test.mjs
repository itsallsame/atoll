import test from 'node:test';import assert from 'node:assert/strict';import fs from 'node:fs';import os from 'node:os';import path from 'node:path';import {fileURLToPath} from 'node:url';
import {CvMaxService} from '../src/service.mjs';import {releaseCoverage} from '../src/recipe-compiler.mjs';
const runtime=process.env.CVMAX_PLAYWRIGHT_MODULE;
test('real plugin DOM: interpret, learn, verify, reset and replay another resume', {skip:!runtime,timeout:30000}, async()=>{
 const {chromium}=await import(runtime),browser=await chromium.launch({headless:true,executablePath:process.env.CVMAX_CHROME_BINARY});const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cvmax-browser-learning-'));
 try{
  const page=await browser.newPage();await page.route('**/*',route=>route.fulfill({contentType:'text/html; charset=utf-8',body:'<!doctype html><html><head><meta charset="utf-8"></head><body><h1>填写简历</h1><label for="name">姓名</label><input id="name" type="text"><script>document.querySelector("#name").addEventListener("input",event=>{if(document.activeElement!==event.target)event.target.value=""})</script></body></html>'}));
  const url='https://cvmax-fixture.invalid/apply';
  async function reset(){await page.goto(url);await page.evaluate(()=>{globalThis.chrome={runtime:{id:'fixture',getManifest:()=>({version:'fixture'}),onMessage:{addListener(){},removeListener(){}}},storage:{local:{set:async()=>{}}}}});for(const script of ['evidence.js','content-logic.js','content.js'])await page.addScriptTag({path:fileURLToPath(new URL('../extension/'+script,import.meta.url))})}
  await reset();let writes=0;
  const service=new CvMaxService({file:path.join(dir,'state.json'),attachments:{root:path.join(dir,'attachments'),prefix:'private:'},browserStatus:()=>true,browser:async(action,args)=>{
   if(action==='stop'||action==='progress')return{accepted:true,stopped:action==='stop'};
   if(action==='act')writes++;
   // Test adapter replaces only the Chrome transport, executes the real content command.
   const result=await page.evaluate(message=>new Promise(resolve=>globalThis.__cvmaxContentListener(message,{id:'fixture'},resolve)),{...args,cvmax:true,action,expiresAt:Date.now()+5000,progress:undefined});if(result.error)throw Error(result.error);return result;
  }});
  function create(id,value){service.request_save({clientRequestId:id,learningMode:'prelearn',currentPage:true,facts:[{id:'candidate.name',value,sourceRef:'synthetic:'+id}]});return service.create({clientRequestId:id,targets:[{url,tabId:1}]})}
  let task=create('first','测试甲');task=service.claim({taskId:task.taskId,expectedRevision:task.revision});const observed=await service.observe({taskId:task.taskId});const field=observed.snapshot.rawFields.find(f=>f.label.includes('姓名'));assert.ok(field,JSON.stringify(observed.snapshot.rawFields));
  const interpreted=service.learning_interpret({taskId:task.taskId,expectedRevision:observed.task.revision,observationId:observed.observationId,mappings:[{kind:'field',ref:field.ref,semanticId:'candidate.name'}]});
  const row=interpreted.coverage.find(item=>item.fieldRef===field.ref),covered=service.coverage_review({taskId:task.taskId,expectedRevision:interpreted.task.revision,items:[{controlRef:row.controlRef,disposition:'mapped',factId:'candidate.name'}]});
  const actions=[{kind:'fill',fieldRef:field.ref,factId:'candidate.name'}],proposal=service.learning_propose({taskId:task.taskId,expectedRevision:covered.revision,observationId:observed.observationId,question:'姓名字段能保留事实吗',hypothesis:'原生文本框可填写',expected:'回读姓名一致',actions});
  const filled=await service.run_adaptive_plan({taskId:task.taskId,expectedRevision:proposal.task.revision,actions});assert.equal(filled.task.state,'finished',JSON.stringify({reason:filled.reason,operation:filled.task.applications[0].operation,plan:filled.task.applications[0].applicationPlan}));assert.equal(await page.locator('#name').inputValue(),'测试甲');
  await reset();task=create('second','测试乙');const replay=await service.run_recipe({taskId:task.taskId,expectedRevision:task.revision});assert.equal(replay.path,'recipe');assert.equal(replay.task.state,'finished');assert.equal(await page.locator('#name').inputValue(),'测试乙');assert.equal(writes,2);assert.equal(releaseCoverage(Object.values(service.store.data.recipes)[0]).eligible,true);
 }finally{await browser.close();fs.rmSync(dir,{recursive:true,force:true})}
});

test('real plugin DOM probes an ongoing date option and fails closed when it is absent',{skip:!runtime,timeout:30000},async()=>{
 const {chromium}=await import(runtime),browser=await chromium.launch({headless:true,executablePath:process.env.CVMAX_CHROME_BINARY});
 try{
  const page=await browser.newPage(),url='https://cvmax-date-fixture.invalid/apply';
  await page.route('**/*',route=>route.fulfill({contentType:'text/html; charset=utf-8',body:`<!doctype html><html><head><meta charset="utf-8"></head><body><h1>项目经历</h1><label for="end">结束日期</label><input id="end" type="text" readonly placeholder="结束日期"><div id="picker" role="dialog" style="display:none;position:fixed;left:100px;top:100px;width:250px;height:90px;background:white;z-index:1000"><button id="ongoing" type="button">至今</button></div><script>document.querySelector('#end').addEventListener('click',()=>document.querySelector('#picker').style.display='block');document.querySelector('#ongoing').addEventListener('click',()=>{document.querySelector('#end').value='至今';document.querySelector('#picker').style.display='none'})</script></body></html>`}));
  async function reset(){await page.goto(url);await page.evaluate(()=>{globalThis.chrome={runtime:{id:'fixture',getManifest:()=>({version:'fixture'}),onMessage:{addListener(){},removeListener(){}}},storage:{local:{set:async()=>{}}}}});for(const script of ['evidence.js','content-logic.js','content.js'])await page.addScriptTag({path:fileURLToPath(new URL('../extension/'+script,import.meta.url))})}
  const command=async(action,actionSpec)=>page.evaluate(message=>new Promise(resolve=>globalThis.__cvmaxContentListener(message,{id:'fixture'},resolve)),{cvmax:true,action,targetURL:url,expiresAt:Date.now()+5000,actionSpec});
  await reset();let observed=await command('observe'),field=observed.fields.find(item=>item.label.includes('结束日期'));
  assert.ok(field);let result=await page.evaluate(({ref,epoch,url})=>new Promise(resolve=>globalThis.__cvmaxContentListener({cvmax:true,action:'act',targetURL:url,expiresAt:Date.now()+5000,documentEpoch:epoch,actionSpec:{kind:'select',fieldRef:ref,value:'至今',before:''}},{id:'fixture'},resolve)),{ref:field.ref,epoch:observed.documentEpoch,url});
  assert.equal(result.accepted,true,JSON.stringify(result));assert.equal(await page.locator('#end').inputValue(),'至今');
  await reset();await page.locator('#ongoing').evaluate(element=>element.remove());observed=await command('observe');field=observed.fields.find(item=>item.label.includes('结束日期'));
  result=await page.evaluate(({ref,epoch,url})=>new Promise(resolve=>globalThis.__cvmaxContentListener({cvmax:true,action:'act',targetURL:url,expiresAt:Date.now()+5000,documentEpoch:epoch,actionSpec:{kind:'select',fieldRef:ref,value:'至今',before:''}},{id:'fixture'},resolve)),{ref:field.ref,epoch:observed.documentEpoch,url});
  assert.match(result.error,/^option_not_found:ongoing_date$/);assert.equal(await page.locator('#end').inputValue(),'');
 }finally{await browser.close()}
});

test('real plugin DOM selects an exact option from a read-only choose field',{skip:!runtime,timeout:30000},async()=>{
 const {chromium}=await import(runtime),browser=await chromium.launch({headless:true,executablePath:process.env.CVMAX_CHROME_BINARY});
 try{
  const page=await browser.newPage(),url='https://cvmax-choice-fixture.invalid/apply';
  await page.route('**/*',route=>route.fulfill({contentType:'text/html; charset=utf-8',body:'<!doctype html><html><head><meta charset="utf-8"></head><body><label for="place">籍贯</label><div><input id="place" type="text" readonly placeholder="请选择"></div><div id="options" role="listbox" style="display:none;position:fixed;left:100px;top:100px;width:180px;height:80px;background:white;z-index:1000"><div role="option" tabindex="0">北京</div><div role="option" tabindex="0">上海</div></div><script>document.querySelector("#place").addEventListener("click",()=>document.querySelector("#options").style.display="block");for(const option of document.querySelectorAll("[role=option]"))option.addEventListener("click",()=>{document.querySelector("#place").value=option.textContent;document.querySelector("#options").style.display="none"})</script></body></html>'}));
  await page.goto(url);
  await page.evaluate(()=>{globalThis.chrome={runtime:{id:'fixture',getManifest:()=>({version:'fixture'}),onMessage:{addListener(){},removeListener(){}}},storage:{local:{set:async()=>{}}}}});
  for(const script of ['evidence.js','content-logic.js','content.js'])await page.addScriptTag({path:fileURLToPath(new URL('../extension/'+script,import.meta.url))});
  const invoke=async(action,actionSpec,documentEpoch)=>page.evaluate(message=>new Promise(resolve=>globalThis.__cvmaxContentListener(message,{id:'fixture'},resolve)),{cvmax:true,action,targetURL:url,expiresAt:Date.now()+5000,documentEpoch,actionSpec});
  const observed=await invoke('observe'),field=observed.fields.find(item=>item.ref&&item.readOnly&&item.placeholder==='请选择');
  assert.ok(field);const result=await invoke('act',{kind:'select',fieldRef:field.ref,value:'北京',before:''},observed.documentEpoch);
  assert.equal(result.accepted,true,JSON.stringify(result));assert.equal(await page.locator('#place').inputValue(),'北京');
 }finally{await browser.close()}
});

test('real plugin DOM selects a repeated municipality path only after the leaf is exposed',{skip:!runtime,timeout:30000},async()=>{
 const {chromium}=await import(runtime),browser=await chromium.launch({headless:true,executablePath:process.env.CVMAX_CHROME_BINARY});
 try{
  const page=await browser.newPage(),url='https://cvmax-cascader-fixture.invalid/apply';
  await page.route('**/*',route=>route.fulfill({contentType:'text/html; charset=utf-8',body:'<!doctype html><html><head><meta charset="utf-8"></head><body><label for="place">籍贯</label><input id="place" type="text" readonly placeholder="请选择"><div id="menu" style="display:none;position:fixed;left:100px;top:100px;background:white;z-index:1000"><button id="province" type="button">北京市</button><button id="city" type="button" style="display:none">北京市-北京市</button></div><script>document.querySelector("#place").addEventListener("click",()=>document.querySelector("#menu").style.display="block");document.querySelector("#province").addEventListener("click",()=>document.querySelector("#city").style.display="block");document.querySelector("#city").addEventListener("click",()=>{document.querySelector("#place").value="北京市-北京市";document.querySelector("#menu").style.display="none"})</script></body></html>'}));
  await page.goto(url);await page.evaluate(()=>{globalThis.chrome={runtime:{id:'fixture',getManifest:()=>({version:'fixture'}),onMessage:{addListener(){},removeListener(){}}},storage:{local:{set:async()=>{}}}}});for(const script of ['evidence.js','content-logic.js','content.js'])await page.addScriptTag({path:fileURLToPath(new URL('../extension/'+script,import.meta.url))});
  const invoke=async(action,actionSpec,documentEpoch)=>page.evaluate(message=>new Promise(resolve=>globalThis.__cvmaxContentListener(message,{id:'fixture'},resolve)),{cvmax:true,action,targetURL:url,expiresAt:Date.now()+5000,documentEpoch,actionSpec});
  const observed=await invoke('observe'),field=observed.fields.find(item=>item.placeholder==='请选择');assert.ok(field);
  const result=await invoke('act',{kind:'select',fieldRef:field.ref,value:'北京',before:''},observed.documentEpoch);
  assert.equal(result.accepted,true,JSON.stringify(result));assert.equal(result.normalized,true);assert.equal(result.value,'北京市-北京市');assert.equal(await page.locator('#place').inputValue(),'北京市-北京市');
 }finally{await browser.close()}
});
