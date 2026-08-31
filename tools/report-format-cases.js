'use strict';
/**
 * `format.js`'s three REMAINING helpers, and the rounding underneath them.
 *
 * `tsFmt`, `fmtDuration` and `annotateDowntime` are already ported and pinned.
 * `maxOf`, `fmtDataMB` and `bucketNoun` are not, and every one of the five report
 * builders needs them — bandwidth alone uses `fmtDataMB` five times and
 * `bucketNoun` twice.
 *
 * All three are exported, so this requires the real module rather than lifting.
 *
 * ---- WHAT MAKES THEM WORTH A CORPUS ---------------------------------------
 *
 * `fmtDataMB` is four branches, three DIFFERENT toFixed precisions and a coercion:
 *
 *   const n = +mb || 0;
 *   if (n >= 1e6)  return (n / 1e6).toFixed(2) + ' TB';
 *   if (n >= 1000) return (n / 1000).toFixed(2) + ' GB';
 *   if (n >= 1)    return n.toFixed(1) + ' MB';
 *   return (n * 1000).toFixed(0) + ' KB';
 *
 * `+mb || 0` turns null, undefined, NaN, '' and 0 into 0 — but NOT a negative,
 * which falls past all three thresholds into the KB branch and comes out as
 * "-5000 KB". And `toFixed` rounds half AWAY FROM ZERO where Go's %.Nf rounds
 * half to EVEN, so 0.5 at zero digits is "1" here and "0" there. That one is not
 * hypothetical: a stored 0.0005 MB is exactly the input that produces it.
 *
 * `maxOf([])` is -Infinity, not zero and not an error. Its callers all guard
 * with `arr.length ? … : '—'`, so the value never reaches a page — but a port
 * that returned 0 would agree on every guarded call and disagree the moment
 * someone dropped the guard.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/report-format-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
// RESOLVED, not joined. `require` treats a path beginning '../' as relative to
// THIS module's directory, so a relative MIKRODASH_SRC would be looked up under
// tools/ and not found.
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const F = require(path.join(SRC, 'src', 'reports', 'format.js'));

// ---- fmtDataMB -----------------------------------------------------------
const MB_INPUTS = [
  // the KB branch, including the boundary and the rounding that lives there
  0, 0.0001, 0.0005, 0.0015, 0.0025, 0.05, 0.5, 0.9994, 0.9995, 0.99999,
  // the MB branch
  1, 1.04, 1.05, 1.25, 9.95, 12.34, 999.94, 999.95, 999.99,
  // the GB branch
  1000, 1004, 1005, 1234.5, 999994, 999995,
  // the TB branch
  1e6, 1234567, 1e6 + 5000,
  // NEGATIVES fall through every threshold into KB
  -1, -0.5, -1234, -1e6,
  // and the coercions `+mb || 0` performs
  null, undefined, NaN, '', '12.5', '1e3', 'abc', true, false, [], [7],
];
const fmtDataMB = MB_INPUTS.map((v) => ({
  in: v === undefined ? null : (typeof v === 'number' && !Number.isFinite(v) ? String(v) : v),
  inKind: v === undefined ? 'undefined' : (Number.isNaN(v) ? 'NaN' : typeof v),
  out: F.fmtDataMB(v),
}));

// ---- bucketNoun ----------------------------------------------------------
const bucketNoun = ['hour', 'day', 'week', 'month', 'minute', '', 'HOUR', 'hours', 'raw']
  .map((agg) => ({ in: agg, out: F.bucketNoun(agg) }))
  .concat([{ in: null, out: F.bucketNoun(null) }, { in: null, out: F.bucketNoun(undefined) }]);

// ---- maxOf ---------------------------------------------------------------
const MAX_INPUTS = [
  [], [1], [3, 1, 2], [-3, -1, -2], [0], [-0], [1.5, 1.5],
  [1e308, -1e308], [0.1 + 0.2, 0.3],
];
const maxOf = MAX_INPUTS.map((arr) => {
  const m = F.maxOf(arr);
  return { in: arr, out: Number.isFinite(m) ? m : String(m) };
});

// ---- toFixed, which is what fmtDataMB is really made of -------------------
//
// Pinned separately and at every precision fmtDataMB uses, because the Go port
// needs ONE exact implementation and this is the corpus that says what exact
// means. Values chosen so the binary representation lands exactly on a half,
// where the two languages' rounding rules disagree.
const FIXED = [];
for (const d of [0, 1, 2]) {
  for (const v of [0, 0.5, 1.5, 2.5, -0.5, -1.5, -2.5, 0.05, 0.15, 0.25, 0.35,
                   1.005, 1.015, 1.25, 1.35, 2.675, -1.25, -1.35,
                   9.995, 99.995, 0.0005, 1e21, 1e-7, 123456.789]) {
    FIXED.push({ v, d, out: v.toFixed(d) });
  }
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  const by = Object.fromEntries(fmtDataMB.map((c) => [JSON.stringify(c.in) + ':' + c.inKind, c.out]));
  assert.equal(by['0:number'], '0 KB', 'zero should render as KB');
  assert.equal(by['null:object'], '0 KB', 'null should coerce to 0');
  assert.equal(by['-1:number'], '-1000 KB',
    'a negative no longer falls through to the KB branch — the port must not clamp it');
  assert.ok(fmtDataMB.some((c) => c.out.endsWith(' TB')), 'no case reached the TB branch');
  assert.ok(fmtDataMB.some((c) => c.out.endsWith(' GB')), 'no case reached the GB branch');
  assert.ok(fmtDataMB.some((c) => c.out.endsWith(' MB')), 'no case reached the MB branch');

  // The rounding must actually differ from round-half-to-even SOMEWHERE, or the
  // Go side could use %.Nf and pass.
  assert.ok(FIXED.some((c) => c.d === 0 && c.v === 0.5 && c.out === '1'),
    '(0.5).toFixed(0) is no longer "1" — the half-away-from-zero rule has changed');
  assert.ok(FIXED.some((c) => c.d === 1 && c.v === 1.25 && c.out === '1.3'),
    '(1.25).toFixed(1) is no longer "1.3"');
  assert.ok(FIXED.some((c) => c.d === 1 && c.v === -1.25 && c.out === '-1.3'),
    'negatives no longer round away from zero');

  assert.equal(maxOf[0].out, '-Infinity', 'maxOf([]) is no longer -Infinity');
  assert.notEqual(bucketNoun.find((c) => c.in === 'hour').out,
    bucketNoun.find((c) => c.in === '').out, 'bucketNoun is not distinguishing its cases');
}

const OUT = path.join(ROOT, 'testdata', 'report-format-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/report-format-cases.js from the live src/reports/format.js. Do not edit.',
  fmtDataMB, bucketNoun, maxOf, toFixed: FIXED,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('report-format-cases: testdata/report-format-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('report-format-cases: up to date');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('report-format-cases: wrote ' + fmtDataMB.length + ' fmtDataMB, '
    + bucketNoun.length + ' bucketNoun, ' + maxOf.length + ' maxOf, ' + FIXED.length + ' toFixed cases');
}
