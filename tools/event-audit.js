'use strict';
/**
 * The WebSocket vocabulary, both directions.
 *
 * `tools/wiring-audit.js` asks which ELEMENTS a ported page never touches. This
 * asks the same question of EVENTS, and it found a real bug on its first run:
 * nothing subscribed to `router:active`, so after switching routers the browser
 * was never re-joined to its page room and the page received nothing at all.
 *
 *   A. the Go server emits it, the TypeScript never mentions it
 *      — server work with no consumer, or a page half-ported.
 *   B. the TypeScript subscribes, the Go server never emits it
 *      — a client waiting for something that will never arrive. Silent by
 *        construction: the handler simply never runs, and the page looks like
 *        it merely has nothing to show.
 *
 * Both lists are recorded below with a reason each, and the record is checked in
 * BOTH directions, so an entry that stops being true has to be deleted.
 *
 * ── THE DETECTION HAD A BLIND SPOT, AND IT IS WORTH KEEPING IN MIND ─────────
 *
 * The first version matched `hub.Send(...)` and `hub.Broadcast(...)` only, and
 * reported fifteen events as unemitted. Twelve of those were emitted through the
 * collectors' own `emit(...)` helper. A pattern cannot report a shape it does
 * not recognise — the same lesson the settings generators learned — so the match
 * now covers any call named Send, Broadcast or emit, and the counts below are
 * asserted so a silent collapse in either list fails rather than passing.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/event-audit.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');

function readAll(dir, re, acc) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) { if (e.name !== 'node_modules') readAll(p, re, acc); continue; }
    if (re.test(e.name)) acc.push(fs.readFileSync(p, 'utf8'));
  }
  return acc;
}
const go = readAll(path.join(ROOT, 'internal'), /\.go$/, []).join('\n');
const ts = readAll(path.join(ROOT, 'web', 'src'), /\.ts$/, []).join('\n');

const EV = /"([a-z][a-zA-Z0-9]*:[a-zA-Z0-9:_-]+)"/g;
const emits = new Set();
// The name may be DECORATED — `BroadcastExcept`, `emitTo`, `SendRaw`. Matching
// the bare three was the second blind spot this file has had: adding
// `BroadcastExcept` for the Backups delete made its event invisible to the very
// audit that had asked for it, and the record then insisted it was unserved
// while the server was serving it. Anything whose name CONTAINS one of the three
// and is called counts.
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
  emits.add(m[1]);
}
const subs = new Set([...ts.matchAll(/socket\.on\(\s*'([^']+)'/g)].map((m) => m[1]));
// Events subscribed through a generated table rather than a literal.
for (const f of fs.readdirSync(path.join(ROOT, 'web', 'src', 'gen'))) {
  const body = fs.readFileSync(path.join(ROOT, 'web', 'src', 'gen', f), 'utf8');
  for (const m of body.matchAll(/"event": "([^"]+)"/g)) subs.add(m[1]);
}
// Socket lifecycle, not server events — fired by `web/src/socket.ts` itself.
//
// `connect_error` joined them on 2026-08-28. It is this port's own signal that a
// handshake was REFUSED rather than dropped: the server auth-gates the upgrade,
// so a dead session means `open` never fires, and nothing else can notice —
// there is no `connect` to check on, `session:expired` needs a live socket, and
// the fetch guard cannot see a WebSocket handshake. The live app takes the same
// signal from Socket.IO's own `connect_error`; here it is raised by `socket.ts`
// when a close arrives on a connection that never opened.
for (const e of ['connect', 'disconnect', 'connect_error']) subs.delete(e);

if (emits.size < 40) throw new Error('only ' + emits.size + ' emits found — the match broke');
if (subs.size < 40) throw new Error('only ' + subs.size + ' subscriptions found — the match broke');

const DASHBOARD = 'the Dashboard is not ported, and this feeds one of its cards';

// ── A NOTE, NOT AN ENTRY ────────────────────────────────────────────────────
//
// `backups:ran` is emitted from TWO places in the live app: a delete, to nudge
// OTHER viewers into re-requesting their own payload, and the scheduler's result
// hook. The delete half is implemented here (hub.BroadcastExcept), which is why
// the event appears in neither list below.
//
// The scheduler half is NOT a gap and must not be recorded as one: `NewScheduler`
// is written and tested but deliberately never constructed, because a Go
// scheduler running beside the Node one would back up the same fleet twice.
// the port record carries that as a cutover step. An entry here would read as work
// outstanding and quietly invite someone to start it.

// A. Emitted by Go, never mentioned in the TypeScript.
const UNCONSUMED = {
  // `sites:update` was recorded here while the server half shipped ahead of
  // the card's wiring. Closed 2026-08-26: `main.ts` subscribes and
  // `onSitesUpdate` re-renders the table. A short-lived entry is the system
  // working -- the gap was real, visible, and closed by the audit refusing it.
  // `setup:required` WAS here — "the FIRST-RUN SETUP WIZARD is the port record
  // item 17 and is not ported, so nothing in the TypeScript can act on this
  // yet". Closed 2026-08-29: `web/src/pages/setup-overlay-wire.ts` handles it.
  //
  // THIS AUDIT ASKS WHETHER A HANDLER EXISTS, not whether it is mounted — and
  // the wire module is deliberately NOT mounted, because
  // `POST /api/routers/:id/activate` is unported (measured: 404). That is
  // recorded in `reachable-audit` and `module-reachability-audit`, which are the
  // audits that ask the reachability question. Three audits, three questions;
  // none answers another's, and conflating them is how an entry ends up
  // technically true in a file that cannot see what makes it true.

  // emitted-but-unconsumed while the server half shipped ahead of the dialog.
  // The dialog landed on 2026-08-26 and this audit refused to let the entries
  // stand — which is what the note said would happen, and the reason it was
  // written that way rather than left to be noticed.
  'packages:applying': 'VESTIGIAL IN THE LIVE APP TOO — src/index.js emits it and nothing under ' +
    'public/ listens. Reproduced faithfully rather than dropped, so a future reader finds this ' +
    'note instead of "fixing" a consumer into existence',

};

// B. Subscribed by the TypeScript, never emitted by Go.
const UNSERVED = {
  // THE ALERTER IS UNPORTED, and these four are its output. The bell subscribes
  // to them because the bell is ported and its live counterpart subscribes to
  // them; nothing in Go fires an alert yet, so nothing emits them.
  //
  // The FEED they complement is ported (`db.OpenAlerts` / `db.RecentAlerts`), so
  // the panel fills on connect and simply does not update live until the alerter
  // lands. That is a visible gap and a working page, which beats an empty one.
  'alert:fired': 'the alerter is unported — it holds per-router evaluator state and it SENDS. ' +
    'The bell renders the stored feed without it',
  'alert:resolved': 'the alerter is unported — see alert:fired',

  'stream:health': 'neither the traffic nor the connections collector reports stream health on ' +
    'this side. It is a NODE-shaped fact — how many times that process restarted a stream — so a ' +
    'Go version would report on the Go streams rather than mirror Node\'s. The warning element ' +
    'stays empty until it exists, which is the honest state: an empty warning says nothing, ' +
    'where a stale one would say the wrong thing',
  'diagnostics:update': 'there is no diagnostics collector in Go at all. The live one reports ' +
    'how many streams each collector holds and whether geoip-lite loaded — both facts about the ' +
    'SERVER, so a Go version would report on the Go process rather than mirror Node\'s numbers. ' +
    'The card renders empty until it exists, which is honest: it would otherwise show another ' +
    'process\'s stream counts',
};

const unconsumed = [...emits].filter((e) => !subs.has(e) && !ts.includes("'" + e + "'")).sort();
const unserved = [...subs].filter((e) => !emits.has(e)).sort();

const problems = [];
function check(found, record, heading) {
  const missing = found.filter((e) => !record[e]);
  if (missing.length) {
    problems.push(heading + '\n' + missing.map((e) => '  ' + e).join('\n'));
  }
  const closed = Object.keys(record).filter((e) => !found.includes(e));
  if (closed.length) {
    problems.push('These entries are no longer true — delete them:\n' +
      closed.map((e) => '  ' + e).join('\n'));
  }
}
check(unconsumed, UNCONSUMED,
  'Emitted by the Go server, never mentioned in the TypeScript. Each needs a consumer\n' +
  'or a UNCONSUMED entry saying why not:');
check(unserved, UNSERVED,
  'Subscribed by the TypeScript, never emitted by the Go server — a handler that can\n' +
  'never run. Each needs an emitter or an UNSERVED entry saying why not:');

// The bug this audit was written by finding. Pinned so it cannot regress
// quietly: without it, a router switch leaves the browser out of its page room.
if (!ts.includes("'router:active'")) {
  problems.push('Nothing subscribes to `router:active`. Rooms are per-router — a switch drops\n' +
    'every room and joins the new base room only, so the page receives NOTHING until the\n' +
    'user navigates away and back. See the handler in web/src/main.ts.');
}

if (problems.length) {
  console.error('the event audit disagrees with its record:\n\n' + problems.join('\n\n') + '\n');
  process.exit(1);
}
console.log('event audit clean: ' + emits.size + ' Go emits, ' + subs.size + ' subscriptions, ' +
  unconsumed.length + ' unconsumed and ' + unserved.length + ' unserved, all recorded');
