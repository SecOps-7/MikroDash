#!/usr/bin/env node
'use strict';
/**
 * DOES ANYTHING IN THE PORT ACTUALLY READ THIS SETTING?
 *
 * ── WRITTEN AFTER THREE DEFECTS OF ONE SHAPE IN TWO DAYS ───────────────────
 *
 * A setting is rendered by the Settings page, validated by the write route,
 * persisted to settings.json — and then read by nobody. Every gate passes: the
 * form renders, the value round-trips, the payload matches. The operator changes
 * it and nothing happens, and the only way anyone finds out is by using the app.
 *
 *   - `topN` ("Top Connections N") was hardcoded to 10 in the collector, with no
 *     reader anywhere. REPORTED BY THE OPERATOR.
 *   - `topTalkersN` was passed as a literal 0 one line away, under a comment
 *     saying the port had no settings write yet — untrue since 2026-08-28.
 *   - `dbRetentionDays`, `dbAlertRetentionDays` and `dbAuditRetentionDays` were
 *     read by nobody because the port had no retention sweep at all, so the
 *     database grew without bound while the UI offered a policy.
 *
 * The first two cost an operator report each. The third was found by running
 * this check by hand. So it is a check.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/settings-consumer-audit.js
 *
 * ── WHAT COUNTS AS A READER, AND WHY IT IS THE QUOTED KEY ──────────────────
 *
 * A key is CONSUMED if its name appears as a quoted string somewhere that is not
 * a generated table, the disclosure list or a test. That is deliberately crude,
 * and crude in the safe direction: it cannot miss a real reader — every settings
 * lookup names the key — and it can only produce a false PASS, from a file that
 * mentions the key without acting on it.
 *
 * A stricter rule was tried and rejected. Matching the Go FIELD name would have
 * called `topN` consumed, because `connections.go` had a `topN` field it never
 * filled from settings. The defect was a name that existed everywhere and a
 * VALUE that flowed nowhere.
 *
 * ── WHAT IT CATCHES, AND WHAT IT DEMONSTRABLY DOES NOT ────────────────────
 *
 * MEASURED BY MUTATION on 2026-08-29, and the result is worth stating plainly
 * rather than leaving a reader to assume the check is stronger than it is.
 *
 * IT CATCHES the `dbRetentionDays` shape: a key nothing anywhere mentions. All
 * three retention settings looked exactly like that, and so would any new
 * setting added to the form without a backend.
 *
 * IT DOES NOT CATCH the `topN` shape. Reverting that fix leaves this check
 * GREEN, because `topN` is still named in `collection.go`'s settings-fingerprint
 * key list — a place that reacts to the value CHANGING without ever applying it.
 * Excluding the clamp-bounds table removed one such false reader; the
 * fingerprint list is a real Go file doing real work, and no honest heuristic
 * separates "names the key in a list" from "applies the value".
 *
 * So: a floor, and a floor that would not have caught the operator's own report.
 * The thing that catches the `topN` shape is a test at the CALL SITE — see
 * `TestBothCountsAreActuallyWiredIn` — and this audit is the cheaper net beneath
 * it, not a replacement for it.
 */

const fs = require('node:fs');
const path = require('node:path');

const say = console.log.bind(console);
const HERE = path.join(__dirname, '..');

// Keys with no reader, each with the reason it is allowed to have none. A key
// may only sit here with a reason a maintainer can check — "not used" is not one.
const RECORDED = {
  // ── EVERY LEGACY KEY IS NOW READ ──────────────────────────────────────
  //
  // The five single-router fields and `collectionMigrated` were all recorded
  // here as reader-less while porting the two one-shot migrations was the
  // operator's call. They decided to port both on 2026-08-30;
  // `internal/store/legacyseed.go` reads the five and
  // `internal/store/legacymigrate.go` reads the flag, so all six entries became
  // stale claims and this audit failed on the sweep after each half landed —
  // twice, on its author, which is what `staleRecords` is for.
  //

  // ── DEAD IN THE LIVE APP TOO, WHICH IS WHY THE PORT IGNORES IT ──────────
  //
  // MEASURED 2026-08-29: `firewallTopN` appears in the live tree ONLY in its
  // default, its clamp bounds and the two settings-form key lists. No collector,
  // no renderer, nothing. The Firewall page's Top N box is a control that has
  // never done anything on either side.
  //
  // So the port is CORRECT to ignore it — reproducing behaviour includes
  // reproducing a control that does nothing, and wiring it up here would make
  // the ported page behave differently from the app it replaces. Reported to
  // `../MikroDash/ToDo.md` so it can be fixed at the source, which is the only
  // place a fix belongs.
  firewallTopN: 'DEAD UPSTREAM TOO — live has no consumer either; reported in ToDo.md',

};

const tablesPath = path.join(HERE, 'internal', 'store', 'settings_tables.json');
if (!fs.existsSync(tablesPath)) {
  say(`settings-consumer-audit: SKIP — no ${tablesPath}`);
  process.exit(0);
}
const defaults = JSON.parse(fs.readFileSync(tablesPath, 'utf8')).defaults || {};
const keys = Object.keys(defaults).sort();
if (!keys.length) {
  say('settings-consumer-audit: the generated defaults are empty; this measures nothing');
  process.exit(1);
}

// Where a mention does NOT count: the generated tables themselves, the
// disclosure allow-list (which names every viewer-readable key by definition and
// would mark most of the file consumed), and tests.
const IGNORED = [
  path.join('internal', 'store', 'settings_tables.json'),
  // The CLAMP BOUNDS table. Naming a key here says what range it is validated
  // to, which is a property of the WRITE path — it is not evidence that anything
  // acts on the value. Leaving it in the haystack is what made `topN` look
  // consumed while the collector ignored it.
  path.join('internal', 'store', 'settings_write_tables.json'),
  path.join('internal', 'store', 'disclose.go'),
  path.join('web', 'src', 'gen') + path.sep,
];
const isIgnored = (rel) =>
  IGNORED.some((p) => rel === p || rel.startsWith(p)) ||
  /_test\.go$|\.test\.[jt]s$|\.spec\.[jt]s$/.test(rel);

const sources = [];
const walk = (dir) => {
  if (!fs.existsSync(dir)) return;
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, e.name);
    if (e.isDirectory()) { walk(full); continue; }
    // `.json` IS INCLUDED, and leaving it out was this audit's own first bug.
    // Every `poll*` key is consumed through `internal/collection/collection_tables.json`
    // — the generated registry whose `pollKey` column `collection.go` reads — so a
    // filter of .go/.ts/.js/.html reported forty-two false findings on the first
    // run. A check that cries wolf about a third of the settings file is worse
    // than no check, because the next real one is read as more noise.
    if (!/\.(go|ts|js|html|json)$/.test(e.name)) continue;
    const rel = path.relative(HERE, full);
    if (isIgnored(rel)) continue;
    sources.push(fs.readFileSync(full, 'utf8'));
  }
};
// THE APP ONLY — `tools/` is deliberately NOT searched, and that was this
// audit's second bug. With it included, the RECORDED list below counts as a
// reader for every key it names, so the exceptions satisfy themselves and the
// check silently stops asking about exactly the keys a maintainer flagged.
for (const d of ['internal', 'web/src', 'cmd']) walk(path.join(HERE, d));
const haystack = sources.join('\n');

const unread = keys.filter((k) => !haystack.includes(`"${k}"`) && !haystack.includes(`'${k}'`));
const unexplained = unread.filter((k) => !RECORDED[k]);
// A recorded exception that has since GAINED a reader must be deleted, or this
// list becomes a place where stale claims accumulate — the failure mode this
// project keeps finding in its own prose.
const staleRecords = Object.keys(RECORDED).filter((k) => !unread.includes(k));

say(`settings-consumer-audit: ${keys.length} settings keys, ${unread.length} with no reader ` +
    `(${Object.keys(RECORDED).length} recorded)`);

let bad = false;
for (const k of unexplained) {
  bad = true;
  say(`  ✗ ${k} is rendered, validated and persisted, and NOTHING in this port reads it.`);
}
for (const k of staleRecords) {
  bad = true;
  say(`  ✗ ${k} is recorded as having no reader, but something reads it now — ` +
      'delete the entry rather than leaving a stale claim.');
}
if (bad) {
  say('');
  say('An operator can set this and nothing will happen. That has cost two operator');
  say('reports already (topN, topTalkersN) and one unbounded database (dbRetentionDays).');
  say('Either wire it up, or add it to RECORDED with the reason it needs no reader.');
  process.exit(1);
}
say('every settings key is either read or recorded');
