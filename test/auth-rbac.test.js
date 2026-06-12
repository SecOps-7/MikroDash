'use strict';
const { describe, test, before, after, beforeEach } = require('node:test');
const assert = require('node:assert/strict');
const os     = require('os');
const fs     = require('fs');
const path   = require('path');

// ── Test helpers ──────────────────────────────────────────────────────────────

function makeTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-test-'));
}

// ═══════════════════════════════════════════════════════════════════════════════
// Area 1 — Session Store (src/auth/sessionStore.js)
// ═══════════════════════════════════════════════════════════════════════════════

const SessionStore = require('../src/auth/sessionStore');

describe('sessionStore', () => {
  after(() => SessionStore.shutdown());

  test('createSession returns a 64-char hex token', () => {
    const { token } = SessionStore.createSession('u1', 'alice', 'admin', 3600_000);
    assert.ok(typeof token === 'string', 'token is string');
    assert.equal(token.length, 64, 'token is 64 hex chars');
    assert.ok(/^[0-9a-f]+$/.test(token), 'token is lowercase hex');
    SessionStore.deleteSession(token);
  });

  test('createSession sets correct expiresAt for positive timeout', () => {
    const before = Date.now();
    const { token, expiresAt } = SessionStore.createSession('u1', 'alice', 'admin', 3600_000);
    assert.ok(expiresAt > before + 3_590_000, 'expiresAt is ~1h in the future');
    SessionStore.deleteSession(token);
  });

  test('createSession with timeoutMs=0 sets expiresAt=Infinity (never expires)', () => {
    const { token, expiresAt } = SessionStore.createSession('u1', 'alice', 'admin', 0);
    assert.equal(expiresAt, Infinity);
    const session = SessionStore.getSession(token);
    assert.ok(session !== null, 'session should be valid');
    SessionStore.deleteSession(token);
  });

  test('getSession returns null for unknown token', () => {
    assert.equal(SessionStore.getSession('nonexistent_token_abc123'), null);
  });

  test('getSession returns null for empty/falsy token', () => {
    assert.equal(SessionStore.getSession(''), null);
    assert.equal(SessionStore.getSession(null), null);
    assert.equal(SessionStore.getSession(undefined), null);
  });

  test('getSession returns null for expired token (1ms TTL)', async () => {
    const { token } = SessionStore.createSession('u1', 'alice', 'admin', 1);
    await new Promise(r => setTimeout(r, 10));
    assert.equal(SessionStore.getSession(token), null);
  });

  test('getSession returns session object for valid token', () => {
    const { token } = SessionStore.createSession('u2', 'bob', 'viewer', 3600_000);
    const session = SessionStore.getSession(token);
    assert.ok(session !== null);
    assert.equal(session.username, 'bob');
    assert.equal(session.role, 'viewer');
    assert.equal(session.userId, 'u2');
    SessionStore.deleteSession(token);
  });

  test('createSession stores allowedRouterIds in session', () => {
    const { token } = SessionStore.createSession('u2', 'bob', 'viewer', 3600_000, ['router-a', 'router-b']);
    const session = SessionStore.getSession(token);
    assert.deepEqual(session.allowedRouterIds, ['router-a', 'router-b']);
    SessionStore.deleteSession(token);
  });

  test('createSession stores empty allowedRouterIds when omitted', () => {
    const { token } = SessionStore.createSession('u2', 'bob', 'admin', 3600_000);
    const session = SessionStore.getSession(token);
    assert.deepEqual(session.allowedRouterIds, []);
    SessionStore.deleteSession(token);
  });

  test('deleteSession removes session; subsequent getSession returns null', () => {
    const { token } = SessionStore.createSession('u3', 'carol', 'admin', 3600_000);
    SessionStore.deleteSession(token);
    assert.equal(SessionStore.getSession(token), null);
  });

  test('pruneExpiredSessions removes expired entries, keeps valid ones', async () => {
    const { token: expired } = SessionStore.createSession('u4', 'dave', 'viewer', 1);
    const { token: valid   } = SessionStore.createSession('u5', 'eve',  'admin',  3600_000);
    await new Promise(r => setTimeout(r, 10));
    SessionStore.pruneExpiredSessions();
    assert.equal(SessionStore.getSession(expired), null, 'expired should be gone');
    assert.ok(SessionStore.getSession(valid) !== null, 'valid should still exist');
    SessionStore.deleteSession(valid);
  });

  describe('parseCookieHeader', () => {
    test('parses a single cookie', () => {
      const result = SessionStore.parseCookieHeader('mikrodash_sid=abc123');
      assert.equal(result['mikrodash_sid'], 'abc123');
    });

    test('parses multiple cookies and returns correct mikrodash_sid', () => {
      const result = SessionStore.parseCookieHeader('foo=bar; mikrodash_sid=token42; baz=qux');
      assert.equal(result['mikrodash_sid'], 'token42');
      assert.equal(result['foo'], 'bar');
    });

    test('returns empty object when header is empty string', () => {
      assert.deepEqual(SessionStore.parseCookieHeader(''), {});
    });

    test('returns empty object on null/undefined without throwing', () => {
      assert.deepEqual(SessionStore.parseCookieHeader(null), {});
      assert.deepEqual(SessionStore.parseCookieHeader(undefined), {});
    });

    test('handles cookie value containing = correctly', () => {
      const result = SessionStore.parseCookieHeader('data=a=b=c');
      assert.equal(result['data'], 'a=b=c');
    });

    test('returns empty object when mikrodash_sid is absent', () => {
      const result = SessionStore.parseCookieHeader('other=value');
      assert.equal(result['mikrodash_sid'], undefined);
    });
  });
});

// ═══════════════════════════════════════════════════════════════════════════════
// Area 2 — User Store (src/users.js)
// ═══════════════════════════════════════════════════════════════════════════════

describe('users', () => {
  let tmpDir;
  let Users;

  before(() => {
    tmpDir = makeTmpDir();
    process.env.DATA_DIR = tmpDir;
    // require after setting DATA_DIR so the module picks up the test path
    Users = require('../src/users');
  });

  beforeEach(() => {
    // Remove users.json and clear module cache between tests for isolation
    try { fs.unlinkSync(path.join(tmpDir, 'users.json')); } catch (_) {}
    Users.invalidateCache();
  });

  after(() => {
    try { fs.rmSync(tmpDir, { recursive: true, force: true }); } catch (_) {}
    delete process.env.DATA_DIR;
  });

  test('createUser returns user without passwordHash or salt', async () => {
    const user = await Users.createUser({ username: 'alice', password: 'secret', role: 'admin' });
    assert.equal(user.username, 'alice');
    assert.equal(user.role, 'admin');
    assert.ok(user.id, 'id is set');
    assert.ok(user.createdAt > 0, 'createdAt is set');
    assert.equal(user.passwordHash, undefined, 'no passwordHash in public output');
    assert.equal(user.salt, undefined, 'no salt in public output');
  });

  test('createUser defaults non-viewer roles to admin', async () => {
    const user = await Users.createUser({ username: 'bob', password: 'pw', role: 'superuser' });
    assert.equal(user.role, 'admin');
  });

  test('createUser with viewer role stores viewer', async () => {
    const user = await Users.createUser({ username: 'carol', password: 'pw', role: 'viewer' });
    assert.equal(user.role, 'viewer');
  });

  test('listUsers returns created users without hash/salt', async () => {
    await Users.createUser({ username: 'alice', password: 's1', role: 'admin' });
    await Users.createUser({ username: 'bob',   password: 's2', role: 'viewer' });
    const list = await Users.listUsers();
    assert.equal(list.length, 2);
    for (const u of list) {
      assert.equal(u.passwordHash, undefined, 'no passwordHash');
      assert.equal(u.salt, undefined, 'no salt');
    }
  });

  test('getUserByUsername returns full user (with hash) for internal use', async () => {
    await Users.createUser({ username: 'alice', password: 'secret', role: 'admin' });
    const user = await Users.getUserByUsername('alice');
    assert.ok(user !== null);
    assert.equal(user.username, 'alice');
    assert.ok(user.passwordHash, 'passwordHash present for verifyPassword');
    assert.ok(user.salt, 'salt present for verifyPassword');
  });

  test('getUserByUsername returns null for unknown username', async () => {
    const user = await Users.getUserByUsername('nobody');
    assert.equal(user, null);
  });

  test('verifyPassword returns true for correct password', async () => {
    await Users.createUser({ username: 'alice', password: 'correct', role: 'admin' });
    const user = await Users.getUserByUsername('alice');
    const ok   = await Users.verifyPassword(user, 'correct');
    assert.equal(ok, true);
  });

  test('verifyPassword returns false for wrong password', async () => {
    await Users.createUser({ username: 'alice', password: 'correct', role: 'admin' });
    const user = await Users.getUserByUsername('alice');
    const ok   = await Users.verifyPassword(user, 'wrong');
    assert.equal(ok, false);
  });

  test('verifyPassword returns false for null user', async () => {
    const ok = await Users.verifyPassword(null, 'anything');
    assert.equal(ok, false);
  });

  test('deleteUser removes user; getUser returns null', async () => {
    const user = await Users.createUser({ username: 'alice', password: 'pw', role: 'admin' });
    const deleted = await Users.deleteUser(user.id);
    assert.equal(deleted, true);
    const fetched = await Users.getUser(user.id);
    assert.equal(fetched, null);
  });

  test('deleteUser returns false for unknown id', async () => {
    const result = await Users.deleteUser('nonexistent-id');
    assert.equal(result, false);
  });

  test('updateUser changes username and role', async () => {
    const user    = await Users.createUser({ username: 'alice', password: 'pw', role: 'admin' });
    const updated = await Users.updateUser(user.id, { username: 'alicia', role: 'viewer' });
    assert.equal(updated.username, 'alicia');
    assert.equal(updated.role, 'viewer');
    assert.equal(updated.passwordHash, undefined, 'no hash in public output');
  });

  test('updateUser changes password; new password verifies correctly', async () => {
    const user = await Users.createUser({ username: 'alice', password: 'old', role: 'admin' });
    await Users.updateUser(user.id, { password: 'new' });
    const fresh = await Users.getUserByUsername('alice');
    assert.equal(await Users.verifyPassword(fresh, 'new'),  true,  'new password works');
    assert.equal(await Users.verifyPassword(fresh, 'old'),  false, 'old password rejected');
  });

  test('updateUser returns null for unknown id', async () => {
    const result = await Users.updateUser('nonexistent', { username: 'x' });
    assert.equal(result, null);
  });

  test('userCount returns 0 on empty store', () => {
    assert.equal(Users.userCount(), 0);
  });

  test('userCount returns correct count after creates', async () => {
    await Users.createUser({ username: 'a', password: 'p', role: 'admin' });
    await Users.createUser({ username: 'b', password: 'p', role: 'viewer' });
    assert.equal(Users.userCount(), 2);
  });

  test('user data persists to disk and survives cache invalidation', async () => {
    await Users.createUser({ username: 'persistent', password: 'pw', role: 'admin' });
    Users.invalidateCache(); // force re-read from disk
    const list = await Users.listUsers();
    assert.equal(list.length, 1);
    assert.equal(list[0].username, 'persistent');
  });

  test('allowedRouterIds defaults to empty array', async () => {
    const user = await Users.createUser({ username: 'alice', password: 'pw', role: 'admin' });
    assert.deepEqual(user.allowedRouterIds, []);
  });

  test('updateUser sets allowedRouterIds', async () => {
    const user    = await Users.createUser({ username: 'alice', password: 'pw', role: 'viewer' });
    const updated = await Users.updateUser(user.id, { allowedRouterIds: ['router-1', 'router-2'] });
    assert.deepEqual(updated.allowedRouterIds, ['router-1', 'router-2']);
  });

  // ── Security hardening: getUserSync (#1 live re-read) ───────────────────────
  test('getUserSync returns public view (no hash/salt) for known id', async () => {
    const created = await Users.createUser({ username: 'alice', password: 'pw', role: 'viewer', allowedRouterIds: ['r1'] });
    const u = Users.getUserSync(created.id);
    assert.ok(u, 'user found');
    assert.equal(u.role, 'viewer');
    assert.deepEqual(u.allowedRouterIds, ['r1']);
    assert.equal(u.passwordHash, undefined, 'no hash leaked');
    assert.equal(u.salt, undefined, 'no salt leaked');
  });

  test('getUserSync reflects role change immediately (no stale snapshot)', async () => {
    const created = await Users.createUser({ username: 'alice', password: 'pw', role: 'viewer' });
    await Users.updateUser(created.id, { role: 'admin' });
    assert.equal(Users.getUserSync(created.id).role, 'admin');
  });

  test('getUserSync returns null after the user is deleted', async () => {
    const created = await Users.createUser({ username: 'alice', password: 'pw', role: 'admin' });
    await Users.deleteUser(created.id);
    assert.equal(Users.getUserSync(created.id), null);
  });

  // ── Security hardening: adminCount (#8 last-admin guard) ────────────────────
  test('adminCount counts only admin-role users', async () => {
    assert.equal(Users.adminCount(), 0);
    await Users.createUser({ username: 'a', password: 'p', role: 'admin' });
    await Users.createUser({ username: 'b', password: 'p', role: 'viewer' });
    await Users.createUser({ username: 'c', password: 'p', role: 'admin' });
    assert.equal(Users.adminCount(), 2);
  });

  test('adminCount drops when an admin is demoted', async () => {
    const a = await Users.createUser({ username: 'a', password: 'p', role: 'admin' });
    await Users.createUser({ username: 'b', password: 'p', role: 'admin' });
    assert.equal(Users.adminCount(), 2);
    await Users.updateUser(a.id, { role: 'viewer' });
    assert.equal(Users.adminCount(), 1);
  });

  // ── Security hardening: verifyPassword constant-time path (#5) ───────────────
  test('verifyPassword returns false for missing user (and still spends scrypt work)', async () => {
    // null user must NOT short-circuit cheaply — it runs a dummy hash then returns false.
    const ok = await Users.verifyPassword(null, 'anything');
    assert.equal(ok, false);
  });

  test('verifyPassword returns false for a user object missing hash/salt', async () => {
    const ok = await Users.verifyPassword({ username: 'x' }, 'anything');
    assert.equal(ok, false);
  });
});

// ═══════════════════════════════════════════════════════════════════════════════
// Area 3 — Settings viewer subset (src/settings.js getViewerPublic, fix #4)
// ═══════════════════════════════════════════════════════════════════════════════

describe('settings.getViewerPublic', () => {
  let tmpDir;
  let Settings;

  before(() => {
    tmpDir = makeTmpDir();
    process.env.DATA_DIR = tmpDir;
    Settings = require('../src/settings');
  });

  after(() => {
    try { fs.rmSync(tmpDir, { recursive: true, force: true }); } catch (_) {}
    delete process.env.DATA_DIR;
  });

  test('omits credential fields entirely (not even masked)', () => {
    const v = Settings.getViewerPublic();
    for (const f of ['routerPass', 'dashPass', 'telegramBotToken', 'pushbulletApiKey', 'smtpUser', 'smtpPass', 'ntfyToken']) {
      assert.equal(f in v, false, `${f} must be absent from viewer subset`);
    }
  });

  test('omits sensitive non-credential config (router host, smtp/ntfy targets, chat ids)', () => {
    const v = Settings.getViewerPublic();
    for (const f of ['routerHost', 'routerUser', 'smtpHost', 'smtpTo', 'ntfyUrl', 'telegramChatId', 'dashUser', 'sessionTimeoutMs']) {
      assert.equal(f in v, false, `${f} must not be exposed to viewers`);
    }
  });

  test('includes dashboard-rendering fields a viewer needs', () => {
    const v = Settings.getViewerPublic();
    for (const f of ['pageWireless', 'pageVpn', 'displayTimezone', 'alertCpuThreshold', 'streamPing', 'activeRouterId']) {
      assert.equal(f in v, true, `${f} should be present for the viewer dashboard`);
    }
  });

  test('getPublic (admin view) still masks credentials but includes them', () => {
    const p = Settings.getPublic();
    assert.equal('routerPass' in p, true, 'admin view includes credential keys (masked)');
    assert.equal('sessionTimeoutMs' in p, true, 'admin view includes admin-only config');
  });
});

// ═══════════════════════════════════════════════════════════════════════════════
// Area 4 — Session cookie helpers (src/auth/sessionStore.js)
// ═══════════════════════════════════════════════════════════════════════════════

describe('sessionStore.buildCookieHeader', () => {
  test('includes token value, HttpOnly, SameSite=Strict, and Path=/', () => {
    const { token, expiresAt } = SessionStore.createSession('u1', 'alice', 'admin', 3600_000);
    const header = SessionStore.buildCookieHeader(token, expiresAt);
    assert.ok(header.startsWith(`mikrodash_sid=${token}`), 'token value present');
    assert.ok(header.includes('HttpOnly'), 'HttpOnly flag present');
    assert.ok(header.includes('SameSite=Strict'), 'SameSite=Strict present');
    assert.ok(header.includes('Path=/'), 'Path=/ present');
    SessionStore.deleteSession(token);
  });

  test('includes positive Max-Age when expiresAt is finite', () => {
    const expiresAt = Date.now() + 3600_000;
    const header = SessionStore.buildCookieHeader('testtoken', expiresAt);
    const match = header.match(/Max-Age=(\d+)/);
    assert.ok(match, 'Max-Age must be present for a finite session');
    assert.ok(Number(match[1]) > 0, 'Max-Age must be a positive integer');
  });

  test('omits Max-Age when expiresAt is Infinity (never-expires session)', () => {
    const header = SessionStore.buildCookieHeader('testtoken', Infinity);
    assert.ok(!header.includes('Max-Age='), 'Max-Age must not appear for Infinity session');
  });
});

describe('sessionStore.clearCookieHeader', () => {
  test('sets Max-Age=0 to expire the cookie and preserves security flags', () => {
    const header = SessionStore.clearCookieHeader();
    assert.ok(header.includes('mikrodash_sid=;'), 'token value must be empty');
    assert.ok(header.includes('Max-Age=0'), 'Max-Age=0 must be set to expire the cookie');
    assert.ok(header.includes('HttpOnly'), 'HttpOnly must be preserved on the clear header');
    assert.ok(header.includes('SameSite=Strict'), 'SameSite=Strict must be preserved');
  });
});

describe('sessionStore.updateSession', () => {
  test('merges new fields into an existing session without overwriting others', () => {
    const { token } = SessionStore.createSession('u1', 'alice', 'admin', 3600_000);
    SessionStore.updateSession(token, { activeRouterId: 'router-99' });
    const session = SessionStore.getSession(token);
    assert.equal(session.activeRouterId, 'router-99', 'new field is merged');
    assert.equal(session.username, 'alice', 'pre-existing fields are preserved');
    assert.equal(session.role, 'admin', 'role not overwritten');
    SessionStore.deleteSession(token);
  });

  test('does not throw for an unknown token', () => {
    assert.doesNotThrow(() => SessionStore.updateSession('nonexistent-token-xyz', { foo: 'bar' }));
  });

  test('does not throw for an expired token', async () => {
    const { token } = SessionStore.createSession('u1', 'alice', 'admin', 1);
    await new Promise(r => setTimeout(r, 10));
    assert.doesNotThrow(() => SessionStore.updateSession(token, { foo: 'bar' }));
  });
});

describe('sessionStore.getSessionCount', () => {
  test('increases by 1 after createSession and decreases by 1 after deleteSession', () => {
    const before = SessionStore.getSessionCount();
    const { token } = SessionStore.createSession('u_count', 'x', 'viewer', 3600_000);
    assert.equal(SessionStore.getSessionCount(), before + 1, 'count must increase after createSession');
    SessionStore.deleteSession(token);
    assert.equal(SessionStore.getSessionCount(), before, 'count must decrease after deleteSession');
  });
});
