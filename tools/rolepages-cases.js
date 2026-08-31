'use strict';
/**
 * `_parseRolePages`, lifted out of the live `src/index.js` and RUN.
 *
 * ---- WHY -------------------------------------------------------------------
 *
 * `LOOP.md` item 1a: `POST /api/roles` and `PUT /api/roles/:id` both validate a
 * submitted page matrix before writing it, and this port has no equivalent. The
 * function is pure — a body in, a page list or an error message out — so it can
 * be lifted and driven directly.
 *
 * ---- THE FOUR ANSWERS ARE NOT THREE ---------------------------------------
 *
 * It returns THREE distinguishable shapes and the caller treats them
 * differently:
 *
 *   { value: null }   the key was NOT SUBMITTED — leave the role's pages alone
 *   { value: [...] }  a validated matrix, which REPLACES the whole set
 *   { error: '...' }  a 400, and nothing is written
 *
 * `{ value: null }` and `{ value: [] }` are the dangerous pair. An ABSENT
 * `pages` key means "do not touch"; an EMPTY ARRAY means "this role now confers
 * nothing". A port collapsing them into one nil would either strip every page
 * from a role on an unrelated rename, or silently ignore an operator revoking
 * the last one.
 *
 * ---- THE ERROR STRINGS ARE THE CONTRACT -----------------------------------
 *
 * They are rendered verbatim in the role editor, so they are compared exactly
 * rather than "some error was returned". `Unknown page: x` and
 * `Duplicate page: x` both interpolate the offending key.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/rolepages-cases.js
 *   MIKRODASH_SRC=../MikroDash node tools/rolepages-cases.js --check
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', 'rolepages-cases.json');

// ---- Lift the function by CONTENT anchor -----------------------------------
const src = fs.readFileSync(path.join(LIVE, 'src', 'index.js'), 'utf8');
const lines = src.split('\n');
const at = lines.findIndex((l) => l.startsWith('function _parseRolePages('));
if (at < 0) throw new Error('anchor lost: function _parseRolePages(');

// Walk to the closing brace rather than counting a fixed number of lines: the
// function has grown before, and a fixed slice that lost its tail would compile
// to something subtly shorter.
let depth = 0, end = -1;
for (let i = at; i < lines.length; i++) {
  for (const ch of lines[i]) {
    if (ch === '{') depth++;
    else if (ch === '}') depth--;
  }
  if (depth === 0 && i > at) { end = i; break; }
}
if (end < 0) throw new Error('could not find the end of _parseRolePages');
const body = lines.slice(at, end + 1).join('\n');
if (!/Unknown page: /.test(body) || !/Duplicate page: /.test(body)) {
  throw new Error('the slice lost one of the error strings — the anchors drifted');
}

// The real page registry, so "unknown page" means what it means in the app.
const Pages = require(path.join(LIVE, 'src', 'pages.js'));
const parse = vm.runInNewContext(
  `${body}\n_parseRolePages;`,
  { Pages, Array, Set, String, Object },
  { filename: 'index.js#_parseRolePages' });

// A real page key and a real second one, read from the registry rather than
// typed — a hard-coded key that was renamed upstream would make every "valid"
// case silently exercise the unknown-page branch instead.
const KEYS = Object.keys(Pages.BY_KEY);
if (KEYS.length < 3) throw new Error(`the page registry has only ${KEYS.length} entries`);
const P1 = KEYS[0], P2 = KEYS[1], P3 = KEYS[2];

const INPUTS = [
  // THE THREE ANSWERS.
  ['pages absent — leave the role alone', {}],
  ['pages explicitly undefined is the same as absent', { pages: undefined }],
  ['an EMPTY array means this role confers nothing', { pages: [] }],
  ['one read page', { pages: [{ page: P1, access: 'read' }] }],
  ['one write page', { pages: [{ page: P1, access: 'write' }] }],
  ['several pages keep their order', {
    pages: [{ page: P2, access: 'write' }, { page: P1, access: 'read' }, { page: P3, access: 'write' }],
  }],
  // NOT AN ARRAY.
  ['pages as a string', { pages: 'dashboard' }],
  ['pages as an object', { pages: { dashboard: 'read' } }],
  ['pages as null', { pages: null }],
  ['pages as a number', { pages: 3 }],
  // ROW SHAPE.
  ['a null row', { pages: [null] }],
  ['a string row', { pages: ['dashboard'] }],
  ['a number row', { pages: [7] }],
  ['an empty row object', { pages: [{}] }],
  // THE PAGE KEY.
  ['an unknown page', { pages: [{ page: 'nosuchpage', access: 'read' }] }],
  ['an empty page key', { pages: [{ page: '', access: 'read' }] }],
  ['a missing page key', { pages: [{ access: 'read' }] }],
  ['a numeric page key', { pages: [{ page: 42, access: 'read' }] }],
  // FALSY values, which `String(row.page || '')` turns into the EMPTY string
  // rather than into their digits — so the message is `Unknown page: ` with
  // nothing after it. Without these, a port that dropped the `|| ''` and
  // stringified straight through passes: 42 reads the same either way, and 0
  // does not.
  ['a page key of ZERO becomes empty, not "0"', { pages: [{ page: 0, access: 'read' }] }],
  ['a page key of false becomes empty', { pages: [{ page: false, access: 'read' }] }],
  ['a valid page after an unknown one still fails', {
    pages: [{ page: 'nosuchpage', access: 'read' }, { page: P1, access: 'read' }],
  }],
  // DUPLICATES.
  ['the same page twice', { pages: [{ page: P1, access: 'read' }, { page: P1, access: 'write' }] }],
  ['a duplicate after a good one', {
    pages: [{ page: P1, access: 'read' }, { page: P2, access: 'read' }, { page: P2, access: 'write' }],
  }],
  // ACCESS.
  ['access none', { pages: [{ page: P1, access: 'none' }] }],
  ['access missing', { pages: [{ page: P1 }] }],
  ['access READ in capitals', { pages: [{ page: P1, access: 'READ' }] }],
  ['access as a boolean', { pages: [{ page: P1, access: true }] }],
  ['access as an empty string', { pages: [{ page: P1, access: '' }] }],
];

const cases = INPUTS.map(([why, body]) => {
  const r = parse(body);
  return {
    why,
    body: JSON.parse(JSON.stringify(body === undefined ? {} : body)),
    // `undefined` and an absent key both serialise away, so the SHAPE is
    // recorded explicitly rather than inferred from the JSON.
    error: r.error === undefined ? null : r.error,
    hasValue: Object.prototype.hasOwnProperty.call(r, 'value'),
    valueIsNull: r.value === null,
    value: r.value === null || r.value === undefined ? null : r.value,
  };
});

// ---- Believability ---------------------------------------------------------
const by = Object.fromEntries(cases.map((c) => [c.why, c]));
const need = (k) => {
  if (!by[k]) throw new Error(`a believability check names a case that is gone: ${k}`);
  return by[k];
};

if (!cases.some((c) => c.error)) throw new Error('nothing is ever refused');
if (!cases.some((c) => !c.error)) throw new Error('everything is refused');

// THE DANGEROUS PAIR. Absent must be null; empty must be an empty LIST.
{
  const absent = need('pages absent — leave the role alone');
  if (absent.error || !absent.valueIsNull) {
    throw new Error(`an absent pages key answered ${JSON.stringify(absent)}; it must be a null `
      + 'value, which the caller reads as "leave the role alone"');
  }
  const empty = need('an EMPTY array means this role confers nothing');
  if (empty.error || empty.valueIsNull || empty.value.length !== 0) {
    throw new Error(`an empty array answered ${JSON.stringify(empty)}; it must be an empty LIST, `
      + 'which the caller writes — revoking every page from the role');
  }
}

// ORDER is preserved, so the corpus can catch a port that sorts.
{
  const c = need('several pages keep their order');
  const got = c.value.map((p) => p.page).join(',');
  if (got !== [P2, P1, P3].join(',')) {
    throw new Error(`the order changed: ${got}`);
  }
}

// THE ERROR STRINGS interpolate the offending key, and are shown verbatim.
if (need('an unknown page').error !== 'Unknown page: nosuchpage') {
  throw new Error(`the unknown-page message is now ${JSON.stringify(need('an unknown page').error)}`);
}
if (need('the same page twice').error !== 'Duplicate page: ' + P1) {
  throw new Error(`the duplicate message is now ${JSON.stringify(need('the same page twice').error)}`);
}

// EVERY invalid shape is refused, or the corpus records a hole as expected output.
for (const why of ['pages as a string', 'pages as an object', 'pages as null', 'pages as a number',
  'a null row', 'a string row', 'a number row', 'an empty row object', 'an unknown page',
  'an empty page key', 'a missing page key', 'a numeric page key', 'access none',
  'access missing', 'access READ in capitals', 'access as a boolean',
  'access as an empty string']) {
  if (!need(why).error) throw new Error(`${why}: was ACCEPTED, as ${JSON.stringify(need(why).value)}`);
}

// A GOOD ROW BEFORE A BAD ONE still fails whole — nothing partial is written.
if (!need('a valid page after an unknown one still fails').error) {
  throw new Error('a list with one bad entry was partially accepted');
}

const json = JSON.stringify(
  { generated_from: 'src/index.js _parseRolePages (executed)', pageKeys: [P1, P2, P3], cases },
  null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('STALE: testdata/rolepages-cases.json - re-run tools/rolepages-cases.js');
    process.exit(1);
  }
  console.log(`rolepages-cases: up to date (${cases.length} cases)`);
} else {
  fs.writeFileSync(OUT, json);
  console.log(`wrote ${OUT} (${cases.length} cases, `
    + `${cases.filter((c) => c.error).length} refused)`);
}
