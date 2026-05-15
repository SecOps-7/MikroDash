/**
 * Ping collector — streams /tool/ping with interval=N.
 *
 * Uses ros.stream() with null callback + 'data' event. RouterOS sends one
 * !re packet per ping result (replied or timeout). No count limit is set so
 * RouterOS pings indefinitely until the channel is cancelled.
 *
 * Loss is computed over a rolling window (last LOSS_WINDOW results) so a
 * single timeout doesn't jump the display to 100% and one recovery doesn't
 * immediately return to 0%.
 *
 * Backoff: stream errors matching "not enough privileges" / "permission denied"
 * set _permissionDenied and stop retrying — the API user needs the 'test' policy.
 */
const RingBuffer = require('../util/ringbuffer');

const MAX_HISTORY  = 60;
const LOSS_WINDOW  = 10; // rolling window for loss %

class PingCollector {
  constructor({ ros, io, pollMs, state, target, streamMode }) {
    this.ros    = ros;
    this.io     = io;
    this.pollMs = pollMs || 5000;
    this.state  = state;
    this.target = target || '1.1.1.1';
    this.streamMode = streamMode !== false; // default true

    this.history = new RingBuffer(MAX_HISTORY);
    this._stream       = null;
    this._pollTimer    = null;
    this._pollInflight = false;
    this._lastFp  = '';
    this.lastPayload       = null;
    this._permissionDenied = false;
    this._lossWindow       = []; // bool[] — true = replied
  }

  _parseRtt(val) {
    if (!val) return null;
    const m = String(val).match(/([\d.]+)(us|ms)?/);
    if (!m) return null;
    const v = parseFloat(m[1]);
    return m[2] === 'us' ? +(v / 1000).toFixed(3) : v;
  }

  _startStream() {
    if (this._stream || !this.ros.connected || this._permissionDenied) return;
    // RouterOS caps /tool/ping interval at 5 s (00:00:05); clamp to [1,5].
    const intervalSec = Math.min(5, Math.max(1, Math.round(this.pollMs / 1000)));
    console.log('[ping] streaming /tool/ping →', this.target, 'interval=' + intervalSec + 's');

    const stream = this.ros.stream(
      '/tool/ping',
      [
        '=address=' + this.target,
        '=interval=' + intervalSec,
      ],
      null
    );

    stream.on('data', (packet) => {
      if (!packet || typeof packet !== 'object' || Array.isArray(packet)) return;
      // Skip summary-like packets that have no time and no status
      if (!packet.time && !packet['response-time'] && !packet.status) return;
      this._processPacket(packet);
    });

    stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      this._stream = null;
      if (/not enough privileges|permission denied|cannot run/i.test(msg)) {
        this._permissionDenied = true;
        console.warn('[ping] test policy not granted — ping disabled. Add "test" to your RouterOS API user group to enable it.');
        const point = { ts: Date.now(), rtt: null, loss: null, permissionDenied: true };
        this.history.push(point);
        this.lastPayload = { target: this.target, rtt: null, loss: null, permissionDenied: true, ts: point.ts, pollMs: this.pollMs };
        this.io.emit('ping:update', this.lastPayload);
        this.state.lastPingTs = Date.now();
      } else {
        console.error('[ping] stream error:', msg);
        this.state.lastPingErr = msg;
        setTimeout(() => { if (this.ros.connected && !this._stream && !this._permissionDenied) this._startStream(); }, 3000);
      }
    });

    this._stream = stream;
  }

  _stopStream() {
    if (!this._stream) return;
    try { this._stream.stop().catch(() => {}); } catch (e) {}
    this._stream = null;
  }

  _restartStream() {
    this._stopStream();
    if (this.streamMode) this._startStream();
  }

  // ── poll-mode ping path ───────────────────────────────────────────────────

  async _pollPingOnce() {
    if (!this.ros.connected || this._pollInflight || this._permissionDenied) return;
    if (this.io.engine.clientsCount === 0) return;
    this._pollInflight = true;
    try {
      const rows = await this.ros.write('/tool/ping', [
        '=address=' + this.target,
        '=count=3',
        '=interval=1',
      ]);
      if (Array.isArray(rows)) {
        for (const r of rows) {
          if (r && (r.time || r['response-time'] || r.status)) this._processPacket(r);
        }
      }
    } catch (e) {
      const msg = String(e && e.message ? e.message : e);
      if (/not enough privileges|permission denied|cannot run/i.test(msg)) {
        this._permissionDenied = true;
        console.warn('[ping] poll: test policy not granted — ping disabled.');
        const point = { ts: Date.now(), rtt: null, loss: null, permissionDenied: true };
        this.history.push(point);
        this.lastPayload = { target: this.target, rtt: null, loss: null, permissionDenied: true, ts: point.ts, pollMs: this.pollMs };
        this.io.emit('ping:update', this.lastPayload);
        this.state.lastPingTs = Date.now();
      } else {
        this.state.lastPingErr = msg;
      }
    } finally {
      this._pollInflight = false;
    }
  }

  _schedulePingNext() {
    clearTimeout(this._pollTimer);
    this._pollTimer = setTimeout(async () => {
      this._pollTimer = null;
      if (!this.streamMode) {
        await this._pollPingOnce();
        this._schedulePingNext();
      }
    }, this.pollMs);
  }

  _processPacket(packet) {
    if (this.io.engine.clientsCount === 0) { this._stopStream(); return; }

    const replied = !packet.status || packet.status === 'replied';
    const rtt     = replied ? this._parseRtt(packet.time || packet['response-time']) : null;

    this._lossWindow.push(replied);
    if (this._lossWindow.length > LOSS_WINDOW) this._lossWindow.shift();
    const loss = this._lossWindow.length > 0
      ? Math.round((this._lossWindow.filter(v => !v).length / this._lossWindow.length) * 100)
      : 100;

    const point = { ts: Date.now(), rtt, loss };
    this.history.push(point);

    const fp = `${this.target}|${rtt}|${loss}`;
    this.lastPayload = { target: this.target, rtt, loss, ts: point.ts, pollMs: this.pollMs };
    if (fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.emit('ping:update', this.lastPayload);
    }
    this.state.lastPingTs  = Date.now();
    this.state.lastPingErr = null;
  }

  getHistory() {
    return { target: this.target, history: this.history.toArray() };
  }

  _startPing() {
    if (this.streamMode) {
      this._startStream();
    } else {
      console.log('[ping] poll mode — polling /tool/ping every', this.pollMs + 'ms');
      this._pollPingOnce();
      this._schedulePingNext();
    }
  }

  start() {
    this._startPing();
    this.io.on('connection', () => {
      if (this.streamMode && !this._stream) this._startStream();
    });
    this.ros.on('close', () => {
      this._stopStream();
      if (this._pollTimer) { clearTimeout(this._pollTimer); this._pollTimer = null; }
    });
    this.ros.on('connected', () => {
      this._lastFp = '';
      this._permissionDenied = false;
      this._stream = null;
      this._startPing();
    });
  }

  suspend() {
    this._stopStream();
    if (this._pollTimer) { clearTimeout(this._pollTimer); this._pollTimer = null; }
  }
  resume() {
    if (this.streamMode && this.ros.connected && !this._permissionDenied) this._startStream();
  }

  stop() {
    this._stopStream();
    if (this._pollTimer) { clearTimeout(this._pollTimer); this._pollTimer = null; }
  }
}

module.exports = PingCollector;
