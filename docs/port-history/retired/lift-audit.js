#!/usr/bin/env node
'use strict';
/**
 * EVERY PAGE `live-renderer.js` CLAIMS IT CAN LIFT, ACTUALLY LIFTED.
 *
 * ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
 *
 * `live-renderer.js dhcp` threw for an unknown length of time. Its declaration
 * list named `var lastTalkers = null, lastLanData = null;`, which the live side
 * DELETED when it fixed the empty-payload guard — and nothing noticed, because
 * `live-renderer` is step ONE of two. It writes a bundle; a step-two gate has to
 * consume it, and no gate consumes dhcp's. So the entry rotted in silence.
 *
 * `CLAUDE.md` already warns that step one "COMPARES NOTHING" and that a page
 * whose bundle no gate consumes is unverified while looking covered. This adds
 * the smaller guarantee that was missing underneath that: the lift still WORKS.
 * A broken lift is not an unverified page, it is a tool that cannot be used at
 * all — and the difference only shows when somebody tries.
 *
 * This does NOT check that anything compares the bundle. `page-gate-audit`
 * answers that question, and answering it here too would be two ledgers for one
 * fact.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/lift-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..', '..', '..');
const TOOL = path.join(__dirname, 'live-renderer.js');

const body = fs.readFileSync(TOOL, 'utf8');

// The page names live-renderer accepts, read from its own tables rather than
// listed here — a second list would be a second thing to keep in step.
const pages = new Set();
for (const m of body.matchAll(/^\s{2}([a-z][a-zA-Z0-9]*):\s*\[/gm)) pages.add(m[1]);
if (pages.size < 5) {
  shout('lift-audit: only %d page names matched — the pattern has drifted from live-renderer.js, '
    + 'and a scan that finds nothing would report every page as fine', pages.size);
  process.exit(1);
}

const broken = [];
for (const page of [...pages].sort()) {
  const r = spawnSync(process.execPath, [TOOL, page], {
    cwd: ROOT, encoding: 'utf8',
    env: { ...process.env, MIKRODASH_SRC: process.env.MIKRODASH_SRC || '../MikroDash' },
  });
  if (r.status !== 0) {
    const msg = (r.stderr || r.stdout || '').split('\n').find((l) => l.includes('Error')) || 'exit ' + r.status;
    broken.push([page, msg.trim()]);
  }
}

say('lift-audit: %d page(s) live-renderer can lift; %d broken', pages.size - broken.length, broken.length);
if (broken.length) {
  shout('\nThese pages are named in live-renderer.js and no longer lift. The live source has moved\n'
    + 'under them, and because step one COMPARES NOTHING this failed silently:\n');
  for (const [page, msg] of broken) shout('  ✗ ' + page + ' — ' + msg);
  process.exit(1);
}
say('every page live-renderer names still lifts');
