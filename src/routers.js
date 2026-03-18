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
 *   addedAt:     number,   // epoch ms
 * }
 */

'use strict';
const fs     = require('fs');
const path   = require('path');
const crypto = require('crypto');

const DATA_DIR     = process.env.DATA_DIR || '/data';
const ROUTERS_FILE = path.join(DATA_DIR, 'routers.json');

// ── Encryption (same algorithm + key derivation as settings.js) ──────────────
const SALT = 'mikrodash-settings-v1';

function _deriveKey() {
  const secret = process.env.DATA_SECRET || 'mikrodash-insecure-default-secret';
  return crypto.scryptSync(secret, SALT, 32);
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
  } catch (_) {
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
  return String(s || '').replace(/\s*[··••].*/,'').trim();
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

// ── File I/O ──────────────────────────────────────────────────────────────────
function _ensureDataDir() {
  try { fs.mkdirSync(DATA_DIR, { recursive: true }); } catch (_) {}
}

let _cache = null; // in-memory list of decrypted router objects

function _readFile() {
  _ensureDataDir();
  try {
    const raw = JSON.parse(fs.readFileSync(ROUTERS_FILE, 'utf8'));
    if (!Array.isArray(raw)) return [];
    return raw.map(r => ({ ...r, password: _decrypt(r.password || '') }));
  } catch (_) {
    return [];
  }
}

function _writeFile(routers) {
  _ensureDataDir();
  const toWrite = routers.map(r => ({ ...r, password: _encrypt(r.password || '') }));
  fs.writeFileSync(ROUTERS_FILE, JSON.stringify(toWrite, null, 2), 'utf8');
}

// ── Public API ────────────────────────────────────────────────────────────────

/**
 * Load all routers. Returns decrypted objects (password in plaintext).
 * Seeds from settings.json on first run if routers.json doesn't exist.
 */
function loadAll() {
  if (_cache) return _cache;

  if (!fs.existsSync(ROUTERS_FILE)) {
    // Backwards-compatibility seed: migrate existing single-router settings
    try {
      const Settings = require('./settings');
      const s = Settings.load();
      if (s.routerHost) {
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
function add(data) {
  const routers = loadAll();
  const rawLabel = _cleanLabel((data.label || data.host || 'New Router').slice(0, 64));
  const label    = _uniqueLabel(rawLabel, routers);
  const entry    = {
    id:          _uuid(),
    label,
    host:        String(data.host        || '').trim(),
    port:        parseInt(data.port      || '8729', 10),
    tls:         data.tls !== false && data.tls !== 'false',
    tlsInsecure: !!(data.tlsInsecure || data.tlsInsecure === 'true'),
    username:    String(data.username    || 'admin').trim(),
    password:    String(data.password    || ''),
    defaultIf:   String(data.defaultIf   || 'ether1').trim(),
    pingTarget:  String(data.pingTarget  || '1.1.1.1').trim(),
    addedAt:     Date.now(),
  };
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
  const rawLabel  = data.label !== undefined
    ? _cleanLabel(String(data.label).slice(0, 64))
    : _cleanLabel(existing.label);
  const label = _uniqueLabel(rawLabel, routers, id);

  const updated = {
    ...existing,
    label,
    host:        data.host        !== undefined ? String(data.host).trim()        : existing.host,
    port:        data.port        !== undefined ? parseInt(data.port, 10)          : existing.port,
    tls:         data.tls         !== undefined ? (data.tls !== false && data.tls !== 'false') : existing.tls,
    tlsInsecure: data.tlsInsecure !== undefined ? !!(data.tlsInsecure || data.tlsInsecure === 'true') : existing.tlsInsecure,
    username:    data.username    !== undefined ? String(data.username).trim()     : existing.username,
    defaultIf:   data.defaultIf   !== undefined ? String(data.defaultIf).trim()   : existing.defaultIf,
    pingTarget:  data.pingTarget  !== undefined ? String(data.pingTarget).trim()   : existing.pingTarget,
  };

  // Only update password if provided and not the mask sentinel
  if (data.password !== undefined && data.password !== '••••••••' && data.password !== '') {
    updated.password = String(data.password);
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

/** Delete a router by id. Returns true if deleted, false if not found. */
function remove(id) {
  const routers = loadAll();
  const next    = routers.filter(r => r.id !== id);
  if (next.length === routers.length) return false;
  _cache = next;
  _writeFile(next);
  return true;
}

/**
 * Return routers safe to send to the browser — passwords masked.
 */
function getPublic() {
  return loadAll().map(r => ({ ...r, password: r.password ? '••••••••' : '' }));
}

/** Invalidate the in-memory cache (used after external settings changes). */
function invalidateCache() { _cache = null; }

module.exports = { loadAll, getById, add, update, updateLabel, remove, getPublic, invalidateCache };
