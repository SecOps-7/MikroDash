/**
 * DHCP Leases — streams /ip/dhcp-server/lease/listen for instant updates,
 * with a one-shot /print on startup to populate the initial state.
 */
const { stopStreamSafe, createPollLoop } = require('./util');

class DhcpLeasesCollector {
  constructor({ ros, io, state, _restartDelayMs, pollMs, streamMode }) {
    this.streamMode = streamMode !== false;   // default stream
    this.pollMs     = Math.max(500, Math.min(600000, Number(pollMs) || 600000));
    this._poll      = createPollLoop(() => this._loadInitial(), () => this.pollMs);
    this.ros = ros;
    this.io = io;
    this._lbl = ros.routerLabel ? `[${ros.routerLabel}][leases]` : '[leases]';
    this.state = state;
    this._restartDelayMs = _restartDelayMs || 2000;
    this.byIP  = new Map();
    this.byMAC = new Map();
    this.seenMACs = new Set();
    this.stream = null;
    this._restarting = false;
    this._restartTimer = null;
    this.lastPayload = null;
    // DHCP server name → { iface, vlanId }. A lease only carries its server
    // name, so the interface and VLAN behind it have to be joined from the
    // server config. Both tables change rarely, so this is refreshed on
    // connect rather than polled.
    this.serverMeta = new Map();
  }

  getNameByIP(ip)  { return this.byIP.get(ip);  }
  getNameByMAC(mac){ return this.byMAC.get(mac); }

  // Returns IPs of all known leases regardless of status (bound, waiting, expired, etc.)
  // Used by DhcpNetworksCollector to count total leases per subnet.
  getAllLeaseIPs() {
    return Array.from(this.byIP.keys());
  }

  getActiveLeaseIPs() {
    const out = [];
    for (const [ip, v] of this.byIP.entries()) {
      const st = String(v.status || '').toLowerCase();
      if (!st || st === 'bound' || st === 'offered') out.push(ip);
    }
    return out;
  }

  // Server list for the leases filter. Built from the leases actually present so
  // a server whose config we could not read (added since the last connect) still
  // appears, just without its interface and VLAN.
  _serverSummary(leases) {
    const counts = new Map();
    for (const l of leases) {
      if (!l.server) continue;
      counts.set(l.server, (counts.get(l.server) || 0) + 1);
    }
    return Array.from(counts.entries()).map(([name, count]) => {
      const meta = this.serverMeta.get(name) || {};
      return { name, iface: meta.iface || '', vlanId: meta.vlanId || '', count };
    }).sort((a, b) => b.count - a.count);
  }

  _emitLeases() {
    const leases = [];
    for (const [ip, v] of this.byIP.entries()) leases.push({ ip, ...v });
    const payload = { ts: Date.now(), leases, servers: this._serverSummary(leases) };
    this.lastPayload = payload;
    this.io.emit('leases:list', payload);
  }

  _applyLease(l, emit = false) {
    const ip     = l.address || l['active-address'];
    const mac    = l['mac-address'] || l['active-mac-address'] || l.mac;
    const status = l.status || '';

    // Prune expired/removed leases so maps don't grow unboundedly on
    // long-running instances. The stream sends these with status='expired'
    // or '.dead=true' — handle both.
    const dead = l['.dead'] === 'true' || l['.dead'] === true;
    if (dead || status === 'expired' || status === 'removed') {
      if (ip)  this.byIP.delete(ip);
      if (mac) this.byMAC.delete(mac);
      if (emit) this._emitLeases();
      return;
    }

    const name = (l.comment && l.comment.trim()) ? l.comment.trim()
               : (l['host-name'] && l['host-name'].trim()) ? l['host-name'].trim() : '';

    const server = l.server || '';
    const meta   = this.serverMeta.get(server) || {};

    // `id` lets the page open a lease in the edit form; `dynamic` is what tells
    // a reservation from a lease the server handed out, which is the difference
    // between an editable row and one that only offers "make static".
    if (ip)  this.byIP.set(ip,   { name, mac, hostName: l['host-name'] || '', comment: l.comment || '', status,
                                   server, iface: meta.iface || '', vlanId: meta.vlanId || '',
                                   id: l['.id'] || '', dynamic: l.dynamic === 'true' || l.dynamic === true });
    if (mac) this.byMAC.set(mac, { name, ip });

    if (mac && ip && !this.seenMACs.has(mac)) {
      this.seenMACs.add(mac);
      this.io.emit('device:new', { ts: Date.now(), ip, mac, name: name || ('Unknown (' + mac + ')'), source: 'dhcp-lease' });
    }

    // Emit updated lease table to all clients when called from the live stream
    if (emit) this._emitLeases();
  }

  // Resolve each DHCP server to the interface it serves, and that interface to a
  // VLAN id when it is a VLAN. A server on a plain ether interface simply has no
  // vlanId. Failure here is not fatal: leases still carry their server name, so
  // the filter degrades to server-only rather than disappearing.
  async _loadServerMap() {
    try {
      const [servers, vlans] = await Promise.all([
        this.ros.write('/ip/dhcp-server/print', ['=.proplist=name,interface']),
        this.ros.write('/interface/vlan/print', ['=.proplist=name,vlan-id']),
      ]);
      const vlanById = new Map();
      for (const v of (vlans || [])) {
        if (v.name) vlanById.set(v.name, v['vlan-id'] || '');
      }
      this.serverMeta.clear();
      for (const s of (servers || [])) {
        if (!s.name) continue;
        const iface = s.interface || '';
        this.serverMeta.set(s.name, { iface, vlanId: vlanById.get(iface) || '' });
      }
    } catch (e) {
      // Constant format string, with the router label and error passed as
      // arguments. Concatenating them into the first argument put attacker
      // influenced text in the format-string position (CodeQL
      // js/tainted-format-string), so a '%' in a router label could consume a
      // later argument. The label is already stripped of '%' at its source in
      // routeros/client.js; not depending on that is cheaper than depending on it.
      console.warn('%s server/VLAN map unavailable: %s', this._lbl, e && e.message ? e.message : e);
    }
  }

  /**
   * Re-read now, after a write, so the page shows what the router did.
   *
   * _loadInitial() is the whole read and already ends in _emitLeases(), so
   * there is nothing to add — but it also rebuilds the server map, which is
   * what a newly created reservation on a server we had not seen needs.
   */
  async refreshNow() {
    if (!this.ros.connected) return;
    await this._loadInitial();
  }

  async _loadInitial() {
    try {
      // Must precede the lease load so the first _applyLease can already resolve
      // each lease's interface and VLAN.
      await this._loadServerMap();
      const leases = await this.ros.write('/ip/dhcp-server/lease/print',
        ['=.proplist=.id,.dead,address,active-address,mac-address,active-mac-address,status,comment,host-name,server,dynamic']);
      for (const l of (leases || [])) this._applyLease(l);
      this.state.lastLeasesTs = Date.now();
      this._emitLeases();
    } catch (e) {
      console.error('%s', this._lbl + ' initial load failed:', e && e.message ? e.message : e);
    }
  }

  _startStream() {
    if (this.stream) return;
    if (!this.ros.connected) return;
    try {
      this.stream = this.ros.stream(
        ['/ip/dhcp-server/lease/listen', '=.proplist=.id,.dead,address,active-address,mac-address,active-mac-address,status,comment,host-name,server,dynamic'],
        (err, data) => {
        if (err) {
          console.error('%s', this._lbl + ' stream error:', err && err.message ? err.message : err);
          this._stopStream();
          if (this.ros.connected && !this._restarting) {
            this._restarting = true;
            this._restartTimer = setTimeout(() => {
              this._restarting = false;
              this._restartTimer = null;
              if (this.ros.connected) this._startStream();
            }, this._restartDelayMs);
          }
          return;
        }
        if (data) { this._applyLease(data, true); this.state.lastLeasesTs = Date.now(); }
      });
      console.log('%s', this._lbl + ' streaming /ip/dhcp-server/lease/listen');
    } catch (e) {
      console.error('%s', this._lbl + ' stream start failed:', e && e.message ? e.message : e);
    }
  }

  _stopStream() {
    if (this._restartTimer) { clearTimeout(this._restartTimer); this._restartTimer = null; }
    this._restarting = false;
    if (this.stream) { stopStreamSafe(this.stream); this.stream = null; }
  }

  // Delivery switch (#105). /listen is event-driven and cheap when idle, but it
  // still holds an open channel, and concurrent channels are what strain a small
  // router. Poll mode swaps the listen stream for a periodic re-read using the
  // same _loadInitial() the stream path already runs on connect, so there is no
  // second parsing path to keep in step.
  _deliver() {
    if (this.streamMode) this._startStream();
    else this._poll.start();
  }

  _stopDelivery() {
    this._stopStream();
    this._poll.stop();
  }

  async start() {
    await this._loadInitial();
    this._deliver();
    this.ros.on('connected', async () => {
      this._stopDelivery();
      await this._loadInitial();
      this._deliver();
    });
    this.ros.on('close', () => this._stopDelivery());
  }

  stop() { this._stopDelivery(); }
}

module.exports = DhcpLeasesCollector;
