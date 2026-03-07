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
  }

  async tick() {
    if (!this.ros.connected) return;
    const now = Date.now();
    const items = await this.ros.write('/ip/kid-control/device/print');

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

    this.io.emit('talkers:update', { ts: now, devices });
    this.state.lastTalkersTs = now;
    delete this.state.lastTalkersErr;
  }

  start() {
    const run = async () => {
      try { await this.tick(); } catch (e) {
        this.state.lastTalkersErr = String(e && e.message ? e.message : e);
        console.error('[talkers]', this.state.lastTalkersErr);
      }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close', () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => { this.timer = this.timer || setInterval(run, this.pollMs); run(); });
  }
}

module.exports = TopTalkersCollector;
