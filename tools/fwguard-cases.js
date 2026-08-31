#!/usr/bin/env node
'use strict';
/**
 * Pin the firewall lockout warning against the LIVE implementation.
 *
 * WHY THIS ONE. `fwGuard` warns and fails open like `queueGuard`, so a wrong
 * answer is equally silent — but the thing it fails to warn about is the one
 * hazard issue #97 names first, and unlike a queue it is NOT recoverable from
 * the row that caused it. A queue that throttles the dashboard can be edited
 * from the page it slowed down. A filter rule that drops the API session cannot
 * be edited from anywhere; the fix is WinBox, in person.
 *
 * It is also the guard with the most ARITHMETIC. Address containment and port
 * specs are the kind of thing two implementations agree on for every case
 * anybody thought to try and differ on the one nobody did, which is exactly what
 * a differential gate is for.
 *
 * THREE THINGS HERE WERE SETTLED BY RUNNING IT, NOT BY READING IT:
 *   - a bare `src-address=10.0.0.5` counts as its own /32, so a rule naming our
 *     exact address does warn;
 *   - `addressCovers` reduces to "does the spec look like an address", because
 *     the first half of its final conjunction is always true by the time it runs;
 *   - `Number('0443') === 443`, so a zero-padded port spec still covers us.
 *
 *   node tools/fwguard-cases.js            # write testdata/fwguard-cases.json
 *   node tools/fwguard-cases.js --check    # fail if it is stale
 */

const fs   = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT  = process.env.FWGUARD_OUT || path.join(__dirname, '..', 'testdata', 'fwguard-cases.json');

const G = require(path.join(ROOT, 'src', 'routeros', 'fwGuard.js'));
for (const fn of ['checkRule', 'matchesUs', 'addressCovers', 'portCovers']) {
  if (typeof G[fn] !== 'function') {
    console.error('src/routeros/fwGuard.js no longer exports ' + fn + ' — this generator was ' +
                  'pinning a function that has moved. Find it before regenerating.');
    process.exit(1);
  }
}
const FILTER = '/ip/firewall/filter';
const RAW    = '/ip/firewall/raw';
for (const [menu, chain] of [[FILTER, 'input'], [RAW, 'prerouting']]) {
  if (G.TO_ROUTER_CHAIN[menu] !== chain) {
    console.error('TO_ROUTER_CHAIN changed for ' + menu + ' — the port hard-codes ' + chain + '.');
    process.exit(1);
  }
}

// ── Our own connection ───────────────────────────────────────────────────────
const CTX = { resolved: true, addresses: ['10.0.0.5', '192.168.88.20'],
              interfaces: ['bridge', 'ether1'], apiPort: 8728 };
const CTX_TLS  = { ...CTX, apiPort: 8729 };
const CTX_NONE = { resolved: false, addresses: [], interfaces: [], apiPort: 8728 };
// Resolved, but /user/active gave nothing — the shape a read-only API user
// produces, and the reason addressCovers has to treat "no addresses" carefully.
const CTX_NOADDR = { resolved: true, addresses: [], interfaces: ['bridge'], apiPort: 8728 };

// ── Address specs ────────────────────────────────────────────────────────────
const ADDRS = [
  '', '   ',
  '10.0.0.5',            // our exact address, bare
  '10.0.0.5/32',
  '10.0.0.0/24',         // contains us
  '10.0.0.0/8',
  '192.168.88.0/24',     // contains the OTHER of our addresses
  '172.16.0.0/12',       // contains neither
  '0.0.0.0/0',           // everything
  '!10.0.0.0/8',         // negation -> undecidable
  '10.0.0.1-10.0.0.9',   // range -> undecidable
  'trusted',             // an address-list name -> undecidable
  'beef',                // a list name that is ALSO hex-shaped
  'cafe',
  'dead:beef::/32',      // v6 spec against v4 addresses
  '2001:db8::1',
  '10.0.0.256/24',       // not an octet
  '10.0.0',              // three-part form — ipaddr.js reads it as 10.0.0.0
  '10.0',                // two-part form
  '167772165',           // the whole address as one integer
  '0x0a000005',          // hex
  '012.0.0.5',           // a leading zero is OCTAL, so this is 10.0.0.5
  'not an address',
  // The prefix half. `parseInt(parts[1], 10)` is NaN for all three of these,
  // and matchCIDR's `while (cidrBits > 0)` never runs — see ToDo item 8.
  '203.0.113.9',         // a bare address that is NOT ours
  '203.0.113.9/32',      // the same rule written with its prefix
  '10.0.0.0/',           // empty prefix
  '10.0.0.0/abc',        // unparseable prefix
  '10.0.0.0/0',          // everything, legitimately
  '10.0.0.0/33',         // longer than the address — ipaddr.js throws, so: no match
  '10.0.0.0/99',
  // The compact forms again, this time WITH a prefix, so what they parse to is
  // pinned rather than hidden behind the NaN quirk above.
  '10.0/16',             // 10.0.0.0/16 — contains 10.0.0.5
  '0x0a000000/8',        // 10.0.0.0/8
  '012.0.0.0/24',        // a leading zero is octal: 012 is 10, so 10.0.0.0/24
  '011.0.0.0/8',         // 011 is 9, so 9.0.0.0/8 — does NOT contain us
  '10.0.0.0/31',         // a prefix one short of the full length
  '10.0.0.5/32',         // exactly the full length
];

// ── Port specs ───────────────────────────────────────────────────────────────
const PORTS = [
  ['', 8728], ['   ', 8728],
  ['8728', 8728], ['8729', 8728],
  ['8728,8729', 8728], ['80,443,8728', 8728], ['80,443', 8728],
  ['8000-9000', 8728], ['1-1024', 8728], ['8728-8728', 8728],
  ['0443', 443], [' 8728 ', 8728], ['08728', 8728],
  ['abc', 8728], ['8000-abc', 8728], ['-', 8728], [',,', 8728],
  ['80,,443', 443], ['1000-2000,8728', 8728],
  ['8729', 8729], ['8728', 8729],
];

// ── Rules ────────────────────────────────────────────────────────────────────
const R = (o) => Object.assign({ chain: '', action: '', srcAddress: '', dstAddress: '',
                                 protocol: '', dstPort: '', inInterface: '', disabled: false }, o);

const RULES = [
  { n: 'bare input drop — matches on all four counts', r: R({ chain: 'input', action: 'drop' }) },
  { n: 'input reject',  r: R({ chain: 'input', action: 'reject' }) },
  { n: 'input tarpit',  r: R({ chain: 'input', action: 'tarpit' }) },
  { n: 'input accept',  r: R({ chain: 'input', action: 'accept' }) },
  { n: 'input log — not a blocking action', r: R({ chain: 'input', action: 'log' }) },
  { n: 'FORWARD drop — cannot touch our session', r: R({ chain: 'forward', action: 'drop' }) },
  { n: 'output drop', r: R({ chain: 'output', action: 'drop' }) },
  { n: 'INPUT in caps', r: R({ chain: 'INPUT', action: 'DROP' }) },
  { n: 'drop, but disabled', r: R({ chain: 'input', action: 'drop', disabled: true }) },
  { n: 'drop from a subnet holding us', r: R({ chain: 'input', action: 'drop', srcAddress: '10.0.0.0/24' }) },
  { n: 'drop from a subnet holding neither', r: R({ chain: 'input', action: 'drop', srcAddress: '172.16.0.0/12' }) },
  { n: 'drop from an address list', r: R({ chain: 'input', action: 'drop', srcAddress: 'trusted' }) },
  { n: 'drop, udp only — the API is TCP', r: R({ chain: 'input', action: 'drop', protocol: 'udp' }) },
  { n: 'drop, tcp', r: R({ chain: 'input', action: 'drop', protocol: 'tcp' }) },
  { n: 'drop, tcp on OUR port', r: R({ chain: 'input', action: 'drop', protocol: 'tcp', dstPort: '8728' }) },
  { n: 'drop, tcp on another port', r: R({ chain: 'input', action: 'drop', protocol: 'tcp', dstPort: '22' }) },
  { n: 'drop on our interface', r: R({ chain: 'input', action: 'drop', inInterface: 'bridge' }) },
  { n: 'drop on an interface we are not on', r: R({ chain: 'input', action: 'drop', inInterface: 'ether9' }) },
  { n: 'drop on BRIDGE in caps', r: R({ chain: 'input', action: 'drop', inInterface: 'BRIDGE' }) },
  { n: 'accept, disabled', r: R({ chain: 'input', action: 'accept', disabled: true }) },
  { n: 'prerouting drop — the raw table', r: R({ chain: 'prerouting', action: 'drop' }) },
];

const WHATS = ['create', 'update', 'delete', 'enable', 'disable', 'move'];

function run() {
  const addressCovers = [];
  for (const spec of ADDRS) {
    for (const [label, ctx] of [['two', CTX], ['none', CTX_NOADDR]]) {
      addressCovers.push({ spec, addresses: ctx.addresses, ctx: label,
                           want: G.addressCovers(spec, ctx.addresses) });
    }
  }

  const portCovers = PORTS.map(([spec, port]) => ({ spec, port, want: G.portCovers(spec, port) }));

  const matchesUs = [];
  for (const { n, r } of RULES) {
    for (const [label, ctx] of [['8728', CTX], ['8729', CTX_TLS]]) {
      matchesUs.push({ name: n + ' @' + label, rule: r, ctx: label, want: G.matchesUs(r, ctx) });
    }
  }

  const verdicts = [];
  for (const { n, r } of RULES) {
    for (const what of WHATS) {
      for (const [menuLabel, menu] of [['filter', FILTER], ['raw', RAW], ['nat', '/ip/firewall/nat']]) {
        for (const [ctxLabel, ctx] of [['ok', CTX], ['unresolved', CTX_NONE]]) {
          // `before` matters for delete/disable/move; a create has none.
          const before = what === 'create' ? null : r;
          const v = G.checkRule({ ctx, menu, values: r, before, what });
          verdicts.push({
            name: n, what, menu: menuLabel, ctx: ctxLabel,
            rule: r, hasBefore: !!before,
            want: {
              level: v.level, code: v.code || '',
              detail: v.detail ? {
                kind: v.detail.kind, chain: v.detail.chain, action: v.detail.action,
                what: v.detail.what,
                address: v.detail.address === null ? '' : v.detail.address,
                iface: v.detail.interface === null ? '' : v.detail.interface,
                port: v.detail.port,
              } : null,
              fingerprint: v.fingerprint || '',
            },
          });
        }
      }
    }
  }

  // A DISABLED `before` with an enabled `values`, which is the shape `enable`
  // and `disable` actually arrive in and the only way case 1's
  // `disabled && what !== 'enable'` clause and case 2's `before.disabled` clause
  // are exercised against each other.
  const toggles = [];
  for (const act of ['drop', 'accept']) {
    for (const what of ['enable', 'disable', 'delete', 'move']) {
      for (const beforeDisabled of [true, false]) {
        const values = R({ chain: 'input', action: act, disabled: what === 'disable' });
        const before = R({ chain: 'input', action: act, disabled: beforeDisabled });
        const v = G.checkRule({ ctx: CTX, menu: FILTER, values, before, what });
        toggles.push({ action: act, what, beforeDisabled,
                       want: { level: v.level, code: v.code || '',
                               kind: v.detail ? v.detail.kind : '' } });
      }
    }
  }

  return { ctx: CTX, ctxTls: CTX_TLS, ctxNoAddr: CTX_NOADDR,
           addressCovers, portCovers, matchesUs, verdicts, toggles };
}

const out = JSON.stringify(run(), null, 2) + '\n';
const n = run();
const counts = `${n.addressCovers.length} address, ${n.portCovers.length} port, ` +
               `${n.matchesUs.length} match, ${n.verdicts.length} verdict, ${n.toggles.length} toggle`;

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== out) {
    console.error('testdata/fwguard-cases.json is stale — run: node tools/fwguard-cases.js');
    process.exit(1);
  }
  console.log(`fwguard-cases up to date (${counts})`);
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, out);
  console.log(`wrote ${path.relative(process.cwd(), OUT)} — ${counts}`);
}
