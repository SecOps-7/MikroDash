'use strict';
/**
 * THE INTERFACES VIEW SWITCH, live against ported — the last unit on this page.
 *
 * `iface-list-check` and `iface-tiles-check` close the two renderers, but both
 * reach their view ASYMMETRICALLY: the port through its real `localStorage`
 * path, the live side by setting `_ifaceView` in the preamble. Each says so, and
 * each declines to claim `#ifaceListWrap` or `#ifaceCardSize` for exactly that
 * reason. This gate drives the CONTROL on both sides, so it can.
 *
 * ---- WHAT THE SWITCH HAS TO GET RIGHT --------------------------------------
 *
 *   THE PAIR        `#ifaceGrid` is hidden in list view and `#ifaceListWrap` is
 *                   hidden in every other. They are never both visible and never
 *                   both gone.
 *   THE LAST REAL   `data-size` is written only when the size is NOT 'list'. The
 *   SIZE            comment on both sides says why: a viewer who goes to list
 *                   view and comes back should get their card scale, not a reset
 *                   to compact. That single `if (!isList)` is the whole feature,
 *                   and it is invisible unless a case goes to list AND RETURNS.
 *   THE CONTROL     `apply()` writes `sel.value` itself, so the select follows a
 *                   size applied from anywhere — including the saved one at
 *                   mount, which nothing else sets.
 *   THE LIST        Switching TO list renders it there and then, from the last
 *                   payload. Without that the table stays empty until the next
 *                   poll, which is up to a second of a blank page.
 *   THE WRITE       `localStorage` is written by the CHANGE HANDLER only. The
 *                   mount's own `apply(saved)` must NOT write — a page that
 *                   re-saved what it just read would look identical and quietly
 *                   defeat the try/catch that exists for private mode.
 *
 * ---- WHY THIS ONE LIFTS A REGION AND NOT A HANDLER -------------------------
 *
 * The switch is a self-contained IIFE under the `── Interface view` banner, not
 * a socket subscriber. It is lifted whole and RUN, which is what makes the mount
 * behaviour above (the saved size, and the fact that it does not write) testable
 * at all.
 *
 * ---- MUTATIONS THIS KILLS (2026-08-25), ten of ten ------------------------
 *
 *   write data-size even for list            11/25   the whole "return to your
 *                                                    card scale" feature.
 *   never hide the grid                      10/25
 *   never show the list wrapper              20/25
 *   invert the wrapper                       22/25
 *   stop syncing the select                  22/25
 *   do not render on entering list view      10/25
 *   save at mount as well as on change       22/25
 *   default to 'lg' instead of 'sm'           3/25
 *   the failed SAVE escapes the try/catch     2/25
 *   the failed READ escapes the try/catch     3/25
 *
 * The last three are the reason the PRIVATE-MODE cases exist. `let saved = 'sm'`
 * is overwritten unconditionally by the line below it, so with a working
 * `localStorage` that default is DEAD CODE and a mutation of it survives — which
 * is exactly what happened on the first mutation run. The only way to reach it is
 * for storage to THROW, and neither implementation had a case for it.
 *
 * The two try/catch mutants also both killed this gate as a CRASH at first: the
 * run died with a stack trace and no case name. Throws are recorded as DATA now,
 * at mount and per step, so the same mutants read "port threw, live did not" on
 * the cases that provoke it.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/iface-view-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc } = require('./lib/dom-shim.js');
const { makeTree, serialise } = require('./lib/tree-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/iface-view-check.js --freeze
const G = L.golden('iface-view-check');

const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const HANDLER = G.value('HANDLER', () => L.handler(src, 'ifstatus:update', { contains: 'ifaceGrid' }));
const REGION = G.value('REGION', () => L.region(src, {
  banner: '// ── Interface view',
  must: ['IFACE_SIZE_KEY', 'ifaceCardSize', 'isList', 'renderIfaceList'],
  mustNot: ['ifstatus:update', 'Interface type filter'],
}));

const LIVE_FNS = [
  L.line(src, 'function esc('),
  L.whole(src, 'function fmtBytes('),
  L.whole(src, 'function fmtMbps('),
  L.whole(src, 'function portSvg('),
  L.whole(src, 'function renderIfPorts('),
  L.whole(src, 'function renderIfTypes('),
  L.whole(src, 'function ifaceSparkSvg('),
  L.whole(src, 'function ifaceRateRow('),
  L.whole(src, 'function renderIfaceList('),
  L.whole(src, 'function iflSortRows('),
  L.whole(src, 'function ifTypePill('),
  L.whole(src, 'function iflCounter('),
  L.whole(src, 'function iflBytes('),
  L.whole(src, 'function iflLastUp('),
  L.whole(src, 'function iflSetSort('),
  L.whole(src, 'function iflRefreshHeaders('),
].join('\n');

const ELEMENT_NAMES = G.value('ELEMENT_NAMES', () => L.fileScopeEls(src, HANDLER + REGION + ' ifaceGrid ifaceCount ifaceTypeFilter'))
  .map((e) => e.name);

const COVERS = ['ifaceListWrap', 'ifaceCardSize'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

const IDS = ['ifaceCount', 'ifTypeGrid', 'ndWiredCount', 'ifaceGrid', 'ifPortsPanel',
  'ifaceTypeFilter', 'ifaceSelect', 'ifaceListBody', 'ifaceListWrap', 'ifaceCardSize'];

const PAGE = fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', 'page-interfaces.html'), 'utf8');
const GRID_LINE = PAGE.split('\n').find((l) => l.includes('id="ifaceGrid"')) || '';
const SEED = /<div class="iface-grid" id="ifaceGrid">([\s\S]*?)<\/div>\s*$/m.exec(GRID_LINE);
assert.ok(SEED, 'page-interfaces.html no longer declares #ifaceGrid as this gate reads it');

// The SIZES the page actually offers, read from the markup. A size added to the
// select and not to this list would otherwise be silently untested.
//
// SCOPED TO THE SELECT, not to the page. `#ifaceTypeFilter` sits four lines above
// and carries options of its own, so a page-wide sweep collects them as card
// sizes. It happens to come out right today only because that select's one
// option is `value=""` and the pattern needs a non-empty value -- an accident,
// not a rule, and the next option added to any select on this page would be
// driven through the view switch as though it were a card size.
const SIZE_SELECT = /<select id="ifaceCardSize"[\s\S]*?<\/select>/.exec(PAGE);
assert.ok(SIZE_SELECT, 'page-interfaces.html no longer declares the #ifaceCardSize select');
const SIZES = [...SIZE_SELECT[0].matchAll(/<option value="([^"]*)"/g)].map((m) => m[1]);
assert.ok(SIZES.includes('list') && SIZES.length >= 3,
  'page-interfaces.html no longer offers the card sizes this gate drives: ' + SIZES.join(','));
assert.ok(!SIZES.includes(''),
  'the #ifaceCardSize select grew an empty option — the switch has no defined behaviour for one');

const ENTRY = path.join(ROOT, 'testdata', '.ifv-entry.ts');
fs.writeFileSync(ENTRY, "export { initInterfacesPage } from '../web/src/pages/interfaces.js';\n");
const OUT = path.join(ROOT, 'testdata', '.ifv-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

function newDoc() {
  const doc = makeDoc(IDS, {});
  const tree = makeTree();
  const grid = tree.mk('div');
  grid.className = 'iface-grid';
  grid.innerHTML = SEED[1];
  const tbody = tree.mk('tbody');
  const base = doc.getElementById.bind(doc);
  const wrapped = Object.assign(Object.create(doc), {
    createElement: (tag) => tree.mk(tag),
    getElementById: (id) => (id === 'ifaceGrid' ? grid
      : id === 'ifaceListBody' ? tbody : base(id)),
  });
  wrapped.nodes = doc.nodes;
  wrapped.unknown = doc.unknown;
  return { doc: wrapped, grid, tbody };
}

/**
 * Everything the switch is responsible for, plus what it wrote — and whether the
 * step THREW.
 *
 * A step that throws is a real divergence and belongs in the comparison, not in
 * a stack trace. Removing the try/catch around the storage write kills this gate
 * either way, but as a CRASH it reports no case name and no side: the run just
 * dies. Recorded as data, the same mutant reads "port threw, live did not" on the
 * private-mode cases, which is the actual finding.
 */
function snap(doc, grid, tbody, writes, threw) {
  return JSON.stringify({
    threw: threw || undefined,
    gridHidden: !!grid.hidden,
    gridSize: grid.dataset.size,
    wrapHidden: !!doc.nodes.ifaceListWrap.hidden,
    select: doc.nodes.ifaceCardSize.value,
    // Whether the LIST is populated, and with what. Switching to list view is
    // supposed to render it immediately.
    rows: tbody.querySelectorAll('tr[data-iface]').map((r) => r.dataset.iface),
    list: serialise(tbody, { serials: false }),
    writes: writes.slice(),
  }, null, 1);
}

/**
 * The storage both sides read at mount.
 *
 * `PRIVATE` makes every access THROW, which is the only way to reach the
 * `let saved = 'sm'` initialiser: the line below it overwrites `saved`
 * unconditionally, so with a working `localStorage` that default is dead code and
 * a mutation of it survives. That was found by mutating it and watching this gate
 * pass -- the private-mode path is a real branch on both sides and had no case.
 */
const PRIVATE = Symbol('private mode');
function storage(saved, writes) {
  if (saved === PRIVATE) {
    const nope = () => { throw new Error('the user agent denied storage access'); };
    return { getItem: nope, setItem: nope, removeItem: nope };
  }
  return {
    getItem: () => saved,
    setItem: (k, v) => { writes.push(k + '=' + v); },
    removeItem() {},
  };
}

const PAYLOAD = {
  interfaces: [
    { name: 'ether1', type: 'ether', running: true, disabled: false, ips: ['198.51.100.1/24'],
      mac: '02:00:00:00:00:01', mtu: 1500, rxMbps: 12.5, txMbps: 3.25, rxBytes: 1048576,
      txBytes: 2097152, errors: 0, drops: 0, errorsDelta: 0, dropsDelta: 0, linkDowns: 0,
      lastLinkUp: '', comment: '' },
    { name: 'ether2', type: 'ether', running: false, disabled: false, ips: [],
      mac: '02:00:00:00:00:02', mtu: 1500, rxMbps: 0, txMbps: 0, rxBytes: 0, txBytes: 0,
      errors: 0, drops: 0, errorsDelta: 0, dropsDelta: 0, linkDowns: 1, lastLinkUp: '', comment: '' },
  ],
};

/**
 * A run: mount with `saved` in storage, optionally deliver a poll, then choose
 * each size in turn through the SELECT — which is how a viewer does it.
 */
function liveRun(saved, steps, poll) {
  const { doc, grid, tbody } = newDoc();
  const writes = [];
  const ctx = {
    String, Array, Math, Number, Object, JSON, Map, parseInt, parseFloat, isFinite,
    document: doc, setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {}, window: {},
    localStorage: storage(saved, writes),
    __run: null,
  };
  vm.createContext(ctx);
  // MOUNT THROWS ARE DATA TOO, for the same reason step throws are: the switch's
  // read of `localStorage` happens at mount, so a missing try/catch there kills
  // this gate as a CRASH with no case name attached. Captured, it reads
  // "port threw at mount, live did not".
  const mount = (fn) => { try { fn(); return null; } catch (e) { return String(e.message || e); } };
  const mountThrew = mount(() => vm.runInContext([
    LIVE_FNS,
    'function $(id){return document.getElementById(id);}',
    L.fileScopeVars(src, HANDLER + REGION + LIVE_FNS, ELEMENT_NAMES),
    L.whole(src, 'var IF_TYPE_COLOURS'),
    L.whole(src, 'var IF_TYPE_FALLBACKS'),
    L.whole(src, 'var IFL_COLS'),
    L.line(src, 'var _iflSort'),
    'var _ifaceTypeFilter = "";',
    'var _iflOrder = "";',
    L.declare(L.fileScopeEls(src, HANDLER + REGION + ' ifaceGrid ifaceCount ifaceTypeFilter')),
    '__run = function (data) {' + HANDLER + '};',
    // The switch itself, lifted whole and RUN -- its mount is half of what this
    // gate is about.
    REGION,
  ].join('\n'), ctx));
  if (mountThrew) return JSON.stringify([{ mountThrew }], null, 1);
  const out = [snap(doc, grid, tbody, writes)];
  if (poll) { ctx.__run(PAYLOAD); out.push(snap(doc, grid, tbody, writes)); }
  for (const size of steps) {
    doc.nodes.ifaceCardSize.value = size;
    let threw = null;
    try { doc.nodes.ifaceCardSize.fire('change'); } catch (e) { threw = String(e.message || e); }
    out.push(snap(doc, grid, tbody, writes, threw));
  }
  return JSON.stringify(out.map((s) => JSON.parse(s)), null, 1);
}

function portRun(saved, steps, poll) {
  const { doc, grid, tbody } = newDoc();
  const writes = [];
  const handlers = {};
  const prev = { doc: globalThis.document, win: globalThis.window, ls: globalThis.localStorage };
  globalThis.document = doc;
  globalThis.window = {};
  globalThis.localStorage = storage(saved, writes);
  try {
    delete require.cache[require.resolve(OUT)];
    let mountThrew = null;
    try {
      require(OUT).initInterfacesPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
        () => true);
    } catch (e) { mountThrew = String(e.message || e); }
    if (mountThrew) return JSON.stringify([{ mountThrew }], null, 1);
    assert.ok(handlers['ifstatus:update'], 'the port registered no ifstatus:update handler');
    assert.deepEqual([...doc.unknown], ['.iface-list thead'],
      'the port looked up ids this gate does not provide: ' + [...doc.unknown].join(', '));
    const out = [snap(doc, grid, tbody, writes)];
    if (poll) { handlers['ifstatus:update'](PAYLOAD); out.push(snap(doc, grid, tbody, writes)); }
    for (const size of steps) {
      doc.nodes.ifaceCardSize.value = size;
      let threw = null;
      try { doc.nodes.ifaceCardSize.fire('change'); } catch (e) { threw = String(e.message || e); }
      out.push(snap(doc, grid, tbody, writes, threw));
    }
    return JSON.stringify(out.map((s) => JSON.parse(s)), null, 1);
  } finally {
    for (const [k, g] of [['doc', 'document'], ['win', 'window'], ['ls', 'localStorage']]) {
      if (prev[k] === undefined) delete globalThis[g]; else globalThis[g] = prev[k];
    }
  }
}

const CASES = {};
// Every size, chosen from the default mount, with and without data on the page.
for (const s of SIZES) {
  CASES['mount default, choose ' + s] = [null, [s], false];
  CASES['with a poll, choose ' + s] = [null, [s], true];
}
// Every size as the SAVED one, which only the mount's `apply` can reach.
for (const s of SIZES) CASES['mounted with ' + s + ' saved'] = [s, [], true];

Object.assign(CASES, {
  // THE FEATURE: go to list and come back. `data-size` must still hold the size
  // that was chosen BEFORE list view, not be reset.
  'lg, then list, then back to lg': [null, ['lg', 'list', 'lg'], true],
  'lg, then list, then sm': [null, ['lg', 'list', 'sm'], true],
  'mounted in list, then out': ['list', ['lg'], true],
  'mounted in list, straight to another list': ['list', ['list'], true],
  'every size in turn': [null, SIZES.slice(), true],
  'the same size twice': [null, ['lg', 'lg'], true],
  'list twice': [null, ['list', 'list'], true],
  // A saved value the select does not offer. Nothing validates it, so both sides
  // must be wrong the SAME way -- which is the only thing a port can promise.
  'a saved size that is not an option': ['xl', [], true],
  'an empty saved value falls back': ['', [], true],
  // PRIVATE MODE: every storage access throws. The mount must still land on the
  // default, and a size chosen afterwards must still apply even though saving it
  // fails -- the try/catch is there so the switch keeps working, not so it fails
  // quietly.
  'private mode, mounting': [PRIVATE, [], true],
  'private mode, then choosing a size': [PRIVATE, ['lg'], true],
  'private mode, then list and back': [PRIVATE, ['lg', 'list', 'lg'], true],
  // Switching to list BEFORE any poll: there is nothing to render.
  'list with no data at all': [null, ['list'], false],
});

let bad = 0, checked = 0;
for (const [name, [saved, steps, poll]] of Object.entries(CASES)) {
  const a = G.live(name, () => liveRun(saved, steps, poll));
  const b = portRun(saved, steps, poll);
  checked++;
  if (a === b) continue;
  bad++;
  if (bad <= 3) {
    shout('DIFF [' + name + ']');
    const A = JSON.parse(a), B = JSON.parse(b);
    for (let i = 0; i < Math.max(A.length, B.length); i++) {
      const x = JSON.stringify(A[i]), y = JSON.stringify(B[i]);
      if (x !== y) {
        shout('  step ' + i + '\n    live: ' + String(x).slice(0, 420) +
          '\n    port: ' + String(y).slice(0, 420));
      }
    }
  }
}

// ---- BELIEVABILITY ---------------------------------------------------------
//
// Two switches that did nothing would agree perfectly, so each promise in the
// header is asserted against the LIVE side ALONE.
{
  const mount = JSON.parse(G.live('auto:4', () => liveRun(null, [], false)));
  assert.equal(mount[0].select, 'sm', 'the LIVE mount did not push the saved size into the select');
  assert.deepEqual(mount[0].writes, [],
    'the LIVE mount WROTE to localStorage — it is only supposed to read there, and a page ' +
    'that re-saves what it just read defeats the try/catch that exists for private mode');

  const toList = JSON.parse(G.live('auto:3', () => liveRun(null, ['lg', 'list'], true)));
  assert.equal(toList[2].gridSize, 'lg',
    'the LIVE switch overwrote data-size on the way into list view — that single `if (!isList)` ' +
    'is the whole "return to your card scale" feature');
  assert.ok(toList[3].gridHidden && !toList[3].wrapHidden,
    'the LIVE switch did not swap the containers for list view');
  assert.ok(toList[3].rows.length > 0,
    'the LIVE switch left the table EMPTY on entering list view — it is supposed to render ' +
    'from the last payload rather than wait for the next poll');
  assert.ok(toList[3].writes.length > 0, 'the LIVE change handler wrote nothing to localStorage');

  const priv = JSON.parse(G.live('auto:2', () => liveRun(PRIVATE, ['lg'], true)));
  assert.equal(priv[0].select, 'sm',
    'the LIVE mount did not fall back to the default size when storage threw');
  // [mount, poll, 'lg'] -- three snapshots, so the chosen size is index 2.
  assert.equal(priv[2].gridSize, 'lg',
    'the LIVE switch stopped working in private mode — the try/catch is there so it keeps ' +
    'working when the save fails, not so it fails quietly');

  const back = JSON.parse(G.live('auto:1', () => liveRun(null, ['lg', 'list', 'lg'], true)));
  assert.equal(back[4].gridSize, 'lg', 'the LIVE size did not survive a trip through list view');
  assert.ok(!back[4].gridHidden && back[4].wrapHidden,
    'the LIVE switch did not swap the containers back');
}

fs.rmSync(OUT, { force: true });
if (bad) {
  shout('\niface-view-check: ' + bad + ' of ' + checked + ' cases differ');
  process.exit(1);
}
console.log('iface-view-check: ' + checked + ' switch sequences identical (' +
  SIZES.length + ' sizes, container pair, saved size and the storage write)');
