#!/usr/bin/env node
'use strict';
/**
 * The topbar router picker, live against ported.
 *
 * ── WHY IT EXISTS ───────────────────────────────────────────────────────────
 *
 * Because it did not, and the port shipped no working router switcher on
 * desktop for several iterations. `shell.html` has never been scanned by
 * `wiring-audit` (its filter is `page-*.html`), so the six dropdown ids sat
 * among 125 unreferenced ones nobody had counted.
 *
 * ── THE THREE PURE PIECES ───────────────────────────────────────────────────
 *
 * `_rtrLabel`, `_ddRouters` and `renderDropdown` are lifted from app.js by
 * their declarations and run against the port's `rtrLabel`, `filterRouters` and
 * `dropdownHtml`. The renderer is compared as MARKUP, which is what makes the
 * status dot's three states and the active row's tick checkable at all.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/router-dropdown-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('router-dropdown-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

function lift(decl, name, must, mustNot, max) {
  const at = src.indexOf(decl);
  assert.ok(at > 0, name + ' has moved in app.js');
  const end = src.indexOf('\n  }', at);
  assert.ok(end > at && end - at < max, name + ' is not where its anchors say');
  const body = src.slice(at, end + 4);
  for (const m of must) assert.ok(body.includes(m), name + ' lost: ' + m);
  for (const m of mustNot) assert.ok(!body.includes(m), name + ' over-read and took in: ' + m);
  return body;
}

const labelSrc = G.value('labelSrc', () => lift('function _rtrLabel(r) {', '_rtrLabel', ['replace'], ['_ddRouters'], 300));
const filterSrc = G.value('filterSrc', () => lift('function _ddRouters() {', '_ddRouters', ['disabled', 'toLowerCase'], ['renderDropdown'], 600));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['labelSrc', labelSrc], ['filterSrc', filterSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}
// The overlay's rule lives inside two socket handlers rather than a function,
// so it is reproduced here from the two lines that carry it — with the source
// asserted so a change over there breaks this rather than drifting past it.
const OVL_OPEN = "if (switchOvl) switchOvl.classList.add('open');";
const OVL_FALSE = 'if (_switchFalseCount > 1) switchOvl.classList.remove(\'open\');';
// GUARDED: a question about the live SOURCE.
if (LIFT.hasReference(ROOT)) assert.ok(src.includes(OVL_OPEN), 'the overlay open line has moved in app.js');
// GUARDED: a question about the live SOURCE.
if (LIFT.hasReference(ROOT)) assert.ok(src.includes(OVL_FALSE), 'the overlay second-false rule has moved in app.js');
if (LIFT.hasReference(ROOT)) assert.ok(src.includes("if (data.connected) {\n      if (switchOvl) switchOvl.classList.remove('open');"),
  'the overlay close-on-connected rule has moved in app.js');

// FROZEN — `lift` both slices AND asserts, so its result is a lifted VALUE the
// comparison consumes.
const renderSrc = G.value('renderSrc', () => lift('function renderDropdown() {', 'renderDropdown',
  ['rtr-dd-item', 'rtr-dd-dot', 'rtr-dd-check', 'aria-selected'], ['updateDropdownLabel'], 1600));
if (!renderSrc || renderSrc.length < 40) throw new Error('the recorded renderSrc is empty');

const ENTRY = path.join(ROOT, 'testdata', '.rtrdd-entry.ts');
const OUT = path.join(ROOT, 'testdata', '.rtrdd-port.cjs');
fs.writeFileSync(ENTRY,
  "export { rtrLabel, filterRouters, dropdownHtml, nextHighlight, overlayOnSwitch, overlayOnStatus } from '../web/src/router-dropdown';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const port = require(OUT);

// `esc` is LIFTED, not stubbed: the port uses its own and a different escaper
// would report a difference that is the harness's. Same lesson as the Bandwidth
// chart's `fmtMbps`.
// FROZEN — the DEFINITION LINE, since `esc` is built from it by `new Function`.
const escLine = G.value('the live esc() definition', () => {
  const t = src.slice(src.indexOf('function esc('));
  return t.slice(0, t.indexOf('\n'));
});
if (!/^function esc\(/.test(escLine)) throw new Error('the recorded esc() is not one');
const esc = new Function(escLine + '\n return esc;')();

const R = (o) => Object.assign({ id: 'r1', label: 'Router One', host: '198.51.100.1', disabled: false }, o);

function liveLabel(r) {
  const ctx = { r }; vm.createContext(ctx);
  return vm.runInContext(labelSrc + '\n_rtrLabel(r);', ctx);
}
function liveFilter(routers, filter) {
  const ctx = { _routers: routers, _ddFilter: filter }; vm.createContext(ctx);
  return vm.runInContext(filterSrc + '\n_ddRouters();', ctx);
}
function liveRender(routers, filter, activeId, status, hl) {
  const list = { innerHTML: '' };
  const ctx = {
    esc, ddList: list, _routers: routers, _ddFilter: filter,
    _routerStatus: status, _activeRouterId: activeId, _ddHl: hl,
  };
  vm.createContext(ctx);
  vm.runInContext(filterSrc + '\n' + labelSrc + '\n' + renderSrc + '\nrenderDropdown();', ctx);
  return list.innerHTML;
}

let bad = 0, checked = 0;
const fail = (what, a, b) => { bad++; console.error('%s\n  live: %j\n  port: %j', what, a, b); };

// ── labels ─────────────────────────────────────────────────────────────────
for (const [name, r] of [
  ['a plain label', R({})],
  ['a label with a middot suffix', R({ label: 'Core · Berlin' })],
  ['a label with a bullet suffix', R({ label: 'Core • Berlin' })],
  ['a suffix with no space before it', R({ label: 'Core·Berlin' })],
  ['no label, falls back to host', R({ label: '' })],
  ['neither label nor host', R({ label: '', host: '' })],
  ['a label that is only whitespace', R({ label: '   ' })],
  ['a label that is only a suffix', R({ label: '· Berlin' })],
]) {
  checked++;
  const a = liveLabel(r), b = port.rtrLabel(r);
  if (a !== b) fail('label: ' + name, a, b);
}

// ── filtering ──────────────────────────────────────────────────────────────
const FLEET = [
  R({ id: 'a', label: 'Alpha', host: '10.0.0.1' }),
  R({ id: 'b', label: 'Beta', host: '10.0.0.2' }),
  R({ id: 'c', label: 'Gamma', host: '192.0.2.9', disabled: true }),
  R({ id: 'd', label: '', host: '203.0.113.4' }),
];
for (const [name, q] of [
  ['no query', ''], ['a label match', 'alph'], ['UPPERCASE query', 'BETA'],
  ['a host match', '10.0.0.2'], ['a partial host', '0.0.'],
  ['matching nothing', 'zzz'], ['whitespace only', '   '],
  ['a query that would match a DISABLED router', 'gamma'],
  ['a query matching the label-less router by host', '203.0'],
]) {
  checked++;
  const a = liveFilter(FLEET, q).map((r) => r.id);
  const b = port.filterRouters(FLEET, q).map((r) => r.id);
  if (JSON.stringify(a) !== JSON.stringify(b)) fail('filter: ' + name, a, b);
}

// ── the markup ─────────────────────────────────────────────────────────────
const ST = { a: true, b: false };   // c/d absent: status not known yet
for (const [name, routers, q, active, status, hl] of [
  ['the whole fleet', FLEET, '', 'a', ST, -1],
  ['nothing matches', FLEET, 'zzz', 'a', ST, -1],
  ['an empty fleet', [], '', '', {}, -1],
  ['no active router', FLEET, '', '', ST, -1],
  ['the active router is DISABLED and so absent', FLEET, '', 'c', ST, -1],
  ['a highlighted row', FLEET, '', 'a', ST, 1],
  ['the highlight is also the active row', FLEET, '', 'a', ST, 0],
  ['a highlight past the end', FLEET, '', 'a', ST, 99],
  ['every status unknown', FLEET, '', 'a', {}, -1],
  ['every status down', FLEET, '', 'a', { a: false, b: false, d: false }, -1],
  ['markup in a label', [R({ id: 'x', label: '<img src=x>' })], '', 'x', {}, -1],
  ['a quote in a label', [R({ id: 'x', label: 'a"b' })], '', 'x', {}, -1],
  ['markup in a host', [R({ id: 'x', label: 'L', host: '<b>h</b>' })], '', '', {}, -1],
  ['a quote in the ID, which lands in data-rtr', [R({ id: 'a"b', label: 'L' })], '', '', {}, -1],
  ['a router with no host has no subtitle', [R({ id: 'x', label: 'L', host: '' })], '', '', {}, -1],
]) {
  checked++;
  const a = liveRender(routers, q, active, status, hl);
  const b = port.dropdownHtml(port.filterRouters(routers, q), active, status, hl);
  if (a !== b) fail('render: ' + name, a, b);
}

// ── the switching overlay ──────────────────────────────────────────────────
//
// The live rule, read off the two lines asserted above: open on a switch, close
// on the first `connected: true`, and on `connected: false` close only from the
// SECOND one — the first is the old session tearing down.
function liveOverlay(steps) {
  let open = false, falses = 0;
  for (const s of steps) {
    if (s === 'switch') { open = true; falses = 0; continue; }
    if (s === true) { open = false; continue; }
    if (!open) continue;              // the guard is `switchOvl.contains('open')`
    falses++;
    if (falses > 1) open = false;
  }
  return open;
}
for (const [name, steps] of [
  ['a switch alone leaves it open', ['switch']],
  ['a successful switch closes it', ['switch', true]],
  ['ONE false does not close it — that is the old session', ['switch', false]],
  ['a second false closes it — the new router failed', ['switch', false, false]],
  ['false then connected still closes', ['switch', false, true]],
  ['three falses stay closed', ['switch', false, false, false]],
  // A status before any switch. NOTE: this does not discriminate the live
  // app's `contains('open')` guard — removing that guard survives, because a
  // closed overlay can only gain a count and the next switch resets it. Kept as
  // a case because the OUTPUT still has to match; the guard's unobservability
  // is recorded at the function.
  ['a status with no switch does nothing', [false, false]],
  ['switching again resets the count', ['switch', false, 'switch', false]],
  ['connected before any switch', [true]],
]) {
  checked++;
  const a = liveOverlay(steps);
  let st = { open: false, falses: 0 };
  for (const s of steps) st = (s === 'switch') ? port.overlayOnSwitch() : port.overlayOnStatus(st, s);
  if (a !== st.open) fail('overlay: ' + name, a, st.open);
}
// BELIEVABILITY: the sequences must produce both outcomes, or the rule could be
// a constant.
assert.ok(liveOverlay(['switch']) && !liveOverlay(['switch', true]),
  'the overlay cases do not separate open from closed');

// BELIEVABILITY: the markup cases must produce a dot in all three states and at
// least one tick, or the corpus cannot see a renderer that hardcodes either.
const all = liveRender(FLEET, '', 'a', ST, 1);
for (const need of ['rtr-dd-dot on', 'rtr-dd-dot off', 'rtr-dd-dot "', 'rtr-dd-check', ' hl"']) {
  assert.ok(all.includes(need), 'no case produces ' + need + ' — this gate cannot see it');
}

if (bad) {
  console.error('\nrouter-dropdown-check: %d of %d differ', bad, checked);
  process.exit(1);
}
console.log('router-dropdown-check: %d comparisons identical', checked);
