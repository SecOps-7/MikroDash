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
  planMigration,
  LEGACY_STREAM_KEYS,
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

// ── Shared poll loop ─────────────────────────────────────────────────────────

test('poll loop does not overlap runs when a poll is slow', async () => {
  // Recursive setTimeout, not setInterval: a router that is already struggling
  // must not accumulate queued requests. This is the whole point of poll mode.
  const { createPollLoop } = require('../src/collectors/util');
  let running = 0, maxConcurrent = 0, runs = 0;
  const loop = createPollLoop(async () => {
    running++; runs++; maxConcurrent = Math.max(maxConcurrent, running);
    await new Promise(r => setTimeout(r, 30));
    running--;
  }, () => 500);
  loop.start();
  await new Promise(r => setTimeout(r, 1300));
  loop.stop();
  assert.ok(runs >= 2, `expected repeats, got ${runs}`);
  assert.equal(maxConcurrent, 1, 'runs must never overlap');
});

test('poll loop stops cleanly and a rejecting run does not kill it', async () => {
  const { createPollLoop } = require('../src/collectors/util');
  let runs = 0;
  const loop = createPollLoop(async () => { runs++; throw new Error('boom'); }, () => 500);
  loop.start();
  await new Promise(r => setTimeout(r, 1300));
  const seen = runs;
  assert.ok(seen >= 2, 'a throwing run must not stop the loop');
  loop.stop();
  assert.equal(loop.pending, false, 'no timer left behind');
  await new Promise(r => setTimeout(r, 700));
  assert.equal(runs, seen, 'stopped means stopped');
});

test('poll loop reads its delay per tick so an interval change applies live', async () => {
  const { createPollLoop } = require('../src/collectors/util');
  let delay = 500, runs = 0;
  const loop = createPollLoop(async () => { runs++; }, () => delay);
  loop.start();
  await new Promise(r => setTimeout(r, 1200));
  const slow = runs;
  delay = 5000;                       // widen without restarting
  await new Promise(r => setTimeout(r, 1500));
  loop.stop();
  assert.ok(runs - slow <= 1, 'a longer interval must take effect without a restart');
});

// ─── Phase 7: retiring the app-global Collection Method ─────────────────────
//
// Stream-vs-poll used to be five app-global settings. They are now per-router,
// which means an install that had switched the fleet to Poll must not silently
// revert to Stream on upgrade — and the old control must be gone, not merely
// hidden, or two half-wired controls would disagree.

test('the global Collection Method control is fully removed, not just hidden', () => {
  const fs   = require('fs');
  const path = require('path');
  const read = (...p) => fs.readFileSync(path.join(__dirname, '..', ...p), 'utf8');

  const html = read('public', 'index.html');
  assert.ok(!html.includes('Collection Method'), 'the global Collection Method card still exists');
  assert.deepEqual(html.match(/s_stream[A-Za-z]+/g), null, 'stream* inputs remain in the settings page');

  const app = read('public', 'app.js');
  assert.deepEqual(app.match(/'stream(System|Ping|Conns|Talkers|Ifrates)'/g), null,
    'app.js still reads or writes the retired global stream* settings');

  // Settings must no longer offer them: a stale key in DEFAULTS would be echoed
  // back by getPublic() and re-saved forever, and one in the allowlist would let
  // a hand-crafted POST resurrect a setting nothing reads.
  const settings = read('src', 'settings.js');
  assert.deepEqual(settings.match(/^\s*stream\w+:/gm), null, 'stream* keys remain in Settings.DEFAULTS');

  const Settings = require('../src/settings');
  for (const k of ['streamSystem', 'streamPing', 'streamConns', 'streamTalkers', 'streamIfrates']) {
    assert.ok(!(k in Settings.DEFAULTS), `${k} is still a settings default`);
  }
});

test('resolveCollection ignores leftover global stream* keys', () => {
  // An upgraded /data/settings.json still physically contains the old keys.
  // They must have no effect, or a router would be stuck in whatever mode the
  // retired global control was last left in.
  const legacy = { ...GLOBAL, streamSystem: false, streamPing: false, streamConns: false,
                   streamTalkers: false, streamIfrates: false };
  const eff = resolveCollection(legacy, { id: 'r1' });
  assert.equal(eff.mode, 'stream', 'default mode must not be inferred from the legacy globals');
  for (const k of ['system', 'ping', 'conns', 'talkers', 'ifStatus']) {
    assert.equal(eff.stream[k], true, `${k} must stream by default despite the legacy global`);
  }
});

test('resolveCollection reports which intervals are pinned', () => {
  // The settings live-patch needs to distinguish inherited from pinned, so it can
  // leave a pinned router alone instead of dragging it to the new fleet default.
  const eff = resolveCollection(GLOBAL, { id: 'r1', collection: { overrides: { pollSystem: 15000 } } });
  assert.equal(eff.overrides.pollSystem, 15000);
  assert.equal(eff.overrides.pollConns, undefined, 'an inherited key must not appear as pinned');
  assert.equal(eff.poll.conns, GLOBAL.pollConns, 'inherited value still resolves from the globals');
});

test('the settings live-patch skips pinned intervals', () => {
  // Source guard: every `'pollX' in updates` branch in the POST /api/settings
  // handler must also test _pinned('pollX'). Missing one silently un-pins a
  // router the moment anyone saves global settings.
  const src = require('fs').readFileSync(
    require('path').join(__dirname, '..', 'src', 'index.js'), 'utf8');
  const unguarded = [];
  const re = /if \('(poll[A-Za-z]+)' in updates && (?!!_pinned)/g;
  let m;
  while ((m = re.exec(src))) unguarded.push(m[1]);
  assert.deepEqual(unguarded, [],
    'these interval branches would overwrite a per-router override:\n  ' + unguarded.join(', '));
});

test('overviewSessions no longer hardcodes a 1 s interval', () => {
  // Every router NOT served by the main pool got polled at 1 s whenever the
  // Routers page was open — harder than the active router itself.
  const src = require('fs').readFileSync(
    require('path').join(__dirname, '..', 'src', 'overviewSessions.js'), 'utf8');
  assert.ok(!/const\s+pollMs\s*=\s*1000/.test(src), 'the hardcoded 1 s interval is back');
  assert.ok(src.includes('resolveCollection'), 'overviewSessions must resolve per-router config');
  assert.ok(src.includes('makeNullCollector'),
    'a disabled collector must be stubbed, not constructed — its constructor opens streams');
});

test('planMigration maps the retired global mode onto routers', () => {
  const off = { streamSystem: false, streamPing: false, streamConns: false,
                streamTalkers: false, streamIfrates: false };
  const on  = { streamSystem: true,  streamPing: true,  streamConns: true,
                streamTalkers: true,  streamIfrates: true };

  // All-off was the whole point of the old control: it must survive the upgrade.
  assert.deepEqual(planMigration(off, [{ id: 'a' }, { id: 'b' }]), [
    { id: 'a', collection: { mode: 'poll' } },
    { id: 'b', collection: { mode: 'poll' } },
  ]);

  // All-on is already the default, so writing anything would be noise.
  assert.deepEqual(planMigration(on, [{ id: 'a' }]), []);

  // Mixed keeps streaming and records only the exceptions.
  assert.deepEqual(planMigration({ ...on, streamPing: false }, [{ id: 'a' }]),
    [{ id: 'a', collection: { mode: 'stream', overrides: { streamPing: false } } }]);

  // A router that already made its own choice is never overwritten.
  assert.deepEqual(planMigration(off, [{ id: 'a', collection: { mode: 'stream' } }]), []);

  // A fresh install has no legacy keys at all.
  assert.deepEqual(planMigration({}, [{ id: 'a' }]), []);
  assert.deepEqual(planMigration(null, null), []);
});

test('the migrated mode actually resolves through routers.js and resolveCollection', () => {
  // End to end: planMigration output must survive _normalizeCollection (which
  // rejects anything it does not recognise) and come back out as poll mode.
  // freshRouters() takes the tmp dir: calling it bare sets DATA_DIR to the string
  // "undefined" and scatters an ./undefined/ directory into the repo.
  const Routers = freshRouters(makeTmpDir());
  const r = Routers.add({ label: 'ac2', host: '10.0.0.1', username: 'u', password: 'p' });
  const [{ id, collection }] = planMigration(
    { streamSystem: false, streamPing: false, streamConns: false,
      streamTalkers: false, streamIfrates: false }, [r]);
  Routers.update(id, { collection });

  const eff = resolveCollection(GLOBAL, Routers.getById(id));
  assert.equal(eff.mode, 'poll');
  assert.equal(eff.stream.conns, false, 'poll mode must reach the collectors');
  assert.equal(eff.stream.traffic, true, 'traffic stays streamed even in poll mode');
});

test('the migration can still read the retired keys after they left DEFAULTS', () => {
  // The trap this catches: load() drops any stored key that is not in DEFAULTS.
  // Removing stream* from DEFAULTS therefore made them invisible to load(), which
  // would have turned the migration into a silent no-op for exactly the installs
  // it exists for — every upgraded Poll-mode user reverting to Stream on restart.
  const fs   = require('fs');
  const os   = require('os');
  const path = require('path');
  const dir  = fs.mkdtempSync(path.join(os.tmpdir(), 'md-retired-'));
  const prev = process.env.DATA_DIR;
  process.env.DATA_DIR = dir;
  for (const k of Object.keys(require.cache)) if (/src[\\/](settings|routers)\.js$/.test(k)) delete require.cache[k];

  try {
    // An upgraded install: the file on disk still carries the old global Poll choice.
    fs.writeFileSync(path.join(dir, 'settings.json'), JSON.stringify({
      streamSystem: false, streamPing: false, streamConns: false,
      streamTalkers: false, streamIfrates: false, pollSystem: 2000,
    }));
    const Settings = require('../src/settings');

    // load() cannot see them — that is correct behaviour, and the reason readRetired exists.
    assert.equal(Settings.load().streamSystem, undefined,
      'load() should filter unknown keys; if this fails the keys were never really retired');

    const legacy = Settings.readRetired(Object.keys(LEGACY_STREAM_KEYS));
    assert.deepEqual(legacy, {
      streamSystem: false, streamPing: false, streamConns: false,
      streamTalkers: false, streamIfrates: false,
    });
    assert.deepEqual(planMigration(legacy, [{ id: 'a' }]),
      [{ id: 'a', collection: { mode: 'poll' } }]);

    // A missing or corrupt file must not throw during startup.
    fs.writeFileSync(path.join(dir, 'settings.json'), 'not json');
    assert.deepEqual(Settings.readRetired(['streamPing']), {});

    // And index.js must actually take that path — reading the migration input
    // from load() would compile, pass every other test, and quietly do nothing.
    const idx = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');
    const mig = idx.slice(idx.indexOf('_migrateCollectionMode'),
                          idx.indexOf('_migrateCollectionMode') + 900);
    assert.ok(mig.includes('readRetired'),
      'the startup migration must read the retired keys off disk, not via load()');
    assert.ok(!/planMigration\(cfg\b/.test(mig),
      'planMigration must not be fed the filtered load() result');
  } finally {
    if (prev === undefined) delete process.env.DATA_DIR; else process.env.DATA_DIR = prev;
    for (const k of Object.keys(require.cache)) if (/src[\\/](settings|routers)\.js$/.test(k)) delete require.cache[k];
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// ─── Poll-mode delivery bugs found by running a real router in Poll (#105) ───
//
// All three predate this work (v0.5.38, commit a264480) and were invisible
// because nobody had actually run a router in poll mode end to end. They share a
// shape: the poll path is *started* correctly, so the startup log says "poll mode
// — polling ... every Nms", and then no data ever arrives.

const EventEmitter = require('events');

function pollRos(writeFn) {
  const ros = new EventEmitter();
  ros.setMaxListeners(30);
  ros.connected = true;
  ros.write  = writeFn || (async () => []);
  ros.stream = () => { const s = new EventEmitter(); s.stop = () => Promise.resolve(); return s; };
  return ros;
}

test('ifStatus poll asks for rates even though RouterOS sends disabled="false"', async () => {
  // The trap: RouterOS returns booleans as STRINGS, so `!iface.disabled` is
  // `!"false"` === false and every interface is filtered out. names came back
  // empty, _pollRatesOnce returned before its write, _streamRates stayed empty,
  // every rate rendered 0.00, the emit fingerprint never changed, and the
  // Interfaces page updated once per 60 s heartbeat instead of once per second.
  const InterfaceStatusCollector = require('../src/collectors/interfaceStatus');
  const calls = [];
  const ros = pollRos(async (cmd, params) => {
    calls.push(cmd);
    if (cmd === '/interface/monitor-traffic') {
      return [{ name: 'ether2', 'rx-bits-per-second': '2000000', 'tx-bits-per-second': '1000000' }];
    }
    return [];
  });
  const c = new InterfaceStatusCollector({
    ros, io: { engine: { clientsCount: 1 }, emit() {} },
    pollMs: 1000, metaPollMs: 60000, state: {}, streamMode: false,
  });
  // Exactly what the metadata stream deposits: string-valued flags.
  c._ifaces.set('ether2', { name: 'ether2', disabled: 'false', running: 'true' });
  c._ifaces.set('ether3', { name: 'ether3', disabled: 'true',  running: 'false' });

  await c._pollRatesOnce();

  assert.ok(calls.includes('/interface/monitor-traffic'),
    'the poll must actually issue monitor-traffic; an empty name list silently skips it');
  assert.equal(c._streamRates.size, 1, 'the enabled interface must get a rate entry');
  assert.deepEqual(c._streamRates.get('ether2'), { rxMbps: 2, txMbps: 1 });
  assert.ok(c._lastRatesSuccessTs > 0, 'a successful poll must be recorded');
  c.stop();
});

test('ifStatus poll still excludes genuinely disabled interfaces', () => {
  // The fix must not swing the other way and monitor disabled interfaces.
  const InterfaceStatusCollector = require('../src/collectors/interfaceStatus');
  const asked = [];
  const ros = pollRos(async (cmd, params) => {
    if (cmd === '/interface/monitor-traffic') asked.push(params.find(p => p.startsWith('=interface=')));
    return [];
  });
  const c = new InterfaceStatusCollector({
    ros, io: { engine: { clientsCount: 1 }, emit() {} },
    pollMs: 1000, metaPollMs: 60000, state: {}, streamMode: false,
  });
  c._ifaces.set('up',   { name: 'up',   disabled: 'false' });
  c._ifaces.set('down', { name: 'down', disabled: 'true'  });
  c._ifaces.set('bool', { name: 'bool', disabled: true    });   // some paths hand back real booleans
  return c._pollRatesOnce().then(() => {
    assert.deepEqual(asked, ['=interface=up']);
    c.stop();
  });
});

for (const [name, mod, sched] of [
  ['PingCollector',       '../src/collectors/ping',    '_pollTimer'],
  ['TopTalkersCollector', '../src/collectors/talkers', '_pollTimer'],
]) {
  test(`${name}.resume() restarts the poll loop, not just the stream`, async () => {
    // suspend() clears _pollTimer as well as the stream, but resume() only ever
    // restarted the stream — so the first time every viewer disconnected, a
    // poll-mode collector died permanently and its card sat stale forever.
    const Collector = require(mod);
    const ros = pollRos(async () => []);
    const c = new Collector({
      ros, io: { engine: { clientsCount: 1 }, emit() {}, on() {},
                 to() { return { emit() {}, to() { return { emit() {} }; } }; } },
      pollMs: 1000, state: {}, target: '1.1.1.1', topN: 5, streamMode: false,
    });
    c.start();
    assert.ok(c[sched], 'poll timer armed by start()');

    c.suspend();
    assert.equal(c[sched], null, 'suspend() clears the poll timer');

    c.resume();
    assert.ok(c[sched], 'resume() must re-arm the poll timer in poll mode');
    c.stop();
  });
}

test('no collector clears a poll timer on suspend without restarting it on resume', () => {
  // Source guard for the whole class of bug above: ping and talkers each had a
  // suspend() that stopped polling and a resume() that only knew about streams.
  const fs   = require('fs');
  const path = require('path');
  const dir  = path.join(__dirname, '..', 'src', 'collectors');
  const body = (src, name) => {
    const i = src.indexOf('\n  ' + name + '() {');
    if (i === -1) return null;
    return src.slice(i, src.indexOf('\n  }', i));
  };
  const offenders = [];
  for (const f of fs.readdirSync(dir).filter(f => f.endsWith('.js'))) {
    const src = fs.readFileSync(path.join(dir, f), 'utf8');
    if (!src.includes('streamMode')) continue;          // no poll path to strand
    const s = body(src, 'suspend'), r = body(src, 'resume');
    if (!s || !r) continue;                              // no suspend/resume pair
    const stopsPoll   = /_pollTimer|_stopPoll|_stopRatesPoll|Poll\.stop\(\)/.test(s);
    const restartsPoll = /_schedule|_startRatesPoll|_poll[A-Z]\w*Once|_startPoll|Poll\.start\(\)|streamMode/.test(r);
    if (stopsPoll && !restartsPoll) offenders.push(f);
  }
  assert.deepEqual(offenders, [],
    'these collectors stop polling on suspend and never resume it:\n  ' + offenders.join(', '));
});

test('connections polls when its session is built while a viewer is already watching', async () => {
  // The router-switch bug. buildSession registers TWO ros 'connected' listeners:
  // the first calls conns.resume() (index.js:556), the second runs
  // startCollectors() -> conns.start() (index.js:601). EventEmitter fires them in
  // registration order, so resume() runs while _started is still false and bails
  // out — leaving _suspended=false but no poll timer.
  //
  // In stream mode start() arms a watchdog that resurrects the dead stream, so the
  // race is invisible. Poll mode has no watchdog (correctly — there is no stream to
  // resurrect), so connections goes permanently silent.
  //
  // Cold start hides it too: with no viewer, resume() returns at the clientsCount
  // check and the later idle->active transition schedules the poll properly. It
  // only bites when a viewer is ALREADY present as the session is built — exactly
  // what happens when you switch the active router from one box to another.
  const ConnectionsCollector = require('../src/collectors/connections');
  const ros = pollRos(async () => []);
  const c = new ConnectionsCollector({
    ros, io: { engine: { clientsCount: 1 }, emit() {}, on() {},
               to() { return { emit() {}, to() { return { emit() {} }; } }; } },
    pollMs: 3000, state: {}, topN: 5, maxConns: 100, streamMode: false,
    dhcpNetworks: { networks: [] }, dhcpLeases: { getNameByIP: () => null },
    arp: { getNameByIP: () => null }, connTableCache: { deposit() {} },
  });

  c.resume();                                   // listener 1 — _started still false
  assert.equal(c._pollTimer, null, 'precondition: resume() before start() cannot schedule');
  c.start();                                    // listener 2 — startCollectors()

  assert.ok(c._pollTimer,
    'connections must be polling after start(); otherwise switching to this router ' +
    'leaves the Connections card frozen with no watchdog to recover it');
  c.stop();
});

test('connections still does not poll for a router nobody is watching', () => {
  // The fix must not undo the idle gate: a cold start with no viewer should stay
  // quiet until someone actually connects.
  const ConnectionsCollector = require('../src/collectors/connections');
  const ros = pollRos(async () => []);
  const c = new ConnectionsCollector({
    ros, io: { engine: { clientsCount: 0 }, emit() {}, on() {},
               to() { return { emit() {}, to() { return { emit() {} }; } }; } },
    pollMs: 3000, state: {}, topN: 5, maxConns: 100, streamMode: false,
    dhcpNetworks: { networks: [] }, dhcpLeases: { getNameByIP: () => null },
    arp: { getNameByIP: () => null }, connTableCache: { deposit() {} },
  });
  c.resume();                                   // no viewer — returns immediately
  c.start();
  assert.equal(c._pollTimer, null, 'must stay idle with no viewer present');

  // ...and must come alive on the idle->active transition.
  c.io.engine.clientsCount = 1;
  c.resume();
  assert.ok(c._pollTimer, 'a viewer arriving must start the poll');
  c.stop();
});
