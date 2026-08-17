'use strict';
/**
 * City/town gazetteer for the location picker (issue #96).
 *
 * Locations are never typed as coordinates — they are chosen from a list. That
 * list has to come from somewhere, and shipping a dataset would mean a new
 * bundled file; calling a geocoding API would mean an outbound request, which
 * the strict CSP and the no-CDN rule both exist to prevent.
 *
 * So it is derived from geoip-lite's own data, which is already a dependency and
 * already in the image. The manual picker and the automatic WAN-IP fix then read
 * the same database and cannot disagree about where a place is.
 *
 * ── The fragile part, stated plainly ──────────────────────────────────────────
 * geoip-lite exposes lookup(ip) and nothing else, so there is no supported way to
 * enumerate places. The offsets below are its INTERNAL on-disk format, read from
 * node_modules/geoip-lite/lib/geoip.js (`conf4` and `lookup4`). The package is
 * pinned ^2.0.3, so a minor release may change them without warning.
 *
 * That is why every read is validated and why failure is a value, not an
 * exception: if the format moves, available() goes false, the picker says so, and
 * everything else — automatic geolocation, the map, the site fallback — keeps
 * working, because those go through the supported geo.lookup() API.
 *
 * Record counts are NOT stable: they change with every geoip-lite data refresh.
 * Nothing here, and no test, may assert one.
 */

const fs   = require('fs');
const path = require('path');

// geoip-lite's own record geometry (lib/geoip.js `conf4`).
const LOC_RECORD_SIZE   = 88;         // geoip-city-names.dat
const RANGE_RECORD_SIZE = 24;         // geoip-city.dat
const NO_LOCATION       = 4294967295; // (-1 >>> 0) — locId sentinel
// Field offsets within a location record.
const LOC_CC     = 0;
const LOC_REGION = 2;
const LOC_CITY   = 42;
// Field offsets within a range record.
const RANGE_LOCID = 8;
const RANGE_LAT   = 12;
const RANGE_LON   = 16;

const CHUNK_BYTES   = 4 * 1024 * 1024;   // aligned down to a record multiple below
const MIN_ROWS      = 10_000;            // a real database has ~110k
const IDLE_EVICT_MS = 10 * 60_000;
const MAX_LIMIT     = 50;

let _rows       = null;   // { names, keys, regions, ccs, lat, lon, weight, n }
let _reason     = '';
let _evictTimer = null;

/**
 * Where geoip-lite keeps its data.
 *
 * Mirrors lib/geoip.js:14-16 exactly, including GEODATADIR — resolved relative to
 * the package's lib/ directory when relative, as geoip-lite does. Hardcoding a
 * node_modules path would break under GEODATADIR, and that env var is also how
 * the degradation path is exercised in tests.
 */
function _dataDir() {
  const libDir = path.dirname(require.resolve('geoip-lite'));
  return path.resolve(libDir, global.geodatadir || process.env.GEODATADIR || '../data/');
}

/** Read a NUL-terminated string out of a fixed-width field. */
function _bufstr(buf, start, end) {
  const nul = buf.indexOf(0, start);
  if (nul >= start && nul < end) end = nul;
  return buf.toString('utf8', start, end);
}

/**
 * Scan the range table, collecting each location's first coordinate and how many
 * IP ranges point at it.
 *
 * Read in chunks rather than whole: the range file is ~76 MB and geoip-lite has
 * already loaded ~146 MB of its own buffers at require time, so a whole-file read
 * is a spike on top of an already-large baseline — which matters on the small
 * hardware MikroDash is often run on. The scan is purely sequential, so chunking
 * costs nothing.
 *
 * The range count becomes the ranking weight. It is a proxy for prominence, and it
 * is what makes searching "berlin" offer Berlin, DE before Berlin, CT — there is
 * no population field in this data, and without it the four Berlins come back in
 * an arbitrary order.
 */
function _scanRanges(file, locCount) {
  const lat    = new Float64Array(locCount);
  const lon    = new Float64Array(locCount);
  const weight = new Uint32Array(locCount);
  const seen   = new Uint8Array(locCount);

  const fd = fs.openSync(file, 'r');
  try {
    const size = fs.fstatSync(fd).size;
    if (size === 0 || size % RANGE_RECORD_SIZE !== 0) {
      throw new Error(`geoip-city.dat is ${size} bytes, not a multiple of ${RANGE_RECORD_SIZE}`);
    }
    const chunkSize = Math.floor(CHUNK_BYTES / RANGE_RECORD_SIZE) * RANGE_RECORD_SIZE;
    const buf = Buffer.alloc(chunkSize);
    let pos = 0;
    while (pos < size) {
      const want = Math.min(chunkSize, size - pos);
      const got  = fs.readSync(fd, buf, 0, want, pos);
      if (got <= 0) break;
      // Only whole records; a short read resumes from where it stopped.
      const usable = got - (got % RANGE_RECORD_SIZE);
      if (usable === 0) break;
      for (let o = 0; o < usable; o += RANGE_RECORD_SIZE) {
        const locId = buf.readUInt32BE(o + RANGE_LOCID);
        if (locId === NO_LOCATION || locId >= locCount) continue;
        weight[locId]++;
        if (seen[locId]) continue;
        seen[locId] = 1;
        lat[locId] = buf.readInt32BE(o + RANGE_LAT) / 10000;
        lon[locId] = buf.readInt32BE(o + RANGE_LON) / 10000;
      }
      pos += usable;
    }
  } finally {
    fs.closeSync(fd);
  }

  return { lat, lon, weight, seen };
}

/**
 * Build the index. Returns the row set, or throws — _ensure() turns a throw into
 * the degraded state.
 */
function _build() {
  const dir = _dataDir();
  const namesFile  = path.join(dir, 'geoip-city-names.dat');
  const rangesFile = path.join(dir, 'geoip-city.dat');

  const namesBuf = fs.readFileSync(namesFile);
  if (namesBuf.length === 0 || namesBuf.length % LOC_RECORD_SIZE !== 0) {
    throw new Error(
      `geoip-city-names.dat is ${namesBuf.length} bytes, not a multiple of ${LOC_RECORD_SIZE}`);
  }
  const locCount = namesBuf.length / LOC_RECORD_SIZE;

  const { lat, lon, weight, seen } = _scanRanges(rangesFile, locCount);

  // Parallel arrays rather than an array of objects: measured at roughly half the
  // retained memory for the same data, and the search is a linear scan either way.
  const names   = [];
  const keys    = [];   // lowercased name, so the hot loop does no case folding
  const regions = [];
  const ccs     = [];
  const rlat    = [];
  const rlon    = [];
  const rweight = [];

  // A few hundred location ids share a (name, region, cc) triple. Collapsing them
  // at build time keeps the picker from offering the same place twice; their
  // weights are summed so the merged row ranks on the combined evidence.
  const byKey = new Map();

  for (let id = 0; id < locCount; id++) {
    if (!seen[id]) continue;                       // no range points here
    const o = id * LOC_RECORD_SIZE;
    const name = _bufstr(namesBuf, o + LOC_CITY, o + LOC_RECORD_SIZE).trim();
    if (!name) continue;                           // country-level row, not a place
    const cc     = _bufstr(namesBuf, o + LOC_CC, o + LOC_CC + 2).trim();
    const region = _bufstr(namesBuf, o + LOC_REGION, o + LOC_REGION + 3).trim();

    const k = name + '|' + region + '|' + cc;
    const hit = byKey.get(k);
    if (hit !== undefined) { rweight[hit] += weight[id]; continue; }

    byKey.set(k, names.length);
    names.push(name);
    keys.push(name.toLowerCase());
    regions.push(region);
    ccs.push(cc);
    rlat.push(lat[id]);
    rlon.push(lon[id]);
    rweight.push(weight[id]);
  }

  const n = names.length;
  if (n < MIN_ROWS) throw new Error(`only ${n} places decoded, expected at least ${MIN_ROWS}`);

  // Sample across the whole array rather than the first few: a format change that
  // shifted one field would still leave early records superficially plausible.
  for (let i = 0; i < 100; i++) {
    const j = Math.floor((i / 100) * n);
    if (!names[j]) throw new Error(`empty place name at row ${j}`);
    if (!/^[A-Z]{2}$/.test(ccs[j])) {
      throw new Error(`bad country code ${JSON.stringify(ccs[j])} at row ${j}`);
    }
    if (!(rlat[j] >= -90 && rlat[j] <= 90)) throw new Error(`latitude ${rlat[j]} out of range at row ${j}`);
    if (!(rlon[j] >= -180 && rlon[j] <= 180)) throw new Error(`longitude ${rlon[j]} out of range at row ${j}`);
  }

  return {
    names, keys, regions, ccs,
    lat: Float64Array.from(rlat),
    lon: Float64Array.from(rlon),
    weight: Uint32Array.from(rweight),
    n,
  };
}

/**
 * Build on first use, and drop the index once nobody has searched for a while.
 *
 * Choosing a town is a rare administrative act, so most installs would otherwise
 * carry tens of megabytes forever for a list nobody opens. The timer is unref'd: a
 * live timer holding the closure would keep the process alive at shutdown.
 */
function _ensure() {
  if (_evictTimer) clearTimeout(_evictTimer);
  _evictTimer = setTimeout(() => { _rows = null; _evictTimer = null; }, IDLE_EVICT_MS);
  if (_evictTimer.unref) _evictTimer.unref();

  if (_rows) return true;
  if (_reason) return false;             // already failed; do not retry every keystroke

  try {
    _rows = _build();
    return true;
  } catch (e) {
    _reason = e && e.message ? e.message : String(e);
    _rows = null;
    // Constant format string, reason as an argument — same rule as src/geo.js: the
    // message describes foreign data and is not ours to trust as a specifier.
    console.warn('[cityIndex] city search unavailable, could not read the geoip place data: %s', _reason);
    return false;
  }
}

/**
 * Prefix search, best match first.
 *
 * A linear scan of ~110k lowercased names measures in low single-digit
 * milliseconds, so there is no index here and should not be one — a trie would be
 * more code, more memory and no faster at this size.
 *
 * Ranking: an exact name match first, then the heaviest place (most IP ranges, so
 * the most prominent), then the shortest name, then country for stability. Weight
 * is what makes "berlin" offer Berlin, DE ahead of Berlin, CT.
 */
function search(q, limit) {
  const query = typeof q === 'string' ? q.trim().toLowerCase() : '';
  // One letter matches thousands of places and is never a real intent.
  if (query.length < 2) return [];
  if (!_ensure()) return [];

  const cap = Math.min(Math.max(parseInt(limit, 10) || 20, 1), MAX_LIMIT);
  const r = _rows;
  const hits = [];
  for (let i = 0; i < r.n; i++) {
    if (r.keys[i].startsWith(query)) hits.push(i);
  }

  hits.sort((a, b) => {
    const ea = r.keys[a] === query ? 0 : 1;
    const eb = r.keys[b] === query ? 0 : 1;
    if (ea !== eb) return ea - eb;
    if (r.weight[a] !== r.weight[b]) return r.weight[b] - r.weight[a];
    if (r.keys[a].length !== r.keys[b].length) return r.keys[a].length - r.keys[b].length;
    return r.ccs[a] < r.ccs[b] ? -1 : r.ccs[a] > r.ccs[b] ? 1 : 0;
  });

  return hits.slice(0, cap).map((i) => ({
    name: r.names[i], region: r.regions[i], cc: r.ccs[i], lat: r.lat[i], lon: r.lon[i],
  }));
}

module.exports = {
  // Mirrors src/geo.js's surface so the two read alike at the call site. Note this
  // builds the index if it is not resident — a caller that only wants to know
  // whether the feature works should expect the first call to be the slow one.
  available: () => _ensure(),
  // Empty string when available. Surfaced in the diagnostics payload, which is
  // where to look if the picker mysteriously returns nothing after a dep bump.
  unavailableReason: () => _reason,
  search,
  // Testing seam: forget both the index and a recorded failure, so a test can
  // point GEODATADIR elsewhere and rebuild.
  _reset: () => {
    if (_evictTimer) clearTimeout(_evictTimer);
    _evictTimer = null;
    _rows = null;
    _reason = '';
  },
};
