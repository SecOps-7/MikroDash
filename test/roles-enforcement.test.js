'use strict';
// Page permissions are actually enforced (issue #108, Phase 4).
//
// Phase 3 computed page access; nothing consumed it. These pin the consumption:
// the install toggle and the role must BOTH allow a page, a dashboard card
// needs the page it borrows data from, and every collector replay is attributed
// to the right page. The last one matters most — without it the page gate is
// cosmetic, because connecting alone replays every collector's payload.
//
// The socket handlers themselves need a live io/session stack, which this suite
// deliberately does not build; the decision functions they call are the thing
// worth pinning, and a source scan holds the wiring in place.

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const os       = require('node:os');
const path     = require('node:path');

process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'roles-enforce-'));

const db      = require('../src/db');
const Pages   = require('../src/pages');
const Routers = require('../src/routers');
const rbac    = require('../src/rbac');
const { COLLECTORS } = require('../src/collection');

const INDEX_JS = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');
const RBAC_JS  = fs.readFileSync(path.join(__dirname, '..', 'src', 'rbac.js'),  'utf8');

db.open();
rbac.init({ isModern: () => true });

const rtr  = Routers.add({ label: 'Enforce', host: '10.7.0.1' });
const sess = (userId) => ({ userId });
let _seq = 0;

function roleWith(pages) {
  const r = db.createRole({ name: 'Enf ' + (++_seq) });
  db.setRolePages(r.id, pages);
  rbac.bump();
  return r.id;
}

function userWith(pages) {
  const u = 'u-' + (++_seq);
  db.upsertGrant({ principalType: 'user', principalId: u, roleId: roleWith(pages), scopeType: 'global' });
  rbac.bump();
  return u;
}

// ── The conjunction: install toggle AND role ─────────────────────────────────

test('a role can grant every page, and only 21 of them have an install toggle', () => {
  const u = userWith(Pages.KEYS.map(page => ({ page, access: 'write' })));
  for (const p of Pages.PAGES) {
    assert.strictEqual(rbac.canPage(sess(u), p.key, 'read', rtr.id), true, p.key + ' by role');
  }
  const toggleable = Pages.PAGES.filter(p => p.settingsKey).map(p => p.key);
  assert.strictEqual(toggleable.length, 21);
  assert.ok(!toggleable.includes('dashboard') && !toggleable.includes('settings'));
});

test('_pageAllowed applies the install toggle and canPage deliberately does not', () => {
  // Pinned by source, because the split is the design decision: putting the
  // toggle inside canPage() would make every role test depend on settings state.
  assert.match(INDEX_JS, /function _pageAllowed\(socket, page, access = 'read'\)/);
  assert.match(INDEX_JS, /if \(def\.settingsKey && Settings\.load\(\)\[def\.settingsKey\] === false\) return false;/);
  // rbac.js must not read settings at all — it has no Settings import, and
  // canPage's body must not reach for one.
  assert.ok(!/require\(['"]\.\/settings['"]\)/.test(RBAC_JS), 'rbac.js must not import Settings');
  const fn = RBAC_JS.slice(RBAC_JS.indexOf('function canPage'), RBAC_JS.indexOf('function requirePage'));
  assert.ok(!/Settings\.load/.test(fn), 'canPage must not consult Settings');
});

// ── page:focus gates the join AND the replay ─────────────────────────────────

test('page:focus returns before both the room join and the payload replay', () => {
  // Gating only the join would still hand the caller a full lastPayload for a
  // page they cannot see — the handler emits it directly, not through the room.
  const at    = INDEX_JS.indexOf("socket.on('page:focus'");
  const block = INDEX_JS.slice(at, INDEX_JS.indexOf("socket.on('page:blur'"));
  const guard = block.indexOf('_pageAllowed(socket, name)');
  const join  = block.indexOf('socket.join(');
  const emit  = block.indexOf('socket.emit(');
  assert.ok(guard > -1, 'page:focus must consult _pageAllowed');
  assert.ok(guard < join, 'the guard must precede the room join');
  assert.ok(guard < emit, 'the guard must precede the replay');
});

test('a dashboard card requires the dashboard and the page it borrows from', () => {
  const at    = INDEX_JS.indexOf("socket.on('dashcard:focus'");
  const block = INDEX_JS.slice(at, INDEX_JS.indexOf("socket.on('dashcard:blur'"));
  assert.match(block, /_pageAllowed\(socket, 'dashboard'\)/);
  assert.match(block, /_dashCardPage\(key\)/);
  assert.ok(block.indexOf('_pageAllowed') < block.indexOf('socket.join('),
    'both checks must precede the join');
});

test('the dash-card→page map is derived from the collector registry', () => {
  // The four room-gated cards are named after their collectors, so the mapping
  // cannot drift from src/pages.js. 'diagnostics' is the dashboard's own card.
  assert.strictEqual(Pages.pageForCollector('firewall'),  'firewall');
  assert.strictEqual(Pages.pageForCollector('vpn'),       'vpn');
  assert.strictEqual(Pages.pageForCollector('logs'),      'logs');
  assert.strictEqual(Pages.pageForCollector('bandwidth'), 'bandwidth');
  assert.strictEqual(Pages.pageForCollector('diagnostics'), null, 'falls back to dashboard');
  assert.match(INDEX_JS, /return Pages\.pageForCollector\(key\) \|\| 'dashboard';/);
});

// ── The replay filter ────────────────────────────────────────────────────────

test('every page-bearing collector replay is gated on its own page', () => {
  // Walks the registry rather than a hand-kept list, so a new collector cannot
  // be added with an ungated replay.
  const at    = INDEX_JS.indexOf('function sendInitialState');
  const block = INDEX_JS.slice(at, INDEX_JS.indexOf('function _updatePageStream'));
  for (const c of COLLECTORS) {
    if (c.page === null) continue;             // traffic/system/arp: global chrome
    assert.ok(block.includes(`_mayReplay(socket, '${c.key}')`),
      `${c.key} replay is not page-gated in sendInitialState`);
  }
});

test('the header collectors are deliberately never withheld', () => {
  // traffic and system drive the gauges on every page. Gating them on
  // 'dashboard' would blank the header for someone who has Logs but not
  // Dashboard, which is why they carry page: null.
  assert.strictEqual(Pages.pageForCollector('traffic'), null);
  assert.strictEqual(Pages.pageForCollector('system'),  null);
  const at    = INDEX_JS.indexOf('function sendInitialState');
  const block = INDEX_JS.slice(at, INDEX_JS.indexOf('function _updatePageStream'));
  assert.ok(!block.includes("_mayReplay(socket, 'system')"));
  assert.ok(!block.includes("_mayReplay(socket, 'traffic')"));
});

test('firewall:tab is a write action on the firewall page', () => {
  // It mutates shared session state for every viewer of the router, so it was
  // already gated on router:diagnose; the page form additionally respects the
  // install toggle, and the projection is what makes the two equivalent.
  const at    = INDEX_JS.indexOf("socket.on('firewall:tab'");
  assert.match(INDEX_JS.slice(at, at + 1400), /_pageAllowed\(socket, 'firewall', 'write'\)/);

  const writer = userWith([{ page: 'firewall', access: 'write' }]);
  assert.strictEqual(rbac.can(sess(writer), 'router:diagnose', rtr.id), true);
  const reader = userWith([{ page: 'firewall', access: 'read' }]);
  assert.strictEqual(rbac.can(sess(reader), 'router:diagnose', rtr.id), false);
});

test('the frequency scan is a write action on the wireless page', () => {
  // The first deliberately disruptive action in MikroDash: it takes the radio
  // off the air and drops every client on it. router:diagnose was the obvious
  // reuse, but it is conferred only by firewall write, which would have meant a
  // firewall operator could disrupt a radio while a wireless operator could not.
  const writer = userWith([{ page: 'wireless', access: 'write' }]);
  assert.strictEqual(rbac.can(sess(writer), 'router:scan', rtr.id), true);

  // Reading the Wireless page is not licence to take it down.
  const reader = userWith([{ page: 'wireless', access: 'read' }]);
  assert.strictEqual(rbac.can(sess(reader), 'router:scan', rtr.id), false);

  // Nor does write elsewhere leak it. This is the distinction that adding a
  // permission bought over reusing router:diagnose.
  const fw = userWith([{ page: 'firewall', access: 'write' }]);
  assert.strictEqual(rbac.can(sess(fw), 'router:scan', rtr.id), false);
  assert.strictEqual(rbac.can(sess(writer), 'router:diagnose', rtr.id), false,
    'and wireless write does not confer the firewall action either');

  // Scoped, so it fails closed with no target — a socket with no active router
  // must not be able to scan anything.
  assert.strictEqual(rbac.can(sess(writer), 'router:scan', null), false);
});

// ── REST ─────────────────────────────────────────────────────────────────────

test('the topology layout routes are scoped to the topology page', () => {
  // They take a routerId, so before this they were a cross-router probe.
  assert.match(INDEX_JS, /app\.get\('\/api\/topology-layout'[\s\S]{0,300}?Rbac\.requirePage\('topology', 'read', Rbac\.fromQuery\('routerId'\)\)/);
  assert.match(INDEX_JS, /app\.post\('\/api\/topology-layout'[\s\S]{0,300}?Rbac\.requirePage\('topology', 'read', Rbac\.fromBody\('routerId'\)\)/);
});

test('the dashboard layout routes require the dashboard somewhere', () => {
  assert.match(INDEX_JS, /app\.get\('\/api\/dashboard-layout', layoutLimiter, _requireDashboard/);
  assert.match(INDEX_JS, /app\.post\('\/api\/dashboard-layout', layoutLimiter, _requireDashboard/);
});

test('canPageAnywhere answers for a request with no router in it', () => {
  const yes = userWith([{ page: 'dashboard', access: 'read' }]);
  const no  = userWith([{ page: 'logs', access: 'read' }]);
  assert.strictEqual(rbac.canPageAnywhere(sess(yes), 'dashboard'), true);
  assert.strictEqual(rbac.canPageAnywhere(sess(no),  'dashboard'), false);
  assert.strictEqual(rbac.canPageAnywhere(null,      'dashboard'), false);
});

test('requirePage denies with 403 and calls next() when permitted', () => {
  const mw = rbac.requirePage('logs', 'read', (req) => req.query.routerId);
  const u  = userWith([{ page: 'logs', access: 'read' }]);

  let nexted = false;
  mw({ authSession: sess(u), query: { routerId: rtr.id } }, null, () => { nexted = true; });
  assert.strictEqual(nexted, true);

  let status = 0, body = null;
  const res = { status(c) { status = c; return this; }, json(b) { body = b; } };
  mw({ authSession: sess(userWith([{ page: 'vpn', access: 'read' }])), query: { routerId: rtr.id } },
     res, () => { throw new Error('should not have been permitted'); });
  assert.strictEqual(status, 403);
  assert.deepStrictEqual(body, { ok: false, error: 'Not permitted' });
});

test('requirePage with no target fails closed', () => {
  const mw = rbac.requirePage('logs', 'read', (req) => req.query.routerId);
  let status = 0;
  const res = { status(c) { status = c; return this; }, json() {} };
  mw({ authSession: sess(userWith([{ page: 'logs', access: 'read' }])), query: {} },
     res, () => { throw new Error('a missing target must never permit'); });
  assert.strictEqual(status, 403);
});
