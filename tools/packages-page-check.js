'use strict';
/**
 * The PACKAGES page, live against ported. Built on `tools/lib/lift.js`.
 *
 * ── WHAT THIS PAGE CONTRIBUTES ──────────────────────────────────────────────
 *
 * A package's state cell is a small precedence ladder, and the top rung is the
 * one that matters: a package with a SCHEDULED action shows that action pending,
 * whatever its current state — because after a reboot it will not be in that
 * state any more. Below it sit installed, available and disabled.
 *
 * The pending card exists only while something is scheduled, and the firmware
 * block reports two different versions (running and upgradeable) that are equal
 * on a router with nothing to do. "Equal" and "absent" render differently, and
 * both are in the corpus.
 *
 * WHAT IT CANNOT SEE: layout.
 *
 * ── THE REBOOT FLOW IS DRIVEN NOW ───────────────────────────────────────────
 *
 * This line used to end "…and the reboot flow beyond the markup it produces".
 * That flow needed no layout and no timers: both sides attach to the button
 * itself, read the operator's answer from `window.prompt`, and emit. Nine cases
 * drive it, and the snapshot carries an EMIT TRAIL — the events sent, their
 * arguments, and the prompt text — because markup alone cannot see a button that
 * reboots a router.
 *
 * The prompt TEXT is compared for a reason: the router name in it is the whole
 * safety mechanism. A prompt naming the wrong router turns a hard mistake into an
 * easy one, and every pixel of the page would look identical.
 *
 * Eight mutations killed, including Apply skipping the permission check, Apply
 * sending after the prompt was cancelled, and the typed answer being replaced by
 * the router's own name — which would confirm any reboot automatically.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/packages-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/packages-page-check.js --freeze
const G = L.golden('packages-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = G.value('iife', () => L.region(src, {
  banner: '/* ── Packages page',
  must: ['packagesTable', 'pkgPendingCard', 'pkgFwBody'],
  mustNot: ['DNS page', 'Bridges page', 'Queues page', 'backupsPage'],
}));
const IDS = G.value('IDS', () => L.idsFor(src, iife));
if (process.argv.includes('--ids')) { console.log(JSON.stringify(IDS)); process.exit(0); }

const ENTRY = path.join(ROOT, 'testdata', '.pk-entry.ts');
fs.writeFileSync(ENTRY, "export { initPackagesPage } from '../web/src/pages/packages.js';\n");
const OUT = path.join(ROOT, 'testdata', '.pk-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const snap = (doc, emits) => {
  const n = doc.nodes;
  const out = {};
  // ── WHAT THE PAGE ASKED THE SERVER TO DO ────────────────────────────────
  //
  // Markup alone cannot see a button that reboots a router. `packages:apply`
  // carries the operator's typed confirmation, and the prompt naming the ROUTER
  // is what makes "the wrong router" a hard mistake rather than an easy one — so
  // the prompt text and the emitted arguments are compared, not just the table.
  if (emits) out.__emits = emits;
  for (const id of IDS.slice().sort()) {
    out[id] = n[id] ? { h: n[id].innerHTML, t: n[id].textContent,
      d: n[id].style && n[id].style.display, dis: n[id].disabled } : null;
  }
  return JSON.stringify(out);
};

function drive(doc, fire, script, o) {
  if (o.search) { doc.nodes.packagesSearch.value = o.search; }
  for (const [ev, payload] of script) fire(ev, payload);
  if (o.search) doc.nodes.packagesSearch.fire('input');
  for (const i of o.clicks || []) {
    const cells = doc.nodes.packagesThead.querySelectorAll('th');
    if (!cells[i]) throw new Error('no header cell at index ' + i);
    cells[i].click();
  }
  // The two write buttons. Fired on the button itself, which is where both
  // sides attach — no delegation here.
  for (const id of o.press || []) {
    if (!doc.nodes[id]) throw new Error('no button ' + id);
    doc.nodes[id].fire('click');
  }
}

/** The prompt answer a case wants, and the text it was asked. */
function makePrompt(answer, seen) {
  return (text) => { seen.push({ prompt: String(text) }); return answer; };
}

function liveRun(script, opts) {
  const o = opts || {};
  const doc = makeDoc(IDS, {});
  const handlers = {};
  const emits = [];
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, parseFloat, isFinite,
    document: doc,
    socket: { on: (ev, fn) => { handlers[ev] = fn; },
              emit: (ev, p) => { emits.push(p === undefined ? { ev } : { ev, p }); } },
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {},
    window: { prompt: makePrompt(o.typed, emits) },
    requestAnimationFrame: (fn) => { fn(); return 0; },
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function fmtBytes('),
    L.whole(src, 'function _sortMul('),
    L.whole(src, 'function _renderSortHeader('),
    'function $(id){return document.getElementById(id);}',
    'function pageVisible(){return true;}',
    'function _debounce(fn){return fn;}',
    L.declare(L.fileScopeEls(src, iife)),
    '(function () {' + iife + '})();',
  ].join('\n'), ctx);
  if (!handlers['packages:update']) {
    throw new Error('the live page registered no handler; ids it wanted and this gate does not ' +
      'provide: ' + ([...doc.unknown].join(', ') || 'none'));
  }
  drive(doc, (ev, p) => {
    if (!handlers[ev]) throw new Error('nothing subscribes ' + ev);
    handlers[ev](p);
  }, script, o);
  return snap(doc, o.press ? emits : null);
}

function portRun(script, opts) {
  const o = opts || {};
  const doc = makeDoc(IDS, {});
  const handlers = {};
  const emits = [];
  const prevWin = globalThis.window;
  const prevST = globalThis.setTimeout;
  const prevRaf = globalThis.requestAnimationFrame;
  globalThis.window = { prompt: makePrompt(o.typed, emits) };
  globalThis.setTimeout = ((fn) => { fn(); return 0; });
  globalThis.requestAnimationFrame = ((fn) => { fn(); return 0; });
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initPackagesPage({ on: (ev, fn) => { handlers[ev] = fn; },
        emit: (ev, p) => { emits.push(p === undefined ? { ev } : { ev, p }); } }, () => true);
      drive(doc, (ev, p) => {
        if (!handlers[ev]) throw new Error('the port does not subscribe ' + ev);
        handlers[ev](p);
      }, script, o);
      return snap(doc, o.press ? emits : null);
    });
  } finally {
    globalThis.requestAnimationFrame = prevRaf;
    globalThis.setTimeout = prevST;
    if (prevWin === undefined) delete globalThis.window; else globalThis.window = prevWin;
  }
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 3) {
    const A = JSON.parse(a), B = JSON.parse(b);
    for (const k of Object.keys(A)) {
      const x = JSON.stringify(A[k]), y = JSON.stringify(B[k]);
      if (x !== y) shout('DIFF %s [%s]\n  live: %s\n  port: %s', what, k,
        String(x).slice(0, 340), String(y).slice(0, 340));
    }
  }
}

const PK = (o) => Object.assign({
  name: 'routeros', version: '7.24', state: 'installed', size: 1048576,
  scheduled: '', scheduledAction: '', disabled: false, buildTime: '2026-08-01',
}, o);
const P = (o) => Object.assign({
  packages: [], firmware: {}, update: {}, available: true,
}, o);
const CAPS = { permitted: true };
const upd = (o) => [['packages:caps', CAPS], ['packages:update', P(o)]];

const CASES = {
  'nothing': [upd({}), {}],
  'one installed package': [upd({ packages: [PK({})] }), {}],
  'several packages': [upd({ packages: [PK({}), PK({ name: 'wireless' })] }), {}],
  // The state ladder, top rung first.
  'a scheduled package overrides its state': [upd({ packages: [
    PK({ scheduled: 'uninstall', scheduledAction: 'uninstall' })] }), {}],
  'scheduled with no action named': [upd({ packages: [PK({ scheduled: 'yes', scheduledAction: '' })] }), {}],
  'an available package': [upd({ packages: [PK({ state: 'available' })] }), {}],
  'a disabled package': [upd({ packages: [PK({ disabled: true })] }), {}],
  'a disabled package that is ALSO scheduled': [upd({ packages: [
    PK({ disabled: true, scheduled: 'enable', scheduledAction: 'enable' })] }), {}],
  'an unknown state': [upd({ packages: [PK({ state: 'wedged' })] }), {}],
  // Package fields.
  'no version': [upd({ packages: [PK({ version: '' })] }), {}],
  'zero size': [upd({ packages: [PK({ size: 0 })] }), {}],
  'no build time': [upd({ packages: [PK({ buildTime: '' })] }), {}],
  // The pending card appears only when something is scheduled.
  'nothing scheduled hides the pending card': [upd({ packages: [PK({})] }), {}],
  'one scheduled shows it': [upd({ packages: [PK({ scheduled: 'uninstall' })] }), {}],
  'several scheduled': [upd({ packages: [
    PK({ scheduled: 'uninstall' }), PK({ name: 'wireless', scheduled: 'enable' })] }), {}],
  // ── FIRMWARE AND UPDATE ───────────────────────────────────────────────────
  //
  // Field names read off the live `renderFirmware`, not guessed: my first
  // fixture invented `current`/`upgrade` and every firmware case rendered a
  // block of em dashes that the port matched exactly.
  //
  // The whole RouterBOARD section is behind `isRouterboard`, which is the branch
  // worth having: a CHR or an x86 install has no firmware to report, and showing
  // "—" for a thing that does not exist reads as a fault.
  'a routerboard with firmware up to date': [upd({ firmware: {
    isRouterboard: true, currentFirmware: '7.24', upgradeAvailable: false,
    minimumFirmware: '6.40' } }), {}],
  'a routerboard with an upgrade': [upd({ firmware: {
    isRouterboard: true, currentFirmware: '7.23', upgradeAvailable: true,
    upgradeFirmware: '7.24', minimumFirmware: '6.40' } }), {}],
  'NOT a routerboard hides the firmware rows': [upd({ firmware: { isRouterboard: false } }), {}],
  'no firmware information at all': [upd({ firmware: {} }), {}],
  'a routerboard with no minimum firmware': [upd({ firmware: {
    isRouterboard: true, currentFirmware: '7.24', minimumFirmware: '' } }), {}],
  'an update available': [upd({ update: {
    installedVersion: '7.23', latestVersion: '7.24', updateAvailable: true,
    channel: 'stable', status: 'New version is available' } }), {}],
  'up to date': [upd({ update: {
    installedVersion: '7.24', updateAvailable: false, channel: 'stable',
    status: 'System is already up to date' } }), {}],
  'no update information': [upd({ update: {} }), {}],
  'an update with no channel': [upd({ update: { installedVersion: '7.24', channel: '' } }), {}],
  'an update status with markup': [upd({ update: { status: '<b>x</b>' } }), {}],
  // Permission and control payloads.
  'a viewer': [[['packages:caps', { permitted: false }], ['packages:update', P({ packages: [PK({})] })]], {}],

  // ── THE TWO WRITE BUTTONS, AND WHAT THEY ASK THE SERVER ──────────────────
  //
  // "the reboot flow beyond the markup it produces" was on this gate's CANNOT
  // SEE list. It needed no layout and no timers: both sides attach to the button
  // itself, take the operator's answer from `window.prompt`, and emit. All three
  // are comparable, and this is the page's highest-consequence path — Apply
  // reboots a production router.
  //
  // The PROMPT TEXT is compared as well as the emit, because the router NAME in
  // it is the whole safety mechanism: a prompt that names the wrong router turns
  // a hard mistake into an easy one, and the markup would look identical.
  'apply, confirmed with the router name': [
    [['packages:caps', { permitted: true, routerName: 'br-01' }], ['packages:update', P({})]],
    { press: ['pkgApplyBtn'], typed: 'br-01' }],
  'apply, confirmed with the WRONG name — still sent, the server decides': [
    [['packages:caps', { permitted: true, routerName: 'br-01' }], ['packages:update', P({})]],
    { press: ['pkgApplyBtn'], typed: 'br-02' }],
  // Cancelling the prompt returns null, and NOTHING may be emitted.
  'apply, cancelled': [
    [['packages:caps', { permitted: true, routerName: 'br-01' }], ['packages:update', P({})]],
    { press: ['pkgApplyBtn'], typed: null }],
  'apply, confirmed with an empty string': [
    [['packages:caps', { permitted: true, routerName: 'br-01' }], ['packages:update', P({})]],
    { press: ['pkgApplyBtn'], typed: '' }],
  // A router with no name: the prompt still has to be coherent.
  'apply on a router with no name': [
    [['packages:caps', { permitted: true }], ['packages:update', P({})]],
    { press: ['pkgApplyBtn'], typed: '' }],
  // A VIEWER pressing Apply must reach neither the prompt nor the wire.
  'a viewer presses apply': [
    [['packages:caps', { permitted: false, routerName: 'br-01' }], ['packages:update', P({})]],
    { press: ['pkgApplyBtn'], typed: 'br-01' }],
  'check for updates': [
    [['packages:caps', { permitted: true, routerName: 'br-01' }], ['packages:update', P({})]],
    { press: ['pkgCheckBtn'] }],
  // ...and a viewer pressing Check gets the message rather than a request.
  'a viewer presses check': [
    [['packages:caps', { permitted: false }], ['packages:update', P({})]],
    { press: ['pkgCheckBtn'] }],
  'check then apply': [
    [['packages:caps', { permitted: true, routerName: 'br-01' }], ['packages:update', P({})]],
    { press: ['pkgCheckBtn', 'pkgApplyBtn'], typed: 'br-01' }],
  'an ok message': [[...upd({ packages: [PK({})] }), ['packages:ok', { action: 'check' }]], {}],
  'an error message': [[...upd({ packages: [PK({})] }), ['packages:error', { code: 'denied', message: 'no' }]], {}],
  'an error with markup': [[...upd({ packages: [PK({})] }), ['packages:error', { code: 'x', message: '<b>&</b>' }]], {}],
  // Search.
  'search by name': [upd({ packages: [PK({}), PK({ name: 'wireless' })] }), { search: 'wire' }],
  'search matching nothing': [upd({ packages: [PK({})] }), { search: 'zzzz' }],
  'search is lowercased': [upd({ packages: [PK({})] }), { search: 'ROUTEROS' }],
  // Escaping.
  'markup in a package name': [upd({ packages: [PK({ name: '<img src=x>' })] }), {}],
  'a quote in a scheduled action': [upd({ packages: [PK({ scheduled: 'a"b', scheduledAction: 'a"b' })] }), {}],
  // Sorting.
  'sorted by the first column': [upd({ packages: [PK({ name: 'z' }), PK({ name: 'a' })] }), { clicks: [0] }],
  'first column descending': [upd({ packages: [PK({ name: 'z' }), PK({ name: 'a' })] }), { clicks: [0, 0] }],
};

for (const [name, [script, opts]] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live(name, () => liveRun(script, opts)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(script, opts); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(G.live('auto:8', () => liveRun(upd({ packages: [PK({})] }), {})));
  assert.match(s.packagesTable.h, /routeros/, 'the live package table rendered no row');
  assert.match(s.packagesTable.h, /installed/, 'the state cell is missing');
  assert.equal(s.packagesBadge.t, '1', 'the badge is ' + s.packagesBadge.t);
}
{
  // A scheduled package shows the SCHEDULED action, not its current state —
  // after the reboot it will not be in that state any more.
  const sched = JSON.parse(G.live('auto:7', () => liveRun(upd({ packages: [
    PK({ state: 'installed', scheduled: 'uninstall', scheduledAction: 'uninstall' })] }), {})));
  assert.match(sched.packagesTable.h, /uninstall pending/,
    'a scheduled package did not show its pending action');
  assert.ok(!/wl-band-6">installed/.test(sched.packagesTable.h),
    'a scheduled package still claimed to be installed');
}
{
  // The pending card is display:none until something is scheduled.
  const idle = JSON.parse(G.live('auto:6', () => liveRun(upd({ packages: [PK({})] }), {})));
  const busy = JSON.parse(G.live('auto:5', () => liveRun(upd({ packages: [PK({ scheduled: 'uninstall' })] }), {})));
  assert.equal(idle.pkgPendingCard.d, 'none', 'the pending card showed with nothing scheduled');
  assert.notEqual(busy.pkgPendingCard.d, 'none', 'the pending card stayed hidden with work pending');
}
{
  // A ROUTERBOARD reports firmware; anything else has none to report, and the
  // rows are absent rather than showing "—" for a thing that does not exist.
  const rb = JSON.parse(G.live('auto:4', () => liveRun(upd({ firmware: {
    isRouterboard: true, currentFirmware: '7.24', minimumFirmware: '6.40' } }), {})));
  const chr = JSON.parse(G.live('auto:3', () => liveRun(upd({ firmware: { isRouterboard: false } }), {})));
  assert.match(rb.pkgFwBody.h, /Firmware/, 'a routerboard reported no firmware row');
  assert.ok(!/Minimum firmware/.test(chr.pkgFwBody.h),
    'a non-routerboard was given firmware rows it has nothing to fill');
  assert.notEqual(rb.pkgFwBody.h, chr.pkgFwBody.h, 'both rendered the same block');
}
{
  // An available update is called out, and the same block without one is not.
  const avail = JSON.parse(G.live('auto:2', () => liveRun(upd({ update: {
    installedVersion: '7.23', latestVersion: '7.24', updateAvailable: true } }), {})));
  const current = JSON.parse(G.live('auto:1', () => liveRun(upd({ update: {
    installedVersion: '7.24', updateAvailable: false } }), {})));
  assert.match(avail.pkgFwBody.h, /7\.23 → 7\.24/, 'an available update did not show the arrow');
  assert.ok(!/→/.test(current.pkgFwBody.h), 'an up-to-date router was shown an upgrade arrow');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('packages-page-check: %d cases identical', checked);
