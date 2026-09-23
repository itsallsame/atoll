import test from 'node:test';
import assert from 'node:assert/strict';
import WebSocket from 'ws';
import {BrowserBridge} from '../src/browser-bridge.mjs';
import {CVMAX_PROTOCOL} from '../src/protocol.mjs';

const once = (target, event) => new Promise((resolve, reject) => {
  target.once(event, (...args) => resolve(args));
  if (event !== 'error') target.once('error', reject);
});

async function pair(port, family) {
  const socket = new WebSocket(`ws://127.0.0.1:${port}/`, {origin:'chrome-extension://cvmax-test'});
  await once(socket, 'open');
  socket.send(JSON.stringify({kind:'pair',token:'test-token',protocol:CVMAX_PROTOCOL.extension,browser:{family}}));
  const [raw] = await once(socket, 'message');
  return {socket, receipt:JSON.parse(raw)};
}

test('browser bridge keeps both Chromium connections and dispatches only to configured Edge',async()=>{
 const bridge=new BrowserBridge({origin:'chrome-extension://cvmax-test',token:'test-token',preferredFamily:'edge',port:0});
 try{
  if(!bridge.server.address())await once(bridge.server,'listening');
  const port=bridge.server.address().port,chrome=await pair(port,'chrome'),edge=await pair(port,'edge');
  assert.equal(chrome.receipt.selected,false);
  assert.equal(edge.receipt.selected,true);
  assert.deepEqual(bridge.status(),{connected:true,preferredFamily:'edge',selectedFamily:'edge',connectedFamilies:['chrome','edge']});
  let chromeCommands=0;chrome.socket.on('message',()=>chromeCommands++);
  edge.socket.once('message',raw=>{const command=JSON.parse(raw);edge.socket.send(JSON.stringify({kind:'result',id:command.id,result:{tabId:77,url:'https://jobs.example/'}}))});
  assert.deepEqual(await bridge.command('current'),{tabId:77,url:'https://jobs.example/'});
  assert.equal(chromeCommands,0);
 }finally{await bridge.close()}
});

test('browser bridge fails closed when configured Edge is absent even if Chrome is connected',async()=>{
 const bridge=new BrowserBridge({origin:'chrome-extension://cvmax-test',token:'test-token',preferredFamily:'edge',port:0});
 try{
  if(!bridge.server.address())await once(bridge.server,'listening');
  await pair(bridge.server.address().port,'chrome');
  assert.deepEqual(bridge.status(),{connected:false,preferredFamily:'edge',selectedFamily:null,connectedFamilies:['chrome']});
  await assert.rejects(bridge.command('current'),/browser_not_connected/);
 }finally{await bridge.close()}
});

test('browser bridge gives read-only observations longer response time without an invalid extension lease',async()=>{
 const bridge=new BrowserBridge({origin:'chrome-extension://cvmax-test',token:'test-token',preferredFamily:'edge',port:0,timeoutMs:20,leaseMs:10,observeTimeoutMs:80,observeLeaseMs:70_000});
 try{
  assert.equal(bridge.timeoutMs,20);assert.equal(bridge.leaseMs,10);assert.equal(bridge.observeTimeoutMs,80);assert.equal(bridge.observeLeaseMs,10_000);
 }finally{await bridge.close()}
});
