'use strict';
/**
 * The Scheduled tab's SEND NOW button, live against ported.
 *
 * ── WHY IT EXISTS NOW AND NOT WITH THE REMOVE GATE ──────────────────────────
 *
 * `sched-remove-check.js` says why it covered Remove alone: "Remove is the one
 * whose endpoint exists on this side". That has stopped being true.
 * `POST schedules/{id}/run` is served — `internal/server/reports_run.go`, with
 * the fpdf renderer and the mailer behind it — so the button was drawn, enabled,
 * and bound to nothing. This gate is the other half of wiring it.
 *
 * ── WHAT IS COMPARED, AND WHY THE BUTTON ITSELF IS PART OF IT ───────────────
 *
 * The REQUEST and the BUTTON'S OWN STATE. Send now is the slowest control on the
 * page — it builds a document and talks to an SMTP server — so the live handler
 * disables it and relabels it before the request goes out, and without that an
 * impatient operator sends the same report three times. A gate comparing only
 * the fetch would pass against a port that left the button live.
 *
 * ── AND IT RELOADS ON BOTH OUTCOMES, WHICH REMOVE DOES NOT ──────────────────
 *
 * Asserted as a DIFFERENCE FROM THE NEIGHBOURING BRANCH rather than assumed: the
 * live Remove branch has no `.catch` and this one does. The reason is in the
 * endpoint — a run that did not send answers 200 with `ok:false` and a reason,
 * and the reason reaches the operator through the run HISTORY rather than
 * through this response. The reload IS how the answer is displayed.
 *
 * The one intended difference is the prefix: the port's report endpoints live
 * under `/next/`. Stripped from both sides in one place, as in the Remove gate,
 * so every OTHER difference still fails.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/sched-run-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

// As in the Remove gate: `schedApi` chains `.then(r => r.json())` and a browser
// logs an unhandled rejection and carries on where Node aborts. The Send-now
// branch DOES have a `.catch`, so this should never fire — it is here so a
// harness fault surfaces as a comparison failure rather than a process abort.
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
const G = LIFT.golden('sched-run-check');
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
const runSrc = slice("    var run = e.target.closest('[data-rs-run]');", '\n    }', 'the Send now branch');
const apiSrc = slice('  function schedApi(path, opts) {', '\n  }', 'schedApi');

// MARKER ASSERTIONS on the lifted slice, because a slice that lost its body
// would compare two branches that both do nothing and pass.
// GUARDED: each asks whether the lifted SLICE still contains a marker.
for (const marker of LIFT.hasReference(ROOT)
  ? ['run.disabled = true', "run.textContent = 'Sending…'", "'/run'", '.catch(']
  : []) {
  if (!runSrc.includes(marker)) {
    throw new Error('the lifted Send now branch has no ' + JSON.stringify(marker) +
      ' — the slice is wrong, or the live handler changed shape');
  }
}

const ENTRY = path.join(ROOT, 'testdata', '.schedrun-entry.ts');
fs.writeFileSync(ENTRY,
  "export { wireScheduleActions, loadSchedules } from '../web/src/pages/reports-schedules.js';\n");
const OUT = path.join(ROOT, 'testdata', '.schedrun-port.cjs');
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
  { id: 'a b/c&d', name: 'Odd id', sections: [], recipients: [], frequency: 'daily', sendHour: 6, enabled: true },
];

// The button is a real observable object, not a stub that swallows writes: the
// disable and the relabel are half of what this gate compares.
function makeButton(id) {
  return {
    disabled: false,
    textContent: 'Send now',
    getAttribute: (k) => (k === 'data-rs-run' ? id : null),
  };
}

function makeWorld(mode) {
  const calls = [];
  return {
    calls,
    fetch: (url, init) => {
      const cred = (init && init.credentials) || 'same-origin';
      calls.push({ url: strip(url), method: (init && init.method) || 'GET', credentials: cred });
      // ONLY THE POST FAILS, for the Remove gate's reason: rejecting every
      // request also breaks the load that seeds the rows.
      if (mode === 'reject' && (init && init.method) === 'POST') {
        return Promise.reject(new Error('network'));
      }
      // A RUN THAT DID NOT SEND IS STILL A 200 with ok:false. That is the live
      // endpoint's contract, and it is why this branch reloads either way —
      // there is no rejected promise to distinguish "sent" from "skipped".
      if (mode === 'notsent' && (init && init.method) === 'POST') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ ok: false, outcome: 'skipped', error: 'no SMTP configured' }),
        });
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true, schedules: ROWS }) });
    },
    state(btn) {
      return JSON.stringify({
        calls, button: { disabled: btn.disabled, textContent: btn.textContent },
      }, null, 1);
    },
  };
}

const settle = () => new Promise((r) => setImmediate(r));

async function liveRun(id, mode, routerValue) {
  const w = makeWorld(mode);
  const btn = makeButton(id);
  const ctx = {
    JSON, String, encodeURIComponent, Promise,
    fetch: w.fetch,
    // `schedApi` reads `rptRouter` for the routerId, so this is not optional
    // scenery: without it the branch throws a ReferenceError, the try/catch
    // below swallows it, and the live side records NO calls — which reads as
    // "the live app does not send" rather than as a broken harness.
    rptRouter: { value: routerValue },
    _sched: { rows: ROWS },
    loadSchedules: () => { w.fetch(P_LIVE + '?routerId=' + encodeURIComponent(routerValue), {}); },
  };
  vm.createContext(ctx);
  vm.runInContext(apiSrc + '\nfunction __click(e){\n' + runSrc + '\n}', ctx);
  const target = { closest: (sel) => (sel === '[data-rs-run]' ? btn : null) };
  try { ctx.__click({ target }); } catch { /* the live branch does not catch */ }
  await settle(); await settle();
  return w.state(btn);
}

async function portRun(id, mode, routerValue) {
  const w = makeWorld(mode);
  const btn = makeButton(id);
  const nodes = {
    rptSchedTbody: { _h: {}, addEventListener(n, f) { (this._h[n] = this._h[n] || []).push(f); } },
    rptRouter: { value: routerValue },
    rptSchedRuns: { innerHTML: '' },
  };
  const saved = { document: global.document, window: global.window, fetch: global.fetch };
  global.document = { getElementById: (k) => nodes[k] || null, addEventListener() {} };
  global.window = {};
  global.fetch = w.fetch;
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    mod.wireScheduleActions();
    mod.loadSchedules();
    await settle(); await settle();
    w.calls.length = 0;
    const target = { closest: (sel) => (sel === '[data-rs-run]' ? btn : null) };
    for (const f of (nodes.rptSchedTbody._h.click || [])) f({ target });
    await settle(); await settle();
  } finally {
    for (const k of Object.keys(saved)) {
      if (saved[k] === undefined) delete global[k]; else global[k] = saved[k];
    }
  }
  return w.state(btn);
}

const bad = [];
let cases = 0;
const queued = [];
function compare(what, id, mode, routerValue) {
  queued.push(async () => {
    cases++;
    const a = await G.live(what, () => liveRun(id, mode, routerValue));
    const b = await portRun(id, mode, routerValue);
    if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
  });
}

compare('send now, the run succeeds', 's1', 'ok', 'rtr-1');
// A RUN THAT DID NOT SEND. 200 with ok:false, which is the case the reload
// exists for — the reason reaches the operator through the history, not here.
compare('send now, the run reports it did not send', 's1', 'notsent', 'rtr-1');
compare('send now, the request is rejected', 's1', 'reject', 'rtr-1');
compare('a schedule id needing encoding', 'a b/c&d', 'ok', 'rtr-1');
compare('a router id needing encoding', 's1', 'ok', 'Branch Office');
compare('an id that is not in the list', 'nope', 'ok', 'rtr-1');
compare('an empty id', '', 'ok', 'rtr-1');

// ── ONE STATED DIFFERENCE, ASSERTED AS A DIFFERENCE ─────────────────────────
//
// The Remove gate records the same one for the same reason. With no router
// selected the live app sends `POST …/s1/run?routerId=`, which the endpoint
// answers 400; this port returns before the fetch.
//
// THE BUTTON IS ALREADY DISABLED AND RELABELLED BY THEN ON BOTH SIDES, which is
// the part worth pinning: the live handler does it before reading the id, and
// this port does too, so an early return still leaves the operator looking at a
// disabled "Sending…" that will never resolve. Reproduced rather than improved,
// and unreachable either way — the button is drawn from a list that cannot load
// without a router.
queued.push(async () => {
  cases++;
  // FROZEN, NOT GUARDED — a DECLARED DIFFERENCE, and the live half is what it is
  // declared against. See the note in sched-remove-check.
  const a = await G.live('no router selected (live)', () => liveRun('s1', 'ok', ''));
  const b = await portRun('s1', 'ok', '');
  if (a === b) {
    bad.push('no router selected: the two now AGREE, so the recorded difference is stale — ' +
      'delete this case and compare them normally');
  }
  const live = JSON.parse(a), port = JSON.parse(b);
  if (live.calls.length === 0) bad.push('no router selected: the live app no longer sends a request');
  if (port.calls.length !== 0) {
    bad.push('no router selected: this port now sends — it is supposed to return early');
  }
  for (const [side, st] of [['live', live], ['port', port]]) {
    if (!st.button.disabled || st.button.textContent !== 'Sending…') {
      bad.push('no router selected: the ' + side + ' button was not left disabled and relabelled, ' +
        'which is the recorded shape of this branch');
    }
  }
});

(async () => {
  for (const run of queued) await run();
  fs.rmSync(OUT, { force: true });
  if (bad.length) {
    console.error('the Send now button differs from the live one:\n\n' +
      bad.slice(0, 2).join('\n\n') + '\n');
    process.exit(1);
  }
  console.log('the Send now button matches the live one (' + cases + ' cases: the request, the ' +
    'disable-and-relabel, and the reload on all three outcomes)');
})().catch((e) => {
  console.error(String((e && e.stack) || e));
  process.exit(1);
});
