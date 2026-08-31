'use strict';
/**
 * THE NOTIFICATION BELL, live against ported.
 *
 * Five ids and one list, and four rules underneath that each read as an
 * implementation detail until they are missing:
 *
 *   THE DOT MEANS UNACKNOWLEDGED **OPEN** ALERTS.
 *     Not "any alert" — a resolved one is history and its dot would never go
 *     out. Not "any open alert" either: acknowledging is how an operator says
 *     they have seen something, and a dot that stayed lit through that would
 *     train them to ignore it.
 *
 *   ACKNOWLEDGING REMOVES AN ALERT FROM THE PANEL, NOT FROM THE LIST.
 *     That is what makes "Clear all" clear anything. The rows stay in memory so
 *     a later `alert:resolved` can still find them by id, and stay in the
 *     database for Reports. So an acknowledged alert that is STILL OPEN is
 *     invisible by design — filtering on "is open" instead leaves it on screen
 *     and makes the button look broken.
 *
 *   A NEW ALERT REPLACES THE OPEN ONE FOR THE SAME KEY.
 *     Without it a flapping interface fills all hundred slots in under an hour
 *     and buries everything else. Only the OPEN entry is replaced; a resolved
 *     one for the same key is history and stays.
 *
 *   A RESOLVED ROW IS TIMED FROM WHEN IT RESOLVED.
 *     Timing it from `firedAt` would say "3h ago" about something that ended a
 *     minute ago.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/notif-bell-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('notif-bell-check');
// ROUTED THROUGH liveSource, which returns '' when the reference is gone rather
// than throwing ENOENT. Everything this gate takes from that source is frozen
// below; the live half itself is transcribed into this file and needs nothing.
const app = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

/** What this gate covers, for element-coverage-audit. Declared before any work. */
const COVERS = ['notifDot', 'notifList', 'notifPanel', 'notifToggleBtn', 'notifClearBtn'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

// The cap, read from the live source rather than retyped.
const MAX = G.value('MAX_ALERTS', () => Number((app.match(/var MAX_ALERTS\s*=\s*(\d+)/) || [])[1]));
// GUARDED, and this is the shape a guard is FOR: each asks the live SOURCE a
// question — is this string still in it — which is unanswerable once the source
// is gone. Contrast a frozen comparison, which answers fine without one and must
// never be guarded (LOOP.md 3n).
if (LIFT.hasReference(ROOT)) {
  assert.ok(MAX > 0, 'could not read MAX_ALERTS from the live app.js');
  for (const s of ['notif-empty', 'notif-sep', 'Recently resolved', 'Acknowledge', 'just now']) {
    assert.ok(app.includes(s), 'the live bell no longer contains ' + JSON.stringify(s));
  }
}
assert.ok(MAX > 0, 'MAX_ALERTS is not a positive number');

// LIFTED, not retyped.
//
// The first version of this line was hand-written from memory and used `&#39;`
// where the live app uses `&#039;`. Both are valid HTML and render identically,
// so nothing was broken — but the gate then reported a difference and blamed the
// PORT for it, which is the worst way for a comparison to fail: it sends someone
// to change correct code. `L.line` takes the real one.
const L = LIFT;
const escSrc = G.value('the live esc() source', () => L.line(app, 'function esc('));
// eslint-disable-next-line no-new-func
const esc = new Function('return ' + escSrc.replace(/^function esc/, 'function'))();

// ---- the live half, transcribed --------------------------------------------

const liveKey = (a) => a.alertType + '|' + (a.subject || '');
const liveIsOpen = (a) => !a.resolvedAt;
const liveNeeding = (alerts) => alerts.filter((a) => liveIsOpen(a) && !a.acknowledgedAt);

function liveSetAlerts(open, recent) {
  const alerts = (open || []).concat(recent || []);
  alerts.sort((a, b) => (b.firedAt || 0) - (a.firedAt || 0));
  if (alerts.length > MAX) alerts.length = MAX;
  return alerts;
}

function liveAddAlert(alerts, a) {
  if (!a) return alerts;
  const k = liveKey(a);
  const next = alerts.filter((x) => !(liveKey(x) === k && liveIsOpen(x)));
  next.unshift(a);
  if (next.length > MAX) next.pop();
  return next;
}

function liveResolve(alerts, ids, resolvedAt) {
  const set = {};
  (ids || []).forEach((id) => { set[id] = 1; });
  return alerts.map((a) => (set[a.id] ? { ...a, resolvedAt } : a));
}

function liveAck(alerts, ids, at, by) {
  const set = {};
  (ids || []).forEach((id) => { set[id] = 1; });
  return alerts.map((a) => (set[a.id]
    ? { ...a, acknowledgedAt: at, acknowledgedBy: by || null } : a));
}

function liveAge(ts, now) {
  const age = now - ts;
  if (age < 60000) return 'just now';
  if (age < 3600000) return Math.floor(age / 60000) + 'm ago';
  if (age < 86400000) return Math.floor(age / 3600000) + 'h ago';
  return Math.floor(age / 86400000) + 'd ago';
}

function livePanel(alerts, now) {
  const shown = alerts.filter((a) => !a.acknowledgedAt);
  if (!shown.length) return '<div class="notif-empty">No alerts</div>';
  const open = shown.filter(liveIsOpen);
  const done = shown.filter((a) => !liveIsOpen(a));

  function row(a) {
    const cls = 'notif-item' + (liveIsOpen(a) ? ' is-open' : ' is-resolved');
    const when = liveIsOpen(a) ? a.firedAt : (a.resolvedAt || a.firedAt);
    return '<div class="' + cls + '" data-alert-id="' + a.id + '">' +
      '<div class="notif-item-title">' + esc(a.label || a.alertType) +
        (a.subject ? ' — ' + esc(a.subject) : '') + '</div>' +
      '<div class="notif-item-body">' + esc(a.detail || '') + '</div>' +
      '<div class="notif-item-time">' +
        (a.routerName ? '<span class="notif-item-router">' + esc(a.routerName) + '</span> · ' : '') +
        liveAge(when, now) + '</div>' +
      (liveIsOpen(a)
        ? '<button class="notif-ack-btn" data-ack="' + a.id + '">Acknowledge</button>' : '') +
    '</div>';
  }

  return open.map(row).join('') +
    (open.length && done.length ? '<div class="notif-sep">Recently resolved</div>' : '') +
    done.map(row).join('');
}

// ---- the ported half --------------------------------------------------------

const ENTRY = path.join(ROOT, 'testdata', '.bell-entry.ts');
fs.writeFileSync(ENTRY,
  "export { setAlerts, addAlert, resolveAlerts, ackAlerts, alertAge, panelHTML,\n" +
  "  dotDisplay, needingAttention, alertKey, isOpen, MAX_ALERTS }\n" +
  "  from '../web/src/pages/notifications.js';\n");
const OUT = path.join(ROOT, 'testdata', '.bell.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const port = require(OUT);

assert.equal(port.MAX_ALERTS, MAX,
  'the port caps at ' + port.MAX_ALERTS + ', the live app at ' + MAX);

// ---- the alerts -------------------------------------------------------------

const NOW = Date.parse('2026-01-02T00:00:00Z');
const MIN = 60000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

const a = (id, over = {}) => ({
  id, alertType: 'link', subject: 'ether1', detail: 'down', label: null,
  routerName: null, firedAt: NOW - HOUR, resolvedAt: null,
  acknowledgedAt: null, acknowledgedBy: null, ...over,
});

const LISTS = {
  'empty': [],
  'one open': [a(1)],
  'one acknowledged and open': [a(1, { acknowledgedAt: NOW - MIN })],
  'one resolved': [a(1, { resolvedAt: NOW - MIN })],
  'open and resolved together': [
    a(1), a(2, { alertType: 'cpu', subject: null, resolvedAt: NOW - MIN })],
  'every age bucket': [
    a(1, { firedAt: NOW - 30000 }), a(2, { firedAt: NOW - 5 * MIN }),
    a(3, { firedAt: NOW - 3 * HOUR }), a(4, { firedAt: NOW - 2 * DAY }),
  ],
  'a resolved row timed from its resolution': [
    a(1, { firedAt: NOW - 3 * HOUR, resolvedAt: NOW - 30000 }),
  ],
  'labels, routers and nulls': [
    a(1, { label: 'Link down', routerName: 'hAP AX3' }),
    a(2, { label: null, subject: null, detail: null, routerName: null }),
  ],
  'markup in every text field': [
    a(1, { label: '<img src=x>', subject: '"><b>', detail: "it's & <bad>", routerName: '<r>' }),
  ],
  'all acknowledged': [a(1, { acknowledgedAt: NOW }), a(2, { acknowledgedAt: NOW })],
};

const bad = [];
let checked = 0;
const cmp = (name, x, y) => {
  checked++;
  if (JSON.stringify(x) !== JSON.stringify(y)) bad.push({ name, a: x, b: y });
};

for (const [name, list] of Object.entries(LISTS)) {
  cmp('panel ' + name, livePanel(list, NOW), port.panelHTML(list, NOW));
  cmp('dot ' + name, liveNeeding(list).length ? 'block' : 'none', port.dotDisplay(list));
  cmp('needing ' + name, liveNeeding(list).map((x) => x.id),
    port.needingAttention(list).map((x) => x.id));
}

// setAlerts: merge, sort, cap.
const OPEN_FEED = [a(10, { firedAt: NOW - 2 * HOUR }), a(11, { firedAt: NOW - 4 * HOUR })];
const RECENT_FEED = [a(12, { firedAt: NOW - 3 * HOUR, resolvedAt: NOW - MIN }),
  a(13, { firedAt: NOW - HOUR, resolvedAt: NOW - MIN })];
for (const [name, open, recent] of [
  ['both feeds', OPEN_FEED, RECENT_FEED],
  ['open only', OPEN_FEED, []],
  ['recent only', [], RECENT_FEED],
  ['neither', [], []],
  ['over the cap', Array.from({ length: MAX + 20 }, (_, i) => a(i, { firedAt: NOW - i * MIN })), []],
]) {
  cmp('setAlerts ' + name,
    liveSetAlerts(open, recent).map((x) => x.id),
    port.setAlerts(open, recent).map((x) => x.id));
}

// addAlert: replacement, and the cap.
for (const [name, list, incoming] of [
  ['into an empty list', [], a(1)],
  ['replacing an open one', [a(1), a(2, { alertType: 'cpu', subject: null })], a(3)],
  ['NOT replacing a resolved one', [a(1, { resolvedAt: NOW - MIN })], a(2)],
  ['a different subject', [a(1, { subject: 'ether1' })], a(2, { subject: 'ether2' })],
  ['a different type', [a(1, { alertType: 'link' })], a(2, { alertType: 'cpu' })],
  ['at the cap', Array.from({ length: MAX }, (_, i) => a(1000 + i, { subject: 'if' + i })), a(1)],
]) {
  cmp('addAlert ' + name,
    liveAddAlert(list, incoming).map((x) => x.id),
    port.addAlert(list, incoming).map((x) => x.id));
}

// resolve and ack.
const MIXED = [a(1), a(2, { alertType: 'cpu', subject: null }), a(3, { alertType: 'ping' })];
for (const ids of [[], [1], [1, 3], [99]]) {
  cmp('resolve ' + JSON.stringify(ids),
    liveResolve(MIXED, ids, NOW).map((x) => [x.id, x.resolvedAt]),
    port.resolveAlerts(MIXED, ids, NOW).map((x) => [x.id, x.resolvedAt]));
  cmp('ack ' + JSON.stringify(ids),
    liveAck(MIXED, ids, NOW, 'alice').map((x) => [x.id, x.acknowledgedAt, x.acknowledgedBy]),
    port.ackAlerts(MIXED, ids, NOW, 'alice').map((x) => [x.id, x.acknowledgedAt, x.acknowledgedBy]));
}

// The age boundaries, exactly.
for (const age of [0, 59999, 60000, 60001, 3599999, 3600000, 86399999, 86400000, 5 * DAY]) {
  cmp('age ' + age, liveAge(NOW - age, NOW), port.alertAge(NOW - age, NOW));
}

// ---- BELIEVABILITY ---------------------------------------------------------
{
  // The dot must go OUT when the only open alert is acknowledged, and stay out
  // for a resolved one.
  assert.equal(liveNeeding(LISTS['one open']).length, 1, 'an open alert does not light the dot');
  assert.equal(liveNeeding(LISTS['one acknowledged and open']).length, 0,
    'acknowledging an OPEN alert leaves the dot lit');
  assert.equal(liveNeeding(LISTS['one resolved']).length, 0, 'a resolved alert lights the dot');

  // An acknowledged alert is invisible in the panel even while open.
  assert.ok(livePanel(LISTS['one acknowledged and open'], NOW).includes('No alerts'),
    'an acknowledged OPEN alert is still shown — "Clear all" would appear to do nothing');
  // ...but it is still in the list, so a later resolve can find it.
  assert.equal(LISTS['one acknowledged and open'].length, 1,
    'the acknowledged alert was dropped from the list, not just the panel');

  // The separator appears only when both halves are present.
  assert.ok(livePanel(LISTS['open and resolved together'], NOW).includes('Recently resolved'),
    'no separator between open and resolved');
  assert.ok(!livePanel(LISTS['one open'], NOW).includes('notif-sep'),
    'a separator with nothing to separate');

  // Escaping.
  const hostile = livePanel(LISTS['markup in every text field'], NOW);
  assert.ok(!hostile.includes('<img src=x>'), 'the live bell does not escape a label');
  assert.ok(!hostile.includes('"><b>'), 'the live bell does not escape a subject');

  // A resolved row is timed from its RESOLUTION.
  assert.ok(livePanel(LISTS['a resolved row timed from its resolution'], NOW).includes('just now'),
    'a resolved row is timed from firedAt — it would say "3h ago" about something that '
    + 'ended thirty seconds ago');

  // Replacement, and that it is scoped to the key AND to open-ness.
  assert.deepEqual(liveAddAlert([a(1), a(2, { alertType: 'cpu', subject: null })], a(3))
    .map((x) => x.id), [3, 2], 'a new alert did not replace the open one for the same key');
  assert.deepEqual(liveAddAlert([a(1, { resolvedAt: NOW - MIN })], a(2)).map((x) => x.id),
    [2, 1], 'a new alert replaced a RESOLVED entry for the same key — that is history');

  // The cap really bites.
  assert.equal(liveSetAlerts(Array.from({ length: MAX + 20 }, (_, i) => a(i)), []).length, MAX,
    'the cap is not applied');
}

fs.rmSync(OUT, { force: true });
if (bad.length) {
  for (const x of bad) {
    console.error('[' + x.name + ']');
    console.error('  live ' + JSON.stringify(x.a));
    console.error('  port ' + JSON.stringify(x.b));
  }
  console.error('\nnotif-bell-check: ' + bad.length + ' of ' + checked + ' cases differ');
  process.exit(1);
}
console.log('notif-bell-check: ' + checked + ' cases identical');
