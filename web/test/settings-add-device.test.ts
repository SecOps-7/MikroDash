/**
 * The Add Device button opens the router modal.
 *
 * ── A BUTTON THAT RENDERS AND DOES NOTHING ─────────────────────────────────
 *
 * `#rtrAddBtn` lives in `page-settings.html` and is SHOWN by `caps.ts`, so it
 * appeared on the Devices tab, enabled, with no listener bound anywhere in the
 * app. Clicking it did nothing and logged nothing.
 *
 * On an install that already has routers that is an annoyance. On a NEW one it
 * is a dead end: the setup overlay is the only other route into the router
 * modal, so an operator who dismissed it had no way to add their first router.
 * Reported on issue #124 by someone who had just built the container and
 * created their account.
 *
 * Two properties, and the second is the one that would otherwise rot:
 *
 *   1. clicking Add calls the opener with NULL, which is what `router-modal.ts`
 *      reads as "this is an add, make them Test before Save". Passing a router
 *      here would silently turn Add into Edit.
 *   2. the binding survives a MISSING `rtrTbody`. It is registered before the
 *      early return that guards the table, and moving it below would make Add
 *      depend on markup it does not use.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

function makeEl(id) {
  const listeners = {};
  return {
    id,
    innerHTML: '',
    textContent: '',
    style: {},
    dataset: {},
    addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    fire: (ev, arg) => (listeners[ev] || []).forEach((fn) => fn(arg)),
    bound: (ev) => (listeners[ev] || []).length,
    closest: () => null,
  };
}

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-settings-add-device.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings-routers.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

/** Mount the real table module. `withTbody` false omits the table entirely. */
function mount(withTbody) {
  const els = { rtrAddBtn: makeEl('rtrAddBtn') };
  if (withTbody) els.rtrTbody = makeEl('rtrTbody');
  global.document = {
    getElementById: (id) => els[id] || null,
    querySelectorAll: () => [],
    addEventListener: () => {},
  };
  global.window = { addEventListener: () => {} };
  global.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) });

  const opened = [];
  const page = require(OUT);
  page.initSettingsRoutersTable({
    routers: () => [{ id: 'r1', label: 'One', host: '198.51.100.1' }],
    activeId: () => 'r1',
    status: () => ({ r1: true }),
    sitesById: () => ({}),
    openModal: (r) => opened.push(r),
  });
  return { els, opened };
}

let failed = 0;
function check(what, fn) {
  try { fn(); say('  ok   ' + what); }
  catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

say('settings: the Add Device button opens the router modal');

check('the button is bound at all', () => {
  const { els } = mount(true);
  assert.equal(els.rtrAddBtn.bound('click'), 1,
    'nothing listens for a click on #rtrAddBtn, so the only control that adds '
    + 'the first router on a new install does nothing at all');
});

check('clicking it opens the ADD form, not an edit', () => {
  const { els, opened } = mount(true);
  els.rtrAddBtn.fire('click', {});
  assert.equal(opened.length, 1, 'the opener was not called');
  assert.strictEqual(opened[0], null,
    'the opener got ' + JSON.stringify(opened[0]) + ' instead of null; '
    + 'router-modal.ts reads a non-null argument as an EDIT and calls '
    + 'gate.pass(), which would let an unverified router be saved without Test');
});

check('it still works when the table markup is absent', () => {
  const { els, opened } = mount(false);
  assert.equal(els.rtrAddBtn.bound('click'), 1,
    'the binding was skipped because #rtrTbody is missing; Add does not use the '
    + 'table, so it must be registered before that early return');
  els.rtrAddBtn.fire('click', {});
  assert.strictEqual(opened[0], null);
});

if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
say('\nall passed');
