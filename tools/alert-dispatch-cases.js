'use strict';
/**
 * The alert DISPATCH's two decisions, run from the live `src/alerter.js`.
 *
 * ── WHAT IS BEING PINNED, AND WHY IT IS NOT OBVIOUS ────────────────────────
 *
 * 1. THE MESSAGE. `allVars` is built as
 *
 *      { routerName, timestamp, ...vars, alertType: labelFor(alertType) }
 *
 *    and the ORDER is load-bearing: `alertType` is overridden AFTER the spread,
 *    so the notification uses the same human name the bell does. The live
 *    comment says what happens otherwise — "a push would say 'RouterOS Update'
 *    while the alert list said 'Update Available'".
 *
 *    The body template also depends on direction: `notifBodyUp` for a
 *    resolution, falling back to `notifBody`, falling back to a built-in. Three
 *    levels, and a port collapsing them sends "⚠️" for a recovery.
 *
 * 2. THE COOLDOWN. Keyed `recipientId|subjectKey`, so the install and a user
 *    cannot collide, and CONSUMED ONLY WHERE A SEND HAPPENS — the live comment:
 *    "a recipient who enables a channel later does not find a warm cooldown
 *    stamped while they had none." The guard order is channel, then toggles,
 *    then cooldown, and reordering it is invisible until someone turns a
 *    channel on.
 *
 * ── THIS FILE SENDS NOTHING ────────────────────────────────────────────────
 *
 * `notifier.send` is stubbed. What is recorded is what WOULD have been sent, to
 * whom, and whether the cooldown allowed it.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/alert-dispatch-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const src = fs.readFileSync(path.join(LIVE, 'src', 'alerter.js'), 'utf8');

function slice(decl, close, name) {
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name + ' — it has moved or been rewritten');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}

// The three pure helpers, plus the label map they lean on.
const parts = [
  slice('function _render(tpl, vars) {', '\n}', '_render'),
  slice('function _hasChannel(s) {', '\n}', '_hasChannel'),
  slice('const ALERT_LABELS = {', '\n};', 'ALERT_LABELS'),
  slice('const _LABEL_ACRONYMS = {', '};', '_LABEL_ACRONYMS'),
  slice('function labelFor(alertType) {', '\n}', 'labelFor'),
  slice('function _deliver(cooldownMap, recipient, subjectKey, title, body, maxEntries) {',
        '\n}', '_deliver'),
];

// The message assembly is INLINE in `fire`, with no function boundary. Anchored
// on the `allVars` line and lifted through the `body` assignment, then wrapped —
// the same shape `tools/poll-tables.js` uses for `populate`'s poll half.
const MSG_START = '    const allVars = { routerName: getNameFn(), timestamp: _ts(), ...vars,';
const MSG_END = '    const body = _render(bodyTpl, allVars);';
const a = src.indexOf(MSG_START);
if (a === -1) throw new Error('cannot find the message assembly — it has moved');
const b = src.indexOf(MSG_END, a);
if (b === -1) throw new Error('the message assembly does not reach its body line');
const msgBody = src.slice(a, b + MSG_END.length)
  .replace('getNameFn()', 'ROUTER_NAME')
  .replace('_ts()', 'TS');

const ctx = {
  Date, String, Object, Math, JSON, console: { warn() {} },
  _settings: null, ROUTER_NAME: '', TS: '',
  // notifier.send is STUBBED. Every case records the call, and nothing leaves.
  notifier: { send: (s, title, body) => { ctx.SENT.push({ title, body }); return { catch() {} }; } },
  SENT: [],
  COOLDOWN_MAX: 1000,
};
vm.createContext(ctx);
vm.runInContext(parts.join('\n') + '\n' +
  'this.message = function (settings, routerName, ts, alertType, vars, isUp) {\n' +
  '  _settings = settings; ROUTER_NAME = routerName; TS = ts;\n' +
  msgBody + '\n' +
  '  return { title: title, body: body, allVars: allVars };\n' +
  '};\n' +
  'this.deliver = _deliver;\n' +
  'this.setSettings = function (s) { _settings = s; };\n' +
  'this.hasChannel = _hasChannel;\n' +
  'this.labelFor = labelFor;', ctx);

// ── the corpus ──────────────────────────────────────────────────────────────
//
// SYNTHETIC throughout. No real token, address or router name appears here.

const BASE = {
  notifTitle: 'MikroDash Alert',
  notifBody: '⚠️ {{alertType}} on {{routerName}}: {{detail}}',
  notifBodyUp: '✅ {{alertType}} on {{routerName}}: {{detail}}',
  notifCooldownSec: 60,
  telegramEnabled: true, telegramBotToken: 'synthetic', telegramChatId: '1',
};

const MESSAGES = [
  { why: 'a down alert uses notifBody', settings: BASE, isUp: false,
    alertType: 'High CPU', vars: { detail: 'CPU at 94%' } },
  { why: 'an up alert uses notifBodyUp', settings: BASE, isUp: true,
    alertType: 'CPU Normal', vars: { detail: 'CPU back to 10%' } },
  { why: 'no notifBodyUp falls back to notifBody',
    settings: { ...BASE, notifBodyUp: '' }, isUp: true,
    alertType: 'CPU Normal', vars: { detail: 'CPU back to 10%' } },
  { why: 'neither template falls back to the built-in',
    settings: { ...BASE, notifBody: '', notifBodyUp: '' }, isUp: false,
    alertType: 'High CPU', vars: { detail: 'CPU at 94%' } },
  // THE SAME, BUT UP. The built-in fallback is DIRECTIONAL — a tick for a
  // recovery, a warning triangle for a fault — and with no case here a port
  // sending the warning glyph for both survives every other message case.
  { why: 'neither template, on a RESOLUTION, uses the tick not the warning',
    settings: { ...BASE, notifBody: '', notifBodyUp: '' }, isUp: true,
    alertType: 'CPU Normal', vars: { detail: 'CPU back to 10%' } },
  { why: 'no title falls back to the built-in',
    settings: { ...BASE, notifTitle: '' }, isUp: false,
    alertType: 'High CPU', vars: { detail: 'x' } },
  // THE OVERRIDE ORDER. `vars.alertType` must LOSE to labelFor(alertType).
  //
  // THE FIRST VERSION OF THIS CASE DID NOT DISCRIMINATE: it used
  // `vars.alertType = 'Update Available'` against `labelFor('routeros_update')`,
  // which IS 'Update Available'. Both orders produced the same string and the
  // case would have passed against a port that got the precedence backwards —
  // exactly the bug the live comment describes. The value below cannot be
  // confused with any label.
  { why: 'vars carrying its own alertType is overridden by the label',
    settings: BASE, isUp: false, alertType: 'routeros_update',
    vars: { alertType: 'WRONG-should-not-appear', detail: 'RouterOS 7.24 is available' } },
  { why: 'a stored-form type is labelled', settings: BASE, isUp: false,
    alertType: 'vpn_disconnected', vars: { detail: 'peer wg0 down' } },
  { why: 'an unknown type still gets a label', settings: BASE, isUp: false,
    alertType: 'something_new', vars: { detail: 'x' } },
  { why: 'an empty type', settings: BASE, isUp: false, alertType: '', vars: { detail: 'x' } },
  { why: 'a template using every placeholder',
    settings: { ...BASE, notifTitle: '{{routerName}} @ {{timestamp}}',
      notifBody: '{{alertType}}/{{subject}}/{{detail}}/{{missing}}' },
    isUp: false, alertType: 'High CPU',
    vars: { subject: 'ether1', detail: 'down' } },
  { why: 'a detail with a brace in it', settings: BASE, isUp: false,
    alertType: 'High CPU', vars: { detail: 'weird {{routerName}} value' } },
  { why: 'no detail at all', settings: BASE, isUp: false,
    alertType: 'High CPU', vars: {} },
];

const messages = MESSAGES.map((m) => {
  const got = ctx.message(m.settings, 'Test Router', '12:34:56', m.alertType, m.vars, m.isUp);
  return { why: m.why, settings: m.settings, alertType: m.alertType, vars: m.vars,
           isUp: m.isUp, title: got.title, body: got.body };
});

// ── the cooldown ────────────────────────────────────────────────────────────
//
// Driven as a SEQUENCE, because every rule here is about what a previous call
// left behind.

function cooldownRun(steps, settings) {
  ctx.setSettings(settings);
  const map = new Map();
  const out = [];
  for (const s of steps) {
    ctx.SENT = [];
    // The live `_deliver` reads Date.now(); the step's `at` is injected by
    // replacing the clock for the duration of the call.
    const real = ctx.Date;
    ctx.Date = Object.assign(function () { return new real(s.at); },
      { now: () => s.at });
    ctx.deliver(map, s.recipient, s.subjectKey, 'T', 'B', s.maxEntries);
    ctx.Date = real;
    out.push({ at: s.at, recipient: s.recipient ? s.recipient.id : null,
               subjectKey: s.subjectKey,
               sent: ctx.SENT.length > 0, mapSize: map.size });
  }
  return out;
}

const withChannel = { id: '_install', settings: BASE };
const noChannel = { id: 'user:u1', settings: { notifCooldownSec: 60 } };

const COOLDOWNS = [
  {
    why: 'the first send goes, the second inside the window does not',
    settings: BASE,
    steps: [
      { at: 1000000, recipient: withChannel, subjectKey: 'cpu:router:down' },
      { at: 1010000, recipient: withChannel, subjectKey: 'cpu:router:down' },
      { at: 1061000, recipient: withChannel, subjectKey: 'cpu:router:down' },
    ],
  },
  {
    why: 'two subjects are independent',
    settings: BASE,
    steps: [
      { at: 1000000, recipient: withChannel, subjectKey: 'a' },
      { at: 1000001, recipient: withChannel, subjectKey: 'b' },
    ],
  },
  {
    why: 'two recipients are independent, so one cannot spend the other cooldown',
    settings: BASE,
    steps: [
      { at: 1000000, recipient: withChannel, subjectKey: 'a' },
      { at: 1000001, recipient: { id: 'user:u2', settings: BASE }, subjectKey: 'a' },
    ],
  },
  {
    why: 'A RECIPIENT WITH NO CHANNEL never stamps the cooldown',
    settings: BASE,
    steps: [
      { at: 1000000, recipient: noChannel, subjectKey: 'a' },
      { at: 1000001, recipient: noChannel, subjectKey: 'a' },
      // ...and when they later gain one, the first send is immediate.
      { at: 1000002, recipient: { id: 'user:u1', settings: BASE }, subjectKey: 'a' },
    ],
  },
  {
    why: 'a nil recipient is ignored',
    settings: BASE,
    steps: [{ at: 1000000, recipient: null, subjectKey: 'a' }],
  },
  {
    why: 'a custom cooldown window is honoured',
    settings: { ...BASE, notifCooldownSec: 5 },
    steps: [
      { at: 1000000, recipient: withChannel, subjectKey: 'a' },
      { at: 1003000, recipient: withChannel, subjectKey: 'a' },
      { at: 1006000, recipient: withChannel, subjectKey: 'a' },
    ],
  },
  {
    why: 'a zero cooldown falls back to 60 seconds',
    settings: { ...BASE, notifCooldownSec: 0 },
    steps: [
      { at: 1000000, recipient: withChannel, subjectKey: 'a' },
      { at: 1030000, recipient: withChannel, subjectKey: 'a' },
    ],
  },
];

const cooldowns = COOLDOWNS.map((c) => ({ why: c.why, settings: c.settings,
  steps: c.steps.map((s) => ({ at: s.at, recipient: s.recipient, subjectKey: s.subjectKey })),
  result: cooldownRun(c.steps, c.settings) }));

// BELIEVABILITY, both ways: a corpus where everything sends would pass against a
// dispatcher with no cooldown, and one where nothing sends against one that
// never sends at all.
const sent = cooldowns.flatMap((c) => c.result).filter((r) => r.sent).length;
const held = cooldowns.flatMap((c) => c.result).filter((r) => !r.sent).length;
if (sent < 5 || held < 4) {
  throw new Error('the cooldown corpus sends ' + sent + ' and holds ' + held +
                  ' — it does not exercise both directions');
}

const out = {
  _generated: 'tools/alert-dispatch-cases.js — do not edit',
  note: 'Every value here is synthetic. Nothing was sent: notifier.send is stubbed.',
  messages, cooldowns,
};

const OUT = path.join(ROOT, 'testdata', 'alert-dispatch-cases.json');
const want = JSON.stringify(out, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== want) { console.log('STALE: ' + path.relative(ROOT, OUT)); process.exit(1); }
  console.log('alert dispatch cases current (' + messages.length + ' messages, ' +
              cooldowns.length + ' cooldown runs, ' + sent + ' sends and ' + held + ' holds)');
} else {
  fs.writeFileSync(OUT, want);
  console.log('wrote ' + path.relative(ROOT, OUT) + ' (' + messages.length + ' messages, ' +
              cooldowns.length + ' cooldown runs, ' + sent + ' sends and ' + held + ' holds)');
}
