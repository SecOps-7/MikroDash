'use strict';
/**
 * Validating a report schedule before it reaches the database.
 *
 * ── Recipients are the security-relevant part ──────────────────────────────
 *
 * Everything else here is ordinary shape-checking. The recipient list is not:
 * these addresses go straight into a mail envelope, and an address containing a
 * newline injects arbitrary mail headers — a Bcc of its own, a forged From, a
 * second body. That is the one input in this feature that could turn a
 * reporting tool into someone else's mail relay.
 *
 * So addresses are checked against a deliberately narrow shape rather than a
 * permissive "is this RFC 5322" pattern, and are always handed to nodemailer as
 * an ARRAY. Never join them into a string; the joining is exactly where the
 * injection lands.
 *
 * Worth noting for context: `smtpTo` in the settings POST gets only a 256-char
 * trim with no `@` check at all, and `userNotify.emailTo` checks only for the
 * presence of an `@`. Neither is touched here — they are older and narrower —
 * but this is the standard the new path holds itself to.
 */

const Build = require('./build');
const Period = require('./period');

/** More than this and it is a mailing list, which is a different feature. */
const MAX_RECIPIENTS = 20;

/** RFC 5321 caps a path at 256 including the angle brackets. */
const MAX_ADDRESS = 254;

const MAX_NAME = 80;

/** Anything that could break out of an address and into the headers. */
const UNSAFE_IN_ADDRESS = /[\r\n,;<>"\s\\]/;

/** Deliberately narrow: local@domain.tld, no display names, no comments. */
const ADDRESS_SHAPE = /^[^@]{1,64}@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$/;

/**
 * A schedule name reaches the email subject, which is a mail header. It cannot
 * be allowed to carry a line break any more than an address can.
 */
function cleanName(raw) {
  const s = String(raw == null ? '' : raw).replace(/[\r\n]+/g, ' ').trim();
  if (!s) throw new Error('a schedule needs a name');
  return s.slice(0, MAX_NAME);
}

/**
 * Normalise and check the recipient list.
 *
 * De-duplicated case-insensitively, because the same address twice is one
 * delivery and two chances to trip a rate limit.
 */
function cleanRecipients(raw) {
  const list = Array.isArray(raw) ? raw : String(raw == null ? '' : raw).split(/\n+/);
  const seen = new Map();
  for (const entry of list) {
    const addr = String(entry == null ? '' : entry).trim();
    if (!addr) continue;
    if (addr.length > MAX_ADDRESS) throw new Error('an email address is too long');
    if (UNSAFE_IN_ADDRESS.test(addr)) {
      // The message names the class of problem rather than echoing the offending
      // characters, so an error surfaced in the UI cannot itself carry the
      // injection attempt back onto the page.
      throw new Error('an email address contains characters that are not allowed');
    }
    if (!ADDRESS_SHAPE.test(addr)) {
      throw new Error('"' + addr.slice(0, 60) + '" is not an email address');
    }
    const key = addr.toLowerCase();
    if (!seen.has(key)) seen.set(key, addr);
  }
  const out = [...seen.values()];
  if (!out.length) throw new Error('a schedule needs at least one recipient');
  if (out.length > MAX_RECIPIENTS) {
    throw new Error('at most ' + MAX_RECIPIENTS + ' recipients per schedule');
  }
  return out;
}

function cleanSections(raw) {
  const list = Array.isArray(raw) ? raw : [];
  const chosen = new Set(list.filter(s => Build.SECTIONS.includes(s)));
  if (!chosen.size) throw new Error('a schedule needs at least one section');
  // Canonical order, so a report always reads the same way round.
  return Build.SECTIONS.filter(s => chosen.has(s));
}

/**
 * Aggregation defaults by frequency.
 *
 * An unaggregated month is ~43,200 one-minute rows per series. Aggregating by
 * default is what keeps a monthly report a readable document rather than a
 * truncated one; an operator can still override to any bucket.
 */
const DEFAULT_AGGREGATE = Object.freeze({
  daily: 'hour',      // 24 buckets
  weekly: 'hour',     // 168
  monthly: 'day',     // ~30
});

function aggregateFor(schedule) {
  return schedule.aggregate || DEFAULT_AGGREGATE[schedule.frequency] || 'day';
}

/**
 * Validate a whole schedule as submitted by a browser.
 *
 * Throws with a message meant to be shown to the operator; callers pass it
 * through sanitizeErr on the way out, as every other route does.
 */
function validate(input, { id, routerId, createdBy, createdAt } = {}) {
  const r = input || {};

  if (!Period.FREQUENCIES.includes(r.frequency)) {
    throw new Error('frequency must be one of ' + Period.FREQUENCIES.join(', '));
  }

  const sections = cleanSections(r.sections);
  const iface = String(r.iface || r.interface || '').trim().slice(0, 128);
  const needsIface = sections.some(s => Build.NEEDS_INTERFACE.includes(s));
  if (needsIface && !iface) {
    throw new Error('traffic and bandwidth reports need an interface');
  }

  let sendHour = Math.trunc(Number(r.sendHour));
  if (!Number.isFinite(sendHour)) sendHour = 7;
  sendHour = Math.min(23, Math.max(0, sendHour));

  const aggregate = ['hour', 'day', 'week', 'month'].includes(r.aggregate) ? r.aggregate : '';

  const now = Date.now();
  return {
    id,
    routerId,
    name: cleanName(r.name),
    sections,
    iface: iface || null,
    aggregate,
    recipients: cleanRecipients(r.recipients),
    frequency: r.frequency,
    sendHour,
    enabled: r.enabled === undefined ? true : !!r.enabled,
    disabledReason: null,
    createdBy: createdBy || null,
    createdAt: createdAt || now,
    updatedAt: now,
  };
}

/** A stored row, decoded for use. The JSON columns come back as strings. */
function decode(row) {
  if (!row) return null;
  const parse = (s, fallback) => {
    try { const v = JSON.parse(s); return Array.isArray(v) ? v : fallback; }
    catch (_) { return fallback; }
  };
  return {
    ...row,
    sections: parse(row.sections, []),
    recipients: parse(row.recipients, []),
    enabled: !!row.enabled,
  };
}

/** What a browser is allowed to see. Recipients included: they are not secrets. */
function toPublic(row) {
  const d = decode(row);
  if (!d) return null;
  return {
    id: d.id, routerId: d.router_id, name: d.name, sections: d.sections,
    iface: d.interface || '', aggregate: d.aggregate || '',
    recipients: d.recipients, frequency: d.frequency, sendHour: d.send_hour,
    enabled: d.enabled, disabledReason: d.disabled_reason || null,
    createdAt: d.created_at, updatedAt: d.updated_at,
  };
}

module.exports = {
  MAX_RECIPIENTS, MAX_ADDRESS, MAX_NAME, DEFAULT_AGGREGATE,
  cleanName, cleanRecipients, cleanSections, aggregateFor, validate, decode, toPublic,
};
