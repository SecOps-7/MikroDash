'use strict';
/**
 * The Scheduled tab's History button, live against ported.
 *
 * ── THE OTHER DELEGATED BRANCH ──────────────────────────────────────────────
 *
 * `sched-list-check` records "the delegated click wiring beyond the markup" as
 * something it cannot see, and `sched-remove-check` closed one of the two
 * branches behind that note. This closes the other: `[data-rs-runs]` fetches a
 * schedule's run history and writes a table into `#rptSchedRuns`.
 *
 * WHAT IS COMPARED: the request, and the MARKUP the response produces. The
 * request matters because a History button that asks the wrong URL shows another
 * schedule's runs; the markup matters because the table has three rules that are
 * easy to get subtly wrong — a zero byte count showing a dash rather than "0 B",
 * an error column that is blank when there is no error, and an empty state that
 * is a sentence rather than an empty table.
 *
 * The `/next/` prefix is the one intended difference and is stripped from both
 * sides in one place, exactly as the export-links and Remove gates do.
 *
 * ── TWO STATED DIFFERENCES, PINNED RATHER THAN SMOOTHED ─────────────────────
 *
 * Both are the port guarding where the live app does not, and both are recorded
 * here so they cannot drift further:
 *
 *   1. `d.runs.length` (live) against `(d.runs || [])` (port). A response with
 *      no `runs` key throws on the live side and renders the empty state here.
 *   2. `esc(String(r.recipients_n))` (live) against `?? 0` (port). A run row
 *      with no recipient count renders the literal text "undefined" on the live
 *      page. Filed in ../MikroDash/ToDo.md.
 *
 * Neither is reachable from this port's own server — it always sends `runs` and
 * always sends `recipients_n` — so the cases naming them drive shapes only a
 * changed or third-party server produces.
 *
 * BOTH DIFFERENCES ARE NOW GONE, fixed upstream on 2026-08-25, so the two cases
 * are compared PLAINLY rather than with a declared difference. That is worth
 * stating because emptying `STATED` had also removed the cases themselves: for
 * some period the port's two defensive fallbacks had no case at all, and a
 * mutation deleting either survived. Measured on 2026-08-31 — the two sides
 * agree on both shapes — not assumed from the upstream fix.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/sched-runs-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

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
const G = LIFT.golden('sched-runs-check');
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
const runsSrc = slice("    var runs = e.target.closest('[data-rs-runs]');", '\n    }', 'the History branch');
// BELIEVABILITY OF THE LIFT: the slice must carry the table it builds, not just
// the fetch. A shorter slice would still run and would render nothing.
if (LIFT.hasReference(ROOT)) assert.match(runsSrc, /rptSchedRuns/, 'the History slice does not reach the box it fills');
if (LIFT.hasReference(ROOT)) assert.match(runsSrc, /No runs yet/, 'the History slice does not carry its empty state');

const apiSrc = slice('  function schedApi(', '\n  }', 'schedApi');
const escSrc = slice('function esc(', '\n}', 'esc');
const fmtSrc = slice('function fmtBytes(', '\n}', 'fmtBytes');

const ENTRY = path.join(ROOT, 'testdata', '.rs-runs.ts');
fs.writeFileSync(ENTRY,
  "export { wireScheduleActions, loadSchedules } from '../web/src/pages/reports-schedules.js';\n");
const OUT = path.join(ROOT, 'testdata', '.rs-runs.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const ROUTER = 'r1';
const ROWS = [{ id: 's1', name: 'Weekly', permitted: true }];

/** Strip the port's `/next/` so only UNINTENDED differences fail. */
const norm = (u) => String(u).replace('/next/api/', '/api/');

function makeWorld(runsBody) {
  const calls = [];
  return {
    calls,
    fetch(url) {
      calls.push(norm(url));
      const body = String(url).includes('/runs') ? runsBody : { ok: true, rows: ROWS, smtpReady: true };
      return Promise.resolve({ ok: true, json: () => Promise.resolve(body) });
    },
  };
}

const settle = () => new Promise((r) => setImmediate(r));

async function liveRun(id, runsBody) {
  const w = makeWorld(runsBody);
  const box = { innerHTML: '' };
  const ctx = {
    JSON, String, Date, Number, Math, encodeURIComponent, Promise, Array, Object,
    fetch: w.fetch,
    rptRouter: { value: ROUTER },
    $: (k) => (k === 'rptSchedRuns' ? box : null),
  };
  vm.createContext(ctx);
  vm.runInContext([escSrc, fmtSrc, apiSrc,
    'function __click(e){\n' + runsSrc + '\n}'].join('\n'), ctx);
  const target = {
    closest: (sel) => (sel === '[data-rs-runs]'
      ? { getAttribute: (k) => (k === 'data-rs-runs' ? id : null) } : null),
  };
  try { ctx.__click({ target }); } catch { /* the live branch has no catch */ }
  await settle(); await settle();
  return { calls: w.calls.filter((u) => u.includes('/runs')), html: box.innerHTML };
}

async function portRun(id, runsBody) {
  const w = makeWorld(runsBody);
  const nodes = {
    rptSchedTbody: { _h: {}, addEventListener(n, f) { (this._h[n] = this._h[n] || []).push(f); } },
    rptRouter: { value: ROUTER },
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
    const target = {
      closest: (sel) => (sel === '[data-rs-runs]'
        ? { getAttribute: (k) => (k === 'data-rs-runs' ? id : null) } : null),
    };
    for (const f of (nodes.rptSchedTbody._h.click || [])) f({ target });
    await settle(); await settle();
  } finally {
    for (const k of Object.keys(saved)) {
      if (saved[k] === undefined) delete global[k]; else global[k] = saved[k];
    }
  }
  return { calls: w.calls.filter((u) => u.includes('/runs')), html: nodes.rptSchedRuns.innerHTML };
}

const RUN = (o) => Object.assign({
  ran_at: '2026-07-15T09:00:00Z', outcome: 'sent', recipients_n: 2, bytes: 4096, error: null,
}, o);

const CASES = {
  'no runs yet': { ok: true, runs: [] },
  'one sent run': { ok: true, runs: [RUN({})] },
  'several runs': { ok: true, runs: [RUN({}), RUN({ outcome: 'failed', error: 'smtp refused' })] },
  // A zero size is a dash, not "0 B" — a run that sent nothing and one with no
  // size recorded read the same to anybody looking.
  'a zero byte count': { ok: true, runs: [RUN({ bytes: 0 })] },
  'no byte count at all': { ok: true, runs: [RUN({ bytes: undefined })] },
  'an error with markup in it': { ok: true, runs: [RUN({ outcome: 'failed', error: '<b>no</b>' })] },
  'markup in an outcome': { ok: true, runs: [RUN({ outcome: 'se<i>nt' })] },
  'zero recipients': { ok: true, runs: [RUN({ recipients_n: 0 })] },
  // RESTORED. The header describes cases for both stated differences, but when
  // STATED was emptied the CASES that drove them went too — so the port's two
  // defensive fallbacks (`d.runs || []` and `recipients_n ?? 0`) had no case at
  // all, and mutations deleting either survived. Both are now compared plainly,
  // because upstream was fixed on 2026-08-25 and the two sides agree: measured,
  // not assumed.
  'a response with NO runs key': { ok: true },
  'a row with NO recipient count': { ok: true, runs: [(() => { const r = RUN({}); delete r.recipients_n; return r; })()] },
  // `ok` false must leave whatever history is already shown.
  'a response that is not ok': { ok: false, runs: [RUN({})] },
};

/** Cases where the two are KNOWN to differ, with the difference declared. */
const STATED = {
  // BOTH STATED DIFFERENCES ARE GONE, fixed upstream on 2026-08-25 and matched
  // here: `(d.runs || [])` for a response with no runs key, and
  // `recipients_n ?? 0` for a row without a count. The entries that named them
  // (ToDo #25, and #26 for the Result cell its fix briefly deleted) are closed.
  // Left EMPTY rather than removed, because the mechanism is what makes a
  // difference declarable at all — and the loop below fails if a declared one
  // stops being true.
};

async function main() {
  const bad = [];
  let checked = 0;
  for (const [name, body] of Object.entries(CASES)) {
    const a = await G.live(name, () => liveRun('s1', body));
    const b = await portRun('s1', body);
    checked++;
    if (JSON.stringify(a) !== JSON.stringify(b)) bad.push({ name, a, b });
  }

  // ── BELIEVABILITY ────────────────────────────────────────────────────────
  //
  // Two empty strings compare equal, and a branch that never fired produces
  // exactly that on both sides. The live side alone must have made the request
  // AND filled the box.
  // RE-AIMED AT THE PORT. "The live side alone must have made the request AND
  // filled the box" was the reasoning, and the port is now the side that must.
  // These are the assertions that stop two empty strings comparing equal, so of
  // everything here they are the ones that must not quietly disappear.
  const probe = await portRun('s1', { ok: true, runs: [RUN({})] });
  assert.equal(probe.calls.length, 1, 'the History branch made no request');
  assert.match(probe.calls[0], /\/api\/reports\/schedules\/s1\/runs\?/,
    'the request does not name the schedule');
  assert.match(probe.html, /<table/, 'the branch wrote no table');
  const empty = await portRun('s1', { ok: true, runs: [] });
  assert.notEqual(empty.html, probe.html, 'the empty state renders the same as a run');

  for (const [name, spec] of Object.entries(STATED)) {
    const a = await G.live('STATED:' + name, () => liveRun('s1', spec.body));
    const b = await portRun('s1', spec.body);
    checked++;
    let lh = a.html;
    if (spec.swap) lh = lh.split(spec.swap[0]).join(spec.swap[1]);
    else if (spec.live !== undefined) lh = spec.live === '' ? b.html && a.html : lh;
    if (spec.live === '') {
      // The live side wrote nothing because it threw. Assert THAT, rather than
      // comparing — a stated difference still has to be the difference stated.
      if (a.html !== '') bad.push({ name, a, b, note: 'live was expected to throw before writing' });
    } else if (lh !== b.html) {
      bad.push({ name, a: { html: lh }, b, note: spec.why });
    }
  }

  if (bad.length) {
    for (const x of bad) {
      console.error('[' + x.name + ']' + (x.note ? '  (' + x.note + ')' : ''));
      console.error('  live ' + JSON.stringify(x.a).slice(0, 240));
      console.error('  port ' + JSON.stringify(x.b).slice(0, 240));
    }
    console.error('\nsched-runs-check: ' + bad.length + ' of ' + checked + ' cases differ');
    process.exit(1);
  }
  fs.rmSync(OUT, { force: true });
  console.log('sched-runs-check: ' + checked + ' cases identical (' +
              Object.keys(STATED).length + ' stated differences)');
}

// A REJECTION MUST NOT BE SILENT. `main()` was called bare, and an assertion
// failing inside it exited 0 with NO OUTPUT AT ALL — a gate that cannot report
// failure. Found when guarding this gate made an assertion fire without a
// reference and the run still "passed".
main().catch((e) => {
  console.error(String((e && e.stack) || e));
  process.exit(1);
});
