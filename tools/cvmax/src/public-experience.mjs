const forbiddenKeys = new Set(['value', 'acceptedValue', 'before', 'beforeFields', 'parsedValues', 'filename', 'receiptText', 'facts', 'attachment']);

function assertPublicShape(value, path = 'bundle') {
  if (value == null || typeof value !== 'object') return;
  for (const [key, child] of Object.entries(value)) {
    if (forbiddenKeys.has(key)) throw new Error(`private_experience_key:${path}.${key}`);
    assertPublicShape(child, `${path}.${key}`);
  }
}

export function publicExperienceBundle(item) {
  if (!item?.family?.key || !item?.versions || !item?.strategies) throw new Error('experience_bundle_invalid');
  const bundle = structuredClone(item);
  for (const version of Object.values(bundle.versions)) delete version.successfulRuns;
  for (const strategy of Object.values(bundle.strategies)) {
    delete strategy.successfulRuns;
    delete strategy.lastFailure?.detail;
  }
  assertPublicShape(bundle);
  return bundle;
}
