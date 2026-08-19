'use strict';
/**
 * VLANs collector (issue #32 — "Uplink & VLAN Analytics").
 *
 * Three read-only prints, joined into one view:
 *
 *   /interface/vlan        the L3 VLAN interfaces  — id, parent, mtu, running
 *   /interface/bridge/vlan the trunk table         — tagged / untagged per VLAN
 *   /interface/bridge/port each port's pvid        — its untagged VLAN
 *
 * Poll-only: none of these menus offers a /listen, so there is no stream path
 * and streamKey is null in the registry.
 *
 * RATES AND CLIENT COUNTS ARE BORROWED, NOT FETCHED. interfaceStatus already
 * computes rxMbps/txMbps for every interface including VLANs, and dhcpLeases
 * already joins DHCP server -> interface -> vlan-id. Both are taken by
 * reference the way topology.js takes them.
 *
 * Deliberately NOT done by adding page-vlans to interfaceStatus's emit rooms:
 * that payload carries IP addresses, MAC addresses and error counters, and
 * would hand all of it to anyone holding read on `vlans` — a different
 * permission from `interfaces`. The join happens here and only VLAN-shaped
 * rows leave. Fields are projected by name for the same reason; spreading the
 * interface object would silently re-leak everything.
 */

const { clampPoll, createPollLoop, createListenRefresh } = require('./util');

const VLAN_CMD   = ['/interface/vlan/print',
                    '=.proplist=.id,name,vlan-id,interface,mtu,running,disabled,comment'];
const BVLAN_CMD  = ['/interface/bridge/vlan/print',
                    '=.proplist=.id,bridge,vlan-ids,tagged,untagged,current-tagged,dynamic,disabled'];
const BPORT_CMD  = ['/interface/bridge/port/print',
                    '=.proplist=.id,bridge,interface,pvid,frame-types,disabled'];

const LISTEN_CMD = '/interface/vlan/listen';

// Re-read the config tables on this multiple of the emit tick. VLAN topology
// changes when somebody edits the router, not every five seconds; the rate half
// costs no router I/O at all because it reads ifStatus from memory.
const CONFIG_EVERY = 12;

// 802.1Q. 0 is priority-tagged and 4095 is reserved, so neither is a VLAN.
const VLAN_MIN = 1;
const VLAN_MAX = 4094;

// A trunk port may legally carry `2-4094`. Expanding that gives 4093 ids from a
// single row, rebuilt every poll and shipped over the socket. Past this many the
// range is carried as a tuple instead.
const RANGE_CAP = 64;

const _bool = (v) => v === true || v === 'true';

/**
 * RouterOS `vlan-ids` is a LIST, not an id: "5", "5,10,20" and "1,10-12" are all
 * valid. A plain parseInt reads the first and silently loses the rest.
 *
 * Returns { ids, ranges, raw, truncated }. `raw` is kept verbatim because that
 * is the string the operator sees in WinBox, and the UI should be able to show
 * exactly what is configured rather than a reconstruction of it.
 */
function parseVlanIds(raw) {
  const out = { ids: [], ranges: [], raw: raw == null ? '' : String(raw), truncated: false };
  const seen = new Set();
  for (const part of out.raw.split(',')) {
    const t = part.trim();
    if (!t) continue;
    const range = /^(\d+)\s*-\s*(\d+)$/.exec(t);
    if (range) {
      const lo = Number(range[1]), hi = Number(range[2]);
      // A reversed range is not something anyone can enter in WinBox, so
      // guessing at an interpretation is worse than dropping it.
      if (!(lo >= VLAN_MIN && hi <= VLAN_MAX && lo <= hi)) continue;
      out.ranges.push([lo, hi]);
      if (hi - lo + 1 > RANGE_CAP) { out.truncated = true; continue; }
      for (let v = lo; v <= hi; v++) if (!seen.has(v)) { seen.add(v); out.ids.push(v); }
      continue;
    }
    if (!/^\d+$/.test(t)) continue;
    const v = Number(t);
    if (v < VLAN_MIN || v > VLAN_MAX) continue;
    if (!seen.has(v)) { seen.add(v); out.ids.push(v); }
  }
  out.ids.sort((a, b) => a - b);
  return out;
}

const _split = (v) => String(v || '').split(',').map(x => x.trim()).filter(Boolean);

/**
 * Build the per-VLAN view.
 *
 * Pure and fully injected so it can be tested without a router or a collector.
 * `ifPayload` is interfaceStatus.lastPayload, or null when that collector is
 * disabled or has not produced anything yet.
 */
function buildVlanRows(vlanRows, bridgeVlanRows, bridgePortRows, ifPayload, leaseVlanCounts) {
  const rates = new Map();
  const ifaces = (ifPayload && Array.isArray(ifPayload.interfaces)) ? ifPayload.interfaces : null;
  if (ifaces) for (const i of ifaces) if (i && i.name) rates.set(i.name, i);

  const byId = new Map();
  const vlan = (id) => {
    if (!byId.has(id)) {
      byId.set(id, { vlanId: id, interfaces: [], tagged: [], untagged: [], bridges: [],
                     clients: 0, rxMbps: null, txMbps: null });
    }
    return byId.get(id);
  };

  // 1. L3 VLAN interfaces. Two rows may share one vlan-id on different parents,
  //    so interfaces is an array and the rates are never summed into one number.
  for (const r of vlanRows || []) {
    if (!r || !r.name) continue;                       // also drops {undefined:''}
    const id = Number(r['vlan-id']);
    if (!Number.isFinite(id)) continue;
    const e = vlan(id);
    const live = rates.get(r.name);
    e.interfaces.push({
      // The row id, so the page can open a VLAN in the edit form.
      id:       r['.id'] || '',
      name:     String(r.name),
      parent:   r.interface || '',
      mtu:      r.mtu ? Number(r.mtu) : null,
      running:  _bool(r.running),
      disabled: _bool(r.disabled),
      comment:  r.comment || '',
      // null, never 0: "the router did not report this" and "this VLAN is idle"
      // must stay tellable apart, or the page confidently shows 0 Mbps on a busy
      // VLAN during the startup window.
      rxMbps:   live && typeof live.rxMbps === 'number' ? live.rxMbps : null,
      txMbps:   live && typeof live.txMbps === 'number' ? live.txMbps : null,
    });
  }

  // 2. Bridge VLAN table. Dynamic rows are kept in the JOIN — on a real router
  //    most membership comes from them — and only hidden at RENDER time.
  const bridgeVlans = [];
  for (const r of bridgeVlanRows || []) {
    if (!r || r['vlan-ids'] === undefined) continue;
    const parsed = parseVlanIds(r['vlan-ids']);
    const tagged = _split(r.tagged);
    const untagged = _split(r.untagged);
    const row = {
      bridge:        r.bridge || '',
      raw:           parsed.raw,
      ids:           parsed.ids,
      ranges:        parsed.ranges,
      truncated:     parsed.truncated,
      tagged, untagged,
      currentTagged: _split(r['current-tagged']),
      dynamic:       _bool(r.dynamic),
      disabled:      _bool(r.disabled),
    };
    bridgeVlans.push(row);
    for (const id of parsed.ids) {
      const e = vlan(id);
      for (const t of tagged)   if (!e.tagged.includes(t))   e.tagged.push(t);
      for (const u of untagged) if (!e.untagged.includes(u)) e.untagged.push(u);
      if (row.bridge && !e.bridges.includes(row.bridge)) e.bridges.push(row.bridge);
    }
  }

  // 3. Bridge ports. pvid is the port's untagged VLAN — this is what puts a WiFi
  //    virtual AP on a VLAN, and it is the only source for a VLAN that exists
  //    purely at layer 2 with no /interface/vlan row.
  const ports = [];
  for (const r of bridgePortRows || []) {
    if (!r || !r.interface) continue;
    const pvid = Number(r.pvid);
    const row = { bridge: r.bridge || '', interface: String(r.interface),
                  pvid: Number.isFinite(pvid) ? pvid : null,
                  frameTypes: r['frame-types'] || '', disabled: _bool(r.disabled) };
    ports.push(row);
    if (row.pvid !== null && row.pvid >= VLAN_MIN && row.pvid <= VLAN_MAX) {
      const e = vlan(row.pvid);
      if (!e.untagged.includes(row.interface)) e.untagged.push(row.interface);
    }
  }

  // 4. Client counts. dhcpLeases stores vlanId as the STRING '10' while every id
  //    here is a number, so both sides are coerced. Comparing them directly
  //    yields 0 for every VLAN, which reads as "no DHCP clients" rather than as
  //    a bug — the most plausible wrong answer this collector could give.
  if (leaseVlanCounts) {
    for (const [k, n] of leaseVlanCounts) {
      const id = Number(k);
      if (Number.isFinite(id) && byId.has(id)) byId.get(id).clients = n;
    }
  }

  // 5. Roll the interface rates up per VLAN, still null when nothing reported.
  for (const e of byId.values()) {
    const rx = e.interfaces.filter(i => i.rxMbps !== null);
    const tx = e.interfaces.filter(i => i.txMbps !== null);
    e.rxMbps = rx.length ? rx.reduce((n, i) => n + i.rxMbps, 0) : null;
    e.txMbps = tx.length ? tx.reduce((n, i) => n + i.txMbps, 0) : null;
    e.name = e.interfaces.length ? e.interfaces.map(i => i.name).join(', ') : '';
    e.tagged.sort(); e.untagged.sort();
  }

  return {
    vlans: [...byId.values()].sort((a, b) => a.vlanId - b.vlanId),
    bridgeVlans,
    ports,
    dynamicCount: bridgeVlans.filter(r => r.dynamic).length,
    ratesAvailable: !!ifaces,
  };
}

class VlansCollector {
  constructor({ ros, io, state, pollMs, ifStatus, dhcpLeases, streamMode }) {
    this.ros    = ros;
    this.io     = io;
    this.state  = state;
    this.pollMs = clampPoll(pollMs, 5000, 60000, 2000);
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][vlans]` : '[vlans]';

    // Both DISABLEABLE — lastPayload may be null, and the collector may be a
    // null-collector stub. Guarded at every read.
    this.ifStatus   = ifStatus || null;
    this.dhcpLeases = dhcpLeases || null;

    // Delivery switch (#105). Both modes emit at pollMs — rates come from
    // ifStatus in memory and must keep moving — but stream mode re-reads the
    // VLAN and bridge tables only when the router says they changed, and poll
    // mode holds no channel open at all.
    this.streamMode = streamMode !== false;
    this._poll   = createPollLoop(() => this._tick(), () => this.pollMs);
    this._listen = createListenRefresh({
      ros, cmd: LISTEN_CMD, label: this._lbl,
      onEvent: () => { this._dirty = true; this._tick().catch(() => {}); },
    });
    this._dirty  = true;
    this._cfg    = { vlanRows: [], bridgeVlanRows: [], bridgePortRows: [] };
    this._ticks  = 0;
    this._lastFp = '';
    this.lastPayload = null;
  }

  async _loadConfig() {
    try {
      const [vlanRows, bridgeVlanRows, bridgePortRows] = await Promise.all([
        this.ros.write(VLAN_CMD[0],  [VLAN_CMD[1]]),
        this.ros.write(BVLAN_CMD[0], [BVLAN_CMD[1]]).catch(() => []),
        this.ros.write(BPORT_CMD[0], [BPORT_CMD[1]]).catch(() => []),
      ]);
      // The bridge menus are tolerated as missing: a router with no bridge still
      // has VLAN interfaces worth showing.
      this._cfg = { vlanRows: vlanRows || [], bridgeVlanRows: bridgeVlanRows || [],
                    bridgePortRows: bridgePortRows || [] };
      this.state.lastVlansErr = null;
    } catch (e) {
      this.state.lastVlansErr = e && e.message ? e.message : String(e);
    }
  }

  /** DHCP lease counts per VLAN id, from data dhcpLeases already holds. */
  _leaseCounts() {
    const lp = this.dhcpLeases && this.dhcpLeases.lastPayload;
    if (!lp || !Array.isArray(lp.leases)) return null;
    const counts = new Map();
    for (const l of lp.leases) {
      if (!l || !l.vlanId) continue;
      const k = String(l.vlanId);
      counts.set(k, (counts.get(k) || 0) + 1);
    }
    return counts;
  }

  _emit() {
    const built = buildVlanRows(
      this._cfg.vlanRows, this._cfg.bridgeVlanRows, this._cfg.bridgePortRows,
      this.ifStatus && this.ifStatus.lastPayload, this._leaseCounts());

    const payload = { ts: Date.now(), pollMs: this.pollMs, ...built };
    // Assigned unconditionally: sendInitialState replays it, so a socket that
    // connects during a quiet spell must still get the current view. Only the
    // emit is fingerprint-gated.
    this.lastPayload = payload;
    this.state.lastVlansTs = payload.ts;

    const fp = JSON.stringify({
      v: built.vlans.map(v => [v.vlanId, v.name, v.tagged.join('|'), v.untagged.join('|'), v.clients]),
      r: built.vlans.map(v => [v.rxMbps, v.txMbps]),
      d: built.dynamicCount,
    });
    if (fp === this._lastFp) return;
    this._lastFp = fp;
    this.io.to('page-vlans').emit('vlans:update', payload);
  }

  /**
   * Re-read now, after a write, so the page shows what the router did.
   *
   * Sets `_dirty` rather than calling _loadConfig() directly: that flag already
   * means "the config changed, read it on this tick", and reusing it keeps one
   * path into the read instead of two.
   */
  async refreshNow() {
    if (!this.ros.connected) return;
    this._dirty = true;
    await this._tick();
  }

  async _tick() {
    if (!this.ros.connected) return;
    // Poll mode re-reads on the config cadence; stream mode waits to be told,
    // with the same cadence left in as a safety net against a missed event.
    if (!this.streamMode || this._dirty || this._ticks % CONFIG_EVERY === 0) {
      await this._loadConfig();
      this._dirty = false;
    }
    this._ticks++;
    this._emit();
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
    if (this.ros.connected) { await this._loadConfig(); this._dirty = false; this._ticks = 1; this._emit(); }
    this._startDelivery();
    this.ros.on('close', () => this._stopDelivery());
    this.ros.on('connected', async () => {
      this._stopDelivery();
      this._lastFp = '';
      this._ticks = 0;
      await this._loadConfig();
      this._dirty = false;
      this._ticks = 1;
      this._emit();
      this._startDelivery();
    });
  }

  suspend() { this._stopDelivery(); }
  resume()  { if (this.ros.connected) this._startDelivery(); }

  stop() { this._stopDelivery(); this._lastFp = ''; }
}

VlansCollector.parseVlanIds  = parseVlanIds;
VlansCollector.buildVlanRows = buildVlanRows;
module.exports = VlansCollector;
