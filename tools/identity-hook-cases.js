'use strict';
/**
 * WHEN the system collector reports a router's identity — the `_lastIdentityKey`
 * dedupe, lifted out of the live `src/collectors/system.js`.
 *
 * ---- WHY THIS IS A SEPARATE CORPUS FROM identity-fields-cases -------------
 *
 * They answer different questions and both were wrong in the port:
 *
 *   identity-fields  what `Routers.updateIdentity` DOES with a reported triple
 *   this one         WHEN the collector reports one at all
 *
 * The port's pool called its identity hook ONCE per connection, off
 * `system.Last()` — which is nil at that moment, so the hook was wired and never
 * fired; and even had it fired, an OS upgrade could never be reported, because
 * there was no second call. The live comment is explicit that the version "must
 * not be write-once". Neither defect is visible to a corpus about the writer.
 *
 * ---- IT IS A SEQUENCE, NOT A SET -----------------------------------------
 *
 * The rule is STATEFUL: `identityKey !== this._lastIdentityKey`. A corpus of
 * independent inputs would pass an implementation that fired every single tick,
 * which is the exact failure that would rewrite routers.json and broadcast to
 * every browser several times a minute. So each case is a RUN of ticks and the
 * recorded output is which ticks fired.
 *
 * ---- THE FIRST TWO TICKS ARE THE INTERESTING ONES -------------------------
 *
 * The serial comes from a STATIC read that happens from the second tick on, so
 * tick 1 carries `serial: null` and tick 2 carries the real one. `[a, null, c]
 * .join(' ')` is `"a  c"` in JavaScript — an empty field, not the word "null" —
 * so the two keys differ and the hook fires TWICE on every fresh connection.
 * That is not waste: it is how the serial gets persisted at all.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/identity-hook-cases.js
 *   MIKRODASH_SRC=../MikroDash node tools/identity-hook-cases.js --check
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', 'identity-hook-cases.json');

// ---- Lift the two lines that decide, by CONTENT anchor ---------------------
const src = fs.readFileSync(path.join(LIVE, 'src', 'collectors', 'system.js'), 'utf8');
const lines = src.split('\n');

const baseAt = lines.findIndex((l) => /const\s+installedBase\s*=\s*installed\./.test(l));
if (baseAt < 0) throw new Error('anchor lost: const installedBase = installed.');
const keyAt = lines.findIndex((l) => /const\s+identityKey\s*=/.test(l));
if (keyAt < 0) throw new Error('anchor lost: const identityKey =');

const baseLine = lines[baseAt].trim();
const decide = lines.slice(keyAt, keyAt + 5).join('\n');
if (!decide.includes('_lastIdentityKey') || !decide.includes('_onIdentity')) {
  throw new Error('the slice lost the dedupe or the hook call — the anchors drifted');
}
if (!/\.join\(' '\)/.test(decide)) {
  throw new Error('the join character is no longer a single space; the key shape changed');
}
// THE SLICE MUST BE BALANCED. Taking one line too few left the `if` unclosed and
// the `vm` compile failed loudly — which is the good case. A slice that happened
// to balance while missing the assignment would compile and quietly test
// nothing, so the closing brace is asserted rather than counted on.
if (decide.trim().slice(-1) !== '}') {
  throw new Error('the slice does not end at the closing brace; the block moved');
}

// A driver that runs ONE tick against a `this` the caller owns. Nothing about
// the rule is retyped — `baseLine` and `decide` are the file's own text.
const tick = vm.runInNewContext(
  `(function (payload, installed) {\n  ${baseLine}\n${decide}\n  return installedBase;\n})`,
  Object.create(null), { filename: 'system.js#identity' });

function replay(ticks) {
  const self = { _lastIdentityKey: undefined, fired: [] };
  self._onIdentity = (id) => self.fired.push(id);
  const out = [];
  for (const t of ticks) {
    const payload = { boardName: t.boardName, serial: t.serial === undefined ? null : t.serial };
    const before = self.fired.length;
    const base = tick.call(self, payload, t.version);
    out.push({
      version: t.version,
      boardName: t.boardName,
      serial: t.serial === undefined ? null : t.serial,
      installedBase: base,
      fired: self.fired.length > before,
      reported: self.fired.length > before ? self.fired[self.fired.length - 1] : null,
    });
  }
  return out;
}

const BOARD = 'C53UiG5HPaxD2HPaxD';
const SER = 'HDX0ABCDEF1';

const INPUTS = [
  ['a fresh connection: no serial, then the serial arrives', [
    { boardName: BOARD, serial: null, version: '7.23.1 (stable)' },
    { boardName: BOARD, serial: SER, version: '7.23.1 (stable)' },
    { boardName: BOARD, serial: SER, version: '7.23.1 (stable)' },
    { boardName: BOARD, serial: SER, version: '7.23.1 (stable)' },
  ]],
  ['a steady router never reports twice', [
    { boardName: BOARD, serial: SER, version: '7.23.1 (stable)' },
    { boardName: BOARD, serial: SER, version: '7.23.1 (stable)' },
    { boardName: BOARD, serial: SER, version: '7.23.1 (stable)' },
  ]],
  // THE ONE THE ONE-SHOT HOOK COULD NOT DO.
  ['an OS upgrade is reported', [
    { boardName: BOARD, serial: SER, version: '7.23.1 (stable)' },
    { boardName: BOARD, serial: SER, version: '7.23.1 (stable)' },
    { boardName: BOARD, serial: SER, version: '7.24 (stable)' },
    { boardName: BOARD, serial: SER, version: '7.24 (stable)' },
  ]],
  // THE CHANNEL IS DROPPED, so a stable→testing switch at the SAME release is
  // not a change and must not churn a write and a broadcast.
  ['switching channel at the same release reports nothing', [
    { boardName: BOARD, serial: SER, version: '7.24 (stable)' },
    { boardName: BOARD, serial: SER, version: '7.24 (testing)' },
    { boardName: BOARD, serial: SER, version: '7.24 (long-term)' },
  ]],
  ['a bare version with no channel at all', [
    { boardName: BOARD, serial: SER, version: '7.24' },
    { boardName: BOARD, serial: SER, version: '7.24 (stable)' },
  ]],
  ['an empty version', [
    { boardName: BOARD, serial: SER, version: '' },
    { boardName: BOARD, serial: SER, version: '7.24' },
  ]],
  // A CHR has no board name and answers on `platform`; the collector has already
  // folded that in, so what arrives here can still be empty.
  ['an empty board name, then one arrives', [
    { boardName: '', serial: null, version: '7.24 (stable)' },
    { boardName: 'CHR', serial: null, version: '7.24 (stable)' },
  ]],
  ['the serial disappears again', [
    { boardName: BOARD, serial: SER, version: '7.24' },
    { boardName: BOARD, serial: null, version: '7.24' },
    { boardName: BOARD, serial: SER, version: '7.24' },
  ]],
  // THE SPACE IS THE SEPARATOR, so a value containing one could in principle
  // collide with a different triple. Recorded rather than asserted away: this is
  // what the live key does, and a port using a different separator would
  // disagree here and nowhere else.
  ['a board name containing a space', [
    { boardName: 'RB 5009', serial: 'UG', version: '7.24' },
    { boardName: 'RB', serial: '5009 UG', version: '7.24' },
  ]],
  ['whitespace around the version is trimmed', [
    { boardName: BOARD, serial: SER, version: '  7.24  ' },
    { boardName: BOARD, serial: SER, version: '7.24' },
  ]],
];

const cases = INPUTS.map(([why, ticks]) => ({ why, ticks: replay(ticks) }));

// ---- Believability ---------------------------------------------------------
const by = Object.fromEntries(cases.map((c) => [c.why, c]));
const need = (k) => {
  if (!by[k]) throw new Error(`a believability check names a case that is gone: ${k}`);
  return by[k];
};
const fires = (k) => need(k).ticks.map((t) => t.fired);
const eq = (a, b) => JSON.stringify(a) === JSON.stringify(b);

// THE FRESH CONNECTION fires exactly twice, at the front.
if (!eq(fires('a fresh connection: no serial, then the serial arrives'),
  [true, true, false, false])) {
  throw new Error(`the fresh-connection shape is not fire,fire,quiet,quiet: `
    + JSON.stringify(fires('a fresh connection: no serial, then the serial arrives')));
}
// ...and the FIRST report carries no serial, which is what makes the writer's
// "an empty field is skipped, not cleared" rule load-bearing.
{
  const first = need('a fresh connection: no serial, then the serial arrives').ticks[0].reported;
  if (first.serial !== null) throw new Error('the first report already carries a serial');
  const second = need('a fresh connection: no serial, then the serial arrives').ticks[1].reported;
  if (second.serial !== SER) throw new Error('the second report did not add the serial');
}

// THE DEDUPE. Without this case a port that fired every tick would pass.
if (!eq(fires('a steady router never reports twice'), [true, false, false])) {
  throw new Error('a steady router reported more than once');
}

// THE UPGRADE. Without this one, a write-once port would pass.
if (!eq(fires('an OS upgrade is reported'), [true, false, true, false])) {
  throw new Error('an OS upgrade was not reported');
}

// THE CHANNEL IS NOT PART OF THE KEY.
if (!eq(fires('switching channel at the same release reports nothing'), [true, false, false])) {
  throw new Error('a channel switch churned a report');
}
// ...and the stored value is the BARE version, or the channel was merely hidden.
if (need('switching channel at the same release reports nothing').ticks[0].reported.osVersion !== '7.24') {
  throw new Error('the reported osVersion still carries its channel');
}

// TRIMMING is real.
if (!eq(fires('whitespace around the version is trimmed'), [true, false])) {
  throw new Error('a padded version was treated as a different release');
}

// EVERY CASE must fire at least once, or it is a corpus of silence.
for (const c of cases) {
  if (!c.ticks.some((t) => t.fired)) throw new Error(`${c.why}: never fires`);
}
// ...and at least one must NOT fire on its first tick, or nothing here tests the
// dedupe's memory across a run.
if (!cases.some((c) => c.ticks.slice(1).some((t) => !t.fired))) {
  throw new Error('no case is quiet after its first tick');
}

const json = JSON.stringify(
  { generated_from: 'src/collectors/system.js identityKey dedupe', cases }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('STALE: testdata/identity-hook-cases.json - re-run tools/identity-hook-cases.js');
    process.exit(1);
  }
  console.log(`identity-hook-cases: up to date (${cases.length} runs)`);
} else {
  fs.writeFileSync(OUT, json);
  const ticks = cases.reduce((n, c) => n + c.ticks.length, 0);
  console.log(`wrote ${OUT} (${cases.length} runs, ${ticks} ticks, `
    + `${cases.reduce((n, c) => n + c.ticks.filter((t) => t.fired).length, 0)} firing)`);
}
