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
    this.timer = null;
    this._inflight = false;
    this._lastFp   = '';
    this.lastPayload = null;
  }

  getLanCidrs() { return this.lanCidrs; }

  async tick() {
    if (!this.ros.connected) return;
    const [nets, addrs, pools, detect] = await Promise.allSettled([
      this.ros.write('/ip/dhcp-server/network/print'),
      this.ros.write('/ip/address/print'),
      this.ros.write('/ip/pool/print', ['=.proplist=name,ranges']),
      this.ros.write('/interface/detect-internet/state/print'),
    ]);
    const netRows    = nets.status    === 'fulfilled' ? (nets.value    || []) : [];
    const addrRows   = addrs.status   === 'fulfilled' ? (addrs.value   || []) : [];
    const poolRows   = pools.status   === 'fulfilled' ? (pools.value   || []) : [];
    const detectRows = detect.status  === 'fulfilled' ? (detect.value  || []) : [];

    // Interfaces that have confirmed internet connectivity, with their assigned IP
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

    // All lease IPs (all statuses) for counting total leases per subnet.
    const allLeaseIps = this.dhcpLeases ? this.dhcpLeases.getAllLeaseIPs() : [];

    const lanCidrs = [];
    const networks = [];

    for (const n of netRows) {
      if (!n.address) continue;
      lanCidrs.push(n.address);

      // Count all leases in this subnet regardless of status
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

  _scheduleNext() {
    if (this.timer) return;
    this.timer = setTimeout(async () => {
      this.timer = null;
      if (!this._inflight) {
        this._inflight = true;
        try { await this.tick(); } catch (e) { console.error(this._lbl, e && e.message ? e.message : e); }
        finally { this._inflight = false; }
      }
      this._scheduleNext();
    }, this.pollMs);
  }

  _restartTimer() {
    if (this.timer) { clearTimeout(this.timer); this.timer = null; }
    if (this.ros.connected) this._scheduleNext();
  }

  stop() {
    if (this.timer) { clearTimeout(this.timer); this.timer = null; }
    this._inflight = false;
  }

  start() {
    const runFirst = async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(); } catch (e) { console.error(this._lbl, e && e.message ? e.message : e); }
      finally { this._inflight = false; }
    };
    runFirst();
    this._scheduleNext();
    this.ros.on('close', () => this.stop());
    this.ros.on('connected', () => { this._lastFp = ''; this.stop(); runFirst(); this._scheduleNext(); });
  }
}

module.exports = DhcpNetworksCollector;
