/**
 * The router selector lists ROUTERS ONLY (docs/DECISIONS.md, #142).
 *
 * The operator: the drop-down "will always only list routers, not switches,
 * since this is a router first application." The server leaves switches out of
 * every router list (TestTheRouterSelectorNeverReceivesASwitch); this pins the
 * browser's last line: whatever list the selector is handed, a SwOS switch is
 * dropped before an option or a row is drawn for it.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-router-selector-only.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'router-dropdown.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
global.document = { getElementById: () => null, querySelector: () => null, querySelectorAll: () => [], addEventListener: () => {} };
global.window = { addEventListener: () => {}, localStorage: { getItem: () => null, setItem: () => {} } };
const m = require(OUT);

let failed = 0;
function check(what, fn) {
  try { fn(); say('  ok   ' + what); }
  catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

const mixed = [
  { id: 'r1', label: 'Edge' },
  { id: 's1', label: 'Core switch', kind: 'swos' },
  { id: 'r2', label: 'Branch', kind: '' },
];

say('router selector: routers only');

check('routersOnly drops every switch and keeps every router, in order', () => {
  assert.deepStrictEqual(m.routersOnly(mixed).map((r) => r.id), ['r1', 'r2']);
});

check('the mobile select gets no option for a switch', () => {
  const html = m.selectOptionsHtml(mixed);
  assert.ok(html.includes('value="r1"') && html.includes('value="r2"'), 'a router option is missing');
  assert.ok(!html.includes('s1') && !html.includes('Core switch'), 'a switch became an option: ' + html);
});

check('main.ts and the dropdown both apply it', () => {
  const main = fs.readFileSync(path.join(ROOT, 'web', 'src', 'main.ts'), 'utf8');
  const dd = fs.readFileSync(path.join(ROOT, 'web', 'src', 'router-dropdown.ts'), 'utf8');
  assert.ok(/return routersOnly\(\(body\.routers/.test(main), 'loadRouters no longer filters the list it returns');
  assert.ok(/const getRouters = \(\): readonly DropdownRouter\[\] => routersOnly\(getRoutersIn\(\)\)/.test(dd),
    'wireRouterDropdown reads its list without routersOnly');
});

if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
say('\nall passed');
