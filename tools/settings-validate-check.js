'use strict';
/**
 * `POST /api/settings`'s VALIDATOR, lifted and run.
 *
 * ── THIS CLOSES A GAP THE PORT RECORDED AGAINST ITSELF ─────────────────────
 *
 * `internal/store/settings_write.go` opens by saying so:
 *
 *   "Every other differential gate here RUNS the live implementation. This
 *    handler is inline in `src/index.js`, which calls `server.listen()` at
 *    require time and cannot be loaded by a test [...] So the RULES below are
 *    read-ported and covered by hand-written tests."
 *
 * The premise was true and is no longer. `tools/alert-row-check.js` lifts a
 * PRIVATE function out of that same file with `lib/lift.js` and evaluates it —
 * the module is READ, never required, so `server.listen()` never runs. The same
 * technique works on a block that is not a function at all: the validator is
 * sliced between two anchors and evaluated with `body` bound.
 *
 * So this handler stops being the one read-ported unit in the project. A
 * read-port is a rewrite that agrees with its author's reading; every rule below
 * is now checked against what the live code DOES.
 *
 * ── THE TWO ANCHORS ARE ASSERTED, NOT ASSUMED ──────────────────────────────
 *
 * A slice that stopped early would produce a validator missing its last rules
 * and this gate would then certify the port against a fragment — passing while
 * checking less. So the slice is required to contain every family of rule it is
 * supposed to carry, and to stop before the write.
 *
 * ── WHAT IT DISCRIMINATES ──────────────────────────────────────────────────
 *
 * AN INVALID VALUE IS IGNORED, NOT CLAMPED. Out of range, unparseable or off the
 * whitelist means the key is ABSENT from the updates, so `save` leaves the
 * stored value alone. Clamping would let a hand-crafted request move a setting
 * to the edge of its range while looking refused — so every numeric field is
 * driven at its bounds and one step outside them.
 *
 * `sessionTimeoutMs` is the one that is not a range: 0 means NEVER, and the
 * values between 1 and 3600000 are refused. A port clamping to a minimum would
 * turn "never expire" into an hour.
 *
 * Runs on the host: `src/index.js` is read, never loaded.
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');
const L = require('./lib/lift.js');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('settings-validate-check');
const OUT = process.env.SETTINGS_VALIDATE_OUT
  || path.join(ROOT, 'testdata', 'settings-validate-cases.json');

const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const indexSrc = LIFT.liveSource(ROOT, path.join('src', 'index.js'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const settingsSrc = LIFT.liveSource(ROOT, path.join('src', 'settings.js'));

// `pages.js` is pure and requirable; SETTING_KEYS is derived from the page table
// there, so a page added means a new `page*` boolean and this follows it.
// FROZEN — what this gate takes from `pages.js` is one DATA TABLE, not the
// module. Requiring the module is what made the gate die without a reference
// (`Cannot find module '/nonexistent/src/pages.js'`), and the table is a lifted
// value the comparison consumes, so it freezes rather than being guarded.
const SETTING_KEYS = G.value('Pages.SETTING_KEYS',
  () => require(path.join(SRC, 'src', 'pages.js')).SETTING_KEYS);
if (!Array.isArray(SETTING_KEYS) || !SETTING_KEYS.length) {
  throw new Error('the recorded SETTING_KEYS is empty — the golden is broken');
}
// The lifted validator is HANDED `Pages`, so it needs an object rather than the
// bare table. Rebuilt from the recording: if the live block ever reaches for
// another field the with-reference run fails immediately, which is the check
// that this reconstruction is complete.
const Pages = { SETTING_KEYS };

// ---- THE SLICE -----------------------------------------------------------
const OPEN = '    const updates = {};';
const CLOSE = '    // Captured before the write';
{
  const n = indexSrc.split(OPEN).length - 1;
  if (LIFT.hasReference(ROOT)) assert.equal(n, 1, 'the opening anchor is ambiguous (' + n + ' matches)');
}
const from = indexSrc.indexOf(OPEN);
const to = indexSrc.indexOf(CLOSE, from);
if (LIFT.hasReference(ROOT)) assert.ok(to > from,
  'the closing anchor is missing or before the opening one');
// FROZEN — the slice this gate checks its rules against.
const block = G.value('block', () => indexSrc.slice(from, to));
if (!block || block.length < 100) throw new Error('the recorded block is empty');

// Every family of rule the slice is supposed to carry. A slice that stopped
// early would certify the port against a fragment.
for (const marker of [
  'intFields', 'strFields', 'boolFields', 'credFields',
  "'authMode' in body", "'sessionTimeoutMs' in body", "'notifBody'", "'notifBodyUp'",
  "'customPollProfile' in body", "'displayTimezone' in body",
  'Pages.SETTING_KEYS', 'Settings.isMasked',
]) {
  assert.ok(block.includes(marker),
    'the lifted validator has no ' + marker + ' — the slice stopped early, and this gate '
    + 'would then check the port against less than the handler does');
}
assert.ok(!block.includes('Settings.save'),
  'the slice runs past the validator into the WRITE');

// FROZEN — built into `Settings.isMasked` by `new Function`, so the definition
// line is what must survive.
const isMaskedSrc = G.value('isMaskedSrc', () => L.line(settingsSrc, 'function isMasked(v)'));
if (!/function isMasked\(/.test(isMaskedSrc)) {
  throw new Error('the recorded isMasked is not a function definition');
}

// eslint-disable-next-line no-new-func
const validate = new Function('body', 'Pages', 'Settings',
  block + '\n  return updates;');
const Settings = { isMasked: new Function(isMaskedSrc + '; return isMasked;')() };
const run = (body) => validate(body, Pages, Settings);

// ---- THE BODIES ----------------------------------------------------------
//
// Chosen for the EDGES of each rule family rather than for coverage of fields:
// the families are what a port gets wrong, and the tables are generated
// separately by `tools/settings-write-tables.js`.
const CASES = {
  empty: {},

  // An integer field at both bounds, and one step outside each. Outside means
  // ABSENT, not clamped.
  intInRange: { topN: 25 },
  intAtLowBound: { topN: 1 },
  intAtHighBound: { topN: 50 },
  intBelowBound: { topN: 0 },
  intAboveBound: { topN: 51 },
  // `parseInt` takes a leading number and ignores the rest, so "25abc" is 25 —
  // a quirk, reproduced rather than tightened.
  intWithTrailingGarbage: { topN: '25abc' },
  intUnparseable: { topN: 'abc' },
  intNull: { topN: null },
  intFloat: { topN: 25.9 },
  intBoolean: { topN: true },

  // `updateCheckHours` is HOURS, not milliseconds, and its floor of 1 protects
  // MikroTik's update servers from a hand-crafted request.
  updateHoursLow: { updateCheckHours: 0 },
  updateHoursOk: { updateCheckHours: 1 },
  updateHoursHigh: { updateCheckHours: 169 },

  // authMode is a WHITELIST, not a string field.
  authModeValid: { authMode: 'modern' },
  authModeNone: { authMode: 'none' },
  authModeInvalid: { authMode: 'basic' },
  authModeEmpty: { authMode: '' },

  // sessionTimeoutMs: 0 is NEVER, and 1..3599999 are refused rather than
  // clamped. A port with a minimum turns "never expire" into an hour.
  sessionZero: { sessionTimeoutMs: 0 },
  sessionJustUnder: { sessionTimeoutMs: 3599999 },
  sessionAtMin: { sessionTimeoutMs: 3600000 },
  sessionAtMax: { sessionTimeoutMs: 86400000 },
  sessionOverMax: { sessionTimeoutMs: 86400001 },
  sessionOne: { sessionTimeoutMs: 1 },
  sessionUnparseable: { sessionTimeoutMs: 'never' },

  // Strings are TRIMMED and cut to 256. The cut is on the TRIMMED value.
  strPlain: { pingTarget: '198.51.100.1' },
  strPadded: { pingTarget: '   198.51.100.1   ' },
  strLong: { pingTarget: 'x'.repeat(300) },
  strPaddedLong: { pingTarget: '  ' + 'x'.repeat(300) + '  ' },
  strEmpty: { pingTarget: '' },
  strNumber: { pingTarget: 12345 },
  strNull: { pingTarget: null },

  // Booleans accept `true` and the STRING 'true', and nothing else — 1, 'yes'
  // and 'TRUE' are all false rather than absent.
  boolTrue: { pingEnabled: true },
  boolStringTrue: { pingEnabled: 'true' },
  boolStringTrueUpper: { pingEnabled: 'TRUE' },
  boolOne: { pingEnabled: 1 },
  boolFalse: { pingEnabled: false },
  boolStringFalse: { pingEnabled: 'false' },
  boolYes: { pingEnabled: 'yes' },
  // A page key, which comes from the generated table rather than a literal list.
  boolPageKey: { pageWifi: true },

  // Credentials: the MASK is refused, so submitting an unchanged form does not
  // overwrite a real token with eight bullets. Everything else is cut to 512
  // and NOT trimmed.
  credPlain: { telegramBotToken: 'abc123' },
  credMasked: { telegramBotToken: '••••••••' },
  credPadded: { telegramBotToken: '  abc123  ' },
  credLong: { telegramBotToken: 'y'.repeat(600) },
  credEmpty: { telegramBotToken: '' },

  // notifBody is trimmed and cut to 512 — a different limit from strFields.
  notifBodyLong: { notifBody: 'z'.repeat(600) },
  notifBodyPadded: { notifBody: '  hello  ' },
  notifBodyUpLong: { notifBodyUp: 'z'.repeat(600) },

  // customPollProfile must PARSE as an object, or be empty. `typeof null` is
  // 'object' in JavaScript, and so is an array — both are accepted, which is a
  // quirk worth pinning rather than fixing.
  profileEmpty: { customPollProfile: '' },
  profileObject: { customPollProfile: '{"pollSystem":5000}' },
  profileArray: { customPollProfile: '[1,2]' },
  profileNull: { customPollProfile: 'null' },
  profileNumber: { customPollProfile: '42' },
  profileString: { customPollProfile: '"hi"' },
  profileBroken: { customPollProfile: '{oops' },
  profileLong: { customPollProfile: '{"a":"' + 'b'.repeat(600) + '"}' },

  // displayTimezone is validated by CONSTRUCTING a formatter. Empty clears it.
  tzValid: { displayTimezone: 'Europe/Stockholm' },
  tzEmpty: { displayTimezone: '' },
  tzPadded: { displayTimezone: '  Europe/Stockholm  ' },
  tzInvalid: { displayTimezone: 'Nowhere/Atall' },
  tzUTC: { displayTimezone: 'UTC' },

  // An unknown key is dropped entirely.
  unknownKey: { notASetting: 1, topN: 10 },
  // Several families at once, which is what a real form submits.
  mixed: {
    topN: 30, pingTarget: ' host ', pingEnabled: 'true', authMode: 'none',
    telegramBotToken: 'tok', displayTimezone: 'UTC', notASetting: 'x',
  },
};

const cases = {};
for (const [name, body] of Object.entries(CASES)) {
  cases[name] = { body, updates: run(body) };
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  const u = (k) => cases[k].updates;
  const has = (k, f) => Object.prototype.hasOwnProperty.call(u(k), f);

  // The validator does something at all.
  assert.deepEqual(u('empty'), {}, 'an empty body produced updates');
  assert.equal(u('intInRange').topN, 25, 'a valid integer was not accepted, so every '
    + 'refusal below is indistinguishable from the validator refusing everything');

  // IGNORED, NOT CLAMPED — the property the whole file turns on.
  for (const k of ['intBelowBound', 'intAboveBound', 'intUnparseable', 'intNull']) {
    assert.ok(!has(k, 'topN'), k + ': the key is present, so the value was CLAMPED rather '
      + 'than refused — a hand-crafted request could then move a setting to its limit');
  }
  assert.equal(u('intAtLowBound').topN, 1);
  assert.equal(u('intAtHighBound').topN, 50);

  // sessionTimeoutMs's hole.
  assert.equal(u('sessionZero').sessionTimeoutMs, 0, '0 (never) was refused');
  assert.ok(!has('sessionJustUnder', 'sessionTimeoutMs'),
    'a value one millisecond under the minimum was accepted');
  assert.ok(!has('sessionOne', 'sessionTimeoutMs'),
    '1ms was accepted — a port clamping to the minimum turns "never" into an hour');
  assert.equal(u('sessionAtMin').sessionTimeoutMs, 3600000);

  // The whitelist.
  assert.equal(u('authModeValid').authMode, 'modern');
  assert.ok(!has('authModeInvalid', 'authMode'), 'an unlisted authMode was accepted');

  // Trim then cut, and the credential rule differs from the string one.
  assert.equal(u('strPadded').pingTarget, '198.51.100.1', 'the string was not trimmed');
  assert.equal(u('strLong').pingTarget.length, 256);
  assert.equal(u('credPadded').telegramBotToken, '  abc123  ',
    'the credential was TRIMMED — it must not be, or a token with meaningful spaces breaks');
  assert.equal(u('credLong').telegramBotToken.length, 512);
  assert.ok(!has('credMasked', 'telegramBotToken'),
    'the MASK was accepted — every save would replace a real token with eight bullets');

  // Booleans are strict.
  assert.equal(u('boolTrue').pingEnabled, true);
  assert.equal(u('boolStringTrue').pingEnabled, true);
  assert.equal(u('boolOne').pingEnabled, false, '1 became true — the rule is `===true || ===\'true\'`');
  assert.equal(u('boolStringTrueUpper').pingEnabled, false, "'TRUE' became true");
  assert.equal(u('boolPageKey').pageWifi, true,
    'a page key was refused — SETTING_KEYS did not reach the validator');

  // customPollProfile's `typeof x === 'object'` quirk.
  assert.ok(has('profileNull', 'customPollProfile'),
    "'null' was refused — `typeof null === 'object'` in JavaScript and the live rule accepts it");
  assert.ok(has('profileArray', 'customPollProfile'), 'an array was refused');
  assert.ok(!has('profileNumber', 'customPollProfile'), 'a number was accepted');
  assert.ok(!has('profileBroken', 'customPollProfile'), 'unparseable JSON was accepted');
  assert.equal(u('profileEmpty').customPollProfile, '');

  // The timezone is validated by construction.
  assert.equal(u('tzValid').displayTimezone, 'Europe/Stockholm');
  assert.equal(u('tzEmpty').displayTimezone, '');
  assert.ok(!has('tzInvalid', 'displayTimezone'), 'a nonexistent zone was accepted');

  assert.ok(!has('unknownKey', 'notASetting'), 'an unknown key survived');
  assert.equal(u('unknownKey').topN, 10);
  assert.equal(Object.keys(u('mixed')).length, 6,
    'the mixed body produced ' + Object.keys(u('mixed')).length + ' updates, want 6');
}

const json = JSON.stringify({ settingKeys: SETTING_KEYS, cases }, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('settings-validate-cases.json is STALE — re-run without --check');
    process.exit(1);
  }
  console.log('settings-validate-cases.json is current (' + Object.keys(cases).length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, json);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' cases)');
}
