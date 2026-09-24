/**
 * DUPLICATE MUST NOT SWITCH A TOGGLE ON.
 *
 * ── WHERE THE TWO SHAPES MEET ───────────────────────────────────────────────
 *
 * A form is filled from two different sources and they disagree about what a
 * checkbox is:
 *
 *	res:row     `resource.RowValues` sends a real Go bool
 *	Duplicate   `readValues` sends `String(node.checked)` - the STRING 'false'
 *
 * `fieldHtml` wrote `value ? ' checked' : ''`, and every non-empty string is
 * truthy, so the first render of a row was right and the copy of it came back
 * with every OFF toggle ON. Duplicating a working firewall rule produced a
 * DISABLED one; a DNS entry came back with Match Subdomains set. Both fields are
 * `Clearable`, so the wrong value reached the router the moment Add was pressed.
 *
 * The four shapes below are the ones that actually arrive; the last two are what
 * RouterOS itself spells a truth as, which `RowValues` already normalises but
 * which a hand-built value could still carry.
 */
import assert from 'node:assert';
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.rdup-entry.ts');
fs.writeFileSync(ENTRY, "export { checkedValue } from '../web/src/resource.js';\n");
const OUT = path.join(ROOT, 'testdata', '.rdup.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const { checkedValue } = require(OUT);

const on: unknown[] = [true, 'true', 'yes'];
const off: unknown[] = [false, 'false', 'no', '', undefined, null, 0];

on.forEach((v) => {
  assert.strictEqual(checkedValue(v), true,
    JSON.stringify(v) + ' should read as a switched-ON toggle');
});
say('ok  a real bool, "true" and "yes" all read as on');

off.forEach((v) => {
  assert.strictEqual(checkedValue(v), false,
    JSON.stringify(v) + ' switched the toggle on; Duplicate would carry it to the router');
});
say('ok  "false" reads as off, and so does everything else that is not a truth');

fs.rmSync(OUT, { force: true });
say('resource-duplicate: all checks passed');
