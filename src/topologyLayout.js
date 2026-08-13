'use strict';
// Validation for saved Topology map node positions.
//
// Split out of index.js purely so it can be unit-tested: this is the one place
// where caller-supplied strings become OBJECT KEYS in a file written to disk, so
// it needs direct test coverage rather than only being exercised through a
// running server.

const RID_RE = /^[A-Za-z0-9_-]{1,64}$/;

// Node keys are MAC addresses ("48:A9:8A:E5:CE:34") or an "id:*3" fallback when a
// neighbour advertises no MAC. The character class is what keeps '..', '/' and
// prototype-pollution keys out of the persisted object.
const KEY_RE = /^[A-Za-z0-9:._-]{1,64}$/;

const MAX_NODES = 200;
const COORD_LIMIT = 5000;

const BANNED_KEYS = new Set(['__proto__', 'constructor', 'prototype']);

function isValidRouterId(rid) {
  return typeof rid === 'string' && RID_RE.test(rid);
}

/**
 * Sanitise a positions map. Returns a null-prototype object of
 * `{ key: {x, y} }`, or null when the input is unusable — callers must treat
 * null as a 400 rather than as "no positions".
 */
function cleanPositions(raw) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  const proto = Object.getPrototypeOf(raw);
  if (proto !== Object.prototype && proto !== null) return null;

  const keys = Object.keys(raw);
  if (keys.length > MAX_NODES) return null;

  const out = Object.create(null);
  for (const k of keys) {
    if (BANNED_KEYS.has(k)) return null;
    if (!KEY_RE.test(k)) return null;
    const v = raw[k];
    if (!v || typeof v !== 'object' || Array.isArray(v)) return null;
    const x = Number(v.x), y = Number(v.y);
    if (!Number.isFinite(x) || !Number.isFinite(y)) return null;
    out[k] = {
      x: +Math.max(-COORD_LIMIT, Math.min(COORD_LIMIT, x)).toFixed(1),
      y: +Math.max(-COORD_LIMIT, Math.min(COORD_LIMIT, y)).toFixed(1),
    };
  }
  return out;
}

module.exports = { isValidRouterId, cleanPositions, MAX_NODES, COORD_LIMIT };
