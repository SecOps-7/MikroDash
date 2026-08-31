'use strict';
/**
 * `_parseSiteBody` — what a site CREATE or EDIT is allowed to write.
 *
 * ── THE WHOLE DECISION IS ABSENT-VERSUS-NULL-VERSUS-SET ─────────────────────
 *
 * Three distinct inputs, three distinct outcomes, and Go's `map[string]any`
 * conflates two of them — `m["place"]` is nil for both "the key is missing" and
 * "the key is JSON null". This port has been caught by exactly that twice
 * (`collection.PollRetunes`, `store.PageSettings`), so the corpus states each
 * one separately for every field:
 *
 *   absent   the caller did not touch it. LEAVE IT ALONE — which is what stops
 *            a rename from blanking a site's location.
 *   null     an explicit "no location" / "no description".
 *   set      a value, validated.
 *
 * On a CREATE (`partial: false`) `name` is required even when absent; on an EDIT
 * (`partial: true`) an absent name is simply not written. Both are generated.
 *
 * ── AND THE LOCATION MOVES AS FIVE COLUMNS OR NONE ──────────────────────────
 *
 * A site's location is a PICKED PLACE, not typed coordinates (#96). `lat`/`lon`
 * survive as the plotted values but are derived from the choice, which is why
 * all five columns move together and a half-set location is unreachable. A port
 * that wrote `lat`/`lon` from the body would make one reachable.
 *
 * ── LIFTED, NOT RETYPED ─────────────────────────────────────────────────────
 *
 * `src/index.js` calls `server.listen()` at require time, so the function is
 * sliced out and evaluated with the real `GeoPlace` bound — the same module the
 * router store validates against, which is the point: a site and a router must
 * not disagree about what a well-formed place is.
 *
 * ── NOTHING HERE IS REAL ────────────────────────────────────────────────────
 *
 * Synthetic place names. No site from the operator's network.
 *
 *   node tools/site-body-cases.js            write testdata/site-body-cases.json
 *   node tools/site-body-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const OUT = process.env.SITE_BODY_OUT || path.join(ROOT, 'testdata', 'site-body-cases.json');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

const GeoPlace = require(path.join(LIVE, 'src', 'geoPlace.js'));
const src = fs.readFileSync(path.join(LIVE, 'src', 'index.js'), 'utf8');

// ---- THE SLICE -----------------------------------------------------------
const OPEN = 'function _parseSiteBody(body, { partial } = {}) {';
{
  const n = src.split(OPEN).length - 1;
  assert.equal(n, 1, 'the anchor is ambiguous (' + n + ' matches)');
}
const from = src.indexOf(OPEN);
// THE BODY'S BRACE, not the first one after the name. The signature destructures
// `{ partial } = {}`, so `indexOf('{', from)` lands in the PARAMETER LIST and the
// depth walk closes on it — yielding a two-line "function" that passed the slice
// and failed the marker check. The anchor already ends with the body's brace.
const open = from + OPEN.length - 1;
if (src[open] !== '{') throw new Error('the anchor no longer ends at the body brace');
let depth = 0, to = -1;
for (let i = open; i < src.length; i++) {
  if (src[i] === '{') depth++;
  else if (src[i] === '}') { depth--; if (!depth) { to = i + 1; break; } }
}
assert.ok(to > from, 'unbalanced body for _parseSiteBody');
const body = src.slice(from, to);

for (const marker of ['partial', 'GeoPlace.normalizePlace', 'place_name', 'out.lat', '256']) {
  assert.ok(body.includes(marker),
    'the lifted function has no ' + marker + ' — the slice is short, and this gate would '
    + 'then check the port against less than the route validates');
}

const ctx = { String, GeoPlace, module: { exports: {} } };
vm.createContext(ctx);
vm.runInContext(body + '\nmodule.exports = _parseSiteBody;', ctx);
const parseSiteBody = ctx.module.exports;

// ---- THE CASES -----------------------------------------------------------
//
// A place the gazetteer will accept, and one it will not. The valid one is
// checked below rather than assumed: a normalizePlace that rejected everything
// would make every `place` case an error and the corpus would look thorough
// while testing one branch.
const PLACE = { name: 'Northtown', region: 'NR', cc: 'ZZ', lat: 12.5, lon: -3.25 };

const CASES = {
  // ── CREATE ──────────────────────────────────────────────────────────────
  createMinimal: { body: { name: 'Depot' }, partial: false },
  createTrimsTheName: { body: { name: '   Depot   ' }, partial: false },
  createRejectsAnAbsentName: { body: {}, partial: false },
  createRejectsAnEmptyName: { body: { name: '' }, partial: false },
  createRejectsWhitespaceOnly: { body: { name: '     ' }, partial: false },
  // The BOUNDARY, both sides. 64 is allowed and 65 is not.
  createAcceptsA64CharName: { body: { name: 'x'.repeat(64) }, partial: false },
  createRejectsA65CharName: { body: { name: 'x'.repeat(65) }, partial: false },
  // TRIMMED FIRST, then measured: 64 characters padded with spaces is legal.
  createAcceptsAPadded64: { body: { name: '  ' + 'x'.repeat(64) + '  ' }, partial: false },
  // `String(x)` coercion, not a type check -- a number is a legal name.
  createCoercesANumericName: { body: { name: 7 }, partial: false },

  // ── THE NAME ON AN EDIT ─────────────────────────────────────────────────
  //
  // ABSENT is fine on a partial and is NOT written; explicitly empty is still
  // refused. A port that treated absent as "" would refuse every description-only
  // edit, and one that wrote `name: ""` would blank the site.
  editWithoutAName: { body: { description: 'x' }, partial: true },
  editRejectsAnEmptyName: { body: { name: '' }, partial: true },
  // A NULL NAME IS NOW REFUSED. It used to create a site literally called
  // "null": `String(b.name === undefined ? '' : b.name)` uses STRICT undefined,
  // so a JSON null fell through to `String(null)` === 'null', four characters
  // that passed the length check. Filed upstream as ToDo.md 6 and FIXED there on
  // 2026-08-27, so this case records a refusal rather than a quirk.
  aNullNameIsRefused: { body: { name: null }, partial: true },

  // ── DESCRIPTION: absent / null / empty / set ────────────────────────────
  descriptionAbsent: { body: { name: 'D' }, partial: false },
  // EXPLICIT NULL CLEARS IT. Not the same as absent, which leaves it alone.
  descriptionNull: { body: { name: 'D', description: null }, partial: false },
  // ...and so does an empty string: `d || null` makes both write NULL.
  descriptionEmpty: { body: { name: 'D', description: '' }, partial: false },
  descriptionWhitespaceOnly: { body: { name: 'D', description: '   ' }, partial: false },
  descriptionSet: { body: { name: 'D', description: '  main site  ' }, partial: false },
  descriptionAt256: { body: { name: 'D', description: 'y'.repeat(256) }, partial: false },
  descriptionAt257: { body: { name: 'D', description: 'y'.repeat(257) }, partial: false },
  // TRIMMED BEFORE MEASURING here too.
  descriptionPadded256: { body: { name: 'D', description: ' ' + 'y'.repeat(256) + ' ' },
    partial: false },

  // ── PLACE: absent / null / valid / invalid ──────────────────────────────
  //
  // ABSENT IS THE ONE THAT MATTERS. It is what stops a rename from blanking a
  // site's location, and it is the one Go's map lookup cannot tell from null.
  placeAbsentOnAnEdit: { body: { name: 'D' }, partial: true },
  placeNullClearsAllFive: { body: { name: 'D', place: null }, partial: false },
  placeValid: { body: { name: 'D', place: PLACE }, partial: false },
  placeRejectsGarbage: { body: { name: 'D', place: { name: '' } }, partial: false },
  placeRejectsAString: { body: { name: 'D', place: 'Northtown' }, partial: false },
  // COORDINATES ALONE ARE NOT A PLACE. Five columns move together or none do,
  // so a body carrying lat/lon and nothing else must not reach the row.
  placeRejectsBareCoordinates: { body: { name: 'D', place: { lat: 1, lon: 2 } },
    partial: false },
  // ...and lat/lon at TOP LEVEL are ignored outright rather than written.
  topLevelCoordinatesAreIgnored: { body: { name: 'D', lat: 51.5, lon: -0.1 }, partial: false },

  // ── AND THE THINGS A BODY MUST NOT CARRY THROUGH ────────────────────────
  //
  // The parser builds a FRESH object; an unknown key is dropped rather than
  // passed to the writer. A port that patched the body in place would let a
  // caller set `id` or `created_at`.
  unknownKeysAreDropped: {
    body: { name: 'D', id: 'forged', created_at: 1, place_cc: 'XX' }, partial: false },

  // A missing body at all.
  noBodyOnCreate: { body: null, partial: false },
  noBodyOnEdit: { body: null, partial: true },
};

const cases = {};
for (const [name, c] of Object.entries(CASES)) {
  const got = parseSiteBody(c.body, { partial: c.partial });
  cases[name] = {
    body: c.body, partial: c.partial,
    error: got.error === undefined ? null : got.error,
    // `value` is absent on an error, and the two are distinguished on purpose.
    value: got.error === undefined ? got.value : null,
  };
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  const v = (k) => cases[k].value;
  const e = (k) => cases[k].error;

  // The gazetteer must actually ACCEPT something, or every place case is an
  // error and the corpus tests one branch while looking thorough.
  assert.ok(!e('placeValid'), 'the valid place was rejected: ' + e('placeValid'));
  assert.equal(v('placeValid').place_name, 'Northtown');
  assert.equal(v('placeValid').lat, 12.5);
  // ...and REJECT something, or the validation is vacuous.
  assert.ok(e('placeRejectsGarbage'), 'a malformed place was accepted');
  assert.ok(e('placeRejectsBareCoordinates'),
    'coordinates with no place name were accepted -- the five columns no longer move together');

  // ABSENT / NULL / SET are three different answers, which is the whole point.
  assert.ok(!('place_name' in v('placeAbsentOnAnEdit')),
    'an absent place still wrote the location columns -- a rename would blank the map pin');
  assert.equal(v('placeNullClearsAllFive').place_name, null);
  assert.equal(v('placeNullClearsAllFive').lat, null);
  for (const k of ['lat', 'lon', 'place_name', 'place_region', 'place_cc']) {
    assert.ok(k in v('placeNullClearsAllFive'), 'clearing a location left ' + k + ' unwritten');
  }

  assert.ok(!('description' in v('descriptionAbsent')),
    'an absent description was written, so a name-only edit would clear it');
  assert.equal(v('descriptionNull').description, null);
  assert.equal(v('descriptionEmpty').description, null);
  assert.equal(v('descriptionSet').description, 'main site');

  // The name.
  assert.equal(v('createTrimsTheName').name, 'Depot');
  assert.ok(e('createRejectsAnAbsentName'), 'a create with no name was accepted');
  assert.ok(!e('editWithoutAName'), 'an edit with no name was refused');
  assert.ok(!('name' in v('editWithoutAName')), 'an absent name was written on an edit');
  assert.ok(e('editRejectsAnEmptyName'), 'an edit blanked the name');
  assert.ok(e('aNullNameIsRefused'),
    'a null name was accepted -- it used to create a site called "null" and was fixed '
    + 'upstream on 2026-08-27; if that fix was reverted the port must be reread rather than '
    + 'this expectation updated');
  assert.ok(!e('createAcceptsA64CharName'), 'a 64-character name was refused');
  assert.ok(e('createRejectsA65CharName'), 'a 65-character name was accepted');
  assert.ok(!e('createAcceptsAPadded64'), 'a padded 64-character name was refused');
  assert.equal(v('createCoercesANumericName').name, '7');

  assert.ok(!e('descriptionAt256') && e('descriptionAt257'),
    'the description boundary is not at 256');

  // Nothing leaks through.
  for (const k of ['id', 'created_at', 'place_cc']) {
    assert.ok(!(k in v('unknownKeysAreDropped')), k + ' reached the writer from the body');
  }
  assert.ok(!('lat' in v('topLevelCoordinatesAreIgnored')),
    'a top-level lat was written -- a half-set location is reachable');

  assert.ok(e('noBodyOnCreate'), 'a create with no body at all was accepted');
  assert.ok(!e('noBodyOnEdit'), 'an edit with no body was refused');
}

const json = JSON.stringify({
  note: 'Generated by tools/site-body-cases.js from the LIVE src/index.js. Do not edit.',
  cases,
}, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('site-body-cases.json is STALE — re-run without --check');
    process.exit(1);
  }
  console.log('site-body-cases.json is current (' + Object.keys(cases).length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, json);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' cases)');
}
