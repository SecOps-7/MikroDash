'use strict';
/**
 * `_parseScanIfaces` — the interface catalogue the Frequency Analyser's dialog
 * is drawn from.
 *
 * ---- THE ENDPOINT RESTRICTION IS A REFUSAL, NOT A GAP ----------------------
 *
 * It returns NOTHING for the legacy `/interface/wireless` stack, and the live
 * comment says why: "its scan command differs and there is no device here to
 * verify it against. Report none rather than offering a picker that cannot
 * work." A port that treated both stacks alike would put a working-looking
 * button in front of an operator on legacy hardware, and the failure would come
 * after the radio was already off the air.
 *
 * ---- AND EVERY FLAG IS A STRING FROM THE WIRE ------------------------------
 *
 * RouterOS answers `"true"` and `"false"`, not booleans, so each flag is
 * `r.x === 'true' || r.x === true`. The second half matters because a fixture
 * replayed through JSON can carry a real boolean. Reading `!!r.disabled` instead
 * would make the STRING `"false"` truthy — every radio would read as disabled
 * and the dialog would offer none of them.
 *
 * `capsmanManaged` is the exception: it is `!!r['configuration.manager']`, a
 * presence test on a name, not a boolean. A dotted key, which is easy to lose to
 * a naive struct mapping.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/wifiscan-catalogue-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

// The live method's body, applied to explicit inputs. It is an instance method
// on a collector that needs a router to construct, so what is reproduced is the
// body — and the endpoint constant comes from the live module rather than being
// retyped.
const wireless = fs.readFileSync(path.join(SRC, 'src', 'collectors', 'wireless.js'), 'utf8');
// ANCHORED ON THE OBJECT, NOT ON THE KEY — corrected 2026-08-29.
//
// This was `/wifi:\s*'([^']+)'/`, which matches the FIRST `wifi:` in the file.
// That is `WL_ENDPOINTS.wifi`, the registration table, twelve lines above the
// `SSID_ENDPOINTS.wifi` this wants. So the constant lifted into the port was
// `/interface/wifi/registration-table/print` where live's guard tests
// `/interface/wifi/print`.
//
// It was invisible for as long as the port's only caller passed the constant to
// itself: `ParseCatalogue(rows, wifiscan.WifiEndpoint)` compares the constant
// against itself, so the guard never fired and any value would have passed. The
// corpus agreed because it made the identical substitution on both sides — a
// round trip through one wrong value agrees with itself.
//
// It surfaced the moment a caller passed the endpoint that ACTUALLY answered,
// which is what the collector knows. `assert` on the shape, so a rename upstream
// fails here rather than silently lifting the neighbouring object again.
const SSID_BLOCK = (wireless.match(/const SSID_ENDPOINTS = \{[\s\S]*?\}/) || [])[0];
assert.ok(SSID_BLOCK, 'could not find SSID_ENDPOINTS in the live collector');
const WIFI_ENDPOINT = (SSID_BLOCK.match(/wifi:\s*'([^']+)'/) || [])[1];
assert.ok(WIFI_ENDPOINT, 'could not read SSID_ENDPOINTS.wifi out of the live collector');
assert.ok(/\/interface\/wifi\/print$/.test(WIFI_ENDPOINT),
  `SSID_ENDPOINTS.wifi is ${WIFI_ENDPOINT}, which is not an interface-print menu — ` +
  'the lift has probably grabbed a neighbouring object again');

function parseScanIfaces(rows, endpoint) {
  if (endpoint !== WIFI_ENDPOINT) return [];
  return (rows || [])
    .filter((r) => r && String(r.name || '').trim())
    .map((r) => ({
      name: String(r.name).trim(),
      id: r['.id'] || null,
      master: r.master === 'true' || r.master === true,
      masterInterface: r['master-interface'] || null,
      capsmanManaged: !!r['configuration.manager'],
      disabled: r.disabled === 'true' || r.disabled === true,
      running: r.running === 'true' || r.running === true,
    }));
}

const ROWS = [
  // A plain master radio, as RouterOS actually answers: strings throughout.
  { '.id': '*1', name: 'wifi1', master: 'true', running: 'true', disabled: 'false' },
  // A virtual AP riding on it.
  { '.id': '*2', name: 'wifi1-guest', master: 'false', 'master-interface': 'wifi1',
    running: 'true', disabled: 'false' },
  // CAPsMAN-managed: a NAME under a dotted key, not a boolean.
  { '.id': '*3', name: 'capsman-ap', master: 'true', running: 'true', disabled: 'false',
    'configuration.manager': 'capsman' },
  // Disabled, and not running.
  { '.id': '*4', name: 'wifi-off', master: 'true', running: 'false', disabled: 'true' },
  // Real booleans, which a JSON-replayed fixture can carry.
  { '.id': '*5', name: 'wifi-bool', master: true, running: true, disabled: false },
  // Names that need trimming, and ones that must be dropped entirely.
  { '.id': '*6', name: '  padded  ', master: 'true', running: 'true' },
  { '.id': '*7', name: '   ', master: 'true' },
  { '.id': '*8', name: '', master: 'true' },
  { '.id': '*9', master: 'true' },
  null,
  // No `.id`: the scan is addressed by id, so this radio cannot be targeted.
  { name: 'no-id', master: 'true', running: 'true' },
  // An empty configuration.manager is NOT managed — a presence test on a name.
  { '.id': '*10', name: 'unmanaged', master: 'true', 'configuration.manager': '' },
  // Absent flags default to false rather than throwing.
  { '.id': '*11', name: 'bare' },
];

const cases = [
  { name: 'the wifi stack', endpoint: WIFI_ENDPOINT, rows: ROWS,
    out: parseScanIfaces(ROWS, WIFI_ENDPOINT) },
  // THE REFUSAL.
  { name: 'the legacy wireless stack', endpoint: '/interface/wireless', rows: ROWS,
    out: parseScanIfaces(ROWS, '/interface/wireless') },
  { name: 'no endpoint at all', endpoint: null, rows: ROWS,
    out: parseScanIfaces(ROWS, null) },
  { name: 'no rows', endpoint: WIFI_ENDPOINT, rows: [], out: parseScanIfaces([], WIFI_ENDPOINT) },
  { name: 'null rows', endpoint: WIFI_ENDPOINT, rows: null, out: parseScanIfaces(null, WIFI_ENDPOINT) },
];

// ---- BELIEVABILITY -------------------------------------------------------
{
  const wifi = cases[0].out;
  const by = Object.fromEntries(wifi.map((r) => [r.name, r]));

  assert.equal(cases[1].out.length, 0,
    'the legacy wireless stack produced a catalogue — its scan command differs and there is no '
    + 'device to verify it against, so offering a picker would be worse than offering none');
  assert.equal(cases[2].out.length, 0, 'a missing endpoint produced a catalogue');
  assert.deepEqual(cases[4].out, [], 'null rows threw or produced entries');

  // DERIVED, not counted by hand: adding a row to ROWS must not silently make
  // this assertion describe a different corpus. (It said `- 5` first, and the
  // generator refused to run until the number matched the rows.)
  const unusable = ROWS.filter((r) => !r || !String(r.name || '').trim()).length;
  assert.equal(wifi.length, ROWS.length - unusable,
    'the wrong number of rows survived the name filter: ' + wifi.length);
  assert.ok(unusable >= 3,
    'fewer than three rows have no usable name, so the filter is barely exercised');
  assert.ok(!wifi.some((r) => r.name === ''), 'a blank name survived');

  assert.equal(by['padded'].name, 'padded', 'a padded name was not trimmed');

  // The string coercions, which is where a port goes wrong.
  assert.equal(by['wifi1'].master, true, '"true" did not read as true');
  assert.equal(by['wifi1'].disabled, false, '"false" read as disabled');
  assert.equal(by['wifi-bool'].master, true, 'a real boolean true did not read as true');
  assert.equal(by['wifi-bool'].disabled, false, 'a real boolean false read as disabled');
  assert.equal(by['bare'].master, false, 'an absent flag did not default to false');
  assert.equal(by['bare'].running, false, 'an absent running flag did not default to false');

  assert.equal(by['capsman-ap'].capsmanManaged, true,
    'a configuration.manager name did not mark the radio managed');
  assert.equal(by['unmanaged'].capsmanManaged, false,
    'an EMPTY configuration.manager marked the radio managed');
  assert.equal(by['wifi1'].capsmanManaged, false, 'an absent manager marked the radio managed');

  assert.equal(by['wifi1-guest'].masterInterface, 'wifi1', 'the master-interface was lost');
  assert.equal(by['wifi1'].masterInterface, null, 'a master gained a master-interface');
  assert.equal(by['no-id'].id, null, 'a row with no .id did not report a null id');
  assert.equal(by['wifi1'].id, '*1', 'the .id was lost');
}

const OUT = path.join(ROOT, 'testdata', 'wifiscan-catalogue-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/wifiscan-catalogue-cases.js from the live wireless collector. Do not edit.',
  wifiEndpoint: WIFI_ENDPOINT, cases,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('wifiscan-catalogue-cases: testdata/wifiscan-catalogue-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('wifiscan-catalogue-cases: up to date');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('wifiscan-catalogue-cases: wrote ' + cases.length + ' cases (endpoint ' + WIFI_ENDPOINT + ')');
}
