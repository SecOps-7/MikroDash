'use strict';
/**
 * `settings:pages` — the payload every browser gets on connect and after a save.
 *
 * ── THE KEY LIST IS DERIVED, WHICH IS WHY IT IS GENERATED ──────────────────
 *
 * `_PAGE_SETTING_KEYS` is not a literal. It is:
 *
 *     ...Pages.SETTING_KEYS                    (one per page, from the page table)
 *     five named keys                          (thresholds, ping, timezone)
 *     'userNotifyEnabled'                      (not caught by the filter below)
 *     ...every DEFAULTS key matching /^notif/ that holds a BOOLEAN
 *
 * So it grows when a page is added AND when a notification toggle is added, from
 * two different files. A hand-copied list in Go would stop carrying a new key
 * silently — and the failure is not a crash: the browser receives the payload
 * without it, and `applyPageVisibility` treats the page as hidden or the toggle
 * as off. A setting the operator can see in the form would have no effect
 * anywhere else.
 *
 * `tools/pages-table.js` does NOT cover this. It captures the roles card's
 * catalogue — key, title, settingsKey — and knows nothing about the five named
 * keys or the notif filter. `tools/settings-form-map.js` mentions
 * `_PAGE_SETTING_KEYS` in a comment and computes nothing.
 *
 * ── THE /^notif/ FILTER TAKES BOOLEANS ONLY ────────────────────────────────
 *
 * `notifTitle`, `notifBody` and `notifCooldownSec` all match the prefix and are
 * excluded by the TYPE test. A port filtering on the name alone would ship the
 * notification message text to every connected browser — a disclosure, not a
 * rendering bug.
 *
 * ── AND A MISSING KEY IS PRESENT-AND-UNDEFINED ─────────────────────────────
 *
 * `out[k] = src[k]` writes the key whatever `src` holds, so a key absent from
 * the source is still IN the object with the value `undefined` — which
 * `JSON.stringify` then DROPS. The payload on the wire omits it, and that is
 * what this corpus records. A Go port emitting an explicit null instead would
 * send a key JavaScript never sends, and `pages[k] === undefined` on the client
 * would stop being true.
 *
 * Runs on the host: DATA_DIR is pointed at a temp directory BEFORE settings.js
 * is required, because that module resolves its path at load time.
 */

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const OUT = process.env.SETTINGS_PAGES_OUT
  || path.join(ROOT, 'testdata', 'settings-pages-cases.json');

// BEFORE the require, not after.
process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-pages-'));

const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const Settings = require(path.join(SRC, 'src', 'settings.js'));
const Pages = require(path.join(SRC, 'src', 'pages.js'));

const indexSrc = fs.readFileSync(path.join(SRC, 'src', 'index.js'), 'utf8');

// ---- THE LIST AND THE PROJECTION, BOTH LIFTED ----------------------------
function liftBalanced(src, decl, open, close) {
  const n = src.split(decl).length - 1;
  assert.equal(n, 1, 'AMBIGUOUS anchor (' + n + '): ' + decl);
  const i = src.indexOf(decl);
  const from = src.indexOf(open, i);
  let depth = 0;
  for (let j = from; j < src.length; j++) {
    if (src[j] === open) depth++;
    else if (src[j] === close) { depth--; if (!depth) return { body: src.slice(from, j + 1), i }; }
  }
  throw new Error('unbalanced ' + open + ' for ' + decl);
}

// eslint-disable-next-line no-new-func
const keys = new Function('Pages', 'Settings',
  'return ' + liftBalanced(indexSrc, 'const _PAGE_SETTING_KEYS = ', '[', ']').body)(Pages, Settings);

const fn = liftBalanced(indexSrc, 'function _pageSettings(src)', '{', '}');
// eslint-disable-next-line no-new-func
const pageSettings = new Function('_PAGE_SETTING_KEYS',
  indexSrc.slice(fn.i, fn.i + (fn.body.length + (indexSrc.indexOf('{', fn.i) - fn.i)))
  + '; return _pageSettings;')(keys);

// ---- THE LIST MUST BE WHAT IT CLAIMS -------------------------------------
{
  for (const k of Pages.SETTING_KEYS) {
    assert.ok(keys.includes(k), 'the lifted list is missing the page key ' + k);
  }
  for (const k of ['alertCpuThreshold', 'alertPingLoss', 'vpnDashTopN', 'pingEnabled',
    'displayTimezone', 'userNotifyEnabled']) {
    assert.ok(keys.includes(k), 'the lifted list is missing ' + k);
  }
  const notifBools = Object.keys(Settings.DEFAULTS)
    .filter((k) => /^notif/.test(k) && typeof Settings.DEFAULTS[k] === 'boolean');
  assert.ok(notifBools.length > 0,
    'no notif booleans were found, so the filter below proves nothing');
  for (const k of notifBools) {
    assert.ok(keys.includes(k), 'the lifted list is missing the notif boolean ' + k);
  }
  // The TYPE test, which is what keeps message text off the wire.
  const notifNonBools = Object.keys(Settings.DEFAULTS)
    .filter((k) => /^notif/.test(k) && typeof Settings.DEFAULTS[k] !== 'boolean');
  assert.ok(notifNonBools.length > 0,
    'every notif key is a boolean, so nothing here distinguishes filtering by TYPE from '
    + 'filtering by name');
  for (const k of notifNonBools) {
    assert.ok(!keys.includes(k), k + ' is in the payload — filtering by name rather than '
      + 'type ships notification message text to every connected browser');
  }
  assert.equal(new Set(keys).size, keys.length, 'the list has duplicates');
}

// ---- THE CASES -----------------------------------------------------------
const CASES = {
  // The defaults, which is what the `_reset` branch emits.
  defaults: Settings.DEFAULTS,
  configured: { ...Settings.DEFAULTS, pageWifi: false, alertCpuThreshold: 90,
    displayTimezone: 'Europe/Stockholm', userNotifyEnabled: true },
  // A source missing most keys: every absent one is present-and-undefined and
  // DROPPED by JSON.stringify.
  sparse: { pageWifi: true, alertCpuThreshold: 55 },
  empty: {},
  // Extra keys are NOT copied — the projection is a whitelist, and two of these
  // are credentials.
  withExtras: { ...Settings.DEFAULTS, routerHost: '198.51.100.1', smtpPass: 'REDACTED',
    telegramBotToken: 'REDACTED' },
  // Falsy values must survive: a port using `||` or a truthiness filter would
  // drop exactly the "off" states.
  falsy: { pageWifi: false, alertCpuThreshold: 0, displayTimezone: '',
    pingEnabled: false, userNotifyEnabled: false },
  // An explicit null is NOT undefined: JSON keeps it.
  nulls: { pageWifi: null, displayTimezone: null },
};

const cases = {};
for (const [name, src] of Object.entries(CASES)) {
  // Round-tripped through JSON, because that is what reaches the browser — and
  // it is the step that drops the undefined values.
  cases[name] = { src, payload: JSON.parse(JSON.stringify(pageSettings(src))) };
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  const p = (k) => cases[k].payload;
  const has = (k, f) => Object.prototype.hasOwnProperty.call(p(k), f);

  assert.equal(Object.keys(p('defaults')).length, keys.length,
    'the defaults payload has ' + Object.keys(p('defaults')).length + ' keys, want '
    + keys.length + ' — every key is set in DEFAULTS, so none should drop');

  for (const k of ['routerHost', 'smtpPass', 'telegramBotToken']) {
    assert.ok(!has('withExtras', k), k + ' reached the browser — the projection is a '
      + 'whitelist and two of these are credentials');
  }

  // Falsy values SURVIVE.
  assert.equal(p('falsy').pageWifi, false, 'false was dropped');
  assert.equal(p('falsy').alertCpuThreshold, 0, '0 was dropped');
  assert.equal(p('falsy').displayTimezone, '', 'the empty string was dropped');
  assert.ok(has('falsy', 'pingEnabled'));

  // Absent keys are DROPPED by the JSON round trip; null is KEPT.
  assert.ok(has('sparse', 'pageWifi'));
  assert.ok(!has('sparse', 'displayTimezone'),
    'an absent key survived the JSON round trip — it is present-and-undefined in the '
    + 'object, which stringify drops');
  assert.equal(Object.keys(p('empty')).length, 0, 'an empty source produced keys');
  assert.ok(has('nulls', 'pageWifi'), 'an explicit null was dropped — null is not undefined');
  assert.equal(p('nulls').pageWifi, null);

  assert.equal(p('configured').pageWifi, false);
  assert.equal(p('configured').displayTimezone, 'Europe/Stockholm');
}

const json = JSON.stringify({ keys, cases }, null, 2) + '\n';

// The Go side EMBEDS the key list, and an embed that drifts from this corpus is
// exactly the silent failure the header describes. So the list is written out as
// its own file rather than transcribed, and `--check` compares both.
const KEYS_OUT = process.env.SETTINGS_PAGEKEYS_OUT
  || path.join(ROOT, 'internal', 'store', 'pagekeys.json');
const keysJson = JSON.stringify(keys, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('settings-pages-cases.json is STALE — re-run without --check');
    process.exit(1);
  }
  const haveKeys = fs.existsSync(KEYS_OUT) ? fs.readFileSync(KEYS_OUT, 'utf8') : '';
  if (haveKeys !== keysJson) {
    console.error('internal/store/pagekeys.json is STALE — the Go embed has drifted from '
      + 'the live list, and a key missing there is a page that silently stays hidden');
    process.exit(1);
  }
  console.log('settings-pages-cases.json is current (' + keys.length + ' keys, '
    + Object.keys(cases).length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, json);
  fs.writeFileSync(KEYS_OUT, keysJson);
  console.log('wrote ' + OUT + ' (' + keys.length + ' keys, '
    + Object.keys(cases).length + ' cases)');
}
