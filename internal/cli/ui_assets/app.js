(() => {
  'use strict';

  const state = { runs: [], run: null, mode: 'actions', query: '', activeTypes: new Set(), selected: null, streams: null, searchTimer: null, loadGeneration: 0 };
  const $ = (id) => document.getElementById(id);
  const node = (tag, className, text) => {
    const el = document.createElement(tag);
    if (className) el.className = className;
    if (text !== undefined) el.textContent = text;
    return el;
  };

  async function getJSON(path) {
    const response = await fetch(path, { headers: { Accept: 'application/json' } });
    const body = await response.json().catch(() => ({ error: `HTTP ${response.status}` }));
    if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
    return body;
  }

  function showError(error) {
    const toast = $('error');
    toast.textContent = error instanceof Error ? error.message : String(error);
    toast.classList.remove('hidden');
    window.setTimeout(() => toast.classList.add('hidden'), 7000);
  }

  function shortID(id) {
    return id.length > 30 ? `${id.slice(0, 21)}…${id.slice(-6)}` : id;
  }

  function relativeTime(value) {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return 'unknown';
    const seconds = Math.round((Date.now() - date.getTime()) / 1000);
    if (seconds < 60) return `${Math.max(0, seconds)}s`;
    if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
    if (seconds < 86400) return `${Math.round(seconds / 3600)}h`;
    return `${Math.round(seconds / 86400)}d`;
  }

  function clock(value) {
    if (!value || value.startsWith('0001-')) return '—';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return '—';
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
  }

  function duration(action) {
    if (!action.startedAt || !action.finishedAt || action.startedAt.startsWith('0001-') || action.finishedAt.startsWith('0001-')) return '';
    const ms = new Date(action.finishedAt) - new Date(action.startedAt);
    if (!Number.isFinite(ms) || ms < 0) return '';
    return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(ms < 10000 ? 2 : 1)}s`;
  }

  function statusClass(value) {
    const v = String(value || '').toLowerCase();
    if (v === 'pass' || v === 'passed' || v === 'completed' || v === 'success') return 'pass';
    if (['fail', 'failed', 'error', 'timeout', 'nonzero', 'interrupted', 'parse_error', 'storage_error', 'start_error'].includes(v)) return 'fail';
    return '';
  }

  function renderRunList() {
    const query = $('run-search').value.trim().toLowerCase();
    const list = $('run-list');
    list.replaceChildren();
    for (const run of state.runs) {
      const haystack = `${run.id} ${run.provider} ${run.project} ${run.exit} ${run.verification}`.toLowerCase();
      if (query && !haystack.includes(query)) continue;
      const button = node('button', `run-item${state.run && state.run.run.id === run.id ? ' active' : ''}`);
      button.type = 'button';
      button.dataset.runId = run.id;
      const head = node('div', 'run-item-head');
      head.append(node('span', 'run-project', run.project), node('span', 'run-time', relativeTime(run.startedAt)));
      const id = node('div', 'run-id', shortID(run.id));
      const status = node('div', `mini-status ${run.statusClass}`, `${run.provider} · ${run.statusLabel}`);
      button.append(head, id, status);
      button.addEventListener('click', () => loadRun(run.id));
      list.append(button);
    }
  }

  function firstDetail(value) {
    if (!value || typeof value !== 'object') return '';
    const keys = ['query', 'pattern', 'command', 'cmd', 'file_path', 'filePath', 'path', 'url', 'prompt', 'description', 'name', 'text'];
    for (const key of keys) {
      if (typeof value[key] === 'string' && value[key]) return value[key].replace(/\s+/g, ' ').slice(0, 180);
    }
    if (Array.isArray(value.command)) return value.command.join(' ').slice(0, 180);
    return '';
  }

  function actionFamily(type) {
    const value = String(type || 'unknown');
    if (value.includes('search')) return 'search';
    if (value.includes('read')) return 'read';
    if (value.includes('write')) return 'write';
    if (value.includes('edit') || value.includes('patch')) return 'edit';
    if (value.includes('shell') || value.includes('exec') || value.includes('command')) return 'exec';
    if (value.includes('mcp')) return 'mcp';
    if (value.includes('subagent')) return 'agent';
    if (value.includes('message')) return 'message';
    if (value.includes('error')) return 'error';
    return 'tool';
  }

  function actionDepth(action, byID) {
    let depth = 0;
    let parent = action.parentId;
    const seen = new Set();
    while (parent && byID.has(parent) && !seen.has(parent) && depth < 5) {
      seen.add(parent);
      depth += 1;
      parent = byID.get(parent).parentId;
    }
    return depth;
  }

  function renderTypeFilters(items, typeOf) {
    const holder = $('type-filters');
    holder.replaceChildren();
    const counts = new Map();
    for (const item of items) {
      const type = typeOf(item);
      counts.set(type, (counts.get(type) || 0) + 1);
    }
    for (const [type, count] of [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
      const chip = node('button', `filter-chip${state.activeTypes.size === 0 || state.activeTypes.has(type) ? ' active' : ''}`, `${type} ${count}`);
      chip.type = 'button';
      chip.addEventListener('click', () => {
        if (state.activeTypes.size === 0) for (const key of counts.keys()) state.activeTypes.add(key);
        if (state.activeTypes.has(type)) state.activeTypes.delete(type); else state.activeTypes.add(type);
        if (state.activeTypes.size === counts.size) state.activeTypes.clear();
        renderTimeline();
      });
      holder.append(chip);
    }
  }

  function matches(item, type, text) {
    if (state.activeTypes.size > 0 && !state.activeTypes.has(type)) return false;
    if (!state.query) return true;
    return text.toLowerCase().includes(state.query);
  }

  function appendPager(timeline, streamName) {
    const stream = state.streams[streamName];
    if (!stream || (stream.history.length === 0 && stream.nextCursor === null)) return;
    const controls = node('div', 'stream-pager');
    const previous = node('button', 'load-more', 'Previous page');
    previous.type = 'button';
    previous.disabled = stream.loading || stream.history.length === 0;
    previous.addEventListener('click', () => {
      const cursor = stream.history.pop();
      loadStreamPage(streamName, cursor, false);
    });
    const next = node('button', 'load-more', 'Next page');
    next.type = 'button';
    next.disabled = stream.loading || stream.nextCursor === null;
    next.addEventListener('click', () => loadStreamPage(streamName, stream.nextCursor, true));
    controls.append(previous, node('span', 'pager-label', `${stream.items.length} items on this page`), next);
    timeline.append(controls);
  }

  function renderTimeline() {
    if (!state.run) return;
    const timeline = $('timeline');
    timeline.replaceChildren();
    state.selected = null;
    renderInspector();

    if (state.mode === 'actions') {
      const actions = state.streams.actions.items;
      renderTypeFilters(actions, (item) => item.type || 'unknown');
      const byID = new Map(actions.map((action) => [action.id, action]));
      let shown = 0;
      actions.forEach((action, index) => {
        const type = action.type || 'unknown';
        const detail = firstDetail(action.input) || firstDetail(action.result);
        const searchable = `${type} ${action.provider || ''} ${action.status || ''} ${detail} ${JSON.stringify(action.input || {})}`;
        if (!matches(action, type, searchable)) return;
        shown += 1;
        const row = node('article', 'action-row');
        row.style.setProperty('--depth', String(actionDepth(action, byID)));
        row.tabIndex = 0;
        row.dataset.index = String(index);
        const time = node('div', 'action-time', clock(action.startedAt));
        const rail = node('div', 'action-rail');
        rail.append(node('span', `action-dot ${actionFamily(type)} ${statusClass(action.status)}`));
        const body = node('div', 'action-body');
        const head = node('div', 'action-head');
        head.append(node('span', 'action-type', type), node('span', 'action-summary', detail || action.id));
        const meta = node('div', 'action-meta');
        meta.append(node('span', 'source-badge', action.provider || 'provider'), node('span', '', action.status || 'reported'));
        const elapsed = duration(action);
        if (elapsed) meta.append(node('span', '', elapsed));
        if (action.parentId) meta.append(node('span', '', `↳ ${shortID(action.parentId)}`));
        body.append(head, meta);
        row.append(time, rail, body);
        const select = () => selectItem(row, { kind: 'action', value: action });
        row.addEventListener('click', select);
        row.addEventListener('keydown', (event) => { if (event.key === 'Enter' || event.key === ' ') select(); });
        timeline.append(row);
      });
      if (shown === 0) timeline.append(node('div', 'timeline-empty', 'No loaded actions match this filter.'));
      appendPager(timeline, 'actions');
      return;
    }

    const events = state.streams.events.items;
    const eventType = (event) => typeof event.type === 'string' && event.type ? event.type : '(untyped)';
    renderTypeFilters(events, eventType);
    let shown = 0;
    events.forEach((event, index) => {
      const type = eventType(event);
      const detail = firstDetail(event) || firstDetail(event.message) || event.subtype || event.event || `event ${index + 1}`;
      if (!matches(event, type, `${type} ${detail} ${JSON.stringify(event)}`)) return;
      shown += 1;
      const row = node('article', 'action-row event-row');
      row.tabIndex = 0;
      const time = node('div', 'action-time', clock(event.timestamp || event.created_at || event.createdAt));
      const rail = node('div', 'action-rail');
      rail.append(node('span', 'action-dot'));
      const body = node('div', 'action-body');
      const head = node('div', 'action-head');
      head.append(node('span', 'action-type', type), node('span', 'action-summary', detail));
      const meta = node('div', 'action-meta');
      meta.append(node('span', 'source-badge', 'provider event'), node('span', '', `#${index + 1}`));
      body.append(head, meta);
      row.append(time, rail, body);
      const select = () => selectItem(row, { kind: 'event', value: event, index });
      row.addEventListener('click', select);
      row.addEventListener('keydown', (key) => { if (key.key === 'Enter' || key.key === ' ') select(); });
      timeline.append(row);
    });
    if (shown === 0) timeline.append(node('div', 'timeline-empty', events.length ? 'No loaded provider events match this filter.' : 'This run has no sanitized provider-event artifact.'));
    appendPager(timeline, 'events');
  }

  function selectItem(row, selected) {
    document.querySelectorAll('.action-row.selected').forEach((el) => el.classList.remove('selected'));
    row.classList.add('selected');
    state.selected = selected;
    renderInspector();
  }

  function addPayload(holder, label, value) {
    if (value === undefined || value === null || value === '') return;
    holder.append(node('div', 'payload-label', label));
    const text = typeof value === 'string' ? value : JSON.stringify(value, null, 2);
    holder.append(node('pre', 'payload', text));
  }

  function renderInspector() {
    const holder = $('inspector');
    holder.replaceChildren();
    if (!state.selected) {
      holder.className = 'inspector-empty';
      holder.textContent = 'Select an action or provider event to inspect its sanitized evidence.';
      return;
    }
    holder.className = '';
    const { kind, value } = state.selected;
    const title = kind === 'action' ? value.type : (value.type || '(untyped event)');
    holder.append(node('div', 'inspector-title', title));
    const meta = node('div', 'inspector-meta');
    if (kind === 'action') {
      [value.provider, value.assurance, value.status, value.id].filter(Boolean).forEach((item) => meta.append(node('span', 'pill', item)));
      holder.append(meta);
      addPayload(holder, 'SANITIZED INPUT', value.input);
      addPayload(holder, 'SANITIZED RESULT', value.result);
    } else {
      meta.append(node('span', 'pill', 'provider_reported'), node('span', 'pill', `event #${state.selected.index + 1}`));
      holder.append(meta);
      addPayload(holder, 'SANITIZED PROVIDER EVENT', value);
    }
  }

  function fieldsMap(fields) {
    return new Map((fields || []).map((field) => [field.name, field.value]));
  }

  function renderEvidence() {
    const holder = $('evidence-sections');
    holder.replaceChildren();
    const sections = [
      ['Provider usage', 'provider_reported', state.run.evidence.providerUsage || []],
      ['Process result', 'supervisor_observed', state.run.evidence.supervisor || []],
      ['Repository delta', 'observed, not causal proof', state.run.evidence.repository || []],
      ['Verification', 'verification_observed', state.run.evidence.verification || []]
    ];
    for (const [title, source, fields] of sections) {
      const block = node('section', 'evidence-block');
      const heading = node('div', 'evidence-title');
      heading.append(node('span', '', title), node('span', 'evidence-source', source));
      block.append(heading);
      const grid = node('div', 'evidence-fields');
      if (fields.length === 0) {
        grid.append(node('span', 'field-value', 'Unavailable'));
      } else {
        for (const field of fields) grid.append(node('span', 'field-name', field.name), node('span', 'field-value', field.value));
      }
      block.append(grid);
      holder.append(block);
    }
  }

  function metric(label, value) {
    const card = node('div', 'metric');
    card.append(node('div', 'metric-value', String(value)), node('div', 'metric-label', label));
    return card;
  }

  function renderRun() {
    const data = state.run;
    const run = data.run;
    $('run-provider').textContent = run.provider || 'unknown';
    $('run-project').textContent = run.project || 'unknown';
    $('run-title').textContent = shortID(run.id);
    $('run-subtitle').textContent = `${run.cwd || 'unknown cwd'} · ${new Date(run.startedAt).toLocaleString()}`;
    $('run-prompt').textContent = run.prompt || 'No recorded request.';
    $('action-count').textContent = String(data.actionCount || 0);
    $('event-count').textContent = String(data.eventCount || 0);
    $('top-meta').textContent = `${run.provider || 'unknown'} · ${run.exitReason || 'running'} · ${run.id}`;

    const verification = fieldsMap(data.evidence.verification).get('Status') || 'UNAVAILABLE';
    const exitStatus = statusClass(run.exitReason);
    const verificationStatus = statusClass(verification);
    const runStatus = exitStatus === 'fail' || verificationStatus === 'fail' ? 'fail' : (exitStatus || verificationStatus);
    const verdict = $('run-verdict');
    verdict.textContent = `RUN ${String(run.exitReason || 'RUNNING').toUpperCase()} · VERIFY ${String(verification).toUpperCase()}`;
    verdict.className = `verdict ${runStatus}`;
    $('provider-dot').className = `status-dot ${runStatus}`;
    const timelineWarning = $('timeline-warning');
    if (run.versionUnverified) {
      timelineWarning.textContent = `${run.provider} ${run.providerVersion || 'unknown version'} is unsupported; this timeline may be incomplete.`;
      timelineWarning.classList.remove('hidden');
    } else {
      timelineWarning.textContent = '';
      timelineWarning.classList.add('hidden');
    }

    const repository = fieldsMap(data.evidence.repository);
    const supervisor = fieldsMap(data.evidence.supervisor);
    const metrics = $('metrics');
    metrics.replaceChildren(
      metric('Normalized actions', data.actionCount || 0),
      metric('Provider events', data.eventCount || 0),
      metric('Repository files', repository.get('Files') || '—'),
      metric('Process duration', supervisor.get('Duration') || '—'),
      metric('Warnings', run.warningCount || 0)
    );
    renderEvidence();
    renderTimeline();
    renderRunList();
  }

  async function loadStreamPage(streamName, cursor = 0, rememberCurrent = false, generation = state.loadGeneration) {
    const stream = state.streams && state.streams[streamName];
    if (!stream || stream.loading || cursor === null) return;
    stream.loading = true;
    renderTimeline();
    try {
      const page = await getJSON(`/api/snapshots/${encodeURIComponent(state.run.snapshotId)}/${streamName}?cursor=${cursor}`);
      if (generation !== state.loadGeneration) return;
      if (rememberCurrent) stream.history.push(stream.currentCursor);
      stream.items = page.items || [];
      stream.currentCursor = cursor;
      stream.nextCursor = page.nextCursor === undefined ? null : page.nextCursor;
    } catch (error) {
      if (generation === state.loadGeneration) showError(error);
    } finally {
      stream.loading = false;
      if (generation === state.loadGeneration) renderTimeline();
    }
  }

  async function loadRun(id) {
    const generation = ++state.loadGeneration;
    try {
      const run = await getJSON(`/api/runs/${encodeURIComponent(id)}`);
      if (generation !== state.loadGeneration) return;
      state.run = run;
      state.streams = {
        actions: { items: [], currentCursor: 0, nextCursor: state.run.actionCount === 0 ? null : 0, history: [], loading: false },
        events: { items: [], currentCursor: 0, nextCursor: state.run.eventCount === 0 ? null : 0, history: [], loading: false }
      };
      state.activeTypes.clear();
      state.selected = null;
      renderRun();
      await loadStreamPage('actions', 0, false, generation);
    } catch (error) {
      if (generation === state.loadGeneration) showError(error);
    }
  }

  async function init() {
    try {
      const list = await getJSON('/api/runs');
      state.runs = list.runs || [];
      $('run-count').textContent = String(state.runs.length);
      if (list.unreadable) {
        const warning = $('unreadable-warning');
        warning.textContent = `${list.unreadable} unreadable run(s) were excluded.`;
        warning.classList.remove('hidden');
      }
      renderRunList();
      if (list.initialRunId) await loadRun(list.initialRunId);
    } catch (error) {
      showError(error);
    }
  }

  $('run-search').addEventListener('input', renderRunList);
  $('timeline-search').addEventListener('input', (event) => {
    window.clearTimeout(state.searchTimer);
    state.searchTimer = window.setTimeout(() => {
      state.query = event.target.value.trim().toLowerCase();
      renderTimeline();
    }, 180);
  });
  document.querySelectorAll('.tab').forEach((tab) => tab.addEventListener('click', async () => {
    document.querySelectorAll('.tab').forEach((item) => { item.classList.toggle('active', item === tab); item.setAttribute('aria-selected', item === tab ? 'true' : 'false'); });
    state.mode = tab.dataset.mode;
    state.activeTypes.clear();
    renderTimeline();
    if (state.streams[state.mode].items.length === 0) await loadStreamPage(state.mode);
  }));

  init();
})();
