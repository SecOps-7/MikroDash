/**
 * The Settings form collector, and the Save button that sends it.
 *
 * ── WHAT WAS BROKEN ────────────────────────────────────────────────────────
 *
 * `#settingsSaveBtn` was bound to nothing, so NO server-side setting could be
 * saved from any tab. Reported on issue #126 as "Appearance Save not working",
 * which was the nearest visible symptom of a page-wide failure.
 *
 * ── WHAT THIS FILE IS FOR, AND WHAT IT CANNOT DO ───────────────────────────
 *
 * It drives the real module and asserts the body it builds. It CANNOT prove
 * `main.ts` mounts the module - that stays green with the mount deleted, which
 * is exactly how the Add Device button and the setup overlay each shipped. That
 * half is `TestTheSettingsSaveIsMounted` in `internal/verify`.
 *
 * The element set is PARSED FROM THE MARKUP rather than hand-listed, so a field
 * added to `page-settings.html` cannot leave this test asserting over a surface
 * that no longer exists.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
// The same generated table the server's own validator is built from, so this
// test cannot disagree with it about which keys are credentials.
import { PLACEHOLDER_CREDENTIALS } from '../src/gen/settings-form-map';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

/** Every `s_*` id the real Settings markup carries, with its kind. */
function markupFields(): { id: string; checkbox: boolean }[] {
  const html = fs.readFileSync(
    path.join(ROOT, 'web', 'src', 'ui', 'page-settings.html'), 'utf8');
  const out: { id: string; checkbox: boolean }[] = [];
  const tag = /<(input|select|textarea)\b[^>]*>/g;
  let m;
  while ((m = tag.exec(html))) {
    const id = /id="(s_[A-Za-z0-9_]+)"/.exec(m[0]);
    if (!id) continue;
    out.push({ id: id[1], checkbox: /type="checkbox"/.test(m[0]) });
  }
  return out;
}

function makeEl(id, checkbox) {
  return {
    id, value: '', checked: false, type: checkbox ? 'checkbox' : 'text',
    style: {}, placeholder: '',
    addEventListener() {}, closest: () => null,
  };
}

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-settings-save.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings-save.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

/** A document holding every real settings field, plus the Save button. */
function mount(opts = {}) {
  const els = {};
  markupFields().forEach((f) => { els[f.id] = makeEl(f.id, f.checkbox); });
  (opts.drop || []).forEach((id) => { delete els[id]; });
  els.settingsSaveBtn = { id: 'settingsSaveBtn', innerHTML: '<svg></svg> Save Settings',
    textContent: '', disabled: false, style: {},
    _clicks: [], addEventListener(ev, fn) { if (ev === 'click') this._clicks.push(fn); },
    click() { this._clicks.forEach((f) => f()); }, closest: () => null };
  els.settingsBanner = makeEl('settingsBanner', false);
  els.settingsBanner.className = '';

  global.document = {
    getElementById: (id) => els[id] || null,
    querySelectorAll: () => [], addEventListener: () => {},
    body: makeEl('body', false),
  };
  global.window = { addEventListener: () => {} };
  const page = require(OUT);
  return { els, page };
}

let failed = 0;

// ── AWAITED, AND THAT IS NOT A DETAIL ──────────────────────────────────────
//
// The button cases are async. A synchronous `check` calls them, gets a promise
// back and reports "ok" before any assertion has run, so a rejected assertion
// surfaces later as an uncaught rejection and the case passes. All three button
// cases did exactly that on the first draft, and a mutation that reloaded the
// form after a 403 sailed through them.
const cases: [string, () => unknown][] = [];
function check(what, fn) { cases.push([what, fn]); }

async function run() {
  for (const [what, fn] of cases) {
    try { await fn(); say('  ok   ' + what); }
    catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
  }
}

// The poll sliders never exist in static markup, so every case supplies them.
const POLL = () => ({ pollSystem: 2000, pollConns: 3000 });

say('settings: the Save button collects and posts the form');

check('integers are numbers, not the strings the DOM holds', () => {
  const { els, page } = mount();
  els.s_topN.value = '5';
  els.s_maxConns.value = '20000';
  const body = page.collectSettingsForm(POLL);
  assert.strictEqual(body.topN, 5, 'topN = ' + JSON.stringify(body.topN));
  assert.strictEqual(body.maxConns, 20000);
});

// THE `smtpPort` FALLBACK CHECK IS GONE WITH THE FIELD IT GUARDED.
//
// It pinned `parseInt(raw, 10) || 587` - a blank port meaning the default rather
// than "leave whatever was there". The mail server became a notification
// channel, so there is no `s_smtpPort` on this page and no such branch in
// `collectSettingsForm` any more. The rule now belongs to the channel modal's
// own port field and is checked there.
//
// Recorded rather than silently dropped, because a check that vanishes reads
// exactly like one that never existed.

check('updateCheckHours: blank is omitted, out of range is clamped', () => {
  const { els, page } = mount();
  els.s_updateCheckHours.value = '';
  assert.ok(!('updateCheckHours' in page.collectSettingsForm(POLL)),
    'a blank hours box sent a key; the server would ignore whatever it was');
  els.s_updateCheckHours.value = '500';
  assert.strictEqual(page.collectSettingsForm(POLL).updateCheckHours, 168,
    'not clamped - the server IGNORES an out-of-range integer, so the typed '
    + 'value would vanish with nothing on screen to explain it');
  els.s_updateCheckHours.value = '0';
  assert.strictEqual(page.collectSettingsForm(POLL).updateCheckHours, 1);
});

check('strings are trimmed, but a timezone is not', () => {
  const { els, page } = mount();
  els.s_notifTitle.value = '  hello  ';
  els.s_displayTimezone.value = ' Europe/Berlin ';
  const body = page.collectSettingsForm(POLL);
  assert.strictEqual(body.notifTitle, 'hello');
  // The credential half of this check moved to 'a typed credential is sent
  // verbatim' below, where `aiApiKey` is now the only field it can be made
  // against: `smtpUser` was the untrimmed value field and it is a channel
  // secret now.
  assert.strictEqual(body.displayTimezone, ' Europe/Berlin ',
    'the timezone was trimmed; it comes from a select rather than a typed box, '
    + 'so a trim here is a rule with no case behind it');
});

check('checkboxes send real booleans, never "on" or 1', () => {
  const { els, page } = mount();
  els.s_pageDns.checked = true;
  els.s_pageLogs.checked = false;
  const body = page.collectSettingsForm(POLL);
  assert.strictEqual(body.pageDns, true);
  assert.strictEqual(body.pageLogs, false);
  const serialised = JSON.stringify(body);
  assert.ok(!/"(on|off)"/.test(serialised),
    'an HTML-form style value reached the body; the server counts only a real '
    + 'true or the string "true", so "on" would switch every page OFF');
});

// THE CREDENTIAL LIST IS DERIVED, NOT TYPED OUT.
//
// It used to name four keys. Three of them - telegramBotToken, pushbulletApiKey
// and ntfyToken - left the Settings page when those transports became
// notification channels, and this check broke on ids that no longer exist. A
// hardcoded list says nothing about the page it is meant to be testing, so it
// now reads the generated PLACEHOLDER_CREDENTIALS and tests whichever of them
// the markup actually renders. Add one to the page and it is covered; remove one
// and nothing here has to be remembered.
//
// PLACEHOLDER_CREDENTIALS rather than CRED_FIELDS: the latter is every ENCRYPTED
// key, which includes `smtpUser` - a plain text box that is sent like any other
// string. The rule being checked here is about the fields whose stored value
// lives in the PLACEHOLDER, which is exactly what makes a blank box mean
// "unchanged" instead of "clear it".
check('a blank credential is OMITTED, never sent as an empty string', () => {
  const { els, page } = mount();
  const creds = Object.keys(PLACEHOLDER_CREDENTIALS).filter((k) => els['s_' + k]);
  assert.ok(creds.length > 0,
    'no credential field was found on the settings page, so this check is '
    + 'asserting nothing: ' + JSON.stringify(Object.keys(PLACEHOLDER_CREDENTIALS)));
  creds.forEach((k) => { els['s_' + k].value = ''; });
  const body = page.collectSettingsForm(POLL);
  creds.forEach((k) => {
    assert.ok(!(k in body),
      k + ' was sent while blank. populateSettings blanks these on every load, '
      + 'so an untouched box is always empty - and the server reads an empty '
      + 'string as an explicit DESTRUCTIVE CLEAR. This wipes every stored secret '
      + 'on the first Save.');
  });
});

check('a typed credential is sent verbatim', () => {
  const { els, page } = mount();
  els.s_aiApiKey.value = ' hunter2 ';
  assert.strictEqual(page.collectSettingsForm(POLL).aiApiKey, ' hunter2 ',
    'a typed secret must go exactly as typed, spaces included - a token with a '
    + 'trailing space is a token the operator pasted');
});

// THE MASK ROUND TRIP HAS NO FIELD LEFT TO CHECK.
//
// `smtpUser` was the one value field the server masked on read: the page handed
// the mask straight back and `store.IsMasked` dropped it from the updates.
// Changing that end silently cleared the SMTP username on every save, which is
// why it was pinned here. The mail server is a notification channel now, so that
// credential lives in a channel's sealed config and never reaches this form.
//
// Every credential this page still holds is a PLACEHOLDER credential, which
// `populateSettings` blanks on load - so the mask is never in the box, and the
// rule that replaces this one is 'a blank credential is OMITTED' above.

check('authMode follows the sign-in toggle, both ways', () => {
  const on = mount();
  on.els.s_authEnabled.checked = true;
  assert.strictEqual(on.page.collectSettingsForm(POLL).authMode, 'modern');
  const off = mount();
  off.els.s_authEnabled.checked = false;
  assert.strictEqual(off.page.collectSettingsForm(POLL).authMode, 'none');
});

check('a missing element contributes no key', () => {
  const { page } = mount({ drop: ['s_topN'] });
  assert.ok(!('topN' in page.collectSettingsForm(POLL)),
    'a key was sent for an input that does not exist. That guard is what lets '
    + 'this page carry fewer fields than the form map lists - eight legacy '
    + 'router keys have no element here.');
});

check('the poll sliders are included, from their own reader', () => {
  const { page } = mount();
  const body = page.collectSettingsForm(POLL);
  assert.strictEqual(body.pollSystem, 2000);
  assert.strictEqual(body.pollConns, 3000);
  assert.ok(!('customPollProfile' in body),
    'the saved custom preset belongs to the Save Custom Profile button and must '
    + 'not be rewritten by this one');
});

// ── the button ─────────────────────────────────────────────────────────────

function wire(response) {
  const { els, page } = mount();
  const calls = [];
  let reloads = 0;
  global.fetch = (url, init) => {
    calls.push({ url, method: init.method, credentials: init.credentials,
      body: JSON.parse(init.body) });
    return Promise.resolve({ ok: response.status !== 403, status: response.status || 200,
      json: () => Promise.resolve(response.json) });
  };
  page.initSettingsSave(() => { reloads++; });
  return { els, calls, reloads: () => reloads };
}

check('a click posts exactly one body to /api/settings', async () => {
  const w = wire({ json: { ok: true } });
  w.els.settingsSaveBtn.click();
  await new Promise((r) => setTimeout(r, 20));
  assert.strictEqual(w.calls.length, 1, 'expected one POST, got ' + w.calls.length);
  assert.strictEqual(w.calls[0].url, '/api/settings');
  assert.strictEqual(w.calls[0].method, 'POST');
  assert.strictEqual(w.calls[0].credentials, 'same-origin',
    'without same-origin the session cookie is not sent and every save is a 401');
});

check('a refusal does NOT reload, and does not claim success', async () => {
  const w = wire({ status: 403, json: { ok: false, error: 'Administrator access required' } });
  w.els.settingsSaveBtn.click();
  await new Promise((r) => setTimeout(r, 20));
  assert.strictEqual(w.reloads(), 0,
    'the form was reloaded after a refusal - that repaints every field from the '
    + 'server and blanks the credential boxes, so everything typed is lost and '
    + 'the page looks exactly as it does after a success');
  assert.ok(!/sbanner-ok/.test(w.els.settingsBanner.className),
    'a refused save reported success');
  assert.strictEqual(w.els.settingsSaveBtn.disabled, false, 'the button stayed disabled');
  assert.ok(/svg/.test(w.els.settingsSaveBtn.innerHTML),
    'the button lost its icon; restore innerHTML, not textContent');
});

check('a success reloads exactly once', async () => {
  const w = wire({ json: { ok: true } });
  w.els.settingsSaveBtn.click();
  await new Promise((r) => setTimeout(r, 20));
  assert.strictEqual(w.reloads(), 1,
    'the reload is what re-blanks the credential inputs, so a second Save does '
    + 'not re-post a secret typed for the first');
  assert.ok(/sbanner-ok/.test(w.els.settingsBanner.className));
});

void run().then(() => {
  if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
  say('\nall passed');
});
