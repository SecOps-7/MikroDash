'use strict';
/**
 * `Users.updateUser` and `Users.deleteUser`, RUN — not read.
 *
 * ---- WHY -------------------------------------------------------------------
 *
 * `LOOP.md` item 1a: the Settings page's user form needs `PUT /api/users/:id`
 * and `DELETE /api/users/:id`, and this port has neither the routes nor the
 * store writers beneath them. `internal/store` has `CreateUser` and
 * `SetPassword`; it has no update and no delete.
 *
 * ---- THE FOUR RULES THAT ARE EASY TO GET WRONG ----------------------------
 *
 *  1. **`undefined` MEANS LEAVE IT ALONE.** Every field is guarded by
 *     `!== undefined`, so a form that submits only what changed edits only what
 *     changed. A port merging a zero-valued struct would blank three fields on
 *     every rename.
 *
 *  2. **AN EMPTY PASSWORD MEANS LEAVE IT ALONE — it does not clear it.**
 *     `updates.password !== undefined && updates.password !== ''`. This is the
 *     dangerous one: the edit form renders an empty password box every time it
 *     opens, so it submits `''` on any save where the operator did not type a
 *     new one. A port treating `''` as a value would re-hash the empty string
 *     and lock the account out of its own password on every unrelated edit.
 *
 *  3. **`allowedRouterIds` IS ARRAY-GUARDED, NOT `!== undefined`.**
 *     `if (Array.isArray(...))` — a string, a number or null is IGNORED rather
 *     than stored or cleared. Different rule from every other field, in the same
 *     function, four lines apart.
 *
 *  4. **AN INVALID ROLE THROWS.** `_validRole` raises rather than falling back
 *     to a default, so the write does not happen at all and the caller answers
 *     400. A port that clamped to 'viewer' would silently demote somebody.
 *
 * ---- WHAT IS RECORDED, AND WHAT IS NOT ------------------------------------
 *
 * The salt is 32 random bytes, so the hash differs on every run and cannot be a
 * corpus value. What is recorded is STRUCTURAL: whether salt and passwordHash
 * CHANGED, which is exactly the property rules 2 and 4 turn on. The users are
 * synthetic and created by this file; no real credential is involved either way.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/userwrite-cases.js
 *   MIKRODASH_SRC=../MikroDash node tools/userwrite-cases.js --check
 */

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', 'userwrite-cases.json');

const DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'mduw-'));
process.env.DATA_DIR = DIR;
const Users = require(path.join(LIVE, 'src', 'users.js'));

const FILE = path.join(DIR, 'users.json');

// Two seeded accounts. Synthetic, and the hashes below are literals that were
// never derived from anything — this file never calls the hasher for the seed,
// only for the updates it drives.
const SEED = [
  {
    id: 'u-1',
    username: 'alice',
    role: 'admin',
    allowedRouterIds: [],
    salt: 'a'.repeat(64),
    passwordHash: 'b'.repeat(128),
    createdAt: 1700000000000,
  },
  {
    id: 'u-2',
    username: 'bob',
    role: 'viewer',
    allowedRouterIds: ['rtr-1'],
    salt: 'c'.repeat(64),
    passwordHash: 'd'.repeat(128),
    createdAt: 1700000001000,
  },
];

function seed() {
  fs.writeFileSync(FILE, JSON.stringify(SEED, null, 2));
  Users.invalidateCache();
}

function raw() {
  return JSON.parse(fs.readFileSync(FILE, 'utf8'));
}

async function update(id, updates) {
  seed();
  const before = raw().find((u) => u.id === id) || null;
  let returned = null;
  let threw = null;
  try {
    returned = await Users.updateUser(id, updates);
  } catch (e) {
    threw = String(e.message || e);
  }
  Users.invalidateCache();
  const after = raw().find((u) => u.id === id) || null;
  return {
    threw,
    // NULL is the "no such user" answer, and the caller turns it into a 404.
    returnedNull: !threw && returned === null,
    // The PUBLIC shape: `_toPublic` strips salt and passwordHash, and a port
    // leaking either into an HTTP response would be handing out the hash.
    returnedKeys: returned ? Object.keys(returned).sort() : null,
    username: after ? after.username : null,
    role: after ? after.role : null,
    allowedRouterIds: after ? after.allowedRouterIds : null,
    createdAt: after ? after.createdAt : null,
    // STRUCTURAL, not the value — see the header.
    saltChanged: !!(before && after) && before.salt !== after.salt,
    hashChanged: !!(before && after) && before.passwordHash !== after.passwordHash,
    // The OTHER record must be untouched, or an update is rewriting the file.
    otherIntact: JSON.stringify(raw().find((u) => u.id === (id === 'u-1' ? 'u-2' : 'u-1')))
      === JSON.stringify(SEED.find((u) => u.id === (id === 'u-1' ? 'u-2' : 'u-1'))),
    count: raw().length,
  };
}

async function remove(id) {
  seed();
  const ok = await Users.deleteUser(id);
  Users.invalidateCache();
  const after = raw();
  return {
    removed: ok,
    count: after.length,
    remaining: after.map((u) => u.id),
    // A bare JSON ARRAY, not an object. `internal/store`'s header calls this a
    // security property rather than a preference.
    isArray: Array.isArray(after),
  };
}

const UPDATES = [
  ['rename only', 'u-1', { username: 'alice2' }],
  ['rename with surrounding whitespace is trimmed', 'u-1', { username: '  alice2  ' }],
  ['role only', 'u-1', { role: 'operator' }],
  ['router ids only', 'u-1', { allowedRouterIds: ['rtr-1', 'rtr-2'] }],
  ['router ids emptied deliberately', 'u-2', { allowedRouterIds: [] }],
  ['everything at once', 'u-2', { username: 'bob2', role: 'admin', allowedRouterIds: ['rtr-9'] }],
  ['an empty updates object changes nothing', 'u-1', {}],
  // RULE 2 — the one that would lock an account out.
  ['an EMPTY password leaves the credential alone', 'u-1', { password: '' }],
  ['a real password re-hashes and re-salts', 'u-1', { password: 'a-new-password' }],
  ['a password alongside other fields', 'u-1', { username: 'alice3', password: 'another-one' }],
  // RULE 3 — array-guarded, unlike every other field.
  ['allowedRouterIds as a STRING is ignored', 'u-2', { allowedRouterIds: 'rtr-1' }],
  ['allowedRouterIds as null is ignored, NOT cleared', 'u-2', { allowedRouterIds: null }],
  ['allowedRouterIds as a number is ignored', 'u-2', { allowedRouterIds: 7 }],
  // RULE 4 — throws, does not clamp.
  ['an invalid role THROWS and writes nothing', 'u-1', { role: 'superuser' }],
  ['an empty role throws too', 'u-1', { role: '' }],
  ['a null role throws', 'u-1', { role: null }],
  ['role casing is not normalised — Admin is invalid', 'u-1', { role: 'Admin' }],
  // Each of the three valid roles, so the corpus cannot pass a port that only
  // knows one of them.
  ['role viewer', 'u-1', { role: 'viewer' }],
  ['role operator', 'u-1', { role: 'operator' }],
  ['role admin', 'u-2', { role: 'admin' }],
  // Not found.
  ['an unknown id returns null', 'u-nope', { username: 'x' }],
  // THE ORDER CASE. The lookup happens BEFORE the role is validated, so an
  // unknown id with an INVALID role answers "no such user" rather than throwing.
  // Without it the port validated first, which made the HTTP route's own role
  // check redundant — and deleting that check survived the whole suite.
  ['an unknown id with an INVALID role still returns null, not a throw', 'u-nope',
    { role: 'superuser' }],
  // A username that is not a string still goes through String().
  ['a numeric username is stringified', 'u-1', { username: 42 }],
];

const DELETES = [
  ['delete an existing user', 'u-1'],
  ['delete the other one', 'u-2'],
  ['delete an unknown id', 'u-nope'],
];

(async () => {
  const updates = [];
  for (const [why, id, patch] of UPDATES) {
    updates.push({ why, id, patch, ...(await update(id, patch)) });
  }
  const deletes = [];
  for (const [why, id] of DELETES) {
    deletes.push({ why, id, ...(await remove(id)) });
  }

  // ---- Believability -------------------------------------------------------
  const byWhy = Object.fromEntries(updates.map((c) => [c.why, c]));
  const need = (k) => {
    if (!byWhy[k]) throw new Error(`a believability check names a case that is gone: ${k}`);
    return byWhy[k];
  };

  if (!updates.some((c) => c.threw)) throw new Error('nothing throws; rule 4 is untested');
  if (!updates.some((c) => c.hashChanged)) throw new Error('no case re-hashes; rule 2 is untested');
  if (!updates.some((c) => !c.hashChanged && !c.threw)) {
    throw new Error('every case re-hashes, so "leave the credential alone" proves nothing');
  }

  // RULE 2, both directions. This is the case that would lock an account out.
  {
    const empty = need('an EMPTY password leaves the credential alone');
    if (empty.saltChanged || empty.hashChanged) {
      throw new Error('an empty password re-hashed. Either the live rule changed or this harness '
        + 'is not reaching it — and a port copying that would wipe a password on every save of '
        + 'the edit form, which renders its password box empty.');
    }
    const real = need('a real password re-hashes and re-salts');
    if (!real.saltChanged || !real.hashChanged) {
      throw new Error('a real password did NOT re-hash, so the pair of cases no longer separates '
        + 'the two branches');
    }
  }

  // RULE 3. The string case must leave the STORED list alone, not replace it.
  for (const why of ['allowedRouterIds as a STRING is ignored',
    'allowedRouterIds as null is ignored, NOT cleared',
    'allowedRouterIds as a number is ignored']) {
    const c = need(why);
    if (JSON.stringify(c.allowedRouterIds) !== JSON.stringify(['rtr-1'])) {
      throw new Error(`${why}: the stored list became ${JSON.stringify(c.allowedRouterIds)}`);
    }
  }
  // ...and the deliberate empty array MUST get through, or "ignored" is
  // indistinguishable from "nothing can change this field".
  if (JSON.stringify(need('router ids emptied deliberately').allowedRouterIds) !== '[]') {
    throw new Error('an explicit empty array did not clear the list, so the array guard is '
      + 'untested in the direction that matters');
  }

  // RULE 4 — throwing means NOTHING is written.
  for (const why of ['an invalid role THROWS and writes nothing', 'an empty role throws too',
    'a null role throws', 'role casing is not normalised — Admin is invalid']) {
    const c = need(why);
    if (!c.threw) throw new Error(`${why}: did not throw`);
    if (c.role !== 'admin' || c.username !== 'alice') {
      throw new Error(`${why}: threw but the record moved to role=${c.role} username=${c.username}`);
    }
  }

  // RULE 1 — an empty patch is a no-op on every field.
  {
    const c = need('an empty updates object changes nothing');
    if (c.username !== 'alice' || c.role !== 'admin' || c.saltChanged || c.hashChanged) {
      throw new Error('an empty patch moved something');
    }
  }

  // THE PUBLIC SHAPE must not carry the credential.
  {
    const keys = need('rename only').returnedKeys || [];
    for (const secret of ['salt', 'passwordHash']) {
      if (keys.includes(secret)) throw new Error(`updateUser returns ${secret} to its caller`);
    }
    if (!keys.includes('id') || !keys.includes('username') || !keys.includes('role')) {
      throw new Error(`the public shape lost a field it must keep: ${JSON.stringify(keys)}`);
    }
  }

  // `createdAt` SURVIVES. It is not in the update path at all, and a port
  // rebuilding the record from a struct would stamp it anew.
  if (need('everything at once').createdAt !== 1700000001000) {
    throw new Error('createdAt moved on an update');
  }

  // NEIGHBOURS ARE UNTOUCHED.
  for (const c of updates) {
    if (!c.threw && !c.returnedNull && !c.otherIntact) {
      throw new Error(`${c.why}: rewrote the OTHER user's record`);
    }
  }

  // DELETES.
  if (!deletes.some((d) => d.removed)) throw new Error('nothing is ever deleted');
  if (!deletes.some((d) => !d.removed)) throw new Error('the unknown-id case does not report false');
  for (const d of deletes) {
    if (!d.isArray) throw new Error(`${d.why}: users.json is no longer a bare array`);
  }

  fs.rmSync(DIR, { recursive: true, force: true });

  const json = JSON.stringify(
    { generated_from: 'src/users.js updateUser + deleteUser (executed)', seed: SEED, updates, deletes },
    null, 2) + '\n';
  if (process.argv.includes('--check')) {
    const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
    if (have !== json) {
      console.error('STALE: testdata/userwrite-cases.json - re-run tools/userwrite-cases.js');
      process.exit(1);
    }
    console.log(`userwrite-cases: up to date (${updates.length} updates, ${deletes.length} deletes)`);
  } else {
    fs.writeFileSync(OUT, json);
    console.log(`wrote ${OUT} (${updates.length} updates — ${updates.filter((c) => c.threw).length} `
      + `throwing, ${updates.filter((c) => c.hashChanged).length} re-hashing — `
      + `and ${deletes.length} deletes)`);
  }
})();
