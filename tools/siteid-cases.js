'use strict';
/**
 * `_cleanSiteIds`, run from the live `src/routers.js`.
 *
 * ---- WHY THIS EXISTS ------------------------------------------------------
 *
 * `cleanSiteIDs` in `internal/store/routeradd.go` trimmed and deduped but never
 * applied `_SITE_ID_RE`, which the live `_cleanSiteId` does. The READ path
 * (`routers_public.go:normalizeSites`) filtered on top, so nothing was visibly
 * broken — but the file on disk could hold an id the live app would have
 * dropped, and the filtering lived in two places instead of one.
 *
 * ---- IT DROPS, IT DOES NOT REJECT -----------------------------------------
 *
 * Worth stating because the decision was put as "reject an invalid id" and the
 * live behaviour is not that. `_cleanSiteIds`'s own comment: "a malformed entry
 * is dropped rather than raising, because that is how a bad id has always
 * behaved here and the pickers submit '' for 'no site'." So a write carrying one
 * good id and one bad one SUCCEEDS with the good one, and a write carrying only
 * a bad one succeeds with no membership at all.
 *
 * ---- ORDER IS MEANINGFUL --------------------------------------------------
 *
 * "The FIRST entry is the primary, which supplies the map's site geo tier and is
 * what the `siteId` mirror stores." So this is compared as a SEQUENCE, and the
 * corpus carries a case where dropping an invalid id CHANGES which id is
 * primary — which a set comparison would miss entirely.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/siteid-cases.js
 *   MIKRODASH_SRC=../MikroDash node tools/siteid-cases.js --check
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', 'siteid-cases.json');

// Sliced rather than required: `routers.js` reaches for DATA_DIR at load, and
// these two functions are pure.
const src = fs.readFileSync(path.join(LIVE, 'src', 'routers.js'), 'utf8');
const lines = src.split('\n');
const reAt = lines.findIndex((l) => l.startsWith('const _SITE_ID_RE'));
if (reAt < 0) throw new Error('anchor lost: const _SITE_ID_RE');
const oneAt = lines.findIndex((l) => l.startsWith('function _cleanSiteId('));
if (oneAt < 0) throw new Error('anchor lost: function _cleanSiteId(');
const manyAt = lines.findIndex((l) => l.startsWith('function _cleanSiteIds('));
if (manyAt < 0) throw new Error('anchor lost: function _cleanSiteIds(');
const slice = [
  lines[reAt],
  lines.slice(oneAt, oneAt + 5).join('\n'),
  lines.slice(manyAt, manyAt + 11).join('\n'),
].join('\n');
if (!slice.includes('_SITE_ID_RE.test') || !slice.includes('indexOf(id) === -1')) {
  throw new Error('the slice lost the regex test or the dedupe — the anchors drifted');
}
const clean = vm.runInNewContext(`${slice}\n_cleanSiteIds;`, Object.create(null),
  { filename: 'routers.js#_cleanSiteIds' });

// Built rather than typed, so no control character appears in this source.
const NUL = String.fromCharCode(0);

const INPUTS = [
  ['a single valid id', 'site-a'],
  ['a list of valid ids', ['site-a', 'site-b']],
  ['duplicates collapse', ['site-a', 'site-a', 'site-b']],
  ['whitespace is trimmed', ['  site-a  ']],
  ['empty string means no site', ''],
  ['null', null],
  ['undefined', undefined],
  ['an empty list', []],
  ['a blank entry among good ones', ['site-a', '', 'site-b']],
  // THE REGEX. Everything below is what the port's write path used to keep.
  ['a slash', ['site/a']],
  ['a dot-dot', ['..']],
  ['a path', ['../../etc/passwd']],
  ['a space inside', ['site a']],
  ['punctuation only', ['!!!']],
  ['a NUL', ['site' + NUL + 'a']],
  ['a newline', ['site\na']],
  ['unicode', ['sité']],
  ['65 characters — one over the cap', ['a'.repeat(65)]],
  ['64 characters — exactly the cap', ['a'.repeat(64)]],
  // ORDER: dropping the first entry PROMOTES the second to primary.
  ['an invalid FIRST entry changes which id is primary', ['bad id', 'site-b']],
  ['valid, invalid, valid', ['site-a', 'bad id', 'site-b']],
  // Non-list, non-string shapes a hand-edited file can hold.
  ['a number', [42]],
  ['a nested list', [['site-a']]],
  // TWO elements, which is what makes the JOIN GLUE visible: `String(["a","b"])`
  // is `a,b`, and the comma then fails the site-id regex. With a single-element
  // nested array the glue never appears, and a port joining with the wrong
  // character passed — that mutation survived until this case existed.
  ['a nested list with two entries', [['site-a', 'site-b']]],
  ['an object', [{ id: 'site-a' }]],
  ['a boolean', [true]],
];

const cases = INPUTS.map(([why, input]) => ({
  why,
  input: input === undefined ? null : input,
  inputWasUndefined: input === undefined,
  ids: clean(input),
}));

// ---- Believability ---------------------------------------------------------
const by = Object.fromEntries(cases.map((c) => [c.why, c]));
const need = (k) => {
  if (!by[k]) throw new Error(`a believability check names a case that is gone: ${k}`);
  return by[k];
};

if (!cases.some((c) => c.ids.length)) throw new Error('nothing survives; the corpus proves nothing');
if (!cases.some((c) => c.ids.length === 0)) throw new Error('nothing is dropped');

// EVERY INVALID SHAPE MUST BE DROPPED. If one survives, this refuses to write
// rather than recording a hole as expected output.
for (const why of ['a slash', 'a dot-dot', 'a path', 'a space inside', 'punctuation only',
  'a NUL', 'a newline', 'unicode', '65 characters — one over the cap']) {
  if (need(why).ids.length !== 0) {
    throw new Error(`${why}: survived _cleanSiteIds as ${JSON.stringify(need(why).ids)}`);
  }
}
// And the boundary the other way, or the length cap is not exercised.
if (need('64 characters — exactly the cap').ids.length !== 1) {
  throw new Error('a 64-character id was dropped; the cap is inclusive');
}
// THE JOIN GLUE. A nested two-element array must come back EMPTY, because the
// comma `String()` inserts fails the regex — which is only observable if the
// glue is a comma.
{
  const c = need('a nested list with two entries');
  if (c.ids.length !== 0) {
    throw new Error(`a nested two-element array survived as ${JSON.stringify(c.ids)}; the comma `
      + 'from String(array) should have failed the regex');
  }
  if (need('a nested list').ids.length !== 1) {
    throw new Error('the single-element nested array no longer survives, so the pair of cases no '
      + 'longer separates the join from the regex');
  }
}

// THE ORDER CASE must actually reorder, or it tests nothing the others do not.
{
  const c = need('an invalid FIRST entry changes which id is primary');
  if (c.ids.length !== 1 || c.ids[0] !== 'site-b') {
    throw new Error(`the primary did not become site-b: ${JSON.stringify(c.ids)}`);
  }
}
if (JSON.stringify(need('valid, invalid, valid').ids) !== JSON.stringify(['site-a', 'site-b'])) {
  throw new Error('the middle invalid entry did not drop cleanly');
}
if (JSON.stringify(need('duplicates collapse').ids) !== JSON.stringify(['site-a', 'site-b'])) {
  throw new Error('duplicates did not collapse');
}

const json = JSON.stringify(
  { generated_from: 'src/routers.js _cleanSiteIds + _SITE_ID_RE', cases }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('STALE: testdata/siteid-cases.json - re-run tools/siteid-cases.js');
    process.exit(1);
  }
  console.log(`siteid-cases: up to date (${cases.length} cases)`);
} else {
  fs.writeFileSync(OUT, json);
  console.log(`wrote ${OUT} (${cases.length} cases, `
    + `${cases.filter((c) => c.ids.length === 0).length} yielding no membership)`);
}
