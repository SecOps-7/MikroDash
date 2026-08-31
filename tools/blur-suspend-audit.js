'use strict';
/**
 * A collector that feeds MORE THAN ONE ROOM must not be suspended because one of
 * them emptied.
 *
 * ---- THE DEFECT ------------------------------------------------------------
 *
 * `pageBlur` suspends a page's collector when the viewer leaves. Most collectors
 * feed one room and that is right. Three feed several — the page, a dashboard
 * card, and in one case the router-wide chrome room — and for those, a page blur
 * says nothing about whether anybody is still watching.
 *
 * `conns` had an occupancy guard from the start. `dhcpNetworks` and `bandwidth`
 * did not, so leaving the DHCP page froze the dashboard's Network card and the
 * WAN chip on every page, and leaving the Bandwidth page froze the dashboard's
 * bandwidth card — for the life of the session.
 *
 * Found 2026-08-28 by comparing this port's blur cases against the live
 * `Pages.STREAM_ROOMS`. The port suspends a strict SUPERSET of what the live app
 * suspends (18 pages against 15), which is the right direction — fewer channels
 * held is the documented reason this port exists — and is exactly why the live
 * app never had this bug: it does not suspend these three at all.
 *
 * ---- WHAT THIS CHECKS ------------------------------------------------------
 *
 * For every collector suspended in `pageBlur`: read the ROOMS IT EMITS TO out of
 * `internal/collect`, and require an occupancy guard if there is more than one.
 * The room list is read from the emit, never restated here, so a collector that
 * gains a dashboard card is covered on the day it gains one.
 *
 *   node tools/blur-suspend-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const ws = fs.readFileSync(path.join(ROOT, 'internal', 'server', 'ws.go'), 'utf8');

const i = ws.indexOf('func (cn *conn) pageBlur(');
if (i < 0) throw new Error('anchor lost: func (cn *conn) pageBlur');
const j = ws.indexOf('func (cn *conn) trafficSelectDefault(', i);
if (j < 0) throw new Error('anchor lost: trafficSelectDefault, which follows pageBlur');
const blur = ws.slice(i, j);
if ((blur.match(/\n\tcase "/g) || []).length < 12) {
  throw new Error('the pageBlur slice holds fewer than 12 cases — the anchors drifted');
}

// Accessor -> the rooms that collector emits to, read from its own emit calls.
const collectDir = path.join(ROOT, 'internal', 'collect');
const roomsByType = {};
for (const f of fs.readdirSync(collectDir)) {
  if (!f.endsWith('.go') || f.endsWith('_test.go')) continue;
  const src = fs.readFileSync(path.join(collectDir, f), 'utf8');
  for (const m of src.matchAll(/\.emit\(\s*"([^"]*)"\s*,\s*"([^"]+)"/g)) {
    // Rooms are comma-separated in one string; "" is the router-wide room.
    const rooms = m[1] === '' ? ['<router-wide>'] : m[1].split(',');
    (roomsByType[f] ||= new Set());
    for (const r of rooms) roomsByType[f].add(r.trim());
  }
  // `emit(logRooms, …)` and friends name a constant; resolve the obvious ones.
  for (const m of src.matchAll(/\.emit\((\w+),\s*"([^"]+)"/g)) {
    const decl = src.match(new RegExp(m[1] + '\\s*=\\s*"([^"]+)"'));
    if (decl) {
      (roomsByType[f] ||= new Set());
      for (const r of decl[1].split(',')) roomsByType[f].add(r.trim());
    }
  }
}

// Which file each suspended accessor lives in, by its Suspend method receiver.
const fileOfAccessor = {};
for (const f of Object.keys(roomsByType)) {
  const src = fs.readFileSync(path.join(collectDir, f), 'utf8');
  for (const m of src.matchAll(/func \(\w+ \*(\w+)\) Suspend\(\)/g)) fileOfAccessor[m[1]] = f;
}

// Session accessor name -> collector type. `Session.X()` returns *collect.T.
const sess = fs.readFileSync(path.join(ROOT, 'internal', 'session', 'session.go'), 'utf8');
const typeOfAccessor = {};
for (const m of sess.matchAll(/func \(s \*Session\) (\w+)\(\) \*collect\.(\w+)/g)) {
  typeOfAccessor[m[1]] = m[2];
}

const problems = [];
let checked = 0;

// A bare `rsession.X().Suspend()` inside pageBlur.
for (const m of blur.matchAll(/cn\.rsession\.(\w+)\(\)\.Suspend\(\)/g)) {
  const acc = m[1];
  const type = typeOfAccessor[acc];
  const file = type && fileOfAccessor[type];
  const rooms = file && roomsByType[file];
  if (!rooms) continue; // an accessor whose rooms could not be read; see the floor below
  checked++;
  if (rooms.size > 1) {
    problems.push(`${acc}() is suspended directly in pageBlur, but its collector emits to `
      + `${rooms.size} rooms (${[...rooms].join(', ')}). A page blur says nothing about whether `
      + 'anybody is still watching the others — use suspendIfNoRoomOccupied.');
  }
}

// ── THE AUDIT MUST PROVE ITS OWN DATA IS REAL ──────────────────────────────
//
// Every direct suspend being single-room is the PASSING state, so the
// `rooms.size > 1` branch never fires on a clean run — and a mutation that
// changed the threshold survived, because nothing here showed the room-reading
// works at all. Asserting that multi-room collectors EXIST and were found is
// what makes the clean run mean something.
{
  const multi = Object.entries(roomsByType).filter(([, r]) => r.size > 1);
  if (multi.length < 3) {
    throw new Error(`only ${multi.length} collectors were read as emitting to more than one room; `
      + 'there are at least four (conns, dhcpNetworks, bandwidth, vpn), so the emit-reading above '
      + 'has stopped matching and this audit is checking nothing');
  }
  // And the four the guards exist for must be among them, by name.
  for (const f of ['connections.go', 'dhcpnetworks.go', 'bandwidth.go', 'vpn.go']) {
    if (!roomsByType[f] || roomsByType[f].size < 2) {
      throw new Error(`${f} was not read as multi-room; it is one of the four this audit was `
        + 'written for');
    }
  }
}

// The guarded form must still exist, or the pattern has been removed wholesale.
if (!blur.includes('suspendIfNoRoomOccupied') && !blur.includes('suspendConnsIfIdle')) {
  problems.push('no occupancy-guarded suspend remains in pageBlur; the guard was removed');
}
if (checked < 10) {
  problems.push(`only ${checked} direct suspends were examined; pageBlur had far more when this `
    + 'was written, so the accessor or room patterns have stopped matching');
}

if (problems.length) {
  console.error('blur-suspend-audit FAILED:');
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
// ── WHY THE TOTAL IS PRINTED, AND PRINTED LARGEST ───────────────────────────
//
// The gate census (`tools/verify.sh`) ratchets on the LARGEST number a gate
// prints, on the assumption that it measures how much the gate checked. For a
// while that number here was `checked` -- the count of DIRECT suspends -- which
// falls every time a collector is correctly moved behind an occupancy guard.
//
// So the census fired on an improvement: moving `routing` behind a guard on
// 2026-08-31 took it from 13 to 12 and failed the sweep, for a change that made
// pageBlur MORE correct, not less. Same shape as `credential-audit`, whose number
// measured the repository rather than the check.
//
// The total is the honest measure of coverage: every suspend in pageBlur is
// either direct or guarded, so it only falls if this audit stops SEEING them --
// which is exactly what the census should fire on.
const guarded = (blur.match(/suspendIfNoRoomOccupied|suspendConnsIfIdle/g) || []).length;
console.log(`blur-suspend-audit: ${checked + guarded} suspends in pageBlur (${checked} direct, `
  + `all single-room; ${guarded} behind an occupancy guard)`);
