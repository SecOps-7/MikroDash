'use strict';
/**
 * When scheduled reports run, and what happens around each run.
 *
 * Structurally a sibling of src/backups/index.js: a five-minute tick, nothing
 * on boot, everything dependency-injected so the whole thing is drivable in a
 * test with no database, no mail server and no clock.
 *
 * Two deliberate differences from the backup scheduler:
 *
 *   Periods are CALENDAR periods, not rolling intervals. See ./period.js.
 *
 *   Due-ness tolerates failure. A backup that fails is simply due again on the
 *   next interval; a report period closes, so a swallowed failure loses that
 *   month entirely. ./period.js bounds the retries instead.
 *
 * Runs are awaited one at a time. PDFKit renders synchronously on the event
 * loop that is simultaneously streaming live dashboards, so two concurrent
 * thousand-row renders would visibly stall the UI.
 */

const Pdf = require('./pdf');
const Period = require('./period');
const Schedules = require('./schedules');
const Mailer = require('./mailer');

const TICK_MS = 5 * 60 * 1000;

/** Schedules running right now, so a slow one never starts twice. */
const _running = new Set();

let _timer = null;
let _deps = null;

function _tz(d) {
  const s = d.settings ? d.settings() : {};
  return s.displayTimezone || '';
}

/**
 * May this schedule still send?
 *
 * Re-asked at send time rather than trusted from creation time, copying
 * userNotify.recipientsFor(): a report that keeps emailing a router's history
 * to arbitrary addresses after its creator lost access is exactly the failure
 * this prevents.
 *
 * `created_by` is NULL on an install that has never had authentication, and
 * Rbac.can({ userId: null }) denies — so a naive check would disable every
 * schedule the moment someone switches auth on. NULL is therefore permitted
 * only while auth is off, and never means "allowed" on a modern install.
 */
function _mayStillSend(schedule, d) {
  if (!schedule.created_by) {
    return d.isModern()
      ? { ok: false, reason: 'created before authentication was enabled — recreate it' }
      : { ok: true };
  }
  return d.canRead(schedule.created_by, schedule.router_id)
    ? { ok: true }
    : { ok: false, reason: 'creator no longer has report access to this router' };
}

/**
 * Produce one report and email it.
 *
 * Never throws: a run that could not happen is a recorded outcome, not an
 * exception for the tick to lose.
 */
async function runOnce(scheduleRow, period, { source, actor } = {}) {
  const d = _deps;
  const started = Date.now();
  const s = Schedules.decode(scheduleRow);
  const tz = _tz(d);
  const result = {
    outcome: 'failed', error: null, bytes: 0, rowsN: 0,
    recipientsN: 0, sections: [], skipped: [],
  };

  try {
    const router = d.getRouter(s.router_id);
    if (!router) {
      result.outcome = 'skipped';
      result.error = 'the router no longer exists';
      d.db.setReportScheduleEnabled(s.id, false, result.error);
      return result;
    }

    const may = _mayStillSend(scheduleRow, d);
    if (!may.ok) {
      result.outcome = 'skipped';
      result.error = may.reason;
      d.db.setReportScheduleEnabled(s.id, false, may.reason);
      return result;
    }

    const settings = d.settings();
    if (!settings.smtpHost || !settings.smtpFrom) {
      // Recorded once per period rather than retried every five minutes: an
      // unconfigured mail server is not a transient failure.
      result.outcome = 'skipped';
      result.error = 'SMTP is not configured';
      return result;
    }

    const aggregate = Schedules.aggregateFor({ aggregate: s.aggregate, frequency: s.frequency });
    const attachments = [];
    for (const section of s.sections) {
      try {
        const built = d.buildReport(section, {
          routerId: s.router_id, iface: s.interface || '',
          from: period.from, to: period.to, aggregate,
        });
        const pdf = await Pdf.toBuffer(built.title, built.pdf.columns, built.pdf.rows,
          built.pdf.meta, Mailer.MAX_ATTACHMENT_BYTES);
        attachments.push({
          section,
          title: built.title,
          rowCount: built.rowCount,
          truncated: built.truncated,
          filename: built.title.replace(/[^A-Za-z0-9]+/g, '-').toLowerCase() + '.pdf',
          content: pdf,
          contentType: 'application/pdf',
        });
        result.rowsN += built.rowCount;
      } catch (e) {
        // An interface renamed on the router should cost that section, not the
        // whole report.
        result.skipped.push({ section, reason: (e && e.message) || String(e) });
      }
    }

    if (!attachments.length) {
      result.outcome = 'failed';
      result.error = 'no section could be produced'
        + (result.skipped.length ? ': ' + result.skipped[0].reason : '');
      return result;
    }

    const fitted = Mailer.fitAttachments(attachments);
    const label = router.label || router.host;

    await d.mail(settings, {
      ...Mailer.envelope(settings, s.recipients),
      subject: Mailer.subject(s, label, period, tz),
      text: Mailer.body({
        schedule: s, routerLabel: label, period,
        sections: fitted.kept, dropped: fitted.dropped, skipped: result.skipped,
        truncated: fitted.kept.some(a => a.truncated), tz,
      }),
      attachments: fitted.kept.map(a => ({
        filename: a.filename, content: a.content, contentType: a.contentType,
      })),
    });

    result.outcome = 'sent';
    result.bytes = fitted.bytes;
    result.recipientsN = s.recipients.length;
    result.sections = fitted.kept.map(a => a.section);
  } catch (e) {
    result.outcome = 'failed';
    result.error = (e && e.message) || String(e);
  } finally {
    d.db.recordReportRun({
      scheduleId: s.id, ranAt: started,
      periodFrom: period.from, periodTo: period.to,
      outcome: result.outcome, source: source || 'schedule', actor: actor || null,
      recipientsN: result.recipientsN, bytes: result.bytes, rowsN: result.rowsN,
      ms: Date.now() - started, error: result.error,
    });
  }

  // Only a genuine failure is worth interrupting someone for: a skipped run is
  // a condition the page already shows, and a sent one is the normal case.
  //
  // This goes through the multi-channel send(), NOT the mailer. Reporting an
  // SMTP failure over SMTP tells nobody anything.
  if (result.outcome === 'failed' && d.notifyFailure) {
    try {
      const r = d.getRouter(s.router_id) || {};
      d.notifyFailure('Scheduled report failed: ' + s.name,
        (r.label || r.host || s.router_id) + ' — ' + (result.error || 'unknown error'));
    } catch (_) { /* delivery is best effort */ }
  }
  return result;
}

/** Guarded entry point: one run at a time per schedule. */
async function run(scheduleRow, period, opts) {
  if (_running.has(scheduleRow.id)) {
    return { outcome: 'skipped', error: 'a run is already in progress for this schedule' };
  }
  _running.add(scheduleRow.id);
  try { return await runOnce(scheduleRow, period, opts); }
  finally { _running.delete(scheduleRow.id); }
}

/** Run now, over the period a scheduled run would currently cover. */
async function runNow(scheduleRow, opts) {
  const d = _deps;
  const period = Period.periodFor(scheduleRow.frequency, Date.now(), _tz(d))
    || { from: 0, to: Date.now() };
  return run(scheduleRow, period, { source: 'manual', ...(opts || {}) });
}

/** Every schedule due right now, with the window each should cover. */
function due(now) {
  const d = _deps;
  const tz = _tz(d);
  const out = [];
  for (const row of d.db.listReportSchedules()) {
    const probe = Period.periodFor(row.frequency, now, tz);
    if (!probe) continue;
    const history = d.db.reportRunHistory(row.id, probe.from, probe.to);
    const window = Period.dueWindow(row, history, now, tz);
    if (window) out.push({ row, period: window });
  }
  return out;
}

async function tick() {
  const d = _deps;
  if (!d) return;
  for (const { row, period } of due(Date.now())) {
    try {
      await run(row, period, { source: 'schedule' });
    } catch (e) {
      d.log('[reports] ' + row.name + ': ' + ((e && e.message) || e));
    }
  }
}

function start(deps) {
  _deps = deps;
  if (_timer) return;
  _timer = setInterval(() => { tick().catch(() => {}); }, deps.tickMs || TICK_MS);
  if (_timer.unref) _timer.unref();
  // No tick on boot: a restart should not fire a fleet's worth of reports
  // before the sessions they describe have even connected.
}

function stop() {
  if (_timer) clearInterval(_timer);
  _timer = null;
  _deps = null;
  _running.clear();
}

module.exports = { TICK_MS, start, stop, tick, due, run, runNow, _running, _mayStillSend };
