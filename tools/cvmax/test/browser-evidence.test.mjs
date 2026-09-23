import test from 'node:test';
import assert from 'node:assert/strict';
import {fileURLToPath} from 'node:url';
const modulePath=process.env.CVMAX_PLAYWRIGHT_MODULE;

test('real DOM evidence: custom overlay, drawer, local expansion, noise, and private inputs', {skip:!modulePath}, async()=>{
 const {chromium}=await import(modulePath);
 const browser=await chromium.launch({headless:true,executablePath:process.env.CVMAX_CHROME_BINARY});
 try{
  const page=await browser.newPage();
  await page.setContent('<button id="apply">申请职位</button><input value="private-test-secret"><div id="body"></div>');
  await page.addScriptTag({path:fileURLToPath(new URL('../extension/evidence.js',import.meta.url))});
  const read=()=>page.evaluate(()=>{const refs=new WeakMap();let i=0;return CvMaxEvidence.capture(el=>{if(!refs.has(el))refs.set(el,'n'+ ++i);return refs.get(el)})});
  const initial=await read();assert.equal(initial.layers.length,0);assert.equal(JSON.stringify(initial).includes('private-test-secret'),false);
  await page.evaluate(()=>document.querySelector('#body').innerHTML='<div style="position:fixed;inset:0;background:#0008;z-index:200"><div id="custom" style="position:absolute;left:20%;top:20%;width:350px;height:180px;background:white"><h2>认证提示</h2><button>再想想</button><button>去认证</button></div></div>');
  const modal=await read();assert.ok(modal.layers.length>0);assert.notEqual(initial.interactionDigest,modal.interactionDigest);
  assert.equal(await page.evaluate(()=>CvMaxEvidence.activeSurface().id),'custom');
  await page.evaluate(()=>document.querySelector('#body').innerHTML='<aside id="drawer" style="position:fixed;right:0;top:0;width:320px;height:100vh;background:white;z-index:200"><button>关闭</button><input></aside>');
  assert.equal(await page.evaluate(()=>CvMaxEvidence.activeSurface().id),'drawer');
  await page.evaluate(()=>document.querySelector('#body').innerHTML='<aside id="assistant" style="position:fixed;right:20px;bottom:20px;width:136px;height:164px;background:white;z-index:900"><button aria-label="打开招聘助手"></button></aside>');
  assert.equal(await page.evaluate(()=>CvMaxEvidence.activeSurface()),null);
  const assistant=await read();assert.ok(assistant.layers.some(layer=>layer.blocking===false));
  await page.evaluate(()=>document.querySelector('#body').innerHTML='<section><h2>教育经历</h2><input><button>保存经历</button></section>');
  const expanded=await read();assert.equal(expanded.layers.length,0);assert.notEqual(expanded.interactionDigest,initial.interactionDigest);
  await page.evaluate(()=>document.querySelector('#body').insertAdjacentHTML('beforeend','<span id="clock">00:01</span>'));
  const noise=await read();assert.equal(noise.interactionDigest,expanded.interactionDigest);
  await page.evaluate(()=>document.querySelector('#clock').textContent='00:02');assert.equal((await read()).interactionDigest,noise.interactionDigest);
  await page.setContent('<div style="position:fixed;top:0;left:0;width:100vw;height:80px;z-index:1000;background:white"><a href="#">首页</a><button>职位</button></div><main style="padding-top:900px"><button>投递</button></main>');
  assert.equal(await page.evaluate(()=>CvMaxEvidence.activeSurface()),null);
  const navigation=await read();assert.ok(navigation.layers.length>0);assert.equal(navigation.layers[0].blocking,false);
  await page.evaluate(()=>{globalThis.chrome={runtime:{id:'fixture',getManifest:()=>({version:'fixture'}),onMessage:{addListener(){},removeListener(){}}}};if(!crypto.randomUUID)crypto.randomUUID=()=> 'fixture-document';});
  await page.addScriptTag({path:fileURLToPath(new URL('../extension/content-logic.js',import.meta.url))});
  await page.addScriptTag({path:fileURLToPath(new URL('../extension/content.js',import.meta.url))});
  const observed=await page.evaluate(()=>new Promise(resolve=>globalThis.__cvmaxContentListener({cvmax:true,action:'observe',targetURL:location.href,expiresAt:Date.now()+5000},{id:'fixture'},resolve)));
  assert.equal(observed.error,undefined);assert.equal(observed.activeDialog,null);assert.equal(observed.formStage,'detail');
  assert.ok(observed.controls.some(c=>c.kind==='start'&&c.label==='投递'&&c.safe));
  await page.setContent(`<aside id="resume-nav" style="position:fixed;left:0;top:0;width:240px;height:100vh;z-index:10;background:white">${Array.from({length:8},(_,index)=>`<a href="#section-${index}">简历章节 ${index}</a>`).join('')}</aside><main style="margin-left:260px"><label>上传文件简历<input id="resume-file" type="file" accept=".pdf,.doc"></label><input id="name"></main>`);
  assert.equal(await page.evaluate(()=>CvMaxEvidence.activeSurface()),null);
  const rail=await read();assert.ok(rail.layers.some(layer=>layer.ref&&layer.blocking===false));
  const railObserved=await page.evaluate(()=>new Promise(resolve=>globalThis.__cvmaxContentListener({cvmax:true,action:'observe',targetURL:location.href,expiresAt:Date.now()+5000},{id:'fixture'},resolve)));
  assert.equal(railObserved.activeDialog,null);assert.equal(railObserved.formStage,'apply');assert.ok(railObserved.fields.some(field=>field.type==='file'));

  await page.setContent('<button id="covered" style="position:fixed;left:100px;top:20px;width:120px;height:40px;z-index:1">立即投递</button><div id="cover" style="position:fixed;left:0;top:0;width:100vw;height:80px;z-index:10;background:white"></div><button id="target" style="position:fixed;left:100px;top:112px;width:120px;height:40px;z-index:20">立即投递</button>');
  const candidates=await page.evaluate(()=>CvMaxEvidence.interactionCandidates(document).map(element=>element.id));
  assert.deepEqual(candidates,['target']);
  assert.equal(await page.evaluate(()=>CvMaxEvidence.resolveInteraction(document,{surface:'page',label:'立即投递',role:'button',tag:'button',type:'',ordinal:0})?.id),'target');
  const duplicateStart=await page.evaluate(()=>new Promise(resolve=>globalThis.__cvmaxContentListener({cvmax:true,action:'observe',targetURL:location.href,expiresAt:Date.now()+5000},{id:'fixture'},resolve)));
  assert.equal(duplicateStart.controls.filter(control=>control.kind==='start').length,2);
  assert.deepEqual(duplicateStart.interactions.filter(control=>control.label==='立即投递').map(control=>control.ref),[duplicateStart.controls.find(control=>control.ordinal===1).ref]);

  await page.setContent('<div id="body"></div>');
  await page.evaluate(()=>document.querySelector('#body').innerHTML='<iframe title="unread frame"></iframe>');
  assert.equal((await read()).complete,false);assert.equal((await read()).fieldInventoryComplete,false);
  await page.evaluate(()=>document.querySelector('iframe').style.display='none');
  assert.equal((await read()).fieldInventoryComplete,true);
  await page.evaluate(()=>{document.querySelector('#body').innerHTML='<div id="widget"></div>';document.querySelector('#widget').attachShadow({mode:'open'}).innerHTML='<button>打开助手</button>'});
  assert.equal((await read()).fieldInventoryComplete,true);
  await page.evaluate(()=>document.querySelector('#widget').shadowRoot.innerHTML='<input aria-label="隐藏表单字段">');
  assert.equal((await read()).fieldInventoryComplete,false);
  await page.evaluate(()=>document.querySelector('#body').innerHTML=Array.from({length:410},()=>'<button>选项</button>').join(''));
  const truncated=await read();assert.equal(truncated.complete,false);assert.equal(truncated.fieldInventoryComplete,true);assert.equal(truncated.nodes.length,400);
 }finally{await browser.close()}
});
