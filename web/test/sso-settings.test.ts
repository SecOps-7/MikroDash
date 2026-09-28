/**
 * The SSO / OIDC tab of Access Management, and its dialog.
 *
 * ── THE ONE THING THAT MUST NOT BREAK ──────────────────────────────────────
 *
 * The server never sends a client secret back, so editing a provider shows an
 * EMPTY secret box - and saving with it empty must leave the stored secret
 * alone. The server reads an ABSENT `clientSecret` as "keep it" and an empty
 * string as "clear it", so a form that sent `clientSecret: ''` would silently
 * break every provider the moment anybody saved it without retyping. It would
 * still look configured, and every sign-in would fail with `invalid_client`.
 * That is the failure this file exists for.
 *
 * The same shape as the webhook URLs in notify-channels.test.ts, and for the
 * same reason - which is why it is worth checking twice rather than assuming
 * the pattern held.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-sso-settings.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings-sso.ts'),
    '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function makeEl(id) {
  const classes = new Set();
  const node = {
    id, innerHTML: '', textContent: '', value: '', checked: false, disabled: false,
    hidden: false, placeholder: '', src: '', files: null, style: {}, listeners: {},
    addEventListener: (ev, fn) => { (node.listeners[ev] = node.listeners[ev] || []).push(fn); },
    fire: (ev, arg) => (node.listeners[ev] || []).forEach((fn) => fn(arg || {})),
    setAttribute: (k, v) => { node['__' + k] = v; },
    getAttribute: (k) => (('__' + k) in node ? node['__' + k] : null),
    closest: () => null,
    click: () => node.fire('click'),
    insertAdjacentHTML: (_where, html) => { node.innerHTML += html; },
    querySelectorAll: () => [],
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
  };
  return node;
}

const ROLES = [
  { id: 'administrator', name: 'Administrator', pages: [] },
  { id: 'operator', name: 'Operator', pages: [] },
  { id: 'a1b2c3d4', name: 'NOC Tier 1', pages: [] },
];

const PROVIDER = {
  id: 'abc123def456', name: 'Entra ID', enabled: true,
  issuer: 'https://login.microsoftonline.com/t/v2.0', clientId: 'cid',
  scopes: 'openid profile email', claimUsername: 'preferred_username',
  claimEmail: 'email', claimName: 'name', claimRoles: 'groups',
  iconVersion: 0, hasSecret: true, icon: '',
  redirectUri: 'https://dash.example/api/auth/sso/callback',
  roleMap: [{ claimValue: 'netops', roleId: 'operator' }],
};

function mount(providers, baseUrlSet = true, before) {
  const els = {};
  const get = (id) => {
    if (!els[id]) els[id] = makeEl(id);
    return els[id];
  };
  // A seam for the case that matters: a field the operator has already typed
  // into, before the module loads.
  if (before) before(get);
  // The mapping rows are written with innerHTML, so this shim cannot discover
  // them. `mapRows` is served for the one selector that reads them back, built
  // from what the test intends the operator to have typed - the harness's
  // limit, not the panel's. What is asserted is the REQUEST, which is what a
  // provider's configuration actually depends on.
  const mapRows = [];
  const sent = [];

  global.document = {
    getElementById: get,
    querySelectorAll: (sel) =>
      (sel === '#so_mapRows [data-sso-map]' ? mapRows : []),
    addEventListener: () => {},
    createElement: () => makeEl(''),
  };
  global.window = { confirm: () => true };
  global.fetch = (url, opts) => {
    const u = String(url);
    if (opts && opts.method && opts.method !== 'GET') {
      sent.push({
        url: u, method: opts.method,
        body: typeof opts.body === 'string' ? JSON.parse(opts.body) : opts.body,
      });
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true, id: 'new1' }) });
    }
    if (u.startsWith('/api/sso/providers')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ ok: true, providers,
          baseUrl: baseUrlSet ? 'https://dash.example' : '',
          redirectUri: baseUrlSet ? 'https://dash.example/api/auth/sso/callback' : '' }),
      });
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
  };

  delete require.cache[require.resolve(OUT)];
  const mod = require(OUT);
  mod.initSSOCard(() => ROLES);
  void mod.loadSSOProviders();
  return { els, sent, mapRows, get };
}

const settle = () => new Promise((r) => setImmediate(r));

let failed = 0;
const checks = [];
function check(name, fn) { checks.push({ name, fn }); }

check('the table lists each provider with its issuer and state', async () => {
  const d = mount([PROVIDER]);
  await settle();
  const html = d.els.ssoTbody.innerHTML;
  assert.ok(html.includes('Entra ID'), 'the name is missing: ' + html);
  assert.ok(html.includes('login.microsoftonline.com'), 'the issuer is missing: ' + html);
  assert.ok(html.includes('>Enabled<'), 'an enabled provider did not read Enabled: ' + html);
  assert.ok(html.includes('1 rule'), 'the mapping count is missing: ' + html);
});

check('a disabled provider reads Disabled', async () => {
  const d = mount([{ ...PROVIDER, enabled: false }]);
  await settle();
  assert.ok(d.els.ssoTbody.innerHTML.includes('>Disabled<'),
    'a disabled provider did not read Disabled: ' + d.els.ssoTbody.innerHTML);
});

// ── NO MAPPING IS A WARNING, NOT A ZERO ────────────────────────────────────
//
// Nobody can sign in through a provider with no role mapping. Rendering "0"
// beside it reads as a count; the warning pill says it is a state.
check('a provider with no role mapping is flagged rather than counted', async () => {
  const d = mount([{ ...PROVIDER, roleMap: [] }]);
  await settle();
  const html = d.els.ssoTbody.innerHTML;
  assert.ok(html.includes('>None<'), 'an unmapped provider did not say None: ' + html);
  assert.ok(html.includes('warn'), 'an unmapped provider was not flagged: ' + html);
});

// A NAME IS OPERATOR INPUT and reaches innerHTML. This app had exactly this bug
// in an interface name (0.7.35).
check('a provider name cannot inject markup', async () => {
  const d = mount([{ ...PROVIDER, name: '<img src=x onerror=alert(1)>' }]);
  await settle();
  const html = d.els.ssoTbody.innerHTML;
  assert.ok(!html.includes('<img'), 'a provider name reached the page as markup: ' + html);
  assert.ok(html.includes('&lt;img'), 'the name was not escaped: ' + html);
});

check('an empty list says so rather than drawing nothing', async () => {
  const d = mount([]);
  await settle();
  assert.ok(d.els.ssoTbody.innerHTML.includes('No providers yet'),
    'an empty table rendered silently: ' + d.els.ssoTbody.innerHTML);
});

// ── THE BASE URL BOX PRE-FILLS FROM THE EFFECTIVE VALUE ────────────────────
//
// The server sends what it will actually use: the stored setting, or the
// address the request arrived on. An empty box on an install where SSO works
// reads as unconfigured and invites somebody to "fix" it.
check('the base URL box pre-fills from the effective value', async () => {
  const d = mount([PROVIDER]);
  await settle();
  assert.strictEqual(d.els.s_baseUrl.value, 'https://dash.example',
    'the base URL box was left empty: ' + d.els.s_baseUrl.value);
});

// A half-typed value is not overwritten. Clobbering what somebody is in the
// middle of typing gets noticed once and never forgiven.
check('a value already in the box is not overwritten', async () => {
  const d = mount([PROVIDER], true, (els) => { els('s_baseUrl').value = 'https://typed.example'; });
  await settle();
  assert.strictEqual(d.els.s_baseUrl.value, 'https://typed.example',
    'the pre-fill overwrote what the operator had typed');
});

// ── THE FIRST PROVIDER STILL LEARNS ITS REDIRECT URI ───────────────────────
//
// It is shown read-only and has to be pasted into the identity provider. The
// first provider on an install has NO ROW to read it from, so a version that
// took it only from the rows would tell a correctly configured operator to go
// and set a base URL they already have.
check('the dialog shows the redirect URI before any provider exists', async () => {
  const d = mount([]);
  await settle();
  d.els.addSsoBtn.fire('click');
  await settle();
  assert.strictEqual(d.els.so_redirectUri.value,
    'https://dash.example/api/auth/sso/callback',
    'the first provider was not shown its redirect URI: ' + d.els.so_redirectUri.value);
});

// ── THE SAVE BODY ──────────────────────────────────────────────────────────

async function editAndSave(mutate) {
  const d = mount([PROVIDER]);
  await settle();
  // Open the dialog the way the delegated listener does.
  d.els.addSsoBtn.fire('click');
  await settle();
  d.els.so_id.value = PROVIDER.id;
  d.els.so_name.value = 'Entra ID';
  d.els.so_issuer.value = PROVIDER.issuer;
  d.els.so_clientId.value = 'cid';
  d.els.so_scopes.value = 'openid profile email';
  d.els.so_claimRoles.value = 'groups';
  if (mutate) mutate(d);
  d.els.so_save.fire('click');
  await settle();
  await settle();
  return d.sent.find((s) => s.method === 'PUT' || s.method === 'POST');
}

// THE CHECK THIS FILE EXISTS FOR.
check('an untouched secret box OMITS the key rather than sending an empty one', async () => {
  const req = await editAndSave((d) => { d.els.so_clientSecret.value = ''; });
  assert.ok(req, 'nothing was sent');
  assert.ok(!('clientSecret' in req.body),
    'the form sent clientSecret=' + JSON.stringify(req.body.clientSecret)
    + '. An empty string CLEARS the stored secret - every edit would break the '
    + 'provider while it still looked configured.');
});

check('a typed secret is sent', async () => {
  const req = await editAndSave((d) => { d.els.so_clientSecret.value = 's3cret'; });
  assert.strictEqual(req.body.clientSecret, 's3cret', 'a typed secret was not sent');
});

check('the enabled checkbox reaches the body as a boolean', async () => {
  const req = await editAndSave((d) => { d.els.so_enabled.checked = true; });
  assert.strictEqual(req.body.enabled, true, 'enabled was ' + JSON.stringify(req.body.enabled));
});

// ── A WHOLLY EMPTY MAPPING ROW IS NOT A MAPPING ────────────────────────────
//
// The form always draws one blank row. Sending it would store a mapping with an
// empty claim value, which the server refuses - so adding a provider without
// touching the Role mapping tab would fail with a message about mappings.
check('a blank mapping row is not sent', async () => {
  const d = mount([PROVIDER]);
  await settle();
  d.els.addSsoBtn.fire('click');
  await settle();
  const blank = makeEl('');
  blank.querySelectorAll = () => [];
  blank.querySelector = () => ({ value: '' });
  d.mapRows.push(blank);
  d.els.so_name.value = 'Okta';
  d.els.so_issuer.value = 'https://example.okta.com';
  d.els.so_clientId.value = 'cid';
  d.els.so_save.fire('click');
  await settle();
  await settle();
  const req = d.sent.find((s) => s.method === 'POST');
  assert.ok(req, 'nothing was sent');
  assert.deepStrictEqual(req.body.roleMap, [],
    'a blank row was sent as a mapping: ' + JSON.stringify(req.body.roleMap));
});

check('a filled mapping row is sent with its role id', async () => {
  const d = mount([PROVIDER]);
  await settle();
  d.els.addSsoBtn.fire('click');
  await settle();
  const row = makeEl('');
  row.querySelector = (sel) =>
    (sel === '[data-sso-claim]' ? { value: 'netops' } : { value: 'a1b2c3d4' });
  d.mapRows.push(row);
  d.els.so_name.value = 'Okta';
  d.els.so_issuer.value = 'https://example.okta.com';
  d.els.so_clientId.value = 'cid';
  d.els.so_save.fire('click');
  await settle();
  await settle();
  const req = d.sent.find((s) => s.method === 'POST');
  assert.deepStrictEqual(req.body.roleMap, [{ claimValue: 'netops', roleId: 'a1b2c3d4' }],
    'the mapping was not sent as typed: ' + JSON.stringify(req.body.roleMap));
});

// ── THE ROLE IS A SELECT, AND THE OPTIONS ARE REAL ROLE IDS ────────────────
//
// A custom role's id is an opaque random string. If the mapping form offered
// NAMES as values, every custom role would map to nothing and present as "SSO
// does not work" rather than as a typo.
check('the mapping row offers role ids, not role names', async () => {
  const d = mount([{ ...PROVIDER, roleMap: [{ claimValue: 'noc', roleId: 'a1b2c3d4' }] }]);
  await settle();
  d.els.addSsoBtn.fire('click');
  await settle();
  const html = d.els.so_mapRows.innerHTML;
  assert.ok(html.includes('value="a1b2c3d4"'),
    'the custom role is not offered by id: ' + html);
  assert.ok(html.includes('>NOC Tier 1<'), 'the role name is not shown: ' + html);
});

(async () => {
  for (const c of checks) {
    try {
      await c.fn();
      say('  ok   ' + c.name);
    } catch (e) {
      failed += 1;
      say('  FAIL ' + c.name + '\n       ' + (e && e.message));
    }
  }
  if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
  say('\nall passed');
})();
