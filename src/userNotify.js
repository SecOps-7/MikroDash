'use strict';
/**
 * Per-user notification channels (issue #109).
 *
 * Notification channels used to be install-wide: one Telegram token, one ntfy
 * topic, one destination for everybody. Since #78/#108 different users can see
 * different routers, so a single destination means every alert goes to one
 * inbox regardless of who was meant to see the router that raised it.
 *
 * This module owns a user's own channel config and, more importantly, answers
 * the question the alerter asks on every send: *who* should receive this alert
 * for this router.
 *
 * THE INVARIANT: a user must never be notified about a router they cannot read.
 * Push delivery would otherwise be an access-control bypass — a Read Only user
 * scoped to one site would learn a router's name from a phone notification
 * while the UI correctly hides it. recipientsFor() therefore asks
 * Rbac.can(..., 'router:read', routerId) at SEND time, not at subscribe time,
 * so revoking a grant stops delivery on the very next alert with nothing to
 * invalidate.
 *
 * Authorization is asked, never reimplemented: Rbac.can is the only place a
 * permission decision is made, and iterating users through it is what keeps it
 * that way. A reverse "which users hold a grant covering this router" query
 * would be faster and would be a second answer to the same question.
 */

const db       = require('./db');
const Settings = require('./settings');
const Rbac     = require('./rbac');
const notifier = require('./notifier');

// A user chooses WHERE their alerts go. They do not choose WHICH alerts exist —
// that is the install's decision, made once by an administrator and enforced in
// alerter.js before anything is recorded or sent. So there are no alert-type or
// interface-type fields here: a per-user copy of them would be a second, weaker
// answer to a question the install has already settled.
//
// Field names mirror the install-wide settings where they overlap, so a stored
// config can be handed straight to notifier.send()/hasConfiguredChannel(). Both
// are stateless and inspect field names, so neither needs to know a user exists.
const CHANNEL_TOGGLES   = ['telegramEnabled', 'pushbulletEnabled', 'ntfyEnabled', 'emailEnabled'];
const CREDENTIAL_FIELDS = ['telegramBotToken', 'pushbulletApiKey', 'ntfyToken'];
const STR_FIELDS        = ['telegramChatId', 'ntfyUrl', 'emailTo'];

// Email is the one channel a user does not configure, only opts into. A mail
// server is install infrastructure — host, port, credentials and From belong to
// the administrator, and asking every user to retype them would be both a
// support burden and a way to copy the server's credentials into per-user rows.
// The user supplies the one part that is genuinely theirs: where to send it.
const DEFAULTS = (() => {
  const d = {};
  for (const k of CHANNEL_TOGGLES)   d[k] = false;
  for (const k of CREDENTIAL_FIELDS) d[k] = '';
  for (const k of STR_FIELDS)        d[k] = '';
  return d;
})();

const MAX_STR = 256;
const MAX_CRED = 512;

/** Mirrors the guard in alerter.js: notifier is replaced through require.cache
 *  in several test files with a stub carrying only send(), so asking it this
 *  question has to degrade to the field check rather than throwing. */
function _hasChannel(s) {
  if (!s) return false;
  if (typeof notifier.hasConfiguredChannel === 'function') return !!notifier.hasConfiguredChannel(s);
  return !!(s.telegramEnabled || s.pushbulletEnabled || s.smtpEnabled || s.ntfyEnabled);
}

/** Raw stored blob — credentials still ciphertext. */
function _raw(userId) {
  return db.getUserNotifyConfig(userId) || {};
}

/** Only keys on the allowlist survive a read, so a blob written by a newer
 *  version (or hand-edited) cannot inject fields into what reaches notifier. */
function _pick(stored, decryptCreds) {
  const out = { ...DEFAULTS };
  for (const k of Object.keys(stored)) {
    if (!(k in DEFAULTS)) continue;
    out[k] = (decryptCreds && CREDENTIAL_FIELDS.includes(k)) ? Settings.decrypt(stored[k]) : stored[k];
  }
  return out;
}

/**
 * Fold the install's mail server into a user's opt-in.
 *
 * notifier.send() only knows smtp* fields, so an opted-in user is handed the
 * install's transport with their own address as the recipient. Nothing about the
 * server is stored per user — it is read fresh here, so changing the install's
 * SMTP settings takes effect for everyone at once and no stale copy can linger
 * in a row.
 *
 * If the install has no usable mail server the opt-in resolves to nothing rather
 * than a half-configured channel that would consume a cooldown and send nothing.
 */
function _withInstallMail(own) {
  const out = { ...own };
  if (!own.emailEnabled || !own.emailTo) return out;
  const s = Settings.load();
  if (!s.smtpHost || !s.smtpFrom) return out;
  out.smtpEnabled = true;
  out.smtpHost    = s.smtpHost;
  out.smtpPort    = s.smtpPort;
  out.smtpSecure  = s.smtpSecure;
  out.smtpUser    = s.smtpUser;
  out.smtpPass    = s.smtpPass;
  out.smtpFrom    = s.smtpFrom;
  out.smtpTo      = own.emailTo;
  return out;
}

/**
 * A user's config with credentials decrypted, merged over DEFAULTS, and the
 * install's mail transport folded in. Shaped for notifier.send() — this is what
 * a recipient record carries.
 */
function load(userId) {
  return _withInstallMail(_pick(_raw(userId), true));
}

/** Browser-facing shape: credentials replaced by the mask if set, '' if not —
 *  the same convention Settings.getPublic() uses, so the client's existing
 *  "leave blank to keep current" handling works unchanged. */
function getPublic(userId) {
  const stored = _raw(userId);
  const out = _pick(stored, false);
  for (const k of CREDENTIAL_FIELDS) out[k] = stored[k] ? '••••••••' : '';
  return out;
}

/**
 * Merge validated updates into a user's config.
 *
 * Merges over the *stored* blob, so credentials the caller did not send keep
 * their existing ciphertext and are never decrypted and re-encrypted just to
 * survive an unrelated edit. A masked value is treated as "unchanged", matching
 * POST /api/settings — the browser omits untouched credentials entirely, and
 * this is the belt to that braces.
 */
function save(userId, updates) {
  if (!userId) return getPublic(userId);
  const next = { ..._raw(userId) };
  const u = updates || {};

  for (const k of CHANNEL_TOGGLES) {
    if (k in u) next[k] = (u[k] === true || u[k] === 'true');
  }
  for (const k of STR_FIELDS) {
    if (k in u) next[k] = String(u[k] == null ? '' : u[k]).trim().slice(0, MAX_STR);
  }
  // An address with no @ cannot be a mailbox. Rejecting it here means a typo
  // fails at the moment it is made rather than silently never arriving.
  if (next.emailTo && next.emailTo.indexOf('@') === -1) {
    throw new Error('That does not look like an email address');
  }
  for (const k of CREDENTIAL_FIELDS) {
    if (!(k in u)) continue;
    if (Settings.isMasked(u[k])) continue;       // unchanged — keep stored ciphertext
    const v = String(u[k] == null ? '' : u[k]).slice(0, MAX_CRED);
    next[k] = v ? Settings.encrypt(v) : '';      // empty clears the credential
  }

  db.setUserNotifyConfig(userId, next);
  return getPublic(userId);
}

/**
 * Every user who should receive an alert about this router.
 *
 * Three filters, cheapest first:
 *   1. only users who actually saved a config — someone who never opened the
 *      panel has no row, so on most installs this is an empty query per alert,
 *   2. only those with a usable channel: the same predicate notifier.send()
 *      applies, asked here so an unusable recipient never consumes a cooldown,
 *   3. only those Rbac says may read this router. This is the invariant, and it
 *      is the last word.
 *
 * Returns recipient records the alerter treats uniformly with the install-wide
 * destination: { id, settings }.
 */
function recipientsFor(routerId) {
  if (!routerId) return [];
  const out = [];
  for (const row of db.listUserNotifyConfigs()) {
    if (!row || !row.userId) continue;
    const settings = load(row.userId);
    if (!_hasChannel(settings)) continue;
    if (!Rbac.can({ userId: row.userId }, 'router:read', routerId)) continue;
    out.push({ id: 'user:' + row.userId, settings });
  }
  return out;
}

module.exports = {
  load, save, getPublic, recipientsFor,
  DEFAULTS, CHANNEL_TOGGLES, CREDENTIAL_FIELDS, STR_FIELDS,
};
