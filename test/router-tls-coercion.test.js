'use strict';
/**
 * `tlsInsecure` is a security predicate, and it arrives as a STRING.
 *
 * `tlsInsecure` decides whether a forged certificate is accepted for every
 * future connection to a router. The value reaches the store straight from a
 * request body — `POST /api/routers` and `PUT /api/routers/:id` pass `req.body`
 * through with no earlier coercion — and a JSON body, a form post or any client
 * that stringifies its booleans all deliver the four characters `"false"`.
 *
 * `!!('false')` is true. So the truthiness form read "accept forged
 * certificates" from a request that said the opposite: an operator turning the
 * relaxed check OFF turned it ON.
 *
 * 2af8164 fixed this in `sameEndpoint`, which is the LEAST consequential of the
 * places it appeared — that one only decides whether a stored password may be
 * reused, and it failed closed (the two sides disagreed, so the password was
 * refused and somebody was prompted). The three fixed here decide what is
 * WRITTEN, and they fail open: nothing disagrees afterwards, so nothing ever
 * prompts anyone. The wrong value lands in routers.json and stays.
 *
 * Found by the Go/TypeScript port while diffing 2af8164 against its own
 * `internal/store/routeradd.go`.
 */

const { test } = require('node:test');
const assert   = require('node:assert/strict');
const fs       = require('node:fs');
const os       = require('node:os');
const path     = require('node:path');

// Before requiring the store: Routers.add() WRITES, and the real /data holds
// live encrypted credentials.
process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'router-tls-'));

const Routers = require('../src/routers');

let _n = 0;
// Distinct labels and hosts: _uniqueLabel() renames collisions, which would
// make a failure here read as a labelling bug.
const add = (fields = {}) =>
  Routers.add({ label: 'R' + (++_n), host: '10.0.0.' + _n, ...fields });

// ── the defect, on the path that stores a new router ────────────────────────

test('add() reads the string "false" as false, not as true', () => {
  assert.equal(add({ tlsInsecure: 'false' }).tlsInsecure, false,
    'an operator turning the relaxed check off must not turn it on');
});

test('add() still honours a genuine request for the relaxed check', () => {
  // The believability twin. Without it, "false" could be passing because the
  // field had been hard-wired off, which would be a different bug wearing the
  // same green tick.
  assert.equal(add({ tlsInsecure: 'true' }).tlsInsecure, true);
  assert.equal(add({ tlsInsecure: true }).tlsInsecure,   true);
});

test('add() defaults to certificate checking ON when the field is absent', () => {
  assert.equal(add().tlsInsecure, false);
  assert.equal(add({ tlsInsecure: undefined }).tlsInsecure, false);
  assert.equal(add({ tlsInsecure: false }).tlsInsecure, false);
});

test('add() treats other truthy junk as strict rather than as consent', () => {
  // Only an explicit true, or the string a form sends for it, may relax the
  // check. Anything else is a client bug, and the safe reading of a client bug
  // is the strict one.
  for (const v of ['1', 'yes', 'TRUE', 'on', 1, {}, []]) {
    assert.equal(add({ tlsInsecure: v }).tlsInsecure, false,
      'accepted a forged certificate on: ' + JSON.stringify(v));
  }
});

// ── the same defect on the path that EDITS a stored router ──────────────────

test('update() reads the string "false" as false, not as true', () => {
  const r = add({ tlsInsecure: true });
  assert.equal(Routers.update(r.id, { tlsInsecure: 'false' }).tlsInsecure, false,
    'turning the relaxed check off on an existing router must actually turn it off');
});

test('update() still honours a genuine request for the relaxed check', () => {
  const r = add({ tlsInsecure: false });
  assert.equal(Routers.update(r.id, { tlsInsecure: 'true' }).tlsInsecure, true);
});

test('update() leaves the stored value alone when the field is absent', () => {
  // The `!== undefined` guard is why an edit that only renames a router does not
  // silently reset its certificate policy. It must survive the fix.
  const lax = add({ tlsInsecure: true });
  assert.equal(Routers.update(lax.id, { label: 'renamed-lax' }).tlsInsecure, true);

  const strict = add({ tlsInsecure: false });
  assert.equal(Routers.update(strict.id, { label: 'renamed-strict' }).tlsInsecure, false);
});

// ── the ledger ──────────────────────────────────────────────────────────────

test('no truthiness coercion of tlsInsecure survives anywhere in src/', () => {
  // A LEDGER over the whole tree, not a window on the three lines just fixed.
  //
  // This defect appeared four times in two files, and 2af8164 fixed exactly one
  // of them — because the fix was verified by reading the line it changed. The
  // third site lives inside an Express route in a 7,400-line index.js and cannot
  // be imported on its own, so scanning is the only thing that reaches it; and
  // if scanning is what reaches it, it may as well reach everywhere at once.
  //
  // Comments are stripped first. The prose above says `!!(` while explaining why
  // it is wrong, and a check that fails on its own explanation is worse than no
  // check: it teaches whoever hits it to weaken the pattern.
  const SRC = path.join(__dirname, '..', 'src');

  const files = [];
  (function walk(dir) {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.name.endsWith('.js')) files.push(p);
    }
  })(SRC);

  const stripComments = (s) => s
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:])\/\/[^\n]*/g, '$1');

  const offenders = [];
  for (const f of files) {
    const code = stripComments(fs.readFileSync(f, 'utf8'));
    code.split('\n').forEach((line, i) => {
      // `!!(` anything `tlsInsecure` — the shape of the coercion, whichever
      // object the field hangs off (data, body, s, router, ...).
      if (/!!\s*\([^)]*tlsInsecure/.test(line)) {
        offenders.push(path.relative(SRC, f) + ':' + (i + 1) + '  ' + line.trim());
      }
    });
  }

  assert.deepEqual(offenders, [],
    'tlsInsecure must be compared explicitly (=== true || === \'true\'), never coerced:\n' +
    offenders.join('\n'));
});

test('the ledger can actually see an offender', () => {
  // The companion the ledger needs. A scan that finds nothing is
  // indistinguishable from a scan that never ran, and this repo has shipped
  // both — a comment-stripping bug once made a check pass by matching its own
  // explanation. So exercise the pattern against a known-bad line and a
  // known-good one rather than trusting the empty result.
  const BAD  = "    tlsInsecure:   !!(data.tlsInsecure || data.tlsInsecure === 'true'),";
  const GOOD = "    tlsInsecure:   data.tlsInsecure === true || data.tlsInsecure === 'true',";
  const RE   = /!!\s*\([^)]*tlsInsecure/;

  assert.equal(RE.test(BAD),  true,  'the ledger would not have caught the original defect');
  assert.equal(RE.test(GOOD), false, 'the ledger rejects the fixed form');
});
