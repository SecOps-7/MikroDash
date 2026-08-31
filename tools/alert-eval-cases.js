'use strict';
/**
 * The ALERT EVALUATOR's rules — what fires, and when it does not.
 *
 * `tools/alerter-cases.js` covers the alerter's four PURE HELPERS and says of
 * this: "the evaluator, the cooldown maps and the delivery are not ported: they
 * hold per-router state and they send." This file takes the first of those
 * three. The delivery stays unported, and `internal/notify/send.go` records why.
 *
 * ── EDGE DETECTION, NOT LEVEL DETECTION, AND THAT IS THE WHOLE THING ────────
 *
 * Every rule compares a reading against a threshold AND against its own previous
 * verdict, firing only on the TRANSITION. A port that fired on the level would
 * alert on every poll — `system:update` runs about every two seconds — and the
 * operator would be paged continuously while a CPU stayed busy. The state is a
 * closure of Maps and scalars, so the decision is
 * `(settings, prior events, this event) -> what fires`, which a corpus can
 * record exactly.
 *
 * ── LIFTED AND RUN, WITH ONLY THE DELIVERY STUBBED ──────────────────────────
 *
 * `createEvaluator` is sliced out and evaluated. What is replaced is the layer
 * BELOW the rules — `_deliver`, `_emit`, `_recipients`, `_ts` and the three `db`
 * calls — so `fire()` itself runs intact. That matters: `fire` holds the
 * install-wide type toggles, and its own comment records the bug that appeared
 * when they were moved elsewhere ("a filter that does not filter what you are
 * looking at is not a filter"). Stubbing `fire` would take that out of the
 * corpus.
 *
 * ── COVERAGE IS PARTIAL AND SAID SO ─────────────────────────────────────────
 *
 * `evaluate` is ~270 lines across several rule families. This corpus covers the
 * one that is a pure function of a single reading — `system:update`, carrying
 * both the CPU and the update-check rules — plus the global gates. The
 * interface, VPN, netwatch, ping and BGP families key off maps of per-peer state
 * and want their own cases; they are NOT covered, and the family list is read
 * from the SOURCE and asserted, so a new one cannot be quietly forgotten. A
 * clean run here does not mean "the evaluator is pinned".
 *
 * ── NOTHING HERE IS REAL ────────────────────────────────────────────────────
 *
 * Synthetic thresholds and version strings.
 *
 *   node tools/alert-eval-cases.js            write testdata/alert-eval-cases.json
 *   node tools/alert-eval-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const OUT = process.env.ALERT_EVAL_OUT || path.join(ROOT, 'testdata', 'alert-eval-cases.json');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const src = fs.readFileSync(path.join(LIVE, 'src', 'alerter.js'), 'utf8');

// ---- THE SLICE -----------------------------------------------------------
const OPEN = 'function createEvaluator(getNameFn, getRouterFn) {';
{
  const n = src.split(OPEN).length - 1;
  assert.equal(n, 1, 'the createEvaluator anchor is ambiguous (' + n + ' matches)');
}
const from = src.indexOf(OPEN);
const open = from + OPEN.length - 1;
assert.equal(src[open], '{', 'the anchor no longer ends at the body brace');
let depth = 0, to = -1;
for (let i = open; i < src.length; i++) {
  if (src[i] === '{') depth++;
  else if (src[i] === '}') { depth--; if (!depth) { to = i + 1; break; } }
}
assert.ok(to > from, 'createEvaluator is unbalanced');
const body = src.slice(from, to);

// ── THE INTERFACE CLASSIFIER IS LIFTED, NOT STUBBED ─────────────────────────
//
// `_ifaceType` and `_ifaceTypeKey` decide WHICH toggle gates an interface alert,
// so they are part of the rule rather than below it. Stubbing them would take
// the type filter out of the corpus, which is half of what the interface family
// does. (`tools/alerter-cases.js` pins the two functions themselves; this lifts
// them so the rule that CALLS them can be exercised.)
function liftFn(decl) {
  const i = src.indexOf(decl);
  assert.ok(i >= 0, 'cannot find ' + decl + ' in the live alerter');
  let depth = 0;
  for (let j = src.indexOf('{', i); j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}') { depth--; if (!depth) return src.slice(i, j + 1); }
  }
  throw new Error('unbalanced ' + decl);
}
// `_capMap` AND `STATE_MAX` MOVED TO MODULE SCOPE upstream (`7185a92`), and
// this generator broke the moment they did -- `_capMap is not defined`, because
// the slice lifts `createEvaluator`'s BODY and they had left it.
//
// That is the gate working, and the move was made for a reason worth recording
// here rather than treating as churn: while `_capMap` lived inside the closure
// the only route to it was through alert counts, so every test written about it
// was really about its consequences. Upstream measured that replacing its body
// with `return` left all 1615 of their tests passing. Extracting it is what made
// the rule checkable at all -- the same conclusion this port reached from the
// other side, where the corpus could not see the prune and it took a direct Go
// test on the map.
const helpers = [
  'const STATE_MAX = ' + (/const STATE_MAX\s*=\s*(\d+)/.exec(src) || [])[1] + ';',
  liftFn('function _capMap(m, live) {'),
  liftFn('function _ifaceType(name, type) {'),
  liftFn('function _ifaceTypeKey(type) {'),
].join('\n');
// The bound is READ, never assumed: the 500/501 pair below is meaningless if the
// number here and the number upstream disagree, and they would disagree
// silently.
assert.ok(/const STATE_MAX\s*=\s*500\b/.test(src),
  'STATE_MAX is no longer 500 upstream -- the cap cases are built around that exact boundary '
  + 'and must be rebuilt, not merely regenerated');

for (const marker of ['prevCpuAlert', 'prevUpdateVersion', 'function fire(', 'return { evaluate }']) {
  assert.ok(body.includes(marker),
    'the lifted evaluator has no ' + marker + ' -- the slice stopped early, and this corpus '
    + 'would then record the answers of a smaller machine than the app runs');
}

// ── WHICH RULE FAMILIES EXIST, read from the source rather than remembered ──
const families = [...new Set(
  [...body.matchAll(/event === '([a-z]+:[a-z]+)'/g)].map((m) => m[1]),
)].sort();
const COVERED = ['system:update', 'ping:update', 'netwatch:update', 'ifstatus:update',
  'vpn:update', 'routing:update'];
// EMPTY, and the assertion below insists it stay meaningful rather than
// decorative: every family the evaluator has is now covered. An entry added here
// must carry a reason, and one whose family disappears fails.
const UNCOVERED = {};
{
  const unknown = families.filter((f) => !COVERED.includes(f) && !UNCOVERED[f]);
  assert.deepEqual(unknown, [],
    'rule families in the live evaluator that this corpus neither covers nor records as '
    + 'uncovered: ' + unknown.join(', ') + '. Add cases or add a reason -- a family nobody '
    + 'listed reads as covered.');
  const goneCovered = COVERED.filter((f) => !families.includes(f));
  assert.deepEqual(goneCovered, [],
    'this corpus claims to cover a family the evaluator no longer has: ' + goneCovered.join(', '));
  const goneUncovered = Object.keys(UNCOVERED).filter((f) => !families.includes(f));
  assert.deepEqual(goneUncovered, [],
    'UNCOVERED records a family the evaluator no longer has: ' + goneUncovered.join(', ')
    + ' -- delete the entry rather than leaving a reason past its question');
}

// ---- THE HARNESS ---------------------------------------------------------
// `openAlerts` PRE-SEEDS THE STORE, which is how a RESTART is modelled: the
// database outlives the process, so a rebuilt evaluator meets rows it did not
// file and has no memory of. Every other case starts empty, and that emptiness
// hid the whole supersede question -- a first announcement finds nothing open,
// so whether it would have superseded is never asked.
function runLive(settings, events, router, openAlerts) {
  const fired = [];
  // Live cooldown keys, one per delivered alert — see the _deliver stub.
  const keys = [];
  const open = [];
  let seq = 0;
  for (const a0 of openAlerts || []) {
    open.push({ id: ++seq, routerId: (router && router.id) || 'r1', ...a0 });
  }
  // ── THE CLOCK IS PINNED, NOT THE FIELD DROPPED ──────────────────────────
  //
  // `fire` stamps `firedAt` with `Date.now()`, so two runs produced two corpora
  // and `--check` called a freshly written file stale. The obvious fix is to
  // strip the field; pinning the clock is better, because `firedAt` stays
  // COMPARED — a port that stamped the wrong thing, or stamped at the wrong
  // point, would still be caught. A corpus whose --check flips on the wall clock
  // is not a gate, which this repo has already recorded once for the alertwrite
  // corpus.
  const FIXED_NOW = 1700000000000;
  function FakeDate(...a) { return new Date(...a); }
  FakeDate.now = () => FIXED_NOW;

  const ctx = {
    Map, Object, Array, String, Number, Math, JSON,
    Date: FakeDate,
    _settings: settings,
    // Below the rules. `fire` itself is NOT stubbed -- see the header.
    //
    // ── `_deliver` RECORDS ITS KEY, AND `_recipients` RETURNS ONE ─────────
    //
    // `_recipients` returned `[]`, so the delivery loop never ran and the
    // COOLDOWN KEY -- `fire`'s own first argument, `iface:ether1:down` -- was
    // never observed. The Go port had to derive an equivalent from what the
    // fired alert carries, which is right in every case but netwatch, where live
    // keys on the host's RouterOS `.id` and the port has only its name.
    //
    // One stub recipient makes the loop run. It changes nothing recorded: the
    // corpus's alerts come from `_emit`, and `_deliver` is inert apart from
    // noting the key. The key is what turns the port's derivation from an
    // approximation into something checkable.
    _deliver: (_cooldown, _recipient, subjectKey) => { keys.push(subjectKey); },
    _emit: (routerId, event, payload) => { fired.push({ event, payload }); },
    _recipients: () => [{ id: '_install', settings: {} }],
    _ts: () => 0,
    _render: (tpl) => tpl,
    _hasChannel: () => false,
    labelFor: (t) => t,
    // ── A REAL LITTLE STORE, NOT FLAT STUBS ─────────────────────────────
    //
    // `hasOpenAlert: () => false` and `resolveAlertEvent: () => {}` were the
    // first version, and they made the corpus describe the STUB rather than the
    // rules: a recovery never emitted, because there was never an open alert to
    // resolve. `fire` genuinely consults this state — it returns early when an
    // alert is already open, which is the DEDUPLICATION half of the rules and is
    // worth recording. So the three calls share one in-memory table.
    db: {
      hasOpenAlert: (routerId, type, subject) => open.some(
        (a2) => a2.routerId === routerId && a2.type === type && a2.subject === subject),
      insertAlertEvent: (routerId, type, subject) => {
        open.push({ id: ++seq, routerId, type, subject });
        return seq;
      },
      resolveAlertEvent: (routerId, type, subject) => {
        const hit = open.filter(
          (a2) => a2.routerId === routerId && a2.type === type && a2.subject === subject);
        for (const h of hit) open.splice(open.indexOf(h), 1);
        return hit.map((h) => h.id);
      },
    },
    module: { exports: {} },
  };
  vm.createContext(ctx);
  vm.runInContext(helpers + '\n' + body + '\nmodule.exports = createEvaluator;', ctx);

  const ev = ctx.module.exports(() => 'Test Router', () => router);
  for (const e of events) ev.evaluate(e.event, e.data);
  // ONE DELIVERY PER EMITTED ALERT, with one recipient. If these ever diverge
  // the pairing below is wrong and the keys would be attributed to the wrong
  // alerts, which is worse than not recording them.
  // ── KEYS ARE A LIST, NOT PAIRED BY INDEX ─────────────────────────────────
  //
  // The first version paired `keys[i]` to `fired[i]` and asserted equal lengths.
  // It failed immediately, and the failure was the point: the update-supersede
  // case EMITS three alerts and DELIVERS two. Live's supersede resolution goes
  // through `_emit` and never reaches the delivery loop, so an index pairing
  // would have attached the wrong key to the wrong alert.
  //
  // So the deliveries are recorded in order, and the Go side matches them
  // against the alerts it would actually SEND — which is what taught the port
  // that `Fired.Silent` had to exist.
  return { fired, cooldownKeys: keys };
}

// THE TOGGLE KEYS ARE THE LIVE ONES, and the first version got the second wrong
// -- `notifUpdate` instead of `notifRouterUpdate`. Every update case then fired
// nothing, because `fire` returns early on a toggle it cannot find set. The
// believability block caught it; a corpus without one would have recorded
// "the update alert never fires" as the live behaviour.
const SETTINGS = {
  notifVpn: true,
  notifIfaceUpDown: true,
  notifIfaceEther: true,
  notifIfaceWlan: true,
  notifIfaceBridge: true,
  notifIfaceVlan: true,
  notifIfaceOther: true,
  notifNetwatch: true,
  alertPingLoss: 20,
  notifPing: true,
  alertCpuThreshold: 80,
  notifCpu: true,
  notifRouterUpdate: true,
  notifBgp: true,
  alertsEnabled: true,
};
// `id` IS REQUIRED, not decoration: `fire` guards its whole record-and-emit
// block on `if (router && router.id)`. A fixture router without one evaluates
// every rule and emits nothing, which reads as "no alert fired" for every case.
const ROUTER = { id: 'r1', alertsEnabled: true };

// ---- THE CASES -----------------------------------------------------------
const up = (cpu) => ({ event: 'system:update', data: { cpuLoad: cpu } });
const png = (target, loss) => ({ event: 'ping:update', data: { target, loss } });
const nw = (...hosts) => ({ event: 'netwatch:update', data: { hosts } });
const vpn = (...tunnels) => ({ event: 'vpn:update', data: { tunnels } });
const tun = (name, state) => ({ name, state });
const ifs = (...interfaces) => ({ event: 'ifstatus:update', data: { interfaces } });
const iface = (name, running, extra) => ({ name, running, ...(extra || {}) });
const rt = (...peers) => ({ event: 'routing:update', data: { peers } });
// `key` defaults to the name because every rule keys its memory on it and a
// blank one is skipped outright. Everything else is left ABSENT unless a case
// asks for it, so the guards on `typeof prefixes === 'number'` and on a strict
// `keepalive === 0` are exercised rather than papered over with zeros.
const bpeer = (name, state, extra) => ({ key: name, name, state, ...(extra || {}) });
const host = (id, status, extra) => ({ id, host: '10.0.0.' + id, status, ...(extra || {}) });
const ver = (v, avail) => ({
  event: 'system:update',
  data: { updateAvailable: avail !== false, latestVersion: v },
});

const CASES = {
  // ── CPU: the edge, in both directions ───────────────────────────────────
  cpuStaysNormal: { events: [up(10), up(20), up(30)] },
  // ONE alert, not three. This is the case that separates edge detection from
  // level detection, and system:update runs about every two seconds.
  cpuGoesHighAndStaysHigh: { events: [up(10), up(90), up(91), up(92)] },
  cpuRecovers: { events: [up(90), up(10)] },
  cpuFlaps: { events: [up(90), up(10), up(90), up(10)] },
  // The FIRST reading has no previous verdict. `prevCpuAlert` starts null, so a
  // first reading above the threshold must alert; a port initialising it to
  // false would agree, and one initialising it to TRUE would go silent.
  firstReadingIsAlreadyHigh: { events: [up(95)] },
  firstReadingIsNormal: { events: [up(5)] },
  // The BOUNDARY is >=, so exactly the threshold alerts.
  cpuExactlyAtTheThreshold: { events: [up(80)] },
  cpuJustBelow: { events: [up(79)] },
  // A reading that is not a number is IGNORED -- and must not reset the state.
  aNonNumericReadingIsSkipped: {
    events: [up(90), { event: 'system:update', data: { cpuLoad: null } }, up(91)],
  },

  // ── THE UPDATE CHECK: keyed on the VERSION, not a boolean ───────────────
  updateAnnouncedOnce: { events: [ver('7.15'), ver('7.15'), ver('7.15')] },
  // ── SUPERSEDE: A LATER RELEASE REPLACES THE OPEN ANNOUNCEMENT ─────────
  //
  // This corpus recorded the OPPOSITE until 2026-08-27. The version check passed
  // and `fire` returned at `db.hasOpenAlert`, so a router left un-updated across
  // two releases was only ever told about the first — contradicting the
  // version-keying comment. Filed as ToDo.md §7 and FIXED upstream: the down
  // path now resolves the stale row before filing the new one.
  //
  // Not fixed by versioning the `subject`, which is what the ToDo entry
  // suggested: the recovery resolves on `(routeros_update, null)`, so a
  // versioned subject would match nothing and every update alert would stay open
  // for ever. The alert key and the resolution key are the same key.
  aLaterReleaseWhileTheFirstIsOpen: { events: [ver('7.15'), ver('7.16')] },
  // ...and the SUPERSEDE IS CONDITIONAL. `prevUpdateVersion` is in-memory, so a
  // rebuilt evaluator starts at null and every open alert would look like a new
  // release — ringing the bell on every rebuild, which is the failure
  // `hasOpenAlert` exists to prevent. A FIRST announcement never supersedes.
  aFirstAnnouncementDoesNotSupersede: { events: [ver('7.15')] },
  // ...AND THAT IS ONLY VISIBLE AGAINST A STORE THAT ALREADY HOLDS THE ROW.
  //
  // The case above starts empty, so nothing is open, the dedup guard never runs
  // and the supersede flag is never consulted -- it records the right answer for
  // the wrong reason. THIS is the restart: the database outlives the process, so
  // the row is there and `prevUpdateVersion` is not. An evaluator that
  // superseded unconditionally would resolve and re-file the operator's
  // acknowledged alert on every restart.
  aRestartMeetingAnOpenUpdateAlert: {
    events: [ver('7.15')],
    openAlerts: [{ type: 'routeros_update', subject: null }],
  },
  // The believability twin: the SAME open row, but with an earlier version
  // observed first, DOES supersede. Without this the case above passes against
  // an evaluator that never supersedes at all.
  aRestartThenTwoReleases: {
    events: [ver('7.15'), ver('7.16')],
    openAlerts: [{ type: 'routeros_update', subject: null }],
  },
  // ...and once the router UPDATES, the alert resolves and the next release
  // does notify. This is the sequence the comment describes.
  aLaterReleaseAfterTheFirstResolves: {
    events: [ver('7.15'), ver('', false), ver('7.16')],
  },
  noUpdateAvailable: { events: [ver('7.15', false)] },
  // ── THE RUNNING VERSION IS CLEANED, and nothing exercised that ────────
  //
  // `(data.version || '').replace(/\s*\(.*\)/, '').trim() || 'unknown'` strips
  // the channel suffix a RouterOS version carries. Added after the Go port was
  // written WITHOUT it and only the resolve case failed -- the announce case had
  // no `version` at all, so both sides said "unknown" and agreed by accident.
  updateShowsTheRunningVersion: {
    events: [{ event: 'system:update',
      data: { updateAvailable: true, latestVersion: '7.16', version: '7.14.3 (stable)' } }],
  },
  // The regex is GREEDY and unanchored: everything from the first bracket to the
  // last goes, along with the whitespace before it.
  aVersionWithTwoBracketedParts: {
    events: [{ event: 'system:update',
      data: { updateAvailable: true, latestVersion: '7.16', version: '7.14 (a) x (b)' } }],
  },
  updateAvailableWithNoVersion: {
    events: [{ event: 'system:update', data: { updateAvailable: true, latestVersion: '' } }],
  },

  // ── PING: THE SAME EDGE, KEYED PER TARGET ───────────────────────────────
  pingStaysClean: { events: [png('1.1.1.1', 0), png('1.1.1.1', 5)] },
  pingGoesLossyAndStaysLossy: {
    events: [png('1.1.1.1', 0), png('1.1.1.1', 50), png('1.1.1.1', 60)],
  },
  pingRecovers: { events: [png('1.1.1.1', 50), png('1.1.1.1', 0)] },
  // INDEPENDENT PER TARGET. One target going lossy must not silence or fire for
  // another -- the state is an object keyed on the target, and a port holding a
  // single flag would alert once and then go quiet for the whole fleet.
  twoTargetsAreIndependent: {
    events: [png('1.1.1.1', 50), png('8.8.8.8', 0), png('8.8.8.8', 60)],
  },
  // TWO TARGETS LOSSY AT ONCE. Added after a mutation SURVIVED: sharing one
  // state key across targets is invisible while only one is lossy, because
  // `emit`'s dedup is keyed on the SUBJECT and still separates them. It shows
  // here — with a shared key the second target sees "already alerting" and never
  // fires.
  twoTargetsBothLossy: { events: [png('1.1.1.1', 50), png('8.8.8.8', 60)] },
  // An EMPTY target is not the same as an absent one: the key falls back to
  // "host" for both, and nothing exercised the empty string until a mutation
  // treating them differently survived.
  pingWithAnEmptyTarget: {
    events: [{ event: 'ping:update', data: { target: '', loss: 50 } }],
  },
  // The BOUNDARY is >=.
  pingExactlyAtTheThreshold: { events: [png('1.1.1.1', 20)] },
  pingJustBelow: { events: [png('1.1.1.1', 19)] },
  // A non-numeric loss is skipped WITHOUT disturbing the state.
  aNonNumericLossIsSkipped: {
    events: [png('1.1.1.1', 50),
      { event: 'ping:update', data: { target: '1.1.1.1', loss: null } },
      png('1.1.1.1', 60)],
  },
  // `rtt` ABSENT reads as 'N/A' rather than being omitted.
  pingWithNoRtt: {
    events: [{ event: 'ping:update', data: { target: '1.1.1.1', loss: 50 } }],
  },
  pingWithAnRtt: {
    events: [{ event: 'ping:update', data: { target: '1.1.1.1', loss: 50, rtt: 12 } }],
  },
  // ── AND THE TWO FALLBACKS THAT DISAGREE ─────────────────────────────────
  //
  // The state KEY is `data.target || 'host'`, the subject is `data.target || ''`,
  // and the detail interpolates `data.target` RAW. With no target at all those
  // are three different answers from one reading, and the detail says
  // "undefined". Recorded, not tidied.
  pingWithNoTarget: {
    events: [{ event: 'ping:update', data: { loss: 50 } }],
  },
  pingTypeToggledOff: { events: [png('1.1.1.1', 50)], settings: { notifPing: false } },

  // ── NETWATCH: TWO RULES THAT DIFFER FROM PING ───────────────────────────
  //
  // 1. THE FIRST OBSERVATION NEVER FIRES. The guard is `prev !== undefined`, so
  //    a host that is already down when the page loads is recorded silently.
  //    CPU and ping both alert on a first reading; this does not, and a port
  //    reusing their shape would page on every reconnect.
  netwatchFirstSightDown: { events: [nw(host('1', 'down'))] },
  netwatchFirstSightUp: { events: [nw(host('1', 'up'))] },

  netwatchGoesDown: { events: [nw(host('1', 'up')), nw(host('1', 'down'))] },
  netwatchRecovers: {
    events: [nw(host('1', 'up')), nw(host('1', 'down')), nw(host('1', 'up'))],
  },
  netwatchStaysDown: {
    events: [nw(host('1', 'up')), nw(host('1', 'down')), nw(host('1', 'down'))],
  },

  // 2. `unknown` IS SKIPPED AND DOES NOT UPDATE THE STATE. The `continue` jumps
  //    the `set` at the bottom of the loop as well, so a transient re-probe
  //    leaves the previous status intact -- it does not become the new baseline
  //    and does not make the next real reading look like a transition.
  netwatchUnknownIsSkipped: {
    events: [nw(host('1', 'up')), nw(host('1', 'unknown')), nw(host('1', 'down'))],
  },
  // ...and an unknown as the FIRST sighting leaves the host unseen, so the next
  // reading is still a first sighting and still silent.
  netwatchUnknownFirstStaysUnseen: {
    events: [nw(host('1', 'unknown')), nw(host('1', 'down'))],
  },

  // The description: `name (host)` when a name differs, the bare host otherwise.
  netwatchNamedHost: {
    events: [nw(host('1', 'up', { name: 'Gateway' })), nw(host('1', 'down', { name: 'Gateway' }))],
  },
  netwatchNameEqualsHost: {
    events: [nw(host('1', 'up', { name: '10.0.0.1' })),
      nw(host('1', 'down', { name: '10.0.0.1' }))],
  },

  // Independent per host id, and BOTH in one event.
  netwatchTwoHostsInOneEvent: {
    events: [nw(host('1', 'up'), host('2', 'up')), nw(host('1', 'down'), host('2', 'down'))],
  },
  netwatchTypeToggledOff: {
    events: [nw(host('1', 'up')), nw(host('1', 'down'))], settings: { notifNetwatch: false },
  },

  // ── INTERFACES: THE ADMIN-TOGGLE SUPPRESSION IS THE POINT ───────────────
  ifaceFirstSightDown: { events: [ifs(iface('ether1', false))] },
  ifaceGoesDown: { events: [ifs(iface('ether1', true)), ifs(iface('ether1', false))] },
  ifaceRecovers: {
    events: [ifs(iface('ether1', true)), ifs(iface('ether1', false)), ifs(iface('ether1', true))],
  },
  ifaceStaysDown: {
    events: [ifs(iface('ether1', true)), ifs(iface('ether1', false)),
      ifs(iface('ether1', false))],
  },

  // AN ADMIN DISABLING AN INTERFACE ALSO STOPS IT RUNNING, and that used to fire
  // "Interface Down". The live comment: the disabled flag "was already being
  // captured for exactly this purpose and then never read".
  ifaceDisabledByAnAdmin: {
    events: [ifs(iface('ether1', true)),
      ifs(iface('ether1', false, { disabled: true }))],
  },
  // ...and the RECOVERY is suppressed too, because `prev.disabled` counts. An
  // admin re-enabling it must not produce an unpaired "Interface Up".
  ifaceReEnabledByAnAdmin: {
    events: [ifs(iface('ether1', true)),
      ifs(iface('ether1', false, { disabled: true })),
      ifs(iface('ether1', true))],
  },

  // AN ADMIN DISABLING AN INTERFACE THAT IS ALREADY ALERTING. Added after a
  // mutation survived: dropping `prev.disabled` is invisible in the simple
  // re-enable sequence, because the "down" was suppressed so there is no open
  // alert for the "up" to resolve and `emit` drops it anyway. With a REAL down
  // first there IS one, and the mutant resolves it -- an "Interface Up" for an
  // interface an admin turned back on, which is exactly the unpaired recovery
  // the flag exists to prevent.
  ifaceDisabledWhileAlreadyDown: {
    events: [ifs(iface('ether1', true)),
      ifs(iface('ether1', false)),
      ifs(iface('ether1', false, { disabled: true })),
      ifs(iface('ether1', true))],
  },

  // ── TWO TOGGLES GATE EACH ALERT: the feature and the TYPE FILTER ────────
  ifaceFeatureToggledOff: {
    events: [ifs(iface('ether1', true)), ifs(iface('ether1', false))],
    settings: { notifIfaceUpDown: false },
  },
  // The type is derived from the NAME when no type is given -- `ether1` is
  // ether, so switching the ether filter off silences it while the feature
  // stays on.
  ifaceTypeFilterOff: {
    events: [ifs(iface('ether1', true)), ifs(iface('ether1', false))],
    settings: { notifIfaceEther: false },
  },
  // ...and a DIFFERENT type's filter does not.
  anotherTypesFilterOff: {
    events: [ifs(iface('ether1', true)), ifs(iface('ether1', false))],
    settings: { notifIfaceWlan: false },
  },
  // An explicit type WINS over the name.
  ifaceExplicitTypeWins: {
    events: [ifs(iface('ether1', true, { type: 'wlan' })),
      ifs(iface('ether1', false, { type: 'wlan' }))],
    settings: { notifIfaceWlan: false },
  },

  ifaceTwoInOneEvent: {
    events: [ifs(iface('ether1', true), iface('ether2', true)),
      ifs(iface('ether1', false), iface('ether2', false))],
  },

  // ── VPN: THREE STATES, TWO OF WHICH MEAN THE SAME THING ─────────────────
  //
  // `peerState` emits 'active' | 'stale' | 'never'. Only 'active' is connected,
  // so a change BETWEEN the two disconnected values is not a transition. The
  // live comment records what happened when this consumer fell out of step with
  // the collector: it used to compare against 'connected'/'idle', so after the
  // rename "wasConn and isConn were both permanently false and VPN alerts could
  // not fire at all". These cases pin the string.
  vpnFirstSightActive: { events: [vpn(tun('peer1', 'active'))] },
  vpnFirstSightStale: { events: [vpn(tun('peer1', 'stale'))] },
  vpnDisconnects: { events: [vpn(tun('peer1', 'active')), vpn(tun('peer1', 'stale'))] },
  vpnReconnects: {
    events: [vpn(tun('peer1', 'active')), vpn(tun('peer1', 'stale')),
      vpn(tun('peer1', 'active'))],
  },
  // 'never' is ALSO disconnected -- a port that compared against 'stale' alone
  // would miss this one entirely.
  vpnGoesToNever: { events: [vpn(tun('peer1', 'active')), vpn(tun('peer1', 'never'))] },
  // THE STATE CHANGES AND THE CONNECTEDNESS DOES NOT. A port comparing
  // `prev !== state` fires here; the live rule does not.
  vpnStaleToNever: { events: [vpn(tun('peer1', 'stale')), vpn(tun('peer1', 'never'))] },
  vpnStaysActive: { events: [vpn(tun('peer1', 'active')), vpn(tun('peer1', 'active'))] },
  // A PEER THAT HAS NEVER CONNECTED, CONNECTING. Added after a mutation
  // survived: comparing `prev !== 'stale'` instead of `prev === 'active'` agrees
  // on every sequence that starts from 'active' or 'stale', and only differs
  // when the previous state was 'never' -- which is a peer's FIRST successful
  // handshake, not an edge case.
  vpnNeverToActive: { events: [vpn(tun('peer1', 'never')), vpn(tun('peer1', 'active'))] },
  // ...and never -> stale is still two disconnected states, so nothing fires.
  vpnNeverToStale: { events: [vpn(tun('peer1', 'never')), vpn(tun('peer1', 'stale'))] },

  vpnTwoPeersIndependent: {
    events: [vpn(tun('peer1', 'active'), tun('peer2', 'active')),
      vpn(tun('peer1', 'stale'), tun('peer2', 'active'))],
  },
  vpnTypeToggledOff: {
    events: [vpn(tun('peer1', 'active')), vpn(tun('peer1', 'stale'))],
    settings: { notifVpn: false },
  },

  // ── THE GLOBAL GATES ────────────────────────────────────────────────────
  routerAlertsDisabled: { events: [up(95), ver('7.15')], router: { id: 'r1', alertsEnabled: false } },

  // ── BGP: FOUR RULES OVER ONE EVENT, EACH WITH ITS OWN MEMORY ────────────
  //
  // state, prefix swing, flapping, hold timer. They share the loop and nothing
  // else, and all four can fire for one peer in one reading.

  // 1. STATE. Boolean established-or-not, so idle -> connect is NOT a
  // transition: only crossing established is.
  bgpFirstSightEstablished: { events: [rt(bpeer('p1', 'established'))] },
  bgpFirstSightDown: { events: [rt(bpeer('p1', 'idle'))] },
  bgpLeavesEstablished: {
    events: [rt(bpeer('p1', 'established')), rt(bpeer('p1', 'idle'))],
  },
  bgpReturnsToEstablished: {
    events: [rt(bpeer('p1', 'established')), rt(bpeer('p1', 'idle')),
      rt(bpeer('p1', 'established'))],
  },
  // TWO NON-ESTABLISHED STATES IN A ROW ARE NOT A TRANSITION -- the comparison
  // is on the boolean, and a port keeping the state STRING (as the VPN rule
  // correctly does) would file a second alert here.
  bgpIdleThenConnect: {
    events: [rt(bpeer('p1', 'idle')), rt(bpeer('p1', 'connect'))],
  },
  // A MISSING STATE reads as "unknown" IN THE DETAIL but is still not
  // established, so the transition is real and the text names the fallback.
  bgpStateGoesMissing: {
    events: [rt(bpeer('p1', 'established')), rt({ key: 'p1', name: 'p1' })],
  },
  // A PEER WITH NO KEY IS SKIPPED before any rule runs -- every map keys on it,
  // and a blank would merge unrelated peers into one slot.
  bgpPeerWithNoKey: {
    events: [rt({ name: 'p1', state: 'established' }), rt({ name: 'p1', state: 'idle' })],
  },
  // THE NAME FALLBACK CHAIN, which decides the SUBJECT and half the detail:
  // `name || remoteAddr || key`. And `where` appends the address only when there
  // is one.
  bgpNamedByRemoteAddr: {
    events: [rt({ key: 'k1', remoteAddr: '198.51.100.7', state: 'established' }),
      rt({ key: 'k1', remoteAddr: '198.51.100.7', state: 'idle' })],
  },
  bgpNamedByKeyAlone: {
    events: [rt({ key: 'k1', state: 'established' }), rt({ key: 'k1', state: 'idle' })],
  },
  bgpNameAndAddressBoth: {
    events: [rt(bpeer('edge', 'established', { remoteAddr: '198.51.100.7' })),
      rt(bpeer('edge', 'idle', { remoteAddr: '198.51.100.7' }))],
  },
  // TWO PEERS SHARING A NAME share one alert SUBJECT while keeping separate
  // state: the maps key on `key`, `fire` keys the row on `bgpPeer`. Both go
  // down, and the second is swallowed by the dedup guard.
  bgpTwoPeersOneName: {
    events: [rt({ key: 'a', name: 'edge', state: 'established' },
      { key: 'b', name: 'edge', state: 'established' }),
    rt({ key: 'a', name: 'edge', state: 'idle' },
      { key: 'b', name: 'edge', state: 'idle' })],
  },

  // 2. PREFIX SWING. Against the previous ESTABLISHED reading, at 20%.
  bgpPrefixesSteady: {
    events: [rt(bpeer('p1', 'established', { prefixes: 100 })),
      rt(bpeer('p1', 'established', { prefixes: 105 }))],
  },
  bgpPrefixesSwingUp: {
    events: [rt(bpeer('p1', 'established', { prefixes: 100 })),
      rt(bpeer('p1', 'established', { prefixes: 130 }))],
  },
  bgpPrefixesSwingDown: {
    events: [rt(bpeer('p1', 'established', { prefixes: 100 })),
      rt(bpeer('p1', 'established', { prefixes: 70 }))],
  },
  // EXACTLY 20% swings: the check is `>=`, and a port using `>` is silent here.
  bgpPrefixesExactlyAtThreshold: {
    events: [rt(bpeer('p1', 'established', { prefixes: 100 })),
      rt(bpeer('p1', 'established', { prefixes: 120 }))],
  },
  bgpPrefixesJustBelowThreshold: {
    events: [rt(bpeer('p1', 'established', { prefixes: 100 })),
      rt(bpeer('p1', 'established', { prefixes: 119 }))],
  },
  // ONE ALERT WHILE IT STAYS SWUNG, then a SETTLED recovery when a reading
  // holds. Note what "holds" means: the comparison is against the PREVIOUS
  // reading, so 130 -> 130 is steady even though it is 30% above where it
  // started.
  bgpPrefixesSwingThenSettle: {
    events: [rt(bpeer('p1', 'established', { prefixes: 100 })),
      rt(bpeer('p1', 'established', { prefixes: 130 })),
      rt(bpeer('p1', 'established', { prefixes: 130 }))],
  },
  bgpPrefixesKeepSwinging: {
    events: [rt(bpeer('p1', 'established', { prefixes: 100 })),
      rt(bpeer('p1', 'established', { prefixes: 130 })),
      rt(bpeer('p1', 'established', { prefixes: 200 }))],
  },
  // ── THE OPEN-ALERT FLAG IS NOT THE SAME THING AS AN OPEN ROW ──────────
  //
  // `prevBgpPfxAlert` is keyed on `key`; the alert row is keyed on the peer
  // NAME. Everywhere else that difference is invisible, because the store's
  // dedup guard swallows the repeat anyway -- which is why dropping the flag
  // check survived every other prefix case in this corpus.
  //
  // A RENAME separates them, and it is reachable rather than contrived:
  // `_peerKey` is `remote.address || name`, while `name` is the session's own
  // `s.name`. Renaming the BGP connection in RouterOS moves the name and leaves
  // the key alone. The flag is then the only thing stopping a second alert.
  bgpKeepsSwingingAcrossARename: {
    events: [rt({ key: '198.51.100.7', name: 'edge-old', remoteAddr: '198.51.100.7',
      state: 'established', prefixes: 100 }),
    rt({ key: '198.51.100.7', name: 'edge-old', remoteAddr: '198.51.100.7',
      state: 'established', prefixes: 130 }),
    rt({ key: '198.51.100.7', name: 'edge-new', remoteAddr: '198.51.100.7',
      state: 'established', prefixes: 200 })],
  },
  // ...and the SETTLE branch reads the same flag, with the same blind spot in
  // reverse. `!swung && flag` -- and a port dropping the flag test emits a
  // recovery for every steady reading, which `emit` then swallows because there
  // is nothing open to resolve. PRE-SEEDING an open row is what makes the
  // difference visible: the flag says "we never alerted", the store says a row
  // is open, and only the flag is right.
  bgpSteadyWithARowAlreadyOpen: {
    events: [rt(bpeer('p1', 'established', { prefixes: 100 })),
      rt(bpeer('p1', 'established', { prefixes: 105 }))],
    openAlerts: [{ type: 'bgp_prefix_change', subject: 'p1' }],
  },
  // A PEER THAT ADVERTISED NOTHING NEVER SWINGS. `oldPfx > 0` guards the
  // division -- without it every first prefix from a zero baseline is an
  // infinite swing.
  bgpPrefixesFromZero: {
    events: [rt(bpeer('p1', 'established', { prefixes: 0 })),
      rt(bpeer('p1', 'established', { prefixes: 500 }))],
  },
  // A SESSION BOUNCE IS NOT A 100% DROP. The prefix memory is only written
  // while established, so the reading either side of the bounce is compared
  // against the last good one and the peer-down alert covers the bounce alone.
  bgpBounceDoesNotCountAsASwing: {
    events: [rt(bpeer('p1', 'established', { prefixes: 100 })),
      rt(bpeer('p1', 'idle', { prefixes: 0 })),
      rt(bpeer('p1', 'established', { prefixes: 100 }))],
  },
  // NON-NUMERIC AND ABSENT PREFIXES are skipped by `typeof === 'number'`. The
  // real collector cannot produce either -- `safeInt` answers 0 -- so this
  // records a guard the live app's own collector never reaches.
  bgpPrefixesAbsent: {
    events: [rt(bpeer('p1', 'established', { prefixes: 100 })),
      rt(bpeer('p1', 'established'))],
  },
  bgpPrefixesNull: {
    events: [rt(bpeer('p1', 'established', { prefixes: 100 })),
      rt(bpeer('p1', 'established', { prefixes: null }))],
  },

  // 3. FLAPPING. A boolean transition, and the FIRST reading being false is not
  // one -- `!!undefined` is false, so nothing fires and nothing is remembered.
  bgpNotFlapping: { events: [rt(bpeer('p1', 'established', { flapping: false }))] },
  bgpStartsFlapping: {
    events: [rt(bpeer('p1', 'established')),
      rt(bpeer('p1', 'established', { flapping: true }))],
  },
  bgpFlappingOnFirstSight: {
    events: [rt(bpeer('p1', 'established', { flapping: true }))],
  },
  bgpStopsFlapping: {
    events: [rt(bpeer('p1', 'established', { flapping: true })),
      rt(bpeer('p1', 'established', { flapping: false }))],
  },
  // TRUTHY, not strictly true: the live check is `!!p.flapping`.
  bgpFlappingIsTruthy: {
    events: [rt(bpeer('p1', 'established')),
      rt(bpeer('p1', 'established', { flapping: 1 }))],
  },

  // 4. HOLD TIMER. `isEst && holdTime > 0 && holdTime < 9 && keepalive === 0`.
  bgpHoldTimerBad: {
    events: [rt(bpeer('p1', 'established', { holdTime: 3, keepalive: 0 }))],
  },
  bgpHoldTimerFixed: {
    events: [rt(bpeer('p1', 'established', { holdTime: 3, keepalive: 0 })),
      rt(bpeer('p1', 'established', { holdTime: 30, keepalive: 10 }))],
  },
  // THE BOUNDARY IS 9, EXCLUSIVE, and 0 is excluded at the other end -- a
  // hold-time of 0 means "no hold timer", which is not the misconfiguration.
  bgpHoldTimerAtNine: {
    events: [rt(bpeer('p1', 'established', { holdTime: 9, keepalive: 0 }))],
  },
  bgpHoldTimerAtEight: {
    events: [rt(bpeer('p1', 'established', { holdTime: 8, keepalive: 0 }))],
  },
  bgpHoldTimerZero: {
    events: [rt(bpeer('p1', 'established', { holdTime: 0, keepalive: 0 }))],
  },
  // KEEPALIVE IS CHECKED STRICTLY. An ABSENT one is undefined, and
  // `undefined === 0` is false -- so a peer with no keepalive reading is not a
  // misconfiguration, where a port using `== 0` or a zero default would alert.
  bgpHoldTimerKeepaliveAbsent: {
    events: [rt(bpeer('p1', 'established', { holdTime: 3 }))],
  },
  bgpHoldTimerKeepaliveNull: {
    events: [rt(bpeer('p1', 'established', { holdTime: 3, keepalive: null }))],
  },
  // NOT ESTABLISHED, so no hold-timer alert however bad the numbers.
  bgpHoldTimerWhileDown: {
    events: [rt(bpeer('p1', 'idle', { holdTime: 3, keepalive: 0 }))],
  },
  // ...and LEAVING established CLEARS an open hold alert, because `badHold`
  // goes false. Two events: the peer-down and the hold-timer recovery.
  bgpLeavingEstablishedClearsTheHoldAlert: {
    events: [rt(bpeer('p1', 'established', { holdTime: 3, keepalive: 0 })),
      rt(bpeer('p1', 'idle', { holdTime: 3, keepalive: 0 }))],
  },

  // ALL FOUR AT ONCE. The rules share only the loop, so one reading can fire
  // the state, prefix, flap and hold alerts together -- a port that returned
  // after the first match would emit one.
  bgpAllFourInOneReading: {
    events: [rt(bpeer('p1', 'established', { prefixes: 100, flapping: false,
      holdTime: 30, keepalive: 10 })),
    rt(bpeer('p1', 'established', { prefixes: 200, flapping: true,
      holdTime: 3, keepalive: 0 })),
    rt(bpeer('p1', 'idle', { prefixes: 200, flapping: false,
      holdTime: 30, keepalive: 10 }))],
  },
  bgpToggledOff: {
    events: [rt(bpeer('p1', 'established')), rt(bpeer('p1', 'idle'))],
    settings: { notifBgp: false },
  },

  // ── THE 500-ENTRY BOUND PRUNES; IT USED TO CLEAR ───────────────────────
  //
  // `_capMap` was `if (size > STATE_MAX) clear()`, called before every write
  // INSIDE the loop, so crossing the bound discarded the whole fleet's previous
  // state mid-iteration and everything after the clear read as never-seen: 501
  // interfaces going down produced ONE alert. **Found by this port while porting
  // the BGP family and fixed upstream the same day** (`07da9a9`); these cases
  // flipped with it, which was expected and is why they were written as a pair.
  //
  // It now prunes the keys ABSENT from the current payload, once per family
  // after the loop. So a batch of 501 live interfaces keeps all 501 and every
  // one of them alerts — the bound stops CHURN accumulating, and a real fleet is
  // not churn.
  ifaceStateMapOverflows: {
    events: [ifs(...Array.from({ length: 501 }, (_, i) => iface('e' + i, true))),
      ifs(...Array.from({ length: 501 }, (_, i) => iface('e' + i, false)))],
  },
  // The believability twin, ONE UNDER the line: the prune does not run at all
  // and every interface alerts. It earns itself by DISCRIMINATING -- restoring
  // the old `clear()` fails the 501 case while this one still passes, which is
  // what says the 501 case measures the boundary and not something incidental.
  ifaceStateMapJustUnderTheCap: {
    events: [ifs(...Array.from({ length: 500 }, (_, i) => iface('e' + i, true))),
      ifs(...Array.from({ length: 500 }, (_, i) => iface('e' + i, false)))],
  },
  // CHURN: 501 ephemeral names, then a small stable fleet. This is what the
  // bound is for -- the pruned keys are gone from the map, and the two survivors
  // still have their previous state and alert.
  ifaceChurnThenASmallFleet: {
    events: [ifs(...Array.from({ length: 501 }, (_, i) => iface('ppp' + i, true))),
      ifs(iface('ppp0', true), iface('ppp1', true)),
      ifs(iface('ppp0', false), iface('ppp1', false))],
  },

  // ── A HOST MID-REPROBE IS STILL LIVE ───────────────────────────────────
  //
  // NetWatch records its key BEFORE the `unknown` skip, and that ordering is the
  // whole case. `unknown` is a transient re-probe state the rule skips, but the
  // HOST has not gone anywhere — recording its key after the skip means the
  // prune treats it as absent, throws away the state its next reading is
  // compared against, and the recovery or failure that follows reads as a first
  // sighting and fires nothing. The same class of bug the prune exists to fix,
  // in miniature.
  //
  // Needs the map OVER the bound, or the prune does not run and the ordering
  // cannot be seen: 501 hosts first, then a payload of two.
  netwatchUnknownHostSurvivesThePrune: {
    events: [nw(...Array.from({ length: 501 }, (_, i) => host('n' + i, 'up'))),
      nw(host('n0', 'unknown'), host('n1', 'up')),
      nw(host('n0', 'down'), host('n1', 'up'))],
  },

  cpuTypeToggledOff: { events: [up(95)], settings: { notifCpu: false } },
  updateTypeToggledOff: { events: [ver('7.15')], settings: { notifRouterUpdate: false } },
};

const cases = {};
for (const [name, c] of Object.entries(CASES)) {
  const settings = { ...SETTINGS, ...(c.settings || {}) };
  const router = c.router === undefined ? ROUTER : c.router;
  const ran = runLive(settings, c.events, router, c.openAlerts);
  cases[name] = {
    settings, events: c.events, router,
    openAlerts: c.openAlerts || [],
    fired: ran.fired,
    // The cooldown keys live actually DELIVERED with, in order — `fire`'s own
    // first argument. Fewer than `fired` wherever an alert is emitted but not
    // notified; see the note in `runLive`.
    cooldownKeys: ran.cooldownKeys,
  };
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  const f = (k) => cases[k].fired;
  const n = (k) => f(k).length;

  assert.equal(n('cpuStaysNormal'), 0, 'a CPU that never crossed the threshold alerted');
  assert.equal(n('cpuGoesHighAndStaysHigh'), 1,
    'a CPU that went high and stayed high fired ' + n('cpuGoesHighAndStaysHigh')
    + ' times -- this is the case that separates EDGE detection from LEVEL detection, and '
    + 'system:update runs about every two seconds');
  assert.equal(n('cpuRecovers'), 2, 'the recovery did not fire its own alert');
  assert.equal(n('cpuFlaps'), 4, 'a flap did not fire on each transition');
  assert.equal(n('firstReadingIsAlreadyHigh'), 1,
    'a first reading above the threshold did not alert -- prevCpuAlert starts null, and a port '
    + 'initialising it to TRUE would go silent on exactly this case');
  assert.equal(n('firstReadingIsNormal'), 0);
  assert.equal(n('cpuExactlyAtTheThreshold'), 1, 'the threshold comparison is not >=');
  assert.equal(n('cpuJustBelow'), 0);
  assert.equal(n('aNonNumericReadingIsSkipped'), 1,
    'a non-numeric reading changed the state, so the alert re-fired');

  assert.equal(n('updateAnnouncedOnce'), 1,
    'the update alert repeated -- it must key on the VERSION, not a boolean');
  // THREE events: the 7.15 announcement, the resolve that supersedes it, and the
  // 7.16 announcement. Two would mean the supersede resolved nothing; one would
  // mean the release was swallowed again, which is the ToDo.md §7 defect.
  assert.equal(n('aLaterReleaseWhileTheFirstIsOpen'), 3,
    'a later release produced ' + n('aLaterReleaseWhileTheFirstIsOpen') + ' events, want 3 '
    + '(announce, supersede-resolve, announce). One means it was swallowed as "already '
    + 'alerting" -- the defect fixed upstream on 2026-08-27');
  assert.deepEqual(f('aLaterReleaseWhileTheFirstIsOpen').map((x) => x.event),
    ['alert:fired', 'alert:resolved', 'alert:fired'],
    'the supersede did not resolve the open row before filing the new one');
  assert.equal(n('aFirstAnnouncementDoesNotSupersede'), 1,
    'a FIRST announcement emitted more than the alert itself -- it must not supersede, or a '
    + 'rebuilt evaluator rings the bell for every open update alert');
  // The case that actually asks the question: a row IS open, and the evaluator
  // has announced nothing. ZERO events -- `fire` RETURNS on the guard, so the
  // alert is not re-filed either; the operator's existing row is left exactly as
  // it was, acknowledgement included. That silence is the point of the guard.
  assert.equal(n('aRestartMeetingAnOpenUpdateAlert'), 0,
    'a restart meeting an open update alert emitted '
    + n('aRestartMeetingAnOpenUpdateAlert') + ' events, want 0. Anything else means it '
    + 'superseded unconditionally -- resolving and re-filing an alert the operator may have '
    + 'acknowledged, on every restart');
  // ...and the twin proves the seeded row is REACHABLE at all, so the zero above
  // is about the flag rather than about a store the rules never consult. TWO,
  // not three: the first release is swallowed by the guard (it is the restart
  // case again) and only the second, with 7.15 now observed, supersedes.
  assert.equal(n('aRestartThenTwoReleases'), 2,
    'the seeded open row was never superseded even after a version was observed -- so the '
    + 'case above passes against an evaluator that simply never supersedes');
  assert.deepEqual(f('aRestartThenTwoReleases').map((x) => x.event),
    ['alert:resolved', 'alert:fired']);
  assert.equal(n('aLaterReleaseAfterTheFirstResolves'), 3,
    'the sequence the comment describes -- alert, resolve on update, alert again -- did not '
    + 'produce three events');
  assert.equal(n('noUpdateAvailable'), 0);
  assert.equal(n('updateAvailableWithNoVersion'), 0);

  // ── BGP ─────────────────────────────────────────────────────────────────
  const bgpTypes = (k) => f(k).map((x) => x.payload.alertType);

  assert.equal(n('bgpFirstSightEstablished'), 0);
  assert.equal(n('bgpFirstSightDown'), 0,
    'a peer FIRST SEEN down alerted -- the rule needs a previous reading, and a port '
    + 'treating "not established" as an alertable state would page on every restart for '
    + 'every peer that is legitimately idle');
  assert.equal(n('bgpLeavesEstablished'), 1);
  assert.deepEqual(bgpTypes('bgpReturnsToEstablished'), ['bgp_peer_down', 'bgp_peer_down'],
    'the recovery did not resolve the down alert -- `resolveType` is what makes the up '
    + 'event find the open row, and without it the alert stays open for ever');
  assert.equal(n('bgpIdleThenConnect'), 0,
    'two non-established states in a row fired -- the comparison is on the BOOLEAN, and a '
    + 'port keeping the state string (as the VPN rule correctly does) files a second alert '
    + 'every time a stuck peer cycles idle -> connect -> idle');
  assert.equal(n('bgpStateGoesMissing'), 1);
  assert.match(f('bgpStateGoesMissing')[0].payload.detail, /\(unknown\)/,
    'a missing state did not fall back to "unknown" in the detail text');
  assert.equal(n('bgpPeerWithNoKey'), 0,
    'a peer with no key was evaluated -- every map keys on it, so a blank one merges '
    + 'unrelated peers into a single state slot');

  // The name fallback chain decides the SUBJECT and half the detail.
  assert.equal(f('bgpNamedByRemoteAddr')[0].payload.subject, '198.51.100.7');
  assert.equal(f('bgpNamedByKeyAlone')[0].payload.subject, 'k1');
  assert.equal(f('bgpNameAndAddressBoth')[0].payload.subject, 'edge');
  assert.match(f('bgpNameAndAddressBoth')[0].payload.detail, /edge \(198\.51\.100\.7\)/,
    'the detail did not append the remote address to the name');
  // The whole detail, not a substring test: `(idle)` is a parenthesised part
  // this text ALWAYS has, so "contains no bracket" is not the question.
  assert.equal(f('bgpNamedByKeyAlone')[0].payload.detail,
    'BGP peer k1 left established (idle)',
    'a peer with no remote address had one appended to its name');
  // ONE, not two: the maps key on `key` and the alert row keys on the NAME, so
  // the second peer is swallowed by the dedup guard. Recorded as live behaviour
  // -- and it is the reason the port must not "tidy" the subject into the key.
  assert.equal(n('bgpTwoPeersOneName'), 1,
    'two peers sharing a name produced ' + n('bgpTwoPeersOneName') + ' alerts. The state '
    + 'maps key on `key` and the alert row on `bgpPeer`, so the second is deduplicated');

  assert.equal(n('bgpPrefixesSteady'), 0);
  assert.equal(n('bgpPrefixesSwingUp'), 1);
  assert.match(f('bgpPrefixesSwingUp')[0].payload.detail, /\+30 prefixes \(100 → 130\)/,
    'the swing detail did not name the direction, the delta and both counts');
  assert.match(f('bgpPrefixesSwingDown')[0].payload.detail, /-30 prefixes \(100 → 70\)/,
    'a downward swing did not render as a negative delta with the ABSOLUTE value');
  assert.equal(n('bgpPrefixesExactlyAtThreshold'), 1, 'the prefix threshold is not >=');
  assert.equal(n('bgpPrefixesJustBelowThreshold'), 0);
  assert.deepEqual(bgpTypes('bgpPrefixesSwingThenSettle'),
    ['bgp_prefix_change', 'bgp_prefix_change'],
    'a settled count did not resolve the swing alert');
  assert.equal(n('bgpPrefixesKeepSwinging'), 1,
    'a peer that kept swinging alerted ' + n('bgpPrefixesKeepSwinging') + ' times -- the '
    + 'open-alert flag is what stops it repeating every poll');
  assert.equal(n('bgpSteadyWithARowAlreadyOpen'), 0,
    'a steady reading resolved an open prefix row. The SETTLE branch is gated on the '
    + 'evaluator having alerted, not on a row existing -- a port reading the store instead '
    + 'announces "prefixes settled" for a peer that never swung, on the first poll after '
    + 'any restart that left a row open');
  assert.equal(n('bgpKeepsSwingingAcrossARename'), 1,
    'a peer that kept swinging across a RENAME alerted '
    + n('bgpKeepsSwingingAcrossARename') + ' times, want 1. The open-alert FLAG is keyed on '
    + '`key` and the alert ROW on the peer name, so a rename is the one sequence where '
    + 'dropping the flag check is not hidden by the store dedup');
  assert.equal(n('bgpPrefixesFromZero'), 0,
    'a peer that advertised nothing swung -- `oldPfx > 0` guards the division, and '
    + 'without it every first prefix from a zero baseline is an infinite swing');
  // TWO events, both about the STATE. The bounce produced no prefix alert: the
  // memory is only written while established, so 100 -> 100 is compared across it.
  assert.deepEqual(bgpTypes('bgpBounceDoesNotCountAsASwing'),
    ['bgp_peer_down', 'bgp_peer_down'],
    'a session bounce produced a PREFIX alert as well as the peer-down one -- it read the '
    + 'zero-prefix reading of a down peer as a 100% drop, which is the double-counting '
    + 'the established-only guard exists to prevent');
  assert.equal(n('bgpPrefixesAbsent'), 0);
  assert.equal(n('bgpPrefixesNull'), 0,
    'a null prefix count was compared -- the guard is `typeof === "number"`, and a port '
    + 'coercing null to 0 reads it as a 100% drop');

  assert.equal(n('bgpNotFlapping'), 0);
  assert.equal(n('bgpStartsFlapping'), 1);
  assert.equal(n('bgpFlappingOnFirstSight'), 1,
    'a peer already flapping when first seen did not alert');
  assert.deepEqual(bgpTypes('bgpStopsFlapping'),
    ['bgp_session_flapping', 'bgp_session_flapping']);
  assert.equal(n('bgpFlappingIsTruthy'), 1,
    'a truthy non-boolean flapping value did not alert -- the live check is `!!p.flapping`');

  assert.equal(n('bgpHoldTimerBad'), 1);
  assert.match(f('bgpHoldTimerBad')[0].payload.detail, /hold-time=3s, keepalive=0/);
  assert.deepEqual(bgpTypes('bgpHoldTimerFixed'),
    ['bgp_hold_timer_warning', 'bgp_hold_timer_warning']);
  assert.equal(n('bgpHoldTimerAtNine'), 0, 'the hold-timer boundary is not exclusive at 9');
  assert.equal(n('bgpHoldTimerAtEight'), 1, 'the hold-timer boundary is not at 9');
  assert.equal(n('bgpHoldTimerZero'), 0,
    'a hold-time of 0 alerted -- that means NO hold timer, not a short one');
  assert.equal(n('bgpHoldTimerKeepaliveAbsent'), 0,
    'an ABSENT keepalive alerted. The live check is the strict `=== 0`, so undefined is '
    + 'not a misconfiguration; a port using `== 0`, or defaulting the field to zero, pages '
    + 'about every peer whose keepalive the router did not report');
  assert.equal(n('bgpHoldTimerKeepaliveNull'), 0,
    'a NULL keepalive alerted -- same strict check, and JSON null is what an absent field '
    + 'becomes over the wire');
  assert.equal(n('bgpHoldTimerWhileDown'), 0,
    'a hold-timer warning fired for a peer that is not established');
  // THREE, and the third is the reason this case exists: leaving established
  // makes `badHold` false, which RESOLVES the open hold alert. A port evaluating
  // the hold rule only while established would leave it open for ever.
  assert.deepEqual(bgpTypes('bgpLeavingEstablishedClearsTheHoldAlert'),
    ['bgp_hold_timer_warning', 'bgp_peer_down', 'bgp_hold_timer_warning'],
    'leaving established did not clear the open hold-timer alert');

  // ALL FOUR RULES ARE INDEPENDENT. Six events across two readings; a port that
  // returned after the first match would emit two.
  assert.equal(n('bgpAllFourInOneReading'), 6,
    'four independent rules over one reading produced '
    + n('bgpAllFourInOneReading') + ' events, want 6');
  assert.deepEqual(bgpTypes('bgpAllFourInOneReading'),
    ['bgp_prefix_change', 'bgp_session_flapping', 'bgp_hold_timer_warning',
      'bgp_peer_down', 'bgp_session_flapping', 'bgp_hold_timer_warning'],
    'the four rules did not fire in source order (state, prefix, flap, hold) -- the port '
    + 'appends in the same order and the corpus compares the sequence');
  assert.equal(n('bgpToggledOff'), 0, 'notifBgp did not suppress the family');

  assert.equal(n('ifaceStateMapJustUnderTheCap'), 500,
    'a 500-interface batch produced ' + n('ifaceStateMapJustUnderTheCap')
    + ' alerts, want 500 -- the cap is not reached, so nothing is forgotten');
  assert.equal(n('ifaceStateMapOverflows'), 501,
    'a 501-interface batch produced ' + n('ifaceStateMapOverflows') + ' alerts, want 501. '
    + 'Crossing the bound must not forget anything that is STILL IN THE PAYLOAD -- the old '
    + '`clear()` produced 1 here, which is the defect this port reported and upstream fixed '
    + 'in 07da9a9');
  // THE CHURN CASE, which is what the bound actually exists for and what tells
  // this apart from a port that simply deleted `_capMap`. 501 ephemeral names in
  // the first reading, a small stable fleet in the second: the map is over the
  // bound, so the 501 absent keys are pruned. Deleting the prune entirely leaves
  // this identical (the alert count is about the second payload either way) --
  // so it is asserted on the MAP, below, not on the alerts.
  assert.equal(n('ifaceChurnThenASmallFleet'), 2,
    'the two surviving interfaces did not both alert');
  assert.equal(n('netwatchUnknownHostSurvivesThePrune'), 1,
    'a host that was mid-reprobe when the prune ran produced '
    + n('netwatchUnknownHostSurvivesThePrune') + ' alerts, want 1. Its key must be recorded '
    + 'BEFORE the `unknown` skip -- recorded after, the prune drops a host that has not gone '
    + 'anywhere and its next reading reads as a first sighting');

  assert.equal(n('pingStaysClean'), 0);
  assert.equal(n('pingGoesLossyAndStaysLossy'), 1,
    'a target that stayed lossy fired ' + n('pingGoesLossyAndStaysLossy') + ' times');
  assert.equal(n('pingRecovers'), 2);
  assert.equal(n('twoTargetsAreIndependent'), 2,
    'two targets did not alert independently -- the state is keyed on the target, and a port '
    + 'holding one flag would go quiet for the whole fleet after the first');
  assert.equal(n('twoTargetsBothLossy'), 2,
    'two targets lossy at once fired ' + n('twoTargetsBothLossy') + ' alerts, want 2 -- a '
    + 'single shared state key silences the second');
  assert.equal(n('pingWithAnEmptyTarget'), 1);
  assert.equal(n('pingExactlyAtTheThreshold'), 1, 'the ping threshold is not >=');
  assert.equal(n('pingJustBelow'), 0);
  assert.equal(n('aNonNumericLossIsSkipped'), 1);
  assert.match(f('pingWithNoRtt')[0].payload.detail, /1\.1\.1\.1/);
  assert.equal(n('pingWithNoTarget'), 1,
    'a reading with no target did not alert -- the state key falls back to "host"');
  assert.match(f('pingWithNoTarget')[0].payload.detail, /undefined/,
    'the detail no longer interpolates a missing target as "undefined". That is the live '
    + 'behaviour this case records; if it has been fixed the port must follow rather than '
    + 'this expectation being relaxed');
  assert.equal(n('pingTypeToggledOff'), 0);

  assert.equal(n('netwatchFirstSightDown'), 0,
    'a host that was ALREADY DOWN on the first sighting alerted -- the guard is '
    + '`prev !== undefined`, and a port reusing the CPU/ping shape would page on every '
    + 'reconnect');
  assert.equal(n('netwatchFirstSightUp'), 0);
  assert.equal(n('netwatchGoesDown'), 1);
  assert.equal(n('netwatchRecovers'), 2);
  assert.equal(n('netwatchStaysDown'), 1, 'a host that stayed down re-alerted');
  assert.equal(n('netwatchUnknownIsSkipped'), 1,
    'an `unknown` reading disturbed the state -- it must not become the baseline');
  assert.equal(n('netwatchUnknownFirstStaysUnseen'), 0,
    'an `unknown` first sighting made the host SEEN, so the next reading fired');
  assert.match(f('netwatchNamedHost')[0].payload.detail, /Gateway \(10\.0\.0\.1\)/,
    'a named host does not read as `name (host)`');
  assert.match(f('netwatchNameEqualsHost')[0].payload.detail, /host 10\.0\.0\.1 is/,
    'a name equal to the host was repeated');
  assert.equal(n('netwatchTwoHostsInOneEvent'), 2, 'two hosts in one event did not both fire');
  assert.equal(n('netwatchTypeToggledOff'), 0);

  assert.equal(n('ifaceFirstSightDown'), 0, 'an interface down on first sight alerted');
  assert.equal(n('ifaceGoesDown'), 1);
  assert.equal(n('ifaceRecovers'), 2);
  assert.equal(n('ifaceStaysDown'), 1);
  assert.equal(n('ifaceDisabledByAnAdmin'), 0,
    'an interface an ADMIN disabled fired "Interface Down" -- the disabled flag exists to '
    + 'suppress exactly that');
  assert.equal(n('ifaceReEnabledByAnAdmin'), 0,
    'an admin re-enabling an interface produced an unpaired "Interface Up"');
  assert.equal(n('ifaceDisabledWhileAlreadyDown'), 1,
    'the sequence fired ' + n('ifaceDisabledWhileAlreadyDown') + ' alerts, want 1 (the real '
    + 'down only) -- an admin re-enabling an interface must not produce an "Interface Up"');
  assert.equal(n('ifaceFeatureToggledOff'), 0);
  assert.equal(n('ifaceTypeFilterOff'), 0,
    'the TYPE filter did not suppress -- both toggles gate an interface alert, and the filter '
    + 'is expected to filter the bell rather than merely the push');
  assert.equal(n('anotherTypesFilterOff'), 1,
    'a DIFFERENT type\'s filter suppressed this one');
  assert.equal(n('ifaceExplicitTypeWins'), 0,
    'an explicit type did not win over the name');
  assert.equal(n('ifaceTwoInOneEvent'), 2);

  assert.equal(n('vpnFirstSightActive'), 0);
  assert.equal(n('vpnFirstSightStale'), 0, 'a peer already disconnected on first sight alerted');
  assert.equal(n('vpnDisconnects'), 1);
  assert.equal(n('vpnReconnects'), 2);
  assert.equal(n('vpnGoesToNever'), 1,
    'a peer going to "never" did not alert -- only "active" is connected, and a port comparing '
    + 'against "stale" alone misses this');
  assert.equal(n('vpnStaleToNever'), 0,
    'a change BETWEEN two disconnected states fired -- the rule compares connectedness, not '
    + 'the state string');
  assert.equal(n('vpnStaysActive'), 0);
  // ZERO, and the reason is worth writing down: never -> active IS a transition,
  // but it is an UP one, and `emit` drops a recovery that resolves nothing. So
  // the live app is silent here, and this case does NOT discriminate a port
  // comparing against "stale" alone -- that port sees no transition and is also
  // silent. `vpnNeverToStale` below is what separates them.
  assert.equal(n('vpnNeverToActive'), 0,
    'a first connection emitted something -- there is no open alert for the recovery to '
    + 'resolve, so `emit` drops it');
  assert.equal(n('vpnNeverToStale'), 0,
    'never -> stale fired; BOTH are disconnected, so there is no transition. This is the case '
    + 'that catches a port comparing `prev !== "stale"`: it reads "never" as connected and '
    + 'files a spurious disconnect');
  assert.equal(n('vpnTwoPeersIndependent'), 1, 'the second peer was affected by the first');
  assert.equal(n('vpnTypeToggledOff'), 0);

  assert.equal(n('routerAlertsDisabled'), 0, 'a router with alerts OFF still alerted');
  assert.equal(n('cpuTypeToggledOff'), 0, 'the per-type toggle did not suppress the CPU alert');
  assert.equal(n('updateTypeToggledOff'), 0);

  const counts = Object.values(cases).map((c) => c.fired.length);
  assert.ok(counts.some((x) => x === 0) && counts.some((x) => x > 0),
    'every case fires the same number of times, so this corpus proves nothing');
}

const json = JSON.stringify({
  note: 'Generated by tools/alert-eval-cases.js by RUNNING the live createEvaluator with only '
    + 'the delivery layer stubbed. Do not edit.',
  covered: COVERED,
  uncovered: UNCOVERED,
  cases,
}, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('alert-eval-cases.json is STALE -- re-run without --check');
    process.exit(1);
  }
  console.log('alert-eval-cases.json is current (' + Object.keys(cases).length + ' cases, '
    + COVERED.length + ' of ' + families.length + ' rule families)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, json);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' cases, '
    + COVERED.length + ' of ' + families.length + ' rule families covered)');
}
