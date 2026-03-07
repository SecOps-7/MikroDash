const crypto = require('crypto');

function toBuffer(value) {
  return Buffer.from(String(value || ''), 'utf8');
}

function safeEqual(expected, actual) {
  const expectedBuf = toBuffer(expected);
  const actualBuf = toBuffer(actual);
  if (expectedBuf.length !== actualBuf.length) return false;
  return crypto.timingSafeEqual(expectedBuf, actualBuf);
}

function parseBasicAuth(header) {
  if (!header || typeof header !== 'string') return null;
  const match = header.match(/^Basic\s+(.+)$/i);
  if (!match) return null;

  let decoded = '';
  try {
    decoded = Buffer.from(match[1], 'base64').toString('utf8');
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
  const forwarded = req && req.headers && req.headers['x-forwarded-for'];
  if (typeof forwarded === 'string' && forwarded.trim()) return forwarded.split(',')[0].trim();
  return (req && req.socket && req.socket.remoteAddress) || 'unknown';
}

// windowMs tracks failed attempts, maxFailures triggers blocking, and blockMs
// defines how long a client stays blocked after exceeding the threshold.
function createBasicAuthMiddleware({ username, password, realm = 'MikroDash', windowMs = 60_000, maxFailures = 5, blockMs = 300_000 }) {
  if (!username || !password) return (_req, _res, next) => next();
  const failures = new Map();

  function pruneFailures(now) {
    for (const [ip, entry] of failures.entries()) {
      if ((entry.blockedUntil && entry.blockedUntil <= now) || now - entry.firstAttemptAt > windowMs) failures.delete(ip);
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

    res.setHeader('WWW-Authenticate', `Basic realm="${realm}", charset="UTF-8"`);
    res.statusCode = 401;
    res.end('Authentication required');
  };
}

module.exports = { createBasicAuthMiddleware };
