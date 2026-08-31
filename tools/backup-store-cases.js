#!/usr/bin/env node
'use strict';
/**
 * Pin the backup store's path helpers against the LIVE `src/backups/store.js`.
 *
 * WHY `slugFor` GETS ADVERSARIAL INPUT. It turns an OPERATOR-SUPPLIED router
 * label into a directory name, and its own comment states the property it
 * exists for: "a label can never escape the base directory". That is a path
 * traversal boundary, so the cases below include the shapes an attacker would
 * try — `../`, absolute paths, a NUL, a label that is only punctuation — rather
 * than only the labels a real fleet happens to have.
 *
 * The two lossy edges are pinned too, because they are the ones a "tidier" port
 * would quietly improve:
 *
 *   - a slug is TRIMMED of leading/trailing dashes and THEN cut to 60, so a cut
 *     landing on a dash leaves a trailing dash. The original does not re-trim.
 *   - a label slugging to nothing falls back to `router` rather than '', which
 *     would write into the base directory itself.
 *
 * `stemFor` is UTC on purpose: a local-time stem repeats itself for an hour
 * every autumn, and two backups that sort as equal are two backups that can
 * overwrite each other.
 *
 *   node tools/backup-store-cases.js            write
 *   node tools/backup-store-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = process.env.BACKUP_STORE_OUT ||
  path.join(__dirname, '..', 'testdata', 'backup-store-cases.json');

const S = require(path.join(LIVE, 'src', 'backups', 'store.js'));
for (const fn of ['slugFor', 'stemFor', 'dirFor', 'rscPath', 'backupPath', 'baseDir']) {
  if (typeof S[fn] !== 'function') {
    console.error('src/backups/store.js no longer exports ' + fn);
    process.exit(1);
  }
}

const labels = [
  'Mikrotik hAP AX3',
  'cAP AX',
  'hAP ac2',
  'ALL CAPS',
  'trailing spaces   ',
  '   leading spaces',
  'dots.and.dots',
  'under_scores',
  'hyphen-already',
  '123',
  'a',
  '',
  null,
  // Only punctuation: slugs to nothing, must fall back rather than write into
  // the base directory.
  '...',
  '---',
  '   ',
  '!!!',
  // ── Traversal shapes ──────────────────────────────────────────────────────
  '../../etc/passwd',
  '..',
  '../',
  '/absolute/path',
  'C:\\Windows\\System32',
  'a/../../b',
  './hidden',
  '.hidden',
  // A NUL and a newline, which some filesystems and some loggers treat oddly.
  'nul\u0000byte',
  'new\nline',
  'tab\there',
  // Non-ASCII: the regex has no `u` flag, so it works in UTF-16 units. A run of
  // them collapses to one dash either way, which is what makes the two agree.
  'Büro Router',
  'маршрутизатор',
  '路由器',
  'emoji \u{1F680} router',
  // ── The 60-character cut ──────────────────────────────────────────────────
  'x'.repeat(59),
  'x'.repeat(60),
  'x'.repeat(61),
  'x'.repeat(200),
  // A cut that lands ON a dash: trimming happens BEFORE the slice, so the slug
  // keeps a trailing dash. Reproduced rather than tidied.
  'x'.repeat(60) + ' tail',
  ('ab '.repeat(40)),
];

const slugs = labels.map((label) => ({ label, slug: S.slugFor(label) }));

// Paths are compared RELATIVE to baseDir(), because the absolute prefix depends
// on DATA_DIR and is not a property of the port.
const base = S.baseDir();
const rel = (p) => path.relative(base, p);

const paths = ['mikrotik-hap-ax3', 'router', 'a'].map((slug) => ({
  slug,
  dir: rel(S.dirFor(slug)),
  rsc: rel(S.rscPath(S.dirFor(slug), '2026-08-19T203521')),
  backup: rel(S.backupPath(S.dirFor(slug), '2026-08-19T203521')),
}));

const stems = [
  Date.parse('2026-08-19T20:35:21Z'),
  Date.parse('2026-01-01T00:00:00Z'),
  Date.parse('2026-12-31T23:59:59Z'),
  // Local-time zones that would shift the date if the stem were not UTC.
  Date.parse('2026-03-15T23:30:00Z'),
  Date.parse('2026-03-15T00:30:00Z'),
  0,
].map((ts) => ({ ts, stem: S.stemFor(ts) }));

// Round-trip: every stem this writes must be one `_stemToMs` can read back, or
// retention silently stops ageing out the pairs this app itself created.
const roundTrip = stems.map((s) => ({ stem: s.stem, ms: S._stemToMs(s.stem), ts: s.ts }));

const out = JSON.stringify({ slugs, paths, stems, roundTrip }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== out) { console.error('backup-store-cases.json is stale — run: node tools/backup-store-cases.js'); process.exit(1); }
  console.log('backup-store-cases.json is up to date (' + slugs.length + ' slugs)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, out);
  console.log('wrote ' + OUT + ' — ' + slugs.length + ' slugs, ' + paths.length +
              ' path sets, ' + stems.length + ' stems');
}
