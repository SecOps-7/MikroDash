/**
 * THE BROWSER HALF OF TRANSLATION (#94): t(), tl(), ts() and countryName(),
 * against an embedded catalog and against none.
 *
 * The catalog is read once from #i18n-catalog, so each case builds the page it
 * needs and calls resetI18n(). The English page (no catalog) is the control:
 * every function must hand its input back unchanged there, or an English
 * install would change under anyone who never picked a language.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.i18n-entry.ts');
fs.writeFileSync(ENTRY, "export { t, tl, ts, countryName, currentLang, languages, resetI18n } from '../web/src/i18n.js';\n");
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'i18n.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

let embedded: string | null = null;
(global as any).document = {
  getElementById: (id: string) => (id === 'i18n-catalog' && embedded !== null ? { textContent: embedded } : null),
};
const I = require(OUT);
const say = console.log.bind(console);

// ── THE ENGLISH PAGE: no catalog, everything unchanged ──────────────────────
embedded = null;
I.resetI18n();
assert.strictEqual(I.currentLang(), 'en');
assert.deepStrictEqual(I.languages(), []);
assert.strictEqual(I.t('Save'), 'Save');
assert.strictEqual(I.t('{n} devices are waiting', { n: 3 }), '3 devices are waiting');
assert.strictEqual(I.tl('Canonical Name'), 'Canonical Name');
assert.strictEqual(I.ts('Not permitted'), 'Not permitted');
assert.strictEqual(I.ts(undefined), '');
assert.strictEqual(I.countryName('DE', 'Germany'), 'Germany', 'an English page keeps the table\'s own name');
say('ok  with no catalog, every function hands its input back');

// ── A GERMAN PAGE ───────────────────────────────────────────────────────────
embedded = JSON.stringify({
  lang: 'de',
  strings: {
    'Save': 'Speichern',
    '{n} devices are waiting': 'Es warten {n} Geräte',
    'Canonical Name': 'Kanonischer Name',
    'Not permitted': 'Nicht erlaubt',
  },
  languages: [{ code: 'en', name: 'English' }, { code: 'de', name: 'Deutsch' }],
});
I.resetI18n();
assert.strictEqual(I.currentLang(), 'de');
assert.strictEqual(I.languages().length, 2);
assert.strictEqual(I.t('Save'), 'Speichern');
// The value is placed AFTER translation, where the translation puts it.
assert.strictEqual(I.t('{n} devices are waiting', { n: 3 }), 'Es warten 3 Geräte');
// A string the catalog lacks stays English: drift degrades, never breaks.
assert.strictEqual(I.t('Not in the catalog'), 'Not in the catalog');
// A placeholder with no value is left visible rather than silently dropped.
assert.strictEqual(I.t('{n} devices are waiting'), 'Es warten {n} Geräte');
say('ok  t() translates, places values after translating, and falls back to English');

assert.strictEqual(I.tl('Canonical Name'), 'Kanonischer Name');
say('ok  tl() translates a label declared in Go');

// ts() is an EXACT match: the server's own literal is translated, anything
// built from values or written by the router passes through as it came.
assert.strictEqual(I.ts('Not permitted'), 'Nicht erlaubt');
assert.strictEqual(I.ts('Not permitted: ether1'), 'Not permitted: ether1');
assert.strictEqual(I.ts('failure: already have such address'), 'failure: already have such address');
assert.strictEqual(I.ts(null), '');
say('ok  ts() translates only an exact server message');

// Country names come from the browser's own region names, not the catalog.
const de = I.countryName('DE', 'Germany');
assert.ok(de === 'Deutschland' || de === 'Germany',
  'countryName(DE) on a German page: ' + de + ' (Deutschland with full ICU, the table\'s name without)');
assert.strictEqual(I.countryName('ZZ-not-a-code', 'Nowhere'), 'Nowhere', 'an unknown code falls back to the table');
assert.strictEqual(I.countryName('XX', undefined), 'XX', 'with no table entry either, the code itself');
say('ok  countryName() uses the browser\'s region names, and falls back');

// ── A CATALOG THAT CANNOT BE READ IS ENGLISH ────────────────────────────────
embedded = '{ not json';
I.resetI18n();
assert.strictEqual(I.currentLang(), 'en');
assert.strictEqual(I.t('Save'), 'Save');
say('ok  an unreadable catalog is English');
