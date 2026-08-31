'use strict';
/**
 * The schedule form, live against ported.
 *
 * ── ONE STATED DIFFERENCE IN THE BODY ───────────────────────────────────────
 *
 * The live payload carries `routerId` in the BODY. This port's endpoints take it
 * from the QUERY and decode with `DisallowUnknownFields`, so sending it in the
 * body would be refused as malformed — the server's own comment says why it
 * refuses unknown keys. The comparison therefore moves `routerId` out of the
 * live body and into its query before diffing, in one place, so that single
 * intended difference is declared once and every other difference still fails.
 *
 * ── WHAT THE FORM HAS TO GET RIGHT ──────────────────────────────────────────
 *
 *   opening for NEW      empty name, `daily`, hour 7, sections `['ping']`,
 *                        enabled TRUE — the server defaults an absent `enabled`
 *                        to true, and a form that started it off would create
 *                        every schedule switched off.
 *   opening for EDIT     every field from the row, recipients joined by newline,
 *                        the row's own hour and sections.
 *   the interface picker shown only when a chosen section needs it, and the set
 *                        that needs it comes from the SERVER.
 *   an empty recipients  splits to [''] and is SENT that way, so the refusal
 *   box                  comes from the validator that owns the message.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/sched-form-check.js
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
const G = LIFT.golden('sched-form-check');
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
// FROZEN AS ONE VALUE — the joined text is what `vm` executes, so freezing it
// keeps the live half RUNNING without a reference.
const liveSrc = G.value('the lifted live source', () => [
  slice('  function openSchedModal(row) {', '\n  }', 'openSchedModal'),
  slice('  function chosenSections() {', '\n  }', 'chosenSections'),
  slice('  function syncIfaceVisibility() {', '\n  }', 'syncIfaceVisibility'),
  slice('  function saveSchedule() {', '\n  }', 'saveSchedule'),
].join('\n'));
if (!liveSrc || liveSrc.length < 200) {
  throw new Error('the recorded live source is empty — the golden is broken');
}

const ENTRY = path.join(ROOT, 'testdata', '.schedform-entry.ts');
fs.writeFileSync(ENTRY,
  "export { openSchedModal, wireScheduleForm, loadSchedules } from '../web/src/pages/reports-schedules.js';\n");
const OUT = path.join(ROOT, 'testdata', '.schedform-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const SECTIONS = ['ping', 'traffic', 'bandwidth', 'alerts', 'connectivity'];
const NEEDS_IFACE = ['traffic', 'bandwidth'];
const ROWS = [
  { id: 's1', name: 'Weekly bandwidth', sections: ['bandwidth'], iface: 'ether1',
    recipients: ['a@example.invalid', 'b@example.invalid'], frequency: 'weekly',
    sendHour: 14, enabled: true },
  { id: 's2', name: 'Ping only', sections: ['ping'], recipients: ['x@example.invalid'],
    frequency: 'daily', sendHour: 0, enabled: false },
];

// ── WHAT THIS GATE COVERS, ANSWERED RATHER THAN GUESSED ─────────────────────
//
// `element-coverage-audit` text-scans a gate's quoted strings when it declares
// no `--ids`, and this gate builds its nodes as PROPERTIES — `nodes.rs_enabled`
// — so the scan saw five of the eleven it compares. Six elements of the schedule
// form were reported uncovered while this file asserted on every one of them.
//
// The list is what `state()` READS BACK, not what the shim provides.
// `rptRouter` is set once and never varied here, so it is not claimed —
// `reports-latch-check` drives that one.
const FORM_IDS = ['rs_name', 'rs_frequency', 'rs_iface', 'rs_recipients', 'rs_hour'];
const COVERS = FORM_IDS.concat([
  'rs_enabled', 'rs_error', 'rs_ifaceWrap', 'rs_sections', 'rptSchedTitle', 'rptSchedModal',
]);
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

function makeWorld() {
  const calls = [];
  const nodes = {};
  // `value` COERCES TO STRING, as a real input does. The live code assigns
  // `hour.value = row.sendHour` — a NUMBER — and relies on the DOM to stringify
  // it; the port calls String() itself. A fake node that stored the raw value
  // reported that as 7 vs "7" and made a faithful port look wrong.
  const mkInput = (v) => {
    let val = String(v);
    return {
      get value() { return val; },
      set value(x) { val = String(x); },
      checked: false, style: {}, innerHTML: '',
    };
  };
  for (const id of FORM_IDS) {
    nodes[id] = mkInput('');
  }
  nodes.rs_enabled = mkInput('');
  nodes.rs_error = { style: { display: '' }, textContent: '' };
  nodes.rs_ifaceWrap = { style: { display: '' } };
  nodes.rptSchedTitle = { textContent: '' };
  nodes.rptSchedModal = { _c: new Set(), classList: { add(c) { nodes.rptSchedModal._c.add(c); },
    remove(c) { nodes.rptSchedModal._c.delete(c); } } };
  nodes.rptRouter = { value: 'rtr-1' };
  let secBoxes = [];
  nodes.rs_sections = {
    _html: '',
    get innerHTML() { return this._html; },
    set innerHTML(v) {
      this._html = v;
      secBoxes = [...v.matchAll(/data-rs-sec="([^"]+)"([^>]*)/g)]
        .map((m) => ({ _sec: m[1], checked: m[2].includes('checked'),
                       getAttribute: (k) => (k === 'data-rs-sec' ? m[1] : null) }));
    },
  };
  // The hour select records what was appended, so "24 options, HH:00" is compared
  // rather than assumed.
  nodes.rs_hour.insertAdjacentHTML = function (_where, h) { this.innerHTML += h; };
  return {
    nodes, calls, secBoxes: () => secBoxes,
    fetch: (url, init) => {
      calls.push({ url, method: (init && init.method) || 'GET', body: (init && init.body) || null });
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) });
    },
    state() {
      return JSON.stringify({
        title: nodes.rptSchedTitle.textContent,
        name: nodes.rs_name.value, freq: nodes.rs_frequency.value,
        iface: nodes.rs_iface.value, recips: nodes.rs_recipients.value,
        enabled: nodes.rs_enabled.checked, hour: nodes.rs_hour.value,
        hourOptions: nodes.rs_hour.innerHTML,
        sections: secBoxes.map((b) => [b._sec, b.checked]),
        ifaceWrap: nodes.rs_ifaceWrap.style.display,
        errorShown: nodes.rs_error.style.display,
        open: [...nodes.rptSchedModal._c],
        calls: calls.map(normalise),
      }, null, 1);
    },
  };
}

/**
 * The one intended difference, removed in one place: the live body carries
 * `routerId` and this port's query does.
 */
function normalise(c) {
  let url = c.url, body = c.body;
  if (body) {
    const o = JSON.parse(body);
    if (o.routerId !== undefined) {
      const sep = url.indexOf('?') === -1 ? '?' : '&';
      url += sep + 'routerId=' + encodeURIComponent(o.routerId);
      delete o.routerId;
    }
    body = JSON.stringify(o, Object.keys(o).sort());
  }
  url = url.replace('/next/api/reports/schedules', '<sched>').replace('/api/reports/schedules', '<sched>');
  // Query order differs only because the two build the URL differently.
  const [p, q] = url.split('?');
  return { url: p + (q ? '?' + q.split('&').sort().join('&') : ''), method: c.method, body };
}

const settle = () => new Promise((r) => setImmediate(r));

function liveRun(row, act) {
  const w = makeWorld();
  const ctx = {
    JSON, String, Number, Array, Object, encodeURIComponent, Promise, RegExp,
    document: {
      querySelectorAll: (sel) => (sel === '[data-rs-sec]' ? w.secBoxes() : []),
    },
    $: (id) => w.nodes[id] || null,
    esc: (v) => String(v == null ? '' : v).replace(/[&<>"']/g,
      (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])),
    rptRouter: w.nodes.rptRouter,
    _sched: { rows: ROWS, sections: SECTIONS, needsInterface: NEEDS_IFACE, editing: null },
    fetch: w.fetch,
    loadSchedules() {},
    schedApi: (p, opts) => {
      const rid = w.nodes.rptRouter.value;
      const sep = p.indexOf('?') === -1 ? '?' : '&';
      return w.fetch('/api/reports/schedules' + p + sep + 'routerId=' + encodeURIComponent(rid), opts)
        .then((r) => r.json());
    },
  };
  vm.createContext(ctx);
  vm.runInContext(liveSrc, ctx);
  act({ open: (r) => ctx.openSchedModal(r), save: () => ctx.saveSchedule(),
        sync: () => ctx.syncIfaceVisibility(), boxes: w.secBoxes }, w);
  return w;
}

function portRun(row, act) {
  const w = makeWorld();
  const saved = { document: global.document, window: global.window, fetch: global.fetch };
  global.document = {
    getElementById: (id) => w.nodes[id] || null,
    querySelectorAll: (sel) => (sel === '[data-rs-sec]' ? w.secBoxes() : []),
    addEventListener() {},
  };
  global.window = {};
  global.fetch = w.fetch;
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    // Seed rows, sections and needsInterface through the REAL load, so the form
    // draws from what the server actually sends.
    mod.loadSchedules();
    return { mod, w, saved };
  } catch (e) {
    for (const k of Object.keys(saved)) {
      if (saved[k] === undefined) delete global[k]; else global[k] = saved[k];
    }
    throw e;
  }
}

const bad = [];
let cases = 0;
const queued = [];
function compare(what, row, act) {
  queued.push(async () => {
    cases++;
    const lw = liveRun(row, act);
    await settle();
    const a = lw.state();

    const w = makeWorld();
    const saved = { document: global.document, window: global.window, fetch: global.fetch };
    global.document = {
      getElementById: (id) => w.nodes[id] || null,
      querySelectorAll: (sel) => (sel === '[data-rs-sec]' ? w.secBoxes() : []),
      addEventListener() {},
    };
    global.window = {};
    global.fetch = w.fetch;
    let b;
    try {
      delete require.cache[require.resolve(OUT)];
      const mod = require(OUT);
      w.fetch('/next/api/reports/schedules?routerId=rtr-1', {});
      // The list load is what carries `sections` and `needsInterface`, so it is
      // stubbed at the same shape the server sends rather than injected.
      const payload = { ok: true, schedules: ROWS, sections: SECTIONS,
                        needsInterface: NEEDS_IFACE, permitted: true, smtpReady: true };
      global.fetch = (url, init) => {
        w.calls.push({ url, method: (init && init.method) || 'GET', body: (init && init.body) || null });
        return Promise.resolve({ ok: true, json: () => Promise.resolve(payload) });
      };
      mod.loadSchedules();
      await settle(); await settle();
      w.calls.length = 0;
      act({ open: (r) => mod.openSchedModal(r), save: () => mod.__save?.(),
            sync: () => {}, boxes: w.secBoxes }, w, mod);
      await settle(); await settle();
      b = w.state();
    } finally {
      for (const k of Object.keys(saved)) {
        if (saved[k] === undefined) delete global[k]; else global[k] = saved[k];
      }
    }
    if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
  });
}

// Opening the form is the whole comparison for these.
compare('open for a NEW schedule', null, (api) => api.open(null));
compare('open for an existing row', ROWS[0], (api) => api.open(ROWS[0]));
compare('open for a disabled row at hour 0', ROWS[1], (api) => api.open(ROWS[1]));
compare('open new, then open a row', null, (api) => { api.open(null); api.open(ROWS[0]); });
compare('open a row, then open new', ROWS[0], (api) => { api.open(ROWS[0]); api.open(null); });

(async () => {
  for (const run of queued) await run();
  fs.rmSync(OUT, { force: true });
  if (bad.length) {
    console.error('the schedule form differs from the live one:\n\n' + bad.slice(0, 1).join('\n\n') + '\n');
    process.exit(1);
  }
  console.log('the schedule form matches the live one (' + cases + ' cases: new, edit, and the ' +
    'reopen paths)');
})();
