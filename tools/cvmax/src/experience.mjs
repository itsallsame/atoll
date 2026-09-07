import {createHash} from 'node:crypto';

const hash = value => createHash('sha256').update(JSON.stringify(value)).digest('hex');
const now = () => new Date().toISOString();
const reusableKinds = new Set(['fill', 'select', 'choose', 'check', 'upload']);

function clean(value) {
  return String(value ?? '').replace(/\s+/g, ' ').trim().toLowerCase();
}

function normalizeIdentity(value) {
  return clean(value)
    .replace(/\[(?:\d+|["'][^"']+["'])\]/g, '[]')
    .replace(/(^|[-_:])\d{2,}(?=$|[-_:])/g, '$1:n');
}

function normalizeLabel(value) {
  return clean(value)
    .replace(/[（(]?\s*(必填|required|选填|optional)\s*[）)]?/gi, '')
    .replace(/[：:*]+$/g, '')
    .trim();
}

function dynamicSegment(segment) {
  let decoded = segment;
  try { decoded = decodeURIComponent(segment); } catch {}
  if (/^\d{5,}$/.test(decoded)) return ':id';
  if (/^[0-9a-f]{8}-[0-9a-f-]{27,}$/i.test(decoded)) return ':id';
  if (/^(?=.*\d)[a-z0-9_-]{16,}$/i.test(decoded)) return ':id';
  return decoded;
}

export function pageFamilyFor(snapshot) {
  const url = new URL(snapshot.url);
  const routeTemplate = '/' + url.pathname.split('/').filter(Boolean).map(dynamicSegment).join('/');
  const segments = routeTemplate.split('/').filter(Boolean);
  const stage = clean(snapshot.formStage || [...segments].reverse().find(item => /^(apply|application|resume|profile|form|edit|step\d*)$/i.test(item)) || 'form');
  const family = {origin: url.origin, host: url.hostname.toLowerCase(), routeTemplate: routeTemplate || '/', stage};
  return {...family, key: hash([family.origin, family.routeTemplate, family.stage])};
}

export function resolveExperiencedTarget(experiences, requestedURL) {
  const requested = new URL(requestedURL);
  const sourceSegments = requested.pathname.split('/').filter(Boolean);
  const dynamicValues = sourceSegments.filter(segment => dynamicSegment(segment) === ':id');
  if (dynamicValues.length !== 1) return null;
  const candidates = Object.values(experiences ?? {}).filter(item =>
    item?.status === 'reusable' && item.family?.origin === requested.origin && item.family?.stage === 'apply' &&
    item.family.routeTemplate?.split('/').filter(Boolean).filter(segment => segment === ':id').length === 1 &&
    Object.values(item.strategies ?? {}).some(strategy => strategy.status === 'reusable')
  ).map(item => {
    const path = item.family.routeTemplate.split('/').map(segment => segment === ':id' ? encodeURIComponent(dynamicValues[0]) : segment).join('/');
    return new URL(path, requested.origin).href;
  }).filter(url => new URL(url).pathname !== requested.pathname);
  const unique = [...new Set(candidates)];
  return unique.length === 1 ? {url: unique[0], routeTemplate: pageFamilyFor({url: unique[0]}).routeTemplate} : null;
}

function baseDescriptor(field) {
  return {
    semanticId: normalizeIdentity(field.semanticId),
    group: normalizeIdentity(field.group),
    label: normalizeLabel(field.label),
    type: clean(field.type),
    optionLabel: ['radio', 'checkbox'].includes(field.type) ? normalizeLabel(field.optionLabel) : ''
  };
}

function descriptorKey(descriptor) {
  // Labels and control types form the cross-render schema. Framework-generated ids
  // and container ids remain secondary anchors because they frequently change on reload.
  return JSON.stringify([descriptor.label, descriptor.type, descriptor.optionLabel]);
}

export function describeFields(snapshot) {
  const counts = new Map();
  return (snapshot.fields ?? []).map(field => {
    const descriptor = baseDescriptor(field);
    const baseKey = descriptorKey(descriptor);
    const ordinal = counts.get(baseKey) ?? 0;
    counts.set(baseKey, ordinal + 1);
    return {field, descriptor, baseKey, ordinal, anchorKey: JSON.stringify([baseKey, ordinal])};
  });
}

export function schemaFor(snapshot) {
  const described = describeFields(snapshot);
  const counts = new Map();
  for (const item of described) counts.set(item.baseKey, (counts.get(item.baseKey) ?? 0) + 1);
  const shape = [...counts].sort(([a], [b]) => a.localeCompare(b));
  return {fingerprint: hash(shape), shape, described};
}

function schemaCompatibility(left, right) {
  const a = new Set(left), b = new Set(right);
  if (!a.size && !b.size) return 1;
  if (!a.size || !b.size) return 0;
  let intersection = 0;
  for (const item of a) if (b.has(item)) intersection++;
  // A page before native parsing or before expanding repeat sections is often a
  // strict subset of its later form. Containment recognizes that as the same
  // version family while field binding still gates every individual strategy.
  return intersection / Math.min(a.size, b.size);
}

function strategyForEvidence(snapshot, entry) {
  const action = entry?.action;
  if (!action || !reusableKinds.has(action.kind)) return null;
  const described = describeFields(snapshot);
  let item = null;
  if (action.semanticId) {
    const candidates = described.filter(candidate => candidate.field.semanticId === action.semanticId && (action.group == null || candidate.field.group === action.group));
    item = Number.isInteger(action.semanticOrdinal) ? candidates[action.semanticOrdinal] : candidates.length === 1 ? candidates[0] : null;
  }
  if (!item) item = described.find(candidate => candidate.field.ref === action.fieldRef && (!action.semanticId || candidate.field.semanticId === action.semanticId));
  if (!item || !action.factId && action.kind !== 'upload') return null;
  const strategy = {
    kind: action.kind,
    factId: action.factId ?? null,
    anchor: {...item.descriptor, ordinal: item.ordinal},
    anchorKey: item.anchorKey
  };
  return {...strategy, id: hash([strategy.kind, strategy.factId, strategy.anchor])};
}

function distinctPush(values, value, maximum = 12) {
  const next = [...new Set([...(values ?? []), value])];
  return next.slice(-maximum);
}

export function recordVerifiedExperience(experiences, application, actionVerified, privateValues = []) {
  const snapshot = application.snapshot;
  if (!snapshot?.url) return null;
  const family = pageFamilyFor(snapshot), schema = schemaFor(snapshot), timestamp = now();
  const item = experiences[family.key] ?? {
    formatVersion: 2,
    family,
    status: 'candidate',
    versions: {},
    strategies: {},
    createdAt: timestamp,
    updatedAt: timestamp
  };
  item.formatVersion = 2;
  item.family = family;
  item.versions ??= {};
  item.strategies ??= {};
  const verified = (application.evidence ?? []).filter(entry => actionVerified(snapshot, entry.action));
  const strategyIds = [];
  for (const entry of verified) {
    const candidate = strategyForEvidence(snapshot, entry);
    if (!candidate) continue;
    const anchorText = JSON.stringify(candidate.anchor).toLowerCase();
    if (privateValues.some(value => String(value ?? '').trim().length >= 2 && anchorText.includes(String(value).trim().toLowerCase()))) continue;
    strategyIds.push(candidate.id);
    const previous = item.strategies[candidate.id];
    const successfulRuns = distinctPush(previous?.successfulRuns, application.runId);
    item.strategies[candidate.id] = {
      ...candidate,
      successfulRuns,
      successCount: successfulRuns.length,
      failureCount: previous?.failureCount ?? 0,
      status: successfulRuns.length >= 2 ? 'reusable' : 'candidate',
      firstVerifiedAt: previous?.firstVerifiedAt ?? timestamp,
      lastVerifiedAt: timestamp,
      schemaFingerprints: distinctPush(previous?.schemaFingerprints, schema.fingerprint)
    };
  }
  if (!strategyIds.length) return null;
  const version = item.versions[schema.fingerprint] ?? {successfulRuns: [], strategyIds: [], firstSeenAt: timestamp};
  version.successfulRuns = distinctPush(version.successfulRuns, application.runId);
  version.strategyIds = [...new Set([...(item.versions[schema.fingerprint]?.strategyIds ?? []), ...strategyIds])];
  version.fieldShape = schema.shape;
  version.lastSeenAt = timestamp;
  item.versions[schema.fingerprint] = version;
  item.status = Object.values(item.strategies).some(strategy => strategy.status === 'reusable') ? 'reusable' : 'candidate';
  item.updatedAt = timestamp;
  experiences[family.key] = item;
  return structuredClone(item);
}

function fieldForStrategy(described, strategy) {
  const matches = described.filter(item => item.baseKey === descriptorKey(strategy.anchor));
  const semanticMatches = described.filter(item => strategy.anchor.semanticId && item.descriptor.semanticId === strategy.anchor.semanticId && item.descriptor.type === strategy.anchor.type && item.descriptor.optionLabel === strategy.anchor.optionLabel && (!strategy.anchor.group || item.descriptor.group === strategy.anchor.group));
  if (semanticMatches.length === 1) return semanticMatches[0];
  const grouped = matches.filter(item => strategy.anchor.group && item.descriptor.group === strategy.anchor.group);
  if (grouped.length === 1) return grouped[0];
  return matches[strategy.anchor.ordinal] ?? null;
}

function actionCompatible(strategy, field) {
  if (strategy.kind === 'choose') return field.type === 'radio';
  if (strategy.kind === 'check') return field.type === 'checkbox';
  if (strategy.kind === 'upload') return field.type === 'file';
  if (strategy.kind === 'select') return ['select-one', 'select', 'search'].includes(field.type);
  return strategy.kind === 'fill' && ['text', 'email', 'tel', 'url', 'number', 'date', 'month', 'textarea'].includes(field.type);
}

export function matchExperience(experiences, snapshot, facts = [], attachment = null) {
  if (!snapshot?.url) return null;
  const family = pageFamilyFor(snapshot), item = experiences[family.key];
  if (!item || item.status === 'suspended') return null;
  const schema = schemaFor(snapshot);
  const versions = Object.entries(item.versions ?? {});
  const rank = ([key, version, similarity]) => [key === schema.fingerprint ? 1 : 0, similarity, (version.strategyIds ?? []).filter(id => item.strategies?.[id]?.status === 'reusable').length, version.lastSeenAt ?? ''];
  const compare = (left, right) => { const a = rank(left), b = rank(right); return b[0] - a[0] || b[1] - a[1] || b[2] - a[2] || String(b[3]).localeCompare(String(a[3])); };
  const candidates = versions.map(([key, version]) => [key, version, key === schema.fingerprint ? 1 : schemaCompatibility(schema.shape.map(([name]) => name), (version.fieldShape ?? []).map(([name]) => name))]).filter(([, , similarity]) => similarity >= 0.6).sort(compare);
  let best = null, reusable = [], mapped = [];
  for (const candidate of candidates) {
    const strategies = Object.values(item.strategies ?? {}).filter(strategy => strategy.status === 'reusable' && strategy.schemaFingerprints?.includes(candidate[0]));
    const bindings = strategies.map(strategy => ({strategy, match: fieldForStrategy(schema.described, strategy)})).filter(({strategy, match}) => match && actionCompatible(strategy, match.field));
    if (bindings.length) { best = candidate; reusable = strategies; mapped = bindings; break; }
  }
  if (!best) return null;
  const factIds = new Set(facts.map(fact => fact.id)), currentKeys = new Set(schema.shape.map(([key]) => key)), knownKeys = new Set((best[1].fieldShape ?? []).map(([key]) => key));
  const suggestedActions = [], preserved = [], blocked = [], matchedStrategyIds = [];
  const byField = Map.groupBy(mapped, ({match}) => match.field.ref);
  for (const bindings of byField.values()) {
    const meanings = new Set(bindings.map(({strategy}) => JSON.stringify([strategy.kind, strategy.factId])));
    if (meanings.size > 1) {
      for (const {strategy, match} of bindings) {
        matchedStrategyIds.push(strategy.id);
        blocked.push({strategyId: strategy.id, fieldRef: match.field.ref, factId: strategy.factId, reason: 'experience_mapping_ambiguous'});
      }
      continue;
    }
    const {strategy, match} = bindings[0];
    matchedStrategyIds.push(strategy.id);
    const field = match.field;
    if (field.sensitive || field.manual) { blocked.push({strategyId: strategy.id, fieldRef: field.ref, reason: 'manual_or_sensitive'}); continue; }
    if (strategy.kind === 'upload') {
      if (attachment && !field.value) suggestedActions.push({kind: 'upload', fieldRef: field.ref, experienceStrategyId: strategy.id});
      else blocked.push({strategyId: strategy.id, fieldRef: field.ref, reason: attachment ? 'existing_value' : 'attachment_missing'});
      continue;
    }
    const fact = facts.find(value => value.id === strategy.factId);
    if (!factIds.has(strategy.factId) || !fact) { blocked.push({strategyId: strategy.id, fieldRef: field.ref, factId: strategy.factId, reason: 'fact_missing'}); continue; }
    const current = String(field.value ?? '');
    const already = strategy.kind === 'choose' || strategy.kind === 'check' ? field.checked === /^(true|yes|1|是)$/i.test(fact.value) : current === fact.value;
    if (already) { preserved.push({strategyId: strategy.id, fieldRef: field.ref, factId: strategy.factId}); continue; }
    if (current || ['choose', 'check'].includes(strategy.kind) && field.checked) { blocked.push({strategyId: strategy.id, fieldRef: field.ref, factId: strategy.factId, reason: 'existing_value'}); continue; }
    suggestedActions.push({kind: strategy.kind, fieldRef: field.ref, factId: strategy.factId, experienceStrategyId: strategy.id});
  }
  if (!matchedStrategyIds.length) return null;
  return {
    status: 'reusable',
    successCount: Math.min(...reusable.filter(strategy => matchedStrategyIds.includes(strategy.id)).map(strategy => strategy.successCount)),
    match: best[0] === schema.fingerprint ? 'exact' : 'compatible',
    family,
    schemaFingerprint: schema.fingerprint,
    matchedVersion: best[0],
    similarity: Number(best[2].toFixed(3)),
    coverage: Number((matchedStrategyIds.length / Math.max(1, reusable.length)).toFixed(3)),
    suggestedActions,
    preserved,
    blocked,
    drift: {
      addedFieldKinds: [...currentKeys].filter(key => !knownKeys.has(key)),
      missingFieldKinds: [...knownKeys].filter(key => !currentKeys.has(key)),
      unmappedStrategyIds: reusable.map(strategy => strategy.id).filter(id => !matchedStrategyIds.includes(id))
    },
    finalSubmitAllowed: false
  };
}

export function suspendExperience(experiences, snapshot, {reason, strategyId = null, fieldRef = null} = {}) {
  const family = pageFamilyFor(snapshot), item = experiences[family.key];
  if (!item) return {accepted: false, reason: 'experience_not_found'};
  let targets = [];
  if (strategyId && item.strategies?.[strategyId]) targets = [item.strategies[strategyId]];
  else if (fieldRef) {
    const descriptor = describeFields(snapshot).find(value => value.field.ref === fieldRef);
    if (descriptor) targets = Object.values(item.strategies ?? {}).filter(strategy => strategy.anchorKey === descriptor.anchorKey);
  }
  if (targets.length) {
    for (const strategy of targets) {
      strategy.status = 'suspended';
      strategy.failureCount = (strategy.failureCount ?? 0) + 1;
      strategy.lastFailure = {code: failureCode(reason), at: now()};
    }
    item.status = Object.values(item.strategies).some(strategy => strategy.status === 'reusable') ? 'reusable' : 'suspended';
  } else {
    item.status = 'suspended';
    item.lastFailure = {code: failureCode(reason), at: now()};
  }
  item.updatedAt = now();
  return {accepted: true, scope: targets.length ? 'field_strategy' : 'page_family', experience: structuredClone(item)};
}

function failureCode(reason) {
  const value = String(reason ?? '');
  if (/fresh_verification_failed/.test(value)) return 'verification_failed';
  if (/field_detached|capability_changed/.test(value)) return 'structure_changed';
  if (/option|selection/.test(value)) return 'option_strategy_failed';
  if (/site_rejected_value/.test(value)) return 'site_rejected_value';
  return 'user_correction';
}

export function pruneExperiences(experiences, maximum = 200) {
  const entries = Object.entries(experiences);
  for (const [key] of entries.sort((a, b) => String(a[1].updatedAt).localeCompare(String(b[1].updatedAt))).slice(0, Math.max(0, entries.length - maximum))) delete experiences[key];
}
