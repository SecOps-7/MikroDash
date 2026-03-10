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
      const rxBytes = parseInt(i["rx-byte"] || "0", 10);
      const txBytes = parseInt(i["tx-byte"] || "0", 10);

      // Derive live rate from cumulative byte counter delta
      let rxMbps = 0, txMbps = 0;
      const prev = this._prev.get(i.name);
      if (prev && now > prev.ts) {
        const elapsedSec = (now - prev.ts) / 1000;
        // Guard against counter resets (reboot) — if delta is negative, skip
        const rxDelta = rxBytes - prev.rxBytes;
        const txDelta = txBytes - prev.txBytes;
        if (rxDelta >= 0 && txDelta >= 0) {
          rxMbps = +((rxDelta * 8) / elapsedSec / 1e6).toFixed(4);
          txMbps = +((txDelta * 8) / elapsedSec / 1e6).toFixed(4);
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
