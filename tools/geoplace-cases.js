'use strict';
/**
 * Place cases — what the LIVE src/geoPlace.js answers, for the Go port to match.
 *
 * Four pure functions decide where a router is drawn on the map and how
 * confident it looks. Every one of them has a failure mode that renders
 * plausibly and is wrong, which is why this corpus is generated from the live
 * implementation rather than written by hand from its comments:
 *
 *   normalizePlace   Number(null) and Number('') are both 0, so a bare isFinite
 *                    check accepts a MISSING coordinate as the equator. That is
 *                    the "0,0 in the Gulf of Guinea" pin.
 *   formatPlace      a NUMERIC region is dropped — tens of thousands of rows
 *                    carry one, and "Motomachi, 34, JP" reads as a typo.
 *   resolveLocation  a four-tier priority order, and a site row with ONE
 *                    coordinate set is not a location.
 *   autoGeoAction    three-way. Folding "no address" into "cannot place it"
 *                    empties the map of every OFFLINE router — the ones the view
 *                    exists to show.
 *
 * ── THE CORPUS IS WIDENED ON PURPOSE ────────────────────────────────────────
 *
 * The obvious cases pass against a wrong implementation. Each group below exists
 * because one specific mutation survives without it: absent-vs-zero coordinates,
 * the exact NAME_MAX boundary, a two- vs three-letter country code, a region at
 * exactly 3 characters, latitude at ±90 and just past it, a site carrying
 * coordinates but no place name, and an accuracyKm that is zero, negative or
 * unparseable.
 *
 * ── NOTHING HERE IS REAL ────────────────────────────────────────────────────
 *
 * Synthetic places and documentation addresses (TEST-NET-2). No router, site or
 * WAN address from the operator's network. `now` is pinned so the corpus is not
 * wall-clock dependent — a --check that flips at midnight is not a gate.
 *
 *   node tools/geoplace-cases.js            write testdata/geoplace-cases.json
 *   node tools/geoplace-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'geoplace-cases.json');

const GeoPlace = require(path.join(LIVE, 'src', 'geoPlace.js'));

const NAME_MAX = GeoPlace.NAME_MAX;
const name64 = 'x'.repeat(NAME_MAX);
const name65 = 'x'.repeat(NAME_MAX + 1);

// ── normalizePlace ───────────────────────────────────────────────────────────
const NORMALIZE = [
  ['a complete place', { name: 'Berlin', region: 'BE', cc: 'DE', lat: 52.52, lon: 13.4 }],
  ['a lower-case country code is accepted', { name: 'Berlin', region: 'BE', cc: 'de', lat: 52.52, lon: 13.4 }],
  ['a padded country code is trimmed', { name: 'Berlin', region: 'BE', cc: ' de ', lat: 52.52, lon: 13.4 }],
  ['a three-letter country code is not', { name: 'Berlin', region: 'BE', cc: 'DEU', lat: 52.52, lon: 13.4 }],
  ['a one-letter country code is not', { name: 'Berlin', region: 'BE', cc: 'D', lat: 52.52, lon: 13.4 }],
  ['no country code at all', { name: 'Berlin', region: 'BE', lat: 52.52, lon: 13.4 }],

  // The town is OPTIONAL: geoip places some addresses only to a country, and
  // requiring a name would drop exactly the approximate fixes the accuracy ring
  // exists to show.
  ['an empty name is allowed', { name: '', region: '', cc: 'DE', lat: 52.52, lon: 13.4 }],
  ['a missing name is allowed', { region: '', cc: 'DE', lat: 52.52, lon: 13.4 }],
  ['a name at exactly NAME_MAX', { name: name64, cc: 'DE', lat: 52.52, lon: 13.4 }],
  ['a name one over NAME_MAX', { name: name65, cc: 'DE', lat: 52.52, lon: 13.4 }],
  ['a name is trimmed before measuring', { name: '  Berlin  ', cc: 'DE', lat: 52.52, lon: 13.4 }],
  ['a non-string name', { name: 42, cc: 'DE', lat: 52.52, lon: 13.4 }],

  ['a numeric region is valid data', { name: 'Motomachi', region: '34', cc: 'JP', lat: 34.4, lon: 132.4 }],
  ['a region at exactly 3', { name: 'Paris', region: 'IDF', cc: 'FR', lat: 48.85, lon: 2.35 }],
  ['a region at 4 is rejected', { name: 'Paris', region: 'IDFX', cc: 'FR', lat: 48.85, lon: 2.35 }],
  ['a region with punctuation is rejected', { name: 'Paris', region: 'I-F', cc: 'FR', lat: 48.85, lon: 2.35 }],
  ['no region at all', { name: 'Paris', cc: 'FR', lat: 48.85, lon: 2.35 }],

  // THE GULF OF GUINEA GROUP. Number(null), Number(undefined) and Number('') are
  // all finite, so absence must be rejected BEFORE any coercion.
  ['a null latitude is absent, not the equator', { name: 'X', cc: 'DE', lat: null, lon: 13.4 }],
  ['a null longitude is absent', { name: 'X', cc: 'DE', lat: 52.52, lon: null }],
  ['an empty-string latitude is absent', { name: 'X', cc: 'DE', lat: '', lon: 13.4 }],
  ['a missing latitude is absent', { name: 'X', cc: 'DE', lon: 13.4 }],
  ['a missing longitude is absent', { name: 'X', cc: 'DE', lat: 52.52 }],
  ['both coordinates missing', { name: 'X', cc: 'DE' }],
  ['a REAL zero coordinate is kept', { name: 'Null Island', cc: 'DE', lat: 0, lon: 0 }],
  ['a numeric string coordinate is coerced', { name: 'X', cc: 'DE', lat: '52.52', lon: '13.4' }],
  ['an unparseable coordinate', { name: 'X', cc: 'DE', lat: 'north', lon: 13.4 }],
  ['a boolean coordinate', { name: 'X', cc: 'DE', lat: true, lon: 13.4 }],

  ['latitude at +90', { name: 'X', cc: 'NO', lat: 90, lon: 13.4 }],
  ['latitude at -90', { name: 'X', cc: 'AQ', lat: -90, lon: 13.4 }],
  ['latitude just past +90', { name: 'X', cc: 'NO', lat: 90.0001, lon: 13.4 }],
  ['longitude at +180', { name: 'X', cc: 'FJ', lat: 0, lon: 180 }],
  ['longitude at -180', { name: 'X', cc: 'FJ', lat: 0, lon: -180 }],
  ['longitude just past -180', { name: 'X', cc: 'FJ', lat: 0, lon: -180.5 }],

  ['null input', null],
  ['a string input', 'Berlin'],
  ['a number input', 7],
  ['an ARRAY input', [{ name: 'Berlin', cc: 'DE', lat: 1, lon: 2 }]],
  ['an empty object', {}],
];

// ── formatPlace ──────────────────────────────────────────────────────────────
const FORMAT = [
  ['name, alphabetic region and cc', { name: 'Berlin', region: 'BE', cc: 'DE' }],
  ['a NUMERIC region is dropped', { name: 'Motomachi', region: '34', cc: 'JP' }],
  ['a region starting with a digit is dropped', { name: 'X', region: '3A', cc: 'JP' }],
  ['a region with no name to qualify is dropped', { name: '', region: 'BE', cc: 'DE' }],
  ['no region', { name: 'Paris', region: '', cc: 'FR' }],
  ['country only', { name: '', region: '', cc: 'FR' }],
  ['nothing at all', { name: '', region: '', cc: '' }],
  ['null', null],
];

// ── resolveLocation ──────────────────────────────────────────────────────────
const place = (o) => Object.assign({ name: 'Berlin', region: 'BE', cc: 'DE', lat: 52.52, lon: 13.4 }, o);
const auto = (o) => Object.assign(
  { name: 'Hamburg', region: 'HH', cc: 'DE', lat: 53.55, lon: 10.0, ip: '198.51.100.7', accuracyKm: 5 }, o);
const site = (o) => Object.assign(
  { id: 1, name: 'HQ', lat: 48.85, lon: 2.35, place_name: 'Paris', place_region: 'IDF', place_cc: 'FR' }, o);

const RESOLVE = [
  ['manual wins over auto and site', { geo: { place: place(), auto: auto() } }, site()],
  ['auto when there is no manual', { geo: { auto: auto() } }, site()],
  ['auto when the manual place is MALFORMED', { geo: { place: place({ cc: 'DEU' }), auto: auto() } }, site()],
  ['site when there is neither', { geo: {} }, site()],
  ['site when the auto fix is malformed', { geo: { auto: auto({ lat: null }) } }, site()],
  ['nothing at all', { geo: {} }, null],
  ['no geo block', {}, site()],
  ['no geo block and no site', {}, null],
  ['a null router', null, site()],

  // The auto tier carries two extra fields, and both have a rule.
  ['auto accuracy of zero becomes null', { geo: { auto: auto({ accuracyKm: 0 }) } }, null],
  ['auto accuracy negative becomes null', { geo: { auto: auto({ accuracyKm: -3 }) } }, null],
  ['auto accuracy unparseable becomes null', { geo: { auto: auto({ accuracyKm: 'far' }) } }, null],
  ['auto accuracy as a numeric string', { geo: { auto: auto({ accuracyKm: '1000' }) } }, null],
  ['auto with a non-string ip', { geo: { auto: auto({ ip: 12345 }) } }, null],

  // BOTH OR NEITHER: a site row with one coordinate is not a location, and must
  // not be read as the other one being zero.
  ['a site with only a latitude', { geo: {} }, site({ lon: null })],
  ['a site with only a longitude', { geo: {} }, site({ lat: null })],
  ['a site with an out-of-range latitude', { geo: {} }, site({ lat: 91 })],
  ['a site at a REAL zero', { geo: {} }, site({ lat: 0, lon: 0 })],
  // Migration 4 reserved lat/lon before there was a picker, so a row may carry
  // coordinates with no place name — the label falls back to the site's name.
  ['a site with coordinates but no place', { geo: {} },
    site({ place_name: '', place_region: '', place_cc: '' })],
  ['a site with a malformed place cc', { geo: {} }, site({ place_cc: 'FRA' })],
  ['a site with no name either', { geo: {} },
    site({ place_name: '', place_region: '', place_cc: '', name: '' })],
];

// ── autoGeoAction ────────────────────────────────────────────────────────────
const NOW = 1773567000000;
const g = (o) => Object.assign({ city: 'Hamburg', region: 'HH', country: 'DE', ll: [53.55, 10.0], area: 5 }, o);

const ACTION = [
  // KEEP is the offline case: no address to work from, so we have learned
  // nothing and must not forget what we knew.
  ['no address at all', '', g()],
  ['a null address', null, g()],
  ['an address but no lookup', '198.51.100.7', null],
  ['a lookup with no ll', '198.51.100.7', g({ ll: undefined })],
  ['a lookup with a null latitude', '198.51.100.7', g({ ll: [null, 10.0] })],
  ['a lookup with a null longitude', '198.51.100.7', g({ ll: [53.55, null] })],
  ['a lookup with an unparseable latitude', '198.51.100.7', g({ ll: ['north', 10.0] })],
  ['a usable fix', '198.51.100.7', g()],
  ['a country centroid', '198.51.100.8', g({ city: '', region: '', area: 1000 })],
  ['a fix with an unparseable area', '198.51.100.9', g({ area: 'wide' })],
  ['a fix at a real zero', '198.51.100.10', g({ ll: [0, 0] })],
  ['string coordinates', '198.51.100.11', g({ ll: ['53.55', '10.0'] })],
];

function main() {
  const check = process.argv.includes('--check');

  const cases = {
    normalizePlace: NORMALIZE.map(([note, input]) => ({
      note, input, want: GeoPlace.normalizePlace(input),
    })),
    formatPlace: FORMAT.map(([note, input]) => ({
      note, input, want: GeoPlace.formatPlace(input),
    })),
    resolveLocation: RESOLVE.map(([note, router, siteRow]) => ({
      note, router, site: siteRow,
      want: GeoPlace.resolveLocation(router, siteRow),
    })),
    autoGeoAction: ACTION.map(([note, wanIp, lookup]) => ({
      note, wanIp, lookup,
      want: GeoPlace.autoGeoAction(wanIp, lookup, NOW),
    })),
  };

  const total = Object.values(cases).reduce((n, a) => n + a.length, 0);
  const body = JSON.stringify({
    note: 'Generated by tools/geoplace-cases.js from the LIVE src/geoPlace.js. Do not edit.',
    nameMax: NAME_MAX,
    now: NOW,
    cases,
  }, null, 2) + '\n';

  if (check) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body) {
      console.error('testdata/geoplace-cases.json is stale — run: node tools/geoplace-cases.js');
      process.exit(1);
    }
    console.log('geoplace cases up to date (' + total + ' cases)');
    return;
  }
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), OUT) + ' — ' + total + ' cases');
}

main();
