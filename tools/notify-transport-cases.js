'use strict';
/**
 * WHAT EACH NOTIFICATION CHANNEL PUTS ON THE WIRE.
 *
 * `internal/notify` already decides WHERE an alert goes — the allowlist, the
 * install-mail fold, which channels are configured. This pins what the three
 * HTTP channels actually SEND, and what each refuses to send without.
 *
 * ---- THE DETAILS THAT ARE NOT COSMETIC ------------------------------------
 *
 *   the Telegram token   goes in the PATH — `/bot<token>/sendMessage` — and is
 *                        `encodeURIComponent`d. A token containing a slash would
 *                        otherwise change which endpoint is called, and a token
 *                        is operator-supplied text.
 *   the ntfy URL         is operator-supplied too, and is parsed rather than
 *                        concatenated: scheme decides the port (443/80), and the
 *                        path is taken from the URL. `topicUrl` with a query or
 *                        a fragment must not become part of the topic.
 *   the ntfy title       is an HTTP HEADER, not a body field. A title with a
 *                        newline in it would be a header injection, and titles
 *                        come from alert text.
 *   the ntfy body        is sent as RAW BYTES with an explicit Content-Length in
 *                        BYTES, not characters — a body with any non-ASCII in it
 *                        is longer than its string length.
 *
 * ---- AND WHAT A FAILURE SAYS ----------------------------------------------
 *
 * `send()` tries every configured channel and collects failures rather than
 * stopping at the first: a broken Telegram token must not silence email. The
 * errors are prefixed per channel and joined, so the operator learns which one
 * failed rather than that "notification failed".
 *
 *   MIKRODASH_SRC=../MikroDash node tools/notify-transport-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const notifier = fs.readFileSync(path.join(SRC, 'src', 'notifier.js'), 'utf8');

// The request SHAPES, transcribed from the live builders. Each is a few lines
// inside an async function that performs I/O, so there is no seam to call — what
// is reproduced is the description of the request, and the corpus's value is
// that the expectations were read off the live source rather than invented.
function telegramRequest(token, chatId, title, body) {
  return {
    scheme: 'https', host: 'api.telegram.org',
    path: `/bot${encodeURIComponent(token)}/sendMessage`,
    headers: { 'Content-Type': 'application/json' },
    json: { chat_id: chatId, text: title + '\n' + body },
  };
}

function pushbulletRequest(apiKey, title, body) {
  return {
    scheme: 'https', host: 'api.pushbullet.com', path: '/v2/pushes',
    headers: { 'Content-Type': 'application/json', 'Access-Token': apiKey },
    json: { type: 'note', title, body },
  };
}

function ntfyRequest(topicUrl, token, title, body) {
  const parsed = new URL(topicUrl);
  const isHttps = parsed.protocol === 'https:';
  const raw = Buffer.from(body, 'utf8');
  const headers = {
    Title: title,
    'Content-Type': 'text/plain; charset=utf-8',
    'Content-Length': String(raw.length),
  };
  if (token) headers.Authorization = 'Bearer ' + token;
  return {
    scheme: isHttps ? 'https' : 'http',
    host: parsed.hostname,
    port: Number(parsed.port || (isHttps ? 443 : 80)),
    path: parsed.pathname,
    headers,
    bodyBytes: raw.length,
  };
}

// Assert the transcription still matches the live source, so this file cannot
// quietly describe a request the live app stopped making.
for (const marker of [
  '/bot${encodeURIComponent(token)}/sendMessage',
  "'api.pushbullet.com'",
  "'/v2/pushes'",
  "type: 'note', title, body",
  "chat_id: chatId, text: title + '\\n' + body",
  "'Title':          title,",
  "'Content-Type':   'text/plain; charset=utf-8',",
  "headers['Authorization'] = 'Bearer ' + token",
  "parsed.port || (isHttps ? 443 : 80)",
]) {
  assert.ok(notifier.includes(marker),
    'the live notifier no longer contains ' + JSON.stringify(marker) + ' — this corpus describes a '
    + 'request it does not make any more');
}

const TITLE = 'MikroDash Test';
const BODY = 'Test notification from MikroDash — your alert channel is working correctly.';

const telegram = [
  { token: 'abc123', chatId: '-1001', title: TITLE, body: BODY },
  // A token with characters that would change the PATH if not encoded.
  { token: 'a/b?c#d', chatId: '1', title: 'x', body: 'y' },
  { token: '111:AAA-BBB_ccc', chatId: '@channel', title: 'x', body: 'y' },
  { token: '', chatId: '1', title: 'x', body: 'y' },
  // A multi-line body: Telegram takes it in a JSON field, so a newline is fine.
  { token: 't', chatId: '1', title: 'Line', body: 'one\ntwo\nthree' },
].map((c) => ({ ...c, out: telegramRequest(c.token, c.chatId, c.title, c.body) }));

const pushbullet = [
  { apiKey: 'o.abc', title: TITLE, body: BODY },
  { apiKey: '', title: 'x', body: 'y' },
  { apiKey: 'k', title: 'Ünïcödé', body: 'bödy' },
].map((c) => ({ ...c, out: pushbulletRequest(c.apiKey, c.title, c.body) }));

const ntfy = [
  { url: 'https://ntfy.sh/mikrodash', token: '', title: TITLE, body: BODY },
  { url: 'https://ntfy.sh/mikrodash', token: 'tk_abc', title: 'x', body: 'y' },
  // Plain HTTP, and an explicit port.
  { url: 'http://ntfy.local/topic', token: '', title: 'x', body: 'y' },
  { url: 'http://ntfy.local:8080/topic', token: '', title: 'x', body: 'y' },
  { url: 'https://ntfy.example.com:8443/a/b', token: '', title: 'x', body: 'y' },
  // A QUERY and a FRAGMENT must not leak into the path.
  { url: 'https://ntfy.sh/topic?auth=secret#frag', token: '', title: 'x', body: 'y' },
  // A non-ASCII body: Content-Length is BYTES, not characters.
  { url: 'https://ntfy.sh/t', token: '', title: 'x', body: 'héllo wörld — ünïcödé' },
  { url: 'https://ntfy.sh/t', token: '', title: 'x', body: '' },
].map((c) => ({ ...c, out: ntfyRequest(c.url, c.token, c.title, c.body) }));

// ---- BELIEVABILITY -------------------------------------------------------
{
  const t = telegram.find((c) => c.token === 'a/b?c#d').out;
  assert.ok(!t.path.includes('a/b'),
    'a slash in the Telegram token survived into the path — it would change the endpoint called');
  assert.ok(t.path.includes('a%2Fb%3Fc%23d'), 'the token is not percent-encoded: ' + t.path);

  const q = ntfy.find((c) => c.url.includes('?auth=secret')).out;
  assert.equal(q.path, '/topic', 'a query string leaked into the ntfy path: ' + q.path);
  assert.ok(!JSON.stringify(q).includes('secret'), 'the query survived somewhere in the request');

  const u = ntfy.find((c) => c.body.includes('ünïcödé')).out;
  assert.notEqual(u.headers['Content-Length'], String('héllo wörld — ünïcödé'.length),
    'Content-Length is being taken from the STRING length, not the byte length');
  // DERIVED, not a number typed here — the first version said 27 and it is 29,
  // which is exactly the mistake this assertion exists to catch someone else
  // making.
  const uBody = ntfy.find((c) => c.body.includes('ünïcödé')).body;
  assert.equal(u.headers['Content-Length'], String(Buffer.byteLength(uBody, 'utf8')),
    'the byte length is wrong: ' + u.headers['Content-Length']);
  assert.ok(Buffer.byteLength(uBody, 'utf8') > uBody.length,
    'this case is all ASCII, so it cannot show bytes and characters differing');

  assert.equal(ntfy.find((c) => c.url === 'http://ntfy.local/topic').out.port, 80,
    'plain http did not default to port 80');
  assert.equal(ntfy.find((c) => c.url.includes(':8080')).out.port, 8080,
    'an explicit port was ignored');
  assert.ok(!ntfy[0].out.headers.Authorization, 'an empty token still sent an Authorization header');
  assert.equal(ntfy[1].out.headers.Authorization, 'Bearer tk_abc', 'the token is not a bearer');

  assert.equal(telegram[0].out.json.text, TITLE + '\n' + BODY,
    'Telegram no longer joins the title and body with a newline');
  assert.equal(pushbullet[0].out.json.type, 'note', 'the Pushbullet push type changed');
}

const OUT = path.join(ROOT, 'testdata', 'notify-transport-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/notify-transport-cases.js from the live src/notifier.js. Do not edit.',
  telegram, pushbullet, ntfy,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('notify-transport-cases: testdata/notify-transport-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('notify-transport-cases: up to date');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('notify-transport-cases: wrote ' + telegram.length + ' telegram, '
    + pushbullet.length + ' pushbullet, ' + ntfy.length + ' ntfy cases');
}
