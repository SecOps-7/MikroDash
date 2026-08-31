'use strict';
/**
 * The permission vocabulary and the page→permission projection, from rbac.js.
 *
 * ── WHY THIS TABLE NEEDS A GATE ────────────────────────────────────────────
 *
 * `internal/rbac/permissions.go` carries these by hand, with comments that earn
 * their place — the one explaining why `reports` confers `router:schedule` and
 * NOT `router:write` is a real security argument, not decoration. So the Go
 * tables stay the source and this only PINS them: a page added to WRITE_CONFERS
 * upstream would otherwise confer nothing here, and a permission removed there
 * would keep being granted here. Neither shows up as a failure anywhere else —
 * the port would simply answer a different question than the app it mirrors.
 *
 * ── EXTRACTED TEXTUALLY, AND THAT IS THE PRACTICAL CHOICE ──────────────────
 *
 * `rbac.js` requires `db.js`, which requires `better-sqlite3` — a native module
 * built only inside the app container — so it cannot be required on the host.
 * The tables are frozen object and Set literals, and `WRITE_CONFERS_ALWAYS` is
 * not exported at all, so reading the source captures MORE than requiring it
 * would. Every extraction has a floor below which it raises rather than
 * recording a table it failed to parse.
 *
 *   node tools/rbac-tables.js            write the tables
 *   node tools/rbac-tables.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'rbac-tables.json');
const src = fs.readFileSync(path.join(LIVE, 'src', 'rbac.js'), 'utf8');

/**
 * Strip `//` comments from a literal before anything is matched out of it.
 *
 * NOT COSMETIC. `SCOPED`'s entries are commented, and one of those comments is
 * `// purge that router's history` — whose apostrophe made a naive quoted-string
 * match read `'s history…'` as an entry. The extraction then recorded a
 * permission that does not exist, and the count-based floor passed because the
 * total was still ten. Found by the Go test disagreeing, not by this file.
 *
 * The strip is line-based, which is safe here because these literals contain no
 * string holding a `//`.
 */
function stripComments(text) {
  return text.split('\n').map((l) => l.replace(/\/\/.*$/, '')).join('\n');
}

/** Slice a literal by its declaration, to the first line that closes it. */
function slice(decl, close) {
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find `' + decl + '` in src/rbac.js');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error('`' + decl + '` is never closed with `' + close + '`');
  return src.slice(i, j + close.length);
}

/** The quoted strings in a Set literal. */
function setOf(decl, min) {
  const body = stripComments(slice(decl, ']);'));
  const out = [...body.matchAll(/'([^']+)'/g)].map((m) => m[1]);
  // EVERY ENTRY MUST LOOK LIKE A PERMISSION. A count alone is not enough — the
  // apostrophe bug produced the right COUNT and a wrong entry.
  for (const p of out) {
    if (!/^[a-z]+:[a-z]+$/.test(p)) {
      throw new Error(decl + ' yielded ' + JSON.stringify(p) + ', which is not a ' +
        'permission — the extraction is matching something other than the list');
    }
  }
  if (out.length < min) {
    throw new Error(decl + ' yielded only ' + out.length + ' entries (expected at least ' +
      min + ') — the literal changed shape and this would record a table that is mostly missing');
  }
  return out.sort();
}

/** A `key: ['a','b']` map literal. */
function mapOf(decl, min) {
  const body = stripComments(slice(decl, '});'));
  const out = {};
  for (const m of body.matchAll(/^\s*(\w+)\s*:\s*\[([^\]]*)\]/gm)) {
    out[m[1]] = [...m[2].matchAll(/'([^']+)'/g)].map((x) => x[1]);
  }
  if (Object.keys(out).length < min) {
    throw new Error(decl + ' yielded only ' + Object.keys(out).length + ' entries (expected at ' +
      'least ' + min + ') — the literal changed shape');
  }
  return out;
}

function main() {
  const check = process.argv.includes('--check');

  const globalOnly = setOf('const GLOBAL_ONLY = new Set([', 3);
  const scoped = setOf('const SCOPED = new Set([', 5);
  const readConfers = mapOf('const READ_CONFERS = Object.freeze({', 1);
  const writeConfers = mapOf('const WRITE_CONFERS = Object.freeze({', 5);

  const alwaysLine = src.match(/const WRITE_CONFERS_ALWAYS = Object\.freeze\(\[([^\]]*)\]\)/);
  if (!alwaysLine) throw new Error('cannot find WRITE_CONFERS_ALWAYS in src/rbac.js');
  const writeConfersAlways = [...alwaysLine[1].matchAll(/'([^']+)'/g)].map((m) => m[1]);

  // EVERY CONFERRED PERMISSION MUST EXIST. A projection naming a permission that
  // is in neither set would be dead in the live app and, if the port typed it
  // differently, dead in a different way — which is how the two silently answer
  // different questions.
  const known = new Set([...globalOnly, ...scoped]);
  for (const [page, perms] of Object.entries({ ...readConfers, ...writeConfers })) {
    for (const p of perms) {
      if (!known.has(p)) {
        throw new Error('the projection gives ' + page + ' the permission ' + p +
          ', which is in neither GLOBAL_ONLY nor SCOPED');
      }
    }
  }
  for (const p of writeConfersAlways) {
    if (!known.has(p)) throw new Error('WRITE_CONFERS_ALWAYS names an unknown permission: ' + p);
  }

  const body = JSON.stringify({
    note: 'Generated by tools/rbac-tables.js from the LIVE src/rbac.js. Do not edit. ' +
          'Extracted textually because rbac.js requires better-sqlite3 and cannot be ' +
          'loaded on the host; WRITE_CONFERS_ALWAYS is not exported at all.',
    globalOnly, scoped, readConfers, writeConfers, writeConfersAlways,
  }, null, 2) + '\n';

  if (check) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body) {
      console.error('testdata/rbac-tables.json is stale — run: node tools/rbac-tables.js');
      process.exit(1);
    }
    console.log('rbac tables up to date (' + globalOnly.length + ' global-only, ' +
                scoped.length + ' scoped, ' + Object.keys(writeConfers).length + ' write pages)');
    return;
  }
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), OUT) + ' — ' +
              globalOnly.length + ' global-only, ' + scoped.length + ' scoped, ' +
              Object.keys(readConfers).length + ' read pages, ' +
              Object.keys(writeConfers).length + ' write pages');
}

main();
