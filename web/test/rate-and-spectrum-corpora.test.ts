/**
 * TWO FROZEN CORPORA THAT HAD OUTLIVED THEIR READERS.
 *
 * ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
 *
 * `testdata/bandwidth-rate-cases.json` and `testdata/fa-spectrum-cases.json`
 * were recorded off the Node app and replayed by the port-parity harness. That
 * harness was retired on 2026-09-01 and 25 MB of recordings went with it; these
 * two survived the sweep and then sat unread, because the generators that made
 * them went in the same sweep. Nothing in the tree named either file.
 *
 * Those generators were tools/bandwidth-rate-cases.js and
 * tools/fa-spectrum-cases.js, named here WITHOUT backticks on purpose: the
 * citation check treats a quoted path as a promise that the file exists, and
 * both of these are gone.
 *
 * The obvious move was to delete them with the rest. The reason not to is that
 * the FUNCTIONS they record are still here, still shipping, and had NO test at
 * all - not a weaker one, none:
 *
 *	splitRate             web/src/pages/bandwidth.ts
 *	spectrumTooltipLines  web/src/pages/wireless-fa.ts
 *	spectrumBandGeometry  web/src/pages/wireless-fa.ts
 *
 * So these are not recordings of something that no longer exists. They are
 * forty cases of behaviour this app still implements and nothing checked, and
 * deleting them would have thrown away the only written-down statement of what
 * these three functions do.
 *
 * ── WHAT THIS CLAIMS, AND WHAT IT DOES NOT ──────────────────────────────────
 *
 * It is NOT a differential test, and there is nothing left to differ from: the
 * implementation these were lifted from is gone. It is an ordinary table-driven
 * unit test whose table happens to have been generated rather than typed. That
 * is the honest description, and it is why the corpora keep their
 * generated-from field rather than having it stripped to look hand-written.
 *
 * Under CLAUDE.md's rule the surviving claim is "this app agrees with itself",
 * which is weaker than "matches what shipped" - but the table was taken from
 * what shipped, so a regression away from the old behaviour still fails here.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

/**
 * Neither module touches the DOM at import time - both are declarations only,
 * checked before this test was written - so unlike the other page tests here
 * there is deliberately no shim.
 */
function bundle(rel: string, out: string): string {
  const OUT = path.join(ROOT, 'testdata', out);
  execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
    [path.join(ROOT, 'web', 'src', 'pages', rel), '--bundle', '--format=cjs',
     '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
    { stdio: 'inherit' });
  return OUT;
}
const read = (f: string) =>
  JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', f), 'utf8'));

// ── splitRate ───────────────────────────────────────────────────────────────
{
  const OUT = bundle('bandwidth.ts', '.rate.cjs');
  const { splitRate } = require(OUT);
  const corpus = read('bandwidth-rate-cases.json');

  // A corpus that quietly emptied would pass every assertion below.
  assert.strictEqual(corpus.cases.length, 30,
    'the rate corpus is ' + corpus.cases.length + ' cases, not the recorded 30');

  let sawUndefined = false;
  for (const c of corpus.cases) {
    // `undefined` cannot survive JSON, so the corpus FLAGS it rather than
    // storing it. Collapsing the flag into `null` would drop exactly the case
    // this function is most likely to get wrong.
    const input = c.inputIsUndefined ? undefined : c.input;
    if (c.inputIsUndefined) sawUndefined = true;
    const got = splitRate(input);
    assert.strictEqual(got.num, c.num,
      'splitRate(' + JSON.stringify(input) + ').num = ' + JSON.stringify(got.num) +
      ', corpus recorded ' + JSON.stringify(c.num));
    assert.strictEqual(got.unit, c.unit,
      'splitRate(' + JSON.stringify(input) + ').unit = ' + JSON.stringify(got.unit) +
      ', corpus recorded ' + JSON.stringify(c.unit));
  }
  assert.ok(sawUndefined,
    'no case sets inputIsUndefined any more - the no-reading case is the one ' +
    'that cannot be expressed in JSON, and it has gone missing from the corpus');

  fs.rmSync(OUT, { force: true });
  say('ok  splitRate matches all ' + corpus.cases.length + ' recorded cases');
}

// ── the spectrum tooltip and the band geometry ──────────────────────────────
{
  const OUT = bundle('wireless-fa.ts', '.fa.cjs');
  const { spectrumTooltipLines, spectrumBandGeometry } = require(OUT);
  const corpus = read('fa-spectrum-cases.json');
  assert.ok(corpus.tooltip.length === 5 && corpus.band.length === 5,
    'the spectrum corpus is ' + corpus.tooltip.length + '/' + corpus.band.length +
    ', not the recorded 5/5');

  // The zero case is the point of this half: a load of 0%, a noise floor of
  // 0 dBm and a count of 0 are all MEASUREMENTS, and a truthiness check instead
  // of `!= null` would silently drop most of a quiet channel's tooltip.
  for (const c of corpus.tooltip) {
    const got = spectrumTooltipLines(c.row, c.currentChannelMhz);
    assert.deepStrictEqual(got, c.lines,
      c.why + '\n  got:  ' + JSON.stringify(got) +
      '\n  want: ' + JSON.stringify(c.lines));
  }
  say('ok  spectrumTooltipLines matches all ' + corpus.tooltip.length + ' recorded cases');

  // `pixelFor` is the x scale's own lookup, and the stub has to keep THREE
  // indices apart or the test proves less than it appears to. The recording
  // makes this visible: every no-bar case at `idx: 3` records `x: 30` no matter
  // what `pixel0` and `pixel1` hold - including the case where they are 0 and
  // 400 - so the generator deliberately answered the DRAWN index with a value
  // distinct from the two the spacing is measured from. Collapsing them (an
  // earlier draft of this file returned `pixel0` for anything but 1) makes the
  // test pass while proving nothing about which index gets asked for.
  const POISON = 999999;
  for (const c of corpus.band) {
    const pixelFor = (i: number) => {
      // The drawn column, checked first because `idx` can be 0. When the bar
      // element is laid out its own x wins and this must never be consulted,
      // so answer with a value that cannot be mistaken for a correct one:
      // a fix that reached for the scale anyway would otherwise pass here.
      if (i === c.idx) return c.el ? POISON : c.x;
      if (i === 0) return c.pixel0;
      if (i === 1) return c.pixel1;
      return POISON;
    };
    const got = spectrumBandGeometry(c.el, c.labelCount, pixelFor, c.idx);
    assert.deepStrictEqual(got, { x: c.x, w: c.w },
      c.why + '\n  got:  ' + JSON.stringify(got) +
      '\n  want: ' + JSON.stringify({ x: c.x, w: c.w }));
  }
  fs.rmSync(OUT, { force: true });
  say('ok  spectrumBandGeometry matches all ' + corpus.band.length + ' recorded cases');
}

say('rate-and-spectrum-corpora: all checks passed');
