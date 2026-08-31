#!/usr/bin/env node
'use strict';
/**
 * Pin the fleet-push warning against the LIVE implementation.
 *
 * WHY THIS ONE. It has the largest blast radius in the registry and the least
 * visible one. Every other write this engine makes affects the thing named in
 * the form; a CAPsMAN profile save reaches every CAP that follows it, and the
 * only sign is this warning. Under-warn and a passphrase change silently drops
 * every client in the building; over-warn and the prompt becomes furniture.
 *
 * Two things make it worth running both implementations rather than reading one:
 *
 *   TRANSITIVE REFERENCES. A configuration profile is named directly by a
 *   provisioning rule; a security, channel or datapath profile is named by a
 *   CONFIGURATION, which is then named by a rule. So the second kind resolves
 *   two levels, and a profile referenced only by an UNPROVISIONED configuration
 *   must stay silent — a case that is easy to get right by accident in one
 *   direction and wrong in the other.
 *
 *   THE COMPARISON IS AGAINST A RAW ROW. `values` carries registry field names
 *   and `before` is the router's row, so a field whose ROS spelling differs
 *   (`authTypes` vs `authentication-types`) compares against nothing and counts
 *   as changed. That is the live behaviour, quirk included.
 *
 *   node tools/capsmanguard-cases.js            # write the corpus
 *   node tools/capsmanguard-cases.js --check    # fail if it is stale
 */

const fs   = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT  = process.env.CAPSGUARD_OUT || path.join(__dirname, '..', 'testdata', 'capsmanguard-cases.json');

const G = require(path.join(ROOT, 'src', 'routeros', 'capsmanGuard.js'));
for (const fn of ['checkPush', 'referencingRules']) {
  if (typeof G[fn] !== 'function') {
    console.error('src/routeros/capsmanGuard.js no longer exports ' + fn + '.');
    process.exit(1);
  }
}
const FIELDS = JSON.stringify(G.CONFIG_FIELD);
if (FIELDS !== '{"capsSecurity":"security","capsChannel":"channel","capsDatapath":"datapath"}') {
  console.error('CONFIG_FIELD changed to ' + FIELDS + ' — the port hard-codes the old map.');
  process.exit(1);
}

// ── The router's profile graph ───────────────────────────────────────────────
//
// `home-cfg` is provisioned; `unused-cfg` is not. That split is the whole point:
// a security profile reached only through `unused-cfg` must stay silent.
const CONFIG_ROWS = [
  { '.id': '*c1', name: 'home-cfg',   security: 'home-sec',   channel: 'ch-5g', datapath: 'dp1' },
  { '.id': '*c2', name: 'guest-cfg',  security: 'guest-sec',  channel: 'ch-5g', datapath: 'dp1' },
  { '.id': '*c3', name: 'unused-cfg', security: 'unused-sec', channel: 'ch-2g', datapath: 'dp2' },
];

const PROV_ROWS = [
  { '.id': '*p1', 'master-configuration': 'home-cfg',
    'slave-configurations': 'guest-cfg', 'name-format': 'home-%I', disabled: 'false' },
  // DISABLED: provisions nothing, so it can push nothing either.
  { '.id': '*p2', 'master-configuration': 'unused-cfg', 'name-format': 'off-%I', disabled: 'true' },
  { '.id': '*p3', 'master-configuration': 'nowhere-cfg', 'name-format': 'orphan-%I' },
];

const BEFORE = {
  capsConfig:   { '.id': '*c1', name: 'home-cfg', ssid: 'HomeNet', country: 'GB' },
  capsSecurity: { '.id': '*s1', name: 'home-sec', 'authentication-types': 'wpa2-psk' },
  capsChannel:  { '.id': '*h1', name: 'ch-5g', frequency: '5180', width: '20/40/80mhz' },
  capsDatapath: { '.id': '*d1', name: 'dp1', bridge: 'bridge', 'vlan-id': '10' },
  unused:       { '.id': '*s9', name: 'unused-sec', 'authentication-types': 'wpa2-psk' },
  orphan:       { '.id': '*s8', name: 'not-referenced-at-all' },
  // A `before` CARRYING a passphrase. The router does not do this — a secret
  // never reads back, which is the whole reason the guard special-cases it — but
  // it is the ONLY input that separates the two implementations. Everywhere else
  // the stored value is absent, so comparing it gives the same answer as the
  // rule, and a port that dropped the special case would pass unnoticed.
  withPass:     { '.id': '*s2', name: 'home-sec', passphrase: 'already-stored' },
};

const CASES = [
  { n: 'config profile, provisioned, ssid changed',
    key: 'capsConfig', before: BEFORE.capsConfig, values: { ssid: 'NewNet' } },
  { n: 'config profile, provisioned, nothing changed',
    key: 'capsConfig', before: BEFORE.capsConfig, values: { ssid: 'HomeNet' } },
  { n: 'config profile reached only as a SLAVE configuration',
    key: 'capsConfig', before: { '.id': '*c2', name: 'guest-cfg' }, values: { ssid: 'X' } },
  { n: 'config profile nothing provisions',
    key: 'capsConfig', before: { '.id': '*c3', name: 'unused-cfg' }, values: { ssid: 'X' } },

  { n: 'security profile, TWO levels up to a rule',
    key: 'capsSecurity', before: BEFORE.capsSecurity, values: { authTypes: 'wpa3-psk' } },
  { n: 'security profile reached only via an unprovisioned config',
    key: 'capsSecurity', before: BEFORE.unused, values: { authTypes: 'wpa3-psk' } },
  { n: 'security profile: a passphrase, which never reads back',
    key: 'capsSecurity', before: BEFORE.capsSecurity, values: { passphrase: 'hunter2hunter2' } },
  { n: 'security profile: a BLANK passphrase',
    key: 'capsSecurity', before: BEFORE.capsSecurity, values: { passphrase: '' } },
  { n: 'STORED passphrase re-submitted unchanged',
    key: 'capsSecurity', before: BEFORE.withPass, values: { passphrase: 'already-stored' } },
  { n: 'STORED passphrase changed',
    key: 'capsSecurity', before: BEFORE.withPass, values: { passphrase: 'a-new-one' } },

  { n: 'channel profile, shared by two configs',
    key: 'capsChannel', before: BEFORE.capsChannel, values: { frequency: '5745' } },
  { n: 'datapath profile',
    key: 'capsDatapath', before: BEFORE.capsDatapath, values: { vlanId: '20' } },

  { n: 'a profile nothing references at all',
    key: 'capsSecurity', before: BEFORE.orphan, values: { authTypes: 'wpa3-psk' } },
  { n: 'an unknown resource key',
    key: 'capsProvisioning', before: BEFORE.capsConfig, values: { ssid: 'X' } },
  { n: 'no before row at all',
    key: 'capsConfig', before: null, values: { name: 'home-cfg', ssid: 'X' } },
];

const ACTIONS = ['create', 'update', 'delete', 'enable', 'disable'];
const CAP_COUNTS = [3, 0, null];

function run() {
  const out = [];
  for (const c of CASES) {
    for (const action of ACTIONS) {
      for (const capCount of CAP_COUNTS) {
        const v = G.checkPush({ resourceKey: c.key, action, values: c.values,
                                before: c.before, configRows: CONFIG_ROWS,
                                provRows: PROV_ROWS, capCount });
        out.push({
          name: c.n, key: c.key, action, capCount,
          values: c.values, valueKeys: Object.keys(c.values),
          before: c.before,
          want: {
            level: v.level, code: v.code || '',
            detail: v.detail ? {
              profile: v.detail.profile, rules: v.detail.rules,
              ruleCount: v.detail.ruleCount,
              caps: v.detail.caps === null ? -1 : v.detail.caps,
              action: v.detail.action,
            } : null,
            fingerprint: v.fingerprint || '',
          },
        });
      }
    }
  }
  return { configRows: CONFIG_ROWS, provRows: PROV_ROWS, cases: out };
}

const out = JSON.stringify(run(), null, 2) + '\n';
const n = run();
const warned = n.cases.filter((c) => c.want.level === 'warn').length;
const counts = `${n.cases.length} decisions, ${warned} warnings`;

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== out) {
    console.error('testdata/capsmanguard-cases.json is stale — run: node tools/capsmanguard-cases.js');
    process.exit(1);
  }
  console.log(`capsmanguard-cases up to date (${counts})`);
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, out);
  console.log(`wrote ${path.relative(process.cwd(), OUT)} — ${counts}`);
}
