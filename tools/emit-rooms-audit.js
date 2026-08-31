'use strict';
/**
 * WHICH ROOMS EACH COLLECTOR PAYLOAD REACHES, port against live.
 *
 * ---- WHY -------------------------------------------------------------------
 *
 * A payload's ROOM decides who sees it, and narrowing one is invisible to every
 * other check in this repo: the collector is correct, the payload is correct,
 * the page that owns the room still works. What breaks is a consumer somewhere
 * else — a dashboard card, or another page that happens to need the same data.
 *
 * On 2026-08-29 the operator reported dashboard cards with no data. The cause
 * was `leases:list`: `dhcpLeases.js:83` is `this.io.emit(...)` — every socket,
 * no room — and this port had scoped it to `page-dhcp`. Two consumers lived
 * outside that page: the dashboard's DHCP card (0 leases where live showed 42)
 * and `connections.ts`, which uses leases to name a device by IP, so every
 * connection rendered as a bare address.
 *
 * Nothing could have caught it. `endpoint-audit` looks at HTTP, the page gates
 * compare rendered DOM for a page that IS in the room, and the payload-keys gate
 * compares shapes rather than delivery.
 *
 * ---- HOW -------------------------------------------------------------------
 *
 * Live: `this.io.to('a').to('b').emit('event', …)` — the chain is the room set.
 * A bare `this.io.emit('event', …)` is ROUTER-WIDE, which this records as `*`.
 * Port: `emit("a,b", "event", …)`, where an empty string means router-wide.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/emit-rooms-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('emit-rooms-audit');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

function liveRooms() {
  const out = {};
  const dir = path.join(LIVE, 'src', 'collectors');
  for (const f of fs.readdirSync(dir).filter((n) => n.endsWith('.js'))) {
    const src = fs.readFileSync(path.join(dir, f), 'utf8');
    // `this.io` optionally followed by .to('x') repeatedly, then .emit('ev'
    for (const m of src.matchAll(/\bio\s*((?:\s*\.\s*to\(\s*'[^']+'\s*\))*)\s*\.\s*emit\(\s*'([^']+)'/g)) {
      const rooms = [...m[1].matchAll(/to\(\s*'([^']+)'\s*\)/g)].map((x) => x[1]);
      const ev = m[2];
      const set = rooms.length ? rooms.slice().sort() : ['*'];
      (out[ev] = out[ev] || []).push(set.join(','));
    }
  }
  return out;
}

function portRooms() {
  const out = {};
  const dir = path.join(ROOT, 'internal', 'collect');
  for (const f of fs.readdirSync(dir).filter((n) => n.endsWith('.go') && !n.endsWith('_test.go'))) {
    const src = fs.readFileSync(path.join(dir, f), 'utf8');
    for (const m of src.matchAll(/\.emit\(\s*"([^"]*)"\s*,\s*"([^"]+)"/g)) {
      const rooms = m[1] ? m[1].split(',').map((s) => s.trim()).sort() : ['*'];
      (out[m[2]] = out[m[2]] || []).push(rooms.join(','));
    }
  }
  return out;
}

// FROZEN — the derived map of event -> rooms, which is the ledger this audit
// compares the Go side against. A lifted VALUE, so it freezes rather than being
// guarded: an empty map would make every Go room look unremarked.
const live = G.value('the live room ledger', () => liveRooms());
if (!live || Object.keys(live).length < 5) {
  throw new Error('only ' + Object.keys(live || {}).length + ' live events recorded — '
    + 'the golden is broken, and this audit would compare against nothing');
}
const port = portRooms();
const problems = [];

// BELIEVABILITY. An extractor that matched nothing would report perfect
// agreement, which is the failure this guards.
if (Object.keys(live).length < 10) {
  console.error('emit-rooms-audit: only %d live emits found — the extractor is broken',
    Object.keys(live).length);
  process.exit(1);
}

let compared = 0;
for (const ev of Object.keys(port).sort()) {
  if (!live[ev]) continue; // ported-only event; nothing to compare against
  compared++;
  const l = [...new Set(live[ev])].sort().join(' | ');
  const p = [...new Set(port[ev])].sort().join(' | ');
  if (l !== p) {
    problems.push(`${ev}\n      live: ${l}\n      port: ${p}`);
  }
}

if (problems.length) {
  console.error('emit-rooms-audit: %d payload(s) reach different rooms\n', problems.length);
  for (const p of problems) console.error('  - %s', p);
  console.error('\n  A narrower room is invisible to every other gate: the collector is right, the');
  console.error('  payload is right, and the page that owns the room still works. What breaks is');
  console.error('  a consumer somewhere else.');
  process.exit(1);
}
console.log('emit-rooms-audit: %d shared payload(s), all reaching the same rooms as live', compared);
