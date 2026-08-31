'use strict';
/**
 * Report period cases — what reports/period.js answers, for the Go port.
 *
 * ── WHY A GENERATOR AND NOT A FIXTURE ───────────────────────────────────────
 *
 * This module touches no router and no database: `now` and `tz` are always
 * arguments, which is exactly what its own header says makes the DST cases
 * testable. There is nothing to capture. What there IS to pin is arithmetic that
 * only misbehaves on a handful of days a year, in zones the developer does not
 * live in.
 *
 * ── THE DAYS THAT ARE HARD ──────────────────────────────────────────────────
 *
 * A sweep of ordinary days proves almost nothing here: on a day with no
 * transition every implementation of this agrees. The interesting instants are
 *
 *   - the hour that DOES NOT EXIST (spring forward: 02:30 where the clocks jump
 *     02:00 → 03:00). The original iterates to whatever the zone maps it to
 *     rather than throwing, and a port using a language's own civil-time
 *     resolver may pick the OTHER side of the gap.
 *   - the hour that happens TWICE (autumn back). Both instants are valid local
 *     times; which one you get is a choice, and it must be the same choice.
 *   - the 23- and 25-hour days themselves, where "send at 07:00" must stay 07:00
 *     wall-clock rather than drifting by an hour.
 *   - a southern-hemisphere zone, where the transitions run the other way.
 *   - a zone with a HALF-HOUR offset and one with 45 minutes, which catch an
 *     implementation that stores offsets in whole hours.
 *   - the month and year boundaries, where `month - 1` has to normalise.
 *
 * So the sample is built around real transition dates rather than swept.
 *
 *   node tools/report-period-cases.js            write testdata/report-period-cases.json
 *   node tools/report-period-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'report-period-cases.json');

const P = require(path.join(LIVE, 'src', 'reports', 'period.js'));

// The endpoint's allow-list, from index.js. Copied rather than required because
// index.js starts a server on load.
const AGG_VALID = new Set(['hour', 'day', 'week', 'month']);

const HOUR = 3600 * 1000;
const DAY = 24 * HOUR;

/** Zones chosen for what each one breaks, not for coverage. */
const ZONES = [
  '',                      // unset, which the app treats as UTC
  'UTC',
  'Europe/Berlin',         // CET/CEST, the common case
  'Europe/London',         // transitions on a different date from Berlin
  'America/New_York',      // transitions on a different date again
  'Australia/Sydney',      // southern hemisphere: the transitions invert
  'Asia/Kolkata',          // +05:30, no DST — a half-hour offset
  'Asia/Kathmandu',        // +05:45, which breaks whole-hour offset storage
  'Pacific/Chatham',       // +12:45/+13:45 — 45 minutes WITH DST
  'America/Sao_Paulo',     // abolished DST in 2019; the tzdata holds both eras
];

/**
 * Instants around real transitions, plus ordinary and boundary days.
 * Every value is a fixed literal: the file must be identical on every run or
 * `--check` becomes a coin toss.
 */
function instants() {
  const at = (s) => Date.parse(s);
  return [
    // ── European spring forward: 2026-03-29 01:00 UTC ─────────────────────
    at('2026-03-29T00:30:00Z'), at('2026-03-29T01:00:00Z'), at('2026-03-29T01:30:00Z'),
    at('2026-03-29T02:30:00Z'), at('2026-03-29T12:00:00Z'), at('2026-03-30T12:00:00Z'),
    // ── European autumn back: 2026-10-25 01:00 UTC ────────────────────────
    at('2026-10-25T00:30:00Z'), at('2026-10-25T01:00:00Z'), at('2026-10-25T01:30:00Z'),
    at('2026-10-25T12:00:00Z'), at('2026-10-26T12:00:00Z'),
    // ── US transitions, which are on different dates ──────────────────────
    at('2026-03-08T06:30:00Z'), at('2026-03-08T07:00:00Z'), at('2026-03-08T08:00:00Z'),
    at('2026-11-01T05:30:00Z'), at('2026-11-01T06:00:00Z'), at('2026-11-01T07:00:00Z'),
    // ── southern hemisphere, running the other way ────────────────────────
    at('2026-04-05T15:30:00Z'), at('2026-04-05T16:30:00Z'),
    at('2026-10-04T15:30:00Z'), at('2026-10-04T16:30:00Z'),
    // ── Chatham's 45-minute transitions ───────────────────────────────────
    at('2026-04-05T14:45:00Z'), at('2026-09-27T14:45:00Z'),
    // ── month, year and leap-day boundaries ───────────────────────────────
    at('2026-01-01T00:00:00Z'), at('2026-01-01T00:30:00Z'), at('2025-12-31T23:30:00Z'),
    at('2026-02-01T00:00:00Z'), at('2026-03-01T00:00:00Z'),
    at('2024-02-29T12:00:00Z'), at('2024-03-01T00:00:00Z'),
    at('2026-08-01T00:00:00Z'), at('2026-08-31T23:59:59Z'),
    // ── every weekday, so the Monday-start arithmetic is exercised whole ──
    at('2026-08-17T09:00:00Z'), at('2026-08-18T09:00:00Z'), at('2026-08-19T09:00:00Z'),
    at('2026-08-20T09:00:00Z'), at('2026-08-21T09:00:00Z'), at('2026-08-22T09:00:00Z'),
    at('2026-08-23T09:00:00Z'),
    // ── the epoch, and an instant before it ───────────────────────────────
    0, -1, at('1969-12-31T23:00:00Z'),
    // ── Sao Paulo either side of the abolition ────────────────────────────
    //
    // 2018-11-04 is the day BRAZIL SPRANG FORWARD AT MIDNIGHT: 00:00 became
    // 01:00, so that date has no midnight at all. It is the only case in this
    // file where `periodFor` itself — not just a send hour — asks for a civil
    // time that does not exist, and the instant has to fall on the 5th for the
    // period boundary to land on the 4th.
    at('2018-11-04T02:30:00Z'), at('2020-11-04T02:30:00Z'),
    at('2018-11-05T12:00:00Z'), at('2018-11-04T12:00:00Z'),
    at('2018-11-11T12:00:00Z'), at('2018-12-01T12:00:00Z'),
  ];
}

function main() {
  const check = process.argv.includes('--check');
  const cases = [];

  for (const tz of ZONES) {
    for (const now of instants()) {
      const row = { tz, now, offset: P.offsetAt(now, tz), civil: P.civil(now, tz), periods: {} };
      for (const freq of ['daily', 'weekly', 'monthly', 'nonsense']) {
        const period = P.periodFor(freq, now, tz);
        const entry = { period };
        if (period) {
          // sendHour 0, 7 and 23 — the ends and a plausible middle. Out-of-range
          // values are clamped by the original, and that clamp is worth pinning.
          entry.fireAt = {};
          // 2 and 3 STRADDLE THE EUROPEAN AND US GAPS. Every other hour here
          // resolves to an instant that exists, and a send hour that exists is
          // one on which any implementation of instantOf agrees — which is how
          // a first version of this file failed to notice a port that used the
          // language's own civil-time resolver instead of the offset loop.
          for (const h of [0, 2, 3, 7, 23, -5, 99]) entry.fireAt[h] = P.fireAt(period, h, tz);
          entry.label = P.label(freq, period, tz);
        }
        row.periods[freq] = entry;
      }
      cases.push(row);
    }
  }

  // ── dueWindow, which is state rather than arithmetic ──────────────────────
  //
  // The retry rules are the part worth pinning: a failure earns another go, a
  // success does not, and both stop at MAX_ATTEMPTS.
  const due = [];
  const base = Date.parse('2026-08-20T09:00:00Z');
  const sched = (over) => Object.assign(
    { enabled: 1, frequency: 'daily', send_hour: 7, created_at: base - 30 * DAY }, over);
  const hist = (over) => Object.assign({ lastRun: 0, lastOutcome: null, runsInPeriod: 0 }, over);
  const combos = [
    ['never run', sched(), hist()],
    ['disabled', sched({ enabled: 0 }), hist()],
    ['unknown frequency', sched({ frequency: 'hourly' }), hist()],
    ['before the fire time', sched({ send_hour: 23 }), hist()],
    ['created after the fire time', sched({ created_at: base }), hist()],
    ['already sent', sched(), hist({ lastRun: base - HOUR, lastOutcome: 'sent' })],
    ['already skipped', sched(), hist({ lastRun: base - HOUR, lastOutcome: 'skipped' })],
    ['failed, too soon', sched(), hist({ lastRun: base - 60 * 1000, lastOutcome: 'failed', runsInPeriod: 1 })],
    ['failed, retry due', sched(), hist({ lastRun: base - HOUR, lastOutcome: 'failed', runsInPeriod: 1 })],
    ['failed, attempts spent', sched(), hist({ lastRun: base - HOUR, lastOutcome: 'failed', runsInPeriod: 3 })],
    ['failed, one under the cap', sched(), hist({ lastRun: base - HOUR, lastOutcome: 'failed', runsInPeriod: 2 })],
    ['weekly, never run', sched({ frequency: 'weekly' }), hist()],
    ['monthly, never run', sched({ frequency: 'monthly' }), hist()],
  ];
  for (const tz of ['', 'Europe/Berlin', 'America/New_York']) {
    for (const [name, s, h] of combos) {
      due.push({ name, tz, now: base, schedule: s, history: h, window: P.dueWindow(s, h, base, tz) });
    }
  }

  // ── the shared helpers the endpoints use ──────────────────────────────────
  //
  // Same file rather than a new one: they are all the pure half of the reports
  // subsystem, they are read by the same Go package, and a second generator
  // would be a second thing to remember to regenerate.
  // format.js reaches only settings.js, which needs nothing native — so this
  // stays runnable on the host. `alerter.labelFor` does NOT: alerter requires
  // db.js and therefore better-sqlite3, so the alert-label cases live in
  // tools/report-history-cases.js, which already runs in the container. Pulling
  // them in here would have made a host generator container-only, which is a
  // real cost for one small table.
  const F = require(path.join(LIVE, 'src', 'reports', 'format.js'));

  // parseInt's LENIENCE is the point of these: '123abc' is 123, 'abc' is 0, and
  // the `|| Date.now()` fallback fires for a parsed ZERO as well as a failure.
  const parseInts = ['0', '', '123', '123abc', 'abc', ' 42 ', '-5', '+7', '0x10',
    '1e3', '007', '1767225600000', '99999999999999999999', 'null', 'undefined',
    '1.9', '.5', '  ', '12_000'];

  const aggregates = ['hour', 'day', 'week', 'month', 'quarter', '', 'HOUR', ' hour', 'minute'];

  // Runs chosen so the backwards pass has something to carry: a trailing outage
  // with no online row after it must stay NULL rather than being measured
  // against now.
  const downtimeRuns = [
    [],
    [{ ts: 1000, connected: 1 }],
    [{ ts: 1000, connected: 0 }],
    [{ ts: 1000, connected: 0 }, { ts: 5000, connected: 1 }],
    [{ ts: 1000, connected: 1 }, { ts: 2000, connected: 0 }, { ts: 2500, connected: 0 },
     { ts: 9000, connected: 1 }],
    [{ ts: 1000, connected: 1 }, { ts: 2000, connected: 0 }],
    [{ ts: 1000, connected: 0 }, { ts: 2000, connected: 0 }, { ts: 3000, connected: 0 }],
  ];

  const helpers = {
    // Recorded as a STRING. `parseInt('99999999999999999999')` is 1e20, which
    // JSON writes as 100000000000000000000 and no int64 can hold — decoding it
    // would fail the whole case file over one input. The string keeps the case
    // visible; the Go side saturates and says why.
    parseInts: parseInts.map((v) => ({ in: v, out: String(parseInt(v, 10) || 0) })),
    aggregates: aggregates.map((v) => ({ in: v, out: AGG_VALID.has(v) ? v : '' })),
    downtime: downtimeRuns.map((rows) => ({
      in: JSON.parse(JSON.stringify(rows)),
      out: F.annotateDowntime(JSON.parse(JSON.stringify(rows))),
    })),
    // `Math.max(1, parseInt(v,10) || 1000)` — a record carrying 0 becomes 1000,
    // and a negative one becomes 1 rather than an infinite utilisation.
    capacities: ['', '0', '1000', '50', '-5', 'abc', '250abc', '1']
      .map((v) => ({ in: v, out: Math.max(1, parseInt(v, 10) || 1000) })),
    // ROUNDING IS THE WHOLE POINT OF THIS TABLE, so it is swept rather than
    // sampled. `toFixed` rounds the exact value of the double with ties going
    // toward +infinity, and the two obvious Go spellings — the 'f' formatter and
    // floor(x*10+0.5) — each get a DIFFERENT subset of these wrong. Two cases
    // caught the first two attempts; a sweep is what stops the third being
    // caught by luck.
    utilisation: (() => {
      const out = [[null, 1000], [0, 1000], [1510, 1000], [1, 3], [1000, 1000]];
      // Exact ties at one decimal: x.x5 where the double is exactly on the half.
      for (let i = 0; i < 40; i++) out.push([(i * 25) / 100, 100]);
      // Values whose ×10 lands on a tie only because of the multiply.
      for (const v of [940.5, 94.05, 9.405, 0.9405, 12.25, 1.225, 122.5]) {
        out.push([v, 1000]); out.push([v, 100]); out.push([v, 3]);
      }
      // Small ones, where the percentage has more decimals than it keeps.
      for (const v of [0.05, 0.005, 0.0005, 2.675, 1.005, 8.995]) out.push([v, 1000]);
      return out;
    })().map(([v, cap]) => ({ v, cap, out: v == null ? null : +((v / cap) * 100).toFixed(1) })),
  };

  const body = JSON.stringify({
    note: 'Generated by tools/report-period-cases.js — do not edit. Answers come ' +
          'from src/reports/period.js, src/reports/format.js and src/alerter.js ' +
          'in the live repo.',
    helpers,
    constants: { MAX_ATTEMPTS: P.MAX_ATTEMPTS, RETRY_AFTER_MS: P.RETRY_AFTER_MS,
                 FREQUENCIES: P.FREQUENCIES },
    zones: ZONES.length, instants: instants().length,
    cases, due,
  }, null, 2) + '\n';

  if (check) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body) {
      console.error('testdata/report-period-cases.json is stale — period.js has changed.\n' +
                    'Run: node tools/report-period-cases.js');
      process.exit(1);
    }
    console.log('report period cases up to date (' + cases.length + ' instants, ' +
                due.length + ' dueWindow cases)');
    return;
  }
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), OUT) +
    ' — ' + cases.length + ' instant/zone rows, ' + due.length + ' dueWindow cases');
}

main();
