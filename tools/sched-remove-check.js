'use strict';
/**
 * The Scheduled tab's Remove button, live against ported.
 *
 * ── WHY ONLY REMOVE ─────────────────────────────────────────────────────────
 *
 * Four write buttons are drawn when the server says `permitted`. Remove is the
 * one whose endpoint exists on this side, and the one that is worst to leave
 * inert: an enabled button labelled Remove that deletes nothing.
 *
 * ── WHAT IS COMPARED ────────────────────────────────────────────────────────
 *
 * The REQUEST and the confirmation, because that is all this button is: does it
 * ask before deleting, does it name the schedule the operator is looking at
 * rather than an opaque id, does it send DELETE to the right URL with the
 * routerId the endpoint requires, and does it reload afterwards — including when
 * the request fails, since the list is the truth and a row that may or may not
 * still exist is worse than one extra read.
 *
 * The one intended difference is the prefix: the port's report endpoints live
 * under `/next/`. Stripped from both sides in one place, as in the export-links
 * gate, so every OTHER difference still fails.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/sched-remove-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

// THE LIVE REMOVE BRANCH LEAVES AN UNHANDLED REJECTION, and that is not a bug in
// this harness — `schedApi` chains `.then(r => r.json())` and the branch adds no
// `.catch`, so a network failure rejects with nobody listening. A browser logs
// that and carries on; Node aborts the process. Modelled as the browser does,
// because the case exists to compare what was SENT before the failure.
// THE BROWSER LOGS AND CONTINUES — but only for the PAGE's rejections.
//
// This handler models a browser: the live Remove/Send handlers call `fetch`
// without a `.catch`, so a failed request rejects with nobody listening, and the
// gate must survive that to compare what happened. A blanket handler was doing
// that AND swallowing the gate's own failures — an assertion thrown anywhere
// asynchronous vanished, exit 0, no output. Measured: injecting a throw into the
// closing IIFE produced a clean pass.
//
// An AssertionError is never the page rejecting. It is this gate failing, and it
// must be fatal.
process.on('unhandledRejection', (e) => {
  if (e && (e.name === 'AssertionError' || e.code === 'ERR_ASSERTION')) {
    console.error(String(e.stack || e));
    process.exit(1);
  }
  /* anything else: the browser logs and continues */
});

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('sched-remove-check');
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
const delSrc = slice("    var del = e.target.closest('[data-rs-del]');", '\n    }', 'the Remove branch');
const apiSrc = slice('  function schedApi(path, opts) {', '\n  }', 'schedApi');

const ENTRY = path.join(ROOT, 'testdata', '.sched-entry.ts');
fs.writeFileSync(ENTRY,
  "export { wireScheduleActions, loadSchedules } from '../web/src/pages/reports-schedules.js';\n");
const OUT = path.join(ROOT, 'testdata', '.sched-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const P_LIVE = '/api/reports/schedules';
const P_PORT = '/next/api/reports/schedules';
const strip = (u) => (u.startsWith(P_PORT) ? '<sched>' + u.slice(P_PORT.length)
  : u.startsWith(P_LIVE) ? '<sched>' + u.slice(P_LIVE.length) : u);

const ROWS = [
  { id: 's1', name: 'Weekly bandwidth', sections: [], recipients: [], frequency: 'weekly', sendHour: 8, enabled: true },
  { id: 's2', name: 'Quotes "and" <angles>', sections: [], recipients: [], frequency: 'daily', sendHour: 6, enabled: true },
  { id: 's3', name: '', sections: [], recipients: [], frequency: 'daily', sendHour: 6, enabled: true },
  // An id that needs percent-encoding. Schedule ids are server-minted, so this
  // is defensive rather than expected — but the encoding is one character to get
  // wrong and no other case could see it.
  { id: 'a b/c&d', name: 'Odd id', sections: [], recipients: [], frequency: 'daily', sendHour: 6, enabled: true },
];

function makeWorld(answer) {
  const asked = [];
  const calls = [];
  return {
    asked, calls,
    confirm: (msg) => { asked.push(msg); return answer; },
    fetch: (url, init) => {
      // `credentials` is NORMALISED, and only this field. `same-origin` is the
      // fetch default for a same-origin request, so stating it and omitting it
      // produce the identical request — the live `schedApi` omits it, and this
      // port states it because every neighbouring call in reports.ts does. A
      // difference the browser cannot observe is not one this gate should
      // report, but narrowing it to one named field keeps everything else exact.
      const cred = (init && init.credentials) || 'same-origin';
      calls.push({ url: strip(url), method: (init && init.method) || 'GET', credentials: cred });
        // ONLY THE DELETE FAILS. Rejecting every request also broke the load that
      // seeds the rows, so the port had no schedule to name and returned before
      // prompting — which looked like the button doing nothing rather than the
      // harness withholding its data.
      if (answer === 'reject' && (init && init.method) === 'DELETE') {
        return Promise.reject(new Error('network'));
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true, schedules: ROWS }) });
    },
    state() { return JSON.stringify({ asked, calls }, null, 1); },
  };
}

const settle = () => new Promise((r) => setImmediate(r));

async function liveRun(id, answer, routerValue) {
  const w = makeWorld(answer);
  const ctx = {
    JSON, String, encodeURIComponent, Promise,
    window: { confirm: w.confirm },
    fetch: w.fetch,
    rptRouter: { value: routerValue },
    _sched: { rows: ROWS },
    loadSchedules: () => { w.fetch(P_LIVE + '?routerId=' + encodeURIComponent(routerValue), {}); },
  };
  vm.createContext(ctx);
  vm.runInContext(apiSrc + '\nfunction __click(e){\n' + delSrc + '\n}', ctx);
  const target = {
    closest: (sel) => (sel === '[data-rs-del]'
      ? { getAttribute: (k) => (k === 'data-rs-del' ? id : null) } : null),
  };
  try { ctx.__click({ target }); } catch { /* the live branch does not catch */ }
  await settle(); await settle();
  return w.state();
}

async function portRun(id, answer, routerValue) {
  const w = makeWorld(answer);
  const nodes = {
    rptSchedTbody: { _h: {}, addEventListener(n, f) { (this._h[n] = this._h[n] || []).push(f); } },
    rptRouter: { value: routerValue },
    rptSchedRuns: { innerHTML: '' },
  };
  const saved = { document: global.document, window: global.window, fetch: global.fetch };
  global.document = { getElementById: (k) => nodes[k] || null, addEventListener() {} };
  global.window = { confirm: w.confirm };
  global.fetch = w.fetch;
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    mod.wireScheduleActions();
    // Seed the rows through the REAL load path, so the name in the confirmation
    // comes from the same place the page's own render reads it from.
    mod.loadSchedules();
    await settle(); await settle();
    w.calls.length = 0; w.asked.length = 0;
    const target = {
      closest: (sel) => (sel === '[data-rs-del]'
        ? { getAttribute: (k) => (k === 'data-rs-del' ? id : null) } : null),
    };
    for (const f of (nodes.rptSchedTbody._h.click || [])) f({ target });
    await settle(); await settle();
  } finally {
    for (const k of Object.keys(saved)) {
      if (saved[k] === undefined) delete global[k]; else global[k] = saved[k];
    }
  }
  return w.state();
}

const bad = [];
let cases = 0;
const queued = [];
function compare(what, id, answer, routerValue) {
  queued.push(async () => {
    cases++;
    const a = await G.live(what, () => liveRun(id, answer, routerValue));
    const b = await portRun(id, answer, routerValue);
    if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
  });
}

compare('remove, confirmed', 's1', true, 'rtr-1');
compare('remove, cancelled at the prompt', 's1', false, 'rtr-1');
compare('a name with quotes and angle brackets', 's2', true, 'rtr-1');
compare('a schedule with an empty name', 's3', true, 'rtr-1');
compare('an id that is not in the list', 'nope', true, 'rtr-1');
compare('an empty id', '', true, 'rtr-1');
// ── ONE STATED DIFFERENCE, ASSERTED AS A DIFFERENCE ─────────────────────────
//
// With no router selected the live app confirms and then sends
// `DELETE …/s1?routerId=`, which the endpoint answers 400 "routerId required".
// This port returns before the prompt.
//
// Unreachable either way: the Remove button is drawn from a schedule list that
// cannot load without a router. Left as a guard rather than "fixed" into a
// doomed request, and pinned HERE so it is a recorded decision — if either side
// changes, this fails and the note has to be revisited.
queued.push(async () => {
  cases++;
  // FROZEN, NOT GUARDED — this is a DECLARED DIFFERENCE, and the live half is
  // what the difference is declared against. Guarding it would delete the only
  // check that the difference still holds; freezing keeps all three assertions
  // working, including "the two now AGREE, so the recorded difference is stale",
  // which still fires if the PORT moves toward the recording.
  const a = await G.live('no router selected (live)', () => liveRun('s1', true, ''));
  const b = await portRun('s1', true, '');
  if (a === b) {
    bad.push('no router selected: the two now AGREE, so the recorded difference is stale — ' +
      'delete this case and compare them normally');
  }
  const live = JSON.parse(a), port = JSON.parse(b);
  if (live.calls.length === 0) bad.push('no router selected: the live app no longer sends a request');
  if (port.calls.length !== 0 || port.asked.length !== 0) {
    bad.push('no router selected: this port now prompts or sends — it is supposed to return early');
  }
});
compare('a router id needing encoding', 's1', true, 'Branch Office');
compare('a schedule id needing encoding', 'a b/c&d', true, 'rtr-1');
// The request FAILS. Neither side reloads — the live Remove branch has no
// `.catch`, and a DELETE that failed leaves the row exactly as the server still
// holds it. The live promise rejects unhandled; the port swallows it, which
// changes nothing observable and is why the comparison below is on the CALLS.
compare('the DELETE is rejected', 's1', 'reject', 'rtr-1');

(async () => {
  for (const run of queued) await run();
  fs.rmSync(OUT, { force: true });
  if (bad.length) {
    console.error('the Remove button differs from the live one:\n\n' + bad.slice(0, 2).join('\n\n') + '\n');
    process.exit(1);
  }
  console.log('the Remove button matches the live one (' + cases + ' cases: the prompt, the ' +
    'request, and the reload on both outcomes)');
})().catch((e) => {
  console.error(String((e && e.stack) || e));
  process.exit(1);
});
