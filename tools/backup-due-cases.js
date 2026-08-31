#!/usr/bin/env node
'use strict';
/**
 * Pin the backup scheduler's `isDue()` against the LIVE implementation.
 *
 * WHY THIS ONE. It decides whether a backup happens at all, and it failed
 * SILENTLY for as long as it was wrong: one router's daily schedule had never
 * fired once, and nothing said so — a backup that does not happen produces no
 * error, no row and no log line, only an absence somebody notices much later.
 *
 * THE TESTING TRAP IS THE REASON THIS EXISTS RATHER THAN A UNIT TEST. The live
 * tests all placed `lastRun` just before the previous day's target, so a full
 * interval had always elapsed by the next one and the elapsed-interval gate
 * could never hold a run back. The bug lived in the gap those cases did not
 * cover. Hand-written cases reproduce the blind spot along with the code, so the
 * cases below deliberately put `lastRun` LATE IN THE DAY — 11:45 on the previous
 * day, which is exactly the shape that broke it.
 *
 *   node tools/backup-due-cases.js            write
 *   node tools/backup-due-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = process.env.BACKUP_DUE_OUT ||
  path.join(__dirname, '..', 'testdata', 'backup-due-cases.json');

const Backups = require(path.join(LIVE, 'src', 'backups', 'index.js'));
const Routers = require(path.join(LIVE, 'src', 'routers.js'));

if (typeof Backups.isDue !== 'function') {
  console.error('src/backups/index.js no longer exports isDue — the port pins a function that moved.');
  process.exit(1);
}

const S = Routers.BACKUP_SCHEDULES;
if (!S || S.daily !== 86400000) {
  console.error('BACKUP_SCHEDULES changed; the port hard-codes these intervals.');
  process.exit(1);
}

// A fixed instant, so the cases never depend on when they were generated.
// 2026-03-15 09:30 UTC, a Sunday.
const NOW = Date.parse('2026-03-15T09:30:00Z');
const DAY = 86400000;

const router = (backup) => ({ id: 'r1', label: 'R', backup });

const cases = [];
const add = (name, r, lastRun, now, tz) => {
  cases.push({
    name, backup: r.backup, lastRun, now, tz,
    want: Backups.isDue(r, lastRun, now, S, tz),
  });
};

// ── The trap: lastRun LATE in the previous day ──────────────────────────────
// 11:45 yesterday, target 08:00 today. The elapsed interval is under 24h, so the
// old code refused; the anchor alone should permit it.
const yesterday1145 = Date.parse('2026-03-14T11:45:00Z');
add('daily 08:00, last run 11:45 yesterday — THE BUG',
    router({ enabled: true, schedule: 'daily', time: '08:00' }), yesterday1145, NOW, 'UTC');

// And once it fires late it must not stay late.
add('daily 08:00, last run 11:45 today — already ran',
    router({ enabled: true, schedule: 'daily', time: '08:00' }),
    Date.parse('2026-03-15T08:05:00Z'), NOW, 'UTC');

// ── The shape the live tests used, which could not fail ────────────────────
add('daily 08:00, last run just before yesterday target',
    router({ enabled: true, schedule: 'daily', time: '08:00' }),
    Date.parse('2026-03-14T07:59:00Z'), NOW, 'UTC');

// ── Not yet at the target ──────────────────────────────────────────────────
add('daily 22:00, not yet today',
    router({ enabled: true, schedule: 'daily', time: '22:00' }), yesterday1145, NOW, 'UTC');

// ── Off, never run, unknown schedule ───────────────────────────────────────
add('disabled', router({ enabled: false, schedule: 'daily', time: '08:00' }), yesterday1145, NOW, 'UTC');
add('no backup block at all', { id: 'r1', label: 'R' }, yesterday1145, NOW, 'UTC');
add('never run is due at once',
    router({ enabled: true, schedule: 'daily', time: '08:00' }), 0, NOW, 'UTC');
add('unknown schedule name',
    router({ enabled: true, schedule: 'fortnightly', time: '08:00' }), yesterday1145, NOW, 'UTC');

// ── time absent vs explicitly '' ───────────────────────────────────────────
// Absent takes the 08:00 default; '' means "any time" and keeps interval-only.
add('time ABSENT takes the default',
    router({ enabled: true, schedule: 'daily' }), yesterday1145, NOW, 'UTC');
add('time explicitly empty is interval-only',
    router({ enabled: true, schedule: 'daily', time: '' }), yesterday1145, NOW, 'UTC');
add('time empty, a full interval elapsed',
    router({ enabled: true, schedule: 'daily', time: '' }),
    NOW - DAY - 1000, NOW, 'UTC');
// A malformed time is not half-parsed.
add('time malformed', router({ enabled: true, schedule: 'daily', time: '25:00' }),
    yesterday1145, NOW, 'UTC');
add('time single-digit hour', router({ enabled: true, schedule: 'daily', time: '8:00' }),
    yesterday1145, NOW, 'UTC');

// ── hourly is never anchored ───────────────────────────────────────────────
add('hourly ignores the chosen time',
    router({ enabled: true, schedule: 'hourly', time: '08:00' }),
    NOW - 3600000 - 1000, NOW, 'UTC');
add('hourly within the interval',
    router({ enabled: true, schedule: 'hourly', time: '08:00' }),
    NOW - 60000, NOW, 'UTC');

// ── weekly and monthly KEEP the interval gate ──────────────────────────────
add('weekly, interval not elapsed',
    router({ enabled: true, schedule: 'weekly', time: '08:00' }), yesterday1145, NOW, 'UTC');
add('weekly, interval elapsed and past target',
    router({ enabled: true, schedule: 'weekly', time: '08:00' }),
    NOW - 604800000 - 1000, NOW, 'UTC');
add('monthly, interval elapsed',
    router({ enabled: true, schedule: 'monthly', time: '08:00' }),
    NOW - 2592000000 - 1000, NOW, 'UTC');

// ── Timezones, including one that puts "today" on a different date ─────────
for (const tz of ['UTC', 'Europe/Berlin', 'Pacific/Kiritimati', 'America/Los_Angeles', '']) {
  add('daily 08:00 in ' + (tz || '(blank zone)'),
      router({ enabled: true, schedule: 'daily', time: '08:00' }), yesterday1145, NOW, tz);
  add('daily 23:30 in ' + (tz || '(blank zone)'),
      router({ enabled: true, schedule: 'daily', time: '23:30' }), yesterday1145, NOW, tz);
}

// ── DST: the hour that does not exist, and the one that happens twice ──────
// Europe/Berlin springs forward 2026-03-29 02:00 -> 03:00.
const springNow = Date.parse('2026-03-29T08:00:00Z');
add('daily 02:30 across spring-forward',
    router({ enabled: true, schedule: 'daily', time: '02:30' }),
    Date.parse('2026-03-28T11:45:00Z'), springNow, 'Europe/Berlin');

const out = JSON.stringify({ schedules: S, defaultTime: Routers.BACKUP_DEFAULTS.time, cases }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== out) { console.error('backup-due-cases.json is stale — run: node tools/backup-due-cases.js'); process.exit(1); }
  console.log('backup-due-cases.json is up to date (' + cases.length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, out);
  console.log('wrote ' + OUT + ' — ' + cases.length + ' cases, ' +
              cases.filter(c => c.want).length + ' due');
}
