/**
 * Top Talkers (Kid Control) — streams /ip/kid-control/device/print.
 *
 * Uses ros.stream() with null callback + 'data' event to bypass RStream's
 * section-handling debounce. RouterOS delivers rate-up / rate-down
 * (bytes/second) directly — no byte-delta calculation needed.
 *
 * A 300 ms debounce accumulates per-device packets from each interval tick
 * before processing (RouterOS sends one !re per device per tick in a burst).
 *
 * Error classification:
 *   "unknown command" / "no such" → feature not present on this router;
 *     latch _unavailable, emit { devices: [], available: false } once, and stop.
 *     The latch is cleared ONLY by a deliberate re-probe — ros 'connected' or
 *     probe() — never by a later successful tick, and _startStream()/resume()
 *     both honour it, so an idle wake-up cannot quietly reopen the channel.
 *   "timeout" in stream mode → CHR/VM thread starvation; auto-downgrade to
 *     poll mode and restart. If poll also fails it goes through the poll
 *     handler below.
 *   "timeout" in poll mode → transient; log and retry normally.
 *   other stream errors → exponential backoff, retry stream.
 */

const { clampPoll, stopStreamSafe } = require('./util');

class TopTalkersCollector {
  constructor({ ros, io, pollMs, state, topN, streamMode }) {
    this.ros    = ros;
    this.io     = io;
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][talkers]` : '[talkers]';
    this.pollMs = pollMs;
    this._pollDelayMs = clampPoll(pollMs, 3000);
    this.state  = state;
    this.topN   = topN || 5;
    this.streamMode = streamMode !== false; // default true
    this.lastPayload = null;

    this._stream      = null;
    this._devicesNext = new Map(); // mac -> { name, mac, rateUp, rateDown }
    this._commitTimer = null;
    this._backoffTimer = null;
    this._backoffUntil = 0;
    this._baseBackoffMs = 60000;
    this._maxBackoffMs  = 600000;              // 10-minute cap
    this._backoffMs    = this._baseBackoffMs;  // doubles on each failure
    this._unavailable  = false;
    this._lastFp       = '';
    this._pollTimer    = null;
    this._pollInflight = false;
    this._heartbeatTimer = null;
    this._heartbeatArmedMs = 0;
    this._silenceTimer = null;
    this._sawData      = false;

    // Register lifecycle listeners once in the constructor so they never
    // accumulate across multiple start() calls (hot-swap safety).
    io.on('connection', () => {
      if (this.streamMode && !this._stream && !this._unavailable) this._startStream();
    });
    ros.on('close', () => {
      this._stopStream();
      // Stop the re-emit too: replaying a payload from before the disconnect would
      // keep the card looking live while the router is unreachable.
      this._stopHeartbeat();
      this._stopSilenceTimer();
      if (this._pollTimer) { clearTimeout(this._pollTimer); this._pollTimer = null; }
    });
    ros.on('connected', () => {
      this._backoffUntil = 0;
      this._backoffMs    = this._baseBackoffMs;
      // A reconnect is a deliberate re-probe: the router may have been upgraded
      // or had the kid-control package added, so the latch is cleared here — and
      // only here, plus the dormancy probe. Never on an ordinary successful tick.
      this._unavailable  = false;
      this._lastFp       = '';
      clearTimeout(this._backoffTimer);
      this._backoffTimer = null;
      this._stream = null;
      this._startTalkers();
    });
  }

  _startStream() {
    if (this._stream) return;
    if (!this.ros.connected) return;
    if (this._unavailable) return;
    if (Date.now() < this._backoffUntil) return;

    const intervalSec = Math.max(1, Math.round(this.pollMs / 1000));
    console.log('%s', this._lbl + ' streaming /ip/kid-control/device/print, interval=' + intervalSec + 's');

    const stream = this.ros.stream(
      '/ip/kid-control/device/print',
      [
        `=interval=${intervalSec}`,
        '=.proplist=name,mac-address,rate-up,rate-down',
      ],
      null
    );

    stream.on('data', (packet) => {
      this._sawData = true;
      // An array packet is an empty section marker. Commit on EVERY one, not only
      // on the non-empty -> empty edge: guarding on `_devicesNext.size > 0` meant
      // a table that was already empty committed nothing at all, so lastPayload
      // froze, no talkers:update reached the browser, and the card went stale on a
      // router that was answering perfectly well. The commit is cheap and the emit
      // is still fingerprint-gated, so a steadily empty table costs one no-op tick
      // per interval and no browser traffic.
      //
      // Do not rely on this firing, though — see _startSilenceTimer(). On a
      // streaming channel patch-routeros.js swallows RouterOS's `!empty`, so an
      // empty result set can produce no packet whatsoever.
      if (Array.isArray(packet)) {
        if (packet.length === 0) { this._devicesNext.clear(); this._scheduleCommit(); }
        return;
      }
      if (!packet || typeof packet !== 'object') return;
      const mac = packet['mac-address'];
      if (!mac) return;
      this._devicesNext.set(mac, {
        name:     packet.name || '',
        mac,
        rateUp:   parseInt(packet['rate-up']   || '0', 10),
        rateDown: parseInt(packet['rate-down'] || '0', 10),
      });
      this._scheduleCommit();
    });

    stream.on('error', (err) => {
      const msg = String(err && err.message ? err.message : err);
      this._stream = null;
      if (msg.includes('unknown command') || msg.includes('no such')) {
        // Feature not present on this router — disable permanently, no retries.
        this._unavailable = true;
        const now = Date.now();
        console.warn('%s', this._lbl + ' Kid Control not available on this router — disabling');
        const payload = { ts: now, devices: [], pollMs: this._reportedPollMs, available: false };
        this.lastPayload = payload;
        this.io.to('page-dashboard').emit('talkers:update', payload);
        this.state.lastTalkersTs  = now;
        this.state.lastTalkersErr = null;
      } else if (msg.includes('timeout')) {
        // Stream timeout on CHR/VM (limited API threads). Feature likely exists
        // but stream mode can't handle it — auto-downgrade to poll mode.
        console.warn('%s', this._lbl + ' stream timeout — switching to poll mode');
        this.streamMode = false;
        this._startTalkers();
      } else {
        console.error('%s', this._lbl + ' stream error:', msg);
        this.state.lastTalkersErr = msg;
        clearTimeout(this._backoffTimer);
        const delay = this._backoffMs;
        this._backoffMs = Math.min(this._backoffMs * 2, this._maxBackoffMs);
        this._backoffTimer = setTimeout(() => { this._backoffTimer = null; this._startStream(); }, delay);
      }
    });

    this._stream = stream;
  }

  // Every other streamed collector re-emits its last payload every 60 s so the
  // browser's stale timer never fires on a healthy but quiet router. Talkers was
  // the one that did not, while still advertising a poll interval — so the client
  // held it to a 23 s threshold that nothing was ever going to meet once the
  // device list stopped changing.
  // What the CLIENT is told, not what we schedule on. A streamed collector reports
  // 0 so the browser keeps its fixed stale threshold and lets the heartbeat set the
  // cadence; advertising a 3 s interval while streaming is what held this card to a
  // 23 s deadline nothing was going to meet. (queues/wan idiom.)
  //
  // A getter, not a field: the stream-timeout path flips streamMode at runtime, and
  // a value captured in the constructor would then describe the wrong delivery mode
  // for the rest of the session.
  get _reportedPollMs() { return this.streamMode ? 0 : this.pollMs; }

  // The heartbeat only does its job if it beats faster than the deadline this
  // collector itself advertises. The client sets its threshold to
  // `pollMs + STALE_GRACE(20 s)` for a polled collector, and keeps a fixed 90 s
  // for a streamed one (pollMs 0) — so a hardcoded 60 s is right for streaming
  // and useless for polling, where a 3 s interval means a 23 s deadline. That is
  // the hAP AC2 case: it runs collection mode "poll", so the card still went
  // stale ~30 s in, and only recovered when dormancy fired ~20 s after that.
  // Clamped at the call site, not only upstream. `pollMs` is already bounded to
  // POLL_BOUNDS.pollTalkers ([1000, 60000]) by Settings.load() and again by
  // clampPollValue(), so the ceiling is a no-op today — it is here because every
  // other collector's timer bounds itself inline as well, and this getter was
  // the one place that trusted its caller. Same shape as `_pollDelayMs` above.
  get _heartbeatMs() { return this.streamMode ? 60000 : clampPoll(this.pollMs, 5000, 60000, 5000); }

  /**
   * Treat prolonged silence on an open stream as the empty answer.
   *
   * RouterOS replies to an empty result set with `!empty`, and patch-routeros.js
   * deliberately SWALLOWS that on a streaming channel, because there it means
   * "nothing YET" rather than "nothing" (/interface/wifi/frequency-scan sends it
   * ~6 ms before delivering real rows ten seconds later). So on a router whose
   * kid-control table is empty, a stream-mode collector receives no packet at
   * all — not even the `[]` the data handler above is written for.
   *
   * That is why this looked fixed on the hAP AC2 and was not on the cAP AX: the
   * AC2 runs collection mode "poll", where ros.write() goes down a one-shot
   * channel and the same patch DOES turn `!empty` into an empty result.
   *
   * Silence on an interval print is the answer, so commit it. Unlike connections
   * this needs no confirming /print: the payload is empty either way, and if the
   * stream really is dead the existing error/backoff path owns that.
   */
  _startSilenceTimer() {
    if (this._silenceTimer) return;
    const intervalMs = Math.max(1000, this.pollMs);
    this._silenceTimer = setInterval(() => {
      if (!this.streamMode || !this._stream || this._unavailable) return;
      if (this._sawData) { this._sawData = false; return; }   // rows arrived; nothing to infer
      if (this._devicesNext.size > 0) return;                 // mid-batch, let the debounce commit
      this._commitTick();
    }, intervalMs * 3);
  }

  _stopSilenceTimer() {
    if (this._silenceTimer) { clearInterval(this._silenceTimer); this._silenceTimer = null; }
    this._sawData = false;
  }

  _startHeartbeat() {
    // Re-arm when the cadence changes: the stream-timeout path flips streamMode
    // and calls _startTalkers() again, which would otherwise hit the early return
    // below and leave a 60 s beat guarding a 23 s deadline.
    if (this._heartbeatTimer && this._heartbeatArmedMs !== this._heartbeatMs) this._stopHeartbeat();
    if (this._heartbeatTimer) return;
    this._heartbeatArmedMs = this._heartbeatMs;
    this._heartbeatTimer = setInterval(() => {
      if (!this.lastPayload) return;
      // Idle gate, as netwatch does: this re-emit exists only for browser stale
      // timers, so it has nothing to do when nobody is watching.
      if (this.io.engine.clientsCount === 0) return;
      this.io.to('page-dashboard').emit('talkers:update', { ...this.lastPayload, ts: Date.now() });
    }, this._heartbeatMs);
  }

  _stopHeartbeat() {
    if (this._heartbeatTimer) { clearInterval(this._heartbeatTimer); this._heartbeatTimer = null; }
    this._heartbeatArmedMs = 0;
  }

  _stopStream() {
    clearTimeout(this._commitTimer);  this._commitTimer  = null;
    clearTimeout(this._backoffTimer); this._backoffTimer = null;
    if (!this._stream) return;
    stopStreamSafe(this._stream);
    this._stream = null;
    this._devicesNext.clear();
  }

  _restartStream() {
    this._stopStream();
    this._startStream();
  }

  _scheduleCommit() {
    clearTimeout(this._commitTimer);
    this._commitTimer = setTimeout(() => this._commitTick(), 300);
  }

  _commitTick() {
    this._commitTimer  = null;
    // Reset the retry backoff on success, but NOT the _unavailable latch: a
    // successful tick means the stream recovered, never that a router which
    // answered "unknown command" has grown a kid-control menu. Clearing it here
    // un-latched the feature probe almost immediately.
    this._backoffMs    = this._baseBackoffMs;
    const now = Date.now();

    if (this.io.engine.clientsCount === 0) {
      this._devicesNext.clear();
      this._stopStream();
      return;
    }

    let devices = [...this._devicesNext.values()].map(d => ({
      name:    d.name,
      mac:     d.mac,
      tx_mbps: +(d.rateUp   / 1_000_000).toFixed(3),
      rx_mbps: +(d.rateDown / 1_000_000).toFixed(3),
    }));
    this._devicesNext.clear();

    devices.sort((a, b) => (b.rx_mbps + b.tx_mbps) - (a.rx_mbps + a.tx_mbps));
    devices = devices.slice(0, this.topN);

    const fp = JSON.stringify(devices.map(d => ({ mac: d.mac, tx: d.tx_mbps, rx: d.rx_mbps })));
    this.lastPayload = { ts: now, devices, pollMs: this._reportedPollMs, available: true };
    if (fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.to('page-dashboard').emit('talkers:update', this.lastPayload);
    }
    this.state.lastTalkersTs  = now;
    this.state.lastTalkersErr = null;
  }

  // ── poll-mode talkers path ────────────────────────────────────────────────

  async _pollTalkersOnce() {
    if (!this.ros.connected || this._pollInflight) return;
    if (this.io.engine.clientsCount === 0) return;
    this._pollInflight = true;
    try {
      const rows = await this.ros.write('/ip/kid-control/device/print', [
        '=.proplist=name,mac-address,rate-up,rate-down',
      ]);
      this._devicesNext.clear();
      if (Array.isArray(rows)) {
        for (const r of rows) {
          const mac = r['mac-address'];
          if (!mac) continue;
          this._devicesNext.set(mac, {
            name:     r.name || '',
            mac,
            rateUp:   parseInt(r['rate-up']   || '0', 10),
            rateDown: parseInt(r['rate-down'] || '0', 10),
          });
        }
      }
      this._commitTick();
    } catch (e) {
      const msg = String(e && e.message ? e.message : e);
      if (msg.includes('unknown command') || msg.includes('no such')) {
        // Feature not present — disable permanently, stop scheduling.
        if (!this._unavailable) {
          this._unavailable = true;
          console.warn('%s', this._lbl + ' poll: Kid Control not available — disabling');
          const now = Date.now();
          const payload = { ts: now, devices: [], pollMs: this._reportedPollMs, available: false };
          this.lastPayload = payload;
          this.io.to('page-dashboard').emit('talkers:update', payload);
          this.state.lastTalkersTs  = now;
          this.state.lastTalkersErr = null;
        }
      } else {
        // Timeout or other transient error — log, let normal scheduling continue.
        this.state.lastTalkersErr = msg;
      }
    } finally {
      this._pollInflight = false;
    }
  }

  _scheduleTalkersNext() {
    if (this._unavailable) return;
    clearTimeout(this._pollTimer);
    this._pollTimer = setTimeout(async () => {
      this._pollTimer = null;
      if (!this.streamMode) {
        await this._pollTalkersOnce();
        this._scheduleTalkersNext();
      }
    }, this._pollDelayMs);
  }

  _startTalkers() {
    // Both paths: poll mode is subject to the same idle/page gating, so it needs
    // the re-emit just as much once the device list settles.
    this._startHeartbeat();
    this._startSilenceTimer();
    if (this.streamMode) {
      this._startStream();
    } else {
      console.log('%s', this._lbl + ' poll mode — polling /ip/kid-control/device/print every', this.pollMs + 'ms');
      this._pollTalkersOnce();
      this._scheduleTalkersNext();
    }
  }

  start() {
    this._startTalkers();
  }

  suspend() {
    this._stopStream();
    this._stopHeartbeat();
    this._stopSilenceTimer();
    if (this._pollTimer) { clearTimeout(this._pollTimer); this._pollTimer = null; }
  }
  resume() {
    if (!this.ros.connected) return;
    // Latched off: resume() is an idle/page-gate wake-up, not a feature re-probe.
    // Without this, every socket reconnect re-opened the stream on a router that
    // had already answered "unknown command".
    if (this._unavailable) return;
    this._startHeartbeat();
    this._startSilenceTimer();
    // Same trap as ping: suspend() clears _pollTimer, so a resume that only
    // restarts the stream strands poll mode permanently once the last viewer
    // has ever gone away — which is why the Top Talkers card stayed stale.
    if (this.streamMode) { this._startStream(); return; }
    this._pollTalkersOnce();
    this._scheduleTalkersNext();
  }

  // Deliberate feature re-probe, as opposed to resume()'s idle wake-up: clears
  // the unsupported latch and the retry backoff, then reopens the channel. The
  // dormancy supervisor calls this on backoff expiry and on page focus.
  probe() {
    this._unavailable  = false;
    this._backoffUntil = 0;
    this._backoffMs    = this._baseBackoffMs;
    this._lastFp       = '';
    this.resume();
  }

  stop() {
    this._stopStream();
    this._stopHeartbeat();
    this._stopSilenceTimer();
    if (this._pollTimer) { clearTimeout(this._pollTimer); this._pollTimer = null; }
  }
}

module.exports = TopTalkersCollector;
