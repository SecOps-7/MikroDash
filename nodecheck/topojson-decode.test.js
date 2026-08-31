'use strict';
/**
 * The port's TopoJSON decode, against the real topojson-client.
 *
 * WHY THIS EXISTS. The live page loads `/vendor/topojson-client.min.js` for one
 * function — `topojson.feature` — and the port reimplements it in about thirty
 * lines instead, which removes a script tag from the page and a dependency from
 * the build. That is only a good trade if the reimplementation is RIGHT, and
 * "right" here means: identical country outlines for the actual atlas this app
 * ships, not for a toy input.
 *
 * The failure modes are quiet ones. A negative arc index means "this arc, run
 * backwards" — that is how TopoJSON stores a shared border once instead of
 * twice — and getting it wrong turns a border inside out. The first point of
 * each arc repeats the last of the previous one, and forgetting to drop it
 * leaves a zero-length segment at every join. Neither throws. Both would draw a
 * map that looks almost right.
 *
 * So this decodes the shipped atlas both ways and compares every path.
 */
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
// THE PORT'S OWN COPY. The atlas is self-hosted in `web/public/vendor` — see
// CONTRIBUTING.md's self-hosted-assets rule — so the thing being decoded never
// depended on the reference. Only the ORACLE did, and that is now recorded.
const ATLAS = path.join(__dirname, '..', 'web', 'public', 'vendor', 'world-atlas',
  'countries-110m.json');
const CLIENT = path.join(LIVE, 'public', 'vendor', 'topojson-client.min.js');
const ROOT = path.join(__dirname, '..');
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'topo-decode.cjs');

test('the port decodes the shipped world atlas exactly as topojson-client does', () => {
  // THE ATLAS IS THE PORT'S OWN. It is self-hosted in `web/public/vendor`, so the
  // thing being decoded never depended on the reference — only the ORACLE did.
  if (!fs.existsSync(ATLAS)) {
    assert.fail('the world atlas is missing from ' + ATLAS);
  }

  // Bundle the port's decode as CommonJS so this test runs the REAL module
  // rather than a copy of it — a copy would only ever test itself.
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'), [
    path.join(ROOT, 'web', 'src', 'pages', 'connections-worldmap.ts'),
    '--bundle', '--format=cjs', '--platform=node', '--log-level=error', '--outfile=' + OUT,
  ], { cwd: ROOT });

  const world = JSON.parse(fs.readFileSync(ATLAS, 'utf8'));
  const { decodeCountries } = require(OUT);
  const mine = decodeCountries(world);

  // THE ORACLE, RECORDED.
  //
  // The port deliberately reimplemented this decode to drop a script tag and a
  // dependency, and this test is the whole of what verifies that reimplementation.
  // The real client lives only in the reference, so its OUTPUT is recorded here
  // and compared against instead — rounded to 1e-6, which is the tolerance the
  // comparison below already uses, so nothing it looks at is lost.
  //
  // With the client present the recording is re-derived and checked, so it cannot
  // drift from the library it claims to speak for.
  const REF_FILE = path.join(__dirname, 'testdata', 'topojson-reference.json');
  let ref;
  if (fs.existsSync(CLIENT)) {
    const topojson = require(CLIENT);
    const fresh = topojson.feature(world, world.objects.countries);
    const rounded = JSON.parse(JSON.stringify(fresh,
      (k, v) => (typeof v === 'number' ? Math.round(v * 1e6) / 1e6 : v)));
    if (process.env.MIKRODASH_TOPO_FREEZE) {
      fs.mkdirSync(path.dirname(REF_FILE), { recursive: true });
      fs.writeFileSync(REF_FILE, JSON.stringify(rounded) + '\n');
    } else if (fs.existsSync(REF_FILE)) {
      assert.deepStrictEqual(rounded, JSON.parse(fs.readFileSync(REF_FILE, 'utf8')),
        'the recorded topojson-client output no longer matches the real client — '
        + 'regenerate with MIKRODASH_TOPO_FREEZE=1');
    }
    ref = rounded;
  } else {
    assert.ok(fs.existsSync(REF_FILE),
      'no recorded topojson-client output at ' + REF_FILE + '. Regenerate with the '
      + 'reference present: MIKRODASH_TOPO_FREEZE=1 node --test nodecheck/topojson-decode.test.js');
    ref = JSON.parse(fs.readFileSync(REF_FILE, 'utf8'));
    assert.ok(ref.features && ref.features.length > 100,
      'the recorded output holds only ' + ((ref.features || []).length) + ' features — '
      + 'the recording is short, and the countries it omits would not be compared');
  }

  assert.ok(mine.length > 100, 'decoded only ' + mine.length + ' countries');
  assert.strictEqual(mine.length, ref.features.filter(hasGeometry).length,
    'the two decodes disagree about how many countries have geometry');

  // Compare the RINGS, not the rendered path: the path builder is shared, so
  // comparing paths would let a shared bug pass. Coordinates are compared to
  // six decimals, which is a tenth of a millimetre on the ground and far below
  // anything a 1000x500 picture can show.
  //
  // Keyed on the atlas's own numeric id, in the ORDER the geometries appear —
  // both decodes walk the same list, so the nth of one is the nth of the other,
  // and matching by index avoids depending on the id mapping the port supplies.
  const refFeatures = ref.features.filter(hasGeometry);

  let compared = 0;
  for (let n = 0; n < mine.length; n++) {
    const c = mine[n];
    const f = refFeatures[n];
    assert.ok(f, c.cc + ': no reference feature at index ' + n);
    const a = flatten(f.geometry);
    const b = c.rings;
    assert.ok(b, c.cc + ': the port produced no rings');
    assert.strictEqual(b.length, a.length, c.cc + ': ring count differs');
    for (let i = 0; i < a.length; i++) {
      assert.strictEqual(b[i].length, a[i].length, c.cc + ' ring ' + i + ': point count differs');
      for (let j = 0; j < a[i].length; j++) {
        assert.ok(Math.abs(a[i][j][0] - b[i][j][0]) < 1e-6 &&
                  Math.abs(a[i][j][1] - b[i][j][1]) < 1e-6,
          c.cc + ' ring ' + i + ' point ' + j + ' differs');
      }
    }
    compared++;
  }
  assert.ok(compared > 100, 'only ' + compared + ' countries were actually compared');
});

function hasGeometry(f) {
  return f.geometry && (f.geometry.type === 'Polygon' || f.geometry.type === 'MultiPolygon');
}

function flatten(geometry) {
  if (geometry.type === 'Polygon') return geometry.coordinates;
  const out = [];
  for (const poly of geometry.coordinates) for (const ring of poly) out.push(ring);
  return out;
}
