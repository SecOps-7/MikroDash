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

let _cachedKey = null;
function _deriveKey() {
  if (!_cachedKey) _cachedKey = crypto.scryptSync(_loadOrCreateSecret(), SALT, 32);
  return _cachedKey;
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
  } catch (e) {
    console.warn('[settings] AES-GCM auth tag failure — credential may be corrupt or key changed');
    return '';
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

  // Auth mode and session management
  authMode:         'modern',  // 'none' | 'modern'
  sessionTimeoutMs: 3600000,   // ms; 0 = never expire; max 86400000 (24h)

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

  // Notification channels
  telegramEnabled:   false,
  telegramBotToken:  '', // stored encrypted
  telegramChatId:    '',
  pushbulletEnabled: false,
  pushbulletApiKey:  '', // stored encrypted

  // SMTP email channel
  smtpEnabled:  false,
  smtpHost:     '',
  smtpPort:     587,
  smtpSecure:   false,   // true = implicit TLS (port 465); false = STARTTLS (port 587)
  smtpUser:     '',      // stored encrypted
  smtpPass:     '',      // stored encrypted
  smtpFrom:     '',
  smtpTo:       '',

  // ntfy channel
  ntfyEnabled: false,
  ntfyUrl:     '',
  ntfyToken:   '', // stored encrypted

  // Alert type toggles (server-persisted; drive both browser + push notifications)
  notifIfaceUpDown:  true,
  notifVpn:          true,
  notifCpu:          true,
  notifPing:         true,
  notifNetwatch:     false,
  notifRouterStatus: false,

  // Interface type filter for up/down alerts
  notifIfaceEther:   true,
  notifIfaceWlan:    true,
  notifIfaceBridge:  false,
  notifIfaceVlan:    false,
  notifIfaceOther:   false,

  // Notification message templates
  notifTitle:        'MikroDash Alert',
  notifBody:         '⚠️ {{alertType}} on {{routerName}}: {{detail}}',
  notifBodyUp:       '✅ {{alertType}} on {{routerName}}: {{detail}}',

  // Minimum seconds between repeated alerts for the same subject
  notifCooldownSec:  60,

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

  // Collection method — true = stream (default), false = poll
  streamSystem:  true,
  streamPing:    true,
  streamConns:   true,
  streamTalkers: true,
  streamIfrates: true,

  // Database retention
  dbRetentionDays:      90,  // days to keep ping + traffic samples
  dbAlertRetentionDays: 365, // days to keep alert + connectivity events

  // Display timezone — IANA name (e.g. 'Europe/London'). Empty = server/browser local.
  displayTimezone: '',
};

// Fields stored encrypted in JSON
const ENCRYPTED_FIELDS = ['routerPass', 'telegramBotToken', 'pushbulletApiKey', 'smtpUser', 'smtpPass', 'ntfyToken'];
// Fields never sent to the client (only their masked presence)
const CREDENTIAL_FIELDS = ['routerPass', 'telegramBotToken', 'pushbulletApiKey', 'smtpUser', 'smtpPass', 'ntfyToken'];

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

  // Clamp poll intervals to their valid ranges so that a corrupt or manually
  // edited settings file can never produce a sub-minimum timer delay.
  const _POLL_BOUNDS = {
    pollConns:[500,60000], pollTalkers:[500,60000], pollBandwidth:[500,60000],
    pollRouting:[500,300000], pollSystem:[500,60000], pollWireless:[500,60000],
    pollVpn:[500,30000], pollFirewall:[500,30000], pollIfstatus:[500,60000],
    pollIfaces:[10000,600000], pollPing:[1000,5000], pollArp:[5000,300000],
    pollDhcp:[5000,600000],
  };
  for (const [k, [lo, hi]] of Object.entries(_POLL_BOUNDS)) {
    if (typeof merged[k] === 'number') merged[k] = Math.max(lo, Math.min(hi, merged[k]));
  }

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
  const tmp = SETTINGS_FILE + '.tmp';
  // mode 0o600 — file holds encrypted credentials; keep it owner-only.
  fs.writeFileSync(tmp, JSON.stringify(toWrite, null, 2), { encoding: 'utf8', mode: 0o600 });
  fs.renameSync(tmp, SETTINGS_FILE);
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

// Non-sensitive fields a viewer-role user may read so the dashboard renders.
// Excludes router connection details, auth config, and notification-channel
// targets (host/chat IDs/SMTP/ntfy) — those are admin-only recon.
const VIEWER_FIELDS = [
  'authMode',
  'pingEnabled', 'pingTarget',
  'topN', 'topTalkersN', 'firewallTopN', 'vpnDashTopN', 'maxConns', 'historyMinutes',
  'alertCpuThreshold', 'alertPingLoss',
  'activeRouterId',
  'pageWireless', 'pageInterfaces', 'pageDhcp', 'pageVpn', 'pageConnections',
  'pageFirewall', 'pageLogs', 'pageBandwidth', 'pageRouting',
  'streamSystem', 'streamPing', 'streamConns', 'streamTalkers', 'streamIfrates',
  'displayTimezone',
];

/** Returns the viewer-safe subset of settings (no credentials, no admin-only config). */
function getViewerPublic() {
  const s = load();
  const out = {};
  for (const f of VIEWER_FIELDS) out[f] = s[f];
  return out;
}

module.exports = { load, save, getPublic, getViewerPublic, isMasked, DEFAULTS };
