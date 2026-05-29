'use strict';
const crypto = require('crypto');

// In-memory session store. Sessions are intentionally lost on container
// restart — the login page will re-prompt. TTL=0 means never expires.

const _sessions = new Map(); // token → { userId, username, role, createdAt, expiresAt }

let _pruneTimer = null;

function _now() { return Date.now(); }

// ── Internal ──────────────────────────────────────────────────────────────────

function _isExpired(session) {
  return session.expiresAt !== Infinity && _now() > session.expiresAt;
}

// ── Public API ────────────────────────────────────────────────────────────────

/**
 * Create a new session. Returns { token, expiresAt }.
 * timeoutMs=0 means never expires (expiresAt=Infinity).
 */
function createSession(userId, username, role, timeoutMs, allowedRouterIds) {
  const token     = crypto.randomBytes(32).toString('hex');
  const createdAt = _now();
  const expiresAt = (timeoutMs && timeoutMs > 0)
    ? createdAt + timeoutMs
    : Infinity;

  _sessions.set(token, {
    userId, username, role, createdAt, expiresAt,
    allowedRouterIds: Array.isArray(allowedRouterIds) ? allowedRouterIds : [],
  });
  return { token, expiresAt };
}

/**
 * Retrieve a session by token. Returns null if not found or expired.
 * Expired sessions are lazily removed on access.
 */
function getSession(token) {
  if (!token) return null;
  const session = _sessions.get(token);
  if (!session) return null;
  if (_isExpired(session)) {
    _sessions.delete(token);
    return null;
  }
  return session;
}

/** Remove a session (logout). */
function deleteSession(token) {
  _sessions.delete(token);
}

/**
 * Merge extra fields into an existing session (e.g. activeRouterId after a router switch).
 * Does nothing if the token is unknown or expired.
 */
function updateSession(token, fields) {
  const session = getSession(token);
  if (!session) return;
  Object.assign(session, fields);
}

/** Remove all expired sessions from the Map. */
function pruneExpiredSessions() {
  for (const [token, session] of _sessions) {
    if (_isExpired(session)) _sessions.delete(token);
  }
}

/**
 * Parse a raw Cookie header string and return an object of name→value pairs.
 * Only splits on the first '=' so values containing '=' are handled correctly.
 */
function parseCookieHeader(cookieHeader) {
  if (!cookieHeader || typeof cookieHeader !== 'string') return {};
  const result = {};
  for (const part of cookieHeader.split(';')) {
    const eq = part.indexOf('=');
    if (eq === -1) continue;
    const name  = part.slice(0, eq).trim();
    const value = part.slice(eq + 1).trim();
    if (name) result[name] = value;
  }
  return result;
}

/**
 * Build a Set-Cookie header value for the session token.
 * Caller must also set the header on the response.
 */
function buildCookieHeader(token, expiresAt) {
  const secure = process.env.FORCE_HTTPS === 'true' ? '; Secure' : '';
  let maxAge   = '';
  if (expiresAt !== Infinity) {
    const seconds = Math.max(1, Math.round((expiresAt - _now()) / 1000));
    maxAge = `; Max-Age=${seconds}`;
  }
  return `mikrodash_sid=${token}; HttpOnly; SameSite=Strict; Path=/${maxAge}${secure}`;
}

/** Clear the session cookie (used on logout). */
function clearCookieHeader() {
  const secure = process.env.FORCE_HTTPS === 'true' ? '; Secure' : '';
  return `mikrodash_sid=; HttpOnly; SameSite=Strict; Path=/; Max-Age=0${secure}`;
}

/** Active session count (for diagnostics). */
function getSessionCount() {
  return _sessions.size;
}

/** Start the background prune interval. Called once at server startup. */
function startPruneInterval() {
  if (_pruneTimer) return;
  _pruneTimer = setInterval(pruneExpiredSessions, 5 * 60 * 1000);
  if (_pruneTimer.unref) _pruneTimer.unref(); // don't keep process alive
}

/** Clear the prune interval on shutdown. */
function shutdown() {
  if (_pruneTimer) { clearInterval(_pruneTimer); _pruneTimer = null; }
}

module.exports = {
  createSession, getSession, updateSession, deleteSession,
  pruneExpiredSessions, parseCookieHeader,
  buildCookieHeader, clearCookieHeader,
  getSessionCount, startPruneInterval, shutdown,
};
