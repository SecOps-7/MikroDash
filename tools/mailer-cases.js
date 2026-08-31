'use strict';
/**
 * `src/reports/mailer.js` — the four functions that decide what a scheduled
 * report email SAYS, and what fits in it.
 *
 * All four are pure and all four are exported, so this requires the real module.
 * The live file's own header says why they were split out: "so the message can
 * be built and inspected without a mail server, a database or a clock" — which
 * is exactly what makes them portable ahead of the transport.
 *
 * ---- THE PARTS THAT ARE NOT ARITHMETIC ------------------------------------
 *
 * `fitAttachments` DROPS THE TAIL, not the largest. The live comment is explicit:
 * "sections arrive in canonical order, so what survives is predictable rather
 * than whichever happened to be smallest". A port that packed greedily by size
 * would fit more in and be wrong — two operators with the same schedule would
 * get different sections.
 *
 * `envelope` puts every recipient in BCC and sets `to` to the sending address.
 * Also from the live comment: "'Customer email groups' means these are
 * frequently different customers, and a `to:` array would disclose every address
 * to all of them." That is a privacy property, not a formatting choice, and it
 * is pinned here so a port cannot quietly simplify it into a `to:` list.
 *
 * `subject` strips CR and LF after joining. The schedule name is the only
 * operator-supplied string that reaches a message, and a newline in a subject is
 * a header injection. `schedules.cleanName` strips it on the way in and this
 * strips it again on the way out; the corpus carries a name with both characters
 * so the port cannot drop the second guard as redundant.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/mailer-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const M = require(path.join(SRC, 'src', 'reports', 'mailer.js'));

const MB = 1024 * 1024;
const att = (section, bytes) => ({ section, content: Buffer.alloc(bytes), filename: section + '.pdf' });

// ---- fitAttachments ------------------------------------------------------
const FIT_CASES = {
  'all fit': [att('ping', 1000), att('traffic', 2000)],
  'none at all': [],
  // Exactly on each limit, and one byte past it. Both limits are `>`, so the
  // boundary value FITS -- which is the opposite of what a careless port does.
  'one exactly at the per-attachment cap': [att('ping', M.MAX_ATTACHMENT_BYTES)],
  'one just over the per-attachment cap': [att('ping', M.MAX_ATTACHMENT_BYTES + 1)],
  'total exactly at the message cap': [att('a', 5 * MB), att('b', 5 * MB), att('c', 5 * MB)],
  'total just over the message cap': [att('a', 5 * MB), att('b', 5 * MB), att('c', 5 * MB + 1)],
  // THE TAIL IS DROPPED, not the biggest: a small section after an oversized one
  // is skipped too if the budget is already spent, but an oversized one in the
  // MIDDLE does not stop the ones after it from being considered.
  'an oversized one in the middle': [
    att('small-first', 100), att('huge', M.MAX_ATTACHMENT_BYTES + 1), att('small-last', 100)],
  // OVERFLOW SKIPS ONE, IT DOES NOT STOP. Three 4 MB sections fit inside the
  // 15 MB message; the fourth would not, so it is dropped -- and the tiny fifth
  // is then still considered and still fits. `continue`, not `break`.
  //
  // It also separates the two caps: every one of these is under the 5 MB
  // per-attachment limit, so only the message total can reject anything.
  'overflow skips one without stopping': [
    att('a', 4 * MB), att('b', 4 * MB), att('c', 4 * MB), att('d', 4 * MB), att('tiny', 10)],
  'empty attachment': [att('empty', 0)],
};
const fit = Object.entries(FIT_CASES).map(([name, list]) => {
  const r = M.fitAttachments(list);
  return { name, sizes: list.map((a) => ({ section: a.section, bytes: a.content.length })),
    kept: r.kept.map((a) => a.section), dropped: r.dropped, bytes: r.bytes };
});

// ---- subject -------------------------------------------------------------
const SUBJECT_CASES = [
  { schedule: { name: 'Monthly usage', frequency: 'monthly' }, routerLabel: 'hAP AX3' },
  { schedule: { name: 'Weekly', frequency: 'weekly' }, routerLabel: 'cAP AX' },
  { schedule: { name: 'Daily', frequency: 'daily' }, routerLabel: 'Router 1' },
  // Empty parts are FILTERED OUT, so the separator does not double up.
  { schedule: { name: '', frequency: 'monthly' }, routerLabel: 'hAP AX3' },
  { schedule: { name: 'No Router', frequency: 'monthly' }, routerLabel: '' },
  // Header injection, both characters and a combination.
  { schedule: { name: 'Bad\r\nBcc: evil@example.net', frequency: 'monthly' }, routerLabel: 'R' },
  { schedule: { name: 'Bare\nNewline', frequency: 'daily' }, routerLabel: 'R' },
  { schedule: { name: 'Bare\rReturn', frequency: 'daily' }, routerLabel: 'R' },
  { schedule: { name: 'Many\r\n\r\n\nBreaks', frequency: 'daily' }, routerLabel: 'R' },
  // Non-ASCII, which the transport has to encode even though this does not.
  { schedule: { name: 'Café — Trafic', frequency: 'monthly' }, routerLabel: 'Röuter' },
];
const P = { from: Date.UTC(2026, 6, 1), to: Date.UTC(2026, 7, 1) };
const subject = SUBJECT_CASES.map((c, i) => ({
  i, schedule: c.schedule, routerLabel: c.routerLabel, period: P, tz: '',
  out: M.subject(c.schedule, c.routerLabel, P, ''),
}));

// ---- body ----------------------------------------------------------------
const BODY_CASES = {
  'two sections': {
    schedule: { name: 'Monthly usage', frequency: 'monthly' }, routerLabel: 'hAP AX3', period: P,
    sections: [{ title: 'Ping Stability Report', rowCount: 1234, truncated: false },
               { title: 'Traffic History Report', rowCount: 43200, truncated: true }],
    dropped: [], skipped: [], truncated: true, tz: '' },
  'no sections at all': {
    schedule: { name: 'Empty', frequency: 'daily' }, routerLabel: 'R', period: P,
    sections: [], dropped: [], skipped: [], truncated: false, tz: '' },
  'with skipped and dropped': {
    schedule: { name: 'Partial', frequency: 'weekly' }, routerLabel: 'R', period: P,
    sections: [{ title: 'Alert Events Report', rowCount: 7, truncated: false }],
    dropped: ['traffic', 'bandwidth'],
    skipped: [{ section: 'connectivity', reason: 'no interface configured' }],
    truncated: false, tz: '' },
  // rowCount goes through toLocaleString, so the grouping trap applies here too.
  'a million rows': {
    schedule: { name: 'Big', frequency: 'monthly' }, routerLabel: 'R', period: P,
    sections: [{ title: 'Traffic History Report', rowCount: 1234567, truncated: true }],
    dropped: [], skipped: [], truncated: true, tz: '' },
  'a timezone set': {
    schedule: { name: 'TZ', frequency: 'monthly' }, routerLabel: 'R', period: P,
    sections: [{ title: 'Ping Stability Report', rowCount: 1, truncated: false }],
    dropped: [], skipped: [], truncated: false, tz: 'Australia/Adelaide' },
};
const body = Object.entries(BODY_CASES).map(([name, o]) => ({ name, in: o, out: M.body(o) }));

// ---- envelope ------------------------------------------------------------
const envelope = [
  { settings: { smtpFrom: 'reports@example.com' }, recipients: ['a@example.net', 'b@example.org'] },
  { settings: { smtpFrom: 'reports@example.com' }, recipients: [] },
  { settings: { smtpFrom: '' }, recipients: ['a@example.net'] },
].map((c) => ({ ...c, out: M.envelope(c.settings, c.recipients) }));

// ---- BELIEVABILITY -------------------------------------------------------
{
  const byName = Object.fromEntries(fit.map((f) => [f.name, f]));
  assert.deepEqual(byName['one exactly at the per-attachment cap'].dropped, [],
    'an attachment of exactly MAX_ATTACHMENT_BYTES was dropped — the test is `>`, not `>=`');
  assert.deepEqual(byName['one just over the per-attachment cap'].dropped, ['ping'],
    'one byte over the cap was kept');
  assert.deepEqual(byName['total exactly at the message cap'].dropped, [],
    'a total of exactly MAX_MAIL_BYTES was rejected');
  assert.deepEqual(byName['total just over the message cap'].dropped, ['c'],
    'one byte over the message cap was kept');
  assert.deepEqual(byName['an oversized one in the middle'].kept, ['small-first', 'small-last'],
    'an oversized attachment stopped the ones after it from being considered');
  assert.deepEqual(byName['overflow skips one without stopping'].kept, ['a', 'b', 'c', 'tiny'],
    'an overflowing attachment stopped the loop instead of being skipped');
  assert.deepEqual(byName['overflow skips one without stopping'].dropped, ['d'],
    'the wrong attachment was dropped');

  for (const s of subject) {
    assert.ok(!/[\r\n]/.test(s.out), 'a subject kept a line break: ' + JSON.stringify(s.out));
  }
  assert.ok(subject[5].out.includes('Bcc: evil@example.net'),
    'the injected text vanished entirely — this case is meant to prove it is NEUTRALISED, not removed');
  assert.equal(subject.find((s) => s.schedule.name === '').out.split(' — ').length, 2,
    'an empty schedule name left a doubled separator');

  const b = Object.fromEntries(body.map((x) => [x.name, x.out]));
  assert.ok(b['no sections at all'].includes('No sections could be produced'),
    'an empty report did not say so');
  assert.ok(b['with skipped and dropped'].includes('Not included:'), 'skipped sections are unreported');
  assert.ok(b['with skipped and dropped'].includes('Left out to keep the message deliverable'),
    'dropped attachments are unreported');
  assert.ok(b['a million rows'].includes('1,234,567'),
    'the row count is not group-separated — toLocaleString again');
  assert.ok(b['two sections'].includes('(table truncated'), 'a truncated section did not say so');
  assert.notEqual(b['a timezone set'], b['two sections'], 'the timezone changed nothing');

  assert.deepEqual(envelope[0].out.to, 'reports@example.com',
    'the envelope `to` is not the sending address');
  assert.deepEqual(envelope[0].out.bcc, ['a@example.net', 'b@example.org'],
    'recipients are not in bcc — every customer would see every other address');
}

const OUT = path.join(ROOT, 'testdata', 'mailer-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/mailer-cases.js from the live src/reports/mailer.js. Do not edit.',
  maxAttachmentBytes: M.MAX_ATTACHMENT_BYTES, maxMailBytes: M.MAX_MAIL_BYTES,
  fit, subject, body, envelope,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('mailer-cases: testdata/mailer-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('mailer-cases: up to date');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('mailer-cases: wrote ' + fit.length + ' fit, ' + subject.length + ' subject, '
    + body.length + ' body, ' + envelope.length + ' envelope cases');
}
