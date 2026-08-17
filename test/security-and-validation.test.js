'use strict';
const { describe, test } = require('node:test');
const assert = require('node:assert/strict');
const path   = require('path');
const fs     = require('fs');

// ═══════════════════════════════════════════════════════════════════════════
// Area 1 — Alerter evaluator (src/alerter.js)
// ═══════════════════════════════════════════════════════════════════════════

// alerter.js requires notifier and routers at load time.
// We inject a fake notifier.send by reaching into the module's require cache
// BEFORE loading alerter, then restoring afterwards. Because Node caches modules,
// we manually insert a stub into the cache so alerter picks it up.

// Build the stub BEFORE requiring alerter
const notifierPath = require.resolve('../src/notifier');
const routersPath  = require.resolve('../src/routers');

// Minimal notifier stub — tracks calls so we can assert on them
const notifierStub = {
  calls: [],
  send: async function(settings, title, body) {
    this.calls.push({ title, body });
  },
};

// Minimal routers stub — alerter calls Routers.getById inside fireConnectivityAlert;
// createEvaluator uses getRouterFn argument, not the module directly.
const routersStub = {
  getById: () => null,
};

// Cache the originals so we can restore them
const origNotifier = require.cache[notifierPath];
const origRouters  = require.cache[routersPath];

// Inject stubs before loading alerter
require.cache[notifierPath] = { id: notifierPath, filename: notifierPath, loaded: true, exports: notifierStub };
require.cache[routersPath]  = { id: routersPath,  filename: routersPath,  loaded: true, exports: routersStub  };

const alerter = require('../src/alerter');

// Restore originals so other test files are unaffected
if (origNotifier) require.cache[notifierPath] = origNotifier; else delete require.cache[notifierPath];
if (origRouters)  require.cache[routersPath]  = origRouters;  else delete require.cache[routersPath];

// Shared helper: inject module-level _settings into alerter via updateSettings
function makeSettings(overrides = {}) {
  return {
    telegramEnabled:   false,
    pushbulletEnabled: false,
    smtpEnabled:       false,
    notifCpu:          true,
    notifPing:         false,
    notifIfaceUpDown:  false,
    notifVpn:          false,
    notifNetwatch:     false,
    notifRouterStatus: false,
    notifCooldownSec:  0,      // 0 so cooldown never blocks in most tests
    alertCpuThreshold: 80,
    alertPingLoss:     10,
    notifTitle:        'Test Alert',
    notifBody:         '{{alertType}}: {{detail}}',
    notifBodyUp:       '{{alertType}}: {{detail}} (recovery)',
    ...overrides,
  };
}

function makeRouter(overrides = {}) {
  return { id: 'r1', host: 'router.local', label: 'Test Router', alertsEnabled: true, ...overrides };
}

describe('alerter createEvaluator', () => {
  test('alertsEnabled guard — evaluate() is a no-op when router.alertsEnabled is false', async () => {
    notifierStub.calls = [];
    alerter.updateSettings(makeSettings({ telegramEnabled: true }));
    const router = makeRouter({ alertsEnabled: false });
    const { evaluate } = alerter.createEvaluator(
      () => 'router',
      () => router,
    );

    evaluate('system:update', { cpuLoad: 99 });
    // Allow microtasks to settle
    await new Promise(r => setImmediate(r));
    assert.equal(notifierStub.calls.length, 0, 'no notification when alertsEnabled is false');
  });

  test('an alert-type toggle suppresses the push it names, and only that', async () => {
    // The push half of the gate that moved out of evaluate() in #109. The
    // detection half — that the event is still recorded and still reaches the
    // bell — is pinned in alert-merge.test.js, the file that stubs the
    // database. Both halves matter: this one alone would still pass if the
    // toggle went back to wrapping the whole rule.
    notifierStub.calls = [];
    alerter.updateSettings(makeSettings({ telegramEnabled: true, notifCpu: false }));
    const { evaluate } = alerter.createEvaluator(() => 'TestRouter', () => makeRouter());

    evaluate('system:update', { cpuLoad: 95 });
    await new Promise(r => setImmediate(r));
    assert.equal(notifierStub.calls.length, 0, 'notifCpu:false must silence the CPU push');

    // A different type carried on the same event is unaffected — the keys are
    // checked per rule, not per event.
    notifierStub.calls = [];
    alerter.updateSettings(makeSettings({ telegramEnabled: true, notifCpu: false, notifRouterUpdate: true }));
    const ev2 = alerter.createEvaluator(() => 'TestRouter', () => makeRouter()).evaluate;
    ev2('system:update', { cpuLoad: 95, updateAvailable: true, latestVersion: '7.99', version: '7.23' });
    await new Promise(r => setImmediate(r));
    assert.equal(notifierStub.calls.length, 1, 'the RouterOS-update push still goes out');
    // Identify the alert by its substance, not its wording — the previous
    // assertion pinned the display name and broke when the alert was renamed,
    // which is a test failing for a reason nobody cares about.
    assert.match(notifierStub.calls[0].body, /7\.99 is available/);
    // That it is *named*, from the one place names are decided. Renaming an
    // alert should not mean editing this test.
    assert.match(notifierStub.calls[0].body,
      new RegExp(alerter.labelFor('routeros_update')));
  });

  test('an alert type reads as a name, never as a database key', () => {
    // alert_type is minted by lowercasing and underscoring, which makes a good
    // key and a poor label. Alerts loaded from the database used to render as
    // "routeros_update" with no indication of which router they came from.
    assert.equal(alerter.labelFor('routeros_update'), 'Update Available');
    assert.equal(alerter.labelFor('routeros_updated'), 'Up To Date');
    assert.equal(alerter.labelFor('connectivity'), 'Router Connectivity');
  });

  test('acronyms survive the title-caser, and unknown types still read as words', () => {
    // A mechanical title-case would render these "Bgp Peer Up" and "Ok".
    assert.equal(alerter.labelFor('bgp_peer_up'), 'BGP Peer Up');
    assert.equal(alerter.labelFor('bgp_hold_timer_ok'), 'BGP Hold Timer OK');
    assert.equal(alerter.labelFor('vpn_disconnected'), 'VPN Disconnected');
    assert.equal(alerter.labelFor('high_cpu'), 'High CPU');
    // An alert type added later must degrade to words rather than leak its key.
    assert.equal(alerter.labelFor('some_future_alert'), 'Some Future Alert');
    assert.equal(alerter.labelFor(''), 'Alert');
    assert.equal(alerter.labelFor(null), 'Alert');
  });

  test('the install destination is just another recipient, and is not duplicated', async () => {
    // #109 turned a single send into a fan-out. The install-wide destination
    // became a recipient with a reserved id, so what is worth pinning is that
    // it still behaves exactly as it did: one send, its own toggles applied.
    notifierStub.calls = [];
    alerter.updateSettings(makeSettings({ telegramEnabled: true, notifCpu: true }));
    const { evaluate } = alerter.createEvaluator(() => 'TestRouter', () => makeRouter());

    evaluate('system:update', { cpuLoad: 95 });
    await new Promise(r => setImmediate(r));
    assert.equal(notifierStub.calls.length, 1,
      'exactly one send with no per-user configs — the fan-out must not duplicate the install');
  });

  test('CPU threshold alert fires when load exceeds threshold', async () => {
    notifierStub.calls = [];
    alerter.updateSettings(makeSettings({ telegramEnabled: true, alertCpuThreshold: 80 }));
    const router = makeRouter();
    const { evaluate } = alerter.createEvaluator(() => 'TestRouter', () => router);

    evaluate('system:update', { cpuLoad: 95 });
    await new Promise(r => setImmediate(r));
    assert.equal(notifierStub.calls.length, 1, 'CPU alert should fire');
    assert.match(notifierStub.calls[0].body, /High CPU/);
  });

  test('CPU recovery alert fires when load drops back below threshold', async () => {
    notifierStub.calls = [];
    alerter.updateSettings(makeSettings({ telegramEnabled: true, alertCpuThreshold: 80 }));
    const router = makeRouter();
    const { evaluate } = alerter.createEvaluator(() => 'TestRouter', () => router);

    // First call: CPU high — alert fires and sets prevCpuAlert = true
    evaluate('system:update', { cpuLoad: 90 });
    await new Promise(r => setImmediate(r));
    assert.equal(notifierStub.calls.length, 1, 'high-CPU alert fired');

    // Second call: CPU recovers — recovery alert should fire
    evaluate('system:update', { cpuLoad: 50 });
    await new Promise(r => setImmediate(r));
    assert.equal(notifierStub.calls.length, 2, 'recovery alert fired');
    assert.match(notifierStub.calls[1].body, /CPU Normal/);
  });

  test('cooldown prevents repeat alert for the same high-CPU condition', async () => {
    notifierStub.calls = [];
    // Use a real cooldown window (10s) so consecutive calls are suppressed
    alerter.updateSettings(makeSettings({ telegramEnabled: true, alertCpuThreshold: 80, notifCooldownSec: 10 }));
    const router = makeRouter();
    const { evaluate } = alerter.createEvaluator(() => 'TestRouter', () => router);

    evaluate('system:update', { cpuLoad: 95 });
    await new Promise(r => setImmediate(r));
    assert.equal(notifierStub.calls.length, 1, 'first alert fires');

    // Second call immediately after — within cooldown window
    evaluate('system:update', { cpuLoad: 95 });
    await new Promise(r => setImmediate(r));
    assert.equal(notifierStub.calls.length, 1, 'second alert suppressed by cooldown');
  });

  test('no-channels guard — evaluate() runs without error and fires nothing when all channels disabled', async () => {
    notifierStub.calls = [];
    alerter.updateSettings(makeSettings({
      telegramEnabled:   false,
      pushbulletEnabled: false,
      smtpEnabled:       false,
    }));
    const router = makeRouter();
    const { evaluate } = alerter.createEvaluator(() => 'TestRouter', () => router);

    assert.doesNotThrow(() => evaluate('system:update', { cpuLoad: 99 }));
    await new Promise(r => setImmediate(r));
    assert.equal(notifierStub.calls.length, 0, 'no notifications when no channels active');
  });
});

// ═══════════════════════════════════════════════════════════════════════════
// Area 3 — Routers validation (src/routers.js)
// ═══════════════════════════════════════════════════════════════════════════
// Strategy: _validateHostPort is internal. We test it through add(), which
// calls it at the top. To prevent file I/O, we pre-populate the module cache
// with a fresh Routers module that has its _cache pre-set in memory.
//
// Because routers.js has module-level mutable state (_cache, _cachedKey), we
// work with the cached module directly. We use invalidateCache() to reset
// between tests that need a clean slate, then monkey-patch _writeFile calls
// by intercepting fs.writeFileSync and fs.renameSync.

const Routers = require('../src/routers');

// Intercept filesystem writes so tests don't touch /data/
// We replace the methods on the fs module temporarily for the duration of
// each add() call that would write to disk.
function withFakeFs(routers, fn) {
  // Pre-inject router list directly into the in-memory cache
  // by doing a first-load read, clearing, then repopulating.
  // The cleanest way: set _cache via a sequence of invalidateCache + remove,
  // but that still tries to read the file. Instead, use a different approach:
  // override fs methods used by _writeFile and _readFile.
  const origWriteFileSync = fs.writeFileSync;
  const origRenameSync    = fs.renameSync;
  const origExistsSync    = fs.existsSync;
  const origReadFileSync  = fs.readFileSync;
  const origMkdirSync     = fs.mkdirSync;

  // Fake in-memory store (pre-seeded)
  const store = routers.map(r => ({
    ...r,
    // Simulate encrypted password (just store as-is for test purposes)
    password: r.password || '',
  }));

  try {
    fs.existsSync    = (p) => p && p.includes('routers') ? true : origExistsSync(p);
    fs.readFileSync  = (p, enc) => {
      if (p && p.includes('routers') && !p.includes('.tmp')) {
        return JSON.stringify(store);
      }
      return origReadFileSync(p, enc);
    };
    fs.writeFileSync = (p, data, opts) => {
      // capture writes to the .tmp file, update our store
      if (p && p.includes('routers')) {
        const parsed = JSON.parse(data);
        store.length = 0;
        parsed.forEach(r => store.push(r));
        return;
      }
      origWriteFileSync(p, data, opts);
    };
    fs.renameSync    = (from, to) => {
      if (from && from.includes('routers')) return; // no-op
      origRenameSync(from, to);
    };
    fs.mkdirSync     = (p, opts) => {
      try { origMkdirSync(p, opts); } catch (_) {}
    };

    // Invalidate module cache so loadAll() re-reads (from our fake readFileSync)
    Routers.invalidateCache();
    return fn(store);
  } finally {
    fs.writeFileSync = origWriteFileSync;
    fs.renameSync    = origRenameSync;
    fs.existsSync    = origExistsSync;
    fs.readFileSync  = origReadFileSync;
    fs.mkdirSync     = origMkdirSync;
    Routers.invalidateCache();
  }
}

describe('routers _validateHostPort (via add())', () => {
  test('valid IPv4 hostname accepted', () => {
    withFakeFs([], () => {
      assert.doesNotThrow(() => {
        Routers.add({ host: '192.168.1.1', port: 8729, username: 'admin', password: '' });
      }, 'valid IPv4 should not throw');
    });
  });

  test('valid FQDN hostname accepted', () => {
    withFakeFs([], () => {
      assert.doesNotThrow(() => {
        Routers.add({ host: 'router.local', port: 8729, username: 'admin', password: '' });
      }, 'valid FQDN should not throw');
    });
  });

  test('valid simple hostname accepted', () => {
    withFakeFs([], () => {
      assert.doesNotThrow(() => {
        Routers.add({ host: 'mikrotik-rb', port: 8291, username: 'admin', password: '' });
      }, 'hostname with hyphen should not throw');
    });
  });

  test('empty host is rejected', () => {
    withFakeFs([], () => {
      assert.throws(
        () => Routers.add({ host: '', port: 8729, username: 'admin', password: '' }),
        /Invalid host/,
        'empty host should throw Invalid host'
      );
    });
  });

  test('host with spaces is rejected', () => {
    withFakeFs([], () => {
      assert.throws(
        () => Routers.add({ host: 'router host', port: 8729, username: 'admin', password: '' }),
        /Invalid host/,
        'host with spaces should throw Invalid host'
      );
    });
  });

  test('host with special characters is rejected', () => {
    withFakeFs([], () => {
      assert.throws(
        () => Routers.add({ host: 'router!@#$', port: 8729, username: 'admin', password: '' }),
        /Invalid host/,
        'host with special chars should throw Invalid host'
      );
    });
  });

  test('valid port 8729 is accepted', () => {
    withFakeFs([], () => {
      assert.doesNotThrow(() => {
        Routers.add({ host: '10.0.0.1', port: 8729, username: 'admin', password: '' });
      }, 'port 8729 should not throw');
    });
  });

  test('valid port 8291 is accepted', () => {
    withFakeFs([], () => {
      assert.doesNotThrow(() => {
        Routers.add({ host: '10.0.0.1', port: 8291, username: 'admin', password: '' });
      }, 'port 8291 should not throw');
    });
  });

  test('port 0 is rejected', () => {
    withFakeFs([], () => {
      assert.throws(
        () => Routers.add({ host: '10.0.0.1', port: 0, username: 'admin', password: '' }),
        /Invalid port/,
        'port 0 should throw Invalid port'
      );
    });
  });

  test('port 65536 is rejected', () => {
    withFakeFs([], () => {
      assert.throws(
        () => Routers.add({ host: '10.0.0.1', port: 65536, username: 'admin', password: '' }),
        /Invalid port/,
        'port 65536 should throw Invalid port'
      );
    });
  });

  test('port NaN is rejected', () => {
    withFakeFs([], () => {
      assert.throws(
        () => Routers.add({ host: '10.0.0.1', port: NaN, username: 'admin', password: '' }),
        /Invalid port/,
        'port NaN should throw Invalid port'
      );
    });
  });

  test('port "abc" (non-numeric string) is rejected', () => {
    withFakeFs([], () => {
      assert.throws(
        () => Routers.add({ host: '10.0.0.1', port: 'abc', username: 'admin', password: '' }),
        /Invalid port/,
        'non-numeric port string should throw Invalid port'
      );
    });
  });

  test('isMasked sentinel password is stored as empty string, not the sentinel', () => {
    withFakeFs([], (store) => {
      Routers.add({ host: '10.0.0.1', port: 8729, username: 'admin', password: '••••••••' });
      // The entry stored in-memory (in the module's _cache) has the plaintext password.
      // Verify via loadAll() that the sentinel was not saved.
      const loaded = Routers.loadAll();
      const entry = loaded[loaded.length - 1];
      assert.equal(entry.password, '', 'sentinel password must be stored as empty string, not "••••••••"');
    });
  });
});

// ═══════════════════════════════════════════════════════════════════════════
// Area N — Topology layout persistence (src/topologyLayout.js)
//
// This is the only endpoint where caller-supplied strings become OBJECT KEYS in
// a file written to disk, so the validator is tested directly rather than only
// through a running server.
// ═══════════════════════════════════════════════════════════════════════════

describe('topology layout validation', () => {
  const { isValidRouterId, cleanPositions, MAX_NODES, COORD_LIMIT } =
    require('../src/topologyLayout');

  test('accepts a well-formed positions map and rounds coordinates', () => {
    const out = cleanPositions({ '48:A9:8A:E5:CE:34': { x: 320.4567, y: -180.5123 } });
    assert.deepEqual(out['48:A9:8A:E5:CE:34'], { x: 320.5, y: -180.5 });
  });

  test('accepts the id: fallback key used when a neighbour has no MAC', () => {
    assert.ok(cleanPositions({ 'id:3': { x: 1, y: 2 } }));
  });

  test('rejects prototype-pollution keys outright', () => {
    for (const k of ['__proto__', 'constructor', 'prototype']) {
      const raw = {};
      Object.defineProperty(raw, k, { value: { x: 1, y: 1 }, enumerable: true, configurable: true });
      assert.equal(cleanPositions(raw), null, k + ' must be rejected');
    }
    assert.equal(Object.prototype.polluted, undefined);
  });

  test('rejects path traversal and separators in keys', () => {
    for (const k of ['../../etc/passwd', 'a/b', 'a\\b', 'a b', 'k'.repeat(65), '']) {
      assert.equal(cleanPositions({ [k]: { x: 1, y: 1 } }), null, JSON.stringify(k));
    }
  });

  test('rejects non-finite, missing and non-numeric coordinates', () => {
    for (const v of [{ x: NaN, y: 0 }, { x: 0, y: Infinity }, { x: 'a', y: 1 }, { x: 1 }, {}, null, [1, 2], 'x']) {
      assert.equal(cleanPositions({ 'AA:BB': v }), null, JSON.stringify(v));
    }
  });

  test('clamps coordinates rather than trusting them', () => {
    const out = cleanPositions({ 'AA:BB': { x: 1e9, y: -1e9 } });
    assert.equal(out['AA:BB'].x, COORD_LIMIT);
    assert.equal(out['AA:BB'].y, -COORD_LIMIT);
  });

  test('rejects an oversized map', () => {
    const raw = {};
    for (let i = 0; i <= MAX_NODES; i++) raw['AA:BB:CC:00:00:' + i] = { x: 0, y: 0 };
    assert.equal(cleanPositions(raw), null);
  });

  test('rejects arrays and non-objects', () => {
    for (const v of [null, undefined, [], 'x', 42, true]) assert.equal(cleanPositions(v), null);
  });

  test('an empty map is valid — it is how Re-layout resets a router', () => {
    assert.deepEqual(Object.keys(cleanPositions({})), []);
  });

  test('the result has a null prototype, so a later key write cannot pollute', () => {
    assert.equal(Object.getPrototypeOf(cleanPositions({ 'AA:BB': { x: 0, y: 0 } })), null);
  });

  test('router ids are constrained to a safe filename-ish charset', () => {
    assert.ok(isValidRouterId('dc1c5d9c-6df4-411b-a741-0245c66a2ad7'));
    assert.ok(isValidRouterId('r1'));
    for (const bad of ['../etc', 'a/b', 'a b', '', 'x'.repeat(65), 'a.b', null, 42]) {
      assert.equal(isValidRouterId(bad), false, JSON.stringify(bad));
    }
  });
});
