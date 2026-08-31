'use strict';
/**
 * `Routers.sameEndpoint(a, b)` — the rule that decides whether a STORED router
 * password may be reused for a connection test.
 *
 * ── THIS IS THE WHOLE SECURITY PROPERTY OF /api/routers/test ────────────────
 *
 * The live comment states the attack it prevents: "A bare 'look it up by id'
 * turns this route into a credential oracle: submit a stored id with an
 * attacker-chosen host and the server posts the saved password to it. So the
 * stored secret is only reused when every field deciding WHERE it goes and HOW
 * it travels is unchanged."
 *
 * Five fields decide that:
 *
 *   host, port, username — where it is sent
 *   tls, tlsInsecure     — whether an observer, or a forged certificate, can
 *                          read it in transit
 *
 * `requireGlobalAdmin` gates the route and is explicitly NOT sufficient: "the
 * point is to stop a stored secret reaching a destination nobody stored it
 * against, INCLUDING at the hands of an admin."
 *
 * ── FOUR COERCIONS THAT LOOK LIKE TIDINESS AND ARE NOT ──────────────────────
 *
 * Each is a way two records can differ textually and mean the same destination,
 * or agree textually and mean different ones. A port that gets one backwards
 * either makes admins retype needlessly, or sends the password somewhere new:
 *
 *   host        trimmed AND lowercased — DNS is case-insensitive
 *   port        defaults to 8729, via `parseInt(r.port || '8729', 10)`
 *   tls         defaults TRUE; only `false` and the STRING 'false' disable it
 *   tlsInsecure defaults FALSE; the STRING 'true' enables it
 *
 * And one guard that is not a coercion at all: **an empty host never matches**,
 * not even another empty host. Without it two half-filled records would compare
 * equal and a password would be reused against nothing in particular.
 *
 * NO CREDENTIAL IS READ OR RECORDED HERE. The function compares only the five
 * fields above; the corpus carries no password field at all, and every value in
 * it is synthetic.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/same-endpoint-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const src = fs.readFileSync(path.join(LIVE, 'src', 'routers.js'), 'utf8');

// LIFTED rather than required: routers.js reads the data directory at load time,
// and this function needs none of that.
const decl = 'function sameEndpoint(a, b) {';
const start = src.indexOf(decl);
if (start === -1) {
  throw new Error('cannot find sameEndpoint — routers.js has been rewritten');
}
const end = src.indexOf('\n}', start);
if (end === -1) throw new Error('sameEndpoint is never closed');
let body = src.slice(start, end + 2);

// ── AND THE HELPERS IT NOW CALLS ──────────────────────────────────────────
//
// `sameEndpoint` used to spell its coercions inline. Upstream `92df43a` pulled
// them into one module-level `_isTrue(v)` — deliberately, because respelling the
// rule at each site is how `2af8164` came to fix one of four — so a slice of the
// function alone no longer runs: `ReferenceError: _isTrue is not defined`.
//
// That is the gate behaving correctly. It went RED on an upstream refactor
// rather than passing quietly, which is the whole argument for lifting instead
// of transcribing. The remedy is to widen the lift, never to inline a copy of
// the helper here — a copy would be a fork with no update path, and this file
// exists precisely because that fork already happened once.
for (const helper of ['function _isTrue(v) {']) {
  const hs = src.indexOf(helper);
  if (hs === -1) {
    throw new Error('cannot find ' + helper.trim() + ' in routers.js — if it was renamed or ' +
      'inlined again, update this list rather than copying the body here');
  }
  const he = src.indexOf('\n}', hs);
  if (he === -1) throw new Error(helper.trim() + ' is never closed');
  body = src.slice(hs, he + 2) + '\n' + body;
}

// MARKER ASSERTIONS. A slice that lost a comparison would record "everything
// matches" as the live behaviour, which is the exact failure this guards.
for (const marker of ["host(a) !== ''", 'port(a) === port(b)', 'user(a) === user(b)',
  'tls(a)  === tls(b)', 'lax(a)  === lax(b)', 'toLowerCase()',
  // The helper must be PRESENT IN THE LIFT, not merely findable in the file.
  "v === true || v === 'true'"]) {
  assert.ok(body.includes(marker),
    'the lifted sameEndpoint has no ' + marker + ' — the slice is wrong, or a field stopped '
    + 'being compared, which would let a stored password reach a new destination');
}

const ctx = { String, parseInt, module: { exports: {} } };
vm.createContext(ctx);
vm.runInContext(body + '\nmodule.exports = sameEndpoint;', ctx);
const sameEndpoint = ctx.module.exports;

// The stored record every case is compared against. RFC 2606 example domain,
// and a username invented for this file.
const STORED = {
  host: 'router.example.net', port: 8729, username: 'example-user',
  tls: true, tlsInsecure: false,
};
const with_ = (over) => Object.assign({}, STORED, over);

const CASES = {
  identical: [STORED, with_({})],

  // ── THE COERCIONS ────────────────────────────────────────────────────
  hostDiffersOnlyInCase: [STORED, with_({ host: 'ROUTER.EXAMPLE.NET' })],
  hostHasSurroundingSpace: [STORED, with_({ host: '  router.example.net  ' })],
  portAsAString: [STORED, with_({ port: '8729' })],
  portAbsentDefaultsTo8729: [with_({ port: 8729 }), with_({ port: undefined })],
  portZeroAlsoDefaults: [with_({ port: 8729 }), with_({ port: 0 })],
  portEmptyStringAlsoDefaults: [with_({ port: 8729 }), with_({ port: '' })],
  tlsAbsentDefaultsToOn: [with_({ tls: true }), with_({ tls: undefined })],
  tlsAsTheStringFalse: [with_({ tls: false }), with_({ tls: 'false' })],
  tlsAsTheStringTrue: [with_({ tls: true }), with_({ tls: 'true' })],
  laxAbsentDefaultsToOff: [with_({ tlsInsecure: false }), with_({ tlsInsecure: undefined })],
  laxAsTheStringTrue: [with_({ tlsInsecure: true }), with_({ tlsInsecure: 'true' })],
  // THE STRING 'false' MEANS OFF, and it did not always. `lax` was
  // `!!(r.tlsInsecure || r.tlsInsecure === 'true')` until 2026-08-27 — any
  // non-empty string is truthy, so 'false' passed the first test and never
  // reached the second, and a record saying certificate checking is ON read as
  // OFF. This port found it and it was FIXED UPSTREAM in 2af8164; `lax` is now
  // `=== true || === 'true'`, explicit like `tls` two lines above.
  //
  // The case stays, with the answer inverted, because it is the input that
  // distinguishes the two implementations: a port still carrying the old
  // coercion refuses here where the live function now matches, and an admin is
  // made to retype a password for a field that did not change.
  laxAsTheStringFalseMatchesBooleanFalse: [with_({ tlsInsecure: false }), with_({ tlsInsecure: 'false' })],
  // AND THE OTHER SPELLINGS ARE STILL OFF. `=== 'true'` is exact, so a port
  // widening it to truthiness would re-open the defect from the other side.
  laxAsAnUnrelatedString: [with_({ tlsInsecure: false }), with_({ tlsInsecure: 'yes' })],
  laxAsTheStringOne: [with_({ tlsInsecure: false }), with_({ tlsInsecure: '1' })],
  laxBothTheStringFalse: [with_({ tlsInsecure: 'false' }), with_({ tlsInsecure: 'false' })],
  // THE USERNAME IS TRIMMED BUT NOT LOWERCASED, which is a finer asymmetry than
  // the host's and was got wrong here first: surrounding space MATCHES, a
  // change of case does NOT. Both are the live rule — RouterOS logins are
  // case-sensitive, and a stray space in a form field is not a different user.
  usernameHasSurroundingSpace: [STORED, with_({ username: ' example-user ' })],

  // ── WHERE IT GOES: any change refuses ────────────────────────────────
  aDifferentHost: [STORED, with_({ host: 'elsewhere.example.com' })],
  aSubdomain: [STORED, with_({ host: 'a.router.example.net' })],
  aDifferentPort: [STORED, with_({ port: 8728 })],
  aDifferentUser: [STORED, with_({ username: 'someone-else' })],
  usernameCaseDiffers: [STORED, with_({ username: 'Example-User' })],

  // ── HOW IT TRAVELS: the two that decide who can read it ──────────────
  //
  // `tlsInsecure` matters as much as the host: turning it on accepts ANY
  // certificate, which makes the same hostname a man-in-the-middle.
  tlsTurnedOff: [STORED, with_({ tls: false })],
  tlsInsecureTurnedOn: [STORED, with_({ tlsInsecure: true })],

  // ── THE EMPTY-HOST GUARD ─────────────────────────────────────────────
  bothHostsEmpty: [with_({ host: '' }), with_({ host: '' })],
  bothHostsWhitespace: [with_({ host: '   ' }), with_({ host: '   ' })],
  storedHostEmpty: [with_({ host: '' }), STORED],
  submittedHostEmpty: [STORED, with_({ host: '' })],
  bothHostsAbsent: [with_({ host: undefined }), with_({ host: undefined })],

  // ── NEITHER RECORD MAY BE MISSING ────────────────────────────────────
  storedIsNull: [null, STORED],
  submittedIsNull: [STORED, null],
  bothNull: [null, null],
  storedIsUndefined: [undefined, STORED],
};

const cases = {};
for (const [name, [a, b]] of Object.entries(CASES)) {
  cases[name] = {
    a: a === undefined ? null : a,
    b: b === undefined ? null : b,
    same: sameEndpoint(a, b),
  };
}

// ── BELIEVABILITY ───────────────────────────────────────────────────────────
{
  const s = (n) => cases[n].same;

  // The matches. Each is a way two records differ textually and mean the same
  // destination; a port that refused them makes an admin retype for nothing.
  for (const n of ['identical', 'hostDiffersOnlyInCase', 'hostHasSurroundingSpace',
    'portAsAString', 'portAbsentDefaultsTo8729', 'portZeroAlsoDefaults',
    'portEmptyStringAlsoDefaults', 'tlsAbsentDefaultsToOn', 'tlsAsTheStringFalse',
    'tlsAsTheStringTrue', 'laxAbsentDefaultsToOff', 'laxAsTheStringTrue',
    'laxBothTheStringFalse', 'usernameHasSurroundingSpace']) {
    assert.equal(s(n), true, n + ' did NOT match. These describe the same destination reached '
      + 'the same way, and refusing them makes an admin retype a password for no reason');
  }

  // The refusals. Each is a way the password would reach somewhere, or travel
  // in a way, nobody stored it against.
  for (const n of ['aDifferentHost', 'aSubdomain', 'aDifferentPort', 'aDifferentUser',
    'usernameCaseDiffers', 'tlsTurnedOff', 'tlsInsecureTurnedOn',
    'bothHostsEmpty', 'bothHostsWhitespace', 'storedHostEmpty', 'submittedHostEmpty',
    'bothHostsAbsent', 'storedIsNull', 'submittedIsNull', 'bothNull', 'storedIsUndefined']) {
    assert.equal(s(n), false, n + ' MATCHED. The stored password would be sent to a destination '
      + 'nobody stored it against — this route is a credential oracle without that refusal');
  }

  // USERNAME IS CASE-SENSITIVE WHERE HOST IS NOT, and that asymmetry is the
  // live behaviour rather than an oversight: DNS is case-insensitive and
  // RouterOS logins are not.
  // HOST is trimmed AND lowercased; USERNAME is trimmed and NOT lowercased.
  // Both halves asserted, because a port applying one rule to both fields would
  // pass a test that only checked the other.
  assert.equal(s('hostDiffersOnlyInCase'), true);
  assert.equal(s('hostHasSurroundingSpace'), true);
  assert.equal(s('usernameHasSurroundingSpace'), true);
  assert.equal(s('usernameCaseDiffers'), false,
    'a username differing only in case MATCHED. RouterOS logins are case-sensitive, so this '
    + 'is a different account and the stored password must not be reused for it');

  // THE tls / tlsInsecure PAIR, asserted together because they are two spellings
  // of one idea and a port is tempted to share an implementation. They agree on
  // what "false" means and disagree on everything else: `tls` DEFAULTS ON and
  // treats anything that is not the word "false" as on, `tlsInsecure` DEFAULTS
  // OFF and treats only the word "true" as on. Sharing one helper would flip a
  // default.
  assert.equal(s('tlsAsTheStringFalse'), true,
    "tls: 'false' is handled explicitly and matches a boolean false");
  assert.equal(s('laxAsTheStringFalseMatchesBooleanFalse'), true,
    "tlsInsecure: 'false' means OFF since 2af8164 and must match a boolean false. A port still "
    + 'carrying the old truthiness coercion refuses here, and the admin is made to retype a '
    + 'password because a field that did not change appears to have');
  assert.equal(s('tlsAbsentDefaultsToOn'), true, 'tls defaults ON');
  assert.equal(s('laxAbsentDefaultsToOff'), true, 'tlsInsecure defaults OFF');
  // The exactness of `=== 'true'`, in both directions.
  assert.equal(s('laxAsTheStringTrue'), true, "tlsInsecure: 'true' is the one string that means ON");
  for (const n of ['laxAsAnUnrelatedString', 'laxAsTheStringOne']) {
    assert.equal(s(n), true, n + ': only the exact word "true" turns tlsInsecure on. A port using '
      + 'truthiness would refuse here — and would be re-opening the defect fixed in 2af8164, where '
      + 'a record saying certificate checking is ON read as OFF');
  }

  // Believability across the set: both answers occur.
  const yes = Object.values(cases).filter((c) => c.same).length;
  assert.ok(yes > 0 && yes < Object.keys(cases).length,
    'every case has the same answer, so this corpus proves nothing');

  // NO CREDENTIAL FIELD ANYWHERE IN THE CORPUS. Asserted rather than assumed:
  // this file transplants into a public repository at cutover.
  const encoded = JSON.stringify(cases);
  for (const banned of ['password', 'passwordHash', 'salt', 'secret']) {
    assert.ok(!encoded.includes(banned),
      'the corpus carries a ' + banned + ' field — sameEndpoint reads none, so nothing here '
      + 'should record one');
  }
}

const OUT = path.join(ROOT, 'testdata', 'same-endpoint-cases.json');
const text = JSON.stringify({ stored: STORED, cases }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== text) {
    console.error('testdata/same-endpoint-cases.json is stale — run: '
      + 'node tools/same-endpoint-cases.js');
    process.exit(1);
  }
  console.log('same-endpoint-cases.json is current');
} else {
  fs.writeFileSync(OUT, text);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' comparisons)');
}
