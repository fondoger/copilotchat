import test from 'node:test';
import assert from 'node:assert/strict';
import { GatewayFeed, createTransport, normalizeSnapshot, filterSessions, summarize, cacheLevel, CACHE_BINS, latestTurn, selectionForHit, neighborTurn, forkLinks, connectorPath, reconcileSessionRows, rowAnimationOffset, REORDER_DURATION, captureGeneration, duration, payloadText, contentPreview, toolOverview } from './visualizer.js';

const time = 1763900000000;
const turn = (id, sessionId, column, extra = {}) => ({ id, sessionId, column, number: column + 1, level: 2, cacheHitRate: 0.8, status: 'completed', startedAt: time + column * 1000, ...extra });
const session = (id, turns, extra = {}) => ({ id, label: id, groupId: id, turns, ...extra });
const snapshot = (revision = 1, sessions = []) => ({ revision, startedAt: time, columns: Array.from({ length: 10 }, (_, i) => ({ start: time + i * 1000, end: time + (i + 1) * 1000, gap: i === 4 })), sessions });
const enabled = (revision = 1, sessions = []) => ({ status: 'enabled', version: `1-${revision}`, snapshot: snapshot(revision, sessions) });
const response = (body, status = 200, etag = '"1-1"') => ({ ok: status >= 200 && status < 300, status, headers: { get: key => key === 'ETag' ? etag : null }, json: async () => body });
const tick = () => new Promise(resolve => setImmediate(resolve));
function deferred() { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
class Clock {
  time = 0;
  sequence = 0;
  timers = new Map();
  now = () => this.time;
  setTimer = (callback, delay) => { const id = ++this.sequence; this.timers.set(id, { callback, at: this.time + delay }); return id; };
  clearTimer = id => this.timers.delete(id);
  async advance(ms) {
    const end = this.time + ms;
    await tick();
    while (true) {
      const next = [...this.timers].filter(([, timer]) => timer.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
      if (!next) break;
      this.time = next[1].at; this.timers.delete(next[0]); next[1].callback(); await tick();
    }
    this.time = end; await tick();
  }
}
function client(t, transport = {}) {
  const clock = new Clock(), frames = [], details = [], errors = [], states = [];
  const feed = new GatewayFeed({ transport: { poll: async () => enabled(), detail: async id => ({ id }), ...transport }, now: clock.now, setTimer: clock.setTimer, clearTimer: clock.clearTimer, onSnapshot: value => frames.push(value), onDetail: value => details.push(value), onDetailError: value => errors.push(value), onConnection: (state, message) => states.push({ state, message }) });
  t.after(() => feed.stop());
  return { feed, clock, frames, details, errors, states };
}

test('default browser timers keep the Window receiver during startup and polling', async t => {
  let scheduled = 0;
  t.mock.method(globalThis, 'setTimeout', function () { assert.equal(this, globalThis); scheduled++; return 42; });
  t.mock.method(globalThis, 'clearTimeout', function () { assert.equal(this, globalThis); });
  const feed = new GatewayFeed({ transport: { poll: async () => enabled() }, now: () => 0 });
  await feed.start();
  assert.equal(feed.status, 'enabled');
  assert.equal(scheduled, 1);
  feed.stop();
});

test('cache bins use exact inclusive boundaries and never assign an empty-cell level', () => {
  for (const [ratio, level] of [[0, 1], [.749999, 1], [.75, 2], [.899999, 2], [.9, 3], [.949999, 3], [.95, 4], [1, 4]]) assert.equal(cacheLevel(ratio), level);
  assert.equal(cacheLevel(null), 1);
  assert.deepEqual(CACHE_BINS.map(bin => bin.level), [1, 2, 3, 4]);
});

test('search includes current instruction and retains backend positions, levels, and family order', () => {
  const sessions = [
    session('parent', [turn('a', 'parent', 1), turn('b', 'parent', 8, { status: 'failed', level: 4, instructionPreview: 'Fix parser handling', modelName: 'Model Beta' })]),
    session('fork', [turn('c', 'fork', 9, { status: 'failed', level: 1, instructionPreview: 'Fix parser handling', modelName: 'Model Beta' })], { parentId: 'parent', groupId: 'parent' }),
    session('other', [turn('d', 'other', 3)])
  ];
  const original = JSON.stringify(sessions), result = filterSessions(sessions, 'parser beta', 'failed');
  assert.deepEqual(result.map(row => row.id), ['parent', 'fork']);
  assert.deepEqual(result.flatMap(row => row.turns.map(turn => [turn.id, turn.column, turn.level])), [['b', 8, 4], ['c', 9, 1]]);
  assert.equal(JSON.stringify(sessions), original);
  assert.deepEqual(filterSessions(sessions, '<script>'), []);
});

test('summary cache rate is input-weighted and counts waiting turns as active', () => {
  const totals = summarize([session('s', [
    turn('a', 's', 0, { status: 'running', inputTokens: 100, cachedTokens: 90, totalTokens: 110, toolCount: 2 }),
    turn('b', 's', 1, { status: 'waiting', inputTokens: 900, cachedTokens: 90, totalTokens: 920, toolCount: 3 })
  ])]);
  assert.equal(totals.cacheHitRate, .18); assert.equal(totals.active, 2);
  assert.equal(totals.tokens, 1030); assert.equal(totals.tools, 5);
  assert.equal(summarize([]).cacheHitRate, null);
});

test('row selection chooses latest timestamp then attempt and ID deterministically', () => {
  const turns = [turn('old', 's', 9, { startedAt: time - 1 }), turn('a', 's', 1, { startedAt: time, attempt: 2 }), turn('z', 's', 2, { startedAt: time, attempt: 2 }), turn('x', 's', 3, { startedAt: time, attempt: 1 })];
  const row = session('s', turns);
  assert.equal(latestTurn(row).id, 'z');
  assert.equal(latestTurn({ turns: [...turns].reverse() }).id, 'z');
  assert.equal(selectionForHit(undefined, 's', [row]), 'z');
  assert.equal(selectionForHit('a', 's', [row]), 'a');
  assert.equal(selectionForHit(undefined, undefined, [row]), null); // Empty matrix clears.
  assert.equal(selectionForHit('evicted', undefined, [row]), null);
  assert.equal(latestTurn({ turns: [] }), null);
});

test('keyboard navigation skips empty time columns and chooses nearest turn across rows', () => {
  const rows = [session('a', [turn('a1', 'a', 1), turn('a2', 'a', 8)]), session('b', [turn('b1', 'b', 2), turn('b2', 'b', 6)])];
  assert.equal(neighborTurn(rows, 'a1', 'ArrowRight'), 'a2');
  assert.equal(neighborTurn(rows, 'a2', 'ArrowDown'), 'b2');
  assert.equal(neighborTurn(rows, 'b1', 'ArrowUp'), 'a1');
  assert.equal(neighborTurn(rows, 'b2', 'Home'), 'b1');
  assert.equal(neighborTurn(rows, 'b2', 'Home', true), 'a1');
  assert.equal(neighborTurn(rows, 'a1', 'End', true), 'b2');
  assert.equal(neighborTurn(rows, 'gone', 'ArrowRight'), null);
});

test('fork connectors leave top or bottom center toward the true child endpoint', () => {
  const parent = session('p', [turn('shared', 'p', 2), turn('later', 'p', 7)]);
  const child = session('c', [turn('child-later', 'c', 8), turn('child-first', 'c', 5)], { parentId: 'p', forkTurnId: 'shared' });
  const link = forkLinks([parent, child])[0];
  assert.equal(link.from.id, 'shared'); assert.equal(link.to.id, 'child-first');
  assert.match(connectorPath(link), /^M 70 34 L 70 /); // Bottom center, not right side.
  const reversed = forkLinks([child, parent])[0];
  assert.match(connectorPath(reversed), /^M 70 62 L 70 /); // Top center toward the child above.
  assert.equal((connectorPath(link).match(/ Q /g) || []).length, 1);
  assert.equal((connectorPath(reversed).match(/ Q /g) || []).length, 1);
  assert.match(connectorPath(link), /L 143 72$/); // Target left-center; marker tip reaches x=144.
  assert.match(connectorPath(reversed), /L 143 24$/);
  for (const toRow of [0, 2]) {
    const adjacent = connectorPath({ from: { column: 0 }, to: { column: 1 }, fromRow: 1, toRow });
    assert.match(adjacent, new RegExp(`L 31 ${toRow * 48 + 24}$`));
    assert.doesNotMatch(adjacent, /C|S|NaN|Infinity/);
  }
  assert.doesNotMatch(connectorPath(link), /C|S|NaN|Infinity/);
});

test('detached forks retain provenance but never draw cross-group arrows', () => {
  const parent = session('p', [turn('shared', 'p', 0)]);
  const child = session('c', [turn('new', 'c', 1)], { parentId: 'p', forkTurnId: 'shared', groupId: 'p' });
  assert.equal(forkLinks([parent, child]).length, 1);
  const detached = { ...child, detached: true, groupId: 'c' };
  assert.deepEqual(forkLinks([parent, detached]), []);
  assert.equal(detached.parentId, 'p');
  assert.deepEqual(forkLinks([{ ...parent, detached: true }, child]), []);
});

test('keyed row reconciliation preserves surviving row/cell identity through reorders', () => {
  let creates = 0;
  const create = session => ({ id: session.id, button: {}, cells: new Map(), serial: ++creates });
  const update = (row, session, index) => { row.label = session.label; row.index = index; };
  const initial = reconcileSessionRows(new Map(), [session('a', []), session('b', []), session('gone', [])], create, update);
  const a = initial.rows.get('a'), b = initial.rows.get('b');
  const next = reconcileSessionRows(initial.rows, [session('b', [], { label: 'New label' }), session('new', []), session('a', [])], create, update);
  assert.equal(next.rows.get('a'), a); assert.equal(next.rows.get('b'), b);
  assert.deepEqual([...next.rows.keys()], ['b', 'new', 'a']);
  assert.equal(b.label, 'New label'); assert.equal(b.index, 0);
  assert.equal(creates, 4); assert.deepEqual(next.removed.map(row => row.id), ['gone']);
});

test('row and connector motion share a bounded 200ms offset and honor reduced motion', () => {
  assert.equal(REORDER_DURATION, 200);
  assert.equal(rowAnimationOffset(96, 0), 96);
  assert.equal(rowAnimationOffset(96, 100), 12);
  assert.equal(rowAnimationOffset(96, 200), 0);
  assert.equal(rowAnimationOffset(-48, 300), 0);
  assert.equal(rowAnimationOffset(96, 0, true), 0);
});

test('wire payload strings preserve full JSON precision and latency distinguishes null from zero', () => {
  const raw = '{"number":90071992547409931234,"text":"<script>untrusted</script>"}';
  assert.equal(payloadText(raw), raw);
  assert.equal(duration(null), '—'); assert.equal(duration(0), '0 ms'); assert.equal(duration(1250), '1.3 s');
  assert.deepEqual(normalizeSnapshot({ revision: 1, startedAt: time, sessions: null, columns: null }).sessions, []);
  assert.throws(() => normalizeSnapshot({ ...snapshot(), sessions: [session('s', [turn('x', 's', 20)])] }), /position/);
});

test('folded content previews extract readable text without changing detailed JSON', () => {
  const raw = '[{"type":"text","text":"Check <script>literal text</script>\\n  and 90071992547409931234"},{"type":"image","source":{"data":"private-binary"}},{"type":"thinking","thinking":"not a reply preview"}]';
  assert.equal(contentPreview(raw), 'Check <script>literal text</script> and 90071992547409931234 [Image]');
  assert.equal(payloadText(raw), raw);
  assert.equal(contentPreview({ role: 'assistant', content: [{ type: 'output_text', text: 'Finished.\nAll checks passed.' }] }), 'Finished. All checks passed.');
  assert.equal(contentPreview('Ordinary instruction'), 'Ordinary instruction');
  assert.equal(contentPreview(null), '');
  assert.equal(contentPreview('[]'), '');
  assert.equal(contentPreview('[unfinished JSON'), '[unfinished JSON');
  assert.equal(Array.from(contentPreview('🌿'.repeat(500))).length, 400);
});

test('tool overview lists enabled names across native, function and namespace definitions', () => {
  const tools = [
    { name: 'Read', input_schema: { properties: { name: { type: 'string' } } } },
    { type: 'function', function: { name: 'lookup' } },
    { type: 'namespace', name: 'files', tools: [{ type: 'function', name: 'write' }] },
    { name: 'Read', description: 'Updated definition' },
    { type: 'web_search' },
    { name: '<img src=x onerror=alert(1)>' }
  ];
  const raw = JSON.stringify(tools);
  assert.deepEqual(toolOverview(raw), { names: ['Read', 'lookup', 'files.write', 'web_search', '<img src=x onerror=alert(1)>'], definitions: 6 });
  assert.equal(payloadText(raw), raw);
  assert.deepEqual(toolOverview(null), { names: [], definitions: 0 });
  assert.deepEqual(toolOverview('not JSON'), { names: [], definitions: 0 });
});

test('native transport calls only the bound Poll and Detail methods', async () => {
  const calls = [];
  const transport = createTransport({ bridge: { call: async (...args) => { calls.push(args); return args[0].endsWith('Poll') ? enabled() : { id: args[1] }; } }, fetcher: () => assert.fail('native must not fetch') });
  await transport.poll('1-4'); await transport.detail('t3');
  assert.deepEqual(calls, [['GatewayVisualizer.Poll', '1-4'], ['GatewayVisualizer.Detail', 't3']]);
});

test('HTTP 304 does not parse a body or redraw and retains If-None-Match verbatim', async t => {
  let requests = 0, parses = 0;
  const headers = [];
  const transport = createTransport({ fetcher: async (url, options) => {
    headers.push(options.headers);
    if (++requests === 1) return response(snapshot());
    return { status: 304, json: () => { parses++; throw new Error('304 has no JSON'); } };
  } });
  const { feed, clock, frames } = client(t, transport);
  await feed.start(); await clock.advance(3000);
  assert.equal(requests, 4); assert.equal(parses, 0); assert.equal(frames.length, 1);
  assert.equal(headers[0]['If-None-Match'], undefined);
  assert.equal(headers[1]['If-None-Match'], '"1-1"');
});

test('poll starts follow one-second wall-clock cadence, not response time plus a second', async t => {
  const pending = deferred(), starts = [];
  const { feed, clock } = client(t, { poll: () => { starts.push(clock.now()); return starts.length === 1 ? pending.promise : Promise.resolve(enabled(starts.length)); } });
  const first = feed.start(); await clock.advance(400); pending.resolve(enabled()); await first;
  await clock.advance(600); await clock.advance(1000);
  assert.deepEqual(starts, [0, 1000, 2000]);
  feed.poll(); await clock.advance(999); assert.equal(starts.length, 3);
  await clock.advance(1); assert.equal(starts.at(-1), 3000);
});

test('slow polls never overlap or cause catch-up bursts; repeated requests coalesce', async t => {
  const pending = deferred(), starts = [];
  const { feed, clock } = client(t, { poll: () => { starts.push(clock.now()); return starts.length === 1 ? pending.promise : Promise.resolve(enabled(starts.length)); } });
  const first = feed.start();
  assert.equal(feed.poll(), first); assert.equal(feed.poll(), first);
  await clock.advance(1600); assert.deepEqual(starts, [0]);
  pending.resolve(enabled()); await first; await clock.advance(0);
  assert.deepEqual(starts, [0, 1600]);
  await clock.advance(999); assert.equal(starts.length, 2);
  await clock.advance(1); assert.deepEqual(starts, [0, 1600, 2600]);
});

test('unchanged native snapshots do no render or selected-detail work', async t => {
  let requests = 0;
  const { feed, clock, frames } = client(t, { poll: async version => version ? { status: 'enabled', version, snapshot: null } : enabled(), detail: async id => { requests++; return { id }; } });
  await feed.start(); await feed.select('a'); await clock.advance(3000);
  assert.equal(frames.length, 1); assert.equal(requests, 1);
});

test('late detail responses cannot replace a newer selection', async t => {
  const a = deferred(), b = deferred();
  const { feed, details } = client(t, { detail: id => id === 'a' ? a.promise : b.promise });
  await feed.start(); const old = feed.select('a'); await tick(); const current = feed.select('b');
  b.resolve({ id: 'b', reply: '<img src=x onerror=alert(1)>' }); await current;
  a.resolve({ id: 'a', reply: 'stale' }); await old;
  assert.deepEqual(details.map(detail => detail.id), ['b']);
});

test('slow details do not freeze polling and changes queue one follow-up inspector request', async t => {
  const pending = deferred(); let revisions = 0, requests = 0;
  const { feed, clock, frames, details } = client(t, { poll: async () => enabled(++revisions), detail: () => ++requests === 1 ? pending.promise : Promise.resolve({ id: 'a', reply: 'latest' }) });
  await feed.start(); const detail = feed.select('a'); await clock.advance(2000);
  assert.equal(frames.at(-1).revision, 3); assert.equal(requests, 1);
  pending.resolve({ id: 'a', reply: 'initial' }); await detail; await tick();
  assert.equal(requests, 2); assert.equal(details.at(-1).reply, 'latest');
});

test('disabled state clears activity and payloads then recovers automatically', async t => {
  let reply = enabled();
  const { feed, clock, frames, details, states } = client(t, { poll: async () => reply });
  await feed.start(); await feed.select('a');
  reply = { status: 'disabled', version: '2-0', snapshot: null }; await clock.advance(2000);
  assert.equal(states.at(-1).state, 'disabled'); assert.equal(feed.selected, null);
  assert.equal(frames.at(-1), null); assert.equal(details.at(-1), null);
  assert.equal(frames.filter(frame => frame === null).length, 1);
  reply = { ...enabled(1), version: '3-1' }; await clock.advance(1000);
  assert.equal(frames.at(-1).revision, 1); assert.equal(states.at(-1).state, 'enabled');
});

test('capture-generation changes discard reused IDs and invalidate pending details', async t => {
  assert.equal(captureGeneration('12-34'), '12'); assert.equal(captureGeneration('W/"12-34"'), '12');
  const pending = deferred(); let generation = 1;
  const rows = [session('s1', [turn('t3', 's1', 1)])];
  const { feed, clock, frames, details } = client(t, { poll: async () => ({ ...enabled(1, rows), version: `${generation}-1` }), detail: () => pending.promise });
  await feed.start(); const old = feed.select('t3'); await tick();
  generation = 2; await clock.advance(1000);
  assert.equal(feed.selected, null); assert.equal(frames.at(-2), null);
  assert.equal(frames.at(-1).sessions[0].turns[0].id, 't3');
  pending.resolve({ id: 't3', reply: 'old capture' }); await old;
  assert.deepEqual(details, [null]);
});

test('HTTP disabled and evicted details have distinct meanings', async t => {
  let disabled = false;
  const transport = createTransport({ fetcher: async url => url === './api/snapshot' && !disabled ? response(snapshot()) : response(null, 404) });
  const { feed, clock, errors, states } = client(t, transport);
  await feed.start(); await feed.select('gone');
  assert.match(errors.at(-1), /no longer retained/); assert.equal(states.at(-1).state, 'enabled');
  disabled = true; await clock.advance(1000); assert.equal(states.at(-1).state, 'disabled');
});

test('poll failures retain the last real frame and recover without a user action', async t => {
  let failure = false;
  const { feed, clock, frames, states } = client(t, { poll: async () => { if (failure) throw new Error('offline'); return enabled(7); } });
  await feed.start(); failure = true; await clock.advance(1000);
  assert.deepEqual(frames.map(frame => frame.revision), [7]); assert.equal(states.at(-1).state, 'error');
  failure = false; await clock.advance(1000); assert.equal(states.at(-1).state, 'enabled');
});

test('resuming a suspended page retries an interrupted detail even when the version is unchanged', async t => {
  const pending = deferred(); let requests = 0;
  const { feed, clock, details } = client(t, { poll: async version => version ? { status: 'enabled', version, snapshot: null } : enabled(), detail: () => ++requests === 1 ? pending.promise : Promise.resolve({ id: 'a', reply: 'resumed' }) });
  await feed.start(); const old = feed.select('a'); await tick();
  feed.stop(); feed.start(); await clock.advance(1000);
  assert.equal(requests, 2); assert.equal(details.at(-1).reply, 'resumed');
  pending.resolve({ id: 'a', reply: 'old page' }); await old;
  assert.equal(details.length, 1);
});

test('stopped/restarted native polls do not overlap and old results are ignored', async t => {
  const pending = deferred(); let calls = 0;
  const { feed, clock, frames } = client(t, { poll: () => ++calls === 1 ? pending.promise : Promise.resolve(enabled(2)) });
  const old = feed.start(); feed.stop(); feed.start();
  assert.equal(calls, 1); await clock.advance(1200);
  pending.resolve(enabled(1)); await old; await clock.advance(0);
  assert.equal(calls, 2); assert.deepEqual(frames.map(frame => frame.revision), [2]);
});
