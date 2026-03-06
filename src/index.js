require('dotenv').config();

const express = require('express');
const http = require('http');
const { Server } = require('socket.io');

const ROS = require('./routeros/client');
const { fetchInterfaces } = require('./collectors/interfaces');
const TrafficCollector = require('./collectors/traffic');
const DhcpLeasesCollector = require('./collectors/dhcpLeases');
const DhcpNetworksCollector = require('./collectors/dhcpNetworks');
const ArpCollector = require('./collectors/arp');
const ConnectionsCollector = require('./collectors/connections');
const TopTalkersCollector = require('./collectors/talkers');
const LogsCollector = require('./collectors/logs');
const SystemCollector = require('./collectors/system');
const WirelessCollector = require('./collectors/wireless');
const VpnCollector = require('./collectors/vpn');
const FirewallCollector = require('./collectors/firewall');
const InterfaceStatusCollector = require('./collectors/interfaceStatus');
const PingCollector = require('./collectors/ping');

const app = express();
app.use(express.static('public'));
const server = http.createServer(app);
const io = new Server(server);

function buildDefaultState() {
    return {
        lastTrafficTs: 0, lastTrafficErr: null,
        lastConnsTs: 0, lastConnsErr: null,
        lastNetworksTs: 0,
        lastLeasesTs: 0,
        lastArpTs: 0,
        lastTalkersTs: 0, lastTalkersErr: null,
        lastLogsTs: 0, lastLogsErr: null,
        lastSystemTs: 0, lastSystemErr: null,
        lastWirelessTs: 0, lastWirelessErr: null,
        lastVpnTs: 0, lastVpnErr: null,
        lastFirewallTs: 0, lastFirewallErr: null,
        lastIfStatusTs: 0,
        lastPingTs: 0,
        lastWanIp: '',
    };
}

function parseBool(v, defaultValue) {
    if (v == null || v === '') return defaultValue;
    return String(v).toLowerCase() === 'true';
}

function parseDeviceConfigs() {
    const defaultDevice = {
        id: process.env.ROUTER_ID || 'default',
        name: process.env.ROUTER_NAME || process.env.ROUTER_HOST || 'Router',
        host: process.env.ROUTER_HOST,
        port: parseInt(process.env.ROUTER_PORT || '8729', 10),
        tls: parseBool(process.env.ROUTER_TLS, true),
        tlsInsecure: parseBool(process.env.ROUTER_TLS_INSECURE, false),
        username: process.env.ROUTER_USER,
        password: process.env.ROUTER_PASS,
        debug: parseBool(process.env.ROS_DEBUG, false),
        defaultIf: process.env.DEFAULT_IF || 'WAN1',
    };

    const raw = (process.env.ROUTERS_JSON || '').trim();
    if (!raw) return [defaultDevice];

    let parsed;
    try {
        parsed = JSON.parse(raw);
    } catch (e) {
        console.error('[MikroDash] Invalid ROUTERS_JSON:', e && e.message ? e.message : e);
        process.exit(1);
    }

    if (!Array.isArray(parsed) || !parsed.length) {
        console.error('[MikroDash] ROUTERS_JSON must be a non-empty JSON array');
        process.exit(1);
    }

    const ids = new Set();
    const devices = parsed.map((d, idx) => {
        const id = String(d.id || `router-${idx + 1}`);
        if (ids.has(id)) {
            console.error('[MikroDash] Duplicate router id in ROUTERS_JSON:', id);
            process.exit(1);
        }
        ids.add(id);
        return {
            id,
            name: d.name || d.host || id,
            host: d.host,
            port: parseInt(String(d.port || process.env.ROUTER_PORT || '8729'), 10),
            tls: d.tls == null ? parseBool(process.env.ROUTER_TLS, true) : parseBool(d.tls, true),
            tlsInsecure: d.tlsInsecure == null ? parseBool(process.env.ROUTER_TLS_INSECURE, false) : parseBool(d.tlsInsecure, false),
            username: d.username,
            password: d.password,
            debug: d.debug == null ? parseBool(process.env.ROS_DEBUG, false) : parseBool(d.debug, false),
            defaultIf: d.defaultIf || process.env.DEFAULT_IF || 'WAN1',
        };
    });

    const missing = devices.filter(d => !d.host || !d.username || d.password == null);
    if (missing.length) {
        console.error('[MikroDash] Each router in ROUTERS_JSON requires host, username, password');
        process.exit(1);
    }

    return devices;
}

class DeviceRuntime {
    constructor(cfg) {
        this.cfg = cfg;
        this.room = `device:${cfg.id}`;
        this.state = buildDefaultState();
        this.leasesBroadcastTimer = null;

        this.ros = new ROS({
            host: cfg.host,
            port: cfg.port,
            tls: cfg.tls,
            tlsInsecure: cfg.tlsInsecure,
            username: cfg.username,
            password: cfg.password,
            debug: cfg.debug,
        });

        // Route broadcast events only to sockets subscribed to this device room.
        this.scopedIo = {
            emit: (eventName, payload) => io.to(this.room).emit(eventName, payload),
            to: (target) => io.to(target),
        };

        this.dhcpLeases = new DhcpLeasesCollector({
            ros: this.ros,
            io: this.scopedIo,
            pollMs: parseInt(process.env.LEASES_POLL_MS || '15000', 10),
            state: this.state,
        });
        this.arp = new ArpCollector({
            ros: this.ros,
            pollMs: parseInt(process.env.ARP_POLL_MS || '30000', 10),
            state: this.state,
        });
        this.dhcpNetworks = new DhcpNetworksCollector({
            ros: this.ros,
            io: this.scopedIo,
            pollMs: parseInt(process.env.DHCP_POLL_MS || '15000', 10),
            dhcpLeases: this.dhcpLeases,
            state: this.state,
            wanIface: cfg.defaultIf,
        });
        this.traffic = new TrafficCollector({
            ros: this.ros,
            io: this.scopedIo,
            defaultIf: cfg.defaultIf,
            historyMinutes: parseInt(process.env.HISTORY_MINUTES || '30', 10),
            state: this.state,
        });
        this.conns = new ConnectionsCollector({
            ros: this.ros,
            io: this.scopedIo,
            pollMs: parseInt(process.env.CONNS_POLL_MS || '3000', 10),
            topN: parseInt(process.env.TOP_N || '10', 10),
            dhcpNetworks: this.dhcpNetworks,
            dhcpLeases: this.dhcpLeases,
            arp: this.arp,
            state: this.state,
        });
        this.talkers = new TopTalkersCollector({
            ros: this.ros,
            io: this.scopedIo,
            pollMs: parseInt(process.env.KIDS_POLL_MS || '3000', 10),
            state: this.state,
            topN: parseInt(process.env.TOP_TALKERS_N || '5', 10),
        });
        this.logs = new LogsCollector({ ros: this.ros, io: this.scopedIo, state: this.state });
        this.system = new SystemCollector({
            ros: this.ros,
            io: this.scopedIo,
            pollMs: parseInt(process.env.SYSTEM_POLL_MS || '3000', 10),
            state: this.state,
        });
        this.wireless = new WirelessCollector({
            ros: this.ros,
            io: this.scopedIo,
            pollMs: parseInt(process.env.WIRELESS_POLL_MS || '5000', 10),
            state: this.state,
            dhcpLeases: this.dhcpLeases,
            arp: this.arp,
        });
        this.vpn = new VpnCollector({
            ros: this.ros,
            io: this.scopedIo,
            pollMs: parseInt(process.env.VPN_POLL_MS || '10000', 10),
            state: this.state,
        });
        this.firewall = new FirewallCollector({
            ros: this.ros,
            io: this.scopedIo,
            pollMs: parseInt(process.env.FIREWALL_POLL_MS || '10000', 10),
            state: this.state,
            topN: parseInt(process.env.FIREWALL_TOP_N || '15', 10),
        });
        this.ifStatus = new InterfaceStatusCollector({
            ros: this.ros,
            io: this.scopedIo,
            pollMs: parseInt(process.env.IFSTATUS_POLL_MS || '5000', 10),
            state: this.state,
        });
        this.ping = new PingCollector({
            ros: this.ros,
            io: this.scopedIo,
            pollMs: parseInt(process.env.PING_POLL_MS || '10000', 10),
            state: this.state,
            target: process.env.PING_TARGET || '1.1.1.1',
        });
    }

    attachSocket(socketId) {
        this.traffic.attachSocket(socketId, this.cfg.defaultIf);
    }

    detachSocket(socketId) {
        this.traffic.detachSocket(socketId);
    }

    handleTrafficSelection(socket, ifName) {
        const history = this.traffic.selectInterface(socket.id, ifName);
        if (!history) return;
        socket.emit('traffic:history', { ifName, points: history });
    }

    async sendInitialState(socket) {
        const history = this.traffic.hist.get(this.cfg.defaultIf);
        socket.emit('traffic:history', {
            ifName: this.cfg.defaultIf,
            windowMinutes: parseInt(process.env.HISTORY_MINUTES || '30', 10),
            points: history ? history.toArray() : [],
        });

        try { await this.ros.waitUntilConnected(10000); } catch (_) { }

        const [ifaceResult] = await Promise.allSettled([
            fetchInterfaces(this.ros),
        ]);

        const ifs = ifaceResult.status === 'fulfilled' ? ifaceResult.value : [];
        socket.emit('interfaces:list', { defaultIf: this.cfg.defaultIf, interfaces: ifs });

        socket.emit('lan:overview', {
            ts: Date.now(),
            lanCidrs: this.dhcpNetworks.getLanCidrs(),
            networks: this.dhcpNetworks.networks || [],
            wanIp: this.state.lastWanIp || '',
        });

        const allLeases = [];
        for (const [ip, v] of this.dhcpLeases.byIP.entries()) {
            allLeases.push({ ip, ...v });
        }
        socket.emit('leases:list', { ts: Date.now(), leases: allLeases });

        if (this.wireless.lastPayload) socket.emit('wireless:update', this.wireless.lastPayload);
    }

    startLeasesBroadcast() {
        if (this.leasesBroadcastTimer) return;
        this.leasesBroadcastTimer = setInterval(() => {
            const allLeases = [];
            for (const [ip, v] of this.dhcpLeases.byIP.entries()) allLeases.push({ ip, ...v });
            io.to(this.room).emit('leases:list', { ts: Date.now(), leases: allLeases });
        }, 15000);
    }

    async startCollectorsWhenConnected() {
        try {
            await this.ros.waitUntilConnected(60000);
            console.log(`[MikroDash] ${this.cfg.id} (${this.cfg.host}) connected, starting collectors`);

            this.wireless.start();
            await this.dhcpLeases.start();
            this.dhcpNetworks.start();
            this.arp.start();
            this.traffic.start();
            this.conns.start();
            this.talkers.start();
            this.logs.start();
            this.system.start();
            this.vpn.start();
            this.firewall.start();
            this.ifStatus.start();
            this.ping.start();
            this.startLeasesBroadcast();

            console.log(`[MikroDash] ${this.cfg.id} collectors running`);
        } catch (e) {
            console.error(`[MikroDash] ${this.cfg.id} startup error:`, e && e.message ? e.message : e);
        }
    }

    run() {
        this.ros.connectLoop();
        this.startCollectorsWhenConnected();
    }

    health() {
        return {
            id: this.cfg.id,
            name: this.cfg.name,
            host: this.cfg.host,
            defaultIf: this.cfg.defaultIf,
            routerConnected: this.ros.connected,
            checks: {
                traffic: { ts: this.state.lastTrafficTs, err: this.state.lastTrafficErr },
                conns: { ts: this.state.lastConnsTs, err: this.state.lastConnsErr },
                leases: { ts: this.state.lastLeasesTs, err: null },
                arp: { ts: this.state.lastArpTs, err: null },
                talkers: { ts: this.state.lastTalkersTs, err: this.state.lastTalkersErr },
                logs: { ts: this.state.lastLogsTs, err: this.state.lastLogsErr },
                system: { ts: this.state.lastSystemTs, err: this.state.lastSystemErr },
                wireless: { ts: this.state.lastWirelessTs, err: this.state.lastWirelessErr },
                vpn: { ts: this.state.lastVpnTs, err: this.state.lastVpnErr },
                firewall: { ts: this.state.lastFirewallTs, err: this.state.lastFirewallErr },
                ping: { ts: this.state.lastPingTs, err: null },
            },
        };
    }
}

const deviceConfigs = parseDeviceConfigs();
const runtimes = deviceConfigs.map(cfg => new DeviceRuntime(cfg));
const runtimeById = new Map(runtimes.map(r => [r.cfg.id, r]));
const defaultRuntime = runtimes[0];
const socketDevice = new Map();

runtimes.forEach(r => r.run());

function getRuntimeBySocket(socketId) {
    const deviceId = socketDevice.get(socketId);
    return runtimeById.get(deviceId) || defaultRuntime;
}

async function selectDeviceForSocket(socket, requestedDeviceId) {
    const runtime = runtimeById.get(requestedDeviceId) || defaultRuntime;
    const previousDeviceId = socketDevice.get(socket.id);

    if (previousDeviceId === runtime.cfg.id) {
        return runtime;
    }

    if (previousDeviceId && runtimeById.has(previousDeviceId)) {
        const prev = runtimeById.get(previousDeviceId);
        prev.detachSocket(socket.id);
        socket.leave(prev.room);
    }

    socketDevice.set(socket.id, runtime.cfg.id);
    socket.join(runtime.room);
    runtime.attachSocket(socket.id);

    socket.emit('device:selected', {
        deviceId: runtime.cfg.id,
        name: runtime.cfg.name,
        host: runtime.cfg.host,
        defaultIf: runtime.cfg.defaultIf,
    });

    await runtime.sendInitialState(socket);
    return runtime;
}

app.get('/api/localcc', (req, res) => {
    let geoip = null;
    try { geoip = require('geoip-lite'); } catch (e) { }

    const runtime = runtimeById.get(String(req.query.deviceId || '')) || defaultRuntime;
    const wanIp = (runtime.state.lastWanIp || '').split('/')[0];
    let cc = '';
    if (geoip && wanIp) {
        const g = geoip.lookup(wanIp);
        if (g) cc = g.country || '';
    }
    res.json({ cc, wanIp, deviceId: runtime.cfg.id });
});

app.get('/healthz', (_req, res) => {
    res.json({
        ok: true,
        version: '0.6.0',
        uptime: process.uptime(),
        now: Date.now(),
        devices: runtimes.map(r => r.health()),
    });
});

io.on('connection', (socket) => {
    socket.emit('devices:list', {
        defaultDeviceId: defaultRuntime.cfg.id,
        devices: deviceConfigs.map(d => ({
            id: d.id,
            name: d.name,
            host: d.host,
            defaultIf: d.defaultIf,
        })),
    });

    selectDeviceForSocket(socket, defaultRuntime.cfg.id).catch(() => { });

    socket.on('device:select', ({ deviceId }) => {
        selectDeviceForSocket(socket, deviceId).catch(() => { });
    });

    socket.on('traffic:select', ({ ifName }) => {
        const runtime = getRuntimeBySocket(socket.id);
        runtime.handleTrafficSelection(socket, ifName);
    });

    socket.on('disconnect', () => {
        const runtime = getRuntimeBySocket(socket.id);
        runtime.detachSocket(socket.id);
        socketDevice.delete(socket.id);
    });
});

const PORT = parseInt(process.env.PORT || '3081', 10);
server.listen(PORT, () => {
    console.log(`[MikroDash] v0.6.0 listening on http://0.0.0.0:${PORT} (${runtimes.length} device${runtimes.length === 1 ? '' : 's'})`);
});
