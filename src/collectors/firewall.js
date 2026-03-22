/**
 * Firewall collector — hybrid stream + counter poll.
 *
 * /ip/firewall/{filter,nat,mangle}/listen handles structural changes (rules
 * added, removed, enabled/disabled, reordered). On RouterOS builds that also
 * push counter updates via the stream, _applyUpdate merges them correctly.
 * On builds that do NOT push counter updates (most v7 stable builds), a
 * separate _pollCounters() runs on pollMs to re-fetch packet/byte counts
 * directly. This guarantees live counters regardless of RouterOS version.
 */
class FirewallCollector {
  constructor({ ros, io, pollMs, state, topN }) {
    this.ros    = ros;
    this.io     = io;
    this.pollMs = pollMs || 10000;
    this.state  = state;
    this.topN   = topN || 15;

    this._filter = [];
    this._nat    = [];
    this._mangle = [];

    this.prevCounts  = new Map();
    this._lastFp     = '';

    this._streams    = { filter: null, nat: null, mangle: null };
    this._restarting = { filter: false, nat: false, mangle: false };
    this._restartTimers = { filter: null, nat: null, mangle: null };
    this._heartbeat  = null;
    this._pollTimer  = null;
    this._pollInflight = false;
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
    const id = data['.id'];
    if (!id) return;

    if (data['.dead'] === 'true' || data['.dead'] === true) {
      this[table] = this[table].filter(r => r.id !== id);
      this.prevCounts.delete(id);
      return;
    }

    const existing = this[table].findIndex(r => r.id === id);
    if (existing >= 0) {
      const rule = this[table][existing];
      const packets = data.packets !== undefined ? parseInt(data.packets, 10) : rule.packets;
      const bytes   = data.bytes   !== undefined ? parseInt(data.bytes,   10) : rule.bytes;
      const prev    = this.prevCounts.get(id);
      const deltaPackets = prev ? Math.max(0, packets - prev.packets) : 0;
      if (id) this.prevCounts.set(id, { packets, bytes });
      if (data.disabled === 'true' || data.disabled === true) {
        this[table].splice(existing, 1);
        return;
      }
      this[table][existing] = {
        ...rule, packets, bytes, deltaPackets,
        ...(data.chain           !== undefined && { chain:       data.chain           || '' }),
        ...(data.action          !== undefined && { action:      data.action          || '?' }),
        ...(data.comment         !== undefined && { comment:     data.comment         || '' }),
        ...(data['src-address']  !== undefined && { srcAddress:  data['src-address']  || '' }),
        ...(data['dst-address']  !== undefined && { dstAddress:  data['dst-address']  || '' }),
        ...(data.protocol        !== undefined && { protocol:    data.protocol        || '' }),
        ...(data['dst-port']     !== undefined && { dstPort:     data['dst-port']     || '' }),
        ...(data['in-interface'] !== undefined && { inInterface: data['in-interface'] || '' }),
      };
    } else {
      const processed = this._processRule(data);
      if (processed) this[table].push(processed);
    }
  }

  _emit() {
    const all       = [...this._filter, ...this._nat, ...this._mangle];
    const topByHits = all.filter(r => r.packets > 0)
                         .sort((a, b) => b.packets - a.packets)
                         .slice(0, this.topN);

    const seenIds = new Set(all.map(r => r.id).filter(Boolean));
    for (const id of this.prevCounts.keys()) {
      if (!seenIds.has(id)) this.prevCounts.delete(id);
    }

    const fp = JSON.stringify({
      filter:   this._filter.map(r => ({ id: r.id, packets: r.packets, bytes: r.bytes })),
      nat:      this._nat.map(r    => ({ id: r.id, packets: r.packets, bytes: r.bytes })),
      mangle:   this._mangle.map(r => ({ id: r.id, packets: r.packets, bytes: r.bytes })),
    });

    const payload = {
      ts: Date.now(),
      filter:   this._filter,
      nat:      this._nat,
      mangle:   this._mangle,
      topByHits,
      pollMs:   this.pollMs,
    };
    this.lastPayload = payload;
    this.state.lastFirewallTs  = Date.now();
    this.state.lastFirewallErr = null;

    if (fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.emit('firewall:update', payload);
    }
  }

  // ── counter poll ─────────────────────────────────────────────────────────
  // Re-fetches just packet/byte counts on each table and merges them into the
  // stored rules. This catches counter increments on RouterOS builds that do
  // not push counter updates via the /listen stream.

  async _pollCounters() {
    if (!this.ros.connected || this._pollInflight) return;
    this._pollInflight = true;
    try {
      const safeGet = async (cmd) => {
        try {
          // Fetch only id/packets/bytes — no proplist restriction since some
          // RouterOS builds error or return empty when proplist is used on
          // firewall tables. Full rows are cheap enough at this frequency.
          return await this.ros.write(cmd);
        } catch { return []; }
      };
      const [filter, nat, mangle] = await Promise.all([
        safeGet('/ip/firewall/filter/print'),
        safeGet('/ip/firewall/nat/print'),
        safeGet('/ip/firewall/mangle/print'),
      ]);
      let changed = false;
      for (const [rows, table] of [[filter, '_filter'], [nat, '_nat'], [mangle, '_mangle']]) {
        for (const row of (rows || [])) {
          const id      = row['.id'];
          const packets = parseInt(row.packets || '0', 10);
          const bytes   = parseInt(row.bytes   || '0', 10);
          if (!id) continue;
          const idx = this[table].findIndex(r => r.id === id);
          if (idx < 0) continue;
          const rule = this[table][idx];
          const prev = this.prevCounts.get(id);
          if (prev && (packets !== rule.packets || bytes !== rule.bytes)) {
            const deltaPackets = Math.max(0, packets - (prev.packets || 0));
            this[table][idx] = { ...rule, packets, bytes, deltaPackets };
            this.prevCounts.set(id, { packets, bytes });
            changed = true;
          } else if (!prev) {
            this.prevCounts.set(id, { packets, bytes });
          }
        }
      }
      if (changed) this._emit();
    } catch (e) {
      console.error('[firewall] counter poll error:', e && e.message ? e.message : e);
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
    this._startCounterPoll();

    this.ros.on('close', () => { this._stopAllStreams(); this._stopHeartbeat(); this._stopCounterPoll(); });
    this.ros.on('connected', async () => {
      this._stopAllStreams();
      this._stopHeartbeat();
      this._stopCounterPoll();
      this.prevCounts.clear();
      this._lastFp = '';
      await this._loadInitial();
      this._startStream('filter', '/ip/firewall/filter/listen');
      this._startStream('nat',    '/ip/firewall/nat/listen');
      this._startStream('mangle', '/ip/firewall/mangle/listen');
      this._startHeartbeat();
      this._startCounterPoll();
    });
  }

  stop() {
    this._stopAllStreams();
    this._stopHeartbeat();
    this._stopCounterPoll();
  }
}

module.exports = FirewallCollector;
