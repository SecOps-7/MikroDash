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
fs.writeFileSync(ENTRY, "export { warningText, guardRefusedText } from '../web/src/resource.js';\n");
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'guard-warning-text.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

global.document = { getElementById: () => null, querySelectorAll: () => [], querySelector: () => null, addEventListener: () => {} };
global.window = { addEventListener: () => {} };
const { warningText, guardRefusedText } = require(OUT);
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

const address = warningText('address-cutoff', { address: '10.0.0.5', prefix: '10.0.0.1/24', interface: 'bridge', action: 'delete' });
assert.ok(address.headline.includes('cut MikroDash off'), 'address-cutoff headline: ' + address.headline);
assert.ok(address.why.includes('10.0.0.5') && address.why.includes('10.0.0.1/24') && address.why.includes('bridge') &&
  address.why.includes('removes'), 'address-cutoff names where MikroDash is, the address and the action: ' + address.why);
say('ok  address-cutoff names where MikroDash is and the address it would cut');

const addressUnknown = warningText('address-cutoff-unknown', { prefix: '10.0.0.1/24', action: 'update' });
assert.ok(addressUnknown.why.includes('could not read') && addressUnknown.why.includes('10.0.0.1/24'),
  'address-cutoff-unknown says it cannot tell: ' + addressUnknown.why);
say('ok  address-cutoff-unknown says the guard cannot tell');

// list-cutoff: the default config's `!LAN drop`, reached by taking the port out
// of LAN, and a blocklist drop reached by putting our address into it. The two
// directions and the two effects are independent, so each sentence is checked.
const ifOut = warningText('list-cutoff', { kind: 'interface', value: 'ether2', list: 'LAN', move: 'leaves', effect: 'starts-drop', ruleMatch: '!LAN' });
assert.ok(ifOut.headline.includes('cut MikroDash off') && ifOut.why.includes('takes the interface MikroDash arrives on, <code>ether2</code>') &&
  ifOut.why.includes('out of the list <code>LAN</code>') && ifOut.why.includes('<code>!LAN</code>') && ifOut.why.includes('would then drop'),
  'list-cutoff, interface leaving LAN: ' + ifOut.why);
const addrIn = warningText('list-cutoff', { kind: 'address', value: '198.51.100.5', list: 'blocklist', move: 'joins', effect: 'starts-drop', ruleMatch: 'blocklist' });
// An address-list entry COVERS MikroDash's address; it is not the address itself.
assert.ok(addrIn.why.includes('puts <code>198.51.100.5</code>, which covers the address the router sees MikroDash at,') && addrIn.why.includes('into the list'),
  'list-cutoff, address joining a blocklist: ' + addrIn.why);
const addrOut = warningText('list-cutoff', { kind: 'address', value: '198.51.100.0/24', list: 'mgmt', move: 'leaves', effect: 'loses-accept', ruleMatch: 'mgmt' });
assert.ok(addrOut.why.includes('out of the list') && addrOut.why.includes('stop accepting'),
  'list-cutoff, address leaving an accept list: ' + addrOut.why);
say('ok  list-cutoff says which way MikroDash moved, which list, which rule, and what the rule then does');

const redefine = warningText('list-redefine', { list: 'LAN', change: 'delete', ruleMatch: '!LAN' });
assert.ok(redefine.headline.includes('cut MikroDash off') && redefine.why.includes('deletes the list <code>LAN</code>') &&
  redefine.why.includes('<code>!LAN</code>'), 'list-redefine: ' + redefine.why);
const renamed = warningText('list-redefine', { list: 'LAN', change: 'rename', ruleMatch: '!LAN' });
assert.ok(renamed.why.includes('renames the list'), 'list-redefine, rename: ' + renamed.why);
say('ok  list-redefine names the list, what the change does to it, and the rule that matches it');

for (const [code, words] of [['self-lockout', 'firewall rule'], ['wifi-inherit', 'inherits'], ['capsman-push', 'pushed']]) {
  const t = warningText(code, {});
  assert.ok(t.why.includes(words), code + ' has its own sentence, not the fallback: ' + t.why);
}
say('ok  the formerly unacknowledgeable warnings each explain themselves');

const other = warningText('<b>odd</b>', {});
assert.ok(other.why.includes('&lt;b&gt;') && !other.why.includes('<b>'), 'an unknown code is escaped: ' + other.why);
say('ok  an unknown code falls back, escaped');

// THE IP-SERVICE REFUSALS each say what the change would do, not the fallback.
for (const [code, words] of [['service-disable', 'Disabling it'], ['service-port', 'port'], ['service-vrf', 'VRF'],
  ['service-address', 'would not admit'], ['service-address-unknown', 'cannot read'],
  ['certificate-in-use', 'Removing it'], ['certificate-unknown', 'cannot read which certificate']]) {
  const t = guardRefusedText(code);
  assert.ok(t.includes(words) && !t.includes('A safety rule refused'), code + ': ' + t);
}
say('ok  each IP-service refusal explains itself');
