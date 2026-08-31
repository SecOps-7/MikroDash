#!/usr/bin/env node
'use strict';
/**
 * Pin backup retention against the LIVE `selectForPruning`.
 *
 * WHY IT IS WORTH GATING. This function DELETES restore points. Every other
 * differential gate in this port protects a rendering or a payload; getting this
 * one wrong loses the artefact the whole feature exists to produce, and loses it
 * quietly — a pruned pair looks exactly like one that was never taken.
 *
 * THREE RULES HERE ARE EASY TO PORT WRONGLY, and each is covered below:
 *
 *   - **The newest pair is never removed**, even when it is older than keepDays.
 *     Pairs are written only when the configuration CHANGED, so the newest one is
 *     the current configuration however old it is. A router stable for longer
 *     than keepDays would otherwise age out its only restore point precisely
 *     because nothing has gone wrong.
 *   - **Both limits apply and the stricter wins.** keepCount bounds disk,
 *     keepDays bounds relevance; they answer different questions. A zero or
 *     missing limit is not applied at all.
 *   - **A malformed stem is never aged out by keepDays.** `_stemToMs` returns
 *     NaN and `NaN < cutoff` is false in JavaScript. A Go port comparing a zero
 *     or an error sentinel instead would delete it.
 *
 *   node tools/backup-prune-cases.js            write
 *   node tools/backup-prune-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = process.env.BACKUP_PRUNE_OUT ||
  path.join(__dirname, '..', 'testdata', 'backup-prune-cases.json');

const Store = require(path.join(LIVE, 'src', 'backups', 'store.js'));
if (typeof Store.selectForPruning !== 'function') {
  console.error('src/backups/store.js no longer exports selectForPruning.');
  process.exit(1);
}

// 2026-03-15T09:30:00Z, fixed so the cases never depend on when they were made.
const NOW = Date.parse('2026-03-15T09:30:00Z');
const DAY = 86400000;

/** A stem for `d` days before NOW, in the format the store writes. */
function stemAt(daysAgo) {
  const d = new Date(NOW - daysAgo * DAY);
  const p = (n) => String(n).padStart(2, '0');
  return d.getUTCFullYear() + '-' + p(d.getUTCMonth() + 1) + '-' + p(d.getUTCDate()) +
         'T' + p(d.getUTCHours()) + p(d.getUTCMinutes()) + p(d.getUTCSeconds());
}
const pair = (stem) => ({ stem, rscBytes: 100, backupBytes: 200 });

const cases = [];
const add = (name, pairs, opts, now) => {
  cases.push({ name, pairs, opts, now, want: Store.selectForPruning(pairs, opts, now) });
};

const five = [0, 1, 2, 3, 4].map((d) => pair(stemAt(d)));
const old = [0, 100, 200, 300].map((d) => pair(stemAt(d)));

add('no limits set keeps everything', five, {}, NOW);
add('keepCount 3 of 5', five, { keepCount: 3 }, NOW);
add('keepCount 1 keeps only the newest', five, { keepCount: 1 }, NOW);
add('keepCount larger than the set', five, { keepCount: 99 }, NOW);
add('keepCount 0 is not applied', five, { keepCount: 0, keepDays: 0 }, NOW);

add('keepDays 30 with everything recent', five, { keepDays: 30 }, NOW);
add('keepDays 30 ages out the old ones', old, { keepDays: 30 }, NOW);

// THE ONE THAT MATTERS MOST: every pair is older than keepDays. The newest must
// survive anyway — it is the current configuration.
add('ALL older than keepDays — the newest still survives',
    [400, 500, 600].map((d) => pair(stemAt(d))), { keepDays: 30 }, NOW);

add('a single pair is never pruned', [pair(stemAt(999))], { keepCount: 1, keepDays: 1 }, NOW);
add('no pairs at all', [], { keepCount: 1, keepDays: 1 }, NOW);

// The stricter of the two wins, and which is stricter flips between these.
add('both limits, keepCount stricter', old, { keepCount: 2, keepDays: 365 }, NOW);
add('both limits, keepDays stricter', old, { keepCount: 99, keepDays: 30 }, NOW);

// A malformed stem: NaN < cutoff is false, so keepDays never reaches it.
const mixed = [pair('not-a-timestamp'), ...old];
add('malformed stem is not aged out by keepDays', mixed, { keepDays: 30 }, NOW);
add('malformed stem sorts first and is spared as newest', mixed, { keepCount: 2 }, NOW);

// ── A REAL DEFECT, PINNED SO THE PORT REPRODUCES IT KNOWINGLY ──────────────
//
// The sort is a plain string comparison, descending, and 'n' > '2' — so a stem
// that is not a timestamp sorts ABOVE every real one and takes the
// never-remove-the-newest slot. With keepCount 1 that leaves the real newest
// unprotected and EVERY REAL BACKUP IS PRUNED.
//
// Reported to ToDo.md. Reproduced here rather than fixed, per the porting rule,
// and pinned so the reproduction is deliberate rather than accidental — when the
// live side fixes it, this case flips and says so.
const stray = [pair('not-a-timestamp'), ...[0, 1, 2].map((d) => pair(stemAt(d)))];
add('DEFECT: a stray stem lets keepCount 1 prune every real backup', stray, { keepCount: 1 }, NOW);

// ── A malformed stem that sorts LOW, which is what makes NaN observable ────
//
// Every malformed case above starts with a letter, so it sorts ABOVE the
// timestamps, lands at sorted[0] and is spared as "newest" whatever else
// decided. That masks the NaN rule completely: a port treating an unparseable
// stem as time 0 passes every one of them.
//
// '!' is 0x21, below '2', so this one sorts LAST. keepDays must still not reach
// it — `NaN < cutoff` is false — while a port comparing 0 would age it out at
// once. Found by mutation: without this case, that mutation survived.
const lowStray = [...[0, 100, 200].map((d) => pair(stemAt(d))), pair('!stray')];
add('a LOW-sorting malformed stem is still never aged out', lowStray, { keepDays: 30 }, NOW);
add('a LOW-sorting malformed stem CAN go by keepCount', lowStray, { keepCount: 2 }, NOW);

// Input order must not matter: the function sorts descending by stem.
add('unsorted input', [pair(stemAt(2)), pair(stemAt(0)), pair(stemAt(4)), pair(stemAt(1))],
    { keepCount: 2 }, NOW);

// Lenient number coercion, as `Number(x) || 0`.
add('string limits', old, { keepCount: '2', keepDays: '30' }, NOW);
add('junk limits are not applied', old, { keepCount: 'abc', keepDays: null }, NOW);
add('negative keepCount', five, { keepCount: -1 }, NOW);

const out = JSON.stringify({ now: NOW, cases }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== out) { console.error('backup-prune-cases.json is stale — run: node tools/backup-prune-cases.js'); process.exit(1); }
  console.log('backup-prune-cases.json is up to date (' + cases.length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, out);
  const pruning = cases.filter(c => c.want.length).length;
  console.log('wrote ' + OUT + ' — ' + cases.length + ' cases, ' + pruning + ' that prune something');
}
