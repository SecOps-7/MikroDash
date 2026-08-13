'use strict';
const fs     = require('fs');
const path   = require('path');
const crypto = require('crypto');

const DATA_DIR   = process.env.DATA_DIR || '/data';
const USERS_FILE = path.join(DATA_DIR, 'users.json');

// scrypt parameters — production-safe, faster than settings.js key derivation
// (key derivation runs once at startup; password hashing runs per login)
const SCRYPT_PARAMS = { N: 16384, r: 8, p: 1 };
const HASH_LEN = 64;

function _uuid() {
  return crypto.randomUUID ? crypto.randomUUID() : crypto.randomBytes(16).toString('hex');
}

function _ensureDataDir() {
  try { fs.mkdirSync(DATA_DIR, { recursive: true }); } catch (_) {}
}

// ── File I/O ──────────────────────────────────────────────────────────────────

let _cache = null;

function _readFile() {
  _ensureDataDir();
  try {
    const raw = JSON.parse(fs.readFileSync(USERS_FILE, 'utf8'));
    return Array.isArray(raw) ? raw : [];
  } catch (_) {
    return [];
  }
}

function _writeFile(users) {
  _ensureDataDir();
  const tmp = USERS_FILE + '.tmp';
  // mode 0o600 — file holds scrypt password hashes + salts; keep it owner-only.
  fs.writeFileSync(tmp, JSON.stringify(users, null, 2), { encoding: 'utf8', mode: 0o600 });
  fs.renameSync(tmp, USERS_FILE);
}

function _load() {
  if (_cache) return _cache;
  _cache = _readFile();
  return _cache;
}

// Strip sensitive fields before returning to callers
function _toPublic(user) {
  const { passwordHash: _h, salt: _s, ...pub } = user;
  return pub;
}

// ── Password helpers ──────────────────────────────────────────────────────────

function _hashPassword(password, salt) {
  return new Promise((resolve, reject) => {
    crypto.scrypt(password, salt, HASH_LEN, SCRYPT_PARAMS, (err, derived) => {
      if (err) return reject(err);
      resolve(derived.toString('hex'));
    });
  });
}

// Roles, in ascending privilege. Single source of truth — the HTTP layer
// validates against this rather than repeating the list.
//
// Validated, never coerced. Both call sites below used to read
// `role === 'viewer' ? 'viewer' : 'admin'`, which silently turned ANY
// unrecognised value into an administrator: a typo, a stale client, or a role
// added later and not yet wired up here. That is the wrong direction to fail
// in, and it is why adding a third role has to start here — `role: 'operator'`
// would otherwise have quietly created an admin.
// Ascending privilege. 'operator' sits between read-only and administrator: it
// can acknowledge alerts, read history and run diagnostics, and it is the tier
// that will receive RouterOS write actions when issue #97 lands.
//
// This could only be widened safely after the coercion above became validation:
// under the old `role === 'viewer' ? 'viewer' : 'admin'`, adding 'operator' here
// would have silently created administrators.
const ROLES = ['viewer', 'operator', 'admin'];

function _validRole(role) {
  if (!ROLES.includes(role)) {
    throw new Error('Invalid role: ' + String(role) + ' (expected one of ' + ROLES.join(', ') + ')');
  }
  return role;
}

// ── Public API (all async except userCount) ───────────────────────────────────

async function createUser({ username, password, role, allowedRouterIds }) {
  const salt         = crypto.randomBytes(32).toString('hex');
  const passwordHash = await _hashPassword(password, salt);
  const user = {
    id:               _uuid(),
    username:         String(username).trim(),
    passwordHash,
    salt,
    role:             _validRole(role),
    allowedRouterIds: Array.isArray(allowedRouterIds) ? allowedRouterIds : [],
    createdAt:        Date.now(),
  };
  const users = _load();
  users.push(user);
  _cache = users;
  _writeFile(users);
  return _toPublic(user);
}

async function getUser(id) {
  const user = _load().find(u => u.id === id) || null;
  return user ? { ...user } : null; // return raw (with hash) for internal use
}

// Synchronous lookup for hot auth paths (middleware / socket revalidation).
// Returns the public view (no hash/salt) or null. Reads the in-memory cache.
function getUserSync(id) {
  const user = _load().find(u => u.id === id) || null;
  return user ? _toPublic(user) : null;
}

async function getUserByUsername(username) {
  const user = _load().find(u => u.username === String(username).trim()) || null;
  return user ? { ...user } : null; // return raw for verifyPassword
}

async function updateUser(id, updates) {
  const users = _load();
  const idx   = users.findIndex(u => u.id === id);
  if (idx === -1) return null;

  const existing = users[idx];
  const updated  = { ...existing };

  if (updates.username !== undefined) updated.username = String(updates.username).trim();
  if (updates.role     !== undefined) updated.role = _validRole(updates.role);
  if (Array.isArray(updates.allowedRouterIds)) updated.allowedRouterIds = updates.allowedRouterIds;

  if (updates.password !== undefined && updates.password !== '') {
    const salt         = crypto.randomBytes(32).toString('hex');
    const passwordHash = await _hashPassword(updates.password, salt);
    updated.salt         = salt;
    updated.passwordHash = passwordHash;
  }

  users[idx] = updated;
  _cache = users;
  _writeFile(users);
  return _toPublic(updated);
}

async function deleteUser(id) {
  const users = _load();
  const next  = users.filter(u => u.id !== id);
  if (next.length === users.length) return false;
  _cache = next;
  _writeFile(next);
  return true;
}

// Synchronous sibling of listUsers(), for callers that cannot await: the RBAC
// migration runs during startup before the server listens, and the orphan sweep
// runs inside a SQLite transaction, where a Promise would be worse than useless.
// _load() is already synchronous — the async on listUsers is a convention of
// this module, not a real I/O boundary. Same precedent as getUserSync().
function listUsersSync() {
  return _load().map(_toPublic);
}

async function listUsers() {
  return _load().map(_toPublic);
}

// Fixed dummy salt for the no-such-user path. Hashing against it costs the same
// scrypt work as a real verification, so login timing does not reveal whether a
// username exists (username enumeration oracle).
const _DUMMY_SALT = 'a'.repeat(64);

async function verifyPassword(user, candidatePassword) {
  if (!user || !user.passwordHash || !user.salt) {
    // Spend the same scrypt work as a real check, then fail — constant-time vs. a
    // missing user. Result is intentionally discarded.
    try { await _hashPassword(String(candidatePassword), _DUMMY_SALT); } catch (_) {}
    return false;
  }
  try {
    const candidateHash = await _hashPassword(candidatePassword, user.salt);
    const a = Buffer.from(user.passwordHash, 'hex');
    const b = Buffer.from(candidateHash,     'hex');
    if (a.length !== b.length) return false;
    return crypto.timingSafeEqual(a, b);
  } catch (_) {
    return false;
  }
}

function userCount() {
  return _load().length;
}

// adminCount() lived here: a count of user records carrying role === 'admin',
// used to block removing the last one. It was deleted with issue #108, not
// merely left unused — administration is a grant now, and a record count cannot
// see an administrator whose grant is held through a group, nor notice one
// demoted in the grant editor. It would answer confidently and wrongly.
// Rbac.wouldOrphanGlobalAdmin() is the question to ask instead.

function invalidateCache() { _cache = null; }

module.exports = {
  createUser, getUser, getUserSync, getUserByUsername,
  updateUser, deleteUser, listUsers, listUsersSync,
  verifyPassword, userCount, invalidateCache,
  ROLES,
};
