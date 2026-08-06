/**
 * Single load point for geoip-lite.
 *
 * The package was previously required independently at three call sites, two of
 * which swallowed a load failure with an empty catch. That left `geoip = null`
 * and every geo lookup silently returning nothing: the world map, country
 * breakdowns and connection geo data would quietly empty out while the
 * dashboard otherwise looked healthy.
 *
 * geoip-lite declares `engines: { node: '>=24' }`, so a future patch release may
 * legitimately use a Node 24 API. npm only warns on an engine mismatch, so such
 * a bump would install cleanly, pass CI and reach production. Loading it once,
 * here, means the failure is reported once and the degraded state is a value
 * something can render rather than a comment in three files. See issue #101.
 */

let _geoip = null;
let _reason = '';

try {
  _geoip = require('geoip-lite');
} catch (e) {
  _reason = e && e.message ? e.message : String(e);
  // Constant format string, reason passed as an argument: the message comes
  // from a module loader and is not ours to trust as a format specifier.
  console.warn('[geo] geo lookups unavailable, geoip-lite failed to load: %s', _reason);
}

module.exports = {
  // Callers gate on this rather than on a truthy module handle so the check
  // reads the same everywhere.
  available: () => _geoip !== null,
  // Empty string when available. Surfaced in the diagnostics payload.
  unavailableReason: () => _reason,
  // Mirrors geoip-lite's own signature. Returns null when unavailable, which is
  // also what geoip-lite returns for an IP it cannot place, so callers already
  // handle it.
  lookup: (ip) => (_geoip ? _geoip.lookup(ip) : null),
};
