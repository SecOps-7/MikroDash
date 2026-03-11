function parseCounter(val) {
  const parsed = parseInt(val || '0', 10);
  return Number.isFinite(parsed) ? parsed : 0;
}

function parseBps(val) {
  if (!val || val === '0') return 0;
  const parsed = parseInt(String(val), 10);
  return Number.isFinite(parsed) ? parsed : 0;
}

function bpsToMbps(bps) {
  return +((bps || 0) / 1_000_000).toFixed(4);
}

class InterfaceStatusCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros = ros; this.io = io; this.pollMs = pollMs || 5000;
    this.state = state; this.timer = null; this._inflight = false;
    // Previous byte counters for rate calculation: name -> { rxBytes, txBytes, ts }
    this._prev = new Map();
  }

  async tick() {
    if (!this.ros.connected) return;
    const [ifRes, addrRes] = await Promise.allSettled([
      this.ros.write("/interface/print", ["=stats="]),
      this.ros.write("/ip/address/print"),
    ]);
    const ifaces = ifRes.status === "fulfilled" ? (ifRes.value || []) : [];
    const addrs  = addrRes.status === "fulfilled" ? (addrRes.value || []) : [];

    const now = Date.now();

    const ipByIface = {};
    for (const a of addrs) {
      const n = a.interface || "";
      if (!ipByIface[n]) ipByIface[n] = [];
      ipByIface[n].push(a.address || "");
    }

    const interfaces = ifaces.map(i => {
      const rxBytes = parseCounter(i['rx-byte']);
      const txBytes = parseCounter(i['tx-byte']);
      const rxBps = parseBps(i['rx-bits-per-second']);
      const txBps = parseBps(i['tx-bits-per-second']);

      // Prefer RouterOS live throughput fields when present; fall back to
      // byte-counter deltas when those fields are unavailable.
      let rxMbps = bpsToMbps(rxBps);
      let txMbps = bpsToMbps(txBps);
      const prev = this._prev.get(i.name);
      if (rxMbps === 0 && txMbps === 0 && prev && now > prev.ts) {
        const elapsedSec = (now - prev.ts) / 1000;
        // Guard against counter resets (reboot) — if delta is negative, skip
        const rxDelta = rxBytes - prev.rxBytes;
        const txDelta = txBytes - prev.txBytes;
        if (rxDelta >= 0 && txDelta >= 0) {
          rxMbps = bpsToMbps((rxDelta * 8) / elapsedSec);
          txMbps = bpsToMbps((txDelta * 8) / elapsedSec);
        }
      }
      this._prev.set(i.name, { rxBytes, txBytes, ts: now });

      return {
        name:     i.name || "",
        type:     i.type || "ether",
        running:  i.running === "true" || i.running === true,
        disabled: i.disabled === "true" || i.disabled === true,
        comment:  i.comment || "",
        macAddr:  i["mac-address"] || "",
        rxBytes,
        txBytes,
        rxMbps,
        txMbps,
        ips:      ipByIface[i.name] || [],
      };
    });

    this.io.emit("ifstatus:update", { ts: now, interfaces });
    this.state.lastIfStatusTs = now;
  }

  start() {
    const run = async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(); } catch(e) { console.error("[ifstatus]", e && e.message ? e.message : e); }
      finally { this._inflight = false; }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on("close",     () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on("connected", () => {
      // Clear prev counters on reconnect — first tick after reconnect will
      // have no baseline to diff against, so rates show 0 for one cycle.
      this._prev.clear();
      this.timer = this.timer || setInterval(run, this.pollMs);
      run();
    });
  }
}
module.exports = InterfaceStatusCollector;
