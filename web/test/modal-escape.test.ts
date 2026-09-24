/**
 * Escape closes the TOPMOST dialog, not every dialog.
 *
 * ── THE BUG THIS EXISTS FOR ────────────────────────────────────────────────
 *
 * `wireModals` closed every dialog in the list on Escape, on the stated grounds
 * that removing a class an element does not have is a no-op. That held while no
 * dialog could open over another.
 *
 * One can now: the notification channel's Events tab has a gear that opens an
 * interface-type picker over it. Pressing Escape in the picker closed the
 * channel dialog with it and threw away every unsaved edit — and it read as the
 * picker doing something violent rather than Escape doing too much.
 *
 * Found by opening it in a browser. Nothing failed, because every test that
 * touched a dialog had exactly one open.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-modals.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'modals.ts'),
    '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function makeEl(id, z) {
  const classes = new Set();
  return {
    id, __z: z,
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
  };
}

/**
 * Mount the real `wireModals` over a shim.
 *
 * `getComputedStyle` is the shim's, because the module reads a z-index to decide
 * which dialog is on top. It answers from the element's own recorded value,
 * which is what a stylesheet would have given it.
 */
function mount(modals) {
  const els = {};
  for (const [id, z] of modals) els[id] = makeEl(id, z);
  const listeners = {};
  global.document = {
    getElementById: (id) => els[id] || null,
    addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    querySelectorAll: () => [],
  };
  global.getComputedStyle = (e) => ({ zIndex: e.__z });
  global.window = {};
  delete require.cache[require.resolve(OUT)];
  require(OUT).wireModals();
  const escape = () => (listeners.keydown || []).forEach((fn) => fn({ key: 'Escape' }));
  const open = (id) => els[id].classList.add('open');
  const isOpen = (id) => els[id].classList.contains('open');
  return { els, escape, open, isOpen };
}

// The two ids are real ones, and their z-indexes are the real ones too: the
// picker is 901 in app.css precisely so it stacks over the channel dialog's 900.
const STACK: Array<[string, string]> = [
  ['notifChanModal', '900'],
  ['nchanIfaceModal', '901'],
  // A REGISTERED DIALOG THAT SETS NO z-index OF ITS OWN, which is most of them.
  ['accountModal', 'auto'],
];

let failed = 0;
const checks = [];
function check(name, fn) { checks.push({ name, fn }); }

say('modals: Escape closes the topmost dialog');

check('one dialog open: Escape closes it', () => {
  const d = mount(STACK);
  d.open('notifChanModal');
  d.escape();
  assert.equal(d.isOpen('notifChanModal'), false);
});

// THE ONE THAT WAS BROKEN.
check('two stacked: Escape closes only the one on top', () => {
  const d = mount(STACK);
  d.open('notifChanModal');
  d.open('nchanIfaceModal');
  d.escape();
  assert.equal(d.isOpen('nchanIfaceModal'), false, 'the picker stayed open');
  assert.equal(d.isOpen('notifChanModal'), true,
    'Escape in the picker closed the channel dialog underneath it, throwing away '
    + 'every edit the operator had made and not yet saved');
});

check('a second Escape then closes the one underneath', () => {
  const d = mount(STACK);
  d.open('notifChanModal');
  d.open('nchanIfaceModal');
  d.escape();
  d.escape();
  assert.equal(d.isOpen('notifChanModal'), false,
    'the dialog underneath can no longer be closed with Escape at all');
});

// A dialog with no z-index of its own computes to 'auto', which parses NaN. It
// must rank as 0 rather than be dropped, or Escape would stop closing every
// dialog that does not set one — which is most of them.
check('a dialog with no z-index of its own still closes', () => {
  const d = mount(STACK);
  d.open('accountModal');
  d.escape();
  assert.equal(d.isOpen('accountModal'), false,
    'a dialog whose z-index computes to "auto" was skipped, so Escape does '
    + 'nothing for it');
});

check('Escape with nothing open does nothing and does not throw', () => {
  const d = mount(STACK);
  d.escape();
  assert.equal(d.isOpen('notifChanModal'), false);
});

(async () => {
  for (const c of checks) {
    try {
      await c.fn();
      say('  ok   ' + c.name);
    } catch (e) {
      failed += 1;
      say('  FAIL ' + c.name + '\n       ' + (e && e.message));
    }
  }
  if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
  say('\nall passed');
})();
