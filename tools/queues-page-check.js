'use strict';
/**
 * The QUEUES page, live against ported.
 *
 * ── WHY THIS ONE, AND WHY THE SHARED SHIM ───────────────────────────────────
 *
 * `queues` is one of only TWO modules in this port that write a button into
 * innerHTML as a STRING and then look it up by id — the self-throttle prompt's
 * Cancel / Create anyway pair. The other was `backups`. A shim that does not
 * register those ids returns null on BOTH sides, both implementations no-op, and
 * the gate passes having compared nothing. That is the false-pass direction, so
 * this gate is built on `tools/lib/dom-shim.js`, where the rule is written down.
 *
 * ── THE WHOLE IIFE IS LIFTED, NOT A CHOSEN SET OF FUNCTIONS ────────────────
 *
 * The page is ~450 lines with a dozen interdependent helpers. Picking some and
 * stubbing the rest means choosing what the gate is allowed to notice, and the
 * choice is invisible in a green run. Lifting the whole IIFE and driving BOTH
 * sides through the same socket events compares two implementations rather than
 * one implementation against a harness.
 *
 * ── THE ROW ACTIONS ARE DRIVEN NOW ──────────────────────────────────────────
 *
 * Five acts share one delegated handler — edit, move, toggle, reset, remove —
 * and only Remove asks. This gate stubbed `window.confirm` as `() => true`,
 * which is exactly enough to stop it throwing and no more: it says yes to every
 * question, so the WORDING was never compared and the cancel path was never
 * taken. A stub that keeps a gate green is harder to notice than one that breaks
 * it.
 *
 * Eleven cases now press the acts through the real delegated listener, and the
 * snapshot carries the question asked and the events emitted. `expectedName`
 * rides with all four writes: it is the router-side guard against an id reused
 * since the page was drawn. Nine mutations killed, including a cancelled
 * confirmation still removing, the question naming the id instead of the queue,
 * `toggle` growing a confirmation of its own, and `move` always sending "up".
 *
 * WHAT IT CANNOT SEE: layout, focus, the sparkline's
 * rendered geometry beyond the markup, and the status timer (stubbed, since it
 * only schedules a later clear).
 *
 *   MIKRODASH_SRC=../MikroDash node tools/queues-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('queues-page-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

const iife = G.value('iife', () => {
  const banner = src.indexOf('/* ── Queues page ');
  assert.ok(banner > 0, 'no Queues page banner in app.js');
  const open = src.indexOf('(function () {', banner);
  assert.ok(open > banner && open - banner < 2000, 'the queues IIFE is not where its banner says');
  // BOTH closing spellings, whichever comes FIRST. app.js uses `}());` here and
  // `})();` elsewhere; looking for only the latter ran this slice 2,000 lines
  // past the end of the page and swallowed three later ones.
  const ends = ['\n}());', '\n})();']
    .map((pat) => src.indexOf(pat, open)).filter((i) => i > open);
  assert.ok(ends.length, 'the queues IIFE never closes at column 0');
  return src.slice(open + '(function () {'.length, Math.min(...ends));
});
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['iife', iife]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}
assert.ok(iife.includes('qf_warnGo'), 'the lifted IIFE lost the self-throttle prompt');
assert.ok(iife.includes('renderSimple'), 'the lifted IIFE is not the queues page');
// AND IT MUST BE BOUNDED. The two asserts above are INCLUSION checks, and an
// over-long slice satisfies both — which is exactly what happened: the first
// version reached into the Backups page and still passed them. A slice is only
// proved correct by what it EXCLUDES.
for (const foreign of ['backupsPage', 'bkDiffBody', 'Bandwidth Page']) {
  assert.ok(!iife.includes(foreign),
    'the lifted queues IIFE reaches into other pages (found ' + foreign + ')');
}

const grab = (decl) => { const i = src.indexOf(decl); return src.slice(i, src.indexOf('\n', i)); };
const whole = (decl) => { const i = src.indexOf(decl); return src.slice(i, src.indexOf('\n}', i) + 2); };
// FROZEN AS ONE JOINED PROGRAM. These lifters were called INLINE inside the
// `vm.runInContext` array — freezing the JOINED RESULT covers every lift inside
// it whatever shape each has, which is cheaper than teaching a converter each.
const LIVE_HELPERS = G.value('the lifted live helpers', () => [
  grab('function esc('),
  whole('function fmtBytes('),
  whole('function _renderSortHeader('),
].join('\n'));
if (!LIVE_HELPERS || LIVE_HELPERS.length < 100) {
  throw new Error('the recorded live helpers are empty — the golden is broken');
}

const ENTRY = path.join(ROOT, 'testdata', '.q-entry.ts');
fs.writeFileSync(ENTRY, "export { initQueuesPage } from '../web/src/pages/queues.js';\n");
const OUT = path.join(ROOT, 'testdata', '.q-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// Every id the live queues IIFE references, extracted from it rather than
// guessed — the page returns early or crashes on a missing element, and
// discovering that one id at a time turns a gate into a scavenger hunt.
//
// `qf_warnCancel` and `qf_warnGo` are DELIBERATELY ABSENT: they are written into
// innerHTML as a string, and the shim is supposed to register them from that
// markup. Pre-declaring them would hide the very behaviour this gate exists to
// check.
// NOTE ON HOW THIS LIST WAS BUILT. Extracting `$('id')` and `id="…"` from the
// live source misses an id passed as an ARGUMENT — `_renderSortHeader('qSimpleThead', …)`
// names its target that way, so both theads were absent, the helper returned
// early on BOTH sides, and mutations to the header markup survived. Found by
// mutating `renderSortHeader` and watching the DNS gate catch it while this one
// did not.
const IDS = [
  'qSimpleThead', 'qTreeThead',
  'qActionNote',
  'qAddBtn',
  'qFormWrap',
  'qFtBanner',
  'qFtCard',
  'qNotice',
  'qNoticeCard',
  'qSearch',
  'qSimpleBadge',
  'qSimpleTable',
  'qSumActive',
  'qSumBytes',
  'qSumSimple',
  'qSumTree',
  'qTreeBadge',
  'qTreeTable',
  'qf_ack',
  'qf_comment',
  'qf_disabled',
  'qf_error',
  'qf_expectedName',
  'qf_id',
  'qf_limitAt',
  'qf_markLabel',
  'qf_maxHint',
  'qf_maxLimit',
  'qf_menu',
  'qf_name',
  'qf_packetMark',
  'qf_parent',
  'qf_parentWrap',
  'qf_priority',
  'qf_save',
  'qf_target',
  'qf_targetWrap',
  'qf_title',
  'qf_warn'
];

const newDoc = () => makeDoc(IDS, { pickSelectors: [] });

function snap(doc, trail) {
  const n = doc.nodes;
  const t = (id) => (n[id] ? n[id].textContent : null);
  const h = (id) => (n[id] ? n[id].innerHTML : null);
  const btn = (id) => (n[id] ? { present: true } : null);
  return JSON.stringify({
    // What the page ASKED and what it SENT. Remove is destructive and the
    // question is the only warning; neither is markup.
    trail: trail || undefined,
    // The TABLES, not a tbody — both sides assign to `$('qSimpleTable')`. The
    // first version snapshotted `qSimpleTbody`, which neither side writes, so
    // every case compared two empty strings and passed. The believability
    // assertion below is the only reason that was caught, which is the argument
    // for having one in every gate.
    simple: h('qSimpleTable'), tree: h('qTreeTable'),
    simpleHead: h('qSimpleThead'), treeHead: h('qTreeThead'),
    simpleBadge: t('qSimpleBadge'), treeBadge: t('qTreeBadge'),
    sumSimple: t('qSumSimple'), sumTree: t('qSumTree'),
    sumActive: t('qSumActive'), sumBytes: t('qSumBytes'),
    status: t('qActionNote'), ftBanner: h('qFtBanner'), notice: h('qNotice'),
    warn: h('qf_warn'), warnShown: n.qf_warn ? n.qf_warn.style.display : null,
    ack: n.qf_ack ? n.qf_ack.value : null,
    // Rule 2 of the shim, asserted as data: the prompt's buttons must EXIST as
    // nodes after the markup naming them was assigned.
    warnButtons: [btn('qf_warnCancel'), btn('qf_warnGo')],
  });
}

function drive(handlers, doc, script, o, trail) {
  for (const [ev, payload] of script) {
    const fn = handlers[ev];
    if (!fn) throw new Error('nothing subscribes ' + ev);
    fn(payload);
  }
  // ── THE ROW ACTIONS ─────────────────────────────────────────────────────
  //
  // Delegated on `document` and found with `closest('[data-qact]')`, so the
  // click is dispatched at document level with a target that answers `closest`.
  // The attributes come from the case, not from the rendered row: a row drawn
  // with the wrong `data-name` is a different bug and the markup half of this
  // gate catches it, whereas mixing the two would let either hide the other.
  if (o && o.act) {
    const btn = {
      getAttribute: (k) => (k === 'data-qact' ? o.act : k === 'data-id' ? o.actId
        : k === 'data-menu' ? (o.actMenu || 'simple') : k === 'data-name' ? o.actName : null),
    };
    btn.closest = (sel) => (sel === '[data-qact]' ? btn : null);
    doc.dispatch('click', btn);
  }
  return snap(doc, o && o.act ? trail : null);
}

function liveRun(script, query, opts) {
  const o = opts || {};
  const doc = newDoc();
  if (query) doc.nodes.qSearch.value = query;
  const handlers = {};
  const trail = [];
  const ctx = {
    String, Array, Math, Number, Object, JSON, Date, isFinite, parseInt, parseFloat,
    document: doc,
    socket: { on: (ev, fn) => { handlers[ev] = fn; },
              emit: (ev, p) => { trail.push({ ev, p }); } },
    setTimeout: () => 0, clearTimeout: () => {},
    // ── THE ANSWER COMES FROM THE CASE, AND THE QUESTION IS RECORDED ──────
    //
    // This was `confirm: () => true`. That is enough to stop the gate throwing
    // and no more: it says yes to every question, so the WORDING was never
    // compared and the CANCEL path was never taken. Remove is the destructive
    // action here — "traffic it was limiting will no longer be shaped".
    window: {
      confirm: (t) => { trail.push({ confirm: String(t) }); return !!o.confirm; },
      prompt: () => '',
    },
  };
  vm.createContext(ctx);
  vm.runInContext([
    LIVE_HELPERS,
    'function $(id){return document.getElementById(id);}',
    // The REAL sort header. It was stubbed here too, which left the queues
    // headers written-but-unwired and their markup uncompared. See the DNS gate
    // for the measurement that showed what a stub costs.
    'function _debounce(fn){return fn;}',
    // The page's visibility check. The port is given `() => true` for the same
    // reason: a gate is always looking at the page it is testing.
    'function pageVisible(){return true;}',
    '(function () {' + iife + '})();',
  ].join('\n'), ctx);
  // The shim RECORDS every id the page asked for and this gate did not declare.
  // The queues IIFE returns early when a required element is missing, so a short
  // IDS list produces a page that registers no handlers and renders nothing —
  // silently. This turns that into a message naming the ids.
  // Same idea for a null-set crash: the shim knows which ids were asked for.
  const report = (e) => new Error(e.message + ' — ids this gate does not provide: ' +
    ([...doc.unknown].join(', ') || 'none'));
  if (!Object.keys(handlers).length) {
    throw new Error('the live page registered no handlers; ids it looked for and ' +
      'this gate does not provide: ' + [...doc.unknown].join(', '));
  }
  try { return drive(handlers, doc, script, o, trail); } catch (e) { throw report(e); }
}

function portRun(script, query, opts) {
  const o = opts || {};
  const doc = newDoc();
  if (query) doc.nodes.qSearch.value = query;
  const handlers = {};
  const trail = [];
  const prevWin = globalThis.window;
  globalThis.window = {
    confirm: (t) => { trail.push({ confirm: String(t) }); return !!o.confirm; },
    prompt: () => '',
  };
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initQueuesPage({ on: (ev, fn) => { handlers[ev] = fn; },
        emit: (ev, p) => { trail.push({ ev, p }); } }, () => true);
      return drive(handlers, doc, script, o, trail);
    });
  } finally {
    if (prevWin === undefined) delete globalThis.window; else globalThis.window = prevWin;
  }
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 3) shout('DIFF %s\n  live: %s\n  port: %s', what, String(a).slice(0, 900), String(b).slice(0, 900));
}

const CAPS = { permitted: true, routerName: 'hAP ax3' };

// The payload shapes are taken from the PORT's interfaces and cross-checked
// against what the live page reads — `rateBps` is a PAIR on a simple queue and a
// bare NUMBER on a tree queue, which is the sort of asymmetry a hand-written
// fixture gets wrong and then "proves" with a green run.
const SQ = (o) => Object.assign({
  id: '1', order: 0, name: 'guest', target: '198.51.100.0/24', parent: 'none',
  packetMarks: '', priority: '8', queueType: 'default',
  limitAt: { up: null, down: null }, maxLimit: { up: 10000000, down: 20000000 },
  burstLimit: { up: null, down: null },
  bytes: { up: 0, down: 0 }, packets: { up: 0, down: 0 },
  dropped: { up: 0, down: 0 }, queuedBytes: { up: 0, down: 0 },
  disabled: false, invalid: false, dynamic: false, comment: '',
  rateBps: { up: 1000000, down: 2000000 }, rateSource: 'poll', rateWindowMs: 5000,
}, o);

const TQ = (o) => Object.assign({
  id: '2', order: 0, name: 'up-tree', parent: 'global', packetMark: 'm1',
  priority: '8', queueType: 'default',
  limitAt: null, maxLimit: null, burstLimit: null,
  bytes: null, packets: null, dropped: null, queuedBytes: null,
  disabled: false, invalid: false, dynamic: false, comment: '',
  fasttrackBypassable: false,
  rateBps: 500000, rateSource: 'poll', rateWindowMs: 5000,
}, o);

const U = (o) => Object.assign({
  ts: 1, pollMs: 5000, simple: [], tree: [],
  fasttrack: { state: 'absent', count: 0, scoped: false },
  stats: '', available: true, denied: false,
}, o);

const Q = [['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({})], tree: [TQ({})] })]];

const CASES = {
  'empty': [[['queues:caps', CAPS], ['queues:update', U({})]], ''],
  'one simple queue': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({})] })]], ''],
  'one tree queue': [[['queues:caps', CAPS], ['queues:update', U({ tree: [TQ({})] })]], ''],
  'both kinds': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({})], tree: [TQ({})] })]], ''],
  'a disabled queue': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({ disabled: true })] })]], ''],
  'a dynamic queue': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({ dynamic: true })] })]], ''],
  'no limits set': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({ maxLimit: { up: null, down: null } })] })]], ''],
  'zero rate': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({ rateBps: { up: 0, down: 0 } })] })]], ''],
  'a comment': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({ comment: 'note' })] })]], ''],
  // Added after a mutant survived: nothing in the corpus had an EMPTY target, so
  // the em-dash fallback was never exercised and removing it went unnoticed.
  'a simple queue with no target': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({ target: '' })] })]], ''],
  'a tree queue with no packet mark': [[['queues:caps', CAPS], ['queues:update', U({ tree: [TQ({ packetMark: '' })] })]], ''],
  'markup in a name': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({ name: '<img src=x>' })] })]], ''],
  'a quote in a name': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({ name: 'a"b' })] })]], ''],
  'a viewer': [[['queues:caps', { permitted: false, routerName: 'r' }], ['queues:update', U({ simple: [SQ({})] })]], ''],
  // Search.
  'search matching': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({}), SQ({ id: '9', name: 'other' })] })]], 'guest'],
  'search matching nothing': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({})] })]], 'zzz'],
  'search is case-insensitive': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({})] })]], 'GUEST'],
  // FastTrack.
  'fasttrack active': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({})], fasttrack: { state: 'active', count: 2, scoped: false } })]], ''],
  'fasttrack absent': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({})], fasttrack: { state: 'absent', count: 0, scoped: false } })]], ''],
  'fasttrack unknown': [[['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({})], available: false })]], ''],
  // Two updates in a row — the history buffer the sparkline reads.
  'two updates build history': [[['queues:caps', CAPS],
    ['queues:update', U({ simple: [SQ({})] })],
    ['queues:update', U({ simple: [SQ({ rateBps: { up: 3000000, down: 4000000 } })] })]], ''],
  'a queue disappearing drops its history': [[['queues:caps', CAPS],
    ['queues:update', U({ simple: [SQ({}), SQ({ id: '9', name: 'gone' })] })],
    ['queues:update', U({ simple: [SQ({})] })]], ''],
  // THE SELF-THROTTLE PROMPT — the write-then-look-up path.
  'self-throttle prompt': [[['queues:caps', CAPS], ['queues:update', U({})],
    ['queues:error', { code: 'self-throttle', fingerprint: 'fp1',
      warning: { address: '198.51.100.7', target: '198.51.100.0/24',
        maxLimit: { up: 1000000, down: 2000000 } } }]], ''],
  'stale-warning adds its note': [[['queues:caps', CAPS], ['queues:update', U({})],
    ['queues:error', { code: 'stale-warning', fingerprint: 'fp2',
      warning: { address: '198.51.100.7', target: '198.51.100.0/24',
        maxLimit: { up: 1000000, down: 2000000 } } }]], ''],
  'the prompt with no warning body': [[['queues:caps', CAPS], ['queues:update', U({})],
    ['queues:error', { code: 'self-throttle', fingerprint: '' }]], ''],
  'an ordinary error is not the prompt': [[['queues:caps', CAPS], ['queues:update', U({})],
    ['queues:error', { code: 'denied', message: 'nope' }]], ''],

  // ── THE ROW ACTIONS ──────────────────────────────────────────────────────
  //
  // Five acts share one delegated handler, and only one of them asks. The gate
  // stubbed `confirm: () => true`, which kept it from throwing while saying yes
  // to every question — so the WORDING was never compared and the cancel path
  // was never taken. `expectedName` rides with all four writes: it is the
  // router-side guard against an id reused since the page was drawn.
  'remove a simple queue': [Q, '', { act: 'remove', actId: '1', actName: 'guest', confirm: true }],
  'remove, cancelled': [Q, '', { act: 'remove', actId: '1', actName: 'guest', confirm: false }],
  'remove a TREE queue': [Q, '',
    { act: 'remove', actId: '2', actName: 'up-tree', actMenu: 'tree', confirm: true }],
  // A name with markup: the question is built by concatenation on both sides,
  // so neither escapes it and neither may start.
  'remove a queue whose name carries markup': [
    [['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({ name: '<b>g</b>' })] })]], '',
    { act: 'remove', actId: '1', actName: '<b>g</b>', confirm: true }],
  // The four that do NOT ask. A confirmation appearing on one of these would be
  // a difference the markup cannot show.
  'toggle a simple queue': [Q, '', { act: 'toggle', actId: '1', actName: 'guest', confirm: true }],
  'toggle a tree queue': [Q, '',
    { act: 'toggle', actId: '2', actName: 'up-tree', actMenu: 'tree', confirm: true }],
  'reset counters': [Q, '', { act: 'reset', actId: '1', actName: 'guest', confirm: true }],
  'move up': [Q, '', { act: 'up', actId: '1', actName: 'guest', confirm: true }],
  'move down': [Q, '', { act: 'down', actId: '1', actName: 'guest', confirm: true }],
  // An id that is not in the payload must reach nothing at all.
  'an act on a row that is not there': [Q, '',
    { act: 'remove', actId: '99', actName: 'ghost', confirm: true }],
  // An act neither side knows.
  'an act neither side knows': [Q, '', { act: 'detonate', actId: '1', actName: 'guest', confirm: true }],
};

for (const [name, [script, query, opts]] of Object.entries(CASES)) {
  // A THROW IS NEVER A PASSING CASE. Catching on both sides and comparing the
  // messages would let a gate go green because both implementations were broken
  // in the same way — or because the harness was. Each side is reported as its
  // own failure instead.
  let a, b;
  try { a = liveRun(script, query, opts); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(script, query, opts); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(liveRun([['queues:caps', CAPS], ['queues:update', U({ simple: [SQ({})] })]], ''));
  assert.ok(s.simple && s.simple.includes('guest'), 'the live simple table rendered no row');
}
{
  const s = JSON.parse(liveRun([['queues:caps', CAPS], ['queues:update', U({})],
    ['queues:error', { code: 'self-throttle', fingerprint: 'fp1',
      warning: { address: '198.51.100.7', target: '198.51.100.0/24',
        maxLimit: { up: 1000000, down: 2000000 } } }]], ''));
  assert.match(s.warn, /own connection to this router/, 'the self-throttle prompt did not render');
  assert.equal(s.ack, 'fp1', 'the fingerprint was not carried into the ack field');
  // The shim rule this gate exists for: without id registration these are null
  // on both sides and the comparison is vacuous.
  assert.ok(s.warnButtons[0] && s.warnButtons[1],
    'the prompt buttons were never registered — the shim is not seeing written ids, ' +
    'so this gate would pass whatever the port did with them');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('queues-page-check: %d cases identical', checked);
