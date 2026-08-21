'use strict';
/**
 * The resource registry — what MikroDash is allowed to write to RouterOS.
 *
 * Four write surfaces were built by hand before this file existed (Queues,
 * Router Users, WAN lease actions, Packages) and each carries its own copy of
 * the same seven steps: check both gates, read fresh, match the row, validate,
 * build the sentence, write, audit, refresh. Queues alone is ~400 lines of
 * server handler and ~200 of browser form. Ten more pages that way is ~3,000
 * lines of near-identical plumbing, and every copy is a place a gate can be
 * forgotten.
 *
 * So a resource is a DESCRIPTION, and one set of handlers in index.js executes
 * every one of them. Adding a page's write access is an entry here.
 *
 * ── What a field's `type` buys ────────────────────────────────────────────
 *
 * Three things from one declaration:
 *
 *   1. the server-side validator, in `validate()`
 *   2. the input widget the browser renders, via `describe()`
 *   3. the ALLOW-LIST — `buildArgs()` can only ever emit `=<field.ros>=`, so a
 *      key this registry does not name cannot reach a RouterOS sentence
 *
 * Point 3 is issue #97's "never build an API sentence from raw user input",
 * enforced once rather than at ten call sites.
 *
 * Being precise about what that does and does not protect: the binary API
 * length-prefixes every word, so a `=` inside a VALUE cannot split it into a
 * second argument the way it could on a CLI. Validation here is not stopping
 * that. It is stopping an unnamed KEY from being set, and giving the operator a
 * sentence about their own input instead of a RouterOS error code.
 *
 * ── The browser gets this schema, not a copy of it ────────────────────────
 *
 * `describe()` is sent to the page, which builds its form from it. app.js
 * already carries five hand-maintained mirrors of server-side lists; ten more
 * would be ten more things to drift. There is no field list in app.js.
 *
 * ── Deliberately absent ───────────────────────────────────────────────────
 *
 * Firewall rules. Rule ORDER decides behaviour and reordering is not a field on
 * a form, and a bad input-chain rule locks MikroDash out of the router it
 * manages. That wants a guard of its own — see selfGuard.js for how much
 * thought one of those takes — and is its own change.
 */

const ipaddr = require('ipaddr.js');

// ── Value types ──────────────────────────────────────────────────────────────
//
// Each returns { ok: true, value } or { ok: false, message }. `value` is what
// reaches the sentence, so a type may normalise (bool → yes/no) but must never
// widen: anything it is unsure about is rejected, because the alternative is
// passing it to the router and hoping.

/** Control characters have no place in a RouterOS value and usually mean a paste went wrong. */
const _CTRL = /[\u0000-\u001f\u007f]/;

const TYPES = {
  text: {
    input: 'text',
    check(raw, f) {
      const s = String(raw == null ? '' : raw).trim();
      if (_CTRL.test(s)) return { ok: false, message: 'contains a control character' };
      const max = (f && f.max) || 255;
      if (s.length > max) return { ok: false, message: 'is longer than ' + max + ' characters' };
      return { ok: true, value: s };
    },
  },

  // Never rendered with a value and never echoed back — see the note on wgPeer
  // below. An empty submission means "leave unchanged", which is why this type
  // is skipped rather than cleared when blank.
  secret: {
    input: 'password',
    check(raw, f) { return TYPES.text.check(raw, f); },
  },

  cidr: {
    input: 'text',
    check(raw) {
      const s = String(raw == null ? '' : raw).trim();
      if (!s) return { ok: false, message: 'is required' };
      try {
        if (s.indexOf('/') !== -1) ipaddr.parseCIDR(s);
        else ipaddr.parse(s);
        return { ok: true, value: s };
      } catch (_) {
        return { ok: false, message: 'is not an address or prefix' };
      }
    },
  },

  ip: {
    input: 'text',
    check(raw) {
      const s = String(raw == null ? '' : raw).trim();
      if (!ipaddr.isValid(s)) return { ok: false, message: 'is not an IP address' };
      return { ok: true, value: s };
    },
  },

  mac: {
    input: 'text',
    check(raw) {
      const s = String(raw == null ? '' : raw).trim().toUpperCase();
      if (!/^([0-9A-F]{2}:){5}[0-9A-F]{2}$/.test(s))
        return { ok: false, message: 'is not a MAC address (AA:BB:CC:DD:EE:FF)' };
      return { ok: true, value: s };
    },
  },

  int: {
    input: 'number',
    check(raw, f) {
      const s = String(raw == null ? '' : raw).trim();
      if (!/^-?\d+$/.test(s)) return { ok: false, message: 'is not a whole number' };
      const n = Number(s);
      if (f && f.min !== undefined && n < f.min) return { ok: false, message: 'is below ' + f.min };
      if (f && f.max !== undefined && n > f.max) return { ok: false, message: 'is above ' + f.max };
      return { ok: true, value: String(n) };
    },
  },

  bool: {
    input: 'checkbox',
    check(raw) { return { ok: true, value: (raw === true || raw === 'true' || raw === 'yes') ? 'yes' : 'no' }; },
  },

  select: {
    input: 'select',
    check(raw, f) {
      const s = String(raw == null ? '' : raw).trim();
      if (((f && f.options) || []).indexOf(s) === -1)
        return { ok: false, message: 'is not one of the allowed values' };
      return { ok: true, value: s };
    },
  },

  // A WireGuard key is 32 bytes of base64. Checking the shape here means a
  // mistyped key is reported as a mistyped key rather than as whatever RouterOS
  // says when it rejects one.
  wgkey: {
    input: 'text',
    check(raw) {
      const s = String(raw == null ? '' : raw).trim();
      if (!/^[A-Za-z0-9+/]{42}[A-Za-z0-9+/]=$/.test(s))
        return { ok: false, message: 'is not a 44-character WireGuard key' };
      return { ok: true, value: s };
    },
  },
};

const TYPE_KEYS = Object.freeze(Object.keys(TYPES));

// ── Field helpers ────────────────────────────────────────────────────────────

/**
 * `clearable` means "send this even when it is empty, so the operator can empty
 * it". Without it an edit could never remove a comment: an omitted argument
 * leaves the router's value alone. Everything else is skipped when blank, so a
 * create does not set a pile of empty properties.
 */
const _f = (name, ros, label, type, extra) =>
  Object.assign({ name, ros, label, type }, extra || {});

/**
 * `optionsFrom` turns a free-text field into a picker.
 *
 * Two shapes:
 *
 *   { menu: '/ip/dhcp-server', value: 'name' }   read that menu, offer its `name` column
 *   { values: ['input', 'forward', 'output'] }   a fixed vocabulary, no read needed
 *
 * In BOTH cases the field's TYPE stays `text`, and that is the point. A firewall
 * action is a fixed vocabulary in practice but not in fact — RouterOS has more
 * actions than any list here will name, and versions add more. A `select` type
 * validates against its options, so a rule whose action is not in our list could
 * not be edited at all; a text field with suggestions renders the same widget
 * and still accepts what the router already holds. `selectHtml()` keeps a
 * current value that is not among the choices for exactly this reason.
 *
 * A menu read is allowed to fail. Denied, or absent on this RouterOS version,
 * simply yields no options and the field renders as the text box it always was.
 */
function optionSources(resource) {
  return resource.fields
    .filter(f => f.optionsFrom && f.optionsFrom.menu)
    .map(f => ({ field: f.name, menu: f.optionsFrom.menu, value: f.optionsFrom.value }));
}

/** The picker lists that need no router read, ready to ship as they are. */
function staticOptions(resource) {
  const out = {};
  for (const f of resource.fields)
    if (f.optionsFrom && f.optionsFrom.values) out[f.name] = f.optionsFrom.values.slice();
  return out;
}

/** Is this field in play, given what the operator has filled in so far? */
function fieldApplies(field, values) {
  const cond = field.showIf;
  if (!cond) return true;
  const v = (values || {})[cond.field];
  return (cond.in || []).indexOf(String(v == null ? '' : v)) !== -1;
}

// ── Firewall field groups ────────────────────────────────────────────────────
//
// The four firewall tables share most of a rule and differ in the interesting
// parts, so the shared parts are built rather than repeated four times. Three
// groups, not one, because ORDER is the form's layout: what the rule matches
// reads better between what it is and what it does about it.
//
// Each group returns fresh objects. The registry is frozen, but two tables
// pointing at one field object would make a later per-table tweak leak sideways.

const _fwHead = (chains, actions) => ([
  _f('chain',  'chain',  'Chain',  'text', { required: true, optionsFrom: { values: chains } }),
  _f('action', 'action', 'Action', 'text', { required: true, optionsFrom: { values: actions } }),
]);

const _fwMatch = () => ([
  _f('srcAddress', 'src-address', 'Source Address', 'text', { placeholder: '10.0.0.0/24' }),
  _f('dstAddress', 'dst-address', 'Destination Address', 'text'),
  _f('protocol', 'protocol', 'Protocol', 'text', {
    optionsFrom: { values: ['tcp', 'udp', 'icmp', 'ipv6-icmp', 'gre', 'ipsec-esp', 'ipsec-ah'] } }),
  _f('srcPort', 'src-port', 'Source Port', 'text'),
  // A port match is a list or a range as often as it is a number, so this is
  // text: `443`, `80,443` and `1000-2000` are all valid to RouterOS.
  _f('dstPort', 'dst-port', 'Destination Port', 'text', { placeholder: '443, or 1000-2000' }),
  _f('inInterface', 'in-interface', 'In Interface', 'text',
     { optionsFrom: { menu: '/interface', value: 'name' } }),
  _f('outInterface', 'out-interface', 'Out Interface', 'text',
     { optionsFrom: { menu: '/interface', value: 'name' } }),
]);

const _fwTail = () => ([
  _f('log', 'log', 'Log', 'bool', { clearable: true }),
  _f('logPrefix', 'log-prefix', 'Log Prefix', 'text'),
  _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
  _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
]);

/**
 * A firewall rule has no name and nothing unique about it, so the stale-row
 * check is a composite. See identityOf() for why that is enough.
 */
const _FW_IDENTITY = ['chain', 'action', 'srcAddress', 'dstAddress', 'comment'];

/**
 * Enable and disable, as row actions rather than as the `disabled` checkbox.
 *
 * The checkbox is still there in the form, but flipping a rule is the single
 * most common thing anyone does to a firewall and it should not require opening
 * one. RouterOS has verbs for it, so res:action already knows how to run them.
 */
const _FW_ACTIONS = [
  { key: 'enable',  verb: 'enable',  label: 'Enable',
    when: (r) => r.disabled === 'true', note: 'enabled a firewall rule' },
  { key: 'disable', verb: 'disable', label: 'Disable',
    when: (r) => r.disabled !== 'true', note: 'disabled a firewall rule' },
];

/** A rule some service added is not ours to edit. */
const _fwReadOnly = (r) => r.dynamic === 'true';

/**
 * RouterOS: "ports can be specified if proto is tcp,udp,udp-lite,dccp,sctp".
 *
 * A real constraint, and one somebody meets the first time they try to allow a
 * port — the obvious thing to fill in is the port, and the protocol is easy to
 * miss. Left to the router it comes back as a bare refusal with no clue which
 * field to fix, so it is checked here and reported against the field that is
 * actually missing.
 */
const _FW_PORT_PROTOS = ['tcp', 'udp', 'udp-lite', 'dccp', 'sctp'];

const _fwCheck = (clean) => {
  const ports = ['srcPort', 'dstPort'].filter(n => clean[n]);
  if (!ports.length) return [];
  const proto = String(clean.protocol || '').toLowerCase();
  if (_FW_PORT_PROTOS.includes(proto)) return [];
  return [{ field: 'protocol',
            message: 'Protocol must be one of ' + _FW_PORT_PROTOS.join(', ') +
                     ' before a port can be matched' }];
};

// ── Wireless ─────────────────────────────────────────────────────────────────
//
// Two stacks, two resources, one table. A router has EITHER /interface/wifi
// (modern: wifi-qcom, wifi-qcom-ac, formerly wifiwave2) or /interface/wireless
// (legacy), never both, so `requiresMenu` decides which Add button is real and
// the collector tags each row with the resource that owns it — a per-row
// `data-res`, the way the Routes table already mixes v4 and v6.
//
// Band, width and authentication-type vocabularies differ across drivers, so
// every one of them is `text` WITH SUGGESTIONS rather than a `select`. A hard
// select would refuse a value the router itself is perfectly happy with, and
// the router is the authority here, not this list.

/** Enable and disable as row actions, exactly as the firewall tables do. */
const _WIFI_ACTIONS = [
  { key: 'enable',  verb: 'enable',  label: 'Enable',
    when: (r) => r.disabled === 'true', note: 'enabled a wireless network' },
  { key: 'disable', verb: 'disable', label: 'Disable',
    when: (r) => r.disabled !== 'true', note: 'disabled a wireless network' },
];

/**
 * A WPA passphrase is 8..63 characters.
 *
 * Checked here rather than left to RouterOS because the router answers a short
 * key with a bare refusal naming no field, and "which box do I fix" is the whole
 * question at that moment.
 *
 * Length only, never presence: a blank passphrase means "leave the current one
 * alone" (see buildArgs), so requiring one would make every unrelated edit —
 * renaming an SSID, changing a VLAN — demand the passphrase be retyped.
 */
const _pskLength = (field) => (clean) => {
  const pass = String(clean[field] || '');
  if (!pass || (pass.length >= 8 && pass.length <= 63)) return [];
  return [{ field, message: 'Passphrase must be 8 to 63 characters' }];
};

/**
 * Only a virtual AP may be removed.
 *
 * A master radio is hardware: it can be edited and disabled, but deleting it is
 * not a thing RouterOS will do. `readOnlyWhen` cannot say this — it would block
 * the edit as well — so removal has a predicate of its own.
 */
const _wifiRemovable = (r) => !!r['master-interface'];

const _WIFI_NET = {
  key: 'wifiNet', page: 'wifi', collector: 'wifi', label: 'Wifi Network',
  title: 'Wifi Network', menu: '/interface/wifi', identity: 'name',
  requiresMenu: '/interface/wifi',
  // Two guards, two different questions. selfPath asks whether this cuts the
  // path we reach the router by; wifiInherit asks whether it quietly overrides
  // a profile more than one radio shares. See _resVerdict in src/index.js.
  guard: ['selfPath', 'wifiInherit'],
  guardInterfaceFields: ['name'],
  // Renaming and disabling are not the only disruptive edits here: changing the
  // SSID or the passphrase drops every client on the radio, the management path
  // included. _resGuardTargets counts a change to these as a target too.
  guardDisruptiveFields: ['ssid', 'passphrase', 'authTypes', 'band'],
  // A CAP takes its configuration from the manager, so a local edit is a no-op
  // that would look like a working save. A dynamic interface is not ours at all.
  readOnlyWhen: (r) => !!r['configuration.manager'] || r.dynamic === 'true',
  removableWhen: _wifiRemovable,
  actions: _WIFI_ACTIONS,
  check: _pskLength('passphrase'),
  fields: [
    _f('name', 'name', 'Interface Name', 'text', { required: true, placeholder: 'wifi1-guest' }),
    // Required, and that is what scopes Add to "another SSID on an existing
    // radio": with no way to omit it, the form cannot create a stray radio.
    _f('masterInterface', 'master-interface', 'Radio', 'text', { required: true,
      optionsFrom: { menu: '/interface/wifi', value: 'name' },
      help: 'the radio this SSID rides on' }),
    _f('ssid', 'configuration.ssid', 'SSID', 'text', { required: true, max: 32 }),
    _f('authTypes', 'security.authentication-types', 'Security', 'text',
      { optionsFrom: { values: ['', 'wpa2-psk', 'wpa3-psk', 'wpa2-psk,wpa3-psk',
                                'wpa2-eap', 'wpa3-eap', 'owe'] },
        help: 'blank is an open network' }),
    _f('passphrase', 'security.passphrase', 'Passphrase', 'secret',
      { max: 63, help: 'leave blank to keep the current passphrase' }),
    _f('hideSsid', 'configuration.hide-ssid', 'Hide SSID', 'bool', { clearable: true }),
    _f('band', 'channel.band', 'Band', 'text',
      { optionsFrom: { values: ['2ghz-ax', '2ghz-n', '5ghz-ax', '5ghz-ac', '6ghz-ax'] } }),
    _f('frequency', 'channel.frequency', 'Frequency', 'text', { placeholder: 'auto, or 5180' }),
    _f('width', 'channel.width', 'Channel Width', 'text',
      { optionsFrom: { values: ['20mhz', '20/40mhz', '20/40/80mhz', '20/40/80/160mhz'] } }),
    _f('country', 'configuration.country', 'Country', 'text'),
    // NOT clearable, unlike almost every other optional field in this registry.
    // `clearable` emits `=datapath.vlan-id=` on an edit, and RouterOS answers a
    // typed integer property given an empty string with "invalid value  for
    // datapath.vlan-id, an integer required" — so leaving it on made EVERY edit
    // of a wireless network fail, whether or not it touched the VLAN. Clearing
    // one needs /interface/wifi/unset, which this engine has no verb for; until
    // it does, an unset VLAN is one WinBox keeps.
    _f('vlanId', 'datapath.vlan-id', 'VLAN ID', 'int', { min: 1, max: 4094 }),
    _f('bridge', 'datapath.bridge', 'Bridge', 'text',
      { optionsFrom: { menu: '/interface/bridge', value: 'name' } }),
    _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
    _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
  ],
};

const _WL_NET = {
  key: 'wlNet', page: 'wifi', collector: 'wifi', label: 'Wifi Network',
  title: 'Wifi Network (legacy)', menu: '/interface/wireless', identity: 'name',
  requiresMenu: '/interface/wireless',
  // No wifiInherit here: the legacy stack has no configuration profiles to
  // inherit from. Security is a reference, not an inherited value, and changing
  // which profile an interface points at is an ordinary edit.
  guard: 'selfPath',
  guardInterfaceFields: ['name'],
  guardDisruptiveFields: ['ssid', 'securityProfile', 'band'],
  // A CAPsMAN-provisioned legacy interface arrives dynamic, and editing it
  // locally is meaningless for the same reason a CAP's is.
  readOnlyWhen: (r) => r.dynamic === 'true',
  removableWhen: _wifiRemovable,
  actions: _WIFI_ACTIONS,
  fields: [
    _f('name', 'name', 'Interface Name', 'text', { required: true, placeholder: 'wlan1-guest' }),
    _f('masterInterface', 'master-interface', 'Radio', 'text', { required: true,
      optionsFrom: { menu: '/interface/wireless', value: 'name' },
      help: 'the radio this SSID rides on' }),
    _f('ssid', 'ssid', 'SSID', 'text', { required: true, max: 32 }),
    // The passphrase is deliberately NOT here: on this stack it lives on the
    // profile, which is why wlSecProfile is a resource of its own.
    _f('securityProfile', 'security-profile', 'Security Profile', 'text',
      { optionsFrom: { menu: '/interface/wireless/security-profiles', value: 'name' },
        help: 'the passphrase lives on the profile, not here' }),
    _f('mode', 'mode', 'Mode', 'select',
      { options: ['ap-bridge', 'bridge', 'station', 'station-bridge', 'station-pseudobridge'] }),
    _f('hideSsid', 'hide-ssid', 'Hide SSID', 'bool', { clearable: true }),
    _f('band', 'band', 'Band', 'text',
      { optionsFrom: { values: ['2ghz-b/g/n', '2ghz-g/n', '2ghz-onlyn',
                                '5ghz-a/n/ac', '5ghz-onlyac', '5ghz-a/n'] } }),
    _f('frequency', 'frequency', 'Frequency', 'text', { placeholder: 'auto, or 5180' }),
    _f('channelWidth', 'channel-width', 'Channel Width', 'text',
      { optionsFrom: { values: ['20mhz', '20/40mhz-Ce', '20/40mhz-eC', '20/40/80mhz-Ceee'] } }),
    // Not clearable, for the reason given on wifiNet.vlanId above.
    _f('vlanId', 'vlan-id', 'VLAN ID', 'int', { min: 1, max: 4094 }),
    _f('vlanMode', 'vlan-mode', 'VLAN Mode', 'select',
      { options: ['no-tag', 'use-service-tag', 'use-tag'] }),
    _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
    _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
  ],
};

const _WL_SEC_PROFILE = {
  key: 'wlSecProfile', page: 'wifi', collector: 'wifi', label: 'Security Profile',
  title: 'Wifi Security Profile', menu: '/interface/wireless/security-profiles',
  identity: 'name', requiresMenu: '/interface/wireless/security-profiles',
  // Both keys are `secret`, so neither is read back into the form and neither
  // reaches the audit trail as a value: _resAuditValues keys on the declared
  // TYPE rather than the field name, which is what covers `wpa2PreSharedKey`
  // despite it not matching audit.js's credential name pattern.
  check: _pskLength('wpa2PreSharedKey'),
  fields: [
    _f('name', 'name', 'Name', 'text', { required: true, placeholder: 'guest-wpa2' }),
    _f('mode', 'mode', 'Mode', 'select',
      { options: ['none', 'static-keys-optional', 'static-keys-required', 'dynamic-keys'] }),
    _f('authenticationTypes', 'authentication-types', 'Authentication', 'text',
      { optionsFrom: { values: ['', 'wpa-psk', 'wpa2-psk', 'wpa-psk,wpa2-psk',
                                'wpa-eap', 'wpa2-eap'] } }),
    _f('wpa2PreSharedKey', 'wpa2-pre-shared-key', 'WPA2 Passphrase', 'secret',
      { max: 63, help: 'leave blank to keep the current passphrase' }),
    _f('wpaPreSharedKey', 'wpa-pre-shared-key', 'WPA Passphrase', 'secret',
      { max: 63, help: 'leave blank to keep the current passphrase' }),
    _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
  ],
};

// ── CAPsMAN ──────────────────────────────────────────────────────────────────
//
// The five menus that decide what a CAP gets provisioned with. They are ordinary
// list menus with `.id` rows, so they need no new machinery — but they differ
// from everything else here in BLAST RADIUS: a write lands on every CAP in the
// fleet the moment it is saved, which is what `capsmanPush` warns about.
//
// The two SINGLETON menus are deliberately absent. /interface/wifi/capsman and
// /interface/wifi/cap each answer with one row carrying NO `.id`, so this engine
// cannot address them at all — it reads rows by id and refuses anything else as
// a stale row. Enabling or disabling CAPsMAN itself stays in WinBox.
//
// PAGE SCOPE IS THE AUTHORISATION BOUNDARY, and the asymmetry is deliberate:
// these are `page: 'capsman'` while wifiNet is `page: 'wifi'`, so a role holding
// write on wifi but not capsman can override a value on ONE interface but cannot
// edit the shared profile every CAP follows. Smaller blast radius for the lesser
// grant. Do not "simplify" the two pages onto one key.

const _CAPS_ACTIONS = [
  { key: 'enable',  verb: 'enable',  label: 'Enable',
    when: (r) => r.disabled === 'true', note: 'enabled a CAPsMAN rule' },
  { key: 'disable', verb: 'disable', label: 'Disable',
    when: (r) => r.disabled !== 'true', note: 'disabled a CAPsMAN rule' },
];

const _CAPS_PROVISIONING = {
  key: 'capsProvisioning', page: 'capsman', collector: 'capsman', label: 'Provisioning Rule',
  title: 'CAPsMAN Provisioning Rule', menu: '/interface/wifi/provisioning',
  requiresMenu: '/interface/wifi/provisioning',
  // A provisioning rule has no name and nothing unique about it — the same
  // problem a firewall rule has, and the same answer: a composite identity. The
  // row is ADDRESSED by `.id`; this only has to answer "is this still the rule I
  // was looking at". src/collectors/capsman.js mirrors this tuple, in this order.
  identity: ['supportedBands', 'action', 'masterConfiguration', 'nameFormat'],
  // ORDER IS MEANING here as it is in the firewall: the first rule whose bands
  // match a joining radio wins, so a broad rule above a specific one hides it.
  ordered: true,
  // NO capsmanPush guard here, unlike the four profile menus. Editing a rule
  // does not push anything: MikroTik's docs are explicit that "provisioning
  // itself is not for sending configuration, it is for essentially creating a
  // new interface" — it acts when a CAP joins. A guard that always returned
  // "nothing to say" would be noise in the registry.
  actions: _CAPS_ACTIONS,
  fields: [
    _f('supportedBands', 'supported-bands', 'Supported Bands', 'text',
      { optionsFrom: { values: ['2ghz-ax', '2ghz-n', '2ghz-g', '5ghz-ax', '5ghz-ac', '5ghz-n', '6ghz-ax'] },
        help: 'a comma list — the rule matches a radio offering any of them' }),
    _f('action', 'action', 'Action', 'select', { required: true,
      options: ['create-dynamic-enabled', 'create-enabled', 'create-disabled', 'none'] }),
    _f('masterConfiguration', 'master-configuration', 'Master Configuration', 'text',
      { optionsFrom: { menu: '/interface/wifi/configuration', value: 'name' } }),
    _f('slaveConfigurations', 'slave-configurations', 'Slave Configurations', 'text',
      { optionsFrom: { menu: '/interface/wifi/configuration', value: 'name' },
        help: 'a comma list — the extra SSIDs provisioned onto the same radio' }),
    _f('nameFormat', 'name-format', 'Name Format', 'text', { placeholder: '%I-%N' }),
    _f('radioMac', 'radio-mac', 'Radio MAC', 'mac',
      { help: 'match one radio only; leave blank to match any' }),
    _f('identityRegexp', 'identity-regexp', 'Identity Regexp', 'text'),
    _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
    _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
  ],
};

const _CAPS_CONFIG = {
  key: 'capsConfig', page: 'capsman', collector: 'capsman', label: 'Configuration Profile',
  title: 'CAPsMAN Configuration Profile', menu: '/interface/wifi/configuration',
  identity: 'name', requiresMenu: '/interface/wifi/configuration',
  guard: 'capsmanPush',
  fields: [
    _f('name', 'name', 'Name', 'text', { required: true, placeholder: 'Guest WiFi 5Ghz' }),
    _f('ssid', 'ssid', 'SSID', 'text', { max: 32 }),
    _f('country', 'country', 'Country', 'text'),
    _f('mode', 'mode', 'Mode', 'select', { options: ['ap', 'station', 'station-bridge'] }),
    _f('hideSsid', 'hide-ssid', 'Hide SSID', 'bool', { clearable: true }),
    _f('security', 'security', 'Security Profile', 'text',
      { optionsFrom: { menu: '/interface/wifi/security', value: 'name' } }),
    _f('channel', 'channel', 'Channel Profile', 'text',
      { optionsFrom: { menu: '/interface/wifi/channel', value: 'name' } }),
    _f('datapath', 'datapath', 'Datapath Profile', 'text',
      { optionsFrom: { menu: '/interface/wifi/datapath', value: 'name' } }),
    // `manager` is DELIBERATELY not a field. MikroTik's own documentation warns
    // that configuration.manager belongs on the CAP device itself and must never
    // be pushed through a provisioned profile. Offering it here is a footgun
    // with no upside — the collector still reads it so the card can SHOW it.
    _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
    _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
  ],
};

const _CAPS_SECURITY = {
  key: 'capsSecurity', page: 'capsman', collector: 'capsman', label: 'Security Profile',
  title: 'CAPsMAN Security Profile', menu: '/interface/wifi/security',
  identity: 'name', requiresMenu: '/interface/wifi/security',
  guard: 'capsmanPush',
  // Length only, never presence: a blank passphrase means "leave the current one
  // alone" (see buildArgs), so requiring one would make renaming a profile
  // demand the passphrase be retyped.
  check: _pskLength('passphrase'),
  fields: [
    _f('name', 'name', 'Name', 'text', { required: true, placeholder: 'Guest WiFi' }),
    _f('authenticationTypes', 'authentication-types', 'Authentication', 'text',
      { optionsFrom: { values: ['', 'wpa2-psk', 'wpa3-psk', 'wpa2-psk,wpa3-psk',
                                'wpa2-eap', 'wpa3-eap', 'owe'] },
        help: 'blank is an open network' }),
    _f('passphrase', 'passphrase', 'Passphrase', 'secret',
      { max: 63, help: 'leave blank to keep the current passphrase' }),
    _f('wps', 'wps', 'WPS', 'select', { options: ['disable', 'push-button'] }),
    _f('ft', 'ft', '802.11r Fast Roaming', 'bool', { clearable: true }),
    _f('ftOverDs', 'ft-over-ds', 'FT over DS', 'bool', { clearable: true }),
    _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
    _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
  ],
};

const _CAPS_CHANNEL = {
  key: 'capsChannel', page: 'capsman', collector: 'capsman', label: 'Channel Profile',
  title: 'CAPsMAN Channel Profile', menu: '/interface/wifi/channel',
  identity: 'name', requiresMenu: '/interface/wifi/channel',
  guard: 'capsmanPush',
  fields: [
    _f('name', 'name', 'Name', 'text', { required: true, placeholder: '5Ghz Channels' }),
    _f('band', 'band', 'Band', 'text',
      { optionsFrom: { values: ['2ghz-ax', '2ghz-n', '5ghz-ax', '5ghz-ac', '6ghz-ax'] } }),
    // A list and a range are both valid: `5180,5260,5500` and `5180-5730`.
    _f('frequency', 'frequency', 'Frequency', 'text', { placeholder: '5180,5260 or 5180-5730' }),
    _f('width', 'width', 'Channel Width', 'text',
      { optionsFrom: { values: ['20mhz', '20/40mhz', '20/40/80mhz', '20/40/80/160mhz'] } }),
    _f('secondaryFrequency', 'secondary-frequency', 'Secondary Frequency', 'text'),
    _f('skipDfsChannels', 'skip-dfs-channels', 'Skip DFS Channels', 'select',
      { options: ['disabled', '10min-cac', 'all'] }),
    _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
    _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
  ],
};

const _CAPS_DATAPATH = {
  key: 'capsDatapath', page: 'capsman', collector: 'capsman', label: 'Datapath Profile',
  title: 'CAPsMAN Datapath Profile', menu: '/interface/wifi/datapath',
  identity: 'name', requiresMenu: '/interface/wifi/datapath',
  guard: 'capsmanPush',
  fields: [
    _f('name', 'name', 'Name', 'text', { required: true, placeholder: 'datapath' }),
    _f('bridge', 'bridge', 'Bridge', 'text',
      { optionsFrom: { menu: '/interface/bridge', value: 'name' } }),
    // NOT clearable — see the note on wifiNet.vlanId. RouterOS refuses an empty
    // value for a typed integer, and `clearable` emits exactly that on an edit.
    _f('vlanId', 'vlan-id', 'VLAN ID', 'int', { min: 1, max: 4094 }),
    _f('clientIsolation', 'client-isolation', 'Client Isolation', 'bool', { clearable: true }),
    _f('localForwarding', 'local-forwarding', 'Local Forwarding', 'bool', { clearable: true }),
    _f('trafficProcessing', 'traffic-processing', 'Traffic Processing', 'select',
      { options: ['on-capsman', 'local-forwarding'] }),
    _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
    _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
  ],
};

// ── The registry ─────────────────────────────────────────────────────────────
//
// `identity` names the field that is round-tripped to detect a stale row. A
// `.id` survives a rename, which makes it the right key to ADDRESS a row with
// and the wrong one to IDENTIFY it by: if the freshly-read row no longer
// carries the value the operator was looking at, the edit is refused rather
// than applied to whatever is there now. That rule is Router Users' and it is
// inherited here wholesale.
//
// `readOnlyWhen` is checked against the FRESHLY READ row, never the browser's
// claim about it.

const RESOURCES = Object.freeze([
  {
    key: 'route', page: 'routing', collector: 'routing', label: 'Route',
    title: 'IPv4 Route', menu: '/ip/route', identity: 'dstAddress',
    // A route MikroDash did not create, it cannot edit: connected routes belong
    // to an address, dynamic ones to a protocol or a DHCP client, and RouterOS
    // rejects the write anyway. Refusing here says why.
    readOnlyWhen: (r) => r.dynamic === 'true' || r.connect === 'true',
    fields: [
      _f('dstAddress', 'dst-address', 'Destination', 'cidr', { required: true, placeholder: '0.0.0.0/0' }),
      // Not type `ip`: a gateway is legitimately an interface name, or
      // `10.0.0.1%ether1` to pin a next hop to a link.
      _f('gateway', 'gateway', 'Gateway', 'text', { required: true, placeholder: '192.168.88.1 or ether1' }),
      _f('distance', 'distance', 'Distance', 'int', { min: 1, max: 255, placeholder: '1' }),
      _f('routingTable', 'routing-table', 'Routing Table', 'text', { placeholder: 'main',
        optionsFrom: { menu: '/routing/table', value: 'name' } }),
      _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
      _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
    ],
  },

  {
    key: 'route6', page: 'routing', collector: 'routing', label: 'IPv6 Route',
    title: 'IPv6 Route', menu: '/ipv6/route', identity: 'dstAddress',
    readOnlyWhen: (r) => r.dynamic === 'true' || r.connect === 'true',
    fields: [
      _f('dstAddress', 'dst-address', 'Destination', 'cidr', { required: true, placeholder: '::/0' }),
      _f('gateway', 'gateway', 'Gateway', 'text', { required: true, placeholder: 'fe80::1%ether1' }),
      _f('distance', 'distance', 'Distance', 'int', { min: 1, max: 255, placeholder: '1' }),
      _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
      _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
    ],
  },

  {
    key: 'dnsStatic', page: 'dns', collector: 'dns', label: 'DNS Entry',
    title: 'Static DNS Entry', menu: '/ip/dns/static', identity: 'name',
    // A regexp entry has no `name` to identify it by and matches a pattern
    // rather than a host. Editing one is a different form; until it exists,
    // saying so beats offering a form that would rename it to its own regexp.
    readOnlyWhen: (r) => !!r.regexp,
    fields: [
      _f('name', 'name', 'Name', 'text', { required: true, placeholder: 'server.lan' }),
      _f('type', 'type', 'Type', 'select', { required: true, options: ['A', 'AAAA', 'CNAME', 'FWD', 'NXDOMAIN', 'TXT'] }),
      _f('address', 'address', 'Address', 'ip', { showIf: { field: 'type', in: ['A', 'AAAA'] }, required: true }),
      _f('cname', 'cname', 'Canonical Name', 'text', { showIf: { field: 'type', in: ['CNAME'] }, required: true }),
      _f('forwardTo', 'forward-to', 'Forward To', 'text', { showIf: { field: 'type', in: ['FWD'] }, required: true }),
      _f('text', 'text', 'Text', 'text', { showIf: { field: 'type', in: ['TXT'] }, required: true }),
      _f('ttl', 'ttl', 'TTL', 'text', { placeholder: '1d' }),
      _f('matchSubdomain', 'match-subdomain', 'Match Subdomains', 'bool', { clearable: true }),
      _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
      _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
    ],
  },

  {
    key: 'dhcpLease', page: 'dhcp', collector: 'dhcpLeases', label: 'Lease',
    title: 'DHCP Lease', menu: '/ip/dhcp-server/lease', identity: 'macAddress',
    // A dynamic lease is the server's, not ours. It is not editable — but it is
    // the input to make-static below, which is how it becomes editable.
    readOnlyWhen: (r) => r.dynamic === 'true',
    actions: [
      { key: 'makeStatic', verb: 'make-static', label: 'Make Static',
        when: (r) => r.dynamic === 'true',
        note: 'converted a dynamic lease to a static reservation' },
    ],
    fields: [
      _f('address', 'address', 'Address', 'ip', { required: true }),
      _f('macAddress', 'mac-address', 'MAC Address', 'mac', { required: true }),
      _f('server', 'server', 'Server', 'text', { placeholder: 'all',
        optionsFrom: { menu: '/ip/dhcp-server', value: 'name' } }),
      _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
      _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
    ],
  },

  {
    key: 'vlan', page: 'vlans', collector: 'vlans', label: 'VLAN',
    title: 'VLAN Interface', menu: '/interface/vlan', identity: 'name',
    guard: 'selfPath',
    // The VLAN itself, and deliberately NOT its parent: our address sitting on
    // `bridge` would otherwise make every VLAN riding that bridge warn, and a
    // warning that fires on the innocent case is one people learn to click
    // through — see the note at the top of queueGuard.js.
    guardInterfaceFields: ['name'],
    fields: [
      _f('name', 'name', 'Name', 'text', { required: true, placeholder: 'vlan10' }),
      _f('vlanId', 'vlan-id', 'VLAN ID', 'int', { required: true, min: 1, max: 4094 }),
      _f('interface', 'interface', 'Interface', 'text', { required: true, placeholder: 'bridge',
        optionsFrom: { menu: '/interface', value: 'name' } }),
      _f('mtu', 'mtu', 'MTU', 'int', { min: 68, max: 65535 }),
      _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
      _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
    ],
  },

  {
    key: 'bridge', page: 'bridges', collector: 'bridges', label: 'Bridge',
    title: 'Bridge', menu: '/interface/bridge', identity: 'name',
    guard: 'selfPath',
    guardInterfaceFields: ['name'],
    fields: [
      _f('name', 'name', 'Name', 'text', { required: true, placeholder: 'bridge1' }),
      _f('protocolMode', 'protocol-mode', 'Protocol Mode', 'select', { options: ['none', 'rstp', 'stp', 'mstp'] }),
      _f('vlanFiltering', 'vlan-filtering', 'VLAN Filtering', 'bool', { clearable: true }),
      _f('igmpSnooping', 'igmp-snooping', 'IGMP Snooping', 'bool', { clearable: true }),
      _f('dhcpSnooping', 'dhcp-snooping', 'DHCP Snooping', 'bool', { clearable: true }),
      _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
      _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
    ],
  },

  {
    key: 'bridgePort', page: 'bridges', collector: 'bridges', label: 'Bridge Port',
    title: 'Bridge Port', menu: '/interface/bridge/port', identity: 'interface',
    guard: 'selfPath',
    // The one in this wave most likely to cut L2 to the dashboard: pulling the
    // port our own traffic arrives on.
    guardInterfaceFields: ['interface', 'bridge'],
    fields: [
      _f('bridge', 'bridge', 'Bridge', 'text', { required: true,
        optionsFrom: { menu: '/interface/bridge', value: 'name' } }),
      _f('interface', 'interface', 'Interface', 'text', { required: true,
        optionsFrom: { menu: '/interface', value: 'name' } }),
      _f('pvid', 'pvid', 'PVID', 'int', { min: 1, max: 4094 }),
      _f('frameTypes', 'frame-types', 'Frame Types', 'select', {
        options: ['admit-all', 'admit-only-untagged-and-priority-tagged', 'admit-only-vlan-tagged'] }),
      _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
      _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
    ],
  },

  {
    key: 'veth', page: 'interfaces', collector: 'ifStatus', label: 'VETH',
    title: 'Virtual Ethernet (VETH)', menu: '/interface/veth', identity: 'name',
    // VETH ships with the container package, so the menu is simply absent on
    // most routers. `requiresMenu` makes the button appear only where it can
    // actually work, rather than offering an action that always fails.
    requiresMenu: '/interface/veth',
    fields: [
      _f('name', 'name', 'Name', 'text', { required: true, placeholder: 'veth1' }),
      // The docs' own example is `address=10.1.1.10/24 gateway=10.1.1.1`, so
      // this carries a prefix while the gateways do not.
      _f('address', 'address', 'Address', 'cidr', { placeholder: '10.1.1.10/24' }),
      _f('gateway', 'gateway', 'Gateway', 'ip', { placeholder: '10.1.1.1' }),
      _f('gateway6', 'gateway6', 'IPv6 Gateway', 'ip'),
      _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
      _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
    ],
  },

  {
    key: 'wgPeer', page: 'vpn', collector: 'vpn', label: 'WireGuard Peer',
    title: 'WireGuard Peer', menu: '/interface/wireguard/peers', identity: 'publicKey',
    // `preshared-key` is a secret and is neither read back into the form nor
    // returned to the browser — see the `secret` type and rowValues() below.
    // audit.js masks it independently, because its NAME matches CRED_PATTERN.
    fields: [
      _f('interface', 'interface', 'Interface', 'text', { required: true, placeholder: 'wireguard1',
        optionsFrom: { menu: '/interface/wireguard', value: 'name' } }),
      _f('publicKey', 'public-key', 'Public Key', 'wgkey', { required: true }),
      _f('allowedAddress', 'allowed-address', 'Allowed Addresses', 'text', { required: true, placeholder: '10.0.0.2/32' }),
      _f('endpointAddress', 'endpoint-address', 'Endpoint', 'text'),
      _f('endpointPort', 'endpoint-port', 'Endpoint Port', 'int', { min: 1, max: 65535 }),
      _f('persistentKeepalive', 'persistent-keepalive', 'Keepalive', 'text', { placeholder: '25s' }),
      _f('presharedKey', 'preshared-key', 'Pre-shared Key', 'secret', { help: 'leave blank to keep the current key' }),
      _f('comment', 'comment', 'Comment', 'text', { clearable: true }),
      _f('disabled', 'disabled', 'Disabled', 'bool', { clearable: true }),
    ],
  },

  // Wireless — defined above the registry, alongside the helpers they share.
  _WIFI_NET,
  _WL_NET,
  _WL_SEC_PROFILE,

  // CAPsMAN — likewise. Five menus, one tabbed card, fleet-wide blast radius.
  _CAPS_PROVISIONING,
  _CAPS_CONFIG,
  _CAPS_SECURITY,
  _CAPS_CHANNEL,
  _CAPS_DATAPATH,

  // ── Firewall ──────────────────────────────────────────────────────────────
  //
  // The one place in this registry where POSITION is part of the meaning. A
  // rule below the final drop does nothing; the same rule above an accept
  // blocks everything. `ordered` says so, and is what puts the move controls on
  // the page and lets res:move address this menu.
  //
  // `guard: 'fwGuard'` is the lockout guard — a filter rule is the one thing
  // here that can cut MikroDash off from the router it manages.

  {
    key: 'fwFilter', page: 'firewall', collector: 'firewall', label: 'Filter Rule',
    title: 'Firewall Filter Rule', menu: '/ip/firewall/filter',
    identity: _FW_IDENTITY, ordered: true, guard: 'fwGuard',
    readOnlyWhen: _fwReadOnly, actions: _FW_ACTIONS, check: _fwCheck,
    fields: [
      ..._fwHead(['input', 'forward', 'output'],
                 ['accept', 'drop', 'reject', 'tarpit', 'log', 'passthrough',
                  'fasttrack-connection', 'jump', 'return',
                  'add-src-to-address-list', 'add-dst-to-address-list']),
      ..._fwMatch(),
      // A comma list, not one value — `established,related` is the single most
      // common thing written here.
      _f('connectionState', 'connection-state', 'Connection State', 'text',
         { placeholder: 'established,related' }),
      _f('rejectWith', 'reject-with', 'Reject With', 'text',
         { showIf: { field: 'action', in: ['reject'] },
           optionsFrom: { values: ['icmp-network-unreachable', 'icmp-host-unreachable',
                                   'icmp-port-unreachable', 'icmp-admin-prohibited', 'tcp-reset'] } }),
      ..._fwTail(),
    ],
  },

  {
    key: 'fwNat', page: 'firewall', collector: 'firewall', label: 'NAT Rule',
    title: 'Firewall NAT Rule', menu: '/ip/firewall/nat',
    identity: _FW_IDENTITY, ordered: true, guard: 'fwGuard',
    readOnlyWhen: _fwReadOnly, actions: _FW_ACTIONS, check: _fwCheck,
    fields: [
      ..._fwHead(['srcnat', 'dstnat'],
                 ['accept', 'masquerade', 'dst-nat', 'src-nat', 'redirect', 'netmap', 'same',
                  'log', 'jump', 'return', 'add-src-to-address-list', 'add-dst-to-address-list']),
      ..._fwMatch(),
      _f('toAddresses', 'to-addresses', 'To Addresses', 'text',
         { showIf: { field: 'action', in: ['dst-nat', 'src-nat', 'netmap', 'same'] } }),
      _f('toPorts', 'to-ports', 'To Ports', 'text',
         { showIf: { field: 'action', in: ['dst-nat', 'redirect', 'netmap'] } }),
      ..._fwTail(),
    ],
  },

  {
    key: 'fwMangle', page: 'firewall', collector: 'firewall', label: 'Mangle Rule',
    title: 'Firewall Mangle Rule', menu: '/ip/firewall/mangle',
    identity: _FW_IDENTITY, ordered: true, guard: 'fwGuard',
    readOnlyWhen: _fwReadOnly, actions: _FW_ACTIONS, check: _fwCheck,
    fields: [
      ..._fwHead(['prerouting', 'input', 'forward', 'output', 'postrouting'],
                 ['accept', 'mark-connection', 'mark-packet', 'mark-routing',
                  'change-mss', 'change-ttl', 'change-dscp', 'route', 'log',
                  'passthrough', 'jump', 'return']),
      ..._fwMatch(),
      _f('newConnectionMark', 'new-connection-mark', 'New Connection Mark', 'text',
         { showIf: { field: 'action', in: ['mark-connection'] }, required: true }),
      _f('newPacketMark', 'new-packet-mark', 'New Packet Mark', 'text',
         { showIf: { field: 'action', in: ['mark-packet'] }, required: true }),
      _f('newRoutingMark', 'new-routing-mark', 'New Routing Mark', 'text',
         { showIf: { field: 'action', in: ['mark-routing'] }, required: true }),
      // Marking rules default to passthrough=yes, and turning it off is how a
      // mangle chain stops after the first match.
      _f('passthrough', 'passthrough', 'Passthrough', 'bool', { clearable: true }),
      ..._fwTail(),
    ],
  },

  {
    key: 'fwRaw', page: 'firewall', collector: 'firewall', label: 'Raw Rule',
    title: 'Firewall Raw Rule', menu: '/ip/firewall/raw',
    identity: _FW_IDENTITY, ordered: true, guard: 'fwGuard',
    readOnlyWhen: _fwReadOnly, actions: _FW_ACTIONS, check: _fwCheck,
    fields: [
      // No connection-state anywhere in raw: it runs before connection
      // tracking, so there is no state to match on yet.
      ..._fwHead(['prerouting', 'output'],
                 ['accept', 'drop', 'notrack', 'log', 'jump', 'return',
                  'add-src-to-address-list', 'add-dst-to-address-list']),
      ..._fwMatch(),
      ..._fwTail(),
    ],
  },
]);

const BY_KEY = Object.freeze(Object.fromEntries(RESOURCES.map(r => [r.key, r])));

function byKey(key) { return BY_KEY[key] || null; }

// ── Validation and sentence building ─────────────────────────────────────────

/**
 * Check a submission against the resource's own fields.
 *
 * A required field is required in both directions — an edit sends the whole
 * form, not a patch — so `editing` changes nothing here. It is carried through
 * to buildArgs(), which is where the two differ.
 */
function validate(resource, values, opts) {
  const v = values || {};
  const errors = [];
  const clean = {};

  for (const f of resource.fields) {
    if (!fieldApplies(f, v)) continue;
    const raw = v[f.name];
    const blank = raw === undefined || raw === null || String(raw).trim() === '';

    if (blank) {
      // A checkbox that is off is a value, not an omission.
      if (f.type === 'bool') { clean[f.name] = TYPES.bool.check(raw, f).value; continue; }
      if (f.required) errors.push({ field: f.name, message: f.label + ' is required' });
      continue;
    }

    const t = TYPES[f.type];
    // Unreachable while the registry test passes; a resource with an unknown
    // type must not silently fall through to writing the raw value.
    if (!t) { errors.push({ field: f.name, message: f.label + ' has an unknown type' }); continue; }

    const res = t.check(raw, f);
    if (!res.ok) errors.push({ field: f.name, message: f.label + ' ' + res.message });
    else clean[f.name] = res.value;
  }

  // Rules that span more than one field, which the per-field types cannot see.
  // Checked last, and only on values that already passed their own type — a
  // cross-check on a rejected value would report a second problem about the
  // first one.
  if (resource.check && !errors.length) errors.push(...(resource.check(clean, v) || []));

  return { ok: errors.length === 0, errors, values: clean, editing: !!(opts && opts.editing) };
}

/**
 * The `=key=value` words for a validated submission.
 *
 * Takes the OUTPUT of validate(), not raw input — passing raw values here would
 * defeat the allow-list, so it reads only keys it knows and only after they
 * have been through a type.
 */
function buildArgs(resource, validated) {
  const clean = (validated && validated.values) || {};
  const editing = !!(validated && validated.editing);
  const args = [];

  for (const f of resource.fields) {
    const has = Object.prototype.hasOwnProperty.call(clean, f.name);
    // A blank secret means "leave it alone", never "clear it": clearing a
    // pre-shared key by forgetting to retype it would silently weaken a tunnel.
    if (f.type === 'secret' && (!has || clean[f.name] === '')) continue;
    if (has) { args.push('=' + f.ros + '=' + clean[f.name]); continue; }
    // Only on an edit, and only for fields declared clearable: on a create an
    // omitted property should keep RouterOS's own default.
    if (editing && f.clearable) args.push('=' + f.ros + '=');
  }

  return args;
}

/**
 * The sentence as a human would type it, for the preview (#97 asks for this).
 *
 * Built from the same args the write uses, so it cannot describe something
 * other than what happens. A secret's VALUE is replaced here — the preview is
 * shown on screen and may be read over a shoulder or pasted into an issue.
 */
function previewCommand(resource, validated, id) {
  const secret = new Set(resource.fields.filter(f => f.type === 'secret').map(f => '=' + f.ros + '='));
  const words = buildArgs(resource, validated).map(w => {
    const eq = w.indexOf('=', 1);
    const head = w.slice(0, eq + 1);
    return secret.has(head) ? head + '«set»' : w;
  });
  const verb = id ? '/set' : '/add';
  const idWord = id ? ['=.id=' + id] : [];
  return [resource.menu + verb].concat(idWord, words).join(' ');
}

/**
 * The identity value carried by a freshly-read RouterOS row.
 *
 * `identity` is usually one field — a name, a MAC, a public key. A firewall
 * rule has none of those: it has no name at all, and nothing about it is
 * unique. So it may also be a LIST of fields, joined.
 *
 * A composite identity is not a primary key and does not need to be. The row is
 * ADDRESSED by its `.id`; the identity only has to answer "is this still the
 * rule I was looking at when I clicked". Mutation is what it catches, and a
 * chain/action/source/destination/comment tuple catches it well.
 */
function identityOf(resource, row) {
  if (!row) return '';
  const names = Array.isArray(resource.identity) ? resource.identity : [resource.identity];
  return names.map(n => {
    const f = resource.fields.find(x => x.name === n);
    return f ? String(row[f.ros] == null ? '' : row[f.ros]) : '';
  }).join('\u0001');
}

/**
 * A raw RouterOS row as form values.
 *
 * The edit form is filled from a read taken now, not from the collector's
 * payload: payload rows carry collector-shaped field names and are as stale as
 * the last tick. Secrets are omitted entirely — the form shows an empty box
 * that means "unchanged".
 */
function rowValues(resource, row) {
  const out = {};
  for (const f of resource.fields) {
    if (f.type === 'secret') continue;
    const raw = row ? row[f.ros] : undefined;
    if (raw === undefined || raw === null) continue;
    out[f.name] = f.type === 'bool' ? (String(raw) === 'true' || String(raw) === 'yes') : String(raw);
  }
  return out;
}

/**
 * What the browser is sent.
 *
 * Functions (`readOnlyWhen`, an action's `when`) cannot cross the wire and must
 * not be relied on by the page anyway — every one of them is re-evaluated
 * server-side against a fresh read. The page gets the shape of the form and
 * nothing that decides anything.
 */
function describe(resource) {
  return {
    key: resource.key,
    label: resource.label,
    title: resource.title,
    page: resource.page,
    identity: resource.identity,
    actions: (resource.actions || []).map(a => ({ key: a.key, label: a.label })),
    fields: resource.fields.map(f => ({
      name: f.name, label: f.label, type: f.type, input: TYPES[f.type].input,
      required: !!f.required, options: f.options || null, placeholder: f.placeholder || '',
      help: f.help || '', showIf: f.showIf || null,
      min: f.min === undefined ? null : f.min, max: f.max === undefined ? null : f.max,
    })),
  };
}

module.exports = {
  RESOURCES, TYPES, TYPE_KEYS, byKey, validate, buildArgs,
  previewCommand, identityOf, rowValues, describe, fieldApplies,
  optionSources, staticOptions,
};
