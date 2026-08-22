const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');

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
  assert.equal(directives.upgradeInsecureRequests, null);
  assert.ok(directives.connectSrc.includes("'self'"));
  const allSrcs = Object.values(directives).flat().filter(s => typeof s === 'string');
  assert.ok(!allSrcs.some(s => s === 'cdn.jsdelivr.net' || s.endsWith('.cdn.jsdelivr.net')), 'CSP must not allow jsdelivr CDN');
  assert.ok(!allSrcs.some(s => s === 'fonts.googleapis.com' || s.endsWith('.fonts.googleapis.com')), 'CSP must not allow Google Fonts');
});

test('computeHealthStatus requires startup, connectivity, and fresh critical collectors', () => {
  const { computeHealthStatus } = require('../src/health');
  const now = 1_000_000;

  const ready = computeHealthStatus({
    startupReady: true,
    rosConnected: true,
    now,
    state: {
      lastTrafficTs: now - 1000,
      lastSystemTs: now - 1000,
      lastIfStatusTs: now - 1000,
    },
    requiredCollectors: ['traffic', 'system', 'ifstatus'],
  });
  assert.equal(ready.ok, true);
  assert.equal(ready.statusCode, 200);
  assert.deepEqual(ready.stale, []);

  const staleTraffic = computeHealthStatus({
    startupReady: true,
    rosConnected: true,
    now,
    state: {
      lastTrafficTs: now - 30000,
      lastSystemTs: now - 1000,
      lastIfStatusTs: now - 1000,
    },
    requiredCollectors: ['traffic', 'system', 'ifstatus'],
  });
  assert.equal(staleTraffic.ok, false);
  assert.equal(staleTraffic.statusCode, 503);
  assert.deepEqual(staleTraffic.stale, ['traffic']);

  const idle = computeHealthStatus({
    startupReady: true,
    rosConnected: true,
    now,
    state: { lastTrafficTs: now - 1000, lastSystemTs: 0, lastIfStatusTs: 0 },
  });
  assert.equal(idle.ok, true, 'intentionally suspended viewer collectors are optional by default');

  const booting = computeHealthStatus({
    startupReady: false,
    rosConnected: true,
    now,
    state: {},
  });
  assert.equal(booting.ok, false);
  assert.equal(booting.statusCode, 503);

  const disconnected = computeHealthStatus({
    startupReady: true,
    rosConnected: false,
    now,
    state: {},
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

test('all node-routeros compatibility patches are required at startup', () => {
  const { PATCH_MARKERS, resolveDistPath, hasExactPatchMarker } = require('../src/routeros/patchVerification');
  assert.deepEqual(PATCH_MARKERS, [
    'MIKRODASH_PATCHED_EMPTY_REPLY',
    'MIKRODASH_PATCHED_EMPTY_NO_CLOSE',
    'MIKRODASH_PATCHED_UNREGISTEREDTAG',
    'MIKRODASH_PATCHED_RAW_BYTES',
    'MIKRODASH_PATCHED_MULTI_BLOCK',
    'MIKRODASH_PATCHED_MULTI_BLOCK_V2',
    'MIKRODASH_PATCHED_UTF8_ENCODE',
  ]);
  assert.equal(resolveDistPath('MIKRODASH_PATCHED_MULTI_BLOCK_V2'), 'Channel.js');
  assert.equal(hasExactPatchMarker('// MIKRODASH_PATCHED_MULTI_BLOCK_V2\n',
    'MIKRODASH_PATCHED_MULTI_BLOCK'), false, 'V2 cannot satisfy the V1 marker');
  assert.equal(hasExactPatchMarker('if (this.streaming) break; // MIKRODASH_PATCHED_MULTI_BLOCK_V2',
    'MIKRODASH_PATCHED_MULTI_BLOCK_V2'), true, 'inline end-of-line markers are valid');
  assert.equal(hasExactPatchMarker('xMIKRODASH_PATCHED_MULTI_BLOCK',
    'MIKRODASH_PATCHED_MULTI_BLOCK'), false, 'an identifier prefix is not a token boundary');
  assert.equal(hasExactPatchMarker('MIKRODASH_PATCHED_MULTI_BLOCKx',
    'MIKRODASH_PATCHED_MULTI_BLOCK'), false, 'an identifier suffix is not a token boundary');
});

test('patch verification fails when only MULTI_BLOCK_V2 is present', () => {
  const { verifyRouterOSPatchMarkers } = require('../src/routeros/patchVerification');
  assert.throws(() => verifyRouterOSPatchMarkers({
    patchMarkers: ['MIKRODASH_PATCHED_MULTI_BLOCK'],
    readFileSync: () => '// MIKRODASH_PATCHED_MULTI_BLOCK_V2\n',
    log: { error() {} },
  }), /MULTI_BLOCK.*not found/i);
});

test('the currently installed patched node-routeros passes the runtime verifier', () => {
  const { verifyRouterOSPatchMarkers } = require('../src/routeros/patchVerification');
  assert.doesNotThrow(() => verifyRouterOSPatchMarkers({ readFileSync: fs.readFileSync }));
});

test('Docker copies the shared patch verifier before running the dependency patch', () => {
  const dockerfile = fs.readFileSync(path.join(__dirname, '..', 'Dockerfile'), 'utf8');
  const verifierCopy = dockerfile.indexOf('COPY src/routeros/patchVerification.js ./src/routeros/patchVerification.js');
  const patchCopy = dockerfile.indexOf('COPY patch-routeros.js ./');
  const patchRun = dockerfile.indexOf('RUN node patch-routeros.js');
  const fullCopy = dockerfile.indexOf('COPY . .');
  assert.ok(verifierCopy >= 0, 'the verifier is present in the cached dependency layer');
  assert.ok(verifierCopy < patchCopy && patchCopy < patchRun,
    'both patch files exist before the patch script runs');
  assert.ok(patchRun < fullCopy, 'the full source tree is not copied early and dependency caching is retained');
});

test('the dependency patch command exits nonzero when required files are absent', () => {
  const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-patch-fail-'));
  try {
    fs.mkdirSync(path.join(temp, 'src', 'routeros'), { recursive: true });
    fs.mkdirSync(path.join(temp, 'node_modules', 'node-routeros', 'dist'), { recursive: true });
    fs.copyFileSync(path.join(__dirname, '..', 'patch-routeros.js'), path.join(temp, 'patch-routeros.js'));
    fs.copyFileSync(path.join(__dirname, '..', 'src', 'routeros', 'patchVerification.js'),
      path.join(temp, 'src', 'routeros', 'patchVerification.js'));
    const result = spawnSync(process.execPath, ['patch-routeros.js'], { cwd: temp, encoding: 'utf8' });
    assert.notEqual(result.status, 0, result.stdout + result.stderr);
    assert.match(result.stdout + result.stderr, /FAILED.*refusing to continue/i);
  } finally {
    fs.rmSync(temp, { recursive: true, force: true });
  }
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
    engine: { clientsCount: 1 },
    sockets: { adapter: { rooms: new Map() } },
    // Records chained room emits too: since issue #108 the full conn:update
    // payload is scoped to page-connections + dash-card-connections, while only
    // the sidebar count stays router-wide.
    to(room) {
      const rec = { to() { return rec; }, emit(event, payload) { emitted.push({ event, payload, room }); } };
      return rec;
    },
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

  // One emit: the full payload, scoped to the Connections page and its dashboard
  // card. Issue #108 also sent a router-wide conn:count for the sidebar badge;
  // the badge was removed, and the count went with it — nothing consumed it, and
  // the Connections page reads its own total from this payload.
  // Asserted on the event by name rather than by position.
  const full = emitted.find(e => e.event === 'conn:update');
  assert.ok(!emitted.some(e => e.event === 'conn:count'),
    'conn:count has no consumer — it must not be emitted on every tick');

  assert.ok(full, 'the full payload must still be emitted');
  assert.equal(full.payload.total, 3);
  assert.equal(full.payload.processed, 2);
  assert.equal(full.payload.processingCapped, true);
});

