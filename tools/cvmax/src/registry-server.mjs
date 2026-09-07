import fs from 'node:fs';
import http from 'node:http';
import {MysqlExperienceRegistry} from './mysql-experience-registry.mjs';
import {ExperienceRegistryApp} from './experience-registry-app.mjs';

function loadEnv(file) {
  const env = {...process.env};
  if (!file) return env;
  for (const line of fs.readFileSync(file, 'utf8').split(/\r?\n/)) {
    const match = line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
    if (match) env[match[1]] = match[2];
  }
  return env;
}

async function body(request) {
  const chunks=[]; let bytes=0;
  for await (const chunk of request) { bytes += chunk.length; if (bytes > 512 * 1024) throw new Error('request_too_large'); chunks.push(chunk); }
  return JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}');
}

export function createRegistryHandler({app, writeToken}) {
  if (!writeToken) throw new Error('CVMAX_REGISTRY_WRITE_TOKEN_required');
  return async (request, response) => {
    const send=(status,value)=>{response.writeHead(status,{'content-type':'application/json','cache-control':'no-store'});response.end(JSON.stringify(value));};
    try {
      const url=new URL(request.url,'http://registry.local');
      if (request.method==='GET'&&url.pathname==='/health') return send(200,{service:'cvmax-experience-registry',...await app.health()});
      const releaseMatch=url.pathname.match(/^\/v1\/releases\/([a-f0-9]{64})$/i);
      if (request.method==='GET'&&releaseMatch){const value=await app.release(releaseMatch[1]);return value?send(200,value):send(404,{error:'release_not_found'});}
      if (request.headers.authorization!==`Bearer ${writeToken}`) return send(401,{error:'unauthorized'});
      if (request.method==='POST'&&url.pathname==='/v1/candidates') return send(200,await app.candidate(await body(request)));
      if (request.method==='POST'&&url.pathname==='/v1/feedback') return send(200,await app.feedback(await body(request)));
      return send(404,{error:'not_found'});
    } catch (error) { return send(400,{error:error.message}); }
  };
}

if (process.argv[1] === new URL(import.meta.url).pathname) {
  const env=loadEnv(process.argv[2]);
  const repository=MysqlExperienceRegistry.fromEnv(env);
  const app=new ExperienceRegistryApp({repository,privateKey:env.CVMAX_REGISTRY_PRIVATE_KEY?.replaceAll('\\n','\n'),signingKeyId:env.CVMAX_REGISTRY_SIGNING_KEY_ID,installationPepper:env.CVMAX_REGISTRY_INSTALLATION_PEPPER,policy:{canaryInstallations:Number(env.CVMAX_REGISTRY_CANARY_INSTALLATIONS??3),releaseInstallations:Number(env.CVMAX_REGISTRY_RELEASE_INSTALLATIONS??5),suspendFailures:Number(env.CVMAX_REGISTRY_SUSPEND_FAILURES??2),maximumFailureRate:Number(env.CVMAX_REGISTRY_MAXIMUM_FAILURE_RATE??0.1)}});
  const server=http.createServer(createRegistryHandler({app,writeToken:env.CVMAX_REGISTRY_WRITE_TOKEN}));
  server.listen(Number(env.CVMAX_REGISTRY_PORT??8791),env.CVMAX_REGISTRY_HOST??'127.0.0.1');
  const close=()=>server.close(()=>repository.close());process.on('SIGTERM',close);process.on('SIGINT',close);
}
