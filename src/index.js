require('dotenv').config();
const Settings = require('./settings');
const _cfg = Settings.load();

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

const app = express();

// When behind a reverse proxy, set TRUSTED_PROXY to the proxy's IP (e.g. "127.0.0.1")
// or "loopback" / "uniquelocal" to trust X-Forwarded-For from those ranges.
// Unset = disabled (direct connections only, X-Forwarded-For ignored).
const TRUSTED_PROXY = process.env.TRUSTED_PROXY;
if (TRUSTED_PROXY) app.set('trust proxy', TRUSTED_PROXY);

const server = http.createServer(app);
const MAX_SOCKETS = parseInt(process.env.MAX_SOCKETS || '50', 10);
const io = new Server(server, {
  maxHttpBufferSize: 1e6,
  connectTimeout: 10000,
  perMessageDeflate: {
    threshold: 512,
    zlibDeflateOptions: { level: 1 },
  },
});
const authEnabled = !!(process.env.BASIC_AUTH_USER && process.env.BASIC_AUTH_PASS);
const authLimiter = rateLimit({
  windowMs: 60_000,
  max: 100,
  standardHeaders: true,
  legacyHeaders: false,
  skip: (req) => !authEnabled || req.path === '/healthz',
});
const basicAuth = createBasicAuthMiddleware({
  username: process.env.BASIC_AUTH_USER,
  password: process.env.BASIC_AUTH_PASS,
});

app.use(helmet(buildHelmetOptions()));
app.use((req, res, next) => {
  if (req.path === '/healthz') return next();
  authLimiter(req, res, (err) => {
    if (err) return next(err);
    basicAuth(req, res, next);
  });
});
io.engine.use(basicAuth);
app.use(express.static(path.join(__dirname, '..', 'public')));
app.use(express.json());

const state = {
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
};
let startupReady = false;

const ros = new ROS({
  host:        _cfg.routerHost,
  port:        _cfg.routerPort,
  tls:         _cfg.routerTls,
  tlsInsecure: _cfg.routerTlsInsecure,
  username:    _cfg.routerUser,
  password:    _cfg.routerPass,
  debug:       (process.env.ROS_DEBUG || 'false').toLowerCase() === 'true',
  writeTimeoutMs: parseInt(process.env.ROS_WRITE_TIMEOUT_MS || '30000', 10),
});

const DEFAULT_IF      = _cfg.defaultIf;
const HISTORY_MINUTES = _cfg.historyMinutes;

// Collectors — order matters: leases must exist before networks/connections
const dhcpLeases   = new DhcpLeasesCollector ({ros,io, state});
// ── Shared /ip/firewall/connection/print cache ───────────────────────────
const MAX_CONN_CACHE_AGE = 1500;
const connTableCache = {
  rows: null, ts: 0,
  async get(ros) {
    const now = Date.now();
    if (this.rows !== null && (now - this.ts) < MAX_CONN_CACHE_AGE) return this.rows;
    this.rows = (await ros.write('/ip/firewall/connection/print')) || [];
    this.ts   = Date.now();
    return this.rows;
  },
  invalidate() { this.rows = null; this.ts = 0; },
};

const arp          = new ArpCollector         ({ros,    pollMs:_cfg.pollArp,      state});
const dhcpNetworks = new DhcpNetworksCollector({ros,io, pollMs:_cfg.pollDhcp,     dhcpLeases, state, wanIface:DEFAULT_IF});
const traffic      = new TrafficCollector     ({ros,io, defaultIf:DEFAULT_IF, historyMinutes:HISTORY_MINUTES, pollMs:1000, state});
const conns        = new ConnectionsCollector ({ros,io, pollMs:_cfg.pollConns,    topN:_cfg.topN, maxConns:_cfg.maxConns, dhcpNetworks, dhcpLeases, arp, state, connTableCache});
const talkers      = new TopTalkersCollector  ({ros,io, pollMs:_cfg.pollConns,    state, topN:_cfg.topTalkersN});
const logs         = new LogsCollector        ({ros,io, state});
const system       = new SystemCollector      ({ros,io, pollMs:_cfg.pollSystem,   state});
const wireless     = new WirelessCollector    ({ros,io, pollMs:_cfg.pollWireless, state, dhcpLeases, arp});
const vpn          = new VpnCollector         ({ros,io, pollMs:_cfg.pollVpn,      state});
const firewall     = new FirewallCollector    ({ros,io, pollMs:_cfg.pollFirewall,  state, topN:_cfg.firewallTopN});
const ifStatus     = new InterfaceStatusCollector({ros,io, pollMs:_cfg.pollIfstatus, state});
const ping         = new PingCollector({ros,io, pollMs:_cfg.pollPing, state, target:_cfg.pingTarget});
const bandwidth    = new BandwidthCollector({ros,io, pollMs:_cfg.pollBandwidth, dhcpNetworks, dhcpLeases, arp, ifStatus, state, connTableCache});

// ── Settings API ─────────────────────────────────────────────────────────────
app.get('/api/settings', (_req, res) => {
  res.json(Settings.getPublic());
});

app.post('/api/settings', (req, res) => {
  try {
    const body = req.body || {};
    // Full reset to defaults
    if (body._reset) {
      const { DEFAULTS } = require('./settings');
      Settings.save(DEFAULTS);
      io.emit('settings:pages', {
        pageWireless:DEFAULTS.pageWireless, pageInterfaces:DEFAULTS.pageInterfaces,
        pageDhcp:DEFAULTS.pageDhcp, pageVpn:DEFAULTS.pageVpn,
        pageConnections:DEFAULTS.pageConnections, pageFirewall:DEFAULTS.pageFirewall,
        pageLogs:DEFAULTS.pageLogs,
      });
      return res.json({ ok:true, requiresRestart:false });
    }
    const updates = {};
    const intFields = {
      routerPort:[1,65535], pollConns:[500,60000], pollSystem:[500,60000],
      pollWireless:[500,60000], pollVpn:[1000,120000], pollFirewall:[1000,120000],
      pollIfstatus:[500,60000], pollPing:[1000,120000], pollArp:[5000,300000],
      pollBandwidth:[500,60000], pollDhcp:[5000,300000], topN:[1,50], topTalkersN:[1,20],
      firewallTopN:[1,50], maxConns:[1000,100000], historyMinutes:[5,120],
    };
    const strFields  = ['routerHost','routerUser','defaultIf','dashUser','pingTarget'];
    const boolFields = ['routerTls','routerTlsInsecure',
      'pageWireless','pageInterfaces','pageDhcp','pageVpn',
      'pageConnections','pageFirewall','pageLogs','pageBandwidth'];
    const credFields = ['routerPass','dashPass'];

    for (const [f, range] of Object.entries(intFields)) {
      if (f in body) { const v = parseInt(body[f],10); if (!isNaN(v) && v>=range[0] && v<=range[1]) updates[f]=v; }
    }
    for (const f of strFields)  { if (f in body) updates[f] = String(body[f]).trim().slice(0,256); }
    for (const f of boolFields) { if (f in body) updates[f] = body[f]===true||body[f]==='true'; }
    for (const f of credFields) { if (f in body && !Settings.isMasked(body[f])) updates[f] = String(body[f]).slice(0,512); }

    const saved = Settings.save(updates);

    // Apply poll changes live without restart
    const collectorMap = { conns, system, wireless, vpn, firewall, ifStatus, ping, arp, dhcpNetworks, bandwidth };
    const pollMap = { pollConns:'conns', pollSystem:'system', pollWireless:'wireless',
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
    if ('pingTarget' in updates) ping.target = saved.pingTarget;

    // Broadcast page visibility to all connected clients
    const pageSettings = {
      pageWireless:saved.pageWireless, pageInterfaces:saved.pageInterfaces,
      pageDhcp:saved.pageDhcp, pageVpn:saved.pageVpn,
      pageConnections:saved.pageConnections, pageFirewall:saved.pageFirewall,
      pageLogs:saved.pageLogs, pageBandwidth:saved.pageBandwidth,
    };
    io.emit('settings:pages', pageSettings);

    const routerChanged = ['routerHost','routerPort','routerTls','routerTlsInsecure','routerUser','routerPass']
      .some(f => f in updates);
    res.json({ ok:true, requiresRestart: routerChanged });
  } catch(e) {
    console.error('[settings] save error:', e);
    res.status(500).json({ ok:false, error: String(e.message||e) });
  }
});

app.get('/api/localcc', (_req, res) => {
  const wanIp = (state.lastWanIp || '').split('/')[0];
  let cc = '';
  if (geoip && wanIp) { const g = geoip.lookup(wanIp); if (g) cc = g.country || ''; }
  res.json({ cc, wanIp });
});

function sanitizeErr(e) {
  if (!e) return null;
  // Strip stack traces and truncate
  return String(e).split('\n')[0].slice(0, 200);
}

app.get('/healthz', (_req, res) => {
  const { ok, statusCode } = computeHealthStatus({
    startupReady,
    rosConnected: ros.connected,
  });
  const body = {
    ok,
    version: APP_VERSION,
    routerConnected: ros.connected,
    startupReady,
    uptime: process.uptime(),
    now: Date.now(),
    defaultIf: DEFAULT_IF,
    checks: {
      traffic:  { ts:state.lastTrafficTs,  err:sanitizeErr(state.lastTrafficErr)  },
      conns:    { ts:state.lastConnsTs,    err:sanitizeErr(state.lastConnsErr)    },
      leases:   { ts:state.lastLeasesTs,   err:null                               },
      arp:      { ts:state.lastArpTs,      err:null                               },
      talkers:  { ts:state.lastTalkersTs,  err:sanitizeErr(state.lastTalkersErr)  },
      logs:     { ts:state.lastLogsTs,     err:sanitizeErr(state.lastLogsErr)     },
      system:   { ts:state.lastSystemTs,   err:sanitizeErr(state.lastSystemErr)   },
      wireless: { ts:state.lastWirelessTs, err:sanitizeErr(state.lastWirelessErr) },
      vpn:      { ts:state.lastVpnTs,      err:sanitizeErr(state.lastVpnErr)      },
      firewall: { ts:state.lastFirewallTs, err:sanitizeErr(state.lastFirewallErr) },
      ping:     { ts:state.lastPingTs,     err:null                               },
    },
  };
  res.status(statusCode).json(body);
});

ros.connectLoop();

// ── RouterOS connection status broadcasting ─────────────────────────────────
// Track the last known ROS connection state so newly connected browser clients
// immediately receive it, rather than waiting for the next status change event.
let rosConnected = false;

function broadcastRosStatus(connected, reason) {
  rosConnected = connected;
  io.emit('ros:status', { connected, reason: reason || null });
}

ros.on('connected', () => broadcastRosStatus(true));
ros.on('close',     () => { connTableCache.invalidate(); broadcastRosStatus(false, 'RouterOS connection closed'); });
ros.on('error',     (e) => {
  const msg = e && e.message ? e.message : String(e);
  // Build a clear, human-readable reason from raw network errors
  let reason = msg;
  if (/ECONNREFUSED/.test(msg))  reason = `Connection refused — is RouterOS reachable at ${process.env.ROUTER_HOST}?`;
  else if (/ETIMEDOUT/.test(msg) || /timed out/i.test(msg)) reason = `Connection timed out — check ROUTER_HOST and firewall rules`;
  else if (/ENOTFOUND/.test(msg) || /ENOENT/.test(msg))    reason = `Host not found — check ROUTER_HOST setting (${process.env.ROUTER_HOST})`;
  else if (/ECONNRESET/.test(msg)) reason = 'Connection reset by router';
  else if (/certificate/i.test(msg)) reason = 'TLS certificate error — try setting ROUTER_TLS_INSECURE=true';
  else if (/authentication/i.test(msg) || /login/i.test(msg)) reason = 'Authentication failed — check ROUTER_USER and ROUTER_PASS';
  broadcastRosStatus(false, reason);
});

// Wait for the first RouterOS connection, then start all collectors.
// Uses ros 'connected' event so this works regardless of how long the
// router takes to come online — the container stays up and the HTTP/WS
// server is already serving the "offline" banner to any waiting clients.
let _collectorsStarted = false;
async function startCollectors() {
  if (_collectorsStarted) return;
  _collectorsStarted = true;
  try {
    console.log(`[MikroDash] v${APP_VERSION} — RouterOS connected, starting collectors`);
    wireless.start();
    await dhcpLeases.start();   // async: loads initial state first
    dhcpNetworks.start();
    arp.start();
    traffic.start();
    conns.start();
    talkers.start();
    logs.start();
    system.start();
    vpn.start();
    firewall.start();
    ifStatus.start();
    ping.start();
    bandwidth.start();

    startupReady = true;
    console.log('[MikroDash] All collectors running');
  } catch (e) {
    startupReady = false;
    _collectorsStarted = false; // allow retry on next reconnect
    console.error('[MikroDash] Collector startup error:', e && e.message ? e.message : e);
  }
}

ros.on('connected', () => startCollectors());

async function sendInitialState(socket) {
  // Send traffic:history FIRST — before any async awaits — so the client
  // has currentIf set before traffic:update events start arriving.
  socket.emit('traffic:history', {
    ifName: DEFAULT_IF,
    windowMinutes: HISTORY_MINUTES,
    points: traffic.hist.get(DEFAULT_IF) ? traffic.hist.get(DEFAULT_IF).toArray() : [],
  });

  // Tell the client the current RouterOS connection state immediately.
  // If ROS is already connected this resolves instantly; if not, we wait
  // briefly then send a ros:status so the client shows the offline banner
  // rather than silently showing empty/stale cards.
  if (!ros.connected) {
    socket.emit('ros:status', { connected: false, reason: rosConnected === false
      ? 'RouterOS is not connected — retrying in background'
      : 'Waiting for RouterOS connection…' });
    try { await ros.waitUntilConnected(10000); } catch (_) {
      // Still not connected after 10s — client already has the banner, continue
      // so the socket gets its (empty) initial state and stays live for when
      // ROS eventually reconnects.
    }
  }

  // Fetch interface list. On failure: log the reason, notify the client so
  // it can show an explicit error state rather than a silently empty dropdown,
  // and leave availableIfs unpopulated so traffic:select events are rejected
  // (rather than bypassing the whitelist) until the next successful fetch.
  let ifs = [];
  try {
    ifs = await fetchInterfaces(ros);
    traffic.setAvailableInterfaces(ifs);
  } catch (e) {
    const reason = e && e.message ? e.message : String(e);
    console.error('[MikroDash] fetchInterfaces failed for socket', socket.id, ':', reason);
    socket.emit('interfaces:error', { reason });
  }
  socket.emit('interfaces:list', { defaultIf: DEFAULT_IF, interfaces: ifs });

  socket.emit('lan:overview', {
    ts: Date.now(),
    lanCidrs: dhcpNetworks.getLanCidrs(),
    networks: dhcpNetworks.networks || [],
    pollMs: dhcpNetworks.pollMs,
  });

  // Send current lease table to newly connected client
  const allLeases = [];
  for (const [ip, v] of dhcpLeases.byIP.entries()) {
    allLeases.push({ ip, ...v });
  }
  socket.emit('leases:list', { ts: Date.now(), leases: allLeases });

  // Push last wireless snapshot immediately so client doesn't wait for next poll
  // Replay last known payload for each collector so new clients don't wait
  // for the next poll cycle before seeing data
  if (wireless.lastPayload)  socket.emit('wireless:update',  wireless.lastPayload);
  if (vpn.lastPayload)       socket.emit('vpn:update',       vpn.lastPayload);
  if (system.lastPayload)    socket.emit('system:update',    system.lastPayload);
  if (ifStatus.lastPayload)  socket.emit('ifstatus:update',  ifStatus.lastPayload);
  if (firewall.lastPayload)  socket.emit('firewall:update',  firewall.lastPayload);
  if (conns.lastPayload)     socket.emit('conn:update',      conns.lastPayload);
  if (talkers.lastPayload)   socket.emit('talkers:update',   talkers.lastPayload);
  if (ping.lastPayload)      socket.emit('ping:update',      ping.lastPayload);
  if (bandwidth.lastPayload) socket.emit('bandwidth:update', bandwidth.lastPayload);

  // Send page visibility settings to newly connected client
  const _ps = Settings.load();
  socket.emit('settings:pages', {
    pageWireless:_ps.pageWireless, pageInterfaces:_ps.pageInterfaces,
    pageDhcp:_ps.pageDhcp, pageVpn:_ps.pageVpn,
    pageConnections:_ps.pageConnections, pageFirewall:_ps.pageFirewall,
    pageLogs:_ps.pageLogs,
  });

  // Send ping history so client can render the chart immediately
  const pingData = ping.getHistory();
  if (pingData.history.length) socket.emit('ping:history', pingData);

  // Replay buffered log history so the logs page survives page refreshes.
  const logHistory = logs.getHistory();
  if (logHistory.length) socket.emit('logs:history', logHistory);
}

// Post-accept check: brief spikes above MAX_SOCKETS are possible but acceptable for a dashboard workload.
io.on('connection', (socket) => {
  if (io.engine.clientsCount > MAX_SOCKETS) {
    console.warn('[MikroDash] connection rejected — max sockets reached:', MAX_SOCKETS);
    socket.disconnect(true);
    return;
  }
  traffic.bindSocket(socket);
  sendInitialState(socket).catch(() => {});
});

const PORT = parseInt(process.env.PORT || '3081', 10);
server.listen(PORT, () => console.log(`[MikroDash] v${APP_VERSION} listening on http://0.0.0.0:${PORT}`));

// ── Graceful shutdown ──────────────────────────────────────────────────────
const allCollectors = [traffic, dhcpLeases, dhcpNetworks, arp, conns, talkers, logs, system, wireless, vpn, firewall, ifStatus, ping, bandwidth];

function shutdown(signal) {
  console.log(`[MikroDash] ${signal} received, shutting down…`);
  startupReady = false;
  for (const c of allCollectors) {
    if (c.timer) { clearInterval(c.timer); c.timer = null; }
  }
  ros.stop();
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
