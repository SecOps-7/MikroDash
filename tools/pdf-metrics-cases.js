'use strict';
/**
 * HELVETICA STRING WIDTHS, measured by the LIVE pdfkit.
 *
 * ---- WHY THIS IS THE FIRST THING THE PDF PORT NEEDS -----------------------
 *
 * `src/reports/pdf.js` places almost nothing at a literal x. It centres the
 * title in a box, right-aligns the date range and every y-axis label, centres
 * the value and the caption inside each stat box, centres five x-axis ticks on
 * computed positions, and walks the legend along by
 * `doc.widthOfString(line.label)`.
 *
 * Every one of those positions is a function of how wide the font renderer
 * thinks a string is. So a PDF library whose Helvetica metrics differ from
 * pdfkit's by even a fraction produces a document that is *correct* and does
 * not *match* -- and item 6's gate compares against pdfkit's bytes.
 *
 * That makes metric agreement a property of the DEPENDENCY, not of the port,
 * and it is worth knowing before the renderer is written rather than after.
 * This corpus is what settles it: pdfkit measures, `pdf_metrics_test.go`
 * checks `fpdf.GetStringWidth` against the answers.
 *
 * ---- WHAT RUNNING IT ACTUALLY FOUND ---------------------------------------
 *
 * Both sides claim the Adobe standard-14 AFM widths, and on the WIDTH TABLE they
 * agree exactly -- `GetStringWidth("AV")` and pdfkit's sum-of-parts are both
 * 14.6740 at size 11. That was the thing worth checking and it passed.
 *
 * But pdfkit also KERNS, and `go-pdf/fpdf` cannot. pdfkit emits
 *
 *     [<41> 70 <5654> 120 <6f> 0] TJ        <- "AVTo", A|V pulled 70/1000 em in
 *
 * where fpdf emits `(AVTo) Tj`. There is no kerning anywhere in fpdf (v0.9.0)
 * and no public escape hatch to the content stream, so this is a capability it
 * does not have rather than a call the port is failing to make.
 *
 * SO EVERY CASE HERE CARRIES BOTH NUMBERS: `width` is what pdfkit measures, and
 * `unkerned` is the sum of per-character widths. The Go side must match
 * `unkerned` EXACTLY -- that is the width table, and a mismatch there means the
 * dependency is wrong. The difference between the two is the KNOWN GAP, and the
 * Go test asserts it is still non-zero somewhere, so that a future fpdf learning
 * to kern fails the suite and forces this note to be deleted rather than left
 * lying. That is the `KNOWN_INCOMPLETE` rule applied to a dependency.
 *
 * ---- HOW BIG THE GAP IS, MEASURED -----------------------------------------
 *
 * Text runs render up to ~1.4pt wider unkerned ("Traffic by Interface" at size
 * 8). Because `_render` mostly centres, that halves: the worst POSITION error
 * across every measured element of a real report is 0.65pt on a centred title,
 * which is 0.23 mm. Every right-aligned and advance-driven element measured
 * exactly zero error, because the strings involved -- timestamps, "1.5k",
 * "ether1" -- contain no kern pairs at all.
 *
 * That is the number the operator's "add a PDF dependency" decision should be
 * read against, and it is why this port keeps fpdf rather than hand-writing a
 * content stream to chase 0.23 mm.
 *
 * ---- THE CORPUS ----------------------------------------------------------
 *
 * The strings are the ones the report actually emits -- column headings, stat
 * captions, formatted byte counts, axis labels, timestamps -- at the six sizes
 * `_render` uses and in both faces. Plus the cases that break a naive width
 * table: the empty string, a lone space, RUNS of spaces (trailing space width
 * is where measurement implementations most often disagree), the punctuation
 * the report's own separator uses, and a non-ASCII character, because a
 * router's identity or a bridge's comment can carry one and the standard-14
 * encoding is WinAnsi rather than ASCII.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/pdf-metrics-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const SRC = process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash');
const PDFDocument = require(path.join(SRC, 'node_modules', 'pdfkit'));

/** The faces `_render` selects, and every size it sets. */
const FONTS = ['Helvetica', 'Helvetica-Bold'];
const SIZES = [17, 13, 11, 8, 7.5, 7];

const STRINGS = [
  // --- what the report puts on a page ---
  'Mikro', 'Dash', 'MikroDash',
  'Bandwidth Report', 'Ping Report', 'Uptime Report', 'Traffic by Interface',
  'Router: Mikrotik hAP AX3',
  '2026-08-25 14:03  →  2026-08-26 14:03',
  'Interface', 'Rx', 'Tx', 'Total', 'Avg', 'Peak', 'Time', 'Status', 'Loss %',
  'ether1', 'bridge-lan', 'wlan2-5GHz', 'sfp-sfpplus1',
  '1.2 GB', '834.7 MB', '0 B', '12.34 Mbps', '99.98%', '—',
  'Mbps', '1.5k', '0.0', '100.0',
  '14:03', '08-25 14:03', '08-25',
  // --- the cases that break a naive implementation ---
  '',                       // no characters at all
  ' ',                      // one space: is it measured, or trimmed?
  '   ',                    // a RUN of spaces -- must scale linearly
  'a ',                     // TRAILING space: the classic disagreement
  ' a',                     // leading space
  'a  b',                   // interior run
  'W'.repeat(40),           // widest glyph, repeated: catches per-char rounding
  'iiiiiiiiiiiiiiiiiiii',   // narrowest glyph, same reason
  'AV', 'To', 'Yo', 'F.',   // pairs a KERNING implementation would shrink
  '()[]{}<>/\\|', '.,;:!?', '"\'`', '#$%&*+-=@^_~',
  '0123456789',
  'ABCDEFGHIJKLMNOPQRSTUVWXYZ',
  'abcdefghijklmnopqrstuvwxyz',
  'Café',              // non-ASCII: cp1252, not ASCII
  '°C', 'µs', '£', '©',
  '€', '\u2019', '\u2014',   // the cp1252 0x80-0x9F block: euro, curly quote, em dash
  // OUTSIDE cp1252 entirely. pdfkit measures these ZERO and draws no glyph, so
  // the port must DROP them rather than substitute -- `→` is not academic, it is
  // the separator `_render` puts in every report's date range.
  '\u2192', '\u2603', '\u4e2d',
  'a\u2192b',            // and a dropped rune must not disturb its neighbours
];

const doc = new PDFDocument({ margin: 40, size: 'A4' });
const measure = (font, size, s) => { doc.font(font).fontSize(size); return doc.widthOfString(s); };

/** Sum of per-character widths: what a plain width TABLE yields, unkerned. */
const unkerned = (font, size, s) => {
  let t = 0;
  for (const ch of s) t += measure(font, size, ch);
  return t;
};

const cases = [];
for (const font of FONTS) {
  for (const size of SIZES) {
    for (const s of STRINGS) {
      cases.push({ font, size, s, width: measure(font, size, s), unkerned: unkerned(font, size, s) });
    }
  }
}

// ---- BELIEVABILITY -------------------------------------------------------
//
// A `widthOfString` that returned 0 for everything would produce a corpus the
// Go side could match trivially, and the gate would be a comparison of two
// zeroes. So the LIVE measurements alone must discriminate.
//
// It MEASURES rather than looking the answers up in `cases`: the atoms a
// discrimination check needs -- a single 'W', a size that is half another one --
// are not strings the report emits, and padding the corpus with them to satisfy
// its own believability check would make the corpus describe this file instead
// of the report.
{
  const w = measure;
  assert.equal(w('Helvetica', 11, ''), 0, 'the empty string must measure zero');
  assert.ok(w('Helvetica', 11, 'W') > w('Helvetica', 11, 'i'),
    'pdfkit did not distinguish a wide glyph from a narrow one -- these are not real metrics');
  assert.ok(Math.abs(w('Helvetica', 17, 'MikroDash') - w('Helvetica', 8.5, 'MikroDash') * 2) < 1e-9,
    'width did not scale linearly with font size');
  assert.notEqual(w('Helvetica', 11, 'MikroDash'), w('Helvetica-Bold', 11, 'MikroDash'),
    'bold measured the same as regular -- the face is being ignored');
  assert.ok(w('Helvetica', 11, ' ') > 0, 'a space measured zero');
  assert.ok(Math.abs(w('Helvetica', 11, '   ') - w('Helvetica', 11, ' ') * 3) < 1e-9,
    'a run of spaces did not scale -- spaces are being collapsed');
  assert.ok(w('Helvetica', 11, 'a ') > w('Helvetica', 11, 'a'),
    'a TRAILING space was dropped from the measurement');
  // pdfkit KERNS, and the whole point of the `unkerned` column is that gap. So
  // the gap must be REAL: if this ever stops being true the column is measuring
  // nothing and `metrics_test.go`'s known-gap assertion has quietly become a
  // tautology.
  //
  // Written the other way round to begin with, on the assumption that a width
  // table was the whole story. Running it said otherwise, which is the entire
  // reason the check exists.
  assert.ok(w('Helvetica', 11, 'AV')
    < w('Helvetica', 11, 'A') + w('Helvetica', 11, 'V') - 0.5,
    'pdfkit no longer kerns A|V -- the `unkerned` column now pins nothing');
  assert.ok(cases.some((c) => Math.abs(c.width - c.unkerned) > 1e-9),
    'not one corpus string is kerned -- the corpus cannot demonstrate the gap it claims');
  assert.ok(cases.some((c) => Math.abs(c.width - c.unkerned) < 1e-9),
    'every corpus string is kerned -- nothing here proves the two agree when there is no pair');
  assert.ok(w('Helvetica', 11, 'Café') > w('Helvetica', 11, 'Caf'),
    'a non-ASCII character measured as nothing');
  // The drop rule the port implements is only faithful if pdfkit really does
  // give an unmappable rune no width at all. Asserted here so the rule is pinned
  // by a measurement rather than by the comment that explains it.
  assert.equal(w('Helvetica', 11, '\u2192'), 0,
    'pdfkit now gives the arrow a width -- EncodeText must stop dropping it');
  assert.equal(w('Helvetica', 11, 'a\u2192b'), w('Helvetica', 11, 'ab'),
    'an unmappable rune changed its neighbours\' advance');
  assert.ok(w('Helvetica', 11, '\u2014') > 0, 'the em dash IS in cp1252 and must keep its width');
}

doc.end();

const OUT = path.join(ROOT, 'testdata', 'pdf-metrics-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/pdf-metrics-cases.js from the live pdfkit. Do not edit.',
  cases,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('pdf-metrics-cases: testdata/pdf-metrics-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('pdf-metrics-cases: up to date (' + cases.length + ' measurements)');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('pdf-metrics-cases: wrote ' + cases.length + ' measurements ('
    + FONTS.length + ' faces × ' + SIZES.length + ' sizes × ' + STRINGS.length + ' strings)');
}
