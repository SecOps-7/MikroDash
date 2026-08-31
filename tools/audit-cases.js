'use strict';
/**
 * The audit redaction/diff contract, as the LIVE implementation actually
 * computes it.
 *
 * Same idea as tools/make-golden.js and for the same reason: the Go port is not
 * asked to be plausible, it is asked to be identical. `audit.diff()` is where
 * that matters most, because it is the function standing between a router
 * password and a table that is deliberately hard to delete. A Go version that
 * masks nearly the same set of fields is not a port, it is a leak with good
 * intentions.
 *
 * RUNS IN THE APP CONTAINER, NOT ON THE HOST. src/audit.js requires ./db, which
 * requires better-sqlite3, which is only installed where the app runs. That is
 * the same constraint tools/capture-fixtures.js already lives under, and it has
 * the same answer: copy this in and run it there.
 *
 *   docker exec mikrodash rm -rf /tools
 *   docker cp tools mikrodash:/tools
 *   docker exec -e MIKRODASH_SRC=/app -e AUDIT_OUT=/audit-cases.json \
 *     mikrodash node /tools/audit-cases.js
 *   docker cp mikrodash:/audit-cases.json testdata/audit-diff-cases.json
 *
 * `--check` COMPARES AGAINST $AUDIT_OUT, so the committed file has to be copied
 * IN first or the check compares against nothing and reports "stale" for a file
 * that is perfectly current:
 *
 *   docker cp testdata/audit-diff-cases.json mikrodash:/audit-cases.json
 *   docker exec -e MIKRODASH_SRC=/app -e AUDIT_OUT=/audit-cases.json \
 *     mikrodash node /tools/audit-cases.js --check
 *
 * `--check` exits 1 when the committed file no longer matches what the live
 * implementation produces, so a change to audit.js cannot silently drift away
 * from the Go implementation pinned against it.
 *
 * The cases are chosen for the EDGES, because the middle is not where this goes
 * wrong: credential detection by pattern rather than by list, a partial update
 * that must not report untouched fields, a value long enough to clip, and a
 * value whose length differs between UTF-16 code units and Unicode code points.
 */

const fs   = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT  = process.env.AUDIT_OUT || path.join(__dirname, '..', 'testdata', 'audit-diff-cases.json');

const audit = require(path.join(ROOT, 'src', 'audit.js'));

// A string whose UTF-16 length and code-point count differ, long enough to clip.
// Each of these is ONE code point and TWO UTF-16 code units, so `.length` sees
// 800 where a Go `[]rune` would see 400 — the clip lands somewhere else unless
// the port counts the way JavaScript counts.
const EMOJI_LONG = '\u{1F600}'.repeat(400);

const CASES = [
  { name: 'plain string change',
    before: { host: 'a' }, after: { host: 'b' } },

  { name: 'unchanged key is not reported',
    before: { host: 'a', port: 8729 }, after: { host: 'a', port: 8729 } },

  { name: 'a key only in before is ignored — partial updates are the norm',
    before: { host: 'a', removed: 'gone' }, after: { host: 'b' } },

  { name: 'credential by explicit CREDENTIAL_FIELDS entry',
    before: { routerPass: 'placeholder-1' }, after: { routerPass: 'placeholder-2' } },

  { name: 'credential by pattern, not by list',
    before: { myApiKey: 'aaa', userPassword: 'bbb', someToken: 'ccc' },
    after:  { myApiKey: 'zzz', userPassword: 'yyy', someToken: 'xxx' } },

  { name: 'credential cleared — to is «unset», not «changed»',
    before: { routerPass: 'placeholder-1' }, after: { routerPass: '' } },

  { name: 'credential set for the first time — from is «unset»',
    before: {}, after: { routerPass: 'placeholder-1' } },

  { name: 'credential set to null',
    before: { smtpPass: 'x' }, after: { smtpPass: null } },

  { name: 'number and boolean keep their JSON type',
    before: { port: 8728, tls: false }, after: { port: 8729, tls: true } },

  { name: 'equal objects compare by JSON and are skipped',
    before: { layout: { a: 1, b: [2, 3] } }, after: { layout: { a: 1, b: [2, 3] } } },

  { name: 'changed object is stringified',
    before: { layout: { a: 1 } }, after: { layout: { a: 2 } } },

  { name: 'long ASCII string is clipped at 300 with an ellipsis',
    before: { note: 'x' }, after: { note: 'y'.repeat(500) } },

  { name: 'long emoji string clips by UTF-16 code units, not code points',
    before: { note: 'x' }, after: { note: EMOJI_LONG } },

  { name: 'value appearing where there was none',
    before: {}, after: { comment: 'new' } },

  { name: 'value becoming null',
    before: { comment: 'was' }, after: { comment: null } },
];

// Spelling matters more than membership here: the pattern is what catches a
// field settings.js has never heard of, and `tokenizer` / `passive` are the
// reminder that a substring match is what it is.
const FIELD_CASES = [
  'routerPass', 'telegramBotToken', 'pushbulletApiKey', 'smtpUser', 'smtpPass',
  'ntfyToken', 'password', 'userPassword', 'apiKey', 'api_key', 'privkey',
  'private_key', 'passphrase', 'credential', 'secret', 'token', 'TOKEN',
  'host', 'port', 'name', 'comment', 'interface', '', 'passive', 'tokenizer',
  // THE HYPHENATED SPELLINGS — the ones RouterOS actually uses — were missing
  // from this list, and their absence made the whole contract blind exactly
  // where it mattered. When audit.js was later widened to mask them, `--check`
  // reported "up to date" because nothing here asked about them. A contract
  // that omits the interesting cases is not a contract.
  'private-key', 'pre-shared-key', 'api-key', 'auth-key', 'psk',
  'privateKey', 'PrivateKey', 'preSharedKey', 'pre_shared_key', 'wg-private-key',
];

function build() {
  return {
    note: 'Generated by tools/audit-cases.js from the live src/audit.js. ' +
          'Both implementations are pinned to this file; see the header there.',
    markers: { set: audit.SET, unset: audit.UNSET, changed: audit.CHANGED },
    isCredentialField: FIELD_CASES.map(f => ({ field: f, credential: audit.isCredentialField(f) })),
    diff: CASES.map(c => ({
      name:   c.name,
      before: c.before,
      after:  c.after,
      expect: audit.diff(c.before, c.after),
    })),
  };
}

const body = JSON.stringify(build(), null, 2) + '\n';

if (process.argv.includes('--check')) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
  if (cur !== body) {
    console.error('testdata/audit-diff-cases.json is stale — run: node tools/audit-cases.js');
    process.exit(1);
  }
  console.log('audit diff cases up to date (' + CASES.length + ' diffs, ' +
              FIELD_CASES.length + ' field checks)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + OUT + ' (' + CASES.length + ' diffs, ' +
              FIELD_CASES.length + ' field checks)');
}
