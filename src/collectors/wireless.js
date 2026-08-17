'use strict';
const dns = require('dns').promises;
const { clampPoll, stopStreamSafe, createPollLoop } = require('./util');

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
// Shared by the stream and poll paths so both query the same table.
const WL_ENDPOINTS = {
  wifi:     '/interface/wifi/registration-table/print',
  wireless: '/interface/wireless/registration-table/print',
  capsman:  '/caps-man/registration-table/print',
};

/* Where the configured SSIDs live, newest stack first.
 *
 * Probed against a real fleet: on RouterOS 7.2x every board answered
 * /interface/wifi, including one still on 802.11ac — /interface/wireless is the
 * older stack and is absent there, so this is a fallback rather than a parallel
 * source. The first endpoint that answers wins. */
const SSID_ENDPOINTS = {
  wifi:     '/interface/wifi/print',
  wireless: '/interface/wireless/print',
};

// How often to repeat the "no ARP entry for any client" warning (#93). This is
// usually a permanent property of the deployment rather than a fault to watch,
// so it is a reminder for whoever reads the log later, not a live signal.
const NO_ARP_LOG_MS = 15 * 60_000;

class WirelessCollector {
  constructor({ ros, io, pollMs, state, dhcpLeases, arp, streamMode }) {
    // Delivery per router (#105). Poll re-reads the same registration table the
    // =interval= stream pushes and hands it to the same _onBatch(), so the
    // wifi/wireless/capsman mode latching is untouched.
    this.streamMode = streamMode !== false;
    this._pollTypes = new Set();   // capsman can run alongside wifi/wireless
    this._poll      = createPollLoop(() => this._pollOnce(), () => this.pollMs);
    this.ros        = ros;
    this.io         = io;
    this.pollMs     = clampPoll(pollMs, 5000);
    this.state      = state;
    this.dhcpLeases = dhcpLeases;
    this.arp        = arp;
    this.mode       = null;
    this._lastFp    = '';
    this.lastPayload = null;

    this._absentTicks = new Map();
    this.ABSENCE_THRESHOLD = 3;
    this._knownClients = new Map();
    this._nameCache    = new Map();
    this._ptrCache     = new Map(); // ip → { name: string, ts: number }
    this._retryTimer   = null;
    // When the "no ARP entry for anyone" warning was last logged (#93). Zero
    // means not currently in that state, so recovery re-arms it.
    this._noArpSince   = 0;
    this._capsmanAvailable = false;
    // SSID list: configuration, refreshed on a slow cadence of its own.
    // undefined = not probed yet, null = no wireless stack answered.
    this._ssidEndpoint = undefined;
    this._ssids = [];
    this._ssidsManagedElsewhere = 0;
    this._ssidTimer = null;
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

  resolveName(mac, ip) {
    if (!mac) return '';
    if (this._nameCache.has(mac)) return this._nameCache.get(mac);
    const byMac = this.dhcpLeases ? this.dhcpLeases.getNameByMAC(mac) : null;
    if (byMac && byMac.name) { this._nameCache.set(mac, byMac.name); return byMac.name; }
    if (!ip) return '';
    const cached = this._ptrCache.get(ip);
    if (cached) return cached.name;
    this._ptrLookup(ip);
    return '';
  }

  _ptrLookup(ip) {
    const cached = this._ptrCache.get(ip);
    if (cached) {
      const ttl = cached.name ? 60000 : 15000;
      if (Date.now() - cached.ts < ttl) return;
      this._ptrCache.delete(ip);
    }
    dns.reverse(ip).then(hosts => {
      const raw  = (hosts && hosts[0]) ? hosts[0].replace(/\.$/, '') : '';
      const name = raw.split('.')[0] || '';
      this._ptrCache.set(ip, { name, ts: Date.now() });
    }).catch(() => {
      this._ptrCache.set(ip, { name: '', ts: Date.now() });
    });
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
      name:    this.resolveName(mac, ip),
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
      console.warn('%s', this._lbl + ` partial result suspected — ${nonCapsmanSeen} from API vs ${nonCapsmanKnown} known — skipping absence aging`);
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
          if (dbg) console.log('%s', this._lbl + ` removing ${mac} — absent ${absent} ticks (>= threshold ${this.ABSENCE_THRESHOLD})`);
          this._knownClients.delete(mac);
          this._absentTicks.delete(mac);
          this._nameCache.delete(mac);
        } else {
          if (dbg) console.log('%s', this._lbl + ` holding ${mac} — absent ${absent}/${this.ABSENCE_THRESHOLD} ticks`);
          this._absentTicks.set(mac, absent);
        }
      }
    }

    if (dbg) {
      console.log('%s', this._lbl + ` batch: ${thisTickByMac.size} from API, ${this._knownClients.size} known${mightBePartial ? ' [partial — aging skipped]' : ''}`);
    }
  }

  /**
   * The SSIDs this router broadcasts.
   *
   * Read from the interface list rather than from connected clients: an SSID
   * with nobody on it is still an SSID, and the client table only knows about
   * networks somebody happens to be using.
   *
   * Only name, SSID and state keys are read. The same rows carry
   * `security.passphrase` in clear text, and none of it has any business
   * leaving this function — the payload goes to every browser on the Wireless
   * page.
   */
  _parseSsids(rows) {
    const byName = new Map();     // ssid -> aggregate row
    let managedElsewhere = 0;

    for (const r of rows || []) {
      // A CAP takes its configuration from the manager, so it genuinely has no
      // local SSID to report. Counting these lets the card say so rather than
      // rendering an empty list that looks like a failure.
      if (r['configuration.manager']) { managedElsewhere++; continue; }

      const ssid = String(r['configuration.ssid'] || r.ssid || '').trim();
      if (!ssid) continue;

      const iface    = String(r.name || '').trim();
      const disabled = r.disabled === 'true' || r.disabled === true;
      // `running` is the honest answer to "is this on the air right now" — an
      // interface can be enabled and still not running.
      const running  = r.running === 'true' || r.running === true;

      let e = byName.get(ssid);
      if (!e) {
        e = { ssid, ifaces: [], bands: [], disabled: true, running: false, clients: 0 };
        byName.set(ssid, e);
      }
      if (iface && e.ifaces.indexOf(iface) === -1) e.ifaces.push(iface);
      // One radio broadcasting it is enough for the SSID to be up; it is only
      // "disabled" when every interface carrying it is.
      if (!disabled) e.disabled = false;
      if (running)   e.running  = true;
    }

    return {
      ssids: this._withClientStats([...byName.values()])
        .sort((a, b) => a.ssid.localeCompare(b.ssid)),
      managedElsewhere,
    };
  }

  /**
   * Fill in bands and client counts from the live registration table.
   *
   * Kept apart from _parseSsids because the two run on different clocks. The
   * SSID list is configuration, re-read every five minutes; who is connected
   * changes constantly. Folding the second into the first froze bands and
   * counts at whatever the client table held during that refresh — and at
   * startup the refresh completes before the first client batch arrives, so
   * every SSID was published with no bands and a count of zero and stayed that
   * way for the rest of the cycle.
   *
   * Returns copies. The cached list is configuration truth and is reused on
   * every emit, so counting into it in place would accumulate.
   */
  _withClientStats(ssids) {
    const out     = (ssids || []).map(s => ({ ...s, bands: [], clients: 0 }));
    const byIface = new Map();
    const bySsid  = new Map();
    for (const e of out) {
      bySsid.set(e.ssid, e);
      for (const i of e.ifaces || []) byIface.set(i, e);
    }

    for (const c of this._knownClients.values()) {
      // Interface first: that is what the association is keyed on, and it is
      // the one field the registration table is certain to carry. Matching on
      // the client's own ssid field alone means that if a RouterOS build does
      // not report one, every count reads zero — indistinguishable from an idle
      // network. The name match stays as the fallback, for the legacy stack and
      // for CAPsMAN rows naming an interface this router does not own.
      const e = byIface.get(c.iface) || bySsid.get(c.ssid);
      if (!e) continue;
      e.clients++;
      if (c.band && e.bands.indexOf(c.band) === -1) e.bands.push(c.band);
    }

    for (const e of out) e.bands.sort();
    return out;
  }

  /**
   * Refresh the SSID list.
   *
   * Deliberately infrequent: this is configuration, not telemetry — it changes
   * when somebody edits the router, not every second. Failure is silent and
   * leaves the previous list in place, because a card that empties itself on one
   * bad poll is worse than a card that is briefly stale.
   */
  async _refreshSsids() {
    if (this._ssidEndpoint === null) return;          // no stack answered; stop asking
    const order = this._ssidEndpoint
      ? [this._ssidEndpoint]
      : [SSID_ENDPOINTS.wifi, SSID_ENDPOINTS.wireless];

    for (const endpoint of order) {
      try {
        const rows = (await this.ros.write(endpoint, [])) || [];
        this._ssidEndpoint = endpoint;                // latch the one that works
        const { ssids, managedElsewhere } = this._parseSsids(rows);
        this._ssids = ssids;
        this._ssidsManagedElsewhere = managedElsewhere;
        return;
      } catch (e) {
        // Try the next stack. Only when every one has failed do we give up, and
        // only for this cycle unless nothing has ever worked.
      }
    }
    if (!this._ssidEndpoint) {
      this._ssidEndpoint = null;                      // neither stack exists here
      this._ssids = [];
    }
  }

  /**
   * Say so when hostname resolution cannot work here (#93, from #88).
   *
   * Names come from the DHCP lease table (by MAC) or from a reverse lookup of
   * the client's IP, and that IP comes only from the router's ARP table. A
   * router that bridges wireless clients at layer 2 — a CAP whose gateway and
   * DHCP live on another device — never holds an ARP entry for them, so no IP
   * is known and resolveName() returns before attempting anything.
   *
   * That is correct behaviour with no error to report, which is exactly why it
   * needs saying out loud. The reporter in #88 had working reverse DNS the
   * whole time and no lookup was ever tried; the page showed bare MACs, and
   * nothing distinguished "your DNS is misconfigured" from "this router cannot
   * know". One line, not one per client, and throttled: a layer 2 AP is
   * permanently in this state, so repeating it every poll would bury the log
   * this exists to make readable.
   */
  _checkArpAvailability(clients) {
    const total = clients.length;
    const noIp  = clients.filter(c => !c.ip).length;
    // Only when it is every client. A handful without an ARP entry is ordinary
    // — one that just associated has not spoken IP yet — and warning on that
    // would cry wolf on every healthy router.
    if (!(total > 0 && noIp === total)) {
      this._noArpSince = 0;   // reset, so a recurrence is reported promptly
      return;
    }
    const now = Date.now();
    if (this._noArpSince && now - this._noArpSince < NO_ARP_LOG_MS) return;
    this._noArpSince = now;

    const named = clients.filter(c => c.name).length;
    console.log('%s', this._lbl +
      ` hostname resolution unavailable: ${noIp}/${total} clients have no ARP entry` +
      (named ? ` (${named} still named from DHCP leases)` : '') +
      '. This router needs an IP interface on the client subnet, or to be their' +
      ' DHCP server, before wireless clients can be named.');
  }

  _emitClients() {
    const parsed = Array.from(this._knownClients.values())
      .sort((a, b) => b.signal - a.signal);

    this._checkArpAvailability(parsed);

    // Bands and counts are recomputed here rather than read off the cached
    // list, because that list is only rebuilt every five minutes while this
    // runs on every client batch. See _withClientStats.
    const ssids = this._withClientStats(this._ssids);

    const fp = JSON.stringify({
      c: parsed.map(x => ({
        mac: x.mac, signal: x.signal, iface: x.iface, band: x.band, name: x.name,
      })),
      // Included so an SSID being added, renamed or disabled reaches the page.
      // Without it the emit is gated purely on client churn, and on a quiet
      // network the card would not update until somebody roamed.
      s: ssids, m: this._ssidsManagedElsewhere,
    });
    const payload = {
      ts: Date.now(), clients: parsed, mode: this.mode || 'none',
      pollMs: this.pollMs, capsmanAvailable: this._capsmanAvailable,
      ssids,
      // How many radios take their SSID from a CAPsMAN manager instead of from
      // local configuration, so an empty list can explain itself.
      ssidsManagedElsewhere: this._ssidsManagedElsewhere,
    };
    this.lastPayload           = payload;
    this.state.lastWirelessTs  = Date.now();
    this.state.lastWirelessErr = null;
    if (fp !== this._lastFp) {
      this._lastFp = fp;
      // Client detail is page-scoped (issue #108). A router-wide wireless:count
      // used to ride alongside it for the sidebar badge; the badge is gone, and
      // it was the only consumer. The Wireless page's own client count comes
      // from this payload.
      this.io.to('page-wireless').to('dash-card-wireless').emit('wireless:update', payload);
    }

    const hasUnnamed = parsed.length > 0 && parsed.some(c => !c.name);
    if (hasUnnamed && !this._retryTimer) {
      const tryResolve = () => {
        this._retryTimer = null;
        if (!this.ros.connected) return;
        let changed = false;
        for (const [mac, client] of this._knownClients) {
          if (!client.name) {
            const name = this.resolveName(mac, client.ip || '');
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
            this.io.to('page-wireless').to('dash-card-wireless').emit('wireless:update', newPayload);
          }
        }
        // Keep retrying only while PTR lookups for known IPs are still in-flight
        const stillPending = Array.from(this._knownClients.values())
          .some(c => !c.name && c.ip && !this._ptrCache.has(c.ip));
        if (stillPending) {
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
          if (this._debug) console.log('%s', this._lbl + ' mode latched: wifi');
        } else {
          // Empty first batch — wifi API not populated, fall through to legacy
          this.mode = 'wireless';
          if (this._debug) console.log('%s', this._lbl + ' mode latched: wireless (wifi returned empty)');
          this._stopStream('wifi');
          this._startDelivery('wireless');
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

  async _pollOnce() {
    if (!this.ros.connected || !this._pollTypes.size) return;
    for (const type of this._pollTypes) {
      try {
        const rows = (await this.ros.write(WL_ENDPOINTS[type])) || [];
        this._onBatch(type, rows);    // same batch path the stream uses
      } catch (e) {
        const msg = (e && e.message) ? e.message : String(e);
        // An API being absent is proven by the command being refused. The stream
        // path has had this fallback all along (see the stream 'error' handler);
        // the poll path had none, so a router pointed at a command tree it does
        // not have just error-logged every interval, forever.
        //
        // Both directions matter. wifi->wireless mirrors the stream. The reverse
        // rescues a board that latched legacy from an EMPTY wifi table — the
        // deliberate heuristic in _onBatch, which is right for a RouterOS 7 box
        // whose radios really are legacy, but wrong for a wifi-only board such as
        // a hAP ac2 with its radios disabled, where wifi answers empty and
        // /interface/wireless does not exist at all. That board now heals itself
        // on the first error instead of staying blank until a restart.
        if (/no such command|unknown command/i.test(msg)) {
          const fallback = type === 'wifi' ? 'wireless' : (type === 'wireless' ? 'wifi' : null);
          this._stopStream(type);
          if (fallback) {
            if (this._debug) {
              console.log('%s', this._lbl + ` ${type} unavailable (${msg}); switching to ${fallback}`);
            }
            this.mode = fallback;
            this._startDelivery(fallback);
          }
          continue;   // capsman simply is not on this board — stop asking.
        }
        console.error('%s', this._lbl + ` ${type} poll error:`, msg);
      }
    }
  }

  // Single place deciding stream vs poll; callers keep using _startStream(type).
  _startDelivery(type) {
    if (this.streamMode) { this._startStream(type); return; }
    this._pollTypes.add(type);
    this._poll.start();
  }

  _startStream(type) {
    if (this._streams[type] || this._restarting[type]) return;
    if (!this.ros.connected) return;
    const intervalSec = Math.max(1, Math.round(this.pollMs / 1000));
    const endpoints = WL_ENDPOINTS;
    const stream = this.ros.stream([endpoints[type], `=interval=${intervalSec}`], null);
    this._streams[type] = stream;
    stream.on('data', (pkt) => {
      // RStream emits [] when the table produced no rows this interval — deliver
      // an empty batch so departed clients age out instead of persisting forever
      // (and so an empty wifi table can still latch legacy-wireless fallback).
      if (Array.isArray(pkt)) {
        if (pkt.length === 0 && !this._batches[type].length && !this._debounces[type]) this._onBatch(type, []);
        return;
      }
      if (!pkt || typeof pkt !== 'object') return;
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
          if (this._debug) console.log('%s', this._lbl + ' mode latched: wireless (wifi command unknown)');
          this._startDelivery('wireless');
        }
        return;
      }
      console.error('%s', this._lbl, `${type} stream error:`, msg);
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
    console.log('%s', this._lbl, `streaming ${endpoints[type]} interval=${intervalSec}s`);
  }

  _stopStream(type) {
    this._pollTypes.delete(type);
    if (!this._pollTypes.size) this._poll.stop();
    if (this._debounces[type])     { clearTimeout(this._debounces[type]);     this._debounces[type] = null; }
    if (this._restartTimers[type]) { clearTimeout(this._restartTimers[type]); this._restartTimers[type] = null; }
    this._restarting[type] = false;
    if (this._streams[type]) { stopStreamSafe(this._streams[type]); this._streams[type] = null; }
    this._batches[type] = [];
  }

  // ── state reset ───────────────────────────────────────────────────────────

  _resetState() {
    this.mode              = null;
    this._lastFp           = '';
    this._capsmanAvailable = false;
    // SSID list: configuration, refreshed on a slow cadence of its own.
    // undefined = not probed yet, null = no wireless stack answered.
    this._ssidEndpoint = undefined;
    this._ssids = [];
    this._ssidsManagedElsewhere = 0;
    this._ssidTimer = null;
    this._lastWifiBatch    = [];
    this._lastCapsmanBatch = [];
    this._nameCache.clear();
    this._ptrCache.clear();
    this._knownClients.clear();
    this._absentTicks.clear();
    if (this._retryTimer) { clearTimeout(this._retryTimer); this._retryTimer = null; }
  }

  async _probeCAPsMAN() {
    try {
      await this.ros.write('/caps-man/registration-table/print', []);
      this._capsmanAvailable = true;
      if (this._debug) console.log('%s', this._lbl + ' capsman probe: available');
      // If resume() was called before this probe completed (page was open), the
      // wifi/wireless stream is already running — start capsman now to catch up.
      if (!this._streams.capsman && (this._streams.wifi || this._streams.wireless)) {
        this._startDelivery('capsman');
      }
    } catch (e) {
      const msg = String(e && e.message ? e.message : e);
      if (msg.includes('unknown command') || msg.includes('no such')) {
        this._capsmanAvailable = false;
        // Nothing else is reset here. This probe owns _capsmanAvailable and
        // nothing more: the SSID list comes from a different endpoint on its own
        // cadence, and clearing it from this catch blanked the card on every
        // router with no /caps-man menu.
        if (this._debug) console.log('%s', this._lbl + ' capsman probe: not available on this router');
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
      this._startDelivery('wireless');
    } else {
      // mode === 'wifi' or null (probe via first empty-batch detection)
      this._startDelivery('wifi');
    }
    if (this._capsmanAvailable) this._startDelivery('capsman');
  }

  stop() {
    this._stopStream('wifi');
    this._stopStream('wireless');
    this._stopStream('capsman');
    if (this._retryTimer) { clearTimeout(this._retryTimer); this._retryTimer = null; }
    if (this._ssidTimer) { clearInterval(this._ssidTimer); this._ssidTimer = null; }
  }

  /* SSIDs are configuration, so they get their own slow cadence rather than
     riding the client poll — five minutes, and once immediately so the card is
     populated on the first paint rather than after the first interval. */
  _startSsidRefresh() {
    if (this._ssidTimer) return;
    const tick = () => this._refreshSsids().then(() => this._emitClients()).catch(() => {});
    tick();
    this._ssidTimer = setInterval(tick, 5 * 60_000);
    // Never hold the process open for a list of network names.
    if (this._ssidTimer.unref) this._ssidTimer.unref();
  }

  start() {
    this._debug = require('../settings').load().rosDebug;
    const doStart = async () => {
      await this._probeCAPsMAN();
      this._startSsidRefresh();
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
      // A reconnect may be a different box, or the same one reconfigured.
      this._ssidEndpoint = undefined;
      this._startSsidRefresh();
    });
  }
}

module.exports = WirelessCollector;
