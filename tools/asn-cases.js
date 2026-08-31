'use strict';
/**
 * ASN cases — what the LIVE lookupOrg answers, for the Go port to reproduce.
 *
 * ── THE TABLE AND THE MATCHING FAIL DIFFERENTLY ─────────────────────────────
 *
 * tools/asn-table.js already guarantees the Go side holds the same ranges: it
 * generates them, so a transcription error is not possible and drift fails
 * `--check`. What it cannot say is whether the two implementations DECIDE the
 * same way given those ranges — first-match ordering, the v4/v6 family rules,
 * the v4-mapped unwrap, what an unparseable address does. That is what this
 * file pins, by asking the live function.
 *
 * ── THE ADDRESSES COME FROM THE RANGES ──────────────────────────────────────
 *
 * Not a random sweep: with 339 ranges, most of them small, random addresses
 * would land almost entirely outside all of them and prove nothing. Each range
 * contributes its FIRST address, its LAST, one in the middle, and the two
 * addresses immediately outside it — which is where an off-by-one in prefix
 * handling shows up and nowhere else.
 *
 * Every address is synthetic, computed from a published range. Nothing here
 * comes from a capture or from the operator's network.
 *
 *   node tools/asn-cases.js            write testdata/asn-cases.json
 *   node tools/asn-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'asn-cases.json');
const SRC = path.join(LIVE, 'src', 'util', 'asnLookup.js');

const { lookupOrg, lookupCategory } = require(SRC);

/** Lift the range list the same way tools/asn-table.js does. */
function liftLiteral(src, name, open, close) {
  const start = src.indexOf('const ' + name + ' = ' + open);
  if (start === -1) throw new Error(name + ' not found');
  const from = src.indexOf(open, start);
  let depth = 0, i = from;
  for (; i < src.length; i++) {
    if (src[i] === open) depth++;
    else if (src[i] === close) { depth--; if (depth === 0) break; }
  }
  return new Function('return ' + src.slice(from, i + 1))();
}

// ── v4 arithmetic. Multiplication rather than `<<`: a shift in JavaScript is a
// 32-bit SIGNED operation, so `1 << 31` is negative and every address in the
// top half of the space comes out wrong.
const aton4 = (s) => s.split('.').reduce((n, o) => (n * 256) + parseInt(o, 10), 0);
const ntoa4 = (n) => [24, 16, 8, 0].map((sh) => Math.floor(n / Math.pow(2, sh)) % 256).join('.');

/**
 * v6 arithmetic on BigInt, because 128 bits does not fit anywhere else.
 * Only the forms this table uses need parsing: a prefix like `2001:4860::/32`.
 */
function aton6(s) {
  const [head, tail] = s.split('::');
  const h = head ? head.split(':').filter(Boolean) : [];
  const t = tail ? tail.split(':').filter(Boolean) : [];
  const mid = new Array(Math.max(0, 8 - h.length - t.length)).fill('0');
  const groups = [...h, ...mid, ...t];
  return groups.reduce((n, g) => (n << 16n) + BigInt(parseInt(g || '0', 16)), 0n);
}
function ntoa6(n) {
  const groups = [];
  for (let i = 7; i >= 0; i--) groups.push(Number((n >> BigInt(i * 16)) & 0xffffn).toString(16));
  return groups.join(':');
}

function addresses() {
  const src = fs.readFileSync(SRC, 'utf8');
  const orgs = liftLiteral(src, 'ORGS', '[', ']');
  const out = [];

  for (const e of orgs) {
    for (const cidr of e.ranges) {
      const [net, bitsRaw] = cidr.split('/');
      const bits = parseInt(bitsRaw, 10);
      if (cidr.includes(':')) {
        const base = aton6(net);
        const size = 1n << BigInt(128 - bits);
        out.push(ntoa6(base));                     // first
        out.push(ntoa6(base + size - 1n));         // last
        out.push(ntoa6(base + (size >> 1n)));      // middle
        if (base > 0n) out.push(ntoa6(base - 1n)); // just below
        out.push(ntoa6(base + size));              // just above
      } else {
        const base = aton4(net);
        const size = Math.pow(2, 32 - bits);
        out.push(ntoa4(base));
        out.push(ntoa4(base + size - 1));
        out.push(ntoa4(base + Math.floor(size / 2)));
        if (base > 0) out.push(ntoa4(base - 1));
        if (base + size <= 4294967295) out.push(ntoa4(base + size));
      }
    }
  }

  // ── the shapes that are about the MATCHER rather than the ranges ───────────
  //
  // The v4-mapped forms are the ones worth naming: the original checks them
  // against the v6 ranges first and only then unwraps, so a port that only
  // unwrapped would agree today and diverge on a future ::ffff:0:0/96 entry.
  out.push('::ffff:8.8.8.8', '::ffff:1.1.1.1', '::ffff:17.1.2.3', '::ffff:192.168.1.1');
  out.push('::1', '::', 'fe80::1', 'fd00::1', 'ff02::1');
  out.push('0.0.0.0', '255.255.255.255', '127.0.0.1', '10.0.0.1', '192.168.1.1');
  // Malformed input reaches this function in the live app too — `_org` calls it
  // behind `isValidIp`, but the guard and the parser are different libraries.
  out.push('', 'not-an-ip', '1.2.3', '1.2.3.4.5', '999.1.1.1', '1.1.1.1/24',
    '8.8.8.8:443', '[2001:4860::1]', ' 8.8.8.8 ', '8.8.8.8 ', ' 8.8.8.8',
    '010.0.0.1', '0x08.8.8.8');
  // A ZONE. Both references accept one and ignore it — `ipaddr.parse` reads the
  // last group with parseInt, which stops at the `%` — so a port that refused it
  // would disagree, and so would one that kept the zone on the address it
  // matches with.
  out.push('2001:4860::1%eth0', 'fe80::1%eth0', '8.8.8.8%eth0');
  // ── THE inet_aton FORMS, WHICH THE PORT KNOWINGLY DOES NOT ACCEPT ──────────
  //
  // ipaddr.js implements the whole legacy grammar: hex octets, octal octets,
  // and the 1-, 2- and 3-part forms. Every one of these IS 8.8.8.8 to it, and
  // therefore Google. Go's netip rejects all of them, so the port answers
  // nothing — a real divergence, kept visible here and asserted in
  // internal/asn/asn_test.go rather than papered over by deleting the cases.
  //
  // These forms are chosen to land INSIDE a range on purpose. A reinterpreted
  // address that lands in no range agrees with the port by accident
  // (`010.0.0.1` is 8.0.0.1, which nobody publishes), and a case that passes by
  // accident is the kind that stops failing when the data moves.
  out.push('0x08.8.8.8', '0x8.0x8.0x8.0x8', '134744072', '8.526344', '8.8.2056',
    '8.8.8.010', '017.0.0.1');

  return [...new Set(out)];
}

function main() {
  const check = process.argv.includes('--check');
  const cases = addresses().map((ip) => {
    const org = lookupOrg(ip);
    // `org` is null when nothing matched. It is recorded as an empty string with
    // a separate `found`, for the same reason the geo cases record `found`: one
    // field cannot say the difference between "no answer" and "an empty answer",
    // and collapsing them is how a gate ends up asserting its own definition
    // instead of the implementation's behaviour.
    return {
      ip, found: org !== null && org !== undefined, org: org || '',
      cat: lookupCategory(org),
    };
  });

  const matched = cases.filter((c) => c.found).length;
  const body = JSON.stringify({
    note: 'Generated by tools/asn-cases.js — do not edit. Answers come from ' +
          'src/util/asnLookup.js in the live repo, which is what the Go package reproduces.',
    total: cases.length, matched,
    cases,
  }, null, 2) + '\n';

  if (check) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body) {
      console.error('testdata/asn-cases.json is stale — asnLookup.js has changed.\n' +
                    'Run: node tools/asn-cases.js');
      process.exit(1);
    }
    console.log('asn cases up to date (' + matched + '/' + cases.length + ' matched)');
    return;
  }
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), OUT) +
    ' — ' + cases.length + ' addresses, ' + matched + ' matched');
}

main();
