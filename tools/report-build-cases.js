'use strict';
/**
 * `build.js`'s TWO SHARED HELPERS, run rather than read.
 *
 * `_thin` and `_capRows` sit under all five report builders: every PDF's chart
 * series goes through the first and every PDF's table through the second. They
 * are pure, they are small, and they are the half of `src/reports/build.js` that
 * needs no database — so they come first, and the builders that call them can be
 * ported against a corpus that already pins their foundations.
 *
 * Neither is exported, so both are LIFTED. The constants they close over
 * (`CHART_POINTS`, `MAX_PDF_ROWS`) are lifted with them rather than retyped: a
 * corpus that hard-coded 150 and 5000 would keep passing after the live side
 * changed either, which is the failure mode extraction exists to prevent.
 *
 * ---- WHAT IS ACTUALLY DIFFICULT HERE --------------------------------------
 *
 * `_capRows`'s note row reads
 *
 *   '… showing the first ' + MAX_PDF_ROWS.toLocaleString() + ' of ' +
 *   rows.length.toLocaleString() + ' rows — …'
 *
 * and `toLocaleString()` with no argument uses the RUNTIME's default locale. In
 * the app container that is en-US, so 5000 becomes "5,000" and 43200 becomes
 * "43,200". Go's strconv writes "5000". A port that formatted the number the
 * obvious way would print a note no live report has ever printed, in a row that
 * only appears on the largest exports — the ones nobody re-reads.
 *
 * The locale is recorded WITH the corpus, because it is a property of where the
 * live app runs rather than of the code, and a container whose locale changed
 * would make these expectations wrong rather than the port.
 *
 *   docker exec -e MIKRODASH_SRC=/app mikrodash \
 *     node /work/tools/report-build-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const L = require('./lib/lift.js');

const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT, path.join('src', 'reports', 'build.js'));

// The constants come from the source, not from this file.
const ctx = { Math, Number, String, Object, Array, JSON };
vm.createContext(ctx);
vm.runInContext([
  L.line(src, 'const MAX_PDF_ROWS'),
  L.line(src, 'const CHART_POINTS'),
  L.whole(src, 'function _thin('),
  L.whole(src, 'function _capRows('),
  // `const` at the top level of a vm script is SCRIPT-scoped and never becomes a
  // property of the context, so the two constants lift and then vanish. The
  // functions survive because a function declaration does bind globally. Exported
  // by hand rather than by rewriting `const` to `var`, which would mean editing
  // lifted code -- the one thing lifting exists to avoid.
  'this.MAX_PDF_ROWS = MAX_PDF_ROWS; this.CHART_POINTS = CHART_POINTS;',
].join('\n'), ctx);

const { MAX_PDF_ROWS, CHART_POINTS, _thin, _capRows } = ctx;
assert.equal(typeof _thin, 'function', '_thin did not lift');
assert.equal(typeof _capRows, 'function', '_capRows did not lift');
assert.ok(MAX_PDF_ROWS > 0 && CHART_POINTS > 0, 'the constants did not lift');

// ---- _thin ---------------------------------------------------------------
//
// Lengths chosen around the boundary, because `step` is `ceil(len/CHART_POINTS)`
// and every interesting behaviour is at a multiple or just off one.
const seq = (n) => Array.from({ length: n }, (_, i) => i);
const THIN_LENGTHS = [
  0, 1, 2,
  CHART_POINTS - 1, CHART_POINTS, CHART_POINTS + 1,   // the `> CHART_POINTS` test
  2 * CHART_POINTS - 1, 2 * CHART_POINTS, 2 * CHART_POINTS + 1, // step 2 -> 3
  3 * CHART_POINTS, 43200,
];
const thin = THIN_LENGTHS.map((n) => ({ n, kept: _thin(seq(n)) }));

// ---- _capRows ------------------------------------------------------------
const COLS4 = ['Timestamp', 'Target', 'RTT (ms)', 'Loss (%)'];
const COLS1 = ['Only'];
const mkRows = (n, cols) => Array.from({ length: n }, (_, i) =>
  Object.fromEntries(cols.map((c, j) => [c, c + '-' + i + '-' + j])));

const CAP_CASES = [
  { name: 'well under the cap', n: 3, cols: COLS4 },
  { name: 'empty', n: 0, cols: COLS4 },
  { name: 'exactly the cap', n: MAX_PDF_ROWS, cols: COLS4 },
  { name: 'one over the cap', n: MAX_PDF_ROWS + 1, cols: COLS4 },
  { name: 'far over the cap', n: 43200, cols: COLS4 },
  { name: 'one column', n: MAX_PDF_ROWS + 5, cols: COLS1 },
];
const cap = CAP_CASES.map((c) => {
  const out = _capRows(mkRows(c.n, c.cols), c.cols);
  return {
    name: c.name, n: c.n, columns: c.cols,
    truncated: out.truncated,
    length: out.rows.length,
    // Only the LAST row can differ from its input, so that is what is recorded;
    // carrying 43,200 rows of synthetic data would make the corpus a megabyte of
    // nothing.
    last: out.rows.length ? out.rows[out.rows.length - 1] : null,
    first: out.rows.length ? out.rows[0] : null,
  };
});

// ---- BELIEVABILITY -------------------------------------------------------
//
// Both helpers can return their input unchanged, and a Go port that did nothing
// at all would match every case where they do. So the corpus must contain cases
// where they DEMONSTRABLY act.
{
  const byN = Object.fromEntries(thin.map((t) => [t.n, t]));
  assert.deepEqual(byN[CHART_POINTS].kept, seq(CHART_POINTS),
    'at exactly CHART_POINTS nothing should be dropped');
  assert.ok(byN[CHART_POINTS + 1].kept.length < CHART_POINTS + 1,
    'one over CHART_POINTS dropped nothing — _thin is inert');
  assert.ok(byN[43200].kept.length <= CHART_POINTS,
    'thinning 43,200 rows left more than CHART_POINTS points');
  assert.deepEqual(byN[43200].kept.slice(0, 3), [0, 288, 576],
    'the thinned points are not every step-th row');

  const byName = Object.fromEntries(cap.map((c) => [c.name, c]));
  assert.equal(byName['exactly the cap'].truncated, false,
    'a report of exactly MAX_PDF_ROWS was truncated — the boundary is off by one');
  assert.equal(byName['one over the cap'].truncated, true,
    'one row over the cap was not truncated');
  assert.equal(byName['one over the cap'].length, MAX_PDF_ROWS + 1,
    'a truncated report should be the cap plus one NOTE row');

  // The note must carry BOTH separated numbers, or the locale trap is not in the
  // corpus and the Go side can pass while writing "5000".
  const note = byName['far over the cap'].last[COLS4[0]];
  assert.ok(note.includes('5,000') && note.includes('43,200'),
    'the note row does not contain group-separated numbers: ' + JSON.stringify(note));
  for (const c of COLS4.slice(1)) {
    assert.equal(byName['far over the cap'].last[c], '',
      'the note row put text in a column other than the first');
  }
}

const locale = Intl.DateTimeFormat().resolvedOptions().locale;
const OUT = path.join(ROOT, 'testdata', 'report-build-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/report-build-cases.js from the live src/reports/build.js. Do not edit.',
  locale, maxPdfRows: MAX_PDF_ROWS, chartPoints: CHART_POINTS,
  thin, cap,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('report-build-cases: testdata/report-build-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('report-build-cases: up to date');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('report-build-cases: wrote ' + thin.length + ' thinning cases and '
    + cap.length + ' capping cases (locale ' + locale + ', cap ' + MAX_PDF_ROWS + ')');
}
