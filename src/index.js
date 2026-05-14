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
const { createBasicAuthMiddleware } = require('./auth/basicAuth');
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

const compression = require('compression');
const app = express();

const TRUSTED_PROXY = process.env.TRUSTED_PROXY;
if (TRUSTED_PROXY) app.set('trust proxy', TRUSTED_PROXY);

const server = http.createServer(app);
const MAX_SOCKETS = parseInt(process.env.MAX_SOCKETS || '50', 10);
const io = new Server(server, {
  maxHttpBufferSize: 1e6,
  connectTimeout: 10000,
  perMessageDeflate: { threshold: 128, zlibDeflateOptions: { level: 1 } },
});

// Auth middleware reads settings dynamically so changes via the Settings UI
// take effect on the next request without a server restart.
const authLimiter = rateLimit({ windowMs: 60_000, max: 100, standardHeaders: true, legacyHeaders: false });

function _authMiddleware(req, res, next) {
  const s = Settings.load();
  if (!(s.dashUser && s.dashPass)) return next(); // auth not configured
  createBasicAuthMiddleware({ username: s.dashUser, password: s.dashPass })(req, res, next);
}

app.use(helmet(buildHelmetOptions()));
app.use(compression());
app.use((req, res, next) => {
  if (req.path === '/healthz') return next();
  authLimiter(req, res, (err) => { if (err) return next(err); _authMiddleware(req, res, next); });
});
io.engine.use((req, res, next) => { // codeql[js/missing-rate-limiting]
  authLimiter(req, res, (err) => { if (err) return next(err); _authMiddleware(req, res, next); });
});
app.use('/vendor', express.static(path.join(__dirname, '..', 'public', 'vendor'), { maxAge: '7d' }));
app.use(express.static(path.join(__dirname, '..', 'public')));
app.use(express.json({ limit: '50kb' }));

// ── Active router session ─────────────────────────────────────────────────────
// All mutable collector/ROS state lives here so buildSession() can replace
// it atomically on a hot-swap without touching anything else in the module.
let _session      = null; // set by buildSession() below
let _noRouterMode = false; // true when no router is configured yet

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

function buildSession(routerCfg) {
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

  const dhcpLeases   = new DhcpLeasesCollector ({ros,io, state});
  const arp          = new ArpCollector         ({ros,    pollMs:_cfg.pollArp,       state});
  const dhcpNetworks = new DhcpNetworksCollector({ros,io, pollMs:_cfg.pollDhcp,      dhcpLeases, state, wanIface:DEFAULT_IF});
  const traffic      = new TrafficCollector     ({ros,io, defaultIf:DEFAULT_IF, historyMinutes:HISTORY_MINUTES, pollMs:1000, state});
  const conns        = new ConnectionsCollector ({ros,io, pollMs:_cfg.pollConns,    topN:_cfg.topN, maxConns:_cfg.maxConns, dhcpNetworks, dhcpLeases, arp, state, connTableCache, geoOrgCache});
  const talkers      = new TopTalkersCollector  ({ros,io, pollMs:_cfg.pollTalkers,  state, topN:_cfg.topTalkersN});
  const logs         = new LogsCollector        ({ros,io, state});
  const system       = new SystemCollector      ({ros,io, pollMs:_cfg.pollSystem,   state});
  const wireless     = new WirelessCollector    ({ros,io, pollMs:_cfg.pollWireless, state, dhcpLeases, arp});
  const vpn          = new VpnCollector         ({ros,io, pollMs:_cfg.pollVpn,      state});
  const firewall     = new FirewallCollector    ({ros,io, pollMs:_cfg.pollFirewall,  state, topN:_cfg.firewallTopN});
  const ifStatus     = new InterfaceStatusCollector({ros,io, pollMs:_cfg.pollIfstatus, metaPollMs:_cfg.pollIfaces, state});
  const ping         = new PingCollector        ({ros,io, pollMs:_cfg.pollPing,     state, target:PING_TARGET});
  const bandwidth    = new BandwidthCollector   ({ros,io, pollMs:_cfg.pollBandwidth, dhcpNetworks, dhcpLeases, arp, ifStatus, state, connTableCache, geoOrgCache});
  const routing      = new RoutingCollector     ({ros,io, pollMs:_cfg.pollRouting,  state});
  const netwatch     = new NetwatchCollector    ({ros,io,                           state});

  const allCollectors = [traffic, dhcpLeases, dhcpNetworks, arp, conns, talkers, logs, system, wireless, vpn, firewall, ifStatus, ping, bandwidth, routing, netwatch];

  return { ros, state, connTableCache, DEFAULT_IF, HISTORY_MINUTES,
           dhcpLeases, dhcpNetworks, arp, traffic, conns, talkers, logs, system,
           wireless, vpn, firewall, ifStatus, ping, bandwidth, routing, netwatch, allCollectors,
           routerId: routerCfg.id, cachedInterfaces: null };
}

// ── Session teardown ──────────────────────────────────────────────────────────
// Stop all collectors and the ROS connection for the current session.
// Returns a Promise that resolves when the old connection is fully closed.
async function teardownSession(session) {
  if (!session) return;
  startupReady = false;
  _collectorsStarted = false;
  for (const c of session.allCollectors) {
    if (typeof c.stop === 'function') c.stop();
  }
  session.ros.stop();
  // Brief yield so in-flight async callbacks can settle before we replace the session
  await new Promise(r => setTimeout(r, 150));
}

// ── Startup state (module-level, reset on hot-swap) ───────────────────────────
let startupReady     = false;
let rosConnected     = false;
let _collectorsStarted = false;

function broadcastRosStatus(connected, reason) {
  rosConnected = connected;
  io.emit('ros:status', { connected, reason: reason || null });
}

function wireRosEvents(session) {
  const { ros } = session;
  const host = ros.cfg.host;
  const port = ros.cfg.port || 8729;
  const user = ros.cfg.username;
  const tls  = ros.cfg.tls !== false;
  let _prevConnected = null;

  function _emitRouterStatus(connected) {
    if (session.routerId) {
      io.emit('router:status', { routerId: session.routerId, connected });
      const r = Routers.getById(session.routerId);
      const label = (r && r.label) || host;
      if (_prevConnected !== null && _prevConnected !== connected)
        alerter.fireConnectivityAlert(session.routerId, label, connected);
      _prevConnected = connected;
    }
  }

  ros.on('connected', () => {
    console.log(`[ROS] ✓ connected to ${host}:${port} as "${user}" (${tls ? 'TLS' : 'plain'})`);
    session.cachedInterfaces = null; // invalidate on reconnect — interfaces may have changed
    broadcastRosStatus(true);
    _emitRouterStatus(true);
    // Restore connections stream if someone was on the Connections page when the
    // session dropped. Collector reconnect handlers fire before this listener,
    // so start() has already reset to fallback-poll mode by this point.
    _updateConnsStream(session);
  });
  ros.on('close', () => {
    session.connTableCache.invalidate();
    console.log(`[ROS] connection to ${host}:${port} closed`);
    broadcastRosStatus(false, 'RouterOS connection closed');
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
    console.error(`[ROS] ✗ ${reason}`);
    if (hint) console.error(`[ROS]   → ${hint}`);
    if (e && e.errno) console.error(`[ROS]   errno: ${e.errno}`);
    console.error(`[ROS]   raw: ${msg}`);
    broadcastRosStatus(false, reason);
    _emitRouterStatus(false);
  });
  ros.on('connected', () => startCollectors(session));
}

async function startCollectors(session) {
  if (_collectorsStarted) return;
  _collectorsStarted = true;
  const _delay = ms => new Promise(r => setTimeout(r, ms));
  try {
    console.log(`[MikroDash] v${APP_VERSION} — RouterOS connected, starting collectors`);
    // Group A — foundation collectors; awaits provide natural sequencing.
    session.wireless.start();
    await session.dhcpLeases.start();
    // Run the first dhcpNetworks tick synchronously so networks/wanIp are
    // populated before sendInitialState broadcasts to connected sockets.
    await session.dhcpNetworks.tick().catch(() => {});
    session.dhcpNetworks.start();
    await session.arp.start();
    // Group B — streaming collectors staggered 75 ms apart to avoid a burst
    // of simultaneous stream-open commands hitting the router at once.
    session.traffic.start();
    await _delay(75);
    session.conns.start();
    session.talkers.start();
    await _delay(75);
    session.logs.start();
    await _delay(75);
    // Set callback before start() so the first board-name tick never races past it
    session.system._onFirstBoardName = (boardName) => {
      const router = Routers.getById(session.routerId);
      if (router && (router.label === 'My Router' || router.label === router.host)) {
        Routers.updateLabel(session.routerId, boardName);
        // Broadcast updated router list to all clients
        io.emit('routers:update', Routers.getPublic());
      }
    };
    session.system.start();
    await session.vpn.start();
    await session.firewall.start();
    await session.ifStatus.start();
    session.ping.start();
    session.bandwidth.start();
    await session.routing.start();
    await session.netwatch.start();

    startupReady = true;
    console.log('[MikroDash] All collectors running');

    // If no browser clients are connected at startup, suspend high-frequency
    // streams immediately — they will resume when the first client connects.
    if (io.engine.clientsCount === 0) _idleSuspend(session);

    // Broadcast initial state to all currently connected sockets.
    // On first startup there are none yet, so this is a no-op.
    // On a hot-swap the Socket.IO connections stay alive — existing browser
    // clients never receive a 'connection' event, so without this they would
    // not get the new router's traffic:history, leases:list, or lastPayload
    // snapshots until they manually refreshed the page.
    for (const [, socket] of io.sockets.sockets) {
      // Rebind traffic streaming to new collector instance
      session.traffic.bindSocket(socket);
      // Re-send all initial state for the new router
      sendInitialState(socket).catch((e) => {
        console.error('[MikroDash] sendInitialState failed for socket', socket.id, ':', e && e.message ? e.message : e);
      });
    }
  } catch (e) {
    startupReady = false;
    _collectorsStarted = false;
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
    broadcastRosStatus(false, `Switching to ${router.label}…`);
    io.emit('router:switching', { routerId: newRouterId, label: router.label });

    // Save the new active router id
    Settings.save({ activeRouterId: newRouterId });

    // Tear down old session (may be null on first-ever activation from setup wizard)
    if (_session) await teardownSession(_session);
    _noRouterMode = false;

    // Build and start new session
    _collectorsStarted = false;
    const newSession = buildSession(router);
    _session = newSession;
    wireRosEvents(newSession);
    newSession.ros.connectLoop();

    return { ok: true };
  } finally {
    _switching = false;
  }
}

// ── Initial session bootstrap ─────────────────────────────────────────────────
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
    return;
  }

  const activeRouter = Routers.getById(activeId);
  _session = buildSession(activeRouter);
  wireRosEvents(_session);
  _session.ros.connectLoop();
})();

alerter.init(io, Settings.load());
alertSessions.init(io);
(function _syncAlertSessions() {
  const cfg = Settings.load();
  alertSessions.syncSessions(Routers.loadAll(), cfg.activeRouterId || '');
})();

// ── Dashboard layout API ──────────────────────────────────────────────────────
const LAYOUT_FILE = path.join(process.env.DATA_DIR || '/data', 'dashboard-layout.json');
const layoutLimiter = rateLimit({ windowMs: 60_000, max: 60, standardHeaders: true, legacyHeaders: false });

app.get('/api/dashboard-layout', layoutLimiter, (_req, res) => {
  try {
    if (fs.existsSync(LAYOUT_FILE)) {
      res.json(JSON.parse(fs.readFileSync(LAYOUT_FILE, 'utf8')));
    } else {
      res.json(null);
    }
  } catch (_) { res.json(null); }
});

app.post('/api/dashboard-layout', layoutLimiter, (req, res) => {
  try {
    const body = req.body || {};
    if (!Array.isArray(body.cards)) return res.status(400).json({ ok: false });
    fs.writeFileSync(LAYOUT_FILE, JSON.stringify({ cards: body.cards }), 'utf8');
    res.json({ ok: true });
  } catch (e) { res.status(500).json({ ok: false }); }
});

// ── Settings API ──────────────────────────────────────────────────────────────
app.get('/api/settings', (_req, res) => {
  res.json(Settings.getPublic());
});

app.post('/api/settings', (req, res) => {
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
      });
      return res.json({ ok:true, requiresRestart:false });
    }
    const updates = {};
    const intFields = {
      routerPort:[1,65535], pollConns:[500,60000], pollTalkers:[500,60000], pollSystem:[500,60000],
      pollWireless:[500,60000], pollVpn:[500,30000],  pollFirewall:[500,30000],
      pollIfstatus:[500,60000], pollIfaces:[10000,600000], pollPing:[1000,5000], pollArp:[5000,300000],
      pollBandwidth:[500,60000], pollDhcp:[5000,600000], topN:[1,50], topTalkersN:[1,20],
      firewallTopN:[1,50], vpnDashTopN:[1,50], maxConns:[1000,100000], historyMinutes:[5,120],
      alertCpuThreshold:[1,100], alertPingLoss:[1,100], notifCooldownSec:[10,3600],
      smtpPort:[1,65535],
    };
    const strFields  = ['dashUser', 'pingTarget', 'telegramChatId', 'notifTitle', 'smtpHost', 'smtpFrom', 'smtpTo'];
    const boolFields = ['pageWireless','pageInterfaces','pageDhcp','pageVpn',
                        'pageConnections','pageFirewall','pageLogs','pageBandwidth','pageRouting',
                        'pingEnabled','rosDebug',
                        'telegramEnabled','pushbulletEnabled','smtpEnabled','smtpSecure',
                        'notifIfaceUpDown','notifVpn','notifCpu','notifPing','notifNetwatch','notifRouterStatus',
                        'notifIfaceEther','notifIfaceWlan','notifIfaceBridge','notifIfaceVlan','notifIfaceOther'];
    const credFields = ['dashPass', 'telegramBotToken', 'pushbulletApiKey', 'smtpUser', 'smtpPass'];

    for (const [f, range] of Object.entries(intFields)) {
      if (f in body) { const v = parseInt(body[f],10); if (!isNaN(v) && v>=range[0] && v<=range[1]) updates[f]=v; }
    }
    for (const f of strFields)  { if (f in body) updates[f] = String(body[f]).trim().slice(0,256); }
    for (const f of boolFields) { if (f in body) updates[f] = body[f]===true||body[f]==='true'; }
    for (const f of credFields) { if (f in body && !Settings.isMasked(body[f])) updates[f] = String(body[f]).slice(0,512); }
    if ('notifBody'   in body) updates.notifBody   = String(body.notifBody).trim().slice(0, 512);
    if ('notifBodyUp' in body) updates.notifBodyUp = String(body.notifBodyUp).trim().slice(0, 512);

    const saved = Settings.save(updates);
    alerter.updateSettings(saved);

    // Apply poll interval changes live
    const s = _session;
    const collectorMap = { conns:s.conns, talkers:s.talkers, system:s.system, wireless:s.wireless, vpn:s.vpn, firewall:s.firewall, ifStatus:s.ifStatus, ping:s.ping, arp:s.arp, dhcpNetworks:s.dhcpNetworks, bandwidth:s.bandwidth, routing:s.routing };
    const pollMap = { pollConns:'conns', pollTalkers:'talkers', pollSystem:'system', pollWireless:'wireless',
      pollVpn:'vpn', pollFirewall:'firewall', pollIfstatus:'ifStatus', pollBandwidth:'bandwidth',
      pollPing:'ping', pollArp:'arp', pollDhcp:'dhcpNetworks' };
    for (const [key, name] of Object.entries(pollMap)) {
      if (key in updates) {
        const col = collectorMap[name];
        if (col) {
          col.pollMs = saved[key];
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

    // pollIfaces controls the /interface/print + /ip/address/print stream interval
    if ('pollIfaces' in updates && s.ifStatus) {
      s.ifStatus.metaPollMs = saved.pollIfaces;
      s.ifStatus._restartMetaStreams();
    }

    // pollFirewall controls the counter poll interval — restart it live
    if ('pollFirewall' in updates && s.firewall) {
      s.firewall.pollMs = saved.pollFirewall;
      s.firewall._stopCounterPoll();
      s.firewall._startCounterPoll();
    }

    // pollVpn controls the VPN counter poll interval — restart it live
    if ('pollVpn' in updates && s.vpn) {
      s.vpn.pollMs = saved.pollVpn;
      s.vpn._stopCounterPoll();
      s.vpn._startCounterPoll();
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

app.post('/api/settings/test-notification', _testNotifLimiter, async (req, res) => {
  try {
    const { channel, apiKey, botToken, chatId,
            smtpHost, smtpPort, smtpSecure, smtpUser, smtpPass, smtpFrom, smtpTo } = req.body || {};
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
    };
    await notifier.testChannel(settings, channel);
    res.json({ ok: true });
  } catch (e) {
    console.error('[test-notification]', e.message);
    res.status(500).json({ ok: false, error: sanitizeErr(e) });
  }
});

// ── Routers API ───────────────────────────────────────────────────────────────

// GET /api/routers — list all routers (passwords masked)
app.get('/api/routers', (_req, res) => {
  const _cfg   = Settings.load();
  const active = _cfg.activeRouterId || '';
  res.json({ routers: Routers.getPublic(), activeId: active });
});

// POST /api/routers — add a new router
app.post('/api/routers', (req, res) => {
  try {
    const body = req.body || {};
    if (!body.host || !String(body.host).trim()) {
      return res.status(400).json({ ok:false, error:'host is required' });
    }
    const router = Routers.add(body);
    io.emit('routers:update', Routers.getPublic());
    alertSessions.syncSessions(Routers.loadAll(), Settings.load().activeRouterId || '');
    res.json({ ok:true, router: { ...router, password: router.password ? '••••••••' : '' } });
  } catch(e) {
    res.status(500).json({ ok:false, error: sanitizeErr(e) });
  }
});

// PUT /api/routers/:id — edit a router
app.put('/api/routers/:id', (req, res) => {
  try {
    const router = Routers.update(req.params.id, req.body || {});
    if (!router) return res.status(404).json({ ok:false, error:'Router not found' });
    io.emit('routers:update', Routers.getPublic());

    // If this is the active router and pingTarget changed, update the live
    // collector immediately — don't make the user wait for the next poll cycle.
    const activeId = Settings.load().activeRouterId;
    if (_session && req.params.id === activeId && req.body && req.body.pingTarget) {
      const newTarget = router.pingTarget;
      if (_session.ping && _session.ping.target !== newTarget) {
        _session.ping.target      = newTarget;
        _session.ping._lastFp     = '';
        _session.ping._lossWindow = [];
        _session.ping._restartStream();
        if (_session.ping.lastPayload) {
          const updated = { ..._session.ping.lastPayload, target: newTarget, ts: Date.now() };
          _session.ping.lastPayload = updated;
          io.emit('ping:update', updated);
        }
      }
    }

    alertSessions.syncSessions(Routers.loadAll(), activeId || '');
    res.json({ ok:true, router: { ...router, password: router.password ? '••••••••' : '' } });
  } catch(e) {
    res.status(500).json({ ok:false, error: sanitizeErr(e) });
  }
});

// DELETE /api/routers/:id — delete a router (cannot delete the active router)
app.delete('/api/routers/:id', (req, res) => {
  try {
    const _cfg = Settings.load();
    if (req.params.id === _cfg.activeRouterId) {
      return res.status(409).json({ ok:false, error:'Cannot delete the active router. Switch to a different router first.' });
    }
    const deleted = Routers.remove(req.params.id);
    if (!deleted) return res.status(404).json({ ok:false, error:'Router not found' });
    io.emit('routers:update', Routers.getPublic());
    alertSessions.syncSessions(Routers.loadAll(), _cfg.activeRouterId || '');
    res.json({ ok:true });
  } catch(e) {
    res.status(500).json({ ok:false, error: sanitizeErr(e) });
  }
});

// POST /api/routers/:id/activate — switch to a different router (hot-swap)
app.post('/api/routers/:id/activate', async (req, res) => {
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
  // Broadcast updated active state to all clients
  io.emit('routers:update', Routers.getPublic());
  io.emit('router:active', { activeId: req.params.id });
  alertSessions.syncSessions(Routers.loadAll(), req.params.id);
});

// POST /api/routers/test — test a connection without saving
app.post('/api/routers/test', async (req, res) => {
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
  if (!_session) return res.json({ cc: '', wanIp: '' });
  const wanIp = (_session.state.lastWanIp || '').split('/')[0];
  let cc = '';
  if (geoip && wanIp) { const g = geoip.lookup(wanIp); if (g) cc = g.country || ''; }
  res.json({ cc, wanIp });
});

function sanitizeErr(e) {
  if (!e) return null;
  return String(e).split('\n')[0].slice(0, 200);
}

app.get('/healthz', (_req, res) => {
  const s = _session;
  const { ok, statusCode } = computeHealthStatus({
    startupReady,
    rosConnected: s ? s.ros.connected : false,
  });
  const st = s ? s.state : {};
  const body = {
    ok,
    version: APP_VERSION,
    routerConnected: s ? s.ros.connected : false,
    activeRouterId:  s ? s.routerId : null,
    startupReady,
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

// ── Socket.IO ─────────────────────────────────────────────────────────────────
async function sendInitialState(socket) {
  // No router configured yet — prompt the browser to show the setup wizard.
  if (_noRouterMode) {
    socket.emit('setup:required', {});
    socket.emit('routers:update', []);
    return;
  }

  const s = _session;
  const _ps = Settings.load(); // single load — used for routers, settings:pages, pingEnabled

  socket.emit('traffic:history', {
    ifName: s.DEFAULT_IF,
    windowMinutes: s.HISTORY_MINUTES,
    points: s.traffic.hist.get(s.DEFAULT_IF) ? s.traffic.hist.get(s.DEFAULT_IF).toArray() : [],
  });

  // Send current router list and active id
  socket.emit('routers:update', Routers.getPublic());
  socket.emit('router:active', { activeId: _ps.activeRouterId || '' });
  // Send live reachability status for active router and all alert-session routers
  if (_ps.activeRouterId) socket.emit('router:status', { routerId: _ps.activeRouterId, connected: rosConnected });
  for (const [routerId, connected] of alertSessions.getStatusMap()) {
    socket.emit('router:status', { routerId, connected });
  }

  if (!s.ros.connected) {
    socket.emit('ros:status', { connected: false, reason: rosConnected === false
      ? 'RouterOS is not connected — retrying in background'
      : 'Waiting for RouterOS connection…' });
    try { await s.ros.waitUntilConnected(10000); } catch (_) {}
  }

  let ifs = [];
  try {
    if (!s.cachedInterfaces) s.cachedInterfaces = await fetchInterfaces(s.ros);
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
  });

  if (_ps.pingEnabled !== false) {
    const pingData = s.ping.getHistory();
    if (pingData.history.length) socket.emit('ping:history', pingData);
  }

  const logHistory = s.logs.getHistory();
  if (logHistory.length) socket.emit('logs:history', logHistory);
}

function _idleSuspend(session) {
  if (!session || !startupReady) return;
  session.conns.suspend();
  session.ifStatus.suspend();
  session.system.suspend();
  session.wireless.suspend();
  session.vpn.suspend();
  session.firewall.suspend();
  session.ping.suspend();
  session.talkers.suspend();
}

function _idleResume(session) {
  if (!session || !startupReady) return;
  // conns is page-aware — _updateConnsStream() manages its stream independently
  session.ifStatus.resume();
  session.system.resume();
  session.wireless.resume();
  session.vpn.resume();
  session.firewall.resume();
  session.ping.resume();
  session.talkers.resume();
}

// Sync the connections stream with page-connections room membership.
// Called whenever the room might have changed (page focus/blur, disconnect, reconnect).
// Resumes the full =interval= stream when the Connections page is open;
// suspends it (fallback poll takes over) when nobody is watching.
function _updateConnsStream(session) {
  if (!session || !startupReady) return;
  const viewers = io.sockets.adapter.rooms.get('page-connections')?.size || 0;
  if (viewers > 0) {
    session.conns.resume();
  } else {
    session.conns.suspend();
  }
}

io.on('connection', (socket) => {
  if (io.engine.clientsCount > MAX_SOCKETS) {
    console.warn('[MikroDash] connection rejected — max sockets reached:', MAX_SOCKETS);
    socket.disconnect(true);
    return;
  }

  // Idle manager: resume streams/timers when the first browser client connects.
  if (io.engine.clientsCount === 1) _idleResume(_session);

  socket.on('disconnect', () => {
    if (io.engine.clientsCount === 0) _idleSuspend(_session);
    // The disconnecting socket may have been the last one on the Connections page.
    // Rooms are cleaned up before this event fires, so the room size is already correct.
    if (_session) _updateConnsStream(_session);
  });

  // Page-aware rooms — clients join/leave rooms as they navigate pages.
  // Collectors for page-specific events (bandwidth, firewall, logs) emit only
  // to the relevant room, avoiding unnecessary serialization for idle pages.
  socket.on('page:focus', (name) => {
    if (typeof name === 'string' && /^[a-z]{2,20}$/.test(name)) {
      socket.join('page-' + name);
      // Immediately replay the last known payload for room-scoped collectors
      // so the page isn't stale while waiting for the next poll cycle.
      const s = _session;
      if (!s) return;
      if (name === 'connections') _updateConnsStream(s);
      if (name === 'firewall'  && s.firewall  && s.firewall.lastPayload)
        socket.emit('firewall:update',  { ...s.firewall.lastPayload,  ts: Date.now() });
      if (name === 'bandwidth' && s.bandwidth && s.bandwidth.lastPayload)
        socket.emit('bandwidth:update', { ...s.bandwidth.lastPayload, ts: Date.now() });
      if (name === 'logs'      && s.logs)
        socket.emit('logs:history', { entries: s.logs.getHistory() });
      if (name === 'connections' && s.conns && s.conns.lastPayload) {
        if (s.conns.lastPayload.countryDests)
          socket.emit('conn:country-data', { ts: s.conns.lastPayload.ts, countryDests: s.conns.lastPayload.countryDests, countryPorts: s.conns.lastPayload.countryPorts });
        if (s.conns.lastPayload.sourceDests)
          socket.emit('conn:source-data',  { ts: s.conns.lastPayload.ts, sourceDests:  s.conns.lastPayload.sourceDests, sourcePorts: s.conns.lastPayload.sourcePorts  });
      }
    }
  });
  socket.on('page:blur', (name) => {
    if (typeof name === 'string' && /^[a-z]{2,20}$/.test(name)) {
      socket.leave('page-' + name);
      if (name === 'connections' && _session) _updateConnsStream(_session);
    }
  });

  // Dashboard card rooms — emitted by dashboard-grid.js via custom DOM events
  // relayed through app.js when a room-gated card is visible on the dashboard.
  socket.on('dashcard:focus', (key) => {
    if (typeof key !== 'string' || !/^[a-z]{2,20}$/.test(key)) return;
    socket.join('dash-card-' + key);
    const s = _session;
    if (!s) return;
    if (key === 'firewall'  && s.firewall  && s.firewall.lastPayload)
      socket.emit('firewall:update',  { ...s.firewall.lastPayload,  ts: Date.now() });
    if (key === 'bandwidth' && s.bandwidth && s.bandwidth.lastPayload)
      socket.emit('bandwidth:update', { ...s.bandwidth.lastPayload, ts: Date.now() });
    if (key === 'logs' && s.logs)
      socket.emit('logs:history', { entries: s.logs.getHistory() });
  });
  socket.on('dashcard:blur', (key) => {
    if (typeof key !== 'string' || !/^[a-z]{2,20}$/.test(key)) return;
    socket.leave('dash-card-' + key);
  });

  if (_session) _session.traffic.bindSocket(socket);
  // If this is the first browser client and idle-gated collectors haven't run
  // yet (lastPayload is null), kick them and wait for them to complete before
  // sending initial state — otherwise sendInitialState fires before the tick
  // finishes and finds lastPayload === null.
  const kickAndSend = async () => {
    if (_session) {
      // Kick idle-gated collectors that haven't run yet (connections, bandwidth,
      // talkers). Wireless handles its own startup via force-tick in start().
      const idleGated = [_session.conns, _session.bandwidth, _session.talkers];
      const kicks = idleGated
        .filter(c => c && !c.lastPayload && typeof c.tick === 'function')
        .map(c => c.tick(true).catch(() => {}));
      if (kicks.length) await Promise.allSettled(kicks);
    }
    await sendInitialState(socket);
  };
  kickAndSend().catch(() => {});
});

const PORT = parseInt(process.env.PORT || '3081', 10);
server.listen(PORT, () => console.log(`[MikroDash] v${APP_VERSION} listening on http://0.0.0.0:${PORT}`));

// ── Graceful shutdown ─────────────────────────────────────────────────────────
function shutdown(signal) {
  console.log(`[MikroDash] ${signal} received, shutting down…`);
  startupReady = false;
  if (_session) {
    for (const c of _session.allCollectors) {
      if (typeof c.stop === 'function') c.stop();
    }
    _session.ros.stop();
  }
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
