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

function createBasicAuthMiddleware({ username, password, realm = 'MikroDash' }) {
  if (!username || !password) return (_req, _res, next) => next();

  return (req, res, next) => {
    const credentials = parseBasicAuth(req.headers.authorization);
    const ok = credentials &&
      safeEqual(username, credentials.user) &&
      safeEqual(password, credentials.pass);

    if (ok) return next();

    res.setHeader('WWW-Authenticate', `Basic realm="${realm}", charset="UTF-8"`);
    res.statusCode = 401;
    res.end('Authentication required');
  };
}

module.exports = { createBasicAuthMiddleware };
