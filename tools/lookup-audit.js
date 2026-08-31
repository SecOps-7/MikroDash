#!/usr/bin/env node
'use strict';
/**
 * IDS THE PORT LOOKS UP THAT NOTHING PRODUCES — the reverse direction, and the
 * one none of the other four audits can see.
 *
 * ── WHY THIS DIRECTION IS ITS OWN AUDIT ─────────────────────────────────────
 *
 * `wiring-audit`, `attr-audit` and `template-id-audit` all run PRODUCER →
 * CONSUMER: here is something the markup or a template creates, does the port
 * bind it? Every one of them is silent about the opposite mistake — the port
 * asks for `#bwStats` and no markup, on either side, has ever contained it. The
 * lookup returns null, the guard beside it swallows the null, and the feature is
 * simply absent while every gate stays green.
 *
 * That is not hypothetical. It is the exact shape of three defects this port
 * reported into the live repo's `ToDo.md` (items 9, 10 and 11), and the live
 * side then built `test/orphaned-references.test.js` for it — whose sweep found
 * EIGHT more beyond the one reported. This port has no equivalent, so a lookup
 * it gets wrong is found by someone opening the page.
 *
 * ── A PRODUCER IS EITHER OF TWO THINGS ──────────────────────────────────────
 *
 * 1. The EXTRACTED markup in `web/src/ui/*.html`, lifted verbatim from the live
 *    `index.html` — so an id there is one the real page has.
 * 2. An `id="…"` written inside a template string in the port's own TypeScript,
 *    which is what `template-id-audit` enumerates. A control the port renders
 *    into innerHTML is a real producer even though no static markup holds it.
 *
 * 3. A script the SERVED page loads that is not the port's own bundle. There is
 *    exactly one that matters: `web/dist/index.html` pulls `/preflight.js`,
 *    proxied from the live `public/`, and preflight creates `#navBoot` with
 *    `_st.id = 'navBoot'` before any module runs. `nav.ts` removes it on first
 *    paint. Without this the audit called that a dead lookup, when the producer
 *    is simply in another file the page really does load.
 *
 *    Only the absolutely-referenced scripts count, never `./app.js` — that is
 *    the port's own bundle, already covered by the TypeScript scan. Counting
 *    the LIVE `app.js` here would be much worse than useless: it produces over a
 *    thousand ids, and every genuine port orphan would be masked by an id some
 *    page the port does not run happens to create.
 *
 * An id in none of the three is a lookup that cannot succeed.
 *
 * ── THE LEDGER FAILS IN BOTH DIRECTIONS ─────────────────────────────────────
 *
 * `ORPHANS` records the lookups that are knowingly unproduced, each with a
 * reason. The audit fails if the list GROWS — a new dead lookup — and equally if
 * an entry STOPS being an orphan, because an allow-list that quietly accumulates
 * fixed entries is how a record outlives its problem. Same rule the live repo's
 * own sweep uses, and the same rule `KNOWN_INCOMPLETE` uses in `nodecheck/`.
 *
 * ── WHAT IT CANNOT SEE, MEASURED RATHER THAN GUESSED ────────────────────────
 *
 * It resolves a literal id, and ONE hop through a file-local helper that
 * forwards its first parameter into `el()`. That second case was added after the
 * first version reported `wlBand6` while `wlBand24` and `wlBand5` — reached
 * through exactly such a helper, equally unproduced — went unmentioned. Both
 * appeared the moment the hop was resolved, which is the only reason to believe
 * the extension works.
 *
 * WHAT REMAINS UNSEEN is an id that is not a literal anywhere. The port has one:
 * `dashboard-stream-health.ts:41` does `el(cardId + 'Warn')`, building the id at
 * runtime from a card name — so `trafficCardWarn` and its siblings are checked
 * by `dash-coverage-check` instead, which knows how they are composed. Nothing
 * here can, and nothing here pretends to.
 *
 * So the count is a FLOOR, not a sweep. A clean run means "no dead lookup this
 * can resolve", never "no dead lookup".
 *
 *   MIKRODASH_SRC=../MikroDash node tools/lookup-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('lookup-audit');
const SRC = path.join(ROOT, 'web', 'src');
const UI = path.join(SRC, 'ui');

function walk(dir, out) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.name.endsWith('.ts')) out.push(p);
  }
  return out;
}

// ── producers ───────────────────────────────────────────────────────────────
const produced = new Set();
for (const f of fs.readdirSync(UI)) {
  if (!f.endsWith('.html')) continue;
  const html = fs.readFileSync(path.join(UI, f), 'utf8');
  for (const m of html.matchAll(/id="([A-Za-z0-9_-]+)"/g)) produced.add(m[1]);
}
// (3) scripts the served page loads, other than the port's own bundle.
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const distIndex = path.join(ROOT, 'web', 'dist', 'index.html');
const loadedScripts = [];
if (fs.existsSync(distIndex)) {
  const shell = fs.readFileSync(distIndex, 'utf8');
  for (const m of shell.matchAll(/<script[^>]*\ssrc="(\/[^"]+\.js)"/g)) {
    const f = path.join(LIVE, 'public', m[1]);
    if (fs.existsSync(f)) loadedScripts.push(f);
  }
}
// FROZEN — the ids those live scripts produce, not the scripts. The served page
// loads them, so an id they create is genuinely produced; without the reference
// they cannot be read and 17 ids would look unproduced. That is a real shrink:
// 1114 produced with a reference, 1097 without, and the census caught it.
// The COUNT is frozen with the ids, so the summary line reports the same figure
// either way. Left unfrozen it read "(incl. 0 loaded scripts)" without a
// reference — a number that shrank while the thing it described had not.
const liveScriptCount = G.value('how many live scripts the page loads', () => loadedScripts.length);
const liveScriptIds = G.value('ids produced by the live loaded scripts', () => {
  const out = new Set();
  for (const f of loadedScripts) {
    const body = fs.readFileSync(f, 'utf8');
    for (const m of body.matchAll(/\.id\s*=\s*["']([A-Za-z][\w-]*)["']/g)) out.add(m[1]);
    for (const m of body.matchAll(/id=\\?["']([A-Za-z][\w-]*)\\?["']/g)) out.add(m[1]);
  }
  return [...out].sort();
});
for (const id of liveScriptIds) produced.add(id);

const tsFiles = walk(SRC, []);
for (const f of tsFiles) {
  const body = fs.readFileSync(f, 'utf8');
  // Same pattern template-id-audit uses, including the escaped form that
  // appears inside a nested template string.
  for (const m of body.matchAll(/id=\\?["']([A-Za-z][\w-]*)\\?["']/g)) produced.add(m[1]);
  // AND THE ASSIGNMENT SPELLING. A node built with createElement carries its id
  // as `node.id = 'x'`, which the markup pattern above cannot see. Leaving it
  // out reported `sysMetaTemp` — a live-faithful lazily-created child, created
  // exactly this way three lines below its own lookup — as a dead lookup on this
  // audit's first run. Reporting a working control is how an audit gets ignored.
  for (const m of body.matchAll(/\.id\s*=\s*["']([A-Za-z][\w-]*)["']/g)) produced.add(m[1]);
  for (const m of body.matchAll(/setAttribute\(\s*['"]id['"]\s*,\s*['"]([A-Za-z][\w-]*)['"]/g)) produced.add(m[1]);
}

// ── consumers ───────────────────────────────────────────────────────────────
// The four binding spellings template-id-audit documents, plus the plain
// getElementById. A spelling missing here reports a WORKING lookup as dead,
// which is how an audit gets ignored — so they are kept in one place and this
// list is the same one, read the other way round.
const LOOKUPS = [
  /\bel(?:<[^>]*>)?\('([A-Za-z][\w-]*)'\)/g,
  /\bbyId\('([A-Za-z][\w-]*)'\)/g,
  /getElementById\('([A-Za-z][\w-]*)'\)/g,
  /closest\('#([A-Za-z][\w-]*)'\)/g,
  /querySelector(?:All)?\('#([A-Za-z][\w-]*)'\)/g,
];

// AND ONE STEP OF INDIRECTION. The dominant shape in this port is a per-render
// helper that forwards its first parameter straight into `el()`:
//
//   const set = (id: string, v: string): void => { const e = el(id); if (e) … };
//   set('wlBand24', '2.4GHz: ' + b24);
//
// Nine pages define exactly that, so without this the audit misses most of the
// lookups on the pages that use it — `wlBand24` and `wlBand5` were as unproduced
// as `wlBand6` and only `wlBand6` was reported, because only it is spelled
// directly. Resolving ONE hop is cheap and covers the shape that actually
// occurs; chasing helpers across modules would be a different tool, and the
// header still says the count is a floor.
const FORWARDERS = [
  // const NAME = (PARAM: string …) => …      and the `function NAME(PARAM: string …)` form
  /const\s+(\w+)\s*=\s*\(\s*(\w+)\s*:\s*string[^)]*\)\s*(?::[^=]*?)?=>/g,
  /function\s+(\w+)\s*\(\s*(\w+)\s*:\s*string[^)]*\)/g,
];

function forwardersIn(body) {
  const out = [];
  for (const re of FORWARDERS) {
    for (const m of body.matchAll(re)) {
      const [name, param] = [m[1], m[2]];
      // Does the body just after the signature hand that parameter to a lookup?
      // A window rather than a real parse: these helpers are one-liners or close
      // to it, and a window that is too generous can only ADD lookups, which the
      // producer check then clears. It cannot invent a dead id.
      const win = body.slice(m.index, m.index + 400);
      const uses = new RegExp(
        '\\bel(?:<[^>]*>)?\\(\\s*' + param + '\\s*\\)'
        + '|\\bbyId\\(\\s*' + param + '\\s*\\)'
        + '|getElementById\\(\\s*' + param + '\\s*\\)',
      );
      if (uses.test(win)) out.push(name);
    }
  }
  return [...new Set(out)];
}

const looked = new Map(); // id -> Set(file)
const viaHelper = new Set();
for (const f of tsFiles) {
  const body = fs.readFileSync(f, 'utf8');
  const rel = path.relative(SRC, f).split(path.sep).join('/');
  const add = (id, indirect) => {
    if (!looked.has(id)) looked.set(id, new Set());
    looked.get(id).add(rel);
    if (indirect) viaHelper.add(id);
  };
  for (const re of LOOKUPS) {
    for (const m of body.matchAll(re)) add(m[1], false);
  }
  // A forwarder is file-local by construction — every one of these is declared
  // inside the render function that calls it — so the scan stays per-file.
  for (const name of forwardersIn(body)) {
    // NOT a method call. `p.set('actor', …)` on a URLSearchParams matched a
    // bare `set(` and reported six query-parameter names as dead element ids on
    // this extension's first run — the same class of false positive as the
    // missing `node.id =` spelling, and caught the same way: by reading what it
    // accused before believing it.
    const call = new RegExp('(?<![.\\w])' + name + "\\(\\s*'([A-Za-z][\\w-]*)'", 'g');
    for (const m of body.matchAll(call)) add(m[1], true);
  }
}

// ── the ledger ──────────────────────────────────────────────────────────────
// id -> why it is looked up with nothing producing it. Empty is NOT the goal
// here: a lookup the live app also makes against markup the live app also lacks
// is the port reproducing a quirk, which is what a port is for. What must never
// be here is an id this port invented.
const ORPHANS = {
  connMapSub:
    'A LIVE ORPHAN, REPRODUCED. `public/app.js` looks it up at six sites and no '
    + 'file anywhere creates it — the live repo records it in the KNOWN set of '
    + 'test/orphaned-references.test.js:38, a remnant of removed UI, guarded and '
    + 'deliberately not deleted. The port makes the same guarded lookup, so the '
    + 'Connections map behaves identically. Removing it here would be a silent '
    + 'divergence from the page being ported.',
  wlBand6:
    'The same, and from the same list (test/orphaned-references.test.js:40, '
    + 'alongside wlBand24 and wlBand5 — all three are orphans live). The port '
    + 'guards it: `const el6 = el(\'wlBand6\'); if (el6) {…}`, so a 6GHz badge '
    + 'appears if the markup ever gains one and nothing breaks while it does '
    + 'not.',
  wlBand24:
    'The first of wlBand6\'s two siblings, and INVISIBLE to this audit until it '
    + 'learned to resolve one hop of helper indirection — `set(\'wlBand24\', …)` '
    + 'where `set` forwards into `el()`. The header predicted both would appear '
    + 'once that landed, and both did. Same live KNOWN set, same guard.',
  wlBand5:
    'The second. See wlBand24.',
};

const dead = [];
for (const [id, where] of [...looked].sort()) {
  if (produced.has(id)) continue;
  dead.push({ id, where: [...where].sort() });
}

const problems = [];
for (const d of dead) {
  if (ORPHANS[d.id]) continue;
  problems.push(
    d.id + ' is looked up by ' + d.where.join(', ') +
    ' and NOTHING produces it — not the extracted markup, not any template in this port',
  );
}
for (const id of Object.keys(ORPHANS)) {
  if (!dead.some((d) => d.id === id)) {
    problems.push(
      id + ' is recorded as an orphan and is now produced — delete the entry rather than ' +
      'leaving a record that has outlived its problem',
    );
  }
}

say(`lookup-audit: ${looked.size} ids looked up (${viaHelper.size} via a forwarding helper), ` +
    `${produced.size} produced ` +
    `(incl. ${liveScriptCount} loaded script${liveScriptCount === 1 ? '' : 's'}), ` +
    `${dead.length} unproduced (${Object.keys(ORPHANS).length} recorded)`);
if (problems.length) {
  shout('\n' + problems.map((p) => '  ✗ ' + p).join('\n'));
  process.exit(1);
}
say('every lookup has a producer');
