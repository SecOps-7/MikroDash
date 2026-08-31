'use strict';
/**
 * SAVING a user's own notification channels, and reading them back.
 *
 * `save()` merges over the STORED blob, not over a fresh object, and that is the
 * whole design: a credential the caller did not send keeps its existing
 * ciphertext and is never decrypted and re-encrypted just to survive an
 * unrelated edit. `getPublic()` is the other half — credentials come back as a
 * mask when set and as '' when not, so the browser's "leave blank to keep
 * current" handling needs no special case.
 *
 * ---- THE RULES ARE NOT THE SAME AS THE TEST MERGE -------------------------
 *
 * `tools/usernotify-test-cases.js` pins the merge used by the Test button, and
 * the two are deliberately different. Getting them the same way round is the
 * mistake this corpus exists to prevent:
 *
 *                    TEST merge                     SAVE merge
 *   guard            `if (body[f] && ...)`          `if (k in u)`
 *   so an empty      keeps the stored value         CLEARS the stored value
 *   trimming         none                           `.trim()` on the string fields
 *
 * Both are right. A test with a blank field should verify what is stored; a SAVE
 * with a blank field is how a channel is switched off, and if it kept the stored
 * value there would be no way to remove an address.
 *
 * ---- AND ONE VALIDATION ---------------------------------------------------
 *
 * `emailTo` must contain an `@`. It is checked on the MERGED value, so an edit
 * to an unrelated field on a config that already holds a bad address fails too —
 * which is the live behaviour, and arguably the useful one: a typo fails at the
 * moment it is made rather than silently never arriving.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/usernotify-save-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const userNotify = fs.readFileSync(path.join(SRC, 'src', 'userNotify.js'), 'utf8');

const listOf = (name) => {
  const m = userNotify.match(new RegExp(name + "\\s*=\\s*\\[([^\\]]*)\\]"));
  assert.ok(m, 'could not read ' + name);
  return m[1].split(',').map((s) => s.trim().replace(/^'|'$/g, '')).filter(Boolean);
};
const CHANNEL_TOGGLES = listOf('CHANNEL_TOGGLES');
const CREDENTIAL_FIELDS = listOf('CREDENTIAL_FIELDS');
const STR_FIELDS = listOf('STR_FIELDS');
const numOf = (name) => {
  const m = userNotify.match(new RegExp('const ' + name + '\\s*=\\s*(\\d+)'));
  assert.ok(m, 'could not read ' + name);
  return Number(m[1]);
};
const MAX_STR = numOf('MAX_STR');
const MAX_CRED = numOf('MAX_CRED');
const MASK = '••••••••';
assert.ok(userNotify.includes('That does not look like an email address'),
  'the live address validation message changed');

/** `Settings.encrypt`, stubbed: the corpus pins WHEN a value is encrypted. */
const enc = (v) => 'enc(' + v + ')';

/** The live save(), transcribed. */
function save(stored, updates) {
  const next = { ...stored };
  const u = updates || {};
  for (const k of CHANNEL_TOGGLES) {
    if (k in u) next[k] = (u[k] === true || u[k] === 'true');
  }
  for (const k of STR_FIELDS) {
    if (k in u) next[k] = String(u[k] == null ? '' : u[k]).trim().slice(0, MAX_STR);
  }
  if (next.emailTo && next.emailTo.indexOf('@') === -1) {
    return { error: 'That does not look like an email address' };
  }
  for (const k of CREDENTIAL_FIELDS) {
    if (!(k in u)) continue;
    if (u[k] === MASK) continue;
    const v = String(u[k] == null ? '' : u[k]).slice(0, MAX_CRED);
    next[k] = v ? enc(v) : '';
  }
  return { next };
}

/** The live getPublic(): the allowlist, plus masked credentials. */
function getPublic(stored) {
  const out = {};
  for (const k of CHANNEL_TOGGLES) out[k] = stored[k] === true;
  for (const k of STR_FIELDS) out[k] = stored[k] == null ? '' : String(stored[k]);
  for (const k of CREDENTIAL_FIELDS) out[k] = stored[k] ? MASK : '';
  return out;
}

const STORED = {
  telegramEnabled: true, telegramBotToken: 'enc(old-token)', telegramChatId: 'old-chat',
  pushbulletEnabled: false, pushbulletApiKey: '',
  ntfyEnabled: true, ntfyUrl: 'https://ntfy.sh/old', ntfyToken: 'enc(old-ntfy)',
  emailEnabled: false, emailTo: 'old@example.com',
};

const CASES = {
  'nothing sent': { updates: {} },
  // A toggle, in each of the forms the live check accepts.
  'a toggle set true': { updates: { pushbulletEnabled: true } },
  'a toggle set to the STRING true': { updates: { pushbulletEnabled: 'true' } },
  'a toggle set false': { updates: { telegramEnabled: false } },
  'a toggle set to something else entirely': { updates: { telegramEnabled: 'yes' } },
  'a toggle set to 1': { updates: { telegramEnabled: 1 } },

  // THE ASYMMETRY WITH THE TEST MERGE: present-but-empty CLEARS.
  'a string field cleared': { updates: { ntfyUrl: '' } },
  'a string field with whitespace': { updates: { ntfyUrl: '  https://ntfy.sh/new  ' } },
  'a string field set to null': { updates: { ntfyUrl: null } },
  'a string field over the cap': { updates: { ntfyUrl: 'u'.repeat(MAX_STR + 50) } },

  // Credentials.
  'a credential left alone': { updates: { telegramChatId: 'c' } },
  'a credential replaced': { updates: { telegramBotToken: 'new-token' } },
  'a credential masked': { updates: { telegramBotToken: MASK } },
  'a credential cleared': { updates: { telegramBotToken: '' } },
  'a credential set to null': { updates: { telegramBotToken: null } },
  'a credential over the cap': { updates: { telegramBotToken: 'k'.repeat(MAX_CRED + 50) } },
  // Credentials are NOT trimmed, unlike the string fields.
  'a credential with whitespace': { updates: { telegramBotToken: '  spaced  ' } },

  // The address validation, on the MERGED value.
  'a valid address': { updates: { emailTo: 'new@example.com' } },
  'an address with no @': { updates: { emailTo: 'not-an-address' } },
  'the address cleared': { updates: { emailTo: '' } },
  'an unrelated edit over a bad stored address': {
    stored: { ...STORED, emailTo: 'already-bad' }, updates: { ntfyUrl: 'https://x/y' } },

  // A key that is not on the allowlist must not survive.
  'an unknown key': { updates: { smtpHost: 'evil.example.com', emailTo: 'a@b.c' } },
};

const cases = Object.entries(CASES).map(([name, c]) => {
  const stored = c.stored || STORED;
  const r = save(stored, c.updates);
  return {
    name, stored, updates: c.updates,
    error: r.error || null,
    next: r.next || null,
    public: r.next ? getPublic(r.next) : null,
  };
});

// ---- BELIEVABILITY -------------------------------------------------------
{
  const by = Object.fromEntries(cases.map((c) => [c.name, c]));

  assert.deepEqual(by['nothing sent'].next, STORED, 'an empty update changed something');

  assert.equal(by['a toggle set to the STRING true'].next.pushbulletEnabled, true,
    'the string "true" did not enable the channel');
  assert.equal(by['a toggle set to something else entirely'].next.telegramEnabled, false,
    'the string "yes" enabled a channel — the live check accepts only a real true '
    + 'or the exact string true');
  assert.equal(by['a toggle set to 1'].next.telegramEnabled, false, '1 enabled a channel');

  // The asymmetry with the test merge.
  assert.equal(by['a string field cleared'].next.ntfyUrl, '',
    'an empty string did not CLEAR the stored url — save and the Test merge differ here on purpose');
  assert.equal(by['a string field with whitespace'].next.ntfyUrl, 'https://ntfy.sh/new',
    'a string field was not trimmed');
  assert.equal(by['a string field set to null'].next.ntfyUrl, '', 'null did not clear');
  assert.equal(by['a string field over the cap'].next.ntfyUrl.length, MAX_STR,
    'a string field was not capped');

  // Credentials.
  assert.equal(by['a credential left alone'].next.telegramBotToken, 'enc(old-token)',
    'an untouched credential was re-encrypted');
  assert.equal(by['a credential masked'].next.telegramBotToken, 'enc(old-token)',
    'a masked credential was taken literally and encrypted');
  assert.equal(by['a credential replaced'].next.telegramBotToken, 'enc(new-token)',
    'a new credential was not encrypted');
  assert.equal(by['a credential cleared'].next.telegramBotToken, '',
    'an empty credential did not clear');
  assert.equal(by['a credential with whitespace'].next.telegramBotToken, 'enc(  spaced  )',
    'a credential was trimmed — only the string fields are');
  assert.equal(by['a credential over the cap'].next.telegramBotToken.length,
    'enc()'.length + MAX_CRED, 'a credential was not capped before encryption');

  // The validation, and that it runs on the merged value.
  assert.equal(by['an address with no @'].error, 'That does not look like an email address',
    'an address with no @ was accepted');
  assert.equal(by['a valid address'].error, null, 'a valid address was rejected');
  assert.equal(by['the address cleared'].error, null, 'clearing the address was rejected');
  assert.equal(by['an unrelated edit over a bad stored address'].error,
    'That does not look like an email address',
    'a bad STORED address did not fail an unrelated edit — the check is on the merged value');

  // The allowlist.
  assert.ok(!('smtpHost' in by['an unknown key'].public),
    'an unknown key survived into the browser-facing shape');

  // getPublic must MASK, never disclose.
  const pub = by['a credential replaced'].public;
  assert.equal(pub.telegramBotToken, MASK, 'a set credential was not masked');
  assert.equal(pub.pushbulletApiKey, '', 'an unset credential was masked as though it were set');
  for (const c of cases) {
    if (!c.public) continue;
    const blob = JSON.stringify(c.public);
    assert.ok(!blob.includes('enc('), c.name + ': ciphertext reached the browser-facing shape');
    assert.ok(!blob.includes('old-token') && !blob.includes('new-token'),
      c.name + ': a credential reached the browser-facing shape');
  }
}

const OUT = path.join(ROOT, 'testdata', 'usernotify-save-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/usernotify-save-cases.js from the live src/userNotify.js. Do not edit.',
  mask: MASK, maxStr: MAX_STR, maxCred: MAX_CRED,
  channelToggles: CHANNEL_TOGGLES, credentialFields: CREDENTIAL_FIELDS, strFields: STR_FIELDS,
  cases,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('usernotify-save-cases: testdata/usernotify-save-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('usernotify-save-cases: up to date');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('usernotify-save-cases: wrote ' + cases.length + ' save cases');
}
