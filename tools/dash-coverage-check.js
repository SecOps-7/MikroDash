'use strict';
/**
 * Which parts of the Dashboard actually have a writer in this port?
 *
 * ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
 *
 * Part 63 concluded "the Dashboard is done" from `dashboard-wiring-check.js`
 * reporting seven cards wired. That check answered its own question honestly and
 * the conclusion did not follow: it asks whether each `dashboard-*.ts` module is
 * wired, and **it cannot see a card that was never given a module.** Its table is
 * hand-maintained, so anything nobody thought of is invisible to it.
 *
 * This asks the opposite question, of the PAGE rather than of the port: every id
 * in `page-dashboard.html`, and whether anything in `web/src` or the stylesheet
 * mentions it. It cannot be fooled by a card nobody remembered, because the
 * markup is extracted from the live app and lists them all.
 *
 * ── IT IS A LEDGER, NOT A PASS/FAIL ─────────────────────────────────────────
 *
 * Most of the page is legitimately unported today. So the unreferenced ids are
 * RECORDED, in named groups, and the check fails in BOTH directions: an id that
 * gains a writer must leave the list, and a new unreferenced id must be added to
 * it. That way the number only ever moves deliberately.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/dash-coverage-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const html = fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', 'page-dashboard.html'), 'utf8');
const css = fs.readFileSync(path.join(ROOT, 'web', 'public', 'app.css'), 'utf8');

function walk(dir, acc = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, acc);
    else if (/\.ts$/.test(e.name)) acc.push(p);
  }
  return acc;
}
const ts = walk(path.join(ROOT, 'web', 'src')).map((p) => fs.readFileSync(p, 'utf8')).join('\n');

const ids = [...new Set([...html.matchAll(/id="([A-Za-z0-9_-]+)"/g)].map((m) => m[1]))];
assert.ok(ids.length > 100, 'only ' + ids.length + ' ids found — the markup scan broke');

// TEXT matching, with the limitation stated: a mention in a COMMENT counts, and
// an id assembled at runtime does not. Both directions are wrong in principle;
// in practice the ledger carries the handful this affects, named individually.
const referenced = (id) =>
  ts.includes("'" + id + "'") || ts.includes('"' + id + '"') || css.includes('#' + id);

// ── the ledger ─────────────────────────────────────────────────────────────
// `EXTRA_CARDS` and `STALE_UI` were declared here and USED BY NOTHING — two
// groups that emptied as their cards were ported, leaving their prose behind.
// Deleted 2026-08-24. It is not a harmless leftover: EXTRA_CARDS still read "14
// EXTRA cards, hidden by default — a ~570-line IIFE at the foot of app.js that
// this port has not taken on", and a session read that as the Dashboard's
// remaining work and went looking for it. All fourteen are ported
// (`web/src/pages/dashboard-card-*.ts`, eleven modules) and every one of their
// ids has a writer, which is why neither constant was reachable any more.
//
// A dead constant carrying a stale claim is worse than a dead constant: nothing
// fails, and the prose keeps being true-looking. The lint that would have caught
// it is the compiler this file does not have.
const DIAGRAM = 'the network diagram and its ping block';

const UNPORTED = {};

for (const id of [
  // `ndPingSection` WAS here — the block's visibility, driven by `pingEnabled`.
  // Closed on 2026-08-24: `caps.ts` applies the setting, and this ledger is what
  // said so, refusing to stay standing the moment the port started writing the
  // id. The four stat ids inside it were already written, so the gap was only
  // ever the hiding — an operator with ping switched off saw a permanently empty
  // block where the live app shows nothing.
  //
  // `ndWanIp` WAS here too, waiting on `lan:wan`. Closed 2026-08-24: the port
  // emits that event and `dhcp.ts` writes the id. The blocker recorded against
  // it — "no router-wide emit convention" — was false the whole time.
  //
  // THE GROUP IS NOW EMPTY, and is kept only so the next diagram id has an
  // obvious home. If it is still empty when the map's SVG half lands, delete it
  // rather than leaving a named group standing for nothing — that is how
  // EXTRA_CARDS came to carry a stale claim for iterations.
]) UNPORTED[id] = DIAGRAM;
// STATIC SVG: in the markup and the stylesheet, and never touched by JavaScript
// in the LIVE app either. They need no writer here and never will. Listed
// separately so the ledger's total stops implying work that does not exist —
// Part 64 counted them as unported, which was true of the letter and false of
// the substance.
// Ids the port DOES write, through an id built at runtime. This checker matches
// text, so it cannot see `cardId + 'Warn'` — and, worse, it counts a mention in a
// COMMENT as a writer, which is how `trafficCardWarn` passed while its identical
// sibling failed: one of them happened to be named in a comment I had written.
//
// Both are listed here so the ledger says something true. A checker that reads
// text cannot resolve a constructed id; the honest response is to record which
// ones it cannot see rather than to accept a comment as evidence.
const BUILT_ID = 'written by dashboard-stream-health.ts through an id built at runtime ' +
  "(`STREAM_WARN_CARDS[collector] + 'Warn'`), which a text search cannot resolve";
for (const id of ['trafficCardWarn', 'connCardWarn']) UNPORTED[id] = BUILT_ID;

const STATIC = 'static SVG in the network diagram; no JS writes it in the live app either';

// RECLASSIFIED (Part 123). These were filed as "extra cards this port has not
// taken on", which implies outstanding WORK. They are neither: nothing in
// `app.js` references either id, and nothing in the port does — they are
// CONTAINERS whose children carry the ids that get written. The IP Utilisation
// gauge is drawn through `dc-dhcpGaugeFill` / `-Lbl` / `-Pct`, all of which the
// port writes, and the card is gated by `tools/iputil-card-check.js`.
//
// Checked before moving them: `grep dc-dhcpGaugeSvg ../MikroDash/public/app.js`
// returns nothing, and the id appears only in `index.html`. A ledger entry that
// overstates what is left is the same defect as one that understates it.
for (const id of ['dc-dhcpGaugeSvg', 'dc-rtProtoGrid']) UNPORTED[id] = STATIC;
for (const id of [
  'ndLineWired', 'ndLineWireless', 'ndLineWan', 'ndWiredGroup', 'ndWirelessGroup', 'ndRouter',
  'ndWanGroup',
  // The map's wrapper: never touched by JS on either side. The card reaches it
  // as `svg.parentElement` to position the tooltip, which is traversal rather
  // than a lookup, so it has no writer and needs none.
  'dc-worldMapWrap',
]) UNPORTED[id] = STATIC;

const unref = ids.filter((id) => !referenced(id)).sort();
const problems = [];

const missing = unref.filter((id) => !UNPORTED[id]);
if (missing.length) {
  problems.push('Dashboard ids with NO writer in web/src and no entry in the ledger.\n' +
    'Either port them or record why not:\n' + missing.map((a) => '  #' + a).join('\n'));
}
const closed = Object.keys(UNPORTED).filter((id) => !unref.includes(id));
if (closed.length) {
  problems.push('These ids now HAVE a writer — remove them from the ledger so it does not\n' +
    'outlive what it described:\n' + closed.map((a) => '  #' + a).join('\n'));
}
const gone = Object.keys(UNPORTED).filter((id) => !ids.includes(id));
if (gone.length) {
  problems.push('The ledger names ids that are no longer in the markup:\n' +
    gone.map((a) => '  #' + a).join('\n'));
}

// The relay, which is why the room bookkeeping is currently inert. Recorded as a
// check rather than a note so closing it is forced to update this file.
const relayInTs = /dashcard:room:(focus|blur)['"]\s*,/.test(ts) &&
  /socket\.emit\(\s*['"]dashcard:(focus|blur)/.test(ts);
const goSrc = walk(path.join(ROOT, 'internal'), []).length ? '' : '';
void goSrc;
let relayInGo = false;
(function scanGo(dir) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) scanGo(p);
    else if (/\.go$/.test(e.name) && fs.readFileSync(p, 'utf8').includes('dashcard:focus')) relayInGo = true;
  }
}(path.join(ROOT, 'internal')));

if (relayInTs !== relayInGo) {
  problems.push('The dashcard room relay is half-built: TypeScript ' + (relayInTs ? 'has' : 'lacks') +
    ' it and Go ' + (relayInGo ? 'has' : 'lacks') + ' it. Both halves or neither — one alone is a ' +
    'room join that reaches nobody.');
}

const pct = Math.round(((ids.length - unref.length) / ids.length) * 100);
if (problems.length) {
  console.error('dash-coverage-check: %d problem(s)\n', problems.length);
  for (const p of problems) console.error(p + '\n');
  process.exit(1);
}
console.log('dash-coverage-check: %d of %d dashboard ids have a writer (%d%%); %d unported and all recorded',
  ids.length - unref.length, ids.length, pct, unref.length);
console.log('  relay: TypeScript %s, Go %s', relayInTs ? 'yes' : 'NO', relayInGo ? 'yes' : 'NO');
