'use strict';
/**
 * The scheduled-reports LIST, live against ported.
 *
 * `sched-form-check` covers the edit dialog and `sched-remove-check` the Remove
 * button; the TABLE those buttons live in — and the SMTP notice above it — were
 * uncovered, which `element-coverage-audit` reported as 12 uncovered elements
 * behind two passing gates.
 *
 * ── DRIVEN THROUGH `loadSchedules`, NOT A SEAM ──────────────────────────────
 *
 * Both sides hold their rows in module state that nothing exports. Rather than
 * reaching in, the gate stubs `fetch` with the same payload and calls the real
 * loader, so the response handling is compared too — including `smtpReady !==
 * false`, where a server that omits the field must not have its schedules
 * declared undeliverable.
 *
 * WHAT IT CANNOT SEE: the modal's contents (covered by `sched-form-check`) and
 * layout.
 *
 * The delegated click wiring was on this list. Both branches behind it are gated
 * now — `[data-rs-del]` by `sched-remove-check`, `[data-rs-runs]` by
 * `sched-runs-check` — so the note is removed rather than left standing over a
 * gap that has closed. A stale "cannot see" is worse than none: it is the reason
 * nobody looks again.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/sched-list-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/sched-list-check.js --freeze
const G = L.golden('sched-list-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = G.value('iife', () => L.region(src, {
  banner: '// ── Reports page',
  must: ['renderSchedules', 'rptSchedTbody', 'loadSchedules'],
  mustNot: ['Queues page', 'backupsPage', 'dnsSettingsBody'],
}));

const IDS = G.value('IDS', () => L.idsFor(src, iife));
const LIST_IDS = ['rptSchedTbody', 'rptSchedActions', 'rptSchedNotice', 'rptSchedNoticeText'];

// What this gate COMPARES — see reports-tables-check for why that is not the
// same as what its region mentions.
if (process.argv.includes('--ids')) { console.log(JSON.stringify(LIST_IDS)); process.exit(0); }

const ENTRY = path.join(ROOT, 'testdata', '.sl-entry.ts');
fs.writeFileSync(ENTRY,
  "export { loadSchedules, renderSchedules } from '../web/src/pages/reports-schedules.js';\n");
const OUT = path.join(ROOT, 'testdata', '.sl-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const snap = (doc) => {
  const n = doc.nodes;
  const out = {};
  for (const id of LIST_IDS) {
    out[id] = n[id] ? { h: n[id].innerHTML, t: n[id].textContent,
      d: n[id].style && n[id].style.display } : null;
  }
  return JSON.stringify(out);
};

const fetchStub = (payload) => () => Promise.resolve({
  ok: true, json: () => Promise.resolve(payload),
});

async function liveRun(payload) {
  const doc = makeDoc(IDS, {});
  doc.nodes.rptRouter.value = 'r1';
  const ctx = {
    String, Array, Math, Number, Object, JSON, Date, Promise, parseInt, parseFloat,
    isFinite, isNaN, encodeURIComponent, document: doc,
    socket: { on() {}, emit() {} },
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {},
    window: { location: { origin: 'http://x' }, confirm: () => true, prompt: () => '' },
    fetch: fetchStub(payload),
    Chart: function () { return { destroy() {}, update() {} }; },
    MutationObserver: function () { return { observe() {}, disconnect() {} }; },
    ResizeObserver: function () { return { observe() {}, disconnect() {} }; },
    requestAnimationFrame: (fn) => { fn(); return 0; },
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    __out: null,
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function fmtBytes('),
    L.whole(src, 'function fmtMbps('),
    L.whole(src, 'function maxOf('),
    L.whole(src, 'function _sortRows('),
    L.whole(src, 'function _sortMul('),
    L.whole(src, 'function _renderSortHeader('),
    L.line(src, 'var _displayTimezone'),
    'function $(id){return document.getElementById(id);}',
    'function _debounce(fn){return fn;}',
    'function pageVisible(){return true;}',
    '(function () {' + iife + '\n__out = { loadSchedules: loadSchedules };\n})();',
  ].join('\n'), ctx);
  assert.ok(ctx.__out && ctx.__out.loadSchedules, 'the region did not publish loadSchedules');
  ctx.__out.loadSchedules();
  // DRAIN THE MICROTASK QUEUE PROPERLY. A fixed number of `await
  // Promise.resolve()` turns is a guess, and guessing wrong renders nothing and
  // compares two empty tables — which is how this gate first passed 27 cases
  // while showing an empty page. `setImmediate` runs after all pending
  // microtasks, so the render has happened by the time it fires.
  await new Promise((r) => setImmediate(r));
  await new Promise((r) => setImmediate(r));
  return snap(doc);
}

async function portRun(payload) {
  const doc = makeDoc(IDS, {});
  doc.nodes.rptRouter.value = 'r1';
  const prevWin = globalThis.window;
  const prevFetch = globalThis.fetch;
  globalThis.window = { location: { origin: 'http://x' } };
  globalThis.fetch = fetchStub(payload);
  try {
    const prevDoc = globalThis.document;
    globalThis.document = doc;
    try {
      delete require.cache[require.resolve(OUT)];
      require(OUT).loadSchedules();
      await new Promise((r) => setImmediate(r));
      await new Promise((r) => setImmediate(r));
      return snap(doc);
    } finally {
      if (prevDoc === undefined) delete globalThis.document; else globalThis.document = prevDoc;
    }
  } finally {
    globalThis.fetch = prevFetch;
    if (prevWin === undefined) delete globalThis.window; else globalThis.window = prevWin;
  }
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 3) {
    const A = JSON.parse(a), B = JSON.parse(b);
    for (const k of Object.keys(A)) {
      const x = JSON.stringify(A[k]), y = JSON.stringify(B[k]);
      if (x !== y) shout('DIFF %s [%s]\n  live: %s\n  port: %s', what, k,
        String(x).slice(0, 380), String(y).slice(0, 380));
    }
  }
}

const R = (o) => Object.assign({
  id: 's1', name: 'weekly wan', frequency: 'weekly', sendHour: 8,
  sections: ['traffic', 'ping'], iface: '', recipients: ['a@example.net'],
  enabled: true, lastRun: null, disabledReason: '',
}, o);
const P = (o) => Object.assign({
  ok: true, schedules: [], permitted: true, smtpReady: true,
  sections: ['traffic', 'ping', 'alerts'], needsInterface: ['traffic'],
}, o);

const CASES = {
  'no schedules': [P({})],
  'one schedule': [P({ schedules: [R({})] })],
  'several schedules': [P({ schedules: [R({}), R({ id: 's2', name: 'daily' })] })],
  // Permission changes the EMPTY TEXT as well as the buttons.
  'a viewer with no schedules': [P({ permitted: false })],
  'a viewer with schedules': [P({ permitted: false, schedules: [R({})] })],
  // SMTP: `!== false`, so an omitted field must not read as unconfigured.
  'smtp ready': [P({ smtpReady: true, schedules: [R({})] })],
  'smtp NOT ready': [P({ smtpReady: false, schedules: [R({})] })],
  'smtpReady OMITTED is treated as ready': [P({ smtpReady: undefined, schedules: [R({})] })],
  'smtpReady null is treated as ready': [P({ smtpReady: null, schedules: [R({})] })],
  // Row fields.
  'a disabled schedule': [P({ schedules: [R({ enabled: false })] })],
  'a disabled schedule with a reason': [P({ schedules: [R({ enabled: false, disabledReason: 'no smtp' })] })],
  'an enabled schedule with a reason': [P({ schedules: [R({ disabledReason: 'odd' })] })],
  'an hour that needs padding': [P({ schedules: [R({ sendHour: 8 })] })],
  'a two-digit hour': [P({ schedules: [R({ sendHour: 17 })] })],
  'midnight': [P({ schedules: [R({ sendHour: 0 })] })],
  'a schedule with an interface': [P({ schedules: [R({ iface: 'ether1' })] })],
  'a schedule with no sections': [P({ schedules: [R({ sections: [] })] })],
  'several recipients show a COUNT': [P({ schedules: [R({ recipients: ['a@x.net', 'b@x.net', 'c@x.net'] })] })],
  'no recipients': [P({ schedules: [R({ recipients: [] })] })],
  'never run': [P({ schedules: [R({ lastRun: null })] })],
  'a last run': [P({ schedules: [R({ lastRun: { ran_at: 1756000000000, outcome: 'sent' } })] })],
  'a failed last run': [P({ schedules: [R({ lastRun: { ran_at: 1756000000000, outcome: 'failed' } })] })],
  // Escaping.
  'markup in a name': [P({ schedules: [R({ name: '<img src=x>' })] })],
  'a quote in an id': [P({ schedules: [R({ id: 'a"b' })] })],
  'markup in an interface': [P({ schedules: [R({ iface: '<b>e</b>' })] })],
  'markup in a disabled reason': [P({ schedules: [R({ enabled: false, disabledReason: '<i>x</i>' })] })],
  // A response the loader must refuse.
  'a not-ok response renders nothing': [P({ ok: false, schedules: [R({})] })],
};

(async () => {
  for (const [name, [payload]] of Object.entries(CASES)) {
    let a, b;
    try { a = await G.live(name, () => liveRun(payload)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
    try { b = await portRun(payload); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
    cmp(name, a, b);
  }

  // ── believability ────────────────────────────────────────────────────────
  {
    const s = JSON.parse(await G.live('auto:5', () => liveRun(P({ schedules: [R({})] }))));
    assert.match(s.rptSchedTbody.h, /weekly wan/, 'the live table rendered no row');
    assert.match(s.rptSchedTbody.h, /data-rs-edit/, 'a permitted viewer got no Edit button');
    assert.match(s.rptSchedActions.h, /New scheduled report/, 'the New button is missing');
    assert.equal(s.rptSchedNotice.d, 'none', 'the SMTP notice showed while SMTP is ready');
  }
  {
    const s = JSON.parse(await G.live('auto:4', () => liveRun(P({ smtpReady: false, schedules: [R({})] }))));
    assert.notEqual(s.rptSchedNotice.d, 'none', 'the SMTP notice stayed hidden while unconfigured');
    assert.match(s.rptSchedNoticeText.t, /SMTP is not configured/, 'the notice text is missing');
  }
  {
    // The empty text differs by permission — "yet" invites an action a viewer
    // cannot take.
    const can = JSON.parse(await G.live('auto:3', () => liveRun(P({ permitted: true }))));
    const cannot = JSON.parse(await G.live('auto:2', () => liveRun(P({ permitted: false }))));
    assert.match(can.rptSchedTbody.h, /yet\./, 'the permitted empty state lost its "yet"');
    assert.ok(!/yet\./.test(cannot.rptSchedTbody.h),
      'a viewer was told there are none "yet" — an invitation to hunt for a button that is not there');
    assert.equal(cannot.rptSchedActions.h, '', 'a viewer was offered the New button');
  }
  {
    // An omitted smtpReady is READY, not unconfigured.
    const omitted = JSON.parse(await G.live('auto:1', () => liveRun(P({ smtpReady: undefined, schedules: [R({})] }))));
    assert.equal(omitted.rptSchedNotice.d, 'none',
      'an omitted smtpReady declared the schedules undeliverable');
  }

  fs.rmSync(OUT, { force: true });
  if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
  say('sched-list-check: %d cases identical', checked);
})();
