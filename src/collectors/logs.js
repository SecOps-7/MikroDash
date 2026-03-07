/**
 * Logs collector — uses /log/listen as a push stream.
 * RouterOS sends each new log entry instantly as it's written.
 * Zero polling, zero seen-set needed — we just receive and forward.
 */
class LogsCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros = ros;
    this.io = io;
    this.state = state;
    this.stream = null;
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
      console.error('[logs] stream error:', this.state.lastLogsErr);
      this.stream = null;
      return;
    }
    if (!data || !data.message) return;

    const topicsRaw = data.topics || '';
    this.io.emit('logs:new', {
      ts:       Date.now(),
      time:     data.time    || '',
      topics:   topicsRaw,
      message:  data.message || '',
      severity: this._classify(topicsRaw),
    });

    this.state.lastLogsTs = Date.now();
    this.state.lastLogsErr = null;
  }

  _startStream() {
    if (this.stream) return;
    if (!this.ros.connected) return;
    try {
      this.stream = this.ros.stream(['/log/listen'], (err, data) => this._onEntry(err, data));
      console.log('[logs] streaming /log/listen');
    } catch (e) {
      this.state.lastLogsErr = String(e && e.message ? e.message : e);
      console.error('[logs] failed to start stream:', this.state.lastLogsErr);
    }
  }

  _stopStream() {
    if (this.stream) {
      try { this.stream.stop(); } catch (_) {}
      this.stream = null;
    }
  }

  start() {
    this._startStream();
    this.ros.on('connected', () => {
      this._stopStream();
      this._startStream();
    });
    this.ros.on('close', () => this._stopStream());
  }
}

module.exports = LogsCollector;
