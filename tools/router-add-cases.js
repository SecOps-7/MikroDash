'use strict';
/**
 * `Routers.add` — what a new router record actually becomes.
 *
 * Twelve defaults, several validators and three sub-normalisers, all applied to
 * a body the browser sends. A port that guessed any of them produces a record
 * that looks right in the form and behaves differently: the wrong default
 * interface watches the wrong link, a missing `alertsEnabled` silently disables
 * alerting for that device, and a `connDownThresholdSec` outside its range falls
 * back to 30 rather than being refused.
 *
 * ── TWO FIELDS CANNOT BE COMPARED DIRECTLY, AND ARE OMITTED ────────────────
 *
 * `id` is a fresh UUID and `addedAt` is `Date.now()`, so a corpus holding either
 * literally would differ on every run and `--check` could never pass. Both are
 * dropped from the recorded record, and their SHAPE is asserted here instead — a
 * v4 UUID, unique across the run, and a timestamp inside the window the
 * generator ran in. That is the whole of what a port can be held to for a value
 * it is supposed to invent.
 *
 * ── AND THE LABEL IS NOT A DEFAULT, IT IS A FUNCTION OF THE FLEET ──────────
 *
 * `_uniqueLabel` appends a counter when the label is taken, so adding two
 * routers called "Depot" gives "Depot" and something else. That depends on what
 * is already in the file, which is why the cases run in ORDER against one
 * growing store rather than independently. A port that treated the label as a
 * pure function of the body would collide silently and the dropdown would show
 * two identical entries.
 *
 * Runs on the host: DATA_DIR is pointed at a temp directory BEFORE routers.js is
 * required, because that module resolves its path at load time and would
 * otherwise write into the real /data.
 */

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const OUT = process.env.ROUTER_ADD_OUT
  || path.join(ROOT, 'testdata', 'router-add-cases.json');

// BEFORE the require, not after.
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-radd-'));
process.env.DATA_DIR = TMP;

const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const Routers = require(path.join(SRC, 'src', 'routers.js'));

/**
 * The bodies, in the ORDER they are added. Order matters for the label cases.
 *
 * `ok: false` marks a body the live code REFUSES — the validators throw rather
 * than returning, and the route turns that into an error. A port that accepted
 * any of them writes a record the live app would not.
 */
const CASES = [
  // The minimum a caller can send. Everything else is a default.
  { name: 'minimal', body: { host: '198.51.100.1' } },

  // Every field supplied, so no default is exercised and the values must
  // survive verbatim.
  { name: 'fullySpecified', body: {
    host: '198.51.100.2', port: 8728, label: 'Depot', tls: false, tlsInsecure: true,
    username: 'api-ro', password: 'PLACEHOLDER', defaultIf: 'ether2',
    pingTarget: '192.0.2.1', bwDownMbps: 500, bwUpMbps: 100,
    alertsEnabled: true, connDownThresholdSec: 60, siteIds: ['site-a', 'site-b'],
  } },

  // THE LABEL IS A FUNCTION OF THE FLEET. "Depot" is taken by the case above.
  { name: 'duplicateLabel', body: { host: '198.51.100.3', label: 'Depot' } },
  { name: 'duplicateLabelAgain', body: { host: '198.51.100.4', label: 'Depot' } },

  // `tls` defaults TRUE and is only false for a literal false or the string
  // "false" — a form posts strings, so both spellings reach here.
  { name: 'tlsStringFalse', body: { host: '198.51.100.5', tls: 'false' } },
  { name: 'tlsStringTrue', body: { host: '198.51.100.6', tls: 'true' } },
  { name: 'tlsAbsent', body: { host: '198.51.100.7' } },
  // ...and tlsInsecure defaults FALSE, with the mirrored string rule.
  { name: 'tlsInsecureStringTrue', body: { host: '198.51.100.8', tlsInsecure: 'true' } },
  // ── THE STRING "false", AND WHY IT WAS MISSING ──────────────────────────
  //
  // `tls` had `tlsStringFalse` from the start and `tlsInsecure` did not, and
  // that asymmetry is exactly where the defect lived: upstream read it as
  // `!!(x || x === 'true')`, so the STRING "false" — a form's own spelling of
  // off — turned certificate checking OFF while claiming to turn it on. Four
  // sites carried it (`dccbf62`).
  //
  // A corpus that never sends the value cannot see the rule invert. This one
  // now does, and it is the reason the generator stayed `--check` clean across
  // an upstream behaviour change.
  { name: 'tlsInsecureStringFalse', body: { host: '198.51.100.9', tlsInsecure: 'false' } },
  { name: 'tlsInsecureLiteralFalse', body: { host: '198.51.100.10', tlsInsecure: false } },
  { name: 'tlsInsecureAbsent', body: { host: '198.51.100.11' } },
  // JUNK IS STRICT, NOT CONSENT. Under the old coercion every one of these was
  // truthy and silently relaxed certificate checking. Pinned so a future
  // "be helpful about spellings" change has to argue with a test.
  { name: 'tlsInsecureOne', body: { host: '198.51.100.12', tlsInsecure: '1' } },
  { name: 'tlsInsecureYes', body: { host: '198.51.100.13', tlsInsecure: 'yes' } },
  { name: 'tlsInsecureUpperTrue', body: { host: '198.51.100.14', tlsInsecure: 'TRUE' } },
  { name: 'tlsInsecureOn', body: { host: '198.51.100.15', tlsInsecure: 'on' } },

  // The MASK is refused, so re-submitting an unchanged form does not store eight
  // bullets as the password.
  { name: 'maskedPassword', body: { host: '198.51.100.9', password: '••••••••' } },
  { name: 'emptyPassword', body: { host: '198.51.100.10', password: '' } },

  // The bandwidth figures have a FLOOR of 1 and a fallback of 1000 — `|| 1000`
  // makes a parsed 0 fall back rather than clamp.
  { name: 'bwZero', body: { host: '198.51.100.11', bwDownMbps: 0, bwUpMbps: 0 } },
  { name: 'bwNegative', body: { host: '198.51.100.12', bwDownMbps: -5, bwUpMbps: -5 } },
  { name: 'bwUnparseable', body: { host: '198.51.100.13', bwDownMbps: 'fast', bwUpMbps: 'slow' } },
  { name: 'bwStrings', body: { host: '198.51.100.14', bwDownMbps: '250', bwUpMbps: '50' } },

  // connDownThresholdSec accepts 0..300 INCLUSIVE and falls back to 30 outside.
  // Zero is a legitimate value, so a truthiness test would silently change it.
  { name: 'connDownZero', body: { host: '198.51.100.15', connDownThresholdSec: 0 } },
  { name: 'connDownMax', body: { host: '198.51.100.16', connDownThresholdSec: 300 } },
  { name: 'connDownOver', body: { host: '198.51.100.17', connDownThresholdSec: 301 } },
  { name: 'connDownNegative', body: { host: '198.51.100.18', connDownThresholdSec: -1 } },
  { name: 'connDownAbsent', body: { host: '198.51.100.19' } },

  // Site membership accepts EITHER shape, and `siteId` is a write-only mirror of
  // the primary.
  { name: 'siteIdsArray', body: { host: '198.51.100.20', siteIds: ['s1', 's2'] } },
  { name: 'siteIdScalar', body: { host: '198.51.100.21', siteId: 's9' } },
  { name: 'siteIdsEmpty', body: { host: '198.51.100.22', siteIds: [] } },
  { name: 'siteNone', body: { host: '198.51.100.23' } },

  // Whitespace is trimmed; a long label is cut to 64 before the uniqueness pass.
  { name: 'paddedFields', body: { host: '  198.51.100.24  ', username: '  op  ',
    label: '   Padded   ' } },
  { name: 'longLabel', body: { host: '198.51.100.25', label: 'L'.repeat(120) } },

  // ---- REFUSED ----
  { name: 'noHost', body: {}, ok: false },
  { name: 'emptyHost', body: { host: '   ' }, ok: false },
  { name: 'portZero', body: { host: '198.51.100.26', port: 0 }, ok: false },
  { name: 'portTooHigh', body: { host: '198.51.100.27', port: 70000 }, ok: false },
];

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

const startedAt = Date.now();
const cases = {};
const seenIDs = new Set();

for (const c of CASES) {
  let entry = null;
  let refused = null;
  try {
    entry = Routers.add(c.body);
  } catch (e) {
    refused = String(e && e.message ? e.message : e);
  }

  if (c.ok === false) {
    assert.ok(refused, c.name + ': the live code ACCEPTED a body this corpus records as '
      + 'refused, so the port would be held to the wrong answer');
    cases[c.name] = { body: c.body, refused: true };
    continue;
  }
  assert.ok(entry, c.name + ': refused unexpectedly (' + refused + ')');

  // ---- the two invented fields ----
  assert.ok(UUID_RE.test(entry.id), c.name + ': id ' + entry.id + ' is not a v4 UUID');
  assert.ok(!seenIDs.has(entry.id), c.name + ': id collides with an earlier one');
  seenIDs.add(entry.id);
  assert.ok(entry.addedAt >= startedAt && entry.addedAt <= Date.now(),
    c.name + ': addedAt ' + entry.addedAt + ' is outside the run window');

  const rec = { ...entry };
  delete rec.id;
  delete rec.addedAt;
  cases[c.name] = { body: c.body, record: rec };
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  const r = (k) => cases[k].record;

  // The defaults a minimal body gets. If any of these is wrong the port is being
  // held to a fiction, so they are named rather than trusted.
  const min = r('minimal');
  assert.equal(min.port, 8729, 'the default port is not 8729');
  assert.equal(min.tls, true, 'tls does not default true');
  assert.equal(min.tlsInsecure, false);
  assert.equal(min.username, 'admin');
  assert.equal(min.password, '');
  assert.equal(min.defaultIf, 'ether1');
  assert.equal(min.pingTarget, '1.1.1.1');
  assert.equal(min.bwDownMbps, 1000);
  assert.equal(min.bwUpMbps, 1000);
  assert.equal(min.alertsEnabled, false);
  assert.equal(min.connDownThresholdSec, 30);
  assert.equal(min.disabled, false);
  assert.deepEqual(min.siteIds, []);
  assert.equal(min.siteId, null);
  assert.equal(min.label, '198.51.100.1', 'an unlabelled router is named after its host');

  // Nothing is defaulted when everything is given.
  const full = r('fullySpecified');
  assert.equal(full.port, 8728);
  assert.equal(full.tls, false);
  assert.equal(full.tlsInsecure, true);
  assert.equal(full.username, 'api-ro');
  assert.equal(full.defaultIf, 'ether2');
  assert.equal(full.connDownThresholdSec, 60);
  assert.deepEqual(full.siteIds, ['site-a', 'site-b']);
  assert.equal(full.siteId, 'site-a', 'siteId is not the FIRST of siteIds');

  // THE LABEL DEPENDS ON THE FLEET.
  assert.equal(full.label, 'Depot');
  assert.notEqual(r('duplicateLabel').label, 'Depot',
    'a duplicate label was accepted verbatim — two identical entries in the dropdown');
  assert.notEqual(r('duplicateLabelAgain').label, r('duplicateLabel').label,
    'the third "Depot" reused the second label');

  // tls, and the string spellings a form posts.
  assert.equal(r('tlsStringFalse').tls, false, "the string 'false' did not turn tls off");
  assert.equal(r('tlsStringTrue').tls, true);
  assert.equal(r('tlsAbsent').tls, true);
  assert.equal(r('tlsInsecureStringTrue').tlsInsecure, true);
  // A SECURITY PREDICATE, so each of these is asserted individually rather than
  // in a loop: the failure message has to name which spelling went wrong.
  assert.equal(r('tlsInsecureStringFalse').tlsInsecure, false,
    'the STRING "false" relaxed the certificate check — the dccbf62 defect is back');
  assert.equal(r('tlsInsecureLiteralFalse').tlsInsecure, false);
  assert.equal(r('tlsInsecureAbsent').tlsInsecure, false);
  assert.equal(r('tlsInsecureOne').tlsInsecure, false, "'1' is not consent");
  assert.equal(r('tlsInsecureYes').tlsInsecure, false, "'yes' is not consent");
  assert.equal(r('tlsInsecureUpperTrue').tlsInsecure, false,
    "'TRUE' is not consent — the live rule is the exact lowercase word");
  assert.equal(r('tlsInsecureOn').tlsInsecure, false, "'on' is not consent");

  // The mask is refused.
  assert.equal(r('maskedPassword').password, '',
    'the MASK was stored as the password — the router would stop authenticating '
    + 'and the form would still show it as configured');
  assert.equal(r('emptyPassword').password, '');

  // The bandwidth floor and the fallback.
  assert.equal(r('bwZero').bwDownMbps, 1000, '`|| 1000` makes a parsed 0 fall BACK, not clamp');
  assert.equal(r('bwNegative').bwDownMbps, 1, 'the floor of 1 was not applied');
  assert.equal(r('bwUnparseable').bwDownMbps, 1000);
  assert.equal(r('bwStrings').bwDownMbps, 250, 'a numeric string was not parsed');

  // The inclusive range, and zero surviving.
  assert.equal(r('connDownZero').connDownThresholdSec, 0,
    'zero fell back to 30 — a truthiness test would do exactly that, and 0 is a '
    + 'legitimate setting meaning "report immediately"');
  assert.equal(r('connDownMax').connDownThresholdSec, 300, 'the upper bound is not inclusive');
  assert.equal(r('connDownOver').connDownThresholdSec, 30);
  assert.equal(r('connDownNegative').connDownThresholdSec, 30);
  assert.equal(r('connDownAbsent').connDownThresholdSec, 30);

  // Site membership, both shapes.
  assert.deepEqual(r('siteIdsArray').siteIds, ['s1', 's2']);
  assert.equal(r('siteIdsArray').siteId, 's1');
  assert.deepEqual(r('siteIdScalar').siteIds, ['s9'],
    'a scalar siteId was not accepted — an older client cannot add a router');
  assert.equal(r('siteIdScalar').siteId, 's9');
  assert.deepEqual(r('siteIdsEmpty').siteIds, []);
  assert.equal(r('siteIdsEmpty').siteId, null);

  // Trimming, and the 64-character cut.
  assert.equal(r('paddedFields').host, '198.51.100.24', 'the host was not trimmed');
  assert.equal(r('paddedFields').username, 'op');
  assert.equal(r('paddedFields').label, 'Padded');
  assert.ok(r('longLabel').label.length <= 64,
    'a 120-character label survived: ' + r('longLabel').label.length);

  // And the refusals are refusals.
  for (const k of ['noHost', 'emptyHost', 'portZero', 'portTooHigh']) {
    assert.ok(cases[k].refused, k + ' was accepted');
  }
}

const json = JSON.stringify({ cases }, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('router-add-cases.json is STALE — re-run without --check');
    process.exit(1);
  }
  console.log('router-add-cases.json is current (' + Object.keys(cases).length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, json);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' cases)');
}
