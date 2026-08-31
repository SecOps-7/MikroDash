'use strict';
/**
 * THE REPORTS TAB STRIP, live against ported.
 *
 * Six tabs share one bar and one set of panels. `element-coverage-audit`
 * reported `#rptTabBar` uncovered and it was: `reports-tables-check` drives the
 * RENDERERS behind each tab and nothing drove the switch between them.
 *
 * ---- WHAT THE SWITCH HAS TO GET RIGHT -------------------------------------
 *
 *   the lit button   exactly one `.stab` carries `active`, and it is the one
 *                    that was pressed.
 *   the shown panel  exactly one `.rtab-panel` carries `active`, and it is
 *                    `#rtab-<name>`.
 *   the SCHEDULED    choosing it fetches the schedule list. That list is
 *   tab              CONFIGURATION, not report data, so it is deliberately not
 *                    part of `loadReports()` -- both sides say so in a comment --
 *                    and a switch that forgot it would show an empty Scheduled
 *                    tab until the operator pressed Load on a date range, which
 *                    is a control that has nothing to do with it.
 *
 * ---- ONE DIFFERENCE THAT IS NOT A DIFFERENCE ------------------------------
 *
 * The live handler marks the lit button by NODE IDENTITY (`b === btn`); the
 * port marks it by VALUE (`b.dataset.rtab === name`). They part company only if
 * two buttons carry the same `data-rtab`, which the markup does not do and
 * cannot usefully do. Recorded because it looks like a divergence on a first
 * read and is not one, and because a page that ever grew a duplicate tab value
 * would put the two rules genuinely at odds.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/reports-tabs-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/reports-tabs-check.js --freeze
const G = L.golden('reports-tabs-check');

const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = G.value('iife', () => L.region(src, {
  banner: '// ── Reports page',
  must: ['rptTabBar', 'rtab-', 'loadSchedules'],
  mustNot: ['Queues page', 'backupsPage', 'DNS page'],
}));
const IDS = G.value('IDS', () => L.idsFor(src, iife));

/**
 * The tabs, read from the markup this port serves rather than typed out.
 *
 * A tab added to the page and not to this list would otherwise be silently
 * untested -- the same reason `firewall-table-check` reads `page-firewall.html`
 * for the table's parent instead of asserting one.
 */
const PAGE = fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', 'page-reports.html'), 'utf8');
const TABS = [...PAGE.matchAll(/<button class="stab[^"]*" data-rtab="([^"]+)"/g)].map((m) => m[1]);
assert.ok(TABS.length >= 5, 'page-reports.html no longer declares the tab strip as this gate reads it');
assert.ok(TABS.includes('scheduled'), 'the Scheduled tab has moved -- its fetch rule is the point here');

const PANELS = TABS.map((t) => ({ id: 'rtab-' + t }));

/** What this gate covers, for element-coverage-audit. Declared before any work. */
const COVERS = ['rptTabBar'].concat(PANELS.map((p) => p.id));
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

const ENTRY = path.join(ROOT, 'testdata', '.rt-entry.ts');
fs.writeFileSync(ENTRY, "export { mountReports } from '../web/src/pages/reports.js';\n");
const OUT = path.join(ROOT, 'testdata', '.rt.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

function newDoc() {
  return makeDoc([...IDS, ...PANELS.map((p) => p.id)], {
    // The BAR is queried for `.stab` and the DOCUMENT for `.rtab-panel`, which
    // is how both sides do it -- the panels are not inside the bar.
    elementQuery: { rptTabBar: { '.stab': TABS, '[data-rtab]': '.stab' } },
    query: { '.rtab-panel': PANELS },
    queryAttr: { '.stab': 'data-rtab' },
  });
}

/** What a viewer can see, plus what went to the wire. */
function snap(doc, urls) {
  const stabs = doc.nodes.rptTabBar.querySelectorAll('.stab');
  return JSON.stringify({
    lit: stabs.filter((b) => b.classList.contains('active'))
      .map((b) => b.getAttribute('data-rtab')),
    shown: doc.queryNodes['.rtab-panel'].filter((p) => p.classList.contains('active'))
      .map((p) => p.id),
    // ONLY the schedules request. The date-range loads fire on mount and on
    // every Load press; including them would make this a comparison of when
    // reports reload, which `reports-latch-check` already owns.
    schedules: urls.filter((u) => u.includes('/schedules')).length,
  }, null, 1);
}

const settle = () => new Promise((r) => setImmediate(r));

function press(doc, tabs) {
  for (const t of tabs) {
    const btn = doc.nodes.rptTabBar.querySelectorAll('.stab')
      .find((b) => b.getAttribute('data-rtab') === t);
    if (!btn) throw new Error('no tab button for ' + t);
    // Delegated on the BAR with `closest`, which is what a real click does.
    btn.closest = (sel) => (sel === '[data-rtab]' ? btn : null);
    doc.nodes.rptTabBar.fire('click', { target: btn });
  }
}

const answer = () => Promise.resolve({
  ok: true, json: () => Promise.resolve({ ok: true, rows: [], runs: [], schedules: [] }),
});

async function liveRun(tabs) {
  const doc = newDoc();
  const urls = [];
  const ctx = {
    String, Array, Math, Number, Object, JSON, Date, parseInt, parseFloat, isFinite, isNaN,
    encodeURIComponent, document: doc,
    socket: { on() {}, emit() {} },
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {},
    window: { location: { origin: 'http://x' }, confirm: () => true, prompt: () => '' },
    fetch: (u) => { urls.push(String(u)); return answer(); },
    Chart: function () { return { destroy() {}, update() {}, data: {}, options: {} }; },
    MutationObserver: function () { return { observe() {}, disconnect() {} }; },
    ResizeObserver: function () { return { observe() {}, disconnect() {} }; },
    requestAnimationFrame: (fn) => { fn(); return 0; },
    cancelAnimationFrame: () => {},
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function fmtBytes('),
    L.whole(src, 'function fmtMbps('),
    L.whole(src, 'function maxOf('),
    L.whole(src, 'function fmtDataMB('),
    L.whole(src, 'function _sortRows('),
    L.whole(src, 'function _sortMul('),
    L.whole(src, 'function _renderSortHeader('),
    'function $(id){return document.getElementById(id);}',
    'function _debounce(fn){return fn;}',
    'function pageVisible(){return true;}',
    L.line(src, 'var _displayTimezone'),
    '(function () {' + iife + '\n}());',
  ].join('\n'), ctx);
  doc.nodes.rptRouter.value = 'r1';
  urls.length = 0;               // the mount's own loads are not on trial
  press(doc, tabs);
  await settle(); await settle();
  return snap(doc, urls);
}

async function portRun(tabs) {
  const doc = newDoc();
  const urls = [];
  const prev = {
    doc: globalThis.document, win: globalThis.window, fetch: globalThis.fetch,
    ls: globalThis.localStorage, st: globalThis.setTimeout,
  };
  globalThis.document = doc;
  globalThis.window = { location: { origin: 'http://x' } };
  globalThis.fetch = (u) => { urls.push(String(u)); return answer(); };
  globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
  globalThis.setTimeout = ((fn) => { fn(); return 0; });
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).mountReports([]);
    doc.nodes.rptRouter.value = 'r1';
    urls.length = 0;
    press(doc, tabs);
    await settle(); await settle();
    return snap(doc, urls);
  } finally {
    for (const [k, g] of [['doc', 'document'], ['win', 'window'], ['fetch', 'fetch'],
                          ['ls', 'localStorage'], ['st', 'setTimeout']]) {
      if (prev[k] === undefined) delete globalThis[g]; else globalThis[g] = prev[k];
    }
  }
}

const CASES = {};
for (const t of TABS) CASES['choose ' + t] = [t];
CASES['every tab in turn'] = TABS.slice();
CASES['scheduled twice'] = ['scheduled', 'scheduled'];
CASES['away from scheduled and back'] = ['scheduled', 'ping', 'scheduled'];
CASES['ping after scheduled'] = ['scheduled', 'ping'];

async function main() {
  const bad = [];
  let checked = 0;
  for (const [name, tabs] of Object.entries(CASES)) {
    const a = await G.live(name, () => liveRun(tabs));
    const b = await portRun(tabs);
    checked++;
    if (a !== b) bad.push({ name, a, b });
  }

  // ---- BELIEVABILITY -------------------------------------------------------
  //
  // Every case above compares two snapshots, and two pages that ignored every
  // click would produce identical ones. So the LIVE side alone must move, and
  // the Scheduled tab alone must fetch.
  const start = JSON.parse(await G.live('auto:3', () => liveRun([])));
  const moved = JSON.parse(await G.live('auto:2', () => liveRun(['bandwidth'])));
  assert.notDeepEqual(start.lit, moved.lit,
    'pressing a tab lit nothing new on the LIVE side -- the click never reached the handler');
  assert.deepEqual(moved.lit, ['bandwidth'], 'exactly the pressed tab must be lit');
  assert.equal(moved.shown.length, 1, 'exactly one panel must be shown');
  const sched = JSON.parse(await G.live('auto:1', () => liveRun(['scheduled'])));
  assert.ok(sched.schedules > 0, 'the LIVE Scheduled tab fetched no schedule list');
  assert.equal(moved.schedules, 0, 'a non-Scheduled tab fetched the schedule list');

  fs.rmSync(OUT, { force: true });
  if (bad.length) {
    for (const x of bad) {
      console.error('[' + x.name + ']');
      console.error('  live ' + x.a.replace(/\s+/g, ' '));
      console.error('  port ' + x.b.replace(/\s+/g, ' '));
    }
    console.error('\nreports-tabs-check: ' + bad.length + ' of ' + checked + ' cases differ');
    process.exit(1);
  }
  console.log('reports-tabs-check: ' + checked + ' cases identical (' + TABS.length + ' tabs)');
}

// A REJECTION MUST NOT BE SILENT — see the note in sched-runs-check: a bare
// `main()` lets an assertion failure inside it exit 0 with no output.
main().catch((e) => {
  console.error(String((e && e.stack) || e));
  process.exit(1);
});
