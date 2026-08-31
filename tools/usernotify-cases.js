'use strict';
/**
 * The per-user notification config's two PURE decisions, as a corpus generated
 * by RUNNING `src/userNotify.js`.
 *
 * Neither is exported — `_pick` and `_withInstallMail` are module-private — so
 * both are LIFTED by source, the way the gate tooling lifts a renderer. The rest
 * of that module is database reads and `Settings.decrypt`, which are I/O.
 *
 * ---- WHY THESE TWO --------------------------------------------------------
 *
 * `_pick` IS AN ALLOWLIST, and its comment says what it is for: "Only keys on
 * the allowlist survive a read, so a blob written by a newer version (or
 * hand-edited) cannot inject fields into what reaches notifier." The blob is a
 * database row and the destination is a function that inspects FIELD NAMES to
 * decide where to send — so an injected `smtpHost` would be a way to point one
 * user's alerts at an arbitrary server.
 *
 * `_withInstallMail` FOLDS THE INSTALL'S MAIL SERVER into a user's opt-in, and
 * carries the same rule the notifier does: "If the install has no usable mail
 * server the opt-in resolves to nothing rather than a half-configured channel
 * that would consume a cooldown and send nothing."
 *
 * The trap in it: `smtpTo` is the USER's address, not the install's. Everything
 * else comes from the install, and a port that copied `smtpTo` across with the
 * rest would send every user's alerts to the administrator.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/usernotify-cases.js          # write
 *   MIKRODASH_SRC=../MikroDash node tools/usernotify-cases.js --check  # fail if stale
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const src = fs.readFileSync(path.join(LIVE, 'src', 'userNotify.js'), 'utf8');

/** Lift one function whole, by its opening line and the dedent that ends it. */
function slice(startsWith) {
  const i = src.indexOf(startsWith);
  assert.ok(i > 0, 'cannot find ' + startsWith.trim());
  const j = src.indexOf('\n}', i);
  assert.ok(j > i && j - i < 1600, startsWith.trim() + ' is not where its anchors say');
  return src.slice(i, j + 2);
}

const tables = slice('const CHANNEL_TOGGLES');
const defaults = src.slice(src.indexOf('const DEFAULTS = (() => {'),
  src.indexOf('})();', src.indexOf('const DEFAULTS = (() => {')) + 5);
const pickSrc = slice('function _pick(stored, decryptCreds) {');
const mailSrc = slice('function _withInstallMail(own) {');
// Each function is checked for something IT actually contains. An earlier
// version asserted both mention DEFAULTS; only `_pick` does — `_withInstallMail`
// spreads the user's own config — so the check failed on a correct lift.
assert.ok(pickSrc.includes('DEFAULTS') && pickSrc.includes('CREDENTIAL_FIELDS'),
  'the lifted _pick lost the allowlist or the credential list');
assert.ok(mailSrc.includes('Settings.load()'),
  'the lifted _withInstallMail lost its read of the install settings');
// Matched on the ASSIGNMENT rather than its exact spacing — the original aligns
// this block and a column change would fail a lift that is perfectly correct.
assert.ok(/out\.smtpTo\s*=\s*own\.emailTo/.test(mailSrc),
  'the lifted _withInstallMail lost the line that makes the RECIPIENT the user');

// `Settings.load()` is the install's settings, injected per case; `decrypt` is
// the identity here, because what it does to a credential is `internal/store`'s
// business and is already pinned there.
let INSTALL = {};
const ctx = {
  Object, Settings: { load: () => INSTALL, decrypt: (v) => 'dec(' + String(v) + ')' },
  notifier: {},
};
vm.createContext(ctx);
// THE THREE TABLES ARE LIFTED, NOT TYPED HERE. Writing them out would mean the
// corpus could keep passing after a field was added upstream, which is the exact
// drift this generator exists to catch. Lifting them also means the module never
// has to be `require`d — it pulls in `db.js`, which needs `better-sqlite3`, a
// native module that is not present on the host.
const tableLines = ['CHANNEL_TOGGLES', 'CREDENTIAL_FIELDS', 'STR_FIELDS'].map((name) => {
  const i = src.indexOf('const ' + name);
  assert.ok(i > 0, 'cannot find ' + name);
  return src.slice(i, src.indexOf('\n', i));
});
for (const line of tableLines) {
  assert.ok(line.includes('['), 'a lifted table is not an array literal: ' + line);
}

vm.runInContext([
  ...tableLines,
  defaults, pickSrc, mailSrc,
  'globalThis.__pick = _pick; globalThis.__mail = _withInstallMail; globalThis.__def = DEFAULTS;',
].join('\n'), ctx);
const pick = ctx.__pick, withMail = ctx.__mail, DEFAULTS = ctx.__def;

// DEFAULTS is BUILT from the lifted tables by the lifted expression, so it
// cannot describe a shape the module does not have — and it must be non-trivial,
// or the allowlist below would let everything through.
assert.ok(Object.keys(DEFAULTS).length >= 8,
  'DEFAULTS has only ' + Object.keys(DEFAULTS).length + ' keys — the tables did not lift');

const PICKS = [
  ['nothing stored', {}, false],
  ['every default', { ...DEFAULTS }, false],
  ['a known toggle', { telegramEnabled: true }, false],
  ['a known string', { telegramChatId: 'abc' }, false],
  // THE ALLOWLIST. An injected transport field must not survive.
  ['an injected smtpHost', { smtpHost: 'evil.example', telegramEnabled: true }, false],
  ['an injected smtpTo', { smtpTo: 'attacker@example' }, false],
  ['an unknown key', { nonsense: 1 }, false],
  ['__proto__ as a key', JSON.parse('{"__proto__":{"x":1}}'), false],
  // Credentials are decrypted only when asked for.
  ['a credential, not decrypted', { telegramBotToken: 'CIPHER' }, false],
  ['a credential, decrypted', { telegramBotToken: 'CIPHER' }, true],
  ['a non-credential is never decrypted', { telegramChatId: 'PLAIN' }, true],
  ['mixed, decrypted', { telegramBotToken: 'C1', ntfyToken: 'C2', ntfyUrl: 'https://n/x' }, true],
];

const MAILS = [
  ['not opted in', { emailEnabled: false, emailTo: 'u@x' }, { smtpHost: 'h', smtpFrom: 'f' }],
  ['opted in with no address', { emailEnabled: true, emailTo: '' }, { smtpHost: 'h', smtpFrom: 'f' }],
  ['opted in, install has no host', { emailEnabled: true, emailTo: 'u@x' }, { smtpFrom: 'f' }],
  ['opted in, install has no from', { emailEnabled: true, emailTo: 'u@x' }, { smtpHost: 'h' }],
  ['opted in, install ready', { emailEnabled: true, emailTo: 'u@x' },
    { smtpHost: 'h', smtpPort: 587, smtpSecure: true, smtpUser: 'su', smtpPass: 'sp', smtpFrom: 'f',
      smtpTo: 'ADMIN@example' }],
  ['opted in with other channels too',
    { emailEnabled: true, emailTo: 'u@x', telegramEnabled: true, telegramBotToken: 't',
      telegramChatId: 'c' },
    { smtpHost: 'h', smtpFrom: 'f' }],
];

const cases = {
  defaults: DEFAULTS,
  picks: PICKS.map(([name, stored, dec]) => ({
    name, stored, decrypt: dec, want: pick(stored, dec),
  })),
  mails: MAILS.map(([name, own, install]) => {
    INSTALL = install;
    return { name, own, install, want: withMail(own) };
  }),
};

// ---- BELIEVABILITY --------------------------------------------------------
{
  const p = (n) => cases.picks.find((c) => c.name === n).want;
  assert.equal(p('an injected smtpHost').smtpHost, undefined,
    'an injected smtpHost survived the allowlist — that is a way to point a user\'s alerts at ' +
    'an arbitrary server');
  assert.equal(p('an injected smtpTo').smtpTo, undefined, 'an injected smtpTo survived');
  assert.equal(p('an unknown key').nonsense, undefined, 'an unknown key survived');
  assert.equal(p('a known toggle').telegramEnabled, true, 'a known toggle did not survive');
  assert.equal(p('a credential, decrypted').telegramBotToken, 'dec(CIPHER)',
    'the credential was not decrypted when asked');
  assert.equal(p('a credential, not decrypted').telegramBotToken, 'CIPHER',
    'the credential was decrypted when it should not have been');
  assert.equal(p('a non-credential is never decrypted').telegramChatId, 'PLAIN',
    'a non-credential was decrypted');

  const m = (n) => cases.mails.find((c) => c.name === n).want;
  assert.ok(!m('not opted in').smtpEnabled, 'a user who did not opt in got mail enabled');
  assert.ok(!m('opted in, install has no host').smtpEnabled,
    'a half-configured install produced an enabled channel — the cooldown-consuming state');
  assert.ok(!m('opted in, install has no from').smtpEnabled, 'as above, without a From');
  const ready = m('opted in, install ready');
  assert.equal(ready.smtpEnabled, true, 'a ready install did not enable mail');
  assert.equal(ready.smtpTo, 'u@x',
    'smtpTo is not the USER\'s address — every user\'s alerts would go to the administrator');
  assert.equal(ready.smtpHost, 'h', 'the install host was not folded in');
}

const FILE = path.join(ROOT, 'testdata', 'usernotify-cases.json');
const text = JSON.stringify(cases, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(FILE) ? fs.readFileSync(FILE, 'utf8') : '';
  if (have !== text) {
    console.error('usernotify-cases: STALE — regenerate with `node tools/usernotify-cases.js`');
    process.exit(1);
  }
  console.log('usernotify-cases: ' + cases.picks.length + ' picks, ' + cases.mails.length +
    ' mail folds current');
} else {
  fs.writeFileSync(FILE, text);
  console.log('usernotify-cases: wrote ' + cases.picks.length + ' picks and ' +
    cases.mails.length + ' mail folds');
}
