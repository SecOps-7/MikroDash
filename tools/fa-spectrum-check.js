'use strict';
/**
 * The spectrum chart's two pure decisions, port against live.
 *
 * `tools/fa-spectrum-cases.js` lifts them out of `public/app.js` and records
 * what the live code produced. This bundles `web/src/pages/wireless-fa.ts` and
 * drives the port's versions over the same inputs.
 *
 *   node tools/fa-spectrum-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const OUT = path.join(ROOT, 'testdata', '.fa-spectrum-port.cjs');

execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'wireless-fa.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

const cases = JSON.parse(
  fs.readFileSync(path.join(ROOT, 'testdata', 'fa-spectrum-cases.json'), 'utf8'));
const m = require(OUT);

const problems = [];

for (const c of cases.tooltip) {
  const got = m.spectrumTooltipLines(c.row, c.currentChannelMhz);
  if (JSON.stringify(got) !== JSON.stringify(c.lines)) {
    problems.push(`tooltip — ${c.why}\n    port: ${JSON.stringify(got)}\n    live: `
      + JSON.stringify(c.lines));
  }
}

for (const c of cases.band) {
  // The live slice reads `xs.getPixelForValue`, which the corpus drove with a
  // two-point stub; the same stub here, so both sides see one scale.
  const pixelFor = (v) => (v === 0 ? c.pixel0 : (v === 1 ? c.pixel1 : v * 10));
  const got = m.spectrumBandGeometry(c.el, c.labelCount, pixelFor, c.idx);
  if (got.x !== c.x || got.w !== c.w) {
    problems.push(`band — ${c.why}\n    port: x=${got.x} w=${got.w}\n    live: x=${c.x} w=${c.w}`);
  }
}

// The corpus must still discriminate: four distinct widths were what made the
// clamp and the single-column fallback visible.
if (new Set(cases.band.map((b) => b.w)).size < 4) {
  problems.push('the band corpus no longer produces four distinct widths, so the clamp and the '
    + 'single-column fallback cannot be told apart');
}

fs.rmSync(OUT, { force: true });
if (problems.length) {
  console.error('fa-spectrum-check FAILED:');
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log(`fa-spectrum-check: ${cases.tooltip.length} tooltip and ${cases.band.length} band `
  + 'cases agree with the live chart');
