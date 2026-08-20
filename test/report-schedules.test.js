'use strict';
/**
 * The scheduled-report runner and its refusals (#60).
 *
 * Everything the scheduler touches is injected, so this drives it with no mail
 * server, no router and no clock — the same shape backups.test.js uses against
 * a fake ROS.
 *
 * Most of what follows is about NOT sending. A report that keeps arriving after
 * its creator lost access, or after the router it describes was deleted, is a
 * standing data leak to addresses that were never authenticated in the first
 * place.
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs   = require('fs');
const os   = require('os');
const path = require('path');

const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-sched-'));
process.env.DATA_DIR = TMP;
const db = require('../src/db');
const Scheduler = require('../src/reports/scheduler');
const Schedules = require('../src/reports/schedules');

const ROUTER = { id: 'r1', label: 'hAP AX3', host: '10.0.0.2' };
const PERIOD = { from: 1785312000000, to: 1785398400000 };

let _n = 0;

/** Records what would have been sent, instead of sending it. */
function harness(over = {}) {
  const sent = [];
  const notified = [];
  Scheduler.stop();
  Scheduler.start({
    db,
    settings: () => ({ smtpHost: 'mail.example.com', smtpFrom: 'md@example.com', displayTimezone: 'UTC' }),
    isModern: () => true,
    getRouter: () => ROUTER,
    buildReport: (section) => ({
      title: section + ' report',
      rowCount: 3,
      truncated: false,
      pdf: { columns: ['A'], rows: [{ A: '1' }], meta: { router: 'r', from: 1, to: 2, stats: [] } },
    }),
    canRead: () => true,
    mail: async (settings, message) => { sent.push(message); },
    notifyFailure: (title, body) => notified.push({ title, body }),
    log: () => {},
    tickMs: 3600000,
    ...over,
  });
  return { sent, notified };
}

function makeSchedule(over = {}) {
  const row = Schedules.validate({
    name: 'Monthly usage', sections: ['ping'], frequency: 'monthly',
    recipients: ['ops@example.com', 'noc@example.com'], sendHour: 7,
    ...(over.input || {}),
  }, { id: over.id || 'sched-' + (++_n),
       routerId: 'r1',
       createdBy: over.createdBy === undefined ? 'u1' : over.createdBy });
  db.upsertReportSchedule(row);
  return db.getReportSchedule(row.id);
}

test('setup', () => { db.open(); });

test('a run builds every section, mails it once, and records the outcome', async () => {
  const h = harness();
  const s = makeSchedule({ input: { sections: ['ping', 'connectivity'] } });

  const r = await Scheduler.run(s, PERIOD, { source: 'schedule' });
  assert.equal(r.outcome, 'sent', r.error || '');
  assert.equal(h.sent.length, 1);
  assert.equal(h.sent[0].attachments.length, 2, 'one PDF per section');
  assert.match(h.sent[0].subject, /hAP AX3/);

  const runs = db.listReportRuns(s.id, 10);
  assert.equal(runs.length, 1);
  assert.equal(runs[0].outcome, 'sent');
  assert.equal(runs[0].recipients_n, 2);
});

test('recipients go in bcc, never in to', async () => {
  // These are frequently different customers; a `to:` array would disclose
  // every address to all of them.
  const h = harness();
  const s = makeSchedule();
  await Scheduler.run(s, PERIOD, {});
  assert.deepEqual(h.sent[0].bcc, ['ops@example.com', 'noc@example.com']);
  assert.equal(h.sent[0].to, 'md@example.com', 'to is the sending address');
});

// ── The refusals ───────────────────────────────────────────────────────────

test('a deleted router disables the schedule and sends nothing', async () => {
  const h = harness({ getRouter: () => null });
  const s = makeSchedule();
  const r = await Scheduler.run(s, PERIOD, {});
  assert.equal(r.outcome, 'skipped');
  assert.equal(h.sent.length, 0);
  assert.equal(db.getReportSchedule(s.id).enabled, 0);
  assert.match(db.getReportSchedule(s.id).disabled_reason, /no longer exists/);
});

test('a creator who lost access disables the schedule and sends nothing', async () => {
  // The whole point of re-asking at send time rather than trusting creation
  // time: otherwise this keeps mailing router history to third parties forever.
  const h = harness({ canRead: () => false });
  const s = makeSchedule();
  const r = await Scheduler.run(s, PERIOD, {});
  assert.equal(r.outcome, 'skipped');
  assert.equal(h.sent.length, 0);
  assert.match(db.getReportSchedule(s.id).disabled_reason, /no longer has report access/);
});

test('a NULL creator is refused on a modern install', async () => {
  // Rbac.can({ userId: null }) denies, so a naive check would disable every
  // schedule the moment auth is switched on. It must be explicit, not incidental.
  const h = harness({ isModern: () => true, canRead: () => false });
  const s = makeSchedule({ createdBy: null });
  const r = await Scheduler.run(s, PERIOD, {});
  assert.equal(r.outcome, 'skipped');
  assert.equal(h.sent.length, 0);
  assert.match(db.getReportSchedule(s.id).disabled_reason, /before authentication/);
});

test('a NULL creator still runs while authentication is off', async () => {
  // The mirror case: on an install that has never had auth, a NULL creator is
  // the normal state and must not be read as a revocation.
  const h = harness({ isModern: () => false, canRead: () => false });
  const s = makeSchedule({ createdBy: null });
  const r = await Scheduler.run(s, PERIOD, {});
  assert.equal(r.outcome, 'sent', r.error || '');
  assert.equal(h.sent.length, 1);
});

test('an unconfigured mail server is skipped, not retried every tick', async () => {
  const h = harness({ settings: () => ({ smtpHost: '', smtpFrom: '', displayTimezone: 'UTC' }) });
  const s = makeSchedule();
  const r = await Scheduler.run(s, PERIOD, {});
  assert.equal(r.outcome, 'skipped');
  assert.match(r.error, /SMTP is not configured/);
  assert.equal(h.sent.length, 0);
  // Skipped rather than failed, so period.js will not retry it — retries are
  // reserved for transient failures, and this one is fixable by a human.
  assert.equal(db.getReportSchedule(s.id).enabled, 1);
});

test('a section that cannot be built costs the section, not the report', async () => {
  // Interfaces get renamed on routers. Losing traffic should not lose ping too.
  const h = harness({
    buildReport: (section) => {
      if (section === 'traffic') throw new Error('traffic reports need an interface');
      return { title: section + ' report', rowCount: 1, truncated: false,
               pdf: { columns: ['A'], rows: [{ A: '1' }], meta: { router: 'r', from: 1, to: 2, stats: [] } } };
    },
  });
  const s = makeSchedule({ input: { sections: ['ping', 'traffic'], iface: 'ether1' } });
  const r = await Scheduler.run(s, PERIOD, {});
  assert.equal(r.outcome, 'sent');
  assert.equal(h.sent[0].attachments.length, 1, 'only the section that built');
  assert.equal(r.skipped.length, 1);
  assert.match(h.sent[0].text, /Not included/);
});

test('a failure is recorded and notified through the other channels', async () => {
  // Reporting an SMTP failure over SMTP reaches nobody, so the notification
  // goes through the multi-channel send(), not the mailer.
  const h = harness({ mail: async () => { throw new Error('connection refused'); } });
  const s = makeSchedule();
  const r = await Scheduler.run(s, PERIOD, {});
  assert.equal(r.outcome, 'failed');
  assert.match(r.error, /connection refused/);
  assert.equal(db.listReportRuns(s.id, 5)[0].outcome, 'failed');
  assert.equal(h.notified.length, 1);
  assert.match(h.notified[0].title, /Scheduled report failed/);
});

// ── Due-ness through the database ──────────────────────────────────────────

test('a schedule sends once per period however often the tick runs', async () => {
  const h = harness();
  // created_at must be set on INSERT: upsert deliberately does not touch it on
  // conflict, since editing a schedule should not reset when it was made. A
  // schedule created after today's fire hour correctly waits for tomorrow, so
  // backdating it here is what makes it due at all.
  db.upsertReportSchedule(Schedules.validate(
    { name: 'Daily', sections: ['ping'], frequency: 'daily', sendHour: 0,
      recipients: ['ops@example.com'] },
    { id: 'sched-daily', routerId: 'r1', createdBy: 'u1', createdAt: 1 }));
  const s = db.getReportSchedule('sched-daily');
  assert.equal(s.created_at, 1);

  await Scheduler.tick();
  assert.equal(h.sent.length, 1, 'due on the first tick');
  await Scheduler.tick();
  assert.equal(h.sent.length, 1, 'not due again in the same period');
});

// ── Recipient validation ───────────────────────────────────────────────────

test('an address carrying a newline is refused', () => {
  // The one input here that could turn this into someone else's mail relay.
  assert.throws(() => Schedules.cleanRecipients(['a@b.com\r\nBcc: victim@x.com']), /not allowed/);
  assert.throws(() => Schedules.cleanRecipients(['a@b.com\nX-Header: y']), /not allowed/);
});

test('addresses are shape-checked, de-duplicated and capped', () => {
  assert.throws(() => Schedules.cleanRecipients(['nope']), /is not an email address/);
  assert.throws(() => Schedules.cleanRecipients([]), /at least one recipient/);
  assert.deepEqual(Schedules.cleanRecipients(['A@b.com', 'a@B.com']), ['A@b.com'],
    'the same address twice is one delivery');
  const many = Array.from({ length: Schedules.MAX_RECIPIENTS + 1 }, (_, i) => 'u' + i + '@x.com');
  assert.throws(() => Schedules.cleanRecipients(many), /at most/);
});

test('a schedule name cannot carry a line break into the subject', () => {
  assert.equal(Schedules.cleanName('Monthly\r\nBcc: x@y.com'), 'Monthly Bcc: x@y.com');
  assert.throws(() => Schedules.cleanName('   '), /needs a name/);
});

test('aggregation defaults keep a monthly report readable', () => {
  // An unaggregated month is ~43,200 one-minute rows per series.
  assert.equal(Schedules.aggregateFor({ frequency: 'daily' }), 'hour');
  assert.equal(Schedules.aggregateFor({ frequency: 'weekly' }), 'hour');
  assert.equal(Schedules.aggregateFor({ frequency: 'monthly' }), 'day');
  assert.equal(Schedules.aggregateFor({ frequency: 'monthly', aggregate: 'hour' }), 'hour',
    'an explicit choice still wins');
});

test('traffic and bandwidth schedules require an interface at creation', () => {
  assert.throws(() => Schedules.validate(
    { name: 'x', sections: ['traffic'], frequency: 'daily', recipients: ['a@b.com'] },
    { routerId: 'r1' }), /need an interface/);
});

// ── Structural invariants ──────────────────────────────────────────────────

const SRC = (n) => fs.readFileSync(path.join(__dirname, '..', 'src', n), 'utf8');

test('a retention sweep can never delete a schedule or its history', () => {
  const dbSrc = SRC('db.js');
  const purge = dbSrc.slice(dbSrc.indexOf('const PURGE_TABLES'), dbSrc.indexOf('const PURGE_TYPES'));
  assert.ok(!purge.includes('report_schedules') && !purge.includes('report_runs'));

  const start = dbSrc.indexOf('function deleteRouterData');
  const del = dbSrc.slice(start, dbSrc.indexOf('}', dbSrc.indexOf('console.log', start)));
  assert.ok(!del.includes('report_'), 'schedules are configuration, not time-series');
});

test('but removing a router does delete its schedules, explicitly', () => {
  // The counterpart to the test above: "unreachable from a sweep" must not
  // quietly become "never cleaned up". A schedule for a deleted router is a
  // live outbound email loop.
  assert.ok(SRC('index.js').includes('db.deleteReportSchedulesForRouter(deletedId)'));
});

test('the multi-channel send() never grew an attachments parameter', () => {
  // Three of its four channels cannot express one, so widening it would be a
  // contract that lies.
  assert.match(SRC('notifier.js'), /async function send\(settings, title, body\)/);
});

test('the scheduler re-checks access at send time', () => {
  assert.ok(SRC('reports/scheduler.js').includes('canRead('), 'must ask, not assume');
  assert.match(SRC('index.js'), /canRead:.*'router:history'/,
    'and index.js must wire it to the real permission');
});

test('scheduling is a write-level grant, and not router:write', () => {
  const rbac = SRC('rbac.js');
  assert.ok(rbac.includes("'router:schedule'"), 'the permission exists');
  assert.match(rbac, /reports:\s*\['router:schedule'\]/, 'conferred by reports write');
  // router:write is conferred by ANY write page via WRITE_CONFERS_ALWAYS, so
  // gating on it would leak scheduling in from an unrelated page.
  assert.ok(!/WRITE_CONFERS_ALWAYS = Object\.freeze\(\[[^\]]*router:schedule/.test(rbac));
});

test('teardown', () => {
  Scheduler.stop();
  db.close();
  fs.rmSync(TMP, { recursive: true, force: true });
});
