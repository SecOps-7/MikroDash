'use strict';
/**
 * Bridges collector.
 *
 *   /interface/bridge        the bridges themselves — STP mode, VLAN filtering,
 *                            IGMP snooping, priority, ageing, MAC, MTU
 *   /interface/bridge/port   each port's STP role, edge/learn/horizon and PVID
 *   /interface/bridge/host   the learned MAC table
 *
 * /interface/bridge/vlan IS NOT READ. The VLAN membership table belongs to the
 * VLANs page, which already fetches it: that table is about which VLANs exist
 * and where they are tagged, not about how a bridge is configured. Reading it
 * here as well would be a second copy of the same rows, on a second poll loop,
 * for a card that no longer exists.
 *
 * WHAT WAS ALREADY AVAILABLE, AND WHY THIS IS STILL NEW. Bridges show up in
 * interfaceStatus today as ordinary interfaces with type 'bridge' — a name, a
 * running flag and rates, and nothing else. Every field that makes a bridge a
 * bridge (protocol-mode, vlan-filtering, igmp-snooping, port roles) is
 * unreadable from there. vlans.js does read the port and vlan menus, but with a
 * proplist that omits role/edge/learn/horizon because the VLAN page has no use
 * for them.
 *
 * RATES ARE BORROWED, NOT FETCHED, the way vlans.js borrows them: ifStatus
 * already computes rxMbps/txMbps for every interface, bridges included, so a
 * bridge's throughput costs no extra router I/O. Fields are projected BY NAME
 * rather than spread, so nothing from the interface payload (IP addresses, MAC
 * lists, error counters) reaches a page whose permission is `bridges` rather
 * than `interfaces`.
 *
 * THE HOST TABLE IS CAPPED. 64 entries on the router this was written against,
 * but a switch with a few hundred clients will happily return thousands, every
 * poll, over the socket. The cap is applied here and the true total travels
 * with it so the page can say "showing 500 of 2431" rather than quietly lying.
 */

const { clampPoll, createPollLoop, createListenRefresh } = require('./util');

const BRIDGE_CMD = ['/interface/bridge/print',
                    '=.proplist=.id,name,protocol-mode,vlan-filtering,igmp-snooping,dhcp-snooping,' +
                    'fast-forward,priority,ageing-time,mac-address,actual-mtu,mtu,running,disabled,comment'];
const PORT_CMD   = ['/interface/bridge/port/print',
                    '=.proplist=.id,bridge,interface,pvid,role,edge,learn,horizon,path-cost,' +
                    'frame-types,disabled,inactive,dynamic'];
const HOST_CMD   = ['/interface/bridge/host/print',
                    '=.proplist=mac-address,on-interface,bridge,vid,dynamic,local,external,age'];
const LISTEN_CMD = '/interface/bridge/port/listen';
const HOST_CAP   = 500;
// In stream mode the bridge and port tables are re-read on a listen event; this
// is the safety net, so a missed event cannot strand the page on stale config.
const CONFIG_EVERY = 12;

const _bool = (v) => v === true || v === 'true' || v === 'yes';
const _num  = (v) => { const n = Number(v); return Number.isFinite(n) ? n : null; };

/**
 * Join the three tables into one view.
 *
 * `ifPayload` is interfaceStatus's lastPayload or null — null must yield rates
 * of null, never 0, because "the router did not report this" and "this bridge
 * is idle" have to stay tellable apart.
 */
function buildBridgeRows(bridgeRows, portRows, hostRows, ifPayload) {
  const rates = new Map();
  const ifaces = (ifPayload && Array.isArray(ifPayload.interfaces)) ? ifPayload.interfaces : null;
  if (ifaces) for (const i of ifaces) if (i && i.name) rates.set(i.name, i);

  const ports = [];
  for (const r of portRows || []) {
    if (!r || !r.interface) continue;              // also drops {undefined:''}
    ports.push({
      // The row id, so the page can open a port in the edit form.
      id:         r['.id'] || '',
      bridge:     r.bridge || '',
      interface:  String(r.interface),
      pvid:       _num(r.pvid),
      // Absent on a bridge running protocol-mode=none: there are no STP roles
      // to report, which is not the same as a port with an unknown role.
      role:       r.role || '',
      edge:       r.edge || '',
      learn:      r.learn || '',
      horizon:    r.horizon || '',
      pathCost:   _num(r['path-cost']),
      frameTypes: r['frame-types'] || '',
      disabled:   _bool(r.disabled),
      inactive:   _bool(r.inactive),
      dynamic:    _bool(r.dynamic),
    });
  }

  const portsByBridge = new Map();
  for (const p of ports) {
    if (!p.bridge) continue;
    portsByBridge.set(p.bridge, (portsByBridge.get(p.bridge) || 0) + 1);
  }

  const bridges = [];
  for (const r of bridgeRows || []) {
    if (!r || !r.name) continue;
    const live = rates.get(r.name);
    bridges.push({
      // The row id, so the page can open a bridge in the edit form.
      id:            r['.id'] || '',
      name:          String(r.name),
      protocolMode:  r['protocol-mode'] || '',
      vlanFiltering: _bool(r['vlan-filtering']),
      igmpSnooping:  _bool(r['igmp-snooping']),
      dhcpSnooping:  _bool(r['dhcp-snooping']),
      fastForward:   _bool(r['fast-forward']),
      priority:      r.priority || '',
      ageingTime:    r['ageing-time'] || '',
      macAddress:    r['mac-address'] || '',
      mtu:           _num(r['actual-mtu']) !== null ? _num(r['actual-mtu']) : _num(r.mtu),
      running:       _bool(r.running),
      disabled:      _bool(r.disabled),
      comment:       r.comment || '',
      portCount:     portsByBridge.get(String(r.name)) || 0,
      // null, never 0: "not reported" and "idle" must stay distinguishable.
      rxMbps:        live && typeof live.rxMbps === 'number' ? live.rxMbps : null,
      txMbps:        live && typeof live.txMbps === 'number' ? live.txMbps : null,
    });
  }
  bridges.sort((a, b) => a.name.localeCompare(b.name));

  const allHosts = [];
  for (const r of hostRows || []) {
    if (!r || !r['mac-address']) continue;
    allHosts.push({
      mac:         String(r['mac-address']),
      onInterface: r['on-interface'] || '',
      bridge:      r.bridge || '',
      vid:         _num(r.vid),
      dynamic:     _bool(r.dynamic),
      local:       _bool(r.local),
      external:    _bool(r.external),
      age:         r.age || '',
    });
  }
  // Learned entries first: a table truncated at the cap should drop the router's
  // own port MACs before it drops a client somebody is looking for.
  allHosts.sort((a, b) => (a.local === b.local)
    ? a.mac.localeCompare(b.mac)
    : (a.local ? 1 : -1));

  return {
    bridges,
    ports,
    hosts:      allHosts.slice(0, HOST_CAP),
    hostTotal:  allHosts.length,
    hostCap:    HOST_CAP,
    ratesAvailable: !!ifaces,
  };
}

class BridgesCollector {
  constructor({ ros, io, state, pollMs, ifStatus, streamMode }) {
    this.ros    = ros;
    this.io     = io;
    this.state  = state;
    this.pollMs = clampPoll(pollMs, 5000, 60000, 2000);
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][bridges]` : '[bridges]';

    // DISABLEABLE, so this may be a null-collector stub with lastPayload null.
    // Guarded at every read.
    this.ifStatus = ifStatus || null;

    // Delivery switch (#105). Both modes emit at pollMs — the rates come from
    // ifStatus in memory and have to keep moving either way — but stream mode
    // re-reads the bridge and port tables only when the router says they
    // changed, and poll mode holds no channel open at all.
    this.streamMode = streamMode !== false;
    this._poll   = createPollLoop(() => this._tick(), () => this.pollMs);
    this._listen = createListenRefresh({
      ros, cmd: LISTEN_CMD, label: this._lbl,
      onEvent: () => { this._dirty = true; this._tick().catch(() => {}); },
    });
    // The first tick always reads; after that stream mode waits to be told.
    this._dirty  = true;
    this._ticks  = 0;
    this._cfg    = { bridgeRows: [], portRows: [] };
    this._lastFp = '';
    // undefined = unprobed, false = this router has no such menu, stop asking.
    this._bridgeAvailable = undefined;
    this._portAvailable   = undefined;
    this._hostAvailable   = undefined;
    this.lastPayload = null;
  }

  async _read(cmd, flag) {
    if (this[flag] === false) return [];
    try {
      const rows = await this.ros.write(cmd[0], [cmd[1]]);
      this[flag] = true;
      return (rows || []).filter(r => r && Object.keys(r).length);
    } catch (e) {
      const msg = String((e && e.message) || e).toLowerCase();
      if (msg.includes('no such') || msg.includes('unknown command')) this[flag] = false;
      // The host table is the one menu a read-only API user can be denied while
      // the rest still answers; topology.js latches the same way rather than
      // asking forever.
      else if (msg.includes('not enough permissions') || msg.includes('permission denied')) this[flag] = false;
      else this.state.lastBridgesErr = e && e.message ? e.message : String(e);
      return [];
    }
  }

  /**
   * Re-read now, after a write, so the page shows what the router did.
   *
   * Sets `_dirty` rather than reaching past _tick(): that flag already means
   * "config changed, read it on this tick", which is exactly the situation a
   * write creates.
   */
  async refreshNow() {
    if (!this.ros.connected) return;
    this._dirty = true;
    await this._tick();
  }

  async _tick() {
    if (!this.ros.connected) return;

    // Config: every tick in poll mode; on a listen event or the safety interval
    // in stream mode. Hosts are volatile — a MAC is learned or ages out with no
    // config event behind it — so they are read every tick in both modes.
    const readConfig = !this.streamMode || this._dirty || (this._ticks % CONFIG_EVERY === 0);
    this._ticks++;
    if (readConfig) {
      const [bridgeRows, portRows] = await Promise.all([
        this._read(BRIDGE_CMD, '_bridgeAvailable'),
        this._read(PORT_CMD,   '_portAvailable'),
      ]);
      this._cfg = { bridgeRows, portRows };
      this._dirty = false;
    }
    const hostRows = await this._read(HOST_CMD, '_hostAvailable');

    const built = buildBridgeRows(this._cfg.bridgeRows, this._cfg.portRows, hostRows,
                                  this.ifStatus && this.ifStatus.lastPayload);
    const payload = {
      ts: Date.now(), pollMs: this.pollMs,
      ...built,
      // So the page can say "this router has no bridges" rather than showing an
      // empty table, which reads as a failure.
      available:      this._bridgeAvailable !== false,
      hostsAvailable: this._hostAvailable !== false,
    };
    // Assigned unconditionally: sendInitialState replays it, so a socket that
    // connects during a quiet spell must still get the current view.
    this.lastPayload = payload;
    this.state.lastBridgesTs = payload.ts;

    const fp = JSON.stringify({
      b: built.bridges.map(b => [b.name, b.protocolMode, b.vlanFiltering, b.igmpSnooping,
                                 b.running, b.portCount, b.rxMbps, b.txMbps]),
      p: built.ports.map(p => [p.interface, p.bridge, p.pvid, p.role, p.inactive, p.disabled]),
      h: built.hostTotal,
    });
    if (fp === this._lastFp) return;
    this._lastFp = fp;
    this.io.to('page-bridges').emit('bridges:update', payload);
  }

  _startDelivery() {
    this._poll.start();
    if (this.streamMode) this._listen.start();
  }

  _stopDelivery() {
    this._poll.stop();
    this._listen.stop();
  }

  async start() {
    if (this.ros.connected) await this._tick();
    this._startDelivery();
    this.ros.on('close', () => this._stopDelivery());
    this.ros.on('connected', async () => {
      this._stopDelivery();
      // A reconnect may be a different router, so every latch and the config
      // cadence reset with it.
      this._lastFp = '';
      this._dirty  = true;
      this._ticks  = 0;
      this._bridgeAvailable = this._portAvailable = this._hostAvailable = undefined;
      await this._tick();
      this._startDelivery();
    });
  }

  suspend() { this._stopDelivery(); }
  resume()  { if (this.ros.connected) this._startDelivery(); }

  stop() { this._stopDelivery(); this._lastFp = ''; }
}

BridgesCollector.buildBridgeRows = buildBridgeRows;
BridgesCollector.HOST_CAP        = HOST_CAP;
module.exports = BridgesCollector;
