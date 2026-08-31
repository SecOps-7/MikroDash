'use strict';
/**
 * THE BROWSER'S OWN EVENT VOCABULARY, BOTH DIRECTIONS.
 *
 * `event-audit` does this for the SOCKET vocabulary — what the Go server emits
 * against what the TypeScript subscribes — and found a real bug on its first
 * run. Pages also talk to each other WITHOUT the socket, through
 * `document.dispatchEvent(new CustomEvent(...))`, and that vocabulary had no
 * audit at all.
 *
 * It has the same two failure modes, and both are silent:
 *
 *   A. ANNOUNCED, NOBODY LISTENING — work done for a consumer that does not
 *      exist, or a consumer that was never ported.
 *   B. LISTENED FOR, NEVER ANNOUNCED — a handler that simply never runs. The
 *      page looks like it has nothing to show rather than like it is broken.
 *
 * ── IT FOUND ONE OF EACH ON ITS FIRST RUN ───────────────────────────────────
 *
 * B was `worldmap:ready`. The live app publishes the decoded atlas on
 * `window._worldMapPathDs` / `_worldMapCentroids` and announces it, so the
 * DASHBOARD's Connections Map card can reuse the paths instead of decoding them
 * again. This port did neither, while `dashboard.ts` listened for the
 * announcement AND checked the global as a fallback — so both routes were dead
 * and **the dashboard's map card could never initialise**. Fixed in
 * `connections-worldmap.ts`; removing either the announcement or the published
 * atlas fails THIS audit, which is the pin — a behavioural gate for the card
 * itself would need the atlas fetch and is not what this file is.
 *
 * A was `mikrodash:resmount`: three pages fire it when a card swaps which resource
 * its Add slot belongs to, and nothing here listened — so the Add button on a
 * swapped tab kept the PREVIOUS tab's resource. Ported the same day, and the
 * ledger entry is gone because this audit FAILED until it was deleted, which is
 * the only reason a record ever gets removed.
 *
 * ── WHY IT COMPARES AGAINST THE LIVE APP TOO ────────────────────────────────
 *
 * An announcement the live app makes and this port does not is a consumer here
 * waiting forever — exactly the `worldmap:ready` case. Names alone would not
 * have shown it: the port both announced and listened for `mikrodash:pagechange`,
 * so a port-only audit would have called the vocabulary consistent.
 *
 *   node tools/announcement-audit.js
 *
 * ── IT CANNOT TELL A COMMENT FROM CODE ──────────────────────────────────────
 *
 * The scan is textual, so writing `window._sitesById` inside a COMMENT reports
 * it as a read with no producer. That happened on 2026-08-25, in a comment
 * explaining why this port does NOT use that cache — the audit was right that
 * the name appeared and wrong about what it meant.
 *
 * Left textual rather than taught to strip comments: a parser here would be a
 * second implementation of "what counts as a read", and the false positive is
 * cheap and obvious. Reword the comment.
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('announcement-audit');
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

const ts = readAll(path.join(ROOT, 'web', 'src'), '.ts');
const rel = (f) => path.relative(path.join(ROOT, 'web', 'src'), f.path).split(path.sep).join('/');

/** name -> files. Only NAMESPACED events: a bare 'click' is a DOM event. */
const NS = /^[a-z][\w-]*:[\w:-]+$/;
const announced = new Map();
const listened = new Map();
const add = (m, k, v) => { if (!m.has(k)) m.set(k, new Set()); m.get(k).add(v); };

for (const f of ts) {
  for (const m of f.body.matchAll(/new CustomEvent\(\s*'([^']+)'/g)) {
    if (NS.test(m[1])) add(announced, m[1], rel(f));
  }
  for (const m of f.body.matchAll(/addEventListener\(\s*'([^']+)'/g)) {
    if (NS.test(m[1])) add(listened, m[1], rel(f));
  }
}

// ── DYNAMIC NAMES ───────────────────────────────────────────────────────────
//
// `dashboard-grid-edit.ts` announces `new CustomEvent(eventName, …)` with a
// VARIABLE, so no literal appears. The names it can take are declared beside it
// and are listed here rather than being reported as listeners with no producer —
// a pattern that cannot see a shape must say so, not accuse.
const DYNAMIC = {
  'dashcard:room:focus': 'dashboard-grid-edit.ts announces it through a variable name',
  'dashcard:room:blur': 'dashboard-grid-edit.ts announces it through a variable name',
};
for (const k of Object.keys(DYNAMIC)) add(announced, k, 'dashboard-grid-edit.ts (dynamic)');

/** The live app's announcements, so a name this port DROPPED is visible. */
const liveSrc = (() => {
  const f = path.join(LIVE, 'public', 'app.js');
  return fs.existsSync(f) ? fs.readFileSync(f, 'utf8') : '';
})();
// FROZEN — the derived SET of live announcements. This is the half of the audit
// that catches a name this port DROPPED, so an empty set would silently remove
// exactly that half while the audit still reported success.
const liveAnnounced = new Set(G.value('the live announcements', () =>
  [...new Set([...liveSrc.matchAll(/new CustomEvent\(\s*'([^']+)'/g)]
    .map((m) => m[1]).filter((n) => NS.test(n)))].sort()));
if (liveAnnounced.size < 5) {
  throw new Error('only ' + liveAnnounced.size + ' live announcements recorded — the golden '
    + 'is broken, and the DROP half of this audit would check nothing');
}

/**
 * Names allowed to be one-sided, each with the reason.
 *
 * Checked in BOTH directions: an entry that stops being needed FAILS, so a
 * record cannot outlive its problem.
 */
//
// ── ONE ENTRY WAS DELETED WHEN COMMENTS STOPPED COUNTING, AND WHY ───────────
//
// `_wanGeoDetect` lived here saying "the port reproduces the orphan rather than
// inventing a producer the original does not have". It does not: `pages/dhcp.ts`
// says in as many words that only the working statement of the live handler is
// ported, and the call is absent. The entry was satisfied ONLY because this
// audit matched the COMMENT describing the decision — so a record claiming the
// port did a thing was kept alive by the prose explaining that it deliberately
// did not.
//
// Do not re-add it. A name the port does not read is not this audit's business;
// the reasoning lives at `pages/dhcp.ts` and in ../MikroDash/ToDo.md §23.
const RECORDED = {};

const problems = [];
const seen = new Set();

for (const [name, where] of announced) {
  if (listened.has(name)) continue;
  if (name in RECORDED) { seen.add(name); continue; }
  problems.push('ANNOUNCED, NOBODY LISTENING: ' + name + ' (' + [...where].join(', ') + ')');
}
for (const [name, where] of listened) {
  if (announced.has(name)) continue;
  if (name in RECORDED) { seen.add(name); continue; }
  problems.push('LISTENED FOR, NEVER ANNOUNCED: ' + name + ' (' + [...where].join(', ') +
                ')' + (liveAnnounced.has(name) ? ' — and the LIVE app announces it, so this ' +
                'port dropped the producer' : ''));
}
for (const name of liveAnnounced) {
  if (announced.has(name)) continue;
  if (!listened.has(name)) continue; // neither side here: not this port's business
  if (name in RECORDED) { seen.add(name); continue; }
  problems.push('THE LIVE APP ANNOUNCES ' + name + ' AND THIS PORT DOES NOT, while ' +
                [...listened.get(name)].join(', ') + ' waits for it');
}

// ── THE OTHER CROSS-MODULE CHANNEL: `window.<_global>` ──────────────────────
//
// The same conversation happens through published globals, and it fails the same
// way. `_worldMapPathDs` and `_worldMapLocalCC` were BOTH read here and written
// nowhere — one killed the dashboard map card outright, the other left it
// drawing no arcs while still colouring countries, which looks like a working
// map with nothing flowing.
//
// Only names read through `window` / `globalThis` / the `w()` accessor count: a
// module-local `_name` is not a channel. A TYPE DECLARATION is not a write —
// that is precisely what made both bugs look answered.
const globals = new Map();
const writes = new Map();

/**
 * Strip comments before scanning.
 *
 * ── A PROSE MENTION IS NOT A READ ──────────────────────────────────────────
 *
 * This audit matched `window._activeRouterId` inside a COMMENT in
 * `pages/notifications.ts` — a line explaining that the port passes an accessor
 * INSTEAD of reaching for that global — and reported the port as having dropped
 * the producer. The code it was describing does the opposite of what it was
 * accused of.
 *
 * That is worse than a nuisance: the fix a reader would reach for is to publish
 * a global the port deliberately does not need. Rewording the comment would have
 * cleared the audit and left the trap for the next person to describe a global
 * they chose not to use.
 *
 * String literals are left alone. A name in a string can be a real channel —
 * `w()['_x']` — and dropping them would trade a false positive for a false
 * negative, which is the wrong direction for a ledger.
 */
function stripComments(src) {
  let out = '';
  let i = 0;
  while (i < src.length) {
    const c = src[i];
    const n = src[i + 1];
    if (c === '/' && n === '/') {
      const nl = src.indexOf('\n', i);
      i = nl < 0 ? src.length : nl;
    } else if (c === '/' && n === '*') {
      const end = src.indexOf('*/', i + 2);
      i = end < 0 ? src.length : end + 2;
    } else if (c === '"' || c === "'" || c === '`') {
      const quote = c;
      out += c;
      i += 1;
      while (i < src.length && src[i] !== quote) {
        // A backslash escapes the next character, INCLUDING the quote — without
        // this, `'\''` ends the string early and the rest of the file is read
        // as if it were inside one.
        if (src[i] === '\\') { out += src[i]; i += 1; }
        if (i < src.length) { out += src[i]; i += 1; }
      }
      if (i < src.length) { out += src[i]; i += 1; }
    } else {
      out += c;
      i += 1;
    }
  }
  return out;
}

for (const f of ts) {
  const body = stripComments(f.body);
  for (const m of body.matchAll(/(?:window|globalThis|w\(\))\s*(?:as[^)]*\)?)?\s*\.\s*(_[A-Za-z]\w*)/g)) {
    add(globals, m[1], rel(f));
  }
  for (const m of body.matchAll(/\.\s*(_[A-Za-z]\w*)\s*(?:\?\?)?=(?!=)/g)) add(writes, m[1], rel(f));
}
for (const [name, where] of globals) {
  if (writes.has(name)) continue;
  if (name in RECORDED) { seen.add(name); continue; }
  problems.push('READ THROUGH window BUT NEVER WRITTEN: ' + name + ' (' + [...where].join(', ') +
                ')' + (liveSrc.includes('window.' + name + '=') || liveSrc.includes('window.' + name + ' =')
                  ? ' — and the LIVE app publishes it, so this port dropped the producer' : ''));
}

// ── THE LEDGER IS CHECKED LAST, AND THAT ORDER IS LOAD-BEARING ─────────────
//
// It was checked immediately after the EVENT loops, before the globals loop had
// run — so a global-vocabulary entry was reported stale on the very run that
// added it. `seen` has to be complete before it is read.
for (const name of Object.keys(RECORDED)) {
  if (!seen.has(name)) {
    problems.push('RECORDED still excuses ' + name + ', which is no longer one-sided. ' +
                  'Delete the entry.');
  }
}

// BELIEVABILITY: a regex that matched nothing would report a clean vocabulary.
if (announced.size < 3 || listened.size < 3 || globals.size < 3) {
  console.error('only ' + announced.size + ' announcements and ' + listened.size +
                ' subscriptions and ' + globals.size +
                ' published globals found — the scan is not reaching the code');
  process.exit(1);
}
// THE DROP-CATCHING HALF MUST STILL HAVE A LEDGER. It used to test `liveSrc`,
// the raw file — but the set derived from it is now FROZEN, so the file's
// absence is expected and the ledger's emptiness is the real failure. Testing
// the wrong one made this fire on every run without a reference while the audit
// was in fact fully armed.
if (!liveAnnounced.size) {
  console.error('no live announcements are recorded — the half of this audit that catches a '
    + 'DROPPED name is not armed. Re-freeze it against the reference.');
  process.exit(1);
}

if (problems.length) {
  for (const p of problems) console.error('  ' + p);
  console.error('\nannouncement-audit: ' + problems.length + ' problem(s)');
  process.exit(1);
}
console.log('announcement-audit: ' + announced.size + ' announced, ' + listened.size +
            ' listened for, ' + globals.size + ' globals crossing modules, ' +
            Object.keys(RECORDED).length + ' one-sided and recorded');
