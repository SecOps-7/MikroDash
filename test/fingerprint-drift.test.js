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
