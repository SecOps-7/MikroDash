#!/usr/bin/env node
'use strict';
/**
 * THE LEGACY SINGLE-ROUTER SEED, by RUNNING the live `loadAll()`.
 *
 * ── WHAT IT IS ────────────────────────────────────────────────────────────
 *
 * `src/routers.js:loadAll()` writes routers.json from the old single-router
 * settings the first time it is asked for a fleet and finds no file:
 *
 *   if (!fs.existsSync(ROUTERS_FILE)) {
 *     const s = Settings.load();
 *     if (fs.existsSync(settingsFile) && s.routerHost) { …seed one router… }
 *   }
 *
 * It is the upgrade path for an install that predates multi-router support. The
 * operator decided on 2026-08-30 that the merged app must carry it (LOOP.md 0g),
 * because after cutover the Node code holding it is gone and such an install
 * would otherwise come up with no routers at all.
 *
 * NOT to be confused with `router-add-cases.js` (the API add path),
 * `routers-public-cases.js` (the read projection) or `switchrouter-cases.js`
 * (the active-router switch). This is the one branch of `loadAll` that WRITES.
 *
 * ── RUN, NOT READ ─────────────────────────────────────────────────────────
 *
 * The seed is eleven fields of defaults and coercions, and each has a history:
 * `port || 8729`, `tls !== false && tls !== 'false'`, `_isTrue(tlsInsecure)`
 * (which upstream corrected twice — `2af8164`, then `dccbf62`), `username ||
 * 'admin'`, `defaultIf || 'ether1'`, `pingTarget || '1.1.1.1'`. Reading them off
 * and retyping them into Go is how a port acquires a rule that was true on the
 * day it was copied. So this drives the real function against a temp DATA_DIR
 * and records what it wrote.
 *
 * ── THE PASSWORD IS RECORDED AS A SHAPE, NEVER A VALUE ────────────────────
 *
 * `_writeFile` ENCRYPTS on the way out, so the seeded record holds ciphertext.
 * The corpus records whether the field is present and whether it came back as
 * plaintext — not the ciphertext, which differs per run, and not the plaintext,
 * which is a credential. `CLAUDE.md`: no credential and nothing identifying ever
 * reaches a file here.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/router-seed-cases.js [--check]
 */

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const assert = require('node:assert');

// RESOLVED, because `require` takes a relative path as relative to THIS module
// rather than to the working directory — the same reason every other generator
// here resolves it.
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'router-seed-cases.json');

// Every case is a legacy settings.json and the record the seed makes of it. The
// values are synthetic — TEST-NET-3 addresses and a placeholder secret — so the
// corpus carries nothing from any real install.
const CASES = [
  {
    name: 'every field supplied',
    settings: {
      routerHost: '203.0.113.7', routerPort: 8729, routerUser: 'operator',
      routerPass: 'not-a-real-secret', routerTls: true, routerTlsInsecure: true,
      defaultIf: 'sfp-sfpplus1', pingTarget: '203.0.113.1',
    },
  },
  {
    name: 'only the host, so every default applies',
    settings: { routerHost: '203.0.113.8' },
  },
  {
    name: 'the booleans as STRINGS, which is how a form submits them',
    settings: {
      routerHost: '203.0.113.9', routerTls: 'false', routerTlsInsecure: 'true',
    },
  },
  {
    // `!!('false')` is true, and upstream fixed that twice. A port reading
    // tlsInsecure as truthy would turn a certificate check OFF for an operator
    // who turned it on — the `dccbf62` defect, on the upgrade path.
    name: 'tlsInsecure as the string "false"',
    settings: { routerHost: '203.0.113.10', routerTlsInsecure: 'false' },
  },
  {
    // The consequence of the above, pinned as its own case: no routerHost key,
    // and a router is seeded anyway at the settings default.
    name: 'no routerHost key — seeded at the default address anyway',
    settings: { defaultIf: 'ether5' },
  },
  {
    name: 'port as a string',
    settings: { routerHost: '203.0.113.11', routerPort: '8728' },
  },
  {
    // ── THE REFUSAL IS A MISSING settings.json, NOT A MISSING routerHost ──
    //
    // MEASURED, and it corrects the obvious reading of the guard. `loadAll`
    // tests `fs.existsSync(settingsFile) && s.routerHost`, which looks like two
    // conditions and is one: `routerHost` DEFAULTS to '192.168.88.1'
    // (`src/settings.js:71`), so `Settings.load()` never returns it empty and
    // the second half can never be false.
    //
    // A first version of this corpus had a case with no `routerHost` key and
    // expected no router; live seeded one at the default address. The live
    // comment says the intent plainly — "Only runs when settings.json already
    // exists (i.e. a real prior deployment)" — so the FILE is the signal and the
    // field test is belt-and-braces that never tightens.
    //
    // The port must reproduce this: an upgrade whose settings.json exists gets a
    // router at 192.168.88.1 even if one was never configured. Recorded rather
    // than corrected, because a port that refused where live seeds would leave
    // an install with an empty fleet that Node would have populated.
    name: 'settings.json absent entirely — the only refusal',
    noSettingsFile: true,
    settings: {},
    expectEmpty: true,
  },
];

function runCase(c) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'mdseed-'));
  if (!c.noSettingsFile) {
    fs.writeFileSync(path.join(dir, 'settings.json'), JSON.stringify(c.settings));
  }
  // A fresh module registry per case: `routers.js` memoises the fleet in
  // `_cache` and `settings.js` caches the whole settings object at first load,
  // so a second case in the same process would see the first one's answers.
  for (const k of Object.keys(require.cache)) delete require.cache[k];
  process.env.DATA_DIR = dir;

  const Routers = require(path.join(LIVE, 'src', 'routers.js'));
  const all = Routers.loadAll();
  const onDisk = fs.existsSync(path.join(dir, 'routers.json'))
    ? JSON.parse(fs.readFileSync(path.join(dir, 'routers.json'), 'utf8'))
    : null;
  fs.rmSync(dir, { recursive: true, force: true });

  if (c.expectEmpty) {
    assert.strictEqual(all.length, 0, `${c.name}: seeded a router with no routerHost`);
    return {
      name: c.name, settings: c.settings, seeded: false,
      noSettingsFile: c.noSettingsFile === true,
    };
  }
  assert.strictEqual(all.length, 1, `${c.name}: expected exactly one seeded router`);
  assert.ok(onDisk, `${c.name}: the seed was returned but not WRITTEN`);

  const r = all[0];
  const w = onDisk[0];
  return {
    name: c.name,
    settings: c.settings,
    seeded: true,
    // The typed fields, verbatim. `id` and `addedAt` are excluded: a uuid and a
    // clock reading differ on every run, and pinning them would make this corpus
    // stale a millisecond after it was written.
    record: {
      label: r.label, host: r.host, port: r.port,
      tls: r.tls, tlsInsecure: r.tlsInsecure,
      username: r.username, defaultIf: r.defaultIf, pingTarget: r.pingTarget,
    },
    // THE PASSWORD AS A SHAPE. Present-and-sealed, or empty — never a value.
    password: {
      suppliedInSettings: c.settings.routerPass !== undefined,
      writtenNonEmpty: typeof w.password === 'string' && w.password.length > 0,
      // The stored form must NOT be the plaintext. This is the assertion that
      // fails if a future change writes the credential straight through.
      writtenIsPlaintext: w.password === c.settings.routerPass,
    },
    idIsUuid: /^[0-9a-f-]{36}$/i.test(String(r.id)),
    addedAtIsNumber: typeof r.addedAt === 'number' && r.addedAt > 0,
  };
}

const cases = CASES.map(runCase);

// The corpus is useless if a change makes every case refuse, so assert it
// exercised the path it exists for.
assert.ok(cases.filter((c) => c.seeded).length >= 5, 'too few cases actually seeded');
for (const c of cases) {
  if (c.seeded && c.password.suppliedInSettings) {
    assert.strictEqual(c.password.writtenIsPlaintext, false,
      `${c.name}: the seed wrote the password in CLEAR`);
  }
}

const text = JSON.stringify({ generatedFrom: 'src/routers.js:loadAll', cases }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== text) {
    console.error('router-seed-cases: STALE — re-run without --check');
    process.exit(1);
  }
  console.log(`router-seed-cases: current (${cases.length} cases)`);
} else {
  fs.writeFileSync(OUT, text);
  console.log(`router-seed-cases: wrote ${cases.length} cases`);
}
