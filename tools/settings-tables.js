'use strict';
/**
 * The four tables `settings.js` merges with, captured from the live module.
 *
 * ── GENERATED, NOT RETYPED, AND THAT IS A PROJECT RULE ─────────────────────
 *
 * CLAUDE.md: "the Node DEFAULTS carry ~120 keys that change as pages are added,
 * and a struct here would be a mirror of exactly the kind this port is meant to
 * stop creating. Typed accessors come later, generated from one definition."
 * This is that one definition. A hand-copied table would drift the first time a
 * page was added, and the drift would be invisible — a missing default reads as
 * "the operator never set it" rather than as a bug.
 *
 *   defaults        113 keys of string, number and boolean
 *   encrypted       the six fields sealed on disk
 *   envMap          field -> { env, kind }, env winning over settings.json
 *   pollBounds      field -> [lo, hi], the clamp that stops a hand-edited file
 *                   producing a sub-minimum timer delay
 *
 * ── THE PARSERS ARE PROBED, NOT READ ───────────────────────────────────────
 *
 * `ENV_MAP` holds functions, which do not serialise. Their SOURCE could be
 * matched with a regex, but that breaks the moment one is reformatted. Instead
 * each is called with values whose answers identify it: an integer parser turns
 * '7' into 7, a boolean one turns 'TRUE' into true and anything else into false,
 * and the identity parser gives back what it was handed. A parser that matches
 * none of the three is an ERROR rather than a guess — see below.
 *
 *   node tools/settings-tables.js            write the tables
 *   node tools/settings-tables.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'internal', 'store', 'settings_tables.json');

// Load with a throwaway DATA_DIR so nothing touches the operator's /data. Only
// the module-scope tables are read; `load()` is never called.
const os = require('node:os');
process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'mdtables-'));
const S = require(path.join(LIVE, 'src', 'settings.js'));

/** Identify a parser by what it does, not by how it is written. */
function parserKind(fn, field) {
  const seven = fn('7');
  if (seven === 7) return 'int';
  const t = fn('TRUE'), f = fn('nope');
  if (t === true && f === false) return 'bool';
  if (fn('abc') === 'abc') return 'string';
  throw new Error('the env parser for ' + field + ' matches no known kind — ' +
    'probe results: parse("7")=' + JSON.stringify(seven) +
    ' parse("TRUE")=' + JSON.stringify(t) + '. Add the kind here AND in the Go ' +
    'side rather than letting it fall through to a default.');
}

function main() {
  const check = process.argv.includes('--check');

  // ENV_MAP is not exported, so it is re-read from the source the same way the
  // api-surface generator reads the live tree: the table is a literal, and its
  // entries are `field: ['ENV_VAR', parser]`. The PARSER still comes from the
  // live module — see below — so only the field/var pairing is textual.
  const src = fs.readFileSync(path.join(LIVE, 'src', 'settings.js'), 'utf8');
  const block = src.slice(src.indexOf('const ENV_MAP = {'));
  const envMap = {};
  for (const m of block.slice(0, block.indexOf('\n};')).matchAll(
    /^\s*(\w+):\s*\[\s*'([A-Z0-9_]+)'\s*,\s*(.+?)\s*\],?\s*$/gm)) {
    const [, field, envVar, parserSrc] = m;
    // Evaluate the parser expression in isolation to probe it. It is a one-line
    // arrow from a file this repo already trusts and reads wholesale.
    // eslint-disable-next-line no-new-func
    const fn = new Function('return (' + parserSrc + ')')();
    envMap[field] = { env: envVar, kind: parserKind(fn, field) };
  }
  // ── COMPLETENESS: EVERY ENV VAR settings.js READS ───────────────────────
  //
  // ENV_MAP is extracted by PATTERN, and a pattern cannot report an entry whose
  // shape it does not match. The settings FORM map lost thirteen fields to
  // exactly that, so the same data-driven check applies: every `process.env.X`
  // the module reads must be in the captured map, or listed below with a reason.
  //
  // An env var that IS an override and is missed here silently stops overriding
  // — the operator edits .env, restarts, and the value does not change, with
  // nothing to say why.
  const NOT_AN_OVERRIDE = {
    ROUTER_PASS: 'handled outside ENV_MAP: env wins, and an absent password is normalised to ""',
    DATA_DIR: 'where the file lives, not a setting in it',
    ROS_DEBUG: 'written BY save() to keep the library in step; never read as an override',
    DATA_SECRET: 'the encryption key material, not a setting — internal/store reads the same ' +
                 'source and derives the same scrypt key from it',
  };
  // FOUR SPELLINGS OF ONE READ. This matched dot access and single-quoted
  // brackets; a probe found that `process.env["X"]` and `process.env?.X` were
  // invisible to it, which is the dangerous direction — an override written that
  // way would never be required to appear in ENV_MAP, and would silently stop
  // overriding. Destructuring is refused separately below rather than parsed,
  // because a `const { A, B } = process.env` binds names this cannot attribute.
  const envRead = new Set(
    // The `\.?` before the bracket is for `process.env?.["X"]`, where optional
    // chaining puts a dot in front of the subscript. Four spellings became five
    // the moment I probed for them, which is the argument for probing.
    [...src.matchAll(/process\.env\s*\??\s*(?:\.([A-Z][A-Z0-9_]*)|\.?\[\s*['"]([A-Z][A-Z0-9_]*)['"]\s*\])/g)]
      .map((m) => m[1] || m[2]),
  );
  const destructured = src.match(/(?:const|let|var)\s*\{[^}]*\}\s*=\s*process\.env/);
  if (destructured) {
    console.error('settings.js destructures process.env:\n  ' + destructured[0].trim() +
      '\nThis check reads env access by name and cannot attribute a destructured binding. ' +
      'Extend it before trusting the result.\n');
    process.exit(1);
  }
  const captured = new Set(Object.values(envMap).map((e) => e.env));
  const missed = [...envRead].filter((v) => !captured.has(v) && !(v in NOT_AN_OVERRIDE));
  if (missed.length) {
    throw new Error('settings.js reads these environment variables and ENV_MAP extraction ' +
      'did not capture them:\n  ' + missed.join('\n  ') +
      '\nAdd the shape, or list each in NOT_AN_OVERRIDE with a reason.');
  }

  if (Object.keys(envMap).length < 10) {
    throw new Error('only ' + Object.keys(envMap).length + ' ENV_MAP entries parsed — ' +
      'the table\'s shape changed and this generator is silently capturing almost none of it');
  }

  const body = JSON.stringify({
    note: 'Generated by tools/settings-tables.js from the LIVE src/settings.js. Do not edit.',
    defaults: S.DEFAULTS,
    // THE KEY ORDER, so a settings.json written by the Go side is byte-comparable
    // with one written by Node. `JSON.stringify` follows insertion order and Go's
    // encoding/json sorts map keys, so without this the first Go save would
    // rewrite all 113 lines and any operator diffing the file would see churn
    // that means nothing.
    defaultsOrder: Object.keys(S.DEFAULTS),
    encrypted: S.CREDENTIAL_FIELDS,
    envMap,
    pollBounds: S.POLL_BOUNDS,
  }, null, 2) + '\n';

  if (check) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body) {
      console.error('internal/store/settings_tables.json is stale — run: node tools/settings-tables.js');
      process.exit(1);
    }
    console.log('settings tables up to date (' + Object.keys(S.DEFAULTS).length + ' defaults, ' +
                Object.keys(envMap).length + ' env vars, ' +
                Object.keys(S.POLL_BOUNDS).length + ' clamped intervals)');
    return;
  }
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), OUT) + ' — ' +
              Object.keys(S.DEFAULTS).length + ' defaults, ' +
              Object.keys(envMap).length + ' env vars, ' +
              Object.keys(S.POLL_BOUNDS).length + ' clamped intervals');
}

main();
