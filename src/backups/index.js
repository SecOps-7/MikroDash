'use strict';
/**
 * When backups run, and what happens around each run.
 *
 * ── Due-ness is read from the database, not from a timer ───────────────────
 *
 * The tick asks, per enabled router, whether `now - lastRun >= interval`, and
 * `lastRun` comes from `config_backups`. A purely in-process timer would both
 * skip a due backup (restarted just before it fired) and re-run a fresh one
 * (restarted just after), and a MikroDash that is redeployed often would do
 * one or the other constantly.
 *
 * Every run is recorded, including the ones that changed nothing. That is what
 * makes "checked daily, changed on these three dates" answerable, and it is
 * the same row the scheduler reads back to decide due-ness.
 *
 * ── One at a time, per router and across the fleet ─────────────────────────
 *
 * A run goes through the caller's write queue so it cannot interleave with a
 * firewall edit on the same router. Across routers the tick awaits each run in
 * turn: a backup is several MB of flash and a few seconds of CPU on hardware
 * as small as a hAP ac2, and a fleet-wide simultaneous sweep is exactly the
 * shape that overwhelms them.
 */

const Runner = require('./runner');
const Store = require('./store');
const Diff = require('./diff');
const Routers = require('../routers');
// Borrowed from reports rather than reimplemented: one DST-correct timezone
// implementation, already tested, and a backup at 02:00 has exactly the same
// spring-forward problem a report at 02:00 does.
const Period = require('../reports/period');

/** How often to ask whether anything is due. Cheap: one integer compare per router. */
const TICK_MS = 5 * 60 * 1000;

/** Routers being backed up right now, so a slow run never starts twice. */
const _running = new Set();

let _timer = null;
let _deps = null;

/** Milliseconds between runs for a schedule name, or 0 if it is not one. */
function intervalFor(schedule, schedules) {
  return (schedules && schedules[schedule]) || 0;
}

/**
 * Is this router due?
 *
 * A router with backups off is never due. A router that has never run is due
 * immediately — the first backup should not wait a day to prove the feature
 * works.
 */
function isDue(router, lastRun, now, schedules, tz) {
  if (!router || !router.backup || !router.backup.enabled) return false;
  const interval = intervalFor(router.backup.schedule, schedules);
  if (!interval) return false;
  if (!lastRun) return true;
  if ((now - lastRun) < interval) return false;

  // Absent means "never chosen", so it takes the default; an explicitly stored ''
  // means "any time" and keeps the interval-only behaviour. Collapsing the two
  // would make clearing the field impossible — it would read back as unset and
  // the default would reappear on the next tick.
  const chosen = router.backup.time === undefined ? Routers.BACKUP_DEFAULTS.time
                                                  : router.backup.time;
  const at = Routers.backupTimeMinutes(chosen);
  // 'hourly' is deliberately excluded — an hourly backup that waits for 08:00 is
  // a daily backup.
  if (at === null || router.backup.schedule === 'hourly') return true;

  // Anchored to the wall clock rather than to the last run, so a daily backup
  // set for 02:00 stays at 02:00 instead of drifting by however long each run
  // took. `lastRun < target` holds it to one run per day; `now >= target` lets a
  // router that was switched off at 02:00 still catch up when it comes back,
  // rather than skipping the day altogether.
  const target = _todayAt(at, now, tz);
  return now >= target && lastRun < target;
}

/** The instant of `minutes` past local midnight on the day `now` falls in. */
function _todayAt(minutes, now, tz) {
  const c = Period.civil(now, tz);
  return Period.instantOf({ year: c.year, month: c.month, day: c.day,
                            hour: Math.floor(minutes / 60), minute: minutes % 60 }, tz);
}

/**
 * Remove pairs beyond the router's retention, and say so.
 *
 * Driven by the database rather than by a directory listing: rows are the
 * record of what MikroDash made, and a file it did not make is not its to
 * delete. `markBackupPruned` clears the artefact, never the row.
 */
function pruneFor(router, db, now, log) {
  const keep = router.backup || {};
  if (!keep.keepCount && !keep.keepDays) return 0;

  const rows = db.storedBackups(router.id);
  const doomed = new Set(Store.selectForPruning(rows, keep, now));
  let removed = 0;
  for (const row of rows) {
    if (!doomed.has(row.stem)) continue;
    try {
      Store.removePair(row.dir || Store.dirFor(Store.slugFor(router.label)), row.stem);
      db.markBackupPruned(row.id, now);
      removed++;
    } catch (e) {
      log('could not prune ' + row.stem + ': ' + ((e && e.message) || e));
    }
  }
  if (removed) log('pruned ' + removed + ' backup pair(s)');
  return removed;
}

/**
 * Tell someone, but only about the two things worth interrupting for.
 *
 * "Backup succeeded, nothing changed" is deliberately not notifiable. On a
 * daily schedule that is a message every day that says nothing, and a channel
 * that cries wolf daily is one people mute — including for the two below.
 */
function _notify(router, result, previous, d) {
  if (!d.notify) return;
  const name = router.label;
  if (result.outcome === 'changed' && previous) {
    // Only when there was something to drift FROM: the very first backup is
    // not drift, it is a baseline.
    d.notify('drift', 'Configuration changed: ' + name,
      name + ' drifted from its last backup. A new restore point was stored.');
  } else if (result.outcome === 'failed') {
    d.notify('fail', 'Backup failed: ' + name, name + ' — ' + (result.error || 'unknown error'));
  }
}

/**
 * Take one backup and record it, whatever the outcome.
 *
 * `source` is 'schedule' or 'manual'; `actor` names the human for a manual
 * run, so the history can answer who took a restore point as well as when.
 */
async function runFor(router, { source, actor } = {}) {
  const d = _deps;
  if (!d) throw new Error('backup scheduler not started');
  if (_running.has(router.id)) {
    return { outcome: 'skipped', error: 'a backup is already running for this router' };
  }

  _running.add(router.id);
  const log = (msg) => d.log('[backup][' + router.label + '] ' + msg);
  try {
    const previous = d.db.latestFingerprint(router.id);
    const result = await Runner.run({
      router,
      connect: () => d.connect(router),
      previousFingerprint: previous,
      log,
    });

    const now = Date.now();
    result.id = d.db.recordBackup({
      routerId: router.id,
      takenAt: now,
      outcome: result.outcome,
      source: source || 'schedule',
      actor: actor || null,
      stem: result.changed ? result.stem : null,
      dir: result.changed ? result.dir : null,
      fingerprint: result.fingerprint,
      rscBytes: result.rscBytes,
      backupBytes: result.backupBytes,
      identity: result.identity,
      ms: result.ms,
      error: result.error,
    });

    if (result.changed) pruneFor(router, d.db, now, log);
    _notify(router, result, previous, d);
    if (d.onResult) { try { d.onResult(router, result); } catch (_) { /* UI only */ } }
    return result;
  } finally {
    _running.delete(router.id);
  }
}

/** Every router whose backup is due right now. */
function dueRouters(now) {
  const d = _deps;
  return d.getRouters()
    .filter(r => !r.disabled)
    .filter(r => isDue(r, d.db.lastBackupRun(r.id), now, d.schedules,
                       d.getTimezone ? d.getTimezone() : ''));
}

async function tick() {
  const d = _deps;
  if (!d) return;
  const due = dueRouters(Date.now());
  for (const router of due) {
    try {
      await d.queue(router.id, () => runFor(router, { source: 'schedule' }));
    } catch (e) {
      d.log('[backup][' + router.label + '] scheduler error: ' + ((e && e.message) || e));
    }
  }
}

/**
 * Start ticking.
 *
 * Everything the scheduler touches is injected, so the whole thing can be
 * driven in a test with no router, no database and no clock.
 *
 * Deliberately does NOT run a tick immediately: a restart should not stampede
 * the fleet before the sessions it needs have even connected.
 */
function start(deps) {
  _deps = deps;
  if (_timer) return;
  _timer = setInterval(() => { tick().catch(() => {}); }, deps.tickMs || TICK_MS);
  if (_timer.unref) _timer.unref();
}

function stop() {
  if (_timer) clearInterval(_timer);
  _timer = null;
  _deps = null;
  _running.clear();
}

/** Read a stored export back, for the diff view. */
function readExport(row) {
  return Store.readRsc(row.dir, row.stem);
}

/**
 * The difference between two stored backups, or between one and some other
 * export text — which is how "what has changed since the last backup" is
 * answered without storing a pair for it.
 */
function diffOf(oldRow, newRowOrText) {
  const a = oldRow ? readExport(oldRow) : '';
  const b = typeof newRowOrText === 'string' ? newRowOrText : readExport(newRowOrText);
  return Diff.diff(a, b);
}

module.exports = {
  TICK_MS,
  isDue,
  intervalFor,
  pruneFor,
  runFor,
  dueRouters,
  tick,
  start,
  stop,
  readExport,
  diffOf,
  _running,
};
