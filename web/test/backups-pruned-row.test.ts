/**
 * A PRUNED ROW MUST NOT CLAIM TO BE A STORED BACKUP.
 *
 * ── THE REPORT ──────────────────────────────────────────────────────────────
 *
 * "Keep at most is set to 10, but there are more than 10 backups listed."
 *
 * Retention was working exactly. Measured on the live install: keepCount 10 gave
 * 10 files on disk and 10 unpruned rows, and keepDays 30 correctly removed
 * nothing because every backup was inside the window. The Restore points card
 * read 10 and the Disk used card counted only live rows.
 *
 * The HISTORY table listed 19, and it was right to — it is history, and the nine
 * extra rows are runs whose files retention has since removed. What was wrong is
 * what those nine rows SAID:
 *
 *	RESULT  Stored      ← green badge, and false: it is not stored any more
 *	SIZE    3.3 MB      ← false, and contradicts the Disk used card
 *	ACTIONS pruned      ← the only truthful cell, in the quietest column
 *
 * `outcome` records what the RUN did and never changes afterwards, so a row went
 * on reporting the day it ran. Nine tombstones dressed as backups is not history,
 * it is nine wrong rows — and the arithmetic an operator does at a glance is
 * "count the green badges", which is exactly what produced the report.
 *
 * ── WHAT THIS PINS ──────────────────────────────────────────────────────────
 *
 * Not the retention arithmetic — that lives in internal/backups and has its own
 * corpus. This pins the CLAIM the table makes about a row whose files are gone.
 *
 * ── AND THESE ROWS ARE NO LONGER PRODUCED ───────────────────────────────────
 *
 * Recorded so nobody reads the cases below as a normal state. Retention now
 * DELETES the row along with the files rather than marking it, and
 * `PurgePrunedBackups` clears the ones older builds left, so a fresh install
 * will never show one.
 *
 * The rendering and this test are kept anyway, and deliberately: `pruned` is
 * still on the wire, the guards on restore and download still read it, and a
 * database that predates the change is a real thing an operator can arrive with
 * — a /data copied from an older install, or one restored from their own backup
 * of it, before the purge has run. If such a row ever reaches the page again it
 * must not go back to claiming it is a stored 3.3MB backup.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.bk-entry.ts');
fs.writeFileSync(ENTRY, "export { initBackupsPage } from '../web/src/pages/backups.js';\n");
const OUT = path.join(ROOT, 'testdata', '.bk.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = [
  'bkBadge', 'bkDelete', 'bkDiffBody', 'bkDiffModal', 'bkDiffSummary', 'bkDiffTitle',
  'bkEnabled', 'bkHistoryActions', 'bkKeepCount', 'bkKeepDays', 'bkNote', 'bkPickAll',
  'bkRestore', 'bkRun', 'bkSave', 'bkSchedule', 'bkSettingsActions', 'bkTable',
  'bkTime', 'bkTimeHint', 'bkSumLast', 'bkSumStored', 'bkSumBytes', 'bkSumSchedule',
];

function row(over) {
  return Object.assign({
    id: 1, takenAt: 1788000000000, outcome: 'changed', source: 'schedule',
    actor: '', stem: '2026-08-27T060323', bytes: 3500000, osVersion: '7.24',
    pruned: false, error: '',
  }, over);
}

function boot(rows, summary) {
  const doc = makeDoc(IDS, { allowUnknown: ['bkRouterName'] });
  const handlers = {};
  const socket = { on: (ev, fn) => { handlers[ev] = fn; }, emit: () => {} };
  const prevDoc = global.document;
  const prevWin = global.window;
  global.document = doc;
  global.window = { addEventListener: () => {}, setTimeout, clearTimeout, confirm: () => true };
  const { initBackupsPage } = require(OUT);
  initBackupsPage(socket, () => true);
  handlers['backups:state']({
    routerId: 'r1', label: 'R1', permitted: true, running: false,
    settings: {
      enabled: true, schedule: 'daily', time: '08:00',
      timezone: 'Europe/Berlin', keepCount: 10, keepDays: 30,
    },
    rows,
    summary: Object.assign(
      { runs: rows.length, stored: 0, bytes: 0, lastAt: 1788000000000 }, summary || {}),
  });
  return {
    html: String(doc.nodes['bkTable'].innerHTML),
    restore: () => { global.document = prevDoc; global.window = prevWin; },
  };
}

// ── 1. a pruned row says Pruned, and claims no disk ─────────────────────────
{
  const { html, restore } = boot([
    row({ id: 45, pruned: false }),
    row({ id: 25, pruned: true }),
  ]);
  restore();
  const cells = html.split('<tr').slice(1);
  assert.strictEqual(cells.length, 2, 'expected two rows, got ' + cells.length);
  const [live, pruned] = cells;

  assert.ok(/>Stored</.test(live), 'the live row no longer says Stored:\n' + live);
  assert.ok(/MB</.test(live), 'the live row lost its size:\n' + live);

  assert.ok(!/>Stored</.test(pruned),
    'a PRUNED row still shows the green "Stored" badge. Its files are gone, so ' +
    'that is a false statement, and counting green badges is exactly how "Keep ' +
    'at most 10" came to look broken:\n' + pruned);
  assert.ok(/>Pruned</.test(pruned),
    'a pruned row does not say Pruned in the Result column; the only mention was ' +
    'a muted note in the actions cell, which is the quietest column there is:\n' + pruned);
  assert.ok(!/MB</.test(pruned),
    'a pruned row still prints a size. That disk has been freed, and the claim ' +
    'contradicts the Disk used card, which counts live rows only:\n' + pruned);
  say('ok  a pruned row says Pruned and claims no size');
}

// ── 2. and it still cannot be restored or downloaded ────────────────────────
//
// The label is cosmetic; this is the property that matters, and it existed
// before this change. Asserted so a later edit to the badge cannot quietly take
// the guard with it.
{
  const { html, restore } = boot([row({ id: 25, pruned: true })]);
  restore();
  assert.ok(!/data-bk-diff/.test(html),
    'a pruned row offers Changes, which needs the file it no longer has:\n' + html);
  assert.ok(!/\/rsc|\/backup"/.test(html),
    'a pruned row offers a download link for files retention removed:\n' + html);
  assert.ok(/pruned/.test(html), 'the pruned note vanished from the actions cell');
  say('ok  a pruned row offers no diff and no download');
}

// ── 3. an UNCHANGED run is not a pruned one, and must not be relabelled ─────
//
// Both have no file, and it would be easy to collapse them. They answer
// different questions: unchanged means the configuration matched and nothing
// needed storing; pruned means something WAS stored and retention removed it.
{
  const { html, restore } = boot([
    row({ id: 7, outcome: 'unchanged', stem: '', bytes: 0, pruned: false }),
  ]);
  restore();
  assert.ok(/>No change</.test(html),
    'an unchanged run no longer reads "No change" — it has been folded into the ' +
    'pruned state, and the two mean different things:\n' + html);
  assert.ok(!/>Pruned</.test(html),
    'an unchanged run is labelled Pruned. Nothing was removed; nothing was ' +
    'stored in the first place:\n' + html);
  say('ok  an unchanged run is still "No change", not "Pruned"');
}

fs.rmSync(OUT, { force: true });
say('backups-pruned-row: all checks passed');
