'use strict';
/**
 * Which live collectors have a Go counterpart?
 *
 * ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
 *
 * the port record said talkers was "the only unported collector" and it stopped
 * being true without anyone noticing, because nothing checked it: `ping.js`
 * (248 lines) has no Go counterpart and emits `ping:update`, which the Dashboard
 * needs. A sentence in a document is not a check.
 *
 * ── MATCHED BY EMIT NAME, NOT BY FILENAME ───────────────────────────────────
 *
 * A filename comparison gets this wrong in both directions: `interfaceStatus.js`
 * is ported as `ifstatus.go`, and a naive match reported it missing along with
 * `dhcpLeages`/`dhcpNetworks`, whose Go files differ only in case. What actually
 * identifies a collector is the EVENT it emits, because that is the contract the
 * browser consumes. Filename is a hint; the emit is the fact.
 *
 * Collectors that emit NOTHING are infrastructure — `util`, `nullCollector`,
 * `interfaces` (5 lines), and `arp`, whose own comment in index.js says it
 * "emits nothing at all". They are listed so a new one cannot hide among them.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/collector-coverage-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const liveDir = path.join(LIVE, 'src', 'collectors');
const goDir = path.join(ROOT, 'internal', 'collect');

// The WHOLE Go tree, not just internal/collect. `traffic:history` is sent from
// `internal/server/ws.go` — a replay on page focus rather than a collector tick
// — and scanning only the collectors reported it missing when it is not.
function goFiles(dir, acc = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) goFiles(p, acc);
    else if (/\.go$/.test(e.name)) acc.push(p);
  }
  return acc;
}
const goSrc = goFiles(path.join(ROOT, 'internal')).map((p) => fs.readFileSync(p, 'utf8')).join('\n');
assert.ok(goSrc.length > 10000, 'the Go scan came back tiny — it broke');
void goDir;

// Collectors with no emit of their own. Named, so a new silent one is a failure
// rather than an assumption.
const NO_EMIT = {
  util: 'shared helpers, not a collector',
  nullCollector: 'the placeholder used when a collector is switched off',
  interfaces: '5 lines; the interface list rides on other payloads',
  arp: 'emits nothing at all — index.js says so where it decides what may be replayed',
};

// SECONDARY events of collectors that ARE ported. A collector is not all-or-
// nothing: three ported ones still hold back an event, each for a reason, and
// without this list the check would demand porting the whole collector again.
const UNSERVED_EVENTS = {
  'stream:health': 'the connection/traffic stream health indicator (connections.js, traffic.js) — ' +
    'no consumer is ported yet',
  'device:new': 'the new-device notification raised when a DHCP lease appears (dhcpLeases.js) — ' +
    'it feeds the notification system, not a page',
  // `lan:wan` WAS here, recorded as deliberately unported because "this side has
  // no router-wide emit convention to send it on". That was never true of the
  // port: `emit("", …)` broadcasts to `router-<id>`, the room every viewer of a
  // router is in (internal/session/session.go:306), and `system:update`,
  // `wan:status` and `ifstatus:names` have always used it. Closed 2026-08-24 —
  // the note had blocked `ndWanIp`, the Dashboard's last unwritten id, for
  // several iterations on a premise nobody checked against session.go.
};

// Collectors that emit but have no Go counterpart yet. Each must STILL be
// missing, so porting one fails this check until its line goes.
// Empty, and that is the point: every live collector with an emit now has a Go
// counterpart. `ping` left this list in Part 67. A new one added upstream lands
// here as a failure rather than as a sentence nobody checks.
const UNPORTED = {};

// FROZEN: the facts this gate reads OUT of the live collectors — which files
// exist, what each emits, and which event names each mentions.
//
// THE COMPARISON THEY FEED STAYS LIVE. Every one of these facts is checked
// against `goSrc`, which is read from `internal/` on every run, so a Go collector
// that stops serving an event still fails here. Freezing the live half turns the
// question from "does the port cover the reference" into "does the port still
// cover the collector set the reference had", which is the same check for as long
// as it matters and is answerable without the reference.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('collector-coverage-check');
const live = G.value('the live collectors, their emits and their event mentions', () => {
  const found = fs.readdirSync(liveDir).filter((f) => /\.js$/.test(f));
  const out = {};
  for (const f of found) {
    const body = fs.readFileSync(path.join(liveDir, f), 'utf8');
    out[f] = {
      emits: [...new Set([...body.matchAll(/emit\(\s*['"]([a-z]+:[a-zA-Z]+)['"]/g)].map((m) => m[1]))],
      // Single-quoted only, matching the `includes("'" + ev + "'")` this replaces.
      mentions: [...new Set([...body.matchAll(/'([a-z]+:[a-zA-Z]+)'/g)].map((m) => m[1]))],
    };
  }
  return out;
});
const files = Object.keys(live);
// NOT guarded: this validates the RECORDING as much as the scan. A golden that
// lost most of its collectors reads exactly like a broken directory walk.
assert.ok(files.length > 20, 'only ' + files.length + ' live collectors found — the scan broke');

const problems = [];
const ported = [];
for (const f of files) {
  const name = f.replace(/\.js$/, '');
  const emits = live[f].emits;

  if (!emits.length) {
    if (!NO_EMIT[name]) {
      problems.push(name + '.js emits nothing and is not recorded as infrastructure — ' +
        'either it lost its emit or it is new');
    }
    continue;
  }
  if (NO_EMIT[name]) {
    problems.push(name + '.js is recorded as emitting nothing, but emits ' + emits.join(', '));
    continue;
  }
  // The fact: does any Go collector send this event?
  const served = emits.filter((e) => goSrc.includes('"' + e + '"') || UNSERVED_EVENTS[e]);
  if (served.length === emits.length) {
    ported.push(name);
    if (UNPORTED[name]) {
      problems.push(name + ' is ported now — remove it from UNPORTED (' + UNPORTED[name] + ')');
    }
    continue;
  }
  if (!UNPORTED[name]) {
    problems.push(name + '.js emits ' + emits.filter((e) => !served.includes(e)).join(', ') +
      ' and no Go collector sends ' + (served.length ? 'all of them' : 'any of them') +
      '. Port it, or record it in UNPORTED with what it feeds.');
  }
}
for (const [ev, why] of Object.entries(UNSERVED_EVENTS)) {
  if (goSrc.includes('"' + ev + '"')) {
    problems.push(ev + ' is served now — remove it from UNSERVED_EVENTS (' + why.slice(0, 60) + '…)');
  }
  const stillEmitted = files.some((f) => live[f].mentions.includes(ev));
  if (!stillEmitted) {
    problems.push('UNSERVED_EVENTS names ' + ev + ', which no live collector emits any more');
  }
}
for (const name of Object.keys(UNPORTED)) {
  if (!files.includes(name + '.js')) {
    problems.push('UNPORTED names ' + name + ', which is no longer a live collector');
  }
}

if (problems.length) {
  console.error('collector-coverage-check: %d problem(s)\n', problems.length);
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log('collector-coverage-check: %d of %d emitting collectors ported, %d recorded unported, %d infrastructure',
  ported.length, ported.length + Object.keys(UNPORTED).length,
  Object.keys(UNPORTED).length, Object.keys(NO_EMIT).length);
