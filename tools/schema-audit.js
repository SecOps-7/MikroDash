'use strict';
/**
 * Does every test fixture in this repo declare the schema the app actually has?
 *
 * ── WRITTEN AFTER THREE BUGS IN THREE TICKS, ALL THE SAME SHAPE ─────────────
 *
 * A Go test creates its own tables. If that DDL is looser than the real one, the
 * code under test is checked against a database that cannot exist, and the test
 * passes while the code cannot read a real row. It happened three times running
 * on 2026-08-26:
 *
 *   - `sites.description` is NULLABLE; the fixture said `NOT NULL DEFAULT ''`,
 *     so `GetSite` scanning into a Go `string` passed and would have answered
 *     500 for any site without a description — which is most of them.
 *   - `grants.id` is `TEXT PRIMARY KEY` holding a uuid; EIGHT fixtures said
 *     `INTEGER PRIMARY KEY AUTOINCREMENT`, so `GrantRow.ID int64` scanned
 *     happily and could not read a single real grant.
 *   - the same eight omitted `created_at`, which `ListGrants` selects.
 *
 * Each was found by tripping over it. This audit stops waiting.
 *
 * ── THE SCHEMA IS REPLAYED, NOT PARSED ──────────────────────────────────────
 *
 * `src/db.js` cannot be required here — it pulls in better-sqlite3. So the
 * `MIGRATIONS` array is sliced out and EVALUATED, and each `up(db)` is run
 * against a real in-memory SQLite (node:sqlite, built in since Node 22). The
 * answer therefore comes from SQLite itself via `PRAGMA table_info`, not from a
 * regex over CREATE TABLE text — which would have to understand `ALTER TABLE …
 * ADD COLUMN` and the `_new` + RENAME rebuild that migration 12 uses, and would
 * be wrong in exactly the cases that matter.
 *
 * ── WHAT IS COMPARED, AND WHAT IS DELIBERATELY NOT ──────────────────────────
 *
 * For every table a fixture declares that the app also has: the COLUMN SET, each
 * column's declared TYPE, and its NOT NULL flag. Those are the three that decide
 * whether a Go scan works.
 *
 * FOREIGN KEYS ARE COMPARED TOO, and that was an omission until 2026-08-26.
 * They were lumped in with the constraints below on the argument that a fixture
 * "legitimately omits what constrains writes the port does not make" — which is
 * wrong wherever the port RELIES on a constraint instead of re-checking it.
 * `internal/db/rolewrite.go` does exactly that: `ON DELETE RESTRICT` on
 * `grants.role_id` is what refuses deleting a role a grant still holds, and the
 * port deliberately does not duplicate the check. A fixture without the key
 * deleted the role happily, and the test written to pin the refusal passed
 * against a port that had none. Found by writing that test, not by this audit.
 *
 * NOT compared: indexes, CHECK constraints, UNIQUE constraints and DEFAULTs. A
 * fixture legitimately omits those — they constrain writes the port does not
 * make — and requiring them would turn this into noise nobody reads. A fixture
 * table the app does NOT have is skipped and REPORTED, since several tests
 * invent scratch tables on purpose.
 *
 *   node tools/schema-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { DatabaseSync } = require('node:sqlite');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('schema-audit');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

// ── replay the live migrations ──────────────────────────────────────────────
const src = LIFT.liveSource(ROOT, path.join('src', 'db.js'));
const OPEN = 'const MIGRATIONS = [';
// GUARDED: both ask whether the live SOURCE still holds the anchor.
if (LIFT.hasReference(ROOT)) {
  assert.equal(src.split(OPEN).length - 1, 1, 'the MIGRATIONS anchor is ambiguous');
}
const from = src.indexOf(OPEN);
const end = src.indexOf('\n];', from);
if (LIFT.hasReference(ROOT)) assert.ok(end > from, 'MIGRATIONS is never closed');
// FROZEN — the migration list is EXECUTED below to build the live schema, so the
// text is what must survive. The `>= 12` assertion beneath then validates the
// RECORDING, which is exactly what it was written to catch.
const body = G.value('the live MIGRATIONS', () => src.slice(from, end + '\n];'.length));

const ctx = { module: { exports: {} }, JSON, Date, String, Number, Math, Object, Array };
vm.createContext(ctx);
vm.runInContext(body + '\nmodule.exports = MIGRATIONS;', ctx);
const migrations = ctx.module.exports;
assert.ok(Array.isArray(migrations) && migrations.length >= 12,
  'only ' + (migrations || []).length + ' migrations were lifted -- the slice is short, and a '
  + 'partial schema would report every later column as a fixture inventing things');

const live = new DatabaseSync(':memory:');
for (const m of migrations) {
  // The shim is only what an `up` uses: `exec` and `prepare().run()`.
  m.up({
    exec: (sql) => live.exec(sql),
    prepare: (sql) => {
      const st = live.prepare(sql);
      return { run: (...a) => st.run(...a), get: (...a) => st.get(...a), all: (...a) => st.all(...a) };
    },
  });
}

/** table -> { col -> {type, notnull} }, straight out of SQLite. */
function schemaOf(db) {
  const out = {};
  for (const row of db.prepare(
    `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).all()) {
    const cols = {};
    for (const c of db.prepare(`PRAGMA table_info(${row.name})`).all()) {
      cols[c.name] = { type: String(c.type || '').toUpperCase(), notnull: !!c.notnull, fk: null };
    }
    // `PRAGMA foreign_key_list` answers per TABLE, one row per key. What matters
    // downstream is the TARGET and the ON DELETE action -- RESTRICT versus
    // CASCADE versus nothing are three different behaviours the port depends on.
    for (const k of db.prepare(`PRAGMA foreign_key_list(${row.name})`).all()) {
      if (cols[k.from]) cols[k.from].fk = k.table + '.' + k.to + ' ON DELETE ' + k.on_delete;
    }
    out[row.name] = cols;
  }
  return out;
}

const LIVE_SCHEMA = schemaOf(live);
assert.ok(LIVE_SCHEMA.grants && LIVE_SCHEMA.grants.id,
  'the replayed schema has no grants.id -- the replay produced nothing useful');
assert.equal(LIVE_SCHEMA.grants.id.type, 'TEXT',
  'grants.id replayed as ' + LIVE_SCHEMA.grants.id.type + ', not TEXT. This audit was written '
  + 'because that column is a TEXT uuid; if the live schema really changed, the port needs '
  + 'rereading rather than this assertion relaxing');

// ── every fixture's DDL ─────────────────────────────────────────────────────
function goTestFiles(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) goTestFiles(p, out);
    else if (e.name.endsWith('_test.go')) out.push(p);
  }
  return out;
}

/** Run one fixture's CREATE TABLE against a scratch database and read it back. */
function fixtureSchema(ddl) {
  const db = new DatabaseSync(':memory:');
  try {
    db.exec(ddl);
  } catch (e) {
    return { error: e.message };
  }
  return { schema: schemaOf(db) };
}

// ── the keys this port DEPENDS ON rather than re-checking ───────────────────
//
// Adding one here is a statement that Go code would be wrong without it.
const RELIED_ON = {
  'grants.role_id':
    'internal/db/rolewrite.go DeleteRole does NOT re-check whether a grant still holds the '
    + 'role -- ON DELETE RESTRICT refuses it, and CountGrantsForRole exists so the caller can '
    + 'say how many. A second check in Go could disagree with the engine under concurrency',
};

const CREATE = /CREATE TABLE (?:IF NOT EXISTS )?([a-z_]+)\s*\(/gi;

const problems = [];
const skipped = [];
let compared = 0;

for (const file of goTestFiles(path.join(ROOT, 'internal'))) {
  const text = fs.readFileSync(file, 'utf8');
  let m;
  CREATE.lastIndex = 0;
  while ((m = CREATE.exec(text))) {
    const table = m[1];
    if (!LIVE_SCHEMA[table]) {
      skipped.push(path.relative(ROOT, file) + ' -> ' + table);
      continue;
    }
    // Slice this one statement: from CREATE to the matching close paren.
    let depth = 0, i = text.indexOf('(', m.index), j = -1;
    for (; i < text.length; i++) {
      if (text[i] === '(') depth++;
      else if (text[i] === ')') { depth--; if (!depth) { j = i + 1; break; } }
    }
    if (j < 0) {
      problems.push(path.relative(ROOT, file) + ': the CREATE TABLE for ' + table
        + ' is not closed -- this audit could not read it, which is not the same as it being fine');
      continue;
    }
    const ddl = text.slice(m.index, j) + ';';
    const got = fixtureSchema(ddl);
    if (got.error) {
      problems.push(path.relative(ROOT, file) + ' -> ' + table
        + ': the fixture DDL does not parse in isolation (' + got.error + ')');
      continue;
    }
    compared++;

    const want = LIVE_SCHEMA[table];
    const have = got.schema[table] || {};
    const where = path.relative(ROOT, file) + ' -> ' + table;

    for (const col of Object.keys(have)) {
      if (!want[col]) {
        problems.push(where + '.' + col + ' does not exist in the real table');
        continue;
      }
      if (have[col].type !== want[col].type) {
        problems.push(where + '.' + col + ' is ' + have[col].type + ' here and '
          + want[col].type + ' on disk -- a Go scan that works against one may not work '
          + 'against the other');
      }
      // ONE DIRECTION ONLY. A fixture may declare NOT NULL where the app does
      // not (it just cannot store a NULL, which is a narrower test, not a wrong
      // one)... except that is EXACTLY the sites.description bug, so it IS
      // reported. The reverse -- nullable here, NOT NULL on disk -- is harmless.
      // ── A MISSING KEY IS A NOTE, EXCEPT WHERE THE PORT LEANS ON IT ──────
      //
      // Most fixtures omit foreign keys harmlessly: the port deletes explicitly
      // rather than relying on a cascade (`groupwrite.go` removes a group's
      // grants itself, and says why). Requiring every key would be noise, and
      // would break fixtures whose insert order the keys forbid.
      //
      // RELIED_ON is the exception — keys the port deliberately does NOT
      // re-check, so a fixture without them cannot exercise the behaviour at
      // all. There is one today, and finding it cost a failing test.
      if (want[col].fk && have[col].fk !== want[col].fk) {
        const key = table + '.' + col;
        if (RELIED_ON[key]) {
          // A HARD FAILURE, promoted from a note on 2026-08-26 once every
          // fixture that declares `grants` was brought up to carry the key.
          // The port does not re-check this constraint, so a fixture without it
          // cannot exercise the refusal at all — and the test written to pin
          // that behaviour passed against a port that had none, which is how the
          // whole omission was found.
          problems.push(where + '.' + col + ' has foreign key ' + (have[col].fk || 'NONE')
            + ' and the app has ' + want[col].fk + '. THE PORT RELIES ON THIS KEY: '
            + RELIED_ON[key]);
        } else {
          // Everything else is a NOTE. Most fixtures omit keys harmlessly: the
          // port deletes explicitly rather than leaning on a cascade
          // (`groupwrite.go` removes a group's grants itself and says why).
          skipped.push(where + '.' + col + ' omits ' + want[col].fk
            + ' (the port does not rely on this key)');
        }
      }
      if (have[col].notnull && !want[col].notnull) {
        problems.push(where + '.' + col + ' is NOT NULL here and NULLABLE on disk -- this '
          + 'fixture cannot produce the row that breaks a scan into a non-pointer Go type. '
          + 'That is the sites.description bug, exactly');
      }
    }
    // A column the real table has and the fixture omits is only a problem if
    // something SELECTs it -- which this audit cannot know. Reported as a note
    // rather than a failure, because listing every unused column would bury the
    // three that matter.
    const missing = Object.keys(want).filter((c) => !have[c]);
    if (missing.length) {
      skipped.push(where + ' omits ' + missing.join(', ')
        + ' (fine unless a query selects them)');
    }
  }
}

// ── AND THE RECORD MUST NOT OUTLIVE THE KEY IT DESCRIBES ───────────────────
//
// `RELIED_ON` is a statement that Go code would be WRONG without a constraint —
// `DeleteRole` does not re-check whether a grant still holds the role, because
// ON DELETE RESTRICT refuses it. If the live schema ever dropped that key, this
// entry would go on excusing fixtures for omitting a constraint that no longer
// exists, and the port's decision not to re-check would quietly become a real
// defect: roles deletable out from under their grants, with nothing failing.
//
// Nothing checked that until 2026-08-27. The entry was consulted and never
// verified — the same asymmetry found in `emit-audit` the same day, where
// `ALLOWED` had an orphan check and `UNPORTED` did not. The generalisable rule:
// an allowance needs BOTH directions asked — is the thing still true here, and
// is it still true THERE.
for (const key of Object.keys(RELIED_ON)) {
  const [table, col] = key.split('.');
  const want = LIVE_SCHEMA[table];
  if (!want) {
    problems.push('RELIED_ON names ' + key + ' but the live schema has no table ' + table
      + ' — the entry describes nothing, and the Go code that leans on it needs rereading');
  } else if (!want[col]) {
    problems.push('RELIED_ON names ' + key + ' but ' + table + ' has no column ' + col
      + ' — the entry describes nothing, and the Go code that leans on it needs rereading');
  } else if (!want[col].fk) {
    problems.push('RELIED_ON names ' + key + ' as a foreign key the port DOES NOT RE-CHECK, and '
      + 'the live schema no longer declares one. Whatever the engine was refusing, nothing '
      + 'refuses it now: ' + RELIED_ON[key]);
  }
}

if (compared === 0) {
  console.error('schema-audit: NOTHING was compared -- no fixture declared a real table, which '
    + 'means the matcher has drifted and a clean run here proves nothing');
  process.exit(1);
}

if (problems.length) {
  problems.forEach((p) => console.error('  ✗ ' + p));
  console.error('\nschema-audit: ' + problems.length + ' fixture(s) disagree with the live schema '
    + '(' + compared + ' compared)');
  process.exit(1);
}
console.log('schema-audit: ' + compared + ' fixture table(s) match the live schema; '
  + skipped.length + ' note(s)');
