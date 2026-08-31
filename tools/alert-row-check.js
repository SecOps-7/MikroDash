'use strict';
/**
 * `_alertRow` — the shape every alert reaches the browser in.
 *
 * The bell's rows, the payload of `alert:acked`, and the two lists inside
 * `alerts:open` are all this object. Three emit sites building it by hand is
 * three chances for one of them to omit `routerName`, which is the whole
 * difficulty with three identical update alerts in one bell — so the live side
 * has one function and so does the port.
 *
 * ---- IT IS LIFTED, NOT RETYPED --------------------------------------------
 *
 * `_alertRow` is private to `src/index.js`; there is no export to require. So it
 * is sliced out of the file with `lib/lift.js` and evaluated, together with
 * `alerter.labelFor`, which it calls. Retyping either would make this gate
 * compare the port against MY READING of the live code — and that has already
 * gone wrong once in this port, when a hand-copied `esc` used `&#39;` where the
 * live one uses `&#039;` and the gate blamed the port for my transcription.
 *
 * ---- WHAT IT ACTUALLY DISCRIMINATES ---------------------------------------
 *
 * Every nullable field goes through `x || null`, so "" arrives as null. The
 * corpus carries an empty subject, an empty detail and an empty acknowledgedBy
 * for that reason: they are indistinguishable from absent once rendered, but
 * `acknowledgedBy` reaches Reports, where "" is a person with no name and null
 * is the evaluator.
 *
 * `routerName` is `(names && rid && names.get(rid)) || null` — FOUR ways to get
 * null, and the corpus has all four: no map, no router id, no entry, and an
 * entry whose label is "".
 *
 * Runs on the host: `src/index.js` is never loaded, only read.
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/alert-row-check.js --freeze
const G = L.golden('alert-row-check');

const ROOT = path.join(__dirname, '..');
const indexSrc = L.liveSource(ROOT, path.join('src', 'index.js'));
const alerterSrc = L.liveSource(ROOT, path.join('src', 'alerter.js'));

// The two live functions, sliced from their files.
const alertRowSrc = G.value('alertRowSrc', () => L.whole(indexSrc, 'function _alertRow(r, names)'));
const labelForSrc = G.value('labelForSrc', () => L.whole(alerterSrc, 'function labelFor(alertType)'));
const labelsSrc = G.value('labelsSrc', () => L.whole(alerterSrc, 'const ALERT_LABELS = '));
const acronymsSrc = G.value('acronymsSrc', () => L.whole(alerterSrc, 'const _LABEL_ACRONYMS = '));

assert.ok(/routerName:/.test(alertRowSrc), 'the lifted _alertRow has no routerName — the slice '
  + 'stopped early and this gate would compare the port against a fragment');
assert.ok(/labelFor/.test(alertRowSrc), 'the lifted _alertRow does not call labelFor');

// eslint-disable-next-line no-new-func
const live = new Function(
  labelsSrc + '\n' + acronymsSrc + '\n' + labelForSrc + '\n'
  + 'const alerter = { labelFor };\n' + alertRowSrc + '\n'
  + 'return { _alertRow, labelFor };')();

/**
 * The rows. Database shape — snake_case, nullable columns — because that is what
 * `_alertRow` is handed.
 */
const ROWS = [
  { name: 'ordinary', row: { id: 1, router_id: 'r1', alert_type: 'cpu', subject: 'CPU',
    detail: 'high', fired_at: 1000, resolved_at: null, acknowledged_at: null,
    acknowledged_by: null } },
  // Every nullable column NULL.
  { name: 'all-null', row: { id: 2, router_id: 'r1', alert_type: 'link', subject: null,
    detail: null, fired_at: 2000, resolved_at: null, acknowledged_at: null,
    acknowledged_by: null } },
  // Every nullable column EMPTY — `x || null` makes these identical to all-null.
  { name: 'all-empty', row: { id: 3, router_id: 'r1', alert_type: 'link', subject: '',
    detail: '', fired_at: 3000, resolved_at: null, acknowledged_at: null,
    acknowledged_by: '' } },
  // Resolved and acknowledged.
  { name: 'closed-acked', row: { id: 4, router_id: 'r1', alert_type: 'routeros_update',
    subject: 'RouterOS', detail: '7.24', fired_at: 4000, resolved_at: 5000,
    acknowledged_at: 4500, acknowledged_by: 'alice' } },
  // A row whose router is NOT in the name map — a deleted router's alerts still
  // render, without a name.
  { name: 'unknown-router', row: { id: 5, router_id: 'gone', alert_type: 'connectivity',
    subject: null, detail: null, fired_at: 6000, resolved_at: null, acknowledged_at: null,
    acknowledged_by: null } },
  // No router id at all.
  { name: 'no-router-id', row: { id: 6, router_id: null, alert_type: 'bgp_down',
    subject: null, detail: null, fired_at: 7000, resolved_at: null, acknowledged_at: null,
    acknowledged_by: null } },
  // A router whose LABEL is empty: `|| null` again, one level further in.
  { name: 'blank-label', row: { id: 7, router_id: 'blank', alert_type: 'vpn_down',
    subject: null, detail: null, fired_at: 8000, resolved_at: null, acknowledged_at: null,
    acknowledged_by: null } },
  // Label derivation: an unmapped type, and the acronyms that stop it reading
  // "Bgp" and "Ok".
  { name: 'unmapped-type', row: { id: 8, router_id: 'r1', alert_type: 'ping_loss_ok',
    subject: null, detail: null, fired_at: 9000, resolved_at: null, acknowledged_at: null,
    acknowledged_by: null } },
];

// FOUR name maps, so every route to a null `routerName` is exercised.
const MAPS = {
  full: new Map([['r1', 'Office'], ['blank', '']]),
  empty: new Map(),
  none: null,
};

const cases = {};
for (const { name, row } of ROWS) {
  for (const mapName of Object.keys(MAPS)) {
    cases[name + '/' + mapName] = live._alertRow(row, MAPS[mapName]);
  }
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  const c = (k) => cases[k];
  assert.equal(c('ordinary/full').routerName, 'Office',
    'the name map did nothing, so every routerName case below proves nothing');
  assert.equal(c('ordinary/empty').routerName, null);
  assert.equal(c('ordinary/none').routerName, null);
  assert.equal(c('unknown-router/full').routerName, null,
    'a router missing from the map got a name');
  assert.equal(c('blank-label/full').routerName, null,
    'a router whose label is "" got an empty name rather than null');
  assert.equal(c('no-router-id/full').routerName, null);

  // `x || null` collapses "" and null.
  assert.deepEqual(
    { s: c('all-empty/full').subject, d: c('all-empty/full').detail,
      a: c('all-empty/full').acknowledgedBy },
    { s: null, d: null, a: null },
    'an empty string survived as "" — in Reports that is a person with no name');
  assert.equal(c('all-null/full').subject, null);

  // The label is DERIVED and differs from the key, or this gate would pass on a
  // port that simply echoed alert_type.
  assert.equal(c('closed-acked/full').label, 'Update Available');
  assert.notEqual(c('closed-acked/full').label, c('closed-acked/full').alertType,
    'the label equals the raw key here, so nothing distinguishes deriving it from '
    + 'echoing it');
  assert.equal(c('unmapped-type/full').label, 'Ping Loss OK',
    'the acronym table did not fire — an unmapped type should not read "Ok"');

  // Timestamps survive, and zero is not turned into null anywhere they matter.
  assert.equal(c('closed-acked/full').resolvedAt, 5000);
  assert.equal(c('closed-acked/full').acknowledgedAt, 4500);
  assert.equal(c('ordinary/full').resolvedAt, null);
}

// ---- THE CORPUS ----------------------------------------------------------
//
// Every expected value here came out of the LIVE function a few lines above, so
// the Go test consuming it compares the port against what `src/index.js` does
// rather than against what I believe it does. The inputs ride along so the Go
// side drives the same rows through the same four maps.
const goInput = ROWS.map(({ name, row }) => ({ name, row }));
const OUT = path.join(ROOT, 'testdata', 'alert-row-cases.json');
const json = JSON.stringify({ rows: goInput, maps: {
  full: [['r1', 'Office'], ['blank', '']], empty: [], none: null,
}, cases }, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('alert-row-cases.json is STALE — regenerate with `node tools/alert-row-check.js`');
    process.exit(1);
  }
  console.log('alert-row-cases.json is current (' + Object.keys(cases).length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, json);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' cases)');
}
