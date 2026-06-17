/**
 * Wireless collector — streams /interface/wifi/registration-table/print (wifi
 * package, ROS 7) or /interface/wireless/registration-table/print (legacy).
 *
 * Mode detection: start wifi stream first. If the first batch is empty, latch
 * to 'wireless' mode, stop the wifi stream, start the wireless stream. Once
 * latched, mode never changes until reconnect.
 *
 * CAPsMAN stream runs independently when available. It fires on the same
 * interval and its clients are merged after each wifi/wireless batch — local
 * clients always win on MAC conflicts.
 *
 * Guard strategy — per-MAC absence counter:
 *   A client is removed only after ABSENCE_THRESHOLD consecutive batches where
 *   it is absent. New clients are added immediately. This eliminates the
 *   "collapse to subset" symptom from wifi-qcom partial results without
 *   delaying legitimate disconnects.
 *
 * Stream idle teardown: suspend() stops all streams; resume() restarts the
 * correct stream for the latched mode. RouterOS does no work while idle.
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

    this._absentTicks = new Map();
    this.ABSENCE_THRESHOLD = 3;
    this._knownClients = new Map();
    this._nameCache    = new Map();
    this._retryTimer   = null;
    this._capsmanAvailable = false;
    this._lbl = ros.routerLabel ? `[${ros.routerLabel}][wireless]` : '[wireless]';

    // Latest complete batches from each source
    this._lastWifiBatch    = [];
    this._lastCapsmanBatch = [];

    this._streams    = { wifi: null, wireless: null, capsman: null };
    this._batches    = { wifi: [], wireless: [], capsman: [] };
    this._debounces  = { wifi: null, wireless: null, capsman: null };
    this._restarting = { wifi: false, wireless: false, capsman: false };
    this._restartTimers = { wifi: null, wireless: null, capsman: null };
  }

  resolveName(mac) {
    if (!mac) return '';
    if (this._nameCache.has(mac)) return this._nameCache.get(mac);
    const byMac = this.dhcpLeases ? this.dhcpLeases.getNameByMAC(mac) : null;
    const name  = (byMac && byMac.name) ? byMac.name : '';
    if (name) this._nameCache.set(mac, name);
    return name;
  }

  // ── client parsing ────────────────────────────────────────────────────────

  _parseClient(c) {
    const mac     = c['mac-address'] || c.mac || '';
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
    return {
      mac, signal, iface, txRate, band, ip,
      rxRate:  c['rx-rate'] || '',
      uptime:  c.uptime || '',
      ssid:    c.ssid   || '',
      name:    this.resolveName(mac),
      source:  c._capsman ? 'capsman' : undefined,
    };
  }

  // ── absence guard and emit ────────────────────────────────────────────────

  _applyAbsenceGuard(rawClients) {
    const dbg = this._debug;

    // Drop rows that lack wireless-specific fields — these are interface metadata
    // rows (including Ethernet) returned by some RouterOS builds in error.
    rawClients = rawClients.filter(c =>
      c.signal || c['signal-strength'] || c['rx-signal'] ||
      c.ssid   || c['tx-rate']         || c['rx-rate']  || c['tx-rate-set']
    );

    const thisTickByMac = new Map();
    for (const c of rawClients) {
      const mac = c['mac-address'] || c.mac || '';
      if (mac) thisTickByMac.set(mac, c);
    }

    const PARTIAL_RATIO = 0.5;
    const PARTIAL_MIN   = 3;
    const nonCapsmanKnown = [...this._knownClients.values()].filter(c => c.source !== 'capsman').length;
    const nonCapsmanSeen  = [...thisTickByMac.values()].filter(c => !c._capsman).length;
    const mightBePartial  = (
      nonCapsmanKnown >= PARTIAL_MIN &&
      nonCapsmanSeen > 0 &&
      nonCapsmanSeen < nonCapsmanKnown * PARTIAL_RATIO
    );
    if (dbg && mightBePartial) {
      console.warn(this._lbl + ` partial result suspected — ${nonCapsmanSeen} from API vs ${nonCapsmanKnown} known — skipping absence aging`);
    }

    // 1. Add or refresh clients present in this batch
    for (const [mac, c] of thisTickByMac) {
      this._absentTicks.delete(mac);
      this._knownClients.set(mac, this._parseClient(c));
    }

    // 2. Age out non-capsman clients absent from this batch
    if (!mightBePartial) {
      for (const mac of [...this._knownClients.keys()]) {
        if (thisTickByMac.has(mac)) continue;
        const client = this._knownClients.get(mac);
        if (client && client.source === 'capsman') continue; // managed separately
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
      console.log(this._lbl + ` batch: ${thisTickByMac.size} from API, ${this._knownClients.size} known${mightBePartial ? ' [partial — aging skipped]' : ''}`);
    }
  }

  _emitClients() {
    const parsed = Array.from(this._knownClients.values())
      .sort((a, b) => b.signal - a.signal);

    const fp = JSON.stringify(parsed.map(c => ({
      mac: c.mac, signal: c.signal, iface: c.iface, band: c.band, name: c.name,
    })));
    const payload = {
      ts: Date.now(), clients: parsed, mode: this.mode || 'none',
      pollMs: this.pollMs, capsmanAvailable: this._capsmanAvailable,
    };
    this.lastPayload           = payload;
    this.state.lastWirelessTs  = Date.now();
    this.state.lastWirelessErr = null;
    if (fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.emit('wireless:update', payload);
    }

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
          const reParsed = Array.from(this._knownClients.values()).sort((a, b) => b.signal - a.signal);
          const newFp    = JSON.stringify(reParsed.map(c => ({ mac: c.mac, signal: c.signal, iface: c.iface, band: c.band, name: c.name })));
          if (newFp !== this._lastFp) {
            const newPayload = { ...this.lastPayload, ts: Date.now(), clients: reParsed };
            this.lastPayload = newPayload;
            this._lastFp     = newFp;
            this.io.emit('wireless:update', newPayload);
          }
        }
        if (Array.from(this._knownClients.values()).some(c => !c.name)) {
          this._retryTimer = setTimeout(tryResolve, 500);
        }
      };
      this._retryTimer = setTimeout(tryResolve, 500);
    }
  }

  // ── batch processing ──────────────────────────────────────────────────────

  _processMainBatch(records) {
    // Combine primary (wifi/wireless) with latest capsman; local wins on MAC
    const localMacs = new Set(records.map(c => c['mac-address'] || c.mac || '').filter(Boolean));
    const capsFiltered = this._lastCapsmanBatch
      .filter(c => { const mac = c['mac-address'] || c.mac || ''; return mac && !localMacs.has(mac); });
    this._applyAbsenceGuard([...records, ...capsFiltered]);
    this._emitClients();
  }

  _updateCapsmanClients() {
    // Remove stale capsman entries from known map
    for (const [mac, c] of this._knownClients) {
      if (c.source === 'capsman') this._knownClients.delete(mac);
    }
    // Add fresh capsman entries; skip MACs held by local wireless
    const localMacs = new Set([...this._knownClients.keys()]);
    for (const c of this._lastCapsmanBatch) {
      const mac = c['mac-address'] || c.mac || '';
      if (!mac || localMacs.has(mac)) continue;
      this._knownClients.set(mac, this._parseClient(c));
      this._absentTicks.delete(mac);
    }
  }

  _onBatch(type, records) {
    if (type === 'wifi') {
      if (this.mode === null) {
        if (records.length > 0) {
          this.mode = 'wifi';
          if (this._debug) console.log(this._lbl + ' mode latched: wifi');
        } else {
          // Empty first batch — wifi API not populated, fall through to legacy
          this.mode = 'wireless';
          if (this._debug) console.log(this._lbl + ' mode latched: wireless (wifi returned empty)');
          this._stopStream('wifi');
          this._startStream('wireless');
          return;
        }
      }
      this._lastWifiBatch = records;
      this._processMainBatch(records);
    } else if (type === 'wireless') {
      this._lastWifiBatch = records;
      this._processMainBatch(records);
    } else if (type === 'capsman') {
      this._lastCapsmanBatch = records.map(c => ({ ...c, _capsman: true }));
      this._updateCapsmanClients();
      this._emitClients();
    }
  }

  // ── stream management ────────────────────────────────────────────────────

  _startStream(type) {
    if (this._streams[type] || this._restarting[type]) return;
    if (!this.ros.connected) return;
    const intervalSec = Math.max(1, Math.round(this.pollMs / 1000));
    const endpoints = {
      wifi:     '/interface/wifi/registration-table/print',
      wireless: '/interface/wireless/registration-table/print',
      capsman:  '/caps-man/registration-table/print',
    };
    const stream = this.ros.stream([endpoints[type], `=interval=${intervalSec}`], null);
    this._streams[type] = stream;
    stream.on('data', (pkt) => {
      if (!pkt || typeof pkt !== 'object' || Array.isArray(pkt)) return;
      this._batches[type].push(pkt);
      if (this._debounces[type]) return;
      this._debounces[type] = setTimeout(() => { // codeql[js/resource-exhaustion]
        this._debounces[type] = null;
        const batch = this._batches[type];
        this._batches[type] = [];
        this._onBatch(type, batch);
      }, 50);
    });
    stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      // wifi "unknown command" → router uses legacy wireless API
      if (type === 'wifi' && (msg.includes('unknown command') || msg.includes('no such'))) {
        this._stopStream('wifi');
        if (this.mode === null) {
          this.mode = 'wireless';
          if (this._debug) console.log(this._lbl + ' mode latched: wireless (wifi command unknown)');
          this._startStream('wireless');
        }
        return;
      }
      console.error(this._lbl, `${type} stream error:`, msg);
      this.state.lastWirelessErr = msg;
      this._stopStream(type);
      if (this.ros.connected && !this._restarting[type]) {
        this._restarting[type] = true;
        this._restartTimers[type] = setTimeout(() => { // codeql[js/resource-exhaustion]
          this._restarting[type] = false;
          this._restartTimers[type] = null;
          this._startStream(type);
        }, 3000);
      }
    });
    console.log(this._lbl, `streaming ${endpoints[type]} interval=${intervalSec}s`);
  }

  _stopStream(type) {
    if (this._debounces[type])     { clearTimeout(this._debounces[type]);     this._debounces[type] = null; }
    if (this._restartTimers[type]) { clearTimeout(this._restartTimers[type]); this._restartTimers[type] = null; }
    this._restarting[type] = false;
    if (this._streams[type]) { try { this._streams[type].stop(); } catch (_) {} this._streams[type] = null; }
    this._batches[type] = [];
  }

  // ── state reset ───────────────────────────────────────────────────────────

  _resetState() {
    this.mode              = null;
    this._lastFp           = '';
    this._capsmanAvailable = false;
    this._lastWifiBatch    = [];
    this._lastCapsmanBatch = [];
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
      // If resume() was called before this probe completed (page was open), the
      // wifi/wireless stream is already running — start capsman now to catch up.
      if (!this._streams.capsman && (this._streams.wifi || this._streams.wireless)) {
        this._startStream('capsman');
      }
    } catch (e) {
      const msg = String(e && e.message ? e.message : e);
      if (msg.includes('unknown command') || msg.includes('no such')) {
        this._capsmanAvailable = false;
        if (this._debug) console.log(this._lbl + ' capsman probe: not available on this router');
      }
      // transient errors leave _capsmanAvailable = false and re-probe on reconnect
    }
  }

  // ── lifecycle ────────────────────────────────────────────────────────────

  suspend() {
    this._stopStream('wifi');
    this._stopStream('wireless');
    this._stopStream('capsman');
  }

  resume() {
    if (!this.ros.connected) return;
    if (this.mode === 'wireless') {
      this._startStream('wireless');
    } else {
      // mode === 'wifi' or null (probe via first empty-batch detection)
      this._startStream('wifi');
    }
    if (this._capsmanAvailable) this._startStream('capsman');
  }

  stop() {
    this._stopStream('wifi');
    this._stopStream('wireless');
    this._stopStream('capsman');
    if (this._retryTimer) { clearTimeout(this._retryTimer); this._retryTimer = null; }
  }

  start() {
    this._debug = require('../settings').load().rosDebug;
    const doStart = async () => {
      await this._probeCAPsMAN();
      this.resume();
    };
    doStart(); // initial start: probe then resume (page may already have viewers)
    this.ros.on('close', () => this.stop());
    this.ros.on('connected', () => {
      this.stop();
      this._resetState();
      // Probe CAPsMAN to refresh availability; resume() is called externally
      // by _updateWirelessStreams() once this reconnect event propagates to index.js.
      this._probeCAPsMAN().catch(() => {});
    });
  }
}

module.exports = WirelessCollector;
