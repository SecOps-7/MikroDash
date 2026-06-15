/**
 * Wireless collector — polls /interface/wifi/registration-table/print (wifi
 * package, ROS 7) or /interface/wireless/registration-table/print (legacy).
 *
 * RouterOS wifi-qcom devices (hAP ax2, hAP AX³) send the registration table
 * as separate response blocks per interface, each with its own !done. The
 * node-routeros library is patched (patch-routeros.js MULTI_BLOCK) to
 * accumulate all blocks with a 20 ms debounce before resolving, so a single
 * combined query correctly returns all clients across all interfaces.
 *
 * Guard strategy — per-MAC absence counter:
 *   Instead of replacing the entire client list each tick, we maintain the
 *   union of known clients. A client is removed only after it has been absent
 *   from ABSENCE_THRESHOLD consecutive ticks. New clients are added immediately.
 *   This eliminates the "collapse to subset" symptom without delaying legitimate
 *   disconnects — at 30 s poll intervals, 3 missed ticks = 90 s before a
 *   genuinely disconnected client disappears, which is acceptable for a dashboard.
 */
class WirelessCollector {
  constructor({ ros, io, pollMs, state, dhcpLeases, arp }) {
    this.ros        = ros;
    this.io         = io;
    const _wPoll = Number.isFinite(Number(pollMs)) ? Math.trunc(Number(pollMs)) : 5000;
    this.pollMs     = Math.max(500, Math.min(60000, _wPoll));
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
    this._capsmanAvailable = false;
    this._lbl = ros.routerLabel ? `[${ros.routerLabel}][wireless]` : '[wireless]';
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

    const dbg = this._debug;
    let rawClients = [], detectedMode = this.mode;

    if (detectedMode === 'wifi' || detectedMode === null) {
      try {
        const res = await this.ros.write('/interface/wifi/registration-table/print', [
          // No =.proplist= — on some ROS v7 builds, listing unknown/absent fields
          // in the proplist causes rows to be silently dropped rather than returned
          // with those fields empty. Omitting it guarantees all clients are returned.
          //
          // Multi-block response (wifi-qcom): RouterOS sends one !done per interface.
          // patch-routeros.js MULTI_BLOCK patches Channel.js to accumulate all blocks
          // with a 20 ms debounce before resolving, so all clients are captured here.
        ]);
        if (res && res.length) {
          rawClients = res;
          detectedMode = 'wifi';
          if (dbg) {
            const ifaceCounts = {};
            for (const c of res) { const k = c.interface || c['ap-interface'] || '(none)'; ifaceCounts[k] = (ifaceCounts[k] || 0) + 1; }
            console.log(this._lbl + ` wifi API: ${res.length} client(s) — by interface: ${JSON.stringify(ifaceCounts)}`);
            for (const c of res) {
              const mac   = c['mac-address'] || c.mac || '?';
              const iface = c.interface || c['ap-interface'] || '(none)';
              const band  = c['band'] || '(no band)';
              const ssid  = c.ssid  || '(no ssid)';
              const sig   = c.signal || c['signal-strength'] || c['rx-signal'] || '?';
              console.log(this._lbl + `   mac=${mac} iface=${iface} band=${band} ssid=${ssid} signal=${sig}`);
            }
          }
        } else if (dbg) {
          console.log(this._lbl + ' wifi API: 0 clients returned');
        }
      } catch (e) {
        if (dbg || (this.ros.cfg && this.ros.cfg.debug))
          console.warn(this._lbl + ' wifi API probe failed:', e && e.message ? e.message : e);
      }
    }
    if (!rawClients.length && (detectedMode === 'wireless' || detectedMode === null)) {
      try {
        const res = await this.ros.write('/interface/wireless/registration-table/print', [
          // No =.proplist= — same reason as above.
        ]);
        if (res && res.length) {
          rawClients = res;
          detectedMode = 'wireless';
          if (dbg) {
            const ifaceCounts = {};
            for (const c of res) { const k = c.interface || c['ap-interface'] || '(none)'; ifaceCounts[k] = (ifaceCounts[k] || 0) + 1; }
            console.log(this._lbl + ` legacy API: ${res.length} client(s) — by interface: ${JSON.stringify(ifaceCounts)}`);
          }
        } else if (dbg) {
          console.log(this._lbl + ' legacy API: 0 clients returned');
        }
      } catch (e) {
        if (dbg || (this.ros.cfg && this.ros.cfg.debug))
          console.warn(this._lbl + ' legacy API probe failed:', e && e.message ? e.message : e);
      }
    }

    if (detectedMode) this.mode = detectedMode;

    if (this._capsmanAvailable) {
      try {
        const caps = await this.ros.write('/caps-man/registration-table/print', []);
        // Skip CAPsMAN rows whose MAC is already present from local wireless — local wins.
        const seenMacs = new Set(rawClients.map(c => c['mac-address'] || c.mac || '').filter(Boolean));
        for (const c of (caps || [])) {
          const mac = c['mac-address'] || '';
          if (mac && !seenMacs.has(mac)) rawClients.push({ ...c, _capsman: true });
        }
        if (dbg && caps && caps.length) console.log(this._lbl + ` capsman: ${caps.length} client(s)`);
      } catch (e) {
        if (dbg) console.warn(this._lbl + ' capsman tick failed:', e && e.message ? e.message : e);
      }
    }

    // Drop rows that lack wireless-specific fields — these are interface metadata
    // rows (including Ethernet) returned by some RouterOS builds in error.
    // Every legitimately connected wireless client has at least one of these fields.
    rawClients = rawClients.filter(function(c) {
      return c.signal || c['signal-strength'] || c['rx-signal'] ||
             c.ssid   || c['tx-rate']         || c['rx-rate']  || c['tx-rate-set'];
    });

    // ── Per-MAC absence guard ─────────────────────────────────────────────────
    // Parse this tick's results into a MAC-keyed map.
    const thisTickByMac = new Map();
    for (const c of rawClients) {
      const mac = c['mac-address'] || c.mac || '';
      if (mac) thisTickByMac.set(mac, c);
    }

    // Partial-result detection — same heuristic as connTableCache (#29).
    // On wifi-qcom devices (hAP ax2, hAP AX³ etc.) with virtual APs, the
    // registration-table API can consistently return only the virtual AP's
    // clients while physical radio clients are temporarily unavailable. When
    // that happens on most ticks the ABSENCE_THRESHOLD guard is eventually
    // exhausted and physical-radio clients are removed. Fix: if this tick
    // returned > 0 clients but < 50% of what we know, treat it as a partial
    // result and skip the absence-aging step entirely for this tick.
    const PARTIAL_RATIO = 0.5;
    const PARTIAL_MIN   = 3;   // only guard when we have enough known clients
    const mightBePartial = (
      this._knownClients.size >= PARTIAL_MIN &&
      thisTickByMac.size > 0 &&
      thisTickByMac.size < this._knownClients.size * PARTIAL_RATIO
    );
    if (dbg && mightBePartial) {
      console.warn(this._lbl + ` partial result suspected — ${thisTickByMac.size} from API vs ${this._knownClients.size} known — skipping absence aging this tick`);
    }

    // 1. Add or refresh clients that ARE present this tick.
    for (const [mac, c] of thisTickByMac) {
      this._absentTicks.delete(mac);   // reset absence counter
      const signal  = parseInt(c.signal || c['signal-strength'] || c['rx-signal'] || '0', 10);
      const iface   = c.interface || c['ap-interface'] || '';
      const txRate  = c['tx-rate'] || c['tx-rate-set'] || '';
      const rawBand = (c['band'] || '').toLowerCase();
      let band = '';
      if (c._capsman && !rawBand) {
        const il = iface.toLowerCase();
        if      (il.endsWith('-2g') || il.includes('2ghz')) band = '2.4GHz';
        else if (il.endsWith('-5g') || il.includes('5ghz')) band = '5GHz';
        else if (il.endsWith('-6g') || il.includes('6ghz')) band = '6GHz';
      } else {
        if      (rawBand.includes('6')) band = '6GHz';
        else if (rawBand.includes('5')) band = '5GHz';
        else if (rawBand.includes('2')) band = '2.4GHz';
      }
      const arpEntry = this.arp ? this.arp.getByMAC(mac) : null;
      const ip       = arpEntry ? arpEntry.ip : '';
      this._knownClients.set(mac, {
        mac, signal, iface, txRate, band, ip,
        rxRate:  c['rx-rate'] || '',
        uptime:  c.uptime || '',
        ssid:    c.ssid   || '',
        name:    this.resolveName(mac),
        source:  c._capsman ? 'capsman' : undefined,
      });
    }

    // 2. Age out clients NOT present this tick — skipped when result looks partial.
    //    Remove them only once they've been absent for ABSENCE_THRESHOLD ticks.
    if (!mightBePartial) {
      for (const mac of [...this._knownClients.keys()]) {
        if (thisTickByMac.has(mac)) continue;
        const absent = (this._absentTicks.get(mac) || 0) + 1;
        if (absent >= this.ABSENCE_THRESHOLD) {
          if (dbg) console.log(this._lbl + ` removing ${mac} — absent ${absent} ticks (>= threshold ${this.ABSENCE_THRESHOLD})`);
          this._knownClients.delete(mac);
          this._absentTicks.delete(mac);
          this._nameCache.delete(mac);
        } else {
          if (dbg) console.log(this._lbl + ` holding ${mac} — absent ${absent}/${this.ABSENCE_THRESHOLD} ticks`);
          this._absentTicks.set(mac, absent);
        }
      }
    }

    if (dbg) {
      console.log(this._lbl + ` tick summary: ${thisTickByMac.size} from API, ${this._knownClients.size} known, ${this._absentTicks.size} held by absence guard${mightBePartial ? ' [partial — aging skipped]' : ''}`);
    }

    // 3. Build the sorted client array from the stable known-clients map.
    const parsed = Array.from(this._knownClients.values())
      .sort((a, b) => b.signal - a.signal);

    const fp = JSON.stringify(parsed.map(c => ({
      mac: c.mac, signal: c.signal, iface: c.iface, band: c.band, name: c.name,
    })));
    const payload = { ts: Date.now(), clients: parsed, mode: this.mode || 'none', pollMs: this.pollMs, capsmanAvailable: this._capsmanAvailable };
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
    this.mode              = null;
    this._lastFp           = '';
    this._capsmanAvailable = false;
    this._nameCache.clear();
    this._knownClients.clear();
    this._absentTicks.clear();
    if (this._retryTimer) { clearTimeout(this._retryTimer); this._retryTimer = null; }
  }

  async _probeCAPsMAN() {
    try {
      await this.ros.write('/caps-man/registration-table/print', []);
      this._capsmanAvailable = true;
      if (this._debug) console.log(this._lbl + ' capsman probe: available');
    } catch (e) {
      const msg = String(e && e.message ? e.message : e);
      if (msg.includes('unknown command') || msg.includes('no such')) {
        this._capsmanAvailable = false;
        if (this._debug) console.log(this._lbl + ' capsman probe: not available on this router');
      }
      // transient errors (timeout, connection drop) leave _capsmanAvailable = false
      // and will be re-probed on the next reconnect
    }
  }

  _scheduleNext() {
    if (this.timer) return;
    this.timer = setTimeout(async () => {
      this.timer = null;
      if (!this._inflight) {
        this._inflight = true;
        try { await this.tick(); } catch (e) {
          this.state.lastWirelessErr = String(e && e.message ? e.message : e);
          console.error(this._lbl, this.state.lastWirelessErr);
        } finally { this._inflight = false; }
      }
      this._scheduleNext();
    }, this.pollMs); // codeql[js/resource-exhaustion]
  }

  _restartTimer() {
    if (this.timer) { clearTimeout(this.timer); this.timer = null; }
    if (this.ros.connected) this._scheduleNext();
  }

  start() {
    this._debug = require('../settings').load().rosDebug;
    const runFirst = async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(true); } catch (e) {
        this.state.lastWirelessErr = String(e && e.message ? e.message : e);
        console.error(this._lbl, this.state.lastWirelessErr);
      } finally { this._inflight = false; }
    };
    runFirst();
    this._probeCAPsMAN();
    this._scheduleNext();
    this.ros.on('close', () => this.stop());
    this.ros.on('connected', () => {
      this.stop();
      this._resetState();
      runFirst();
      this._probeCAPsMAN();
      this._scheduleNext();
    });
  }

  suspend() {
    if (this.timer) { clearTimeout(this.timer); this.timer = null; }
  }

  resume() {
    if (this.ros.connected) this._scheduleNext();
  }

  stop() {
    if (this.timer)       { clearTimeout(this.timer);        this.timer      = null; }
    if (this._retryTimer) { clearTimeout(this._retryTimer);  this._retryTimer = null; }
    this._inflight = false;
  }
}

module.exports = WirelessCollector;
