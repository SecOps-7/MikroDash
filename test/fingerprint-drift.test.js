'use strict';
/**
 * Change fingerprints must cover what the page renders.
 *
 * Collectors suppress a redundant emit by fingerprinting the payload, and
 * several built that fingerprint from a hand-listed tuple. Every field left off
 * the list is then invisible to the dirty check: the collector re-reads the
 * router, sees a value it does not hash, and returns without emitting — so an
 * edit that really did land on the device never reaches the open page.
 *
 * It hid for a long time because most of these collectors also hash something
 * that moves on its own (a cache counter, a byte counter). On a busy router the
 * fingerprint changes for an unrelated reason a tick or two later and the table
 * catches up, looking merely slow. On an idle device the update never arrives.
 *
 * The rule these pin is: every field the page displays belongs in the
 * fingerprint.
 */

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');

const { track } = require('./helpers/collector-cleanup');
const DnsCollector = track(require('../src/collectors/dns'));
const InterfaceStatusCollector = track(require('../src/collectors/interfaceStatus'));

/** Answers keyed by command; the object is live, so a test can change a reply. */
function fakeRos(answers) {
  return {
    connected: true,
    routerLabel: 'test',
    on() {},
    async write(cmd) { return answers[cmd] || []; },
  };
}

/** Counts only the named event, so a collector that emits two does not confuse it. */
function countingIo(event) {
  const io = {
    n: 0,
    engine: { clientsCount: 1 },
    to() { return io; },
    emit(ev) { if (ev === event) io.n++; },
  };
  return io;
}

// ── DNS: the reported case ───────────────────────────────────────────────────

test('a comment-only DNS edit reaches an open page on an idle router', async () => {
  // cache-used is held still throughout. It is the field that moves on a busy
  // router and accidentally carried these updates through.
  const entry = { '.id': '*1', name: 'nas.lan', address: '10.0.0.5',
                  type: 'A', ttl: '1d', comment: 'before' };
  const answers = {
    '/ip/dns/print': [{ servers: '1.1.1.1', 'cache-used': '100' }],
    '/ip/dns/static/print': [entry],
  };
  const io = countingIo('dns:update');
  const c = new DnsCollector({ ros: fakeRos(answers), io, state: {}, pollMs: 5000 });

  await c._tick();
  assert.equal(io.n, 1, 'the first read is always an update');

  answers['/ip/dns/static/print'] = [Object.assign({}, entry, { comment: 'after' })];
  await c.refreshNow();
  assert.equal(io.n, 2, 'a comment-only change must be pushed');
  assert.equal(c.lastPayload.staticEntries[0].comment, 'after');

  c.stop();
});

test('a TTL-only DNS edit reaches an open page too', async () => {
  // TTL was missing from the tuple for the same reason comment was, and the
  // table renders and sorts by it.
  const entry = { '.id': '*1', name: 'nas.lan', address: '10.0.0.5',
                  type: 'A', ttl: '1d', comment: '' };
  const answers = {
    '/ip/dns/print': [{ servers: '1.1.1.1', 'cache-used': '100' }],
    '/ip/dns/static/print': [entry],
  };
  const io = countingIo('dns:update');
  const c = new DnsCollector({ ros: fakeRos(answers), io, state: {}, pollMs: 5000 });

  await c._tick();
  answers['/ip/dns/static/print'] = [Object.assign({}, entry, { ttl: '5m' })];
  await c.refreshNow();
  assert.equal(io.n, 2, 'a ttl-only change must be pushed');

  c.stop();
});

test('an unchanged DNS table still emits nothing', async () => {
  // The point of the fingerprint is not lost by widening it: identical reads
  // must stay silent, or the dirty check has simply been switched off.
  const answers = {
    '/ip/dns/print': [{ servers: '1.1.1.1', 'cache-used': '100' }],
    '/ip/dns/static/print': [{ '.id': '*1', name: 'nas.lan', address: '10.0.0.5', type: 'A' }],
  };
  const io = countingIo('dns:update');
  const c = new DnsCollector({ ros: fakeRos(answers), io, state: {}, pollMs: 5000 });

  await c._tick();
  await c.refreshNow();
  assert.equal(io.n, 1, 'a re-read that changed nothing must not emit');

  c.stop();
});

// ── Interfaces ───────────────────────────────────────────────────────────────

test('an interface comment change reaches an open page', () => {
  // The list renders the comment in the name tooltip and beside the type, but
  // the fingerprint held only name/running/disabled/rates/errors.
  const io = countingIo('ifstatus:update');
  const c = new InterfaceStatusCollector({
    ros: { connected: true, routerLabel: 'test', on() {} },
    io, state: {}, pollMs: 5000, streamMode: false, rid: 'r1',
  });

  const row = { name: 'ether1', type: 'ether', running: 'true',
                disabled: 'false', comment: 'before', 'mac-address': 'AA:BB:CC:00:11:22' };
  c._ifaces = new Map([['ether1', row]]);
  c._buildAndEmit();
  assert.equal(io.n, 1);

  c._ifaces = new Map([['ether1', Object.assign({}, row, { comment: 'after' })]]);
  c._buildAndEmit();
  assert.equal(io.n, 2, 'a comment-only change must be pushed');

  c.stop();
});

test('an unchanged interface list stays silent inside the heartbeat window', () => {
  const io = countingIo('ifstatus:update');
  const c = new InterfaceStatusCollector({
    ros: { connected: true, routerLabel: 'test', on() {} },
    io, state: {}, pollMs: 5000, streamMode: false, rid: 'r1',
  });

  c._ifaces = new Map([['ether1', { name: 'ether1', type: 'ether', running: 'true',
                                    disabled: 'false', comment: '', 'mac-address': '' }]]);
  c._buildAndEmit();
  c._buildAndEmit();
  assert.equal(io.n, 1, 'identical rows must not re-emit before the 60 s heartbeat');

  c.stop();
});

// ── The rest of the sweep ────────────────────────────────────────────────────
//
// Firewall and queues reach their fingerprint through stream plumbing that is
// not worth reconstructing here, so these read the expression itself. Both
// pages render a comment per row.

/** The `const fp = JSON.stringify(...)` expression in a collector, as source. */
function fingerprintSource(file) {
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'collectors', file), 'utf8');
  const start = src.indexOf('const fp = JSON.stringify(');
  assert.notEqual(start, -1, file + ' no longer computes a fingerprint');
  const end = src.indexOf('if (fp', start);
  assert.notEqual(end, -1, file + ' computes a fingerprint it never compares');
  return src.slice(start, end);
}

test('the queues fingerprint covers the comment both tables render', () => {
  const fp = fingerprintSource('queues.js');
  const hits = (fp.match(/q\.comment/g) || []).length;
  assert.equal(hits, 2, 'both the simple and the tree tuple must carry q.comment');
  // The counters stay out on purpose — widening must not have swallowed them.
  assert.ok(!/q\.bytes/.test(fp),
    'byte counters belong outside the fingerprint; they move every tick');
});

test('the firewall fingerprint covers the whole rule, not just its counters', () => {
  const fp = fingerprintSource('firewall.js');
  assert.ok(!/packets: r\.packets/.test(fp),
    'fingerprinting id/packets/bytes alone means a rule edit only lands when '
    + 'traffic happens to move a counter in the same tick');
  for (const table of ['this._filter', 'this._nat', 'this._mangle', 'this._raw']) {
    assert.ok(fp.includes(table), table + ' is missing from the fingerprint');
  }
});

// ── the same rule, on the render side ───────────────────────────────────────
//
// Everything above pins a COLLECTOR fingerprint. This pins a render one, and it
// is the same failure read backwards: the collector emits correctly, and the
// page rebuilds regardless.
//
// system:update carries cpuLoad and uptime, so it arrives on every poll tick.
// The #rosUpdateRow block wrote innerHTML unconditionally, which destroyed
// #sysUpdateAction and the Update button inside it, and `.sbtn` carries a
// transition that a freshly inserted node restarts. The amber "7.x available"
// strip therefore flashed once per tick. Worse, the dispatch it made on the way
// past told the upgrade module to redraw the button AND emit packages:caps, so
// an idle dashboard did a socket round trip every tick to learn nothing.
test('the update row is rebuilt only when the update itself changes', () => {
  const vm  = require('node:vm');
  const APP = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');

  const m = APP.match(/if\(rosUpdateRow\)\{[\s\S]*?\n {2}\}\n\}/);
  assert.ok(m, 'the #rosUpdateRow render block moved or was renamed');
  const src = m[0].replace(/\n\}$/, '');   // drop _flushSysUpdate's closing brace

  let writes = 0, dispatches = 0, html = '', orderOk = true;
  const ctx = {
    _lastUpdateRowHtml: null,
    esc: (s) => String(s == null ? '' : s),
    CustomEvent: function (name, opts) { this.type = name; this.detail = opts && opts.detail; },
    document: {
      dispatchEvent(ev) {
        dispatches++;
        // The listener's draw() looks up #sysUpdateAction, so the markup that
        // creates that slot must already be in place. Firing first would have
        // it fill the node the next write is about to throw away.
        if (!html.includes(ev.detail.latest)) orderOk = false;
      },
    },
    rosUpdateRow: {
      set innerHTML(v) { writes++; html = v; },
      get innerHTML() { return html; },
    },
    d: null,
  };
  vm.createContext(ctx);
  const tick = (payload) => { ctx.d = payload; vm.runInContext(src, ctx); };

  const avail = { version: '7.23.4 (stable)', latestVersion: '7.24', updateAvailable: true, updateChannel: 'stable' };

  tick(avail);
  assert.equal(writes, 1, 'first payload must render the row');
  assert.equal(dispatches, 1, 'and announce it once');
  assert.ok(orderOk, 'the row must be written before the event is dispatched');

  // The actual bug: five more identical ticks, as a poll loop produces.
  for (let i = 0; i < 5; i++) tick(avail);
  assert.equal(writes, 1, 'an unchanged update must not rebuild the row');
  assert.equal(dispatches, 1, 'nor re-announce it, which redraws the button and refetches caps');

  // The inverse, so this cannot pass by never rendering at all.
  tick(Object.assign({}, avail, { latestVersion: '7.25' }));
  assert.equal(writes, 2, 'a genuinely new version must still render');
  assert.equal(dispatches, 2, 'and must still be announced');
  assert.ok(html.includes('7.25'), 'and the new version must be what is on screen');

  // Transitioning to up-to-date is also a change, and must not be swallowed.
  tick({ version: '7.25 (stable)', latestVersion: '7.25', updateAvailable: false });
  assert.equal(writes, 3, 'clearing the warning is a change too');
  assert.equal(dispatches, 2, 'but there is no update to announce any more');
});
