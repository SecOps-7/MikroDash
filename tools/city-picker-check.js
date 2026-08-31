#!/usr/bin/env node
'use strict';
/**
 * The town search's list and query rules, live against ported.
 *
 * ── IT IS A PRECONDITION, NOT A FINISHING TOUCH ─────────────────────────────
 *
 * The router dialog's save sends `geo: { place: picker.get() }`, and the store
 * reads a null `place` as "clear the override". A modal wired without this
 * picker would send null on every save and wipe an operator's manual location
 * whenever they edited anything else. That is why this is being gated before
 * the modal is wired, rather than after.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/city-picker-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('city-picker-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

const at = src.indexOf('  function renderList() {', src.indexOf('function _mountCityPicker'));
if (LIFT.hasReference(ROOT)) assert.ok(at > 0, 'renderList has moved inside _mountCityPicker');
const end = src.indexOf('\n  }', at);
if (LIFT.hasReference(ROOT)) assert.ok(end > at && end - at < 1400, 'renderList is not where its anchors say');
const renderSrc = G.value('renderSrc', () => src.slice(at, end + 4));
for (const m of ['cpick-empty', 'cpick-opt', 'is-active', 'cpick-cc', 'unavailable']) {
  if (LIFT.hasReference(ROOT)) assert.ok(renderSrc.includes(m), 'renderList lost: ' + m);
}
if (LIFT.hasReference(ROOT)) assert.ok(!renderSrc.includes('function search'), 'the slice over-read into search()');

// The query floor and the debounce, from the input handler.
const FLOOR = 'if (q.length < 2) { closeList(); return; }';
const DEBOUNCE = 'timer = setTimeout(function () { search(q); }, 250);';
const STALE = 'if (mine !== seq) return;';
for (const [what, needle] of [['the query floor', FLOOR], ['the debounce', DEBOUNCE],
  ['the stale-response guard', STALE]]) {
  if (LIFT.hasReference(ROOT)) assert.ok(src.includes(needle), what + ' has moved in app.js');
}

const escSrc = G.value('escSrc', () => src.slice(src.indexOf('function esc(')));
const liveEsc = new Function(escSrc.slice(0, escSrc.indexOf('\n')) + '\n return esc;')();

const ENTRY = path.join(ROOT, 'testdata', '.cpick-entry.ts');
const OUT = path.join(ROOT, 'testdata', '.cpick-port.cjs');
fs.writeFileSync(ENTRY,
  "export { cityListHtml, shouldSearchCity, cityResponseIsStale, CITY_MIN_QUERY, CITY_DEBOUNCE_MS, formatPlace, CityPickerState }"
  + " from '../web/src/pages/city-picker';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const port = require(OUT);

function liveRender(results, active, unavailable) {
  const listEl = { innerHTML: '', hidden: true };
  const ctx = {
    esc: liveEsc, results, active, unavailable, listEl,
    inputEl: { setAttribute() {} },
  };
  vm.createContext(ctx);
  vm.runInContext(renderSrc + '\nrenderList();', ctx);
  return listEl.innerHTML;
}

const C = (o) => Object.assign({ name: 'Berlin', region: 'BE', cc: 'DE' }, o);
let bad = 0, checked = 0;
const fail = (w, a, b) => { bad++; console.error('%s\n  live: %j\n  port: %j', w, a, b); };

for (const [name, results, active, unavailable] of [
  ['no results', [], -1, false],
  ['no results, search UNAVAILABLE', [], -1, true],
  ['one result, active', [C({})], 0, false],
  ['one result, none active', [C({})], -1, false],
  ['three results, the middle active', [C({}), C({ name: 'Bern' }), C({ name: 'Bergen' })], 1, false],
  ['a town with no region', [C({ region: '' })], 0, false],
  ['a town with no cc', [C({ cc: '' })], 0, false],
  ['a town with neither', [C({ region: '', cc: '' })], 0, false],
  ['markup in a name', [C({ name: '<img src=x>' })], 0, false],
  ['a quote in a name', [C({ name: 'a"b' })], 0, false],
  ['markup in a region', [C({ region: '<b>x</b>' })], 0, false],
  ['an active index past the end', [C({})], 5, false],
  // `unavailable` must be ignored when there ARE results.
  ['results WITH unavailable set', [C({})], 0, true],
]) {
  checked++;
  const a = liveRender(results, active, unavailable);
  const b = port.cityListHtml(results, active, unavailable);
  if (a !== b) fail('list: ' + name, a, b);
}

// The query floor, from the live expression `q.length < 2` on a TRIMMED value.
const liveFloor = (raw) => !(raw.trim().length < 2);
for (const q of ['', ' ', '  ', 'a', 'ab', ' ab ', ' a ', 'abc', '\t\tx\t\t']) {
  checked++;
  const a = liveFloor(q), b = port.shouldSearchCity(q);
  if (a !== b) fail('floor: ' + JSON.stringify(q), a, b);
}
for (const [mine, seq] of [[1, 1], [1, 2], [3, 2], [0, 0]]) {
  checked++;
  const a = mine !== seq, b = port.cityResponseIsStale(mine, seq);
  if (a !== b) fail('stale: ' + mine + '/' + seq, a, b);
}

// The two constants must match the live literals rather than merely existing.
assert.strictEqual(port.CITY_MIN_QUERY, 2, 'the query floor is not 2');
assert.strictEqual(port.CITY_DEBOUNCE_MS, 250, 'the debounce is not 250ms');

// ── the formatter ──────────────────────────────────────────────────────────
const fmtAt = src.indexOf('function _fmtPlace(p) {');
if (LIFT.hasReference(ROOT)) assert.ok(fmtAt > 0, '_fmtPlace has moved in app.js');
const fmtEnd = src.indexOf('\n}', fmtAt);
const fmtSrc = G.value('fmtSrc', () => src.slice(fmtAt, fmtEnd + 2));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['renderSrc', renderSrc], ['escSrc', escSrc], ['fmtSrc', fmtSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}
assert.ok(fmtSrc.includes('/^[A-Za-z]/'), '_fmtPlace lost its region test');
const liveFmt = new Function(fmtSrc + '\n return _fmtPlace;')();

for (const p of [
  null, undefined, {}, { name: 'Berlin' }, { name: 'Berlin', cc: 'DE' },
  { name: 'Berlin', region: 'BE', cc: 'DE' },
  // A NUMERIC region is dropped — geo databases carry them for many countries.
  { name: 'Berlin', region: '16', cc: 'DE' },
  { name: 'Berlin', region: '', cc: 'DE' },
  // A region with no name is not shown either.
  { region: 'BE', cc: 'DE' }, { cc: 'DE' },
  { name: 'X', region: 'A1', cc: 'GB' },
]) {
  checked++;
  const a = liveFmt(p), b = port.formatPlace(p);
  if (a !== b) fail('format ' + JSON.stringify(p), a, b);
}

// ── the state machine, as SEQUENCES ────────────────────────────────────────
//
// Reproduced from the live closure's four entry points, whose source is
// asserted so a change over there breaks this. The rules only exist across
// steps: `get()` after a preview differs from `get()` after a set, and
// restoring differs from committing only once something was previewed first.
for (const [what, needle] of [
  ['get', 'get: function () { return previewOnly ? null : chosen; },'],
  ['set', 'set: function (place) { commit(place || null); },'],
  ['preview', 'previewOnly = !!place;'],
  ['clear', 'clear: function () { commit(null); },'],
  ['commit resets previewOnly', 'previewOnly = false;                 // a commit is always'],
  ['restoreText does NOT commit', "function restoreText() {\n    inputEl.value = chosen ? _fmtPlace(chosen) : '';\n  }"],
]) {
  if (LIFT.hasReference(ROOT)) assert.ok(src.includes(needle), 'the picker\'s ' + what + ' has moved in app.js');
}

function liveState() {
  let chosen = null, previewOnly = false;
  const commit = (p) => { chosen = p || null; previewOnly = false; };
  return {
    set: (p) => commit(p),
    preview: (p) => { commit(p); previewOnly = !!p; },
    clear: () => commit(null),
    restore: () => {},                    // restoreText touches only the input
    get: () => (previewOnly ? null : chosen),
    isPreview: () => previewOnly,
    text: () => liveFmt(chosen),
  };
}

const BERLIN = { name: 'Berlin', region: 'BE', cc: 'DE' };
const AUTO2 = { name: 'Frankfurt', region: 'HE', cc: 'DE' };
for (const [name, steps] of [
  ['nothing has happened', []],
  ['a set is a choice', [['set', BERLIN]]],
  ['A PREVIEW IS NOT — get() must be null', [['preview', AUTO2]]],
  ['restoring after a preview keeps it a preview', [['preview', AUTO2], ['restore']]],
  ['restoring twice still keeps it', [['preview', AUTO2], ['restore'], ['restore']]],
  ['setting over a preview makes it a choice', [['preview', AUTO2], ['set', BERLIN]]],
  ['clearing a preview', [['preview', AUTO2], ['clear']]],
  ['clearing a choice', [['set', BERLIN], ['clear']]],
  ['previewing NOTHING is not a preview', [['preview', null]]],
  ['previewing after a set replaces it', [['set', BERLIN], ['preview', AUTO2]]],
  ['a set of null', [['set', null]]],
  ['restore before anything', [['restore']]],
]) {
  checked++;
  const L = liveState();
  const P = new port.CityPickerState();
  for (const [op, arg] of steps) {
    if (op === 'set') { L.set(arg); P.set(arg); }
    else if (op === 'preview') { L.preview(arg); P.preview(arg); }
    else if (op === 'clear') { L.clear(); P.clear(); }
    else { L.restore(); /* the port's restore is text-only too */ }
  }
  const a = { get: L.get(), isPreview: L.isPreview(), text: L.text() };
  const b = { get: P.get(), isPreview: P.isPreview(), text: P.text() };
  if (JSON.stringify(a) !== JSON.stringify(b)) fail('state: ' + name, a, b);
}
// BELIEVABILITY: a preview and a set must DIFFER in what get() returns, or the
// safety property is untested.
{
  const A = liveState(); A.preview(AUTO2);
  const B = liveState(); B.set(AUTO2);
  assert.ok(A.get() === null && B.get() !== null,
    'preview and set are indistinguishable — the override guard is not being tested');
}

// BELIEVABILITY: the three list states must be distinct, or the empty-message
// split is untested.
const states = new Set([liveRender([], -1, false), liveRender([], -1, true), liveRender([C({})], 0, false)]);
assert.strictEqual(states.size, 3, 'the three list states do not produce three distinct answers');

if (bad) {
  console.error('\ncity-picker-check: %d of %d differ', bad, checked);
  process.exit(1);
}
console.log('city-picker-check: %d comparisons identical', checked);
