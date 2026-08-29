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

// ═══════════════════════════════════════════════════════════════════════════
// Area 8 — the {{comment}} notification variable (issue #116)
// ═══════════════════════════════════════════════════════════════════════════
//
// Operators comment their interfaces with what is actually plugged in, so
// "ether5 went down" is far less useful than knowing it is the uplink. #116
// asked for that comment in alerts; folding it into the alert text was rejected
// because it would rewrite the wording every existing user already receives.
//
// The design instead is a generic {{comment}} template variable that is NOT in
// the default template. The template is the opt-in, which is why the inverse
// assertions below matter more than the positive ones: they are what pins
// "nobody's alerts change unless they ask for it".
describe('{{comment}} notification variable', () => {
  // fire() requires BOTH keys for an interface alert — the feature toggle and
  // the per-type filter. Omitting notifIfaceEther yields a test that passes
  // while asserting nothing, because no push ever happens.
  const IFACE_SETTINGS = {
    telegramEnabled: true, notifIfaceUpDown: true, notifIfaceEther: true,
  };

  const up   = (over) => [Object.assign({ name: 'ether1', type: 'ether', running: true,  disabled: false }, over)];
  const down = (over) => [Object.assign({ name: 'ether1', type: 'ether', running: false, disabled: false }, over)];

  async function fireIface(settings, first, second) {
    notifierStub.calls = [];
    alerter.updateSettings(makeSettings(Object.assign({}, IFACE_SETTINGS, settings)));
    const { evaluate } = alerter.createEvaluator(() => 'TestRouter', () => makeRouter());
    evaluate('ifstatus:update', { interfaces: first });
    evaluate('ifstatus:update', { interfaces: second });
    await new Promise(r => setImmediate(r));
    return notifierStub.calls;
  }

  test('the comment reaches the body when the template asks for it', async () => {
    const calls = await fireIface(
      { notifBody: '{{ifaceName}} ({{comment}})' },
      up({ comment: 'Uplink to ISP' }), down({ comment: 'Uplink to ISP' }),
    );
    assert.equal(calls.length, 1);
    assert.equal(calls[0].body, 'ether1 (Uplink to ISP)');
  });

  test('and does not reach it when the template does not', async () => {
    // The opt-in itself. Without this the feature would still "pass" if someone
    // had folded the comment into {{detail}} — the design that was rejected.
    const calls = await fireIface(
      { notifBody: '{{alertType}}: {{detail}}' },
      up({ comment: 'Uplink to ISP' }), down({ comment: 'Uplink to ISP' }),
    );
    assert.equal(calls.length, 1);
    assert.equal(calls[0].body, 'Interface Down: ether1 went down');
    assert.ok(!calls[0].body.includes('Uplink'), 'comment must not appear unasked');
  });

  test('an interface with no comment renders the placeholder as empty', async () => {
    // Exact equality, not a match: this pins the deliberate decision to leave
    // the empty-parenthesis wart alone rather than add conditional sections or
    // a name fallback. Every other variable already behaves this way, and a
    // name fallback here would render a lie — "ether1 (ether1)".
    const calls = await fireIface(
      { notifBody: '{{ifaceName}} ({{comment}})' },
      up(), down(),
    );
    assert.equal(calls.length, 1);
    assert.equal(calls[0].body, 'ether1 ()');
  });

  test('the recovery template carries it too', async () => {
    // Both fire() sites, not only the down one. A fix applied to just one of
    // the pair passes every test above.
    notifierStub.calls = [];
    alerter.updateSettings(makeSettings(Object.assign({}, IFACE_SETTINGS, {
      notifBody: 'down {{comment}}', notifBodyUp: 'up {{comment}}',
    })));
    const { evaluate } = alerter.createEvaluator(() => 'TestRouter', () => makeRouter());
    evaluate('ifstatus:update', { interfaces: up({ comment: 'Uplink' })   });
    evaluate('ifstatus:update', { interfaces: down({ comment: 'Uplink' }) });
    evaluate('ifstatus:update', { interfaces: up({ comment: 'Uplink' })   });
    await new Promise(r => setImmediate(r));
    assert.equal(notifierStub.calls.length, 2);
    assert.equal(notifierStub.calls[0].body, 'down Uplink');
    assert.equal(notifierStub.calls[1].body, 'up Uplink');
  });

  test('a hostile comment is capped and stripped like every other variable', async () => {
    // This is the first template variable that is free-form operator text the
    // router controls end to end, with no MikroDash formatting in between. A
    // newline surviving here would inject a header into the ntfy Title.
    const nasty = 'A'.repeat(250) + '\u000aB';
    const calls = await fireIface(
      { notifBody: '{{comment}}' },
      up({ comment: nasty }), down({ comment: nasty }),
    );
    assert.equal(calls[0].body.length, 200, 'capped at 200 characters');
    assert.ok(!calls[0].body.includes('\u000a'), 'control characters stripped');
  });

  test('a comment shorter than the cap survives whole', async () => {
    const c = 'B'.repeat(199);
    const calls = await fireIface({ notifBody: '{{comment}}' }, up({ comment: c }), down({ comment: c }));
    assert.equal(calls[0].body, c);
  });

  test('settings are still not reachable as template variables', async () => {
    // There is a standing warning above _render that _settings must never be
    // spread into vars, and until now nothing pinned it. Adding a variable is
    // exactly the moment someone widens that object "just a little".
    //
    // This doubles as the generic-contract test: {{comment}} renders empty on
    // an alert type with no commented RouterOS object behind it.
    notifierStub.calls = [];
    alerter.updateSettings(makeSettings({
      telegramEnabled: true, notifCpu: true,
      telegramBotToken: 'SECRET-TOKEN', smtpPass: 'SECRET-PASS',
      notifBody: '[{{telegramBotToken}}|{{smtpPass}}|{{comment}}]',
    }));
    const { evaluate } = alerter.createEvaluator(() => 'TestRouter', () => makeRouter());
    evaluate('system:update', { cpuLoad: 95 });
    await new Promise(r => setImmediate(r));
    assert.equal(notifierStub.calls.length, 1);
    assert.equal(notifierStub.calls[0].body, '[||]');
  });

  test('the shipped defaults and the UI placeholders stay free of it', () => {
    // The only test here that pins the maintainer's actual decision rather than
    // the mechanics. It fails, by name, the day someone helpfully adds
    // {{comment}} to the stock template and thereby changes the wording every
    // existing user receives without anyone asking them.
    const DEFAULTS = require('../src/settings').DEFAULTS;
    assert.ok(!DEFAULTS.notifBody.includes('{{comment}}'),   'default body must stay unchanged');
    assert.ok(!DEFAULTS.notifBodyUp.includes('{{comment}}'), 'default recovery body must stay unchanged');

    const html = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');
    assert.ok(html.includes('{{comment}}'), 'the variable must be documented or nobody can find it');
    assert.ok(!/placeholder="[^"]*\{\{comment\}\}/.test(html), 'and must stay out of the textarea placeholders');
  });
});

// ═══════════════════════════════════════════════════════════════════════════
// Area 9 — attribute escaping and payload shapes in app.js (ToDo #16, #17)
// ═══════════════════════════════════════════════════════════════════════════
describe('app.js escaping and payload shapes', () => {
  const APP_SRC = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');

  test('no HTML attribute is built with the text-only escaper', () => {
    // dcEsc round-trips through a text node, which is the browser's own TEXT
    // escaper: it handles & < > and deliberately leaves " and ' alone. Correct
    // for its ten text-position uses, wrong inside an attribute.
    //
    // Confirmed reachable on hardware rather than assumed: a hAP ac2 running
    // RouterOS 7.24 accepted an interface named qt"test and the API returned
    // the raw quote, so an interface called `ether1" onmouseover="x` would have
    // closed the Physical Ports title attribute and opened another.
    //
    // A sweep rather than two assertions, because the defect was one helper
    // used in two contexts: the Interfaces page renders the same payload with
    // esc() and was always right, which is what made the card look like a
    // correct copy of it.
    // The gap is `="'+` : this file builds HTML inside single-quoted JS strings,
    // so an attribute opens with the HTML quote immediately followed by the JS
    // string terminator and a concatenation. An earlier version of this regex
    // looked for the value directly after `="` and matched neither real site,
    // which made the test vacuous. `[^\n>]` keeps it from reaching past the tag
    // and flagging the ten legitimate text-position uses.
    const bad = [];
    const re = /=["'][^\n>]{0,4}\+\s*dcEsc\s*\(/g;
    for (const m of APP_SRC.matchAll(re)) {
      bad.push(APP_SRC.slice(Math.max(0, m.index - 60), m.index + 40).replace(/\s+/g, ' '));
    }
    assert.deepEqual(bad, [],
      'dcEsc does not escape quotes, so it must not build an attribute value; use esc(): ' +
      bad.join(' | '));
  });

  test('dcEsc stays a text escaper', () => {
    // The inverse of the sweep above, and why the fix went to the two call
    // sites rather than to the helper. Ten text-position uses rely on quotes
    // and apostrophes surviving; "fixing" dcEsc to escape them would visibly
    // mangle any name containing an apostrophe.
    assert.ok(/function dcEsc\(s\)\{[^}]*textContent[^}]*innerHTML/.test(APP_SRC),
      'dcEsc must keep round-tripping through a text node');
    assert.ok(!/function dcEsc\(s\)\{[^}]*&quot;/.test(APP_SRC),
      'dcEsc must not start escaping quotes; fix the call site instead');
  });

  test('the logs card accepts both payload shapes the server sends', () => {
    // `data.entries || data` looks like it handles both and cannot: on a bare
    // array, data.entries is Array.prototype.entries, a truthy FUNCTION, so the
    // first operand wins and the isArray guard below it rejects the payload.
    //
    // Reachable because the two emit sites disagree. src/index.js sends a bare
    // array on connect and { entries } on card focus, so the connect-time
    // replay was dropped and the card stayed blank until a focus arrived.
    const vm = require('node:vm');
    const line = APP_SRC.match(/var entries=Array\.isArray\(data\).*?;/);
    assert.ok(line, 'the logs:history shape guard moved or was rewritten');

    const rows = [{ time: '12:00:00', topics: 'system', message: 'hello' }];
    const run = (data) => {
      const ctx = { data };
      vm.createContext(ctx);
      vm.runInContext(line[0], ctx);
      return ctx.entries;
    };

    assert.equal(run(rows).length, 1, 'bare array, the connect-time replay');
    assert.equal(run({ entries: rows }).length, 1, 'wrapped, the card-focus shape');
    assert.equal(run(null).length, 0, 'and a null payload is still safe');

    // Proves the test is not vacuous: the old form silently yields a non-array
    // for the bare-array case, which is what made the card render nothing.
    const oldCtx = { data: rows };
    vm.createContext(oldCtx);
    vm.runInContext('var entries=data.entries||data||[];', oldCtx);
    assert.ok(!Array.isArray(oldCtx.entries),
      'the old expression must be the broken one, or this test proves nothing');
  });
});

// ── Reusing a stored password for a connection test (#117 followup) ─────────
//
// The device modal blanks the password on edit — its placeholder says "leave
// blank to keep current" — while Save refuses to write until a connection test
// passes. With no password that test could never pass, so NO field of an
// existing device could be saved without retyping the credential. Reported on
// #117 as sites not being removable; it was never about sites, and it has been
// true since the test gate landed in 0.5.33.
//
// The fix lets POST /api/routers/test fall back to the stored password. That is
// only safe because of the predicate below, so the predicate is what is tested:
// looking a password up by id ALONE would turn the route into a credential
// oracle — submit a stored id with an attacker-chosen host and the server posts
// the saved secret to it.
describe('sameEndpoint — when a stored credential may be reused', () => {
  const STORED = { host: '10.0.0.53', port: 8729, username: 'mikrodash', tls: true, tlsInsecure: false };
  const like = (over) => Object.assign({}, STORED, over);

  test('an unchanged endpoint matches', () => {
    assert.equal(Routers.sameEndpoint(STORED, like({})), true);
  });

  test('a different host does NOT match — this is the exfiltration case', () => {
    // The whole reason the predicate exists. An admin editing a device could
    // otherwise point it at a host they control, leave the password blank, and
    // have the server deliver the stored secret there.
    assert.equal(Routers.sameEndpoint(STORED, like({ host: '10.9.9.9' })), false);
    assert.equal(Routers.sameEndpoint(STORED, like({ host: 'evil.example.com' })), false);
  });

  test('a different port or username does not match', () => {
    assert.equal(Routers.sameEndpoint(STORED, like({ port: 8728 })), false);
    assert.equal(Routers.sameEndpoint(STORED, like({ username: 'admin' })), false);
  });

  test('turning TLS off does not match', () => {
    // Same host, but the secret would now cross the wire in clear text where
    // anyone on the path can read it. That is a downgrade, not a detail.
    assert.equal(Routers.sameEndpoint(STORED, like({ tls: false })), false);
  });

  test('accepting an invalid certificate does not match', () => {
    // The subtlest of the five, and the one most likely to be dropped as
    // over-strict: tlsInsecure accepts a FORGED certificate, so the right
    // hostname becomes a man-in-the-middle and the credential is readable.
    assert.equal(Routers.sameEndpoint(STORED, like({ tlsInsecure: true })), false);
  });

  test('the host comparison is case-insensitive but not loose', () => {
    assert.equal(Routers.sameEndpoint({ ...STORED, host: 'Router.Example.COM' },
                                      like({ host: 'router.example.com' })), true);
    // A prefix or suffix is a DIFFERENT host and must not match.
    assert.equal(Routers.sameEndpoint({ ...STORED, host: 'router.example.com' },
                                      like({ host: 'router.example.com.evil.net' })), false);
  });

  test('absent tls fields are read the same way on both sides', () => {
    // A record stored before a field existed must not compare unequal to a form
    // that defaults it, or the fallback silently stops working for old devices.
    assert.equal(Routers.sameEndpoint({ host: '10.0.0.53', port: 8729, username: 'mikrodash' },
                                      like({})), true);
  });

  test('a missing or empty host never matches', () => {
    assert.equal(Routers.sameEndpoint(null, like({})), false);
    assert.equal(Routers.sameEndpoint(STORED, null), false);
    assert.equal(Routers.sameEndpoint({ host: '', port: 8729, username: 'mikrodash' },
                                      { host: '', port: 8729, username: 'mikrodash' }), false);
  });
});

test('the test route only falls back when the submitted password is empty', () => {
  // A source scan for the reason this file's siblings give: the route needs a
  // live server stack. What must hold is the WIRING — a fallback that ran even
  // when a password was supplied would silently ignore what the admin typed,
  // and one that skipped sameEndpoint would be the oracle.
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');
  const at = src.indexOf("app.post('/api/routers/test'");
  assert.ok(at > 0, 'the test route moved or was renamed');
  const block = src.slice(at, at + 3000);

  assert.match(block, /if \(!_testPass && body\.id\)/,
    'the fallback must be reached only when no password was supplied');
  assert.match(block, /Routers\.sameEndpoint\(/,
    'the stored password may only be reused for a matching endpoint');

  const guard = block.indexOf('Routers.sameEndpoint(');
  const use   = block.indexOf('_testPass = String(_stored.password)');
  assert.ok(guard > 0 && use > guard,
    'the endpoint check has to happen BEFORE the stored password is adopted');

  const line = src.slice(at, src.indexOf('\n', at));
  assert.ok(line.includes('Rbac.requireGlobalAdmin'),
    'the test route must stay administrator-only');
});

// ── A JSON null name is missing, not the string "null" (ToDo §6) ────────────
//
// `_parseSiteBody` and `_parseName` tested `b.name === undefined` — strict — so
// a JSON null fell through to String(null), which is four characters, passes
// the 1-64 length check, and creates a record literally called "null". A second
// such POST is then refused with 409 "a site with that name already exists",
// which is a confusing thing to be told about a field you sent as empty.
//
// `{"name": null}` is what a cleared form field serialises to, so this is
// reachable from the UI rather than only from curl.
//
// The tell that it was accidental rather than deliberate: the DESCRIPTION check
// two lines below each one already used loose `== null` and never had the
// problem. The two lines are now consistent, and this test pins them together
// so a future edit cannot quietly split them again.
test('a null name is treated as missing by every parser that takes one', () => {
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');

  const nameChecks = src.match(/const name = String\(b\.name [^)]*\)\.trim\(\);/g) || [];
  assert.ok(nameChecks.length >= 2,
    'expected the site and principal name parsers; found ' + nameChecks.length);
  for (const line of nameChecks) {
    assert.ok(!/b\.name === undefined/.test(line),
      'strict === undefined lets a JSON null through as the string "null": ' + line);
    assert.match(line, /b\.name == null/,
      'a null name must be read as missing: ' + line);
  }

  // The sibling that was always right. If someone "tidies" these to match by
  // making the description strict, that is the same bug in the other field.
  const descChecks = src.match(/const d = String\(b\.description [^)]*\)\.trim\(\);/g) || [];
  assert.ok(descChecks.length >= 2, 'expected the matching description parsers');
  for (const line of descChecks) {
    assert.match(line, /b\.description == null/,
      'the description check must stay loose too: ' + line);
  }
});

// ── The connection test's error classifier ──────────────────────────────────
//
// Found by the Go port, which lifted this handler into a vm and drove it. The
// handler needs a live server stack, so the wiring is scanned; the DIVERGENCE
// test below is the one that matters, because it pins the rule rather than
// today's symptom.
describe('POST /api/routers/test error classification', () => {
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');
  const at  = src.indexOf("testRos.on('connectionError'");
  const handler = src.slice(at, src.indexOf("testRos.on('connected'", at));

  test('the TLS sentence describes the connection that was attempted', () => {
    // It tells the operator WHICH RouterOS service to go and check, so it has
    // to read the coerced value the ROS constructor was given, not the raw
    // request field. body.tls disagrees with testTls in both directions:
    // absent -> testTls true but body.tls falsy (api-ssl blamed on api);
    // the string "false" -> testTls false but the string is truthy (a plain
    // connection blamed on a TLS handshake that never happened).
    assert.ok(at > 0, 'the connectionError handler moved or was renamed');
    assert.ok(!/reason = body\.tls/.test(handler),
      'the raw request field describes a connection that may not have been attempted');
    assert.match(handler, /reason = testTls/,
      'the coerced value is the one the ROS constructor was given');
  });

  test('the two error classifiers agree on what counts as an auth failure', () => {
    // THE ACTUAL RULE. This handler is a second copy of the decision that lives
    // in classifyError.js, and copies drift: RouterOS's own words for a rejected
    // login are "invalid user name or password", which classifyError.js caught
    // and this one did not — so the commonest failure of the Test Connection
    // button showed the raw driver message instead of a useful sentence.
    //
    // Comparing the alternative SETS rather than asserting today's list, so the
    // test keeps working when either side legitimately gains a pattern, and
    // fails the moment one gains it alone.
    const canon = fs.readFileSync(path.join(__dirname, '..', 'src', 'routeros', 'classifyError.js'), 'utf8');
    const authLine = (s) => (s.split('\n').find(l => l.includes("'Authentication failed")) ||
                             s.split('\n').find(l => l.includes('Authentication failed')) || '');
    const patterns = (s) => new Set((s.match(/\/[^/\n]+\/i\.test\(/g) || []).map(x => x.slice(0, -6)));

    // classifyError.js splits reason onto its own line; take the branch above it.
    const canonIdx  = canon.split('\n').findIndex(l => l.includes('Authentication failed'));
    // THE EXTRACTION MUST PROVE IT FOUND SOMETHING. A ledger whose extractor
    // silently returns nothing compares two empty sets and passes for ever —
    // green about a relationship it can no longer see. Reword either sentence
    // and this currently dies on a TypeError, which is a crash rather than a
    // verdict: it happens to fail, and one `|| ''` anywhere in this path would
    // turn that into a permanent pass. Assert, rather than rely on the crash.
    assert.ok(canonIdx > 0,
      'could not locate the canonical auth branch in classifyError.js — a ledger ' +
      'cannot compare what it cannot find, so this is a failure, not a pass');
    const canonAuth = canon.split('\n')[canonIdx - 1] + canon.split('\n')[canonIdx];
    const hereAuth  = authLine(handler);

    assert.ok(hereAuth, 'the auth branch moved or was renamed');
    assert.ok(patterns(canonAuth).size > 0,
      'the canonical branch was found but yielded no patterns — the extractor has ' +
      'stopped understanding the shape it reads');
    const missing = [...patterns(canonAuth)].filter(p => !patterns(hereAuth).has(p));
    assert.deepEqual(missing, [],
      'classifyError.js recognises these and the test route does not, so the same ' +
      'router error gets two different answers: ' + missing.join(', '));
  });

  test("RouterOS's own rejection wording is recognised", () => {
    // The concrete case, asserted against the canonical classifier, which is
    // callable. "invalid user name or password" matches none of the older
    // alternatives: no "authentication", no "login", and "user name" is two
    // words where the pattern wants one.
    const { classifyRosError } = require('../src/routeros/classifyError');
    const out = classifyRosError({ message: 'invalid user name or password (6)' },
                                 { host: '10.0.0.1', port: 8728, user: 'admin', tls: false });
    assert.equal(out.classified, true, 'the commonest login failure must not fall through');
    assert.match(out.reason, /Authentication failed/);
  });
});

test('sameEndpoint reads the string "false" as false, not as true', () => {
  // `!!('false')` is true. The truthiness form read "certificate checking is
  // off" from a record that says it is on, turning the strictest setting into
  // the laxest by coercion. It failed closed — the two sides disagreed and the
  // stored password was refused — but a security predicate that is wrong in a
  // safe direction is still wrong.
  const BASE = { host: '10.0.0.53', port: 8729, username: 'mikrodash', tls: true };

  assert.equal(Routers.sameEndpoint({ ...BASE, tlsInsecure: 'false' },
                                    { ...BASE, tlsInsecure: false }), true,
    'the string "false" and boolean false mean the same thing');
  assert.equal(Routers.sameEndpoint({ ...BASE, tlsInsecure: 'false' },
                                    { ...BASE }), true,
    'and so does absent');
  // The distinction that must survive: "true" in either form is still a match
  // only against another true, never against false.
  assert.equal(Routers.sameEndpoint({ ...BASE, tlsInsecure: 'true' },
                                    { ...BASE, tlsInsecure: false }), false,
    'accepting a forged certificate is still a different endpoint');
  assert.equal(Routers.sameEndpoint({ ...BASE, tlsInsecure: 'true' },
                                    { ...BASE, tlsInsecure: true }), true);
});

// ── The WAN address is withheld by EVERY path that serves a router record ────
//
// geo.auto.ip is a WAN address. /api/localcc withholds it from anyone without
// system:settings, and so does the socket payload, and so does the stats
// payload. GET /api/routers served the same Routers.getPublic() records with no
// strip at all — so a `router:read` grant, including the readonly tier this rule
// exists to exclude, could read the address straight out of the JSON.
//
// The strip was a local const inside _routersForSocket. Three copies of one
// disclosure rule, and the copy that mattered was the one nobody wrote. It is
// now a single module-level _stripWanIp.
//
// Found by the Go/TypeScript port diffing each endpoint's payload against its
// own on the same /data with the same session. No test on either side could have
// found it: a round trip through one implementation agrees with itself whatever
// it disclosed.
describe('geo.auto.ip is withheld from anyone without system:settings', () => {
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');

  // Extract the helper and run it for real. A scan alone would pin the wiring
  // while saying nothing about whether the strip actually removes the field, and
  // this rule is one where "it is called" and "it works" are separate claims.
  const at = src.indexOf('function _stripWanIp(');
  assert.ok(at > 0, '_stripWanIp moved or was renamed');
  const end = src.indexOf('\n}\n', at);
  assert.ok(end > at, 'could not delimit _stripWanIp');
  const vm = require('node:vm');
  const ctx = { module: {} };
  vm.createContext(ctx);
  vm.runInContext(src.slice(at, end + 3) + '\nmodule.exports = _stripWanIp;', ctx);
  const stripWanIp = ctx.module.exports;

  const LOCATED = {
    id: 'r-1', label: 'edge',
    geo: { place: { name: 'Berlin', cc: 'DE' },
           auto:  { name: 'Brilon', cc: 'DE', lat: 51.39, lon: 8.56, ip: '203.0.113.7' } },
  };

  test('the WAN address is removed', () => {
    const out = stripWanIp(LOCATED);
    assert.equal(out.geo.auto.ip, undefined);
  });

  test('everything else about the location survives', () => {
    // The believability twin. A strip that returned {} would also pass the test
    // above, and would break the map for every operator instead of protecting
    // one field.
    const out = stripWanIp(LOCATED);
    assert.equal(out.geo.auto.name, 'Brilon');
    assert.equal(out.geo.auto.lat, 51.39);
    assert.deepEqual(out.geo.place, LOCATED.geo.place);
    assert.equal(out.label, 'edge');
  });

  test('the caller\'s record is not mutated', () => {
    // getPublic() records are served to several sockets in a row. A strip that
    // deleted in place would blank the field for the NEXT caller too — including
    // one who may see it — which reads as the WAN address randomly vanishing.
    const input = JSON.parse(JSON.stringify(LOCATED));
    stripWanIp(input);
    assert.equal(input.geo.auto.ip, '203.0.113.7');
  });

  test('a router with no location passes through untouched', () => {
    for (const r of [{ id: 'a' }, { id: 'b', geo: {} }, { id: 'c', geo: { auto: {} } }]) {
      assert.deepEqual(stripWanIp(r), r);
    }
    assert.equal(stripWanIp(null), null);
  });

  test('BOTH paths that serve getPublic() records apply it', () => {
    // The rule that was actually broken. Not "the helper exists" — it did, as a
    // local — but that every path serving these records reaches it. A future
    // route that serves getPublic() and forgets this is the same bug again, so
    // the ledger is over the call sites rather than over one route.
    const route = src.indexOf("app.get('/api/routers'");
    assert.ok(route > 0, 'GET /api/routers moved or was renamed');
    const block = src.slice(route, src.indexOf('});', route));
    assert.match(block, /_stripWanIp/,
      'GET /api/routers serves getPublic() records and must strip the WAN address');
    assert.match(block, /Rbac\.can\(req\.authSession, 'system:settings'\)/,
      'and it must be gated on system:settings, the same permission /api/localcc uses');

    const sock = src.indexOf('function _routersForSocket(');
    assert.ok(sock > 0, '_routersForSocket moved or was renamed');
    assert.match(src.slice(sock, src.indexOf('\n}\n', sock)), /_stripWanIp/,
      'the socket payload must go through the shared helper, not a private copy');
  });

  test('the strip is not defined twice', () => {
    // Three copies is what let one of them miss the rule. Re-inlining it would
    // pass every test above while re-opening the exact failure mode.
    const inlined = src.match(/const \{ ip, \.\.\.auto \} = /g) || [];
    assert.equal(inlined.length, 1,
      'the WAN-address strip must have exactly one implementation; found ' + inlined.length);
  });
});
