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
const audit    = require('./audit');

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
const selfGuard            = require('./routeros/selfGuard');
const queueGuard           = require('./routeros/queueGuard');
const wanGuard             = require('./routeros/wanGuard');
const selfPath             = require('./routeros/selfPath');
const fwGuard              = require('./routeros/fwGuard');
const history              = require('./routeros/history');
const Resources            = require('./routeros/resources');
const Pdf                  = require('./reports/pdf');
const Format               = require('./reports/format');
const Reports              = require('./reports/build');
const ReportSchedules      = require('./reports/schedules');
const ReportScheduler      = require('./reports/scheduler');
const crypto               = require('node:crypto');
const Backups              = require('./backups');
const BackupStore          = require('./backups/store');
const BackupDiff           = require('./backups/diff');
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
        LEGACY_STREAM_KEYS, COLLECTORS: _COLLECTOR_DEFS } = require('./collection');
const { makeNullCollector } = require('./collectors/nullCollector');
const { createDormancyState, payloadEmpty } = require('./collectors/util');
const WirelessCollector    = require('./collectors/wireless');
const VpnCollector         = require('./collectors/vpn');
const FirewallCollector    = require('./collectors/firewall');
const InterfaceStatusCollector = require('./collectors/interfaceStatus');
const PingCollector         = require('./collectors/ping');
const BandwidthCollector    = require('./collectors/bandwidth');
const RoutingCollector      = require('./collectors/routing');
const NetwatchCollector     = require('./collectors/netwatch');
const TopologyCollector     = require('./collectors/topology');
const VlansCollector        = require('./collectors/vlans');
const PppCollector          = require('./collectors/ppp');
const BridgesCollector      = require('./collectors/bridges');
const DnsCollector          = require('./collectors/dns');
const CapsmanCollector      = require('./collectors/capsman');
const PackagesCollector     = require('./collectors/packages');
const RosUsersCollector     = require('./collectors/rosusers');
const QueuesCollector       = require('./collectors/queues');
const WanCollector          = require('./collectors/wan');
const alerter               = require('./alerter');
const notifier              = require('./notifier');
const alertSessions         = require('./alertSessions');
const wifiScanLib           = require('./wifiScan');
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
    // A frequency scan takes the radio off the air, so the client list collapses
    // to zero for its duration. That is the operator's own doing, not an
    // outage — evaluating alerts on it would page somebody about a button they
    // just pressed. The emit still goes out, so the page shows the truth.
    if (event === 'wireless:update' && wifiScans.isScanning(routerId)) return;
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

/**
 * Unauthenticated by necessity, not by category: the router fetching its own
 * backup cannot hold a session cookie. Guarded by a single-use capability token
 * bound to one backup, one router and one source address — see
 * _mintRestoreToken. Matched by prefix because the id is in the path.
 */
const _MODERN_PUBLIC_PREFIXES = ['/api/backups/'];
const _isPublicPath = (path) =>
  _MODERN_PUBLIC.has(path) ||
  _MODERN_PUBLIC_PREFIXES.some(p => path.startsWith(p) && path.endsWith('/raw'));

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
  if (_isPublicPath(req.path) || req.path.startsWith('/vendor/')) return next();
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
        // socket kept streaming a router its owner had just lost. Leaving the
        // rooms is what stops most of the data — the notice is only so the page
        // can explain itself.
        //
        // "Most", not all: the traffic collector is the one that does NOT
        // deliver through a room. It emits `traffic:update` straight to each
        // subscribed socket, once a second, so a socket that has left every
        // room keeps receiving samples for its selected interface until it
        // happens to disconnect. Unbinding is what actually stops it, and it
        // has to run while `socket.routerId` still names the router whose
        // session owns the subscription.
        if (socket.routerId && !_socketCan(socket, 'router:read', socket.routerId)) {
          const revoked = _routerSessions.get(socket.routerId);
          if (revoked && revoked.session && revoked.session.traffic) {
            revoked.session.traffic.unbindSocket(socket);
          }
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
    lastVlansTs:0, lastVlansErr:null,
    lastPppTs:0, lastPppErr:null,
    lastBridgesTs:0, lastBridgesErr:null,
    lastDnsTs:0, lastDnsErr:null,
    lastCapsmanTs:0, lastCapsmanErr:null,
    lastPackagesTs:0, lastPackagesErr:null,
    lastRosusersTs:0, lastRosusersErr:null,
    lastQueuesTs:0, lastQueuesErr:null,
    lastWanTs:0, lastWanErr:null,
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

  // vlans is constructed after ifStatus and dhcpLeases: it reads their
  // lastPayload by reference, so they must exist first. Both are DISABLEABLE,
  // so it guards for a null-collector stub rather than assuming a payload.
  const vlans        = _on('vlans',    () => new VlansCollector       ({ros, io:routerIo, pollMs:eff.poll.vlans, state, ifStatus, dhcpLeases, streamMode:eff.stream.vlans}));
  const ppp          = _on('ppp',      () => new PppCollector         ({ros, io:routerIo, pollMs:eff.poll.ppp,   state, streamMode:eff.stream.ppp}));
  // bridges borrows rates from ifStatus by reference, so it is constructed after
  // it for the same reason vlans is.
  const bridges      = _on('bridges',  () => new BridgesCollector     ({ros, io:routerIo, pollMs:eff.poll.bridges,  state, ifStatus, streamMode:eff.stream.bridges}));
  const dns          = _on('dns',      () => new DnsCollector         ({ros, io:routerIo, pollMs:eff.poll.dns,      state}));
  const capsman      = _on('capsman',  () => new CapsmanCollector     ({ros, io:routerIo, pollMs:eff.poll.capsman,  state, streamMode:eff.stream.capsman}));
  const packages     = _on('packages', () => new PackagesCollector    ({ros, io:routerIo, pollMs:eff.poll.packages, state}));
  // Both usernames, deliberately: the fingerprint in collection.js does not
  // cover credentials, so a username edit does not rebuild the session and the
  // live login can differ from routers.json indefinitely. selfGuard protects
  // whichever is which. See src/routeros/selfGuard.js.
  const rosusers     = _on('rosusers', () => new RosUsersCollector    ({ros, io:routerIo, pollMs:eff.poll.rosusers, state,
                                                                        usernames:[(ros.cfg||{}).username, routerCfg.username]}));
  // queues borrows the firewall collector BY REFERENCE for its FastTrack
  // summary, so it is constructed after it — the same ordering reason vlans and
  // bridges are constructed after ifStatus. Only a summary leaves the queues
  // payload; see the collector header.
  const queues       = _on('queues',   () => new QueuesCollector      ({ros, io:routerIo, pollMs:eff.poll.queues,   state, streamMode:eff.stream.queues, firewall}));
  // wan borrows rates from ifStatus by reference, so it is constructed after it
  // for the same reason vlans and bridges are.
  const wan          = _on('wan',      () => new WanCollector         ({ros, io:routerIo, pollMs:eff.poll.wan,      state, streamMode:eff.stream.wan, ifStatus}));
  const allCollectors = [traffic, dhcpLeases, dhcpNetworks, arp, conns, talkers, logs, system, wireless, vpn, firewall, ifStatus, ping, bandwidth, routing, netwatch, topology, vlans, ppp, bridges, dns, capsman, packages, rosusers, queues, wan];

  return { ros, state, connTableCache, DEFAULT_IF, HISTORY_MINUTES, collection: eff,
           dhcpLeases, dhcpNetworks, arp, traffic, conns, talkers, logs, system,
           wireless, vpn, firewall, ifStatus, ping, bandwidth, routing, netwatch, topology,
           vlans, ppp, bridges, dns, capsman, packages, rosusers, queues, wan, allCollectors,
           routerId: routerCfg.id, cachedInterfaces: null };
}

// ── Session teardown ──────────────────────────────────────────────────────────
// Stop all collectors and the ROS connection. `entry` is the _routerSessions entry.
async function teardownSession(session, entry) {
  if (!session) return;
  // The scan holds a reference to this session's ros. Letting the registry entry
  // outlive the session means finish() would later dereference a dead
  // connection, and the router would keep scanning with nothing tracking it.
  if (session.routerId) wifiScans.abortAllForRouter(session.routerId, 'session-restart');
  const _tearLabel = (session.ros && session.ros.routerLabel) || 'router';
  console.log('%s', `[${_tearLabel}] ── session torn down`);
  if (entry) { entry.startupReady = false; entry.collectorsStarted = false; }
  if (entry && entry._diagTimer) { clearInterval(entry._diagTimer); entry._diagTimer = null; }
  if (entry && entry._dormancyTimer) { clearInterval(entry._dormancyTimer); entry._dormancyTimer = null; }
  // Dormancy is per-session state: a rebuilt session re-probes everything, which
  // is what makes a router switch or a settings change a clean slate.
  if (entry) entry._dormancy = null;
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
    //
    // Dormancy is cleared first, and deliberately: a reconnect may follow the
    // RouterOS upgrade or package install that turns an "unknown command" into a
    // working menu, so every verdict from before the disconnect is stale.
    _resetDormancy(session, entry);
    _resumeCollector(session, entry, 'conns');
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
        audit.system().record({ action: 'router.autoname', targetType: 'router',
          targetId: session.routerId, targetName: boardName, routerId: session.routerId });
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
    await _delay(300);
    await session.vlans.start();
    await _delay(300);
    await session.ppp.start();
    await _delay(300);
    await session.bridges.start();
    await _delay(300);
    await session.dns.start();
    await _delay(300);
    await session.capsman.start();
    await _delay(300);
    await session.packages.start();
    await _delay(300);
    await session.rosusers.start();
    await _delay(300);
    await session.queues.start();
    await _delay(300);
    await session.wan.start();

    entry.startupReady = true;
    console.log('[MikroDash] All collectors running');
    if (entry.routerIo) {
      entry.routerIo.emit('collection:config', _collectionPayload(session.routerId, session));
    }
    _startDormancy(entry);

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
    if (Routers.updateIdentity(routerId, identity)) {
      audit.system().record({ action: 'router.identity', targetType: 'router',
        targetId: routerId, routerId, after: identity });
      _broadcastRoutersList();
    }
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

/**
 * A connection dedicated to moving a file off a router.
 *
 * `rawBytes` makes the receiver decode losslessly, which is required for the
 * binary backup and wrong for everything else — so this is deliberately NOT
 * the session's shared connection. It lives for the length of one backup.
 */
function _backupConnect(router) {
  const ros = new ROS({
    host: router.host,
    port: router.port || 8729,
    tls: router.tls === false ? false : { rejectUnauthorized: !router.tlsInsecure },
    username: router.username,
    password: router.password,
    writeTimeoutMs: 120000,
    rawBytes: true,
  });
  ros.connectLoop();
  return ros.waitUntilConnected(30000).then(() => ros).catch((e) => {
    try { ros.stop(); } catch (_) { /* never leave the socket behind */ }
    throw e;
  });
}

Backups.start({
  db,
  schedules: Routers.BACKUP_SCHEDULES,
  getRouters: () => Routers.loadAll(),
  connect: _backupConnect,
  queue: (rid, fn) => _routerWriteQueue(rid, fn),
  log: (msg) => console.log('%s', msg),
  notify: (kind, title, body) => {
    const s = Settings.load();
    const on = kind === 'drift' ? s.notifBackupDrift !== false : s.notifBackupFail !== false;
    if (!on) return;
    notifier.send(s, title, body).catch(() => { /* delivery is best effort */ });
  },
  onResult: (router, result) => {
    // Anyone looking at the page sees the run land without reloading.
    io.to('router-' + router.id + '-page-backups').emit('backups:ran', {
      routerId: router.id, outcome: result.outcome, error: result.error || null,
    });
  },
});

// Scheduled email reports (#60). Same injected shape as the backup scheduler,
// so the whole thing is drivable in a test with no database, mail server or
// clock — see test/report-schedules.test.js.
ReportScheduler.start({
  db,
  settings: () => Settings.load(),
  isModern: () => _isModern(),
  getRouter: (rid) => Routers.getById(rid),
  buildReport: (section, opts) => Reports.build(section, opts),
  // Re-asked at send time, never trusted from creation time: a report must not
  // keep emailing a router's history after its creator loses access to it.
  canRead: (userId, routerId) => Rbac.can({ userId }, 'router:history', routerId),
  mail: (settings, message) => notifier.sendMail(settings, message),
  // Deliberately the multi-channel send(), not sendMail(): telling someone over
  // SMTP that SMTP is broken reaches nobody.
  notifyFailure: (title, body) => {
    const cfg = Settings.load();
    if (cfg.notifReportFail === false) return;
    notifier.send(cfg, title, body).catch(() => {});
  },
  log: (msg) => console.log('%s', msg),
});
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
      // The claimed name, not a resolved one: a failed login may name a user
      // that does not exist, and that is worth seeing.
      audit.forLogin(req, username).denied({ action: 'auth.login', targetType: 'user', targetName: username });
      return res.status(401).json({ ok: false, error: 'Invalid username or password' });
    }
    const timeoutMs   = _sessionTimeoutMs();
    const { token, expiresAt } = SessionStore.createSession(user.id, user.username, user.role, timeoutMs, user.allowedRouterIds);
    res.setHeader('Set-Cookie', SessionStore.buildCookieHeader(token, expiresAt));
    console.log('%s', `[auth] login — user="${user.username}" role=${user.role} ip=${_clientIp(req)}`);
    audit.forLogin(req, user.username).record({ action: 'auth.login', targetType: 'user',
      targetId: user.id, targetName: user.username });
    res.json({ ok: true, role: user.role, username: user.username });
  } catch (e) {
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// GET /api/auth/logout
//
// Rate limiting is real but invisible to CodeQL: authLimiter is applied to
// every path except /healthz by the app.use() above, inside a callback the
// analysis cannot follow back to this handler. Measured against the running
// container: 100 requests answered, the next 10 got 429.
//
// A route-level limiter would be the wrong fix, not a belt-and-braces one —
// it would consume two of the 100/min budget per request, which is exactly
// what the note above the /login route warns against.
app.get('/api/auth/logout', (req, res) => { // codeql[js/missing-rate-limiting]
  const token = SessionStore.parseCookieHeader(req.headers.cookie || '')['mikrodash_sid'];
  const session = token ? SessionStore.getSession(token) : null;
  if (token) SessionStore.deleteSession(token);
  res.setHeader('Set-Cookie', SessionStore.clearCookieHeader());
  if (session) {
    console.log('%s', `[auth] logout — user="${session.username}" ip=${_clientIp(req)}`);
    audit.forLogin(req, session.username).record({ action: 'auth.logout', targetType: 'user',
      targetId: session.userId, targetName: session.username });
  }
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
  audit.fromReq(req).record({ action: 'account.active-router', targetType: 'router',
    targetId: routerId, targetName: router.label || router.host, routerId });
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
      // A public route that mints an administrator. It is the single most
      // consequential write in the app and had no record of any kind.
      audit.forLogin(req, user.username).record({ action: 'auth.setup', targetType: 'user',
        targetId: user.id, targetName: user.username, note: 'initial administrator created' });
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
    audit.fromReq(req).record({ action: 'user.create', targetType: 'user',
      targetId: user.id, targetName: user.username });
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
    const _before = Users.getUserSync(id);
    const updated = await Users.updateUser(id, updates);
    if (!updated) return res.status(404).json({ ok: false, error: 'User not found' });
    // `updates` may carry a password; audit.js redacts it by field name.
    audit.fromReq(req).record({ action: 'user.update', targetType: 'user', targetId: id,
      targetName: updated.username || id, before: _before, after: updates });
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
    const _before = Users.getUserSync(id);
    const deleted = await Users.deleteUser(id);
    if (!deleted) return res.status(404).json({ ok: false, error: 'User not found' });
    audit.fromReq(req).record({ action: 'user.delete', targetType: 'user', targetId: id,
      targetName: _before && _before.username ? _before.username : id,
      note: 'grants, layouts and notification config removed with the account' });
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
    audit.fromReq(req).record({ action: 'account.password', targetType: 'user',
      targetId: req.authSession.userId, targetName: req.authSession.username,
      extra: { otherSessionsRevoked: revoked.length } });
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
  audit.fromReq(req).record({ action: 'account.sessions.revoke', targetType: 'user',
    targetId: req.authSession.userId, targetName: req.authSession.username,
    extra: { revoked: revoked.length } });
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
    audit.fromReq(req).record({ action: 'layout.update', targetType: 'layout',
      targetName: 'dashboard' });
    res.json({ ok: true });
  } catch (e) {
    console.error('[dashboard-layout] save failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

// ── Nav preferences API ───────────────────────────────────────────────────────
// Whether the sidebar is grouped into categories, and which categories are open.
// Same storage as the layouts above — one opaque blob per user, keyed by kind —
// so the '_shared' identity for authMode 'none' and the delete-user cascade come
// for free.
//
// RATE LIMIT ONLY, NO PERMISSION CHECK, and that is deliberate rather than an
// omission. _modernAuthMiddleware has already 401'd anyone unauthenticated, and
// this preference discloses nothing: not a router, not page data, not even which
// pages exist. Copying _requireDashboard's canPageAnywhere here would lock a
// Read Only user out of their own sidebar — the one thing every signed-in user
// has, whatever their role.
app.get('/api/nav-prefs', layoutLimiter, (req, res) => {
  try { res.json(db.getLayout(_layoutUser(req), 'nav')); } catch (_) { res.json(null); }
});

app.post('/api/nav-prefs', layoutLimiter, (req, res) => {
  try {
    const body = req.body || {};
    if (typeof body.grouped !== 'boolean') return res.status(400).json({ ok: false });
    if (!Array.isArray(body.expanded))     return res.status(400).json({ ok: false });
    // Filtered through the registry rather than stored as sent. An unbounded
    // list of arbitrary strings inside a blob that later gets rendered is how a
    // preference becomes a stored-XSS vector; there are only ever a handful of
    // category keys, and they are all known here.
    const expanded = [...new Set(body.expanded.map(String))]
      .filter(k => Pages.CATEGORY_KEYS.includes(k))
      .sort();
    db.setLayout(_layoutUser(req), 'nav', { grouped: body.grouped, expanded });
    // No audit row, unlike the dashboard layout above. Expanding a nav category
    // is up to 60 events a minute per user, and a trail that records sidebar
    // clicks is one nobody will read the important rows in.
    res.json({ ok: true });
  } catch (e) {
    console.error('[nav-prefs] save failed:', sanitizeErr(e));
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
    audit.fromReq(req).record({ action: 'layout.update', targetType: 'layout',
      targetName: 'topology', routerId: body.routerId || null });
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
      // Recorded here, not after the normal save below: this branch returns
      // early, so a single hook at the end of the handler would miss the one
      // settings write that replaces the entire file.
      audit.fromReq(req).record({ action: 'settings.reset', targetType: 'settings',
        note: 'all settings restored to defaults' });
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
      pollTopology:[5000,600000], pollVlans:[1000,60000], pollPpp:[1000,60000],
      pollBridges:[1000,60000], pollDns:[1000,60000], pollCapsman:[1000,60000],
      pollPackages:[5000,600000], pollRosusers:[5000,300000], pollQueues:[2000,60000], pollWan:[1000,60000],
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
                        'notifRouterUpdate','notifBgp','notifBackupDrift','notifBackupFail','notifReportFail',
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

    // Captured before the write, or "before" is just "after" again.
    const _prev  = Settings.load();
    const saved  = Settings.save(updates);
    audit.fromReq(req).record({ action: 'settings.update', targetType: 'settings',
      before: _prev, after: updates });
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

    const collectorMap = { conns:s.conns, talkers:s.talkers, system:s.system, wireless:s.wireless, vpn:s.vpn, firewall:s.firewall, ifStatus:s.ifStatus, ping:s.ping, arp:s.arp, dhcpNetworks:s.dhcpNetworks, bandwidth:s.bandwidth, routing:s.routing, vlans:s.vlans, ppp:s.ppp,
      topology:s.topology, bridges:s.bridges, dns:s.dns, capsman:s.capsman, packages:s.packages, rosusers:s.rosusers, queues:s.queues, wan:s.wan };
    const pollMap = { pollConns:'conns', pollTalkers:'talkers', pollSystem:'system', pollWireless:'wireless',
      pollVpn:'vpn', pollFirewall:'firewall', pollIfstatus:'ifStatus', pollBandwidth:'bandwidth',
      pollPing:'ping', pollArp:'arp', pollDhcp:'dhcpNetworks', pollRouting:'routing',
      // These five were missing, and pollTopology/pollVlans/pollPpp with them:
      // the sliders existed and the bounds existed, but with no entry here (and
      // none in intFields below) the value was dropped on save and never
      // reached the collector. Adding four more poll keys to that hole would
      // have made it four times worse.
      pollTopology:'topology', pollVlans:'vlans', pollPpp:'ppp',
      pollBridges:'bridges', pollDns:'dns', pollCapsman:'capsman', pollPackages:'packages',
      pollRosusers:'rosusers', pollQueues:'queues', pollWan:'wan' };
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
    const _cfg = userNotify.save(req.authSession.userId, req.body || {});
    // The body carries channel credentials; audit.js redacts them by field name,
    // so only the fact that a destination changed is recorded.
    audit.fromReq(req).record({ action: 'account.notify', targetType: 'user',
      targetId: req.authSession.userId, targetName: req.authSession.username,
      note: 'personal notification channels updated' });
    res.json({ ok: true, config: _cfg });
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
// The collector registry, for the Router Settings modal's toggle grid.
//
// The grid used to be hand-written markup, and drifted: 21 collectors were
// disableable and only 11 had a toggle, so ten of them could be turned off only
// by editing routers.json by hand. Serving the registry makes that structurally
// impossible — a collector added to src/collection.js appears in the modal with
// no second edit, which is the same reason PAGE_NAV_MAP is derived rather than
// listed.
//
// No permission gate beyond the app's own: this is static metadata about what
// MikroDash can collect, identical for every router and every user, and it
// reveals nothing about any configured device.
app.get('/api/collectors', (_req, res) => {
  res.json({
    collectors: _COLLECTOR_DEFS
      .filter(c => c.disableable)
      .map(c => ({ key: c.key, label: c.label, requires: c.requires || [] })),
  });
});

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
    audit.fromReq(req).record({ action: 'router.create', targetType: 'router',
      targetId: router.id, targetName: router.label || router.host, routerId: router.id });
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
    const _before = Routers.getById(req.params.id);
    const router = Routers.update(req.params.id, body);
    audit.fromReq(req).record({ action: 'router.update', targetType: 'router',
      targetId: req.params.id, targetName: router ? (router.label || router.host) : req.params.id,
      routerId: req.params.id, before: _before, after: body });
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

    const _before = Routers.getById(deletedId);
    const deleted = Routers.remove(deletedId);
    audit.fromReq(req).record({ action: 'router.delete', targetType: 'router',
      targetId: deletedId, targetName: _before ? (_before.label || _before.host) : deletedId,
      routerId: deletedId,
      note: 'router-scoped grants and all stored history for this router were deleted with it' });
    if (!deleted) return res.status(404).json({ ok:false, error:'Router not found' });
    db.deleteGrantsForScope('router', deletedId);
    // A schedule for a router that no longer exists cannot run, and left
    // behind it is a live outbound email loop. Removed here, where it is
    // visible, rather than as a side effect of a retention sweep.
    db.deleteReportSchedulesForRouter(deletedId);
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
  audit.fromReq(req).record({ action: 'router.activate', targetType: 'router',
    targetId: req.params.id, routerId: req.params.id });
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
    audit.fromReq(req).record({ action: 'group.create', targetType: 'group',
      targetId: group.id, targetName: group.name });
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
    const _before = db.getGroup(req.params.id);
    const group = db.updateGroup(req.params.id, parsed.value);
    audit.fromReq(req).record({ action: 'group.update', targetType: 'group',
      targetId: req.params.id, targetName: group ? group.name : req.params.id,
      before: _before, after: parsed.value });
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
    const _before = db.getGroup(req.params.id);
    db.deleteGroup(req.params.id);
    audit.fromReq(req).record({ action: 'group.delete', targetType: 'group',
      targetId: req.params.id, targetName: _before ? _before.name : req.params.id });
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
    audit.fromReq(req).record({ action: 'grant.create', targetType: 'grant', targetId: grant.id,
      targetName: `${b.principalType}:${b.principalId} → ${roleId} @ ${b.scopeType}${scopeId ? ':' + scopeId : ''}`,
      // Router-scoped grants are recorded against that router, so whoever
      // administers it can see access to it being handed out.
      routerId: b.scopeType === 'router' ? scopeId : null });
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
    audit.fromReq(req).record({ action: 'grant.delete', targetType: 'grant', targetId: req.params.id });
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
    audit.fromReq(req).record({ action: 'role.create', targetType: 'role',
      targetId: role.id, targetName: role.name });
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

    const _before = db.getRole(req.params.id);
    const _beforePages = db.rolePages(req.params.id);
    const role = db.updateRole(req.params.id, parsed.value);
    audit.fromReq(req).record({ action: 'role.update', targetType: 'role',
      targetId: req.params.id, targetName: role ? role.name : req.params.id,
      before: { name: _before && _before.name, pages: _beforePages },
      after:  { name: parsed.value.name, pages: parsed.value.pages },
      note: 'a role edit changes the answer for every principal holding it' });
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
    const _before = db.getRole(req.params.id);
    db.deleteRole(req.params.id);
    audit.fromReq(req).record({ action: 'role.delete', targetType: 'role',
      targetId: req.params.id, targetName: _before ? _before.name : req.params.id });
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
    audit.fromReq(req).record({ action: 'site.create', targetType: 'site',
      targetId: site.id, targetName: site.name });
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
    const _before = db.getSite(req.params.id);
    const site = db.updateSite(req.params.id, parsed.value);
    audit.fromReq(req).record({ action: 'site.update', targetType: 'site',
      targetId: req.params.id, targetName: site ? site.name : req.params.id,
      before: _before, after: parsed.value });
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
      if (shouldBeHere && !isHere) {
        Routers.update(r.id, { siteId: req.params.id }); changed++;
        audit.fromReq(req).record({ action: 'router.site', targetType: 'router', targetId: r.id,
          targetName: r.label || r.host, routerId: r.id,
          before: { siteId: r.siteId || '' }, after: { siteId: req.params.id } });
      } else if (!shouldBeHere && isHere) {
        Routers.update(r.id, { siteId: '' }); changed++;
        audit.fromReq(req).record({ action: 'router.site', targetType: 'router', targetId: r.id,
          targetName: r.label || r.host, routerId: r.id,
          before: { siteId: r.siteId || '' }, after: { siteId: '' } });
      }
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
    const _before = db.getSite(req.params.id);
    db.deleteSite(req.params.id);
    audit.fromReq(req).record({ action: 'site.delete', targetType: 'site',
      targetId: req.params.id, targetName: _before ? _before.name : req.params.id,
      note: 'routers detached and site-scoped grants removed' });
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
      vlans: { ts:st.lastVlansTs, err:sanitizeErr(st.lastVlansErr) },
      ppp: { ts:st.lastPppTs, err:sanitizeErr(st.lastPppErr) },
      bridges: { ts:st.lastBridgesTs, err:sanitizeErr(st.lastBridgesErr) },
      dns: { ts:st.lastDnsTs, err:sanitizeErr(st.lastDnsErr) },
      capsman: { ts:st.lastCapsmanTs, err:sanitizeErr(st.lastCapsmanErr) },
      packages: { ts:st.lastPackagesTs, err:sanitizeErr(st.lastPackagesErr) },
      rosusers: { ts:st.lastRosusersTs, err:sanitizeErr(st.lastRosusersErr) },
      queues: { ts:st.lastQueuesTs, err:sanitizeErr(st.lastQueuesErr) },
      wan: { ts:st.lastWanTs, err:sanitizeErr(st.lastWanErr) },
    },
  };
  res.status(statusCode).json(body);
});

// ── Reports API ───────────────────────────────────────────────────────────────

const _AGG_VALID = new Set(['hour', 'day', 'week', 'month']);

// Moved to src/reports/format.js so the scheduled-report path shares them
// rather than growing a second copy. Rebound under their original names:
// the JSON report routes and the audit export still call them, and renaming
// would touch a dozen call sites for no behaviour change.
const _toCsv            = Format.toCsv;
const _tsFmt            = Format.tsFmt;
const _fmtDuration      = Format.fmtDuration;
const _fmtDataMB        = Format.fmtDataMB;
const _bucketNoun       = Format.bucketNoun;
const _annotateDowntime = Format.annotateDowntime;
const _maxOf            = Format.maxOf;

function _parseReportParams(query) {
  const routerId  = String(query.routerId || '');
  const from      = parseInt(query.from, 10) || 0;
  const to        = parseInt(query.to,   10) || Date.now();
  const aggregate = _AGG_VALID.has(query.aggregate) ? query.aggregate : '';
  return { routerId, from, to, aggregate };
}


// The renderer and its two sinks now live in src/reports/pdf.js, so a
// scheduled report can get a Buffer instead of an HTTP response. Same
// positional signature, so every call site below is untouched.
const _toPdf = Pdf.pipe;


// GET /api/reports/ping
// ── Restore capability tokens ────────────────────────────────────────────────
//
// A restore is the one direction that still needs the ROUTER to reach US:
// `/tool/fetch upload=yes` refuses anything but [s]ftp, so the file has to be
// pulled by the router over HTTP. It cannot present a session cookie, so
// `/api/backups/:id/raw` sits in _MODERN_PUBLIC — the same allow-list that
// holds /api/users/setup, and CLAUDE.md is explicit about why that one is
// dangerous.
//
// So it is constrained on every axis available. A token is:
//   - 32 random bytes, minted only by an operator-initiated restore
//   - bound to ONE backup id and ONE router
//   - single use: redeemed on first read, whether or not the read succeeds
//   - valid for 120 seconds
//   - checked against the router's configured host, so a token that leaks off
//     the box cannot be redeemed from anywhere else
//
// It can only ever READ one specific file. Nothing mints one on a schedule.
const _RESTORE_TOKEN_TTL_MS = 120000;
const _restoreTokens = new Map();

function _mintRestoreToken(backupId, router) {
  const token = crypto.randomBytes(32).toString('hex');
  _restoreTokens.set(token, {
    backupId: Number(backupId),
    routerId: router.id,
    host: router.host,
    expires: Date.now() + _RESTORE_TOKEN_TTL_MS,
  });
  // Never let a failed restore leave a live token behind.
  setTimeout(() => _restoreTokens.delete(token), _RESTORE_TOKEN_TTL_MS).unref();
  return token;
}

/**
 * Redeem a token, or explain why not.
 *
 * Deleted on the FIRST attempt regardless of outcome: a token that survives a
 * rejected read is a token an attacker may keep guessing conditions against.
 */
function _redeemRestoreToken(token, remoteIp) {
  const entry = _restoreTokens.get(String(token || ''));
  if (!entry) return { ok: false, reason: 'unknown-token' };
  _restoreTokens.delete(String(token));
  if (Date.now() > entry.expires) return { ok: false, reason: 'expired' };
  const ip = String(remoteIp || '').replace(/^::ffff:/, '');
  if (ip !== entry.host) return { ok: false, reason: 'wrong-source' };
  return { ok: true, entry };
}

/**
 * The only unauthenticated backup route, and the only one the router itself
 * calls. It serves one binary, once, to one address.
 */
app.get('/api/backups/:id/raw', (req, res) => {
  const verdict = _redeemRestoreToken(req.query.t, req.ip || (req.socket && req.socket.remoteAddress));
  if (!verdict.ok) {
    audit.system().record({ action: 'backup.raw.denied', targetType: 'backup',
      targetId: String(req.params.id), outcome: 'denied',
      extra: { reason: verdict.reason } });
    return res.status(403).json({ ok: false, error: 'forbidden' });
  }
  const row = db.getBackup(Number(req.params.id));
  if (!row || row.id !== verdict.entry.backupId || row.router_id !== verdict.entry.routerId ||
      !row.stem || row.pruned_at) {
    return res.status(404).json({ ok: false, error: 'not found' });
  }
  try {
    const buf = BackupStore.readBackup(row.dir, row.stem);
    audit.system().record({ action: 'backup.raw', targetType: 'backup', scope: 'router',
      routerId: row.router_id, targetId: String(row.id), extra: { bytes: buf.length } });
    res.setHeader('Content-Type', 'application/octet-stream');
    return res.send(buf);
  } catch (e) {
    console.error('%s', '[backup] raw read failed:', (e && e.message) || e);
    return res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// ── Backup downloads ─────────────────────────────────────────────────────────
//
// Both halves need `router:write`, not read. An export describes the entire
// network, and the binary carries every key on the device — so handing either
// to a browser is closer to taking a copy of the router than to reading a page.
//
// The row is looked up by id and its router is read from the ROW, never from
// the query: that is what makes Rbac.fromQuery('routerId') meaningful here,
// because a caller who names a router they may write cannot then be handed a
// backup belonging to a different one.
function _sendBackupPart(req, res, part) {
  const id = Number(req.params.id);
  const row = db.getBackup(id);
  if (!row || !row.stem || row.pruned_at) {
    return res.status(404).json({ ok: false, error: 'not found' });
  }
  if (row.router_id !== String(req.query.routerId || '')) {
    return res.status(404).json({ ok: false, error: 'not found' });
  }
  const router = Routers.getById(row.router_id);
  const name = (router ? BackupStore.slugFor(router.label) : 'router') + '-' + row.stem;
  try {
    if (part === 'rsc') {
      const text = BackupStore.readRsc(row.dir, row.stem);
      audit.fromReq(req).record({ action: 'backup.download', targetType: 'backup',
        scope: 'router', routerId: row.router_id, targetId: String(row.id),
        extra: { part: 'rsc' } });
      res.setHeader('Content-Type', 'text/plain; charset=utf-8');
      res.setHeader('Content-Disposition', 'attachment; filename="' + name + '.rsc"');
      return res.send(text);
    }
    const buf = BackupStore.readBackup(row.dir, row.stem);
    audit.fromReq(req).record({ action: 'backup.download', targetType: 'backup',
      scope: 'router', routerId: row.router_id, targetId: String(row.id),
      extra: { part: 'backup' } });
    res.setHeader('Content-Type', 'application/octet-stream');
    res.setHeader('Content-Disposition', 'attachment; filename="' + name + '.backup"');
    return res.send(buf);
  } catch (e) {
    console.error('%s', '[backup] download failed:', (e && e.message) || e);
    return res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
}

app.get('/api/backups/:id/rsc',
  Rbac.requirePerm('router:write', Rbac.fromQuery('routerId')),
  (req, res) => _sendBackupPart(req, res, 'rsc'));

app.get('/api/backups/:id/backup',
  Rbac.requirePerm('router:write', Rbac.fromQuery('routerId')),
  (req, res) => _sendBackupPart(req, res, 'backup'));

// ── Scheduled report schedules (#60) ─────────────────────────────────────────
//
// Reading the list is router:history: anyone who can already export a report
// may see what is scheduled, and visibility is itself a control — a mail-out
// nobody can see is the bad case.
//
// Creating one is router:schedule, a write-level grant, because a schedule
// mails router history to arbitrary third-party addresses indefinitely without
// anyone signing in again. Not router:write, which WRITE_CONFERS_ALWAYS would
// leak in from any write page at all.
//
// Every route that names a schedule reads its router from the ROW and 404s if
// it does not match the query, the pattern _sendBackupPart establishes: naming
// a router you may write must never reach a record belonging to another.

const _scheduleLimiter = rateLimit({ windowMs: 60_000, max: 30, standardHeaders: true, legacyHeaders: false });
const _sendNowLimiter  = rateLimit({ windowMs: 60_000, max: 5,  standardHeaders: true, legacyHeaders: false });

function _scheduleRow(req, res) {
  const row = db.getReportSchedule(req.params.id);
  if (!row || row.router_id !== String(req.query.routerId || '')) {
    res.status(404).json({ ok: false, error: 'not found' });
    return null;
  }
  return row;
}

app.get('/api/reports/schedules',
  Rbac.requirePerm('router:history', Rbac.fromQuery('routerId')), (req, res) => {
    const routerId = String(req.query.routerId || '');
    if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
    const cfg = Settings.load();
    res.json({
      ok: true,
      schedules: db.listReportSchedulesFor(routerId).map((row) => {
        const pub = ReportSchedules.toPublic(row);
        // The page shows when each last ran, so the list is useful without
        // opening the history for every row in turn.
        const last = db.listReportRuns(row.id, 1)[0];
        pub.lastRun = last ? { ran_at: last.ran_at, outcome: last.outcome } : null;
        return pub;
      }),
      // So the page can say "this will never send" at creation time rather than
      // in a run row a month later.
      smtpReady: !!(cfg.smtpHost && cfg.smtpFrom),
      permitted: Rbac.can(req.authSession, 'router:schedule', routerId),
      sections: Reports.SECTIONS,
      needsInterface: Reports.NEEDS_INTERFACE,
    });
  });

app.post('/api/reports/schedules', _scheduleLimiter,
  Rbac.requirePerm('router:schedule', Rbac.fromBody('routerId')), (req, res) => {
    const routerId = String((req.body && req.body.routerId) || '');
    if (!Routers.getById(routerId)) return res.status(404).json({ ok: false, error: 'not found' });
    try {
      const row = ReportSchedules.validate(req.body, {
        id: crypto.randomUUID(),
        routerId,
        createdBy: req.authSession ? req.authSession.userId : null,
      });
      const saved = db.upsertReportSchedule(row);
      audit.fromReq(req).record({ action: 'report.schedule.create', targetType: 'report-schedule',
        scope: 'router', routerId, targetId: row.id, targetName: row.name,
        extra: { frequency: row.frequency, sections: row.sections, recipients: row.recipients } });
      res.json({ ok: true, schedule: ReportSchedules.toPublic(saved) });
    } catch (e) {
      res.status(400).json({ ok: false, error: sanitizeErr(e) });
    }
  });

app.put('/api/reports/schedules/:id', _scheduleLimiter,
  Rbac.requirePerm('router:schedule', Rbac.fromQuery('routerId')), (req, res) => {
    const row = _scheduleRow(req, res);
    if (!row) return undefined;
    try {
      const next = ReportSchedules.validate(req.body, {
        id: row.id, routerId: row.router_id,
        createdBy: row.created_by, createdAt: row.created_at,
      });
      const saved = db.upsertReportSchedule(next);
      audit.fromReq(req).record({ action: 'report.schedule.update', targetType: 'report-schedule',
        scope: 'router', routerId: row.router_id, targetId: row.id, targetName: next.name,
        extra: { frequency: next.frequency, sections: next.sections, recipients: next.recipients,
                 enabled: next.enabled } });
      res.json({ ok: true, schedule: ReportSchedules.toPublic(saved) });
    } catch (e) {
      res.status(400).json({ ok: false, error: sanitizeErr(e) });
    }
  });

app.delete('/api/reports/schedules/:id', _scheduleLimiter,
  Rbac.requirePerm('router:schedule', Rbac.fromQuery('routerId')), (req, res) => {
    const row = _scheduleRow(req, res);
    if (!row) return undefined;
    db.deleteReportSchedule(row.id);
    audit.fromReq(req).record({ action: 'report.schedule.delete', targetType: 'report-schedule',
      scope: 'router', routerId: row.router_id, targetId: row.id, targetName: row.name });
    res.json({ ok: true });
  });

app.post('/api/reports/schedules/:id/run', _sendNowLimiter,
  Rbac.requirePerm('router:schedule', Rbac.fromQuery('routerId')), async (req, res) => {
    const row = _scheduleRow(req, res);
    if (!row) return undefined;
    const actor = req.authSession ? req.authSession.username : null;
    const result = await ReportScheduler.runNow(row, { actor });
    audit.fromReq(req).record({ action: 'report.schedule.send', targetType: 'report-schedule',
      scope: 'router', routerId: row.router_id, targetId: row.id, targetName: row.name,
      outcome: result.outcome === 'sent' ? 'ok' : 'error',
      extra: { outcome: result.outcome, bytes: result.bytes, sections: result.sections } });
    res.json({ ok: result.outcome === 'sent', outcome: result.outcome,
               error: result.error || null });
  });

app.get('/api/reports/schedules/:id/runs',
  Rbac.requirePerm('router:history', Rbac.fromQuery('routerId')), (req, res) => {
    const row = _scheduleRow(req, res);
    if (!row) return undefined;
    res.json({ ok: true, runs: db.listReportRuns(row.id, 20) });
  });

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
  const fmt = (req.query.format || 'csv').toLowerCase();
  const built = Reports.build('ping', { routerId, from, to, aggregate });
  if (fmt === 'pdf') {
    return _toPdf(built.title, built.pdf.columns, built.pdf.rows, res, built.pdf.meta);
  }
  res.setHeader('Content-Type', 'text/csv');
  res.setHeader('Content-Disposition', `attachment; filename="${built.csvFilename}"`);
  res.send(_toCsv(built.csv.rows, built.csv.columns));
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
// Moved to src/reports/build.js, where the export path needs it too.
const _ifaceSummary = Reports.ifaceSummary;

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
  const fmt = (req.query.format || 'csv').toLowerCase();
  const built = Reports.build('traffic', { routerId, iface, from, to, aggregate });
  if (fmt === 'pdf') {
    return _toPdf(built.title, built.pdf.columns, built.pdf.rows, res, built.pdf.meta);
  }
  res.setHeader('Content-Type', 'text/csv');
  res.setHeader('Content-Disposition', `attachment; filename="${built.csvFilename}"`);
  res.send(_toCsv(built.csv.rows, built.csv.columns));
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
  const fmt = (req.query.format || 'csv').toLowerCase();
  const built = Reports.build('bandwidth', { routerId, iface, from, to, aggregate });
  if (fmt === 'pdf') {
    return _toPdf(built.title, built.pdf.columns, built.pdf.rows, res, built.pdf.meta);
  }
  res.setHeader('Content-Type', 'text/csv');
  res.setHeader('Content-Disposition', `attachment; filename="${built.csvFilename}"`);
  res.send(_toCsv(built.csv.rows, built.csv.columns));
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

// Frequency-scan registry. Not a collector: src/collection.js models long-lived
// things with a poll interval and a session property, and this is a one-off
// action that takes a radio off the air for at most 35 seconds.
const wifiScans = wifiScanLib.createRegistry({ sanitize: sanitizeErr });

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
    audit.fromReq(req).record({ action: 'alert.ack', targetType: 'alert', targetId: String(id),
      routerId: owner });
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
    audit.fromReq(req).record({ action: 'alert.clear', targetType: 'alert', routerId: rid,
      extra: { cleared: ids.length } });
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
  const { routerId, from, to, aggregate } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const fmt = (req.query.format || 'csv').toLowerCase();
  const built = Reports.build('alerts', { routerId, from, to, aggregate });
  if (fmt === 'pdf') {
    return _toPdf(built.title, built.pdf.columns, built.pdf.rows, res, built.pdf.meta);
  }
  res.setHeader('Content-Type', 'text/csv');
  res.setHeader('Content-Disposition', `attachment; filename="${built.csvFilename}"`);
  res.send(_toCsv(built.csv.rows, built.csv.columns));
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
  const { routerId, from, to, aggregate } = _parseReportParams(req.query);
  if (!routerId) return res.status(400).json({ ok: false, error: 'routerId required' });
  const fmt = (req.query.format || 'csv').toLowerCase();
  const built = Reports.build('connectivity', { routerId, from, to, aggregate });
  if (fmt === 'pdf') {
    return _toPdf(built.title, built.pdf.columns, built.pdf.rows, res, built.pdf.meta);
  }
  res.setHeader('Content-Type', 'text/csv');
  res.setHeader('Content-Disposition', `attachment; filename="${built.csvFilename}"`);
  res.send(_toCsv(built.csv.rows, built.csv.columns));
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

// ── Audit trail ───────────────────────────────────────────────────────────────
//
// Deliberately not under /api/reports/*. Those are all
// requirePerm('router:history', fromQuery('routerId')) and answer 400 without a
// router; half the audit rows have no router at all. The gate here is per-ROW
// instead of per-request:
//
//   scope='app'     a user, role, grant or settings change — system:principals
//   scope='router'  filtered to the routers this session may see
//
// Both halves are resolved from the session and passed to the query, which
// returns nothing when neither applies. /api/db/stats mixes a global gate with a
// per-router filter the same way.
function _auditScope(req) {
  // Auth disabled means one local operator with full reach; matching what
  // Rbac.can() already does rather than inventing a second answer.
  if (!_isModern()) return { includeApp: true, routerIds: Routers.loadAll().map(r => r.id) };
  return {
    includeApp: Rbac.can(req.authSession, 'system:principals'),
    routerIds:  Rbac.effectiveRouterIds(req.authSession, 'router:history'),
  };
}

function _auditQuery(req) {
  const q = req.query || {};
  const scope = _auditScope(req);
  return {
    includeApp: scope.includeApp,
    // A routerId filter narrows the permitted set; it can never widen it.
    routerIds:  q.routerId && scope.routerIds.includes(String(q.routerId))
                  ? [String(q.routerId)] : scope.routerIds,
    from:    parseInt(q.from, 10) || 0,
    to:      parseInt(q.to, 10)   || Date.now(),
    actor:   q.actor   ? String(q.actor).slice(0, 100)   : '',
    action:  q.action  ? String(q.action).slice(0, 60)   : '',
    outcome: ['ok', 'denied', 'failed'].includes(q.outcome) ? q.outcome : '',
    search:  q.search  ? String(q.search).slice(0, 100)  : '',
    limit:   q.limit, offset: q.offset,
  };
}

// Any signed-in user may reach the page; what they SEE is decided per row, and a
// session with neither global administration nor router history gets an empty
// list rather than a 403 — the page is legitimately empty for them.
app.get('/api/audit', (req, res) => {
  try {
    const out = db.queryAuditEvents(_auditQuery(req));
    res.json({ ok: true, ...out, facets: db.auditFacets() });
  } catch (e) {
    console.error('[audit] query failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

app.get('/api/audit/export', (req, res) => {
  try {
    const q = _auditQuery(req);
    // Export is a snapshot of the filtered view, not the page — but still
    // bounded, because a PDF has no row cap of its own.
    const { rows } = db.queryAuditEvents({ ...q, limit: 1000, offset: 0 });
    const flat = rows.map(r => ({
      ts: _tsFmt(r.ts), actor: r.actor_name, ip: r.actor_ip || '', action: r.action,
      target: r.target_name || r.target_id || '', router: r.router_id || '',
      outcome: r.outcome, detail: r.detail || '',
    }));
    const cols = ['ts', 'actor', 'ip', 'action', 'target', 'router', 'outcome', 'detail'];
    if (String(req.query.format) === 'pdf') {
      return _toPdf('Audit Trail', cols, flat, res, { meta: `${flat.length} events` });
    }
    res.setHeader('Content-Type', 'text/csv; charset=utf-8');
    res.setHeader('Content-Disposition', 'attachment; filename="mikrodash-audit.csv"');
    res.send(_toCsv(flat, cols));
  } catch (e) {
    console.error('[audit] export failed:', sanitizeErr(e));
    res.status(500).json({ ok: false });
  }
});

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
    // audit_events is absent from PURGE_TABLES, so this row survives the very
    // purge it describes — which is the point of keeping it out.
    audit.fromReq(req).record({ action: 'db.purge', targetType: 'database',
      routerId: opts.routerId || null,
      extra: { deleted: result.deleted, types: opts.types || 'all',
               olderThanDays: req.body && req.body.olderThanDays } });
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

  if (decision.action === 'clear') {
    const done = !!Routers.updateGeoAuto(router.id, null);
    if (done) audit.system().record({ action: 'router.geo', targetType: 'router',
      targetId: router.id, routerId: router.id, note: 'auto location cleared' });
    return done;
  }
  const done = !!Routers.updateGeoAuto(router.id, decision.auto);
  if (done) audit.system().record({ action: 'router.geo', targetType: 'router',
    targetId: router.id, routerId: router.id, after: decision.auto });
  return done;
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
  socket.emit('collection:status', _dormancyPayload(entry));

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
  if (s.vlans.lastPayload && _mayReplay(socket, 'vlans')) socket.emit('vlans:update', s.vlans.lastPayload);
  if (s.ppp.lastPayload   && _mayReplay(socket, 'ppp'))   socket.emit('ppp:update',   s.ppp.lastPayload);
  if (s.bridges.lastPayload  && _mayReplay(socket, 'bridges'))  socket.emit('bridges:update',  s.bridges.lastPayload);
  if (s.dns.lastPayload      && _mayReplay(socket, 'dns'))      socket.emit('dns:update',      s.dns.lastPayload);
  if (s.capsman.lastPayload  && _mayReplay(socket, 'capsman'))  socket.emit('capsman:update',  s.capsman.lastPayload);
  if (s.packages.lastPayload && _mayReplay(socket, 'packages')) socket.emit('packages:update', s.packages.lastPayload);
  if (s.rosusers.lastPayload && _mayReplay(socket, 'rosusers')) socket.emit('rosusers:update', s.rosusers.lastPayload);
  if (s.queues.lastPayload   && _mayReplay(socket, 'queues'))   socket.emit('queues:update',   s.queues.lastPayload);
  if (s.wan.lastPayload      && _mayReplay(socket, 'wan'))      socket.emit('wan:update',      s.wan.lastPayload);

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

// ── Collector dormancy ────────────────────────────────────────────────────────
/*
 * A collector with nothing to report should stop holding a channel open, and the
 * card should say so rather than render a blank table that reads as a fault.
 *
 * One supervisor per session rather than a backoff loop inside each collector:
 * emptiness is declared once in the registry (`emptyKey`), so the judgement reads
 * `lastPayload` generically and no collector grows an emptiness hook.
 *
 * Three gates now decide whether a collector runs — idle (nobody on this router),
 * page rooms (nobody on its page) and dormancy. They are layered, not competing:
 * dormancy is a VETO consulted inside _resumeCollector(), which is the only place
 * anything is resumed. _idleResume() calling resume() directly is precisely what
 * would wake a dormant collector on the next socket join.
 */
const _DORMANCY_TICK_MS = 15000;
const _DORMANCY_DEFS    = _COLLECTOR_DEFS.filter(c => c.emptyKey && c.disableable);

function _dormancyState(entry, key) {
  if (!entry._dormancy) entry._dormancy = new Map();
  let st = entry._dormancy.get(key);
  if (!st) { st = createDormancyState(); entry._dormancy.set(key, st); }
  return st;
}

function _isDormant(entry, key) {
  const st = entry && entry._dormancy && entry._dormancy.get(key);
  return !!(st && st.dormant);
}

/**
 * The one place a collector is resumed. Every caller — _idleResume,
 * _updatePageStream, the reconnect handler — goes through here so a gate that
 * knows nothing about dormancy cannot undo it.
 */
function _resumeCollector(session, entry, key) {
  const coll = session && session[key];
  if (!coll || typeof coll.resume !== 'function') return;
  if (_isDormant(entry, key)) return;
  coll.resume();
}

/**
 * Look again at a collector we put to sleep. probe() where one exists — it clears
 * a capability latch that resume() deliberately honours — otherwise resume plus
 * refreshNow() where that exists, so the answer arrives on this tick rather than
 * one poll interval later.
 */
function _probeCollector(coll) {
  if (!coll) return;
  if (typeof coll.probe === 'function') { coll.probe(); return; }
  if (typeof coll.resume === 'function') coll.resume();
  if (typeof coll.refreshNow === 'function') {
    Promise.resolve(coll.refreshNow()).catch(() => { /* the collector reports its own errors */ });
  }
}

function _dormancyPayload(entry) {
  const dormant = [];
  if (entry._dormancy) for (const [k, st] of entry._dormancy) if (st.dormant) dormant.push(k);
  return { routerId: entry.session.routerId, dormant };
}

function _emitDormancy(entry) {
  if (entry.routerIo) entry.routerIo.emit('collection:status', _dormancyPayload(entry));
}

function _dormancyTick(entry) {
  const session = entry && entry.session;
  if (!session || !entry.startupReady || session._destroyed) return;
  // Judge only while somebody is watching this router. A suspended collector
  // emits nothing, so an idle session would otherwise read as universally empty
  // and put the whole set to sleep for a reason that has nothing to do with the
  // router.
  const rid = session.routerId;
  if ((io.sockets.adapter.rooms.get('router-' + rid)?.size || 0) === 0) return;

  const now = Date.now();
  let changed = false;

  for (const def of _DORMANCY_DEFS) {
    if (!session.collection.enabled[def.key]) continue;   // the user turned it off
    const coll = session[def.sessionProp];
    if (!coll) continue;
    const st = _dormancyState(entry, def.key);
    const p  = coll.lastPayload;

    if (p) {
      const verdict = st.observe({
        ts:          p.ts,
        empty:       payloadEmpty(p, def.emptyKey),
        unsupported: p.available === false,
      }, now);
      if (verdict === 'sleep') {
        changed = true;
        if (typeof coll.suspend === 'function') coll.suspend();
        console.log('%s', `[${session.ros.routerLabel}][dormancy] ${def.key} asleep — ` +
          (p.available === false ? 'not supported on this router' : 'nothing to report'));
      } else if (verdict === 'wake') {
        changed = true;
        console.log('%s', `[${session.ros.routerLabel}][dormancy] ${def.key} awake`);
        _resumeCollector(session, entry, def.key);
      }
    }

    if (st.dueForProbe(now)) { st.markProbed(now); _probeCollector(coll); }
  }

  if (changed) _emitDormancy(entry);
}

function _startDormancy(entry) {
  if (entry._dormancyTimer) return;
  entry._dormancyTimer = setInterval(() => {
    try { _dormancyTick(entry); }
    catch (e) { console.error('%s', '[dormancy] tick failed:', (e && e.message) || e); }
  }, _DORMANCY_TICK_MS);
  if (entry._dormancyTimer.unref) entry._dormancyTimer.unref();
}

/**
 * Clear every verdict and wake whatever was asleep. A reconnect may follow a
 * RouterOS upgrade or a package install, which is exactly the event that turns an
 * "unknown command" into a working menu.
 */
function _resetDormancy(session, entry) {
  if (!entry || !entry._dormancy) return;
  let had = false;
  for (const [key, st] of entry._dormancy) {
    if (st.dormant) { had = true; _probeCollector(session && session[key]); }
    st.reset();
  }
  if (had) _emitDormancy(entry);
}

/**
 * Somebody just opened the page this collector feeds. That is the cheapest and
 * most timely re-probe there is — a user who has just added a netwatch host opens
 * the NetWatch page next — so it pre-empts the backoff entirely.
 */
function _wakeForFocus(session, entry, key) {
  if (!_isDormant(entry, key)) return false;
  const st = entry._dormancy.get(key);
  st.reset();
  _probeCollector(session && session[key]);
  _emitDormancy(entry);
  return true;
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
  session.vlans.suspend();
  session.ppp.suspend();
  session.bridges.suspend();
  session.dns.suspend();
  session.capsman.suspend();
  session.packages.suspend();
  session.rosusers.suspend();
  session.queues.suspend();
  session.wan.suspend();
  session.ping.suspend();
  session.talkers.suspend();
  session.dhcpNetworks.suspend();
}

function _idleResume(session, entry) {
  if (!session || !entry.startupReady) return;
  // Every resume goes through _resumeCollector so dormancy can veto it. Calling
  // resume() directly here is what would wake a collector we had just put to
  // sleep, on the next socket join, forever.
  _resumeCollector(session, entry, 'conns');
  _resumeCollector(session, entry, 'ifStatus');
  _resumeCollector(session, entry, 'system');
  // Every page-scoped collector is resumed HERE and only here, from room
  // occupancy — so a collector whose page nobody is looking at stays suspended
  // even though a browser is connected. Resuming vlans/ppp/bridges/dns/capsman/
  // packages/rosusers/queues/wan by name used to happen below this line, which
  // is exactly what kept them polling the router from the Dashboard.
  _updateAllPageStreams(session, entry);
  // These three genuinely have no page of their own: ping feeds the dashboard
  // gauge and the alerter, talkers and dhcpNetworks feed dashboard cards. They
  // are suspended by name above and must be resumed by name.
  _resumeCollector(session, entry, 'ping');
  _resumeCollector(session, entry, 'talkers');
  _resumeCollector(session, entry, 'dhcpNetworks');
}

// Room-driven suspend/resume for the page-aware collectors: each keeps
// streaming only while at least one socket is in one of its rooms. The keys
// double as the session property and page name (session.firewall ↔ 'firewall').
const _PAGE_STREAM_ROOMS = Pages.STREAM_ROOMS;

// Pages whose collector holds no stream, so page:focus has to replay the last
// payload explicitly. Derived from the registry — a page belongs here precisely
// when it has a collector but no stream rooms — rather than listed by hand,
// because the hand-written version was already two pages out of date once.
// logs is excluded deliberately: its replay is a DIFFERENT event
// (logs:history, from a ring buffer), and emitting logs:update at the page
// would append the last batch a second time on every visit.
/**
 * Serialise router writes per router.
 *
 * Every write feature here reads the router's tables, checks them, and then
 * writes — and RouterOS offers no compare-and-swap, so two operators acting at
 * once could otherwise interleave an edit between another request's read and its
 * write, letting a write land on a row the check cleared under different values.
 * A promise chain per router makes the fresh read mean something.
 *
 * Shared by Router Users and Queues. One chain per router is strictly safer than
 * one per feature, since the hazard is concurrent writes to the same device.
 *
 * Keyed by router id rather than by socket, because the hazard is two people on
 * one router, not one person twice. Entries are dropped when the chain drains,
 * so a removed router leaves nothing behind.
 */
const _routerWriteChains = new Map();
function _routerWriteQueue(rid, fn) {
  if (!rid) return Promise.resolve();
  const prev = _routerWriteChains.get(rid) || Promise.resolve();
  // Errors are already handled inside the handlers; catch here so one rejection
  // cannot poison the chain for every later request on this router.
  const next = prev.then(() => fn(rid)).catch((e) => {
    console.error('%s', '[router-write] action failed:', (e && e.message) || e);
  });
  _routerWriteChains.set(rid, next);
  next.finally(() => { if (_routerWriteChains.get(rid) === next) _routerWriteChains.delete(rid); });
  return next;
}

const _NO_FOCUS_REPLAY = new Set(['logs']);
const _REPLAY_ON_FOCUS = new Set(
  Pages.KEYS.filter(k => !_NO_FOCUS_REPLAY.has(k)
                      && !(_PAGE_STREAM_ROOMS[k] && _PAGE_STREAM_ROOMS[k].length)
                      && _COLLECTOR_DEFS.some(c => c.page === k && c.sessionProp === k)));

function _updatePageStream(session, entry, which) {
  if (!session || !entry.startupReady) return;
  const rid = session.routerId;
  const viewers = _PAGE_STREAM_ROOMS[which].reduce(
    (n, room) => n + (io.sockets.adapter.rooms.get('router-' + rid + '-' + room)?.size || 0), 0);
  if (viewers > 0) _resumeCollector(session, entry, which); else session[which].suspend();
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
    // These four hold one /listen channel each in stream mode and none in poll
    // mode, so the count has to be read rather than assumed — a hardcoded 0 was
    // exactly what made the old poll-only shape invisible here.
    { name: 'vlans',        streams: s.vlans._listen   && s.vlans._listen.open   ? 1 : 0 },
    { name: 'ppp',          streams: s.ppp._listen     && s.ppp._listen.open     ? 1 : 0 },
    { name: 'bridges',      streams: s.bridges._listen && s.bridges._listen.open ? 1 : 0 },
    { name: 'capsman',      streams: s.capsman._listen && s.capsman._listen.open ? 1 : 0 },
    // dns, packages and rosusers hold no channel by design — see src/collection.js.
    { name: 'dns',          streams: 0 },
    { name: 'packages',     streams: 0 },
    { name: 'rosusers',     streams: 0 },
    // Two channels when streaming: one per queue menu. Neither carries data —
    // they mark the tables stale and the tick reads them.
    { name: 'queues',       streams: (s.queues._listens || []).filter(l => l && l.open).length },
    { name: 'wan',          streams: s.wan._listen && s.wan._listen.open ? 1 : 0 },
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
  // `trust proxy` is Express middleware and a socket's request never passes
  // through it, so req.ip is not available here. Resolve the address once at
  // connect for the audit trail: behind a trusted proxy the forwarded header is
  // the real client, otherwise the socket's own address is.
  {
    const fwd = TRUSTED_PROXY ? String(socket.handshake.headers['x-forwarded-for'] || '').split(',')[0].trim() : '';
    socket._clientIp = (fwd || socket.handshake.address || '').replace(/^::ffff:/, '') || null;
  }
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
    // Before any early return: the person who started the scan is gone, so stop
    // disrupting the radio. The registry's own wall-clock stop is the backstop,
    // never the only guard.
    wifiScans.abortByOwner(socket.id);
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
    // Opening a page is the cheapest re-probe available and by far the most
    // timely: somebody who has just configured a netwatch host opens NetWatch
    // next. It pre-empts the backoff entirely, which is what lets the backoff be
    // slow enough to be free. Cleared BEFORE _updatePageStream, or the veto would
    // still be in place when it tries to resume.
    for (const ck of Pages.collectorsFor(name)) _wakeForFocus(s, e, ck);
    if (_PAGE_STREAM_ROOMS[name]) {
      _updatePageStream(s, e, name);
      if (s[name] && s[name].lastPayload)
        socket.emit(name + ':update', { ...s[name].lastPayload, ts: Date.now() });
    }
    if (name === 'logs' && s.logs)
      socket.emit('logs:history', { entries: s.logs.getHistory() });
    // Poll-only pages have no streamRooms, so the generic replay above — gated
    // on _PAGE_STREAM_ROOMS[name] — never fires for them, and each would sit
    // blank for a whole poll interval on every visit. Derived from the registry
    // rather than listed by hand: the hand-written version was two pages out of
    // date within one release. This also covers bandwidth, whose identical
    // hand-written replay it replaced.
    if (_REPLAY_ON_FOCUS.has(name) && s[name] && s[name].lastPayload)
      socket.emit(name + ':update', { ...s[name].lastPayload, ts: Date.now() });
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
    // Same re-probe as page:focus, via the page the card borrows its data from —
    // a dashboard card is the only view some collectors get.
    for (const ck of Pages.collectorsFor(src)) _wakeForFocus(s, e, ck);
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

  // ── Packages ──────────────────────────────────────────────────────────────
  //
  // The first router-CONFIG writes in the app. router:write has sat in the
  // permission vocabulary since #108 with no call sites; these are them.
  //
  // enable/disable/uninstall DO NOT ACT — they schedule, `unschedule` reverses
  // them, and nothing happens until apply-changes reboots the router. Verified
  // on the live fleet. So the per-package verbs are cheap and reversible, and
  // the reboot is the single dangerous button, gated separately below.

  const _PKG_SCHEDULE = Object.freeze({
    enable:     '/system/package/enable',
    disable:    '/system/package/disable',
    uninstall:  '/system/package/uninstall',
    unschedule: '/system/package/unschedule',
  });

  const _pkgSession = () => {
    const rid = socket.routerId;
    const e   = rid ? _routerSessions.get(rid) : null;
    const session = e && e.session;
    // A collector switched off for this router (#105) has no inventory, so there
    // is nothing to target and nothing to show afterwards. The writes go through
    // session.ros rather than the collector precisely because the collector may
    // be a null stub — but without its payload the actions have no subject.
    const off = !session || !session.packages || session.packages.disabled;
    return { rid, entry: e, session, off };
  };
  const _pkgErr = (code, extra) =>
    socket.emit('packages:error', Object.assign({ code }, extra || {}));

  // A permission trap on a write reads nothing like a missing menu, and the
  // difference matters to whoever is looking at the button: one is "you cannot",
  // the other is "the RouterOS user cannot". system.js:229 draws the same line
  // for the update check.
  const _rosWriteFail = (e) => {
    const msg = String((e && e.message) || e).toLowerCase();
    if (msg.includes('not enough permissions') || msg.includes('permission denied') ||
        msg.includes('no permissions')) return 'router-write-policy';
    if (msg.includes('no such') || msg.includes('unknown command')) return 'unsupported';
    return 'failed';
  };

  // The page draws its action buttons from this, not from the payload: whether
  // somebody may act is a property of the socket, and the collector payload is
  // shared by every viewer of the router.
  socket.on('packages:caps', () => {
    const { rid, session, off } = _pkgSession();
    if (!rid || !session || off) return _pkgErr('unavailable');
    if (!_pageAllowed(socket, 'packages', 'read')) return _pkgErr('denied');
    socket.emit('packages:caps', {
      permitted: _socketCan(socket, 'router:write', rid) && _pageAllowed(socket, 'packages', 'write'),
      routerName: (Routers.getById(rid) || {}).label || '',
    });
  });

  socket.on('packages:schedule', async (req) => {
    const { rid, session, off } = _pkgSession();
    if (!rid || !session || off) return _pkgErr('unavailable');
    // Both gates, the way wifiscan does it: _pageAllowed carries the install-wide
    // page toggle, and _socketCan keeps router:write the named, greppable one.
    if (!_pageAllowed(socket, 'packages', 'write') || !_socketCan(socket, 'router:write', rid)) {
      audit.fromSocket(socket).denied({ action: 'package.schedule', targetType: 'package',
        routerId: rid, targetName: req && req.name ? String(req.name) : null });
      return _pkgErr('denied');
    }

    const action = req && typeof req.action === 'string' ? req.action : '';
    const name   = req && typeof req.name === 'string' ? req.name : '';
    const cmd    = _PKG_SCHEDULE[action];
    if (!cmd || !name) return _pkgErr('bad-request');

    // Resolved against what the collector last read rather than trusting the id
    // the browser sent, so a stale or crafted page cannot address a row that was
    // never on screen.
    const pkg = ((session.packages.lastPayload || {}).packages || [])
      .find(x => x.name === name);
    if (!pkg || !pkg.id) return _pkgErr('no-such-package', { name });

    try {
      await session.ros.write(cmd, ['=.id=' + pkg.id]);
      audit.fromSocket(socket).record({ action: 'package.' + action, targetType: 'package',
        targetId: pkg.id, targetName: pkg.name, routerId: rid,
        note: action === 'unschedule' ? 'scheduled change cancelled'
                                      : 'scheduled; inert until apply-changes reboots the router' });
      // Re-read rather than assuming: the banner must show what the router did,
      // not what the browser hoped it did.
      await session.packages.refreshNow();
      socket.emit('packages:ok', { action, name });
    } catch (e) {
      _pkgErr(_rosWriteFail(e), { name, message: sanitizeErr(e) });
    }
  });

  socket.on('packages:check', async () => {
    const { rid, session, off } = _pkgSession();
    if (!rid || !session || off) return _pkgErr('unavailable');
    if (!_pageAllowed(socket, 'packages', 'write') || !_socketCan(socket, 'router:write', rid)) {
      audit.fromSocket(socket).denied({ action: 'package.check', routerId: rid });
      return _pkgErr('denied');
    }
    try {
      // Reaches MikroTik's servers, so it is a button rather than a poll. The
      // 12-hourly background check in system.js is unaffected.
      await session.ros.write('/system/package/update/check-for-updates', []);
      audit.fromSocket(socket).record({ action: 'package.check', targetType: 'package',
        routerId: rid, note: 'contacted MikroTik update servers' });
      await session.packages.refreshNow();
      socket.emit('packages:ok', { action: 'check' });
    } catch (e) {
      _pkgErr(_rosWriteFail(e), { message: sanitizeErr(e) });
    }
  });

  socket.on('packages:apply', async (req) => {
    const { rid, session, off } = _pkgSession();
    if (!rid || !session || off) return _pkgErr('unavailable');
    if (!_pageAllowed(socket, 'packages', 'write') || !_socketCan(socket, 'router:write', rid)) {
      audit.fromSocket(socket).denied({ action: 'package.apply', routerId: rid });
      return _pkgErr('denied');
    }

    // Second gate, and the only one of its kind in the app: this reboots a
    // production router. The browser must send back the router's own name, so a
    // misclick — or a click on the wrong router — cannot reach it.
    const routerName = (Routers.getById(rid) || {}).label || '';
    const confirm    = req && typeof req.confirm === 'string' ? req.confirm.trim() : '';
    if (!routerName || confirm.toLowerCase() !== routerName.toLowerCase())
      return _pkgErr('confirm-mismatch', { routerName });

    const pending = ((session.packages.lastPayload || {}).packages || [])
      .filter(x => x.scheduled);
    if (!pending.length) return _pkgErr('nothing-scheduled');

    console.log('%s', `[packages] apply-changes on ${routerName} — ${pending.length} scheduled change(s), router will reboot`);
    socket.emit('packages:applying', { routerName, count: pending.length });
    try {
      // The router reboots as it answers, so a lost connection here is the
      // expected outcome, not a failure. Anything the write throws after the
      // command is away would be reported as an error the user should ignore.
      // Recorded BEFORE the call: this reboots the router, so the connection is
      // expected to drop while the command is in flight. Writing the row
      // afterwards would lose the record of the most consequential action here.
      audit.fromSocket(socket).record({ action: 'package.apply', targetType: 'router',
        targetId: rid, targetName: routerName, routerId: rid,
        extra: { scheduled: pending.map(p => p.name + ':' + (p.scheduledAction || 'change')) },
        note: 'applied scheduled package changes and rebooted the router' });
      await session.ros.write('/system/package/apply-changes', []);
      socket.emit('packages:ok', { action: 'apply', routerName });
    } catch (e) {
      const code = _rosWriteFail(e);
      if (code === 'failed') socket.emit('packages:ok', { action: 'apply', routerName, rebooting: true });
      else _pkgErr(code, { message: sanitizeErr(e) });
    }
  });


  /**
   * Upgrade RouterOS — the Update button on the Dashboard's System card.
   *
   * `/system/package/update/install` downloads the new packages AND reboots, in
   * one command. It is the single most consequential thing this app can do to a
   * router, so it carries the same second gate apply-changes does: the browser
   * must send back the router's own name. A misclick cannot reach it, and
   * neither can a click on the router you thought you were looking at.
   *
   * Gated on the PACKAGES page rather than the dashboard. The button lives on
   * the dashboard, but the authority it needs is the one that can already
   * reboot this router from the Packages page — inventing a second permission
   * for the same power would mean two answers to one question.
   *
   * Queued and rid-captured, unlike its three siblings above: they were written
   * before _routerWriteQueue existed. A reboot landing on the wrong router
   * because of a router:switch mid-flight is exactly what the capture prevents.
   */
  socket.on('packages:upgrade', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const entry   = rid ? _routerSessions.get(rid) : null;
    const session = entry && entry.session;
    // Deliberately not gated on the collector being enabled: the write goes
    // through session.ros, so an install works on a router whose Packages
    // collector is switched off (#105). Only the refresh afterwards needs it,
    // and the router is rebooting anyway.
    if (!rid || !session) return _pkgErr('unavailable');

    if (!_pageAllowed(socket, 'packages', 'write') || !_socketCan(socket, 'router:write', rid)) {
      audit.fromSocket(socket).denied({ action: 'package.upgrade', targetType: 'router',
        targetId: rid, routerId: rid });
      return _pkgErr('denied');
    }

    const routerName = (Routers.getById(rid) || {}).label || '';
    const confirm    = req && typeof req.confirm === 'string' ? req.confirm.trim() : '';
    if (!routerName || confirm.toLowerCase() !== routerName.toLowerCase())
      return _pkgErr('confirm-mismatch', { routerName });

    try {
      // Read fresh rather than trusting the payload the button was drawn from:
      // an update that has already been installed by somebody else must not
      // reboot the router a second time for nothing.
      const row = ((await session.ros.write('/system/package/update/print', []) || [])[0]) || {};
      const installed = row['installed-version'] || '';
      const latest    = row['latest-version'] || '';
      if (!latest || (installed && latest === installed))
        return _pkgErr('nothing-to-update', { installed, latest });

      console.log('%s', `[packages] upgrade on ${routerName} — ${installed || '?'} to ${latest}, router will reboot`);
      socket.emit('packages:applying', { routerName, count: 1, upgrade: true });

      // Recorded BEFORE the call, as apply-changes is: the router reboots while
      // the command is in flight, so writing the row afterwards would lose the
      // record of the most consequential action in the app.
      audit.fromSocket(socket).record({ action: 'package.upgrade', targetType: 'router',
        targetId: rid, targetName: routerName, routerId: rid,
        extra: { from: installed, to: latest, channel: row.channel || '' },
        note: 'downloaded the RouterOS update and rebooted the router' });

      await session.ros.write('/system/package/update/install', []);
      socket.emit('packages:ok', { action: 'upgrade', routerName, latest });
    } catch (e) {
      // A lost connection here is the expected outcome, not a failure — the
      // router is rebooting as it answers.
      const code = _rosWriteFail(e);
      if (code === 'failed') socket.emit('packages:ok', { action: 'upgrade', routerName, rebooting: true });
      else _pkgErr(code, { message: sanitizeErr(e) });
    }
  }));


  // ── Router Users (RouterOS /user) ─────────────────────────────────────────
  //
  // The only write surface in this app that can lock MikroDash out of the
  // router it manages, and unrecoverably: once the login is broken the fix is
  // WinBox, not this page. src/routeros/selfGuard.js holds every refusal and
  // explains each one; the rules below are about how it is CALLED.
  //
  // Three properties every handler here has, and the Packages handlers above do
  // not need:
  //
  //  1. IT RE-READS FROM THE ROUTER. Packages resolves its target from the
  //     collector's last payload, which is safer than trusting the browser.
  //     Here the payload is the thing that goes stale in the dangerous
  //     direction — a row renamed to `MikroDash` after the last tick would read
  //     as unprotected — so the guard runs against a read taken in the same
  //     tick as the write. `lastPayload` appears in none of these handlers, and
  //     a test asserts it.
  //
  //  2. IT ROUND-TRIPS THE NAME. The browser sends the `.id` AND the name it
  //     displayed. A `.id` is stable across renames, which makes it the right
  //     key to address a row with and the wrong one to identify it by. If the
  //     freshly-read row no longer carries the name the operator was looking
  //     at, the action is refused as `stale-row` rather than applied to
  //     whatever is there now.
  //
  //  3. IT IS SERIALISED PER ROUTER. read → check → write runs under a
  //     per-router promise chain, so two operators cannot interleave a rename
  //     with an edit. RouterOS has no compare-and-swap, so this is what makes
  //     the fresh read mean anything.

  // Takes the router id rather than reading socket.routerId, because these
  // handlers run from a queue: by the time one executes, a router:switch may
  // have moved the socket, and the write must land on the router the operator
  // was looking at when they pressed the button.
  const _ruSession = (rid) => {
    const e   = rid ? _routerSessions.get(rid) : null;
    const session = e && e.session;
    // Writes go through session.ros, not the collector, so they work even for a
    // null-collector stub (#105) — but without the collector there is nothing to
    // refresh afterwards and no page to show it on.
    const off = !session || !session.rosusers || session.rosusers.disabled;
    return { rid, session, off };
  };
  const _ruErr = (code, extra) =>
    socket.emit('rosusers:error', Object.assign({ code }, extra || {}));

  /** Both gates. _pageAllowed is the install toggle AND the role; router:write is conferred by write on any page, so the page gate is what scopes this feature. */
  const _ruMayWrite = (rid) =>
    _pageAllowed(socket, 'rosusers', 'write') && _socketCan(socket, 'router:write', rid);

  /**
   * Read the three tables and resolve who we are, in one place.
   *
   * Deliberately not proplist-filtered the way the collector is: the guard
   * compares names and groups, and a field the guard does not read cannot
   * change its answer, but a field the ROUTER renamed could. Cheapest possible
   * correctness on a table of single-digit length.
   */
  const _ruRead = async (session, rid) => {
    const [users, groups, active] = await Promise.all([
      session.ros.write('/user/print', []),
      session.ros.write('/user/group/print', []),
      session.ros.write('/user/active/print', []),
    ]);
    const clean = (rows) => (rows || []).filter(r => r && r.name);
    const cfg   = Routers.getById(rid) || {};
    const self  = selfGuard.resolveSelf(clean(users), clean(active),
                                        [(session.ros.cfg || {}).username, cfg.username]);
    return { users: clean(users), groups: clean(groups), active: clean(active), self };
  };

  /**
   * Find a row by id and confirm it still carries the name the operator saw.
   *
   * Returns null for both "gone" and "renamed underneath you", which the caller
   * reports as `stale-row`: from the operator's side those are the same event —
   * the screen they acted on no longer describes the router.
   */
  const _ruRow = (rows, id, expectedName) => {
    const row = (rows || []).find(r => r['.id'] === id);
    if (!row) return null;
    if (expectedName && String(row.name) !== String(expectedName)) return null;
    return row;
  };

  socket.on('rosusers:caps', () => {
    const rid = socket.routerId;
    const { session, off } = _ruSession(rid);
    if (!rid || !session || off) return _ruErr('unavailable');
    if (!_pageAllowed(socket, 'rosusers', 'read')) return _ruErr('denied');
    socket.emit('rosusers:caps', {
      permitted: _ruMayWrite(rid),
      routerName: (Routers.getById(rid) || {}).label || '',
    });
  });

  /**
   * Create or edit a user.
   *
   * `id` present means edit. A password is accepted on create and on an
   * explicit reset, is never echoed back, and never reaches the audit trail:
   * audit.js redacts by field name and a test asserts it for this action.
   */
  socket.on('rosuser:save', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const { session, off } = _ruSession(rid);
    if (!rid || !session || off) return _ruErr('unavailable');
    const r = req || {};
    const editing = !!r.id;
    if (!_ruMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action: editing ? 'rosuser.update' : 'rosuser.create',
        targetType: 'rosuser', routerId: rid, targetName: r.name ? String(r.name) : null });
      return _ruErr('denied');
    }

    const name  = typeof r.name  === 'string' ? r.name.trim()  : '';
    const group = typeof r.group === 'string' ? r.group.trim() : '';
    if (!name || !group) return _ruErr('bad-request');

    try {
      const { users, groups, self } = await _ruRead(session, rid);
      if (!groups.some(g => String(g.name) === group)) return _ruErr('no-such-group', { name: group });

      const target = editing ? _ruRow(users, r.id, r.expectedName) : null;
      if (editing && !target) return _ruErr('stale-row', { name });

      const verdict = selfGuard.checkUser(self, {
        verb: editing ? 'set' : 'add',
        target: target ? { name: target.name, group: target.group } : null,
        values: { name, group },
      });
      if (!verdict.ok) {
        audit.fromSocket(socket).denied({ action: editing ? 'rosuser.update' : 'rosuser.create',
          targetType: 'rosuser', routerId: rid, targetName: name, note: verdict.code });
        return _ruErr(verdict.code, { name: verdict.detail || name });
      }

      // The router enforces its own minimum, but it answers with a bare
      // failure. Checking here is what turns that into a sentence the operator
      // can act on — the router stays the authority either way.
      const pw = typeof r.password === 'string' ? r.password : '';
      // The plaintext is never mentioned again below this line — the audit call
      // reads this flag instead, so there is no expression anywhere in it that
      // could serialise the password even by accident.
      const passwordSet = !!pw;
      const minLen = ((session.rosusers.lastPayload || {}).passwordPolicy || {}).minLength || 0;
      if (pw && minLen && pw.length < minLen) return _ruErr('weak-password', { minLength: minLen });
      if (!editing && !pw && minLen) return _ruErr('weak-password', { minLength: minLen });

      const args = ['=name=' + name, '=group=' + group,
                    '=address=' + (typeof r.address === 'string' ? r.address.trim() : ''),
                    '=comment=' + (typeof r.comment === 'string' ? r.comment.trim() : ''),
                    '=disabled=' + (r.disabled ? 'yes' : 'no')];
      if (pw) args.push('=password=' + pw);

      const before = target ? { name: target.name, group: target.group, address: target.address || '',
                                comment: target.comment || '', disabled: target.disabled === 'true' } : {};
      const after  = { name, group, address: (r.address || '').trim(),
                       comment: (r.comment || '').trim(), disabled: !!r.disabled };

      if (editing) await session.ros.write('/user/set', ['=.id=' + r.id].concat(args));
      else         await session.ros.write('/user/add', args);

      audit.fromSocket(socket).record({ action: editing ? 'rosuser.update' : 'rosuser.create',
        targetType: 'rosuser', targetId: editing ? r.id : null, targetName: name,
        routerId: rid, before, after,
        // A flag, not a diff field. /user/print never returns a password, so
        // `before` cannot know whether one was set — recording «unset» →
        // «changed» would be a claim the trail cannot support. This says only
        // what is actually known, and keeps the plaintext out of audit.js
        // entirely rather than relying on its redaction.
        extra: passwordSet ? { passwordSet: true } : undefined });
      await session.rosusers.refreshNow();
      socket.emit('rosusers:ok', { action: editing ? 'update' : 'create', name });
    } catch (e) {
      _ruErr(_rosWriteFail(e), { name, message: sanitizeErr(e) });
    }
  }));

  socket.on('rosuser:remove', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const { session, off } = _ruSession(rid);
    if (!rid || !session || off) return _ruErr('unavailable');
    const r = req || {};
    if (!_ruMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action: 'rosuser.delete', targetType: 'rosuser',
        routerId: rid, targetName: r.expectedName ? String(r.expectedName) : null });
      return _ruErr('denied');
    }
    if (!r.id) return _ruErr('bad-request');

    try {
      const { users, self } = await _ruRead(session, rid);
      const target = _ruRow(users, r.id, r.expectedName);
      if (!target) return _ruErr('stale-row');

      const verdict = selfGuard.checkUser(self, { verb: 'remove', target: { name: target.name, group: target.group } });
      if (!verdict.ok) {
        audit.fromSocket(socket).denied({ action: 'rosuser.delete', targetType: 'rosuser',
          routerId: rid, targetId: r.id, targetName: target.name, note: verdict.code });
        return _ruErr(verdict.code, { name: verdict.detail || target.name });
      }

      await session.ros.write('/user/remove', ['=.id=' + r.id]);
      audit.fromSocket(socket).record({ action: 'rosuser.delete', targetType: 'rosuser',
        targetId: r.id, targetName: target.name, routerId: rid,
        extra: { group: target.group || '' } });
      await session.rosusers.refreshNow();
      socket.emit('rosusers:ok', { action: 'delete', name: target.name });
    } catch (e) {
      _ruErr(_rosWriteFail(e), { message: sanitizeErr(e) });
    }
  }));

  /**
   * Create or edit a group.
   *
   * `policy` arrives as the list of GRANTED names. RouterOS normalises whatever
   * it is sent — send `read,api` and it stores all 17 with the rest negated —
   * so there is no point sending negations, and buildPolicy filters to the
   * vocabulary the UI actually showed rather than passing strings through.
   */
  socket.on('rosgroup:save', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const { session, off } = _ruSession(rid);
    if (!rid || !session || off) return _ruErr('unavailable');
    const r = req || {};
    const editing = !!r.id;
    if (!_ruMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action: editing ? 'rosgroup.update' : 'rosgroup.create',
        targetType: 'rosgroup', routerId: rid, targetName: r.name ? String(r.name) : null });
      return _ruErr('denied');
    }

    const name = typeof r.name === 'string' ? r.name.trim() : '';
    if (!name || !Array.isArray(r.policy)) return _ruErr('bad-request');
    const policy = RosUsersCollector.buildPolicy(r.policy);

    try {
      const { groups, self } = await _ruRead(session, rid);
      const target = editing ? _ruRow(groups, r.id, r.expectedName) : null;
      if (editing && !target) return _ruErr('stale-row', { name });

      const verdict = selfGuard.checkGroup(self, {
        verb: editing ? 'set' : 'add',
        target: target ? { name: target.name } : null,
        values: { name },
      });
      if (!verdict.ok) {
        audit.fromSocket(socket).denied({ action: editing ? 'rosgroup.update' : 'rosgroup.create',
          targetType: 'rosgroup', routerId: rid, targetName: name, note: verdict.code });
        return _ruErr(verdict.code, { name: verdict.detail || name });
      }

      const args = ['=name=' + name, '=policy=' + policy,
                    '=comment=' + (typeof r.comment === 'string' ? r.comment.trim() : '')];
      if (editing) await session.ros.write('/user/group/set', ['=.id=' + r.id].concat(args));
      else         await session.ros.write('/user/group/add', args);

      audit.fromSocket(socket).record({ action: editing ? 'rosgroup.update' : 'rosgroup.create',
        targetType: 'rosgroup', targetId: editing ? r.id : null, targetName: name, routerId: rid,
        before: target ? { name: target.name, policy: target.policy || '' } : {},
        after:  { name, policy } });
      await session.rosusers.refreshNow();
      socket.emit('rosusers:ok', { action: editing ? 'group-update' : 'group-create', name });
    } catch (e) {
      _ruErr(_rosWriteFail(e), { name, message: sanitizeErr(e) });
    }
  }));

  socket.on('rosgroup:remove', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const { session, off } = _ruSession(rid);
    if (!rid || !session || off) return _ruErr('unavailable');
    const r = req || {};
    if (!_ruMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action: 'rosgroup.delete', targetType: 'rosgroup',
        routerId: rid, targetName: r.expectedName ? String(r.expectedName) : null });
      return _ruErr('denied');
    }
    if (!r.id) return _ruErr('bad-request');

    try {
      const { groups, self } = await _ruRead(session, rid);
      const target = _ruRow(groups, r.id, r.expectedName);
      if (!target) return _ruErr('stale-row');

      const verdict = selfGuard.checkGroup(self, { verb: 'remove', target: { name: target.name } });
      if (!verdict.ok) {
        audit.fromSocket(socket).denied({ action: 'rosgroup.delete', targetType: 'rosgroup',
          routerId: rid, targetId: r.id, targetName: target.name, note: verdict.code });
        return _ruErr(verdict.code, { name: verdict.detail || target.name });
      }

      // Not pre-checked against the member count. The router refuses with
      // "group has some users" and it is the authority — a count read a moment
      // earlier could be wrong in either direction.
      await session.ros.write('/user/group/remove', ['=.id=' + r.id]);
      audit.fromSocket(socket).record({ action: 'rosgroup.delete', targetType: 'rosgroup',
        targetId: r.id, targetName: target.name, routerId: rid,
        extra: { policy: target.policy || '' } });
      await session.rosusers.refreshNow();
      socket.emit('rosusers:ok', { action: 'group-delete', name: target.name });
    } catch (e) {
      const msg = String((e && e.message) || e).toLowerCase();
      if (msg.includes('has some users')) return _ruErr('group-in-use');
      _ruErr(_rosWriteFail(e), { message: sanitizeErr(e) });
    }
  }));

  socket.on('rossession:remove', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const { session, off } = _ruSession(rid);
    if (!rid || !session || off) return _ruErr('unavailable');
    const r = req || {};
    if (!_ruMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action: 'rossession.remove', targetType: 'rossession',
        routerId: rid, targetName: r.expectedName ? String(r.expectedName) : null });
      return _ruErr('denied');
    }
    if (!r.id) return _ruErr('bad-request');

    try {
      const { active, self } = await _ruRead(session, rid);
      const target = _ruRow(active, r.id, r.expectedName);
      if (!target) return _ruErr('stale-row');

      const verdict = selfGuard.checkSession(self, { target: { name: target.name } });
      if (!verdict.ok) {
        audit.fromSocket(socket).denied({ action: 'rossession.remove', targetType: 'rossession',
          routerId: rid, targetId: r.id, targetName: target.name, note: verdict.code });
        return _ruErr(verdict.code, { name: verdict.detail || target.name });
      }

      await session.ros.write('/user/active/remove', ['=.id=' + r.id]);
      audit.fromSocket(socket).record({ action: 'rossession.remove', targetType: 'rossession',
        targetId: r.id, targetName: target.name, routerId: rid,
        extra: { via: target.via || '', from: target.address || '' },
        note: 'ended an active RouterOS session' });
      await session.rosusers.refreshNow();
      socket.emit('rosusers:ok', { action: 'session-remove', name: target.name });
    } catch (e) {
      _ruErr(_rosWriteFail(e), { message: sanitizeErr(e) });
    }
  }));


  // ── Queues (RouterOS traffic shaping) ─────────────────────────────────────
  //
  // The third router-write feature. Unlike Router Users it cannot lock MikroDash
  // out — but a simple queue CAN throttle the dashboard's own traffic, so
  // src/routeros/queueGuard.js warns about that. Read its header before
  // assuming selfGuard's rules apply: it warns rather than refuses, and it fails
  // open rather than closed. Both are deliberate.
  //
  // The same three properties as the Router Users handlers: a fresh read in the
  // same tick as the write, a round-tripped name so a `.id` addresses a row
  // without identifying it, and per-router serialisation.

  const _QUEUE_MENUS = Object.freeze({ simple: '/queue/simple', tree: '/queue/tree' });

  const _qSession = (rid) => {
    const e = rid ? _routerSessions.get(rid) : null;
    const session = e && e.session;
    const off = !session || !session.queues || session.queues.disabled;
    return { session, off };
  };
  const _qErr = (code, extra) =>
    socket.emit('queues:error', Object.assign({ code }, extra || {}));

  const _qMayWrite = (rid) =>
    _pageAllowed(socket, 'queues', 'write') && _socketCan(socket, 'router:write', rid);

  /**
   * Read one queue menu, plus the active sessions the throttle warning needs.
   *
   * /user/active is read for its `address` column — the source address the
   * ROUTER sees us from, which is what makes the warning possible at all. A
   * router that denies it simply yields no addresses, and the guard fails open.
   */
  const _qRead = async (session, rid, menu) => {
    const rows = (await session.ros.write(_QUEUE_MENUS[menu] + '/print', []) || [])
      .filter(r => r && r['.id']);
    let active = [];
    try { active = (await session.ros.write('/user/active/print', []) || []).filter(r => r && r.name); }
    catch (_) { /* denied or unsupported — the guard fails open, by design */ }
    const cfg = Routers.getById(rid) || {};
    const self = queueGuard.resolveSelfAddresses(active,
      [(session.ros.cfg || {}).username, cfg.username]);
    return { rows, self };
  };

  /** Address by id, identify by name — a `.id` survives a rename, a name does not. */
  const _qRow = (rows, id, expectedName) => {
    const row = (rows || []).find(r => r['.id'] === id);
    if (!row) return null;
    if (expectedName && String(row.name) !== String(expectedName)) return null;
    return row;
  };

  /**
   * RouterOS refuses max-limit below limit-at with "download-max-limit less than
   * download-limit". Checking here turns that into a sentence naming both
   * fields; the router stays the authority either way.
   */
  const _qLimitsOk = (maxLimit, limitAt) => {
    const m = queueGuard.parsePair(maxLimit), l = queueGuard.parsePair(limitAt);
    const bad = (mx, lo) => typeof mx === 'number' && mx > 0 && typeof lo === 'number' && lo > mx;
    return !(bad(m.up, l.up) || bad(m.down, l.down));
  };

  socket.on('queues:caps', () => {
    const rid = socket.routerId;
    const { session, off } = _qSession(rid);
    if (!rid || !session || off) return _qErr('unavailable');
    if (!_pageAllowed(socket, 'queues', 'read')) return _qErr('denied');
    socket.emit('queues:caps', {
      permitted: _qMayWrite(rid),
      routerName: (Routers.getById(rid) || {}).label || '',
    });
  });

  socket.on('queue:save', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const { session, off } = _qSession(rid);
    if (!rid || !session || off) return _qErr('unavailable');
    const r = req || {};
    const menu = r.menu === 'tree' ? 'tree' : 'simple';
    const editing = !!r.id;
    const action = 'queue.' + (editing ? 'update' : 'create');
    if (!_qMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action, targetType: 'queue', routerId: rid,
        targetName: r.name ? String(r.name) : null, extra: { menu } });
      return _qErr('denied');
    }

    const name = typeof r.name === 'string' ? r.name.trim() : '';
    if (!name) return _qErr('bad-request');
    if (menu === 'simple' && !editing && !r.target) return _qErr('bad-request');
    if (!_qLimitsOk(r.maxLimit, r.limitAt)) return _qErr('limit-above-max');

    try {
      const { rows, self } = await _qRead(session, rid, menu);
      const target = editing ? _qRow(rows, r.id, r.expectedName) : null;
      if (editing && !target) return _qErr('stale-row', { name });
      // Checked on the freshly-read row, never the browser's claim. Only simple
      // queues can be dynamic — a tree has no such field.
      if (target && (target.dynamic === 'true' || target.dynamic === true)) {
        audit.fromSocket(socket).denied({ action, targetType: 'queue', routerId: rid,
          targetId: r.id, targetName: name, note: 'dynamic-row' });
        return _qErr('dynamic-row', { name });
      }

      // Only simple queues carry a target, so only they can be aimed at us.
      if (menu === 'simple') {
        const verdict = queueGuard.checkSimpleQueue({
          selfAddresses: self,
          values: { target: r.target, maxLimit: r.maxLimit, disabled: !!r.disabled },
          before: target ? { target: target.target, maxLimit: target['max-limit'],
                             disabled: target.disabled === 'true' } : null,
        });
        if (verdict.level === 'warn') {
          if (!r.ack) {
            // Nothing is written and nothing is audited — this is a prompt, not
            // a refusal, and a denial row here would make the trail lie about
            // what was attempted.
            return _qErr('self-throttle', { warning: verdict.detail, fingerprint: verdict.fingerprint, name });
          }
          if (r.ack !== verdict.fingerprint) {
            // An acknowledgement taken against different values, or against a
            // different self-address. Reported separately from the first prompt
            // so an ack cannot be carried from a mild queue to a harsher one, or
            // replayed against another write.
            return _qErr('stale-warning', { warning: verdict.detail, fingerprint: verdict.fingerprint, name });
          }
        }
        // verdict.level === 'none' with an ack present is harmless: the warning
        // simply no longer applies to what is being written.
      }

      const args = ['=name=' + name, '=comment=' + (typeof r.comment === 'string' ? r.comment.trim() : ''),
                    '=disabled=' + (r.disabled ? 'yes' : 'no')];
      const put = (k, v) => { if (v !== undefined && v !== null && v !== '') args.push('=' + k + '=' + String(v).trim()); };
      put('max-limit', r.maxLimit);
      put('limit-at',  r.limitAt);
      put('priority',  r.priority);
      if (menu === 'simple') {
        put('target',       r.target);
        put('packet-marks', r.packetMarks);
      } else {
        put('parent',      r.parent);
        put('packet-mark', r.packetMark);
      }

      if (editing) await session.ros.write(_QUEUE_MENUS[menu] + '/set', ['=.id=' + r.id].concat(args));
      else         await session.ros.write(_QUEUE_MENUS[menu] + '/add', args);

      audit.fromSocket(socket).record({ action, targetType: 'queue',
        targetId: editing ? r.id : null, targetName: name, routerId: rid,
        before: target ? { name: target.name, target: target.target || '', parent: target.parent || '',
                           maxLimit: target['max-limit'] || '', limitAt: target['limit-at'] || '',
                           disabled: target.disabled === 'true' } : {},
        after: { name, target: r.target || '', parent: r.parent || '',
                 maxLimit: r.maxLimit || '', limitAt: r.limitAt || '', disabled: !!r.disabled },
        // The acknowledgement is the interesting fact in the trail, not the queue.
        extra: Object.assign({ menu }, r.ack ? { selfThrottleAcknowledged: true } : null) });
      // A set can zero a counter, and the next window would otherwise be
      // measured against a baseline the router no longer agrees with.
      session.queues.forgetRates();
      await session.queues.refreshNow();
      socket.emit('queues:ok', { action: editing ? 'update' : 'create', name, menu });
    } catch (e) {
      const msg = String((e && e.message) || e).toLowerCase();
      if (msg.includes('less than')) return _qErr('limit-above-max', { name });
      _qErr(_rosWriteFail(e), { name, message: sanitizeErr(e) });
    }
  }));

  socket.on('queue:remove', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const { session, off } = _qSession(rid);
    if (!rid || !session || off) return _qErr('unavailable');
    const r = req || {};
    const menu = r.menu === 'tree' ? 'tree' : 'simple';
    if (!_qMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action: 'queue.delete', targetType: 'queue', routerId: rid,
        targetName: r.expectedName ? String(r.expectedName) : null, extra: { menu } });
      return _qErr('denied');
    }
    if (!r.id) return _qErr('bad-request');

    try {
      const { rows } = await _qRead(session, rid, menu);
      const target = _qRow(rows, r.id, r.expectedName);
      if (!target) return _qErr('stale-row');
      if (target.dynamic === 'true' || target.dynamic === true) {
        audit.fromSocket(socket).denied({ action: 'queue.delete', targetType: 'queue', routerId: rid,
          targetId: r.id, targetName: target.name, note: 'dynamic-row' });
        return _qErr('dynamic-row', { name: target.name });
      }

      await session.ros.write(_QUEUE_MENUS[menu] + '/remove', ['=.id=' + r.id]);
      audit.fromSocket(socket).record({ action: 'queue.delete', targetType: 'queue',
        targetId: r.id, targetName: target.name, routerId: rid,
        extra: { menu, target: target.target || target.parent || '', maxLimit: target['max-limit'] || '' } });
      session.queues.forgetRates();
      await session.queues.refreshNow();
      socket.emit('queues:ok', { action: 'delete', name: target.name, menu });
    } catch (e) {
      _qErr(_rosWriteFail(e), { message: sanitizeErr(e) });
    }
  }));

  socket.on('queue:toggle', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const { session, off } = _qSession(rid);
    if (!rid || !session || off) return _qErr('unavailable');
    const r = req || {};
    const menu = r.menu === 'tree' ? 'tree' : 'simple';
    if (!_qMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action: 'queue.toggle', targetType: 'queue', routerId: rid,
        targetName: r.expectedName ? String(r.expectedName) : null, extra: { menu } });
      return _qErr('denied');
    }
    if (!r.id) return _qErr('bad-request');

    try {
      const { rows, self } = await _qRead(session, rid, menu);
      const target = _qRow(rows, r.id, r.expectedName);
      if (!target) return _qErr('stale-row');
      if (target.dynamic === 'true' || target.dynamic === true) {
        audit.fromSocket(socket).denied({ action: 'queue.toggle', targetType: 'queue', routerId: rid,
          targetId: r.id, targetName: target.name, note: 'dynamic-row' });
        return _qErr('dynamic-row', { name: target.name });
      }

      const wasDisabled = target.disabled === 'true';
      // Enabling is the moment a throttle takes effect, and the easy one to
      // miss: the values were checked when the queue was created, but it may
      // have sat disabled ever since.
      if (menu === 'simple' && wasDisabled) {
        const verdict = queueGuard.checkSimpleQueue({
          selfAddresses: self,
          values: { target: target.target, maxLimit: target['max-limit'], disabled: false },
          before: null,
        });
        if (verdict.level === 'warn' && r.ack !== verdict.fingerprint) {
          return _qErr('self-throttle', { warning: verdict.detail, fingerprint: verdict.fingerprint,
                                          name: target.name });
        }
      }

      await session.ros.write(_QUEUE_MENUS[menu] + '/set',
        ['=.id=' + r.id, '=disabled=' + (wasDisabled ? 'no' : 'yes')]);
      audit.fromSocket(socket).record({ action: 'queue.toggle', targetType: 'queue',
        targetId: r.id, targetName: target.name, routerId: rid,
        before: { disabled: wasDisabled }, after: { disabled: !wasDisabled },
        extra: Object.assign({ menu }, r.ack ? { selfThrottleAcknowledged: true } : null) });
      await session.queues.refreshNow();
      socket.emit('queues:ok', { action: wasDisabled ? 'enable' : 'disable', name: target.name, menu });
    } catch (e) {
      _qErr(_rosWriteFail(e), { message: sanitizeErr(e) });
    }
  }));

  socket.on('queue:resetCounters', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const { session, off } = _qSession(rid);
    if (!rid || !session || off) return _qErr('unavailable');
    const r = req || {};
    const menu = r.menu === 'tree' ? 'tree' : 'simple';
    if (!_qMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action: 'queue.reset', targetType: 'queue', routerId: rid,
        targetName: r.expectedName ? String(r.expectedName) : null, extra: { menu } });
      return _qErr('denied');
    }
    if (!r.id) return _qErr('bad-request');

    try {
      const { rows } = await _qRead(session, rid, menu);
      const target = _qRow(rows, r.id, r.expectedName);
      if (!target) return _qErr('stale-row');

      await session.ros.write(_QUEUE_MENUS[menu] + '/reset-counters', ['=.id=' + r.id]);
      audit.fromSocket(socket).record({ action: 'queue.reset', targetType: 'queue',
        targetId: r.id, targetName: target.name, routerId: rid, extra: { menu },
        note: 'zeroed the queue statistics' });
      // Mandatory here, not merely tidy: the counter just went to zero and the
      // next delta would be measured against the pre-reset baseline.
      session.queues.forgetRates();
      await session.queues.refreshNow();
      socket.emit('queues:ok', { action: 'reset', name: target.name, menu });
    } catch (e) {
      _qErr(_rosWriteFail(e), { message: sanitizeErr(e) });
    }
  }));

  /**
   * Reorder a simple queue.
   *
   * Only simple queues have meaningful order — each packet walks the list until
   * one matches, so position changes behaviour. Trees are unordered and offer no
   * move.
   */
  socket.on('queue:move', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const { session, off } = _qSession(rid);
    if (!rid || !session || off) return _qErr('unavailable');
    const r = req || {};
    if (!_qMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action: 'queue.move', targetType: 'queue', routerId: rid,
        targetName: r.expectedName ? String(r.expectedName) : null });
      return _qErr('denied');
    }
    if (!r.id || (r.direction !== 'up' && r.direction !== 'down')) return _qErr('bad-request');

    try {
      const { rows } = await _qRead(session, rid, 'simple');
      const idx = rows.findIndex(x => x['.id'] === r.id);
      if (idx < 0) return _qErr('stale-row');
      const target = _qRow(rows, r.id, r.expectedName);
      if (!target) return _qErr('stale-row');

      // RouterOS moves a row to sit BEFORE `destination`. Moving down therefore
      // means "before the row after the next one", and moving the last row down
      // or the first row up is a no-op rather than an error.
      const destIdx = r.direction === 'up' ? idx - 1 : idx + 2;
      if (destIdx < 0 || idx === rows.length - 1 && r.direction === 'down') {
        return socket.emit('queues:ok', { action: 'move', name: target.name, menu: 'simple' });
      }
      const args = ['=.id=' + r.id];
      if (destIdx < rows.length) args.push('=destination=' + rows[destIdx]['.id']);
      await session.ros.write('/queue/simple/move', args);

      audit.fromSocket(socket).record({ action: 'queue.move', targetType: 'queue',
        targetId: r.id, targetName: target.name, routerId: rid,
        before: { position: idx }, after: { position: r.direction === 'up' ? idx - 1 : idx + 1 },
        extra: { menu: 'simple' },
        note: 'simple queue order decides which queue a packet matches first' });
      await session.queues.refreshNow();
      socket.emit('queues:ok', { action: 'move', name: target.name, menu: 'simple' });
    } catch (e) {
      _qErr(_rosWriteFail(e), { message: sanitizeErr(e) });
    }
  }));


  // ── WAN (DHCP lease actions) ──────────────────────────────────────────────
  //
  // Renew and release both drop the uplink for a few seconds. On a router
  // managed over its LAN that is harmless; on one managed THROUGH the WAN it
  // drops the dashboard, and unlike a bad queue you cannot undo it from the row
  // that caused it. src/routeros/wanGuard.js decides which case applies and
  // warns — it never refuses, and it fails open. Read its header before
  // assuming otherwise.
  //
  // Renew and release are treated identically: both interrupt the uplink, so
  // there is no quieter path to the riskier one.

  const _wanSession = (rid) => {
    const e = rid ? _routerSessions.get(rid) : null;
    const session = e && e.session;
    const off = !session || !session.wan || session.wan.disabled;
    return { session, off };
  };
  const _wanErr = (code, extra) =>
    socket.emit('wan:error', Object.assign({ code }, extra || {}));

  const _wanMayWrite = (rid) =>
    _pageAllowed(socket, 'wan', 'write') && _socketCan(socket, 'router:write', rid);

  /**
   * Read what the guard and the write both need, in one tick.
   *
   * The connected subnets come from /ip/address rather than from the collector
   * payload: this is the input that decides whether we are about to cut our own
   * management path, and it must not be a cached answer.
   */
  const _wanRead = async (session, rid) => {
    const [clients, addrs, active, routes] = await Promise.all([
      session.ros.write('/ip/dhcp-client/print', []),
      session.ros.write('/ip/address/print', ['=.proplist=address,interface,disabled']),
      session.ros.write('/user/active/print', []).catch(() => []),
      session.ros.write('/ip/route/print', ['=.proplist=dst-address,gateway,distance,active']),
    ]);
    const rows = (clients || []).filter(r => r && r['.id']);
    const connectedCidrs = (addrs || [])
      .filter(a => a && a.address && a.disabled !== 'true').map(a => a.address);
    const cfg  = Routers.getById(rid) || {};
    const self = queueGuard.resolveSelfAddresses((active || []).filter(r => r && r.name),
      [(session.ros.cfg || {}).username, cfg.username]);
    const path = wanGuard.resolveManagementPath({ selfAddresses: self, connectedCidrs });
    // Which uplink is carrying our return traffic.
    //
    // ONLY WHEN THERE IS EXACTLY ONE. Verified on a live router: four default
    // routes can be active at distance 1 at the same time, and picking the first
    // would name an uplink our packets may not use — warning about the wrong one
    // while staying silent on the right one. Ambiguity is reported as unknown,
    // which makes the guard warn for any WAN rather than guess.
    const activeDefaults = (routes || []).filter(r => r && r['dst-address'] === '0.0.0.0/0' && r.active === 'true');
    let activeDefaultWan = '';
    if (activeDefaults.length === 1 && activeDefaults[0].gateway) {
      const gw = activeDefaults[0].gateway;
      const byName  = rows.find(c => c.interface === gw);
      const byLease = rows.find(c => c.gateway === gw);
      activeDefaultWan = (byName && byName.interface) || (byLease && byLease.interface) || gw;
    }
    return { rows, path, activeDefaultWan };
  };

  /** Address by id, identify by interface name — an id survives a rename. */
  const _wanRow = (rows, id, expectedName) => {
    const row = (rows || []).find(r => r['.id'] === id);
    if (!row) return null;
    if (expectedName && String(row.interface) !== String(expectedName)) return null;
    return row;
  };

  socket.on('wan:caps', () => {
    const rid = socket.routerId;
    const { session, off } = _wanSession(rid);
    if (!rid || !session || off) return _wanErr('unavailable');
    if (!_pageAllowed(socket, 'wan', 'read')) return _wanErr('denied');
    socket.emit('wan:caps', {
      permitted: _wanMayWrite(rid),
      routerName: (Routers.getById(rid) || {}).label || '',
    });
  });

  const _WAN_VERBS = Object.freeze({ renew: '/ip/dhcp-client/renew', release: '/ip/dhcp-client/release' });

  /**
   * Renew or release one lease.
   *
   * One body, two registrations below rather than a loop: every drift test in
   * this repo greps for a literal `socket.on('wan:renew'`, and so will the next
   * person looking for where this is handled.
   */
  const _wanLeaseAction = async (verb, req, rid) => {
    const { session, off } = _wanSession(rid);
    if (!rid || !session || off) return _wanErr('unavailable');
    const r = req || {};
    const action = 'wan.' + verb;
    if (!_wanMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action, targetType: 'wan', routerId: rid,
        targetName: r.expectedName ? String(r.expectedName) : null });
      return _wanErr('denied');
    }
    if (!r.id) return _wanErr('bad-request');

    try {
      const { rows, path, activeDefaultWan } = await _wanRead(session, rid);
      const target = _wanRow(rows, r.id, r.expectedName);
      if (!target) return _wanErr('stale-row');

      const verdict = wanGuard.checkLeaseAction({ path, targetWan: target.interface, activeDefaultWan });
      if (verdict.level === 'warn') {
        if (!r.ack) {
          // Nothing written, nothing audited — a prompt is not a refusal, and a
          // denied row here would misrepresent what was attempted.
          return _wanErr('self-cutoff', { warning: verdict.detail, fingerprint: verdict.fingerprint,
                                          name: target.interface, verb });
        }
        if (r.ack !== verdict.fingerprint) {
          // Acknowledged against different values, or our own path moved between
          // the prompt and the retry.
          return _wanErr('stale-warning', { warning: verdict.detail, fingerprint: verdict.fingerprint,
                                            name: target.interface, verb });
        }
      }

      await session.ros.write(_WAN_VERBS[verb], ['=.id=' + r.id]);
      audit.fromSocket(socket).record({ action, targetType: 'wan',
        targetId: r.id, targetName: target.interface, routerId: rid,
        extra: Object.assign({ status: target.status || '' },
                             r.ack ? { selfCutoffAcknowledged: true } : null),
        note: verb === 'release'
          ? 'released the DHCP lease; the uplink is down until the client rebinds'
          : 'requested a DHCP lease renewal' });
      // The lease state settles over the next second or two, so this re-read may
      // still show the old value. The page says "requested" rather than claiming
      // the new state, and the next tick tells the truth.
      await session.wan.refreshNow();
      socket.emit('wan:ok', { action: verb, name: target.interface });
    } catch (e) {
      _wanErr(_rosWriteFail(e), { message: sanitizeErr(e) });
    }
  };

  socket.on('wan:renew',   (req) => _routerWriteQueue(socket.routerId, (rid) => _wanLeaseAction('renew', req, rid)));
  socket.on('wan:release', (req) => _routerWriteQueue(socket.routerId, (rid) => _wanLeaseAction('release', req, rid)));

  // ── Generic resource writes (issue #97) ───────────────────────────────────
  //
  // One set of handlers for every resource in src/routeros/resources.js. The
  // three write features above are hand-written because they came first and
  // each has something genuinely its own — Router Users can lock us out, Queues
  // can throttle us, WAN can cut the uplink. Everything after them is the same
  // seven steps with different field names, so the seven steps live here once
  // and the field names live in the registry.
  //
  // Every property the Router Users block documents at length applies here, and
  // for the same reasons:
  //
  //  1. A FRESH READ in the same tick as the write. `lastPayload` appears
  //     nowhere below, and a test asserts it — the payload is exactly what goes
  //     stale in the dangerous direction.
  //  2. THE NAME IS ROUND-TRIPPED. The browser sends the `.id` and the identity
  //     value it displayed. A `.id` survives a rename, which makes it the right
  //     key to address a row with and the wrong one to identify it by.
  //  3. SERIALISED PER ROUTER, with `rid` captured at enqueue so a router:switch
  //     mid-flight cannot land the write on the router the operator is now
  //     looking at rather than the one they pressed the button on.
  //
  // Both gates on every path: the install-wide page toggle and the role, via
  // _pageAllowed, and `router:write` via _socketCan. Named separately so
  // `router:write` stays greppable.

  const _resErr = (code, extra) =>
    socket.emit('res:error', Object.assign({ code }, extra || {}));

  /**
   * The session, and the collector that owns this resource's view.
   *
   * The write goes through `session.ros`, never the collector, so it works on a
   * router whose collector is switched off (#105) — there is simply nothing to
   * refresh afterwards, which is a missing page rather than a missing
   * capability.
   */
  const _resSession = (rid, resource) => {
    const e = rid ? _routerSessions.get(rid) : null;
    const session = e && e.session;
    const def = _COLLECTOR_DEFS.find(c => c.key === resource.collector);
    const coll = (session && def) ? session[def.sessionProp] : null;
    return { session, coll: (coll && !coll.disabled) ? coll : null };
  };

  const _resMayWrite = (rid, resource) =>
    _pageAllowed(socket, resource.page, 'write') && _socketCan(socket, 'router:write', rid);

  /** Every row in the menu, read now. No proplist: the guards and readOnlyWhen
   *  need fields no page asked for, and this runs once per write, not per tick. */
  const _resRead = async (session, resource) =>
    (await session.ros.write(resource.menu + '/print', []) || []).filter(r => r && r['.id']);

  /** Address by id, identify by the resource's own identity field. */
  const _resFind = (rows, resource, id, expected) => {
    const row = (rows || []).find(r => r['.id'] === id);
    if (!row) return null;
    if (expected !== undefined && expected !== null && expected !== '' &&
        Resources.identityOf(resource, row) !== String(expected)) return null;
    return row;
  };

  /**
   * Where the router sees us from, and on which interface — for selfPath.
   *
   * Both reads are allowed to fail: `/user/active` is denied to the read-only
   * API user the README recommends, and that is the common case. The guard
   * fails open, so a denied read means no warning rather than no write.
   */
  const _resSelfPath = async (session, rid) => {
    let active = [], addrs = [];
    try { active = (await session.ros.write('/user/active/print', []) || []).filter(r => r && r.name); }
    catch (_) { /* denied or unsupported — fails open, by design */ }
    try { addrs = (await session.ros.write('/ip/address/print', []) || []); }
    catch (_) { /* same */ }
    const cfg = Routers.getById(rid) || {};
    return selfPath.resolveManagementInterfaces({
      activeRows: active,
      usernames: [(session.ros.cfg || {}).username, cfg.username],
      addressRows: addrs,
    });
  };

  /**
   * The interface names a write is about — or none, when the edit is harmless.
   *
   * A comment or an MTU change on the bridge we are reachable through is not
   * worth a warning, and warning about it is how a warning becomes furniture
   * (queueGuard.js says the same thing at more length). So an update only
   * counts when it disables the row or changes one of the interface fields
   * themselves. A delete always counts.
   */
  const _resGuardTargets = (resource, action, values, before) => {
    const names = resource.guardInterfaceFields || [];
    const of = (row, name) => {
      const f = resource.fields.find(x => x.name === name);
      return (f && row) ? String(row[f.ros] == null ? '' : row[f.ros]) : '';
    };
    const after = names.map(n => String(values[n] == null ? '' : values[n])).filter(Boolean);
    if (action === 'delete') return names.map(n => of(before, n)).filter(Boolean);
    if (!before) return [];                                   // a create cuts nothing that exists
    const wasEnabled = before.disabled !== 'true';
    const nowDisabled = values.disabled === 'yes' || values.disabled === true;
    const renamed = names.some(n => of(before, n) !== String(values[n] == null ? '' : values[n]));
    if (!renamed && !(wasEnabled && nowDisabled)) return [];
    return after.concat(names.map(n => of(before, n))).filter(Boolean);
  };

  /**
   * Run whichever guard the resource declares, for one write.
   *
   * `null` when it declares none, or when the guard has nothing to say. The two
   * guards ask different questions — selfPath asks which interface carries us,
   * fwGuard asks whether a rule could match us — but they answer in the same
   * shape, which is what lets the acknowledgement below be written once.
   *
   * `before` is the RAW freshly-read row throughout; each guard converts it to
   * whatever it needs.
   */
  const _resVerdict = async (resource, session, rid, what, values, before) => {
    if (!resource.guard) return null;
    const path = await _resSelfPath(session, rid);

    if (resource.guard === 'selfPath') {
      const targets = _resGuardTargets(resource, what, values || {}, before);
      if (!targets.length) return null;
      return selfPath.checkInterfaceEdit({
        path, targets, action: what === 'delete' ? 'delete' : 'update' });
    }

    if (resource.guard === 'fwGuard') {
      // The API port is the one this router is actually reached on, not a
      // guess: a rule that spares 8729 still locks us out of a router we talk
      // to on 8728.
      const cfg = Routers.getById(rid) || {};
      return fwGuard.checkRule({
        menu: resource.menu, values, what,
        before: before ? Resources.rowValues(resource, before) : null,
        ctx: { resolved: path.resolved, addresses: path.addresses || [],
               interfaces: path.interfaces, apiPort: Number(cfg.port) || 8728 },
      });
    }
    return null;
  };

  /**
   * The prompt, and the acknowledgement of it — one implementation for every
   * guard.
   *
   * Nothing is written and nothing is audited on the first pass: this is a
   * question, not a refusal, and a denial row would make the trail lie about
   * what was attempted. The retry carries the fingerprint, recomputed here from
   * a fresh read, so an acknowledgement cannot be carried from one row to
   * another or replayed against a later write.
   *
   * Returns the error payload to send, or null to proceed.
   */
  const _resAckGate = (verdict, ack) => {
    if (!verdict || verdict.level !== 'warn') return null;
    const detail = { warning: verdict.detail, fingerprint: verdict.fingerprint };
    if (!ack) return Object.assign({ code: verdict.code }, detail);
    if (ack !== verdict.fingerprint) return Object.assign({ code: 'stale-warning' }, detail);
    return null;
  };

  /**
   * The values as the audit trail should see them.
   *
   * A `secret` field's VALUE never reaches a row. audit.js masks on field NAME
   * and `presharedKey` does not match its pattern, so relying on that would put
   * a pre-shared key in the one table that is deliberately hard to delete.
   * Keying on the declared type instead means a future secret field is covered
   * the moment it is declared.
   */
  const _resAuditValues = (resource, values) => {
    const out = {};
    for (const f of resource.fields) {
      if (!Object.prototype.hasOwnProperty.call(values, f.name)) continue;
      out[f.name] = f.type === 'secret'
        ? (values[f.name] ? audit.SET : audit.UNSET)
        : values[f.name];
    }
    return out;
  };

  /**
   * The choices a form's pickers offer, read from the router now.
   *
   * "Which DHCP server?" has a right answer the router already knows, and
   * making somebody type it from memory is how you get a typo in a
   * reservation. Each menu is read once per form open and cached across the
   * fields that share it — /interface backs both the VLAN parent and the bridge
   * port, and reading it twice would be silly.
   *
   * EVERY READ FAILS SOFT. A menu the API user cannot see, or that does not
   * exist on this RouterOS version (/routing/table is not on every build), just
   * yields no options, and the field renders as the text box it always was. A
   * picker is a convenience; it must never be the thing that stops a write.
   */
  const _resOptions = async (session, resource) => {
    // Fixed vocabularies — firewall chains and actions — need no read at all.
    const out = Resources.staticOptions(resource);
    const menus = new Map();
    for (const src of Resources.optionSources(resource)) {
      if (!menus.has(src.menu)) {
        try { menus.set(src.menu, (await session.ros.write(src.menu + '/print', [])) || []); }
        catch (_) { menus.set(src.menu, null); }
      }
      const rows = menus.get(src.menu);
      if (!rows) continue;
      const vals = [];
      for (const r of rows) {
        const v = String((r && r[src.value]) || '').trim();
        if (v && vals.indexOf(v) === -1) vals.push(v);
      }
      if (vals.length) out[src.field] = vals.sort();
    }
    return out;
  };

  // ── Undo / redo ────────────────────────────────────────────────────────────
  //
  // Per socket, per resource, in memory, dying with the connection. "Undo" here
  // means "undo what I just did", which is what anyone pressing the button
  // expects — a stack shared between operators would let one silently revert
  // another's work, and a stack that outlived the session would offer to
  // reverse something from last week.
  //
  // Per RESOURCE, not one global stack: undo on the Firewall card must never
  // reach into DNS.

  const _HIST_DEPTH = 20;

  const _histFor = (key) => {
    socket._resHist = socket._resHist || {};
    socket._resHist[key] = socket._resHist[key] || { undo: [], redo: [] };
    return socket._resHist[key];
  };

  const _histEmit = (key) => {
    const h = _histFor(key);
    socket.emit('res:history', {
      resource: key,
      canUndo: h.undo.length > 0, canRedo: h.redo.length > 0,
      undoLabel: h.undo.length ? h.undo[h.undo.length - 1].label : '',
      redoLabel: h.redo.length ? h.redo[h.redo.length - 1].label : '',
    });
  };

  const _histPush = (resource, entry) => {
    if (!entry) return;
    const h = _histFor(resource.key);
    h.undo.push(entry);
    if (h.undo.length > _HIST_DEPTH) h.undo.shift();
    // A fresh action forks the timeline: what was undone can no longer be
    // redone on top of something else.
    h.redo.length = 0;
    _histEmit(resource.key);
  };

  /** The history no longer describes this router, so none of it can be trusted. */
  const _histDrop = (key) => {
    const h = _histFor(key);
    h.undo.length = 0; h.redo.length = 0;
    _histEmit(key);
  };

  /**
   * Where a row sits, as the id of the row AFTER it — or null for the end.
   *
   * An anchor rather than an index, because an index is wrong the moment
   * anything else in the table moves, and undo exists precisely because time
   * has passed.
   */
  const _anchorAt = (rows, at) => (rows[at + 1] ? rows[at + 1]['.id'] : null);

  /** Move `id` so it sits immediately before `anchor`; null sends it to the end. */
  const _resMoveTo = async (session, resource, id, anchor) => {
    const args = ['=numbers=' + id];
    if (anchor) args.push('=destination=' + anchor);
    await session.ros.write(resource.menu + '/move', args);
  };

  /** What a recorded operation is, in the vocabulary the guards speak. */
  const _OP_MEANS = Object.freeze({ add: 'create', set: 'update', remove: 'delete',
                                    move: 'move', enable: 'enable', disable: 'disable' });

  /**
   * Apply one recorded operation, and answer with the id the row now has.
   *
   * An `add` is the awkward one: RouterOS assigns the id, so the row is found
   * by diffing the table against itself rather than by assuming the new row is
   * last. It usually is last — but "usually" is not a thing to build an undo on.
   */
  const _applyOp = async (session, resource, op) => {
    if (op.op === 'add') {
      const validated = Resources.validate(resource, op.values, { editing: false });
      if (!validated.ok) return { error: 'invalid', errors: validated.errors };
      const seen = new Set((await _resRead(session, resource)).map(r => r['.id']));
      await session.ros.write(resource.menu + '/add', Resources.buildArgs(resource, validated));
      const rows = await _resRead(session, resource);
      const made = rows.find(r => !seen.has(r['.id']));
      if (!made) return { error: 'failed' };
      if (resource.ordered) await _resMoveTo(session, resource, made['.id'], op.anchor || null);
      return { id: made['.id'] };
    }

    if (op.op === 'set') {
      const validated = Resources.validate(resource, op.values, { editing: true });
      if (!validated.ok) return { error: 'invalid', errors: validated.errors };
      await session.ros.write(resource.menu + '/set',
        ['=.id=' + op.id].concat(Resources.buildArgs(resource, validated)));
      return { id: op.id };
    }

    if (op.op === 'move') {
      await _resMoveTo(session, resource, op.id, op.anchor || null);
      return { id: op.id };
    }

    // remove, enable, disable — RouterOS has a verb for each.
    await session.ros.write(resource.menu + '/' + op.op, ['=.id=' + op.id]);
    return { id: op.op === 'remove' ? null : op.id };
  };

  /**
   * Undo or redo the top of a stack.
   *
   * Everything the ordinary write handlers do, this does too: both gates, a
   * fresh read, a staleness check, the guard, an audit row, a refresh. An undo
   * is a write like any other — undoing the deletion of a `drop` rule puts that
   * rule back, and it can lock us out exactly as the original did.
   */
  const _histRun = (dir) => (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const resource = _resolve(req);
    if (!resource) return;
    const { session, coll } = _resSession(rid, resource);
    if (!rid || !session) return _resErr('unavailable', { resource: resource.key });
    const action = resource.key + '.' + dir;
    const r = req || {};

    if (!_resMayWrite(rid, resource)) {
      audit.fromSocket(socket).denied({ action, targetType: resource.key, routerId: rid });
      return _resErr('denied', { resource: resource.key });
    }

    const h = _histFor(resource.key);
    const stack = dir === 'undo' ? h.undo : h.redo;
    const entry = stack[stack.length - 1];
    if (!entry) return _resErr('nothing-to-' + dir, { resource: resource.key });
    const op = dir === 'undo' ? entry.reverse : entry.forward;

    try {
      const rows = await _resRead(session, resource);

      // The row this entry is about must still be the row it was about. If it
      // is not, everything below it on the stack is suspect too, so the whole
      // history goes rather than leaving a trap for the next click.
      if (op.op !== 'add') {
        const row = rows.find(x => x['.id'] === op.id);
        if (!row || Resources.identityOf(resource, row) !== entry.identity) {
          _histDrop(resource.key);
          return _resErr('stale-history', { resource: resource.key });
        }
      }
      // An anchor that has been deleted cannot put anything back where it was.
      if (op.anchor && !rows.some(x => x['.id'] === op.anchor)) {
        _histDrop(resource.key);
        return _resErr('stale-history', { resource: resource.key });
      }

      const beforeRow = op.op === 'add' ? null : rows.find(x => x['.id'] === op.id);
      const gate = _resAckGate(
        await _resVerdict(resource, session, rid, _OP_MEANS[op.op],
                          op.values || (beforeRow ? Resources.rowValues(resource, beforeRow) : {}),
                          beforeRow), r.ack);
      if (gate) return _resErr(gate.code, { resource: resource.key, name: entry.label,
        warning: gate.warning, fingerprint: gate.fingerprint });

      const out = await _applyOp(session, resource, op);
      if (out.error) return _resErr(out.error, { resource: resource.key, errors: out.errors });

      // Keep the entry pointing at the row that now exists, and at what it now
      // looks like, so the opposite direction can check it in turn.
      history.rebind(entry, out.id);
      if (out.id) {
        const nowRow = (await _resRead(session, resource)).find(x => x['.id'] === out.id);
        if (nowRow) entry.identity = Resources.identityOf(resource, nowRow);
      }

      stack.pop();
      (dir === 'undo' ? h.redo : h.undo).push(entry);
      _histEmit(resource.key);

      audit.fromSocket(socket).record({ action, targetType: resource.key,
        targetId: out.id ? String(out.id) : null, targetName: entry.identity || null,
        routerId: rid, note: dir + ': ' + entry.label,
        extra: Object.assign({ [dir]: true, op: op.op },
                             r.ack ? { selfLockoutAcknowledged: true } : null) });

      if (coll && typeof coll.refreshNow === 'function') await coll.refreshNow();
      socket.emit('res:ok', { resource: resource.key, action: dir,
                              name: entry.identity || '', movedId: out.id || null });
    } catch (e) {
      _resErr(_rosWriteFail(e), { resource: resource.key, message: sanitizeErr(e) });
    }
  });

  socket.on('res:undo', _histRun('undo'));
  socket.on('res:redo', _histRun('redo'));

  /** Resolve the resource named on the wire, or answer why not. */
  // ── Configuration backups ────────────────────────────────────────────────
  //
  // Read shows the history and the diffs; WRITE is required to take a backup,
  // change the schedule, or download either half of a pair. Downloading is a
  // write-level act deliberately: an export describes the whole network, and
  // the binary carries every key on the device.

  const _bkErr = (code, extra) =>
    socket.emit('backups:error', Object.assign({ code }, extra || {}));

  const _bkMayRead  = () => _pageAllowed(socket, 'backups', 'read');
  const _bkMayWrite = (rid) =>
    _pageAllowed(socket, 'backups', 'write') && _socketCan(socket, 'router:write', rid);

  /** The router this socket is looking at, or null. */
  const _bkRouter = (rid) => (rid ? Routers.getById(rid) : null);

  /** A row the caller is allowed to touch: it must belong to THIS router. */
  const _bkRow = (id, rid) => {
    const row = db.getBackup(id);
    // Not "not found" for a row on another router — the two are the same answer
    // from outside, and distinguishing them would confirm the id exists.
    if (!row || row.router_id !== rid) return null;
    return row;
  };

  const _bkPayload = (rid) => {
    const router = _bkRouter(rid);
    const backup = (router && router.backup) || {};
    return {
      routerId: rid,
      label: router ? router.label : '',
      settings: {
        enabled: !!backup.enabled,
        schedule: backup.schedule || Routers.BACKUP_DEFAULTS.schedule,
        keepCount: backup.keepCount == null ? Routers.BACKUP_DEFAULTS.keepCount : backup.keepCount,
        keepDays: backup.keepDays == null ? Routers.BACKUP_DEFAULTS.keepDays : backup.keepDays,
      },
      summary: db.backupSummary(rid),
      running: Backups._running.has(rid),
      permitted: _bkMayWrite(rid),
      rows: db.listBackups(rid, 200).map(r => ({
        id: r.id, takenAt: r.taken_at, outcome: r.outcome, source: r.source,
        actor: r.actor, stem: r.stem, pruned: !!r.pruned_at,
        bytes: (r.rsc_bytes || 0) + (r.backup_bytes || 0),
        osVersion: r.os_version, model: r.model, serial: r.serial,
        ms: r.ms, error: r.error,
      })),
    };
  };

  socket.on('backups:list', () => {
    const rid = socket.routerId;
    if (!rid || !_bkMayRead()) return _bkErr('denied');
    socket.emit('backups:state', _bkPayload(rid));
  });

  socket.on('backups:settings', async (req) => {
    const rid = socket.routerId;
    if (!rid || !_bkMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action: 'backup.settings', targetType: 'router',
        routerId: rid });
      return _bkErr('denied');
    }
    const r = req || {};
    try {
      Routers.update(rid, { backup: {
        enabled: !!r.enabled, schedule: r.schedule,
        keepCount: r.keepCount, keepDays: r.keepDays,
      } });
      audit.fromSocket(socket).record({ action: 'backup.settings', targetType: 'router',
        scope: 'router', routerId: rid,
        extra: { enabled: !!r.enabled, schedule: r.schedule } });
      socket.emit('backups:state', _bkPayload(rid));
    } catch (e) {
      _bkErr('failed', { message: sanitizeErr(e) });
    }
  });

  socket.on('backups:run', () => _routerWriteQueue(socket.routerId, async (rid) => {
    if (!rid || !_bkMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action: 'backup.run', targetType: 'router',
        routerId: rid });
      return _bkErr('denied');
    }
    const router = _bkRouter(rid);
    if (!router) return _bkErr('unavailable');
    if (!router.backup || !router.backup.password) {
      // Enabling generates the password; without one there is nothing to
      // encrypt the binary with, and an unencrypted backup is not an option.
      return _bkErr('not-configured');
    }
    socket.emit('backups:running', { routerId: rid });
    try {
      const result = await Backups.runFor(router,
        { source: 'manual', actor: socket.request?._authSession?.username || null });
      audit.fromSocket(socket).record({ action: 'backup.run', targetType: 'router',
        scope: 'router', routerId: rid, outcome: result.outcome === 'failed' ? 'error' : 'ok',
        extra: { outcome: result.outcome, changed: !!result.changed } });
      socket.emit('backups:state', _bkPayload(rid));
    } catch (e) {
      _bkErr('failed', { message: sanitizeErr(e) });
    }
  }));

  /**
   * The difference between two stored exports, or between one and the newest.
   *
   * Both ids are checked against this router before either file is opened, so
   * a diff cannot be used to read another router's configuration.
   */
  socket.on('backups:diff', (req) => {
    const rid = socket.routerId;
    if (!rid || !_bkMayRead()) return _bkErr('denied');
    const r = req || {};
    const newer = _bkRow(r.id, rid);
    if (!newer || !newer.stem || newer.pruned_at) return _bkErr('not-found');

    // Default comparison is against the previous stored pair, which is what
    // "what changed in this backup" means.
    let older = null;
    if (r.against) {
      older = _bkRow(r.against, rid);
      if (!older || !older.stem || older.pruned_at) return _bkErr('not-found');
    } else {
      older = db.storedBackups(rid).find(x => x.taken_at < newer.taken_at) || null;
    }

    try {
      const result = older ? Backups.diffOf(older, newer)
                           : BackupDiff.diff('', Backups.readExport(newer));
      socket.emit('backups:diff', {
        id: newer.id, against: older ? older.id : null,
        baseline: !older,
        added: result.added, removed: result.removed,
        truncated: result.truncated, hunks: result.hunks,
      });
    } catch (e) {
      _bkErr('failed', { message: sanitizeErr(e) });
    }
  });

  /**
   * Restore a stored configuration.
   *
   * `/system/backup/load` REPLACES the entire configuration and reboots. Per
   * MikroTik's own documentation a backup carries the device's MAC addresses
   * and belongs to one device, so:
   *
   *   - the recorded serial must equal the router's serial NOW, or refuse
   *     outright. A backup from a different device is never right, and this is
   *     checked before anything is sent.
   *   - a RouterOS version mismatch WARNS but does not block. MikroTik
   *     recommend matching versions, and blocking would stop the restore you
   *     most want after a bad upgrade.
   *   - the operator types the router's name, as packages:upgrade requires.
   *   - the row is audited BEFORE the call, because the connection drops
   *     mid-flight and there may be no "after".
   */
  /**
   * Delete stored restore points: the files, and the row that listed them.
   *
   * Deliberately NOT markBackupPruned, which is retention's half. The two are
   * different acts: retention aging a pair out is something MikroDash did on its
   * own, so a row left behind reading "pruned" explains where the backup went.
   * Pressing Delete is somebody saying "I do not want this listed", and a
   * tombstone answers a question they did not ask.
   *
   * The evidence is not erased with it. audit_events independently holds the
   * backup.run that created each one and the backup.delete below, and the audit
   * table is the one place deliberately hard to clear — so the record of a backup
   * having been taken outlives its row.
   *
   * Every id goes through _bkRow, so it must belong to the router this socket is
   * on — a caller cannot reach another router's backups by guessing ids.
   */
  socket.on('backups:delete', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    if (!rid || !_bkMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action: 'backup.delete', targetType: 'backup',
        routerId: rid });
      return _bkErr('denied');
    }
    const router = _bkRouter(rid);
    if (!router) return _bkErr('not-found');

    // Bounded and de-duplicated: one message must not be able to ask for
    // unbounded filesystem work.
    const raw = (req && Array.isArray(req.ids)) ? req.ids : [];
    const ids = [...new Set(raw.map(Number).filter(Number.isInteger))].slice(0, 200);
    if (!ids.length) return _bkErr('not-found');

    const fallbackDir = BackupStore.dirFor(BackupStore.slugFor(router.label));
    const removed = [];
    let failed = 0;
    for (const id of ids) {
      const row = _bkRow(id, rid);
      // Not ours, or already gone: skip silently. A selection that raced a
      // retention sweep is not an error worth showing.
      if (!row) continue;
      try {
        // A row with no files is still the operator's to remove — a run that
        // stored nothing because the configuration was unchanged, or one whose
        // pair retention already took. Now that Delete clears the row, the
        // History table is a list somebody curates rather than an append-only
        // log, and refusing those would leave rows nothing can ever clear.
        // Files first: drop the row first and fail the unlink, and several MB
        // are orphaned on disk with nothing left pointing at them.
        if (row.stem && !row.pruned_at) BackupStore.removePair(row.dir || fallbackDir, row.stem);
        db.deleteBackup(row.id);
        removed.push(row.id);
      } catch (e) {
        failed++;
        console.error('%s', '[backups] could not delete ' + row.stem + ':', (e && e.message) || e);
      }
    }

    if (removed.length) {
      audit.fromSocket(socket).record({ action: 'backup.delete', targetType: 'backup',
        scope: 'router', routerId: rid, targetId: removed.join(','),
        note: removed.length + ' restore point(s) deleted, rows removed; '
            + 'this audit entry is the surviving record' });
    }

    socket.emit('backups:state', _bkPayload(rid));
    // Everyone else on this router's Backups page re-requests their OWN payload:
    // _bkPayload carries `permitted`, computed for the calling socket, so
    // broadcasting it would tell a viewer they may write.
    socket.to('router-' + rid + '-page-backups').emit('backups:ran', { routerId: rid });
    if (failed) return _bkErr('failed', { message: 'Some restore points could not be removed.' });
  }));

  socket.on('backups:restore', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    if (!rid || !_bkMayWrite(rid)) {
      audit.fromSocket(socket).denied({ action: 'backup.restore', targetType: 'backup',
        routerId: rid });
      return _bkErr('denied');
    }
    const r = req || {};
    const router = _bkRouter(rid);
    const row = _bkRow(r.id, rid);
    if (!router || !row || !row.stem || row.pruned_at) return _bkErr('not-found');

    // Typed confirmation, compared to the label the operator can see.
    if (String(r.confirm || '').trim() !== String(router.label || '').trim()) {
      return _bkErr('confirm-mismatch');
    }

    const entry = _routerSessions.get(rid);
    const session = entry && entry.session;
    if (!session || !session.ros || !session.ros.connected) return _bkErr('unavailable');

    // ── Identity, read fresh from the device ────────────────────────────────
    let serialNow = '', versionNow = '';
    try {
      const rb = ((await session.ros.write('/system/routerboard/print',
        ['=.proplist=serial-number'])) || [])[0] || {};
      serialNow = rb['serial-number'] || '';
      const res = ((await session.ros.write('/system/resource/print',
        ['=.proplist=version'])) || [])[0] || {};
      versionNow = String(res.version || '').split(' ')[0];
    } catch (e) {
      return _bkErr('failed', { message: sanitizeErr(e) });
    }

    if (row.serial && serialNow && row.serial !== serialNow) {
      audit.fromSocket(socket).denied({ action: 'backup.restore', targetType: 'backup',
        scope: 'router', routerId: rid, targetId: String(row.id), note: 'serial-mismatch' });
      return _bkErr('serial-mismatch', { was: row.serial, now: serialNow });
    }

    // A version difference is a question, asked once, not a refusal.
    if (row.os_version && versionNow && row.os_version !== versionNow && !r.acceptVersion) {
      return _bkErr('version-mismatch', { was: row.os_version, now: versionNow });
    }

    // ── The address the ROUTER can reach us at ──────────────────────────────
    let base = String(Settings.load().backupBaseUrl || '').trim().replace(/\/+$/, '');
    if (!base) {
      let active = [];
      try { active = (await session.ros.write('/user/active/print', []) || []).filter(x => x && x.name); }
      catch (_) { /* falls through to the error below */ }
      const self = queueGuard.resolveSelfAddresses(active,
        [(session.ros.cfg || {}).username, router.username]);
      const addr = (self && (self.address || (self.addresses || [])[0])) || '';
      if (!addr) return _bkErr('no-route-back');
      base = 'http://' + addr + ':' + (process.env.PORT || 3081);
    }

    // Audited BEFORE the call: the reboot may take the answer with it.
    audit.fromSocket(socket).record({ action: 'backup.restore', targetType: 'backup',
      scope: 'router', routerId: rid, targetId: String(row.id),
      targetName: row.stem,
      extra: { stem: row.stem, serial: row.serial, fromVersion: row.os_version,
               toVersion: versionNow, acceptedVersionMismatch: !!r.acceptVersion } });

    const token = _mintRestoreToken(row.id, router);
    const dst = 'mikrodash-restore.backup';
    try {
      socket.emit('backups:restoring', { routerId: rid, id: row.id });
      await session.ros.write('/tool/fetch',
        ['=url=' + base + '/api/backups/' + row.id + '/raw?t=' + token, '=dst-path=' + dst]);

      // The load reboots, so this call is not expected to answer. A rejection
      // here is normal and must not be reported as a failed restore.
      session.ros.write('/system/backup/load',
        ['=name=' + dst, '=password=' + (router.backup && router.backup.password) || ''])
        .catch(() => { /* the connection drops as the router reboots */ });

      socket.emit('backups:restored', { routerId: rid, id: row.id });
    } catch (e) {
      _bkErr('failed', { message: sanitizeErr(e) });
    }
  }));

  const _resolve = (req) => {
    const r = req || {};
    const resource = Resources.byKey(typeof r.resource === 'string' ? r.resource : '');
    if (!resource) { _resErr('unknown-resource'); return null; }
    return resource;
  };

  socket.on('res:schema', async (req) => {
    const resource = _resolve(req);
    if (!resource) return;
    const rid = socket.routerId;
    const { session } = _resSession(rid, resource);
    if (!rid) return _resErr('unavailable', { resource: resource.key });
    // Read access to see the page at all; write access is reported separately,
    // because the page draws its Add button from `permitted` rather than from
    // the payload, which every viewer of this router shares.
    if (!_pageAllowed(socket, resource.page, 'read')) return _resErr('denied', { resource: resource.key });

    // A resource whose menu ships with an optional package — VETH comes with
    // containers — is only offered where the menu answers. Reading it is the
    // only way to know: the package list would say the package is installed
    // without saying the menu is reachable by THIS API user. One read, once
    // per connect, for the one resource that asks for it.
    let unsupported = false;
    if (resource.requiresMenu && session) {
      try { await session.ros.write(resource.requiresMenu + '/print', ['=.proplist=.id']); }
      catch (_) { unsupported = true; }
    }

    socket.emit('res:schema', Object.assign(Resources.describe(resource), {
      permitted: !unsupported && _resMayWrite(rid, resource),
      unsupported,
      ordered: !!resource.ordered,
    }));
    // So the undo and redo buttons start out grey rather than absent.
    _histEmit(resource.key);
  });

  /**
   * Opening a blank Add form.
   *
   * Its only job is the pickers: they are read when a form opens rather than
   * shipped with the schema, because the schema is requested for all eight
   * resources on every connect and that would be eight bursts of router reads
   * nobody asked for.
   */
  socket.on('res:new', async (req) => {
    const resource = _resolve(req);
    if (!resource) return;
    const rid = socket.routerId;
    const { session } = _resSession(rid, resource);
    if (!rid || !session) return _resErr('unavailable', { resource: resource.key });
    if (!_resMayWrite(rid, resource)) return _resErr('denied', { resource: resource.key });
    let options = {};
    try { options = await _resOptions(session, resource); }
    catch (_) { /* fails soft — every field falls back to a text box */ }
    socket.emit('res:new', { resource: resource.key, options });
  });

  /**
   * The current values of one row, read fresh, for the edit form.
   *
   * Not taken from the collector payload: payload rows carry collector-shaped
   * field names and are as stale as the last tick, and a form filled from stale
   * values would write them back. Gated on write because opening the edit form
   * is the first half of an edit.
   */
  socket.on('res:row', async (req) => {
    const resource = _resolve(req);
    if (!resource) return;
    const rid = socket.routerId;
    const { session } = _resSession(rid, resource);
    if (!rid || !session) return _resErr('unavailable', { resource: resource.key });
    if (!_resMayWrite(rid, resource)) return _resErr('denied', { resource: resource.key });
    const id = (req || {}).id;
    if (!id) return _resErr('bad-request', { resource: resource.key });
    try {
      const rows = await _resRead(session, resource);
      const row  = _resFind(rows, resource, id, (req || {}).expectedIdentity);
      if (!row) return _resErr('stale-row', { resource: resource.key });
      let options = {};
      try { options = await _resOptions(session, resource); }
      catch (_) { /* fails soft — every field falls back to a text box */ }
      socket.emit('res:row', {
        resource: resource.key, id,
        identity: Resources.identityOf(resource, row),
        readOnly: !!(resource.readOnlyWhen && resource.readOnlyWhen(row)),
        actions: (resource.actions || []).filter(a => a.when(row)).map(a => a.key),
        values: Resources.rowValues(resource, row),
        options,
      });
    } catch (e) {
      _resErr(_rosWriteFail(e), { resource: resource.key, message: sanitizeErr(e) });
    }
  });

  /**
   * The exact sentence a save would send, without sending it (#97 asks for a
   * preview before apply). Not queued, because it writes nothing.
   */
  socket.on('res:preview', (req) => {
    const resource = _resolve(req);
    if (!resource) return;
    const rid = socket.routerId;
    if (!rid) return _resErr('unavailable', { resource: resource.key });
    if (!_resMayWrite(rid, resource)) return _resErr('denied', { resource: resource.key });
    const r = req || {};
    const validated = Resources.validate(resource, r.values, { editing: !!r.id });
    if (!validated.ok) return _resErr('invalid', { resource: resource.key, errors: validated.errors });
    socket.emit('res:preview', {
      resource: resource.key,
      command: Resources.previewCommand(resource, validated, r.id || null),
    });
  });

  socket.on('res:save', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const resource = _resolve(req);
    if (!resource) return;
    const { session, coll } = _resSession(rid, resource);
    if (!rid || !session) return _resErr('unavailable', { resource: resource.key });
    const r = req || {};
    const editing = !!r.id;
    const action  = resource.key + (editing ? '.update' : '.create');

    if (!_resMayWrite(rid, resource)) {
      audit.fromSocket(socket).denied({ action, targetType: resource.key, routerId: rid,
        targetId: editing ? String(r.id) : null,
        targetName: r.expectedIdentity ? String(r.expectedIdentity) : null });
      return _resErr('denied', { resource: resource.key });
    }

    const validated = Resources.validate(resource, r.values, { editing });
    if (!validated.ok) return _resErr('invalid', { resource: resource.key, errors: validated.errors });
    const name = String(validated.values[resource.identity] || r.expectedIdentity || '');

    try {
      const rows   = await _resRead(session, resource);
      const before = editing ? _resFind(rows, resource, r.id, r.expectedIdentity) : null;
      if (editing && !before) return _resErr('stale-row', { resource: resource.key, name });

      // Checked on the freshly-read row, never on the browser's claim about it.
      if (before && resource.readOnlyWhen && resource.readOnlyWhen(before)) {
        audit.fromSocket(socket).denied({ action, targetType: resource.key, routerId: rid,
          targetId: String(r.id), targetName: name, note: 'read-only-row' });
        return _resErr('read-only-row', { resource: resource.key, name });
      }

      const gate = _resAckGate(
        await _resVerdict(resource, session, rid, editing ? 'update' : 'create',
                          validated.values, before), r.ack);
      if (gate) return _resErr(gate.code, { resource: resource.key, name,
        warning: gate.warning, fingerprint: gate.fingerprint });

      const args = Resources.buildArgs(resource, validated);
      if (editing) await session.ros.write(resource.menu + '/set', ['=.id=' + r.id].concat(args));
      else         await session.ros.write(resource.menu + '/add', args);

      // Recorded for undo. A create needs one extra read: RouterOS assigns the
      // id, and the row is found by diffing against the read taken before the
      // write rather than by assuming it landed last.
      let newId = r.id, anchorAfter;
      if (!editing) {
        const seen  = new Set(rows.map(x => x['.id']));
        const after = await _resRead(session, resource);
        const made  = after.find(x => !seen.has(x['.id']));
        newId = made && made['.id'];
        if (resource.ordered && made) anchorAfter = _anchorAt(after, after.indexOf(made));
      }
      if (newId) _histPush(resource, history.buildEntry({
        resource, what: editing ? 'update' : 'create', id: newId,
        identity: name,
        before: before ? Resources.rowValues(resource, before) : null,
        after: validated.values, anchorAfter }));

      audit.fromSocket(socket).record({ action, targetType: resource.key,
        targetId: editing ? String(r.id) : null, targetName: name, routerId: rid,
        before: before ? _resAuditValues(resource, Resources.rowValues(resource, before)) : {},
        after:  _resAuditValues(resource, validated.values),
        extra:  r.ack ? { selfCutoffAcknowledged: true } : undefined });

      if (coll && typeof coll.refreshNow === 'function') await coll.refreshNow();
      socket.emit('res:ok', { resource: resource.key, action: editing ? 'update' : 'create', name });
    } catch (e) {
      _resErr(_rosWriteFail(e), { resource: resource.key, name, message: sanitizeErr(e) });
    }
  }));

  socket.on('res:remove', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const resource = _resolve(req);
    if (!resource) return;
    const { session, coll } = _resSession(rid, resource);
    if (!rid || !session) return _resErr('unavailable', { resource: resource.key });
    const r = req || {};
    const action = resource.key + '.delete';

    if (!_resMayWrite(rid, resource)) {
      audit.fromSocket(socket).denied({ action, targetType: resource.key, routerId: rid,
        targetId: r.id ? String(r.id) : null,
        targetName: r.expectedIdentity ? String(r.expectedIdentity) : null });
      return _resErr('denied', { resource: resource.key });
    }
    if (!r.id) return _resErr('bad-request', { resource: resource.key });

    try {
      const rows   = await _resRead(session, resource);
      const before = _resFind(rows, resource, r.id, r.expectedIdentity);
      if (!before) return _resErr('stale-row', { resource: resource.key });
      const name = Resources.identityOf(resource, before);

      if (resource.readOnlyWhen && resource.readOnlyWhen(before)) {
        audit.fromSocket(socket).denied({ action, targetType: resource.key, routerId: rid,
          targetId: String(r.id), targetName: name, note: 'read-only-row' });
        return _resErr('read-only-row', { resource: resource.key, name });
      }

      const gate = _resAckGate(
        await _resVerdict(resource, session, rid, 'delete', {}, before), r.ack);
      if (gate) return _resErr(gate.code, { resource: resource.key, name,
        warning: gate.warning, fingerprint: gate.fingerprint });

      // Where it sat, so undo can put it back rather than append it.
      const anchorBefore = resource.ordered
        ? _anchorAt(rows, rows.findIndex(x => x['.id'] === r.id)) : undefined;

      await session.ros.write(resource.menu + '/remove', ['=.id=' + r.id]);

      _histPush(resource, history.buildEntry({
        resource, what: 'delete', id: String(r.id), identity: name,
        before: Resources.rowValues(resource, before), anchorBefore }));

      audit.fromSocket(socket).record({ action, targetType: resource.key,
        targetId: String(r.id), targetName: name, routerId: rid,
        before: _resAuditValues(resource, Resources.rowValues(resource, before)), after: {},
        extra: r.ack ? { selfCutoffAcknowledged: true } : undefined });

      if (coll && typeof coll.refreshNow === 'function') await coll.refreshNow();
      socket.emit('res:ok', { resource: resource.key, action: 'delete', name });
    } catch (e) {
      _resErr(_rosWriteFail(e), { resource: resource.key, message: sanitizeErr(e) });
    }
  }));

  /**
   * A named verb a resource declares — make-static on a DHCP lease today.
   *
   * Kept separate from save because it is not a form: it takes no fields, and
   * its `when` decides which rows may receive it. That `when` is evaluated
   * against the fresh read, so the browser offering the button is a hint, never
   * a permission.
   */
  socket.on('res:action', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const resource = _resolve(req);
    if (!resource) return;
    const { session, coll } = _resSession(rid, resource);
    if (!rid || !session) return _resErr('unavailable', { resource: resource.key });
    const r   = req || {};
    const def = (resource.actions || []).find(a => a.key === r.action);
    if (!def) return _resErr('bad-request', { resource: resource.key });
    const action = resource.key + '.' + def.key;

    if (!_resMayWrite(rid, resource)) {
      audit.fromSocket(socket).denied({ action, targetType: resource.key, routerId: rid,
        targetId: r.id ? String(r.id) : null,
        targetName: r.expectedIdentity ? String(r.expectedIdentity) : null });
      return _resErr('denied', { resource: resource.key });
    }
    if (!r.id) return _resErr('bad-request', { resource: resource.key });

    try {
      const rows = await _resRead(session, resource);
      const row  = _resFind(rows, resource, r.id, r.expectedIdentity);
      if (!row) return _resErr('stale-row', { resource: resource.key });
      const name = Resources.identityOf(resource, row);
      if (!def.when(row)) {
        audit.fromSocket(socket).denied({ action, targetType: resource.key, routerId: rid,
          targetId: String(r.id), targetName: name, note: 'not-applicable' });
        return _resErr('not-applicable', { resource: resource.key, name });
      }

      // A named verb is still a write. Enabling a firewall rule has exactly the
      // blast radius of creating it, and disabling the accept that lets us in
      // is how the other half of a lockout happens.
      const gate = _resAckGate(
        await _resVerdict(resource, session, rid, def.key,
                          Resources.rowValues(resource, row), row), r.ack);
      if (gate) return _resErr(gate.code, { resource: resource.key, name,
        warning: gate.warning, fingerprint: gate.fingerprint });

      await session.ros.write(resource.menu + '/' + def.verb, ['=.id=' + r.id]);

      // enable and disable invert each other, so they are recorded. A verb with
      // no inverse — make-static, say — yields no entry and buildEntry says so
      // by returning null.
      _histPush(resource, history.buildEntry({
        resource, what: def.key, id: String(r.id), identity: name }));

      audit.fromSocket(socket).record({ action, targetType: resource.key,
        targetId: String(r.id), targetName: name, routerId: rid, note: def.note });

      if (coll && typeof coll.refreshNow === 'function') await coll.refreshNow();
      socket.emit('res:ok', { resource: resource.key, action: def.key, name });
    } catch (e) {
      _resErr(_rosWriteFail(e), { resource: resource.key, message: sanitizeErr(e) });
    }
  }));


  /**
   * Reorder a rule in a table where position is meaning.
   *
   * Firewall only, today, and `ordered` is what says so. Everywhere else the
   * router keeps its own order and moving a row would mean nothing.
   *
   * THE BROWSER SENDS A DIRECTION, NEVER A POSITION. The neighbour is resolved
   * here, from a read taken in this same tick, so an operator clicking twice
   * quickly — or two operators at once — cannot move a rule to an index
   * computed against a table that has already changed underneath them. It is
   * the same reasoning as the fresh read everywhere else in this block, applied
   * to ordering instead of to values.
   */
  socket.on('res:move', (req) => _routerWriteQueue(socket.routerId, async (rid) => {
    const resource = _resolve(req);
    if (!resource) return;
    if (!resource.ordered) return _resErr('bad-request', { resource: resource.key });
    const { session, coll } = _resSession(rid, resource);
    if (!rid || !session) return _resErr('unavailable', { resource: resource.key });
    const r      = req || {};
    // A drag says WHERE, an arrow says WHICH WAY. Both refuse to name an index:
    // `anchor` is the id the row should land before (or '' for the end), which
    // stays correct if the table shifts, and a direction is resolved against a
    // read taken here.
    const anchored = Object.prototype.hasOwnProperty.call(r, 'anchor');
    const up       = r.direction === 'up';
    const action   = resource.key + '.move';

    if (!_resMayWrite(rid, resource)) {
      audit.fromSocket(socket).denied({ action, targetType: resource.key, routerId: rid,
        targetId: r.id ? String(r.id) : null,
        targetName: r.expectedIdentity ? String(r.expectedIdentity) : null });
      return _resErr('denied', { resource: resource.key });
    }
    if (!r.id || (!anchored && r.direction !== 'up' && r.direction !== 'down'))
      return _resErr('bad-request', { resource: resource.key });

    try {
      const rows = await _resRead(session, resource);
      const at   = rows.findIndex(x => x['.id'] === r.id);
      if (at === -1) return _resErr('stale-row', { resource: resource.key });
      const row  = rows[at];
      const name = Resources.identityOf(resource, row);
      if (r.expectedIdentity !== undefined && r.expectedIdentity !== null &&
          r.expectedIdentity !== '' && name !== String(r.expectedIdentity))
        return _resErr('stale-row', { resource: resource.key });
      if (anchored) {
        // The row the drag aimed at must still be there. If it has gone, the
        // table the operator was looking at is not the table on the router, and
        // dropping the rule somewhere approximate is worse than saying so.
        if (r.anchor && !rows.some(x => x['.id'] === r.anchor))
          return _resErr('stale-row', { resource: resource.key, name });
        // Dropped exactly where it already was.
        if (_anchorAt(rows, at) === (r.anchor || null))
          return _resErr('at-end', { resource: resource.key, name });
      } else if (up ? at === 0 : at === rows.length - 1) {
        // Already where it is going. Not an error worth a banner, but the page
        // should stop drawing an arrow that does nothing.
        return _resErr('at-end', { resource: resource.key, name });
      }

      const gate = _resAckGate(
        await _resVerdict(resource, session, rid, 'move',
                          Resources.rowValues(resource, row), row), r.ack);
      if (gate) return _resErr(gate.code, { resource: resource.key, name,
        warning: gate.warning, fingerprint: gate.fingerprint });

      // RouterOS inserts the moved rule BEFORE `destination`. So moving up
      // means "before the rule currently above me", and moving down means
      // "before the rule two below" — with no destination at all when there is
      // nothing below, which sends it to the end.
      //
      // A drag names its destination directly, as the id it should land before
      // (`anchor`), which is still not an index: an anchor survives the table
      // shifting underneath it and an ordinal does not.
      const anchorBefore = _anchorAt(rows, at);
      const dest = anchored ? r.anchor
                 : up      ? rows[at - 1]['.id']
                           : (rows[at + 2] ? rows[at + 2]['.id'] : null);
      await _resMoveTo(session, resource, r.id, dest);

      const moved = await _resRead(session, resource);
      const nowAt = moved.findIndex(x => x['.id'] === r.id);
      _histPush(resource, history.buildEntry({
        resource, what: 'move', id: String(r.id), identity: name,
        anchorBefore, anchorAfter: _anchorAt(moved, nowAt) }));

      audit.fromSocket(socket).record({ action, targetType: resource.key,
        targetId: String(r.id), targetName: name, routerId: rid,
        before: { position: at }, after: { position: nowAt },
        extra: Object.assign({ how: anchored ? 'drag' : (up ? 'up' : 'down') },
                             r.ack ? { selfLockoutAcknowledged: true } : null) });

      if (coll && typeof coll.refreshNow === 'function') await coll.refreshNow();
      // `movedId` is what the page pulses, so the eye can find the row that
      // just changed places in a table of thirty near-identical ones.
      socket.emit('res:ok', { resource: resource.key, action: 'move', name, movedId: String(r.id) });
    } catch (e) {
      _resErr(_rosWriteFail(e), { resource: resource.key, message: sanitizeErr(e) });
    }
  }));


  // ── WiFi frequency analyzer ───────────────────────────────────────────────
  //
  // The only disruptive action in the app: it takes the radio off the air and
  // drops every client on it. Gated on BOTH the page toggle and the action
  // permission, and answers rather than going silent — unlike firewall:tab this
  // is a button somebody pressed, and silence is a support ticket.

  const _scanSession = () => {
    const rid = socket.routerId;
    const e   = rid ? _routerSessions.get(rid) : null;
    return { rid, entry: e, session: e && e.session };
  };

  const _scanDenied = (code, extra) =>
    socket.emit('wifiscan:error', Object.assign({ scanId: null, code }, extra || {}));

  socket.on('wifiscan:interfaces', () => {
    const { rid, session } = _scanSession();
    if (!rid || !session) return _scanDenied('unavailable');
    if (!_pageAllowed(socket, 'wireless', 'read')) return _scanDenied('denied');
    const wl = session.wireless;
    socket.emit('wifiscan:interfaces', {
      // May scan at all — the button is drawn from this, not from the list.
      permitted: _socketCan(socket, 'router:scan', rid) && _pageAllowed(socket, 'wireless', 'write'),
      interfaces: (wl && typeof wl.listScannableInterfaces === 'function')
        ? wl.listScannableInterfaces().map(i => ({ name: i.name, running: i.running, clients: i.clients }))
        : [],
      scanning: wifiScans.isScanning(rid),
    });
  });

  socket.on('wifiscan:start', async (req) => {
    const { rid, session } = _scanSession();
    if (!rid || !session) return _scanDenied('unavailable');
    // Both gates, deliberately. _pageAllowed carries the install-wide Wireless
    // toggle — a deployment that turned the page off must not have a scan
    // endpoint — and _socketCan keeps router:scan the named, greppable gate.
    if (!_pageAllowed(socket, 'wireless', 'write') || !_socketCan(socket, 'router:scan', rid)) {
      audit.fromSocket(socket).denied({ action: 'wifi.scan', routerId: rid,
        targetName: req && req.iface ? String(req.iface) : null });
      return _scanDenied('denied');
    }

    const iface       = req && typeof req.iface === 'string' ? req.iface : '';
    const durationSec = req && Number(req.durationSec);
    const wl          = session.wireless;
    const interfaces  = (wl && typeof wl.listScannableInterfaces === 'function')
      ? wl.listScannableInterfaces() : null;

    // Read the operating channel BEFORE the scan: during one the radio is off
    // its channel, and the interface's own channel.frequency is a configured
    // RANGE ("5180-5730"), not where it actually is. This is a fast =once= read,
    // unlike the scan itself which must never go through write().
    let currentChannelMhz = null;
    try {
      const mon = await session.ros.write('/interface/wifi/monitor', ['=numbers=' + iface, '=once=']);
      const raw = mon && mon[0] && mon[0].channel;
      const n   = parseInt(String(raw || '').split('/')[0], 10);
      if (Number.isFinite(n)) currentChannelMhz = n;
    } catch (_) { /* advisory only — a scan without it is still useful */ }

    audit.fromSocket(socket).record({ action: 'wifi.scan', targetType: 'interface',
      targetName: iface, routerId: rid,
      note: 'takes the radio off the air; connected clients are disconnected for the scan' });
    const res = wifiScans.start({
      routerId: rid, ros: session.ros, iface, durationSec,
      socketId: socket.id, currentChannelMhz, interfaces,
      emit: (ev, d) => socket.emit(ev, d),
    });
    if (!res.ok) {
      const { code, ...rest } = res;
      return _scanDenied(code, rest);
    }
    socket.emit('wifiscan:state', {
      scanning: true, scanId: res.scanId, iface, durationSec,
      startedAt: res.startedAt, endsAt: res.endsAt, currentChannelMhz, rows: [],
    });
    console.log('%s', `[${(session.ros && session.ros.routerLabel) || rid}][wifiscan] ${iface} for ${durationSec}s — clients on that radio will drop`);
  });

  socket.on('wifiscan:stop', (req) => {
    const { rid } = _scanSession();
    if (!rid) return;
    if (!_socketCan(socket, 'router:scan', rid)) return _scanDenied('denied');
    wifiScans.abort(rid, req && req.scanId);
  });

  // Per-user router switching (modern auth only).
  socket.on('router:switch', (newRouterId) => {
    // Every undo entry names a row on the router being left, so none of it
    // means anything on the next one. Dropped rather than carried.
    socket._resHist = {};
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
