'use strict';
/**
 * `POST /api/nav-prefs`'s normalisation, recorded by running the live filter.
 *
 * ── WHY THIS ONE IS WORTH A CORPUS ──────────────────────────────────────────
 *
 * It is four lines and one of them is a security boundary. The live comment:
 * "Filtered through the registry rather than stored as sent. An unbounded list
 * of arbitrary strings inside a blob that later gets rendered is how a
 * preference becomes a stored-XSS vector."
 *
 * So the cases are mostly about what the filter REFUSES, and the payloads are
 * the shapes a caller who is not the app would send — a script tag, a key with
 * different case, a very long list, a nested object where a string belongs.
 *
 * The expression is lifted rather than retyped, and `CATEGORY_KEYS` comes from
 * the live `src/pages.js`, so a category added upstream changes this corpus
 * instead of quietly falling outside it.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/navprefs-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const Pages = require(path.join(LIVE, 'src', 'pages.js'));

const src = fs.readFileSync(path.join(LIVE, 'src', 'index.js'), 'utf8');
// MARKER ASSERTIONS on the route, so a corpus cannot be built from a handler
// that has been rewritten underneath it.
for (const marker of ["app.post('/api/nav-prefs'", 'CATEGORY_KEYS.includes(k)',
  "typeof body.grouped !== 'boolean'", 'Array.isArray(body.expanded)']) {
  assert.ok(src.includes(marker),
    'the live nav-prefs route has no ' + marker + ' — it has been rewritten and this corpus '
    + 'describes a handler that no longer exists');
}

// THE NORMALISATION, LIFTED. One expression, and it is the whole of what this
// corpus records: dedupe, filter against the registry, sort.
const normalize = (expanded) => [...new Set(expanded.map(String))]
  .filter((k) => Pages.CATEGORY_KEYS.includes(k))
  .sort();

// The live validity check, also lifted rather than described.
const accepts = (body) => typeof body.grouped === 'boolean' && Array.isArray(body.expanded);

const keys = [...Pages.CATEGORY_KEYS];
assert.ok(keys.length >= 5, 'only ' + keys.length + ' category keys — the registry looks wrong');

const CASES = [
  ['every real category', { grouped: true, expanded: keys.slice() }],
  ['a subset', { grouped: false, expanded: [keys[2], keys[0]] }],
  ['empty', { grouped: true, expanded: [] }],
  ['duplicates collapse', { grouped: true, expanded: [keys[0], keys[0], keys[0]] }],
  ['out of order sorts', { grouped: true, expanded: keys.slice().reverse() }],
  // ── WHAT THE FILTER IS FOR ───────────────────────────────────────────
  ['a script tag', { grouped: true, expanded: ['<script>alert(1)</script>'] }],
  ['an html attribute payload', { grouped: true, expanded: ['" onload="alert(1)'] }],
  ['a very long string', { grouped: true, expanded: ['x'.repeat(5000)] }],
  ['a real key with different case', { grouped: true, expanded: [keys[0].toUpperCase()] }],
  ['a real key with whitespace', { grouped: true, expanded: [' ' + keys[0] + ' '] }],
  ['a prototype key', { grouped: true, expanded: ['__proto__', 'constructor'] }],
  ['one real among many fake', { grouped: true, expanded: ['a', 'b', keys[1], 'c'] }],
  // NON-STRINGS ARE COERCED, not rejected — `.map(String)` runs BEFORE the
  // filter, so a number becomes "7" and is then refused for not being a key.
  // Recorded because a port filtering first would throw on a non-string.
  ['numbers and nulls', { grouped: true, expanded: [7, null, true] }],
  ['a nested object', { grouped: true, expanded: [{ evil: 1 }] }],
  // ── REFUSED OUTRIGHT: the shape is wrong ─────────────────────────────
  ['grouped missing', { expanded: [] }],
  ['grouped is a string', { grouped: 'true', expanded: [] }],
  ['grouped is null', { grouped: null, expanded: [] }],
  ['expanded missing', { grouped: true }],
  ['expanded is a string', { grouped: true, expanded: keys[0] }],
  ['expanded is an object', { grouped: true, expanded: { 0: keys[0] } }],
  ['expanded is null', { grouped: true, expanded: null }],
  ['an empty body', {}],
];

const cases = {};
for (const [name, body] of CASES) {
  const ok = accepts(body);
  cases[name] = {
    body,
    accepted: ok,
    // `expanded` is only computed on the accepted path; recording it for a
    // refused body would describe an expression the route never reaches.
    stored: ok ? { grouped: body.grouped, expanded: normalize(body.expanded) } : null,
  };
}

// ── BELIEVABILITY ───────────────────────────────────────────────────────────
{
  const c = (n) => cases[n];
  assert.ok(c('every real category').accepted);
  assert.equal(c('every real category').stored.expanded.length, keys.length,
    'the filter dropped a REAL category, so it is not the registry it claims to be');
  assert.deepEqual(c('duplicates collapse').stored.expanded, [keys[0]]);
  assert.deepEqual(c('out of order sorts').stored.expanded, keys.slice().sort(),
    'the result is not sorted, so two clients expanding the same categories store '
    + 'different blobs');
  for (const n of ['a script tag', 'an html attribute payload', 'a very long string',
    'a real key with different case', 'a real key with whitespace', 'a prototype key',
    'numbers and nulls', 'a nested object']) {
    assert.deepEqual(c(n).stored.expanded, [],
      n + ' survived the filter — this is the stored-XSS boundary and it kept a value the '
      + 'registry does not name');
  }
  assert.deepEqual(c('one real among many fake').stored.expanded, [keys[1]],
    'a real key beside fake ones was lost, or a fake one survived');
  for (const n of ['grouped missing', 'grouped is a string', 'grouped is null',
    'expanded missing', 'expanded is a string', 'expanded is an object',
    'expanded is null', 'an empty body']) {
    assert.equal(c(n).accepted, false, n + ' was accepted');
  }
  // Believability the other way: at least one case is accepted and at least one
  // refused, or every assertion above could pass against a stub.
  const acc = Object.values(cases).filter((x) => x.accepted).length;
  assert.ok(acc > 0 && acc < Object.keys(cases).length,
    'every case has the same verdict, so this corpus proves nothing');
}

const OUT = path.join(ROOT, 'testdata', 'navprefs-cases.json');
const text = JSON.stringify({ categoryKeys: keys, cases }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== text) {
    console.error('testdata/navprefs-cases.json is stale — run: node tools/navprefs-cases.js');
    process.exit(1);
  }
  console.log('navprefs-cases.json is current');
} else {
  fs.writeFileSync(OUT, text);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' bodies, '
    + keys.length + ' category keys)');
}
