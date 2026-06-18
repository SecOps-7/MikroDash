/**
 * Firewall collector — single interval-based stream for the active tab.
 *
 * One /ip/firewall/<table>/print =interval=N stream runs at a time, covering
 * the tab currently open in the browser. RouterOS delivers a full snapshot of
 * that table's rules on each tick. A 150 ms snapshot-flush debounce waits for
 * all records in the batch, then swaps staging → live array and emits.
 *
 * All four tables are loaded once at startup / resume so the Chain Count card
 * has fresh counts for every table even when only one is being streamed.
 * The active-table stream is stopped on suspend and restarted on resume.
 */
class FirewallCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros    = ros;
    this.io     = io;
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][firewall]` : '[firewall]';
    const _fPoll = Number.isFinite(Number(pollMs)) ? Math.trunc(Number(pollMs)) : 10000;
    this.pollMs = Math.max(500, Math.min(30000, _fPoll));
    this.state  = state;

    this._filter = [];
    this._nat    = [];
    this._mangle = [];
    this._raw    = [];

    this.prevCounts       = new Map();
    this._lastFp          = '';
    this._activeTable     = 'filter';

    this._tableStream      = null;
    this._tableRestarting  = false;
    this._tableRestartTimer = null;
    this._staging          = [];
    this._snapshotDebounce = null;
    this._heartbeat        = null;
    this._resuming         = false;

    ros.on('close', () => {
      this._stopTableStream();
      this._stopHeartbeat();
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

  _emit() {
    const all = [...this._filter, ...this._nat, ...this._mangle, ...this._raw];
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
      ts:          Date.now(),
      filter:      this._filter,
      nat:         this._nat,
      mangle:      this._mangle,
      raw:         this._raw,
      activeTable: this._activeTable,
      pollMs:      this.pollMs,
    };
    this.lastPayload = payload;
    this.state.lastFirewallTs  = Date.now();
    this.state.lastFirewallErr = null;

    if (fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.to('page-firewall').to('dash-card-firewall').emit('firewall:update', payload);
    }
  }

  // ── snapshot flush ────────────────────────────────────────────────────────
  // RouterOS delivers counter records one-by-one (.id, packets, bytes only).
  // A 150 ms debounce fires after the last record arrives; we then merge the
  // updated counters into the existing rule objects loaded by _loadInitial().

  _scheduleSnapshotFlush() {
    if (this._snapshotDebounce) clearTimeout(this._snapshotDebounce);
    this._snapshotDebounce = setTimeout(() => { // codeql[js/resource-exhaustion]
      this._snapshotDebounce = null;
      const tableKey = '_' + this._activeTable;
      const updates = new Map();
      for (const r of this._staging) { if (r['.id']) updates.set(r['.id'], r); }
      this._staging = [];
      this[tableKey] = this[tableKey].map(rule => {
        const u = updates.get(rule.id);
        if (!u) return rule;
        const packets = parseInt(u.packets || '0', 10);
        const bytes   = parseInt(u.bytes   || '0', 10);
        const prev    = this.prevCounts.get(rule.id);
        const deltaPackets = prev ? Math.max(0, packets - prev.packets) : 0;
        this.prevCounts.set(rule.id, { packets, bytes });
        return { ...rule, packets, bytes, deltaPackets };
      });
      this._emit();
    }, 150);
  }

  // ── table stream ──────────────────────────────────────────────────────────

  _startTableStream(table) {
    if (this._tableStream || this._tableRestarting) return;
    if (!this.ros.connected) return;
    const intervalSec = Math.max(1, Math.round(this.pollMs / 1000));
    const pl = '=.proplist=.id,packets,bytes';
    const stream = this.ros.stream(
      [`/ip/firewall/${table}/print`, `=interval=${intervalSec}`, pl],
      null
    );
    this._tableStream = stream;
    stream.on('data', (pkt) => {
      if (!pkt || typeof pkt !== 'object' || Array.isArray(pkt)) return;
      this._staging.push(pkt);
      this._scheduleSnapshotFlush();
    });
    stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      console.error(this._lbl + ` ${table} stream error:`, msg); // codeql[js/tainted-format-string]
      this.state.lastFirewallErr = msg;
      this._stopTableStream();
      if (this.ros.connected && !this._tableRestarting) {
        this._tableRestarting = true;
        this._tableRestartTimer = setTimeout(() => { // codeql[js/resource-exhaustion]
          this._tableRestarting = false;
          this._tableRestartTimer = null;
          this._startTableStream(this._activeTable);
        }, 3000);
      }
    });
    console.log(this._lbl + ` streaming /ip/firewall/${table}/print interval=${intervalSec}s`); // codeql[js/tainted-format-string]
  }

  _stopTableStream() {
    if (this._snapshotDebounce) { clearTimeout(this._snapshotDebounce); this._snapshotDebounce = null; }
    if (this._tableRestartTimer) { clearTimeout(this._tableRestartTimer); this._tableRestartTimer = null; }
    this._tableRestarting = false;
    this._staging = [];
    if (this._tableStream) {
      try { this._tableStream.stop(); } catch (_) {}
      this._tableStream = null;
    }
  }

  setActiveTable(table) {
    if (table === this._activeTable && this._tableStream) return;
    this._activeTable = table;
    this._stopTableStream();
    // If resume() is in progress let it start the stream with the updated _activeTable.
    if (this.ros.connected && !this._resuming) this._startTableStream(table);
  }

  // ── heartbeat ────────────────────────────────────────────────────────────

  _startHeartbeat() {
    if (this._heartbeat) return;
    this._heartbeat = setInterval(() => { // codeql[js/resource-exhaustion]
      if (this.lastPayload) this.io.to('page-firewall').emit('firewall:update', { ...this.lastPayload, ts: Date.now() });
    }, 60000);
  }

  _stopHeartbeat() {
    if (this._heartbeat) { clearInterval(this._heartbeat); this._heartbeat = null; }
  }

  // ── initial load ─────────────────────────────────────────────────────────
  // Fetches all four tables once so the Chain Count card has fresh counts
  // for every chain even while only the active table is being streamed.

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

  // ── lifecycle ─────────────────────────────────────────────────────────────

  async start() {
    // One-shot load at startup to populate lastPayload. The table stream opens
    // only when the Firewall page becomes visible (resume() via _updateFirewallStreams).
    if (!this.ros.connected) return;
    try {
      await this._loadInitial();
    } catch (e) {
      // Non-fatal — lastPayload stays null; resume() retries when page opens.
    }
  }

  suspend() {
    this._resuming = false;
    this._stopTableStream();
    this._stopHeartbeat();
    this._filter = [];
    this._nat    = [];
    this._mangle = [];
    this._raw    = [];
    this.prevCounts.clear();
    this._lastFp = '';
  }

  async resume(activeTable) {
    if (activeTable) this._activeTable = activeTable;
    if (this._resuming) return;
    if (this._tableStream) return;
    if (!this.ros.connected) return;
    this._resuming = true;
    try {
      await this._loadInitial();
      if (!this._resuming) return; // suspend() was called during the load
      this._startTableStream(this._activeTable);
      this._startHeartbeat();
    } finally {
      this._resuming = false;
    }
  }

  stop() {
    this._stopTableStream();
    this._stopHeartbeat();
  }
}

module.exports = FirewallCollector;
