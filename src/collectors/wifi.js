'use strict';
/**
 * Wifi Networks collector — the CONFIGURATION side of wireless.
 *
 * `wireless.js` answers "who is connected"; this one answers "what is this
 * router set up to broadcast". They are deliberately separate collectors on
 * separate pages: the client table changes every few seconds and the
 * configuration changes when a human edits it, so folding them together would
 * mean re-reading config on a client-table cadence forever.
 *
 * TWO STACKS, ONE VIEW. RouterOS has two incompatible wireless trees:
 *
 *   modern  /interface/wifi/*      wifi-qcom / wifi-qcom-ac (was `wifiwave2`)
 *   legacy  /interface/wireless/*  everything before it
 *
 * The stack is latched the way wireless.js latches it — try modern, fall back
 * on "no such command", and allow the fallback to run in both directions so a
 * modern board whose radios are all disabled does not get stuck on legacy
 * because its first read came back empty.
 *
 * SECRETS NEVER LEAVE THIS FILE, AND ARE NEVER EVEN ASKED FOR.
 * `/interface/wifi/print` returns `security.passphrase` in clear text, and
 * `/interface/wireless/security-profiles/print` returns the pre-shared key the
 * same way. This payload goes to every browser on the page, so every read here
 * is proplist-scoped and no proplist names a passphrase field. The page shows a
 * security MODE; the value itself only ever travels the other way, as a
 * `secret`-typed field in src/routeros/resources.js, which strips it on the way
 * back out (see rowValues there).
 *
 * INHERITANCE. A modern interface can take its SSID and security from a shared
 * `/interface/wifi/configuration` profile instead of carrying them inline, and
 * `print` reports the inherited value indistinguishably from a local one. The
 * CLI tells them apart with `print detail config`, which has no dependable
 * binary-API equivalent — so this compares the interface against the profile it
 * names instead. Equal means inherited; different means somebody has already
 * overridden it locally. It fails toward "not inherited", which suppresses a
 * warning rather than blocking a write.
 */

const { clampPoll, createPollLoop, createListenRefresh } = require('./util');
// The profile menus are read by capsman.js too, and they are the ones carrying
// the passphrase. One definition of the safe proplist, so the two callers
// cannot drift apart — see the header of that file.
const { MENUS } = require('../routeros/wifiMenus');

// ── Command registries, one per stack ────────────────────────────────────────
//
// Note what is absent from every proplist below: security.passphrase,
// wpa-pre-shared-key, wpa2-pre-shared-key. That absence is the security
// property — see the header. Adding a field here puts it in front of every
// browser holding read on this page.

const WIFI_CMDS = {
  ifaces: ['/interface/wifi/print',
    '=.proplist=.id,name,default-name,disabled,running,master-interface,radio-mac,mac-address,' +
    'configuration,configuration.ssid,configuration.mode,configuration.hide-ssid,' +
    'configuration.country,configuration.manager,security,security.authentication-types,' +
    'channel,channel.band,channel.frequency,channel.width,datapath,datapath.bridge,' +
    'datapath.vlan-id,comment,dynamic'],
  configs:  MENUS.configuration,
  security: MENUS.security,
  channels: MENUS.channel,
  radios: ['/interface/wifi/radio/print',
    '=.proplist=radio-mac,interface,cap,disabled'],
  reg: ['/interface/wifi/registration-table/print', '=.proplist=interface,ssid'],
  listen: '/interface/wifi/listen',
};

const WL_CMDS = {
  ifaces: ['/interface/wireless/print',
    '=.proplist=.id,name,default-name,disabled,running,ssid,mode,band,frequency,channel-width,' +
    'security-profile,master-interface,hide-ssid,vlan-id,vlan-mode,mac-address,comment,dynamic'],
  profiles: ['/interface/wireless/security-profiles/print',
    '=.proplist=.id,name,mode,authentication-types,default'],
  reg: ['/interface/wireless/registration-table/print', '=.proplist=interface,ssid'],
  listen: '/interface/wireless/listen',
};

/** The router saying "that menu is not on this build". */
const _absent = (e) => /no such command|unknown command|not supported/i.test(String((e && e.message) || e));

const _bool = (v) => v === true || v === 'true';

// Re-read on this multiple of the emit tick when streaming. Wireless config
// changes when somebody edits the router, so the /listen channel does the real
// work and this is the safety net for an event that never arrived.
const CONFIG_EVERY = 10;

/**
 * "2.4 GHz" / "5 GHz" / "6 GHz" from whatever the stack calls a band.
 *
 * Modern bands read `2ghz-ax`, `5ghz-ac`, `6ghz-ax`; legacy ones read
 * `2ghz-b/g/n` or `5ghz-a/n/ac`. Both start with the number, which is the only
 * part worth putting in a table column.
 */
function bandLabel(raw) {
  const s = String(raw || '').toLowerCase();
  if (s.startsWith('6ghz') || s.startsWith('6g')) return '6GHz';
  if (s.startsWith('5ghz') || s.startsWith('5g')) return '5GHz';
  if (s.startsWith('2ghz') || s.startsWith('2g') || s.startsWith('2.4')) return '2.4GHz';
  return '';
}

/**
 * A human name for an authentication-types list.
 *
 * Empty means open, which is worth saying loudly rather than leaving blank —
 * an open SSID nobody noticed is the failure this column exists to catch.
 */
function securityLabel(authTypes) {
  const s = String(authTypes || '').trim().toLowerCase();
  if (!s) return 'Open';
  const has = (t) => s.includes(t);
  const parts = [];
  if (has('wpa3-psk') || has('wpa3-eap')) parts.push('WPA3');
  if (has('wpa2-psk') || has('wpa2-eap')) parts.push('WPA2');
  if (has('wpa-psk')  || has('wpa-eap'))  parts.push('WPA');
  if (has('owe')) parts.push('OWE');
  if (!parts.length) return s.toUpperCase();
  const enterprise = has('eap');
  return parts.join('/') + (enterprise ? ' Enterprise' : '');
}

/**
 * The band a frequency implies, for a radio that names no band of its own.
 *
 * Real routers frequently set neither: the band lives on a channel profile, or
 * is left for RouterOS to infer. An empty Band column on every row is useless,
 * so this is the second guess after the profile.
 *
 * The three strings are spelled exactly as wireless.js spells them, because the
 * Wifi Clients page's band pill keys off them and both pages should say the
 * same word for the same thing.
 */
function bandFromFrequency(freq) {
  const first = parseInt(String(freq || '').split(/[-,]/)[0], 10);
  if (!Number.isFinite(first)) return '';
  if (first >= 5925) return '6GHz';
  if (first >= 4900) return '5GHz';
  if (first >= 2400) return '2.4GHz';
  return '';
}

/**
 * Last resort: the band an interface NAME advertises.
 *
 * Used only when neither the interface, its channel profile, nor a frequency
 * says anything — which is the common case on a 2.4 GHz radio left on auto.
 * wireless.js already infers CAPsMAN bands from interface names for the same
 * reason; operators name these things after the band far more reliably than
 * they set the property.
 */
function bandFromName(name) {
  const s = String(name || '').toLowerCase();
  if (/6\s*ghz|-6g\b/.test(s)) return '6GHz';
  if (/5\s*ghz|-5g\b/.test(s)) return '5GHz';
  if (/2\.4\s*ghz|2\s*ghz|-2g\b/.test(s)) return '2.4GHz';
  return '';
}

/** Clients per interface name, from a registration table read. */
function countByInterface(regRows) {
  const counts = new Map();
  for (const r of regRows || []) {
    const iface = String((r && r.interface) || '').trim();
    if (!iface) continue;
    counts.set(iface, (counts.get(iface) || 0) + 1);
  }
  return counts;
}

/**
 * Build the modern view.
 *
 * `configs` is keyed by profile NAME because that is what an interface's
 * `configuration` field holds — the profile's own `.id` never appears on the
 * interface row.
 */
function buildWifiView({ ifaces, configs, security, channels, reg }) {
  // An EMPTY RouterOS menu answers with one junk row — `[{"undefined":""}]` is
  // what /interface/wifi/channel/print returns on a router with no channel
  // profiles. Keyed by name it would become an entry under '', which is exactly
  // the value an interface naming no profile has.
  const named = (rows) => new Map((rows || [])
    .filter(r => r && String(r.name || '').trim())
    .map(r => [String(r.name), r]));

  const byConfigName = named(configs);
  const bySecName    = named(security);
  const byChanName   = named(channels);
  const counts       = countByInterface(reg);

  // How many interfaces lean on each configuration profile. An override on a
  // profile only one interface uses splits nothing; on a shared one it splits
  // two things that currently move together, and that is the case worth a
  // warning. Counted here so the guard does not have to re-read the router.
  const profileUsedBy = new Map();
  for (const r of ifaces || []) {
    const p = String(r.configuration || '');
    if (p) profileUsedBy.set(p, (profileUsedBy.get(p) || 0) + 1);
  }

  const networks = [];
  const radios   = [];

  for (const r of ifaces || []) {
    const name   = String(r.name || '');
    const master = String(r['master-interface'] || '');
    const profileName = String(r.configuration || '');
    const profile = profileName ? byConfigName.get(profileName) : null;

    const ssid      = String(r['configuration.ssid'] || '');
    const authTypes = String(r['security.authentication-types'] || '');

    // security and channel can be pulled in through the configuration profile
    // as well as named directly on the interface; either way the sub-profile is
    // the thing an override would shadow.
    const secProfile  = String(r.security || (profile && profile.security) || '');
    const chanProfile = String(r.channel  || (profile && profile.channel)  || '');
    const chan        = chanProfile ? byChanName.get(chanProfile) : null;

    // Band, frequency and width are read through the channel profile as well as
    // off the interface. A real router very often sets none of them inline — on
    // a CAPsMAN-provisioned board every one of these came back empty, which put
    // an em dash in the Band column of every row on the page.
    const band  = String(r['channel.band']      || (chan && chan.band)      || '');
    const freq  = String(r['channel.frequency'] || (chan && chan.frequency) || '');
    const width = String(r['channel.width']     || (chan && chan.width)     || '');
    // Explicit band first, then what the frequency implies, then the name.
    const bandText = bandLabel(band) || bandFromFrequency(freq) || bandFromName(name);

    // Inherited iff the interface names a profile, that profile defines the
    // field, and the effective value still equals the profile's. See the header
    // for why this is a comparison rather than a `print detail config`.
    const inheritedFrom = (key, effective) => {
      if (!profile) return null;
      const fromProfile = String(profile[key] == null ? '' : profile[key]);
      if (!fromProfile) return null;
      return fromProfile === String(effective || '') ? profileName : null;
    };

    const inherits = {
      ssid:     inheritedFrom('ssid', ssid),
      security: (secProfile  && bySecName.has(secProfile))   ? secProfile  : null,
      channel:  (chanProfile && byChanName.has(chanProfile)) ? chanProfile : null,
    };
    const anyInherited = !!(inherits.ssid || inherits.security || inherits.channel);

    const capsManaged = !!String(r['configuration.manager'] || '');
    const isVirtual   = !!master;
    const dynamic     = _bool(r.dynamic);

    // WHY a row is read-only, not just that it is. A router running CAPsMAN
    // against its own radios reports them dynamic with no `configuration.manager`
    // at all, so keying the badge on manager alone left the AX3 showing twelve
    // uneditable rows and no explanation for any of them. The edit that would
    // work is on the provisioning profile, which is the CAPsMAN page's job.
    const readOnlyReason = capsManaged ? 'caps'
                         : dynamic     ? 'provisioned'
                         : null;

    networks.push({
      id: String(r['.id'] || ''),
      name,
      ssid,
      radio: master || name,
      master,
      isVirtual,
      band: bandText,
      bandRaw: band,
      security: securityLabel(authTypes),
      authTypes,
      hidden: _bool(r['configuration.hide-ssid']),
      vlanId: String(r['datapath.vlan-id'] || ''),
      bridge: String(r['datapath.bridge'] || ''),
      disabled: _bool(r.disabled),
      running: _bool(r.running),
      clients: counts.get(name) || 0,
      comment: String(r.comment || ''),
      capsManaged,
      profile: profileName,
      profileUsedBy: profileName ? (profileUsedBy.get(profileName) || 0) : 0,
      inherits: anyInherited ? inherits : null,
      readOnlyReason,
      // A CAP takes its configuration from the manager, so a local edit is a
      // no-op; a dynamic interface is not ours to edit at all.
      editable: !readOnlyReason,
      // Only a virtual AP may be removed. A physical radio is hardware.
      removable: isVirtual && !readOnlyReason,
      resource: 'wifiNet',
    });

    if (!isVirtual) {
      radios.push({
        name,
        defaultName: String(r['default-name'] || ''),
        mac: String(r['radio-mac'] || r['mac-address'] || ''),
        band: bandText,
        bandRaw: band,
        frequency: freq,
        channelWidth: width,
        country: String(r['configuration.country'] || ''),
        disabled: _bool(r.disabled),
        running: _bool(r.running),
        capsManaged,
        readOnlyReason,
        profile: profileName,
      });
    }
  }

  return { networks, radios };
}

/**
 * Build the legacy view.
 *
 * The shape is identical to the modern one so the page renders one table. What
 * differs is where security lives: on legacy the interface names a profile and
 * the profile holds the authentication types (and, invisibly to us, the key).
 */
function buildWirelessView({ ifaces, profiles, reg }) {
  // See buildWifiView: an empty menu answers with one nameless junk row.
  const byProfileName = new Map((profiles || [])
    .filter(p => p && String(p.name || '').trim())
    .map(p => [String(p.name), p]));
  const counts = countByInterface(reg);

  const networks = [];
  const radios   = [];

  for (const r of ifaces || []) {
    const name    = String(r.name || '');
    const master  = String(r['master-interface'] || '');
    const profile = String(r['security-profile'] || '');
    const prof    = profile ? byProfileName.get(profile) : null;
    const authTypes = String((prof && prof['authentication-types']) || '');
    const band    = String(r.band || '');
    const isVirtual = !!master;
    // A CAPsMAN-provisioned legacy interface arrives as dynamic; editing it
    // locally is meaningless for the same reason a CAP's is.
    const dynamic = _bool(r.dynamic);

    networks.push({
      id: String(r['.id'] || ''),
      name,
      ssid: String(r.ssid || ''),
      radio: master || name,
      master,
      isVirtual,
      band: bandLabel(band) || bandFromFrequency(r.frequency) || bandFromName(name),
      bandRaw: band,
      // A profile in `none` mode has no authentication types at all, which is
      // an open network however the profile is named.
      security: (prof && String(prof.mode || '') === 'none') ? 'Open' : securityLabel(authTypes),
      authTypes,
      hidden: _bool(r['hide-ssid']),
      vlanId: String(r['vlan-id'] || ''),
      bridge: '',
      disabled: _bool(r.disabled),
      running: _bool(r.running),
      clients: counts.get(name) || 0,
      comment: String(r.comment || ''),
      capsManaged: dynamic,
      profile,
      profileUsedBy: 0,
      inherits: null,
      // The legacy stack has no local-manager field, so a provisioned interface
      // is only ever recognisable by being dynamic.
      readOnlyReason: dynamic ? 'provisioned' : null,
      editable: !dynamic,
      removable: isVirtual && !dynamic,
      resource: 'wlNet',
    });

    if (!isVirtual) {
      radios.push({
        name,
        defaultName: String(r['default-name'] || ''),
        mac: String(r['mac-address'] || ''),
        band: bandLabel(band) || bandFromFrequency(r.frequency) || bandFromName(name),
        bandRaw: band,
        frequency: String(r.frequency || ''),
        channelWidth: String(r['channel-width'] || ''),
        country: '',
        disabled: _bool(r.disabled),
        running: _bool(r.running),
        capsManaged: dynamic,
        readOnlyReason: dynamic ? 'provisioned' : null,
        profile,
      });
    }
  }

  const secProfiles = (profiles || []).filter(p => p && String(p.name || '').trim()).map(p => ({
    id: String(p['.id'] || ''),
    name: String(p.name || ''),
    mode: String(p.mode || ''),
    authTypes: String(p['authentication-types'] || ''),
    security: securityLabel(p['authentication-types']),
    isDefault: _bool(p.default),
  }));

  return { networks, radios, secProfiles };
}

/** Sort so each radio's own row leads, with its virtual APs beneath it. */
function sortNetworks(networks) {
  return [...networks].sort((a, b) =>
    a.radio.localeCompare(b.radio) ||
    (a.isVirtual ? 1 : 0) - (b.isVirtual ? 1 : 0) ||
    a.name.localeCompare(b.name));
}

class WifiCollector {
  constructor({ ros, io, state, pollMs, streamMode }) {
    this.ros    = ros;
    this.io     = io;
    this.state  = state;
    this.pollMs = clampPoll(pollMs, 10000, 600000, 30000);
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][wifi]` : '[wifi]';

    this.streamMode = streamMode !== false;

    // undefined = unprobed, 'wifi' | 'wireless' = latched, 'none' = neither
    // menu exists on this build. Reset on reconnect, because a package can be
    // installed and the router rebooted under us.
    this.stack = undefined;

    this._poll   = createPollLoop(() => this._tick(), () => this.pollMs);
    this._listen = null;
    this._dirty  = true;
    this._ticks  = 0;
    this._lastFp = '';
    this._view   = { networks: [], radios: [], secProfiles: [] };
    this.lastPayload = null;
  }

  // ── Reads ──────────────────────────────────────────────────────────────────

  async _readWifi() {
    const [ifaces, configs, security, channels, reg] = await Promise.all([
      this.ros.write(WIFI_CMDS.ifaces[0],   [WIFI_CMDS.ifaces[1]]),
      // Everything after the interface list is enrichment. A build without a
      // menu, or an API user who cannot see it, costs a badge — never the page.
      this.ros.write(WIFI_CMDS.configs[0],  [WIFI_CMDS.configs[1]]).catch(() => []),
      this.ros.write(WIFI_CMDS.security[0], [WIFI_CMDS.security[1]]).catch(() => []),
      this.ros.write(WIFI_CMDS.channels[0], [WIFI_CMDS.channels[1]]).catch(() => []),
      this.ros.write(WIFI_CMDS.reg[0],      [WIFI_CMDS.reg[1]]).catch(() => []),
    ]);
    return buildWifiView({
      ifaces: ifaces || [], configs: configs || [], security: security || [],
      channels: channels || [], reg: reg || [],
    });
  }

  async _readWireless() {
    const [ifaces, profiles, reg] = await Promise.all([
      this.ros.write(WL_CMDS.ifaces[0],   [WL_CMDS.ifaces[1]]),
      this.ros.write(WL_CMDS.profiles[0], [WL_CMDS.profiles[1]]).catch(() => []),
      this.ros.write(WL_CMDS.reg[0],      [WL_CMDS.reg[1]]).catch(() => []),
    ]);
    return buildWirelessView({
      ifaces: ifaces || [], profiles: profiles || [], reg: reg || [],
    });
  }

  /**
   * Read whichever stack this router has, latching on the way.
   *
   * The fallback runs in BOTH directions. wireless.js learned this the hard
   * way: a modern board whose radios are all disabled returns an empty
   * `/interface/wifi/print` rather than an error, which looks exactly like a
   * legacy router until something asks the legacy menu and gets refused.
   */
  async _load() {
    const order = this.stack === 'wireless' ? ['wireless', 'wifi'] : ['wifi', 'wireless'];
    let lastErr = null;

    for (const which of order) {
      try {
        const view = which === 'wifi' ? await this._readWifi() : await this._readWireless();
        // An empty answer from a menu that exists is a real answer — a router
        // can genuinely have no wireless configured. Only latch on it when the
        // other stack has not been tried yet.
        if (!view.networks.length && which === order[0]) { lastErr = null; continue; }
        this.stack = which;
        this._view = { secProfiles: [], ...view };
        this.state.lastWifiErr = null;
        return;
      } catch (e) {
        if (!_absent(e)) { lastErr = e; break; }
        lastErr = e;
      }
    }

    // Both menus refused, or the first was empty and the second refused. The
    // first case is a router with no wireless at all; keep serving an empty
    // view rather than an error, because that is what it is.
    if (lastErr && !_absent(lastErr)) {
      this.state.lastWifiErr = lastErr.message ? lastErr.message : String(lastErr);
      return;
    }
    this.stack = this.stack || 'none';
    this._view = { networks: [], radios: [], secProfiles: [] };
    this.state.lastWifiErr = null;
  }

  // ── Emit ───────────────────────────────────────────────────────────────────

  _emit() {
    const networks = sortNetworks(this._view.networks || []);
    const radios   = this._view.radios || [];
    const payload = {
      ts: Date.now(),
      pollMs: this.pollMs,
      stack: this.stack || 'none',
      available: this.stack === 'wifi' || this.stack === 'wireless',
      radios,
      networks,
      secProfiles: this._view.secProfiles || [],
      totals: {
        radios: radios.length,
        networks: networks.length,
        clients: networks.reduce((n, x) => n + x.clients, 0),
        capsManaged: networks.filter(x => x.capsManaged).length,
        // Counted separately from capsManaged: a router provisioning its own
        // radios reports neither a manager nor an editable row, and the page
        // has to be able to say which of the two it is looking at.
        readOnly: networks.filter(x => !!x.readOnlyReason).length,
      },
    };

    // Assigned unconditionally: sendInitialState replays it, so a socket that
    // connects during a quiet spell must still get the current view. Only the
    // emit is fingerprint-gated.
    this.lastPayload = payload;
    this.state.lastWifiTs = payload.ts;

    const fp = JSON.stringify([
      payload.stack,
      networks.map(n => [n.id, n.name, n.ssid, n.band, n.security, n.vlanId,
                         n.disabled, n.running, n.hidden, n.clients, n.profile]),
      radios.map(r => [r.name, r.band, r.frequency, r.channelWidth, r.disabled, r.running]),
      payload.secProfiles.map(p => [p.id, p.name, p.mode, p.authTypes]),
    ]);
    if (fp === this._lastFp) return;
    this._lastFp = fp;
    this.io.to('page-wifi').emit('wifi:update', payload);
  }

  // ── Lifecycle ──────────────────────────────────────────────────────────────

  /**
   * Re-read now, after a write, so the page shows what the router did.
   *
   * Every res:* write handler calls this through resource.collector. Without
   * it a save lands on the router and the table sits still until the next poll,
   * which reads as a failed write.
   */
  async refreshNow() {
    if (!this.ros.connected) return;
    this._dirty = true;
    await this._tick();
  }

  async _tick() {
    if (!this.ros.connected) return;
    if (!this.streamMode || this._dirty || this._ticks % CONFIG_EVERY === 0) {
      await this._load();
      this._dirty = false;
      this._syncListen();
    }
    this._ticks++;
    this._emit();
  }

  /**
   * Point the /listen channel at the stack we actually latched.
   *
   * The command tree does not list `listen` under /interface/wifi, so this is
   * best-effort by construction: createListenRefresh logs and gives up if the
   * channel will not open, and the poll loop carries on regardless.
   */
  _syncListen() {
    if (!this.streamMode) return;
    const cmd = this.stack === 'wifi' ? WIFI_CMDS.listen
              : this.stack === 'wireless' ? WL_CMDS.listen : null;
    if (!cmd || (this._listen && this._listen.cmd === cmd)) return;
    if (this._listen) this._listen.stop();
    this._listen = createListenRefresh({
      ros: this.ros, cmd, label: this._lbl,
      onEvent: () => { this._dirty = true; this._tick().catch(() => {}); },
    });
    this._listen.cmd = cmd;
    this._listen.start();
  }

  _startDelivery() {
    this._poll.start();
    this._syncListen();
  }

  _stopDelivery() {
    this._poll.stop();
    if (this._listen) { this._listen.stop(); this._listen = null; }
  }

  async start() {
    if (this.ros.connected) {
      await this._load();
      this._dirty = false;
      this._ticks = 1;
      this._emit();
    }
    this._startDelivery();
    this.ros.on('close', () => this._stopDelivery());
    this.ros.on('connected', async () => {
      this._stopDelivery();
      this._lastFp = '';
      this._ticks  = 0;
      // A package can be installed and the router rebooted under us, so the
      // stack is re-probed rather than carried across a reconnect.
      this.stack = undefined;
      await this._load();
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

WifiCollector.bandLabel          = bandLabel;
WifiCollector.bandFromFrequency  = bandFromFrequency;
WifiCollector.bandFromName       = bandFromName;
WifiCollector.securityLabel      = securityLabel;
WifiCollector.countByInterface   = countByInterface;
WifiCollector.buildWifiView      = buildWifiView;
WifiCollector.buildWirelessView  = buildWirelessView;
WifiCollector.sortNetworks       = sortNetworks;
WifiCollector.WIFI_CMDS          = WIFI_CMDS;
WifiCollector.WL_CMDS            = WL_CMDS;
module.exports = WifiCollector;
