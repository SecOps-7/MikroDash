'use strict';
/**
 * Which stretch of time a scheduled report covers, and whether it is due.
 *
 * ── Calendar periods, not rolling intervals ────────────────────────────────
 *
 * The backup scheduler treats "monthly" as 2,592,000,000 ms — thirty days —
 * and that is right for a backup, which does not care *which* thirty days. A
 * report does. "August" is the deliverable a customer expects; "the last thirty
 * days as of whenever the process happened to tick" is not, and it silently
 * changes meaning every time the container restarts.
 *
 * So a period here is a real calendar period in the operator's timezone, and it
 * is always the last COMPLETE one. A daily report sent on the 20th covers the
 * 19th, not a partial 20th.
 *
 * ── Timezone arithmetic without a dependency ───────────────────────────────
 *
 * There is no date library in this project and this does not justify adding
 * one. Offsets come from the same `Intl.DateTimeFormat('sv-SE', { timeZone })`
 * trick the report formatter already uses: format an instant in the target
 * zone, read the result back as if it were UTC, and the difference is the
 * offset. Going the other way — local civil time to an instant — needs one
 * iteration, because the offset you must subtract depends on the answer. Twice
 * settles it either side of a DST transition, and the loop is bounded anyway.
 *
 * Everything here is pure. No database, no clock of its own, no Settings read:
 * `now` and `tz` are always arguments, which is what makes the DST cases
 * testable.
 */

const FREQUENCIES = Object.freeze(['daily', 'weekly', 'monthly']);

/** A failed period is retried this many times before it is abandoned. */
const MAX_ATTEMPTS = 3;

/** ...and not sooner than this after the previous attempt. */
const RETRY_AFTER_MS = 30 * 60 * 1000;

/**
 * Milliseconds this zone is ahead of UTC at `ts`.
 *
 * An empty zone means UTC, which is what the rest of the app does when
 * `displayTimezone` is unset.
 */
function offsetAt(ts, tz) {
  if (!tz) return 0;
  const s = new Intl.DateTimeFormat('sv-SE', {
    timeZone: tz, year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(new Date(ts));
  // 'sv-SE' gives '2026-08-20 10:37:07'; read it back as if it were UTC.
  const asUtc = Date.parse(s.replace(' ', 'T') + 'Z');
  return asUtc - ts;
}

/** The civil date and time in `tz` at `ts`, as plain numbers. */
function civil(ts, tz) {
  const d = new Date(ts + offsetAt(ts, tz));
  return {
    year: d.getUTCFullYear(), month: d.getUTCMonth(), day: d.getUTCDate(),
    // `minute` is here for schedules that fire at a time of day rather than on a
    // period boundary — a backup picks an HH:MM. Reports never read it.
    hour: d.getUTCHours(), minute: d.getUTCMinutes(), weekday: d.getUTCDay(),
  };
}

/**
 * The instant at which the given civil time occurs in `tz`.
 *
 * Iterated because the offset to subtract depends on the instant being
 * computed. On a spring-forward day the requested local time may not exist at
 * all (02:30 where clocks jump 02:00 to 03:00); the loop settles on the instant
 * the zone actually maps it to rather than throwing, which is the behaviour a
 * schedule wants — the report still goes out that day.
 */
function instantOf({ year, month, day, hour = 0, minute = 0 }, tz) {
  // `minute` defaults to 0, so every existing caller — all of which pass a period
  // boundary with no minute — lands on exactly the instant it always did.
  const wall = Date.UTC(year, month, day, hour, minute, 0);
  let guess = wall;
  for (let i = 0; i < 3; i++) {
    const next = wall - offsetAt(guess, tz);
    if (next === guess) break;
    guess = next;
  }
  return guess;
}

/**
 * The last complete calendar period before `now`.
 *
 * Returns `{ from, to }` as instants, where `to` is the boundary that closed
 * the period — so the range is [from, to) and never includes today.
 * Null for a frequency this does not know, which is how a corrupted row stops
 * rather than running constantly.
 */
function periodFor(frequency, now, tz) {
  const c = civil(now, tz);

  if (frequency === 'daily') {
    const to   = instantOf({ year: c.year, month: c.month, day: c.day }, tz);
    const from = instantOf({ year: c.year, month: c.month, day: c.day - 1 }, tz);
    return { from, to };
  }

  if (frequency === 'weekly') {
    // Monday-start. getUTCDay() is 0 for Sunday, so Sunday is 6 days into the week.
    const backToMonday = (c.weekday + 6) % 7;
    const to   = instantOf({ year: c.year, month: c.month, day: c.day - backToMonday }, tz);
    const from = instantOf({ year: c.year, month: c.month, day: c.day - backToMonday - 7 }, tz);
    return { from, to };
  }

  if (frequency === 'monthly') {
    // Date.UTC normalises month -1 into the previous year, so December works.
    const to   = instantOf({ year: c.year, month: c.month,     day: 1 }, tz);
    const from = instantOf({ year: c.year, month: c.month - 1, day: 1 }, tz);
    return { from, to };
  }

  return null;
}

/**
 * When a report for `period` should go out: `sendHour` local time on the day
 * the period closed.
 *
 * Computed from the civil date rather than by adding hours to `to`, so a
 * 23-hour or 25-hour DST day still fires at the requested wall-clock hour.
 */
function fireAt(period, sendHour, tz) {
  const c = civil(period.to, tz);
  const h = Math.min(23, Math.max(0, Number(sendHour) || 0));
  return instantOf({ year: c.year, month: c.month, day: c.day, hour: h }, tz);
}

/**
 * The window a schedule should report on right now, or null if it is not due.
 *
 * `history` is what the database knows about this schedule:
 *   lastRun        epoch ms of the most recent attempt, 0 if never
 *   lastOutcome    'sent' | 'failed' | 'skipped' | null
 *   runsInPeriod   attempts already recorded inside the current period
 *
 * A schedule is due once `now` passes the period's fire time, provided nothing
 * has already run for it since. `created_at` acts as the floor when there is no
 * history, so a schedule created at 10:00 with a 07:00 send hour does not
 * immediately dump yesterday's report — "Send now" exists for proving it works.
 *
 * ── Why failures are retried, and why only a few times ─────────────────────
 *
 * Recording a failed attempt moves `lastRun` past the fire time, which would
 * silently swallow an entire month's report because one SMTP connection timed
 * out. Not recording it means retrying every tick forever against a dead host.
 * Neither is acceptable, so a failed period is retried up to MAX_ATTEMPTS with
 * RETRY_AFTER_MS between attempts, and then abandoned until the next period.
 * Every attempt is recorded either way, so the history stays honest.
 */
function dueWindow(schedule, history, now, tz) {
  if (!schedule || !schedule.enabled) return null;
  const period = periodFor(schedule.frequency, now, tz);
  if (!period) return null;

  const fire = fireAt(period, schedule.send_hour, tz);
  if (now < fire) return null;

  const h = history || {};
  const lastRun = h.lastRun || 0;
  const floor = lastRun || schedule.created_at || 0;
  if (floor < fire) return period;          // nothing has run for this period yet

  // Something has. Only a failure earns another go.
  if (h.lastOutcome !== 'failed') return null;
  if ((h.runsInPeriod || 0) >= MAX_ATTEMPTS) return null;
  if (now - lastRun < RETRY_AFTER_MS) return null;
  return period;
}

/** A human label for the period, for the email subject. */
function label(frequency, period, tz) {
  const c = civil(period.from, tz);
  const pad = (n) => String(n).padStart(2, '0');
  if (frequency === 'monthly') {
    return new Intl.DateTimeFormat('en-GB', { timeZone: tz || 'UTC', year: 'numeric', month: 'long' })
      .format(new Date(period.from));
  }
  const start = c.year + '-' + pad(c.month + 1) + '-' + pad(c.day);
  if (frequency === 'daily') return start;
  const e = civil(period.to - 1, tz);
  return start + ' to ' + e.year + '-' + pad(e.month + 1) + '-' + pad(e.day);
}

module.exports = {
  FREQUENCIES, MAX_ATTEMPTS, RETRY_AFTER_MS,
  offsetAt, civil, instantOf, periodFor, fireAt, dueWindow, label,
};
