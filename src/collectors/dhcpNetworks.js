const ipaddr = require('ipaddr.js');

function ipInCidr(ip, cidr) {
  try { return ipaddr.parse(ip).match(ipaddr.parseCIDR(cidr)); } catch { return false; }
}

// Extract the first IP from a RouterOS ranges string like "192.168.1.100-192.168.1.200"
function firstIpOfRange(rangesStr) {
  if (!rangesStr) return null;
  const first = String(rangesStr).split(',')[0].trim();
  const ip = first.includes('-') ? first.split('-')[0].trim() : first;
  try { ipaddr.parse(ip); return ip; } catch { return null; }
}

// Count total IPs across all ranges in a RouterOS pool ranges string.
function poolRangeSize(rangesStr) {
  if (!rangesStr) return 0;
  let total = 0;
  for (const part of String(rangesStr).split(',')) {
    const trimmed = part.trim();
    const dash = trimmed.lastIndexOf('-');
    if (dash < 0) { total += 1; continue; }
    const from = trimmed.slice(0, dash).trim();
    const to   = trimmed.slice(dash + 1).trim();
    try {
      const fromN = ipaddr.parse(from).toByteArray().reduce((acc, b) => (acc << 8) + b, 0) >>> 0;
      const toN   = ipaddr.parse(to).toByteArray().reduce((acc, b) => (acc << 8) + b, 0) >>> 0;
      if (toN >= fromN) total += (toN - fromN + 1);
    } catch { /* skip malformed range */ }
  }
  return total;
}

class DhcpNetworksCollector {
  constructor({ ros, io, pollMs, dhcpLeases, state, wanIface }) {
    this.ros = ros;
    this.io = io;
    this._lbl = ros.routerLabel ? `[${ros.routerLabel}][dhcp-networks]` : '[dhcp-networks]';
    this.pollMs = pollMs;
    this.dhcpLeases = dhcpLeases;
    this.state = state;
    this.wanIface = wanIface || 'WAN1';
    this.lanCidrs = [];
    this.networks = [];
    this._lastFp  = '';
    this.lastPayload = null;

    this._streams    = { networks: null, addresses: null, pools: null, internet: null };
    this._raw        = { networks: [], addresses: [], pools: [], internet: [] };
    this._batches    = { networks: [], addresses: [], pools: [], internet: [] };
    this._debounces  = { networks: null, addresses: null, pools: null, internet: null };
    this._restarting = { networks: false, addresses: false, pools: false, internet: false };
    this._restartTimers = { networks: null, addresses: null, pools: null, internet: null };
    this._rebuildDebounce = null;
  }

  getLanCidrs() { return this.lanCidrs; }

  // ── rebuild (combination logic from former tick()) ────────────────────────

  _rebuild() {
    if (!this.ros.connected) return;
    const netRows    = this._raw.networks;
    const addrRows   = this._raw.addresses;
    const poolRows   = this._raw.pools;
    const detectRows = this._raw.internet;

    const internetIfaces = detectRows
      .filter(r => r.state === 'internet')
      .map(r => {
        const ifName = r.name || r.interface || '';
        const addr = addrRows.find(a => a.interface === ifName && a.disabled !== 'true');
        return { name: ifName, ip: addr ? addr.address : '' };
      });

    const wanIface = this.wanIface;
    let wanIp = '';
    for (const a of addrRows) {
      if (a.interface === wanIface && a.address) { wanIp = a.address; break; }
    }

    const allLeaseIps = this.dhcpLeases ? this.dhcpLeases.getAllLeaseIPs() : [];

    const lanCidrs = [];
    const networks = [];

    for (const n of netRows) {
      if (!n.address) continue;
      lanCidrs.push(n.address);

      const leaseCount = allLeaseIps.reduce(
        (acc, ip) => acc + (ipInCidr(ip, n.address) ? 1 : 0), 0
      );

      // Match pools to this subnet by checking whether the pool's first IP
      // falls within the subnet CIDR. This is more reliable than chasing the
      // server→interface→address chain.
      let size = 0;
      for (const p of poolRows) {
        if (!p.ranges) continue;
        const firstIp = firstIpOfRange(p.ranges);
        if (firstIp && ipInCidr(firstIp, n.address)) {
          size += poolRangeSize(p.ranges);
        }
      }

      networks.push({
        cidr:       n.address,
        gateway:    n.gateway || '',
        dns:        n['dns-server'] || n['dns'] || '',
        leaseCount,
        poolSize:   size,
      });
    }

    this.lanCidrs = Array.from(new Set(lanCidrs));
    this.networks = networks;
    if (this.state) this.state.lastWanIp = wanIp;

    const totalPoolSize = networks.reduce((a, n) => a + (n.poolSize || 0), 0);
    const totalLeases   = networks.reduce((a, n) => a + (n.leaseCount || 0), 0);

    const fp = JSON.stringify({
      cidrs: this.lanCidrs, wanIp, internetIfaces,
      networks: networks.map(n => ({ cidr: n.cidr, leaseCount: n.leaseCount, poolSize: n.poolSize })),
    });
    this.lastPayload = {
      ts: Date.now(), lanCidrs: this.lanCidrs, networks: this.networks,
      wanIp, totalPoolSize, totalLeases, pollMs: this.pollMs, internetIfaces,
    };
    if (fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.emit('lan:overview', this.lastPayload);
    }
    this.state.lastNetworksTs = Date.now();
  }

  _scheduleRebuild() {
    if (this._rebuildDebounce) return;
    this._rebuildDebounce = setTimeout(() => { // codeql[js/resource-exhaustion]
      this._rebuildDebounce = null;
      this._rebuild();
    }, 10);
  }

  // ── initial fetch ─────────────────────────────────────────────────────────
  // Synchronous one-shot fetch to populate getLanCidrs() before stream batches
  // arrive. Also called on reconnect to restore state immediately.

  async _fetchOnce() {
    if (!this.ros.connected) return;
    const [nets, addrs, pools, detect] = await Promise.allSettled([
      this.ros.write('/ip/dhcp-server/network/print', ['=.proplist=address,gateway,dns-server']),
      this.ros.write('/ip/address/print',             ['=.proplist=address,interface,disabled']),
      this.ros.write('/ip/pool/print',                ['=.proplist=name,ranges']),
      this.ros.write('/interface/detect-internet/state/print', ['=.proplist=name,interface,state']),
    ]);
    this._raw.networks  = nets.status   === 'fulfilled' ? (nets.value   || []) : [];
    this._raw.addresses = addrs.status  === 'fulfilled' ? (addrs.value  || []) : [];
    this._raw.pools     = pools.status  === 'fulfilled' ? (pools.value  || []) : [];
    this._raw.internet  = detect.status === 'fulfilled' ? (detect.value || []) : [];
    this._rebuild();
  }

  // ── stream management ────────────────────────────────────────────────────

  _startStream(key) {
    if (this._streams[key] || this._restarting[key]) return;
    if (!this.ros.connected) return;
    const intervalSec = Math.max(5, Math.round(this.pollMs / 1000));
    const cmds = {
      networks:  ['/ip/dhcp-server/network/print', '=.proplist=address,gateway,dns-server'],
      addresses: ['/ip/address/print',             '=.proplist=address,interface,disabled'],
      pools:     ['/ip/pool/print',                '=.proplist=name,ranges'],
      internet:  ['/interface/detect-internet/state/print', '=.proplist=name,interface,state'],
    };
    const stream = this.ros.stream([...cmds[key], `=interval=${intervalSec}`], null);
    this._streams[key] = stream;
    stream.on('data', (pkt) => {
      if (!pkt || typeof pkt !== 'object' || Array.isArray(pkt)) return;
      this._batches[key].push(pkt);
      if (this._debounces[key]) return;
      this._debounces[key] = setTimeout(() => { // codeql[js/resource-exhaustion]
        this._debounces[key] = null;
        this._raw[key] = this._batches[key];
        this._batches[key] = [];
        this._scheduleRebuild();
      }, 50);
    });
    stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      console.error(this._lbl, `${key} stream error:`, msg);
      this._stopStream(key);
      if (this.ros.connected && !this._restarting[key]) {
        this._restarting[key] = true;
        this._restartTimers[key] = setTimeout(() => { // codeql[js/resource-exhaustion]
          this._restarting[key] = false;
          this._restartTimers[key] = null;
          this._startStream(key);
        }, 3000);
      }
    });
    console.log(this._lbl, `streaming ${cmds[key][0]} interval=${intervalSec}s`);
  }

  _stopStream(key) {
    if (this._debounces[key])     { clearTimeout(this._debounces[key]);     this._debounces[key] = null; }
    if (this._restartTimers[key]) { clearTimeout(this._restartTimers[key]); this._restartTimers[key] = null; }
    this._restarting[key] = false;
    if (this._streams[key]) { try { this._streams[key].stop(); } catch (_) {} this._streams[key] = null; }
    this._batches[key] = [];
  }

  _startStreams() { for (const k of ['networks', 'addresses', 'pools', 'internet']) this._startStream(k); }
  _stopStreams()  { for (const k of ['networks', 'addresses', 'pools', 'internet']) this._stopStream(k); }

  // ── lifecycle ────────────────────────────────────────────────────────────

  async start() {
    // Initial fetch populates getLanCidrs() synchronously before stream batches
    // arrive — other collectors depend on it at startup.
    if (this.ros.connected) {
      try { await this._fetchOnce(); } catch (e) { console.error(this._lbl, e && e.message ? e.message : e); }
    }
    this._startStreams();
    this.ros.on('close', () => this.stop());
    this.ros.on('connected', () => {
      this._lastFp = '';
      this.stop();
      this._fetchOnce().catch(e => console.error(this._lbl, e && e.message ? e.message : e));
      this._startStreams();
    });
  }

  suspend() {
    this._stopStreams();
    if (this._rebuildDebounce) { clearTimeout(this._rebuildDebounce); this._rebuildDebounce = null; }
  }

  resume() {
    if (this.ros.connected) this._startStreams();
  }

  stop() {
    this._stopStreams();
    if (this._rebuildDebounce) { clearTimeout(this._rebuildDebounce); this._rebuildDebounce = null; }
  }
}

module.exports = DhcpNetworksCollector;
