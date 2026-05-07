/**
 * Settings store — persists to /data/settings.json (Docker volume mount).
 * Credentials are AES-256-GCM encrypted at rest using a key derived from a
 * stable secret. Secret priority:
 *   1. DATA_SECRET env var (explicit override — useful for key rotation)
 *   2. /data/.secret file (auto-generated on first run, survives restarts)
 */

'use strict';
const fs     = require('fs');
const path   = require('path');
const crypto = require('crypto');

const DATA_DIR      = process.env.DATA_DIR || '/data';
const SETTINGS_FILE = path.join(DATA_DIR, 'settings.json');

// ── Encryption helpers ───────────────────────────────────────────────────────
const SALT = 'mikrodash-settings-v1'; // fixed salt — uniqueness via secret

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

function _deriveKey() {
  return crypto.scryptSync(_loadOrCreateSecret(), SALT, 32);
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

  // Dashboard auth (managed via Settings UI — not env-driven)
  dashUser:          '',
  dashPass:          '', // stored encrypted

  // Ping
  pingEnabled:       true,
  pingTarget:        process.env.PING_TARGET         || '1.1.1.1',

  // Poll intervals (ms)
  pollConns:         parseInt(process.env.CONNS_POLL_MS     || '5000',  10),
  pollTalkers:       parseInt(process.env.TALKERS_POLL_MS   || '3000',  10),
  pollBandwidth:     parseInt(process.env.BANDWIDTH_POLL_MS  || '5000',  10),
  pollRouting:       parseInt(process.env.ROUTING_POLL_MS    || '10000', 10),
  pageRouting:       true,
  pollSystem:        parseInt(process.env.SYSTEM_POLL_MS    || '2000',  10),
  pollWireless:      parseInt(process.env.WIRELESS_POLL_MS  || '30000', 10),
  pollVpn:           parseInt(process.env.VPN_POLL_MS       || '10000', 10),
  pollFirewall:      parseInt(process.env.FIREWALL_POLL_MS  || '5000',  10),
  pollIfstatus:      parseInt(process.env.IFSTATUS_POLL_MS  || '5000',  10),
  pollIfaces:        parseInt(process.env.IFACES_POLL_MS    || '60000', 10),
  pollPing:          parseInt(process.env.PING_POLL_MS      || '5000',  10),
  pollArp:           parseInt(process.env.ARP_POLL_MS       || '30000', 10),
  pollDhcp:          parseInt(process.env.DHCP_POLL_MS      || '600000', 10),

  // Limits
  topN:              parseInt(process.env.TOP_N             || '5',     10),
  topTalkersN:       parseInt(process.env.TOP_TALKERS_N     || '5',     10),
  firewallTopN:      parseInt(process.env.FIREWALL_TOP_N    || '15',    10),
  vpnDashTopN:       parseInt(process.env.VPN_DASH_TOP_N    || '5',     10),
  maxConns:          parseInt(process.env.MAX_CONNS         || '20000', 10),
  historyMinutes:    parseInt(process.env.HISTORY_MINUTES   || '30',    10),

  // Alert thresholds
  alertCpuThreshold: parseInt(process.env.ALERT_CPU_THRESHOLD || '90',  10), // % — trigger CPU spike notification
  alertPingLoss:     parseInt(process.env.ALERT_PING_LOSS     || '100', 10), // % — trigger ping loss notification

  // Diagnostics
  rosDebug:          (process.env.ROS_DEBUG || 'false').toLowerCase() === 'true',

  // Active router (managed by routers.js / router switcher)
  activeRouterId:  '',

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

// ── Env-var override map ─────────────────────────────────────────────────────
// For each settings field that has an env var backing, map field → [envVar, parser].
// After merging settings.json, any env var that is explicitly set in process.env
// takes priority — env is the authoritative layer for infrastructure-level config
// and must not be silently overridden by a persisted settings.json value.
const ENV_MAP = {
  routerHost:        ['ROUTER_HOST',         v => v],
  routerPort:        ['ROUTER_PORT',          v => parseInt(v, 10)],
  routerTls:         ['ROUTER_TLS',           v => v.toLowerCase() === 'true'],
  routerTlsInsecure: ['ROUTER_TLS_INSECURE',  v => v.toLowerCase() === 'true'],
  routerUser:        ['ROUTER_USER',          v => v],
  defaultIf:         ['DEFAULT_IF',           v => v],
  pingTarget:        ['PING_TARGET',          v => v],
  pollConns:         ['CONNS_POLL_MS',        v => parseInt(v, 10)],
  pollTalkers:       ['TALKERS_POLL_MS',      v => parseInt(v, 10)],
  pollBandwidth:     ['BANDWIDTH_POLL_MS',    v => parseInt(v, 10)],
  pollRouting:       ['ROUTING_POLL_MS',      v => parseInt(v, 10)],
  pollSystem:        ['SYSTEM_POLL_MS',       v => parseInt(v, 10)],
  pollWireless:      ['WIRELESS_POLL_MS',     v => parseInt(v, 10)],
  pollVpn:           ['VPN_POLL_MS',          v => parseInt(v, 10)],
  pollFirewall:      ['FIREWALL_POLL_MS',     v => parseInt(v, 10)],
  pollIfstatus:      ['IFSTATUS_POLL_MS',     v => parseInt(v, 10)],
  pollIfaces:        ['IFACES_POLL_MS',       v => parseInt(v, 10)],
  pollPing:          ['PING_POLL_MS',         v => parseInt(v, 10)],
  pollArp:           ['ARP_POLL_MS',          v => parseInt(v, 10)],
  pollDhcp:          ['DHCP_POLL_MS',         v => parseInt(v, 10)],
  topN:              ['TOP_N',                v => parseInt(v, 10)],
  topTalkersN:       ['TOP_TALKERS_N',        v => parseInt(v, 10)],
  firewallTopN:      ['FIREWALL_TOP_N',       v => parseInt(v, 10)],
  vpnDashTopN:       ['VPN_DASH_TOP_N',       v => parseInt(v, 10)],
  maxConns:          ['MAX_CONNS',            v => parseInt(v, 10)],
  historyMinutes:    ['HISTORY_MINUTES',      v => parseInt(v, 10)],
  alertCpuThreshold: ['ALERT_CPU_THRESHOLD',  v => parseInt(v, 10)],
  alertPingLoss:     ['ALERT_PING_LOSS',      v => parseInt(v, 10)],
  rosDebug:          ['ROS_DEBUG',            v => v.toLowerCase() === 'true'],
};

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
  // Re-apply any env var that is explicitly set — env always wins over settings.json.
  // This ensures that changes to the .env file take effect on restart even when
  // a settings.json already exists on the volume.
  for (const [field, [envVar, parse]] of Object.entries(ENV_MAP)) {
    if (process.env[envVar] !== undefined) merged[field] = parse(process.env[envVar]);
  }
  // Router password: env always wins if present.
  if (process.env.ROUTER_PASS !== undefined) merged.routerPass = process.env.ROUTER_PASS;
  else if (!merged.routerPass)              merged.routerPass = '';

  // Basic Auth migration: if settings.json has no dashUser/dashPass yet but
  // BASIC_AUTH_USER/PASS env vars are set, seed them once so existing deployments
  // that previously used env-based auth continue to work after upgrade.
  if (!merged.dashUser && process.env.BASIC_AUTH_USER) merged.dashUser = process.env.BASIC_AUTH_USER;
  if (!merged.dashPass && process.env.BASIC_AUTH_PASS) merged.dashPass = process.env.BASIC_AUTH_PASS;

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
  // Keep process.env in sync so library patches (node-routeros) pick up the change
  if ('rosDebug' in updates) process.env.ROS_DEBUG = next.rosDebug ? 'true' : 'false';
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
