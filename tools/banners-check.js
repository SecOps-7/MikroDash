'use strict';
/**
 * The two banners and the topbar clock, live against ported.
 *
 * ── WHY THIS GATE EXISTS AT ALL ─────────────────────────────────────────────
 *
 * The port already had banner code, and it was wrong in five ways at once: it
 * wrote `style.display` where the live app toggles a `.show` class, set neither
 * body class, never paused the diagram, never blanked the live rates, and kept
 * no memory of the router's state across a reconnect. Every one of those is
 * invisible to a payload gate and to a screenshot of a healthy system. The
 * failure they add up to is the bad kind: a browser that reconnects to a server
 * whose router is still down clears the red banner and shows nothing at all —
 * the most reassuring possible display of a broken system.
 *
 * ── THE SEQUENCES ARE THE POINT ─────────────────────────────────────────────
 *
 * Every interesting rule here is about ORDER, so the cases are sequences:
 *
 *   amber suppressed by red   a RouterOS outage reported while the socket is
 *                             down must not raise the amber banner; told both,
 *                             an operator learns nothing from the second.
 *   the flag outlives the socket
 *                             disconnect, reconnect, and the router is STILL
 *                             down: the amber banner has to come back without
 *                             waiting for another status event.
 *   the diagram resumes only when BOTH are back
 *                             and only if the tab is visible.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/banners-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/banners-check.js --freeze
const G = L.golden('banners-check');
const src = L.liveSource(ROOT);

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
const rosSrc = slice('function setRosBanner(connected, reason){', '\n}', 'setRosBanner');
const clockSrc = slice("(function(){\n  var el = $('tobarClock');", '})();', 'the topbar clock');
// The socket handlers are lifted as their callback BODIES: the live ones are
// registered inline on `socket`, so the shim below captures them by intercepting
// `socket.on` rather than re-typing what they do.
const discSrc = slice("socket.on('disconnect',function(){", '\n});', 'the disconnect handler');
const connSrc = slice("socket.on('connect',function(){", '\n});', 'the connect handler');

// The clock reads its timezone from caps.ts, which owns it — so the bundle is
// built from a small entry that re-exports both. Driving the timezone through
// the REAL `applyPageVisibility` rather than a test setter is the point: it is
// the same path a settings broadcast takes, so this also pins that the clock is
// reading the value that broadcast actually writes.
const ENTRY = path.join(ROOT, 'testdata', '.banners-entry.ts');
fs.writeFileSync(ENTRY,
  "export * from '../web/src/banners.js';\n" +
  "export { applyPageVisibility } from '../web/src/caps.js';\n");
const OUT = path.join(ROOT, 'testdata', '.banners-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── Time is frozen ──────────────────────────────────────────────────────────
//
// The clock renders "now". The two runs happen microseconds apart, and a second
// boundary between them would produce a difference that is real and meaningless.
// Both sides get the same fixed instant instead, so a difference can only come
// from the formatting.
const FIXED = 1773567045123; // 2026-03-15T10:30:45.123Z
function FrozenDate(...args) {
  return args.length ? new RealDate(...args) : new RealDate(FIXED);
}
const RealDate = Date;
FrozenDate.now = () => FIXED;
FrozenDate.prototype = RealDate.prototype;

const IDS = ['rosBanner', 'rosBannerText', 'reconnectBanner', 'liveRx', 'liveTx', 'netDiagram', 'tobarClock'];

function makeWorld(missing, hidden) {
  const nodes = {};
  const anim = [];
  for (const id of IDS) {
    if ((missing || []).includes(id)) continue;
    const classes = new Set();
    let text = '';
    let writes = 0;
    nodes[id] = {
      _id: id,
      // COUNTED, not just stored. "Write only when the string changed" is a
      // guard whose whole effect is the absence of a write, so the final value
      // cannot see it — at one tick a second it is 59 avoided DOM writes a
      // minute, and a port without it looks identical in every other respect.
      get textContent() { return text; },
      set textContent(v) { text = String(v); writes++; },
      get _writes() { return writes; },
      classList: {
        add: (c) => classes.add(c), remove: (c) => classes.delete(c),
        contains: (c) => classes.has(c), _set: classes,
      },
    };
  }
  if (nodes.netDiagram) {
    nodes.netDiagram.pauseAnimations = () => anim.push('pause');
    nodes.netDiagram.unpauseAnimations = () => anim.push('unpause');
  }
  const bodyClasses = new Set();
  const doc = {
    hidden: !!hidden,
    getElementById: (id) => nodes[id] || null,
    // Empty, and deliberately so. The port reaches the clock's timezone through
    // the real `applyPageVisibility`, which also runs the nav sweep — a sweep
    // this gate is not about and `caps-check.js` already covers in full. With no
    // nav items to find it touches nothing that is compared here.
    querySelectorAll: () => [],
    body: { classList: { add: (c) => bodyClasses.add(c), remove: (c) => bodyClasses.delete(c) } },
  };
  return {
    doc, nodes, anim,
    state() {
      return JSON.stringify({
        nodes: IDS.map((id) => (nodes[id]
          ? [id, [...nodes[id].classList._set].sort(), nodes[id].textContent, nodes[id]._writes]
          : [id, null])),
        body: [...bodyClasses].sort(),
        anim,
      }, null, 1);
    },
  };
}

function liveRun(missing, hidden, body) {
  const w = makeWorld(missing, hidden);
  const handlers = {};
  let ticker = null;
  const ctx = {
    document: w.doc, String, Array, JSON, Object, Intl,
    setInterval: (fn) => { ticker = fn; return 0; },
    Date: FrozenDate,
    $: (id) => w.doc.getElementById(id),
    rosBanner: w.doc.getElementById('rosBanner'),
    rosBannerText: w.doc.getElementById('rosBannerText'),
    reconnectBanner: w.doc.getElementById('reconnectBanner'),
    liveRx: w.doc.getElementById('liveRx'),
    liveTx: w.doc.getElementById('liveTx'),
    _rosCurrentlyDisconnected: false,
    _displayTimezone: '',
    // Everything the connect handler touches that belongs to blocks this gate
    // is not about. Stubbed rather than lifted, and named so it is obvious which
    // parts of that handler are NOT being compared here.
    _sysMetaWritten: false, currentIf: '', allPoints: [], _currentPage: 'dashboard',
    _resetStaleTimers() {}, fetch: () => ({ then: () => ({ then: () => ({ catch() {} }) }) }),
    CustomEvent: function (n, o) { return { type: n, detail: o && o.detail }; },
    window: {},
    socket: { on: (n, f) => { handlers[n] = f; }, emit() {} },
  };
  ctx.document.dispatchEvent = () => {};
  vm.createContext(ctx);
  vm.runInContext(rosSrc + '\n' + discSrc + '\n' + connSrc, ctx);
  body({
    ros: (connected, reason) => ctx.setRosBanner(connected, reason),
    disconnect: () => handlers.disconnect(),
    connect: () => handlers.connect(),
    clock: (tz) => { ctx._displayTimezone = tz || ''; vm.runInContext(clockSrc, ctx); },
    tickAgain: () => { if (ticker) ticker(); },
  }, w);
  return w.state();
}

function portRun(missing, hidden, body) {
  const w = makeWorld(missing, hidden);
  const keys = ['document', 'setInterval', 'Date'];
  const saved = {};
  for (const k of keys) saved[k] = global[k];
  let ticker = null;
  global.document = w.doc;
  global.setInterval = (fn) => { ticker = fn; return 0; };
  global.Date = FrozenDate;
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    body({
      ros: (connected, reason) => mod.setRosBanner(connected, reason),
      disconnect: () => mod.onSocketDisconnect(),
      connect: () => mod.onSocketConnect(),
      clock: (tz) => { mod.applyPageVisibility({ displayTimezone: tz || '' }); mod.initClock(); },
      tickAgain: () => { if (ticker) ticker(); },
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
function compare(what, missing, hidden, act) {
  cases++;
  const a = G.live(G.seq(), () => liveRun(missing, hidden, act));
  const b = portRun(missing, hidden, act);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

// ── The RouterOS banner on its own ──────────────────────────────────────────
compare('routeros goes down', [], false, (api) => api.ros(false, 'connection refused'));
compare('routeros goes down with no reason', [], false, (api) => api.ros(false));
compare('routeros goes down with a null reason', [], false, (api) => api.ros(false, null));
compare('routeros goes down with an empty reason', [], false, (api) => api.ros(false, ''));
compare('routeros comes back', [], false, (api) => { api.ros(false, 'refused'); api.ros(true); });
compare('routeros down twice', [], false, (api) => { api.ros(false, 'a'); api.ros(false, 'b'); });
compare('routeros up when it was never down', [], false, (api) => api.ros(true));
// The tab being hidden stops the diagram resuming.
compare('routeros comes back with the tab hidden', [], true,
  (api) => { api.ros(false, 'refused'); api.ros(true); });

// ── The socket banner ───────────────────────────────────────────────────────
compare('the socket drops', [], false, (api) => api.disconnect());
compare('the socket drops and returns', [], false, (api) => { api.disconnect(); api.connect(); });
compare('the socket returns having never dropped', [], false, (api) => api.connect());

// ── The two together, which is where the rules live ─────────────────────────
compare('routeros down, then the socket drops', [], false,
  (api) => { api.ros(false, 'refused'); api.disconnect(); });
compare('the socket drops, THEN routeros is reported down — amber suppressed', [], false,
  (api) => { api.disconnect(); api.ros(false, 'refused'); });
compare('down, drop, reconnect — the amber banner must COME BACK', [], false,
  (api) => { api.ros(false, 'refused'); api.disconnect(); api.connect(); });
compare('routeros fine, drop, reconnect — no amber banner', [], false,
  (api) => { api.ros(true); api.disconnect(); api.connect(); });
compare('down, drop, reconnect, then routeros recovers', [], false,
  (api) => { api.ros(false, 'refused'); api.disconnect(); api.connect(); api.ros(true); });
compare('down, drop, reconnect with the tab hidden', [], true,
  (api) => { api.ros(false, 'refused'); api.disconnect(); api.connect(); });
compare('a full flap, twice', [], false, (api) => {
  api.ros(false, 'refused'); api.disconnect(); api.connect(); api.ros(true);
  api.ros(false, 'again'); api.disconnect(); api.connect();
});

// ── The clock ───────────────────────────────────────────────────────────────
//
// Two formatters that must agree at the same instant. The browser-local branch
// builds the string by hand from getHours/getMinutes/getSeconds; the timezone
// branch hands the whole job to Intl. A port that used Intl for both would
// render the same string here and a different one in any locale whose default
// format is not HH:MM:SS.
for (const tz of ['', 'UTC', 'Europe/Berlin', 'America/New_York', 'Asia/Kolkata',
                  'Pacific/Chatham', 'Australia/Eucla']) {
  compare('the clock with timezone ' + JSON.stringify(tz), [], false, (api) => api.clock(tz));
}
// Ticking again at the SAME instant must write NOTHING. Time is frozen, so the
// string is identical and the guard is the only thing that can tell the two
// implementations apart.
for (const tz of ['', 'UTC', 'Asia/Kolkata']) {
  compare('the clock ticks twice at the same instant, tz=' + JSON.stringify(tz), [], false,
    (api) => { api.clock(tz); api.tickAgain(); api.tickAgain(); });
}
// A timezone the runtime does not know: Intl throws a RangeError, so BOTH sides
// throw. What is compared is that neither wrote a half-formatted string first.
compare('the clock with an unknown timezone', [], false, (api) => {
  try { api.clock('Mars/Olympus_Mons'); } catch { /* both throw; the DOM is what matters */ }
});
// The element absent is a no-op — the live version returns before it does
// anything, including before it starts the interval.
compare('the clock with no element', ['tobarClock'], false, (api) => api.clock('UTC'));

// ── Missing elements ────────────────────────────────────────────────────────
//
// Only the ones the LIVE code actually guards are compared here. `setRosBanner`
// opens with `if(!rosBanner) return;`, and `rosBannerText`, `netDiagram`,
// `liveRx` and `liveTx` are each tested before use — so a missing one is a
// no-op on both sides and that is worth pinning.
//
// `rosBanner` and `reconnectBanner` are NOT in this list, and the reason is a
// stated difference rather than an oversight. The socket handlers dereference
// both without a guard, so in the live app a missing banner element throws a
// TypeError inside the handler; this port optional-chains and carries on. The
// shell markup contains both, so neither path is reachable — but the port does
// not reproduce a crash, and pretending the two agree by omitting the case
// would be worse than saying so.
for (const missing of [['rosBannerText'], ['netDiagram'], ['liveRx'], ['liveRx', 'liveTx']]) {
  compare('without ' + missing.join('+'), missing, false,
    (api) => { api.ros(false, 'refused'); api.disconnect(); api.connect(); });
}
// `rosBanner` missing, through the one entry point that guards it.
compare('setRosBanner without its banner element', ['rosBanner'], false,
  (api) => { api.ros(false, 'refused'); api.ros(true); });

fs.rmSync(OUT, { force: true });
if (bad.length) {
  console.error('the banners differ from the live ones:\n\n' + bad.slice(0, 2).join('\n\n') +
    (bad.length > 2 ? '\n\n… and ' + (bad.length - 2) + ' more' : '') + '\n');
  process.exit(1);
}
console.log(`banners match the live ones (${cases} cases, sequences included)`);
