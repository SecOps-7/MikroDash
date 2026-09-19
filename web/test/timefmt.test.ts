/**
 * EVERY TIME IN THE INSTALL'S DISPLAY TIMEZONE (review loop, 2026-09-19).
 *
 * Only the top-bar clock honoured `displayTimezone`. Reports and Audit kept a
 * copy of the zone fed by a setter nothing called; backups, account sessions,
 * schedule runs, the database card and topology formatted in the browser's zone
 * and locale. timefmt.ts is now the one formatter, reading caps.ts's zone.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.timefmt-entry.ts');
fs.writeFileSync(ENTRY,
  "export { fmtTs, fmtDate, fmtTime } from '../web/src/timefmt.js';\n" +
  "export { applyPageVisibility } from '../web/src/caps.js';\n");
const OUT = path.join(ROOT, 'testdata', '.timefmt.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
global.document = makeDoc([], { allowUnknown: ['*'] }); // the nav sweep's lookups are not this test's subject
global.window = { addEventListener: () => {} };
const mod = require(OUT);
fs.rmSync(OUT, { force: true });

const T = Date.UTC(2026, 0, 2, 3, 4, 5); // 2026-01-02 03:04:05 UTC

// The formatter, given a zone.
assert.strictEqual(mod.fmtTs(T, true, 'Asia/Tokyo'), '2026-01-02 12:04:05');
assert.strictEqual(mod.fmtTs(T, false, 'UTC'), '2026-01-02 03:04');
assert.strictEqual(mod.fmtDate(T, 'America/Los_Angeles'), '2026-01-01');
assert.strictEqual(mod.fmtTime(Date.UTC(2026, 0, 2, 15, 0, 0), true, 'Asia/Tokyo'), '00:00:00',
  'midnight must read 00, not 24');
assert.strictEqual(mod.fmtTs(0), '—', 'a missing time is a dash, never 1970');
assert.strictEqual(mod.fmtTs(new Date(T).toISOString(), true, 'UTC'), '2026-01-02 03:04:05', 'an ISO string');

// The wiring: the settings payload sets the zone every formatter reads.
mod.applyPageVisibility({ displayTimezone: 'Asia/Tokyo' });
assert.strictEqual(mod.fmtTs(T), '2026-01-02 12:04:05', 'the settings zone did not reach the formatter');
mod.applyPageVisibility({ displayTimezone: 'UTC' });
assert.strictEqual(mod.fmtTs(T), '2026-01-02 03:04:05', 'a changed zone did not replace the old one');

// And nothing else formats a date itself.
const offenders: string[] = [];
const walk = (dir: string): void => {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const f = path.join(dir, e.name);
    if (e.isDirectory()) { if (e.name !== 'gen') walk(f); continue; }
    if (!f.endsWith('.ts') || f.endsWith('timefmt.ts')) continue;
    const src = fs.readFileSync(f, 'utf8');
    if (/toLocaleDateString\(|toLocaleTimeString\(|Intl\.DateTimeFormat\(|new Date\([^)]*\)\.toLocaleString\(/.test(src)) {
      offenders.push(path.relative(ROOT, f));
    }
  }
};
walk(path.join(ROOT, 'web', 'src'));
assert.deepStrictEqual(offenders, [], 'these format a date outside timefmt.ts: ' + offenders.join(', '));
console.log('ok  every time follows the display timezone, through one formatter');
