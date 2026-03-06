require('dotenv').config();

const express = require('express');
const http    = require('http');
const { Server } = require('socket.io');

const ROS                  = require('./routeros/client');
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
const RuckusCollector       = require('./collectors/ruckus');

const app = express();
app.use(express.static('public'));
const server = http.createServer(app);
const io = new Server(server);

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
  lastRuckusTs:0, lastRuckusErr:null,
};

const ros = new ROS({
  host:        process.env.ROUTER_HOST,
  port:        parseInt(process.env.ROUTER_PORT || '8729', 10),
  tls:         (process.env.ROUTER_TLS          || 'true') .toLowerCase() === 'true',
  tlsInsecure: (process.env.ROUTER_TLS_INSECURE || 'false').toLowerCase() === 'true',
  username:    process.env.ROUTER_USER,
  password:    process.env.ROUTER_PASS,
  debug:       (process.env.ROS_DEBUG           || 'false').toLowerCase() === 'true',
});

const DEFAULT_IF      = process.env.DEFAULT_IF       || 'WAN1';
const HISTORY_MINUTES = parseInt(process.env.HISTORY_MINUTES || '30', 10);

// Collectors — order matters: leases must exist before networks/connections
const dhcpLeases   = new DhcpLeasesCollector ({ros,io, pollMs:parseInt(process.env.LEASES_POLL_MS   ||'15000',10), state});
const arp          = new ArpCollector         ({ros,    pollMs:parseInt(process.env.ARP_POLL_MS      ||'30000',10), state});
const dhcpNetworks = new DhcpNetworksCollector({ros,io, pollMs:parseInt(process.env.DHCP_POLL_MS     ||'15000',10), dhcpLeases, state});
const traffic      = new TrafficCollector     ({ros,io, defaultIf:DEFAULT_IF, historyMinutes:HISTORY_MINUTES, pollMs:1000, state});
const conns        = new ConnectionsCollector ({ros,io, pollMs:parseInt(process.env.CONNS_POLL_MS    ||'3000',10),  topN:parseInt(process.env.TOP_N||'10',10), dhcpNetworks, dhcpLeases, arp, state});
const talkers      = new TopTalkersCollector  ({ros,io, pollMs:parseInt(process.env.KIDS_POLL_MS     ||'3000',10),  state, topN:parseInt(process.env.TOP_TALKERS_N||'5',10)});
const logs         = new LogsCollector        ({ros,io, state});
const system       = new SystemCollector      ({ros,io, pollMs:parseInt(process.env.SYSTEM_POLL_MS   ||'3000',10),  state});
const ruckus       = new RuckusCollector      ({io, pollMs:parseInt(process.env.RUCKUS_POLL_MS   ||'10000',10), state});
const wireless     = new WirelessCollector    ({ros,io, pollMs:parseInt(process.env.WIRELESS_POLL_MS ||'5000',10),  state, dhcpLeases, arp, ruckus});
const vpn          = new VpnCollector         ({ros,io, pollMs:parseInt(process.env.VPN_POLL_MS      ||'10000',10), state});
const firewall     = new FirewallCollector    ({ros,io, pollMs:parseInt(process.env.FIREWALL_POLL_MS ||'10000',10), state, topN:parseInt(process.env.FIREWALL_TOP_N||'15',10)});
const ifStatus     = new InterfaceStatusCollector({ros,io, pollMs:parseInt(process.env.IFSTATUS_POLL_MS||'5000',10), state});
const ping         = new PingCollector({ros,io, pollMs:parseInt(process.env.PING_POLL_MS||'10000',10), state, target:process.env.PING_TARGET||'1.1.1.1'});

app.get('/api/localcc', (_req, res) => {
  let geoip = null;
  try { geoip = require('geoip-lite'); } catch(e) {}
  const wanIp = (state.lastWanIp || '').split('/')[0];
  let cc = '';
  if (geoip && wanIp) { const g = geoip.lookup(wanIp); if (g) cc = g.country || ''; }
  res.json({ cc, wanIp });
});

app.get('/healthz', (_req, res) => {
  res.json({
    ok: true,
    version: '0.3.0',
    routerConnected: ros.connected,
    uptime: process.uptime(),
    now: Date.now(),
    defaultIf: DEFAULT_IF,
    checks: {
      traffic:  { ts:state.lastTrafficTs,  err:state.lastTrafficErr  },
      conns:    { ts:state.lastConnsTs,    err:state.lastConnsErr    },
      leases:   { ts:state.lastLeasesTs,   err:null                  },
      arp:      { ts:state.lastArpTs,      err:null                  },
      talkers:  { ts:state.lastTalkersTs,  err:state.lastTalkersErr  },
      logs:     { ts:state.lastLogsTs,     err:state.lastLogsErr     },
      system:   { ts:state.lastSystemTs,   err:state.lastSystemErr   },
      wireless: { ts:state.lastWirelessTs, err:state.lastWirelessErr },
      vpn:      { ts:state.lastVpnTs,      err:state.lastVpnErr      },
      firewall: { ts:state.lastFirewallTs, err:state.lastFirewallErr },
      ping:     { ts:state.lastPingTs,     err:null                  },
      ruckus:   { ts:state.lastRuckusTs,  err:state.lastRuckusErr   },
    },
  });
});

ros.connectLoop();

(async () => {
  try {
    await ros.waitUntilConnected(60000);
    console.log('[MikroDash] v0.3.2 — RouterOS connected, starting collectors');

    // Streams (traffic, logs, leases) start themselves and register
    // reconnect handlers internally. Polling collectors do the same.
    // No staggering needed — node-routeros handles concurrent commands.
    // Start wireless immediately in parallel — don't wait for dhcpLeases
    // Names won't resolve on the very first poll but arrive on the second
    ruckus.start();
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

    console.log('[MikroDash] All collectors running');
  } catch (e) {
    console.error('[MikroDash] Startup error:', e && e.message ? e.message : e);
  }
})();

async function sendInitialState(socket) {
  // Send traffic:history FIRST — before any async awaits — so the client
  // has currentIf set before traffic:update events start arriving.
  socket.emit('traffic:history', {
    ifName: DEFAULT_IF,
    windowMinutes: HISTORY_MINUTES,
    points: traffic.hist.get(DEFAULT_IF) ? traffic.hist.get(DEFAULT_IF).toArray() : [],
  });

  try { await ros.waitUntilConnected(10000); } catch (_) {}

  // These can fire in parallel — node-routeros is fully concurrent
  const [ifaceResult] = await Promise.allSettled([
    fetchInterfaces(ros),
  ]);

  const ifs = ifaceResult.status === 'fulfilled' ? ifaceResult.value : [];
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
}

io.on('connection', (socket) => {
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
server.listen(PORT, () => console.log(`[MikroDash] v0.4.8 listening on http://0.0.0.0:${PORT}`));
