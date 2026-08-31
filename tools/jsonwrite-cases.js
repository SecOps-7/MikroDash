'use strict';
/**
 * THE EXACT BYTES `JSON.stringify(value, null, 2)` produces, so the Go writers
 * can be held to them.
 *
 * ---- WHY THIS EXISTS ------------------------------------------------------
 *
 * `internal/store`'s premise is that it must read what Node wrote. Once it also
 * WRITES those files — `routers.json` already does, `users.json` at cutover —
 * the premise runs the other way, and `json.MarshalIndent` is NOT
 * `JSON.stringify(x, null, 2)`. Three differences, all found by running both:
 *
 *   & < >    Go escapes them to & < >. JavaScript does not.
 *   keys     Go sorts a map alphabetically. JavaScript keeps insertion order.
 *   end      Go's Encoder appends a newline. JSON.stringify does not, and
 *            neither does any of the three real files on disk.
 *
 * The first and third were live defects in this port and are fixed. The second
 * is recorded as a KNOWN DIFFERENCE and asserted to still exist, so the note
 * describing it cannot outlive it.
 *
 * ---- A GENERATED CORPUS, NOT A TRANSCRIBED ONE ----------------------------
 *
 * The expected bytes come from RUNNING `JSON.stringify` rather than from anyone
 * typing what they believe it produces. That distinction is why the escaping
 * defect survived: the Go output looked like JSON, parsed as JSON, and
 * round-tripped through Node correctly. Only a byte comparison shows that a site
 * called "A & B" reached disk as "A & B".
 *
 *   node tools/jsonwrite-cases.js          # write
 *   node tools/jsonwrite-cases.js --check  # fail if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const OUT = path.join(ROOT, 'testdata', 'jsonwrite-cases.json');

// Each case names WHY it is here. `keyOrderDiffers` is set by MEASUREMENT below,
// never by hand, so a case added later is classified without anyone remembering.
const CASES = [
  ['an empty array, which is what users.json holds on a fresh install', []],
  ['one record whose keys are already alphabetical, so Go agrees',
    [{ a: 1, b: 2, c: 3 }]],
  ['THE ESCAPING: an ampersand in a label',
    [{ label: 'A & B' }]],
  ['angle brackets, which Go turns into escape sequences',
    [{ label: '<site>', note: 'a > b' }]],
  ['all three at once, in the shape a real site name takes',
    [{ id: 'r1', label: 'Ops & Eng <HQ>' }]],
  ['a quote and a backslash, which BOTH sides escape and must escape alike',
    [{ label: 'a "quoted" \\ path' }]],
  ['a newline and a tab inside a value',
    [{ note: 'line one\nline two\tend' }]],
  ['non-ASCII stays raw on both sides - neither escapes it',
    [{ label: 'Café Münster', city: 'Kraków' }]],
  ['an emoji, which is a surrogate pair and must not be escaped',
    [{ label: 'ops 🚀' }]],
  ['a nested object, so the indent is checked at depth',
    [{ backup: { enabled: true, keepCount: 7 } }]],
  ['a nested array of strings',
    [{ id: 'r1', siteIds: ['site-a', 'site-b'] }]],
  ['null, true, false and a zero - the falsy values, whose spellings must match',
    [{ a: null, b: true, c: false, d: 0 }]],
  ['a float and a large integer, where Go and JS number formatting can diverge',
    [{ big: 1000000, port: 8729, ratio: 0.5 }]],
  ['an empty string and an empty object',
    [{ backup: {}, label: '' }]],
  ['two records, so the separator between array elements is checked',
    [{ id: 'r1' }, { id: 'r2' }]],
  // THE KNOWN DIFFERENCE. Keys deliberately out of alphabetical order, which is
  // how every real record is shaped: `id` before `label` before `host`.
  ['KEY ORDER: a realistic record, whose keys are not alphabetical',
    [{ id: 'r1', label: 'One', host: '198.51.100.1', port: 8729, tls: true }]],
  ['KEY ORDER: reverse-alphabetical, the clearest possible case',
    [{ z: 1, y: 2, x: 3 }]],
];

// Sort every object's keys at every depth, the way Go's map encoder does.
function sortDeep(v) {
  if (Array.isArray(v)) return v.map(sortDeep);
  if (v && typeof v === 'object') {
    const o = {};
    for (const k of Object.keys(v).sort()) o[k] = sortDeep(v[k]);
    return o;
  }
  return v;
}

const out = CASES.map(([why, value]) => {
  const node = JSON.stringify(value, null, 2);
  // Whether Go's sorted-key encoding can possibly match: only when every object's
  // keys are ALREADY ascending at every depth.
  const goSorted = JSON.stringify(sortDeep(value), null, 2);
  return {
    why,
    // The value, so the Go test builds the same input rather than parsing the
    // expectation and comparing it with itself.
    value,
    // The EXPECTED BYTES. This is the whole point of the file.
    node,
    goSorted,
    keyOrderDiffers: node !== goSorted,
  };
});

// ---- Believability ---------------------------------------------------------
// A corpus of expectations nobody can fail is not a corpus. Every check below is
// about what NODE produced.
const byWhy = Object.fromEntries(out.map((c) => [c.why, c]));
const need = (k) => {
  if (!byWhy[k]) throw new Error(`a believability check names a case that is gone: ${k}`);
  return byWhy[k];
};

// THE ESCAPING, asserted as an actual difference. If Node ever started escaping
// these the Go fix would become wrong, and this must fail rather than quietly
// agree with it.
for (const k of ['THE ESCAPING: an ampersand in a label',
  'angle brackets, which Go turns into escape sequences',
  'all three at once, in the shape a real site name takes']) {
  if (/\\u00(26|3c|3e)/i.test(need(k).node)) {
    throw new Error(`${k}: Node escaped an HTML character. The port sets SetEscapeHTML(false) `
      + 'on the strength of it NOT doing so - re-read internal/store/jsonwrite.go.');
  }
}
if (!need('THE ESCAPING: an ampersand in a label').node.includes('A & B')) {
  throw new Error('the ampersand case no longer contains a raw ampersand');
}
// NO TRAILING NEWLINE, on every case. This is the difference that was wrong in
// three writers at once.
for (const c of out) {
  if (c.node.endsWith('\n')) {
    throw new Error(`${c.why}: JSON.stringify produced a trailing newline, which it does not`);
  }
}
// THE KEY-ORDER GAP STILL EXISTS. If this stops being true the port gained an
// ordered encoder or the cases lost their unsorted keys; either way the note in
// jsonwrite.go must be re-read rather than left standing.
const differing = out.filter((c) => c.keyOrderDiffers);
if (differing.length < 2) {
  throw new Error('no case has out-of-order keys any more, so the known key-order difference '
    + 'is no longer demonstrated - see internal/store/jsonwrite.go');
}
// And the cases that should AGREE do agree, or the corpus proves only that
// everything differs.
if (need('one record whose keys are already alphabetical, so Go agrees').keyOrderDiffers) {
  throw new Error('an already-sorted record is reported as differing; sortDeep is wrong');
}
if (out.filter((c) => !c.keyOrderDiffers).length < 10) {
  throw new Error('almost every case differs on key order; the corpus cannot show what matches');
}
// Nothing here may carry a credential: these are shapes, not data.
const blob = JSON.stringify(out).toLowerCase();
for (const bad of ['passwordhash', 'password', 'secret', '-----begin']) {
  if (blob.includes(bad)) throw new Error(`corpus contains ${bad}`);
}

const json = JSON.stringify({
  generated_from: 'JSON.stringify(value, null, 2)',
  cases: out,
}, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('STALE: testdata/jsonwrite-cases.json - re-run tools/jsonwrite-cases.js');
    process.exit(1);
  }
  console.log(`jsonwrite-cases: up to date (${out.length} cases, ${differing.length} differ on key order)`);
} else {
  fs.writeFileSync(OUT, json);
  console.log(`wrote ${OUT} (${out.length} cases, ${differing.length} differ on key order)`);
}
