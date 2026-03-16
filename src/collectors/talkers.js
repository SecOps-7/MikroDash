/**
 * Top Talkers (Kid Control) — polls /ip/kid-control/device/print.
 * Runs concurrently with all streams via node-routeros tagged multiplexing.
 */
const mbps = (d, dtMs) => dtMs <= 0 ? 0 : ((d * 8) / (dtMs / 1000)) / 1_000_000;

class TopTalkersCollector {
  constructor({ ros, io, pollMs, state, topN }) {
    this.ros = ros;
    this.io = io;
    this.pollMs = pollMs;
    this.state = state;
    this.topN = topN || 5;
    this.prev = new Map();
    this.timer = null;
    this._inflight = false;
    this._unavailable  = false; // set true when Kid Control is not licensed/configured
    this._backoffUntil = 0;     // epoch ms — don't poll before this time
    this._backoffMs    = 60000; // start at 1 min, doubles each miss up to 10 min
  }

  async tick() {
    if (!this.ros.connected) return;
    const now = Date.now();

    // Use a short per-command timeout — Kid Control may not be configured on
    // all RouterOS builds. A 30s hang would trip the global write timeout and
    // force an unnecessary reconnect. If unavailable, emit an empty list quietly.
    // Skip poll during backoff window (Kid Control unavailable/unlicensed)
    if (now < this._backoffUntil) {
      this.lastPayload = { ts: now, devices: [], pollMs: this.pollMs };
      this.io.emit('talkers:update', this.lastPayload);
      this.state.lastTalkersTs = now;
      return;
    }

    let items;
    try {
      items = await this.ros.write('/ip/kid-control/device/print',
        ['=.proplist=name,mac-address,bytes-up,bytes-down'],
        5000); // 5s timeout — fail fast if Kid Control is not available
      // Successful response — reset backoff
      this._backoffMs    = 60000;
      this._unavailable  = false;
    } catch (e) {
      const msg = String(e && e.message ? e.message : e);
      // Suppress timeout/unavailable errors — back off to avoid hammering the router
      if (msg.includes('timeout') || msg.includes('unknown command') || msg.includes('no such')) {
        this._unavailable  = true;
        this._backoffUntil = now + this._backoffMs;
        this._backoffMs    = Math.min(this._backoffMs * 2, 600000); // cap at 10 min
        console.warn(`[talkers] Kid Control unavailable — backing off ${Math.round(this._backoffMs/1000)}s`);
        this.lastPayload = { ts: now, devices: [], pollMs: this.pollMs };
        this.io.emit('talkers:update', this.lastPayload);
        this.state.lastTalkersTs  = now;
        this.state.lastTalkersErr = null;
        return;
      }
      throw e;
    }

    const seenMACs = new Set();
    let devices = (items || []).map(d => {
      const mac  = d['mac-address'] || '';
      const up   = parseInt(d['bytes-up']   || '0', 10);
      const down = parseInt(d['bytes-down'] || '0', 10);
      const prev = this.prev.get(mac);
      let rx = 0, tx = 0;
      if (prev && up >= prev.up && down >= prev.down) {
        const dt = now - prev.ts;
        tx = mbps(up - prev.up, dt);
        rx = mbps(down - prev.down, dt);
      }
      if (mac) { this.prev.set(mac, { up, down, ts: now }); seenMACs.add(mac); }
      return { name: d.name || '', mac, tx_mbps: +tx.toFixed(3), rx_mbps: +rx.toFixed(3) };
    });

    // Prune stale entries for devices no longer reported
    for (const k of this.prev.keys()) {
      if (!seenMACs.has(k)) this.prev.delete(k);
    }

    devices.sort((a, b) => (b.rx_mbps + b.tx_mbps) - (a.rx_mbps + a.tx_mbps));
    devices = devices.slice(0, this.topN);

    this.lastPayload = { ts: now, devices, pollMs: this.pollMs };
    this.io.emit('talkers:update', this.lastPayload);
    this.state.lastTalkersTs = now;
    this.state.lastTalkersErr = null;
  }

  start() {
    const run = async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(); } catch (e) {
        this.state.lastTalkersErr = String(e && e.message ? e.message : e);
        console.error('[talkers]', this.state.lastTalkersErr);
      } finally { this._inflight = false; }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close', () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => { this._backoffUntil = 0; this._backoffMs = 60000; this.timer = this.timer || setInterval(run, this.pollMs); run(); });
  }
}

module.exports = TopTalkersCollector;
