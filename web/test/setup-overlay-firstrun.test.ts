/**
 * The first-run wizard is reachable on a first run.
 *
 * ── AN OVERLAY THAT COULD ONLY BE SHOWN BY DELETING SOMETHING ──────────────
 *
 * `#setupOverlay` asks for a router's host, port and credentials, and it was
 * shown on one trigger only: the `setup:required` broadcast, which the server
 * sends when the LAST router is DELETED. So it appeared for an operator who
 * removed their only device, and never for one who had never added a device at
 * all — the case it exists for.
 *
 * A new operator saw the dashboard instead, drawn in full with every card empty
 * and nothing saying a router was needed. Reported on issue #124 with a
 * screenshot of exactly that.
 *
 * `showSetupOverlayNow()` is the direct call `main()` makes when the fleet it
 * just fetched is empty. Emitting the event on connect was tried first and
 * RACED: the frame arrives while `main()` is still awaiting its fetches, before
 * anything has subscribed, so the overlay stayed hidden. That failure is the
 * reason this is a function call and not a message, and it is why the test
 * drives the function.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

function makeEl(id) {
  const classes = new Set();
  const listeners = {};
  const node = {
    id, value: '', textContent: '', innerHTML: '', type: 'text',
    style: {}, hidden: false,
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
    addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    fire: (ev, a) => (listeners[ev] || []).forEach((f) => f(a)),
    has: (c) => classes.has(c),
    focus() {}, closest: () => null,
  };
  return node;
}

const IDS = ['setupOverlay', 'setupHost', 'setupPort', 'setupUser', 'setupPass',
  'setupLabel', 'setupWan', 'setupPing', 'setupTls', 'setupInsecure',
  'setupTestBtn', 'setupSaveBtn', 'setupTestResult'];

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-setup-firstrun.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'setup-overlay-wire.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

/** `present` false omits the overlay markup entirely. */
function mount(present) {
  const els = {};
  IDS.forEach((id) => { if (present || id !== 'setupOverlay') els[id] = makeEl(id); });
  // `body` IS REQUIRED: showOverlay dims the app behind the wizard with
  // `document.body.classList.add('is-disconnected')`, so a shim without it
  // throws before the overlay is ever shown.
  global.document = {
    getElementById: (id) => els[id] || null,
    querySelectorAll: () => [], addEventListener: () => {},
    body: makeEl('body'),
  };
  global.window = { addEventListener: () => {} };
  global.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) });
  const page = require(OUT);
  return { els, page };
}

let failed = 0;
function check(what, fn) {
  try { fn(); say('  ok   ' + what); }
  catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

say('setup overlay: a first run gets the wizard');

check('it is hidden until something shows it', () => {
  const { els } = mount(true);
  assert.notEqual(els.setupOverlay.style.display, 'block',
    'the overlay is shown before anyone asked for it, which would cover a '
    + 'working dashboard on every load');
});

check('showSetupOverlayNow opens it', () => {
  const { els, page } = mount(true);
  page.showSetupOverlayNow();
  assert.equal(els.setupOverlay.style.display, 'block',
    'the direct call did not show the overlay, so an install with no routers '
    + 'still lands on an empty dashboard with no way to know why');
  assert.ok(document.body.has('is-disconnected'),
    'the app behind the wizard was not dimmed, so the empty dashboard stays '
    + 'interactive underneath it');
});

check('it does not need the socket event', () => {
  // No initSetupOverlay call at all: showing must not depend on wiring that
  // only happens later, which is the race the event-based attempt had.
  const { els, page } = mount(true);
  page.showSetupOverlayNow();
  assert.equal(els.setupOverlay.style.display, 'block');
});

check('markup absent is survived, not thrown', () => {
  const { page } = mount(false);
  page.showSetupOverlayNow();   // must not throw
});

check('the socket trigger still works, for a fleet emptied elsewhere', () => {
  const { els, page } = mount(true);
  const handlers = {};
  page.initSetupOverlay({ on: (ev, cb) => { handlers[ev] = cb; } });
  assert.ok(handlers['setup:required'],
    'nothing subscribed to setup:required; deleting the last router in another '
    + 'tab would leave this browser on a dashboard for a fleet that is gone');
  handlers['setup:required']();
  assert.equal(els.setupOverlay.style.display, 'block');
});

if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
say('\nall passed');
