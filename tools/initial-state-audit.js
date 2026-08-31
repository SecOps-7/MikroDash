'use strict';
/**
 * What a browser gets THE MOMENT IT ATTACHES, compared with the live app's
 * `sendInitialState`.
 *
 * ---- WHY THIS EXISTS -------------------------------------------------------
 *
 * On 2026-08-28 the port was found never to emit `collection:status` on connect.
 * It emitted it correctly on every CHANGE, so a viewer who attached after a
 * collector went dormant never learned it and that card was never dimmed. The
 * live app sends it unconditionally in `sendInitialState`, on the line after
 * `collection:config`, with the comment "a card for a disabled collector must be
 * marked as such before it would otherwise start its stale countdown".
 *
 * Nothing caught it. `emit-audit` asks whether an event is emitted ANYWHERE and
 * it was; the unit tests drove the supervisor and it was right; three ledgers
 * passed. Only two live servers side by side showed it, and only because the
 * router happened to have nothing dormant — which made the missing emit the
 * entire difference. That is luck, not verification.
 *
 * This asks the narrower question no other check does: for each event the live
 * app sends ON CONNECT, does this port send it on connect, on page focus, or
 * never?
 *
 * ---- THE PORT DEFERS BY DESIGN, AND THAT IS NOT A DEFECT ------------------
 *
 * The live app sends all forty-odd payloads to every socket at connect. This
 * port's collectors are page-gated: opening a page resumes its collector and
 * replays its last payload. So most of the list SHOULD be missing from the
 * handshake and present in the focus switch, and this audit says so rather than
 * reporting forty differences.
 *
 * What it fails on is an event in NEITHER — sent on connect over there and only
 * on a change over here, which is the shape of the defect above.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/initial-state-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('initial-state-audit');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

// ---- The live side ---------------------------------------------------------
const idx = LIFT.liveSource(ROOT, path.join('src', 'index.js'));
const lines = idx.split('\n');
const start = lines.findIndex((l) => l.startsWith('async function sendInitialState('));
// GUARDED: both anchors ask whether the live SOURCE still holds the function.
if (LIFT.hasReference(ROOT) && start < 0) {
  throw new Error('anchor lost: async function sendInitialState(');
}
// The function ends at the next top-level `}`.
const end = lines.findIndex((l, i) => i > start && l === '}');
if (LIFT.hasReference(ROOT) && end < 0) {
  throw new Error('anchor lost: the closing brace of sendInitialState');
}

// FROZEN — the derived EVENT LIST, not the slice it came from. This is the
// ledger the Go side is compared against, so an empty one would report every
// port emit as fine. The `< 30` floor beneath validates the recording.
const liveEvents = G.value('the live sendInitialState events', () => {
  const body = lines.slice(start, end).join('\n');
  return [...new Set([...body.matchAll(/socket\.emit\('([^']+)'/g)].map((m) => m[1]))].sort();
});
if (liveEvents.length < 30) {
  throw new Error(`sendInitialState emits ${liveEvents.length} events; it had 42 when this was `
    + 'written, so the slice is wrong');
}

// ---- The port side ---------------------------------------------------------
const ws = fs.readFileSync(path.join(ROOT, 'internal', 'server', 'ws.go'), 'utf8');

/** The handshake: selectRouter plus the helpers it calls from there. */
function handshakeEvents() {
  const fnBody = (name) => {
    const i = ws.indexOf(`func (cn *conn) ${name}(`);
    if (i < 0) return '';
    const j = ws.indexOf('\n}\n', i);
    return ws.slice(i, j < 0 ? ws.length : j);
  };
  const select = fnBody('selectRouter');
  if (!select) throw new Error('anchor lost: func (cn *conn) selectRouter');
  // THE HELPERS ARE DISCOVERED, NOT LISTED. A hand-written list of two missed
  // `trafficSelectDefault`, which the handshake calls and which is where
  // `traffic:history` is sent — so a correctly-replayed event was reported as
  // missing. Every `cn.<method>(` the handshake calls is followed one level
  // down, which is where an emit can hide without being a page focus.
  let text = select;
  const called = new Set([...select.matchAll(/cn\.([a-zA-Z]\w*)\(/g)].map((m) => m[1]));
  for (const helper of called) {
    const b = fnBody(helper);
    if (b) text += '\n' + b;
  }
  return new Set([...text.matchAll(/"([a-z][a-zA-Z]*:[a-zA-Z-]+)"/g)].map((m) => m[1]));
}

/** The page-focus switch: what opening a page replays.
 *
 * `resumePage` is the big `switch page` that resumes a collector and replays its
 * last payload; `pageFocus` is the thin caller. Both are read, because a replay
 * could sit in either.
 *
 * THE NAME WAS WRONG at first — this looked for `focusPage`, which does not
 * exist, and the fallback scanned the WHOLE FILE. Every event the port emits
 * anywhere landed in `onFocus`, so the audit would have reported nothing had the
 * seven failures not been emitted outside ws.go entirely. A missing anchor now
 * throws, because an audit that passes by scanning everything is worse than none.
 */
function focusEvents() {
  const i = ws.indexOf('func (cn *conn) resumePage(');
  // A MISSING ANCHOR MUST THROW. Falling back to the whole file would put every
  // event this port emits into `onFocus`, and the audit would report nothing —
  // passing loudly while checking nothing, which is the failure it exists to
  // prevent elsewhere.
  if (i < 0) throw new Error('anchor lost: func (cn *conn) resumePage');
  const j = ws.indexOf('\nfunc (cn *conn) pageBlur(', i);
  if (j < 0) throw new Error('anchor lost: pageBlur, which follows resumePage');
  const text = ws.slice(i, j);
  // The switch must still be a switch. A slice that lost its cases would report
  // every deferred event as missing.
  if ((text.match(/\n\tcase "/g) || []).length < 15) {
    throw new Error('the resumePage slice holds fewer than 15 page cases — the anchors drifted');
  }
  return new Set([...text.matchAll(/"([a-z][a-zA-Z]*:[a-zA-Z-]+)"/g)].map((m) => m[1]));
}

const onConnect = handshakeEvents();
const onFocus = focusEvents();
// Everything this port emits ANYWHERE, so "never" can be distinguished from
// "elsewhere".
const anywhere = new Set(
  [...fs.readdirSync(path.join(ROOT, 'internal'), { recursive: true })]
    .filter((f) => typeof f === 'string' && f.endsWith('.go') && !f.endsWith('_test.go'))
    .flatMap((f) => [...fs.readFileSync(path.join(ROOT, 'internal', f), 'utf8')
      .matchAll(/"([a-z][a-zA-Z]*:[a-zA-Z-]+)"/g)].map((m) => m[1])),
);

// ---- The ledger ------------------------------------------------------------
//
// An event the live app sends on connect that this port sends NEITHER on connect
// NOR on page focus needs a reason. Each was traced to source.
const EXPLAINED = {
  'ros:status': 'MERGED into this port\'s single room-scoped `router:status`, which the handshake '
    + 'does send. Decided during the port.',
  'interfaces:list': 'REPLACED by `ifstatus:names`, which the ifStatus collector emits router-wide.',
  'routers:update': 'The router list is loaded over HTTP by `loadRouters()`; the socket carries '
    + 'only CHANGES. Nothing user-visible differs.',
  'setup:required': 'Fires when there are NO routers, and the handshake it would ride on is a '
    + 'router selection — there is no router to select. Emitted by routers_api.go when the last '
    + 'one is deleted, which is the only way to reach that state with a socket open.',
  'interfaces:error': 'The live picker\'s failure placeholder. This port fills the picker from '
    + '`ifstatus:names` and has no separate error event; recorded in emit-audit as unported.',
  'conn:source-data': 'Replayed by the connections page focus, not the handshake — it is the '
    + 'heaviest payload the app has and only that page renders it.',
};

const problems = [];
const deferred = [];
const sent = [];
for (const ev of liveEvents) {
  if (onConnect.has(ev)) { sent.push(ev); continue; }
  if (onFocus.has(ev)) { deferred.push(ev); continue; }
  if (EXPLAINED[ev]) continue;
  problems.push(`${ev} — the live app sends it on connect; this port sends it `
    + (anywhere.has(ev) ? 'only from elsewhere (a collector tick, or a change). A viewer that '
      + 'attaches after the fact never learns the current value.'
      : 'NOWHERE AT ALL.'));
}

// The ledger's other direction: an entry for an event the port now sends.
for (const ev of Object.keys(EXPLAINED)) {
  if (onConnect.has(ev)) {
    problems.push(`${ev} is in EXPLAINED but the handshake sends it now — delete the entry.`);
  }
  if (!liveEvents.includes(ev)) {
    problems.push(`${ev} is in EXPLAINED but sendInitialState no longer sends it — delete it.`);
  }
}

if (problems.length) {
  console.error('initial-state-audit FAILED:');
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log(`initial-state-audit: ${liveEvents.length} events in sendInitialState — `
  + `${sent.length} on this port's handshake, ${deferred.length} deferred to page focus, `
  + `${Object.keys(EXPLAINED).length} explained`);
