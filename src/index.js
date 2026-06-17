require('dotenv').config();

// ── Timestamped console output ────────────────────────────────────────────────
// Prepend a timestamp to every log line so Docker logs are readable without
// needing `docker logs --timestamps`.
(function _patchConsole() {
  const ts = () => {
    const d = new Date();
    const pad = (n) => String(n).padStart(2, '0');
    return d.getFullYear() + '-' + pad(d.getMonth()+1) + '-' + pad(d.getDate()) +
           ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
  };
  for (const level of ['log', 'info', 'warn', 'error']) {
    const orig = console[level].bind(console);
    console[level] = (...args) => orig(`[${ts()}]`, ...args);
  }
})();

const Settings = require('./settings');
const Routers  = require('./routers');

const fs   = require('fs');
const path = require('path');
const express = require('express');
const http    = require('http');
const helmet  = require('helmet');
const rateLimit = require('express-rate-limit');
const { Server } = require('socket.io');
const { version: APP_VERSION } = require('../package.json');
const { buildHelmetOptions } = require('./security/helmetOptions');
const { computeHealthStatus } = require('./health');
const { verifyRouterOSPatchMarkers } = require('./routeros/patchVerification');
const { scheduleForcedShutdownTimer } = require('./shutdown');

try {
  verifyRouterOSPatchMarkers({ readFileSync: fs.readFileSync });
} catch (_error) {
  console.error('[MikroDash] Run: node patch-routeros.js');
  process.exit(1);
}

let geoip = null;
try { geoip = require('geoip-lite'); } catch (_) {}

const ROS                  = require('./routeros/client');

const { isValidIp }        = require('./util/ip');
const { fetchInterfaces }  = require('./collectors/interfaces');
const TrafficCollector     = require('./collectors/traffic');
const DhcpLeasesCollector  = require('./collectors/dhcpLeases');
const DhcpNetworksCollector= require('./collectors/dhcpNetworks');
const ArpCollector         = require('./collectors/arp');
const ConnectionsCollector = require('./collectors/connections');
const TopTalkersCollector  = require('./collectors/talkers');
const LogsCollector        = require('./collectors/logs');
const SystemCollector      = require('./collectors/system');
const WirelessCollector    = require('./collectors/wireless');
const VpnCollector         = require('./collectors/vpn');
const FirewallCollector    = require('./collectors/firewall');
const InterfaceStatusCollector = require('./collectors/interfaceStatus');
const PingCollector         = require('./collectors/ping');
const BandwidthCollector    = require('./collectors/bandwidth');
const RoutingCollector      = require('./collectors/routing');
const NetwatchCollector     = require('./collectors/netwatch');
const alerter               = require('./alerter');
const notifier              = require('./notifier');
const alertSessions         = require('./alertSessions');
const overviewSessions      = require('./overviewSessions');
const SessionStore          = require('./auth/sessionStore');
const Users                 = require('./users');
const db                    = require('./db');
const dbWriter              = require('./db-writer');

const compression = require('compression');
const app = express();

const TRUSTED_PROXY = process.env.TRUSTED_PROXY;
if (TRUSTED_PROXY) app.set('trust proxy', TRUSTED_PROXY);

const server = http.createServer(app);
const MAX_SOCKETS = parseInt(process.env.MAX_SOCKETS || '50', 10);
const io = new Server(server, {
  maxHttpBufferSize: 1e6,
  connectTimeout: 10000,
  pingInterval: 10000,
  pingTimeout:  5000,
  perMessageDeflate: { threshold: 128, zlibDeflateOptions: { level: 1 } },
});

// Scoped IO wrapper — emits only to sockets watching a specific router.
// Collectors receive this instead of raw `io` so all their broadcasts are
// automatically scoped to the correct router room with no internal changes.
function buildRouterIo(routerId) {
  const room = 'router-' + routerId;
  return {
    emit(event, data) {
      io.to(room).emit(event, data);
      if (event === 'ping:update' && data && typeof data.loss === 'number') {
        dbWriter.recordPing(routerId, data.target, data.rtt != null ? data.rtt : null, data.loss, data.ts);
      }
      alerter.evaluateForRouter(routerId, event, data);
    },
    to(subRoom) {
      return {
        emit(event, data) { io.to(room + '-' + subRoom).emit(event, data); },
        to(r2)            { return { emit(e, d) { io.to(room + '-' + subRoom).to(room + '-' + r2).emit(e, d); } }; },
      };
    },
    // Collectors may call io.on('connection', ...) to restart streams on reconnect.
    on(event, handler)   { io.on(event, handler); },
    engine: { get clientsCount() { return io.sockets.adapter.rooms.get(room)?.size || 0; } },
    // Collectors that check room sizes (e.g. connections) use io.sockets.adapter.rooms.get(subRoom).
    // Transparently scope the lookup to this router's rooms so they get the right count.
    sockets: {
      adapter: {
        rooms: {
          get(subRoom) { return io.sockets.adapter.rooms.get(room + '-' + subRoom); },
        },
      },
    },
  };
}

// Three-mode auth dispatcher. Reads authMode from settings on every request
// so changes take effect immediately without a restart.
const authLimiter = rateLimit({ windowMs: 60_000, max: 100, standardHeaders: true, legacyHeaders: false });
const loginLimiter = rateLimit({ windowMs: 60_000, max: 10, standardHeaders: true, legacyHeaders: false });
const setupLimiter = rateLimit({ windowMs: 60_000, max: 5, standardHeaders: true, legacyHeaders: false });

// Public paths that always pass through in modern mode
const _MODERN_PUBLIC = new Set([
  '/login', '/login.html', '/login.js', '/preflight.js',
  '/healthz', '/logo.png', '/favicon.ico',
  '/api/auth/status', '/api/auth/login', '/api/users/setup',
]);

// ── Session resolution (single source for all cookie→session lookups) ───────────
// Resolves the session cookie on a request/socket-request to a *live* auth view:
// role and allowedRouterIds are re-read from the current user record on every
// call, so role changes and permission revocations take effect immediately, and
// a deleted user's session is invalidated (and pruned). Returns null if there is
// no valid session or the backing user no longer exists.
function _sessionFromReq(req) {
  const token   = SessionStore.parseCookieHeader(req.headers.cookie || '')['mikrodash_sid'];
  const session = token ? SessionStore.getSession(token) : null;
  if (!session) return null;
  const user = Users.getUserSync(session.userId);
  if (!user) {
    // User was deleted — kill the orphaned session so the cookie stops working.
    SessionStore.deleteSession(token);
    return null;
  }
  // Overlay live role/perms onto the stored session (which still owns activeRouterId).
  // Mutate in place so persisted preferences and the live view stay consistent.
  session.role             = user.role;
  session.username         = user.username;
  session.allowedRouterIds = Array.isArray(user.allowedRouterIds) ? user.allowedRouterIds : [];
  return session;
}

function _authMiddleware(req, res, next) {
  const mode = Settings.load().authMode || 'modern';
  if (mode === 'none') return next();
  return _modernAuthMiddleware(req, res, next);
}

function _modernAuthMiddleware(req, res, next) {
  if (_MODERN_PUBLIC.has(req.path) || req.path.startsWith('/vendor/')) return next();
  const session = _sessionFromReq(req);
  if (session) { req.authSession = session; return next(); }
  if (req.path.startsWith('/api/')) return res.status(401).json({ ok: false, error: 'Not authenticated' });
  const next_url = encodeURIComponent(req.originalUrl);
  return res.redirect(302, `/login?next=${next_url}`);
}

// Role-based access control only exists in modern auth (it has a user model with
// roles). In 'none' mode there is no identity, so every request is implicitly an
// admin — this is by design. RBAC is enforced whenever an identity with a role is present.
function _requireAdmin(req, res, next) {
  if ((Settings.load().authMode || 'modern') !== 'modern') return next();
  if (!req.authSession || req.authSession.role !== 'admin') {
    return res.status(403).json({ ok: false, error: 'Admin access required' });
  }
  next();
}

// Reject a report/export request whose ?routerId is outside the caller's allowed
// set. Live socket data is already per-router scoped (_resolveRouterId); the
// historical-data routes accept an arbitrary routerId, so a restricted admin must
// be held to the same boundary here. No-op in none mode (no per-user perms).
function _scopeRouterId(req, res, next) {
  if ((Settings.load().authMode || 'modern') !== 'modern') return next();
  const allowed = req.authSession && req.authSession.allowedRouterIds;
  if (Array.isArray(allowed) && allowed.length > 0) {
    const rid = String(req.query.routerId || '');
    if (rid && !allowed.includes(rid)) {
      return res.status(403).json({ ok: false, error: 'Router not permitted' });
    }
  }
  next();
}

app.use(helmet(buildHelmetOptions()));
app.use(compression());
app.use((req, res, next) => {
  if (req.path === '/healthz') return next();
  authLimiter(req, res, (err) => { if (err) return next(err); _authMiddleware(req, res, next); });
});
// WebSocket upgrade requests bypass Express middleware so don't have req.ip/req.app.
// authLimiter cannot be used here; auth-only is applied instead. Rate limiting for
// the preceding polling handshake is covered by the app.use() handler above.
io.engine.use((req, res, next) => { // codeql[js/missing-rate-limiting]
  if ((Settings.load().authMode || 'modern') === 'none') return next();
  const session = _sessionFromReq(req);
  if (!session) { res.statusCode = 401; return res.end('Not authenticated'); }
  req._authSession = session; // accessible as socket.request._authSession in io.on('connection')
  next();
});

// Start session prune interval (no-op if already started)
SessionStore.startPruneInterval();

// Single shared sweep (one timer for the whole process, not one per socket) that
// re-validates every connected socket in modern auth. A socket whose session has
// expired or been revoked (user deleted) is told and disconnected; otherwise its
// cached auth view is refreshed so live role/permission changes take effect.
let _sessionSweepTimer = null;
function _startSessionSweep() {
  if (_sessionSweepTimer) return;
  _sessionSweepTimer = setInterval(() => {
    if ((Settings.load().authMode || 'modern') !== 'modern') return;
    for (const [, socket] of io.sockets.sockets) {
      const live = _sessionFromReq(socket.request);
      if (!live) {
        socket.emit('session:expired');
        socket.disconnect(true);
      } else {
        socket.request._authSession = live; // refresh live role/allowedRouterIds
      }
    }
  }, 60_000);
  if (_sessionSweepTimer.unref) _sessionSweepTimer.unref();
}
_startSessionSweep();

app.use('/vendor', express.static(path.join(__dirname, '..', 'public', 'vendor'), { maxAge: '7d' }));
app.use(express.static(path.join(__dirname, '..', 'public')));
app.use(express.json({ limit: '50kb' }));

// ── Router session pool ───────────────────────────────────────────────────────
// Each entry: { session, startupReady, collectorsStarted, rosConnected, idleTimer, routerIo }
// Modern-auth users independently connect to their chosen router; basic/none
// auth always uses settings.activeRouterId (a single shared entry).
const _routerSessions = new Map();
let _noRouterMode = false; // true when no router is configured yet

// Helpers to access the global-default entry (used by REST routes that don't have a per-user context).
function _globalEntry() {
  const id = Settings.load().activeRouterId;
  return id ? (_routerSessions.get(id) || null) : null;
}
function _globalSession() { return _globalEntry()?.session || null; }

// Router ids the main pool is actively serving. alertSessions must skip these so
// connectivity/alerts for a pool-served router are tracked by exactly one owner.
function _poolOwnedIds() { return new Set(_routerSessions.keys()); }

// Re-sync the alertSessions pool, always excluding routers the main pool owns.
// Call after any change to _routerSessions (create/teardown) or the router list.
function _syncAlertSessions() {
  alertSessions.syncSessions(Routers.loadAll(), Settings.load().activeRouterId || '', _poolOwnedIds());
}

function _syncOverviewSessions() {
  overviewSessions.syncSessions(Routers.loadAll(), _poolOwnedIds());
}

function _freshState() {
  return {
    lastTrafficTs:0,  lastTrafficErr:null,
    lastConnsTs:0,    lastConnsErr:null,
    lastNetworksTs:0,
    lastLeasesTs:0,
    lastArpTs:0,
    lastTalkersTs:0,  lastTalkersErr:null,
    lastLogsTs:0,     lastLogsErr:null,
    lastSystemTs:0,   lastSystemErr:null,
    lastWirelessTs:0, lastWirelessErr:null,
    lastVpnTs:0,      lastVpnErr:null,
    lastFirewallTs:0, lastFirewallErr:null,
    lastIfStatusTs:0,
    lastPingTs:0,
    lastRoutingTs:0,  lastRoutingErr:null,
    lastBandwidthTs:0, lastBandwidthErr:null,
    lastNetwatchTs:0, lastNetwatchErr:null,
  };
}

function buildSession(routerCfg, routerIo) {
  const _cfg   = Settings.load();
  const state  = _freshState();

  // When TLS is enabled, pass an options object rather than a boolean so we can
  // set rejectUnauthorized. node-routeros passes this directly to tls.connect().
  const tlsOpts = routerCfg.tls
    ? { rejectUnauthorized: !routerCfg.tlsInsecure }
    : false;

  const ros = new ROS({
    host:           routerCfg.host,
    port:           routerCfg.port,
    tls:            tlsOpts,
    username:       routerCfg.username,
    password:       routerCfg.password,
    debug:          Settings.load().rosDebug,
    writeTimeoutMs: parseInt(process.env.ROS_WRITE_TIMEOUT_MS || '30000', 10),
  });
  ros.routerLabel = routerCfg.label || routerCfg.host;

  const DEFAULT_IF  = routerCfg.defaultIf  || _cfg.defaultIf  || 'ether1';
  const PING_TARGET = routerCfg.pingTarget  || _cfg.pingTarget || '1.1.1.1';

  // Validate before values reach the RouterOS API
  if (!/^[A-Za-z0-9_./-]{1,128}$/.test(DEFAULT_IF)) {
    throw new Error(`[MikroDash] Invalid defaultIf value: "${DEFAULT_IF}"`);
  }
  if (!isValidIp(PING_TARGET)) {
    throw new Error(`[MikroDash] Invalid pingTarget value: "${PING_TARGET}" — must be a valid IP address`);
  }
  const HISTORY_MINUTES = _cfg.historyMinutes;

  // Shared geo/org lookup cache — passed to both ConnectionsCollector and
  // BandwidthCollector so geoip.lookup() and lookupOrg() are called at most
  // once per unique IP per session rather than once per collector per tick.
  const geoOrgCache = { geo: new Map(), org: new Map() };

  // Push-fed snapshot cache — ConnectionsCollector.deposit() writes each
  // completed stream batch here; BandwidthCollector reads via latestWithTs().
  // Partial-result detection lives in ConnectionsCollector._onBatchComplete().
  const connTableCache = {
    _rows: null, _ts: 0,
    deposit(rows, ts) { this._rows = rows; this._ts = ts; },
    latestWithTs()    { return { rows: this._rows || [], ts: this._ts }; },
    invalidate()      { this._rows = null; this._ts = 0; },
  };

  const dhcpLeases   = new DhcpLeasesCollector ({ros, io:routerIo, state});
  const arp          = new ArpCollector         ({ros,              pollMs:_cfg.pollArp,       state});
  const dhcpNetworks = new DhcpNetworksCollector({ros, io:routerIo, pollMs:_cfg.pollDhcp,      dhcpLeases, state, wanIface:DEFAULT_IF});
  const traffic      = new TrafficCollector     ({ros, io:routerIo, defaultIf:DEFAULT_IF, historyMinutes:HISTORY_MINUTES, pollMs:1000, state,
    onSample: (ifName, rxMbps, txMbps, ts) => dbWriter.recordTraffic(routerCfg.id, ifName, rxMbps, txMbps, ts)});
  // Backfill ring buffer from SQLite so the chart has history on first browser connect
  // (covers both server restarts and sessions where no browser was open during recording).
  const _histFromTs = Date.now() - HISTORY_MINUTES * 60 * 1000;
  const _histRows   = db.queryTrafficSamples(routerCfg.id, DEFAULT_IF, _histFromTs, Date.now(), traffic.maxPoints);
  if (_histRows.length) traffic.preloadHistory(DEFAULT_IF, _histRows);
  const conns        = new ConnectionsCollector ({ros, io:routerIo, pollMs:_cfg.pollConns,    topN:_cfg.topN, maxConns:_cfg.maxConns, dhcpNetworks, dhcpLeases, arp, state, connTableCache, geoOrgCache, streamMode:_cfg.streamConns});
  const talkers      = new TopTalkersCollector  ({ros, io:routerIo, pollMs:_cfg.pollTalkers,  state, topN:_cfg.topTalkersN, streamMode:_cfg.streamTalkers});
  const logs         = new LogsCollector        ({ros, io:routerIo, state});
  const system       = new SystemCollector      ({ros, io:routerIo, pollMs:_cfg.pollSystem,   state, streamMode:_cfg.streamSystem});
  const wireless     = new WirelessCollector    ({ros, io:routerIo, pollMs:_cfg.pollWireless, state, dhcpLeases, arp});
  const vpn          = new VpnCollector         ({ros, io:routerIo, pollMs:_cfg.pollVpn,      state, rid:routerCfg.id});
  const firewall     = new FirewallCollector    ({ros, io:routerIo, pollMs:_cfg.pollFirewall,  state, topN:_cfg.firewallTopN});
  const ifStatus     = new InterfaceStatusCollector({ros, io:routerIo, pollMs:_cfg.pollIfstatus, metaPollMs:_cfg.pollIfaces, state, streamMode:_cfg.streamIfrates});
  const ping         = new PingCollector        ({ros, io:routerIo, pollMs:_cfg.pollPing,     state, target:PING_TARGET, streamMode:_cfg.streamPing});
  const bandwidth    = new BandwidthCollector   ({ros, io:routerIo, pollMs:_cfg.pollBandwidth, dhcpNetworks, dhcpLeases, arp, ifStatus, state, connTableCache, geoOrgCache});
  const routing      = new RoutingCollector     ({ros, io:routerIo, pollMs:_cfg.pollRouting,  state});
  const netwatch     = new NetwatchCollector    ({ros, io:routerIo,                           state});

  const allCollectors = [traffic, dhcpLeases, dhcpNetworks, arp, conns, talkers, logs, system, wireless, vpn, firewall, ifStatus, ping, bandwidth, routing, netwatch];

  return { ros, state, connTableCache, DEFAULT_IF, HISTORY_MINUTES,
           dhcpLeases, dhcpNetworks, arp, traffic, conns, talkers, logs, system,
           wireless, vpn, firewall, ifStatus, ping, bandwidth, routing, netwatch, allCollectors,
           routerId: routerCfg.id, cachedInterfaces: null };
}

// ── Session teardown ──────────────────────────────────────────────────────────
// Stop all collectors and the ROS connection. `entry` is the _routerSessions entry.
async function teardownSession(session, entry) {
  if (!session) return;
  const _tearLabel = (session.ros && session.ros.routerLabel) || 'router';
  console.log(`[${_tearLabel}] ── session torn down`);
  if (entry) { entry.startupReady = false; entry.collectorsStarted = false; }
  if (entry && entry._diagTimer) { clearInterval(entry._diagTimer); entry._diagTimer = null; }
  if (session._cancelDownTimer) session._cancelDownTimer();
  for (const c of session.allCollectors) {
    if (typeof c.stop === 'function') c.stop();
  }
  session.ros.stop();
  // Flush any open 1-minute traffic buckets before discarding the session
  if (session.routerId) dbWriter.flushTraffic(session.routerId);
  // Brief yield so in-flight async callbacks can settle before we replace the session
  await new Promise(r => setTimeout(r, 150));
}

const _serverStartTime = Date.now();
const STARTUP_GRACE_MS = 15000; // 15 s covers staggered collector startup

// Per-entry ros:status broadcast — scoped to the router's room.
// `router:status` (router reachability for the list UI) stays as io.emit (global).
function broadcastRosStatus(connected, reason, entry) {
  if (entry) entry.rosConnected = connected;
  const target = entry ? entry.routerIo : io;
  target.emit('ros:status', { connected, reason: reason || null });
}

function wireRosEvents(session, entry) {
  const { ros } = session;
  const host = ros.cfg.host;
  const port = ros.cfg.port || 8729;
  const user = ros.cfg.username;
  const tls  = ros.cfg.tls !== false;
  let _prevConnected  = null;  // null = never connected
  let _downTimer      = null;  // pending offline-declaration timer
  let _declaredOffline = false; // timer fired — badge is showing Offline

  session._cancelDownTimer = () => { if (_downTimer) { clearTimeout(_downTimer); _downTimer = null; } };

  function _emitRouterStatus(connected) {
    if (!session.routerId) return;
    const r     = Routers.getById(session.routerId);
    const label = (r && r.label) || host;

    if (connected) {
      // Cancel any pending offline timer and go Online immediately.
      session._cancelDownTimer();
      io.emit('router:status', { routerId: session.routerId, connected: true });
      // Record connected=1 only on a real transition into connected. A flapping
      // link can fire 'connected' repeatedly within the down-debounce window
      // (which suppresses the matching connected=0); writing a 1 each time would
      // inflate SUM(connected)/COUNT(*) uptime toward ~100% and hide the flapping.
      if (_prevConnected !== true) dbWriter.recordConnectivity(session.routerId, true);
      if (_declaredOffline) {
        // Recovery alert — only when we had previously declared this router offline.
        alerter.fireConnectivityAlert(session.routerId, label, true);
        _declaredOffline = false;
      }
      _prevConnected = true;
    } else {
      // Don't immediately flip to Offline — start (or continue) the debounce window.
      if (_downTimer) return; // already counting, don't reset
      if (_prevConnected === null) {
        // Never connected at all (startup failure): reflect immediately, no alert.
        io.emit('router:status', { routerId: session.routerId, connected: false });
        dbWriter.recordConnectivity(session.routerId, false);
        _prevConnected = false;
        return;
      }
      const threshMs = ((r && r.connDownThresholdSec !== undefined) ? r.connDownThresholdSec : 30) * 1000;
      if (threshMs <= 0) {
        // Threshold = 0 → react immediately (original behaviour).
        io.emit('router:status', { routerId: session.routerId, connected: false });
        dbWriter.recordConnectivity(session.routerId, false);
        if (_prevConnected !== false)
          alerter.fireConnectivityAlert(session.routerId, label, false);
        _prevConnected = false;
        return;
      }
      _downTimer = setTimeout(() => {
        _downTimer      = null;
        _declaredOffline = true;
        _prevConnected  = false;
        io.emit('router:status', { routerId: session.routerId, connected: false });
        dbWriter.recordConnectivity(session.routerId, false);
        alerter.fireConnectivityAlert(session.routerId, label, false);
      }, threshMs);
    }
  }

  ros.on('connected', () => {
    console.log(`[${ros.routerLabel}][ROS] ✓ connected to ${host}:${port} as "${user}" (${tls ? 'TLS' : 'plain'})`);
    session.cachedInterfaces = null; // invalidate on reconnect — interfaces may have changed
    session._ifacesFetch    = null;
    broadcastRosStatus(true, null, entry);
    _emitRouterStatus(true);
    // Restore page-aware streams for any pages still open after the reconnect.
    // Collector reconnect handlers (in constructors) fire before this listener
    // and call suspend() to clear state first.
    session.conns.resume();
    _updateFirewallStreams(session, entry);
    _updateRoutingStreams(session, entry);
    _updateWirelessStreams(session, entry);
    _updateVpnStreams(session, entry);
  });
  ros.on('close', () => {
    session.connTableCache.invalidate();
    console.log(`[${ros.routerLabel}][ROS] connection to ${host}:${port} closed`);
    broadcastRosStatus(false, 'RouterOS connection closed', entry);
    _emitRouterStatus(false);
  });
  ros.on('connectionError', (e) => {
    const msg = e && e.message ? e.message : String(e);
    let reason = msg;
    let hint   = '';
    if (/ECONNREFUSED/.test(msg)) {
      reason = `Connection refused — is RouterOS reachable at ${host}?`;
      hint   = `Check that the RouterOS API service is enabled: /ip service set api${tls?'-ssl':''} disabled=no`;
    } else if (/ETIMEDOUT/.test(msg) || /timed out/i.test(msg)) {
      reason = 'Connection timed out — check host and firewall rules';
      hint   = `Verify ${host}:${port} is reachable and not blocked by a firewall rule`;
    } else if (/ENOTFOUND/.test(msg) || /ENOENT/.test(msg)) {
      reason = `Host not found — check router host setting (${host})`;
      hint   = 'Ensure the hostname or IP address is correct and DNS is resolving';
    } else if (/ECONNRESET/.test(msg)) {
      reason = 'Connection reset by router';
      hint   = 'The router closed the connection unexpectedly — check RouterOS logs';
    } else if (/certificate/i.test(msg)) {
      reason = 'TLS certificate error — try enabling "Allow self-signed cert"';
      hint   = 'Set tlsInsecure=true in settings or use a valid certificate on the router';
    } else if (/authentication/i.test(msg) || /login/i.test(msg) || /invalid user/i.test(msg) || /wrong password/i.test(msg) || /username.*invalid|password.*invalid/i.test(msg) || (e && e.errno === 'CANTLOGIN')) {
      reason = 'Authentication failed — check username and password';
      hint   = `Confirm user "${user}" exists on the router and has API access: /user print`;
    } else if (/RosException/.test(msg) || (e && e.name === 'RosException')) {
      const errno = e && e.errno ? e.errno : '';
      if (tls) {
        reason = `TLS handshake failed — check that RouterOS api-ssl is enabled${errno ? ` [${errno}]` : ''}`;
        hint   = 'Run: /ip service set api-ssl disabled=no  — and verify the certificate is valid';
      } else {
        reason = `RouterOS API error${errno ? ` [${errno}]` : ''} — check that the API service is enabled and the user has API access`;
        hint   = `Run: /ip service set api disabled=no  — then confirm user "${user}" has API group permissions`;
      }
    }
    console.error(`[${ros.routerLabel}][ROS] ✗ ${reason}`);
    if (hint) console.error(`[${ros.routerLabel}][ROS]   → ${hint}`);
    if (e && e.errno) console.error(`[${ros.routerLabel}][ROS]   errno: ${e.errno}`);
    console.error(`[${ros.routerLabel}][ROS]   raw: ${msg}`);
    broadcastRosStatus(false, reason, entry);
    _emitRouterStatus(false);
  });
  ros.on('connected', () => startCollectors(session, entry));
}

async function startCollectors(session, entry) {
  if (entry.collectorsStarted) return;
  entry.collectorsStarted = true;
  const _delay = ms => new Promise(r => setTimeout(r, ms));
  try {
    console.log(`[${session.ros.routerLabel}] ── session started (v${APP_VERSION})`);
    // Group A — foundation collectors; awaits provide natural sequencing.
    session.wireless.start();
    await session.dhcpLeases.start();
    // start() does an initial synchronous fetch so networks/wanIp are
    // populated before sendInitialState broadcasts to connected sockets.
    await session.dhcpNetworks.start();
    await session.arp.start();
    // Group B — streaming collectors staggered 300 ms apart to avoid overwhelming
    // the RouterOS API handler thread pool. CHR/VM instances have very few handler
    // threads (typically 2-4); a burst of simultaneous stream-open commands can
    // exhaust them, forcing RouterOS to terminate the entire API session.
    session.traffic.start();
    await _delay(300);
    session.conns.start();   // starts fallback poll only — no stream at start()
    session.talkers.start();
    await _delay(300);
    session.logs.start();
    await _delay(300);
    // Set callback before start() so the first board-name tick never races past it
    session.system._onFirstBoardName = (boardName) => {
      const router = Routers.getById(session.routerId);
      if (router && (router.label === 'My Router' || router.label === router.host)) {
        Routers.updateLabel(session.routerId, boardName);
        // Broadcast updated router list to all clients
        _broadcastRoutersList();
      }
    };
    session.system.start();
    await _delay(300);
    await session.vpn.start();
    await session.firewall.start();
    await _delay(300);
    await session.ifStatus.start();
    session.ping.start();
    await _delay(300);
    session.bandwidth.start();
    await _delay(300);
    await session.routing.start();
    await _delay(300);
    await session.netwatch.start();

    entry.startupReady = true;
    console.log('[MikroDash] All collectors running');

    // If no sockets are in this router's room, suspend high-frequency streams.
    const routerRoom = io.sockets.adapter.rooms.get('router-' + session.routerId);
    if (!routerRoom || routerRoom.size === 0) _idleSuspend(session, entry);

    // Broadcast initial state to sockets watching this router.
    // On first startup there are none yet, so this is a no-op.
    // On a hot-swap the Socket.IO connections stay alive — existing browser
    // clients never receive a 'connection' event, so without this they would
    // not get the new router's data until they manually refreshed the page.
    for (const [, socket] of io.sockets.sockets) {
      if (socket.routerId !== session.routerId) continue;
      session.traffic.bindSocket(socket);
      sendInitialState(socket, entry).catch((e) => {
        console.error('[MikroDash] sendInitialState failed for socket', socket.id, ':', e && e.message ? e.message : e);
      });
    }
  } catch (e) {
    entry.startupReady = false;
    entry.collectorsStarted = false;
    console.error('[MikroDash] Collector startup error:', e && e.message ? e.message : e);
  }
}

// ── Hot-swap ──────────────────────────────────────────────────────────────────
let _switching = false;

async function switchRouter(newRouterId) {
  if (_switching) return { ok: false, error: 'Switch already in progress' };
  const router = Routers.getById(newRouterId);
  if (!router) return { ok: false, error: 'Router not found' };

  _switching = true;
  try {
    console.log(`[MikroDash] Switching to router: ${router.label} (${router.host})`);

    // Find old global-default entry before saving the new id
    const oldActiveId = Settings.load().activeRouterId;
    const oldEntry = oldActiveId ? _routerSessions.get(oldActiveId) : null;
    if (oldEntry) {
      broadcastRosStatus(false, `Switching to ${router.label}…`, oldEntry);
    }
    io.emit('router:switching', { routerId: newRouterId, label: router.label });

    // Save the new active router id
    Settings.save({ activeRouterId: newRouterId });

    // Tear down old session (may be null on first-ever activation from setup wizard)
    if (oldEntry) {
      if (oldEntry.idleTimer) { clearTimeout(oldEntry.idleTimer); oldEntry.idleTimer = null; }
      await teardownSession(oldEntry.session, oldEntry);
      _routerSessions.delete(oldActiveId);
    }
    _noRouterMode = false;

    // Move non-modern-auth sockets to the new router room
    for (const [, socket] of io.sockets.sockets) {
      if (!socket.request?._authSession) {
        if (socket.routerId && socket.routerId !== newRouterId) {
          socket.leave('router-' + socket.routerId);
          socket.routerId = newRouterId;
          socket.join('router-' + newRouterId);
        }
      }
    }

    // Build and start new session
    const newEntry = ensureRouterSession(newRouterId);
    return { ok: true };
  } finally {
    _switching = false;
  }
}

// ── Session pool helpers ──────────────────────────────────────────────────────

// Returns (creating if absent) the pool entry for the given router id.
function ensureRouterSession(routerId) {
  let entry = _routerSessions.get(routerId);
  if (entry) return entry;

  const router = Routers.getById(routerId);
  if (!router) return null;

  const routerIo = buildRouterIo(routerId);
  const session  = buildSession(router, routerIo);
  entry = { session, startupReady: false, collectorsStarted: false, rosConnected: false, idleTimer: null, routerIo };
  _routerSessions.set(routerId, entry);
  wireRosEvents(session, entry);
  session.ros.connectLoop();
  // This router is now pool-owned — drop any alertSessions session for it so
  // connectivity/alerts aren't tracked twice. (No-op for the global active router,
  // which alertSessions already excludes.)
  _syncAlertSessions();
  _syncOverviewSessions();
  return entry;
}

// Schedule idle teardown for a router after all its sockets disconnect.
// Cancelled if a new socket joins the router's room before the timer fires.
function scheduleIdleTeardown(routerId, delayMs = 60_000) {
  const entry = _routerSessions.get(routerId);
  if (!entry) return;
  if (entry.idleTimer) return; // already scheduled

  const cfg = Settings.load();
  // Never tear down the global default — it must stay available for new connections.
  if (cfg.activeRouterId === routerId) return;

  entry.idleTimer = setTimeout(async () => {
    entry.idleTimer = null;
    const room = io.sockets.adapter.rooms.get('router-' + routerId);
    if (room && room.size > 0) return; // sockets rejoined while timer was pending
    console.log(`[MikroDash] Idle teardown — router ${routerId}`);
    await teardownSession(entry.session, entry);
    _routerSessions.delete(routerId);
    alerter.dropEvaluator(routerId);
    // No longer pool-owned — let alertSessions resume status-only tracking.
    _syncAlertSessions();
    _syncOverviewSessions();
  }, delayMs);
}

// Resolve which router a connecting socket should watch.
function _resolveRouterId(socket) {
  const authSession = socket.request?._authSession;
  const cfg = Settings.load();
  if (authSession) {
    const allowed = authSession.allowedRouterIds;
    const hasRestriction = Array.isArray(allowed) && allowed.length > 0;
    // Personal preference — validate it's still within the allowed set
    if (authSession.activeRouterId) {
      if (!hasRestriction || allowed.includes(authSession.activeRouterId)) {
        return authSession.activeRouterId;
      }
    }
    if (hasRestriction) return allowed[0];
  }
  return cfg.activeRouterId || '';
}

// Return the router list visible to a specific socket (filtered by allowedRouterIds).
function _routersForSocket(socket) {
  const all = Routers.getPublic();
  const cfg = Settings.load();
  if ((cfg.authMode || 'basic') !== 'modern') return all;
  const allowed = socket.request?._authSession?.allowedRouterIds;
  if (Array.isArray(allowed) && allowed.length > 0) return all.filter(r => allowed.includes(r.id));
  return all;
}

// Broadcast updated router list to every connected socket, filtered per-user.
function _broadcastRoutersList() {
  for (const [, socket] of io.sockets.sockets) {
    socket.emit('routers:update', _routersForSocket(socket));
  }
}

// ── Startup ─────────────────────────────────────────────────────────────────
// Open the DB and initialise alerting BEFORE building any session: buildSession
// backfills the traffic chart from SQLite (needs the DB open), and
// ensureRouterSession re-syncs the alertSessions pool (needs alertSessions.init
// to have run). Getting this order wrong silently skips the chart backfill on
// every restart and builds status-only sessions before _mainIo is set.
db.open();
db.startPruneInterval(() => Settings.load());
alerter.init(io, Settings.load());
alertSessions.init(io);

// Auto-migrate any deployment still on 'basic' mode.
(function _migrateBasicAuth() {
  const s = Settings.load();
  if ((s.authMode || 'basic') !== 'basic') return;
  if (Users.userCount() > 0) {
    Settings.save({ authMode: 'modern' });
    console.warn('[auth] basic mode migrated to modern — existing users retained');
    return;
  }
  if (s.dashUser && s.dashPass) {
    const dashUser = s.dashUser;
    Users.createUser({ username: dashUser, password: s.dashPass, role: 'admin', allowedRouterIds: [] })
      .then(() => {
        Settings.save({ authMode: 'modern', dashUser: '', dashPass: '' });
        console.warn('[auth] basic credentials migrated to modern admin account "' + dashUser + '"');
      })
      .catch(e => console.error('[auth] migration failed:', e && e.message ? e.message : e));
    return;
  }
  Settings.save({ authMode: 'modern' });
  console.warn('[auth] basic mode with no credentials — switching to modern; create an admin account to get started');
})();

// Warn loudly if the dashboard is reachable with no authentication configured.
(function _warnIfOpen() {
  const s = Settings.load();
  if ((s.authMode || 'modern') === 'none') {
    console.warn('[MikroDash] ⚠ SECURITY: the dashboard is served with NO authentication.');
    console.warn('[MikroDash]   Switch to Session-based auth in Settings → Security.');
  }
})();

(function bootstrap() {
  // Ensure router list is seeded (backwards-compat: seed from settings.json)
  const routers = Routers.loadAll();

  // Determine active router
  const _cfg = Settings.load();
  let activeId = _cfg.activeRouterId;

  // If activeRouterId not set or points to non-existent router, use first entry
  if (!activeId || !Routers.getById(activeId)) {
    activeId = routers.length > 0 ? routers[0].id : null;
    if (activeId) Settings.save({ activeRouterId: activeId });
  }

  if (!activeId) {
    console.log('[MikroDash] No routers configured — open the web UI to add one.');
    _noRouterMode = true;
    // No pool session to own anything yet — start status-only tracking for all routers.
    _syncAlertSessions();
    _syncOverviewSessions();
    return;
  }

  ensureRouterSession(activeId); // also triggers _syncAlertSessions()
})();

// ── Login page route ──────────────────────────────────────────────────────────
app.get('/login', authLimiter, (_req, res) => res.sendFile(require('path').join(__dirname, '..', 'public', 'login.html')));

// ── Auth API ──────────────────────────────────────────────────────────────────

// GET /api/auth/status — mode, firstRun flag, current session info (no auth needed)
app.get('/api/auth/status', (req, res) => {
  const s        = Settings.load();
  const mode     = s.authMode || 'basic';
  const session  = _sessionFromReq(req); // live role (reflects demotions/deletions)
  const firstRun = mode === 'modern' && Users.userCount() === 0;
  res.json({
    authMode: mode,
    firstRun,
    session: session ? { username: session.username, role: session.role } : null,
  });
});

// POST /api/auth/login
const _clientIp = (req) => (req.ip || '').replace(/^::ffff:/, '');
// Strip CR/LF and other control chars so an attacker-supplied username can't
// forge or inject extra log lines (log injection).
const _logSafe = (v) => String(v == null ? '' : v).replace(/[\x00-\x1f\x7f]/g, '?').slice(0, 128);

// Resolve the configured session lifetime. 0 means "never expire" (→ Infinity in
// createSession); only fall back to the 1h default when the value is absent/invalid.
function _sessionTimeoutMs() {
  const v = Number(Settings.load().sessionTimeoutMs);
  return Number.isFinite(v) && v >= 0 ? v : 3600000;
}

app.post('/api/auth/login', loginLimiter, async (req, res) => {
  try {
    const { username, password } = req.body || {};
    if (!username || !password) return res.status(400).json({ ok: false, error: 'Missing credentials' });
    const user = await Users.getUserByUsername(username);
    // Always run verifyPassword — even for a missing user it spends equal scrypt
    // work (constant-time), so response timing doesn't leak whether the user exists.
    const ok = await Users.verifyPassword(user, password);
    if (!ok) {
      console.warn(`[auth] login failed — user="${_logSafe(username)}" ip=${_clientIp(req)}`);
      return res.status(401).json({ ok: false, error: 'Invalid username or password' });
    }
    const timeoutMs   = _sessionTimeoutMs();
    const { token, expiresAt } = SessionStore.createSession(user.id, user.username, user.role, timeoutMs, user.allowedRouterIds);
    res.setHeader('Set-Cookie', SessionStore.buildCookieHeader(token, expiresAt));
    console.log(`[auth] login — user="${user.username}" role=${user.role} ip=${_clientIp(req)}`);
    res.json({ ok: true, role: user.role, username: user.username });
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// GET /api/auth/logout
app.get('/api/auth/logout', (req, res) => {
  const token = SessionStore.parseCookieHeader(req.headers.cookie || '')['mikrodash_sid'];
  const session = token ? SessionStore.getSession(token) : null;
  if (token) SessionStore.deleteSession(token);
  res.setHeader('Set-Cookie', SessionStore.clearCookieHeader());
  if (session) console.log(`[auth] logout — user="${session.username}" ip=${_clientIp(req)}`);
  res.json({ ok: true });
});

// PUT /api/auth/me/active-router — persist personal router preference for modern-auth users
app.put('/api/auth/me/active-router', (req, res) => {
  const token = SessionStore.parseCookieHeader(req.headers.cookie || '')['mikrodash_sid'];
  const authSession = _sessionFromReq(req); // live role/perms
  if (!authSession) return res.status(401).json({ ok: false, error: 'Not authenticated' });
  const { routerId } = req.body || {};
  if (!routerId || typeof routerId !== 'string') return res.status(400).json({ ok: false, error: 'Missing routerId' });
  // Validate: admin can switch to any router; viewer only to allowed ones
  const router = Routers.getById(routerId);
  if (!router) return res.status(404).json({ ok: false, error: 'Router not found' });
  if (authSession.role !== 'admin') {
    const allowed = authSession.allowedRouterIds || [];
    if (allowed.length > 0 && !allowed.includes(routerId)) {
      return res.status(403).json({ ok: false, error: 'Router not in allowed list' });
    }
  }
  SessionStore.updateSession(token, { activeRouterId: routerId });
  res.json({ ok: true });
});

// ── Users API (admin only) ────────────────────────────────────────────────────

const _USERNAME_RE = /^[a-zA-Z0-9_.\-]{1,64}$/;

// POST /api/users/setup — create first admin (only when no users exist + modern mode)
// In-process latch: createUser is async, so two concurrent requests could both pass
// the userCount()===0 check before either writes. The synchronous latch closes that
// race so only the first request can create the initial admin.
let _setupClaimed = false;
app.post('/api/users/setup', setupLimiter, async (req, res) => {
  try {
    const s = Settings.load();
    if ((s.authMode || 'basic') !== 'modern') return res.status(400).json({ ok: false, error: 'Modern auth not enabled' });
    if (_setupClaimed || Users.userCount() > 0) return res.status(409).json({ ok: false, error: 'Setup already complete' });
    const { username, password } = req.body || {};
    if (!username || !_USERNAME_RE.test(username)) return res.status(400).json({ ok: false, error: 'Invalid username' });
    if (!password || String(password).length < 4)  return res.status(400).json({ ok: false, error: 'Password too short' });
    _setupClaimed = true; // claim synchronously, before the first await
    try {
      const user = await Users.createUser({ username, password, role: 'admin', allowedRouterIds: [] });
      res.json({ ok: true, user });
    } catch (e) {
      _setupClaimed = false; // creation failed — let setup be retried
      throw e;
    }
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// GET /api/users
app.get('/api/users', _requireAdmin, async (_req, res) => {
  try {
    res.json({ ok: true, users: await Users.listUsers() });
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// POST /api/users
app.post('/api/users', _requireAdmin, async (req, res) => {
  try {
    const { username, password, role, allowedRouterIds } = req.body || {};
    if (!username || !_USERNAME_RE.test(username)) return res.status(400).json({ ok: false, error: 'Invalid username' });
    if (!password || String(password).length < 4)  return res.status(400).json({ ok: false, error: 'Password too short' });
    if (role && !['admin','viewer'].includes(role))  return res.status(400).json({ ok: false, error: 'Invalid role' });
    if (await Users.getUserByUsername(username))     return res.status(409).json({ ok: false, error: 'Username already exists' });
    const user = await Users.createUser({ username, password, role: role || 'viewer', allowedRouterIds });
    res.json({ ok: true, user });
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// PUT /api/users/:id
app.put('/api/users/:id', _requireAdmin, async (req, res) => {
  try {
    const { id } = req.params;
    const updates = req.body || {};
    // Block admin from downgrading their own role
    if (req.authSession && req.authSession.userId === id && updates.role === 'viewer') {
      return res.status(400).json({ ok: false, error: 'Cannot remove admin role from your own account' });
    }
    if (updates.username !== undefined && !_USERNAME_RE.test(updates.username)) {
      return res.status(400).json({ ok: false, error: 'Invalid username' });
    }
    if (updates.role !== undefined && !['admin','viewer'].includes(updates.role)) {
      return res.status(400).json({ ok: false, error: 'Invalid role' });
    }
    // Don't let the last admin be demoted — that would lock everyone out of admin functions.
    if (updates.role === 'viewer') {
      const target = await Users.getUser(id);
      if (target && target.role === 'admin' && Users.adminCount() <= 1) {
        return res.status(400).json({ ok: false, error: 'Cannot demote the last admin' });
      }
    }
    const updated = await Users.updateUser(id, updates);
    if (!updated) return res.status(404).json({ ok: false, error: 'User not found' });
    res.json({ ok: true, user: updated });
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// DELETE /api/users/:id
app.delete('/api/users/:id', _requireAdmin, async (req, res) => {
  try {
    const { id } = req.params;
    if (req.authSession && req.authSession.userId === id) {
      return res.status(400).json({ ok: false, error: 'Cannot delete your own account' });
    }
    // Don't let the last admin be deleted — that would lock everyone out of admin functions.
    const target = await Users.getUser(id);
    if (target && target.role === 'admin' && Users.adminCount() <= 1) {
      return res.status(400).json({ ok: false, error: 'Cannot delete the last admin' });
    }
    const deleted = await Users.deleteUser(id);
    if (!deleted) return res.status(404).json({ ok: false, error: 'User not found' });
    res.json({ ok: true });
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// ── Dashboard layout API ──────────────────────────────────────────────────────
// Per-user layout when modern auth is active; falls back to shared file otherwise.
function _layoutFile(req) {
  const uid  = req.authSession?.userId;
  const base = process.env.DATA_DIR || '/data';
  return path.join(base, uid ? `dashboard-layout-${uid}.json` : 'dashboard-layout.json');
}

const layoutLimiter = rateLimit({ windowMs: 60_000, max: 60, standardHeaders: true, legacyHeaders: false });

app.get('/api/dashboard-layout', layoutLimiter, (req, res) => {
  try {
    const file = _layoutFile(req);
    if (fs.existsSync(file)) {
      return res.json(JSON.parse(fs.readFileSync(file, 'utf8')));
    }
    // Per-user file doesn't exist yet — fall back to shared file so the client's
    // localStorage cache is refreshed rather than left stale from a previous user.
    const shared = path.join(process.env.DATA_DIR || '/data', 'dashboard-layout.json');
    if (fs.existsSync(shared)) {
      return res.json(JSON.parse(fs.readFileSync(shared, 'utf8')));
    }
    res.json(null);
  } catch (_) { res.json(null); }
});

app.post('/api/dashboard-layout', layoutLimiter, (req, res) => {
  try {
    const body = req.body || {};
    if (!Array.isArray(body.cards)) return res.status(400).json({ ok: false });
    fs.writeFileSync(_layoutFile(req), JSON.stringify({ cards: body.cards }), 'utf8');
    res.json({ ok: true });
  } catch (e) { res.status(500).json({ ok: false }); }
});

// ── Settings API ──────────────────────────────────────────────────────────────
// In modern auth, viewers get only a non-sensitive subset; admins get the full
// (credential-masked) settings. Basic/none mode is unchanged (full masked view).
app.get('/api/settings', (req, res) => {
  const modern = (Settings.load().authMode || 'basic') === 'modern';
  if (modern && (!req.authSession || req.authSession.role !== 'admin')) {
    return res.json(Settings.getViewerPublic());
  }
  res.json(Settings.getPublic());
});

app.post('/api/settings', _requireAdmin, (req, res) => {
  try {
    const body = req.body || {};
    if (body._reset) {
      const { DEFAULTS } = require('./settings');
      Settings.save(DEFAULTS);
      io.emit('settings:pages', {
        pageWireless:DEFAULTS.pageWireless, pageInterfaces:DEFAULTS.pageInterfaces,
        pageDhcp:DEFAULTS.pageDhcp, pageVpn:DEFAULTS.pageVpn,
        pageConnections:DEFAULTS.pageConnections, pageFirewall:DEFAULTS.pageFirewall,
        pageLogs:DEFAULTS.pageLogs, pageBandwidth:DEFAULTS.pageBandwidth,
        pageRouting:DEFAULTS.pageRouting,
        alertCpuThreshold:DEFAULTS.alertCpuThreshold, alertPingLoss:DEFAULTS.alertPingLoss,
        vpnDashTopN:DEFAULTS.vpnDashTopN, pingEnabled:DEFAULTS.pingEnabled,
        notifIfaceUpDown:DEFAULTS.notifIfaceUpDown, notifVpn:DEFAULTS.notifVpn,
        notifCpu:DEFAULTS.notifCpu, notifPing:DEFAULTS.notifPing, notifNetwatch:DEFAULTS.notifNetwatch,
        notifRouterStatus:DEFAULTS.notifRouterStatus,
        notifIfaceEther:DEFAULTS.notifIfaceEther, notifIfaceWlan:DEFAULTS.notifIfaceWlan,
        notifIfaceBridge:DEFAULTS.notifIfaceBridge, notifIfaceVlan:DEFAULTS.notifIfaceVlan,
        notifIfaceOther:DEFAULTS.notifIfaceOther,
        displayTimezone:DEFAULTS.displayTimezone,
      });
      return res.json({ ok:true, requiresRestart:false });
    }
    const updates = {};
    const intFields = {
      routerPort:[1,65535], pollConns:[1000,60000], pollTalkers:[1000,60000], pollSystem:[1000,60000],
      pollWireless:[10000,600000], pollVpn:[1000,30000],  pollFirewall:[1000,30000],
      pollIfstatus:[1000,60000], pollIfaces:[10000,600000], pollPing:[1000,30000], pollArp:[5000,300000],
      pollBandwidth:[1000,60000], pollDhcp:[10000,600000], pollRouting:[500,300000], topN:[1,50], topTalkersN:[1,20],
      firewallTopN:[1,50], vpnDashTopN:[1,50], maxConns:[1000,100000], historyMinutes:[5,120],
      alertCpuThreshold:[1,100], alertPingLoss:[1,100], notifCooldownSec:[10,3600],
      smtpPort:[1,65535],
      dbRetentionDays:[1,3650], dbAlertRetentionDays:[1,3650],
    };
    const strFields  = ['pingTarget', 'telegramChatId', 'notifTitle', 'smtpHost', 'smtpFrom', 'smtpTo', 'ntfyUrl'];
    // authMode: whitelist only valid values
    if ('authMode' in body && ['none','modern'].includes(body.authMode)) updates.authMode = body.authMode;
    // sessionTimeoutMs: 0 (never) or 3600000–86400000 — must not clamp 0 to a minimum
    if ('sessionTimeoutMs' in body) {
      const v = parseInt(body.sessionTimeoutMs, 10);
      if (!isNaN(v) && (v === 0 || (v >= 3600000 && v <= 86400000))) updates.sessionTimeoutMs = v;
    }
    const boolFields = ['pageWireless','pageInterfaces','pageDhcp','pageVpn',
                        'pageConnections','pageFirewall','pageLogs','pageBandwidth','pageRouting',
                        'pingEnabled','rosDebug',
                        'streamSystem','streamPing','streamConns','streamTalkers','streamIfrates',
                        'telegramEnabled','pushbulletEnabled','smtpEnabled','smtpSecure','ntfyEnabled',
                        'notifIfaceUpDown','notifVpn','notifCpu','notifPing','notifNetwatch','notifRouterStatus',
                        'notifIfaceEther','notifIfaceWlan','notifIfaceBridge','notifIfaceVlan','notifIfaceOther'];
    const credFields = ['telegramBotToken', 'pushbulletApiKey', 'smtpUser', 'smtpPass', 'ntfyToken'];

    for (const [f, range] of Object.entries(intFields)) {
      if (f in body) { const v = parseInt(body[f],10); if (!isNaN(v) && v>=range[0] && v<=range[1]) updates[f]=v; }
    }
    for (const f of strFields)  { if (f in body) updates[f] = String(body[f]).trim().slice(0,256); }
    for (const f of boolFields) { if (f in body) updates[f] = body[f]===true||body[f]==='true'; }
    for (const f of credFields) { if (f in body && !Settings.isMasked(body[f])) updates[f] = String(body[f]).slice(0,512); }
    if ('notifBody'   in body) updates.notifBody   = String(body.notifBody).trim().slice(0, 512);
    if ('notifBodyUp' in body) updates.notifBodyUp = String(body.notifBodyUp).trim().slice(0, 512);
    if ('customPollProfile' in body) {
      const v = String(body.customPollProfile).trim().slice(0, 512);
      try { if (v === '' || typeof JSON.parse(v) === 'object') updates.customPollProfile = v; } catch(_) {}
    }
    if ('displayTimezone' in body) {
      const tz = String(body.displayTimezone).trim().slice(0, 64);
      if (tz === '') { updates.displayTimezone = ''; }
      else { try { new Intl.DateTimeFormat(undefined, { timeZone: tz }); updates.displayTimezone = tz; } catch (_) {} }
    }

    const saved = Settings.save(updates);
    alerter.updateSettings(saved);

    // Apply poll interval changes live to the global-default session
    const s = _globalSession();
    if (!s) {
      return res.json({ ok: true, requiresRestart: false });
    }
    const collectorMap = { conns:s.conns, talkers:s.talkers, system:s.system, wireless:s.wireless, vpn:s.vpn, firewall:s.firewall, ifStatus:s.ifStatus, ping:s.ping, arp:s.arp, dhcpNetworks:s.dhcpNetworks, bandwidth:s.bandwidth, routing:s.routing };
    const pollMap = { pollConns:'conns', pollTalkers:'talkers', pollSystem:'system', pollWireless:'wireless',
      pollVpn:'vpn', pollFirewall:'firewall', pollIfstatus:'ifStatus', pollBandwidth:'bandwidth',
      pollPing:'ping', pollArp:'arp', pollDhcp:'dhcpNetworks', pollRouting:'routing' };
    for (const [key, name] of Object.entries(pollMap)) {
      if (key in updates) {
        const col = collectorMap[name];
        if (col) {
          const _p = Number.isFinite(Number(saved[key])) ? Math.trunc(Number(saved[key])) : col.pollMs;
          col.pollMs = Math.max(500, Math.min(600000, _p));
          if (typeof col._restartTimer === 'function') {
            col._restartTimer();
          } else if (col.timer) {
            clearInterval(col.timer); col.timer = null;
            const run = async () => {
              if (col._inflight) return; col._inflight = true;
              try { await col.tick(); } catch(_){} finally { col._inflight = false; }
            };
            col.timer = setInterval(run, Math.max(500, col.pollMs)); // codeql[js/resource-exhaustion]
          }
        }
      }
    }
    // pollConns controls the /ip/firewall/connection/print stream interval
    if ('pollConns' in updates && s.conns) {
      s.conns.pollMs = saved.pollConns;
      s.conns._restartStream();
    }

    // pollPing controls the /tool/ping stream interval
    if ('pollPing' in updates && s.ping) {
      s.ping.pollMs = saved.pollPing;
      s.ping._restartStream();
    }

    // pollTalkers controls the kid-control stream interval
    if ('pollTalkers' in updates && s.talkers) {
      s.talkers.pollMs = saved.pollTalkers;
      s.talkers._restartStream();
    }

    // pollSystem controls the ros.stream() interval — restart with new =interval=N
    if ('pollSystem' in updates && s.system) {
      s.system.pollMs = saved.pollSystem;
      s.system._restartStream();
    }

    // pollIfstatus controls the emit timer + monitor-traffic stream interval
    if ('pollIfstatus' in updates && s.ifStatus) {
      s.ifStatus.pollMs = saved.pollIfstatus;
      s.ifStatus._restartEmitTimer();
      s.ifStatus._restartMonitorStream();
    }

    // streamMode toggles — stop collector, flip flag, restart with new mode
    for (const [key, collector] of [
      ['streamSystem',  s.system],
      ['streamPing',    s.ping],
      ['streamConns',   s.conns],
      ['streamTalkers', s.talkers],
      ['streamIfrates', s.ifStatus],
    ]) {
      if (key in updates && collector) {
        collector.stop();
        collector.streamMode = saved[key];
        collector.start();
        if (io.engine.clientsCount === 0 && typeof collector.suspend === 'function') collector.suspend();
      }
    }

    // pollIfaces controls the /interface/print + /ip/address/print stream interval
    if ('pollIfaces' in updates && s.ifStatus) {
      s.ifStatus.metaPollMs = saved.pollIfaces;
      s.ifStatus._restartMetaStreams();
    }

    // pollFirewall controls the counter stream interval — restart it live
    if ('pollFirewall' in updates && s.firewall) {
      s.firewall.pollMs = saved.pollFirewall;
      s.firewall._stopCounterStreams();
      s.firewall._startCounterStreams();
    }

    // pollVpn controls the VPN counter stream interval — restart it live
    if ('pollVpn' in updates && s.vpn) {
      s.vpn.pollMs = saved.pollVpn;
      s.vpn._stopCounterStream();
      s.vpn._startCounterStream();
    }

    // Apply pingEnabled toggle live — stop/start the collector immediately
    if ('pingEnabled' in updates && s.ping) {
      if (saved.pingEnabled) {
        s.ping._permissionDenied = false;
        s.ping._lastFp = '';
        if (!s.ping._stream) s.ping.start();
      } else {
        s.ping.stop();
        io.emit('ping:update', { enabled: false });
      }
    }

    // Apply pingTarget change live — restart stream with new =address=
    if ('pingTarget' in updates && s.ping) {
      s.ping.target = saved.pingTarget;
      s.ping._lastFp = '';
      s.ping._lossWindow = [];
      s.ping._restartStream();
      if (s.ping.lastPayload) {
        const updated = { ...s.ping.lastPayload, target: saved.pingTarget, ts: Date.now() };
        s.ping.lastPayload = updated;
        io.emit('ping:update', updated);
      }
    }

    // Apply topN changes live — update running collectors and force re-emit
    if (s) {
      if ('topN' in updates && s.conns) {
        s.conns.topN = saved.topN;
        s.conns._lastFp = '';
      }
      if ('topTalkersN' in updates && s.talkers) {
        s.talkers.topN = saved.topTalkersN;
        s.talkers._lastFp = '';
      }
      if ('firewallTopN' in updates && s.firewall) {
        s.firewall.topN = saved.firewallTopN;
        s.firewall._lastFp = '';
      }
      if ('maxConns' in updates && s.conns) {
        s.conns.maxConns = saved.maxConns;
      }
    }

    const pageSettings = {
      pageWireless:saved.pageWireless, pageInterfaces:saved.pageInterfaces,
      pageDhcp:saved.pageDhcp, pageVpn:saved.pageVpn,
      pageConnections:saved.pageConnections, pageFirewall:saved.pageFirewall,
      pageLogs:saved.pageLogs, pageBandwidth:saved.pageBandwidth,
      pageRouting:saved.pageRouting,
      alertCpuThreshold:saved.alertCpuThreshold, alertPingLoss:saved.alertPingLoss,
      vpnDashTopN:saved.vpnDashTopN, pingEnabled:saved.pingEnabled,
      notifIfaceUpDown:saved.notifIfaceUpDown, notifVpn:saved.notifVpn,
      notifCpu:saved.notifCpu, notifPing:saved.notifPing, notifNetwatch:saved.notifNetwatch,
      notifRouterStatus:saved.notifRouterStatus,
      notifIfaceEther:saved.notifIfaceEther, notifIfaceWlan:saved.notifIfaceWlan,
      notifIfaceBridge:saved.notifIfaceBridge, notifIfaceVlan:saved.notifIfaceVlan,
      notifIfaceOther:saved.notifIfaceOther,
      displayTimezone:saved.displayTimezone,
    };
    io.emit('settings:pages', pageSettings);
    res.json({ ok:true, requiresRestart:false });
  } catch(e) {
    console.error('[settings] save error:', e);
    res.status(500).json({ ok:false, error: sanitizeErr(e) });
  }
});

// ── Notification test endpoint ────────────────────────────────────────────────
const _testNotifLimiter = rateLimit({ windowMs: 60_000, max: 10, standardHeaders: true, legacyHeaders: false });

app.post('/api/settings/test-notification', _requireAdmin, _testNotifLimiter, async (req, res) => {
  try {
    const { channel, apiKey, botToken, chatId,
            smtpHost, smtpPort, smtpSecure, smtpUser, smtpPass, smtpFrom, smtpTo,
            ntfyUrl, ntfyToken } = req.body || {};
    if (!channel) return res.status(400).json({ ok: false, error: 'channel is required' });
    // Merge any credentials supplied directly (typed but not yet saved) over stored settings.
    const base = Settings.load();
    const settings = {
      ...base,
      ...(botToken  && { telegramBotToken: String(botToken).slice(0, 512) }),
      ...(chatId    && { telegramChatId:   String(chatId).slice(0, 256)  }),
      ...(apiKey    && { pushbulletApiKey: String(apiKey).slice(0, 512)  }),
      ...(smtpHost  && { smtpHost:  String(smtpHost).slice(0, 256)  }),
      ...(smtpFrom  && { smtpFrom:  String(smtpFrom).slice(0, 256)  }),
      ...(smtpTo    && { smtpTo:    String(smtpTo).slice(0, 256)    }),
      ...(smtpUser  && { smtpUser:  String(smtpUser).slice(0, 256)  }),
      ...(smtpPass  && { smtpPass:  String(smtpPass).slice(0, 512)  }),
      ...(smtpPort  !== undefined && { smtpPort:   parseInt(smtpPort,  10) || 587 }),
      ...(smtpSecure !== undefined && { smtpSecure: smtpSecure === true || smtpSecure === 'true' }),
      ...(ntfyUrl   && { ntfyUrl:   String(ntfyUrl).slice(0, 512)   }),
      ...(ntfyToken && { ntfyToken: String(ntfyToken).slice(0, 512) }),
    };
    await notifier.testChannel(settings, channel);
    res.json({ ok: true });
  } catch (e) {
    console.error('[test-notification]', e.message);
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// ── Routers API ───────────────────────────────────────────────────────────────

// GET /api/routers — list all routers (passwords masked); filtered by allowedRouterIds in modern mode
app.get('/api/routers', (req, res) => {
  const cfg    = Settings.load();
  const active = cfg.activeRouterId || '';
  let routers  = Routers.getPublic();
  if ((cfg.authMode || 'basic') === 'modern' && req.authSession) {
    const allowed = req.authSession.allowedRouterIds;
    if (Array.isArray(allowed) && allowed.length > 0) {
      routers = routers.filter(r => allowed.includes(r.id));
    }
  }
  res.json({ routers, activeId: active });
});

// POST /api/routers — add a new router
app.post('/api/routers', _requireAdmin, (req, res) => {
  try {
    const body = req.body || {};
    if (!body.host || !String(body.host).trim()) {
      return res.status(400).json({ ok:false, error:'host is required' });
    }
    const router = Routers.add(body);
    _broadcastRoutersList();
    _syncAlertSessions();
    _syncOverviewSessions();
    res.json({ ok:true, router: { ...router, password: router.password ? '••••••••' : '' } });
  } catch(e) {
    res.status(500).json({ ok:false, error: sanitizeErr(e) });
  }
});

// PUT /api/routers/:id — edit a router
app.put('/api/routers/:id', _requireAdmin, (req, res) => {
  try {
    const router = Routers.update(req.params.id, req.body || {});
    if (!router) return res.status(404).json({ ok:false, error:'Router not found' });
    _broadcastRoutersList();

    // If this is the active router and pingTarget changed, update the live
    // collector immediately — don't make the user wait for the next poll cycle.
    const activeId = Settings.load().activeRouterId;
    const _gs = _globalSession();
    if (_gs && req.params.id === activeId && req.body && req.body.pingTarget) {
      const newTarget = router.pingTarget;
      if (_gs.ping && _gs.ping.target !== newTarget) {
        _gs.ping.target      = newTarget;
        _gs.ping._lastFp     = '';
        _gs.ping._lossWindow = [];
        _gs.ping._restartStream();
        if (_gs.ping.lastPayload) {
          const updated = { ..._gs.ping.lastPayload, target: newTarget, ts: Date.now() };
          _gs.ping.lastPayload = updated;
          io.emit('ping:update', updated);
        }
      }
    }

    _syncAlertSessions();
    _syncOverviewSessions();
    res.json({ ok:true, router: { ...router, password: router.password ? '••••••••' : '' } });
  } catch(e) {
    res.status(500).json({ ok:false, error: sanitizeErr(e) });
  }
});

// DELETE /api/routers/:id — delete a router (cannot delete the active router)
app.delete('/api/routers/:id', _requireAdmin, async (req, res) => {
  try {
    const _cfg = Settings.load();
    if (req.params.id === _cfg.activeRouterId) {
      return res.status(409).json({ ok:false, error:'Cannot delete the active router. Switch to a different router first.' });
    }
    const deleted = Routers.remove(req.params.id);
    if (!deleted) return res.status(404).json({ ok:false, error:'Router not found' });
    // Tear down any live pool session for the deleted router.
    const _deletedEntry = _routerSessions.get(req.params.id);
    if (_deletedEntry) {
      if (_deletedEntry.idleTimer) { clearTimeout(_deletedEntry.idleTimer); _deletedEntry.idleTimer = null; }
      await teardownSession(_deletedEntry.session, _deletedEntry);
      _routerSessions.delete(req.params.id);
    }
    // Drop any pool evaluator/alertSession state for the removed router.
    alerter.dropEvaluator(req.params.id);
    _broadcastRoutersList();
    _syncAlertSessions();
    _syncOverviewSessions();
    res.json({ ok:true });
  } catch(e) {
    res.status(500).json({ ok:false, error: sanitizeErr(e) });
  }
});

// POST /api/routers/:id/activate — switch to a different router (hot-swap)
app.post('/api/routers/:id/activate', _requireAdmin, async (req, res) => {
  const _cfg = Settings.load();
  if (req.params.id === _cfg.activeRouterId) {
    return res.json({ ok:true, alreadyActive:true });
  }
  res.json({ ok:true, switching:true }); // respond before the async switch
  const result = await switchRouter(req.params.id);
  if (!result.ok) {
    console.error('[MikroDash] Router switch failed:', result.error);
    io.emit('router:switch-error', { error: result.error });
  }
  // Broadcast updated active state. This is the GLOBAL default changing, so only
  // notify sockets that actually follow the global default — i.e. those now in the
  // new router's room. Modern-auth users pinned to a different router via
  // router:switch keep their own view (switchRouter only moved non-pinned sockets),
  // so a global io.emit here would wrongly flip their selector to a router whose
  // data they aren't receiving.
  _broadcastRoutersList();
  io.to('router-' + req.params.id).emit('router:active', { activeId: req.params.id });
  _syncAlertSessions();
  _syncOverviewSessions();
});

// POST /api/routers/test — test a connection without saving
const _testConnLimiter = rateLimit({ windowMs: 60_000, max: 10, standardHeaders: true, legacyHeaders: false });
app.post('/api/routers/test', _requireAdmin, _testConnLimiter, async (req, res) => {
  const body = req.body || {};
  if (!body.host) return res.status(400).json({ ok:false, error:'host is required' });

  const testTls = (body.tls !== false && body.tls !== 'false');
  const testTlsInsecure = !!(body.tlsInsecure || body.tlsInsecure === 'true');
  const testRos = new ROS({
    host:           String(body.host).trim(),
    port:           parseInt(body.port || '8729', 10),
    tls:            testTls ? { rejectUnauthorized: !testTlsInsecure } : false,
    username:       String(body.username || 'admin').trim(),
    password:       body.password && body.password !== '••••••••' ? String(body.password) : '',
    writeTimeoutMs: 8000,
  });

  let resolved = false;
  const done = (ok, error, boardName) => {
    if (resolved) return;
    resolved = true;
    testRos.stop();
    if (ok) res.json({ ok:true, boardName: boardName || '' });
    else    res.json({ ok:false, error: error || 'Connection failed' });
  };

  const timeout = setTimeout(() => done(false, 'Connection timed out after 8 seconds'), 9000);

  testRos.on('connectionError', (e) => {
    clearTimeout(timeout);
    const msg = e && e.message ? e.message : String(e);
    let reason = msg;
    if (/ECONNREFUSED/.test(msg))                                reason = 'Connection refused — check host and port';
    else if (/ETIMEDOUT/.test(msg) || /timed out/i.test(msg))   reason = 'Connection timed out — check host and firewall rules';
    else if (/ENOTFOUND/.test(msg) || /ENOENT/.test(msg))       reason = 'Host not found — check router host/IP';
    else if (/ECONNRESET/.test(msg))                            reason = 'Connection reset by router';
    else if (/certificate/i.test(msg))                          reason = 'TLS certificate error — try enabling "Allow self-signed cert"';
    else if (/authentication/i.test(msg) || /login/i.test(msg) || /username.*invalid|password.*invalid/i.test(msg) || (e && e.errno === 'CANTLOGIN')) reason = 'Authentication failed — check username and password';
    else if (/RosException/.test(msg) || (e && e.name === 'RosException')) {
      const errno = e && e.errno ? ` [${e.errno}]` : '';
      reason = body.tls
        ? `TLS handshake failed — check that RouterOS api-ssl is enabled${errno}`
        : `RouterOS API error${errno} — check that api service is enabled and user has API access`;
    }
    done(false, reason);
  });
  testRos.on('connected', async () => {
    clearTimeout(timeout);
    try {
      const result = await testRos.write('/system/resource/print', [
        '=.proplist=board-name,version',
      ]);
      const r = (result && result[0]) || {};
      done(true, null, r['board-name'] || r.platform || '');
    } catch (_) {
      done(true, null, ''); // connected but /system/resource failed — still OK
    }
  });

  testRos.connectLoop().catch(() => {});
});

// ── Existing read-only endpoints ──────────────────────────────────────────────
app.get('/api/localcc', (_req, res) => {
  const s = _globalSession();
  if (!s) return res.json({ cc: '', wanIp: '' });
  const wanIp = (s.state.lastWanIp || '').split('/')[0];
  let cc = '';
  if (geoip && wanIp) { const g = geoip.lookup(wanIp); if (g) cc = g.country || ''; }
  res.json({ cc, wanIp });
});

function sanitizeErr(e) {
  if (!e) return null;
  const msg = (e && e.message) ? e.message : String(e);
  return msg
    .replace(/\/[^\s'"]{2,}/g, '[path]')
    .replace(/\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(:\d+)?/g, '[addr]')
    .slice(0, 200);
}

app.get('/healthz', (_req, res) => {
  const ge = _globalEntry();
  const s  = ge ? ge.session : null;
  const { ok, statusCode } = computeHealthStatus({
    startupReady: ge ? ge.startupReady : false,
    rosConnected: s ? s.ros.connected : false,
  });
  const st = s ? s.state : {};
  const isStarting = !(ge && ge.startupReady) && (Date.now() - _serverStartTime < STARTUP_GRACE_MS);
  const body = {
    ok,
    starting: isStarting,
    version: APP_VERSION,
    routerConnected: s ? s.ros.connected : false,
    activeRouterId:  s ? s.routerId : null,
    startupReady: ge ? ge.startupReady : false,
    uptime: process.uptime(),
    now: Date.now(),
    defaultIf: s ? s.DEFAULT_IF : '',
    checks: {
      traffic:  { ts:st.lastTrafficTs,  err:sanitizeErr(st.lastTrafficErr)  },
      conns:    { ts:st.lastConnsTs,    err:sanitizeErr(st.lastConnsErr)    },
      leases:   { ts:st.lastLeasesTs,   err:null                            },
      arp:      { ts:st.lastArpTs,      err:null                            },
      talkers:  { ts:st.lastTalkersTs,  err:sanitizeErr(st.lastTalkersErr)  },
      logs:     { ts:st.lastLogsTs,     err:sanitizeErr(st.lastLogsErr)     },
      system:   { ts:st.lastSystemTs,   err:sanitizeErr(st.lastSystemErr)   },
      wireless: { ts:st.lastWirelessTs, err:sanitizeErr(st.lastWirelessErr) },
      vpn:      { ts:st.lastVpnTs,      err:sanitizeErr(st.lastVpnErr)      },
      firewall: { ts:st.lastFirewallTs, err:sanitizeErr(st.lastFirewallErr) },
      ping:     { ts:st.lastPingTs,     err:null                            },
      netwatch: { ts:st.lastNetwatchTs, err:sanitizeErr(st.lastNetwatchErr) },
    },
  };
  res.status(statusCode).json(body);
});

// ── Reports API ───────────────────────────────────────────────────────────────

const _AGG_VALID = new Set(['hour', 'day', 'week', 'month']);

function _parseReportParams(query) {
  const routerId  = String(query.routerId || '');
  const from      = parseInt(query.from, 10) || 0;
  const to        = parseInt(query.to,   10) || Date.now();
  const aggregate = _AGG_VALID.has(query.aggregate) ? query.aggregate : '';
  return { routerId, from, to, aggregate };
}

function _toCsv(rows, columns) {
  const header = columns.join(',');
  const body   = rows.map(r => columns.map(c => {
    const v = r[c];
    if (v == null) return '';
    let s = String(v);
    // Neutralise spreadsheet formula injection: a cell that a router-controlled
    // string (interface name, ping target, alert subject) could start with
    // =, +, -, @, tab or CR is executed as a formula by Excel/Sheets. Prefix a
    // single quote so it's treated as literal text.
    if (/^[=+\-@\t\r]/.test(s)) s = "'" + s;
    return s.includes(',') || s.includes('"') || s.includes('\n') ? `"${s.replace(/"/g, '""')}"` : s;
  }).join(',')).join('\n');
  return header + '\n' + body;
}

// meta: { router, from, to, stats:[{label,value}], chartData:{lines:[{label,color,pts:[{x,y}]}],yLabel} }
function _toPdf(title, columns, rows, res, meta) {
  const PDFDocument = require('pdfkit');
  const L = 40, R = 40;
  const doc = new PDFDocument({ margin: L, size: 'A4' });
  res.setHeader('Content-Type', 'application/pdf');
  res.setHeader('Content-Disposition', `attachment; filename="${title}.pdf"`);
  doc.pipe(res);

  const PW = doc.page.width;
  const inner = PW - L - R;

  // ── Header bar ────────────────────────────────────────────────────────
  const hTop = 30;
  doc.rect(0, 0, PW, 52).fill('#0f172a');
  // Logo text
  doc.font('Helvetica-Bold').fontSize(17).fillColor('#38bdf8')
     .text('Mikro', L, hTop, { continued: true })
     .fillColor('#f8fafc')
     .text('Dash', { lineBreak: false });
  // Report title centred
  doc.font('Helvetica-Bold').fontSize(13).fillColor('#f8fafc')
     .text(title, L, hTop + 1, { width: inner, align: 'center', lineBreak: false });
  doc.fillColor('#000000'); // reset

  let y = 66;

  // ── Meta info row ─────────────────────────────────────────────────────
  const fmtTs = ts => ts ? _tsFmt(ts) || '—' : '—';
  const routerLabel = (meta && meta.router) ? meta.router : '';
  const dateRange   = (meta && meta.from && meta.to)
    ? `${fmtTs(meta.from)}  →  ${fmtTs(meta.to)}`
    : '';
  if (routerLabel || dateRange) {
    doc.font('Helvetica').fontSize(8).fillColor('#64748b');
    if (routerLabel) doc.text(`Router: ${routerLabel}`, L, y, { lineBreak: false });
    if (dateRange)   doc.text(dateRange, L, y, { width: inner, align: 'right', lineBreak: false });
    doc.fillColor('#000000');
    y += 16;
    doc.moveTo(L, y).lineTo(PW - R, y).lineWidth(0.5).strokeColor('#e2e8f0').stroke();
    doc.lineWidth(1).strokeColor('#000000');
    y += 10;
  }

  // ── Stat boxes ────────────────────────────────────────────────────────
  if (meta && meta.stats && meta.stats.length) {
    const n     = meta.stats.length;
    const boxW  = Math.min(110, Math.floor((inner - (n - 1) * 8) / n));
    const boxH  = 36;
    const totalW = n * boxW + (n - 1) * 8;
    const startX = L + Math.floor((inner - totalW) / 2);
    meta.stats.forEach((s, i) => {
      const bx = startX + i * (boxW + 8);
      doc.roundedRect(bx, y, boxW, boxH, 4).lineWidth(0.75).strokeColor('#cbd5e1').stroke();
      doc.font('Helvetica-Bold').fontSize(11).fillColor('#0f172a')
         .text(String(s.value), bx + 4, y + 5, { width: boxW - 8, align: 'center', lineBreak: false });
      doc.font('Helvetica').fontSize(7).fillColor('#64748b')
         .text(s.label, bx + 4, y + 20, { width: boxW - 8, align: 'center', lineBreak: false });
    });
    doc.fillColor('#000000');
    y += boxH + 14;
  }

  // ── Chart ─────────────────────────────────────────────────────────────
  if (meta && meta.chartData && meta.chartData.lines && meta.chartData.lines.length) {
    const cd      = meta.chartData;
    const lines   = cd.lines.filter(l => l.pts && l.pts.length > 1);
    if (lines.length) {
      const CH = 110, yAxisW = 38, xAxisH = 16;
      const cLeft = L + yAxisW, cRight = PW - R;
      const cW    = cRight - cLeft;
      const cTop  = y, cBot = y + CH;

      // Compute y-range across all lines
      let yMin = Infinity, yMax = -Infinity;
      lines.forEach(l => l.pts.forEach(p => { if (p.y < yMin) yMin = p.y; if (p.y > yMax) yMax = p.y; }));
      if (yMin === yMax) { yMin = 0; yMax = yMax || 1; }
      if (yMin > 0) yMin = 0;
      const yRange = yMax - yMin;
      const xMin = lines[0].pts[0].x;
      const xMax = lines[0].pts[lines[0].pts.length - 1].x;
      const xRange = xMax - xMin || 1;

      const toX = xv => cLeft + ((xv - xMin) / xRange) * cW;
      const toY = yv => cBot  - ((yv - yMin) / yRange) * CH;

      // Grid lines + Y labels (5 steps)
      doc.font('Helvetica').fontSize(7).fillColor('#94a3b8');
      for (let step = 0; step <= 4; step++) {
        const yv  = yMin + (yRange / 4) * step;
        const gy  = toY(yv);
        doc.moveTo(cLeft, gy).lineTo(cRight, gy).lineWidth(0.3).strokeColor('#e2e8f0').stroke();
        const lbl = yv >= 1000 ? (yv / 1000).toFixed(1) + 'k' : yv.toFixed(1);
        doc.text(lbl, L, gy - 4, { width: yAxisW - 4, align: 'right', lineBreak: false });
      }
      if (cd.yLabel) {
        doc.text(cd.yLabel, L, y + CH / 2 - 4, { width: yAxisW - 4, align: 'right', lineBreak: false });
      }

      // X axis time labels (5 ticks) — format adapts to span; respects displayTimezone
      const _tz      = Settings.load().displayTimezone || '';
      const HOUR     = 3600000, DAY = 86400000;
      const spanMs   = xRange;
      const labelW   = spanMs <= 12 * HOUR ? 28 : spanMs <= 3 * DAY ? 54 : 28;
      const _pdfTick = ts => {
        if (_tz) {
          let opts;
          if (spanMs <= 12 * HOUR) opts = { timeZone:_tz, hour:'2-digit', minute:'2-digit', hour12:false };
          else if (spanMs <= 3 * DAY) opts = { timeZone:_tz, month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit', hour12:false };
          else opts = { timeZone:_tz, month:'2-digit', day:'2-digit' };
          return new Intl.DateTimeFormat('sv-SE', opts).format(new Date(ts));
        }
        const d = new Date(ts), p = n => String(n).padStart(2, '0');
        if (spanMs <= 12 * HOUR)  return `${p(d.getHours())}:${p(d.getMinutes())}`;
        if (spanMs <= 3  * DAY)   return `${p(d.getMonth()+1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
        return `${p(d.getMonth()+1)}-${p(d.getDate())}`;
      };
      for (let ti = 0; ti <= 4; ti++) {
        const ts  = xMin + (xRange / 4) * ti;
        const tx  = toX(ts);
        const lbl = _pdfTick(ts);
        doc.text(lbl, tx - labelW / 2, cBot + 3, { width: labelW, align: 'center', lineBreak: false });
      }
      doc.fillColor('#000000');

      // Border
      doc.rect(cLeft, cTop, cW, CH).lineWidth(0.5).strokeColor('#cbd5e1').stroke();
      doc.lineWidth(1);

      // Lines
      lines.forEach(line => {
        const pts = line.pts;
        doc.save();
        doc.rect(cLeft, cTop, cW, CH).clip();
        doc.moveTo(toX(pts[0].x), toY(pts[0].y));
        for (let i = 1; i < pts.length; i++) doc.lineTo(toX(pts[i].x), toY(pts[i].y));
        doc.lineWidth(1.2).strokeColor(line.color || '#38bdf8').stroke();
        doc.restore();
      });

      // Legend
      let legX = cLeft;
      lines.forEach(line => {
        doc.rect(legX, cBot + xAxisH + 2, 10, 6).fill(line.color || '#38bdf8');
        doc.font('Helvetica').fontSize(7).fillColor('#334155')
           .text(line.label, legX + 13, cBot + xAxisH + 1, { lineBreak: false });
        legX += 13 + doc.widthOfString(line.label) + 16;
      });
      doc.fillColor('#000000');

      y = cBot + xAxisH + 18;
    }
  }

  // ── Table ─────────────────────────────────────────────────────────────
  const colW = Math.floor(inner / columns.length);
  const _drawTableHeader = yh => {
    doc.rect(L, yh, inner, 14).fill('#f1f5f9');
    doc.font('Helvetica-Bold').fontSize(8).fillColor('#0f172a');
    columns.forEach((col, i) => doc.text(col, L + i * colW + 3, yh + 3, { width: colW - 4, lineBreak: false }));
    doc.fillColor('#000000');
  };
  _drawTableHeader(y);
  y += 14;

  doc.font('Helvetica').fontSize(7.5);
  let rowIdx = 0;
  for (const row of rows) {
    if (y > doc.page.height - 50) {
      doc.addPage();
      y = 40;
      _drawTableHeader(y);
      doc.font('Helvetica').fontSize(7.5);
      y += 14; rowIdx = 0;
    }
    if (rowIdx % 2 === 1) doc.rect(L, y, inner, 12).fill('#f8fafc').stroke();
    doc.fillColor('#334155');
    columns.forEach((col, i) => {
      const v = row[col] != null ? String(row[col]) : '';
      doc.text(v, L + i * colW + 3, y + 2, { width: colW - 4, lineBreak: false });
    });
    doc.fillColor('#000000');
    y += 12;
    rowIdx++;
  }

  doc.end();
}

function _tsFmt(ts) {
  if (!ts) return '';
  const tz = Settings.load().displayTimezone;
  if (tz) {
    return new Intl.DateTimeFormat('sv-SE', {
      timeZone: tz, year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
    }).format(new Date(ts)).replace('T', ' ');
  }
  return new Date(ts).toISOString().replace('T', ' ').slice(0, 19) + ' UTC';
}
function _fmtDuration(ms) {
  if (!ms || ms < 0) return '';
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) return h + 'h ' + m + 'm';
  if (m > 0) return m + 'm ' + sec + 's';
  return sec + 's';
}

// GET /api/reports/ping
app.get('/api/reports/ping', _requireAdmin, _scopeRouterId, (req, res) => {
  const { routerId, from, to, aggregate } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const rows = aggregate
    ? db.queryPingSamplesAgg(routerId, from, to, aggregate)
    : db.queryPingSamples(routerId, from, to);
  res.json({ ok: true, rows });
});

// GET /api/reports/ping/export
app.get('/api/reports/ping/export', _requireAdmin, _scopeRouterId, (req, res) => {
  const { routerId, from, to, aggregate } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const rows = aggregate
    ? db.queryPingSamplesAgg(routerId, from, to, aggregate)
    : db.queryPingSamples(routerId, from, to);
  const fmt  = (req.query.format || 'csv').toLowerCase();
  const cols  = ['ts', 'target', 'rtt_ms', 'loss_pct'];
  const label = rows.map(r => ({ ...r, ts: _tsFmt(r.ts) }));
  if (fmt === 'pdf') {
    const rtts   = rows.filter(r => r.rtt_ms != null).map(r => r.rtt_ms);
    const losses = rows.map(r => r.loss_pct);
    const avgRtt = rtts.length   ? (rtts.reduce((a,b)=>a+b,0)/rtts.length).toFixed(1) : '—';
    const maxRtt = rtts.length   ? Math.max(...rtts).toFixed(1) : '—';
    const avgLoss= losses.length ? (losses.reduce((a,b)=>a+b,0)/losses.length).toFixed(1) : '—';
    const uptime = losses.length ? ((losses.filter(l=>l<1).length/losses.length)*100).toFixed(1)+'%' : '—';
    const step   = rows.length > 150 ? Math.ceil(rows.length / 150) : 1;
    const sub    = rows.filter((_,i)=>i%step===0);
    const rtr    = Routers.getById(routerId);
    return _toPdf('Ping Stability Report', ['Timestamp', 'Target', 'RTT (ms)', 'Loss (%)'],
      label.map(r => ({ Timestamp: r.ts, Target: r.target, 'RTT (ms)': r.rtt_ms ?? '', 'Loss (%)': r.loss_pct })), res, {
        router: rtr ? (rtr.label || rtr.host) : routerId, from, to,
        stats: [
          { label: 'Uptime',   value: uptime },
          { label: 'Avg RTT',  value: avgRtt !== '—' ? avgRtt+' ms' : '—' },
          { label: 'Max RTT',  value: maxRtt !== '—' ? maxRtt+' ms' : '—' },
          { label: 'Avg Loss', value: avgLoss !== '—' ? avgLoss+'%' : '—' },
          { label: 'Samples',  value: rows.length.toLocaleString() },
        ],
        chartData: { yLabel: 'ms / %', lines: [
          { label: 'RTT ms',  color: '#38bdf8', pts: sub.filter(r=>r.rtt_ms!=null).map(r=>({ x:r.ts, y:r.rtt_ms })) },
          { label: 'Loss %',  color: '#f87171', pts: sub.map(r=>({ x:r.ts, y:r.loss_pct })) },
        ]},
      });
  }
  res.setHeader('Content-Type', 'text/csv');
  res.setHeader('Content-Disposition', 'attachment; filename="ping-report.csv"');
  res.send(_toCsv(label, cols));
});

// GET /api/reports/traffic
app.get('/api/reports/traffic', _requireAdmin, _scopeRouterId, (req, res) => {
  const { routerId, from, to, aggregate } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const iface = req.query.interface || '';
  if (iface) {
    const rows = aggregate
      ? db.queryTrafficSamplesAgg(routerId, iface, from, to, aggregate)
      : db.queryTrafficSamples(routerId, iface, from, to);
    return res.json({ ok: true, rows });
  }
  res.json({ ok: true, interfaces: db.queryTrafficInterfaces(routerId) });
});

// GET /api/reports/traffic/export
app.get('/api/reports/traffic/export', _requireAdmin, _scopeRouterId, (req, res) => {
  const { routerId, from, to, aggregate } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const iface = req.query.interface || '';
  if (!iface) return res.status(400).json({ ok: false, error: 'interface required for export' });
  const rows  = aggregate
    ? db.queryTrafficSamplesAgg(routerId, iface, from, to, aggregate)
    : db.queryTrafficSamples(routerId, iface, from, to);
  const fmt   = (req.query.format || 'csv').toLowerCase();
  const cols  = ['ts', 'interface', 'rx_mbps', 'tx_mbps'];
  const label = rows.map(r => ({ ...r, ts: _tsFmt(r.ts), rx_mbps: +r.rx_mbps.toFixed(1), tx_mbps: +r.tx_mbps.toFixed(1) }));
  if (fmt === 'pdf') {
    const rxs   = rows.map(r => r.rx_mbps);
    const txs   = rows.map(r => r.tx_mbps);
    const avgRx = rxs.length ? (rxs.reduce((a,b)=>a+b,0)/rxs.length).toFixed(1) : '—';
    const avgTx = txs.length ? (txs.reduce((a,b)=>a+b,0)/txs.length).toFixed(1) : '—';
    const peakRx= rxs.length ? Math.max(...rxs).toFixed(1) : '—';
    const peakTx= txs.length ? Math.max(...txs).toFixed(1) : '—';
    const step  = rows.length > 150 ? Math.ceil(rows.length / 150) : 1;
    const sub   = rows.filter((_,i)=>i%step===0);
    const rtr   = Routers.getById(routerId);
    return _toPdf('Traffic History Report', ['Timestamp', 'Interface', 'RX (Mbps)', 'TX (Mbps)'],
      label.map(r => ({ Timestamp: r.ts, Interface: r.interface, 'RX (Mbps)': r.rx_mbps, 'TX (Mbps)': r.tx_mbps })), res, {
        router: rtr ? (rtr.label || rtr.host) : routerId, from, to,
        stats: [
          { label: 'Peak RX', value: peakRx !== '—' ? peakRx+' Mbps' : '—' },
          { label: 'Peak TX', value: peakTx !== '—' ? peakTx+' Mbps' : '—' },
          { label: 'Avg RX',  value: avgRx  !== '—' ? avgRx +' Mbps' : '—' },
          { label: 'Avg TX',  value: avgTx  !== '—' ? avgTx +' Mbps' : '—' },
          { label: 'Samples', value: rows.length.toLocaleString() },
        ],
        chartData: { yLabel: 'Mbps', lines: [
          { label: 'RX Mbps', color: '#38bdf8', pts: sub.map(r=>({ x:r.ts, y:r.rx_mbps })) },
          { label: 'TX Mbps', color: '#4ade80', pts: sub.map(r=>({ x:r.ts, y:r.tx_mbps })) },
        ]},
      });
  }
  res.setHeader('Content-Type', 'text/csv');
  res.setHeader('Content-Disposition', 'attachment; filename="traffic-report.csv"');
  res.send(_toCsv(label, cols));
});

// GET /api/reports/bandwidth
app.get('/api/reports/bandwidth', _requireAdmin, _scopeRouterId, (req, res) => {
  const { routerId, from, to, aggregate } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const iface = req.query.interface || '';
  if (iface) {
    const rows = aggregate
      ? db.queryBandwidthSamplesAgg(routerId, iface, from, to, aggregate)
      : db.queryBandwidthSamples(routerId, iface, from, to);
    return res.json({ ok: true, rows });
  }
  res.json({ ok: true, interfaces: db.queryBandwidthInterfaces(routerId) });
});

// GET /api/reports/bandwidth/export
app.get('/api/reports/bandwidth/export', _requireAdmin, _scopeRouterId, (req, res) => {
  const { routerId, from, to, aggregate } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const iface = req.query.interface || '';
  if (!iface) return res.status(400).json({ ok: false, error: 'interface required for export' });
  const rows  = aggregate
    ? db.queryBandwidthSamplesAgg(routerId, iface, from, to, aggregate)
    : db.queryBandwidthSamples(routerId, iface, from, to);
  const fmt   = (req.query.format || 'csv').toLowerCase();
  const cols  = ['ts', 'interface', 'rx_mb', 'tx_mb'];
  const label = rows.map(r => ({ ...r, ts: _tsFmt(r.ts), rx_mb: +r.rx_mb.toFixed(1), tx_mb: +r.tx_mb.toFixed(1) }));
  if (fmt === 'pdf') {
    const rxs    = rows.map(r => r.rx_mb);
    const txs    = rows.map(r => r.tx_mb);
    const totalRx= rxs.reduce((a,b)=>a+b,0).toFixed(1);
    const totalTx= txs.reduce((a,b)=>a+b,0).toFixed(1);
    const peakRx = rxs.length ? Math.max(...rxs).toFixed(1) : '—';
    const peakTx = txs.length ? Math.max(...txs).toFixed(1) : '—';
    const step   = rows.length > 150 ? Math.ceil(rows.length / 150) : 1;
    const sub    = rows.filter((_,i)=>i%step===0);
    const rtr    = Routers.getById(routerId);
    return _toPdf('Bandwidth Usage Report', ['Timestamp', 'Interface', 'Download (MB)', 'Upload (MB)'],
      label.map(r => ({ Timestamp: r.ts, Interface: r.interface, 'Download (MB)': r.rx_mb, 'Upload (MB)': r.tx_mb })), res, {
        router: rtr ? (rtr.label || rtr.host) : routerId, from, to,
        stats: [
          { label: 'Total Download', value: totalRx+' MB' },
          { label: 'Total Upload',   value: totalTx+' MB' },
          { label: 'Peak Download',  value: peakRx !== '—' ? peakRx+' MB' : '—' },
          { label: 'Peak Upload',    value: peakTx !== '—' ? peakTx+' MB' : '—' },
          { label: 'Samples',        value: rows.length.toLocaleString() },
        ],
        chartData: { yLabel: 'MB/min', lines: [
          { label: 'Download MB', color: '#38bdf8', pts: sub.map(r=>({ x:r.ts, y:r.rx_mb })) },
          { label: 'Upload MB',   color: '#4ade80', pts: sub.map(r=>({ x:r.ts, y:r.tx_mb })) },
        ]},
      });
  }
  res.setHeader('Content-Type', 'text/csv');
  res.setHeader('Content-Disposition', 'attachment; filename="bandwidth-report.csv"');
  res.send(_toCsv(label, cols));
});

// GET /api/reports/alerts
app.get('/api/reports/alerts', _requireAdmin, _scopeRouterId, (req, res) => {
  const { routerId, from, to } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  res.json({ ok: true, rows: db.queryAlertEvents(routerId, from, to) });
});

// GET /api/reports/alerts/export
app.get('/api/reports/alerts/export', _requireAdmin, _scopeRouterId, (req, res) => {
  const { routerId, from, to } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const rows  = db.queryAlertEvents(routerId, from, to);
  const fmt   = (req.query.format || 'csv').toLowerCase();
  const cols  = ['fired_at', 'alert_type', 'subject', 'detail', 'resolved_at', 'down_time'];
  const label = rows.map(r => ({
    ...r,
    fired_at:    _tsFmt(r.fired_at),
    resolved_at: _tsFmt(r.resolved_at),
    down_time:   r.resolved_at ? _fmtDuration(r.resolved_at - r.fired_at) : '',
  }));
  if (fmt === 'pdf') {
    const open     = rows.filter(r => !r.resolved_at).length;
    const resolved = rows.filter(r =>  r.resolved_at).length;
    const typeCounts = {};
    rows.forEach(r => { typeCounts[r.alert_type] = (typeCounts[r.alert_type]||0)+1; });
    const topEntry = Object.entries(typeCounts).sort((a,b)=>b[1]-a[1])[0];
    const rtr = Routers.getById(routerId);
    return _toPdf('Alert Events Report', ['Fired At', 'Type', 'Subject', 'Detail', 'Resolved At', 'Down Time'],
      label.map(r => ({ 'Fired At': r.fired_at, Type: r.alert_type, Subject: r.subject || '', Detail: r.detail || '', 'Resolved At': r.resolved_at, 'Down Time': r.down_time || '—' })), res, {
        router: rtr ? (rtr.label || rtr.host) : routerId, from, to,
        stats: [
          { label: 'Total',    value: rows.length.toLocaleString() },
          { label: 'Open',     value: open.toLocaleString() },
          { label: 'Resolved', value: resolved.toLocaleString() },
          { label: 'Top Type', value: topEntry ? topEntry[0] : '—' },
        ],
      });
  }
  res.setHeader('Content-Type', 'text/csv');
  res.setHeader('Content-Disposition', 'attachment; filename="alerts-report.csv"');
  res.send(_toCsv(label, cols));
});

// GET /api/reports/connectivity
app.get('/api/reports/connectivity', _requireAdmin, _scopeRouterId, (req, res) => {
  const { routerId, from, to, aggregate } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  if (aggregate) return res.json({ ok: true, rows: db.queryConnectivityEventsAgg(routerId, from, to, aggregate) });
  const rows = db.queryConnectivityEvents(routerId, from, to);
  // Pair each Offline row with the next Online row to compute outage duration
  for (let i = 0; i < rows.length; i++) {
    if (!rows[i].connected) {
      const next = rows.slice(i + 1).find(r => r.connected);
      rows[i].downtime_ms = next ? next.ts - rows[i].ts : null; // null = still offline
    } else {
      rows[i].downtime_ms = null;
    }
  }
  res.json({ ok: true, rows });
});

// GET /api/reports/connectivity/export
app.get('/api/reports/connectivity/export', _requireAdmin, _scopeRouterId, (req, res) => {
  const { routerId, from, to } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const rows = db.queryConnectivityEvents(routerId, from, to);
  for (let i = 0; i < rows.length; i++) {
    if (!rows[i].connected) {
      const next = rows.slice(i + 1).find(r => r.connected);
      rows[i].downtime_ms = next ? next.ts - rows[i].ts : null;
    } else {
      rows[i].downtime_ms = null;
    }
  }
  const fmt  = (req.query.format || 'csv').toLowerCase();
  const cols = ['ts', 'status', 'down_duration'];
  const label = rows.map(r => ({
    ts:           _tsFmt(r.ts),
    status:       r.connected ? 'Online' : 'Offline',
    down_duration: (!r.connected && r.downtime_ms != null) ? _fmtDuration(r.downtime_ms)
                 : (!r.connected)                          ? 'Ongoing'
                 : '',
  }));
  if (fmt === 'pdf') {
    const offlineRows   = rows.filter(r => !r.connected);
    const resolvedMs    = offlineRows.filter(r => r.downtime_ms != null).map(r => r.downtime_ms);
    const totalDownMs   = resolvedMs.reduce((a, b) => a + b, 0);
    const longestDownMs = resolvedMs.length ? Math.max(...resolvedMs) : null;
    const rtr = Routers.getById(routerId);
    return _toPdf('Connectivity Report', ['Timestamp', 'Status', 'Down Duration'],
      label.map(r => ({ Timestamp: r.ts, Status: r.status, 'Down Duration': r.down_duration || '—' })), res, {
        router: rtr ? (rtr.label || rtr.host) : routerId, from, to,
        stats: [
          { label: 'Total Events',   value: rows.length.toLocaleString() },
          { label: 'Offline Events', value: offlineRows.length.toLocaleString() },
          { label: 'Total Downtime', value: totalDownMs ? _fmtDuration(totalDownMs) : '—' },
          { label: 'Longest Outage', value: longestDownMs != null ? _fmtDuration(longestDownMs) : '—' },
        ],
      });
  }
  res.setHeader('Content-Type', 'text/csv');
  res.setHeader('Content-Disposition', 'attachment; filename="connectivity-report.csv"');
  res.send(_toCsv(label, cols));
});

// ── Socket.IO ─────────────────────────────────────────────────────────────────
function _buildRoutersStats() {
  const allRouters  = Routers.loadAll();
  const bgSummaries = overviewSessions.getSummaries();
  const cfg         = Settings.load();

  return allRouters.map(r => {
    const mainEntry = _routerSessions.get(r.id);
    const s         = mainEntry && mainEntry.session;
    const bg        = bgSummaries.find(x => x.routerId === r.id);
    const defaultIf = r.defaultIf || cfg.defaultIf || 'ether1';

    const connected = s ? !!mainEntry.rosConnected : (bg ? bg.connected : false);
    const sysPay    = s ? s.system.lastPayload    : (bg ? bg.systemPayload   : null);
    const ifPay     = s ? s.ifStatus.lastPayload  : (bg ? bg.ifStatusPayload : null);
    const wanIf     = ifPay ? (ifPay.interfaces || []).find(i => i.name === defaultIf) : null;

    return {
      id:        r.id,
      label:     r.label || r.host,
      host:      r.host,
      isActive:  !!s,
      connected,
      cpu:       sysPay ? sysPay.cpuLoad   : null,
      uptime:    sysPay ? sysPay.uptimeRaw : null,
      memPct:    sysPay ? sysPay.memPct    : null,
      hddPct:    sysPay ? sysPay.hddPct    : null,
      version:   sysPay ? sysPay.version   : null,
      boardName:    sysPay ? sysPay.boardName    : null,
      arch:         sysPay ? sysPay.arch         : null,
      serial:       sysPay ? sysPay.serial       : null,
      licenseLevel: sysPay ? sysPay.licenseLevel : null,
      rxMbps:    wanIf  ? wanIf.rxMbps     : null,
      txMbps:    wanIf  ? wanIf.txMbps     : null,
      clients:   (() => {
        const lp = s ? s.dhcpLeases.lastPayload : (bg ? bg.dhcpLeasesPayload : null);
        return lp ? lp.leases.length : null;
      })(),
    };
  });
}

async function sendInitialState(socket, entry) {
  // No router configured yet — prompt the browser to show the setup wizard.
  if (_noRouterMode) {
    socket.emit('setup:required', {});
    socket.emit('routers:update', []);
    return;
  }

  const s = entry.session;
  const _ps = Settings.load(); // single load — used for routers, settings:pages, pingEnabled

  socket.emit('traffic:history', {
    ifName: s.DEFAULT_IF,
    windowMinutes: s.HISTORY_MINUTES,
    points: s.traffic.hist.get(s.DEFAULT_IF) ? s.traffic.hist.get(s.DEFAULT_IF).toArray() : [],
  });

  // Send current router list and personal active router id
  socket.emit('routers:update', _routersForSocket(socket));
  socket.emit('router:active', { activeId: s.routerId });
  // Send live reachability status for this router and all alert-session routers
  socket.emit('router:status', { routerId: s.routerId, connected: entry.rosConnected });
  for (const [routerId, connected] of alertSessions.getStatusMap()) {
    socket.emit('router:status', { routerId, connected });
  }

  if (!s.ros.connected) {
    socket.emit('ros:status', { connected: false, reason: entry.rosConnected === false
      ? 'RouterOS is not connected — retrying in background'
      : 'Waiting for RouterOS connection…' });
    try { await s.ros.waitUntilConnected(10000); } catch (_) {}
  }

  let ifs = [];
  try {
    if (!s._ifacesFetch) s._ifacesFetch = fetchInterfaces(s.ros);
    s.cachedInterfaces = await s._ifacesFetch;
    ifs = s.cachedInterfaces;
    s.traffic.setAvailableInterfaces(ifs);
  } catch (e) {
    const reason = sanitizeErr(e);
    console.error('[MikroDash] fetchInterfaces failed for socket', socket.id, ':', reason);
    socket.emit('interfaces:error', { reason });
  }
  socket.emit('interfaces:list', { defaultIf: s.DEFAULT_IF, interfaces: ifs });

  let _wanIp = s.state.lastWanIp || '';
  if (!_wanIp && s.ifStatus.lastPayload) {
    const _wanIface = (s.DEFAULT_IF || '').toLowerCase();
    const _match = (s.ifStatus.lastPayload.interfaces || [])
      .find(i => i.name && i.name.toLowerCase() === _wanIface && i.ips && i.ips.length);
    if (_match) _wanIp = _match.ips[0];
  }
  if (s.dhcpNetworks.lastPayload) {
    socket.emit('lan:overview', s.dhcpNetworks.lastPayload);
  } else {
    socket.emit('lan:overview', {
      ts: Date.now(),
      lanCidrs: s.dhcpNetworks.getLanCidrs(),
      networks: s.dhcpNetworks.networks || [],
      wanIp: _wanIp,
      totalPoolSize: 0,
      totalLeases: 0,
      pollMs: s.dhcpNetworks.pollMs,
    });
  }

  const allLeases = [];
  for (const [ip, v] of s.dhcpLeases.byIP.entries()) allLeases.push({ ip, ...v });
  socket.emit('leases:list', { ts: Date.now(), leases: allLeases });

  if (s.traffic && s.traffic.lastWanStatus) socket.emit('wan:status', s.traffic.lastWanStatus);
  if (s.wireless.lastPayload)  socket.emit('wireless:update',  s.wireless.lastPayload);
  if (s.vpn.lastPayload)       socket.emit('vpn:update',       s.vpn.lastPayload);
  if (s.system.lastPayload)    socket.emit('system:update',    s.system.lastPayload);
  if (s.ifStatus.lastPayload)  socket.emit('ifstatus:update',  s.ifStatus.lastPayload);
  if (s.firewall.lastPayload)  socket.emit('firewall:update',  s.firewall.lastPayload);
  if (s.conns.lastPayload) {
    socket.emit('conn:update', s.conns.lastPayload);
    if (s.conns.lastPayload.sourceDests)
      socket.emit('conn:source-data', { ts: s.conns.lastPayload.ts, sourceDests: s.conns.lastPayload.sourceDests, sourcePorts: s.conns.lastPayload.sourcePorts });
  }
  if (s.talkers.lastPayload)   socket.emit('talkers:update',   s.talkers.lastPayload);
  if (s.ping.lastPayload)      socket.emit('ping:update',      s.ping.lastPayload);
  if (s.bandwidth.lastPayload) socket.emit('bandwidth:update', s.bandwidth.lastPayload);
  if (s.routing.lastPayload)   socket.emit('routing:update',   s.routing.lastPayload);
  if (s.netwatch.lastPayload)  socket.emit('netwatch:update',  s.netwatch.lastPayload);

  socket.emit('settings:pages', {
    pageWireless:_ps.pageWireless, pageInterfaces:_ps.pageInterfaces,
    pageDhcp:_ps.pageDhcp, pageVpn:_ps.pageVpn,
    pageConnections:_ps.pageConnections, pageFirewall:_ps.pageFirewall,
    pageLogs:_ps.pageLogs, pageBandwidth:_ps.pageBandwidth, pageRouting:_ps.pageRouting,
    alertCpuThreshold:_ps.alertCpuThreshold, alertPingLoss:_ps.alertPingLoss,
    vpnDashTopN:_ps.vpnDashTopN, pingEnabled:_ps.pingEnabled,
    notifIfaceUpDown:_ps.notifIfaceUpDown, notifVpn:_ps.notifVpn,
    notifCpu:_ps.notifCpu, notifPing:_ps.notifPing, notifNetwatch:_ps.notifNetwatch,
    notifRouterStatus:_ps.notifRouterStatus,
    notifIfaceEther:_ps.notifIfaceEther, notifIfaceWlan:_ps.notifIfaceWlan,
    notifIfaceBridge:_ps.notifIfaceBridge, notifIfaceVlan:_ps.notifIfaceVlan,
    notifIfaceOther:_ps.notifIfaceOther,
    displayTimezone:_ps.displayTimezone,
  });

  if (_ps.pingEnabled !== false) {
    const pingData = s.ping.getHistory();
    const pingLp = s.ping.lastPayload;
    if (pingData.history.length) socket.emit('ping:history', {
      ...pingData,
      minRtt: pingLp ? pingLp.minRtt : null,
      maxRtt: pingLp ? pingLp.maxRtt : null,
    });
  }

  const logHistory = s.logs.getHistory();
  if (logHistory.length) socket.emit('logs:history', logHistory);
}

function _idleSuspend(session, entry) {
  if (!session || !entry.startupReady) return;
  session.conns.suspend();
  session.ifStatus.suspend();
  session.system.suspend();
  session.wireless.suspend();
  session.vpn.suspend();
  session.firewall.suspend();
  session.routing.suspend();
  session.ping.suspend();
  session.talkers.suspend();
  session.dhcpNetworks.suspend();
}

function _idleResume(session, entry) {
  if (!session || !entry.startupReady) return;
  session.conns.resume();
  session.ifStatus.resume();
  session.system.resume();
  _updateWirelessStreams(session, entry);
  _updateVpnStreams(session, entry);
  _updateFirewallStreams(session, entry);
  _updateRoutingStreams(session, entry);
  session.ping.resume();
  session.talkers.resume();
  session.dhcpNetworks.resume();
}

// Sync firewall streams with page-firewall / dash-card-firewall room membership.
function _updateFirewallStreams(session, entry) {
  if (!session || !entry.startupReady) return;
  const rid = session.routerId;
  const viewers = (io.sockets.adapter.rooms.get('router-' + rid + '-page-firewall')?.size    || 0) +
                  (io.sockets.adapter.rooms.get('router-' + rid + '-dash-card-firewall')?.size || 0);
  if (viewers > 0) session.firewall.resume(); else session.firewall.suspend();
}

// Sync routing streams with page-routing room membership.
function _updateRoutingStreams(session, entry) {
  if (!session || !entry.startupReady) return;
  const rid = session.routerId;
  const viewers = io.sockets.adapter.rooms.get('router-' + rid + '-page-routing')?.size || 0;
  if (viewers > 0) session.routing.resume(); else session.routing.suspend();
}

// Sync wireless streams with page-wireless room membership.
function _updateWirelessStreams(session, entry) {
  if (!session || !entry.startupReady) return;
  const rid = session.routerId;
  const viewers = io.sockets.adapter.rooms.get('router-' + rid + '-page-wireless')?.size || 0;
  if (viewers > 0) session.wireless.resume(); else session.wireless.suspend();
}

// Sync vpn counter stream with page-vpn / dash-card-vpn room membership.
function _updateVpnStreams(session, entry) {
  if (!session || !entry.startupReady) return;
  const rid = session.routerId;
  const viewers = (io.sockets.adapter.rooms.get('router-' + rid + '-page-vpn')?.size     || 0) +
                  (io.sockets.adapter.rooms.get('router-' + rid + '-dash-card-vpn')?.size || 0);
  if (viewers > 0) session.vpn.resume(); else session.vpn.suspend();
}

function _emitDiagnostics(session, rid, socket) {
  const s = session;
  const countObj = o => o ? Object.values(o).filter(Boolean).length : 0;
  const collectors = [
    { name: 'traffic',      streams: s.traffic._allStream    ? 1 : 0 },
    { name: 'system',       streams: s.system._stream        ? 1 : 0 },
    { name: 'connections',  streams: s.conns._stream         ? 1 : 0 },
    { name: 'talkers',      streams: s.talkers._stream       ? 1 : 0 },
    { name: 'logs',         streams: s.logs._stream          ? 1 : 0 },
    { name: 'ping',         streams: s.ping._stream          ? 1 : 0 },
    { name: 'netwatch',     streams: s.netwatch._stream      ? 1 : 0 },
    { name: 'wireless',     streams: countObj(s.wireless._streams) },
    { name: 'vpn',          streams: (s.vpn._stream?1:0)+(s.vpn._counterStream?1:0) },
    { name: 'firewall',     streams: countObj(s.firewall._streams)+countObj(s.firewall._counterStreams) },
    { name: 'dhcpNetworks', streams: countObj(s.dhcpNetworks._streams) },
    { name: 'ifStatus',     streams: (s.ifStatus._ifStream?1:0)+(s.ifStatus._addrStream?1:0)+(s.ifStatus._monitorStream?1:0) },
    { name: 'routing',      streams: (s.routing._routeStream?1:0)+(s.routing._ipv6Stream?1:0)+(s.routing._bgpStream?1:0) },
  ];
  const total = collectors.reduce((sum, c) => sum + c.streams, 0);
  const payload = { ts: Date.now(), total, collectors };
  if (socket) {
    socket.emit('diagnostics:update', payload);
  } else {
    io.to('router-' + rid + '-dash-card-diagnostics').emit('diagnostics:update', payload);
  }
}

// Tracks which sockets are currently viewing the Routers page.
// Overview session collectors run only while this set is non-empty.
const _routersPageSockets = new Set();

io.on('connection', (socket) => {
  if (io.engine.clientsCount > MAX_SOCKETS) {
    console.warn('[MikroDash] connection rejected — max sockets reached:', MAX_SOCKETS);
    socket.disconnect(true);
    return;
  }

  // Resolve which router this socket should watch, cancel any pending idle
  // teardown for it, and ensure the session is running.
  const routerId = _resolveRouterId(socket);
  socket.routerId = routerId;

  if (routerId) {
    const existingEntry = _routerSessions.get(routerId);
    if (existingEntry && existingEntry.idleTimer) {
      clearTimeout(existingEntry.idleTimer);
      existingEntry.idleTimer = null;
    }
  }

  const entry = routerId ? ensureRouterSession(routerId) : null;

  if (routerId) socket.join('router-' + routerId);

  // Idle manager: resume streams/timers when the first socket joins this router's room.
  if (entry) {
    const roomSize = io.sockets.adapter.rooms.get('router-' + routerId)?.size || 0;
    if (roomSize === 1) _idleResume(entry.session, entry);
  }

  let _routersTimer = null;

  socket.on('disconnect', () => {
    if (_routersTimer) { clearInterval(_routersTimer); _routersTimer = null; }
    if (_routersPageSockets.delete(socket.id) && _routersPageSockets.size === 0) overviewSessions.suspend();
    const rid = socket.routerId;
    if (!rid) return;
    const e = _routerSessions.get(rid);
    if (!e) return;
    const roomSize = io.sockets.adapter.rooms.get('router-' + rid)?.size || 0;
    if (roomSize === 0) {
      _idleSuspend(e.session, e);
      scheduleIdleTeardown(rid);
    }
    // Rooms are cleaned up before this event fires, so room sizes are already correct.
    _updateFirewallStreams(e.session, e);
    _updateRoutingStreams(e.session, e);
    _updateWirelessStreams(e.session, e);
    _updateVpnStreams(e.session, e);
  });

  // Page-aware rooms — clients join/leave rooms as they navigate pages.
  socket.on('page:focus', (name) => {
    if (typeof name !== 'string' || !/^[a-z]{2,20}$/.test(name)) return;
    const rid = socket.routerId;
    socket.join('router-' + rid + '-page-' + name);
    if (name === 'routers') {
      if (!_routersPageSockets.has(socket.id)) {
        _routersPageSockets.add(socket.id);
        if (_routersPageSockets.size === 1) overviewSessions.resume();
      }
      if (_routersTimer) clearInterval(_routersTimer);
      const _emitRouters = () => socket.emit('routers:stats', _buildRoutersStats());
      _emitRouters();
      _routersTimer = setInterval(_emitRouters, 2000); // codeql[js/resource-exhaustion]
    }
    const e = rid ? _routerSessions.get(rid) : null;
    if (!e || !e.session) return;
    const s = e.session;
    if (name === 'firewall') {
      _updateFirewallStreams(s, e);
      if (s.firewall && s.firewall.lastPayload)
        socket.emit('firewall:update', { ...s.firewall.lastPayload, ts: Date.now() });
    }
    if (name === 'routing') {
      _updateRoutingStreams(s, e);
      if (s.routing && s.routing.lastPayload)
        socket.emit('routing:update', { ...s.routing.lastPayload, ts: Date.now() });
    }
    if (name === 'wireless') {
      _updateWirelessStreams(s, e);
      if (s.wireless && s.wireless.lastPayload)
        socket.emit('wireless:update', { ...s.wireless.lastPayload, ts: Date.now() });
    }
    if (name === 'vpn') {
      _updateVpnStreams(s, e);
      if (s.vpn && s.vpn.lastPayload)
        socket.emit('vpn:update', { ...s.vpn.lastPayload, ts: Date.now() });
    }
    if (name === 'bandwidth' && s.bandwidth && s.bandwidth.lastPayload)
      socket.emit('bandwidth:update', { ...s.bandwidth.lastPayload, ts: Date.now() });
    if (name === 'logs' && s.logs)
      socket.emit('logs:history', { entries: s.logs.getHistory() });
    if (name === 'connections' && s.conns && s.conns.lastPayload) {
      if (s.conns.lastPayload.countryDests)
        socket.emit('conn:country-data', { ts: s.conns.lastPayload.ts, countryDests: s.conns.lastPayload.countryDests, countryPorts: s.conns.lastPayload.countryPorts });
      if (s.conns.lastPayload.sourceDests)
        socket.emit('conn:source-data',  { ts: s.conns.lastPayload.ts, sourceDests:  s.conns.lastPayload.sourceDests, sourcePorts: s.conns.lastPayload.sourcePorts  });
    }
  });

  socket.on('page:blur', (name) => {
    if (typeof name !== 'string' || !/^[a-z]{2,20}$/.test(name)) return;
    const rid = socket.routerId;
    socket.leave('router-' + rid + '-page-' + name);
    const e = rid ? _routerSessions.get(rid) : null;
    if (!e) return;
    if (name === 'firewall')    _updateFirewallStreams(e.session, e);
    if (name === 'routing')     _updateRoutingStreams(e.session, e);
    if (name === 'wireless')    _updateWirelessStreams(e.session, e);
    if (name === 'vpn')         _updateVpnStreams(e.session, e);
    if (name === 'routers') {
      if (_routersTimer) { clearInterval(_routersTimer); _routersTimer = null; }
      if (_routersPageSockets.delete(socket.id) && _routersPageSockets.size === 0) overviewSessions.suspend();
    }
  });

  // Dashboard card rooms — emitted by dashboard-grid.js via custom DOM events
  // relayed through app.js when a room-gated card is visible on the dashboard.
  socket.on('dashcard:focus', (key) => {
    if (typeof key !== 'string' || !/^[a-z]{2,20}$/.test(key)) return;
    const rid = socket.routerId;
    socket.join('router-' + rid + '-dash-card-' + key);
    const e = rid ? _routerSessions.get(rid) : null;
    if (!e || !e.session) return;
    const s = e.session;
    if (key === 'firewall') {
      _updateFirewallStreams(s, e);
      if (s.firewall && s.firewall.lastPayload)
        socket.emit('firewall:update', { ...s.firewall.lastPayload, ts: Date.now() });
    }
    if (key === 'vpn') {
      _updateVpnStreams(s, e);
      if (s.vpn && s.vpn.lastPayload)
        socket.emit('vpn:update', { ...s.vpn.lastPayload, ts: Date.now() });
    }
    if (key === 'diagnostics') {
      _emitDiagnostics(s, rid, socket);
      if (!e._diagTimer) {
        e._diagTimer = setInterval(() => {
          const viewers = io.sockets.adapter.rooms.get('router-' + rid + '-dash-card-diagnostics')?.size || 0;
          if (!viewers) { clearInterval(e._diagTimer); e._diagTimer = null; return; }
          _emitDiagnostics(s, rid, null);
        }, 2000);
      }
    }
    if (key === 'bandwidth' && s.bandwidth && s.bandwidth.lastPayload)
      socket.emit('bandwidth:update', { ...s.bandwidth.lastPayload, ts: Date.now() });
    if (key === 'logs' && s.logs)
      socket.emit('logs:history', { entries: s.logs.getHistory() });
  });

  socket.on('dashcard:blur', (key) => {
    if (typeof key !== 'string' || !/^[a-z]{2,20}$/.test(key)) return;
    const rid = socket.routerId;
    socket.leave('router-' + rid + '-dash-card-' + key);
    const e = rid ? _routerSessions.get(rid) : null;
    if (!e) return;
    if (key === 'firewall') _updateFirewallStreams(e.session, e);
    if (key === 'vpn')      _updateVpnStreams(e.session, e);
    if (key === 'diagnostics') {
      const viewers = io.sockets.adapter.rooms.get('router-' + rid + '-dash-card-diagnostics')?.size || 0;
      if (!viewers && e._diagTimer) { clearInterval(e._diagTimer); e._diagTimer = null; }
    }
  });

  // Per-user router switching (modern auth only).
  socket.on('router:switch', (newRouterId) => {
    // Re-resolve live role/perms (don't trust the ≤60s-stale cached view) so a
    // just-revoked viewer can't switch into a router they no longer have access to.
    const authSession = socket.request ? _sessionFromReq(socket.request) : null;
    if (!authSession) { socket.emit('session:expired'); return; }
    socket.request._authSession = authSession;

    if (typeof newRouterId !== 'string') return;
    const router = Routers.getById(newRouterId);
    if (!router) return;

    // Validate access: viewer can only switch to allowed routers
    if (authSession.role !== 'admin') {
      const allowed = authSession.allowedRouterIds || [];
      if (allowed.length > 0 && !allowed.includes(newRouterId)) return;
    }

    const oldRid = socket.routerId;
    if (oldRid === newRouterId) return;

    // Leave old router room (and all its sub-rooms)
    for (const room of [...socket.rooms]) {
      if (room.startsWith('router-' + oldRid)) socket.leave(room);
    }

    // Schedule idle teardown for old router if nobody is watching it anymore
    if (oldRid) scheduleIdleTeardown(oldRid);

    // Join new router room and cancel its idle timer
    const newEntry = _routerSessions.get(newRouterId);
    if (newEntry && newEntry.idleTimer) { clearTimeout(newEntry.idleTimer); newEntry.idleTimer = null; }

    socket.routerId = newRouterId;
    socket.join('router-' + newRouterId);

    // Idle-resume if this is the first socket on this router
    const roomSize = io.sockets.adapter.rooms.get('router-' + newRouterId)?.size || 0;
    const activeEntry = ensureRouterSession(newRouterId);
    if (roomSize === 1) _idleResume(activeEntry.session, activeEntry);

    // Persist preference in session store
    const token = SessionStore.parseCookieHeader(socket.request?.headers?.cookie || '')['mikrodash_sid'];
    if (token) SessionStore.updateSession(token, { activeRouterId: newRouterId });

    // Rebind traffic and replay initial state
    activeEntry.session.traffic.bindSocket(socket);
    sendInitialState(socket, activeEntry)
      .then(() => socket.emit('router:switched', { activeId: newRouterId }))
      .catch(() => {});
  });

  if (entry) entry.session.traffic.bindSocket(socket);

  // Session expiry / revocation for connected sockets is handled by a single
  // shared sweep (see _startSessionSweep), not a per-socket timer.

  // Kick idle-gated collectors that haven't run yet, then send initial state.
  const kickAndSend = async () => {
    if (entry) {
      const idleGated = [entry.session.conns, entry.session.bandwidth, entry.session.talkers];
      const kicks = idleGated
        .filter(c => c && !c.lastPayload && typeof c.tick === 'function')
        .map(c => c.tick(true).catch(() => {}));
      if (kicks.length) await Promise.allSettled(kicks);
    }
    if (entry) await sendInitialState(socket, entry);
    else {
      socket.emit('setup:required', {});
      socket.emit('routers:update', []);
    }
  };
  kickAndSend().catch(() => {});
});

const PORT = parseInt(process.env.PORT || '3081', 10);
server.listen(PORT, () => console.log(`[MikroDash] v${APP_VERSION} listening on http://0.0.0.0:${PORT}`));

// ── Graceful shutdown ─────────────────────────────────────────────────────────
function shutdown(signal) {
  console.log(`[MikroDash] ${signal} received, shutting down…`);
  SessionStore.shutdown();
  for (const [, entry] of _routerSessions) {
    if (entry.idleTimer) { clearTimeout(entry.idleTimer); entry.idleTimer = null; }
    if (entry.session) {
      for (const c of entry.session.allCollectors) {
        if (typeof c.stop === 'function') c.stop();
      }
      entry.session.ros.stop();
    }
  }
  dbWriter.flushTraffic();
  db.close();
  io.close();
  server.close(() => {
    console.log('[MikroDash] HTTP server closed');
    process.exit(0);
  });
  scheduleForcedShutdownTimer(() => {
    console.error('[MikroDash] Forceful shutdown after timeout');
    process.exit(1);
  }, 5000);
}
process.on('SIGTERM', () => shutdown('SIGTERM'));
process.on('SIGINT',  () => shutdown('SIGINT'));
process.on('unhandledRejection', (err) => {
  console.error('[MikroDash] unhandledRejection:', err);
});
