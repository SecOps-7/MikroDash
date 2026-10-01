/**
 * A DEVICES CARD IS AN OVERVIEW, AND IT CLAIMS NOTHING THE SERVER DID NOT SAY.
 *
 *   - THE UPDATE PILL needs `updateAvailable === true`. Null is "the check has
 *     not run", and drawing it as either answer would be a guess.
 *   - THE BACKUP LINE is silent on null: the server sends null both for "never
 *     backed up" and for "you may not see this router's backups", so "No backup"
 *     would be a claim to someone who simply cannot see them.
 *   - A FAILED BACKUP reads as failed, however recent - "Backup 2m ago" about a
 *     run that produced nothing is the dangerous wording.
 *   - Everything the router supplies is escaped.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const OUT = path.join(ROOT, 'testdata', '.dvcard.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'devices-card.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const C = require(OUT);

const NOW = 1_000_000_000_000, H = 3600_000;

function row(over) {
  return Object.assign({
    id: 'r1', label: 'Edge', host: '198.51.100.1', isActive: false,
    connected: true, online: true, known: true, lastError: null, openAlerts: 0,
    cpu: null, uptime: '1d18h57m12s', memPct: null, hddPct: null,
    version: '7.24.4', boardName: 'hAP ax3', arch: null, serial: null,
    licenseLevel: null, updateAvailable: null, latestVersion: null, clients: null,
    siteIds: [], siteNames: [], siteId: null, siteName: null, geo: null,
  }, over || {});
}
function ov(backup) {
  return { routerId: 'r1', spans: [{ from: NOW - 24 * H, to: NOW, state: 'up' }],
    uptimePct: 100, monitoredMs: 24 * H, backup };
}

// ── the update pill ─────────────────────────────────────────────────────────
{
  assert.strictEqual(C.updatePill(row({ updateAvailable: null })), '', 'an unchecked router shows an update pill');
  assert.strictEqual(C.updatePill(row({ updateAvailable: false })), '');
  const pill = C.updatePill(row({ updateAvailable: true, latestVersion: '7.25' }));
  assert.match(pill, /Update<\/span>$/);
  assert.match(pill, /title="RouterOS update available to 7\.25"/);
  say('ok  the update pill needs a positive answer, and null is not one');
}

// ── the backup line ─────────────────────────────────────────────────────────
{
  assert.strictEqual(C.backupHtml(ov(null), NOW), '', 'a null backup drew a claim');
  assert.strictEqual(C.backupHtml(undefined, NOW), '');
  const ok = C.backupHtml(ov({ lastAt: NOW - 2 * H, lastOutcome: 'unchanged', lastSuccessAt: NOW - 2 * H }), NOW);
  assert.match(ok, /dv-backup-ok/);
  assert.match(ok, /Backup 2h ago/);
  const bad = C.backupHtml(ov({ lastAt: NOW - 120_000, lastOutcome: 'failed', lastSuccessAt: NOW - 50 * H }), NOW);
  assert.match(bad, /dv-backup-bad/, 'a failed backup is not marked failed');
  assert.match(bad, /Backup failed 2m ago/);
  assert.match(bad, /title="Last good backup /, 'a failed backup does not say when one last worked');
  const never = C.backupHtml(ov({ lastAt: NOW - H, lastOutcome: 'failed', lastSuccessAt: null }), NOW);
  assert.match(never, /No successful backup yet/);
  say('ok  backups: silent on null, failed reads as failed, with the last good one');
}

// ── the card ────────────────────────────────────────────────────────────────
{
  const html = C.deviceCardHtml(row({ siteNames: ['Home', ''] }), ov(null), false, NOW);
  assert.match(html, /class="card h-100 dv-card dv-online" role="button" tabindex="0" data-device="r1"/,
    'the card is not a focusable button carrying its router id');
  assert.match(html, /198\.51\.100\.1 · Home</, 'the subtitle lost the host or the site');
  assert.ok(!/Home, </.test(html), 'a blank (deleted) site name was drawn');
  assert.match(html, /Up 1d 18h 57m</, 'uptime is not the leading components');
  assert.match(html, /hAP ax3 · RouterOS 7\.24\.4/);
  assert.match(html, /24h ago.*now/);
  const compact = C.deviceCardHtml(row(), ov(null), true, NOW);
  assert.ok(!/dv-axis|dv-model|dv-sub/.test(compact), 'compact kept the axis, the model or the subtitle');
  assert.match(compact, /dv-strip/, 'compact lost its strip');
  const loading = C.deviceCardHtml(row(), undefined, false, NOW);
  assert.match(loading, /dv-strip-loading/, 'a card before the overview has no placeholder strip');
  say('ok  the card is a button, compact keeps the strip, and it holds its shape while loading');
}

// ── escaping ────────────────────────────────────────────────────────────────
{
  const evil = '<img src=x onerror=alert(1)>';
  const html = C.deviceCardHtml(row({ id: evil, label: evil, host: evil, boardName: evil,
    version: evil, siteNames: [evil], online: false, lastError: evil, updateAvailable: true,
    latestVersion: evil }), ov(null), false, NOW);
  assert.ok(!/<img/.test(html), 'router-supplied text reached the card unescaped');
  say('ok  everything the router supplies is escaped');
}

fs.rmSync(OUT, { force: true });
say('devices-card: all checks passed');
