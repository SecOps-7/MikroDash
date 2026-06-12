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
 *     disable permanently (no retries, empty payload, silent card).
 *   "timeout" in stream mode → CHR/VM thread starvation; auto-downgrade to
 *     poll mode and restart. If poll also fails it goes through the poll
 *     handler below.
 *   "timeout" in poll mode → transient; log and retry normally.
 *   other stream errors → exponential backoff, retry stream.
 */

class TopTalkersCollector {
  constructor({ ros, io, pollMs, state, topN, streamMode }) {
    this.ros    = ros;
    this.io     = io;
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][talkers]` : '[talkers]';
    this.pollMs = pollMs;
    this._pollDelayMs = Number.isFinite(Number(pollMs)) ? Math.max(500, Math.min(60_000, Math.trunc(Number(pollMs)))) : 3000;
    this.state  = state;
    this.topN   = topN || 5;
    this.streamMode = streamMode !== false; // default true

    this._stream      = null;
    this._devicesNext = new Map(); // mac -> { name, mac, rateUp, rateDown }
    this._commitTimer = null;
    this._backoffTimer = null;
    this._backoffUntil = 0;
    this._backoffMs    = 60000;
    this._unavailable  = false;
    this._lastFp       = '';
    this._pollTimer    = null;
    this._pollInflight = false;

    // Register lifecycle listeners once in the constructor so they never
    // accumulate across multiple start() calls (hot-swap safety).
    io.on('connection', () => {
      if (this.streamMode && !this._stream) this._startStream();
    });
    ros.on('close', () => {
      this._stopStream();
      if (this._pollTimer) { clearTimeout(this._pollTimer); this._pollTimer = null; }
    });
    ros.on('connected', () => {
      this._backoffUntil = 0;
      this._backoffMs    = 60000;
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
    if (Date.now() < this._backoffUntil) return;

    const intervalSec = Math.max(1, Math.round(this.pollMs / 1000));
    console.log(this._lbl + ' streaming /ip/kid-control/device/print, interval=' + intervalSec + 's');

    const stream = this.ros.stream(
      '/ip/kid-control/device/print',
      [
        `=interval=${intervalSec}`,
        '=.proplist=name,mac-address,rate-up,rate-down',
      ],
      null
    );

    stream.on('data', (packet) => {
      if (!packet || typeof packet !== 'object' || Array.isArray(packet)) return;
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
        console.warn(this._lbl + ' Kid Control not available on this router — disabling');
        const payload = { ts: now, devices: [], pollMs: this.pollMs };
        this.lastPayload = payload;
        this.io.emit('talkers:update', payload);
        this.state.lastTalkersTs  = now;
        this.state.lastTalkersErr = null;
      } else if (msg.includes('timeout')) {
        // Stream timeout on CHR/VM (limited API threads). Feature likely exists
        // but stream mode can't handle it — auto-downgrade to poll mode.
        console.warn(this._lbl + ' stream timeout — switching to poll mode');
        this.streamMode = false;
        this._startTalkers();
      } else {
        console.error(this._lbl + ' stream error:', msg); // codeql[js/tainted-format-string]
        this.state.lastTalkersErr = msg;
        clearTimeout(this._backoffTimer);
        this._backoffTimer = setTimeout(() => { this._backoffTimer = null; this._startStream(); }, this._backoffMs);
      }
    });

    this._stream = stream;
  }

  _stopStream() {
    clearTimeout(this._commitTimer);  this._commitTimer  = null;
    clearTimeout(this._backoffTimer); this._backoffTimer = null;
    if (!this._stream) return;
    try { this._stream.stop().catch(() => {}); } catch (e) {}
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
    this._backoffMs    = 60000;
    this._unavailable  = false;
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
    this.lastPayload = { ts: now, devices, pollMs: this.pollMs };
    if (fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.emit('talkers:update', this.lastPayload);
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
          console.warn(this._lbl + ' poll: Kid Control not available — disabling');
          const now = Date.now();
          const payload = { ts: now, devices: [], pollMs: this.pollMs };
          this.lastPayload = payload;
          this.io.emit('talkers:update', payload);
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
    if (this.streamMode) {
      this._startStream();
    } else {
      console.log(this._lbl + ' poll mode — polling /ip/kid-control/device/print every', this.pollMs + 'ms'); // codeql[js/tainted-format-string]
      this._pollTalkersOnce();
      this._scheduleTalkersNext();
    }
  }

  start() {
    this._startTalkers();
  }

  suspend() {
    this._stopStream();
    if (this._pollTimer) { clearTimeout(this._pollTimer); this._pollTimer = null; }
  }
  resume() {
    if (this.streamMode && this.ros.connected) this._startStream();
  }

  stop() {
    this._stopStream();
    if (this._pollTimer) { clearTimeout(this._pollTimer); this._pollTimer = null; }
  }
}

module.exports = TopTalkersCollector;
