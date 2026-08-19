'use strict';
/**
 * Router Users — the lockout guard, and the shape of the handlers that call it.
 *
 * This page can permanently disconnect MikroDash from the router it manages,
 * and no other write in the app can. So the tests are heavier than the feature
 * size suggests: the pure guard is table-driven over every verb, and the
 * handler tests are source scans that pin the ORDER of read → check → write,
 * because a guard called after the write is a guard that does nothing.
 */

const { test } = require('node:test');
const assert = require('node:assert');
const fs   = require('fs');
const path = require('path');

const guard = require('../src/routeros/selfGuard');
const RosUsers = require('../src/collectors/rosusers');

const SRC = (...p) => fs.readFileSync(path.join(__dirname, '..', 'src', ...p), 'utf8');
/** Comments describe the rules; they must not be able to satisfy a scan for them. */
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');

// A router as this one actually answers: MikroDash in its own group, a human
// admin, and a service account. Three concurrent MikroDash logins, because the
// dashboard, the alerter and the routers overview each hold one.
const USERS = [
  { '.id': '*1', name: 'MikroDash', group: 'mikrodash' },
  { '.id': '*2', name: 'SecOps7',   group: 'full' },
  { '.id': '*3', name: 'mktxp',     group: 'read' },
];
const GROUPS = [
  { '.id': '*g1', name: 'mikrodash', policy: 'read,write,policy,api,!ftp' },
  { '.id': '*g2', name: 'full',      policy: 'read,write,policy' },
  { '.id': '*g3', name: 'read',      policy: 'read,!write' },
];
const ACTIVE = [
  { '.id': '*a1', name: 'MikroDash', group: 'mikrodash', via: 'api' },
  { '.id': '*a2', name: 'MikroDash', group: 'mikrodash', via: 'api' },
  { '.id': '*a3', name: 'MikroDash', group: 'mikrodash', via: 'api' },
  { '.id': '*a4', name: 'SecOps7',   group: 'full',      via: 'winbox' },
];

const self = () => guard.resolveSelf(USERS, ACTIVE, ['MikroDash']);

// ── resolveSelf ──────────────────────────────────────────────────────────────

test('the self group is resolved from /user/active in preference to /user', () => {
  // The active row names the group the session that actually authenticated
  // landed in. /user can disagree, and when it does the active row is right.
  const stale = [{ '.id': '*1', name: 'MikroDash', group: 'was-moved-here' }];
  const s = guard.resolveSelf(stale, ACTIVE, ['MikroDash']);
  assert.strictEqual(s.source, 'active');
  assert.deepStrictEqual(s.groups, ['mikrodash']);
});

test('/user is the fallback when no active row matches', () => {
  const s = guard.resolveSelf(USERS, [], ['MikroDash']);
  assert.strictEqual(s.source, 'user');
  assert.deepStrictEqual(s.groups, ['mikrodash']);
  assert.strictEqual(s.resolved, true);
});

test('both the live username and the stored one are protected', () => {
  // collection.js's fingerprint does not cover credentials, so editing a
  // router's username does not rebuild the session: the live login and
  // routers.json can disagree indefinitely. Neither may be edited.
  const s = guard.resolveSelf(USERS, ACTIVE, ['MikroDash', 'MikroDash-old']);
  assert.ok(s.names.includes('mikrodash'));
  assert.ok(s.names.includes('mikrodash-old'));
  assert.strictEqual(guard.isSelfUser(s, 'MikroDash-old'), true);
});

test('names match case-insensitively and ignore surrounding whitespace', () => {
  const s = self();
  for (const variant of ['mikrodash', 'MIKRODASH', '  MikroDash  ']) {
    assert.strictEqual(guard.isSelfUser(s, variant), true, variant + ' is protected');
  }
  assert.strictEqual(guard.isSelfGroup(s, ' MIKRODASH '), true);
});

test('an unidentifiable connecting account refuses every write', () => {
  // Fail closed. Allowing everything because we cannot tell what is ours is
  // exactly the accident the guard exists to prevent.
  const s = guard.resolveSelf(USERS, ACTIVE, ['someone-else']);
  assert.strictEqual(s.resolved, false);
  for (const v of [guard.checkUser(s, { verb: 'remove', target: { name: 'mktxp' } }),
                   guard.checkGroup(s, { verb: 'set', target: { name: 'read' } }),
                   guard.checkSession(s, { target: { name: 'SecOps7' } })]) {
    assert.strictEqual(v.ok, false);
    assert.strictEqual(v.code, 'self-unresolved');
  }
});

// ── The target side ──────────────────────────────────────────────────────────

test('every user verb against the connecting account is refused', () => {
  const s = self();
  const target = { name: 'MikroDash', group: 'mikrodash' };
  const cases = {
    remove:   { verb: 'remove', target },
    disable:  { verb: 'set', target, values: { disabled: true } },
    enable:   { verb: 'set', target, values: { disabled: false } },
    rename:   { verb: 'set', target, values: { name: 'md2' } },
    password: { verb: 'set', target, values: { password: 'x' } },
    expire:   { verb: 'set', target, values: { expired: true } },
    regroup:  { verb: 'set', target, values: { group: 'full' } },
    address:  { verb: 'set', target, values: { address: '10.0.0.0/8' } },
    timeout:  { verb: 'set', target, values: { inactivityTimeout: '5m' } },
    comment:  { verb: 'set', target, values: { comment: 'harmless' } },
  };
  for (const [label, op] of Object.entries(cases)) {
    const v = guard.checkUser(s, op);
    assert.strictEqual(v.ok, false, label + ' must be refused');
    assert.strictEqual(v.code, 'protected-account', label + ' reports protected-account');
  }
});

test('even an apparently harmless edit to the connecting account is refused', () => {
  // The comment case above is the point of this one: the guard refuses by
  // target, not by judging which fields are survivable. Deciding that would be
  // a second guard, with its own bugs, protecting the first.
  const s = self();
  assert.strictEqual(guard.checkUser(s, { verb: 'set', target: { name: 'MikroDash' },
                                          values: { comment: 'note to self' } }).ok, false);
});

test('every group verb against the connecting account group is refused', () => {
  const s = self();
  const target = { name: 'mikrodash' };
  for (const [label, op] of Object.entries({
    policy: { verb: 'set', target, values: { policy: 'read,write,policy,api' } },
    rename: { verb: 'set', target, values: { name: 'md-group' } },
    remove: { verb: 'remove', target },
  })) {
    const v = guard.checkGroup(s, op);
    assert.strictEqual(v.ok, false, label + ' must be refused');
    assert.strictEqual(v.code, 'protected-group', label + ' reports protected-group');
  }
});

test('a policy edit that would keep api and read is still refused', () => {
  // Blunt on purpose: reasoning about which policy edits survive means parsing
  // RouterOS negation syntax and implicit defaults, and being wrong there costs
  // a site visit. The UI says to use WinBox.
  const s = self();
  const v = guard.checkGroup(s, { verb: 'set', target: { name: 'mikrodash' },
                                  values: { policy: 'read,write,policy,api,ssh' } });
  assert.strictEqual(v.code, 'protected-group');
});

test('all of our concurrent sessions are refused, whatever the via', () => {
  // MikroDash holds several logins per router at once. Every one of them is
  // ours, and none is worth ending: it would simply reconnect.
  const s = self();
  const ours = ACTIVE.filter(r => r.name === 'MikroDash');
  assert.strictEqual(ours.length, 3, 'the fixture has all three concurrent logins');
  for (const row of ours) {
    const v = guard.checkSession(s, { target: { name: row.name } });
    assert.strictEqual(v.ok, false, row['.id'] + ' is refused');
    assert.strictEqual(v.code, 'protected-account');
  }
});

// ── The value side ───────────────────────────────────────────────────────────

test('no other user may be moved into the connecting account group', () => {
  // Not lockout — privilege escalation. Someone with page-write could otherwise
  // put themself in the group that holds `policy`.
  const s = self();
  const v = guard.checkUser(s, { verb: 'set', target: { name: 'mktxp', group: 'read' },
                                 values: { name: 'mktxp', group: 'mikrodash' } });
  assert.strictEqual(v.ok, false);
  assert.strictEqual(v.code, 'protected-group-value');
});

test('no new user may be created in the connecting account group', () => {
  const s = self();
  const v = guard.checkUser(s, { verb: 'add', target: null,
                                 values: { name: 'newbie', group: 'mikrodash' } });
  assert.strictEqual(v.code, 'protected-group-value');
});

test('no new user may claim the connecting account name', () => {
  const s = self();
  const v = guard.checkUser(s, { verb: 'add', target: null,
                                 values: { name: 'mikrodash', group: 'read' } });
  assert.strictEqual(v.code, 'protected-name-value');
});

test('no other group may be renamed onto the connecting account group name', () => {
  const s = self();
  const v = guard.checkGroup(s, { verb: 'set', target: { name: 'read' },
                                  values: { name: 'MikroDash' } });
  assert.strictEqual(v.code, 'protected-group-value');
});

// ── Positive controls ────────────────────────────────────────────────────────

test('ordinary users, groups and sessions are allowed', () => {
  // Over-blocking is a bug too: a guard that refuses everything makes the page
  // useless and teaches operators to work around it.
  const s = self();
  assert.strictEqual(guard.checkUser(s, { verb: 'remove', target: { name: 'mktxp', group: 'read' } }).ok, true);
  assert.strictEqual(guard.checkUser(s, { verb: 'set', target: { name: 'mktxp', group: 'read' },
                                          values: { name: 'mktxp', group: 'full' } }).ok, true);
  assert.strictEqual(guard.checkUser(s, { verb: 'add', target: null,
                                          values: { name: 'newbie', group: 'read' } }).ok, true);
  assert.strictEqual(guard.checkGroup(s, { verb: 'remove', target: { name: 'read' } }).ok, true);
  assert.strictEqual(guard.checkGroup(s, { verb: 'set', target: { name: 'read' },
                                           values: { name: 'read-only', policy: 'read' } }).ok, true);
  assert.strictEqual(guard.checkSession(s, { target: { name: 'SecOps7' } }).ok, true);
});

// ── Policy round-trip ────────────────────────────────────────────────────────

test('the granted policies survive a parse and rebuild', () => {
  const parsed = RosUsers.parsePolicy('read,write,policy,api,!ftp,!telnet');
  assert.deepStrictEqual(parsed.granted, ['read', 'write', 'policy', 'api']);
  assert.deepStrictEqual(parsed.denied,  ['ftp', 'telnet']);
  const rebuilt = RosUsers.parsePolicy(RosUsers.buildPolicy(parsed.granted));
  // Rebuilt in the router's own order, not the order they arrived in.
  assert.deepStrictEqual(rebuilt.granted, parsed.granted);
});

test('every ungranted policy is explicitly negated', () => {
  // Verified against a live router, and the reason this is not cosmetic:
  // `/user/group/set =policy=read` against a group holding read,test,api leaves
  // it holding read,test,api. A positive-only list is ADDITIVE on set — RouterOS
  // removes a policy only when it is named with a `!`. Without the negations the
  // group editor would appear to work while never removing a permission.
  const built = RosUsers.buildPolicy(['read', 'api']);
  assert.strictEqual(built.split(',').length, RosUsers.POLICIES.length,
    'all seventeen are named, granted or not');
  for (const p of RosUsers.POLICIES) {
    const expected = (p === 'read' || p === 'api') ? p : '!' + p;
    assert.ok(built.split(',').includes(expected), p + ' is stated as ' + expected);
  }
  assert.deepStrictEqual(RosUsers.parsePolicy(built).granted, ['read', 'api']);
});

test('a policy the UI does not know about cannot be sent', () => {
  // The editor renders exactly POLICIES. Anything else arriving from a browser
  // is either a newer RouterOS or a crafted request; neither should be relayed.
  const built = RosUsers.buildPolicy(['read', 'not-a-policy', 'api']);
  assert.ok(!built.includes('not-a-policy'));
  assert.deepStrictEqual(RosUsers.parsePolicy(built).granted, ['read', 'api']);
});

test('the policy vocabulary is the full RouterOS list', () => {
  assert.strictEqual(RosUsers.POLICIES.length, 17);
  for (const p of ['read', 'write', 'policy', 'api', 'rest-api', 'sensitive']) {
    assert.ok(RosUsers.POLICIES.includes(p), p + ' is offered');
  }
});

// ── The collector reads, and only reads ──────────────────────────────────────

test('the rosusers collector issues no write commands, ever', () => {
  const code = stripComments(SRC('collectors', 'rosusers.js'));
  for (const verb of ['/user/add', '/user/set', '/user/remove',
                      '/user/group/add', '/user/group/set', '/user/group/remove',
                      '/user/active/remove', 'expire-password', '/user/aaa']) {
    // Quoted, so the command is matched and not merely a prefix of a longer
    // path: '/user/set' is a substring of the perfectly innocent
    // '/user/settings/print' this collector legitimately reads.
    assert.ok(!code.includes("'" + verb + "'"), 'rosusers.js must not issue ' + verb);
  }
});

test('the collector marks rows the guard would refuse', () => {
  // The marks are decoration — the guard is the authority — but decoration that
  // disagreed with the server would teach operators to distrust the padlock.
  const view = RosUsers.buildUsersView(USERS, GROUPS, ACTIVE, null, ['MikroDash']);
  const s = self();
  for (const u of view.users) {
    assert.strictEqual(u.protected, !guard.checkUser(s, { verb: 'remove', target: { name: u.name } }).ok,
      'user ' + u.name + ' marked to match the guard');
  }
  for (const g of view.groups) {
    assert.strictEqual(g.protected, !guard.checkGroup(s, { verb: 'remove', target: { name: g.name } }).ok,
      'group ' + g.name + ' marked to match the guard');
  }
  for (const x of view.sessions) {
    assert.strictEqual(x.protected, !guard.checkSession(s, { target: { name: x.name } }).ok,
      'session ' + x.id + ' marked to match the guard');
  }
});

test('the payload carries no password field', () => {
  // /user/print does not return one. If that ever changed, this catches it
  // before a router password reaches a browser.
  const view = RosUsers.buildUsersView(
    USERS.map(u => Object.assign({ password: 'should-never-appear' }, u)),
    GROUPS, ACTIVE, null, ['MikroDash']);
  assert.ok(!JSON.stringify(view).includes('should-never-appear'));
});

test('group member counts and the password policy reach the page', () => {
  const view = RosUsers.buildUsersView(USERS, GROUPS, ACTIVE,
    { 'minimum-password-length': '8', 'minimum-categories': '3' }, ['MikroDash']);
  assert.strictEqual(view.groups.find(g => g.name === 'read').members, 1);
  assert.deepStrictEqual(view.passwordPolicy, { minLength: 8, minCategories: 3 });
});

test('an empty menu does not become a row', () => {
  // RouterOS answers an empty menu with {undefined:''}, which has a key and no
  // name — the shape that has produced phantom rows on other pages.
  const view = RosUsers.buildUsersView([{ undefined: '' }], [{ undefined: '' }],
                                       [{ undefined: '' }], null, ['MikroDash']);
  assert.deepStrictEqual(view.users, []);
  assert.deepStrictEqual(view.groups, []);
  assert.deepStrictEqual(view.sessions, []);
});

// ── The handlers ─────────────────────────────────────────────────────────────

const WRITE_EVENTS = ['rosuser:save', 'rosuser:remove', 'rosgroup:save',
                      'rosgroup:remove', 'rossession:remove'];

/** The body of one socket.on handler, from its registration to the next one. */
function handlerBody(src, ev) {
  const start = src.indexOf("socket.on('" + ev + "'");
  assert.ok(start > 0, ev + ' handler exists');
  const next = src.indexOf("socket.on('", start + 20);
  return src.slice(start, next > 0 ? next : start + 4000);
}

test('every Router Users action is gated on router:write and the page toggle', () => {
  const src = SRC('index.js');
  for (const ev of WRITE_EVENTS) {
    const body = handlerBody(src, ev);
    assert.ok(body.includes('_ruMayWrite(rid)'), ev + ' checks both gates');
  }
  // ...and _ruMayWrite is both, not one wearing the name of two.
  assert.ok(/_ruMayWrite = \(rid\) =>\s*\n?\s*_pageAllowed\(socket, 'rosusers', 'write'\) && _socketCan\(socket, 'router:write', rid\)/
    .test(src), '_ruMayWrite is the page gate AND router:write');
});

test('every refusal is recorded before it returns', () => {
  const src = SRC('index.js');
  for (const ev of WRITE_EVENTS) {
    const body = handlerBody(src, ev);
    assert.ok(body.includes('audit.fromSocket(socket).denied'), ev + ' audits its refusals');
    assert.ok(body.includes('audit.fromSocket(socket).record'), ev + ' audits its successes');
  }
});

test('the guard runs against a fresh read, and before the write', () => {
  // The ordering IS the guarantee. A check against a stale table, or one that
  // runs after the write, is a check that does nothing.
  const src = stripComments(SRC('index.js'));
  for (const ev of WRITE_EVENTS) {
    const body  = handlerBody(src, ev);
    const read  = body.indexOf('_ruRead(session, rid)');
    const check = body.search(/selfGuard\.check(User|Group|Session)\(/);
    const write = body.search(/session\.ros\.write\('\/user/);
    assert.ok(read  > 0, ev + ' re-reads from the router');
    assert.ok(check > 0, ev + ' consults the guard');
    assert.ok(write > 0, ev + ' writes to the router');
    assert.ok(read < check, ev + ': the read precedes the check');
    assert.ok(check < write, ev + ': the check precedes the write');
  }
});

test('no Router Users action resolves its target from the collector payload', () => {
  // The deliberate inversion of the Packages pattern. There, lastPayload is
  // safer than trusting the browser; here it is the thing that goes stale in
  // the dangerous direction — a row renamed after the last tick reads as
  // unprotected. The one permitted use is the router's password-length policy,
  // which is advisory and cannot unprotect anything.
  const src = stripComments(SRC('index.js'));
  for (const ev of WRITE_EVENTS) {
    const body = handlerBody(src, ev);
    for (const m of body.match(/lastPayload[^\n]*/g) || []) {
      assert.ok(m.includes('passwordPolicy'),
        ev + ' must not resolve a target from lastPayload: ' + m.trim());
    }
  }
});

test('a row is addressed by id but identified by name', () => {
  // A .id survives a rename, which makes it the right key to address a row with
  // and the wrong one to decide what the row IS.
  const src = SRC('index.js');
  assert.ok(/_ruRow = \(rows, id, expectedName\)/.test(src));
  const fn = src.slice(src.indexOf('_ruRow = (rows'), src.indexOf('_ruRow = (rows') + 500);
  assert.ok(fn.includes("String(row.name) !== String(expectedName)"),
    'the freshly-read name is compared against the one the operator saw');
  for (const ev of WRITE_EVENTS) {
    assert.ok(handlerBody(src, ev).includes('stale-row'),
      ev + ' refuses a row that changed underneath the page');
  }
});

test('writes are serialised per router and target the captured router', () => {
  const src = SRC('index.js');
  for (const ev of WRITE_EVENTS) {
    assert.ok(new RegExp("socket\\.on\\('" + ev + "', \\(req\\) => _routerWriteQueue\\(socket\\.routerId, async \\(rid\\) =>")
      .test(src), ev + ' is queued with the router id captured at enqueue');
  }
  // Reading socket.routerId inside the closure would let a router:switch
  // retarget a queued write onto a router nobody was looking at.
  // Renamed from _ruQueue when Queues became a second caller: two features
  // serialising router writes should not read a helper named for the first.
  const chain = src.slice(src.indexOf('function _routerWriteQueue'),
                          src.indexOf('function _routerWriteQueue') + 700);
  assert.ok(chain.includes('prev.then(() => fn(rid))'), 'the captured id is what the handler runs with');
});

test('a plaintext password never reaches the audit trail', () => {
  const src = stripComments(SRC('index.js'));
  const body = handlerBody(src, 'rosuser:save');
  const auditCall = body.slice(body.indexOf('audit.fromSocket(socket).record'), 
                               body.indexOf('audit.fromSocket(socket).record') + 800);
  // The plaintext variable is not mentioned in the audit call at ALL, rather
  // than mentioned in a position argued to be safe. A flag computed earlier is
  // what the call reads, so there is no expression here to reason about.
  assert.ok(!/\bpw\b/.test(auditCall), 'the password variable is never named in the audit call');
  assert.ok(!/\bbefore\.password|\bafter\.password/.test(body),
    'the password is not routed through the before/after diff either');
  assert.ok(body.includes('passwordSet: true'), 'the trail records that one was set');
});

// ── Registry ─────────────────────────────────────────────────────────────────

test('the page and collector are registered and page-scoped', () => {
  const Pages = require('../src/pages');
  const { COLLECTORS, BY_KEY } = require('../src/collection');
  const page = Pages.BY_KEY.rosusers;
  assert.ok(page, 'rosusers is a registered page');
  assert.strictEqual(page.settingsKey, 'pageRosusers');
  assert.deepStrictEqual(page.streamRooms, ['page-rosusers'], 'suspends when nobody is on the page');
  const col = BY_KEY.rosusers;
  assert.strictEqual(col.page, 'rosusers');
  assert.strictEqual(col.streamKey, null, 'poll-only by design, like packages');
  assert.strictEqual(col.disableable, true);
  assert.strictEqual(col.pollKey, 'pollRosusers');
  assert.ok(COLLECTORS.some(c => c.key === 'rosusers'));
});

test('a new page joins Advanced by existing, and not the lower tiers', () => {
  const Pages = require('../src/pages');
  assert.ok(Pages.VIEW_PRESETS.advanced.includes('rosusers'));
  assert.ok(!Pages.VIEW_PRESETS.home.includes('rosusers'));
  assert.ok(!Pages.VIEW_PRESETS.standard.includes('rosusers'));
});

test('the poll interval is settable and bounded', () => {
  const Settings = require('../src/settings');
  assert.strictEqual(typeof Settings.DEFAULTS.pollRosusers, 'number');
  assert.strictEqual(Settings.DEFAULTS.pageRosusers, true);
  const src = SRC('index.js');
  assert.ok(src.includes('pollRosusers:[5000,300000]'), 'bounded in intFields');
  assert.ok(src.includes("pollRosusers:'rosusers'"), 'reaches the live collector on save');
});
