'use strict';
/**
 * NO GATE MAY LOSE A CASE TO A REPEATED NAME.
 *
 * Every gate here keeps its corpus in an object literal keyed by a sentence.
 * JavaScript keeps the LAST entry for a repeated key and says nothing, so a case
 * added with a name already used three hundred lines above simply replaces the
 * earlier one. The gate then reports the same number of identical cases it
 * reported before, and the older case is gone.
 *
 * ---- IT FOUND TWO ON ITS FIRST RUN ----------------------------------------
 *
 * `dhcp-table-check` had two cases called 'markup in a server name' -- one for a
 * LEASE's `server` field and one for a SERVER row's own name, which are
 * different columns. The lease case had not run since the server case was added.
 *
 * `routing-page-check` had 'an inactive route' twice, identically, which cost
 * nothing but hid that a real case was missing where the second one sat.
 *
 * Both were found because a THIRD duplicate was created on 2026-08-25 and the
 * case count did not move. That is the whole failure mode: the number a gate
 * prints is the number of cases that SURVIVED, and nothing compared it to the
 * number written down.
 *
 * ---- WHAT IT READS --------------------------------------------------------
 *
 * Top-level keys of a `const CASES = {` literal: exactly two spaces, a quoted
 * name, a colon. Nested object keys inside a case's VALUE sit at deeper
 * indentation and are not read -- a looser pattern reported twenty-six gates on
 * its first attempt, every one of them a fragment of a payload rather than a
 * case name.
 *
 *   node tools/case-name-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const TOOLS = __dirname;
const KEY = /^ {2}(?:"([^"]+)"|'([^']+)')\s*:/gm;

const problems = [];
let scanned = 0;
let cases = 0;

for (const f of fs.readdirSync(TOOLS).sort()) {
  if (!f.endsWith('-check.js')) continue;
  const body = fs.readFileSync(path.join(TOOLS, f), 'utf8');
  const at = body.search(/^const CASES\s*=\s*\{/m);
  if (at < 0) continue;

  // ---- TWO SHAPES, AND THE SECOND IS NOT A FAULT --------------------------
  //
  // Most gates write the corpus out. `reports-tabs-check` starts from `{}` and
  // fills it in a loop over the tabs it read from the page markup, so there is
  // no literal body and no closing `};` — reporting that as "never closed" was
  // this audit accusing a gate of a shape it simply had not been taught.
  //
  // For that form the ASSIGNED names are read instead. A name built from a
  // variable cannot be read statically and is not claimed to be: what this
  // catches there is a repeated literal, which is the mistake a person makes.
  const literal = body.indexOf('\n};', at);
  const emptyStart = /^const CASES\s*=\s*\{\s*\}/m.test(body.slice(at, at + 40));
  scanned++;
  let keys;
  if (emptyStart || literal < 0) {
    keys = [...body.matchAll(/^CASES\[(?:"([^"]+)"|'([^']+)')\]\s*=/gm)].map((m) => m[1] || m[2]);
  } else {
    keys = [...body.slice(at, literal).matchAll(KEY)].map((m) => m[1] || m[2]);
  }
  cases += keys.length;
  const dupes = [...new Set(keys.filter((k, i) => keys.indexOf(k) !== i))];
  for (const d of dupes) {
    problems.push(f + ': the case name ' + JSON.stringify(d) + ' is used ' +
      keys.filter((k) => k === d).length + ' times -- every one but the last is DISCARDED, ' +
      'silently, and the gate still reports a full run');
  }
}

// BELIEVABILITY: a pattern that matched nothing would report a clean sweep, and
// so would one that found no gates at all.
if (scanned < 10 || cases < 200) {
  console.error('read ' + cases + ' case names across ' + scanned + ' gates -- the scan is not ' +
    'reaching the corpora it is meant to check, and every gate would pass by default');
  process.exit(1);
}

if (problems.length) {
  for (const p of problems) console.error('  ' + p);
  console.error('\ncase-name-audit: ' + problems.length + ' problem(s)');
  process.exit(1);
}
console.log('case-name-audit: ' + cases + ' case names across ' + scanned +
  ' gates, none repeated');
