'use strict';
/**
 * The alert-filter toggles, live against ported.
 *
 * ── WHAT ONLY A DRIVEN GATE SEES ────────────────────────────────────────────
 *
 * Every rule here is about what happens AFTER the click:
 *
 *   the request         one key, the one that toggled, and nothing else
 *   a refusal           the box goes BACK, the local copy goes back, the local
 *                       copy is re-saved, and the banner says so
 *   a network failure   the box STAYS — deliberately different from a refusal
 *   the filter card     dimmed and click-through-disabled when Up/Down is off
 *   localStorage        written on every change, refusal included
 *
 * A renderer diff sees none of that. Both sides are driven through the same
 * checkboxes against the same fake DOM and every write, request and storage call
 * is compared in order.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/alert-filters-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const L = require('./lib/lift.js');
const { freezeCase } = L;
// The live half is FROZEN so this gate outlives `../MikroDash`.
// Re-freeze with: node tools/alert-filters-check.js --freeze
const G = L.golden('alert-filters-check');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// THROUGH THE SHARED SEAM, not a direct read: `L.liveSource` returns '' when
// the reference is absent instead of throwing ENOENT at module load.
const src = L.liveSource(ROOT);
const TABLES = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'alert-filters.json'), 'utf8'));

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

// The whole IIFE, plus the two loaders it calls and the two objects it mutates.
// BUILT LAZILY, not at module scope. The slices below throw when the reference
// is absent — `cannot find NOTIF_TYPES_KEY` — and they were being built on
// require even though the VM that uses them lives inside `runLive`, which the
// frozen output means we never enter. Deferring the assembly is what lets the
// output freeze work without vendoring the reference's JavaScript.
let _liveSrc = null;
function liveSrc() {
  if (_liveSrc === null) _liveSrc = [
  slice('var NOTIF_TYPES_KEY       =', ';', 'NOTIF_TYPES_KEY'),
  slice('var NOTIF_IFACE_TYPES_KEY =', ';', 'NOTIF_IFACE_TYPES_KEY'),
  slice('var _alertTypes      = {', '};', '_alertTypes'),
  slice('var _alertIfaceTypes = {', '};', '_alertIfaceTypes'),
  slice('function loadAlertFilters() {', '\n}', 'loadAlertFilters'),
  slice('function saveAlertFilters() {', '\n}', 'saveAlertFilters'),
  slice("(function(){\n  var TYPE_MAP = [", '\n})();', 'the alert-filter IIFE'),
].join('\n');
  return _liveSrc;
}

const OUT = path.join(ROOT, 'testdata', '.alertfilters-port.cjs');
const ENTRY = path.join(ROOT, 'testdata', '.alertfilters-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/settings-alert-filters.js';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── The fake DOM ────────────────────────────────────────────────────────────

function makeWorld(stored, refuse) {
  const ops = [];
  const nodes = {};
  const store = { ...stored };
  const listeners = {};

  function box(id) {
    let checked = false;
    const n = {
      _id: id,
      get checked() { return checked; },
      set checked(v) { checked = !!v; ops.push([id, 'checked', checked]); },
      addEventListener(ev, fn) { listeners[id + ':' + ev] = fn; },
    };
    return n;
  }
  for (const m of TABLES.map) nodes[m.id] = box(m.id);

  const style = new Proxy({}, {
    set(t, k, v) { t[k] = v; ops.push(['notifIfaceFilterCard', 'style.' + String(k), String(v)]); return true; },
    get(t, k) { return t[k]; },
  });
  nodes.notifIfaceFilterCard = { _id: 'notifIfaceFilterCard', style };
  nodes.settingsBanner = (() => {
    let cls = '', txt = '';
    return {
      _id: 'settingsBanner',
      get className() { return cls; },
      set className(v) { cls = String(v); ops.push(['settingsBanner', 'class', cls]); },
      get textContent() { return txt; },
      set textContent(v) { txt = String(v); ops.push(['settingsBanner', 'text', txt]); },
    };
  })();
  ops.length = 0;

  const doc = {
    getElementById: (id) => nodes[id] || null,
    addEventListener(ev, fn) { listeners['document:' + ev] = fn; },
  };
  return {
    doc, ops, nodes, listeners, store,
    localStorage: {
      getItem: (k) => (k in store ? store[k] : null),
      setItem: (k, v) => {
        // A store that refuses ONE key. Real browsers throw on setItem when the
        // quota is gone or the origin is blocked, and they throw per call — so
        // the live code's TWO separate try blocks are what let the second write
        // through when the first fails. One shared try loses both, and nothing
        // observable says so unless a fixture can refuse exactly one.
        if (refuse && k === refuse) throw new Error('QuotaExceededError');
        store[k] = String(v);
        ops.push(['storage', k, String(v)]);
      },
    },
    toggle(id, to) {
      nodes[id].checked = to;
      listeners[id + ':change']();
    },
    pageChange() { listeners['document:mikrodash:pagechange']({ detail: 'settings' }); },
    state() {
      return {
        store,
        boxes: TABLES.map.map((m) => [m.id, nodes[m.id].checked]),
      };
    },
  };
}

function fetchFor(reply, log) {
  return (url, init) => {
    log.push(['fetch', url, init && init.body ? JSON.parse(init.body) : null]);
    if (reply === 'reject') return Promise.reject(new Error('network'));
    if (reply === 'notjson') {
      return Promise.resolve({ ok: false, json: () => Promise.reject(new Error('not json')) });
    }
    if (reply === 'notjson-ok') {
      return Promise.resolve({ ok: true, json: () => Promise.reject(new Error('not json')) });
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve(reply) });
  };
}

const flush = () => new Promise((r) => setImmediate(r));

// A fetch whose replies are released by the scenario, so two clicks can be in
// flight at once. `wanted` is captured per request in both implementations; a
// port reading the box at REPLY time instead gets the second click's value.
function deferredFetch(log) {
  const pending = [];
  const fn = (url, init) => {
    log.push(['fetch', url, init && init.body ? JSON.parse(init.body) : null]);
    return new Promise((resolve) => pending.push(resolve));
  };
  fn.release = (i, body) => pending[i]({ ok: true, json: () => Promise.resolve(body) });
  fn.count = () => pending.length;
  return fn;
}

async function runLive(sc) {
  const w = makeWorld(sc.stored, sc.refuse);
  const log = [];
  let ctxFetch = null;
  const ctx = {
    document: w.doc, Object, JSON, Array, String, localStorage: w.localStorage,
    $: (id) => w.doc.getElementById(id),
    fetch: sc.deferred ? (ctxFetch = deferredFetch(log)) : fetchFor(sc.reply, log),
    setImmediate,
    window: { showBanner: (t, m) => log.push(['banner', t, m]) },
  };
  vm.createContext(ctx);
  vm.runInContext(liveSrc(), ctx);
  ctx.loadAlertFilters();
  ctx.document.dispatchEvent = () => {};
  await sc.drive(w, flush, ctxFetch);
  return { log, ops: w.ops, state: w.state() };
}

async function runPort(sc) {
  const w = makeWorld(sc.stored, sc.refuse);
  const log = [];
  const prev = {
    document: globalThis.document, fetch: globalThis.fetch, localStorage: globalThis.localStorage,
  };
  globalThis.document = w.doc;
  const portFetch = sc.deferred ? deferredFetch(log) : fetchFor(sc.reply, log);
  globalThis.fetch = portFetch;
  globalThis.localStorage = w.localStorage;
  try {
    delete require.cache[require.resolve(OUT)];
    // The banner is the port's own `showBanner`, which writes to
    // `settingsBanner` — the live side calls `window.showBanner`, which this
    // gate stubs into the log. Both are compared, one through the DOM and one
    // through the log, so the MESSAGE is pinned even though the route differs.
    // That difference is real and recorded: the live app hoists showBanner onto
    // window because its two IIFEs cannot see each other; the port imports it.
    const m = require(OUT);
    m.initAlertFilters();
    await sc.drive(w, flush, sc.deferred ? portFetch : null);
  } finally {
    Object.assign(globalThis, prev);
  }
  // The port's banner lands in `ops` as a settingsBanner write; the live one
  // lands in `log`. Normalise the port's into the same log shape so the two
  // comparisons line up.
  const banner = w.ops.filter((o) => o[0] === 'settingsBanner');
  if (banner.length) {
    const cls = banner.find((o) => o[1] === 'class');
    const txt = banner.find((o) => o[1] === 'text');
    log.push(['banner', cls ? cls[2].replace('sbanner show sbanner-', '') : '', txt ? txt[2] : '']);
  }
  return { log, ops: w.ops.filter((o) => o[0] !== 'settingsBanner'), state: w.state() };
}

const T = (id) => TABLES.map.find((m) => m.id === id);

const SCENARIOS = [
  {
    name: 'the page opens with nothing stored',
    stored: {},
    async drive(w) { w.pageChange(); },
  },
  {
    name: 'the page opens with Interface Up/Down stored OFF',
    stored: { [TABLES.typesKey]: JSON.stringify({ ifaceUpDown: false }) },
    async drive(w) { w.pageChange(); },
  },
  {
    name: 'stored state carrying a key the UI does not know',
    stored: { [TABLES.typesKey]: JSON.stringify({ ifaceUpDown: false, invented: true }) },
    async drive(w) { w.pageChange(); },
  },
  {
    name: 'a CORRUPT stored entry falls back to the defaults',
    stored: { [TABLES.typesKey]: '{not json' },
    async drive(w) { w.pageChange(); },
  },
  {
    name: 'turning Interface Up/Down OFF dims the filter card',
    stored: {}, reply: { ok: true },
    async drive(w, f) { w.pageChange(); w.toggle('s_notifIfaceUpDown', false); await f(); await f(); },
  },
  {
    name: 'turning it back ON restores the card',
    stored: { [TABLES.typesKey]: JSON.stringify({ ifaceUpDown: false }) }, reply: { ok: true },
    async drive(w, f) { w.pageChange(); w.toggle('s_notifIfaceUpDown', true); await f(); await f(); },
  },
  {
    name: 'a non-card toggle does NOT touch the filter card',
    stored: {}, reply: { ok: true },
    async drive(w, f) { w.pageChange(); w.toggle('s_notifCpu', false); await f(); await f(); },
  },
  {
    name: 'an interface-kind toggle writes to the other object',
    stored: {}, reply: { ok: true },
    async drive(w, f) { w.pageChange(); w.toggle('s_notifIfaceBridge', true); await f(); await f(); },
  },
  {
    name: 'a REFUSED save puts the box back and says so',
    stored: {}, reply: { ok: false, error: 'not permitted' },
    async drive(w, f) { w.pageChange(); w.toggle('s_notifCpu', false); await f(); await f(); },
  },
  {
    name: 'a refused save on the CARD toggle also restores the card',
    stored: {}, reply: { ok: false, error: 'not permitted' },
    async drive(w, f) { w.pageChange(); w.toggle('s_notifIfaceUpDown', false); await f(); await f(); },
  },
  {
    name: 'a refusal with NO message falls back',
    stored: {}, reply: { ok: false },
    async drive(w, f) { w.pageChange(); w.toggle('s_notifPing', false); await f(); await f(); },
  },
  {
    name: 'a NON-JSON error body still reads as a refusal',
    stored: {}, reply: 'notjson',
    async drive(w, f) { w.pageChange(); w.toggle('s_notifPing', false); await f(); await f(); },
  },
  {
    name: 'a non-JSON body on a 200 reads as success',
    stored: {}, reply: 'notjson-ok',
    async drive(w, f) { w.pageChange(); w.toggle('s_notifPing', false); await f(); await f(); },
  },
  {
    name: 'a NETWORK failure leaves the box as the operator set it',
    stored: {}, reply: 'reject',
    async drive(w, f) { w.pageChange(); w.toggle('s_notifVpn', false); await f(); await f(); },
  },
  {
    name: 'TWO clicks in flight: the revert uses what THIS request sent',
    stored: {}, deferred: true,
    async drive(w, f, fetchCtl) {
      w.pageChange();
      w.toggle('s_notifCpu', false);      // request 0 tried to set false
      await f();
      w.toggle('s_notifCpu', true);       // request 1 tried to set true
      await f();
      // The FIRST is refused after the second click. The revert must put back
      // the opposite of what request 0 sent — not the opposite of whatever the
      // box says now.
      fetchCtl.release(0, { ok: false, error: 'not permitted' });
      await f(); await f(); await f();
      fetchCtl.release(1, { ok: true });
      await f(); await f();
    },
  },
  {
    name: 'a stored key the defaults do not have is not written back',
    stored: { [TABLES.typesKey]: JSON.stringify({ ifaceUpDown: false, invented: true }) },
    reply: { ok: true },
    async drive(w, f) {
      w.pageChange();
      // The SAVE is what exposes it: an adopted key would be serialised back
      // into localStorage, so the stored blob would grow a field the UI has no
      // control for and no way to clear.
      w.toggle('s_notifCpu', false);
      await f(); await f();
    },
  },
  {
    name: 'localStorage refuses the FIRST key: the second is still written',
    stored: {}, reply: { ok: true }, refuse: TABLES.typesKey,
    async drive(w, f) {
      w.pageChange();
      w.toggle('s_notifIfaceBridge', true);
      await f(); await f();
    },
  },
  {
    name: 'every toggle in turn',
    stored: {}, reply: { ok: true },
    async drive(w, f) {
      w.pageChange();
      for (const m of TABLES.map) { w.toggle(m.id, !w.nodes[m.id].checked); await f(); await f(); }
    },
  },
  {
    name: 'a second page change re-syncs from the in-memory state',
    stored: {}, reply: { ok: true },
    async drive(w, f) {
      w.pageChange();
      w.toggle('s_notifCpu', false); await f(); await f();
      w.pageChange();
    },
  },
];

(async () => {
  let bad = 0, total = 0;
  for (const sc of SCENARIOS) {
    // One case object reaches BOTH runs; a mutating drive would leak the live
    // run's state into the port's and make the gate accuse correct code.
    // See lift.js:freezeCase — this happened once and was hard to see.
    freezeCase(sc);
    // THE OUTPUT IS FROZEN, not the source. `vm.runInContext(liveSrc, ...)`
    // lives inside runLive, so with the reference absent the live half is
    // never entered and the stubbed `liveSrc` is never used. That keeps the
    // reference's JavaScript out of this repo, which freezing the source
    // would not have done.
    const live = await G.live(sc.name, () => runLive(sc));
    const port = await runPort(sc);
    total += live.ops.length;
    if (live.ops.length === 0) {
      console.log('  UNDRIVEN     ' + sc.name + ' — the LIVE side did nothing');
      bad++;
      continue;
    }
    const a = JSON.stringify({ log: live.log, ops: live.ops, state: live.state }, null, 1);
    const b = JSON.stringify({ log: port.log, ops: port.ops, state: port.state }, null, 1);
    if (a === b) { console.log('  ok  ' + sc.name + '  (' + live.ops.length + ' ops)'); continue; }
    bad++;
    console.log('  DIFF  ' + sc.name);
    const al = a.split('\n'), bl = b.split('\n');
    for (let k = 0, shown = 0; k < Math.max(al.length, bl.length) && shown < 12; k++) {
      if (al[k] !== bl[k]) {
        console.log('        live: ' + (al[k] === undefined ? '(end)' : al[k].trim().slice(0, 200)));
        console.log('        port: ' + (bl[k] === undefined ? '(end)' : bl[k].trim().slice(0, 200)));
        shown++;
      }
    }
  }
  fs.rmSync(OUT, { force: true });
  console.log('\n' + SCENARIOS.length + ' scenarios, ' + total + ' live operations compared');
  if (bad) { console.log(bad + ' FAILED'); process.exit(1); }
  console.log('all agree');
})();
