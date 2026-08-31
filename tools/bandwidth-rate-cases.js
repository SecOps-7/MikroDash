#!/usr/bin/env node
'use strict';
/**
 * Pin the Bandwidth page's live RX/TX readout — `_splitRate`, LIFTED from
 * public/app.js rather than retyped.
 *
 * ── WHY A THREE-LINE FUNCTION GETS A GATE ───────────────────────────────────
 *
 * It splits a rate into a NUMBER and a UNIT, written into two separate elements,
 * and every boundary is a place where the two can disagree with the rest of the
 * page. The thresholds are `>= 1000` Gbps, `>= 1` Mbps, `>= 0.001` Kbps, and
 * below that an em dash with an EMPTY unit — so a link doing 0.0009 Mbps reads
 * "—" and not "0.9 Kbps", and a port that rounded instead of flooring would put
 * a unit next to a dash.
 *
 * The decimal places differ per unit (2, 2, 1) and that is not decoration: it is
 * what stops a Kbps figure claiming hundredths of a kilobit it never measured.
 *
 * `+mbps || 0` is the input coercion, and this corpus proves it is UNOBSERVABLE:
 * a port using a bare `Number(x)` passes every case, because NaN fails all three
 * `>=` comparisons and reaches the same dash branch as `0`. No input separates
 * them. Recorded here because "the corpus cannot distinguish these" is a fact
 * about the function, not a hole in the corpus — and because the first draft of
 * this header asserted the opposite until a mutation said otherwise.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/bandwidth-rate-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'bandwidth-rate-cases.json');
const CHECK = process.argv.includes('--check');

const app = fs.readFileSync(path.join(SRC, 'public', 'app.js'), 'utf8');
const START = 'function _splitRate(mbps) {';
const from = app.indexOf(START);
assert.ok(from > 0, '_splitRate has moved in app.js');
const END = '\n  }';
const to = app.indexOf(END, from);
assert.ok(to > from && to - from < 600, '_splitRate is not where its anchors say');
const block = app.slice(from, to + END.length);

for (const must of ['1000', 'Gbps', 'Mbps', 'Kbps', "num: '—'"]) {
  assert.ok(block.includes(must), 'the lifted function lost: ' + must);
}
// The slice must stop before the NEXT function, or the corpus would be built
// from something that also writes the DOM.
for (const mustNot of ['_updateBwStats', 'textContent', 'bwLiveRxNum']) {
  assert.ok(!block.includes(mustNot), 'the slice over-read and took in: ' + mustNot);
}

const splitRate = new Function(block + '\n return _splitRate;')();

const INPUTS = [
  // The three thresholds, and the value immediately below each.
  1000, 999.999, 1, 0.999, 0.001, 0.0009,
  // Ordinary traffic.
  0, 0.5, 12.5, 137.04, 2500, 10000,
  // Rounding at each unit, where the decimal places differ.
  1.005, 0.0015, 1234.567,
  // Coercion: everything here must land on the dash rather than render as-is.
  null, undefined, '', 'abc', NaN,
  // Coercible strings — `+x` takes them, so they are NOT the dash branch.
  '12.5', '0.0005', '1000',
  // Negatives, which no counter should produce but a delta can.
  -1, -0.5, -1000,
  // Extremes.
  1e9, 1e-9, Infinity, -Infinity,
];

const cases = INPUTS.map((v) => ({
  input: (typeof v === 'number' && !Number.isFinite(v)) ? String(v)
    : (v === undefined ? null : v),
  inputIsUndefined: v === undefined,
  ...splitRate(v),
}));

// BELIEVABILITY: all four branches must be reached, or the corpus cannot tell a
// working split from one that returns a constant.
const units = new Set(cases.map((c) => c.unit));
for (const u of ['Gbps', 'Mbps', 'Kbps', '']) {
  assert.ok(units.has(u), 'no case produces the ' + (u || 'empty') + ' unit');
}
const dashes = cases.filter((c) => c.num === '—').length;
assert.ok(dashes > 1 && dashes < cases.length, 'the dash branch is constant across the corpus');

const text = JSON.stringify({ generatedFrom: 'public/app.js _splitRate', cases }, null, 2) + '\n';
if (CHECK) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (cur !== text) { console.error('bandwidth-rate-cases.json is STALE — run: node tools/bandwidth-rate-cases.js'); process.exit(1); }
  console.log(`bandwidth-rate-cases.json up to date (${cases.length} cases, ${units.size} units)`);
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, text);
  console.log(`wrote ${cases.length} cases across ${units.size} units -> ${path.relative(process.cwd(), OUT)}`);
}
