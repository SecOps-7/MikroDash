'use strict';
/**
 * The CAPsMAN page, live against ported. Built on `tools/lib/lift.js`.
 *
 * ── FOUR EMPTY STATES, AND THE FOURTH ARRIVED ON 2026-08-25 ─────────────────
 *
 * "No CAPs" is four different situations, and the page now distinguishes all
 * four:
 *
 *   - the legacy wireless package, which has no CAPsMAN at all
 *   - this router IS a CAP, managed from somewhere else
 *   - a manager with nothing provisioned yet
 *   - a search that matched none of the CAPs it has
 *
 * The last was missing and rendered as "No CAPs are connected to this manager.",
 * so a viewer whose own filter was hiding every row was told the manager is
 * empty — a statement about the router rather than about what they typed.
 * Reported as ToDo #20 and pinned here AS IT WAS, with the note that the port
 * would follow if upstream fixed it.
 *
 * Upstream fixed it, this gate's own assertion fired and said so by name, and
 * the port followed the same day. That is the third entry to cross that way
 * (#16 and #17 were the others), and the reason the pin is written as an
 * assertion rather than as a comment.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/capsman-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/capsman-page-check.js --freeze
const G = L.golden('capsman-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = L.region(src, {
  banner: '/* ── CAPsMAN page',
  must: ['capsmanTable', 'capSumMode'],
  mustNot: ['DNS page', 'Bridges page', 'Router Users page', 'backupsPage'],
});
// ── THE CONFIGURATION CARD IS A SECOND IIFE ────────────────────────────────
//
// CAPsMAN is built from two blocks — the page and the configuration card — and
// this gate lifted only the first. That is why `capsCfgTabBar` was not in `IDS`
// and driving the strip threw: the bar belongs to a region nothing here had
// read. `live-renderer.js` already records the same split.
const cfg = L.region(src, {
  banner: '// ── CAPsMAN configuration',
  must: ['capsCfgTabBar', 'capsAddSlot', 'mikrodash:resmount'],
  mustNot: ['Bridges page', 'DNS page', 'Router Users page'],
});
// `L.idsFor` finds `$('id')`; the card uses `document.getElementById('id')`
// directly, so it returns nothing for that region. Read both spellings rather
// than listing the ids here — a table added to the card is then driven without
// anyone remembering to add it.
// THREE SPELLINGS, because the card uses all three: `getElementById` for the
// bar, its own `el(...)` helper for everything else, and `$` nowhere. Reading
// only the first two found ONE id, and the Add slot and the CAP note were then
// absent from the document — so mutating either compared `null` against `null`
// and survived. Measured.
// FROZEN AT THE LIFT, not downstream. The asserts below check that the lift
// still finds the card's ids — with the reference absent the lifters return
// stubs, so an unfrozen CFG_IDS is empty and those asserts fire for a reason
// that has nothing to do with the card. Freezing here keeps them meaningful:
// they now also catch a corrupted golden.
const CFG_IDS = G.value('CFG_IDS', () => [...new Set(
  [...cfg.matchAll(/(?:getElementById|el|\$)\('([A-Za-z0-9_-]+)'\)/g)].map((m) => m[1]))]);
assert.ok(CFG_IDS.includes('capsCfgTabBar'),
  'the configuration card no longer names its tab bar — the lift has broken');
for (const need of ['capsAddSlot', 'capsCfgNote']) {
  assert.ok(CFG_IDS.includes(need),
    'the card no longer names #' + need + ' — without it that element is absent from the ' +
    'document and every mutation to it compares null against null');
}
const IDS = G.value('IDS', () => [...new Set([...L.idsFor(src, iife), ...CFG_IDS])]);

// ── THE CONFIGURATION CARD'S TAB STRIP ─────────────────────────────────────
//
// Five tabs over five tables, and nothing drove them: `capsCfgTabBar`,
// `capsCfgNote` and `capsAddSlot` were all reported uncovered while this gate
// compared the CAP table beside them.
//
// The strip does more than switch a panel. It re-points the ADD SLOT at the
// resource the visible table belongs to and announces `mikrodash:resmount`, so
// a switch that forgot it leaves the Add button creating the previous tab's
// resource — the same bug this port had page-wide until the resmount listener
// was ported earlier today.
const CAP_TABS = ['provisioning', 'configuration', 'security', 'channel', 'datapath'];
// A SIXTH button carrying a value no tab uses. `setTab` returns early on
// anything `CAPS_RES` does not know, and without a bogus value that guard is
// unreachable — nothing else can produce one.
const TAB_BUTTONS = CAP_TABS.concat(['nonsense']);
const PANELS = CAP_TABS.map((t) => 'capstab-' + t);
const TAB_DOM = {
  query: { '.cap-row': [] },
  elementQuery: {
    capsCfgTabBar: { '.stab': TAB_BUTTONS, '[data-capstab]': '.stab' },
  },
  queryAttr: { '.stab': 'data-capstab' },
};
if (process.argv.includes('--ids')) {
  console.log(JSON.stringify(IDS.concat(PANELS))); process.exit(0);
}

const ENTRY = path.join(ROOT, 'testdata', '.cm-entry.ts');
fs.writeFileSync(ENTRY, "export { initCapsmanPage } from '../web/src/pages/capsman.js';\n");
const OUT = path.join(ROOT, 'testdata', '.cm-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const snap = (doc) => {
  const n = doc.nodes;
  const out = {};
  for (const id of IDS.slice().sort()) {
    out[id] = n[id] ? { h: n[id].innerHTML, t: n[id].textContent, c: n[id].className,
      d: n[id].style && n[id].style.display } : null;
  }
  // ── WHICH CONFIG TAB IS SHOWING, AND WHAT ADD WOULD CREATE ─────────────
  //
  // `hidden` on each panel, the lit button, and the Add slot's `data-res-add`.
  // That last one is the whole point of the switch: it decides which resource
  // the Add button creates, and no table's markup shows it.
  const stabs = doc.nodes.capsCfgTabBar.querySelectorAll('.stab');
  out.__tabs = {
    lit: stabs.filter((b) => b.classList.contains('active'))
      .map((b) => b.getAttribute('data-capstab')),
    aria: stabs.map((b) => [b.getAttribute('data-capstab'),
      b.attributes && b.attributes['aria-selected']]),
    hidden: PANELS.map((id) => [id, n[id] ? n[id].hidden : null]),
    addSlot: n.capsAddSlot ? (n.capsAddSlot.attributes || {})['data-res-add'] : null,
    resmounts: doc.__resmounts || 0,
  };
  return JSON.stringify(out);
};

function drive(doc, fire, script, o) {
  if (o.search) doc.nodes.capsmanSearch.value = o.search;
  for (const [ev, p] of script) fire(ev, p);
  if (o.search) doc.nodes.capsmanSearch.fire('input');
  for (const i of o.clicks || []) {
    const cells = doc.nodes.capsmanThead.querySelectorAll('th');
    if (!cells[i]) throw new Error('no header cell at index ' + i);
    cells[i].click();
  }
  // The config tabs, pressed through the bar's delegated listener.
  for (const t of o.tabs || []) {
    const btn = doc.nodes.capsCfgTabBar.querySelectorAll('.stab')
      .find((b) => b.getAttribute('data-capstab') === t);
    if (!btn) throw new Error('no config tab button for ' + t);
    btn.closest = (sel) => (sel === '[data-capstab]' ? btn : null);
    doc.nodes.capsCfgTabBar.fire('click', { target: btn });
  }
}

function liveRun(script, opts) {
  const o = opts || {};
  const doc = makeDoc([...IDS, ...PANELS], TAB_DOM);
  const handlers = {};
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, parseFloat, isFinite,
    document: doc,
    // ── A LIST PER EVENT, NOT ONE HANDLER ─────────────────────────────────
    //
    // The page and the configuration card BOTH subscribe `capsman:update`, and a
    // map of ev -> cb silently keeps the last one — so lifting the card made the
    // page's own handler disappear and the summary counters went blank.
    // `live-renderer.js` records the identical trap for this identical page.
    socket: { on: (ev, fn) => { (handlers[ev] = handlers[ev] || []).push(fn); }, emit() {} },
    // The card ANNOUNCES `mikrodash:resmount` when the Add slot changes
    // resource, so both sides need a CustomEvent to construct and a document
    // that accepts the dispatch. The count is compared: a switch that stopped
    // announcing would leave the Add button rebuilt from the previous tab.
    CustomEvent: function (name) { return { type: name }; },
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {}, window: {},
    requestAnimationFrame: (fn) => { fn(); return 0; },
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function fmtMbps('),
    L.whole(src, 'function _sortMul('),
    L.whole(src, 'function _renderSortHeader('),
    'function $(id){return document.getElementById(id);}',
    'function pageVisible(){return true;}',
    'function _debounce(fn){return fn;}',
    L.declare(L.fileScopeEls(src, iife)),
    '(function () {' + iife + '})();',
    // The card, evaluated in the SAME context and against the same document, as
    // the served page has them. WRAPPED, because the region's own guard is
    // `if (!bar) return;` — at top level that is an illegal return, and the
    // whole block then fails to parse rather than failing to run.
    '(function () {' + cfg + '}());',
  ].join('\n'), ctx);
  if (!handlers['capsman:update'] || handlers['capsman:update'].length < 2) {
    throw new Error('the live page and its configuration card did not BOTH subscribe ' +
      'capsman:update (' + ((handlers['capsman:update'] || []).length) + ' handler(s)); ids the ' +
      'lift wanted and this gate does not provide: ' + ([...doc.unknown].join(', ') || 'none'));
  }
  drive(doc, (ev, p) => {
    if (!handlers[ev] || !handlers[ev].length) throw new Error('nothing subscribes ' + ev);
    for (const fn of handlers[ev].slice()) fn(p);
  }, script, o);
  return snap(doc);
}

function portRun(script, opts) {
  const o = opts || {};
  const doc = makeDoc([...IDS, ...PANELS], TAB_DOM);
  const handlers = {};
  const prevWin = globalThis.window;
  const prevST = globalThis.setTimeout;
  const prevRaf = globalThis.requestAnimationFrame;
  globalThis.window = {};
  globalThis.setTimeout = ((fn) => { fn(); return 0; });
  globalThis.requestAnimationFrame = ((fn) => { fn(); return 0; });
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initCapsmanPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
      drive(doc, (ev, p) => {
        if (!handlers[ev]) throw new Error('the port does not subscribe ' + ev);
        handlers[ev](p);
      }, script, o);
      return snap(doc);
    });
  } finally {
    globalThis.requestAnimationFrame = prevRaf;
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
        String(x).slice(0, 340), String(y).slice(0, 340));
    }
  }
}

const C = (o) => Object.assign({
  id: '*1', identity: 'cap-hall', boardName: 'cAP ax', serial: 'ABC123',
  radios: 2, clients: 5, version: '7.24', state: 'running', address: '198.51.100.5',
}, o);
const P = (o) => Object.assign({
  caps: [], available: true, role: 'manager', radios: 0, clients: 0,
}, o);
const upd = (o) => [['capsman:update', P(o)]];

const CASES = {
  // ── the four empty states ────────────────────────────────────────────────
  'a manager with nothing provisioned': [upd({ caps: [], role: 'manager' }), {}],
  'this router is a CAP': [upd({ caps: [], role: 'cap' }), {}],
  'the legacy wireless package': [upd({ caps: [], available: false }), {}],
  'a search that matched none of its CAPs': [upd({ caps: [C({})] }), { search: 'zzzz' }],
  // ── CAPs ─────────────────────────────────────────────────────────────────
  'one cap': [upd({ caps: [C({})] }), {}],
  'several caps': [upd({ caps: [C({}), C({ id: '*2', identity: 'cap-office' })] }), {}],
  'a cap with no clients': [upd({ caps: [C({ clients: 0 })] }), {}],
  'a cap with one radio': [upd({ caps: [C({ radios: 1 })] }), {}],
  'a cap with no serial': [upd({ caps: [C({ serial: '' })] }), {}],
  'a cap with no board name': [upd({ caps: [C({ boardName: '' })] }), {}],
  'a cap with no address': [upd({ caps: [C({ address: '' })] }), {}],
  'a cap that is not running': [upd({ caps: [C({ state: 'disabled' })] }), {}],
  'a cap with no version': [upd({ caps: [C({ version: '' })] }), {}],
  // ── the summary ──────────────────────────────────────────────────────────
  'summary across several caps': [upd({
    caps: [C({ radios: 2, clients: 5 }), C({ id: '*2', identity: 'b', radios: 1, clients: 3 })],
    radios: 3, clients: 8 }), {}],
  'summary with zero everything': [upd({ caps: [], radios: 0, clients: 0 }), {}],
  'the mode reads as cap': [upd({ role: 'cap' }), {}],
  'the mode reads as manager': [upd({ role: 'manager' }), {}],
  'an unknown role': [upd({ role: 'both' }), {}],
  // ── search ───────────────────────────────────────────────────────────────
  'search by identity': [upd({ caps: [C({}), C({ id: '*2', identity: 'other' })] }), { search: 'hall' }],
  'search by board name': [upd({ caps: [C({}), C({ id: '*2', boardName: 'hAP' })] }), { search: 'hap' }],
  'search by serial': [upd({ caps: [C({})] }), { search: 'abc' }],
  'search is lowercased': [upd({ caps: [C({})] }), { search: 'CAP-HALL' }],
  // ── escaping and sorting ─────────────────────────────────────────────────
  'markup in an identity': [upd({ caps: [C({ identity: '<img src=x>' })] }), {}],
  'a quote in a board name': [upd({ caps: [C({ boardName: 'a"b' })] }), {}],
  'sorted by the first column': [upd({ caps: [C({ identity: 'z' }), C({ id: '*2', identity: 'a' })] }), { clicks: [0] }],
  'first column descending': [upd({ caps: [C({ identity: 'z' }), C({ id: '*2', identity: 'a' })] }), { clicks: [0, 0] }],
  // A router switch clears the page rather than leaving another router's CAPs.
  'a router switch clears': [[['capsman:update', P({ caps: [C({})] })], ['router:switched', {}]], {}],
  // ── THE CONFIGURATION CARD'S TABS ────────────────────────────────────────
  //
  // Five tabs over five tables. The switch does three things a table's markup
  // cannot show: it hides four panels, it re-points the ADD SLOT at the resource
  // the visible table belongs to, and it announces `mikrodash:resmount` so the
  // Add button is rebuilt. A switch that forgot the slot leaves Add creating the
  // PREVIOUS tab's resource — pressing it on Security would make a provisioning
  // rule.
  'switch to configuration': [upd({ caps: [], role: 'manager' }), { tabs: ['configuration'] }],
  'switch to security': [upd({ caps: [], role: 'manager' }), { tabs: ['security'] }],
  'switch to channel': [upd({ caps: [], role: 'manager' }), { tabs: ['channel'] }],
  'switch to datapath': [upd({ caps: [], role: 'manager' }), { tabs: ['datapath'] }],
  'every config tab in turn': [upd({ caps: [], role: 'manager' }), { tabs: CAP_TABS.slice() }],
  'switch away and back': [upd({ caps: [], role: 'manager' }), { tabs: ['security', 'provisioning'] }],
  'the same config tab twice': [upd({ caps: [], role: 'manager' }), { tabs: ['security', 'security'] }],
  // The guard: a button whose value no tab uses must change NOTHING.
  'a config button with an unknown value': [upd({ caps: [], role: 'manager' }),
    { tabs: ['security', 'nonsense'] }],

};

for (const [name, [script, opts]] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live(name, () => liveRun(script, opts)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(script, opts); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(G.live('believability:one-cap', () => liveRun(upd({ caps: [C({})] }), {})));
  assert.match(s.capsmanTable.h, /cap-hall/, 'the live table rendered no CAP');
  assert.equal(s.capsmanBadge.t, '1', 'the badge is ' + s.capsmanBadge.t);
  assert.match(s.capsmanBadge.c, /active-blue/, 'a non-empty table left the badge inactive');
}
{
  // THE FOUR EMPTY STATES ARE FOUR DIFFERENT SENTENCES. Collapsing any pair
  // sends an operator hunting for a fault that is not there.
  const mgr = JSON.parse(G.live('believability:role-manager', () => liveRun(upd({ caps: [], role: 'manager' }), {}))).capsmanTable.h;
  const cap = JSON.parse(G.live('believability:role-cap', () => liveRun(upd({ caps: [], role: 'cap' }), {}))).capsmanTable.h;
  const legacy = JSON.parse(G.live('believability:legacy', () => liveRun(upd({ caps: [], available: false }), {}))).capsmanTable.h;
  const nomatch = JSON.parse(G.live('believability:no-match', () => liveRun(upd({ caps: [C({})] }), { search: 'zzzz' }))).capsmanTable.h;
  // THREE, NOT FOUR — and the missing one is a defect, reported as ToDo #20.
  //
  // A search that matches none of the CAPs this manager HAS renders the same
  // sentence as a manager with none at all: "No CAPs are connected to this
  // manager." So a viewer whose own filter is hiding every row is told the
  // manager is empty. The Firewall page distinguishes exactly this ("No rules"
  // versus "No rules match search") and so does PPP, which is how the gap was
  // noticed here.
  //
  // FOUR NOW. Upstream added the fourth sentence on 2026-08-25 and this
  // assertion fired, exactly as the note below it said it would — the port
  // followed the same day. The mechanism that carried ToDo #16 and #17 across
  // has now carried #20.
  assert.equal(new Set([mgr, cap, legacy, nomatch]).size, 4,
    'the four distinguished empty states collapsed: ' +
    JSON.stringify([mgr, cap, legacy, nomatch]));
  assert.notEqual(nomatch, mgr,
    'a search that matches nothing says the manager is empty again — that is ToDo #20 ' +
    'and this gate and the port must follow it');
  assert.match(legacy, /legacy wireless/, 'the legacy-package state is wrong');
  assert.match(cap, /is a CAP/, 'the this-is-a-CAP state is wrong');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('capsman-page-check: %d cases identical', checked);
