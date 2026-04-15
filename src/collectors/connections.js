let geoip = null;
try { geoip = require('geoip-lite'); } catch(e) { console.warn('[connections] geoip-lite not available, geo lookups disabled'); }
/**
 * Connections collector — polls /ip/firewall/connection/print on interval.
 * node-routeros allows this to run concurrently with active streams since
 * each write() gets a unique tag for demultiplexing.
 */
const { extractAddress, isInCidrs, isValidIp } = require('../util/ip');
const { lookupOrg, lookupCategory } = require('../util/asnLookup');

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
  constructor({ ros, io, pollMs, topN, dhcpNetworks, dhcpLeases, arp, state, maxConns, geoLookup, connTableCache, geoOrgCache }) {
    this.ros = ros;
    this.io = io;
    this.pollMs = pollMs;
    this.topN = topN;
    this.maxConns = maxConns || 20000;
    this.dhcpNetworks = dhcpNetworks;
    this.dhcpLeases = dhcpLeases;
    this.arp = arp;
    this.state = state;
    this.geoLookup = geoLookup || (geoip ? (ip) => geoip.lookup(ip) : null);
    this.connTableCache = connTableCache || null;
    // Shared with BandwidthCollector so geo/org lookups for the same IPs are
    // computed once and reused across both collectors and across ticks.
    this._geoCache = geoOrgCache ? geoOrgCache.geo : new Map(); // ip -> { country, city }
    this._orgCache = geoOrgCache ? geoOrgCache.org : new Map(); // ip -> org string | null
    this.prevIds = new Set();
    this.timer = null;
    this._inflight = false;
    this.lastPayload = null;
    this._lastFp = '';
    // Set to true by start(), never reset. Allows the connected handler to
    // distinguish the initial connect (where startCollectors() calls start()
    // explicitly) from a reconnect after a close event.
    this._started = false;

    // Register ROS lifecycle listeners once in the constructor so they never
    // accumulate across multiple start() calls (matches the canonical pattern).
    this.ros.on('close',     () => this.stop());
    this.ros.on('connected', () => {
      // Clear geo/org caches on reconnect — IPs may be reassigned.
      this._geoCache.clear();
      this._orgCache.clear();
      // Only restart here on reconnect after a close. On the very first
      // connect, startCollectors() in index.js calls start() explicitly —
      // calling stop()+start() here too would create two concurrent intervals.
      if (this._started) {
        this._lastFp = '';
        this.stop();
        this.start();
      }
    });
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

  async tick(force = false) {
    if (!this.ros.connected) return;
    // Skip when no browser clients are watching — the connection table can be
    // large and geo/ASN lookups are non-trivial; no point doing the work idle.
    if (!force && this.io.engine.clientsCount === 0) return;
    const lanCidrs = this.dhcpNetworks.getLanCidrs();

    // Use shared cache when available — halves API calls when both
    // connections and bandwidth collectors run on similar poll intervals.
    const raw = this.connTableCache
      ? await this.connTableCache.get(this.ros)
      : ((await this.ros.write('/ip/firewall/connection/print')) || []);
    const totalRaw = (raw || []).length;
    // When capped, connections beyond maxConns are not processed — their
    // destination IPs will be missing from the geo cache, so top destinations
    // that only appear in the truncated portion will lack country/city data.
    const conns = totalRaw > this.maxConns ? raw.slice(0, this.maxConns) : (raw || []);
    const srcCounts         = new Map();
    const dstCounts         = new Map();
    const srcDestsMap       = new Map(); // srcIp -> Map<destKey, count> — for per-source filter
    const curIds            = new Set();
    const protoCounts       = { tcp: 0, udp: 0, icmp: 0, other: 0 };
    const countryProto      = new Map();
    const countryCity       = new Map();
    const portCounts        = new Map();
    const countryPortCounts = new Map(); // cc -> Map<port, count> — per-country port index
    const sourcePortCounts  = new Map(); // srcIp -> Map<port, count> — per-source port index
    const countryOrgs       = new Map(); // cc -> Map<org, count>
    // this._geoCache and this._orgCache are persistent across ticks (shared with
    // BandwidthCollector) — external IP→country/org is stable between 3s polls.

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
        if (this.geoLookup && isValidIp(ip)) {
          if (!this._geoCache.has(ip)) {
            const geo = this.geoLookup(ip);
            this._geoCache.set(ip, geo && geo.country
              ? { country: geo.country, city: geo.city || '' }
              : { country: '', city: '' });
          }
          const cached = this._geoCache.get(ip);
          if (cached.country) {
            const cc = cached.country;
            if (!countryCity.has(cc)) countryCity.set(cc, cached.city);
            const cp = countryProto.get(cc) || { tcp:0, udp:0, other:0 };
            if (p === 'tcp') cp.tcp++; else if (p === 'udp') cp.udp++; else cp.other++;
            countryProto.set(cc, cp);
            // Per-country port index — counts every connection, no destination cap
            if (port) {
              if (!countryPortCounts.has(cc)) countryPortCounts.set(cc, new Map());
              const cpc = countryPortCounts.get(cc);
              cpc.set(port, (cpc.get(port) || 0) + 1);
            }
          }
        }
        if (isValidIp(ip) && !this._orgCache.has(ip)) {
          const org = lookupOrg(ip);
          this._orgCache.set(ip, org || null);
        }
        // Tally org connections per country for the breakdown sub-rows
        const resolvedOrg = this._orgCache.get(ip);
        if (resolvedOrg) {
          const cc = (this._geoCache.get(ip) || {}).country || '__unknown__';
          if (!countryOrgs.has(cc)) countryOrgs.set(cc, new Map());
          const orgMap = countryOrgs.get(cc);
          orgMap.set(resolvedOrg, (orgMap.get(resolvedOrg) || 0) + 1);
        }

        // Per-source destination + port indexes — power the client-side source filter
        if (src && isInCidrs(src, lanCidrs)) {
          if (!srcDestsMap.has(src)) srcDestsMap.set(src, new Map());
          const sdm = srcDestsMap.get(src);
          sdm.set(k, (sdm.get(k) || 0) + 1);
          if (port) {
            if (!sourcePortCounts.has(src)) sourcePortCounts.set(src, new Map());
            const spc = sourcePortCounts.get(src);
            spc.set(port, (spc.get(port) || 0) + 1);
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
        const geo = this._geoCache.get(ip) || { country: '', city: '' };
        const country = geo.country;
        const city = geo.city;
        const proto = country ? (countryProto.get(country) || {}) : {};
        const org = this._orgCache.get(ip) || null;
        const cat = org ? lookupCategory(org) : null;
        return { key, count, country, city, proto, org, cat };
      });

    // Per-country destination index — used by the client-side country filter to
    // populate the Connection Flow and Top Ports cards even for countries whose
    // individual IPs don't appear in the global topDestinations list.
    // Includes all destinations for every country in topCountries, sorted by
    // connection count, capped at 20 per country to keep payload size bounded.
    const countryDests = {};
    for (const [key, count] of dstCounts.entries()) {
      const ip = extractAddress(key);
      const geo = this._geoCache.get(ip);
      if (!geo || !geo.country) continue;
      const cc = geo.country;
      if (!countryDests[cc]) countryDests[cc] = [];
      const org = this._orgCache.get(ip) || null;
      const cat = org ? lookupCategory(org) : null;
      countryDests[cc].push({ key, count, country: cc, city: geo.city || '', org, cat });
    }
    for (const cc of Object.keys(countryDests)) {
      countryDests[cc].sort((a, b) => b.count - a.count);
      if (countryDests[cc].length > 20) countryDests[cc].length = 20;
    }

    // Per-country port index — top 10 ports for each country, counts every
    // matching connection (not capped by destination list size like countryDests).
    const countryPorts = {};
    for (const [cc, portMap] of countryPortCounts.entries()) {
      countryPorts[cc] = Array.from(portMap.entries())
        .sort((a, b) => b[1] - a[1])
        .slice(0, 10)
        .map(([port, count]) => ({ port, count }));
    }

    // Per-source destination index — mirrors countryDests but keyed by source IP.
    // Clients use this to filter all connections-page cards to a single device.
    const sourceDests = {};
    for (const [srcIp, dstMap] of srcDestsMap.entries()) {
      const entries = [];
      for (const [key, cnt] of dstMap.entries()) {
        const ip  = extractAddress(key);
        const geo = this._geoCache.get(ip) || { country: '', city: '' };
        const org = this._orgCache.get(ip) || null;
        const cat = org ? lookupCategory(org) : null;
        entries.push({ key, count: cnt, country: geo.country, city: geo.city, org, cat });
      }
      entries.sort((a, b) => b.count - a.count);
      sourceDests[srcIp] = entries.slice(0, 30);
    }

    // Per-source port index — top 10 ports per source IP, counts every matching
    // connection (not capped like sourceDests) so Top Ports totals are accurate.
    const sourcePorts = {};
    for (const [srcIp, portMap] of sourcePortCounts.entries()) {
      sourcePorts[srcIp] = Array.from(portMap.entries())
        .sort((a, b) => b[1] - a[1])
        .slice(0, 10)
        .map(([port, count]) => ({ port, count }));
    }

    const topCountries = Array.from(countryProto.entries())
      .map(([cc, proto]) => {
        // Top orgs for this country, sorted by connection count
        const orgMap = countryOrgs.get(cc);
        const orgs = orgMap
          ? Array.from(orgMap.entries())
              .sort((a, b) => b[1] - a[1])
              .slice(0, 4)
              .map(([org, count]) => ({ org, count, cat: lookupCategory(org) }))
          : [];
        return {
          cc, city: countryCity.get(cc) || '',
          count: (proto.tcp||0)+(proto.udp||0)+(proto.other||0),
          proto, orgs,
        };
      })
      .sort((a,b) => b.count - a.count)
      .slice(0, 30); // cap — client never renders more than this

    const topPorts = Array.from(portCounts.entries())
      .sort((a,b) => b[1]-a[1]).slice(0,10)
      .map(([port,count]) => ({ port, count }));

    this.lastPayload = {
      ts: Date.now(), total: totalRaw, processed: conns.length, processingCapped: totalRaw > this.maxConns, newSinceLast,
      protoCounts, topSources, topDestinations, topCountries, topPorts, countryDests, countryPorts, sourceDests, sourcePorts, pollMs: this.pollMs,
    };
    // Dirty-check: suppress emit when aggregate counts and top-N lists are unchanged.
    // ts and newSinceLast are deliberately excluded — they change every tick.
    const fp = JSON.stringify({
      total: totalRaw, protoCounts,
      src: topSources.map(s => ({ ip: s.ip, n: s.count })),
      dst: topDestinations.map(d => ({ k: d.key, n: d.count })),
      ports: topPorts,
    });
    if (fp !== this._lastFp) {
      this._lastFp = fp;
      // Global emit omits countryDests, countryPorts, sourceDests, sourcePorts —
      // only the Connections page needs them; lastPayload retains all for sendInitialState.
      const emitPayload = Object.assign({}, this.lastPayload);
      delete emitPayload.countryDests;
      delete emitPayload.countryPorts;
      delete emitPayload.sourceDests;
      delete emitPayload.sourcePorts;
      this.io.emit('conn:update', emitPayload);
      // Connections page gets per-country destination + port indexes.
      this.io.to('page-connections').emit('conn:country-data', {
        ts: this.lastPayload.ts,
        countryDests: this.lastPayload.countryDests,
        countryPorts: this.lastPayload.countryPorts,
      });
      this.io.to('page-connections').emit('conn:source-data', {
        ts: this.lastPayload.ts,
        sourceDests: this.lastPayload.sourceDests,
        sourcePorts: this.lastPayload.sourcePorts,
      });
    }
    this.state.lastConnsTs = Date.now();
    this.state.lastConnsErr = null;
  }

  stop() {
    if (this.timer) { clearInterval(this.timer); this.timer = null; }
  }

  start() {
    this._started = true;
    const run = async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(); } catch (e) {
        const msg = String(e && e.message ? e.message : e);
        // RouterOS races: connections expire between list and fetch — not a real error
        if (msg.includes('no such item')) return;
        this.state.lastConnsErr = msg;
        console.error('[connections]', this.state.lastConnsErr);
      } finally { this._inflight = false; }
    };
    // Set the timer before the first run so the close handler can always
    // find and clear it, even if run() resolves synchronously.
    this.timer = setInterval(run, this.pollMs);
    run();
  }
}

module.exports = ConnectionsCollector;
