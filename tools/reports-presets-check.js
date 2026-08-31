'use strict';
/**
 * The REPORTS date-range presets, live against ported.
 *
 * ── WHY THIS ONE IS WORTH A GATE OF ITS OWN ─────────────────────────────────
 *
 * Twenty-four presets built out of six date helpers, and every classic calendar
 * trap is in there: weeks that start on MONDAY (`day === 0 ? 6 : day - 1`), a
 * month end found by asking for day 0 of the NEXT month, a "so far" variant of
 * four ranges that leaves `to` at the current instant, and an end-of-day pinned
 * at 23:59:00 rather than 23:59:59.
 *
 * None of that is visible in a payload and none of it is caught by rendering:
 * it decides which rows the server is asked for. A range that is off by one day
 * returns real data and looks entirely correct.
 *
 * ── TIME IS FROZEN, AND THE FROZEN INSTANTS ARE THE CORPUS ──────────────────
 *
 * `new Date()` is called inside the live function, so both sides run against a
 * pinned clock. The instants are chosen to break things: a Sunday (where a
 * Monday-start week wraps back six days), a Monday, the 1st and 31st of a month,
 * the 29th of a leap February, a 31st in a month whose predecessor is shorter,
 * New Year's Eve, and a DST boundary.
 *
 * ── ONE EQUIVALENT MUTANT, WITH THE REASON ─────────────────────────────────
 *
 * Changing the end-of-day from 23:59:00 to 23:59:59 survives, and it is
 * equivalent rather than untested: `_dtVal` formats to MINUTE precision
 * (`YYYY-MM-DDTHH:MM`), which is what a datetime-local input accepts, so the
 * seconds never leave the function. The distinction would matter only if
 * something read the Date object directly, and nothing does.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/reports-presets-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/reports-presets-check.js --freeze
const G = L.golden('reports-presets-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const APPLY = G.value('APPLY', () => L.whole(src, 'function _applyRptPreset('));
assert.ok(APPLY.includes('_sowMon'), 'the lifted preset function lost its week helpers');
assert.ok(APPLY.includes('thisYearSoFar'), 'the lifted preset function lost its "so far" cases');

const COMPARED = ['rptFrom', 'rptTo'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COMPARED)); process.exit(0); }
const IDS = [...COMPARED, 'rptPreset'];

const ENTRY = path.join(ROOT, 'testdata', '.rp-entry.ts');
fs.writeFileSync(ENTRY, "export { applyPreset } from '../web/src/pages/reports.js';\n");
const OUT = path.join(ROOT, 'testdata', '.rp-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const snap = (doc) => JSON.stringify({
  from: doc.nodes.rptFrom.value, to: doc.nodes.rptTo.value,
});

// A Date whose `new Date()` (no arguments) is pinned, while every other form
// behaves normally. Both sides get the same one.
function frozenDate(nowMs) {
  const Real = Date;
  function F(...args) {
    if (!(this instanceof F)) return Real(...args);
    return args.length === 0 ? new Real(nowMs) : new Real(...args);
  }
  F.prototype = Real.prototype;
  F.now = () => nowMs;
  F.parse = Real.parse;
  F.UTC = Real.UTC;
  return F;
}

function liveRun(preset, nowMs) {
  const doc = makeDoc(IDS, {});
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, isFinite,
    Date: frozenDate(nowMs),
    document: doc,
    setTimeout: () => 0, clearTimeout: () => {}, window: {},
    __run: null,
  };
  vm.createContext(ctx);
  vm.runInContext([
    'function $(id){return document.getElementById(id);}',
    // `rptFrom` and `rptTo` are declared INSIDE the Reports IIFE, indented, so
    // `fileScopeEls` (column-0 anchored) does not see them. `regionEls` is the
    // matching spelling.
    L.declare(L.regionEls(L.region(src, {
      banner: '// ── Reports page', must: ['rptFrom'], mustNot: [],
    })).filter((e) => e.id === 'rptFrom' || e.id === 'rptTo')),
    // `_dtVal` formats a Date into the datetime-local input's value, and it
    // depends on `_rptP`, a padding helper declared beside it. Both lifted; the
    // padding is what makes '2026-08-03' rather than '2026-8-3', which the input
    // silently rejects.
    L.line(src, '  var _rptP = function').trim(),
    L.whole(src, 'function _dtVal('),
    APPLY,
    '__run = function (v) { _applyRptPreset(v); };',
  ].join('\n'), ctx);
  ctx.__run(preset);
  return snap(doc);
}

function portRun(preset, nowMs) {
  const doc = makeDoc(IDS, {});
  const prevDate = globalThis.Date;
  const prevWin = globalThis.window;
  globalThis.Date = frozenDate(nowMs);
  globalThis.window = {};
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).applyPreset(preset);
      return snap(doc);
    });
  } finally {
    globalThis.Date = prevDate;
    if (prevWin === undefined) delete globalThis.window; else globalThis.window = prevWin;
  }
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 6) shout('DIFF %s\n  live: %s\n  port: %s', what, a, b);
}

const PRESETS = ['last1h', 'last3h', 'last6h', 'last12h', 'last24h', 'last2d', 'last7d',
  'last30d', 'last90d', 'last6mo', 'last1y', 'dayBeforeYesterday', 'thisDayLastWeek',
  'prevWeek', 'prevMonth', 'prevYear', 'today', 'thisWeek', 'thisMonth', 'thisYear',
  'todaySoFar', 'thisWeekSoFar', 'thisMonthSoFar', 'thisYearSoFar',
  // Not a preset the page offers. A value nothing matches must leave the inputs
  // alone rather than clearing them, and only a case proves which.
  'notAPreset'];

// Local-time instants chosen to break the helpers. Built with explicit
// components so the runner's own zone is what the arithmetic sees, which is what
// the page sees too.
const WHEN = {
  'a Sunday (a Monday-start week wraps back six days)': new Date(2026, 7, 23, 14, 30).getTime(),
  'a Monday (the week starts today)': new Date(2026, 7, 24, 9, 5).getTime(),
  'the 1st of a month': new Date(2026, 7, 1, 0, 30).getTime(),
  'the 31st of a month': new Date(2026, 7, 31, 23, 45).getTime(),
  'the 31st when the previous month is shorter': new Date(2026, 6, 31, 12, 0).getTime(),
  'a leap-year 29 February': new Date(2028, 1, 29, 12, 0).getTime(),
  'the 1st of March in a leap year': new Date(2028, 2, 1, 12, 0).getTime(),
  'the 1st of January': new Date(2026, 0, 1, 0, 5).getTime(),
  "New Year's Eve": new Date(2026, 11, 31, 23, 50).getTime(),
  'a spring DST boundary': new Date(2026, 2, 29, 3, 30).getTime(),
  'an autumn DST boundary': new Date(2026, 9, 25, 2, 30).getTime(),
  'midnight exactly': new Date(2026, 7, 24, 0, 0, 0).getTime(),
};

for (const [whenName, nowMs] of Object.entries(WHEN)) {
  for (const preset of PRESETS) {
    const name = preset + ' @ ' + whenName;
    let a, b;
    try { a = G.live(name, () => liveRun(preset, nowMs)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
    try { b = portRun(preset, nowMs); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
    cmp(name, a, b);
  }
}

// ── believability ──────────────────────────────────────────────────────────
{
  // The clock really is frozen, and the presets really do compute.
  const sunday = new Date(2026, 7, 23, 14, 30).getTime();
  const s = JSON.parse(G.live('auto:6', () => liveRun('today', sunday)));
  assert.match(s.from, /2026-08-23/, 'today did not start on the frozen day: ' + s.from);
  assert.match(s.to, /2026-08-23/, 'today did not end on the frozen day: ' + s.to);
  assert.match(s.to, /23:59/, 'the end of day is not 23:59: ' + s.to);
}
{
  // A Monday-start week, from a SUNDAY: the week began six days ago, not today.
  const sunday = new Date(2026, 7, 23, 14, 30).getTime();
  const w = JSON.parse(G.live('auto:5', () => liveRun('thisWeek', sunday)));
  assert.match(w.from, /2026-08-17/, 'the week did not start on Monday the 17th: ' + w.from);
  assert.match(w.to, /2026-08-23/, 'the week did not end on Sunday the 23rd: ' + w.to);
}
{
  // Month end via day 0 of the next month — February in a leap year.
  const feb = new Date(2028, 1, 10, 12, 0).getTime();
  const m = JSON.parse(G.live('auto:4', () => liveRun('thisMonth', feb)));
  assert.match(m.from, /2028-02-01/, 'the month did not start on the 1st: ' + m.from);
  assert.match(m.to, /2028-02-29/, 'a leap February did not end on the 29th: ' + m.to);
}
{
  // "So far" leaves `to` at the current instant rather than the end of the range.
  const now = new Date(2026, 7, 24, 9, 5).getTime();
  const full = JSON.parse(G.live('auto:3', () => liveRun('today', now)));
  const sofar = JSON.parse(G.live('auto:2', () => liveRun('todaySoFar', now)));
  assert.equal(full.from, sofar.from, 'todaySoFar started somewhere else');
  assert.notEqual(full.to, sofar.to, 'todaySoFar ran to the end of the day');
  assert.match(sofar.to, /09:05/, 'todaySoFar did not end at the current instant: ' + sofar.to);
}
{
  // An unknown value leaves the inputs untouched.
  const now = new Date(2026, 7, 24, 9, 5).getTime();
  const before = JSON.parse(G.live('auto:1', () => liveRun('notAPreset', now)));
  assert.equal(before.from, '', 'an unknown preset wrote a from value: ' + before.from);
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('reports-presets-check: %d cases identical (%d presets x %d instants)',
  checked, PRESETS.length, Object.keys(WHEN).length);
