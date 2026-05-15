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
 * Backoff: if the stream errors with "unknown command" or similar, Kid Control
 * is not licensed/configured on this router. The stream is stopped, an empty
 * payload is emitted, and a retry is scheduled (1 min → 2 min → … → 10 min).
 */

class TopTalkersCollector {
  constructor({ ros, io, pollMs, state, topN, streamMode }) {
    this.ros    = ros;
    this.io     = io;
    this.pollMs = pollMs;
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
  }

  _startStream() {
    if (this._stream) return;
    if (!this.ros.connected) return;
    if (Date.now() < this._backoffUntil) return;

    const intervalSec = Math.max(1, Math.round(this.pollMs / 1000));
    console.log('[talkers] streaming /ip/kid-control/device/print, interval=' + intervalSec + 's');

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
      if (msg.includes('timeout') || msg.includes('unknown command') || msg.includes('no such')) {
        this._unavailable  = true;
        const now = Date.now();
        this._backoffUntil = now + this._backoffMs;
        this._backoffMs    = Math.min(this._backoffMs * 2, 600000);
        console.warn('[talkers] Kid Control unavailable — backing off ' + Math.round(this._backoffMs / 1000) + 's');
        const payload = { ts: now, devices: [], pollMs: this.pollMs };
        this.lastPayload = payload;
        this.io.emit('talkers:update', payload);
        this.state.lastTalkersTs  = now;
        this.state.lastTalkersErr = null;
        clearTimeout(this._backoffTimer);
        this._backoffTimer = setTimeout(() => { this._backoffTimer = null; this._startStream(); }, this._backoffMs);
      } else {
        console.error('[talkers] stream error:', msg);
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
      tx_mbps: +((d.rateUp   * 8) / 1_000_000).toFixed(3),
      rx_mbps: +((d.rateDown * 8) / 1_000_000).toFixed(3),
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
      if (msg.includes('unknown command') || msg.includes('no such') || msg.includes('timeout')) {
        if (!this._unavailable) {
          this._unavailable = true;
          console.warn('[talkers] poll: Kid Control unavailable —', msg);
          const now = Date.now();
          const payload = { ts: now, devices: [], pollMs: this.pollMs };
          this.lastPayload = payload;
          this.io.emit('talkers:update', payload);
          this.state.lastTalkersTs  = now;
          this.state.lastTalkersErr = null;
        }
      } else {
        this.state.lastTalkersErr = msg;
      }
    } finally {
      this._pollInflight = false;
    }
  }

  _scheduleTalkersNext() {
    clearTimeout(this._pollTimer);
    this._pollTimer = setTimeout(async () => {
      this._pollTimer = null;
      if (!this.streamMode) {
        await this._pollTalkersOnce();
        this._scheduleTalkersNext();
      }
    }, this.pollMs);
  }

  _startTalkers() {
    if (this.streamMode) {
      this._startStream();
    } else {
      console.log('[talkers] poll mode — polling /ip/kid-control/device/print every', this.pollMs + 'ms');
      this._pollTalkersOnce();
      this._scheduleTalkersNext();
    }
  }

  start() {
    this._startTalkers();
    this.io.on('connection', () => {
      if (this.streamMode && !this._stream) this._startStream();
    });
    this.ros.on('close', () => {
      this._stopStream();
      if (this._pollTimer) { clearTimeout(this._pollTimer); this._pollTimer = null; }
    });
    this.ros.on('connected', () => {
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
