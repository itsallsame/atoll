import fs from 'node:fs';
import path from 'node:path';
import {randomUUID} from 'node:crypto';
import {verifyExperienceRelease} from './experience-release.mjs';
import {publicExperienceBundle} from './public-experience.mjs';

const now = () => Date.now();

export class ExperienceRegistryClient {
  constructor({baseUrl, writeToken, installationId, publicKeys, cacheFile, cacheTtlMs = 86_400_000, readTimeoutMs = 5_000, writeTimeoutMs = 20_000, fetchImpl = fetch}) {
    if (!baseUrl || !installationId || !publicKeys || !cacheFile) throw new Error('experience_registry_client_config_invalid');
    this.baseUrl = baseUrl.replace(/\/$/, '');
    this.writeToken = writeToken;
    this.installationId = installationId;
    this.publicKeys = publicKeys;
    this.cacheFile = cacheFile;
    this.cacheTtlMs = cacheTtlMs;
    this.readTimeoutMs = readTimeoutMs;
    this.writeTimeoutMs = writeTimeoutMs;
    this.fetch = fetchImpl;
    this.cache = this.readCache();
  }

  readCache() {
    try { const value=JSON.parse(fs.readFileSync(this.cacheFile, 'utf8'));value.queue??=[];return value; } catch { return {version: 1, releases: {}, queue: []}; }
  }

  writeCache() {
    fs.mkdirSync(path.dirname(this.cacheFile), {recursive: true, mode: 0o700});
    const temporary = `${this.cacheFile}.tmp`;
    fs.writeFileSync(temporary, JSON.stringify(this.cache), {mode: 0o600});
    fs.renameSync(temporary, this.cacheFile);
  }

  cachedBundles() {
    const result = {};
    for (const [familyKey, entry] of Object.entries(this.cache.releases ?? {})) {
      if (now() - entry.cachedAt > this.cacheTtlMs) continue;
      try { result[familyKey] = verifyExperienceRelease(entry.release, this.publicKeys); } catch { delete this.cache.releases[familyKey]; }
    }
    return result;
  }

  async request(route, options = {}) {
    const {timeoutMs=this.readTimeoutMs,...fetchOptions}=options;
    const response = await this.fetch(`${this.baseUrl}${route}`, {signal: AbortSignal.timeout(timeoutMs), ...fetchOptions, headers:{...fetchOptions.body&&{'content-type':'application/json'},...this.writeToken&&{'authorization':`Bearer ${this.writeToken}`},...(fetchOptions.headers ?? {})}});
    if (!response.ok) throw new Error(`experience_registry_http_${response.status}`);
    return response.json();
  }

  async getReleased(familyKey) {
    try {
      const release = await this.request(`/v1/releases/${encodeURIComponent(familyKey)}`);
      if (!release) return null;
      verifyExperienceRelease(release, this.publicKeys);
      this.cache.releases[familyKey] = {release, cachedAt: now()};
      this.writeCache();
      void this.flushQueue().catch(()=>{});
      return structuredClone(release);
    } catch (error) {
      const cached = this.cache.releases?.[familyKey];
      if (cached && now() - cached.cachedAt <= this.cacheTtlMs) {
        verifyExperienceRelease(cached.release, this.publicKeys);
        return structuredClone(cached.release);
      }
      throw error;
    }
  }

  async writeOrQueue(route, payload) {
    try { return await this.request(route,{method:'POST',timeoutMs:this.writeTimeoutMs,body:JSON.stringify(payload)}); }
    catch(error){if(this.cache.queue.length>=500)this.cache.queue.shift();this.cache.queue.push({id:randomUUID(),route,payload,queuedAt:now(),attempts:0});this.writeCache();return{queued:true,error:'experience_registry_unavailable'};}
  }

  async flushQueue() {
    if (this.flushing || !this.cache.queue.length) return {flushed:0,remaining:this.cache.queue.length};
    this.flushing=true;let flushed=0;
    try{while(this.cache.queue.length){const item=this.cache.queue[0];try{await this.request(item.route,{method:'POST',timeoutMs:this.writeTimeoutMs,body:JSON.stringify(item.payload)});this.cache.queue.shift();flushed++;this.writeCache()}catch{item.attempts++;this.writeCache();break}}return{flushed,remaining:this.cache.queue.length}}
    finally{this.flushing=false}
  }

  submitCandidate({bundle, evidenceDigest, receiptId}) {
    return this.writeOrQueue('/v1/candidates',{installationId:this.installationId,bundle:publicExperienceBundle(bundle),evidenceDigest,receiptId});
  }

  submitFeedback(feedback) {
    return this.writeOrQueue('/v1/feedback',{installationId:this.installationId,...feedback});
  }
}
