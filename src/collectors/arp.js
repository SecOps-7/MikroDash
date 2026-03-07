class ArpCollector {
  constructor({ ros, pollMs, state }) {
    this.ros = ros;
    this.pollMs = pollMs;
    this.state = state;
    this.byIP = new Map();
    this.byMAC = new Map();
    this.timer = null;
  }

  getByIP(ip)   { return this.byIP.get(ip); }
  getByMAC(mac) { return this.byMAC.get(mac) || null; }

  async tick() {
    if (!this.ros.connected) return;
    const items = await this.ros.write('/ip/arp/print');
    const ipMap = new Map();
    const macMap = new Map();
    for (const a of (items || [])) {
      if (a.address && a['mac-address']) {
        const entry = { mac: a['mac-address'], iface: a.interface || '' };
        ipMap.set(a.address, entry);
        macMap.set(a['mac-address'], { ip: a.address, ...entry });
      }
    }
    this.byIP = ipMap;
    this.byMAC = macMap;
    this.state.lastArpTs = Date.now();
  }

  start() {
    const run = async () => { try { await this.tick(); } catch (e) { console.error('[arp]', e && e.message ? e.message : e); } };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close', () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => { this.timer = this.timer || setInterval(run, this.pollMs); run(); });
  }
}

module.exports = ArpCollector;
