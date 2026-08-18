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
 * WHAT IS DELIBERATELY NOT READ. /interface/wifi/configuration holds
 * `security.passphrase` in clear text — wireless.js:229-265 already refuses to
 * read that row for the same reason. Provisioning references configurations by
 * NAME, which is all this page needs, so the configuration table is never
 * fetched and no passphrase can reach the browser.
 */

const { clampPoll, createPollLoop, createListenRefresh } = require('./util');

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
      const [mgr, cap, prov] = await Promise.all([
        this._read(MANAGER_CMD, '_managerAvailable'),
        this._read(CAP_CMD,     '_capAvailable'),
        this._read(PROV_CMD,    '_provAvailable'),
      ]);
      this._manager = mgr[0] || null;
      this._cap     = cap[0] || null;
      this._prov    = prov;
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
    const payload = {
      ts: Date.now(), pollMs: this.pollMs,
      ...built,
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
      p: built.provisioning.map(p => [p.action, p.masterConfiguration, p.nameFormat, p.disabled]),
      t: built.totals,
    });
    if (fp === this._lastFp) return;
    this._lastFp = fp;
    this.io.to('page-capsman').emit('capsman:update', payload);
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
      this._managerAvailable = this._capAvailable = this._remoteAvailable = undefined;
      this._provAvailable = this._radioAvailable = undefined;
      this._ifaceAvailable = this._regAvailable = undefined;
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
