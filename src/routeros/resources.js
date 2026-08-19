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
 * `optionsFrom` turns a free-text field into a picker of what the router
 * actually has.
 *
 * `{ menu: '/ip/dhcp-server', value: 'name' }` means "read that menu and offer
 * its `name` column". The field's TYPE stays `text`: the list is a convenience,
 * not a constraint, because RouterOS accepts names this app has no way to
 * enumerate on every version, and a select that refused a legitimate value
 * would be worse than a text box.
 *
 * The read is allowed to fail. A menu that is denied, or absent on this
 * RouterOS version, simply yields no options and the field renders as the text
 * box it always was.
 */
function optionSources(resource) {
  return resource.fields
    .filter(f => f.optionsFrom)
    .map(f => ({ field: f.name, menu: f.optionsFrom.menu, value: f.optionsFrom.value }));
}

/** Is this field in play, given what the operator has filled in so far? */
function fieldApplies(field, values) {
  const cond = field.showIf;
  if (!cond) return true;
  const v = (values || {})[cond.field];
  return (cond.in || []).indexOf(String(v == null ? '' : v)) !== -1;
}

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

/** The identity value carried by a freshly-read RouterOS row. */
function identityOf(resource, row) {
  const f = resource.fields.find(x => x.name === resource.identity);
  if (!f || !row) return '';
  return String(row[f.ros] == null ? '' : row[f.ros]);
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
  previewCommand, identityOf, rowValues, describe, fieldApplies, optionSources,
};
