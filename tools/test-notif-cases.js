'use strict';
/**
 * `POST /api/settings/test-notification`'s credential MERGE, run from the live
 * source.
 *
 * ── WHY THIS NEEDS A CORPUS RATHER THAN A READING ───────────────────────────
 *
 * The merge is one object literal with fourteen spread guards, and it uses TWO
 * DIFFERENT GUARDS that behave differently on exactly the values an operator can
 * produce:
 *
 *   `botToken && {...}`              a FALSY value does not override. An empty
 *                                    field falls back to what is stored, which
 *                                    is what makes "test without saving" work.
 *   `smtpPort !== undefined && {...}` an explicitly-sent value DOES override,
 *                                    even when falsy — and then
 *                                    `parseInt(x,10) || 587` turns 0 into 587.
 *
 * So `smtpPort: 0` and `smtpSecure: false` behave differently from `botToken:
 * ""`, and a port that used one guard for all fourteen would be wrong on half of
 * them without failing anything. Each field also has its OWN length cap — 512
 * for the secrets, 256 for the addresses — and a port that capped them all the
 * same would truncate a long bot token or accept an over-long host.
 *
 * The corpus runs the real expression, so the caps and the guards come from the
 * source rather than from this file's reading of it.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/test-notif-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const src = fs.readFileSync(path.join(LIVE, 'src', 'index.js'), 'utf8');

// The merge, sliced by its two anchors. Wrapped in a function taking the body
// and the stored settings, which is exactly what the route has at that point.
const START = '    const base = Settings.load();';
const END = '    await notifier.testChannel(settings, channel);';
const a = src.indexOf(START);
if (a === -1) throw new Error('cannot find the test-notification merge — it has moved');
const b = src.indexOf(END, a);
if (b === -1) throw new Error('the merge is not followed by the testChannel call');
const mergeBody = src.slice(a, b)
  .replace('const base = Settings.load();', 'const base = STORED;');

const ctx = { STORED: null, parseInt, String };
vm.createContext(ctx);
vm.runInContext(
  'this.merge = function (body, stored) {\n' +
  '  STORED = stored;\n' +
  '  const { channel, apiKey, botToken, chatId,\n' +
  '          smtpHost, smtpPort, smtpSecure, smtpUser, smtpPass, smtpFrom, smtpTo,\n' +
  '          ntfyUrl, ntfyToken } = body || {};\n' +
  mergeBody +
  '  return settings;\n' +
  '};', ctx);

// Stored settings a real install would have. SYNTHETIC — no credential here is
// real, and none may be: this file is committed.
const STORED = {
  telegramBotToken: 'stored-tg-token', telegramChatId: 'stored-chat',
  pushbulletApiKey: 'stored-pb-key',
  smtpHost: 'stored.mail.invalid', smtpPort: 25, smtpSecure: false,
  smtpUser: 'stored-user', smtpPass: 'stored-pass',
  smtpFrom: 'from@stored.invalid', smtpTo: 'to@stored.invalid',
  ntfyUrl: 'https://ntfy.invalid/stored', ntfyToken: 'stored-ntfy',
  unrelated: 'must survive the merge untouched',
};

const LONG = 'x'.repeat(600);

const CASES = [
  { why: 'an empty body changes nothing', body: {} },
  { why: 'a telegram token typed but not saved', body: { botToken: 'typed-token' } },
  { why: 'an EMPTY telegram token falls back to the stored one', body: { botToken: '' } },
  { why: 'a null token falls back too', body: { botToken: null } },
  { why: 'both telegram fields', body: { botToken: 'typed-token', chatId: 'typed-chat' } },
  { why: 'a pushbullet key', body: { apiKey: 'typed-pb' } },
  { why: 'an over-long token is CAPPED', body: { botToken: LONG } },
  { why: 'an over-long chat id is capped SHORTER', body: { chatId: LONG } },
  { why: 'an over-long smtp host is capped', body: { smtpHost: LONG } },
  { why: 'an over-long smtp pass is capped', body: { smtpPass: LONG } },
  { why: 'an over-long ntfy url is capped', body: { ntfyUrl: LONG } },
  { why: 'a full smtp form', body: {
      smtpHost: 'mail.typed.invalid', smtpPort: 465, smtpSecure: true,
      smtpUser: 'typed-user', smtpPass: 'typed-pass',
      smtpFrom: 'from@typed.invalid', smtpTo: 'to@typed.invalid' } },
  // The undefined-guarded fields, which override even when falsy.
  { why: 'smtpPort 0 becomes 587, not 0', body: { smtpPort: 0 } },
  { why: 'smtpPort as a numeric string', body: { smtpPort: '2525' } },
  { why: 'smtpPort that is not a number becomes 587', body: { smtpPort: 'abc' } },
  { why: 'smtpPort null becomes 587', body: { smtpPort: null } },
  { why: 'smtpSecure FALSE overrides a stored true', body: { smtpSecure: false },
    stored: { ...STORED, smtpSecure: true } },
  { why: 'smtpSecure as the string "true"', body: { smtpSecure: 'true' } },
  { why: 'smtpSecure as the string "yes" is NOT true', body: { smtpSecure: 'yes' } },
  { why: 'smtpSecure as 1 is NOT true', body: { smtpSecure: 1 } },
  { why: 'ntfy url and token', body: { ntfyUrl: 'https://ntfy.typed.invalid/t', ntfyToken: 'typed-ntfy' } },
  { why: 'a number where a string is expected', body: { chatId: 12345 } },
  { why: 'a key the route does not destructure is ignored',
    body: { channel: 'telegram', somethingElse: 'ignored' } },
];

const out = {
  _generated: 'tools/test-notif-cases.js — do not edit',
  note: 'Every credential in this file is synthetic. See the header of CLAUDE.md.',
  stored: STORED,
  cases: CASES.map((c) => {
    const stored = c.stored || STORED;
    const merged = ctx.merge(c.body, stored);
    // Only what CHANGED, so a case's intent is readable and a new stored key
    // does not rewrite every expectation.
    const changed = {};
    for (const k of Object.keys(merged)) {
      if (merged[k] !== stored[k]) changed[k] = merged[k];
    }
    return { why: c.why, body: c.body, stored: c.stored ? stored : null, changed };
  }),
};

// BELIEVABILITY: a corpus where nothing ever changes would pass against a merge
// that ignored the body entirely.
const changing = out.cases.filter((c) => Object.keys(c.changed).length).length;
if (changing < 15) {
  throw new Error('only ' + changing + ' of ' + out.cases.length +
                  ' cases change anything — the merge is not being exercised');
}

const OUT = path.join(ROOT, 'testdata', 'test-notif-cases.json');
const want = JSON.stringify(out, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== want) { console.log('STALE: ' + path.relative(ROOT, OUT)); process.exit(1); }
  console.log('test-notification merge cases current (' + out.cases.length + ', ' +
              changing + ' changing)');
} else {
  fs.writeFileSync(OUT, want);
  console.log('wrote ' + path.relative(ROOT, OUT) + ' (' + out.cases.length + ' cases, ' +
              changing + ' changing)');
}
