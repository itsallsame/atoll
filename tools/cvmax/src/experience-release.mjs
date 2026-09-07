import {createHash, sign, verify} from 'node:crypto';

export function stableJson(value) {
  if (Array.isArray(value)) return `[${value.map(stableJson).join(',')}]`;
  if (value && typeof value === 'object') return `{${Object.keys(value).sort().map(key => `${JSON.stringify(key)}:${stableJson(value[key])}`).join(',')}}`;
  return JSON.stringify(value);
}

export function releaseDigest(bundle) {
  return createHash('sha256').update(stableJson(bundle)).digest('hex');
}

export function signExperienceRelease(bundle, privateKey, keyId) {
  const payload = Buffer.from(stableJson(bundle));
  return {bundleSha256: releaseDigest(bundle), signingKeyId: keyId, signature: sign(null, payload, privateKey).toString('base64')};
}

export function verifyExperienceRelease(release, publicKeys) {
  if (!release?.bundle || release.status !== 'released' || typeof release.signature !== 'string') throw new Error('experience_release_invalid');
  if (releaseDigest(release.bundle) !== release.bundleSha256) throw new Error('experience_release_digest_mismatch');
  const publicKey = publicKeys?.[release.signingKeyId];
  if (!publicKey || !verify(null, Buffer.from(stableJson(release.bundle)), publicKey, Buffer.from(release.signature, 'base64'))) throw new Error('experience_release_signature_invalid');
  return structuredClone(release.bundle);
}
