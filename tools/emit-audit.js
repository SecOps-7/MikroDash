'use strict';
/**
 * WHAT THE LIVE SERVER EMITS THAT THIS ONE DOES NOT.
 *
 * ---- WHY THIS FILE EXISTS ---------------------------------------------------
 *
 * `event-audit.js` compares the Go server's emits against the TypeScript's
 * subscriptions, in both directions. That catches an event nobody listens to and
 * a subscription nobody serves — but it is blind to an event NEITHER SIDE HAS.
 *
 * On 2026-08-26 that blindness cost a real defect. The Frequency Analyser's
 * server half shipped emitting four of its five events: `wifiscan:done` was
 * never sent, because the registry's callback was never assigned. The live
 * client subscribes to it, so a scan would have run to completion and left the
 * dialog at "scanning" for ever. `event-audit` saw nothing wrong — the port
 * emitted nothing and the port's TypeScript subscribed to nothing, which is a
 * perfectly consistent pair.
 *
 * The missing question is the one this file asks: the LIVE server emits it, so
 * where is it?
 *
 * ---- WHAT COUNTS AS AN ANSWER -----------------------------------------------
 *
 * Not every live event belongs here. Most are for pages this port has not
 * reached, and listing them one by one would be a second copy of the port record itself
 * that goes stale on its own schedule. So the record is by EVENT PREFIX — the
 * feature — and a prefix is either:
 *
 *   PORTED    every event under it must be emitted by Go. A missing one is a
 *             failure, which is the case this file was written for.
 *   UNPORTED  none of it is expected yet, with a reason.
 *
 * A prefix that is neither is also a failure: a new feature must be classified
 * rather than silently ignored, which is the trap the first version of every
 * ledger in this repo has fallen into.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/emit-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('emit-audit');
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

function readAll(dir, re, acc) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) { if (e.name !== 'node_modules') readAll(p, re, acc); continue; }
    if (re.test(e.name)) acc.push(fs.readFileSync(p, 'utf8'));
  }
  return acc;
}

// NOT FROZEN — this is the reference's whole `src/` concatenated, megabytes of
// it, and nothing downstream needs the TEXT. What the comparison consumes is the
// derived SET below, which is what gets recorded instead.
const live = LIFT.hasReference(ROOT)
  ? readAll(path.join(SRC, 'src'), /\.js$/, []).join('\n') : '';
const go = readAll(path.join(ROOT, 'internal'), /\.go$/, []).join('\n');

const EVENT = /^[a-z][a-zA-Z0-9]*:[a-zA-Z0-9:_-]+$/;

// The live side emits through socket.emit, io.emit and a room's emit. Matched on
// the CALL rather than on a receiver name, for the same reason event-audit
// matches decorated Go names: `_room(rid).emit(...)` and `socket.emit(...)` are
// the same act.
// FROZEN — the DERIVED SET, not the source it came from. This is the ledger the
// audit compares the Go side against, so it is a lifted VALUE: guarding it would
// leave the set empty and every Go emit would look unmatched-but-fine.
const liveEmits = new Set(G.value('the live emit ledger', () => {
  const out = new Set();
  for (const m of live.matchAll(/\.emit\(\s*'([^']+)'/g)) {
    if (EVENT.test(m[1])) out.add(m[1]);
  }
  for (const m of live.matchAll(/\.emit\(\s*"([^"]+)"/g)) {
    if (EVENT.test(m[1])) out.add(m[1]);
  }
  return [...out].sort();
}));
if (liveEmits.size < 20) {
  throw new Error('only ' + liveEmits.size + ' live emits recorded — the golden is broken, '
    + 'and this audit would report every Go emit as unmatched');
}

const goEmits = new Set();
// ── THE ARGUMENT LIST MAY CONTAIN A CALL ────────────────────────────────────
//
// This was `([^)]*)`, which stops at the FIRST `)`. That was true of every emit
// in the port until `traffic.go` started building its room name with
// `t.emit(TrafficSub(sample.IfName), "traffic:update", &sample)` — the capture
// then ended inside `TrafficSub(` and the event name was never seen, so this
// audit reported `traffic:update` as having no emitter at all.
//
// It failed in the RIGHT direction and for a real reason: it genuinely could no
// longer see the emitter. But the fact it was reporting was false, and a ledger
// that cannot read the code it audits is worse than one that is merely
// incomplete — this one would have made a session go looking for a missing
// emitter that was three lines above where it stopped reading.
//
// THE EVENT IS NOT ALWAYS THE SAME ARGUMENT, which is what made the first two
// attempts at this wrong. Three shapes are in use and all three are real:
//
//	BroadcastAll("perms:changed", payload)                 -> 1st
//	Send(cn.c, "backups:state", payload)                   -> 2nd
//	BroadcastExcept(room, cn.c, "backups:ran", payload)    -> 3rd
//
// So it skips up to three arguments, where an argument may itself contain a
// call — `t.emit(TrafficSub(sample.IfName), "traffic:update", ...)`. Anything
// deeper wants a parser rather than a regex, and `event-audit`'s
// `emits.size < 40` believability check is what stops a broken expression
// passing as "nothing to report": it caught exactly that during this rewrite,
// reporting 0 emits found.
for (const m of go.matchAll(/\b\w*(?:Send|Broadcast|[Ee]mit)\w*\(\s*(?:(?:[^,()]|\([^()]*\))*,\s*){0,3}"([a-z][a-zA-Z0-9]*:[a-zA-Z0-9:_-]+)"/gs)) {
  goEmits.add(m[1]);
}

const prefixOf = (e) => e.split(':')[0];

/**
 * Features whose events must ALL be emitted by this port.
 *
 * Derived ONCE from what this server already emits, then written down — not
 * recomputed, which would make the audit tautological: a prefix would become
 * "ported" the moment one of its events existed, and the rest could go missing
 * for ever.
 */
const PORTED = new Set([
  'collection',
  'access', 'backups', 'bandwidth', 'bridges', 'capsman', 'conn', 'dns', 'firewall',
  'ifstatus', 'lan', 'leases', 'logs', 'netwatch', 'packages', 'ping', 'ppp',
  'alert', 'alerts', 'perms', 'routers', 'settings', 'setup', 'sites',
  'queues', 'res', 'rosusers', 'router', 'routing', 'session', 'system', 'talkers',
  'topology', 'traffic', 'vlans', 'vpn', 'wan', 'wifi', 'wifiscan', 'wireless',
]);

/**
 * Individual events under a PORTED feature that this server deliberately does
 * not send. Each needs a reason, and the reason has to be about THIS event.
 */
const ALLOWED = {
  'router:switching': 'a DELIBERATE mechanic change, recorded at web/src/main.ts:switchRouter. ' +
    'The live server sends this before the new state so the client can clear the old router\'s ' +
    'rows; this port clears at the moment the client ASKS instead — same instant, one fewer round ' +
    'trip, and the rows never outlive the router they belong to. Nothing user-visible differs, ' +
    'which is the line this port may not cross; the mechanism is not.',
  'router:switch-error': 'emitted by the live app\'s HTTP switch route, which belongs to the same ' +
    'router administration as router:disabled. This port switches over the WebSocket and reports ' +
    'a failure on that channel.',
};

/** Features this port has not reached, and why. */
const UNPORTED = {
  device: 'the device-discovery announcement belongs to the Devices page, blocked with routers',
  interfaces: 'the interfaces picker\'s list/error events belong to the Frequency Analyser\'s ' +
    'sibling flows and the unported Interfaces admin paths',
  ros: 'RouterOS upgrade announcements from the live app\'s own update checker',
  diagnostics: 'there is no diagnostics collector in Go. The live one reports facts about the NODE ' +
    'PROCESS — how many streams each collector holds — so a Go version would report on the Go ' +
    'process rather than mirror Node\'s numbers',
  stream: 'stream health is a Node-shaped fact: how many times THAT process restarted a stream',
};

const missing = [];
const unclassified = [];
for (const e of [...liveEmits].sort()) {
  const p = prefixOf(e);
  if (PORTED.has(p)) {
    if (!goEmits.has(e) && !ALLOWED[e]) missing.push(e);
    continue;
  }
  if (UNPORTED[p]) continue;
  unclassified.push(e);
}

// A PORTED prefix that this port does not emit AT ALL is a mistake in the record
// rather than a missing event: it would report every one of its events as
// missing and bury the real ones.
const emptyPorted = [...PORTED].filter((p) => ![...goEmits].some((e) => prefixOf(e) === p));

const problems = [];
if (missing.length) {
  problems.push('emitted by the LIVE server, missing from this one — its feature is recorded as ported:\n  '
    + missing.join('\n  '));
}
if (emptyPorted.length) {
  problems.push('recorded as PORTED but this server emits nothing under it: ' + emptyPorted.join(', '));
}
if (unclassified.length) {
  problems.push('live events whose feature is in neither PORTED nor UNPORTED — classify them:\n  '
    + unclassified.join('\n  '));
}
// And the record must not outlive its reason: a prefix recorded as UNPORTED that
// this port now emits is a stale entry, which is the failure every ledger here
// is built to catch.
const stale = Object.keys(UNPORTED).filter((p) => [...goEmits].some((e) => prefixOf(e) === p));
if (stale.length) {
  problems.push('recorded as UNPORTED but this server emits them now — move to PORTED: '
    + stale.join(', '));
}
// The same rule for a single event: an allowance that outlived its reason is the
// failure every ledger here exists to catch.
const staleAllowed = Object.keys(ALLOWED).filter((e) => goEmits.has(e));
if (staleAllowed.length) {
  problems.push('recorded as deliberately absent but this server emits them now — delete the '
    + 'entry: ' + staleAllowed.join(', '));
}
// ...AND THE SAME RULE FOR A FEATURE, WHICH WAS MISSING. `ALLOWED` had an orphan
// check and `UNPORTED` did not, so an entry naming a feature the live app does
// not emit AT ALL sat here describing nothing — `notify` did, with a reason
// ("the notification transports are unported") that had ALSO expired, since
// `internal/notify` holds them and it is the CALLER that is missing. Two stale
// facts in one line, and every existing check passed over it: the stale test
// asks whether THIS port emits the prefix, which for a prefix nobody emits is
// false on both sides.
//
// This is the third ledger asymmetry of the same shape found this week. The rule
// worth generalising: every allowance needs BOTH directions checked — is the
// thing still absent here, and is it still present THERE.
const orphanUnported = Object.keys(UNPORTED).filter(
  (p2) => ![...liveEmits].some((e) => prefixOf(e) === p2));
if (orphanUnported.length) {
  problems.push('recorded as UNPORTED but the LIVE app emits nothing under them, so the entry '
    + 'describes no event — delete it: ' + orphanUnported.join(', '));
}
// And an allowance for an event the LIVE app no longer sends describes nothing.
const orphanAllowed = Object.keys(ALLOWED).filter((e) => !liveEmits.has(e));
if (orphanAllowed.length) {
  problems.push('recorded as deliberately absent but the LIVE app does not emit them either: '
    + orphanAllowed.join(', '));
}

if (problems.length) {
  console.error('the emit audit disagrees with its record:\n');
  for (const p of problems) console.error(p + '\n');
  process.exit(1);
}
console.log('emit audit clean: %d live events, %d emitted here; %d feature(s) ported, %d recorded unported',
  liveEmits.size, goEmits.size, PORTED.size, Object.keys(UNPORTED).length);
