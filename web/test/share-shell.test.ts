/**
 * THE SHARE SHELL'S DECISIONS (share links, #165 follow-up).
 *
 * A slideshow steps through its own list only, skips what it must, stops on
 * its last slide when told to, and never runs a transition for a screen that
 * asks for reduced motion. The theme follows the link before the browser.
 * The Settings tab names what a link shows and warns about the mistakes an
 * administrator cannot see from the form: an https site framing an http
 * link, a link that never expires, and an address list behind an untrusted
 * proxy.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.share-shell-entry.ts');
fs.writeFileSync(ENTRY,
  "export * from '../web/src/share-logic.js';\n" +
  "export { subjectOf, statusPill, formWarnings, shownOnce } from '../web/src/pages/settings-shares.js';\n");
const OUT = path.join(ROOT, 'testdata', '.share-shell-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const m = require(OUT);
fs.rmSync(OUT, { force: true });

const slide = (page, shareable = true, secs = 0) => ({ page, router: 'Edge', secs, shareable });
const four = [slide('dashboard'), slide('interfaces'), slide('logs'), slide('dns')];

// ── Stepping ────────────────────────────────────────────────────────────────
assert.strictEqual(m.nextSlide(-1, four, new Set()), 0, 'the first slide is not 0');
assert.strictEqual(m.nextSlide(3, four, new Set()), 0, 'a looping slideshow does not wrap');
assert.strictEqual(m.nextSlide(3, four, new Set(), 'stop'), -1, 'a stopping slideshow went past its end');
assert.strictEqual(m.nextSlide(0, four, new Set([1, 2])), 3, 'skipped slides were shown');
assert.strictEqual(m.nextSlide(0, four, new Set([0, 1, 2, 3])), -1, 'with every slide skipped there is no next');
assert.strictEqual(m.nextSlide(1, four, new Set([2, 3]), 'stop'), -1, 'stop: the rest skipped means the end');
assert.strictEqual(m.prevSlide(0, four, new Set()), 3, 'previous from the first is not the last');
assert.strictEqual(m.prevSlide(3, four, new Set([2])), 1, 'previous did not skip');

// ── Timing, theme, motion ───────────────────────────────────────────────────
assert.strictEqual(m.slideSecs(slide('x', true, 45), { defaultSecs: 20 }), 45, 'a slide\'s own time was ignored');
assert.strictEqual(m.slideSecs(slide('x'), { defaultSecs: 20 }), 20, 'the default time was ignored');
assert.strictEqual(m.slideSecs(slide('x'), {}), 30, 'the fallback is not 30 seconds');
assert.strictEqual(m.themeFor('light', true), 'light', 'a Light link followed a dark browser');
assert.strictEqual(m.themeFor('dark', false), 'dark', 'a Dark link followed a light browser');
assert.strictEqual(m.themeFor('', false), 'light', 'Follow the browser did not follow it');
assert.strictEqual(m.transitionMs({ transition: 'fade', speed: 'slow' }, false), 1000);
assert.strictEqual(m.transitionMs({ transition: 'fade', speed: 'fast' }, false), 300);
assert.strictEqual(m.transitionMs({ transition: 'slide' }, true), 0, 'reduced motion still animated');
assert.strictEqual(m.transitionMs({ transition: 'none' }, false), 0);
assert.strictEqual(m.onByDefault(undefined), true);
assert.strictEqual(m.onByDefault(false), false);
assert.strictEqual(m.expiryNote(null), '', 'a never-expiring link claims an expiry');
assert.ok(m.renewInMs(Date.now() + 600_000, Date.now()) > 500_000, 'a fresh ticket is renewed too early');
assert.strictEqual(m.renewInMs(Date.now() + 10_000, Date.now()), 5000, 'a lapsing ticket is not renewed promptly');

// ── The Settings tab ────────────────────────────────────────────────────────
const opts = { pages: [{ key: 'dashboard', label: 'Dashboard' }], routers: [{ key: 'r1', label: 'Edge' }],
  cards: [{ key: 'dc-card-ping', label: 'Ping' }] };
const link = (over) => ({ kind: 'page', page: 'dashboard', routerId: 'r1', slides: [], options: {},
  status: 'active', neverExpires: false, ...over });
assert.strictEqual(m.subjectOf(link({}), opts), 'Dashboard on Edge');
assert.strictEqual(m.subjectOf(link({ kind: 'card', options: { card: 'dc-card-ping' } }), opts), 'Ping on Edge');
assert.strictEqual(m.subjectOf(link({ kind: 'slideshow', slides: [{ routerId: 'r1' }, { routerId: 'r2' }, { routerId: 'r1' }] }), opts),
  '3 pages on 2 routers');
assert.match(m.statusPill(link({ status: 'revoked', neverExpires: true })), />Revoked</, 'a revoked link is not shown revoked');
assert.match(m.statusPill(link({ neverExpires: true })), />Never expires</);
assert.match(m.statusPill(link({ status: 'expired' })), />Expired</);
assert.match(m.statusPill(link({})), />Active</);

const w = (...a) => m.formWarnings(...a).join(' | ');
assert.match(w('http://md.lan:3081', ['https://ha.example.com'], '7d', false, []), /https page cannot embed an http link/);
assert.doesNotMatch(w('https://md.example.com', ['https://ha.example.com'], '7d', false, []), /cannot embed/);
assert.match(w('https://md.example.com', [], 'never', false, []), /never expires/);
assert.match(w('https://md.example.com', [], '7d', true, ['198.51.100.7']), /does not trust/);
assert.strictEqual(w('https://md.example.com', [], '7d', true, []), '', 'a warning with nothing to warn about');

assert.strictEqual(m.shownOnce('page', 'http://md:3081/s/abc'), 'http://md:3081/s/abc');
assert.strictEqual(m.shownOnce('feed', 'http://md:3081/s/abc'), 'http://md:3081/share/api/feed\nAuthorization: Bearer abc',
  'a feed was shown as a page link, which would put its token in a URL');

say('share shell: stepping, timing, theme, motion, and the Settings tab\'s words and warnings hold');
