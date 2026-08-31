#!/usr/bin/env node
'use strict';
/**
 * Compare the port's `splitRate` against the corpus lifted from the live
 * `_splitRate`. See tools/bandwidth-rate-cases.js for why a three-line function
 * is worth a gate.
 */

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const ENTRY = path.join(ROOT, 'testdata', '.bwrate-entry.ts');
const OUT = path.join(ROOT, 'testdata', '.bwrate-port.cjs');

fs.writeFileSync(ENTRY, "export { splitRate } from '../web/src/pages/bandwidth';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const { splitRate } = require(OUT);
const corpus = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'bandwidth-rate-cases.json'), 'utf8'));

const bad = [];
for (const c of corpus.cases) {
  // `undefined` cannot survive JSON, so it is flagged and rebuilt here. The
  // non-finite numbers come back as the STRINGS "Infinity"/"NaN" for the same
  // reason — and that is not a weakening: `+x` coerces both spellings to the
  // same value, so the case still exercises the branch it was written for.
  const input = c.inputIsUndefined ? undefined : c.input;
  const got = splitRate(input);
  if (got.num !== c.num || got.unit !== c.unit) {
    bad.push(`  ${JSON.stringify(input)}\n    live: ${c.num} ${c.unit}\n    port: ${got.num} ${got.unit}`);
  }
}

if (bad.length) {
  console.error('bandwidth-rate-check: %d of %d cases differ\n%s',
    bad.length, corpus.cases.length, bad.join('\n'));
  process.exit(1);
}
console.log('bandwidth-rate-check: %d cases identical', corpus.cases.length);
