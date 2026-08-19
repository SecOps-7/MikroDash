'use strict';
/**
 * The firewall lockout guard, and the ordering verbs.
 *
 * fwGuard is the one guard that reasons about MATCHING rather than topology, so
 * most of this is a truth table: which rules could stop our own management
 * traffic, and which plainly could not. The cases that must stay QUIET matter as
 * much as the ones that must warn — a guard that fires on every edit is one
 * people learn to click through, which is the failure mode queueGuard's header
 * warns about at length.
 *
 * Handler behaviour is covered by source scan, for the reason given at the top
 * of test/resource-writes.test.js: what matters about a handler is structural.
 */

const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');

const G = require('../src/routeros/fwGuard');
const R = require('../src/routeros/resources');

const SRC = (...p) => fs.readFileSync(path.join(__dirname, '..', 'src', ...p), 'utf8');
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');

const FILTER = '/ip/firewall/filter';
const RAW    = '/ip/firewall/raw';
const NAT    = '/ip/firewall/nat';
const MANGLE = '/ip/firewall/mangle';

/** The router sees us at 10.0.0.5, arriving on `Home`, talking to the API on 8729. */
const ctx = { resolved: true, addresses: ['10.0.0.5'], interfaces: ['Home'], apiPort: 8729 };

const verdict = (o) => G.checkRule(Object.assign({ ctx, menu: FILTER }, o));
const warns   = (o) => verdict(o).level === 'warn';
const quiet   = (o) => verdict(o).level === 'none';

// ── Rules that would stop our packets ────────────────────────────────────────

test('a bare drop on input warns — it matches on every count', () => {
  const v = verdict({ values: { chain: 'input', action: 'drop' }, what: 'create' });
  assert.equal(v.level, 'warn');
  assert.equal(v.code, 'self-lockout');
  assert.equal(v.detail.kind, 'block');
  assert.equal(v.detail.address, '10.0.0.5');
  assert.equal(v.detail.interface, 'Home');
  assert.equal(v.detail.port, 8729);
});

test('reject and tarpit block us just as drop does', () => {
  for (const action of ['drop', 'reject', 'tarpit'])
    assert.ok(warns({ values: { chain: 'input', action }, what: 'create' }), action);
});

test('a drop whose source contains us warns; one that does not stays quiet', () => {
  assert.ok(warns({ values: { chain: 'input', action: 'drop', srcAddress: '10.0.0.0/24' }, what: 'create' }));
  assert.ok(quiet({ values: { chain: 'input', action: 'drop', srcAddress: '192.0.2.0/24' }, what: 'create' }));
});

test('raw prerouting is the other chain that sees traffic to the router', () => {
  assert.ok(warns({ menu: RAW, values: { chain: 'prerouting', action: 'drop' }, what: 'create' }));
  assert.ok(quiet({ menu: RAW, values: { chain: 'output', action: 'drop' }, what: 'create' }));
});

test('enabling a blocking rule warns, because enabling is a write', () => {
  assert.ok(warns({ values: { chain: 'input', action: 'drop', disabled: true }, what: 'enable' }));
});

// ── Rules that must stay quiet ───────────────────────────────────────────────

test('forward traffic passes through and cannot touch our session', () => {
  assert.ok(quiet({ values: { chain: 'forward', action: 'drop' }, what: 'create' }));
});

test('a protocol that is not TCP cannot match the API', () => {
  assert.ok(quiet({ values: { chain: 'input', action: 'drop', protocol: 'udp' }, what: 'create' }));
  assert.ok(quiet({ values: { chain: 'input', action: 'drop', protocol: 'icmp' }, what: 'create' }));
  assert.ok(warns({ values: { chain: 'input', action: 'drop', protocol: 'tcp' }, what: 'create' }));
});

test('a port match that excludes the API port stays quiet', () => {
  assert.ok(quiet({ values: { chain: 'input', action: 'drop', dstPort: '80' }, what: 'create' }));
  assert.ok(quiet({ values: { chain: 'input', action: 'drop', dstPort: '80,443' }, what: 'create' }));
  assert.ok(warns({ values: { chain: 'input', action: 'drop', dstPort: '8729' }, what: 'create' }));
  assert.ok(warns({ values: { chain: 'input', action: 'drop', dstPort: '8000-9000' }, what: 'create' }));
  assert.ok(warns({ values: { chain: 'input', action: 'drop', dstPort: '22,8729' }, what: 'create' }));
});

test('the API port is the one this router is reached on, not a guess', () => {
  const other = { resolved: true, addresses: ['10.0.0.5'], interfaces: ['Home'], apiPort: 8728 };
  const rule  = { chain: 'input', action: 'drop', dstPort: '8728' };
  assert.equal(G.checkRule({ ctx: other, menu: FILTER, values: rule, what: 'create' }).level, 'warn');
  assert.equal(G.checkRule({ ctx, menu: FILTER, values: rule, what: 'create' }).level, 'none',
    'a rule sparing 8729 still locks us out of a router we reach on 8728');
});

test('an in-interface that is not ours stays quiet', () => {
  assert.ok(quiet({ values: { chain: 'input', action: 'drop', inInterface: 'WAN1' }, what: 'create' }));
  assert.ok(warns({ values: { chain: 'input', action: 'drop', inInterface: 'Home' }, what: 'create' }));
  assert.ok(warns({ values: { chain: 'input', action: 'drop', inInterface: 'home' }, what: 'create' }),
    'interface names over-match on case, the safe direction for a warning');
});

test('a rule created disabled blocks nothing', () => {
  assert.ok(quiet({ values: { chain: 'input', action: 'drop', disabled: true }, what: 'create' }));
  assert.ok(quiet({ values: { chain: 'input', action: 'drop', disabled: 'yes' }, what: 'update' }));
});

test('accept is not a blocking action', () => {
  assert.ok(quiet({ values: { chain: 'input', action: 'accept' }, what: 'create' }));
});

test('mangle cannot drop and NAT is not a filter', () => {
  assert.ok(quiet({ menu: MANGLE, values: { chain: 'input', action: 'drop' }, what: 'create' }));
  assert.ok(quiet({ menu: NAT, values: { chain: 'dstnat', action: 'drop' }, what: 'create' }));
});

// ── The other half: the accept that lets us in ───────────────────────────────

test('removing the accept that currently permits us warns', () => {
  for (const what of ['delete', 'disable']) {
    const v = verdict({ before: { chain: 'input', action: 'accept', srcAddress: '10.0.0.0/24' }, what });
    assert.equal(v.level, 'warn', what);
    assert.equal(v.detail.kind, 'accept-removed', what);
  }
});

test('moving an accept on input warns — it may no longer win', () => {
  assert.ok(warns({ values: { chain: 'input', action: 'accept', srcAddress: '10.0.0.0/24' },
                    before: { chain: 'input', action: 'accept', srcAddress: '10.0.0.0/24' }, what: 'move' }));
});

test('removing an accept that never covered us stays quiet', () => {
  assert.ok(quiet({ before: { chain: 'input', action: 'accept', srcAddress: '192.0.2.0/24' }, what: 'delete' }));
});

test('removing an already-disabled accept changes nothing', () => {
  assert.ok(quiet({ before: { chain: 'input', action: 'accept', srcAddress: '10.0.0.0/24', disabled: true },
                    what: 'delete' }));
});

// ── Failing open ─────────────────────────────────────────────────────────────

test('an unresolved management path fails open', () => {
  // /user/active is denied to the read-only API user the README recommends, so
  // this is the common case, not an edge one.
  const v = G.checkRule({ ctx: { resolved: false }, menu: FILTER,
                          values: { chain: 'input', action: 'drop' }, what: 'create' });
  assert.equal(v.level, 'none');
});

test('an address spec it cannot parse is treated as a match, not as a pass', () => {
  // Ranges and negations are beyond isInCidrs. For a BLOCKING rule the safe
  // direction is to ask.
  assert.equal(G.addressCovers('10.0.0.1-10.0.0.9', ['10.0.0.5']), null);
  assert.equal(G.addressCovers('!10.0.0.0/8', ['10.0.0.5']), null);
  assert.ok(warns({ values: { chain: 'input', action: 'drop', srcAddress: '10.0.0.1-10.0.0.9' },
                    what: 'create' }));
});

// ── The acknowledgement ──────────────────────────────────────────────────────

test('the fingerprint binds to the values it was issued for', () => {
  const a = verdict({ values: { chain: 'input', action: 'drop' }, what: 'create' });
  const b = verdict({ values: { chain: 'input', action: 'drop' }, what: 'create' });
  const c = verdict({ values: { chain: 'input', action: 'drop', dstPort: '8729' }, what: 'create' });
  const d = verdict({ values: { chain: 'input', action: 'drop' }, what: 'move' });
  assert.equal(a.fingerprint, b.fingerprint, 'the same question must give the same token');
  assert.notEqual(a.fingerprint, c.fingerprint, 'a narrower rule is a different question');
  assert.notEqual(a.fingerprint, d.fingerprint, 'an ack must not carry from a create to a move');
});

// ── Port parsing ─────────────────────────────────────────────────────────────

test('port specs: single, list, range, and empty', () => {
  assert.ok(G.portCovers('', 8729), 'no port match means every port');
  assert.ok(G.portCovers('8729', 8729));
  assert.ok(G.portCovers('22,8729,443', 8729));
  assert.ok(G.portCovers('8700-8800', 8729));
  assert.ok(!G.portCovers('8730', 8729));
  assert.ok(!G.portCovers('1-1024', 8729));
});

// ── The registry side ────────────────────────────────────────────────────────

test('all four firewall tables are registered, ordered, and guarded', () => {
  for (const key of ['fwFilter', 'fwNat', 'fwMangle', 'fwRaw']) {
    const r = R.byKey(key);
    assert.ok(r, `${key} is missing`);
    assert.equal(r.page, 'firewall');
    assert.equal(r.guard, 'fwGuard');
    assert.ok(r.ordered, `${key} must declare ordered — position is meaning here`);
    assert.ok(r.menu.startsWith('/ip/firewall/'));
  }
});

test('every firewall table offers its own chains and actions', () => {
  for (const key of ['fwFilter', 'fwNat', 'fwMangle', 'fwRaw']) {
    const opts = R.staticOptions(R.byKey(key));
    assert.ok((opts.chain || []).length, `${key} offers no chains`);
    assert.ok((opts.action || []).length, `${key} offers no actions`);
  }
  // The chains really are per table — NAT has none of filter's.
  const f = R.staticOptions(R.byKey('fwFilter')).chain;
  const n = R.staticOptions(R.byKey('fwNat')).chain;
  assert.ok(!f.some(c => n.includes(c)), 'filter and NAT should share no chain');
  assert.ok(!R.staticOptions(R.byKey('fwRaw')).action.includes('reject'),
    'raw has no reject action');
});

test('chain and action stay text fields, so an unlisted value still edits', () => {
  // RouterOS has more actions than any list here will name, and versions add
  // more. A `select` type validates against its options and would make a rule
  // with an exotic action uneditable.
  const f = R.byKey('fwFilter');
  for (const name of ['chain', 'action']) {
    const fld = f.fields.find(x => x.name === name);
    assert.equal(fld.type, 'text', `${name} must not be a validating select`);
    assert.ok(fld.optionsFrom.values.length);
  }
  const v = R.validate(f, { chain: 'input', action: 'some-future-action' });
  assert.ok(v.ok, 'an action we have never heard of must still validate');
});

test('a port without a protocol is caught before the router sees it', () => {
  // RouterOS: "ports can be specified if proto is tcp,udp,udp-lite,dccp,sctp".
  // A real constraint, met live during verification, and the obvious thing to
  // fill in first is the port. Left to the router it comes back as a bare
  // refusal naming no field.
  for (const key of ['fwFilter', 'fwNat', 'fwMangle', 'fwRaw']) {
    const r = R.byKey(key);
    const chain = R.staticOptions(r).chain[0];
    const bad = R.validate(r, { chain, action: 'accept', dstPort: '8080' });
    assert.ok(!bad.ok, `${key} accepted a port with no protocol`);
    assert.equal(bad.errors[0].field, 'protocol', `${key} blamed the wrong field`);

    assert.ok(R.validate(r, { chain, action: 'accept', dstPort: '8080', protocol: 'tcp' }).ok);
    assert.ok(R.validate(r, { chain, action: 'accept', srcPort: '1024-2048', protocol: 'udp' }).ok);
    assert.ok(!R.validate(r, { chain, action: 'accept', srcPort: '53', protocol: 'icmp' }).ok,
      `${key}: icmp has no ports`);
    assert.ok(R.validate(r, { chain, action: 'accept' }).ok, `${key}: no port, no constraint`);
  }
});

test('a cross-check only runs once the fields themselves are valid', () => {
  // Otherwise a rejected value produces a second complaint about the first.
  const r = R.validate(R.byKey('fwFilter'), { chain: 'input', action: '', dstPort: '80' });
  assert.ok(!r.ok);
  assert.ok(r.errors.every(e => e.field !== 'protocol'),
    'the missing action is the problem to report, not the protocol');
});

test('a raw rule has no connection-state — raw runs before conntrack', () => {
  assert.ok(!R.byKey('fwRaw').fields.some(f => f.name === 'connectionState'));
  assert.ok(R.byKey('fwFilter').fields.some(f => f.name === 'connectionState'));
});

test('enable and disable are offered, and only when they apply', () => {
  const acts = R.byKey('fwFilter').actions;
  const on  = acts.find(a => a.key === 'enable');
  const off = acts.find(a => a.key === 'disable');
  assert.ok(on.when({ disabled: 'true' }) && !on.when({ disabled: 'false' }));
  assert.ok(off.when({ disabled: 'false' }) && !off.when({ disabled: 'true' }));
  assert.equal(on.verb, 'enable');
  assert.equal(off.verb, 'disable');
});

test('a rule some service added is not ours to edit', () => {
  assert.ok(R.byKey('fwFilter').readOnlyWhen({ dynamic: 'true' }));
  assert.ok(!R.byKey('fwFilter').readOnlyWhen({ dynamic: 'false' }));
});

test('the composite identity is what the browser also builds', () => {
  // public/app.js carries fwIdentity(), the one mirror in that file, because
  // RouterOS reuses `*N` ids after a delete and an id alone cannot say the row
  // is still the one that was on screen. The two must agree exactly.
  const SEP = String.fromCharCode(1);
  const server = R.identityOf(R.byKey('fwFilter'), {
    chain: 'input', action: 'drop', 'src-address': '10.0.0.0/24',
    'dst-address': '', comment: 'block lan' });
  assert.equal(server, ['input', 'drop', '10.0.0.0/24', '', 'block lan'].join(SEP));

  const app = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');
  const m = app.match(/function fwIdentity\(r\)\{[\s\S]*?return \[([^\]]+)\]\.join\('\\u0001'\);/);
  assert.ok(m, 'fwIdentity is gone or no longer joins on the same separator');
  assert.deepEqual(
    m[1].split(',').map(s => s.trim().replace(/^r\./, '').replace(/\|\|''$/, '')),
    R.byKey('fwFilter').identity,
    'the browser mirror must list the same fields, in the same order');
});

// ── res:move, structurally ───────────────────────────────────────────────────

function moveBody(src) {
  const at = src.indexOf("socket.on('res:move'");
  assert.ok(at > 0, 'res:move is gone');
  const end = src.indexOf('── WiFi frequency analyzer', at);
  assert.ok(end > at);
  return src.slice(at, end);
}

test('res:move is queued, gated and audited', () => {
  const src = SRC('index.js');
  assert.ok(/socket\.on\('res:move', \(req\) => _routerWriteQueue\(socket\.routerId/.test(src),
    'a move must be queued with rid captured at enqueue');
  const body = stripComments(moveBody(src));
  assert.ok(/!_resMayWrite\(rid, resource\)/.test(body), 'both gates');
  assert.ok(/audit\.fromSocket\(socket\)\.denied\(/.test(body), 'a refusal belongs in the trail');
  assert.ok(/audit\.fromSocket\(socket\)\.record\(/.test(body), 'a move belongs in the trail');
  assert.ok(!body.includes('lastPayload'), 'the order must come from a fresh read');
});

test('the browser sends a direction, and the server resolves the neighbour', () => {
  const body = stripComments(moveBody(SRC('index.js')));
  assert.ok(/r\.direction !== 'up' && r\.direction !== 'down'/.test(body),
    'only a direction is accepted');
  assert.ok(/rows\.findIndex/.test(body), 'the position comes from the fresh read');
  assert.ok(/rows\[at - 1\]\['\.id'\]/.test(body) && /rows\[at \+ 2\]/.test(body),
    'the neighbour is resolved from that read, by id');
  // An index from the browser could be computed against a table that has since
  // changed; that is the whole reason for this shape.
  assert.ok(!/r\.position|r\.index|r\.destination/.test(body),
    'the browser must not be able to name a position');
});

test('a move only applies where position is meaning', () => {
  const body = stripComments(moveBody(SRC('index.js')));
  assert.ok(/if \(!resource\.ordered\) return _resErr\('bad-request'/.test(body));
  for (const key of ['route', 'dnsStatic', 'vlan', 'bridge', 'wgPeer', 'veth'])
    assert.ok(!R.byKey(key).ordered, `${key} should not be ordered`);
});

test('a move runs the guard before it moves anything', () => {
  const body = stripComments(moveBody(SRC('index.js')));
  const gate = body.indexOf('_resAckGate');
  const write = body.indexOf('_resMoveTo(');
  assert.ok(gate > 0, 'the guard is gone');
  assert.ok(write > 0, 'the move is gone');
  assert.ok(gate < write, 'the guard must run before the write');
});

test('a drag names an anchor, never an index', () => {
  // The arrows say which way; a drag has to say where. An ordinal would be
  // wrong the moment the table shifted, so a drag sends the id it should land
  // BEFORE, which stays correct.
  const body = stripComments(moveBody(SRC('index.js')));
  assert.ok(/hasOwnProperty\.call\(r, 'anchor'\)/.test(body), 'a drag cannot say where');
  assert.ok(/!rows\.some\(x => x\['\.id'\] === r\.anchor\)/.test(body),
    'the row a drag aimed at must still exist');
  assert.ok(!/r\.position|r\.index|r\.destination|toIndex/.test(body),
    'the browser must not be able to name a position');
});

test('the guard reaches every write path, including the named verbs', () => {
  // Enabling a rule has the blast radius of creating it.
  const body = stripComments(SRC('index.js'));
  const at = body.indexOf("socket.on('res:action'");
  const end = body.indexOf("socket.on('res:move'", at);
  assert.ok(at > 0 && end > at);
  assert.ok(/_resAckGate/.test(body.slice(at, end)), 'res:action runs no guard');
});
