import {randomUUID} from 'node:crypto';
import {WebSocketServer} from 'ws';
import {CVMAX_PROTOCOL,assertProtocolCompatible} from './protocol.mjs';
// Each request is bound to a random ID AND the exact authenticated connection.
export class BrowserBridge {
 constructor({origin,token,preferredFamily=null,port=18833,timeoutMs=10000,leaseMs=8000,observeTimeoutMs=50000,observeLeaseMs=8000,openTimeoutMs=30000,openLeaseMs=25000,onEvent=async()=>({accepted:false})}){
  if(preferredFamily!=null&&!['edge','chrome','chromium'].includes(preferredFamily))throw Error('browser_preferred_family_invalid');
  this.token=token;this.preferredFamily=preferredFamily;this.timeoutMs=timeoutMs;this.leaseMs=leaseMs;this.observeTimeoutMs=Math.max(timeoutMs,observeTimeoutMs);this.observeLeaseMs=Math.min(10_000,Math.max(leaseMs,observeLeaseMs));this.openTimeoutMs=openTimeoutMs;this.openLeaseMs=openLeaseMs;this.onEvent=onEvent;this.pending=new Map();this.sockets=new Map();this.lastPairedFamily=null;
  this.server=new WebSocketServer({host:'127.0.0.1',port,maxPayload:1024*1024,verifyClient:info=>info.origin===origin});
  this.server.on('connection',ws=>{
   let paired=false,family=null;const timer=setTimeout(()=>ws.close(),1500);
   ws.on('message',raw=>{let m;try{m=JSON.parse(raw)}catch{return ws.close();}
    if(!paired){if(m.kind!=='pair'||m.token!==this.token||!['edge','chrome','chromium'].includes(m.browser?.family))return ws.close();try{assertProtocolCompatible('extension',m.protocol)}catch{return ws.close(4406,'protocol_incompatible')}paired=true;family=m.browser.family;clearTimeout(timer);const previous=this.sockets.get(family);if(previous&&previous!==ws)previous.close(4000,'replaced_same_browser_family');this.sockets.set(family,ws);this.lastPairedFamily=family;ws.send(JSON.stringify({kind:'paired',protocol:CVMAX_PROTOCOL.extension,browser:{family},selected:this.selectedFamily()===family}));return;}
    if(m.kind==='event'){Promise.resolve(this.onEvent(m)).then(result=>ws.send(JSON.stringify({kind:'event_receipt',event:m.event,result}))).catch(error=>ws.send(JSON.stringify({kind:'event_receipt',event:m.event,error:error.message})));return;}
    const p=this.pending.get(m.id);if(m.kind!=='result'||!p||p.socket!==ws)return;
    this.pending.delete(m.id);clearTimeout(p.timer);m.error?p.reject(Error(m.error)):p.resolve(m.result);
   });
   ws.on('close',()=>{clearTimeout(timer);if(family&&this.sockets.get(family)===ws)this.sockets.delete(family);for(const [id,p]of this.pending)if(p.socket===ws){clearTimeout(p.timer);this.pending.delete(id);p.reject(Error(p.mayHaveEffect?'browser_disconnected; effect unknown':'browser_disconnected'));}});
  });
 }
 selectedFamily(){return this.preferredFamily??this.lastPairedFamily;}
 selectedSocket(){return this.sockets.get(this.selectedFamily())??null;}
 status(){const selectedFamily=this.selectedFamily(),socket=this.selectedSocket();return{connected:socket?.readyState===1,preferredFamily:this.preferredFamily,selectedFamily:socket?.readyState===1?selectedFamily:null,connectedFamilies:[...this.sockets.entries()].filter(([,item])=>item.readyState===1).map(([item])=>item).sort()};}
 connected(){return this.status().connected;}
 command(action,params={}){
  if(!this.connected())return Promise.reject(Error('browser_not_connected'));
  const socket=this.selectedSocket(),id=randomUUID();
  return new Promise((resolve,reject)=>{
   const startsApplication=action==='act'&&params.actionSpec?.kind==='start',refreshesPage=action==='refresh',observesPage=action==='observe',mayHaveEffect=['act','open','refresh'].includes(action),timeoutMs=action==='open'?this.openTimeoutMs:startsApplication||refreshesPage?32000:observesPage?this.observeTimeoutMs:this.timeoutMs,leaseMs=action==='open'?this.openLeaseMs:startsApplication||refreshesPage?28000:observesPage?this.observeLeaseMs:this.leaseMs;
   const timer=setTimeout(()=>{this.pending.delete(id);reject(Error(mayHaveEffect?'browser_timeout; effect unknown':'browser_timeout'));},timeoutMs);
   this.pending.set(id,{socket,timer,resolve,reject,mayHaveEffect});
   socket.send(JSON.stringify({kind:'command',id,action,...params,protocol:CVMAX_PROTOCOL.extension,expiresAt:Date.now()+leaseMs}));
  });
 }
 async close(){for(const ws of this.server.clients)ws.terminate();await new Promise(resolve=>this.server.close(resolve));}
}
