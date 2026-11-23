const CELL = 28;
const MATRIX_GUTTER = 8;
const ROW = 48;
const AXIS = 32;
export const POLL_INTERVAL = 1000;
export const REORDER_DURATION = 200;
export const CACHE_BINS = Object.freeze([
  { level: 1, label: '<75%' }, { level: 2, label: '75–<90%' },
  { level: 3, label: '90–<95%' }, { level: 4, label: '≥95%' }
]);
const STATUSES = new Set(['running', 'waiting', 'completed', 'failed']);
const numberFormat = new Intl.NumberFormat(undefined);
const compactFormat = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });
const dateFormat = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
const timeFormat = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', hour12: false });
const list = value => Array.isArray(value) ? value : [];
const text = value => typeof value === 'string' ? value : '';
const amount = value => Number.isFinite(value) ? Math.max(0, value) : 0;
const count = value => numberFormat.format(amount(value));
const statusOf = turn => STATUSES.has(turn.status) ? turn.status : 'unknown';
const statusText = status => ({ running: 'Running', waiting: 'Waiting for tools', completed: 'Completed', failed: 'Failed' })[status] || 'Unknown';
const date = value => Number.isFinite(value) && value > 0 ? dateFormat.format(new Date(value)) : 'Not recorded';
const percent = value => Number.isFinite(value) ? `${(Math.max(0, Math.min(1, value)) * 100).toFixed(1)}%` : '—';

export function duration(ms) {
  if (!Number.isFinite(ms) || ms < 0) return '—';
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`;
}
export function payloadText(value) {
  if (value === undefined || value === null) return 'Not recorded';
  // Wire payload strings retain exact large JSON numbers; never parse them again.
  return typeof value === 'string' ? value : JSON.stringify(value, null, 2);
}
// Parse a copy only for a compact summary; detailed payloads retain their original
// JSON spelling (including numbers that JavaScript cannot represent exactly).
function previewValue(value) {
  if (typeof value !== 'string' || !/^[\s]*[\[{]/.test(value)) return value;
  try { return JSON.parse(value); } catch { return value; }
}
export function contentPreview(value) {
  const parts = [];
  function collect(content) {
    if (typeof content === 'string') parts.push(content);
    else if (Array.isArray(content)) content.forEach(collect);
    else if (content && typeof content === 'object') {
      if (['text', 'input_text', 'output_text'].includes(content.type)) parts.push(text(content.text));
      else if (['image', 'image_url', 'input_image'].includes(content.type)) parts.push('[Image]');
      else if (['document', 'input_file'].includes(content.type)) parts.push('[Document]');
      else if (content.content != null && !content.type) collect(content.content);
    }
  }
  const parsed = previewValue(value);
  collect(parsed);
  if (!parts.length && typeof parsed === 'string') parts.push(parsed);
  return Array.from(parts.join(' ').replace(/\s+/g, ' ').trim()).slice(0, 400).join('');
}
export function toolOverview(value) {
  const definitions = previewValue(value);
  if (!Array.isArray(definitions)) return { names: [], definitions: 0 };
  const names = new Set();
  let total = 0;
  function collect(tools, prefix = '') {
    for (const tool of tools) {
      if (!tool || typeof tool !== 'object') continue;
      if (tool.type === 'namespace' && Array.isArray(tool.tools)) {
        collect(tool.tools, `${prefix}${text(tool.name) || 'namespace'}.`);
        continue;
      }
      total++;
      names.add(prefix + (text(tool.name) || text(tool.function?.name) || text(tool.type) || 'Unnamed tool'));
    }
  }
  collect(definitions);
  return { names: [...names], definitions: total };
}

export function cacheLevel(ratio) {
  const value = amount(ratio);
  return value < 0.75 ? 1 : value < 0.90 ? 2 : value < 0.95 ? 3 : 4;
}
export function normalizeSnapshot(value) {
  if (!value || !Number.isFinite(value.revision) || !Number.isFinite(value.startedAt)
      || (value.sessions != null && !Array.isArray(value.sessions))
      || (value.columns != null && !Array.isArray(value.columns))) throw new Error('Invalid activity snapshot.');
  const columns = list(value.columns);
  if (columns.some(column => !column || !Number.isFinite(column.start) || !Number.isFinite(column.end))) throw new Error('Invalid chronology.');
  const ids = new Set(), turnIDs = new Set();
  const sessions = list(value.sessions).map(session => {
    if (!session || typeof session.id !== 'string' || ids.has(session.id) || (session.turns != null && !Array.isArray(session.turns))) throw new Error('Invalid session.');
    ids.add(session.id);
    const turns = list(session.turns);
    for (const turn of turns) {
      if (!turn || typeof turn.id !== 'string' || turnIDs.has(turn.id) || !Number.isInteger(turn.column) || turn.column < 0 || turn.column >= columns.length) throw new Error('Invalid turn position.');
      turnIDs.add(turn.id);
    }
    return { ...session, turns };
  });
  return { ...value, columns, sessions };
}

// Keep Go's family order, positions, and cache levels through every filter.
export function filterSessions(sessions, query = '', status = 'all') {
  const terms = query.toLocaleLowerCase().trim().split(/\s+/).filter(Boolean);
  return list(sessions).map(session => {
    const sessionText = [session.label, session.id, session.client].filter(Boolean).join(' ').toLocaleLowerCase();
    const turns = list(session.turns).filter(turn => {
      if (status !== 'all' && statusOf(turn) !== status) return false;
      const haystack = `${sessionText} ${[turn.id, turn.modelName, turn.modelId, turn.instructionPreview, turn.trigger, turn.error, turn.number].filter(v => v != null).join(' ')}`.toLocaleLowerCase();
      return terms.every(term => haystack.includes(term));
    });
    return { ...session, turns };
  }).filter(session => session.turns.length > 0);
}
export function summarize(sessions) {
  const totals = { sessions: sessions.length, turns: 0, active: 0, tools: 0, tokens: 0, input: 0, cached: 0 };
  for (const session of sessions) for (const turn of session.turns) {
    totals.turns++;
    if (turn.status === 'running' || turn.status === 'waiting') totals.active++;
    totals.tools += amount(turn.toolCount);
    totals.tokens += amount(turn.totalTokens);
    totals.input += amount(turn.inputTokens);
    totals.cached += amount(turn.cachedTokens);
  }
  totals.cacheHitRate = totals.input ? totals.cached / totals.input : null;
  return totals;
}
export function latestTurn(session) {
  return list(session?.turns).reduce((latest, turn) => {
    if (!latest) return turn;
    const difference = amount(turn.startedAt) - amount(latest.startedAt) || amount(turn.attempt) - amount(latest.attempt);
    return difference > 0 || (difference === 0 && text(turn.id) > text(latest.id)) ? turn : latest;
  }, null);
}
export function selectionForHit(turnID, sessionID, sessions) {
  if (turnID) return sessions.some(session => session.turns.some(turn => turn.id === turnID)) ? turnID : null;
  return sessionID ? latestTurn(sessions.find(session => session.id === sessionID))?.id || null : null;
}
export function neighborTurn(sessions, id, key, wholeGrid = false) {
  const rows = sessions.map(session => [...session.turns].sort((a, b) => a.column - b.column || a.number - b.number));
  const row = rows.findIndex(turns => turns.some(turn => turn.id === id));
  if (row < 0) return null;
  const col = rows[row].findIndex(turn => turn.id === id);
  if (key === 'Home') return (wholeGrid ? rows[0] : rows[row])[0]?.id || null;
  if (key === 'End') return (wholeGrid ? rows.at(-1) : rows[row]).at(-1)?.id || null;
  if (key === 'ArrowLeft') return rows[row][Math.max(0, col - 1)]?.id || null;
  if (key === 'ArrowRight') return rows[row][Math.min(rows[row].length - 1, col + 1)]?.id || null;
  const nextRow = row + (key === 'ArrowUp' ? -1 : key === 'ArrowDown' ? 1 : 0);
  if (nextRow === row || !rows[nextRow]) return null;
  const column = rows[row][col].column;
  return rows[nextRow].reduce((best, turn) => Math.abs(turn.column - column) < Math.abs(best.column - column) ? turn : best).id;
}
export function forkLinks(sessions) {
  const byID = new Map(sessions.map((session, row) => [session.id, { session, row }]));
  const links = [];
  sessions.forEach((session, row) => {
    const parent = byID.get(session.parentId);
    if (session.detached || parent?.session.detached) return;
    const from = parent?.session.turns.find(turn => turn.id === session.forkTurnId);
    const to = session.turns.filter(turn => turn.id !== session.forkTurnId).reduce((first, turn) => !first || turn.column < first.column ? turn : first, null);
    if (from && to) links.push({ from, to, fromSession: parent.session.id, toSession: session.id, fromRow: parent.row, toRow: row });
  });
  return links;
}
export function connectorPath({ from, to, fromRow, toRow }) {
  const y1 = fromRow * ROW + ROW / 2, y2 = toRow * ROW + ROW / 2;
  const direction = y2 >= y1 ? 1 : -1;
  const x1 = from.column * CELL + CELL / 2;
  // The marker tip is 1px beyond its reference point: land on the mark's left edge.
  const x2 = to.column * CELL + 3;
  // One elbow: leave the parent vertically and enter the child's left midpoint.
  const points = [[x1, y1 + direction * 10], [x1, y2], [x2, y2]];
  let path = `M ${points[0].join(' ')}`;
  for (let i = 1; i < points.length - 1; i++) {
    const a = points[i - 1], b = points[i], c = points[i + 1];
    const before = Math.hypot(b[0] - a[0], b[1] - a[1]), after = Math.hypot(c[0] - b[0], c[1] - b[1]);
    if (!before || !after) { path += ` L ${b.join(' ')}`; continue; }
    const radius = Math.min(3, before / 2, after / 2);
    const entry = b.map((v, j) => v - (v - a[j]) / before * radius);
    const exit = b.map((v, j) => v + (c[j] - v) / after * radius);
    path += ` L ${entry.join(' ')} Q ${b.join(' ')} ${exit.join(' ')}`;
  }
  return `${path} L ${points.at(-1).join(' ')}`;
}
export function reconcileSessionRows(previous, sessions, create, update) {
  const rows = new Map();
  sessions.forEach((session, index) => {
    const row = previous.get(session.id) || create(session);
    update(row, session, index);
    rows.set(session.id, row);
  });
  return { rows, removed: [...previous].filter(([id]) => !rows.has(id)).map(([, row]) => row) };
}
export function rowAnimationOffset(delta, elapsed, reducedMotion = false) {
  if (reducedMotion || elapsed >= REORDER_DURATION) return 0;
  return delta * (1 - Math.max(0, elapsed / REORDER_DURATION)) ** 3;
}

export function captureGeneration(version) {
  return text(version).match(/^(?:W\/)?"?(\d+)-\d+"?$/)?.[1] ?? null;
}

class HTTPError extends Error {
  constructor(status) { super(`Gateway request failed (HTTP ${status}).`); this.status = status; }
}
export function createTransport({ bridge = null, fetcher = (...args) => fetch(...args) } = {}) {
  if (bridge?.call) return {
    poll: version => bridge.call('GatewayVisualizer.Poll', version),
    detail: id => bridge.call('GatewayVisualizer.Detail', id)
  };
  async function request(url, signal, version, consume) {
    const controller = new AbortController();
    const abort = () => controller.abort();
    signal?.addEventListener('abort', abort, { once: true });
    if (signal?.aborted) controller.abort();
    const timeout = setTimeout(abort, 15000);
    try {
      const headers = { Accept: 'application/json' };
      if (version) headers['If-None-Match'] = version;
      const response = await fetcher(url, { signal: controller.signal, credentials: 'same-origin', cache: 'no-store', headers });
      return await consume(response);
    } finally { clearTimeout(timeout); signal?.removeEventListener('abort', abort); }
  }
  return {
    poll(version, signal) {
      return request('./api/snapshot', signal, version, async response => {
        if (response.status === 304) return { status: 'enabled', version, snapshot: null };
        if (response.status === 404) return { status: 'disabled', version: '', snapshot: null };
        if (!response.ok) throw new HTTPError(response.status);
        return { status: 'enabled', version: response.headers?.get('ETag') || '', snapshot: await response.json() };
      });
    },
    detail(id, signal) {
      return request(`./api/turns/${encodeURIComponent(id)}`, signal, '', async response => {
        if (!response.ok) throw new HTTPError(response.status);
        return response.json();
      });
    }
  };
}

// One wall-clock start per second, never overlapping; unchanged versions do no render work.
export class GatewayFeed {
  constructor({ transport = createTransport(), now = () => performance.now(), setTimer = (callback, delay) => globalThis.setTimeout(callback, delay), clearTimer = timer => globalThis.clearTimeout(timer), onSnapshot = () => {}, onDetail = () => {}, onDetailError = () => {}, onConnection = () => {} } = {}) {
    Object.assign(this, { transport, now, setTimer, clearTimer, onSnapshot, onDetail, onDetailError, onConnection });
    this.stopped = true;
    this.epoch = 0;
    this.detailTicket = 0;
    this.lastStart = -Infinity;
    this.version = '';
    this.selected = null;
    this.snapshot = null;
  }
  start() {
    if (!this.stopped) return this.inflight || Promise.resolve();
    this.stopped = false;
    this.epoch++;
    return this.poll();
  }
  stop() {
    this.stopped = true;
    this.epoch++;
    this.clearTimer(this.timer);
    this.timer = null;
    this.pollAbort?.abort();
    this.detailAbort?.abort();
    if (this.selected && this.detailFlight) this.detailFailed = true;
    this.detailTicket++;
    this.detailFlight = null;
    this.detailQueued = false;
    // Keep an outstanding native Poll until it settles: restart must not overlap it.
  }
  schedule() {
    this.clearTimer(this.timer);
    if (!this.stopped) this.timer = this.setTimer(() => { this.timer = null; this.poll(); }, Math.max(0, this.lastStart + POLL_INTERVAL - this.now()));
  }
  poll() {
    if (this.stopped) return Promise.resolve();
    if (this.inflight) return this.inflight;
    if (this.now() < this.lastStart + POLL_INTERVAL) { this.schedule(); return Promise.resolve(); }
    this.clearTimer(this.timer);
    this.lastStart = this.now();
    const epoch = this.epoch;
    this.pollAbort = new AbortController();
    this.inflight = this.receivePoll(epoch, this.pollAbort.signal).finally(() => {
      this.inflight = null;
      this.schedule();
    });
    return this.inflight;
  }
  async receivePoll(epoch, signal) {
    try {
      const reply = await this.transport.poll(this.version, signal);
      if (this.stopped || epoch !== this.epoch) return;
      if (!reply || !['enabled', 'disabled'].includes(reply.status) || typeof reply.version !== 'string') throw new Error('Invalid poll response.');
      if (reply.status === 'disabled') {
        if (this.status !== 'disabled' || this.snapshot || this.selected) {
          this.snapshot = null;
          this.select(null);
          this.onSnapshot(null);
        }
        this.status = 'disabled';
        this.version = reply.version;
        this.onConnection('disabled', 'Visualizer is disabled. Enable it in the app to resume activity.');
        return;
      }
      const previousGeneration = captureGeneration(this.version), nextGeneration = captureGeneration(reply.version);
      if (previousGeneration !== null && nextGeneration !== null && previousGeneration !== nextGeneration) {
        this.snapshot = null;
        this.select(null); // Capture resets can reuse turn IDs; invalidate every old detail ticket.
        this.onSnapshot(null); // Drop keyed rows before reconciling reused session IDs.
      }
      if (reply.snapshot != null) {
        const snapshot = normalizeSnapshot(reply.snapshot);
        this.onSnapshot(snapshot);
        this.snapshot = snapshot;
      } else if (!this.snapshot) {
        this.version = '';
        throw new Error('Initial snapshot missing.');
      }
      this.version = reply.version;
      this.status = 'enabled';
      this.onConnection('enabled', '');
      if (this.selected && (reply.snapshot != null || this.detailFailed)) this.loadDetail();
    } catch (error) {
      if (this.stopped || epoch !== this.epoch) return;
      this.status = 'error';
      this.onConnection('error', 'Cannot load Gateway activity. Retrying automatically; the last available activity is shown.');
    }
  }
  select(id) {
    this.selected = id;
    this.detailTicket++;
    this.detailAbort?.abort();
    this.detailFlight = null;
    this.detailQueued = false;
    this.detailFailed = false;
    if (!id) { this.onDetail(null); return Promise.resolve(); }
    return this.loadDetail();
  }
  loadDetail() {
    const id = this.selected;
    if (!id || this.stopped) return Promise.resolve();
    if (this.detailFlight) { this.detailQueued = true; return this.detailFlight; }
    const ticket = ++this.detailTicket, epoch = this.epoch;
    this.detailQueued = false;
    const controller = new AbortController();
    this.detailAbort = controller;
    this.detailFlight = Promise.resolve().then(() => this.transport.detail(id, controller.signal)).then(detail => {
      if (this.stopped || epoch !== this.epoch || ticket !== this.detailTicket || id !== this.selected) return;
      if (!detail) throw new HTTPError(404);
      if (detail.id !== id) throw new Error('Unexpected turn response.');
      this.onDetail(detail);
      this.detailFailed = false;
    }).catch(error => {
      if (this.stopped || epoch !== this.epoch || ticket !== this.detailTicket || id !== this.selected) return;
      this.detailFailed = error.status !== 404;
      this.onDetailError(error.status === 404 ? 'This turn is no longer retained. Select another turn.' : 'Could not refresh this turn. Retrying automatically; previously loaded details are still shown.');
    }).finally(() => {
      if (ticket !== this.detailTicket || epoch !== this.epoch) return;
      this.detailFlight = null;
      if (this.detailQueued && !this.stopped) this.loadDetail();
    });
    return this.detailFlight;
  }
}

function el(tag, className = '', content) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (content !== undefined) node.textContent = content;
  return node;
}
function setText(node, value) { const next = String(value); if (node.textContent !== next) node.textContent = next; }
function svg(tag, attributes = {}) {
  const node = document.createElementNS('http://www.w3.org/2000/svg', tag);
  for (const [name, value] of Object.entries(attributes)) node.setAttribute(name, value);
  return node;
}
function metricList(node, entries) {
  while (node.children.length > entries.length) node.lastElementChild.remove();
  entries.forEach(([label, value, title], index) => {
    let item = node.children[index];
    if (!item) { item = el('div'); item.append(el('dt'), el('dd')); node.append(item); }
    setText(item.children[0], label); setText(item.children[1], value);
    if (title) item.title = title; else item.removeAttribute('title');
  });
}
function setStatus(node, status) { node.dataset.status = status; setText(node, statusText(status)); }

export function initializeApp() {
  const $ = id => document.getElementById(id);
  const state = { snapshot: null, rows: [], selected: null, detail: null, focusID: null, connection: 'loading', rowNodes: new Map(), cellNodes: new Map(), turnIndex: new Map(), sectionNodes: new Map(), callNodes: new Map(), reduceMotion: false, links: [] };
  const matrix = $('matrix');
  const axis = el('div', 'matrix-axis'); axis.setAttribute('role', 'row');
  const rowContainer = el('div', 'matrix-rows'); rowContainer.setAttribute('role', 'rowgroup');
  const definitions = svg('svg', { class: 'matrix-defs', 'aria-hidden': 'true', focusable: 'false' });
  const overlay = svg('svg', { class: 'fork-connectors', 'aria-hidden': 'true', focusable: 'false' });
  matrix.append(axis, definitions, rowContainer, overlay);
  let motionFrame = null;
  let tooltipTarget = null, tooltipTimer = null;
  const feed = new GatewayFeed({
    transport: createTransport({ bridge: window.mygo }),
    onSnapshot(snapshot) {
      state.snapshot = snapshot;
      if (!snapshot) { state.selected = null; state.detail = null; renderDetail(null); }
      renderActivity();
    },
    onDetail(detail) { state.detail = detail; renderDetail(detail); },
    onDetailError(message) { setText($('detail-state'), message); },
    onConnection(status, message) {
      state.connection = status;
      $('notice').hidden = status === 'enabled';
      $('notice').dataset.state = status;
      setText($('notice'), message);
      if (!state.snapshot) renderEmpty();
    }
  });

  function renderEmpty() {
    let title, message;
    if (state.connection === 'disabled') { title = 'Visualizer is disabled'; message = 'Activity resumes when the visualizer is enabled.'; }
    else if (!state.snapshot && state.connection === 'error') { title = 'Gateway is unavailable'; message = 'Activity will appear when the connection returns.'; }
    else if (!state.snapshot) { title = 'Loading activity'; message = 'Waiting for the Gateway.'; }
    else if (!state.snapshot.sessions.some(session => session.turns.length)) { title = 'Waiting for the first turn'; message = 'Send a request through the LLM Gateway to start a session.'; }
    else { title = 'No matching turns'; message = 'Try a different search or clear the status filter.'; }
    setText($('empty-title'), title); setText($('empty-message'), message);
  }
  function captureFrame() {
    const scroll = $('matrix-scroll');
    const column = Math.floor(Math.max(0, scroll.scrollLeft - MATRIX_GUTTER) / CELL);
    return { top: scroll.scrollTop, left: scroll.scrollLeft,
      rowID: state.rows[Math.floor(scroll.scrollTop / ROW)]?.id, rowOffset: scroll.scrollTop % ROW,
      columnStart: state.renderedColumns?.[column]?.start, columnOffset: scroll.scrollLeft - MATRIX_GUTTER - column * CELL,
      focusID: document.activeElement?.dataset?.turnId, focusSession: document.activeElement?.dataset?.sessionId };
  }
  function restoreFrame(frame) {
    const scroll = $('matrix-scroll');
    const row = state.rows.findIndex(session => session.id === frame.rowID);
    const column = state.snapshot?.columns.findIndex(item => item.start === frame.columnStart) ?? -1;
    scroll.scrollTop = row < 0 ? frame.top : row * ROW + frame.rowOffset;
    scroll.scrollLeft = column < 0 ? frame.left : MATRIX_GUTTER + column * CELL + frame.columnOffset;
    syncScrollDecorations();
    const target = frame.focusID ? state.cellNodes.get(frame.focusID) : frame.focusSession ? state.rowNodes.get(frame.focusSession)?.button : null;
    if (target && document.activeElement !== target) target.focus({ preventScroll: true });
    else if ((frame.focusID || frame.focusSession) && !target) scroll.focus({ preventScroll: true });
  }
  function cancelRowMotion() {
    if (motionFrame !== null) cancelAnimationFrame(motionFrame);
    motionFrame = null;
    for (const row of state.rowNodes.values()) row.root.style.transform = '';
  }
  function renderActivity() {
    const frame = captureFrame(), tooltipID = tooltipTarget?.dataset.turnId;
    const oldTops = new Map([...state.rowNodes].map(([id, row]) => [id, row.root.getBoundingClientRect().top]));
    cancelRowMotion(); hideTooltip();
    const snapshot = state.snapshot;
    state.rows = filterSessions(snapshot?.sessions, $('search').value, $('status').value);
    state.turnIndex = new Map();
    for (const session of list(snapshot?.sessions)) for (const turn of session.turns) state.turnIndex.set(turn.id, { turn, session });
    const totals = summarize(state.rows);
    metricList($('totals'), [['sessions', count(totals.sessions)], ['turns', count(totals.turns)], ['active', count(totals.active)], ['tool calls', count(totals.tools)], ['tokens', compactFormat.format(totals.tokens), `${count(totals.tokens)} total tokens`], ['cache hit', percent(totals.cacheHitRate), 'Cached input tokens / input tokens']]);
    $('clear-filters').hidden = !$('search').value && $('status').value === 'all';
    $('empty-state').hidden = state.rows.length > 0;
    $('matrix-panel').hidden = !state.rows.length;
    renderEmpty();
    renderMatrix();
    const columns = snapshot?.columns || [];
    state.renderedColumns = columns;
    setText($('time-range'), columns.length ? `${date(columns[0].start)} — ${date(columns.at(-1).end)}` : '');
    restoreFrame(frame);
    renderDetailContext();
    const deltas = new Map();
    for (const [id, row] of state.rowNodes) if (oldTops.has(id)) {
      const delta = oldTops.get(id) - row.root.getBoundingClientRect().top;
      if (Math.abs(delta) > 0.5) deltas.set(id, delta);
    }
    animateRows(deltas);
    if ((!deltas.size || state.reduceMotion) && state.cellNodes.has(tooltipID)) showTooltip(state.cellNodes.get(tooltipID));
  }

  function renderColumns(columns) {
    const signature = columns.map(column => `${column.start}:${column.end}:${column.gap ? 1 : 0}`).join(',');
    if (state.columnSignature === signature) return;
    state.columnSignature = signature;
    matrix.style.setProperty('--columns', columns.length);
    const corner = el('div', 'axis-corner', 'Session'); corner.setAttribute('role', 'columnheader');
    const fragment = document.createDocumentFragment(); fragment.append(corner);
    let lastLabel = -5;
    columns.forEach((column, i) => {
      const slot = el('div', `axis-slot${column.gap ? ' axis-gap' : ''}`);
      slot.setAttribute('role', 'columnheader'); slot.setAttribute('aria-colindex', i + 2);
      slot.setAttribute('aria-label', column.gap ? `Idle gap, ${date(column.start)} to ${date(column.end)}` : date(column.start));
      if (column.gap) { slot.textContent = '··'; slot.title = `Idle gap: ${duration(column.end - column.start)}`; }
      else if (i - lastLabel >= 4 && (i < columns.length - 2 || i === 0)) { slot.append(el('span', 'axis-label', timeFormat.format(new Date(column.start)))); lastLabel = i; }
      fragment.append(slot);
    });
    axis.replaceChildren(fragment);
    const defs = svg('defs');
    const empty = svg('pattern', { id: 'empty-cells', width: CELL, height: ROW, patternUnits: 'userSpaceOnUse' });
    empty.append(svg('rect', { x: 4, y: 14, width: 20, height: 20, rx: 4, class: 'empty-pattern-cell' }));
    const lattice = svg('pattern', { id: 'row-lattice', width: Math.max(CELL, columns.length * CELL), height: ROW, patternUnits: 'userSpaceOnUse' });
    lattice.append(svg('rect', { width: Math.max(CELL, columns.length * CELL), height: ROW, fill: 'url(#empty-cells)' }));
    columns.forEach((column, i) => { if (column.gap) lattice.append(svg('path', { d: `M ${i * CELL + 12} 19 l -3 10 M ${i * CELL + 17} 19 l -3 10`, class: 'gap-pattern-mark' })); });
    const marker = svg('marker', { id: 'fork-arrow', markerWidth: 5, markerHeight: 5, refX: 4, refY: 2.5, orient: 'auto', markerUnits: 'userSpaceOnUse' });
    marker.append(svg('path', { d: 'M 0 0 L 5 2.5 L 0 5 Z', class: 'fork-arrow' }));
    defs.append(empty, lattice, marker); definitions.replaceChildren(defs);
  }
  function createRow(session) {
    const root = el('div', 'matrix-row'); root.setAttribute('role', 'row');
    const label = el('div', 'session-label'); label.setAttribute('role', 'rowheader');
    const button = el('button', 'session-button'); button.type = 'button'; button.dataset.sessionId = session.id;
    const name = el('span', 'session-name'), meta = el('span', 'session-meta');
    button.append(name, meta); label.append(button);
    const track = el('div', 'matrix-track'); track.setAttribute('role', 'presentation');
    const background = svg('svg', { class: 'row-background', height: ROW, 'aria-hidden': 'true', focusable: 'false' });
    background.append(svg('rect', { width: '100%', height: ROW, fill: 'url(#row-lattice)' }));
    track.append(background); root.append(label, track);
    return { root, label, button, name, meta, track, background, cells: new Map() };
  }
  function updateRow(row, session, index) {
    row.root.setAttribute('aria-rowindex', index + 2);
    const previousFamily = state.rows[index - 1]?.groupId || state.rows[index - 1]?.id;
    row.root.classList.toggle('family-start', index > 0 && previousFamily !== (session.groupId || session.id));
    row.button.title = `${session.label || session.id}\n${session.id}${session.parentId ? `\nFork of ${session.parentId}${session.forkTurnId ? ` at ${session.forkTurnId}` : ''}${session.detached ? ' · sorted independently' : ''}` : ''}`;
    row.button.setAttribute('aria-label', `${session.label || session.id}, open latest turn`);
    setText(row.name, `${session.parentId ? '↳ ' : ''}${session.label || session.id}`);
    setText(row.meta, `${session.client || 'Gateway'} · ${count(session.turns.length)} turns`);
    row.background.setAttribute('width', state.snapshot.columns.length * CELL);
    const keep = new Set();
    for (const turn of session.turns) {
      keep.add(turn.id);
      let cell = row.cells.get(turn.id);
      if (!cell) {
        const slot = el('div', 'matrix-slot'); slot.setAttribute('role', 'gridcell');
        const button = el('button', 'turn-cell'); button.type = 'button'; button.dataset.turnId = turn.id;
        const mark = el('span', 'cell-mark'); mark.setAttribute('aria-hidden', 'true');
        button.append(mark); slot.append(button); row.track.append(slot);
        cell = { slot, button, mark }; row.cells.set(turn.id, cell);
      }
      cell.slot.style.left = `${MATRIX_GUTTER + turn.column * CELL}px`;
      cell.slot.setAttribute('aria-colindex', turn.column + 2);
      cell.button.tabIndex = turn.id === state.focusID ? 0 : -1;
      cell.button.setAttribute('aria-label', `Turn ${turn.number}, ${statusText(statusOf(turn))}, ${percent(turn.cacheHitRate)} cache hit, ${count(turn.toolCount)} tool calls, ${date(turn.startedAt)}, ${turn.modelName || turn.modelId || 'unknown model'}`);
      setText(cell.mark, turn.status === 'failed' ? '×' : statusOf(turn) === 'unknown' ? '?' : '');
      const level = Number.isInteger(turn.level) && turn.level >= 1 && turn.level <= 4 ? turn.level : cacheLevel(turn.cacheHitRate);
      cell.mark.style.setProperty('--cell-color', `var(--heat-${level})`);
      cell.mark.dataset.level = level;
      cell.mark.dataset.ratio = Math.round(amount(turn.cacheHitRate) * 100);
      cell.mark.dataset.status = statusOf(turn);
      state.cellNodes.set(turn.id, cell.button);
    }
    for (const [id, cell] of row.cells) if (!keep.has(id)) { cell.slot.remove(); row.cells.delete(id); }
  }
  function renderMatrix() {
    const columns = state.snapshot?.columns || [];
    renderColumns(columns);
    matrix.setAttribute('aria-rowcount', state.rows.length + 1);
    matrix.setAttribute('aria-colcount', columns.length + 1);
    const visibleIDs = new Set(state.rows.flatMap(session => session.turns.map(turn => turn.id)));
    state.focusID = visibleIDs.has(state.focusID) ? state.focusID : visibleIDs.has(state.selected) ? state.selected : state.rows[0]?.turns[0]?.id;
    state.cellNodes = new Map();
    const result = reconcileSessionRows(state.rowNodes, state.rows, createRow, updateRow);
    for (const row of result.removed) row.root.remove();
    state.rowNodes = result.rows;
    let index = 0;
    for (const row of state.rowNodes.values()) {
      const before = rowContainer.children[index++] || null;
      if (before === row.root) continue;
      if (rowContainer.moveBefore && row.root.parentNode === rowContainer) rowContainer.moveBefore(row.root, before);
      else rowContainer.insertBefore(row.root, before);
    }
    updateSelection();
    overlay.setAttribute('width', columns.length * CELL); overlay.setAttribute('height', state.rows.length * ROW);
    const visibleRows = new Map(state.rows.map((session, i) => [session.id, i]));
    state.links = forkLinks(state.snapshot?.sessions || []).filter(link => visibleIDs.has(link.from.id) && visibleIDs.has(link.to.id)).map(link => ({ ...link, fromRow: visibleRows.get(link.fromSession), toRow: visibleRows.get(link.toSession), path: svg('path', { class: 'fork-line', 'marker-end': 'url(#fork-arrow)' }) }));
    overlay.replaceChildren(...state.links.map(link => link.path));
  }
  function syncScrollDecorations() {
    const covered = Math.max(0, $('matrix-scroll').scrollLeft - MATRIX_GUTTER);
    $('matrix-panel').classList.toggle('has-covered-columns', covered > 0);
    overlay.style.clipPath = `inset(0 0 0 ${covered}px)`;
  }
  function drawConnectors(offsets = new Map()) {
    syncScrollDecorations();
    for (const link of state.links) link.path.setAttribute('d', connectorPath({ ...link, fromRow: link.fromRow + (offsets.get(link.fromSession) || 0) / ROW, toRow: link.toRow + (offsets.get(link.toSession) || 0) / ROW }));
  }
  function animateRows(deltas) {
    if (!deltas.size || state.reduceMotion) { drawConnectors(); return; }
    const started = performance.now();
    function frame(now) {
      const elapsed = now - started, offsets = new Map();
      for (const [id, delta] of deltas) {
        const offset = rowAnimationOffset(delta, elapsed, state.reduceMotion);
        const row = state.rowNodes.get(id);
        if (row) row.root.style.transform = offset ? `translateY(${offset}px)` : '';
        offsets.set(id, offset);
      }
      drawConnectors(offsets);
      if (elapsed < REORDER_DURATION && !state.reduceMotion) motionFrame = requestAnimationFrame(frame);
      else motionFrame = null;
    }
    frame(started);
  }
  function updateSelection() {
    for (const [id, button] of state.cellNodes) button.setAttribute('aria-pressed', String(id === state.selected));
    for (const row of state.rowNodes.values()) row.root.classList.toggle('has-selection', row.cells.has(state.selected));
  }
  function renderLegend() {
    const fragment = document.createDocumentFragment();
    for (const { level, label } of CACHE_BINS) {
      const item = el('li'), swatch = el('i', 'legend-swatch');
      swatch.style.setProperty('--cell-color', `var(--heat-${level})`); swatch.setAttribute('aria-hidden', 'true');
      item.append(swatch, el('span', '', label)); fragment.append(item);
    }
    $('cache-legend').replaceChildren(fragment);
  }
  function selectTurn(id) {
    hideTooltip();
    if (state.selected === id) return;
    state.selected = id; state.detail = null;
    renderDetail(null); updateSelection();
    if (id) setText($('detail-state'), 'Loading turn details…');
    feed.select(id);
  }
  function renderDetailContext() {
    const entry = state.turnIndex.get(state.selected), turn = state.detail || entry?.turn;
    const visible = state.cellNodes.has(state.selected);
    setText($('detail-title'), state.selected ? `Turn ${turn?.number ?? ''}`.trim() : 'Turn details');
    setText($('detail-context'), state.selected ? `${entry?.session.label || entry?.session.id || state.selected}${turn ? ` · ${date(turn.startedAt)}${turn.attempt > 1 ? ` · attempt ${turn.attempt}` : ''}${turn.trigger ? ` · ${turn.trigger}` : ''}` : ''}${!visible ? ' · Outside current view' : ''}` : 'Select a turn to inspect its work.');
  }
  function renderDetail(detail) {
    renderDetailContext(); $('detail-content').hidden = !detail;
    if (!detail) {
      $('detail-sections').replaceChildren(); $('detail-models').replaceChildren(); $('detail-metrics').replaceChildren();
      delete $('detail-models').dataset.signature;
      setText($('detail-error'), ''); setText($('detail-capture-notice'), ''); setText($('detail-state'), '');
      state.sectionNodes.clear(); state.callNodes.clear();
      return;
    }
    setText($('detail-state'), '');
    const models = list(detail.models).length ? detail.models : [{ id: detail.modelId, name: detail.modelName }];
    const signature = JSON.stringify(models);
    if ($('detail-models').dataset.signature !== signature) {
      const fragment = document.createDocumentFragment();
      for (const model of models) { const item = el('div'); item.append(el('p', 'model-name', model.name || model.id || 'Unknown model'), el('span', 'model-id', model.id || '')); fragment.append(item); }
      $('detail-models').replaceChildren(fragment); $('detail-models').dataset.signature = signature;
    }
    setStatus($('detail-status'), statusOf(detail));
    metricList($('detail-metrics'), [['Uncached input', count(detail.uncachedTokens)], ['Total tokens', count(detail.totalTokens)], ['Cache hit', percent(detail.cacheHitRate), 'Cached input tokens / input tokens'], ['Duration', duration(detail.durationMs)], ['First chunk', duration(detail.firstChunkMs)], ['Tool calls', count(detail.toolCount)], ['Requests', count(detail.requestCount)]]);
    $('detail-error').hidden = !detail.error; setText($('detail-error'), text(detail.error));
    $('detail-capture-notice').hidden = !detail.captureNotice; setText($('detail-capture-notice'), text(detail.captureNotice));
    renderCalls(list(detail.calls));
    for (const [key, title] of [['instruction', 'Instruction'], ['reply', 'Reply'], ['system', 'System'], ['tools', 'Tool definitions'], ['injections', 'Injected messages'], ['requests', 'Requests']]) {
      if (key === 'tools') { renderToolDefinitions(detail.tools); continue; }
      const section = ensureSection(key, title);
      setText(section.body, payloadText(detail[key]));
      if (key === 'instruction' || key === 'reply') setText(section.preview, contentPreview(detail[key]));
    }
  }
  function ensureSection(key, title, calls = false) {
    let section = state.sectionNodes.get(key);
    if (!section) {
      const root = el('details', 'payload-section'), summary = el('summary');
      root.dataset.section = key;
      const heading = el('span', 'payload-heading');
      const label = el('span', 'payload-title'), preview = el('span', 'payload-preview');
      heading.append(label, preview); summary.append(heading);
      const body = el(calls ? 'div' : 'pre', calls ? 'tool-calls' : 'payload-body');
      if (!calls) { body.tabIndex = 0; body.setAttribute('aria-label', title); }
      root.append(summary, body); $('detail-sections').append(root);
      section = { root, summary, label, preview, body }; state.sectionNodes.set(key, section);
    }
    setText(section.label, title); return section;
  }
  function renderToolDefinitions(value) {
    const section = ensureSection('tools', 'Tool definitions');
    const overview = toolOverview(value);
    const total = overview.names.length;
    setText(section.preview, `${count(total)} ${total === 1 ? 'tool' : 'tools'}${overview.definitions > total ? ` · ${count(overview.definitions)} definitions` : ''}`);
    if (!section.tools) {
      section.tools = el('ul', 'tool-names'); section.tools.setAttribute('aria-label', 'Enabled tools');
      section.root.insertBefore(section.tools, section.body);
    }
    const signature = JSON.stringify(overview.names);
    if (section.namesSignature !== signature) {
      section.tools.replaceChildren(...overview.names.map(name => el('li', '', name)));
      section.tools.hidden = !total;
      section.namesSignature = signature;
    }
    setText(section.body, payloadText(value));
  }
  function renderCalls(calls) {
    const section = ensureSection('calls', `Tool calls (${count(calls.length)})`, true), keep = new Set();
    if (!calls.length) {
      if (!section.body.querySelector('.no-calls')) section.body.replaceChildren(el('p', 'no-calls', 'No tool calls recorded.'));
      state.callNodes.clear(); return;
    }
    section.body.querySelector('.no-calls')?.remove();
    calls.forEach((call, index) => {
      const key = `${text(call.id)}:${index}`; keep.add(key);
      let item = state.callNodes.get(key);
      if (!item) {
        const root = el('details', 'tool-call'), summary = el('summary');
        const name = el('span'), meta = el('span', 'call-meta'); summary.append(name, meta);
        const input = el('pre', 'payload-body'), result = el('pre', 'payload-body'); input.tabIndex = result.tabIndex = 0;
        input.setAttribute('aria-label', 'Tool input'); result.setAttribute('aria-label', 'Tool result');
        root.append(summary, el('h4', '', 'Input'), input, el('h4', '', 'Result'), result);
        item = { root, name, meta, input, result }; state.callNodes.set(key, item); section.body.append(root);
      }
      setText(item.name, `${call.isError ? '× ' : ''}${call.name || 'Unnamed tool'}`);
      setText(item.meta, `${call.isError ? 'Failed' : call.endedAt ? 'Completed' : 'Waiting'}${call.endedAt && call.startedAt ? ` · ${duration(call.endedAt - call.startedAt)}` : ''}${call.id ? ` · ${call.id}` : ''}`);
      setText(item.input, payloadText(call.input)); setText(item.result, payloadText(call.result));
    });
    for (const [key, item] of state.callNodes) if (!keep.has(key)) { item.root.remove(); state.callNodes.delete(key); }
  }
  function hideTooltip() {
    clearTimeout(tooltipTimer); tooltipTarget?.removeAttribute('aria-describedby');
    tooltipTarget = null; $('turn-tooltip').hidden = true;
  }
  function showTooltip(button) {
    const entry = state.turnIndex.get(button?.dataset.turnId);
    if (!entry) return;
    const { turn } = entry, tip = $('turn-tooltip');
    hideTooltip(); tooltipTarget = button;
    const stats = el('div', 'tooltip-metrics');
    for (const [value, label] of [[percent(turn.cacheHitRate), 'cache hit'], [count(turn.totalTokens), 'total tokens'], [duration(turn.durationMs), 'duration'], [count(turn.toolCount), 'tool calls']]) {
      const item = el('div'); item.append(el('strong', '', value), el('span', '', label)); stats.append(item);
    }
    tip.replaceChildren(el('p', 'tooltip-instruction', turn.instructionPreview || 'Instruction not recorded'), el('p', 'tooltip-context', `Turn ${turn.number} · ${turn.modelName || turn.modelId || 'Unknown model'} · ${statusText(statusOf(turn))}`), el('p', 'tooltip-context', date(turn.startedAt)), stats);
    tip.hidden = false;
    const rect = button.getBoundingClientRect(), bounds = tip.getBoundingClientRect();
    const left = Math.max(12, Math.min(window.innerWidth - bounds.width - 12, rect.left + rect.width / 2 - bounds.width / 2));
    const top = rect.top - bounds.height - 8 >= 12 ? rect.top - bounds.height - 8 : Math.max(12, Math.min(window.innerHeight - bounds.height - 12, rect.bottom + 8));
    tip.style.left = `${left}px`; tip.style.top = `${top}px`;
    button.setAttribute('aria-describedby', 'turn-tooltip');
  }

  $('matrix-scroll').addEventListener('click', event => {
    const turn = event.target.closest('.turn-cell'), session = event.target.closest('.session-button');
    selectTurn(selectionForHit(turn?.dataset.turnId, session?.dataset.sessionId, state.snapshot?.sessions || []));
  });
  matrix.addEventListener('pointerover', event => { const button = event.target.closest('.turn-cell'); if (button && !button.contains(event.relatedTarget)) showTooltip(button); });
  matrix.addEventListener('pointerout', event => { const button = event.target.closest('.turn-cell'); if (button && !button.contains(event.relatedTarget)) tooltipTimer = setTimeout(hideTooltip, 150); });
  $('turn-tooltip').addEventListener('pointerenter', () => clearTimeout(tooltipTimer));
  $('turn-tooltip').addEventListener('pointerleave', hideTooltip);
  matrix.addEventListener('focusin', event => {
    const button = event.target.closest('.turn-cell');
    if (!button) return;
    const previous = state.cellNodes.get(state.focusID); if (previous) previous.tabIndex = -1;
    state.focusID = button.dataset.turnId; button.tabIndex = 0; showTooltip(button);
  });
  matrix.addEventListener('focusout', hideTooltip);
  matrix.addEventListener('keydown', event => {
    if (event.key === 'Escape') { hideTooltip(); selectTurn(null); return; }
    const button = event.target.closest('.turn-cell');
    if (!button || !['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const id = neighborTurn(state.rows, button.dataset.turnId, event.key, event.ctrlKey || event.metaKey), target = state.cellNodes.get(id);
    if (!target) return;
    target.focus({ preventScroll: true });
    const scroll = $('matrix-scroll'), bounds = scroll.getBoundingClientRect(), rect = target.getBoundingClientRect();
    const labelWidth = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--label-width'));
    if (rect.left < bounds.left + labelWidth) scroll.scrollLeft -= bounds.left + labelWidth - rect.left + 4;
    else if (rect.right > bounds.right) scroll.scrollLeft += rect.right - bounds.right + 4;
    if (rect.top < bounds.top + AXIS) scroll.scrollTop -= bounds.top + AXIS - rect.top + 4;
    else if (rect.bottom > bounds.bottom) scroll.scrollTop += rect.bottom - bounds.bottom + 4;
    showTooltip(target);
  });
  $('matrix-scroll').addEventListener('scroll', () => { hideTooltip(); syncScrollDecorations(); }, { passive: true });
  document.querySelector('main').addEventListener('scroll', hideTooltip, { passive: true });
  window.addEventListener('resize', hideTooltip, { passive: true });
  document.addEventListener('keydown', event => { if (event.key === 'Escape') hideTooltip(); });
  $('filters').addEventListener('submit', event => event.preventDefault());
  $('search').addEventListener('input', renderActivity); $('status').addEventListener('change', renderActivity);
  $('clear-filters').addEventListener('click', () => { $('search').value = ''; $('status').value = 'all'; renderActivity(); $('search').focus(); });

  // MyGo's native Theme.Source drives the WebView media query. Pinned preferences
  // expose reduceMotion/textScale, not a dark property (packages/runtime/src/types.ts).
  const darkMedia = window.matchMedia('(prefers-color-scheme: dark)');
  const motionMedia = window.matchMedia('(prefers-reduced-motion: reduce)');
  function syncPreferences(preferences = window.mygo?.preferences) {
    document.documentElement.dataset.theme = darkMedia.matches ? 'dark' : 'light';
    state.reduceMotion = Boolean(preferences?.reduceMotion) || motionMedia.matches;
    if (state.reduceMotion) { cancelRowMotion(); drawConnectors(); }
  }
  darkMedia.addEventListener('change', () => syncPreferences());
  motionMedia.addEventListener('change', () => syncPreferences());
  window.mygo?.on('mygo:preferences', syncPreferences);
  syncPreferences(); renderLegend(); renderActivity(); feed.start();
  window.addEventListener('pagehide', () => { feed.stop(); cancelRowMotion(); hideTooltip(); });
  window.addEventListener('pageshow', event => { if (event.persisted) { syncPreferences(); feed.start(); } });
  return { feed, state };
}

if (typeof document !== 'undefined' && document.getElementById('matrix')) initializeApp();
