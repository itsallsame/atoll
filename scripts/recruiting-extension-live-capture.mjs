import {spawn} from 'node:child_process';
import {readFile, mkdtemp, rm} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

const targetURL = process.argv[2] || 'https://job-boards.greenhouse.io/discord';
const chrome = process.env.RECRUITING_CHROME_BIN || 'google-chrome';
const profile = await mkdtemp(path.join(os.tmpdir(), 'atoll-recruiting-extension-live-'));
const child = spawn(chrome, ['--headless=new', '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage',
  `--user-data-dir=${profile}`, '--remote-debugging-port=0', targetURL], {stdio: ['ignore', 'ignore', 'pipe']});

let debuggerURL = '';
const started = new Promise((resolve, reject) => {
  let stderr = '';
  const timer = setTimeout(() => reject(new Error(`chrome_start_timeout: ${stderr.slice(-1000)}`)), 15000);
  child.stderr.setEncoding('utf8');
  child.stderr.on('data', chunk => {
    stderr += chunk;
    const match = stderr.match(/DevTools listening on (ws:\/\/[^\s]+)/);
    if (match && !debuggerURL) { debuggerURL = match[1]; clearTimeout(timer); resolve(); }
  });
  child.once('error', error => { clearTimeout(timer); reject(error); });
  child.once('exit', code => { if (!debuggerURL) { clearTimeout(timer); reject(new Error(`chrome_exited_${code}: ${stderr.slice(-1000)}`)); } });
});

class CDP {
  constructor(url) { this.socket = new WebSocket(url); this.next = 0; this.pending = new Map(); }
  async open() {
    await new Promise((resolve, reject) => {
      this.socket.addEventListener('open', resolve, {once: true});
      this.socket.addEventListener('error', () => reject(new Error('cdp_connect_failed')), {once: true});
    });
    this.socket.addEventListener('message', event => {
      const message = JSON.parse(event.data);
      const pending = this.pending.get(message.id);
      if (!pending) return;
      this.pending.delete(message.id);
      message.error ? pending.reject(new Error(message.error.message)) : pending.resolve(message.result);
    });
  }
  call(method, params = {}) {
    return new Promise((resolve, reject) => {
      const id = ++this.next;
      this.pending.set(id, {resolve, reject});
      this.socket.send(JSON.stringify({id, method, params}));
    });
  }
  close() { this.socket.close(); }
}

try {
  await started;
  const browser = new URL(debuggerURL);
  const listURL = `http://${browser.host}/json/list`;
  let target;
  for (let attempt = 0; attempt < 80 && !target; attempt++) {
    const targets = await fetch(listURL).then(response => response.json());
    target = targets.find(item => item.type === 'page' && item.url.startsWith('https://job-boards.greenhouse.io/discord'));
    if (!target) await new Promise(resolve => setTimeout(resolve, 100));
  }
  if (!target) throw new Error('real_job_page_target_not_found');
  const cdp = new CDP(target.webSocketDebuggerUrl);
  await cdp.open();
  try {
	await cdp.call('Page.enable');
	await cdp.call('Page.navigate', {url: targetURL});
    for (let attempt = 0; attempt < 200; attempt++) {
      const ready = await cdp.call('Runtime.evaluate', {expression: "document.readyState === 'complete' && document.querySelectorAll('.job-post').length >= 2", returnByValue: true});
      if (ready.result.value === true) break;
      if (attempt === 199) {
        const diagnostic = await cdp.call('Runtime.evaluate', {expression: "({url:location.href,title:document.title,ready:document.readyState,count:document.querySelectorAll('.job-post').length,text:document.body?.innerText?.slice(0,300)})", returnByValue: true});
        throw new Error(`real_job_cards_not_ready: ${JSON.stringify(diagnostic.result.value)}`);
      }
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    const logicSource = await readFile(new URL('../tools/recruiting-extension/extension/content-logic.js', import.meta.url), 'utf8');
    await cdp.call('Runtime.evaluate', {expression: logicSource});
    const expression = `(() => {
      const logic = globalThis.AtollRecruitingCaptureLogic;
      const card = document.querySelector('.job-post');
      const collection = logic.collectionSelectorFor(card, document);
      const link = card.querySelector('a[href]');
      const title = card.querySelector('p.body--medium');
      const draft = logic.buildDraft({version: logic.VERSION, captureId: 'live-discord-capture',
        sourceId: 'live-discord-source', recipeId: 'live-discord-listing', recipeVersion: 1,
        pageURL: location.href, userConfirmed: true, capturedAt: new Date().toISOString(),
        matchedCount: document.querySelectorAll(collection).length,
        sampleHTML: [...document.querySelectorAll(collection)].slice(0, 3).map(node => node.outerHTML).join('\\n'),
        marks: {collection: {selector: collection},
          job_key: {selector: logic.selectorFor(link, card, document), attribute: 'href'},
          title: {selector: logic.selectorFor(title, card, document)},
          detail_url: {selector: logic.selectorFor(link, card, document), attribute: 'href'}}});
      return {draft, collection, matchedCount: document.querySelectorAll(collection).length,
        firstHref: link.href, firstTitle: title.textContent.trim()};
    })()`;
    const evaluated = await cdp.call('Runtime.evaluate', {expression, returnByValue: true, awaitPromise: true});
    if (evaluated.exceptionDetails) throw new Error(evaluated.exceptionDetails.text || 'capture_evaluation_failed');
    const value = evaluated.result.value;
    if (!value || value.matchedCount < 2 || !value.firstHref.startsWith('https://job-boards.greenhouse.io/discord/jobs/') || !value.firstTitle) {
      throw new Error(`real_capture_quality_failed: ${JSON.stringify(value)}`);
    }
    await cdp.call('Page.navigate', {url: value.firstHref});
    for (let attempt = 0; attempt < 200; attempt++) {
      const ready = await cdp.call('Runtime.evaluate', {expression: "document.readyState === 'complete' && document.querySelector('h1') && document.querySelector('.job__description')", returnByValue: true});
      if (ready.result.value) break;
      if (attempt === 199) throw new Error('real_job_detail_not_ready');
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    await cdp.call('Runtime.evaluate', {expression: logicSource});
    const detailExpression = [
      "(() => {",
      "const logic = globalThis.AtollRecruitingCaptureLogic;",
      "const title = document.querySelector('h1');",
      "const description = document.querySelector('.job__description');",
      "const draft = logic.buildDraft({version: logic.VERSION, captureId: 'live-discord-detail-capture',",
      "sourceId: 'live-discord-source', recipeId: 'live-discord-detail', recipeVersion: 1,",
      "recipeKind: 'detail', sampleJobId: 'live-discord-job', pageURL: location.href,",
      "userConfirmed: true, capturedAt: new Date().toISOString(), matchedCount: 2,",
      "sampleHTML: title.outerHTML + '\\n' + description.outerHTML,",
      "marks: {title: {selector: logic.selectorFor(title, null, document)},",
      "description: {selector: logic.selectorFor(description, null, document)}}});",
      "return {draft, pageURL: location.href, title: title.textContent.trim(),",
      "descriptionLength: description.textContent.trim().length};",
      "})()",
    ].join('\n');
    const detailEvaluated = await cdp.call('Runtime.evaluate', {expression: detailExpression, returnByValue: true});
    if (detailEvaluated.exceptionDetails) throw new Error(detailEvaluated.exceptionDetails.text || 'detail_capture_evaluation_failed');
    const detail = detailEvaluated.result.value;
    if (!detail || !detail.title || detail.descriptionLength < 100 || detail.pageURL !== value.firstHref) {
      throw new Error('real_detail_capture_quality_failed: ' + JSON.stringify(detail));
    }
    process.stdout.write(JSON.stringify({...value, detail}));
  } finally {
    cdp.close();
  }
} finally {
  child.kill('SIGTERM');
  await new Promise(resolve => { child.once('exit', resolve); setTimeout(resolve, 3000); });
  if (child.exitCode == null) child.kill('SIGKILL');
  await rm(profile, {recursive: true, force: true});
}
