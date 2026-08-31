'use strict';
/**
 * The decisions a report RUN makes before any mail leaves — and the one guard
 * that can switch a schedule off.
 *
 * `runOnce` in `src/reports/scheduler.js` is orchestration: it fetches, renders,
 * mails and records. Most of that is I/O and belongs to the server. What is
 * portable, and what this pins, is the part that decides WHAT HAPPENS:
 *
 *   _mayStillSend      whether the schedule's creator may still read the router
 *   the filename       what each attachment is called
 *   the failure text   what "no section could be produced" says when it says it
 *
 * ---- WHY _mayStillSend IS THE IMPORTANT ONE -------------------------------
 *
 * It is an authorisation check that runs LONG AFTER the request that created the
 * schedule. A schedule mails a router's traffic to a fixed recipient list every
 * month; if the person who set it up loses access to that router, the mail must
 * stop. Nothing else in the run path re-checks that — the recipients are stored,
 * the router is stored, and the tick has no session.
 *
 * It has two failure modes and they are not the same:
 *
 *   no `created_by` at all   a schedule from before authentication existed. It is
 *                            refused only when the install is "modern" — one that
 *                            HAS authentication — because on an install without
 *                            it there is nobody to attribute the schedule to and
 *                            refusing would break every existing schedule.
 *   creator lost access      refused outright.
 *
 * Both DISABLE the schedule rather than skipping the run, which is the caller's
 * doing (`setReportScheduleEnabled(id, false, reason)`) but is why the reason
 * strings are pinned: they are what the operator sees on the Reports page when a
 * schedule has switched itself off, and "recreate it" is actionable where a bare
 * "not permitted" is not.
 *
 * Runs in the CONTAINER: `scheduler.js` requires `pdf.js`, which requires
 * pdfkit, so the module cannot even be loaded on the host. Nothing it measures
 * needs pdfkit — the dependency is transitive and the tool never renders — but
 * requiring a module runs its whole require tree.
 *
 *   docker exec -e MIKRODASH_SRC=/app mikrodash \
 *     node /work/tools/sendnow-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const Sched = require(path.join(SRC, 'src', 'reports', 'scheduler.js'));

// ---- _mayStillSend -------------------------------------------------------
//
// `d` is the dependency bag the scheduler is started with. Only two of its
// members are reached from here, and both are stubbed per case.
const MAY_CASES = [
  { name: 'no creator on a modern install', row: { created_by: null, router_id: 'r1' },
    modern: true, canRead: false },
  { name: 'no creator on a legacy install', row: { created_by: null, router_id: 'r1' },
    modern: false, canRead: false },
  { name: 'no creator, empty string, modern', row: { created_by: '', router_id: 'r1' },
    modern: true, canRead: true },
  { name: 'creator still has access', row: { created_by: 'alice', router_id: 'r1' },
    modern: true, canRead: true },
  { name: 'creator lost access', row: { created_by: 'alice', router_id: 'r1' },
    modern: true, canRead: false },
  // The legacy branch is only reached when there is NO creator: a named creator
  // who cannot read is refused whether or not the install is modern.
  { name: 'creator lost access on a legacy install', row: { created_by: 'alice', router_id: 'r1' },
    modern: false, canRead: false },
];
const mayStillSend = MAY_CASES.map((c) => ({
  name: c.name, createdBy: c.row.created_by, modern: c.modern, canRead: c.canRead,
  out: Sched._mayStillSend(c.row, { isModern: () => c.modern, canRead: () => c.canRead }),
}));

// ---- the attachment filename ---------------------------------------------
//
// `built.title.replace(/[^A-Za-z0-9]+/g, '-').toLowerCase() + '.pdf'`, lifted as
// the expression it is. A RUN of non-alphanumerics collapses to ONE dash, and
// leading/trailing ones are NOT trimmed — "Café Report" becomes "caf-report.pdf"
// but "— Report" becomes "-report.pdf". Reproduced, not tidied.
const nameOf = (title) => title.replace(/[^A-Za-z0-9]+/g, '-').toLowerCase() + '.pdf';
const filenames = [
  'Ping Stability Report', 'Traffic History Report', 'Bandwidth Usage Report',
  'Alert Events Report', 'Connectivity Report',
  'Café — Trafic', '  leading and trailing  ', 'ALLCAPS', 'a', '',
  'lots---of___separators', '99 Percent', 'Ünïcödé Ønly',
].map((title) => ({ title, out: nameOf(title) }));

// ---- the "no section" failure text ---------------------------------------
//
// `'no section could be produced' + (skipped.length ? ': ' + skipped[0].reason : '')`
// — the FIRST skipped reason only, however many there are.
const failText = (skipped) => 'no section could be produced'
  + (skipped.length ? ': ' + skipped[0].reason : '');
const failure = [
  [], [{ reason: 'no interface configured' }],
  [{ reason: 'first reason' }, { reason: 'second reason' }],
].map((skipped) => ({ skipped: skipped.map((s) => s.reason), out: failText(skipped) }));

// ---- BELIEVABILITY -------------------------------------------------------
{
  const by = Object.fromEntries(mayStillSend.map((m) => [m.name, m.out]));
  assert.equal(by['no creator on a modern install'].ok, false,
    'a schedule with no creator was allowed to send on an install that HAS authentication');
  assert.ok(by['no creator on a modern install'].reason.includes('recreate it'),
    'the reason is not actionable — the operator has to be told what to do about it');
  assert.equal(by['no creator on a legacy install'].ok, true,
    'a legacy install refused a schedule it has nobody to attribute');
  assert.equal(by['creator still has access'].ok, true, 'a valid schedule was refused');
  assert.equal(by['creator lost access'].ok, false,
    'a creator who lost access can still mail the router — this is the whole point of the guard');
  assert.equal(by['creator lost access on a legacy install'].ok, false,
    'the legacy branch swallowed a named creator who cannot read');
  assert.notEqual(by['no creator on a modern install'].reason, by['creator lost access'].reason,
    'the two refusals give the same reason — they are different problems');
  // An empty-string creator is falsy and takes the no-creator branch.
  assert.equal(by['no creator, empty string, modern'].ok, false,
    'an empty creator string was treated as a named creator');

  const f = Object.fromEntries(filenames.map((x) => [x.title, x.out]));
  assert.equal(f['Ping Stability Report'], 'ping-stability-report.pdf');
  assert.equal(f['lots---of___separators'], 'lots-of-separators.pdf',
    'a run of separators did not collapse to one dash');
  assert.ok(f['  leading and trailing  '].startsWith('-'),
    'leading separators are being trimmed — the live expression does not trim');
  assert.equal(f['Ünïcödé Ønly'], '-n-c-d-nly.pdf',
    'non-ASCII letters are not being replaced');

  assert.equal(failure[0].out, 'no section could be produced', 'the bare message changed');
  assert.ok(failure[2].out.endsWith('first reason'),
    'the failure text names a reason other than the first');
}

const OUT = path.join(ROOT, 'testdata', 'sendnow-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/sendnow-cases.js from the live src/reports/scheduler.js. Do not edit.',
  mayStillSend, filenames, failure,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('sendnow-cases: testdata/sendnow-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('sendnow-cases: up to date');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('sendnow-cases: wrote ' + mayStillSend.length + ' guard, '
    + filenames.length + ' filename, ' + failure.length + ' failure-text cases');
}
