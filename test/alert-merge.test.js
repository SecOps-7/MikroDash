// Tests for the merge of the notification bell into the server alerting system.
//
// The bell used to be an independent, browser-only, in-memory detector; the
// server had a second one driving push channels. They disagreed. The server is
// now the only detector and the bell is a view of what it recorded. What is
// tested here is the seam that makes that true — the emit alongside the DB
// write — plus the BGP rules that moved across, because they changed shape on
// the way (cooldown-gated → edge-triggered).

const { test } = require('node:test');
const assert   = require('node:assert');
const path     = require('node:path');
const Module   = require('node:module');

// ── db and routers stubs ────────────────────────────────────────────────────
// alerter.js requires both directly. Seeding require.cache before it loads is
// what lets these tests assert on the recorded rows without a real SQLite file.
const inserted = [];
const resolved = [];
let   nextId   = 1;
let   resolveIds = null;   // what resolveAlertEvent should claim it closed

// Tracks which alerts are open, rather than only recording that a call
// happened. The real table enforces one open row per (router, type, subject),
// and a stub that always says "recorded" cannot show a duplicate being filed.
const openKeys = new Set();
const _key = (r, t, s) => r + '|' + t + '|' + (s || '');

const dbStub = {
  hasOpenAlert(routerId, alertType, subject) {
    return openKeys.has(_key(routerId, alertType, subject));
  },
  insertAlertEvent(routerId, alertType, subject, detail) {
    const id = nextId++;
    openKeys.add(_key(routerId, alertType, subject));
    inserted.push({ id, routerId, alertType, subject, detail });
    return id;
  },
  resolveAlertEvent(routerId, alertType, subject) {
    resolved.push({ routerId, alertType, subject });
    openKeys.delete(_key(routerId, alertType, subject));
    // Default: pretend one row was open, since that is the ordinary case.
    return resolveIds === null ? [nextId++] : resolveIds;
  },
  insertConnectivityEvent() {},
};

const routersStub = { getById: () => ({ id: 'r1', alertsEnabled: true }) };

function stub(relPath, exports) {
  const abs = require.resolve(path.join(__dirname, '..', 'src', relPath));
  require.cache[abs] = new Module(abs, null);
  require.cache[abs].filename = abs;
  require.cache[abs].loaded   = true;
  require.cache[abs].exports  = exports;
}
stub('db.js', dbStub);
stub('routers.js', routersStub);

const alerter = require('../src/alerter');

// No channel is enabled anywhere in here. That is the point: the common case is
// a user who has never configured Telegram, and the bell must still work.
const SETTINGS = {
  notifCpu: true, notifBgp: true, notifCooldownSec: 60,
  alertCpuThreshold: 80,
};

function harness(settings) {
  const emits = [];
  const io = {
    to(room) { return { emit: (ev, payload) => emits.push({ room, ev, payload }) }; },
    emit(ev, payload) { emits.push({ room: null, ev, payload }); },
  };
  inserted.length = 0; resolved.length = 0; nextId = 1; resolveIds = null;
  alerter.init(io, Object.assign({}, SETTINGS, settings || {}));
  const ev = alerter.createEvaluator(() => 'Home', () => ({ id: 'r1', alertsEnabled: true }));
  return { emits, ev };
}

const peer = (over) => Object.assign({
  key: 'p1', name: 'upstream', remoteAddr: '10.0.0.1',
  state: 'established', prefixes: 100, flapping: false, holdTime: 180, keepalive: 60,
}, over);

// ── the seam ────────────────────────────────────────────────────────────────

test('an alert reaches the bell with no push channel configured', () => {
  // The regression this guards: putting the emit below the _noChannelsActive()
  // return in fire() silences the bell for everyone without Telegram/ntfy/SMTP,
  // which is most people. The emit belongs with the unconditional DB write.
  const { emits, ev } = harness();
  ev.evaluate('system:update', { cpuLoad: 95 });

  const fired = emits.filter(e => e.ev === 'alert:fired');
  assert.equal(fired.length, 1, 'the bell must be told even with no channel enabled');
  assert.equal(fired[0].room, 'router-r1', 'the alert belongs to its router, not the fleet');
  assert.equal(fired[0].payload.alertType, 'high_cpu');
  assert.equal(fired[0].payload.routerName, 'Home');
  assert.equal(typeof fired[0].payload.id, 'number', 'the bell needs the row id to acknowledge it');
  assert.equal(fired[0].payload.resolvedAt, null);
  assert.equal(inserted.length, 1, 'and it must be recorded, not only broadcast');
});

test('a resolve names only the rows it actually closed', () => {
  // resolveAlertEvent returns the ids it updated. Emitting a resolve when it
  // closed nothing would tell the bell to strike through an alert that is still
  // open — the two systems disagreeing again, in a new place.
  const h = harness();
  h.ev.evaluate('system:update', { cpuLoad: 95 });
  resolveIds = [];
  h.ev.evaluate('system:update', { cpuLoad: 10 });
  assert.equal(h.emits.filter(e => e.ev === 'alert:resolved').length, 0,
    'nothing was open, so nothing may be announced as resolved');

  const h2 = harness();
  h2.ev.evaluate('system:update', { cpuLoad: 95 });
  resolveIds = [41, 42];
  h2.ev.evaluate('system:update', { cpuLoad: 10 });
  const done = h2.emits.filter(e => e.ev === 'alert:resolved');
  assert.equal(done.length, 1);
  assert.deepEqual(done[0].payload.ids, [41, 42]);
  assert.equal(done[0].payload.alertType, 'high_cpu',
    'the resolve must carry the DOWN alert type so the bell can match the open row');
});

// ── BGP rules ported from the browser ───────────────────────────────────────

test('a misconfigured BGP hold timer alerts once, not on every update', () => {
  // This is the bug the port fixes. In the browser this was gated on a 2-minute
  // cooldown, but hold-time=3s/keepalive=0 is static configuration — it never
  // stops being true, so the alert repeated every 2 minutes indefinitely.
  const { emits, ev } = harness();
  const bad = peer({ holdTime: 3, keepalive: 0 });
  for (let i = 0; i < 6; i++) ev.evaluate('routing:update', { peers: [bad] });

  const hold = inserted.filter(r => r.alertType === 'bgp_hold_timer_warning');
  assert.equal(hold.length, 1, 'a standing misconfiguration is worth exactly one alert');
  assert.equal(hold[0].subject, 'upstream', 'the peer name must land in subject');

  // ...and it clears when the configuration is fixed.
  ev.evaluate('routing:update', { peers: [peer()] });
  assert.equal(resolved.filter(r => r.alertType === 'bgp_hold_timer_warning').length, 1);
  assert.ok(emits.some(e => e.ev === 'alert:resolved' &&
                            e.payload.alertType === 'bgp_hold_timer_warning'));
});

test('BGP peer down and up are edge-triggered', () => {
  const { ev } = harness();
  ev.evaluate('routing:update', { peers: [peer()] });                    // baseline, silent
  assert.equal(inserted.length, 0, 'the first sighting is a baseline, not a transition');

  ev.evaluate('routing:update', { peers: [peer({ state: 'idle' })] });
  ev.evaluate('routing:update', { peers: [peer({ state: 'idle' })] });   // still down
  assert.equal(inserted.filter(r => r.alertType === 'bgp_peer_down').length, 1,
    'a peer that stays down is one alert, not one per poll');

  ev.evaluate('routing:update', { peers: [peer()] });
  assert.equal(resolved.filter(r => r.alertType === 'bgp_peer_down').length, 1);
});

test('a BGP prefix swing resolves once the count settles', () => {
  const { ev } = harness();
  ev.evaluate('routing:update', { peers: [peer({ prefixes: 100 })] });
  ev.evaluate('routing:update', { peers: [peer({ prefixes: 500 })] });   // +400%, fires
  assert.equal(inserted.filter(r => r.alertType === 'bgp_prefix_change').length, 1);

  ev.evaluate('routing:update', { peers: [peer({ prefixes: 505 })] });   // steady, resolves
  assert.equal(resolved.filter(r => r.alertType === 'bgp_prefix_change').length, 1);

  // A swing smaller than the threshold is not an alert at all.
  const h2 = harness();
  h2.ev.evaluate('routing:update', { peers: [peer({ prefixes: 100 })] });
  h2.ev.evaluate('routing:update', { peers: [peer({ prefixes: 110 })] });
  assert.equal(inserted.filter(r => r.alertType === 'bgp_prefix_change').length, 0);
});

test('a session bounce is not also counted as a prefix collapse', () => {
  // Prefixes are compared against the last ESTABLISHED reading. Comparing
  // against the last reading of any kind would make every peer-down alert drag
  // a bogus "-100% prefixes" alert along behind it.
  const { ev } = harness();
  ev.evaluate('routing:update', { peers: [peer({ prefixes: 100 })] });
  ev.evaluate('routing:update', { peers: [peer({ state: 'idle', prefixes: 0 })] });
  ev.evaluate('routing:update', { peers: [peer({ prefixes: 100 })] });
  assert.equal(inserted.filter(r => r.alertType === 'bgp_prefix_change').length, 0);
});

test('an install-wide alert-type toggle suppresses the event entirely', () => {
  // The install setting is authoritative: a type switched off is not recorded,
  // does not reach the bell, and is sent to nobody.
  //
  // #109 briefly made these gate delivery only, so a user could opt in to a type
  // the install had disabled. The cost showed up immediately in the Interface
  // Alert Filter: turning Wireless off silenced the push but still rang the bell
  // on every wlan flap, because the bell follows the DB write and the DB write
  // had stopped being gated. Per-user toggles narrow within what the install
  // allows; they cannot widen past it.
  const { ev, emits } = harness({ notifBgp: false });
  ev.evaluate('routing:update', { peers: [peer()] });
  ev.evaluate('routing:update', { peers: [peer({ state: 'idle' })] });
  assert.equal(inserted.length, 0, 'nothing may be recorded while the type is switched off');
  assert.equal(emits.filter(e => e.ev === 'alert:fired').length, 0,
    'and nothing may reach the bell — a filter that does not filter what you see is not a filter');
});

test('the interface-type filter applies install-wide, not only to push', () => {
  // The reported bug: Wireless unticked in the Interface Alert Filter, wlan
  // alerts still arriving in the notification bell.
  const { ev, emits } = harness({ notifIfaceUpDown: true, notifIfaceWlan: false, notifIfaceEther: true });
  const ifaces = (wlanRunning) => ({ interfaces: [
    { name: 'wlan1',  type: 'wlan',  running: wlanRunning, disabled: false },
    { name: 'ether1', type: 'ether', running: true,        disabled: false },
  ] });

  ev.evaluate('ifstatus:update', ifaces(true));   // seed previous state
  ev.evaluate('ifstatus:update', ifaces(false));  // wlan1 goes down

  assert.equal(inserted.filter(r => r.subject === 'wlan1').length, 0,
    'a filtered-out interface type must not be recorded');
  assert.equal(emits.filter(e => e.ev === 'alert:fired').length, 0,
    'nor reach the bell');
});

test('an interface type that is still enabled continues to alert', () => {
  // The other half: the filter must narrow, not silence everything.
  const { ev } = harness({ notifIfaceUpDown: true, notifIfaceWlan: false, notifIfaceEther: true });
  const ifaces = (etherRunning) => ({ interfaces: [
    { name: 'ether1', type: 'ether', running: etherRunning, disabled: false },
  ] });

  ev.evaluate('ifstatus:update', ifaces(true));
  ev.evaluate('ifstatus:update', ifaces(false));

  assert.equal(inserted.filter(r => r.subject === 'ether1').length, 1,
    'ethernet is still ticked, so it must still alert');
});

// ── {{comment}} must not leak into what is recorded (issue #116) ─────────────
//
// The comment was added as a notification template variable only. The bell, the
// alerts history table and the PDF export all render the stored `detail`, and
// the open/resolve pairing keys off `subject`. Both must be exactly what they
// were before, or a feature meant to change one optional line of a push message
// has quietly changed three other surfaces.

const IFACE_ON = { notifIfaceUpDown: true, notifIfaceEther: true };
const eth = (running, comment) => [{ name: 'ether1', type: 'ether', running, disabled: false, comment }];

test('a comment changes the notification and nothing that is recorded', () => {
  // harness() resets inserted/resolved but deliberately not openKeys, and an
  // earlier test in this file leaves ether1 open. Clearing it here keeps that
  // shared state out of these assertions without changing what the tests
  // above are asserting.
  openKeys.clear();
  const { ev } = harness(IFACE_ON);
  ev.evaluate('ifstatus:update', { interfaces: eth(true,  'Uplink to ISP') });
  ev.evaluate('ifstatus:update', { interfaces: eth(false, 'Uplink to ISP') });

  assert.equal(inserted.length, 1);
  assert.equal(inserted[0].alertType, 'interface_down');
  assert.equal(inserted[0].subject,   'ether1', 'subject stays the bare interface name');
  assert.equal(inserted[0].detail,    'ether1 went down', 'detail is untouched — the bell and PDF render this');
});

test('editing a comment between the down and the up still resolves the alert', () => {
  // The exact failure mode of the obvious wrong implementation: folding the
  // comment into `subject` to get a richer bell entry. That would key the down
  // row on "ether1 (A)" while the up event looked for "ether1 (B)", so the
  // alert would never close and the bell would accumulate forever. An operator
  // relabelling a port between an outage and its recovery is an ordinary
  // Tuesday, not a contrived case.
  openKeys.clear();
  const { ev } = harness(IFACE_ON);
  ev.evaluate('ifstatus:update', { interfaces: eth(true,  'A') });
  ev.evaluate('ifstatus:update', { interfaces: eth(false, 'A') });
  ev.evaluate('ifstatus:update', { interfaces: eth(true,  'B') });

  assert.equal(inserted.length, 1);
  assert.equal(resolved.length, 1);
  assert.equal(resolved[0].alertType, 'interface_down');
  assert.equal(resolved[0].subject,   'ether1');
  assert.equal(openKeys.size, 0, 'nothing left open');
});

// ── A later RouterOS release still notifies (ToDo §7) ───────────────────────
//
// prevUpdateVersion is keyed on the version rather than a boolean, and its
// comment said that means "a later release still notifies instead of being
// swallowed as 'already alerting'". It did not. Both alerts carry alertType
// `routeros_update` and a null subject, so with the first still open the
// version check passed and fire() then returned at the hasOpenAlert guard. A
// router left un-updated across two releases was told about the first only.
//
// These drive createEvaluator directly, which is the only way to see it: the
// version check and the guard are twenty lines apart and both look right.
const UPD = (latest, running) => ({ updateAvailable: true, latestVersion: latest, version: running });

test('a second release fires while the first alert is still open', () => {
  openKeys.clear();
  const { ev } = harness({ notifRouterUpdate: true });

  ev.evaluate('system:update', UPD('7.19', '7.18'));
  ev.evaluate('system:update', UPD('7.20', '7.18'));

  const updates = inserted.filter(r => r.alertType === 'routeros_update');
  assert.equal(updates.length, 2, 'the newer release must be recorded, not swallowed');
  assert.match(updates[1].detail, /7\.20/, 'the second alert names the newer version');
});

test('the superseded alert is closed rather than left open', () => {
  // Superseding rather than versioning the subject is deliberate: the recovery
  // event resolves on (routeros_update, null), so a versioned subject would
  // match nothing and every update alert would stay open forever. The cost of
  // that choice is that the stale row must be closed explicitly, which is what
  // this pins.
  openKeys.clear();
  const { emits, ev } = harness({ notifRouterUpdate: true });

  ev.evaluate('system:update', UPD('7.19', '7.18'));
  ev.evaluate('system:update', UPD('7.20', '7.18'));

  const closed = resolved.filter(r => r.alertType === 'routeros_update');
  assert.equal(closed.length, 1, 'exactly the one stale row is closed');
  assert.equal(closed[0].subject, null, 'closed on the key the recovery path also uses');

  // The bell has to hear about it too, or the browser keeps showing the old one
  // alongside the new.
  const res = emits.filter(e => e.ev === 'alert:resolved');
  assert.ok(res.length >= 1, 'the browser must be told the old advisory closed');
});

test('the same release does not fire twice', () => {
  // The behaviour the version keying was there for in the first place, and what
  // supersede must not break: an update that simply persists says nothing new.
  openKeys.clear();
  const { ev } = harness({ notifRouterUpdate: true });

  ev.evaluate('system:update', UPD('7.19', '7.18'));
  ev.evaluate('system:update', UPD('7.19', '7.18'));
  ev.evaluate('system:update', UPD('7.19', '7.18'));

  assert.equal(inserted.filter(r => r.alertType === 'routeros_update').length, 1);
});

test('a rebuilt evaluator does not re-announce an update it already filed', () => {
  // THE TRAP IN THE FIX. prevUpdateVersion is in-memory, so a rebuilt evaluator
  // starts at null and every open alert looks like a new release. Superseding
  // unconditionally would ring the bell on every rebuild, which is precisely
  // the failure the hasOpenAlert guard exists to prevent — so supersede is only
  // set when this evaluator actually saw an earlier version.
  openKeys.clear();
  const first = harness({ notifRouterUpdate: true });
  first.ev.evaluate('system:update', UPD('7.19', '7.18'));
  assert.equal(inserted.filter(r => r.alertType === 'routeros_update').length, 1);

  // Same open row, brand new evaluator, same version still available.
  const second = harness({ notifRouterUpdate: true });
  second.ev.evaluate('system:update', UPD('7.19', '7.18'));

  assert.equal(inserted.filter(r => r.alertType === 'routeros_update').length, 0,
    'a rebuild must stay silent about an alert that is already open');
  assert.equal(resolved.filter(r => r.alertType === 'routeros_update').length, 0,
    'and must not close it either');
});
