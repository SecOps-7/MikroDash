/**
 * VPN / WireGuard collector — hybrid stream + counter poll.
 *
 * /interface/wireguard/peers/listen handles structural changes (peers
 * added/removed). On RouterOS 7, the listen stream does NOT reliably push
 * live rx-bytes, tx-bytes, or last-handshake updates — these are computed
 * fields that RouterOS only emits on structural record changes, not on every
 * counter increment or handshake event. This matches the same behaviour seen
 * in the firewall collector where a dedicated counter poll was required.
 *
 * A separate _pollCounters() runs on pollMs to re-fetch all peer counters
 * directly via /print. This drives live rate calculation and last-handshake
 * display. The stream still handles peer add/remove instantly.
 */
class VpnCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros    = ros;
    this.io     = io;
    this.pollMs = pollMs || 10000;
    this.state  = state;

    this._peers      = new Map(); // public-key -> raw peer row
    this._prev       = new Map(); // public-key -> { rx, tx, ts }
    this._lastFp     = '';
    this._debuggedOnce = false;

    this._stream       = null;
    this._restarting   = false;
    this._restartTimer = null;
    this._heartbeat    = null;
    this._pollTimer    = null;
    this._pollInflight = false;
  }

  // ── helpers ───────────────────────────────────────────────────────────────

  _peerName(p) {
    if (p.name    && String(p.name).trim())            return String(p.name).trim();
    if (p.comment && String(p.comment).trim())         return String(p.comment).trim();
    if (p['allowed-address'] && String(p['allowed-address']).trim()) return String(p['allowed-address']).trim();
    return p['public-key'] ? p['public-key'].slice(0, 16) + '\u2026' : '?';
  }

  _buildTunnels() {
    const now = Date.now();
    const tunnels = [];
    for (const p of this._peers.values()) {
      const lh        = p['last-handshake'] || '';
      const connected = lh && lh !== 'never';
      const name      = this._peerName(p);
      const rxBytes   = parseInt(p['rx'] || p['rx-bytes'] || '0', 10);
      const txBytes   = parseInt(p['tx'] || p['tx-bytes'] || '0', 10);
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
      // spans a real measurement window even when the poll fires between
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
    const payload = { ts: Date.now(), tunnels, pollMs: this.pollMs };
    this.lastPayload = payload;
    this.state.lastVpnTs  = Date.now();
    this.state.lastVpnErr = null;
    if (force || fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.emit('vpn:update', payload);
    }
  }

  // ── counter poll ──────────────────────────────────────────────────────────
  // Re-fetches peer counters (rx-bytes, tx-bytes, last-handshake) on a
  // regular interval. The /listen stream handles structural changes; this
  // poll drives live rate and handshake-age updates.

  async _pollCounters() {
    if (!this.ros.connected || this._pollInflight) return;
    if (this.io.engine.clientsCount === 0) return;
    this._pollInflight = true;
    try {
      const rows = await this.ros.write('/interface/wireguard/peers/print', ['=detail=']);
      for (const row of (rows || [])) {
        const key = row['public-key'] || this._peerName(row);
        const existing = this._peers.get(key);
        if (existing) {
            // Merge only counter fields — preserve structural fields from the stream
          this._peers.set(key, {
            ...existing,
            'rx':             row['rx']             ?? existing['rx'],
            'tx':             row['tx']             ?? existing['tx'],
            'last-handshake': row['last-handshake'] || existing['last-handshake'],
            'endpoint-address':         row['endpoint-address']         || existing['endpoint-address'],
            'current-endpoint-address': row['current-endpoint-address'] || existing['current-endpoint-address'],
          });
        }
      }
      this._emit(); // dirty-check suppresses emit when bytes and rates are unchanged
    } catch (e) {
      console.error('[vpn] counter poll error:', e && e.message ? e.message : e);
    } finally {
      this._pollInflight = false;
    }
  }

  _startCounterPoll() {
    if (this._pollTimer) return;
    this._pollTimer = setInterval(() => this._pollCounters(), this.pollMs);
  }

  _stopCounterPoll() {
    if (this._pollTimer) { clearInterval(this._pollTimer); this._pollTimer = null; }
    this._pollInflight = false;
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
        console.log(`[vpn] ${this._peers.size} WireGuard peer(s) found on interfaces: ${ifaces}`);

        this._debuggedOnce = true;
      }
      this._emit();
    } catch (e) {
      console.error('[vpn] initial load failed:', e && e.message ? e.message : e);
    }
  }

  // ── stream management ─────────────────────────────────────────────────────

  _startStream() {
    if (this._stream) return;
    if (!this.ros.connected) return;
    try {
      this._stream = this.ros.stream(['/interface/wireguard/peers/listen'], (err, data) => {
        if (err) {
          console.error('[vpn] stream error:', err && err.message ? err.message : err);
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
      console.log('[vpn] streaming /interface/wireguard/peers/listen');
    } catch (e) {
      console.error('[vpn] stream start failed:', e && e.message ? e.message : e);
    }
  }

  _stopStream() {
    if (this._restartTimer) { clearTimeout(this._restartTimer); this._restartTimer = null; }
    this._restarting = false;
    if (this._stream) { try { this._stream.stop(); } catch (_) {} this._stream = null; }
  }

  // ── heartbeat ────────────────────────────────────────────────────────────
  // Re-emits lastPayload once per minute so the dashboard stale-timer never
  // fires when peers are stable and the poll is suppressed by dirty-check.

  _startHeartbeat() {
    if (this._heartbeat) return;
    this._heartbeat = setInterval(() => {
      if (this.lastPayload) this.io.emit('vpn:update', { ...this.lastPayload, ts: Date.now() });
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
    this._startCounterPoll();

    this.ros.on('close', () => {
      this._stopStream();
      this._stopHeartbeat();
      this._stopCounterPoll();
    });
    this.ros.on('connected', async () => {
      this._stopStream();
      this._stopHeartbeat();
      this._stopCounterPoll();
      this._prev.clear();
      this._lastFp = '';
      await this._loadInitial();
      this._startStream();
      this._startHeartbeat();
      this._startCounterPoll();
    });
  }

  stop() {
    this._stopStream();
    this._stopHeartbeat();
    this._stopCounterPoll();
  }
}

module.exports = VpnCollector;
