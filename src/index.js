require('dotenv').config();

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
  host:        process.env.ROUTER_HOST,
  port:        parseInt(process.env.ROUTER_PORT || '8729', 10),
  tls:         (process.env.ROUTER_TLS          || 'true') .toLowerCase() === 'true',
  tlsInsecure: (process.env.ROUTER_TLS_INSECURE || 'false').toLowerCase() === 'true',
  username:    process.env.ROUTER_USER,
  password:    process.env.ROUTER_PASS,
  debug:       (process.env.ROS_DEBUG           || 'false').toLowerCase() === 'true',
  writeTimeoutMs: parseInt(process.env.ROS_WRITE_TIMEOUT_MS || '30000', 10),
});

const DEFAULT_IF      = process.env.DEFAULT_IF       || 'WAN1';
const HISTORY_MINUTES = parseInt(process.env.HISTORY_MINUTES || '30', 10);

// Collectors — order matters: leases must exist before networks/connections
const dhcpLeases   = new DhcpLeasesCollector ({ros,io, pollMs:parseInt(process.env.LEASES_POLL_MS   ||'15000',10), state});
const arp          = new ArpCollector         ({ros,    pollMs:parseInt(process.env.ARP_POLL_MS      ||'30000',10), state});
const dhcpNetworks = new DhcpNetworksCollector({ros,io, pollMs:parseInt(process.env.DHCP_POLL_MS     ||'15000',10), dhcpLeases, state, wanIface:DEFAULT_IF});
const traffic      = new TrafficCollector     ({ros,io, defaultIf:DEFAULT_IF, historyMinutes:HISTORY_MINUTES, pollMs:1000, state});
const conns        = new ConnectionsCollector ({ros,io, pollMs:parseInt(process.env.CONNS_POLL_MS    ||'3000',10),  topN:parseInt(process.env.TOP_N||'10',10), maxConns:parseInt(process.env.MAX_CONNS||'20000',10), dhcpNetworks, dhcpLeases, arp, state});
const talkers      = new TopTalkersCollector  ({ros,io, pollMs:parseInt(process.env.KIDS_POLL_MS     ||'3000',10),  state, topN:parseInt(process.env.TOP_TALKERS_N||'5',10)});
const logs         = new LogsCollector        ({ros,io, state});
const system       = new SystemCollector      ({ros,io, pollMs:parseInt(process.env.SYSTEM_POLL_MS   ||'3000',10),  state});
const wireless     = new WirelessCollector    ({ros,io, pollMs:parseInt(process.env.WIRELESS_POLL_MS ||'5000',10),  state, dhcpLeases, arp});
const vpn          = new VpnCollector         ({ros,io, pollMs:parseInt(process.env.VPN_POLL_MS      ||'10000',10), state});
const firewall     = new FirewallCollector    ({ros,io, pollMs:parseInt(process.env.FIREWALL_POLL_MS ||'10000',10), state, topN:parseInt(process.env.FIREWALL_TOP_N||'15',10)});
const ifStatus     = new InterfaceStatusCollector({ros,io, pollMs:parseInt(process.env.IFSTATUS_POLL_MS||'5000',10), state});
const ping         = new PingCollector({ros,io, pollMs:parseInt(process.env.PING_POLL_MS||'10000',10), state, target:process.env.PING_TARGET||'1.1.1.1'});

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
ros.on('close',     () => broadcastRosStatus(false, 'RouterOS connection closed'));
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
  });

  // Send current lease table to newly connected client
  const allLeases = [];
  for (const [ip, v] of dhcpLeases.byIP.entries()) {
    allLeases.push({ ip, ...v });
  }
  socket.emit('leases:list', { ts: Date.now(), leases: allLeases });

  // Push last wireless snapshot immediately so client doesn't wait for next poll
  if (wireless.lastPayload) socket.emit('wireless:update', wireless.lastPayload);

  // Send ping history so client can render the chart immediately
  const pingData = ping.getHistory();
  if (pingData.history.length) socket.emit('ping:history', pingData);
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

// Broadcast full lease table every 15s so DHCP page stays current
setInterval(() => {
  const allLeases = [];
  for (const [ip, v] of dhcpLeases.byIP.entries()) allLeases.push({ ip, ...v });
  io.emit('leases:list', { ts: Date.now(), leases: allLeases });
}, 15000);

const PORT = parseInt(process.env.PORT || '3081', 10);
server.listen(PORT, () => console.log(`[MikroDash] v${APP_VERSION} listening on http://0.0.0.0:${PORT}`));

// ── Graceful shutdown ──────────────────────────────────────────────────────
const allCollectors = [traffic, dhcpLeases, dhcpNetworks, arp, conns, talkers, logs, system, wireless, vpn, firewall, ifStatus, ping];

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
