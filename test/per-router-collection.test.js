'use strict';
// Per-router collection settings (#105) — the resolver.
//
// These pin the precedence rules, the part a future change is most likely to get
// subtly wrong: delivery (stream vs poll) is per-router with no global input,
// intervals still inherit from global, and the two must not leak into each other.

const test   = require('node:test');
const assert = require('node:assert');

const {
  COLLECTORS, DISABLEABLE, MODES, DEFAULT_MODE,
  resolveCollection, collectionFingerprint,
} = require('../src/collection');

// A realistic subset of Settings.load() output.
const GLOBAL = {
  pollSystem: 2000, pollConns: 5000, pollTalkers: 3000, pollIfstatus: 5000,
  pollPing: 5000, pollWireless: 30000, pollVpn: 10000, pollFirewall: 5000,
  pollRouting: 10000, pollArp: 30000, pollDhcp: 600000, pollBandwidth: 5000,
  pingEnabled: true, topN: 5, topTalkersN: 5, maxConns: 20000, historyMinutes: 30,
};

// ── Registry sanity ──────────────────────────────────────────────────────────

test('registry covers every collector the session builds, exactly once', () => {
  const keys = COLLECTORS.map(c => c.key);
  assert.equal(new Set(keys).size, keys.length, 'no duplicate keys');
  // Mirrors the session object returned by buildSession() in src/index.js.
  const sessionProps = ['dhcpLeases','dhcpNetworks','arp','traffic','conns','talkers','logs',
                        'system','wireless','vpn','firewall','ifStatus','ping','bandwidth',
                        'routing','netwatch'];
  assert.deepEqual(COLLECTORS.map(c => c.sessionProp).sort(), [...sessionProps].sort());
});

test('protected collectors are the ones other collectors read unguarded', () => {
  const protectedKeys = COLLECTORS.filter(c => !c.disableable).map(c => c.key).sort();
  // arp/dhcpLeases/dhcpNetworks are read without a null guard by connections.js;
  // traffic feeds stored history; system feeds identity, the update check and CPU alerts.
  assert.deepEqual(protectedKeys, ['arp','dhcpLeases','dhcpNetworks','system','traffic']);
  assert.equal(DISABLEABLE.length, 11);
});

// ── Defaults and inheritance ─────────────────────────────────────────────────

test('a router with no collection block inherits everything and streams', () => {
  const r = resolveCollection(GLOBAL, { id: 'r1' });
  assert.equal(r.mode, DEFAULT_MODE);
  assert.equal(r.mode, 'stream');
  assert.equal(r.poll.system, 2000, 'interval comes from the global setting');
  assert.equal(r.poll.conns, 5000);
  assert.equal(r.stream.system, true);
  assert.ok(Object.values(r.enabled).every(Boolean), 'nothing disabled by default');
});

test('a missing router record or missing settings does not throw', () => {
  assert.doesNotThrow(() => resolveCollection(GLOBAL, null));
  assert.doesNotThrow(() => resolveCollection(null, { id: 'r1' }));
  const r = resolveCollection(null, null);
  assert.equal(r.mode, 'stream');
  assert.equal(r.poll.system, 2000, 'falls back to the registry default');
});

// ── Delivery: per-router, no global input ────────────────────────────────────

test('mode poll switches every pollable collector to polling', () => {
  const r = resolveCollection(GLOBAL, { collection: { mode: 'poll' } });
  for (const c of COLLECTORS) {
    if (!c.pollable || !c.streamKey) continue;
    assert.equal(r.stream[c.key], false, `${c.key} should poll`);
  }
});

test('collectors with no poll path keep streaming even in poll mode', () => {
  const r = resolveCollection(GLOBAL, { collection: { mode: 'poll' } });
  // Polling /log/print would drop lines between polls; 1s polling of
  // monitor-traffic is worse than a single stream.
  assert.equal(r.stream.logs, true);
  assert.equal(r.stream.traffic, true);
});

test('a per-collector override beats the master mode', () => {
  const r = resolveCollection(GLOBAL, {
    collection: { mode: 'poll', overrides: { streamPing: true } },
  });
  assert.equal(r.stream.ping, true, 'explicit override wins');
  assert.equal(r.stream.system, false, 'others still follow the mode');
});

test('delivery takes no input from global settings', () => {
  // Even with a stale global streamSystem:false, a router in stream mode streams.
  const r = resolveCollection({ ...GLOBAL, streamSystem: false }, { collection: { mode: 'stream' } });
  assert.equal(r.stream.system, true);
});

test('mode never changes intervals', () => {
  const streamed = resolveCollection(GLOBAL, { collection: { mode: 'stream' } });
  const polled   = resolveCollection(GLOBAL, { collection: { mode: 'poll' } });
  assert.deepEqual(polled.poll, streamed.poll,
    'choosing Poll must not secretly also mean slower');
});

// ── Intervals ────────────────────────────────────────────────────────────────

test('an interval override replaces the global and is clamped', () => {
  const r = resolveCollection(GLOBAL, { collection: { overrides: { pollSystem: 15000 } } });
  assert.equal(r.poll.system, 15000);
  assert.equal(r.poll.conns, 5000, 'untouched keys still inherit');

  const tooBig = resolveCollection(GLOBAL, { collection: { overrides: { pollSystem: 999999 } } });
  assert.equal(tooBig.poll.system, 60000, 'clamped to the settings.js upper bound');
  const tooSmall = resolveCollection(GLOBAL, { collection: { overrides: { pollSystem: 1 } } });
  assert.equal(tooSmall.poll.system, 1000, 'clamped to the lower bound');
});

test('a non-numeric interval override falls back rather than producing NaN', () => {
  const r = resolveCollection(GLOBAL, { collection: { overrides: { pollSystem: 'soon' } } });
  assert.ok(Number.isFinite(r.poll.system));
  assert.equal(r.poll.system, 2000);
});

// ── Enable / disable and the cascade ─────────────────────────────────────────

test('disabling connections cascades to bandwidth', () => {
  // bandwidth.js has no fetch of its own: it reads connTableCache, which only
  // the connections collector fills.
  const r = resolveCollection(GLOBAL, { collection: { off: ['conns'] } });
  assert.equal(r.enabled.conns, false);
  assert.equal(r.enabled.bandwidth, false, 'cascade, not a silently empty card');
});

test('disabling bandwidth alone leaves connections running', () => {
  const r = resolveCollection(GLOBAL, { collection: { off: ['bandwidth'] } });
  assert.equal(r.enabled.bandwidth, false);
  assert.equal(r.enabled.conns, true);
});

test('a protected collector cannot be disabled through off', () => {
  const r = resolveCollection(GLOBAL, { collection: { off: ['arp', 'system', 'traffic'] } });
  assert.equal(r.enabled.arp, true);
  assert.equal(r.enabled.system, true);
  assert.equal(r.enabled.traffic, true);
});

test('the global pingEnabled kill switch still wins', () => {
  const r = resolveCollection({ ...GLOBAL, pingEnabled: false }, { collection: { off: [] } });
  assert.equal(r.enabled.ping, false);
});

test('an unknown key in off is ignored', () => {
  const r = resolveCollection(GLOBAL, { collection: { off: ['nonsense'] } });
  assert.ok(Object.values(r.enabled).every(Boolean));
});

// ── Fingerprint ──────────────────────────────────────────────────────────────

test('fingerprint is stable across key and array order', () => {
  const a = collectionFingerprint(GLOBAL, {
    collection: { mode: 'poll', off: ['conns', 'talkers'], overrides: { pollSystem: 3000 } },
  });
  const b = collectionFingerprint(GLOBAL, {
    collection: { overrides: { pollSystem: 3000 }, off: ['talkers', 'conns'], mode: 'poll' },
  });
  assert.equal(a, b, 'a cosmetic re-save must not force a reconnect');
});

test('fingerprint ignores a label-only edit but reacts to a real one', () => {
  const before = collectionFingerprint(GLOBAL, { label: 'old', collection: { mode: 'stream' } });
  const label  = collectionFingerprint(GLOBAL, { label: 'new', collection: { mode: 'stream' } });
  assert.equal(before, label, 'renaming a router must not rebuild its session');

  const mode = collectionFingerprint(GLOBAL, { label: 'old', collection: { mode: 'poll' } });
  assert.notEqual(before, mode);
  const off = collectionFingerprint(GLOBAL, { label: 'old', collection: { mode: 'stream', off: ['vpn'] } });
  assert.notEqual(before, off);
});

test('fingerprint reacts to defaultIf and pingTarget, which also shape the session', () => {
  const a = collectionFingerprint(GLOBAL, { defaultIf: 'ether1' });
  const b = collectionFingerprint(GLOBAL, { defaultIf: 'WAN1' });
  assert.notEqual(a, b);
  const c = collectionFingerprint(GLOBAL, { defaultIf: 'ether1', pingTarget: '8.8.8.8' });
  assert.notEqual(a, c);
});

test('fingerprint reacts to a global interval change for an inheriting router', () => {
  const a = collectionFingerprint(GLOBAL, { id: 'r1' });
  const b = collectionFingerprint({ ...GLOBAL, pollSystem: 9000 }, { id: 'r1' });
  assert.notEqual(a, b, 'an inheriting router is affected by a global change');
});

test('MODES is exactly the two-way switch the UI offers', () => {
  assert.deepEqual([...MODES], ['stream', 'poll']);
});

// ── Storage: routers.js normalisation ────────────────────────────────────────
// update() rebuilds a record field by field, so a field it does not enumerate is
// silently dropped on edit even though ...existing preserves it. That is the bug
// class that lost notifRouterUpdate, and `collection` is exactly such a field.

const fs   = require('fs');
const os   = require('os');
const path = require('path');

function makeTmpDir() { return fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-coll-')); }
function freshRouters(tmpDir) {
  process.env.DATA_DIR = tmpDir;
  delete require.cache[require.resolve('../src/routers')];
  delete require.cache[require.resolve('../src/settings')];
  delete require.cache[require.resolve('../src/collection')];
  return require('../src/routers');
}

test('a router with no collection block stores nothing extra', () => {
  const R = freshRouters(makeTmpDir());
  const added = R.add({ host: '192.168.88.1' });
  assert.equal(added.collection, undefined,
    'defaults must leave routers.json byte-identical to before this feature');
});

test('collection survives an unrelated edit', () => {
  const R = freshRouters(makeTmpDir());
  const a = R.add({ host: '192.168.88.1', collection: { mode: 'poll', off: ['conns'] } });
  assert.equal(a.collection.mode, 'poll');
  const edited = R.update(a.id, { label: 'renamed' });   // body omits collection
  assert.equal(edited.label, 'renamed');
  assert.deepEqual(edited.collection, { mode: 'poll', off: ['conns'] },
    'omitting the field on edit must not wipe it');
});

test('explicit null resets collection to inherit', () => {
  const R = freshRouters(makeTmpDir());
  const a = R.add({ host: '192.168.88.1', collection: { mode: 'poll' } });
  const cleared = R.update(a.id, { collection: null });
  assert.equal(cleared.collection, undefined);
});

test('normalisation drops junk and clamps intervals', () => {
  const R = freshRouters(makeTmpDir());
  const a = R.add({ host: '192.168.88.1', collection: {
    mode: 'sideways',                                  // not a valid mode
    off: ['conns', 'conns', 'arp', 'nonsense'],        // dupe, protected, unknown
    overrides: { pollSystem: 999999, streamPing: 'true', bogusKey: 1 },
  }});
  assert.equal(a.collection.mode, undefined, 'invalid mode dropped');
  assert.deepEqual(a.collection.off, ['conns'], 'deduped, protected and unknown removed');
  assert.equal(a.collection.overrides.pollSystem, 60000, 'clamped to the shared bounds');
  assert.equal(a.collection.overrides.streamPing, true, 'string "true" coerced');
  assert.equal('bogusKey' in a.collection.overrides, false);
});

test('a block carrying no information is stored as absent', () => {
  const R = freshRouters(makeTmpDir());
  const a = R.add({ host: '192.168.88.1', collection: { mode: 'stream', off: [], overrides: {} } });
  assert.equal(a.collection, undefined, 'stream is the default, so this says nothing');
});

test('collection round-trips through disk', () => {
  const tmp = makeTmpDir();
  const R = freshRouters(tmp);
  const a = R.add({ host: '192.168.88.1', collection: { mode: 'poll', off: ['talkers'] } });
  const R2 = freshRouters(tmp);                       // re-read from disk
  assert.deepEqual(R2.getById(a.id).collection, { mode: 'poll', off: ['talkers'] });
});

test('getPublic exposes collection but still masks the password', () => {
  const R = freshRouters(makeTmpDir());
  const a = R.add({ host: '192.168.88.1', password: 'sup3r-secret',
                    collection: { mode: 'poll' } });
  const pub = R.getPublic().find(r => r.id === a.id);
  assert.equal(pub.collection.mode, 'poll');
  assert.equal(pub.password, '••••••••');
});

// ── Client card map must mirror the registry ─────────────────────────────────

test('every disableable collector with cards is mapped in the client', () => {
  const fs   = require('fs');
  const p    = require('path').join(__dirname, '..', 'public', 'app.js');
  const app  = fs.readFileSync(p, 'utf8');
  const block = app.slice(app.indexOf('var COLLECTOR_CARDS = {'));
  const mapText = block.slice(0, block.indexOf('};') + 2);

  const missing = COLLECTORS
    .filter(c => c.disableable && c.cards.length)
    .filter(c => !new RegExp('\\b' + c.key + '\\s*:').test(mapText))
    .map(c => c.key);
  assert.deepEqual(missing, [],
    'these disableable collectors have dashboard cards but no COLLECTOR_CARDS entry, '
    + 'so their cards would show a false "stale" scrim when switched off:\n  ' + missing.join(', '));

  // And every card id referenced must exist in the markup.
  const html = fs.readFileSync(require('path').join(__dirname, '..', 'public', 'index.html'), 'utf8');
  for (const c of COLLECTORS) {
    for (const card of c.cards) {
      assert.ok(html.includes('id="' + card + '"'), `card ${card} (${c.key}) not found in index.html`);
    }
  }
});

test('null collector methods are chainable, not bare undefined', async () => {
  // index.js:2888 does `c.tick(true).catch(...)` and elsewhere awaits start().
  // Returning undefined throws a TypeError that silently aborts sendInitialState,
  // so the browser never learns which collectors are disabled.
  const { makeNullCollector } = require('../src/collectors/nullCollector');
  const c = makeNullCollector('conns');
  for (const m of ['tick', 'start', 'stop', 'suspend', 'resume']) {
    const r = c[m](true);
    assert.ok(r && typeof r.then === 'function', `${m}() must return a promise`);
    assert.ok(typeof r.catch === 'function', `${m}() result must be catchable`);
    await r;
  }
  await assert.doesNotReject(() => c.tick(true).catch(() => {}));
});
