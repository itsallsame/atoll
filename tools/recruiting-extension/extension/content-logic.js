((root) => {
  const VERSION = 'recruiting.extension-draft.v1';
  const RECIPE_VERSION = 'recruiting.recipe.v1';
  const TRACE_KINDS = new Set(['navigate', 'mark_collection', 'mark_field', 'mark_pagination', 'capture_artifact']);

  function clean(value, max = 500) {
    return String(value ?? '').replace(/\s+/g, ' ').trim().slice(0, max);
  }

  function safeSegment(value) {
    return /^[A-Za-z0-9._-]{1,160}$/.test(clean(value, 161));
  }

  function cssEscape(value) {
    if (root.CSS?.escape) return root.CSS.escape(value);
    return String(value).replace(/[^A-Za-z0-9_-]/g, character => `\\${character.codePointAt(0).toString(16)} `);
  }

  function usefulClasses(element) {
    return [...element.classList].filter(name => name.length <= 64 && !/[0-9a-f]{8,}/i.test(name) && !/^css-/i.test(name)).slice(0, 2);
  }

  function stablePart(element) {
    for (const attribute of ['data-job-id', 'data-id', 'data-automation-id', 'data-testid']) {
      const value = element.getAttribute(attribute);
      if (value && value.length <= 128) return `${element.localName}[${attribute}="${cssEscape(value)}"]`;
    }
    if (element.id && element.id.length <= 80 && !/[0-9a-f]{12,}/i.test(element.id)) return `#${cssEscape(element.id)}`;
    const classes = usefulClasses(element);
    if (classes.length) return `${element.localName}.${classes.map(cssEscape).join('.')}`;
    const siblings = [...(element.parentElement?.children || [])].filter(item => item.localName === element.localName);
    return siblings.length > 1 ? `${element.localName}:nth-of-type(${siblings.indexOf(element) + 1})` : element.localName;
  }

  function selectorFor(element, scopeRoot, documentRoot = root.document) {
    const parts = [];
    let current = element;
    while (current && current !== scopeRoot && current !== documentRoot.documentElement) {
      parts.unshift(stablePart(current));
      const candidate = parts.join(' > ');
      try {
        const scope = scopeRoot || documentRoot;
        const matches = scope.querySelectorAll(candidate);
        if (matches.length === 1 && matches[0] === element) return candidate;
      } catch {}
      current = current.parentElement;
    }
    return parts.join(' > ');
  }

  function collectionSelectorFor(element, documentRoot = root.document) {
    const tag = element.localName;
    const classes = usefulClasses(element);
    const candidates = [];
    if (classes.length > 1) candidates.push(`${tag}.${classes.map(cssEscape).join('.')}`);
    for (const name of classes) candidates.push(`${tag}.${cssEscape(name)}`);
    const role = element.getAttribute('role');
    if (role && role.length <= 40) candidates.push(`${tag}[role="${cssEscape(role)}"]`);
    candidates.push(tag);
    let structuralSingle = '';
    for (const selector of candidates) {
      try {
        const matches = [...documentRoot.querySelectorAll(selector)];
        if (matches.length >= 2 && matches.length <= 500 && matches.includes(element)) return selector;
        if (!structuralSingle && selector !== tag && matches.length === 1 && matches[0] === element) structuralSingle = selector;
      } catch {}
    }
    if (structuralSingle) return structuralSingle;
    throw new Error('collection_selector_not_repeated');
  }

  function buildDraft(input) {
    const recipeKind = input?.recipeKind === 'detail' ? 'detail' : 'listing';
    if (!input || input.version !== VERSION || !safeSegment(input.captureId) || !clean(input.sourceId) ||
        !clean(input.recipeId) || !Number.isSafeInteger(input.recipeVersion) || input.recipeVersion < 1 ||
        !input.userConfirmed || !/^https:\/\//.test(input.pageURL)) throw new Error('capture_identity_invalid');
    const marks = input.marks || {};
    if (recipeKind === 'listing' && (!clean(marks.collection?.selector) || !clean(marks.job_key?.selector) ||
        !clean(marks.title?.selector) || !clean(marks.detail_url?.selector))) throw new Error('required_marks_missing');
    if (recipeKind === 'detail' && (!clean(input.sampleJobId) || !clean(marks.title?.selector) ||
        !clean(marks.description?.selector))) throw new Error('required_detail_marks_missing');
    const fields = {}, attributes = {};
    const fieldNames = recipeKind === 'listing'
      ? ['job_key', 'title', 'detail_url', 'activity_at']
      : ['title', 'description', 'location', 'employment_type', 'posted_at'];
    for (const name of fieldNames) {
      const mark = marks[name];
      if (!mark) continue;
      fields[name] = clean(mark.selector);
      if (clean(mark.attribute)) attributes[name] = clean(mark.attribute);
    }
    const trace = [{kind: 'navigate'}];
    if (recipeKind === 'listing') trace.push({kind: 'mark_collection', selector: clean(marks.collection.selector)});
    for (const name of fieldNames) {
      if (!marks[name]) continue;
      trace.push({kind: 'mark_field', selector: fields[name], field: name, ...(attributes[name] ? {attribute: attributes[name]} : {})});
    }
    trace.push({kind: 'capture_artifact'});
    if (!trace.every(step => TRACE_KINDS.has(step.kind))) throw new Error('trace_invalid');
    const evidence = {
      version: 'recruiting.extension-evidence.v1',
      page_url: input.pageURL,
      captured_at: input.capturedAt,
      ...(recipeKind === 'listing' ? {collection_selector: clean(marks.collection.selector)} : {}),
      matched_count: input.matchedCount,
      sample_html: clean(input.sampleHTML, 200000),
    };
    return {
      version: VERSION,
      capture_id: input.captureId,
      source_id: clean(input.sourceId),
      recipe_id: clean(input.recipeId),
      recipe_version: input.recipeVersion,
      page_url: input.pageURL,
      ...(recipeKind === 'detail' ? {sample_job_id: clean(input.sampleJobId)} : {}),
      captured_at: input.capturedAt,
      user_confirmed: true,
      candidate: {
        abi_version: RECIPE_VERSION,
        kind: recipeKind,
        required_capability: 'http.fetch',
        transport: 'http_html',
        request: {method: 'GET', headers: {Accept: 'text/html'}, timeout_ms: 20000,
          max_response_bytes: 2097152, max_redirects: 2, user_agent: 'Atoll-Recruiting-Extension/1'},
        extraction: {...(recipeKind === 'listing' ? {collection: clean(marks.collection.selector)} : {}), fields,
          ...(Object.keys(attributes).length ? {attributes} : {})},
        ...(recipeKind === 'listing' ? {listing: {identity_field: 'job_key', detail_url_field: 'detail_url',
          ...(fields.activity_at ? {activity_field: 'activity_at'} : {}), boundary_mode: fields.activity_at ? 'activity_time' : 'frontier_keys',
          ordering: 'newest_activity_desc', update_retop: true, overlap_pages: 1, max_pages: 10,
          max_items_per_page: 500, max_total_bytes: 10485760, frontier_width: 20}} : {}),
      },
      trace,
      evidence,
    };
  }

  function bridgeEndpoint(value) {
    const parsed = new URL(clean(value));
    if (parsed.protocol !== 'ws:' || (parsed.hostname !== '127.0.0.1' && parsed.hostname !== '[::1]' && parsed.hostname !== '::1') ||
        parsed.pathname !== '/capture' || parsed.username || parsed.password || parsed.search || parsed.hash) throw new Error('bridge_endpoint_invalid');
    return parsed.toString().replace(/\/$/, '');
  }

  // Run the frozen, site-specific Browser Recipe against the exact canary
  // page. Only a count leaves this boundary; extracted DOM values remain in
  // the page and never enter extension storage, messages, or artifacts.
  function profileRecipeProbe(input, environment = {}) {
    const documentRoot = environment.documentRoot || root.document;
    const pageLocation = environment.location || root.location;
    const securityDomain = clean(input?.securityDomain, 253);
    let canary;
    try { canary = new URL(String(input?.canaryURL || '')); }
    catch { throw new Error('profile_canary_location_mismatch'); }
    if (!documentRoot || !pageLocation || canary.protocol !== 'https:' || canary.username || canary.password ||
        canary.hostname !== securityDomain || pageLocation.protocol !== 'https:' ||
        pageLocation.hostname !== securityDomain || pageLocation.href !== canary.href) {
      throw new Error('profile_canary_location_mismatch');
    }
    const recipe = input?.recipe;
    const minimumRecords = Number(input?.minimumRecords);
    if (recipe?.abi_version !== RECIPE_VERSION || recipe.transport !== 'browser' ||
        recipe.required_capability !== 'browser.profile.repair' || !['listing', 'detail'].includes(recipe.kind) ||
        String(recipe.request?.method || '').trim().toUpperCase() !== 'GET' ||
        !recipe.extraction?.fields || Array.isArray(recipe.extraction.fields) ||
        Object.keys(recipe.extraction.fields).length < 1 || !Number.isInteger(minimumRecords) ||
        minimumRecords < 1 || minimumRecords > 100) {
      throw new Error('profile_canary_recipe_invalid');
    }
    for (const [field, selector] of Object.entries(recipe.extraction.fields)) {
      if (!clean(field, 161) || !clean(selector, 4097)) throw new Error('profile_canary_recipe_invalid');
    }
    let roots;
    try {
      if (recipe.kind === 'listing') {
        const collection = clean(recipe.extraction.collection, 4097);
        if (!collection) throw new Error('profile_canary_collection_required');
        roots = [...documentRoot.querySelectorAll(collection)].slice(0, 101);
      } else {
        roots = [documentRoot];
      }
    } catch (error) {
      if (error?.message === 'profile_canary_collection_required') throw error;
      throw new Error('profile_canary_selector_invalid');
    }
    let recordCount = 0;
    for (const recordRoot of roots) {
      let complete = true;
      for (const [field, selector] of Object.entries(recipe.extraction.fields)) {
        let element;
        try { element = recordRoot.querySelector(String(selector)); }
        catch { throw new Error('profile_canary_selector_invalid'); }
        const attribute = recipe.extraction.attributes?.[field];
        const value = attribute ? element?.getAttribute(String(attribute)) : element?.textContent;
        if (!String(value || '').trim()) { complete = false; break; }
      }
      if (complete) recordCount++;
      if (recordCount >= 100) break;
    }
    return {authenticated: recordCount >= minimumRecords, recordCount};
  }

  const api = Object.freeze({VERSION, RECIPE_VERSION, clean, safeSegment, selectorFor, collectionSelectorFor,
    buildDraft, bridgeEndpoint, profileRecipeProbe});
  root.AtollRecruitingCaptureLogic = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof globalThis !== 'undefined' ? globalThis : this);
