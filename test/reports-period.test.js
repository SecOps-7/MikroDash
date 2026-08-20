'use strict';
/**
 * Calendar periods and due-ness for scheduled reports (#60).
 *
 * Pure functions, no database and no clock: `now` and `tz` are arguments, which
 * is the whole reason the DST cases below can be written at all.
 *
 * The DST tests are not decoration. This is hand-rolled timezone arithmetic
 * with no date library, and the two days a year when a local day is not 24
 * hours long are exactly where that goes wrong.
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');

const P = require('../src/reports/period');

const BERLIN = 'Europe/Berlin';
const HOUR = 3600000;
const DAY = 86400000;

/** An instant from a UTC wall-clock, for readable fixtures. */
const utc = (y, m, d, h = 0, mi = 0) => Date.UTC(y, m - 1, d, h, mi);

// ── Offsets ────────────────────────────────────────────────────────────────

test('an empty timezone behaves as UTC', () => {
  assert.equal(P.offsetAt(utc(2026, 8, 20, 12), ''), 0);
  assert.equal(P.offsetAt(utc(2026, 1, 20, 12), undefined), 0);
});

test('offsets follow the zone across a DST boundary', () => {
  // Berlin is UTC+1 in winter, UTC+2 in summer.
  assert.equal(P.offsetAt(utc(2026, 1, 15, 12), BERLIN), 1 * HOUR);
  assert.equal(P.offsetAt(utc(2026, 7, 15, 12), BERLIN), 2 * HOUR);
});

// ── Daily ──────────────────────────────────────────────────────────────────

test('a daily period is yesterday, not a partial today', () => {
  const now = utc(2026, 8, 20, 9);            // 11:00 Berlin
  const p = P.periodFor('daily', now, BERLIN);
  // Local midnight Berlin is 22:00 UTC the previous day in summer.
  assert.equal(p.from, utc(2026, 8, 18, 22));
  assert.equal(p.to,   utc(2026, 8, 19, 22));
  assert.ok(p.to <= now, 'the period must be complete');
  assert.equal(p.to - p.from, DAY);
});

test('the spring-forward day is 23 hours long', () => {
  // Clocks go forward 02:00 to 03:00 on 2026-03-29 in Berlin.
  const now = utc(2026, 3, 30, 12);
  const p = P.periodFor('daily', now, BERLIN);
  assert.equal(p.to - p.from, 23 * HOUR, 'the 29th lost an hour');
});

test('the fall-back day is 25 hours long', () => {
  // Clocks go back 03:00 to 02:00 on 2026-10-25 in Berlin.
  const now = utc(2026, 10, 26, 12);
  const p = P.periodFor('daily', now, BERLIN);
  assert.equal(p.to - p.from, 25 * HOUR, 'the 25th gained an hour');
});

test('the fire hour is a wall-clock hour, even on a DST day', () => {
  // 07:00 local on the short day is still 07:00 local, not 06:00 or 08:00.
  const p = P.periodFor('daily', utc(2026, 3, 30, 12), BERLIN);
  assert.equal(P.civil(P.fireAt(p, 7, BERLIN), BERLIN).hour, 7);
  const winter = P.periodFor('daily', utc(2026, 1, 20, 12), BERLIN);
  assert.equal(P.civil(P.fireAt(winter, 7, BERLIN), BERLIN).hour, 7);
});

// ── Weekly and monthly ─────────────────────────────────────────────────────

test('a weekly period is the previous Monday-to-Monday week', () => {
  // 2026-08-20 is a Thursday.
  const p = P.periodFor('weekly', utc(2026, 8, 20, 12), 'UTC');
  assert.equal(new Date(p.from).getUTCDay(), 1, 'starts on a Monday');
  assert.equal(new Date(p.to).getUTCDay(), 1, 'ends on a Monday');
  assert.equal(p.to - p.from, 7 * DAY);
  assert.equal(p.to, utc(2026, 8, 17));
});

test('a week that begins on Sunday still looks back to Monday', () => {
  // Sunday is weekday 0, the trap in Monday-start arithmetic.
  const sunday = utc(2026, 8, 23, 12);
  assert.equal(new Date(sunday).getUTCDay(), 0);
  const p = P.periodFor('weekly', sunday, 'UTC');
  assert.equal(p.to, utc(2026, 8, 17), 'not the Monday two days from now');
});

test('a monthly period is the previous calendar month, whatever its length', () => {
  const feb = P.periodFor('monthly', utc(2026, 3, 15, 12), 'UTC');
  assert.equal(feb.from, utc(2026, 2, 1));
  assert.equal(feb.to,   utc(2026, 3, 1));
  assert.equal(feb.to - feb.from, 28 * DAY, '2026 is not a leap year');

  const jan = P.periodFor('monthly', utc(2026, 2, 15, 12), 'UTC');
  assert.equal(jan.to - jan.from, 31 * DAY);

  const apr = P.periodFor('monthly', utc(2026, 5, 15, 12), 'UTC');
  assert.equal(apr.to - apr.from, 30 * DAY);
});

test('January looks back into the previous year', () => {
  const p = P.periodFor('monthly', utc(2026, 1, 15, 12), 'UTC');
  assert.equal(p.from, utc(2025, 12, 1));
  assert.equal(p.to,   utc(2026, 1, 1));
});

test('a leap February is 29 days', () => {
  const p = P.periodFor('monthly', utc(2028, 3, 15, 12), 'UTC');
  assert.equal(p.to - p.from, 29 * DAY);
});

test('an unknown frequency yields no period', () => {
  // A corrupted row must stop, not run on every tick.
  assert.equal(P.periodFor('fortnightly', Date.now(), 'UTC'), null);
  assert.equal(P.periodFor('', Date.now(), 'UTC'), null);
});

// ── Due-ness ───────────────────────────────────────────────────────────────

const schedule = (over = {}) => ({
  enabled: 1, frequency: 'daily', send_hour: 7, created_at: 0, ...over,
});

test('not due before the fire hour, due after it', () => {
  const before = utc(2026, 8, 20, 4);       // 06:00 Berlin
  const after  = utc(2026, 8, 20, 6);       // 08:00 Berlin
  assert.equal(P.dueWindow(schedule(), {}, before, BERLIN), null);
  assert.ok(P.dueWindow(schedule(), {}, after, BERLIN), 'past 07:00 local');
});

test('a disabled schedule is never due', () => {
  assert.equal(P.dueWindow(schedule({ enabled: 0 }), {}, utc(2026, 8, 20, 12), BERLIN), null);
});

test('a schedule sends once per period, however often the tick runs', () => {
  const now = utc(2026, 8, 20, 12);
  assert.ok(P.dueWindow(schedule(), {}, now, BERLIN));
  // Having run at the fire time, it is not due again on the next tick.
  assert.equal(
    P.dueWindow(schedule(), { lastRun: now, lastOutcome: 'sent' }, now + 300000, BERLIN), null);
});

test('a schedule created after today fire time waits for tomorrow', () => {
  // Created at 10:00 local with a 07:00 send hour: it must not immediately
  // dump yesterday's report. "Send now" exists for proving it works.
  const now = utc(2026, 8, 20, 12);
  const createdToday = utc(2026, 8, 20, 8);   // 10:00 Berlin, after 07:00
  assert.equal(P.dueWindow(schedule({ created_at: createdToday }), {}, now, BERLIN), null);
});

test('a failed period is retried, then abandoned', () => {
  const now = utc(2026, 8, 20, 12);
  const failed = (n, at) => ({ lastRun: at, lastOutcome: 'failed', runsInPeriod: n });

  // Too soon after the failure.
  assert.equal(P.dueWindow(schedule(), failed(1, now - 60000), now, BERLIN), null);
  // Far enough past it.
  assert.ok(P.dueWindow(schedule(), failed(1, now - P.RETRY_AFTER_MS), now, BERLIN));
  // Out of attempts: the period is abandoned rather than retried forever.
  assert.equal(
    P.dueWindow(schedule(), failed(P.MAX_ATTEMPTS, now - P.RETRY_AFTER_MS), now, BERLIN), null);
});

test('a skipped period is not retried', () => {
  // Skipped means a condition that will not change on its own — the router is
  // gone, or the creator lost access. Retrying would just log the same refusal.
  const now = utc(2026, 8, 20, 12);
  assert.equal(
    P.dueWindow(schedule(),
      { lastRun: now - P.RETRY_AFTER_MS, lastOutcome: 'skipped', runsInPeriod: 1 }, now, BERLIN),
    null);
});

// ── Labels ─────────────────────────────────────────────────────────────────

test('the period label reads as the deliverable', () => {
  assert.equal(P.label('daily', P.periodFor('daily', utc(2026, 8, 20, 12), 'UTC'), 'UTC'), '2026-08-19');
  assert.equal(P.label('monthly', P.periodFor('monthly', utc(2026, 8, 20, 12), 'UTC'), 'UTC'), 'July 2026');
  assert.equal(P.label('weekly', P.periodFor('weekly', utc(2026, 8, 20, 12), 'UTC'), 'UTC'),
    '2026-08-10 to 2026-08-16');
});
