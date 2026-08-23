/**
 * Dormancy and capability-latch regressions.
 *
 * A collector that finds nothing to report should stop holding a channel open
 * and say so, rather than emitting an empty payload that is indistinguishable
 * from a working card with no rows. These tests pin the latch semantics that
 * the Top Talkers collector got wrong: the "this router has no kid-control"
 * verdict must survive an ordinary successful tick, must not be undone by an
 * idle wake-up, and must only be revisited by a deliberate re-probe.
 */

const test   = require('node:test');
const assert = require('node:assert');

// Tracked, because several tests here call probe(), which resumes the collector
// and starts a 9 s silence timer and a 60 s heartbeat. Those are meant to keep a
// SERVER alive; in a test they keep the RUNNER alive. This file was the reason
// the suite needed --test-force-exit, and that flag is what made the reported
// test count unstable — it killed the process the moment the runner thought it
// was done, intermittently truncating the tail of the largest file.
const { track } = require('./helpers/collector-cleanup');
const TopTalkersCollector = track(require('../src/collectors/talkers'));

function harness({ clientsCount = 1, connected = true } = {}) {
  const emitted = [];
  const streamHandlers = {};
  const fakeStream = { on(ev, fn) { streamHandlers[ev] = fn; }, stop() {} };
  let streamsOpened = 0;
  const ros = {
    connected,
    on() {},
    stream() { streamsOpened++; return fakeStream; },
    write: async () => [],
  };
  const io = {
    to() { return io; },
    engine: { clientsCount },
    on() {},
    emit(ev, data) { emitted.push({ ev, data }); },
  };
  return {
    emitted, streamHandlers, ros, io,
    opened: () => streamsOpened,
    make: (opts = {}) => new TopTalkersCollector({
      ros, io, pollMs: 3000, state: {}, topN: 5, ...opts,
    }),
  };
}

// --- the latch must survive success -----------------------------------------

test('an unsupported verdict is not undone by a later successful tick', () => {
  const h = harness();
  const c = h.make();

  c._startStream();
  h.streamHandlers.error(new Error('unknown command'));
  assert.equal(c._unavailable, true, 'latched after "unknown command"');

  // A commit can still fire from a debounce scheduled before the error landed.
  // It used to clear the latch outright, re-opening the probe on the next tick.
  c._devicesNext.set('00:00:5E:00:53:00', {
    name: 'device-a', mac: '00:00:5E:00:53:00', rateUp: 1_000_000, rateDown: 2_000_000,
  });
  c._commitTick();

  assert.equal(c._unavailable, true, 'the latch must outlive a successful commit');
});

test('a latched collector refuses to reopen its stream', () => {
  const h = harness();
  const c = h.make();

  c._startStream();
  const afterFirst = h.opened();
  h.streamHandlers.error(new Error('no such command prefix'));

  c._startStream();
  c.resume();

  assert.equal(h.opened(), afterFirst, 'neither _startStream nor resume may reopen a latched channel');
});

test('probe() is the one deliberate way back in', () => {
  const h = harness();
  const c = h.make();

  c._startStream();
  h.streamHandlers.error(new Error('unknown command'));
  const afterError = h.opened();

  c.probe();

  assert.equal(c._unavailable, false, 'probe clears the latch');
  assert.equal(h.opened(), afterError + 1, 'probe reopens exactly one channel');
});

// --- the payload has to be legible to the client ----------------------------

test('an unsupported router is reported as unavailable, not merely empty', () => {
  const h = harness();
  const c = h.make();

  c._startStream();
  h.streamHandlers.error(new Error('unknown command'));

  assert.equal(h.emitted.length, 1);
  assert.deepEqual(h.emitted[0].data.devices, []);
  assert.equal(h.emitted[0].data.available, false,
    'an empty device list alone cannot tell the card why it is empty');
});

test('a working router reports itself available', () => {
  const h = harness();
  const c = h.make();

  c._devicesNext.set('00:00:5E:00:53:01', {
    name: 'device-b', mac: '00:00:5E:00:53:01', rateUp: 8_000, rateDown: 16_000,
  });
  c._commitTick();

  assert.equal(c.lastPayload.available, true);
});

test('a supported router with nothing to report is available and empty', () => {
  const h = harness();
  const c = h.make();

  c._commitTick();

  assert.equal(c.lastPayload.available, true, 'kid-control exists, it just tracks nobody');
  assert.deepEqual(c.lastPayload.devices, []);
});

// --- backoff has to actually back off ---------------------------------------

test('a transient stream error backs off exponentially, not at a flat rate', () => {
  const h = harness();
  const c = h.make();
  const base = c._backoffMs;

  c._startStream();
  h.streamHandlers.error(new Error('connection reset'));
  const afterOne = c._backoffMs;

  c._stream = null;
  c._startStream();
  h.streamHandlers.error(new Error('connection reset'));
  const afterTwo = c._backoffMs;

  assert.equal(afterOne, base * 2, 'first failure doubles the delay');
  assert.equal(afterTwo, base * 4, 'second failure doubles it again');

  clearTimeout(c._backoffTimer);
});

test('the backoff is capped rather than growing without bound', () => {
  const h = harness();
  const c = h.make();
  c._backoffMs = c._maxBackoffMs;

  c._startStream();
  h.streamHandlers.error(new Error('connection reset'));

  assert.equal(c._backoffMs, c._maxBackoffMs);

  clearTimeout(c._backoffTimer);
});

test('a successful tick resets the backoff to its base', () => {
  const h = harness();
  const c = h.make();

  c._startStream();
  h.streamHandlers.error(new Error('connection reset'));
  assert.ok(c._backoffMs > c._baseBackoffMs);
  clearTimeout(c._backoffTimer);

  c._devicesNext.set('00:00:5E:00:53:02', {
    name: 'device-c', mac: '00:00:5E:00:53:02', rateUp: 100, rateDown: 200,
  });
  c._commitTick();

  assert.equal(c._backoffMs, c._baseBackoffMs);
});

// --- a reconnect is a re-probe ----------------------------------------------

test('probe() clears the retry backoff along with the latch', () => {
  const h = harness();
  const c = h.make();

  c._startStream();
  h.streamHandlers.error(new Error('connection reset'));
  clearTimeout(c._backoffTimer);
  c._backoffUntil = Date.now() + 60_000;

  c.probe();

  assert.equal(c._backoffMs, c._baseBackoffMs);
  assert.equal(c._backoffUntil, 0, 'a re-probe must not be blocked by a pending backoff window');
});

// --- createDormancyState: the decision itself -------------------------------

const { createDormancyState } = require('../src/collectors/util');

const T0 = 1_755_000_000_000;   // fixed epoch; the helper takes `now` explicitly

function state(opts = {}) {
  return createDormancyState({ emptyThreshold: 3, backoffMs: 1000, maxBackoffMs: 8000, ...opts });
}

test('one empty result is not a verdict', () => {
  const d = state();
  assert.equal(d.observe({ ts: 1, empty: true }, T0), null);
  assert.equal(d.observe({ ts: 2, empty: true }, T0), null);
  assert.equal(d.dormant, false, 'two empties is a lull, not an absence');
});

test('a sustained empty streak puts the collector to sleep', () => {
  const d = state();
  d.observe({ ts: 1, empty: true }, T0);
  d.observe({ ts: 2, empty: true }, T0);
  assert.equal(d.observe({ ts: 3, empty: true }, T0), 'sleep');
  assert.equal(d.dormant, true);
});

test('a repeated timestamp is not a second observation', () => {
  const d = state();
  d.observe({ ts: 1, empty: true }, T0);
  d.observe({ ts: 1, empty: true }, T0);
  d.observe({ ts: 1, empty: true }, T0);
  assert.equal(d.dormant, false,
    'a slow collector must not be condemned by the supervisor ticking faster than it emits');
  assert.equal(d.streak, 1);
});

test('an unsupported command sleeps at once and at the long delay', () => {
  const d = state();
  assert.equal(d.observe({ ts: 1, unsupported: true }, T0), 'sleep');
  assert.equal(d.delayMs, 8000, 'a command error only changes on upgrade — do not re-probe eagerly');
});

test('data wakes a dormant collector and resets the delay', () => {
  const d = state();
  d.observe({ ts: 1, unsupported: true }, T0);
  assert.equal(d.observe({ ts: 2, empty: false }, T0), 'wake');
  assert.equal(d.dormant, false);
  assert.equal(d.delayMs, 1000);
});

test('a non-empty result mid-streak clears the streak', () => {
  const d = state();
  d.observe({ ts: 1, empty: true }, T0);
  d.observe({ ts: 2, empty: true }, T0);
  d.observe({ ts: 3, empty: false }, T0);
  d.observe({ ts: 4, empty: true }, T0);
  assert.equal(d.dormant, false, 'the streak must be consecutive, not cumulative');
});

test('a probe is due only once the backoff has elapsed', () => {
  const d = state();
  d.observe({ ts: 1, empty: true }, T0);
  d.observe({ ts: 2, empty: true }, T0);
  d.observe({ ts: 3, empty: true }, T0);

  assert.equal(d.dueForProbe(T0), false);
  assert.equal(d.dueForProbe(T0 + 999), false);
  assert.equal(d.dueForProbe(T0 + 1000), true);
});

test('a probe that comes back empty lengthens the wait', () => {
  const d = state();
  d.observe({ ts: 1, empty: true }, T0);
  d.observe({ ts: 2, empty: true }, T0);
  d.observe({ ts: 3, empty: true }, T0);

  d.markProbed(T0 + 1000);
  assert.equal(d.dueForProbe(T0 + 1001), false, 'a probe in flight is not due again');
  assert.equal(d.observe({ ts: 4, empty: true }, T0 + 1001), null, 'no re-announcement of sleep');
  assert.equal(d.delayMs, 2000);
  assert.equal(d.dormant, true);
});

test('the re-probe delay is capped', () => {
  const d = state();
  d.observe({ ts: 1, empty: true }, T0);
  d.observe({ ts: 2, empty: true }, T0);
  d.observe({ ts: 3, empty: true }, T0);

  let ts = 3;
  for (let i = 0; i < 10; i++) {
    d.markProbed(T0);
    d.observe({ ts: ++ts, empty: true }, T0);
  }
  assert.equal(d.delayMs, 8000);
});

test('a probe that never reports back is settled rather than left hanging', () => {
  const d = state({ probeTimeoutMs: 5000 });
  d.observe({ ts: 1, empty: true }, T0);
  d.observe({ ts: 2, empty: true }, T0);
  d.observe({ ts: 3, empty: true }, T0);

  d.markProbed(T0 + 1000);
  // A collector that suppresses an unchanged emit never advances ts, so no
  // observation will ever arrive to close this out.
  assert.equal(d.dueForProbe(T0 + 5000), false, 'still inside the probe window');
  assert.equal(d.dueForProbe(T0 + 6001), false, 'window expired: treated as still asleep');
  assert.equal(d.probing, false, 'the probe must not stay in flight forever');
  assert.equal(d.delayMs, 2000, 'and the wait lengthens as if it had come back empty');
});

test('reset forgets everything, as a reconnect should', () => {
  const d = state();
  d.observe({ ts: 1, unsupported: true }, T0);
  assert.equal(d.dormant, true);

  d.reset();

  assert.equal(d.dormant, false);
  assert.equal(d.delayMs, 1000);
  assert.equal(d.observe({ ts: 1, empty: false }, T0), null, 'ts 1 is new again after a reset');
});

test('a payload with no timestamp is ignored rather than counted', () => {
  const d = state();
  assert.equal(d.observe(null, T0), null);
  assert.equal(d.observe({ empty: true }, T0), null);
  assert.equal(d.streak, 0, 'a collector that has never emitted is not evidence of emptiness');
});

// --- payloadEmpty: reading a payload the registry described ------------------

const { payloadEmpty } = require('../src/collectors/util');

test('a single named list decides on its own', () => {
  assert.equal(payloadEmpty({ devices: [] }, 'devices'), true);
  assert.equal(payloadEmpty({ devices: [{ mac: '00:00:5E:00:53:00' }] }, 'devices'), false);
});

test('several lists are empty only when all of them are', () => {
  const key = ['tunnels', 'ppp', 'ipsec'];
  assert.equal(payloadEmpty({ tunnels: [], ppp: [], ipsec: [] }, key), true);
  assert.equal(payloadEmpty({ tunnels: [], ppp: [], ipsec: [{ n: 'sa1' }] }, key), false,
    'a VPN with no WireGuard peers but a live IPsec SA has plenty to show');
});

test('a payload missing every named list cannot be judged', () => {
  assert.equal(payloadEmpty({ ts: 1, pollMs: 3000 }, 'devices'), false,
    'absent is not empty — a collector mid-construction must not be condemned');
  assert.equal(payloadEmpty({ devices: null }, 'devices'), false);
  assert.equal(payloadEmpty({}, ['simple', 'tree']), false);
});

test('a partially readable payload is judged on the lists that are there', () => {
  assert.equal(payloadEmpty({ simple: [], tree: undefined }, ['simple', 'tree']), true);
  assert.equal(payloadEmpty({ simple: [{ id: '*1' }], tree: undefined }, ['simple', 'tree']), false);
});

test('no payload and no key are both non-answers', () => {
  assert.equal(payloadEmpty(null, 'devices'), false);
  assert.equal(payloadEmpty({ devices: [] }, undefined), false);
});

// --- the registry drives the supervisor -------------------------------------

const { COLLECTORS, DISABLEABLE, DORMANCY_ELIGIBLE } = require('../src/collection');

test('every dormancy-eligible collector is one the user could disable anyway', () => {
  for (const key of DORMANCY_ELIGIBLE) {
    assert.ok(DISABLEABLE.includes(key),
      `${key} may sleep but cannot be turned off — a protected collector feeds stored ` +
      'history or another collector, so "nobody is watching" is not a reason to stop reading it');
  }
});

test('no protected collector declares an emptyKey', () => {
  const offenders = COLLECTORS.filter(c => c.emptyKey && !c.disableable).map(c => c.key);
  assert.deepEqual(offenders, []);
});

test('the collectors deliberately left out are the ones with no list to be empty', () => {
  const out = DISABLEABLE.filter(k => !DORMANCY_ELIGIBLE.includes(k)).sort();
  assert.deepEqual(out, ['conns', 'dns', 'logs', 'ping'],
    'ping reports scalars, conns an aggregate count, dns a settings row that is always ' +
    'present, logs no lastPayload at all. Adding one here needs a reason recorded in ' +
    'src/collection.js, not just a passing test');
});

test('every emptyKey names a field, not a stray type', () => {
  for (const c of COLLECTORS) {
    if (!c.emptyKey) continue;
    const keys = Array.isArray(c.emptyKey) ? c.emptyKey : [c.emptyKey];
    assert.ok(keys.length > 0, `${c.key} has an empty emptyKey list`);
    for (const k of keys) {
      assert.equal(typeof k, 'string', `${c.key} emptyKey entry is not a string`);
      assert.ok(k.length > 0, `${c.key} has a blank emptyKey entry`);
    }
  }
});

// --- the resume arbiter is the only door ------------------------------------

const fs   = require('node:fs');
const path = require('node:path');
const INDEX_SRC = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');

test('nothing resumes a collector behind the arbiter\'s back', () => {
  // Three gates decide whether a collector runs — idle, page rooms, dormancy.
  // A bare session.<x>.resume() anywhere is a fourth caller that knows nothing
  // about the other three, and would wake a dormant collector on the next
  // socket join, forever.
  const bare = INDEX_SRC.split('\n')
    .map((line, i) => ({ line, n: i + 1 }))
    .filter(({ line }) => /\bsession\.\w+\.resume\(\)/.test(line) || /\bs\[\w+\]\.resume\(\)/.test(line));
  assert.deepEqual(bare.map(b => b.n), [],
    'use _resumeCollector(session, entry, key) instead:\n' + bare.map(b => `  ${b.n}: ${b.line.trim()}`).join('\n'));
});

test('the arbiter actually consults dormancy', () => {
  const fn = INDEX_SRC.slice(INDEX_SRC.indexOf('function _resumeCollector'));
  const body = fn.slice(0, fn.indexOf('\n}\n') + 2);
  assert.ok(/_isDormant\(entry, key\)/.test(body), '_resumeCollector must check _isDormant');
  assert.ok(/return;/.test(body), '_resumeCollector must refuse, not merely note the state');
});

test('a reconnect clears every verdict', () => {
  // A reconnect is the event most likely to follow the RouterOS upgrade or
  // package install that turns "unknown command" into a working menu.
  assert.ok(/_resetDormancy\(session, entry\);/.test(INDEX_SRC));
});

test('the supervisor timer is cleared on teardown', () => {
  assert.ok(/clearInterval\(entry\._dormancyTimer\)/.test(INDEX_SRC),
    'a surviving interval would keep a torn-down session alive');
  assert.ok(/entry\._dormancy = null/.test(INDEX_SRC),
    'dormancy is per-session: a rebuilt session must re-probe from scratch');
});

test('page focus re-probes before the page stream is resumed', () => {
  const focus = INDEX_SRC.indexOf("socket.on('page:focus'");
  const chunk = INDEX_SRC.slice(focus, focus + 2500);
  // Match the CALLS, not the words: the comment above the wake loop names
  // _updatePageStream too, and matching that made this pass for the wrong reason.
  const wake  = chunk.indexOf('_wakeForFocus(s, e, ck)');
  const gate  = chunk.indexOf('_updatePageStream(s, e, name)');
  assert.ok(wake > -1, 'page:focus must re-probe dormant collectors');
  assert.ok(gate > -1, 'page:focus still gates the page stream');
  assert.ok(wake < gate,
    'the veto has to be lifted first, or _updatePageStream resumes into a dormant collector');
});

test('the supervisor skips collectors the user turned off', () => {
  const tick = INDEX_SRC.slice(INDEX_SRC.indexOf('function _dormancyTick'));
  const body = tick.slice(0, tick.indexOf('\n}\n') + 2);
  assert.ok(/session\.collection\.enabled\[def\.key\]/.test(body),
    'a disabled collector is a makeNullCollector — judging it would report a user choice as dormancy');
  assert.ok(/clientsCount|rooms\.get\('router-'/.test(body),
    'an idle session emits nothing and would otherwise read as universally empty');
});

// --- the card has to say which of the two silences this is -------------------

const APP_JS   = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');
const INDEX_HTML = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');

test('a dormant card gets no visual treatment at all', () => {
  // The card body already says "No devices" / "No active peers", and that is the
  // whole message. A scrim on top made an ordinary empty card stand out from its
  // neighbours on the dashboard, which reads as a fault rather than as emptiness.
  assert.ok(!/\.card\.is-dormant\s*\{/.test(INDEX_HTML),
    'is-dormant must not dim the card');
  assert.ok(!/\.card\.is-dormant\s+\.stale-overlay\s*\{/.test(INDEX_HTML),
    'is-dormant must not raise the overlay');
  assert.ok(APP_JS.indexOf('nothing to report') === -1,
    'and must not write an overlay caption');
});

test('a collector the user switched off still announces itself', () => {
  // The opposite call, deliberately: an operator who disabled a collector has no
  // other way to tell that card apart from one that is merely empty.
  assert.ok(/\.card\.is-collector-off\s*\{/.test(INDEX_HTML));
  assert.ok(/\.card\.is-collector-off \.stale-overlay\{/.test(INDEX_HTML));
  assert.ok(APP_JS.indexOf('collection disabled') > -1);
});

test('the stale overlay keeps saying "stale"', () => {
  // Rewriting this caption for dormancy meant a card that later went genuinely
  // stale announced itself with the wrong sentence.
  const h = APP_JS.slice(APP_JS.indexOf("socket.on('collection:status'"));
  const body = h.slice(0, h.indexOf('\n});') + 4);
  assert.ok(!/\.stale-overlay/.test(body),
    'the status handler must not touch the overlay');
  assert.ok(/&#9679; stale|● stale/.test(INDEX_HTML),
    'the markup still carries the stale caption');
});

test('a dormant card stops counting down to stale', () => {
  const h = APP_JS.slice(APP_JS.indexOf("socket.on('collection:status'"));
  const body = h.slice(0, h.indexOf('\n});') + 4);
  assert.ok(/staleTimers\[cardId\] = 0;/.test(body),
    'zero is the established "quiet" sentinel; a sleeping collector is not late');
  assert.ok(/classList\.remove\('is-stale'\)/.test(body));
});

test('the sweep keeps suppressing the countdown for a dormant card', () => {
  // collection:status arrives once per transition. The dashboard grid re-renders
  // cards afterwards and wipes their classes, which is the exact bug the
  // collector-off re-assertion was added for.
  const sweep = APP_JS.slice(APP_JS.indexOf('setInterval(function(){'));
  const body  = sweep.slice(0, sweep.indexOf('},3000);') + 8);
  assert.ok(/_collectionDormantCard\(cfg\.cardId\)/.test(body));
  assert.ok(/classList\.add\('is-dormant'\)/.test(body), 'the marker survives a re-render');
  assert.ok(/staleTimers\[cfg\.cardId\]=0/.test(body),
    'which is the point: a sleeping collector must not be counted late');
  assert.ok(!/nothing to report/.test(body), 'and still writes no caption');
});

test('a user-disabled collector outranks dormancy on the card', () => {
  const h = APP_JS.slice(APP_JS.indexOf("socket.on('collection:status'"));
  const body = h.slice(0, h.indexOf('\n});') + 4);
  assert.ok(/_collectionOffCard\(cardId\)\) return;/.test(body),
    'the two events can arrive in either order on first load');
});

test('an empty payload clears the table instead of leaving the last one up', () => {
  // Both guards re-armed the stale timer on the empty payload and then returned,
  // so the card kept rendering rows the router had stopped reporting — and, after
  // a router switch, the previous router's rows — with nothing to reveal it.
  assert.ok(!/if\(lastTalkers\)return;/.test(APP_JS),
    'talkers:update must not treat an empty payload as "no news"');
  assert.ok(!/if\(lastLanData\)return;/.test(APP_JS),
    'the LAN overview had the identical bug');
  assert.ok(/lastTalkers=null;/.test(APP_JS), 'and must drop the cached rows');
  assert.ok(/lastLanData=null;/.test(APP_JS));
});

test('an unsupported talkers payload says so rather than guessing "no devices"', () => {
  assert.ok(/data\.available===false\?'Kid Control is not available on this router'/.test(APP_JS),
    '"No devices" on a router with no kid-control menu is a claim we cannot support');
});

// --- the toggle grid cannot drift again -------------------------------------

test('every disableable collector is offered to the modal', () => {
  // This is the assertion whose absence let ten collectors ship with no toggle.
  // It now guards the endpoint rather than the markup, because the markup is
  // generated from exactly this list.
  const served = INDEX_SRC.slice(INDEX_SRC.indexOf("app.get('/api/collectors'"));
  const body = served.slice(0, served.indexOf('\n});') + 4);
  assert.ok(/_COLLECTOR_DEFS/.test(body), 'the endpoint must read the registry');
  assert.ok(/\.filter\(c => c\.disableable\)/.test(body),
    'and offer exactly the disableable set — not a hand-kept subset of it');
  assert.ok(/requires: c\.requires/.test(body),
    'requires has to reach the form, or the dependency locking is a guess');
});

test('the toggle grid is no longer hand-written markup', () => {
  const grid = INDEX_HTML.slice(INDEX_HTML.indexOf('id="rtrModalCollectors"'));
  const end  = grid.indexOf('</div>');
  assert.ok(!/data-coll=/.test(grid.slice(0, end)),
    'a hand-written toggle here is a second copy of the registry, and it drifted last time');
});

test('the form locks dependencies from the registry, not from a hardcoded pair', () => {
  const fn = APP_JS.slice(APP_JS.indexOf('function _syncCollDeps'));
  const body = fn.slice(0, fn.indexOf('\n  }\n') + 4);
  assert.ok(/data-requires/.test(body), 'read the dependency from the markup');
  assert.ok(!/'bandwidth'/.test(body) && !/'conns'/.test(body),
    'naming the conns->bandwidth pair here means a second dependency goes unnoticed, ' +
    'and the server cascades it either way — so the form would silently disagree');
});

test('an unloaded grid omits the collection block rather than clearing it', () => {
  // off: [] reads as "enable everything". Sending that because a fetch had not
  // resolved yet would wipe the router's disabled collectors on any save.
  const fn = APP_JS.slice(APP_JS.indexOf('collection: (function ()'));
  const body = fn.slice(0, fn.indexOf('})(),') + 5);
  assert.ok(/if \(!toggles\.length\) return undefined;/.test(body));
});

test('the registry endpoint is reachable without a router being selected', () => {
  // Static metadata about what MikroDash can collect, identical for every router
  // and user. Gating it on a router permission would break the modal for exactly
  // the person configuring their first router.
  const served = INDEX_SRC.slice(INDEX_SRC.indexOf("app.get('/api/collectors'"));
  const line = served.slice(0, served.indexOf('\n'));
  assert.ok(!/Rbac\.require/.test(line), 'no per-router gate on static registry metadata');
});

// --- the empty card that went stale (hAP AC2) -------------------------------
//
// Root cause: talkers only committed on the non-empty -> empty EDGE, and had no
// heartbeat. A table that was already empty produced nothing at all — no
// talkers:update for the browser's stale timer, and no fresh lastPayload.ts for
// the dormancy supervisor. So the card went stale on a router that was answering
// correctly, and the one collector that most needed to sleep never could.

test('an already-empty table still commits every interval', () => {
  const h = harness();
  const c = h.make();
  c._startStream();

  // Two empty intervals in a row, the second with nothing cached to clear.
  h.streamHandlers.data([]);
  assert.ok(c._commitTimer, 'first empty interval schedules a commit');
  clearTimeout(c._commitTimer); c._commitTimer = null;

  h.streamHandlers.data([]);
  assert.ok(c._commitTimer,
    'a table that is already empty must still commit — this is the hAP AC2 bug');
  clearTimeout(c._commitTimer);
});

test('a steadily empty table keeps advancing lastPayload.ts', () => {
  const h = harness();
  const c = h.make();

  c._commitTick();
  const first = c.lastPayload.ts;
  assert.ok(first, 'an empty commit still produces a payload');

  // The supervisor discriminates observations by ts. A frozen ts reads as "no new
  // information" forever, so the empty streak never reaches the threshold.
  const st = createDormancyState({ emptyThreshold: 3, backoffMs: 1000, maxBackoffMs: 8000 });
  let ts = first, slept = null;
  for (let i = 0; i < 3; i++) {
    ts += 3000;
    c.lastPayload = { ...c.lastPayload, ts };
    const v = st.observe({ ts: c.lastPayload.ts, empty: !c.lastPayload.devices.length }, T0 + i);
    if (v === 'sleep') slept = i;
  }
  assert.equal(slept, 2, 'three distinct empty payloads put it to sleep');
});

test('talkers heartbeats like every other streamed collector', () => {
  const h = harness();
  const c = h.make();
  c.start();
  assert.ok(c._heartbeatTimer, 'start() must arm the re-emit');

  c.suspend();
  assert.equal(c._heartbeatTimer, null, 'suspend() must disarm it');

  c.resume();
  assert.ok(c._heartbeatTimer, 'resume() must re-arm it — including in poll mode');

  c.stop();
  assert.equal(c._heartbeatTimer, null, 'stop() must not leak the interval');
});

test('a streamed collector reports pollMs 0 so the client keeps its fixed threshold', () => {
  const h = harness();
  const streamed = h.make({ streamMode: true });
  streamed._commitTick();
  assert.equal(streamed.lastPayload.pollMs, 0,
    'advertising 3000 while streaming gave the card a 23 s deadline nothing could meet');

  const polled = h.make({ streamMode: false });
  polled._commitTick();
  assert.equal(polled.lastPayload.pollMs, 3000, 'poll mode still reports its real interval');
});

test('the reported interval follows a runtime downgrade', () => {
  const h = harness();
  const c = h.make({ streamMode: true });
  assert.equal(c._reportedPollMs, 0);
  // The stream-timeout path flips streamMode mid-session (CHR/VM thread starvation).
  c.streamMode = false;
  assert.equal(c._reportedPollMs, 3000,
    'a value captured in the constructor would describe the wrong delivery mode forever');
});

test('the Top Talkers card is treated as streamed by the client', () => {
  const cfg = APP_JS.match(/\{cardId:'talkersCard',\s*event:'talkers:update',\s*threshold:(\d+)\}/);
  assert.ok(cfg, 'talkersCard must still have a stale config entry');
  assert.equal(Number(cfg[1]), 90000,
    'a 60 s heartbeat cannot satisfy a 20 s threshold');
});

// --- the heartbeat has to beat faster than the deadline it advertises --------
//
// The hAP AC2 runs collection mode "poll". Talkers then reports pollMs 3000, the
// client sets its threshold to pollMs + STALE_GRACE = 23 s, and a fixed 60 s
// heartbeat cannot possibly meet it — so the card still went stale ~30 s in and
// recovered only when dormancy fired ~20 s later.

test('a polled collector beats faster than its own stale threshold', () => {
  const h = harness();
  const STALE_GRACE = 20000;   // mirrors public/app.js
  [1000, 3000, 5000, 30000].forEach(function (pollMs) {
    const c = h.make({ streamMode: false, pollMs: pollMs });
    const threshold = c._reportedPollMs + STALE_GRACE;
    assert.ok(c._heartbeatMs < threshold,
      'pollMs ' + pollMs + ': heartbeat ' + c._heartbeatMs + ' must beat threshold ' + threshold);
  });
});

test('a streamed collector beats faster than the fixed 90 s threshold', () => {
  const h = harness();
  const c = h.make({ streamMode: true });
  assert.equal(c._reportedPollMs, 0, 'streamed reports 0, so the client keeps its fixed threshold');
  assert.ok(c._heartbeatMs < 90000, 'heartbeat ' + c._heartbeatMs + ' must beat the streamed threshold');
});

test('the heartbeat re-arms when a runtime downgrade changes the cadence', () => {
  const h = harness();
  const c = h.make({ streamMode: true });
  c._startHeartbeat();
  const streamed = c._heartbeatArmedMs;
  assert.equal(streamed, 60000);

  // The stream-timeout path flips streamMode and calls _startTalkers() again.
  c.streamMode = false;
  c._startHeartbeat();
  assert.notEqual(c._heartbeatArmedMs, streamed,
    'the early return would otherwise leave a 60 s beat guarding a 23 s deadline');
  assert.ok(c._heartbeatArmedMs < c._reportedPollMs + 20000);
  c.stop();
});

test('stopping the heartbeat forgets the armed cadence', () => {
  const h = harness();
  const c = h.make({ streamMode: false });
  c._startHeartbeat();
  assert.ok(c._heartbeatArmedMs > 0);
  c._stopHeartbeat();
  assert.equal(c._heartbeatArmedMs, 0, 'a stale armed value would suppress the next re-arm check');
});

// --- a frozen payload is still evidence, when it is empty -------------------
//
// netwatch, vpn, firewall, routing and topology heartbeat by emitting
// { ...lastPayload, ts: Date.now() } to the browser and never reassign
// lastPayload. Their ts freezes the moment the data settles, so requiring a
// fresh ts meant dormancy could never fire for any of them — it worked for the
// 9 poll-loop collectors and silently skipped the other 8.

test('a frozen empty payload eventually sleeps', () => {
  const d = state({ restampMs: 45000 });
  // One payload, emitted once, then never reassigned — the netwatch shape.
  assert.equal(d.observe({ ts: 1, empty: true }, T0), null);
  assert.equal(d.observe({ ts: 1, empty: true }, T0 + 45000), null);
  assert.equal(d.observe({ ts: 1, empty: true }, T0 + 90000), 'sleep');
  assert.equal(d.dormant, true);
});

test('a frozen payload is re-counted no faster than restampMs', () => {
  const d = state({ restampMs: 45000 });
  d.observe({ ts: 1, empty: true }, T0);
  // The supervisor ticks every 15 s; without the rate limit these three would
  // reach the threshold on their own and condemn a slow collector.
  d.observe({ ts: 1, empty: true }, T0 + 15000);
  d.observe({ ts: 1, empty: true }, T0 + 30000);
  d.observe({ ts: 1, empty: true }, T0 + 44999);
  assert.equal(d.streak, 1, 'one observation, however many ticks noticed it');
  assert.equal(d.dormant, false);
});

test('a frozen NON-empty payload is never counted', () => {
  const d = state({ restampMs: 1000 });
  d.observe({ ts: 1, empty: false }, T0);
  for (let i = 1; i <= 10; i++) d.observe({ ts: 1, empty: false }, T0 + i * 5000);
  assert.equal(d.dormant, false, 'a settled collector with data must never sleep');
  assert.equal(d.streak, 0);
});

test('a frozen payload does not drive the backoff while already dormant', () => {
  const d = state({ restampMs: 1000 });
  d.observe({ ts: 1, empty: true }, T0);
  d.observe({ ts: 1, empty: true }, T0 + 2000);
  d.observe({ ts: 1, empty: true }, T0 + 4000);
  assert.equal(d.dormant, true);
  const delayAtSleep = d.delayMs;

  // Once asleep the collector is suspended and its ts cannot move. Letting the
  // frozen payload keep counting would double the backoff on every tick, racing
  // the probe logic that is supposed to own it.
  for (let i = 1; i <= 10; i++) d.observe({ ts: 1, empty: true }, T0 + 4000 + i * 5000);
  assert.equal(d.delayMs, delayAtSleep, 'only a real probe result advances the backoff');
});

test('fresh data still wakes a collector that slept on a frozen payload', () => {
  const d = state({ restampMs: 1000 });
  d.observe({ ts: 1, empty: true }, T0);
  d.observe({ ts: 1, empty: true }, T0 + 2000);
  d.observe({ ts: 1, empty: true }, T0 + 4000);
  assert.equal(d.dormant, true);

  assert.equal(d.observe({ ts: 2, empty: false }, T0 + 10000), 'wake');
  assert.equal(d.dormant, false);
});

test('the restamp clock restarts when a genuinely fresh payload arrives', () => {
  const d = state({ restampMs: 45000 });
  d.observe({ ts: 1, empty: true }, T0);
  d.observe({ ts: 2, empty: true }, T0 + 40000);          // fresh: counts, resets the clock
  assert.equal(d.streak, 2);
  d.observe({ ts: 2, empty: true }, T0 + 80000);          // 40s since that one — too soon
  assert.equal(d.streak, 2, 'restamp is measured from the last counted observation');
  d.observe({ ts: 2, empty: true }, T0 + 85001);
  assert.equal(d.dormant, true);
});

// --- silence IS the empty answer on a streaming channel ---------------------
//
// patch-routeros.js deliberately swallows RouterOS's `!empty` on a streaming
// channel, because there it means "nothing YET" — frequency-scan sends it ~6 ms
// before delivering real rows. The cost is that a stream-mode collector on a
// router with an empty table receives no packet at all, not even the [] its data
// handler is written for. That is why this looked fixed on the hAP AC2 (poll
// mode, one-shot writes, where the same patch DOES yield an empty result) and
// was still broken on the cAP AX (stream mode).

test('the patch really does swallow !empty on a stream', () => {
  // Pinned because the collector fixes below only make sense against it, and a
  // future patch edit that "helpfully" emitted [] would silently make them dead
  // code — while breaking the frequency-scan case the swallow exists for.
  const patch = fs.readFileSync(path.join(__dirname, '..', 'patch-routeros.js'), 'utf8');
  assert.ok(/if \(reply === '!empty'\) \{ if \(this\.streaming\) return;/.test(patch),
    'streaming !empty must stay swallowed');
  assert.ok(/this\.emit\('done', \[\]\); return;/.test(patch),
    'and a one-shot !empty must still yield an empty result');
});

test('talkers commits on prolonged stream silence', () => {
  const h = harness();
  const c = h.make({ streamMode: true });
  c._startStream();
  c._startSilenceTimer();
  assert.ok(c._silenceTimer, 'stream mode arms a silence timer');

  // No packet has arrived at all — the cAP AX shape.
  assert.equal(c.lastPayload, null);
  c._commitTick();                       // what the timer callback does
  assert.ok(c.lastPayload, 'silence produces the empty payload the supervisor needs');
  assert.deepEqual(c.lastPayload.devices, []);
  c.stop();
});

test('rows arriving suppress the silence inference', () => {
  const h = harness();
  const c = h.make({ streamMode: true });
  c._startStream();
  h.streamHandlers.data({ 'mac-address': '00:00:5E:00:53:00', name: 'a', 'rate-up': '1', 'rate-down': '2' });
  assert.equal(c._sawData, true, 'a real packet marks the stream as alive');
  clearTimeout(c._commitTimer);
  c.stop();
});

test('the silence timer is armed and disarmed across the lifecycle', () => {
  const h = harness();
  const c = h.make({ streamMode: true });
  c.start();
  assert.ok(c._silenceTimer);
  c.suspend();
  assert.equal(c._silenceTimer, null, 'suspend must not leave it running');
  c.resume();
  assert.ok(c._silenceTimer);
  c.stop();
  assert.equal(c._silenceTimer, null, 'stop must not leak the interval');
});

test('connections asks the router what silence means before restarting', () => {
  // The watchdog used to restart on silence alone. On an AP with connection
  // tracking off that is a restart every 20 s forever, plus a "stream degraded"
  // banner — constant load on the smallest hardware we support.
  const SRC = fs.readFileSync(path.join(__dirname, '..', 'src', 'collectors', 'connections.js'), 'utf8');
  // Anchor on the DEFINITION, not the constructor's call site — the bare name
  // matches that first and slices four useless lines.
  const wd = SRC.slice(SRC.indexOf('_startWatchdog() {'));
  const body = wd.slice(0, wd.indexOf('\n  }\n') + 4);
  assert.ok(/_confirmSilence\(age\)/.test(body),
    'the watchdog must probe, not restart, when the stream has merely gone quiet');
  assert.ok(!/recordRestart\(\)\);\s*\n\s*this\._restartStream\(\);/.test(body),
    'the unconditional restart is gone from the silence branch');

  const probe = SRC.slice(SRC.indexOf('async _confirmSilence'));
  const pbody = probe.slice(0, probe.indexOf('\n  }\n') + 4);
  assert.ok(/rows\.length === 0/.test(pbody), 'empty means quiet, not dead');
  assert.ok(/_restartStream\(\)/.test(pbody), 'rows back means the stream really is broken');
  assert.ok(/_silenceProbe/.test(pbody), 'and only one probe may be in flight');
});

test('a confirmed-empty connection table is restated faster than the card expires', () => {
  // The card's threshold is pollMs + STALE_GRACE. Driving the empty restatement
  // off the watchdog's age gate meant an emit every 20-30 s against a 23 s
  // deadline, so connCard flickered stale on an AP with tracking off even after
  // the restart loop was fixed.
  const SRC = fs.readFileSync(path.join(__dirname, '..', 'src', 'collectors', 'connections.js'), 'utf8');
  const wd = SRC.slice(SRC.indexOf('_startWatchdog() {'));
  const body = wd.slice(0, wd.indexOf('\n  }\n') + 4);

  const checkMs = body.match(/const checkMs\s*=\s*(.+);/);
  assert.ok(checkMs, 'watchdog still derives a check interval');
  assert.ok(/_emptyConfirmed/.test(body), 'the empty restatement runs on the watchdog cadence');
  const emptyIdx = body.indexOf('this._emptyConfirmed');
  const ageIdx   = body.indexOf('age > staleMs');
  assert.ok(emptyIdx > -1 && ageIdx > -1 && emptyIdx < ageIdx,
    'it must run BEFORE the age gate, or refreshing lastConnsTs stops the gate ever tripping');

  // checkMs = max(pollMs*2, 10s) beats threshold = pollMs + 20s for every
  // interval the UI actually offers.
  const STALE_GRACE = 20000;
  [1000, 3000, 5000, 10000].forEach(function (pollMs) {
    const check = Math.max(pollMs * 2, 10000);
    assert.ok(check < pollMs + STALE_GRACE,
      'pollMs ' + pollMs + ': restatement every ' + check + 'ms must beat ' + (pollMs + STALE_GRACE) + 'ms');
  });
});

test('an empty table is still re-verified against the router now and then', () => {
  const SRC = fs.readFileSync(path.join(__dirname, '..', 'src', 'collectors', 'connections.js'), 'utf8');
  assert.ok(/const EMPTY_REPROBE_MS = \d+;/.test(SRC));
  assert.ok(/_lastEmptyProbeTs > EMPTY_REPROBE_MS.*_confirmSilence/.test(SRC),
    'a table that fills while the stream is dead must not be reported empty forever');
});
