'use strict';
// Model / serial / RouterOS version shown in the Settings → Routers table.
//
// These are learned from RouterOS but persisted onto the router entry, so the
// columns stay populated for a router that is offline or disabled. That makes
// three things worth guarding: the store writes only on a real change, a user
// editing a router does not wipe what was learned, and the collector reports
// identity when (and only when) it actually changes.

const test   = require('node:test');
const assert = require('node:assert');
// Stops every collector these tests construct once the file finishes; without
// it their timers keep the test process alive. See the helper for why that
// made the reported test count unstable.
const { track } = require('./helpers/collector-cleanup');
const fs     = require('fs');
const os     = require('os');
const path   = require('path');

function makeTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-identity-'));
}

// routers.js reads DATA_DIR at module load and keeps a module-level cache, so
// each test needs a fresh copy pointed at its own directory.
function freshRouters(tmpDir) {
  process.env.DATA_DIR = tmpDir;
  delete require.cache[require.resolve('../src/routers')];
  delete require.cache[require.resolve('../src/settings')];
  return require('../src/routers');
}

const IDENTITY = { model: 'hAP ax^3', serial: 'HFX09XXXXXX', osVersion: '7.20.2' };

// ── Store ────────────────────────────────────────────────────────────────────

test('updateIdentity persists model, serial and version across a reload', () => {
  const tmp = makeTmpDir();
  const R = freshRouters(tmp);
  const added = R.add({ host: '192.168.88.1', label: 'edge' });

  const updated = R.updateIdentity(added.id, IDENTITY);
  assert.equal(updated.model, 'hAP ax^3');
  assert.equal(updated.serial, 'HFX09XXXXXX');
  assert.equal(updated.osVersion, '7.20.2');

  // Re-read from disk, not from the in-memory cache — persistence is the point.
  const R2 = freshRouters(tmp);
  const reloaded = R2.getById(added.id);
  assert.equal(reloaded.model, 'hAP ax^3');
  assert.equal(reloaded.serial, 'HFX09XXXXXX');
  assert.equal(reloaded.osVersion, '7.20.2');
});

test('updateIdentity returns null and writes nothing when identity is unchanged', () => {
  const tmp = makeTmpDir();
  const R = freshRouters(tmp);
  const added = R.add({ host: '192.168.88.1' });
  R.updateIdentity(added.id, IDENTITY);

  // The system collector reports identity on every tick. A no-op repeat must
  // not cost a file write or a broadcast to every connected client.
  const origWrite = fs.writeFileSync;
  let writes = 0;
  fs.writeFileSync = function (...args) { writes++; return origWrite.apply(fs, args); };
  try {
    assert.equal(R.updateIdentity(added.id, IDENTITY), null, 'repeat report returns null');
    assert.equal(writes, 0, 'no file write for an unchanged identity');
  } finally {
    fs.writeFileSync = origWrite;
  }
});

test('updateIdentity records a version upgrade but leaves model and serial alone', () => {
  const tmp = makeTmpDir();
  const R = freshRouters(tmp);
  const added = R.add({ host: '192.168.88.1' });
  R.updateIdentity(added.id, IDENTITY);

  const after = R.updateIdentity(added.id, { ...IDENTITY, osVersion: '7.21.1' });
  assert.ok(after, 'a version change is a real change');
  assert.equal(after.osVersion, '7.21.1');
  assert.equal(after.serial, 'HFX09XXXXXX');
  assert.equal(after.model, 'hAP ax^3');
});

test('updateIdentity ignores blank and non-string values, and caps length', () => {
  const tmp = makeTmpDir();
  const R = freshRouters(tmp);
  const added = R.add({ host: '192.168.88.1' });
  R.updateIdentity(added.id, IDENTITY);

  // A router that reports an empty serial must not erase a known one.
  assert.equal(R.updateIdentity(added.id, { serial: '' }), null);
  assert.equal(R.updateIdentity(added.id, { serial: null }), null);
  assert.equal(R.updateIdentity(added.id, { serial: { evil: true } }), null);
  assert.equal(R.getById(added.id).serial, 'HFX09XXXXXX');

  const long = R.updateIdentity(added.id, { model: 'M'.repeat(200) });
  assert.equal(long.model.length, 64, 'model capped at 64 chars');
});

test('updateIdentity is a no-op for an unknown router id', () => {
  const tmp = makeTmpDir();
  const R = freshRouters(tmp);
  R.add({ host: '192.168.88.1' });
  assert.equal(R.updateIdentity('no-such-id', IDENTITY), null);
});

test('editing a router preserves learned identity', () => {
  const tmp = makeTmpDir();
  const R = freshRouters(tmp);
  const added = R.add({ host: '192.168.88.1', label: 'edge' });
  R.updateIdentity(added.id, IDENTITY);

  // update() rebuilds the record field by field; identity is not in that list,
  // so this guards against it being dropped the way the settings allowlist
  // silently dropped new keys.
  const edited = R.update(added.id, { label: 'edge-renamed', port: 8729 });
  assert.equal(edited.label, 'edge-renamed');
  assert.equal(edited.serial, 'HFX09XXXXXX', 'serial survived an edit');
  assert.equal(edited.model, 'hAP ax^3');
  assert.equal(edited.osVersion, '7.20.2');
});

test('getPublic exposes identity to the browser but still masks the password', () => {
  const tmp = makeTmpDir();
  const R = freshRouters(tmp);
  const added = R.add({ host: '192.168.88.1', password: 'sup3r-secret' });
  R.updateIdentity(added.id, IDENTITY);

  const pub = R.getPublic().find(r => r.id === added.id);
  assert.equal(pub.serial, 'HFX09XXXXXX', 'serial reaches routers:update');
  assert.equal(pub.model, 'hAP ax^3');
  assert.equal(pub.osVersion, '7.20.2');
  assert.equal(pub.password, '••••••••', 'password still masked');
  assert.ok(!JSON.stringify(pub).includes('sup3r-secret'), 'no plaintext password leaks');
});

// ── Collector reporting ──────────────────────────────────────────────────────

const SystemCollector = track(require('../src/collectors/system'));

function makeSystemCollector() {
  const chain = { emit() {} }; chain.to = () => chain;
  const io = { engine: { clientsCount: 1 }, emit() {}, to: () => chain };
  const c = new SystemCollector({ ros: { connected: true, on() {} }, io, pollMs: 5000, state: {} });
  c._lastUpdateFetch = Date.now();
  c._lastHealth = [];
  c._lastUpdateRow = {};
  return c;
}

const ROW = { 'cpu-load': '5', 'total-memory': '100', 'free-memory': '50',
              'board-name': 'RB5009', version: '7.20.2 (stable)', 'architecture-name': 'arm64' };

test('system collector reports identity once, not on every tick', () => {
  const c = makeSystemCollector();
  c._staticSerial = 'E41AXXXXXXXX';
  const seen = [];
  c._onIdentity = (identity) => seen.push(identity);

  c._processRow({ ...ROW });
  c._processRow({ ...ROW });
  c._processRow({ ...ROW });

  assert.equal(seen.length, 1, 'unchanged identity reported only once');
  // Bare version: RouterOS reports "7.20.2 (stable)", the table shows "7.20.2".
  assert.deepEqual(seen[0], { model: 'RB5009', serial: 'E41AXXXXXXXX', osVersion: '7.20.2' });
  assert.equal(c.lastPayload.version, '7.20.2 (stable)',
    'system:update keeps the channel — only the stored identity is stripped');
});

test('a channel switch at the same release is not a version change', () => {
  const c = makeSystemCollector();
  c._staticSerial = 'E41AXXXXXXXX';
  const seen = [];
  c._onIdentity = (identity) => seen.push(identity);

  c._processRow({ ...ROW });
  c._processRow({ ...ROW, version: '7.20.2 (testing)' });

  assert.equal(seen.length, 1, 'stable→testing at 7.20.2 must not churn a write and a broadcast');
});

test('system collector re-reports when the RouterOS version changes', () => {
  const c = makeSystemCollector();
  c._staticSerial = 'E41AXXXXXXXX';
  const seen = [];
  c._onIdentity = (identity) => seen.push(identity);

  c._processRow({ ...ROW });
  c._processRow({ ...ROW, version: '7.21.1 (stable)' });

  assert.equal(seen.length, 2, 'an upgrade must not be swallowed by a write-once guard');
  assert.equal(seen[1].osVersion, '7.21.1');
  assert.equal(seen[1].serial, 'E41AXXXXXXXX');
});

test('system collector without an identity hook still emits normally', () => {
  const c = makeSystemCollector();
  c._staticSerial = 'E41AXXXXXXXX';
  assert.doesNotThrow(() => c._processRow({ ...ROW }));
  assert.equal(c.lastPayload.boardName, 'RB5009');
});
