'use strict';
/**
 * The Reports export links, live against ported.
 *
 * ── THERE IS NO LONGER ANY DIFFERENCE AT ALL ────────────────────────────────
 *
 * This used to read "one deliberate difference, and it is the prefix": the live
 * app built `/api/reports/<type>/export` while the port built
 * `/next/api/reports/…`, because `/api/*` still proxied to Node and a ported page
 * linking there would have pointed at an implementation nobody had compared.
 *
 * The prefix came off on 2026-08-25 with the operator's cutover decision, so
 * both sides now build the SAME path and the comparison is strictly stronger:
 * nothing is stripped before comparing, so a prefix reappearing on either side
 * is now a failure rather than something this gate quietly normalises away.
 *
 * The two constants are kept rather than collapsed into one — if they ever
 * diverge again it should be visible here as two different values, not hidden in
 * a shared literal.
 *
 * ── WHAT THE CORPUS IS FOR ──────────────────────────────────────────────────
 *
 *   the aggregate     read from the SELECT at call time, not from the load's
 *                     snapshot. Omitted entirely when empty rather than sent as
 *                     `aggregate=` — an empty parameter is a value, and the
 *                     server would have to decide what it means.
 *   the interface     arrives pre-encoded as `interface=<name>`, so a name with
 *                     a space or a slash is encoded ONCE. Encoding the whole
 *                     `extra` again would double it.
 *   router ids        are encoded; `from` and `to` are not, because they are
 *                     numbers and the original leaves them alone.
 *   reveal only       there is no hiding path. An empty report still shows its
 *                     buttons, because "no rows in this range" is a legitimate
 *                     thing to export.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/export-links-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/export-links-check.js --freeze
const G = L.golden('export-links-check');
const src = L.liveSource(ROOT);

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent, and this then threw `cannot find <name>` at module scope — before
  // any frozen output could be served. An empty slice is harmless because the
  // live half is never entered when the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name + ' — it has moved or been rewritten');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}
// The interface `extra` is an INLINE expression at each live call site rather
// than a function, so it is lifted as text and wrapped. Comparing one level
// down like this is the only way to reach it — the port factored it into a
// helper, which is a shape change, and what must not change is the string.
const ifExprSrc = slice("    var ifExtra = $('rptBwIface')", ';', 'the bandwidth interface extra');

const liveSrc = [
  slice('  function exportUrl(type, fmt, routerId, from, to, extra) {', '\n  }', 'exportUrl'),
  slice('  function setExportLinks(csvEl, pdfEl, type, routerId, from, to, extra) {', '\n  }', 'setExportLinks'),
].join('\n');

const ENTRY = path.join(ROOT, 'testdata', '.export-entry.ts');
fs.writeFileSync(ENTRY,
  "export { exportUrl, setExportLinks, ifaceExtra } from '../web/src/pages/reports.js';\n");
const OUT = path.join(ROOT, 'testdata', '.export-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const PREFIX_LIVE = '/api/reports/';
const PREFIX_PORT = '/api/reports/';

/**
 * What this gate covers, for `element-coverage-audit`.
 *
 * Both are INPUTS the gate never displays, and both are covered in the sense
 * that matters: every case sets a value and compares the export URL built from
 * it, so a page that stopped reading either one fails here. `rptAggregate` was
 * reported uncovered only because this file builds its nodes as PROPERTIES
 * (`nodes.rptAggregate`), which the audit's text scan cannot see.
 */
const COVERS = ['rptAggregate', 'rptBwIface'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

function makeWorld(agg) {
  const nodes = {};
  for (const id of ['rptBwCsvLink', 'rptBwPdfLink', 'rptPingCsvLink', 'rptPingPdfLink',
                    'rptTrafficCsvLink', 'rptTrafficPdfLink', 'rptAlertCsvLink', 'rptAlertPdfLink',
                    'rptConnCsvLink', 'rptConnPdfLink']) {
    const style = {};
    nodes[id] = {
      id, href: '',
      style: { get display() { return style.display === undefined ? 'none' : style.display; },
               set display(v) { style.display = String(v); } },
    };
  }
  nodes.rptAggregate = { value: agg };
  nodes.rptBwIface = { value: '' };
  return {
    doc: { getElementById: (id) => nodes[id] || null },
    nodes,
    state(prefix) {
      // The LINKS only. The select stand-ins live in the same map and have no
      // href; naming what belongs here beats excluding what does not, since the
      // exclusion list grew silently the moment a second select was added.
      return JSON.stringify(Object.keys(nodes).filter((k) => k.endsWith('Link')).map((id) => {
        const n = nodes[id];
        // The one intended difference, removed from both sides in one place.
        const href = n.href.startsWith(prefix) ? '<prefix>' + n.href.slice(prefix.length) : n.href;
        return [id, href, n.style.display];
      }), null, 1);
    },
  };
}

function liveRun(agg, body) {
  const w = makeWorld(agg);
  const ctx = { document: w.doc, encodeURIComponent, String };
  ctx.$ = (id) => w.doc.getElementById(id);
  ctx.rptAggregate = w.nodes.rptAggregate;
  vm.createContext(ctx);
  vm.runInContext(liveSrc + '\nfunction __ifExtra(){' + ifExprSrc + '\nreturn ifExtra; }', ctx);
  body({
    set: (csv, pdf, type, routerId, from, to, extra) =>
      ctx.setExportLinks(w.doc.getElementById(csv), w.doc.getElementById(pdf),
        type, routerId, from, to, extra),
    ifExtra: () => ctx.__ifExtra(),
  }, w);
  return w.state(PREFIX_LIVE);
}

function portRun(agg, body) {
  const w = makeWorld(agg);
  const saved = global.document;
  global.document = w.doc;
  try {
    delete require.cache[require.resolve(OUT)];
    const m = require(OUT);
    body({ set: (...a) => m.setExportLinks(...a), ifExtra: () => m.ifaceExtra('rptBwIface') }, w);
  } finally {
    if (saved === undefined) delete global.document; else global.document = saved;
  }
  return w.state(PREFIX_PORT);
}

const bad = [];
let cases = 0;
function compare(what, agg, act) {
  cases++;
  const a = G.live(G.seq(), () => liveRun(agg, act));
  const b = portRun(agg, act);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

const SECTIONS = [
  ['rptPingCsvLink', 'rptPingPdfLink', 'ping', ''],
  ['rptAlertCsvLink', 'rptAlertPdfLink', 'alerts', ''],
  ['rptConnCsvLink', 'rptConnPdfLink', 'connectivity', ''],
  ['rptBwCsvLink', 'rptBwPdfLink', 'bandwidth', 'interface=ether1'],
  ['rptTrafficCsvLink', 'rptTrafficPdfLink', 'traffic', 'interface=ether1'],
];
const FROM = 1773500000000;
const TO = 1773567000000;

// Every section, at every aggregate the select offers plus none.
for (const agg of ['', 'hour', 'day', 'week', 'month']) {
  for (const [csv, pdf, type, extra] of SECTIONS) {
    compare(type + ' at aggregate ' + JSON.stringify(agg), agg,
      (api) => api.set(csv, pdf, type, 'rtr-1', FROM, TO, extra));
  }
}
// Router ids that need encoding. An id is operator-supplied in the live app's
// data model, so a space or an ampersand is not hypothetical.
for (const id of ['rtr-1', 'Branch Office', 'a&b', 'a/b', 'a?b', 'a=b', '', 'ünïcode', 'a+b', 'a%b']) {
  compare('routerId ' + JSON.stringify(id), 'hour',
    (api) => api.set('rptPingCsvLink', 'rptPingPdfLink', 'ping', id, FROM, TO, ''));
}
// Interface names, pre-encoded by the caller — encoded ONCE, not twice.
for (const iface of ['ether1', 'bridge local', 'vlan/10', 'a&b', 'ether1+2', 'wlan%1']) {
  const extra = 'interface=' + encodeURIComponent(iface);
  compare('interface ' + JSON.stringify(iface), '',
    (api) => api.set('rptBwCsvLink', 'rptBwPdfLink', 'bandwidth', 'rtr-1', FROM, TO, extra));
}
// The interface `extra` itself, built from the select rather than handed in.
// RouterOS names take spaces, slashes and ampersands, so this is a real
// encoding boundary and not a hypothetical one.
for (const name of ['', 'ether1', 'bridge local', 'vlan/10', 'a&b', 'ether1+2', 'wlan%1',
                    'ünïcode', 'a=b', 'a?b', '  ', '2.4GHz WiFi']) {
  compare('the interface extra for ' + JSON.stringify(name), 'hour', (api, w) => {
    w.nodes.rptBwIface.value = name;
    // Through the URL, so a double-encode shows up where it would be seen.
    api.set('rptBwCsvLink', 'rptBwPdfLink', 'bandwidth', 'rtr-1', FROM, TO, api.ifExtra());
  });
}

// Ranges, including the degenerate ones.
for (const [from, to] of [[0, 0], [FROM, FROM], [TO, FROM], [-1, 0], [1.5, 2.5]]) {
  compare('range ' + from + '..' + to, 'day',
    (api) => api.set('rptPingCsvLink', 'rptPingPdfLink', 'ping', 'rtr-1', from, to, ''));
}
// An extra that is not an interface at all.
compare('an extra with several parameters', 'hour',
  (api) => api.set('rptPingCsvLink', 'rptPingPdfLink', 'ping', 'rtr-1', FROM, TO, 'a=1&b=2'));
// Setting the same links twice — the second wins, and nothing accumulates.
compare('the same section rendered twice at different aggregates', 'day', (api) => {
  api.set('rptPingCsvLink', 'rptPingPdfLink', 'ping', 'rtr-1', FROM, TO, '');
  api.set('rptPingCsvLink', 'rptPingPdfLink', 'ping', 'rtr-2', FROM, TO, '');
});
// All five, as one load does.
compare('a whole load sets all five sections', 'hour', (api) => {
  for (const [csv, pdf, type, extra] of SECTIONS) api.set(csv, pdf, type, 'rtr-1', FROM, TO, extra);
});
// And the sections nobody rendered stay hidden, which is what the state carries.

fs.rmSync(OUT, { force: true });
if (bad.length) {
  console.error('the export links differ from the live ones:\n\n' + bad.slice(0, 2).join('\n\n') +
    (bad.length > 2 ? '\n\n… and ' + (bad.length - 2) + ' more' : '') + '\n');
  process.exit(1);
}
console.log(`export links match the live ones (${cases} cases across ${SECTIONS.length} sections, ` +
  `prefix difference stated and stripped)`);
