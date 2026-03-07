const PING_COUNT = 3;

class PingCollector {
  constructor({ ros, io, pollMs, state, target }) {
    this.ros    = ros;
    this.io     = io;
    this.pollMs = pollMs || 10000;
    this.state  = state;
    this.target = target || '1.1.1.1';
    this.timer  = null;
    this.history = []; // {ts, rtt, loss}
    this.MAX_HISTORY = 60;
  }

  async tick() {
    if (!this.ros.connected) return;
    let rtt = null, loss = 100;
    try {
      const results = await this.ros.write('/tool/ping', [
        '=address=' + this.target,
        '=count=' + PING_COUNT,
        '=interval=0.2',
      ]);
      const rows = Array.isArray(results) ? results : [];
      const replied = rows.filter(r => r.status === 'replied' || (r['avg-rtt'] && !r.status));
      // RouterOS returns a summary row with avg-rtt
      const summary = rows.find(r => r['avg-rtt'] || r['min-rtt']);
      if (summary && summary['avg-rtt']) {
        // avg-rtt is like "3ms" or "1.5ms"
        const m = String(summary['avg-rtt']).match(/([\d.]+)/);
        if (m) rtt = parseFloat(m[1]);
        const sent = parseInt(summary['sent'] || String(PING_COUNT), 10);
        const recv = parseInt(summary['received'] || replied.length, 10);
        loss = sent > 0 ? Math.round(((sent - recv) / sent) * 100) : 0;
      } else if (replied.length > 0) {
        // Fallback: average individual reply times
        const times = replied.map(r => {
          const m = String(r.time || r['response-time'] || '0').match(/([\d.]+)/);
          return m ? parseFloat(m[1]) : 0;
        }).filter(v => v > 0);
        if (times.length) rtt = Math.round(times.reduce((a,b)=>a+b,0) / times.length);
        loss = Math.round(((PING_COUNT - replied.length) / PING_COUNT) * 100);
      }
    } catch (e) {
      console.error('[ping]', e && e.message ? e.message : e);
    }

    const point = { ts: Date.now(), rtt, loss };
    this.history.push(point);
    if (this.history.length > this.MAX_HISTORY) this.history.shift();

    this.io.emit('ping:update', { target: this.target, rtt, loss, ts: point.ts });
    this.state.lastPingTs = Date.now();
  }

  getHistory() {
    return { target: this.target, history: this.history };
  }

  start() {
    const run = async () => {
      try { await this.tick(); } catch (e) { console.error('[ping]', e && e.message ? e.message : e); }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close',     () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => { this.timer = this.timer || setInterval(run, this.pollMs); run(); });
  }
}

module.exports = PingCollector;
