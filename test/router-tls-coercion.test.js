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

test('no truthiness coercion of a request boolean survives anywhere in src/', () => {
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
      // `!!(` on a field read off a request body or a stored record. The FIELD
      // NAME is deliberately absent: the first version of this ledger named
      // tlsInsecure, and alertsEnabled and disabled sat five lines away carrying
      // the identical defect. A ledger over one member of a class is how the
      // rest of the class survives.
      if (/!!\s*\(\s*(data|body|updates|patch|existing)\./.test(line)) {
        offenders.push(path.relative(SRC, f) + ':' + (i + 1) + '  ' + line.trim());
      }
    });
  }

  assert.deepEqual(offenders, [],
    'a boolean off a request body or a stored record must go through _isTrue, ' +
    'never `!!` — the string "false" is truthy:\n' + offenders.join('\n'));
});

test('the ledger can actually see an offender', () => {
  // The companion the ledger needs. A scan that finds nothing is
  // indistinguishable from a scan that never ran, and this repo has shipped
  // both — a comment-stripping bug once made a check pass by matching its own
  // explanation. So exercise the pattern against a known-bad line and a
  // known-good one rather than trusting the empty result.
  const BAD  = "    tlsInsecure:   !!(data.tlsInsecure || data.tlsInsecure === 'true'),";
  const BAD2 = "    disabled: data.disabled !== undefined ? !!(data.disabled) : !!(existing.disabled),";
  const GOOD = "    tlsInsecure:   _isTrue(data.tlsInsecure),";
  const RE   = /!!\s*\(\s*(data|body|updates|patch|existing)\./;

  assert.equal(RE.test(BAD),  true,  'the ledger would not have caught the original defect');
  assert.equal(RE.test(BAD2), true,  'nor the disabled/alertsEnabled members of the same class');
  assert.equal(RE.test(GOOD), false, 'the ledger rejects the fixed form');
});

// ── The rest of the class: alertsEnabled and disabled ───────────────────────
//
// Filed by the port after the tlsInsecure fix landed, having enumerated the
// class rather than the instance. `disabled` is the one with teeth: PUT with
// `disabled: "false"` is an operator ENABLING a router, and the truthiness form
// read it as true and disabled it — tearing the session down.
//
// The stored-value branches mattered as much as the incoming ones. They read
// `!!(existing.disabled)`, so a record written by an earlier binary holding the
// string "false" would have the bug revived on every read. They now go through
// the same predicate, which is read-time normalisation — the migration approach
// used elsewhere in this file.

test('update() reads disabled: "false" as ENABLED', () => {
  const r = add();
  Routers.update(r.id, { disabled: true });
  assert.equal(Routers.update(r.id, { disabled: 'false' }).disabled, false,
    'an operator enabling a router must not have it disabled instead');
});

test('update() still disables on a genuine request', () => {
  const r = add();
  assert.equal(Routers.update(r.id, { disabled: 'true' }).disabled, true);
  assert.equal(Routers.update(r.id, { disabled: true }).disabled,   true);
});

test('update() leaves disabled alone when the field is absent', () => {
  const off = add();
  Routers.update(off.id, { disabled: true });
  assert.equal(Routers.update(off.id, { label: 'renamed-disabled' }).disabled, true);

  const on = add();
  assert.equal(Routers.update(on.id, { label: 'renamed-enabled' }).disabled, false);
});

test('update() treats junk as ENABLED rather than taking a router out of service', () => {
  const r = add();
  for (const v of ['1', 'yes', 'TRUE', 'on', 1, {}, []]) {
    Routers.update(r.id, { disabled: false });
    assert.equal(Routers.update(r.id, { disabled: v }).disabled, false,
      'disabled a router on: ' + JSON.stringify(v));
  }
});

test('alertsEnabled reads "false" as off, on both add and update', () => {
  assert.equal(add({ alertsEnabled: 'false' }).alertsEnabled, false);
  const r = add({ alertsEnabled: true });
  assert.equal(Routers.update(r.id, { alertsEnabled: 'false' }).alertsEnabled, false);
});

test('alertsEnabled still honours a genuine request', () => {
  assert.equal(add({ alertsEnabled: 'true' }).alertsEnabled, true);
  assert.equal(add({ alertsEnabled: true }).alertsEnabled,   true);
  assert.equal(add().alertsEnabled, false, 'absent means off, as before');
});

test('a stored "false" written by an earlier binary is not revived as true', () => {
  // The half that is invisible from the request side. Before the fix these
  // branches were `!!(existing.X)`, so a record the buggy version wrote would
  // have the defect re-applied every time it was read — a fix that only touched
  // the incoming values would have left every already-corrupted record wrong.
  const r = add();
  const raw = JSON.parse(fs.readFileSync(path.join(process.env.DATA_DIR, 'routers.json'), 'utf8'));
  const rec = raw.find(x => x.id === r.id);
  rec.disabled = 'false'; rec.alertsEnabled = 'false'; rec.tlsInsecure = 'false';
  fs.writeFileSync(path.join(process.env.DATA_DIR, 'routers.json'), JSON.stringify(raw));
  Routers.invalidateCache();

  // An edit that does not mention any of them must normalise, not preserve.
  const out = Routers.update(r.id, { label: 'touched' });
  assert.equal(out.disabled,      false, 'a stored "false" must read as false');
  assert.equal(out.alertsEnabled, false);
  assert.equal(out.tlsInsecure,   false);
});

// ── The legacy seed: the likeliest place of all to meet a stored string ──────
//
// loadAll() migrates a pre-multi-router settings.json into routers.json the
// first time it runs. It read `!!s.routerTlsInsecure`, so a settings.json
// holding the string "false" seeded a router that accepts forged certificates —
// and wrote it, permanently.
//
// This path exists specifically to read OLD data, which makes it the likeliest
// place in the codebase to meet a boolean stored as a string, and the worst
// place to get it wrong because the answer is persisted rather than recomputed.
//
// Run in a child process: the seed branch only fires when routers.json does not
// exist, and the tests above have already created one in this process's DATA_DIR.
test('the settings.json seed reads stored booleans the same way as addRouter', () => {
  const { execFileSync } = require('node:child_process');

  const seed = (routerTlsInsecure, routerTls) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'router-seed-'));
    fs.writeFileSync(path.join(dir, 'settings.json'), JSON.stringify({
      routerHost: '10.0.0.53', routerPort: 8729, routerUser: 'mikrodash',
      routerTls, routerTlsInsecure,
    }));
    const out = execFileSync(process.execPath, ['-e', `
      process.env.DATA_DIR = ${JSON.stringify(dir)};
      const R = require(${JSON.stringify(path.join(__dirname, '..', 'src', 'routers'))});
      const r = R.loadAll()[0];
      console.log(JSON.stringify({ tls: r.tls, tlsInsecure: r.tlsInsecure }));
    `], { encoding: 'utf8' });
    return JSON.parse(out.trim().split('\n').pop());
  };

  // The defect: a stored "false" seeded a router that accepts forged certs.
  assert.deepEqual(seed('false', true), { tls: true, tlsInsecure: false },
    'a stored "false" must not seed a router that accepts a forged certificate');

  // The believability twin — a genuine opt-in still survives the migration.
  assert.deepEqual(seed('true', true), { tls: true, tlsInsecure: true });
  assert.deepEqual(seed(true, true),   { tls: true, tlsInsecure: true });

  // tls defaults ON, so its rule is the other one: only false or "false" is off.
  assert.equal(seed(false, 'false').tls, false,
    'a stored "false" for tls must turn TLS off, matching addRouter');
  assert.equal(seed(false, undefined).tls, true, 'absent still means on');
});
