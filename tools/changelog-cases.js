'use strict';
/**
 * `Changelog.fetchNotes`'s decisions, run from the live `src/changelog.js`.
 *
 * ---- WHY NOW: THREE LEDGERS POINTING AT A CLOSED GROUP ---------------------
 *
 * `packages:notes` was recorded UNPORTED in three places — `emit-audit`,
 * `inbound-audit` and `wiring-audit`'s `upd_notes` — each deferring to "the
 * Update DIALOG that would drive it", which is "itself unported (the `upgrade
 * dialog` group in wiring-audit)".
 *
 * THAT GROUP WAS CLOSED ON 2026-08-25. `wiring-audit.js` says so in its own
 * comment nine lines above: "UPGRADE was here, covering the nine `upd*` ids.
 * Closed 2026-08-25: web/src/pages/upgrade.ts drives the dialog". The dialog is
 * ported; the entries deferred to it for three days anyway, and nothing failed,
 * because a cross-reference is only as live as the thing it points at.
 *
 * ---- THE VERSION WHITELIST IS THE POINT ------------------------------------
 *
 * The live header calls `VERSION_RE` "the single most important line in this
 * file", and it is: `version` arrives from a SOCKET PAYLOAD and is interpolated
 * into a URL PATH. Without an anchored whitelist that is a path traversal
 * (`../../`) and an open-redirect-shaped fetch (`//evil.example.com/`) in one.
 *
 * So the corpus is mostly attempts on it. Everything else here — the negative
 * cache TTL, the FIFO trim, the streaming size cap — is recorded as constants
 * the Go side must match, because they are the kind of number a port rounds.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/changelog-cases.js
 *   MIKRODASH_SRC=../MikroDash node tools/changelog-cases.js --check
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', 'changelog-cases.json');

const CL = require(path.join(LIVE, 'src', 'changelog.js'));

const VERSIONS = [
  // Accepted.
  '7.24', '7.24.1', '6.49.18', '0.0', '10.100.999',
  // PATH TRAVERSAL, the reason the whitelist exists.
  '../../etc/passwd', '7.24/../../..', '..', '../7.24',
  // OPEN-REDIRECT SHAPED: a leading `//` turns the path into a host.
  '//evil.example.com/', '//evil.example.com/routeros/7.24/CHANGELOG',
  // Query and fragment injection into the path.
  '7.24?x=1', '7.24#frag', '7.24%2F..%2F',
  // Shapes that look numeric and are not.
  '7', '7.', '.7', '7..24', '7.24.', '7.24.1.2', ' 7.24', '7.24 ',
  '7.24\n', '7,24', '7-24', 'v7.24', '7.24a', '', '   ',
  // Non-strings the socket could deliver.
  null, undefined, 0, 7.24, true, [], {},
];

// `fetchNotes` on a bad version rejects SYNCHRONOUSLY-ish with 'bad version'
// before any network use, which is what makes this safe to run offline: an
// ACCEPTED version would try to reach upgrade.mikrotik.com, so only the verdict
// of the whitelist is recorded, never a fetch.
const cases = VERSIONS.map((v) => {
  const asGiven = typeof v === 'string' ? v : JSON.stringify(v) ?? String(v);
  const trimmed = String(v == null ? '' : v).trim();
  return { input: asGiven, trimmed, accepted: CL.VERSION_RE.test(trimmed) };
});

const constants = {
  maxBytes: CL.MAX_BYTES,
  timeoutMs: CL.TIMEOUT_MS,
  // Read from the source: they are not exported, and a port that guessed them
  // would be wrong in a way no test could see.
  cacheMax: Number(/const CACHE_MAX = (\d+);/
    .exec(fs.readFileSync(path.join(LIVE, 'src', 'changelog.js'), 'utf8'))[1]),
  negTtlMs: Number(/const NEG_TTL_MS = (\d+);/
    .exec(fs.readFileSync(path.join(LIVE, 'src', 'changelog.js'), 'utf8'))[1]),
  host: /const HOST = '([^']+)';/
    .exec(fs.readFileSync(path.join(LIVE, 'src', 'changelog.js'), 'utf8'))[1],
  pathTemplate: "/routeros/<version>/CHANGELOG",
};

// ---- Believability ---------------------------------------------------------
const accepted = cases.filter((c) => c.accepted).map((c) => c.trimmed);
const rejected = cases.filter((c) => !c.accepted);

if (accepted.length < 4) throw new Error('almost nothing is accepted; the corpus proves nothing');
if (rejected.length < 20) throw new Error('too few rejections to call this a whitelist test');

// EVERY TRAVERSAL AND REDIRECT SHAPE MUST BE REJECTED. If one is accepted the
// generator refuses to write, rather than recording a hole as expected output.
for (const bad of ['../../etc/passwd', '..', '../7.24', '//evil.example.com/', '7.24?x=1',
  '7.24#frag', '7.24%2F..%2F', '7.24/../../..']) {
  const c = cases.find((x) => x.input === bad);
  if (!c) throw new Error(`the corpus lost its case for ${JSON.stringify(bad)}`);
  if (c.accepted) throw new Error(`SECURITY: ${JSON.stringify(bad)} passes VERSION_RE`);
}
// And the ordinary ones are accepted, or the regex is simply refusing everything.
for (const good of ['7.24', '7.24.1', '6.49.18']) {
  if (!cases.find((x) => x.input === good).accepted) {
    throw new Error(`${good} is rejected; the whitelist is not usable`);
  }
}
// TRIMMING IS PART OF THE CONTRACT: ' 7.24' is accepted BECAUSE fetchNotes
// trims first. A port that validated the raw string would reject it.
if (!cases.find((x) => x.input === ' 7.24').accepted) {
  throw new Error('a leading space is rejected; fetchNotes trims before testing');
}
// A newline must NOT survive trimming into acceptance by accident — it does
// trim, so this is accepted, and the port must agree rather than treating \\n
// as a header-injection risk it has already removed.
if (!cases.find((x) => x.input === '7.24\n').accepted) {
  throw new Error('a trailing newline changed the verdict; String.trim removes it');
}
if (!constants.host || constants.host.indexOf('mikrotik') === -1) {
  throw new Error('the host does not look like MikroTik: ' + constants.host);
}

const json = JSON.stringify(
  { generated_from: 'src/changelog.js VERSION_RE and its constants', constants, cases },
  null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('STALE: testdata/changelog-cases.json - re-run tools/changelog-cases.js');
    process.exit(1);
  }
  console.log(`changelog-cases: up to date (${cases.length} versions, ${accepted.length} accepted)`);
} else {
  fs.writeFileSync(OUT, json);
  console.log(`wrote ${OUT} (${cases.length} versions, ${accepted.length} accepted, `
    + `${rejected.length} rejected)`);
}
