'use strict';
/**
 * THE SCAN TABLE: what a stream of freeze-frames accumulates into.
 *
 * A frequency scan does not send rows once. RouterOS re-sends the WHOLE table
 * every freeze-frame interval, so the runner keys on channel and lets the latest
 * value win — the live comment: "Each freeze-frame re-sends the whole table, so
 * without keying on channel the graph would grow a duplicate bar every second."
 *
 * ---- THE BOUND IS NOT A SIMPLE CAP ----------------------------------------
 *
 *   if (!e.rows.has(row.ch) && e.rows.size >= MAX_CHANNELS) { truncated = true; continue; }
 *
 * Once the table is full, a NEW channel is dropped and the scan is marked
 * truncated — but a channel ALREADY IN the table keeps updating. That matters
 * because the bound exists to "bound memory against a device reporting
 * per-sample": a device doing that would otherwise fill the table and then
 * freeze it, so every one of the 200 channels an operator can see would stop
 * moving for the rest of the scan while the other rows were silently discarded.
 * A cap that stopped all updates would look identical for the first 200 rows and
 * be wrong for the rest of the scan.
 *
 * `sampleCount` counts EVERY row seen, including the dropped ones — it is how
 * many samples the radio produced, not how many survived.
 *
 * ---- AND THE ORDER --------------------------------------------------------
 *
 * `table()` sorts by channel number ascending. The insertion order is whatever
 * the radio swept in, which is not monotonic across bands.
 *
 *   docker exec -e MIKRODASH_SRC=/app mikrodash \\
 *     node /work/tools/wifiscan-accum-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const WifiScan = require(path.join(SRC, 'src', 'wifiScan.js'));
const MAX = WifiScan.MAX_CHANNELS;

/**
 * The live accumulate step, applied exactly as `onPacket` applies it.
 *
 * Lifted as behaviour rather than as text: the loop is four lines inside a
 * closure inside `openStream`, with no seam to call. What is reproduced here is
 * the SEQUENCE of operations, and every value in it comes from the live
 * `parseRow` and the live `MAX_CHANNELS`.
 */
function accumulate(raws) {
  const rows = new Map();
  let sampleCount = 0;
  let truncated = false;
  for (const raw of raws) {
    const row = WifiScan.parseRow(raw);
    if (!row) continue;
    sampleCount++;
    if (!rows.has(row.ch) && rows.size >= MAX) { truncated = true; continue; }
    rows.set(row.ch, row);
  }
  return {
    rows: Array.from(rows.values()).sort((a, b) => a.ch - b.ch),
    sampleCount, truncated,
  };
}

/** A raw RouterOS row as the scan command sends them. */
const raw = (freq, over = {}) => ({
  channel: String(freq) + '/20-Ce', networks: '3', load: '12', nf: '-98',
  'max-signal': '-55', 'min-signal': '-90', ...over,
});

const CASES = {
  'one frame': [raw(2412), raw(2437), raw(2462)],
  'nothing at all': [],
  // Every row unparseable: the count must stay at zero, not at three.
  'rows that do not parse': [{}, { channel: '' }, { channel: 'nonsense' }],
  // Two frames of the same three channels: the table stays three long and the
  // second frame's values win.
  'two frames, latest wins': [
    raw(2412, { load: '10' }), raw(2437, { load: '20' }),
    raw(2412, { load: '77' }), raw(2437, { load: '88' }),
  ],
  // Swept out of order across bands, which is what a real dual-band sweep does.
  'out of order across bands': [raw(5180), raw(2412), raw(5745), raw(2437), raw(5240)],

  // ---- the bound ----
  'exactly at the cap': Array.from({ length: MAX }, (_, i) => raw(2412 + i * 5)),
  'one channel over the cap': Array.from({ length: MAX + 1 }, (_, i) => raw(2412 + i * 5)),
  'far over the cap': Array.from({ length: MAX + 50 }, (_, i) => raw(2412 + i * 5)),
  // THE CASE THAT SEPARATES THE TWO POSSIBLE BOUNDS: fill the table, then re-send
  // an EXISTING channel with a new value. A cap that froze the table would keep
  // the old value; the live one updates it.
  'an existing channel updates after the cap': [
    ...Array.from({ length: MAX }, (_, i) => raw(2412 + i * 5, { load: '1' })),
    raw(2412 + 999 * 5),                 // a NEW channel — dropped
    raw(2412, { load: '99' }),           // an EXISTING one — must still update
  ],
};

const cases = Object.entries(CASES).map(([name, raws]) => {
  const out = accumulate(raws);
  return {
    name, sent: raws.length,
    sampleCount: out.sampleCount, truncated: out.truncated, kept: out.rows.length,
    // The ends only: a 250-row table would make this corpus mostly padding.
    first: out.rows[0] || null,
    last: out.rows[out.rows.length - 1] || null,
    channels: out.rows.length <= 12 ? out.rows.map((r) => r.ch) : null,
  };
});

// ---- BELIEVABILITY -------------------------------------------------------
{
  const by = Object.fromEntries(cases.map((c) => [c.name, c]));

  assert.equal(by['one frame'].kept, 3, 'a three-row frame did not produce three rows');
  assert.equal(by['rows that do not parse'].sampleCount, 0,
    'unparseable rows were counted as samples');

  // Latest wins, and the table does not grow.
  assert.equal(by['two frames, latest wins'].kept, 2,
    'a repeated frame grew the table — the rows are not keyed on channel');
  assert.equal(by['two frames, latest wins'].sampleCount, 4,
    'sampleCount counts surviving rows rather than rows SEEN');
  assert.equal(by['two frames, latest wins'].first.load, 77,
    'the first frame won — latest must win');

  // Sorted, not insertion-ordered.
  assert.deepEqual(by['out of order across bands'].channels,
    by['out of order across bands'].channels.slice().sort((a, b) => a - b),
    'the table is not sorted by channel');
  assert.notEqual(by['out of order across bands'].channels[0], 36,
    'this case no longer arrives out of order, so it proves nothing about sorting');

  // The bound, and which side of it is inclusive.
  assert.equal(by['exactly at the cap'].truncated, false,
    'a table of exactly MAX_CHANNELS was marked truncated');
  assert.equal(by['exactly at the cap'].kept, MAX, 'the full table lost a row');
  assert.equal(by['one channel over the cap'].truncated, true,
    'one channel past the cap was not marked truncated');
  assert.equal(by['one channel over the cap'].kept, MAX, 'the cap did not hold');
  assert.equal(by['far over the cap'].sampleCount, MAX + 50,
    'sampleCount stopped counting once the table was full — it counts what the radio produced');

  // And the case that matters most.
  const upd = by['an existing channel updates after the cap'];
  assert.equal(upd.truncated, true, 'the new channel past the cap was not recorded as truncation');
  assert.equal(upd.kept, MAX, 'the table grew past the cap');
  assert.equal(upd.first.load, 99,
    'an EXISTING channel stopped updating once the table was full — the bound is on INSERTION, '
    + 'not on updates, and a frozen table would leave every visible bar stuck for the rest of the scan');
}

const OUT = path.join(ROOT, 'testdata', 'wifiscan-accum-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/wifiscan-accum-cases.js from the live src/wifiScan.js. Do not edit.',
  maxChannels: MAX, cases,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('wifiscan-accum-cases: testdata/wifiscan-accum-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('wifiscan-accum-cases: up to date');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('wifiscan-accum-cases: wrote ' + cases.length + ' accumulation cases (cap ' + MAX + ')');
}
