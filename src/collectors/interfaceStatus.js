/**
 * Interface Status collector — all four data sources use persistent streams.
 *
 * Metadata streams (interval = metaPollMs, default 60 s):
 *   /interface/print =.proplist=name,type,running,disabled,comment,mac-address,<counters> =interval=N
 *   /ip/address/print =.proplist=interface,address =interval=N
 *   /interface/ethernet/print =.proplist=name,<phy errors> =interval=N
 *
 * Rate stream (interval derived from pollMs, default 5 s):
 *   /interface/monitor-traffic =interface=<all> =.proplist=name,rx-bits-per-second,tx-bits-per-second =interval=N
 *
 * All use ros.stream() with null callback + 'data' event to bypass RStream's
 * section-handling debounce.
 *
 * _emitTimer fires every pollMs — calls _buildAndEmit() so rate bars update
 * smoothly. _commitMeta() fires immediately after each metadata tick (via a
 * 300 ms debounce) so interface up/down changes are reflected without waiting
 * for the next emit tick.
 */

const { parseBps, bpsToMbps, clampPoll, stopStreamSafe } = require('./util');

// Cumulative counters carried by /interface/print. Which of these a row
// actually returns depends on the interface type, so they are read defensively
// and a missing counter stays null rather than collapsing to 0 — "this driver
// does not report errors" and "this interface has no errors" are different
// claims and the UI renders them differently.
const IF_COUNTER_PROPS = 'rx-byte,tx-byte,rx-error,tx-error,rx-drop,tx-drop,tx-queue-drop,link-downs,last-link-up-time';

// Ethernet is the notable gap: ether rows return tx-queue-drop but none of the
// rx/tx error or drop counters. The PHY-level equivalents live on
// /interface/ethernet instead, which is why that stream exists at all.
const ETH_ERR_FIELDS = [
  'rx-fcs-error', 'rx-align-error', 'rx-fragment', 'rx-overflow',
  'rx-too-short', 'rx-too-long',
  'tx-underrun', 'tx-late-collision', 'tx-excessive-collision',
];

// Errors are link-integrity faults (corruption, collisions). Drops are
// discards (full queue, no buffer). Both are counted, but conflating them
// would hide the difference between a bad cable and a congested link.
const IF_ERR_FIELDS  = ['rx-error', 'tx-error'];
const IF_DROP_FIELDS = ['rx-drop', 'tx-drop', 'tx-queue-drop'];

function num(v) {
  if (v === undefined || v === null || v === '') return null;
  const n = Number(v);
  return Number.isFinite(n) ? n : null;
}

// Sums the fields a row actually reports. Returns null only when the row
// reports none of them, which is how an unsupported counter set stays
// distinguishable from a genuine zero.
function sumCounters(row, fields) {
  let total = null;
  for (const f of fields) {
    const n = num(row[f]);
    if (n === null) continue;
    total = (total === null ? 0 : total) + n;
  }
  return total;
}

// A counter that went backwards means it was reset (reboot, or an explicit
// reset-counters), not that negative errors occurred.
function deltaOf(prev, cur) {
  if (prev === null || prev === undefined || cur === null) return null;
  return cur >= prev ? cur - prev : 0;
}

class InterfaceStatusCollector {
  constructor({ ros, io, pollMs, metaPollMs, state, streamMode, alertsActive, rid }) {
    this.ros        = ros;
    this.io         = io;
    // See SystemCollector: alerts are fed from the emit path, so a router with
    // alerts enabled must keep emitting with no viewer attached or interface
    // up/down alerts never fire.
    this._alertsActive = typeof alertsActive === 'function' ? alertsActive : () => false;
    this._lbl       = ros.routerLabel ? `[${ros.routerLabel}][ifstatus]` : '[ifstatus]';
    this.pollMs       = clampPoll(pollMs, 5000); // rate stream + emit timer interval
    this._pollDelayMs = clampPoll(pollMs, 5000);
    this.metaPollMs = metaPollMs || 60000; // metadata streams interval
    this.state      = state;
    this.streamMode = streamMode !== false; // default true
    // Stamped onto every payload so the browser can tell WHICH router an
    // interface list describes. Without it the client compares by interface
    // name alone, and a late in-flight update from the outgoing session — the
    // teardown is asynchronous — reads as ether2..5 going down and back up.
    this.rid = rid || '';

    this._ifaces     = new Map(); // name -> committed interface row
    // The `.section` stamp of the interval cycle currently being accumulated,
    // and how many interfaces the last COMPLETE cycle held. Together they keep
    // a truncated list from being installed as the truth (#119).
    this._ifaceSection  = undefined;
    this._lastCycleSize = 0;
    this._addrs      = new Map(); // interface name -> [cidr, ...]
    this._eth        = new Map(); // ether name -> committed PHY error row
    this._ifacesNext = new Map(); // accumulator for current metadata tick
    this._addrsNext  = new Map(); // accumulator for current metadata tick
    this._ethNext    = new Map(); // accumulator for current metadata tick

    // Counter snapshot from the previous metadata commit, used to turn
    // lifetime totals into "errors since the last tick". A lifetime count of
    // 656 says nothing about whether the fault is ongoing; the delta does.
    this._prevCounters = new Map(); // name -> { errors, drops, ts }
    this._deltas       = new Map(); // name -> { errors, drops, windowMs }

    this._ifStream        = null;
    this._ifRestartTimer  = null;
    this._addrStream      = null;
    this._addrRestartTimer = null;
    this._ethStream       = null;
    this._ethRestartTimer = null;
    this._metaDebounce    = null;

    this._monitorStream        = null;
    this._streamRates          = new Map(); // name -> { rxMbps, txMbps }
    this._monitorIfaceKey      = '';
    this._monitorRestartTimer  = null;

    this._emitTimer    = null;
    this._ratesTimer   = null;
    this._ratesInflight = false;
    this._lastFp       = '';
    this._lastEmitTs   = 0;
    this._lastRatesSuccessTs = 0;
    this._lastPollErrLogTs = 0;
    this.lastPayload   = null;

    this.ros.on('close', () => {
      this._stopMetaStreams();
      this._stopMonitorStream();
      this._stopRatesPoll();
      this._stopEmitTimer();
    });
    this.ros.on('connected', () => {
      this._stopMetaStreams();
      this._stopMonitorStream();
      this._stopRatesPoll();
      this._stopEmitTimer();
      this._ifaces.clear();
      this._addrs.clear();
      this._eth.clear();
      this._streamRates.clear();
      // A reconnect may follow a router reboot, where every counter restarts
      // from zero. Dropping the baseline costs one tick of delta and avoids
      // reporting a spurious drop-to-zero as activity.
      this._prevCounters.clear();
      this._deltas.clear();
      this._lastFp = '';
      this._startMetaStreams();
      this._startEmitTimer();
      if (!this.streamMode) this._startRatesPoll();
    });
  }

  // ── poll-mode rate path ───────────────────────────────────────────────────

  async _pollRatesOnce() {
    if (!this.ros.connected || this._ratesInflight) return;
    const names = [...this._ifaces.keys()].filter(n => {
      const iface = this._ifaces.get(n);
      // RouterOS sends `disabled` as the STRING "false", which is truthy — so a
      // bare !iface.disabled filters out EVERY interface, names comes back empty,
      // _pollRatesOnce returns before its write, and rates sit at 0 forever. The
      // payload builder below already guards this with an explicit === 'true'.
      return iface && !(iface.disabled === 'true' || iface.disabled === true);
    });
    if (!names.length) return;
    this._ratesInflight = true;
    try {
      const rows = await this.ros.write('/interface/monitor-traffic', [
        `=interface=${names.join(',')}`,
        '=once=',
        '=.proplist=name,rx-bits-per-second,tx-bits-per-second',
      ]);
      if (Array.isArray(rows)) {
        for (const r of rows) {
          if (!r || !r.name) continue;
          this._streamRates.set(r.name, {
            rxMbps: bpsToMbps(parseBps(r['rx-bits-per-second'])),
            txMbps: bpsToMbps(parseBps(r['tx-bits-per-second'])),
          });
        }
        const now = Date.now();
        this._lastRatesSuccessTs = now;
        this.state.lastIfStatusTs = now;
        this.state.lastIfStatusErr = null;
      }
    } catch (e) {
      const msg = e && e.message ? e.message : String(e);
      this.state.lastIfStatusErr = msg;
      const now = Date.now();
      if (now - this._lastPollErrLogTs >= 60000) {
        this._lastPollErrLogTs = now;
        // Constant format string; label and message are arguments, so a '%' in
        // either cannot act as a format specifier (CodeQL js/tainted-format-string).
        console.error('%s monitor-traffic poll error: %s', this._lbl, msg);
      }
    } finally {
      this._ratesInflight = false;
    }
  }

  _scheduleRatesNext() {
    clearTimeout(this._ratesTimer);
    this._ratesTimer = setTimeout(async () => {
      this._ratesTimer = null;
      if (!this.streamMode) {
        await this._pollRatesOnce();
        this._scheduleRatesNext();
      }
    }, Math.max(500, Math.min(60000, this._pollDelayMs)));
  }

  _startRatesPoll() {
    console.log('%s', this._lbl + ' poll mode — polling /interface/monitor-traffic every', this.pollMs + 'ms');
    this._pollRatesOnce();
    this._scheduleRatesNext();
  }

  // ── metadata streams ──────────────────────────────────────────────────────

  _startMetaStreams() {
    this._startIfStream();
    this._startAddrStream();
    this._startEthStream();
  }

  _stopMetaStreams() {
    if (this._ifRestartTimer)   { clearTimeout(this._ifRestartTimer);   this._ifRestartTimer   = null; }
    if (this._addrRestartTimer) { clearTimeout(this._addrRestartTimer); this._addrRestartTimer = null; }
    if (this._ethRestartTimer)  { clearTimeout(this._ethRestartTimer);  this._ethRestartTimer  = null; }
    if (this._ifStream)   { stopStreamSafe(this._ifStream);   this._ifStream   = null; }
    if (this._addrStream) { stopStreamSafe(this._addrStream); this._addrStream = null; }
    if (this._ethStream)  { stopStreamSafe(this._ethStream);  this._ethStream  = null; }
    clearTimeout(this._metaDebounce);
    this._metaDebounce = null;
    this._ifacesNext   = new Map();
    this._addrsNext    = new Map();
    this._ethNext      = new Map();
  }

  _restartMetaStreams() {
    this._stopMetaStreams();
    this._startMetaStreams();
  }

  /**
   * Re-read now, after a write, so the page shows what the router did.
   *
   * The interface list arrives on an `=interval=N` push rather than a /listen,
   * so a newly created VETH would otherwise not appear until the next meta
   * tick — a minute on the default interval, which reads as a failed save.
   * Restarting the streams re-issues the print immediately, and is this
   * collector's own idiom for "read the metadata again".
   */
  async refreshNow() {
    if (!this.ros.connected) return;
    this._restartMetaStreams();
  }

  _startIfStream() {
    if (this._ifStream || !this.ros.connected) return;
    // A restart begins its cycle numbering again at 0, so anything half
    // accumulated from the previous stream belongs to nobody. Dropping it also
    // stops the first packet of the new stream reading as a cycle boundary and
    // committing that orphan as a complete list.
    this._ifacesNext.clear();
    this._ifaceSection = undefined;
    const intervalSec = Math.max(1, Math.round(this.metaPollMs / 1000));
    console.log('%s', this._lbl + ' streaming /interface/print, interval=' + intervalSec + 's');
    const stream = this.ros.stream(
      '/interface/print',
      [
        `=interval=${intervalSec}`,
        `=.proplist=name,type,running,disabled,comment,mac-address,${IF_COUNTER_PROPS}`,
      ],
      null
    );
    stream.on('data', (packet) => {
      if (!packet || !packet.name || typeof packet.name !== 'string') return;
      // `.section` IS THE CYCLE DELIMITER, and it was on the wire all along.
      // RouterOS stamps every packet of an `=interval=N` response with the
      // cycle it belongs to — 0, 1, 2, … — so a changed section means the
      // previous cycle ended and the batch we are holding is COMPLETE, rather
      // than "whatever arrived before a 300 ms timer happened to fire" (#119).
      //
      // Verified on a hAP AC2 (RouterOS 7.24): /interface/print =interval=3
      // delivers nine packets stamped .section=0, then nine stamped 1, and so
      // on. It is on the monitor-shaped commands too, which is why this and not
      // `!done` is the delimiter worth relying on: a `print =interval=N`
      // terminates each cycle with `!done` but `/interface/monitor-traffic`
      // never does, so only `.section` covers both.
      //
      // Guarded on !== undefined rather than truthiness: the first cycle is
      // section '0'.
      const _sec = packet['.section'];
      if (_sec !== undefined && this._ifaceSection !== undefined && _sec !== this._ifaceSection) {
        this._commitMeta(true);
      }
      if (_sec !== undefined) this._ifaceSection = _sec;
      this._ifacesNext.set(packet.name, packet);
      this._scheduleMetaCommit();
    });
    stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      console.error('%s', this._lbl + ' /interface/print stream error:', msg);
      this.state.lastIfStatusErr = msg;
      this._ifStream = null;
      if (!this._ifRestartTimer) {
        this._ifRestartTimer = setTimeout(() => {
          this._ifRestartTimer = null;
          if (this.ros.connected && !this._ifStream) this._startIfStream();
        }, 3000);
      }
    });
    this._ifStream = stream;
  }

  _startAddrStream() {
    if (this._addrStream || !this.ros.connected) return;
    const intervalSec = Math.max(1, Math.round(this.metaPollMs / 1000));
    console.log('%s', this._lbl + ' streaming /ip/address/print, interval=' + intervalSec + 's');
    const stream = this.ros.stream(
      '/ip/address/print',
      [
        `=interval=${intervalSec}`,
        '=.proplist=interface,address',
      ],
      null
    );
    stream.on('data', (packet) => {
      if (!packet || !packet.interface || typeof packet.interface !== 'string') return;
      if (!this._addrsNext.has(packet.interface)) this._addrsNext.set(packet.interface, []);
      this._addrsNext.get(packet.interface).push(packet.address || '');
      this._scheduleMetaCommit();
    });
    stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      console.error('%s', this._lbl + ' /ip/address/print stream error:', msg);
      this.state.lastIfStatusErr = msg;
      this._addrStream = null;
      if (!this._addrRestartTimer) {
        this._addrRestartTimer = setTimeout(() => {
          this._addrRestartTimer = null;
          if (this.ros.connected && !this._addrStream) this._startAddrStream();
        }, 3000);
      }
    });
    this._addrStream = stream;
  }

  // PHY error counters for ether ports only. A router with no ethernet (CHR,
  // a pure-wireless CAP) simply never emits here and the map stays empty,
  // which the row builder already treats as "not reported".
  _startEthStream() {
    if (this._ethStream || !this.ros.connected) return;
    const intervalSec = Math.max(1, Math.round(this.metaPollMs / 1000));
    console.log('%s', this._lbl + ' streaming /interface/ethernet/print, interval=' + intervalSec + 's');
    const stream = this.ros.stream(
      '/interface/ethernet/print',
      [
        `=interval=${intervalSec}`,
        `=.proplist=name,${ETH_ERR_FIELDS.join(',')}`,
      ],
      null
    );
    stream.on('data', (packet) => {
      if (!packet || !packet.name || typeof packet.name !== 'string') return;
      this._ethNext.set(packet.name, packet);
      this._scheduleMetaCommit();
    });
    stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      this._ethStream = null;
      // Not fatal and not worth a reconnect loop: every other column still
      // renders, the PHY error column just goes blank for ether ports.
      console.error('%s /interface/ethernet/print stream error: %s', this._lbl, msg);
      if (!this._ethRestartTimer) {
        this._ethRestartTimer = setTimeout(() => {
          this._ethRestartTimer = null;
          if (this.ros.connected && !this._ethStream) this._startEthStream();
        }, 3000);
      }
    });
    this._ethStream = stream;
  }

  _scheduleMetaCommit() {
    clearTimeout(this._metaDebounce);
    this._metaDebounce = setTimeout(() => this._commitMeta(), 300);
  }

  /**
   * @param {boolean} [fromCycleWrap] the interface stream saw a repeated name,
   *   so the batch it is holding is a whole print cycle.
   */
  _commitMeta(fromCycleWrap) {
    // A wrap can land while the debounce is still pending; leaving it armed
    // would fire a second commit over the fresh batch.
    clearTimeout(this._metaDebounce);
    this._metaDebounce = null;
    // Deltas are only meaningful against a fresh counter read. A commit driven
    // solely by the address or ethernet stream leaves _ifaces untouched, and
    // differencing it against itself would report a zero-error window that
    // never actually elapsed.
    //
    // A BATCH SMALLER THAN THE LAST COMPLETE CYCLE IS NOT TRUSTED unless the
    // `.section` stamp changed and proved the cycle ended. The debounce cannot
    // delimit a burst — it measures silence, not completeness — so if any gap
    // between two packets of one cycle exceeds 300 ms it fires MID-CYCLE, and
    // this swap would install a partial map as the whole truth. That is issue
    // #119, the Traffic dropdown losing all but one interface on a CCR2004.
    //
    // The size test is not redundant with the section test: it covers the FIRST
    // cycle after a (re)start, which has no previous section to differ from.
    // That is what keeps refreshNow() instant — it restarts the streams so a
    // newly created VETH appears at once rather than a meta interval later,
    // where its absence reads as a failed save. A bigger cycle commits on the
    // debounce; a smaller one waits for the section change, which arrives with
    // the first packet of the next cycle.
    // "Did the interface batch actually change?", not "is it non-empty?". Now
    // that a provisional commit LEAVES the batch in place, non-empty is true for
    // the whole cycle, and a commit driven by the address or ethernet stream
    // would otherwise re-run _computeDeltas() against rows it had already
    // differenced — reporting a zero-error window that never elapsed.
    //
    // Identity, not deep equality: every packet off the stream is a fresh
    // object, so a carried-over row is the SAME object and a re-read is not.
    const _batchChanged = this._ifacesNext.size > 0 && (
      this._ifaces.size !== this._ifacesNext.size ||
      [...this._ifacesNext.keys()].some((k) => this._ifaces.get(k) !== this._ifacesNext.get(k))
    );
    const ifacesTicked = _batchChanged &&
                         (fromCycleWrap || this._ifacesNext.size >= this._lastCycleSize);
    if (ifacesTicked) {
      // A DEBOUNCE COMMIT IS PROVISIONAL: it publishes what has arrived so far
      // and leaves the batch accumulating, because only a section change ends a
      // cycle. Handing the batch over and starting a new one mid-cycle is what
      // kept #119 alive after the section stamp was adopted — an interface that
      // reports late (a ZeroTier tunnel, in the report) landed ALONE in the
      // fresh batch, the size guard correctly refused to publish a batch of
      // one, and then the next cycle's first packet committed it on the wrap,
      // where the guard is bypassed by design. The reporter's dropdown
      // contained exactly `zerotier1`.
      //
      // So: copy here, hand over only at a real boundary. A straggler then
      // rejoins the cycle it belongs to instead of replacing it.
      this._ifaces = new Map(this._ifacesNext);
    }
    // THE BOUNDARY RESET IS UNCONDITIONAL, and deliberately outside the block
    // above. A wrap is a structural fact — the router has started a new cycle —
    // not a data event. Gating it on the batch having changed meant a cycle
    // already published provisionally never got cleared, so the next cycle
    // accumulated ON TOP of it and a deleted interface stayed in the list
    // forever.
    //
    // A completed cycle is also the only thing that can teach us how big a
    // cycle is: learning that from a provisional commit would set the bar to a
    // half-arrived burst and defeat the guard on the next one.
    if (fromCycleWrap && this._ifacesNext.size > 0) {
      this._lastCycleSize = this._ifacesNext.size;
      this._ifacesNext    = new Map();
    }
    // Only swap addresses when the new set is non-empty — an empty _addrsNext
    // means the address stream tick fired before the data arrived, not that
    // there are genuinely no IPs assigned. Always reset _addrsNext for the next batch.
    if (this._addrsNext && this._addrsNext.size > 0) {
      this._addrs = this._addrsNext;
    }
    this._addrsNext = new Map();

    // Same reasoning as addresses: an empty tick is a timing artefact, not an
    // ethernet-free router.
    if (this._ethNext && this._ethNext.size > 0) {
      this._eth = this._ethNext;
    }
    this._ethNext = new Map();

    if (ifacesTicked) this._computeDeltas();

    this._startMonitorStream(); // no-op if already running with same iface set
    this._buildAndEmit();
  }

  // Total link-integrity errors for a row: the driver-level counters where the
  // interface reports them, plus the PHY counters for ether ports, which are
  // the only place ethernet exposes them.
  _errorsFor(row) {
    const base = sumCounters(row, IF_ERR_FIELDS);
    const eth  = this._eth.get(row.name);
    const phy  = eth ? sumCounters(eth, ETH_ERR_FIELDS) : null;
    if (base === null && phy === null) return null;
    return (base || 0) + (phy || 0);
  }

  _dropsFor(row) {
    return sumCounters(row, IF_DROP_FIELDS);
  }

  _computeDeltas() {
    const now     = Date.now();
    const deltas  = new Map();
    const snapshot = new Map();
    for (const i of this._ifaces.values()) {
      const errors = this._errorsFor(i);
      const drops  = this._dropsFor(i);
      const prev   = this._prevCounters.get(i.name);
      if (prev) {
        const dErr  = deltaOf(prev.errors, errors);
        const dDrop = deltaOf(prev.drops, drops);
        if (dErr !== null || dDrop !== null) {
          deltas.set(i.name, { errors: dErr, drops: dDrop, windowMs: now - prev.ts });
        }
      }
      snapshot.set(i.name, { errors, drops, ts: now });
    }
    // Rebuilt rather than mutated so an interface that disappears does not
    // leave a stale delta behind for a name that later gets reused.
    this._deltas       = deltas;
    this._prevCounters = snapshot;
  }

  // ── monitor-traffic stream ────────────────────────────────────────────────

  _startMonitorStream() {
    const names = [...this._ifaces.keys()];
    if (!names.length) return;
    if (!this.streamMode) return; // poll mode — rates fetched by _pollRatesOnce
    const key = names.slice().sort().join(',');
    if (this._monitorStream && this._monitorIfaceKey === key) return;
    this._stopMonitorStream();
    if (!this.ros.connected) return;

    // /interface/monitor-traffic rejects intervals > 5s ("value of interval is out of range")
    const intervalSec = Math.max(1, Math.min(5, Math.round(this.pollMs / 1000)));
    console.log('%s', this._lbl + ' starting monitor-traffic stream,', names.length, 'interfaces, interval=' + intervalSec + 's');
    const stream = this.ros.stream(
      '/interface/monitor-traffic',
      [
        `=interface=${names.join(',')}`,
        '=.proplist=name,rx-bits-per-second,tx-bits-per-second',
        `=interval=${intervalSec}`,
      ],
      null
    );
    stream.on('data', (packet) => {
      if (!packet || typeof packet !== 'object' || Array.isArray(packet)) return;
      const name = packet.name;
      if (!name || typeof name !== 'string') return;
      this._streamRates.set(name, {
        rxMbps: bpsToMbps(parseBps(packet['rx-bits-per-second'])),
        txMbps: bpsToMbps(parseBps(packet['tx-bits-per-second'])),
      });
      const now = Date.now();
      this._lastRatesSuccessTs = now;
      this.state.lastIfStatusTs = now;
      this.state.lastIfStatusErr = null;
    });
    stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      this._monitorStream   = null;
      this._monitorIfaceKey = '';
      this._streamRates.clear();
      // 'no such item' fires when an interface in the list briefly disappears.
      // Suppress the log and reschedule — avoid a rapid restart loop.
      if (msg.includes('no such item')) {
        this._monitorRestartTimer = setTimeout(() => {
          this._monitorRestartTimer = null;
          if (this.ros.connected) this._startMonitorStream();
        }, 5000);
        return;
      }
      console.error('%s', this._lbl + ' monitor-traffic stream error:', msg);
      this.state.lastIfStatusErr = msg;
      // Recover directly instead of waiting up to metaPollMs (60 s default)
      // for _commitMeta to incidentally reopen the stream.
      if (!this._monitorRestartTimer) {
        this._monitorRestartTimer = setTimeout(() => {
          this._monitorRestartTimer = null;
          if (this.ros.connected && !this._monitorStream) this._startMonitorStream();
        }, 3000);
      }
    });
    this._monitorStream   = stream;
    this._monitorIfaceKey = key;
  }

  _stopMonitorStream() {
    if (this._monitorRestartTimer) { clearTimeout(this._monitorRestartTimer); this._monitorRestartTimer = null; }
    if (!this._monitorStream) return;
    stopStreamSafe(this._monitorStream);
    this._monitorStream   = null;
    this._monitorIfaceKey = '';
    this._streamRates.clear();
  }

  _restartMonitorStream() {
    this._stopMonitorStream();
    this._startMonitorStream();
  }

  // ── emit timer ────────────────────────────────────────────────────────────

  _startEmitTimer() {
    if (this._emitTimer) return;
    this._emitTimer = setInterval(() => this._buildAndEmit(), this.pollMs); // codeql[js/resource-exhaustion]
  }

  _stopEmitTimer() {
    if (this._emitTimer) { clearInterval(this._emitTimer); this._emitTimer = null; }
  }

  _restartEmitTimer() {
    this._stopEmitTimer();
    this._startEmitTimer();
  }

  // Aliases kept for index.js pollIfstatus live-update handler compatibility
  _startAddrPoll() { this._startEmitTimer(); }
  _stopAddrPoll()  { this._stopEmitTimer(); }

  // ── build + emit ──────────────────────────────────────────────────────────

  _buildAndEmit() {
    if (!this._ifaces.size) return;

    const now = Date.now();
    const interfaces = [];

    for (const i of this._ifaces.values()) {
      const sr = this._streamRates.get(i.name) || { rxMbps: 0, txMbps: 0 };
      const d  = this._deltas.get(i.name) || null;
      interfaces.push({
        name:     i.name     || '',
        type:     i.type     || 'ether',
        running:  i.running  === 'true' || i.running  === true,
        disabled: i.disabled === 'true' || i.disabled === true,
        comment:  i.comment  || '',
        macAddr:  i['mac-address'] || '',
        rxMbps:   sr.rxMbps,
        txMbps:   sr.txMbps,
        ips: this._addrs.get(i.name) || [],
        // Cumulative counters. null means the interface does not report the
        // counter at all, which the list view renders as a dash rather than 0.
        rxBytes:    num(i['rx-byte']),
        txBytes:    num(i['tx-byte']),
        errors:     this._errorsFor(i),
        drops:      this._dropsFor(i),
        linkDowns:  num(i['link-downs']),
        lastLinkUp: i['last-link-up-time'] || '',
        // Movement over the last metadata window, null until a baseline exists.
        errorsDelta:   d ? d.errors : null,
        dropsDelta:    d ? d.drops : null,
        deltaWindowMs: d ? d.windowMs : null,
      });
    }

    // Byte totals are deliberately absent from the fingerprint. They creep up
    // even on an idle link (broadcast traffic), so including them would defeat
    // the idle-suppression this check exists for. Errors, drops and flap counts
    // are in: they hold steady on a healthy link, so any movement is worth
    // pushing immediately, and the 60 s heartbeat carries the totals along.
    // type, comment and MAC are in for the opposite reason: they never move on
    // their own, so they cost nothing here, and leaving them out meant an edit
    // to one of them never reached an open page — the list renders all three.
    const fp = JSON.stringify(interfaces.map(i => ({
      n: i.name, t: i.type, c: i.comment, m: i.macAddr,
      r: i.running, d: i.disabled,
      rx: +i.rxMbps.toFixed(2), tx: +i.txMbps.toFixed(2),
      ips: i.ips,
      e: i.errors, dr: i.drops, ld: i.linkDowns,
    })));
    this.lastPayload = { ts: now, routerId: this.rid, interfaces };
    // Alerts ride the emit path, so a router with alerts enabled is exempt from
    // the idle gate or interface up/down alerts never fire.
    if (this.io.engine.clientsCount === 0 && !this._alertsActive()) return;
    // Re-emit a heartbeat even when rates are unchanged so the browser can
    // distinguish an idle interface from a dead collector.
    if (fp === this._lastFp && now - this._lastEmitTs < 60000) return;
    this._lastFp = fp;
    this._lastEmitTs = now;
    // Split delivery (issue #108). The full payload carries per-interface
    // rates, IP addresses and MACs, so it goes only to the pages that render
    // them: Interfaces, Topology (link rates — see public/js/topology.js) and
    // the dashboard ports card.
    //
    // The router-wide half carries names and up/down only. That is exactly what
    // the traffic chart's interface picker and the sidebar badge need, and it
    // is chrome on every page — so it must not be withheld, and it must not
    // disclose anything a denied page would have shown.
    const ifs = this.lastPayload.interfaces || [];
    this.io.to('page-interfaces').to('page-topology').to('dash-card-physports')
      .emit('ifstatus:update', this.lastPayload);
    this.io.emit('ifstatus:names', {
      ts: this.lastPayload.ts,
      total: ifs.length,
      interfaces: ifs.map(i => ({ name: i.name, running: !!i.running, disabled: !!i.disabled })),
    });
  }

  _stopRatesPoll() {
    if (this._ratesTimer) { clearTimeout(this._ratesTimer); this._ratesTimer = null; }
  }

  // ── lifecycle ─────────────────────────────────────────────────────────────

  start() {
    this._startMetaStreams();
    this._startEmitTimer();
    if (!this.streamMode) this._startRatesPoll();
  }

  suspend() {
    this._stopMonitorStream();
    this._stopRatesPoll();
    this._stopEmitTimer();
  }

  resume() {
    if (this.streamMode) {
      this._startMonitorStream();
    } else {
      this._startRatesPoll();
    }
    this._startEmitTimer();
  }

  stop() {
    this._stopMetaStreams();
    this._stopMonitorStream();
    this._stopRatesPoll();
    this._stopEmitTimer();
  }
}

module.exports = InterfaceStatusCollector;
