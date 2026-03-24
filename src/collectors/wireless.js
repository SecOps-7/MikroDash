/**
 * Wireless collector — polls /interface/wifi/registration-table/print (wifi
 * package, ROS 7) or /interface/wireless/registration-table/print (legacy).
 *
 * RouterOS — particularly the wifi-qcom driver used on HAPax2 and similar
 * boards — can return partial registration-table results for several ticks
 * during client re-association. A single call that normally returns N clients
 * may return only a subset (e.g. only the virtual-AP clients) while the
 * physical radios are briefly reassociating. Accepting each tick's result
 * verbatim would cause the table to flash between the full set and the partial
 * set on every poll cycle.
 *
 * Guard strategy — per-MAC absence counter:
 *   Instead of replacing the entire client list each tick, we maintain the
 *   union of known clients. A client is removed only after it has been absent
 *   from ABSENCE_THRESHOLD consecutive ticks. New clients are added immediately.
 *   This eliminates both the "collapse to subset" and "flash full then collapse"
 *   symptoms without delaying legitimate disconnects — at 30 s poll intervals,
 *   3 missed ticks = 90 s before a genuinely disconnected client disappears,
 *   which is acceptable for a dashboard. At faster poll rates the window shrinks.
 */
class WirelessCollector {
  constructor({ ros, io, pollMs, state, dhcpLeases, arp }) {
    this.ros        = ros;
    this.io         = io;
    this.pollMs     = pollMs || 5000;
    this.state      = state;
    this.dhcpLeases = dhcpLeases;
    this.arp        = arp;
    this.mode       = null;
    this._lastFp    = '';

    // Per-MAC absence counter.  mac -> number of consecutive ticks it was absent.
    // Clients absent for >= ABSENCE_THRESHOLD ticks are removed.
    this._absentTicks = new Map();
    this.ABSENCE_THRESHOLD = 3;

    // Stable client map: mac -> last-known parsed client object.
    // This is the source of truth for what we emit.
    this._knownClients = new Map();

    this.timer        = null;
    this._inflight    = false;
    this._nameCache   = new Map();
    this._retryTimer  = null;
  }

  resolveName(mac) {
    if (!mac) return '';
    if (this._nameCache.has(mac)) return this._nameCache.get(mac);
    const byMac = this.dhcpLeases ? this.dhcpLeases.getNameByMAC(mac) : null;
    const name  = (byMac && byMac.name) ? byMac.name : '';
    if (name) this._nameCache.set(mac, name);
    return name;
  }

  async tick(force = false) {
    if (!this.ros.connected) return;
    if (!force && this.io.engine.clientsCount === 0) return;

    let rawClients = [], detectedMode = this.mode;

    if (detectedMode === 'wifi' || detectedMode === null) {
      try {
        const res = await this.ros.write('/interface/wifi/registration-table/print', [
          // No =.proplist= — on some ROS v7 builds, listing unknown/absent fields
          // in the proplist causes rows to be silently dropped rather than returned
          // with those fields empty. Omitting it guarantees all clients are returned.
        ]);
        if (res && res.length) { rawClients = res; detectedMode = 'wifi'; }
      } catch (e) {
        if (this.ros.cfg && this.ros.cfg.debug)
          console.warn('[wireless] wifi API probe failed:', e && e.message ? e.message : e);
      }
    }
    if (!rawClients.length && (detectedMode === 'wireless' || detectedMode === null)) {
      try {
        const res = await this.ros.write('/interface/wireless/registration-table/print', [
          // No =.proplist= — same reason as above.
        ]);
        if (res && res.length) { rawClients = res; detectedMode = 'wireless'; }
      } catch (e) {
        if (this.ros.cfg && this.ros.cfg.debug)
          console.warn('[wireless] legacy API probe failed:', e && e.message ? e.message : e);
      }
    }

    if (detectedMode) this.mode = detectedMode;

    // ── Per-MAC absence guard ─────────────────────────────────────────────────
    // Parse this tick's results into a MAC-keyed map.
    const thisTickByMac = new Map();
    for (const c of rawClients) {
      const mac = c['mac-address'] || c.mac || '';
      if (mac) thisTickByMac.set(mac, c);
    }

    // 1. Add or refresh clients that ARE present this tick.
    for (const [mac, c] of thisTickByMac) {
      this._absentTicks.delete(mac);   // reset absence counter
      const signal  = parseInt(c.signal || c['signal-strength'] || c['rx-signal'] || '0', 10);
      const iface   = c.interface || c['ap-interface'] || '';
      const txRate  = c['tx-rate'] || c['tx-rate-set'] || '';
      const rawBand = (c['band'] || '').toLowerCase();
      let band = '';
      if      (rawBand.includes('6')) band = '6GHz';
      else if (rawBand.includes('5')) band = '5GHz';
      else if (rawBand.includes('2')) band = '2.4GHz';
      const arpEntry = this.arp ? this.arp.getByMAC(mac) : null;
      const ip       = arpEntry ? arpEntry.ip : '';
      this._knownClients.set(mac, {
        mac, signal, iface, txRate, band, ip,
        rxRate: c['rx-rate'] || '',
        uptime: c.uptime || '',
        ssid:   c.ssid   || '',
        name:   this.resolveName(mac),
      });
    }

    // 2. Increment absence counter for clients NOT present this tick.
    //    Remove them only once they've been absent for ABSENCE_THRESHOLD ticks.
    for (const mac of this._knownClients.keys()) {
      if (thisTickByMac.has(mac)) continue;
      const absent = (this._absentTicks.get(mac) || 0) + 1;
      if (absent >= this.ABSENCE_THRESHOLD) {
        this._knownClients.delete(mac);
        this._absentTicks.delete(mac);
        this._nameCache.delete(mac);
      } else {
        this._absentTicks.set(mac, absent);
      }
    }

    // 3. Build the sorted client array from the stable known-clients map.
    const parsed = Array.from(this._knownClients.values())
      .sort((a, b) => b.signal - a.signal);

    const fp = JSON.stringify(parsed.map(c => ({
      mac: c.mac, signal: c.signal, iface: c.iface, band: c.band, name: c.name,
    })));
    const payload = { ts: Date.now(), clients: parsed, mode: this.mode || 'none', pollMs: this.pollMs };
    this.lastPayload        = payload;
    this.state.lastWirelessTs  = Date.now();
    this.state.lastWirelessErr = null;
    if (fp !== this._lastFp) { this._lastFp = fp; this.io.emit('wireless:update', payload); }

    // If any client is still missing a name (DHCP not yet loaded at startup),
    // schedule a re-resolve using already-fetched data — no extra API call.
    const hasUnnamed = parsed.length > 0 && parsed.some(c => !c.name);
    if (hasUnnamed && !this._retryTimer) {
      const tryResolve = () => {
        this._retryTimer = null;
        if (!this.ros.connected) return;
        let changed = false;
        for (const [mac, client] of this._knownClients) {
          if (!client.name) {
            const name = this.resolveName(mac);
            if (name) { this._knownClients.set(mac, { ...client, name }); changed = true; }
          }
        }
        if (changed) {
          const reParsed  = Array.from(this._knownClients.values()).sort((a, b) => b.signal - a.signal);
          const newFp     = JSON.stringify(reParsed.map(c => ({ mac: c.mac, signal: c.signal, iface: c.iface, band: c.band, name: c.name })));
          if (newFp !== this._lastFp) {
            const newPayload = { ...this.lastPayload, ts: Date.now(), clients: reParsed };
            this.lastPayload = newPayload;
            this._lastFp     = newFp;
            this.io.emit('wireless:update', newPayload);
          }
        }
        // Keep retrying until all names are resolved
        if (Array.from(this._knownClients.values()).some(c => !c.name)) {
          this._retryTimer = setTimeout(tryResolve, 500);
        }
      };
      this._retryTimer = setTimeout(tryResolve, 500);
    }
  }

  _resetState() {
    this.mode     = null;
    this._lastFp  = '';
    this._nameCache.clear();
    this._knownClients.clear();
    this._absentTicks.clear();
    if (this._retryTimer) { clearTimeout(this._retryTimer); this._retryTimer = null; }
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
      this.stop();
      this._resetState();
      runFirst();
      this.timer = setInterval(run, this.pollMs);
    });
  }

  stop() {
    if (this.timer)       { clearInterval(this.timer);       this.timer      = null; }
    if (this._retryTimer) { clearTimeout(this._retryTimer);  this._retryTimer = null; }
  }
}

module.exports = WirelessCollector;
