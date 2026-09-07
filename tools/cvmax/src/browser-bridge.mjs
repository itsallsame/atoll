import {randomUUID} from 'node:crypto';
import {WebSocketServer} from 'ws';
// Each request is bound to a random ID AND the exact authenticated connection.
export class BrowserBridge {
 constructor({origin,token,port=18833,timeoutMs=3000,leaseMs=2500,openTimeoutMs=30000,openLeaseMs=25000,onEvent=async()=>({accepted:false})}){
  this.token=token;this.timeoutMs=timeoutMs;this.leaseMs=leaseMs;this.openTimeoutMs=openTimeoutMs;this.openLeaseMs=openLeaseMs;this.onEvent=onEvent;this.pending=new Map();this.socket=null;
  this.server=new WebSocketServer({host:'127.0.0.1',port,maxPayload:1024*1024,verifyClient:info=>info.origin===origin});
  this.server.on('connection',ws=>{
   let paired=false;const timer=setTimeout(()=>ws.close(),1500);
   ws.on('message',raw=>{let m;try{m=JSON.parse(raw)}catch{return ws.close();}
    if(!paired){if(m.kind!=='pair'||m.token!==this.token)return ws.close();paired=true;clearTimeout(timer);this.socket?.close();this.socket=ws;ws.send(JSON.stringify({kind:'paired'}));return;}
    if(m.kind==='event'){Promise.resolve(this.onEvent(m)).then(result=>ws.send(JSON.stringify({kind:'event_receipt',event:m.event,result}))).catch(error=>ws.send(JSON.stringify({kind:'event_receipt',event:m.event,error:error.message})));return;}
    const p=this.pending.get(m.id);if(m.kind!=='result'||!p||p.socket!==ws)return;
    this.pending.delete(m.id);clearTimeout(p.timer);m.error?p.reject(Error(m.error)):p.resolve(m.result);
   });
   ws.on('close',()=>{clearTimeout(timer);if(this.socket===ws)this.socket=null;for(const [id,p]of this.pending)if(p.socket===ws){clearTimeout(p.timer);this.pending.delete(id);p.reject(Error('browser_disconnected; effect unknown'));}});
  });
 }
 connected(){return this.socket?.readyState===1;}
 command(action,params={}){
  if(!this.connected())return Promise.reject(Error('browser_not_connected'));
  const socket=this.socket,id=randomUUID();
  return new Promise((resolve,reject)=>{
   const startsApplication=action==='act'&&params.actionSpec?.kind==='start',timeoutMs=action==='open'?this.openTimeoutMs:startsApplication?15000:this.timeoutMs,leaseMs=action==='open'?this.openLeaseMs:startsApplication?12000:this.leaseMs;
   const timer=setTimeout(()=>{this.pending.delete(id);reject(Error('browser_timeout; effect unknown'));},timeoutMs);
   this.pending.set(id,{socket,timer,resolve,reject});
   socket.send(JSON.stringify({kind:'command',id,action,...params,expiresAt:Date.now()+leaseMs}));
  });
 }
 async close(){for(const ws of this.server.clients)ws.terminate();await new Promise(resolve=>this.server.close(resolve));}
}
