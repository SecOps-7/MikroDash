'use strict';
/**
 * Composing one report email.
 *
 * Kept apart from the scheduler so the message can be built and inspected
 * without a mail server, a database or a clock.
 */

const Period = require('./period');

/** No single attachment larger than this. */
const MAX_ATTACHMENT_BYTES = 5 * 1024 * 1024;

/**
 * ...and no message larger than this in total.
 *
 * Most MTAs reject somewhere between 10 and 25 MB, and a bounce is worse than a
 * short report: the operator sees nothing at all rather than most of it.
 */
const MAX_MAIL_BYTES = 15 * 1024 * 1024;

/**
 * Fit as many attachments as the budget allows, in the order given.
 *
 * Returns what fits plus the names of what did not, so the body can say so.
 * Dropping the tail is deliberate: sections arrive in canonical order, so what
 * survives is predictable rather than whichever happened to be smallest.
 */
function fitAttachments(attachments) {
  const kept = [];
  const dropped = [];
  let total = 0;
  for (const a of attachments) {
    if (a.content.length > MAX_ATTACHMENT_BYTES || total + a.content.length > MAX_MAIL_BYTES) {
      dropped.push(a.section);
      continue;
    }
    kept.push(a);
    total += a.content.length;
  }
  return { kept, dropped, bytes: total };
}

/** `hAP AX3 — Monthly usage — July 2026`, with no line breaks anywhere. */
function subject(schedule, routerLabel, period, tz) {
  return [routerLabel, schedule.name, Period.label(schedule.frequency, period, tz)]
    .filter(Boolean).join(' — ').replace(/[\r\n]+/g, ' ');
}

/**
 * The plain-text body.
 *
 * Generated entirely from values this process computed. The only operator
 * string that reaches the message is the schedule name, and that is stripped of
 * line breaks by schedules.cleanName and again in subject() — so this cannot
 * become a channel for arbitrary content.
 */
function body({ schedule, routerLabel, period, sections, dropped, skipped, truncated, tz }) {
  const stamp = (ts) => new Date(ts).toISOString().slice(0, 16).replace('T', ' ');
  const lines = [];

  lines.push(routerLabel + ' — ' + Period.label(schedule.frequency, period, tz));
  lines.push('');
  lines.push('Covering ' + stamp(period.from) + ' to ' + stamp(period.to) + ' UTC.');
  lines.push('');

  if (sections.length) {
    lines.push('Attached:');
    for (const s of sections) {
      lines.push('  - ' + s.title + ' — ' + s.rowCount.toLocaleString() + ' rows' +
                 (s.truncated ? ' (table truncated, see the note in the PDF)' : ''));
    }
  } else {
    lines.push('No sections could be produced for this period.');
  }

  if (skipped && skipped.length) {
    lines.push('');
    lines.push('Not included:');
    for (const s of skipped) lines.push('  - ' + s.section + ' — ' + s.reason);
  }
  if (dropped && dropped.length) {
    lines.push('');
    lines.push('Left out to keep the message deliverable: ' + dropped.join(', ') + '.');
  }
  if (truncated) {
    lines.push('');
    lines.push('Some tables were truncated. Narrow the range or choose a coarser');
    lines.push('aggregation, or export the CSV from the Reports page for the full data.');
  }

  lines.push('');
  lines.push('Sent by MikroDash because a scheduled report named "' + schedule.name +
             '" is configured for this router.');
  return lines.join('\n');
}

/**
 * Recipients go in bcc, with `to` set to the sending address.
 *
 * "Customer email groups" means these are frequently different customers, and a
 * `to:` array would disclose every address to all of them.
 */
function envelope(settings, recipients) {
  return { to: settings.smtpFrom, bcc: recipients.slice() };
}

module.exports = {
  MAX_ATTACHMENT_BYTES, MAX_MAIL_BYTES,
  fitAttachments, subject, body, envelope,
};
