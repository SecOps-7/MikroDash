'use strict';

class NetwatchCollector {
  constructor({ ros, io, state }) {
    this.ros   = ros;
    this.io    = io;
    this._lbl  = ros.routerLabel ? `[${ros.routerLabel}][netwatch]` : '[netwatch]';
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
    };
  }

  _emit() {
    const hosts = [...this._hosts.values()].map(r => this._normalize(r));
    const fp = JSON.stringify(hosts.map(h => h.id + ':' + h.status));
    if (fp === this._lastFp && this.lastPayload) return;
    this._lastFp = fp;
    const payload = { hosts, ts: Date.now() };
    this.lastPayload = payload;
    this.state.lastNetwatchTs  = Date.now();
    this.state.lastNetwatchErr = null;
    this.io.emit('netwatch:update', payload);
  }

  async _loadInitial() {
    try {
      const rows = await this.ros.write('/tool/netwatch/print');
      this._hosts.clear();
      for (const r of (rows || [])) {
        const id = r['.id'] || r.id;
        if (id) this._hosts.set(id, r);
      }
      console.log(this._lbl, this._hosts.size, 'entr' + (this._hosts.size === 1 ? 'y' : 'ies') + ' loaded');
      this._emit();
    } catch (e) {
      const msg = e && e.message ? e.message : String(e);
      if (/not allowed|no such command/i.test(msg)) {
        this._permissionDenied = true;
        console.warn(this._lbl + ' permission denied — netwatch alerts disabled');
        return;
      }
      console.error(this._lbl + ' initial load failed:', msg);
      this.state.lastNetwatchErr = msg;
    }
  }

  _startStream() {
    if (this._stream || !this.ros.connected || this._permissionDenied) return;
    try {
      this._stream = this.ros.stream(['/tool/netwatch/listen'], (err, data) => {
        if (err) {
          console.error(this._lbl + ' stream error:', err && err.message ? err.message : err);
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
      console.log(this._lbl + ' streaming /tool/netwatch/listen');
    } catch (e) {
      console.error(this._lbl + ' stream start failed:', e && e.message ? e.message : e);
    }
  }

  _stopStream() {
    if (this._restartTimer) { clearTimeout(this._restartTimer); this._restartTimer = null; }
    this._restarting = false;
    if (this._stream) { try { this._stream.stop(); } catch (_) {} this._stream = null; }
  }

  // Re-emits lastPayload every 60 s so the browser stale-timer (threshold 90 s)
  // never fires when the NetWatch state is stable and no change events arrive.
  _startHeartbeat() {
    if (this._heartbeat) return;
    this._heartbeat = setInterval(() => {
      if (!this.lastPayload) return;
      this.io.emit('netwatch:update', { ...this.lastPayload, ts: Date.now() });
    }, 60000);
  }

  _stopHeartbeat() {
    if (this._heartbeat) { clearInterval(this._heartbeat); this._heartbeat = null; }
  }

  async start() {
    await this._loadInitial();
    this._startStream();
    this._startHeartbeat();

    this.ros.on('close', () => {
      this._stopStream();
      this._stopHeartbeat();
    });
    this.ros.on('connected', async () => {
      this._stopStream();
      this._stopHeartbeat();
      this._lastFp = '';
      await this._loadInitial();
      this._startStream();
      this._startHeartbeat();
    });
  }

  stop() {
    this._stopStream();
    this._stopHeartbeat();
    this._lastFp = '';
  }
}

module.exports = NetwatchCollector;
