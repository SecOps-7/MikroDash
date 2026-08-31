'use strict';
/**
 * WHAT `_render` DRAWS, recorded call by call from the LIVE renderer.
 *
 * ---- WHY NOT COMPARE THE PDFs ---------------------------------------------
 *
 * The obvious gate -- render both and diff the bytes -- cannot work and would
 * not be worth much if it did. pdfkit and fpdf lay out a PDF file differently
 * (object order, xref, stream compression, how a float is printed), so two
 * byte-identical PAGES produce two different FILES. A diff would be red forever
 * for reasons that have nothing to do with the drawing.
 *
 * So this records the drawing itself: every call `_render` makes, in order, with
 * its arguments. That is the layer where the two implementations must agree, and
 * it catches what a screenshot cannot -- a colour set and never used, a
 * lineWidth left dirty for the next shape, a save() without its restore().
 *
 * ---- IT WRAPS A REAL PDFDocument, IT DOES NOT FAKE ONE --------------------
 *
 * The recorder DELEGATES to a real pdfkit document rather than stubbing it. That
 * is not politeness: `_render`'s arithmetic READS BACK from the document --
 * `doc.page.width`, `doc.page.height` and `doc.widthOfString(label)` for the
 * legend advance -- so a stub returning plausible numbers would produce a
 * plausible op list that the real renderer never emits. Every recorded
 * coordinate here was computed from real metrics.
 *
 * ---- AND IT IS THE REAL MODULE --------------------------------------------
 *
 * `src/reports/pdf.js` exports `_render`, so this requires it instead of lifting
 * the function out of the file. Extraction cannot drift, but not extracting at
 * all cannot drift either, and it is one less thing to keep in step.
 *
 * ---- TZ IS PINNED ---------------------------------------------------------
 *
 * The chart's x-axis labels fall back to LOCAL time when no display timezone is
 * set, so the corpus would otherwise record the container's clock and the Go
 * test would have to run somewhere identical. Both sides are pinned to UTC and
 * the corpus says so, and the timezone-set path is covered by its own cases.
 *
 *   docker exec -e MIKRODASH_SRC=/app mikrodash \
 *     node /work/tools/pdf-render-cases.js [--check]
 */

// BEFORE anything constructs a Date. The chart's x labels fall back to LOCAL
// time when no display timezone is set, so an unpinned clock would bake the
// container's zone into the corpus and the Go test would have to run somewhere
// identical. Set here rather than demanded of the caller: `verify.sh` runs every
// container generator the same way, and the three that predate this one format
// their own timestamps -- forcing UTC on them from outside would make them stale
// for a reason that has nothing to do with this tool.
process.env.TZ = 'UTC';

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const SRC = process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash');

const PDFDocument = require(path.join(SRC, 'node_modules', 'pdfkit'));
const Settings = require(path.join(SRC, 'src', 'reports', '..', 'settings'));
const { _render } = require(path.join(SRC, 'src', 'reports', 'pdf'));

// Setting process.env.TZ is only meant to take effect if it is read before the
// first Date. Asserted rather than trusted: a runtime that ignored it would
// record local-time labels and the corpus would be quietly unportable.
assert.equal(new Date(Date.UTC(2026, 7, 25, 6, 0, 0)).getHours(), 6,
  'process.env.TZ = UTC did not take effect — this corpus would record local time');

/** Every call `_render` can make on the document, and nothing else. */
const RECORDED = [
  'font', 'fontSize', 'fillColor', 'strokeColor', 'lineWidth',
  'text', 'rect', 'roundedRect', 'moveTo', 'lineTo',
  'stroke', 'fill', 'save', 'restore', 'clip', 'addPage', 'end',
];

/** Round a float the way both sides can agree on, without hiding a real gap. */
const r6 = (v) => (typeof v === 'number' && Number.isFinite(v) ? Math.round(v * 1e6) / 1e6 : v);
const clean = (a) => (Array.isArray(a) ? a.map(clean)
  : a && typeof a === 'object' ? Object.fromEntries(Object.entries(a).map(([k, v]) => [k, clean(v)]))
  : r6(a));

function record(fn) {
  const doc = new PDFDocument({ margin: 40, size: 'A4' });
  doc.on('error', () => {});          // a throw must not leave an unhandled emitter
  const ops = [];
  const reads = [];
  const proxy = new Proxy(doc, {
    get(t, prop) {
      const v = t[prop];
      if (typeof v === 'function' && RECORDED.includes(prop)) {
        return (...args) => {
          ops.push({ op: prop, args: clean(args) });
          const out = v.apply(t, args);
          // pdfkit chains by returning the document; hand back the PROXY so the
          // rest of a chained expression is recorded too. `.rect().fill()` is
          // two ops, and a recorder that lost the second would be blind to
          // every fill in the file.
          return out === t ? proxy : out;
        };
      }
      // widthOfString is not a mark on the page, but it IS the one read whose
      // answer the port cannot reproduce: the legend advance uses pdfkit's
      // KERNED width and fpdf does not kern. Recording the question and the
      // answer lets the Go gate account for that difference exactly -- as a
      // derived correction it can then assert away to zero -- instead of
      // widening a tolerance until the comparison stops meaning anything.
      if (prop === 'widthOfString') {
        return (...args) => {
          const w = v.apply(t, args);
          // BOTH numbers. `width` is what the live legend advanced by; `unkerned`
          // is the sum of per-character widths, which is what fpdf will measure.
          //
          // Recording only the first would have made the Go gate unfalsifiable
          // here: it derives its kern correction from the PORT's measurement, so
          // a port that measured badly would produce a bad correction that
          // cancelled its own error exactly. Found by mutation -- dropping
          // EncodeText from the measurement survived. With `unkerned` recorded,
          // the measurement itself is checked rather than only its effect.
          let parts = 0;
          for (const ch of String(args[0])) parts += v.apply(t, [ch]);
          reads.push({ s: args[0], width: r6(w), unkerned: r6(parts) });
          return w;
        };
      }
      // `page` is read through: page.width and page.height are constants of the
      // A4 sheet, and the port takes them from the same place.
      return typeof v === 'function' ? v.bind(t) : v;
    },
  });
  fn(proxy);
  return { ops, reads };
}

/**
 * WHERE THE GLYPHS ACTUALLY LANDED, read out of a real PDF.
 *
 * The op list says what `_render` ASKED FOR; this says what pdfkit DID with it.
 * They are different questions, and the gap between them is the whole of
 * pdfkit's text engine: it converts the top of a line box into a baseline, and
 * it resolves `{ width, align }` into an x using its own measurement of the
 * string. A port that recorded the right calls and then placed the text by a
 * different rule would pass the op comparison and print a different page.
 *
 * Each entry also carries the KERNED and UNKERNED width of its string at the
 * size it was drawn, because that is exactly what an aligned x depends on: a
 * centred string starts half its own width in. So the Go side's expected x is a
 * derivation from these numbers, not a tolerance.
 */
// ASYNC, and it has to be: pdfkit emits its bytes on the event loop, so reading
// the chunk list straight after `fn(doc)` returns gives an EMPTY buffer and a
// silent zero positions. Caught by the believability check below, which is the
// only reason it is not still doing that.
async function positions(fn, ops) {
  const doc = new PDFDocument({ margin: 40, size: 'A4', compress: false });
  const chunks = [];
  const done = new Promise((resolve, reject) => {
    doc.on('data', (c) => chunks.push(c));
    doc.on('end', resolve);
    doc.on('error', reject);
  });
  fn(doc);
  await done;
  const body = Buffer.concat(chunks).toString('latin1');

  // pdfkit writes one `Tm` per positioned string.
  const out = [];
  const re = /1 0 0 1 ([\d.-]+) ([\d.-]+) Tm\s*\/F\d+ ([\d.]+) Tf/g;
  let m;
  while ((m = re.exec(body)) !== null) {
    out.push({ x: r6(+m[1]), y: r6(+m[2]), size: r6(+m[3]) });
  }

  // Pair them, in order, with the text ops -- that is where the STRING is, and
  // pdfkit does not put it in the stream in a form worth parsing back.
  //
  // NON-EMPTY ones. An empty cell -- `row[col] != null ? String(...) : ''` -- is
  // still a `text` call but pdfkit writes no text matrix for it, so pairing on
  // every call slides the whole list by one from the first null onward and
  // compares each string against the position of a later one. The full report
  // has two such cells, which is how this was found.
  const texts = ops.filter((o) => o.op === 'text' && String(o.args[0]) !== '');
  const meas = new PDFDocument({ margin: 40, size: 'A4' });
  let font = 'Helvetica';
  let i = -1;
  for (const o of ops) {
    if (o.op === 'font') font = o.args[0];
    if (o.op !== 'text') continue;
    const s = String(o.args[0]);
    if (s === '') continue;   // no text matrix was written for it
    i++;
    if (i >= out.length) break;
    meas.font(font).fontSize(out[i].size);
    let parts = 0;
    for (const ch of s) parts += meas.widthOfString(ch);
    out[i].s = s;
    out[i].font = font;
    out[i].kerned = r6(meas.widthOfString(s));
    out[i].unkerned = r6(parts);
  }
  return out.filter((p) => p.s !== undefined);
}

// ---- THE PAYLOADS --------------------------------------------------------
//
// Chosen for _render's BRANCHES, not for variety. Each names the branch it is
// the only case to reach.

const ts0 = Date.UTC(2026, 7, 25, 6, 0, 0);
const H = 3600000, D = 86400000;
const series = (n, step, f) => Array.from({ length: n }, (_, i) => ({ x: ts0 + i * step, y: f(i) }));

const COLS = ['Timestamp', 'Interface', 'RX (Mbps)', 'TX (Mbps)'];
const row = (i) => ({
  Timestamp: '2026-08-25 06:' + String(i % 60).padStart(2, '0'),
  Interface: i % 3 === 0 ? 'ether1' : i % 3 === 1 ? 'bridge-lan' : 'wlan2-5GHz',
  'RX (Mbps)': (i * 1.37).toFixed(2),
  'TX (Mbps)': i % 7 === 0 ? null : (i * 0.41).toFixed(2),
});

const STATS = [
  { label: 'Peak RX', value: '941.2 Mbps' },
  { label: 'Peak TX', value: '112.8 Mbps' },
  { label: 'Avg RX', value: '88.3 Mbps' },
  { label: 'Avg TX', value: '12.0 Mbps' },
];

const CHART = {
  yLabel: 'Mbps',
  lines: [
    { label: 'RX Mbps', color: '#38bdf8', pts: series(40, 15 * 60000, (i) => 20 + i * 3.5) },
    { label: 'TX Mbps', color: '#f472b6', pts: series(40, 15 * 60000, (i) => 5 + (i % 9) * 2.25) },
  ],
};

const CASES = {
  // The whole document, every section present.
  'full report': ['Traffic History Report', COLS,
    Array.from({ length: 12 }, (_, i) => row(i)), { router: 'Mikrotik hAP AX3', from: ts0, to: ts0 + 10 * H, stats: STATS, chartData: CHART }],

  // No meta at all: the header bar and the table, nothing between them.
  'no meta': ['Bare Report', COLS, [row(1), row(2)], null],

  // Router but no range, and range but no router: the meta row is one `if` with
  // two independent halves, and only these two tell them apart.
  'router only': ['Router Only', COLS, [row(1)], { router: 'cAP AX' }],
  'range only': ['Range Only', COLS, [row(1)], { from: ts0, to: ts0 + 2 * H }],

  // Stats without a chart, and a chart without stats.
  'stats no chart': ['Stats Only', COLS, [row(1)], { router: 'r', stats: STATS }],
  'chart no stats': ['Chart Only', COLS, [row(1)], { router: 'r', from: ts0, to: ts0 + 6 * H, chartData: CHART }],

  // One stat box and many: boxW is min(110, ...) so the two land differently.
  'one stat': ['One Stat', COLS, [row(1)], { router: 'r', stats: [STATS[0]] }],
  'eight stats': ['Eight Stats', COLS, [row(1)], {
    router: 'r', stats: Array.from({ length: 8 }, (_, i) => ({ label: 'Stat ' + i, value: String(i * 111) })) }],

  // ---- the chart's own branches ----
  // A line with ONE point is filtered out; if every line is, the whole chart is
  // skipped and `y` never advances. Two separate outcomes, two cases.
  'chart one point': ['Chart 1pt', COLS, [row(1)], { router: 'r', chartData: {
    yLabel: 'x', lines: [{ label: 'a', color: '#111111', pts: [{ x: ts0, y: 1 }] }] } }],
  'chart mixed lengths': ['Chart Mixed', COLS, [row(1)], { router: 'r', chartData: {
    yLabel: 'x', lines: [
      { label: 'kept', color: '#111111', pts: series(5, H, (i) => i) },
      { label: 'dropped', color: '#222222', pts: [{ x: ts0, y: 9 }] }] } }],
  // FLAT line: yMin === yMax, which _render rewrites to 0..yMax || 1.
  'chart flat': ['Chart Flat', COLS, [row(1)], { router: 'r', chartData: {
    yLabel: 'flat', lines: [{ label: 'f', color: '#333333', pts: series(6, H, () => 7) }] } }],
  // Flat AT ZERO, where `yMax || 1` takes the 1.
  'chart flat zero': ['Chart Zero', COLS, [row(1)], { router: 'r', chartData: {
    yLabel: 'zero', lines: [{ label: 'z', color: '#444444', pts: series(6, H, () => 0) }] } }],
  // yMin > 0 is clamped down to 0; negative y is NOT clamped.
  'chart positive floor': ['Chart Floor', COLS, [row(1)], { router: 'r', chartData: {
    yLabel: 'pos', lines: [{ label: 'p', color: '#555555', pts: series(6, H, (i) => 50 + i) }] } }],
  'chart negative': ['Chart Neg', COLS, [row(1)], { router: 'r', chartData: {
    yLabel: 'neg', lines: [{ label: 'n', color: '#666666', pts: series(6, H, (i) => i - 10) }] } }],
  // The x-label FORMAT switches on span: <=12h, <=3d, longer. Three cases, and
  // the label WIDTH changes with them too.
  'chart span hours': ['Span Hours', COLS, [row(1)], { router: 'r', chartData: {
    yLabel: 'h', lines: [{ label: 'h', color: '#777777', pts: series(8, H, (i) => i) }] } }],
  'chart span days': ['Span Days', COLS, [row(1)], { router: 'r', chartData: {
    yLabel: 'd', lines: [{ label: 'd', color: '#888888', pts: series(8, 8 * H, (i) => i) }] } }],
  'chart span weeks': ['Span Weeks', COLS, [row(1)], { router: 'r', chartData: {
    yLabel: 'w', lines: [{ label: 'w', color: '#999999', pts: series(8, 2 * D, (i) => i) }] } }],
  // The y label is `yv.toFixed(1)` below 1000 and `(yv/1000).toFixed(1)+'k'` at
  // or above it, and NO other case reaches the second branch. It is also where
  // JS rounding and Go's part company: 5000 puts a tick at 1250, and
  // (1.25).toFixed(1) is '1.3' -- half away from zero -- where Go's %.1f gives
  // '1.2', half to EVEN. A corpus without this case would let that through.
  'chart kilo': ['Chart Kilo', COLS, [row(1)], { router: 'r', chartData: {
    yLabel: 'bps', lines: [{ label: 'k', color: '#aaaaaa', pts: series(6, H, (i) => i * 1000) }] } }],
  // THE ONE PLACE the port cannot be exact. The legend advance is
  // `legX += 13 + doc.widthOfString(line.label) + 16`, and that width is KERNED
  // here and cannot be in fpdf. Every other label in this corpus happens to have
  // no kern pair, so without this case the Go gate's correction would never once
  // be applied and an allowance that never fires proves nothing.
  //
  // 'AVATo Wy' is chosen for pairs Adobe's Helvetica actually kerns; the SECOND
  // line's legend x is what inherits the difference.
  'chart kerned legend': ['Chart Kerned', COLS, [row(1)], { router: 'r', chartData: {
    yLabel: 'k', lines: [
      { label: 'AVATo Wy', color: '#bbbbbb', pts: series(5, H, (i) => i) },
      { label: 'Yo Ta.', color: '#cccccc', pts: series(5, H, (i) => i * 2) }] } }],
  // A NON-ASCII legend label. Nothing else in the corpus measures one, so a port
  // that forgot EncodeText before measuring -- and therefore counted UTF-8 bytes
  // -- advanced the legend by the wrong amount with nothing to say so.
  'chart non-ascii legend': ['Chart Accents', COLS, [row(1)], { router: 'r', chartData: {
    yLabel: 'µ', lines: [
      { label: 'Café µs', color: '#dddddd', pts: series(5, H, (i) => i) },
      { label: '£ ©ute', color: '#eeeeee', pts: series(5, H, (i) => i * 2) }] } }],
  // A line with no colour falls back to '#38bdf8' -- in the stroke AND in the
  // legend swatch, which are two separate `||` in the source.
  'chart no colour': ['Chart NoColour', COLS, [row(1)], { router: 'r', chartData: {
    yLabel: 'c', lines: [{ label: 'nc', pts: series(5, H, (i) => i) }] } }],

  // ---- the table's own branches ----
  // Enough rows to cross doc.page.height - 50 and force addPage + a repeated
  // header. The zebra index RESETS on a new page, which a single-page corpus
  // would never show.
  'paginated': ['Paginated Report', COLS, Array.from({ length: 90 }, (_, i) => row(i)),
    { router: 'r', from: ts0, to: ts0 + D }],
  // The SAME 90 rows, started lower down the page by a row of stat boxes, so the
  // first page holds an ODD number of them. The plain `paginated` case happens to
  // fit an even 58, and `rowIdx % 2` is therefore the same whether or not the
  // index is reset on the new page -- a mutation removing the reset survived
  // against it. Parity is the whole point of the reset, so a corpus that only
  // ever paginates on an even boundary cannot see it.
  'paginated odd': ['Paginated Odd', COLS, Array.from({ length: 90 }, (_, i) => row(i)),
    { router: 'r', from: ts0, to: ts0 + D, stats: STATS }],
  // A null cell renders '', a 0 renders '0' -- `row[col] != null` lets 0 through
  // and a truthiness test would not.
  'null and zero cells': ['Nulls', COLS, [
    { Timestamp: 'a', Interface: null, 'RX (Mbps)': 0, 'TX (Mbps)': undefined }], { router: 'r' }],
  // One column and many: colW is floor(inner/n).
  'one column': ['One Col', ['Only'], [{ Only: 'x' }], { router: 'r' }],
  'twelve columns': ['Twelve Col', Array.from({ length: 12 }, (_, i) => 'C' + i),
    [Object.fromEntries(Array.from({ length: 12 }, (_, i) => ['C' + i, 'v' + i]))], { router: 'r' }],
  // No rows at all: the header is drawn and the loop never runs.
  'no rows': ['Empty Report', COLS, [], { router: 'r', stats: STATS }],
  // Non-ASCII and an unmappable rune, which is what EncodeText exists for.
  'non-ascii cells': ['Café Report', ['Name', 'Note'], [
    { Name: 'Café', Note: 'a→b' }, { Name: 'µs', Note: '€ 12' }], { router: 'Café' }],
};

// The display timezone is read INSIDE _render, once per chart. Two of the cases
// are re-run with one set, because the label format changes completely.
// ALL THREE, because sv-SE does not format them the way the fallback does and
// the difference is not guessable: with a timeZone the <=3d label is `25/08
// 15:30` -- day/month, slashes -- against the fallback's `08-25 06:00`. The
// long-span format had to be measured for the same reason.
const TZ_CASES = ['chart span hours', 'chart span days', 'chart span weeks'];

/** Record the calls, then render again for real and read the glyph positions. */
async function withPositions(fn) {
  const r = record(fn);
  return { ...r, positions: await positions(fn, r.ops) };
}

const cases = [];
async function buildCases() {
  for (const [name, [title, columns, rows, meta]] of Object.entries(CASES)) {
    Settings.load = () => ({ displayTimezone: '' });
    cases.push({ name, tz: '', title, columns, rows: clean(rows), meta: clean(meta),
      ...(await withPositions((doc) => _render(doc, title, columns, rows, meta))) });
  }
  for (const name of TZ_CASES) {
    const [title, columns, rows, meta] = CASES[name];
    Settings.load = () => ({ displayTimezone: 'Australia/Adelaide' });
    cases.push({ name: name + ' (tz)', tz: 'Australia/Adelaide', title, columns, rows: clean(rows), meta: clean(meta),
      ...(await withPositions((doc) => _render(doc, title, columns, rows, meta))) });
  }
}

async function main() {
// ---- BELIEVABILITY -------------------------------------------------------
//
// A recorder whose proxy failed to intercept would produce empty op lists, and a
// Go renderer emitting nothing would match them perfectly. So the LIVE recording
// alone must show the document being drawn, and the branch cases must actually
// differ from each other.
{
  const byName = Object.fromEntries(cases.map((c) => [c.name, c]));
  const ops = (n) => byName[n].ops;
  const kinds = (n) => new Set(ops(n).map((o) => o.op));

  assert.ok(ops('full report').length > 100,
    'the full report recorded almost nothing — the proxy is not intercepting');
  for (const k of ['rect', 'text', 'font', 'fill', 'stroke', 'moveTo', 'lineTo', 'save', 'restore', 'clip', 'roundedRect'])
    assert.ok(kinds('full report').has(k), 'the full report never called ' + k);
  assert.ok(ops('full report').some((o) => o.op === 'end'), '_render never ended the document');

  // Chaining: `.rect().fill()` must record BOTH halves.
  assert.ok(ops('full report').some((o, i, a) => o.op === 'rect' && a[i + 1] && a[i + 1].op === 'fill'),
    'no rect is followed by a fill — the proxy is losing chained calls');

  // Each branch pair must actually diverge, or the case is not testing anything.
  const differs = (a, b) => assert.notDeepEqual(ops(a), ops(b), a + ' and ' + b + ' recorded the same drawing');
  differs('no meta', 'router only');
  differs('router only', 'range only');
  differs('one stat', 'eight stats');
  differs('chart flat', 'chart flat zero');
  differs('chart span hours', 'chart span days');
  differs('chart span days', 'chart span weeks');
  differs('chart span hours', 'chart span hours (tz)');
  differs('one column', 'twelve columns');

  // The filtered-out line must leave NO trace, and the mixed case must draw one.
  assert.ok(!kinds('chart one point').has('clip'),
    'a one-point line still drew a chart — the length filter is not being exercised');
  assert.ok(kinds('chart mixed lengths').has('clip'), 'the mixed-length chart drew no chart at all');

  // The two paginated cases must break on OPPOSITE parities, or the zebra
  // reset is still untested.
  {
    const firstPageRows = (n) => {
      const o = ops(n);
      const i = o.findIndex((x) => x.op === 'addPage');
      assert.ok(i > 0, n + ' never paginated');
      // One `fillColor('#334155')` per table row, and nowhere else in the
      // document. Counting 4-arg texts instead counts the stat boxes and the
      // meta line too, which is what made this check fail against a case that
      // was in fact correct.
      return o.slice(0, i).filter((x) => x.op === 'fillColor' && x.args[0] === '#334155').length;
    };
    const a = firstPageRows('paginated'), b = firstPageRows('paginated odd');
    assert.notEqual(a % 2, b % 2,
      'both paginated cases break on the same parity (' + a + ' and ' + b
      + ') — the zebra reset is untested');
  }

  // A non-ASCII legend label must measure DIFFERENTLY from its byte count, or it
  // does not exercise the encoder.
  assert.ok(byName['chart non-ascii legend'].reads.every((r) => /[^\x00-\x7F]/.test(r.s)),
    'the non-ascii legend case has an all-ASCII label');

  // The positions must have been read, and must line up with the text ops.
  {
    for (const c of cases) {
      const texts = c.ops.filter((o) => o.op === 'text' && String(o.args[0]) !== '').length;
      assert.equal(c.positions.length, texts,
        c.name + ': read ' + c.positions.length + ' glyph positions for ' + texts + ' non-empty text calls');
    }
    // A centred string must NOT start at the box's left edge, or alignment is
    // not being exercised and the port could ignore it.
    const t = byName['full report'].positions.find((p) => p.s === 'Traffic History Report');
    assert.ok(t && t.x > 40 + 1, 'the centred title starts at the left margin — align is inert');
    // And at least one string must be measurably kerned, since that is what the
    // Go side has to derive its offset from.
    assert.ok(cases.some((c) => c.positions.some((p) => p.unkerned - p.kerned > 0.1)),
      'no drawn string is kerned — the derived x offset would always be zero');
  }

  // Pagination must really paginate, and only there.
  assert.ok(ops('paginated').some((o) => o.op === 'addPage'), 'the 90-row report never added a page');
  assert.ok(!ops('full report').some((o) => o.op === 'addPage'), 'the 12-row report paginated — it should not');

  // Every chart case must have MEASURED its legend labels, or the recorded
  // reads are not the ones the coordinates were built from.
  assert.ok(byName['full report'].reads.length >= 2,
    'the two-line chart measured fewer than two legend labels');
  assert.deepEqual(byName['full report'].reads.map((r) => r.s), ['RX Mbps', 'TX Mbps'],
    'the recorded widthOfString reads are not the legend labels, in order');
  assert.equal(byName['no meta'].reads.length, 0, 'a chartless report measured a string');
  // The kerned-legend case only earns its place if its first label really is
  // narrower kerned than the sum of its parts -- otherwise the Go gate's
  // correction is still never applied.
  {
    const k = byName['chart kerned legend'];
    const d = new PDFDocument({ margin: 40, size: 'A4' });
    d.font('Helvetica').fontSize(7);
    const first = k.reads[0];
    let parts = 0;
    for (const ch of first.s) parts += d.widthOfString(ch);
    assert.ok(parts - first.width > 0.1,
      'the kerned-legend label is not measurably kerned (' + (parts - first.width).toFixed(4)
      + 'pt) -- the Go side would need no correction and the allowance stays untested');
    assert.equal(k.reads.length, 2, 'the kerned-legend case needs a SECOND label to shift');
  }

  // The k-suffix branch must really be reached, and the plain branch too.
  {
    const kilo = ops('chart kilo').map((o) => o.args[0]).filter((v) => typeof v === 'string');
    assert.ok(kilo.some((v) => /^\d+\.\dk$/.test(v)),
      'no y label took the >=1000 k-suffix branch — the case does not reach it');
    assert.ok(ops('full report').map((o) => o.args[0])
      .some((v) => typeof v === 'string' && /^\d+\.\d$/.test(v)),
      'no y label took the plain branch');
  }

  // The three timezone formats must all differ from their fallbacks AND from
  // each other, or one of them is not being exercised.
  differs('chart span weeks', 'chart span weeks (tz)');

  // The colour fallback must appear in both places it is written.
  const nc = JSON.stringify(ops('chart no colour'));
  assert.ok((nc.match(/#38bdf8/g) || []).length >= 2,
    'the missing-colour fallback appeared fewer than twice — one of the two `||` is not covered');
}

const OUT = path.join(ROOT, 'testdata', 'pdf-render-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/pdf-render-cases.js from the live src/reports/pdf.js. Do not edit.',
  tz: 'UTC',
  cases,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('pdf-render-cases: testdata/pdf-render-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('pdf-render-cases: up to date (' + cases.length + ' payloads)');
} else {
  fs.writeFileSync(OUT, payload);
  const total = cases.reduce((n, c) => n + c.ops.length, 0);
  console.log('pdf-render-cases: wrote ' + cases.length + ' payloads, ' + total + ' recorded calls');
}

}

buildCases().then(main).catch((e) => { console.error(e); process.exit(1); });
