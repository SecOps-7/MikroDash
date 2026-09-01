// Moved from `nodecheck/conn-tables.test.js` when the port-parity harness was retired.
//
// KEPT, because it is not a parity check. It compares the port against an
// objectively correct answer -- a real library's output, or a table of values
// that is simply right or wrong -- rather than against how the old app looked.
// Nothing here constrains the app from changing.
//
// Body verbatim apart from imports and paths.
/**
 * The Connections page's reference tables, lifted rather than retyped.
 *
 * `PORT_NAMES`, `CC_NAMES` and `CC_CENTROIDS` are 75 lines of pure data — every
 * country's name and a hand-picked centroid for each. They were copied out of
 * public/app.js by a script, and this is what keeps them copied: a mistyped
 * centroid draws an arc to the wrong country and NOTHING ELSE WOULD EVER FAIL.
 * There is no fixture that can catch it, no payload that changes, and no
 * rendering difference a DOM comparison would see, because both sides would be
 * drawing the same wrong arc from the same wrong number.
 *
 * It also catches the other direction: the live tables gaining a country the
 * port does not have.
 */
import test from 'node:test';
import assert from 'node:assert';
import fs from 'node:fs';
import path from 'node:path';

// The repository root. Inside the bundle `__dirname` is `web/test-out`, so a
// relative walk resolves to the wrong tree; the runner passes it explicitly.
const REPO = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(REPO, '..', 'MikroDash'));
const APP = path.join(LIVE, 'public', 'app.js');
const PORT = path.join(REPO, 'web', 'src', 'pages', 'connections-map.ts');

// ── THE RECORDED LIVE TABLES ────────────────────────────────────────────────
//
// Regenerate with MIKRODASH_CONNTABLES_FREEZE=1 and a reference present.
const REC_FILE = path.join(process.env.MIKRODASH_ROOT || path.join(REPO, '..'), 'web', 'test', 'testdata', 'conn-tables-live.json');
const recorded = fs.existsSync(REC_FILE) ? JSON.parse(fs.readFileSync(REC_FILE, 'utf8')) : {};

function frozen(key, fn) {
  if (fs.existsSync(APP)) {
    const fresh = fn();
    if (process.env.MIKRODASH_CONNTABLES_FREEZE) {
      recorded[key] = fresh;
      fs.mkdirSync(path.dirname(REC_FILE), { recursive: true });
      fs.writeFileSync(REC_FILE, JSON.stringify(recorded, null, 2) + '\n');
      return fresh;
    }
    if (recorded[key] !== undefined) {
      assert.deepStrictEqual(fresh, recorded[key],
        'the recorded live ' + key + ' no longer matches app.js — '
        + 'regenerate with MIKRODASH_CONNTABLES_FREEZE=1');
    }
    return fresh;
  }
  assert.ok(recorded[key] !== undefined,
    'no recorded live ' + key + ' at ' + REC_FILE
    + '. Regenerate with a reference present: MIKRODASH_CONNTABLES_FREEZE=1');
  return recorded[key];
}

/** The object literal assigned to `name`, evaluated. */
function tableFrom(src, pattern) {
  const m = src.match(pattern);
  assert.ok(m, 'no table matched ' + pattern);
  // eslint-disable-next-line no-eval -- both inputs are this project's own source
  return eval('(' + m[1] + ')');
}

for (const name of ['PORT_NAMES', 'CC_NAMES', 'CC_CENTROIDS', 'NUM_TO_ISO2']) {
  test('connections ' + name + ' matches the live table exactly', () => {
    const port = fs.readFileSync(PORT, 'utf8');
    // THE LIVE TABLE, RECORDED. These four are DATA — the port copied them and
    // must keep matching — so the table itself is what has to survive, not the
    // file it was cut from. With the reference present the recording is
    // re-derived and compared, so it cannot drift from what it claims to hold.
    const a = frozen(name, () => {
      const live = fs.readFileSync(APP, 'utf8');
      return tableFrom(live, new RegExp('var ' + name + ' = (\\{[\\s\\S]*?\\});'));
    });
    const b = tableFrom(port, new RegExp('export const ' + name + '[^=]*= (\\{[\\s\\S]*?\\});'));

    const ak = Object.keys(a).sort(), bk = Object.keys(b).sort();
    assert.deepStrictEqual(bk, ak, name + ': the key sets differ');
    for (const k of ak) {
      assert.deepStrictEqual(b[k], a[k], name + '[' + k + '] differs');
    }
    // A sanity floor, so a regex that silently matched an empty object cannot
    // pass this test by comparing nothing to nothing.
    assert.ok(ak.length > 10, name + ' has only ' + ak.length + ' entries — did the match fail?');
  });
}
