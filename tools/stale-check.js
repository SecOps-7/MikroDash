'use strict';
/**
 * Stale detection, live against ported.
 *
 * ── WHAT IS COMPARED, AND WHAT IS NOT ───────────────────────────────────────
 *
 * The live block mixes pure state machinery with socket wiring for events this
 * port's server does not emit. The MACHINERY is what is compared here, driven
 * identically on both sides: the timers, the two markings, the sweep, the reset
 * and the row clear.
 *
 * `UNWIRED` below records the handlers whose events have no Go emitter yet, and
 * asserts the live source still registers them — so the day one is emitted, this
 * gate fails and the note has to go rather than being left lying.
 *
 * ── TIME IS STEPPED, NOT REAL ───────────────────────────────────────────────
 *
 * Every threshold is measured in milliseconds against `Date.now()`, so both
 * sides get a clock the harness advances. A test that slept would be slow and
 * flaky; one that used the real clock could not reach a 345-second threshold at
 * all.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/stale-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('stale-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));
const T = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'stale-tables.json'), 'utf8'));

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent, and this then threw `cannot find <name>` at module scope — before
  // any frozen output could be served. An empty slice is harmless because the
  // live half is never entered when the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name + ' — it has moved or been rewritten');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}

const UNWIRED = [
  ["socket.on('collection:status'", 'per-router collection status has no Go emitter yet'],
  ["socket.on('collection:config'", 'per-router collection config has no Go emitter yet'],
  ["socket.on('router:switching'", "this port's server announces router:active/switched AFTER the " +
    'move instead, so the clear happens where the client asks'],
  ["socket.on('ping:update'", 'the ping block is not ported'],
  ["socket.on('traffic:update'", 'the traffic card belongs to the unported Dashboard'],
];
// GUARDED: this asks whether the live source still contains each listener, so
// that UNWIRED cannot quietly describe a page that moved on. With no source
// there is nothing for the list to go stale against.
const goneUnwired = LIFT.hasReference(ROOT)
  ? UNWIRED.filter(([needle]) => !src.includes(needle)) : [];
if (goneUnwired.length) {
  console.error('UNWIRED is out of date — these are no longer in the live source:\n' +
    goneUnwired.map(([n, why]) => '  ' + why + '  (' + n + ')').join('\n') + '\n');
  process.exit(1);
}

const OUT = path.join(ROOT, 'testdata', '.stale-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'stale.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

const CARD_IDS = T.cards.map((c) => c.cardId);
const ALL_IDS = [...new Set(CARD_IDS.concat(['trafficCard'], Object.values(T.dashCardTables)))];

function makeWorld(missing) {
  const nodes = {};
  for (const id of ALL_IDS) {
    if ((missing || []).includes(id)) continue;
    const classes = new Set();
    const overlay = { textContent: '● stale' };
    nodes[id] = {
      id, innerHTML: '<tr><td>previous router</td></tr>',
      classList: {
        add: (c) => classes.add(c), remove: (c) => classes.delete(c),
        contains: (c) => classes.has(c),
        toggle: (c, on) => { if (on) classes.add(c); else classes.delete(c); },
        _set: classes,
      },
      querySelector: (sel) => (sel === '.stale-overlay' ? overlay : null),
      _overlay: overlay,
    };
  }
  return {
    doc: { getElementById: (id) => nodes[id] || null },
    nodes,
    state() {
      return JSON.stringify(ALL_IDS.map((id) => {
        const n = nodes[id];
        return n ? [id, [...n.classList._set].sort(), n._overlay.textContent, n.innerHTML] : [id, null];
      }), null, 1);
    },
  };
}

// A clock both sides read.
let CLOCK = 1773567000000;
const clockFn = () => CLOCK;

function liveRun(missing, body) {
  const w = makeWorld(missing);
  const handlers = {};
  const ctx = {
    document: { ...w.doc, addEventListener() {} },
    Object, Array, JSON, String, Date: { now: clockFn },
    setInterval: () => 0,
    $: (id) => w.doc.getElementById(id),
    COLLECTOR_CARDS: JSON.parse(JSON.stringify(T.collectorCards)),
    _DASH_CARD_TABLES: JSON.parse(JSON.stringify(T.dashCardTables)),
    STALE_GRACE: T.staleGrace,
    staleConfig: JSON.parse(JSON.stringify(T.cards)),
    staleTimers: {},
    _collectionOff: {}, _collectionDormant: {},
    socket: { on: (n, f) => { (handlers[n] = handlers[n] || []).push(f); } },
  };
  vm.createContext(ctx);
  vm.runInContext([
    slice('function clearDashboardData() {', '\n}', 'clearDashboardData'),
    slice('function _collectionOffCard(cardId){', '\n', '_collectionOffCard'),
    slice('function _collectionDormantCard(cardId){', '\n', '_collectionDormantCard'),
    slice("socket.on('collection:status', function (st) {", '\n});', 'the status handler'),
    slice("socket.on('collection:config', function (cfg) {", '\n});', 'the config handler'),
    slice('function _resetStaleTimers() {', '\n}', '_resetStaleTimers'),
    // The per-event subscriptions and the sweep, both lifted whole.
    slice('staleConfig.forEach(function(cfg){\n  staleTimers[cfg.cardId]=0;', '\n});',
      'the per-card subscriptions'),
    slice('setInterval(function(){\n  var now=Date.now();', '\n},3000);', 'the sweep')
      .replace(/^setInterval\(function\(\)\{/, 'function __sweep(){').replace(/\},3000\);$/, '}'),
  ].join('\n'), ctx);
  body({
    clear: () => ctx.clearDashboardData(),
    reset: () => ctx._resetStaleTimers(),
    payload: (event, d) => { for (const f of (handlers[event] || [])) f(d); },
    status: (dormant) => { for (const f of (handlers['collection:status'] || [])) f({ dormant }); },
    config: (enabled) => { for (const f of (handlers['collection:config'] || [])) f({ enabled }); },
    sweep: () => ctx.__sweep(),
  }, w);
  return w.state();
}

function portRun(missing, body) {
  const w = makeWorld(missing);
  const keys = ['document', 'Date', 'setInterval'];
  const saved = {};
  for (const k of keys) saved[k] = global[k];
  global.document = w.doc;
  global.Date = { now: clockFn };
  global.setInterval = () => 0;
  try {
    delete require.cache[require.resolve(OUT)];
    const m = require(OUT);
    body({
      clear: () => m.clearDashboardData(),
      reset: () => m.resetStaleTimers(),
      payload: (event, d) => { for (const id of m.cardsForEvent(event)) m.notePayload(id, d && d.pollMs); },
      status: (dormant) => m.applyCollectionStatus(dormant),
      config: (enabled) => m.applyCollectionConfig(enabled),
      sweep: () => m.sweepStale(CLOCK),
    }, w);
  } finally {
    for (const k of keys) {
      if (saved[k] === undefined) delete global[k]; else global[k] = saved[k];
    }
  }
  return w.state();
}

const bad = [];
let cases = 0;
function compare(what, missing, act) {
  cases++;
  const t0 = CLOCK;
  // The live run mutates CLOCK; the reset below it stays outside the closure so
  // the port run still starts from t0 whether or not the live half ran.
  const a = G.live(what, () => liveRun(missing, act));
  CLOCK = t0;
  const b = portRun(missing, act);
  CLOCK = t0;
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}
const advance = (ms) => { CLOCK += ms; };

// ── The row clear ───────────────────────────────────────────────────────────
compare('clearDashboardData empties every row table', [], (api) => api.clear());
compare('clear with one tbody missing', [Object.values(T.dashCardTables)[0]], (api) => api.clear());

// ── Timers and the sweep ────────────────────────────────────────────────────
compare('a sweep before anything has arrived leaves everything alone', [], (api) => api.sweep());
compare('reset, then sweep immediately', [], (api) => { api.reset(); api.sweep(); });
// Each card, just under and just over its own threshold.
for (const c of T.cards) {
  compare('a payload for ' + c.cardId + ', swept just under its threshold', [], (api) => {
    api.payload(c.event, {});
    advance(c.threshold - 1);
    api.sweep();
  });
  compare('a payload for ' + c.cardId + ', swept just over its threshold', [], (api) => {
    api.payload(c.event, {});
    advance(c.threshold + 1);
    api.sweep();
  });
}
// EXACTLY at the threshold, which is where `>` and `>=` disagree and nowhere
// else. Both neighbours were already covered and neither could see it.
for (const c of T.cards.slice(0, 6)) {
  compare('a payload for ' + c.cardId + ', swept exactly AT its threshold', [], (api) => {
    api.payload(c.event, {});
    advance(c.threshold);
    api.sweep();
  });
}

// ── Three rules the sweep hides until the marking is LIFTED ─────────────────
//
// While a card is off or dormant the sweep re-asserts it every pass and zeroes
// the timer, so a version that failed to zero it at the moment the marking
// arrived looks identical. The difference only becomes visible after the
// marking goes away, because what happens then depends on whether the timer is
// still 0: zero means "restart the clock", anything else means "carry on from
// where you were", and those diverge by the whole time the collector was off.
// The advance AFTER re-enabling is deliberately SHORT. With the timer correctly
// left at 0 through the off period, re-enabling restarts the clock and a second
// later the card is fine; with it re-armed by the reset instead, the clock has
// been running the whole time the collector was off and the card is stale the
// moment it comes back. Both are "stale" if the final advance is long enough,
// which is why the first version of this case could not tell them apart.
compare('a disabled collector re-enabled after a reset', [], (api) => {
  api.config({ routing: false });
  api.reset();
  advance(200000);
  api.config({ routing: true });
  advance(1000);
  api.sweep();
});
compare('a dormant card that had a payload before it slept, then wakes', [], (api) => {
  api.payload('routing:update', {});
  api.status(['routing']);
  advance(200000);
  api.status([]);
  advance(95000);
  api.sweep();
});
compare('a dormant card that wakes and is swept immediately', [], (api) => {
  api.payload('routing:update', {});
  api.status(['routing']);
  advance(200000);
  api.status([]);
  api.sweep();
});

// A later payload clears the mark again.
compare('stale, then a payload arrives', [], (api) => {
  api.payload('dns:update', {});
  advance(500000);
  api.sweep();
  api.payload('dns:update', {});
  api.sweep();
});
// pollMs retunes the threshold — up and down.
for (const pollMs of [1000, 60000, 300000]) {
  compare('pollMs=' + pollMs + ' retunes the threshold', [], (api) => {
    api.payload('dns:update', { pollMs });
    advance(pollMs + T.staleGrace + 1);
    api.sweep();
  });
  compare('pollMs=' + pollMs + ' is not yet exceeded', [], (api) => {
    api.payload('dns:update', { pollMs });
    advance(pollMs + T.staleGrace - 1);
    api.sweep();
  });
}
// pollMs of 0 means STREAMED: the fixed threshold stays.
//
// The advance has to land BETWEEN the two answers. `dnsCard` is fixed at 40s;
// treating 0 as a poll interval would make it 0 + 20s grace. At 41s both call
// the card stale and the case proves nothing — at 30s only the wrong one does.
compare('pollMs=0 keeps the fixed threshold', [], (api) => {
  api.payload('dns:update', { pollMs: 0 });
  advance(30000);
  api.sweep();
});
compare('pollMs=0 still goes stale at the FIXED threshold', [], (api) => {
  api.payload('dns:update', { pollMs: 0 });
  advance(41000);
  api.sweep();
});
// One event feeding several cards — routing:update feeds four.
compare('routing:update re-arms all four of its cards', [], (api) => {
  api.payload('routing:update', {});
  advance(95000);
  api.sweep();
});
// The reset re-arms everything, which is the whole point of it.
compare('stale, then a reset', [], (api) => {
  api.payload('dns:update', {});
  advance(500000);
  api.sweep();
  api.reset();
  api.sweep();
});

// ── Disabled and dormant ────────────────────────────────────────────────────
for (const key of Object.keys(T.collectorCards)) {
  compare('collector ' + key + ' switched off', [], (api) => {
    api.config({ [key]: false });
    advance(500000);
    api.sweep();
  });
  compare('collector ' + key + ' dormant', [], (api) => {
    api.status([key]);
    advance(500000);
    api.sweep();
  });
  // Off OUTRANKS dormant, whichever order they arrive in.
  compare('collector ' + key + ' off then dormant', [], (api) => {
    api.config({ [key]: false });
    api.status([key]);
    api.sweep();
  });
  compare('collector ' + key + ' dormant then off', [], (api) => {
    api.status([key]);
    api.config({ [key]: false });
    api.sweep();
  });
}
// Switched back on: the countdown restarts and the overlay text goes back.
compare('a collector switched off then on again', [], (api) => {
  api.config({ routing: false });
  api.sweep();
  api.config({ routing: true });
  advance(95000);
  api.sweep();
});
// A card re-rendered after the event would lose its class; the sweep re-asserts.
compare('the sweep re-asserts a disabled marking', [], (api) => {
  api.config({ routing: false });
  api.sweep();
  api.sweep();
});
compare('dormancy lifted', [], (api) => {
  api.status(['routing']);
  api.sweep();
  api.status([]);
  advance(95000);
  api.sweep();
});
compare('a status payload that is not an array is ignored', [], (api) => {
  api.status(['routing']);
  api.status('nope');
  api.sweep();
});
compare('a config with no enabled map is ignored', [], (api) => {
  api.config({ routing: false });
  api.config(undefined);
  api.sweep();
});
// A reset does NOT re-arm a switched-off card.
compare('reset does not re-arm a disabled collector', [], (api) => {
  api.config({ routing: false });
  api.reset();
  advance(500000);
  api.sweep();
});

fs.rmSync(OUT, { force: true });
if (bad.length) {
  console.error('stale detection differs from the live app:\n\n' + bad.slice(0, 2).join('\n\n') +
    (bad.length > 2 ? '\n\n… and ' + (bad.length - 2) + ' more' : '') + '\n');
  process.exit(1);
}
console.log(`stale detection matches the live app (${cases} cases, ${T.cards.length} cards, ` +
  `${UNWIRED.length} handlers awaiting a Go emitter)`);
