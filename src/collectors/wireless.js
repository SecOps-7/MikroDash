class WirelessCollector {
  constructor({ ros, io, pollMs, state, dhcpLeases, arp }) {
    this.ros = ros;
    this.io = io;
    this.pollMs = pollMs || 5000;
    this.state = state;
    this.dhcpLeases = dhcpLeases;
    this.arp = arp;
    this.mode = null;
    this.timer = null;
  }

  resolveName(mac) {
    if (!mac) return '';
    const byMac = this.dhcpLeases ? this.dhcpLeases.getNameByMAC(mac) : null;
    return (byMac && byMac.name) ? byMac.name : '';
  }

  async tick() {
    if (!this.ros.connected) return;
    let clients = [], detectedMode = this.mode;

    // Probe both APIs concurrently — node-routeros handles it fine
    if (detectedMode === 'wifi' || detectedMode === null) {
      try {
        const res = await this.ros.write('/interface/wifi/registration-table/print');
        if (res && res.length) { clients = res; detectedMode = 'wifi'; }
      } catch (_) {}
    }
    if (!clients.length && (detectedMode === 'wireless' || detectedMode === null)) {
      try {
        const res = await this.ros.write('/interface/wireless/registration-table/print');
        if (res && res.length) { clients = res; detectedMode = 'wireless'; }
      } catch (_) {}
    }

    // Lock in the detected mode so we stop probing the wrong API
    if (detectedMode) this.mode = detectedMode;

    const parsed = clients.map(c => {
      const mac    = c['mac-address'] || c.mac || '';
      const signal = parseInt(c.signal || c['signal-strength'] || c['rx-signal'] || '0', 10);
      const iface  = c.interface || c['ap-interface'] || '';
      const txRate = c['tx-rate'] || c['tx-rate-set'] || '';
      // Derive band from interface name or tx-rate string
      let band = '';
      const ifLow = iface.toLowerCase(), txLow = txRate.toLowerCase();
      if (/wifi[12]|wlan[12]|5g|5ghz|ax5/.test(ifLow) || /[MW]HT-[4-9]\d{2}|[MW]HT-[1-9]\d{3}|HE-MCS/.test(txRate)) band = '5GHz';
      else if (/wifi[34]|6g|6ghz|ax6/.test(ifLow)) band = '6GHz';
      else if (txRate) band = '2.4GHz';
      // IP from ARP reverse lookup
      const arpEntry = this.arp ? this.arp.getByMAC(mac) : null;
      const ip = arpEntry ? arpEntry.ip : '';
      return {
        mac, signal, iface, txRate, band, ip,
        rxRate: c['rx-rate'] || '',
        uptime: c.uptime || '',
        ssid:   c.ssid   || '',
        name:   this.resolveName(mac),
      };
    }).sort((a, b) => b.signal - a.signal);

    // Always emit — even with zero clients — so the stale timer is refreshed
    const payload = { ts: Date.now(), clients: parsed, mode: this.mode || 'none' };
    this.lastPayload = payload;
    this.io.emit('wireless:update', payload);
    this.state.lastWirelessTs = Date.now();
    this.state.lastWirelessErr = null;
  }

  start() {
    const run = async () => {
      try { await this.tick(); } catch (e) {
        this.state.lastWirelessErr = String(e && e.message ? e.message : e);
        console.error('[wireless]', this.state.lastWirelessErr);
      }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close', () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => { this.mode = null; this.timer = this.timer || setInterval(run, this.pollMs); run(); });
  }
}

module.exports = WirelessCollector;
