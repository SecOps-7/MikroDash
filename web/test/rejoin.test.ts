/**
 * A RECONNECT RE-JOINS THE PAGE ROOM.
 *
 * ── THE BUG ─────────────────────────────────────────────────────────────────
 *
 * Reported as "the WiFi Clients table goes stale after watching it for a while",
 * and it was not specific to that page - it was every page-scoped card, on any
 * socket that had reconnected. WiFi Clients simply shows it soonest: a 30s poll
 * leaves the longest visible gap.
 *
 * Room membership is per-CONNECTION. A reconnect arrives as a brand-new server
 * `conn` whose `cn.page` is empty, so `rejoinPage` returns immediately; the
 * client re-sends `router:select`, which rejoins the CARDS but has nothing to
 * say about the page. The one thing that could restore it - `page:focus` - was
 * gated on the router id CHANGING, and on a reconnect it has not changed.
 *
 * The browser then sat in no page room at all while the server considered the
 * session healthy and the collector polled into a room nobody was in.
 *
 * ── MEASURED BEFORE IT WAS FIXED ────────────────────────────────────────────
 *
 * Driving the real sequence over a WebSocket against the live app on
 * 2026-09-07:
 *
 *   with page:focus            4 wireless:update in 75s
 *   reconnect, select only     0 wireless:update in 75s
 *
 * ── WHY THE TEST IS HERE AND NOT ON THE HANDLER ─────────────────────────────
 *
 * The decision used to be three lines inside `main()`, which nothing can import
 * because `main.ts` runs `main()` at module scope. That is why it went unchecked
 * for as long as it did, so the fix moved the decision into `web/src/rejoin.ts`
 * and this asks it directly.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.rejoin-entry.ts');
fs.writeFileSync(ENTRY, "export { rejoinDecision } from '../web/src/rejoin.js';\n");
const OUT = path.join(ROOT, 'testdata', '.rejoin.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const { rejoinDecision } = require(OUT);

const A = 'router-a';
const B = 'router-b';

// ── 1. a first connect does NOT re-join ─────────────────────────────────────
//
// The code that opened the page joined the room already; re-emitting would be a
// second join for a room we are in. Keeping this is what stops the fix turning
// every page open into two joins and two replays.
{
  const { rejoin, next } = rejoinDecision(A, { lastId: '', lostRooms: false });
  assert.strictEqual(rejoin, false,
    'a first connect re-joined, so every page open now costs a duplicate join ' +
    'and a duplicate replay');
  assert.strictEqual(next.lastId, A);
  say('ok  a first connect does not re-join');
}

// ── 2. a switch re-joins, which is the behaviour that already worked ────────
{
  const { rejoin } = rejoinDecision(B, { lastId: A, lostRooms: false });
  assert.strictEqual(rejoin, true,
    'switching router no longer re-joins the page room; the page would show the ' +
    'OLD router\'s data until the user navigated away and back');
  say('ok  a router switch re-joins');
}

// ── 3. a steady connection does not re-join on every heartbeat ──────────────
{
  const { rejoin } = rejoinDecision(A, { lastId: A, lostRooms: false });
  assert.strictEqual(rejoin, false,
    'an unchanged router:active re-joined; the server sends these on more than ' +
    'a switch, so this would rejoin and replay repeatedly');
  say('ok  an unchanged id on a live socket does nothing');
}

// ── 4. THE REGRESSION: a reconnect re-joins even though the id is unchanged ─
{
  const { rejoin, next } = rejoinDecision(A, { lastId: A, lostRooms: true });
  assert.strictEqual(rejoin, true,
    'a reconnect to the SAME router did not re-join the page room. That is the ' +
    'reported bug: room membership is per-connection, the new conn has an empty ' +
    'cn.page so rejoinPage restores nothing, and the browser is left subscribed ' +
    'to no page room while the server looks healthy. Measured: 0 payloads in 75s.');
  assert.strictEqual(next.lostRooms, false,
    'the lost-rooms flag survived the re-join, so the next ordinary router:active ' +
    'would re-join again for no reason');
  say('ok  a reconnect to the same router re-joins the page room');
}

// ── 5. and a reconnect that is ALSO the first id still re-joins ─────────────
//
// A socket that dropped before any `router:active` arrived is in no room either,
// so "first" must not be allowed to mask "lost". This is the case a fix written
// as `if (first) return` before the lost check would get wrong.
{
  const { rejoin } = rejoinDecision(A, { lastId: '', lostRooms: true });
  assert.strictEqual(rejoin, true,
    'a socket that dropped before its first router:active did not re-join. ' +
    '"First" was allowed to mask "rooms lost", which leaves exactly the same ' +
    'empty-room state the fix is for');
  say('ok  first-and-lost still re-joins');
}

// ── 6. an empty id changes nothing ──────────────────────────────────────────
//
// It must not clear the lost flag either: doing so would consume the one signal
// that a re-join is owed, and the real router:active that followed would be
// judged as an ordinary unchanged id.
{
  const before = { lastId: A, lostRooms: true };
  const { rejoin, next } = rejoinDecision('', before);
  assert.strictEqual(rejoin, false, 'an empty activeId asked for a re-join');
  assert.strictEqual(next.lostRooms, true,
    'an empty activeId cleared the lost-rooms flag, swallowing the signal that ' +
    'a re-join is owed - the next real id would then look like an ordinary one');
  say('ok  an empty id neither re-joins nor eats the pending re-join');
}

fs.rmSync(OUT, { force: true });
say('rejoin: all checks passed');
