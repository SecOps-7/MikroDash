// Per-user notification channels (issue #109).
//
// The headline case is the one the issue calls out as worth failing loudly:
// grant a user one router, raise an alert on another, and assert nothing is
// delivered to them. Push delivery that ignored grants would be an
// access-control bypass — a phone notification naming a router the UI
// deliberately hides — so it is pinned here at the resolver, where the decision
// is actually made.
//
// The second thing worth pinning is *when* that decision happens. It is made at
// send time, not subscribe time, so revoking a grant stops delivery on the very
// next alert with no cache to invalidate. A test that only ever asks once
// cannot tell those two designs apart, so one here asks twice across a
// revocation.

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const os       = require('node:os');
const path     = require('node:path');

process.env.DATA_DIR    = fs.mkdtempSync(path.join(os.tmpdir(), 'user-notify-'));
process.env.DATA_SECRET = 'test-secret-for-user-notify';

const db         = require('../src/db');
const Routers    = require('../src/routers');
const rbac       = require('../src/rbac');
const Settings   = require('../src/settings');
const userNotify = require('../src/userNotify');

db.open();
let _modern = true;
rbac.init({ isModern: () => _modern });

const SITE = 'site-berlin';
const rA = Routers.add({ label: 'Router A', host: '10.0.0.1' });
const rB = Routers.add({ label: 'Router B', host: '10.0.0.2' });
Routers.update(rA.id, { siteId: SITE });

// A fully usable Telegram channel — recipientsFor() drops anyone whose config
// could not actually deliver, so every access test needs one of these or it
// would pass for the wrong reason.
const WORKING_CHANNEL = { telegramEnabled: true, telegramBotToken: 'bot-token', telegramChatId: '42' };

function reset() {
  for (const g of db.listGrants()) db.deleteGrant(g.id);
  for (const gr of db.listGroups()) db.deleteGroup(gr.id);
  for (const row of db.listUserNotifyConfigs()) db.deleteUserNotifyConfig(row.userId);
  rbac.bump();
}

function grant(principalId, scopeType, scopeId = '', principalType = 'user') {
  db.upsertGrant({ principalType, principalId, role: 'viewer', roleId: 'readonly', scopeType, scopeId });
  rbac.bump();
}

const ids = (routerId) => userNotify.recipientsFor(routerId).map(r => r.id);

// ── The invariant ────────────────────────────────────────────────────────────

test('a user is never a recipient for a router they cannot read', () => {
  reset();
  userNotify.save('u1', WORKING_CHANNEL);
  grant('u1', 'router', rA.id);

  assert.deepEqual(ids(rA.id), ['user:u1'], 'the granted router must reach them');
  assert.deepEqual(ids(rB.id), [],
    'a router they hold no grant on must not reach them — this is the access-control bypass');
});

test('revoking a grant stops delivery on the next alert, with nothing to invalidate', () => {
  reset();
  userNotify.save('u1', WORKING_CHANNEL);
  grant('u1', 'router', rA.id);
  assert.deepEqual(ids(rA.id), ['user:u1']);

  for (const g of db.listGrants()) db.deleteGrant(g.id);
  rbac.bump();
  assert.deepEqual(ids(rA.id), [],
    'resolution must happen at send time — a subscription captured earlier would still fire here');
});

test('a site grant covers the routers in it and nothing else', () => {
  reset();
  userNotify.save('u1', WORKING_CHANNEL);
  grant('u1', 'site', SITE);
  assert.deepEqual(ids(rA.id), ['user:u1'], 'router A is in the site');
  assert.deepEqual(ids(rB.id), [], 'router B is in no site, so a site grant must not reach it');
});

test('a global grant covers every router', () => {
  reset();
  userNotify.save('u1', WORKING_CHANNEL);
  grant('u1', 'global');
  assert.deepEqual(ids(rA.id), ['user:u1']);
  assert.deepEqual(ids(rB.id), ['user:u1']);
});

test('a grant held through a group counts, exactly as it does everywhere else', () => {
  reset();
  userNotify.save('u1', WORKING_CHANNEL);
  const g = db.createGroup({ name: 'NOC' });
  db.setGroupMembers(g.id, ['u1']);
  grant(g.id, 'router', rA.id, 'group');
  assert.deepEqual(ids(rA.id), ['user:u1'],
    'the legacy allowedRouterIds field cannot express this, which is why Rbac.can is asked');
});

// ── Who is eligible at all ───────────────────────────────────────────────────

test('access without a usable channel is not a recipient', () => {
  reset();
  grant('u1', 'global');
  userNotify.save('u1', { telegramEnabled: true });   // enabled but no token or chat id
  assert.deepEqual(ids(rA.id), [],
    'an unusable channel must not become a recipient — it would consume a cooldown and send nothing');
});

test('a user who never configured anything costs the fan-out nothing', () => {
  reset();
  grant('u1', 'global');
  assert.deepEqual(ids(rA.id), []);
  assert.equal(db.listUserNotifyConfigs().length, 0, 'no row is written until the user saves');
});

test('no router id resolves to no recipients rather than everyone', () => {
  reset();
  userNotify.save('u1', WORKING_CHANNEL);
  grant('u1', 'global');
  assert.deepEqual(userNotify.recipientsFor(''), []);
  assert.deepEqual(userNotify.recipientsFor(null), []);
});

// ── Credential handling ──────────────────────────────────────────────────────

test('credentials are encrypted at rest and masked to the browser', () => {
  reset();
  userNotify.save('u1', { telegramEnabled: true, telegramBotToken: 'super-secret', telegramChatId: '42' });

  const stored = db.getUserNotifyConfig('u1');
  assert.notEqual(stored.telegramBotToken, 'super-secret', 'the plaintext must never be at rest');
  assert.ok(stored.telegramBotToken.length > 0);

  assert.equal(userNotify.load('u1').telegramBotToken, 'super-secret',
    'the delivery path needs the real value');
  assert.equal(userNotify.getPublic('u1').telegramBotToken, '••••••••',
    'the browser gets the mask');
  assert.equal(userNotify.getPublic('u1').pushbulletApiKey, '',
    'an unset credential reads as empty, so the client can tell "not configured" from "hidden"');
});

test('an unrelated edit leaves a stored credential untouched', () => {
  reset();
  userNotify.save('u1', { telegramEnabled: true, telegramBotToken: 'super-secret', telegramChatId: '42' });
  userNotify.save('u1', { telegramChatId: '99' });
  assert.equal(userNotify.load('u1').telegramBotToken, 'super-secret');
  assert.equal(userNotify.load('u1').telegramChatId, '99');
});

test('the mask sentinel is never written back as a real credential', () => {
  reset();
  userNotify.save('u1', { telegramBotToken: 'super-secret' });
  userNotify.save('u1', { telegramBotToken: '••••••••' });
  assert.equal(userNotify.load('u1').telegramBotToken, 'super-secret',
    'posting the mask back must mean "unchanged", never a password of eight bullets');
});

test('an empty string clears a credential', () => {
  reset();
  userNotify.save('u1', { telegramBotToken: 'super-secret' });
  userNotify.save('u1', { telegramBotToken: '' });
  assert.equal(userNotify.load('u1').telegramBotToken, '');
});

// ── Defaults and validation ──────────────────────────────────────────────────

test('a user cannot choose which alert types exist', () => {
  // Per-user alert-type and interface-type toggles were removed deliberately:
  // which alerts are raised is one decision, made once by an administrator and
  // enforced in alerter.js. A per-user copy would be a second, weaker answer to
  // a question already settled, and it let an end user widen their own alerting.
  reset();
  userNotify.save('u1', { telegramEnabled: true, notifCpu: true, notifIfaceWlan: true });
  const cfg = userNotify.load('u1');
  for (const k of ['notifCpu', 'notifIfaceWlan', 'notifVpn', 'notifBgp']) {
    assert.equal(cfg[k], undefined, k + ' must not be storable per user');
  }
  assert.equal(userNotify.TYPE_TOGGLES, undefined, 'TYPE_TOGGLES should no longer be exported');
});

test('a user cannot configure the mail server, only opt in to it', () => {
  // The install owns the transport. Accepting these per user would both invite a
  // support burden and copy the server's credentials into per-user rows.
  reset();
  userNotify.save('u1', {
    emailEnabled: true, emailTo: 'me@example.com',
    smtpHost: 'evil.example.com', smtpPort: 2525, smtpUser: 'x', smtpPass: 'y', smtpFrom: 'spoof@example.com',
  });
  const stored = db.getUserNotifyConfig('u1');
  for (const k of ['smtpHost', 'smtpPort', 'smtpUser', 'smtpPass', 'smtpFrom']) {
    assert.equal(stored[k], undefined, k + ' must never be stored per user');
  }
  assert.equal(stored.emailTo, 'me@example.com');
  assert.equal(stored.emailEnabled, true);
});

test('an opted-in user is handed the install mail server with their own address', () => {
  reset();
  Settings.save({ smtpHost: 'mail.example.com', smtpPort: 587, smtpFrom: 'mikrodash@example.com' });
  userNotify.save('u1', { emailEnabled: true, emailTo: 'me@example.com' });

  const cfg = userNotify.load('u1');
  assert.equal(cfg.smtpEnabled, true, 'the channel must be usable by notifier.send()');
  assert.equal(cfg.smtpHost, 'mail.example.com', 'transport comes from the install');
  assert.equal(cfg.smtpFrom, 'mikrodash@example.com');
  assert.equal(cfg.smtpTo, 'me@example.com', 'and the destination from the user');
});

test('opting in with no mail server configured resolves to no channel', () => {
  reset();
  Settings.save({ smtpHost: '', smtpFrom: '' });
  userNotify.save('u1', { emailEnabled: true, emailTo: 'me@example.com' });
  const cfg = userNotify.load('u1');
  assert.notEqual(cfg.smtpEnabled, true,
    'a half-configured channel would consume a cooldown and send nothing');
  grant('u1', 'global');
  assert.deepEqual(ids(rA.id), [], 'and must not make the user a recipient');
});

test('a malformed email address is rejected rather than silently never arriving', () => {
  reset();
  assert.throws(() => userNotify.save('u1', { emailTo: 'not-an-address' }), /email address/i);
});

test('channels start off', () => {
  reset();
  const cfg = userNotify.load('nobody');
  for (const k of userNotify.CHANNEL_TOGGLES) assert.equal(cfg[k], false);
});

test('unknown keys cannot be smuggled into what reaches the notifier', () => {
  reset();
  userNotify.save('u1', { telegramEnabled: true, telegramChatId: '42', evilField: 'nope' });
  const cfg = userNotify.load('u1');
  assert.equal(cfg.evilField, undefined, 'only allowlisted keys survive a save');
  assert.equal(cfg.telegramChatId, '42', 'while allowlisted ones do');
});

test('a corrupt blob reads as not-configured instead of throwing mid-alert', () => {
  reset();
  db.setUserNotifyConfig('u1', { telegramEnabled: true });
  // Simulate a hand-edited or truncated row.
  const raw = require('better-sqlite3')(path.join(process.env.DATA_DIR, 'mikrodash.db'));
  raw.prepare('UPDATE user_notify_config SET data = ? WHERE user_id = ?').run('{not json', 'u1');
  raw.close();
  assert.equal(db.getUserNotifyConfig('u1'), null);
  assert.deepEqual(db.listUserNotifyConfigs(), [], 'a corrupt row is skipped, not fatal');
});

// ── authMode 'none' ──────────────────────────────────────────────────────────

test("in authMode 'none' a saved config still delivers, because everyone is admin", () => {
  reset();
  userNotify.save('u1', WORKING_CHANNEL);
  _modern = false;
  rbac.bump();
  try {
    assert.deepEqual(ids(rB.id), ['user:u1'],
      "'none' means every request is implicitly admin, so a config saved earlier keeps working");
  } finally {
    _modern = true;
    rbac.bump();
  }
});

// ── An ntfy failure has to say WHY ──────────────────────────────────────────
//
// notifier.js has a `_reason()` helper whose own comment names ntfy as one of
// the two channels that return a human explanation: "without it a failure reads
// as a bare status code". Telegram and Pushbullet go through _httpsPost and get
// it; sendNtfy had its own request and did not, even though it had already
// buffered the body two lines before throwing it away.
//
// Reachable on any misconfigured topic: a token-protected one answers 403 with
// a body saying authentication is required, and the operator saw `HTTP 403`.
//
// Driven over a real loopback server rather than a source scan, because
// sendNtfy picks http or https off the URL scheme, so the whole path runs.
const http = require('node:http');
const Notifier = require('../src/notifier');

function _ntfyServer(status, payload) {
  const srv = http.createServer((req, res) => {
    res.writeHead(status, { 'Content-Type': 'application/json' });
    res.end(payload);
  });
  return new Promise((resolve) => {
    srv.listen(0, '127.0.0.1', () => resolve({
      srv,
      url: 'http://127.0.0.1:' + srv.address().port + '/alerts',
      close: () => new Promise((r) => srv.close(r)),
    }));
  });
}

test('an ntfy rejection carries the server\'s explanation, not just a status', async () => {
  const s = await _ntfyServer(403, '{"code":40301,"http":403,"error":"unauthorized"}');
  try {
    await assert.rejects(
      () => Notifier.testChannel({ ntfyUrl: s.url }, 'ntfy'),
      (e) => {
        assert.match(e.message, /403/, 'the status must survive');
        assert.match(e.message, /unauthorized/,
          'the reason the server gave must reach the operator, got: ' + e.message);
        return true;
      });
  } finally { await s.close(); }
});

test('a body with no recognisable reason still reports the status alone', async () => {
  // _reason returns '' for an empty body, so the message must not gain a
  // dangling separator with nothing after it.
  const s = await _ntfyServer(500, '');
  try {
    await assert.rejects(
      () => Notifier.testChannel({ ntfyUrl: s.url }, 'ntfy'),
      (e) => {
        assert.match(e.message, /^HTTP 500$/, 'got: ' + e.message);
        return true;
      });
  } finally { await s.close(); }
});

test('a long error page cannot flood the log', async () => {
  // _reason truncates at 160 characters and collapses whitespace, which is why
  // handing it an arbitrary body is safe.
  const s = await _ntfyServer(400, '<html>\n' + 'x'.repeat(5000) + '</html>');
  try {
    await assert.rejects(
      () => Notifier.testChannel({ ntfyUrl: s.url }, 'ntfy'),
      (e) => {
        assert.ok(e.message.length < 300, 'message was ' + e.message.length + ' chars');
        return true;
      });
  } finally { await s.close(); }
});
