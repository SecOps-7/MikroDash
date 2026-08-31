'use strict';
/**
 * EVERY BROWSER-STORAGE KEY THIS PORT USES, AGAINST THE LIVE APP'S.
 *
 * A storage key is a CONTRACT WITH THE PAST. The operators who will run this
 * port already have preferences saved by the Node app under its spellings, and
 * a port that invents its own reads none of them: nothing breaks, nothing logs,
 * and the setting simply is not there any more. At cutover that is indisputably
 * user-visible and completely silent — the worst combination this port can
 * produce.
 *
 * ── IT FOUND ONE ON ITS FIRST RUN ───────────────────────────────────────────
 *
 * `RPT_PRESET_KEY` was `'mikrodash.rpt.preset'` here and `'mkd_rpt_preset'`
 * live (`../MikroDash/public/app.js:9583`). Every operator's saved Reports date
 * preset would have been invisible after cutover, the page falling back to
 * `last7d`. The port's spelling was the more consistent of the two, which is
 * precisely why it was written — the live app is inconsistent (`mkd_` for the
 * preset, `mikrodash_` for the capacity two lines away) and matching an
 * inconsistency feels like a mistake until you remember whose data it is.
 *
 * ── HOW IT MATCHES ──────────────────────────────────────────────────────────
 *
 * By the CONSTANT'S NAME, not by value. Both codebases name these the same way
 * (`RPT_PRESET_KEY`, `VIEW_KEY`, …), so a name present on both sides with
 * different values is the failure this exists for. A key the port defines and
 * the live app does not is reported too — it may be new functionality, and then
 * it belongs in `PORT_ONLY` with a reason.
 *
 *   node tools/storage-key-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('storage-key-audit');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

function readAll(dir, ext, out = []) {
  if (!fs.existsSync(dir)) return out;
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) readAll(p, ext, out);
    else if (e.name.endsWith(ext)) out.push({ path: p, body: fs.readFileSync(p, 'utf8') });
  }
  return out;
}

const KEYCONST = /(?:const|var|let)\s+([A-Z][A-Z0-9_]*KEY[A-Z0-9_]*)\s*=\s*'([^']+)'/g;
/** A key spelled inline at the call site rather than through a constant. */
const INLINE = /(?:localStorage|sessionStorage)\.(?:getItem|setItem|removeItem)\(\s*'([^']+)'/g;

const portFiles = readAll(path.join(ROOT, 'web', 'src'), '.ts');
const port = new Map();      // CONST NAME -> { value, file }
const portInline = new Map(); // literal key -> file
for (const f of portFiles) {
  const rel = path.relative(path.join(ROOT, 'web', 'src'), f.path).split(path.sep).join('/');
  for (const m of f.body.matchAll(KEYCONST)) port.set(m[1], { value: m[2], file: rel });
  for (const m of f.body.matchAll(INLINE)) portInline.set(m[1], rel);
}

const liveDir = path.join(LIVE, 'public');
let liveSrc = '';
for (const f of fs.existsSync(liveDir) ? fs.readdirSync(liveDir) : []) {
  if (f.endsWith('.js')) liveSrc += '\n' + fs.readFileSync(path.join(liveDir, f), 'utf8');
}
// FROZEN — the two derived LEDGERS, name->value and the set of values. They are
// what this audit compares the port's keys against, so an empty pair would make
// every port key look unremarked. The emptiness check below now tests the
// LEDGER rather than the raw source, since the source's absence is expected.
const [liveEntries, liveValueList] = G.value('the live storage keys', () => {
  const m1 = new Map();
  for (const m of liveSrc.matchAll(KEYCONST)) m1.set(m[1], m[2]);
  const vs = new Set([...m1.values()]);
  for (const m of liveSrc.matchAll(INLINE)) vs.add(m[1]);
  return [[...m1.entries()].sort(), [...vs].sort()];
});
const live = new Map(liveEntries);
const liveValues = new Set(liveValueList);
if (!live.size) {
  console.error('no live storage keys are recorded — this audit compares against them and '
    + 'cannot run without a ledger. Re-freeze it against the reference.');
  process.exit(1);
}

/** Keys this port has and the live app does not, each with a reason. */
const PORT_ONLY = {};

const problems = [];
const seen = new Set();

for (const [name, { value, file }] of port) {
  if (live.has(name)) {
    if (live.get(name) !== value) {
      problems.push(name + ' is \'' + value + '\' in ' + file + ' but \'' + live.get(name) +
                    '\' in the live app — every value an operator already saved under the live ' +
                    'spelling would be invisible after cutover');
    }
    continue;
  }
  // The constant is named differently but the VALUE may still be the live one.
  if (liveValues.has(value)) continue;
  if (name in PORT_ONLY) { seen.add(name); continue; }
  problems.push(name + " = '" + value + "' (" + file + ') exists in neither the live constants nor ' +
                'its storage calls. If it is new, add it to PORT_ONLY with a reason; if it is a ' +
                'rename, use the live spelling.');
}

for (const [key, file] of portInline) {
  if (liveValues.has(key)) continue;
  if (key in PORT_ONLY) { seen.add(key); continue; }
  problems.push("the inline key '" + key + "' (" + file + ') appears nowhere in the live app');
}

for (const k of Object.keys(PORT_ONLY)) {
  if (!seen.has(k)) problems.push('PORT_ONLY still excuses ' + k + ', which no longer needs it.');
}

// BELIEVABILITY: a pattern that matched nothing would report a clean contract.
if (port.size < 3) {
  console.error('only ' + port.size + ' storage-key constants found in the port — the scan is not ' +
                'reaching the code it is meant to check');
  process.exit(1);
}
if (live.size < 5) {
  console.error('only ' + live.size + ' storage-key constants found in the live app — the scan is ' +
                'not reaching it, and every comparison above would pass by default');
  process.exit(1);
}

if (problems.length) {
  for (const p of problems) console.error('  ' + p);
  console.error('\nstorage-key-audit: ' + problems.length + ' problem(s)');
  process.exit(1);
}
console.log('storage-key-audit: ' + port.size + ' key constants and ' + portInline.size +
            ' inline keys, all matching the live app (' + live.size + ' live constants)');
