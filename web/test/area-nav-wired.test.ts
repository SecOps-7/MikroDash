/**
 * A GENERATED PAGE'S NAV ENTRY MUST BE CLICKABLE (found 2026-09-18).
 *
 * `wireNav` in main.ts binds a click listener to each `.nav-item` that EXISTS
 * when it runs. The generated pages' entries were appended by `initAreaPages`,
 * two hundred lines later, so each one rendered in its group and did nothing
 * when clicked: IP Pools, IP Addresses and Address Lists were reachable only by
 * URL, which is how every earlier check had opened them.
 *
 * WHAT THIS PINS, AND WHY IT IS SOURCE ORDER. `wireNav` is a private step of the
 * boot sequence, and the boot sequence needs a socket, the branding fetch and
 * the whole shell; driving it in the shim would test the shim. The invariant is
 * an ordering one, so it is asserted as one: `mountAreaNav()` is called exactly
 * once, from main.ts, BEFORE `wireNav(socket)`, and `initAreaPages` no longer
 * mounts entries of its own (a second, later mount would be the bug again).
 * The live proof is clicking the entry, done when this was fixed.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const main = fs.readFileSync(path.join(ROOT, 'web', 'src', 'main.ts'), 'utf8');
const area = fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', 'area.ts'), 'utf8');
const say = console.log.bind(console);

const calls = main.match(/^\s*mountAreaNav\(\);/gm) || [];
assert.strictEqual(calls.length, 1, 'main.ts calls mountAreaNav() ' + calls.length + ' times, want exactly once');
const mountAt = main.search(/^\s*mountAreaNav\(\);/m);
const wireAt = main.search(/^\s*wireNav\(socket\);/m);
assert.ok(wireAt > 0, 'wireNav(socket) is not called in main.ts; this check has lost its anchor');
assert.ok(mountAt < wireAt, 'mountAreaNav() runs AFTER wireNav(socket), so generated nav entries get no click handler');
say('ok  generated nav entries are mounted once, before wireNav binds clicks');

// The body of initAreaPages must not mount entries again.
const init = area.slice(area.indexOf('export function initAreaPages'));
const body = init.slice(0, init.indexOf('\n}\n') + 2);
assert.ok(body.length > 40, 'could not find initAreaPages in area.ts; this check has lost its anchor');
assert.ok(!/mountAreaNav\(|mountNav\(/.test(body), 'initAreaPages mounts nav entries again, after wireNav has run');
say('ok  initAreaPages does not mount a second, unwired set');
