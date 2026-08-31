#!/usr/bin/env node
'use strict';
/**
 * Pin the self-throttle warning — and the rate parsing under it — against the
 * LIVE implementation.
 *
 * WHY THIS ONE. queueGuard is the opposite of selfGuard in both directions: it
 * warns rather than refuses, and it fails open rather than closed. That makes a
 * wrong answer QUIET. A guard that refuses tells you when it is wrong, because
 * somebody is locked out and complains; a guard that warns and gets it wrong
 * either says nothing when it should have, or cries wolf until the operator
 * clicks through the one that mattered. Neither shows up in a screenshot.
 *
 * There is no fixture for it — it is pure arithmetic over rows — so the only way
 * to know the port agrees with the original is to run both. This runs the live
 * `queueGuard` over each case and records what it returns;
 * internal/guard/queueguard_test.go replays the same inputs through the port and
 * compares. Neither implementation is asked about itself.
 *
 * THE PARSERS ARE PINNED TOO, not just the verdict. `parseRate` and `parsePair`
 * are what the COLLECTOR uses to build the payload, so a disagreement there is a
 * wrong number on the page long before it is a wrong warning — and the
 * null-versus-zero distinction the original calls out is exactly the kind of
 * thing a port quietly flattens.
 *
 *   node tools/queueguard-cases.js            # write testdata/queueguard-cases.json
 *   node tools/queueguard-cases.js --check    # fail if it is stale
 */

const fs   = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT  = process.env.QUEUEGUARD_OUT || path.join(__dirname, '..', 'testdata', 'queueguard-cases.json');

const G = require(path.join(ROOT, 'src', 'routeros', 'queueGuard.js'));
for (const fn of ['parseRate', 'parsePair', 'cidrContains', 'checkSimpleQueue']) {
  if (typeof G[fn] !== 'function') {
    console.error('src/routeros/queueGuard.js no longer exports ' + fn + ' — this generator was ' +
                  'pinning a function that has moved. Find it before regenerating.');
    process.exit(1);
  }
}
if (G.SELF_THROTTLE_FLOOR_BPS !== 1000000) {
  console.error('SELF_THROTTLE_FLOOR_BPS moved to ' + G.SELF_THROTTLE_FLOOR_BPS +
                ' — the port hard-codes 1000000. Re-sync before regenerating.');
  process.exit(1);
}

// ── What a rate can look like ────────────────────────────────────────────────
//
// The router's own form, the CLI form an operator types, and the shapes that
// must NOT parse. "0" and "" are the pair the original's header singles out.
const RATES = [
  '15000000', '0', '', '   ', '15M', '15m', '15 M', '1.5M', '0.5k', '512k', '512K',
  '2G', '2g', '1000000', '999999', '15MB', 'M', '1.5.2M', '-5M', '1e6', 'unlimited',
  '15M ', ' 15M', '00015M', '1.0M', '.5M',
];

const PAIRS = [
  '15000000/30000000', '15M/30M', '15M', '0/0', '0/15M', '15M/0', '', '/', '15M/',
  '/15M', '15M/30M/45M', 'garbage/15M', '10000000',
];

// ── What containment has to decide, and what it must refuse to ───────────────
const CIDRS = [
  ['10.0.0.0/24',      '10.0.0.53'],   // in
  ['10.0.0.0/24',      '10.0.1.53'],   // out
  ['10.0.0.53',        '10.0.0.53'],   // bare address, /32 implied
  ['10.0.0.53',        '10.0.0.54'],
  ['0.0.0.0/0',        '10.0.0.53'],   // contains everything
  ['10.0.0.0/0',       '198.51.100.7'],
  ['10.0.0.0/32',      '10.0.0.0'],
  ['10.0.0.0/33',      '10.0.0.1'],    // out of range -> undecidable
  ['10.0.0.0/-1',      '10.0.0.1'],
  ['10.0.0.0/abc',     '10.0.0.1'],
  ['10.0.0.0/',        '10.0.0.1'],    // Number('') is 0 in the original
  ['bridge',           '10.0.0.53'],   // an interface name decides nothing
  ['WAN1',             '10.0.0.53'],
  ['',                 '10.0.0.53'],
  ['   ',              '10.0.0.53'],
  ['2001:db8::/32',    '10.0.0.53'],   // v6 either side: not attempted
  ['10.0.0.0/24',      '2001:db8::1'],
  ['10.0.0.256/24',    '10.0.0.1'],    // 256 is not an octet
  ['10.0.0/24',        '10.0.0.1'],
  ['10.0.0.0/24',      ''],
  ['  10.0.0.0/24  ',  '10.0.0.53'],   // trimmed on both sides
  ['10.0.0.0/24',      ' 10.0.0.53 '],
  ['192.168.1.0/25',   '192.168.1.100'],
  ['192.168.1.0/25',   '192.168.1.200'],
];

// ── The verdict ──────────────────────────────────────────────────────────────
//
// Our address on this router. The second entry exists so the fingerprint's sort
// is exercised by more than one element.
const SELF = { addresses: ['10.0.0.53', '10.0.0.9'] };
const NONE = { addresses: [] };

const V = (target, maxLimit, disabled) => ({ target, maxLimit, disabled: !!disabled });

const CHECKS = [
  // Fail open: we do not know where we are, so nothing is said.
  { name: 'fail open — no self addresses',
    self: NONE, values: V('10.0.0.0/24', '500k/500k') },

  // Not in force.
  { name: 'disabled queue is not in force',
    self: SELF, values: V('10.0.0.0/24', '500k/500k', true) },

  // 0 is explicitly unlimited, and unlimited throttles nothing.
  { name: 'unlimited (0/0) covering us',
    self: SELF, values: V('10.0.0.0/24', '0/0') },
  { name: 'absent max-limit covering us',
    self: SELF, values: V('10.0.0.0/24', '') },

  // At and around the floor. 1M exactly is NOT below it.
  { name: 'exactly at the floor',   self: SELF, values: V('10.0.0.0/24', '1M/1M') },
  { name: 'one bit under the floor', self: SELF, values: V('10.0.0.0/24', '999999/999999') },
  { name: 'upload capped only',     self: SELF, values: V('10.0.0.0/24', '500k/50M') },
  { name: 'download capped only',   self: SELF, values: V('10.0.0.0/24', '50M/500k') },

  // Covering or not.
  { name: 'create — covers us, well under the floor',
    self: SELF, values: V('10.0.0.0/24', '500k/500k') },
  { name: 'create — covers the OTHER of our addresses',
    self: SELF, values: V('10.0.0.9', '100k/100k') },
  { name: 'create — does not cover us',
    self: SELF, values: V('192.168.99.0/24', '500k/500k') },
  { name: 'create — interface target, undecidable',
    self: SELF, values: V('bridge', '500k/500k') },
  { name: 'create — 0.0.0.0/0 covers everything',
    self: SELF, values: V('0.0.0.0/0', '500k/500k') },

  // Edits. Only a change for the WORSE warns.
  { name: 'edit — unchanged throttling queue stays quiet',
    self: SELF, values: V('10.0.0.0/24', '500k/500k'), before: V('10.0.0.0/24', '500k/500k') },
  { name: 'edit — comment-only on a throttling queue (same inputs) stays quiet',
    self: SELF, values: V('10.0.0.0/24', '500k/500k'), before: V('10.0.0.0/24', '500k/500k') },
  { name: 'edit — raised the cap, still under the floor',
    self: SELF, values: V('10.0.0.0/24', '900k/900k'), before: V('10.0.0.0/24', '500k/500k') },
  { name: 'edit — LOWERED the cap',
    self: SELF, values: V('10.0.0.0/24', '100k/500k'), before: V('10.0.0.0/24', '500k/500k') },
  { name: 'edit — lowered the download half only',
    self: SELF, values: V('10.0.0.0/24', '500k/100k'), before: V('10.0.0.0/24', '500k/500k') },
  { name: 'edit — newly enabled',
    self: SELF, values: V('10.0.0.0/24', '500k/500k'), before: V('10.0.0.0/24', '500k/500k', true) },
  { name: 'edit — newly covering us',
    self: SELF, values: V('10.0.0.0/24', '500k/500k'), before: V('192.168.99.0/24', '500k/500k') },
  { name: 'edit — was unlimited, now capped',
    self: SELF, values: V('10.0.0.0/24', '500k/500k'), before: V('10.0.0.0/24', '0/0') },
  { name: 'edit — was absent, now capped',
    self: SELF, values: V('10.0.0.0/24', '500k/500k'), before: V('10.0.0.0/24', '') },
  { name: 'edit — was an interface target (undecidable), now ours',
    self: SELF, values: V('10.0.0.0/24', '500k/500k'), before: V('bridge', '500k/500k') },
  { name: 'edit — narrowed from /24 to the host, same cap',
    self: SELF, values: V('10.0.0.53', '500k/500k'), before: V('10.0.0.0/24', '500k/500k') },
  { name: 'edit — being disabled',
    self: SELF, values: V('10.0.0.0/24', '500k/500k', true), before: V('10.0.0.0/24', '500k/500k') },
];

function run() {
  return {
    floorBps: G.SELF_THROTTLE_FLOOR_BPS,
    rates: RATES.map((raw) => ({ raw, want: G.parseRate(raw) })),
    pairs: PAIRS.map((raw) => {
      const p = G.parsePair(raw);
      return { raw, up: p.up, down: p.down };
    }),
    cidrs: CIDRS.map(([cidr, ip]) => ({ cidr, ip, want: G.cidrContains(cidr, ip) })),
    checks: CHECKS.map((c) => {
      const want = G.checkSimpleQueue({
        selfAddresses: c.self,
        values: c.values,
        before: c.before || null,
        floorBps: G.SELF_THROTTLE_FLOOR_BPS,
      });
      return {
        name: c.name,
        selfAddresses: c.self.addresses,
        values: c.values,
        before: c.before || null,
        want: {
          level: want.level,
          code: want.code || '',
          // Recorded field by field rather than as a blob: a detail that drifted
          // shape would otherwise read as one opaque mismatch.
          detail: want.detail ? {
            address: want.detail.address,
            target: want.detail.target,
            maxLimitUp: want.detail.maxLimit.up,
            maxLimitDown: want.detail.maxLimit.down,
          } : null,
          fingerprint: want.fingerprint || '',
        },
      };
    }),
  };
}

const out = JSON.stringify(run(), null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== out) {
    console.error('testdata/queueguard-cases.json is stale — run: node tools/queueguard-cases.js');
    process.exit(1);
  }
  const n = run();
  console.log(`queueguard-cases up to date (${n.rates.length} rates, ${n.pairs.length} pairs, ` +
              `${n.cidrs.length} containments, ${n.checks.length} verdicts)`);
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, out);
  const n = run();
  console.log(`wrote ${path.relative(process.cwd(), OUT)} — ${n.rates.length} rates, ` +
              `${n.pairs.length} pairs, ${n.cidrs.length} containments, ${n.checks.length} verdicts`);
}
