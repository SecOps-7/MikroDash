let geoip = null;
try { geoip = require('geoip-lite'); } catch(e) { console.warn('[connections] geoip-lite not available, geo lookups disabled'); }
/**
 * Connections collector — polls /ip/firewall/connection/print on interval.
 * node-routeros allows this to run concurrently with active streams since
 * each write() gets a unique tag for demultiplexing.
 */
const { extractAddress, isInCidrs, isValidIp } = require('../util/ip');

function makeDestKey(c) {
  const dst   = c['dst-address'] || c.dst || '';
  const proto = (c.protocol || c['ip-protocol'] || '').toLowerCase();
  const dport = c['dst-port'] || c['port'] || '';
  const displayDst = isValidIp(dst) && dst.includes(':') ? `[${dst}]` : dst;
  if (displayDst && proto && dport) return displayDst + ':' + dport + '/' + proto;
  if (displayDst && dport)          return displayDst + ':' + dport;
  return displayDst || 'unknown';
}

class ConnectionsCollector {
  constructor({ ros, io, pollMs, topN, dhcpNetworks, dhcpLeases, arp, state }) {
    this.ros = ros;
    this.io = io;
    this.pollMs = pollMs;
    this.topN = topN;
    this.dhcpNetworks = dhcpNetworks;
    this.dhcpLeases = dhcpLeases;
    this.arp = arp;
    this.state = state;
    this.prevIds = new Set();
    this.timer = null;
  }

  resolveName(ip) {
    const lease = this.dhcpLeases.getNameByIP(ip);
    if (lease && lease.name) return { name: lease.name, mac: lease.mac };
    const a = this.arp.getByIP(ip);
    if (a && a.mac) {
      const lm = this.dhcpLeases.getNameByMAC(a.mac);
      if (lm && lm.name) return { name: lm.name, mac: a.mac };
      return { name: 'Unknown (' + a.mac + ')', mac: a.mac };
    }
    return { name: ip, mac: '' };
  }

  async tick() {
    if (!this.ros.connected) return;
    const lanCidrs = this.dhcpNetworks.getLanCidrs();

    // node-routeros: write() is concurrent-safe, doesn't block streams
    const conns = await this.ros.write('/ip/firewall/connection/print');
    const srcCounts = new Map();
    const dstCounts = new Map();
    const curIds    = new Set();
    const protoCounts = { tcp: 0, udp: 0, icmp: 0, other: 0 };
    const countryProto = new Map();
    const countryCity  = new Map();
    const portCounts   = new Map();

    for (const c of (conns || [])) {
      const id  = c['.id'];
      const src = c['src-address'] || c.src || '';
      const dst = c['dst-address'] || c.dst || '';
      const p   = (c.protocol || c['ip-protocol'] || '').toLowerCase();
      if (id) curIds.add(id);

      // Protocol counts
      if (p === 'tcp') protoCounts.tcp++;
      else if (p === 'udp') protoCounts.udp++;
      else if (p.includes('icmp')) protoCounts.icmp++;
      else protoCounts.other++;

      // Source counts (LAN hosts)
      if (src && isInCidrs(src, lanCidrs)) srcCounts.set(src, (srcCounts.get(src) || 0) + 1);

      // Destination counts, geo, and port tracking (non-LAN)
      if (dst && !isInCidrs(dst, lanCidrs)) {
        const k = makeDestKey(c);
        dstCounts.set(k, (dstCounts.get(k) || 0) + 1);
        const ip   = extractAddress(dst);
        const port = c['dst-port'] || c['port'] || '';
        if (port) portCounts.set(port, (portCounts.get(port) || 0) + 1);
        if (geoip && isValidIp(ip)) {
          const geo = geoip.lookup(ip);
          if (geo && geo.country) {
            const cc = geo.country;
            if (!countryCity.has(cc)) countryCity.set(cc, geo.city || '');
            const cp = countryProto.get(cc) || { tcp:0, udp:0, other:0 };
            if (p === 'tcp') cp.tcp++; else if (p === 'udp') cp.udp++; else cp.other++;
            countryProto.set(cc, cp);
          }
        }
      }
    }

    let newSinceLast = 0;
    for (const id of curIds) if (!this.prevIds.has(id)) newSinceLast++;
    this.prevIds = curIds;

    const topSources = Array.from(srcCounts.entries())
      .sort((a, b) => b[1] - a[1]).slice(0, this.topN)
      .map(([ip, count]) => { const r = this.resolveName(ip); return { ip, name: r.name, mac: r.mac, count }; });

    const topDestinations = Array.from(dstCounts.entries())
      .sort((a, b) => b[1] - a[1]).slice(0, this.topN)
      .map(([key, count]) => {
        const ip = extractAddress(key);
        let country = '', city = '';
        if (geoip && isValidIp(ip)) {
          const geo = geoip.lookup(ip);
          if (geo) { country = geo.country || ''; city = geo.city || ''; }
        }
        const proto = country ? (countryProto.get(country) || {}) : {};
        return { key, count, country, city, proto };
      });

    const topCountries = Array.from(countryProto.entries())
      .map(([cc, proto]) => ({
        cc, city: countryCity.get(cc) || '',
        count: (proto.tcp||0)+(proto.udp||0)+(proto.other||0),
        proto
      }))
      .sort((a,b) => b.count - a.count); // all countries, no cap

    const topPorts = Array.from(portCounts.entries())
      .sort((a,b) => b[1]-a[1]).slice(0,10)
      .map(([port,count]) => ({ port, count }));

    this.io.emit('conn:update', {
      ts: Date.now(), total: (conns || []).length, newSinceLast,
      protoCounts, topSources, topDestinations, topCountries, topPorts,
    });
    this.state.lastConnsTs = Date.now();
    this.state.lastConnsErr = null;
  }

  start() {
    const run = async () => {
      try { await this.tick(); } catch (e) {
        const msg = String(e && e.message ? e.message : e);
        // RouterOS races: connections expire between list and fetch — not a real error
        if (msg.includes('no such item')) return;
        this.state.lastConnsErr = msg;
        console.error('[connections]', this.state.lastConnsErr);
      }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close', () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => { this.timer = this.timer || setInterval(run, this.pollMs); run(); });
  }
}

module.exports = ConnectionsCollector;
