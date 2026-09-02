/**
 * The notification bell closes when you click away.
 *
 * ── WHY THIS IS A TEST AND NOT A ONE-LINE DIFF ──────────────────────────────
 *
 * There was no dismissal at all: the only way to close the panel was to find
 * the bell and click it a second time, leaving an opaque overlay over the page
 * in the meantime. Reported by the operator.
 *
 * The fix is a `document` click listener that ignores anything inside
 * `.notif-wrap`, and the reason it needs pinning is that the containment test is
 * load-bearing in TWO directions at once, one of which is invisible until it
 * breaks:
 *
 *   too WIDE  — the panel never closes, which is the original bug back again.
 *   too NARROW — the toggle's own click is treated as an outside click, so the
 *                document handler closes the panel in the same tick the toggle
 *                opened it. The bell then looks completely dead: one click does
 *                nothing at all, and nothing is logged.
 *
 * The second is why `openingClickSurvives` exists. It fires the two listeners in
 * the order a real browser fires them — the button's, then the document's — and
 * asserts the panel is still open afterwards. A shim that only ran the button's
 * handler would pass while the real page was broken.
 *
 * `router-dropdown.ts` avoids the same trap with `stopPropagation` on its
 * toggle. Deliberately not copied, and the module says why: swallowing the click
 * stops it reaching every other document listener, and this panel sits in a
 * topbar beside several.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

// ── the shim ────────────────────────────────────────────────────────────────
//
// `closest` walks a real parent chain, because that is the thing under test. The
// shared `dom-shim` stubs it as `() => null`, which would make every click look
// like an outside click and pass this file against a broken panel.
function makeEl(id, parent) {
  const classes = new Set();
  const listeners = {};
  const node = {
    id,
    parent: parent || null,
    className: '',
    textContent: '',
    innerHTML: '',
    style: {},
    classList: {
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c) => (classes.has(c) ? (classes.delete(c), false) : (classes.add(c), true)),
    },
    addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    fire: (ev, arg) => (listeners[ev] || []).forEach((fn) => fn(arg)),
    // Matches on the wrap's marker class or on an id selector; enough for the
    // one selector the module uses, and it walks UP like the real thing.
    closest(sel) {
      let n = node;
      while (n) {
        if (sel === '.notif-wrap' && n.isWrap) return n;
        if (sel === '#' + n.id) return n;
        n = n.parent;
      }
      return null;
    },
  };
  return node;
}

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-notif-dismiss.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'notifications.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

/** Mount the real bell against a fresh shim; returns the handles a click needs. */
function mount() {
  const wrap = makeEl('notifWrap', null);
  wrap.isWrap = true;
  const toggle = makeEl('notifToggleBtn', wrap);
  const panel = makeEl('notifPanel', wrap);
  const list = makeEl('notifList', panel);
  const dot = makeEl('notifDot', wrap);
  const clear = makeEl('notifClearBtn', panel);
  // Somewhere else on the page entirely — the "click away" target.
  const elsewhere = makeEl('someCard', null);

  const els = { notifToggleBtn: toggle, notifPanel: panel, notifList: list,
    notifDot: dot, notifClearBtn: clear };
  const docListeners = {};
  global.document = {
    getElementById: (id) => els[id] || null,
    addEventListener: (ev, fn) => { (docListeners[ev] = docListeners[ev] || []).push(fn); },
    querySelectorAll: () => [],
  };
  global.window = { addEventListener: () => {} };
  global.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) });

  const page = require(OUT);
  page.initNotifications({ on: () => {} }, () => 'r1');

  const clickDoc = (target) =>
    (docListeners.click || []).forEach((fn) => fn({ target }));
  return {
    panel, toggle, list, clear, elsewhere,
    isOpen: () => panel.classList.contains('open'),
    // A REAL click on an element inside the document fires that element's own
    // handlers and then the document's, in that order. Both, or the test proves
    // nothing about the interaction between them.
    click: (node) => { node.fire('click', { target: node }); clickDoc(node); },
    clickAway: (node) => clickDoc(node),
  };
}

let failed = 0;
function check(what, fn) {
  try { fn(); say('  ok   ' + what); }
  catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

say('notification bell: click away to dismiss');

check('the opening click survives the document handler', () => {
  const b = mount();
  b.click(b.toggle);
  assert.equal(b.isOpen(), true,
    'the panel closed in the same tick it opened — the document listener is '
    + 'treating the toggle as an outside click, and the bell looks dead');
});

check('a click elsewhere on the page closes it', () => {
  const b = mount();
  b.click(b.toggle);
  b.clickAway(b.elsewhere);
  assert.equal(b.isOpen(), false,
    'the panel is still open after clicking away, which is the reported bug');
});

check('a click inside the panel leaves it open', () => {
  const b = mount();
  b.click(b.toggle);
  b.clickAway(b.list);
  assert.equal(b.isOpen(), true,
    'clicking the list closed the panel; acknowledging an alert would make the '
    + 'panel vanish underneath the pointer');
});

check('Acknowledge and Clear all do not dismiss it', () => {
  const b = mount();
  b.click(b.toggle);
  b.clickAway(b.clear);
  assert.equal(b.isOpen(), true, 'Clear all closed the panel');
});

check('the bell still toggles shut on a second click', () => {
  const b = mount();
  b.click(b.toggle);
  b.click(b.toggle);
  assert.equal(b.isOpen(), false,
    'the original way of closing the panel stopped working');
});

check('clicking away while already closed is inert', () => {
  const b = mount();
  b.clickAway(b.elsewhere);
  assert.equal(b.isOpen(), false);
});

if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
say('\nall passed');
