'use strict';
/**
 * Configuration backups: normalisation, diffing, retention, the runner's
 * conversation with a router, and the structural invariants that keep a
 * restore point out of reach of a retention sweep.
 *
 * The runner is exercised against a fake ROS, following the pattern in
 * resource-writes.test.js. What it must get right is not "did it call
 * /export" but the decisions around it: skip when flash is short, store only
 * when the configuration changed, and never leave a file on the router.
 */
const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');

const Diff = require('../src/backups/diff');
const Store = require('../src/backups/store');
const Runner = require('../src/backups/runner');
const Scheduler = require('../src/backups');

// ── A real export's shape, minus 36,000 lines of it ─────────────────────────
const HEADER = '# 2026-08-19 20:35:21 by RouterOS 7.24\r\n' +
               '# software id = XXXX-XXXX\r\n' +
               '#\r\n' +
               '# model = C53UiG+5HPaxD2HPaxD\r\n' +
               '# serial number = SYNTHETIC01\r\n';
const BODY = '/interface bridge\r\n' +
             'add name=Bridge vlan-filtering=yes\r\n' +
             '/ip address\r\n' +
             'add address=192.0.2.1/24 interface=Bridge\r\n';
const EXPORT = HEADER + BODY;

function tmpdir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'bk-test-'));
}

// ── Normalisation: the thing that decides whether drift is real ────────────

test('the volatile header line is not part of the configuration', () => {
  // Same config, taken a day later on a newer RouterOS.
  const later = EXPORT.replace('# 2026-08-19 20:35:21 by RouterOS 7.24',
                               '# 2026-09-01 04:00:00 by RouterOS 7.25');
  assert.equal(Diff.fingerprint(EXPORT), Diff.fingerprint(later),
    'a new timestamp or version must not read as drift');
  assert.equal(Diff.diff(EXPORT, later).changed, false);
});

test('but the model and serial ARE part of it', () => {
  // These do not change on their own. If one does, this is not the same device
  // and continuity should not be implied.
  const swapped = EXPORT.replace('SYNTHETIC01', 'SYNTHETIC02');
  assert.notEqual(Diff.fingerprint(EXPORT), Diff.fingerprint(swapped));
});

test('a real one-line change is drift', () => {
  const changed = EXPORT.replace('address=192.0.2.1/24', 'address=192.0.2.2/24');
  const d = Diff.diff(EXPORT, changed);
  assert.equal(d.changed, true);
  assert.equal(d.added, 1);
  assert.equal(d.removed, 1);
  assert.equal(d.hunks.length, 1);
  const added = d.hunks[0].lines.filter(l => l.op === '+').map(l => l.text);
  assert.ok(added[0].includes('192.0.2.2'), 'the hunk must name what changed');
});

test('CRLF and LF are the same configuration', () => {
  assert.equal(Diff.fingerprint(EXPORT), Diff.fingerprint(EXPORT.replace(/\r\n/g, '\n')));
});

test('identical input produces no hunks at all', () => {
  const d = Diff.diff(EXPORT, EXPORT);
  assert.deepEqual(d, { changed: false, added: 0, removed: 0, hunks: [], truncated: false });
});

// ── Storage and retention ──────────────────────────────────────────────────

test('a pair is written, read back, and removed together', () => {
  const dir = tmpdir();
  try {
    const w = Store.writePair(dir, '2026-08-19T203521', EXPORT, Buffer.from([0, 255, 128, 7]));
    assert.ok(w.rscBytes > 0 && w.backupBytes === 4);
    assert.equal(Store.readRsc(dir, '2026-08-19T203521'), EXPORT);
    assert.deepEqual([...Store.readBackup(dir, '2026-08-19T203521')], [0, 255, 128, 7],
      'the binary must come back byte for byte');
    assert.equal(Store.listPairs(dir).length, 1);
    Store.removePair(dir, '2026-08-19T203521');
    assert.equal(Store.listPairs(dir).length, 0);
  } finally { fs.rmSync(dir, { recursive: true, force: true }); }
});

test('half a pair is not a backup', () => {
  const dir = tmpdir();
  try {
    // A crash between the two writes leaves the binary with no export.
    fs.writeFileSync(path.join(dir, '2026-08-19T203521.backup'), Buffer.from([1]));
    assert.equal(Store.listPairs(dir).length, 0,
      'a lone half must not be offered as a restore point');
  } finally { fs.rmSync(dir, { recursive: true, force: true }); }
});

test('removing a pair twice is not an error', () => {
  const dir = tmpdir();
  try {
    Store.writePair(dir, '2026-08-19T203521', EXPORT, Buffer.from([1]));
    assert.equal(Store.removePair(dir, '2026-08-19T203521'), 2);
    assert.equal(Store.removePair(dir, '2026-08-19T203521'), 0,
      'pruning runs after a crash as readily as after a success');
  } finally { fs.rmSync(dir, { recursive: true, force: true }); }
});

test('a label cannot escape the backup directory', () => {
  for (const evil of ['../../etc', '/etc/passwd', '..', '....//']) {
    const slug = Store.slugFor(evil);
    assert.ok(!slug.includes('/') && !slug.includes('..'), evil + ' slugged to ' + slug);
    assert.ok(Store.dirFor(slug).startsWith(Store.baseDir()));
  }
  assert.equal(Store.slugFor('...'), 'router', 'a label that slugs to nothing needs a name');
});

test('stems sort chronologically as plain strings', () => {
  const a = Store.stemFor(Date.UTC(2026, 7, 19, 20, 35, 21));
  const b = Store.stemFor(Date.UTC(2026, 8, 1, 4, 0, 0));
  assert.equal(a, '2026-08-19T203521');
  assert.ok(a < b);
  assert.equal(Store._stemToMs(a), Date.UTC(2026, 7, 19, 20, 35, 21));
});

test('two runs in the same minute get different stems', () => {
  const a = Store.stemFor(Date.UTC(2026, 7, 19, 20, 35, 21));
  const b = Store.stemFor(Date.UTC(2026, 7, 19, 20, 35, 47));
  assert.notEqual(a, b, 'seconds are what stop a manual run overwriting a scheduled one');
});

test('keepCount prunes the oldest and keepDays the stalest', () => {
  const now = Date.UTC(2026, 7, 20, 0, 0, 0);
  const pairs = [
    { stem: '2026-08-19T000000' },   // 1 day old
    { stem: '2026-08-15T000000' },   // 5 days
    { stem: '2026-08-01T000000' },   // 19 days
    { stem: '2026-06-01T000000' },   // 80 days
  ];
  assert.deepEqual(Store.selectForPruning(pairs, { keepCount: 2 }, now),
    ['2026-08-01T000000', '2026-06-01T000000']);
  assert.deepEqual(Store.selectForPruning(pairs, { keepDays: 10 }, now),
    ['2026-08-01T000000', '2026-06-01T000000']);
  // Both apply, and the stricter one wins.
  assert.deepEqual(Store.selectForPruning(pairs, { keepCount: 3, keepDays: 10 }, now),
    ['2026-08-01T000000', '2026-06-01T000000']);
  assert.deepEqual(Store.selectForPruning(pairs, {}, now), [],
    'no limits means no pruning');
});

test('the newest pair is never pruned, however old it is', () => {
  const now = Date.UTC(2026, 7, 20);
  const pairs = [{ stem: '2020-01-01T000000' }];
  assert.deepEqual(Store.selectForPruning(pairs, { keepCount: 1, keepDays: 1 }, now), [],
    'a stable router would otherwise age out its only restore point');
  // And with two, the newest still survives its own staleness.
  const two = [{ stem: '2020-01-01T000000' }, { stem: '2019-01-01T000000' }];
  assert.deepEqual(Store.selectForPruning(two, { keepDays: 1 }, now), ['2019-01-01T000000']);
});

// ── The runner ─────────────────────────────────────────────────────────────

/** A router that answers like RouterOS, with the flash and files we choose. */
function fakeRos({ freeBytes = 64 * 1048576, files = {}, fail = null } = {}) {
  const calls = [];
  const store = Object.assign({}, files);
  return {
    calls,
    stopped: false,
    stop() { this.stopped = true; },
    async write(cmd, args) {
      calls.push(cmd + ' ' + (args || []).join(' '));
      if (fail && fail(cmd)) throw new Error('router said no to ' + cmd);
      const arg = (name) => {
        const hit = (args || []).find(a => a.startsWith('=' + name + '='));
        return hit ? hit.slice(name.length + 2) : '';
      };
      if (cmd === '/system/resource/print') {
        return [{ 'board-name': 'TestBoard', version: '7.24 (stable)',
                  'free-hdd-space': String(freeBytes), 'total-hdd-space': '134217728' }];
      }
      if (cmd === '/system/routerboard/print') return [{ 'serial-number': 'SYNTHETIC01' }];
      if (cmd === '/file/print') {
        return Object.keys(store).map(n => ({ name: n, size: String(store[n].length) }));
      }
      if (cmd === '/export') { store[arg('file') + '.rsc'] = Buffer.from(EXPORT, 'utf8'); return []; }
      if (cmd === '/system/backup/save') {
        assert.ok(arg('password'), 'a backup must never be written unencrypted');
        assert.equal(arg('encryption'), 'aes-sha256');
        store[arg('name') + '.backup'] = Buffer.from([0, 1, 255, 254, 0]);
        return [];
      }
      if (cmd === '/file/read') {
        const buf = store[arg('file')];
        if (!buf) throw new Error('no such file');
        const off = Number(arg('offset')), size = Number(arg('chunk-size'));
        return [{ data: buf.slice(off, off + size).toString('latin1') }];
      }
      if (cmd === '/file/remove') { delete store[arg('numbers')]; return []; }
      return [];
    },
    remaining: () => Object.keys(store),
  };
}

const ROUTER = { id: 'r1', label: 'Test Router', backup: { password: 'synthetic-pw' } };

// The runner writes a real pair through Store, and Store resolves its base from
// DATA_DIR. Without this the suite wrote into the live /data/config-backups and
// left a `test-router` directory on a production instance — which is exactly
// how that was found.
const _REAL_DATA_DIR = process.env.DATA_DIR;
const _TEST_DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'bk-data-'));
test.before(() => { process.env.DATA_DIR = _TEST_DATA_DIR; });
test.after(() => {
  if (_REAL_DATA_DIR === undefined) delete process.env.DATA_DIR;
  else process.env.DATA_DIR = _REAL_DATA_DIR;
  fs.rmSync(_TEST_DATA_DIR, { recursive: true, force: true });
});

test('the suite never writes to the real data directory', () => {
  assert.ok(Store.baseDir().startsWith(_TEST_DATA_DIR),
    'a test must not be able to touch /data/config-backups');
});

test('a first backup reads both files and stores a pair', async () => {
  const ros = fakeRos();
  const r = await Runner.run({ router: ROUTER, connect: async () => ros, log: () => {} });
  try {
    assert.equal(r.outcome, 'changed', r.error || '');
    assert.ok(r.fingerprint);
    assert.ok(r.rscBytes > 0 && r.backupBytes === 5);
    assert.deepEqual(ros.remaining(), [], 'the router must be left with no temp files');
    assert.equal(ros.stopped, true, 'the dedicated connection must be closed');
  } finally { if (r.dir) Store.removePair(r.dir, r.stem); }
});

test('an unchanged configuration stores nothing and never writes a binary', async () => {
  const first = await Runner.run({ router: ROUTER, connect: async () => fakeRos(), log: () => {} });
  try {
    const ros2 = fakeRos();
    const r = await Runner.run({ router: ROUTER, connect: async () => ros2,
                                 previousFingerprint: first.fingerprint, log: () => {} });
    assert.equal(r.outcome, 'unchanged');
    assert.equal(r.backupBytes, 0);
    assert.ok(!ros2.calls.some(c => c.startsWith('/system/backup/save')),
      'the expensive half must not run when nothing changed');
  } finally { if (first.dir) Store.removePair(first.dir, first.stem); }
});

test('a small router is attempted, not pre-emptively refused', async () => {
  // The regression this pins: an 8 MB threshold, extrapolated from one busy
  // AX3, refused a hAP ac2 whose entire backup is 45.7 KiB. Backup size tracks
  // configuration, not hardware, so no constant can stand in for the router's
  // own answer.
  const ros = fakeRos({ freeBytes: 2 * 1048576 });
  const r = await Runner.run({ router: ROUTER, connect: async () => ros, log: () => {} });
  assert.equal(r.outcome, 'changed', r.error || '');
  assert.ok(ros.calls.some(c => c.startsWith('/export')),
    'the router decides whether it has room, by being asked');
  if (r.dir) Store.removePair(r.dir, r.stem);
});

test('a router that has no room reports its own refusal, and cleans up', async () => {
  // What a full flash actually looks like: RouterOS refuses the write and says
  // why. That message is more use than any threshold we could have guessed.
  const ros = fakeRos({ freeBytes: 4096, fail: (cmd) => cmd === '/export' });
  const r = await Runner.run({ router: ROUTER, connect: async () => ros, log: () => {} });
  assert.equal(r.outcome, 'failed');
  assert.match(r.error, /router said no to \/export/);
  assert.deepEqual(ros.remaining(), [],
    'a refused attempt must not leave a partial file behind');
});

test('files left by an earlier crashed run are swept', async () => {
  const ros = fakeRos({ files: { 'mikrodash-backup-2026-01-01T000000.rsc': Buffer.from('old') } });
  const r = await Runner.run({ router: ROUTER, connect: async () => ros, log: () => {} });
  try {
    assert.equal(r.outcome, 'changed');
    assert.deepEqual(ros.remaining(), []);
  } finally { if (r.dir) Store.removePair(r.dir, r.stem); }
});

test('a router that refuses still leaves nothing behind', async () => {
  const ros = fakeRos({ fail: (cmd) => cmd === '/system/backup/save' });
  const r = await Runner.run({ router: ROUTER, connect: async () => ros, log: () => {} });
  assert.equal(r.outcome, 'failed');
  assert.ok(r.error);
  assert.deepEqual(ros.remaining(), [], 'the export must be cleaned up even on failure');
});

test('a backup is refused outright when no password is configured', async () => {
  const ros = fakeRos();
  const r = await Runner.run({ router: { id: 'r1', label: 'x', backup: {} },
                               connect: async () => ros, log: () => {} });
  assert.equal(r.outcome, 'failed');
  assert.match(r.error, /password/);
  assert.ok(!ros.calls.some(c => c.startsWith('/system/backup/save')),
    'an unencrypted backup must never be the fallback');
});

test('a short read is an error, not a shorter backup', async () => {
  // The router goes quiet part-way through the file. Length is the only
  // integrity check available, so a truncated backup must refuse rather than
  // be stored as one that restores into a half-configured device.
  const ros = fakeRos();
  const real = ros.write.bind(ros);
  let reads = 0;
  ros.write = async (cmd, args) => {
    const out = await real(cmd, args);
    if (cmd === '/file/read' && ++reads > 1 && out[0]) out[0].data = '';
    return out;
  };
  const r = await Runner.run({ router: ROUTER, connect: async () => ros, log: () => {} });
  assert.equal(r.outcome, 'failed');
  assert.match(r.error, /read \d+ of \d+ bytes/);
  assert.deepEqual(ros.remaining(), [], 'and still cleans up after itself');
});

test('a file delivered in small chunks is reassembled, not rejected', () => {
  // The mirror of the test above: fewer bytes per read than asked for is
  // normal, and must not be mistaken for truncation.
  const buf = Buffer.from('0123456789');
  let off = 0;
  const write = async (cmd, args) => {
    const o = Number(args.find(a => a.startsWith('=offset=')).slice(8));
    return [{ data: buf.slice(o, o + 3).toString('latin1') }];
  };
  return Runner._readFile(write, 'x', buf.length).then((out) => {
    assert.equal(out.toString(), '0123456789');
    assert.equal(off, 0);
  });
});

// ── Scheduling ─────────────────────────────────────────────────────────────

const SCHEDULES = { hourly: 3600000, daily: 86400000, weekly: 604800000, monthly: 2592000000 };

test('due-ness follows the schedule, and a router that never ran is due now', () => {
  const now = 1000000000;
  const daily = { backup: { enabled: true, schedule: 'daily' } };
  assert.equal(Scheduler.isDue(daily, 0, now, SCHEDULES), true, 'never run means due');
  assert.equal(Scheduler.isDue(daily, now - 3600000, now, SCHEDULES), false);
  assert.equal(Scheduler.isDue(daily, now - 86400000, now, SCHEDULES), true);
  assert.equal(Scheduler.isDue({ backup: { enabled: false, schedule: 'daily' } }, 0, now, SCHEDULES),
    false, 'disabled is never due');
  assert.equal(Scheduler.isDue({}, 0, now, SCHEDULES), false, 'no block is never due');
  assert.equal(Scheduler.isDue({ backup: { enabled: true, schedule: 'never' } }, 0, now, SCHEDULES),
    false, 'an unknown schedule is not a licence to run constantly');
});

test('daily is the default schedule, and nothing is enabled by an upgrade', () => {
  const Routers = require('../src/routers');
  assert.equal(Routers.BACKUP_DEFAULTS.schedule, 'daily');
  assert.equal(Routers.BACKUP_DEFAULTS.enabled, false);
});

// ── Backup time ─────────────────────────────────────────────────────────────
//
// Scheduling was purely "has `interval` elapsed since the last run", which drifts
// by however long each run took and lands wherever the first one happened to.
// A chosen time anchors it to the wall clock instead.

const TZ = 'Europe/Berlin';                       // +02:00 in August, so 02:00 local is 00:00Z
const at = (iso) => Date.parse(iso);
const daily = (time) => ({ backup: { enabled: true, schedule: 'daily', time } });

test('an explicitly cleared time means any time, and keeps interval behaviour', () => {
  // '' is a real choice the operator can make, distinct from never having chosen.
  // If the two collapsed, clearing the field would read back as unset and the
  // default would silently reappear on the next tick.
  assert.equal(Scheduler.isDue(daily(''), at('2026-08-19T11:00:00Z'), at('2026-08-20T10:00:00Z'),
    SCHEDULES, TZ), false, '23h is not a day');
  assert.equal(Scheduler.isDue(daily(''), at('2026-08-19T09:00:00Z'), at('2026-08-20T10:00:00Z'),
    SCHEDULES, TZ), true, '25h is');
});

test('a router that never chose a time takes the default', () => {
  const Routers = require('../src/routers');
  assert.equal(Routers.BACKUP_DEFAULTS.time, '08:00');

  // No `time` key at all — the shape of a backup block written before the field
  // existed, which is what an upgraded install looks like.
  const never = { backup: { enabled: true, schedule: 'daily' } };
  assert.equal(Scheduler.isDue(never, at('2026-08-19T06:05:00Z'), at('2026-08-20T05:00:00Z'),
    SCHEDULES, TZ), false, '07:00 local is before the 08:00 default');
  assert.equal(Scheduler.isDue(never, at('2026-08-19T06:05:00Z'), at('2026-08-20T06:05:00Z'),
    SCHEDULES, TZ), true, '08:05 local is after it');
});

test('a daily backup at 02:00 waits for 02:00', () => {
  assert.equal(Scheduler.isDue(daily('02:00'), at('2026-08-19T00:05:00Z'),
    at('2026-08-19T23:00:00Z'), SCHEDULES, TZ), false, '01:00 local is before the target');
  assert.equal(Scheduler.isDue(daily('02:00'), at('2026-08-19T00:05:00Z'),
    at('2026-08-20T00:05:00Z'), SCHEDULES, TZ), true, '02:05 local is after it');
});

test('it runs once a day, not once per tick after the target', () => {
  // The scheduler ticks every five minutes; without the `lastRun < target` half
  // it would fire on every one of them from 02:00 until midnight.
  assert.equal(Scheduler.isDue(daily('02:00'), at('2026-08-20T00:05:00Z'),
    at('2026-08-20T12:00:00Z'), SCHEDULES, TZ), false, 'already ran at 02:05 today');
});

test('a router that was off at 02:00 catches up rather than skipping the day', () => {
  assert.equal(Scheduler.isDue(daily('02:00'), at('2026-08-17T00:05:00Z'),
    at('2026-08-20T13:00:00Z'), SCHEDULES, TZ), true,
    'three days without a backup must not wait for tomorrow');
});

test('hourly ignores the time', () => {
  // An hourly backup that waits for 02:00 is a daily backup.
  const hourly = { backup: { enabled: true, schedule: 'hourly', time: '02:00' } };
  assert.equal(Scheduler.isDue(hourly, at('2026-08-20T12:00:00Z'),
    at('2026-08-20T13:01:00Z'), SCHEDULES, TZ), true);
});

test('weekly still waits a week, then honours the time', () => {
  const weekly = (time) => ({ backup: { enabled: true, schedule: 'weekly', time } });
  assert.equal(Scheduler.isDue(weekly('02:00'), at('2026-08-13T00:05:00Z'),
    at('2026-08-19T23:10:00Z'), SCHEDULES, TZ), false, 'six days and change is not a week');
  assert.equal(Scheduler.isDue(weekly('02:00'), at('2026-08-13T00:05:00Z'),
    at('2026-08-20T00:05:00Z'), SCHEDULES, TZ), true);
});

test('the time is stored as a real clock time or not at all', () => {
  const Routers = require('../src/routers');
  const keep = (v) => Routers._normalizeBackup({ enabled: true, schedule: 'daily', time: v }, null).time;
  assert.equal(keep('02:00'), '02:00');
  assert.equal(keep('2:05'), '02:05', 'a single-digit hour is padded, not rejected');
  assert.equal(keep('23:59'), '23:59');
  assert.equal(keep(''), '', 'empty is a real choice: any time');
  // Half-parsing would schedule a backup at an hour nobody chose, silently, so
  // anything unparseable falls back to the default rather than being coerced.
  const Def = require('../src/routers').BACKUP_DEFAULTS.time;
  assert.equal(keep('24:00'), Def, 'there is no 24:00');
  assert.equal(keep('2:5'), Def, 'not a clock time');
  assert.equal(keep('nonsense'), Def);
});

test('the timezone is read per tick, not captured at startup', () => {
  // The operator can change the display timezone without restarting; a schedule
  // anchored to the old one would fire an hour out with nothing to explain it.
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');
  assert.ok(/getTimezone: \(\) => Settings\.load\(\)\.displayTimezone/.test(src));
});


test('an edit that does not mention backups leaves them alone', () => {
  const Routers = require('../src/routers');
  const existing = { backup: { enabled: true, schedule: 'weekly', keepCount: 5,
                               keepDays: 30, password: 'kept' } };
  assert.deepEqual(Routers._normalizeBackup(undefined, existing), existing.backup);
  assert.equal(Routers._normalizeBackup(null, existing), undefined, 'null is an explicit reset');
  const edited = Routers._normalizeBackup({ enabled: true, schedule: 'daily' }, existing);
  assert.equal(edited.password, 'kept',
    'regenerating the password would orphan every stored .backup');
  assert.equal(edited.keepCount, 5, 'unmentioned limits are carried forward');
});

test('a caller cannot choose the backup password', () => {
  const Routers = require('../src/routers');
  const out = Routers._normalizeBackup({ enabled: true, password: 'attacker-chosen' }, null);
  assert.notEqual(out.password, 'attacker-chosen');
  assert.ok(out.password.length >= 24, 'it must be generated, and long');
});

// ── Structural invariants ──────────────────────────────────────────────────

const SRC = (name) => fs.readFileSync(path.join(__dirname, '..', 'src', name), 'utf8');
const DB_SRC = SRC('db.js');
const INDEX_SRC = SRC('index.js');

test('a retention sweep can never delete a restore point', () => {
  // Same guarantee audit_events has, and for a stronger reason: these are the
  // artefacts you reach for when something has already gone wrong.
  const purge = DB_SRC.slice(DB_SRC.indexOf('const PURGE_TABLES'),
                             DB_SRC.indexOf('const PURGE_TYPES'));
  assert.ok(!purge.includes('config_backups'),
    'config_backups must not be reachable from PURGE_TABLES');

  const start = DB_SRC.indexOf('function deleteRouterData');
  const del = DB_SRC.slice(start, DB_SRC.indexOf('}', DB_SRC.indexOf('console.log', start)));
  assert.ok(!del.includes('config_backups'),
    'removing a router must not delete its last known-good configuration');
});

test('pruning clears the artefact, never the run', () => {
  assert.ok(DB_SRC.includes('UPDATE config_backups SET pruned_at'),
    'the history of when a router was checked outlives its files');
});

test('the only way to delete a backup row is one id at a time', () => {
  // This used to be a blanket ban on DELETE FROM config_backups. An operator
  // pressing Delete now removes the row as well as the files — a tombstone
  // reading "pruned" is retention's answer, not the answer to somebody saying
  // "I do not want this listed". What must NOT come back is a sweep, so the
  // guard is now about the SHAPE of the delete rather than its existence.
  const deletes = DB_SRC.match(/DELETE FROM config_backups[^']*/g) || [];
  assert.equal(deletes.length, 1, 'exactly one delete statement, no more');
  assert.match(deletes[0], /WHERE id = \?$/,
    'scoped to a single row by id — never by router, by age, or unscoped');
});

test('retention and router removal still never delete a row', () => {
  // The two paths that run without anybody asking. A backup row outliving its
  // router is the point: it is the last record of that device's configuration.
  const prune = SRC('backups/index.js');
  assert.ok(/markBackupPruned/.test(prune) && !/deleteBackup/.test(prune),
    'retention marks pruned and must not start deleting');
  const start = DB_SRC.indexOf('function deleteRouterData');
  const del = DB_SRC.slice(start, DB_SRC.indexOf('}', DB_SRC.indexOf('console.log', start)));
  assert.ok(!/config_backups/.test(del), 'and removing a router leaves the rows alone');
});

test('the backup password never reaches a browser', () => {
  const routers = SRC('routers.js');
  const pub = routers.slice(routers.indexOf('function getPublic'),
                            routers.indexOf('function getPublic') + 700);
  assert.ok(pub.includes('hasPassword'),
    'getPublic must replace the backup password with a flag, not mask it');
  const payload = INDEX_SRC.slice(INDEX_SRC.indexOf('const _bkPayload'),
                                  INDEX_SRC.indexOf("socket.on('backups:list'"));
  assert.ok(!payload.includes('password'),
    'the backups page payload must carry no credential');
});

test('the only unauthenticated backup route is the single-use raw read', () => {
  const publicSet = INDEX_SRC.slice(INDEX_SRC.indexOf('const _MODERN_PUBLIC = new Set'),
                                    INDEX_SRC.indexOf('const _MODERN_PUBLIC_PREFIXES'));
  assert.ok(!publicSet.includes('/api/backups'),
    'no backup route may be added to the static public allow-list');
  assert.ok(INDEX_SRC.includes("path.endsWith('/raw')"),
    'the prefix exemption must be narrowed to the raw read alone');
});

test('downloading either half needs write, not read', () => {
  // An export describes the whole network; the binary carries every key on the
  // device. Neither is a "view the page" act.
  for (const part of ['rsc', 'backup']) {
    const at = INDEX_SRC.indexOf("app.get('/api/backups/:id/" + part + "'");
    assert.ok(at > 0, part + ' route missing');
    assert.ok(INDEX_SRC.slice(at, at + 200).includes("Rbac.requirePerm('router:write'"),
      'the .' + part + ' download must require router:write');
  }
});

test('a restore token is single use and bound to one backup', () => {
  const redeem = INDEX_SRC.slice(INDEX_SRC.indexOf('function _redeemRestoreToken'),
                                 INDEX_SRC.indexOf('function _redeemRestoreToken') + 800);
  assert.ok(redeem.includes('_restoreTokens.delete'),
    'the token must be consumed on the first attempt, successful or not');
  assert.ok(redeem.includes('entry.expires'), 'it must expire');
  assert.ok(redeem.includes('entry.host'), 'it must be bound to the router address');
});

test('a restore is audited before the router is touched', () => {
  const start = INDEX_SRC.indexOf("socket.on('backups:restore'");
  const handler = INDEX_SRC.slice(start, INDEX_SRC.indexOf("socket.on('res:schema'", start));
  const auditAt = handler.indexOf("action: 'backup.restore'", handler.indexOf('.record('));
  const loadAt = handler.indexOf('/system/backup/load');
  assert.ok(auditAt > 0 && loadAt > 0 && auditAt < loadAt,
    'the reboot takes the answer with it, so the row must be written first');
  assert.ok(handler.includes('serial-mismatch'), 'a backup from another device must be refused');
  assert.ok(handler.includes('confirm-mismatch'), 'the operator must type the router name');
  assert.ok(handler.includes('acceptVersion'),
    'a version mismatch warns rather than blocking — you may need it most after a bad upgrade');
});
