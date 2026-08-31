'use strict';
/**
 * What `Routers.getPublic()` discloses, for the Go port to reproduce exactly.
 *
 * ---- WHY THIS EXISTS ------------------------------------------------------
 *
 * Live verification on 2026-08-28 diffed every endpoint the port serves against
 * Node's answer to the same request. `GET /api/routers` came back with ELEVEN
 * keys where the live app sends TWENTY-THREE. The port models a subset of a
 * document it does not own — the same failure `internal/store/users_public.go`
 * warns about, one endpoint over.
 *
 * `getPublic` is four lines and none of them is a field list:
 *
 *   const out = { ...r, password: r.password ? '(mask)' : '' };
 *   if (r.backup) {
 *     const { password, ...rest } = r.backup;
 *     out.backup = { ...rest, hasPassword: !!password };
 *   }
 *
 * SPREAD, KEEP EVERYTHING, mask one field and fold one nested one. A corpus of
 * field NAMES would therefore be the wrong shape: what has to be pinned is that
 * anything the file carries survives, including keys no struct declares.
 *
 * ---- THE MASK IS ON THE DECRYPTED VALUE -----------------------------------
 *
 * `r.password` here is what `_readFile` decrypted, not the ciphertext. So a
 * record whose password CANNOT be decrypted — a wrong `.secret`, a corrupted
 * envelope — discloses `''`, not the mask. That is a real distinction: `''`
 * means "no password, or one this install cannot read" and the mask means "there
 * is one". The corpus carries both, and gets the encrypted one by calling the
 * live `add()` so the ciphertext is made the way the app makes it.
 *
 * ---- NOTHING HERE IS REAL -------------------------------------------------
 *
 * Every password is a literal invented in this file, the secret is the word
 * `corpus-secret`, and the module is pointed at a throwaway directory. The
 * operator's /data is never read, and the checks at the bottom refuse to write
 * a file carrying either literal.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/routers-public-cases.js          # write
 *   MIKRODASH_SRC=../MikroDash node tools/routers-public-cases.js --check  # fail if stale
 */

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', 'routers-public-cases.json');

const PW = 'corpus-only-not-a-real-password';
const BACKUP_PW = 'invented-backup-pw';

/**
 * Run the live getPublic over a routers.json this function writes.
 *
 * `seedWithAdd` asks the live `add()` to create the record, so its password is
 * encrypted exactly as the app encrypts one — the only way to exercise the mask
 * rather than the cannot-decrypt path.
 */
function runCase(records, seed) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'mdrouters-'));
  try {
    fs.writeFileSync(path.join(dir, '.secret'), 'corpus-secret');
    fs.writeFileSync(path.join(dir, 'routers.json'), JSON.stringify(records));
    const out = execFileSync(process.execPath, ['-e', `
      const R = require(${JSON.stringify(path.join(LIVE, 'src', 'routers.js'))});
      const seed = ${JSON.stringify(seed || '')};
      if (seed) {
        const rec = R.add({ host: '198.51.100.7', username: 'ops',
          password: ${JSON.stringify(PW)}, label: 'Seeded',
          defaultIf: 'ether1', pingTarget: '1.1.1.1' });
        // A BACKUP PASSWORD HAS TO GO THROUGH update() TOO. Written straight
        // into the file it is not a valid envelope, _readFile decrypts it to ''
        // and hasPassword comes back FALSE — which is the arm this case exists
        // to distinguish from. Found by the believability check below.
        if (seed === 'backup') {
          R.update(rec.id, { backup: { enabled: true, schedule: 'daily', keepCount: 7,
                                       password: ${JSON.stringify(BACKUP_PW)} } });
        }
        R.invalidateCache();
      }
      process.stdout.write(JSON.stringify(R.getPublic()));
    `], { env: { ...process.env, DATA_DIR: dir }, encoding: 'utf8' });
    return JSON.parse(out);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

const CASES = [
  ['no routers at all', [], false],
  // THE MASK, on a password the live `add()` encrypted.
  ['a record whose password this install CAN decrypt', [], 'add'],
  // Written straight into the file with a value the envelope cannot open: the
  // disclosure is '', not the mask.
  ['a password this install CANNOT decrypt discloses the empty string', [
    { id: 'r1', label: 'Undecryptable', host: '198.51.100.1', username: 'ops',
      password: 'not-a-valid-envelope' },
  ], false],
  ['a record with NO password at all', [
    { id: 'r2', label: 'None', host: '198.51.100.2', username: 'ops' },
  ], false],
  // THE BACKUP FOLD. `password` is REMOVED, not masked, and replaced by a
  // boolean — the live comment: "a masked secret invites a round trip that could
  // write the mask back".
  // Seeded through add() + update(), so the backup password is a real envelope.
  ['a backup block WITH a password', [], 'backup'],
  ['a backup block WITHOUT a password', [
    { id: 'r4', label: 'Backed up', host: '198.51.100.4', username: 'ops',
      backup: { enabled: true, schedule: 'daily', keepCount: 7 } },
  ], false],
  ['no backup block at all - no hasPassword is invented', [
    { id: 'r5', label: 'Plain', host: '198.51.100.5', username: 'ops' },
  ], false],
  // THE POINT OF THE SPREAD: keys no struct in the port declares must survive.
  ['a record carrying every field the real file has, plus one nobody models', [
    { id: 'r6', label: 'Full', host: '198.51.100.6', port: 8729, tls: true, tlsInsecure: false,
      username: 'ops', defaultIf: 'ether1', pingTarget: '1.1.1.1', disabled: false,
      addedAt: 1700000000000, alertsEnabled: true, connDownThresholdSec: 60,
      model: 'hAP ax3', osVersion: '7.24', serial: 'INVENTED123', siteId: 'site-a',
      siteIds: ['site-a'], bwDownMbps: 100, bwUpMbps: 50,
      geo: { auto: { ip: '198.51.100.200', cc: 'DE' }, place: { name: 'Berlin' } },
      aFieldNobodyModels: 'survives the spread' },
  ], false],
];

// The INPUT is recorded so a reader can see what produced each disclosure — with
// every secret replaced first. The credential check below caught this file
// carrying `invented-backup-pw` in exactly this field on its first run, which is
// the check doing its job on its own author.
const redact = (r) => {
  const c = JSON.parse(JSON.stringify(r));
  if (typeof c.password === 'string' && c.password) c.password = '<invented>';
  if (c.backup && c.backup.password) c.backup.password = '<invented>';
  return c;
};

// A SEEDED record carries values the live writers MINT — a fresh uuid and a
// `Date.now()` — so a corpus recording them verbatim is different on every run
// and `--check` is permanently stale. Which is what happened: verify.sh reported
// it the first time this generator ran inside the sweep.
//
// The two are replaced by a marker rather than dropped, because their PRESENCE
// is part of the disclosure — `addedAt` is one of the twelve fields that started
// this, and a corpus that quietly lost it would stop pinning it.
const stabilise = (r, seeded) => {
  if (!seeded) return r;
  const c = { ...r };
  if (typeof c.id === 'string') c.id = '<generated-uuid>';
  if (typeof c.addedAt === 'number') c.addedAt = '<generated-ms-epoch>';
  return c;
};

const out = CASES.map(([why, records, seed]) => ({
  why,
  input: records.map(redact),
  seededWith: seed || '',
  public: runCase(records, seed).map((r) => stabilise(r, !!seed)),
}));

// ---- Believability ---------------------------------------------------------
const byWhy = Object.fromEntries(out.map((c) => [c.why, c]));
const need = (k) => {
  if (!byWhy[k]) throw new Error(`a believability check names a case that is gone: ${k}`);
  return byWhy[k];
};
const MASK = '•'.repeat(8);

// NO PLAINTEXT AND NO CIPHERTEXT may reach the corpus. This is the check that
// matters most: the file transplants into a public repository at cutover.
const blob = JSON.stringify(out);
if (blob.includes(PW)) throw new Error('the corpus contains a plaintext password');
if (blob.includes(BACKUP_PW)) throw new Error('the corpus contains a backup password');
for (const c of out) {
  for (const r of c.public) {
    if (r.password !== undefined && r.password !== '' && r.password !== MASK) {
      throw new Error(`${c.why}: password disclosed as ${JSON.stringify(r.password)}`);
    }
    if (r.backup && 'password' in r.backup) {
      throw new Error(`${c.why}: the backup password survived the fold`);
    }
  }
}

// THE MASK actually appears somewhere, or the corpus proves only that nothing is
// disclosed because nothing was set.
const masked = need('a record whose password this install CAN decrypt').public;
if (masked.length !== 1 || masked[0].password !== MASK) {
  throw new Error('the seeded record did not come back masked: '
    + JSON.stringify(masked.map((r) => r.password)));
}
// AND THE OTHER ARM: an unreadable password is '', not the mask.
const bad = need('a password this install CANNOT decrypt discloses the empty string').public;
if (bad[0].password !== '') {
  throw new Error(`an undecryptable password disclosed ${JSON.stringify(bad[0].password)}`);
}
// A SEEDED record still carries the minted fields, marker or not. `addedAt` is
// one of the twelve that started this file.
for (const c of out.filter((x) => x.seededWith)) {
  const r = c.public[0];
  if (r.id !== '<generated-uuid>') throw new Error(`${c.why}: the id was not recorded`);
  if (r.addedAt !== '<generated-ms-epoch>') {
    throw new Error(`${c.why}: addedAt is ${JSON.stringify(r.addedAt)} — the live add() sets a `
      + 'millisecond epoch, and its absence would mean the field stopped being disclosed');
  }
}

// THE BACKUP FOLD, both ways.
if (need('a backup block WITH a password').public[0].backup.hasPassword !== true) {
  throw new Error('hasPassword is not true for a set password');
}
if (need('a backup block WITHOUT a password').public[0].backup.hasPassword !== false) {
  throw new Error('hasPassword is not false for an unset one');
}
if (need('no backup block at all - no hasPassword is invented').public[0].backup !== undefined) {
  throw new Error('a backup block was invented for a record that has none');
}
// THE SPREAD. Every input key must survive.
const full = need('a record carrying every field the real file has, plus one nobody models');
const outKeys = new Set(Object.keys(full.public[0]));
for (const k of Object.keys(full.input[0])) {
  if (!outKeys.has(k)) throw new Error(`getPublic DROPPED ${k}; it spreads and must keep everything`);
}
if (!outKeys.has('aFieldNobodyModels')) {
  throw new Error('a field no struct declares was dropped — which is the whole point');
}
if (full.public[0].geo.auto.ip !== '198.51.100.200') {
  throw new Error('getPublic stripped geo.auto.ip. It does NOT — the strip lives in '
    + '_routersForSocket, and GET /api/routers skips it. Filed upstream in ToDo.md; if this '
    + 'starts failing, upstream fixed it and the port must follow.');
}

const json = JSON.stringify({ generated_from: 'src/routers.js getPublic', cases: out }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('STALE: testdata/routers-public-cases.json - re-run tools/routers-public-cases.js');
    process.exit(1);
  }
  console.log(`routers-public-cases: up to date (${out.length} cases)`);
} else {
  fs.writeFileSync(OUT, json);
  console.log(`wrote ${OUT} (${out.length} cases)`);
}
