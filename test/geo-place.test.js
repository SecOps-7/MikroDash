'use strict';
// Places: validation, display and priority resolution (issue #96).
//
// Locations are never typed as coordinates — they are picked from the gazetteer,
// or inferred from a WAN IP. Three things can fail silently here, so each gets
// pinned:
//
//   1. A missing coordinate coercing to 0. Number(null), Number(undefined) and
//      Number('') are all finite zeros, so the obvious Number.isFinite() check
//      quietly places an unlocated site off the coast of Africa — precisely what
//      migration 4's comment warned about when it reserved the sites columns.
//   2. A numeric subdivision code rendered as if it were a name: geoip stores
//      Hiroshima's region as '34', which reads as a typo in "Motomachi, 34, JP".
//   3. The priority order drifting. It is the whole feature, and a renderer that
//      re-derives it is a second implementation that can disagree.

const { test } = require('node:test');
const assert   = require('node:assert');

const {
  normalizePlace, formatPlace, resolveLocation, autoGeoAction,
} = require('../src/geoPlace');

const BERLIN = { name: 'Berlin', region: 'BE', cc: 'DE', lat: 52.5174, lon: 13.3985 };

// ── normalizePlace: accepts ──────────────────────────────────────────────────

test('a well-formed place round-trips unchanged', () => {
  assert.deepStrictEqual(normalizePlace(BERLIN), BERLIN);
});

test('a lower-case country code is accepted and upper-cased', () => {
  // The gazetteer only ever emits upper case, but the value crosses the browser
  // boundary on save, so the server normalises rather than trusting it.
  assert.strictEqual(normalizePlace({ ...BERLIN, cc: 'de' }).cc, 'DE');
});

test('surrounding whitespace is trimmed, not rejected', () => {
  const p = normalizePlace({ ...BERLIN, name: '  Berlin  ', region: ' BE ', cc: ' de ' });
  assert.strictEqual(p.name, 'Berlin');
  assert.strictEqual(p.region, 'BE');
  assert.strictEqual(p.cc, 'DE');
});

test('an absent region is allowed — several hundred places have none', () => {
  assert.strictEqual(normalizePlace({ ...BERLIN, region: '' }).region, '');
});

test('a numeric region is valid data and is preserved', () => {
  // Japan's subdivisions are numeric in this database. formatPlace decides
  // whether to show it; normalizePlace has no business dropping it.
  assert.strictEqual(normalizePlace({ ...BERLIN, region: '34' }).region, '34');
});

test('0,0 is a real coordinate and is accepted', () => {
  // The rejection below is of *absence*, not of zero. Conflating the two would
  // make a legitimate equatorial location unrepresentable.
  const p = normalizePlace({ ...BERLIN, lat: 0, lon: 0 });
  assert.strictEqual(p.lat, 0);
  assert.strictEqual(p.lon, 0);
});

// ── normalizePlace: rejects ──────────────────────────────────────────────────

test('a missing coordinate is rejected rather than read as zero', () => {
  for (const absent of [null, undefined, '']) {
    assert.strictEqual(normalizePlace({ ...BERLIN, lat: absent }), null,
      `lat: ${JSON.stringify(absent)} must not become 0`);
    assert.strictEqual(normalizePlace({ ...BERLIN, lon: absent }), null,
      `lon: ${JSON.stringify(absent)} must not become 0`);
  }
});

test('out-of-range coordinates are rejected', () => {
  assert.strictEqual(normalizePlace({ ...BERLIN, lat: 91 }), null);
  assert.strictEqual(normalizePlace({ ...BERLIN, lat: -91 }), null);
  assert.strictEqual(normalizePlace({ ...BERLIN, lon: 181 }), null);
  assert.strictEqual(normalizePlace({ ...BERLIN, lon: -181 }), null);
});

test('non-numeric coordinates are rejected', () => {
  assert.strictEqual(normalizePlace({ ...BERLIN, lat: 'abc' }), null);
  assert.strictEqual(normalizePlace({ ...BERLIN, lat: NaN }), null);
  assert.strictEqual(normalizePlace({ ...BERLIN, lon: Infinity }), null);
  assert.strictEqual(normalizePlace({ ...BERLIN, lat: {} }), null);
});

test('a three-letter country code is rejected — that is a different standard', () => {
  assert.strictEqual(normalizePlace({ ...BERLIN, cc: 'DEU' }), null);
  assert.strictEqual(normalizePlace({ ...BERLIN, cc: '' }), null);
  assert.strictEqual(normalizePlace({ ...BERLIN, cc: 'D1' }), null);
});

test('an over-long name is rejected', () => {
  assert.strictEqual(normalizePlace({ ...BERLIN, name: 'x'.repeat(65) }), null);
  assert.ok(normalizePlace({ ...BERLIN, name: 'x'.repeat(64) }));
});

test('a country-only fix is a location, not a rejection', () => {
  // geoip answers an address it can only place to a country with an empty city
  // and a 1000 km radius. Requiring a town would drop precisely the approximate
  // fixes the accuracy ring exists to show, leaving the router unlocated instead
  // of roughly located.
  const p = normalizePlace({ name: '', region: '', cc: 'US', lat: 37.751, lon: -97.822 });
  assert.ok(p, 'accepted');
  assert.strictEqual(p.name, '');
  assert.strictEqual(formatPlace(p), 'US');
});

test('a region with no town is not shown on its own', () => {
  // "NW" alone tells nobody anything; it only qualifies a name.
  assert.strictEqual(formatPlace({ name: '', region: 'NW', cc: 'DE' }), 'DE');
});

test('an over-long region is rejected — the source field is three bytes', () => {
  assert.strictEqual(normalizePlace({ ...BERLIN, region: 'ABCD' }), null);
  assert.ok(normalizePlace({ ...BERLIN, region: 'ENG' }));
});

test('non-objects are rejected rather than coerced', () => {
  for (const bad of [null, undefined, 'Berlin', 42, true, [BERLIN]]) {
    assert.strictEqual(normalizePlace(bad), null, `${JSON.stringify(bad)} is not a place`);
  }
});

// ── formatPlace ──────────────────────────────────────────────────────────────

test('an alphabetic region is shown', () => {
  assert.strictEqual(formatPlace(BERLIN), 'Berlin, BE, DE');
  assert.strictEqual(formatPlace({ name: 'London', region: 'ENG', cc: 'GB' }), 'London, ENG, GB');
});

test('a numeric region is dropped rather than rendered as a name', () => {
  assert.strictEqual(
    formatPlace({ name: 'Motomachi', region: '34', cc: 'JP' }), 'Motomachi, JP');
});

test('an absent region is simply omitted', () => {
  assert.strictEqual(formatPlace({ name: 'Somewhere', region: '', cc: 'ZZ' }), 'Somewhere, ZZ');
});

test('a place with no name formats to the empty string, not "undefined"', () => {
  assert.strictEqual(formatPlace(null), '');
  assert.strictEqual(formatPlace({}), '');
});

// ── resolveLocation: the priority order ──────────────────────────────────────

const AUTO = {
  name: 'Brilon', region: 'NW', cc: 'DE', lat: 51.3924, lon: 8.5663,
  ip: '203.0.113.7', accuracyKm: 5,
};
const SITE = {
  name: 'Head Office', lat: 51.5, lon: -0.12,
  place_name: 'London', place_region: 'ENG', place_cc: 'GB',
};

test("the router's own pick beats everything", () => {
  const r = resolveLocation({ geo: { place: BERLIN, auto: AUTO } }, SITE);
  assert.strictEqual(r.source, 'manual');
  assert.strictEqual(r.label, 'Berlin, BE, DE');
  assert.strictEqual(r.lat, 52.5174);
});

test('the automatic fix is used when there is no pick', () => {
  const r = resolveLocation({ geo: { auto: AUTO } }, SITE);
  assert.strictEqual(r.source, 'auto');
  assert.strictEqual(r.label, 'Brilon, NW, DE');
  assert.strictEqual(r.accuracyKm, 5);
  assert.strictEqual(r.wanIp, '203.0.113.7');
});

test('the site is the fallback when the router has neither', () => {
  const r = resolveLocation({ geo: {} }, SITE);
  assert.strictEqual(r.source, 'site');
  assert.strictEqual(r.label, 'London, ENG, GB');
  assert.strictEqual(r.lat, 51.5);
});

test('a router with nothing anywhere is unlocated', () => {
  assert.strictEqual(resolveLocation({}, null), null);
  assert.strictEqual(resolveLocation({ geo: {} }, null), null);
  assert.strictEqual(resolveLocation(null, null), null);
});

test('a vague automatic fix still outranks the site', () => {
  // The agreed order is strict: the router's own IP speaks for it even when the
  // fix is only country-accurate. Changing this is a product decision, not a
  // refactor, so it is pinned.
  const vague = {
    name: 'Wichita', region: 'KS', cc: 'US', lat: 37.751, lon: -97.822, accuracyKm: 1000,
  };
  const r = resolveLocation({ geo: { auto: vague } }, SITE);
  assert.strictEqual(r.source, 'auto');
  assert.strictEqual(r.accuracyKm, 1000);
});

// ── resolveLocation: the site row's edges ────────────────────────────────────

test('a site with only one coordinate is not a location', () => {
  // The half-written row is the dangerous one: it would place the site on the
  // prime meridian or the equator with no signal that anything is wrong.
  assert.strictEqual(resolveLocation({}, { name: 'X', lat: 51.5, lon: null }), null);
  assert.strictEqual(resolveLocation({}, { name: 'X', lat: 51.5 }), null);
  assert.strictEqual(resolveLocation({}, { name: 'X', lat: null, lon: -0.12 }), null);
  assert.strictEqual(resolveLocation({}, { name: 'X', lat: '', lon: '' }), null);
});

test('a site with coordinates but no place name falls back to the site name', () => {
  // Migration 4 reserved lat/lon before a picker existed, so such a row is
  // reachable on an install that set them through the API.
  const r = resolveLocation({}, { name: 'Old Site', lat: 10, lon: 20 });
  assert.strictEqual(r.source, 'site');
  assert.strictEqual(r.label, 'Old Site');
});

test('a malformed stored place is ignored, not rendered', () => {
  // Storage is validated on write, but a hand-edited routers.json or a rolled-back
  // binary can still present nonsense. It must fall through to the next tier.
  const r = resolveLocation({ geo: { place: { name: 'Bad', cc: 'XXX', lat: 1, lon: 1 } } }, SITE);
  assert.strictEqual(r.source, 'site');
});

test('an automatic fix with no accuracy reports null rather than zero', () => {
  // Zero would draw an accuracy ring of radius zero, which reads as a precise
  // fix — the opposite of what an unknown radius means.
  const r = resolveLocation({ geo: { auto: { ...AUTO, accuracyKm: undefined } } }, null);
  assert.strictEqual(r.accuracyKm, null);
});

// ── autoGeoAction: keep vs clear ─────────────────────────────────────────────
//
// This section exists because of a bug that a green test suite did not catch and
// a browser did. The first implementation treated "no WAN address to look at"
// and "an address we cannot place" as the same thing and cleared the cached
// location for both. An offline router has no address to look at — so every
// offline router silently lost its position and fell into the tray, which is the
// exact opposite of why the fix is cached rather than resolved live.

test('no WAN address keeps whatever was already known', () => {
  // The offline case. We have learned nothing new, so we must not forget.
  for (const none of ['', null, undefined]) {
    assert.strictEqual(autoGeoAction(none, null, 1).action, 'keep',
      `wanIp ${JSON.stringify(none)} must not clear a cached location`);
  }
  // Even if a lookup result is somehow supplied, no address means no decision.
  assert.strictEqual(autoGeoAction('', { ll: [1, 2] }, 1).action, 'keep');
});

test('an address that cannot be placed clears the stale fix', () => {
  // The CGNAT/RFC1918 case: the router has moved somewhere unresolvable, so a
  // position derived from its previous address is now wrong.
  assert.strictEqual(autoGeoAction('192.168.1.1', null, 1).action, 'clear');
  assert.strictEqual(autoGeoAction('10.0.0.1', { ll: [null, null] }, 1).action, 'clear');
  assert.strictEqual(autoGeoAction('10.0.0.1', { ll: null }, 1).action, 'clear');
  assert.strictEqual(autoGeoAction('10.0.0.1', {}, 1).action, 'clear');
});

test('a usable lookup becomes a storable fix', () => {
  const d = autoGeoAction('203.0.113.7',
    { city: 'Brilon', region: 'NW', country: 'DE', ll: [51.3924, 8.5663], area: 5 }, 1755400000000);
  assert.strictEqual(d.action, 'set');
  assert.deepStrictEqual(d.auto, {
    name: 'Brilon', region: 'NW', cc: 'DE', lat: 51.3924, lon: 8.5663,
    ip: '203.0.113.7', accuracyKm: 5, ts: 1755400000000,
  });
  // And it must survive the validator it will be stored through.
  assert.ok(normalizePlace(d.auto));
});

test('a country-centroid lookup is still a fix, carrying its vagueness', () => {
  const d = autoGeoAction('198.51.100.4',
    { city: '', region: '', country: 'US', ll: [37.751, -97.822], area: 1000 }, 2);
  assert.strictEqual(d.action, 'set');
  assert.strictEqual(d.auto.accuracyKm, 1000, 'the radius is what stops this reading as a site');
  assert.strictEqual(formatPlace(d.auto), 'US');
});
