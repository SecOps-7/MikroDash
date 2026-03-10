/**
 * DHCP Leases — streams /ip/dhcp-server/lease/listen for instant updates,
 * with a one-shot /print on startup to populate the initial state.
 */
class DhcpLeasesCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros = ros;
    this.io = io;
    this.pollMs = pollMs;
    this.state = state;
    this.byIP  = new Map();
    this.byMAC = new Map();
    this.seenMACs = new Set();
    this.stream = null;
    this._restarting = false;
    this._restartTimer = null;
  }

  getNameByIP(ip)  { return this.byIP.get(ip);  }
  getNameByMAC(mac){ return this.byMAC.get(mac); }

  getActiveLeaseIPs() {
    const out = [];
    for (const [ip, v] of this.byIP.entries()) {
      const st = String(v.status || '').toLowerCase();
      if (!st || st === 'bound' || st === 'offered') out.push(ip);
    }
    return out;
  }

  _applyLease(l) {
    const ip   = l.address || l['active-address'];
    const mac  = l['mac-address'] || l['active-mac-address'] || l.mac;
    const name = (l.comment && l.comment.trim()) ? l.comment.trim()
               : (l['host-name'] && l['host-name'].trim()) ? l['host-name'].trim() : '';
    const status = l.status || '';

    if (ip) this.byIP.set(ip, { name, mac, hostName: l['host-name'] || '', comment: l.comment || '', status });
    if (mac) this.byMAC.set(mac, { name, ip });

    if (mac && ip && !this.seenMACs.has(mac)) {
      this.seenMACs.add(mac);
      this.io.emit('device:new', { ts: Date.now(), ip, mac, name: name || ('Unknown (' + mac + ')'), source: 'dhcp-lease' });
    }
  }

  async _loadInitial() {
    try {
      const leases = await this.ros.write('/ip/dhcp-server/lease/print');
      for (const l of (leases || [])) this._applyLease(l);
      this.state.lastLeasesTs = Date.now();
    } catch (e) {
      console.error('[leases] initial load failed:', e && e.message ? e.message : e);
    }
  }

  _startStream() {
    if (this.stream) return;
    if (!this.ros.connected) return;
    try {
      this.stream = this.ros.stream(['/ip/dhcp-server/lease/listen'], (err, data) => {
        if (err) {
          console.error('[leases] stream error:', err && err.message ? err.message : err);
          this._stopStream();
          if (this.ros.connected && !this._restarting) {
            this._restarting = true;
            this._restartTimer = setTimeout(() => {
              this._restarting = false;
              this._restartTimer = null;
              if (this.ros.connected) this._startStream();
            }, 2000);
          }
          return;
        }
        if (data) { this._applyLease(data); this.state.lastLeasesTs = Date.now(); }
      });
      console.log('[leases] streaming /ip/dhcp-server/lease/listen');
    } catch (e) {
      console.error('[leases] stream start failed:', e && e.message ? e.message : e);
    }
  }

  _stopStream() {
    if (this._restartTimer) { clearTimeout(this._restartTimer); this._restartTimer = null; }
    this._restarting = false;
    if (this.stream) { try { this.stream.stop(); } catch (_) {} this.stream = null; }
  }

  async start() {
    await this._loadInitial();
    this._startStream();
    this.ros.on('connected', async () => {
      this._stopStream();
      await this._loadInitial();
      this._startStream();
    });
    this.ros.on('close', () => this._stopStream());
  }
}

module.exports = DhcpLeasesCollector;
