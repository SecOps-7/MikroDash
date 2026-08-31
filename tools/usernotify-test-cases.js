'use strict';
/**
 * THE "TEST BEFORE SAVE" MERGE for a user's own notification channels.
 *
 * Pressing Test on the My Alerts tab must verify what is TYPED, not only what is
 * stored — otherwise nobody can check a token without committing it first. So
 * the route merges the form over the stored config. Three rules make that safe,
 * and each of them is the kind that looks like a detail:
 *
 *   MASKED MEANS "USE THE STORED ONE".  A saved credential is rendered back as
 *     `••••••••`. If the form's value were taken literally, pressing Test
 *     without retyping would send eight bullet characters to Telegram as a bot
 *     token — and the failure would read as a bad token rather than a bug.
 *   EMPTY MEANS "USE THE STORED ONE" TOO.  Only a non-empty, unmasked value
 *     overrides, so clearing a field in the form does not silently test against
 *     nothing.
 *   LENGTHS ARE CAPPED.  512 for a credential, 256 for the rest. This is the one
 *     place an ordinary account chooses what the SERVER connects to — the live
 *     comment on the install-wide switch says enabling per-user channels "widens
 *     what an ordinary account can make the server connect to" — so the input is
 *     bounded before it becomes a request.
 *
 * And the channel is FORCE-ENABLED for the test: testing before ticking the box
 * would otherwise report "not configured" rather than the truth.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/usernotify-test-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const userNotify = fs.readFileSync(path.join(SRC, 'src', 'userNotify.js'), 'utf8');
const index = fs.readFileSync(path.join(SRC, 'src', 'index.js'), 'utf8');
const settings = fs.readFileSync(path.join(SRC, 'src', 'settings.js'), 'utf8');

/** The field lists, read from the live source rather than retyped. */
const listOf = (name) => {
  const m = userNotify.match(new RegExp(name + "\\s*=\\s*\\[([^\\]]*)\\]"));
  assert.ok(m, 'could not read ' + name + ' from the live userNotify.js');
  return m[1].split(',').map((s) => s.trim().replace(/^'|'$/g, '')).filter(Boolean);
};
const CREDENTIAL_FIELDS = listOf('CREDENTIAL_FIELDS');
const STR_FIELDS = listOf('STR_FIELDS');

const MASK = (settings.match(/isMasked\(v\)\s*\{\s*return v === '([^']+)'/) || [])[1];
assert.ok(MASK, 'could not read the mask string from the live settings.js');
assert.ok(index.includes('.slice(0, 512)') && index.includes('.slice(0, 256)'),
  'the live caps are no longer 512 and 256');

const ENABLE_KEY = { telegram: 'telegramEnabled', pushbullet: 'pushbulletEnabled',
  ntfy: 'ntfyEnabled', email: 'emailEnabled' };
for (const [ch, key] of Object.entries(ENABLE_KEY)) {
  assert.ok(index.includes(`${ch}: '${key}'`), 'the live enable-key map no longer has ' + ch);
}

/** The live merge, transcribed from the route. */
function mergeForTest(body, stored, channel) {
  const typed = {};
  for (const f of CREDENTIAL_FIELDS) {
    if (body[f] && body[f] !== MASK) typed[f] = String(body[f]).slice(0, 512);
  }
  for (const f of STR_FIELDS) {
    if (body[f] && body[f] !== MASK) typed[f] = String(body[f]).slice(0, 256);
  }
  const enableKey = ENABLE_KEY[channel];
  if (!enableKey) return { error: 'Unknown channel' };
  const out = { ...stored, ...typed };
  out[enableKey] = true;
  return { settings: out, typed };
}

const STORED = {
  telegramEnabled: false, telegramBotToken: 'stored-token', telegramChatId: 'stored-chat',
  pushbulletEnabled: false, pushbulletApiKey: 'stored-key',
  ntfyEnabled: false, ntfyUrl: 'https://ntfy.sh/stored', ntfyToken: 'stored-ntfy',
  emailEnabled: false, emailTo: 'stored@example.com',
};

const CASES = {
  'nothing typed': { body: {}, channel: 'telegram' },
  'a new token typed': { body: { telegramBotToken: 'typed-token' }, channel: 'telegram' },
  // THE ONE THAT MATTERS: the form sends back the mask for a stored credential.
  'the mask is sent back': { body: { telegramBotToken: MASK }, channel: 'telegram' },
  'the mask alongside a real change': {
    body: { telegramBotToken: MASK, telegramChatId: 'typed-chat' }, channel: 'telegram' },
  'an empty field': { body: { telegramBotToken: '' }, channel: 'telegram' },
  'every field masked': {
    body: Object.fromEntries([...CREDENTIAL_FIELDS, ...STR_FIELDS].map((f) => [f, MASK])),
    channel: 'ntfy' },
  // The caps.
  'an over-long credential': {
    body: { telegramBotToken: 'x'.repeat(900) }, channel: 'telegram' },
  'an over-long string field': { body: { ntfyUrl: 'y'.repeat(900) }, channel: 'ntfy' },
  'exactly at the credential cap': {
    body: { telegramBotToken: 'z'.repeat(512) }, channel: 'telegram' },
  'exactly at the string cap': { body: { ntfyUrl: 'w'.repeat(256) }, channel: 'ntfy' },
  // Each channel forces its own flag and no other.
  'pushbullet': { body: { pushbulletApiKey: 'k' }, channel: 'pushbullet' },
  'ntfy': { body: { ntfyUrl: 'https://ntfy.sh/typed' }, channel: 'ntfy' },
  'email': { body: { emailTo: 'typed@example.com' }, channel: 'email' },
  'an unknown channel': { body: {}, channel: 'nonsense' },
  // A non-string value: `String(...)` before slicing, so a number does not throw.
  'a numeric value': { body: { telegramChatId: 12345 }, channel: 'telegram' },
  // FALSY BUT NOT NULL. The live guard is `if (body[f] && ...)`, so 0 and false
  // are skipped and the stored value stands. A port checking `!= null` instead
  // would override with the strings "0" and "false" — and a chat id of "0" is a
  // request Telegram answers with a refusal the operator cannot explain.
  'a zero chat id': { body: { telegramChatId: 0 }, channel: 'telegram' },
  'a false url': { body: { ntfyUrl: false }, channel: 'ntfy' },
  'a zero credential': { body: { telegramBotToken: 0 }, channel: 'telegram' },
};

const cases = Object.entries(CASES).map(([name, { body, channel }]) => ({
  name, body, channel, out: mergeForTest(body, STORED, channel),
}));

// ---- BELIEVABILITY -------------------------------------------------------
{
  const by = Object.fromEntries(cases.map((c) => [c.name, c.out]));

  assert.equal(by['a new token typed'].settings.telegramBotToken, 'typed-token',
    'a typed token did not override the stored one — testing before saving would be impossible');

  // THE MASK MUST NEVER TRAVEL.
  assert.equal(by['the mask is sent back'].settings.telegramBotToken, 'stored-token',
    'the mask was taken literally — eight bullets would be sent to Telegram as a bot token');
  for (const c of cases) {
    const blob = JSON.stringify(c.out.settings || {});
    assert.ok(!blob.includes(MASK), c.name + ': the mask survived into the settings');
  }
  assert.equal(by['the mask alongside a real change'].settings.telegramBotToken, 'stored-token',
    'a masked credential beside a real change was taken literally');
  assert.equal(by['the mask alongside a real change'].settings.telegramChatId, 'typed-chat',
    'the real change beside a masked credential was lost');

  assert.equal(by['an empty field'].settings.telegramBotToken, 'stored-token',
    'an empty field cleared the stored credential for the test');

  assert.equal(by['an over-long credential'].settings.telegramBotToken.length, 512,
    'a credential is not capped at 512');
  assert.equal(by['an over-long string field'].settings.ntfyUrl.length, 256,
    'a string field is not capped at 256');
  assert.equal(by['exactly at the credential cap'].settings.telegramBotToken.length, 512,
    'a value exactly at the cap was truncated');

  // Force-enable, and ONLY the channel under test.
  for (const [name, key] of [['a new token typed', 'telegramEnabled'],
                             ['pushbullet', 'pushbulletEnabled'],
                             ['ntfy', 'ntfyEnabled'], ['email', 'emailEnabled']]) {
    assert.equal(by[name].settings[key], true, name + ': the channel was not force-enabled');
    for (const other of Object.values(ENABLE_KEY)) {
      if (other === key) continue;
      assert.equal(by[name].settings[other], false,
        name + ': testing one channel enabled ' + other + ' as well');
    }
  }

  assert.equal(by['an unknown channel'].error, 'Unknown channel', 'an unknown channel was accepted');
  assert.equal(by['a numeric value'].settings.telegramChatId, '12345',
    'a non-string value was not stringified');
  assert.equal(by['a zero chat id'].settings.telegramChatId, 'stored-chat',
    'a chat id of 0 overrode the stored one — the guard is truthiness, not a null check');
  assert.equal(by['a false url'].settings.ntfyUrl, 'https://ntfy.sh/stored',
    'a false url overrode the stored one');
  assert.equal(by['a zero credential'].settings.telegramBotToken, 'stored-token',
    'a credential of 0 overrode the stored one');
}

const OUT = path.join(ROOT, 'testdata', 'usernotify-test-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/usernotify-test-cases.js from the live src/index.js. Do not edit.',
  mask: MASK, credentialFields: CREDENTIAL_FIELDS, strFields: STR_FIELDS,
  enableKey: ENABLE_KEY, stored: STORED, cases,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('usernotify-test-cases: testdata/usernotify-test-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('usernotify-test-cases: up to date');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('usernotify-test-cases: wrote ' + cases.length + ' merge cases');
}
