'use strict';
/**
 * The validation tables in `POST /api/settings`, captured from the live source.
 *
 * ── WHY THIS IS TABLES ONLY, AND WHAT THAT COSTS ───────────────────────────
 *
 * Every other differential gate in this repo RUNS the live implementation. This
 * one cannot: the validation is inline in `src/index.js`, which calls
 * `server.listen()` at require time and so cannot be loaded by a test. The live
 * repo moved its write guards into pure modules for exactly this reason; this
 * handler was never moved.
 *
 * So the RULES are ported by reading, and only the DATA is generated — the ~40
 * integer ranges, the string list, the boolean list and the credential list.
 * That is where drift actually happens: a page added means a new `page*` boolean,
 * a new alert type means a new `notif*` boolean, and a hand-copied list would
 * silently stop accepting them while the form kept offering them.
 *
 * The rules themselves are covered by hand-written tests in Go, and this comment
 * is the honest statement of the difference: they are NOT pinned to the original
 * the way the rest of this port is.
 *
 *   node tools/settings-write-tables.js            write the tables
 *   node tools/settings-write-tables.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'internal', 'store', 'settings_write_tables.json');

const src = fs.readFileSync(path.join(LIVE, 'src', 'index.js'), 'utf8');

/** Slice a literal out of the handler by its opening line. */
function sliceLiteral(startsWith, closeAt) {
  const i = src.indexOf(startsWith);
  if (i === -1) throw new Error('cannot find `' + startsWith + '` in src/index.js — ' +
    'the handler changed shape and this generator is capturing nothing');
  const j = src.indexOf(closeAt, i);
  if (j === -1) throw new Error('`' + startsWith + '` is never closed');
  return src.slice(i, j + closeAt.length);
}

// ── the integer ranges ──────────────────────────────────────────────────────
const intBlock = sliceLiteral('const intFields = {', '\n    };');
const intFields = {};
for (const m of intBlock.matchAll(/(\w+)\s*:\s*\[\s*(-?\d+)\s*,\s*(-?\d+)\s*\]/g)) {
  intFields[m[1]] = [Number(m[2]), Number(m[3])];
}

// ── the three name lists ────────────────────────────────────────────────────
function names(startsWith) {
  const block = sliceLiteral(startsWith, '];');
  return [...block.matchAll(/'([A-Za-z0-9_]+)'/g)].map((m) => m[1]);
}
const strFields = names("const strFields  = [");
const credFields = names("const credFields = [");

// boolFields spreads `Pages.SETTING_KEYS`, so the literal alone is short by
// every page toggle. The module IS loadable — it is a plain table — so the
// spread is resolved rather than approximated.
const Pages = require(path.join(LIVE, 'src', 'pages.js'));
const boolLiteral = names("const boolFields = [...Pages.SETTING_KEYS,");
const boolFields = [...Pages.SETTING_KEYS, ...boolLiteral];

// ── COMPLETENESS AGAINST THE DEFAULTS TABLE ───────────────────────────────
//
// The four tables above are extracted by PATTERN, and a pattern cannot report a
// shape it does not recognise. That is not hypothetical: the settings FORM map
// missed thirteen alert toggles for exactly this reason, because they were
// filled by a loop over objects rather than over strings.
//
// So the same data-driven completeness check applies here. Every settings key
// the handler MENTIONS must be accounted for — in one of the four tables, or in
// the list of special cases below, each of which the Go port handles explicitly.
// A new special case upstream makes this refuse rather than silently dropping
// the field from every save.
const DEFAULT_KEYS = Object.keys(
  JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'internal', 'store',
    'settings_tables.json'), 'utf8')).defaults);

// The handler's body, from the route to its closing brace.
const handlerStart = src.indexOf("app.post('/api/settings'");
const handlerBody = src.slice(handlerStart, src.indexOf("\napp.", handlerStart + 10));

const SPECIAL_CASES = {
  authMode: 'whitelisted to none|modern',
  sessionTimeoutMs: '0 or 3600000..86400000 — zero means NEVER and must not be clamped',
  notifBody: 'trimmed to 512, not 256',
  notifBodyUp: 'trimmed to 512, not 256',
  customPollProfile: 'cleared, or a value that JSON.parse reads as an object',
  displayTimezone: 'cleared, or a zone the runtime recognises',
};

const covered = new Set([
  ...Object.keys(intFields), ...strFields, ...boolFields, ...credFields,
  ...Object.keys(SPECIAL_CASES),
]);
const mentioned = new Set(
  [...handlerBody.matchAll(/'([A-Za-z][A-Za-z0-9_]*)'/g)].map((m) => m[1]),
);
const unhandled = DEFAULT_KEYS.filter((k) => mentioned.has(k) && !covered.has(k));
if (unhandled.length) {
  throw new Error('POST /api/settings names these settings and none of the four tables ' +
    'covers them:\n  ' + unhandled.join('\n  ') +
    '\nAdd the shape that validates them, or list each in SPECIAL_CASES with a reason.');
}

function main() {
  const check = process.argv.includes('--check');

  for (const [name, list] of Object.entries({ intFields, strFields, credFields, boolFields })) {
    const n = Array.isArray(list) ? list.length : Object.keys(list).length;
    if (n < 3) throw new Error(name + ' captured only ' + n + ' entries — the literal ' +
      'changed shape and this would silently accept almost nothing');
  }

  const body = JSON.stringify({
    note: 'Generated by tools/settings-write-tables.js from the LIVE src/index.js. Do not edit. ' +
          'TABLES ONLY — the validation rules are ported by reading, because the handler is ' +
          'inline in index.js and cannot be loaded by a test.',
    intFields, strFields, boolFields: [...new Set(boolFields)].sort(), credFields,
    // The keys handled outside the four tables — see SPECIAL_CASES.
    specialCases: Object.keys(SPECIAL_CASES).sort(),
  }, null, 2) + '\n';

  if (check) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body) {
      console.error('internal/store/settings_write_tables.json is stale — run: node tools/settings-write-tables.js');
      process.exit(1);
    }
    console.log('settings write tables up to date (' + Object.keys(intFields).length + ' ranges, ' +
                boolFields.length + ' booleans)');
    return;
  }
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), OUT) + ' — ' +
              Object.keys(intFields).length + ' integer ranges, ' + strFields.length + ' strings, ' +
              new Set(boolFields).size + ' booleans, ' + credFields.length + ' credentials');
}

main();
