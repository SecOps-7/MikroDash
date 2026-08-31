'use strict';
/**
 * WHICH RADIOS CAN BE SCANNED, and how many clients each would drop.
 *
 * `listScannableInterfaces` in the live wireless collector is what the Frequency
 * Analyser's dialog is built from: the list it offers, and — through the client
 * count — the warning it shows before taking a radio off the air.
 *
 * ---- THE CLIENT COUNT IS THE POINT ----------------------------------------
 *
 * A scan disconnects everyone on the radio, and "everyone" includes clients of
 * the VIRTUAL APs riding on it. The live code rolls those up:
 *
 *   for (const v of all) if (v.masterInterface === radio.name) clients += perIface.get(v.name) || 0;
 *
 * So a master with two guest SSIDs reports the sum of all three. A port that
 * counted only the master's own clients would show "3 clients" before dropping
 * thirty, which is worse than showing nothing — the operator would have made the
 * decision on a number the interface invented.
 *
 * ---- AND THE FILTER -------------------------------------------------------
 *
 * `master && !capsmanManaged && !disabled`. Each exclusion is a different
 * reason: a virtual AP has no radio of its own to scan with, a CAPsMAN-managed
 * radio is not this router's to disrupt, and a disabled one is not on the air to
 * begin with.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/wifiscan-ifaces-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');

/**
 * The live method, applied to explicit inputs.
 *
 * It is an instance method reading two private fields, so there is no seam to
 * call — what is reproduced is its BODY, and the corpus's value is that the
 * expectations come from running it rather than from reading it.
 */
function listScannable(all, clients) {
  const perIface = new Map();
  for (const c of clients) {
    if (!c || !c.iface) continue;
    perIface.set(c.iface, (perIface.get(c.iface) || 0) + 1);
  }
  return all.filter((i) => i.master && !i.capsmanManaged && !i.disabled).map((radio) => {
    let n = perIface.get(radio.name) || 0;
    for (const v of all) {
      if (v.masterInterface === radio.name) n += perIface.get(v.name) || 0;
    }
    return { name: radio.name, running: !!radio.running, clients: n };
  });
}

const iface = (name, over = {}) => ({
  name, master: true, capsmanManaged: false, disabled: false,
  running: true, masterInterface: '', ...over,
});
const client = (n, on) => Array.from({ length: n }, () => ({ iface: on }));

const CASES = {
  'one radio, no clients': { all: [iface('wifi1')], clients: [] },
  'one radio with clients': { all: [iface('wifi1')], clients: client(3, 'wifi1') },
  // THE ROLL-UP: two guest SSIDs on one radio. A scan drops all eleven.
  'virtual APs roll up into their master': {
    all: [iface('wifi1'),
          iface('wifi1-guest', { master: false, masterInterface: 'wifi1' }),
          iface('wifi1-iot', { master: false, masterInterface: 'wifi1' })],
    clients: [...client(3, 'wifi1'), ...client(5, 'wifi1-guest'), ...client(3, 'wifi1-iot')],
  },
  // A virtual AP pointing at a radio that is NOT in the list contributes to
  // nobody, and must not crash or be counted twice.
  'a virtual AP whose master is absent': {
    all: [iface('wifi2'), iface('orphan', { master: false, masterInterface: 'gone' })],
    clients: [...client(2, 'wifi2'), ...client(9, 'orphan')],
  },
  // Each exclusion, separately.
  'a virtual AP is not offered': {
    all: [iface('wifi1'), iface('wifi1-guest', { master: false, masterInterface: 'wifi1' })],
    clients: [],
  },
  'a capsman-managed radio is not offered': {
    all: [iface('wifi1'), iface('capsman-ap', { capsmanManaged: true })], clients: [],
  },
  'a disabled radio is not offered': {
    all: [iface('wifi1'), iface('wifi-off', { disabled: true })], clients: [],
  },
  // A disabled MASTER excludes itself but its virtuals still roll up nowhere.
  'a disabled master with virtuals': {
    all: [iface('wifi1', { disabled: true }),
          iface('wifi1-guest', { master: false, masterInterface: 'wifi1' })],
    clients: client(4, 'wifi1-guest'),
  },
  'two radios, clients on each': {
    all: [iface('wifi1'), iface('wifi2-5GHz')],
    clients: [...client(2, 'wifi1'), ...client(7, 'wifi2-5GHz')],
  },
  'a radio that is not running': {
    all: [iface('wifi1', { running: false })], clients: client(1, 'wifi1'),
  },
  'clients on an interface nobody knows': {
    all: [iface('wifi1')], clients: [...client(1, 'wifi1'), ...client(6, 'ghost')],
  },
  // A BLANK interface name on both sides. The live guard is
  // `if (!c || !c.iface) continue`, and without it a nameless client lands in
  // the map under "" -- where a nameless virtual AP would then collect it and
  // roll it up into whatever radio it claims as its master. Marginal, but a
  // catalogue row with no name is a router's answer, not this code's choice.
  'a nameless client and a nameless virtual AP': {
    all: [iface('wifi1'), iface('', { master: false, masterInterface: 'wifi1' })],
    clients: [{ iface: '' }, { iface: '' }, { iface: 'wifi1' }],
  },
  'nothing at all': { all: [], clients: [] },
};

const cases = Object.entries(CASES).map(([name, { all, clients }]) => ({
  name, all, clients: clients.map((c) => c.iface), out: listScannable(all, clients),
}));

// ---- BELIEVABILITY -------------------------------------------------------
{
  const by = Object.fromEntries(cases.map((c) => [c.name, c.out]));

  assert.equal(by['one radio, no clients'].length, 1, 'a plain radio was not offered');
  assert.equal(by['one radio with clients'][0].clients, 3, 'the client count is not being read');

  // The roll-up, which is the whole reason this is not a filter.
  assert.equal(by['virtual APs roll up into their master'].length, 1,
    'the virtual APs were offered as scannable radios');
  assert.equal(by['virtual APs roll up into their master'][0].clients, 11,
    'the virtual APs\' clients were not rolled up — the dialog would warn about 3 and drop 11');

  assert.equal(by['a virtual AP whose master is absent'][0].clients, 2,
    'an orphaned virtual AP\'s clients were attributed to an unrelated radio');

  assert.equal(by['a capsman-managed radio is not offered'].length, 1,
    'a CAPsMAN-managed radio was offered — it is not this router\'s to disrupt');
  assert.equal(by['a disabled radio is not offered'].length, 1,
    'a disabled radio was offered');
  assert.equal(by['a disabled master with virtuals'].length, 0,
    'a disabled master was offered anyway');

  assert.equal(by['a radio that is not running'][0].running, false,
    'a stopped radio is reported as running');
  assert.equal(by['clients on an interface nobody knows'][0].clients, 1,
    'clients on an unknown interface were attributed to a radio');
  assert.deepEqual(by['nothing at all'], [], 'an empty catalogue produced entries');
  assert.equal(by['a nameless client and a nameless virtual AP'][0].clients, 1,
    'nameless clients were counted -- the `!c.iface` guard is what stops them being '
    + 'rolled up through a nameless virtual AP');
}

const OUT = path.join(ROOT, 'testdata', 'wifiscan-ifaces-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/wifiscan-ifaces-cases.js from the live listScannableInterfaces. Do not edit.',
  cases,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('wifiscan-ifaces-cases: testdata/wifiscan-ifaces-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('wifiscan-ifaces-cases: up to date');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('wifiscan-ifaces-cases: wrote ' + cases.length + ' cases');
}
