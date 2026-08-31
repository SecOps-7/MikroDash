#!/usr/bin/env node
'use strict';
/**
 * KEYS A GATE'S FIXTURE SETS THAT NOTHING READS.
 *
 * ── THE DEFECT THIS EXISTS FOR ──────────────────────────────────────────────
 *
 * `wan-page-check`'s fixture said `lease: null`. Both renderers read `w.dhcp`.
 * So the key was dead, and with it went the entire DHCP half of that page: all
 * 34 of its cases described a WAN with no DHCP client, `leaseCell` only ever
 * rendered its "no DHCP client" branch, and when an actions column arrived it
 * could not produce a button in any case at all. The gate printed
 * "53 cases identical" the whole time.
 *
 * A dead key is invisible to every other check here. Both sides get the same
 * payload, so both agree; the DOM comparison passes; the mutation that would
 * expose it returns early before reaching anything comparable. It was found by
 * mutating the port and watching six of eight mutations survive, which is not a
 * thing anyone does on a gate that is already green.
 *
 * ── WHAT COUNTS AS READ ─────────────────────────────────────────────────────
 *
 * Either implementation reading it is enough — this port and the live `app.js`
 * are meant to agree, and a key only one of them reads is a DIFFERENT finding
 * (a divergence, which the DOM gates do catch). What this looks for is a key
 * NEITHER has heard of, which can only be a fixture that drifted from the
 * payload it is imitating.
 *
 * The collectors count too: a key the Go or Node collector emits is real even
 * if no renderer happens to draw it today.
 *
 * ── WHAT IT MISSES, MEASURED ────────────────────────────────────────────────
 *
 * The test is "this string appears NOWHERE in either tree". That is deliberately
 * loose — a tighter one would accuse working code — but it means a dead key
 * whose name collides with any unrelated identifier passes. Both halves of that
 * were seen on the first run:
 *
 *   found:  `localAs`, `prefixCount`, `maxRx`, `lastLoggedIn`, `deltaBytes`
 *   missed: `remoteAddress` (the payload says `remoteAddr`), `uptime` (it says
 *           `uptimeSec`), and `dynamic`/`disabled`/`scope` on a route, which the
 *           collector strips out of the payload before emitting it
 *
 * All five of the missed ones were in the SAME two fixtures as the found ones,
 * and were caught by reading the emit site once this pointed there. So treat a
 * hit as a reason to go and read the emit site for that payload, not as the
 * complete list of what is wrong with it.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/fixture-key-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

function readAll(dir, exts, out = []) {
  if (!fs.existsSync(dir)) return out;
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) readAll(p, exts, out);
    else if (exts.some((x) => e.name.endsWith(x))) out.push(fs.readFileSync(p, 'utf8'));
  }
  return out;
}

// Everything that could legitimately read a payload key.
//
// THE WHOLE OF `internal/` AND THE WHOLE OF THE LIVE `src/`, not just the
// collectors. The first version searched `internal/collect` and
// `src/collectors` only, and reported thirteen report-summary keys as dead —
// they are built in `internal/reports` and `src/reports/build.js`. An audit that
// accuses working code is one people learn to ignore, so the haystack is wide
// and the burden is on the finding to survive it.
const haystack = [
  ...readAll(path.join(ROOT, 'web', 'src'), ['.ts']),
  ...readAll(path.join(ROOT, 'internal'), ['.go']),
  LIFT.liveSource(ROOT, path.join('public', 'app.js')),
  ...readAll(path.join(LIVE, 'src'), ['.js']),
  ...readAll(path.join(LIVE, 'public', 'js'), ['.js']),
].join('\n');

// A fixture builder, as every gate in this repo writes one:
//   const W = (o) => Object.assign({ ... }, o);
const BUILDER = /const\s+(\w+)\s*=\s*\((\w*)\)\s*=>\s*Object\.assign\(\{/g;

/** The keys of the object literal starting at `open` (the index of its `{`). */
function keysOf(src, open) {
  let depth = 0, i = open, inStr = null;
  const keys = [];
  for (; i < src.length; i++) {
    const c = src[i];
    if (inStr) { if (c === '\\') i++; else if (c === inStr) inStr = null; continue; }
    if (c === '"' || c === "'" || c === '`') { inStr = c; continue; }
    if (c === '{' || c === '[' || c === '(') depth++;
    else if (c === '}' || c === ']' || c === ')') { depth--; if (depth === 0) break; }
    // Only keys at depth 1 — a nested object's keys belong to that object.
    else if (depth === 1 && /[A-Za-z_$]/.test(c)) {
      const m = /^([A-Za-z_$][\w$]*)\s*:/.exec(src.slice(i));
      if (m) { keys.push(m[1]); i += m[1].length; }
    }
  }
  return keys;
}

// Keys that are structural rather than payload — a fixture may legitimately
// carry them without any renderer naming them.
const STRUCTURAL = new Set(['ts']);

// id -> why a key that nothing reads is nonetheless correct. Empty is the goal.
const EXPECTED = {};

const dead = [];
// COUNTED, so a clean run can report what it EXAMINED. Without these the output
// was "0 fixture key(s) that no implementation reads" — a findings count, which
// reads identically whether every key is read or the scan found no gates at all.
// `wiring-audit` had the same weakness and was corrected on 2026-08-30; this one
// was found by the same pass.
let gatesExamined = 0;
let keysExamined = 0;
for (const f of fs.readdirSync(path.join(ROOT, 'tools'))) {
  if (!f.endsWith('-check.js')) continue;
  const src = fs.readFileSync(path.join(ROOT, 'tools', f), 'utf8');
  gatesExamined++;
  BUILDER.lastIndex = 0;
  let m;
  while ((m = BUILDER.exec(src))) {
    const open = src.indexOf('{', m.index + m[0].length - 1);
    for (const k of keysOf(src, open)) {
      if (STRUCTURAL.has(k)) continue;
      keysExamined++;
      // `.k`, `'k'`, `"k"` or `k:` anywhere an implementation could read it.
      const q = k.replace(/\$/g, '\\$');
      const re = new RegExp('\\.' + q + '\\b|[\'"`]' + q + '[\'"`]|\\b' + q + '\\s*:');
      if (re.test(haystack)) continue;
      dead.push({ gate: f, builder: m[1], key: k });
    }
  }
}

const problems = dead.filter((d) => !EXPECTED[d.gate + ':' + d.key]);
for (const k of Object.keys(EXPECTED)) {
  if (!dead.some((d) => d.gate + ':' + d.key === k)) {
    problems.push({ gate: k, key: '(recorded, but no longer dead — delete the entry)' });
  }
}

// A SCAN THAT FOUND NO GATES OR NO KEYS IS A FAILURE, NOT A PASS. The glob is
// `*-check.js` and the builders are found by regex; either could stop matching
// and this would print "every fixture key is read" over an empty scan.
if (gatesExamined === 0 || keysExamined === 0) {
  shout(`fixture-key-audit: examined ${gatesExamined} gate(s) and ${keysExamined} key(s) — ` +
    'it is measuring nothing, which is not the same as clean');
  process.exit(1);
}
say(`fixture-key-audit: ${keysExamined} key(s) across ${gatesExamined} gate(s) examined; ` +
  `${dead.length} that no implementation reads`);
if (problems.length) {
  shout('');
  for (const d of problems) {
    shout(`  ✗ ${d.gate}: ${d.builder || ''}${d.builder ? '()' : ''} sets \`${d.key}\`, ` +
      'and neither this port nor the live app reads it — the fixture has drifted from the payload ' +
      'it imitates, and every branch behind that key is unreachable in this gate');
  }
  process.exit(1);
}
say('every fixture key is read by an implementation');
