/**
 * THE VPN PAGE IS AN OVERVIEW.
 *
 * It was a WireGuard page wearing the wrong name — tiles counting WireGuard
 * peers only, and a WireGuard peer grid. That moved to /wireguard, and what is
 * left is the one view none of the dedicated pages can give: every VPN
 * technology at once.
 *
 * ── WHAT IS WORTH PINNING ───────────────────────────────────────────────────
 *
 *   the counts      four rows from one payload, and the OpenVPN split is the
 *                   part that can silently go wrong: OpenVPN sessions arrive in
 *                   `ppp` under the service `OVPN`, so counting them twice (once
 *                   as OpenVPN, once as PPP) is a one-character mistake.
 *   the dash        "Configured" is blank for three rows ON PURPOSE. `ppp` and
 *                   `ipsec` are SESSIONS, not configuration, so a zero there
 *                   would say "none configured" when it means "none connected".
 *                   A test that accepted `0` would be pinning the lie.
 *   the gated link  a row pointing at a page the viewer may not open is an
 *                   invitation to a permission error.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.vpnov-entry.ts');
fs.writeFileSync(ENTRY,
  "export { initVpnPage, overviewRows } from '../web/src/pages/vpn.js';\n");
const OUT = path.join(ROOT, 'testdata', '.vpnov.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);

const doc = makeDoc(['vpnOverviewCount', 'vpnOverviewTbody',
  'vpnPppCard', 'vpnPppCount', 'vpnPppTbody',
  'vpnIpsecCard', 'vpnIpsecCount', 'vpnIpsecTbody']);
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };

const handlers = {};
const socket = { on: (ev, fn) => { handlers[ev] = fn; }, emit: () => {} };
let visible = (p) => true;
mod.initVpnPage(socket, (p) => visible(p));
const n = doc.nodes;

let keyN = 0;
const wg = (state) => ({ id: '*1', publicKey: 'k' + (++keyN), type: 'WireGuard', name: 'p',
  state, comment: '', lastHandshake: '5s', keepalive: '', endpoint: '', allowedIp: '10.0.0.2/32',
  interface: 'wg0', rx: 0, tx: 0, rxRate: 0, txRate: 0, disabled: false, responder: false });
const pppSess = (service) => ({ type: 'PPP', name: 'u', service, address: '198.51.100.5',
  callerId: '', uptime: '1h', rx: 0, tx: 0 });
const ipsecPeer = (state) => ({ type: 'IPsec', name: 'p', state, uptime: '', side: '', enc: '', auth: '' });

const payload = (over) => Object.assign({ ts: 1, tunnels: [], ppp: [], ipsec: [], pollMs: 10000 }, over);

let failed = 0;
function check(what, fn) {
  try { fn(); say('  ok   ' + what); } catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

say('vpn: the overview');

check('the four technologies are counted from one payload', () => {
  const rows = mod.overviewRows(payload({
    tunnels: [wg('active'), wg('active'), wg('stale')],
    ppp: [pppSess('OVPN'), pppSess('L2TP'), pppSess('SSTP')],
    ipsec: [ipsecPeer('established'), ipsecPeer('connecting')],
  }));
  const by = Object.fromEntries(rows.map((r) => [r.label, r]));
  assert.strictEqual(by['WireGuard'].configured, 3, 'every configured peer should be counted');
  assert.strictEqual(by['WireGuard'].active, 2, 'only the active peers should count as active');
  assert.strictEqual(by['IPsec'].active, 1, 'only established IPsec peers should count as active');
  assert.strictEqual(by['OpenVPN'].active, 1, 'the OVPN session should be the OpenVPN row');
  assert.strictEqual(by['PPP / L2TP / SSTP'].active, 2,
    'the OpenVPN session was counted in the PPP row as well as its own');
});

check('"Configured" is a dash where the payload cannot say', () => {
  const rows = mod.overviewRows(payload({ ppp: [pppSess('L2TP')], ipsec: [ipsecPeer('established')] }));
  for (const r of rows) {
    if (r.label === 'WireGuard') {
      assert.strictEqual(r.configured, 0, 'WireGuard genuinely knows it has none');
    } else {
      assert.strictEqual(r.configured, null,
        r.label + ' reports a configured count from a SESSION list; a zero there reads as '
        + '"none configured" when it means "none connected"');
    }
  }
  handlers['vpn:update'](payload({ ppp: [pppSess('L2TP')] }));
  assert.ok(/&mdash;/.test(String(n.vpnOverviewTbody.innerHTML)),
    'the unknown counts did not render as a dash');
});

check('a link is rendered only for a page the viewer may open', () => {
  visible = () => true;
  handlers['vpn:update'](payload({ tunnels: [wg('active')] }));
  assert.ok(/data-vpn-page="wireguard"/.test(String(n.vpnOverviewTbody.innerHTML)),
    'no link to the WireGuard page for a viewer who may open it');

  visible = (p) => p !== 'wireguard';
  handlers['vpn:update'](payload({ tunnels: [wg('active')] }));
  const html = String(n.vpnOverviewTbody.innerHTML);
  assert.ok(!/data-vpn-page="wireguard"/.test(html),
    'a viewer who may not open the WireGuard page was still offered the link');
  assert.ok(/WireGuard/.test(html), 'the row itself disappeared; only its link should');
  visible = () => true;
});

check('the badge counts technologies that have something live', () => {
  handlers['vpn:update'](payload({ tunnels: [wg('active')], ppp: [pppSess('L2TP')] }));
  assert.strictEqual(String(n.vpnOverviewCount.textContent), '2',
    'the badge should count the technologies with an active session, not the rows');
  assert.ok(String(n.vpnOverviewCount.className).includes('active-blue'));

  handlers['vpn:update'](payload({ tunnels: [wg('never')] }));
  assert.strictEqual(String(n.vpnOverviewCount.textContent), '0');
  assert.ok(!String(n.vpnOverviewCount.className).includes('active-blue'),
    'a zero count is still marked active-blue');
});

check('the PPP and IPsec tables stay hidden until the router has any', () => {
  handlers['vpn:update'](payload({}));
  assert.strictEqual(n.vpnPppCard.style.display, 'none');
  assert.strictEqual(n.vpnIpsecCard.style.display, 'none');
  handlers['vpn:update'](payload({ ppp: [pppSess('L2TP')], ipsec: [ipsecPeer('established')] }));
  assert.strictEqual(n.vpnPppCard.style.display, '', 'the PPP card stayed hidden with a session');
  assert.strictEqual(n.vpnIpsecCard.style.display, '', 'the IPsec card stayed hidden with a peer');
});

say(failed ? '  ' + failed + ' failed' : 'vpn-overview: all checks passed');
fs.rmSync(OUT, { force: true });
if (failed) process.exit(1);
