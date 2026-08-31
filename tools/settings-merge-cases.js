'use strict';
/**
 * What the live `load()` produces, for the Go `Merge` to reproduce.
 *
 * Four layers, and every one of them has a case here that fails without it:
 * the defaults, the stored file over them, the environment over that, and the
 * clamps that stop a hand-edited file producing a sub-minimum timer delay.
 *
 * ── THE CASES ARE CHOSEN FOR THE EDGES, NOT THE MIDDLE ─────────────────────
 *
 *   an unknown key on disk        must be DROPPED, not carried into the payload
 *   each env parser kind          string, int and bool are three code paths
 *   an unparseable int env var    parseInt gives NaN, and NaN reaches the map
 *   ROUTER_PASS                   handled outside ENV_MAP, env winning
 *   updateCheckHours              its own bounds, in HOURS, with a non-number
 *                                 falling back to 12 rather than being clamped
 *   poll intervals               below, above and inside the bounds
 *   a sealed credential          decrypted, and an undecryptable one emptied
 *
 * Runs the live module against a throwaway DATA_DIR in a FRESH process per case
 * — `settings.js` caches after its first `load()`, and env is read at load time.
 * Nothing from the operator's real /data is touched, and every credential is
 * obviously fake.
 *
 *   node tools/settings-merge-cases.js            write the corpus
 *   node tools/settings-merge-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'settings-merge-cases.json');
const ENCRYPTED = ['routerPass', 'telegramBotToken', 'pushbulletApiKey',
                   'smtpUser', 'smtpPass', 'ntfyToken'];

const CASES = [
  ['nothing stored, no env', {}, {}],
  ['a key that is not a default is dropped', { somethingNobodyDefined: 'x', topN: 12 }, {}],
  ['a stored value beats the default', { topN: 42, pingTarget: '198.51.100.9' }, {}],

  ['env beats the stored file (string)', { routerHost: '198.51.100.1' }, { ROUTER_HOST: '203.0.113.7' }],
  ['env beats the stored file (int)', { routerPort: 8728 }, { ROUTER_PORT: '8729' }],
  ['env beats the stored file (bool, true)', { routerTls: false }, { ROUTER_TLS: 'TRUE' }],
  ['env beats the stored file (bool, anything else)', { routerTls: true }, { ROUTER_TLS: 'yes' }],
  ['an EMPTY env var still wins', { routerHost: '198.51.100.1' }, { ROUTER_HOST: '' }],
  ['an unparseable int env var', {}, { ROUTER_PORT: 'not-a-port' }],
  ['an int env var with trailing rubbish', {}, { ROUTER_PORT: '8729abc' }],

  ['ROUTER_PASS from the environment', {}, { ROUTER_PASS: 'NOT-A-REAL-ENV-PASSWORD' }],
  ['ROUTER_PASS explicitly empty in the environment', { routerPass: 'NOT-A-REAL-PASSWORD' }, { ROUTER_PASS: '' }],

  ['updateCheckHours below the floor', { updateCheckHours: 0 }, {}],
  ['updateCheckHours above the ceiling', { updateCheckHours: 1000 }, {}],
  ['updateCheckHours fractional (rounded)', { updateCheckHours: 2.6 }, {}],
  ['updateCheckHours not a number', { updateCheckHours: 'soon' }, {}],

  ['a poll interval below its floor', { pollConns: 1, pollRouting: 1 }, {}],
  ['a poll interval above its ceiling', { pollConns: 999999, pollWifi: 999999 }, {}],
  ['a poll interval inside its bounds', { pollConns: 5000 }, {}],
  ['a poll interval that is not a number', { pollConns: 'fast' }, {}],
  ['a poll interval from the environment, out of bounds', {}, { CONNS_POLL_MS: '1' }],

  ['a sealed credential is decrypted', { routerPass: 'NOT-A-REAL-PASSWORD', smtpPass: 'NOT-A-REAL-SMTP-PASS' }, {}],
];

function runCase(stored, env) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'mdmerge-'));
  try {
    const out = execFileSync(process.execPath, ['-e', `
      const fs = require('node:fs'), path = require('node:path');
      const S = require(${JSON.stringify(path.join(LIVE, 'src', 'settings.js'))});
      const stored = JSON.parse(${JSON.stringify(JSON.stringify(stored))});
      const enc = ${JSON.stringify(ENCRYPTED)};
      const onDisk = {};
      for (const [k, v] of Object.entries(stored)) {
        onDisk[k] = (enc.includes(k) && v) ? S.encrypt(String(v)) : v;
      }
      fs.writeFileSync(path.join(process.env.DATA_DIR, 'settings.json'), JSON.stringify(onDisk));
      // load() is not exported; getPublic() calls it and masks six fields, so
      // the credentials are compared through the disclosure gate instead and
      // this one reports everything else.
      const merged = S.getPublic();
      process.stdout.write(JSON.stringify(merged));
    `], { env: { ...process.env, DATA_DIR: dir, ...env }, encoding: 'utf8' });
    return JSON.parse(out);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

function main() {
  const check = process.argv.includes('--check');
  const cases = CASES.map(([note, stored, env]) => ({
    note, stored, env, merged: runCase(stored, env),
  }));

  const body = JSON.stringify({
    note: 'Generated by tools/settings-merge-cases.js from the LIVE src/settings.js. ' +
          'Credentials appear MASKED because the live load() is reached through getPublic(); ' +
          'their values are gated separately by settings-public-cases.json.',
    cases,
  }, null, 2) + '\n';

  if (check) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body) {
      console.error('testdata/settings-merge-cases.json is stale — run: node tools/settings-merge-cases.js');
      process.exit(1);
    }
    console.log('settings merge cases up to date (' + cases.length + ' cases)');
    return;
  }
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), OUT) + ' — ' + cases.length + ' cases');
}

main();
