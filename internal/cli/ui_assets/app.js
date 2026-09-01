(() => {
  'use strict';

  const POLL_MS = 5000;
  const state = { runs: [], run: null, mode: 'actions', query: '', activeTypes: new Set(), selected: null, streams: null, searchTimer: null, loadGeneration: 0, runAbortController: null, pollTimer: null, pollController: null, autoSelectedId: '' };
  const $ = (id) => document.getElementById(id);
  const node = (tag, className, text) => {
    const el = document.createElement(tag);
    if (className) el.className = className;
    if (text !== undefined) el.textContent = text;
    return el;
  };

  async function getJSON(path, signal) {
    const response = await fetch(path, { headers: { Accept: 'application/json' }, signal });
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

  function announceInspector(message) {
    $('inspector-status').textContent = message;
  }

  function shortID(id) {
    return id.length > 30 ? `${id.slice(0, 21)}…${id.slice(-6)}` : id;
  }

  function relativeTime(value) {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return 'unknown';
    const seconds = Math.round((Date.now() - date.getTime()) / 1000);
    if (seconds < 60) return `${Math.max(0, seconds)}s ago`;
    if (seconds < 3600) return `${Math.round(seconds / 60)}m ago`;
    if (seconds < 86400) return `${Math.round(seconds / 3600)}h ago`;
    return `${Math.round(seconds / 86400)}d ago`;
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

  // Plain-English copy for the status vocabulary. The raw tokens stay (they are documented and pinned); these only explain them.
  const NO_VERIFICATION = 'No checks were run for this session. Record with --verify (agentrec setup --verify) to run the checks pinned in .agentrec.yaml.';
  const NOT_OBSERVED = 'agentrec did not launch this session, so exit code and signal were never seen.';
  const ENDED_BY_HOOK = 'The end was reported by the provider\'s SessionEnd hook.';
  const REPOSITORY_PENDING = 'The diff is measured when the session ends.';
  const REPOSITORY_NONE = 'No repository diff was recorded for this run.';
  const NOT_CAUSAL = 'Observed during the run — this is not proof the agent caused it';
  const REPOSITORY_REASONS = {
    'repository change evidence was not recorded': REPOSITORY_NONE,
    'repository change evidence is pending': REPOSITORY_PENDING,
    'repository change artifacts are missing git/result.json': 'The repository diff artifacts are incomplete (git/result.json is missing).',
    'repository result is missing change-list evidence': 'The repository diff artifacts are incomplete (the change list is missing).'
  };
  const ATTRIBUTIONS = {
    provider_reported: (provider) => `Reported by ${provider || 'the provider'}`,
    supervisor_observed: () => 'Observed by agentrec',
    verification_observed: () => 'Observed by verification checks',
    'observed during run, not causal proof': () => NOT_CAUSAL,
    'observed, not causal proof': () => NOT_CAUSAL
  };
  const EMPTY_SECTION = {
    'Provider usage': 'The provider did not report usage for this run.',
    'Process result': 'No process result was recorded.',
    'Repository delta': REPOSITORY_NONE,
    Verification: NO_VERIFICATION
  };

  function humanAttribution(raw, provider) {
    const known = ATTRIBUTIONS[raw];
    return known ? known(provider) : raw;
  }

  // labelled renders humanised text and keeps the raw token reachable in the title attribute.
  function labelled(tag, className, text, raw) {
    const el = node(tag, className, text);
    if (raw && raw !== text) el.title = raw;
    return el;
  }

  function sentence(text) {
    const t = String(text || '').trim();
    if (!t) return '';
    if (REPOSITORY_REASONS[t]) return REPOSITORY_REASONS[t];
    return t[0].toUpperCase() + t.slice(1) + (/[.!?]$/.test(t) ? '' : '.');
  }

  function outcome(exitReason, supervisor) {
    const reason = String(exitReason || 'running').toLowerCase();
    if (reason === 'running') return { value: 'RUNNING', tone: '', detail: 'Session is still open; results appear when it ends.' };
    if (reason === 'unknown') return { value: 'UNKNOWN', tone: '', detail: 'The recorder ended without writing how the session ended.' };
    if (reason === 'session_ended') return { value: 'ENDED', tone: '', detail: 'Ended by the provider\'s SessionEnd hook, as reported.' };
    if (reason === 'session_lost') return { value: 'LOST', tone: 'warn', detail: 'No hook arrived for the idle timeout, or the recorder was signalled; the session\'s own end was not seen.' };
    const facts = [supervisor.has('Exit Code') ? `exit code ${supervisor.get('Exit Code')}` : '', supervisor.get('Signal') ? `signal ${supervisor.get('Signal')}` : '', supervisor.get('Duration') || ''].filter(Boolean);
    return { value: reason.toUpperCase(), tone: statusClass(reason), detail: facts.join(' · ') || 'Duration unavailable' };
  }

  function verificationSummary(fields) {
    if (!fields || fields.length === 0) return { value: 'UNAVAILABLE', tone: '', detail: NO_VERIFICATION };
    const map = fieldsMap(fields);
    const status = String(map.get('Status') || 'UNAVAILABLE').toUpperCase();
    const checks = fields.filter((field) => field.name === 'Check');
    const passed = checks.filter((field) => String(field.value).startsWith('PASS ')).length;
    const failed = checks.filter((field) => String(field.value).startsWith('FAIL ')).length;
    let detail = sentence(map.get('Reason'));
    if (checks.length) detail = `${passed} of ${checks.length} checks passed${failed ? `, ${failed} failed` : ''}.`;
    else if (!detail && status === 'UNAVAILABLE') detail = NO_VERIFICATION;
    return { value: status, tone: statusClass(status), detail };
  }

  function repositorySummary(changes, fields) {
    const status = String(changes.status || 'unavailable').toUpperCase();
    if (status === 'AVAILABLE') return { value: String(changes.total || 0), detail: `${changes.tracked || 0} tracked · ${changes.untracked || 0} untracked · +${changes.additions || 0}/−${changes.deletions || 0} · ${changes.binary || 0} binary` };
    if (status === 'PENDING' || fields.get('Status') === 'PENDING') return { value: 'PENDING', detail: REPOSITORY_PENDING };
    return { value: 'UNAVAILABLE', detail: sentence(changes.reason) || REPOSITORY_NONE };
  }

  // explainStatus is the hover text for the server-classified run-list chip.
  function explainStatus(label) {
    const v = String(label || '').toUpperCase();
    if (v === 'UNAVAILABLE') return 'No checks were run for this session.';
    if (v === 'PASS') return 'Verification checks passed.';
    if (v === 'FAIL') return 'Verification checks failed.';
    const known = outcome(label, new Map());
    return ['RUNNING', 'UNKNOWN', 'ENDED', 'LOST'].includes(known.value) ? known.detail : `The run ended with ${label}.`;
  }

  function renderRunList() {
    const query = $('run-search').value.trim().toLowerCase();
    const list = $('run-list');
    const focused = document.activeElement && document.activeElement.dataset ? document.activeElement.dataset.runId : undefined;
    list.replaceChildren();
    let shown = 0;
    for (const run of state.runs) {
      const haystack = `${run.id} ${run.provider} ${run.project} ${run.exit} ${run.verification}`.toLowerCase();
      if (query && !haystack.includes(query)) continue;
      shown += 1;
      const active = Boolean(state.run && state.run.run.id === run.id);
      const button = node('button', `run-item${active ? ' active' : ''}`);
      button.type = 'button';
      button.dataset.runId = run.id;
      if (active) button.setAttribute('aria-current', 'true');
      const head = node('div', 'run-item-head');
      head.append(node('span', 'run-project', run.project || 'unknown project'), node('span', 'run-time', relativeTime(run.startedAt)));
      const foot = node('div', 'run-item-foot');
      const status = node('span', `mini-status ${run.statusClass}`, run.statusLabel);
      status.title = explainStatus(run.statusLabel);
      foot.append(node('span', 'run-provider', run.provider || 'unknown'), status);
      button.append(head, node('div', 'run-id', shortID(run.id)), foot);
      button.addEventListener('click', () => loadRun(run.id));
      list.append(button);
      if (focused === run.id) button.focus({ preventScroll: true });
    }
    $('run-count').textContent = String(state.runs.length);
    const empty = $('run-list-empty');
    if (shown === 0) {
      empty.textContent = state.runs.length === 0 ? 'No runs recorded yet — start a Claude Code or Codex session; it appears here when it ends.' : 'No runs match this search.';
      empty.classList.remove('hidden');
    } else {
      empty.classList.add('hidden');
    }
  }

  function renderWorkspaceState() {
    const empty = $('workspace-empty');
    const view = $('run-view');
    if (state.run) {
      empty.classList.add('hidden');
      view.classList.remove('hidden');
      return;
    }
    view.classList.add('hidden');
    empty.classList.remove('hidden');
    const noRuns = state.runs.length === 0;
    $('workspace-empty-title').textContent = noRuns ? 'No runs recorded yet' : 'No run selected';
    $('workspace-empty-body').textContent = noRuns ? 'Start a Claude Code or Codex session; it appears here when it ends.' : 'Pick a run from the list to inspect its recorded evidence.';
    $('top-meta').textContent = noRuns ? 'No recorded runs' : `${state.runs.length} recorded run(s)`;
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

  function changeFamily(change) {
    if (!change.tracked) return 'untracked';
    if (change.binary) return 'binary';
    return 'tracked';
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
      chip.setAttribute('aria-pressed', String(state.activeTypes.size === 0 || state.activeTypes.has(type)));
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
      const cursor = stream.history[stream.history.length - 1];
      loadStreamPage(streamName, cursor, false, state.loadGeneration, true);
    });
    const next = node('button', 'load-more', 'Next page');
    next.type = 'button';
    next.disabled = stream.loading || stream.nextCursor === null;
    next.addEventListener('click', () => loadStreamPage(streamName, stream.nextCursor, true));
    controls.append(previous, node('span', 'pager-label', `${stream.items.length} items on this page`), next);
    timeline.append(controls);
  }

  // One focusable, keyboard-activatable timeline row; shared by actions, changes and events.
  function timelineRow(className, select) {
    const row = node('article', className);
    row.tabIndex = 0;
    row.setAttribute('role', 'button');
    row.setAttribute('aria-controls', 'inspector');
    row.addEventListener('click', select);
    row.addEventListener('keydown', (event) => {
      if (event.key === 'Enter' || event.key === ' ') {
        if (event.key === ' ') event.preventDefault();
        select();
      }
    });
    return row;
  }

  function renderTimeline() {
    if (!state.run) return;
    const timeline = $('timeline');
    timeline.replaceChildren();
    state.selected = null;
    renderInspector();

    if (state.mode === 'actions') {
      const stream = state.streams.actions;
      const actions = stream.items;
      if (stream.error) timeline.append(node('div', 'timeline-empty stream-error', `Could not load actions: ${stream.error}`));
      renderTypeFilters(actions, (item) => item.type || 'unknown');
      const byID = new Map(actions.map((action) => [action.id, action]));
      let shown = 0;
      actions.forEach((action, index) => {
        const type = action.type || 'unknown';
        const detail = firstDetail(action.input) || firstDetail(action.result);
        const searchable = `${type} ${action.provider || ''} ${action.status || ''} ${detail} ${JSON.stringify(action.input || {})}`;
        if (!matches(action, type, searchable)) return;
        shown += 1;
        const row = timelineRow('action-row', () => selectItem(row, { kind: 'action', value: action }));
        row.style.setProperty('--depth', String(actionDepth(action, byID)));
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
        const observedPaths = action.samePathObserved || [];
        if (observedPaths.length) meta.append(node('span', 'path-correlation', 'same path observed — not causal proof'));
        body.append(head, meta);
        row.append(time, rail, body);
        timeline.append(row);
      });
      if (shown === 0 && !stream.error) timeline.append(node('div', 'timeline-empty', stream.loaded ? 'No loaded actions match this filter.' : 'Loading actions…'));
      appendPager(timeline, 'actions');
      return;
    }

    if (state.mode === 'changes') {
      const stream = state.streams.changes;
      const changes = stream.items;
      const source = stream.attribution || 'observed during run, not causal proof';
      if (stream.error) timeline.append(node('div', 'timeline-empty stream-error', `Could not load repository changes: ${stream.error}`));
      renderTypeFilters(changes, changeFamily);
      if (stream.loaded && stream.status === 'unavailable') {
        const pending = fieldsMap(state.run.evidence.repository).get('Status') === 'PENDING';
        const reason = pending ? REPOSITORY_PENDING : sentence(stream.reason);
        timeline.append(node('div', 'timeline-empty change-evidence-warning', `Repository change evidence is unavailable.${reason ? ` ${reason}` : ''}`));
      }
      if (changes.length) timeline.append(labelled('div', 'timeline-note', humanAttribution(source), source));
      let shown = 0;
      changes.forEach((change) => {
        const type = changeFamily(change);
        const counts = change.binary ? 'binary' : [change.additions === undefined ? '' : `+${change.additions}`, change.deletions === undefined ? '' : `-${change.deletions}`].filter(Boolean).join(' ');
        if (!matches(change, type, `${type} ${change.path} ${change.kind || ''} ${counts}`)) return;
        shown += 1;
        const row = timelineRow('action-row change-row', () => {
          selectItem(row, { kind: 'change', value: change, patch: null, patchCursor: 0, patchNextCursor: null, patchHistory: [], patchLoading: false });
          if (change.tracked) loadPatchPage(change.path, 0, false, state.loadGeneration);
        });
        const marker = node('div', `change-marker ${type}`, change.tracked ? (change.binary ? 'B' : 'M') : '?');
        const rail = node('div', 'action-rail');
        rail.append(node('span', `action-dot ${type}`));
        const body = node('div', 'action-body');
        const head = node('div', 'action-head');
        head.append(node('span', 'action-type', change.path));
        const meta = node('div', 'action-meta');
        meta.append(node('span', '', type));
        if (counts) meta.append(node('span', 'change-counts', counts));
        body.append(head, meta);
        row.append(marker, rail, body);
        timeline.append(row);
      });
      if (shown === 0 && !stream.error && !(stream.loaded && stream.status === 'unavailable')) {
        let message = 'Loading repository changes…';
        if (stream.loaded && stream.total === 0) message = 'No repository changes were observed.';
        else if (stream.loaded) message = 'No loaded changes match this filter.';
        timeline.append(node('div', 'timeline-empty', message));
      }
      appendPager(timeline, 'changes');
      return;
    }

    const stream = state.streams.events;
    const events = stream.items;
    if (stream.error) timeline.append(node('div', 'timeline-empty stream-error', `Could not load provider events: ${stream.error}`));
    const eventType = (event) => typeof event.type === 'string' && event.type ? event.type : '(untyped)';
    renderTypeFilters(events, eventType);
    let shown = 0;
    events.forEach((event, index) => {
      const type = eventType(event);
      const detail = firstDetail(event) || firstDetail(event.message) || event.subtype || event.event || `event ${index + 1}`;
      if (!matches(event, type, `${type} ${detail} ${JSON.stringify(event)}`)) return;
      shown += 1;
      const row = timelineRow('action-row event-row', () => selectItem(row, { kind: 'event', value: event, index }));
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
      timeline.append(row);
    });
    if (shown === 0 && !stream.error) {
      const message = stream.loaded ? (events.length ? 'No loaded provider events match this filter.' : 'This run has no sanitized provider-event artifact.') : 'Loading provider events…';
      timeline.append(node('div', 'timeline-empty', message));
    }
    appendPager(timeline, 'events');
  }

  function selectItem(row, selected) {
    document.querySelectorAll('.action-row.selected').forEach((el) => {
      el.classList.remove('selected');
    });
    row.classList.add('selected');
    state.selected = selected;
    renderInspector();
    const label = selected.kind === 'change' ? selected.value.path : (selected.value.type || selected.kind);
    announceInspector(`${selected.kind} selected: ${label}`);
  }

  function addPayload(holder, label, value) {
    if (value === undefined || value === null || value === '') return;
    holder.append(node('div', 'payload-label', label));
    const text = typeof value === 'string' ? value : JSON.stringify(value, null, 2);
    holder.append(node('pre', 'payload', text));
  }

  // Unified-diff lines rendered as text nodes with a tone class; no markup is derived from the payload.
  function patchNode(patch) {
    const pre = node('pre', 'payload diff-patch');
    for (const line of patch.split('\n')) {
      let tone = '';
      if (line.startsWith('+++') || line.startsWith('---') || line.startsWith('diff ') || line.startsWith('index ')) tone = ' diff-meta';
      else if (line.startsWith('+')) tone = ' diff-add';
      else if (line.startsWith('-')) tone = ' diff-del';
      else if (line.startsWith('@@')) tone = ' diff-hunk';
      pre.append(node('span', `diff-line${tone}`, `${line}\n`));
    }
    return pre;
  }

  function renderInspector() {
    const holder = $('inspector');
    holder.replaceChildren();
    if (!state.selected) {
      holder.className = 'inspector-empty';
      holder.textContent = 'Select an action, change, or provider event to inspect its sanitized evidence.';
      return;
    }
    holder.className = '';
    const { kind, value } = state.selected;
    const title = kind === 'action' ? value.type : (kind === 'change' ? value.path : (value.type || '(untyped event)'));
    holder.append(node('div', 'inspector-title', title));
    const meta = node('div', 'inspector-meta');
    if (kind === 'action') {
      if (value.provider) meta.append(node('span', 'pill', value.provider));
      if (value.assurance) meta.append(labelled('span', 'pill', humanAttribution(value.assurance, value.provider), value.assurance));
      [value.status, value.id].filter(Boolean).forEach((item) => meta.append(node('span', 'pill', item)));
      const observedPaths = value.samePathObserved || [];
      if (observedPaths.length) meta.append(node('span', 'pill path-correlation', 'same path observed — not causal proof'));
      holder.append(meta);
      if (observedPaths.length) addPayload(holder, 'SAME PATH OBSERVED — NOT CAUSAL PROOF', observedPaths);
      addPayload(holder, 'SANITIZED INPUT', value.input);
      addPayload(holder, 'SANITIZED RESULT', value.result);
    } else if (kind === 'change') {
      const source = state.streams.changes.attribution || 'observed during run, not causal proof';
      meta.append(node('span', 'pill', value.tracked ? 'tracked' : 'untracked'), labelled('span', 'pill', humanAttribution(source), source));
      if (value.binary) meta.append(node('span', 'pill', 'binary'));
      holder.append(meta);
      const details = {
        kind: value.kind,
        additions: value.additions,
        deletions: value.deletions,
        mode: value.mode,
        size: value.size,
        stored: value.stored,
        reason: value.reason
      };
      Object.keys(details).forEach((key) => details[key] === undefined && delete details[key]);
      addPayload(holder, 'REPOSITORY-OBSERVED METADATA', details);
      if (value.tracked) {
        holder.append(node('div', 'payload-label', 'SANITIZED REPOSITORY PATCH'));
        const { patchLoading, patchError, patch } = state.selected;
        if (!patchLoading && !patchError && patch) {
          holder.append(patchNode(patch));
        } else {
          holder.append(node('pre', 'payload diff-patch', patchLoading ? 'Loading patch…' : (patchError || 'No patch bytes on this page.')));
        }
        if (!state.selected.patchLoading && (state.selected.patchHistory.length > 0 || state.selected.patchNextCursor !== null)) {
          const controls = node('div', 'stream-pager patch-pager');
          const previous = node('button', 'load-more', 'Previous page');
          previous.type = 'button';
          previous.disabled = state.selected.patchHistory.length === 0;
          previous.addEventListener('click', () => {
            const cursor = state.selected.patchHistory[state.selected.patchHistory.length - 1];
            loadPatchPage(value.path, cursor, false, state.loadGeneration, true);
          });
          const next = node('button', 'load-more', 'Next page');
          next.type = 'button';
          next.disabled = state.selected.patchNextCursor === null;
          next.addEventListener('click', () => loadPatchPage(value.path, state.selected.patchNextCursor, true, state.loadGeneration));
          controls.append(previous, node('span', 'pager-label', 'bounded patch page'), next);
          holder.append(controls);
        }
      }
    } else {
      meta.append(labelled('span', 'pill', humanAttribution('provider_reported', state.run.run.provider), 'provider_reported'), node('span', 'pill', `event #${state.selected.index + 1}`));
      holder.append(meta);
      addPayload(holder, 'SANITIZED PROVIDER EVENT', value);
    }
  }

  function fieldsMap(fields) {
    return new Map((fields || []).map((field) => [field.name, field.value]));
  }

  // describeField turns a raw evidence value into display text plus, where the word alone is opaque, a one-line caption.
  function describeField(section, name, value, fields, provider) {
    if (name === 'Attribution') return { text: humanAttribution(value, provider), title: value };
    if (section === 'Process result' && name === 'Status' && value.startsWith('UNAVAILABLE (interactive session')) {
      const ended = fields.get('Exit Reason') === 'session_ended';
      return { text: 'NOT OBSERVED', caption: ended ? `${NOT_OBSERVED} ${ENDED_BY_HOOK}` : NOT_OBSERVED, title: value };
    }
    if (section === 'Repository delta' && name === 'Status') {
      if (value === 'PENDING') return { text: value, caption: REPOSITORY_PENDING };
      if (value === 'UNAVAILABLE') return { text: value, caption: sentence(fields.get('Reason')) || REPOSITORY_NONE };
    }
    if (section === 'Verification' && name === 'Status' && value === 'UNAVAILABLE') return { text: value, caption: sentence(fields.get('Reason')) || NO_VERIFICATION };
    return { text: value };
  }

  function fieldValue(described) {
    const el = node('span', 'field-value', described.text);
    if (described.title && described.title !== described.text) el.title = described.title;
    if (described.caption) el.append(node('span', 'field-caption', described.caption));
    return el;
  }

  function renderEvidence() {
    const holder = $('evidence-sections');
    holder.replaceChildren();
    const provider = state.run.run.provider;
    const sections = [
      ['Provider usage', 'provider_reported', state.run.evidence.providerUsage || []],
      ['Process result', 'supervisor_observed', state.run.evidence.supervisor || []],
      ['Repository delta', 'observed, not causal proof', state.run.evidence.repository || []],
      ['Verification', 'verification_observed', state.run.evidence.verification || []]
    ];
    for (const [title, source, fields] of sections) {
      const block = node('section', 'evidence-block');
      const heading = node('div', 'evidence-title');
      heading.append(node('span', '', title), labelled('span', 'evidence-source', humanAttribution(source, provider), source));
      block.append(heading);
      const grid = node('div', 'evidence-fields');
      if (fields.length === 0) {
        const empty = fieldValue({ text: 'Unavailable', caption: EMPTY_SECTION[title] });
        empty.classList.add('field-span');
        grid.append(empty);
      } else {
        const map = fieldsMap(fields);
        for (const field of fields) grid.append(node('span', 'field-name', field.name), fieldValue(describeField(title, field.name, String(field.value), map, provider)));
      }
      block.append(grid);
      holder.append(block);
    }
  }

  // tone is one of '', 'pass', 'fail', 'warn'; UNAVAILABLE and RUNNING stay neutral on purpose.
  function metric(label, value, detail = '', tone = '') {
    const card = node('div', `metric${tone ? ` ${tone}` : ''}`);
    card.append(node('div', 'metric-label', label), node('div', 'metric-value', String(value)));
    if (detail) card.append(node('div', 'metric-detail', detail));
    return card;
  }

  function renderRun() {
    const data = state.run;
    const run = data.run;
    $('run-provider').textContent = run.provider || 'unknown';
    $('run-project').textContent = run.project || 'unknown';
    $('run-title').textContent = run.id;
    $('run-subtitle').textContent = `${run.cwd || 'unknown cwd'} · ${new Date(run.startedAt).toLocaleString()}`;
    $('run-prompt').textContent = run.prompt || 'No recorded request.';
    $('action-count').textContent = String(data.actionCount || 0);
    $('change-count').textContent = '0';
    $('event-count').textContent = String(data.eventCount || 0);
    $('top-meta').textContent = `${run.provider || 'unknown'} · ${run.exitReason || 'running'} · ${run.id}`;
    document.title = `${run.project || run.id} · agentrec`;

    const supervisor = fieldsMap(data.evidence.supervisor);
    const process = outcome(run.exitReason, supervisor);
    const verification = verificationSummary(data.evidence.verification);
    const tones = [process.tone, verification.tone];
    // ponytail: a run reads as passed only when both the process and the checks did; UNAVAILABLE never borrows green.
    const runStatus = tones.includes('fail') ? 'fail' : (tones.includes('warn') ? 'warn' : (process.tone === 'pass' && verification.tone === 'pass' ? 'pass' : ''));
    const verdict = $('run-verdict');
    verdict.textContent = `Run ${process.value} · Verify ${verification.value}`;
    verdict.title = `${process.detail} ${verification.detail}`.trim();
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

    const repository = repositorySummary(data.changes || {}, fieldsMap(data.evidence.repository));
    const warnings = Number(run.warningCount) || 0;
    const metrics = $('metrics');
    metrics.replaceChildren(
      metric('Process outcome', process.value, process.detail, process.tone),
      metric('Verification verdict', verification.value, verification.detail, verification.tone),
      metric('Repository evidence', repository.value, repository.detail),
      metric('Normalized actions', data.actionCount || 0),
      metric('Provider events', data.eventCount || 0),
      metric('Warnings', warnings, '', warnings > 0 ? 'warn' : '')
    );
    renderEvidence();
    renderTimeline();
    renderRunList();
    renderWorkspaceState();
  }

  async function loadPatchPage(path, cursor = 0, rememberCurrent = false, generation = state.loadGeneration, consumeHistory = false) {
    const selected = state.selected;
    if (!selected || selected.kind !== 'change' || selected.value.path !== path || selected.patchLoading && cursor === selected.patchCursor) return;
    selected.patchLoading = true;
    announceInspector(`Loading patch for ${path}`);
    renderInspector();
    try {
      const snapshotID = state.run.snapshotId;
      const page = await getJSON(`/api/snapshots/${encodeURIComponent(snapshotID)}/patch?path=${encodeURIComponent(path)}&cursor=${cursor}`);
      if (generation !== state.loadGeneration || state.selected !== selected || selected.value.path !== page.path) return;
      if (rememberCurrent) selected.patchHistory.push(selected.patchCursor);
      if (consumeHistory) selected.patchHistory.pop();
      selected.patch = page.patch || '';
      selected.patchError = '';
      selected.patchCursor = cursor;
      selected.patchNextCursor = page.nextCursor === undefined ? null : page.nextCursor;
      selected.patchAttribution = page.attribution;
      announceInspector(`Patch loaded for ${path}`);
    } catch (error) {
      if (generation === state.loadGeneration && state.selected === selected) {
        selected.patchError = `Patch unavailable: ${error instanceof Error ? error.message : String(error)}`;
        announceInspector(selected.patchError);
        showError(error);
      }
    } finally {
      if (generation === state.loadGeneration && state.selected === selected) {
        selected.patchLoading = false;
        renderInspector();
      }
    }
  }

  async function loadStreamPage(streamName, cursor = 0, rememberCurrent = false, generation = state.loadGeneration, consumeHistory = false, signal) {
    if (generation !== state.loadGeneration) return;
    const stream = state.streams && state.streams[streamName];
    if (!stream || stream.loading || cursor === null) return;
    stream.loading = true;
    renderTimeline();
    try {
      const page = await getJSON(`/api/snapshots/${encodeURIComponent(state.run.snapshotId)}/${streamName}?cursor=${cursor}`, signal);
      if (generation !== state.loadGeneration) return;
      if (cursor !== stream.currentCursor) state.activeTypes.clear();
      if (rememberCurrent) stream.history.push(stream.currentCursor);
      if (consumeHistory) stream.history.pop();
      stream.items = page.items || [];
      stream.error = '';
      stream.currentCursor = cursor;
      stream.nextCursor = page.nextCursor === undefined ? null : page.nextCursor;
      stream.loaded = true;
      if (streamName === 'changes') {
        stream.total = page.total || 0;
        stream.status = page.status || '';
        stream.reason = page.reason || '';
        stream.attribution = page.attribution || '';
        stream.baseline = page.baseline || '';
        $('change-count').textContent = stream.status === 'unavailable' ? (stream.total === 0 ? '?' : `${stream.total}+?`) : String(stream.total);
      }
    } catch (error) {
      if (generation === state.loadGeneration) {
        stream.error = error instanceof Error ? error.message : String(error);
        showError(error);
      }
    } finally {
      stream.loading = false;
      if (generation === state.loadGeneration) renderTimeline();
    }
  }

  async function loadRun(id) {
    if (state.runAbortController) state.runAbortController.abort();
    const controller = new AbortController();
    state.runAbortController = controller;
    const generation = ++state.loadGeneration;
    try {
      const run = await getJSON(`/api/runs/${encodeURIComponent(id)}`, controller.signal);
      if (generation !== state.loadGeneration) return;
      state.run = run;
      state.streams = {
        actions: { items: [], currentCursor: 0, nextCursor: state.run.actionCount === 0 ? null : 0, history: [], loading: false, loaded: state.run.actionCount === 0, error: '' },
        changes: { items: [], currentCursor: 0, nextCursor: 0, history: [], loading: false, loaded: false, error: '', total: 0, attribution: '', baseline: '', status: '', reason: '' },
        events: { items: [], currentCursor: 0, nextCursor: state.run.eventCount === 0 ? null : 0, history: [], loading: false, loaded: state.run.eventCount === 0, error: '' }
      };
      state.activeTypes.clear();
      state.selected = null;
      renderRun();
      await loadStreamPage(state.mode, 0, false, generation, false, controller.signal);
      if (state.mode !== 'actions') await loadStreamPage('actions', 0, false, generation, false, controller.signal);
      if (state.mode !== 'changes') await loadStreamPage('changes', 0, false, generation, false, controller.signal);
    } catch (error) {
      if (error.name !== 'AbortError' && generation === state.loadGeneration) showError(error);
    } finally {
      if (state.runAbortController === controller) state.runAbortController = null;
    }
  }

  function applyRunList(list) {
    state.runs = list.runs || [];
    const warning = $('unreadable-warning');
    if (list.unreadable) {
      warning.textContent = `${list.unreadable} unreadable run(s) were excluded.`;
      warning.classList.remove('hidden');
    } else {
      warning.classList.add('hidden');
    }
    renderRunList();
    renderWorkspaceState();
  }

  // Selects the newest run when nothing is shown; the guard stops a run that fails to load from being retried every tick.
  function autoSelect(list) {
    if (state.run || state.runAbortController || state.runs.length === 0) return;
    const id = list.initialRunId || state.runs[0].id;
    if (id === state.autoSelectedId) return;
    state.autoSelectedId = id;
    return loadRun(id);
  }

  // Live refresh: re-fetch /api/runs and redraw the list in place. Selection, focus and the loaded run are untouched.
  async function refreshRuns() {
    if (state.pollController || document.visibilityState === 'hidden') return;
    state.pollController = new AbortController();
    try {
      const list = await getJSON('/api/runs', state.pollController.signal);
      applyRunList(list);
      await autoSelect(list);
    } catch (error) {
      // ponytail: poll failures stay quiet; the next tick retries and user-initiated loads still surface errors.
    } finally {
      state.pollController = null;
    }
  }

  function startPolling() {
    stopPolling();
    state.pollTimer = window.setInterval(refreshRuns, POLL_MS);
  }

  function stopPolling() {
    window.clearInterval(state.pollTimer);
    state.pollTimer = null;
  }

  async function init() {
    try {
      const list = await getJSON('/api/runs');
      applyRunList(list);
      await autoSelect(list);
    } catch (error) {
      $('workspace-empty-title').textContent = 'Could not load recorded runs';
      $('workspace-empty-body').textContent = error instanceof Error ? error.message : String(error);
      showError(error);
    }
    startPolling();
  }

  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') {
      stopPolling();
    } else {
      refreshRuns();
      startPolling();
    }
  });

  $('run-search').addEventListener('input', renderRunList);
  $('timeline-search').addEventListener('input', (event) => {
    window.clearTimeout(state.searchTimer);
    state.searchTimer = window.setTimeout(() => {
      state.query = event.target.value.trim().toLowerCase();
      renderTimeline();
    }, 180);
  });
  const tabs = Array.from(document.querySelectorAll('.tab'));
  tabs.forEach((tab, index) => {
    tab.addEventListener('click', async () => {
      tabs.forEach((item) => {
        item.classList.toggle('active', item === tab);
        item.setAttribute('aria-selected', item === tab ? 'true' : 'false');
        item.tabIndex = item === tab ? 0 : -1;
      });
      $('timeline').setAttribute('aria-labelledby', tab.id);
      state.mode = tab.dataset.mode;
      state.activeTypes.clear();
      renderTimeline();
      if (state.streams && !state.streams[state.mode].loaded) await loadStreamPage(state.mode);
    });
    tab.addEventListener('keydown', (event) => {
      let next = index;
      if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
      else if (event.key === 'ArrowLeft') next = (index - 1 + tabs.length) % tabs.length;
      else if (event.key === 'Home') next = 0;
      else if (event.key === 'End') next = tabs.length - 1;
      else return;
      event.preventDefault();
      tabs[next].focus();
      tabs[next].click();
    });
  });

  init();
})();
