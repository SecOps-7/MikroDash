'use strict';
/**
 * The FIREWALL rule table, live against ported.
 *
 * `fw-tabs-check` covers the tab bar; `element-coverage-audit` reported the table
 * itself, the chain count and the search box as uncovered.
 *
 * ── I EXPECTED THIS TO BE NODE-BUILDING AND IT IS NOT ───────────────────────
 *
 * The Interfaces page turned out to diff nodes in both its renderers, and I
 * predicted the Firewall table would be a third. It is not: the full render is a
 * string. Checking beat repeating the guess, and this gate exists because of it.
 *
 * There IS a node path here — a fast path that updates the packet and byte cells
 * in place through `row.querySelector('.fw-pkt')`, so the flash animation is not
 * restarted on every poll. It is guarded by an id match and falls through to the
 * full rebuild whenever the rule set changes. The REBUILD is what this gate
 * drives; the in-place counter update needs real nodes and is out of scope for
 * the same reason as the Interfaces list.
 *
 * WHAT IT CANNOT SEE: the in-place counter path, drag reordering, layout — and
 * two specific things worth naming, because an unnamed gap gets rediscovered:
 *
 *   - `fw-noedit` USED to be a surviving mutation: it is toggled on
 *     `firewallTable.parentElement`, the shim had no parent chain, so the toggle
 *     happened on neither side and two empty class lists compared equal. The
 *     note here said faking a parent would be inventing structure — but the
 *     structure is written down. `web/src/ui/page-firewall.html` is the markup
 *     this port serves, and it wraps `#firewallTable` in a `<table>`. The shim
 *     takes a parent from `opts.parents`, this gate reads it out of that file
 *     (and FAILS if the page stops wrapping the table), and the class list is
 *     part of the comparison. Inverting, dropping or misspelling the toggle all
 *     die now, and a corpus that stopped exercising both edit states fails on
 *     its own check rather than going quietly vacuous.
 *   - Making the dstPort search case-insensitive survives, and is equivalent
 *     rather than untested: RouterOS ports are digits and ranges, so lowercasing
 *     cannot change the match. It is the one field searched case-sensitively,
 *     and the corpus pins that it is searched at all.

 *
 *   MIKRODASH_SRC=../MikroDash node tools/firewall-table-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/firewall-table-check.js --freeze
const G = L.golden('firewall-table-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const RENDER = G.value('RENDER', () => L.whole(src, 'function renderFirewallTab('));
assert.ok(RENDER.includes('firewallTable'), 'the lifted renderer is not the firewall table');

// `fwChainCount` joined `firewallTable` on 2026-08-25. It was one of the three
// this gate's PARTIAL entry in element-coverage-audit named as uncovered, and it
// is worth comparing rather than recording: the chain bars EXCLUDE DISABLED
// RULES, because "a chain's weight is about what actually runs" — a rule that
// only shows when a payload carries disabled rows, which the corpus now does.
// `fwActionList` and the four per-table counts joined on 2026-08-25. They are
// all written by `fwUpdateSummary`, which ENDS by calling `fwUpdateChainCount` —
// on both sides, in the same order — so driving the outer function drives every
// one of them and the harness does not have to know the order.
const COMPARED = ['firewallTable', 'fwChainCount', 'fwActionList',
  'fwCntFilter', 'fwCntFilterDis', 'fwCntNat', 'fwCntNatDis',
  'fwCntMangle', 'fwCntMangleDis', 'fwCntRaw', 'fwCntRawDis'];
// COMPARED plus the SEARCH BOX, which every case sets and whose effect is in
// the compared table — a driven input is covered when the gate can tell that the
// page stopped reading it.
if (process.argv.includes('--ids')) {
  console.log(JSON.stringify(COMPARED.concat(['fwSearch']))); process.exit(0);
}

const IDS = [...new Set([...COMPARED, 'fwSearch', 'fwChainCount', 'fwActionList',
  'fwTabBar', 'fwCount'])];

const ENTRY = path.join(ROOT, 'testdata', '.fw-entry.ts');
fs.writeFileSync(ENTRY, "export { initFirewallPage } from '../web/src/pages/firewall.js';\n");
const OUT = path.join(ROOT, 'testdata', '.fw-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

/**
 * `#firewallTable`'s parent, as `web/src/ui/page-firewall.html` describes it.
 *
 * Read from the extracted markup rather than asserted, so a page whose table
 * stops being wrapped fails here instead of quietly keeping a parent this file
 * remembers. The class the port toggles on it — `fw-noedit` — is what hides the
 * write controls from a read-only account.
 */
const PAGE = fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', 'page-firewall.html'), 'utf8');
assert.match(PAGE, /<table[^>]*>\s*<thead>[\s\S]{0,900}?<tbody id="firewallTable"/,
  'page-firewall.html no longer wraps #firewallTable in a <table> — the parent this gate ' +
  'gives the shim is not the one the page has');
const PARENTS = { parents: { firewallTable: 'fwTableEl' } };

const snap = (doc) => {
  const n = doc.nodes;
  const out = {};
  for (const id of COMPARED) out[id] = n[id] ? { h: n[id].innerHTML, t: n[id].textContent } : null;
  // The parent's classes are part of the comparison now — see PARENTS.
  const p = doc.parents.fwTableEl;
  out.__parent = p ? [...p.classList._s].sort() : null;
  return JSON.stringify(out);
};

const LIVE_FNS = [
  // fwUpdateSummary calls fwUpdateChainCount, so both are lifted and only the
  // outer one is driven.
  L.whole(src, 'function fwUpdateChainCount('),
  L.whole(src, 'function fwUpdateSummary('),
  L.line(src, 'function esc('),
  L.whole(src, 'function resRow('),
  L.whole(src, 'function fmtBytes('),
  L.whole(src, 'function actionBadge('),
  L.whole(src, 'function fwIdentity('),
  RENDER,
].join('\n');

function liveRun(data, opts) {
  const o = opts || {};
  const doc = makeDoc(IDS, PARENTS);
  doc.nodes.fwSearch.value = o.search || '';
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, isFinite,
    document: Object.assign(doc, { body: { classList: { contains: () => false } } }),
    setTimeout: () => 0, clearTimeout: () => {}, window: {},
    requestAnimationFrame: (fn) => { fn(); return 0; },
    __run: null, __grant: null,
  };
  vm.createContext(ctx);
  vm.runInContext([
    LIVE_FNS,
    'function $(id){return document.getElementById(id);}',
    L.fileScopeVars(src, RENDER, ['firewallTable']),
    // A multi-line map, which `fileScopeVars` will not capture — lifted whole
    // rather than retyped, because it names the resource each tab writes to.
    L.whole(src, 'var FW_RES'),
    L.declare(L.fileScopeEls(src, RENDER)),
    // The live names are `fwTab` and `fwData`, declared together at app.js:250.
    // `fileScopeVars` will not capture that line — two declarators in one
    // statement is not the literal form it accepts — so the caller supplies it,
    // with the DEFAULT the source declares rather than a guess.
    'var fwTab = ' + JSON.stringify(o.tab || 'filter') + ', fwData = {};',
    // The live app does not store the raw box contents: it trims and lowercases
    // when reading the input (app.js:2477). Setting `_fwSearch` raw here made a
    // case with 'ACCEPT' differ, which was the DRIVER bypassing a transformation
    // the page performs — not the port disagreeing. The expression is taken from
    // the source rather than reimplemented.
    'var fwSearchEl = $("fwSearch");',
    L.line(src, '  _fwSearch=(fwSearchEl.value').trim(),
    // Write permission arrives on `res:schema`, and without it every row renders
    // read-only — which made `canMove = mayWrite && !search` unreachable and a
    // mutation removing the search half survive. Set the way the page sets it.
    '__grant = function (k) { _fwWritable[k] = true; };',
    // The chain count is a SECOND writer driven by the same payload, so the
    // harness must call it too — the page does.
    '__run = function (d) { fwData = d; renderFirewallTab(); fwUpdateSummary(d); };',
  ].join('\n'), ctx);
  if (o.writable) ctx.__grant('fwFilter');
  ctx.__run(data);
  return snap(doc);
}

function portRun(data, opts) {
  const o = opts || {};
  const doc = makeDoc(IDS, PARENTS);
  doc.body = { classList: { contains: () => false } };
  if (o.search) doc.nodes.fwSearch.value = o.search;
  const handlers = {};
  const prevWin = globalThis.window;
  const prevRaf = globalThis.requestAnimationFrame;
  const prevST = globalThis.setTimeout;
  globalThis.window = {};
  // The port's search listener is DEBOUNCED. The live side is driven by setting
  // `_fwSearch` directly, so leaving the port's timer real means it never applies
  // the filter and three cases "differ" — the harness being asymmetric, exactly
  // as the Logs gate was.
  globalThis.setTimeout = ((fn) => { fn(); return 0; });
  // Run the frame immediately, matching the live sandbox. Deferring it would
  // snapshot before the render and compare two empty tables — the failure the
  // schedules gate already made once with a promise.
  globalThis.requestAnimationFrame = ((fn) => { fn(); return 0; });
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initFirewallPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
      if (!handlers['firewall:update']) throw new Error('the port registered no firewall:update handler');
      if (o.writable && handlers['res:schema']) {
        handlers['res:schema']({ key: 'fwFilter', permitted: true });
      }
      if (o.search) doc.nodes.fwSearch.fire('input');
      handlers['firewall:update'](data);
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
        String(x).slice(0, 400), String(y).slice(0, 400));
    }
  }
}

const R = (o) => Object.assign({
  id: '*1', chain: 'forward', action: 'accept', protocol: 'tcp',
  srcAddress: '198.51.100.0/24', dstAddress: '', dstPort: '443',
  // `deltaBytes` was here and no implementation has ever read it; the row
  // carries `deltaPackets` alone. Removed rather than renamed — there is no
  // byte-delta in this payload to rename it to.
  packets: 1234, bytes: 1048576, deltaPackets: 0,
  disabled: false, dynamic: false, comment: '', log: false,
}, o);
const D = (o) => Object.assign({ filter: [], nat: [], mangle: [], raw: [] }, o);

const CASES = {
  'no rules': [D({}), {}],
  'one rule': [D({ filter: [R({})] }), {}],
  // ── the CHAIN COUNT's own cases ──────────────────────────────────────────
  //
  // Two mutations survived until these existed, and both because every earlier
  // payload had ONE chain in ONE table: "the chains are not sorted by weight"
  // and "only the filter table is counted" were invisible against a single bar
  // from a single list. A count needs something to count.
  'chains across ALL FOUR tables, in weight order that is not input order':
    [D({
      filter: [R({ chain: 'input' })],
      nat: [R({ id: '*2', chain: 'srcnat' }), R({ id: '*3', chain: 'srcnat' })],
      mangle: [R({ id: '*4', chain: 'prerouting' }), R({ id: '*5', chain: 'prerouting' }),
               R({ id: '*6', chain: 'prerouting' })],
      raw: [R({ id: '*7', chain: 'prerouting' })],
    }), {}],
  // ── the ACTION BREAKDOWN's own cases ─────────────────────────────────────
  //
  // Three mutations survived until these existed — the list is capped at SEVEN
  // and sorted by count, and neither rule is visible against a corpus whose
  // payloads carry two actions in the order they were written.
  'NINE distinct actions, so the cap at seven bites':
    [D({ filter: ['accept', 'drop', 'reject', 'log', 'jump', 'return', 'passthrough', 'tarpit', 'fasttrack-connection']
      .map((a, i) => R({ id: '*' + (i + 1), action: a, chain: 'input' })) }), {}],
  'actions whose COUNTS put them out of input order':
    [D({ filter: [
      R({ id: '*1', action: 'log' }),
      R({ id: '*2', action: 'drop' }), R({ id: '*3', action: 'drop' }), R({ id: '*4', action: 'drop' }),
      R({ id: '*5', action: 'accept' }), R({ id: '*6', action: 'accept' }),
    ] }), {}],
  'a rule with NO action falls back to ?':
    [D({ filter: [R({ action: '' })] }), {}],
  'a DISABLED rule does not add weight':
    [D({
      filter: [R({ chain: 'input' }), R({ id: '*2', chain: 'forward', disabled: true }),
               R({ id: '*3', chain: 'forward' })],
    }), {}],
  'every rule disabled leaves no chains at all':
    [D({ filter: [R({ chain: 'input', disabled: true })] }), {}],
  'several rules': [D({ filter: [R({}), R({ id: '*2', action: 'drop' })] }), {}],
  // Every action the badge colours differently.
  'action accept': [D({ filter: [R({ action: 'accept' })] }), {}],
  'action drop': [D({ filter: [R({ action: 'drop' })] }), {}],
  'action reject': [D({ filter: [R({ action: 'reject' })] }), {}],
  'action log': [D({ filter: [R({ action: 'log' })] }), {}],
  'action masquerade': [D({ filter: [R({ action: 'masquerade' })] }), {}],
  'action dst-nat': [D({ filter: [R({ action: 'dst-nat' })] }), {}],
  'action passthrough': [D({ filter: [R({ action: 'passthrough' })] }), {}],
  'an unknown action': [D({ filter: [R({ action: 'wormhole' })] }), {}],
  // Rule state.
  'a disabled rule': [D({ filter: [R({ disabled: true })] }), {}],
  'a dynamic rule': [D({ filter: [R({ dynamic: true })] }), {}],
  'a logged rule': [D({ filter: [R({ log: true })] }), {}],
  'a rule with a comment': [D({ filter: [R({ comment: 'allow https' })] }), {}],
  // Counters: zero is a real reading.
  'zero counters': [D({ filter: [R({ packets: 0, bytes: 0 })] }), {}],
  'a delta on the packet counter': [D({ filter: [R({ deltaPackets: 5 })] }), {}],
  'large counters': [D({ filter: [R({ packets: 1234567, bytes: 9876543210 })] }), {}],
  // Address and port fields.
  'no source address': [D({ filter: [R({ srcAddress: '' })] }), {}],
  'both addresses': [D({ filter: [R({ dstAddress: '203.0.113.0/24' })] }), {}],
  'no port': [D({ filter: [R({ dstPort: '' })] }), {}],
  'a port range': [D({ filter: [R({ dstPort: '80-443' })] }), {}],
  'no protocol': [D({ filter: [R({ protocol: '' })] }), {}],
  // Search — and the empty state that says WHICH kind of empty it is.
  'search matching a chain': [D({ filter: [R({}), R({ id: '*2', chain: 'input' })] }), { search: 'input' }],
  'search matching an action': [D({ filter: [R({})] }), { search: 'accept' }],
  'search matching an address': [D({ filter: [R({})] }), { search: '198.51' }],
  'search matching a comment': [D({ filter: [R({ comment: 'allow https' })] }), { search: 'https' }],
  'search matching a port': [D({ filter: [R({})] }), { search: '443' }],
  'search matching NOTHING says so differently': [D({ filter: [R({})] }), { search: 'zzzz' }],
  'search is lowercased': [D({ filter: [R({})] }), { search: 'ACCEPT' }],
  // Escaping.
  'markup in a comment': [D({ filter: [R({ comment: '<img src=x>' })] }), {}],
  'a quote in an address': [D({ filter: [R({ srcAddress: 'a"b' })] }), {}],
  'markup in a chain': [D({ filter: [R({ chain: '<b>c</b>' })] }), {}],
  // Search whitespace: the box is trimmed, and without a padded case a mutation
  // removing the trim survives.
  'search is trimmed': [D({ filter: [R({})] }), { search: '  accept  ' }],
  // ── WITH WRITE PERMISSION ─────────────────────────────────────────────────
  //
  // Every case above renders read-only, because permission arrives on a separate
  // event this gate was not firing. That made `canMove = mayWrite && !search`
  // unreachable — the move arrows never appeared at all — and a mutation
  // dropping the search half survived the whole corpus.
  'writable: one rule': [D({ filter: [R({})] }), { writable: true }],
  'writable: several rules show move arrows': [D({ filter: [
    R({}), R({ id: '*2' }), R({ id: '*3' })] }), { writable: true }],
  'writable: the FIRST and LAST rows differ': [D({ filter: [
    R({}), R({ id: '*2' })] }), { writable: true }],
  // …and a search SUPPRESSES the arrows, because the visible order is not the
  // real one and moving by it would reorder the wrong rules.
  'writable BUT searching hides the move arrows': [D({ filter: [
    R({}), R({ id: '*2', chain: 'input' })] }), { writable: true, search: 'forward' }],
  'writable and a disabled rule': [D({ filter: [R({ disabled: true })] }), { writable: true }],
};

const parentStates = new Set();
for (const [name, [data, opts]] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live(name, () => liveRun(data, opts)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(data, opts); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  parentStates.add(JSON.parse(a).__parent.join(','));
  cmp(name, a, b);
}

// ── BOTH SIDES OF THE fw-noedit TOGGLE MUST BE REACHED ─────────────────────
//
// The parent's class list is compared now, and a comparison of two empty lists
// is what this whole addition was meant to stop being. If every case granted
// write access — or none did — `__parent` would be the same string throughout,
// inverting the toggle would flip both sides together, and the mutation would go
// back to surviving while the gate looked richer than before.
if (parentStates.size < 2) {
  shout('every case left the firewall table in the same edit state (%s) — the fw-noedit ' +
        'toggle is not being exercised, and comparing the parent proves nothing',
        [...parentStates].map((x) => x || '<none>').join(' / '));
  process.exit(1);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(G.live('auto:3', () => liveRun(D({ filter: [R({})] }), {})));
  assert.match(s.firewallTable.h, /forward/, 'the live table rendered no rule');
  assert.match(s.firewallTable.h, /data-id="\*1"/, 'the rule lost its resource id');
}
{
  // The two empty states are different sentences, and the difference matters:
  // "No rules" means the chain is empty, "No rules match search" means the
  // viewer's filter is hiding them.
  const none = JSON.parse(G.live('auto:2', () => liveRun(D({}), {}))).firewallTable.h;
  const nomatch = JSON.parse(G.live('auto:1', () => liveRun(D({ filter: [R({})] }), { search: 'zzzz' }))).firewallTable.h;
  assert.match(none, /No rules</, 'the empty-chain state is wrong: ' + none);
  assert.match(nomatch, /No rules match search/, 'the no-match state is wrong: ' + nomatch);
  assert.notEqual(none, nomatch, 'both empty states rendered the same sentence');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('firewall-table-check: %d cases identical', checked);
