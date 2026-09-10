(() => {
  if (globalThis.__atollRecruitingCaptureInstalled) return;
  globalThis.__atollRecruitingCaptureInstalled = true;
  const logic = globalThis.AtollRecruitingCaptureLogic;
  const state = {active: false, recipeKind: 'listing', armed: '', marks: {}, targets: {}, highlighted: []};

  function fieldAttribute(role, element) {
    if (role === 'detail_url') return element.closest('a[href]') ? 'href' : '';
    if (role !== 'job_key') return '';
    for (const attribute of ['data-job-id', 'data-id', 'data-key', 'href']) {
      if (element.hasAttribute(attribute)) return attribute;
      const parent = element.closest(`[${attribute}]`);
      if (parent) return attribute;
    }
    return '';
  }

  function targetForRole(role, clicked) {
    if (role === 'detail_url') return clicked.closest('a[href]') || clicked;
    if (role === 'job_key') {
      return clicked.closest('[data-job-id],[data-id],[data-key],a[href]') || clicked;
    }
    return clicked;
  }

  function mark(role, clicked) {
    const target = targetForRole(role, clicked);
    if (role === 'collection') {
      const selector = logic.collectionSelectorFor(target, document);
      state.marks.collection = {selector};
    } else {
      const card = state.recipeKind === 'listing' ? target.closest(state.marks.collection?.selector || ':not(*)') : null;
      if (state.recipeKind === 'listing' && !state.marks.collection) throw new Error('mark_collection_first');
      if (state.recipeKind === 'listing' && !card) throw new Error('field_outside_collection');
      const selector = logic.selectorFor(target, card, document);
      if (!selector) throw new Error('field_selector_failed');
      const attribute = fieldAttribute(role, target);
      if ((role === 'job_key' || role === 'detail_url') && !attribute) throw new Error(`${role}_requires_stable_attribute`);
      state.marks[role] = {selector, ...(attribute ? {attribute} : {})};
    }
    state.targets[role] = target;
    target.dataset.atollRecruitingCaptured = role;
    target.style.outline = '3px solid #0d9488';
    state.highlighted.push(target);
    chrome.runtime.sendMessage({type: 'capture.marked', role, mark: state.marks[role]}).catch(() => {});
  }

  function sanitizeURL(value) {
    try {
      const parsed = new URL(value, location.href);
      parsed.username = '';
      parsed.password = '';
      parsed.search = '';
      parsed.hash = '';
      return parsed.href;
    } catch {
      return '';
    }
  }

  function sanitizedSamples() {
    const selector = state.marks.collection?.selector;
    const samples = selector ? [...document.querySelectorAll(selector)].slice(0, 3) :
      [...new Set(Object.values(state.targets))].slice(0, 8);
    const count = selector ? document.querySelectorAll(selector).length : samples.length;
    const html = samples.map(card => {
      const clone = card.cloneNode(true);
      clone.querySelectorAll('script,style,form,input,textarea,select,button,iframe,object,embed').forEach(node => node.remove());
      for (const element of [clone, ...clone.querySelectorAll('*')]) {
        for (const attribute of [...element.attributes]) {
          const name = attribute.name.toLowerCase();
          if (name === 'style' || name === 'value' || name === 'checked' || name === 'selected' || name.startsWith('on') ||
              /(token|secret|password|session|authorization|signature|api.?key)/i.test(name)) element.removeAttribute(attribute.name);
          else if (name === 'href' || name === 'src') element.setAttribute(attribute.name, sanitizeURL(attribute.value));
        }
      }
      return clone.outerHTML;
    }).join('\n');
    return {count, html: html.slice(0, 200000)};
  }

  document.addEventListener('click', event => {
    if (!state.active || !state.armed) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    const role = state.armed;
    state.armed = '';
    try { mark(role, event.target); }
    catch (error) { chrome.runtime.sendMessage({type: 'capture.error', error: error.message}).catch(() => {}); }
  }, true);

  chrome.runtime.onMessage.addListener((message, _sender, reply) => {
    if (message?.type === 'capture.begin') {
      state.active = true;
      state.recipeKind = message.recipeKind === 'detail' ? 'detail' : 'listing';
      state.armed = '';
      state.marks = {};
      state.targets = {};
      for (const element of state.highlighted) {
        delete element.dataset.atollRecruitingCaptured;
        element.style.outline = '';
      }
      state.highlighted = [];
      reply({ok: true, pageURL: location.href});
      return;
    }
    if (message?.type === 'profile.recipe_probe') {
      try {
        reply({ok: true, ...logic.profileRecipeProbe({securityDomain: message.securityDomain,
          canaryURL: message.canaryURL, recipe: message.recipe, minimumRecords: message.minimumRecords})});
      } catch (error) {
        reply({ok: false, error: error.message});
      }
      return;
    }
    if (message?.type === 'capture.arm') {
      const roles = state.recipeKind === 'detail'
        ? ['title', 'description', 'location', 'employment_type', 'posted_at']
        : ['collection', 'job_key', 'title', 'detail_url', 'activity_at'];
      if (!state.active || !roles.includes(message.role)) {
        reply({ok: false, error: 'capture_not_active'});
      } else {
        state.armed = message.role;
        reply({ok: true, role: state.armed});
      }
      return;
    }
    if (message?.type === 'capture.snapshot') {
      try {
        const sample = sanitizedSamples();
        const draft = logic.buildDraft({version: logic.VERSION, ...message.config, pageURL: location.href,
          userConfirmed: message.config.userConfirmed, marks: state.marks, matchedCount: sample.count,
          sampleHTML: sample.html, capturedAt: new Date().toISOString()});
        reply({ok: true, draft});
      } catch (error) {
        reply({ok: false, error: error.message});
      }
      return true;
    }
  });
})();
