class InterfaceStatusCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros = ros; this.io = io; this.pollMs = pollMs || 5000;
    this.state = state; this.timer = null; this._inflight = false;
  }
  async tick() {
    if (!this.ros.connected) return;
    const [ifRes, addrRes] = await Promise.allSettled([
      this.ros.write("/interface/print", ["=stats="]),
      this.ros.write("/ip/address/print"),
    ]);
    const ifaces = ifRes.status === "fulfilled" ? (ifRes.value || []) : [];
    const addrs  = addrRes.status === "fulfilled" ? (addrRes.value || []) : [];
    const ipByIface = {};
    for (const a of addrs) {
      const n = a.interface || "";
      if (!ipByIface[n]) ipByIface[n] = [];
      ipByIface[n].push(a.address || "");
    }
    const intOrZero = (value) => {
      const parsed = parseInt(value || "0", 10);
      return Number.isFinite(parsed) ? parsed : 0;
    };
    const mbpsOrZero = (value) => {
      const parsed = parseFloat(value || "0");
      return Number.isFinite(parsed) ? Math.round((parsed / 1e6) * 10) / 10 : 0;
    };
    const interfaces = ifaces.map(i => ({
      name:     i.name || "",
      type:     i.type || "ether",
      running:  i.running === "true" || i.running === true,
      disabled: i.disabled === "true" || i.disabled === true,
      comment:  i.comment || "",
      macAddr:  i["mac-address"] || "",
      rxBytes:  intOrZero(i["rx-byte"]),
      txBytes:  intOrZero(i["tx-byte"]),
      rxMbps:   mbpsOrZero(i["rx-bits-per-second"]),
      txMbps:   mbpsOrZero(i["tx-bits-per-second"]),
      ips:      ipByIface[i.name] || [],
    }));
    this.io.emit("ifstatus:update", { ts: Date.now(), interfaces });
    this.state.lastIfStatusTs = Date.now();
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
    this.ros.on("connected", () => { this.timer = this.timer || setInterval(run, this.pollMs); run(); });
  }
}
module.exports = InterfaceStatusCollector;
