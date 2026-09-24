/**
 * What the Bandwidth tab calls its volume peak.
 *
 * ── THE LABEL CAME FROM THE WRONG PLACE, AND HAD FOR LONGER THAN #59 ────────
 *
 * "Busiest Minute" is the stat card under the volume chart. Its number is
 * `MAX(rx_mb)` taken over the rows the server read, UNGROUPED - so the
 * aggregation dropdown never affected it, while the noun came from exactly
 * that dropdown. Measured on a real range: the card read 2,684.9 MB labelled
 * "Busiest Day" while the busiest day in the chart beside it was 36,544.7 MB.
 * The number was the busiest MINUTE the whole time.
 *
 * #59 then added a second way to be wrong. Beyond the raw window the rows are
 * hourly rollups, so the peak became the busiest HOUR while the card still
 * said "Minute" - a number with the wrong unit rather than one that is coarse.
 *
 * So the noun now comes from `summary.resolution`, which the server sets from
 * the same predicate that chose the table (`db.Resolution`). This checks the
 * rendered card, not the helper, because the helper being right while the card
 * passes it the aggregation is precisely the bug that shipped.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

function makeEl(id) {
  const node = {
    id,
    innerHTML: '',
    textContent: '',
    style: {},
    value: '',
    disabled: false,
    addEventListener: () => {},
    setAttribute: () => {},
    getAttribute: () => null,
    // The sort helpers walk the table they just wrote; an empty result is
    // correct for a shim that never parses innerHTML back into nodes.
    querySelectorAll: () => [],
    querySelector: () => null,
    appendChild: () => {},
    insertAdjacentHTML: () => {},
    classList: { add: () => {}, remove: () => {}, contains: () => false, toggle: () => {} },
  };
  return node;
}

const IDS = [
  'rptBwStats', 'rptBwTruncHint', 'rptBwTbody', 'rptBwPager', 'rptBwPageInfo',
  'rptBwPrev', 'rptBwNext', 'rptBwThead', 'rptBandwidthChart',
];

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-peak-noun.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'reports-traffic.ts'),
    '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function mount() {
  const els = {};
  IDS.forEach((id) => { els[id] = makeEl(id); });
  global.document = {
    getElementById: (id) => els[id] || null,
    querySelectorAll: () => [],
    querySelector: () => null,
    addEventListener: () => {},
    createElement: () => makeEl(''),
  };
  global.window = {};
  // No Chart.js in the shim, so renderBandwidthChart returns early. That is
  // fine here: the card is what this file is about, and the chart's own dashed
  // label is checked from the source at the end, where it is reachable.
  delete require.cache[require.resolve(OUT)];
  return { els, mod: require(OUT) };
}

let failed = 0;
function check(name, fn) {
  try {
    fn();
    say('  ok  ' + name);
  } catch (e) {
    failed += 1;
    say('  FAIL ' + name + '\n       ' + (e && e.message));
  }
}

const ROWS = [{ ts: 1790000000000, interface: 'ether1', rx_mb: 10, tx_mb: 5 }];

/**
 * EVERY noun on the card, not any of them.
 *
 * An earlier version of this file asserted only that "Busiest Minute" appeared
 * somewhere. A mutation that renamed just the DOWNLOAD card survived it, because
 * the upload card still said Minute - the card pair disagreed with itself and
 * the test was satisfied. Both labels are read, and both must be the same unit.
 */
function nouns(html) {
  return (html.match(/Busiest (\w+)/g) || []).map((m) => m.replace('Busiest ', ''));
}

check('a minute-resolution range says Minute, on both cards', () => {
  const m = mount();
  m.mod.renderBandwidth(ROWS, { rxMaxMb: 4, txMaxMb: 2, resolution: 'minute' }, '');
  assert.deepEqual(nouns(m.els.rptBwStats.innerHTML), ['Minute', 'Minute'],
    'the cards read ' + m.els.rptBwStats.innerHTML);
});

check('an hour-resolution range says Hour, on both cards', () => {
  const m = mount();
  m.mod.renderBandwidth(ROWS, { rxMaxMb: 6, txMaxMb: 3, resolution: 'hour' }, '');
  assert.deepEqual(nouns(m.els.rptBwStats.innerHTML), ['Hour', 'Hour'],
    'beyond the raw window the peak is an hour; the cards read '
    + m.els.rptBwStats.innerHTML);
});

// THE REGRESSION THAT PREDATES #59: aggregating must not rename the peak,
// because aggregating does not change what the peak is measured over.
check('no aggregation renames the peak', () => {
  for (const agg of ['hour', 'day', 'week', 'month']) {
    const m = mount();
    m.mod.renderBandwidth(ROWS, { rxMaxMb: 4, txMaxMb: 2, resolution: 'minute' }, agg);
    assert.deepEqual(nouns(m.els.rptBwStats.innerHTML), ['Minute', 'Minute'],
      'aggregate=' + agg + ' renamed a MINUTE peak: ' + m.els.rptBwStats.innerHTML);
  }
});

// A server that has not been updated sends no `resolution`. The stored
// granularity it would have had is a minute, so that is the fallback - and it
// must not read as "undefined" or blank on the card.
check('a missing resolution falls back to Minute', () => {
  const m = mount();
  m.mod.renderBandwidth(ROWS, { rxMaxMb: 4, txMaxMb: 2 }, '');
  assert.deepEqual(nouns(m.els.rptBwStats.innerHTML), ['Minute', 'Minute'],
    'no resolution should read as a minute: ' + m.els.rptBwStats.innerHTML);
});

// AND THE CHART'S DASHED OVERLAY IS NAMED THE SAME WAY. It draws `rx_max_mb`,
// which is the same MAX one level down, so a hardcoded "minute" there would
// contradict the card two inches above it.
check('the chart overlay label is not hardcoded to a minute', () => {
  const src = fs.readFileSync(
    path.join(ROOT, 'web', 'src', 'pages', 'reports-charts.ts'), 'utf8');
  assert.ok(!/label: 'Busiest minute/.test(src),
    'reports-charts.ts still hardcodes "Busiest minute" on the overlay');
  assert.ok(/peakNoun\(resolution\)/.test(src),
    'the overlay label does not come from the resolution');
});

if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
say('\nall passed');
