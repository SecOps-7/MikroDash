#!/usr/bin/env node
'use strict';
/**
 * Pin `_normalizeBackup` against the LIVE implementation.
 *
 * WHY IT NEEDS A GATE. This is the WRITE side of the three-way contract the
 * scheduler reads. Every value distinguishes absent / explicitly-cleared / set,
 * and each gets it wrong differently:
 *
 *   an absent field reverting to a default  silently changes a schedule nobody
 *                                           touched, because update() rebuilds
 *                                           the record field by field
 *   `time: ""` taking the default           an operator who cleared the field
 *                                           watches 08:00 come back
 *   `keepCount: 0` taking the default       retention starts deleting restore
 *                                           points the operator asked to keep
 *   an unknown schedule taking the default  a typo moves a weekly backup to daily
 *
 * The password is compared only as PRESENT / ABSENT / SAME-AS-BEFORE. Its value
 * is random and never leaves the server, so the cases record whether one exists
 * rather than what it is.
 *
 *   node tools/backup-normalize-cases.js            write
 *   node tools/backup-normalize-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = process.env.BACKUP_NORM_OUT ||
  path.join(__dirname, '..', 'testdata', 'backup-normalize-cases.json');

const R = require(path.join(LIVE, 'src', 'routers.js'));
if (typeof R._normalizeBackup !== 'function') {
  console.error('src/routers.js no longer exports _normalizeBackup.');
  process.exit(1);
}

const PREV_PW = 'existing-password-value';
const prevFull = { enabled: true, schedule: 'weekly', time: '02:00',
                   keepCount: 7, keepDays: 30, password: PREV_PW };
const prevCleared = { enabled: true, schedule: 'daily', time: '',
                      keepCount: 0, keepDays: 0, password: PREV_PW };

const cases = [];
const add = (name, input, prev) => {
  const got = R._normalizeBackup(input, prev ? { backup: prev } : undefined);
  cases.push({
    name, input, prev: prev || null,
    want: got === undefined ? null : {
      enabled: got.enabled, schedule: got.schedule, time: got.time,
      keepCount: got.keepCount, keepDays: got.keepDays,
      // Value never recorded — see the header.
      password: !got.password ? 'none' : (got.password === PREV_PW ? 'carried' : 'generated'),
    },
  });
};

// ── absent fields keep what is stored ──────────────────────────────────────
add('empty patch over a full block', {}, prevFull);
add('empty patch with nothing stored', {}, null);
add('only enabled, over a full block', { enabled: false }, prevFull);

// ── time: absent vs cleared vs set ─────────────────────────────────────────
add('time absent keeps stored', { enabled: true }, prevFull);
add('time cleared is a real choice', { time: '' }, prevFull);
add('time set', { time: '23:45' }, prevFull);
add('time single-digit hour is padded', { time: '8:00' }, prevFull);
add('time invalid falls back', { time: '25:00' }, prevFull);
add('time garbage falls back', { time: 'later' }, prevFull);
add('time absent with nothing stored takes the default', {}, null);
add('time cleared stays cleared on the next write', { enabled: true }, prevCleared);

// ── keepCount / keepDays: 0 means no limit ─────────────────────────────────
add('keepCount zero is a real choice', { keepCount: 0 }, prevFull);
add('keepCount zero survives a later unrelated write', { enabled: true }, prevCleared);
add('keepCount absent keeps stored', { schedule: 'daily' }, prevFull);
add('keepCount as a string', { keepCount: '12' }, prevFull);
add('keepCount empty string falls back', { keepCount: '' }, prevFull);
add('keepCount garbage falls back', { keepCount: 'lots' }, prevFull);
add('keepCount fractional truncates', { keepCount: 12.9 }, prevFull);
add('keepCount negative clamps to 0', { keepCount: -5 }, prevFull);
add('keepCount over the cap clamps', { keepCount: 99999 }, prevFull);
add('keepDays over the cap clamps', { keepDays: 99999 }, prevFull);
add('keepDays negative clamps', { keepDays: -1 }, prevFull);

// ── schedule ───────────────────────────────────────────────────────────────
add('schedule valid', { schedule: 'monthly' }, prevFull);
add('schedule unknown keeps stored', { schedule: 'fortnightly' }, prevFull);
add('schedule unknown with nothing stored takes the default', { schedule: 'nope' }, null);

// ── enabled coercion ───────────────────────────────────────────────────────
add('enabled true', { enabled: true }, prevFull);
add('enabled the string true', { enabled: 'true' }, prevFull);
add('enabled a truthy non-true is FALSE', { enabled: 1 }, prevFull);
add('enabled false', { enabled: false }, prevFull);

// ── the password ───────────────────────────────────────────────────────────
add('enabling with nothing stored generates one', { enabled: true }, null);
add('enabling with one stored carries it', { enabled: true }, prevFull);
add('disabling keeps the stored password', { enabled: false }, prevFull);
add('a caller cannot set the password', { enabled: true, password: 'chosen-by-me' }, null);
add('disabled with nothing stored generates nothing', { enabled: false }, null);

// ── the block itself ───────────────────────────────────────────────────────
add('null input removes the block', null, prevFull);
add('undefined input keeps the block', undefined, prevFull);
add('a non-object input keeps the block', 'nope', prevFull);

const out = JSON.stringify({ cases }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== out) { console.error('backup-normalize-cases.json is stale'); process.exit(1); }
  console.log('backup-normalize-cases.json is up to date (' + cases.length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, out);
  console.log('wrote ' + OUT + ' — ' + cases.length + ' cases');
}
