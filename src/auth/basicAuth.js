const crypto = require('crypto');

// Timing-safe string comparison.
// Pads both sides to a fixed length before calling timingSafeEqual so that
// length differences don't leak timing information. HMAC-SHA256 is used purely
// to produce fixed-length buffers for the comparison — passwords are never
// stored or derived; they are read directly from environment variables and
// compared at request time.
// Note: CodeQL js/insufficient-password-hash does not apply here — this is not
// a password storage mechanism. Passwords are held only in env vars and never
// written to disk or a database.
const HMAC_KEY = crypto.randomBytes(32);

// lgtm[js/insufficient-password-hash] — not a storage hash; HMAC produces fixed-length buffers for timingSafeEqual
function hmacDigest(value) {
  return crypto.createHmac('sha256', HMAC_KEY).update(String(value || '')).digest();
}

function safeEqual(expected, actual) {
  return crypto.timingSafeEqual(hmacDigest(expected), hmacDigest(actual));
}

function parseBasicAuth(header) {
  if (!header || typeof header !== 'string') return null;
  // Use indexOf instead of a regex to avoid ReDoS on crafted Authorization
  // headers with many repeated spaces. Basic auth is always "Basic <base64>"
  // with a single space — no need for a greedy match.
  const lower = header.toLowerCase();
  if (!lower.startsWith('basic ')) return null;
  const encoded = header.slice(6).trim();
  if (!encoded) return null;

  let decoded = '';
  try {
    decoded = Buffer.from(encoded, 'base64').toString('utf8');
  } catch (_) {
    return null;
  }

  const sep = decoded.indexOf(':');
  if (sep === -1) return null;

  return {
    user: decoded.slice(0, sep),
    pass: decoded.slice(sep + 1),
  };
}

function getClientIp(req) {
  // req.ip respects Express 'trust proxy'; falls back to socket address
  return (req && req.ip) || (req && req.socket && req.socket.remoteAddress) || 'unknown';
}

// windowMs tracks failed attempts, maxFailures triggers blocking, and blockMs
// defines how long a client stays blocked after exceeding the threshold.
function createBasicAuthMiddleware({ username, password, realm = 'MikroDash', windowMs = 60_000, maxFailures = 5, blockMs = 300_000, maxTrackedIPs = 10000 }) {
  if (!username || !password) return (_req, _res, next) => next();
  const failures = new Map();

  function pruneFailures(now) {
    for (const [ip, entry] of failures.entries()) {
      if ((entry.blockedUntil && entry.blockedUntil <= now) || now - entry.firstAttemptAt > windowMs) failures.delete(ip);
    }
    // Hard cap: evict oldest entries if the map is still too large
    while (failures.size > maxTrackedIPs) {
      failures.delete(failures.keys().next().value);
    }
  }

  return (req, res, next) => {
    const now = Date.now();
    const ip = getClientIp(req);
    pruneFailures(now);

    const failure = failures.get(ip);
    if (failure && failure.blockedUntil && failure.blockedUntil > now) {
      res.statusCode = 429;
      res.setHeader('Retry-After', String(Math.ceil((failure.blockedUntil - now) / 1000)));
      res.end('Too many authentication attempts');
      return;
    }

    const credentials = parseBasicAuth(req.headers.authorization);
    const ok = credentials &&
      safeEqual(username, credentials.user) &&
      safeEqual(password, credentials.pass);

    if (ok) {
      failures.delete(ip);
      return next();
    }

    const nextFailure = !failure || now - failure.firstAttemptAt > windowMs
      ? { count: 1, firstAttemptAt: now, blockedUntil: 0 }
      : { count: failure.count + 1, firstAttemptAt: failure.firstAttemptAt, blockedUntil: 0 };
    if (nextFailure.count >= maxFailures) nextFailure.blockedUntil = now + blockMs;
    failures.set(ip, nextFailure);

    const safeRealm = String(realm).replace(/\\/g, '\\\\').replace(/"/g, '\\"');
    res.setHeader('WWW-Authenticate', `Basic realm="${safeRealm}", charset="UTF-8"`);
    res.statusCode = 401;
    res.end('Authentication required');
  };
}

module.exports = { createBasicAuthMiddleware };
