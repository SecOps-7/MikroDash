'use strict';
/**
 * WHAT `to=` DOES THE REPORTS PAGE ACTUALLY QUERY, AFTER A PRESET IS CHOSEN?
 *
 * The page keeps ONE boolean — `_rptToManual` live, `toIsManual` in the port —
 * and it has two writers: the operator typing in the To field, and the preset
 * `<select>` changing. `loadReports` reads it, and when it is false it
 * OVERWRITES the To field with the current time before building the query.
 *
 * ── WHY THIS GATE EXISTS ────────────────────────────────────────────────────
 *
 * The port had that boolean INVERTED at the preset writer — `toIsManual = false`
 * where the live page sets `_rptToManual = true`
 * (`../MikroDash/public/app.js:10562`). Nine presets set an explicit end that is
 * not now (prevMonth, prevYear, prevWeek, dayBeforeYesterday, thisDayLastWeek,
 * today, thisWeek, thisMonth, thisYear), and for every one of them the false
 * latch let `loadReports` stomp the To field one line later. "Previous month"
 * queried from the start of last month up to RIGHT NOW.
 *
 * Ninety-three gates were green with that bug in the tree, and they had to be:
 * `nodecheck/reports-presets.test.js` pins `applyPreset`'s ARITHMETIC, and the
 * arithmetic was never wrong. The defect lived in what happened to the field
 * afterwards, which nothing drove. This drives it.
 *
 * ── WHAT IT COMPARES ────────────────────────────────────────────────────────
 *
 * Not the DOM: the QUERY. Both sides get the same frozen clock and the same
 * shim, both have their own preset listener fired, and the `from=`/`to=` pair
 * each one puts on the wire must match. That is the thing the operator sees as
 * "the report covers the wrong period", and it is invisible to a DOM diff
 * because both sides render whatever rows they are handed.
 *
 *   node tools/reports-latch-check.js
 */

const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/reports-latch-check.js --freeze
const G = L.golden('reports-latch-check');

const ROOT = path.join(__dirname, '..');
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-reports-latch.cjs');
const src = L.liveSource(ROOT);

const iife = G.value('iife', () => L.region(src, {
  banner: '// ── Reports page',
  must: ['_rptToManual', 'rptPreset', 'loadReports'],
  mustNot: ['Queues page', 'backupsPage', 'DNS page'],
}));
const IDS = G.value('IDS', () => L.idsFor(src, iife));

// ── THE CLOCK IS FROZEN, AND IT HAS TO BE ───────────────────────────────────
//
// Every preset is arithmetic on `new Date()`, and `dtVal` truncates to the
// minute — so two sides evaluated either side of a minute boundary would differ by
// one minute and this gate would fail perhaps once an hour. Worse, it would
// PASS the rest of the time while telling nobody it was flaky. A fixed instant
// removes the question.
//
// Mid-month, mid-week, mid-day and mid-year on purpose: a clock sitting on a
// boundary makes `prevMonth`'s end and `thisMonth`'s start coincide, and two
// presets that should differ would agree by accident.
const FIXED = new Date(2026, 6, 15, 14, 37, 0, 0).getTime(); // 2026-07-15 14:37 local
function frozen() {
  class FrozenDate extends Date {
    constructor(...a) { if (a.length === 0) super(FIXED); else super(...a); }
    static now() { return FIXED; }
  }
  return FrozenDate;
}

/** Every preset whose end is NOT the current time, plus rolling ones for contrast. */
// ── WHAT THIS GATE COVERS, ANSWERED RATHER THAN GUESSED ─────────────────────
//
// `element-coverage-audit` text-scans a gate's quoted strings when it declares
// no `--ids`. This one reaches its controls as `doc.nodes.rptRouter`, a property
// access, so the scan saw none of them and reported five elements as uncovered
// that this gate drives on every run.
//
// ONLY WHAT IS ACTUALLY EXERCISED is listed. `rptAggregate` is READ by
// `loadReports` and never varied here, so it is not claimed — a gate that
// overstates its coverage is worse than one that understates it, because the
// audit is what decides where the next gate goes.
const COVERS = ['rptPreset', 'rptTo', 'rptFrom', 'rptRouter', 'rptLoadBtn', 'rptSpinner'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

const PRESETS = [
  'prevMonth', 'prevYear', 'prevWeek', 'dayBeforeYesterday', 'thisDayLastWeek',
  'today', 'thisWeek', 'thisMonth', 'thisYear',
  'todaySoFar', 'thisWeekSoFar', 'thisMonthSoFar', 'thisYearSoFar',
  'last1h', 'last24h', 'last7d', 'last30d', 'last1y',
];

// ── THE OTHER WRITER ────────────────────────────────────────────────────────
//
// The latch has two writers and the preset list above exercises one of them.
// Deleting the To field's own `change` listener SURVIVED this gate until these
// cases existed — measured, not assumed. A typed end must reach the wire
// unchanged when Load is pressed; without the listener `loadReports` overwrites
// it with now, and the operator's chosen end silently becomes "whenever I
// clicked".
const TYPED = ['2026-07-01T00:00', '2026-06-30T23:59', '2025-01-02T09:15'];

// ── TWO STATES THE PRESETS AND THE TYPED ENDS BOTH MISS ─────────────────────
//
// `bare` presses Load having touched nothing, which is the ONLY case that can
// see the latch's initial value; starting it true survived every case above,
// because every one of them wrote to it first.
//
// `cleared` types an end and then EMPTIES the box — the only way to reach
// `loadReports` with the latch true and the field blank. That combination is
// what makes `dateToTs('', true)` differ from `dateToTs('', false)`: with a
// value present the flag changes nothing, which is why flipping it survived
// twenty-one runs. An operator clearing the To box is not a contrived state.
//
// `bare` alone was NOT enough, and the way it failed is worth recording: the
// page restores a SAVED preset on mount, so the To field is never actually
// untouched. The default fallback is `last7d`, whose end IS now — so a latch
// that started true (leaving the restored end) and one that started false
// (overwriting it with now) produced the same string, and the mutation
// survived. `bareSaved` restores `prevMonth` instead, whose end is in June, and
// the two answers separate. An operator with a saved preset is the common case,
// not the exotic one.
//
// `clearedFrom` empties the FROM box, which is the only state where
// `dateToTs(from, false)` differs from `dateToTs(from, true)`.
const BARE = ['bare', 'bareSaved', 'cleared', 'clearedFrom'];

/** The preset a run's localStorage hands back on mount, or null for the default. */
function savedPreset(kind) { return kind === 'bareSaved' ? 'prevMonth' : null; }

/** Drive one side: type into To, fire its change, then press Load. */
function driveTyped(doc, value) {
  const to = doc.nodes.rptTo;
  to.value = value;
  to.fire('change');
  doc.nodes.rptLoadBtn.fire('click');
}

/** Drive one side through a state that never writes a usable end. */
function driveBare(doc, kind) {
  if (kind === 'cleared' || kind === 'clearedFrom') {
    const to = doc.nodes.rptTo;
    to.value = '2026-05-05T05:05';
    to.fire('change');   // the latch takes here...
    to.value = '';       // ...and the operator then empties the box
    if (kind === 'clearedFrom') doc.nodes.rptFrom.value = '';
  }
  doc.nodes.rptLoadBtn.fire('click');
}

/**
 * The BUSY state, read at two moments.
 *
 * `#rptSpinner` and the Load button are the only sign the page gives that a load
 * is in flight, and both are invisible to a snapshot taken after the promises
 * settle — which is every snapshot this gate took until now. So they are read
 * SYNCHRONOUSLY after the trigger and again after the settle, and the pair is
 * compared.
 *
 * The second reading is why the failure path matters: both sides hide the
 * spinner in a `.then` AFTER the `.catch`, so a load that fails still re-enables
 * the page. A `finally` that only ran on success would leave the page looking
 * permanently busy, and only an after-reading on a REJECTED run can see it.
 */
function busy(doc) {
  const sp = doc.nodes.rptSpinner;
  const btn = doc.nodes.rptLoadBtn;
  return { spinner: sp ? sp.style.display : null, loadDisabled: btn ? btn.disabled : null };
}

/** The from/to a run put on the wire, plus what the To field ended up showing. */
function readQuery(urls, doc) {
  // The LAST range-carrying request, not the first: a mount that auto-loads
  // would otherwise be what this reads, and the preset's own load — the thing
  // on trial — would never be looked at.
  const hit = [...urls].reverse().find((u) => u.indexOf('from=') !== -1);
  const q = {};
  if (hit) {
    for (const part of hit.split('?')[1].split('&')) {
      const [k, v] = part.split('=');
      if (k === 'from' || k === 'to') q[k] = v;
    }
  }
  q.field = doc.nodes.rptTo ? doc.nodes.rptTo.value : null;
  return q;
}

async function liveRun(preset, typed, bare) {
  const doc = makeDoc(IDS, {});
  const urls = [];
  const ctx = {
    String, Array, Math, Number, Object, JSON, Date: frozen(),
    parseInt, parseFloat, isFinite, isNaN, encodeURIComponent, document: doc,
    socket: { on() {}, emit() {} },
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {},
    window: { location: { origin: 'http://x' } },
    fetch: (u) => { urls.push(String(u)); return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true, rows: [] }) }); },
    Chart: function () { return { destroy() {}, update() {}, data: {}, options: {} }; },
    MutationObserver: function () { return { observe() {}, disconnect() {} }; },
    ResizeObserver: function () { return { observe() {}, disconnect() {} }; },
    requestAnimationFrame: (fn) => { fn(); return 0; },
    cancelAnimationFrame: () => {},
    localStorage: { getItem: () => savedPreset(bare), setItem() {}, removeItem() {} },
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function fmtBytes('),
    L.whole(src, 'function fmtMbps('),
    L.whole(src, 'function maxOf('),
    L.whole(src, 'function fmtDataMB('),
    L.whole(src, 'function _sortRows('),
    L.whole(src, 'function _sortMul('),
    L.whole(src, 'function _renderSortHeader('),
    'function $(id){return document.getElementById(id);}',
    'function _debounce(fn){return fn;}',
    'function pageVisible(){return true;}',
    L.line(src, 'var _displayTimezone'),
    '(function () {' + iife + '\n})();',
  ].join('\n'), ctx);

  // The page's own listener, fired the way the browser fires it.
  // `loadReports` returns at once with no router selected, so the page has to
  // look like a page that has one. Set AFTER the IIFE on both sides, so neither
  // gets an extra auto-load the other does not.
  doc.nodes.rptRouter.value = 'r1';
  // ── ONE READER FOR EVERY PATH ───────────────────────────────────────────
  //
  // Three early returns, and adding the busy fields to two of them left the
  // `typed` cases comparing an object with them against one without. Same shape
  // as a corpus losing a case: the difference was real and said nothing about
  // the page.
  const finish = async () => {
    const q = readQuery(urls, doc);
    q.during = busy(doc);                          // before the promises settle
    await new Promise((r) => setImmediate(r));
    q.after = busy(doc);                           // and after
    return q;
  };
  if (bare !== undefined) { driveBare(doc, bare); return finish(); }
  if (typed !== undefined) { driveTyped(doc, typed); return finish(); }
  const sel = doc.nodes.rptPreset;
  assert.ok(sel && sel._listeners && sel._listeners.change,
    'the live region installed no change listener on #rptPreset — the lift has broken');
  sel.value = preset;
  sel.fire('change');
  return finish();
}

async function portRun(preset, typed, bare) {
  execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
    [path.join(ROOT, 'web', 'src', 'pages', 'reports.ts'),
     '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
    { stdio: 'inherit' });

  const doc = makeDoc(IDS, {});
  const urls = [];
  const prev = {
    window: globalThis.window, fetch: globalThis.fetch, Date: globalThis.Date,
    ls: globalThis.localStorage, st: globalThis.setTimeout,
  };
  globalThis.window = { location: { origin: 'http://x' } };
  globalThis.fetch = (u) => { urls.push(String(u)); return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true, rows: [] }) }); };
  globalThis.Date = frozen();
  globalThis.localStorage = { getItem: () => savedPreset(bare), setItem() {}, removeItem() {} };
  globalThis.setTimeout = ((fn) => { fn(); return 0; });
  // ── THE SHIM STAYS INSTALLED UNTIL THE PROMISES HAVE SETTLED ────────────
  //
  // `loadReports` fetches five endpoints and renders in the `.then`. Those
  // callbacks run on the microtask queue, i.e. AFTER a synchronous helper would
  // have restored the real globals — and they then throw `document is not
  // defined`, as unhandled rejections that print past the gate's own output and
  // look like the gate failing. The query itself is captured synchronously, so
  // the wait is not for correctness; it is so the run is quiet enough to read.
  const prevDoc = globalThis.document;
  globalThis.document = doc;
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    mod.mountReports([]);
    doc.nodes.rptRouter.value = 'r1';
    if (bare !== undefined) {
      driveBare(doc, bare);
    } else if (typed !== undefined) {
      driveTyped(doc, typed);
    } else {
      const sel = doc.nodes.rptPreset;
      assert.ok(sel && sel._listeners && sel._listeners.change,
        'the port installed no change listener on #rptPreset');
      sel.value = preset;
      sel.fire('change');
    }
    const q = readQuery(urls, doc);
    q.during = busy(doc);
    await new Promise((r) => setImmediate(r));
    q.after = busy(doc);
    return q;
  } finally {
    if (prevDoc === undefined) delete globalThis.document; else globalThis.document = prevDoc;
    for (const [k, g] of [['window', 'window'], ['fetch', 'fetch'], ['Date', 'Date'],
                          ['ls', 'localStorage'], ['st', 'setTimeout']]) {
      if (prev[k] === undefined) delete globalThis[g]; else globalThis[g] = prev[k];
    }
  }
}

async function main() {
  const bad = [];
  let distinct = new Set();
  const RUNS = PRESETS.map((p) => ({ label: p, preset: p }))
    .concat(TYPED.map((v) => ({ label: 'typed ' + v, typed: v })))
    .concat(BARE.map((k) => ({ label: k, bare: k })));
  for (const run of RUNS) {
    const preset = run.preset, typed = run.typed, bare = run.bare;
    const live = await G.live(run.label, () => liveRun(preset, typed, bare));
    const port = await portRun(preset, typed, bare);

    // ── A RUN THAT QUERIED NOTHING IS NOT A RUN ──────────────────────────
    //
    // `readQuery` returns {} when no fetch carried a range, and two empty
    // objects compare equal. Without this, a listener that silently failed to
    // reach `loadReports` on BOTH sides would print green.
    if (live.from === undefined || live.to === undefined) {
      console.error('[' + run.label + '] the LIVE side put no from/to on the wire — ' +
                    'the preset listener is not reaching loadReports');
      process.exit(1);
    }
    // A TYPED end that came back as `now` means the latch never took, on the
    // LIVE side — the case would then compare two identical wrong answers.
    if (typed !== undefined && live.field !== typed) {
      console.error('[' + run.label + '] the LIVE To field reads ' + live.field +
                    ' after typing ' + typed + ' — the typed end was overwritten, so ' +
                    'this case cannot tell the two sides apart');
      process.exit(1);
    }
    distinct.add(live.from + '|' + live.to);
    if (JSON.stringify(live) !== JSON.stringify(port)) {
      bad.push({ preset: run.label, live, port });
    }
  }

  // ── THE BUSY STATE MUST HAVE MOVED ───────────────────────────────────────
  //
  // `during` and `after` are compared on every run, and two pages that never
  // touched the spinner would agree on `{spinner: undefined}` twice. So the LIVE
  // side alone must SHOW it while loading and HIDE it afterwards — an assertion
  // that fails the moment the readings stop being taken at two different
  // moments, which is the only thing making them worth comparing.
  const probe = await G.live('auto:1', () => liveRun('last7d'));
  assert.equal(probe.during.spinner, '',
    'the LIVE page did not show its spinner while loading');
  assert.equal(probe.after.spinner, 'none',
    'the LIVE page did not hide its spinner after loading');
  assert.equal(probe.during.loadDisabled, true, 'the LIVE Load button stayed enabled mid-load');
  assert.equal(probe.after.loadDisabled, false, 'the LIVE Load button stayed disabled afterwards');

  // ── THE CLOCK MUST HAVE MATTERED ─────────────────────────────────────────
  //
  // If every preset produced the same range, the arithmetic is not running and
  // the whole corpus is one case wearing eighteen names.
  // `bare` and `cleared` both end up querying up to `now`, so they share a range
  // with each other and with the rolling presets. Three collisions are expected;
  // the check is that the arithmetic RAN, not that every run is unique.
  if (distinct.size < RUNS.length - 7) {
    console.error('only ' + distinct.size + ' distinct ranges across ' + RUNS.length +
                  ' presets — the preset arithmetic is not being exercised');
    process.exit(1);
  }

  if (bad.length) {
    for (const b of bad) {
      console.error('[' + b.preset + ']');
      console.error('  live from=' + b.live.from + ' to=' + b.live.to + ' field=' + b.live.field);
      console.error('  port from=' + b.port.from + ' to=' + b.port.to + ' field=' + b.port.field);
    }
    console.error('\nreports-latch-check: ' + bad.length + ' preset(s) query a different range');
    process.exit(1);
  }
  console.log('reports-latch-check: ' + RUNS.length + ' runs (' + PRESETS.length +
              ' presets, ' + TYPED.length + ' typed ends, ' + BARE.length + ' untouched) ' +
              'query the same range as the ' +
              'live page (' + distinct.size + ' distinct)');
}

// A REJECTION MUST NOT BE SILENT — see the note in sched-runs-check: a bare
// `main()` lets an assertion failure inside it exit 0 with no output.
main().catch((e) => {
  console.error(String((e && e.stack) || e));
  process.exit(1);
});
