/**
 * Logs collector — uses /log/listen as a push stream.
 * RouterOS sends each new log entry instantly as it's written.
 * Zero polling, zero seen-set needed — we just receive and forward.
 */
const RingBuffer = require('../util/ringbuffer');
const LOG_HISTORY_SIZE = parseInt(process.env.LOG_HISTORY_SIZE || '500', 10);

class LogsCollector {
  constructor({ ros, io, _pollMs, state, _restartDelayMs }) {
    this.ros = ros;
    this.io = io;
    this._lbl = ros.routerLabel ? `[${ros.routerLabel}][logs]` : '[logs]';
    this.state = state;
    this._restartDelayMs = _restartDelayMs || 2000;
    this._backoffMs     = this._restartDelayMs; // grows on each failure
    this._maxBackoffMs  = 300000;               // 5-minute cap
    this.stream = null;
    this._restarting = false;
    this._restartTimer = null;
    this._loadingInitial = false;
    this._history = new RingBuffer(LOG_HISTORY_SIZE);
  }

  getHistory() {
    return this._history.toArray();
  }

  _classify(topicsRaw) {
    const t = String(topicsRaw).toLowerCase();
    if (t.includes('critical') || t.includes('error')) return 'error';
    if (t.includes('warning')) return 'warning';
    if (t.includes('debug'))   return 'debug';
    return 'info';
  }

  _onEntry(err, data) {
    if (err) {
      this.state.lastLogsErr = String(err && err.message ? err.message : err);
      console.error(this._lbl + ' stream error:', this.state.lastLogsErr);
      this._stopStream();
      if (this.ros.connected && !this._restarting) {
        this._restarting = true;
        const delay = this._backoffMs;
        this._backoffMs = Math.min(this._backoffMs * 2, this._maxBackoffMs);
        this._restartTimer = setTimeout(() => {
          this._restarting = false;
          this._restartTimer = null;
          if (this.ros.connected) this._startStream();
        }, delay);
      }
      return;
    }
    if (!data || !data.message) return;

    const topicsRaw = data.topics || '';
    const entry = {
      ts:       Date.now(),
      time:     data.time    || '',
      topics:   topicsRaw,
      message:  data.message || '',
      severity: this._classify(topicsRaw),
    };
    this._history.push(entry);
    this.io.to('page-logs').to('dash-card-logs').emit('logs:new', entry);

    this.state.lastLogsTs = Date.now();
    this.state.lastLogsErr = null;
    this._backoffMs = this._restartDelayMs; // reset on successful entry
  }

  async _loadInitial() {
    if (!this.ros.connected || this._loadingInitial) return;
    this._loadingInitial = true;
    try {
      const rows = await this.ros.write('/log/print', ['=.proplist=time,topics,message']);
      const all = rows || [];
      const recent = all.length > LOG_HISTORY_SIZE ? all.slice(-LOG_HISTORY_SIZE) : all;
      for (const data of recent) {
        if (!data.message) continue;
        const topicsRaw = data.topics || '';
        this._history.push({
          ts: Date.now(), time: data.time || '', topics: topicsRaw,
          message: data.message, severity: this._classify(topicsRaw),
        });
      }
      if (this.io.engine && this.io.engine.clientsCount > 0)
        this.io.to('page-logs').to('dash-card-logs').emit('logs:history', this.getHistory());
    } catch (e) {
      console.error(this._lbl + ' initial log fetch failed:', e && e.message ? e.message : e); // codeql[js/tainted-format-string]
    } finally {
      this._loadingInitial = false;
    }
  }

  _startStream() {
    if (this.stream) return;
    if (!this.ros.connected) return;
    try {
      this.stream = this.ros.stream(['/log/listen'], (err, data) => this._onEntry(err, data));
      this._backoffMs = this._restartDelayMs; // reset backoff on successful stream creation
      console.log(this._lbl + ' streaming /log/listen');
    } catch (e) {
      this.state.lastLogsErr = String(e && e.message ? e.message : e);
      console.error(this._lbl + ' failed to start stream:', this.state.lastLogsErr);
    }
  }

  _stopStream() {
    if (this._restartTimer) { clearTimeout(this._restartTimer); this._restartTimer = null; }
    this._restarting = false;
    if (this.stream) {
      try { this.stream.stop(); } catch (_) {}
      this.stream = null;
    }
  }

  start() {
    this._loadInitial().catch(() => {});
    this._startStream();
    this.ros.on('connected', () => {
      this._backoffMs = this._restartDelayMs;
      this._stopStream();
      this._history = new RingBuffer(LOG_HISTORY_SIZE);
      this._loadInitial().catch(() => {});
      this._startStream();
    });
    this.ros.on('close', () => this._stopStream());
  }

  stop() { this._stopStream(); }
}

module.exports = LogsCollector;
