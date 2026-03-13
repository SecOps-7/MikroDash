class FirewallCollector {
  constructor({ ros, io, pollMs, state, topN }) {
    this.ros = ros;
    this.io = io;
    this.pollMs = pollMs || 10000;
    this.state = state;
    this.topN = topN || 15;
    this.prevCounts = new Map();
    this._lastFp = '';
    this.timer = null;
    this._inflight = false;
  }

  async safeGet(cmd) {
    try { const r = await this.ros.write(cmd); return Array.isArray(r) ? r : []; } catch { return []; }
  }

  processChain(rules) {
    return rules.filter(r => r.disabled !== 'true' && r.disabled !== true).map(r => {
      const id = r['.id'] || '';
      const packets = parseInt(r.packets || '0', 10);
      const bytes   = parseInt(r.bytes   || '0', 10);
      const prev = this.prevCounts.get(id);
      const deltaPackets = prev ? Math.max(0, packets - prev.packets) : 0;
      if (id) this.prevCounts.set(id, { packets, bytes });
      return { id, chain:r.chain||'', action:r.action||'?', comment:r.comment||'', srcAddress:r['src-address']||'', dstAddress:r['dst-address']||'', protocol:r.protocol||'', dstPort:r['dst-port']||'', inInterface:r['in-interface']||'', packets, bytes, deltaPackets, disabled:false };
    });
  }

  async tick() {
    if (!this.ros.connected) return;
    // All three fire concurrently
    const [filter, nat, mangle] = await Promise.all([
      this.safeGet('/ip/firewall/filter/print'),
      this.safeGet('/ip/firewall/nat/print'),
      this.safeGet('/ip/firewall/mangle/print'),
    ]);
    const filterRules = this.processChain(filter);
    const natRules    = this.processChain(nat);
    const mangleRules = this.processChain(mangle);
    const topByHits   = [...filterRules,...natRules,...mangleRules].filter(r=>r.packets>0).sort((a,b)=>b.packets-a.packets).slice(0,this.topN);

    // Prune stale entries from prevCounts for rules no longer present
    const seenIds = new Set([...filterRules,...natRules,...mangleRules].map(r => r.id).filter(Boolean));
    for (const id of this.prevCounts.keys()) {
      if (!seenIds.has(id)) this.prevCounts.delete(id);
    }

    // Fingerprint excludes ts so identical rule sets suppress the emit
    const fp = JSON.stringify({ filter: filterRules.map(r=>({id:r.id,packets:r.packets,bytes:r.bytes,deltaPackets:r.deltaPackets})),
      nat: natRules.map(r=>({id:r.id,packets:r.packets})), topByHits: topByHits.map(r=>r.id) });
    this.lastPayload = { ts:Date.now(), filter:filterRules, nat:natRules, mangle:mangleRules, topByHits, pollMs: this.pollMs };
    if (fp !== this._lastFp) { this._lastFp = fp; this.io.emit('firewall:update', this.lastPayload); }
    this.state.lastFirewallTs = Date.now();
    this.state.lastFirewallErr = null;
  }

  start() {
    const run = async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(); } catch (e) {
        this.state.lastFirewallErr = String(e && e.message ? e.message : e);
        console.error('[firewall]', this.state.lastFirewallErr);
      } finally { this._inflight = false; }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close', () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => { this.timer = this.timer || setInterval(run, this.pollMs); run(); });
  }
}

module.exports = FirewallCollector;
