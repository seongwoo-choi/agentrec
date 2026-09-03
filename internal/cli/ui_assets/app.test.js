import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { JSDOM } from 'jsdom';

const here = new URL('.', import.meta.url);
const html = await readFile(new URL('index.html', here), 'utf8');
const app = await readFile(new URL('app.js', here), 'utf8');

const response = (body) => Promise.resolve({
  ok: true,
  status: 200,
  json: async () => body,
});

async function renderFixture({ list, details, actions = [], events = [] }) {
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
  window.fetch = (input) => {
    const url = new URL(String(input), window.location.href);
    if (url.pathname === '/api/shadow') return response({ allowRun: false });
    if (url.pathname === '/api/runs') return response(list);
    if (url.pathname === `/api/runs/${details.run.id}`) return response(details);
    if (url.pathname.includes('/actions')) return response({ items: actions, nextCursor: null });
    if (url.pathname.includes('/events')) return response({ items: events, nextCursor: null });
    if (url.pathname.includes('/changes')) return response({ files: [], nextCursor: null, total: 0 });
    throw new Error(`unexpected fetch ${url}`);
  };
  window.eval(app);
  for (let i = 0; i < 10; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
  return dom;
}

function fixture(exitReason, statusClass, statusLabel) {
  const startedAt = '2026-09-03T00:00:00Z';
  const run = {
    id: '20260903T000000.000000000Z-00000001',
    provider: 'claude',
    project: 'agentrec',
    cwd: '/repo',
    prompt: 'test',
    startedAt,
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
  assert.match(document.querySelector('.run-item .mini-status').className, /\bfail\b/);
  assert.match(document.querySelector('#run-verdict').className, /\bfail\b/);
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
  assert.match(document.querySelector('.run-item .mini-status').className, /\bwarn\b/);
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

test('initial run outside the first page is selected directly', async (t) => {
  const selected = fixture('completed', 'pass', 'PASS');
  selected.details.run.id = 'run-older-selected';
  selected.details.run.startedAt = '2024-01-01T00:00:00Z';
  selected.list.initialRunId = selected.details.run.id;
  selected.list.runs = [];
  selected.list.pageIds = ['run-newest-unreadable'];
  selected.list.unreadable = 1;
  selected.list.total = 55;
  selected.list.nextCursor = 'opaque';
  selected.list.generation = 'g1';
  const dom = await renderFixture(selected);
  t.after(() => dom.window.close());
  assert.equal(dom.window.document.querySelector('#run-title').textContent, selected.details.run.id);
});

test('run pages append explicitly and unchanged polls retain DOM nodes', async (t) => {
  const data = fixture('completed', 'pass', 'PASS');
  const first = data.details.run;
  const second = { ...first, id: '20260902T000000.000000000Z-00000002', project: 'second' };
  const third = { ...first, id: '20260901T000000.000000000Z-00000003', project: 'third' };
  const dom = new JSDOM(html, { runScripts: 'outside-only', url: 'http://localhost:42817/', pretendToBeVisual: true });
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
  const loadedFirstNode = window.document.querySelector('.run-item');
  assert.notEqual(loadedFirstNode, firstNode);
  assert.ok(poll);
  await poll();
  assert.equal(window.document.querySelectorAll('.run-item').length, 3);
  assert.equal(window.document.querySelector('.run-item'), loadedFirstNode);
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
