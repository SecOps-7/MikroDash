'use strict';
/**
 * The BROWSER session store's pure helpers, recorded by running the live ones.
 *
 * ── WHY THIS EXISTS NOW ─────────────────────────────────────────────────────
 *
 * Nothing in Go minted a session until 2026-08-27. `internal/server/auth.go`
 * delegated to Node on purpose — `sessionStore.js` keeps sessions in a
 * process-local Map, so there is no shared store Go could write into — and that
 * is correct DURING COEXISTENCE and fatal at cutover, when Node stops and nobody
 * can log in. Found by driving the live server for the first time; it is not a
 * defect in either app, it is a step nobody had written down.
 *
 * ── WHAT IS RECORDED, AND WHAT DELIBERATELY IS NOT ──────────────────────────
 *
 * The three pure functions: `parseCookieHeader`, `buildCookieHeader` and the
 * expiry rule. Those decide what a browser is sent and what it is believed
 * about, and every one of them has an edge that a reasonable port gets wrong.
 *
 * NOT recorded: `createSession`'s token. It is 32 random bytes and there is
 * nothing to compare — the properties that matter (length, hex, unguessable,
 * never reused) are asserted directly on the Go side instead, which is the right
 * tool for a value that is different every time by design.
 *
 * ── THE CLOCK AND THE ENVIRONMENT ARE BOTH PINNED ───────────────────────────
 *
 * `buildCookieHeader` reads `Date.now()` for its Max-Age and `process.env
 * .FORCE_HTTPS` for its Secure flag. Both are driven explicitly, so the corpus
 * records a DECISION rather than whatever the generating machine happened to
 * have set — and FORCE_HTTPS is recorded in both states, because an install
 * behind TLS and one without get different cookies and only one of them was ever
 * going to be tested by accident.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/websession-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const SRC = path.join(LIVE, 'src', 'auth', 'sessionStore.js');
const src = fs.readFileSync(SRC, 'utf8');

// MARKER ASSERTIONS. A slice that lost its body would compare two functions
// that both answer nothing, and the corpus would record that as live behaviour.
for (const marker of ['function parseCookieHeader', 'function buildCookieHeader',
  'function clearCookieHeader', 'mikrodash_sid', 'SameSite=Strict', 'FORCE_HTTPS']) {
  assert.ok(src.includes(marker),
    'sessionStore.js has no ' + marker + ' -- it has been rewritten and this corpus is '
    + 'describing a file that no longer exists');
}

const FIXED_NOW = 1700000000000;

function load(forceHttps) {
  const ctx = {
    require, module: { exports: {} }, Math, String, Array, Object, Number, JSON,
    Date: Object.assign(function D(...a) { return new Date(...a); }, { now: () => FIXED_NOW }),
    // Only the ONE variable this module reads. A whole `process.env` copy would
    // let the generating machine's environment leak into the corpus.
    process: { env: { FORCE_HTTPS: forceHttps } },
    setInterval: () => ({ unref() {} }),
    clearInterval: () => {},
  };
  ctx.exports = ctx.module.exports;
  vm.createContext(ctx);
  vm.runInContext(src, ctx);
  return ctx.module.exports;
}

const plain = load(undefined);
const https = load('true');

// ── COOKIE HEADERS THE BROWSER MIGHT SEND ───────────────────────────────────
const HEADERS = [
  ['a single cookie', 'mikrodash_sid=abc123'],
  ['several, the one we want last', 'theme=dark; lang=en; mikrodash_sid=abc123'],
  ['whitespace around the pairs', '  mikrodash_sid=abc123  ;  theme=dark  '],
  ['no cookies at all', ''],
  // A VALUE CONTAINING '=' — the live code splits on the FIRST '=' only, and a
  // port using a two-way split would truncate a base64 token at its padding.
  ['a value containing equals signs', 'mikrodash_sid=YWJjPT0='],
  ['a bare name with no equals', 'flagonly; mikrodash_sid=abc123'],
  ['an empty name', '=orphan; mikrodash_sid=abc123'],
  ['an empty value', 'mikrodash_sid='],
  ['a duplicate name, last wins', 'mikrodash_sid=first; mikrodash_sid=second'],
  ['only a semicolon', ';'],
  ['a trailing semicolon', 'mikrodash_sid=abc123;'],
  // Not a string at all. The live guard is `typeof !== 'string'`, and Go's
  // header API cannot produce these -- recorded so the Go port's own guard is
  // known to be defending something the original also defends.
  ['null', null],
  ['undefined', undefined],
];

// ── EXPIRY, DRIVEN THROUGH createSession + getSession ────────────────────────
//
// The rule is not directly exported, so it is exercised through the pair. A
// timeout of 0 means NEVER EXPIRES (Infinity), which is the trap: a port
// treating 0 as "already expired" logs everybody out instantly, and a port
// treating a negative the same way as 0 keeps a session an operator asked to
// end immediately.
const TIMEOUTS = [
  ['zero means never', 0],
  ['negative means never too', -1],
  ['a normal hour', 3600000],
  ['one millisecond', 1],
];

const cases = { parse: [], cookie: [], expiry: [] };

for (const [name, header] of HEADERS) {
  cases.parse.push({
    name, header: header === undefined ? null : header, headerIsNull: header === null,
    headerIsUndefined: header === undefined,
    out: plain.parseCookieHeader(header),
  });
}

// buildCookieHeader in both environments, across the expiry shapes.
for (const [name, ms] of TIMEOUTS) {
  const expiresAt = ms > 0 ? FIXED_NOW + ms : Infinity;
  cases.cookie.push({
    name, timeoutMs: ms, infinite: expiresAt === Infinity,
    plain: plain.buildCookieHeader('TOKEN', expiresAt),
    forceHttps: https.buildCookieHeader('TOKEN', expiresAt),
  });
}
// An expiry ALREADY IN THE PAST still yields Max-Age=1, not 0 or a negative —
// `Math.max(1, ...)`. A port emitting a negative Max-Age hands the browser a
// cookie some implementations treat as a session cookie instead of a dead one.
cases.cookie.push({
  name: 'an expiry already in the past', timeoutMs: -3600000, infinite: false,
  plain: plain.buildCookieHeader('TOKEN', FIXED_NOW - 3600000),
  forceHttps: https.buildCookieHeader('TOKEN', FIXED_NOW - 3600000),
});
cases.clear = { plain: plain.clearCookieHeader(), forceHttps: https.clearCookieHeader() };

for (const [name, ms] of TIMEOUTS) {
  const { token, expiresAt } = plain.createSession('u1', 'someone', 'admin', ms, ['r1']);
  cases.expiry.push({
    name, timeoutMs: ms,
    infinite: expiresAt === Infinity,
    expiresAt: expiresAt === Infinity ? null : expiresAt,
    tokenLength: token.length,
    tokenIsHex: /^[0-9a-f]+$/.test(token),
    // Immediately after creation the session is live in every case, INCLUDING
    // the one-millisecond one: the check is `now > expiresAt`, strictly, and no
    // time has passed on a pinned clock.
    liveImmediately: plain.getSession(token) !== null,
  });
}

// ── BELIEVABILITY ───────────────────────────────────────────────────────────
{
  const p = (n) => cases.parse.find((c) => c.name === n).out;
  assert.equal(p('a single cookie').mikrodash_sid, 'abc123');
  assert.equal(p('a value containing equals signs').mikrodash_sid, 'YWJjPT0=',
    'the value was truncated at an "=" -- the live split takes the FIRST one only, and a '
    + 'two-way split eats the padding off a base64 token');
  assert.equal(p('a duplicate name, last wins').mikrodash_sid, 'second');
  assert.deepEqual(p('no cookies at all'), {});
  assert.ok(!('' in p('an empty name')), 'an empty cookie name was kept');
  assert.equal(p('an empty value').mikrodash_sid, '');
  assert.ok(!('flagonly' in p('a bare name with no equals')),
    'a cookie with no "=" was kept, which would give it the value undefined');

  const c = (n) => cases.cookie.find((x) => x.name === n);
  assert.ok(c('zero means never').infinite, 'a timeout of 0 did not mean "never expires"');
  assert.ok(!c('zero means never').plain.includes('Max-Age'),
    'a never-expiring cookie carried a Max-Age');
  assert.ok(c('a normal hour').plain.includes('Max-Age=3600'),
    'an hour did not render as Max-Age=3600');
  assert.ok(c('an expiry already in the past').plain.includes('Max-Age=1'),
    'an expiry in the past did not clamp to Max-Age=1 -- `Math.max(1, ...)` is what stops a '
    + 'negative Max-Age reaching the browser');
  assert.ok(!c('a normal hour').plain.includes('Secure'),
    'the plain build emitted Secure without FORCE_HTTPS');
  assert.ok(c('a normal hour').forceHttps.includes('; Secure'),
    'FORCE_HTTPS did not add Secure');
  for (const x of cases.cookie) {
    for (const v of [x.plain, x.forceHttps]) {
      assert.ok(v.startsWith('mikrodash_sid=TOKEN; HttpOnly; SameSite=Strict; Path=/'),
        'the cookie lost HttpOnly, SameSite=Strict or Path=/: ' + v);
    }
  }
  assert.ok(cases.clear.plain.includes('Max-Age=0'), 'the clear cookie does not expire');

  const e = (n) => cases.expiry.find((x) => x.name === n);
  assert.ok(e('zero means never').infinite && e('negative means never too').infinite,
    'a non-positive timeout did not mean "never expires" -- a port reading 0 as "already '
    + 'expired" logs every user out the instant they sign in');
  assert.ok(!e('a normal hour').infinite);
  for (const x of cases.expiry) {
    assert.equal(x.tokenLength, 64, 'the token is not 32 bytes of hex');
    assert.ok(x.tokenIsHex, 'the token is not lower-case hex');
    assert.ok(x.liveImmediately, 'a session was expired the moment it was created');
  }
}

const OUT = path.join(ROOT, 'testdata', 'websession-cases.json');
const text = JSON.stringify({ fixedNow: FIXED_NOW, ...cases }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== text) {
    console.error('testdata/websession-cases.json is stale — run: node tools/websession-cases.js');
    process.exit(1);
  }
  console.log('websession-cases.json is current');
} else {
  fs.writeFileSync(OUT, text);
  console.log('wrote ' + OUT + ' (' + cases.parse.length + ' cookie headers, '
    + cases.cookie.length + ' cookie builds, ' + cases.expiry.length + ' expiry shapes)');
}
