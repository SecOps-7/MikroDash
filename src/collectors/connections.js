let geoip = null;
try { geoip = require('geoip-lite'); } catch(e) { console.warn('[connections] geoip-lite not available, geo lookups disabled'); }
/**
 * Connections collector — streams /ip/firewall/connection/print interval=N.
 * One persistent stream per session; rows are accumulated per batch (each
 * interval fires a full table dump ending with a trigger packet).  On batch
 * complete, rows are deposited into connTableCache for BandwidthCollector to
 * read, then the expensive geo/ASN processing runs (skipped when idle).
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

const PARTIAL_DROP_RATIO = 0.5;
const PARTIAL_DROP_MIN   = 10;
const PARTIAL_MAX_STREAK = 5;

const _categoryCache = new Map();
function _cachedCategory(org) {
  if (_categoryCache.has(org)) return _categoryCache.get(org);
  const cat = lookupCategory(org);
  _categoryCache.set(org, cat);
  return cat;
}

class ConnectionsCollector {
  constructor({ ros, io, pollMs, topN, dhcpNetworks, dhcpLeases, arp, state, maxConns, geoLookup, connTableCache, geoOrgCache }) {
    this.ros = ros;
    this.io = io;
    const _cPoll = Number.isFinite(Number(pollMs)) ? Math.trunc(Number(pollMs)) : 5000;
    this.pollMs = Math.max(500, Math.min(60000, _cPoll));
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
    this.lastPayload = null;
    this._lastFp = '';
    this._lastEmitTs = 0;
    this._lastDetailFp = '';
    this._fallbackInflight = false;
    this._stream = null;
    this._pollTimer = null;   // fallback poll timer used when stream is not running
    this._rowsNext = [];      // accumulates rows for the current in-progress batch
    this._rowsPrev = null;    // last committed batch, used for partial-result detection
    this._partialStreak = 0;
    this._commitTimer  = null; // debounce: fires 300ms after last row arrives
    this._watchdogTimer = null;
    this._streamStartTs = 0;  // when _startStream() last ran, for watchdog grace period
    // Set to true by start(), never reset. Allows the connected handler to
    // distinguish the initial connect from a reconnect after a close event.
    this._started = false;

    this.ros.on('close',     () => this.stop());
    this.ros.on('connected', () => {
      // Clear geo/org caches on reconnect — IPs may be reassigned.
      this._geoCache.clear();
      this._orgCache.clear();
      if (this._started) {
        this._lastFp = '';
        this._lastDetailFp = '';
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

  // Debounce: schedule a commit 300ms after the last row of a batch arrives.
  // RouterOS sends rows in bursts (one !re per connection) with silence between
  // intervals — there is no explicit trigger packet marking batch end, so we
  // treat 300ms of silence as "this interval's batch is complete".
  _scheduleCommit() {
    clearTimeout(this._commitTimer);
    this._commitTimer = setTimeout(() => {
      this._commitTimer = null;
      this._onBatchComplete();
    }, 300);
  }

  // Runs partial-result detection, deposits into cache, then processes rows.
  _onBatchComplete() {
    const fresh = this._rowsNext;
    this._rowsNext = [];

    const looksPartial = this._rowsPrev !== null
      && this._rowsPrev.length > PARTIAL_DROP_MIN
      && fresh.length > 0
      && fresh.length < this._rowsPrev.length * PARTIAL_DROP_RATIO;

    let rows;
    if (looksPartial) {
      this._partialStreak++;
      const dbg = require('../settings').load().rosDebug;
      if (this._partialStreak >= PARTIAL_MAX_STREAK) {
        if (dbg) console.warn(`[connections] partial result (${fresh.length} rows, prev ${this._rowsPrev.length}) — accepted after ${this._partialStreak} consecutive`);
        this._partialStreak = 0;
        rows = fresh;
        this._rowsPrev = fresh;
      } else {
        if (dbg) console.warn(`[connections] partial result (${fresh.length} rows, prev ${this._rowsPrev.length}) — keeping stale (${this._partialStreak}/${PARTIAL_MAX_STREAK})`);
        rows = this._rowsPrev;
      }
    } else {
      this._partialStreak = 0;
      rows = (fresh.length > 0 || this._rowsPrev === null) ? fresh : this._rowsPrev;
      this._rowsPrev = rows;
    }

    // Always deposit into shared cache (cheap) so bandwidth can read fresh data
    if (this.connTableCache) this.connTableCache.deposit(rows, Date.now());

    // Skip expensive geo/ASN processing when no browser clients are watching
    if (this.io.engine.clientsCount === 0) return;

    this._processRows(rows).catch(e => console.error('[connections]', e));
  }

  async _processRows(raw) {
    const lanCidrs = this.dhcpNetworks.getLanCidrs();
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
    // BandwidthCollector) — external IP→country/org is stable between polls.

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
        const cat = org ? _cachedCategory(org) : null;
        return { key, count, country, city, proto, org, cat };
      });

    // Only build per-country and per-source indexes when the connections page is
    // actually open — these structures are the most CPU-intensive part of the tick
    // (iterating all destinations, running geo lookups, building nested maps) and
    // are only emitted to the page-connections room anyway.
    const buildDetailed = (this.io.sockets.adapter.rooms.get('page-connections')?.size || 0) > 0;

    const countryDests = {};
    const countryPorts = {};
    const sourceDests  = {};
    const sourcePorts  = {};

    if (buildDetailed) {
      // Per-country destination index — used by the client-side country filter to
      // populate the Connection Flow and Top Ports cards even for countries whose
      // individual IPs don't appear in the global topDestinations list.
      for (const [key, count] of dstCounts.entries()) {
        const ip = extractAddress(key);
        const geo = this._geoCache.get(ip);
        if (!geo || !geo.country) continue;
        const cc = geo.country;
        if (!countryDests[cc]) countryDests[cc] = [];
        const org = this._orgCache.get(ip) || null;
        const cat = org ? _cachedCategory(org) : null;
        countryDests[cc].push({ key, count, country: cc, city: geo.city || '', org, cat });
      }
      for (const cc of Object.keys(countryDests)) {
        countryDests[cc].sort((a, b) => b.count - a.count);
        if (countryDests[cc].length > 20) countryDests[cc].length = 20;
      }

      // Per-country port index — top 10 ports for each country.
      for (const [cc, portMap] of countryPortCounts.entries()) {
        countryPorts[cc] = Array.from(portMap.entries())
          .sort((a, b) => b[1] - a[1])
          .slice(0, 10)
          .map(([port, count]) => ({ port, count }));
      }

      // Per-source destination index — keyed by source IP.
      for (const [srcIp, dstMap] of srcDestsMap.entries()) {
        const entries = [];
        for (const [key, cnt] of dstMap.entries()) {
          const ip  = extractAddress(key);
          const geo = this._geoCache.get(ip) || { country: '', city: '' };
          const org = this._orgCache.get(ip) || null;
          const cat = org ? _cachedCategory(org) : null;
          entries.push({ key, count: cnt, country: geo.country, city: geo.city, org, cat });
        }
        entries.sort((a, b) => b.count - a.count);
        sourceDests[srcIp] = entries.slice(0, 30);
      }

      // Per-source port index — top 10 ports per source IP.
      for (const [srcIp, portMap] of sourcePortCounts.entries()) {
        sourcePorts[srcIp] = Array.from(portMap.entries())
          .sort((a, b) => b[1] - a[1])
          .slice(0, 10)
          .map(([port, count]) => ({ port, count }));
      }
    }

    const topCountries = Array.from(countryProto.entries())
      .map(([cc, proto]) => {
        // Top orgs for this country, sorted by connection count
        const orgMap = countryOrgs.get(cc);
        const orgs = orgMap
          ? Array.from(orgMap.entries())
              .sort((a, b) => b[1] - a[1])
              .slice(0, 4)
              .map(([org, count]) => ({ org, count, cat: _cachedCategory(org) }))
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
    const now = Date.now();
    // Fingerprint for the heavy per-country/per-source data (only built when
    // page-connections room is populated).
    const detailFp = buildDetailed ? JSON.stringify({
      cc:  Object.fromEntries([...countryProto.entries()].map(([k, v]) => [k, (v.tcp||0)+(v.udp||0)+(v.other||0)])),
      src: Object.fromEntries([...srcCounts.entries()]),
    }) : '';

    // Force-emit every 15 s even when data is unchanged — keeps the frontend
    // stale timer (pollMs + 20 s grace = 25 s) from expiring on stable networks.
    if (fp !== this._lastFp || now - this._lastEmitTs > 15000) {
      this._lastFp = fp;
      this._lastEmitTs = now;
      // Global emit omits countryDests, countryPorts, sourceDests, sourcePorts —
      // only the Connections page needs them; lastPayload retains all for sendInitialState.
      const emitPayload = Object.assign({}, this.lastPayload);
      delete emitPayload.countryDests;
      delete emitPayload.countryPorts;
      delete emitPayload.sourceDests;
      delete emitPayload.sourcePorts;
      this.io.emit('conn:update', emitPayload);
    }
    // Connections page gets per-country and per-source indexes only when they change.
    if (buildDetailed && detailFp !== this._lastDetailFp) {
      this._lastDetailFp = detailFp;
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
    this.state.lastConnsTs  = Date.now();
    this.state.lastConnsErr = null;
  }

  // tick(force) — kept for kickAndSend compatibility. Does a one-shot fetch when
  // lastPayload is null (stream hasn't fired its first batch yet). No-ops once
  // the stream has delivered initial data.
  async tick(force = false) {
    if (!this.ros.connected) return;
    if (!force && this.io.engine.clientsCount === 0) return;
    if (this.lastPayload) return; // stream is running; wait for next batch
    try {
      const rows = (await this.ros.write('/ip/firewall/connection/print', [
        '=.proplist=.id,src-address,dst-address,protocol,dst-port,orig-bytes,repl-bytes',
      ])) || [];
      if (this.connTableCache) this.connTableCache.deposit(rows, Date.now());
      this._rowsPrev = rows;
      await this._processRows(rows);
    } catch (e) {
      const msg = String(e && e.message ? e.message : e);
      if (!msg.includes('no such item')) {
        this.state.lastConnsErr = msg;
        console.error('[connections]', msg);
      }
    }
  }

  _startStream() {
    if (this._stream) return;
    if (!this.ros.connected) return;
    const intervalSec = Math.max(1, Math.round(this.pollMs / 1000));
    console.log('[connections] streaming interval=' + intervalSec + 's');
    this._stream = this.ros.stream(
      '/ip/firewall/connection/print',
      [
        '=.proplist=.id,src-address,dst-address,protocol,dst-port,orig-bytes,repl-bytes',
        `=interval=${intervalSec}`,
      ],
      null  // null callback — use 'data' event to bypass section-handling debounce
    );
    this._rowsNext = [];
    this._streamStartTs = Date.now();
    this._stream.on('data', (pkt) => {
      if (!pkt || typeof pkt !== 'object' || Array.isArray(pkt)) return;
      if (!pkt['.id']) return; // skip non-row packets
      this._rowsNext.push(pkt);
      // Reset the 300ms debounce — batch is complete when rows stop arriving
      this._scheduleCommit();
    });
    this._stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      // 'no such item' is a transient RouterOS error when a connection entry
      // expires mid-dump — log at debug level and restart rather than error.
      if (msg.includes('no such item')) {
        console.warn('[connections] stream: transient "no such item" — restarting');
      } else {
        console.error('[connections] stream error:', msg);
        this.state.lastConnsErr = msg;
      }
      this._stream = null;
      if (this._started && this.ros.connected) {
        setTimeout(() => this._startStream(), 3000);
      }
    });
  }

  _stopStream() {
    clearTimeout(this._commitTimer);
    this._commitTimer  = null;
    this._streamStartTs = 0;
    if (this._stream) {
      try { this._stream.stop().catch(() => {}); } catch (_) {}
      this._stream = null;
    }
    this._rowsNext = [];
  }

  _restartStream() {
    this._stopStream();
    this._lastFp = '';
    this._lastDetailFp = '';
    this._lastEmitTs = 0;
    if (this._started && this.ros.connected) this._startStream();
  }

  // Fallback poll: runs when the connections stream is not active (nobody on the
  // Connections page). Fetches the connection table at pollMs so the dashboard
  // connCard stays alive and connTableCache stays warm for bandwidth.
  // Uses recursive setTimeout (seamless interval) so a slow router response never
  // causes concurrent requests — matching the pattern of all other polling collectors.
  _scheduleFallbackNext() {
    if (this._pollTimer) return;
    this._pollTimer = setTimeout(async () => {
      this._pollTimer = null;
      await this._runFallbackTick();
      this._scheduleFallbackNext();
    }, this.pollMs); // codeql[js/resource-exhaustion]
  }

  _startPollFallback() {
    if (this._pollTimer) return;
    this._scheduleFallbackNext();
  }

  _stopPollFallback() {
    if (this._pollTimer) { clearTimeout(this._pollTimer); this._pollTimer = null; }
    this._fallbackInflight = false;
  }

  async _runFallbackTick() {
    if (!this.ros.connected || this.io.engine.clientsCount === 0) return;
    if (this._fallbackInflight) return;
    this._fallbackInflight = true;
    try {
      const rows = (await this.ros.write('/ip/firewall/connection/print', [
        '=.proplist=.id,src-address,dst-address,protocol,dst-port,orig-bytes,repl-bytes',
      ])) || [];
      if (this.connTableCache) this.connTableCache.deposit(rows, Date.now());
      this._rowsPrev = rows;
      await this._processRows(rows);
    } catch (e) {
      const msg = String(e && e.message ? e.message : e);
      if (!msg.includes('no such item')) {
        this.state.lastConnsErr = msg;
        console.error('[connections] fallback poll error:', msg);
      }
    } finally {
      this._fallbackInflight = false;
    }
  }

  // Watchdog: fires every 2× the poll interval. If the stream is supposed to be
  // running but lastConnsTs hasn't moved in 4× the interval, something went
  // wrong (silent stream death, unhandled event, etc.) — restart.
  _startWatchdog() {
    this._stopWatchdog();
    const checkMs   = Math.max(this.pollMs * 2, 10000);
    const staleMs   = Math.max(this.pollMs * 4, 20000);
    this._watchdogTimer = setInterval(() => {
      if (!this._started || !this.ros.connected || !this._stream) return;
      // Grace period: don't trigger within one staleMs window of stream start
      if (Date.now() - this._streamStartTs < staleMs) return;
      const age = Date.now() - this.state.lastConnsTs;
      if (age > staleMs) {
        console.warn(`[connections] watchdog: no data for ${Math.round(age / 1000)}s — restarting stream`);
        this._restartStream();
      }
    }, checkMs);
  }

  _stopWatchdog() {
    clearInterval(this._watchdogTimer);
    this._watchdogTimer = null;
  }

  suspend() { this._stopStream(); this._startPollFallback(); }

  resume() {
    this._stopPollFallback();
    if (this._started && this.ros.connected) this._startStream();
  }

  stop() {
    this._stopWatchdog();
    this._stopStream();
    this._stopPollFallback();
  }

  // start() does NOT open the stream immediately. The stream is opened by
  // resume(), which is called from index.js when the Connections page is open.
  // The fallback poll keeps the dashboard connCard and connTableCache warm.
  start() {
    this._started = true;
    this._startPollFallback();
    this._startWatchdog();
  }
}

module.exports = ConnectionsCollector;
