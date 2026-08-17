'use strict';
// Per-router location on the router store (issue #96).
//
// A router's location has two independent halves: `place`, which a person picks,
// and `auto`, which the server derives from the WAN IP in the background. They
// are written by different callers at different times, and that is where the
// silent failures live:
//
//   1. The router modal sends `{ place }` and NEVER sends `auto`. If an absent
//      `auto` were read as "clear it", every save from the modal would wipe the
//      automatic fix — and nothing would say so, because the map would simply
//      fall back a tier.
//   2. Conversely the background refresh must never touch `place`, or a save
//      race would discard what somebody deliberately chose.
//   3. updateGeoAuto() returning null when nothing changed is load-bearing, not
//      an optimisation: _buildRoutersStats runs every two seconds per viewing
//      socket, and routers.json holds encrypted credentials. A missing
//      change-check rewrites that file several times a second, forever.

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const os       = require('node:os');
const path     = require('node:path');

process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'router-geo-'));

const Routers = require('../src/routers');

let _n = 0;
// Distinct labels: _uniqueLabel() would otherwise rename collisions and make a
// failure read as a labelling bug rather than a location one.
const addRouter = (fields = {}) =>
  Routers.add({ label: 'R' + (++_n), host: '10.0.0.' + _n, ...fields });

const BERLIN = { name: 'Berlin', region: 'BE', cc: 'DE', lat: 52.5174, lon: 13.3985 };
const AUTO   = {
  name: 'Brilon', region: 'NW', cc: 'DE', lat: 51.3924, lon: 8.5663,
  ip: '203.0.113.7', accuracyKm: 5, ts: 1755400000000,
};

// ── The default: no location at all ──────────────────────────────────────────

test('a new router has no geo key, so routers.json is unchanged for it', () => {
  const r = addRouter();
  assert.strictEqual(r.geo, undefined);
  // The stored form matters as much as the in-memory one: an empty object would
  // churn the file for every router nobody has located.
  assert.ok(!Object.prototype.hasOwnProperty.call(JSON.parse(JSON.stringify(r)), 'geo'));
});

test('an information-free geo block is stored as absent, not as {}', () => {
  const r = addRouter({ geo: {} });
  assert.strictEqual(r.geo, undefined);
});

test('a malformed place is dropped rather than written through', () => {
  const r = addRouter({ geo: { place: { name: 'Nowhere', cc: 'XXX', lat: 1, lon: 1 } } });
  assert.strictEqual(r.geo, undefined);
});

// ── The manual pick ──────────────────────────────────────────────────────────

test('add() persists a picked place', () => {
  const r = addRouter({ geo: { place: BERLIN } });
  assert.deepStrictEqual(r.geo.place, BERLIN);
});

test('update() persists a picked place', () => {
  const r = addRouter();
  const u = Routers.update(r.id, { geo: { place: BERLIN } });
  assert.deepStrictEqual(u.geo.place, BERLIN);
});

test('an unrelated edit leaves the location alone', () => {
  // update() rebuilds the record field by field, so a `geo` it never sees must
  // mean "keep". This is the same trap _normalizeCollection documents.
  const r = addRouter({ geo: { place: BERLIN } });
  const u = Routers.update(r.id, { label: 'Renamed' });
  assert.strictEqual(u.label, 'Renamed');
  assert.deepStrictEqual(u.geo.place, BERLIN);
});

test('geo: null clears the location entirely', () => {
  const r = addRouter({ geo: { place: BERLIN } });
  const u = Routers.update(r.id, { geo: null });
  assert.strictEqual(u.geo, undefined);
});

test('place: null clears only the manual pick', () => {
  // This is what the picker's "use automatic" control sends. The automatic fix
  // underneath it has to survive, or clearing an override would leave the router
  // unlocated instead of falling back.
  const r = addRouter({ geo: { place: BERLIN, auto: AUTO } });
  const u = Routers.update(r.id, { geo: { place: null } });
  assert.strictEqual(u.geo.place, undefined);
  assert.strictEqual(u.geo.auto.name, 'Brilon');
});

// ── The half that is easiest to get wrong ────────────────────────────────────

test('saving a place does NOT erase the automatic fix', () => {
  // The modal sends { place } and never { auto }. If this regresses, every save
  // silently discards work the server did, and the only symptom is the map
  // quietly dropping a tier.
  const r = addRouter({ geo: { auto: AUTO } });
  const u = Routers.update(r.id, { geo: { place: BERLIN } });
  assert.deepStrictEqual(u.geo.place, BERLIN);
  assert.strictEqual(u.geo.auto.name, 'Brilon', 'the automatic fix survived the save');
  assert.strictEqual(u.geo.auto.ip, '203.0.113.7');
});

test('auto: null clears only the automatic fix', () => {
  const r = addRouter({ geo: { place: BERLIN, auto: AUTO } });
  const u = Routers.update(r.id, { geo: { auto: null } });
  assert.deepStrictEqual(u.geo.place, BERLIN);
  assert.strictEqual(u.geo.auto, undefined);
});

test('provenance is kept alongside the automatic place', () => {
  const r = addRouter({ geo: { auto: AUTO } });
  assert.strictEqual(r.geo.auto.ip, '203.0.113.7');
  assert.strictEqual(r.geo.auto.accuracyKm, 5);
  assert.strictEqual(r.geo.auto.ts, 1755400000000);
});

// ── updateGeoAuto: the write-suppression guard ───────────────────────────────

test('the first fix is written', () => {
  const r = addRouter();
  const u = Routers.updateGeoAuto(r.id, AUTO);
  assert.ok(u, 'a new fix returns the updated router');
  assert.strictEqual(u.geo.auto.name, 'Brilon');
});

test('an identical repeat writes nothing', () => {
  // The guard that stops a two-second tick from rewriting a credential file.
  const r = addRouter();
  assert.ok(Routers.updateGeoAuto(r.id, AUTO));
  assert.strictEqual(Routers.updateGeoAuto(r.id, AUTO), null, 'no change, no write');
  assert.strictEqual(
    Routers.updateGeoAuto(r.id, { ...AUTO }), null, 'a fresh object is still no change');
});

test('a moved WAN IP re-resolves', () => {
  const r = addRouter();
  Routers.updateGeoAuto(r.id, AUTO);
  const moved = { ...AUTO, ip: '198.51.100.9', name: 'Munich', region: 'BY' };
  const u = Routers.updateGeoAuto(r.id, moved);
  assert.ok(u, 'a different address is a change');
  assert.strictEqual(u.geo.auto.name, 'Munich');
  assert.strictEqual(u.geo.auto.ip, '198.51.100.9');
});

test('clearing an absent fix writes nothing', () => {
  // A router on a private WAN address hits this on every single tick, so it has
  // to be free.
  const r = addRouter();
  assert.strictEqual(Routers.updateGeoAuto(r.id, null), null);
});

test('clearing a stale fix is a change, and is written', () => {
  const r = addRouter();
  Routers.updateGeoAuto(r.id, AUTO);
  const u = Routers.updateGeoAuto(r.id, null);
  assert.ok(u, 'losing a location is a change worth persisting');
  assert.strictEqual(u.geo, undefined);
});

test('the background writer never disturbs the manual pick', () => {
  const r = addRouter({ geo: { place: BERLIN } });
  const u = Routers.updateGeoAuto(r.id, AUTO);
  assert.deepStrictEqual(u.geo.place, BERLIN, 'what a person chose is untouched');
  assert.strictEqual(u.geo.auto.name, 'Brilon');

  const cleared = Routers.updateGeoAuto(r.id, null);
  assert.deepStrictEqual(cleared.geo.place, BERLIN, 'still untouched when the fix is dropped');
});

test('a malformed fix is refused rather than stored', () => {
  const r = addRouter();
  assert.strictEqual(Routers.updateGeoAuto(r.id, { name: 'Bad', cc: 'XXX', lat: 1, lon: 1 }), null);
  assert.strictEqual(Routers.getById(r.id).geo, undefined);
});

test('an unknown router id is a no-op, not a throw', () => {
  assert.strictEqual(Routers.updateGeoAuto('no-such-router', AUTO), null);
});

// ── Persistence across a reload ──────────────────────────────────────────────

test('a location survives a cache invalidation', () => {
  // Proves the block is actually on disk rather than only in the in-memory list.
  const r = addRouter({ geo: { place: BERLIN, auto: AUTO } });
  Routers.invalidateCache();
  const reloaded = Routers.getById(r.id);
  assert.deepStrictEqual(reloaded.geo.place, BERLIN);
  assert.strictEqual(reloaded.geo.auto.ip, '203.0.113.7');
});

test('getPublic() exposes the location — the modal needs it to seed the picker', () => {
  const r = addRouter({ geo: { place: BERLIN } });
  const pub = Routers.getPublic().find((x) => x.id === r.id);
  assert.deepStrictEqual(pub.geo.place, BERLIN);
  assert.strictEqual(pub.password, '', 'and still masks the credential');
});
