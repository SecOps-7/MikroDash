'use strict';

const { stopStreamSafe, createPollLoop } = require('./util');

class NetwatchCollector {
  constructor({ ros, io, state, pollMs, streamMode }) {
    this.streamMode = streamMode !== false;   // default stream
    this.pollMs     = Math.max(500, Math.min(600000, Number(pollMs) || 30000));
    this._poll      = createPollLoop(() => this._loadInitial(), () => this.pollMs);
    this.ros   = ros;
    this.io    = io;
    this._lbl  = ros.routerLabel ? `[${ros.routerLabel}][netwatch]` : '[netwatch]';
    this.pollMs = 60000; // contract metadata — event-driven via /listen; 60 s heartbeat
    this.state = state;

    this._hosts          = new Map(); // .id -> raw row
    this._lastFp         = '';
    this._stream         = null;
    this._restarting     = false;
    this._restartTimer   = null;
    this._heartbeat      = null;
    this._permissionDenied = false;
    this.lastPayload     = null;
  }

  _normalize(row) {
    return {
      id:      row['.id']  || '',
      host:    row.host    || '',
      type:    row.type    || 'icmp',
      status:  row.status  || 'unknown',
      name:    row.name    || '',
      // Free: the print above takes no proplist, so the comment is already in
      // the row and was simply being dropped here. It feeds the {{comment}}
      // notification variable — the operator's note about what a host IS, which
      // is the useful half of "NetWatch host 10.0.5.20 is unreachable".
      comment: row.comment || '',
    };
  }

  _emit() {
    const hosts = [...this._hosts.values()].map(r => this._normalize(r));
    // `comment` is deliberately NOT in this fingerprint, which looks like the
    // bug fixed across four collectors in 0.7.33 and is not. That rule is
    // "every field the PAGE DISPLAYS belongs in the fingerprint"; nothing
    // renders a NetWatch comment. It exists only to feed the {{comment}}
    // notification variable, which is read at alert time from the payload the
    // status change itself emits. Hashing it would buy re-emits with no
    // consumer.
    const fp = JSON.stringify(hosts.map(h => h.id + ':' + h.status));
    if (fp === this._lastFp && this.lastPayload) return;
    this._lastFp = fp;
    const payload = { hosts, ts: Date.now() };
    this.lastPayload = payload;
    this.state.lastNetwatchTs  = Date.now();
    this.state.lastNetwatchErr = null;
    this.io.to('page-dashboard').emit('netwatch:update', payload);
  }

  async _loadInitial() {
    try {
      const rows = await this.ros.write('/tool/netwatch/print');
      this._hosts.clear();
      for (const r of (rows || [])) {
        const id = r['.id'] || r.id;
        if (id) this._hosts.set(id, r);
      }
      console.log('%s', this._lbl, this._hosts.size, 'entr' + (this._hosts.size === 1 ? 'y' : 'ies') + ' loaded');
      this._emit();
    } catch (e) {
      const msg = e && e.message ? e.message : String(e);
      if (/not allowed|no such command/i.test(msg)) {
        this._permissionDenied = true;
        console.warn('%s', this._lbl + ' permission denied — netwatch alerts disabled');
        return;
      }
      console.error('%s', this._lbl + ' initial load failed:', msg);
      this.state.lastNetwatchErr = msg;
    }
  }

  _startStream() {
    if (this._stream || !this.ros.connected || this._permissionDenied) return;
    try {
      this._stream = this.ros.stream(['/tool/netwatch/listen'], (err, data) => {
        if (err) {
          console.error('%s', this._lbl + ' stream error:', err && err.message ? err.message : err);
          this.state.lastNetwatchErr = String(err && err.message ? err.message : err);
          this._stopStream();
          if (this.ros.connected && !this._restarting) {
            this._restarting = true;
            this._restartTimer = setTimeout(() => {
              this._restarting   = false;
              this._restartTimer = null;
              if (this.ros.connected) this._loadInitial().then(() => this._startStream());
            }, 3000);
          }
          return;
        }
        if (!data) return;
        const id = data['.id'] || data.id;
        if (!id) return;
        if (data['.dead'] === 'true' || data['.dead'] === true) {
          this._hosts.delete(id);
        } else {
          this._hosts.set(id, { ...(this._hosts.get(id) || {}), ...data });
        }
        this._emit();
      });
      console.log('%s', this._lbl + ' streaming /tool/netwatch/listen');
    } catch (e) {
      console.error('%s', this._lbl + ' stream start failed:', e && e.message ? e.message : e);
    }
  }

  _stopStream() {
    if (this._restartTimer) { clearTimeout(this._restartTimer); this._restartTimer = null; }
    this._restarting = false;
    if (this._stream) { stopStreamSafe(this._stream); this._stream = null; }
  }

  // Re-emits lastPayload every 60 s so the browser stale-timer (threshold 90 s)
  // never fires when the NetWatch state is stable and no change events arrive.
  _startHeartbeat() {
    if (this._heartbeat) return;
    this._heartbeat = setInterval(() => {
      if (!this.lastPayload) return;
      // Idle gate: state changes still reach the alerter via the /listen stream;
      // this re-emit only serves browser stale-timers.
      if (this.io.engine.clientsCount === 0) return;
      this.io.to('page-dashboard').emit('netwatch:update', { ...this.lastPayload, ts: Date.now() });
    }, 60000);
  }

  _stopHeartbeat() {
    if (this._heartbeat) { clearInterval(this._heartbeat); this._heartbeat = null; }
  }

  // Delivery switch (#105). /listen is event-driven and cheap when idle, but it
  // still holds an open channel, and concurrent channels are what strain a small
  // router. Poll mode swaps the listen stream for a periodic re-read using the
  // same _loadInitial() the stream path already runs on connect, so there is no
  // second parsing path to keep in step.
  _deliver() {
    if (this.streamMode) this._startStream();
    else this._poll.start();
  }

  _stopDelivery() {
    this._stopStream();
    this._poll.stop();
  }

  async start() {
    await this._loadInitial();
    this._deliver();
    this._startHeartbeat();

    this.ros.on('close', () => {
      this._stopDelivery();
      this._stopHeartbeat();
    });
    this.ros.on('connected', async () => {
      this._stopDelivery();
      this._stopHeartbeat();
      this._lastFp = '';
      await this._loadInitial();
      this._deliver();
      this._startHeartbeat();
    });
  }

  stop() {
    this._stopDelivery();
    this._stopHeartbeat();
    this._lastFp = '';
  }
}

module.exports = NetwatchCollector;
