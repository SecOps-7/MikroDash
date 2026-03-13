/**
 * Bandwidth collector — derives per-connection RX/TX rates from
 * /ip/firewall/connection/print byte counter deltas between ticks,
 * then aggregates to per-source-IP rows with geo, org, hostname, MAC.
 *
 * Emits: bandwidth:update
 */
'use strict';

const { extractAddress, isInCidrs, isValidIp } = require('../util/ip');
const { lookupOrg, lookupCategory }             = require('../util/asnLookup');

let geoip = null;
try { geoip = require('geoip-lite'); } catch (_) {}

const bpsToMbps = (bytes, dtMs) =>
  dtMs > 0 ? +((bytes * 8) / (dtMs / 1000) / 1_000_000).toFixed(4) : 0;

class BandwidthCollector {
  constructor({ ros, io, pollMs, dhcpNetworks, dhcpLeases, arp, ifStatus, state, geoLookup, connTableCache }) {
    this.ros          = ros;
    this.io           = io;
    this.pollMs       = pollMs || 3000;
    this.dhcpNetworks = dhcpNetworks;
    this.dhcpLeases   = dhcpLeases;
    this.arp          = arp;
    this.ifStatus     = ifStatus;
    this.state        = state;
    this.geoLookup    = geoLookup || (geoip ? ip => geoip.lookup(ip) : null);
    // prev: connId -> { origBytes, replBytes, ts, src, dst, proto, iface }
    this._prev        = new Map();
    this._ifaceCache  = new Map(); // srcIp -> iface name
    this.connTableCache = connTableCache || null;
    this._geoCache    = new Map(); // ip -> { country, city }
    this._orgCache    = new Map(); // ip -> org string | null
    this.timer        = null;
    this._inflight    = false;
    this.lastPayload  = null;
  }

  _resolveName(ip) {
    const lease = this.dhcpLeases ? this.dhcpLeases.getNameByIP(ip) : null;
    if (lease && lease.name) return { name: lease.name, mac: lease.mac || '' };
    const a = this.arp ? this.arp.getByIP(ip) : null;
    if (a && a.mac) {
      const lm = this.dhcpLeases ? this.dhcpLeases.getNameByMAC(a.mac) : null;
      return { name: (lm && lm.name) ? lm.name : '', mac: a.mac };
    }
    return { name: '', mac: '' };
  }

  _geo(ip) {
    if (this._geoCache.has(ip)) return this._geoCache.get(ip);
    const g = this.geoLookup && isValidIp(ip) ? this.geoLookup(ip) : null;
    const result = g && g.country ? { country: g.country, city: g.city || '' } : { country: '', city: '' };
    this._geoCache.set(ip, result);
    return result;
  }

  _org(ip) {
    if (this._orgCache.has(ip)) return this._orgCache.get(ip);
    const org = isValidIp(ip) ? (lookupOrg(ip) || null) : null;
    this._orgCache.set(ip, org);
    return org;
  }

  // _ifaceCache: srcIp -> iface string. Cleared on reconnect same as geo/org caches.
  // Avoids iterating all interface CIDRs for every connection every tick.

  _resolveIface(ip) {
    if (this._ifaceCache && this._ifaceCache.has(ip)) return this._ifaceCache.get(ip);
    if (!this.ifStatus || !this.ifStatus.lastPayload) return '';
    const ifaces = this.ifStatus.lastPayload.interfaces || [];
    for (const iface of ifaces) {
      if (!iface.running || iface.disabled) continue;
      for (const cidr of (iface.ips || [])) {
        try {
          if (isInCidrs(ip, [cidr])) {
            if (this._ifaceCache) this._ifaceCache.set(ip, iface.name);
            return iface.name;
          }
        } catch (_) {}
      }
    }
    if (this._ifaceCache) this._ifaceCache.set(ip, '');
    return '';
  }

  async tick() {
    if (!this.ros.connected) return;

    const now      = Date.now();
    const lanCidrs = this.dhcpNetworks ? this.dhcpNetworks.getLanCidrs() : [];

    const rows = this.connTableCache
      ? await this.connTableCache.get(this.ros)
      : ((await this.ros.write('/ip/firewall/connection/print')) || []);

    // Per-source-IP aggregation map
    // srcIp -> { rxMbps, txMbps, dsts: Map<dstKey, {rxMbps,txMbps,proto,iface,dstIp}> }
    const srcMap = new Map();

    const seenIds = new Set();

    for (const c of rows) {
      const id        = c['.id'];
      if (!id) continue;
      seenIds.add(id);

      const src       = extractAddress(c['src-address'] || c.src || '');
      const dst       = extractAddress(c['dst-address'] || c.dst || '');
      const proto     = (c.protocol || c['ip-protocol'] || '').toLowerCase();
      // Resolve interface by matching src IP against each interface's assigned subnets.
      // This works for all interface types (bridge, VLAN, physical) and is more
      // reliable than the ARP table which only returns the bridge for most LAN hosts.
      const iface     = this._resolveIface(src);
      const origBytes = parseInt(c['orig-bytes'] || '0', 10) || 0;
      const replBytes = parseInt(c['repl-bytes'] || '0', 10) || 0;

      if (!src || !dst) continue;

      // Calculate rates from byte deltas
      let rxMbps = 0, txMbps = 0;
      const prev = this._prev.get(id);
      if (prev && now > prev.ts) {
        const dt = now - prev.ts;
        // orig = src→dst (TX from src perspective), repl = dst→src (RX from src perspective)
        const origDelta = origBytes - prev.origBytes;
        const replDelta = replBytes - prev.replBytes;
        if (origDelta >= 0 && replDelta >= 0) {
          txMbps = bpsToMbps(origDelta, dt);
          rxMbps = bpsToMbps(replDelta, dt);
        }
      }
      this._prev.set(id, { origBytes, replBytes, ts: now, src, dst, proto, iface });

      // Only include LAN sources. If lanCidrs isn't populated yet, fall back to
      // RFC-1918 ranges so the page isn't blank on first load.
      const RFC1918 = ['10.0.0.0/8','172.16.0.0/12','192.168.0.0/16'];
      const activeCidrs = (lanCidrs && lanCidrs.length) ? lanCidrs : RFC1918;
      if (!src || !isInCidrs(src, activeCidrs)) continue;

      if (!srcMap.has(src)) {
        srcMap.set(src, { rxMbps: 0, txMbps: 0, dsts: new Map() });
      }
      const srcEntry = srcMap.get(src);
      srcEntry.rxMbps += rxMbps;
      srcEntry.txMbps += txMbps;

      // Track per-destination breakdown
      const dstKey = dst + '|' + proto;
      if (!srcEntry.dsts.has(dstKey)) {
        srcEntry.dsts.set(dstKey, { rxMbps: 0, txMbps: 0, proto, iface, dstIp: dst });
      }
      const dstEntry = srcEntry.dsts.get(dstKey);
      dstEntry.rxMbps += rxMbps;
      dstEntry.txMbps += txMbps;
    }

    // Prune stale connection IDs
    for (const k of this._prev.keys()) {
      if (!seenIds.has(k)) this._prev.delete(k);
    }

    // Build rows — one per source IP, top destination attached
    const devices = [];
    for (const [srcIp, entry] of srcMap.entries()) {
      const resolved = this._resolveName(srcIp);

      // Best destination = highest combined throughput
      let topDst = { rxMbps: 0, txMbps: 0, proto: '', iface: '', dstIp: '' };
      for (const d of entry.dsts.values()) {
        if ((d.rxMbps + d.txMbps) > (topDst.rxMbps + topDst.txMbps)) topDst = d;
      }

      const dstIp  = topDst.dstIp;
      const geo    = dstIp ? this._geo(dstIp) : { country: '', city: '' };
      const org    = dstIp ? this._org(dstIp) : null;
      const cat    = org ? lookupCategory(org) : null;
      const isLan  = dstIp ? isInCidrs(dstIp, lanCidrs) : false;
      const isIpv6 = srcIp.includes(':');

      devices.push({
        srcIp,
        dstIp,
        rxMbps:    +entry.rxMbps.toFixed(4),
        txMbps:    +entry.txMbps.toFixed(4),
        totalMbps: +(entry.rxMbps + entry.txMbps).toFixed(4),
        proto:     topDst.proto,
        iface:     topDst.iface,
        name:      resolved.name,
        mac:       resolved.mac,
        country:   geo.country,
        city:      geo.city,
        org,
        cat,
        isLan,
        isIpv6,
      });
    }

    // Sort by total throughput descending
    devices.sort((a, b) => b.totalMbps - a.totalMbps);

    this.lastPayload = { ts: now, devices, pollMs: this.pollMs };
    this.io.emit('bandwidth:update', this.lastPayload);
  }

  start() {
    const run = async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(); } catch (e) {
        console.error('[bandwidth]', e && e.message ? e.message : e);
      } finally { this._inflight = false; }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close', () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => {
      this._prev.clear();
      this._geoCache.clear();
      this._orgCache.clear();
      this._ifaceCache.clear();
      this.timer = this.timer || setInterval(run, this.pollMs);
      run();
    });
  }
}

module.exports = BandwidthCollector;
