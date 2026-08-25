'use strict';
// Site membership on the router store (issue #78).
//
// Sites live in SQLite and routers in routers.json, so there is no foreign key
// between them — every rule that keeps the two consistent is hand-written here
// and in clearSite(). These pin the edges that would fail *silently*: an update
// that forgets siteId would detach every router on an unrelated label edit, and
// a site id is the one field on this record that comes straight from the
// browser, so its validation is the store's half of the site boundary.

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const os       = require('node:os');
const path     = require('node:path');

process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'router-sites-'));

const Routers = require('../src/routers');

let _n = 0;
// Distinct labels: _uniqueLabel() would otherwise rename collisions and make a
// failure read as a labelling bug rather than a site one.
const addRouter = (fields = {}) =>
  Routers.add({ label: 'R' + (++_n), host: '10.0.0.' + _n, ...fields });

// ── The default: no site ─────────────────────────────────────────────────────

test('a new router with no siteId is site-less, not undefined', () => {
  const r = addRouter();
  assert.strictEqual(r.siteId, null);
});

test('add() persists a valid site id', () => {
  const r = addRouter({ siteId: 'site-berlin' });
  assert.strictEqual(r.siteId, 'site-berlin');
});

test('empty string, null and an absent field all mean "no site"', () => {
  // The picker's "— No site —" option submits '', and a record written before
  // #78 has no field at all. All three have to land on the same value, or
  // "site-less" becomes three states the rest of the code has to know about.
  assert.strictEqual(addRouter({ siteId: '' }).siteId,        null);
  assert.strictEqual(addRouter({ siteId: null }).siteId,      null);
  assert.strictEqual(addRouter({ siteId: undefined }).siteId, null);
  assert.strictEqual(addRouter().siteId,                      null);
});

// ── Validation ───────────────────────────────────────────────────────────────

test('a malformed site id is dropped rather than stored', () => {
  // Site ids reach this field straight from the browser. Anything that is not
  // [A-Za-z0-9_-]{1,64} is not a site id we could have issued, so it becomes
  // "no site" instead of being written through to disk.
  for (const bad of ['../../etc/passwd', 'has spaces', '<script>', 'a/b', "x'; DROP TABLE sites;--", 'x'.repeat(65)]) {
    assert.strictEqual(addRouter({ siteId: bad }).siteId, null, bad);
  }
});

test('a 64-character site id is accepted', () => {
  const max = 'a'.repeat(64);
  assert.strictEqual(addRouter({ siteId: max }).siteId, max);
});

// ── update() ─────────────────────────────────────────────────────────────────

test('update() preserves siteId when the field is omitted', () => {
  // PUT /api/routers/:id sends only the fields the modal changed, so an omitted
  // siteId has to mean "unchanged", never "clear it" — otherwise renaming a
  // router would detach it from its site, and its operators from their access.
  // update() currently provides this twice over (the ...existing spread and the
  // else-branch of the siteId ternary), so removing either alone still passes.
  // This pins the behaviour, not the mechanism.
  const r = addRouter({ siteId: 'site-oslo' });
  const updated = Routers.update(r.id, { label: 'Renamed' });
  assert.strictEqual(updated.siteId, 'site-oslo');
  assert.strictEqual(updated.label, 'Renamed');
});

test('update() moves a router between sites and can clear it with an empty string', () => {
  const r = addRouter({ siteId: 'site-oslo' });
  assert.strictEqual(Routers.update(r.id, { siteId: 'site-lisbon' }).siteId, 'site-lisbon');
  assert.strictEqual(Routers.update(r.id, { siteId: '' }).siteId, null);
});

test('update() with a malformed site id detaches rather than keeping the old one', () => {
  // Deliberate: an unparseable id is treated as "no site", so a bad request can
  // only ever *narrow* access, never silently leave a router in a site the
  // caller did not name.
  const r = addRouter({ siteId: 'site-oslo' });
  assert.strictEqual(Routers.update(r.id, { siteId: 'not a site id' }).siteId, null);
});

// ── clearSite() ──────────────────────────────────────────────────────────────

test('clearSite() detaches only its own members and reports how many', () => {
  const a    = addRouter({ siteId: 'site-doomed' });
  const b    = addRouter({ siteId: 'site-doomed' });
  const keep = addRouter({ siteId: 'site-kept' });
  const none = addRouter();

  assert.strictEqual(Routers.clearSite('site-doomed'), 2);
  assert.strictEqual(Routers.getById(a.id).siteId, null);
  assert.strictEqual(Routers.getById(b.id).siteId, null);
  assert.strictEqual(Routers.getById(keep.id).siteId, 'site-kept');
  assert.strictEqual(Routers.getById(none.id).siteId, null);
});

test('clearSite() on an unknown or empty site changes nothing', () => {
  const r = addRouter({ siteId: 'site-kept-2' });
  assert.strictEqual(Routers.clearSite('site-never-existed'), 0);
  assert.strictEqual(Routers.clearSite(''), 0);
  assert.strictEqual(Routers.clearSite(null), 0);
  assert.strictEqual(Routers.clearSite(undefined), 0);
  assert.strictEqual(Routers.getById(r.id).siteId, 'site-kept-2');
});

// ── Persistence and exposure ─────────────────────────────────────────────────

test('siteId survives a round-trip through disk', () => {
  const r = addRouter({ siteId: 'site-durable' });
  Routers.invalidateCache();
  assert.strictEqual(Routers.getById(r.id).siteId, 'site-durable');
});

test('clearSite() persists, it does not just mutate the cache', () => {
  const r = addRouter({ siteId: 'site-transient' });
  Routers.clearSite('site-transient');
  Routers.invalidateCache();
  assert.strictEqual(Routers.getById(r.id).siteId, null);
});

test('getPublic() exposes siteId while still masking the password', () => {
  // The Routers page groups by site, so siteId has to survive the masking pass.
  const r = addRouter({ siteId: 'site-visible', password: 'hunter2' });
  const pub = Routers.getPublic().find(x => x.id === r.id);
  assert.strictEqual(pub.siteId, 'site-visible');
  assert.strictEqual(pub.password, '••••••••');
});

// ── Many-to-many membership (issue #117) ────────────────────────────────────
//
// A device used to hold one site, so assigning it to a second silently removed
// it from the first. The list is now the real field; `siteId` survives only as
// a write-only mirror of the primary, for a binary rolled back to before #117.

test('a device can belong to several sites at once', () => {
  const r = addRouter({ siteIds: ['site-a', 'site-b'] });
  assert.deepStrictEqual(r.siteIds, ['site-a', 'site-b']);
  // The inverse of the old behaviour: adding the second must not drop the first.
  assert.ok(r.siteIds.includes('site-a'), 'the first site must survive the second');
});

test('the first site is the primary, and siteId mirrors it', () => {
  // The mirror is what a rolled-back binary reads. Without it every device
  // would load site-less there: fail-closed for authorization, but it silently
  // empties the map's site tier and every site chip.
  const r = addRouter({ siteIds: ['site-a', 'site-b'] });
  assert.equal(r.siteId, 'site-a', 'siteId must hold the primary, not null');
});

test('a legacy record with a scalar siteId reads as a one-element list', () => {
  // The whole migration. Nothing rewrites routers.json; the record is
  // normalised on read, so a file nobody edits is never touched.
  const r = addRouter({ siteId: 'site-a' });
  assert.deepStrictEqual(r.siteIds, ['site-a']);
  assert.equal(r.siteId, 'site-a');
});

test('duplicate and malformed ids are dropped, not stored', () => {
  // Same contract the scalar always had: a bad id means "no site" rather than
  // an error, because the pickers submit '' and old records omit the field.
  const r = addRouter({ siteIds: ['site-a', 'site-a', '../etc', '', null, 'site-b'] });
  assert.deepStrictEqual(r.siteIds, ['site-a', 'site-b']);
});

test('an empty list is site-less, exactly like the old null', () => {
  // This is the case rbac-model.test.js relies on to catch a rule written as
  // "everything inherits from a site".
  const r = addRouter({ siteIds: [] });
  assert.deepStrictEqual(r.siteIds, []);
  assert.equal(r.siteId, null);
});

test('update() preserves membership when the field is omitted', () => {
  // A label-only edit must not detach a device from every site it is in.
  const r = addRouter({ siteIds: ['site-a', 'site-b'] });
  const after = Routers.update(r.id, { label: 'Renamed' });
  assert.deepStrictEqual(after.siteIds, ['site-a', 'site-b']);
  assert.equal(after.siteId, 'site-a');
});

test('update() still accepts a bare siteId from an older client', () => {
  const r = addRouter({ siteIds: ['site-a', 'site-b'] });
  const after = Routers.update(r.id, { siteId: 'site-c' });
  assert.deepStrictEqual(after.siteIds, ['site-c'], 'the scalar replaces the whole list');
});

test('clearSite removes one site and leaves the others', () => {
  // The old clearSite nulled the field. Detaching a device from a DELETED site
  // must not take its unrelated memberships with it.
  const r = addRouter({ siteIds: ['site-a', 'site-b'] });
  const changed = Routers.clearSite('site-a');
  assert.ok(changed >= 1);
  const after = Routers.getById(r.id);
  assert.deepStrictEqual(after.siteIds, ['site-b']);
  assert.equal(after.siteId, 'site-b', 'the mirror follows the new primary');
});

test('clearSite on a site the device is not in changes nothing', () => {
  const r = addRouter({ siteIds: ['site-a'] });
  Routers.clearSite('site-zzz');
  assert.deepStrictEqual(Routers.getById(r.id).siteIds, ['site-a']);
});

test('membership survives a round trip through disk', () => {
  const r = addRouter({ siteIds: ['site-a', 'site-b'] });
  const fresh = Routers.loadAll().find((x) => x.id === r.id);
  assert.deepStrictEqual(fresh.siteIds, ['site-a', 'site-b']);
});
