/**
 * Settings store — persists to /data/settings.json (Docker volume mount).
 * Credentials (router password, dashboard password) are AES-256-GCM encrypted
 * at rest using a key derived from a machine-stable secret.
 *
 * Key derivation: scryptSync(SECRET, salt, 32) where SECRET comes from
 * DATA_SECRET env var (required for encryption) or falls back to a
 * deterministic value so the container starts without it (not recommended
 * for production credential storage).
 */

'use strict';
const fs     = require('fs');
const path   = require('path');
const crypto = require('crypto');

const DATA_DIR      = process.env.DATA_DIR || '/data';
const SETTINGS_FILE = path.join(DATA_DIR, 'settings.json');

// ── Encryption helpers ───────────────────────────────────────────────────────
const SALT = 'mikrodash-settings-v1'; // fixed salt — uniqueness via DATA_SECRET
function _deriveKey() {
  const secret = process.env.DATA_SECRET || 'mikrodash-insecure-default-secret';
  return crypto.scryptSync(secret, SALT, 32);
}

function encrypt(plaintext) {
  if (!plaintext) return '';
  const key  = _deriveKey();
  const iv   = crypto.randomBytes(12);
  const cipher = crypto.createCipheriv('aes-256-gcm', key, iv);
  const enc  = Buffer.concat([cipher.update(plaintext, 'utf8'), cipher.final()]);
  const tag  = cipher.getAuthTag();
  // iv(12) + tag(16) + ciphertext — all base64
  return Buffer.concat([iv, tag, enc]).toString('base64');
}

function decrypt(b64) {
  if (!b64) return '';
  try {
    const key  = _deriveKey();
    const buf  = Buffer.from(b64, 'base64');
    const iv   = buf.slice(0, 12);
    const tag  = buf.slice(12, 28);
    const enc  = buf.slice(28);
    const dec  = crypto.createDecipheriv('aes-256-gcm', key, iv);
    dec.setAuthTag(tag);
    return dec.update(enc) + dec.final('utf8');
  } catch (_) {
    return ''; // wrong key / corrupted
  }
}

// ── Defaults ─────────────────────────────────────────────────────────────────
const DEFAULTS = {
  // Router connection
  routerHost:        process.env.ROUTER_HOST        || '192.168.88.1',
  routerPort:        parseInt(process.env.ROUTER_PORT || '8729', 10),
  routerTls:         (process.env.ROUTER_TLS         || 'true').toLowerCase() === 'true',
  routerTlsInsecure: (process.env.ROUTER_TLS_INSECURE|| 'false').toLowerCase() === 'true',
  routerUser:        process.env.ROUTER_USER         || 'admin',
  routerPass:        '', // stored encrypted
  defaultIf:         process.env.DEFAULT_IF          || 'ether1',

  // Dashboard auth
  dashUser:          process.env.BASIC_AUTH_USER     || '',
  dashPass:          '', // stored encrypted

  // Ping
  pingTarget:        process.env.PING_TARGET         || '1.1.1.1',

  // Poll intervals (ms)
  pollConns:         parseInt(process.env.CONNS_POLL_MS     || '3000',  10),
  pollBandwidth:     parseInt(process.env.BANDWIDTH_POLL_MS  || '3000',  10),
  pollSystem:        parseInt(process.env.SYSTEM_POLL_MS    || '3000',  10),
  pollWireless:      parseInt(process.env.WIRELESS_POLL_MS  || '5000',  10),
  pollVpn:           parseInt(process.env.VPN_POLL_MS       || '10000', 10),
  pollFirewall:      parseInt(process.env.FIREWALL_POLL_MS  || '10000', 10),
  pollIfstatus:      parseInt(process.env.IFSTATUS_POLL_MS  || '5000',  10),
  pollPing:          parseInt(process.env.PING_POLL_MS      || '10000', 10),
  pollArp:           parseInt(process.env.ARP_POLL_MS       || '30000', 10),
  pollDhcp:          parseInt(process.env.DHCP_POLL_MS      || '300000', 10),

  // Limits
  topN:              parseInt(process.env.TOP_N             || '10',    10),
  topTalkersN:       parseInt(process.env.TOP_TALKERS_N     || '5',     10),
  firewallTopN:      parseInt(process.env.FIREWALL_TOP_N    || '15',    10),
  maxConns:          parseInt(process.env.MAX_CONNS         || '20000', 10),
  historyMinutes:    parseInt(process.env.HISTORY_MINUTES   || '30',    10),

  // Page visibility (true = visible)
  pageWireless:    true,
  pageInterfaces:  true,
  pageDhcp:        true,
  pageVpn:         true,
  pageConnections: true,
  pageFirewall:    true,
  pageLogs:        true,
  pageBandwidth:   true,
};

// Fields stored encrypted in JSON
const ENCRYPTED_FIELDS = ['routerPass', 'dashPass'];
// Fields never sent to the client (only their masked presence)
const CREDENTIAL_FIELDS = ['routerPass', 'dashPass'];

// ── Load / Save ──────────────────────────────────────────────────────────────
let _cache = null;

function _ensureDataDir() {
  try { fs.mkdirSync(DATA_DIR, { recursive: true }); } catch (_) {}
}

function load() {
  if (_cache) return _cache;
  _ensureDataDir();
  let stored = {};
  try {
    stored = JSON.parse(fs.readFileSync(SETTINGS_FILE, 'utf8'));
  } catch (_) {
    // File missing or corrupt — start from defaults
  }
  // Merge stored over defaults; decrypt encrypted fields
  const merged = { ...DEFAULTS };
  for (const [k, v] of Object.entries(stored)) {
    if (k in DEFAULTS || ENCRYPTED_FIELDS.includes(k)) {
      merged[k] = ENCRYPTED_FIELDS.includes(k) ? decrypt(v) : v;
    }
  }
  // Seed from env if settings file has no credentials yet
  if (!merged.routerPass) merged.routerPass = process.env.ROUTER_PASS || '';
  if (!merged.dashPass)   merged.dashPass   = process.env.BASIC_AUTH_PASS || '';

  _cache = merged;
  return _cache;
}

function save(updates) {
  _ensureDataDir();
  const current = load();
  const next = { ...current, ...updates };
  _cache = next;

  // Encrypt sensitive fields before writing
  const toWrite = { ...next };
  for (const f of ENCRYPTED_FIELDS) {
    toWrite[f] = encrypt(next[f] || '');
  }
  fs.writeFileSync(SETTINGS_FILE, JSON.stringify(toWrite, null, 2), 'utf8');
  return next;
}

/** Returns settings safe to send to the browser — credentials masked */
function getPublic() {
  const s = load();
  const out = { ...s };
  for (const f of CREDENTIAL_FIELDS) {
    out[f] = s[f] ? '••••••••' : '';
  }
  return out;
}

/** Returns true if the value is the mask sentinel */
function isMasked(v) { return v === '••••••••'; }

module.exports = { load, save, getPublic, isMasked, DEFAULTS };
