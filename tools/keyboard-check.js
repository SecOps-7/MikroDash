'use strict';
/**
 * The shell's keyboard shortcuts, live against ported.
 *
 * Both sides are driven with the same synthetic keydown events and compared on
 * WHICH PAGE each key asks for, whether the default was prevented, the hint's
 * class, and the deferred focus. The live `showPage` and the port's `navigate`
 * are both stubbed to RECORD rather than act — what is under test is the
 * mapping from a key to a page, not what coexistence then does with that page.
 * The strangler-fig redirect for an unported page is `navigate()` in main.ts,
 * shared with the nav items so there is only one copy of it.
 *
 * ── THE CASES THAT MATTER ───────────────────────────────────────────────────
 *
 *   every digit       0-9, and 0 must do NOTHING: `n >= 1` is what stops
 *                     PAGE_KEYS[-1] being read.
 *   a field has focus INPUT, SELECT and TEXTAREA are exempt, or searching the
 *                     logs for "3" jumps to the third page mid-word.
 *   `/`               prevents the default BEFORE navigating — the browser's
 *                     own quick-find would otherwise eat it — and defers the
 *                     focus, because focusing a field in a panel that is still
 *                     hidden does nothing.
 *   a non-digit       `parseInt('a')` is NaN and every comparison against it is
 *                     false, which is the only thing stopping every letter on
 *                     the keyboard from being a shortcut.
 *   a repeat press    restarts the hint timer rather than stacking two.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/keyboard-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/keyboard-check.js --freeze
const G = L.golden('keyboard-check');
const src = L.liveSource(ROOT);
const TABLE = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'pages-table.json'), 'utf8'));
const PAGE_KEYS = TABLE.pageKeys;

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
const kbdSrc = slice("var kbdHint = $('kbdHint');", '\n});', 'the keyboard shortcuts block');

const OUT = path.join(ROOT, 'testdata', '.keyboard-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'keyboard.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function makeWorld() {
  const classes = new Set();
  const hint = { classList: { add: (c) => classes.add(c), remove: (c) => classes.delete(c) } };
  let focused = 0;
  const logSearch = { focus: () => { focused++; } };
  const timers = new Map();
  let seq = 0;
  const asked = [];
  const handlers = [];
  const doc = {
    getElementById: (id) => (id === 'kbdHint' ? hint : id === 'logSearch' ? logSearch : null),
    addEventListener(n, f) { if (n === 'keydown') handlers.push(f); },
  };
  return {
    doc, asked,
    setTimeout: (fn, ms) => { const id = ++seq; timers.set(id, { fn, ms }); return id; },
    clearTimeout: (id) => { timers.delete(id); },
    press(key, tagName) {
      let prevented = 0;
      const ev = {
        key,
        target: tagName === undefined ? null : { tagName },
        preventDefault: () => { prevented++; },
      };
      for (const f of handlers) f(ev);
      this._prevented = (this._prevented || 0) + prevented;
    },
    flush() {
      const pending = [...timers.entries()].sort((a, b) => a[0] - b[0]);
      timers.clear();
      for (const [, t] of pending) t.fn();
    },
    state() {
      return JSON.stringify({
        asked,
        prevented: this._prevented || 0,
        hint: [...classes].sort(),
        focused,
        pending: [...timers.values()].map((t) => t.ms).sort(),
      }, null, 1);
    },
  };
}

function liveRun(body) {
  const w = makeWorld();
  const ctx = {
    document: w.doc, setTimeout: w.setTimeout, clearTimeout: w.clearTimeout,
    parseInt, PAGE_KEYS,
    $: (id) => w.doc.getElementById(id),
    logSearch: w.doc.getElementById('logSearch'),
    showPage: (name) => { w.asked.push(name); },
  };
  vm.createContext(ctx);
  vm.runInContext(kbdSrc, ctx);
  body(w);
  return w.state();
}

function portRun(body) {
  const w = makeWorld();
  const saved = { document: global.document, setTimeout: global.setTimeout, clearTimeout: global.clearTimeout };
  global.document = w.doc; global.setTimeout = w.setTimeout; global.clearTimeout = w.clearTimeout;
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).initKeyboard((page) => { w.asked.push(page); });
    body(w);
  } finally {
    for (const k of Object.keys(saved)) {
      if (saved[k] === undefined) delete global[k]; else global[k] = saved[k];
    }
  }
  return w.state();
}

const bad = [];
let cases = 0;
function compare(what, act) {
  cases++;
  const a = G.live(G.seq(), () => liveRun(act));
  const b = portRun(act);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

// Every digit, including 0 — which must do nothing, since `n >= 1` is the only
// thing standing between it and PAGE_KEYS[-1].
for (const d of ['0', '1', '2', '3', '4', '5', '6', '7', '8', '9']) {
  compare('press ' + d, (w) => { w.press(d); w.flush(); });
  compare('press ' + d + ' without flushing the hint timer', (w) => { w.press(d); });
}
// The slash: prevented, navigated, hinted, and the focus deferred.
compare('press /', (w) => { w.press('/'); w.flush(); });
compare('press / without flushing', (w) => { w.press('/'); });

// Keys that must do nothing at all. `parseInt('a')` is NaN and every comparison
// against NaN is false — that is the entire guard against the alphabet.
for (const k of ['a', 'Z', ' ', 'Enter', 'Escape', 'ArrowDown', '-', '+', 'F5', '.', ',']) {
  compare('press ' + JSON.stringify(k), (w) => { w.press(k); w.flush(); });
}
// Keys parseInt WILL read a number out of, which is the interesting half.
for (const k of ['1e2', '01', '3px', ' 4', '2.9', '10', '+5', '0x3']) {
  compare('press ' + JSON.stringify(k), (w) => { w.press(k); w.flush(); });
}

// A focused field is exempt — and only these three tags are.
for (const tag of ['INPUT', 'SELECT', 'TEXTAREA', 'BUTTON', 'DIV', 'A', 'BODY']) {
  compare('press 3 with focus in a ' + tag, (w) => { w.press('3', tag); w.flush(); });
  compare('press / with focus in a ' + tag, (w) => { w.press('/', tag); w.flush(); });
}
// No target at all, which is what a synthetic event without one looks like.
compare('press 3 with no event target', (w) => { w.press('3'); w.flush(); });

// Repeats restart the hint rather than stacking timers.
//
// The NO-FLUSH version is the one that can see it. Flushing runs both timers and
// both remove the class, so a stacked pair and a restarted single look identical
// afterwards. Left un-flushed, the pending list shows two timers instead of one
// — and two is a real defect, not bookkeeping: the first fires 1800ms after the
// FIRST press, so on a rapid double-press the hint blinks out early.
compare('press 2 twice without flushing', (w) => { w.press('2'); w.press('2'); });
compare('press four digits without flushing', (w) => {
  for (const d of ['1', '2', '3', '4']) w.press(d);
});
compare('press 2 twice', (w) => { w.press('2'); w.press('2'); w.flush(); });
compare('press 2 then 5', (w) => { w.press('2'); w.press('5'); w.flush(); });
compare('press / then 4', (w) => { w.press('/'); w.press('4'); w.flush(); });
compare('press every digit in turn', (w) => {
  for (const d of ['1', '2', '3', '4', '5', '6', '7', '8', '9']) w.press(d);
  w.flush();
});

fs.rmSync(OUT, { force: true });
if (bad.length) {
  console.error('the keyboard shortcuts differ from the live ones:\n\n' + bad.slice(0, 3).join('\n\n') +
    (bad.length > 3 ? '\n\n… and ' + (bad.length - 3) + ' more' : '') + '\n');
  process.exit(1);
}
console.log(`keyboard shortcuts match the live ones (${cases} cases, ` +
  `${TABLE.reachableShortcuts} of ${PAGE_KEYS.length} slots reachable)`);
