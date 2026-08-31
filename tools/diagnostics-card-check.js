'use strict';
/**
 * The API Diagnostics card, live against ported.
 *
 * ── IT DISAGREES WITH ITS NEIGHBOUR ABOUT `null` ────────────────────────────
 *
 * The Routes card prints a null count as the string "null"; this one shows an em
 * dash, because its setter is `!= null` rather than `!== undefined`. Two cards in
 * one IIFE, two readings of "no value" — so the corpus carries null and undefined
 * separately here as well, and pins THIS card's answer rather than the other's.
 *
 * ── AND THE GEO ROW HAS THREE STATES, NOT TWO ───────────────────────────────
 *
 * Present-and-unavailable shows the row; present-and-available shows nothing;
 * ABSENT also shows nothing. The third is the one worth pinning: a payload with
 * no geo key must not claim a failure nobody reported.
 *
 * The `title` attribute in that row is ToDo #16 — `dcEsc` does not escape quotes.
 * Reproduced deliberately, so the corpus includes a reason containing a quote and
 * this gate FLIPS when the live app is fixed.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/diagnostics-card-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('diagnostics-card-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

function braceBody(from) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const open = src.indexOf('{', from);
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (!depth) return src.slice(open + 1, i); }
  }
  throw new Error('unbalanced body');
}
const iifeAt = src.indexOf('All 14 new cards live here');
const at = src.indexOf("socket.on('diagnostics:update'", iifeAt);
// GUARDED: a question about the live SOURCE.
if (LIFT.hasReference(ROOT)) assert.ok(at > 0, 'no diagnostics:update handler in the extra-cards IIFE');
const body = G.value('body', () => braceBody(at));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['body', body]]) {
  assert.ok(typeof __v === 'string' && __v.length > 4,
    'the recorded ' + __n + ' is empty — the golden is broken');
}
assert.ok(body.includes('dc-diagList'), 'the slice is not the diagnostics handler');
assert.ok(body.includes('geo lookups'), 'the slice lost its geo row');
const escSrc = G.value('escSrc', () => { const i = src.indexOf('function dcEsc('); return src.slice(i, src.indexOf('\n', i)); });
// `esc` too: the geo row's title attribute moved to it when ToDo #16 was fixed.
const escPageSrc = G.value('escPageSrc', () => { const i = src.indexOf('function esc('); return src.slice(i, src.indexOf('\n', i)); });
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['escSrc', escSrc], ['escPageSrc', escPageSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}

const ENTRY = path.join(ROOT, 'testdata', '.diag-entry.ts');
fs.writeFileSync(ENTRY, "export { renderDiagnosticsCard } from '../web/src/pages/dashboard-card-diagnostics.js';\n");
const OUT = path.join(ROOT, 'testdata', '.diag-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

function makeDom() {
  const byId = new Map();
  const mk = (id) => {
    const n = {
      id,
      set textContent(v) { n._t = String(v); },
      get textContent() { return n._t === undefined ? '' : n._t; },
      set innerHTML(v) { n._h = v; },
      get innerHTML() {
        // A real div: textContent in, escaped HTML out — & < > only, quotes left
        // alone. That asymmetry is what ToDo #16 is about.
        if (n._h !== undefined) return n._h;
        return String(n._t === undefined ? '' : n._t)
          .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
      },
    };
    if (id) byId.set(id, n);
    return n;
  };
  mk('dc-diagTotal'); mk('dc-diagList');
  return { byId, mk };
}
function snap(d) {
  return JSON.stringify({
    total: d.byId.get('dc-diagTotal').textContent,
    list: d.byId.get('dc-diagList').innerHTML,
  });
}
function liveRun(payload) {
  const d = makeDom();
  const ctx = {
    String, Array,
    dcEl: (id) => d.byId.get(id) || null,
    document: { createElement: () => d.mk('') },
  };
  vm.createContext(ctx);
  vm.runInContext(escSrc + '\n' + escPageSrc + '\nfunction __run(data){' + body + '}', ctx);
  ctx.__run(payload);
  return snap(d);
}
function portRun(payload) {
  const d = makeDom();
  globalThis.document = { getElementById: (id) => d.byId.get(id) || null, createElement: () => d.mk('') };
  delete require.cache[require.resolve(OUT)];
  require(OUT).renderDiagnosticsCard(payload);
  return snap(d);
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 4) shout('DIFF %s\n  live: %s\n  port: %s', what, a, b);
}

const C = (name, streams) => ({ name, streams });
const CASES = {
  'a normal payload': { total: 5, collectors: [C('dns', 2), C('logs', 0)] },
  'no collectors': { total: 0, collectors: [] },
  'no collectors key': { total: 1 },
  'an empty payload': {},
  // This card's reading of "no value" — an em dash for BOTH spellings.
  'a null total': { total: null, collectors: [] },
  'an undefined total': { total: undefined, collectors: [] },
  'a zero total is NOT an absence': { total: 0, collectors: [] },
  // Geo, all three states.
  'geo unavailable with a reason': { total: 1, collectors: [], geo: { available: false, reason: 'ENOENT' } },
  'geo unavailable with NO reason': { total: 1, collectors: [], geo: { available: false } },
  'geo AVAILABLE shows no row': { total: 1, collectors: [], geo: { available: true } },
  'geo ABSENT shows no row': { total: 1, collectors: [] },
  'geo present but empty': { total: 1, collectors: [], geo: {} },
  // ToDo #16: the reason lands in an attribute and dcEsc leaves quotes alone.
  'a reason containing a QUOTE (ToDo #16)': {
    total: 1, collectors: [], geo: { available: false, reason: 'cannot find "geoip-lite"' },
  },
  'a reason containing markup': {
    total: 1, collectors: [], geo: { available: false, reason: '<b>&x</b>' },
  },
  // Collector rows.
  'a collector with no streams key': { total: 1, collectors: [{ name: 'dns' }] },
  'a collector with no name': { total: 1, collectors: [C(undefined, 3)] },
  'a negative stream count': { total: 1, collectors: [C('dns', -1)] },
  'markup in a collector name': { total: 1, collectors: [C('<img src=x>', 1)] },
  'many collectors': { total: 9, collectors: Array.from({ length: 12 }, (_, i) => C('c' + i, i % 3)) },
};

for (const [name, payload] of Object.entries(CASES)) {
  cmp(name, liveRun(payload), portRun(payload));
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(liveRun({ total: 5, collectors: [C('dns', 2), C('logs', 0)] }));
  assert.equal(s.total, '5', 'the live total is ' + s.total);
  assert.match(s.list, /diag-count-active/, 'an active collector lost its class');
  assert.match(s.list, /diag-count-zero/, 'a zero collector lost its class');
}
{
  const s = JSON.parse(liveRun({ total: 1, collectors: [], geo: { available: false } }));
  assert.match(s.list, /geo lookups/, 'the live geo row did not render');
  const t = JSON.parse(liveRun({ total: 1, collectors: [] }));
  assert.ok(!/geo lookups/.test(t.list), 'an ABSENT geo key claimed a failure');
}
{
  // ToDo #16, FIXED: the quote is escaped now, so the attribute holds. This
  // assertion was written the other way round and turned red when the fix
  // landed — which is how the port knew to follow it within the hour.
  const s = JSON.parse(liveRun({ total: 1, collectors: [], geo: { available: false, reason: 'a"b' } }));
  assert.match(s.list, /title="a&quot;b"/,
    'the geo reason is no longer escaped for attribute context: ' + s.list);
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('diagnostics-card-check: %d cases identical', checked);
