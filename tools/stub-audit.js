'use strict';
/**
 * A GATE MUST NOT QUIETLY REPLACE LIVE LOGIC WITH ITS OWN.
 *
 * ---- WHY THIS EXISTS -------------------------------------------------------
 *
 * `backups-page-check` carried `function saveSettings(){}` — an empty stub of a
 * real page function — under a comment saying another gate owned it. That gate
 * did not exist and never had: the only occurrence of its name anywhere in the
 * repo was the comment. The Save button's emit was therefore UNOWNED WHILE
 * LOOKING OWNED, which is worse than an acknowledged gap, and it survived
 * because nothing could see it. `#bkSave` showing up as an uncovered element was
 * the only symptom, and that was a coincidence.
 *
 * That gate's own header already knew the rule — "a stub is a rewrite" — written
 * when the destructive three (Delete, Restore, Back Up Now) were found stubbed
 * four lines above `saveSettings`. The rule was right and nothing enforced it.
 *
 * ---- WHAT IT CHECKS --------------------------------------------------------
 *
 * Every `tools/*.js` is scanned for a COMPLETE function definition inside a
 * quoted string — `'function name(...) { ... }'` — which is how a gate injects
 * code into a lifted context. If that name is also a function in the live
 * `public/app.js`, the gate is shadowing live logic with its own, and must
 * declare it in ALLOWED below with a reason.
 *
 * A LIFT ANCHOR IS NOT A STUB. Most quoted `'function esc('` strings in this
 * repo are arguments to `L.whole` / `slice` / `whole`, naming what to lift. They
 * carry no body and are ignored — the pattern requires both braces.
 *
 * ---- WHAT IT DOES NOT CATCH, SAID PLAINLY ---------------------------------
 *
 * A stub written as `var f = function(){}`, as an arrow, or assembled from
 * pieces. This catches the shape that has actually occurred twice, and it is a
 * tripwire rather than a proof. It also cannot tell a HARNESS shim from a page
 * stub on its own — that is what the declaration is for, and why each entry
 * carries a reason rather than just a name.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/stub-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('stub-audit');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

/**
 * Names a gate may define itself, each with the reason it is not a rewrite.
 *
 * These are HARNESS shims: they stand in for the browser or for the app shell,
 * not for logic under test. The port is handed the same thing, so neither side
 * gets an advantage.
 */
const ALLOWED = {
  $: 'the live `$(id)` helper — document.getElementById, and nothing else',
  el: 'as `$`, under the other spelling',
  _debounce: 'identity instead of a timer, so a gate does not have to wait; both sides run undebounced',
  pageVisible: 'always true — a gate is by definition looking at the page it is testing, and the port is given `() => true`',
};

const bad = [];
let scanned = 0, defs = 0;

// FROZEN — the set of function names the live app defines. It is a lifted VALUE
// the audit filters on, so guarding it would leave the set EMPTY and every
// injected definition would be dismissed as "a name the live app does not have".
// Measured: with the reference this audit finds 37 shadowing definitions, and
// without it found 0 while still reporting success.
const liveFns = new Set(G.value('the live function names', () =>
  [...src.matchAll(/(?:^|\s)function\s+([A-Za-z_$][\w$]*)\s*\(/g)].map((m) => m[1])));
if (liveFns.size < 50) {
  throw new Error('only ' + liveFns.size + ' live function names recorded — the golden is '
    + 'broken, and this audit would silently find nothing');
}

for (const f of fs.readdirSync(path.join(ROOT, 'tools')).filter((n) => n.endsWith('.js'))) {
  const body = fs.readFileSync(path.join(ROOT, 'tools', f), 'utf8');
  scanned++;
  // A complete definition inside ONE quoted string: an opening brace and a
  // closing one, with no quote between them.
  for (const m of body.matchAll(/'(function\s+([A-Za-z_$][\w$]*)\s*\([^)]*\)\s*\{[^']*\})'/g)) {
    const name = m[2];
    if (!liveFns.has(name)) continue;   // a name the live app does not have is the gate's own
    defs++;
    if (ALLOWED[name]) continue;
    bad.push({ file: f, name, text: m[1].replace(/\s+/g, ' ').slice(0, 100) });
  }
}

// The declaration must not outlive its need either — an ALLOWED entry for a name
// no gate defines any more is a rule nobody is following, and reads as though
// something is still being shimmed.
const defined = new Set();
for (const f of fs.readdirSync(path.join(ROOT, 'tools')).filter((n) => n.endsWith('.js'))) {
  const body = fs.readFileSync(path.join(ROOT, 'tools', f), 'utf8');
  for (const m of body.matchAll(/'(function\s+([A-Za-z_$][\w$]*)\s*\([^)]*\)\s*\{[^']*\})'/g)) {
    defined.add(m[2]);
  }
}
const stale = Object.keys(ALLOWED).filter((n) => !defined.has(n));

if (bad.length || stale.length) {
  for (const b of bad) {
    console.error('  ' + b.file + ' defines `' + b.name + '`, which is a function in the live ' +
      'app.js — a gate that defines it is comparing its own version, not the app\'s:');
    console.error('      ' + b.text);
  }
  for (const n of stale) {
    console.error('  ALLOWED lists `' + n + '` but no gate defines it any more — ' +
      'delete the entry rather than leaving a permission nobody uses');
  }
  console.error('\nstub-audit: ' + (bad.length + stale.length) + ' problem(s)');
  process.exit(1);
}

console.log('stub-audit: ' + scanned + ' tools scanned, ' + defs +
  ' injected definition(s) shadowing a live function, all declared harness shims');
