/**
 * WHAT A GUARD WARNING SAYS, BY CODE (#97).
 *
 * The resource modal's acknowledgement prompt opened only for `self-cutoff`, so a
 * firewall `self-lockout`, a Wi-Fi `wifi-inherit` or a CAPsMAN `capsman-push`
 * warning showed as a plain refusal nobody could acknowledge. It now opens for
 * any guard warning, and `warningText` is what it says. The route guard added two
 * codes of its own.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.gw-entry.ts');
fs.writeFileSync(ENTRY, "export { warningText } from '../web/src/resource.js';\n");
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'guard-warning-text.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

global.document = { getElementById: () => null, querySelectorAll: () => [], querySelector: () => null, addEventListener: () => {} };
global.window = { addEventListener: () => {} };
const { warningText } = require(OUT);
const say = console.log.bind(console);

const route = warningText('route-cutoff', { address: '203.0.113.50', destination: '0.0.0.0/0', action: 'delete' });
assert.ok(route.headline.includes('cut MikroDash off'), 'route-cutoff headline: ' + route.headline);
assert.ok(route.why.includes('203.0.113.50') && route.why.includes('0.0.0.0/0') && route.why.includes('removes'),
  'route-cutoff names the address, the route and the action: ' + route.why);
say('ok  route-cutoff names the address and the route it would cut');

const unknown = warningText('route-cutoff-unknown', { destination: '0.0.0.0/0', action: 'update' });
assert.ok(unknown.why.includes('could not read') && unknown.why.includes('0.0.0.0/0'),
  'route-cutoff-unknown says it cannot tell: ' + unknown.why);
say('ok  route-cutoff-unknown says the guard cannot tell');

for (const [code, words] of [['self-lockout', 'firewall rule'], ['wifi-inherit', 'inherits'], ['capsman-push', 'pushed']]) {
  const t = warningText(code, {});
  assert.ok(t.why.includes(words), code + ' has its own sentence, not the fallback: ' + t.why);
}
say('ok  the formerly unacknowledgeable warnings each explain themselves');

const other = warningText('<b>odd</b>', {});
assert.ok(other.why.includes('&lt;b&gt;') && !other.why.includes('<b>'), 'an unknown code is escaped: ' + other.why);
say('ok  an unknown code falls back, escaped');
