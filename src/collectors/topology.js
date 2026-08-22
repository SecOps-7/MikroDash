'use strict';
// Network topology — discovers neighbouring infrastructure via /ip/neighbor
// (LLDP / CDP / MNDP) and turns it into a node/edge graph for the Topology page.
//
// Scope: ONE HOP. LLDP frames are link-local and are not forwarded, so the router
// only ever learns about devices in its own broadcast domains. MNDP and CDP can
// cross an unmanaged switch, so several neighbours may share one local interface
// — that is a shared segment, not a chain, and it is reported as `shared` rather
// than being invented into a hierarchy.
//
// Delivery: /ip/neighbor has no /listen variant, so the stream path is
// `print =interval=N` exactly as dhcpNetworks does. The poll path runs the same
// print through createPollLoop. Both funnel into one _rebuild(), so the two
// paths cannot produce different payloads (#105).

const { stopStreamSafe, createPollLoop } = require('./util');

// Hoisted so the stream and poll paths cannot drift apart.
//
// No =.proplist=: the neighbour table is bounded by RouterOS itself (RAM-MB x 16
// per interface), the rows are small, and every field is used either for
// classification or for the detail panel. Asking for everything also means a
// firmware that adds a field starts showing it without a code change.
const NEIGHBOR_CMD = '/ip/neighbor/print';
const SETTINGS_CMD = '/ip/neighbor/discovery-settings/print';

// The bridge MAC table is what turns a flat neighbour list into a real tree.
// /ip/neighbor reports the interface a frame ARRIVED on, which for a tagged
// device is the VLAN interface ("Home") rather than the physical port — so two
// devices on the same cable can look like they sit on different links. The host
// table gives the actual bridge port behind which each MAC lives, which is the
// only thing that lets a device be matched to the switch in front of it.
// `?local=false` drops the bridge's own MACs.
const HOSTS_CMD = ['/interface/bridge/host/print', '?local=false',
                   '=.proplist=mac-address,on-interface,bridge,vid'];

// VLAN ids are meaningless read as bare numbers, so they are resolved to the
// interface names the operator actually uses ("Home", "IoT", "Guest").
const VLAN_CMD = ['/interface/vlan/print', '=.proplist=name,vlan-id'];

// Client attribution. A wireless client is bridged onto the radio it associated
// with — including a CAPsMAN-managed remote AP's radio, which appears on the
// controller as its own interface. So the bridge port already names the radio;
// these tables only translate that radio into a physical device:
//   wifi/print          radio-mac per interface (empty on a virtual AP, hence
//                       the master-interface chain)
//   capsman/remote-cap  which base MAC belongs to which managed AP
//   registration-table  signal / SSID / uptime, and proof a MAC is wireless
const WIFI_CMDS = {
  ifaces: ['/interface/wifi/print', '=.proplist=name,radio-mac,master-interface,disabled'],
  caps:   ['/interface/wifi/capsman/remote-cap/print', '=.proplist=identity,address,board-name,state'],
  reg:    ['/interface/wifi/registration-table/print',
           '=.proplist=mac-address,interface,ssid,signal,uptime'],
};
// Pre-7.13 hardware runs the legacy stack; same idea, different paths.
const WIFI_LEGACY = {
  ifaces: ['/interface/wireless/print', '=.proplist=name,mac-address,master-interface,disabled'],
  reg:    ['/interface/wireless/registration-table/print',
           '=.proplist=mac-address,interface,signal-strength,uptime'],
};

// A hard ceiling on client nodes. The map is a diagram, not an inventory — past
// a few hundred it stops being readable and the payload stops being cheap.
const MAX_CLIENTS = 400;

// A node that stops being advertised is kept this long, flagged `gone`, before
// being dropped. RouterOS simply ages entries out, so without this a device that
// dies just vanishes from the map — the least useful thing a topology view can do.
const RETAIN_MS = 300000;

// Never emit more often than this when nothing structural changed. Latency and
// age tick constantly; without a floor they would push a full payload per probe.
const EMIT_MIN_MS = 5000;

// Ping round-robin: one probe in flight, ever. Concurrent API channels — not data
// volume — are what overwhelm small hardware, so N nodes must not mean N streams.
const PING_STEP_MS = 3000;
const MAX_PING_TARGETS = 24;
const PING_WINDOW = 5;

const DENIED_RE = /not enough privileges|permission denied|cannot run|no such command/i;

/** Split a RouterOS comma list into clean tokens. */
function splitList(v) {
  if (!v) return [];
  return String(v).split(',').map(s => s.trim()).filter(Boolean);
}

/**
 * Device classification.
 *
 * Captured from real hardware: `system-caps` is LLDP-only and comes back EMPTY
 * for every MNDP-discovered MikroTik neighbour, so the board fallback is the
 * common path rather than an edge case. Observed literals are plain lowercase
 * comma lists — "bridge" (a Meraki switch) and "bridge,router" (a MikroTik
 * router). Matching is therefore set membership over a token list with unknown
 * tokens tolerated, not an exact-string switch.
 *
 * `typeSource` is returned so the UI can mark a guess as a guess.
 */
const CAP_MATCH = [
  ['ap',       ['wlan-access-point', 'wlan-ap', 'wlan_ap', 'wlanap', 'wlan']],
  ['station',  ['station-only', 'station']],
  ['phone',    ['telephone', 'phone', 'voice']],
  ['modem',    ['docsis-cable-device', 'docsis']],
  ['repeater', ['repeater']],
];

// Board families. Ordered: the first hit wins, so AP prefixes are tested before
// the generic RouterBOARD ones. `hAP` is deliberately NOT an AP — it is a router
// with a radio, and drawing home routers as access points would be wrong.
const BOARD_MATCH = [
  ['switch', /^(crs|css|fiberbox)/i],
  ['ap',     /^(cap|wap|map|wsap|audience|sxt|lhg|ldf|disc|groove|metal|qrt|basebox|omnitik|netmetal|cube|ltap|knot|sextant)/i],
  ['router', /^(ccr|rb|hap|hex|chateau|powerbox|l0\d|c5\d|d52|stormboard)/i],
];

function matchBoard(board) {
  const b = String(board || '').trim();
  if (!b) return null;
  for (const [type, re] of BOARD_MATCH) if (re.test(b)) return type;
  return null;
}

function classifyDevice(row) {
  // system-caps-enabled is what the device currently DOES; system-caps is what it
  // merely supports. A CRS switch supports routing but only enables bridging, so
  // preferring "enabled" is what keeps it a switch.
  const caps = splitList(row['system-caps-enabled']).map(s => s.toLowerCase());
  const capsSupported = splitList(row['system-caps']).map(s => s.toLowerCase());
  const eff = caps.length ? caps : capsSupported;

  if (eff.length) {
    for (const [type, tokens] of CAP_MATCH) {
      if (eff.some(c => tokens.includes(c))) return { type, typeSource: 'caps' };
    }
    const isRouter = eff.includes('router');
    const isBridge = eff.includes('bridge') || eff.includes('switch');
    // Both set is the normal case for a MikroTik router seen over LLDP, so break
    // the tie on the board rather than picking arbitrarily.
    if (isRouter && isBridge) {
      const byBoard = matchBoard(row.board);
      return { type: byBoard || 'router', typeSource: byBoard ? 'board' : 'caps' };
    }
    if (isRouter) return { type: 'router', typeSource: 'caps' };
    if (isBridge) return { type: 'switch', typeSource: 'caps' };
  }

  const byBoard = matchBoard(row.board);
  if (byBoard) return { type: byBoard, typeSource: 'board' };

  const platform = String(row.platform || '').trim();
  if (platform && !/^mikrotik$/i.test(platform)) return { type: 'other', typeSource: 'platform' };
  if (platform) return { type: 'router', typeSource: 'platform' };

  return { type: 'unknown', typeSource: 'unknown' };
}

/** RouterOS age strings ("5s", "1m20s") → seconds. Never NaN. */
function parseAgeSec(v) {
  if (v === undefined || v === null || v === '') return null;
  const s = String(v).trim();
  if (/^\d+$/.test(s)) return parseInt(s, 10);
  const m = s.match(/\d+[wdhms]/g);
  if (!m) return null;
  let sec = 0;
  for (const part of m) {
    const n = parseInt(part, 10);
    if (!Number.isFinite(n)) continue;
    const unit = part[part.length - 1];
    if (unit === 'w') sec += n * 604800;
    else if (unit === 'd') sec += n * 86400;
    else if (unit === 'h') sec += n * 3600;
    else if (unit === 'm') sec += n * 60;
    else sec += n;
  }
  return sec;
}

/**
 * RouterOS RTT strings → milliseconds. Sub-millisecond replies come back in
 * MICROSECONDS ("413us"), so stripping the unit and treating the number as ms
 * turns a 0.4 ms LAN hop into 413 ms — fast enough to look like a broken link.
 * Same parse as ping.js:_parseRtt; deliberately identical so the two agree.
 */
function parseRttMs(val) {
  if (!val) return null;
  const m = String(val).match(/([\d.]+)\s*(us|ms|s)?/);
  if (!m) return null;
  const v = parseFloat(m[1]);
  if (!Number.isFinite(v)) return null;
  if (m[2] === 'us') return +(v / 1000).toFixed(3);
  if (m[2] === 's') return +(v * 1000).toFixed(3);
  return v;
}

/** First five octets of a MAC — the granularity at which a device's radios and
 *  its base address agree (MikroTik assigns radios as base+1, +2 …). */
function macPrefix(mac) {
  return String(mac || '').toUpperCase().split(':').slice(0, 5).join(':');
}

function isIPv4(v) {
  return typeof v === 'string' && /^(\d{1,3}\.){3}\d{1,3}$/.test(v) &&
    v.split('.').every(o => Number(o) >= 0 && Number(o) <= 255);
}

class TopologyCollector {
  constructor({ ros, io, state, pollMs, streamMode, rid, arp, ifStatus, system, dhcpLeases, showClients }) {
    this.ros = ros;
    this.io = io;
    this.state = state;
    this.rid = rid;
    this.arp = arp || null;
    this.dhcpLeases = dhcpLeases || null;
    // Client discovery costs three extra tables per interval, so it is a flag
    // rather than something the collector does unconditionally.
    this.showClients = showClients !== false;
    this.ifStatus = ifStatus || null;   // DISABLEABLE — lastPayload may be null
    this.system = system || null;
    this.streamMode = streamMode !== false;
    this.pollMs = Math.max(10000, Math.min(300000, Number(pollMs) || 30000));
    this._lbl = ros && ros.routerLabel ? `[${ros.routerLabel}][topology]` : '[topology]';

    this._rows = [];
    this._batch = [];
    this._debounce = null;
    this._rebuildDebounce = null;
    this._discovery = null;

    this._stream = null;
    this._restarting = false;
    this._restartTimer = null;
    this._heartbeat = null;

    this._hosts = new Map();       // MAC -> physical bridge port
    this._hostVlans = new Map();   // MAC -> [vid]
    this._vlanNames = new Map();   // vid -> interface name
    this._hostsTs = 0;
    this._hostsDenied = false;
    this._ifaceRadio = new Map();  // wifi interface -> radio MAC
    this._capByPrefix = new Map(); // radio MAC prefix -> managed AP
    this._assoc = new Map();       // client MAC -> association details
    this._clientsTruncated = 0;
    this._seen = new Map();        // key -> { firstSeen, lastSeen, node }
    this._ping = new Map();        // key -> { rtt, loss, ts, window: [] }
    this._pingCursor = 0;
    this._pingDenied = false;
    this._permissionDenied = false;

    this._lastFp = '';
    this._lastEmitTs = 0;
    this.lastPayload = null;

    this._poll = createPollLoop(() => this._pollOnce(), () => this.pollMs);
    this._pingLoop = createPollLoop(() => this._pingNextOnce(), () => PING_STEP_MS);
  }

  // ── fetch ────────────────────────────────────────────────────────────────

  async _pollOnce() {
    if (!this.ros.connected || this._permissionDenied) return;
    try {
      this._rows = (await this.ros.write(NEIGHBOR_CMD)) || [];
      this.state.lastTopologyErr = null;
    } catch (e) {
      const msg = e && e.message ? e.message : String(e);
      if (DENIED_RE.test(msg)) {
        this._permissionDenied = true;
        console.warn('%s', this._lbl + ' /ip/neighbor not permitted for this API user — topology disabled.');
        return;
      }
      this.state.lastTopologyErr = msg;
    }
    this._scheduleRebuild();
  }

  /**
   * Refresh the bridge MAC table, at most once per poll interval. A plain
   * request rather than a second stream: concurrent channels are what overwhelm
   * small hardware, and this table only needs to be as fresh as the neighbour
   * list it annotates.
   */
  /** One throttled refresh of everything that annotates the neighbour list. */
  async _refreshFabric() {
    if (!this.ros.connected) return;
    if (Date.now() - this._hostsTs < Math.min(this.pollMs, 30000)) return;
    this._hostsTs = Date.now();
    await this._refreshHosts();
    await this._refreshWifi();
  }

  async _refreshHosts() {
    if (!this.ros.connected || this._hostsDenied) return;
    try {
      const rows = await this.ros.write(HOSTS_CMD[0], HOSTS_CMD.slice(1));
      const m = new Map();
      const v = new Map();
      for (const r of rows || []) {
        if (!r || typeof r !== 'object') continue;
        const mac = String(r['mac-address'] || '').trim().toUpperCase();
        const port = r['on-interface'];
        if (!mac || !port) continue;
        // First writer wins for the port: a MAC seen on several VLANs of one
        // port yields the same port, and a genuinely moving MAC is not something
        // to chase here. VLANs accumulate, because a trunked device legitimately
        // appears on more than one.
        if (!m.has(mac)) m.set(mac, port);
        const vid = parseInt(r.vid, 10);
        if (Number.isFinite(vid)) {
          if (!v.has(mac)) v.set(mac, []);
          if (!v.get(mac).includes(vid)) v.get(mac).push(vid);
        }
      }
      this._hosts = m;
      this._hostVlans = v;
    } catch (e) {
      // A router with no bridge, or an API user without the right policy: the
      // map still works, it just falls back to the arrival interface.
      const msg = e && e.message ? e.message : String(e);
      if (DENIED_RE.test(msg)) this._hostsDenied = true;
    }
  }

  /**
   * Radio and association tables, on the same throttle as the host table.
   * Everything here is best-effort: a router with no wireless, or an API user
   * without the policy, simply yields wired-only attribution rather than an error.
   */
  async _refreshWifi() {
    if (!this.ros.connected || !this.showClients) return;

    const get = async (spec) => {
      try { return (await this.ros.write(spec[0], spec.slice(1))) || []; }
      catch (_) { return null; }
    };

    let ifaces = await get(WIFI_CMDS.ifaces);
    let reg = ifaces ? await get(WIFI_CMDS.reg) : null;
    let legacy = false;
    if (!ifaces) {                                   // pre-7.13 stack
      ifaces = await get(WIFI_LEGACY.ifaces);
      reg = ifaces ? await get(WIFI_LEGACY.reg) : null;
      legacy = !!ifaces;
    }
    const caps = legacy ? [] : (await get(WIFI_CMDS.caps)) || [];

    // radio-mac per interface, following master-interface for virtual APs. A
    // multi-SSID interface carries no radio of its own, so without this chain
    // every client on a virtual AP would be misattributed to the router.
    const raw = new Map();
    for (const i of ifaces || []) {
      if (!i || !i.name) continue;
      raw.set(i.name, {
        mac: String(i['radio-mac'] || i['mac-address'] || '').toUpperCase(),
        master: i['master-interface'] || '',
      });
    }
    const radioOf = (name, depth = 0) => {
      const r = raw.get(name);
      if (!r || depth > 4) return '';
      if (r.mac) return r.mac;
      return r.master ? radioOf(r.master, depth + 1) : '';
    };
    const ifaceRadio = new Map();
    for (const name of raw.keys()) ifaceRadio.set(name, radioOf(name));

    // A managed AP's radios are not its base MAC but a small offset from it
    // (base+1, +2 …), so match on the first five octets rather than exactly.
    const capByPrefix = new Map();
    for (const c of caps) {
      const base = String(c.address || '').split('%')[0].toUpperCase();
      if (base) capByPrefix.set(macPrefix(base), { identity: c.identity || '', base });
    }

    const assoc = new Map();
    for (const w of reg || []) {
      const mac = String(w['mac-address'] || '').toUpperCase();
      if (!mac) continue;
      assoc.set(mac, {
        iface: w.interface || '',
        ssid: w.ssid || '',
        signal: w.signal !== undefined ? w.signal : (w['signal-strength'] || ''),
        uptime: w.uptime || '',
      });
    }

    this._ifaceRadio = ifaceRadio;
    this._capByPrefix = capByPrefix;
    this._assoc = assoc;
  }

  /** VLAN id -> name. Changes only when the operator edits the config, so this
   *  rides along with the discovery settings on connect rather than per tick. */
  async _fetchVlans() {
    if (!this.ros.connected) return;
    try {
      const rows = await this.ros.write(VLAN_CMD[0], VLAN_CMD.slice(1));
      const m = new Map();
      for (const r of rows || []) {
        const vid = parseInt(r && r['vlan-id'], 10);
        if (Number.isFinite(vid) && r.name) m.set(vid, r.name);
      }
      this._vlanNames = m;
    } catch (_) { /* no VLANs configured, or not permitted: ids alone still work */ }
  }

  /** Discovery settings change rarely — fetched once per connect, not per tick. */
  async _fetchDiscovery() {
    if (!this.ros.connected) return;
    try {
      const rows = await this.ros.write(SETTINGS_CMD);
      const r = (rows && rows[0]) || {};
      this._discovery = {
        protocol: splitList(r.protocol),
        mode: r.mode || '',
        interfaceList: r['discover-interface-list'] || '',
        interval: r['discover-interval'] || '',
      };
    } catch (_) { this._discovery = null; }
  }

  // ── stream ───────────────────────────────────────────────────────────────

  _startStream() {
    if (this._stream || this._restarting || !this.ros.connected || this._permissionDenied) return;
    const intervalSec = Math.max(5, Math.round(this.pollMs / 1000));
    const stream = this.ros.stream([NEIGHBOR_CMD, `=interval=${intervalSec}`], null);
    this._stream = stream;

    stream.on('data', (pkt) => {
      // RStream emits [] when the table is empty this interval — rebuild from an
      // empty row set so departed neighbours actually disappear.
      if (Array.isArray(pkt)) {
        if (pkt.length === 0 && !this._batch.length && !this._debounce) {
          this._rows = [];
          this._scheduleRebuild();
        }
        return;
      }
      if (!pkt || typeof pkt !== 'object') return;
      this._batch.push(pkt);
      if (this._debounce) return;
      this._debounce = setTimeout(() => { // codeql[js/resource-exhaustion]
        this._debounce = null;
        this._rows = this._batch;
        this._batch = [];
        this._scheduleRebuild();
      }, 50);
    });

    stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      this._stopStream();
      if (DENIED_RE.test(msg)) {
        this._permissionDenied = true;
        console.warn('%s', this._lbl + ' /ip/neighbor not permitted for this API user — topology disabled.');
        return;
      }
      console.error('%s', this._lbl, 'stream error:', msg);
      this.state.lastTopologyErr = msg;
      if (this.ros.connected && !this._restarting) {
        this._restarting = true;
        this._restartTimer = setTimeout(() => { // codeql[js/resource-exhaustion]
          this._restarting = false;
          this._restartTimer = null;
          this._startStream();
        }, 3000);
      }
    });

    console.log('%s', this._lbl, `streaming ${NEIGHBOR_CMD} interval=${intervalSec}s`);
  }

  _stopStream() {
    if (this._debounce) { clearTimeout(this._debounce); this._debounce = null; }
    if (this._restartTimer) { clearTimeout(this._restartTimer); this._restartTimer = null; }
    this._restarting = false;
    this._batch = [];
    if (this._stream) { stopStreamSafe(this._stream); this._stream = null; }
  }

  // ── ping round-robin ─────────────────────────────────────────────────────

  _pingTargets() {
    if (!this.lastPayload) return [];
    return this.lastPayload.nodes
      .filter(n => n.kind !== 'core' && !n.gone && isIPv4(n.ip))
      .slice(0, MAX_PING_TARGETS)
      .map(n => ({ key: n.key, ip: n.ip }));
  }

  _recordPing(key, replied, rtt) {
    const rec = this._ping.get(key) || { window: [] };
    rec.window.push(replied ? 1 : 0);
    if (rec.window.length > PING_WINDOW) rec.window.shift();
    rec.rtt = replied && Number.isFinite(rtt) ? rtt : null;
    rec.loss = Math.round((1 - rec.window.reduce((a, b) => a + b, 0) / rec.window.length) * 100);
    rec.ts = Date.now();
    this._ping.set(key, rec);
    return rec;
  }

  async _pingNextOnce() {
    if (!this.ros.connected || this._pingDenied) return;
    if (this.io.engine.clientsCount === 0) return;
    const targets = this._pingTargets();
    if (!targets.length) return;

    const t = targets[this._pingCursor % targets.length];
    this._pingCursor = (this._pingCursor + 1) % targets.length;

    try {
      const rows = await this.ros.write('/tool/ping',
        ['=address=' + t.ip, '=count=1', '=interval=1']);
      const row = (rows || []).filter(r => r && typeof r === 'object').pop() || {};
      const rtt = parseRttMs(row.time);
      // received=0 is an explicit timeout; a missing time with no counters means
      // the reply was not usable either way.
      const replied = rtt !== null && row.received !== '0';
      this._recordPing(t.key, replied, rtt);
      this._scheduleRebuild();
    } catch (e) {
      const msg = e && e.message ? e.message : String(e);
      if (DENIED_RE.test(msg)) {
        this._pingDenied = true;
        this._pingLoop.stop();
        console.warn('%s', this._lbl + ' /tool/ping needs the "test" policy — per-device latency disabled.');
        this._scheduleRebuild();
        return;
      }
      // A single unreachable host must never stop the loop.
      this._recordPing(t.key, false, NaN);
    }
  }

  // ── build ────────────────────────────────────────────────────────────────

  _scheduleRebuild() {
    if (this._rebuildDebounce) return;
    this._rebuildDebounce = setTimeout(() => { // codeql[js/resource-exhaustion]
      this._rebuildDebounce = null;
      // _rebuild stays synchronous (and directly testable); only the host-table
      // refresh in front of it is async, and it self-throttles.
      this._refreshFabric().then(() => this._rebuild(), () => this._rebuild());
    }, 10);
  }

  /**
   * Interface names that are bridges, so a neighbour heard on both the bridge and
   * its physical port collapses to the physical one — RouterOS reports `interface`
   * as e.g. "ether1,bridgeLocal". Falls back to a name heuristic when ifStatus is
   * disabled, since it is a disableable collector.
   */
  _bridgeNames() {
    const set = new Set();
    const p = this.ifStatus && this.ifStatus.lastPayload;
    if (p && Array.isArray(p.interfaces)) {
      for (const i of p.interfaces) if (i && i.type === 'bridge' && i.name) set.add(i.name);
    }
    return set;
  }

  _pickIfaces(raw, bridges) {
    const all = splitList(raw);
    if (all.length < 2) return all;
    const physical = all.filter(n => !bridges.has(n) && !/^(bridge|br[-_])/i.test(n));
    return physical.length ? physical : all;
  }

  _rebuild() {
    const now = Date.now();
    const bridges = this._bridgeNames();
    const rows = Array.isArray(this._rows) ? this._rows : [];

    // ── nodes from the current table ──
    const byKey = new Map();
    for (const r of rows) {
      if (!r || typeof r !== 'object' || Array.isArray(r)) continue;
      const mac = String(r['mac-address'] || '').trim().toUpperCase();
      // RouterOS ids look like "*3". Strip the punctuation: the key is persisted
      // as an object key by /api/topology-layout, whose validator keeps its
      // charset tight, and '*' would be silently rejected there — meaning a
      // MAC-less neighbour could never keep a dragged position.
      const id = String(r['.id'] || r.id || '').replace(/[^A-Za-z0-9]/g, '');
      const key = mac || (id ? 'id:' + id : '');
      if (!key) continue;

      const ifaces = this._pickIfaces(r.interface, bridges);

      // A device heard on several interfaces is still one device.
      const prev = byKey.get(key);
      if (prev) { for (const i of ifaces) if (!prev.ifaces.includes(i)) prev.ifaces.push(i); continue; }

      const cls = classifyDevice(r);
      const arpIp = this.arp && typeof this.arp.getByMAC === 'function'
        ? ((this.arp.getByMAC(mac) || {}).ip || '') : '';
      const ip = r.address || r.address4 || arpIp || '';

      const seen = this._seen.get(key) || { firstSeen: now };
      seen.lastSeen = now;
      this._seen.set(key, seen);

      byKey.set(key, {
        key,
        kind: 'neighbor',
        name: r.identity || r.board || mac || ip || key,
        identity: r.identity || '',
        mac,
        ip,
        ip6: r.address6 || '',
        type: cls.type,
        typeSource: cls.typeSource,
        caps: splitList(r['system-caps']),
        capsEnabled: splitList(r['system-caps-enabled']),
        platform: r.platform || '',
        board: r.board || '',
        version: r.version || '',
        softwareId: r['software-id'] || '',
        description: r['system-description'] || '',
        uptime: r.uptime || '',
        ageSec: parseAgeSec(r.age),
        via: splitList(r['discovered-by']),
        running: splitList(r.running),
        ifaces,
        remoteIface: r['interface-name'] || '',
        ipv6: r.ipv6 === 'true' || r.ipv6 === true,
        gone: false,
        firstSeen: seen.firstSeen,
        lastSeen: now,
        rtt: null, loss: null, pingTs: null,
        status: 'unknown',
      });
    }

    // Remember the last good shape of each live node, for the retention branch.
    for (const [key, node] of byKey) {
      const seen = this._seen.get(key);
      if (seen) seen.node = node;
    }

    // ── retain recently departed devices so an outage is visible ──
    for (const [key, seen] of this._seen) {
      if (byKey.has(key)) continue;
      if (now - seen.lastSeen > RETAIN_MS || !seen.node) {
        this._seen.delete(key);
        this._ping.delete(key);
        continue;
      }
      byKey.set(key, { ...seen.node, gone: true, ageSec: null, status: 'down', lastSeen: seen.lastSeen });
    }

    // ── ping + status ──
    for (const node of byKey.values()) {
      const p = this._ping.get(node.key);
      if (p) { node.rtt = p.rtt; node.loss = p.loss; node.pingTs = p.ts; }
      node.status = this._statusFor(node);
    }

    // ── core node ──
    const sys = this.system && this.system.lastPayload ? this.system.lastPayload : null;
    const core = {
      key: 'core',
      kind: 'core',
      // The router's own label, not its RouterOS identity. Every OTHER node here
      // is named from `/ip/neighbor`, which carries `identity`; the core has no
      // such source, because nothing in the app reads `/system/identity` and the
      // system payload has never carried one. This used to try `sys.identity`
      // first, which no producer sets, so that branch could never be taken and
      // the label was reached by accident rather than by choice. Adding an
      // upstream read per router to name one node was not worth it; if that
      // changes, put `identity` on the system payload and prefer it here.
      name: (this.ros && this.ros.routerLabel) || 'Router',
      identity: '',
      mac: '',
      ip: (this.ros && this.ros.host) || '',
      ip6: '',
      type: 'router',
      typeSource: 'self',
      caps: [], capsEnabled: [],
      platform: 'MikroTik',
      board: (sys && (sys.boardName || sys.board)) || '',
      version: (sys && sys.version) || '',
      softwareId: '', description: '',
      uptime: (sys && sys.uptime) || '',
      ageSec: 0, via: [], running: [],
      ifaces: [], remoteIface: '', ipv6: false,
      // The core is the root: it has no parent, and every other node carries
      // these two fields, so it should too rather than being subtly different.
      port: '', parent: null,
      gone: false, firstSeen: 0, lastSeen: now,
      rtt: null, loss: null, pingTs: null,
      cpuLoad: sys && Number.isFinite(Number(sys.cpuLoad)) ? Number(sys.cpuLoad) : null,
      memPct: sys && Number.isFinite(Number(sys.memPct)) ? Number(sys.memPct) : null,
      status: 'up',
    };

    // Ports and parentage must be settled before clients are built: client
    // attribution reads node.port to find the switch fronting a port.
    this._resolveParents(byKey);

    // Clients are appended after the infrastructure tier and carry their own
    // parent, so the client tier can be hidden without disturbing the map above.
    const clients = this._buildClients(byKey, now);
    const clientCount = {};
    for (const c of clients) clientCount[c.parent] = (clientCount[c.parent] || 0) + 1;
    core.clientCount = clientCount.core || 0;
    for (const n of byKey.values()) n.clientCount = clientCount[n.key] || 0;

    const nodes = [core, ...byKey.values(), ...clients];

    // ── edges ──
    // A node with a parent hangs off that parent; everything else hangs off the
    // core. Only the core's own edges carry an `iface`, because only those
    // correspond to a router interface whose throughput the router can measure —
    // attaching the port's rate to a downstream link would double-count it.
    const perPort = new Map();
    for (const n of byKey.values()) {
      if (n.parent) continue;
      const p = n.port || (n.ifaces[0] || '');
      if (!perPort.has(p)) perPort.set(p, new Set());
      perPort.get(p).add(n.key);
    }

    const edges = [];
    for (const n of byKey.values()) {
      if (n.parent && byKey.has(n.parent)) {
        edges.push({
          id: n.parent + '>' + n.key,
          from: n.parent,
          to: n.key,
          iface: '',
          viaPort: n.port || '',
          remoteIface: n.remoteIface,
          shared: false,
          inferred: true,
          gone: n.gone,
        });
        continue;
      }
      // Prefer the physical port over the arrival interface: a tagged device
      // arrives on a VLAN, but the cable it is actually reachable over is the
      // bridge port — which is also the interface whose throughput matches the
      // link being drawn. Falls back to the arrival interface when the MAC is
      // not in the bridge table (a routed or non-bridged link).
      const list = n.port ? [n.port] : (n.ifaces.length ? n.ifaces : ['']);
      for (const i of list) {
        edges.push({
          id: i + '|' + n.key,
          from: 'core',
          to: n.key,
          iface: i,
          viaPort: n.port || '',
          remoteIface: n.remoteIface,
          shared: (perPort.get(n.port || i) || new Set()).size > 1,
          inferred: false,
          gone: n.gone,
        });
      }
    }

    for (const c of clients) {
      edges.push({
        id: 'c|' + c.parent + '>' + c.key,
        from: c.parent,
        to: c.key,
        iface: '',
        viaPort: c.port || '',
        remoteIface: '',
        shared: false,
        // Client links are drawn uniformly. How the parent was decided is still
        // recorded on the node (`attrib`) and surfaced in the detail panel, but
        // not on the canvas: purple is reserved for an inferred link between
        // INFRASTRUCTURE, where it carries real meaning. Styling a whole tier of
        // client links differently just made the map noisier.
        inferred: false,
        client: true,
        gone: false,
      });
    }

    const payload = {
      ts: now,
      routerId: this.rid,
      pollMs: this.streamMode ? 0 : this.pollMs,
      discovery: this._discovery,
      permissionDenied: this._permissionDenied,
      pingDenied: this._pingDenied,
      neighborCount: byKey.size,
      // Only the VLANs clients were actually seen on, so the filter never offers
      // an option that would match nothing.
      vlans: [...new Set(clients.flatMap(c => c.vlans))].sort((a, b) => a - b)
        .map(v => ({ vid: v, name: this._vlanNames.get(v) || String(v) })),
      clientCount: clients.length,
      clientsTruncated: this._clientsTruncated,
      nodes,
      edges,
    };

    // lastPayload is assigned unconditionally — sendInitialState() replays it, so
    // gating it on the fingerprint would leave a new client blank whenever the
    // topology had been static since the last emit.
    this.lastPayload = payload;
    this.state.lastTopologyTs = now;

    if (this.io.engine.clientsCount === 0) return;

    // Fingerprint deliberately excludes rtt / ageSec / lastSeen: they change on
    // every probe and would defeat the dirty check entirely. Structural or status
    // changes push immediately; otherwise EMIT_MIN_MS carries the fresh latency.
    const fp = JSON.stringify({
      n: nodes.map(n => [n.key, n.type, n.status, n.ip, n.name, n.gone ? 1 : 0, n.parent || '']),
      c: clients.length,
      e: edges.map(e => e.id + (e.shared ? '*' : '')),
      d: this._pingDenied ? 1 : 0,
    });
    const changed = fp !== this._lastFp;
    if (!changed && now - this._lastEmitTs < EMIT_MIN_MS) return;
    this._lastFp = fp;
    this._lastEmitTs = now;
    this.io.to('page-topology').emit('topology:update', payload);
  }

  /**
   * Work out which devices sit BEHIND another device, and annotate each node
   * with `port` (the physical bridge port it lives behind) and `parent`.
   *
   * The signal is LLDP's link-locality. LLDP frames use a reserved multicast
   * destination that a conformant bridge must not forward, so a device the
   * router discovered via LLDP is necessarily attached to that port directly.
   * MNDP and CDP are ordinary frames that a switch happily passes along, so a
   * device seen only via those is somewhere further out.
   *
   * Therefore: on a given physical port, at most one device can be the direct
   * neighbour, and it is the LLDP one. Anything else on the same port is behind
   * it. With no LLDP device on the port there is nothing to attribute the others
   * to — an unmanaged switch is invisible by definition — so they stay on the
   * core and the port is flagged `shared` instead of inventing a hierarchy.
   */
  _resolveParents(byKey) {
    // Physical port per node. The bridge host table wins: /ip/neighbor reports
    // the arrival interface, which for a tagged device is the VLAN, and two
    // devices on one cable would otherwise never be grouped together.
    for (const n of byKey.values()) {
      n.port = (n.mac && this._hosts.get(n.mac)) || n.ifaces[0] || '';
      n.parent = null;
    }

    const byPort = new Map();
    for (const n of byKey.values()) {
      if (!n.port) continue;
      if (!byPort.has(n.port)) byPort.set(n.port, []);
      byPort.get(n.port).push(n);
    }

    for (const group of byPort.values()) {
      if (group.length < 2) continue;
      const direct = group.filter(n => n.via.includes('lldp'));
      // Exactly one direct neighbour is the only unambiguous case. Zero means an
      // invisible (unmanaged) switch; more than one means something is forwarding
      // LLDP that should not be. Both stay flat rather than guessing.
      if (direct.length !== 1) continue;
      const parent = direct[0];
      for (const n of group) {
        if (n.key === parent.key) continue;
        n.parent = parent.key;
      }
    }

    // A device cannot be its own ancestor. Cycles are not reachable from the rule
    // above (a parent is never itself re-parented within its own group), but a
    // stale retained node could carry an old parent, so verify rather than trust.
    for (const n of byKey.values()) {
      const seen = new Set([n.key]);
      let p = n.parent;
      while (p) {
        if (seen.has(p)) { n.parent = null; break; }
        seen.add(p);
        const up = byKey.get(p);
        p = up ? up.parent : null;
      }
      // A parent that aged out entirely leaves the child on the core.
      if (n.parent && !byKey.has(n.parent)) n.parent = null;
    }
  }

  /**
   * Build the client tier from the bridge MAC table.
   *
   * Attribution comes from the port a MAC is bridged on, which answers all four
   * cases without guessing:
   *   a wifi interface whose radio belongs to a managed AP  -> that AP
   *   a wifi interface whose radio is one of ours           -> this router
   *   a physical port fronted by a discovered switch        -> that switch
   *   any other physical port                               -> this router
   *
   * Infrastructure MACs are excluded — a discovered neighbour is already a node,
   * and it also appears in the host table.
   */
  _buildClients(byKey, now) {
    if (!this.showClients || !this._hosts.size) return [];

    // Every MAC that belongs to a device already on the map, so a switch or AP
    // is never also drawn as a client of itself.
    const infra = new Set();
    for (const n of byKey.values()) {
      if (n.mac) { infra.add(n.mac); infra.add(macPrefix(n.mac)); }
    }

    // Which discovered device fronts each port, so a client on that port can be
    // attributed to it rather than to the router. Only unparented devices are
    // candidates — something already known to sit behind a switch is not the
    // thing at the front of the cable.
    //
    // A LONE device on a port fronts it whatever protocol found it: there is no
    // second candidate, so nothing to disambiguate. LLDP is only needed to pick
    // between several devices, where it identifies the directly-attached one.
    // Without this, a neighbour that speaks only CDP/MNDP could never own a
    // client even when it is demonstrably the only thing on that port.
    const onPort = new Map();
    for (const n of byKey.values()) {
      if (!n.port || n.parent) continue;
      if (!onPort.has(n.port)) onPort.set(n.port, []);
      onPort.get(n.port).push(n);
    }
    const switchOnPort = new Map();
    for (const [port, list] of onPort) {
      if (list.length === 1) { switchOnPort.set(port, list[0].key); continue; }
      const direct = list.filter(n => n.via.includes('lldp'));
      if (direct.length === 1) switchOnPort.set(port, direct[0].key);
    }

    const out = [];
    let truncated = 0;
    for (const [mac, port] of this._hosts) {
      if (infra.has(mac) || infra.has(macPrefix(mac))) continue;
      if (out.length >= MAX_CLIENTS) { truncated++; continue; }

      const assoc = this._assoc.get(mac) || null;
      const radio = this._ifaceRadio.get(port) || '';
      let parent = 'core';
      let wireless = false;
      // How the parent was decided, so the UI can distinguish an observed
      // association from a deduced one:
      //   radio  - the client associated with this AP's radio (observed)
      //   port   - it merely shares a port with that device (deduced)
      //   direct - straight into a router port (observed)
      let attrib = 'direct';

      if (radio) {
        wireless = true;
        const cap = this._capByPrefix.get(macPrefix(radio));
        // A managed AP's radio resolves to that AP — but only if it is on the
        // map; otherwise the client belongs to the router that fronts it.
        if (cap) {
          for (const n of byKey.values()) {
            if (macPrefix(n.mac) === macPrefix(cap.base)) { parent = n.key; attrib = 'radio'; break; }
          }
        }
      } else if (switchOnPort.has(port)) {
        parent = switchOnPort.get(port);
        attrib = 'port';
      }
      if (assoc) wireless = true;

      const vlans = (this._hostVlans.get(mac) || []).slice().sort((a, b) => a - b);
      const vlanNames = vlans.map(v => this._vlanNames.get(v) || String(v));

      // getNameByMAC returns the whole lease RECORD ({name, ip}), not a string,
      // despite its name — reading it directly renders "[object Object]".
      const lease = (this.dhcpLeases && typeof this.dhcpLeases.getNameByMAC === 'function'
        ? this.dhcpLeases.getNameByMAC(mac) : null) || null;
      const name = (lease && typeof lease === 'object' ? lease.name : lease) || '';
      const ip = (this.arp && typeof this.arp.getByMAC === 'function'
        ? (this.arp.getByMAC(mac) || {}).ip : '') || (lease && lease.ip) || '';

      out.push({
        key: mac,
        kind: 'client',
        name: name || ip || mac,
        identity: name,
        mac,
        ip,
        ip6: '',
        type: wireless ? 'wifi-client' : 'wired-client',
        typeSource: wireless ? 'assoc' : 'bridge',
        caps: [], capsEnabled: [],
        platform: '', board: '', version: '', softwareId: '', description: '',
        uptime: assoc ? assoc.uptime : '',
        ageSec: null,
        via: [], running: [],
        ifaces: [port], remoteIface: '',
        ipv6: false,
        port,
        parent,
        attrib,
        vlans,
        vlanNames,
        ssid: assoc ? assoc.ssid : '',
        signal: assoc ? assoc.signal : '',
        gone: false,
        firstSeen: now, lastSeen: now,
        rtt: null, loss: null, pingTs: null,
        status: 'up',
      });
    }
    this._clientsTruncated = truncated;
    return out;
  }

  _statusFor(node) {
    if (node.gone) return 'down';
    const p = this._ping.get(node.key);
    if (p && p.window && p.window.length) {
      if (p.loss >= 100) return 'down';
      if (p.loss > 0) return 'warn';
      if (Number.isFinite(p.rtt) && p.rtt > 100) return 'warn';
      return 'up';
    }
    if (Number.isFinite(node.ageSec)) return node.ageSec > 90 ? 'warn' : 'up';
    return 'unknown';
  }

  // ── lifecycle ────────────────────────────────────────────────────────────

  _startHeartbeat() {
    if (this._heartbeat) return;
    this._heartbeat = setInterval(() => { // codeql[js/resource-exhaustion]
      if (!this.lastPayload) return;
      if (this.io.engine.clientsCount === 0) return;
      this._lastEmitTs = Date.now();
      this.io.to('page-topology').emit('topology:update', { ...this.lastPayload, ts: Date.now() });
    }, 60000);
  }

  _stopHeartbeat() {
    if (this._heartbeat) { clearInterval(this._heartbeat); this._heartbeat = null; }
  }

  _startDelivery() {
    if (this._permissionDenied) return;
    if (this.streamMode) this._startStream(); else this._poll.start();
    if (!this._pingDenied) this._pingLoop.start();
  }

  _stopDelivery() {
    this._stopStream();
    this._poll.stop();
    this._pingLoop.stop();
    if (this._rebuildDebounce) { clearTimeout(this._rebuildDebounce); this._rebuildDebounce = null; }
  }

  async start() {
    if (this.ros.connected) {
      await this._fetchDiscovery();
      await this._fetchVlans();
      await this._pollOnce();
    }
    this._startDelivery();
    this._startHeartbeat();

    // Registered once, here — never inside a connected handler, which would
    // double the listener count on every reconnect.
    this.ros.on('close', () => { this._stopDelivery(); this._stopHeartbeat(); });
    this.ros.on('connected', async () => {
      this._stopDelivery();
      this._stopHeartbeat();
      this._lastFp = '';
      this._permissionDenied = false;
      this._pingDenied = false;
      await this._fetchDiscovery();
      await this._fetchVlans();
      await this._pollOnce();
      this._startDelivery();
      this._startHeartbeat();
    });
  }

  suspend() { this._stopDelivery(); }

  resume() { if (this.ros.connected) this._startDelivery(); }

  stop() {
    this._stopDelivery();
    this._stopHeartbeat();
    this._lastFp = '';
  }
}

TopologyCollector.classifyDevice = classifyDevice;
TopologyCollector.parseAgeSec = parseAgeSec;
TopologyCollector.parseRttMs = parseRttMs;
TopologyCollector.RETAIN_MS = RETAIN_MS;

module.exports = TopologyCollector;
