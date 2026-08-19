'use strict';
/**
 * Queues — rate derivation, the self-throttle warning, and FastTrack detection.
 *
 * Weighted towards the two mechanics that are new to this page rather than the
 * page/collector wiring, which six pages before it already exercise:
 *
 *   RATES are derived from a byte counter, so every way a counter can lie —
 *   first sample, reset, idle, a reused RouterOS id — is a way the page can show
 *   a number nobody can explain.
 *
 *   THE WARNING must fire on a genuinely throttling queue and stay silent on an
 *   ordinary one. Only the second half is easy to get wrong quietly, so the
 *   positive controls matter as much as the negative ones.
 */

const { test } = require('node:test');
const assert = require('node:assert');
const fs   = require('fs');
const path = require('path');

const Q     = require('../src/collectors/queues');
const guard = require('../src/routeros/queueGuard');

const SRC = (...p) => fs.readFileSync(path.join(__dirname, '..', 'src', ...p), 'utf8');
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');

// Rows exactly as the live router answers: raw bps, pairs on simple, single
// values on tree, and no `dynamic` field on tree at all.
const simpleRow = (over) => Object.assign({
  '.id': '*1', name: 'lan-cap', target: '10.0.0.0/24',
  'max-limit': '15000000/20000000', 'limit-at': '5000000/5000000',
  bytes: '0/0', packets: '0/0', dropped: '0/0', 'queued-bytes': '0/0',
  rate: '0/0', disabled: 'false', dynamic: 'false', invalid: 'false', comment: '',
}, over || {});
const treeRow = (over) => Object.assign({
  '.id': '*1000000', name: 'yt', parent: 'global', 'packet-mark': 'pm',
  'max-limit': '10000000', bytes: '0', rate: '0', disabled: 'false',
}, over || {});

// ── Rate parsing ─────────────────────────────────────────────────────────────

test('rates parse from both the wire form and the form people type', () => {
  // The API answers in raw bps; the CLI and every operator use suffixes, and the
  // router accepts them on input. One function reads both.
  assert.strictEqual(guard.parseRate('15000000'), 15000000);
  assert.strictEqual(guard.parseRate('15M'), 15000000);
  assert.strictEqual(guard.parseRate('64k'), 64000);
  assert.strictEqual(guard.parseRate('1.5M'), 1500000);
  assert.strictEqual(guard.parseRate('nonsense'), null);
  assert.strictEqual(guard.parseRate(''), null);
});

test('zero is unlimited and is not the same as absent', () => {
  // RouterOS reads an unlimited queue back as "0/0" rather than omitting the
  // field, so collapsing 0 into null would report "no limit set" as "unknown".
  assert.deepStrictEqual(guard.parsePair('0/0'), { up: 0, down: 0 });
  assert.deepStrictEqual(guard.parsePair(''),    { up: null, down: null });
  assert.strictEqual(guard.parseRate(0), 0);
});

test('a single value fills both halves, which is what a tree needs', () => {
  assert.deepStrictEqual(guard.parsePair('10000000'), { up: 10000000, down: 10000000 });
  assert.deepStrictEqual(guard.parsePair('15000000/20000000'), { up: 15000000, down: 20000000 });
});

// ── Rate derivation ──────────────────────────────────────────────────────────

test('the first sample is seeded from the router, never fabricated', () => {
  // There is no window yet. Reporting a delta would be inventing one; reporting
  // 0 would claim an idle queue that may be saturating the line.
  const prev = new Map();
  const r = Q.buildQueueRows([simpleRow({ rate: '125000/250000', bytes: '1000/2000' })], [], prev, 1000);
  assert.strictEqual(r.simple[0].rateSource, 'router');
  // The router reports BYTES per second; the page works in bits.
  assert.strictEqual(r.simple[0].rateBps.up, 1000000);
  assert.strictEqual(r.simple[0].rateBps.down, 2000000);
  assert.strictEqual(r.simple[0].rateWindowMs, null);
});

test('with no router rate and no window, the rate is null rather than zero', () => {
  const prev = new Map();
  const row = simpleRow(); delete row.rate;
  const r = Q.buildQueueRows([row], [], prev, 1000);
  assert.strictEqual(r.simple[0].rateBps.up, null);
  assert.strictEqual(r.simple[0].rateSource, null);
});

test('the second sample is measured, and reports its window', () => {
  const prev = new Map();
  Q.buildQueueRows([simpleRow({ bytes: '1000/2000' })], [], prev, 1000);
  const r = Q.buildQueueRows([simpleRow({ bytes: '1000000/2000000' })], [], prev, 6000);
  assert.strictEqual(r.simple[0].rateSource, 'delta');
  assert.strictEqual(r.simple[0].rateWindowMs, 5000);
  // (1000000 - 1000) bytes * 8 / 5 s
  assert.strictEqual(Math.round(r.simple[0].rateBps.up), 1598400);
});

test('an idle queue falls to zero rather than holding a stale rate', () => {
  const prev = new Map();
  Q.buildQueueRows([simpleRow({ bytes: '1000/2000' })], [], prev, 1000);
  Q.buildQueueRows([simpleRow({ bytes: '9000/9000' })], [], prev, 6000);
  // Unchanged for longer than IDLE_AFTER_SEC.
  const r = Q.buildQueueRows([simpleRow({ bytes: '9000/9000' })], [], prev,
                             6000 + (Q.IDLE_AFTER_SEC + 5) * 1000);
  assert.strictEqual(r.simple[0].rateBps.up, 0);
  assert.strictEqual(r.simple[0].rateBps.down, 0);
});

test('an unchanged counter holds its last rate until the idle threshold', () => {
  // Between ticks a busy queue can report the same byte count. Dropping to zero
  // there would make a steady transfer flicker.
  const prev = new Map();
  Q.buildQueueRows([simpleRow({ bytes: '1000/1000' })], [], prev, 1000);
  const moved = Q.buildQueueRows([simpleRow({ bytes: '500000/500000' })], [], prev, 6000);
  const held  = Q.buildQueueRows([simpleRow({ bytes: '500000/500000' })], [], prev, 8000);
  assert.ok(moved.simple[0].rateBps.up > 0);
  assert.strictEqual(held.simple[0].rateBps.up, moved.simple[0].rateBps.up);
});

test('a counter reset never produces a negative rate', () => {
  const prev = new Map();
  Q.buildQueueRows([simpleRow({ bytes: '5000000/5000000' })], [], prev, 1000);
  const r = Q.buildQueueRows([simpleRow({ bytes: '10/10' })], [], prev, 6000);
  assert.strictEqual(r.simple[0].rateBps.up, 0);
  assert.strictEqual(r.simple[0].rateBps.down, 0);
});

test('a recreated queue reusing a RouterOS id does not inherit the old baseline', () => {
  // RouterOS reuses `*N` after a removal. Keyed on the id alone, the new queue's
  // first sample would be (its bytes - the dead queue's bytes) and read as a
  // multi-gigabit spike that never happened.
  const prev = new Map();
  Q.buildQueueRows([simpleRow({ '.id': '*1', name: 'old', bytes: '900000000/900000000' })], [], prev, 1000);
  const r = Q.buildQueueRows([simpleRow({ '.id': '*1', name: 'brand-new', bytes: '0/0', rate: '0/0' })],
                             [], prev, 6000);
  assert.strictEqual(r.simple[0].rateSource, 'router', 'treated as a first sample');
  assert.strictEqual(r.simple[0].rateBps.up, 0);
  assert.strictEqual(prev.size, 1, 'the dead queue baseline is gone');
});

test('forgetRates is called after every write that can move a counter', () => {
  // set and reset-counters can zero a counter. The Math.max(0, …) clamp hides
  // that as a zero, but the window AFTER it would be measured against a baseline
  // the router no longer agrees with.
  const src = stripComments(SRC('index.js'));
  for (const ev of ['queue:save', 'queue:remove', 'queue:resetCounters']) {
    const start = src.indexOf("socket.on('" + ev + "'");
    const next  = src.indexOf("socket.on('", start + 20);
    const body  = src.slice(start, next > 0 ? next : start + 4000);
    assert.ok(body.includes('forgetRates()'), ev + ' forgets the rate baselines');
  }
});

// ── Row shapes ───────────────────────────────────────────────────────────────

test('simple and tree are different shapes, not one shape with gaps', () => {
  const r = Q.buildQueueRows([simpleRow()], [treeRow()], new Map(), 1000);
  const s = r.simple[0], t = r.tree[0];
  assert.deepStrictEqual(s.maxLimit, { up: 15000000, down: 20000000 }, 'simple limits are a pair');
  assert.strictEqual(t.maxLimit, 10000000, 'tree limits are a single number');
  assert.strictEqual(typeof s.packetMarks, 'string', 'simple uses packet-marks, plural');
  assert.strictEqual(typeof t.packetMark,  'string', 'tree uses packet-mark, singular');
});

test('a tree row reports dynamic:false rather than undefined', () => {
  // /queue/tree has no such field. Reporting undefined would give the frontend
  // two shapes to render for one column.
  const r = Q.buildQueueRows([], [treeRow()], new Map(), 1000);
  assert.strictEqual(r.tree[0].dynamic, false);
});

test('simple queue order is preserved, not sorted', () => {
  // Each packet walks the list until one matches, so position changes what a
  // queue does. Sorting these would misrepresent the router.
  const rows = [simpleRow({ '.id': '*1', name: 'zebra' }),
                simpleRow({ '.id': '*2', name: 'alpha' }),
                simpleRow({ '.id': '*3', name: 'middle' })];
  const r = Q.buildQueueRows(rows, [], new Map(), 1000);
  assert.deepStrictEqual(r.simple.map(x => x.name), ['zebra', 'alpha', 'middle']);
  assert.deepStrictEqual(r.simple.map(x => x.order), [0, 1, 2]);
});

test('an empty menu does not become a row', () => {
  const r = Q.buildQueueRows([{ undefined: '' }], [{ undefined: '' }], new Map(), 1000);
  assert.deepStrictEqual(r.simple, []);
  assert.deepStrictEqual(r.tree, []);
});

test('a dynamic simple queue is marked', () => {
  const r = Q.buildQueueRows([simpleRow({ dynamic: 'true', name: 'kid-control' })], [], new Map(), 1000);
  assert.strictEqual(r.simple[0].dynamic, true);
});

// ── FastTrack ────────────────────────────────────────────────────────────────

test('an enabled forward fasttrack rule is detected', () => {
  const ft = Q.activeFasttrack([{ action: 'fasttrack-connection', chain: 'forward', disabled: false }]);
  assert.strictEqual(ft.state, 'active');
  assert.strictEqual(ft.count, 1);
  assert.strictEqual(ft.scoped, false);
});

test('a disabled fasttrack rule bypasses nothing and is not reported', () => {
  const ft = Q.activeFasttrack([{ action: 'fasttrack-connection', chain: 'forward', disabled: 'true' }]);
  assert.strictEqual(ft.state, 'clear');
});

test('a rule narrowed by address or interface is reported as scoped', () => {
  // Scoped means "some traffic still reaches the queues", which is a different
  // sentence from "none does".
  for (const narrowing of [{ srcAddress: '10.0.0.0/24' }, { inInterface: 'bridge' },
                           { 'dst-address': '10.0.0.0/24' }]) {
    const ft = Q.activeFasttrack([Object.assign(
      { action: 'fasttrack-connection', chain: 'forward', disabled: false }, narrowing)]);
    assert.strictEqual(ft.scoped, true, JSON.stringify(narrowing) + ' is scoped');
  }
});

test('a fasttrack rule in another chain is not a forward bypass', () => {
  assert.strictEqual(
    Q.activeFasttrack([{ action: 'fasttrack-connection', chain: 'input', disabled: false }]).state, 'clear');
  assert.strictEqual(Q.activeFasttrack([{ action: 'accept', chain: 'forward' }]).state, 'clear');
});

test('only a global-parented tree is bypassed by FastTrack', () => {
  // Confirmed twice in the MikroTik docs: FastTrack bypasses "simple queues,
  // queue tree with parent=global". An interface-parented tree still works, so a
  // blanket banner over the tree tab would be wrong.
  const r = Q.buildQueueRows([], [treeRow({ parent: 'global' }),
                                  treeRow({ '.id': '*2', name: 'wan', parent: 'WAN1' })],
                             new Map(), 1000);
  assert.strictEqual(r.tree[0].fasttrackBypassable, true);
  assert.strictEqual(r.tree[1].fasttrackBypassable, false);
});

test('the FastTrack summary carries no firewall rules', () => {
  // Borrowed from the firewall collector, but only a summary leaves: a reader
  // holding `queues` but not `firewall` learns that FastTrack is on, which is a
  // fact about this page's own correctness, not a firewall listing.
  const ft = Q.activeFasttrack([{ action: 'fasttrack-connection', chain: 'forward',
                                  disabled: false, srcAddress: '10.0.0.0/24', id: '*1F',
                                  comment: 'defconf: Fasttrack Rule' }]);
  assert.deepStrictEqual(Object.keys(ft).sort(), ['count', 'scoped', 'state']);
  assert.ok(!JSON.stringify(ft).includes('defconf'));
  assert.ok(!JSON.stringify(ft).includes('10.0.0.0/24'));
});

// ── CIDR arithmetic ──────────────────────────────────────────────────────────

test('containment is decided correctly where it can be', () => {
  assert.strictEqual(guard.cidrContains('10.0.0.0/24', '10.0.0.5'), true);
  assert.strictEqual(guard.cidrContains('10.0.1.0/24', '10.0.0.5'), false);
  assert.strictEqual(guard.cidrContains('0.0.0.0/0',   '10.0.0.5'), true, 'everything is inside /0');
  assert.strictEqual(guard.cidrContains('10.0.0.5/32', '10.0.0.5'), true, '/32 is exact');
  assert.strictEqual(guard.cidrContains('10.0.0.6/32', '10.0.0.5'), false);
  assert.strictEqual(guard.cidrContains('10.0.0.5',    '10.0.0.5'), true, 'a bare address means /32');
});

test('undecidable is null, and is not the same as false', () => {
  // Both end in "no warning", but they mean different things to a reader, and
  // keeping them apart is what lets a test prove which branch it took.
  assert.strictEqual(guard.cidrContains('WAN1', '10.0.0.5'), null, 'an interface name decides nothing');
  assert.strictEqual(guard.cidrContains('2001:db8::/32', '10.0.0.5'), null, 'v6 target, v4 address');
  assert.strictEqual(guard.cidrContains('10.0.0.0/33', '10.0.0.5'), null, 'impossible prefix');
  assert.strictEqual(guard.cidrContains('', '10.0.0.5'), null);
});

// ── The self-throttle warning ────────────────────────────────────────────────

const ACTIVE = [
  { name: 'MikroDash', address: '10.0.0.5', via: 'api' },
  { name: 'MikroDash', address: '10.0.0.5', via: 'api' },
  { name: 'SecOps7',   address: '10.0.0.40', via: 'winbox' },
];
const self = () => guard.resolveSelfAddresses(ACTIVE, ['MikroDash']);
const check = (values, before) =>
  guard.checkSimpleQueue({ selfAddresses: self(), values, before: before || null });

test('our own address comes from what the router sees, deduplicated', () => {
  const s = self();
  assert.deepStrictEqual(s.addresses, ['10.0.0.5'], 'three sessions, one address');
  assert.strictEqual(s.resolved, true);
});

test('a queue that would throttle us warns', () => {
  const v = check({ target: '10.0.0.0/24', maxLimit: '64000/64000' });
  assert.strictEqual(v.level, 'warn');
  assert.strictEqual(v.code, 'self-throttle');
  assert.strictEqual(v.detail.address, '10.0.0.5');
  assert.ok(v.fingerprint);
});

test('an ordinary LAN queue does NOT warn', () => {
  // The control that matters most. Shaping the LAN that contains the dashboard
  // is the single most common queue anyone writes; warning about it would train
  // the operator to click through the warning that mattered.
  assert.strictEqual(check({ target: '10.0.0.0/24', maxLimit: '50000000/50000000' }).level, 'none');
});

test('the warning stays silent where it cannot help', () => {
  for (const [label, values] of Object.entries({
    'another subnet':      { target: '192.0.2.0/24', maxLimit: '64000/64000' },
    'an interface target': { target: 'WAN1',         maxLimit: '64000/64000' },
    'explicitly unlimited':{ target: '0.0.0.0/0',    maxLimit: '0/0' },
    'no limit at all':     { target: '0.0.0.0/0',    maxLimit: '' },
    'created disabled':    { target: '10.0.0.0/24',  maxLimit: '64000/64000', disabled: true },
  })) {
    assert.strictEqual(check(values).level, 'none', label + ' must not warn');
  }
});

test('it fails OPEN when we cannot identify ourselves', () => {
  // The deliberate inversion of selfGuard. /user/active being denied to the API
  // user is the common case, so failing closed would block queue creation on
  // exactly those routers to prevent a slow dashboard.
  const none = guard.resolveSelfAddresses(ACTIVE, ['somebody-else']);
  assert.strictEqual(none.resolved, false);
  const v = guard.checkSimpleQueue({ selfAddresses: none,
    values: { target: '0.0.0.0/0', maxLimit: '1000/1000' } });
  assert.strictEqual(v.level, 'none');
});

test('an edit only warns when it makes things worse', () => {
  const before = { target: '10.0.0.0/24', maxLimit: '64000/64000', disabled: false };
  // Already throttling us, and nothing about that changed.
  assert.strictEqual(check({ target: '10.0.0.0/24', maxLimit: '64000/64000' }, before).level, 'none',
    'a comment-only edit must not prompt every time');
  assert.strictEqual(check({ target: '10.0.0.0/24', maxLimit: '32000/32000' }, before).level, 'warn',
    'a lower cap is worse');
  assert.strictEqual(check({ target: '10.0.0.0/24', maxLimit: '64000/64000' },
    Object.assign({}, before, { disabled: true })).level, 'warn', 'enabling it is the moment it bites');
  assert.strictEqual(check({ target: '10.0.0.0/24', maxLimit: '64000/64000' },
    { target: '192.0.2.0/24', maxLimit: '64000/64000', disabled: false }).level, 'warn',
    'newly covering us is worse');
});

test('a fingerprint binds to the values it was issued for', () => {
  // This is what stops an acknowledgement being carried from a mild queue to a
  // harsher one, or replayed against a different write.
  const mild   = check({ target: '10.0.0.0/24', maxLimit: '900000/900000' });
  const harsh  = check({ target: '10.0.0.0/24', maxLimit: '8000/8000' });
  const repeat = check({ target: '10.0.0.0/24', maxLimit: '900000/900000' });
  assert.notStrictEqual(mild.fingerprint, harsh.fingerprint);
  assert.strictEqual(mild.fingerprint, repeat.fingerprint, 'stable for identical inputs');
});

test('a mismatched acknowledgement is stale-warning, not the first prompt again', () => {
  // Both are refusals to write, but they are different events: one is "you have
  // not been asked yet", the other is "what you agreed to has changed".
  const src = stripComments(SRC('index.js'));
  const start = src.indexOf("socket.on('queue:save'");
  const body  = src.slice(start, src.indexOf("socket.on('", start + 20));
  assert.ok(body.includes("_qErr('self-throttle'"), 'prompts when unacknowledged');
  assert.ok(body.includes("_qErr('stale-warning'"), 'distinguishes a stale acknowledgement');
  assert.ok(body.indexOf("_qErr('self-throttle'") < body.indexOf("_qErr('stale-warning'"),
    'the no-ack case is checked before the mismatch case');
});

// ── The collector reads, and only reads ──────────────────────────────────────

test('the queues collector issues no write commands, ever', () => {
  const code = stripComments(SRC('collectors', 'queues.js'));
  for (const verb of ['/queue/simple/add', '/queue/simple/set', '/queue/simple/remove',
                      '/queue/simple/move', '/queue/simple/reset-counters',
                      '/queue/tree/add', '/queue/tree/set', '/queue/tree/remove',
                      '/queue/simple/enable', '/queue/simple/disable']) {
    assert.ok(!code.includes("'" + verb + "'"), 'queues.js must not issue ' + verb);
  }
});

// ── The handlers ─────────────────────────────────────────────────────────────

const WRITE_EVENTS = ['queue:save', 'queue:remove', 'queue:toggle',
                      'queue:resetCounters', 'queue:move'];

function handlerBody(src, ev) {
  const start = src.indexOf("socket.on('" + ev + "'");
  assert.ok(start > 0, ev + ' handler exists');
  const next = src.indexOf("socket.on('", start + 20);
  return src.slice(start, next > 0 ? next : start + 4000);
}

test('every queue action is gated on router:write and the page toggle', () => {
  const src = SRC('index.js');
  for (const ev of WRITE_EVENTS) {
    assert.ok(handlerBody(src, ev).includes('_qMayWrite(rid)'), ev + ' checks both gates');
  }
  assert.ok(/_qMayWrite = \(rid\) =>\s*\n?\s*_pageAllowed\(socket, 'queues', 'write'\) && _socketCan\(socket, 'router:write', rid\)/
    .test(src), '_qMayWrite is the page gate AND router:write');
});

test('every refusal and every success is recorded', () => {
  const src = SRC('index.js');
  for (const ev of WRITE_EVENTS) {
    const body = handlerBody(src, ev);
    assert.ok(body.includes('audit.fromSocket(socket).denied'), ev + ' audits its refusals');
    assert.ok(body.includes('audit.fromSocket(socket).record'), ev + ' audits its successes');
  }
});

test('the check runs against a fresh read, and before the write', () => {
  const src = stripComments(SRC('index.js'));
  for (const ev of WRITE_EVENTS) {
    const body  = handlerBody(src, ev);
    const read  = body.indexOf('_qRead(session, rid');
    const write = body.search(/session\.ros\.write\(_QUEUE_MENUS|session\.ros\.write\('\/queue/);
    assert.ok(read  > 0, ev + ' re-reads from the router');
    assert.ok(write > 0, ev + ' writes to the router');
    assert.ok(read < write, ev + ': the read precedes the write');
  }
});

test('no queue action resolves its target from the collector payload', () => {
  // The deliberate inversion of the Packages pattern, for the same reason as
  // Router Users: here the payload is what goes stale in the dangerous
  // direction.
  const src = stripComments(SRC('index.js'));
  for (const ev of WRITE_EVENTS) {
    for (const m of handlerBody(src, ev).match(/lastPayload[^\n]*/g) || []) {
      assert.fail(ev + ' must not read lastPayload: ' + m.trim());
    }
  }
});

test('a row is addressed by id but identified by name', () => {
  const src = SRC('index.js');
  assert.ok(/_qRow = \(rows, id, expectedName\)/.test(src));
  for (const ev of WRITE_EVENTS) {
    assert.ok(handlerBody(src, ev).includes('stale-row'),
      ev + ' refuses a row that changed underneath the page');
  }
});

test('dynamic rows are refused by every verb that mutates one', () => {
  // RouterOS refuses these itself, so there is no operator judgement to defer
  // to — refusing locally turns an opaque router error into a sentence.
  const src = SRC('index.js');
  for (const ev of ['queue:save', 'queue:remove', 'queue:toggle']) {
    const body = handlerBody(src, ev);
    assert.ok(body.includes("_qErr('dynamic-row'"), ev + ' refuses a dynamic row');
    assert.ok(body.includes('target.dynamic'), ev + ' reads dynamic off the freshly-read row');
  }
});

test('writes are serialised per router and target the captured router', () => {
  const src = SRC('index.js');
  for (const ev of WRITE_EVENTS) {
    assert.ok(new RegExp("socket\\.on\\('" + ev + "', \\(req\\) => _routerWriteQueue\\(socket\\.routerId, async \\(rid\\) =>")
      .test(src), ev + ' is queued with the router id captured at enqueue');
  }
  // Router Users and Queues share one chain per router — the hazard is two
  // people on one device, not two features.
  assert.ok(src.includes('function _routerWriteQueue'), 'the mutex is named for router writes, not one feature');
  assert.ok(!src.includes('_ruQueue('), 'the old feature-specific name is gone');
});

test('the router stays the authority on limit validation', () => {
  // RouterOS refuses max-limit below limit-at. Checking locally turns that into
  // a sentence naming both fields; mapping the router's message keeps it correct
  // when the local check misses a case.
  const body = handlerBody(SRC('index.js'), 'queue:save');
  assert.ok(body.includes("_qErr('limit-above-max')"), 'checked before the write');
  assert.ok(body.includes("msg.includes('less than')"), 'the router own refusal is mapped too');
});

// ── Registry ─────────────────────────────────────────────────────────────────

test('the page and collector are registered and page-scoped', () => {
  const Pages = require('../src/pages');
  const { COLLECTORS, BY_KEY } = require('../src/collection');
  const page = Pages.BY_KEY.queues;
  assert.ok(page, 'queues is a registered page');
  assert.strictEqual(page.settingsKey, 'pageQueues');
  assert.deepStrictEqual(page.streamRooms, ['page-queues'], 'suspends when nobody is on the page');
  const col = BY_KEY.queues;
  assert.strictEqual(col.page, 'queues');
  assert.strictEqual(col.streamKey, 'streamQueues', 'ships both delivery paths');
  assert.strictEqual(col.pollable, true);
  assert.strictEqual(col.disableable, true);
  assert.ok(COLLECTORS.some(c => c.key === 'queues'));
});

test('queues does not hard-require firewall', () => {
  // It borrows the FastTrack summary by reference. A hard require would cascade
  // into a disable, so switching Firewall collection off would blank the whole
  // Queues page instead of degrading one banner.
  const { BY_KEY } = require('../src/collection');
  assert.deepStrictEqual(BY_KEY.queues.requires, []);
  const src = SRC('collectors', 'queues.js');
  assert.ok(src.includes("state: 'unknown'"), 'an unavailable firewall collector degrades the banner');
  assert.ok(/!fw \|\| fw\.disabled \|\| !fw\.lastPayload/.test(src),
    'one guard covers the null stub, the not-started case and the failed-start case');
});

test('a new page joins Advanced by existing, and not the lower tiers', () => {
  const Pages = require('../src/pages');
  assert.ok(Pages.VIEW_PRESETS.advanced.includes('queues'));
  assert.ok(!Pages.VIEW_PRESETS.home.includes('queues'));
  assert.ok(!Pages.VIEW_PRESETS.standard.includes('queues'));
});

test('the poll interval is settable and bounded', () => {
  const Settings = require('../src/settings');
  assert.strictEqual(typeof Settings.DEFAULTS.pollQueues, 'number');
  assert.strictEqual(Settings.DEFAULTS.pageQueues, true);
  const src = SRC('index.js');
  assert.ok(src.includes('pollQueues:[2000,60000]'), 'bounded in intFields');
  assert.ok(src.includes("pollQueues:'queues'"), 'reaches the live collector on save');
});
