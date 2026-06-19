/**
 * VPN / WireGuard collector — hybrid stream + counter stream.
 *
 * /interface/wireguard/peers/listen handles structural changes (peers
 * added/removed). On RouterOS 7, the listen stream does NOT reliably push
 * live rx-bytes, tx-bytes, or last-handshake updates — these are computed
 * fields that RouterOS only emits on structural record changes, not on every
 * counter increment or handshake event.
 *
 * A separate =interval=N counter stream re-fetches all peer counters on a
 * router-managed schedule. This drives live rate calculation and last-handshake
 * display. The structural stream still handles peer add/remove instantly.
 *
 * The counter stream is stopped on idle (suspend) and restarted on resume so
 * RouterOS does no work when no clients are connected.
 */
class VpnCollector {
  constructor({ ros, io, pollMs, state, rid }) {
    this.ros    = ros;
    this.io     = io;
    this._rid   = rid || null;
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][vpn]` : '[vpn]';
    const _vPoll = Number.isFinite(Number(pollMs)) ? Math.trunc(Number(pollMs)) : 10000;
    this.pollMs = Math.max(500, Math.min(30000, _vPoll));
    this.state  = state;

    this._peers      = new Map(); // public-key -> raw peer row
    this._prev       = new Map(); // public-key -> { rx, tx, ts }
    this._lastFp     = '';
    this._debuggedOnce = false;

    this._stream              = null;
    this._restarting          = false;
    this._restartTimer        = null;
    this._heartbeat           = null;
    this._counterStream       = null;
    this._counterRestarting   = false;
    this._counterRestartTimer = null;
    this._emitDebounce        = null;
  }

  // ── helpers ───────────────────────────────────────────────────────────────

  _peerName(p) {
    if (p.name    && String(p.name).trim())            return String(p.name).trim();
    if (p.comment && String(p.comment).trim())         return String(p.comment).trim();
    if (p['allowed-address'] && String(p['allowed-address']).trim()) return String(p['allowed-address']).trim();
    return p['public-key'] ? p['public-key'].slice(0, 16) + '…' : '?';
  }

  _buildTunnels() {
    const now = Date.now();
    const tunnels = [];
    for (const p of this._peers.values()) {
      const lh        = p['last-handshake'] || '';
      const connected = lh && lh !== 'never';
      const name      = this._peerName(p);
      const rxBytes   = parseInt(p['rx'] ?? p['rx-bytes'] ?? '0', 10);
      const txBytes   = parseInt(p['tx'] ?? p['tx-bytes'] ?? '0', 10);
      const key       = p['public-key'] || name;
      const prev      = this._prev.get(key);
      let rxRate = 0, txRate = 0;
      if (prev && now > prev.ts) {
        const dtSec = (now - prev.ts) / 1000;
        rxRate = Math.max(0, (rxBytes - prev.rx) / dtSec);
        txRate = Math.max(0, (txBytes - prev.tx) / dtSec);
        // Peer went idle — bytes unchanged for more than 10 s
        if (rxBytes === prev.rx && txBytes === prev.tx && dtSec > 10) {
          rxRate = 0; txRate = 0;
        }
      }
      // Only advance timestamp when bytes actually changed, so dtSec always
      // spans a real measurement window even when the counter stream fires between
      // byte-counter updates.
      if (!prev || rxBytes !== prev.rx || txBytes !== prev.tx) {
        this._prev.set(key, { rx: rxBytes, tx: txBytes, ts: now });
      }
      tunnels.push({
        type: 'WireGuard', name,
        state: connected ? 'connected' : 'idle',
        uptime: lh,
        endpoint:   p['endpoint-address'] || p['current-endpoint-address'] || '',
        allowedIp:  p['allowed-address'] || '',
        interface:  p.interface || '',
        rx: rxBytes, tx: txBytes, rxRate, txRate,
      });
    }
    // Prune prev entries for peers no longer tracked
    const liveKeys = new Set([...this._peers.values()].map(p => p['public-key'] || this._peerName(p)));
    for (const k of this._prev.keys()) { if (!liveKeys.has(k)) this._prev.delete(k); }
    return tunnels;
  }

  _emit(force = false) {
    const tunnels = this._buildTunnels();
    // Fingerprint covers structural state, cumulative bytes, and rounded rates.
    // Including rxRate/txRate (rounded to 2dp) ensures the browser is updated
    // when throughput transitions to/from zero without forcing every identical
    // idle tick to emit. uptime (last-handshake) is excluded: it changes every
    // ~3 min even with zero traffic, causing spurious emits.
    const fp = JSON.stringify(tunnels.map(t => ({
      name: t.name, state: t.state, rx: t.rx, tx: t.tx,
      rxRate: +t.rxRate.toFixed(2), txRate: +t.txRate.toFixed(2),
    })));
    const payload = { ts: Date.now(), tunnels, pollMs: 0 };
    this.lastPayload = payload;
    this.state.lastVpnTs  = Date.now();
    this.state.lastVpnErr = null;
    if (force || fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.to('page-vpn').to('dash-card-vpn').emit('vpn:update', payload);
    }
  }

  // ── counter stream ────────────────────────────────────────────────────────
  // =interval=N stream re-fetches peer counters (rx, tx, last-handshake) on a
  // router-managed schedule. Stopped on idle; restarted on resume.

  _scheduleEmit() {
    if (this._emitDebounce) return;
    this._emitDebounce = setTimeout(() => { // codeql[js/resource-exhaustion]
      this._emitDebounce = null;
      this._emit();
    }, 50);
  }

  _onCounterRecord(row) {
    const key = row['public-key'] || this._peerName(row);
    const existing = this._peers.get(key);
    if (existing) {
      this._peers.set(key, {
        ...existing,
        'rx':             row['rx']             ?? existing['rx'],
        'tx':             row['tx']             ?? existing['tx'],
        'last-handshake': row['last-handshake'] || existing['last-handshake'],
        'endpoint-address':         row['endpoint-address']         || existing['endpoint-address'],
        'current-endpoint-address': row['current-endpoint-address'] || existing['current-endpoint-address'],
      });
    } else {
      // Peer not yet in map — RouterOS returned incomplete results during
      // early boot when _loadInitial() ran. Add it now so it appears immediately
      // without waiting for a stream event.
      this._peers.set(key, row);
      console.log(this._lbl, `late-discovered peer: ${key.slice(0, 16)}…`);
    }
    this._scheduleEmit();
  }

  _startCounterStream() {
    if (this._counterStream || this._counterRestarting) return;
    if (!this.ros.connected) return;
    const intervalSec = Math.max(1, Math.round(this.pollMs / 1000));
    const stream = this.ros.stream(
      ['/interface/wireguard/peers/print', '=detail=', `=interval=${intervalSec}`],
      null
    );
    this._counterStream = stream;
    stream.on('data', (pkt) => {
      if (!pkt || typeof pkt !== 'object' || Array.isArray(pkt)) return;
      this._onCounterRecord(pkt);
    });
    stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      console.error(this._lbl + ' counter stream error:', msg);
      this._stopCounterStream();
      if (this.ros.connected && !this._counterRestarting) {
        this._counterRestarting = true;
        this._counterRestartTimer = setTimeout(() => { // codeql[js/resource-exhaustion]
          this._counterRestarting = false;
          this._counterRestartTimer = null;
          this._startCounterStream();
        }, 3000);
      }
    });
    console.log(this._lbl + ` streaming /interface/wireguard/peers/print interval=${intervalSec}s`);
  }

  _stopCounterStream() {
    if (this._counterRestartTimer) { clearTimeout(this._counterRestartTimer); this._counterRestartTimer = null; }
    this._counterRestarting = false;
    if (this._emitDebounce) { clearTimeout(this._emitDebounce); this._emitDebounce = null; }
    if (this._counterStream) {
      try { this._counterStream.stop(); } catch (_) {}
      this._counterStream = null;
    }
  }

  // ── initial load ──────────────────────────────────────────────────────────

  async _loadInitial() {
    try {
      const rows = await this.ros.write('/interface/wireguard/peers/print', ['=detail=']);
      this._peers.clear();
      for (const p of (rows || [])) {
        const key = p['public-key'] || this._peerName(p);
        this._peers.set(key, p);
      }
      if (!this._debuggedOnce && this._peers.size > 0) {
        const ifaces = [...new Set([...this._peers.values()].map(p => p.interface).filter(Boolean))].join(', ') || '?';
        console.log(this._lbl, `${this._peers.size} WireGuard peer(s) found on interfaces: ${ifaces}`);
        this._debuggedOnce = true;
      }
      this._emit();
    } catch (e) {
      console.error(this._lbl + ' initial load failed:', e && e.message ? e.message : e);
    }
  }

  // ── structural stream ─────────────────────────────────────────────────────

  _startStream() {
    if (this._stream) return;
    if (!this.ros.connected) return;
    try {
      this._stream = this.ros.stream(['/interface/wireguard/peers/listen'], (err, data) => {
        if (err) {
          console.error(this._lbl + ' stream error:', err && err.message ? err.message : err);
          this.state.lastVpnErr = String(err && err.message ? err.message : err);
          this._stopStream();
          if (this.ros.connected && !this._restarting) {
            this._restarting = true;
            this._restartTimer = setTimeout(() => {
              this._restarting  = false;
              this._restartTimer = null;
              if (this.ros.connected) this._loadInitial().then(() => this._startStream());
            }, 3000);
          }
          return;
        }
        if (!data) return;
        const key = data['public-key'] || this._peerName(data);
        if (data['.dead'] === 'true' || data['.dead'] === true) {
          this._peers.delete(key);
          this._prev.delete(key);
        } else {
          const existing = this._peers.get(key) || {};
          this._peers.set(key, { ...existing, ...data });
        }
        this._emit();
      });
      console.log(this._lbl + ' streaming /interface/wireguard/peers/listen');
    } catch (e) {
      console.error(this._lbl + ' stream start failed:', e && e.message ? e.message : e);
    }
  }

  _stopStream() {
    if (this._restartTimer) { clearTimeout(this._restartTimer); this._restartTimer = null; }
    this._restarting = false;
    if (this._stream) { try { this._stream.stop(); } catch (_) {} this._stream = null; }
  }

  // ── heartbeat ────────────────────────────────────────────────────────────
  // Re-emits lastPayload once per minute so the dashboard stale-timer never
  // fires when peers are stable and the counter stream is suppressed by dirty-check.

  _startHeartbeat() {
    if (this._heartbeat) return;
    this._heartbeat = setInterval(() => {
      if (!this.lastPayload) return;
      this.io.to('page-vpn').to('dash-card-vpn').emit('vpn:update', { ...this.lastPayload, ts: Date.now() });
    }, 60000);
  }

  _stopHeartbeat() {
    if (this._heartbeat) { clearInterval(this._heartbeat); this._heartbeat = null; }
  }

  // ── lifecycle ─────────────────────────────────────────────────────────────

  async start() {
    await this._loadInitial();
    this._startStream();
    this._startHeartbeat();
    this._startCounterStream();

    this.ros.on('close', () => {
      this._stopStream();
      this._stopHeartbeat();
      this._stopCounterStream();
    });
    this.ros.on('connected', async () => {
      this._stopStream();
      this._stopHeartbeat();
      this._stopCounterStream();
      this._prev.clear();
      this._lastFp = '';
      await this._loadInitial();
      this._startStream();
      this._startHeartbeat();
      // counter stream restarted externally by _updateVpnStreams() (page-awareness)
    });
  }

  suspend() { this._stopCounterStream(); }

  resume()  { this._startCounterStream(); }

  stop() {
    this._stopStream();
    this._stopHeartbeat();
    this._stopCounterStream();
  }
}

module.exports = VpnCollector;
