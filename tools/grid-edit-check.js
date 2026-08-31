'use strict';
/**
 * The grid's edit mode and Add panel, live against ported.
 *
 * ── THE ROOM ASYMMETRY IS WHAT THIS IS FOR ──────────────────────────────────
 *
 * `addCard` and `removeCard` both ask whether any OTHER visible card wants the
 * room, and both exclude the card in hand — but `addCard` sets `visible = true`
 * BEFORE it asks, so without that exclusion it would always find itself and
 * never emit a join, while `removeCard`'s exclusion is redundant. One of the two
 * is load-bearing and the other is not, and nothing about reading them says
 * which. The corpus drives both over a synthetic table where two cards share a
 * room, because the shipped table has no such pair — the same gap that let a
 * dedupe mutation survive in Part 59.
 *
 * ── AND THE PANEL IS COMPARED AS A DOM TREE, NOT A STRING ───────────────────
 *
 * It is built with createElement and appendChild, so there is no innerHTML to
 * diff. The fake nodes record their tag, class, text, attributes and children,
 * and the two trees are compared — including how many listeners were bound and
 * to what, since a chip that renders correctly and does nothing on click is the
 * defect shape this port has hit five times.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/grid-edit-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const LIFT = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/grid-edit-check.js --freeze
const G = LIFT.golden('grid-edit-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'js', 'dashboard-grid.js'));

function grab(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name);
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}
const fn = (name) => grab('function ' + name + '(', '\n  }', name);

const ENTRY = path.join(ROOT, 'testdata', '.gridedit-entry.ts');
fs.writeFileSync(ENTRY,
  "export { createGridEditor } from '../web/src/pages/dashboard-grid-edit.js';\n" +
  "export { DEFAULT_LAYOUT, CARD_ROOMS, CARD_LABELS, LS_KEY } from '../web/src/gen/grid-tables.js';\n");
const OUT = path.join(ROOT, 'testdata', '.gridedit-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── the fake world ─────────────────────────────────────────────────────────
const IDS = ['dash-grid-root', 'dashEditBtn', 'dashEditControls', 'dashAddPanel', 'page-dashboard'];

function makeWorld(opts) {
  const w = { dispatched: [], fetches: [], storage: {}, nodes: new Map(), logs: [] };
  const mk = (tag) => {
    const n = {
      tagName: String(tag).toUpperCase(), className: '', textContent: '', type: '', href: '',
      style: { _p: {}, setProperty(k, v) { this._p[k] = v; }, display: undefined },
      children: [], listeners: [], classes: new Set(),
      classList: {
        add: (c) => n.classes.add(c), remove: (c) => n.classes.delete(c),
        contains: (c) => n.classes.has(c),
      },
      appendChild(c) { this.children.push(c); return c; },
      addEventListener(type, cb) { this.listeners.push({ type, cb }); },
      getBoundingClientRect: () => ({ width: 1200, height: 800 }),
      set innerHTML(v) { this._h = v; if (v === '') this.children = []; },
      get innerHTML() { return this._h || ''; },
    };
    return n;
  };
  for (const id of IDS) { const n = mk('div'); n.id = id; w.nodes.set(id, n); }
  if (opts && opts.dashActive) w.nodes.get('page-dashboard').classes.add('active');
  // Cards the layout positions.
  for (const id of (opts && opts.cardIds) || []) { const n = mk('div'); n.id = id; w.nodes.set(id, n); }
  w.mk = mk;
  w.doc = {
    getElementById: (id) => w.nodes.get(id) || null,
    createElement: (tag) => mk(tag),
    dispatchEvent: (e) => { w.dispatched.push(e.type + ':' + e.detail); return true; },
  };
  w.ls = { getItem: (k) => (k in w.storage ? w.storage[k] : null), setItem: (k, v) => { w.storage[k] = v; } };
  return w;
}

/** A fake node as comparable data — tag, class, text, attrs, listeners, children. */
function tree(n) {
  if (!n) return null;
  return {
    tag: n.tagName, cls: n.className, text: n.textContent,
    type: n.type || undefined, href: n.href || undefined,
    html: n.innerHTML || undefined,
    listeners: n.listeners.map((l) => l.type),
    children: n.children.map(tree),
  };
}
function snapshot(w) {
  const out = {
    dispatched: w.dispatched.slice(), storage: w.storage, fetches: w.fetches.length,
    // COUNTED: the reset link has href="#", so failing to preventDefault would
    // navigate. Nothing else in the snapshot can see that.
    prevented: w.prevented || 0,
  };
  for (const id of IDS) {
    const n = w.nodes.get(id);
    out[id] = { classes: [...n.classes].sort(), display: n.style.display, vars: n.style._p };
  }
  out.panel = tree(w.nodes.get('dashAddPanel'));
  // Card positions, so applyLayout's effect is part of the comparison.
  out.cards = {};
  for (const [id, n] of w.nodes) {
    if (IDS.includes(id)) continue;
    out.cards[id] = { display: n.style.display, gc: n.style.gridColumn, gr: n.style.gridRow };
  }
  return JSON.stringify(out);
}

function liveEditor(w, layout, rooms) {
  const ctx = {
    Math, Object, JSON, Array, Error,
    console: { warn: (...a) => w.logs.push('warn:' + a.join(' ')), log: (...a) => w.logs.push('log:' + a.join(' ')) },
    document: w.doc, localStorage: w.ls,
    CustomEvent: function (type, init) { return { type, detail: init && init.detail }; },
    fetch: () => { w.fetches.push(1); return Promise.resolve({ ok: true, status: 200 }); },
    layout, editSnapshot: [], isEditing: false,
    gridRoot: w.nodes.get('dash-grid-root'),
    editBtn: w.nodes.get('dashEditBtn'),
    editControls: w.nodes.get('dashEditControls'),
    addPanel: w.nodes.get('dashAddPanel'),
  };
  vm.createContext(ctx);
  vm.runInContext([
    grab('var COLS = 24', ';', 'constants'), grab('var MIN_W', ';', 'MIN_W'),
    grab('var CARD_LABELS = {', '};', 'CARD_LABELS'),
    grab('var CARD_ROOMS = {', '};', 'CARD_ROOMS'),
    grab('var DEFAULT_LAYOUT = [', '];', 'DEFAULT_LAYOUT'),
    grab("var LS_KEY = '", ';', 'LS_KEY'),
    fn('cloneLayout'), fn('mergeLayout'), fn('saveLayout'), fn('applyLayout'),
    fn('getCard'), fn('rectOverlaps'), fn('hasOverlap'), fn('findFreeSlot'),
    fn('getCellSize'), fn('_notifyRoom'), fn('_dashActive'), fn('updateGridOverlay'),
    fn('enterEditMode'), fn('exitEditMode'), fn('removeCard'), fn('addCard'),
    fn('renderAddPanel'), fn('openAddPanel'), fn('closeAddPanel'),
  ].join('\n'), ctx);
  if (rooms) ctx.CARD_ROOMS = rooms;
  return ctx;
}
function portEditor(w, layout, rooms) {
  globalThis.document = w.doc;
  globalThis.localStorage = w.ls;
  globalThis.CustomEvent = function (type, init) { return { type, detail: init && init.detail }; };
  globalThis.fetch = () => { w.fetches.push(1); return Promise.resolve({ ok: true, status: 200 }); };
  globalThis.console.warn = (...a) => w.logs.push('warn:' + a.join(' '));
  globalThis.console.log = (...a) => w.logs.push('log:' + a.join(' '));
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  return { m, ed: m.createGridEditor(layout, rooms || undefined) };
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 4) {
    const A = JSON.parse(a), B = JSON.parse(b);
    shout('DIFF %s', what);
    for (const k of Object.keys(A)) {
      if (JSON.stringify(A[k]) !== JSON.stringify(B[k])) {
        shout('  %s\n    live: %s\n    port: %s', k,
          JSON.stringify(A[k]).slice(0, 300), JSON.stringify(B[k]).slice(0, 300));
      }
    }
  }
}

const probe = (() => { const w = makeWorld({}); return portEditor(w, [], null).m; })();
const C = (id, x, y, w, h, visible) => ({ id, x, y, w, h, visible });

// ── the scripts ────────────────────────────────────────────────────────────
const SCRIPTS = {
  'enter edit mode': [['enter']],
  'enter then save': [['enter'], ['exit', true]],
  'enter then discard': [['enter'], ['exit', false]],
  'discard puts back what entry snapshotted': [
    ['enter'], ['remove', 'card-system'], ['exit', false],
  ],
  'save keeps the change': [['enter'], ['remove', 'card-system'], ['exit', true]],
  'remove then add the same card': [['remove', 'card-system'], ['add', 'card-system']],
  'add a card that is already visible': [['add', 'card-system']],
  'add a card that does not exist': [['add', 'card-nonexistent']],
  'remove a card that does not exist': [['remove', 'card-nonexistent']],
  'add a hidden extra card': [['add', 'dc-card-bgp']],
  'remove every visible card': [['removeAll']],
  // NOT the empty state: the shipped default hides 14 of its 23 cards, so this
  // opens a panel that is full of chips.
  'open the panel with the shipped default': [['openPanel']],
  // The ACTUAL empty state, which needs every hidden card added first. Without
  // it the `hidden.length === 0` branch is unreachable and a mutation skipping
  // it survives — the panel would render an empty chip row instead of saying
  // "All cards are visible".
  'open the panel with NOTHING hidden': [['addAll'], ['openPanel']],
  'open the panel with one hidden': [['remove', 'card-system'], ['openPanel']],
  'open the panel with several hidden': [
    ['remove', 'card-system'], ['remove', 'card-network'], ['openPanel'],
  ],
  'open then close': [['openPanel'], ['closePanel']],
  'the overlay variables': [['overlay']],
  'reset to defaults from the panel': [['openPanel'], ['reset']],
  'a chip click adds its card': [['remove', 'card-system'], ['openPanel'], ['clickChip', 0]],
};

function runStep(kind, api, w, step) {
  const [op, arg] = step;
  if (op === 'enter') return api.enter();
  if (op === 'exit') return api.exit(arg);
  if (op === 'add') return api.add(arg);
  if (op === 'remove') return api.remove(arg);
  if (op === 'removeAll') { for (const c of api.layout().filter((x) => x.visible)) api.remove(c.id); return; }
  if (op === 'addAll') { for (const c of api.layout().filter((x) => !x.visible)) api.add(c.id); return; }
  if (op === 'openPanel') return api.openPanel();
  if (op === 'closePanel') return api.closePanel();
  if (op === 'overlay') return api.overlay();
  if (op === 'reset') {
    // The reset link is the LAST child of the panel; clicking it is the only way
    // to reach that branch, so the gate clicks it rather than calling a function
    // the live app does not expose.
    const panel = w.nodes.get('dashAddPanel');
    const link = panel.children[panel.children.length - 1];
    const l = link.listeners.find((x) => x.type === 'click');
    if (!l) throw new Error('the reset link has no click listener');
    l.cb({ preventDefault() { w.prevented = (w.prevented || 0) + 1; } });
    return;
  }
  if (op === 'clickChip') {
    const panel = w.nodes.get('dashAddPanel');
    const chips = panel.children.find((c) => c.className === 'dash-add-chips');
    if (!chips) throw new Error('no chip row in the panel');
    const chip = chips.children[arg];
    const l = chip.listeners.find((x) => x.type === 'click');
    if (!l) throw new Error('chip ' + arg + ' has no click listener');
    l.cb();
    return;
  }
  throw new Error('unknown step ' + op);
}

function runScript(script, rooms, dashActive) {
  const ids = probe.DEFAULT_LAYOUT.map((c) => c.id);
  const lw = makeWorld({ cardIds: ids, dashActive });
  const pw = makeWorld({ cardIds: ids, dashActive });
  // THE LIVE EDITOR IS BUILT ONLY WHEN THERE IS A REFERENCE. Every `lapi` call
  // now happens inside a frozen closure, which does not run on replay, so a null
  // here is never reached rather than being a lurking crash.
  const L = LIFT.hasReference(ROOT)
    ? liveEditor(lw, probe.DEFAULT_LAYOUT.map((c) => ({ ...c })), rooms) : null;
  const P = portEditor(pw, probe.DEFAULT_LAYOUT.map((c) => ({ ...c })), rooms);
  const lapi = {
    enter: () => L.enterEditMode(), exit: (s) => L.exitEditMode(s),
    add: (id) => L.addCard(id), remove: (id) => L.removeCard(id),
    openPanel: () => L.openAddPanel(), closePanel: () => L.closeAddPanel(),
    overlay: () => L.updateGridOverlay(), layout: () => L.layout,
  };
  const papi = {
    enter: () => P.ed.enterEditMode(), exit: (s) => P.ed.exitEditMode(s),
    add: (id) => P.ed.addCard(id), remove: (id) => P.ed.removeCard(id),
    openPanel: () => P.ed.openAddPanel(), closePanel: () => P.ed.closeAddPanel(),
    overlay: () => P.ed.updateGridOverlay(), layout: () => P.ed.getLayout(),
  };
  return { lw, pw, L, P, lapi, papi };
}

for (const [name, script] of Object.entries(SCRIPTS)) {
  for (const dashActive of [true, false]) {
    const key = name + ' [dashActive=' + dashActive + ']';
    // THE WHOLE LIVE RUN, FROZEN AS ONE ORDERED SEQUENCE. The live world is
    // driven step by step OUTSIDE the comparison, so freezing only the read
    // would leave the driver running on replay. Recipe 3i.
    const live = G.value(key + ' live run', () => {
      const lr = runScript(script, null, dashActive);
      const snaps = script.map((step) => {
        runStep('live', lr.lapi, lr.lw, step);
        return snapshot(lr.lw);
      });
      return { snaps, layout: JSON.stringify(lr.lapi.layout()) };
    });
    const r = runScript(script, null, dashActive);
    script.forEach((step, i) => {
      runStep('port', r.papi, r.pw, step);
      cmp(key + ' after step ' + (i + 1) + ' (' + step[0] + ')', live.snaps[i], snapshot(r.pw));
    });
    cmp(key + ' final layout', live.layout, JSON.stringify(r.papi.layout()));
  }
}

// ── the room asymmetry, over a table where two cards SHARE a room ──────────
{
  const SYN = { 'card-system': 'firewall', 'card-network': 'firewall', 'card-toptalkers': 'logs' };
  const scripts = {
    'remove one of a shared pair — the room STAYS': [['remove', 'card-system']],
    'remove both — the room goes on the second': [['remove', 'card-system'], ['remove', 'card-network']],
    'remove the only card in its room': [['remove', 'card-toptalkers']],
    're-add one of a pair while the other is visible — NO second join': [
      ['remove', 'card-system'], ['add', 'card-system'],
    ],
    'add the first of a pair back when both were gone': [
      ['remove', 'card-system'], ['remove', 'card-network'], ['add', 'card-system'],
    ],
    'add the second of a pair — no duplicate join': [
      ['remove', 'card-system'], ['remove', 'card-network'],
      ['add', 'card-system'], ['add', 'card-network'],
    ],
  };
  for (const [name, script] of Object.entries(scripts)) {
    for (const dashActive of [true, false]) {
      const key = '[shared room] ' + name + ' [active=' + dashActive + ']';
      const live = G.value(key + ' live run', () => {
        const lr = runScript(script, SYN, dashActive);
        return script.map((step) => {
          runStep('live', lr.lapi, lr.lw, step);
          return snapshot(lr.lw);
        });
      });
      const r = runScript(script, SYN, dashActive);
      script.forEach((step, i) => {
        runStep('port', r.papi, r.pw, step);
        cmp(key + ' step ' + (i + 1), live[i], snapshot(r.pw));
      });
    }
  }
  // THE PROPERTIES THEMSELVES, STATED AGAINST THE PORT. They were asserted
  // against the LIVE side — the half that stops existing, and not the half that
  // has to keep getting this right. Aimed at the port they survive the reference
  // going away AND check the implementation that ships.
  {
    const r = runScript([], SYN, true);
    r.papi.remove('card-system');
    assert.deepEqual(r.pw.dispatched, [],
      'removing one of a shared pair left the room the other card still needs');
    r.papi.remove('card-network');
    assert.deepEqual(r.pw.dispatched, ['dashcard:room:blur:firewall'],
      'removing the last card of a room did not leave it');
  }
  {
    const r = runScript([], SYN, true);
    r.papi.remove('card-system');
    r.pw.dispatched.length = 0;
    r.papi.add('card-system');
    assert.deepEqual(r.pw.dispatched, [],
      're-adding a card whose room is already joined emitted a duplicate join');
  }
  {
    const r = runScript([], SYN, true);
    r.papi.remove('card-system'); r.papi.remove('card-network');
    r.pw.dispatched.length = 0;
    r.papi.add('card-system');
    assert.deepEqual(r.pw.dispatched, ['dashcard:room:focus:firewall'],
      'adding the first card of an empty room did not join it — this is the case the ' +
      'self-exclusion in addCard exists for');
  }
}

// ── a stored card with a ZERO size ─────────────────────────────────────────
//
// `addCard`'s `c.w || 3` floor looks unreachable from the shipped defaults, and
// is not: `mergeLayout` takes a saved entry verbatim, so a hand-edited
// localStorage can carry `w: 0`. Without this case the mutation that drops the
// floor SURVIVES, and a 0x0 card is invisible and unclickable — with no way to
// get it back except Reset.
{
  const ids = probe.DEFAULT_LAYOUT.map((c) => c.id);
  for (const [name, over] of Object.entries({
    'zero width': { w: 0 }, 'zero height': { h: 0 }, 'both zero': { w: 0, h: 0 },
  })) {
    const lay = probe.DEFAULT_LAYOUT.map((c) =>
      c.id === 'dc-card-bgp' ? { ...c, ...over, visible: false } : { ...c });
    // Driven inside the closure: `L.addCard` mutates before either read.
    const live = G.value('addCard(' + name + ') live', () => {
      const lw = makeWorld({ cardIds: ids, dashActive: true });
      const L = liveEditor(lw, lay.map((c) => ({ ...c })), undefined);
      L.addCard('dc-card-bgp');
      return { snap: snapshot(lw), layout: JSON.stringify(L.layout) };
    });
    const pw = makeWorld({ cardIds: ids, dashActive: true });
    const P = portEditor(pw, lay.map((c) => ({ ...c })), undefined);
    P.ed.addCard('dc-card-bgp');
    cmp('addCard(' + name + ')', live.snap, snapshot(pw));
    cmp('addCard(' + name + ') layout', live.layout, JSON.stringify(P.ed.getLayout()));
  }
}

// BELIEVABILITY — that the comparisons above are comparing a real panel and not
// two empty divs agreeing. It asked the question of the LIVE panel; asked of the
// PORT it is the same guard against a vacuous pass, and it outlives the
// reference.
{
  const r = runScript([], null, true);
  r.papi.remove('card-system');
  r.papi.openPanel();
  const t = tree(r.pw.nodes.get('dashAddPanel'));
  assert.equal(t.children[0].text, 'Hidden Cards', 'the panel has no header');
  assert.ok(t.children.some((c) => c.cls === 'dash-add-chips'), 'the panel rendered no chips');
  assert.ok(t.children.some((c) => c.cls === 'dash-reset-link'), 'the panel has no reset link');
  assert.ok(r.pw.nodes.get('dashAddPanel').classes.has('open'), 'the panel never opened');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
say('grid-edit-check: %d comparisons identical', checked);
