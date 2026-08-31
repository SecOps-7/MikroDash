'use strict';
/**
 * The Dashboard's System card, live against ported.
 *
 * ── IT IS A SEQUENCE, NOT A RENDER ──────────────────────────────────────────
 *
 * Three of this card's rules are about what happened BEFORE the payload in
 * hand, so a corpus of single payloads cannot reach them: the meta line is
 * written once and re-armed by a reset, the temperature slot is created on
 * first sight and updated in place after, and a hidden tab keeps its data
 * pending rather than dropping it. Every case here is therefore a SCRIPT — a
 * list of steps, each an update, a visibility change, a frame or a reset — and
 * the whole DOM is compared after every step, not at the end.
 *
 * Comparing only the final state was tried first and is not enough: it missed
 * a port that rendered a hidden tab's payload immediately, because by the last
 * step both sides had converged on the same numbers anyway.
 *
 * ── THE FAKE DOM HAS TO MODEL ONE THING HONESTLY ────────────────────────────
 *
 * Writing `innerHTML` on `sysMeta` destroys its children, and `sysMetaTemp` is
 * one of them. If the fake DOM kept that node findable afterwards, the ordering
 * rule this card depends on could not be tested at all — the live code looks up
 * the slot AFTER rewriting the meta line precisely so the next temperature
 * recreates it. So `innerHTML =` unregisters the subtree, exactly as a browser
 * would, and the ordering mutation is caught rather than silently passed.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/system-card-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('system-card-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

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

const flushSrc = slice('function _flushSysUpdate()', '\n}', '_flushSysUpdate');
// GUARDED: each asks whether the lifted SLICE still contains a marker.
for (const must of LIFT.hasReference(ROOT) ? ['document.hidden', '_sysMetaWritten', 'sysMetaTemp', 'ros-update-row'] : []) {
  if (!flushSrc.includes(must)) throw new Error('the _flushSysUpdate slice lost ' + must);
}
const gaugeSrc = slice('function gauge(label, pct, cls)', '\n}', 'gauge');
const helperSrc = ['function _rotPt(', 'function _lp(', 'function _v(']
  .map((d) => slice(d, '\n}', d)).join('\n');
const escSrc = slice('function esc(', '\n}', 'esc');
const upSrc = slice('function parseUptime(', '\n}', 'parseUptime');
const bytesSrc = slice('function fmtBytes(', '\n}', 'fmtBytes');

const ENTRY = path.join(ROOT, 'testdata', '.syscard-entry.ts');
fs.writeFileSync(ENTRY,
  "export { flushSysUpdate, noteSystemUpdate, flushPendingSystem, resetSysMeta }" +
  " from '../web/src/pages/dashboard-system.js';\n");
const OUT = path.join(ROOT, 'testdata', '.syscard-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── the fake DOM ───────────────────────────────────────────────────────────
function makeDom() {
  const byId = new Map();
  function node(id, tag) {
    const n = {
      id: id || '', tagName: tag || 'DIV', className: '', style: { display: 'none' },
      textContent: '', children: [],
      set innerHTML(v) {
        // A browser detaches the old subtree; so does this. That is what makes
        // the sysMetaTemp ordering rule testable.
        for (const c of this.children) if (c.id) byId.delete(c.id);
        this.children = [];
        this._html = v;
      },
      get innerHTML() { return this._html; },
      _html: '',
      appendChild(c) { this.children.push(c); if (c.id) byId.set(c.id, c); return c; },
    };
    if (id) byId.set(id, n);
    return n;
  }
  const dispatched = [];
  const doc = {
    hidden: false,
    getElementById: (id) => byId.get(id) || null,
    createElement: (tag) => node('', tag.toUpperCase()),
    dispatchEvent: (e) => { dispatched.push({ type: e.type, detail: e.detail }); return true; },
  };
  // Every id the card writes to. `sysMetaTemp` is deliberately absent — it is
  // created lazily, and seeding it would skip the branch that creates it.
  for (const id of ['uptimeDisplay', 'uptimeChip', 'gaugeRow', 'sysMeta', 'rosUpdateRow']) node(id, 'DIV');
  return { doc, byId, dispatched, node };
}

// What a step is allowed to leave behind, read back the same way from both.
function snapshot(dom, runner) {
  // FRAMES BOOKED, not just the DOM. Two mutations survived an earlier version
  // of this gate: booking a frame per payload instead of one per burst, and
  // never clearing `pending` after a render. Both leave the DOM identical —
  // they render the same numbers — and both undo the only thing the RAF hop
  // exists for. Counting the bookings is what makes the coalescing checkable.
  const out = {
    dispatched: dom.dispatched.map((e) => JSON.stringify(e)),
    framesBooked: runner.booked(),
  };
  for (const id of ['uptimeDisplay', 'uptimeChip', 'gaugeRow', 'sysMeta', 'rosUpdateRow']) {
    const n = dom.byId.get(id);
    out[id] = n ? { html: n.innerHTML, text: n.textContent, display: n.style.display } : null;
  }
  // The temperature slot: whether it EXISTS, whether it is still attached to
  // sysMeta, and what it says. A detached node that is still being updated
  // shows up here as attached:false with fresh content.
  const t = dom.byId.get('sysMetaTemp');
  const meta = dom.byId.get('sysMeta');
  out.temp = t ? { html: t.innerHTML, attached: !!meta && meta.children.includes(t) } : null;
  return JSON.stringify(out);
}

function liveRunner() {
  const dom = makeDom();
  const frames = [];
  let booked = 0;
  const ctx = {
    Math, parseInt, String, JSON, RegExp, Number, Date,
    document: dom.doc,
    CustomEvent: function (type, init) { return { type, detail: init && init.detail }; },
    requestAnimationFrame: (fn) => { booked++; frames.push(fn); return frames.length; },
    $: (id) => dom.doc.getElementById(id),
  };
  // The live function reads these as free variables off the window.
  for (const id of ['uptimeDisplay', 'uptimeChip', 'gaugeRow', 'sysMeta', 'rosUpdateRow']) {
    ctx[id] = dom.byId.get(id);
  }
  vm.createContext(ctx);
  vm.runInContext([escSrc, upSrc, bytesSrc, helperSrc, gaugeSrc,
    // `_lastUpdateRowHtml` arrived with live v0.7.35 and this gate went RED on
    // `_lastUpdateRowHtml is not defined` — upstream drift reaching the port as a
    // failing gate rather than as a discovery, which is what these lifts are for.
    //
    // It memoises the update row's markup so the row is rewritten only when it
    // CHANGES: innerHTML destroys and recreates the node, and a freshly inserted
    // .sbtn restarts its own CSS transition, which made the amber "available"
    // strip and its Update button flash. The rendered markup is identical either
    // way — what differs is how often it is written.
    'var _sysMetaWritten=false, _pendingSysData=null, _sysRafId=null;',
    'var _lastUpdateRowHtml=null;',
    flushSrc,
    // The two call sites, lifted from their handlers verbatim.
    'function __note(d){ _pendingSysData=d; if(!_sysRafId) _sysRafId=requestAnimationFrame(_flushSysUpdate); }',
    'function __flushPending(){ if(_pendingSysData && !_sysRafId) _sysRafId=requestAnimationFrame(_flushSysUpdate); }',
    'function __reset(){ _sysMetaWritten=false; _lastUpdateRowHtml=null; }',
    'function __writes(){ return _lastUpdateRowHtml; }',
  ].join('\n'), ctx);
  return {
    dom, frames, booked: () => booked,
    note: (d) => ctx.__note(d),
    flushPending: () => ctx.__flushPending(),
    reset: () => ctx.__reset(),
    setHidden: (v) => { dom.doc.hidden = v; },
    runFrames: () => { const q = frames.splice(0); for (const f of q) f(); },
  };
}

function portRunner() {
  const dom = makeDom();
  const frames = [];
  let booked = 0;
  const g = globalThis;
  g.document = dom.doc;
  g.requestAnimationFrame = (fn) => { booked++; frames.push(fn); return frames.length; };
  g.CustomEvent = function (type, init) { return { type, detail: init && init.detail }; };
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  return {
    dom, frames, booked: () => booked,
    note: (d) => m.noteSystemUpdate(d),
    flushPending: () => m.flushPendingSystem(),
    reset: () => m.resetSysMeta(),
    setHidden: (v) => { dom.doc.hidden = v; },
    runFrames: () => { const q = frames.splice(0); for (const f of q) f(); },
  };
}

// ── the corpus ─────────────────────────────────────────────────────────────
const base = {
  uptimeRaw: '1w2d03:04:05', cpuLoad: 12, memPct: 41, hddPct: 7, totalHdd: 16777216,
  totalMem: 1073741824, boardName: 'hAP ax3', version: '7.24 (stable)', cpuCount: 4,
  cpuFreq: 1200, tempC: null, updateAvailable: false, latestVersion: '', updateStatus: '',
};
const p = (over) => Object.assign({}, base, over);

const CASES = [
  ['an ordinary payload renders everything', [
    ['note', p({})], ['frames'],
  ]],
  ['a burst books ONE frame and renders the LATEST', [
    ['note', p({ cpuLoad: 10 })], ['note', p({ cpuLoad: 20 })], ['note', p({ cpuLoad: 30 })],
    ['frames'],
  ]],
  ['a hidden tab renders nothing and KEEPS the data', [
    ['hidden', true], ['note', p({ cpuLoad: 55 })], ['frames'],
    // Still hidden, still nothing.
    ['frames'],
    // Back in view: the payload that arrived while hidden is what renders.
    ['hidden', false], ['flushPending'], ['frames'],
  ]],
  ['no storage means TWO gauges, not three', [
    ['note', p({ totalHdd: 0, hddPct: 0 })], ['frames'],
  ]],
  ['the meta line is written once, not on every payload', [
    ['note', p({ boardName: 'hAP ax3' })], ['frames'],
    ['note', p({ boardName: 'CCR2004', version: '7.25', cpuCount: 16 })], ['frames'],
  ]],
  ['a reset re-arms it — a switch to another router', [
    ['note', p({})], ['frames'],
    ['reset'],
    ['note', p({ boardName: 'CCR2004', version: '7.25', cpuCount: 16, cpuFreq: 1700 })], ['frames'],
  ]],
  ['a payload with NO identity does not consume the one write', [
    ['note', p({ boardName: '', version: '', cpuCount: 0, totalMem: 0 })], ['frames'],
    ['note', p({})], ['frames'],
  ]],
  ['the temperature slot is created once and updated in place', [
    ['note', p({ tempC: 41 })], ['frames'],
    ['note', p({ tempC: 43.5 })], ['frames'],
    ['note', p({ tempC: 44 })], ['frames'],
  ]],
  ['a temperature that arrives BEFORE the meta line survives its rewrite', [
    // The slot is a child of sysMeta, so the meta write destroys it. This is
    // the ordering case: the next temperature must recreate it, ATTACHED.
    ['note', p({ boardName: '', version: '', cpuCount: 0, totalMem: 0, tempC: 39 })], ['frames'],
    ['note', p({ tempC: 40 })], ['frames'],
  ]],
  ['a reset then a temperature: the slot is rebuilt, not orphaned', [
    ['note', p({ tempC: 39 })], ['frames'],
    ['reset'],
    ['note', p({ boardName: 'CCR2004', tempC: 42 })], ['frames'],
    ['note', p({ tempC: 43 })], ['frames'],
  ]],
  ['a null temperature leaves an existing slot alone', [
    ['note', p({ tempC: 39 })], ['frames'],
    ['note', p({ tempC: null })], ['frames'],
  ]],
  ['zero degrees is a temperature, not an absence', [
    ['note', p({ tempC: 0 })], ['frames'],
  ]],
  ['update available names both versions and publishes the event', [
    ['note', p({ updateAvailable: true, latestVersion: '7.25', updateChannel: 'stable' })], ['frames'],
  ]],
  ['available WITHOUT a latest version falls through to checking', [
    ['note', p({ updateAvailable: true, latestVersion: '' })], ['frames'],
  ]],
  ['up to date', [
    ['note', p({ updateAvailable: false, latestVersion: '7.24' })], ['frames'],
  ]],
  ['a status that reads as unavailable is muted, not pending', [
    ['note', p({ updateStatus: 'Update server unavailable' })], ['frames'],
    ['note', p({ updateStatus: 'ERROR: could not connect' })], ['frames'],
    ['note', p({ updateStatus: 'Checking…' })], ['frames'],
  ]],
  ['nothing known at all: checking', [
    ['note', p({ latestVersion: '', updateStatus: '' })], ['frames'],
  ]],
  ['the installed version is stripped of its channel for the arrow', [
    ['note', p({ version: '7.24 (long-term)', updateAvailable: true, latestVersion: '7.25' })], ['frames'],
  ]],
  ['markup in a board name is escaped', [
    ['note', p({ boardName: '<img src=x>', version: '7 & 24', updateStatus: 'a "quoted" <b>' })], ['frames'],
  ]],
  ['re-showing the tab with nothing new books no frame', [
    // `pending` is cleared by the render, so coming back into view has nothing
    // to flush. A port that left it set would book a frame here — invisible in
    // the DOM, which is why framesBooked is part of the snapshot.
    ['note', p({})], ['frames'],
    ['hidden', true], ['hidden', false], ['flushPending'], ['frames'],
  ]],
  // ── THE DIRTY CHECK, live v0.7.35 ─────────────────────────────────────────
  //
  // The update row is rewritten only when its markup CHANGES. innerHTML destroys
  // and recreates the node, and a freshly inserted .sbtn restarts its own CSS
  // transition — which made the amber strip and its Update button flash on every
  // poll tick. The rendered markup is identical either way; what differs is how
  // often it is written and how often `mikrodash:updateavailable` fires.
  //
  // The event is the observable half here, and this gate counts dispatches, so
  // an unconditional write shows up as a rising count against a flat one.
  ['the same update row twice fires the event ONCE', [
    ['note', p({ updateAvailable: true, latestVersion: '7.25' })], ['frames'],
    ['note', p({ updateAvailable: true, latestVersion: '7.25' })], ['frames'],
  ]],
  ['a CHANGED update row fires again', [
    ['note', p({ updateAvailable: true, latestVersion: '7.25' })], ['frames'],
    ['note', p({ updateAvailable: true, latestVersion: '7.26' })], ['frames'],
  ]],
  ['an unchanged NON-update row is also written once', [
    ['note', p({ latestVersion: '7.24' })], ['frames'],
    ['note', p({ latestVersion: '7.24' })], ['frames'],
  ]],
  ['moving from available to up-to-date rewrites', [
    ['note', p({ updateAvailable: true, latestVersion: '7.25' })], ['frames'],
    ['note', p({ latestVersion: '7.25' })], ['frames'],
  ]],
  // A reconnect or router switch must CLEAR the fingerprint: two routers can
  // report the same versions, and a suppressed row would keep showing the
  // previous router's.
  ['a reset makes an identical row render again', [
    ['note', p({ updateAvailable: true, latestVersion: '7.25' })], ['frames'],
    ['reset'],
    ['note', p({ updateAvailable: true, latestVersion: '7.25' })], ['frames'],
  ]],
  ['a frame with nothing pending renders nothing', [
    ['note', p({})], ['frames'], ['frames'],
  ]],
];

const step = (r, [op, arg]) => ({
  note: () => r.note(arg), frames: () => r.runFrames(), hidden: () => r.setHidden(arg),
  flushPending: () => r.flushPending(), reset: () => r.reset(),
}[op] || (() => { throw new Error('unknown step ' + op); }))();

let bad = 0, steps = 0;
for (const [name, script] of CASES) {
  // Recipe 3i: the live runner is DRIVEN step by step outside the comparison, so
  // the whole run is frozen as one ordered snapshot sequence.
  const liveSnaps = G.value(name + ' live run', () => {
    const l = liveRunner();
    return script.map((st) => { step(l, st); return snapshot(l.dom, l); });
  });
  const port = portRunner();
  script.forEach((s, i) => {
    step(port, s);
    steps++;
    const a = liveSnaps[i], b = snapshot(port.dom, port);
    if (a === b) return;
    bad++;
    if (bad <= 3) {
      console.error('\nDIFF %s — after step %d (%s)', name, i + 1, s[0]);
      const A = JSON.parse(a), B = JSON.parse(b);
      for (const k of Object.keys(A)) {
        if (JSON.stringify(A[k]) !== JSON.stringify(B[k])) {
          console.error('  %s\n    live: %j\n    port: %j', k, A[k], B[k]);
        }
      }
    }
  });
}

// The run is only believable if the live side actually rendered. Checked here
// rather than assumed: two harnesses that both render nothing agree perfectly.
{
  // RE-AIMED AT THE PORT. "Two harnesses that both render nothing agree
  // perfectly" is the reason this block exists, and the port is the harness that
  // has to keep rendering something.
  const r = portRunner();
  r.note(p({ tempC: 40, updateAvailable: true, latestVersion: '7.25' }));
  r.runFrames();
  const s = JSON.parse(snapshot(r.dom, r));
  assert.match(s.gaugeRow.html, /gauge-arc-wrap/, 'the gauges did not render');
  assert.match(s.sysMeta.html, /sys-meta-item/, 'the meta line did not render');
  assert.match(s.rosUpdateRow.html, /ros-update-row warn/, 'the update row did not render');
  assert.equal(s.temp.attached, true, 'the live temperature slot did not attach');
  assert.equal(s.dispatched.length, 1, 'the live update event was not published');
  assert.match(s.uptimeDisplay.text, /^Uptime: /, 'the live uptime did not render');
}

fs.rmSync(OUT, { force: true });
if (bad) {
  console.error('\n%d of %d steps differ', bad, steps);
  process.exit(1);
}
console.log('system-card-check: %d cases, %d steps identical', CASES.length, steps);
