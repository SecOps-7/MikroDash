#!/usr/bin/env node
'use strict';
/**
 * Pin `src/routeros/wanGuard.js` — the LAST unported guard — against the live
 * module. Both functions, driven directly: it is pure, so there is nothing to
 * fake and no reason to reconstruct anything.
 *
 * ── WHAT THE CORPUS HAS TO SEPARATE ─────────────────────────────────────────
 *
 * This guard WARNS and never refuses, and it FAILS OPEN. Both of those make its
 * failure mode quiet: a port that always returned `none` would pass any corpus
 * built only from cases where nothing should warn, and would silently remove the
 * one warning that stops an admin cutting their own management path. So every
 * `none` here is paired with the case that differs from it by one field.
 *
 * The three `none` routes are genuinely different facts and are kept apart:
 *   - unresolved  → we could not tell (a read-only API user cannot see
 *                   /user/active). Fail open.
 *   - local       → our session is on a directly attached subnet, so no lease
 *                   action can strand us.
 *   - other WAN   → we are remote, but this is not the uplink carrying us.
 *
 * ── THE TWO THAT ARE EASIEST TO PORT WRONGLY ────────────────────────────────
 *
 * `resolveManagementPath` reports the FIRST OFF-SUBNET address when remote, and
 * `addrs[0]` when local — two different selections from the same array, and the
 * fingerprint carries whichever it picked. A port returning `addrs[0]` in both
 * cases agrees on every single-address input and diverges only when several
 * sessions disagree about their path, which is exactly the case the live header
 * says it errs toward warning about.
 *
 * `certain` is FALSE when we warn because the active default route could not be
 * identified, and TRUE when this WAN demonstrably is it. Same `level: 'warn'`
 * either way, so a port that hard-coded `true` differs in one boolean the UI
 * uses to word the prompt.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/wanguard-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');

const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'wanguard-cases.json');
const CHECK = process.argv.includes('--check');

const wanGuard = require(path.join(SRC, 'src', 'routeros', 'wanGuard.js'));

// A router with a LAN, a management VLAN and an IPv6 prefix. The addresses are
// TEST-NET-3 and the documentation IPv6 range, matching the fixture anonymiser.
const CONNECTED = ['203.0.113.0/24', '198.51.100.0/25', '2001:db8:1::/64'];

const PATHS = [
  { name: 'no session addresses at all — /user/active denied, fail open',
    selfAddresses: { addresses: [] }, connectedCidrs: CONNECTED },
  { name: 'selfAddresses missing entirely',
    selfAddresses: null, connectedCidrs: CONNECTED },
  { name: 'addresses known but NO connected subnets — knows nothing, must not warn',
    selfAddresses: { addresses: ['203.0.113.9'] }, connectedCidrs: [] },
  { name: 'connected subnets present but every entry falsy',
    selfAddresses: { addresses: ['203.0.113.9'] }, connectedCidrs: ['', null, undefined] },
  { name: 'single address, on a connected subnet — local',
    selfAddresses: { addresses: ['203.0.113.9'] }, connectedCidrs: CONNECTED },
  { name: 'single address, off subnet — remote',
    selfAddresses: { addresses: ['192.0.2.44'] }, connectedCidrs: CONNECTED },
  { name: 'THREE addresses, all local — the reported one is addrs[0], not the match',
    selfAddresses: { addresses: ['198.51.100.7', '203.0.113.9', '2001:db8:1::5'] },
    connectedCidrs: CONNECTED },
  { name: 'THREE addresses, the SECOND off subnet — the off-subnet one is reported',
    selfAddresses: { addresses: ['203.0.113.9', '192.0.2.44', '198.51.100.7'] },
    connectedCidrs: CONNECTED },
  { name: 'THREE addresses, the LAST off subnet',
    selfAddresses: { addresses: ['203.0.113.9', '198.51.100.7', '192.0.2.44'] },
    connectedCidrs: CONNECTED },
  { name: 'TWO off-subnet addresses — the FIRST is reported',
    selfAddresses: { addresses: ['192.0.2.44', '192.0.2.99'] }, connectedCidrs: CONNECTED },
  { name: 'IPv6 session inside the connected v6 prefix — local',
    selfAddresses: { addresses: ['2001:db8:1::5'] }, connectedCidrs: CONNECTED },
  { name: 'IPv6 session OUTSIDE it — remote, and the v4 subnets must not match it',
    selfAddresses: { addresses: ['2001:db8:99::5'] }, connectedCidrs: CONNECTED },
  { name: 'an address on the far half of a /25 is OUTSIDE it',
    selfAddresses: { addresses: ['198.51.100.200'] }, connectedCidrs: ['198.51.100.0/25'] },
  { name: 'a bare address as a connected subnet means /32, not everything',
    selfAddresses: { addresses: ['192.0.2.44'] }, connectedCidrs: ['203.0.113.9'] },
  { name: 'an unparseable session address is off-subnet, not an error',
    selfAddresses: { addresses: ['not-an-address'] }, connectedCidrs: CONNECTED },
];

const ACTIONS = [
  { targetWan: 'ether1', activeDefaultWan: 'ether1' },
  { targetWan: 'ether1', activeDefaultWan: 'ether2' },
  { targetWan: 'ether1', activeDefaultWan: '' },
  { targetWan: '', activeDefaultWan: '' },
  { targetWan: 'pppoe-out1', activeDefaultWan: 'pppoe-out1' },
  { targetWan: undefined, activeDefaultWan: 'ether1' },
];

const cases = [];
for (const p of PATHS) {
  const resolved = wanGuard.resolveManagementPath({
    selfAddresses: p.selfAddresses, connectedCidrs: p.connectedCidrs,
  });
  const actions = ACTIONS.map((a) => ({
    targetWan: a.targetWan === undefined ? null : a.targetWan,
    activeDefaultWan: a.activeDefaultWan,
    verdict: wanGuard.checkLeaseAction({
      path: resolved, targetWan: a.targetWan, activeDefaultWan: a.activeDefaultWan,
    }),
  }));
  cases.push({
    name: p.name,
    selfAddresses: p.selfAddresses,
    connectedCidrs: (p.connectedCidrs || []).map((c) => (c === undefined ? null : c)),
    path: resolved,
    actions,
  });
}

// A path object the guard never produced, to pin the fail-open branch on input
// it can actually receive: the handler passes whatever resolve returned, and a
// null there must not throw.
cases.push({
  name: 'a null path — fail open rather than throw',
  selfAddresses: null, connectedCidrs: [], path: null,
  actions: [{
    targetWan: 'ether1', activeDefaultWan: 'ether1',
    verdict: wanGuard.checkLeaseAction({ path: null, targetWan: 'ether1', activeDefaultWan: 'ether1' }),
  }],
});

const warned = cases.reduce((n, c) => n + c.actions.filter((a) => a.verdict.level === 'warn').length, 0);
const total = cases.reduce((n, c) => n + c.actions.length, 0);
// BELIEVABILITY. A corpus of nothing but `none` would pass a port that never
// warns, which is the whole failure this guard exists to prevent.
if (!warned) throw new Error('no case warns — the corpus cannot tell a working guard from a silent one');
if (warned === total) throw new Error('every case warns — the corpus cannot see a guard that warns always');

const text = JSON.stringify({ generatedFrom: 'src/routeros/wanGuard.js', cases }, null, 2) + '\n';
if (CHECK) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (cur !== text) { console.error('wanguard-cases.json is STALE — run: node tools/wanguard-cases.js'); process.exit(1); }
  console.log(`wanguard-cases.json up to date (${cases.length} paths, ${total} verdicts, ${warned} warn)`);
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, text);
  console.log(`wrote ${cases.length} paths, ${total} verdicts (${warned} warn) -> ${path.relative(process.cwd(), OUT)}`);
}
