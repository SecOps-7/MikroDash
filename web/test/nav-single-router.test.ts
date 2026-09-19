/**
 * DEVICES IS VISIBLE ON A ONE-DEVICE INSTALL.
 *
 * ── THE BUG THIS REPLACES ───────────────────────────────────────────────────
 *
 * `applyPageVisibility` used to AND in a fourth term:
 *
 *     const byCount = pageName !== 'devices' || routersMultiple;
 *
 * driven in the live app by `_routers.length > 1` (`public/app.js:7995`). A
 * fleet page was judged meaningless for a fleet of one, so the nav entry simply
 * was not drawn.
 *
 * It was reported as a bug (issue #121) by an operator whose only device was a
 * CHR: no Devices entry until they added a second router. They read it as the
 * app failing to recognise a virtual router, and it was not — the rule never
 * looked at the type, only the count, and any second device would have done it.
 *
 * In the port the rule was already DEAD: its setter lost its last caller when
 * the parity harness went, so the flag stayed true and the term always passed.
 * A gate that reads as live and cannot fire is worse than either behaviour,
 * which is why it was removed rather than rewired.
 *
 * ── WHY A TEST AND NOT JUST A DELETION ──────────────────────────────────────
 *
 * Deleting a gate and deleting a CHECK look identical in a diff. This is what
 * makes the difference visible: the behaviour is now asserted, so re-introducing
 * any term that hides Devices fails here rather than shipping.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.nav-entry.ts');
fs.writeFileSync(ENTRY, "export { applyPageVisibility } from '../web/src/caps.js';\n");
const OUT = path.join(ROOT, 'testdata', '.nav.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const SEL = '.nav-item[data-page="devices"]';

/** A document holding one Devices nav item, and nothing else of note. */
function boot() {
  const doc = makeDoc([], { allowUnknown: ['acctMyAlerts', '.nav-item[data-page=*'], query: { [SEL]: [{ id: 'nav-devices' }], '.nav-group': [] } });
  const prevDoc = global.document;
  const prevWin = global.window;
  global.document = doc;
  global.window = { addEventListener: () => {} };
  const { applyPageVisibility } = require(OUT);
  return {
    doc,
    apply: (pages) => applyPageVisibility(pages),
    navItem: () => doc.queryNodes[SEL][0],
    restore: () => { global.document = prevDoc; global.window = prevWin; },
  };
}

// ── the property ────────────────────────────────────────────────────────────
//
// No router count is supplied at all, which is the point: there is no longer any
// input by which the fleet size could reach this decision.
{
  const { apply, navItem, restore } = boot();
  apply({});
  const display = navItem().style.display;
  restore();
  // `=== ''` AND NOT `!== 'none'`. The shim starts `style` as an empty object, so
  // `style.display` is `undefined` until something writes it — and
  // `undefined !== 'none'` passes. A page that stopped writing display at all,
  // or a selector that stopped matching, would satisfy the looser assertion
  // while rendering nothing. Assert the value actually written.
  assert.strictEqual(display, '',
    'the Devices nav entry is hidden on an install with one device. That was ' +
    'issue #121, and the fleet-size rule behind it was deleted deliberately.');
  say('ok  Devices is in the nav with no router count supplied');
}

// ── and the gate that SHOULD still hide it is untouched ─────────────────────
//
// The deletion must not have turned the entry into one nothing can hide. A test
// that only proved "always shown" would pass against a function that had lost
// its filtering altogether, so the surviving install toggle is exercised too.
{
  const { apply, navItem, restore } = boot();
  apply({ pageDevices: false });          // the install's own Visible Pages toggle
  const display = navItem().style.display;
  restore();
  assert.strictEqual(display, 'none',
    'switching Devices off in Visible Pages no longer hides it — the install ' +
    'toggle has stopped working, which is a worse bug than the one being fixed');
  say('ok  the Visible Pages toggle still hides Devices');
}

fs.rmSync(OUT, { force: true });
say('nav-single-router: all checks passed');
