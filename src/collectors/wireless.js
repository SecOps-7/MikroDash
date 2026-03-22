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
    this._emptyTicks = 0;
    this.timer = null;
    this._inflight = false;
    this._nameCache = new Map();
    this._retryTimer = null;  // one-shot re-tick to pick up DHCP names after startup
  }

  resolveName(mac) {
    if (!mac) return '';
    // _nameCache persists between ticks — MAC→name is stable once resolved.
    // Only cache non-empty names: if DHCP hasn't loaded yet the lookup returns
    // '' and we must not lock that in — the next tick should retry.
    if (this._nameCache.has(mac)) return this._nameCache.get(mac);
    const byMac = this.dhcpLeases ? this.dhcpLeases.getNameByMAC(mac) : null;
    const name = (byMac && byMac.name) ? byMac.name : '';
    if (name) this._nameCache.set(mac, name);
    return name;
  }

  async tick(force = false) {
    if (!this.ros.connected) return;
    // Skip when no browser clients are connected — wifi API probe is wasted
    // work if nobody is watching the dashboard.
    if (!force && this.io.engine.clientsCount === 0) return;
    let clients = [], detectedMode = this.mode;

    // Probe both APIs concurrently — node-routeros handles it fine
    if (detectedMode === 'wifi' || detectedMode === null) {
      try {
        const res = await this.ros.write('/interface/wifi/registration-table/print', [
        // No =.proplist= here: on some RouterOS v7 builds, including unknown or
          // absent fields in the proplist for registration-table causes rows to be
          // filtered rather than just having those fields omitted — resulting in
          // only 1 of N clients being returned. The table is small so omitting
          // the proplist optimisation has no meaningful performance impact.
        ]);
        if (res && res.length) { clients = res; detectedMode = 'wifi'; }
      } catch (e) {
        if (this.ros.cfg && this.ros.cfg.debug) console.warn('[wireless] wifi API probe failed:', e && e.message ? e.message : e);
      }
    }
    if (!clients.length && (detectedMode === 'wireless' || detectedMode === null)) {
      try {
        const res = await this.ros.write('/interface/wireless/registration-table/print', [
          // No =.proplist= — same reason as above; legacy wireless API also varies
          // in field availability across RouterOS versions.
        ]);
        if (res && res.length) { clients = res; detectedMode = 'wireless'; }
      } catch (e) {
        if (this.ros.cfg && this.ros.cfg.debug) console.warn('[wireless] legacy API probe failed:', e && e.message ? e.message : e);
      }
    }

    // Lock in the detected mode so we stop probing the wrong API
    if (detectedMode) this.mode = detectedMode;

    // Guard against transient empty responses from RouterOS. On some firmware
    // builds — particularly the new wifi package — the registration table briefly
    // clears during client re-association or internal table refreshes, returning
    // zero rows for 1–2 ticks before repopulating. At fast poll intervals this
    // window is hit regularly. We tolerate up to 2 consecutive empty ticks by
    // holding the last known state; only a third consecutive empty is treated as
    // authoritative (all clients genuinely disconnected). Counter resets to zero
    // whenever a non-empty result arrives.
    if (clients.length === 0 && this.lastPayload && this.lastPayload.clients && this.lastPayload.clients.length > 0) {
      this._emptyTicks = (this._emptyTicks || 0) + 1;
      if (this._emptyTicks <= 2) return; // transient — hold last known state
    } else {
      this._emptyTicks = 0;
    }

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

    const fp = JSON.stringify(parsed.map(c=>({mac:c.mac,signal:c.signal,iface:c.iface,band:c.band,name:c.name})));
    const payload = { ts: Date.now(), clients: parsed, mode: this.mode || 'none', pollMs: this.pollMs };
    this.lastPayload = payload;
    // Always update lastPayload and stale state; only suppress the socket emit when data is identical
    this.state.lastWirelessTs = Date.now();
    if (fp !== this._lastFp) { this._lastFp = fp; this.io.emit('wireless:update', payload); }
    this.state.lastWirelessErr = null;

    // If any client is still missing a name (DHCP not yet loaded at startup),
    // schedule a one-shot re-resolve after 500 ms. Crucially, this re-uses the
    // already-fetched client list rather than making a second RouterOS API call —
    // some firmware builds return partial results when queried soon after startup.
    const hasUnnamed = clients.length > 0 && parsed.some(c => !c.name);
    if (hasUnnamed && !this._retryTimer) {
      const savedClients = clients.slice(); // snapshot the raw rows — no new API call
      const tryResolve = () => {
        this._retryTimer = null;
        if (!this.ros.connected) return;
        // Re-resolve names for every client using the same raw rows.
        const reParsed = savedClients.map(c => {
          const mac = c['mac-address'] || c.mac || '';
          const base = (this.lastPayload.clients || []).find(x => x.mac === mac) || {};
          return { ...base, name: this.resolveName(mac) };
        });
        const newFp = JSON.stringify(reParsed.map(c => ({mac:c.mac,signal:c.signal,iface:c.iface,band:c.band,name:c.name})));
        if (newFp !== this._lastFp) {
          const newPayload = { ...this.lastPayload, ts: Date.now(), clients: reParsed };
          this.lastPayload = newPayload;
          this._lastFp = newFp;
          this.io.emit('wireless:update', newPayload);
        }
        // Keep retrying every 500 ms until all names are resolved
        if (reParsed.some(c => !c.name)) {
          this._retryTimer = setTimeout(tryResolve, 500);
        }
      };
      this._retryTimer = setTimeout(tryResolve, 500);
    }
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
    // First tick runs unconditionally (force=true) so lastPayload is populated
    // before the first browser client connects, regardless of clientsCount.
    const runFirst = async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(true); } catch (e) {
        this.state.lastWirelessErr = String(e && e.message ? e.message : e);
        console.error('[wireless]', this.state.lastWirelessErr);
      } finally { this._inflight = false; }
    };
    runFirst();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close', () => this.stop());
    this.ros.on('connected', () => {
      this.mode = null; this._lastFp = ''; this._nameCache.clear(); this._emptyTicks = 0;
      if (this._retryTimer) { clearTimeout(this._retryTimer); this._retryTimer = null; }
      this.timer = this.timer || setInterval(run, this.pollMs); runFirst();
    });
  }

  stop() {
    if (this.timer) { clearInterval(this.timer); this.timer = null; }
    if (this._retryTimer) { clearTimeout(this._retryTimer); this._retryTimer = null; }
  }
}

module.exports = WirelessCollector;
