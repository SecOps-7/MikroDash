'use strict';
/**
 * The REPORTS tables, live against ported.
 *
 * ── THE BIGGEST BLOCK OF UNTESTED PORTED UI ─────────────────────────────────
 *
 * `tools/element-coverage-audit.js` found the Reports family sitting behind
 * three narrow gates with 44 uncovered elements between them. This gate takes
 * the four TABLE renderers — alerts, ping, connections and traffic — which are
 * where most of those elements live.
 *
 * ── THE WHOLE IIFE IS RUN, AND THE RENDERERS EXPOSED FROM INSIDE ────────────
 *
 * All forty functions and every element variable are IIFE-local, so there is
 * nothing to lift piecemeal: picking some and stubbing the rest would choose
 * what the gate may notice, invisibly. The region is executed and the four
 * renderers published from within it.
 *
 * ── SIGNATURES DIFFER ON PURPOSE ────────────────────────────────────────────
 *
 * Live's `renderAlerts(rows, routerId, from, to)` carries three arguments the
 * port's `renderAlerts(rows)` does not: they drive the CSV export links, which
 * the port builds elsewhere and `export-links-check.js` already covers. The gate
 * passes them to live and compares only the elements both sides own.
 *
 * WHAT IT CANNOT SEE: the charts (Chart.js), layout and focus.
 *
 * ── THE PAGERS ARE DRIVEN NOW, AND THEY WERE NOT ────────────────────────────
 *
 * This line used to end "…and the pagers' click wiring beyond the markup they
 * produce", which was true and cost something: `_bwPage`/`_pingPage` survive a
 * render, so the rule that matters is what happens when a SECOND, SHORTER
 * result set arrives while the operator is on page 5. `clicks:` presses Next,
 * `prevClicks:` presses Prev, and `then:` delivers the second payload — both
 * sides through their own listeners, installed by their own wiring function.
 *
 * THE PAGE SIZE IS 100. The first draft of these cases assumed 50, so a 51-row
 * "two page" set was one page, every Next was refused, and all five pagination
 * mutations survived a green run. Measured, after every one of them survived.
 *
 * ── TWO GUARDS THAT RESCUE EACH OTHER, AND CANNOT BE KILLED ALONE ───────────
 *
 * The page index is protected twice: Next refuses to go past `pages - 1`, and
 * `renderBwPage` clamps `if (page >= pages) page = pages - 1`. Weaken EITHER and
 * this gate stays green — Next overshooting is caught by the clamp, and the
 * clamp is unreachable while Next is correct. Removing BOTH is caught.
 *
 * That is a property of the code, not a hole in the corpus, and it is written
 * here rather than left for the next reader to rediscover. The redundancy is
 * real on both sides, so reproducing it is the port's job; a gate reporting
 * "clamp covered" would claim something no behavioural test of this shape can.
 *
 * It also records what the clamp is FOR. Every new result set resets the index
 * to zero, so no ordinary flow reaches it — it exists for the moment Next has
 * just overshot, which is exactly why it hides that bug.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/reports-tables-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/reports-tables-check.js --freeze
const G = L.golden('reports-tables-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = G.value('iife', () => L.region(src, {
  banner: '// ── Reports page',
  must: ['renderAlerts', 'renderPing', 'renderTraffic', 'rptAlertStats'],
  mustNot: ['Queues page', 'backupsPage', 'DNS page', 'dnsSettingsBody'],
}));

// Element vars declared INSIDE the region (`var rptAlertStats = $('rptAlertStats')`),
// which `fileScopeEls` cannot see because they are not at file scope.
const REGION_ELS = [...iife.matchAll(/var\s+([A-Za-z_][\w]*)\s*=\s*\$\('([A-Za-z0-9_-]+)'\)/g)]
  .map(([, name, id]) => ({ name, id }));
assert.ok(REGION_ELS.length > 10, 'the region declares no element vars — the lift has broken');

const IDS = G.value('IDS', () => L.idsFor(src, iife));

// Declared AFTER TABLE_IDS, and it declares THOSE — see the note there.

const ENTRY = path.join(ROOT, 'testdata', '.rpt-entry.ts');
fs.writeFileSync(ENTRY, [
  "export { renderAlerts } from '../web/src/pages/reports-alerts.js';",
  "export { renderPing, renderConn, wirePingPager } from '../web/src/pages/reports-ping.js';",
  "export { renderTraffic, renderBandwidth, wireBwPager } from '../web/src/pages/reports-traffic.js';",
  "export { setReportTimezone } from '../web/src/pages/reports.js';",
].join('\n') + '\n');
const OUT = path.join(ROOT, 'testdata', '.rpt-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// Only the elements the four table renderers own. The tab bar, the date pickers
// and the chart canvases belong to other parts of the page.
const TABLE_IDS = ['rptAlertStats', 'rptAlertTbody', 'rptPingStats', 'rptPingTbody',
  'rptPingPager', 'rptPingPageInfo', 'rptPingPrev', 'rptPingNext',
  'rptConnStats', 'rptConnTbody', 'rptTrafficStats', 'rptTrafficTbody',
  // The Bandwidth Usage sub-tab. It is VOLUME where Traffic is RATE, and the
  // two are easy to conflate — the live comment above `renderBandwidth` records
  // that reducing `rows` here got it wrong twice, because the rows are averages
  // once an aggregation is picked AND capped by the query LIMIT. Every figure
  // comes from the server summary instead.
  'rptBwStats', 'rptBwTbody', 'rptBwPager', 'rptBwPageInfo', 'rptBwPrev', 'rptBwNext',
  'rptBwTruncHint'];

// WHAT THIS GATE COMPARES, which is not the same as what the region mentions.
// The first version answered `--ids` with all 66 ids the Reports IIFE touches,
// and `element-coverage-audit` promptly reported all six Reports modules as
// fully covered. They are not: this gate compares the four TABLES. Declaring
// reach instead of coverage is precisely the over-claim that audit exists to
// catch, and it caught its author within the hour.
if (process.argv.includes('--ids')) { console.log(JSON.stringify(TABLE_IDS)); process.exit(0); }

const snap = (doc) => {
  const n = doc.nodes;
  const out = {};
  for (const id of TABLE_IDS) {
    out[id] = n[id] ? { h: n[id].innerHTML, t: n[id].textContent,
      d: n[id].style && n[id].style.display, dis: n[id].disabled } : null;
  }
  return JSON.stringify(out);
};

function liveRun(call, TZ) {
  const doc = makeDoc(IDS, {});
  const ctx = {
    String, Array, Math, Number, Object, JSON, Date, parseInt, parseFloat, isFinite, isNaN,
    encodeURIComponent, document: doc,
    socket: { on() {}, emit() {} },
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {},
    window: { location: { origin: 'http://x' } },
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
    Chart: function () { return { destroy() {}, update() {}, data: {}, options: {} }; },
    // The region observes the page for layout changes it reacts to. Nothing this
    // gate drives depends on it firing, and a stub is safe HERE for the reason a
    // stub is not safe in general: it produces no markup, so it cannot reach the
    // comparison. (`resRow` taught that distinction — a stub for anything that
    // BUILDS markup is a rewrite of the live code.)
    MutationObserver: function () { return { observe() {}, disconnect() {} }; },
    ResizeObserver: function () { return { observe() {}, disconnect() {} }; },
    requestAnimationFrame: (fn) => { fn(); return 0; },
    cancelAnimationFrame: () => {},
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    __out: null,
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function fmtBytes('),
    L.whole(src, 'function fmtMbps('),
    L.whole(src, 'function maxOf('),
    L.whole(src, 'function fmtDataMB('),
    // App-wide helpers the region relies on. LIFTED, not stubbed: every one of
    // these produces or orders something that reaches the comparison.
    L.whole(src, 'function _sortRows('),
    L.whole(src, 'function _sortMul('),
    L.whole(src, 'function _renderSortHeader('),
    'function $(id){return document.getElementById(id);}',
    'function _debounce(fn){return fn;}',
    'function pageVisible(){return true;}',
    // FROM THE LIVE SOURCE, and then set explicitly below. `_displayTimezone`
    // decides whether timestamps are formatted in a named zone or the viewer's
    // own, so leaving it to a default would compare two sides that happen to
    // agree today. It is also the exact variable `tools/live-renderer.js` records
    // as having slipped through a call-only scan.
    L.line(src, 'var _displayTimezone'),
    '(function () {' + iife +
      '\n__out = { renderAlerts: renderAlerts, renderPing: renderPing,' +
      ' renderConn: renderConn, renderTraffic: renderTraffic,' +
      ' renderBandwidth: renderBandwidth };\n})();',
  ].join('\n'), ctx);
  assert.ok(ctx.__out && ctx.__out.renderAlerts, 'the region did not publish its renderers');
  vm.runInContext('_displayTimezone = ' + JSON.stringify(TZ) + ';', ctx);
  call(ctx.__out, doc);
  return snap(doc);
}

function portRun(call, TZ) {
  const doc = makeDoc(IDS, {});
  const prevWin = globalThis.window;
  const prevST = globalThis.setTimeout;
  globalThis.window = { location: { origin: 'http://x' } };
  globalThis.setTimeout = ((fn) => { fn(); return 0; });
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      const mod = require(OUT);
      if (mod.setReportTimezone) mod.setReportTimezone(TZ);
      // The live region installs its pager listeners as it evaluates; the port
      // exports that as a function, so calling it here is the same moment.
      mod.wirePingPager();
      mod.wireBwPager();
      call(mod, doc);
      return snap(doc);
    });
  } finally {
    globalThis.setTimeout = prevST;
    if (prevWin === undefined) delete globalThis.window; else globalThis.window = prevWin;
  }
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 3) {
    const A = JSON.parse(a), B = JSON.parse(b);
    for (const k of Object.keys(A)) {
      const x = JSON.stringify(A[k]), y = JSON.stringify(B[k]);
      if (x !== y) shout('DIFF %s [%s]\n  live: %s\n  port: %s', what, k,
        String(x).slice(0, 400), String(y).slice(0, 400));
    }
  }
}

// ── EVERY CASE RUNS UNDER BOTH TIMEZONE SETTINGS ────────────────────────────
//
// `fmtTs` has two entirely separate implementations: with a named zone it uses
// `Intl.DateTimeFormat`, and with none it builds the string by hand from
// `getFullYear()`/`p2(...)`. Pinning only the named-zone case left the hand-built
// branch — which is what an operator who has NOT set a timezone sees, i.e. the
// default — completely uncompared. A mutation dropping the two-digit padding
// survived the whole corpus until this existed.
//
// UTC for the named case because the corpus timestamps are absolute; '' for the
// other, where both sides format in the runner's own zone.
const ZONES = ['UTC', ''];

const FROM = 1756000000000, TO = 1756086400000;
// Read off the live row builder: `fired_at` (not `created_at`), `alert_label`
// falling back to `alert_type`, and the acknowledgement pair. My first fixture
// invented `created_at`/`message` and rendered empty cells that the port matched.
const AL = (o) => Object.assign({
  id: 1, alert_type: 'wan-down', alert_label: 'WAN down', severity: 'error',
  detail: 'WAN went down', fired_at: FROM, resolved_at: null,
  acknowledged_at: null, acknowledged_by: null, router_id: 'r1',
}, o);
const PG = (o) => Object.assign({ ts: FROM, rtt_ms: 12.5, loss_pct: 0, target: '1.1.1.1' }, o);
const CN = (o) => Object.assign({ ts: FROM, total: 120, tcp: 80, udp: 30, icmp: 5, other: 5 }, o);
const TR = (o) => Object.assign({
  ts: FROM, iface: 'ether1', rx_mbps: 12.5, tx_mbps: 3.25,
}, o);
const BW = (o) => Object.assign({
  ts: FROM, iface: 'ether1', rx_mb: 125.5, tx_mb: 40.25,
}, o);
const BSUM = (o) => Object.assign({
  rxTotalMb: 5000, txTotalMb: 1200, rxMaxMb: 300, txMaxMb: 90, bandwidthSamples: 2048,
}, o);
const SUM = (o) => Object.assign({
  // THE REAL KEY NAMES. This fixture said `maxRx / maxTx / avgRx / avgTx /
  // p95Rx / p95Tx` until 2026-08-24 and NO implementation has ever read any of
  // them — the payload calls them `rxMaxMbps`, `rxAvgMbps`, `rxP95Mbps` and so
  // on. So every stat card rendered its em-dash branch on both sides, agreed,
  // and the gate reported identical while covering none of them. Found by
  // `tools/fixture-key-audit.js`, which exists because the same defect hid the
  // whole DHCP half of the WAN gate.
  trafficSamples: 1440, bandwidthSamples: 1200,
  rxAvgMbps: 20, txAvgMbps: 10,
  rxMaxMbps: 100, txMaxMbps: 50,
  rxP95Mbps: 80, txP95Mbps: 40,
  rxTotalMb: 4096, txTotalMb: 1024,
  rxMaxMb: 900, txMaxMb: 300,
  rxPeakPct: 10, txPeakPct: 50,
  capacityDownMbps: 1000, capacityUpMbps: 100,
}, o);

// ── A DECLARATIVE CASE, AND ONE ADAPTER PER SIDE ────────────────────────────
//
// The two sides take different argument lists, and passing live's to both is a
// silent way to get nonsense: the port's `renderConn(rows, agg)` received the
// ROUTER ID as its aggregation and rendered a different set of columns, which
// read exactly like a port defect.
//
// Live carries `routerId`, `from` and `to` because it builds the CSV export
// links inline; the port builds those elsewhere and `export-links-check.js`
// covers them. So a case names its DATA and each side is called the way it
// actually wants to be.
const LIVE_CALL = {
  renderAlerts: (m, c) => m.renderAlerts(c.rows, 'r1', FROM, TO),
  renderPing: (m, c) => m.renderPing(c.rows, 'r1', FROM, TO),
  // LIVE TAKES NO `agg` ARGUMENT. It reads the aggregation from the `rptAggregate`
  // SELECT, while the port receives it as a parameter — so the adapter sets the
  // control before calling. Passing it positionally to live would have silently
  // become an extra ignored argument, and the aggregated cases would have
  // compared an aggregated port against an unaggregated live.
  renderConn: (m, c, doc) => { doc.nodes.rptAggregate.value = c.agg || ''; return m.renderConn(c.rows, 'r1', FROM, TO); },
  renderTraffic: (m, c, doc) => { doc.nodes.rptAggregate.value = c.agg || ''; return m.renderTraffic(c.rows, 'r1', FROM, TO, c.summary || null); },
  renderBandwidth: (m, c, doc) => { doc.nodes.rptAggregate.value = c.agg || ''; return m.renderBandwidth(c.rows, 'r1', FROM, TO, c.summary || null); },
};
const PORT_CALL = {
  renderAlerts: (m, c) => m.renderAlerts(c.rows),
  renderPing: (m, c) => m.renderPing(c.rows),
  renderConn: (m, c) => m.renderConn(c.rows, c.agg || ''),
  renderTraffic: (m, c) => m.renderTraffic(c.rows, c.summary || null, c.agg || ''),
  renderBandwidth: (m, c) => m.renderBandwidth(c.rows, c.summary || null, c.agg || ''),
};

/** N rows, one per second. The pagers need more than a page of them: 100. */
const BW_ROWS = (n) => Array.from({ length: n }, (_, i) => BW({ ts: FROM + i * 1000 }));
const PG_ROWS = (n) => Array.from({ length: n }, (_, i) => PG({ ts: FROM + i * 1000 }));

const C = (fn, rows, extra) => Object.assign({ fn, rows }, extra || {});

/**
 * Press Next `clicks` times, then optionally deliver a second result set.
 *
 * The ORDER is the whole point: paging happens first, the shorter payload
 * arrives second. Reversed, nothing is on trial — a renderer that dropped the
 * page index on every payload would still show page 1, which is what a naive
 * case would have expected anyway.
 */
function drivePager(doc, c, render) {
  const bw = c.fn === 'renderBandwidth';
  const next = bw ? 'rptBwNext' : 'rptPingNext';
  const prev = bw ? 'rptBwPrev' : 'rptPingPrev';
  for (let i = 0; i < (c.clicks || 0); i++) doc.nodes[next].fire('click');
  // PREV IS DRIVEN TOO. Only Next was, and `if (page > 0)` weakened to
  // `if (page >= 0)` therefore survived: nothing ever pressed Prev, so nothing
  // ever went below zero. A negative index slices from the END of the array —
  // the last hundred rows presented as page zero.
  for (let i = 0; i < (c.prevClicks || 0); i++) doc.nodes[prev].fire('click');
  if (c.then) render(c.then);
}

const CASES = {
  // ── alerts ────────────────────────────────────────────────────────────────
  'alerts: none': C('renderAlerts', []),
  'alerts: one open': C('renderAlerts', [AL({})]),
  'alerts: one resolved': C('renderAlerts', [AL({ resolved_at: TO })]),
  'alerts: open and resolved': C('renderAlerts', [AL({}), AL({ id: 2, resolved_at: TO })]),
  'alerts: the top type is the commonest': C('renderAlerts', [
    AL({ id: 1, alert_type: 'a' }), AL({ id: 2, alert_type: 'b' }), AL({ id: 3, alert_type: 'b' })]),
  'alerts: markup in a message': C('renderAlerts', [AL({ message: '<img src=x>' })]),
  'alerts: a quote in a type': C('renderAlerts', [AL({ alert_type: 'a"b' })]),
  'alerts: acknowledged, still open': C('renderAlerts', [AL({ acknowledged_at: TO, acknowledged_by: 'kim' })]),
  'alerts: acknowledged with no name': C('renderAlerts', [AL({ acknowledged_at: TO })]),
  'alerts: resolved and never acknowledged': C('renderAlerts', [AL({ resolved_at: TO })]),
  'alerts: no label falls back to the type': C('renderAlerts', [AL({ alert_label: '' })]),
  'alerts: many': C('renderAlerts', Array.from({ length: 25 }, (_, i) => AL({ id: i }))),
  // ── ping ──────────────────────────────────────────────────────────────────
  'ping: none': C('renderPing', []),
  'ping: one row': C('renderPing', [PG({})]),
  'ping: a null rtt is not zero': C('renderPing', [PG({ rtt_ms: null })]),
  'ping: every rtt null': C('renderPing', [PG({ rtt_ms: null }), PG({ rtt_ms: null })]),
  'ping: total loss': C('renderPing', [PG({ rtt_ms: null, loss_pct: 100 })]),
  'ping: zero loss': C('renderPing', [PG({ loss_pct: 0 })]),
  'ping: mixed': C('renderPing', [PG({}), PG({ rtt_ms: null, loss_pct: 100 }), PG({ rtt_ms: 40 })]),
  'ping: enough rows to paginate': C('renderPing',
    Array.from({ length: 250 }, (_, i) => PG({ ts: FROM + i * 1000, rtt_ms: i }))),
  // ── connections ───────────────────────────────────────────────────────────
  'conn: none': C('renderConn', []),
  'conn: one row': C('renderConn', [CN({})]),
  'conn: zero totals': C('renderConn', [CN({ total: 0, tcp: 0, udp: 0, icmp: 0, other: 0 })]),
  'conn: several': C('renderConn', [CN({}), CN({ ts: FROM + 1000, total: 200 })]),
  'conn: aggregated': C('renderConn', [CN({})], { agg: 'hour' }),
  // ── traffic ───────────────────────────────────────────────────────────────
  'traffic: none': C('renderTraffic', []),
  'traffic: one row': C('renderTraffic', [TR({})], { summary: SUM({}) }),
  'traffic: no summary': C('renderTraffic', [TR({})]),
  'traffic: no capacity configured': C('renderTraffic', [TR({})], { summary: SUM({ capacityDownMbps: 0, capacityUpMbps: 0 }) }),
  'traffic: zero rates': C('renderTraffic', [TR({ rx_mbps: 0, tx_mbps: 0 })], { summary: SUM({}) }),
  'traffic: several rows': C('renderTraffic',
    [TR({}), TR({ ts: FROM + 1000, rx_mbps: 40 })], { summary: SUM({}) }),
  'traffic: markup in an interface name': C('renderTraffic',
    [TR({ iface: '<b>e1</b>' })], { summary: SUM({}) }),
  'traffic: aggregated': C('renderTraffic', [TR({})], { summary: SUM({}), agg: 'hour' }),
  // ── bandwidth usage ───────────────────────────────────────────────────────
  'bw: none': C('renderBandwidth', []),
  'bw: one row': C('renderBandwidth', [BW({})], { summary: BSUM({}) }),
  'bw: no summary': C('renderBandwidth', [BW({})]),
  'bw: a null busiest bucket': C('renderBandwidth', [BW({})], { summary: BSUM({ rxMaxMb: null, txMaxMb: null }) }),
  'bw: a ZERO busiest bucket is not absent': C('renderBandwidth', [BW({})], { summary: BSUM({ rxMaxMb: 0, txMaxMb: 0 }) }),
  'bw: zero totals': C('renderBandwidth', [BW({ rx_mb: 0, tx_mb: 0 })], { summary: BSUM({ rxTotalMb: 0, txTotalMb: 0 }) }),
  // The count card names BUCKETS when aggregated and SAMPLES when not, and takes
  // its number from a different place in each case.
  'bw: unaggregated counts SAMPLES from the summary': C('renderBandwidth',
    [BW({}), BW({ ts: FROM + 1000 })], { summary: BSUM({ bandwidthSamples: 4096 }) }),
  'bw: aggregated counts BUCKETS from the rows': C('renderBandwidth',
    [BW({}), BW({ ts: FROM + 1000 })], { summary: BSUM({ bandwidthSamples: 4096 }), agg: 'hour' }),
  'bw: no sample count at all': C('renderBandwidth', [BW({})], { summary: BSUM({ bandwidthSamples: undefined }) }),
  // Pagination.
  'bw: exactly one page': C('renderBandwidth',
    Array.from({ length: 50 }, (_, i) => BW({ ts: FROM + i * 1000 })), { summary: BSUM({}) }),
  'bw: two pages': C('renderBandwidth',
    Array.from({ length: 51 }, (_, i) => BW({ ts: FROM + i * 1000 })), { summary: BSUM({}) }),
  'bw: many pages': C('renderBandwidth',
    Array.from({ length: 260 }, (_, i) => BW({ ts: FROM + i * 1000 })), { summary: BSUM({}) }),
  // Escaping.
  'bw: markup in an interface name': C('renderBandwidth', [BW({ iface: '<b>e1</b>' })], { summary: BSUM({}) }),

  // ── PAGING, AND THEN A SECOND RESULT SET ─────────────────────────────────
  //
  // The page index outlives a render on both sides. THE PAGE SIZE IS 100 — the
  // first draft of these cases assumed 50, so a 51-row "two page" set was one
  // page, every Next was refused, and all five pagination mutations survived a
  // green run. Measured, after they all survived.
  //
  // `then:` with MORE rows than the current index can hold is the case that
  // bites: a shorter set is rescued by the clamp, so it cannot tell a missing
  // reset from a present one. See the note under KNOWN_SHARED_DEAD below.
  'bw: paged forward': C('renderBandwidth', BW_ROWS(260), { summary: BSUM({}), clicks: 2 }),
  'bw: paged forward, then a LONGER set': C('renderBandwidth', BW_ROWS(260),
    { summary: BSUM({}), clicks: 2, then: BW_ROWS(260) }),
  'bw: paged forward, then a SHORTER set': C('renderBandwidth', BW_ROWS(260),
    { summary: BSUM({}), clicks: 2, then: BW_ROWS(150) }),
  'bw: paged forward, then ONE page': C('renderBandwidth', BW_ROWS(260),
    { summary: BSUM({}), clicks: 2, then: [BW({})] }),
  'bw: paged forward, then NOTHING': C('renderBandwidth', BW_ROWS(260),
    { summary: BSUM({}), clicks: 2, then: [] }),
  // Next at the last page must not walk off the end: 101 rows is two pages.
  'bw: Next past the last page': C('renderBandwidth', BW_ROWS(101), { summary: BSUM({}), clicks: 6 }),
  // Prev from the first page. A negative index slices from the END of the array.
  'bw: Prev on the first page': C('renderBandwidth', BW_ROWS(260), { summary: BSUM({}), prevClicks: 3 }),
  'bw: forward then all the way back': C('renderBandwidth', BW_ROWS(260),
    { summary: BSUM({}), clicks: 2, prevClicks: 5 }),

  'ping: paged forward': C('renderPing', PG_ROWS(260), { clicks: 2 }),
  'ping: paged forward, then a LONGER set': C('renderPing', PG_ROWS(260),
    { clicks: 2, then: PG_ROWS(260) }),
  'ping: paged forward, then NOTHING': C('renderPing', PG_ROWS(260), { clicks: 2, then: [] }),
  'ping: Next past the last page': C('renderPing', PG_ROWS(101), { clicks: 6 }),
  'ping: Prev on the first page': C('renderPing', PG_ROWS(260), { prevClicks: 3 }),
  'ping: forward then all the way back': C('renderPing', PG_ROWS(260), { clicks: 2, prevClicks: 5 }),
};

for (const TZ of ZONES) {
  const tag = TZ ? ' [tz=' + TZ + ']' : ' [tz=local]';
  for (const [name, c] of Object.entries(CASES)) {
    let a, b;
    try {
      a = G.live(name + tag, () => liveRun((m, d) => {
        LIVE_CALL[c.fn](m, c, d);
        drivePager(d, c, (rows) => LIVE_CALL[c.fn](m, Object.assign({}, c, { rows }), d));
      }, TZ));
    }
    catch (e) { shout('LIVE THREW on %s%s: %s', name, tag, e.message); bad++; checked++; continue; }
    try {
      b = portRun((m, d) => {
        PORT_CALL[c.fn](m, c, d);
        drivePager(d, c, (rows) => PORT_CALL[c.fn](m, Object.assign({}, c, { rows }), d));
      }, TZ);
    }
    catch (e) { shout('PORT THREW on %s%s: %s', name, tag, e.message); bad++; checked++; continue; }
    cmp(name + tag, a, b);
  }
}

// ── believability ──────────────────────────────────────────────────────────
{
  // ── A CLICK THAT REACHED NOTHING IS NOT A CLICK ──────────────────────────
  //
  // `fire('click')` on a node with no listener is a silent no-op, and a silent
  // no-op happens identically on both sides — so every `clicks:` case above
  // would compare two FIRST pages and pass. The live side alone is driven with
  // and without the clicks, and the two must differ.
  const rows = Array.from({ length: 260 }, (_, i) => BW({ ts: FROM + i * 1000 }));
  const c = { fn: 'renderBandwidth', rows, summary: BSUM({}) };
  const still = JSON.parse(G.live('auto:7', () => liveRun((m, d) => LIVE_CALL.renderBandwidth(m, c, d), 'UTC')));
  const paged = JSON.parse(G.live('auto:6', () => liveRun((m, d) => {
    LIVE_CALL.renderBandwidth(m, c, d);
    d.nodes.rptBwNext.fire('click');
  }, 'UTC')));
  assert.notEqual(still.rptBwTbody.h, paged.rptBwTbody.h,
    'pressing Next changed nothing on the LIVE side — the pager listener is not installed, ' +
    'and every clicks: case is comparing two first pages');

  // ...and the same for the port, which installs its listeners through an
  // exported function rather than by evaluating. A missing `wireBwPager()` call
  // would be invisible above for exactly the same reason.
  const pStill = JSON.parse(portRun((m, d) => PORT_CALL.renderBandwidth(m, c, d), 'UTC'));
  const pPaged = JSON.parse(portRun((m, d) => {
    PORT_CALL.renderBandwidth(m, c, d);
    d.nodes.rptBwNext.fire('click');
  }, 'UTC'));
  assert.notEqual(pStill.rptBwTbody.h, pPaged.rptBwTbody.h,
    'pressing Next changed nothing on the PORT side — wireBwPager is not being called');
}
{
  const s = JSON.parse(G.live('auto:5', () => liveRun((m) => m.renderAlerts([AL({}), AL({ id: 2, resolved_at: TO })], 'r1', FROM, TO), 'UTC')));
  assert.match(s.rptAlertTbody.h, /WAN down/, 'the alerts table rendered no row');
  assert.match(s.rptAlertStats.h, /Open/, 'the alerts stat cards are missing');
}
{
  const s = JSON.parse(G.live('auto:4', () => liveRun((m) => m.renderPing([PG({})], 'r1', FROM, TO), 'UTC')));
  assert.match(s.rptPingTbody.h, /<tr/, 'the ping table rendered no row');
  assert.match(s.rptPingStats.h, /stat/i, 'the ping stat cards are missing');
}
{
  const s = JSON.parse(G.live('auto:3', () => liveRun((m) => m.renderTraffic([TR({})], 'r1', FROM, TO, SUM({})), 'UTC')));
  assert.match(s.rptTrafficTbody.h, /<tr/, 'the traffic table rendered no row');
}
{
  // A null rtt is not zero — it is a ping that did not come back.
  const nul = JSON.parse(G.live('auto:2', () => liveRun((m) => m.renderPing([PG({ rtt_ms: null })], 'r1', FROM, TO), 'UTC')));
  const zero = JSON.parse(G.live('auto:1', () => liveRun((m) => m.renderPing([PG({ rtt_ms: 0 })], 'r1', FROM, TO), 'UTC')));
  assert.notEqual(nul.rptPingTbody.h, zero.rptPingTbody.h,
    'a null rtt rendered the same as a zero one');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('reports-tables-check: %d cases identical', checked);
