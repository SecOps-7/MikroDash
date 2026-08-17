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
    // Merge the timestamp INTO the caller's format string rather than passing it
    // as a separate leading argument. Passing it separately made the timestamp
    // itself the format string, so every caller's specifiers were left
    // unsubstituted — 'Telegram error: %s' printed literally as "%s boom".
    // A non-string first argument (an object, an Error) has no format string to
    // merge into, so it keeps the old shape.
    console[level] = (...args) => (typeof args[0] === 'string'
      ? orig(`[${ts()}] ${args[0]}`, ...args.slice(1))
      : orig(`[${ts()}]`, ...args));
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
const { classifyRosError } = require('./routeros/classifyError');
const { scheduleForcedShutdownTimer } = require('./shutdown');

try {
  verifyRouterOSPatchMarkers({ readFileSync: fs.readFileSync });
} catch (_error) {
  console.error('[MikroDash] Run: node patch-routeros.js');
  process.exit(1);
}

const geo = require('./geo');
const cityIndex = require('./cityIndex');
const GeoPlace  = require('./geoPlace');

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
const { resolveCollection, collectionFingerprint, planMigration,
        LEGACY_STREAM_KEYS } = require('./collection');
const { makeNullCollector } = require('./collectors/nullCollector');
const WirelessCollector    = require('./collectors/wireless');
const VpnCollector         = require('./collectors/vpn');
const FirewallCollector    = require('./collectors/firewall');
const InterfaceStatusCollector = require('./collectors/interfaceStatus');
const PingCollector         = require('./collectors/ping');
const BandwidthCollector    = require('./collectors/bandwidth');
const RoutingCollector      = require('./collectors/routing');
const NetwatchCollector     = require('./collectors/netwatch');
const TopologyCollector     = require('./collectors/topology');
const alerter               = require('./alerter');
const notifier              = require('./notifier');
const alertSessions         = require('./alertSessions');
const overviewSessions      = require('./overviewSessions');
const SessionStore          = require('./auth/sessionStore');
const Users                 = require('./users');
const db                    = require('./db');
const Rbac                  = require('./rbac');
const Pages                 = require('./pages');
const dbWriter              = require('./db-writer');
const userNotify            = require('./userNotify');

const compression = require('compression');
const app = express();

const TRUSTED_PROXY = process.env.TRUSTED_PROXY;
if (TRUSTED_PROXY === 'true' || TRUSTED_PROXY === '1') {
  // 'true' would trust the entire X-Forwarded-For chain, letting any client spoof
  // its IP and defeat the login/setup rate limiters. Trust exactly one hop instead.
  if (TRUSTED_PROXY === 'true') console.warn('[MikroDash] TRUSTED_PROXY=true is unsafe — trusting only the first proxy hop. Set an IP/CIDR or hop count to silence this.');
  app.set('trust proxy', 1);
} else if (TRUSTED_PROXY) {
  app.set('trust proxy', /^\d+$/.test(TRUSTED_PROXY) ? parseInt(TRUSTED_PROXY, 10) : TRUSTED_PROXY);
}

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
  // Handlers collectors register via on() land on the process-global `io`;
  // track them so teardownSession can remove them — otherwise every hot-swap
  // leaks listeners that retain the whole dead session.
  const _handlers = [];
  // Room-scoped emits must still feed the alerter, or events that only ever go
  // through to() (e.g. vpn:update) can never trigger notifications.
  // Side effects of an emit, independent of who receives it. Persisting a ping
  // sample and evaluating alerts are consequences of the payload existing, not
  // of its delivery scope — recordPing used to sit on the router-wide emit()
  // alone, so the moment ping:update became page-scoped (issue #108) history
  // would have stopped being written, silently, with the Ping report going
  // empty and nothing logging an error.
  const _hook = (event, data) => {
    if (event === 'ping:update' && data && typeof data.loss === 'number') {
      dbWriter.recordPing(routerId, data.target, data.rtt != null ? data.rtt : null, data.loss, data.ts);
    }
    alerter.evaluateForRouter(routerId, event, data);
  };
  return {
    emit(event, data) {
      io.to(room).emit(event, data);
      _hook(event, data);
    },
    // Recursively chainable, so .to(a).to(b).to(c) works. This used to hardcode
    // exactly two levels: the second .to() returned an object with only emit(),
    // so a third threw "to is not a function" at runtime — invisible until a
    // collector needed to reach three rooms (ifstatus does, issue #108).
    to(subRoom) {
      const _chain = (rooms) => ({
        to(r) { return _chain(rooms.concat(r)); },
        emit(event, data) {
          let op = io;
          for (const r of rooms) op = op.to(room + '-' + r);
          op.emit(event, data);
          _hook(event, data);
        },
      });
      return _chain([subRoom]);
    },
    // Collectors may call io.on('connection', ...) to restart streams on reconnect.
    on(event, handler)   { _handlers.push([event, handler]); io.on(event, handler); },
    removeAllHandlers()  { for (const [ev, h] of _handlers) io.off(ev, h); _handlers.length = 0; },
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

// Single source of truth for the effective auth mode. Only 'none' and 'modern'
// exist; anything else (including a falsy/legacy value) resolves to 'modern' so
// no call site can accidentally fail open by defaulting to the removed 'basic'.
function _authMode() {
  return Settings.load().authMode === 'none' ? 'none' : 'modern';
}
function _isModern() { return _authMode() === 'modern'; }

function _authMiddleware(req, res, next) {
  if (_authMode() === 'none') return next();
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

// Authorization lives in src/rbac.js. _requireAdmin, _routerPermitted and
// _scopeRouterId used to sit here; all three are gone, deliberately rather than
// left dormant. Each carried its own copy of the "in 'none' auth mode everything
// is implicitly admin" short circuit, and each read allowedRouterIds directly,
// where an empty array meant UNRESTRICTED. A second authorization helper left
// alive is one a future route reaches for, quietly bypassing the grant model —
// so the only way to answer "may this caller do this?" is now Rbac.can().

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
  if (_authMode() === 'none') return next();
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
    if (!_isModern()) return;
    for (const [, socket] of io.sockets.sockets) {
      const live = _sessionFromReq(socket.request);
      if (!live) {
        socket.emit('session:expired');
        socket.disconnect(true);
      } else {
        socket.request._authSession = live; // refresh live role/grants
        // Revocation used to take effect only on the next page load, so a
        // socket kept streaming a router its owner had just lost. Room
        // membership is the actual data boundary, so leaving the rooms is what
        // stops the data — the notice is only so the page can explain itself.
        if (socket.routerId && !_socketCan(socket, 'router:read', socket.routerId)) {
          for (const room of socket.rooms) {
            if (room.startsWith('router-')) socket.leave(room);
          }
          socket.routerId = '';
          socket.emit('access:revoked');
        }
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
  alertSessions.syncSessions(Routers.loadAll().filter(r => !r.disabled), Settings.load().activeRouterId || '', _poolOwnedIds());
}

function _syncOverviewSessions() {
  overviewSessions.syncSessions(Routers.loadAll().filter(r => !r.disabled), _poolOwnedIds());
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
    lastIfStatusTs:0, lastIfStatusErr:null,
    lastPingTs:0,
    lastRoutingTs:0,  lastRoutingErr:null,
    lastBandwidthTs:0, lastBandwidthErr:null,
    lastNetwatchTs:0, lastNetwatchErr:null,
    lastTopologyTs:0, lastTopologyErr:null,
  };
}

function buildSession(routerCfg, routerIo) {
  const _cfg   = Settings.load();
  // Per-router collection config (#105). Intervals inherit from _cfg; delivery
  // (stream vs poll) and enable/disable are per-router. Resolving once here is
  // what makes every collector below honour this router's own settings.
  const eff    = resolveCollection(_cfg, routerCfg);
  // A disabled collector must never be CONSTRUCTED: 11 of the 16 open their
  // streams from a ros.on('connected') handler in the constructor, so skipping
  // start() would not stop them. makeNullCollector stands in on the session.
  const _on    = (key, build) => eff.enabled[key] ? build() : makeNullCollector(key);
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

  const dhcpLeases   = new DhcpLeasesCollector ({ros, io:routerIo, state, pollMs:eff.poll.dhcpLeases, streamMode:eff.stream.dhcpLeases});
  const arp          = new ArpCollector         ({ros,              pollMs:eff.poll.arp,       state, streamMode:eff.stream.arp});
  const dhcpNetworks = new DhcpNetworksCollector({ros, io:routerIo, pollMs:eff.poll.dhcpNetworks, dhcpLeases, state, wanIface:DEFAULT_IF, streamMode:eff.stream.dhcpNetworks});
  const traffic      = new TrafficCollector     ({ros, io:routerIo, defaultIf:DEFAULT_IF, historyMinutes:HISTORY_MINUTES, pollMs:1000, state,
    onSample: (ifName, rxMbps, txMbps, ts) => dbWriter.recordTraffic(routerCfg.id, ifName, rxMbps, txMbps, ts)});
  // Backfill ring buffer from SQLite so the chart has history on first browser connect
  // (covers both server restarts and sessions where no browser was open during recording).
  const _histFromTs = Date.now() - HISTORY_MINUTES * 60 * 1000;
  const _histRows   = db.queryTrafficSamples(routerCfg.id, DEFAULT_IF, _histFromTs, Date.now(), traffic.maxPoints);
  if (_histRows.length) traffic.preloadHistory(DEFAULT_IF, _histRows);
  const conns        = _on('conns', () => new ConnectionsCollector ({ros, io:routerIo, pollMs:eff.poll.conns,    topN:_cfg.topN, maxConns:_cfg.maxConns, dhcpNetworks, dhcpLeases, arp, state, connTableCache, geoOrgCache, streamMode:eff.stream.conns}));
  const talkers      = _on('talkers', () => new TopTalkersCollector  ({ros, io:routerIo, pollMs:eff.poll.talkers,  state, topN:_cfg.topTalkersN, streamMode:eff.stream.talkers}));
  const logs         = _on('logs', () => new LogsCollector        ({ros, io:routerIo, state}));
  // Read live rather than captured: alertsEnabled can be toggled without
  // rebuilding the session. The alerter only sees events that are actually
  // emitted, so the three collectors feeding CPU / ping / interface alerts must
  // not be idle-gated while alerts are on. Non-active routers already behave
  // this way via the stubbed clientsCount in alertSessions.
  const _alertsActive = () => {
    const r = Routers.getById(routerCfg.id);
    return !!(r && r.alertsEnabled);
  };
  const system       = new SystemCollector      ({ros, io:routerIo, pollMs:eff.poll.system,   state, streamMode:eff.stream.system, alertsActive:_alertsActive});
  const wireless     = _on('wireless', () => new WirelessCollector    ({ros, io:routerIo, pollMs:eff.poll.wireless, state, dhcpLeases, arp, streamMode:eff.stream.wireless}));
  const vpn          = _on('vpn', () => new VpnCollector         ({ros, io:routerIo, pollMs:eff.poll.vpn,      state, rid:routerCfg.id, streamMode:eff.stream.vpn}));
  const firewall     = _on('firewall', () => new FirewallCollector    ({ros, io:routerIo, pollMs:eff.poll.firewall,  state, streamMode:eff.stream.firewall}));
  const ifStatus     = _on('ifStatus', () => new InterfaceStatusCollector({ros, io:routerIo, pollMs:eff.poll.ifStatus, metaPollMs:eff.poll.ifaces, state, streamMode:eff.stream.ifStatus, alertsActive:_alertsActive, rid:routerCfg.id}));
  const ping         = _on('ping', () => new PingCollector        ({ros, io:routerIo, pollMs:eff.poll.ping,     state, target:PING_TARGET, streamMode:eff.stream.ping, alertsActive:_alertsActive}));
  const bandwidth    = _on('bandwidth', () => new BandwidthCollector   ({ros, io:routerIo, pollMs:eff.poll.bandwidth, dhcpNetworks, dhcpLeases, arp, ifStatus, state, connTableCache, geoOrgCache}));
  const routing      = _on('routing', () => new RoutingCollector     ({ros, io:routerIo, pollMs:eff.poll.routing,  state, streamMode:eff.stream.routing}));
  const netwatch     = _on('netwatch', () => new NetwatchCollector    ({ros, io:routerIo, pollMs:eff.poll.netwatch,  state, streamMode:eff.stream.netwatch}));
  // Constructed last: it enriches neighbours from arp/ifStatus/system rather than
  // re-fetching what they already hold, so it must come after all three.
  const topology     = _on('topology', () => new TopologyCollector    ({ros, io:routerIo, pollMs:eff.poll.topology,  state, streamMode:eff.stream.topology, rid:routerCfg.id, arp, ifStatus, system, dhcpLeases}));

  const allCollectors = [traffic, dhcpLeases, dhcpNetworks, arp, conns, talkers, logs, system, wireless, vpn, firewall, ifStatus, ping, bandwidth, routing, netwatch, topology];

  return { ros, state, connTableCache, DEFAULT_IF, HISTORY_MINUTES, collection: eff,
           dhcpLeases, dhcpNetworks, arp, traffic, conns, talkers, logs, system,
           wireless, vpn, firewall, ifStatus, ping, bandwidth, routing, netwatch, topology, allCollectors,
           routerId: routerCfg.id, cachedInterfaces: null };
}

// ── Session teardown ──────────────────────────────────────────────────────────
// Stop all collectors and the ROS connection. `entry` is the _routerSessions entry.
async function teardownSession(session, entry) {
  if (!session) return;
  const _tearLabel = (session.ros && session.ros.routerLabel) || 'router';
  console.log('%s', `[${_tearLabel}] ── session torn down`);
  if (entry) { entry.startupReady = false; entry.collectorsStarted = false; }
  if (entry && entry._diagTimer) { clearInterval(entry._diagTimer); entry._diagTimer = null; }
  // Detach collector-registered global io listeners so the dead session can be GC'd.
  if (entry && entry.routerIo && typeof entry.routerIo.removeAllHandlers === 'function') {
    entry.routerIo.removeAllHandlers();
  }
  // Mark the session dead BEFORE stopping the ROS connection. ros.stop() closes
  // the socket, and the resulting 'close' event arrives asynchronously, after the
  // cancel below, so without this flag it re-enters _emitRouterStatus() and starts
  // a fresh offline-debounce timer that nobody cancels. That timer then recorded a
  // phantom outage (and fired a router-down alert) about 30 s after every router
  // switch or idle teardown, for a router that was never unreachable. See #84.
  session._destroyed = true;
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
    // Session torn down (router switch, idle teardown, disable, delete): the
    // disconnect is our own doing, not a router outage, so it must not reach the
    // connectivity log or the alerter. Same guard alertSessions.js already uses.
    if (session._destroyed) return;
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
        if (_prevConnected !== false) {
          // Must mirror the debounce branch below: the recovery path is guarded
          // on _declaredOffline, so leaving it false here meant a router with
          // connDownThresholdSec = 0 opened a connectivity alert that could
          // never be resolved. Set inside the same condition that fires the
          // alert, so recovery cannot emit an unpaired "online".
          _declaredOffline = true;
          alerter.fireConnectivityAlert(session.routerId, label, false);
        }
        _prevConnected = false;
        return;
      }
      // The outage started now, not when the debounce expires. Record the
      // observed time so downtime is not under-reported by threshMs (#99).
      const downAt = Date.now();
      _downTimer = setTimeout(() => {
        _downTimer      = null;
        _declaredOffline = true;
        _prevConnected  = false;
        io.emit('router:status', { routerId: session.routerId, connected: false });
        dbWriter.recordConnectivity(session.routerId, false, downAt);
        alerter.fireConnectivityAlert(session.routerId, label, false);
      }, threshMs);
    }
  }

  ros.on('connected', () => {
    console.log('%s', `[${ros.routerLabel}][ROS] ✓ connected to ${host}:${port} as "${user}" (${tls ? 'TLS' : 'plain'})`);
    session.cachedInterfaces = null; // invalidate on reconnect — interfaces may have changed
    session._ifacesFetch    = null;
    if (entry) { entry.lastError = null; entry.lastErrorTs = 0; } // recovered (#92)
    broadcastRosStatus(true, null, entry);
    _emitRouterStatus(true);
    // Restore page-aware streams for any pages still open after the reconnect.
    // Collector reconnect handlers (in constructors) fire before this listener
    // and call suspend() to clear state first.
    session.conns.resume();
    _updateAllPageStreams(session, entry);
  });
  ros.on('close', () => {
    session.connTableCache.invalidate();
    console.log('%s', `[${ros.routerLabel}][ROS] connection to ${host}:${port} closed`);
    broadcastRosStatus(false, 'RouterOS connection closed', entry);
    _emitRouterStatus(false);
  });
  ros.on('connectionError', (e) => {
    const { reason, hint, msg, classified } = classifyRosError(e, { host, port, user, tls });
    console.error('%s', `[${ros.routerLabel}][ROS] ✗ ${reason}`);
    if (hint) console.error('%s', `[${ros.routerLabel}][ROS]   → ${hint}`);
    if (e && e.errno) console.error('%s', `[${ros.routerLabel}][ROS]   errno: ${e.errno}`);
    console.error('%s', `[${ros.routerLabel}][ROS]   raw: ${msg}`);
    // No classifier matched → reason is still the raw message; sanitize before
    // it reaches the browser (hard constraint: never send raw .message).
    const safeReason = classified ? reason : sanitizeErr(e);
    // Remember it so the Routers page can say *why* this router is offline (#92).
    if (entry) { entry.lastError = safeReason; entry.lastErrorTs = Date.now(); }
    broadcastRosStatus(false, safeReason, entry);
    _emitRouterStatus(false);
  });
  ros.on('connected', () => startCollectors(session, entry));
}

async function startCollectors(session, entry) {
  if (entry.collectorsStarted) return;
  entry.collectorsStarted = true;
  const _delay = ms => new Promise(r => setTimeout(r, ms));
  try {
    console.log('%s', `[${session.ros.routerLabel}] ── session started (v${APP_VERSION})`);
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
    session.system._onIdentity = (identity) => _persistRouterIdentity(session.routerId, identity);
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
    await _delay(300);
    await session.topology.start();

    entry.startupReady = true;
    console.log('[MikroDash] All collectors running');
    if (entry.routerIo) {
      entry.routerIo.emit('collection:config', _collectionPayload(session.routerId, session));
    }

    /* Match the collectors to who is actually watching, now that startupReady
       is true.
     *
     * The suspend half was always here; the resume half was missing, and its
     * absence is what left the Connections card stale. _idleResume() is
     * otherwise only triggered by the first socket joining the router's room,
     * which is a one-shot: a browser that reconnects while the session is still
     * building — the ordinary case after a restart, since the session takes
     * seconds to bring up sixteen collectors — hits `!entry.startupReady` and is
     * dropped, and nothing retries it.
     *
     * Every other collector survived that because it opens its own stream in
     * start(). Connections is the one that does not: resume() is the only thing
     * that opens it, so a dropped resume left it suspended indefinitely, with
     * its watchdog returning silently on the same flag. */
    const routerRoom = io.sockets.adapter.rooms.get('router-' + session.routerId);
    if (!routerRoom || routerRoom.size === 0) _idleSuspend(session, entry);
    else _idleResume(session, entry);

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
    console.log('%s', `[MikroDash] Switching to router: ${router.label} (${router.host})`);

    // Find old global-default entry before saving the new id
    const oldActiveId = Settings.load().activeRouterId;
    const oldEntry = oldActiveId ? _routerSessions.get(oldActiveId) : null;
    if (oldEntry) {
      broadcastRosStatus(false, `Switching to ${router.label}…`, oldEntry);
    }
    // Only sockets watching the outgoing router should reset their UI state —
    // a global emit would wipe charts/logs in every other user's browser.
    if (oldActiveId) io.to('router-' + oldActiveId).emit('router:switching', { routerId: newRouterId, label: router.label });

    // Save the new active router id
    Settings.save({ activeRouterId: newRouterId });

    // Tear down old session (may be null on first-ever activation from setup wizard)
    if (oldEntry) {
      if (oldEntry.idleTimer) { clearTimeout(oldEntry.idleTimer); oldEntry.idleTimer = null; }
      await teardownSession(oldEntry.session, oldEntry);
      _routerSessions.delete(oldActiveId);
      // Drop stale alert edge-detection state (prevIfState/prevVpnState/…) for
      // the torn-down session — same as idle teardown and router delete do.
      alerter.dropEvaluator(oldActiveId);
    }
    _noRouterMode = false;

    // Relocate every socket that was watching the just-torn-down router — in
    // modern auth that's everyone following the global default; users pinned to
    // a different router via router:switch keep their own view. (The old check
    // skipped sockets with an auth session, orphaning all modern-auth clients.)
    for (const [, socket] of io.sockets.sockets) {
      if (socket.routerId === oldActiveId && socket.routerId !== newRouterId) {
        for (const room of [...socket.rooms]) {
          if (room.startsWith('router-' + socket.routerId)) socket.leave(room);
        }
        socket.routerId = newRouterId;
        socket.join('router-' + newRouterId);
      }
    }

    // Build and start new session
    ensureRouterSession(newRouterId);
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
  session.ros.connectLoop().catch((e) => {
    console.error('%s', `[${session.ros.routerLabel}] connectLoop exited unexpectedly:`, e && e.message ? e.message : e);
  });
  // This router is now pool-owned — drop any alertSessions session for it so
  // connectivity/alerts aren't tracked twice. (No-op for the global active router,
  // which alertSessions already excludes.)
  _syncAlertSessions();
  _syncOverviewSessions();
  return entry;
}

/**
 * Rebuild one router's session in place (#105).
 *
 * ensureRouterSession() memoises on _routerSessions and returns early when a
 * session exists, so it will never pick up an edited setting. Collection
 * settings cannot be live-patched either: whether a collector runs at all is
 * decided at construction. So a change means teardown and rebuild, for that one
 * router.
 *
 * Sockets are deliberately untouched. Rooms are keyed by routerId, which does
 * not change, so nobody leaves `router-<id>` or its page sub-rooms; the tail of
 * startCollectors() re-binds traffic and replays initial state for every socket
 * already in the room. No page reload, and no other router is affected.
 */
async function rebuildRouterSession(routerId) {
  const entry = _routerSessions.get(routerId);
  if (!entry) {                       // nothing live to rebuild
    _syncAlertSessions();
    _syncOverviewSessions();
    return null;
  }
  if (entry.idleTimer) { clearTimeout(entry.idleTimer); entry.idleTimer = null; }
  await teardownSession(entry.session, entry);
  _routerSessions.delete(routerId);
  alerter.dropEvaluator(routerId);
  return ensureRouterSession(routerId);
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
    console.log('%s', `[MikroDash] Idle teardown — router ${routerId}`);
    await teardownSession(entry.session, entry);
    _routerSessions.delete(routerId);
    alerter.dropEvaluator(routerId);
    // No longer pool-owned — let alertSessions resume status-only tracking.
    _syncAlertSessions();
    _syncOverviewSessions();
  }, delayMs);
}

// Socket-side authorization. Sockets carry their session on
// socket.request._authSession rather than req.authSession, so these two exist to
// keep every handler asking Rbac rather than reaching for allowedRouterIds — the
// legacy field cannot express a grant held through a group or a site.
function _visibleRouterIds(socket) {
  return Rbac.effectiveRouterIds(socket?.request?._authSession, 'router:read');
}
function _socketCan(socket, permission, routerId) {
  return Rbac.can(socket?.request?._authSession, permission, routerId);
}

/**
 * Tell every connected browser its permissions changed (issue #108).
 *
 * Without this, editing a role leaves every open session on stale permissions
 * until someone reloads — the feature looks broken. This does not push the new
 * caps (the socket's cached session may itself be stale); it tells the client to
 * re-fetch, which re-resolves server-side and cannot be spoofed by the payload.
 */
function _broadcastPermsChanged() {
  try { io.emit('perms:changed', {}); } catch (_) { /* io not up yet during startup */ }
}

/**
 * May this socket see (or act on) a page for the router it is watching?
 *
 * The one place requirement "globally enabled AND the role grants it" is
 * decided (issue #108). The install-wide toggle is a statement about the
 * deployment — "this site does not use Topology" — and a role narrows further;
 * neither can widen the other. Keeping the conjunction here, rather than inside
 * Rbac.canPage(), is what lets the two be tested independently.
 *
 * Only 10 of the 14 pages have an install toggle; dashboard, reports, routers
 * and settings are governed by role alone, which is what the settingsKey guard
 * below allows for.
 */
function _pageAllowed(socket, page, access = 'read') {
  const rid = socket?.routerId;
  if (!rid) return false;
  const def = Pages.BY_KEY[page];
  if (!def) return false;                       // unknown page: deny
  if (def.settingsKey && Settings.load()[def.settingsKey] === false) return false;
  return Rbac.canPage(socket.request?._authSession, page, access, rid);
}

/**
 * The page a dashboard card borrows its data from.
 *
 * Derived from the collector registry — the dash-card keys ARE collector keys —
 * so it cannot drift from src/pages.js. 'diagnostics' is not a collector and is
 * the dashboard's own card, hence the fallback.
 */
function _dashCardPage(key) {
  // A card room is named after either a page or the collector that feeds it —
  // 'firewall' happens to be both. Page first, so a room added for a page whose
  // collector has a different key ('connections' vs the 'conns' collector) is
  // still gated on that page rather than silently falling back to dashboard.
  if (Pages.BY_KEY[key]) return key;
  return Pages.pageForCollector(key) || 'dashboard';
}

/**
 * May this socket receive a collector's payload on connect?
 *
 * `traffic` and `system` have no page — they drive the header gauges on every
 * page — so they follow router:read like the router list itself and are never
 * withheld. `arp` emits nothing at all.
 */
function _mayReplay(socket, collectorKey) {
  const page = Pages.pageForCollector(collectorKey);
  return page === null || _pageAllowed(socket, page);
}

// Resolve which router a connecting socket should watch.
function _resolveRouterId(socket) {
  const authSession = socket.request?._authSession;
  const cfg = Settings.load();
  if (authSession) {
    const allowed = _visibleRouterIds(socket);
    // Newly reachable case: under the old model an empty allowedRouterIds meant
    // "everything", so "no routers at all" could not happen. Deny-by-default
    // makes it real, and returning '' here is what lets the caller emit
    // access:none instead of leaving a dashboard spinning forever.
    if (!allowed.length) return '';
    // Precedence matters, and an earlier version of this got it wrong: it
    // returned allowed[0] for everyone, and effectiveRouterIds sorts by id — so
    // an unrestricted admin with no personal preference landed on whichever
    // router happened to have the lowest UUID instead of the one configured as
    // active. Sessions are in-memory, so any restart makes that the common path.
    //
    //   1. personal preference, if they may still read it
    //   2. the globally configured active router, if they may read it
    //   3. anything they can read
    if (authSession.activeRouterId && allowed.includes(authSession.activeRouterId)) {
      return authSession.activeRouterId;
    }
    if (cfg.activeRouterId && allowed.includes(cfg.activeRouterId)) {
      return cfg.activeRouterId;
    }
    return allowed[0];
  }
  return cfg.activeRouterId || '';
}

// The router list a specific socket may see, resolved through grants.
function _routersForSocket(socket) {
  const all = Routers.getPublic();
  // geo.auto.ip is a WAN address, and /api/localcc deliberately withholds that
  // from anyone without system:settings. getPublic() masks the password but does
  // not know about this, so the strip happens here — otherwise adding a location
  // would quietly undo an existing disclosure rule. _buildRoutersStats does the
  // same for the stats payload.
  const strip = (r) => {
    if (!r.geo || !r.geo.auto || r.geo.auto.ip === undefined) return r;
    const { ip, ...auto } = r.geo.auto;
    return { ...r, geo: { ...r.geo, auto } };
  };
  if (!_isModern()) return all;
  const maySeeWanIp = _socketCan(socket, 'system:settings');
  const visible = new Set(_visibleRouterIds(socket));
  return all.filter(r => visible.has(r.id)).map(r => (maySeeWanIp ? r : strip(r)));
}

// Persist model/serial/version learned from RouterOS against the router entry,
// and tell clients only when something actually changed. Every session pool
// (main, overview, alert) wires its system collector here, so identity is
// learned for a router whether or not anyone has the Routers page open.
function _persistRouterIdentity(routerId, identity) {
  if (!routerId) return;
  try {
    if (Routers.updateIdentity(routerId, identity)) _broadcastRoutersList();
  } catch (e) {
    console.warn('[MikroDash] could not persist router identity:', sanitizeErr(e));
  }
}

// Tell the client which collectors are off for a router, and how it delivers
// (#105). Router-scoped on purpose: settings:pages is a global io.emit with no
// router id, so it structurally cannot express this. Without it the client marks
// a deliberately-disabled card as stale, which reads as a fault.
function _collectionPayload(routerId, session) {
  const eff = (session && session.collection)
    || resolveCollection(Settings.load(), Routers.getById(routerId) || {});
  return {
    routerId,
    mode:    eff.mode,
    enabled: eff.enabled,
    stream:  eff.stream,
    poll:    eff.poll,
    // Keys the user cannot turn off, so the UI never offers them.
    off:     Object.keys(eff.enabled).filter(k => !eff.enabled[k]),
  };
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

// ── RBAC (issue #78) ─────────────────────────────────────────────────────────
// Computed but NOT enforced yet: every existing guard still decides. This runs
// here so the grant table exists and is populated before anything can read it.
//
// isModern is injected rather than imported by rbac.js, which would otherwise
// have to pull in the whole settings stack to answer one question.
Rbac.init({ isModern: _isModern });
Rbac.migrateFromLegacy();
// Users and routers live in JSON, so nothing stops a grant outliving its
// subject — a hand-edited users.json, or a router removed by an older build.
// Sweeping at startup is both the safety net and the repair path.
db.sweepOrphanGrants(
  Users.listUsersSync().map(u => u.id),
  Routers.loadAll().map(r => r.id),
);

// One-time import of the per-user layout JSON files into user_layouts.
//
// Gated on the table being empty, so it can never re-import over layouts saved
// since. The source files are deliberately LEFT on disk: they are small, and an
// operator who rolls back to an older image needs them to still be there.
(function _migrateLayoutFiles() {
  if (db.layoutCount() > 0) return;
  const dir = process.env.DATA_DIR || '/data';
  let names = [];
  try { names = fs.readdirSync(dir); } catch (_) { return; }

  let imported = 0;
  for (const name of names) {
    const m = /^(dashboard|topology)-layout(?:-(.+))?\.json$/.exec(name);
    if (!m) continue;
    const kind = m[1] === 'dashboard' ? 'dashboard' : 'topology';
    const uid  = m[2] || db.SHARED_LAYOUT_USER;
    try {
      const data = JSON.parse(fs.readFileSync(path.join(dir, name), 'utf8'));
      // Skip empties rather than writing a row that means nothing — a fresh
      // topology file is literally "{}".
      if (!data || (typeof data === 'object' && !Object.keys(data).length)) continue;
      db.setLayout(uid, kind, data);
      imported++;
    } catch (_) { /* corrupt file — the old readers ignored it too */ }
  }
  if (imported) console.log('%s', `[db] imported ${imported} layout file(s) into user_layouts`);
})();

db.startPruneInterval(() => Settings.load());
alerter.init(io, Settings.load());
alertSessions.init(io);

// Register before either pool syncs: both read the hook when they build a
// session, so a hook set later would miss every router created at startup.
alertSessions.setIdentityHook(_persistRouterIdentity);

// One-shot migration for #105: stream-vs-poll used to be a global setting and is
// now per-router. Without this, anyone running global Poll would silently revert
// to Stream on upgrade. planMigration() decides; this just persists the result.
(function _migrateCollectionMode() {
  try {
    const cfg = Settings.load();
    if (cfg.collectionMigrated) return;
    // The legacy stream* keys are no longer in DEFAULTS, so load() filters them
    // out. They have to be read straight off disk or this sees nothing at all.
    const legacy = Settings.readRetired(Object.keys(LEGACY_STREAM_KEYS));
    const plan   = planMigration(legacy, Routers.loadAll());
    for (const { id, collection } of plan) Routers.update(id, { collection });
    if (plan.length) {
      console.log('[MikroDash] migrated global collection method onto %d router(s) (#105)', plan.length);
    }
    Settings.save({ collectionMigrated: true });
  } catch (e) {
    console.warn('[MikroDash] collection migration skipped: %s', sanitizeErr(e));
  }
})();


overviewSessions.setIdentityHook(_persistRouterIdentity);

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
      .then((user) => {
        // This runs AFTER Rbac.migrateFromLegacy(), which is gated on there
        // being no grants yet — so this account would never get one, and the
        // upgraded deployment would come back up with nobody able to log in
        // usefully. Grant it explicitly.
        Rbac.syncUserGrants(user);
        Settings.save({ authMode: 'modern', dashUser: '', dashPass: '' });
        console.warn('%s', '[auth] basic credentials migrated to modern admin account "' + dashUser + '"');
      })
      .catch(e => console.error('[auth] migration failed:', e && e.message ? e.message : e));
    return;
  }
  Settings.save({ authMode: 'modern' });
  console.warn('[auth] basic mode with no credentials — switching to modern; create an admin account to get started');
})();

// Warn loudly if the dashboard is reachable with no authentication configured.
(function _warnIfOpen() {
  if (_authMode() === 'none') {
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
// authLimiter already applies globally (app.use above) — no route-level repeat,
// which would consume two of the 100/min budget per request.
app.get('/login', (_req, res) => res.sendFile(require('path').join(__dirname, '..', 'public', 'login.html')));

// ── Auth API ──────────────────────────────────────────────────────────────────

// GET /api/auth/status — mode, firstRun flag, current session info (no auth needed)
app.get('/api/auth/status', (req, res) => {
  const mode     = _authMode();
  const session  = _sessionFromReq(req); // live role (reflects demotions/deletions)
  const firstRun = mode === 'modern' && Users.userCount() === 0;
  res.json({
    authMode: mode,
    firstRun,
    // caps, not role: with three roles and per-router scope, "is this person a
    // viewer?" no longer answers "may they press this button". Resolved
    // booleans and id lists only — never the grant graph, which would disclose
    // every other principal's access to anyone who opened devtools.
    session: session
      ? { username: session.username, role: session.role, caps: Rbac.capsFor(session) }
      : null,
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
      console.warn('%s', `[auth] login failed — user="${_logSafe(username)}" ip=${_clientIp(req)}`);
      return res.status(401).json({ ok: false, error: 'Invalid username or password' });
    }
    const timeoutMs   = _sessionTimeoutMs();
    const { token, expiresAt } = SessionStore.createSession(user.id, user.username, user.role, timeoutMs, user.allowedRouterIds);
    res.setHeader('Set-Cookie', SessionStore.buildCookieHeader(token, expiresAt));
    console.log('%s', `[auth] login — user="${user.username}" role=${user.role} ip=${_clientIp(req)}`);
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
  if (session) console.log('%s', `[auth] logout — user="${session.username}" ip=${_clientIp(req)}`);
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
  if (!Rbac.can(authSession, 'router:read', routerId)) {
    return res.status(403).json({ ok: false, error: 'Not permitted' });
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
    if (!_isModern()) return res.status(400).json({ ok: false, error: 'Modern auth not enabled' });
    if (_setupClaimed || Users.userCount() > 0) return res.status(409).json({ ok: false, error: 'Setup already complete' });
    const { username, password } = req.body || {};
    if (!username || !_USERNAME_RE.test(username)) return res.status(400).json({ ok: false, error: 'Invalid username' });
    if (!password || String(password).length < 4)  return res.status(400).json({ ok: false, error: 'Password too short' });
    _setupClaimed = true; // claim synchronously, before the first await
    try {
      const user = await Users.createUser({ username, password, role: 'admin', allowedRouterIds: [] });
      // Without this the very first administrator of a fresh install holds no
      // grants, and every guard refuses them — locked out of their own instance
      // the moment setup completes.
      Rbac.syncUserGrants(user);
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
app.get('/api/users', Rbac.requireGlobalAdmin, async (_req, res) => {
  try {
    // Grants are joined here the way /api/groups already does, so the Users card
    // can render real access instead of the legacy role + allowedRouterIds pair
    // (issue #108). One fetch, and the two principal types stay symmetric.
    const users = (await Users.listUsers()).map(u => Object.assign({}, u, {
      grants: db.listGrants({ principalType: 'user', principalId: u.id }),
    }));
    res.json({ ok: true, users });
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// POST /api/users
app.post('/api/users', Rbac.requireGlobalAdmin, async (req, res) => {
  try {
    const { username, password, role, allowedRouterIds } = req.body || {};
    if (!username || !_USERNAME_RE.test(username)) return res.status(400).json({ ok: false, error: 'Invalid username' });
    if (!password || String(password).length < 4)  return res.status(400).json({ ok: false, error: 'Password too short' });
    if (role && !Users.ROLES.includes(role))         return res.status(400).json({ ok: false, error: 'Invalid role' });
    if (await Users.getUserByUsername(username))     return res.status(409).json({ ok: false, error: 'Username already exists' });
    const user = await Users.createUser({ username, password, role: role || 'viewer', allowedRouterIds });
    // Project the legacy fields onto grants ONLY when a caller actually sent
    // them. The Users card grants access through /api/grants now (#108), so a
    // new user starts with none and is granted explicitly — projecting a
    // default 'viewer' here would hand every new account read of every router.
    if (role !== undefined || allowedRouterIds !== undefined) Rbac.syncUserGrants(user);
    res.json({ ok: true, user });
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// PUT /api/users/:id
app.put('/api/users/:id', Rbac.requireGlobalAdmin, async (req, res) => {
  try {
    const { id } = req.params;
    const updates = req.body || {};
    // The old "cannot demote your own account" and "cannot demote the last
    // admin" guards lived here, keyed on updates.role === 'viewer' and
    // Users.adminCount(). Both are gone (issue #108): the Users card no longer
    // sends a role at all, and administration is a grant, not a field. Losing
    // administrator access now means removing the grant, which
    // DELETE /api/grants/:id already guards with wouldOrphanGlobalAdmin — the
    // one check that can see a grant held through a group.
    if (updates.username !== undefined && !_USERNAME_RE.test(updates.username)) {
      return res.status(400).json({ ok: false, error: 'Invalid username' });
    }
    if (updates.role !== undefined && !Users.ROLES.includes(updates.role)) {
      return res.status(400).json({ ok: false, error: 'Invalid role' });
    }
    const updated = await Users.updateUser(id, updates);
    if (!updated) return res.status(404).json({ ok: false, error: 'User not found' });
    // Re-project onto grants ONLY if this request actually carried the legacy
    // fields. syncUserGrants() deletes every grant the principal holds and
    // rebuilds them from role + allowedRouterIds — so running it
    // unconditionally would mean renaming a user silently destroyed every grant
    // an administrator had built in the editor. A legacy caller still sends the
    // fields and still gets the projection.
    if (updates.role !== undefined || updates.allowedRouterIds !== undefined) {
      // Demoting the last administrator through the legacy field is still a way
      // to orphan administration, so the projection is probed before it runs.
      if (Rbac.wouldOrphanGlobalAdmin(() => Rbac.syncUserGrants(updated))) {
        return res.status(400).json({ ok: false, error: 'That would leave nobody with administrator access' });
      }
      Rbac.syncUserGrants(updated);
    }
    res.json({ ok: true, user: updated });
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// DELETE /api/users/:id
app.delete('/api/users/:id', Rbac.requireGlobalAdmin, async (req, res) => {
  try {
    const { id } = req.params;
    if (req.authSession && req.authSession.userId === id) {
      return res.status(400).json({ ok: false, error: 'Cannot delete your own account' });
    }
    // Don't let the last administrator be deleted — that would lock everyone out
    // of administration with no way back in.
    //
    // Asked of the GRANTS, not of Users.adminCount() (issue #108). That counted
    // user records carrying role === 'admin', a field nothing has decided
    // anything with since roles became rows: it cannot see an administrator
    // whose grant is held through a group, and it counts one who was demoted in
    // the editor. The probe below runs the deletion in a transaction, checks
    // whether any global administrator survives, and always rolls back.
    //
    // The user record lives in JSON and cannot join that transaction, so the
    // grant deletion is what gets probed — which is exactly what
    // globalAdminUserIds() reads.
    if (Rbac.wouldOrphanGlobalAdmin(() => db.deleteGrantsForPrincipal('user', id))) {
      return res.status(400).json({ ok: false, error: 'That would leave nobody with administrator access' });
    }
    const deleted = await Users.deleteUser(id);
    if (!deleted) return res.status(404).json({ ok: false, error: 'User not found' });
    // Users live in JSON, so their grants and memberships have no foreign key to
    // cascade through — clear them here rather than leaving rows pointing at an
    // id that could later be reused.
    db.deleteGrantsForPrincipal('user', id);
    // The JSON files had no cleanup path at all, so every deleted user left
    // their dashboard and topology layouts on disk indefinitely.
    db.deleteLayouts(id);
    // Same reasoning, and it matters more here: the row holds encrypted channel
    // credentials, and a reused id would otherwise inherit them.
    db.deleteUserNotifyConfig(id);
    Rbac.bump(); _broadcastPermsChanged();
    res.json({ ok: true });
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// ── Account API (self-service) ────────────────────────────────────────────────
// Everything in the Users block above is administration: one person changing
// another. This is the opposite — a signed-in user acting on themselves — so it
// is gated on identity, not on `system:principals`. Copying
// Rbac.requireGlobalAdmin down here would lock the feature to the one audience
// that does not need it, which is the most likely way to get this wrong.
//
// Every handler reads the user id from the session and never from a path
// parameter, so there is no id for a caller to tamper with: acting on someone
// else is not a permission that is refused here, it is a request that cannot be
// expressed.
//
// Renaming is deliberately absent. A username is the identity an admin manages,
// and alert_events.acknowledged_by stores it as raw text, so a self-service
// rename would quietly orphan historical acknowledgements.
function _requireAccount(req, res, next) {
  // authMode 'none' never populates authSession — there is no person to own an
  // account, so this is "not applicable" rather than "forbidden".
  if (!req.authSession || !req.authSession.userId) {
    return res.status(400).json({ ok: false, error: 'Account settings require user accounts' });
  }
  next();
}

const _accountLimiter         = rateLimit({ windowMs: 60_000, max: 60, standardHeaders: true, legacyHeaders: false });
const _accountPasswordLimiter = rateLimit({ windowMs: 60_000, max: 5,  standardHeaders: true, legacyHeaders: false });
const _accountSessionLimiter  = rateLimit({ windowMs: 60_000, max: 10, standardHeaders: true, legacyHeaders: false });

// What this user may reach, by name — answers "why can't I see router X?"
// without an admin having to explain it.
app.get('/api/account/access', _accountLimiter, _requireAccount, (req, res) => {
  res.json({ ok: true, access: Rbac.accessSummaryFor(req.authSession.userId) });
});

// Where this user is signed in. The session token identifies the current row and
// is stripped before responding — it is the credential itself, and it has no
// business in a browser beyond the cookie it already lives in.
app.get('/api/account/sessions', _accountLimiter, _requireAccount, (req, res) => {
  const token = SessionStore.parseCookieHeader(req.headers.cookie || '')['mikrodash_sid'];
  const sessions = SessionStore.listSessionsForUser(req.authSession.userId)
    .map(s => ({
      createdAt: s.createdAt,
      expiresAt: s.expiresAt === Infinity ? null : s.expiresAt,
      current:   s.token === token,
    }))
    .sort((a, b) => b.createdAt - a.createdAt);
  res.json({ ok: true, sessions });
});

app.post('/api/account/password', _accountPasswordLimiter, _requireAccount, async (req, res) => {
  try {
    const { currentPassword, newPassword } = req.body || {};
    if (!currentPassword || !newPassword) {
      return res.status(400).json({ ok: false, error: 'Current and new password are required' });
    }
    // Same floor as POST /api/users. PUT /api/users/:id skips this check, which
    // is a gap on that route — not one to inherit here.
    if (String(newPassword).length < 4) {
      return res.status(400).json({ ok: false, error: 'Password too short' });
    }
    // The raw record, because verifyPassword needs the hash and salt.
    const user = await Users.getUser(req.authSession.userId);
    if (!user) return res.status(404).json({ ok: false, error: 'User not found' });
    if (!await Users.verifyPassword(user, currentPassword)) {
      return res.status(401).json({ ok: false, error: 'Current password is incorrect' });
    }
    await Users.updateUser(req.authSession.userId, { password: newPassword });

    // A password change is often a response to a suspected compromise, so the
    // other sessions go with it. The caller's own session is spared, or they
    // would be signed out by their own security action.
    const token   = SessionStore.parseCookieHeader(req.headers.cookie || '')['mikrodash_sid'];
    const revoked = SessionStore.deleteSessionsForUser(req.authSession.userId, token);
    console.log('%s', `[account] password changed — user="${_logSafe(req.authSession.username)}" ` +
                      `ip=${_clientIp(req)} othersRevoked=${revoked.length}`);
    res.json({ ok: true, revokedOtherSessions: revoked.length });
  } catch (e) {
    console.error('[account] password change failed:', e.message);
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

app.post('/api/account/sessions/revoke-others', _accountSessionLimiter, _requireAccount, (req, res) => {
  const token   = SessionStore.parseCookieHeader(req.headers.cookie || '')['mikrodash_sid'];
  const revoked = SessionStore.deleteSessionsForUser(req.authSession.userId, token);
  console.log('%s', `[account] sessions revoked — user="${_logSafe(req.authSession.username)}" count=${revoked.length}`);
  res.json({ ok: true, revoked: revoked.length });
});

// ── Dashboard layout API ──────────────────────────────────────────────────────
// Per-user layout when modern auth is active; falls back to shared file otherwise.
// Whose layout this request is for. In authMode 'none' there is no identity, so
// everyone shares one row — the same behaviour the unsuffixed
// dashboard-layout.json had, now explicit rather than emergent from a filename.
function _layoutUser(req) {
  return req.authSession?.userId || db.SHARED_LAYOUT_USER;
}

const layoutLimiter = rateLimit({ windowMs: 60_000, max: 60, standardHeaders: true, legacyHeaders: false });

// The dashboard layout is a per-user preference with no router in the request,
// so a scoped check with no target would fail closed and lock everyone out.
// Requiring the page on at least one visible router is the equivalent question.
const _requireDashboard = (req, res, next) =>
  Rbac.canPageAnywhere(req.authSession, 'dashboard')
    ? next()
    : res.status(403).json({ ok: false, error: 'Not permitted' });

app.get('/api/dashboard-layout', layoutLimiter, _requireDashboard, (req, res) => {
  try {
    const own = db.getLayout(_layoutUser(req), 'dashboard');
    if (own) return res.json(own);
    // No layout of their own yet — fall back to the shared one so the client's
    // localStorage cache is refreshed rather than left stale from a previous user.
    const shared = db.getLayout(db.SHARED_LAYOUT_USER, 'dashboard');
    res.json(shared || null);
  } catch (_) { res.json(null); }
});

app.post('/api/dashboard-layout', layoutLimiter, _requireDashboard, (req, res) => {
  try {
    const body = req.body || {};
    if (!Array.isArray(body.cards)) return res.status(400).json({ ok: false });
    db.setLayout(_layoutUser(req), 'dashboard', { cards: body.cards });
    res.json({ ok: true });
  } catch (e) {
    console.error('[dashboard-layout] save failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

// ── Topology layout API ───────────────────────────────────────────────────────
// Saved node positions for the Topology map, one row per user holding every
// router, so the row count stays bounded as the fleet grows.
function _readTopoLayoutRow(req) {
  return db.getLayout(_layoutUser(req), 'topology')
      || db.getLayout(db.SHARED_LAYOUT_USER, 'topology')
      || {};
}

const { isValidRouterId: _topoValidRid, cleanPositions: _cleanPositions } = require('./topologyLayout');

/** Whole-file read: per-user file first, shared file as the fallback, matching
 *  how /api/dashboard-layout resolves a user who has not saved anything yet. */

// Scoped on the Topology page (issue #108). These take a routerId, so before
// this they were a cross-router probe: any authenticated session could confirm
// a router's existence and read its saved node positions.
app.get('/api/topology-layout', layoutLimiter,
        Rbac.requirePage('topology', 'read', Rbac.fromQuery('routerId')), (req, res) => {
  try {
    const rid = String(req.query.routerId || '');
    if (!_topoValidRid(rid)) return res.json(null);
    const all = _readTopoLayoutRow(req);
    res.json({ positions: all[rid] || {} });
  } catch (_) { res.json(null); }
});

app.post('/api/topology-layout', layoutLimiter,
         Rbac.requirePage('topology', 'read', Rbac.fromBody('routerId')), (req, res) => {
  try {
    const body = req.body || {};
    const rid = String(body.routerId || '');
    if (!_topoValidRid(rid)) return res.status(400).json({ ok: false });
    const positions = _cleanPositions(body.positions);
    if (!positions) return res.status(400).json({ ok: false });

    // Merge: a save for one router must never discard another router's layout.
    const all = _readTopoLayoutRow(req);
    if (Object.keys(positions).length) all[rid] = positions;
    else delete all[rid];                       // Re-layout posts {} to reset
    db.setLayout(_layoutUser(req), 'topology', all);
    res.json({ ok: true });
  } catch (e) {
    console.error('[topology-layout] save failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

// ── Settings API ──────────────────────────────────────────────────────────────
// In modern auth, viewers get only a non-sensitive subset; admins get the full
// (credential-masked) settings. Basic/none mode is unchanged (full masked view).
app.get('/api/settings', (req, res) => {
  // Not session.role: an administrator whose grant is held through a group has
  // role 'viewer' on their user record, and would have been handed the reduced
  // payload while every other route treated them as an admin.
  if (!Rbac.can(req.authSession, 'system:settings')) {
    return res.json(Settings.getViewerPublic());
  }
  res.json(Settings.getPublic());
});

// Single source for the settings:pages payload.
//
// Three call sites used to maintain identical 26-key object literals by hand:
// the _reset branch, the post-save broadcast and the on-connect emit. The
// browser builds its _alertTypes map from this payload, so a key missing from
// any one of them left that alert type switched off in the UI while the server
// had it enabled — the push channel fired and the notification bell stayed
// empty, with nothing logged. Adding a single setting drifted this three times.
//
// The notif* booleans are derived from DEFAULTS rather than listed, so a new
// alert toggle reaches the client automatically. That assumes every boolean
// named notif* is a client-facing alert toggle, which holds today; anything
// sensitive must not adopt that prefix. Credentials are excluded structurally
// (they are strings, and filtered by CREDENTIAL_FIELDS in settings.js).
const _PAGE_SETTING_KEYS = [
  ...Pages.SETTING_KEYS,
  'alertCpuThreshold', 'alertPingLoss', 'vpnDashTopN', 'pingEnabled',
  'displayTimezone',
  // Not caught by the /^notif/ filter below, and the browser needs it to decide
  // whether to offer the My Alerts tab at all.
  'userNotifyEnabled',
  ...Object.keys(Settings.DEFAULTS)
    .filter(k => /^notif/.test(k) && typeof Settings.DEFAULTS[k] === 'boolean'),
];

function _pageSettings(src) {
  const out = {};
  for (const k of _PAGE_SETTING_KEYS) out[k] = src[k];
  return out;
}

app.post('/api/settings', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    const body = req.body || {};
    if (body._reset) {
      const { DEFAULTS } = require('./settings');
      Settings.save(DEFAULTS);
      io.emit('settings:pages', _pageSettings(DEFAULTS));
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
      // Hours, not milliseconds. Floor of 1 h mirrors the clamp in settings.js
      // and protects MikroTik's update servers from a hand-crafted request.
      updateCheckHours:[1,168],
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
    const boolFields = [...Pages.SETTING_KEYS,
                        'pingEnabled','rosDebug','userNotifyEnabled',
                        'telegramEnabled','pushbulletEnabled','smtpEnabled','smtpSecure','ntfyEnabled',
                        'notifIfaceUpDown','notifVpn','notifCpu','notifPing','notifNetwatch','notifRouterStatus',
                        'notifRouterUpdate','notifBgp',
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
    // A per-router interval override outranks the fleet default (#105). Without
    // this the global save would silently un-pin whichever router the pool is
    // currently serving, and the modal would then disagree with reality.
    const _ovr    = (s.collection && s.collection.overrides) || {};
    const _pinned = (key) => _ovr[key] !== undefined;

    const collectorMap = { conns:s.conns, talkers:s.talkers, system:s.system, wireless:s.wireless, vpn:s.vpn, firewall:s.firewall, ifStatus:s.ifStatus, ping:s.ping, arp:s.arp, dhcpNetworks:s.dhcpNetworks, bandwidth:s.bandwidth, routing:s.routing };
    const pollMap = { pollConns:'conns', pollTalkers:'talkers', pollSystem:'system', pollWireless:'wireless',
      pollVpn:'vpn', pollFirewall:'firewall', pollIfstatus:'ifStatus', pollBandwidth:'bandwidth',
      pollPing:'ping', pollArp:'arp', pollDhcp:'dhcpNetworks', pollRouting:'routing' };
    for (const [key, name] of Object.entries(pollMap)) {
      if (key in updates && !_pinned(key)) {
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
    if ('pollConns' in updates && !_pinned('pollConns') && s.conns) {
      s.conns.pollMs = saved.pollConns;
      s.conns._restartStream();
    }

    // pollPing controls the /tool/ping stream interval
    if ('pollPing' in updates && !_pinned('pollPing') && s.ping) {
      s.ping.pollMs = saved.pollPing;
      s.ping._restartStream();
    }

    // pollTalkers controls the kid-control stream interval
    if ('pollTalkers' in updates && !_pinned('pollTalkers') && s.talkers) {
      s.talkers.pollMs = saved.pollTalkers;
      s.talkers._restartStream();
    }

    // pollSystem controls the ros.stream() interval — restart with new =interval=N
    if ('pollSystem' in updates && !_pinned('pollSystem') && s.system) {
      s.system.pollMs = saved.pollSystem;
      s.system._restartStream();
    }

    // pollIfstatus controls the emit timer + monitor-traffic stream interval
    if ('pollIfstatus' in updates && !_pinned('pollIfstatus') && s.ifStatus) {
      s.ifStatus.pollMs = saved.pollIfstatus;
      s.ifStatus._restartEmitTimer();
      s.ifStatus._restartMonitorStream();
    }

    // pollIfaces controls the /interface/print + /ip/address/print stream interval
    if ('pollIfaces' in updates && !_pinned('pollIfaces') && s.ifStatus) {
      s.ifStatus.metaPollMs = saved.pollIfaces;
      s.ifStatus._restartMetaStreams();
    }

    // pollFirewall controls the table stream interval — restart it live
    if ('pollFirewall' in updates && !_pinned('pollFirewall') && s.firewall) {
      s.firewall.pollMs = saved.pollFirewall;
      s.firewall._stopTableStream();
      s.firewall._startTableStream(s.firewall._activeTable);
    }

    // pollVpn controls the VPN counter stream interval — restart it live
    if ('pollVpn' in updates && !_pinned('pollVpn') && s.vpn) {
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
      if ('maxConns' in updates && s.conns) {
        s.conns.maxConns = saved.maxConns;
      }
    }

    const pageSettings = _pageSettings(saved);
    io.emit('settings:pages', pageSettings);
    res.json({ ok:true, requiresRestart:false });
  } catch(e) {
    console.error('[settings] save error:', e);
    res.status(500).json({ ok:false, error: sanitizeErr(e) });
  }
});

// ── Notification test endpoint ────────────────────────────────────────────────
const _testNotifLimiter = rateLimit({ windowMs: 60_000, max: 10, standardHeaders: true, legacyHeaders: false });

app.post('/api/settings/test-notification', Rbac.requireGlobalAdmin, _testNotifLimiter, async (req, res) => {
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

// ── Per-user notification channels (issue #109) ───────────────────────────────
// Deliberately NOT behind Rbac.requireGlobalAdmin, unlike every route above:
// these manage the caller's own delivery preferences, and gating them on
// administrator access would leave the feature reachable by exactly the people
// who least need it. A copy-paste of the guard from /api/settings is the most
// likely way to break this, which is why it is called out here and pinned by a
// test.
//
// There is no permission check on the router set either, and that is the point:
// authorization happens in userNotify.recipientsFor(), per router, at send time.
// Deciding here which routers a user may be alerted about would be a second,
// staler answer to a question Rbac.can already owns.
const _userNotifyLimiter     = rateLimit({ windowMs: 60_000, max: 60, standardHeaders: true, legacyHeaders: false });
const _userNotifyTestLimiter = rateLimit({ windowMs: 60_000, max: 10, standardHeaders: true, legacyHeaders: false });

function _requireUserNotify(req, res, next) {
  // The install-wide switch ships off: per-user ntfy and SMTP let the *user*
  // choose a destination host, so enabling this widens what an ordinary account
  // can make the server connect to.
  if (!Settings.load().userNotifyEnabled) {
    return res.status(403).json({ ok: false, error: 'Per-user notification channels are disabled for this install' });
  }
  // authMode 'none' never populates authSession — there is no person to own a
  // personal channel, so there is nothing to serve rather than an error state.
  if (!req.authSession || !req.authSession.userId) {
    return res.status(400).json({ ok: false, error: 'Per-user notification channels require user accounts' });
  }
  next();
}

app.get('/api/user-notify', _userNotifyLimiter, _requireUserNotify, (req, res) => {
  res.json(userNotify.getPublic(req.authSession.userId));
});

app.post('/api/user-notify', _userNotifyLimiter, _requireUserNotify, (req, res) => {
  try {
    res.json({ ok: true, config: userNotify.save(req.authSession.userId, req.body || {}) });
  } catch (e) {
    // save() rejects a malformed address. That is the caller's mistake to fix,
    // not a server fault, so it must not read as one.
    console.error('[user-notify]', e.message);
    res.status(400).json({ ok: false, error: sanitizeErr(e) });
  }
});

app.post('/api/user-notify/test-notification', _userNotifyTestLimiter, _requireUserNotify, async (req, res) => {
  try {
    const body = req.body || {};
    if (!body.channel) return res.status(400).json({ ok: false, error: 'channel is required' });
    // Same "test before save" affordance the install-wide route has: merge what
    // is typed into the form over what is stored, so somebody can verify their
    // own token without committing it first. Masked values mean "use the stored
    // one" and must never be sent as the literal bullets.
    const typed = {};
    for (const f of userNotify.CREDENTIAL_FIELDS) {
      if (body[f] && !Settings.isMasked(body[f])) typed[f] = String(body[f]).slice(0, 512);
    }
    for (const f of userNotify.STR_FIELDS) {
      if (body[f] && !Settings.isMasked(body[f])) typed[f] = String(body[f]).slice(0, 256);
    }
    // The channel has to be treated as enabled for the test, or testing before
    // ticking the box would report "not configured" rather than the truth.
    const enableKey = { telegram: 'telegramEnabled', pushbullet: 'pushbulletEnabled',
                        ntfy: 'ntfyEnabled', email: 'emailEnabled' }[body.channel];
    if (!enableKey) return res.status(400).json({ ok: false, error: 'Unknown channel' });

    const stored   = userNotify.load(req.authSession.userId);
    const settings = { ...stored, ...typed };
    settings[enableKey] = true;

    // Email is the install's mail server plus this user's address, so a test has
    // to compose the same thing delivery would — including an address typed but
    // not yet saved. notifier still calls the channel 'smtp'; 'email' is what it
    // is called to a user, who never sees a mail server.
    if (body.channel === 'email') {
      const inst = Settings.load();
      if (!inst.smtpHost || !inst.smtpFrom) {
        return res.status(400).json({ ok: false, error: 'No mail server is configured for this install' });
      }
      const to = typed.emailTo || stored.emailTo;
      if (!to) return res.status(400).json({ ok: false, error: 'Enter an email address first' });
      Object.assign(settings, {
        smtpEnabled: true, smtpHost: inst.smtpHost, smtpPort: inst.smtpPort,
        smtpSecure: inst.smtpSecure, smtpUser: inst.smtpUser, smtpPass: inst.smtpPass,
        smtpFrom: inst.smtpFrom, smtpTo: to,
      });
    }
    await notifier.testChannel(settings, body.channel === 'email' ? 'smtp' : body.channel);
    res.json({ ok: true });
  } catch (e) {
    console.error('[user-notify test]', e.message);
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// ── Routers API ───────────────────────────────────────────────────────────────

// GET /api/routers — list all routers (passwords masked); filtered by allowedRouterIds in modern mode
app.get('/api/routers', (req, res) => {
  const cfg    = Settings.load();
  const active = cfg.activeRouterId || '';
  let routers  = Routers.getPublic();
  if (_isModern()) {
    // effectiveRouterIds, not allowedRouterIds: the legacy field cannot express
    // a grant held via a group or a site, so filtering on it would hide routers
    // the caller is genuinely entitled to see.
    const visible = new Set(Rbac.effectiveRouterIds(req.authSession, 'router:read'));
    routers = routers.filter(r => visible.has(r.id));
  }
  res.json({ routers, activeId: active });
});

// POST /api/routers — add a new router
app.post('/api/routers', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    const body = req.body || {};
    if (!body.host || !String(body.host).trim()) {
      return res.status(400).json({ ok:false, error:'host is required' });
    }
    const router = Routers.add(body);
    Rbac.bump(); _broadcastPermsChanged();
    _broadcastRoutersList();
    _syncAlertSessions();
    _syncOverviewSessions();
    res.json({ ok:true, router: { ...router, password: router.password ? '••••••••' : '' } });
  } catch(e) {
    res.status(500).json({ ok:false, error: sanitizeErr(e) });
  }
});

// PUT /api/routers/:id — edit a router
app.put('/api/routers/:id', Rbac.requirePerm('router:manage', Rbac.fromParam('id')), async (req, res) => {
  try {
    const body = req.body || {};
    if (body.disabled === true && req.params.id === Settings.load().activeRouterId) {
      return res.status(400).json({ ok:false, error:'Switch to another router before disabling this one.' });
    }
    if (body.disabled === true) {
      const _e = _routerSessions.get(req.params.id);
      if (_e) {
        if (_e.idleTimer) { clearTimeout(_e.idleTimer); _e.idleTimer = null; }
        await teardownSession(_e.session, _e);
        _routerSessions.delete(req.params.id);
        alerter.dropEvaluator(req.params.id);
      }
      const disabledRoom = 'router-' + req.params.id;
      for (const [, sock] of io.sockets.sockets) {
        if (sock.rooms && sock.rooms.has(disabledRoom)) {
          sock.leave(disabledRoom);
          sock.emit('router:disabled', { routerId: req.params.id });
        }
      }
    }
    // Snapshot before the write so we can tell whether anything that shapes the
    // session actually changed. A label-only edit must not cost a reconnect.
    const _beforeRouter = Routers.getById(req.params.id);
    const _beforeFp = _beforeRouter ? collectionFingerprint(Settings.load(), _beforeRouter) : null;

    // A siteId change alters who can reach this router, so every cached
    // authorization view is stale. Easy to miss: it reads as router config.
    const router = Routers.update(req.params.id, body);
    Rbac.bump(); _broadcastPermsChanged();
    if (!router) return res.status(404).json({ ok:false, error:'Router not found' });
    _broadcastRoutersList();

    // Collection settings are decided at construction, so applying a change means
    // rebuilding this one router's session. Skipped when the router was just
    // disabled above (its session is already gone).
    const _afterFp = collectionFingerprint(Settings.load(), router);
    if (_beforeFp !== null && _afterFp !== _beforeFp && !router.disabled) {
      await rebuildRouterSession(req.params.id);
    }

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

// DELETE /api/routers/:id — delete a router (active router may also be deleted)
app.delete('/api/routers/:id', Rbac.requirePerm('router:manage', Rbac.fromParam('id')), async (req, res) => {
  try {
    const deletedId  = req.params.id;
    const _cfg       = Settings.load();
    const wasActive  = deletedId === _cfg.activeRouterId;

    const deleted = Routers.remove(deletedId);
    if (!deleted) return res.status(404).json({ ok:false, error:'Router not found' });
    db.deleteGrantsForScope('router', deletedId);
    Rbac.bump(); _broadcastPermsChanged();

    // Tear down any live pool session for the deleted router.
    const _deletedEntry = _routerSessions.get(deletedId);
    if (_deletedEntry) {
      if (_deletedEntry.idleTimer) { clearTimeout(_deletedEntry.idleTimer); _deletedEntry.idleTimer = null; }
      await teardownSession(_deletedEntry.session, _deletedEntry);
      _routerSessions.delete(deletedId);
    }

    // Purge all historical data for the removed router.
    db.deleteRouterData(deletedId);

    // Drop any pool evaluator/alertSession state for the removed router.
    alerter.dropEvaluator(deletedId);

    if (wasActive) {
      const remaining = Routers.loadAll();
      if (remaining.length > 0) {
        // Auto-promote the first remaining router.
        const nextId = remaining[0].id;
        Settings.save({ activeRouterId: nextId });
        _noRouterMode = false;
        // Relocate every socket that was watching the deleted router (see switchRouter).
        for (const [, socket] of io.sockets.sockets) {
          if (socket.routerId === deletedId && socket.routerId !== nextId) {
            for (const room of [...socket.rooms]) {
              if (room.startsWith('router-' + socket.routerId)) socket.leave(room);
            }
            socket.routerId = nextId;
            socket.join('router-' + nextId);
          }
        }
        ensureRouterSession(nextId);
        // Notify clients of the new active router.
        io.to('router-' + nextId).emit('router:active', { activeId: nextId });
      } else {
        // No routers left — show setup wizard.
        _noRouterMode = true;
        io.emit('setup:required', {});
        io.emit('routers:update', []);
      }
    }

    _broadcastRoutersList();
    _syncAlertSessions();
    _syncOverviewSessions();
    res.json({ ok:true });
  } catch(e) {
    res.status(500).json({ ok:false, error: sanitizeErr(e) });
  }
});

// POST /api/routers/:id/activate — switch to a different router (hot-swap)
app.post('/api/routers/:id/activate', Rbac.requireGlobalAdmin, async (req, res) => {
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
app.post('/api/routers/test', Rbac.requireGlobalAdmin, _testConnLimiter, async (req, res) => {
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
    // No classifier matched → sanitize the raw message before responding.
    done(false, reason === msg ? sanitizeErr(e) : reason);
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
app.get('/api/localcc', (req, res) => {
  const s = _globalSession();
  if (!s) return res.json({ cc: '', wanIp: '' });
  const wanIp = (s.state.lastWanIp || '').split('/')[0];
  let cc = '';
  if (wanIp) { const g = geo.lookup(wanIp); if (g) cc = g.country || ''; }
  // Viewers only need the country code (world-map arc origin); the WAN IP is
  // withheld from them like the rest of the router network detail.
  // Same reasoning as GET /api/settings: resolve through grants, not the role
  // field, which cannot see access conferred by a group.
  res.json({ cc, wanIp: Rbac.can(req.authSession, 'system:settings') ? wanIp : '' });
});

/**
 * City/town search for the location picker (issue #96).
 *
 * This guard is about resources, not confidentiality: place names are public
 * geographic data, and the gazetteer is derived from a database already shipped
 * in the image. What it protects is the *build* — the first search costs a few
 * hundred milliseconds and tens of megabytes, so an arbitrary viewer should not
 * be able to trigger it. Anyone who can edit something that carries a location
 * may search: a site (system:principals) or at least one router.
 *
 * Do not "fix" this into a confidentiality guard; there is nothing here to leak.
 */
function _requireLocationEditor(req, res, next) {
  if (Rbac.can(req.authSession, 'system:principals')) return next();
  if (Rbac.effectiveRouterIds(req.authSession, 'router:manage').length) return next();
  return res.status(403).json({ ok: false, error: 'Not permitted' });
}

// A type-ahead is chatty by nature — the widget debounces, so a fast typist
// produces well under this. Same shape as _testConnLimiter.
const _citySearchLimiter = rateLimit({
  windowMs: 60_000, max: 120, standardHeaders: true, legacyHeaders: false,
});

app.get('/api/cities', _requireLocationEditor, _citySearchLimiter, (req, res) => {
  try {
    if (!cityIndex.available()) {
      // Not an error. An install whose geoip data cannot be read still works; it
      // simply cannot offer the picker, and the widget renders that as a message
      // rather than as a failure. Automatic geolocation is unaffected — it goes
      // through geo.lookup(), which is a supported API.
      return res.json({
        ok: true, cities: [], unavailable: true, reason: cityIndex.unavailableReason(),
      });
    }
    res.json({ ok: true, cities: cityIndex.search(req.query.q, req.query.limit) });
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

function sanitizeErr(e) {
  if (!e) return null;
  const msg = (e && e.message) ? e.message : String(e);
  return msg
    .replace(/\/[^\s'"]{2,}/g, '[path]')
    .replace(/\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(:\d+)?/g, '[addr]')
    // Provider errors (SMTP auth, Telegram API) can embed the account/email or
    // bot token — redact both before anything reaches the browser.
    .replace(/[\w.+-]+@[\w.-]+\.\w+/g, '[email]')
    .replace(/\b\d{6,}:[A-Za-z0-9_-]{20,}\b/g, '[token]')
    .slice(0, 200);
}

// ── Sites (issue #78) ────────────────────────────────────────────────────────
// A site groups routers; a router belongs to exactly one, or none. In this phase
// sites are organisational only — nothing authorises against them yet, so writes
// are admin-only exactly as the router routes are. Phase 3 moves these to
// `system:principals` and filters the GET by what the caller may see.

// Parse the body of a site create/update. Returns {error} or {value}. Names and
// descriptions are rendered in the browser, so they are length-capped here and
// escaped there. Coordinates are range-checked: a bad latitude silently stored
// would put a marker in the wrong hemisphere rather than fail visibly.
function _parseSiteBody(body, { partial } = {}) {
  const out = {};
  const b   = body || {};

  if (b.name !== undefined || !partial) {
    const name = String(b.name === undefined ? '' : b.name).trim();
    if (!name || name.length > 64) return { error: 'Name must be 1-64 characters' };
    out.name = name;
  }
  if (b.description !== undefined) {
    const d = String(b.description == null ? '' : b.description).trim();
    if (d.length > 256) return { error: 'Description must be 256 characters or fewer' };
    out.description = d || null;
  }
  // A site's location is a picked place, not typed coordinates (#96). lat/lon
  // survive as the plotted values — they are simply derived from the choice
  // rather than entered, which is why all five columns move together and a
  // half-set location is unreachable.
  //
  //   undefined  the caller did not touch the location -> leave it alone, so a
  //              rename cannot blank it
  //   null       explicit "no location"
  if (b.place !== undefined) {
    if (b.place === null) {
      out.lat = null; out.lon = null;
      out.place_name = null; out.place_region = null; out.place_cc = null;
    } else {
      // Validated by the same function the router store uses, so a site and a
      // router cannot disagree about what a well-formed place is.
      const p = GeoPlace.normalizePlace(b.place);
      if (!p) return { error: 'Pick a town from the list, or clear the location' };
      out.lat = p.lat; out.lon = p.lon;
      out.place_name = p.name; out.place_region = p.region; out.place_cc = p.cc;
    }
  }
  return { value: out };
}

// ── Groups and grants (issue #78) ────────────────────────────────────────────
// All of these are system:principals, which is GLOBAL-only by construction — a
// site-scoped administrator cannot reach them, and therefore cannot edit their
// own grant to widen it. That is what makes a site scope a boundary rather than
// a default view.

function _parseName(body, { partial } = {}) {
  const out = {}, b = body || {};
  if (b.name !== undefined || !partial) {
    const name = String(b.name === undefined ? '' : b.name).trim();
    if (!name || name.length > 64) return { error: 'Name must be 1-64 characters' };
    out.name = name;
  }
  if (b.description !== undefined) {
    const d = String(b.description == null ? '' : b.description).trim();
    if (d.length > 256) return { error: 'Description must be 256 characters or fewer' };
    out.description = d || null;
  }
  return { value: out };
}

app.get('/api/groups', Rbac.requireGlobalAdmin, (req, res) => {
  const groups = db.listGroups().map(g => Object.assign({}, g, {
    memberUserIds: db.getGroupMembers(g.id),
    grants:        db.listGrants({ principalType: 'group', principalId: g.id }),
  }));
  res.json({ ok: true, groups });
});

app.post('/api/groups', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    const parsed = _parseName(req.body, { partial: false });
    if (parsed.error) return res.status(400).json({ ok: false, error: parsed.error });
    const group = db.createGroup(parsed.value);
    if (Array.isArray(req.body.memberUserIds)) db.setGroupMembers(group.id, req.body.memberUserIds);
    Rbac.bump(); _broadcastPermsChanged();
    res.json({ ok: true, group });
  } catch (e) {
    if (/UNIQUE constraint failed/.test(e.message)) {
      return res.status(409).json({ ok: false, error: 'A group with that name already exists' });
    }
    console.error('[groups] create failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.put('/api/groups/:id', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    if (!db.getGroup(req.params.id)) return res.status(404).json({ ok: false, error: 'No such group' });
    const parsed = _parseName(req.body, { partial: true });
    if (parsed.error) return res.status(400).json({ ok: false, error: parsed.error });

    // Emptying the group that holds the only global admin grant is one of the
    // five ways to orphan the last administrator, and the least obvious.
    if (Array.isArray(req.body.memberUserIds) &&
        Rbac.wouldOrphanGlobalAdmin(() => db.setGroupMembers(req.params.id, req.body.memberUserIds))) {
      return res.status(400).json({ ok: false, error: 'That would leave nobody with administrator access' });
    }
    const group = db.updateGroup(req.params.id, parsed.value);
    if (Array.isArray(req.body.memberUserIds)) db.setGroupMembers(req.params.id, req.body.memberUserIds);
    Rbac.bump(); _broadcastPermsChanged();
    res.json({ ok: true, group });
  } catch (e) {
    if (/UNIQUE constraint failed/.test(e.message)) {
      return res.status(409).json({ ok: false, error: 'A group with that name already exists' });
    }
    console.error('[groups] update failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.delete('/api/groups/:id', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    if (!db.getGroup(req.params.id)) return res.status(404).json({ ok: false, error: 'No such group' });
    if (Rbac.wouldOrphanGlobalAdmin(() => db.deleteGroup(req.params.id))) {
      return res.status(400).json({ ok: false, error: 'That would leave nobody with administrator access' });
    }
    db.deleteGroup(req.params.id);
    Rbac.bump(); _broadcastPermsChanged();
    res.json({ ok: true });
  } catch (e) {
    console.error('[groups] delete failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.get('/api/grants', Rbac.requireGlobalAdmin, (req, res) => {
  res.json({ ok: true, grants: db.listGrants({
    principalType: req.query.principalType, principalId: req.query.principalId,
  }) });
});

app.post('/api/grants', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    const b = req.body || {};
    if (!['user', 'group'].includes(b.principalType)) return res.status(400).json({ ok: false, error: 'Invalid principal type' });
    // roleId is the current form; `role` is accepted for the legacy three so an
    // older client — or a scripted caller — keeps working until Phase 6.
    const roleId = b.roleId || { admin: 'administrator', operator: 'operator', viewer: 'readonly' }[b.role];
    if (!roleId || !db.getRole(roleId))               return res.status(400).json({ ok: false, error: 'Invalid role' });
    if (!['global', 'site', 'router'].includes(b.scopeType)) return res.status(400).json({ ok: false, error: 'Invalid scope type' });
    const scopeId = b.scopeType === 'global' ? '' : String(b.scopeId || '');
    if (b.scopeType !== 'global' && !scopeId)   return res.status(400).json({ ok: false, error: 'Scope id required' });
    // Refuse a grant naming something that does not exist: it would sit in the
    // table forever, conferring nothing, and read as working in the UI.
    if (b.scopeType === 'site'   && !db.getSite(scopeId))     return res.status(404).json({ ok: false, error: 'No such site' });
    if (b.scopeType === 'router' && !Routers.getById(scopeId)) return res.status(404).json({ ok: false, error: 'No such router' });
    if (b.principalType === 'group' && !db.getGroup(b.principalId)) return res.status(404).json({ ok: false, error: 'No such group' });

    const grant = db.upsertGrant({
      principalType: b.principalType, principalId: String(b.principalId),
      roleId, scopeType: b.scopeType, scopeId,
      createdBy: req.authSession ? req.authSession.userId : null,
    });
    Rbac.bump(); _broadcastPermsChanged();
    res.json({ ok: true, grant });
  } catch (e) {
    console.error('[grants] create failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.delete('/api/grants/:id', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    if (Rbac.wouldOrphanGlobalAdmin(() => db.deleteGrant(req.params.id))) {
      return res.status(400).json({ ok: false, error: 'That would leave nobody with administrator access' });
    }
    if (!db.deleteGrant(req.params.id)) return res.status(404).json({ ok: false, error: 'No such grant' });
    Rbac.bump(); _broadcastPermsChanged();
    res.json({ ok: true });
  } catch (e) {
    console.error('[grants] delete failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

// What the caller may do, as resolved booleans and id lists. Deliberately NOT in
// _MODERN_PUBLIC and deliberately not the raw grant graph — shipping the graph
// would disclose every other principal's access to anyone who opened devtools.
// ── Roles (issue #108) ───────────────────────────────────────────────────────
//
// Every verb is global-admin, INCLUDING the read. Two reasons: a role holding
// settings:write must not be able to widen itself in one request, and the full
// matrix of every role discloses the security model. An ordinary user learns
// only their own resolution, via capsFor().

/** Validate a submitted page matrix against the registry. */
function _parseRolePages(body) {
  if (body.pages === undefined) return { value: null };  // not submitted: leave alone
  if (!Array.isArray(body.pages)) return { error: 'pages must be an array' };
  const out = [], seen = new Set();
  for (const row of body.pages) {
    if (!row || typeof row !== 'object') return { error: 'Each page entry must be an object' };
    const page = String(row.page || '');
    if (!Pages.BY_KEY[page]) return { error: 'Unknown page: ' + page };
    if (seen.has(page)) return { error: 'Duplicate page: ' + page };
    if (row.access !== 'read' && row.access !== 'write') {
      return { error: 'access must be read or write' };
    }
    seen.add(page);
    out.push({ page, access: row.access });
  }
  return { value: out };
}

const _roleView = (r) => ({ ...r, builtin: !!r.builtin, pages: db.rolePages(r.id), grants: db.countGrantsForRole(r.id) });

app.get('/api/roles', Rbac.requireGlobalAdmin, (req, res) => {
  // writeCapablePages is derived from the projection table, never restated in
  // the client — it is what greys out a Write toggle that would confer nothing.
  res.json({
    ok: true,
    roles: db.listRoles().map(_roleView),
    pages: Pages.PAGES.map(p => ({ key: p.key, title: p.title, settingsKey: p.settingsKey })),
    writeCapablePages: Object.keys(Rbac.WRITE_CONFERS),
  });
});

app.post('/api/roles', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    const parsed = _parseName(req.body, { partial: false });
    if (parsed.error) return res.status(400).json({ ok: false, error: parsed.error });
    const pages = _parseRolePages(req.body);
    if (pages.error) return res.status(400).json({ ok: false, error: pages.error });

    const role = db.createRole(parsed.value);
    if (pages.value) db.setRolePages(role.id, pages.value);
    Rbac.bump(); _broadcastPermsChanged();
    res.json({ ok: true, role: _roleView(role) });
  } catch (e) {
    if (/UNIQUE constraint failed/.test(e.message)) {
      return res.status(409).json({ ok: false, error: 'A role with that name already exists' });
    }
    console.error('[roles] create failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.put('/api/roles/:id', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    const existing = db.getRole(req.params.id);
    if (!existing) return res.status(404).json({ ok: false, error: 'No such role' });
    // Administrator's reach is structural. Letting it be edited would either do
    // nothing (it has no page rows) or silently narrow every admin in the fleet.
    if (existing.builtin) {
      return res.status(400).json({ ok: false, error: 'The Administrator role cannot be edited' });
    }
    const parsed = _parseName(req.body, { partial: true });
    if (parsed.error) return res.status(400).json({ ok: false, error: parsed.error });
    const pages = _parseRolePages(req.body);
    if (pages.error) return res.status(400).json({ ok: false, error: pages.error });

    const role = db.updateRole(req.params.id, parsed.value);
    if (pages.value) db.setRolePages(req.params.id, pages.value);
    // Editing a role changes the answer for every principal holding it, at any
    // scope — the easiest bump to forget, and silent when missed.
    Rbac.bump(); _broadcastPermsChanged();
    res.json({ ok: true, role: _roleView(role) });
  } catch (e) {
    if (/UNIQUE constraint failed/.test(e.message)) {
      return res.status(409).json({ ok: false, error: 'A role with that name already exists' });
    }
    console.error('[roles] update failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.delete('/api/roles/:id', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    const existing = db.getRole(req.params.id);
    if (!existing) return res.status(404).json({ ok: false, error: 'No such role' });
    if (existing.builtin) {
      return res.status(400).json({ ok: false, error: 'The Administrator role cannot be deleted' });
    }
    // The foreign key would refuse this anyway; saying how many grants block it
    // is more useful than surfacing a constraint error.
    const used = db.countGrantsForRole(req.params.id);
    if (used) {
      return res.status(409).json({
        ok: false,
        error: `That role is still assigned by ${used} grant${used === 1 ? '' : 's'}`,
      });
    }
    db.deleteRole(req.params.id);
    Rbac.bump(); _broadcastPermsChanged();
    res.json({ ok: true });
  } catch (e) {
    console.error('[roles] delete failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.get('/api/auth/permissions', (req, res) => {
  try {
    res.json({ ok: true, caps: Rbac.capsFor(req.authSession) });
  } catch (e) {
    console.error('[rbac] permissions failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.get('/api/sites', (req, res) => {
  try {
    res.json({ ok: true, sites: db.listSites() });
  } catch (e) {
    console.error('[sites] list failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.post('/api/sites', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    const parsed = _parseSiteBody(req.body, { partial: false });
    if (parsed.error) return res.status(400).json({ ok: false, error: parsed.error });
    const site = db.createSite(parsed.value);
    io.emit('sites:update', db.listSites());
    res.json({ ok: true, site });
  } catch (e) {
    // The UNIQUE ... COLLATE NOCASE index is what actually enforces distinct
    // names, so a duplicate surfaces here rather than from a pre-check that
    // would race anyway.
    if (/UNIQUE constraint failed/.test(e.message)) {
      return res.status(409).json({ ok: false, error: 'A site with that name already exists' });
    }
    console.error('[sites] create failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.put('/api/sites/:id', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    if (!db.getSite(req.params.id)) return res.status(404).json({ ok: false, error: 'No such site' });
    const parsed = _parseSiteBody(req.body, { partial: true });
    if (parsed.error) return res.status(400).json({ ok: false, error: parsed.error });
    const site = db.updateSite(req.params.id, parsed.value);
    io.emit('sites:update', db.listSites());
    res.json({ ok: true, site });
  } catch (e) {
    if (/UNIQUE constraint failed/.test(e.message)) {
      return res.status(409).json({ ok: false, error: 'A site with that name already exists' });
    }
    console.error('[sites] update failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

// Assign routers to a site in one call. The router modal can also set a site one
// router at a time; this is the same operation from the other direction, which is
// how anyone actually thinks about it when setting a site up.
//
// One request rather than N router PUTs so a half-applied assignment is not
// possible, and so removals are handled: a router previously here and no longer
// listed has to be detached, which a per-router save never sees.
app.put('/api/sites/:id/routers', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    if (!db.getSite(req.params.id)) return res.status(404).json({ ok: false, error: 'No such site' });
    const wanted = Array.isArray(req.body && req.body.routerIds)
      ? req.body.routerIds.map(String) : null;
    if (!wanted) return res.status(400).json({ ok: false, error: 'routerIds must be an array' });

    const all = Routers.loadAll();
    let changed = 0;
    for (const r of all) {
      const shouldBeHere = wanted.includes(r.id);
      const isHere       = r.siteId === req.params.id;
      if (shouldBeHere && !isHere)      { Routers.update(r.id, { siteId: req.params.id }); changed++; }
      else if (!shouldBeHere && isHere) { Routers.update(r.id, { siteId: '' });            changed++; }
    }
    if (changed) {
      // A router's site determines who can reach it through a site-scoped grant.
      Rbac.bump(); _broadcastPermsChanged(); _broadcastPermsChanged();
      _broadcastRoutersList();
    }
    res.json({ ok: true, changed });
  } catch (e) {
    console.error('[sites] router assignment failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.delete('/api/sites/:id', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    if (!db.getSite(req.params.id)) return res.status(404).json({ ok: false, error: 'No such site' });
    // Routers live in routers.json, so SQLite cannot cascade into them. Detach
    // first: a router pointing at a site that no longer exists would render a
    // blank chip and, once Phase 3 lands, be unreachable to a site-scoped grant.
    const detached = Routers.clearSite(req.params.id);
    db.deleteSite(req.params.id);
    // Site-scoped grants would otherwise outlive the site they name.
    db.deleteGrantsForScope('site', req.params.id);
    // Detaching routers changes who can reach them, so every cached view is stale.
    Rbac.bump(); _broadcastPermsChanged();
    io.emit('sites:update', db.listSites());
    if (detached) _broadcastRoutersList();
    res.json({ ok: true, detached });
  } catch (e) {
    console.error('[sites] delete failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.get('/healthz', (req, res) => {
  const ge = _globalEntry();
  const s  = ge ? ge.session : null;
  const st = s ? s.state : {};
  const activeRoomSize = s ? (io.sockets.adapter.rooms.get('router-' + s.routerId)?.size || 0) : 0;
  // High-frequency system/interface collectors are intentionally suspended
  // while nobody is viewing this router. Traffic remains active for history
  // recording, so it is always required; add viewer-driven collectors only
  // while their suspension would be unexpected.
  const requiredCollectors = activeRoomSize > 0
    ? ['traffic', 'system', 'ifstatus']
    : ['traffic'];
  const { ok, statusCode, stale } = computeHealthStatus({
    startupReady: ge ? ge.startupReady : false,
    rosConnected: s ? s.ros.connected : false,
    state: st,
    requiredCollectors,
  });
  const isStarting = !(ge && ge.startupReady) && (Date.now() - _serverStartTime < STARTUP_GRACE_MS);
  // Unauthenticated callers (the Docker healthcheck) only need the status code
  // and ok/starting flags — version, router ids and collector detail would
  // otherwise be free fingerprinting for anyone who can reach the port.
  if (_isModern() && !_sessionFromReq(req)) {
    return res.status(statusCode).json({ ok, starting: isStarting });
  }
  const body = {
    ok,
    starting: isStarting,
    version: APP_VERSION,
    routerConnected: s ? s.ros.connected : false,
    activeRouterId:  s ? s.routerId : null,
    startupReady: ge ? ge.startupReady : false,
    stale,
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
      ifstatus: { ts:st.lastIfStatusTs, err:sanitizeErr(st.lastIfStatusErr) },
      ping:     { ts:st.lastPingTs,     err:sanitizeErr(st.lastPingErr)     },
      netwatch: { ts:st.lastNetwatchTs, err:sanitizeErr(st.lastNetwatchErr) },
      topology: { ts:st.lastTopologyTs, err:sanitizeErr(st.lastTopologyErr) },
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

// Math.max(...arr) overflows the call stack above ~65k arguments — report
// queries default to a 100k row limit, so reduce instead of spreading.
const _maxOf = (arr) => arr.reduce((m, v) => (v > m ? v : m), -Infinity);

// Format a stored bandwidth_usage MB value for display. Decimal thresholds are
// deliberate: rx_mb is written as Mbps/8, i.e. 10^6-based, so rendering it
// against 1024-based thresholds overstated every total by ~4.9%. Decimal is
// also the right convention here — ISP quotas are quoted decimal.
// A volume peak is per bucket, so the label has to say which bucket. Without an
// aggregation the stored granularity is one minute.
function _bucketNoun(agg) {
  return agg === 'hour' ? 'Hour' : agg === 'day' ? 'Day'
       : agg === 'week' ? 'Week' : agg === 'month' ? 'Month' : 'Minute';
}

function _fmtDataMB(mb) {
  const n = +mb || 0;
  if (n >= 1e6)  return (n / 1e6).toFixed(2) + ' TB';
  if (n >= 1000) return (n / 1000).toFixed(2) + ' GB';
  if (n >= 1)    return n.toFixed(1) + ' MB';
  return (n * 1000).toFixed(0) + ' KB';
}

// Pair each Offline row with the next Online row to compute outage duration.
// Single backward pass (rows are ts-ASC); null downtime = still offline.
function _annotateDowntime(rows) {
  let nextOnlineTs = null;
  for (let i = rows.length - 1; i >= 0; i--) {
    if (rows[i].connected) { nextOnlineTs = rows[i].ts; rows[i].downtime_ms = null; }
    else rows[i].downtime_ms = nextOnlineTs != null ? nextOnlineTs - rows[i].ts : null;
  }
  return rows;
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
app.get('/api/reports/ping', Rbac.requirePerm('router:history', Rbac.fromQuery('routerId')), (req, res) => {
  const { routerId, from, to, aggregate } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const rows = aggregate
    ? db.queryPingSamplesAgg(routerId, from, to, aggregate)
    : db.queryPingSamples(routerId, from, to);
  res.json({ ok: true, rows });
});

// GET /api/reports/ping/export
app.get('/api/reports/ping/export', Rbac.requirePerm('router:history', Rbac.fromQuery('routerId')), (req, res) => {
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
    const maxRtt = rtts.length   ? _maxOf(rtts).toFixed(1) : '—';
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
// Per-interface report summary: rate stats (peak, mean, 95th percentile) from
// traffic_samples, volume stats (totals) from bandwidth_usage, and the link
// utilisation those rates represent.
//
// Capacity is resolved here from the *requested* routerId rather than in the
// browser from whichever router happens to be active, so a report for router B
// viewed while router A is selected still uses B's line speed. _scopeRouterId
// has already authorised this id, and only the two capacity integers are read
// off the record.
//
// Utilisation is deliberately NOT clamped to 100. The live dashboard card does
// clamp, which is what hides a misconfigured capacity — a link reporting 151%
// is telling you the configured figure is wrong, and that is worth seeing.
function _ifaceSummary(routerId, iface, from, to) {
  const t = db.queryTrafficSummary(routerId, iface, from, to, 95);
  const b = db.queryBandwidthSummary(routerId, iface, from, to);
  const r = Routers.getById(routerId);
  const capDown = Math.max(1, parseInt(r && r.bwDownMbps, 10) || 1000);
  const capUp   = Math.max(1, parseInt(r && r.bwUpMbps,   10) || 1000);
  const pct = (v, cap) => (v == null ? null : +((v / cap) * 100).toFixed(1));
  return {
    ...t, ...b,
    capacityDownMbps: capDown,
    capacityUpMbps:   capUp,
    rxPeakPct: pct(t.rxMaxMbps, capDown), txPeakPct: pct(t.txMaxMbps, capUp),
    rxP95Pct:  pct(t.rxP95Mbps, capDown), txP95Pct:  pct(t.txP95Mbps, capUp),
  };
}

app.get('/api/reports/traffic', Rbac.requirePerm('router:history', Rbac.fromQuery('routerId')), (req, res) => {
  const { routerId, from, to, aggregate } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const iface = req.query.interface || '';
  if (iface) {
    const rows = aggregate
      ? db.queryTrafficSamplesAgg(routerId, iface, from, to, aggregate)
      : db.queryTrafficSamples(routerId, iface, from, to);
    return res.json({ ok: true, rows, summary: _ifaceSummary(routerId, iface, from, to) });
  }
  res.json({ ok: true, interfaces: db.queryTrafficInterfaces(routerId) });
});

// GET /api/reports/traffic/export
app.get('/api/reports/traffic/export', Rbac.requirePerm('router:history', Rbac.fromQuery('routerId')), (req, res) => {
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
    // Shared summary rather than reducing `rows`: those are averages once an
    // aggregation is selected, so a max over them is a peak of averages, and
    // they are capped by the query LIMIT.
    const s     = _ifaceSummary(routerId, iface, from, to);
    const n1    = (v) => (v == null ? '—' : v.toFixed(1));
    const avgRx = n1(s.rxAvgMbps);
    const avgTx = n1(s.txAvgMbps);
    const peakRx= n1(s.rxMaxMbps);
    const peakTx= n1(s.txMaxMbps);
    const step  = rows.length > 150 ? Math.ceil(rows.length / 150) : 1;
    const sub   = rows.filter((_,i)=>i%step===0);
    const rtr   = Routers.getById(routerId);
    return _toPdf('Traffic History Report', ['Timestamp', 'Interface', 'RX (Mbps)', 'TX (Mbps)'],
      label.map(r => ({ Timestamp: r.ts, Interface: r.interface, 'RX (Mbps)': r.rx_mbps, 'TX (Mbps)': r.tx_mbps })), res, {
        router: rtr ? (rtr.label || rtr.host) : routerId, from, to,
        stats: [
          { label: 'Peak RX',   value: peakRx !== '—' ? peakRx+' Mbps' : '—' },
          { label: 'Peak TX',   value: peakTx !== '—' ? peakTx+' Mbps' : '—' },
          { label: 'Avg RX',    value: avgRx  !== '—' ? avgRx +' Mbps' : '—' },
          { label: 'Avg TX',    value: avgTx  !== '—' ? avgTx +' Mbps' : '—' },
          { label: '95th RX',   value: n1(s.rxP95Mbps) !== '—' ? n1(s.rxP95Mbps)+' Mbps' : '—' },
          // Utilisation against the router's configured line capacity, not
          // clamped at 100 — over-capacity is the signal worth seeing.
          { label: 'Peak Util', value: s.rxPeakPct == null ? '—'
                                  : Math.round(s.rxPeakPct)+'% / '+Math.round(s.txPeakPct)+'%' },
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
app.get('/api/reports/bandwidth', Rbac.requirePerm('router:history', Rbac.fromQuery('routerId')), (req, res) => {
  const { routerId, from, to, aggregate } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const iface = req.query.interface || '';
  if (iface) {
    const rows = aggregate
      ? db.queryBandwidthSamplesAgg(routerId, iface, from, to, aggregate)
      : db.queryBandwidthSamples(routerId, iface, from, to);
    return res.json({ ok: true, rows, summary: _ifaceSummary(routerId, iface, from, to) });
  }
  res.json({ ok: true, interfaces: db.queryBandwidthInterfaces(routerId) });
});

// GET /api/reports/bandwidth/export
app.get('/api/reports/bandwidth/export', Rbac.requirePerm('router:history', Rbac.fromQuery('routerId')), (req, res) => {
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
    // Same summary the on-screen cards use, so the two cannot disagree. Totals
    // come from SQL over the whole range rather than from `rows`, which is
    // capped by the query LIMIT. Volume only here — rates belong to the traffic
    // report, so that the two reports stay about different things.
    const s      = _ifaceSummary(routerId, iface, from, to);
    const step   = rows.length > 150 ? Math.ceil(rows.length / 150) : 1;
    const sub    = rows.filter((_,i)=>i%step===0);
    const rtr    = Routers.getById(routerId);
    return _toPdf('Bandwidth Usage Report', ['Timestamp', 'Interface', 'Download (MB)', 'Upload (MB)'],
      label.map(r => ({ Timestamp: r.ts, Interface: r.interface, 'Download (MB)': r.rx_mb, 'Upload (MB)': r.tx_mb })), res, {
        router: rtr ? (rtr.label || rtr.host) : routerId, from, to,
        // Six boxes maximum — _toPdf renders them with lineBreak:false, so a
        // seventh starts truncating values rather than wrapping.
        stats: [
          { label: 'Total Download', value: _fmtDataMB(s.rxTotalMb) },
          { label: 'Total Upload',   value: _fmtDataMB(s.txTotalMb) },
          { label: 'Total',          value: _fmtDataMB((s.rxTotalMb || 0) + (s.txTotalMb || 0)) },
          { label: 'Busiest ' + _bucketNoun(aggregate) + ' ↓', value: s.rxMaxMb == null ? '—' : _fmtDataMB(s.rxMaxMb) },
          { label: 'Busiest ' + _bucketNoun(aggregate) + ' ↑', value: s.txMaxMb == null ? '—' : _fmtDataMB(s.txMaxMb) },
          { label: aggregate ? 'Buckets' : 'Samples', value: s.samples.toLocaleString() },
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
// ── Alert acknowledgment ──────────────────────────────────────────────────────
// Acknowledgment is what makes "Clear all" on the bell mean something. It is
// deliberately separate from resolution: `resolved_at` is what the system
// observed, `acknowledged_at` is what a person decided. An alert can be
// acknowledged while still open, and recovering later must not erase who
// acknowledged it.
const ackLimiter = rateLimit({ windowMs: 60_000, max: 120, standardHeaders: true, legacyHeaders: false });

/** DB row -> the shape the browser gets, matching the alert:fired payload so the
 *  bell never has to care whether an entry arrived live or via replay. */
/**
 * Shape a stored alert row for the browser.
 *
 * The live socket path (alerter.fire) sends `label` and `routerName` alongside
 * the raw type; this one did not, so every alert already open when a page loaded
 * rendered with a blank device and a database key for a title — "routeros_update"
 * with nothing to say which of three routers it came from.
 *
 * `names` is an optional routerId -> label map. Callers rendering a list build it
 * once rather than making this reach for the router store per row.
 */
function _alertRow(r, names) {
  const rid = r.router_id || null;
  return {
    id: r.id,
    routerId: rid,
    alertType: r.alert_type,
    // Derived, never stored: see alerter.labelFor. Keeping the key in the
    // database and the name in code means renaming an alert is not a migration.
    label: alerter.labelFor(r.alert_type),
    // Without this an alert cannot say which router it belongs to, which is the
    // whole difficulty with three identical update alerts.
    routerName: (names && rid && names.get(rid)) || null,
    subject: r.subject || null,
    detail: r.detail || null,
    firedAt: r.fired_at,
    resolvedAt: r.resolved_at || null,
    acknowledgedAt: r.acknowledged_at || null,
    acknowledgedBy: r.acknowledged_by || null,
  };
}

app.post('/api/alerts/:id/ack', ackLimiter, (req, res) => {
  try {
    const id = parseInt(req.params.id, 10);
    if (!Number.isFinite(id) || id <= 0) return res.status(400).json({ ok: false });
    // Scope check BEFORE the write. The caller supplies only an alert id, so
    // without this a user restricted to one router could acknowledge alerts on
    // every other one — the same boundary _scopeRouterId holds for the report
    // routes, which can enforce it as middleware because they take ?routerId.
    const owner = db.getAlertRouterId(id);
    if (!owner) return res.status(404).json({ ok: false });
    // Acknowledging is an operator action now, not something any authenticated
    // viewer may do. The router is only known after resolving the alert, which
    // is why this is an inline check rather than route middleware.
    if (!Rbac.can(req.authSession, 'router:ack', owner)) {
      return res.status(403).json({ ok: false, error: 'Not permitted' });
    }
    const who = req.authSession?.username || null;
    const row = db.acknowledgeAlert(id, who);
    if (!row) return res.status(404).json({ ok: false });
    // Tell every browser on that router, so two people looking at the same
    // alert do not each have to acknowledge it.
    const _one = new Map();
    const _r = Routers.getById(row.router_id);
    if (_r) _one.set(row.router_id, _r.label || _r.host);
    io.to('router-' + row.router_id).emit('alert:acked', _alertRow(row, _one));
    res.json({ ok: true, alert: _alertRow(row, _one) });
  } catch (e) {
    console.error('[alerts] ack failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

// "Clear all" in the bell. Named for what it does rather than for how it used
// to do it: this resolved nothing until now, so a router whose alert condition
// had quietly gone away stayed on the Routers page as Alerting with no way to
// clear it. Still gated on router:ack — clearing the list is the same operator
// act as acknowledging one row, and it destroys nothing.
app.post('/api/alerts/clear-all', ackLimiter, (req, res) => {
  try {
    const rid = String((req.body && req.body.routerId) || '');
    if (!/^[A-Za-z0-9_-]{1,64}$/.test(rid)) return res.status(400).json({ ok: false });
    if (!Rbac.can(req.authSession, 'router:ack', rid)) {
      return res.status(403).json({ ok: false, error: 'Not permitted' });
    }
    const who = req.authSession?.username || null;
    const ids = db.resolveAllAlerts(rid, who);
    if (ids.length) {
      io.to('router-' + rid).emit('alerts:cleared-all', {
        routerId: rid, ids, clearedAt: Date.now(), clearedBy: who,
      });
    }
    res.json({ ok: true, count: ids.length });
  } catch (e) {
    console.error('[alerts] clear-all failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.get('/api/reports/alerts', Rbac.requirePerm('router:history', Rbac.fromQuery('routerId')), (req, res) => {
  const { routerId, from, to } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  // alert_label rides alongside the raw key rather than replacing it: sorting,
  // filtering and the CSV export all key off alert_type, and only the display
  // wants a human name. Derived server-side so the report and the notification
  // bell cannot end up calling the same alert two different things.
  res.json({ ok: true, rows: db.queryAlertEvents(routerId, from, to).map(r => (
    { ...r, alert_label: alerter.labelFor(r.alert_type) })) });
});

// GET /api/reports/alerts/export
app.get('/api/reports/alerts/export', Rbac.requirePerm('router:history', Rbac.fromQuery('routerId')), (req, res) => {
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
app.get('/api/reports/connectivity', Rbac.requirePerm('router:history', Rbac.fromQuery('routerId')), (req, res) => {
  const { routerId, from, to, aggregate } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  if (aggregate) return res.json({ ok: true, rows: db.queryConnectivityEventsAgg(routerId, from, to, aggregate) });
  const rows = _annotateDowntime(db.queryConnectivityEvents(routerId, from, to));
  res.json({ ok: true, rows });
});

// GET /api/reports/connectivity/export
app.get('/api/reports/connectivity/export', Rbac.requirePerm('router:history', Rbac.fromQuery('routerId')), (req, res) => {
  const { routerId, from, to } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const rows = _annotateDowntime(db.queryConnectivityEvents(routerId, from, to));
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
    const longestDownMs = resolvedMs.length ? _maxOf(resolvedMs) : null;
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

// ── Historical data cleanup ───────────────────────────────────────────────────

// A restricted admin (non-empty allowedRouterIds) must not be able to run a
// global purge, since that would delete history for routers they cannot even
// see. Force them to name a router, and hold it to their allowed set.
function _purgeScope(req) {
  const routerId = String((req.body && req.body.routerId) || '').trim();
  if (!_isModern()) return { routerId };
  // A global purge deletes history for every router, including ones the caller
  // may not even be able to see, so it is a system-level action rather than a
  // scoped one.
  if (!routerId) {
    return Rbac.can(req.authSession, 'system:db')
      ? { routerId }
      : { error: 'Select a router — your account cannot purge all routers' };
  }
  if (!Rbac.can(req.authSession, 'router:purge', routerId)) return { error: 'Router not permitted' };
  return { routerId };
}

// Age presets the UI offers, in days. 0 means "everything, regardless of age".
const _PURGE_AGES = [0, 1, 7, 30, 90, 365];

function _purgeOpts(req) {
  const scope = _purgeScope(req);
  if (scope.error) return scope;
  const body  = req.body || {};
  const types = Array.isArray(body.types)
    ? body.types.filter(t => db.PURGE_TYPES.includes(t))
    : [];
  if (Array.isArray(body.types) && types.length === 0) {
    return { error: 'No valid data types selected' };
  }
  const days = Number(body.olderThanDays);
  if (!_PURGE_AGES.includes(days)) return { error: 'Invalid age filter' };
  return { routerId: scope.routerId, types, olderThanMs: days * 86400000 };
}

app.get('/api/db/stats', Rbac.requireGlobalAdmin, (req, res) => {
  try {
    const s = db.stats();
    // Restricted admins only see their own routers' row counts.
    if (_isModern()) {
      const readable = new Set(Rbac.effectiveRouterIds(req.authSession, 'router:read'));
      s.byRouter = s.byRouter.filter(r => readable.has(r.routerId));
    }
    res.json({ ok: true, ...s });
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

app.post('/api/db/purge', Rbac.requireGlobalAdmin, (req, res) => {
  const opts = _purgeOpts(req);
  if (opts.error) return res.status(400).json({ ok: false, error: opts.error });
  try {
    // dryRun powers the "this will delete N rows" preview shown before the user
    // confirms. Same predicate as the delete, so the two can never disagree.
    if (req.body && req.body.dryRun) {
      return res.json({ ok: true, dryRun: true, ...db.countPurge(opts) });
    }
    const before = db.stats().bytes;
    const result = db.purge(opts);
    // Without this the rows go but the file on disk never shrinks, which reads
    // as the cleanup having done nothing.
    const vac    = result.deleted > 0 ? db.vacuum() : { before, after: before };
    console.log('%s', `[db] purge by ${req.authSession ? req.authSession.username : 'local'}: ${result.deleted} rows`);
    res.json({ ok: true, deleted: result.deleted, bytesBefore: vac.before, bytesAfter: vac.after });
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

/**
 * Which address a router faces the internet with (issue #96).
 *
 * Two sources, because the two session pools know different things:
 *
 *   state.lastWanIp   only the main pool has it — dhcpNetworks resolves it, and
 *                     overviewSessions does not run that collector.
 *   ifStatus `ips`    both pools have it: interface status streams
 *                     /ip/address/print, so a background router still reports
 *                     the addresses on its WAN interface.
 *
 * Both are CIDR strings ('10.0.0.2/24'), so the mask is stripped — the same thing
 * /api/localcc does. Frequently an RFC1918 address, which geoip cannot place at
 * all; that is a real answer, not a failure, and the caller treats it as such.
 */
function _wanIpFor(session, bg, wanIf) {
  const live = session && session.state ? session.state.lastWanIp : '';
  if (live) return String(live).split('/')[0];
  const ips = wanIf && Array.isArray(wanIf.ips) ? wanIf.ips : [];
  return ips.length ? String(ips[0]).split('/')[0] : '';
}

// routerId -> the WAN IP we last attempted a lookup for.
//
// _buildRoutersStats runs on a 2s timer *per socket with the Routers page open*,
// so two administrators means two passes a second. This makes the repeat case a
// Map hit rather than a loadAll() plus a deep compare, and it means an
// unresolvable private address is looked up once per process rather than forever.
const _autoGeoSeen = new Map();

/**
 * Refresh a router's cached location if its WAN IP has changed.
 *
 * Returns true when something was written, so the caller knows to re-read the
 * store before building the payload — otherwise the router just located would
 * still carry the stale record.
 *
 * Persisting the fix rather than resolving it live is what lets an OFFLINE router
 * still appear on the map at its last known position, which is the whole point of
 * the view: a live-only lookup would drop exactly the routers you most want to
 * see.
 */
function _refreshAutoGeo(router, wanIp) {
  const decision = GeoPlace.autoGeoAction(wanIp, wanIp ? geo.lookup(wanIp) : null, Date.now());
  if (decision.action === 'keep') return false;

  // Cheap repeat guard, ahead of the store read. _buildRoutersStats runs on a 2s
  // timer *per socket with the Routers page open*, so two administrators means
  // two passes a second; and an address geoip cannot place is looked up once per
  // process rather than forever.
  if (_autoGeoSeen.get(router.id) === wanIp) return false;
  _autoGeoSeen.set(router.id, wanIp);

  // Survives a restart, so a reboot costs one lookup per router, not one write.
  if (router.geo && router.geo.auto && router.geo.auto.ip === wanIp) return false;

  if (decision.action === 'clear') return !!Routers.updateGeoAuto(router.id, null);
  return !!Routers.updateGeoAuto(router.id, decision.auto);
}

// ── Socket.IO ─────────────────────────────────────────────────────────────────
function _buildRoutersStats(socket) {
  let allRouters    = Routers.loadAll();
  const bgSummaries = overviewSessions.getSummaries();
  const cfg         = Settings.load();
  // Resolved once for the whole payload, not per router: this runs on a 2s timer
  // while anyone has the Routers page open.
  const openAlerts  = db.countOpenAlertsByRouter();

  // Same RBAC boundary as _routersForSocket: a restricted user only ever sees
  // stats (host/serial/version/cpu…) for routers in their allowed set.
  let visible = allRouters.filter(r => !r.disabled);
  if (_isModern()) {
    const readable = new Set(_visibleRouterIds(socket));
    visible = visible.filter(r => readable.has(r.id));
  }

  // Refresh cached locations before building rows, so a router that just
  // resolved is not rendered from the record it had a moment ago. Almost always
  // a no-op — see _refreshAutoGeo's two guards.
  let geoWritten = false;
  for (const r of visible) {
    const mainEntry = _routerSessions.get(r.id);
    const s         = mainEntry && mainEntry.session;
    const bg        = bgSummaries.find(x => x.routerId === r.id);
    const ifPay     = s ? s.ifStatus.lastPayload : (bg ? bg.ifStatusPayload : null);
    const dIf       = r.defaultIf || cfg.defaultIf || 'ether1';
    const wIf       = ifPay ? (ifPay.interfaces || []).find(i => i.name === dIf) : null;
    if (_refreshAutoGeo(r, _wanIpFor(s, bg, wIf))) geoWritten = true;
  }
  if (geoWritten) {
    allRouters = Routers.loadAll();
    const byId = new Map(allRouters.map(r => [r.id, r]));
    visible = visible.map(r => byId.get(r.id) || r);
    _broadcastRoutersList();     // getPublic() now carries a different geo block
  }

  // Sites are the last fallback tier, resolved once for the whole payload rather
  // than per router — same reasoning as openAlerts above.
  const sitesById = new Map(db.listSites().map(s => [s.id, s]));
  // The WAN address is withheld from anyone without system:settings, matching
  // /api/localcc. Resolved once per build, not per row.
  const maySeeWanIp = _socketCan(socket, 'system:settings');

  return visible.map(r => {
    const mainEntry = _routerSessions.get(r.id);
    const s         = mainEntry && mainEntry.session;
    const bg        = bgSummaries.find(x => x.routerId === r.id);
    const defaultIf = r.defaultIf || cfg.defaultIf || 'ether1';

    const connected = s ? !!mainEntry.rosConnected : (bg ? bg.connected : false);
    // Why it is offline, so the card can explain itself (#92). Already sanitized
    // at the point of capture; null while connected.
    const lastError = connected ? null
      : (s ? (mainEntry.lastError || null) : (bg ? (bg.lastError || null) : null));
    const sysPay    = s ? s.system.lastPayload    : (bg ? bg.systemPayload   : null);
    const ifPay     = s ? s.ifStatus.lastPayload  : (bg ? bg.ifStatusPayload : null);
    const wanIf     = ifPay ? (ifPay.interfaces || []).find(i => i.name === defaultIf) : null;

    return {
      id:        r.id,
      label:     r.label || r.host,
      host:      r.host,
      isActive:  !!s,
      connected,
      lastError,
      // Unresolved alerts on this router. Independent of `connected` — a router
      // can be reachable and still have something wrong on it.
      openAlerts: openAlerts[r.id] || 0,
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
      siteId:    r.siteId || null,
      siteName:  (r.siteId && sitesById.get(r.siteId)) ? sitesById.get(r.siteId).name : null,
      // Where to draw it, and how confident to look (#96). Resolved server-side
      // so the browser holds one answer per router rather than reimplementing
      // the priority order — a second implementation is one that can disagree.
      // null means unlocated: the map's tray, never a marker at 0,0.
      geo: (() => {
        const loc = GeoPlace.resolveLocation(r, r.siteId ? sitesById.get(r.siteId) : null);
        if (!loc) return null;
        if (loc.wanIp !== undefined && !maySeeWanIp) delete loc.wanIp;
        return loc;
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

  // Before any replay: a card for a disabled collector must be marked as such
  // before it would otherwise start its stale countdown.
  socket.emit('collection:config', _collectionPayload(entry.session.routerId, entry.session));

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
  // Reachability of other routers is only disclosed within the caller's allowed set.
  const _readable = _isModern() ? new Set(_visibleRouterIds(socket)) : null;
  for (const [routerId, connected] of alertSessions.getStatusMap()) {
    if (_readable && !_readable.has(routerId)) continue;
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
    // Don't cache the rejected promise — the next connect should retry instead
    // of replaying this failure until the router reconnects.
    s._ifacesFetch = null;
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
  // Both DHCP replays follow the DHCP page, including the dashboard's networks
  // card they also feed — same rule as the dash-card rooms: a page you were
  // denied must not reappear as a dashboard widget.
  // The WAN IP is chrome (map origin, network diagram), so it is replayed
  // regardless of whether the DHCP page is permitted — same as it is broadcast.
  if (s.dhcpNetworks.lastPayload) {
    socket.emit('lan:wan', { ts: s.dhcpNetworks.lastPayload.ts, wanIp: s.dhcpNetworks.lastPayload.wanIp });
  }
  if (!_mayReplay(socket, 'dhcpNetworks')) { /* denied: no LAN overview */ }
  else if (s.dhcpNetworks.lastPayload) {
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

  // Replay the collector's own payload rather than rebuilding it here — a
  // hand-built copy silently drops any field the collector adds later (it had
  // already lost the DHCP server summary the leases filter needs). The manual
  // build stays only as a fallback for a socket that connects before the first
  // lease load completes.
  if (!_mayReplay(socket, 'dhcpLeases')) { /* denied: no lease list */ }
  else if (s.dhcpLeases.lastPayload) {
    socket.emit('leases:list', s.dhcpLeases.lastPayload);
  } else {
    const allLeases = [];
    for (const [ip, v] of s.dhcpLeases.byIP.entries()) allLeases.push({ ip, ...v });
    socket.emit('leases:list', { ts: Date.now(), leases: allLeases, servers: [] });
  }

  // Page-scoped replays (issue #108). Without this filter the page gate on
  // page:focus is cosmetic: connecting alone would hand a socket the current
  // payload of every collector regardless of which pages its role allows.
  //
  // `traffic` and `system` are deliberately unfiltered — they drive the header
  // gauges on every page and belong to no single one, so they follow
  // router:read like the router list itself.
  if (s.traffic && s.traffic.lastWanStatus) socket.emit('wan:status', s.traffic.lastWanStatus);
  if (s.system.lastPayload)    socket.emit('system:update',    s.system.lastPayload);
  if (s.wireless.lastPayload  && _mayReplay(socket, 'wireless'))  socket.emit('wireless:update',  s.wireless.lastPayload);
  if (s.vpn.lastPayload       && _mayReplay(socket, 'vpn'))       socket.emit('vpn:update',       s.vpn.lastPayload);
  if (s.ifStatus.lastPayload  && _mayReplay(socket, 'ifStatus'))  socket.emit('ifstatus:update',  s.ifStatus.lastPayload);
  if (s.ifStatus.lastPayload) {
    // Names only — the traffic picker and sidebar badge need these whatever the
    // role allows, and they disclose nothing the Interfaces page would have.
    const _ifs = s.ifStatus.lastPayload.interfaces || [];
    socket.emit('ifstatus:names', {
      ts: s.ifStatus.lastPayload.ts,
      total: _ifs.length,
      interfaces: _ifs.map(i => ({ name: i.name, running: !!i.running, disabled: !!i.disabled })),
    });
  }
  if (s.firewall.lastPayload  && _mayReplay(socket, 'firewall'))  socket.emit('firewall:update',  s.firewall.lastPayload);
  if (s.conns.lastPayload     && _mayReplay(socket, 'conns')) {
    socket.emit('conn:update', s.conns.lastPayload);
    if (s.conns.lastPayload.sourceDests)
      socket.emit('conn:source-data', { ts: s.conns.lastPayload.ts, sourceDests: s.conns.lastPayload.sourceDests, sourcePorts: s.conns.lastPayload.sourcePorts });
  }
  if (s.talkers.lastPayload   && _mayReplay(socket, 'talkers'))   socket.emit('talkers:update',   s.talkers.lastPayload);
  if (s.ping.lastPayload      && _mayReplay(socket, 'ping'))      socket.emit('ping:update',      s.ping.lastPayload);
  if (s.bandwidth.lastPayload && _mayReplay(socket, 'bandwidth')) socket.emit('bandwidth:update', s.bandwidth.lastPayload);
  if (s.routing.lastPayload   && _mayReplay(socket, 'routing'))   socket.emit('routing:update',   s.routing.lastPayload);
  if (s.netwatch.lastPayload  && _mayReplay(socket, 'netwatch'))  socket.emit('netwatch:update',  s.netwatch.lastPayload);

  // The bell's initial state. Without this it would start empty on every load
  // and only fill as new alerts happened — which is exactly the "empty again
  // after a refresh while the database holds open alerts" problem this replaces.
  // Recently-resolved rows ride along so the panel shows what just happened as
  // well as what is still wrong.
  try {
    // Built once for up to 250 rows rather than per row.
    const _alertNames = new Map(Routers.loadAll().map(r => [r.id, r.label || r.host]));
    socket.emit('alerts:open', {
      routerId: s.routerId,
      open:     db.queryOpenAlerts(s.routerId, 200).map(r => _alertRow(r, _alertNames)),
      recent:   db.queryRecentAlerts(s.routerId, Date.now() - 24 * 3600 * 1000, 50)
                  .map(r => _alertRow(r, _alertNames)),
    });
  } catch (e) {
    console.warn('[alerts] initial state failed:', sanitizeErr(e));
  }
  if (s.topology.lastPayload && _mayReplay(socket, 'topology')) socket.emit('topology:update', s.topology.lastPayload);

  socket.emit('settings:pages', _pageSettings(_ps));

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
  if (logHistory.length && _mayReplay(socket, 'logs')) socket.emit('logs:history', logHistory);
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
  session.topology.suspend();
  session.ping.suspend();
  session.talkers.suspend();
  session.dhcpNetworks.suspend();
}

function _idleResume(session, entry) {
  if (!session || !entry.startupReady) return;
  session.conns.resume();
  session.ifStatus.resume();
  session.system.resume();
  _updateAllPageStreams(session, entry);
  session.ping.resume();
  session.talkers.resume();
  session.dhcpNetworks.resume();
}

// Room-driven suspend/resume for the page-aware collectors: each keeps
// streaming only while at least one socket is in one of its rooms. The keys
// double as the session property and page name (session.firewall ↔ 'firewall').
const _PAGE_STREAM_ROOMS = Pages.STREAM_ROOMS;

function _updatePageStream(session, entry, which) {
  if (!session || !entry.startupReady) return;
  const rid = session.routerId;
  const viewers = _PAGE_STREAM_ROOMS[which].reduce(
    (n, room) => n + (io.sockets.adapter.rooms.get('router-' + rid + '-' + room)?.size || 0), 0);
  if (viewers > 0) session[which].resume(); else session[which].suspend();
}

function _updateAllPageStreams(session, entry) {
  for (const which of Object.keys(_PAGE_STREAM_ROOMS)) _updatePageStream(session, entry, which);
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
    { name: 'firewall',     streams: s.firewall._tableStream ? 1 : 0 },
    { name: 'dhcpNetworks', streams: countObj(s.dhcpNetworks._streams) },
    { name: 'ifStatus',     streams: (s.ifStatus._ifStream?1:0)+(s.ifStatus._addrStream?1:0)+(s.ifStatus._monitorStream?1:0) },
    { name: 'routing',      streams: (s.routing._routeStream?1:0)+(s.routing._ipv6Stream?1:0)+(s.routing._bgpStream?1:0) },
    { name: 'topology',     streams: s.topology._stream      ? 1 : 0 },
  ];
  const total = collectors.reduce((sum, c) => sum + c.streams, 0);
  // Geo availability rides along here so a failed geoip-lite load is visible in
  // the UI rather than only in the container log. Without it the world map and
  // country breakdowns just render empty and look like a router with no
  // traffic. See issue #101.
  const payload = {
    ts: Date.now(), total, collectors,
    geo: { available: geo.available(), reason: geo.unavailableReason() },
  };
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

  // Routers exist, but this session may read none of them. Impossible under the
  // old model — an empty allowedRouterIds meant "all" — and newly reachable now
  // that access is deny-by-default. Say so explicitly: otherwise the client
  // cannot tell this apart from "no routers configured yet" and shows the
  // first-run setup wizard, or simply spins.
  if (_isModern() && !routerId && Routers.loadAll().length > 0) {
    socket.emit('access:none');
  }

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
    _updateAllPageStreams(e.session, e);
  });

  // Page-aware rooms — clients join/leave rooms as they navigate pages.
  socket.on('page:focus', (name) => {
    if (typeof name !== 'string' || !/^[a-z]{2,20}$/.test(name)) return;
    const rid = socket.routerId;
    // Two things happen below — a room join AND an immediate replay of
    // lastPayload straight to this socket. Gating only the join would still
    // hand the caller a full payload for a page they cannot see, so this
    // returns before both.
    if (!_pageAllowed(socket, name)) return;
    socket.join('router-' + rid + '-page-' + name);
    if (name === 'routers') {
      if (!_routersPageSockets.has(socket.id)) {
        _routersPageSockets.add(socket.id);
        if (_routersPageSockets.size === 1) overviewSessions.resume();
      }
      if (_routersTimer) clearInterval(_routersTimer);
      const _emitRouters = () => socket.emit('routers:stats', _buildRoutersStats(socket));
      _emitRouters();
      _routersTimer = setInterval(_emitRouters, 2000); // codeql[js/resource-exhaustion]
    }
    const e = rid ? _routerSessions.get(rid) : null;
    if (!e || !e.session) return;
    const s = e.session;
    if (_PAGE_STREAM_ROOMS[name]) {
      _updatePageStream(s, e, name);
      if (s[name] && s[name].lastPayload)
        socket.emit(name + ':update', { ...s[name].lastPayload, ts: Date.now() });
    }
    if (name === 'bandwidth' && s.bandwidth && s.bandwidth.lastPayload)
      socket.emit('bandwidth:update', { ...s.bandwidth.lastPayload, ts: Date.now() });
    if (name === 'logs' && s.logs)
      socket.emit('logs:history', { entries: s.logs.getHistory() });
    // Interfaces and Topology both render the full interface payload, and
    // neither has a suspendable stream to replay through _PAGE_STREAM_ROOMS —
    // so without this, opening either shows nothing until the next tick now
    // that the emit is page-scoped (issue #108).
    if ((name === 'interfaces' || name === 'topology') && s.ifStatus && s.ifStatus.lastPayload)
      socket.emit('ifstatus:update', { ...s.ifStatus.lastPayload, ts: Date.now() });
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
    if (_PAGE_STREAM_ROOMS[name]) _updatePageStream(e.session, e, name);
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
    // A dashboard card needs the dashboard AND the page it borrows its data
    // from. Denying someone the Firewall page while still streaming them
    // firewall data through a dashboard card would make the whole matrix a lie,
    // and that is the first thing an operator will check.
    if (!_pageAllowed(socket, 'dashboard')) return;
    const src = _dashCardPage(key);
    if (src !== 'dashboard' && !_pageAllowed(socket, src)) return;
    socket.join('router-' + rid + '-dash-card-' + key);
    const e = rid ? _routerSessions.get(rid) : null;
    if (!e || !e.session) return;
    const s = e.session;
    if (key === 'firewall' || key === 'vpn') {
      _updatePageStream(s, e, key);
      if (s[key] && s[key].lastPayload)
        socket.emit(key + ':update', { ...s[key].lastPayload, ts: Date.now() });
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
    if (key === 'firewall' || key === 'vpn') _updatePageStream(e.session, e, key);
    if (key === 'diagnostics') {
      const viewers = io.sockets.adapter.rooms.get('router-' + rid + '-dash-card-diagnostics')?.size || 0;
      if (!viewers && e._diagTimer) { clearInterval(e._diagTimer); e._diagTimer = null; }
    }
  });

  // Active firewall tab — switch the single table stream to the selected table.
  socket.on('firewall:tab', (table) => {
    if (!['filter', 'nat', 'mangle', 'raw'].includes(table)) return;
    const rid = socket.routerId;
    // The active table is shared session state streamed to every viewer of this
    // router — only sockets actually viewing the firewall page/card may drive it.
    if (!socket.rooms.has('router-' + rid + '-page-firewall') &&
        !socket.rooms.has('router-' + rid + '-dash-card-firewall')) return;
    // Room membership says who is WATCHING; it never said who may change what
    // everyone else sees. A viewer could switch the table out from under every
    // other viewer of this router. Changing it is a diagnostic action.
    if (!_pageAllowed(socket, 'firewall', 'write')) return;
    const e = rid ? _routerSessions.get(rid) : null;
    if (e && e.session && e.session.firewall) e.session.firewall.setActiveTable(table);
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
    // A disabled router's session was deliberately torn down — don't let any
    // client resurrect it via router:switch.
    if (router.disabled) return;

    // The session was re-resolved just above rather than trusting the ≤60s-stale
    // cached view, so this asks the live grant set.
    if (!_socketCan(socket, 'router:read', newRouterId)) return;

    const oldRid = socket.routerId;
    if (oldRid === newRouterId) return;

    // Reset this socket's UI before it starts receiving the new router's data.
    // Only the global (authMode 'none') hot-swap used to emit this, so under
    // modern auth — where a switch comes through here instead — none of the
    // browser's reset handlers ever fired and every card kept the previous
    // router's rendered rows. Emitted before the replay below, so the reset
    // cannot land on top of the new router's first payload.
    socket.emit('router:switching', { routerId: newRouterId, label: router.label });

    // Detach from the old session's traffic collector — otherwise it keeps this
    // socket's interface in its monitor-traffic stream until disconnect.
    const oldEntry = oldRid ? _routerSessions.get(oldRid) : null;
    if (oldEntry && oldEntry.session) oldEntry.session.traffic.unbindSocket(socket);

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
server.listen(PORT, () => console.log('%s', `[MikroDash] v${APP_VERSION} listening on http://0.0.0.0:${PORT}`));

// ── Graceful shutdown ─────────────────────────────────────────────────────────
function shutdown(signal) {
  console.log('%s', `[MikroDash] ${signal} received, shutting down…`);
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
