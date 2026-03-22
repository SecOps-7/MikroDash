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

const app = express();

const TRUSTED_PROXY = process.env.TRUSTED_PROXY;
if (TRUSTED_PROXY) app.set('trust proxy', TRUSTED_PROXY);

const server = http.createServer(app);
const MAX_SOCKETS = parseInt(process.env.MAX_SOCKETS || '50', 10);
const io = new Server(server, {
  maxHttpBufferSize: 1e6,
  connectTimeout: 10000,
  perMessageDeflate: { threshold: 512, zlibDeflateOptions: { level: 1 } },
});

const authEnabled = !!(process.env.BASIC_AUTH_USER && process.env.BASIC_AUTH_PASS);
const authLimiter = rateLimit({
  windowMs: 60_000, max: 100,
  standardHeaders: true, legacyHeaders: false,
  skip: (req) => !authEnabled || req.path === '/healthz',
});
const basicAuth = createBasicAuthMiddleware({
  username: process.env.BASIC_AUTH_USER,
  password: process.env.BASIC_AUTH_PASS,
});

app.use(helmet(buildHelmetOptions()));
app.use((req, res, next) => {
  if (req.path === '/healthz') return next();
  authLimiter(req, res, (err) => { if (err) return next(err); basicAuth(req, res, next); });
});
io.engine.use(basicAuth);
app.use(express.static(path.join(__dirname, '..', 'public')));
app.use(express.json());

// ── Active router session ─────────────────────────────────────────────────────
// All mutable collector/ROS state lives here so buildSession() can replace
// it atomically on a hot-swap without touching anything else in the module.
let _session = null; // set by buildSession() below

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
  };
}

function buildSession(routerCfg) {
  const _cfg   = Settings.load();
  const state  = _freshState();

  const ros = new ROS({
    host:           routerCfg.host,
    port:           routerCfg.port,
    tls:            routerCfg.tls,
    tlsInsecure:    routerCfg.tlsInsecure,
    username:       routerCfg.username,
    password:       routerCfg.password,
    debug:          (process.env.ROS_DEBUG || 'false').toLowerCase() === 'true',
    writeTimeoutMs: parseInt(process.env.ROS_WRITE_TIMEOUT_MS || '30000', 10),
  });

  const DEFAULT_IF      = routerCfg.defaultIf  || _cfg.defaultIf  || 'ether1';
  const PING_TARGET     = routerCfg.pingTarget  || _cfg.pingTarget || '1.1.1.1';
  const HISTORY_MINUTES = _cfg.historyMinutes;

  const connTableCache = {
    rows: null, ts: 0,
    maxAge: Math.min(_cfg.pollConns, _cfg.pollBandwidth) * 0.9,
    updateMaxAge(pollConns, pollBandwidth) {
      this.maxAge = Math.min(pollConns, pollBandwidth) * 0.9;
    },
    async get(rosInst) {
      const now = Date.now();
      if (this.rows !== null && (now - this.ts) < this.maxAge) return this.rows;
      this.rows = (await rosInst.write('/ip/firewall/connection/print', [
        '=.proplist=.id,src-address,dst-address,protocol,dst-port,orig-bytes,repl-bytes',
      ])) || [];
      this.ts = Date.now();
      return this.rows;
    },
    invalidate() { this.rows = null; this.ts = 0; },
  };

  const dhcpLeases   = new DhcpLeasesCollector ({ros,io, state});
  const arp          = new ArpCollector         ({ros,    pollMs:_cfg.pollArp,       state});
  const dhcpNetworks = new DhcpNetworksCollector({ros,io, pollMs:_cfg.pollDhcp,      dhcpLeases, state, wanIface:DEFAULT_IF});
  const traffic      = new TrafficCollector     ({ros,io, defaultIf:DEFAULT_IF, historyMinutes:HISTORY_MINUTES, pollMs:1000, state});
  const conns        = new ConnectionsCollector ({ros,io, pollMs:_cfg.pollConns,    topN:_cfg.topN, maxConns:_cfg.maxConns, dhcpNetworks, dhcpLeases, arp, state, connTableCache});
  const talkers      = new TopTalkersCollector  ({ros,io, pollMs:_cfg.pollTalkers,  state, topN:_cfg.topTalkersN});
  const logs         = new LogsCollector        ({ros,io, state});
  const system       = new SystemCollector      ({ros,io, pollMs:_cfg.pollSystem,   state});
  const wireless     = new WirelessCollector    ({ros,io, pollMs:_cfg.pollWireless, state, dhcpLeases, arp});
  const vpn          = new VpnCollector         ({ros,io, pollMs:_cfg.pollVpn,      state});
  const firewall     = new FirewallCollector    ({ros,io, pollMs:_cfg.pollFirewall,  state, topN:_cfg.firewallTopN});
  const ifStatus     = new InterfaceStatusCollector({ros,io, pollMs:_cfg.pollIfstatus, state});
  const ping         = new PingCollector        ({ros,io, pollMs:_cfg.pollPing,     state, target:PING_TARGET});
  const bandwidth    = new BandwidthCollector   ({ros,io, pollMs:_cfg.pollBandwidth, dhcpNetworks, dhcpLeases, arp, ifStatus, state, connTableCache});
  const routing      = new RoutingCollector     ({ros,io, pollMs:_cfg.pollRouting,  state});

  const allCollectors = [traffic, dhcpLeases, dhcpNetworks, arp, conns, talkers, logs, system, wireless, vpn, firewall, ifStatus, ping, bandwidth, routing];

  return { ros, state, connTableCache, DEFAULT_IF, HISTORY_MINUTES,
           dhcpLeases, dhcpNetworks, arp, traffic, conns, talkers, logs, system,
           wireless, vpn, firewall, ifStatus, ping, bandwidth, routing, allCollectors,
           routerId: routerCfg.id };
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

  ros.on('connected', () => {
    console.log(`[ROS] ✓ connected to ${host}:${port} as "${user}" (${tls ? 'TLS' : 'plain'})`);
    broadcastRosStatus(true);
  });
  ros.on('close', () => {
    session.connTableCache.invalidate();
    console.log(`[ROS] connection to ${host}:${port} closed`);
    broadcastRosStatus(false, 'RouterOS connection closed');
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
    } else if (/authentication/i.test(msg) || /login/i.test(msg) || /invalid user/i.test(msg) || /wrong password/i.test(msg)) {
      reason = 'Authentication failed — check username and password';
      hint   = `Confirm user "${user}" exists on the router and has API access: /user print`;
    } else if (tls && /RosException/.test(msg)) {
      reason = 'TLS handshake failed — check that RouterOS api-ssl is enabled';
      hint   = 'Run: /ip service set api-ssl disabled=no  — and verify the certificate is valid';
    }
    console.error(`[ROS] ✗ ${reason}`);
    if (hint) console.error(`[ROS]   → ${hint}`);
    console.error(`[ROS]   raw: ${msg}`);
    broadcastRosStatus(false, reason);
  });
  ros.on('connected', () => startCollectors(session));
}

async function startCollectors(session) {
  if (_collectorsStarted) return;
  _collectorsStarted = true;
  try {
    console.log(`[MikroDash] v${APP_VERSION} — RouterOS connected, starting collectors`);
    session.wireless.start();
    await session.dhcpLeases.start();
    // Run the first dhcpNetworks tick synchronously so networks/wanIp are
    // populated before sendInitialState broadcasts to connected sockets.
    await session.dhcpNetworks.tick().catch(() => {});
    session.dhcpNetworks.start();
    await session.arp.start();
    session.traffic.start();
    session.conns.start();
    session.talkers.start();
    session.logs.start();
    session.system.start();
    await session.vpn.start();
    await session.firewall.start();
    await session.ifStatus.start();
    session.ping.start();
    session.bandwidth.start();
    await session.routing.start();

    // Auto-update router label from board name on first connect
    // (only if label is still the default 'My Router' or matches host)
    session.system._onFirstBoardName = (boardName) => {
      const router = Routers.getById(session.routerId);
      if (router && (router.label === 'My Router' || router.label === router.host)) {
        Routers.updateLabel(session.routerId, boardName);
        // Broadcast updated router list to all clients
        io.emit('routers:update', Routers.getPublic());
      }
    };

    startupReady = true;
    console.log('[MikroDash] All collectors running');

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

    // Tear down old session
    await teardownSession(_session);

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
    console.error('[MikroDash] No routers configured. Add a router in Settings.');
    // Start with a dummy session so the server stays up
    const fallback = {
      host: '127.0.0.1', port: 8729, tls: false, tlsInsecure: false,
      username: 'admin', password: '', defaultIf: 'ether1', pingTarget: '1.1.1.1', id: null,
    };
    _session = buildSession(fallback);
    wireRosEvents(_session);
    _session.ros.connectLoop();
    return;
  }

  const activeRouter = Routers.getById(activeId);
  _session = buildSession(activeRouter);
  wireRosEvents(_session);
  _session.ros.connectLoop();
})();

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
      });
      return res.json({ ok:true, requiresRestart:false });
    }
    const updates = {};
    const intFields = {
      routerPort:[1,65535], pollConns:[500,60000], pollTalkers:[500,60000], pollSystem:[500,60000],
      pollWireless:[500,60000], pollVpn:[1000,120000], pollFirewall:[1000,30000],
      pollIfstatus:[500,60000], pollPing:[1000,120000], pollArp:[5000,300000],
      pollBandwidth:[500,60000], pollDhcp:[5000,600000], topN:[1,50], topTalkersN:[1,20],
      firewallTopN:[1,50], maxConns:[1000,100000], historyMinutes:[5,120],
      alertCpuThreshold:[1,100], alertPingLoss:[1,100],
    };
    const strFields  = ['dashUser', 'pingTarget'];
    const boolFields = ['pageWireless','pageInterfaces','pageDhcp','pageVpn',
                        'pageConnections','pageFirewall','pageLogs','pageBandwidth','pageRouting'];
    const credFields = ['dashPass'];

    for (const [f, range] of Object.entries(intFields)) {
      if (f in body) { const v = parseInt(body[f],10); if (!isNaN(v) && v>=range[0] && v<=range[1]) updates[f]=v; }
    }
    for (const f of strFields)  { if (f in body) updates[f] = String(body[f]).trim().slice(0,256); }
    for (const f of boolFields) { if (f in body) updates[f] = body[f]===true||body[f]==='true'; }
    for (const f of credFields) { if (f in body && !Settings.isMasked(body[f])) updates[f] = String(body[f]).slice(0,512); }

    const saved = Settings.save(updates);

    // Apply poll interval changes live
    const s = _session;
    const collectorMap = { conns:s.conns, talkers:s.talkers, system:s.system, wireless:s.wireless, vpn:s.vpn, firewall:s.firewall, ifStatus:s.ifStatus, ping:s.ping, arp:s.arp, dhcpNetworks:s.dhcpNetworks, bandwidth:s.bandwidth, routing:s.routing };
    const pollMap = { pollConns:'conns', pollTalkers:'talkers', pollSystem:'system', pollWireless:'wireless',
      pollVpn:'vpn', pollFirewall:'firewall', pollIfstatus:'ifStatus', pollBandwidth:'bandwidth',
      pollPing:'ping', pollArp:'arp', pollDhcp:'dhcpNetworks' };
    for (const [key, name] of Object.entries(pollMap)) {
      if (key in updates) {
        const col = collectorMap[name];
        if (col && col.timer) {
          col.pollMs = saved[key];
          clearInterval(col.timer); col.timer = null;
          const run = async () => {
            if (col._inflight) return; col._inflight = true;
            try { await col.tick(); } catch(_){} finally { col._inflight = false; }
          };
          col.timer = setInterval(run, col.pollMs);
        }
      }
    }
    if ('pollConns' in updates || 'pollBandwidth' in updates) {
      s.connTableCache.updateMaxAge(saved.pollConns, saved.pollBandwidth);
    }

    // pollFirewall controls the counter poll interval — restart it live
    if ('pollFirewall' in updates && s.firewall) {
      s.firewall.pollMs = saved.pollFirewall;
      s.firewall._stopCounterPoll();
      s.firewall._startCounterPoll();
    }

    // Apply pingTarget change live — the collector stores it as this.target
    if ('pingTarget' in updates && s.ping) {
      s.ping.target = saved.pingTarget;
      s.ping._lastFp = ''; // force next tick to emit with the new target label
      // Broadcast immediately so the dashboard label updates without waiting
      // up to pollPing ms for the next scheduled tick.
      if (s.ping.lastPayload) {
        const updated = { ...s.ping.lastPayload, target: saved.pingTarget, ts: Date.now() };
        s.ping.lastPayload = updated;
        io.emit('ping:update', updated);
      }
    }

    const pageSettings = {
      pageWireless:saved.pageWireless, pageInterfaces:saved.pageInterfaces,
      pageDhcp:saved.pageDhcp, pageVpn:saved.pageVpn,
      pageConnections:saved.pageConnections, pageFirewall:saved.pageFirewall,
      pageLogs:saved.pageLogs, pageBandwidth:saved.pageBandwidth,
      pageRouting:saved.pageRouting,
      alertCpuThreshold:saved.alertCpuThreshold, alertPingLoss:saved.alertPingLoss,
    };
    io.emit('settings:pages', pageSettings);
    res.json({ ok:true, requiresRestart:false });
  } catch(e) {
    console.error('[settings] save error:', e);
    res.status(500).json({ ok:false, error: String(e.message||e) });
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
    res.json({ ok:true, router: { ...router, password: router.password ? '••••••••' : '' } });
  } catch(e) {
    res.status(500).json({ ok:false, error: String(e.message||e) });
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
        _session.ping.target  = newTarget;
        _session.ping._lastFp = ''; // force next tick to emit with new target
        if (_session.ping.lastPayload) {
          const updated = { ..._session.ping.lastPayload, target: newTarget, ts: Date.now() };
          _session.ping.lastPayload = updated;
          io.emit('ping:update', updated);
        }
      }
    }

    res.json({ ok:true, router: { ...router, password: router.password ? '••••••••' : '' } });
  } catch(e) {
    res.status(500).json({ ok:false, error: String(e.message||e) });
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
    res.json({ ok:true });
  } catch(e) {
    res.status(500).json({ ok:false, error: String(e.message||e) });
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
});

// POST /api/routers/test — test a connection without saving
app.post('/api/routers/test', async (req, res) => {
  const body = req.body || {};
  if (!body.host) return res.status(400).json({ ok:false, error:'host is required' });

  const testRos = new ROS({
    host:           String(body.host).trim(),
    port:           parseInt(body.port || '8729', 10),
    tls:            body.tls !== false && body.tls !== 'false',
    tlsInsecure:    !!(body.tlsInsecure || body.tlsInsecure === 'true'),
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
    else if (/authentication/i.test(msg) || /login/i.test(msg)) reason = 'Authentication failed — check username and password';
    else if (body.tls && /RosException/.test(msg))              reason = 'TLS handshake failed — check that RouterOS api-ssl is enabled and the certificate is valid.';
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
    },
  };
  res.status(statusCode).json(body);
});

// ── Socket.IO ─────────────────────────────────────────────────────────────────
async function sendInitialState(socket) {
  const s = _session;

  socket.emit('traffic:history', {
    ifName: s.DEFAULT_IF,
    windowMinutes: s.HISTORY_MINUTES,
    points: s.traffic.hist.get(s.DEFAULT_IF) ? s.traffic.hist.get(s.DEFAULT_IF).toArray() : [],
  });

  // Send current router list and active id
  const _cfg = Settings.load();
  socket.emit('routers:update', Routers.getPublic());
  socket.emit('router:active', { activeId: _cfg.activeRouterId || '' });

  if (!s.ros.connected) {
    socket.emit('ros:status', { connected: false, reason: rosConnected === false
      ? 'RouterOS is not connected — retrying in background'
      : 'Waiting for RouterOS connection…' });
    try { await s.ros.waitUntilConnected(10000); } catch (_) {}
  }

  let ifs = [];
  try {
    ifs = await fetchInterfaces(s.ros);
    s.traffic.setAvailableInterfaces(ifs);
  } catch (e) {
    const reason = e && e.message ? e.message : String(e);
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
  if (s.conns.lastPayload)     socket.emit('conn:update',      s.conns.lastPayload);
  if (s.talkers.lastPayload)   socket.emit('talkers:update',   s.talkers.lastPayload);
  if (s.ping.lastPayload)      socket.emit('ping:update',      s.ping.lastPayload);
  if (s.bandwidth.lastPayload) socket.emit('bandwidth:update', s.bandwidth.lastPayload);
  if (s.routing.lastPayload)   socket.emit('routing:update',   s.routing.lastPayload);

  const _ps = Settings.load();
  socket.emit('settings:pages', {
    pageWireless:_ps.pageWireless, pageInterfaces:_ps.pageInterfaces,
    pageDhcp:_ps.pageDhcp, pageVpn:_ps.pageVpn,
    pageConnections:_ps.pageConnections, pageFirewall:_ps.pageFirewall,
    pageLogs:_ps.pageLogs, pageBandwidth:_ps.pageBandwidth, pageRouting:_ps.pageRouting,
    alertCpuThreshold:_ps.alertCpuThreshold, alertPingLoss:_ps.alertPingLoss,
  });

  const pingData = s.ping.getHistory();
  if (pingData.history.length) socket.emit('ping:history', pingData);

  const logHistory = s.logs.getHistory();
  if (logHistory.length) socket.emit('logs:history', logHistory);
}

io.on('connection', (socket) => {
  if (io.engine.clientsCount > MAX_SOCKETS) {
    console.warn('[MikroDash] connection rejected — max sockets reached:', MAX_SOCKETS);
    socket.disconnect(true);
    return;
  }
  _session.traffic.bindSocket(socket);
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
