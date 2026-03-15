/**
 * VPN / WireGuard collector — initial /print on connect, then /listen.
 *
 * WireGuard peer stats (rx-bytes, tx-bytes, last-handshake) update on every
 * handshake and packet exchange, so the listen stream fires frequently when
 * peers are active — but only then. When all peers are idle RouterOS sends
 * nothing, which is exactly the right behaviour vs a blind 10s poll.
 */
class VpnCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros    = ros;
    this.io     = io;
    this.pollMs = pollMs || 10000; // kept for Settings UI compatibility
    this.state  = state;

    this._peers      = new Map(); // public-key -> raw peer row
    this._prev       = new Map(); // public-key -> { rx, tx, ts }
    this._lastFp     = '';
    this._debuggedOnce = false;

    this._stream       = null;
    this._restarting   = false;
    this._restartTimer = null;
    this._heartbeat    = null; // slow emit so stale timer resets even when peers are idle
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
      const rxBytes   = parseInt(p['rx-bytes'] || '0', 10);
      const txBytes   = parseInt(p['tx-bytes'] || '0', 10);
      const key       = p['public-key'] || name;
      const prev      = this._prev.get(key);
      let rxRate = 0, txRate = 0;
      if (prev && now > prev.ts) {
        const dtSec = (now - prev.ts) / 1000;
        rxRate = Math.max(0, (rxBytes - prev.rx) / dtSec);
        txRate = Math.max(0, (txBytes - prev.tx) / dtSec);
      }
      this._prev.set(key, { rx: rxBytes, tx: txBytes, ts: now });
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

  _emit() {
    const tunnels = this._buildTunnels();
    const fp      = JSON.stringify(tunnels.map(t => ({ name: t.name, state: t.state, uptime: t.uptime, rx: t.rx, tx: t.tx })));
    const payload = { ts: Date.now(), tunnels, pollMs: 0 }; // 0 = streamed, not polled
    this.lastPayload = payload;
    this.state.lastVpnTs  = Date.now();
    this.state.lastVpnErr = null;
    if (fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.emit('vpn:update', payload);
    }
  }

  // ── initial load ──────────────────────────────────────────────────────────

  async _loadInitial() {
    try {
      const rows = await this.ros.write('/interface/wireguard/peers/print');
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
        } else {
          // Merge update into existing peer row so we always have full data
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
  // Emit once per minute so the dashboard stale-timer never fires when the
  // stream is healthy but peers happen to be idle (no handshakes/traffic).

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
    this.ros.on('close', () => { this._stopStream(); this._stopHeartbeat(); });
    this.ros.on('connected', async () => {
      this._stopStream();
      this._stopHeartbeat();
      this._prev.clear();
      this._lastFp = '';
      await this._loadInitial();
      this._startStream();
      this._startHeartbeat();
    });
  }
}

module.exports = VpnCollector;
