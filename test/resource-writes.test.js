'use strict';
/**
 * The resource write engine (issue #97).
 *
 * Split the way the feature is: the registry and the guard are pure and get
 * ordinary assertions, and the handlers are covered by SOURCE SCANS, because
 * what matters about them is structural — that a gate is present, that a fresh
 * read happens, that `lastPayload` does not appear. A scan catches the removal
 * of a check; a behavioural test around a fake socket would only catch a check
 * that stopped working, which is the rarer failure.
 *
 * The same reasoning is already in test/queues.test.js and
 * test/router-users-guard.test.js, and the helpers below are theirs.
 */

const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');

const SRC = (...p) => fs.readFileSync(path.join(__dirname, '..', 'src', ...p), 'utf8');
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');

const R        = require('../src/routeros/resources');
const selfPath = require('../src/routeros/selfPath');
const Pages    = require('../src/pages');
const { COLLECTORS } = require('../src/collection');

/** The generic handler block, so a scan cannot accidentally match Queues. */
function engineBody(src) {
  const at = src.indexOf('── Generic resource writes');
  assert.ok(at > 0, 'the generic resource block is gone');
  const end = src.indexOf('── WiFi frequency analyzer', at);
  assert.ok(end > at, 'the block should end at the WiFi analyzer');
  return src.slice(at, end);
}

/** The upgrade handler, likewise. */
function upgradeBody(src) {
  const at = src.indexOf("socket.on('packages:upgrade'");
  assert.ok(at > 0, 'packages:upgrade is gone');
  const end = src.indexOf('── Router Users', at);
  assert.ok(end > at);
  return src.slice(at, end);
}

// ── The registry holds together ──────────────────────────────────────────────

test('every resource names a real page and a real collector', () => {
  for (const r of R.RESOURCES) {
    assert.ok(Pages.BY_KEY[r.page], `${r.key} names page "${r.page}", which does not exist`);
    assert.ok(COLLECTORS.some(c => c.key === r.collector),
      `${r.key} names collector "${r.collector}", which is not in COLLECTORS`);
  }
});

test('the collector a resource refreshes actually feeds its page', () => {
  // Otherwise a save would refresh a view the operator is not looking at, and
  // the page they ARE looking at would keep showing the old row.
  for (const r of R.RESOURCES) {
    const c = COLLECTORS.find(x => x.key === r.collector);
    assert.equal(c.page, r.page, `${r.key}: collector ${r.collector} feeds ${c.page}, not ${r.page}`);
  }
});

test('every field declares a type the validator knows', () => {
  for (const r of R.RESOURCES) {
    for (const f of r.fields) {
      assert.ok(R.TYPE_KEYS.includes(f.type), `${r.key}.${f.name} has unknown type "${f.type}"`);
      assert.ok(f.ros, `${r.key}.${f.name} has no RouterOS property name`);
    }
  }
});

test('every resource identifies its rows by one of its own fields', () => {
  // identityOf() reads the identity through the field's `ros` name, so an
  // identity naming a field that does not exist would silently compare '' to
  // '' and make every stale row look current.
  for (const r of R.RESOURCES) {
    // A firewall rule has no name and nothing unique, so its identity is a
    // composite. Either way every field it names has to exist, or identityOf()
    // would compare '' to '' and make every stale row look current.
    const names = Array.isArray(r.identity) ? r.identity : [r.identity];
    assert.ok(names.length, `${r.key} has no identity`);
    for (const n of names)
      assert.ok(r.fields.some(f => f.name === n),
        `${r.key}: identity "${n}" is not one of its fields`);
  }
});

test('an action declares a verb and a when', () => {
  for (const r of R.RESOURCES) {
    for (const a of r.actions || []) {
      assert.equal(typeof a.when, 'function', `${r.key}.${a.key} has no when()`);
      assert.ok(a.verb, `${r.key}.${a.key} has no RouterOS verb`);
    }
  }
});

test('a resource that declares a guard declares what the guard looks at', () => {
  for (const r of R.RESOURCES) {
    if (!r.guard) continue;
    // A resource may declare several guards: they answer different questions
    // and one write can trip more than one. _resVerdict runs them in order and
    // returns the first warn.
    const kinds = Array.isArray(r.guard) ? r.guard : [r.guard];
    assert.ok(kinds.length, `${r.key}: declares an empty guard list`);
    for (const g of kinds)
      assert.ok(['selfPath', 'fwGuard', 'wifiInherit', 'capsmanPush'].includes(g),
        `${r.key}: unknown guard "${g}"`);
    // selfPath asks about interfaces, so it needs to be told which fields hold
    // them. fwGuard reads the rule itself and needs no such hint, and
    // wifiInherit is answered from the rows the caller already holds.
    if (kinds.includes('selfPath')) {
      assert.ok((r.guardInterfaceFields || []).length,
        `${r.key} declares selfPath but names no interface fields for it to check`);
      for (const n of r.guardInterfaceFields)
        assert.ok(r.fields.some(f => f.name === n), `${r.key}: guard field "${n}" is not a field`);
    }
  }
});

// ── Types reject what they should ────────────────────────────────────────────

test('cidr takes v4, v6 and a bare address, and refuses a bad prefix', () => {
  const ok = (v) => R.TYPES.cidr.check(v).ok;
  assert.ok(ok('192.0.2.0/24'));
  assert.ok(ok('2001:db8::/32'));
  assert.ok(ok('192.0.2.1'), 'a bare address is allowed; RouterOS normalises it');
  assert.ok(!ok('192.0.2.0/33'));
  assert.ok(!ok('999.0.2.0/24'));
  assert.ok(!ok('ether1'));
});

test('mac accepts the RouterOS form and normalises case', () => {
  const r = R.TYPES.mac.check('aa:bb:cc:dd:ee:ff');
  assert.ok(r.ok);
  assert.equal(r.value, 'AA:BB:CC:DD:EE:FF');
  assert.ok(!R.TYPES.mac.check('aa:bb:cc').ok);
  assert.ok(!R.TYPES.mac.check('aabbccddeeff').ok);
});

test('int honours the bounds its field declares', () => {
  const f = { min: 1, max: 4094 };
  assert.ok(R.TYPES.int.check('10', f).ok);
  assert.ok(!R.TYPES.int.check('0', f).ok);
  assert.ok(!R.TYPES.int.check('4095', f).ok);
  assert.ok(!R.TYPES.int.check('1.5', f).ok);
  assert.ok(!R.TYPES.int.check('ten', f).ok);
});

test('select refuses a value it was not offered', () => {
  const f = { options: ['A', 'AAAA', 'CNAME'] };
  assert.ok(R.TYPES.select.check('CNAME', f).ok);
  assert.ok(!R.TYPES.select.check('SRV', f).ok);
  assert.ok(!R.TYPES.select.check('', f).ok);
});

test('wgkey wants 44 base64 characters', () => {
  const good = 'A'.repeat(43) + '=';
  assert.ok(R.TYPES.wgkey.check(good).ok);
  assert.ok(!R.TYPES.wgkey.check('A'.repeat(43)).ok, 'no padding');
  assert.ok(!R.TYPES.wgkey.check('A'.repeat(20) + '=').ok, 'too short');
  assert.ok(!R.TYPES.wgkey.check('!'.repeat(43) + '=').ok, 'not base64');
});

test('text refuses control characters but keeps ordinary punctuation', () => {
  assert.ok(R.TYPES.text.check('uplink to the DC (primary)').ok);
  assert.ok(R.TYPES.text.check('admit-only-vlan-tagged').ok, 'hyphens are ordinary');
  assert.ok(R.TYPES.text.check('a b c').ok, 'so are spaces');
  assert.ok(!R.TYPES.text.check('line\nbreak').ok);
  assert.ok(!R.TYPES.text.check('tab\there').ok);
});

// ── The sentence builder is an allow-list ────────────────────────────────────

test('a key the registry does not name cannot reach a sentence', () => {
  const route = R.byKey('route');
  const v = R.validate(route, {
    dstAddress: '192.0.2.0/24', gateway: '10.0.0.1',
    // None of these are fields on `route`. If any reached buildArgs, an
    // operator could set arbitrary RouterOS properties from a crafted page.
    'routing-mark': 'evil', policy: 'write', '.id': '*99',
  });
  const args = R.buildArgs(route, v);
  const keys = args.map(a => a.slice(1, a.indexOf('=', 1)));
  const declared = route.fields.map(f => f.ros);
  for (const k of keys) assert.ok(declared.includes(k), `"${k}" is not a declared property`);
  assert.ok(!args.some(a => a.includes('evil')));
  assert.ok(!args.some(a => a.startsWith('=.id=')));
});

test('a value that fails its type never reaches a sentence', () => {
  const vlan = R.byKey('vlan');
  const v = R.validate(vlan, { name: 'vlan10', vlanId: '9999', interface: 'bridge' });
  assert.ok(!v.ok);
  assert.ok(!R.buildArgs(vlan, v).some(a => a.startsWith('=vlan-id=')),
    'a rejected value must not be written anyway');
});

test('buildArgs reads only validated values, never raw input', () => {
  const route = R.byKey('route');
  // A caller passing raw input in the shape validate() returns still gets
  // nothing through, because the keys are read off the registry's fields.
  const args = R.buildArgs(route, { values: { nonsense: 'x' }, editing: false });
  assert.ok(!args.some(a => a.includes('nonsense')));
});

test('clearable fields empty on an edit and are omitted on a create', () => {
  const route = R.byKey('route');
  const base = { dstAddress: '192.0.2.0/24', gateway: '10.0.0.1' };
  const created = R.buildArgs(route, R.validate(route, base, { editing: false }));
  const edited  = R.buildArgs(route, R.validate(route, base, { editing: true }));
  assert.ok(!created.includes('=comment='), 'a create should not set an empty comment');
  assert.ok(edited.includes('=comment='), 'an edit must be able to clear a comment');
});

test('a checkbox that is off is a value, not an omission', () => {
  const route = R.byKey('route');
  const v = R.validate(route, { dstAddress: '192.0.2.0/24', gateway: '10.0.0.1', disabled: false });
  assert.ok(R.buildArgs(route, v).includes('=disabled=no'));
});

test('a field hidden by showIf is not required', () => {
  const dns = R.byKey('dnsStatic');
  // type=CNAME, so `address` is not on screen and must not be demanded.
  const v = R.validate(dns, { name: 'a.lan', type: 'CNAME', cname: 'b.lan' });
  assert.ok(v.ok, JSON.stringify(v.errors));
  assert.ok(!R.buildArgs(dns, v).some(a => a.startsWith('=address=')));
});

test('a required field that is on screen is still demanded', () => {
  const dns = R.byKey('dnsStatic');
  const v = R.validate(dns, { name: 'a.lan', type: 'A' });
  assert.ok(!v.ok);
  assert.ok(v.errors.some(e => e.field === 'address'));
});

// ── Secrets ──────────────────────────────────────────────────────────────────

test('a blank secret is left alone rather than cleared', () => {
  const peer = R.byKey('wgPeer');
  const v = R.validate(peer, {
    interface: 'wireguard1', publicKey: 'A'.repeat(43) + '=',
    allowedAddress: '10.0.0.2/32', presharedKey: '',
  });
  assert.ok(v.ok, JSON.stringify(v.errors));
  assert.ok(!R.buildArgs(peer, v).some(a => a.startsWith('=preshared-key=')),
    'forgetting to retype a pre-shared key must not remove it');
});

test('a secret never comes back out of a router row', () => {
  const peer = R.byKey('wgPeer');
  const vals = R.rowValues(peer, {
    '.id': '*1', interface: 'wireguard1', 'public-key': 'A'.repeat(43) + '=',
    'preshared-key': 'S'.repeat(43) + '=', 'allowed-address': '10.0.0.2/32',
  });
  assert.ok(!('presharedKey' in vals), 'the form must not be filled with the current secret');
  assert.equal(vals.interface, 'wireguard1');
});

test('the preview shows the command but not the secret', () => {
  const peer = R.byKey('wgPeer');
  const secret = 'S'.repeat(43) + '=';
  const v = R.validate(peer, {
    interface: 'wireguard1', publicKey: 'A'.repeat(43) + '=',
    allowedAddress: '10.0.0.2/32', presharedKey: secret,
  });
  const cmd = R.previewCommand(peer, v, null);
  assert.ok(cmd.startsWith('/interface/wireguard/peers/add'));
  assert.ok(!cmd.includes(secret), 'the preview is on screen and may be shared');
  assert.ok(cmd.includes('=preshared-key=«set»'));
});

test('the preview reflects the same args the write would send', () => {
  const route = R.byKey('route');
  const v = R.validate(route, { dstAddress: '192.0.2.0/24', gateway: '10.0.0.1' }, { editing: true });
  const cmd = R.previewCommand(route, v, '*A3');
  assert.ok(cmd.startsWith('/ip/route/set =.id=*A3'));
  for (const a of R.buildArgs(route, v)) assert.ok(cmd.includes(a), `preview is missing ${a}`);
});

// ── What crosses the wire to the browser ─────────────────────────────────────

test('describe() is JSON-safe and carries no decisions', () => {
  for (const r of R.RESOURCES) {
    const d = JSON.parse(JSON.stringify(R.describe(r)));
    assert.ok(!('readOnlyWhen' in d), `${r.key}: a predicate must not cross the wire`);
    assert.ok(!('menu' in d), `${r.key}: the browser has no use for the RouterOS path`);
    assert.ok(!('guard' in d));
    for (const f of d.fields) {
      assert.ok(!('ros' in f), `${r.key}.${f.name}: the RouterOS property name is not the browser's business`);
      assert.ok(f.input, `${r.key}.${f.name} has no widget`);
    }
  }
});

test('identityOf reads the identity through the field it names', () => {
  const route = R.byKey('route');
  assert.equal(R.identityOf(route, { 'dst-address': '192.0.2.0/24' }), '192.0.2.0/24');
  assert.equal(R.identityOf(route, {}), '', 'a row without it must not match anything by accident');
});

// ── readOnlyWhen ─────────────────────────────────────────────────────────────

test('routes the router owns are not editable', () => {
  const route = R.byKey('route');
  assert.ok(route.readOnlyWhen({ dynamic: 'true' }));
  assert.ok(route.readOnlyWhen({ connect: 'true' }));
  assert.ok(!route.readOnlyWhen({ dynamic: 'false', connect: 'false' }));
});

test('a dynamic lease is not editable, but is the input to make-static', () => {
  const lease = R.byKey('dhcpLease');
  assert.ok(lease.readOnlyWhen({ dynamic: 'true' }));
  assert.ok(!lease.readOnlyWhen({ dynamic: 'false' }));
  const mk = lease.actions.find(a => a.key === 'makeStatic');
  assert.ok(mk.when({ dynamic: 'true' }));
  assert.ok(!mk.when({ dynamic: 'false' }), 'an already-static lease has nothing to convert');
});

test('a regexp DNS entry is not editable by the name form', () => {
  const dns = R.byKey('dnsStatic');
  assert.ok(dns.readOnlyWhen({ regexp: '.*\\.ads\\.example' }));
  assert.ok(!dns.readOnlyWhen({ name: 'server.lan' }));
});

// ── selfPath, the L2 guard ───────────────────────────────────────────────────

const ACTIVE = [{ name: 'mikrodash', address: '10.0.0.5' }];
const ADDRS  = [
  { address: '10.0.0.1/24',     interface: 'bridge', 'actual-interface': 'bridge' },
  { address: '192.168.50.1/24', interface: 'vlan50', 'actual-interface': 'vlan50' },
];
const pathOf = (active = ACTIVE, addrs = ADDRS) =>
  selfPath.resolveManagementInterfaces({ activeRows: active, usernames: ['mikrodash'], addressRows: addrs });

test('the management interface is found by subnet, not by equality', () => {
  const p = pathOf();
  assert.ok(p.resolved);
  assert.deepEqual(p.interfaces, ['bridge']);
  assert.equal(p.address, '10.0.0.5');
});

test('pulling the port on our own bridge warns', () => {
  const v = selfPath.checkInterfaceEdit({ path: pathOf(), targets: ['ether5', 'bridge'], action: 'delete' });
  assert.equal(v.level, 'warn');
  assert.equal(v.code, 'self-cutoff');
  assert.equal(v.detail.interface, 'bridge');
  assert.equal(v.detail.address, '10.0.0.5');
});

test('an unrelated interface does not warn', () => {
  const v = selfPath.checkInterfaceEdit({ path: pathOf(), targets: ['vlan50'], action: 'update' });
  assert.equal(v.level, 'none');
});

test('the guard fails open when /user/active is denied', () => {
  // The documented read-only monitoring group denies `policy`, and RouterOS
  // gates /user/active behind it — so this is the common case, not an edge one.
  const v = selfPath.checkInterfaceEdit({ path: pathOf([]), targets: ['bridge'], action: 'delete' });
  assert.equal(v.level, 'none', 'a denied read must not block a routine edit');
});

test('the guard fails open when we reach the router over a route', () => {
  // Off-subnet: no connected prefix contains us, so no single interface is the
  // management interface. That is wanGuard's question, not this one.
  const p = pathOf([{ name: 'mikrodash', address: '203.0.113.9' }]);
  assert.ok(!p.resolved);
  assert.equal(selfPath.checkInterfaceEdit({ path: p, targets: ['bridge'], action: 'delete' }).level, 'none');
});

test('interface names are matched case-insensitively', () => {
  const v = selfPath.checkInterfaceEdit({ path: pathOf(), targets: ['BRIDGE'], action: 'delete' });
  assert.equal(v.level, 'warn', 'over-matching is the safe direction for a warning');
});

test('the fingerprint binds to the values it was issued for', () => {
  const p = pathOf();
  const a = selfPath.checkInterfaceEdit({ path: p, targets: ['bridge'], action: 'delete' });
  const b = selfPath.checkInterfaceEdit({ path: p, targets: ['bridge'], action: 'delete' });
  const c = selfPath.checkInterfaceEdit({ path: p, targets: ['bridge'], action: 'update' });
  assert.equal(a.fingerprint, b.fingerprint, 'the same question must produce the same token');
  assert.notEqual(a.fingerprint, c.fingerprint, 'an ack must not carry from one action to another');
});

test('a resolved path with no targets does not warn', () => {
  assert.equal(selfPath.checkInterfaceEdit({ path: pathOf(), targets: [], action: 'update' }).level, 'none');
});

// ── The handlers, structurally ───────────────────────────────────────────────

test('every res: handler is registered', () => {
  const src = SRC('index.js');
  for (const ev of ['res:schema', 'res:row', 'res:save', 'res:remove', 'res:action', 'res:preview'])
    assert.ok(src.includes(`socket.on('${ev}'`), `${ev} is not registered`);
});

test('no res: handler consults lastPayload', () => {
  // The payload is exactly what goes stale in the dangerous direction: a row
  // renamed since the last tick would be identified by the name it used to
  // have. Every handler re-reads instead.
  const body = stripComments(engineBody(SRC('index.js')));
  assert.ok(!body.includes('lastPayload'),
    'a resource handler resolved its target from the collector payload');
});

test('the writing handlers run through the per-router queue', () => {
  const body = SRC('index.js');
  for (const ev of ['res:save', 'res:remove', 'res:action'])
    assert.ok(new RegExp(`socket\\.on\\('${ev}',\\s*\\(req\\)\\s*=>\\s*_routerWriteQueue\\(socket\\.routerId`).test(body),
      `${ev} must be queued with rid captured at enqueue`);
});

test('preview is the one handler that is not queued, because it writes nothing', () => {
  const src = SRC('index.js');
  assert.ok(!/socket\.on\('res:preview',\s*\(req\)\s*=>\s*_routerWriteQueue/.test(src));
  const block = engineBody(src);
  const at = block.indexOf("socket.on('res:preview'");
  const end = block.indexOf("socket.on('res:save'", at);
  assert.ok(end > at);
  const preview = stripComments(block.slice(at, end));
  assert.ok(!/ros\.write\(/.test(preview), 'the preview must not reach the router');
});

test('both gates are named, and every write path checks them', () => {
  const body = stripComments(engineBody(SRC('index.js')));
  // _resMayWrite is the conjunction; keeping `router:write` spelled out in it
  // is what keeps the permission greppable rather than hidden behind a helper.
  assert.ok(/_pageAllowed\(socket, resource\.page, 'write'\)/.test(body));
  assert.ok(/_socketCan\(socket, 'router:write', rid\)/.test(body));
  const checks = (body.match(/!_resMayWrite\(rid, resource\)/g) || []).length;
  assert.ok(checks >= 4, `expected every write path to check _resMayWrite, found ${checks}`);
});

test('a refused write is audited, not just refused', () => {
  const body = stripComments(engineBody(SRC('index.js')));
  const denied = (body.match(/audit\.fromSocket\(socket\)\.denied\(/g) || []).length;
  assert.ok(denied >= 5, `expected denial rows on every refusal path, found ${denied}`);
});

test('readOnlyWhen is checked against the freshly read row', () => {
  const body = stripComments(engineBody(SRC('index.js')));
  assert.ok(/resource\.readOnlyWhen\(before\)/.test(body),
    'readOnlyWhen must be applied to `before`, which came from the router');
  assert.ok(!/readOnlyWhen\(r\.|readOnlyWhen\(req/.test(body),
    "readOnlyWhen must never be applied to the browser's claim about the row");
});

test('an action re-checks its own when() server-side', () => {
  const body = stripComments(engineBody(SRC('index.js')));
  assert.ok(/if \(!def\.when\(row\)\)/.test(body),
    'the browser offering a button is a hint, never a permission');
});

test('the audit trail masks a field whose declared type is secret', () => {
  const body = stripComments(engineBody(SRC('index.js')));
  assert.ok(/_resAuditValues/.test(body), '_resAuditValues is gone');
  assert.ok(/f\.type === 'secret'/.test(body),
    'masking must key on the declared type, not on the field name');
  // audit.js masks on NAME, and `presharedKey` does not match its pattern —
  // which is exactly why the engine must not rely on it.
  const m = SRC('audit.js').match(/const CRED_PATTERN = \/([^/]+)\/i;/);
  assert.ok(m, 'CRED_PATTERN moved; check whether the type-based masking is still needed');
  assert.ok(!new RegExp(m[1], 'i').test('presharedKey'),
    'if audit.js now matches this name, say so here rather than deleting the type check');
});

// ── The upgrade button ───────────────────────────────────────────────────────

test('the upgrade demands the router name back', () => {
  const body = stripComments(upgradeBody(SRC('index.js')));
  assert.ok(/confirm\.toLowerCase\(\) !== routerName\.toLowerCase\(\)/.test(body),
    'a misclick, or a click on the wrong router, must not reach a reboot');
  assert.ok(/_pkgErr\('confirm-mismatch'/.test(body));
});

test('the upgrade is audited before the command is sent', () => {
  // The router reboots while the command is in flight, so a row written
  // afterwards would lose the record of the most consequential action here.
  const body = upgradeBody(SRC('index.js'));
  const recordAt  = body.indexOf('audit.fromSocket(socket).record(');
  const installAt = body.indexOf("'/system/package/update/install'");
  assert.ok(recordAt > 0 && installAt > 0);
  assert.ok(recordAt < installAt, 'the audit row must precede the install');
});

test('the upgrade re-reads rather than trusting the payload the button was drawn from', () => {
  const body = stripComments(upgradeBody(SRC('index.js')));
  assert.ok(body.includes("'/system/package/update/print'"),
    'an update installed by somebody else must not reboot the router for nothing');
  assert.ok(!body.includes('lastPayload'));
  assert.ok(/_pkgErr\('nothing-to-update'/.test(body));
});

test('the upgrade is gated on the packages page and router:write', () => {
  const body = stripComments(upgradeBody(SRC('index.js')));
  assert.ok(/_pageAllowed\(socket, 'packages', 'write'\)/.test(body));
  assert.ok(/_socketCan\(socket, 'router:write', rid\)/.test(body));
  assert.ok(/audit\.fromSocket\(socket\)\.denied\(/.test(body), 'a refusal belongs in the trail');
});

test('the upgrade is queued with the router captured at enqueue', () => {
  const src = SRC('index.js');
  assert.ok(/socket\.on\('packages:upgrade', \(req\) => _routerWriteQueue\(socket\.routerId/.test(src),
    'a reboot must land on the router the operator pressed the button on');
});

// ── Collectors still only read ───────────────────────────────────────────────

test('no collector issues a write verb', () => {
  // The engine writes through session.ros; a collector that learned to write
  // would be a second, unaudited path to the router.
  const files = ['routing', 'dns', 'dhcpLeases', 'vlans', 'bridges', 'vpn',
                 'queues', 'rosusers', 'wan', 'packages'];
  const verbs = ['add', 'set', 'remove', 'make-static', 'install', 'apply-changes'];
  for (const f of files) {
    const code = SRC('collectors', `${f}.js`);
    for (const verb of verbs) {
      // Quoted, so /user/set does not match '/user/settings/print'.
      assert.ok(!code.includes(`/${verb}'`), `collectors/${f}.js appears to issue "${verb}"`);
    }
  }
});

test('the collectors the engine refreshes can be refreshed', () => {
  // refreshNow() is what makes a save show the router's answer instead of the
  // old row until the next config sweep — up to ten minutes on DNS and DHCP.
  // sessionProp matches the filename for every collector but one, so the
  // exception is named here rather than guessed at.
  const FILE_FOR = { ifStatus: 'interfaceStatus' };
  for (const key of new Set(R.RESOURCES.map(r => r.collector))) {
    const def = COLLECTORS.find(c => c.key === key);
    const name = FILE_FOR[def.sessionProp] || def.sessionProp;
    assert.ok(/async refreshNow\s*\(/.test(SRC('collectors', `${name}.js`)),
      `collectors/${name}.js has no refreshNow()`);
  }
});

test('the null collector stub answers refreshNow too', () => {
  // A router with the collector switched off (#105) still accepts writes,
  // because they go through session.ros — the refresh must not throw.
  assert.ok(/refreshNow/.test(SRC('collectors', 'nullCollector.js')));
});

// ── The browser side is mounted ──────────────────────────────────────────────

/** A mount slot may name several resources — `data-res-add="route,route6"`. */
const mountedKeys = (html, attr) => {
  const out = new Set();
  for (const m of html.matchAll(new RegExp(`data-res-${attr}="([^"]+)"`, 'g')))
    for (const k of m[1].split(',')) out.add(k.trim());
  return out;
};

test('every resource has somewhere to be added from', () => {
  // A registry entry with no mount point is a feature nobody can reach.
  const html = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');
  // A slot whose resource follows what the page is showing declares its whole
  // range in data-res-add-dynamic, so a resource reachable only after a tab
  // change still counts as mounted.
  const mounted = new Set([...mountedKeys(html, 'add'), ...mountedKeys(html, 'add-dynamic')]);
  for (const r of R.RESOURCES)
    assert.ok(mounted.has(r.key), `${r.key} has no + Add mount in index.html`);
});

test('every mount point names a resource that exists', () => {
  const html = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');
  const keys = new Set(R.RESOURCES.map(r => r.key));
  for (const attr of ['add', 'rows'])
    for (const k of mountedKeys(html, attr))
      assert.ok(keys.has(k), `index.html mounts "${k}", which is not a resource`);
});

test('a card header keeps its own controls beside the Add button', () => {
  // The regression this pins: with the auto margin on the BUTTON, it swallowed
  // all the free space and shoved every search box over to the title. The
  // margin belongs to the group that holds both.
  const html = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');
  assert.ok(/\.hdr-actions\{margin-left:auto/.test(html),
    'the right-hand group rule is gone');
  // Every slot that shares its header with another control must be inside a
  // group with it, not a bare sibling of it.
  for (const id of ['dnsStaticSearch', 'vlansSearch', 'dhcpServerFilter', 'bridgesHostSearch']) {
    const at = html.indexOf(`id="${id}"`);
    assert.ok(at > 0, `${id} is gone`);
    const around = html.slice(Math.max(0, at - 900), at + 900);
    assert.ok(/class="hdr-actions"|justify-content-end/.test(around),
      `${id} is not in a right-hand group`);
    assert.ok(/data-res-add=/.test(around), `${id} lost the Add button from its group`);
  }
});

test('the Add button is the last thing in its card header', () => {
  // It is pinned to the top right corner by an auto margin, which only reaches
  // the corner if nothing follows it. The bridge-port card is the one that
  // caught this: the host-table search sits in the same header and used to come
  // after the button on the Hosts tab.
  const html = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');
  for (const m of html.matchAll(/<span data-res-add="[^"]+"><\/span>/g)) {
    const after = html.slice(m.index + m[0].length, m.index + m[0].length + 400)
      .replace(/<!--[\s\S]*?-->/g, '').trimStart();
    assert.ok(after.startsWith('</div>'),
      `a mount slot is followed by ${after.slice(0, 60).replace(/\n/g, ' ')}`);
  }
});

test('a card with two resources uses one slot, not two', () => {
  // Two slots are two flex items, and in a wrapping header they landed on
  // separate lines — right-aligned but stacked. A resource may legitimately be
  // mounted on more than one card (VLAN and Bridge appear on their own pages
  // and again behind "+ Virtual Interface"), so the rule is per slot.
  const html = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');
  const slots = [...html.matchAll(/data-res-add="([^"]+)"/g)].map(m => m[1]);
  for (const s of slots) {
    const keys = s.split(',').map(k => k.trim());
    assert.equal(new Set(keys).size, keys.length, `slot "${s}" names a resource twice`);
  }
  assert.ok(slots.some(s => s.includes(',')), 'the routes card should mount both families in one slot');
});

test('a resource whose menu is optional says so rather than offering a dead button', () => {
  // VETH ships with the container package, so the menu is simply absent on
  // most routers. The button must not be offered where it cannot work.
  const veth = R.byKey('veth');
  assert.equal(veth.requiresMenu, '/interface/veth');
  const body = stripComments(engineBody(SRC('index.js')));
  assert.ok(/resource\.requiresMenu && session/.test(body), 'nothing probes the menu');
  assert.ok(/permitted: !unsupported && _resMayWrite\(rid, resource\)/.test(body),
    'an absent menu must clear `permitted`, not merely annotate it');
});

test('a field that offers router-supplied options names a menu and a column', () => {
  for (const r of R.RESOURCES) {
    for (const s of R.optionSources(r)) {
      assert.ok(s.menu.startsWith('/'), `${r.key}.${s.field}: "${s.menu}" is not a RouterOS path`);
      assert.ok(s.value, `${r.key}.${s.field} names no column to read`);
      assert.ok(r.fields.some(f => f.name === s.field));
    }
  }
});

test('the fields the user named all offer a picker', () => {
  // The complaint was concrete: a lease could not pick its DHCP server, a peer
  // could not pick its interface, a bridge port could not pick its bridge.
  const want = {
    dhcpLease:  { server: '/ip/dhcp-server' },
    wgPeer:     { interface: '/interface/wireguard' },
    bridgePort: { bridge: '/interface/bridge', interface: '/interface' },
    vlan:       { interface: '/interface' },
  };
  for (const [key, fields] of Object.entries(want)) {
    const byField = Object.fromEntries(R.optionSources(R.byKey(key)).map(s => [s.field, s.menu]));
    for (const [field, menu] of Object.entries(fields))
      assert.equal(byField[field], menu, `${key}.${field} should be picked from ${menu}`);
  }
});

test('options are fetched when a form opens, not shipped with every schema', () => {
  // res:schema is requested for every resource on every connect; reading five
  // menus there would be a burst of router I/O nobody asked for.
  const body = stripComments(engineBody(SRC('index.js')));
  const schemaAt = body.indexOf("socket.on('res:schema'");
  const newAt    = body.indexOf("socket.on('res:new'");
  assert.ok(schemaAt > 0 && newAt > 0);
  const schemaBody = body.slice(schemaAt, newAt);
  assert.ok(!/_resOptions/.test(schemaBody), 'res:schema must not read option menus');
  assert.ok(/socket\.on\('res:new'/.test(body), 'res:new is what fetches them');
  assert.ok(/_resOptions/.test(body.slice(newAt)), 'res:new should read them');
});

test('an option menu that cannot be read degrades to a text box', () => {
  // /routing/table is absent on some builds and /ip/dhcp-server can be denied.
  // A picker is a convenience; it must never be what stops a write.
  const body = engineBody(SRC('index.js'));
  const at = body.indexOf('const _resOptions');
  assert.ok(at > 0, '_resOptions is gone');
  const fn = body.slice(at, body.indexOf('const _resolve', at));
  assert.ok(/catch \(_\) \{ menus\.set\(src\.menu, null\); \}/.test(fn),
    'each menu read must be caught individually');
  assert.ok(/if \(!rows\) continue;/.test(fn), 'a failed menu yields no options rather than throwing');
});

test('an empty slot is removed from the layout, not merely blanked', () => {
  // A viewer who may not write must get back exactly the header they had
  // before. An empty span left as a flex item strands the filters in the
  // middle of a space-between header.
  const html = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');
  assert.ok(/\[data-res-add\]\{[^}]*margin-left:auto/.test(html),
    'the corner rule is gone');
  assert.ok(/\[data-res-add\]:empty\{display:none\}/.test(html),
    'an empty slot must be taken out of the flex layout');
});

test('a read-only row still offers the verbs it declares', () => {
  // The gap this closes: a dynamic DHCP lease is read-only BY DEFINITION, and
  // make-static is the one useful thing to do with it. A page that refuses to
  // open a read-only row makes that verb unreachable, which is how the action
  // shipped dead the first time.
  const app = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');
  const body = app.slice(app.indexOf('── Resource write engine'));
  assert.ok(/data-res-actionbtn/.test(body), 'nothing renders an action button');
  assert.ok(/socket\.emit\('res:action'/.test(body), 'nothing emits res:action');
  assert.ok(!/if \(d\.readOnly\) \{[\s\S]{0,120}return;/.test(body),
    'a read-only row must open rather than be refused outright');

  const html = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');
  assert.ok(html.includes('id="res_actions"'), 'the dialog has nowhere to put an action button');
});

test('every declared action reaches the browser with a label', () => {
  for (const r of R.RESOURCES) {
    const described = R.describe(r).actions;
    assert.equal(described.length, (r.actions || []).length, `${r.key}: an action was dropped`);
    for (const a of described) assert.ok(a.key && a.label, `${r.key}: an action has no label to render`);
  }
});

test('the page never decides whether a write is allowed', () => {
  // `permitted` draws a button and nothing else. If app.js ever gated a write
  // on its own copy of a role, the gate would be one XSS away from irrelevant.
  const app = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');
  const at = app.indexOf('── Resource write engine');
  assert.ok(at > 0, 'the resource engine module is gone');
  // Bounded to the engine's own section rather than running to end-of-file.
  // Everything after it is ordinary page code that happens to be appended later,
  // and catching that was accidental: the CAPsMAN card says `_data.role === 'cap'`
  // about the CAPsMAN role — manager / cap / both — which has nothing to do with
  // an authorization role. The invariant is about the ENGINE, and this still
  // pins the engine.
  const end  = app.indexOf('\n// ── ', at + 10);
  assert.ok(end > at, 'could not find the end of the engine section');
  const body = app.slice(at, end);
  assert.ok(!/Rbac|allowedRouterIds|role ===/.test(body),
    'the browser must not reason about roles');
});

// ── Drag: the slot you are leaving ──────────────────────────────────────────
//
// The engine moves the actual <tr> through the DOM, so before this the row's
// origin vanished the instant you moved and there was no way to change your mind
// mid-drag. A marker row now holds the slot open until the drag ends.

const APP  = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');
const HTML = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');

// Scope every lookup below to the resource engine. app.js holds a SECOND
// endDrag (the world map's pan handler) and a second pointerdown listener
// (topology), both earlier in the file — anchoring on the bare name found the
// map's and asserted against the wrong function entirely.
const ENGINE = APP.slice(APP.indexOf('── Resource write engine'));

test('the origin marker is never mistaken for a real row', () => {
  // Two things key on data-id: the drop-target lookup and the move-anchor walk.
  // A marker carrying one would be reported to the server as the row to move
  // beneath — a wrong reorder, not a cosmetic bug.
  const at = ENGINE.indexOf('function makeOriginMarker');
  const body = ENGINE.slice(at, ENGINE.indexOf('\n  }', at));
  assert.ok(/className = 'res-drag-origin'/.test(body));
  assert.ok(!/data-id/.test(body), 'the marker must carry no data-id');
  assert.ok(/colSpan/.test(body), 'and must span the table, or the gap collapses');
});

test('the gap is exactly the size of the hole it fills', () => {
  // Rules wrap to different heights. A CSS-guessed height would shift every row
  // beneath the gap by the difference.
  const at = ENGINE.indexOf('function makeOriginMarker');
  const body = ENGINE.slice(at, ENGINE.indexOf('\n  }', at));
  assert.ok(/getBoundingClientRect\(\)\.height/.test(body),
    'measure the row rather than guessing in CSS');
});

test('the marker appears on the first move, not on pointerdown', () => {
  // Inserting it up front would push everything below down a row before the drag
  // had gone anywhere.
  const down = ENGINE.slice(ENGINE.indexOf("addEventListener('pointerdown'"));
  assert.ok(!/makeOriginMarker/.test(down.slice(0, 900)),
    'pointerdown must not create the marker');
  const to = ENGINE.slice(ENGINE.indexOf('function dragTo'));
  assert.ok(/if \(!_drag\.marker\)[\s\S]{0,200}makeOriginMarker/.test(to),
    'dragTo creates it once, before the row leaves its slot');
});

test('dropping back onto the gap returns the row to where it started', () => {
  const to = ENGINE.slice(ENGINE.indexOf('function dragTo'));
  const body = to.slice(0, to.indexOf('\n  }'));
  assert.ok(/over === _drag\.marker/.test(body), 'the gap is a drop target');
  assert.ok(/insertBefore\(_drag\.row, _drag\.marker\.nextSibling\)/.test(body),
    'and puts the row straight back into it');
  assert.ok(/tr\.res-drag-origin/.test(ENGINE),
    'rowUnder must accept the marker, or hovering the gap is a dead spot');
});

test('the gap collapses while the row is home', () => {
  // A row cannot be moved INSIDE its own marker — the DOM offers only before and
  // after — so dragging back left the rule sitting BESIDE the gap with the slot
  // still looking empty. It never read as "dropped back where it was".
  const at = ENGINE.indexOf('function syncOriginMarker');
  const body = ENGINE.slice(at, ENGINE.indexOf('\n  }', at));
  assert.ok(/nextElementSibling === _drag\.row/.test(body),
    'home is the row sitting immediately after its own marker');
  assert.ok(/classList\.toggle\('is-home'/.test(body), 'and toggles, so it comes back');
  assert.ok(/tr\.res-drag-origin\.is-home\{display:none\}/.test(HTML),
    'collapsed outright, so the table reads exactly as it did before the drag');

  const to = ENGINE.slice(ENGINE.indexOf('function dragTo'));
  assert.ok(/syncOriginMarker\(\);/.test(to.slice(0, to.indexOf('\n  }'))),
    'every placement re-evaluates it, or the gap sticks in the wrong state');
});

test('the gap closes however the drag ended', () => {
  const at = ENGINE.indexOf('function endDrag');
  const body = ENGINE.slice(at, ENGINE.indexOf('\n  }', at));
  assert.ok(/removeChild\(d\.marker\)/.test(body), 'dropped, cancelled or abandoned');
  // pointercancel and the re-render bail both route through endDrag, so this one
  // removal covers every exit.
  assert.ok(/pointercancel[\s\S]{0,120}endDrag\(\)/.test(ENGINE));
});

test('the gap does not wear the same colour as the row being dragged', () => {
  // One is where you are going, the other is what you are leaving; the same tint
  // for both would say they are the same kind of thing.
  assert.ok(/tr\.res-drag-origin\{background:rgba\(248,113,113/.test(HTML), 'red for the origin');
  assert.ok(/tr\.res-dragging\{[^}]*rgba\(56,189,248/.test(HTML), 'cyan for the row in flight');
});

test('the gap is the same red as the drop pill, carried by colour alone', () => {
  // Same RED as actionBadge's drop/reject/tarpit pill, so the Firewall page
  // carries one red rather than two nearly-identical ones.
  const badge = APP.slice(APP.indexOf('function actionBadge'),
                          APP.indexOf('function parseTxRate'));
  assert.ok(/'rgba\(248,113,113,\.9\)'/.test(badge),
    'the drop pill still starts from this red — if it moves, the gap must follow');

  const fill = (HTML.match(/tr\.res-drag-origin\{background:rgba\(248,113,113,([\d.]+)\)\}/) || [])[1];
  assert.ok(fill, 'the gap fills with the pill red');
  assert.ok(parseFloat(fill) <= 0.1,
    'more translucent than the pill fill (' + fill + '): a pill is a value, this is an absence');

  // No outline at all. The cell keeps the table's ordinary grey rule, so the gap
  // reads as an empty row rather than a boxed callout — which also means nothing
  // here has to fight `.table td{border-color:var(--border) !important}`.
  const cell = (HTML.match(/tr\.res-drag-origin td\{([^}]*)\}/) || [])[1];
  assert.ok(cell !== undefined, 'the cell rule is gone');
  assert.ok(!/border/.test(cell), 'colour alone — no border on the gap cell');
  assert.ok(!/!important/.test(cell), 'and so no !important needed');
});
