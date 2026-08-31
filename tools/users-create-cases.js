'use strict';
/**
 * `createUser` — the WRITE side of users.json — by actually running it.
 *
 * ---- THE COMPANION TO users-public-cases.js -------------------------------
 *
 * That one pins `_toPublic`/`listUsers`: what a record DISCLOSES. This one pins
 * what a record IS — the seven fields, their order, their shapes, and the ways
 * `createUser` refuses.
 *
 * ---- NOT SLICED. RUN. -----------------------------------------------------
 *
 * `src/users.js` requires only `fs`, `path` and `crypto`, and takes its
 * directory from `DATA_DIR`. So unlike `rbac.js` or the inline route handlers,
 * it can simply be required and pointed at a throwaway directory — which makes
 * this the strongest kind of corpus here: the real function, writing a real
 * file, whose bytes are then read back.
 *
 * ---- WHAT IS RANDOM AND WHAT IS PINNED ------------------------------------
 *
 * The id, the salt, the hash and the timestamp differ on every call, so their
 * VALUES cannot be expected. Their SHAPES can, and the shapes are the part a
 * port gets wrong: a 32-character salt instead of 64, a hash from the wrong
 * scrypt parameters, an id that is not a UUID, or `createdAt` as an RFC 3339
 * string instead of a millisecond epoch — which is what a Go port reaches for by
 * default, and which `users.js` would then read back as `NaN`.
 *
 * ---- THE ASYMMETRY WORTH KNOWING ------------------------------------------
 *
 * `_validRole` THROWS on an unrecognised role, and on an ABSENT one. It does not
 * default. `Rbac.syncUserGrants`, in another file, maps an unrecognised role to
 * `viewer` instead — and both are deliberate: validation at the write boundary,
 * least privilege at the read boundary. A port sharing one helper between them
 * would either throw where the live app grants viewer, or silently create a user
 * the live app refuses to create.
 *
 * The live comment on `ROLES` says why the throw exists: both call sites once
 * read `role === 'viewer' ? 'viewer' : 'admin'`, so ANY unrecognised value
 * became an administrator — "a typo, a stale client, or a role added later and
 * not yet wired up here".
 *
 * ---- NOTHING HERE IS REAL -------------------------------------------------
 *
 * Every password is a literal invented in this file, and the module is pointed
 * at a fresh directory under the system temp dir. The operator's /data is never
 * read, and no hash from it can reach a file that transplants into a public
 * repository at cutover. No hash or salt VALUE is recorded either — only its
 * length and whether it is hex.
 *
 *   node tools/users-create-cases.js          # write
 *   node tools/users-create-cases.js --check  # fail if stale
 */

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', 'users-create-cases.json');

// A throwaway directory, created BEFORE `users.js` is required — the module
// reads DATA_DIR at load time, so setting it afterwards would silently point the
// whole run at the operator's /data.
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-users-'));
process.env.DATA_DIR = TMP;
const Users = require(path.join(LIVE, 'src', 'users.js'));

// Invented here and used nowhere.
const PW = 'corpus-only-not-a-real-password';

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const HEX_RE = /^[0-9a-f]+$/;
const USERS_FILE = path.join(TMP, 'users.json');

function readFile() {
  return JSON.parse(fs.readFileSync(USERS_FILE, 'utf8'));
}
function readFileSafe() {
  try {
    return readFile();
  } catch (_) {
    return [];
  }
}

const ACCEPT = [
  ['a plain admin', { username: 'ann', password: PW, role: 'admin', allowedRouterIds: [] }],
  ['an operator, the role added last and the reason the throw exists',
    { username: 'bob', password: PW, role: 'operator', allowedRouterIds: ['r1'] }],
  ['a viewer', { username: 'cy', password: PW, role: 'viewer', allowedRouterIds: ['r1', 'r2'] }],
  ['THE USERNAME IS TRIMMED', { username: '  di  ', password: PW, role: 'viewer', allowedRouterIds: [] }],
  ['allowedRouterIds ABSENT becomes an empty array, not undefined',
    { username: 'eve', password: PW, role: 'admin' }],
  ['allowedRouterIds as a NON-ARRAY also becomes an empty array',
    { username: 'fay', password: PW, role: 'admin', allowedRouterIds: 'r1' }],
  ['a username with an ampersand, which must reach disk unescaped',
    { username: 'g & h', password: PW, role: 'viewer', allowedRouterIds: [] }],
  ['a non-ASCII username', { username: 'Ünal', password: PW, role: 'viewer', allowedRouterIds: [] }],
  ['an EMPTY password is accepted HERE - the length rule belongs to the HTTP layer',
    { username: 'ivy', password: '', role: 'viewer', allowedRouterIds: [] }],
];

// `_validRole` throws, does not default, and the MESSAGE names the valid list —
// which is how an operator learns a role was added.
const REFUSE = [
  ['an unrecognised role', { username: 'x1', password: PW, role: 'superuser', allowedRouterIds: [] }],
  ['an ABSENT role - there is no default', { username: 'x2', password: PW, allowedRouterIds: [] }],
  ['a capitalised role - the match is exact', { username: 'x3', password: PW, role: 'Admin', allowedRouterIds: [] }],
  ['the empty string', { username: 'x4', password: PW, role: '', allowedRouterIds: [] }],
  ['null', { username: 'x5', password: PW, role: null, allowedRouterIds: [] }],
];

async function main() {
  const accepted = [];
  const refused = [];

  for (const [why, input] of ACCEPT) {
    const before = readFileSafe().length;
    const pub = await Users.createUser(input);
    const all = readFile();
    const rec = all[all.length - 1];
    accepted.push({
      why,
      input: { ...input, password: '<invented>' },
      // THE KEY ORDER of the stored record and of the public view, recorded
      // because it is what a Go `map[string]any` cannot reproduce.
      recordKeys: Object.keys(rec),
      publicKeys: Object.keys(pub),
      // The values that are not random.
      username: rec.username,
      role: rec.role,
      allowedRouterIds: rec.allowedRouterIds,
      // The SHAPES of the ones that are. No value is recorded.
      idIsUuidV4: UUID_RE.test(rec.id),
      saltLen: rec.salt.length,
      saltIsHex: HEX_RE.test(rec.salt),
      hashLen: rec.passwordHash.length,
      hashIsHex: HEX_RE.test(rec.passwordHash),
      createdAtType: typeof rec.createdAt,
      createdAtDigits: String(rec.createdAt).length,
      // `_toPublic` is a denylist of two. Recorded as what is MISSING, because
      // that is the security property.
      publicOmits: ['passwordHash', 'salt'].filter((k) => !(k in pub)),
      appended: all.length === before + 1,
    });
  }

  for (const [why, input] of REFUSE) {
    const before = readFileSafe().length;
    let message = null;
    try {
      await Users.createUser(input);
    } catch (e) {
      message = e.message;
    }
    refused.push({
      why,
      input: { ...input, password: '<invented>' },
      message,
      // NOTHING WAS WRITTEN. A port that appended and then validated would leave
      // a user with an invalid role in the file, and `_validRole` is the only
      // thing standing between a typo and an administrator.
      wroteNothing: readFileSafe().length === before,
    });
  }

  const bytes = fs.readFileSync(USERS_FILE);
  const text = bytes.toString('utf8');
  return {
    accepted,
    refused,
    file: {
      endsWithNewline: bytes[bytes.length - 1] === 0x0a,
      // The actual second line, rather than an assertion about the indent.
      secondLine: text.split('\n')[1],
      // The same property `tools/jsonwrite-cases.js` pins for the Go encoder,
      // checked here on a file the LIVE implementation wrote.
      ampersandRaw: text.includes('"g & h"'),
    },
    roles: Users.ROLES || null,
  };
}

main().then((data) => {
  // ---- Believability -----------------------------------------------------
  // Every check is about what the LIVE module did. The silent failure this
  // guards against is a module that threw on everything, leaving two empty
  // lists that any Go implementation would satisfy.
  const { accepted, refused, file, roles } = data;
  if (accepted.length < 8) throw new Error(`only ${accepted.length} accepted cases`);
  if (refused.length < 4) throw new Error(`only ${refused.length} refusals`);

  const byWhy = Object.fromEntries(accepted.map((c) => [c.why, c]));
  const need = (k) => {
    if (!byWhy[k]) throw new Error(`a believability check names a case that is gone: ${k}`);
    return byWhy[k];
  };

  // THE SEVEN FIELDS, in order. `store.User` models five; a round trip through
  // it drops two.
  const keys = accepted[0].recordKeys;
  if (keys.length !== 7) {
    throw new Error(`a record has ${keys.length} fields, expected 7: ${keys.join(',')}`);
  }
  for (const c of accepted) {
    if (c.recordKeys.join(',') !== keys.join(',')) {
      throw new Error(`${c.why}: key order differs from the first record`);
    }
    if (!c.idIsUuidV4) throw new Error(`${c.why}: the id is not a UUID`);
    if (c.saltLen !== 64 || !c.saltIsHex) throw new Error(`${c.why}: salt is ${c.saltLen} chars`);
    if (c.hashLen !== 128 || !c.hashIsHex) throw new Error(`${c.why}: hash is ${c.hashLen} chars`);
    if (c.createdAtType !== 'number' || c.createdAtDigits !== 13) {
      throw new Error(`${c.why}: createdAt is a ${c.createdAtType} of ${c.createdAtDigits} digits; `
        + 'it must be a 13-digit millisecond epoch, not a formatted date');
    }
    if (c.publicOmits.length !== 2) {
      throw new Error(`${c.why}: the public view leaks ${JSON.stringify(c.publicOmits)}`);
    }
    if (!c.appended) throw new Error(`${c.why}: the record was not appended`);
  }
  // THE TRIM, and the two coercions.
  if (need('THE USERNAME IS TRIMMED').username !== 'di') {
    throw new Error('the username is no longer trimmed');
  }
  for (const k of ['allowedRouterIds ABSENT becomes an empty array, not undefined',
    'allowedRouterIds as a NON-ARRAY also becomes an empty array']) {
    const v = need(k).allowedRouterIds;
    if (!Array.isArray(v) || v.length !== 0) {
      throw new Error(`${k}: got ${JSON.stringify(v)}`);
    }
  }
  // EVERY REFUSAL THREW AND WROTE NOTHING.
  for (const c of refused) {
    if (!c.message) {
      throw new Error(`${c.why}: createUser ACCEPTED it. _validRole is the only thing between `
        + 'a typo and an administrator - read the ROLES comment in users.js.');
    }
    if (!c.wroteNothing) throw new Error(`${c.why}: it threw but still wrote a record`);
    if (!/Invalid role/.test(c.message)) {
      throw new Error(`${c.why}: the message no longer names the problem: ${c.message}`);
    }
  }
  // THE FILE SHAPE, from the live writer, cross-checking what
  // tools/jsonwrite-cases.js asserts about the Go encoder.
  if (file.endsWithNewline) {
    throw new Error('users.js now writes a trailing newline; internal/store/jsonwrite.go trims one '
      + 'on the strength of it not doing so');
  }
  if (file.secondLine !== '  {') {
    throw new Error(`the indent changed: the second line is ${JSON.stringify(file.secondLine)}`);
  }
  if (!file.ampersandRaw) {
    throw new Error('an ampersand reached disk escaped, which JSON.stringify does not do');
  }
  if (!Array.isArray(roles) || roles.length < 3) {
    throw new Error('ROLES is not exported as a list any more; the port copies it from here');
  }

  const json = JSON.stringify({
    generated_from: 'src/users.js createUser, run against a throwaway DATA_DIR',
    roles,
    file,
    accepted,
    refused,
  }, null, 2) + '\n';

  if (process.argv.includes('--check')) {
    const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
    if (have !== json) {
      console.error('STALE: testdata/users-create-cases.json - re-run tools/users-create-cases.js');
      process.exit(1);
    }
    console.log(`users-create-cases: up to date (${accepted.length} accepted, ${refused.length} refused)`);
  } else {
    fs.writeFileSync(OUT, json);
    console.log(`wrote ${OUT} (${accepted.length} accepted, ${refused.length} refused, roles: ${roles.join('/')})`);
  }
  fs.rmSync(TMP, { recursive: true, force: true });
}).catch((e) => {
  fs.rmSync(TMP, { recursive: true, force: true });
  console.error(e.message);
  process.exit(1);
});
