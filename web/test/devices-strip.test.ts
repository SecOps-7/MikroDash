/**
 * THE CONNECTIVITY STRIP DRAWS WHAT THE SERVER DECIDED, AND NOTHING VANISHES.
 *
 * `history.Spans` tiles the window and computes the uptime; `stripHtml` only
 * positions segments. Three things here are easy to break and invisible in a
 * screenshot of a healthy fleet:
 *
 *   - AN OUTAGE KEEPS A FLOOR WIDTH. The one real outage measured on the dev
 *     fleet lasted 52 seconds: 0.06% of a 24h strip, a fifteenth of a pixel.
 *     Without `min-width` on `.dv-seg.dv-down` it is not drawn at all, and the
 *     strip reads "never down" about a router that was.
 *   - SEGMENTS ARE PLACED BY PERCENTAGE OF THE WINDOW, so the left edge of each
 *     is where its time is, at any card width.
 *   - NO DATA IS GREY AND SAYS SO, never an absent strip and never green.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const OUT = path.join(ROOT, 'testdata', '.dvstrip.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'devices-strip.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const S = require(OUT);

const H = 3600_000, FROM = 1_000_000_000_000, TO = FROM + 24 * H;

// ── placement ───────────────────────────────────────────────────────────────
{
  const html = S.stripHtml([
    { from: FROM, to: FROM + 6 * H, state: 'unmonitored' },
    { from: FROM + 6 * H, to: FROM + 12 * H, state: 'up' },
    { from: FROM + 12 * H, to: FROM + 12 * H + 52_000, state: 'down' },
    { from: FROM + 12 * H + 52_000, to: TO, state: 'up' },
  ], FROM, TO, { uptimePct: 99.9 });
  const segs = [...html.matchAll(/class="dv-seg dv-(\w+)" style="left:([\d.]+)%;width:([\d.]+)%"/g)]
    .map((m) => ({ state: m[1], left: +m[2], width: +m[3] }));
  assert.deepStrictEqual(segs.map((s) => s.state), ['unmonitored', 'up', 'down', 'up']);
  assert.strictEqual(segs[0].left, 0);
  assert.strictEqual(segs[1].left, 25, 'a segment six hours in does not start a quarter across');
  assert.strictEqual(segs[2].left, 50);
  // The tiling: every segment starts where the last ended, and the last ends at 100%.
  for (let i = 1; i < segs.length; i++) {
    assert.ok(Math.abs(segs[i].left - (segs[i - 1].left + segs[i - 1].width)) < 0.001,
      'segment ' + i + ' does not start where ' + (i - 1) + ' ended');
  }
  const last = segs[segs.length - 1];
  assert.ok(Math.abs(last.left + last.width - 100) < 0.001, 'the strip does not reach "now"');
  // The tooltip carries the TRUE duration, since the bar is not to scale here.
  assert.match(html, /title="Down [^"]*\(52s\)"/, 'the outage tooltip lost its true duration');
  assert.match(html, /aria-label="99\.9% up over the last 24 hours, 1 outage"/);
  say('ok  segments tile the window by percentage, and an outage says how long it was');
}

// ── the floor width lives in the CSS, on the down segment only ──────────────
{
  const css = fs.readFileSync(path.join(ROOT, 'web', 'public', 'app.css'), 'utf8');
  const rule = (css.match(/\.dv-seg\.dv-down\{[^}]*\}/) || [''])[0];
  assert.match(rule, /min-width:\s*[1-9]/,
    'the down segment has no floor width, so a short outage is drawn narrower than a pixel and vanishes');
  assert.match(rule, /z-index:\s*[1-9]/,
    'the down segment is not layered above its neighbours, so the up segment after it paints over its floor');
  const up = (css.match(/\.dv-seg\.dv-up\{[^}]*\}/) || [''])[0];
  assert.ok(up && !/min-width/.test(up), 'the floor belongs to outages; on up time it overstates');
  say('ok  an outage has a floor width and sits above its neighbours');
}

// ── no data ─────────────────────────────────────────────────────────────────
{
  const html = S.stripHtml([], FROM, TO, { uptimePct: null });
  assert.match(html, /dv-seg dv-unmonitored/, 'an empty strip drew no grey bar');
  assert.ok(!/dv-up/.test(html), 'an empty strip drew something green');
  assert.match(html, /aria-label="Not monitored over the last 24 hours"/);
  assert.strictEqual(S.uptimeLabel(null), 'No data', '0% and "no idea" are different facts');
  assert.strictEqual(S.uptimeLabel(100), '100%');
  assert.strictEqual(S.uptimeLabel(99.5), '99.5%');
  say('ok  no data is grey and says so');
}

// ── durations ───────────────────────────────────────────────────────────────
{
  assert.strictEqual(S.fmtDuration(52_000), '52s');
  assert.strictEqual(S.fmtDuration(7 * 60_000), '7m');
  assert.strictEqual(S.fmtDuration(2 * H + 15 * 60_000), '2h 15m');
  assert.strictEqual(S.fmtDuration(76 * H), '3d 4h');
  say('ok  durations read in their two largest units');
}

// ── escaping ────────────────────────────────────────────────────────────────
{
  const html = S.stripHtml([{ from: FROM, to: TO, state: 'up' }], FROM, TO,
    { uptimePct: 100, windowWord: '<img src=x>' });
  assert.ok(!/<img/.test(html), 'the window word reached the markup unescaped');
  say('ok  text reaching an attribute is escaped');
}

fs.rmSync(OUT, { force: true });
say('devices-strip: all checks passed');
