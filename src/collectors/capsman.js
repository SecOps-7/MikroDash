'use strict';
/**
 * CAPsMAN collector — the new (`wifi`) stack only.
 *
 *   /interface/wifi/capsman              is this router a manager, and on what
 *   /interface/wifi/cap                  is this router a CAP, and who manages it
 *   /interface/wifi/capsman/remote-cap   the CAPs it manages
 *   /interface/wifi/provisioning         the rules that configure them
 *   /interface/wifi/radio                radios, local and CAP-provided
 *   /interface/wifi/registration-table   clients, for per-CAP counts
 *
 * WHY NEW-STACK ONLY. The legacy `/caps-man` tree answers "no such command" on
 * every router in the fleet this was written against, so a legacy code path here
 * would be fiction that nothing could exercise. wireless.js already probes
 * `/caps-man/registration-table` for legacy CLIENTS and merges them; that stays
 * where it is. If a legacy manager ever needs supporting, it should arrive with
 * a router to test it on.
 *
 * ATTRIBUTION IS EXACT, NOT INFERRED. Both /interface/wifi and /interface/wifi/
 * radio carry a `cap` field on CAP-provided entries, shaped
 * `identity@base-mac%id` — e.g. `cAP@48:A9:8A:E5:CE:34%*41`. topology.js had to
 * match the first five octets of a radio MAC against a CAP's base MAC because
 * its proplist does not request this field; here it does, so a radio is tied to
 * its CAP by what the router says rather than by arithmetic on MAC addresses.
 * The prefix match remains as a fallback for routers that do not report it.
 *
 * WHAT IS READ CAREFULLY. /interface/wifi/configuration and
 * /interface/wifi/security hold the passphrase in clear text. This collector
 * used to avoid them entirely; the configuration card needs both, so they are
 * now read through the proplists in ../routeros/wifiMenus.js, which name no
 * credential, and projected field by name into `profiles` rather than spread —
 * so a proplist widened later still cannot push a new field at the browser.
 * That module's header explains why the list lives in one place.
 */

const { clampPoll, createPollLoop, createListenRefresh } = require('./util');
// The four profile menus the configuration card edits. Their proplists live in
// one place because two collectors read them and they are the ones carrying the
// passphrase — see that file's header.
const { MENUS, named } = require('../routeros/wifiMenus');

const MANAGER_CMD = ['/interface/wifi/capsman/print', ''];
const CAP_CMD     = ['/interface/wifi/cap/print', ''];
const REMOTE_CMD  = ['/interface/wifi/capsman/remote-cap/print',
                     '=.proplist=.id,address,identity,board-name,serial,version,base-mac,common-name,state,connected-time,uptime'];
const PROV_CMD    = ['/interface/wifi/provisioning/print',
                     '=.proplist=.id,supported-bands,action,master-configuration,slave-configurations,name-format,radio-mac,identity-regexp,comment,disabled'];
const RADIO_CMD   = ['/interface/wifi/radio/print', '=.proplist=radio-mac,interface,cap,disabled'];
const IFACE_CMD   = ['/interface/wifi/print',
                     '=.proplist=name,radio-mac,master-interface,cap,disabled,inactive'];
const REG_CMD     = ['/interface/wifi/registration-table/print',
                     '=.proplist=interface,mac-address,uptime,signal,ssid'];

// A CAP joining or leaving is the event worth reacting to instantly; client
// association is already visible on the Wireless page.
const LISTEN_CMD = '/interface/wifi/capsman/remote-cap/listen';

// Manager/CAP settings and provisioning rules are configuration; CAP state,
// radios and clients are live.
const CONFIG_EVERY    = 12;
const CLIENTS_PER_CAP = 200;
// Depth limit when chasing a virtual AP up to its master, mirroring topology.js.
const MASTER_DEPTH = 4;

const _bool = (v) => v === true || v === 'true' || v === 'yes';

function _split(v) {
  if (!v) return [];
  return String(v).split(',').map(s => s.trim()).filter(Boolean);
}

/**
 * Parse `identity@base-mac%id` into its parts.
 *
 * Returns null for anything that is not that shape, so a router reporting the
 * field differently degrades to the MAC-prefix fallback rather than inventing a
 * CAP called `undefined`.
 */
function parseCapField(v) {
  if (!v) return null;
  const s = String(v);
  const at = s.indexOf('@');
  if (at < 1) return null;
  const identity = s.slice(0, at);
  const rest     = s.slice(at + 1);
  const pct      = rest.indexOf('%');
  const baseMac  = (pct === -1 ? rest : rest.slice(0, pct)).toUpperCase();
  const id       = pct === -1 ? '' : rest.slice(pct + 1);
  if (!baseMac) return null;
  return { identity, baseMac, id };
}

/** First five octets — the fallback when a router does not report `cap`. */
function _macPrefix(mac) {
  const s = String(mac || '').toUpperCase();
  const parts = s.split(':');
  return parts.length >= 5 ? parts.slice(0, 5).join(':') : '';
}

/**
 * Join every table into the CAPsMAN view.
 *
 * The interface table is what ties clients to CAPs: a client's registration row
 * names an INTERFACE, and only the master interface carries the `cap` field, so
 * virtual APs are resolved up to their master first.
 */
function buildCapsmanView(managerRow, capRow, remoteRows, provRows, radioRows, ifaceRows, regRows) {
  const mgr = managerRow || {};
  const cap = capRow || {};

  const manager = {
    enabled:    _bool(mgr.enabled),
    interfaces: _split(mgr.interfaces),
    caCertificate: mgr['ca-certificate'] || '',
    certificate:   mgr.certificate || '',
    requirePeerCertificate: _bool(mgr['require-peer-certificate']),
    upgradePolicy: mgr['upgrade-policy'] || '',
    packagePath:   mgr['package-path'] || '',
  };
  const capMode = {
    enabled:             _bool(cap.enabled),
    discoveryInterfaces: _split(cap['discovery-interfaces']),
    capsManAddresses:    _split(cap['caps-man-addresses']),
    currentAddress:      cap['current-caps-man-address'] || '',
    currentIdentity:     cap['current-caps-man-identity'] || '',
    certificate:         cap.certificate || '',
    slavesDatapath:      cap['slaves-datapath'] || '',
  };

  // A router can be both: a manager that also runs its own radios as a CAP
  // pointed at 127.0.0.1, which is exactly how the fleet's hAP AX3 is set up.
  let role = 'none';
  if (manager.enabled && capMode.enabled) role = 'both';
  else if (manager.enabled) role = 'manager';
  else if (capMode.enabled) role = 'cap';

  const caps = [];
  const byIdentity = new Map();
  const byBaseMac  = new Map();
  const byPrefix   = new Map();
  for (const r of remoteRows || []) {
    if (!r || !r.identity) continue;              // also drops {undefined:''}
    const baseMac = String(r['base-mac'] || '').toUpperCase();
    const entry = {
      identity:      String(r.identity),
      address:       r.address || '',
      boardName:     r['board-name'] || '',
      serial:        r.serial || '',
      version:       r.version || '',
      baseMac,
      commonName:    r['common-name'] || '',
      state:         r.state || '',
      connectedTime: r['connected-time'] || '',
      uptime:        r.uptime || '',
      radios:        [],
      clients:       [],
      clientCount:   0,
    };
    caps.push(entry);
    byIdentity.set(entry.identity, entry);
    if (baseMac) {
      byBaseMac.set(baseMac, entry);
      const p = _macPrefix(baseMac);
      if (p) byPrefix.set(p, entry);
    }
  }

  const capFor = (capValue, radioMac) => {
    const parsed = parseCapField(capValue);
    if (parsed) {
      return byBaseMac.get(parsed.baseMac) || byIdentity.get(parsed.identity) || null;
    }
    // Fallback: a CAP's radios sit in the same /40 block as its base MAC.
    const p = _macPrefix(radioMac);
    return p ? (byPrefix.get(p) || null) : null;
  };

  // Radios, split into the manager's own and each CAP's.
  const localRadios = [];
  for (const r of radioRows || []) {
    if (!r || !r['radio-mac']) continue;
    const radio = {
      radioMac:  String(r['radio-mac']).toUpperCase(),
      interface: r.interface || '',
      disabled:  _bool(r.disabled),
    };
    const owner = capFor(r.cap, radio.radioMac);
    if (owner) owner.radios.push(radio);
    else localRadios.push(radio);
  }

  // Interface -> CAP, chasing virtual APs up to their master. Only the master
  // carries `cap`, so without this every client on a guest SSID would look like
  // it belonged to the manager.
  const ifaceByName = new Map();
  for (const r of ifaceRows || []) {
    if (!r || !r.name) continue;
    ifaceByName.set(String(r.name), r);
  }
  const ifaceCap = new Map();
  for (const [name, row] of ifaceByName) {
    let cur = row, depth = 0;
    while (cur && !cur.cap && cur['master-interface'] && depth < MASTER_DEPTH) {
      cur = ifaceByName.get(String(cur['master-interface']));
      depth++;
    }
    const owner = cur ? capFor(cur.cap, cur['radio-mac']) : null;
    if (owner) ifaceCap.set(name, owner);
  }

  let clientsOnCaps = 0, clientsLocal = 0;
  for (const r of regRows || []) {
    if (!r || !r['mac-address']) continue;
    const iface  = r.interface || '';
    const owner  = ifaceCap.get(iface) || null;
    const client = {
      mac:       String(r['mac-address']).toUpperCase(),
      interface: iface,
      ssid:      r.ssid || '',
      signal:    r.signal !== undefined && r.signal !== '' ? Number(r.signal) : null,
      uptime:    r.uptime || '',
    };
    if (owner) {
      owner.clientCount++;
      if (owner.clients.length < CLIENTS_PER_CAP) owner.clients.push(client);
      clientsOnCaps++;
    } else {
      clientsLocal++;
    }
  }
  for (const c of caps) {
    c.radios.sort((a, b) => a.interface.localeCompare(b.interface));
    c.clients.sort((a, b) => (b.signal === null ? -Infinity : b.signal) -
                             (a.signal === null ? -Infinity : a.signal));
  }
  caps.sort((a, b) => a.identity.localeCompare(b.identity));

  const provisioning = [];
  for (const r of provRows || []) {
    if (!r || r.action === undefined) continue;
    provisioning.push({
      // The row's own id, and an identity built the way resources.js builds a
      // composite one. A provisioning rule has no name and nothing unique about
      // it, so the edit dialog ADDRESSES it by `.id` and IDENTIFIES it by this
      // tuple — an id survives an edit, which makes it the wrong thing to
      // recognise a row by. Both are needed before a row can carry
      // data-id / data-identity.
      id:                  r['.id'] || '',
      // The separator is U+0001 and the field ORDER is capsProvisioning's
      // `identity` array — both must match resources.js identityOf() exactly or
      // every edit is refused as a stale row. Mirrored rather than imported, the
      // way app.js mirrors fwIdentity() for the firewall, and pinned by a test
      // that compares the two for the same row.
      identity:            [r['supported-bands'] || '', r.action || '',
                            r['master-configuration'] || '', r['name-format'] || ''].join(''),
      supportedBands:      _split(r['supported-bands']),
      action:              r.action || '',
      masterConfiguration: r['master-configuration'] || '',
      slaveConfigurations: _split(r['slave-configurations']),
      nameFormat:          r['name-format'] || '',
      radioMac:            r['radio-mac'] || '',
      identityRegexp:      r['identity-regexp'] || '',
      comment:             r.comment || '',
      disabled:            _bool(r.disabled),
    });
  }

  return {
    role, manager, cap: capMode,
    caps, provisioning, localRadios,
    totals: {
      caps:         caps.length,
      capsOk:       caps.filter(c => /^ok$/i.test(c.state)).length,
      radios:       localRadios.length + caps.reduce((n, c) => n + c.radios.length, 0),
      clients:      clientsOnCaps + clientsLocal,
      clientsOnCaps,
      clientsLocal,
    },
  };
}

class CapsmanCollector {
  constructor({ ros, io, state, pollMs, streamMode }) {
    this.ros    = ros;
    this.io     = io;
    this.state  = state;
    this.pollMs = clampPoll(pollMs, 10000, 60000, 2000);
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][capsman]` : '[capsman]';

    // Delivery switch (#105). Stream mode shows a CAP joining or dropping the
    // moment it happens rather than up to one interval later, and re-reads the
    // manager, CAP and provisioning tables only when something changed. The
    // radio, interface and registration tables are read every tick either way —
    // clients roam with no CAP event behind it. Poll mode holds no channel.
    this.streamMode = streamMode !== false;
    this._poll    = createPollLoop(() => this._tick(), () => this.pollMs);
    this._listen  = createListenRefresh({
      ros, cmd: LISTEN_CMD, label: this._lbl,
      onEvent: () => { this._dirty = true; this._tick().catch(() => {}); },
    });
    this._dirty   = true;
    this._manager = null;
    this._cap     = null;
    this._prov    = [];
    this._profiles = { configuration: [], security: [], channel: [], datapath: [] };
    this._ticks   = 0;
    this._lastFp  = '';
    // undefined = unprobed, false = this router has no such menu, stop asking.
    this._managerAvailable = undefined;
    this._capAvailable     = undefined;
    this._remoteAvailable  = undefined;
    this._provAvailable    = undefined;
    this._radioAvailable   = undefined;
    this._ifaceAvailable   = undefined;
    this._regAvailable     = undefined;
    this.lastPayload = null;
  }

  async _read(cmd, flag) {
    if (this[flag] === false) return [];
    try {
      const args = cmd[1] ? [cmd[1]] : [];
      const rows = await this.ros.write(cmd[0], args);
      this[flag] = true;
      return (rows || []).filter(r => r && Object.keys(r).length);
    } catch (e) {
      const msg = String((e && e.message) || e).toLowerCase();
      // A router on the legacy wireless package has none of these menus. Ask
      // once, then stop — the same latch vpn.js and ppp.js use.
      if (msg.includes('no such') || msg.includes('unknown command')) this[flag] = false;
      else if (msg.includes('not enough permissions') || msg.includes('permission denied')) this[flag] = false;
      else this.state.lastCapsmanErr = e && e.message ? e.message : String(e);
      return [];
    }
  }

  async _tick() {
    if (!this.ros.connected) return;

    if (!this.streamMode || this._dirty || this._ticks % CONFIG_EVERY === 0) {
      const [mgr, cap, prov, cfg, sec, chan, dpath] = await Promise.all([
        this._read(MANAGER_CMD, '_managerAvailable'),
        this._read(CAP_CMD,     '_capAvailable'),
        this._read(PROV_CMD,    '_provAvailable'),
        // The four profile menus behind the configuration card. Each latches
        // independently, so a build without one costs a tab rather than a page.
        this._read(MENUS.configuration, '_configAvailable'),
        this._read(MENUS.security,      '_securityAvailable'),
        this._read(MENUS.channel,       '_channelAvailable'),
        this._read(MENUS.datapath,      '_datapathAvailable'),
      ]);
      this._manager = mgr[0] || null;
      this._cap     = cap[0] || null;
      this._prov    = prov;
      // named() drops the nameless junk row an empty RouterOS menu answers with.
      this._profiles = {
        configuration: named(cfg),
        security:      named(sec),
        channel:       named(chan),
        datapath:      named(dpath),
      };
      this._dirty   = false;
    }
    this._ticks++;

    const [remoteRows, radioRows, ifaceRows, regRows] = await Promise.all([
      this._read(REMOTE_CMD, '_remoteAvailable'),
      this._read(RADIO_CMD,  '_radioAvailable'),
      this._read(IFACE_CMD,  '_ifaceAvailable'),
      this._read(REG_CMD,    '_regAvailable'),
    ]);

    const built = buildCapsmanView(this._manager, this._cap, remoteRows, this._prov,
                                   radioRows, ifaceRows, regRows);
    // The rows behind the configuration card's five tabs. Projected by name
    // rather than spread, so a proplist widened later cannot silently push a new
    // field — a passphrase included — at every browser on the page.
    const profiles = {
      configuration: (this._profiles.configuration || []).map(r => ({
        id: r['.id'] || '', name: r.name || '', ssid: r.ssid || '', mode: r.mode || '',
        country: r.country || '', hideSsid: _bool(r['hide-ssid']),
        security: r.security || '', channel: r.channel || '', datapath: r.datapath || '',
        manager: r.manager || '', comment: r.comment || '', disabled: _bool(r.disabled),
      })),
      security: (this._profiles.security || []).map(r => ({
        id: r['.id'] || '', name: r.name || '',
        authTypes: r['authentication-types'] || '', wps: r.wps || '',
        ft: _bool(r.ft), comment: r.comment || '', disabled: _bool(r.disabled),
      })),
      channel: (this._profiles.channel || []).map(r => ({
        id: r['.id'] || '', name: r.name || '', band: r.band || '',
        frequency: r.frequency || '', width: r.width || '',
        secondaryFrequency: r['secondary-frequency'] || '',
        skipDfsChannels: r['skip-dfs-channels'] || '',
        comment: r.comment || '', disabled: _bool(r.disabled),
      })),
      datapath: (this._profiles.datapath || []).map(r => ({
        id: r['.id'] || '', name: r.name || '', bridge: r.bridge || '',
        vlanId: r['vlan-id'] || '', clientIsolation: _bool(r['client-isolation']),
        localForwarding: _bool(r['local-forwarding']),
        trafficProcessing: r['traffic-processing'] || '',
        comment: r.comment || '', disabled: _bool(r.disabled),
      })),
    };

    const payload = {
      ts: Date.now(), pollMs: this.pollMs,
      ...built,
      profiles,
      // False on a router running the legacy wireless package, so the page can
      // say so instead of rendering an empty manager panel.
      available: this._managerAvailable !== false || this._capAvailable !== false,
    };
    this.lastPayload = payload;
    this.state.lastCapsmanTs = payload.ts;

    const fp = JSON.stringify({
      r: built.role,
      m: [built.manager.enabled, built.manager.interfaces, built.cap.enabled, built.cap.currentIdentity],
      c: built.caps.map(c => [c.identity, c.state, c.version, c.connectedTime, c.clientCount,
                              c.radios.map(x => x.interface)]),
      // EVERY field the configuration card can edit belongs here. A field left
      // out means a save that lands on the router and never reaches the browser,
      // which reads as a failed write — `comment` and `slaveConfigurations` were
      // exactly that before the card existed.
      p: built.provisioning.map(p => [p.id, p.supportedBands, p.action, p.masterConfiguration,
                                      p.slaveConfigurations, p.nameFormat, p.radioMac,
                                      p.identityRegexp, p.comment, p.disabled]),
      f: [profiles.configuration, profiles.security, profiles.channel, profiles.datapath],
      t: built.totals,
    });
    if (fp === this._lastFp) return;
    this._lastFp = fp;
    this.io.to('page-capsman').emit('capsman:update', payload);
  }

  /**
   * Re-read now, after a write, so the card shows what the router did.
   *
   * Every res:* handler calls this through resource.collector — but only if it
   * exists, so its absence was silent: a save landed on the router and the card
   * sat still until the next config tick, up to two minutes at the default
   * interval. `_lastFp` is cleared as well as `_dirty` set, because a write that
   * happens to restore a previous value would otherwise fingerprint identical
   * and be swallowed.
   */
  async refreshNow() {
    if (!this.ros.connected) return;
    this._dirty  = true;
    this._lastFp = '';
    await this._tick();
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
      this._lastFp = '';
      this._ticks  = 0;
      this._dirty  = true;
      this._manager = this._cap = null;
      this._prov = [];
      this._profiles = { configuration: [], security: [], channel: [], datapath: [] };
      this._managerAvailable = this._capAvailable = this._remoteAvailable = undefined;
      this._provAvailable = this._radioAvailable = undefined;
      this._ifaceAvailable = this._regAvailable = undefined;
      // A package can be installed and the router rebooted under us, so the four
      // profile menus are re-probed rather than carried across a reconnect.
      this._configAvailable = this._securityAvailable = undefined;
      this._channelAvailable = this._datapathAvailable = undefined;
      await this._tick();
      this._startDelivery();
    });
  }

  suspend() { this._stopDelivery(); }
  resume()  { if (this.ros.connected) this._startDelivery(); }

  stop() { this._stopDelivery(); this._lastFp = ''; }
}

CapsmanCollector.buildCapsmanView = buildCapsmanView;
CapsmanCollector.parseCapField    = parseCapField;
CapsmanCollector.CLIENTS_PER_CAP  = CLIENTS_PER_CAP;
module.exports = CapsmanCollector;
