'use strict';
// The audit trail: redaction, the it-cannot-be-erased invariant, read gating,
// and a coverage guard that keeps "every write action is recorded" true as
// routes are added.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs   = require('fs');
const os   = require('os');
const path = require('path');

// db.js resolves DATA_DIR at require time, so point it at a temp dir first.
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-audit-'));
process.env.DATA_DIR = TMP;
const db    = require('../src/db');
const audit = require('../src/audit');

db.open();

const INDEX_JS = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');

// ── Redaction ────────────────────────────────────────────────────────────────

test('a credential value never reaches the trail, in either direction', () => {
  // The whole point of putting redaction in one place. Every credential field
  // settings.js knows about, plus shapes it does not: a router password from
  // routers.json, a user password, an arbitrary token.
  const before = {
    routerPass: 'old-router-pw', telegramBotToken: 'old-tg', pushbulletApiKey: 'old-pb',
    smtpPass: 'old-smtp', ntfyToken: 'old-ntfy', password: 'old-user-pw',
    apiKey: 'old-key', somePassphrase: 'old-phrase', pollDns: 10000,
  };
  const after = {
    routerPass: 'NEW-ROUTER-PW', telegramBotToken: 'NEW-TG', pushbulletApiKey: 'NEW-PB',
    smtpPass: 'NEW-SMTP', ntfyToken: 'NEW-NTFY', password: 'NEW-USER-PW',
    apiKey: 'NEW-KEY', somePassphrase: 'NEW-PHRASE', pollDns: 30000,
  };
  const serialised = JSON.stringify(audit.diff(before, after));

  for (const secret of Object.values(before).concat(Object.values(after))) {
    if (typeof secret !== 'string') continue;
    assert.ok(!serialised.includes(secret), 'value leaked into the trail: ' + secret);
  }
  // The non-credential field is recorded in full — redaction must not be so
  // broad that the log stops being useful.
  assert.ok(serialised.includes('10000') && serialised.includes('30000'));
});

test('redaction distinguishes set, unset and changed', () => {
  const [a] = audit.diff({ smtpPass: '' },  { smtpPass: 'x' });
  const [b] = audit.diff({ smtpPass: 'x' }, { smtpPass: '' });
  assert.deepStrictEqual([a.from, a.to], [audit.UNSET, audit.CHANGED], 'newly set');
  assert.deepStrictEqual([b.from, b.to], [audit.SET, audit.UNSET],     'cleared');
  // "Was a password set before this?" is a real question a trail should answer,
  // and it is answerable without recording the password.
});

test('unchanged fields are omitted, so a partial save is not reported as a rewrite', () => {
  // Every settings POST is partial. Diffing the whole DEFAULTS against it would
  // report dozens of untouched fields on every save and bury the real change.
  assert.deepStrictEqual(audit.diff({ a: 1, b: 2, c: 3 }, { a: 1 }), []);
  assert.deepStrictEqual(audit.diff({ list: [1, 2] }, { list: [1, 2] }), []);
  assert.strictEqual(audit.diff({ list: [1, 2] }, { list: [1, 3] }).length, 1);
});

test('one oversized field cannot turn a row into a document', () => {
  // The audit table is the one store that cannot be pruned selectively, so a
  // dashboard layout pasted into a detail column would be permanent.
  const [c] = audit.diff({}, { note: 'x'.repeat(5000) });
  assert.ok(c.to.length < 400, 'clipped, got ' + c.to.length);
});

// ── The invariant: rows cannot be erased on demand ───────────────────────────

test('audit rows survive purge() and deleteRouterData()', () => {
  // This is the property the whole feature rests on: an administrator must not
  // be able to erase the record of what they did, and "who deleted router X"
  // has to outlive router X. Enforced by audit_events being absent from
  // PURGE_TABLES and from deleteRouterData(), i.e. by not opting in.
  db.insertAuditEvent({ action: 'db.purge',      actorName: 'kim', scope: 'app' });
  db.insertAuditEvent({ action: 'router.delete', actorName: 'kim', scope: 'router',
                        routerId: 'doomed-router' });
  const before = db.queryAuditEvents({ includeApp: true, routerIds: ['doomed-router'] }).total;
  assert.ok(before >= 2);

  db.purge({});                              // every type, every age, every router
  db.deleteRouterData('doomed-router');

  const after = db.queryAuditEvents({ includeApp: true, routerIds: ['doomed-router'] });
  assert.strictEqual(after.total, before, 'purge or router deletion erased audit history');
  assert.ok(after.rows.some(r => r.action === 'router.delete' && r.router_id === 'doomed-router'),
    'the record of deleting a router must outlive the router');
});

test('the same sweep leaves the RBAC tables alone', () => {
  // Documented in db.js and CLAUDE.md but never actually asserted. Same
  // mechanism, so it belongs with the test above.
  const site  = db.createSite({ name: 'audit-test-site' });
  const group = db.createGroup({ name: 'audit-test-group' });
  db.purge({});
  assert.ok(db.getSite(site.id),   'a retention purge must not delete a site');
  assert.ok(db.getGroup(group.id), 'a retention purge must not delete a group');
});

test('age is the only thing that can remove a row, and the sweep records itself', () => {
  db.insertAuditEvent({ action: 'settings.update', actorName: 'old', scope: 'app',
                        ts: Date.now() - 400 * 86400000 });
  db.prune(90, 365, 365);                    // 400 days > 365
  const rows = db.queryAuditEvents({ includeApp: true, routerIds: [] }).rows;
  assert.ok(!rows.some(r => r.actor_name === 'old'), 'the audit retention setting must apply');
  // The sweep writes its own row as it goes, so a shrinking history always has
  // an explanation — which is also why row counts do not simply drop by one.
  assert.ok(rows.some(r => r.action === 'db.prune' && r.actor_name === 'system'),
    'the retention sweep must record what it deleted');

  // Retention is its own setting, not the alert one.
  db.insertAuditEvent({ action: 'settings.update', actorName: 'mid', scope: 'app',
                        ts: Date.now() - 200 * 86400000 });
  db.prune(90, 30, 365);                     // alerts 30d would take it; audit 365 does not
  assert.ok(db.queryAuditEvents({ includeApp: true, routerIds: [] })
              .rows.some(r => r.actor_name === 'mid'),
    'a 200-day-old row must survive a 365-day audit retention');
});

// ── Read gating ──────────────────────────────────────────────────────────────

test('app-scope rows are invisible without system administration', () => {
  db.insertAuditEvent({ action: 'role.update',      actorName: 'kim', scope: 'app' });
  db.insertAuditEvent({ action: 'package.schedule', actorName: 'kim', scope: 'router',
                        routerId: 'r-visible' });

  const scoped = db.queryAuditEvents({ includeApp: false, routerIds: ['r-visible'] });
  assert.ok(scoped.total > 0);
  assert.ok(scoped.rows.every(r => r.scope === 'router'),
    'a router-scoped reader must not see who changed a role');
  assert.ok(scoped.rows.every(r => r.router_id === 'r-visible'));
});

test('a reader with neither gets nothing, not everything', () => {
  // The failure mode this shape exists to avoid: an empty permitted-router list
  // meaning "unrestricted". Same bug class as the old allowedRouterIds
  // fallthrough that RBAC deleted.
  const none = db.queryAuditEvents({ includeApp: false, routerIds: [] });
  assert.strictEqual(none.total, 0);
  assert.deepStrictEqual(none.rows, []);
});

test('a routerId filter narrows the permitted set and cannot widen it', () => {
  db.insertAuditEvent({ action: 'wifi.scan', actorName: 'kim', scope: 'router', routerId: 'r-secret' });
  // Asking for a router the session may not see yields nothing rather than that
  // router's rows — the endpoint intersects, it does not substitute.
  const out = db.queryAuditEvents({ includeApp: false, routerIds: ['r-visible'], routerId: 'r-secret' });
  assert.ok(out.rows.every(r => r.router_id !== 'r-secret'));
});

// ── Coverage ─────────────────────────────────────────────────────────────────

test('every mutating route records to the trail', () => {
  // The guard that makes "any write action" stay true. A new POST/PUT/DELETE
  // without an audit call fails here rather than being noticed a release later.
  const routes = [...INDEX_JS.matchAll(/app\.(post|put|patch|delete)\('([^']+)'/g)]
    .map(m => ({ verb: m[1], path: m[2], at: m.index }));
  assert.ok(routes.length >= 20, 'found the mutating routes (' + routes.length + ')');

  // Endpoints that genuinely change nothing stored.
  const EXEMPT = new Set([
    '/api/routers/test',                  // opens a connection, stores nothing
    '/api/settings/test-notification',    // sends a test message, stores nothing
    '/api/user-notify/test-notification',
  ]);

  // A separate list on purpose: these DO store something, so filing them above
  // would make the comment there untrue. They are personal UI preferences whose
  // volume would drown the trail — the nav one can fire 60 times a minute from
  // one user clicking categories open and shut — and whose loss costs nobody
  // anything. Anything that changes what a user can DO belongs in EXEMPT's
  // sibling: the trail, not here.
  const EXEMPT_UI_PREFERENCE = new Set([
    '/api/nav-prefs',                     // sidebar grouping and open categories
  ]);

  const missing = [];
  for (let i = 0; i < routes.length; i++) {
    if (EXEMPT.has(routes[i].path) || EXEMPT_UI_PREFERENCE.has(routes[i].path)) continue;
    const end  = i + 1 < routes.length ? routes[i + 1].at : INDEX_JS.length;
    const body = INDEX_JS.slice(routes[i].at, end);
    if (!/audit\.(fromReq|fromSocket|forLogin|system)\(/.test(body)) {
      missing.push(routes[i].verb.toUpperCase() + ' ' + routes[i].path);
    }
  }
  assert.deepStrictEqual(missing, [], 'these mutating routes record nothing');
});

test('the mutations that are not POST/PUT/DELETE are recorded too', () => {
  // Three the route sweep above cannot see: a logout behind a GET, and the
  // socket actions that write RouterOS config.
  const at = INDEX_JS.indexOf("app.get('/api/auth/logout'");
  assert.ok(/audit\./.test(INDEX_JS.slice(at, at + 900)), 'logout is a mutation behind a GET');

  for (const ev of ['packages:schedule', 'packages:apply', 'wifiscan:start']) {
    const evAt = INDEX_JS.indexOf("socket.on('" + ev + "'");
    assert.ok(evAt > 0, ev + ' handler exists');
    assert.ok(/audit\.fromSocket\(/.test(INDEX_JS.slice(evAt, evAt + 2600)), ev + ' records nothing');
  }
});

test('apply-changes is recorded before the router is asked to reboot', () => {
  // The connection drops as the command runs, so a row written afterwards would
  // be the one row most worth having and least likely to exist.
  const at   = INDEX_JS.indexOf("socket.on('packages:apply'");
  const body = INDEX_JS.slice(at, at + 2600);
  const auditAt = body.indexOf('audit.fromSocket(socket).record');
  const writeAt = body.indexOf("'/system/package/apply-changes'");
  assert.ok(auditAt > 0 && writeAt > 0);
  assert.ok(auditAt < writeAt, 'the audit row must be written before the reboot command');
});

test('refusals are recorded, not just successes', () => {
  const at = INDEX_JS.indexOf("socket.on('packages:apply'");
  assert.ok(/\.denied\(/.test(INDEX_JS.slice(at, at + 1200)),
    'a refused router write must leave a trace');
  assert.ok(/audit\.forLogin\(req, username\)\.denied\(/.test(INDEX_JS),
    'a failed login must leave a trace');
});

test('the settings reset branch records before returning early', () => {
  // It returns before the normal save path, so a single hook at the end of the
  // handler would miss the one write that replaces every setting.
  const at   = INDEX_JS.indexOf('if (body._reset)');
  const body = INDEX_JS.slice(at, at + 700);
  assert.ok(/audit\.fromReq\(req\)\.record/.test(body), 'a full settings reset must be recorded');
  assert.ok(body.indexOf('audit.') < body.indexOf('Settings.save(DEFAULTS)'),
    'record before the write, so the actor is captured even if the save throws');
});

test('the audit table is not reachable from the purge registry', () => {
  // Belt and braces on the invariant above: assert the table name is absent
  // from PURGE_TABLES and deleteRouterData rather than only testing behaviour.
  const DB_JS = fs.readFileSync(path.join(__dirname, '..', 'src', 'db.js'), 'utf8');
  const registry = DB_JS.slice(DB_JS.indexOf('const PURGE_TABLES'), DB_JS.indexOf('const PURGE_TYPES'));
  assert.ok(!registry.includes('audit_events'), 'audit_events must not be purgeable on demand');
  const delAt = DB_JS.indexOf('function deleteRouterData');
  assert.ok(!DB_JS.slice(delAt, delAt + 900).includes('audit_events'),
    'deleting a router must not delete its audit history');
});

test('a refusal in RBAC middleware is recorded, not just one inside a handler', () => {
  // The gap this closes: requireGlobalAdmin answers 403 BEFORE the route
  // handler runs, so a handler that records its own row never sees the attempt.
  // Recording once at the guard covers every route, including ones added later.
  const RBAC_JS = fs.readFileSync(path.join(__dirname, '..', 'src', 'rbac.js'), 'utf8');
  for (const guard of ['function requireGlobalAdmin', 'function requirePerm', 'function requirePage']) {
    const at   = RBAC_JS.indexOf(guard);
    assert.ok(at > 0, guard + ' exists');
    const body = RBAC_JS.slice(at, at + 700);
    assert.ok(/_auditDenied\(/.test(body), guard + ' must record a refused write');
  }
  // Only mutating methods, or a viewer browsing a page they lack would bury the
  // writes that matter.
  const helper = RBAC_JS.slice(RBAC_JS.indexOf('function _auditDenied'), RBAC_JS.indexOf('function requireGlobalAdmin'));
  assert.ok(/POST\|PUT\|PATCH\|DELETE/.test(helper), 'reads must not be recorded as refusals');
});

// ── The Target column names the router ──────────────────────────────────────
//
// The pill read the literal word "router" — a scope marker that told the reader
// nothing the Action column had not already said — while the export's router
// column carried a bare uuid, which told them less.

const APP_JS = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');

test('the audit API resolves router ids to names', () => {
  const at = INDEX_JS.indexOf('function _auditRouterNames');
  assert.ok(at > -1, 'the resolver is gone');
  const body = INDEX_JS.slice(at, INDEX_JS.indexOf('\n}', at));
  assert.ok(/Routers\.getById\(id\)/.test(body));
  assert.ok(/router\.label \|\| router\.host/.test(body), 'a router with no label still has a host');
  // One lookup per distinct router, not one per row: a page is 200 events and
  // most of them name the same handful of devices.
  assert.ok(/names\.has\(id\)/.test(body), 'ids are resolved once each');
});

test('resolving names discloses nothing the row did not already carry', () => {
  // Rows reaching here have passed _auditQuery's permission scope, and each one
  // already holds the router id. This turns a uuid into the name of the same
  // device; it must not become a way to widen the set.
  const at = INDEX_JS.indexOf("app.get('/api/audit'");
  // '\n});' and not '});': the route body contains `offset: 0 });` and
  // `}));`, either of which truncates the slice before the lines under test.
  const body = INDEX_JS.slice(at, INDEX_JS.indexOf('\n});', at));
  assert.ok(/_auditQuery\(req\)/.test(body), 'the scope still decides which rows exist');
  assert.ok(/_auditRouterNames\(out\.rows/.test(body),
    'names are added to those rows, not fetched separately');
});

test('a deleted router degrades differently in the table and the export', () => {
  // The table keeps the old generic marker, because a bare uuid in a pill is
  // noise. The export keeps the id, because a dangling reference still has to be
  // followable — that is what an export is for.
  assert.ok(/esc\(r\.router_name \|\| 'router'\)/.test(APP_JS),
    'the pill falls back to the generic marker');
  const at = INDEX_JS.indexOf("app.get('/api/audit/export'");
  const body = INDEX_JS.slice(at, INDEX_JS.indexOf('\n});', at));
  assert.ok(/names\.get\(r\.router_id\) \|\| r\.router_id/.test(body),
    'the export falls back to the id');
});

test('the pill no longer hardcodes the word router', () => {
  assert.ok(!/wl-band-5">router<\/span>/.test(APP_JS),
    'the literal marker is gone from the Target cell');
  assert.ok(/router_name: r\.router_name \|\| ''/.test(APP_JS),
    'and the row carries the name through to the renderer');
});
