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

async function renderFixture({ list, details, actions = [], events = [], configure = () => {} }) {
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
  window.fetch = (input) => {
    const url = new URL(String(input), window.location.href);
    if (url.pathname === '/api/shadow') return response({ allowRun: false });
    if (url.pathname === '/api/runs') return response(list);
    const currentDetails = typeof details === 'function' ? details() : details;
    if (url.pathname === `/api/runs/${currentDetails.run.id}`) return response(currentDetails);
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
  assert.equal(cards.get('run-process-failed').querySelector('.run-verdict-verify').classList.contains('pass'), true);
  assert.equal(cards.get('run-verification-failed').querySelector('.run-verdict-run').classList.contains('pass'), true);
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
