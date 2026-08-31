'use strict';
/**
 * WHICH COLLECTORS A SETTINGS SAVE RE-TUNES, and to what.
 *
 * `POST /api/settings` does not just write the file: it applies the new poll
 * intervals to the RUNNING collectors, so a slider takes effect without a
 * restart. Two rules decide that, and both are easy to port wrongly because both
 * are about what NOT to do.
 *
 * ---- 1. A PER-ROUTER OVERRIDE OUTRANKS THE FLEET DEFAULT -------------------
 *
 * The live comment (#105): "Without this the global save would silently un-pin
 * whichever router the pool is currently serving, and the modal would then
 * disagree with reality." So a key the operator pinned for this router is
 * SAVED to the file and NOT applied to the running collector — the two halves
 * disagree on purpose, and a port that applied everything would quietly discard
 * a per-router setting that the modal still shows.
 *
 * `_pinned` is `overrides[key] !== undefined`, so an override of ZERO or of
 * `false` still pins. A port testing truthiness un-pins exactly the values an
 * operator would set to mean "off".
 *
 * ---- 2. ONLY KEYS PRESENT IN THE UPDATES ----------------------------------
 *
 * `key in updates`, not `key in saved`. `saved` is the whole merged file, so
 * every poll key is in it and a port reading that would re-tune all twenty-three
 * collectors on every save — restarting streams nobody touched.
 *
 * ---- 3. AND THE VALUE IS RE-CLAMPED ON THE WAY OUT ------------------------
 *
 * `Math.max(500, Math.min(600000, _p))`, which is NOT the same range the
 * validator enforced: `pollRouting` accepts 500..300000 and `pollWifi`
 * 10000..600000, so this second clamp is wider than some fields and narrower
 * than none. It exists because `saved[key]` may predate the current bounds.
 *
 * A value that is not finite falls back to the collector's CURRENT interval,
 * which is why this function reports "keep" rather than a number.
 *
 * The TABLE is lifted from the route rather than retyped, because it is exactly
 * the thing that has been wrong before: the live comment records that
 * `pollTopology`, `pollVlans` and `pollPpp` were missing from it, so "the
 * sliders existed and the bounds existed, but with no entry here the value was
 * dropped on save and never reached the collector".
 *
 * Runs on the host: `src/index.js` is read, never loaded.
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const OUT = process.env.SETTINGS_APPLY_OUT
  || path.join(ROOT, 'testdata', 'settings-apply-cases.json');

const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const indexSrc = fs.readFileSync(path.join(SRC, 'src', 'index.js'), 'utf8');

// ---- THE TABLE, LIFTED ---------------------------------------------------
function liftObject(src, decl) {
  const n = src.split(decl).length - 1;
  assert.equal(n, 1, 'AMBIGUOUS anchor (' + n + '): ' + decl);
  const i = src.indexOf(decl);
  const open = src.indexOf('{', i);
  let depth = 0;
  for (let j = open; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}') { depth--; if (!depth) return src.slice(open, j + 1); }
  }
  throw new Error('unbalanced object for ' + decl);
}

// eslint-disable-next-line no-new-func
const pollMap = new Function('return ' + liftObject(indexSrc, 'const pollMap = '))();
const collectorNames = Object.values(pollMap);

// The three the live comment says were MISSING once. If the lift ever loses
// them again this gate says so rather than the port quietly matching a hole.
for (const k of ['pollTopology', 'pollVlans', 'pollPpp', 'pollWifi', 'pollWan']) {
  assert.ok(k in pollMap, 'the lifted pollMap has no ' + k + ' — that exact omission is '
    + 'recorded in the live source as a defect that dropped the value on save');
}
assert.ok(Object.keys(pollMap).length >= 20,
  'the lifted pollMap has only ' + Object.keys(pollMap).length + ' entries');

// ---- THE RULE, TRANSCRIBED FROM THE LOOP IT LIVES IN ----------------------
//
// The loop is inline in the route and interleaved with collector mechanics
// (`_restartTimer`, `setInterval`) that have no meaning outside a running Node
// process, so it cannot be evaluated the way the validator was. What CAN be
// lifted is the decision, and the two expressions that make it are quoted below
// from the source and asserted to still be there.
for (const frag of [
  'if (key in updates && !_pinned(key))',
  "const _pinned = (key) => _ovr[key] !== undefined;",
  'Math.max(500, Math.min(600000, _p))',
  'Number.isFinite(Number(saved[key])) ? Math.trunc(Number(saved[key])) : col.pollMs',
]) {
  assert.ok(indexSrc.includes(frag),
    'the route no longer contains `' + frag + '` — the rule below is transcribed from it '
    + 'and has drifted');
}

/** What the route does, for one save. `null` means "keep the current interval". */
function retunes(updates, saved, overrides) {
  const out = {};
  const pinned = (key) => overrides[key] !== undefined;
  for (const [key, name] of Object.entries(pollMap)) {
    if (!(key in updates) || pinned(key)) continue;
    const n = Number(saved[key]);
    out[name] = Number.isFinite(n) ? Math.max(500, Math.min(600000, Math.trunc(n))) : null;
  }
  return out;
}

// ---- THE CASES -----------------------------------------------------------
const CASES = {
  nothing: { updates: {}, saved: { pollSystem: 5000 }, overrides: {} },

  onePlain: { updates: { pollSystem: 5000 }, saved: { pollSystem: 5000 }, overrides: {} },

  // `key in updates`, NOT `key in saved`. Every poll key is in the merged file.
  savedHasEverything: {
    updates: { pollSystem: 5000 },
    saved: { pollSystem: 5000, pollConns: 2000, pollWifi: 30000, pollDns: 4000 },
    overrides: {},
  },

  // PINNED: saved to the file, NOT applied.
  pinned: { updates: { pollSystem: 5000 }, saved: { pollSystem: 5000 },
    overrides: { pollSystem: 9000 } },
  // A pin of ZERO still pins — `!== undefined`, not truthiness.
  pinnedZero: { updates: { pollSystem: 5000 }, saved: { pollSystem: 5000 },
    overrides: { pollSystem: 0 } },
  pinnedFalse: { updates: { pollSystem: 5000 }, saved: { pollSystem: 5000 },
    overrides: { pollSystem: false } },
  pinnedEmptyString: { updates: { pollSystem: 5000 }, saved: { pollSystem: 5000 },
    overrides: { pollSystem: '' } },
  // An override for a DIFFERENT key does not pin this one.
  pinnedOther: { updates: { pollSystem: 5000 }, saved: { pollSystem: 5000 },
    overrides: { pollConns: 3000 } },
  // A pin on one of two.
  pinnedOneOfTwo: {
    updates: { pollSystem: 5000, pollConns: 2000 },
    saved: { pollSystem: 5000, pollConns: 2000 },
    overrides: { pollConns: 7000 },
  },

  // THE SECOND CLAMP, which is not the validator's range.
  clampLow: { updates: { pollRouting: 1 }, saved: { pollRouting: 1 }, overrides: {} },
  clampHigh: { updates: { pollWifi: 900000 }, saved: { pollWifi: 900000 }, overrides: {} },
  clampAtFloor: { updates: { pollRouting: 500 }, saved: { pollRouting: 500 }, overrides: {} },
  clampAtCeiling: { updates: { pollWifi: 600000 }, saved: { pollWifi: 600000 }, overrides: {} },
  // TRUNCATED toward zero, not rounded.
  fractional: { updates: { pollSystem: 5000 }, saved: { pollSystem: 5999.9 }, overrides: {} },
  // A numeric STRING is finite once coerced.
  numericString: { updates: { pollSystem: 5000 }, saved: { pollSystem: '4000' }, overrides: {} },
  // Not finite: keep whatever the collector has.
  notFinite: { updates: { pollSystem: 5000 }, saved: { pollSystem: 'soon' }, overrides: {} },
  savedMissing: { updates: { pollSystem: 5000 }, saved: {}, overrides: {} },
  savedNull: { updates: { pollSystem: 5000 }, saved: { pollSystem: null }, overrides: {} },

  // A non-poll key in the updates re-tunes nothing.
  nonPollKey: { updates: { topN: 25 }, saved: { topN: 25 }, overrides: {} },

  // Everything at once.
  all: {
    updates: Object.fromEntries(Object.keys(pollMap).map((k) => [k, 3000])),
    saved: Object.fromEntries(Object.keys(pollMap).map((k) => [k, 3000])),
    overrides: {},
  },
};

const cases = {};
for (const [name, c] of Object.entries(CASES)) {
  cases[name] = { ...c, retunes: retunes(c.updates, c.saved, c.overrides) };
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  const r = (k) => cases[k].retunes;
  assert.deepEqual(r('nothing'), {}, 'an empty update re-tuned something');
  assert.deepEqual(r('onePlain'), { system: 5000 },
    'a plain save re-tuned nothing, so every refusal below is indistinguishable');

  assert.deepEqual(r('savedHasEverything'), { system: 5000 },
    'the merged file drove the re-tune — every collector would restart on every save');

  for (const k of ['pinned', 'pinnedZero', 'pinnedFalse', 'pinnedEmptyString']) {
    assert.deepEqual(r(k), {}, k + ': a pinned key was applied anyway, silently un-pinning '
      + 'the router the pool is serving while the modal still shows the override');
  }
  assert.deepEqual(r('pinnedOther'), { system: 5000 },
    'an override for a different key pinned this one');
  assert.deepEqual(r('pinnedOneOfTwo'), { system: 5000 },
    'the pin took out the wrong key, or both');

  assert.equal(r('clampLow').routing, 500, 'the floor of 500 was not applied');
  assert.equal(r('clampHigh').wifi, 600000, 'the ceiling of 600000 was not applied');
  assert.equal(r('clampAtFloor').routing, 500);
  assert.equal(r('clampAtCeiling').wifi, 600000);
  assert.equal(r('fractional').system, 5999, 'the value was rounded rather than truncated');
  assert.equal(r('numericString').system, 4000, 'a numeric string was not coerced');
  assert.equal(r('notFinite').system, null, 'an unparseable value produced a number');
  assert.equal(r('savedMissing').system, null);
  // `Number(null)` is 0, and 0 is FINITE — so this takes the number path, not
  // the keep-current path, and the clamp then raises it to the floor. The
  // distinction that matters is 500 versus null: a port treating null as absent
  // would keep the collector's existing interval, where the live app sets it to
  // the fastest one allowed.
  assert.equal(r('savedNull').system, 500,
    '`Number(null)` is 0 and 0 is finite, so this clamps to the floor rather than '
    + 'keeping the current interval');
  assert.notEqual(r('savedNull').system, r('notFinite').system,
    'null and an unparseable string produced the same answer, so nothing here '
    + 'distinguishes the finite path from the keep-current one');
  assert.deepEqual(r('nonPollKey'), {}, 'a non-poll key re-tuned a collector');
  assert.equal(Object.keys(r('all')).length, new Set(collectorNames).size,
    'the all-keys case re-tuned ' + Object.keys(r('all')).length + ' collectors, want '
    + new Set(collectorNames).size);
}

const json = JSON.stringify({ pollMap, cases }, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('settings-apply-cases.json is STALE — re-run without --check');
    process.exit(1);
  }
  console.log('settings-apply-cases.json is current (' + Object.keys(cases).length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, json);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' cases, '
    + Object.keys(pollMap).length + ' poll keys)');
}
