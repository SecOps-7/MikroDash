'use strict';
/**
 * WAN collector — the uplinks RouterOS considers connected to the internet.
 *
 *   /interface/detect-internet/state   which interfaces reach the internet
 *   /ip/dhcp-client                    lease detail for the ones that have one
 *   /ip/route  (dst 0.0.0.0/0)         which uplink is actually carrying traffic
 *   /ip/address                        the address each one holds
 *   /interface (name,type,running)     physical link or tunnel
 *
 * THE SET IS RouterOS's, NOT OURS. A WAN here is an interface reporting
 * `state=internet`, which is exactly what the Dashboard Network card shows
 * (src/collectors/dhcpNetworks.js). This page adds detail to that set; it does
 * not redefine it. In particular it does NOT infer uplinks from default routes
 * — a deliberate decision, because a page that disagreed with the card about
 * what counts as a WAN would be worse than one that shows nothing.
 *
 * WHICH MEANS IT SHOWS NOTHING WHEN DETECTION IS OFF, and that is the common
 * case: `detect-interface-list` defaults to `none`, so a router nobody has
 * configured reports zero rows. `detectionEnabled` carries that distinction to
 * the page, which explains how to switch it on rather than rendering an empty
 * table that looks like a fault.
 *
 * RATES ARE BORROWED FROM ifStatus, NOT FETCHED — the vlans.js discipline,
 * including its security half: fields are projected BY NAME rather than spread,
 * because that payload carries MAC addresses, per-interface IP lists and error
 * counters, and `wan` is a different permission from `interfaces`. `requires` is
 * empty so switching Interface Rates off degrades the rate column rather than
 * blanking the page.
 *
 * THIS COLLECTOR ONLY READS. Renew and release live in the socket actions in
 * index.js, gated on the page and on router:write. A test asserts those command
 * paths never appear here.
 */

const { clampPoll, createPollLoop, createListenRefresh } = require('./util');
// The app's existing containment answer — v4 and v6, already used by
// connections.js and bandwidth.js.
const { isInCidrs } = require('../util/ip');

const DETECT_CMD = ['/interface/detect-internet/state/print', '=.proplist=.id,name,state,state-change-time'];
const DHCPC_CMD  = ['/ip/dhcp-client/print',
                    '=.proplist=.id,interface,status,address,gateway,primary-dns,secondary-dns,expires-after,dhcp-server,disabled,invalid'];
const ROUTE_CMD  = ['/ip/route/print', '=.proplist=.id,dst-address,gateway,distance,active,dynamic'];
const ADDR_CMD   = ['/ip/address/print', '=.proplist=address,interface,disabled'];
const IFACE_CMD  = ['/interface/print', '=.proplist=name,type,running'];

// Structure changes when somebody edits the router; only the lease countdown and
// the rates move on a tick, and the rates are borrowed. A slow config cadence
// keeps this cheap on hardware that dislikes concurrent reads.
const CONFIG_EVERY = 6;

const _bool = (v) => v === true || v === 'true' || v === 'yes';

/** RouterOS reports tunnels as their own interface types. */
const TUNNEL_TYPES = ['wg', 'wireguard', 'ipip', 'gre', 'eoip', 'l2tp-out', 'pptp-out', 'sstp-out',
                      'ovpn-out', 'pppoe-out', '6to4', 'ipsec'];

/**
 * Is this address routable on the public internet?
 *
 * Only IPv4 is judged. The point is to tell an operator "this is your real
 * public address" apart from a CGNAT or tunnel address that looks like one, so
 * a wrong answer is worse than no answer — anything unrecognised returns null
 * rather than guessing.
 */
function isPublicV4(cidr) {
  const ip = String(cidr || '').split('/')[0].trim();
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(ip);
  if (!m) return null;
  const [a, b] = [Number(m[1]), Number(m[2])];
  if (a === 10 || a === 127 || a === 0) return false;
  if (a === 192 && b === 168) return false;
  if (a === 172 && b >= 16 && b <= 31) return false;
  if (a === 169 && b === 254) return false;          // link-local
  if (a === 100 && b >= 64 && b <= 127) return false; // CGNAT — looks public, is not
  if (a >= 224) return false;                         // multicast and above
  return true;
}

/**
 * Build the WAN rows.
 *
 * Pure and exported so every join here is testable without a router, the way
 * buildVlanRows and buildQueueRows are.
 */
function buildWanRows(detectRows, dhcpRows, routeRows, addrRows, ifaceRows, ifPayload) {
  // Borrowed rates, guarded: ifStatus is DISABLEABLE and may be a null stub.
  const rates = new Map();
  const ifaces = (ifPayload && Array.isArray(ifPayload.interfaces)) ? ifPayload.interfaces : null;
  if (ifaces) for (const i of ifaces) if (i && i.name) rates.set(i.name, i);

  const meta = new Map();
  for (const i of ifaceRows || []) if (i && i.name) meta.set(i.name, i);

  const addrByIface = new Map();
  for (const a of addrRows || []) {
    if (!a || !a.address || !a.interface || _bool(a.disabled)) continue;
    if (!addrByIface.has(a.interface)) addrByIface.set(a.interface, a.address);
  }

  const dhcpByIface = new Map();
  for (const d of dhcpRows || []) {
    if (!d || !d.interface || _bool(d.disabled)) continue;
    dhcpByIface.set(d.interface, d);
  }

  // Which default route belongs to which uplink.
  //
  // A route's gateway is the NEXT HOP, never our own address — matching them
  // against each other finds nothing, which is how the first version of this
  // silently reported every uplink as standby. Three shapes, in order:
  //
  //   tunnel      the route points at the interface by name  (gateway=WG-SA)
  //   dhcp uplink the route points at the lease's gateway    (gateway=37.120.64.1)
  //   static      the route points at some address inside our own subnet
  const defaults = (routeRows || []).filter(r => r && r['dst-address'] === '0.0.0.0/0');
  const routeFor = (name, address, dhcpGw) => {
    const byName = defaults.find(r => r.gateway === name);
    if (byName) return byName;
    if (dhcpGw) {
      const byLease = defaults.find(r => r.gateway === dhcpGw);
      if (byLease) return byLease;
    }
    if (address) {
      const bySubnet = defaults.find(r => r.gateway && isInCidrs(r.gateway, [address]));
      if (bySubnet) return bySubnet;
    }
    return null;
  };

  const wans = [];
  for (const d of detectRows || []) {
    if (!d || !d.name || d.state !== 'internet') continue;
    const name = String(d.name);
    const m    = meta.get(name) || {};
    const type = m.type || '';
    const address = addrByIface.get(name) || '';
    const dhcp = dhcpByIface.get(name) || null;
    // A tunnel's own /32 is not the uplink's gateway; the DHCP client's is
    // authoritative when there is one, and the default route's otherwise.
    const route = routeFor(name, address, dhcp && dhcp.gateway);
    const live  = rates.get(name);

    wans.push({
      name,
      type,
      isTunnel: TUNNEL_TYPES.indexOf(type) !== -1,
      state:    d.state,
      // The router's own words. Rendered as a duration by the page, which knows
      // the display timezone; converting here would bake in the server's.
      since:    d['state-change-time'] || '',
      running:  m.running === undefined ? null : _bool(m.running),
      address,
      isPublic: address ? isPublicV4(address) : null,
      gateway:  (dhcp && dhcp.gateway) || (route && route.gateway) || '',
      routeDistance: route ? (route.distance || '') : '',
      // Only one route per distance is active; this is what tells an operator
      // which uplink is actually carrying traffic rather than merely standing by.
      routeActive:   route ? _bool(route.active) : false,
      hasDefaultRoute: !!route,
      // null, never 0: "the router did not report this" and "this uplink is
      // idle" must stay tellable apart, or the page shows a confident 0 Mbps on
      // a saturated link during the startup window.
      rxMbps:  live && typeof live.rxMbps === 'number' ? live.rxMbps : null,
      txMbps:  live && typeof live.txMbps === 'number' ? live.txMbps : null,
      rxBytes: live && typeof live.rxBytes === 'number' ? live.rxBytes : null,
      txBytes: live && typeof live.txBytes === 'number' ? live.txBytes : null,
      dhcp: dhcp ? {
        id:           dhcp['.id'] || '',
        status:       dhcp.status || '',
        server:       dhcp['dhcp-server'] || '',
        primaryDns:   dhcp['primary-dns'] || '',
        secondaryDns: dhcp['secondary-dns'] || '',
        expiresAfter: dhcp['expires-after'] || '',
        invalid:      _bool(dhcp.invalid),
      } : null,
    });
  }

  // Active first, then by route distance, then by name — the order an operator
  // reads them in: what is carrying traffic now, then what would take over.
  wans.sort((a, b) => (b.routeActive - a.routeActive) ||
                      (Number(a.routeDistance || 99) - Number(b.routeDistance || 99)) ||
                      a.name.localeCompare(b.name));

  const active = wans.find(w => w.routeActive);
  return {
    wans,
    ratesAvailable: !!ifaces,
    activeDefaultWan: active ? active.name : '',
    // The one address worth showing at the top: a real public one if any uplink
    // holds it, rather than the first tunnel /32 that happens to sort first.
    publicIp: (wans.find(w => w.isPublic === true) || {}).address || '',
  };
}

class WanCollector {
  constructor({ ros, io, state, pollMs, streamMode, ifStatus }) {
    this.ros    = ros;
    this.io     = io;
    this.state  = state;
    this.pollMs = clampPoll(pollMs, 10000, 60000, 2000);
    this.streamMode = streamMode !== false;
    // DISABLEABLE, so this may be a null-collector stub with lastPayload null.
    // Guarded at every read.
    this.ifStatus = ifStatus || null;
    this._lbl = ros.routerLabel ? `[${ros.routerLabel}][wan]` : '[wan]';

    this._poll   = createPollLoop(() => this._tick(), () => this.pollMs);
    this._ticks  = 0;
    this._lastFp = '';
    this._listen = null;
    this._meta   = { ifaces: [], dhcp: [], addrs: [] };
    this._detectAvailable = undefined;
    this._denied = false;
    this.lastPayload = null;
  }

  async _read(cmd, flag) {
    if (flag && this[flag] === false) return [];
    try {
      const rows = await this.ros.write(cmd[0], cmd[1] ? [cmd[1]] : []);
      if (flag) this[flag] = true;
      return (rows || []).filter(r => r && Object.keys(r).length);
    } catch (e) {
      const msg = String((e && e.message) || e).toLowerCase();
      if (msg.includes('no such') || msg.includes('unknown command')) { if (flag) this[flag] = false; }
      else if (msg.includes('not enough permission') || msg.includes('permission denied') ||
               msg.includes('no permissions')) { if (flag) this[flag] = false; this._denied = true; }
      else this.state.lastWanErr = e && e.message ? e.message : String(e);
      return [];
    }
  }

  /** Re-read now, after an action, so the page shows what the router did. */
  async refreshNow() {
    this._ticks = 0;
    if (this.ros.connected) await this._tick();
  }

  async _tick() {
    if (!this.ros.connected) return;

    if (this._ticks % CONFIG_EVERY === 0) {
      const [ifaces, dhcp, addrs] = await Promise.all([
        this._read(IFACE_CMD), this._read(DHCPC_CMD), this._read(ADDR_CMD),
      ]);
      this._meta = { ifaces, dhcp, addrs };
    }
    this._ticks++;

    const [detect, routes] = await Promise.all([
      this._read(DETECT_CMD, '_detectAvailable'),
      this._read(ROUTE_CMD),
    ]);

    const built = buildWanRows(detect, this._meta.dhcp, routes, this._meta.addrs,
                               this._meta.ifaces, this.ifStatus && this.ifStatus.lastPayload);
    const payload = {
      ts: Date.now(), pollMs: this.streamMode ? 0 : this.pollMs,
      ...built,
      // Zero rows means detection is switched off far more often than it means
      // the router is offline — detect-interface-list defaults to none. The page
      // says which rather than showing an empty table.
      detectionEnabled: detect.length > 0,
      available: this._detectAvailable !== false,
      denied: this._denied,
    };
    this.lastPayload = payload;
    this.state.lastWanTs = payload.ts;

    // Byte totals are excluded from the fingerprint: they move every tick on a
    // live uplink and would defeat the dirty check on their own. Rates are
    // included, rounded, because they are what changes visibly.
    const fp = JSON.stringify({
      w: built.wans.map(w => [w.name, w.address, w.gateway, w.routeActive, w.routeDistance,
                              w.running, Math.round((w.rxMbps || 0) * 10), Math.round((w.txMbps || 0) * 10),
                              w.dhcp && w.dhcp.status, w.dhcp && w.dhcp.expiresAfter]),
      d: payload.detectionEnabled, r: built.ratesAvailable,
    });
    if (fp === this._lastFp) return;
    this._lastFp = fp;
    this.io.to('page-wan').emit('wan:update', payload);
  }

  _startListen() {
    if (!this.streamMode || this._listen) return;
    // A default route appearing, vanishing or going inactive IS a failover, and
    // it is the one event on this page worth seeing immediately rather than on
    // the next tick. The channel carries no data — it marks the tables stale and
    // the ordinary tick reads them.
    this._listen = createListenRefresh({
      ros: this.ros, cmd: '/ip/route/listen', label: this._lbl,
      onEvent: () => { this._tick().catch(() => {}); },
    });
    this._listen.start();
  }

  _stopListen() {
    if (this._listen) this._listen.stop();
    this._listen = null;
  }

  async start() {
    // The tick runs on its own rather than waiting for stream data: a router
    // with detection switched off would never fire a route event either, and the
    // page would sit on "waiting" instead of explaining itself.
    if (this.ros.connected) await this._tick();
    this._startListen();
    this._poll.start();
    this.ros.on('close', () => { this._poll.stop(); this._stopListen(); });
    this.ros.on('connected', async () => {
      this._poll.stop();
      this._stopListen();
      this._lastFp = '';
      this._ticks  = 0;
      this._denied = false;
      this._detectAvailable = undefined;
      await this._tick();
      this._startListen();
      this._poll.start();
    });
  }

  suspend() { this._poll.stop(); this._stopListen(); }
  resume()  { if (this.ros.connected) { this._startListen(); this._poll.start(); } }

  stop() { this._poll.stop(); this._stopListen(); this._lastFp = ''; }
}

WanCollector.buildWanRows = buildWanRows;
WanCollector.isPublicV4   = isPublicV4;
module.exports = WanCollector;
