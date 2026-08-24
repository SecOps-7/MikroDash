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
const { clampPoll, stopStreamSafe, createPollLoop } = require('./util');

class VpnCollector {
  constructor({ ros, io, pollMs, state, rid, streamMode }) {
    // Delivery per router (#105). Only the counter refresh is convertible; the
    // peer add/remove /listen stays, as polling it would miss transitions.
    this.streamMode = streamMode !== false;
    this._counterPoll = createPollLoop(() => this._pollCountersOnce(), () => this.pollMs);
    this.ros    = ros;
    this.io     = io;
    this._rid   = rid || null;
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][vpn]` : '[vpn]';
    this.pollMs = clampPoll(pollMs, 10000, 30000);
    this.state  = state;

    this._peers      = new Map(); // public-key -> raw peer row
    this._prev       = new Map(); // public-key -> { rx, tx, ts }
    this._lastFp     = '';
    this._debuggedOnce = false;
    this.lastPayload = null;

    this._stream              = null;
    this._restarting          = false;
    this._restartTimer        = null;
    this._heartbeat           = null;
    this._counterStream       = null;
    this._counterRestarting   = false;
    this._counterRestartTimer = null;
    this._emitDebounce        = null;

    // Non-WireGuard tunnels. These are polled, not streamed — none of the paths
    // supports /listen — so they are gated three ways: only while someone is
    // viewing (suspend/resume), latched off entirely when the router does not
    // have the subsystem, and backed off to a slow cadence once the tables have
    // come back empty a few times running. A WireGuard-only router therefore
    // settles at one cheap probe a minute and stops as soon as the page closes.
    this._ppp            = [];
    this._ipsec          = [];
    this._otherTimer     = null;
    this._otherEmpties   = 0;
    this._pppAvailable   = true;   // false once the router says "no such command"
    this._ipsecAvailable = true;
  }

  // ── helpers ───────────────────────────────────────────────────────────────

  _peerName(p) {
    if (p.name    && String(p.name).trim())            return String(p.name).trim();
    if (p.comment && String(p.comment).trim())         return String(p.comment).trim();
    if (p['allowed-address'] && String(p['allowed-address']).trim()) return String(p['allowed-address']).trim();
    return p['public-key'] ? p['public-key'].slice(0, 16) + '…' : '?';
  }

  // WireGuard re-keys roughly every 2 minutes while a peer is actually passing
  // traffic, so handshake *age* is the only real liveness signal. The previous
  // rule was "has this peer ever handshaken", which counted a peer that
  // disappeared days ago as connected, and contradicted the UI, which already
  // graded the same value by age and drew it red. Thresholds match that badge.
  static peerState(lastHandshake) {
    if (!lastHandshake || lastHandshake === 'never') return 'never';
    return VpnCollector.handshakeAgeSec(lastHandshake) < 180 ? 'active' : 'stale';
  }

  // Parse a RouterOS duration ("2m30s", "1h5m20s", "3d4h") to seconds.
  static handshakeAgeSec(s) {
    if (!s || s === 'never') return Infinity;
    let total = 0, m;
    if ((m = s.match(/(\d+)w/))) total += parseInt(m[1], 10) * 604800;
    if ((m = s.match(/(\d+)d/))) total += parseInt(m[1], 10) * 86400;
    if ((m = s.match(/(\d+)h/))) total += parseInt(m[1], 10) * 3600;
    if ((m = s.match(/(\d+)m/))) total += parseInt(m[1], 10) * 60;
    if ((m = s.match(/(\d+)s/))) total += parseInt(m[1], 10);
    return total;
  }

  _buildTunnels() {
    const now = Date.now();
    const tunnels = [];
    for (const p of this._peers.values()) {
      const lh        = p['last-handshake'] || '';
      const state     = VpnCollector.peerState(lh);
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
        // `id` addresses the peer for an edit; `publicKey` identifies it, and
        // is the field the write round-trips to prove the row is still the one
        // the operator was looking at. A public key is public by construction.
        //
        // `preshared-key` is NOT here and must never be. It is the secret half,
        // the form's `secret` type never reads one back, and audit.js masks it
        // on name alone. Adding it to this payload would put it on the wire to
        // every viewer of the VPN page.
        id: p['.id'] || '', publicKey: p['public-key'] || '',
        type: 'WireGuard', name, state,
        // For the {{comment}} notification variable. Reported as the router has
        // it, with no attempt to suppress a comment that happens to equal the
        // peer's name: _peerName falls back to the comment for a peer with no
        // name, so those two CAN be the same string. Hiding it in that case
        // would make the variable's output depend on router config the reader
        // cannot see, and the duplication belongs to _peerName's naming
        // fallback rather than to this field.
        comment: p.comment ? String(p.comment).trim() : '',
        // Named for what it actually is. WireGuard is stateless — there is no
        // session and therefore no uptime; this field has always held the time
        // since the last handshake, and calling it uptime made it read as one.
        lastHandshake: lh,
        keepalive:  p['persistent-keepalive'] || '',
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
    const fp = JSON.stringify({
      w: tunnels.map(t => ({
        name: t.name, state: t.state, rx: t.rx, tx: t.tx,
        rxRate: +t.rxRate.toFixed(2), txRate: +t.txRate.toFixed(2),
      })),
      // Included so a PPP session coming or going actually reaches the browser.
      // Uptime is excluded for the same reason last-handshake is: it ticks
      // constantly and would defeat the suppression.
      p: this._ppp.map(s => ({ n: s.name, s: s.service, a: s.address, rx: s.rx, tx: s.tx })),
      i: this._ipsec.map(s => ({ n: s.name, st: s.state, e: s.enc, a: s.auth })),
    });
    const payload = { ts: Date.now(), tunnels, ppp: this._ppp, ipsec: this._ipsec, pollMs: 0 };
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
      console.log('%s', this._lbl, `late-discovered peer: ${key.slice(0, 16)}…`);
    }
    this._scheduleEmit();
  }

  async _pollCountersOnce() {
    if (!this.ros.connected) return;
    try {
      const rows = (await this.ros.write('/interface/wireguard/peers/print', ['=detail='])) || [];
      for (const r of rows) this._onCounterRecord(r);   // same per-record path
    } catch (e) {
      console.error('%s', this._lbl + ' counter poll error:', e && e.message ? e.message : e);
    }
  }

  _startCounterStream() {
    if (!this.streamMode) { this._counterPoll.start(); return; }
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
      console.error('%s', this._lbl + ' counter stream error:', msg);
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
    console.log('%s', this._lbl + ` streaming /interface/wireguard/peers/print interval=${intervalSec}s`);
  }

  _stopCounterStream() {
    this._counterPoll.stop();
    if (this._counterRestartTimer) { clearTimeout(this._counterRestartTimer); this._counterRestartTimer = null; }
    this._counterRestarting = false;
    if (this._emitDebounce) { clearTimeout(this._emitDebounce); this._emitDebounce = null; }
    if (this._counterStream) {
      stopStreamSafe(this._counterStream);
      this._counterStream = null;
    }
  }

  // ── initial load ──────────────────────────────────────────────────────────

  /**
   * Re-read now, after a write, so the page shows what the router did.
   *
   * _loadInitial() rebuilds the peer map from scratch and emits, which is what
   * a peer that has just been added or removed needs — the counter stream only
   * ever updates peers already in the map.
   */
  async refreshNow() {
    if (!this.ros.connected) return;
    await this._loadInitial();
  }

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
        console.log('%s', this._lbl, `${this._peers.size} WireGuard peer(s) found on interfaces: ${ifaces}`);
        this._debuggedOnce = true;
      }
      this._emit();
    } catch (e) {
      console.error('%s', this._lbl + ' initial load failed:', e && e.message ? e.message : e);
    }
  }

  // ── other VPN types ───────────────────────────────────────────────────────
  // WireGuard was the only protocol MikroDash watched. PPP-based tunnels
  // (L2TP, PPTP, SSTP, OpenVPN) and IPsec are where the rest of #64 actually
  // lives: /ppp/active carries a genuine session uptime, which WireGuard has no
  // concept of, and the IPsec SAs carry the negotiated ciphers, which WireGuard
  // does not negotiate.
  //
  // /ppp/secret is deliberately NOT read — it holds credentials, and the active
  // session list already has everything worth displaying.

  static parsePppSessions(rows) {
    return (rows || []).filter(r => r && r.name).map(r => ({
      type:     'PPP',
      name:     r.name,
      service:  (r.service || '').toUpperCase(),   // l2tp | pptp | sstp | ovpn | pppoe
      address:  r.address || '',
      callerId: r['caller-id'] || '',
      uptime:   r.uptime || '',                    // a real session uptime
      rx:       parseInt(r['bytes-in']  || '0', 10),
      tx:       parseInt(r['bytes-out'] || '0', 10),
    }));
  }

  // Active peers carry the session; installed SAs carry the ciphers. Joined on
  // the peer address so each row can state what it actually negotiated.
  static parseIpsecPeers(peers, sas) {
    const byAddr = new Map();
    for (const sa of (sas || [])) {
      const addr = (sa['dst-address'] || '').split('/')[0];
      if (addr && !byAddr.has(addr)) byAddr.set(addr, sa);
    }
    return (peers || []).filter(p => p && (p['remote-address'] || p.id)).map(p => {
      const addr = (p['remote-address'] || '').split('/')[0];
      const sa   = byAddr.get(addr) || {};
      return {
        type:    'IPsec',
        name:    addr || '(peer)',
        state:   p.state || '',
        uptime:  p.uptime || '',
        side:    p.side || '',
        enc:     sa['enc-algorithm']  || '',
        auth:    sa['auth-algorithm'] || '',
      };
    });
  }

  // Polled rather than streamed: these tables change only when a tunnel comes
  // up or down, and none of the paths supports /listen. Absent subsystems are
  // latched off after the first "no such command" so an unsupported router is
  // not re-probed on every cycle.
  async _loadOtherVpns() {
    const tryRead = async (path, flag) => {
      if (this[flag] === false) return [];
      try {
        const rows = await this.ros.write(path, []);
        return (rows || []).filter(r => r && Object.keys(r).length);
      } catch (e) {
        const msg = String((e && e.message) || e).toLowerCase();
        if (msg.includes('no such') || msg.includes('unknown command')) this[flag] = false;
        return [];
      }
    };
    const [ppp, peers, sas] = await Promise.all([
      tryRead('/ppp/active/print',            '_pppAvailable'),
      tryRead('/ip/ipsec/active-peers/print', '_ipsecAvailable'),
      tryRead('/ip/ipsec/installed-sa/print', '_ipsecAvailable'),
    ]);
    this._ppp   = VpnCollector.parsePppSessions(ppp);
    this._ipsec = VpnCollector.parseIpsecPeers(peers, sas);
  }

  // Poll cadence for the non-WireGuard tables. Fast while something is up, slow
  // once they have been empty a few times running.
  static get OTHER_ACTIVE_MS() { return 10000; }
  static get OTHER_IDLE_MS()   { return 60000; }
  static get OTHER_EMPTY_LIMIT() { return 3; }

  _scheduleOtherVpns(delayMs) {
    if (this._otherTimer) { clearTimeout(this._otherTimer); this._otherTimer = null; }
    // Router has neither subsystem — stop entirely rather than probing forever.
    if (this._pppAvailable === false && this._ipsecAvailable === false) return;
    this._otherTimer = setTimeout(async () => {
      this._otherTimer = null;
      if (!this.ros.connected) return;
      const hadAny = this._ppp.length + this._ipsec.length;
      await this._loadOtherVpns();
      const hasAny = this._ppp.length + this._ipsec.length;
      if (hasAny) this._otherEmpties = 0;
      else        this._otherEmpties++;
      // Only emit when something actually changed state, so an idle router does
      // not push an identical payload every minute.
      if (hasAny || hadAny) this._emit();
      this._scheduleOtherVpns(this._otherEmpties >= VpnCollector.OTHER_EMPTY_LIMIT
        ? VpnCollector.OTHER_IDLE_MS : VpnCollector.OTHER_ACTIVE_MS);
    }, delayMs);
    if (this._otherTimer.unref) this._otherTimer.unref();
  }

  _stopOtherVpns() {
    if (this._otherTimer) { clearTimeout(this._otherTimer); this._otherTimer = null; }
  }

  // ── structural stream ─────────────────────────────────────────────────────

  _startStream() {
    if (this._stream) return;
    if (!this.ros.connected) return;
    try {
      this._stream = this.ros.stream(['/interface/wireguard/peers/listen'], (err, data) => {
        if (err) {
          console.error('%s', this._lbl + ' stream error:', err && err.message ? err.message : err);
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
      console.log('%s', this._lbl + ' streaming /interface/wireguard/peers/listen');
    } catch (e) {
      console.error('%s', this._lbl + ' stream start failed:', e && e.message ? e.message : e);
    }
  }

  _stopStream() {
    if (this._restartTimer) { clearTimeout(this._restartTimer); this._restartTimer = null; }
    this._restarting = false;
    if (this._stream) { stopStreamSafe(this._stream); this._stream = null; }
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
      this._stopOtherVpns();
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

  // The PPP/IPsec poll rides the same page-visibility gate as the counter
  // stream: nobody looking at the VPN page means no polling at all.
  suspend() { this._stopCounterStream(); this._stopOtherVpns(); }

  resume()  {
    this._startCounterStream();
    this._otherEmpties = 0;                 // re-probe promptly on reopen
    this._scheduleOtherVpns(0);
  }

  stop() {
    this._stopStream();
    this._stopHeartbeat();
    this._stopCounterStream();
    this._stopOtherVpns();
  }
}

module.exports = VpnCollector;
