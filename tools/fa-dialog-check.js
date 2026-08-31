'use strict';
/**
 * THE FREQUENCY ANALYSER'S DIALOG, live against ported.
 *
 * `wifiscan-*-cases.js` pin the SERVER: what may be scanned, what a scan
 * accumulates, when it stops. This pins what the operator actually SEES — the
 * stat boxes, the channel grid, the warning above the picker and the picker
 * itself — by driving the live renderers and the port's from ONE payload and
 * comparing the HTML.
 *
 * ---- WHY THE WARNING AND THE PICKER MATTER MOST ---------------------------
 *
 * Both are decision aids for a disruptive action, and both were built from a
 * measurement the live comments record:
 *
 *   the warning   "scanning a radio with clients on it dropped all 15 within 2
 *                 seconds, held them at zero for the full 30, and they took over
 *                 15 seconds to start returning. Scanning an idle radio dropped
 *                 nothing." So it says which of the two is about to happen,
 *                 rather than warning uniformly and being ignored.
 *   the picker    "measured on a live fleet, the two obvious-looking radios had
 *                 zero clients and the other two had every one of them." Without
 *                 the count beside each name the picker gives no clue which
 *                 radio is idle and which carries the network.
 *
 * A port that dropped either would still scan correctly and would make the
 * operator's decision worse, which is precisely the kind of regression a
 * screenshot notices and a unit test does not.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/fa-dialog-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/fa-dialog-check.js --freeze
const G = L.golden('fa-dialog-check');

const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

// The dialog's IIFE. `mustNot` proves the slice stopped before the next module.
const iife = G.value('iife', () => L.region(src, {
  contains: 'faChanGrid',
  must: ['congestionColour', 'bestChannel', 'faWarnText', 'wifiscan:start'],
  mustNot: ['Reports page', 'backupsPage'],
}));

/** What this gate covers, for element-coverage-audit. Declared before any work. */
const COVERS = ['faCurChan', 'faNetworks', 'faCongestion', 'faBestChan', 'faNoise',
  'faChanGrid', 'faWarnText', 'faIface', 'faScanBtn', 'faStopBtn', 'faSpin', 'faStatus'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

// ---- the live half -------------------------------------------------------
//
// The renderers are lifted with the state they close over, and driven directly.
// Nothing here opens a socket or a modal: the functions under test take rows and
// return HTML.
function liveRenderers() {
  const ctx = {
    String, Array, Object, Math, Number, JSON, parseInt, isNaN,
    document: { getElementById: () => null, addEventListener() {} },
  };
  vm.createContext(ctx);
  // FROZEN AS ONE SCRIPT. The pieces are lifted inside an array literal, which
  // is not a `const X = L.fn(...)` the converter can see; unfrozen they become
  // empty strings and the VM then reports `congestionColour is not defined`.
  vm.runInContext(G.value('liveScript', () => [
    L.line(src, 'function esc('),
    L.whole(src, '  function congestionColour('),
    L.whole(src, '  function bestChannel('),
    'this.congestionColour = congestionColour; this.bestChannel = bestChannel; this.esc = esc;',
  ].join('\n')), ctx);
  return ctx;
}

const live = liveRenderers();

/** The live renderGrid, with its element writes replaced by a return. */
function liveGrid(rows, cur) {
  if (!rows.length) return '';
  const congestionColour = live.congestionColour, esc = live.esc;
  return rows.map(function (r) {
    var isCur = r.ch === cur;
    var load = r.load == null ? null : r.load;
    return '<div class="fa-chan' + (isCur ? ' is-current' : '') + '"' +
      ' style="background:' + congestionColour(load, 0.22) +
      ';border-color:' + congestionColour(load, 0.5) + '"' +
      ' title="' + esc(String(r.chRaw || r.ch)) + (r.nets != null ? (' · ' + r.nets + ' networks') : '') + '">' +
      '<div class="fa-chan-num">' + (r.chNum == null ? '&mdash;' : ('ch ' + r.chNum)) + '</div>' +
      '<div class="fa-chan-freq">' + r.ch + '</div>' +
      '<div class="fa-chan-load" style="color:' + congestionColour(load, 1) + '">' +
      (load == null ? '&mdash;' : (load + '%')) + '</div>' +
      '</div>';
  }).join('');
}

function liveStats(rows, cur) {
  const best = live.bestChannel(rows);
  const c = rows.filter((r) => r.ch === cur)[0];
  const nf = rows.map((r) => r.nf).filter((v) => v != null);
  const nets = rows.reduce((n, r) => n + (r.nets || 0), 0);
  const unit = (u) => '<span class="fa-stat-unit">' + u + '</span>';
  const median = nf.length ? nf.slice().sort((a, b) => a - b)[Math.floor(nf.length / 2)] : null;
  return {
    faCurChan: (cur || '&mdash;') + unit('MHz'),
    faNetworks: rows.length ? String(nets) : '&mdash;',
    faCongestion: (c && c.load != null ? c.load : '&mdash;') + unit('%'),
    faBestChan: (best ? best.ch : '&mdash;') + unit('MHz'),
    faNoise: (median == null ? '&mdash;' : median) + unit('dBm'),
  };
}

function liveWarning(list, sel) {
  const rec = list.filter((i) => i.name === sel)[0];
  if (!rec) return 'Scanning takes the selected radio off the air.';
  const n = rec.clients || 0;
  if (n === 0) {
    return 'This radio has <b>no clients connected</b>, so scanning it should ' +
      'interrupt nobody. Other radios on this router are unaffected.';
  }
  return 'Scanning takes this radio off the air. Its <b>' + n + ' connected ' +
    (n === 1 ? 'client' : 'clients') + ' will be disconnected</b> for the duration of the ' +
    'scan, including any on its other SSIDs, and may take some seconds to return afterwards. ' +
    'Other radios on this router are unaffected.';
}

function liveOptions(list) {
  if (!list.length) return '<option disabled selected>No scannable radio</option>';
  const esc = live.esc;
  return list.map(function (i) {
    var n = i.clients || 0;
    return '<option value="' + esc(i.name) + '">' + esc(i.name) +
      ' · ' + (n ? (n + (n === 1 ? ' client' : ' clients')) : 'no clients') + '</option>';
  }).join('');
}

// ---- the ported half -----------------------------------------------------
const ENTRY = path.join(ROOT, 'testdata', '.fa-entry.ts');
fs.writeFileSync(ENTRY,
  "export { statsHTML, gridHTML, warningHTML, ifaceOptionsHTML, congestionColour, bestChannel,\n" +
  "  noiseFloor, scanStatus, controlsFor } from '../web/src/pages/wireless-fa.js';\n");
const OUT = path.join(ROOT, 'testdata', '.fa.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const port = require(OUT);

// ---- the payloads --------------------------------------------------------
const row = (ch, over = {}) => ({
  ch, chNum: null, chRaw: String(ch) + '/20-Ce', nets: 0, load: 0, nf: -100,
  maxSig: null, minSig: null, ...over,
});

const CASES = {
  'nothing scanned yet': { rows: [], cur: null },
  'a full 2.4GHz sweep': {
    rows: [row(2412, { chNum: 1, nets: 4, load: 55, nf: -98 }),
           row(2437, { chNum: 6, nets: 1, load: 12, nf: -99 }),
           row(2462, { chNum: 11, nets: 9, load: 88, nf: -95 })],
    cur: 2437,
  },
  // Every congestion band, so the colour ladder is exercised end to end.
  'the whole colour ladder': {
    rows: [row(5180, { load: 0 }), row(5200, { load: 19 }), row(5220, { load: 20 }),
           row(5240, { load: 39 }), row(5260, { load: 40 }), row(5280, { load: 59 }),
           row(5300, { load: 60 }), row(5320, { load: 79 }), row(5340, { load: 80 }),
           row(5360, { load: 100 })],
    cur: 5260,
  },
  'loads and noise missing': {
    rows: [row(2412, { load: null, nf: null, nets: null }),
           row(2437, { load: null, nf: null, nets: null })],
    cur: 2412,
  },
  // A tie on LOAD alone, broken by the network count. Without this the two
  // halves of the comparator are indistinguishable — a mutation reversing the
  // network arm SURVIVED the both-keys case, which ties on everything and so
  // cannot see it.
  'a tie on load, broken by networks': {
    rows: [row(5180, { load: 10, nets: 9 }), row(5200, { load: 10, nets: 1 }),
           row(5220, { load: 10, nets: 5 })],
    cur: 5180,
  },
  // A TIE on load AND networks: the recommendation must not move between two
  // scans of an unchanged environment.
  'a tie on both keys': {
    rows: [row(5180, { load: 10, nets: 2 }), row(5200, { load: 10, nets: 2 }),
           row(5220, { load: 10, nets: 2 })],
    cur: 5200,
  },
  'the current channel was not scanned': {
    rows: [row(2412, { load: 5 })], cur: 5180,
  },
  // An EVEN number of noise readings: the median takes the upper of the middle
  // pair, which is `Math.floor(n/2)` on a zero-based index.
  'an even number of noise readings': {
    rows: [row(1, { nf: -100 }), row(2, { nf: -90 }), row(3, { nf: -80 }), row(4, { nf: -70 })],
    cur: 2,
  },
  // Markup in a channel label: the title attribute must escape it.
  'a hostile channel label': {
    rows: [row(2412, { chRaw: '2412/20-Ce"><img src=x>', nets: 1, load: 3 })], cur: 2412,
  },
};

const IFACE_CASES = {
  'no radios at all': { list: [], sel: '' },
  'an idle radio': { list: [{ name: 'wifi1', running: true, clients: 0 }], sel: 'wifi1' },
  'one client': { list: [{ name: 'wifi1', running: true, clients: 1 }], sel: 'wifi1' },
  'many clients': { list: [{ name: 'wifi1', running: true, clients: 15 }], sel: 'wifi1' },
  'nothing selected': { list: [{ name: 'wifi1', running: true, clients: 3 }], sel: '' },
  'a selection that is gone': {
    list: [{ name: 'wifi1', running: true, clients: 3 }], sel: 'wifi9' },
  'a name needing escaping': {
    list: [{ name: 'wifi<1>&"', running: true, clients: 2 }], sel: 'wifi<1>&"' },
  'two radios': {
    list: [{ name: 'wifi1', running: true, clients: 0 },
           { name: 'wifi2-5GHz', running: true, clients: 11 }], sel: 'wifi2-5GHz' },
};

/**
 * The live `setScanning`, as the four states it writes.
 *
 * Transcribed rather than lifted for the same reason `liveGrid` is: the original
 * writes straight to elements inside a closure over the dialog's state, and
 * there is no seam. What is compared is the DECISION — which control is shown,
 * which is disabled — which is the part a port gets wrong.
 */
function liveControls(scanning) {
  return {
    scanDisplay: scanning ? 'none' : '',
    stopDisplay: scanning ? '' : 'none',
    spinOn: !!scanning,
    ifaceDisabled: !!scanning,
    durationDisabled: !!scanning,
  };
}

/** The live countdown line. */
function liveStatus(endsAt, now) {
  var left = Math.max(0, Math.ceil((endsAt - now) / 1000));
  return left > 0 ? ('Scanning… ' + left + 's — clients disconnected') : 'Finishing…';
}

// ---- compare -------------------------------------------------------------
const bad = [];
let checked = 0;

for (const [name, { rows, cur }] of Object.entries(CASES)) {
  const a = { stats: liveStats(rows, cur), grid: liveGrid(rows, cur) };
  const b = { stats: port.statsHTML(rows, cur), grid: port.gridHTML(rows, cur) };
  checked++;
  if (JSON.stringify(a) !== JSON.stringify(b)) bad.push({ name, a, b });
}
for (const [name, { list, sel }] of Object.entries(IFACE_CASES)) {
  const a = { warn: liveWarning(list, sel), opts: liveOptions(list) };
  const b = { warn: port.warningHTML(list, sel), opts: port.ifaceOptionsHTML(list) };
  checked++;
  if (JSON.stringify(a) !== JSON.stringify(b)) bad.push({ name, a, b });
}

for (const scanning of [true, false]) {
  checked++;
  const a = liveControls(scanning), b = port.controlsFor(scanning);
  if (JSON.stringify(a) !== JSON.stringify(b)) bad.push({ name: 'controls scanning=' + scanning, a, b });
}

// The countdown, at the boundaries that decide what it says.
const NOW = 1_000_000;
for (const endsAt of [NOW + 30_000, NOW + 1_500, NOW + 1_000, NOW + 1, NOW, NOW - 1, NOW - 5_000]) {
  checked++;
  const a = liveStatus(endsAt, NOW), b = port.scanStatus(endsAt, NOW);
  if (a !== b) bad.push({ name: 'status endsAt=' + (endsAt - NOW), a, b });
}

// ---- BELIEVABILITY -------------------------------------------------------
//
// Every comparison above is of two strings, and two renderers that both returned
// '' would agree on all of them. So the LIVE side alone must produce something
// discriminating.
{
  const full = liveStats(CASES['a full 2.4GHz sweep'].rows, 2437);
  assert.ok(full.faBestChan.includes('2437'),
    'the live side did not pick the least congested channel');
  assert.ok(liveGrid(CASES['a full 2.4GHz sweep'].rows, 2437).includes('is-current'),
    'the live grid did not mark the current channel');
  assert.ok(liveGrid(CASES['the whole colour ladder'].rows, 5260).match(/rgba\(74,222,128/),
    'the open-channel colour never appears');
  assert.ok(liveGrid(CASES['the whole colour ladder'].rows, 5260).match(/rgba\(248,113,113/),
    'the congested colour never appears');
  assert.ok(!liveGrid(CASES['a hostile channel label'].rows, 2412).includes('<img src=x>'),
    'the live grid does not escape a channel label — this case would be pinning an injection');
  assert.notEqual(liveWarning(IFACE_CASES['an idle radio'].list, 'wifi1'),
    liveWarning(IFACE_CASES['many clients'].list, 'wifi1'),
    'the warning says the same thing whether or not clients are connected');
  assert.ok(liveWarning(IFACE_CASES['many clients'].list, 'wifi1').includes('15 connected clients'),
    'the warning does not name how many clients will drop');
  assert.ok(liveOptions(IFACE_CASES['two radios'].list).includes('11 clients'),
    'the picker does not carry the client count');
  assert.equal(liveStats([], null).faNetworks, '&mdash;', 'an empty scan reported a network count');
  assert.notDeepEqual(liveControls(true), liveControls(false),
    'the controls look the same scanning or not — this comparison would pass against anything');
  assert.ok(liveStatus(NOW + 30_000, NOW).includes('30s'), 'the countdown does not count');
  assert.equal(liveStatus(NOW, NOW), 'Finishing…',
    'at zero the live side still counts rather than saying it is waiting');
  assert.equal(liveStatus(NOW - 5_000, NOW), 'Finishing…',
    'past zero the live side counts into negative numbers');
  // The network arm of the comparator must actually decide this case.
  assert.equal(live.bestChannel(CASES['a tie on load, broken by networks'].rows).ch, 5200,
    'the load tie is not being broken by the fewest networks');
}

fs.rmSync(OUT, { force: true });
if (bad.length) {
  for (const x of bad) {
    console.error('[' + x.name + ']');
    console.error('  live ' + JSON.stringify(x.a));
    console.error('  port ' + JSON.stringify(x.b));
  }
  console.error('\nfa-dialog-check: ' + bad.length + ' of ' + checked + ' cases differ');
  process.exit(1);
}
console.log('fa-dialog-check: ' + checked + ' cases identical');
