'use strict';
/**
 * THE MY ALERTS FORM, live against ported.
 *
 * The server side of per-user channels is pinned by four corpora already. This
 * pins the BROWSER's half: what the form shows when the stored config arrives,
 * what it sends when Save is pressed, what it sends when Test is pressed, and
 * what the result line says.
 *
 * ---- THE THREE RULES THAT MATTER ------------------------------------------
 *
 *   A STORED CREDENTIAL GOES IN THE PLACEHOLDER, NEVER THE VALUE.
 *     Otherwise the user must clear eight bullet characters before typing, and
 *     an untouched form posts them back as a literal password. The server treats
 *     the mask as "unchanged" so it would survive — but only because of a second
 *     guard, and a port that relied on that would be one guard away from sending
 *     a password made of bullets.
 *
 *   A SAVE SENDS A CREDENTIAL ONLY IF ONE WAS TYPED.
 *     Key ABSENCE means "keep what is stored". That is what lets ticking a box
 *     leave a token untouched rather than re-sending and re-encrypting it.
 *
 *   A TEST SENDS NO TOGGLES, AND ONLY NON-EMPTY FIELDS.
 *     The server force-enables the channel under test, so testing before ticking
 *     the box reports the truth rather than "not configured"; and a blank field
 *     means "use what is stored", where on a SAVE it means "clear it".
 *
 *   MIKRODASH_SRC=../MikroDash node tools/usernotify-form-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('usernotify-form-check');
const app = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

/** What this gate covers, for element-coverage-audit. Declared before any work. */
const COVERS = [
  'un_telegramEnabled', 'un_pushbulletEnabled', 'un_ntfyEnabled', 'un_emailEnabled',
  'un_telegramChatId', 'un_ntfyUrl', 'un_emailTo',
  'un_telegramBotToken', 'un_pushbulletApiKey', 'un_ntfyToken',
  'saveUserNotifyBtn', 'userNotifySaveResult',
  'btn-un-test-telegram', 'btn-un-test-pushbullet', 'btn-un-test-email', 'btn-un-test-ntfy',
  'un-test-telegram-result', 'un-test-pushbullet-result', 'un-test-email-result',
  'un-test-ntfy-result',
];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

// The live field lists, read from app.js rather than retyped.
const listOf = (name) => {
  const m = app.match(new RegExp('var ' + name + "\\s*=\\s*\\[([^\\]]*)\\]"));
  assert.ok(m, 'could not read ' + name + ' from the live app.js');
  return m[1].split(',').map((s) => s.trim().replace(/^'|'$/g, '')).filter(Boolean);
};
// FROZEN: three lists lifted out of the live source. The forms they are compared
// against are built from the PORT on every run, so a port field that disappears
// still fails here.
const LIVE_BOOLS = G.value('UN_BOOLS', () => listOf('UN_BOOLS'));
const LIVE_STRS = G.value('UN_STRS', () => listOf('UN_STRS'));
const LIVE_CREDS = G.value('UN_CREDS', () => listOf('UN_CREDS'));
// VALIDATES THE RECORDING, not the reference — so it is outside the guard. A
// golden holding three empty lists would otherwise make every comparison below
// vacuously true, which is the failure mode this whole conversion risks.
for (const [n, l] of [['UN_BOOLS', LIVE_BOOLS], ['UN_STRS', LIVE_STRS], ['UN_CREDS', LIVE_CREDS]]) {
  assert.ok(Array.isArray(l) && l.length, n + ' is empty — the lift or the recording is broken');
}

// The live strings, asserted to still be there: they are what the user reads.
// GUARDED — each asks the live SOURCE a question (LOOP.md 3n).
if (LIFT.hasReference(ROOT)) {
  for (const s of ['leave blank to keep current', 'not set', '✓ Saved', '✓ Sent!']) {
    assert.ok(app.includes(s), 'the live app no longer says ' + JSON.stringify(s));
  }
}

/**
 * A form, as a map of id -> field. `null` for an id the markup does not have,
 * which is how the two dead credential entries are modelled.
 */
function makeForm(fields) {
  const nodes = {};
  for (const [id, f] of Object.entries(fields)) nodes[id] = f === null ? null : { ...f };
  return {
    read: (id) => (id in nodes ? nodes[id] : null),
    nodes,
  };
}

// ---- the live half, transcribed --------------------------------------------

function livePopulate(data, form) {
  LIVE_BOOLS.forEach((k) => { const e = form.read('un_' + k); if (e) e.checked = !!data[k]; });
  LIVE_STRS.forEach((k) => { const e = form.read('un_' + k); if (e) e.value = data[k] || ''; });
  LIVE_CREDS.forEach((k) => {
    const e = form.read('un_' + k);
    if (e) { e.value = ''; e.placeholder = data[k] ? 'leave blank to keep current' : 'not set'; }
  });
}

function liveCollect(form) {
  const out = {};
  LIVE_BOOLS.forEach((k) => { const e = form.read('un_' + k); if (e) out[k] = e.checked; });
  LIVE_STRS.forEach((k) => { const e = form.read('un_' + k); if (e) out[k] = e.value.trim(); });
  LIVE_CREDS.forEach((k) => { const e = form.read('un_' + k); if (e && e.value) out[k] = e.value; });
  return out;
}

function liveTestPayload(channel, form) {
  const payload = { channel };
  LIVE_STRS.concat(LIVE_CREDS).forEach((k) => {
    const e = form.read('un_' + k); if (e && e.value) payload[k] = e.value;
  });
  return payload;
}

// ---- the ported half --------------------------------------------------------

const ENTRY = path.join(ROOT, 'testdata', '.un-entry.ts');
fs.writeFileSync(ENTRY,
  "export { populateWith, collectForm, collectTestPayload, saveOutcome, testOutcome,\n" +
  "  credentialPlaceholder, UN_IDS } from '../web/src/pages/usernotify.js';\n");
const OUT = path.join(ROOT, 'testdata', '.un.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const port = require(OUT);

// ---- the forms --------------------------------------------------------------

const EVERY_ID = {};
// A CHECKBOX HAS A `.value` IN THE DOM — "on" by default — and the first version
// of this model gave the toggles none. That made a mutation adding the toggles
// to the TEST payload invisible: the loop reads `node.value`, which was
// undefined and therefore skipped. In a browser it would have sent
// `telegramEnabled: "on"`. The double now carries what a real input carries.
for (const k of LIVE_BOOLS) EVERY_ID['un_' + k] = { checked: false, value: 'on' };
for (const k of [...LIVE_STRS, ...LIVE_CREDS]) EVERY_ID['un_' + k] = { value: '' };
// The two dead ones: the live list names them, the markup does not have them.
EVERY_ID['un_smtpUser'] = null;
EVERY_ID['un_smtpPass'] = null;

const withValues = (over) => {
  const f = JSON.parse(JSON.stringify(EVERY_ID));
  for (const [id, v] of Object.entries(over)) f[id] = v;
  return f;
};

const CONFIGS = {
  'nothing configured': {},
  'everything set': {
    telegramEnabled: true, telegramChatId: '-100123', telegramBotToken: '••••••••',
    pushbulletEnabled: true, pushbulletApiKey: '••••••••',
    ntfyEnabled: true, ntfyUrl: 'https://ntfy.sh/x', ntfyToken: '••••••••',
    emailEnabled: true, emailTo: 'a@example.com',
  },
  'toggles on, nothing else': {
    telegramEnabled: true, pushbulletEnabled: true, ntfyEnabled: true, emailEnabled: true,
  },
  'one credential set': { telegramBotToken: '••••••••' },
  'strings only': { telegramChatId: 'c', ntfyUrl: 'u', emailTo: 'e@f.g' },
  'a null address': { emailTo: null },
};

const FORMS = {
  'an empty form': withValues({}),
  'a filled form': withValues({
    un_telegramEnabled: { checked: true },
    un_telegramChatId: { value: '-100123' },
    un_telegramBotToken: { value: 'typed-token' },
    un_ntfyUrl: { value: '  https://ntfy.sh/y  ' },
    un_emailTo: { value: ' a@example.com ' },
  }),
  'toggles only': withValues({
    un_telegramEnabled: { checked: true }, un_emailEnabled: { checked: true },
  }),
  'a credential typed, nothing else': withValues({ un_telegramBotToken: { value: 'tok' } }),
  'whitespace in every string': withValues({
    un_telegramChatId: { value: '  c  ' }, un_ntfyUrl: { value: '  u  ' },
    un_emailTo: { value: '  e  ' },
  }),
};

// ---- compare ----------------------------------------------------------------

const bad = [];
let checked = 0;

for (const [cname, cfg] of Object.entries(CONFIGS)) {
  for (const [fname, fields] of Object.entries(FORMS)) {
    const a = makeForm(fields); livePopulate(cfg, a);
    const b = makeForm(fields); port.populateWith(cfg, b.read);
    checked++;
    if (JSON.stringify(a.nodes) !== JSON.stringify(b.nodes)) {
      bad.push({ name: 'populate ' + cname + ' / ' + fname, a: a.nodes, b: b.nodes });
    }
  }
}

for (const [fname, fields] of Object.entries(FORMS)) {
  const a = liveCollect(makeForm(fields));
  const b = port.collectForm(makeForm(fields).read);
  checked++;
  if (JSON.stringify(a) !== JSON.stringify(b)) bad.push({ name: 'collect ' + fname, a, b });

  for (const ch of ['telegram', 'pushbullet', 'email', 'ntfy']) {
    const pa = liveTestPayload(ch, makeForm(fields));
    const pb = port.collectTestPayload(ch, makeForm(fields).read);
    checked++;
    if (JSON.stringify(pa) !== JSON.stringify(pb)) {
      bad.push({ name: 'test payload ' + ch + ' / ' + fname, a: pa, b: pb });
    }
  }
}

// ---- BELIEVABILITY ---------------------------------------------------------
{
  const MASK = '••••••••';
  const filled = makeForm(FORMS['a filled form']);
  livePopulate(CONFIGS['everything set'], filled);

  // The mask must never land in a VALUE.
  for (const k of LIVE_CREDS) {
    const e = filled.read('un_' + k);
    if (!e) continue;
    assert.equal(e.value, '', 'a credential value was populated: ' + k);
    assert.ok(e.placeholder, 'a credential has no placeholder: ' + k);
  }
  assert.ok(!JSON.stringify(filled.nodes).includes(MASK),
    'the mask reached a form field — an untouched form would post it back as a password');
  assert.equal(filled.read('un_telegramBotToken').placeholder, 'leave blank to keep current',
    'a stored credential does not say so');
  const empty = makeForm(FORMS['an empty form']);
  livePopulate(CONFIGS['nothing configured'], empty);
  assert.equal(empty.read('un_telegramBotToken').placeholder, 'not set',
    'an unset credential does not say so');

  // A save omits untyped credentials, and includes typed ones.
  const savedEmpty = liveCollect(makeForm(FORMS['toggles only']));
  for (const k of LIVE_CREDS) {
    assert.ok(!(k in savedEmpty), 'an untyped credential was sent on save: ' + k);
  }
  const savedTyped = liveCollect(makeForm(FORMS['a credential typed, nothing else']));
  assert.equal(savedTyped.telegramBotToken, 'tok', 'a typed credential was not sent');
  assert.ok('telegramEnabled' in savedTyped, 'a save omitted the toggles');
  assert.equal(liveCollect(makeForm(FORMS['whitespace in every string'])).telegramChatId, 'c',
    'a save did not trim');

  // A test sends NO toggles and only non-empty fields.
  const t = liveTestPayload('telegram', makeForm(FORMS['a filled form']));
  for (const k of LIVE_BOOLS) assert.ok(!(k in t), 'a test payload carried a toggle: ' + k);
  // ...and the toggles are only excluded because the live loop does not iterate
  // them. If they were iterated they WOULD be sent, since a checkbox has a
  // value — which is what makes the previous assertion worth making.
  assert.equal(makeForm(EVERY_ID).read('un_telegramEnabled').value, 'on',
    'the toggle double has no value, so excluding the toggles proves nothing');
  assert.equal(t.channel, 'telegram', 'the test payload does not name its channel');
  assert.ok(!('emailTo' in liveTestPayload('telegram', makeForm(FORMS['toggles only']))),
    'a test sent an empty field');
  assert.equal(t.ntfyUrl, '  https://ntfy.sh/y  ',
    'a test TRIMMED, which the live client does not — the route trims nothing either');

  // The two dead credential entries must reach nothing.
  assert.ok(LIVE_CREDS.includes('smtpPass'),
    'the live list no longer names smtpPass — this gate models it as dead and should be updated');
  const all = JSON.stringify(liveCollect(makeForm(FORMS['a filled form'])));
  assert.ok(!all.includes('smtpPass'), 'a dead field reached a payload');
}

// The result lines.
for (const [ok, err] of [[true, undefined], [false, 'boom'], [false, undefined]]) {
  checked += 2;
  const s = port.saveOutcome(ok, err);
  const t = port.testOutcome(ok, err);
  const wantSave = ok ? '✓ Saved' : '✗ ' + (err || 'failed');
  const wantTest = ok ? '✓ Sent!' : '✗ ' + (err || 'failed');
  if (s.text !== wantSave) bad.push({ name: 'saveOutcome', a: wantSave, b: s.text });
  if (t.text !== wantTest) bad.push({ name: 'testOutcome', a: wantTest, b: t.text });
  if (s.clearAfterMs !== 4000) bad.push({ name: 'save clears after', a: 4000, b: s.clearAfterMs });
  if (t.clearAfterMs !== 5000) bad.push({ name: 'test clears after', a: 5000, b: t.clearAfterMs });
}

fs.rmSync(OUT, { force: true });
if (bad.length) {
  for (const x of bad) {
    console.error('[' + x.name + ']');
    console.error('  live ' + JSON.stringify(x.a));
    console.error('  port ' + JSON.stringify(x.b));
  }
  console.error('\nusernotify-form-check: ' + bad.length + ' of ' + checked + ' cases differ');
  process.exit(1);
}
console.log('usernotify-form-check: ' + checked + ' cases identical');
