'use strict';
/**
 * What the DNS collector considers "a change worth pushing".
 *
 * The fingerprint used to be a hand-listed tuple —
 *
 *     [name, regexp, address, type, disabled]
 *
 * — which omitted `comment` and `ttl`, both of which the page renders. So a
 * comment-only edit wrote the router, refreshNow() re-read it, and the
 * fingerprint came back identical: the open page kept the old value until
 * something unrelated moved. It hid for a long time because the SETTINGS row is
 * fingerprinted too and carries `cache-used`, which moves constantly on a busy
 * router; on an idle device the update never arrived at all.
 *
 * The port found it, the live app now fingerprints the whole entry, and these
 * tests pin the fixed behaviour. The Go side asserts the same thing against the
 * Go collector (TestDNSFingerprintCoversComment), so the two cannot drift.
 */

const { test } = require('node:test');
const assert   = require('node:assert');
const path     = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const DnsCollector = require(path.join(LIVE, 'src', 'collectors', 'dns.js'));

/** A router whose one static row is whatever `row` currently holds. */
function harness(row) {
  const emitted = [];
  const chain = { to: () => chain, emit: (ev) => emitted.push(ev) };
  const ros = {
    connected: true, routerLabel: 'fingerprint',
    async write(cmd) {
      // cache-used is held STILL on purpose: it is in the settings fingerprint,
      // and letting it move would mask exactly what this test measures.
      if (cmd === '/ip/dns/print') return [{ servers: '', 'cache-size': '2048', 'cache-used': '46' }];
      if (cmd === '/ip/dns/static/print') return [{ ...row.value }];
      return [];
    },
    on() {}, stream() { return { stop() {}, on() {} }; },
  };
  const io = { emit: (ev) => emitted.push(ev), to: () => chain,
               engine: { clientsCount: 1 }, sockets: { adapter: { rooms: new Map() } } };
  return { emitted, collector: new DnsCollector({ ros, io, state: {}, pollMs: 10000 }) };
}

const BASE = { '.id': '*1', name: 'host.lan', address: '198.51.100.7',
               type: 'A', ttl: '1d', disabled: 'false', comment: 'before' };

test('a comment-only edit pushes an update', async () => {
  const row = { value: { ...BASE } };
  const { emitted, collector } = harness(row);

  await collector._tick();
  assert.strictEqual(emitted.length, 1, 'the first tick should emit');

  row.value = { ...BASE, comment: 'after' };
  await collector.refreshNow();              // exactly what a res:save does

  assert.strictEqual(emitted.length, 2,
    'a comment-only change did not emit — `comment` is a rendered column, so it ' +
    'must be inside the fingerprint');
  assert.strictEqual(collector.lastPayload.staticEntries[0].comment, 'after',
    'the payload carries what the router now holds');
});

test('a change the fingerprint covers DOES push an update', async () => {
  const row = { value: { ...BASE } };
  const { emitted, collector } = harness(row);

  await collector._tick();
  row.value = { ...BASE, address: '198.51.100.9' };
  await collector.refreshNow();

  assert.strictEqual(emitted.length, 2, 'an address change must emit');
});
