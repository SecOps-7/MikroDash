'use strict';
/**
 * What the live `getPublic` and `getViewerPublic` disclose, for the Go port.
 *
 * ── BOTH ARE SECURITY BOUNDARIES, WHICH IS WHY THEY ARE GENERATED ──────────
 *
 * `getPublic` MASKS credentials: six named fields become a bullet string when
 * set and an empty string when not, so the browser can tell "configured" from
 * "blank" without receiving either value. A port that forgot one field would
 * hand a Telegram bot token or an SMTP password to every administrator's
 * devtools, and nothing on screen would look different.
 *
 * `getViewerPublic` is an ALLOW-LIST, not a denylist. A viewer gets a named
 * subset and nothing else — no router host, no auth configuration, no
 * notification targets, all of which the live comment calls "admin-only recon".
 * A port that inverted the sense, or that started from the full object and
 * removed fields, would leak every setting added after it was written. That is
 * the difference this corpus exists to pin.
 *
 * ── THE LIVE MODULE IS RUN AGAINST A SYNTHETIC /data ───────────────────────
 *
 * `settings.js` reads `DATA_DIR`, so the generator points it at a temporary
 * directory holding a settings.json this file wrote. NOTHING from the operator's
 * real /data is read, and every credential value below is obviously fake — this
 * repo is public.
 *
 * The module caches after its first `load()`, so each case runs in a FRESH child
 * process. Reusing one would have every case answer with the first one's data,
 * which reads as agreement.
 *
 *   node tools/settings-public-cases.js            write the corpus
 *   node tools/settings-public-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'settings-public-cases.json');

// Every value here is synthetic. The credential fields carry recognisable
// nonsense so a leak in a diff is unmistakable.
const CASES = [
  ['an empty settings file', {}],
  ['every credential set', {
    routerPass: 'NOT-A-REAL-PASSWORD', telegramBotToken: 'NOT-A-REAL-TOKEN',
    pushbulletApiKey: 'NOT-A-REAL-KEY', smtpUser: 'not-a-real-user',
    smtpPass: 'NOT-A-REAL-SMTP-PASS', ntfyToken: 'NOT-A-REAL-NTFY-TOKEN',
  }],
  // EMPTY IS NOT THE SAME AS SET: the mask must distinguish them, because the
  // page shows "configured" for one and an empty box for the other.
  ['credentials explicitly empty', {
    routerPass: '', telegramBotToken: '', pushbulletApiKey: '',
    smtpUser: '', smtpPass: '', ntfyToken: '',
  }],
  ['only some credentials set', {
    routerPass: 'NOT-A-REAL-PASSWORD', smtpPass: 'NOT-A-REAL-SMTP-PASS',
  }],
  ['admin-only configuration present', {
    routerHost: '198.51.100.1', routerUser: 'not-a-real-user', routerPort: 8729,
    authMode: 'local', smtpHost: 'smtp.example.invalid', telegramChatId: '12345',
    ntfyTopic: 'not-a-real-topic', pushbulletApiKey: 'NOT-A-REAL-KEY',
  }],
  ['viewer-visible fields at non-default values', {
    pingEnabled: false, pingTarget: '198.51.100.9', topN: 25, maxConns: 4321,
    historyMinutes: 720, alertCpuThreshold: 77, alertPingLoss: 33,
    displayTimezone: 'Europe/Berlin', userNotifyEnabled: true,
    pageWifi: false, pageAudit: false, pageBackups: true,
  }],
  ['a key that is not in DEFAULTS at all', { somethingNobodyDefined: 'x' }],
];

// The six fields settings.js stores encrypted. Identical to CREDENTIAL_FIELDS
// today, and kept separate because they answer different questions: one is what
// is sealed on disk, the other is what is masked on the wire.
const ENCRYPTED = ['routerPass', 'telegramBotToken', 'pushbulletApiKey',
                   'smtpUser', 'smtpPass', 'ntfyToken'];

function runCase(stored) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'mdset-'));
  try {
    // ── THE CREDENTIALS MUST BE SEALED WITH THE MODULE'S OWN KEY ──────────
    //
    // The first version of this wrote them as PLAINTEXT. `load()` then failed to
    // decrypt each one, logged an auth-tag warning and yielded the empty string
    // — so every credential came back masked as '' and "every credential set"
    // was byte-identical to "an empty settings file". The corpus would have
    // passed against a port that never masked anything at all, which is the
    // exact defect it exists to catch. Caught by reading the generated file
    // rather than by trusting that it wrote.
    //
    // So the child encrypts with `S.encrypt`, against the .secret the module
    // creates in this same throwaway DATA_DIR, and only then writes the file.
    // A FRESH PROCESS PER CASE, because settings.js caches after its first
    // load() and a reused one would answer every case with the first's data.
    const out = execFileSync(process.execPath, ['-e', `
      const fs = require('node:fs'), path = require('node:path');
      const S = require(${JSON.stringify(path.join(LIVE, 'src', 'settings.js'))});
      const raw = ${JSON.stringify(JSON.stringify(stored))};
      const enc = ${JSON.stringify(ENCRYPTED)};
      const stored = JSON.parse(raw);
      const onDisk = {};
      for (const [k, v] of Object.entries(stored)) {
        onDisk[k] = (enc.includes(k) && v) ? S.encrypt(String(v)) : v;
      }
      fs.writeFileSync(path.join(process.env.DATA_DIR, 'settings.json'),
                       JSON.stringify(onDisk));
      process.stdout.write(JSON.stringify({
        pub: S.getPublic(), viewer: S.getViewerPublic(),
        credentialFields: S.CREDENTIAL_FIELDS,
      }));
    `], { env: { ...process.env, DATA_DIR: dir }, encoding: 'utf8' });
    return JSON.parse(out);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

function main() {
  const check = process.argv.includes('--check');
  const cases = CASES.map(([note, stored]) => {
    const r = runCase(stored);
    return { note, stored, public: r.pub, viewer: r.viewer };
  });

  const first = runCase({});
  const body = JSON.stringify({
    note: 'Generated by tools/settings-public-cases.js from the LIVE src/settings.js. Do not edit.',
    credentialFields: first.credentialFields,
    // The allow-list itself, derived from a case where every viewer field is at
    // a non-default value — so a field silently dropped from the list shows up
    // as a missing KEY here rather than only as a changed value.
    viewerFields: Object.keys(first.viewer).sort(),
    cases,
  }, null, 2) + '\n';

  if (check) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body) {
      console.error('testdata/settings-public-cases.json is stale — run: node tools/settings-public-cases.js');
      process.exit(1);
    }
    console.log('settings disclosure cases up to date (' + cases.length + ' cases, ' +
                Object.keys(first.viewer).length + ' viewer fields)');
    return;
  }
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), OUT) + ' — ' +
              cases.length + ' cases, ' + Object.keys(first.viewer).length + ' viewer fields, ' +
              first.credentialFields.length + ' credential fields');
}

main();
