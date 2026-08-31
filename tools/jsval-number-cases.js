'use strict';
/**
 * `Number(v)` and `Number.isFinite(...)`, run in the runtime that defines them.
 *
 * ── WHY THIS EXISTS SEPARATELY FROM ITS CALLERS ────────────────────────────
 *
 * `internal/jsval` had NO TEST FILES when `ToNumber` was added to it — the
 * package exists precisely so a JavaScript coercion is written once, and a
 * shared helper with no gate of its own is worse than a private copy: every
 * caller inherits the same mistake, and each of their corpora only covers the
 * shapes that caller happens to pass.
 *
 * ── AND IT IS NOT `parseInt` ───────────────────────────────────────────────
 *
 * `internal/store`'s `parseIntLike` is `parseInt`, which takes a LEADING number
 * and ignores the rest. `Number` does not. The settings VALIDATOR uses the first
 * and the poll RE-TUNE uses the second, so the pair `parseInt("25abc") === 25`
 * against `Number("25abc") === NaN` is in the corpus to keep them from being
 * merged into one helper.
 *
 * ── AND `undefined` IS NOT `null` ──────────────────────────────────────────
 *
 * `Number(undefined)` is NaN; `Number(null)` is 0. Go's `m[k]` yields the same
 * nil for both, which is how `collection.PollRetunes` came to clamp a
 * never-written interval to 500 instead of leaving the collector alone. Both are
 * here.
 *
 * Runs on the host. Nothing is required.
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const OUT = process.env.JSVAL_NUMBER_OUT
  || path.join(ROOT, 'testdata', 'jsval-number-cases.json');

// Each entry is [label, value]. The label is what the Go test switches on to
// rebuild the same value, because JSON cannot carry NaN or undefined.
const VALUES = [
  ['null', null],
  ['true', true],
  ['false', false],
  ['zero', 0],
  ['int', 4000],
  ['negative', -5],
  ['float', 5999.9],
  ['emptyString', ''],
  ['spaces', '   '],
  ['numericString', '4000'],
  ['paddedNumericString', '  4000  '],
  ['floatString', '12.5'],
  ['negativeString', '-7'],
  ['plusString', '+7'],
  ['expString', '1e3'],
  // `parseInt` says 25; `Number` says NaN. The whole reason the two helpers
  // stay separate.
  ['trailingGarbage', '25abc'],
  ['leadingGarbage', 'abc25'],
  ['word', 'soon'],
  ['hex', '0x10'],
  ['binary', '0b101'],
  ['octal', '0o17'],
  ['infinity', 'Infinity'],
  ['negInfinity', '-Infinity'],
  ['nanString', 'NaN'],
  ['object', {}],
  ['array', []],
  ['arrayOfOne', [5]],
  ['arrayOfTwo', [1, 2]],
];

const cases = {};
for (const [label, v] of VALUES) {
  const n = Number(v);
  cases[label] = {
    kind: v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v,
    value: typeof v === 'object' && v !== null ? undefined : v,
    finite: Number.isFinite(n),
    // A non-finite result has no number worth recording, and NaN is not JSON.
    number: Number.isFinite(n) ? n : null,
  };
}

// `undefined` cannot appear in a JSON object at all, so it is recorded by name.
{
  const n = Number(undefined);
  cases.undefined = { kind: 'undefined', finite: Number.isFinite(n), number: null };
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  const c = (k) => cases[k];

  // The pair that keeps parseInt and Number from being merged.
  assert.equal(c('trailingGarbage').finite, false,
    "Number('25abc') is finite — then it is parseInt, and the two helpers would merge");
  assert.equal(c('numericString').number, 4000);

  // undefined and null differ, which is the trap PollRetunes hit.
  assert.equal(c('undefined').finite, false, 'Number(undefined) is finite');
  assert.equal(c('null').finite, true, 'Number(null) is not finite');
  assert.equal(c('null').number, 0);

  // Empty and whitespace-only strings are ZERO, not NaN.
  assert.equal(c('emptyString').number, 0, "Number('') is not 0");
  assert.equal(c('spaces').number, 0, "Number('   ') is not 0");

  assert.equal(c('hex').number, 16);
  assert.equal(c('binary').number, 5);
  assert.equal(c('octal').number, 15);
  assert.equal(c('infinity').finite, false);
  assert.equal(c('nanString').finite, false);

  // Objects are NaN; the EMPTY array is 0 and a one-element array is its
  // element — both via ToPrimitive, and both recorded even though only the
  // first matters to this port.
  assert.equal(c('object').finite, false);
  assert.equal(c('array').number, 0, 'Number([]) is not 0');
  assert.equal(c('arrayOfOne').number, 5, 'Number([5]) is not 5');
  assert.equal(c('arrayOfTwo').finite, false);

  assert.equal(c('true').number, 1);
  assert.equal(c('false').number, 0);
  assert.equal(c('float').number, 5999.9, 'Number does not truncate — the CALLER does');
}

const json = JSON.stringify({ cases }, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('jsval-number-cases.json is STALE — re-run without --check');
    process.exit(1);
  }
  console.log('jsval-number-cases.json is current (' + Object.keys(cases).length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, json);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' cases)');
}
