'use strict';
/**
 * `_parseName` — the name and description a GROUP or a ROLE is allowed to have.
 *
 * ── THE ORIGINAL HAS THIS LOGIC TWICE, AND SO DOES THIS PORT ────────────────
 *
 * `_parseName` is byte-identical to the name/description half of
 * `_parseSiteBody`. The obvious move is one shared parser — and it is the wrong
 * one here. Two corpora lifted from two originals catch upstream changing ONE of
 * them; a single shared function would silently apply the change to both, or to
 * neither, and the gate would stay green either way.
 *
 * So this duplicates `internal/sites/parse.go`'s first half on purpose, and says
 * so at both ends.
 *
 * ── ABSENT VERSUS NULL VERSUS SET, AGAIN ────────────────────────────────────
 *
 *   absent   not written. On a CREATE the name is still required.
 *   null     for a description, an explicit clear.
 *   set      validated, trimmed, bounded.
 *
 * ── NOTHING HERE IS REAL ────────────────────────────────────────────────────
 *
 *   node tools/name-cases.js            write testdata/name-cases.json
 *   node tools/name-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const OUT = process.env.NAME_CASES_OUT || path.join(ROOT, 'testdata', 'name-cases.json');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const src = fs.readFileSync(path.join(LIVE, 'src', 'index.js'), 'utf8');

// ---- THE SLICE -----------------------------------------------------------
const OPEN = 'function _parseName(body, { partial } = {}) {';
{
  const n = src.split(OPEN).length - 1;
  assert.equal(n, 1, 'the anchor is ambiguous (' + n + ' matches)');
}
const from = src.indexOf(OPEN);
// THE BODY'S BRACE, not the first one after the name: the signature destructures
// `{ partial } = {}`, so `indexOf('(')` lands in the PARAMETER LIST and the depth
// walk closes on it. Same trap as tools/site-body-cases.js.
const open = from + OPEN.length - 1;
if (src[open] !== '{') throw new Error('the anchor no longer ends at the body brace');
let depth = 0, to = -1;
for (let i = open; i < src.length; i++) {
  if (src[i] === '{') depth++;
  else if (src[i] === '}') { depth--; if (!depth) { to = i + 1; break; } }
}
assert.ok(to > from, 'unbalanced body for _parseName');
const body = src.slice(from, to);

for (const marker of ['partial', 'out.name', 'out.description', '256', '64']) {
  assert.ok(body.includes(marker),
    'the lifted function has no ' + marker + ' -- the slice is short, and this gate would '
    + 'then check the port against less than the routes validate');
}
// It must NOT have picked up the place handling: that belongs to _parseSiteBody,
// and a slice that reached it would be comparing the wrong function.
assert.ok(!body.includes('GeoPlace'),
  'the slice reached the PLACE handling, so it is not _parseName');

const ctx = { String, module: { exports: {} } };
vm.createContext(ctx);
vm.runInContext(body + '\nmodule.exports = _parseName;', ctx);
const parseName = ctx.module.exports;

// ---- THE CASES -----------------------------------------------------------
const CASES = {
  createMinimal: { body: { name: 'Ops' }, partial: false },
  createTrimsTheName: { body: { name: '   Ops   ' }, partial: false },
  createRejectsAnAbsentName: { body: {}, partial: false },
  createRejectsAnEmptyName: { body: { name: '' }, partial: false },
  createRejectsWhitespaceOnly: { body: { name: '   ' }, partial: false },
  createAcceptsA64CharName: { body: { name: 'x'.repeat(64) }, partial: false },
  createRejectsA65CharName: { body: { name: 'x'.repeat(65) }, partial: false },
  createAcceptsAPadded64: { body: { name: '  ' + 'x'.repeat(64) + '  ' }, partial: false },
  createCoercesANumericName: { body: { name: 7 }, partial: false },

  // ── THE NAME ON AN EDIT ─────────────────────────────────────────────────
  editWithoutAName: { body: { description: 'x' }, partial: true },
  editRejectsAnEmptyName: { body: { name: '' }, partial: true },
  // A NULL NAME IS NOW REFUSED. It used to become the string "null" -- the
  // check was the strict `=== undefined`, so a null slipped past and
  // `String(null)` named the group. Filed upstream as ToDo.md 6 and FIXED there
  // on 2026-08-27, so this case records a refusal rather than a quirk.
  aNullNameIsRefused: { body: { name: null }, partial: true },

  // ── DESCRIPTION: absent / null / empty / set ────────────────────────────
  descriptionAbsent: { body: { name: 'D' }, partial: false },
  descriptionNull: { body: { name: 'D', description: null }, partial: false },
  descriptionEmpty: { body: { name: 'D', description: '' }, partial: false },
  descriptionWhitespaceOnly: { body: { name: 'D', description: '   ' }, partial: false },
  descriptionSet: { body: { name: 'D', description: '  the ops team  ' }, partial: false },
  descriptionAt256: { body: { name: 'D', description: 'y'.repeat(256) }, partial: false },
  descriptionAt257: { body: { name: 'D', description: 'y'.repeat(257) }, partial: false },
  descriptionPadded256: { body: { name: 'D', description: ' ' + 'y'.repeat(256) + ' ' },
    partial: false },

  // Unknown keys are DROPPED: the parser builds a fresh object, so a caller
  // cannot set `id` or `created_at` through it.
  unknownKeysAreDropped: {
    body: { name: 'D', id: 'forged', created_at: 1, builtin: 1 }, partial: false },

  noBodyOnCreate: { body: null, partial: false },
  noBodyOnEdit: { body: null, partial: true },
};

const cases = {};
for (const [name, c] of Object.entries(CASES)) {
  const got = parseName(c.body, { partial: c.partial });
  cases[name] = {
    body: c.body, partial: c.partial,
    error: got.error === undefined ? null : got.error,
    value: got.error === undefined ? got.value : null,
  };
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  const v = (k) => cases[k].value;
  const e = (k) => cases[k].error;

  assert.equal(v('createTrimsTheName').name, 'Ops');
  assert.ok(e('createRejectsAnAbsentName'), 'a create with no name was accepted');
  assert.ok(e('createRejectsAnEmptyName') && e('createRejectsWhitespaceOnly'));
  assert.ok(!e('createAcceptsA64CharName') && e('createRejectsA65CharName'),
    'the name boundary is not at 64');
  assert.ok(!e('createAcceptsAPadded64'), 'a padded 64-character name was refused');
  assert.equal(v('createCoercesANumericName').name, '7');

  assert.ok(!e('editWithoutAName'), 'an edit with no name was refused');
  assert.ok(!('name' in v('editWithoutAName')),
    'an absent name was written on an edit, which would blank the group');
  assert.ok(e('editRejectsAnEmptyName'));
  assert.ok(e('aNullNameIsRefused'),
    'a null name was accepted -- it used to become the string "null" and was fixed upstream '
    + 'on 2026-08-27; if that fix was reverted the port must be reread rather than this '
    + 'expectation updated');

  assert.ok(!('description' in v('descriptionAbsent')),
    'an absent description was written, so a rename would clear it');
  assert.equal(v('descriptionNull').description, null);
  assert.equal(v('descriptionEmpty').description, null);
  assert.equal(v('descriptionSet').description, 'the ops team');
  assert.ok(!e('descriptionAt256') && e('descriptionAt257'),
    'the description boundary is not at 256');
  assert.ok(!e('descriptionPadded256'), 'a padded 256-character description was refused');

  for (const k of ['id', 'created_at', 'builtin']) {
    assert.ok(!(k in v('unknownKeysAreDropped')), k + ' reached the writer from the body');
  }
  assert.ok(e('noBodyOnCreate') && !e('noBodyOnEdit'));

  // The corpus must contain BOTH outcomes, or a port that always refused (or
  // always accepted) would satisfy it.
  const outcomes = Object.values(cases).map((c) => !!c.error);
  assert.ok(outcomes.includes(true) && outcomes.includes(false),
    'every case has the same outcome, so this corpus proves nothing');
}

const json = JSON.stringify({
  note: 'Generated by tools/name-cases.js from the LIVE src/index.js. Do not edit.',
  cases,
}, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('name-cases.json is STALE -- re-run without --check');
    process.exit(1);
  }
  console.log('name-cases.json is current (' + Object.keys(cases).length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, json);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' cases)');
}
