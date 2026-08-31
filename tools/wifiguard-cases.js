#!/usr/bin/env node
'use strict';
/**
 * Pin the inherited-profile warning against the LIVE implementation.
 *
 * WHY THIS ONE. It is the third warn-and-fail-open guard, so a wrong answer is
 * silent — but its failure mode is unusual and worth naming. The others get
 * loud about a real hazard; this one is about a SURPRISE. Overriding a shared
 * profile is a legitimate thing to do, and the warning exists only because the
 * consequence lands on a radio you are NOT looking at. Warn too eagerly and it
 * becomes furniture on every save of a defconf router; warn too rarely and two
 * SSIDs quietly stop moving together.
 *
 * Three details make it worth running both implementations rather than reading
 * one:
 *
 *   PRESENCE, NOT EMPTINESS. The original tests `hasOwnProperty`, so a field
 *   submitted as '' is a value and an absent field is not. Go maps cannot tell
 *   the two apart, so the port carries a separate `Set`, and only running both
 *   proves the split is in the same place.
 *
 *   PASSPHRASE IS WRITE-ONLY. It is never read back, so "is it changing" cannot
 *   be answered by comparison: any non-empty value counts, a blank one does not.
 *
 *   ORDER. `detail.fields` is built by walking `Object.entries(INHERITABLE)` —
 *   insertion order — while the fingerprint sorts a copy. A Go map would
 *   randomise the first and leave the second right, which is precisely the shape
 *   of bug a single-implementation test never sees.
 *
 *   node tools/wifiguard-cases.js            # write testdata/wifiguard-cases.json
 *   node tools/wifiguard-cases.js --check    # fail if it is stale
 */

const fs   = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT  = process.env.WIFIGUARD_OUT || path.join(__dirname, '..', 'testdata', 'wifiguard-cases.json');

const G = require(path.join(ROOT, 'src', 'routeros', 'wifiGuard.js'));
if (typeof G.checkInherit !== 'function') {
  console.error('src/routeros/wifiGuard.js no longer exports checkInherit — this generator was ' +
                'pinning a function that has moved. Find it before regenerating.');
  process.exit(1);
}
// The field list and its ORDER are both load-bearing; a silent reorder upstream
// would change `detail.fields` on every warning.
const LIVE_ORDER = Object.keys(G.INHERITABLE).join(',');
if (LIVE_ORDER !== 'ssid,authTypes,passphrase,band,frequency,width') {
  console.error('INHERITABLE changed to [' + LIVE_ORDER + '] — the port hard-codes the order.');
  process.exit(1);
}

// ── Interfaces ───────────────────────────────────────────────────────────────
//
// Two radios following one profile, a third on its own, and a fourth with no
// profile at all. Invented; no router has these.
const IF = (name, cfg, extra) => Object.assign({
  name, configuration: cfg,
  'configuration.ssid': 'HomeNet',
  'security.authentication-types': 'wpa2-psk,wpa3-psk',
  'security.passphrase': '',
  'channel.band': '5ghz-ax',
  'channel.frequency': '5180',
  'channel.width': '20/40/80mhz',
}, extra || {});

const SHARED_A = IF('wifi1', 'home');
const SHARED_B = IF('wifi2', 'home');
const LONE     = IF('wifi3', 'guest', { 'configuration.ssid': 'GuestNet' });
const NOPROF   = IF('wifi4', '');

// A SECOND shared pair, whose stored passphrase is NOT empty.
//
// This exists to pin the write-only branch, and nothing else can. Everywhere
// else `security.passphrase` reads back empty, so comparing it gives the same
// answer as the special case and the two implementations are indistinguishable.
// Here they differ: re-submitting the value the router already holds compares
// EQUAL — no change — while the rule says any non-empty passphrase is a change,
// because it cannot be read back and therefore cannot be compared.
const STORED_A = IF('wifi5', 'iot', { 'security.passphrase': 'stored-secret' });
const STORED_B = IF('wifi6', 'iot', { 'security.passphrase': 'stored-secret' });

const SIBLINGS = [SHARED_A, SHARED_B, LONE, NOPROF, STORED_A, STORED_B];

// ── Submissions ──────────────────────────────────────────────────────────────
//
// `undefined` is how a field is ABSENT; '' is how it is submitted empty. The
// two are different questions and the generator has to keep them apart.
const V = (o) => o;

const CASES = [
  { n: 'no change at all', before: SHARED_A, values: V({ ssid: 'HomeNet' }) },
  { n: 'ssid changed on a shared profile', before: SHARED_A, values: V({ ssid: 'NewNet' }) },
  { n: 'ssid changed on a LONE profile', before: LONE, values: V({ ssid: 'NewNet' }) },
  { n: 'ssid changed with NO profile', before: NOPROF, values: V({ ssid: 'NewNet' }) },
  { n: 'ssid cleared to empty', before: SHARED_A, values: V({ ssid: '' }) },
  { n: 'ssid absent from the submission', before: SHARED_A, values: V({ band: '5ghz-ax' }) },

  { n: 'passphrase set', before: SHARED_A, values: V({ passphrase: 'hunter2hunter2' }) },
  { n: 'passphrase submitted BLANK', before: SHARED_A, values: V({ passphrase: '' }) },
  { n: 'passphrase absent', before: SHARED_A, values: V({ ssid: 'HomeNet' }) },

  { n: 'band changed', before: SHARED_A, values: V({ band: '2ghz-ax' }) },
  { n: 'frequency changed', before: SHARED_A, values: V({ frequency: '5745' }) },
  { n: 'width changed', before: SHARED_A, values: V({ width: '20mhz' }) },
  { n: 'authTypes changed', before: SHARED_A, values: V({ authTypes: 'wpa3-psk' }) },

  // ORDER: several at once, submitted in an order that is NOT the declaration
  // order, so `detail.fields` proves it walks INHERITABLE rather than the input.
  { n: 'three at once, submitted out of order', before: SHARED_A,
    values: V({ width: '20mhz', ssid: 'NewNet', band: '2ghz-ax' }) },
  { n: 'every inheritable field at once', before: SHARED_A,
    values: V({ ssid: 'A', authTypes: 'wpa3-psk', passphrase: 'p', band: '2ghz-ax',
                frequency: '2412', width: '20mhz' }) },

  // Fields the guard does not know about cannot raise it.
  // The write-only branch, against a row that DOES hold a passphrase.
  { n: 'STORED passphrase re-submitted unchanged', before: STORED_A,
    values: V({ passphrase: 'stored-secret' }) },
  { n: 'STORED passphrase changed', before: STORED_A,
    values: V({ passphrase: 'a-new-secret' }) },
  { n: 'STORED passphrase submitted blank', before: STORED_A,
    values: V({ passphrase: '' }) },

  { n: 'only a comment changed', before: SHARED_A, values: V({ comment: 'note' }) },
  { n: 'disabled toggled', before: SHARED_A, values: V({ disabled: 'yes' }) },
];

const ACTIONS = ['create', 'update', 'delete', 'enable', 'disable'];

function run() {
  const out = [];
  for (const c of CASES) {
    for (const action of ACTIONS) {
      for (const [beforeLabel, before] of [['row', c.before], ['null', null]]) {
        const v = G.checkInherit({ values: c.values, before, siblings: SIBLINGS, action });
        out.push({
          name: c.n, action, before: beforeLabel,
          values: c.values,
          // The keys the submission actually carries. Go cannot tell an absent
          // field from an empty one, and the original turns on exactly that.
          valueKeys: Object.keys(c.values),
          want: {
            level: v.level, code: v.code || '',
            detail: v.detail ? {
              profile: v.detail.profile, sharedBy: v.detail.sharedBy,
              fields: v.detail.fields, iface: v.detail.interface,
            } : null,
            fingerprint: v.fingerprint || '',
          },
        });
      }
    }
  }
  return { siblings: SIBLINGS, order: LIVE_ORDER.split(','), cases: out };
}

const out = JSON.stringify(run(), null, 2) + '\n';
const n = run();
const warned = n.cases.filter((c) => c.want.level === 'warn').length;
const counts = `${n.cases.length} decisions, ${warned} warnings`;

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== out) {
    console.error('testdata/wifiguard-cases.json is stale — run: node tools/wifiguard-cases.js');
    process.exit(1);
  }
  console.log(`wifiguard-cases up to date (${counts})`);
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, out);
  console.log(`wrote ${path.relative(process.cwd(), OUT)} — ${counts}`);
}
