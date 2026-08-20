/**
 * Router store — persists to /data/routers.json (Docker volume mount).
 *
 * Each entry represents one MikroTik router MikroDash can connect to.
 * Router passwords are AES-256-GCM encrypted using the same key derivation
 * as settings.js (DATA_SECRET env var → scryptSync → 32-byte key).
 *
 * On first start, if routers.json does not exist but settings.json contains
 * router credentials, a single router entry is automatically seeded from
 * those credentials so existing deployments upgrade seamlessly.
 *
 * Shape of a stored entry (all fields except password in plaintext):
 * {
 *   id:          string,   // UUID v4 — stable identifier across edits
 *   label:       string,   // User-editable display name (default: board-name from RouterOS)
 *   host:        string,
 *   port:        number,
 *   tls:         boolean,
 *   tlsInsecure: boolean,
 *   username:    string,
 *   password:    string,   // AES-256-GCM encrypted at rest
 *   defaultIf:   string,
 *   pingTarget:  string,
 *   bwDownMbps:  number,   // WAN download capacity in Mbps (default 1000 = 1 Gbps)
 *   bwUpMbps:    number,   // WAN upload capacity in Mbps   (default 1000 = 1 Gbps)
 *   addedAt:     number,   // epoch ms
 *   alertsEnabled:        boolean,  // per-router alert monitoring
 *   connDownThresholdSec: number,   // 0-300, debounce before declaring offline
 *   disabled:    boolean,  // set via PUT; the session is torn down while true
 *   siteId:      string|null,  // site membership (#78); null = no site.
 *                              // Sites live in SQLite — only the membership is
 *                              // here, so there is no foreign key, and deleting
 *                              // a site must call clearSite() to detach routers.
 *   model/serial/osVersion: string,  // learned from RouterOS, see updateIdentity()
 *   collection:  {         // optional, per-router collection settings (#105)
 *     mode:      'stream'|'poll',        // delivery; absent = stream
 *     off:       string[],               // collector keys turned off
 *     overrides: { pollX:number, streamX:boolean },
 *   },
 *   geo:         {         // optional, where the router is (#96)
 *     place:     { name, region, cc, lat, lon },   // picked from the gazetteer
 *     auto:      { name, region, cc, lat, lon,     // derived from the WAN IP
 *                  ip, accuracyKm, ts },           //   ip = which address, ts = when
 *   },                     // absent entirely when nothing locates the router
 * }
 */

'use strict';
const fs     = require('fs');
const path   = require('path');
const crypto = require('crypto');
const { isValidIp } = require('./util/ip');
const { MODES, DISABLEABLE, POLL_KEYS, STREAM_KEYS, clampPollValue } = require('./collection');
const GeoPlace = require('./geoPlace');

const DATA_DIR     = process.env.DATA_DIR || '/data';
const ROUTERS_FILE = path.join(DATA_DIR, 'routers.json');

// ── Encryption (same algorithm + key derivation as settings.js) ──────────────
const SALT = 'mikrodash-settings-v1';

function _loadOrCreateSecret() {
  if (process.env.DATA_SECRET) return process.env.DATA_SECRET;
  const secretFile = path.join(DATA_DIR, '.secret');
  try {
    if (fs.existsSync(secretFile)) return fs.readFileSync(secretFile, 'utf8').trim();
  } catch (_) {}
  const generated = crypto.randomBytes(32).toString('base64');
  try {
    fs.mkdirSync(DATA_DIR, { recursive: true });
    fs.writeFileSync(secretFile, generated, { encoding: 'utf8', mode: 0o600 });
  } catch (_) {}
  return generated;
}

let _cachedKey = null;
function _deriveKey() {
  if (!_cachedKey) _cachedKey = crypto.scryptSync(_loadOrCreateSecret(), SALT, 32);
  return _cachedKey;
}

function _encrypt(plaintext) {
  if (!plaintext) return '';
  const key    = _deriveKey();
  const iv     = crypto.randomBytes(12);
  const cipher = crypto.createCipheriv('aes-256-gcm', key, iv);
  const enc    = Buffer.concat([cipher.update(plaintext, 'utf8'), cipher.final()]);
  const tag    = cipher.getAuthTag();
  return Buffer.concat([iv, tag, enc]).toString('base64');
}

function _decrypt(b64) {
  if (!b64) return '';
  try {
    const key = _deriveKey();
    const buf = Buffer.from(b64, 'base64');
    const iv  = buf.slice(0, 12);
    const tag = buf.slice(12, 28);
    const enc = buf.slice(28);
    const dec = crypto.createDecipheriv('aes-256-gcm', key, iv);
    dec.setAuthTag(tag);
    return dec.update(enc) + dec.final('utf8');
  } catch (e) {
    console.warn('[routers] AES-GCM auth tag failure — credential may be corrupt or key changed');
    return '';
  }
}

// ── UUID v4 ───────────────────────────────────────────────────────────────────
function _uuid() {
  return crypto.randomUUID ? crypto.randomUUID() : crypto.randomBytes(16).toString('hex');
}

// ── Label sanitisation ────────────────────────────────────────────────────────
// Strips ROS version suffixes like " · ROS 7.22 (stable)" that may have been
// written into labels by earlier code iterations. Keeps the display name clean.
function _cleanLabel(s) {
  return String(s || '').replace(/\s*[·•‧․].*/u, '').trim();
}

// ── Name uniqueness ───────────────────────────────────────────────────────────
// If `label` already exists in `routers`, append " - [2]", " - [3]", etc.
function _uniqueLabel(label, routers, excludeId = null) {
  const base   = label.replace(/\s*-\s*\[\d+\]$/, '').trim();
  const taken  = new Set(
    routers
      .filter(r => r.id !== excludeId)
      .map(r => r.label)
  );
  if (!taken.has(base)) return base;
  let n = 2;
  while (taken.has(`${base} - [${n}]`)) n++;
  return `${base} - [${n}]`;
}

// ── Input validation ──────────────────────────────────────────────────────────
/**
 * Normalise a per-router `collection` block (#105).
 *
 * Server-side truth: the UI mirrors these rules but a hand-edited routers.json
 * must not be able to produce a combination that breaks a card, so filtering
 * happens here rather than in the browser.
 *
 *   undefined  the caller omitted the field entirely -> keep what is stored.
 *              update() rebuilds records field by field, so without this an
 *              unrelated edit would silently wipe the block.
 *   null       explicit reset to "inherit everything".
 *
 * A block that carries no information is stored as undefined, so a router left
 * on defaults keeps routers.json byte-identical to before this feature.
 */
function _normalizeCollection(input, existing) {
  const prev = existing ? existing.collection : undefined;
  if (input === undefined) return prev;
  if (input === null) return undefined;
  if (typeof input !== 'object' || Array.isArray(input)) return prev;

  const out = {};
  if (MODES.includes(input.mode)) out.mode = input.mode;

  if (Array.isArray(input.off)) {
    // Unknown and protected keys are dropped silently, matching how update()
    // already ignores unknown fields.
    const off = [...new Set(input.off.filter(k => DISABLEABLE.includes(k)))].sort();
    if (off.length) out.off = off;
  }

  if (input.overrides && typeof input.overrides === 'object') {
    const ovr = {};
    for (const [k, v] of Object.entries(input.overrides)) {
      if (POLL_KEYS.includes(k)) {
        const n = clampPollValue(k, v);      // same bounds settings.js enforces
        if (n !== null) ovr[k] = n;
      } else if (STREAM_KEYS.includes(k)) {
        ovr[k] = (v === true || v === 'true');
      }
    }
    if (Object.keys(ovr).length) out.overrides = ovr;
  }

  // 'stream' is the default, so a block saying only that carries no information.
  const hasInfo = (out.mode && out.mode !== 'stream') || out.off || out.overrides;
  return hasInfo ? out : undefined;
}

/**
 * Schedules a backup may run on, and how often each means in milliseconds.
 *
 * Daily is the default. A run that finds nothing changed writes no files at
 * all — it costs one export read, about two seconds — so the frequency buys
 * tighter drift detection rather than disk.
 */
const BACKUP_SCHEDULES = Object.freeze({
  hourly:  3600000,
  daily:   86400000,
  weekly:  604800000,
  monthly: 2592000000,
});

const BACKUP_DEFAULTS = Object.freeze({
  enabled: false,          // opt in per router; nothing starts backing up on upgrade
  schedule: 'daily',
  // A router that has never had a time chosen backs up at 08:00. Note the
  // distinction the scheduler relies on: an ABSENT `time` takes this default,
  // while an explicitly stored '' means "any time" and keeps the interval-only
  // behaviour. So clearing the field is a real choice the operator can make, and
  // one that survives — it is not read back as "unset, use the default".
  //
  // The cost, accepted deliberately: a router carrying a backup block written
  // before this field existed has no `time` key, so it moves to 08:00 on upgrade
  // rather than staying wherever its interval had drifted to.
  time: '08:00',
  keepCount: 30,
  keepDays: 365,
});

/**
 * 'HH:MM' 24-hour, or '' for no preference.
 *
 * Anything else falls back rather than being coerced: half-parsing a time would
 * schedule the backup at an hour nobody chose, and do it silently.
 */
function _normalizeTime(value, fallback) {
  if (value === undefined || value === null) return fallback;
  const s = String(value).trim();
  if (s === '') return '';
  const m = /^([01]?\d|2[0-3]):([0-5]\d)$/.exec(s);
  if (!m) return fallback;
  return String(m[1]).padStart(2, '0') + ':' + m[2];
}

/** Minutes since local midnight, or null when no time is set. */
function backupTimeMinutes(time) {
  const m = /^([01]\d|2[0-3]):([0-5]\d)$/.exec(String(time || ''));
  return m ? (Number(m[1]) * 60 + Number(m[2])) : null;
}

function _clampInt(value, fallback, min, max) {
  if (value === undefined || value === null || value === '') return fallback;
  const n = Math.trunc(Number(value));
  if (!Number.isFinite(n)) return fallback;
  return Math.max(min, Math.min(max, n));
}

/**
 * Normalize the per-router `backup` block.
 *
 * Same three-way contract as _normalizeCollection, and for the same reason:
 * update() rebuilds a record field by field, so an edit that does not mention
 * backups must not erase them.
 *
 *   undefined  keep what is stored
 *   null       reset to no backup configuration at all
 *
 * The password is generated here on first enable and never accepted from a
 * caller: it is the key to an encrypted archive of the whole device, so the
 * only thing that should ever know it is this process. An existing one is
 * carried forward, because regenerating it would orphan every stored .backup —
 * RouterOS can only load one with the password it was written with.
 */
function _normalizeBackup(input, existing) {
  const prev = existing ? existing.backup : undefined;
  if (input === undefined) return prev;
  if (input === null) return undefined;
  if (typeof input !== 'object' || Array.isArray(input)) return prev;

  const out = {
    enabled:  input.enabled === undefined ? (prev ? !!prev.enabled : BACKUP_DEFAULTS.enabled)
                                          : (input.enabled === true || input.enabled === 'true'),
    schedule: BACKUP_SCHEDULES[input.schedule] ? input.schedule
                                               : ((prev && prev.schedule) || BACKUP_DEFAULTS.schedule),
    time: _normalizeTime(input.time, prev && prev.time !== undefined ? prev.time
                                                                    : BACKUP_DEFAULTS.time),
    keepCount: _clampInt(input.keepCount, prev ? prev.keepCount : BACKUP_DEFAULTS.keepCount, 0, 1000),
    keepDays:  _clampInt(input.keepDays,  prev ? prev.keepDays  : BACKUP_DEFAULTS.keepDays,  0, 3650),
    // Never from the caller. Generated once, then carried forward forever.
    password: (prev && prev.password) || '',
  };
  if (out.enabled && !out.password) {
    out.password = crypto.randomBytes(24).toString('base64url');
  }
  return out;
}

/**
 * Normalize the per-router `geo` block (issue #96).
 *
 * Same three-way contract as _normalizeCollection, for the same reason:
 *
 *   undefined  the caller omitted the field entirely -> keep what is stored.
 *              update() rebuilds records field by field, so without this an
 *              unrelated edit would silently wipe the block.
 *   null       explicit reset to "no location of its own".
 *
 * The same rule then applies INDEPENDENTLY to each of `place` and `auto`, and
 * that second layer is the one that is easy to get wrong. The router modal sends
 * `{ place: <picked>|null }` and never sends `auto`, so `auto === undefined`
 * MUST mean "keep the fix the server learned in the background". Collapse the two
 * halves into a single all-or-nothing rule and every save from the modal quietly
 * erases the automatic location.
 *
 *   place  the manual pick — the only half a browser may set
 *   auto   the cached fix from the WAN IP — written by updateGeoAuto()
 */
function _normalizeGeo(input, existing) {
  const prev = existing ? existing.geo : undefined;
  if (input === undefined) return prev;
  if (input === null) return undefined;
  if (typeof input !== 'object' || Array.isArray(input)) return prev;

  const out = {};

  if (input.place === undefined) {
    if (prev && prev.place) out.place = prev.place;
  } else if (input.place !== null) {
    const p = GeoPlace.normalizePlace(input.place);
    if (p) out.place = p;                    // malformed input is dropped, not stored
  }

  if (input.auto === undefined) {
    if (prev && prev.auto) out.auto = prev.auto;
  } else if (input.auto !== null) {
    const a = GeoPlace.normalizePlace(input.auto);
    if (a) {
      out.auto = a;
      // Provenance, kept beside the place so the popover can say where the guess
      // came from and so the refresh can tell whether the WAN IP has moved.
      const ip = typeof input.auto.ip === 'string' ? input.auto.ip.trim().slice(0, 45) : '';
      if (ip) out.auto.ip = ip;
      const km = Number(input.auto.accuracyKm);
      if (Number.isFinite(km) && km > 0) out.auto.accuracyKm = km;
      const ts = Number(input.auto.ts);
      if (Number.isFinite(ts) && ts > 0) out.auto.ts = ts;
    }
  }

  // A block with neither half carries no information. Returning undefined keeps
  // routers.json byte-identical for every router nobody has located.
  return (out.place || out.auto) ? out : undefined;
}

const VALID_HOST = /^[a-zA-Z0-9.\-]{1,253}$/;

function _validateHostPort(host, port) {
  if (!VALID_HOST.test(String(host || '').trim())) throw new Error('Invalid host');
  const p = Number(port);
  if (!Number.isInteger(p) || p < 1 || p > 65535) throw new Error('Invalid port');
}

// Same rules buildSession() enforces — reject at save time, because a persisted
// bad value makes buildSession throw on every future connection (survives restart).
function _validateTargets(defaultIf, pingTarget) {
  if (defaultIf && !/^[A-Za-z0-9_./-]{1,128}$/.test(defaultIf)) throw new Error('Invalid defaultIf');
  if (pingTarget && !isValidIp(pingTarget)) throw new Error('Invalid pingTarget — must be a valid IP address');
}

// ── File I/O ──────────────────────────────────────────────────────────────────
function _ensureDataDir() {
  try { fs.mkdirSync(DATA_DIR, { recursive: true }); } catch (_) {}
}

let _cache = null; // in-memory list of decrypted router objects

// Ciphertext that failed to decrypt (key mismatch/corruption), keyed by router id.
// Preserved verbatim on save so an unrelated edit can't blank the stored
// credential permanently; cleared when a new password is explicitly set.
const _cipherKeep = new Map();

function _readFile() {
  _ensureDataDir();
  try {
    const raw = JSON.parse(fs.readFileSync(ROUTERS_FILE, 'utf8'));
    if (!Array.isArray(raw)) return [];
    return raw.map(r => {
      const plain = _decrypt(r.password || '');
      if (!plain && r.password) _cipherKeep.set(r.id, r.password);
      const out = { ...r, password: plain };
      // The backup password unlocks an encrypted archive of the entire device,
      // so it is encrypted at rest exactly as the router credential is.
      if (r.backup && r.backup.password) {
        out.backup = { ...r.backup, password: _decrypt(r.backup.password) };
      }
      return out;
    });
  } catch (_) {
    return [];
  }
}

function _writeFile(routers) {
  _ensureDataDir();
  const toWrite = routers.map(r => {
    const out = {
      ...r,
      password: r.password ? _encrypt(r.password) : (_cipherKeep.get(r.id) || ''),
    };
    if (r.backup && r.backup.password) {
      out.backup = { ...r.backup, password: _encrypt(r.backup.password) };
    }
    return out;
  });
  const tmp = ROUTERS_FILE + '.tmp';
  // mode 0o600 — file holds encrypted credentials; keep it owner-only.
  fs.writeFileSync(tmp, JSON.stringify(toWrite, null, 2), { encoding: 'utf8', mode: 0o600 });
  fs.renameSync(tmp, ROUTERS_FILE);
}

// ── Public API ────────────────────────────────────────────────────────────────

/**
 * Load all routers. Returns decrypted objects (password in plaintext).
 * Seeds from settings.json on first run if routers.json doesn't exist.
 */
function loadAll() {
  if (_cache) return _cache;

  if (!fs.existsSync(ROUTERS_FILE)) {
    // Backwards-compatibility seed: migrate existing single-router settings.
    // Only runs when settings.json already exists (i.e. a real prior deployment).
    // On a fresh install there is no settings.json, so routers.json starts empty.
    const Settings = require('./settings');
    const settingsFile = path.join(DATA_DIR, 'settings.json');
    try {
      const s = Settings.load();
      if (fs.existsSync(settingsFile) && s.routerHost) {
        const seed = [{
          id:          _uuid(),
          label:       'My Router',   // will be replaced by board name on first connect
          host:        s.routerHost,
          port:        s.routerPort   || 8729,
          tls:         s.routerTls    !== false,
          tlsInsecure: !!s.routerTlsInsecure,
          username:    s.routerUser   || 'admin',
          password:    s.routerPass   || '',
          defaultIf:   s.defaultIf    || 'ether1',
          pingTarget:  s.pingTarget   || '1.1.1.1',
          addedAt:     Date.now(),
        }];
        _cache = seed;
        _writeFile(seed);
        return _cache;
      }
    } catch (_) {}
    _cache = [];
    return _cache;
  }

  _cache = _readFile();
  return _cache;
}

/** Return a single router by id, or null. */
function getById(id) {
  return loadAll().find(r => r.id === id) || null;
}

/**
 * Add a new router. `data` fields: host, port, tls, tlsInsecure, username,
 * password, defaultIf, pingTarget, label (optional).
 * Returns the saved router object (with generated id, decrypted password).
 */
// Site ids come from the browser, so they are validated rather than trusted.
// Empty string, null and undefined all mean "no site" — the picker's
// "— No site —" option submits '', and an old record has the field absent.
const _SITE_ID_RE = /^[A-Za-z0-9_-]{1,64}$/;
function _cleanSiteId(v) {
  if (v === null || v === undefined || v === '') return null;
  const s = String(v).trim();
  return _SITE_ID_RE.test(s) ? s : null;
}

/**
 * Detach every router from a site. Called when the site is deleted: sites live
 * in SQLite and routers in JSON, so there is no foreign key to cascade through.
 * Returns how many routers were changed.
 */
function clearSite(siteId) {
  if (!siteId) return 0;
  const routers = loadAll();
  let changed = 0;
  for (const r of routers) {
    if (r.siteId === siteId) { r.siteId = null; changed++; }
  }
  if (changed) { _cache = routers; _writeFile(routers); }
  return changed;
}

function add(data) {
  _validateHostPort(data.host, data.port !== undefined ? data.port : 8729);
  const routers = loadAll();
  const rawLabel = _cleanLabel((data.label || data.host || 'New Router').slice(0, 64));
  const label    = _uniqueLabel(rawLabel, routers);
  const entry    = {
    id:            _uuid(),
    label,
    host:          String(data.host          || '').trim(),
    port:          parseInt(data.port        || '8729', 10),
    tls:           data.tls !== false && data.tls !== 'false',
    tlsInsecure:   !!(data.tlsInsecure || data.tlsInsecure === 'true'),
    username:      String(data.username      || 'admin').trim(),
    password:      (data.password && data.password !== '••••••••') ? String(data.password) : '',
    defaultIf:     String(data.defaultIf     || 'ether1').trim(),
    pingTarget:    String(data.pingTarget    || '1.1.1.1').trim(),
    bwDownMbps:    Math.max(1, parseInt(data.bwDownMbps || '1000', 10) || 1000),
    bwUpMbps:      Math.max(1, parseInt(data.bwUpMbps   || '1000', 10) || 1000),
    alertsEnabled:       !!(data.alertsEnabled),
    connDownThresholdSec:(function(){ var n = parseInt(data.connDownThresholdSec, 10); return (n >= 0 && n <= 300) ? n : 30; }()),
    collection:          _normalizeCollection(data.collection, null),
    geo:                 _normalizeGeo(data.geo, null),
    backup:              _normalizeBackup(data.backup, null),
    // Site membership (issue #78). Exactly one site, or none. Sites themselves
    // live in SQLite; only the membership is here, next to the rest of the
    // router's configuration. An absent field reads as site-less, so existing
    // records need no migration.
    siteId:              _cleanSiteId(data.siteId),
    disabled:            false,
    addedAt:             Date.now(),
  };
  _validateTargets(entry.defaultIf, entry.pingTarget);
  routers.push(entry);
  _cache = routers;
  _writeFile(routers);
  return entry;
}

/**
 * Update an existing router by id. Only provided fields are changed.
 * Password field is ignored if it equals the mask sentinel '••••••••'.
 * Returns the updated router, or null if not found.
 */
function update(id, data) {
  const routers = loadAll();
  const idx     = routers.findIndex(r => r.id === id);
  if (idx === -1) return null;

  const existing = routers[idx];
  if (data.host !== undefined) _validateHostPort(data.host, data.port !== undefined ? data.port : existing.port);
  else if (data.port !== undefined) _validateHostPort(existing.host, data.port);
  const rawLabel  = data.label !== undefined
    ? _cleanLabel(String(data.label).slice(0, 64))
    : _cleanLabel(existing.label);
  const label = _uniqueLabel(rawLabel, routers, id);

  const updated = {
    ...existing,
    label,
    host:          data.host          !== undefined ? String(data.host).trim()        : existing.host,
    port:          data.port          !== undefined ? parseInt(data.port, 10)          : existing.port,
    tls:           data.tls           !== undefined ? (data.tls !== false && data.tls !== 'false') : existing.tls,
    tlsInsecure:   data.tlsInsecure   !== undefined ? !!(data.tlsInsecure || data.tlsInsecure === 'true') : existing.tlsInsecure,
    username:      data.username      !== undefined ? String(data.username).trim()     : existing.username,
    defaultIf:     data.defaultIf     !== undefined ? String(data.defaultIf).trim()   : existing.defaultIf,
    pingTarget:    data.pingTarget     !== undefined ? String(data.pingTarget).trim()  : existing.pingTarget,
    bwDownMbps:    data.bwDownMbps    !== undefined ? Math.max(1, parseInt(data.bwDownMbps, 10) || 1000) : (existing.bwDownMbps || 1000),
    bwUpMbps:      data.bwUpMbps      !== undefined ? Math.max(1, parseInt(data.bwUpMbps,   10) || 1000) : (existing.bwUpMbps   || 1000),
    alertsEnabled:       data.alertsEnabled       !== undefined ? !!(data.alertsEnabled)           : !!(existing.alertsEnabled),
    connDownThresholdSec:(function(){ var raw = data.connDownThresholdSec !== undefined ? data.connDownThresholdSec : (existing.connDownThresholdSec !== undefined ? existing.connDownThresholdSec : 30); var n = parseInt(raw, 10); return (n >= 0 && n <= 300) ? n : 30; }()),
    collection:          _normalizeCollection(data.collection, existing),
    geo:                 _normalizeGeo(data.geo, existing),
    backup:              _normalizeBackup(data.backup, existing),
    siteId:              data.siteId !== undefined ? _cleanSiteId(data.siteId) : (existing.siteId || null),
    disabled:            data.disabled !== undefined ? !!(data.disabled) : !!(existing.disabled),
  };

  _validateTargets(updated.defaultIf, updated.pingTarget);

  // Only update password if provided and not the mask sentinel
  if (data.password !== undefined && data.password !== '••••••••' && data.password !== '') {
    updated.password = String(data.password);
    _cipherKeep.delete(id); // explicit new password supersedes preserved ciphertext
  }

  routers[idx] = updated;
  _cache = routers;
  _writeFile(routers);
  return updated;
}

/**
 * Update just the label for a router (called after first system:update
 * gives us the board name from RouterOS).
 */
function updateLabel(id, rawLabel) {
  const routers = loadAll();
  const idx     = routers.findIndex(r => r.id === id);
  if (idx === -1) return;
  const label = _uniqueLabel(_cleanLabel(String(rawLabel).slice(0, 64)), routers, id);
  routers[idx] = { ...routers[idx], label };
  _cache = routers;
  _writeFile(routers);
  return routers[idx];
}

/** Identity fields learned from RouterOS rather than entered by the user. */
const IDENTITY_FIELDS = ['model', 'serial', 'osVersion'];

/**
 * Record hardware/firmware identity reported by RouterOS. Persisted so the
 * Routers table can still show model, serial and version for a router that is
 * currently offline or disabled — which is the point of an inventory column,
 * and why this is stored rather than read from the live stats feed.
 *
 * Returns the updated router, or null when nothing changed. Callers rely on
 * that null: the system collector reports identity on every tick, and only a
 * genuine change should cost a file write and a broadcast to every client.
 */
function updateIdentity(id, identity) {
  const routers = loadAll();
  const idx     = routers.findIndex(r => r.id === id);
  if (idx === -1) return null;

  const current = routers[idx];
  const changed = {};
  for (const key of IDENTITY_FIELDS) {
    const val = identity ? identity[key] : null;
    if (typeof val !== 'string') continue;
    const clean = val.trim().slice(0, 64);
    if (clean && clean !== current[key]) changed[key] = clean;
  }
  if (!Object.keys(changed).length) return null;

  routers[idx] = { ...current, ...changed };
  _cache = routers;
  _writeFile(routers);
  return routers[idx];
}

/**
 * Persist the automatic location learned from a router's WAN IP (issue #96).
 *
 * Deliberately NOT routed through update(): that re-validates host and port,
 * recomputes the unique label, and its HTTP callers go on to bump RBAC and
 * broadcast a permissions change. A background geo refresh must do none of those.
 * Same reasoning, and same shape, as updateIdentity() directly above.
 *
 * Returns null when nothing changed, which callers rely on to skip both the file
 * write and the broadcast. That matters more here than for identity: the Routers
 * page rebuilds its stats every two seconds *per viewing socket*, so without this
 * guard two administrators with the page open would rewrite routers.json — a file
 * holding encrypted credentials — several times a second, forever.
 *
 * Pass null to clear a stale fix, which is what happens when a router moves to a
 * private WAN address that cannot be geolocated at all.
 */
function updateGeoAuto(id, auto) {
  const routers = loadAll();
  const idx     = routers.findIndex(r => r.id === id);
  if (idx === -1) return null;

  const current  = routers[idx];
  const prevGeo  = current.geo || undefined;
  const prevAuto = prevGeo ? prevGeo.auto : undefined;
  // Reuse the same validation the HTTP path uses, so a fix can never be stored in
  // a shape the reader would reject.
  const nextAuto = (auto === null || auto === undefined)
    ? undefined
    : (_normalizeGeo({ auto }, undefined) || {}).auto;

  // Both sides are produced by _normalizeGeo, so key order is stable and a string
  // compare is a sound deep-equality check here.
  if (JSON.stringify(prevAuto) === JSON.stringify(nextAuto)) return null;

  const geo = {};
  if (prevGeo && prevGeo.place) geo.place = prevGeo.place;   // never touch the manual pick
  if (nextAuto) geo.auto = nextAuto;

  routers[idx] = { ...current, geo: (geo.place || geo.auto) ? geo : undefined };
  _cache = routers;
  _writeFile(routers);
  return routers[idx];
}

/** Delete a router by id. Returns true if deleted, false if not found. */
function remove(id) {
  const routers = loadAll();
  const next    = routers.filter(r => r.id !== id);
  if (next.length === routers.length) return false;
  _cipherKeep.delete(id);
  _cache = next;
  _writeFile(next);
  return true;
}

/**
 * Return routers safe to send to the browser — passwords masked.
 */
function getPublic() {
  return loadAll().map(r => {
    const out = { ...r, password: r.password ? '••••••••' : '' };
    // The backup password is never masked-and-shown, it is REMOVED. Nothing in
    // the UI edits it, so there is no field for a mask to stand in for, and a
    // masked secret invites a round trip that could write the mask back.
    if (r.backup) {
      const { password, ...rest } = r.backup;
      out.backup = { ...rest, hasPassword: !!password };
    }
    return out;
  });
}

/** Invalidate the in-memory cache (used after external settings changes). */
function invalidateCache() { _cache = null; }

module.exports = { loadAll, getById, add, update, updateLabel, updateIdentity, updateGeoAuto, remove, getPublic, invalidateCache, clearSite,
  BACKUP_SCHEDULES, BACKUP_DEFAULTS, _normalizeBackup, backupTimeMinutes };
