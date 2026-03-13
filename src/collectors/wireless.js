class WirelessCollector {
  constructor({ ros, io, pollMs, state, dhcpLeases, arp }) {
    this.ros = ros;
    this.io = io;
    this.pollMs = pollMs || 5000;
    this.state = state;
    this.dhcpLeases = dhcpLeases;
    this.arp = arp;
    this.mode = null;
    this._lastFp = '';
    this.timer = null;
    this._inflight = false;
    this._nameCache = new Map();
  }

  resolveName(mac) {
    if (!mac) return '';
    // _nameCache persists between ticks — MAC→name is stable until lease changes
    if (this._nameCache.has(mac)) return this._nameCache.get(mac);
    const byMac = this.dhcpLeases ? this.dhcpLeases.getNameByMAC(mac) : null;
    const name = (byMac && byMac.name) ? byMac.name : '';
    this._nameCache.set(mac, name);
    return name;
  }

  async tick() {
    if (!this.ros.connected) return;
    let clients = [], detectedMode = this.mode;

    // Probe both APIs concurrently — node-routeros handles it fine
    if (detectedMode === 'wifi' || detectedMode === null) {
      try {
        const res = await this.ros.write('/interface/wifi/registration-table/print');
        if (res && res.length) { clients = res; detectedMode = 'wifi'; }
      } catch (e) {
        if (this.ros.cfg && this.ros.cfg.debug) console.warn('[wireless] wifi API probe failed:', e && e.message ? e.message : e);
      }
    }
    if (!clients.length && (detectedMode === 'wireless' || detectedMode === null)) {
      try {
        const res = await this.ros.write('/interface/wireless/registration-table/print');
        if (res && res.length) { clients = res; detectedMode = 'wireless'; }
      } catch (e) {
        if (this.ros.cfg && this.ros.cfg.debug) console.warn('[wireless] legacy API probe failed:', e && e.message ? e.message : e);
      }
    }

    // Lock in the detected mode so we stop probing the wrong API
    if (detectedMode) this.mode = detectedMode;

    const parsed = clients.map(c => {
      const mac    = c['mac-address'] || c.mac || '';
      const signal = parseInt(c.signal || c['signal-strength'] || c['rx-signal'] || '0', 10);
      const iface  = c.interface || c['ap-interface'] || '';
      const txRate = c['tx-rate'] || c['tx-rate-set'] || '';
      // Band: read directly from the registration table 'band' field (same source as Winbox)
      const rawBand = (c['band'] || '').toLowerCase();
      let band = '';
      if      (rawBand.includes('6'))  band = '6GHz';
      else if (rawBand.includes('5'))  band = '5GHz';
      else if (rawBand.includes('2'))  band = '2.4GHz';
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

    const fp = JSON.stringify(parsed.map(c=>({mac:c.mac,signal:c.signal,iface:c.iface,band:c.band})));
    const payload = { ts: Date.now(), clients: parsed, mode: this.mode || 'none', pollMs: this.pollMs };
    this.lastPayload = payload;
    // Always update lastPayload and stale state; only suppress the socket emit when data is identical
    this.state.lastWirelessTs = Date.now();
    if (fp !== this._lastFp) { this._lastFp = fp; this.io.emit('wireless:update', payload); }
    this.state.lastWirelessErr = null;
  }

  start() {
    const run = async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(); } catch (e) {
        this.state.lastWirelessErr = String(e && e.message ? e.message : e);
        console.error('[wireless]', this.state.lastWirelessErr);
      } finally { this._inflight = false; }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close', () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => { this.mode = null; this._lastFp = ''; this._nameCache.clear(); this.timer = this.timer || setInterval(run, this.pollMs); run(); });
  }
}

module.exports = WirelessCollector;
