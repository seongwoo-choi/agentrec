import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { JSDOM } from 'jsdom';

const here = new URL('.', import.meta.url);
const html = await readFile(new URL('index.html', here), 'utf8');
const app = await readFile(new URL('app.js', here), 'utf8');
const css = await readFile(new URL('app.css', here), 'utf8');

const response = (body) => Promise.resolve({
  ok: true,
  status: 200,
  json: async () => body,
});

async function renderFixture({ list, details, actions = [], changes = [], events = [], live = null, search = { hits: [], truncated: false }, configure = () => {} }) {
  const dom = new JSDOM(html, {
    runScripts: 'outside-only',
    url: 'http://localhost:42817/',
    pretendToBeVisual: true,
  });
  const { window } = dom;
  window.IntersectionObserver = class {
    observe() {}
    disconnect() {}
  };
  window.HTMLElement.prototype.scrollIntoView = () => {};
  configure(window);
  window.__fetchPaths = [];
  window.fetch = (input, init = {}) => {
    const url = new URL(String(input), window.location.href);
    window.__fetchPaths.push(`${url.pathname}${url.search}`);
    if (url.pathname === '/api/shadow') return response({ allowRun: false });
    if (url.pathname === '/api/token') return response({ token: 'test-token' });
    if (init.method === 'DELETE' && /^\/api\/runs\/[^/]+$/.test(url.pathname)) {
      return Promise.resolve({ ok: true, status: 204, json: async () => ({}) });
    }
    if (url.pathname === '/api/runs') return response(list);
    if (url.pathname === '/api/search') return response(search);
    const detailMatch = /^\/api\/runs\/([^/]+)$/.exec(url.pathname);
    if (detailMatch) {
      const currentDetails = typeof details === 'function' ? details(decodeURIComponent(detailMatch[1])) : details;
      if (currentDetails && typeof currentDetails.then === 'function') {
        return currentDetails.then((resolved) => {
          if (resolved && url.pathname === `/api/runs/${resolved.run.id}`) return response(resolved);
          throw new Error(`unexpected fetch ${url}`);
        });
      }
      if (currentDetails && url.pathname === `/api/runs/${currentDetails.run.id}`) return response(currentDetails);
    }
    if (url.pathname.includes('/actions')) return response(typeof actions === 'function' ? actions(Number(url.searchParams.get('cursor') || 0)) : { items: actions, nextCursor: null });
    if (url.pathname.endsWith('/live') && live) return response(typeof live === 'function' ? live() : live);
    if (url.pathname.includes('/events')) {
      try {
        return response(typeof events === 'function' ? events(Number(url.searchParams.get('cursor') || 0)) : { items: events, nextCursor: null });
      } catch (error) {
        return Promise.reject(error);
      }
    }
    if (url.pathname.includes('/changes')) return response(typeof changes === 'function' ? changes(Number(url.searchParams.get('cursor') || 0)) : { items: changes, nextCursor: null, total: changes.length, status: 'available' });
    throw new Error(`unexpected fetch ${url}`);
  };
  window.eval(app);
  for (let i = 0; i < 10; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  return dom;
}

function paginatedChanges(targetPath) {
  const items = Array.from({ length: 251 }, (_, index) => ({ path: index === 250 ? targetPath : `prefix/file-${index}.go`, kind: 'added', tracked: false }));
  return (cursor) => ({ items: items.slice(cursor, cursor + 250), nextCursor: cursor + 250 < items.length ? cursor + 250 : null, total: items.length, status: 'available' });
}

// Explicit byte boundaries: no action index is a usable cursor.
function actionLinkFixture() {
  const data = fixture('completed', 'pass', 'PASS');
  const items = Array.from({ length: 253 }, (_, i) => ({ id: `action-${i}`, type: 'tool.call', status: 'success', input: { command: i === 250 ? 'go test' : i === 251 ? 'git status' : `command-${i}` } }));
  const boundaries = new Map([[0, 0], [98765, 250], [99234, 251]]);
  data.details.actionCount = items.length;
  data.actions = (cursor) => {
    assert.ok(boundaries.has(cursor), `invalid byte cursor ${cursor}`);
    const start = boundaries.get(cursor);
    return { items: items.slice(start, start + 250), nextCursor: start === 0 ? 98765 : null };
  };
  data.search = { hits: [250, 251].map((i) => ({ runId: data.details.run.id, kind: 'action', actionId: items[i].id, offset: i === 250 ? 98765 : 99234, snippet: items[i].input.command })), truncated: false };
  return data;
}

const settle = async () => { for (let i = 0; i < 10; i += 1) await new Promise((resolve) => setTimeout(resolve, 0)); };
async function openActionHit(window, index = 0) {
  const input = window.document.querySelector('#search-all');
  input.value = 'test';
  input.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  await settle();
  window.document.querySelectorAll('.search-hit')[index].click();
  await settle();
}

test('copy evidence link uses selected action and page bytes, not address-bar filters', async (t) => {
  const data = actionLinkFixture();
  const writes = [];
  let resolve;
  data.configure = (w) => {
    w.history.replaceState(null, '', '/?q=agentrec&exit=completed&verification=PASS#compare');
    Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: (url) => { writes.push(url); return new Promise((r) => { resolve = r; }); } } });
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window;
  await openActionHit(w);
  w.document.querySelector('.action-row[data-index="1"]').click();
  const button = w.document.querySelector('.copy-evidence-link');
  assert.ok(button, 'selected action offers a native copy button');
  assert.equal(button.tagName, 'BUTTON');
  assert.deepEqual(writes, []);
  button.click();
  assert.equal(writes[0], `http://localhost:42817/?run=${data.details.run.id}&focus=actions&action=action-251&actionCursor=98765`);
  assert.doesNotMatch(w.document.querySelector('.evidence-link-status').textContent, /Copied/);
  resolve();
  await settle();
  assert.equal(w.document.querySelector('.evidence-link-status').textContent, 'Copied');
  assert.match(w.document.querySelector('.copy-evidence-link').title, /same Viewer and recorded data/);
});

test('copy evidence link preserves other supported loopback origins', async (t) => {
  const data = actionLinkFixture();
  const writes = [];
  data.configure = (w) => Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: async (url) => writes.push(url) } });
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  dom.reconfigure({ url: dom.window.location.href.replace('localhost', '127.0.0.2') });
  dom.window.document.querySelector('#timeline .action-row').click();
  const button = dom.window.document.querySelector('.copy-evidence-link');
  assert.ok(button, 'supported Viewer loopback origin retains copy control');
  button.click();
  await settle();
  assert.equal(new URL(writes[0]).origin, 'http://127.0.0.2:42817');
});

test('copy evidence link for stored change uses absolute index and no unrelated state', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.changes = paginatedChanges('src/a b&c.js');
  const writes = [];
  data.configure = (w) => {
    w.history.replaceState(null, '', `/?run=${data.details.run.id}&focus=changes&change=src%2Fa+b%26c.js&changeCursor=250&q=agentrec#compare`);
    Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: async (url) => writes.push(url) } });
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window;
  const button = w.document.querySelector('.copy-evidence-link');
  assert.ok(button, 'stored selected change offers copy');
  w.history.replaceState(null, '', `${w.location.href.split('#')[0]}&project=other&action=stale&actionCursor=123&exit=fail&verification=FAIL#compare`);
  button.click();
  await settle();
  assert.equal(writes[0], `http://localhost:42817/?run=${data.details.run.id}&focus=changes&change=src%2Fa+b%26c.js&changeCursor=250`);
});

for (const clipboard of ['absent', 'denied']) test(`copy evidence link ${clipboard} offers focused selectable manual URL`, async (t) => {
  const data = actionLinkFixture();
  data.configure = (w) => {
    if (clipboard === 'denied') Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: async () => { throw new Error('denied'); } } });
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window;
  w.document.querySelector('.action-row').click();
  w.document.querySelector('.copy-evidence-link').click();
  await settle();
  const input = w.document.querySelector('.evidence-link-url');
  assert.ok(input, 'clipboard failure offers manual URL');
  assert.equal(input.readOnly, true);
  assert.equal(input.getAttribute('aria-label'), 'Local evidence URL');
  assert.equal(w.document.activeElement, input);
  assert.equal(input.selectionStart, 0);
  assert.equal(input.selectionEnd, input.value.length);
  assert.match(input.value, /action=action-0&actionCursor=0$/);
  const status = w.document.querySelector('.evidence-link-status');
  assert.equal(status.getAttribute('aria-live'), 'polite');
  assert.equal(status.textContent, 'Clipboard unavailable or denied. Select and copy the local URL manually.');
});

for (const [lang, copy, copied, label, failure, caption] of [
  ['en', 'Copy evidence link', 'Copied', 'Local evidence URL', 'Clipboard unavailable or denied. Select and copy the local URL manually.', 'Local link: requires the same Viewer and recorded data. Not a public share.'],
  ['ko', '증거 링크 복사', '복사됨', '로컬 증거 URL', '클립보드를 사용할 수 없거나 권한이 거부되었습니다. 로컬 URL을 선택하여 직접 복사하세요.', '로컬 링크: 동일한 Viewer와 기록된 데이터가 필요합니다. 공개 공유 링크가 아닙니다.'],
  ['ja', '証拠リンクをコピー', 'コピーしました', 'ローカル証拠URL', 'クリップボードを利用できないか、許可されていません。ローカルURLを選択して手動でコピーしてください。', 'ローカルリンク: 同じViewerと記録済みデータが必要です。公開共有リンクではありません。'],
  ['zh-CN', '复制证据链接', '已复制', '本地证据URL', '剪贴板不可用或权限被拒绝。请选择并手动复制本地URL。', '本地链接：需要同一Viewer和已记录的数据。不是公开分享链接。'],
]) test(`copy evidence link labels and feedback are localized (${lang})`, async (t) => {
  const data = actionLinkFixture();
  let denied = false;
  data.configure = (w) => Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: async () => { if (denied) throw new Error('denied'); } } });
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window;
  w.document.querySelector('#lang').value = lang;
  w.document.querySelector('#lang').dispatchEvent(new w.Event('change', { bubbles: true }));
  w.document.querySelector('.action-row').click();
  const button = w.document.querySelector('.copy-evidence-link');
  assert.equal(button.textContent, copy);
  assert.equal(w.document.querySelector('.evidence-link-caption'), null, 'the caption is no longer a visible paragraph');
  assert.equal(button.title, caption);
  assert.equal(button.getAttribute('aria-description'), caption);
  button.click();
  await settle();
  assert.equal(w.document.querySelector('.evidence-link-status').textContent, copied);
  denied = true;
  button.click();
  await settle();
  assert.equal(w.document.querySelector('.evidence-link-status').textContent, failure);
  assert.equal(w.document.querySelector('.evidence-link-url').getAttribute('aria-label'), label);
});

test('copy evidence link after append retains per-item byte page boundary', async (t) => {
  const data = actionLinkFixture();
  const writes = [];
  data.configure = (w) => Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: async (url) => writes.push(url) } });
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window;
  w.document.querySelector('#timeline .load-more').click();
  await settle();
  w.document.querySelector('.action-row[data-index="251"]').click();
  w.document.querySelector('.copy-evidence-link').click();
  await settle();
  assert.match(writes[0], /action=action-251&actionCursor=98765$/);
});

async function trackedCopyFixture(t, clipboard = 'deferred') {
  const data = fixture('completed', 'pass', 'PASS');
  data.changes = [{ path: 'tracked.js', kind: 'modified', tracked: true }, { path: 'other.js', kind: 'added', tracked: false }];
  let resolveCopy, rejectCopy, releasePatch;
  const writes = [];
  data.configure = (w) => {
    if (clipboard !== 'absent') Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: (url) => {
      writes.push(url);
      return new Promise((resolve, reject) => { resolveCopy = resolve; rejectCopy = () => reject(new Error('denied')); });
    } } });
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window;
  const original = w.fetch;
  w.fetch = (input, init) => String(input).includes('/patch?')
    ? new Promise((resolve) => { releasePatch = () => resolve(response({ path: 'tracked.js', patch: '+retained patch', nextCursor: null })); })
    : original(input, init);
  w.document.querySelector('#timeline-tab-changes').click();
  await settle();
  w.document.querySelector('.action-row').click();
  assert.equal(typeof releasePatch, 'function', 'tracked patch is pending');
  return { w, writes, releasePatch: () => releasePatch(), resolveCopy: () => resolveCopy(), rejectCopy: () => rejectCopy() };
}

test('copy evidence link rejection before tracked patch retains fallback focus and selection', async (t) => {
  const f = await trackedCopyFixture(t);
  const d = f.w.document;
  d.querySelector('.copy-evidence-link').click();
  f.rejectCopy();
  await settle();
  const input = d.querySelector('.evidence-link-url');
  assert.ok(input);
  input.setSelectionRange(7, 20, 'backward');
  f.releasePatch();
  await settle();
  assert.match(d.querySelector('.diff-patch').textContent, /retained patch/);
  assert.equal(d.querySelector('.evidence-link-url'), input);
  assert.equal(d.activeElement, input);
  assert.equal(input.selectionStart, 7);
  assert.equal(input.selectionEnd, 20);
  assert.equal(input.selectionDirection, 'backward');
  assert.equal(input.readOnly, true);
  assert.match(d.querySelector('.evidence-link-status').textContent, /Clipboard unavailable or denied/);
  assert.equal(d.querySelectorAll('.evidence-link').length, 1);
});

for (const outcome of ['resolve', 'reject']) test(`copy evidence link ${outcome} after tracked patch settles current controls`, async (t) => {
  const f = await trackedCopyFixture(t);
  const d = f.w.document;
  const button = d.querySelector('.copy-evidence-link');
  button.click();
  assert.equal(button.disabled, true);
  f.releasePatch();
  await settle();
  assert.equal(d.querySelector('.copy-evidence-link'), button);
  assert.equal(button.disabled, true);
  assert.equal(d.querySelector('.evidence-link-status').textContent, '');
  assert.equal(f.writes.length, 1);
  if (outcome === 'resolve') f.resolveCopy();
  else f.rejectCopy();
  await settle();
  assert.equal(button.disabled, false);
  if (outcome === 'resolve') {
    assert.equal(d.querySelector('.evidence-link-status').textContent, 'Copied');
    assert.equal(d.querySelector('.evidence-link-url'), null);
  } else {
    const input = d.querySelector('.evidence-link-url');
    assert.ok(input);
    assert.equal(input.value, f.writes[0]);
    assert.equal(input.readOnly, true);
    assert.equal(d.activeElement, input);
    assert.equal(input.selectionStart, 0);
    assert.equal(input.selectionEnd, input.value.length);
    assert.match(d.querySelector('.evidence-link-status').textContent, /Clipboard unavailable or denied/);
  }
  assert.equal(d.querySelectorAll('.evidence-link').length, 1);
  // A subsequent attempt must still be invalidated by a genuinely new target.
  button.click();
  d.querySelectorAll('#timeline .action-row')[1].click();
  const current = d.querySelector('.copy-evidence-link');
  current.focus();
  if (outcome === 'resolve') f.resolveCopy();
  else f.rejectCopy();
  await settle();
  assert.equal(d.querySelector('.evidence-link-status').textContent, '');
  assert.equal(d.querySelector('.evidence-link-url'), null);
  assert.equal(d.activeElement, current);
  assert.equal(current.disabled, false);
  button.click();
  assert.equal(f.writes.length, 2, 'detached old control cannot write again');
});

for (const keepFocus of [true, false]) test(`copy evidence link missing API survives tracked patch without stealing focus (focused=${keepFocus})`, async (t) => {
  const f = await trackedCopyFixture(t, 'absent');
  const d = f.w.document;
  d.querySelector('.copy-evidence-link').click();
  await settle();
  const input = d.querySelector('.evidence-link-url');
  assert.ok(input);
  const elsewhere = d.querySelector('#timeline-search');
  if (!keepFocus) elsewhere.focus();
  f.releasePatch();
  await settle();
  assert.equal(d.querySelector('.evidence-link-url'), input);
  assert.equal(d.activeElement, keepFocus ? input : elsewhere);
  assert.equal(input.selectionStart, 0);
  assert.equal(input.selectionEnd, input.value.length);
  assert.equal(d.querySelectorAll('.copy-evidence-link').length, 1);
  assert.match(d.querySelector('.evidence-link-status').textContent, /Clipboard unavailable or denied/);
});

for (const outcome of ['resolve', 'reject']) test(`copy evidence link async ${outcome} never labels a new selection`, async (t) => {
  const data = actionLinkFixture();
  let finish;
  data.configure = (w) => Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: () => new Promise((resolve, reject) => { finish = () => outcome === 'resolve' ? resolve() : reject(new Error('denied')); }) } });
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window;
  w.document.querySelector('.action-row').click();
  w.document.querySelector('.copy-evidence-link').click();
  w.document.querySelector('.action-row[data-index="1"]').click();
  const current = w.document.querySelector('.copy-evidence-link');
  current.focus();
  finish();
  await settle();
  assert.equal(w.document.querySelector('.evidence-link-status').textContent, '');
  assert.equal(w.document.querySelector('.evidence-link-url'), null);
  assert.equal(w.document.activeElement, current);
  assert.equal(current.disabled, false);
});

for (const rerender of [false, true]) test(`copy evidence link rejects pending run transition and stale rows (rerender=${rerender})`, async (t) => {
  const data = actionLinkFixture();
  const original = data.details;
  const next = { ...original, run: { ...original.run, id: 'next-run' } };
  data.list.runs.push({ ...next.run, verification: 'PASS' });
  let release;
  data.details = (id) => id === original.run.id ? original : new Promise((r) => { release = () => r(next); });
  const writes = [];
  data.configure = (w) => Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: async (url) => writes.push(url) } });
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window;
  w.document.querySelector('.action-row').click();
  const oldButton = w.document.querySelector('.copy-evidence-link');
  w.document.querySelector('[data-run-id="next-run"]').click();
  await settle();
  if (rerender) {
    w.document.querySelector('#timeline-search').dispatchEvent(new w.Event('input', { bubbles: true }));
    await new Promise((r) => setTimeout(r, 220));
  }
  w.document.querySelector('.action-row').click();
  oldButton.click();
  w.document.querySelector('.copy-evidence-link')?.click();
  assert.deepEqual(writes, []);
  assert.equal(new URLSearchParams(w.location.search).get('run'), 'next-run');
  release();
  await settle();
  assert.equal(w.document.querySelector('.copy-evidence-link'), null);
});

test('copy evidence link is absent for no selection and unsupported provider event', async (t) => {
  const data = actionLinkFixture();
  data.details.eventCount = 1;
  data.events = [{ type: 'provider.event', value: 'test' }];
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window;
  assert.equal(w.document.querySelector('.copy-evidence-link'), null);
  w.document.querySelector('#timeline-tab-events').click();
  await settle();
  w.document.querySelector('.action-row').click();
  assert.equal(w.document.querySelector('.copy-evidence-link'), null);
});

test('copy evidence link excludes live working-tree selections', async (t) => {
  const dom = await renderFixture(fixture('running', 'pending', 'RUNNING'));
  t.after(() => dom.window.close());
  const w = dom.window;
  const original = w.fetch;
  w.fetch = (input, init) => String(input).endsWith('/live') ? response({ files: [{ path: 'live.js', status: 'M' }] }) : original(input, init);
  w.document.querySelector('#timeline-tab-changes').click();
  await settle();
  w.document.querySelector('.change-row').click();
  assert.match(w.document.querySelector('#inspector').textContent, /Working tree/);
  assert.equal(w.document.querySelector('.copy-evidence-link'), null);
});

test('copy evidence link excludes actions without exact IDs and empty runs', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const empty = await renderFixture(data);
  t.after(() => empty.window.close());
  assert.equal(empty.window.document.querySelector('.copy-evidence-link'), null);
  data.details.actionCount = 1;
  data.actions = [{ type: 'tool.call', input: { command: 'test' } }];
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  dom.window.document.querySelector('.action-row').click();
  assert.equal(dom.window.document.querySelector('.copy-evidence-link'), null);
});

test('copy evidence link controls have bounded themed styles and visible focus', () => {
  assert.match(css, /\.copy-evidence-link\s*\{[^}]*background:\s*var\(--panel\)/);
  assert.match(css, /\.evidence-link-url\s*\{[^}]*width:\s*100%/);
  assert.match(css, /:focus-visible\s*\{[^}]*outline:/);
});

test('action search records exact byte link and reload restores selection', async (t) => {
  const data = actionLinkFixture();
  data.configure = (w) => w.history.replaceState(null, '', '/?q=agentrec#kept');
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  await openActionHit(dom.window);
  const params = new URLSearchParams(dom.window.location.search);
  assert.equal(params.get('action'), 'action-250');
  assert.equal(params.get('actionCursor'), '98765');
  assert.equal(params.get('q'), 'agentrec');
  assert.equal(dom.window.location.hash, '#kept');
  const url = dom.window.location.href;
  const reloaded = await renderFixture({ ...data, configure: (w) => w.history.replaceState(null, '', url) });
  t.after(() => reloaded.window.close());
  assert.match(reloaded.window.document.querySelector('.action-row.selected').textContent, /go test/);
  assert.match(reloaded.window.document.querySelector('#inspector').textContent, /go test/);
});

test('manual second action retains its page byte cursor across reload', async (t) => {
  const data = actionLinkFixture();
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  await openActionHit(dom.window);
  dom.window.document.querySelector('.action-row[data-index="1"]').click();
  const params = new URLSearchParams(dom.window.location.search);
  assert.equal(params.get('action'), 'action-251');
  assert.equal(params.get('actionCursor'), '98765');
  const reload = await renderFixture({ ...data, configure: (w) => w.history.replaceState(null, '', dom.window.location.href) });
  t.after(() => reload.window.close());
  assert.match(reload.window.document.querySelector('.action-row.selected').textContent, /git status/);
});

test('action tab and history roundtrip clears exact params and restores page zero', async (t) => {
  const dom = await renderFixture(actionLinkFixture());
  t.after(() => dom.window.close());
  const w = dom.window;
  await openActionHit(w);
  w.document.querySelector('#timeline-tab-events').click();
  await settle();
  assert.equal(new URLSearchParams(w.location.search).has('action'), false);
  w.history.back();
  await settle();
  assert.match(w.document.querySelector('.action-row.selected').textContent, /go test/);
  w.history.forward();
  await settle();
  assert.equal(w.document.querySelector('#timeline-tab-events').getAttribute('aria-selected'), 'true');
  w.history.back();
  await settle();
  w.document.querySelector('#timeline-tab-actions').click();
  await settle();
  assert.equal(new URLSearchParams(w.location.search).has('action'), false);
  assert.match(w.document.querySelector('.action-row').textContent, /command-0/);
  assert.equal(w.document.querySelector('.action-row.selected'), null);
});

for (const rerender of [false, true]) test(`stale action row cannot hijack deferred destination (rerender=${rerender})`, async (t) => {
  const data = actionLinkFixture();
  const next = { ...data.details, run: { ...data.details.run, id: 'next-run' }, snapshotId: 'next-snapshot' };
  data.list.runs.push({ ...next.run, verification: 'PASS' });
  let resolve;
  data.details = ((original) => (id) => id === original.run.id ? original : new Promise((r) => { resolve = () => r(next); }))(data.details);
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window;
  w.document.querySelector('.action-row').click();
  let row = w.document.querySelector('.action-row');
  w.document.querySelector('[data-run-id="next-run"]').click();
  await settle();
  if (rerender) {
    const input = w.document.querySelector('#timeline-search');
    input.dispatchEvent(new w.Event('input', { bubbles: true }));
    await new Promise((r) => setTimeout(r, 220));
    row = w.document.querySelector('.action-row');
  }
  row.click();
  assert.equal(new URLSearchParams(w.location.search).get('run'), 'next-run');
  assert.equal(new URLSearchParams(w.location.search).has('action'), false);
  resolve();
  await settle();
  assert.equal(w.document.querySelector('.action-row.selected'), null);
});

for (const delay of [0, 220]) test(`action hit clears pending or settled timeline filter (${delay})`, async (t) => {
  const dom = await renderFixture(actionLinkFixture());
  t.after(() => dom.window.close());
  const w = dom.window;
  const input = w.document.querySelector('#timeline-search');
  input.value = 'not-present';
  input.dispatchEvent(new w.Event('input', { bubbles: true }));
  if (delay) await new Promise((r) => setTimeout(r, delay));
  await openActionHit(w, 1);
  await new Promise((r) => setTimeout(r, 220));
  assert.match(w.document.querySelector('.action-row.selected').textContent, /git status/);
  assert.equal(input.value, '');
});

for (const cursor of ['-1', '1.5', 'NaN', '9007199254740992', '01']) test(`malformed action cursor ${cursor} never selects a substitute`, async (t) => {
  const data = actionLinkFixture();
  data.configure = (w) => w.history.replaceState(null, '', `/?run=${data.details.run.id}&focus=actions&action=action-250&actionCursor=${cursor}`);
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  assert.equal(dom.window.document.querySelector('.action-row.selected'), null);
  assert.ok(dom.window.__fetchPaths.includes('/api/snapshots/snapshot/actions?cursor=0'));
});

test('nonexistent action ID at valid byte cursor never selects first row', async (t) => {
  const data = actionLinkFixture();
  data.search.hits[0].actionId = 'missing-action';
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  await openActionHit(dom.window);
  assert.equal(dom.window.document.querySelector('.action-row.selected'), null);
});

test('pending exact restoration cannot overwrite newer run or focus', async (t) => {
  const data = actionLinkFixture();
  const original = data.details;
  const other = { ...original, run: { ...original.run, id: 'other-run' } };
  data.list.runs.push({ ...other.run, verification: 'PASS' });
  let release;
  let deferred = false;
  data.details = (id) => id === 'other-run' ? other : deferred ? new Promise((r) => { release = () => r(original); }) : original;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  deferred = true;
  await openActionHit(dom.window);
  dom.window.document.querySelector('[data-run-id="other-run"]').click();
  await settle();
  release();
  await settle();
  assert.equal(new URLSearchParams(dom.window.location.search).get('run'), 'other-run');
  assert.equal(dom.window.document.querySelector('.action-row.selected'), null);
});

test('manual selection after append stores byte page cursor not row index', async (t) => {
  const data = actionLinkFixture();
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  dom.window.document.querySelector('#timeline .load-more').click();
  await settle();
  dom.window.document.querySelector('.action-row[data-index="251"]').click();
  assert.equal(new URLSearchParams(dom.window.location.search).get('actionCursor'), '98765');
  const reload = await renderFixture({ ...data, configure: (w) => w.history.replaceState(null, '', dom.window.location.href) });
  t.after(() => reload.window.close());
  assert.match(reload.window.document.querySelector('.action-row.selected').textContent, /git status/);
});

// Defer one real fixture response, ignoring abort deliberately so stale results
// still exercise the application's generation checks when explicitly released.
function deferFetch(window, matches) {
  const original = window.fetch;
  let release;
  let pending = false;
  window.fetch = (input, init) => {
    const result = original(input, init);
    if (!pending && matches(String(input))) {
      pending = true;
      return new Promise((resolve) => { release = () => resolve(result); });
    }
    return result;
  };
  return () => {
    assert.ok(release, 'expected request must be pending before release');
    release();
  };
}

for (const destination of ['same-run search', 'cross-run search', 'history']) test(`old ordinary reset cannot clear newer exact action: ${destination}`, async (t) => {
  const data = actionLinkFixture();
  const original = data.details;
  const other = { ...original, snapshotId: 'other-snapshot', run: { ...original.run, id: 'other-run' } };
  data.list.runs.push({ ...other.run, verification: 'PASS' });
  data.details = (id) => id === 'other-run' ? other : original;
  if (destination === 'cross-run search') data.search.hits[1].runId = 'other-run';
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window;
  await openActionHit(w);
  if (destination === 'history') await openActionHit(w, 1);
  const release = deferFetch(w, (url) => url.endsWith('/actions?cursor=0'));
  w.document.querySelector('#timeline-tab-actions').click();
  await settle();
  if (destination === 'history') {
    w.history.back();
    await settle();
  } else await openActionHit(w, 1);
  const expectedURL = w.location.href;
  assert.match(w.document.querySelector('.action-row.selected')?.textContent || '', /git status/);
  release();
  await settle();
  assert.equal(w.location.href, expectedURL);
  assert.equal(new URLSearchParams(w.location.search).get('action'), 'action-251');
  assert.match(w.document.querySelector('.action-row.selected')?.textContent || '', /git status/);
  assert.match(w.document.querySelector('#inspector').textContent, /git status/);
  assert.match(w.document.querySelector('.action-row').textContent, /git status/);
});

for (const pending of ['exact page', 'same-run detail', 'cross-run detail', 'append', 'reset']) {
  for (const mode of ['actions', 'events', 'changes']) test(`ordinary ${mode} supersedes pending ${pending} with page zero`, async (t) => {
    const data = actionLinkFixture();
    data.details.eventCount = 1;
    data.events = [{ type: 'stdout', text: 'ordinary event' }];
    data.changes = [{ path: 'ordinary.go', kind: 'added', tracked: false }];
    const original = data.details;
    const other = { ...original, snapshotId: 'other-snapshot', run: { ...original.run, id: 'other-run' } };
    data.list.runs.push({ ...other.run, verification: 'PASS' });
    data.details = (id) => id === 'other-run' ? other : original;
    if (pending === 'cross-run detail') data.search.hits[0].runId = 'other-run';
    if (pending === 'append') {
      const pages = data.actions;
      data.actions = (cursor) => cursor === 98765
        ? { items: pages(cursor).items.slice(0, 1), nextCursor: 99234 }
        : pages(cursor);
    }
    data.configure = (w) => w.history.replaceState(null, '', '/?q=agentrec&exit=completed&verification=PASS#kept');
    const dom = await renderFixture(data);
    t.after(() => dom.window.close());
    const w = dom.window;
    let release;
    if (pending === 'append' || pending === 'reset') {
      await openActionHit(w);
      release = deferFetch(w, (url) => url.endsWith(`/actions?cursor=${pending === 'append' ? 99234 : 0}`));
      w.document.querySelector(pending === 'append' ? '#timeline .load-more' : '#timeline-tab-actions').click();
      await settle();
    } else {
      release = deferFetch(w, (url) => pending === 'exact page'
        ? url.endsWith('/actions?cursor=98765')
        : url === `/api/runs/${pending === 'cross-run detail' ? 'other-run' : original.run.id}`);
      await openActionHit(w);
    }
    const requestsBeforeOrdinary = w.__fetchPaths.length;
    w.document.querySelector(`#timeline-tab-${mode}`).click();
    await settle();
    const ordinaryURL = w.location.href;
    const params = new URLSearchParams(w.location.search);
    assert.equal(params.has('action'), false);
    assert.equal(params.has('actionCursor'), false);
    assert.equal(params.get('focus'), mode);
    assert.equal(params.get('run'), pending === 'cross-run detail' ? 'other-run' : original.run.id);
    assert.equal(params.get('q'), 'agentrec');
    assert.equal(params.get('exit'), 'completed');
    assert.equal(params.get('verification'), 'PASS');
    assert.equal(w.location.hash, '#kept');
    const snapshot = pending === 'cross-run detail' ? 'other-snapshot' : 'snapshot';
    assert.ok(w.__fetchPaths.slice(requestsBeforeOrdinary).includes(`/api/snapshots/${snapshot}/actions?cursor=0`), 'ordinary navigation must issue a fresh page-zero load before the obsolete response resolves');
    assert.equal(w.document.querySelector(`#timeline-tab-${mode}`).getAttribute('aria-selected'), 'true');
    release();
    await settle();
    assert.equal(w.location.href, ordinaryURL);
    assert.equal(w.document.querySelector(`#timeline-tab-${mode}`).getAttribute('aria-selected'), 'true');
    assert.equal(w.document.querySelector('.action-row.selected, .change-row.selected'), null);
    if (mode !== 'actions') {
      w.document.querySelector('#timeline-tab-actions').click();
      await settle();
    }
    assert.match(w.document.querySelector('.action-row').textContent, /command-0/);
    assert.equal(w.document.querySelectorAll('.action-row').length, 250);
    assert.equal(w.document.querySelector('.action-row.selected'), null);
    assert.doesNotMatch(w.document.querySelector('#inspector').textContent, /go test|git status/);
  });
}

test('ordinary actions after leaving exact tab starts at page zero', async (t) => {
  const dom = await renderFixture(actionLinkFixture());
  t.after(() => dom.window.close());
  await openActionHit(dom.window);
  dom.window.document.querySelector('#timeline-tab-events').click();
  await settle();
  dom.window.document.querySelector('#timeline-tab-actions').click();
  await settle();
  assert.match(dom.window.document.querySelector('.action-row').textContent, /command-0/);
});

function fixture(exitReason, statusClass, statusLabel) {
  const startedAt = '2026-09-03T00:00:00Z';
  const run = {
    id: '20260903T000000.000000000Z-00000001',
    provider: 'claude',
    project: 'agentrec',
    cwd: '/repo',
    prompt: 'test',
    startedAt,
    exit: exitReason,
    exitReason,
    statusClass,
    statusLabel,
    warningCount: 0,
  };
  return {
    list: { runs: [{ ...run, verification: 'PASS' }], total: 1, unreadable: 0 },
    details: {
      snapshotId: 'snapshot',
      run,
      actionCount: 0,
      eventCount: 0,
      changes: { status: 'complete', files: [] },
      evidence: { supervisor: [], verification: [{ name: 'Status', value: 'PASS' }], repository: [], sections: [] },
    },
  };
}

test('session_lost is failure-class in list and detail', async (t) => {
  const dom = await renderFixture(fixture('session_lost', 'fail', 'session_lost'));
  t.after(() => dom.window.close());
  const { document } = dom.window;
  assert.match(document.querySelector('.run-item .run-verdict-run').className, /\bfail\b/);
  assert.match(document.querySelector('#run-verdict').className, /\bfail\b/);
});

test('global search opens an exact changed-file result', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const path = 'internal/cli/search-target.go';
  data.details.run.changeCount = 251;
  const dom = await renderFixture({
    ...data,
    changes: paginatedChanges(path),
    search: {
      hits: [{
        runId: data.details.run.id,
        project: data.details.run.project,
        provider: data.details.run.provider,
        kind: 'change',
        type: 'modified',
        path,
        index: 250,
        snippet: path,
        createdAt: data.details.run.startedAt,
        status: 'PASS',
      }],
      truncated: false,
    },
  });
  t.after(() => dom.window.close());
  const { document, Event, KeyboardEvent } = dom.window;
  const timelineSearch = document.querySelector('#timeline-search');
  timelineSearch.value = 'unrelated-filter';
  timelineSearch.dispatchEvent(new Event('input', { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 220));
  const input = document.querySelector('#global-search input');
  input.value = 'search-target.go';
  input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  for (let i = 0; i < 10; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));

  const hit = document.querySelector('#search-results .search-hit');
  assert.ok(hit);
  assert.equal(hit.querySelector('.search-kind').textContent, 'change');
  assert.equal(hit.querySelector('.search-snippet').textContent, path);
  hit.click();
  for (let i = 0; i < 10; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(document.querySelector('#timeline-tab-changes').getAttribute('aria-selected'), 'true');
  assert.ok(dom.window.__fetchPaths.includes(`/api/snapshots/${data.details.snapshotId}/changes?cursor=250`), dom.window.__fetchPaths.join('\n'));
  const row = document.querySelector(`.change-row[data-path="${path}"]`);
  assert.ok(row);
  assert.match(row.className, /\bselected\b/);
  assert.equal(document.querySelector('.inspector-title').textContent, path);
  const params = new URLSearchParams(dom.window.location.search);
  assert.equal(params.get('focus'), 'changes');
  assert.equal(params.get('change'), path);
  assert.equal(params.get('changeCursor'), '250');
});

test('changed-file deep link restores the exact paginated row and inspector', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const path = 'internal/cli/search-target.go';
  data.details.run.changeCount = 251;
  data.configure = (window) => window.history.replaceState(null, '', `/?run=${data.details.run.id}&focus=changes&change=${encodeURIComponent(path)}&changeCursor=250#kept`);

  const dom = await renderFixture({
    ...data,
    changes: paginatedChanges(path),
  });
  t.after(() => dom.window.close());
  const { document, location } = dom.window;

  assert.ok(dom.window.__fetchPaths.includes(`/api/snapshots/${data.details.snapshotId}/changes?cursor=250`), dom.window.__fetchPaths.join('\n'));
  assert.equal(document.querySelector('#timeline-tab-changes').getAttribute('aria-selected'), 'true');
  const row = document.querySelector(`.change-row[data-path="${path}"]`);
  assert.ok(row);
  assert.match(row.className, /\bselected\b/);
  assert.equal(document.querySelector('.inspector-title').textContent, path);
  assert.equal(location.hash, '#kept');
});

test('outgoing change row cannot overwrite a deferred incoming run link', async (t) => {
  for (const rerender of [false, true]) {
    await t.test(rerender ? 'after Changes tab re-render' : 'original node', async (t) => {
      const data = fixture('completed', 'pass', 'PASS');
      const incoming = { ...data.details, snapshotId: 'snapshot-b', run: { ...data.details.run, id: 'run-b' } };
      let release;
      const deferred = new Promise((resolve) => { release = resolve; });
      const dom = await renderFixture({
        ...data,
        list: { ...data.list, runs: [...data.list.runs, { ...incoming.run, verification: 'PASS' }], total: 2 },
        details: (id) => id === incoming.run.id ? deferred : data.details,
        changes: [{ path: 'old-run-only.go', kind: 'added', tracked: false }],
        configure: (window) => window.history.replaceState(null, '', `/?run=${data.details.run.id}&focus=changes&q=agentrec&verification=PASS#kept`),
      });
      t.after(() => dom.window.close());
      const { document, history, location } = dom.window;
      const originalFetch = dom.window.fetch;
      dom.window.fetch = (input, init) => String(input).includes('/api/snapshots/snapshot-b/changes')
        ? response({ items: [{ path: 'new-run-only.go', kind: 'added', tracked: false }], nextCursor: null, total: 1, status: 'available' })
        : originalFetch(input, init);
      let oldRow = document.querySelector('.change-row[data-path="old-run-only.go"]');
      assert.ok(oldRow);
      document.querySelector('.run-item[data-run-id="run-b"]').click();
      if (rerender) {
        document.querySelector('#timeline-tab-changes').click();
        const rerenderedRow = document.querySelector('.change-row[data-path="old-run-only.go"]');
        assert.ok(rerenderedRow);
        assert.notEqual(rerenderedRow, oldRow);
        oldRow = rerenderedRow;
      }
      const destination = location.href;
      const historyLength = history.length;
      assert.equal(new URL(destination).searchParams.get('run'), 'run-b');
      oldRow.click();
      assert.equal(location.href, destination);
      assert.equal(history.length, historyLength);
      assert.equal(document.querySelector('.change-row.selected'), null);
      release(incoming);
      for (let i = 0; i < 10; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
      assert.equal(document.querySelector('.change-row')?.dataset.path, 'new-run-only.go');
      assert.equal(document.querySelector('.change-row.selected'), null);
      assert.equal(location.href, destination);
      assert.equal(new URL(location.href).searchParams.get('q'), 'agentrec');
      assert.equal(new URL(location.href).searchParams.get('verification'), 'PASS');
      assert.equal(location.hash, '#kept');
    });
  }
});

test('ordinary focus navigation clears exact change state and history restores it', async (t) => {
  for (const focus of ['actions', 'events', 'changes', 'verification']) {
    await t.test(focus, async (t) => {
      const data = fixture('completed', 'pass', 'PASS');
      data.details.evidence.verification.push({ name: 'Warning', value: 'Review verification evidence' });
      const initial = `/?run=${data.details.run.id}&focus=changes&change=last.go&changeCursor=250&q=agentrec&exit=completed&verification=PASS#kept`;
      const dom = await renderFixture({ ...data, changes: paginatedChanges('last.go'), configure: (window) => window.history.replaceState(null, '', initial) });
      t.after(() => dom.window.close());
      const { document, history, location } = dom.window;
      assert.equal(document.querySelector('.change-row.selected')?.dataset.path, 'last.go');
      const historyLength = history.length;
      document.querySelector(focus === 'verification' ? '#triage-verification' : `#timeline-tab-${focus}`).click();
      const params = new URLSearchParams(location.search);
      assert.equal(params.get('focus'), focus);
      assert.equal(params.has('change'), false);
      assert.equal(params.has('changeCursor'), false);
      assert.equal(history.length, historyLength + 1);
      assert.equal(params.get('q'), 'agentrec');
      assert.equal(params.get('exit'), 'completed');
      assert.equal(params.get('verification'), 'PASS');
      assert.equal(location.hash, '#kept');
      if (focus !== 'changes') document.querySelector('#timeline-tab-changes').click();
      const ordinary = location.href;
      assert.equal(new URL(ordinary).searchParams.has('change'), false);
      assert.equal(new URL(ordinary).searchParams.has('changeCursor'), false);
      assert.equal(document.querySelector('.change-row.selected'), null);
      assert.equal(document.querySelector('.inspector-title'), null);
      const finalHistoryLength = history.length;
      history.go(focus === 'changes' ? -1 : -2);
      for (let i = 0; i < 20; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
      assert.equal(`${location.pathname}${location.search}${location.hash}`, initial);
      assert.equal(document.querySelector('.change-row.selected')?.dataset.path, 'last.go');
      assert.equal(document.querySelector('.inspector-title')?.textContent, 'last.go');
      assert.equal(history.length, finalHistoryLength);
      history.go(focus === 'changes' ? 1 : 2);
      for (let i = 0; i < 20; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
      assert.equal(location.href, ordinary);
      assert.equal(document.querySelector('.change-row.selected'), null);
      assert.equal(history.length, finalHistoryLength);
    });
  }
});

test('selecting a different changed file updates the exact link', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.configure = (window) => window.history.replaceState(null, '', `/?run=${data.details.run.id}&focus=changes&change=first.go&changeCursor=250#kept`);
  const items = Array.from({ length: 252 }, (_, index) => ({ path: index === 250 ? 'first.go' : index === 251 ? 'second.go' : `prefix/file-${index}.go`, kind: 'added', tracked: false }));
  data.details.run.changeCount = items.length;
  const changes = (cursor) => ({ items: items.slice(cursor, cursor + 250), nextCursor: cursor + 250 < items.length ? cursor + 250 : null, total: items.length, status: 'available' });
  const dom = await renderFixture({ ...data, changes });
  t.after(() => dom.window.close());
  assert.equal(dom.window.document.querySelector('.change-row.selected')?.dataset.path, 'first.go');
  assert.equal(dom.window.document.querySelectorAll('.change-row').length, 2);
  assert.ok(dom.window.__fetchPaths.includes('/api/snapshots/snapshot/changes?cursor=250'));
  dom.window.document.querySelector('.change-row[data-path="second.go"]').click();
  const params = new URLSearchParams(dom.window.location.search);
  assert.equal(params.get('change'), 'second.go');
  assert.equal(params.get('changeCursor'), '251');
  assert.equal(dom.window.location.hash, '#kept');
  assert.equal(dom.window.document.querySelector('.change-row.selected')?.dataset.path, 'second.go');
  assert.equal(dom.window.document.querySelector('.inspector-title')?.textContent, 'second.go');
  const reload = await renderFixture({ ...data, changes, configure: (window) => window.history.replaceState(null, '', dom.window.location.href) });
  t.after(() => reload.window.close());
  assert.ok(reload.window.__fetchPaths.includes('/api/snapshots/snapshot/changes?cursor=251'));
  assert.equal(reload.window.document.querySelectorAll('.change-row').length, 1);
  assert.equal(reload.window.document.querySelector('.change-row.selected')?.dataset.path, 'second.go');
  assert.equal(reload.window.document.querySelector('.inspector-title')?.textContent, 'second.go');
  assert.equal(reload.window.location.href, dom.window.location.href);
});

test('leaving an exact change link restores page zero for ordinary or malformed links', async (t) => {
  for (const suffix of ['', '&change=last.go', '&change=last.go&changeCursor=bogus', '&changeCursor=250']) {
    const data = fixture('completed', 'pass', 'PASS');
    data.configure = (window) => window.history.replaceState(null, '', `/?run=${data.details.run.id}&focus=changes&change=last.go&changeCursor=250`);
    const items = Array.from({ length: 251 }, (_, i) => ({ path: i === 250 ? 'last.go' : `file${i}.go`, tracked: false, kind: 'added' }));
    const dom = await renderFixture({ ...data, changes: (cursor) => ({ items: items.slice(cursor, cursor + 250), nextCursor: cursor + 250 < items.length ? cursor + 250 : null, total: items.length, status: 'available' }) });
    t.after(() => dom.window.close());
    assert.equal(dom.window.document.querySelector('.change-row.selected')?.dataset.path, 'last.go');
    dom.window.__fetchPaths.length = 0;
    dom.window.history.pushState(null, '', `/?run=${data.details.run.id}&focus=changes${suffix}`);
    dom.window.dispatchEvent(new dom.window.PopStateEvent('popstate'));
    for (let i = 0; i < 10; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
    assert.ok(dom.window.__fetchPaths.includes('/api/snapshots/snapshot/changes?cursor=0'), suffix);
    assert.equal(dom.window.document.querySelector('.change-row')?.dataset.path, 'file0.go');
    assert.equal(dom.window.document.querySelector('.change-row.selected'), null);
  }
});

test('changed-file search badges use the selected language', async (t) => {
  for (const [language, label] of [['ko', '변경'], ['ja', '変更'], ['zh-CN', '变更']]) {
    const data = fixture('completed', 'pass', 'PASS');
    const dom = await renderFixture({ ...data, search: { hits: [{ runId: data.details.run.id, kind: 'change', path: 'target.go', snippet: 'target.go', index: 0 }], truncated: false } });
    t.after(() => dom.window.close());
    const { document, Event, KeyboardEvent } = dom.window;
    document.querySelector('#lang').value = language;
    document.querySelector('#lang').dispatchEvent(new Event('change'));
    document.querySelector('#search-all').value = 'target';
    document.querySelector('#search-all').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    for (let i = 0; i < 5; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
    assert.equal(document.querySelector('.search-kind').textContent, label, language);
  }
});

test('same-run popstate restores an exact changed-file deep link', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const path = 'internal/cli/search-target.go';
  data.details.run.changeCount = 251;
  data.configure = (window) => window.history.replaceState(null, '', `/?run=${data.details.run.id}&focus=changes#kept`);

  const dom = await renderFixture({
    ...data,
    changes: paginatedChanges(path),
  });
  t.after(() => dom.window.close());
  const { document, history, PopStateEvent, Event } = dom.window;
  document.querySelector('#timeline-search').value = 'unrelated';
  document.querySelector('#timeline-search').dispatchEvent(new Event('input', { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 220));

  history.pushState(null, '', `/?run=${data.details.run.id}&focus=changes&change=${encodeURIComponent(path)}&changeCursor=250#kept`);
  dom.window.dispatchEvent(new PopStateEvent('popstate'));
  for (let i = 0; i < 10; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));

  assert.ok(dom.window.__fetchPaths.includes(`/api/snapshots/${data.details.snapshotId}/changes?cursor=250`), dom.window.__fetchPaths.join('\n'));
  const row = document.querySelector(`.change-row[data-path="${path}"]`);
  assert.ok(row);
  assert.match(row.className, /\bselected\b/);
  assert.equal(document.querySelector('.inspector-title').textContent, path);
});

test('run list separates process and verification verdicts before opening a run', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs = [
    { ...data.list.runs[0], id: 'run-process-failed', exit: 'nonzero', verification: 'PASS', warningCount: 0 },
    { ...data.list.runs[0], id: 'run-verification-failed', exit: 'completed', verification: 'FAIL', warningCount: 2 },
    { ...data.list.runs[0], id: 'run-verification-tainted', exit: 'completed', verification: 'TAINTED', warningCount: 1 },
    { ...data.list.runs[0], id: 'run-pending', exit: 'unknown', verification: 'PENDING', warningCount: 0 },
    { ...data.list.runs[0], id: 'run-parse-error', exit: 'parse_error', verification: 'NOT RUN', warningCount: 1 },
  ];
  data.list.total = data.list.runs.length;

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  const cards = new Map(Array.from(document.querySelectorAll('.run-item'), (card) => [card.dataset.runId, card]));
  const verdicts = (id) => Array.from(cards.get(id).querySelectorAll('.run-verdict'), (badge) => badge.textContent.trim());

  assert.deepEqual(verdicts('run-process-failed'), ['Run NONZERO', 'Verify PASS']);
  assert.deepEqual(verdicts('run-verification-failed'), ['Run COMPLETED', 'Verify FAIL']);
  assert.deepEqual(verdicts('run-verification-tainted'), ['Run COMPLETED', 'Verify TAINTED']);
  assert.deepEqual(verdicts('run-pending'), ['Run UNKNOWN', 'Verify PENDING']);
  assert.equal(cards.get('run-process-failed').querySelector('.run-verdict-run').classList.contains('fail'), true);
  assert.equal(cards.get('run-process-failed').querySelector('.run-verdict-verify').classList.contains('pass'), false);
  assert.equal(cards.get('run-verification-failed').querySelector('.run-verdict-run').classList.contains('pass'), false);
  assert.equal(cards.get('run-verification-failed').querySelector('.run-verdict-verify').classList.contains('fail'), true);
  assert.equal(cards.get('run-verification-tainted').querySelector('.run-verdict-verify').classList.contains('warn'), true);
  assert.equal(cards.get('run-pending').querySelector('.run-verdict-verify').classList.contains('fail'), false);
  assert.equal(cards.get('run-pending').querySelector('.run-verdict-verify .run-verdict-value').title, 'Verification is pending.');
  assert.equal(cards.get('run-verification-failed').querySelector('.run-warning-count').textContent.trim(), '2 warnings');
  assert.match(css, /\.run-verdict-kind \{[^}]*font-size: 11px;[^}]*opacity: 1;/);

  const language = document.querySelector('#lang');
  language.value = 'ko';
  language.dispatchEvent(new dom.window.Event('change', { bubbles: true }));
  const localizedCards = new Map(Array.from(document.querySelectorAll('.run-item'), (card) => [card.dataset.runId, card]));
  const localizedVerdicts = (id) => Array.from(localizedCards.get(id).querySelectorAll('.run-verdict'), (badge) => badge.textContent.trim());
  assert.deepEqual(localizedVerdicts('run-process-failed'), ['실행 비정상 종료', '검증 통과']);
  assert.deepEqual(localizedVerdicts('run-verification-failed'), ['실행 정상 종료', '검증 실패']);
  assert.deepEqual(localizedVerdicts('run-parse-error'), ['실행 파싱 오류', '검증 미실행']);
  assert.equal(localizedCards.get('run-verification-failed').querySelector('.run-warning-count').textContent.trim(), '경고 2개');
});

test('long run titles stay within a shrinkable sidebar grid track', () => {
  assert.match(css, /\.run-list\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)/);
});

test('run list is title-first and searches loaded summary titles without detail calls', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const base = data.list.runs[0], detail = data.details;
  data.list.runs = [
    { ...base, id: 'normal-id', title: 'Review the release notes', project: 'agentrec', exit: 'completed', verification: 'PASS' },
    { ...base, id: 'failure-id', title: 'Repair the broken build', project: 'dogfood', exit: 'nonzero', verification: 'FAIL' },
    { ...base, id: 'warning-id', title: 'Inspect tainted evidence', exit: 'completed', verification: 'TAINTED' },
    { ...base, id: 'running-id', title: 'Watch the active recorder', exit: 'running', verification: 'PENDING' },
    { ...base, id: 'fallback-id', title: '' },
  ];
  data.list.total = data.list.runs.length;
  data.details = (id) => ({ ...detail, run: { ...detail.run, id } });
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  const rows = new Map(Array.from(d.querySelectorAll('.run-item'), (row) => [row.dataset.runId, row]));

  assert.equal(rows.get('normal-id').querySelector('.run-title-text').textContent, 'Review the release notes');
  assert.match(rows.get('normal-id').querySelector('.run-item-meta').textContent, /agentrec.*claude.*ago/);
  assert.equal(rows.get('fallback-id').querySelector('.run-title-text').textContent, 'fallback-id');
  assert.equal(rows.get('normal-id').querySelector('.run-verdict-run').classList.contains('pass'), false);
  assert.equal(rows.get('normal-id').querySelector('.run-verdict-verify').classList.contains('pass'), false);
  assert.equal(rows.get('failure-id').querySelector('.run-verdict-run').classList.contains('fail'), true);
  assert.equal(rows.get('warning-id').querySelector('.run-verdict-verify').classList.contains('warn'), true);
  assert.equal(rows.get('running-id').querySelector('.run-verdict-run').classList.contains('running'), true);

  const detailCalls = () => w.__fetchPaths.filter((path) => /^\/api\/runs\/[^/]+$/.test(path)).length;
  assert.equal(detailCalls(), 1, 'only the selected run detail is loaded');
  const search = d.querySelector('#run-search');
  search.value = 'broken build';
  search.dispatchEvent(new w.Event('input', { bubbles: true }));
  assert.deepEqual(runIDs(d), ['failure-id']);
  assert.equal(detailCalls(), 1, 'title filtering uses /api/runs summaries');
});

test('main heading uses only the loaded safe summary title and keeps the full run ID visible', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs[0].title = 'Review the release notes';
  data.details.run.title = 'detail-only title must not be projected';
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;

  assert.equal(document.querySelector('#run-title').textContent, 'Review the release notes');
  assert.equal(document.querySelector('#run-id').textContent, data.details.run.id);
  assert.match(document.querySelector('.run-subtitle').textContent, new RegExp(data.details.run.id));
});

test('main heading falls back to the ID for an older unloaded run', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs[0] = { ...data.list.runs[0], id: 'loaded-run', title: 'Loaded safe title' };
  data.details.run = { ...data.details.run, id: 'older-unloaded-run', title: 'detail-only title must not be projected' };
  data.configure = (w) => w.history.replaceState(null, '', '/?run=older-unloaded-run');
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;

  assert.equal(document.querySelector('#run-title').textContent, 'older-unloaded-run');
  assert.equal(document.querySelector('#run-id').textContent, 'older-unloaded-run');
});

test('polling updates the selected safe summary title without refetching terminal details', async (t) => {
  let poll;
  let detailReads = 0;
  const data = fixture('completed', 'pass', 'PASS');
  data.list.generation = 'generation';
  data.list.runs[0].title = 'Initial safe title';
  data.details = () => {
    detailReads += 1;
    return { ...fixture('completed', 'pass', 'PASS').details };
  };
  data.configure = (w) => {
    w.setInterval = (callback, delay) => { if (delay === 5000) poll = callback; return delay; };
    w.clearInterval = () => {};
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  assert.equal(document.querySelector('#run-title').textContent, 'Initial safe title');
  assert.equal(detailReads, 1);

  data.list.runs[0] = { ...data.list.runs[0], title: 'Polled safe title' };
  await poll();

  assert.equal(document.querySelector('#run-title').textContent, 'Polled safe title');
  assert.equal(detailReads, 1);
});

test('request uses a localized native disclosure, preserves full text, and resets on run change', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const first = '첫 줄의 아주 긴 요청\nSecond line stays fully inspectable.';
  const second = '次の実行の完全なリクエスト';
  const base = data.details;
  data.list.runs = [
    { ...data.list.runs[0], id: 'first-run' },
    { ...data.list.runs[0], id: 'second-run' },
  ];
  data.list.total = 2;
  data.details = (id) => ({ ...base, run: { ...base.run, id, prompt: id === 'first-run' ? first : second } });
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  const disclosure = d.querySelector('#request-disclosure');

  assert.equal(disclosure.tagName, 'DETAILS');
  assert.ok(disclosure.querySelector(':scope > summary'));
  assert.equal(disclosure.open, false);
  assert.equal(d.querySelector('#run-prompt').textContent, first);
  assert.match(d.querySelector('#request-preview').textContent, /첫 줄의 아주 긴 요청/);

  disclosure.open = true;
  for (const [lang, request, show, hide] of [
    ['en', 'Request', 'Show request', 'Hide request'],
    ['ko', '요청', '요청 보기', '요청 접기'],
    ['ja', 'リクエスト', 'リクエストを表示', 'リクエストを隠す'],
    ['zh-CN', '请求', '显示请求', '隐藏请求'],
  ]) {
    d.querySelector('#lang').value = lang;
    d.querySelector('#lang').dispatchEvent(new w.Event('change', { bubbles: true }));
    assert.equal(disclosure.open, true, `${lang} keeps disclosure state`);
    assert.equal(disclosure.querySelector('.section-label').textContent, request);
    assert.equal(d.querySelector('#request-expand-label').textContent, show);
    assert.equal(d.querySelector('#request-collapse-label').textContent, hide);
    assert.equal(d.querySelector('#run-prompt').textContent, first);
  }

  d.querySelector('[data-run-id="second-run"]').click();
  await settle();
  assert.equal(disclosure.open, false);
  assert.equal(d.querySelector('#run-prompt').textContent, second);
  assert.equal(d.querySelector('#request-preview').textContent, second);
});

test('request preview bounds Unicode text without shortening full evidence', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const prompt = '🧪'.repeat(200) + '\nFull evidence remains here.';
  data.details.run.prompt = prompt;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;
  assert.equal(d.querySelector('#request-preview').textContent, '🧪'.repeat(160) + '…');
  assert.equal(d.querySelector('#run-prompt').textContent, prompt);
});

test('advanced run filters start collapsed, report applied count, and retain hidden values', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs[0].failure = true;
  data.configure = (w) => w.history.replaceState(null, '', '/?exit=completed&verification=PASS&failures=1');
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;
  const advanced = d.querySelector('#run-advanced-filters');

  assert.ok(advanced);
  assert.equal(advanced.open, false);
  assert.equal(d.querySelector('#run-advanced-count').textContent, '3 filters applied');
  assert.equal(advanced.contains(d.querySelector('#run-search')), false);
  assert.equal(advanced.contains(d.querySelector('#run-project-filter')), false);
  advanced.open = true;
  advanced.open = false;
  assert.equal(d.querySelector('#run-exit-filter').value, 'completed');
  assert.equal(d.querySelector('#run-verification-filter').value, 'PASS');
  assert.equal(d.querySelector('#run-failures-only').checked, true);
  assert.deepEqual(runIDs(d), [data.list.runs[0].id]);
  for (const [lang, label, count] of [
    ['en', 'Advanced filters', '3 filters applied'],
    ['ko', '고급 필터', '필터 3개 적용'],
    ['ja', '詳細フィルター', 'フィルター適用 3 件'],
    ['zh-CN', '高级筛选', '已应用 3 个筛选条件'],
  ]) {
    d.querySelector('#lang').value = lang;
    d.querySelector('#lang').dispatchEvent(new dom.window.Event('change'));
    assert.equal(advanced.querySelector('summary span').textContent, label);
    assert.equal(d.querySelector('#run-advanced-count').textContent, count);
  }
});

test('failure triage separates failures and navigates to existing evidence', async (t) => {
  const data = fixture('nonzero', 'fail', 'nonzero');
  data.details.changes = { status: 'available', total: 2, tracked: 1, untracked: 1, additions: 3, deletions: 1, binary: 0 };
  data.details.evidence.supervisor = [{ name: 'Exit Code', value: '1' }];
  data.details.evidence.repository = [{ name: 'Attribution', value: 'repository_observed' }];
  data.details.evidence.verification = [
    { name: 'Status', value: 'FAIL' },
    { name: 'Check', value: 'PASS unit  go test ./...' },
    { name: 'Check', value: 'FAIL integration  go test ./integration  exit 1' },
    { name: 'Warning', value: 'verification mutated repository' },
  ];

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  const triage = document.querySelector('#failure-triage');
  assert.equal(triage.classList.contains('hidden'), false);
  assert.match(triage.textContent, /Run NONZERO/);
  assert.match(triage.textContent, /Verify FAIL/);
  assert.match(triage.textContent, /FAIL integration/);
  assert.match(triage.textContent, /verification mutated repository/);
  assert.doesNotMatch(triage.textContent, /PASS unit/);
  assert.match(triage.textContent, /not proof.*caused/i);

  document.querySelector('#triage-changes').click();
  for (let i = 0; i < 3; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.querySelector('#timeline-tab-changes').getAttribute('aria-selected'), 'true');

  document.querySelector('#triage-verification').click();
  assert.equal(document.activeElement.id, 'evidence-verification');
  const verificationLabel = document.activeElement.getAttribute('aria-labelledby');
  assert.equal(document.getElementById(verificationLabel).textContent, 'Verification');

  const cleanDom = await renderFixture(fixture('completed', 'pass', 'PASS'));
  t.after(() => cleanDom.window.close());
  assert.equal(cleanDom.window.document.querySelector('#failure-triage').classList.contains('hidden'), true);
});

test('pending verification does not reveal failure triage', async (t) => {
  const data = fixture('running', '', 'PENDING');
  data.details.evidence.verification = [
    { name: 'Status', value: 'PENDING' },
    { name: 'Check', value: 'PENDING integration  go test ./integration' },
  ];

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  assert.equal(dom.window.document.querySelector('#failure-triage').classList.contains('hidden'), true);
});

test('failure triage localizes verification check verdicts', async (t) => {
  const data = fixture('completed', 'fail', 'FAIL');
  data.details.evidence.verification = [
    { name: 'Status', value: 'FAIL' },
    { name: 'Check', value: 'FAIL integration  go test ./integration  exit 1' },
  ];

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, Event } = dom.window;
  const language = document.querySelector('#lang');
  language.value = 'ko';
  language.dispatchEvent(new Event('change', { bubbles: true }));

  const check = document.querySelector('#failure-triage-facts code');
  assert.match(check.textContent, /^실패 integration/);
  assert.equal(check.title, 'FAIL integration  go test ./integration  exit 1');
});

test('live failure transition updates a persistent triage status region', async (t) => {
  let liveTick;
  const data = fixture('running', '', 'PENDING');
  let details = data.details;
  details.evidence.verification = [{ name: 'Status', value: 'PENDING' }];
  data.details = () => details;
  data.configure = (window) => {
    window.setTimeout = (callback, delay) => {
      if (delay === 3000) liveTick = callback;
      return delay;
    };
    window.clearTimeout = () => {};
  };

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  const status = document.querySelector('#failure-triage-status');
  assert.ok(status);
  assert.equal(status.getAttribute('role'), 'status');
  assert.equal(status.getAttribute('aria-live'), 'polite');
  assert.equal(status.textContent, '');
  assert.equal(typeof liveTick, 'function');

  details = {
    ...details,
    run: { ...details.run, exitReason: 'nonzero', statusClass: 'fail', statusLabel: 'nonzero' },
    evidence: {
      ...details.evidence,
      verification: [
        { name: 'Status', value: 'FAIL' },
        { name: 'Check', value: 'FAIL integration  go test ./integration  exit 1' },
      ],
    },
  };
  await liveTick();

  assert.match(status.textContent, /Run NONZERO/);
  assert.match(status.textContent, /Verify FAIL/);
});

function recentRunsFixture() {
  const data = fixture('completed', 'pass', 'PASS');
  const base = data.list.runs[0], detail = data.details;
  // API order is newest first; cutoff must be matching rows, never a wall clock.
  data.list.runs = Array.from({ length: 29 }, (_, i) => ({ ...base, id: `run-${i}`, project: i % 2 ? 'dogfoodlab' : 'agentrec' }));
  data.list.total = 29;
  data.list.generation = 'same';
  data.details = (id) => ({ ...detail, run: { ...detail.run, id } });
  return data;
}

const runIDs = (d) => Array.from(d.querySelectorAll('#run-list .run-item'), (r) => r.dataset.runId);

test('empty search and unreadable loaded page do not claim the whole store is empty', async (t) => {
  for (const emptyPage of [false, true]) {
    const data = recentRunsFixture();
    data.list.total = 55;
    data.list.nextCursor = 'more';
    if (emptyPage) data.list.runs = [];
    data.configure = (w) => w.history.replaceState(null, '', '/?q=absent');
    const dom = await renderFixture(data);
    t.after(() => dom.window.close());
    const d = dom.window.document;
    assert.equal(d.querySelector('#run-list-empty').textContent, emptyPage ? 'No readable runs loaded yet. Load more to continue.' : 'No loaded runs match this search.');
    if (emptyPage) assert.notEqual(d.querySelector('#workspace-empty-title').textContent, 'No runs recorded yet');
    assert.equal(d.querySelector('#run-load-more').classList.contains('hidden'), false);
    assert.equal(d.querySelector('#run-earlier-toggle').classList.contains('hidden'), true);
  }
});

test('unreadable final page refreshes empty copy and hides exhausted load-more with unchanged runs', async (t) => {
  const data = recentRunsFixture();
  data.list.runs = [];
  data.list.total = 55;
  data.list.nextCursor = 'page-two';
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  const more = d.querySelector('#run-load-more');
  assert.equal(more.classList.contains('hidden'), false);
  assert.equal(d.querySelector('#run-list-empty').textContent, 'No readable runs loaded yet. Load more to continue.');
  const fetch = w.fetch;
  let requests = 0;
  w.fetch = (input, init) => {
    if (String(input).includes('/api/runs?cursor=')) {
      requests += 1;
      assert.equal(String(input), '/api/runs?cursor=page-two');
      return response({ runs: [], total: 55, nextCursor: '', generation: 'same', unreadable: 55 });
    }
    return fetch(input, init);
  };
  more.click();
  await settle();
  assert.equal(requests, 1);
  assert.equal(more.classList.contains('hidden'), true);
  assert.equal(more.disabled, false);
  assert.deepEqual(runIDs(d), []);
  assert.equal(d.querySelector('#run-list-empty').textContent, 'No readable runs loaded.');
  assert.equal(d.querySelector('#run-list-status').textContent, 'No readable runs loaded.');
  assert.equal(d.querySelector('#workspace-empty-title').textContent, 'No run selected');
  assert.equal(d.querySelector('#unreadable-warning').textContent, '55 unreadable run(s) were excluded.');
  assert.equal(d.querySelector('#unreadable-warning').classList.contains('hidden'), false);
});

test('project and recent-run controls explain loaded scope in all four locales', async (t) => {
  const dom = await renderFixture(recentRunsFixture());
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  const scopeDetails = d.querySelector('.run-scope-help');
  assert.equal(scopeDetails.tagName, 'DETAILS');
  assert.equal(scopeDetails.open, false);
  for (const [lang, project, all, show, hide, scopeLabel, scope, count] of [
    ['en', 'Filter by project', 'All projects', 'Show 19 earlier runs', 'Hide earlier runs', 'Loaded-run scope', 'Filters and project choices cover loaded runs only. By default, newest 10 matches shown; selected older match stays visible.', '10 shown · 29 matching · 29 loaded · 19 folded'],
    ['ko', '프로젝트로 필터링', '모든 프로젝트', '이전 실행 19개 보기', '이전 실행 숨기기', '로드된 실행 범위', '필터와 프로젝트 목록은 로드된 실행만 포함합니다. 기본적으로 일치하는 최신 10개와 선택된 이전 실행을 표시합니다.', '표시 10개 · 일치 29개 · 로드 29개 · 접힘 19개'],
    ['ja', 'プロジェクトで絞り込む', 'すべてのプロジェクト', '以前の実行を19件表示', '以前の実行を隠す', '読み込み済みの範囲', 'フィルターとプロジェクト候補は読み込み済みの実行のみが対象です。既定では、一致する最新10件と選択中の以前の実行を表示します。', '表示10件 · 一致29件 · 読み込み済み29件 · 折りたたみ19件'],
    ['zh-CN', '按项目筛选', '所有项目', '显示19个较早运行', '隐藏较早运行', '已加载运行范围', '筛选和项目选项仅涵盖已加载的运行。默认显示最新的10个匹配项，并保留选中的较早运行。', '显示10个 · 匹配29个 · 已加载29个 · 已折叠19个'],
  ]) {
    d.querySelector('#lang').value = lang;
    d.querySelector('#lang').dispatchEvent(new w.Event('change'));
    const select = d.querySelector('#run-project-filter');
    assert.equal(select.getAttribute('aria-label'), project, lang);
    assert.equal(select.options[0].textContent, all, lang);
    assert.equal(select.getAttribute('aria-describedby'), 'run-list-scope');
    assert.equal(scopeDetails.querySelector('summary').textContent, scopeLabel, lang);
    assert.equal(d.querySelector('#run-list-scope').textContent, scope, lang);
    assert.equal(d.querySelector('#run-count').textContent, count, lang);
    const toggle = d.querySelector('#run-earlier-toggle');
    assert.equal(toggle.textContent, show, lang);
    toggle.click();
    assert.equal(toggle.textContent, hide, lang);
    assert.equal(d.querySelector('#run-list-scope').textContent, scope, `${lang} expanded default explanation`);
    toggle.click();
  }
});

test('project dropdown spans its row while counts and scope help stay quiet', () => {
  assert.match(css, /#run-project-filter\s*\{[^}]*grid-column:\s*1 \/ -1/);
  assert.match(css, /\.sidebar-head\s*\{[^}]*flex-wrap:\s*wrap/);
  assert.match(css, /#run-count\s*\{[^}]*color:\s*var\(--quiet\)/);
  assert.doesNotMatch(css, /#run-count\s*\{[^}]*border/);
  assert.match(css, /\.run-scope-help\s*\{[^}]*font-size:\s*11px/);
});

test('polish layout keeps row contents uncompressed and request disclosure inline', () => {
  assert.match(css, /\.run-list\s*\{[^}]*grid-auto-rows:\s*max-content/);
  assert.match(css, /\.request-card summary\s*\{[^}]*display:\s*flex/);
  assert.match(css, /#run-count\s*\{[^}]*word-break:\s*keep-all/);
  assert.match(css, /\.metrics\s*\{[^}]*repeat\(4,minmax\(0,1fr\)\)/);
});

// JSDOM has no layout engine or responsive media evaluation. Flatten only the
// applicable width rules to test the cascade, not pixel geometry (browser QA owns that).
function responsiveFixture(markup, width) {
  const dom = new JSDOM(markup);
  const style = dom.window.document.createElement('style');
  style.textContent = css;
  dom.window.document.head.append(style);
  const flatten = (rules) => Array.from(rules).flatMap((rule) => {
    if (rule.selectorText) return [rule.cssText];
    if (!rule.cssRules) return [];
    const maxWidth = /^\(max-width: (\d+)px\)$/.exec(rule.conditionText);
    return maxWidth && width <= Number(maxWidth[1]) ? flatten(rule.cssRules) : [];
  }).join('\n');
  style.textContent = flatten(style.sheet.cssRules);
  return dom;
}

test('responsive fix: conversation keeps two tracks with intrinsic timestamp width', (t) => {
  for (const width of [320, 375, 720]) {
    const dom = responsiveFixture('<div class="action-row conversation-row"><div class="action-time">23:59:59</div><div class="speech-body">Recorded speech</div></div>', width);
    t.after(() => dom.window.close());
    const row = dom.window.document.querySelector('.conversation-row');
    assert.equal(dom.window.getComputedStyle(row).gridTemplateColumns, 'max-content minmax(0,1fr)', `two children must not inherit the action rail at ${width}px`);
    assert.equal(dom.window.getComputedStyle(row.firstElementChild).whiteSpace, 'nowrap');
    assert.equal(dom.window.getComputedStyle(row.lastElementChild).minWidth, '0px');
  }
});

test('responsive fix: mobile tabs stack full wrapping labels and counts without a fixed height', (t) => {
  const dom = responsiveFixture(html, 375);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  const style = (element) => dom.window.getComputedStyle(element);
  assert.equal(style(document.querySelector('.tabs')).height, 'auto');
  for (const tab of document.querySelectorAll('.tab')) {
    assert.equal(style(tab).display, 'flex');
    assert.equal(style(tab).flexDirection, 'column');
    assert.equal(style(tab).whiteSpace, 'normal');
    for (const span of tab.children) {
      assert.equal(style(span).overflowWrap, 'anywhere');
      assert.equal(style(span).wordBreak, 'keep-all');
      assert.notEqual(style(span).display, 'none');
      assert.notEqual(style(span).textOverflow, 'ellipsis');
    }
  }
});

test('responsive fix: empty navigation collapses while populated rows retain their scroll area', (t) => {
  for (const width of [375, 900]) {
    const dom = responsiveFixture('<nav class="run-list"></nav>', width);
    t.after(() => dom.window.close());
    const nav = dom.window.document.querySelector('nav');
    assert.equal(dom.window.getComputedStyle(nav).minHeight, '0px', `empty navigation at ${width}px`);
    assert.equal(dom.window.getComputedStyle(nav).height, 'auto');
    nav.innerHTML = '<button class="run-item">Recorded run</button>';
    assert.equal(dom.window.getComputedStyle(nav).minHeight, '144px');
    assert.equal(dom.window.getComputedStyle(nav).height, `${Math.max(144, Math.min(dom.window.innerHeight * 0.32, 320))}px`);
    assert.equal(dom.window.getComputedStyle(nav).gridAutoRows, 'max-content');
  }
});

test('responsive fix: unresolved advanced filter count has no English loading placeholder', (t) => {
  const dom = new JSDOM(html);
  t.after(() => dom.window.close());
  assert.equal(dom.window.document.querySelector('#run-advanced-count').textContent, '');
});

test('responsive CSS keeps navigation reachable and reflows dense controls', () => {
  const tablet = css.slice(css.indexOf('@media (max-width: 1023px)'), css.indexOf('@media (max-width: 720px)'));
  const mobile = css.slice(css.indexOf('@media (max-width: 720px)'), css.indexOf('@media (prefers-reduced-motion: reduce)'));

  assert.doesNotMatch(tablet, /max-height:\s*280px/);
  assert.doesNotMatch(tablet, /overflow-x:\s*hidden/);
  assert.match(tablet, /\.run-list\s*\{[^}]*min-height:\s*144px[^}]*height:/);
  assert.match(mobile, /\.topbar\s*\{[^}]*grid-template-areas:\s*"brand controls"\s*"search search"/);
  assert.match(mobile, /\.global-search\s*\{[^}]*grid-area:\s*search[^}]*max-width:\s*none/);
  assert.match(mobile, /\.run-header-side\s*\{[^}]*align-items:\s*flex-start/);
  assert.match(mobile, /\.evidence-fields\s*\{[^}]*grid-template-columns:\s*1fr/);
  assert.match(css, /\.metrics\s*\{[^}]*border:\s*1px solid var\(--border\)[^}]*background:\s*var\(--panel\)/);
  assert.match(css, /\.metric\s*\{[^}]*border:\s*0/);
});

test('new-run polling moves focused cutoff row to earlier toggle without expanding the list', async (t) => {
  let poll;
  const data = recentRunsFixture();
  data.configure = (w) => {
    w.setInterval = (fn, delay) => { if (delay === 5000) poll = fn; return delay; };
    w.clearInterval = () => {};
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;
  const toggle = d.querySelector('#run-earlier-toggle');
  assert.equal(d.querySelector('#run-title').textContent, 'run-0');
  d.querySelector('[data-run-id="run-9"]').focus();
  data.list.runs.unshift({ ...data.list.runs[0], id: 'new-run' });
  data.list.total = 30;
  await poll();
  assert.equal(d.activeElement, toggle, 'newly folded unselected row hands focus to the stable toggle');
  assert.equal(toggle.classList.contains('hidden'), false);
  assert.equal(toggle.getAttribute('aria-expanded'), 'false');
  assert.equal(toggle.textContent, 'Show 20 earlier runs');
  assert.deepEqual(runIDs(d), ['new-run', ...Array.from({ length: 9 }, (_, i) => `run-${i}`)]);
  assert.equal(d.querySelector('#run-count').textContent, '10 shown · 30 matching · 30 loaded · 20 folded');
  assert.equal(d.querySelector('#run-title').textContent, 'run-0');
  d.querySelector('[data-run-id="run-5"]').focus();
  data.list.runs.unshift({ ...data.list.runs[0], id: 'newer-run' });
  data.list.total = 31;
  await poll();
  assert.equal(d.activeElement.dataset.runId, 'run-5', 'still-visible focus is restored rather than sent to toggle');
  assert.deepEqual(runIDs(d), ['newer-run', 'new-run', ...Array.from({ length: 8 }, (_, i) => `run-${i}`)]);
  assert.equal(d.querySelector('#run-count').textContent, '10 shown · 31 matching · 31 loaded · 21 folded');
});

test('fold expansion survives same-generation polling and load-more, resets on history filter changes', async (t) => {
  let poll;
  const data = recentRunsFixture();
  data.list.total = 40;
  data.list.nextCursor = 'page-two';
  data.configure = (w) => {
    w.setInterval = (fn, delay) => { if (delay === 5000) poll = fn; return delay; };
    w.clearInterval = () => {};
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  const toggle = d.querySelector('#run-earlier-toggle');
  const more = d.querySelector('#run-load-more');
  assert.equal(more.classList.contains('hidden'), false);
  assert.equal(toggle.contains(more), false);
  toggle.click();
  const row = d.querySelector('[data-run-id="run-20"]');
  row.focus();
  await poll();
  assert.equal(d.activeElement, row, 'unchanged polling preserves DOM/focus');
  data.list.runs[20].warningCount = 1;
  await poll();
  assert.equal(d.activeElement.dataset.runId, 'run-20', 'changed same-generation polling restores row focus');
  assert.equal(toggle.getAttribute('aria-expanded'), 'true');
  const fetch = w.fetch;
  w.fetch = (input, init) => String(input).includes('/api/runs?cursor=')
    ? response({ runs: [{ ...data.list.runs[28], id: 'run-29', project: 'new-project' }], total: 40, nextCursor: 'page-three', generation: 'same' })
    : fetch(input, init);
  more.click();
  await settle();
  assert.equal(runIDs(d).length, 30);
  assert.equal(d.querySelector('#run-count').textContent, '30 shown · 30 matching · 30 loaded · 0 folded');
  assert.ok(Array.from(d.querySelector('#run-project-filter').options).some((o) => o.value === 'new-project'));
  await poll();
  assert.equal(runIDs(d).length, 30, 'first-page poll retains appended rows');
  toggle.click();
  assert.equal(toggle.textContent, 'Show 20 earlier runs');
  assert.equal(more.classList.contains('hidden'), false);
  toggle.click();
  w.localStorage.setItem('agentrec.project', 'wrong');
  w.history.pushState(null, '', '/?project=dogfoodlab&run=run-0#kept');
  w.dispatchEvent(new w.PopStateEvent('popstate'));
  await settle();
  assert.equal(toggle.getAttribute('aria-expanded'), 'false', 'history filter change resets fold');
  assert.equal(runIDs(d).length, 10);
  assert.equal(d.querySelector('#run-title').textContent, 'run-0');
});

test('fold cutoff uses matching projects and changing any filter resets expansion without changing open run', async (t) => {
  const data = recentRunsFixture();
  data.configure = (w) => w.history.replaceState(null, '', '/?project=dogfoodlab');
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  assert.deepEqual(runIDs(d), Array.from({ length: 10 }, (_, i) => `run-${i * 2 + 1}`));
  assert.equal(d.querySelector('#run-count').textContent, '10 shown · 14 matching · 29 loaded · 4 folded');
  for (const [id, value, event] of [['run-project-filter', '', 'change'], ['run-search', 'run', 'input'], ['run-exit-filter', 'completed', 'change'], ['run-verification-filter', 'PASS', 'change'], ['run-failures-only', true, 'change']]) {
    const toggle = d.querySelector('#run-earlier-toggle');
    toggle.click();
    assert.equal(toggle.getAttribute('aria-expanded'), 'true');
    const filter = d.getElementById(id);
    if (id === 'run-failures-only') filter.checked = value; else filter.value = value;
    filter.dispatchEvent(new w.Event(event));
    assert.equal(toggle.getAttribute('aria-expanded'), 'false', id);
    assert.equal(d.querySelector('#run-title').textContent, 'run-1', id);
  }
});

test('selected matching older run remains reachable exactly once while other earlier runs stay folded', async (t) => {
  const data = recentRunsFixture();
  data.configure = (w) => w.history.replaceState(null, '', '/?project=agentrec&run=run-28&focus=verification#kept');
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  assert.deepEqual(runIDs(d), [...Array.from({ length: 10 }, (_, i) => `run-${i * 2}`), 'run-28']);
  assert.equal(d.querySelector('#run-count').textContent, '11 shown · 15 matching · 29 loaded · 4 folded');
  assert.equal(d.querySelector('#run-earlier-toggle').textContent, 'Show 4 earlier runs');
  assert.equal(d.querySelector('[data-run-id="run-28"]').getAttribute('aria-current'), 'true');
  assert.equal(d.querySelector('[data-run-id="run-28"]').tabIndex, 0);
  d.querySelector('#run-earlier-toggle').click();
  assert.equal(runIDs(d).length, 15);
  assert.equal(new Set(runIDs(d)).size, 15);
  d.querySelector('#run-earlier-toggle').click();
  assert.equal(runIDs(d).length, 11);
  d.querySelector('#run-project-filter').value = 'dogfoodlab';
  d.querySelector('#run-project-filter').dispatchEvent(new w.Event('change'));
  assert.equal(runIDs(d).includes('run-28'), false);
  assert.equal(d.querySelector('#run-title').textContent, 'run-28');
  assert.equal(new URLSearchParams(w.location.search).get('focus'), 'verification');
  assert.equal(w.location.hash, '#kept');
});

test('recent runs show ten of 29 loaded with an accessible earlier-run toggle and honest count', async (t) => {
  const dom = await renderFixture(recentRunsFixture());
  t.after(() => dom.window.close());
  const d = dom.window.document;
  assert.deepEqual(runIDs(d), Array.from({ length: 10 }, (_, i) => `run-${i}`));
  assert.equal(d.querySelector('#run-count').textContent, '10 shown · 29 matching · 29 loaded · 19 folded');
  const toggle = d.querySelector('#run-earlier-toggle');
  assert.equal(toggle.tagName, 'BUTTON');
  assert.equal(toggle.type, 'button');
  assert.equal(toggle.getAttribute('aria-controls'), 'run-list');
  assert.equal(toggle.getAttribute('aria-expanded'), 'false');
  assert.equal(toggle.textContent, 'Show 19 earlier runs');
  toggle.focus();
  toggle.click(); // Native button activation (Enter/Space behavior is provided by the browser).
  assert.equal(d.activeElement, toggle);
  assert.equal(toggle.getAttribute('aria-expanded'), 'true');
  assert.equal(toggle.textContent, 'Hide earlier runs');
  assert.equal(runIDs(d).length, 29);
  assert.equal(d.querySelector('#run-count').textContent, '29 shown · 29 matching · 29 loaded · 0 folded');
  toggle.click();
  assert.equal(runIDs(d).length, 10);
});

test('project changes remember exact choice including All without disturbing evidence URLs or inspector', async (t) => {
  const data = actionLinkFixture();
  data.list.runs.push({ ...data.list.runs[0], id: 'other', project: 'other project' });
  data.list.total = 2;
  data.configure = (w) => w.history.replaceState(null, '', `/?run=${data.details.run.id}&focus=actions&action=action-250&actionCursor=98765&q=agentrec&exit=completed&verification=PASS&failures=1#kept`);
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  const inspector = d.querySelector('#inspector').textContent;
  const original = new URLSearchParams(w.location.search);
  for (const value of ['other project', '']) {
    const select = d.querySelector('#run-project-filter');
    select.value = value;
    select.dispatchEvent(new w.Event('change'));
    assert.equal(w.localStorage.getItem('agentrec.project'), value);
    const params = new URLSearchParams(w.location.search);
    assert.equal(params.has('project'), true);
    assert.equal(params.get('project'), value);
    for (const [key, val] of original) assert.equal(params.get(key), val, key);
    assert.equal(w.location.hash, '#kept');
    assert.equal(d.querySelector('#run-title').textContent, data.details.run.id);
    assert.equal(d.querySelector('#inspector').textContent, inspector);
    assert.ok(d.querySelector('.action-row.selected'));
  }
});

test('project storage read and write failures are harmless', async (t) => {
  for (const url of ['/', '/?project=agentrec']) {
    const data = fixture('completed', 'pass', 'PASS');
    data.configure = (w) => {
      w.history.replaceState(null, '', url);
      Object.defineProperty(w, 'localStorage', { get() { throw new Error('blocked'); } });
    };
    const dom = await renderFixture(data);
    t.after(() => dom.window.close());
    const w = dom.window, d = w.document;
    const select = d.querySelector('#run-project-filter');
    select.value = 'agentrec';
    select.dispatchEvent(new w.Event('change'));
    assert.equal(new URLSearchParams(w.location.search).get('project'), 'agentrec');
    assert.equal(d.querySelector('#run-title').textContent, data.details.run.id);
  }
});

test('project preference applies only to bare landing; explicit and legacy shared URLs win', async (t) => {
  for (const [url, expected] of [
    ['/', 'remembered'], ['/?project=', ''], ['/?project=unknown', 'unknown'],
    ['/?q=agentrec', ''], ['/?exit=completed', ''], ['/?verification=PASS', ''], ['/?failures=1', ''],
    ['/?run=outside&focus=verification', ''], ['/#compare=outside,other', ''],
    ['/?focus=changes', ''], ['/?kept=1#kept', ''],
  ]) {
    const data = fixture('completed', 'pass', 'PASS');
    const detail = data.details;
    data.list.runs.push({ ...data.list.runs[0], id: 'remembered-run', project: 'remembered' });
    data.list.total = 2;
    data.details = (id) => ({ ...detail, run: { ...detail.run, id } });
    data.configure = (w) => {
      w.localStorage.setItem('agentrec.project', 'remembered');
      w.history.replaceState(null, '', url);
    };
    const dom = await renderFixture(data);
    t.after(() => dom.window.close());
    const select = dom.window.document.querySelector('#run-project-filter');
    assert.equal(select.value, expected, url);
    if (expected === 'unknown') {
      assert.equal(select.selectedOptions[0].textContent, 'unknown');
      assert.equal(dom.window.document.querySelectorAll('#run-list .run-item').length, 0);
    }
    if (url === '/') {
      assert.equal(dom.window.document.querySelector('#run-title').textContent, 'remembered-run');
      assert.equal(new URLSearchParams(dom.window.location.search).get('project'), 'remembered', 'canonical landing URL survives reload');
    }
    if (url.includes('run=outside')) assert.equal(dom.window.document.querySelector('#run-title').textContent, 'outside');
  }
});

test('project filter matches exact loaded projects and composes with other filters', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const base = data.list.runs[0];
  data.list.runs = [
    { ...base, id: 'exact', project: 'agentrec', failure: true },
    { ...base, id: 'substring', project: 'agentrec-tools', failure: true },
    { ...base, id: 'not-failed', project: 'agentrec', failure: false },
  ];
  data.list.total = 3;
  const detail = data.details;
  data.details = (id) => ({ ...detail, run: { ...detail.run, id } });
  data.configure = (w) => w.history.replaceState(null, '', '/?project=agentrec&q=agentrec&exit=completed&verification=PASS&failures=1#kept');
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document: d } = dom.window;
  assert.ok(d.querySelector('#run-project-filter'), 'exact project dropdown exists');
  assert.deepEqual(Array.from(d.querySelector('#run-project-filter').options, (o) => o.value), ['', 'agentrec', 'agentrec-tools']);
  assert.equal(d.querySelector('#run-project-filter').value, 'agentrec');
  assert.deepEqual(Array.from(d.querySelectorAll('#run-list .run-item'), (r) => r.dataset.runId), ['exact']);
  assert.equal(d.querySelector('#run-title').textContent, 'exact');
});

test('run list filters by an exact persisted exit value', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs = [
    { ...data.list.runs[0], id: 'run-completed', exit: 'completed' },
    { ...data.list.runs[0], id: 'run-future', exit: 'provider_crash', statusClass: 'fail', statusLabel: 'provider_crash' },
  ];
  data.list.total = data.list.runs.length;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, Event } = dom.window;
  const filter = document.querySelector('#run-exit-filter');
  assert.deepEqual(Array.from(filter.options, (option) => option.value), ['', 'completed', 'provider_crash']);

  filter.value = 'provider_crash';
  filter.dispatchEvent(new Event('change', { bubbles: true }));

  assert.deepEqual(Array.from(document.querySelectorAll('.run-item'), (item) => item.dataset.runId), ['run-future']);
});

test('run list restores shareable filters from the URL', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs = [
    { ...data.list.runs[0], id: 'warning-pass', exit: 'completed', verification: 'PASS' },
    { ...data.list.runs[0], id: 'warning-fail', exit: 'completed', verification: 'FAIL' },
    { ...data.list.runs[0], id: 'other-fail', exit: 'completed', verification: 'FAIL' },
  ];
  data.list.total = data.list.runs.length;
  const details = data.details;
  data.details = (id) => ({ ...details, run: { ...details.run, id } });
  data.configure = (window) => window.history.replaceState(null, '', '/?q=warning&exit=completed&verification=FAIL#compare=warning-fail,other-fail');

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;

  assert.equal(document.querySelector('#run-search').value, 'warning');
  assert.equal(document.querySelector('#run-exit-filter').value, 'completed');
  assert.equal(document.querySelector('#run-verification-filter').value, 'FAIL');
  assert.deepEqual(Array.from(document.querySelectorAll('#run-list .run-item'), (item) => item.dataset.runId), ['warning-fail']);
  assert.equal(document.querySelector('#run-title').textContent, 'warning-fail');
  assert.equal(document.querySelector('#run-list .run-item').getAttribute('aria-current'), 'true');
  assert.equal(document.querySelector('#diff-panel').classList.contains('hidden'), false);
  assert.equal(dom.window.location.hash, '#compare=warning-fail,other-fail');
});

test('run deep link restores the selected run and verification focus', async (t) => {
  const data = fixture('completed', 'fail', 'FAIL');
  data.list.runs = [
    { ...data.list.runs[0], id: 'first-failure', verification: 'FAIL' },
    { ...data.list.runs[0], id: 'shared-failure', verification: 'FAIL' },
  ];
  data.list.total = data.list.runs.length;
  const details = data.details;
  data.details = (id) => ({ ...details, run: { ...details.run, id } });
  data.configure = (window) => window.history.replaceState(null, '', '/?exit=completed&verification=FAIL&run=shared-failure&focus=verification#kept');

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, location } = dom.window;

  assert.equal(document.querySelector('#run-title').textContent, 'shared-failure');
  assert.equal(document.querySelector('#run-list .run-item[aria-current]').dataset.runId, 'shared-failure');
  assert.equal(document.activeElement.id, 'evidence-verification');
  assert.equal(new URLSearchParams(location.search).get('run'), 'shared-failure');
  assert.equal(new URLSearchParams(location.search).get('focus'), 'verification');
  assert.equal(location.hash, '#kept');
});

test('automatic selection canonicalizes the selected run in the current URL', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.configure = (window) => window.history.replaceState(null, '', '/?kept=1#kept');

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());

  assert.equal(new URLSearchParams(dom.window.location.search).get('run'), data.details.run.id);
  assert.equal(new URLSearchParams(dom.window.location.search).get('kept'), '1');
  assert.equal(dom.window.location.hash, '#kept');
});

test('run and evidence navigation update URL state without discarding filters or hash', async (t) => {
  const data = fixture('completed', 'fail', 'FAIL');
  data.list.runs = [
    { ...data.list.runs[0], id: 'first-failure', verification: 'FAIL' },
    { ...data.list.runs[0], id: 'shared-failure', verification: 'FAIL' },
  ];
  data.list.total = data.list.runs.length;
  const details = {
    ...data.details,
    evidence: { ...data.details.evidence, verification: [{ name: 'Status', value: 'FAIL' }] },
  };
  data.details = (id) => ({ ...details, run: { ...details.run, id } });
  data.configure = (window) => window.history.replaceState(null, '', '/?kept=1&verification=FAIL#kept');

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, location } = dom.window;

  document.querySelector('[data-run-id="shared-failure"]').click();
  for (let i = 0; i < 3; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(new URLSearchParams(location.search).get('run'), 'shared-failure');
  assert.equal(new URLSearchParams(location.search).get('verification'), 'FAIL');
  assert.equal(location.hash, '#kept');

  document.querySelector('#timeline-tab-changes').click();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(new URLSearchParams(location.search).get('focus'), 'changes');
  assert.equal(document.querySelector('#timeline-tab-changes').getAttribute('aria-selected'), 'true');

  const verificationButton = document.querySelector('#triage-verification');
  assert.equal(typeof verificationButton.onclick, 'function');
  verificationButton.click();
  assert.equal(new URLSearchParams(location.search).get('focus'), 'verification');
  assert.equal(document.activeElement.id, 'evidence-verification');
});

test('popstate restores run selection and evidence focus', async (t) => {
  const data = fixture('completed', 'fail', 'FAIL');
  data.list.runs = [
    { ...data.list.runs[0], id: 'first-failure', verification: 'FAIL' },
    { ...data.list.runs[0], id: 'shared-failure', verification: 'FAIL' },
  ];
  data.list.total = data.list.runs.length;
  const details = data.details;
  data.details = (id) => ({ ...details, run: { ...details.run, id } });
  data.configure = (window) => window.history.replaceState(null, '', '/?run=first-failure&focus=verification#kept');

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, history, PopStateEvent, location } = dom.window;

  history.pushState(null, '', '/?run=shared-failure&focus=changes#kept');
  dom.window.dispatchEvent(new PopStateEvent('popstate'));
  for (let i = 0; i < 5; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(document.querySelector('#run-title').textContent, 'shared-failure');
  assert.equal(document.querySelector('#run-list .run-item[aria-current]').dataset.runId, 'shared-failure');
  assert.equal(document.querySelector('#timeline-tab-changes').getAttribute('aria-selected'), 'true');
  assert.equal(document.activeElement.id, 'timeline-tab-changes');
  assert.equal(new URLSearchParams(location.search).get('focus'), 'changes');
  assert.equal(location.hash, '#kept');
});

test('popstate invalidates an in-flight run load even when the displayed run matches the URL', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const first = data.details;
  const secondID = 'slow-second';
  let resolveSecond;
  data.list.runs = [data.list.runs[0], { ...data.list.runs[0], id: secondID }];
  data.list.total = data.list.runs.length;
  data.details = (id) => id === secondID
    ? new Promise((resolve) => { resolveSecond = resolve; })
    : ({ ...first, run: { ...first.run, id } });

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const firstID = first.run.id;
  dom.window.document.querySelector(`[data-run-id="${secondID}"]`).click();
  await new Promise((resolve) => setTimeout(resolve, 0));
  dom.window.history.replaceState(null, '', `/?run=${firstID}`);
  dom.window.dispatchEvent(new dom.window.PopStateEvent('popstate'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  resolveSecond({ ...first, run: { ...first.run, id: secondID } });
  for (let i = 0; i < 5; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(new URLSearchParams(dom.window.location.search).get('run'), firstID);
  assert.equal(dom.window.document.querySelector('#run-title').textContent, firstID);
  assert.equal(dom.window.document.querySelector('[aria-current="true"]').dataset.runId, firstID);
});

test('popstate without focus restores the default Actions tab', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.configure = (window) => window.history.replaceState(null, '', `/?run=${data.details.run.id}`);
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());

  dom.window.document.querySelector('#timeline-tab-changes').click();
  dom.window.history.replaceState(null, '', `/?run=${data.details.run.id}`);
  dom.window.dispatchEvent(new dom.window.PopStateEvent('popstate'));
  for (let i = 0; i < 3; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(new URLSearchParams(dom.window.location.search).has('focus'), false);
  assert.equal(dom.window.document.querySelector('#timeline-tab-actions').getAttribute('aria-selected'), 'true');
  assert.equal(dom.window.document.activeElement.id, 'timeline-tab-actions');
});

test('popstate to a missing run clears stale evidence', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const details = data.details;
  data.details = (id) => id === 'missing-run' ? null : ({ ...details, run: { ...details.run, id } });
  data.configure = (window) => window.history.replaceState(null, '', '/?run=first-run');

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, history, PopStateEvent } = dom.window;
  assert.equal(document.querySelector('#run-title').textContent, 'first-run');

  history.pushState(null, '', '/?run=missing-run&focus=verification');
  dom.window.dispatchEvent(new PopStateEvent('popstate'));
  for (let i = 0; i < 5; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(document.querySelector('#run-view').classList.contains('hidden'), true);
  assert.equal(document.querySelector('#workspace-empty-title').textContent, 'Could not load selected run');
  assert.equal(document.querySelector('#run-list .run-item[aria-current]'), null);
});

test('failed sidebar navigation clears stale evidence and records the requested run', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const details = data.details;
  const missing = 'missing-after-list';
  let unavailable = false;
  data.list.runs = [data.list.runs[0], { ...data.list.runs[0], id: missing }];
  data.list.total = data.list.runs.length;
  data.details = (id) => unavailable && id === missing ? null : ({ ...details, run: { ...details.run, id } });

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  unavailable = true;
  dom.window.document.querySelector(`[data-run-id="${missing}"]`).click();
  for (let i = 0; i < 5; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(new URLSearchParams(dom.window.location.search).get('run'), missing);
  assert.equal(dom.window.document.querySelector('#run-view').classList.contains('hidden'), true);
  assert.equal(dom.window.document.querySelector('#workspace-empty-title').textContent, 'Could not load selected run');
  assert.equal(dom.window.document.activeElement.id, 'workspace-empty');
});

test('missing run deep link stays unselected and reports the selected run failure', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.details = () => null;
  data.configure = (window) => window.history.replaceState(null, '', '/?run=missing-run&focus=changes#kept');

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, location } = dom.window;

  assert.equal(document.querySelector('#run-view').classList.contains('hidden'), true);
  assert.equal(document.querySelector('#workspace-empty-title').textContent, 'Could not load selected run');
  assert.equal(document.querySelector('#run-list .run-item[aria-current]'), null);
  assert.equal(document.activeElement.id, 'workspace-empty');
  assert.equal(new URLSearchParams(location.search).get('run'), 'missing-run');
  assert.equal(location.hash, '#kept');
});

test('missing run error survives polling and language changes', async (t) => {
  let poll;
  const data = fixture('completed', 'pass', 'PASS');
  data.details = () => null;
  data.configure = (window) => {
    window.history.replaceState(null, '', '/?run=missing-run');
    window.setInterval = (callback, delay) => {
      if (delay === 5000) poll = callback;
      return delay;
    };
    window.clearInterval = () => {};
  };

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  assert.ok(poll);
  await poll();
  const language = dom.window.document.querySelector('#lang');
  language.value = 'ko';
  language.dispatchEvent(new dom.window.Event('change', { bubbles: true }));

  assert.equal(dom.window.document.querySelector('#run-view').classList.contains('hidden'), true);
  assert.equal(dom.window.document.querySelector('#workspace-empty-title').textContent, '선택한 실행을 불러오지 못했습니다');
  assert.equal(new URLSearchParams(dom.window.location.search).get('run'), 'missing-run');
});

test('comparison startup canonicalizes its primary run', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const details = data.details;
  data.list.runs = [
    { ...data.list.runs[0], id: 'selected' },
    { ...data.list.runs[0], id: 'compare-a' },
    { ...data.list.runs[0], id: 'compare-b' },
  ];
  data.list.total = data.list.runs.length;
  data.details = (id) => ({ ...details, run: { ...details.run, id } });
  data.configure = (window) => window.history.replaceState(null, '', '/?run=selected#compare=compare-a,compare-b');

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());

  assert.equal(dom.window.document.querySelector('#run-title').textContent, 'compare-a');
  assert.equal(new URLSearchParams(dom.window.location.search).get('run'), 'compare-a');
  assert.equal(dom.window.location.hash, '#compare=compare-a,compare-b');
  assert.equal(dom.window.document.querySelector('#diff-panel').classList.contains('hidden'), false);
});

test('comparison startup does not override a missing linked run', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const details = data.details;
  data.details = (id) => id === 'missing' ? null : ({ ...details, run: { ...details.run, id } });
  data.configure = (window) => window.history.replaceState(null, '', '/?run=missing#compare=compare-a,compare-b');

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());

  assert.equal(dom.window.document.querySelector('#run-view').classList.contains('hidden'), true);
  assert.equal(dom.window.document.querySelector('#workspace-empty-title').textContent, 'Could not load selected run');
  assert.equal(new URLSearchParams(dom.window.location.search).get('run'), 'missing');
});

test('deleting the final run clears its deep-link state', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.initialRunId = data.details.run.id;
  data.configure = (window) => window.history.replaceState(null, '', `/?kept=1&run=${data.details.run.id}&focus=changes#kept`);
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());

  dom.window.document.querySelector('#delete-run').click();
  dom.window.document.querySelector('#run-actions .danger-button').click();
  for (let i = 0; i < 5; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));

  const params = new URLSearchParams(dom.window.location.search);
  assert.equal(params.has('run'), false);
  assert.equal(params.has('focus'), false);
  assert.equal(params.get('kept'), '1');
  assert.equal(dom.window.location.hash, '#kept');

  dom.window.dispatchEvent(new dom.window.PopStateEvent('popstate'));
  for (let i = 0; i < 5; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(dom.window.document.querySelector('#run-view').classList.contains('hidden'), true);
});

test('run filter changes replace URL state without discarding compare state', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs = [
    { ...data.list.runs[0], id: 'warning-fail', exit: 'completed', verification: 'FAIL' },
  ];
  data.list.total = data.list.runs.length;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, Event, history, location } = dom.window;
  history.replaceState(null, '', '/?kept=1#compare=a,b');
  const initialLength = history.length;

  const search = document.querySelector('#run-search');
  search.value = 'warning / ? & # 한글';
  search.dispatchEvent(new Event('input', { bubbles: true }));
  const exit = document.querySelector('#run-exit-filter');
  exit.value = 'completed';
  exit.dispatchEvent(new Event('change', { bubbles: true }));
  const verification = document.querySelector('#run-verification-filter');
  verification.value = 'FAIL';
  verification.dispatchEvent(new Event('change', { bubbles: true }));

  assert.equal(new URLSearchParams(location.search).get('kept'), '1');
  assert.equal(new URLSearchParams(location.search).get('q'), 'warning / ? & # 한글');
  assert.equal(new URLSearchParams(location.search).get('exit'), 'completed');
  assert.equal(new URLSearchParams(location.search).get('verification'), 'FAIL');
  assert.equal(location.hash, '#compare=a,b');
  assert.equal(history.length, initialLength);

  const lang = document.querySelector('#lang');
  lang.value = 'ko';
  lang.dispatchEvent(new Event('change', { bubbles: true }));
  assert.equal(new URLSearchParams(location.search).get('q'), 'warning / ? & # 한글');
  assert.equal(location.hash, '#compare=a,b');

  search.value = '';
  search.dispatchEvent(new Event('input', { bubbles: true }));
  exit.value = '';
  exit.dispatchEvent(new Event('change', { bubbles: true }));
  verification.value = '';
  verification.dispatchEvent(new Event('change', { bubbles: true }));

  const cleared = new URLSearchParams(location.search);
  assert.equal(cleared.has('q'), false);
  assert.equal(cleared.has('exit'), false);
  assert.equal(cleared.has('verification'), false);
  assert.equal(cleared.get('kept'), '1');
  assert.equal(location.hash, '#compare=a,b');
  assert.equal(history.length, initialLength);
});

test('popstate restores future exact filter values without changing compare state', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs = [
    { ...data.list.runs[0], id: 'run-pass', exit: 'completed', verification: 'PASS' },
  ];
  data.list.total = data.list.runs.length;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, history, location, PopStateEvent } = dom.window;

  history.pushState(null, '', '/?q=future&exit=provider_crash&verification=REVIEW#compare=a,b');
  dom.window.dispatchEvent(new PopStateEvent('popstate'));

  assert.equal(document.querySelector('#run-search').value, 'future');
  assert.equal(document.querySelector('#run-exit-filter').value, 'provider_crash');
  assert.equal(document.querySelector('#run-verification-filter').value, 'REVIEW');
  assert.deepEqual(Array.from(document.querySelector('#run-exit-filter').options, (option) => option.value), ['', 'completed', 'provider_crash']);
  assert.deepEqual(Array.from(document.querySelector('#run-verification-filter').options, (option) => option.value), ['', 'PASS', 'REVIEW']);
  assert.equal(document.querySelectorAll('.run-item').length, 0);
  assert.equal(location.hash, '#compare=a,b');
});

test('polling refreshes filters when exact status values change', async (t) => {
  let poll;
  const data = fixture('completed', 'pass', 'PASS');
  data.list.generation = 'generation';
  data.list.runs[0].exit = 'old-exit';
  data.configure = (window) => {
    window.setInterval = (callback, delay) => {
      if (delay === 5000) poll = callback;
      return delay;
    };
    window.clearInterval = () => {};
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  assert.equal(typeof poll, 'function');
  assert.deepEqual(Array.from(document.querySelector('#run-exit-filter').options, (option) => option.value), ['', 'old-exit']);

  data.list.runs[0] = { ...data.list.runs[0], exit: 'new-exit' };
  await poll();

  assert.deepEqual(Array.from(document.querySelector('#run-exit-filter').options, (option) => option.value), ['', 'new-exit']);
});

test('polling redraws an active failure filter when only failure changes', async (t) => {
  let poll;
  const data = fixture('completed', 'pass', 'PASS');
  data.list.generation = 'generation';
  data.list.runs[0].failure = false;
  data.configure = (window) => {
    window.history.replaceState({}, '', `/?run=${data.details.run.id}&failures=1`);
    window.setInterval = (callback, delay) => {
      if (delay === 5000) poll = callback;
      return delay;
    };
    window.clearInterval = () => {};
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  assert.equal(typeof poll, 'function');
  assert.equal(document.querySelectorAll('.run-item').length, 0);

  data.list.runs[0] = { ...data.list.runs[0], failure: true };
  await poll();

  assert.deepEqual(Array.from(document.querySelectorAll('.run-item'), (item) => item.dataset.runId), [data.details.run.id]);
});

test('polling refreshes a changed run warning count', async (t) => {
  let poll;
  const data = fixture('completed', 'pass', 'PASS');
  data.list.generation = 'generation';
  data.configure = (window) => {
    window.setInterval = (callback, delay) => {
      if (delay === 5000) poll = callback;
      return delay;
    };
    window.clearInterval = () => {};
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  assert.equal(document.querySelector('.run-warning-count'), null);

  data.list.runs[0] = { ...data.list.runs[0], warningCount: 3 };
  await poll();

  assert.equal(document.querySelector('.run-warning-count').textContent.trim(), '3 warnings');
});

test('polling refreshes selected terminal run details when verification completes', async (t) => {
  let poll;
  let detailReads = 0;
  const data = fixture('session_ended', '', 'session_ended');
  data.list.generation = 'generation';
  data.list.runs[0].verification = 'PENDING';
  let details = {
    ...data.details,
    evidence: { ...data.details.evidence, verification: [{ name: 'Status', value: 'PENDING' }] },
  };
  data.details = () => {
    detailReads += 1;
    return details;
  };
  data.configure = (window) => {
    window.setInterval = (callback, delay) => {
      if (delay === 5000) poll = callback;
      return delay;
    };
    window.clearInterval = () => {};
  };

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  assert.equal(typeof poll, 'function');
  assert.match(document.querySelector('#evidence-verification').textContent, /PENDING/);
  assert.equal(detailReads, 1);
  document.querySelector('#evidence-verification').focus();
  assert.equal(document.activeElement.id, 'evidence-verification');
  await poll();
  assert.equal(detailReads, 1);

  details = {
    ...details,
    evidence: { ...details.evidence, verification: [{ name: 'Status', value: 'PASS' }] },
  };
  data.list.runs[0] = { ...data.list.runs[0], verification: 'PASS' };
  await poll();

  assert.match(document.querySelector('#evidence-verification').textContent, /PASS/);
  assert.equal(detailReads, 2);
  assert.equal(document.activeElement.id, 'evidence-verification');
});

test('run list combines exact exit and verification filters', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs = [
    { ...data.list.runs[0], id: 'run-pass', exit: 'completed', verification: 'PASS' },
    { ...data.list.runs[0], id: 'run-fail', exit: 'completed', verification: 'FAIL', statusClass: 'fail', statusLabel: 'FAIL' },
    { ...data.list.runs[0], id: 'run-nonzero', exit: 'nonzero', verification: 'FAIL', statusClass: 'fail', statusLabel: 'nonzero' },
  ];
  data.list.total = data.list.runs.length;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, Event } = dom.window;
  const exit = document.querySelector('#run-exit-filter');
  const verification = document.querySelector('#run-verification-filter');

  exit.value = 'completed';
  exit.dispatchEvent(new Event('change', { bubbles: true }));
  verification.value = 'FAIL';
  verification.dispatchEvent(new Event('change', { bubbles: true }));

  assert.deepEqual(Array.from(document.querySelectorAll('.run-item'), (item) => item.dataset.runId), ['run-fail']);
});

test('run list filters the canonical failure union and stores it in the URL', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs = [
    { ...data.list.runs[0], id: data.details.run.id, failure: false },
    { ...data.list.runs[0], id: 'run-process-failed', exit: 'nonzero', verification: 'PASS', failure: true },
    { ...data.list.runs[0], id: 'run-verification-failed', exit: 'completed', verification: 'FAIL', failure: true },
  ];
  data.list.total = data.list.runs.length;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, Event } = dom.window;
  const failures = document.querySelector('#run-failures-only');

  failures.checked = true;
  failures.dispatchEvent(new Event('change', { bubbles: true }));

  assert.deepEqual(Array.from(document.querySelectorAll('.run-item'), (item) => item.dataset.runId), [
    'run-process-failed',
    'run-verification-failed',
  ]);
  const count = document.querySelector('#run-count');
  assert.equal(count.textContent, '2 shown · 2 matching · 3 loaded · 0 folded');
  assert.equal(count.getAttribute('role'), 'status');
  assert.equal(count.getAttribute('aria-live'), 'polite');
  assert.equal(new URL(dom.window.location.href).searchParams.get('failures'), '1');
});

test('run list restores failure filtering and composes it with exact filters', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs = [
    { ...data.list.runs[0], id: data.details.run.id, failure: false },
    { ...data.list.runs[0], id: 'run-process-failed', exit: 'nonzero', verification: 'PASS', failure: true },
    { ...data.list.runs[0], id: 'run-verification-failed', exit: 'completed', verification: 'FAIL', failure: true },
  ];
  data.list.total = data.list.runs.length;
  const dom = await renderFixture({
    ...data,
    configure(window) {
      window.history.replaceState({}, '', `/?run=${data.details.run.id}&failures=1&exit=completed`);
    },
  });
  t.after(() => dom.window.close());
  const { document } = dom.window;

  assert.equal(document.querySelector('#run-failures-only').checked, true);
  assert.equal(document.querySelector('#run-exit-filter').value, 'completed');
  assert.deepEqual(Array.from(document.querySelectorAll('.run-item'), (item) => item.dataset.runId), [
    'run-verification-failed',
  ]);
  const params = new URL(dom.window.location.href).searchParams;
  assert.equal(params.get('run'), data.details.run.id);
  assert.equal(params.get('failures'), '1');
  assert.equal(params.get('exit'), 'completed');
});

test('failure-only startup selects the first matching run', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const pass = { ...data.list.runs[0], id: 'run-pass', failure: false };
  const failure = { ...pass, id: 'run-failure', exit: 'nonzero', failure: true };
  data.list.runs = [pass, failure];
  data.list.total = data.list.runs.length;
  data.list.initialRunId = pass.id;
  const details = data.details;
  data.details = (id) => ({ ...details, run: { ...details.run, id } });
  data.configure = (window) => window.history.replaceState({}, '', '/?failures=1');

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());

  assert.equal(dom.window.document.querySelector('#run-title').textContent, failure.id);
  assert.equal(new URL(dom.window.location.href).searchParams.get('run'), failure.id);
});

test('failure-only popstate selects the first matching run', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const pass = { ...data.list.runs[0], id: 'run-pass', failure: false };
  const failure = { ...pass, id: 'run-failure', exit: 'nonzero', failure: true };
  data.list.runs = [pass, failure];
  data.list.total = data.list.runs.length;
  data.list.initialRunId = pass.id;
  const details = data.details;
  data.details = (id) => ({ ...details, run: { ...details.run, id } });
  data.configure = (window) => window.history.replaceState({}, '', `/?run=${pass.id}`);

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  dom.window.history.pushState({}, '', '/?failures=1');
  dom.window.dispatchEvent(new dom.window.PopStateEvent('popstate'));
  for (let i = 0; i < 3; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(dom.window.document.querySelector('#run-failures-only').checked, true);
  assert.equal(dom.window.document.querySelector('#run-title').textContent, failure.id);
  assert.equal(new URL(dom.window.location.href).searchParams.get('run'), failure.id);
});

test('failure-only control occupies a full-width filter row', () => {
  const dom = new JSDOM(html);
  const label = dom.window.document.querySelector('#run-failures-only').closest('label');
  assert.equal(label.classList.contains('run-failure-filter'), true);
  assert.match(css, /\.run-failure-filter \{[^}]*grid-column: 1 \/ -1;[^}]*display: flex;/);
});

test('failure-only filter localizes without losing its checked state', async (t) => {
  const data = fixture('completed', 'fail', 'FAIL');
  data.list.runs[0].failure = true;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, Event } = dom.window;
  const failures = document.querySelector('#run-failures-only');
  failures.checked = true;
  failures.dispatchEvent(new Event('change', { bubbles: true }));

  const language = document.querySelector('#lang');
  language.value = 'ko';
  language.dispatchEvent(new Event('change', { bubbles: true }));

  assert.equal(document.querySelector('[data-i18n="Failures only"]').textContent, '실패만');
  assert.equal(document.querySelector('#run-count').textContent, '표시 1개 · 일치 1개 · 로드 1개 · 접힘 0개');
  assert.equal(failures.checked, true);
});

test('run list reports an empty filter result without calling it a search miss', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs = [
    { ...data.list.runs[0], id: 'run-pass', exit: 'completed', verification: 'PASS' },
    { ...data.list.runs[0], id: 'run-fail', exit: 'nonzero', verification: 'FAIL', statusClass: 'fail', statusLabel: 'nonzero' },
  ];
  data.list.total = data.list.runs.length;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, Event } = dom.window;
  const exit = document.querySelector('#run-exit-filter');
  const verification = document.querySelector('#run-verification-filter');

  exit.value = 'completed';
  exit.dispatchEvent(new Event('change', { bubbles: true }));
  verification.value = 'FAIL';
  verification.dispatchEvent(new Event('change', { bubbles: true }));

  const empty = document.querySelector('#run-list-empty');
  assert.equal(empty.textContent, 'No loaded runs match these filters.');
  const status = document.querySelector('#run-list-status');
  assert.equal(status.textContent, 'No loaded runs match these filters.');
  assert.equal(status.getAttribute('role'), 'status');
  assert.equal(status.getAttribute('aria-live'), 'polite');
});

test('run list describes an empty combined search and filter result', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs = [
    { ...data.list.runs[0], id: 'run-search-match', project: 'needle', exit: 'completed', verification: 'PASS' },
    { ...data.list.runs[0], id: 'run-filter-match', project: 'other', exit: 'nonzero', verification: 'FAIL', statusClass: 'fail', statusLabel: 'nonzero' },
  ];
  data.list.total = data.list.runs.length;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, Event } = dom.window;
  const search = document.querySelector('#run-search');
  const exit = document.querySelector('#run-exit-filter');

  search.value = 'needle';
  search.dispatchEvent(new Event('input', { bubbles: true }));
  exit.value = 'nonzero';
  exit.dispatchEvent(new Event('change', { bubbles: true }));

  assert.equal(document.querySelector('#run-list-empty').textContent, 'No loaded runs match this search and these filters.');
});

test('run list describes an empty combined search and failure result', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs = [
    { ...data.list.runs[0], id: 'run-search-match', project: 'needle', failure: false },
    { ...data.list.runs[0], id: 'run-failure-match', project: 'other', exit: 'nonzero', failure: true },
  ];
  data.list.total = data.list.runs.length;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document, Event } = dom.window;

  const search = document.querySelector('#run-search');
  search.value = 'needle';
  search.dispatchEvent(new Event('input', { bubbles: true }));
  const failures = document.querySelector('#run-failures-only');
  failures.checked = true;
  failures.dispatchEvent(new Event('change', { bubbles: true }));

  assert.equal(document.querySelector('#run-list-empty').textContent, 'No loaded runs match this search and these filters.');
});

const metricByLabel = (document, label) => Array.from(document.querySelectorAll('.metric'))
  .find((metric) => metric.querySelector('.metric-label')?.textContent === label);

test('tainted verification is warning-class in list and detail', async (t) => {
  const data = fixture('completed', 'warn', 'TAINTED');
  data.list.runs[0].verification = 'TAINTED';
  data.details.evidence.verification = [{ name: 'Status', value: 'TAINTED' }];
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  assert.match(document.querySelector('.run-item .run-verdict-verify').className, /\bwarn\b/);
  assert.match(document.querySelector('#run-verdict').className, /\bwarn\b/);
  const verification = metricByLabel(document, 'Verification verdict');
  assert.match(verification.className, /\bwarn\b/);
  assert.match(verification.textContent, /TAINTED/);
});

test('verification mutation keeps PASS but warns aggregate and count', async (t) => {
  const data = fixture('completed', 'warn', 'PASS');
  data.details.run.warningCount = 1;
  data.details.evidence.verification.push({ name: 'Warning', value: 'verification_mutated_repository: changed.txt' });
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  assert.match(document.querySelector('#run-verdict').className, /\bwarn\b/);
  const verification = metricByLabel(document, 'Verification verdict');
  assert.match(verification.className, /\bpass\b/);
  assert.match(verification.textContent, /PASS/);
  const warnings = metricByLabel(document, 'Warnings');
  assert.match(warnings.className, /\bwarn\b/);
  assert.match(warnings.textContent, /1/);
  assert.match(document.querySelector('#evidence-sections').textContent, /verification_mutated_repository/);
});

test('tab switching updates ARIA state and lazily renders events', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.details.eventCount = 1;
  data.events = [{ id: 'event-1', type: 'assistant.message', provider: 'claude', startedAt: data.details.run.startedAt, data: { text: 'done' } }];
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  const actions = document.querySelector('#timeline-tab-actions');
  const events = document.querySelector('#timeline-tab-events');
  events.click();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(actions.getAttribute('aria-selected'), 'false');
  assert.equal(actions.tabIndex, -1);
  assert.equal(events.getAttribute('aria-selected'), 'true');
  assert.equal(events.tabIndex, 0);
  assert.equal(document.querySelector('#timeline').getAttribute('aria-labelledby'), 'timeline-tab-events');
  assert.equal(document.querySelectorAll('.event-row').length, 1);
});

test('action hierarchy and type filters render behaviorally', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.details.actionCount = 2;
  data.actions = [
    { id: 'parent', type: 'tool.call', provider: 'claude', status: 'reported', startedAt: data.details.run.startedAt, input: { command: 'build' } },
    { id: 'child', parentId: 'parent', type: 'tool.result', provider: 'claude', status: 'success', startedAt: data.details.run.startedAt, result: { text: 'ok' } },
  ];
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  const rows = document.querySelectorAll('.action-row');
  assert.equal(rows.length, 2);
  assert.equal(rows[0].style.getPropertyValue('--depth'), '0');
  assert.equal(rows[1].style.getPropertyValue('--depth'), '1');
  const filter = Array.from(document.querySelectorAll('#type-filters button')).find((button) => button.textContent.includes('tool.call'));
  assert.ok(filter);
  filter.click();
  assert.equal(document.querySelectorAll('.action-row').length, 1);
});

test('verification runner invocations stay visible but file inspection words do not', async (t) => {
  const negative = ['wc -l app.test.js', "sed -n '1,80p' app.test.js", "rg 'test|check|build' app.test.js", 'cat app.test.js', 'git status && test -d .codegraph', 'go test.txt', 'cargo test-data', "printf '%s' 'go test ./...'", "rg 'x; go test' app.js"];
  const positive = ['go test ./...', 'go vet ./...', 'go build ./...', 'node --test app.test.js', 'npm test', 'npm run test:ui', 'npm run check', 'npm run build', 'pytest -q', 'python -m pytest', 'cargo test', './gradlew check', 'gradle test', 'mvn verify', './mvnw test', 'cd repo && go test ./...', 'git status; npm run check', 'git status\nnode --test', ['go', 'test', './...']];
  for (const [commands, grouped] of [[negative, true], [positive, false]]) {
    for (const command of commands) {
      const data = fixture('completed', 'pass', 'PASS');
      data.actions = [0, 1].map((i) => ({ id: `runner-${i}`, type: 'shell.exec', status: 'completed', input: { command } }));
      data.details.actionCount = 2;
      const dom = await renderFixture(data);
      t.after(() => dom.window.close());
      assert.equal(dom.window.document.querySelectorAll('details.action-group').length, grouped ? 1 : 0, JSON.stringify(command));
    }
  }
});

test('live same-page group extension preserves expanded state and focused summary', async (t) => {
  const data = fixture('running', '', 'RUNNING');
  let poll;
  let actions = [0, 1].map((i) => ({ id: `live-${i}`, parentId: 'turn', type: 'file.read', status: 'completed' }));
  const details = data.details;
  data.details = () => ({ ...details, actionCount: actions.length });
  let reads = 0;
  data.actions = (cursor) => {
    assert.equal(cursor, 0, 'refresh retains the exact byte-page anchor');
    return { items: reads++ ? actions.slice(2) : actions, nextCursor: null };
  };
  data.configure = (w) => {
    const timeout = w.setTimeout.bind(w);
    w.setTimeout = (callback, delay, ...args) => {
      if (delay === 3000) { poll = callback; return 987654; }
      return timeout(callback, delay, ...args);
    };
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  const group = d.querySelector('details.action-group');
  group.open = true;
  group.dispatchEvent(new w.Event('toggle'));
  group.querySelector('summary').focus();
  const anchor = group.dataset.groupId;
  actions = [...actions, { id: 'live-2', parentId: 'turn', type: 'file.read', status: 'completed' }];
  await poll();
  await settle();
  assert.equal(d.querySelectorAll('.action-row').length, 3, 'the live tick appended a record');
  // A locale redraw regroups the loaded same-page tail without changing scope.
  d.querySelector('#lang').value = 'en';
  d.querySelector('#lang').dispatchEvent(new w.Event('change', { bubbles: true }));
  const refreshed = d.querySelector('details.action-group');
  assert.equal(refreshed.querySelectorAll('.action-row').length, 3, 'a real poll grew the loaded group');
  assert.equal(refreshed.open, true);
  assert.equal(refreshed.dataset.groupId, anchor);
  assert.equal(d.activeElement, refreshed.querySelector('summary'));
  assert.deepEqual([...refreshed.querySelectorAll('.action-row')].map((row) => row.dataset.index), ['0', '1', '2']);
});

test('group kind names and top-level entry counts are localized without success claims', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.actions = ['shell.exec', 'tool.call', 'file.read', 'search', 'mcp.call', 'web.fetch'].map((type, i) => ({ id: `kind-${i}`, type, status: 'completed' }));
  data.details.actionCount = 6;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;
  for (const [language, kinds, count] of [
    ['en', ['commands', 'tool calls', 'file reading', 'searches', 'MCP calls', 'web fetching'], '1 top-level entries from 6 loaded actions'],
    ['ko', ['명령', '도구 호출', '파일 읽기', '검색', 'MCP 호출', '웹 가져오기'], '로드된 액션 6개에서 최상위 항목 1개'],
    ['ja', ['コマンド', 'ツール呼び出し', 'ファイル読み取り', '検索', 'MCP 呼び出し', 'ウェブ取得'], '読み込み済みアクション6件から最上位項目1件'],
    ['zh-CN', ['命令', '工具调用', '文件读取', '搜索', 'MCP 调用', '网页获取'], '已加载 6 个操作，显示为 1 个顶层条目'],
  ]) {
    d.querySelector('#lang').value = language;
    d.querySelector('#lang').dispatchEvent(new dom.window.Event('change', { bubbles: true }));
    const group = d.querySelector('details.action-group');
    for (const kind of kinds) assert.ok(group.querySelector('.action-group-kinds').textContent.includes(`${kind} × 1`), `${language}: ${kind}`);
    group.open = true;
    assert.equal(d.querySelector('#action-view-count').textContent, count);
    assert.doesNotMatch(group.querySelector('summary').textContent, /success|passed|verified/i);
  }
});

function readingActions() {
  return [
    { id: 'prompt', type: 'user.prompt', status: 'completed', input: { prompt: 'inspect the run' } },
    { id: 'read-1', parentId: 'turn-1', type: 'file.read', provider: 'claude', status: 'completed', input: { path: 'one.go', query: 'needle' } },
    { id: 'search-1', parentId: 'turn-1', type: 'search', provider: 'claude', status: 'success', input: { pattern: 'TODO' } },
    { id: 'write-1', parentId: 'turn-1', type: 'file.write', provider: 'claude', status: 'completed', input: { path: 'one.go' } },
    { id: 'verify-1', parentId: 'turn-1', type: 'shell.exec', provider: 'claude', status: 'completed', input: { command: 'go test ./...' }, result: { exitCode: 0 } },
    { id: 'error-1', parentId: 'turn-1', type: 'tool.call', provider: 'claude', status: 'completed', result: { error: 'structured failure' } },
    { id: 'exit-1', parentId: 'turn-1', type: 'shell.exec', provider: 'claude', status: 'completed', input: { command: 'false' }, result: { exitCode: 1, aggregatedOutput: 'ignored for classification' } },
    { id: 'mcp-1', parentId: 'turn-1', type: 'mcp.call', provider: 'codex', status: 'completed', result: { error: null } },
    { id: 'fetch-1', parentId: 'turn-1', type: 'web.fetch', provider: 'codex', status: 'completed', input: { url: 'https://example.test' } },
    { id: 'warning-1', parentId: 'turn-1', type: 'tool.call', provider: 'codex', status: 'warning' },
    { id: 'running-1', parentId: 'turn-1', type: 'tool.call', provider: 'codex', status: 'in_progress' },
    { id: 'unknown-1', parentId: 'turn-1', type: 'future.tool', provider: 'codex', status: 'completed' },
    { id: 'pending-1', parentId: 'turn-1', type: 'tool.call', provider: 'codex', status: 'pending' },
    { id: 'unknown-status-1', parentId: 'turn-1', type: 'tool.call', provider: 'codex' },
    { id: 'stdout-error-1', parentId: 'turn-2', type: 'tool.call', provider: 'codex', status: 'completed', result: { aggregatedOutput: 'error: words alone are not structured failure evidence' } },
    { id: 'stdout-peer-1', parentId: 'turn-2', type: 'search', provider: 'codex', status: 'completed' },
  ];
}

// JSON.parse matches the external result boundary; no executable coercion hooks.
const deepReadingResult = (depth) => JSON.parse('{"x":'.repeat(depth) + '0' + '}'.repeat(depth));
for (const [name, result] of [
  ['deep4500', deepReadingResult(4500)],
  ['hostile exitCode', JSON.parse('{"exitCode":{"toString":null,"valueOf":null}}')],
  ['hostile status', JSON.parse('{"status":{"toString":null}}')],
  ['depth budget', deepReadingResult(65)],
  ['work budget', { values: Array(4097).fill(0) }],
  ['uncertain error object', { error: { toString: null } }],
  ['uncertain exit array', { exitCode: [] }],
]) test(`Reading robustness keeps ${name} visible and following actions reachable`, async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const errors = [];
  data.configure = (w) => {
    w.addEventListener('error', (event) => errors.push(event.message));
    w.addEventListener('unhandledrejection', (event) => errors.push(event.reason));
  };
  data.actions = [
    { id: 'uncertain', type: 'tool.call', status: 'completed', result },
    { id: 'normal-a', type: 'file.read', status: 'completed', input: { path: 'one.go' } },
    { id: 'normal-b', type: 'search', status: 'completed', input: { pattern: 'TODO' } },
  ];
  data.details.actionCount = data.actions.length;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  const rows = d.querySelectorAll('#timeline .action-row');
  t.diagnostic(`${name}: currentrows=${rows.length}; streamError=${d.querySelector('.stream-error')?.textContent || 'none'}`);
  assert.equal(rows.length, 3, 'no record disappears when result inspection is unsafe');
  assert.ok(d.querySelector('#timeline > .action-row[data-index="0"]'), 'uncertain is individually visible, not folded');
  const group = d.querySelector('#timeline > details.action-group');
  assert.ok(group, 'normal following pair still folds');
  assert.deepEqual([...group.querySelectorAll('.action-row')].map((row) => row.dataset.index), ['1', '2']);
  group.open = true;
  for (const index of [0, 1, 2]) {
    const row = d.querySelector(`.action-row[data-index="${index}"]`);
    row.click();
    row.focus();
    assert.match(row.className, /\bselected\b/);
    assert.equal(d.activeElement, row);
  }
  assert.equal(data.actions[0].result, result, 'canonical result is not replaced');
  assert.equal(data.actions[0].status, 'completed', 'uncertainty is not a failure verdict');
  assert.equal(d.querySelector('.stream-error'), null, 'no swallowed render exception');
  assert.ok(d.querySelector('#workspace-empty').classList.contains('hidden'), 'no swallowed global run-load error');
  assert.ok(!d.querySelector('#run-view').classList.contains('hidden'));
  assert.deepEqual(errors, [], 'no global error or unhandled rejection');
});

test('Reading robustness preserves scalar evidence and inclusive inspection budgets', async (t) => {
  const benign = [
    {}, { exitCode: 0 }, { exit_code: '0' }, { exitCode: null }, { exitCode: false }, { exitCode: '' },
    { error: null, warning: false, warnings: '', isError: 0, failed: false, success: true, ok: true },
    { status: 'completed' }, { nested: [{ exitCode: '0', status: 'success' }] },
    { stdout: '{"error":true,"exitCode":1}', aggregatedOutput: 'ERROR FAILED warning' },
    deepReadingResult(64), { values: Array(4095).fill(0) },
  ];
  const individual = [
    { error: true }, { error: 'oops' }, { warning: true }, { failed: true }, { success: false }, { ok: false },
    { exitCode: 1 }, { exit_code: '-1' }, { status: 'FaIlEd' }, { nested: [{ status: 'timeout' }] },
    { exitCode: 'unknown' }, { status: [] }, { failed: {} }, { success: 'true' },
  ];
  const data = fixture('completed', 'pass', 'PASS');
  const cases = [...benign.map((result) => ({ result, fold: true })), ...individual.map((result) => ({ result, fold: false }))];
  data.actions = cases.flatMap(({ result }, i) => [
    { id: `case-${i}`, parentId: `scope-${i}`, type: 'tool.call', status: 'completed', result },
    { id: `peer-${i}`, parentId: `scope-${i}`, type: 'search', status: 'completed' },
  ]);
  data.details.actionCount = data.actions.length;
  const before = JSON.stringify(data.actions);
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;
  assert.equal(d.querySelectorAll('#timeline .action-row').length, data.actions.length);
  for (const [i, { fold }] of cases.entries()) {
    const row = d.querySelector(`.action-row[data-index="${i * 2}"]`);
    assert.equal(Boolean(row.closest('.action-group')), fold, `case ${i}: ${JSON.stringify(cases[i].result).slice(0, 120)}`);
  }
  assert.equal(JSON.stringify(data.actions), before, 'inspection does not alter canonical evidence');
});

test('payload formatting limits are explicit and preserve subsequent evidence', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const payload = { answer: 42 }, errors = [];
  data.actions = [{ id: 'limited', type: 'mcp.call', status: 'unknown', result: payload }, { id: 'after', type: 'file.read', status: 'completed', input: { path: 'after.md' } }];
  data.details.actionCount = data.actions.length;
  data.configure = (w) => {
    w.addEventListener('error', (e) => { errors.push(e.message); e.preventDefault(); });
    const original = w.JSON.stringify;
    w.JSON.stringify = (value, ...args) => { if (value === payload) throw new w.RangeError('synthetic engine formatting limit'); return original(value, ...args); };
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  d.querySelector('.action-row[data-index="0"]').click();
  for (const [lang, message] of [['en', 'Original recorded evidence is unchanged.'], ['ko', '기록 원본은 변경되지 않았습니다.'], ['ja', '記録された元の証拠は変更されていません。'], ['zh-CN', '原始证据记录未被修改。']]) {
    d.querySelector('#lang').value = lang;
    d.querySelector('#lang').dispatchEvent(new w.Event('change', { bubbles: true }));
    const warning = d.querySelector('.payload-format-warning');
    assert.ok(warning, lang);
    assert.equal(warning.getAttribute('role'), 'alert');
    assert.ok(warning.textContent.includes(message), lang);
  }
  assert.deepEqual(errors, []);
  assert.deepEqual(payload, { answer: 42 });
  d.querySelector('.action-row[data-index="1"]').click();
  assert.match(d.querySelector('#inspector').textContent, /after\.md/);
  assert.equal(d.querySelector('.payload-format-warning'), null);
});

test('unexpected payload serialization errors are not disguised as formatting limits', async (t) => {
  const data = fixture('completed', 'pass', 'PASS'), payload = { answer: 42 }, errors = [];
  data.actions = [{ id: 'unexpected', type: 'mcp.call', status: 'unknown', result: payload }];
  data.configure = (w) => {
    w.addEventListener('error', (e) => { errors.push(e.message); e.preventDefault(); });
    const original = w.JSON.stringify;
    w.JSON.stringify = (value, ...args) => { if (value === payload) throw new w.Error('synthetic unexpected serialization error'); return original(value, ...args); };
  };
  const dom = await renderFixture(data); t.after(() => dom.window.close());
  dom.window.document.querySelector('.action-row').click();
  assert.deepEqual(errors, ['synthetic unexpected serialization error']);
  assert.equal(dom.window.document.querySelector('.payload-format-warning'), null);
});

test('Reading view groups only consecutive eligible completed provider records', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.actions = readingActions();
  data.details.actionCount = data.actions.length;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;

  assert.equal(d.querySelector('#all-actions-toggle').checked, false);
  assert.equal(d.querySelector('#action-view-label').textContent, 'Reading view');
  const groups = d.querySelectorAll('#timeline > details.action-group');
  assert.equal(groups.length, 3, 'only eligible completed runs fold');
  assert.deepEqual([...groups].map((group) => [...group.querySelectorAll('.action-row')].map((row) => row.dataset.index)), [['1', '2'], ['7', '8'], ['14', '15']]);
  assert.match(groups[0].querySelector('summary').textContent, /file reading × 1/);
  assert.match(groups[0].querySelector('summary').textContent, /searches × 1/);
  assert.match(groups[0].querySelector('summary').textContent, /2 completed provider records · loaded page/);
  assert.doesNotMatch(groups[0].querySelector('summary').textContent, /verified|passed|success/i);
  assert.equal(d.querySelectorAll('#timeline > .action-row').length, 10, 'prompts, mutations, verification, failures and unsupported states stay top-level');
  assert.match(d.querySelector('#action-view-count').textContent, /13 top-level entries from 16 loaded actions/);
  assert.equal(groups[0].open, false);
  assert.equal(groups[0].querySelector('summary').tabIndex, 0);
  for (const [language, view, all, scope] of [
    ['en', 'Reading view', 'All actions', '2 completed provider records · loaded page'],
    ['ko', '읽기 보기', '모든 액션', '완료로 보고된 기록 2개 · 로드된 페이지'],
    ['ja', '読みやすい表示', 'すべてのアクション', '完了と報告された記録2件 · 読み込み済みページ'],
    ['zh-CN', '阅读视图', '所有操作', '2 条报告为已完成的记录 · 已加载页面'],
  ]) {
    d.querySelector('#lang').value = language;
    d.querySelector('#lang').dispatchEvent(new dom.window.Event('change', { bubbles: true }));
    assert.equal(d.querySelector('#action-view-label').textContent, view);
    assert.equal(d.querySelector('.action-view-toggle span').textContent, all);
    assert.ok(d.querySelector('.action-group-summary').textContent.includes(scope));
  }
});

test('Reading view never joins parent or byte-page boundaries and preserves original cursors', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const actions = [
    { id: 'same-page-a', parentId: 'parent-a', type: 'file.read', status: 'completed' },
    { id: 'same-page-b', parentId: 'parent-a', type: 'search', status: 'completed' },
    { id: 'different-parent', parentId: 'parent-b', type: 'tool.call', status: 'completed' },
    { id: 'last-first-page', parentId: 'parent-c', type: 'tool.call', status: 'completed' },
    { id: 'first-second-page', parentId: 'parent-c', type: 'tool.call', status: 'completed' },
    { id: 'second-second-page', parentId: 'parent-c', type: 'mcp.call', status: 'completed' },
  ];
  data.details.actionCount = actions.length;
  data.actions = (cursor) => cursor === 0
    ? { items: actions.slice(0, 4), nextCursor: 731, endCursor: 731 }
    : { items: actions.slice(4), nextCursor: null, endCursor: 1199 };
  const writes = [];
  let append;
  data.configure = (w) => {
    w.IntersectionObserver = class {
      constructor(callback) { append = callback; }
      observe() {}
      disconnect() {}
    };
    Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: async (url) => writes.push(url) } });
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;

  const retainedGroup = d.querySelector('#timeline > details.action-group');
  retainedGroup.open = true;
  retainedGroup.dispatchEvent(new w.Event('toggle'));
  const retainedRow = retainedGroup.querySelector('.action-row[data-index="1"]');
  retainedRow.click();
  retainedRow.focus();
  assert.deepEqual([...d.querySelectorAll('#timeline > details.action-group')].map((group) => group.querySelectorAll('.action-row').length), [2]);
  append([{ isIntersecting: true, target: d.querySelector('.stream-sentinel') }]);
  await settle();
  const groups = d.querySelectorAll('#timeline > details.action-group');
  assert.deepEqual([...groups].map((group) => [...group.querySelectorAll('.action-row')].map((row) => row.dataset.index)), [['0', '1'], ['4', '5']]);
  assert.equal(groups[0], retainedGroup);
  assert.equal(groups[0].open, true);
  assert.match(retainedRow.className, /\bselected\b/);
  assert.equal(d.activeElement, retainedRow);
  assert.ok(d.querySelector('#timeline > .action-row[data-index="3"]'), 'the first page tail remains outside the second page group');
  groups[1].open = true;
  d.querySelector('.action-row[data-index="5"]').click();
  d.querySelector('.copy-evidence-link').click();
  await settle();
  assert.match(writes[0], /action=second-second-page&actionCursor=731$/);
});

test('timeline search and active type filters expose matching actions outside groups', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.actions = readingActions().slice(1, 4);
  data.details.actionCount = data.actions.length;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document: d, Event } = dom.window;

  assert.equal(d.querySelectorAll('#timeline > details.action-group').length, 1);
  const search = d.querySelector('#timeline-search');
  search.value = 'needle';
  search.dispatchEvent(new Event('input', { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 220));
  assert.equal(d.querySelectorAll('#timeline > details.action-group').length, 0);
  assert.deepEqual([...d.querySelectorAll('#timeline > .action-row')].map((row) => row.dataset.index), ['0']);

  search.value = '';
  search.dispatchEvent(new Event('input', { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 220));
  const editFilter = [...d.querySelectorAll('#type-filters button')].find((button) => button.dataset.type === 'file.write');
  editFilter.click();
  assert.equal(d.querySelectorAll('#timeline > details.action-group').length, 0);
  assert.deepEqual([...d.querySelectorAll('#timeline > .action-row')].map((row) => row.dataset.index), ['0', '1']);
});

test('expanded group, selected action and focus survive locale and All-actions toggles', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.actions = readingActions().slice(1, 3);
  data.details.actionCount = data.actions.length;
  data.details.evidence.verification = [{ name: 'Status', value: 'PENDING' }];
  const firstDetails = data.details;
  const secondRun = { ...firstDetails.run, id: 'second-reading-run' };
  data.list.runs.push({ ...secondRun, verification: 'PENDING' });
  data.list.total = 2;
  data.details = (id) => ({ ...firstDetails, run: id === secondRun.id ? secondRun : firstDetails.run });
  let poll;
  data.configure = (w) => {
    w.setInterval = (callback, delay) => { if (delay === 5000) poll = callback; return delay; };
    w.clearInterval = () => {};
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  let group = d.querySelector('details.action-group');
  group.open = true;
  group.dispatchEvent(new w.Event('toggle'));
  let selected = d.querySelector('.action-row[data-index="1"]');
  selected.click();
  selected.focus();

  await poll();
  assert.equal(d.querySelector('details.action-group'), group);
  assert.equal(group.open, true);
  assert.match(selected.className, /\bselected\b/);
  assert.equal(d.activeElement, selected);

  d.querySelector('#lang').value = 'ko';
  d.querySelector('#lang').dispatchEvent(new w.Event('change', { bubbles: true }));
  group = d.querySelector('details.action-group');
  selected = d.querySelector('.action-row[data-index="1"]');
  assert.equal(group.open, true);
  assert.match(selected.className, /\bselected\b/);
  assert.equal(d.activeElement, selected);
  assert.match(d.querySelector('.inspector-title').textContent, /search/);

  const toggle = d.querySelector('#all-actions-toggle');
  toggle.checked = true;
  toggle.dispatchEvent(new w.Event('change', { bubbles: true }));
  selected = d.querySelector('.action-row[data-index="1"]');
  assert.equal(d.querySelector('details.action-group'), null);
  assert.deepEqual([...d.querySelectorAll('#timeline > .action-row')].map((row) => row.dataset.index), ['0', '1']);
  assert.match(selected.className, /\bselected\b/);
  assert.equal(d.activeElement, selected);
  assert.equal(d.querySelector('#action-view-label').textContent, '모든 액션');

  toggle.checked = false;
  toggle.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.equal(d.querySelector('details.action-group').open, true);
  assert.match(d.querySelector('.action-row[data-index="1"]').className, /\bselected\b/);
  assert.equal(d.activeElement, d.querySelector('.action-row[data-index="1"]'));

  d.querySelector(`[data-run-id="${secondRun.id}"]`).click();
  await settle();
  assert.equal(d.querySelector('details.action-group').open, false, 'group expansion does not cross run boundaries');
  assert.equal(d.querySelector('.action-row.selected'), null);
});

test('exact action search and history open a beyond-first-page containing group', async (t) => {
  const data = actionLinkFixture();
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  await openActionHit(w, 1);
  let selected = d.querySelector('.action-row[data-index="0"]');
  assert.equal(selected.dataset.index, '0', 'the exact byte page starts a fresh local index');
  assert.equal(selected.closest('details.action-group').open, true);
  assert.match(selected.className, /\bselected\b/);
  assert.equal(d.activeElement, selected);
  assert.equal(new URLSearchParams(w.location.search).get('actionCursor'), '99234');

  w.history.back();
  await settle();
  w.history.forward();
  await settle();
  selected = d.querySelector('.action-row[data-index="0"]');
  assert.equal(selected.closest('details.action-group').open, true);
  assert.match(selected.className, /\bselected\b/);
  assert.equal(d.activeElement, selected);
});

test('a delayed A response cannot overwrite the selected B run', async (t) => {
  const a = fixture('completed', 'pass', 'PASS').details;
  const b = fixture('completed', 'pass', 'PASS').details;
  a.run.id = '20260903T000000.000000000Z-00000001';
  b.run.id = '20260903T000001.000000000Z-00000002';
  b.run.project = 'selected-b';
  let resolveA;
  let resolveB;
  const detailA = new Promise((resolve) => { resolveA = resolve; });
  const detailB = new Promise((resolve) => { resolveB = resolve; });
  const dom = new JSDOM(html, { runScripts: 'outside-only', url: 'http://localhost:42817/', pretendToBeVisual: true });
  t.after(() => dom.window.close());
  const { window } = dom;
  window.IntersectionObserver = class { observe() {} disconnect() {} };
  window.HTMLElement.prototype.scrollIntoView = () => {};
  window.fetch = (input) => {
    const url = new URL(String(input), window.location.href);
    if (url.pathname === '/api/shadow') return response({ allowRun: false });
    if (url.pathname === '/api/runs') {
      return response({ runs: [{ ...a.run, verification: 'PASS' }, { ...b.run, verification: 'PASS' }], total: 2 });
    }
    if (url.pathname === `/api/runs/${a.run.id}`) return detailA;
    if (url.pathname === `/api/runs/${b.run.id}`) return detailB;
    if (url.pathname.includes('/actions')) return response({ items: [], nextCursor: null });
    if (url.pathname.includes('/changes')) return response({ files: [], nextCursor: null, total: 0 });
    throw new Error(`unexpected fetch ${url}`);
  };
  window.eval(app);
  for (let i = 0; i < 4; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  const bButton = Array.from(window.document.querySelectorAll('.run-item')).find((button) => button.dataset.runId === b.run.id);
  assert.ok(bButton);
  bButton.click();
  resolveB(await response(b));
  for (let i = 0; i < 4; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(window.document.querySelector('#run-title').textContent, b.run.id);
  resolveA(await response(a));
  for (let i = 0; i < 4; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(window.document.querySelector('#run-title').textContent, b.run.id);
});

test('initial pending run outside the first page is selected and refreshed', async (t) => {
  let poll;
  let detailReads = 0;
  const selected = fixture('session_ended', '', 'session_ended');
  selected.details.run.id = 'run-older-selected';
  selected.details.run.startedAt = '2024-01-01T00:00:00Z';
  selected.details.evidence.verification = [{ name: 'Status', value: 'PENDING' }];
  selected.list.initialRunId = selected.details.run.id;
  selected.list.runs = [];
  selected.list.pageIds = ['run-newest-unreadable'];
  selected.list.unreadable = 1;
  selected.list.total = 55;
  selected.list.nextCursor = 'opaque';
  selected.list.generation = 'g1';
  let details = selected.details;
  selected.details = () => {
    detailReads += 1;
    return details;
  };
  selected.configure = (window) => {
    window.setInterval = (callback, delay) => {
      if (delay === 5000) poll = callback;
      return delay;
    };
    window.clearInterval = () => {};
  };

  const dom = await renderFixture(selected);
  t.after(() => dom.window.close());
  const { document } = dom.window;
  assert.equal(document.querySelector('#run-title').textContent, details.run.id);
  assert.match(document.querySelector('#evidence-verification').textContent, /PENDING/);

  details = { ...details, evidence: { ...details.evidence, verification: [{ name: 'Status', value: 'PASS' }] } };
  await poll();

  assert.match(document.querySelector('#evidence-verification').textContent, /PASS/);
  assert.equal(detailReads, 2);
  await poll();
  assert.equal(detailReads, 2);
});

test('run pages append explicitly and unchanged polls retain DOM nodes', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const first = data.details.run;
  const second = { ...first, id: '20260902T000000.000000000Z-00000002', project: 'second' };
  const third = { ...first, id: '20260901T000000.000000000Z-00000003', project: 'third' };
  const filterURL = '?q=claude&exit=completed&verification=PASS#kept';
  const selectedURL = `?q=claude&exit=completed&verification=PASS&run=${encodeURIComponent(first.id)}#kept`;
  const dom = new JSDOM(html, { runScripts: 'outside-only', url: `http://localhost:42817/${filterURL}`, pretendToBeVisual: true });
  t.after(() => dom.window.close());
  const { window } = dom;
  let poll;
  let changed = false;
  let unreadable = false;
  window.setInterval = (callback, delay) => {
    if (delay === 5000) poll = callback;
    return 1;
  };
  window.clearInterval = () => {};
  window.IntersectionObserver = class { observe() {} disconnect() {} };
  window.HTMLElement.prototype.scrollIntoView = () => {};
  window.fetch = (input) => {
    const url = new URL(String(input), window.location.href);
    if (url.pathname === '/api/shadow') return response({ allowRun: false });
    if (url.pathname === '/api/runs' && url.searchParams.has('cursor')) {
      return response({ runs: [{ ...third, verification: 'PASS' }], total: 3, generation: 'g1' });
    }
    if (url.pathname === '/api/runs') {
      const runs = unreadable
        ? [{ ...second, verification: 'PASS' }]
        : [{ ...first, verification: 'PASS' }, { ...second, verification: 'PASS' }];
      return response({ runs, pageIds: [first.id, second.id], unreadable: unreadable ? 1 : 0, total: changed ? 2 : 3, nextCursor: changed ? '' : second.id, generation: changed ? 'g2' : 'g1' });
    }
    if (url.pathname === `/api/runs/${first.id}`) return response(data.details);
    if (url.pathname.includes('/actions')) return response({ items: [], nextCursor: null });
    if (url.pathname.includes('/changes')) return response({ files: [], nextCursor: null, total: 0 });
    throw new Error(`unexpected fetch ${url}`);
  };
  window.eval(app);
  for (let i = 0; i < 6; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(window.document.querySelectorAll('.run-item').length, 2);
  const firstNode = window.document.querySelector('.run-item');
  window.document.querySelector('#run-load-more').click();
  for (let i = 0; i < 3; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(window.document.querySelectorAll('.run-item').length, 3);
  assert.equal(window.location.search + window.location.hash, selectedURL);
  const loadedFirstNode = window.document.querySelector('.run-item');
  assert.notEqual(loadedFirstNode, firstNode);
  assert.ok(poll);
  await poll();
  assert.equal(window.document.querySelectorAll('.run-item').length, 3);
  assert.equal(window.document.querySelector('.run-item'), loadedFirstNode);
  assert.equal(window.location.search + window.location.hash, selectedURL);
  unreadable = true;
  await poll();
  assert.deepEqual([...window.document.querySelectorAll('.run-item')].map((node) => node.dataset.runId), [second.id, third.id]);
  unreadable = false;
  await poll();
  assert.deepEqual([...window.document.querySelectorAll('.run-item')].map((node) => node.dataset.runId), [first.id, second.id, third.id]);
  changed = true;
  await poll();
  assert.equal(window.document.querySelectorAll('.run-item').length, 2);
  assert.equal(window.document.querySelector('#run-load-more').classList.contains('hidden'), true);
});

test('Folder view groups only loaded changes by exact immediate directory and keeps full paths accessible', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.changes = (cursor) => cursor === 0
    ? {
        items: [
          { path: 'README.md', kind: 'modified', tracked: true, additions: 2, deletions: 1 },
          { path: 'src/a/index.js', kind: 'added', tracked: false, additions: 4 },
          { path: 'src/b/index.js', kind: 'modified', tracked: true, binary: true },
          { path: 'src/a/util.js', kind: 'modified', tracked: true, additions: 1, deletions: 3 },
        ],
        nextCursor: 4,
        total: 7,
        status: 'available',
        attribution: 'observed during run, not causal proof',
      }
    : { items: [], nextCursor: null, total: 7, status: 'available', attribution: 'observed during run, not causal proof' };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document: d, Event } = dom.window;
  d.querySelector('#timeline-tab-changes').click();
  await settle();

  assert.equal(d.querySelector('#change-view-label').textContent, 'Folder view');
  assert.equal(d.querySelector('#all-changes-toggle').checked, false);
  const groups = [...d.querySelectorAll('#timeline > details.change-folder-group')];
  assert.deepEqual(groups.map((group) => group.querySelector('.change-folder-name').textContent), ['Repository root', 'src/a', 'src/b']);
  assert.deepEqual(groups.map((group) => group.querySelectorAll('.change-row').length), [1, 2, 1]);
  assert.match(groups[1].querySelector('.change-folder-meta').textContent, /2 loaded files/);
  assert.match(d.querySelector('#change-view-count').textContent, /3 folders from 4 loaded changes/);
  assert.match(d.querySelector('.pager-label').textContent, /Loaded 4 of 7/);
  assert.match(d.querySelector('.timeline-note').textContent, /not proof the agent caused it/);
  const twins = [...d.querySelectorAll('.change-row')].filter((row) => row.querySelector('.action-type').textContent === 'index.js');
  assert.deepEqual(twins.map((row) => row.dataset.path), ['src/a/index.js', 'src/b/index.js']);
  assert.deepEqual(twins.map((row) => row.querySelector('.action-type').title), ['src/a/index.js', 'src/b/index.js']);
  assert.deepEqual(twins.map((row) => row.querySelector('.action-type').getAttribute('aria-label')), ['src/a/index.js', 'src/b/index.js']);
  assert.match(twins[1].textContent, /binary/);

  const search = d.querySelector('#timeline-search');
  search.value = 'src/b/index.js';
  search.dispatchEvent(new Event('input', { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 220));
  assert.equal(d.querySelector('details.change-folder-group'), null, 'search hits are exposed as individual files');
  assert.deepEqual([...d.querySelectorAll('#timeline > .change-row')].map((row) => row.dataset.path), ['src/b/index.js']);
  assert.deepEqual([...d.querySelectorAll('#timeline > .change-row .action-type')].map((el) => el.textContent), ['src/b/index.js']);

  search.value = '';
  search.dispatchEvent(new Event('input', { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 220));
  const toggle = d.querySelector('#all-changes-toggle');
  toggle.checked = true;
  toggle.dispatchEvent(new Event('change', { bubbles: true }));
  assert.equal(d.querySelector('details.change-folder-group'), null);
  assert.equal(d.querySelectorAll('#timeline > .change-row').length, 4);
  assert.deepEqual([...d.querySelectorAll('#timeline > .change-row .action-type')].map((el) => el.textContent), ['README.md', 'src/a/index.js', 'src/b/index.js', 'src/a/util.js']);
  assert.equal(d.querySelector('#all-actions-toggle').checked, false, 'Actions keeps its independent reading setting');
});

test('exact later-page change opens its folder and retains canonical link and patch loading', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const path = 'deep/target/same.js';
  data.details.run.changeCount = 251;
  const pages = paginatedChanges(path);
  data.changes = (cursor) => {
    const page = pages(cursor);
    return { ...page, items: page.items.map((change) => change.path === path ? { ...change, tracked: true } : change) };
  };
  const writes = [];
  data.configure = (w) => {
    w.history.replaceState(null, '', `/?run=${data.details.run.id}&focus=changes&change=${encodeURIComponent(path)}&changeCursor=250`);
    Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: async (url) => writes.push(url) } });
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  const originalFetch = w.fetch;
  w.fetch = (input, init) => String(input).includes('/patch?')
    ? response({ path, patch: '+exact patch', nextCursor: null, attribution: 'observed during run, not causal proof' })
    : originalFetch(input, init);
  const row = d.querySelector(`.change-row[data-path="${path}"]`);
  assert.ok(row.closest('details.change-folder-group').open);
  row.click();
  await settle();
  d.querySelector('.copy-evidence-link').click();
  await settle();
  assert.match(writes[0], /change=deep%2Ftarget%2Fsame.js&changeCursor=250$/);
  assert.match(d.querySelector('.diff-patch').textContent, /exact patch/);
});

test('Korean attribution notes keep words intact without overflowing identifiers', (t) => {
  const dom = responsiveFixture('<p class="timeline-note">원인이라는 증명은 아닙니다</p>', 375);
  t.after(() => dom.window.close());
  dom.window.document.documentElement.lang = 'ko';
  const style = dom.window.getComputedStyle(dom.window.document.querySelector('.timeline-note'));
  assert.equal(style.wordBreak, 'keep-all');
  assert.equal(style.overflowWrap, 'anywhere');
});

test('event group summaries are concise while original tool names remain inspectable', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const opaque = 'mcp__very_long_diagnostic_tool_name';
  data.events = ['Bash', opaque].map((tool_name) => ({ hook_event_name: 'PostToolUse', session_id: 's', tool_name, tool_response: {} }));
  data.details.eventCount = data.events.length;
  const dom = await renderFixture(data); t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  d.querySelector('#timeline-tab-events').click(); await settle();
  for (const [lang, title, names] of [['en', '2 tool records', '2 tool names'], ['ko', '도구 기록 2개', '도구 이름 2종'], ['ja', 'ツール記録2件', 'ツール名2種類'], ['zh-CN', '2 条工具记录', '2 种工具名称']]) {
    d.querySelector('#lang').value = lang; d.querySelector('#lang').dispatchEvent(new w.Event('change', { bubbles: true }));
    const group = d.querySelector('.event-group');
    assert.equal(group.querySelector('.action-group-kinds').textContent, title);
    assert.ok(group.querySelector('.action-group-meta').textContent.includes(names));
    assert.ok(!group.querySelector('summary').textContent.includes(opaque));
    assert.ok(group.querySelectorAll('.event-row')[1].textContent.includes(opaque));
  }
});

test('Event summary rejects conflicting unknown and invalid primary types', async (t) => {
  for (const type of ['error', 'future.provider.shape', 'SessionStart', '', null, 42, {}, []]) {
    await t.test(`primary ${JSON.stringify(type)}`, async (t) => {
      const data = fixture('completed', 'pass', 'PASS');
      const hook = { hook_event_name: 'PostToolUse', session_id: 's', tool_name: 'Read' };
      data.events = [hook, { ...hook, type }, hook, { ...hook, type: 'PostToolUse' }];
      data.details.eventCount = data.events.length;
      const dom = await renderFixture(data); t.after(() => dom.window.close());
      const d = dom.window.document;
      d.querySelector('#timeline-tab-events').click(); await settle();
      assert.ok(d.querySelector('#timeline > .event-row[data-index="1"]'), 'ambiguous primary type stays individually visible');
      const group = d.querySelector('.event-group');
      assert.deepEqual([...group.querySelectorAll('.event-row')].map((row) => row.dataset.index), ['2', '3']);
      assert.doesNotMatch(group.querySelector('summary').textContent, /success|passed|completed/i);
    });
  }
});

test('Event summary folds only safe same-page same-session PostToolUse spans', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const first = [
    { hook_event_name: 'SessionStart', session_id: 's1', source: 'startup' },
    { hook_event_name: 'PostToolUse', session_id: 's1', tool_name: 'Bash', tool_response: 'not interpreted' },
    { hook_event_name: 'PostToolUse', session_id: 's1', tool_name: 'Read', tool_response: 'not interpreted' },
    { hook_event_name: 'PostToolUseFailure', session_id: 's1', tool_name: 'Bash', error: 'failed' },
    { type: 'future.provider.shape', session_id: 's1', payload: { value: 1 } },
    { hook_event_name: 'PostToolUse', session_id: 's1', tool_name: 'Write', agentrec_dropped: 'payload too large' },
    { hook_event_name: 'PostToolUse', session_id: 's1', tool_name: 'Bash' },
  ];
  const second = [
    { hook_event_name: 'PostToolUse', session_id: 's1', tool_name: 'Read' },
    { hook_event_name: 'UserPromptSubmit', session_id: 's1', prompt: 'next request' },
    { hook_event_name: 'PostToolUse', session_id: 's2', tool_name: 'Bash' },
    { hook_event_name: 'PostToolUse', session_id: 's3', tool_name: 'Read' },
    { hook_event_name: 'PostToolUse', session_id: 's3', tool_name: 'Bash', tool_response: { exit_code: 2 } },
    { hook_event_name: 'Stop', session_id: 's3', last_assistant_message: 'turn stopped' },
    { hook_event_name: 'SessionEnd', session_id: 's3', reason: 'done' },
  ];
  data.details.eventCount = first.length + second.length;
  data.events = (cursor) => cursor === 0
    ? { items: first, nextCursor: first.length }
    : { items: second, nextCursor: null };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document: d, Event } = dom.window;
  d.querySelector('#timeline-tab-events').click();
  await settle();

  assert.equal(d.querySelector('#event-view-label').textContent, 'Event summary');
  let groups = [...d.querySelectorAll('#timeline > details.event-group')];
  assert.equal(groups.length, 1);
  assert.deepEqual([...groups[0].querySelectorAll('.event-row')].map((row) => row.dataset.index), ['1', '2']);
  assert.equal(groups[0].querySelector('.action-group-kinds').textContent, '2 tool records');
  assert.match(groups[0].querySelector('.action-group-meta').textContent, /2 tool names · loaded page/);
  for (const index of [1, 2]) assert.equal(groups[0].querySelector(`.event-row[data-index="${index}"] .action-summary`).textContent, first[index].tool_name);
  assert.doesNotMatch(groups[0].querySelector('summary').textContent, /success|passed|completed/i);
  groups[0].querySelector('.event-row[data-index="2"]').click();
  assert.match(d.querySelector('#inspector').textContent, /event #3/);
  assert.match(d.querySelector('#inspector').textContent, /"hook_event_name": "PostToolUse"/);
  assert.equal(d.querySelector('.copy-evidence-link'), null, 'provider events do not gain invented permalinks');
  for (const type of ['SessionStart', 'PostToolUseFailure', 'future.provider.shape']) {
    assert.ok([...d.querySelectorAll('#timeline > .event-row')].some((row) => row.title === type), `${type} stays visible`);
  }
  assert.ok([...d.querySelectorAll('#timeline > .event-row')].some((row) => row.textContent.includes('payload too large')), 'dropped record stays visible');
  assert.equal(d.querySelector('.stream-tail .load-more').classList.contains('hidden'), false, 'summary paging stays explicit');
  d.querySelector('.stream-tail .load-more').click();
  await settle();
  groups = [...d.querySelectorAll('#timeline > details.event-group')];
  assert.equal(groups.length, 1, 'records never merge across page or session boundaries');
  assert.equal(d.querySelectorAll('#timeline .event-row').length, first.length + second.length);
  for (const type of ['UserPromptSubmit', 'Stop', 'SessionEnd']) assert.ok([...d.querySelectorAll('#timeline > .event-row')].some((row) => row.title === type));
  assert.equal([...d.querySelectorAll('#timeline > .event-row')].filter((row) => row.title === 'PostToolUse').length, 6, 'page/session/error barriers remain individual');

  const otherTypes = [...d.querySelectorAll('#type-filters button')].map((button) => button.dataset.type).filter((type) => type !== 'PostToolUse');
  for (const type of otherTypes) d.querySelector(`#type-filters button[data-type="${type}"]`).click();
  assert.equal(d.querySelector('details.event-group'), null);
  assert.equal(d.querySelectorAll('#timeline > .event-row').length, 8, 'filters expose every matching record');
  const toggle = d.querySelector('#all-events-toggle');
  toggle.checked = true;
  toggle.dispatchEvent(new Event('change', { bubbles: true }));
  assert.equal(d.querySelector('details.event-group'), null);
  assert.equal(d.querySelector('#all-changes-toggle').checked, false, 'Changes keeps its independent folder setting');
});

test('folder and event disclosure state, selection and focus survive locale and view changes', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.changes = [
    { path: 'src/a.js', kind: 'modified', tracked: true },
    { path: 'src/b.js', kind: 'added', tracked: false },
  ];
  data.details.eventCount = 2;
  data.events = [
    { hook_event_name: 'PostToolUse', session_id: 's', tool_name: 'Read' },
    { hook_event_name: 'PostToolUse', session_id: 's', tool_name: 'Bash' },
  ];
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  d.querySelector('#timeline-tab-changes').click();
  await settle();
  let group = d.querySelector('details.change-folder-group');
  group.open = true;
  group.dispatchEvent(new w.Event('toggle'));
  let row = group.querySelectorAll('.change-row')[1];
  row.click();
  row.focus();
  d.querySelector('#lang').value = 'ko';
  d.querySelector('#lang').dispatchEvent(new w.Event('change', { bubbles: true }));
  group = d.querySelector('details.change-folder-group');
  row = group.querySelector('.change-row[data-path="src/b.js"]');
  assert.equal(group.open, true);
  assert.match(row.className, /selected/);
  assert.equal(d.activeElement, row);
  assert.equal(d.querySelector('#change-view-label').textContent, '폴더 보기');
  const changeToggle = d.querySelector('#all-changes-toggle');
  changeToggle.checked = true;
  changeToggle.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.equal(d.activeElement.dataset.path, 'src/b.js');
  changeToggle.checked = false;
  changeToggle.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.equal(d.querySelector('details.change-folder-group').open, true);
  assert.equal(d.activeElement.dataset.path, 'src/b.js');

  d.querySelector('#timeline-tab-events').click();
  await settle();
  const eventGroup = d.querySelector('details.event-group');
  eventGroup.open = true;
  eventGroup.dispatchEvent(new w.Event('toggle'));
  const eventRow = eventGroup.querySelector('.event-row[data-index="1"]');
  eventRow.click();
  eventRow.focus();
  d.querySelector('#lang').value = 'ja';
  d.querySelector('#lang').dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.equal(d.querySelector('details.event-group').open, true);
  assert.match(d.querySelector('.event-row[data-index="1"]').className, /selected/);
  assert.equal(d.activeElement, d.querySelector('.event-row[data-index="1"]'));
  assert.equal(d.querySelector('#event-view-label').textContent, 'イベント概要');
  assert.match(d.querySelector('#inspector').textContent, /PostToolUse/);
});

test('Changes and Events view labels localize independently in all supported languages', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.changes = [{ path: 'README.md', kind: 'modified', tracked: false }];
  data.details.eventCount = 1;
  data.events = [{ hook_event_name: 'SessionStart', session_id: 's', source: 'startup' }];
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  for (const [lang, folder, files, summary, events] of [
    ['en', 'Folder view', 'All files', 'Event summary', 'All events'],
    ['ko', '폴더 보기', '모든 파일', '이벤트 요약', '모든 이벤트'],
    ['ja', 'フォルダー表示', 'すべてのファイル', 'イベント概要', 'すべてのイベント'],
    ['zh-CN', '文件夹视图', '所有文件', '事件摘要', '所有事件'],
  ]) {
    d.querySelector('#lang').value = lang;
    d.querySelector('#lang').dispatchEvent(new w.Event('change', { bubbles: true }));
    d.querySelector('#timeline-tab-changes').click();
    await settle();
    assert.equal(d.querySelector('#change-view-label').textContent, folder);
    assert.equal(d.querySelector('#change-view-controls label span').textContent, files);
    d.querySelector('#timeline-tab-events').click();
    await settle();
    assert.equal(d.querySelector('#event-view-label').textContent, summary);
    assert.equal(d.querySelector('#event-view-controls label span').textContent, events);
  }
  assert.equal(d.querySelector('#all-actions-toggle').checked, false);
  assert.equal(d.querySelector('#all-changes-toggle').checked, false);
  assert.equal(d.querySelector('#all-events-toggle').checked, false);
});

for (const mode of ['changes', 'events']) for (const outcome of ['success', 'error', 'moved-success', 'moved-error']) {
  test(`keyboard manual paging ${mode} ${outcome} keeps visible focus`, async (t) => {
    const data = fixture('completed', 'pass', 'PASS');
    const items = mode === 'changes'
      ? [{ path: 'old/a.js', tracked: false }, { path: 'old/b.js', tracked: false }]
      : ['Read', 'Bash'].map((tool_name) => ({ hook_event_name: 'PostToolUse', session_id: 's', tool_name }));
    data[mode] = () => ({ items, nextCursor: 98765, total: 4, status: 'available' });
    data.details.eventCount = 4;
    const dom = await renderFixture(data); t.after(() => dom.window.close());
    const w = dom.window, d = w.document;
    d.querySelector(`#timeline-tab-${mode}`).click(); await settle();
    const selected = d.querySelector('.action-row');
    selected.click();
    let resolvePage, rejectPage;
    const originalFetch = w.fetch;
    w.fetch = (input, init) => String(input).includes(`/${mode}?cursor=98765`)
      ? new Promise((resolve, reject) => { resolvePage = resolve; rejectPage = reject; }) : originalFetch(input, init);
    const more = d.querySelector('.stream-tail .load-more');
    more.focus(); assert.equal(d.activeElement, more); more.click();
    assert.ok(resolvePage);
    const other = d.querySelector('#search-all');
    if (outcome.startsWith('moved')) other.focus();
    if (outcome.endsWith('error')) rejectPage(new Error('keyboard page failure'));
    else resolvePage(await response({ items: mode === 'changes' ? items.map((item) => ({ ...item, path: item.path.replace('old/', 'new/') })) : items, nextCursor: null, total: 4, status: 'available' }));
    await settle();
    assert.ok(d.querySelector('.action-row.selected'), 'selected evidence survives');
    if (outcome.startsWith('moved')) assert.equal(d.activeElement, other, 'pending request must not steal focus');
    else if (outcome === 'error') {
      assert.equal(d.activeElement, d.querySelector('.stream-tail .load-more'));
      assert.equal(d.activeElement.classList.contains('hidden'), false);
    } else {
      const row = d.querySelector('.action-row[data-index="2"]');
      assert.ok(d.activeElement === row || d.activeElement === row.closest('details')?.querySelector('summary'), 'new evidence receives focus');
      assert.ok(!d.activeElement.closest('details:not([open])') || d.activeElement.tagName === 'SUMMARY', 'focus must be visible');
    }
  });
}

test('Folder view append preserves the open group, selected file and focus', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.changes = (cursor) => cursor === 0
    ? { items: [{ path: 'src/a.js', kind: 'modified', tracked: false }, { path: 'src/b.js', kind: 'modified', tracked: false }], nextCursor: 2, total: 3, status: 'available' }
    : { items: [{ path: 'src/c.js', kind: 'added', tracked: false }], nextCursor: null, total: 3, status: 'available' };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  d.querySelector('#timeline-tab-changes').click();
  await settle();
  let group = d.querySelector('details.change-folder-group');
  group.open = true;
  group.dispatchEvent(new w.Event('toggle'));
  let selected = group.querySelector('.change-row[data-path="src/b.js"]');
  selected.click();
  selected.focus();
  d.querySelector('.stream-tail .load-more').click();
  await settle();
  group = d.querySelector('details.change-folder-group');
  selected = group.querySelector('.change-row[data-path="src/b.js"]');
  assert.equal(group.open, true);
  assert.equal(group.querySelector('.change-folder-meta').textContent, '3 loaded files');
  assert.match(selected.className, /selected/);
  assert.equal(d.activeElement, selected);
});

for (const scenario of ['poll', 'locale', 'disappear', 'empty']) {
  test(`live Folder summary focus survives ${scenario}`, async (t) => {
    const data = fixture('running', '', 'RUNNING');
    let files = [{ path: 'src/a.js', status: 'M' }], tick;
    data.live = () => ({ measuredAt: '2026-09-03T00:00:05Z', files });
    data.configure = (w) => {
      const native = w.setTimeout.bind(w);
      w.setTimeout = (callback, delay) => {
        if (delay === 3000) { tick = callback; return 3000; }
        return native(callback, delay);
      };
      w.clearTimeout = () => {};
    };
    const dom = await renderFixture(data); t.after(() => dom.window.close());
    const w = dom.window, d = w.document;
    d.querySelector('#timeline-tab-changes').click(); await settle();
    const summary = d.querySelector('.change-folder-summary');
    summary.focus(); assert.equal(d.activeElement, summary);
    assert.equal(typeof tick, 'function');
    if (scenario === 'locale') {
      d.querySelector('#lang').value = 'ko';
      d.querySelector('#lang').dispatchEvent(new w.Event('change', { bubbles: true }));
    } else {
      if (scenario === 'disappear') files = [{ path: 'other/b.js', status: 'M' }];
      if (scenario === 'empty') files = [];
      await tick(); await settle();
    }
    if (scenario === 'poll' || scenario === 'locale') {
      assert.equal(d.activeElement, d.querySelector('.change-folder-summary'));
      assert.equal(d.activeElement.dataset.groupId, summary.dataset.groupId);
    } else assert.equal(d.activeElement, d.querySelector('#timeline-tab-changes'), 'missing group falls back to visible Changes tab');
  });
}

test('live Folder view clears a disappearing selection and never invents stored patch controls', async (t) => {
  const data = fixture('running', '', 'RUNNING');
  let liveFiles = [{ path: 'src/live.js', status: 'M' }, { path: 'src/peer.js', status: '??' }];
  let tick;
  data.live = () => ({ measuredAt: '2026-09-03T00:00:05Z', files: liveFiles });
  data.configure = (w) => {
    const native = w.setTimeout.bind(w);
    w.setTimeout = (callback, delay) => {
      if (delay === 3000) { tick = callback; return 3000; }
      return native(callback, delay);
    };
    w.clearTimeout = () => {};
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  d.querySelector('#timeline-tab-changes').click();
  await settle();
  const group = d.querySelector('details.change-folder-group');
  assert.equal(group.querySelector('.change-folder-meta').textContent, '2 loaded files');
  group.open = true;
  const row = d.querySelector('.change-row[data-path="src/live.js"]');
  row.click();
  row.focus();
  assert.equal(d.querySelector('.copy-evidence-link'), null);
  assert.equal(d.querySelector('.diff-patch'), null);
  liveFiles = [...liveFiles, { path: 'src/new.js', status: 'A' }];
  await tick();
  await settle();
  assert.match(d.querySelector('.change-row[data-path="src/live.js"]').className, /selected/);
  assert.equal(d.activeElement, d.querySelector('.change-row[data-path="src/live.js"]'));
  liveFiles = [{ path: 'src/peer.js', status: '??' }];
  await tick();
  await settle();
  assert.equal(d.querySelector('.change-row.selected'), null);
  assert.match(d.querySelector('#inspector').textContent, /Select an action/);
});

test('grouped event paging stays manual and a failed page retries from the visible button', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.details.eventCount = 3;
  let secondReads = 0;
  data.events = (cursor) => {
    if (cursor === 0) return { items: [
      { hook_event_name: 'PostToolUse', session_id: 's', tool_name: 'Read' },
      { hook_event_name: 'PostToolUse', session_id: 's', tool_name: 'Bash' },
    ], nextCursor: 2 };
    secondReads += 1;
    if (secondReads === 1) throw new Error('synthetic page failure');
    return { items: [{ hook_event_name: 'SessionEnd', session_id: 's', reason: 'done' }], nextCursor: null };
  };
  let observed = 0;
  data.configure = (w) => {
    w.IntersectionObserver = class {
      constructor() {}
      observe() { observed += 1; }
      disconnect() {}
    };
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;
  d.querySelector('#timeline-tab-events').click();
  await settle();
  assert.equal(observed, 0, 'collapsed summaries do not register an eager paging sentinel');
  const allEvents = d.querySelector('#all-events-toggle');
  allEvents.checked = true;
  allEvents.dispatchEvent(new dom.window.Event('change', { bubbles: true }));
  assert.equal(observed, 1, 'the exhaustive view retains the existing observer paging path');
  assert.ok(d.querySelector('.stream-sentinel'));
  allEvents.checked = false;
  allEvents.dispatchEvent(new dom.window.Event('change', { bubbles: true }));
  assert.equal(d.querySelector('.stream-sentinel'), null);
  let group = d.querySelector('details.event-group');
  group.open = true;
  group.dispatchEvent(new dom.window.Event('toggle'));
  let selected = group.querySelector('.event-row[data-index="1"]');
  selected.click();
  selected.focus();
  let more = d.querySelector('.stream-tail .load-more');
  assert.equal(more.classList.contains('hidden'), false);
  more.click();
  await settle();
  assert.match(d.querySelector('.stream-error').textContent, /synthetic page failure/);
  more = d.querySelector('.stream-tail .load-more');
  assert.equal(more.classList.contains('hidden'), false);
  more.click();
  await settle();
  assert.equal(secondReads, 2);
  assert.equal(d.querySelector('.stream-error'), null);
  assert.equal(d.querySelectorAll('.event-row').length, 3);
  group = d.querySelector('details.event-group');
  selected = group.querySelector('.event-row[data-index="1"]');
  assert.equal(group.open, true);
  assert.match(selected.className, /selected/);
  assert.equal(d.activeElement, selected);
});

test('a delayed load-more response cannot replace a newer run generation', async (t) => {
  const html = await readFile(new URL('index.html', here), 'utf8');
  const app = await readFile(new URL('app.js', here), 'utf8');
  const dom = new JSDOM(html, { url: 'http://127.0.0.1:7777/', runScripts: 'outside-only' });
  t.after(() => dom.window.close());
  const { window } = dom;
  const base = fixture('completed', 'pass', 'PASS');
  const oldFirst = { ...base.list.runs[0], id: '20260903T000000.000000000Z-00000001' };
  const oldSecond = { ...oldFirst, id: '20260902T000000.000000000Z-00000002' };
  const oldThird = { ...oldFirst, id: '20260901T000000.000000000Z-00000003' };
  const newFirst = { ...oldFirst, id: '20260904T000000.000000000Z-00000004' };
  let poll;
  let changed = false;
  let resolveMore;
  window.setInterval = (callback, delay) => {
    if (delay === 5000) poll = callback;
    return 1;
  };
  window.clearInterval = () => {};
  window.IntersectionObserver = class { observe() {} disconnect() {} };
  window.HTMLElement.prototype.scrollIntoView = () => {};
  window.fetch = (input) => {
    const url = new URL(String(input), window.location.href);
    if (url.pathname === '/api/shadow') return response({ allowRun: false });
    if (url.pathname === '/api/runs' && url.searchParams.has('cursor')) {
      return new Promise((resolve) => { resolveMore = resolve; });
    }
    if (url.pathname === '/api/runs') {
      return changed
        ? response({ runs: [newFirst, oldFirst], pageIds: [newFirst.id, oldFirst.id], total: 2, generation: 'g2' })
        : response({ runs: [oldFirst, oldSecond], pageIds: [oldFirst.id, oldSecond.id], total: 3, nextCursor: 'g1-cursor', generation: 'g1' });
    }
    if (url.pathname.startsWith('/api/runs/')) return response({ ...base.details, run: { ...base.details.run, id: oldFirst.id } });
    if (url.pathname.includes('/actions')) return response({ items: [], nextCursor: null });
    if (url.pathname.includes('/changes')) return response({ files: [], nextCursor: null, total: 0 });
    throw new Error(`unexpected fetch ${url}`);
  };
  window.eval(app);
  for (let i = 0; i < 6; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  window.document.querySelector('#run-load-more').click();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.ok(resolveMore);
  changed = true;
  await poll();
  resolveMore(await response({ runs: [oldThird], pageIds: [oldThird.id], total: 3, generation: 'g1' }));
  for (let i = 0; i < 3; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  assert.deepEqual([...window.document.querySelectorAll('.run-item')].map((node) => node.dataset.runId), [newFirst.id, oldFirst.id]);
});

// --- Loaded-run overview (DESIGN.md section 11) ---

function overviewFixture(runs) {
  const data = fixture('completed', 'pass', 'PASS');
  const base = data.list.runs[0];
  const detail = data.details;
  data.list.runs = runs.map((run, index) => ({ ...base, id: `run-${index}`, ...run }));
  data.list.total = data.list.runs.length;
  data.list.generation = 'same';
  data.details = (id) => ({ ...detail, run: { ...detail.run, id } });
  return data;
}

const overviewGroups = (d, kind) =>
  Array.from(d.querySelectorAll(`#run-overview [data-overview-kind="${kind}"] .overview-group`), (node) => [
    node.querySelector('.overview-group-name').textContent,
    node.querySelector('.overview-group-count').textContent,
  ]);

test('overview counts only loaded runs and keeps every recorded verification value', async (t) => {
  const data = overviewFixture([
    { provider: 'claude', verification: 'PASS', project: 'agentrec' },
    { provider: 'claude', verification: 'FAIL', project: 'agentrec' },
    { provider: 'codex', verification: 'PASS', project: 'etf-trading' },
    { provider: 'codex', verification: 'TAINTED', project: 'etf-trading' },
    { provider: 'codex', verification: 'NOT RUN', project: 'hermes-sustain' },
    { provider: 'codex', verification: 'PENDING', project: 'hermes-sustain' },
  ]);
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;

  const overview = d.querySelector('#run-overview');
  assert.ok(overview, 'the sidebar offers a loaded-run overview');
  assert.equal(overview.classList.contains('hidden'), false);
  // It costs one row until asked for, beside the list it describes.
  assert.equal(overview.tagName, 'DETAILS');
  assert.equal(overview.open, false);
  assert.ok(d.querySelector('.sidebar #run-overview'), 'the overview sits with the run list');

  assert.deepEqual(overviewGroups(d, 'provider'), [['claude', '2'], ['codex', '4']]);
  // Recorded verification values survive verbatim: no residual bucket, no re-ranking.
  assert.deepEqual(overviewGroups(d, 'verification'), [
    ['FAIL', '1'], ['NOT RUN', '1'], ['PASS', '2'], ['PENDING', '1'], ['TAINTED', '1'],
  ]);
  assert.deepEqual(overviewGroups(d, 'project'), [['agentrec', '2'], ['etf-trading', '2'], ['hermes-sustain', '2']]);

  const total = overview.querySelector('#run-overview-scope').textContent;
  assert.match(total, /6/);
  // A fully loaded store must not imply unloaded runs exist.
  assert.doesNotMatch(total, /more/i);
});

test('overview says the count is partial while more runs remain unloaded', async (t) => {
  const data = overviewFixture([
    { provider: 'claude', verification: 'PASS', project: 'agentrec' },
    { provider: 'codex', verification: 'FAIL', project: 'agentrec' },
  ]);
  data.list.total = 34;
  data.list.nextCursor = 'page-two';
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;

  const scope = d.querySelector('#run-overview-scope').textContent;
  assert.match(scope, /2 loaded/);
  assert.match(scope, /34 recorded/);
  // Load more stays the only way to widen the scope.
  assert.equal(d.querySelector('#run-load-more').classList.contains('hidden'), false);
});

test('overview groups drive the existing run-list filters instead of a separate view', async (t) => {
  const data = overviewFixture([
    { provider: 'claude', verification: 'PASS', project: 'agentrec' },
    { provider: 'codex', verification: 'FAIL', project: 'etf-trading' },
    { provider: 'codex', verification: 'PASS', project: 'etf-trading' },
  ]);
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;

  const codex = Array.from(d.querySelectorAll('#run-overview [data-overview-kind="project"] .overview-group'))
    .find((node) => node.querySelector('.overview-group-name').textContent === 'etf-trading');
  assert.ok(codex, 'each group is reachable');
  assert.equal(codex.tagName, 'BUTTON', 'groups are keyboard-reachable native controls');

  codex.click();
  await settle();

  assert.equal(d.querySelector('#run-project-filter').value, 'etf-trading');
  assert.deepEqual(runIDs(d), ['run-1', 'run-2']);
  assert.equal(new w.URL(w.location.href).searchParams.get('project'), 'etf-trading');
});

test('overview survives run selection and stays hidden for an empty store', async (t) => {
  const data = overviewFixture([{ provider: 'claude', verification: 'PASS', project: 'agentrec' }]);
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;
  // The viewer auto-selects a run, so the overview must coexist with the run view.
  assert.equal(d.querySelector('#run-view').classList.contains('hidden'), false);
  assert.equal(d.querySelector('#run-overview').classList.contains('hidden'), false);
  assert.deepEqual(overviewGroups(d, 'provider'), [['claude', '1']]);

  const emptyStore = fixture('completed', 'pass', 'PASS');
  emptyStore.list = { runs: [], total: 0, unreadable: 0 };
  const emptyDom = await renderFixture(emptyStore);
  t.after(() => emptyDom.window.close());
  const e = emptyDom.window.document;
  assert.equal(e.querySelector('#run-overview').classList.contains('hidden'), true);
  assert.equal(e.querySelector('#workspace-empty-title').textContent, 'No runs recorded yet');
});

test('overview is localized without translating recorded status values', async (t) => {
  const data = overviewFixture([
    { provider: 'claude', verification: 'PASS', project: 'agentrec' },
    { provider: 'codex', verification: 'NOT RUN', project: 'agentrec' },
  ]);
  data.configure = (w) => w.localStorage.setItem('agentrec.lang', 'ko');
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;

  const heading = d.querySelector('#run-overview > summary').textContent;
  assert.match(heading, /[가-힣]/, 'the overview heading is localized');
  // Recorded values are identifiers, not prose.
  assert.deepEqual(overviewGroups(d, 'verification'), [['NOT RUN', '1'], ['PASS', '1']]);
});

test('overview stays consistent after delete, undo, and a metadata-only poll', async (t) => {
  const data = overviewFixture([
    { provider: 'claude', verification: 'PASS', project: 'agentrec' },
    { provider: 'codex', verification: 'FAIL', project: 'etf-trading' },
  ]);
  let list = data.list;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  // renderFixture installs its own fetch after configure; wrap it afterwards so
  // the list can change under the page and restore has a route.
  w.fetch = ((original) => (input, init) => {
    const url = new w.URL(String(input), w.location.href);
    if (url.pathname === '/api/runs') return response(list);
    if (init && init.method === 'POST' && /\/restore$/.test(url.pathname)) return Promise.resolve({ ok: true, status: 204, json: async () => ({}) });
    return original(input, init);
  })(w.fetch);
  const scope = () => d.querySelector('#run-overview-scope').textContent;
  assert.equal(scope(), '2 loaded run(s)');

  // Delete through the page's own path: the store shrank with the page, so the
  // count must not claim an unloaded run exists behind a hidden Load more.
  list = { ...data.list, runs: data.list.runs.slice(1), total: 1 };
  [...d.querySelectorAll('button')].find((b) => /^Delete/i.test(b.textContent)).click();
  await settle();
  [...d.querySelectorAll('button')].find((b) => /^Delete/i.test(b.textContent)).click();
  await settle();
  assert.equal(d.querySelectorAll('#run-list .run-item').length, 1);
  assert.equal(scope(), '1 loaded run(s)');
  assert.doesNotMatch(scope(), /load more/i);

  // Undo restores the summary and the count together.
  list = data.list;
  [...d.querySelectorAll('button')].find((b) => /^Undo$/i.test(b.textContent)).click();
  await settle();
  assert.equal(scope(), '2 loaded run(s)');
  assert.deepEqual(overviewGroups(d, 'provider'), [['claude', '1'], ['codex', '1']]);

  // A poll whose page content is unchanged but whose total shrank must not
  // leave the overview describing the old store.
  list = { ...data.list, total: 2, nextCursor: '' };
  d.dispatchEvent(new w.Event('visibilitychange'));
  await settle();
  assert.equal(scope(), '2 loaded run(s)');
});

test('overview group activation keeps focus and remembers the project like the select does', async (t) => {
  const data = overviewFixture([
    { provider: 'claude', verification: 'PASS', project: 'agentrec' },
    { provider: 'codex', verification: 'FAIL', project: 'etf-trading' },
  ]);
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  const group = [...d.querySelectorAll('[data-overview-kind="project"] .overview-group')]
    .find((n) => n.querySelector('.overview-group-name').textContent === 'etf-trading');
  group.focus();
  group.click();
  await settle();
  // The list re-renders, but the reader's keyboard position must survive on the
  // same group, not fall to BODY.
  const active = d.activeElement;
  assert.ok(active && active.classList.contains('overview-group'), `focus landed on ${active && active.tagName}`);
  assert.equal(active.querySelector('.overview-group-name').textContent, 'etf-trading');
  assert.equal(w.localStorage.getItem('agentrec.project'), 'etf-trading');
});

// --- Discoverable later verification (DESIGN.md section 12) ---


test('verification block tells a read-only viewer how to verify later, and only then', async (t) => {
  // Default fixture answers /api/shadow with allowRun:false.
  const dom = await renderFixture(fixture('completed', 'pass', 'PASS'));
  t.after(() => dom.window.close());
  const d = dom.window.document;
  assert.equal(d.querySelector('#verify-now'), null, 'no Verify now without --allow-run');
  const hint = d.querySelector('#verify-later-hint');
  assert.ok(hint, 'the Verification block explains how to verify later');
  assert.match(hint.textContent, /agentrec start --allow-run/);
  assert.match(hint.textContent, /agentrec verify/);
  // Commands are verbatim code, not prose.
  assert.ok([...hint.querySelectorAll('code')].some((c) => c.textContent === 'agentrec start --allow-run'));
  // The hint is about the viewer's permission, never the run's verdict.
  const verdict = d.querySelector('#run-verdict').textContent;
  assert.doesNotMatch(hint.textContent, new RegExp(verdict));
});

test('verification hint yields to Verify now when running is allowed', async (t) => {
  const dom = await renderFixture(fixture('completed', 'pass', 'PASS'));
  t.after(() => dom.window.close());
  const w = dom.window;
  // Override the shadow answer after renderFixture installs its fetch.
  const original = w.fetch;
  w.fetch = (input, init) => {
    const url = new w.URL(String(input), w.location.href);
    if (url.pathname === '/api/shadow') return response({ allowRun: true, runners: [], jobs: [] });
    return original(input, init);
  };
  w.document.querySelector('#compare-open').click();
  await settle();
  w.document.querySelector('#run-list .run-item').click();
  await settle();
  const d = w.document;
  assert.ok(d.querySelector('#verify-now'), 'Verify now renders when allowed');
  assert.equal(d.querySelector('#verify-later-hint'), null, 'no hint once running is allowed');
});

test('live runs show neither Verify now nor the later-verification hint', async (t) => {
  const dom = await renderFixture(fixture('running', 'running', 'running'));
  t.after(() => dom.window.close());
  const d = dom.window.document;
  assert.equal(d.querySelector('#verify-now'), null);
  assert.equal(d.querySelector('#verify-later-hint'), null);
});

test('later-verification hint is localized with verbatim commands', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.configure = (w) => w.localStorage.setItem('agentrec.lang', 'ko');
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const hint = dom.window.document.querySelector('#verify-later-hint');
  assert.ok(hint);
  assert.match(hint.textContent, /[가-힣]/);
  assert.match(hint.textContent, /agentrec start --allow-run/);
});

test('later-verification hint never pastes a shell-unsafe run id', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  // Reaches the client only if the server accepted it; the guard is defence in depth.
  // A tilde survives URL paths verbatim yet expands in a shell.
  const unsafe = 'run~x';
  data.list.runs[0].id = unsafe;
  data.details.run.id = unsafe;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;
  const codes = [...d.querySelectorAll('#verify-later-hint code')].map((c) => c.textContent);
  assert.deepEqual(codes, ['agentrec start --allow-run', 'agentrec verify <run-id>']);
  assert.doesNotMatch(d.querySelector('#verify-later-hint').textContent, /run~x|\{restart\}|\{cli\}/);
});

// --- Duration on the run row (DESIGN.md section 13) ---

test('run row shows a compact duration beside the relative time, never for open runs', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const base = data.list.runs[0];
  data.list.runs = [
    { ...base, id: 'run-short', durationMillis: 6192 },
    { ...base, id: 'run-minutes', durationMillis: 14 * 60 * 1000 + 3000 },
    { ...base, id: 'run-hours', durationMillis: 72 * 60 * 1000 + 15 * 1000 },
    { ...base, id: 'run-whole-hour', durationMillis: 3600 * 1000 },
    { ...base, id: 'run-fraction', durationMillis: 20 * 60 * 1000 + 9616 },
    { ...base, id: 'run-open', exit: 'running', exitReason: 'running', statusClass: 'running', statusLabel: 'running' },
    { ...base, id: 'run-unknown' },
  ];
  data.list.total = data.list.runs.length;
  data.details = (id) => ({ ...data.details, run: { ...data.details.run, id } });
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;
  const durationOf = (id) => {
    const el = d.querySelector(`.run-item[data-run-id="${id}"] .run-duration`);
    return el ? [el.textContent, el.title] : null;
  };
  assert.deepEqual(durationOf('run-short'), ['6s', '6.192s']);
  assert.deepEqual(durationOf('run-minutes'), ['14m', '14m3s']);
  assert.deepEqual(durationOf('run-hours'), ['1h 12m', '1h12m15s']);
  assert.deepEqual(durationOf('run-whole-hour'), ['1h', '1h0m0s']);
  // The token rounds to what fits; the title keeps every recorded millisecond.
  assert.deepEqual(durationOf('run-fraction'), ['20m', '20m9.616s']);
  // Open and duration-less runs show nothing: no 0s, no dash.
  assert.equal(durationOf('run-open'), null);
  assert.equal(durationOf('run-unknown'), null);
  // Placement: after the relative time inside the meta line.
  const meta = d.querySelector('.run-item[data-run-id="run-short"] .run-item-meta');
  const kids = [...meta.children].map((n) => n.className);
  assert.ok(kids.indexOf('run-duration') > kids.indexOf('run-time'));
});

test('run row duration is localized without changing the exact title', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs[0].durationMillis = 90 * 1000;
  data.configure = (w) => w.localStorage.setItem('agentrec.lang', 'ko');
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const el = dom.window.document.querySelector('.run-item .run-duration');
  assert.ok(el);
  assert.match(el.textContent, /1분/);
  assert.equal(el.title, '1m30s');
});

test('run row keeps relative time visible and exposes its exact recorded start', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs[0].startedAt = '2026-09-03T00:00:00Z';
  data.configure = (w) => w.localStorage.setItem('agentrec.lang', 'ko');
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const el = dom.window.document.querySelector('.run-item .run-time');
  const exact = new Date(data.list.runs[0].startedAt).toLocaleString('ko');
  assert.equal(el.tagName, 'TIME');
  assert.match(el.textContent, /전$/);
  assert.equal(el.dateTime, '2026-09-03T00:00:00.000Z');
  assert.equal(el.title, exact);
  assert.equal(el.getAttribute('aria-label'), `시작: ${exact}`);

  const lang = dom.window.document.querySelector('#lang');
  lang.value = 'en';
  lang.dispatchEvent(new dom.window.Event('change'));
  const localized = dom.window.document.querySelector('.run-item .run-time');
  const englishExact = new Date(data.list.runs[0].startedAt).toLocaleString('en');
  assert.equal(localized.title, englishExact);
  assert.equal(localized.getAttribute('aria-label'), `Started: ${englishExact}`);
});

test('run row does not expose the absent zero-time sentinel as an exact instant', async (t) => {
  let poll;
  const data = fixture('completed', 'pass', 'PASS');
  data.list.runs[0].startedAt = '0001-01-01T00:00:00Z';
  data.configure = (w) => {
    w.setInterval = (callback, delay) => { if (delay === 5000) poll = callback; return delay; };
    w.clearInterval = () => {};
  };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const el = dom.window.document.querySelector('.run-item .run-time');
  assert.equal(el.tagName, 'SPAN');
  assert.equal(el.textContent, 'unknown');
  assert.equal(el.hasAttribute('datetime'), false);
  assert.equal(el.hasAttribute('title'), false);
  assert.equal(el.hasAttribute('aria-label'), false);
  await poll();
  assert.equal(dom.window.document.querySelector('.run-item .run-time').textContent, 'unknown');
});

// --- Skip links (DESIGN.md section 14) ---

test('skip links are the first tab stops and hand focus to the run list and run view', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;

  const links = [...d.querySelectorAll('a.skip-link')];
  assert.equal(links.length, 2, 'two skip links');
  assert.equal(links[0].getAttribute('href'), '#run-list');
  assert.equal(links[1].getAttribute('href'), '#run-view');
  // They must precede every other focusable element in document order.
  const firstFocusable = d.querySelector('a[href], button, input, select, summary, [tabindex]:not([tabindex="-1"])');
  assert.equal(firstFocusable, links[0], 'skip link is the first focusable element');

  // Activating the first link puts focus inside the run list so the next Tab is a run row.
  links[0].click();
  await settle();
  assert.equal(d.activeElement.id, 'run-list', `focus landed on ${d.activeElement.id || d.activeElement.tagName}`);
  assert.equal(d.querySelector('#run-list').getAttribute('tabindex'), '-1', 'target is programmatically focusable only');

  links[1].click();
  await settle();
  assert.equal(d.activeElement.id, 'run-view');
});

test('skip links are localized', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.configure = (w) => w.localStorage.setItem('agentrec.lang', 'ko');
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const [a, b] = [...dom.window.document.querySelectorAll('a.skip-link')].map((n) => n.textContent);
  assert.match(a, /[가-힣]/);
  assert.match(b, /[가-힣]/);
  assert.notEqual(a, b);
});

// --- The agent's last message beside the request (DESIGN.md section 15) ---

test('run detail shows the last agent message as a disclosure beside the request', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.details.actionCount = 4;
  data.details.lastAgentMessage = { actionId: 'm2', position: 3, offset: 0, text: 'Closing report: done.\nSecond line.', truncated: false };
  data.actions = [
    { id: 'a1', type: 'file.read', status: 'completed', input: { file_path: 'x' } },
    { id: 'm1', type: 'agent.message', status: 'completed', input: { text: 'first' } },
    { id: 'm2', type: 'agent.message', status: 'completed', input: { text: 'Closing report: done.\nSecond line.' } },
    { id: 'a4', type: 'file.read', status: 'completed', input: { file_path: 'y' } },
  ];
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;

  const card = d.querySelector('#reply-disclosure');
  assert.ok(card, 'a last-message disclosure exists');
  assert.equal(card.tagName, 'DETAILS');
  assert.equal(card.open, false, 'collapsed like the request card');
  assert.equal(card.classList.contains('hidden'), false);
  // Same shape and neighbour as the request card.
  assert.equal(card.previousElementSibling.id, 'request-disclosure');
  assert.match(d.querySelector('#reply-preview').textContent, /^Closing report: done\. Second line\./);
  assert.equal(d.querySelector('#run-reply').textContent, 'Closing report: done.\nSecond line.');
  // Whose words, and where they sit: not the final action here.
  const label = d.querySelector('#reply-disclosure .section-label').textContent;
  assert.match(label, /claude/i, 'label names the provider whose message it is');
  assert.match(d.querySelector('#reply-position').textContent, /3 of 4/);

  // The link opens the same action the timeline shows.
  const link = d.querySelector('#reply-action-link');
  assert.ok(link);
  link.click();
  await settle();
  const selected = d.querySelector('.action-row.selected');
  assert.ok(selected, 'the linked action is selected in the timeline');
  assert.equal(selected.dataset.index, '2', 'm2 is the third loaded action');
  // It went through the same deep-link path search hits use, so the URL is an evidence link.
  const url = new w.URL(w.location.href);
  assert.equal(url.searchParams.get('focus'), 'actions');
  assert.equal(url.searchParams.get('action'), 'm2');
  assert.equal(url.searchParams.get('actionCursor'), '0');
});

test('last-message card is absent without one and hidden for live runs', async (t) => {
  const none = fixture('completed', 'pass', 'PASS');
  const dom = await renderFixture(none);
  t.after(() => dom.window.close());
  assert.equal(dom.window.document.querySelector('#reply-disclosure').classList.contains('hidden'), true);

  const live = fixture('running', 'running', 'running');
  live.details.lastAgentMessage = { actionId: 'm1', position: 1, text: 'working…', truncated: false };
  const liveDom = await renderFixture(live);
  t.after(() => liveDom.window.close());
  assert.equal(liveDom.window.document.querySelector('#reply-disclosure').classList.contains('hidden'), true);
});

test('truncated last message says so and is localized', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.details.actionCount = 1;
  data.details.lastAgentMessage = { actionId: 'm1', position: 1, text: 'cut', truncated: true };
  data.configure = (w) => w.localStorage.setItem('agentrec.lang', 'ko');
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const d = dom.window.document;
  assert.match(d.querySelector('#reply-disclosure .section-label').textContent, /[가-힣]/);
  assert.ok(d.querySelector('#reply-truncated'), 'truncation is stated');
  assert.equal(d.querySelector('#reply-truncated').classList.contains('hidden'), false);
});

// --- Hook lifecycle folding in the event summary (DESIGN.md section 16) ---

test('Event summary folds consecutive system hook lifecycle records and keeps everything else apart', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const hook = (subtype, name, extra = {}) => ({ type: 'system', subtype, hook_name: name, session_id: 's1', ...extra });
  const first = [
    { type: 'system', subtype: 'init', session_id: 's1' },
    hook('hook_started', 'SessionStart:startup'),
    hook('hook_response', 'SessionStart:startup'),
    hook('hook_started', 'PreToolUse:Bash'),
    hook('hook_progress', 'PreToolUse:Bash'),
    hook('hook_response', 'PreToolUse:Bash'),
    { type: 'system', subtype: 'thinking_tokens', session_id: 's1', count: 12 },
    { type: 'assistant', session_id: 's1', message: { content: [{ type: 'text', text: 'working' }] } },
    hook('hook_started', 'PostToolUse:Bash'),
    hook('hook_response', 'PostToolUse:Bash', { error: 'hook failed' }),
    hook('hook_started', 'Stop'),
    { type: 'system', subtype: 'task_notification', session_id: 's1' },
    hook('hook_started', 'SessionEnd'),
  ];
  const second = [
    hook('hook_response', 'SessionEnd'),
    hook('hook_started', 'SessionEnd'),
    { hook_event_name: 'PostToolUse', session_id: 's1', tool_name: 'Bash' },
    { hook_event_name: 'PostToolUse', session_id: 's1', tool_name: 'Read' },
    { type: 'system', subtype: 'hook_started', hook_name: 'Stop', session_id: 's2' },
  ];
  data.details.eventCount = first.length + second.length;
  data.events = (cursor) => cursor === 0
    ? { items: first, nextCursor: first.length }
    : { items: second, nextCursor: null };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document: d } = dom.window;
  d.querySelector('#timeline-tab-events').click();
  await settle();

  const groups = () => [...d.querySelectorAll('#timeline > details.event-group')];
  const indexesOf = (group) => [...group.querySelectorAll('.event-row')].map((row) => row.dataset.index);
  let hookGroups = groups().filter((g) => g.classList.contains('hook-group'));
  assert.equal(hookGroups.length, 1, 'one hook lifecycle group on the first page');
  assert.deepEqual(indexesOf(hookGroups[0]), ['1', '2', '3', '4', '5', '6'], 'init before and assistant after keep the span exact; thinking_tokens folds');
  assert.equal(hookGroups[0].querySelector('.action-group-kinds').textContent, '6 hook lifecycle records');
  assert.equal(hookGroups[0].querySelector('.action-group-meta').textContent, 'hook_started 2 · hook_response 2 · hook_progress 1 · thinking_tokens 1 · loaded page');
  assert.doesNotMatch(hookGroups[0].querySelector('summary').textContent, /success|passed|completed|\bok\b/i);
  assert.doesNotMatch(hookGroups[0].querySelector('summary').textContent, /PreToolUse|SessionStart/, 'hook names stay in the records, not the summary');
  const lone = [...d.querySelectorAll('#timeline > .event-row')];
  assert.ok(lone.some((row) => row.dataset.index === '0'), 'system init stays its own row');
  assert.ok(lone.some((row) => row.dataset.index === '9'), 'a hook record with an error field stays its own row');
  assert.ok(lone.some((row) => row.dataset.index === '10'), 'a single hook record after the error is not a group');
  assert.ok(lone.some((row) => row.dataset.index === '11'), 'task_notification stays its own row');
  assert.ok(lone.some((row) => row.dataset.index === '12'), 'the last record of the page does not fold with the next page');
  assert.equal(d.querySelectorAll('#timeline .event-row').length, first.length, 'every loaded record is still in the DOM');

  d.querySelector('.stream-tail .load-more').click();
  await settle();
  hookGroups = groups().filter((g) => g.classList.contains('hook-group'));
  assert.equal(hookGroups.length, 2, 'the next page starts its own hook group');
  assert.deepEqual(indexesOf(hookGroups[1]), ['13', '14'], 'session s2 record and the PostToolUse pair stay out');
  const toolGroups = groups().filter((g) => !g.classList.contains('hook-group'));
  assert.equal(toolGroups.length, 1);
  assert.deepEqual(indexesOf(toolGroups[0]), ['15', '16'], 'PostToolUse folding is unchanged');
  assert.equal(d.querySelectorAll('#timeline .event-row').length, first.length + second.length);

  hookGroups[0].querySelector('.event-row[data-index="4"]').click();
  assert.match(d.querySelector('#inspector').textContent, /event #5/);
  assert.match(d.querySelector('#inspector').textContent, /"hook_name": "PreToolUse:Bash"/, 'the original record is one click away');

  d.querySelector('#all-events-toggle').click();
  await settle();
  assert.equal(d.querySelector('details.event-group'), null, 'All events is untouched');
  assert.equal(d.querySelectorAll('#timeline > .event-row').length, first.length + second.length);
});

test('hook lifecycle group summary is localized', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const hook = (subtype) => ({ type: 'system', subtype, hook_name: 'Stop', session_id: 's1' });
  data.events = () => ({ items: [hook('hook_started'), hook('hook_response')], nextCursor: null });
  data.details.eventCount = 2;
  const dom = await renderFixture({ ...data, configure: (w) => w.localStorage.setItem('agentrec.lang', 'ko') });
  t.after(() => dom.window.close());
  const { document: d } = dom.window;
  d.querySelector('#timeline-tab-events').click();
  await settle();
  const summary = d.querySelector('#timeline > details.hook-group summary');
  assert.ok(summary);
  assert.equal(summary.querySelector('.action-group-kinds').textContent, '훅 수명주기 기록 2건');
  assert.match(summary.querySelector('.action-group-meta').textContent, /^hook_started 1 · hook_response 1 · /, 'subtype tokens stay verbatim');
});

// --- Codex patch rows name their files (DESIGN.md section 17) ---

test('a codex apply_patch row lists its file headers instead of the patch preamble', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const patch = [
    '*** Begin Patch',
    '*** Update File: internal/cli/ui_assets/app.js',
    '@@',
    '-old',
    '+new',
    '*** Add File: internal/cli/ui_assets/new.css',
    '+body {}',
    '*** Update File: README.md',
    '@@',
    '-a',
    '+b',
    '*** End Patch',
  ].join('\n');
  data.actions = [
    { id: 'p1', type: 'file.edit', provider: 'codex', status: 'completed', startedAt: '2026-09-03T00:00:01Z', input: { command: patch } },
    { id: 'p2', type: 'file.edit', provider: 'codex', status: 'completed', startedAt: '2026-09-03T00:00:02Z', input: { command: '*** Begin Patch\n*** End Patch' } },
    { id: 's1', type: 'shell.exec', provider: 'codex', status: 'completed', startedAt: '2026-09-03T00:00:03Z', input: { command: 'go test ./...' } },
  ];
  data.details.actionCount = 3;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document: d } = dom.window;
  d.querySelector('#all-actions-toggle')?.click();
  await settle();
  const summary = (index) => d.querySelector(`.action-row[data-index="${index}"] .action-summary`).textContent;
  assert.equal(summary(0), 'Update File: internal/cli/ui_assets/app.js · Add File: internal/cli/ui_assets/new.css · Update File: README.md');
  assert.equal(summary(1), '*** Begin Patch *** End Patch', 'a patch without file headers keeps the plain detail');
  assert.equal(summary(2), 'go test ./...', 'other commands are unchanged');
  // Long absolute paths must not cut a header in half: whole headers, then a count of the rest.
  const long = (n) => `*** Update File: /Users/someone/code/project-${n}/internal/cli/ui_assets/app.test.js`;
  data.actions = [{ id: 'p3', type: 'file.edit', provider: 'codex', status: 'completed', startedAt: '2026-09-03T00:00:04Z', input: { command: ['*** Begin Patch', long(1), '@@', long(2), '@@', long(3), '@@', long(4), '@@', '*** End Patch'].join('\n') } }];
  data.details.actionCount = 1;
  const again = await renderFixture(data);
  t.after(() => again.window.close());
  again.window.document.querySelector('#all-actions-toggle').click();
  await settle();
  const cut = again.window.document.querySelector('.action-row[data-index="0"] .action-summary').textContent;
  assert.match(cut, /^Update File: \/Users\/someone\/code\/project-1\/internal\/cli\/ui_assets\/app\.test\.js · Update File: \/Users\/someone\/code\/project-2\/internal\/cli\/ui_assets\/app\.test\.js · \+2 files$/);
  assert.ok(cut.length <= 180);
  // Review findings: an oversized first header still keeps the count inside the row; CRLF documents do not leak '\r'.
  const huge = `*** Update File: /${'x'.repeat(190)}`;
  data.actions = [
    { id: 'p4', type: 'file.edit', provider: 'codex', status: 'completed', startedAt: '2026-09-03T00:00:05Z', input: { command: ['*** Begin Patch', huge, long(2), long(3), '*** End Patch'].join('\n') } },
    { id: 'p5', type: 'file.edit', provider: 'codex', status: 'completed', startedAt: '2026-09-03T00:00:06Z', input: { command: '*** Begin Patch\r\n*** Update File: a.txt\r\n*** End Patch\r\n' } },
  ];
  data.details.actionCount = 2;
  const third = await renderFixture(data);
  t.after(() => third.window.close());
  third.window.document.querySelector('#all-actions-toggle').click();
  await settle();
  const oversized = third.window.document.querySelector('.action-row[data-index="0"] .action-summary').textContent;
  assert.equal(oversized.length, 180);
  assert.match(oversized, / · \+2 files$/);
  assert.equal(third.window.document.querySelector('.action-row[data-index="1"] .action-summary').textContent, 'Update File: a.txt');
  // Search still covers the hunk text, not only the headers.
  d.querySelector('#timeline-search').value = '+new';
  d.querySelector('#timeline-search').dispatchEvent(new dom.window.Event('input', { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 250)); // past the 180 ms debounce
  await settle();
  assert.deepEqual([...d.querySelectorAll('.action-row')].map((row) => row.dataset.index), ['0']);
});

// --- A compact summary strip (DESIGN.md section 18) ---

test('the summary strip keeps four cards and leaves the counts to the tab labels', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.details.actionCount = 42;
  data.details.eventCount = 44;
  data.details.run.warningCount = 2;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document: d } = dom.window;
  const labels = [...d.querySelectorAll('#metrics .metric .metric-label')].map((n) => n.textContent);
  assert.deepEqual(labels, ['Process outcome', 'Verification verdict', 'Repository evidence', 'Warnings']);
  assert.match(d.querySelector('#timeline-tab-actions').textContent, /Actions\s+42/);
  assert.match(d.querySelector('#timeline-tab-events').textContent, /Provider events\s+44/);
  const warnings = metricByLabel(d, 'Warnings');
  assert.match(warnings.className, /\bwarn\b/);
  assert.equal(warnings.querySelector('.metric-value').textContent, '2');
  assert.match(css, /^\.metrics\s*\{[^}]*grid-template-columns:\s*repeat\(4,/m, 'four columns fill the strip on desktop');
});

// --- The timeline panel fits the screen it is on (DESIGN.md section 19) ---

test('desktop panels flex to the space left below the run context instead of guessing 340px', () => {
  // JSDOM has no layout; the contract is pinned in the stylesheet and measured in Chrome (DESIGN.md section 19).
  const desktop = css.slice(0, css.indexOf('@media (max-width: 1023px)'));
  assert.match(desktop, /^\.workspace\s*\{[^}]*display:\s*flex;[^}]*flex-direction:\s*column/m, 'the workspace is a column flex container');
  assert.match(desktop, /^#run-view\s*\{[^}]*flex:\s*1;[^}]*min-height:\s*0/m, 'the run view fills it');
  assert.match(desktop, /^\.content-grid\s*\{[^}]*flex:\s*1;[^}]*min-height:\s*0/m, 'the content grid fills the run view');
  assert.match(desktop, /^\.timeline-panel, \.inspector-panel\s*\{[^}]*min-height:\s*320px/m, 'a 320px floor');
  assert.doesNotMatch(desktop, /clamp\(520px, calc\(100vh - 340px\)/, 'the fixed guess is gone');
  const narrow = css.slice(css.indexOf('@media (max-width: 1023px)'));
  assert.match(narrow, /\.timeline-panel, \.inspector-panel\s*\{\s*height:\s*min\(70vh, 640px\);?\s*\}/, 'narrow layouts keep their height');
});

// --- The run heading preserves the loaded title (DESIGN.md section 20) ---

test('the run heading and sidebar preserve the complete loaded title', async (t) => {
  const cases = [
    'Test-maintenance task only. Add focused regression tests for exitReason.',
    'The request says “stop.” Then report what happened.',
    '調査を完了する。結果を報告する。',
    'Read AGENTS.md if present and README.md. Report the project purpose and current state.',
    'Implement the approved local Viewer UI/UX polish with no sentence boundary',
  ];
  for (const title of cases) {
    const data = fixture('completed', 'pass', 'PASS');
    data.list.runs[0].title = title;
    const dom = await renderFixture(data);
    t.after(() => dom.window.close());
    const heading = dom.window.document.querySelector('#run-title');
    assert.equal(heading.textContent, title);
    assert.equal(heading.title, '', 'the visible heading needs no duplicate tooltip');
    assert.equal(dom.window.document.querySelector('.run-item .run-title-text').textContent, title, 'the sidebar row keeps the full title');
  }
});

// --- Notification-shaped prompts do not assert an operator (DESIGN.md section 22) ---

test('a user.prompt with notification-shaped text uses a cautious shape label', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const at = (s) => `2026-09-03T00:00:0${s}Z`;
  const notification = '<task-notification>\n<task-id>abc123</task-id>\n<status>completed</status>\n<summary>Background task finished.</summary>\n</task-notification>';
  data.actions = [
    { id: 'p1', type: 'user.prompt', provider: 'claude', status: 'completed', startedAt: at(1), input: { prompt: 'Rewrite the resume.' } },
    { id: 'p2', type: 'user.prompt', provider: 'claude', status: 'completed', startedAt: at(2), input: { prompt: '  \n' + notification } },
    { id: 'p3', type: 'user.prompt', provider: 'claude', status: 'completed', startedAt: at(3), input: { prompt: 'Please ignore the <task-notification> above.' } },
    { id: 'm1', type: 'agent.message', provider: 'claude', status: 'completed', startedAt: at(4), input: { text: 'Done.' } },
  ];
  data.details.actionCount = 4;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document: d } = dom.window;
  const speakers = [...d.querySelectorAll('.conversation-row')].map((row) => [row.dataset.index, row.querySelector('.speaker').textContent, row.classList.contains('notification')]);
  assert.deepEqual(speakers, [['0', 'You', false], ['1', 'Notification-shaped prompt', true], ['2', 'You', false], ['3', 'claude', false]]);
  d.querySelector('.conversation-row[data-index="1"] .show-more').click();
  assert.match(d.querySelector('.conversation-row[data-index="1"] .speech').textContent, /<task-id>abc123<\/task-id>/, 'the text is shown verbatim once expanded');
  d.querySelector('.conversation-row[data-index="1"]').click();
  assert.match(d.querySelector('#inspector').textContent, /user\.prompt/, 'the record is still a user.prompt');
});

test('the notification-shaped prompt label is localized', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.actions = [{ id: 'p2', type: 'user.prompt', provider: 'claude', status: 'completed', startedAt: '2026-09-03T00:00:01Z', input: { prompt: '<task-notification>\n<status>completed</status>\n</task-notification>' } }];
  data.details.actionCount = 1;
  for (const [lang, label] of [['ko', '알림 형식 요청'], ['ja', '通知形式のプロンプト'], ['zh-CN', '通知格式提示']]) {
    const dom = await renderFixture({ ...data, configure: (w) => w.localStorage.setItem('agentrec.lang', lang) });
    t.after(() => dom.window.close());
    assert.equal(dom.window.document.querySelector('.conversation-row .speaker').textContent, label, lang);
  }
});

// --- Prompt rows say which turn they are (DESIGN.md section 23) ---

test('prompt rows carry their ordinal among the recorded prompts, exact per loaded page', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const at = (s) => `2026-09-03T00:00:${String(s).padStart(2, '0')}Z`;
  const prompt = (id, s, text, promptRank) => ({ id, type: 'user.prompt', provider: 'claude', status: 'completed', startedAt: at(s), input: { prompt: text }, promptRank });
  const reply = (id, s) => ({ id, type: 'agent.message', provider: 'claude', status: 'completed', startedAt: at(s), input: { text: 'ok' } });
  const first = [prompt('p1', 1, 'first request', 1), reply('m1', 2), prompt('p2', 3, '<task-notification>\n<status>completed</status>\n</task-notification>', 2)];
  const second = [reply('m2', 4), prompt('p3', 5, 'third request', 3), reply('m3', 6)];
  data.details.actionCount = 6;
  data.details.promptCount = 3;
  data.actions = (cursor) => cursor === 0 ? { items: first, nextCursor: 1000 } : { items: second, nextCursor: null };
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  const { document: d } = dom.window;
  const speakers = () => [...d.querySelectorAll('.conversation-row.prompt .speaker')].map((n) => n.textContent);
  assert.deepEqual(speakers(), ['You · 1 of 3', 'Notification-shaped prompt · 2 of 3'], 'first page: exact ranks, total from the record');
  d.querySelector('.stream-tail .load-more').click();
  await settle();
  assert.deepEqual(speakers(), ['You · 1 of 3', 'Notification-shaped prompt · 2 of 3', 'You · 3 of 3']);
});

test('an exact action link preserves record-wide prompt ranks from a nonzero cursor', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.details.actionCount = 5;
  data.details.promptCount = 4;
  data.actions = (cursor) => {
    assert.equal(cursor, 98765);
    return {
      items: [
        { id: '', type: 'user.prompt', provider: 'claude', status: 'completed', input: { prompt: 'second request' }, promptRank: 2 },
        { id: 'duplicate', type: 'user.prompt', provider: 'claude', status: 'completed', input: { prompt: 'third request' }, promptRank: 3 },
        { id: 'duplicate', type: 'user.prompt', provider: 'claude', status: 'completed', input: { prompt: 'fourth request' }, promptRank: 4 },
      ],
      nextCursor: null,
    };
  };
  data.configure = (w) => w.history.replaceState(null, '', `/?run=${data.details.run.id}&focus=actions&action=duplicate&actionCursor=98765`);

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());

  assert.deepEqual(
    [...dom.window.document.querySelectorAll('.conversation-row.prompt .speaker')].map((node) => node.textContent),
    ['You · 2 of 4', 'You · 3 of 4', 'You · 4 of 4'],
  );
});

test('a missing prompt-rank projection does not invent a page-local record rank', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.details.actionCount = 3;
  data.details.promptCount = 3;
  data.actions = () => ({
    items: [{ id: 'p3', type: 'user.prompt', provider: 'claude', status: 'completed', input: { prompt: 'third request' } }],
    nextCursor: null,
  });
  data.configure = (w) => w.history.replaceState(null, '', `/?run=${data.details.run.id}&focus=actions&action=p3&actionCursor=98765`);

  const dom = await renderFixture(data);
  t.after(() => dom.window.close());

  assert.equal(dom.window.document.querySelector('.conversation-row.prompt .speaker').textContent, 'You');
});

test('a single-prompt run shows no ordinal', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.actions = [{ id: 'p1', type: 'user.prompt', provider: 'claude', status: 'completed', startedAt: '2026-09-03T00:00:01Z', input: { prompt: 'only request' } }];
  data.details.actionCount = 1;
  data.details.promptCount = 1;
  const dom = await renderFixture(data);
  t.after(() => dom.window.close());
  assert.equal(dom.window.document.querySelector('.conversation-row.prompt .speaker').textContent, 'You');
});

test('the prompt ordinal is localized', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  data.actions = [
    { id: 'p1', type: 'user.prompt', provider: 'claude', status: 'completed', startedAt: '2026-09-03T00:00:01Z', input: { prompt: 'a' }, promptRank: 1 },
    { id: 'p2', type: 'user.prompt', provider: 'claude', status: 'completed', startedAt: '2026-09-03T00:00:02Z', input: { prompt: 'b' }, promptRank: 2 },
  ];
  data.details.actionCount = 2;
  data.details.promptCount = 2;
  for (const [lang, expected] of [['ko', '나 · 2 / 2'], ['ja', '自分 · 2 / 2'], ['zh-CN', '我 · 2 / 2']]) {
    const dom = await renderFixture({ ...data, configure: (w) => w.localStorage.setItem('agentrec.lang', lang) });
    t.after(() => dom.window.close());
    assert.equal([...dom.window.document.querySelectorAll('.conversation-row.prompt .speaker')].pop().textContent, expected, lang);
  }
});
