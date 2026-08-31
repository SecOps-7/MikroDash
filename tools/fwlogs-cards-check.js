'use strict';
/**
 * FW Actions and Logs, live against ported.
 *
 * ── TWO CARDS, ONE GATE, BECAUSE THEY SHARE NOTHING BUT A SIZE ──────────────
 *
 * Both are small and both hang off the extra-cards IIFE, so they are compared
 * here together rather than in two files that would be 90% the same harness.
 *
 * ── THE SORT IS BY COUNT ONLY, WHICH MAKES TIES ORDER-DEPENDENT ─────────────
 *
 * `sort((a,b)=>b[1]-a[1])` over `Object.entries`, so two actions with the same
 * count keep insertion order — the order the RULES were read in. The corpus
 * carries ties on purpose, because a port that added a tie-breaker would be
 * stable where the original is merely consistent.
 *
 * ── AND THE LOG TAIL IS A SEQUENCE ──────────────────────────────────────────
 *
 * A history replay followed by streamed lines, past the 50-line cap, is the only
 * way the shift-and-render behaviour is visible.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/fwlogs-cards-check.js
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
const G = LIFT.golden('fwlogs-cards-check');
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
const fwAt = src.indexOf("socket.on('firewall:update'", iifeAt);
if (LIFT.hasReference(ROOT)) assert.ok(fwAt > 0, 'no firewall:update handler in the extra-cards IIFE');
const fwBody = G.value('fwBody', () => braceBody(fwAt));
if (LIFT.hasReference(ROOT)) assert.ok(fwBody.includes('dc-fwActionList'), 'the slice is not the FW Actions handler');

// FROZEN. A two-step slice rather than a single lifter call, so `freeze-src.py`
// did not match it — the third syntactic form of the same thing.
const renderSrc = G.value('renderSrc', () => {
  const renderAt = src.indexOf('function _renderDcLogs()');
  return src.slice(renderAt, src.indexOf('\n  }', src.indexOf('scrollHeight', renderAt)) + 4);
});
if (LIFT.hasReference(ROOT)) assert.ok(renderSrc.includes('log-dhcp'), 'the _renderDcLogs slice lost its topic classes');
const histBody = G.value('histBody', () => braceBody(src.indexOf("socket.on('logs:history'", iifeAt)));
const newBody = G.value('newBody', () => braceBody(src.indexOf("socket.on('logs:new'", iifeAt)));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['fwBody', fwBody], ['histBody', histBody], ['newBody', newBody]]) {
  assert.ok(typeof __v === 'string' && __v.length > 4,
    'the recorded ' + __n + ' is empty — the golden is broken');
}
const escSrc = G.value('escSrc', () => { const i = src.indexOf('function dcEsc('); return src.slice(i, src.indexOf('\n', i)); });
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['escSrc', escSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}

const ENTRY = path.join(ROOT, 'testdata', '.fwlogs-entry.ts');
fs.writeFileSync(ENTRY,
  "export { renderFwActionsCard } from '../web/src/pages/dashboard-card-fwactions.js';\n" +
  "export { onLogsHistory, onLogsNew, resetLogsCard } from '../web/src/pages/dashboard-card-logs.js';\n");
const OUT = path.join(ROOT, 'testdata', '.fwlogs-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

function makeDom() {
  const byId = new Map();
  const mk = (id) => {
    const n = {
      id, scrollTop: 0, scrollHeight: 4242,
      set textContent(v) { n._t = String(v); },
      get textContent() { return n._t === undefined ? '' : n._t; },
      set innerHTML(v) { n._h = v; },
      get innerHTML() { return n._h === undefined ? '' : n._h; },
    };
    if (id) byId.set(id, n);
    return n;
  };
  mk('dc-fwActionList'); mk('dc-logs');
  return { byId, mk };
}
function snap(d) {
  const fw = d.byId.get('dc-fwActionList'), lg = d.byId.get('dc-logs');
  return JSON.stringify({
    fw: fw.innerHTML,
    logs: lg.innerHTML,
    // The tail must be pinned to the bottom on every render.
    scrollTop: lg.scrollTop,
  });
}

function liveSide() {
  const d = makeDom();
  const ctx = {
    Math, String, Object, Array, JSON,
    dcEl: (id) => d.byId.get(id) || null,
    document: { createElement: () => d.mk('') },
    DC_LOG_MAX: 50, _dcLogs: [],
  };
  vm.createContext(ctx);
  vm.runInContext([escSrc, renderSrc,
    'function __fw(data){' + fwBody + '}',
    'function __hist(data){' + histBody + '}',
    'function __new(entry){' + newBody + '}',
  ].join('\n'), ctx);
  return { d, fw: (x) => ctx.__fw(x), hist: (x) => ctx.__hist(x), add: (x) => ctx.__new(x) };
}
function portSide() {
  const d = makeDom();
  globalThis.document = { getElementById: (id) => d.byId.get(id) || null, createElement: () => d.mk('') };
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  m.resetLogsCard();
  return { d, fw: m.renderFwActionsCard, hist: m.onLogsHistory, add: m.onLogsNew };
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 4) {
    const A = JSON.parse(a), B = JSON.parse(b);
    shout('DIFF %s', what);
    for (const k of Object.keys(A)) {
      if (JSON.stringify(A[k]) !== JSON.stringify(B[k])) {
        shout('  %s\n    live: %s\n    port: %s', k,
          JSON.stringify(A[k]).slice(0, 260), JSON.stringify(B[k]).slice(0, 260));
      }
    }
  }
}

// ── FW Actions ─────────────────────────────────────────────────────────────
const R = (action, disabled) => ({ action, disabled });
const FW = {
  'one accept': { filter: [R('accept')] },
  'a mixed ruleset': { filter: [R('accept'), R('accept'), R('drop')], nat: [R('masquerade')] },
  'DISABLED rules are skipped': { filter: [R('drop', true), R('drop', true), R('accept')] },
  'every rule disabled': { filter: [R('drop', true)] },
  'no rules at all': { filter: [], nat: [], mangle: [], raw: [] },
  'an empty payload': {},
  'a rule with no action': { filter: [R(undefined), R('accept')] },
  'an unknown action gets the fallback colour': { filter: [R('tarpit')] },
  'more than seven actions': {
    filter: ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i'].flatMap((a, i) =>
      Array.from({ length: 9 - i }, () => R(a))),
  },
  // Ties keep insertion order: the sort compares counts only.
  'a tie keeps rule order': { filter: [R('zulu'), R('alpha')] },
  'a tie the other way round': { filter: [R('alpha'), R('zulu')] },
  'all four chains contribute': {
    filter: [R('accept')], nat: [R('src-nat')], mangle: [R('mark-packet')], raw: [R('notrack')],
  },
  'markup in an action name': { filter: [R('<b>&"x"')] },
};
for (const [name, payload] of Object.entries(FW)) {
  const L = liveSide(), P = portSide();
  L.fw(payload); P.fw(payload);
  cmp('fw: ' + name, snap(L.d), snap(P.d));
}

// ── Logs ───────────────────────────────────────────────────────────────────
const E = (msg, topics, sev, time) => ({ message: msg, topics, severity: sev, time });
const LOG_SCRIPTS = {
  'a history replay': [['hist', { entries: [E('one', 'system'), E('two', 'dhcp')] }]],
  // A bare array RENDERS. It did not until ToDo #17 was fixed — `data.entries`
  // on an Array is `Array.prototype.entries`, so the shape that looked accepted
  // was silently dropped. This case was pinned against the old behaviour and
  // turned red the moment the fix landed, which is how the port learned to
  // follow it.
  'a BARE ARRAY history renders (ToDo #17, fixed)': [['hist', [E('one', 'system')]]],
  'a bare array with several entries': [['hist', [E('one', 'dhcp'), E('two', 'firewall')]]],
  'an EMPTY bare array': [['hist', []]],
  'a non-array history is ignored': [['hist', { entries: 'nope' }], ['hist', { error: 'x' }]],
  'an empty history': [['hist', { entries: [] }]],
  'topic classes, first match wins': [['hist', {
    entries: [E('a', 'dhcp,wireless'), E('b', 'wireless,firewall'), E('c', 'firewall,system'),
      E('d', 'system'), E('e', 'account'), E('f', 'DHCP')],
  }]],
  'severities': [['hist', {
    entries: [E('a', '', 'error'), E('b', '', 'warning'), E('c', '', undefined)],
  }]],
  'a line with no topics': [['hist', { entries: [E('a', undefined)] }]],
  'a streamed line appends': [['hist', { entries: [E('one', 'system')] }], ['add', E('two', 'dhcp')]],
  'a line with NO message is dropped': [['hist', { entries: [E('one', 'system')] }], ['add', E('', 'dhcp')]],
  'an undefined entry is dropped': [['add', undefined]],
  'the cap holds at fifty': [
    ['hist', { entries: Array.from({ length: 60 }, (_, i) => E('line ' + i, 'system')) }],
    ...Array.from({ length: 5 }, (_, i) => [['add', E('extra ' + i, 'dhcp')]]).flat(),
  ],
  'markup in a message': [['add', E('<img src=x> & "q"', 'system')]],
  'markup in a topic': [['add', E('m', '<b>')]],
};
for (const [name, script] of Object.entries(LOG_SCRIPTS)) {
  const L = liveSide(), P = portSide();
  script.forEach(([op, arg], i) => {
    L[op](arg); P[op](arg);
    cmp('logs: ' + name + ' step ' + (i + 1), snap(L.d), snap(P.d));
  });
}

// ── believability ──────────────────────────────────────────────────────────
{
  const L = liveSide();
  L.fw({ filter: [R('accept'), R('accept'), R('drop')] });
  const s = JSON.parse(snap(L.d));
  assert.match(s.fw, /fw-action-row/, 'the live FW card rendered nothing');
  assert.match(s.fw, /width:100%/, 'the top action should be a full bar');
  assert.match(s.fw, /width:50%/, 'the second action should be half');
  const E2 = liveSide();
  E2.fw({ filter: [] });
  assert.match(JSON.parse(snap(E2.d)).fw, /No rules/, 'an empty ruleset should say so');
}
{
  const L = liveSide();
  L.hist({ entries: [E('hello', 'dhcp')] });
  const s = JSON.parse(snap(L.d));
  assert.match(s.logs, /log-dhcp/, 'the live log line lost its topic class');
  assert.equal(s.scrollTop, 4242, 'the live tail did not pin to the bottom');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
say('fwlogs-cards-check: %d comparisons identical', checked);
