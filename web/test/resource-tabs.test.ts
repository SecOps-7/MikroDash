/**
 * THE TABBED FIREWALL FORM (2026-10-03).
 *
 * The rule dialog showed about fifteen of a rule's sixty properties in one
 * column; it now carries all of them on WinBox's five tabs, with a "not"
 * toggle for RouterOS's leading `!`. The DOM shim does not parse innerHTML, so
 * the pieces that decide what the form shows and sends are pure and tested
 * here: the tab order, the `!` split and join, and the lockout warning's list
 * of matches it could not evaluate.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.rt-entry.ts');
fs.writeFileSync(ENTRY, "export { formTabs, splitNegation, joinNegation, warningText } from '../web/src/resource.js';\n" +
  "export { rateBetween, readingInterval, windowSecsFor } from '../web/src/pages/firewall-stats.js';\n");
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'resource-tabs.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

global.document = { getElementById: () => null, querySelectorAll: () => [], querySelector: () => null, addEventListener: () => {} };
global.window = { addEventListener: () => {} };
const { formTabs, splitNegation, joinNegation, warningText, rateBetween, readingInterval, windowSecsFor } = require(OUT);
const say = console.log.bind(console);

const f = (name, tab, display = false) => ({ name, tab, display });
const fields = [f('chain', 'General'), f('srcAddressList', 'Advanced'), f('limit', 'Extra'),
  f('action', 'Action'), f('bytes', 'Statistics', true), f('packets', 'Statistics', true)];

assert.deepStrictEqual(formTabs(fields, true), ['General', 'Advanced', 'Extra', 'Action', 'Statistics']);
say('ok  an edit shows the five tabs in the order the fields name them');
assert.deepStrictEqual(formTabs(fields, false), ['General', 'Advanced', 'Extra', 'Action']);
say('ok  a new rule has no Statistics tab: there is nothing to show on it');
assert.deepStrictEqual(formTabs([f('name', ''), f('comment', '')], true), []);
say('ok  a resource with no tabs gets no tab strip');

assert.deepStrictEqual(splitNegation('!10.0.0.0/8'), { not: true, value: '10.0.0.0/8' });
assert.deepStrictEqual(splitNegation('10.0.0.0/8'), { not: false, value: '10.0.0.0/8' });
assert.deepStrictEqual(splitNegation(undefined), { not: false, value: '' });
say('ok  a stored `!` becomes the toggle, and the box holds the match');

assert.strictEqual(joinNegation(true, 'udp'), '!udp');
assert.strictEqual(joinNegation(false, 'udp'), 'udp');
assert.strictEqual(joinNegation(true, '!udp'), '!udp', 'a typed ! is not doubled');
assert.strictEqual(joinNegation(true, ''), '', 'an empty box sends nothing, toggle or not');
assert.strictEqual(joinNegation(false, '!ether2'), '!ether2', 'a ! typed with the toggle off is kept');
say('ok  the toggle puts the `!` back as RouterOS spells it');

// The round trip a Duplicate does: read back, render again.
for (const stored of ['!LAN', 'LAN', '']) {
  const s = splitNegation(stored);
  assert.strictEqual(joinNegation(s.not, s.value), stored, 'round trip of ' + JSON.stringify(stored));
}
say('ok  a value survives being drawn and read back');

const plain = warningText('self-lockout', {});
assert.ok(!plain.why.includes('cannot evaluate'), 'the control: no list, no note: ' + plain.why);
const listed = warningText('self-lockout', { unmodelled: ['Src. Address List: <b>mgmt</b>', 'Time: 8h-17h'] });
assert.ok(listed.why.includes('cannot evaluate') && listed.why.includes('Time: 8h-17h'), listed.why);
assert.ok(!listed.why.includes('<b>'), 'a value from the router is escaped: ' + listed.why);
say('ok  the lockout warning names the matches it could not evaluate, escaped');

// The Statistics tab's graph: a rate is two readings over the time between them.
const r = rateBetween({ t: 1000, bytes: 1000, packets: 10 }, { t: 11000, bytes: 126000, packets: 110 });
assert.deepStrictEqual(r, { t: 11000, bps: 100000, pps: 10 });
say('ok  125 kB and 100 packets over 10 s is 100 kbit/s and 10 p/s');
assert.strictEqual(rateBetween({ t: 1000, bytes: 5000, packets: 50 }, { t: 2000, bytes: 10, packets: 1 }), null);
say('ok  a counter that went backwards (reset-counters) starts over rather than going negative');
assert.strictEqual(rateBetween({ t: 1000, bytes: 1, packets: 1 }, { t: 1000, bytes: 2, packets: 2 }), null);
say('ok  two readings at one moment give no rate');

// The graph scrolls as the Dashboard's does, with the right edge one reading
// interval behind now: measured from the readings, not capped at the
// Dashboard's 2.5 s, or a 10 s poll would leave the line short of the edge.
assert.strictEqual(readingInterval([0, 10000], 10000), 10000, 'too few gaps: the poll interval');
assert.strictEqual(readingInterval([], 0), 10000, 'nothing at all: the default poll');
assert.strictEqual(readingInterval([0, 3000, 6000, 9000, 12000], 10000), 3000, 'measured, not the setting');
assert.strictEqual(readingInterval([0, 10000, 20000, 30000, 40000], 3000), 10000, 'uncapped: a 10 s poll is 10 s');
say('ok  the right edge trails now by the measured reading interval');
assert.strictEqual(windowSecsFor(1000), 60);
assert.strictEqual(windowSecsFor(10000), 300);
assert.strictEqual(windowSecsFor(30000), 600);
say('ok  the window holds about thirty readings, one to ten minutes');
