/**
 * Firewall collector — hybrid stream + counter stream.
 *
 * /ip/firewall/{filter,nat,mangle,raw}/listen handles structural changes (rules
 * added, removed, enabled/disabled, reordered). On RouterOS builds that also
 * push counter updates via the stream, _applyUpdate merges them correctly.
 * On builds that do NOT push counter updates (most v7 stable builds), a
 * separate =interval=N counter stream re-fetches packet/byte counts directly.
 * This guarantees live counters regardless of RouterOS version.
 *
 * Counter streams are stopped on idle (suspend) and restarted on resume so
 * RouterOS does no work when no clients are connected.
 */
class FirewallCollector {
  constructor({ ros, io, pollMs, state, topN }) {
    this.ros    = ros;
    this.io     = io;
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][firewall]` : '[firewall]';
    const _fPoll = Number.isFinite(Number(pollMs)) ? Math.trunc(Number(pollMs)) : 10000;
    this.pollMs = Math.max(500, Math.min(30000, _fPoll));
    this.state  = state;
    this.topN   = topN || 15;

    this._filter = [];
    this._nat    = [];
    this._mangle = [];
    this._raw    = [];

    this.prevCounts  = new Map();
    this._lastFp     = '';

    this._streams    = { filter: null, nat: null, mangle: null, raw: null };
    this._restarting = { filter: false, nat: false, mangle: false, raw: false };
    this._restartTimers = { filter: null, nat: null, mangle: null, raw: null };
    this._heartbeat  = null;
    this._resuming   = false;

    this._counterStreams       = { filter: null, nat: null, mangle: null, raw: null };
    this._counterRestarting   = { filter: false, nat: false, mangle: false, raw: false };
    this._counterRestartTimers = { filter: null, nat: null, mangle: null, raw: null };
    this._emitDebounce        = null;

    ros.on('close', () => {
      this._stopAllStreams();
      this._stopHeartbeat();
      this._stopCounterStreams();
    });
    // On reconnect, clear state — index.js _updateFirewallStreams() calls
    // resume() if the Firewall page or dashboard card is still open.
    ros.on('connected', () => this.suspend());
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
    const all       = [...this._filter, ...this._nat, ...this._mangle, ...this._raw];
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
      raw:      this._raw.map(r    => ({ id: r.id, packets: r.packets, bytes: r.bytes })),
    });

    const payload = {
      ts: Date.now(),
      filter:   this._filter,
      nat:      this._nat,
      mangle:   this._mangle,
      raw:      this._raw,
      topByHits,
      pollMs:   this.pollMs,
    };
    this.lastPayload = payload;
    this.state.lastFirewallTs  = Date.now();
    this.state.lastFirewallErr = null;

    if (fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.to('page-firewall').to('dash-card-firewall').emit('firewall:update', payload);
    }
  }

  // ── counter streams ───────────────────────────────────────────────────────
  // One =interval=N stream per table re-fetches packet/byte counts on a
  // router-managed schedule. Stopped on idle; restarted on resume.

  _scheduleEmit() {
    if (this._emitDebounce) return;
    this._emitDebounce = setTimeout(() => { // codeql[js/resource-exhaustion]
      this._emitDebounce = null;
      this._emit();
    }, 50);
  }

  _onCounterRecord(table, pkt) {
    const id      = pkt['.id'];
    const packets = parseInt(pkt.packets || '0', 10);
    const bytes   = parseInt(pkt.bytes   || '0', 10);
    if (!id) return;
    const tableKey = '_' + table;
    const idx = this[tableKey].findIndex(r => r.id === id);
    if (idx < 0) return;
    const rule = this[tableKey][idx];
    const prev = this.prevCounts.get(id);
    if (prev && (packets !== rule.packets || bytes !== rule.bytes)) {
      const deltaPackets = Math.max(0, packets - (prev.packets || 0));
      this[tableKey][idx] = { ...rule, packets, bytes, deltaPackets };
      this.prevCounts.set(id, { packets, bytes });
      this._scheduleEmit();
    } else if (!prev) {
      this.prevCounts.set(id, { packets, bytes });
    }
  }

  _startCounterStream(table) {
    if (this._counterStreams[table] || this._counterRestarting[table]) return;
    if (!this.ros.connected) return;
    const intervalSec = Math.max(1, Math.round(this.pollMs / 1000));
    const stream = this.ros.stream(
      [`/ip/firewall/${table}/print`, '=.proplist=.id,packets,bytes', `=interval=${intervalSec}`],
      null
    );
    this._counterStreams[table] = stream;
    stream.on('data', (pkt) => {
      if (!pkt || typeof pkt !== 'object' || Array.isArray(pkt)) return;
      this._onCounterRecord(table, pkt);
    });
    stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      console.error(this._lbl, `${table} counter stream error:`, msg);
      this._stopCounterStream(table);
      if (this.ros.connected && !this._counterRestarting[table]) {
        this._counterRestarting[table] = true;
        this._counterRestartTimers[table] = setTimeout(() => { // codeql[js/resource-exhaustion]
          this._counterRestarting[table] = false;
          this._counterRestartTimers[table] = null;
          this._startCounterStream(table);
        }, 3000);
      }
    });
    console.log(this._lbl, `streaming /ip/firewall/${table}/print interval=${intervalSec}s`);
  }

  _stopCounterStream(table) {
    if (this._counterRestartTimers[table]) { clearTimeout(this._counterRestartTimers[table]); this._counterRestartTimers[table] = null; }
    this._counterRestarting[table] = false;
    if (this._counterStreams[table]) {
      try { this._counterStreams[table].stop(); } catch (_) {}
      this._counterStreams[table] = null;
    }
  }

  _startCounterStreams() {
    for (const t of ['filter', 'nat', 'mangle', 'raw']) this._startCounterStream(t);
  }

  _stopCounterStreams() {
    if (this._emitDebounce) { clearTimeout(this._emitDebounce); this._emitDebounce = null; }
    for (const t of ['filter', 'nat', 'mangle', 'raw']) this._stopCounterStream(t);
  }

  // ── initial load ─────────────────────────────────────────────────────────

  async _loadInitial() {
    const safeGet = async (cmd, params) => {
      try { const r = await this.ros.write(cmd, params || []); return Array.isArray(r) ? r : []; }
      catch { return []; }
    };
    const pl = ['=.proplist=.id,disabled,chain,action,comment,src-address,dst-address,protocol,dst-port,in-interface,packets,bytes'];
    const [filter, nat, mangle, raw] = await Promise.all([
      safeGet('/ip/firewall/filter/print', pl),
      safeGet('/ip/firewall/nat/print',    pl),
      safeGet('/ip/firewall/mangle/print', pl),
      safeGet('/ip/firewall/raw/print',    pl),
    ]);
    this._filter = filter.map(r => this._processRule(r)).filter(Boolean);
    this._nat    = nat.map(r    => this._processRule(r)).filter(Boolean);
    this._mangle = mangle.map(r => this._processRule(r)).filter(Boolean);
    this._raw    = raw.map(r    => this._processRule(r)).filter(Boolean);
    this._emit();
  }

  // ── structural stream management ──────────────────────────────────────────

  _startStream(table, cmd) {
    if (this._streams[table]) return;
    if (!this.ros.connected) return;
    try {
      this._streams[table] = this.ros.stream([cmd], (err, data) => {
        if (err) {
          console.error(this._lbl, `${table} stream error:`, err && err.message ? err.message : err);
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
      console.log(this._lbl, `streaming /ip/firewall/${table}/listen`);
    } catch (e) {
      console.error(this._lbl, `${table} stream start failed:`, e && e.message ? e.message : e);
    }
  }

  _stopStream(table) {
    if (this._restartTimers[table]) { clearTimeout(this._restartTimers[table]); this._restartTimers[table] = null; }
    this._restarting[table] = false;
    if (this._streams[table]) { try { this._streams[table].stop(); } catch (_) {} this._streams[table] = null; }
  }

  _stopAllStreams() {
    for (const t of ['filter', 'nat', 'mangle', 'raw']) this._stopStream(t);
  }

  // ── heartbeat ────────────────────────────────────────────────────────────

  _startHeartbeat() {
    if (this._heartbeat) return;
    this._heartbeat = setInterval(() => {
      if (this.lastPayload) this.io.to('page-firewall').emit('firewall:update', { ...this.lastPayload, ts: Date.now() });
    }, 60000);
  }

  _stopHeartbeat() {
    if (this._heartbeat) { clearInterval(this._heartbeat); this._heartbeat = null; }
  }

  // ── lifecycle ─────────────────────────────────────────────────────────────

  async start() {
    // Poll once at startup to populate lastPayload — structural streams and
    // counter streams open only when the Firewall page becomes visible
    // (resume() is called by _updateFirewallStreams() in index.js).
    if (!this.ros.connected) return;
    try {
      await this._loadInitial();
    } catch (e) {
      // Non-fatal — lastPayload stays null; resume() retries when page opens.
    }
  }

  suspend() {
    this._resuming = false;
    this._stopAllStreams();
    this._stopHeartbeat();
    this._stopCounterStreams();
    this._filter = [];
    this._nat    = [];
    this._mangle = [];
    this._raw    = [];
    this.prevCounts.clear();
    this._lastFp = '';
  }

  async resume() {
    if (this._resuming) return;
    if (Object.values(this._streams).some(s => s !== null)) return;
    if (!this.ros.connected) return;
    this._resuming = true;
    try {
      await this._loadInitial();
      if (!this._resuming) return; // suspend() was called during the load
      this._startStream('filter', '/ip/firewall/filter/listen');
      this._startStream('nat',    '/ip/firewall/nat/listen');
      this._startStream('mangle', '/ip/firewall/mangle/listen');
      this._startStream('raw',    '/ip/firewall/raw/listen');
      this._startHeartbeat();
      this._startCounterStreams();
    } finally {
      this._resuming = false;
    }
  }

  stop() {
    this._stopAllStreams();
    this._stopHeartbeat();
    this._stopCounterStreams();
  }
}

module.exports = FirewallCollector;
