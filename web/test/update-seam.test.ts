// Moved from `tools/update-seam-check.js` when the port-parity harness was retired.
//
// The body is VERBATIM apart from its imports and the path to the repository
// root: this test drives the port's OWN TypeScript and asserts what it does, so
// nothing in it referred to the implementation this app replaced.
/**
 * DO THE TWO HALVES OF `mikrodash:updateavailable` ACTUALLY FIT?
 *
 * The System card publishes what it drew — installed version, latest version,
 * channel — and the Upgrade dialog listens for it and fills its fields from the
 * `detail`. Two modules, one event, and NOTHING drove the join:
 *
 *   - `system-card-check` counts DISPATCHES. It proves the card fires once per
 *     changed row, and nothing about what it fires.
 *   - `upgrade-dialog-check` never mentions the event; it seeds the dialog by
 *     other means.
 *   - Four gates stub `document.dispatchEvent` as `() => true`, which is what
 *     made the gap invisible: an announcement nobody hears looks the same as one
 *     nobody sends.
 *
 * ── WHY THE COMPILER CANNOT DO THIS ─────────────────────────────────────────
 *
 * `CustomEvent.detail` is `any`. The dialog casts it (`detail as UpdInfo`), so
 * the two shapes could diverge and TypeScript would say nothing — the dialog
 * would simply show a dash where a version belongs. They were separately
 * declared interfaces that happened to agree; they now name one exported type,
 * which closes the drift but not the cast.
 *
 * This is the browser twin of `internal/geoplace/lookup_test.go`, which exists
 * for the same reason on the Go side: "both sides pass their own tests, and the
 * seam between them is discovered at the call site much later."
 *
 * ── WHAT IT DRIVES ──────────────────────────────────────────────────────────
 *
 * The REAL System card renderer, a REAL document that delivers events, and the
 * REAL Upgrade listener. Nothing is hand-built except the payload the socket
 * would have carried. The assertions are on what the dialog SHOWS after the
 * Update button is pressed, because that is what an operator reads before
 * upgrading a router.
 *
 *   node tools/update-seam-check.js
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.seam-entry.ts');
fs.writeFileSync(ENTRY, [
  "export { noteSystemUpdate, flushSysUpdate, resetSysMeta } from '../web/src/pages/dashboard-system.js';",
  "export { initUpgrade } from '../web/src/pages/upgrade.js';",
].join('\n') + '\n');
const OUT = path.join(ROOT, 'testdata', '.seam.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['rosUpdateRow', 'sysUpdateAction', 'sysUpdateBtn', 'updModal',
  'upd_from', 'upd_to', 'upd_channel', 'upd_confirm', 'upd_error', 'upd_go',
  'upd_cancel', 'upd_state', 'upd_note'];

// ── WHAT THIS GATE COVERS ───────────────────────────────────────────────────
//
// A SUBSET of IDS, not IDS itself. The shim provides thirteen so the page can
// run; this gate ASSERTS on five and presses one. Claiming the rest would tell
// `element-coverage-audit` that `#upd_confirm` and `#upd_go` are covered when
// nothing here looks at them — and that audit is what decides where the next
// gate goes, so an overstatement there costs more than the silence it replaces.
const COVERS = ['rosUpdateRow', 'updModal', 'upd_from', 'upd_to', 'upd_channel', 'sysUpdateBtn',
  'upd_error', 'upd_go'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

function run(payload, o) {
  const opts = o || {};
  const doc = makeDoc(IDS, {});
  const emits = [];
  const handlers = {};
  const prev = { doc: globalThis.document, win: globalThis.window };
  globalThis.document = Object.assign(doc, { hidden: false });
  globalThis.window = {};
  const prevRaf = globalThis.requestAnimationFrame;
  globalThis.requestAnimationFrame = ((fn) => { fn(); return 0; });
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    mod.resetSysMeta();
    // The dialog subscribes FIRST, exactly as `main.ts` mounts it — a listener
    // registered after the card has already drawn hears nothing, and that
    // ordering bug is one this gate can see.
    mod.initUpgrade({ on: (ev, fn) => { handlers[ev] = fn; },
                      emit: (ev, p) => { emits.push({ ev, p }); } });
    // Through the real socket path: `noteSystemUpdate` books a frame and
    // `flushSysUpdate` draws it. Calling the renderer directly would skip the
    // rAF coalescing that decides how often the announcement fires.
    mod.noteSystemUpdate(payload);

    // Press Update, which is where the dialog reads what it was told.
    const btn = { closest: (sel) => (sel === '#sysUpdateBtn' ? btn : null) };
    doc.dispatch('click', btn);

    // ── THE REFUSAL PATH ────────────────────────────────────────────────
    //
    // `upgradeErrorText` is gated as a pure function by `upgrade-dialog-check`,
    // and nothing checked that its answer REACHES the box. That is the same
    // shape as this file's own reason for existing: two halves, each tested
    // alone. `#upd_error` was reported uncovered the moment this gate started
    // declaring what it actually asserts.
    if (opts.error) {
      if (opts.closeFirst) doc.nodes.updModal.classList.remove('open');
      if (handlers['packages:error']) handlers['packages:error'](opts.error);
      // REOPENING MUST CLEAR IT. The operator corrects the name and presses
      // Update again; a refusal left on screen from the previous attempt reads
      // as a refusal of THIS one. Deleting the `display = 'none'` on the open
      // path survived every case until this second press existed.
      if (opts.reopen) doc.dispatch('click', btn);
      // ...and so must pressing Go. The operator corrects the name in place and
      // issues again without closing; a refusal from the previous attempt left
      // showing beside a running install is worse than one left on an idle
      // dialog. Deleting THAT `display = 'none'` survived until this existed.
      if (opts.pressGo) {
        const go = { closest: (sel) => (sel === '#upd_go' ? go : null) };
        for (let i = 0; i < (opts.pressGo === true ? 1 : opts.pressGo); i++) {
          doc.dispatch('click', go);
        }
      }
    }
  } finally {
    if (prev.doc === undefined) delete globalThis.document; else globalThis.document = prev.doc;
    if (prev.win === undefined) delete globalThis.window; else globalThis.window = prev.win;
    if (prevRaf === undefined) delete globalThis.requestAnimationFrame;
    else globalThis.requestAnimationFrame = prevRaf;
  }
  const t = (id) => (doc.nodes[id] ? doc.nodes[id].textContent : null);
  return {
    row: doc.nodes.rosUpdateRow.innerHTML,
    open: doc.nodes.updModal.classList.contains('open'),
    from: t('upd_from'), to: t('upd_to'), channel: t('upd_channel'),
    error: t('upd_error'), errorShown: doc.nodes.upd_error.style.display,
    emits,
  };
}

const P = (o) => Object.assign({
  version: '7.24.1', boardName: 'hAP ax^3', cpuCount: 4, totalMem: 1073741824,
  updateAvailable: true, latestVersion: '7.25', updateChannel: 'stable',
}, o);

const problems = [];
const check = (name, got, want) => {
  for (const k of Object.keys(want)) {
    if (JSON.stringify(got[k]) !== JSON.stringify(want[k])) {
      problems.push(name + ': ' + k + ' = ' + JSON.stringify(got[k]) +
                    ', want ' + JSON.stringify(want[k]));
    }
  }
};

// ── AN UPDATE IS OFFERED ────────────────────────────────────────────────────
{
  const r = run(P({}));
  // BELIEVABILITY FIRST: the card must have drawn the amber row, or the event
  // never fired and every assertion below is about an untouched dialog.
  assert.match(r.row, /ros-update-row/, 'the System card drew no update row');
  assert.ok(r.open, 'the Update dialog did not open — the click never reached it');
  check('an update is offered', r, {
    from: '7.24.1', to: '7.25', channel: 'channel: stable',
  });
  // The dialog asks for permission on hearing the announcement; that emit is
  // the proof the LISTENER ran, not just that the dialog opened.
  assert.ok(r.emits.some((e) => e.ev === 'packages:caps'),
    'the dialog never asked for caps — its listener did not run');
}

// ── NO CHANNEL ──────────────────────────────────────────────────────────────
{
  const r = run(P({ updateChannel: '' }));
  check('no channel', r, { from: '7.24.1', to: '7.25', channel: '' });
}

// ── A VERSION WITH A BUILD SUFFIX ───────────────────────────────────────────
//
// The card publishes the INSTALLED BASE, not the raw version string, and the
// dialog shows whatever it is given. A seam that lost the base-stripping would
// show the operator a "from" they do not recognise.
{
  const r = run(P({ version: '7.24.1 (stable)' }));
  check('a version with a suffix', r, { to: '7.25' });
  assert.ok(r.from && r.from !== '—',
    'the dialog showed no installed version for a suffixed version string');
}

// ── NO UPDATE AVAILABLE ─────────────────────────────────────────────────────
//
// The card must NOT announce, so the dialog keeps its dashes. Two empty strings
// would compare equal here, which is why the offered case above asserts real
// versions first.
{
  const r = run(P({ updateAvailable: false, latestVersion: '7.24.1' }));
  check('no update available', r, { from: '—', to: '—' });
  assert.ok(!r.emits.some((e) => e.ev === 'packages:caps'),
    'the dialog asked for caps although nothing was announced');
}

// ── A REFUSAL REACHES THE BOX ───────────────────────────────────────────────
{
  const r = run(P({}), { error: { code: 'confirm-mismatch', routerName: 'br-01' } });
  check('a refusal', r, { errorShown: '' });
  if (!r.error || !r.error.includes('br-01')) {
    problems.push('the refusal box reads ' + JSON.stringify(r.error) +
                  ' — the router name the server sent is not in it');
  }
  // BELIEVABILITY: an empty box and a hidden box are different, and a gate that
  // only checked the text would pass on a box nobody can see.
  assert.notEqual(r.errorShown, 'none', 'the refusal was written into a hidden box');
}
{
  // Each code says something different; a single fallback for all of them would
  // pass a check that only asserted "some text appeared".
  const a = run(P({}), { error: { code: 'nothing-to-update' } });
  const b = run(P({}), { error: { code: 'router-write-policy' } });
  if (a.error === b.error) {
    problems.push('two different refusal codes produced the same sentence: ' +
                  JSON.stringify(a.error));
  }
}
{
  const r = run(P({}), { error: { code: 'confirm-mismatch', routerName: 'br-01' }, reopen: true });
  // HIDDEN, not cleared. The live app sets `display = 'none'` and leaves the
  // text (`../MikroDash/public/app.js:15287`), so asserting the box is EMPTY
  // would fail a faithful port — this check said exactly that on its first run,
  // and the fix was to read the original rather than to change the port.
  if (r.errorShown !== 'none') {
    problems.push('reopening the dialog left the previous refusal VISIBLE (display=' +
                  JSON.stringify(r.errorShown) + ') — it reads as a refusal of the new attempt');
  }
}
{
  const r = run(P({}), { error: { code: 'confirm-mismatch', routerName: 'br-01' }, pressGo: true });
  if (r.errorShown !== 'none') {
    problems.push('issuing again left the previous refusal VISIBLE (display=' +
                  JSON.stringify(r.errorShown) + ')');
  }
  // BELIEVABILITY: Go must actually have fired, or the hidden box above proves
  // nothing about the Go path.
  if (!r.emits.some((e) => e.ev === 'packages:upgrade')) {
    problems.push('pressing Go issued nothing — the button click did not reach the handler');
  }
}
{
  // ONE COMMAND PER DIALOG, not one per click. `apply('issuing')` disables the
  // button, and that disabled check is the only thing between an impatient
  // second click and a second install on a rebooting router. A single press
  // cannot see it — the guard only matters on the press after.
  const r = run(P({}), { error: { code: 'confirm-mismatch', routerName: 'br-01' }, pressGo: 3 });
  const issued = r.emits.filter((e) => e.ev === 'packages:upgrade').length;
  if (issued !== 1) {
    problems.push('three clicks on Go issued ' + issued + ' upgrade command(s), want 1');
  }
}
{
  // A refusal arriving while the dialog is CLOSED must not be painted into it —
  // `packages:error` is shared with the Packages page, and a message written
  // where nobody will see it also leaves the page's own unshown.
  const r = run(P({}), { error: { code: 'denied' }, closeFirst: true });
  if (r.error) {
    problems.push('a refusal was painted into a CLOSED dialog: ' + JSON.stringify(r.error));
  }
}

fs.rmSync(OUT, { force: true });
if (problems.length) {
  for (const p of problems) console.error('  ' + p);
  console.error('\nupdate-seam-check: ' + problems.length + ' problem(s)');
  process.exit(1);
}
console.log('update-seam-check: the System card\'s announcement reaches the Upgrade dialog intact');
