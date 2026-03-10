const ipaddr = require('ipaddr.js');

function ipInCidr(ip, cidr) {
  try { return ipaddr.parse(ip).match(ipaddr.parseCIDR(cidr)); } catch { return false; }
}

class DhcpNetworksCollector {
  constructor({ ros, io, pollMs, dhcpLeases, state, wanIface }) {
    this.ros = ros;
    this.io = io;
    this.pollMs = pollMs;
    this.dhcpLeases = dhcpLeases;
    this.state = state;
    this.wanIface = wanIface || 'WAN1';
    this.lanCidrs = [];
    this.networks = [];
    this.timer = null;
    this._inflight = false;
  }

  getLanCidrs() { return this.lanCidrs; }

  async tick() {
    if (!this.ros.connected) return;
    const [nets, addrs] = await Promise.allSettled([
      this.ros.write('/ip/dhcp-server/network/print'),
      this.ros.write('/ip/address/print'),
    ]);
    const netRows  = nets.status  === 'fulfilled' ? (nets.value  || []) : [];
    const addrRows = addrs.status === 'fulfilled' ? (addrs.value || []) : [];

    const wanIface = this.wanIface;
    let wanIp = '';
    for (const a of addrRows) {
      if (a.interface === wanIface && a.address) { wanIp = a.address; break; }
    }

    const leaseIps = this.dhcpLeases ? this.dhcpLeases.getActiveLeaseIPs() : [];
    const lanCidrs = [];
    const networks = [];
    for (const n of netRows) {
      if (!n.address) continue;
      lanCidrs.push(n.address);
      const leaseCount = leaseIps.reduce((acc, ip) => acc + (ipInCidr(ip, n.address) ? 1 : 0), 0);
      networks.push({ cidr: n.address, gateway: n.gateway || '', dns: n['dns-server'] || n['dns'] || '', leaseCount });
    }
    this.lanCidrs = Array.from(new Set(lanCidrs));
    this.networks = networks;
    if (this.state) this.state.lastWanIp = wanIp;
    this.io.emit('lan:overview', { ts: Date.now(), lanCidrs: this.lanCidrs, networks: this.networks, wanIp });
    this.state.lastNetworksTs = Date.now();
  }

  start() {
    const run = async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(); } catch (e) { console.error('[dhcp-networks]', e && e.message ? e.message : e); }
      finally { this._inflight = false; }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close', () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => { this.timer = this.timer || setInterval(run, this.pollMs); run(); });
  }
}

module.exports = DhcpNetworksCollector;
