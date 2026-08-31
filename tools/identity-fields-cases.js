'use strict';
/**
 * `Routers.updateIdentity`, RUN — not sliced, not read.
 *
 * ---- WHY THIS EXISTS ------------------------------------------------------
 *
 * The background pool learns what a router IS (model, serial, osVersion) from
 * its first `/system/resource` read and writes it back onto the record. The port
 * had no equivalent, so `internal/routers.Pool` took a nil `IdentityHook` and a
 * router added through this app would have kept an empty `model` column on the
 * Devices page forever.
 *
 * ---- IT IS A WRITE PATH, SO IT IS DRIVEN END TO END ------------------------
 *
 * `routers.js` needs only DATA_DIR, so this REQUIRES it against a throwaway
 * directory, seeds a routers.json, calls `updateIdentity` and reads back what
 * landed on disk. Nothing here reimplements the rule: a slice would have to
 * carry `loadAll`, `_writeFile` and the cipher-keep map with it, and the parts
 * most likely to differ are exactly the parts a slice would leave behind.
 *
 * ---- THE THREE RULES THAT ARE EASY TO GET WRONG ---------------------------
 *
 *  1. NON-STRINGS ARE SKIPPED, NOT CLEARED. `typeof val !== 'string'` continues
 *     past the field. A port treating a missing key as "" would blank a model
 *     the router simply did not report this time.
 *  2. AN EMPTY RESULT IS SKIPPED TOO. `if (clean && ...)` — trimming "   " to ""
 *     does not clear the stored value.
 *  3. NULL MEANS NO WRITE HAPPENED, and the caller relies on it: the audit event
 *     and the router-list broadcast are both gated on it. A port that always
 *     wrote would rewrite routers.json on every poll of every router and emit an
 *     audit event each time.
 *
 * The 64-cap is `String.prototype.slice`, which counts UTF-16 CODE UNITS. Go's
 * `s[:64]` counts BYTES, so a multi-byte model name is where the two part
 * company — hence the accented and CJK cases below.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/identity-fields-cases.js
 *   MIKRODASH_SRC=../MikroDash node tools/identity-fields-cases.js --check
 */

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', 'identity-cases.json');

const DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'mdid-'));
process.env.DATA_DIR = DIR;
const Routers = require(path.join(LIVE, 'src', 'routers.js'));

// The record as it sits on disk. `password: ''` so `_writeFile`'s cipher-keep
// path is the one exercised — this function must not disturb the credential,
// and a seeded plaintext would be re-encrypted and prove nothing about that.
const BASE = {
  id: 'rtr-1',
  label: 'Router One',
  host: '198.51.100.1',
  port: 8728,
  username: 'api',
  password: '',
  model: 'C53UiG5HPaxD2HPaxD',
  serial: 'HDX0ABCDEF1',
  osVersion: '7.23.1',
};

function run(identity, base) {
  fs.writeFileSync(path.join(DIR, 'routers.json'),
    JSON.stringify([base || BASE], null, 2));
  Routers.invalidateCache();
  const returned = Routers.updateIdentity('rtr-1', identity);
  Routers.invalidateCache();
  const onDisk = JSON.parse(fs.readFileSync(path.join(DIR, 'routers.json'), 'utf8'))[0];
  return {
    // `null` is the signal the caller gates its audit event and broadcast on, so
    // it is recorded as its own fact rather than inferred from the record.
    wrote: returned !== null,
    model: onDisk.model === undefined ? null : onDisk.model,
    serial: onDisk.serial === undefined ? null : onDisk.serial,
    osVersion: onDisk.osVersion === undefined ? null : onDisk.osVersion,
  };
}

const A65 = 'a'.repeat(65);
const A64 = 'a'.repeat(64);

const INPUTS = [
  ['all three change', { model: 'C53UiG5HPaxD2HPaxD-new', serial: 'HDX0ZZZZZZ9', osVersion: '7.24' }],
  ['one changes, two are identical', { model: 'C53UiG5HPaxD2HPaxD', serial: 'HDX0ABCDEF1', osVersion: '7.24' }],
  ['nothing changes at all', { model: 'C53UiG5HPaxD2HPaxD', serial: 'HDX0ABCDEF1', osVersion: '7.23.1' }],
  ['an empty identity object', {}],
  ['null', null],
  ['undefined', undefined],
  // RULE 1 — a non-string is SKIPPED, so the stored value survives.
  ['a number', { osVersion: 724 }],
  ['a null field', { model: null }],
  ['a boolean field', { serial: true }],
  ['an object field', { model: { name: 'x' } }],
  ['an array field', { model: ['x'] }],
  // RULE 2 — trimming to empty is not a clear either.
  ['an empty string', { model: '' }],
  ['whitespace only', { model: '   ' }],
  ['a tab and newline only', { model: '\t\n' }],
  // TRIMMING is real, and the trimmed form is what is compared.
  ['padded but IDENTICAL once trimmed', { model: '  C53UiG5HPaxD2HPaxD  ' }],
  ['padded and different', { model: '  RB5009UG  ' }],
  // THE 64-UNIT CAP.
  ['65 characters — one over the cap', { model: A65 }],
  ['64 characters — exactly the cap', { model: A64 }],
  // ...and where BYTES and CODE UNITS disagree. Each of these is under 64 code
  // units and OVER 64 bytes, so a Go port slicing bytes truncates and this does
  // not.
  ['40 accented characters — 80 bytes, 40 code units', { model: 'é'.repeat(40) }],
  ['40 CJK characters — 120 bytes, 40 code units', { model: '路'.repeat(40) }],
  // ...and one that is over the cap in BOTH, so the cap itself still fires on a
  // multi-byte string rather than only the byte/unit difference being tested.
  ['70 CJK characters — over the cap in units too', { model: '路'.repeat(70) }],
  // A record that has never carried identity at all: every field absent.
  ['a record with no identity fields yet', { model: 'RB5009UG', serial: 'HDX0NEW', osVersion: '7.24' },
    { id: 'rtr-1', label: 'Router One', host: '198.51.100.1', port: 8728, username: 'api', password: '' }],
];

const cases = INPUTS.map(([why, identity, base]) => ({
  why,
  identity: identity === undefined ? null : identity,
  identityWasUndefined: identity === undefined,
  base: base || null,
  ...run(identity, base),
}));

// ---- Believability ---------------------------------------------------------
const by = Object.fromEntries(cases.map((c) => [c.why, c]));
const need = (k) => {
  if (!by[k]) throw new Error(`a believability check names a case that is gone: ${k}`);
  return by[k];
};

if (!cases.some((c) => c.wrote)) throw new Error('nothing ever writes; the corpus proves nothing');
if (!cases.some((c) => !c.wrote)) throw new Error('everything writes, so the null return is untested');

// RULE 1 and RULE 2: the stored value SURVIVES, and no write happens.
for (const why of ['a number', 'a null field', 'a boolean field', 'an object field',
  'an array field', 'an empty string', 'whitespace only', 'a tab and newline only',
  'nothing changes at all', 'an empty identity object', 'null', 'undefined',
  'padded but IDENTICAL once trimmed']) {
  const c = need(why);
  if (c.wrote) throw new Error(`${why}: wrote, and nothing should have changed`);
  if (c.model !== BASE.model || c.serial !== BASE.serial || c.osVersion !== BASE.osVersion) {
    throw new Error(`${why}: the stored identity moved to ${JSON.stringify(c)}`);
  }
}

// TRIMMING actually applies on the write path too.
if (need('padded and different').model !== 'RB5009UG') {
  throw new Error(`the padded value was not trimmed: ${JSON.stringify(need('padded and different').model)}`);
}

// THE CAP fires, and the boundary is inclusive.
if (need('65 characters — one over the cap').model !== A64) {
  throw new Error('a 65-character model was not truncated to 64');
}
if (need('64 characters — exactly the cap').model !== A64) {
  throw new Error('a 64-character model was altered; the cap is inclusive');
}

// THE BYTE/UNIT DIFFERENCE must actually be observable, or the two multi-byte
// cases are decoration. Each must come back WHOLE despite exceeding 64 bytes.
for (const [why, n, ch] of [['40 accented characters — 80 bytes, 40 code units', 40, 'é'],
  ['40 CJK characters — 120 bytes, 40 code units', 40, '路']]) {
  const c = need(why);
  if (c.model !== ch.repeat(n)) {
    throw new Error(`${why}: came back as ${c.model.length} units, not ${n} — the live function `
      + 'truncated it, so this case no longer separates code units from bytes');
  }
  if (Buffer.byteLength(c.model, 'utf8') <= 64) {
    throw new Error(`${why}: only ${Buffer.byteLength(c.model, 'utf8')} bytes, so a byte-slicing `
      + 'port would pass it and the case tests nothing');
  }
}
// ...and the cap still fires on a multi-byte string.
if (need('70 CJK characters — over the cap in units too').model !== '路'.repeat(64)) {
  throw new Error('the cap did not fire on a 70-character CJK model');
}

// A record with no identity yet must gain all three.
{
  const c = need('a record with no identity fields yet');
  if (!c.wrote || c.model !== 'RB5009UG' || c.serial !== 'HDX0NEW' || c.osVersion !== '7.24') {
    throw new Error(`a bare record did not gain its identity: ${JSON.stringify(c)}`);
  }
}

fs.rmSync(DIR, { recursive: true, force: true });

const json = JSON.stringify(
  { generated_from: 'src/routers.js updateIdentity (executed)', base: BASE, cases }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('STALE: testdata/identity-cases.json - re-run tools/identity-fields-cases.js');
    process.exit(1);
  }
  console.log(`identity-cases: up to date (${cases.length} cases)`);
} else {
  fs.writeFileSync(OUT, json);
  console.log(`wrote ${OUT} (${cases.length} cases, `
    + `${cases.filter((c) => c.wrote).length} writing)`);
}
