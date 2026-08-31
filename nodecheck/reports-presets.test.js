'use strict';
/**
 * The Reports page's pure pieces, compared against the live implementation:
 * the date presets, and the formatters the tabs share.
 *
 * ── WHY THIS ONE NEEDS A GATE AT ALL ────────────────────────────────────────
 *
 * Twenty-five presets, and the arithmetic in them is the kind that looks right
 * and is not: a Monday-start week computed from `getDay()` where Sunday is 0,
 * "end of month" as day 0 of the NEXT month, "six months ago" on the 31st of a
 * month whose counterpart has thirty days. Each is a one-line expression whose
 * wrong version also produces a plausible date.
 *
 * And they are all LOCAL-TIME expressions — `setHours(0,0,0,0)` is midnight
 * where the operator is — so a day containing a DST transition is 23 or 25 hours
 * long and "seven days ago at midnight" is not `now - 7*86400000`.
 *
 * ── HOW IT COMPARES ─────────────────────────────────────────────────────────
 *
 * The live `_applyRptPreset` lives inside a page IIFE, writes into two DOM
 * inputs, and calls `new Date()` itself. So it is LIFTED out of app.js by text,
 * given stub inputs and a frozen clock, and run — rather than reimplemented
 * here, which would only ever test a copy against itself.
 *
 * The port's half is the REAL module, bundled by esbuild for the same reason.
 */

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const APP = path.join(LIVE, 'public', 'app.js');
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'reports-presets.cjs');
const DOM_OUT = path.join(ROOT, 'web', 'dist', '_compare', 'reports-dom.cjs');

/** Every preset the page's own <select> offers, plus one it does not. */
const PRESETS = [
  'last1h', 'last3h', 'last6h', 'last12h', 'last24h', 'last2d', 'last7d',
  'last30d', 'last90d', 'last6mo', 'last1y',
  'dayBeforeYesterday', 'thisDayLastWeek', 'prevWeek', 'prevMonth', 'prevYear',
  'today', 'thisWeek', 'thisMonth', 'thisYear',
  'todaySoFar', 'thisWeekSoFar', 'thisMonthSoFar', 'thisYearSoFar',
  // The original returns without touching the inputs, and a port that defaulted
  // to something would silently move a range the operator had set.
  'nonsense',
];

/**
 * Instants chosen for what each one breaks. All parsed as LOCAL time on purpose:
 * the presets are local-time arithmetic, so the interesting cases are relative
 * to the machine running the test rather than to UTC.
 */
const NOWS = [
  '2026-01-01T00:00:00',   // year boundary, exactly midnight
  '2026-01-01T00:00:01',   // one second past it
  '2025-12-31T23:59:59',   // one second before
  '2026-03-31T12:00:00',   // the 31st: "six months ago" lands on a 30-day month
  '2026-05-31T12:00:00',   // the 31st again, the other direction
  '2026-02-28T12:00:00',   // end of a short February
  '2024-02-29T12:00:00',   // a leap day: "one year ago" has no counterpart
  '2026-03-01T00:30:00',   // just after a month boundary
  '2026-03-29T12:00:00',   // European spring forward: a 23-hour day
  '2026-10-25T12:00:00',   // European autumn back: a 25-hour day
  '2026-08-17T09:00:00',   // a Monday — the week arithmetic's edge
  '2026-08-23T09:00:00',   // a Sunday, where getDay() is 0 and (day===0?6:day-1) fires
  '2026-08-20T09:00:00',   // an ordinary Thursday
  '2026-12-31T23:00:00',   // the last hour of a year
];

/**
 * Lift `_applyRptPreset` and the helpers it closes over out of app.js.
 *
 * Sliced by text because it is inside an IIFE with no export. The slice is
 * anchored on the function's own opening line and closed by brace counting, so
 * an unrelated edit above or below does not move the boundaries.
 */
function liftApplyPreset(src) {
  // THE ASSEMBLED PROGRAM IS WHAT IS RECORDED. The body, `_dtVal` and the `_rptP`
  // pad are three separate slices that only mean anything together, so recording
  // them apart would keep pieces that are never run in isolation.
  const text = frozen('applyRptPreset', () => {

  const start = src.indexOf('  function _applyRptPreset(val) {');
  assert.ok(start > -1, '_applyRptPreset not found in app.js — has it been renamed?');
  let depth = 0;
  let i = src.indexOf('{', start);
  const from = i;
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') {
      depth--;
      if (depth === 0) break;
    }
  }
  assert.strictEqual(depth, 0, '_applyRptPreset is not brace-balanced');
  const body = src.slice(from, i + 1);

  const dvStart = src.indexOf('  function _dtVal(d) {');
  assert.ok(dvStart > -1, '_dtVal not found in app.js');
  const dtVal = src.slice(dvStart, src.indexOf('}', dvStart) + 1);
  assert.ok(dtVal.includes('getFullYear'), '_dtVal was sliced wrongly');

  // `_rptP` is defined above both and closed over.
  const pad = "var _rptP = function(n){ return String(n).padStart(2,'0'); };";
    return `
    ${pad}
    ${dtVal}
    function _applyRptPreset(val) ${body}
    return _applyRptPreset;
  `;
  });
  return new Function('rptFrom', 'rptTo', 'Date', text);
}

/** A Date subclass whose no-argument form is frozen. */
function frozenDateClass(nowMs) {
  return class FrozenDate extends Date {
    constructor(...args) {
      if (args.length === 0) super(nowMs);
      else super(...args);
    }
    static now() { return nowMs; }
  };
}

/**
 * Lift a named top-level function out of app.js by brace counting.
 *
 * `fmtDataMB` and `maxOf` are NOT inside the reports IIFE — they were hoisted
 * out of it so every page could use one implementation — so they slice cleanly
 * on their own declaration.
 */
// ── THE LIFTED LIVE SOURCE, RECORDED ────────────────────────────────────────
//
// Both tests here EXECUTE text lifted from the live `app.js`, so the text is
// what has to survive the reference going. Recording it keeps the live halves
// running — a new case added later still gets a live answer, which a recording
// of the ANSWERS could not give.
//
// Regenerate with MIKRODASH_PRESETS_FREEZE=1 and a reference present.
const REC_FILE = path.join(__dirname, 'testdata', 'reports-presets-live.json');
const recorded = fs.existsSync(REC_FILE) ? JSON.parse(fs.readFileSync(REC_FILE, 'utf8')) : {};
// '' when the reference is gone. The lifters below are only reached inside
// `frozen()`, which does not call them without one.
const appSrc = fs.existsSync(APP) ? fs.readFileSync(APP, 'utf8') : '';
const freezing = !!process.env.MIKRODASH_PRESETS_FREEZE;

/** Record `fn()`'s text under `key`, or replay it. */
function frozen(key, fn) {
  if (fs.existsSync(APP)) {
    const fresh = fn();
    if (freezing) {
      recorded[key] = fresh;
      fs.mkdirSync(path.dirname(REC_FILE), { recursive: true });
      fs.writeFileSync(REC_FILE, JSON.stringify(recorded, null, 2) + '\n');
      return fresh;
    }
    if (recorded[key] !== undefined) {
      assert.strictEqual(fresh, recorded[key],
        'the recorded live source for ' + key + ' no longer matches app.js — '
        + 'regenerate with MIKRODASH_PRESETS_FREEZE=1');
    }
    return fresh;
  }
  assert.ok(typeof recorded[key] === 'string' && recorded[key].length > 20,
    'no recorded live source for ' + key + ' at ' + REC_FILE
    + '. Regenerate with a reference present: MIKRODASH_PRESETS_FREEZE=1');
  return recorded[key];
}

/** The TEXT of a top-level live function, so it can be recorded. */
function liftTopLevelSrc(src, name) {
  const start = src.indexOf('function ' + name + '(');
  assert.ok(start > -1, name + ' not found in app.js');
  let depth = 0;
  let i = src.indexOf('{', start);
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') {
      depth--;
      if (depth === 0) break;
    }
  }
  assert.strictEqual(depth, 0, name + ' is not brace-balanced');
  return src.slice(start, i + 1);
}

function liftTopLevel(src, name) {
  // The RECORDING FIRST: the anchor walk runs only inside `frozen()`, which does
  // not call it without a reference. Computing the anchors before that made this
  // fail with "not found in app.js" on an empty source — a lift that ran when
  // there was nothing to lift from.
  const text = frozen('fn:' + name, () => liftTopLevelSrc(src, name));
  return new Function(`${text} return ${name};`)();
}

test('the shared report formatters match the live page', () => {
  // NO LONGER FAILS WITHOUT THE REFERENCE — the live halves are recorded below.
  // This assert made the whole file inert the moment the reference went.

  fs.mkdirSync(path.dirname(DOM_OUT), { recursive: true });
  execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'), [
    path.join(ROOT, 'web', 'src', 'dom.ts'),
    '--bundle', '--format=cjs', '--platform=node', '--log-level=error', '--outfile=' + DOM_OUT,
  ], { cwd: ROOT });
  const { fmtDataMB, maxOf } = require(DOM_OUT);

  const src = appSrc;
  const liveFmtDataMB = liftTopLevel(src, 'fmtDataMB');
  const liveMaxOf = liftTopLevel(src, 'maxOf');

  // The unit boundaries and either side of each, plus the shapes that decide
  // which branch runs. 1000 MB is a GB here, not 1024 — decimal on purpose.
  const volumes = [
    0, 0.0004, 0.5, 0.999, 1, 1.05, 9.95, 99.99, 999, 999.99, 1000, 1000.004,
    1024, 1500, 999999, 1e6, 1e6 + 1, 2.5e6, null, undefined, NaN, -1, -0.5,
  ];
  for (const v of volumes) {
    assert.strictEqual(fmtDataMB(v), liveFmtDataMB(v), 'fmtDataMB(' + String(v) + ')');
  }

  // maxOf, including the case the live app left a comment about: a spread would
  // overflow the stack here, and a report query really can return this many rows.
  const arrays = [
    [], [0], [1, 7, 3], [-5, -2], [0, -0], [1.5, 1.4999], ['3', 7], [null, 4],
    [undefined, 2], Array.from({ length: 200000 }, (_, i) => i % 977),
  ];
  for (const a of arrays) {
    const label = a.length > 10 ? '[' + a.length + ' entries]' : JSON.stringify(a);
    assert.strictEqual(maxOf(a), liveMaxOf(a), 'maxOf(' + label + ')');
  }
});

test('the Reports date presets match the live page', () => {
  // NO LONGER FAILS WITHOUT THE REFERENCE — the live halves are recorded below.
  // This assert made the whole file inert the moment the reference went.

  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'), [
    path.join(ROOT, 'web', 'src', 'pages', 'reports.ts'),
    '--bundle', '--format=cjs', '--platform=node', '--log-level=error', '--outfile=' + OUT,
  ], { cwd: ROOT });
  const { presetRange, dtVal } = require(OUT);

  const make = liftApplyPreset(appSrc);

  let compared = 0;
  for (const nowStr of NOWS) {
    const now = new Date(nowStr);
    assert.ok(!Number.isNaN(+now), 'unparseable test instant ' + nowStr);
    const FrozenDate = frozenDateClass(+now);

    for (const preset of PRESETS) {
      // The live side writes into these; an unknown preset must leave them
      // alone, so they start holding a value nothing would produce.
      const from = { value: 'UNTOUCHED' };
      const to = { value: 'UNTOUCHED' };
      make(from, to, FrozenDate)(preset);

      const mine = presetRange(preset, now);
      if (from.value === 'UNTOUCHED') {
        assert.strictEqual(mine, null,
          `${preset} @ ${nowStr}: the live page left the inputs alone, the port returned a range`);
        compared++;
        continue;
      }
      assert.ok(mine, `${preset} @ ${nowStr}: the live page set a range, the port returned null`);
      assert.strictEqual(dtVal(mine.from), from.value, `${preset} @ ${nowStr}: from`);
      assert.strictEqual(dtVal(mine.to), to.value, `${preset} @ ${nowStr}: to`);
      compared++;
    }
  }
  assert.strictEqual(compared, NOWS.length * PRESETS.length);
});
