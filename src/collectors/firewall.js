/**
 * Firewall collector — initial /print on connect, then /listen for changes.
 *
 * RouterOS fires a change event on the filter/nat/mangle tables whenever a
 * rule is added, removed, enabled/disabled, or its packet/byte counters are
 * updated (which happens on every matched packet). Counter updates are the
 * high-frequency case; the delta calculation below handles them the same way
 * the old polling approach did, but without the 10-second round-trip cost.
 *
 * Falls back to a one-shot re-fetch (no stream restart) on stream error so
 * the page doesn't go stale; the stream is restarted after a short delay.
 */
class FirewallCollector {
  constructor({ ros, io, pollMs, state, topN }) {
    this.ros    = ros;
    this.io     = io;
    this.pollMs = pollMs || 10000; // kept for Settings UI compatibility
    this.state  = state;
    this.topN   = topN || 15;

    // Raw rule stores — keyed by chain for quick rebuild
    this._filter = [];
    this._nat    = [];
    this._mangle = [];

    this.prevCounts  = new Map();
    this._lastFp     = '';

    // One stream per table
    this._streams    = { filter: null, nat: null, mangle: null };
    this._restarting = { filter: false, nat: false, mangle: false };
    this._restartTimers = { filter: null, nat: null, mangle: null };
    this._heartbeat  = null;
  }

  // ── helpers ──────────────────────────────────────────────────────────────

  _processRule(r) {
    if (r.disabled === 'true' || r.disabled === true) return null;
    const id      = r['.id'] || '';
    const packets = parseInt(r.packets || '0', 10);
    const bytes   = parseInt(r.bytes   || '0', 10);
    const prev    = this.prevCounts.get(id);
    const deltaPackets = prev ? Math.max(0, packets - prev.packets) : 0;
    if (id) this.prevCounts.set(id, { packets, bytes });
    return {
      id, chain: r.chain||'', action: r.action||'?', comment: r.comment||'',
      srcAddress: r['src-address']||'', dstAddress: r['dst-address']||'',
      protocol: r.protocol||'', dstPort: r['dst-port']||'',
      inInterface: r['in-interface']||'', packets, bytes, deltaPackets, disabled: false,
    };
  }

  _applyUpdate(table, data) {
    // data is a single row pushed by /listen — may be new, updated, or deleted
    const id = data['.id'];
    if (!id) return;

    if (data['.dead'] === 'true' || data['.dead'] === true) {
      this[table] = this[table].filter(r => r.id !== id);
      this.prevCounts.delete(id);
      return;
    }

    const processed = this._processRule(data);
    const existing  = this[table].findIndex(r => r.id === id);
    if (existing >= 0) {
      if (processed) this[table][existing] = processed;
      else            this[table].splice(existing, 1); // became disabled
    } else {
      if (processed) this[table].push(processed);
    }
  }

  _emit() {
    const all       = [...this._filter, ...this._nat, ...this._mangle];
    const topByHits = all.filter(r => r.packets > 0)
                         .sort((a, b) => b.packets - a.packets)
                         .slice(0, this.topN);

    // Prune prevCounts for rules no longer in any table
    const seenIds = new Set(all.map(r => r.id).filter(Boolean));
    for (const id of this.prevCounts.keys()) {
      if (!seenIds.has(id)) this.prevCounts.delete(id);
    }

    const fp = JSON.stringify({
      filter:   this._filter.map(r => ({ id: r.id, packets: r.packets, bytes: r.bytes })),
      nat:      this._nat.map(r    => ({ id: r.id, packets: r.packets })),
      topByHits: topByHits.map(r  => r.id),
    });

    const payload = {
      ts: Date.now(),
      filter:   this._filter,
      nat:      this._nat,
      mangle:   this._mangle,
      topByHits,
      pollMs:   0, // 0 = streamed, not polled
    };
    this.lastPayload = payload;
    this.state.lastFirewallTs  = Date.now();
    this.state.lastFirewallErr = null;

    if (fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.emit('firewall:update', payload);
    }
  }

  // ── initial load ─────────────────────────────────────────────────────────

  async _loadInitial() {
    const safeGet = async (cmd) => {
      try { const r = await this.ros.write(cmd); return Array.isArray(r) ? r : []; }
      catch { return []; }
    };
    const [filter, nat, mangle] = await Promise.all([
      safeGet('/ip/firewall/filter/print'),
      safeGet('/ip/firewall/nat/print'),
      safeGet('/ip/firewall/mangle/print'),
    ]);
    this._filter = filter.map(r => this._processRule(r)).filter(Boolean);
    this._nat    = nat.map(r    => this._processRule(r)).filter(Boolean);
    this._mangle = mangle.map(r => this._processRule(r)).filter(Boolean);
    this._emit();
  }

  // ── stream management ────────────────────────────────────────────────────

  _startStream(table, cmd) {
    if (this._streams[table]) return;
    if (!this.ros.connected) return;
    try {
      this._streams[table] = this.ros.stream([cmd], (err, data) => {
        if (err) {
          console.error(`[firewall] ${table} stream error:`, err && err.message ? err.message : err);
          this.state.lastFirewallErr = String(err && err.message ? err.message : err);
          this._stopStream(table);
          if (this.ros.connected && !this._restarting[table]) {
            this._restarting[table] = true;
            this._restartTimers[table] = setTimeout(() => {
              this._restarting[table] = false;
              this._restartTimers[table] = null;
              if (this.ros.connected) {
                this._loadInitial().then(() => this._startStream(table, cmd));
              }
            }, 3000);
          }
          return;
        }
        if (data) {
          this._applyUpdate('_' + table, data);
          this._emit();
        }
      });
      console.log(`[firewall] streaming /ip/firewall/${table}/listen`);
    } catch (e) {
      console.error(`[firewall] ${table} stream start failed:`, e && e.message ? e.message : e);
    }
  }

  _stopStream(table) {
    if (this._restartTimers[table]) { clearTimeout(this._restartTimers[table]); this._restartTimers[table] = null; }
    this._restarting[table] = false;
    if (this._streams[table]) { try { this._streams[table].stop(); } catch (_) {} this._streams[table] = null; }
  }

  _stopAllStreams() {
    for (const t of ['filter', 'nat', 'mangle']) this._stopStream(t);
  }

  // ── heartbeat ────────────────────────────────────────────────────────────

  _startHeartbeat() {
    if (this._heartbeat) return;
    this._heartbeat = setInterval(() => {
      if (this.lastPayload) this.io.emit('firewall:update', { ...this.lastPayload, ts: Date.now() });
    }, 60000);
  }

  _stopHeartbeat() {
    if (this._heartbeat) { clearInterval(this._heartbeat); this._heartbeat = null; }
  }

  // ── lifecycle ─────────────────────────────────────────────────────────────

  async start() {
    await this._loadInitial();
    this._startStream('filter', '/ip/firewall/filter/listen');
    this._startStream('nat',    '/ip/firewall/nat/listen');
    this._startStream('mangle', '/ip/firewall/mangle/listen');
    this._startHeartbeat();

    this.ros.on('close', () => { this._stopAllStreams(); this._stopHeartbeat(); });
    this.ros.on('connected', async () => {
      this._stopAllStreams();
      this._stopHeartbeat();
      this.prevCounts.clear();
      this._lastFp = '';
      await this._loadInitial();
      this._startStream('filter', '/ip/firewall/filter/listen');
      this._startStream('nat',    '/ip/firewall/nat/listen');
      this._startStream('mangle', '/ip/firewall/mangle/listen');
      this._startHeartbeat();
    });
  }
}

module.exports = FirewallCollector;
