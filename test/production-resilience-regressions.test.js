const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');

const ROS = require('../src/routeros/client');
const ConnectionsCollector = require('../src/collectors/connections');

test('frontend assets are self-hosted and avoid inline script handlers', () => {
  const html = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');
  const app = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');
  const tabler = fs.readFileSync(path.join(__dirname, '..', 'public', 'vendor', 'tabler.min.css'), 'utf8');

  assert.doesNotMatch(html, /https:\/\/cdn\.jsdelivr\.net/);
  assert.doesNotMatch(html, /https:\/\/unpkg\.com/);
  assert.doesNotMatch(html, /https:\/\/fonts\.googleapis\.com/);
  assert.doesNotMatch(app, /https:\/\/cdn\.jsdelivr\.net/);
  assert.doesNotMatch(html, /\sonerror=/i);
  assert.doesNotMatch(html, /src="logo\.png"/);
  assert.match(html, /<img src="\/logo\.png"/);
  assert.doesNotMatch(tabler, /sourceMappingURL=tabler\.min\.css\.map/);
});

test('buildHelmetOptions uses a self-hosted CSP policy', () => {
  const { buildHelmetOptions } = require('../src/security/helmetOptions');
  const opts = buildHelmetOptions();
  const directives = opts.contentSecurityPolicy.directives;

  assert.deepEqual(directives.defaultSrc, ["'self'"]);
  assert.deepEqual(directives.scriptSrc, ["'self'"]);
  assert.deepEqual(directives.fontSrc, ["'self'"]);
  assert.ok(directives.connectSrc.includes("'self'"));
  assert.ok(!JSON.stringify(directives).includes('cdn.jsdelivr.net'));
  assert.ok(!JSON.stringify(directives).includes('fonts.googleapis.com'));
});

test('computeHealthStatus reports readiness from startup and connectivity, not traffic freshness', () => {
  const { computeHealthStatus } = require('../src/health');

  // state is passed intentionally — proves traffic freshness doesn't influence health
  const ready = computeHealthStatus({
    startupReady: true,
    rosConnected: true,
    state: { lastTrafficTs: 0 },
  });
  assert.equal(ready.ok, true);
  assert.equal(ready.statusCode, 200);

  const booting = computeHealthStatus({
    startupReady: false,
    rosConnected: true,
    state: { lastTrafficTs: Date.now() },
  });
  assert.equal(booting.ok, false);
  assert.equal(booting.statusCode, 503);

  const disconnected = computeHealthStatus({
    startupReady: true,
    rosConnected: false,
    state: { lastTrafficTs: Date.now() },
  });
  assert.equal(disconnected.ok, false);
  assert.equal(disconnected.statusCode, 503);
});

test('scheduleForcedShutdownTimer unreferences the fallback timer', () => {
  const { scheduleForcedShutdownTimer } = require('../src/shutdown');
  let callback = null;
  let delay = null;
  let unrefCalls = 0;

  const timer = scheduleForcedShutdownTimer(() => {}, 5000, (fn, ms) => {
    callback = fn;
    delay = ms;
    return {
      unref() {
        unrefCalls++;
      },
    };
  });

  assert.equal(typeof callback, 'function');
  assert.equal(delay, 5000);
  assert.equal(unrefCalls, 1);
  assert.equal(typeof timer.unref, 'function');
});

test('verifyRouterOSPatchMarkers throws when a patch file cannot be read', () => {
  const { verifyRouterOSPatchMarkers } = require('../src/routeros/patchVerification');

  assert.throws(
    () => verifyRouterOSPatchMarkers({
      patchMarkers: ['MIKRODASH_PATCHED_EMPTY_REPLY'],
      resolveDistPath(marker) {
        return marker.includes('EMPTY') ? 'Channel.js' : path.join('connector', 'Receiver.js');
      },
      readFileSync() {
        const err = new Error('ENOENT: no such file or directory');
        err.code = 'ENOENT';
        throw err;
      },
    }),
    /Could not verify patch .*ENOENT/i
  );
});

test('ROS write timeout closes the active connection before rejecting', async () => {
  const ros = new ROS({});
  let closeCalls = 0;
  ros.connected = true;
  ros.conn = {
    write() {
      return new Promise(() => {});
    },
    close() {
      closeCalls++;
    },
  };

  await assert.rejects(
    ros.write('/slow/test', [], 10),
    /write timeout/i
  );
  assert.equal(closeCalls, 1);
});

test('connections collector emits processed count and processingCapped when truncating work', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    write: async () => ([
      { '.id': '*1', 'src-address': '192.168.1.10', 'dst-address': '1.1.1.1', protocol: 'tcp', 'dst-port': '443' },
      { '.id': '*2', 'src-address': '192.168.1.11', 'dst-address': '8.8.8.8', protocol: 'udp', 'dst-port': '53' },
      { '.id': '*3', 'src-address': '192.168.1.12', 'dst-address': '9.9.9.9', protocol: 'tcp', 'dst-port': '80' },
    ]),
    on() {},
  };
  const io = {
    emit(event, payload) {
      emitted.push({ event, payload });
    },
  };
  const collector = new ConnectionsCollector({
    ros,
    io,
    pollMs: 1000,
    topN: 5,
    maxConns: 2,
    state: {},
    dhcpNetworks: { getLanCidrs: () => ['192.168.1.0/24'] },
    dhcpLeases: { getNameByIP: () => null, getNameByMAC: () => null },
    arp: { getByIP: () => null },
  });

  await collector.tick();

  assert.equal(emitted.length, 1);
  assert.equal(emitted[0].event, 'conn:update');
  assert.equal(emitted[0].payload.total, 3);
  assert.equal(emitted[0].payload.processed, 2);
  assert.equal(emitted[0].payload.processingCapped, true);
});
