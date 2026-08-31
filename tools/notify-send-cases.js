'use strict';
/**
 * WHAT A NOTIFICATION FAILURE SAYS, and what each channel refuses to try.
 *
 * Two behaviours, both of them about the operator rather than the protocol:
 *
 *   send()         tries EVERY configured channel and collects the failures.
 *                  A broken Telegram token must not silence email. The errors
 *                  are prefixed per channel and joined with '; ', so the answer
 *                  names which one failed rather than saying "notification
 *                  failed".
 *   testChannel()  refuses before sending, with a message naming the field that
 *                  is missing. "Telegram Bot Token is not configured" is
 *                  actionable; a 401 from Telegram is not.
 *
 * ---- A LIVE INCONSISTENCY, REPRODUCED ------------------------------------
 *
 * `_reason()` exists to pull a human explanation out of an error body, and its
 * own comment says "Telegram and ntfy both return one". Telegram's and
 * Pushbullet's failures use it; **ntfy's does not** — `sendNtfy` rejects with a
 * bare `HTTP <status>`. So an ntfy misconfiguration reports "HTTP 403" while the
 * body sitting in the same buffer says why.
 *
 * Reproduced rather than improved, and filed as ../MikroDash/ToDo.md. A port
 * that fixed it would give a different answer from the app it is replacing, and
 * the fix belongs upstream.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/notify-send-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const notifier = fs.readFileSync(path.join(SRC, 'src', 'notifier.js'), 'utf8');

// `_reason` is not exported; lifted whole so the corpus runs the real one.
const reasonSrc = notifier.slice(notifier.indexOf('function _reason('),
  notifier.indexOf('\n}\n', notifier.indexOf('function _reason(')) + 3);
const _reason = new Function('return ' + reasonSrc)();

/** The live error text for the channels that include a reason. */
const httpError = (status, raw) => 'HTTP ' + status + _reason(raw);

// ---- NTFY IS READ OUT OF THE SOURCE, NOT TRANSCRIBED ---------------------
//
// This used to be `const ntfyError = (status) => 'HTTP ' + status`, written from
// reading `sendNtfy` at the time: it rejected with a bare status where the other
// two appended `_reason`. The port reproduced the asymmetry deliberately and
// filed it as ../MikroDash/ToDo.md §4.
//
// The live side then FIXED it, and this line did not notice — it encoded my
// reading of the source rather than the source, so the corpus kept asserting an
// inconsistency that no longer existed. That is the failure mode every other
// generator here avoids by lifting; this one had one hand-written exception and
// the exception is what went stale.
//
// So the branch is now DETECTED. `sendNtfy`'s reject line is sliced out and
// tested for `_reason`, and which branch was taken is recorded in the corpus so
// the Go side cannot disagree with it silently either.
const ntfyBody = (() => {
  const i = notifier.indexOf('function sendNtfy(');
  assert.ok(i > 0, 'sendNtfy is gone from notifier.js');
  const end = notifier.indexOf('\nfunction ', i + 1);
  return notifier.slice(i, end < 0 ? notifier.length : end);
})();
const ntfyRejects = ntfyBody.match(/reject\(new Error\((.*)\)\);/);
assert.ok(ntfyRejects, 'sendNtfy no longer rejects with a constructed Error — this '
  + 'generator can no longer tell which text it produces');
const ntfyHasReason = /_reason\(/.test(ntfyRejects[1]);
const ntfyError = ntfyHasReason
  ? (status, raw) => 'HTTP ' + status + _reason(raw)
  : (status) => 'HTTP ' + status;

const BODIES = [
  '',
  '{"description":"chat not found"}',
  '{"error":"Unauthorized"}',
  '{"message":"rate limited"}',
  '{"ok":false}',
  'plain text failure',
  '   whitespace   around   ',
  // A long HTML error page: kept short so it cannot flood a log or a response.
  '<html><body>' + 'x'.repeat(400) + '</body></html>',
  // Newlines and tabs collapse to single spaces.
  '{"description":"line one\\nline two\\ttabbed"}',
  '{"description":""}',
  'null',
  '[1,2,3]',
];

const reasons = BODIES.map((raw) => ({ raw, out: _reason(raw) }));
const errors = [400, 401, 403, 429, 500].flatMap((status) => [
  { channel: 'http', status, raw: '{"description":"chat not found"}', out: httpError(status, '{"description":"chat not found"}') },
  { channel: 'http', status, raw: '', out: httpError(status, '') },
  { channel: 'ntfy', status, raw: '{"error":"forbidden"}',
    out: ntfyError(status, '{"error":"forbidden"}') },
]);

// ---- testChannel's preconditions -----------------------------------------
//
// Transcribed from the live source, and asserted to still be in it: these are
// the exact strings an operator reads.
const PRECONDITIONS = {
  telegram: [
    ['telegramBotToken', 'Telegram Bot Token is not configured'],
    ['telegramChatId', 'Telegram Chat ID is not configured'],
  ],
  pushbullet: [['pushbulletApiKey', 'Pushbullet API Key is not configured']],
  smtp: [
    ['smtpHost', 'SMTP Host is not configured'],
    ['smtpFrom', 'SMTP From address is not configured'],
    ['smtpTo', 'SMTP To address is not configured'],
  ],
  ntfy: [['ntfyUrl', 'ntfy topic URL is not configured']],
};
for (const list of Object.values(PRECONDITIONS)) {
  for (const [, msg] of list) {
    assert.ok(notifier.includes(msg),
      'the live notifier no longer says ' + JSON.stringify(msg));
  }
}
assert.ok(notifier.includes('Unknown notification channel'),
  'the unknown-channel message changed');

/** The live testChannel's refusal, in order, or null when it would send. */
function precondition(settings, channel) {
  const list = PRECONDITIONS[channel];
  if (!list) return 'Unknown notification channel';
  for (const [key, msg] of list) if (!settings[key]) return msg;
  return null;
}

const FULL = {
  telegramBotToken: 't', telegramChatId: 'c',
  pushbulletApiKey: 'k',
  smtpHost: 'h', smtpFrom: 'f', smtpTo: 'to',
  ntfyUrl: 'https://ntfy.sh/x',
};
const preconditions = [];
for (const channel of ['telegram', 'pushbullet', 'smtp', 'ntfy', 'nonsense', '']) {
  preconditions.push({ channel, settings: {}, out: precondition({}, channel) });
  preconditions.push({ channel, settings: FULL, out: precondition(FULL, channel) });
  // ...and each field missing on its own, so the ORDER of the checks is pinned.
  for (const [key] of PRECONDITIONS[channel] || []) {
    const s = { ...FULL };
    delete s[key];
    preconditions.push({ channel, settings: s, missing: key, out: precondition(s, channel) });
  }
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  const by = Object.fromEntries(reasons.map((r) => [r.raw, r.out]));
  assert.equal(by[''], '', 'an empty body produced a reason');
  assert.equal(by['{"description":"chat not found"}'], ' — chat not found',
    'the JSON description is not being used');
  assert.equal(by['{"error":"Unauthorized"}'], ' — Unauthorized', 'the `error` key is ignored');
  assert.equal(by['{"ok":false}'], '', 'a JSON body with no message produced one anyway');
  assert.equal(by['plain text failure'], ' — plain text failure', 'a non-JSON body is discarded');
  const longPage = reasons.find((r) => r.raw.length > 300).out;
  assert.ok(longPage.length <= 163,
    'a long error page is not being truncated to 160 characters — it would flood the log '
    + 'and the test-notification response: ' + longPage.length);
  // Found by CONTENT, not by an exact key: the corpus entry contains a literal
  // backslash-n and the lookup would need to match it byte for byte, which is a
  // way to write an assertion that silently tests undefined.
  const collapsed = reasons.find((r) => r.raw.includes('line one')).out;
  assert.ok(!/[\r\n\t]/.test(collapsed),
    'whitespace is not collapsed to single spaces: ' + JSON.stringify(collapsed));
  assert.ok(collapsed.includes('line one line two tabbed'),
    'the collapsed message lost its words: ' + JSON.stringify(collapsed));

  // ---- THE ASYMMETRY IS GONE, AND THIS ASSERTION WENT WITH IT -------------
  //
  // This used to read `assert.ok(!n.out.includes('forbidden'))` — the port's
  // "assert the gap STILL EXISTS" rule, so that closing it upstream would fail
  // the suite and force the note to be deleted rather than left lying. That is
  // exactly what happened: the live `sendNtfy` now appends `_reason(buf)` like
  // the other two transports, and the assertion fired.
  //
  // It is DELETED rather than inverted, because there is no longer an asymmetry
  // to describe. What replaces it is the ordinary property: every channel
  // reports the reason it was given.
  const t = errors.find((e) => e.channel === 'http' && e.raw.includes('chat not found'));
  const n = errors.find((e) => e.channel === 'ntfy');
  assert.ok(t.out.includes('chat not found'), 'the http error dropped its reason');
  assert.equal(ntfyHasReason, true,
    'ntfy no longer includes a reason — if the live side reverted the ToDo.md §4 fix, this '
    + 'corpus and internal/notify.Post both have to go back to modelling the asymmetry');
  assert.ok(n.out.includes('forbidden'),
    'the ntfy error dropped its reason even though sendNtfy appends one');

  assert.equal(precondition({}, 'telegram'), 'Telegram Bot Token is not configured',
    'the first missing telegram field is not reported first');
  assert.equal(precondition({ telegramBotToken: 't' }, 'telegram'),
    'Telegram Chat ID is not configured', 'the second check does not run');
  assert.equal(precondition(FULL, 'telegram'), null, 'a complete config was refused');
  assert.equal(precondition(FULL, 'nonsense'), 'Unknown notification channel',
    'an unknown channel was accepted');
}

const OUT = path.join(ROOT, 'testdata', 'notify-send-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/notify-send-cases.js from the live src/notifier.js. Do not edit.',
  ntfyHasReason, reasons, errors, preconditions,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('notify-send-cases: testdata/notify-send-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('notify-send-cases: up to date');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('notify-send-cases: wrote ' + reasons.length + ' reasons, ' + errors.length
    + ' errors, ' + preconditions.length + ' preconditions');
}
