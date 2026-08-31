'use strict';
/**
 * WHAT MUST BE FORGOTTEN, AND WHERE — AS A LEDGER RATHER THAN A MEMORY.
 *
 * A dashboard card that keeps state across a reconnect or a router switch shows
 * the previous router's data under the new router's name, or bridges an outage
 * it should have drawn a gap through. The live app handles that in two handlers,
 * and each one is a LIST of variables it blanks:
 *
 *   ../MikroDash/public/app.js   socket 'connect'
 *   ../MikroDash/public/app.js   'router:switching'
 *
 * Two bugs on 2026-08-25 were both a name missing from the port's version of one
 * of those lists — the traffic chart not clearing on reconnect, and clearing
 * three things the live app never clears. Each was found by reading the two
 * sides side by side, which is exactly the kind of comparison that stops being
 * done. This does it every run.
 *
 * ── HOW IT WORKS, AND WHY THE MAPPING IS HAND-WRITTEN ───────────────────────
 *
 * The live handler bodies are lifted and every assignment in them collected.
 * Each live name is mapped to the port function that must clear its counterpart.
 * The mapping cannot be derived: the two codebases do not share names
 * (`_sysMetaWritten` / `metaWritten`), and the port keeps caches the live app has
 * no equivalent for. So it is written out, and the tool FAILS ON AN UNMAPPED
 * NAME rather than skipping it — a variable added to a live handler shows up
 * here as a refusal, which is the point.
 *
 * ── IT FAILS IN BOTH DIRECTIONS ─────────────────────────────────────────────
 *
 * A `PORT_ONLY` entry declares a reset the live app does not make, with the
 * reason. If that clear disappears from the port, the entry is stale and the run
 * FAILS — an allowance that outlives its problem is worse than no allowance,
 * because it reads as "checked".
 *
 *   node tools/reset-contract-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');
const L = require('./lib/lift.js');
const G = L.golden('reset-contract-audit');

const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);
const P = (rel) => fs.readFileSync(path.join(ROOT, 'web', 'src', rel), 'utf8');

/** The port's reset functions, and what each one actually clears. */
const RESETS = {
  resetSysMeta: { file: 'pages/dashboard-system.ts' },
  resetTraffic: { file: 'pages/dashboard-traffic.ts' },
  // The reconnect half. `resetTraffic` DELEGATES the shared clears to it, so
  // both must be listed and `clears` must follow the call — see below.
  resetTrafficOnReconnect: { file: 'pages/dashboard-traffic.ts' },
  resetPing: { file: 'pages/dashboard-ping.ts' },
  resetConnCaches: { file: 'pages/dashboard-conn.ts' },
  resetLogsCard: { file: 'pages/dashboard-card-logs.ts' },
  resetRoutingCards: { file: 'pages/dashboard-card-routing.ts' },
  resetBandwidthCard: { file: 'pages/dashboard-card-bandwidth.ts' },
};
for (const [name, r] of Object.entries(RESETS)) {
  const body = P(r.file).split('export function ' + name + '(): void {')[1];
  assert.ok(body, name + ' is gone from ' + r.file);
  r.body = body.split('\n}')[0];
  r.clears = [...r.body.matchAll(/(^|[^.\w])([_a-zA-Z][\w]*)\s*=(?!=)/g)].map((m) => m[2])
    .concat([...r.body.matchAll(/([_a-zA-Z][\w]*)\.length\s*=\s*0/g)].map((m) => m[1]));
}
// ── DELEGATION COUNTS AS CLEARING ───────────────────────────────────────────
//
// `resetTraffic` calls `resetTrafficOnReconnect` for the half they share rather
// than repeating it. Reading only the literal body reported the delegating
// function as clearing almost nothing, and this audit raised four problems
// against a correct refactor. A reset's effective set is its own assignments
// plus those of every reset it calls.
//
// Bounded passes rather than recursion with a seen-set: the graph is two deep
// and a cycle would not compile.
for (let pass = 0; pass < 3; pass++) {
  for (const r of Object.values(RESETS)) {
    for (const [name2, r2] of Object.entries(RESETS)) {
      if (r === r2) continue;
      if (new RegExp('\\b' + name2 + '\\s*\\(').test(r.body)) {
        for (const c of r2.clears) if (!r.clears.includes(c)) r.clears.push(c);
      }
    }
  }
}

/**
 * Live name → the port reset that must own it.
 *
 * `null` means "deliberately not ported", with the reason beside it. Every one
 * of those is a claim about the port's design, not a shrug.
 */
const OWNER = {
  _sysMetaWritten: 'resetSysMeta',
  _lastUpdateRowHtml: 'resetSysMeta',
  currentIf: 'resetTraffic',
  allPoints: 'resetTraffic',
  // Added upstream on 2026-08-28 (`d7548b0`, "The Traffic selection survives a
  // reconnect") and ported the same day — THIS AUDIT is what noticed, by failing
  // on a live variable it could not map onto anything here.
  //
  // The asymmetry is the whole fix and belongs in this table rather than in
  // prose: it is cleared on a ROUTER SWITCH and NOT on reconnect, because a
  // reconnect is the same operator on the same router while a switch is a
  // different fleet of interfaces.
  _userPickedIf: 'resetTraffic',
  pingHistory: 'resetPing',
  _connSrcFp: 'resetConnCaches',
  _connDstFp: 'resetConnCaches',
  _connProtoFp: 'resetConnCaches',
  logBuffer: 'resetLogsCard',   // named `lines` here — see ALIAS
  // The live page counts spurious `router:switching` events to decide when to
  // take its switching OVERLAY down. This port's server emits no switching
  // event at all — `main.ts:switchRouter` clears at the moment it ASKS, so there
  // is no overlay and nothing to count. Recorded rather than mapped.
  _switchFalseCount: null,
};

/**
 * Live name → port name, where the two do not share a stem.
 *
 * The matcher below compares SHAPES (`_sysMetaWritten` against `metaWritten`),
 * which covers most of the port's renames for free. It cannot bridge a rename
 * that keeps no letters in common, and it must not be loosened until it can —
 * a matcher lax enough to pair `logBuffer` with `lines` would pair almost
 * anything with almost anything, and every check here would start passing.
 */
const ALIAS = { logBuffer: 'lines' };

/** Locals declared inside the handler — not state, and not this gate's business. */
const LOCALS = new Set(['svg', 'tc']);

/** The two lifecycle sites, and the port path that answers each. */
const SITES = [
  { event: 'connect', contains: '_sysMetaWritten', wiredIn: 'pages/dashboard.ts',
    how: "socket.on('connect', …)" },
  { event: 'router:switching', contains: 'switchOvl', wiredIn: 'main.ts',
    how: 'switchRouter()' },
];

/**
 * Resets the port makes that the live app does not, each with why.
 *
 * These exist because the PORT has caches the live app has no equivalent for —
 * it suppresses redraws by fingerprint where the live page simply redraws. A
 * fingerprint that survives a router switch is worse than no fingerprint: two
 * routers agreeing on a number would skip the redraw and leave the previous
 * router's card on screen.
 */
const PORT_ONLY = {
  'resetConnCaches:connHistory': 'the port keeps the sparkline history the live card rebuilds',
  'resetConnCaches:pending': 'a queued rAF payload from the old router must not flush into the new one',
  'resetRoutingCards:donut': "the port owns the Chart instance; destroying it is how the canvas is freed",
  'resetRoutingCards:donutTotal': 'a redraw-suppressing fingerprint — two routers can agree on a total',
  'resetBandwidthCard:bwDown': 'the plan ceiling is per-router and scales the bars',
  'resetBandwidthCard:bwUp': 'the plan ceiling is per-router and scales the bars',
  'resetBandwidthCard:routers': 'the per-router list the card switches between',
  'resetBandwidthCard:activeId': 'which router the card is showing',
};

const problems = [];
const seenPortOnly = new Set();

for (const site of SITES) {
  // FROZEN — the NAMES the live handler clears, not the handler text. That list
  // is the whole of what this audit compares the port's resets against, and an
  // empty one would silently accept a port that clears nothing.
  const names = G.value('the live ' + site.event + ' assignments', () => {
    const body = L.handler(src, site.event, { contains: site.contains });
    return [...new Set([...body.matchAll(/(^|[^.\w])([_a-zA-Z][\w]*)\s*=(?!=)/g)]
      .map((m) => m[2]).filter((n) => !LOCALS.has(n)))];
  });

  // BELIEVABILITY: a handler that assigns nothing means the lift missed.
  // The believability check now validates the RECORDING, which is what it was
  // always about: a list of nothing means the audit compares against nothing.
  if (!names.length) {
    console.error("no assignments are recorded for the live '" + site.event + "' handler — "
      + 'the lift broke, or the golden is');
    process.exit(1);
  }

  const wiring = P(site.wiredIn);
  for (const n of names) {
    if (!(n in OWNER)) {
      problems.push("the live '" + site.event + "' handler clears `" + n + "`, which this audit " +
                    'does not map to anything. Add it to OWNER — with a port reset that clears it, ' +
                    'or with `null` and the reason it is not ported.');
      continue;
    }
    const owner = OWNER[n];
    if (owner === null) continue;
    const r = RESETS[owner];
    if (!r) { problems.push('OWNER names ' + owner + ', which is not in RESETS'); continue; }
    if (!r.clears.length) {
      problems.push(owner + ' clears nothing');
    }
    // The port's counterpart is matched by SHAPE, not by name: strip the live
    // underscore prefix and compare case-insensitively, then require that SOME
    // assignment in the reset looks like it.
    const stem = (ALIAS[n] || n.replace(/^_/, '')).toLowerCase();
    const hit = r.clears.some((c) => c.toLowerCase() === stem ||
      c.toLowerCase().includes(stem) || stem.includes(c.toLowerCase()));
    if (!hit) {
      problems.push("the live '" + site.event + "' handler clears `" + n + "` but " + owner +
                    ' clears only {' + r.clears.join(', ') + '}');
    }
    // ...and SOMETHING THAT CLEARS IT must actually run on this path.
    //
    // Not `owner` by name. The two traffic moments clear overlapping sets
    // through two different functions — a reconnect must NOT clear the
    // operator's chosen interface and a router switch must — so requiring one
    // named function at both sites forbids the very asymmetry the live app has.
    // What matters is that the variable is cleared there, by whatever runs.
    const runs = Object.entries(RESETS).some(([nm, rr]) =>
      new RegExp('\\b' + nm + '\\s*\\(\\)').test(wiring) &&
      rr.clears.some((c) => c.toLowerCase() === stem ||
        c.toLowerCase().includes(stem) || stem.includes(c.toLowerCase())));
    if (!runs) {
      problems.push('nothing called from ' + site.wiredIn + ' clears `' + n +
                    "`, so it survives the live app's '" + site.event + "' moment");
    }
  }
}

// ── THE OTHER DIRECTION ─────────────────────────────────────────────────────
// FROZEN TOO — the OTHER direction lifts the same handlers a second time, with
// its own normalisation. Freezing only the forward list left this one empty, and
// every port reset was then accused of clearing something no live handler does:
// 12 problems reported by an audit that was in fact fully satisfied.
const liveAll = new Set(G.value('the live cleared names, normalised', () =>
  [...new Set(SITES.flatMap((s) =>
    [...L.handler(src, s.event, { contains: s.contains })
      .matchAll(/(^|[^.\w])([_a-zA-Z][\w]*)\s*=(?!=)/g)]
      .map((m) => (ALIAS[m[2]] || m[2].replace(/^_/, '')).toLowerCase())))].sort()));
if (liveAll.size < 5) {
  throw new Error('only ' + liveAll.size + ' normalised live names recorded — the golden is '
    + 'broken, and this direction would accuse every port reset');
}

for (const [name, r] of Object.entries(RESETS)) {
  for (const c of r.clears) {
    const stem = c.toLowerCase();
    if ([...liveAll].some((l) => l === stem || l.includes(stem) || stem.includes(l))) continue;
    const key = name + ':' + c;
    if (key in PORT_ONLY) { seenPortOnly.add(key); continue; }
    problems.push(name + ' clears `' + c + '`, which neither live handler clears. Either the ' +
                  'live app keeps it on purpose (as it keeps the EMA-smoothed serverOffset) or ' +
                  'this is a cache the port alone has — say which in PORT_ONLY.');
  }
}
for (const key of Object.keys(PORT_ONLY)) {
  if (!seenPortOnly.has(key)) {
    problems.push('PORT_ONLY still allows `' + key + '`, which is no longer cleared. ' +
                  'An allowance that outlives its problem reads as "checked".');
  }
}

if (problems.length) {
  for (const p of problems) console.error('  ' + p);
  console.error('\nreset-contract-audit: ' + problems.length + ' problem(s)');
  process.exit(1);
}
console.log('reset-contract-audit: both live lifecycle handlers are answered by the port (' +
            Object.keys(OWNER).length + ' names mapped, ' + Object.keys(PORT_ONLY).length +
            ' port-only resets declared)');
