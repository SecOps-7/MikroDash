'use strict';
// The city/town gazetteer behind the location picker (issue #96).
//
// This module reads geoip-lite's INTERNAL binary format, because the package
// exposes lookup(ip) and no way to enumerate places. That buys the picker with no
// new dependency and no outbound call, at the cost of a data format nobody
// promised to keep. Two things therefore matter more than usual:
//
//   1. The degraded path must actually degrade. If the offsets move, the picker
//      has to go quiet and say so — not throw, and not take automatic
//      geolocation or the map down with it. Those tests are last here, and they
//      are what earns the whole approach.
//   2. NOTHING may assert a record count. The counts change with every
//      geoip-lite data refresh — two checkouts of this repo already disagree by
//      thousands of places. Assert shape, ordering and presence; never totals.

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const os       = require('node:os');
const path     = require('node:path');

const cityIndex = require('../src/cityIndex');

// ── The real database ────────────────────────────────────────────────────────

test('the gazetteer builds from the shipped geoip data', () => {
  assert.strictEqual(cityIndex.available(), true);
  assert.strictEqual(cityIndex.unavailableReason(), '');
});

test('every returned row is a well-formed place', () => {
  for (const p of cityIndex.search('san', 50)) {
    assert.ok(p.name && typeof p.name === 'string', 'name is a non-empty string');
    assert.match(p.cc, /^[A-Z]{2}$/, `country code ${JSON.stringify(p.cc)} is ISO alpha-2`);
    assert.match(p.region, /^[A-Za-z0-9]{0,3}$/, `region ${JSON.stringify(p.region)} is short`);
    assert.ok(Number.isFinite(p.lat) && p.lat >= -90 && p.lat <= 90, `lat ${p.lat} in range`);
    assert.ok(Number.isFinite(p.lon) && p.lon >= -180 && p.lon <= 180, `lon ${p.lon} in range`);
  }
});

test('a well-known city is findable and correctly placed', () => {
  const hit = cityIndex.search('berlin', 50).find((p) => p.cc === 'DE' && p.region === 'BE');
  assert.ok(hit, 'Berlin, BE, DE is in the gazetteer');
  // Loose bounds: the coordinate is a city centroid from a database that gets
  // refreshed, so pin the city, not the decimal.
  assert.ok(Math.abs(hit.lat - 52.5) < 0.5, `latitude ${hit.lat} is Berlin's`);
  assert.ok(Math.abs(hit.lon - 13.4) < 0.5, `longitude ${hit.lon} is Berlin's`);
});

test('duplicate names are disambiguated rather than collapsed', () => {
  // Four Berlins is the case that makes a picker either usable or infuriating.
  const berlins = cityIndex.search('berlin', 50).filter((p) => p.name === 'Berlin');
  assert.ok(berlins.length > 1, 'more than one Berlin exists');
  const keys = new Set(berlins.map((p) => `${p.region}|${p.cc}`));
  assert.strictEqual(keys.size, berlins.length, 'each Berlin is a distinct region/country pair');
});

// ── Ranking ──────────────────────────────────────────────────────────────────

test('the prominent city outranks its smaller namesakes', () => {
  // There is no population field in this data. Rank comes from how many IP ranges
  // point at a place, which is the whole reason the range table is scanned at all
  // — without it these come back in file order, and the picker offers Berlin,
  // Connecticut first.
  const first = cityIndex.search('berlin', 5)[0];
  assert.strictEqual(first.name, 'Berlin');
  assert.strictEqual(first.cc, 'DE');

  assert.strictEqual(cityIndex.search('london', 5)[0].cc, 'GB');
  assert.strictEqual(cityIndex.search('paris', 5)[0].cc, 'FR');
});

test('an exact name beats a longer name that merely starts the same', () => {
  const top = cityIndex.search('paris', 5)[0];
  assert.strictEqual(top.name, 'Paris', 'Paris outranks Parisville and friends');
});

test('a prefix shorter than the name still finds it', () => {
  const hits = cityIndex.search('amster', 10);
  assert.ok(hits.some((p) => p.name === 'Amsterdam' && p.cc === 'NL'));
});

test('search is case-insensitive and ignores surrounding space', () => {
  const a = cityIndex.search('BERLIN', 3);
  const b = cityIndex.search('  berlin  ', 3);
  assert.deepStrictEqual(a, b);
  assert.ok(a.length > 0);
});

// ── Guards ───────────────────────────────────────────────────────────────────

test('a query under two characters returns nothing', () => {
  // One letter matches thousands of places and is never a real intent; it would
  // also make every keystroke of a real query do useless work.
  assert.deepStrictEqual(cityIndex.search('b'), []);
  assert.deepStrictEqual(cityIndex.search(' '), []);
  assert.deepStrictEqual(cityIndex.search(''), []);
  assert.deepStrictEqual(cityIndex.search(null), []);
  assert.deepStrictEqual(cityIndex.search(undefined), []);
});

test('the result count is capped however large a limit is asked for', () => {
  assert.ok(cityIndex.search('san', 9999).length <= 50);
  assert.ok(cityIndex.search('san', -5).length > 0, 'a nonsense limit falls back to the default');
  assert.strictEqual(cityIndex.search('san', 3).length, 3);
});

test('a query matching nothing returns an empty list, not an error', () => {
  assert.deepStrictEqual(cityIndex.search('zzzzzznotaplace', 10), []);
});

// ── Degradation: the point of the whole design ───────────────────────────────
//
// Kept last: these repoint GEODATADIR, and the module caches both the index and a
// recorded failure, so running them earlier would poison the tests above.

test('missing data files degrade to unavailable rather than throwing', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cityindex-empty-'));
  const prev = process.env.GEODATADIR;
  process.env.GEODATADIR = dir;
  cityIndex._reset();
  try {
    assert.strictEqual(cityIndex.available(), false);
    assert.ok(cityIndex.unavailableReason().length > 0, 'a reason is recorded for diagnostics');
    assert.deepStrictEqual(cityIndex.search('berlin'), [], 'search stays quiet, does not throw');
  } finally {
    if (prev === undefined) delete process.env.GEODATADIR; else process.env.GEODATADIR = prev;
    cityIndex._reset();
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('a data file whose record size no longer divides evenly is rejected', () => {
  // The shape a format change would actually take. Truncating mid-record is the
  // cheap stand-in for "the offsets moved": either way the file no longer
  // decodes, and decoding it anyway would yield plausible-looking nonsense.
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cityindex-bad-'));
  const prev = process.env.GEODATADIR;
  fs.writeFileSync(path.join(dir, 'geoip-city-names.dat'), Buffer.alloc(1000));
  fs.writeFileSync(path.join(dir, 'geoip-city.dat'), Buffer.alloc(1000));
  process.env.GEODATADIR = dir;
  cityIndex._reset();
  try {
    assert.strictEqual(cityIndex.available(), false);
    assert.match(cityIndex.unavailableReason(), /not a multiple of/);
    assert.deepStrictEqual(cityIndex.search('berlin'), []);
  } finally {
    if (prev === undefined) delete process.env.GEODATADIR; else process.env.GEODATADIR = prev;
    cityIndex._reset();
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('a structurally valid but empty database is rejected, not served', () => {
  // Record sizes divide evenly, so the cheap checks pass — but there are no
  // places in it. Serving an empty gazetteer would read as "your town is not in
  // the list" rather than as a broken install.
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cityindex-thin-'));
  const prev = process.env.GEODATADIR;
  fs.writeFileSync(path.join(dir, 'geoip-city-names.dat'), Buffer.alloc(88 * 10));
  fs.writeFileSync(path.join(dir, 'geoip-city.dat'), Buffer.alloc(24 * 10));
  process.env.GEODATADIR = dir;
  cityIndex._reset();
  try {
    assert.strictEqual(cityIndex.available(), false);
    assert.match(cityIndex.unavailableReason(), /expected at least/);
  } finally {
    if (prev === undefined) delete process.env.GEODATADIR; else process.env.GEODATADIR = prev;
    cityIndex._reset();
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('the gazetteer recovers once the data is readable again', () => {
  // _reset() clears a recorded failure as well as the index. Without that, one bad
  // read would leave the picker dead until a restart.
  cityIndex._reset();
  assert.strictEqual(cityIndex.available(), true);
  assert.ok(cityIndex.search('berlin', 3).length > 0);
});

