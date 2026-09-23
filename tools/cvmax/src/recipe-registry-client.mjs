import fs from 'node:fs';
import path from 'node:path';
import {randomUUID,verify} from 'node:crypto';
import {stableJson,verifyRecipeRelease} from './recipe-release.mjs';
import {publicRecipe} from './recipe.mjs';

const now = () => Date.now();

export class RecipeRegistryClient {
  constructor({baseUrl, credential, writeToken, installationId, publicKeys, cacheFile, cacheTtlMs = 86_400_000, readTimeoutMs = 5_000, writeTimeoutMs = 20_000, fetchImpl = fetch}) {
    if (!baseUrl || !installationId || !publicKeys || !cacheFile) throw new Error('recipe_registry_client_config_invalid');
    this.baseUrl = baseUrl.replace(/\/$/, '');
    this.writeToken = credential ?? writeToken;
    this.installationId = installationId;
    this.publicKeys = {...publicKeys};
    this.cacheFile = cacheFile;
    this.cacheTtlMs = cacheTtlMs;
    this.readTimeoutMs = readTimeoutMs;
    this.writeTimeoutMs = writeTimeoutMs;
    this.fetch = fetchImpl;
    this.cache = this.readCache();
    Object.assign(this.publicKeys,this.cache.trustedKeys??{});
  }

  readCache() {
    try { const value=JSON.parse(fs.readFileSync(this.cacheFile, 'utf8'));if(value.version!==3||!value.releases||!Array.isArray(value.queue))return {version:3,releases:{},queue:[]};return value; } catch { return {version:3,releases:{},queue:[]}; }
  }

  writeCache() {
    fs.mkdirSync(path.dirname(this.cacheFile), {recursive: true, mode: 0o700});
    const temporary = `${this.cacheFile}.tmp`;
    fs.writeFileSync(temporary, JSON.stringify(this.cache), {mode: 0o600});
    fs.renameSync(temporary, this.cacheFile);
  }

  cachedBundles() {
    const result = {};
    for (const [systemKey, entry] of Object.entries(this.cache.releases ?? {})) {
      if (now() - entry.cachedAt > this.cacheTtlMs) continue;
      try { result[systemKey] = verifyRecipeRelease(entry.release, this.publicKeys); } catch { delete this.cache.releases[systemKey]; }
    }
    return result;
  }

  async request(route, options = {}) {
    const {timeoutMs=this.readTimeoutMs,notFound,...fetchOptions}=options;
    const response = await this.fetch(`${this.baseUrl}${route}`, {signal: AbortSignal.timeout(timeoutMs), ...fetchOptions, headers:{...fetchOptions.body&&{'content-type':'application/json'},...this.writeToken&&{'authorization':`Bearer ${this.writeToken}`},...(fetchOptions.headers ?? {})}});
    if (response.status === 404 && Object.hasOwn(options, 'notFound')) return notFound;
    if (!response.ok) throw new Error(`recipe_registry_http_${response.status}`);
    return response.json();
  }

  async getReleased(systemKey) {
    try {
      const release = await this.request(`/v1/releases/${encodeURIComponent(systemKey)}`, {notFound:null});
      if(!release){delete this.cache.releases[systemKey];this.writeCache();return null;}
      try{verifyRecipeRelease(release,this.publicKeys)}catch(error){if(error.message!=='recipe_release_signature_invalid')throw error;await this.refreshSigningKeys();verifyRecipeRelease(release,this.publicKeys)}
      this.cache.releases[systemKey] = {release, cachedAt: now()};
      this.writeCache();
      void this.flushQueue().catch(()=>{});
      return structuredClone(release);
    } catch (error) {
      const cached = this.cache.releases?.[systemKey];
      if (cached && now() - cached.cachedAt <= this.cacheTtlMs) {
        verifyRecipeRelease(cached.release, this.publicKeys);
        return structuredClone(cached.release);
      }
      throw error;
    }
  }

  async refreshSigningKeys(){const response=await this.request('/v1/signing-keys'),pending=[...(response.keys??[])];let changed=true;while(changed&&pending.length){changed=false;for(let index=pending.length-1;index>=0;index--){const item=pending[index];if(this.publicKeys[item.signingKeyId]){pending.splice(index,1);continue}const predecessor=this.publicKeys[item.previousSigningKeyId];if(!predecessor||!item.transitionSignature)continue;const transition={kind:'cvmax-signing-key-transition',from:item.previousSigningKeyId,to:item.signingKeyId,publicKeyPem:item.publicKeyPem};if(!verify(null,Buffer.from(stableJson(transition)),predecessor,Buffer.from(item.transitionSignature,'base64')))throw Error('signing_key_transition_invalid');this.publicKeys[item.signingKeyId]=item.publicKeyPem;pending.splice(index,1);changed=true}}
    this.cache.trustedKeys=Object.fromEntries(Object.entries(this.publicKeys));this.writeCache();return{accepted:Object.keys(this.publicKeys).length,pending:pending.length}}

  async writeOrQueue(route, payload) {
    try { return await this.request(route,{method:'POST',timeoutMs:this.writeTimeoutMs,body:JSON.stringify(payload)}); }
    catch(error){if(this.cache.queue.length>=500)this.cache.queue.shift();this.cache.queue.push({id:randomUUID(),route,payload,queuedAt:now(),attempts:0});this.writeCache();return{queued:true,error:'recipe_registry_unavailable'};}
  }

  async flushQueue() {
    if (this.flushing || !this.cache.queue.length) return {flushed:0,remaining:this.cache.queue.length};
    this.flushing=true;let flushed=0;
    try{while(this.cache.queue.length){const item=this.cache.queue[0];try{await this.request(item.route,{method:'POST',timeoutMs:this.writeTimeoutMs,body:JSON.stringify(item.payload)});this.cache.queue.shift();flushed++;this.writeCache()}catch{item.attempts++;this.writeCache();break}}return{flushed,remaining:this.cache.queue.length}}
    finally{this.flushing=false}
  }

  submitCandidate({bundle, evidenceDigest, receiptId}) {
    return this.writeOrQueue('/v1/candidates',{installationId:this.installationId,bundle:publicRecipe(bundle),evidenceDigest,receiptId});
  }

  submitFeedback(feedback) {
    return this.writeOrQueue('/v1/feedback',{installationId:this.installationId,...feedback});
  }

  submitMetric(metric) {
    return this.writeOrQueue('/v1/metrics',{installationId:this.installationId,receiptId:randomUUID(),metric});
  }
}
