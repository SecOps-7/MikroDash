'use strict';
/**
 * Dialog closing, live against ported.
 *
 * Three routes — the × button, the backdrop, Escape — and the one that matters
 * most is the route that must NOT close anything: a click inside an open dialog.
 * The live test is `e.target.classList.contains('rtr-modal-bg')` on the target
 * ITSELF rather than on an ancestor, so a click on the dialog's own content
 * never matches. A port that used `closest('.rtr-modal-bg')` would close the
 * dialog whenever anybody clicked in it, which is the kind of thing that looks
 * fine until someone tries to select text in a form.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/modals-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('modals-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));
const IDS = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'modal-list.json'), 'utf8')).all;

function slice(decl, close, name) {
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
const closeFnSrc = slice('  function _closePrincipalModals() {', '\n  }', '_closePrincipalModals');
const clickSrc = slice("  document.addEventListener('click', function (e) {\n    var closer = e.target.closest",
  '\n  });', 'the delegated click handler');
const keySrc = slice("  document.addEventListener('keydown', function (e) {\n    if (e.key === 'Escape')",
  '\n  });', 'the Escape handler');

const OUT = path.join(ROOT, 'testdata', '.modals-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'modals.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function makeWorld(openIds) {
  const nodes = {};
  for (const id of IDS.concat(['resModal', 'notAModal'])) {
    const classes = new Set(openIds.includes(id) ? ['open'] : []);
    // EVERY dialog element carries the backdrop class, `resModal` included.
    // Giving it only to the listed ones made the membership test unreachable —
    // `contains('rtr-modal-bg')` failed first, so dropping the list check
    // changed nothing and the mutation survived.
    classes.add('rtr-modal-bg');
    nodes[id] = {
      id, classList: {
        add: (c) => classes.add(c), remove: (c) => classes.delete(c),
        contains: (c) => classes.has(c), _set: classes,
      },
    };
  }
  const handlers = {};
  let prevented = 0;
  const doc = {
    getElementById: (id) => nodes[id] || null,
    addEventListener: (n, f) => { (handlers[n] = handlers[n] || []).push(f); },
  };
  return {
    doc, nodes,
    fire(name, ev) { for (const f of (handlers[name] || [])) f({ preventDefault: () => { prevented++; }, ...ev }); },
    /** A click whose target is the element itself. */
    clickOn(id, closestAttr) {
      const n = nodes[id];
      this.fire('click', {
        target: Object.assign(Object.create(n), {
          closest: (sel) => (sel === '[data-modal-close]' && closestAttr
            ? { getAttribute: () => closestAttr } : null),
          getAttribute: () => closestAttr,
        }),
      });
    },
    press(key) { this.fire('keydown', { key }); },
    state() {
      return JSON.stringify({
        open: IDS.concat(['resModal']).filter((id) => nodes[id].classList.contains('open')),
        prevented,
      }, null, 1);
    },
  };
}

function liveRun(openIds, body) {
  const w = makeWorld(openIds);
  const ctx = { document: w.doc, Array, JSON, Object, String,
    _PRINCIPAL_MODALS: IDS };
  vm.createContext(ctx);
  vm.runInContext(closeFnSrc + '\n' + clickSrc + '\n' + keySrc, ctx);
  body(w);
  return w.state();
}
function portRun(openIds, body) {
  const w = makeWorld(openIds);
  const saved = global.document;
  global.document = w.doc;
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).wireModals();
    body(w);
  } finally {
    if (saved === undefined) delete global.document; else global.document = saved;
  }
  return w.state();
}

const bad = [];
let cases = 0;
function compare(what, openIds, act) {
  cases++;
  const a = G.live(what, () => liveRun(openIds, act));
  const b = portRun(openIds, act);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

const ALL_OPEN = IDS.slice();

// Escape closes every one of them, whichever are open.
compare('Escape with all open', ALL_OPEN, (w) => w.press('Escape'));
compare('Escape with none open', [], (w) => w.press('Escape'));
for (const id of IDS) compare('Escape with only ' + id + ' open', [id], (w) => w.press('Escape'));
compare('a key that is not Escape', ALL_OPEN, (w) => w.press('Enter'));
compare('Escape twice', ALL_OPEN, (w) => { w.press('Escape'); w.press('Escape'); });

// The × button, which names its target explicitly.
for (const id of IDS.slice(0, 4)) {
  compare('the × of ' + id, ALL_OPEN, (w) => w.clickOn(id, id));
}
compare('a × naming something that is not a modal', ALL_OPEN, (w) => w.clickOn('notAModal', 'notAModal'));
compare('a × naming an element that does not exist', ALL_OPEN, (w) => w.clickOn('notAModal', 'nosuchid'));

// The backdrop: the target must BE the backdrop, and be in the list.
for (const id of IDS) compare('backdrop click on ' + id, ALL_OPEN, (w) => w.clickOn(id));
// A dialog NOT in the list keeps its own handling — clicking its backdrop here
// must do nothing, which is what stops this from closing things it does not own.
compare('backdrop click on resModal, which is not in the list', ALL_OPEN.concat(['resModal']),
  (w) => w.clickOn('resModal'));
// A click on something with no backdrop class at all.
compare('a click on ordinary content', ALL_OPEN, (w) => {
  w.nodes.notAModal.classList.remove('rtr-modal-bg');
  w.clickOn('notAModal');
});

// A × whose target is ITSELF a backdrop, naming a DIFFERENT dialog.
//
// This is what the `return` after the × branch is for, and nothing else can see
// it: in every other × case the target and the named dialog are the same
// element, so falling through closes something already closed and the two
// implementations agree. Here, falling through would close the dialog that was
// merely clicked THROUGH as well as the one the button named.
compare('a × naming another dialog, clicked on a backdrop', ALL_OPEN,
  (w) => w.clickOn('userFormWrap', 'groupFormWrap'));

fs.rmSync(OUT, { force: true });
if (bad.length) {
  console.error('dialog closing differs from the live app:\n\n' + bad.slice(0, 3).join('\n\n') + '\n');
  process.exit(1);
}
console.log(`dialog closing matches the live app (${cases} cases across ${IDS.length} dialogs)`);
