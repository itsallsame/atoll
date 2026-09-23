import fs from 'node:fs';
import http from 'node:http';
import https from 'node:https';
import {createPublicKey,randomBytes,randomUUID} from 'node:crypto';
import {MysqlRecipeRegistry} from './mysql-recipe-registry.mjs';
import {RecipeRegistryApp} from './recipe-registry-app.mjs';
import {FixedWindowRateLimit} from './rate-limit.mjs';

export function loadEnv(file) {
  const env = {...process.env};
  if (!file) return env;
  for (const line of fs.readFileSync(file, 'utf8').split(/\r?\n/)) {
    const match = line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
    if (match) {
      const raw=match[2].trim(),doubleQuoted=raw.length>=2&&raw.startsWith('"')&&raw.endsWith('"'),singleQuoted=raw.length>=2&&raw.startsWith("'")&&raw.endsWith("'");
      env[match[1]]=doubleQuoted?JSON.parse(raw):singleQuoted?raw.slice(1,-1):raw;
    }
  }
  return env;
}

async function body(request) {
  const chunks=[]; let bytes=0;
  for await (const chunk of request) { bytes += chunk.length; if (bytes > 512 * 1024) throw new Error('request_too_large'); chunks.push(chunk); }
  return JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}');
}

function bearer(request) {
  const match=String(request.headers.authorization??'').match(/^Bearer (\S+)$/u);
  return match?.[1]??null;
}

export function createRegistryHandler({app, writeToken, authenticateWrite, adminToken=null, writeRateLimit=new FixedWindowRateLimit(), adminRateLimit=new FixedWindowRateLimit({limit:30})}) {
  if (!writeToken && !authenticateWrite) throw new Error('registry_write_auth_required');
  return async (request, response) => {
    const send=(status,value)=>{response.writeHead(status,{'content-type':'application/json','cache-control':'no-store','x-content-type-options':'nosniff'});response.end(JSON.stringify(value));};
    try {
      const url=new URL(request.url,'http://registry.local');
      if (request.method==='GET'&&url.pathname==='/livez') return send(200,{service:'cvmax-recipe-registry',live:true});
      if (request.method==='GET'&&url.pathname==='/readyz') {
        try { return send(200,{service:'cvmax-recipe-registry',...await app.health()}); }
        catch { return send(503,{service:'cvmax-recipe-registry',ready:false,error:'database_unavailable'}); }
      }
      if (request.method==='GET'&&url.pathname==='/health') return send(200,{service:'cvmax-recipe-registry',...await app.health()});
      const releaseMatch=url.pathname.match(/^\/v1\/releases\/([a-f0-9]{64})$/i);
      if (request.method==='GET'&&releaseMatch){const value=await app.release(releaseMatch[1]);return value?send(200,value):send(404,{error:'release_not_found'});}
      if(request.method==='GET'&&url.pathname==='/v1/signing-keys')return send(200,{keys:await app.signingKeys()});
      if (request.method==='POST'&&['/v1/candidates','/v1/feedback','/v1/metrics'].includes(url.pathname)) {
        const payload=await body(request),credential=bearer(request);
        const authorized=authenticateWrite?await authenticateWrite({installationId:payload.installationId,credential}):credential===writeToken;
        if(!authorized)return send(401,{error:'unauthorized'});
        writeRateLimit.consume(payload.installationId);
        if(url.pathname==='/v1/candidates')return send(200,await app.candidate(payload));
        if(url.pathname==='/v1/feedback')return send(200,await app.feedback(payload));
        return send(200,await app.metric(payload));
      }
      if(url.pathname.startsWith('/v1/admin/')){
        if(!adminToken||bearer(request)!==adminToken)return send(401,{error:'unauthorized'});adminRateLimit.consume(request.socket.remoteAddress??'admin');const payload=await body(request),operator=String(request.headers['x-cvmax-operator']??'admin').slice(0,120);
        if(request.method==='POST'&&url.pathname==='/v1/admin/installations'){const installationId=payload.installationId??randomUUID(),credential=randomBytes(32).toString('base64url');await app.provisionInstallation({installationId,credential,operator});return send(201,{installationId,credential})}
        if(request.method==='POST'&&url.pathname==='/v1/admin/installations/revoke')return send(200,await app.revokeInstallation({installationId:payload.installationId,operator}));
        const rollback=url.pathname.match(/^\/v1\/admin\/releases\/(\d+)\/rollback$/);if(request.method==='POST'&&rollback)return send(200,await app.rollback({releaseId:Number(rollback[1]),reason:payload.reason,operator}));
        if(request.method==='POST'&&url.pathname==='/v1/admin/signing-keys/rotate')return send(200,await app.rotateSigningKey({signingKeyId:payload.signingKeyId,privateKey:payload.privateKey,operator}));
        return send(404,{error:'not_found'});
      }
      return send(404,{error:'not_found'});
    } catch (error) {
      if (error.message==='request_too_large') return send(413,{error:error.message});
      if (error.statusCode===429){response.setHeader('retry-after',String(error.retryAfter??60));return send(429,{error:error.message});}
      if (error instanceof SyntaxError) return send(400,{error:'invalid_json'});
      return send(400,{error:error.message});
    }
  };
}

export function createRegistryShutdown({server,repository,onError=error=>console.error(`[cvmax-registry] shutdown failed: ${error.message}`)}) {
  let closing=false;
  return ()=>{
    if(closing)return;
    closing=true;
    server.close(()=>Promise.resolve(repository.close()).catch(onError));
  };
}

export async function startRegistryServer(env = process.env) {
  for (const name of ['CVMAX_REGISTRY_PRIVATE_KEY','CVMAX_REGISTRY_SIGNING_KEY_ID','CVMAX_REGISTRY_INSTALLATION_PEPPER']) {
    if (!env[name]) throw new Error(`${name}_required`);
  }
  const repository=MysqlRecipeRegistry.fromEnv(env);
  const privateKey=env.CVMAX_REGISTRY_PRIVATE_KEY?.replaceAll('\\n','\n'),signingKeyId=env.CVMAX_REGISTRY_SIGNING_KEY_ID,app=new RecipeRegistryApp({repository,privateKey,signingKeyId,installationPepper:env.CVMAX_REGISTRY_INSTALLATION_PEPPER,policy:{canaryInstallations:Number(env.CVMAX_REGISTRY_CANARY_INSTALLATIONS??3),releaseInstallations:Number(env.CVMAX_REGISTRY_RELEASE_INSTALLATIONS??5),suspendFailures:Number(env.CVMAX_REGISTRY_SUSPEND_FAILURES??2),maximumFailureRate:Number(env.CVMAX_REGISTRY_MAXIMUM_FAILURE_RATE??0.1)}}),knownKeys=await repository.signingKeys();if(!knownKeys.length)await repository.recordSigningKey({signingKeyId,publicKeyPem:createPublicKey(privateKey).export({type:'spki',format:'pem'}).toString()});else if(!knownKeys.some(key=>key.signingKeyId===signingKeyId))throw Error('signing_key_rotation_required');
  const authMode=env.CVMAX_REGISTRY_AUTH_MODE??(env.CVMAX_REGISTRY_WRITE_TOKEN?'static':'installation');
  if(!['static','installation'].includes(authMode))throw new Error('CVMAX_REGISTRY_AUTH_MODE_invalid');
  if(env.NODE_ENV==='production'&&(authMode!=='installation'||!env.CVMAX_REGISTRY_ADMIN_TOKEN))throw new Error('production_registry_auth_incomplete');
  const authenticateWrite=authMode==='installation'?({installationId,credential})=>repository.authenticateInstallation({installationHash:app.installationHash(installationId),credential,pepper:env.CVMAX_REGISTRY_INSTALLATION_PEPPER}):null;
  const production=env.NODE_ENV==='production',tlsCert=env.CVMAX_REGISTRY_TLS_CERT&&fs.readFileSync(env.CVMAX_REGISTRY_TLS_CERT),tlsKey=env.CVMAX_REGISTRY_TLS_KEY&&fs.readFileSync(env.CVMAX_REGISTRY_TLS_KEY);if(production&&!tlsCert&&!tlsKey&&env.CVMAX_REGISTRY_TRUST_TLS_PROXY!=='true')throw Error('production_tls_required');if(Boolean(tlsCert)!==Boolean(tlsKey))throw Error('registry_tls_config_incomplete');
  const handler=createRegistryHandler({app,writeToken:authMode==='static'?env.CVMAX_REGISTRY_WRITE_TOKEN:null,authenticateWrite,adminToken:env.CVMAX_REGISTRY_ADMIN_TOKEN});const server=tlsCert?https.createServer({cert:tlsCert,key:tlsKey},handler):http.createServer(handler);
  const port=Number(env.CVMAX_REGISTRY_PORT??8791),host=env.CVMAX_REGISTRY_HOST??'127.0.0.1';
  await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(port,host,resolve)});
  const close=createRegistryShutdown({server,repository});
  process.on('SIGTERM',close);process.on('SIGINT',close);
  return {server,repository,host,port};
}

if (process.argv[1] === new URL(import.meta.url).pathname) {
  const env=loadEnv(process.argv[2]);
  startRegistryServer(env).then(({host,port})=>console.log(`[cvmax-registry] listening on ${host}:${port}`)).catch(error=>{console.error(`[cvmax-registry] startup failed: ${error.message}`);process.exitCode=1});
}
