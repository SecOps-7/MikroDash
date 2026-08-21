'use strict';
// The CAPsMAN configuration card: five RouterOS menus behind one tab strip.
//
// Fixtures are shaped exactly as the live hAP AX3 returned them on 2026-08-20 —
// a real CAPsMAN manager with 2 provisioning rules, 6 configuration profiles,
// 3 security, 3 channel and 1 datapath.
//
// What is pinned here, in rough order of how much it would cost to get wrong:
//
//   * no proplist asks the router for a passphrase, and none reaches the payload
//   * the collector's mirrored provisioning identity matches identityOf() — if
//     the separator or the field order drifted, EVERY edit would be refused as a
//     stale row, and nothing else would fail
//   * the push guard stays silent unless something ENABLED references the profile
//   * no typed non-string field is `clearable` (that one cost every edit on the
//     Wifi Networks page last time)

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const path     = require('node:path');

const Capsman  = require('../src/collectors/capsman');
const guard    = require('../src/routeros/capsmanGuard');
const menus    = require('../src/routeros/wifiMenus');
const R        = require('../src/routeros/resources');

const SRC = (...p) => fs.readFileSync(path.join(__dirname, '..', 'src', ...p), 'utf8');

// ── Fixtures, off the live AX3 ───────────────────────────────────────────────

const PROV_ROWS = [
  { '.id': '*1', 'supported-bands': '5ghz-ac,5ghz-ax,5ghz-n,5ghz-a',
    action: 'create-dynamic-enabled', 'master-configuration': 'Home WiFi 5Ghz',
    'slave-configurations': 'Guest WiFi 5Ghz,IoT WiFi 5Ghz',
    'name-format': '5GHz WiFi', disabled: 'false' },
  { '.id': '*2', 'supported-bands': '2ghz-n,2ghz-g,2ghz-ax',
    action: 'create-dynamic-enabled', 'master-configuration': 'Home WiFi 2.4Ghz',
    'slave-configurations': 'Guest WiFi 2.4Ghz,IoT WiFi 2.4Ghz',
    'name-format': '2.4GHz WiFi', disabled: 'false' },
];

const CONFIG_ROWS = [
  { '.id': '*1', name: 'Home WiFi 5Ghz', ssid: 'SkyNet', country: 'Germany', mode: 'ap',
    security: 'Home WiFi', channel: '5Ghz Channel Range', datapath: 'datapath' },
  { '.id': '*2', name: 'Home WiFi 2.4Ghz', ssid: 'SkyNet', country: 'Germany', mode: 'ap',
    security: 'Home WiFi', datapath: 'datapath' },
  { '.id': '*3', name: 'Guest WiFi 2.4Ghz', ssid: 'The Promised LAN', country: 'Germany',
    mode: 'ap', security: 'Guest WiFi', datapath: 'datapath' },
];

// ── The security property ────────────────────────────────────────────────────

test('no menu asks the router for a secret', () => {
  // This one assertion now covers BOTH collectors, which is the whole reason the
  // proplists live in one module. /interface/wifi/security/print and
  // /interface/wifi/configuration/print return the passphrase in clear text.
  const all = JSON.stringify(menus.MENUS).toLowerCase();
  for (const secret of ['passphrase', 'pre-shared-key', 'presharedkey', 'password'])
    assert.ok(!all.includes(secret), 'a shared proplist asks for ' + secret);
});

test('neither collector hand-rolls a proplist for the profile menus', () => {
  // The point of the shared module is that there is nowhere else to drift TO.
  for (const f of [['collectors', 'wifi.js'], ['collectors', 'capsman.js']]) {
    const src = SRC(...f);
    assert.ok(src.includes("require('../routeros/wifiMenus')"),
      f.join('/') + ' must read the profile menus through the shared module');
    assert.ok(!/proplist=[^']*passphrase/.test(src),
      f.join('/') + ' names a passphrase in a proplist');
  }
});

test('a passphrase in a router row never reaches the payload', () => {
  // The payload projects field by name rather than spreading the row, so even a
  // widened proplist cannot push one through.
  const view = Capsman.buildCapsmanView(null, null, [], PROV_ROWS, [], [], []);
  assert.ok(!JSON.stringify(view).includes('hunter2hunter2'));
  const src = SRC('collectors', 'capsman.js');
  const at  = src.indexOf('const profiles = {');
  assert.ok(at > 0, 'the profile projection is gone');
  const body = src.slice(at, src.indexOf('\n    };', at));
  assert.ok(!/\.\.\.r\b/.test(body), 'the projection spreads the row instead of naming fields');
  assert.ok(!/passphrase/i.test(body));
});

test('the security resource declares its passphrase secret, so it is stripped both ways', () => {
  const f = R.byKey('capsSecurity').fields.find(x => x.name === 'passphrase');
  assert.strictEqual(f.type, 'secret');
  const vals = R.rowValues(R.byKey('capsSecurity'),
    { name: 'Home WiFi', passphrase: 'hunter2hunter2', 'authentication-types': 'wpa2-psk' });
  assert.strictEqual(vals.passphrase, undefined, 'the form would be filled with the passphrase');
  assert.strictEqual(vals.name, 'Home WiFi');
});

// ── The identity mirror ──────────────────────────────────────────────────────

test('the collector builds the same provisioning identity resources.js does', () => {
  // A provisioning rule has no name, so the row is ADDRESSED by .id and
  // IDENTIFIED by a composite. The collector mirrors that composite the way
  // app.js mirrors fwIdentity() for the firewall. If the separator or the field
  // ORDER drifted, every edit would be refused as a stale row and nothing else
  // would fail — so it is pinned here rather than left to be discovered.
  const built = Capsman.buildCapsmanView(null, null, [], PROV_ROWS, [], [], []);
  const resource = R.byKey('capsProvisioning');
  built.provisioning.forEach((p, i) => {
    assert.strictEqual(p.identity, R.identityOf(resource, PROV_ROWS[i]),
      'row ' + i + ': the mirrored identity has drifted from identityOf()');
  });
});

test('provisioning rows carry the id the edit dialog addresses them by', () => {
  const built = Capsman.buildCapsmanView(null, null, [], PROV_ROWS, [], [], []);
  assert.deepStrictEqual(built.provisioning.map(p => p.id), ['*1', '*2']);
});

// ── The registry ─────────────────────────────────────────────────────────────

const CAPS_KEYS = ['capsProvisioning', 'capsConfig', 'capsSecurity', 'capsChannel', 'capsDatapath'];

test('all five CAPsMAN resources are registered against the capsman page', () => {
  for (const k of CAPS_KEYS) {
    const r = R.byKey(k);
    assert.ok(r, k + ' is not registered');
    assert.strictEqual(r.page, 'capsman');
    assert.strictEqual(r.collector, 'capsman');
    assert.ok(r.requiresMenu, k + ' must declare the menu it needs');
  }
});

test('the page scope keeps the fleet-wide edit behind the stronger grant', () => {
  // Deliberate asymmetry: a role with write on `wifi` can override a value on
  // ONE interface, but editing the shared profile every CAP follows needs
  // `capsman`. Smaller blast radius for the lesser grant.
  assert.strictEqual(R.byKey('wifiNet').page, 'wifi');
  assert.strictEqual(R.byKey('capsConfig').page, 'capsman');
});

test('provisioning is ordered, because the first matching rule wins', () => {
  assert.strictEqual(R.byKey('capsProvisioning').ordered, true);
  // And the other four are not — nothing about a profile depends on its row.
  for (const k of CAPS_KEYS.filter(x => x !== 'capsProvisioning'))
    assert.ok(!R.byKey(k).ordered, k + ' should not be ordered');
});

test('the configuration profile does not offer `manager`', () => {
  // MikroTik's docs warn that configuration.manager belongs on the CAP device
  // itself and must never be pushed through a provisioned profile.
  const names = R.byKey('capsConfig').fields.map(f => f.name);
  assert.ok(!names.includes('manager'), 'manager must not be editable here');
});

test('no typed non-string field is clearable', () => {
  // `clearable` emits `=key=` on an edit, and RouterOS refuses an empty value
  // for a typed integer — which made every edit of a wireless network fail last
  // time. A bool is exempt: validate() always produces yes/no for one.
  for (const k of CAPS_KEYS) {
    for (const f of R.byKey(k).fields) {
      if (!f.clearable) continue;
      assert.ok(f.type === 'bool' || f.type === 'text',
        k + '.' + f.name + ' is clearable but typed ' + f.type);
    }
  }
});

test('an edit that leaves the VLAN alone sends no empty VLAN word', () => {
  const d = R.byKey('capsDatapath');
  const v = R.validate(d, { name: 'datapath', bridge: 'Bridge' }, { editing: true });
  assert.ok(v.ok, JSON.stringify(v.errors));
  assert.ok(!R.buildArgs(d, v).some(a => a === '=vlan-id='));
});

test('a passphrase is length-checked, and blank still means unchanged', () => {
  const s = R.byKey('capsSecurity');
  const short = R.validate(s, { name: 'x', passphrase: 'short' }, {});
  assert.strictEqual(short.ok, false);
  assert.strictEqual(short.errors[0].field, 'passphrase');
  assert.strictEqual(R.validate(s, { name: 'x', passphrase: '' }, { editing: true }).ok, true);
});

// ── The push guard ───────────────────────────────────────────────────────────

const push = (over) => guard.checkPush(Object.assign({
  resourceKey: 'capsConfig', action: 'update',
  values: { ssid: 'Changed' }, before: { name: 'Home WiFi 5Ghz', ssid: 'SkyNet' },
  configRows: CONFIG_ROWS, provRows: PROV_ROWS, capCount: 1,
}, over || {}));

test('editing a profile a live rule provisions warns, and names the rule', () => {
  const v = push();
  assert.strictEqual(v.level, 'warn');
  assert.strictEqual(v.code, 'capsman-push');
  assert.strictEqual(v.detail.profile, 'Home WiFi 5Ghz');
  assert.strictEqual(v.detail.ruleCount, 1);
  assert.deepStrictEqual(v.detail.rules, ['5GHz WiFi']);
  assert.strictEqual(v.detail.caps, 1);
  assert.ok(v.fingerprint);
});

test('a profile nothing references is not worth a warning', () => {
  // The whole reason this guard is narrow: a prompt on every save of an unused
  // profile is one people learn to click through.
  const v = push({ before: { name: 'Unused Profile' }, values: { ssid: 'x' } });
  assert.strictEqual(v.level, 'none');
});

test('a DISABLED rule provisions nothing, so it warns about nothing', () => {
  const off = PROV_ROWS.map(p => Object.assign({}, p, { disabled: 'true' }));
  assert.strictEqual(push({ provRows: off }).level, 'none');
});

test('a slave configuration counts as referenced', () => {
  const v = push({ before: { name: 'Guest WiFi 5Ghz' }, values: { ssid: 'x' } });
  assert.strictEqual(v.level, 'warn');
  assert.strictEqual(v.detail.ruleCount, 1);
});

test('security, channel and datapath resolve through the configuration profile', () => {
  // Two levels: the profile is named by a configuration, which is named by a
  // rule. A profile referenced only by an unprovisioned configuration is silent.
  const sec = push({ resourceKey: 'capsSecurity', before: { name: 'Home WiFi' },
                     values: { passphrase: 'longenough1' } });
  assert.strictEqual(sec.level, 'warn');
  assert.ok(sec.detail.ruleCount >= 1);

  const chan = push({ resourceKey: 'capsChannel', before: { name: '5Ghz Channel Range' },
                      values: { width: '20mhz' } });
  assert.strictEqual(chan.level, 'warn');

  const orphan = push({ resourceKey: 'capsSecurity', before: { name: 'Nothing Uses This' },
                        values: { passphrase: 'longenough1' } });
  assert.strictEqual(orphan.level, 'none');
});

test('a create pushes nothing, because nothing is following it yet', () => {
  assert.strictEqual(push({ action: 'create', before: null }).level, 'none');
});

test('a delete of a provisioned profile still warns', () => {
  const v = push({ action: 'delete', values: {} });
  assert.strictEqual(v.level, 'warn');
  assert.strictEqual(v.detail.action, 'delete');
});

test('a missing CAP count costs a number, never the warning', () => {
  const v = push({ capCount: undefined });
  assert.strictEqual(v.level, 'warn');
  assert.strictEqual(v.detail.caps, null);
});

test('the provisioning menu itself raises no push warning', () => {
  // Editing a rule does not push: it acts when a CAP joins. That is why
  // capsProvisioning declares no guard at all.
  assert.ok(!R.byKey('capsProvisioning').guard);
  assert.strictEqual(push({ resourceKey: 'capsProvisioning' }).level, 'none');
});

test('the guard reads the router in the same tick, not the collector payload', () => {
  // A count or a reference taken from the last tick could be two minutes old,
  // and the engine is forbidden from touching lastPayload at all.
  const src = SRC('index.js');
  const at  = src.indexOf("if (kind === 'capsmanPush')");
  assert.ok(at > 0, 'the capsmanPush branch is gone');
  // Comments stripped first, the way the engine's own lastPayload test does it:
  // the branch EXPLAINS in prose why it does not read the payload, and prose is
  // not code.
  const body = src.slice(at, src.indexOf('\n    }', at))
    .replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
  assert.ok(!body.includes('lastPayload'), 'the guard must not read the collector payload');
  assert.ok(/remote-cap\/print/.test(body), 'the CAP count must be read fresh');
});

// ── Collector wiring ─────────────────────────────────────────────────────────

test('the collector exposes refreshNow, which is how a write reaches the card', () => {
  // Its absence was silent: every res:* handler calls it only if it exists, so a
  // save landed on the router and the card sat still for up to two minutes.
  assert.strictEqual(typeof Capsman.prototype.refreshNow, 'function');
  const src = SRC('collectors', 'capsman.js');
  const at  = src.indexOf('async refreshNow()');
  const body = src.slice(at, src.indexOf('\n  }', at));
  assert.ok(/_lastFp\s*=\s*''/.test(body),
    'refreshNow must clear the fingerprint, or a write restoring a previous value is swallowed');
});

test('the fingerprint covers every field the card can edit', () => {
  // A field left out means a save that lands on the router and never reaches the
  // browser — which reads as a failed write. comment and slaveConfigurations
  // were exactly that before this card existed.
  const src = SRC('collectors', 'capsman.js');
  const at  = src.indexOf('const fp = JSON.stringify({');
  const body = src.slice(at, src.indexOf('});', at));
  for (const field of ['supportedBands', 'slaveConfigurations', 'comment', 'identityRegexp'])
    assert.ok(body.includes(field), 'the fingerprint omits ' + field);
  assert.ok(/profiles\.configuration/.test(body), 'the fingerprint omits the profile tables');
});

test('an empty menu answers with a nameless junk row, which must not become a profile', () => {
  assert.deepStrictEqual(menus.named([{ undefined: '' }]), []);
  assert.deepStrictEqual(menus.named([{ name: 'real' }, { undefined: '' }]), [{ name: 'real' }]);
  assert.deepStrictEqual(menus.identified([{ undefined: '' }]), []);
});

// ── The card is mounted and does not decide anything ─────────────────────────

test('the tabbed card mounts all five resources statically', () => {
  // data-res-add-dynamic must be a literal in the HTML: the mount test reads the
  // file, so a list built in JS would not count.
  const html = fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');
  const m = html.match(/data-res-add-dynamic="([^"]+)"/g) || [];
  const declared = new Set(m.flatMap(s => s.replace(/.*="|"$/g, '').split(',').map(x => x.trim())));
  for (const k of CAPS_KEYS) assert.ok(declared.has(k), k + ' is not declared on a mount slot');
  // And each panel's tbody names its own resource.
  for (const k of CAPS_KEYS) assert.ok(html.includes('data-res-rows="' + k + '"'), k + ' has no rows mount');
});

test('the CAPsMAN page drops its state when the router changes', () => {
  // Every page that can be edited from does this, so an edit form is never
  // offered against rows belonging to another device. This page could not be
  // edited from before the configuration card existed, and had no handler.
  const app = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');
  const at  = app.indexOf('── CAPsMAN configuration');
  assert.ok(at > 0, 'the CAPsMAN configuration card is gone');
  assert.ok(/router:switched/.test(app.slice(at)), 'the new card must reset on router switch');
  // The older CAPsMAN IIFE too.
  const older = app.indexOf('capsmanThead');
  assert.ok(older > 0);
  assert.ok(/router:switched/.test(app.slice(older, at)), 'the CAPs table must reset too');
});
