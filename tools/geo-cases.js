'use strict';
/**
 * Geo cases — what geoip-lite answers, for the Go reader to reproduce.
 *
 * ── WHY THE PORT READS geoip-lite's OWN FILES ───────────────────────────────
 *
 * The live app's geo is `geoip-lite`, an npm package carrying ~146 MB of its own
 * binary data, used through its supported `lookup(ip)`. The Go port cannot
 * require an npm package, and the tempting alternative — a Go MaxMind reader
 * with an .mmdb — would be a DIFFERENT DATA SOURCE giving different answers.
 * Every disagreement would then have to be triaged as "port defect or different
 * database?", which is exactly the question a differential gate exists to
 * remove.
 *
 * So the port reads the same `geoip-city.dat` and `geoip-city-names.dat` the
 * package ships, with the same binary search. Same data, same answers, and this
 * file is what proves it.
 *
 * ── THE ADDRESSES ARE SYNTHETIC ON PURPOSE ──────────────────────────────────
 *
 * A systematic sweep plus a handful of well-known public resolvers — no address
 * from any capture, and nothing from the operator's network. A geo case set
 * built from real traffic would put someone's destinations in a public repo.
 *
 *   node tools/geo-cases.js            write testdata/geo-cases.json
 *   node tools/geo-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'geo-cases.json');

const geoip = require(path.join(LIVE, 'node_modules', 'geoip-lite'));

/**
 * The addresses to ask about.
 *
 * Three groups, each testing something the reader has to get right:
 *
 *   sweep     every 4.x.y.1 across the space, which walks the whole index and
 *             lands in ranges of every size — the binary search's real exercise
 *   known     public resolvers, so a human can sanity-check the answers
 *   edges     private ranges and the ends of the space, ALL of which must come
 *             back null: geoip-lite refuses them before it searches, and a
 *             reader that searched anyway would return a neighbour's answer
 */
function addresses() {
  const out = [];
  for (let a = 1; a < 256; a += 7) {
    for (const b of [0, 77, 128, 201]) out.push(a + '.' + b + '.13.1');
  }
  out.push('1.1.1.1', '8.8.8.8', '9.9.9.9', '208.67.222.222', '185.228.168.9');
  out.push('10.0.0.1', '10.255.255.255', '172.16.0.1', '172.31.255.255',
    '192.168.0.1', '192.168.255.255', '127.0.0.1', '0.0.0.0', '255.255.255.255',
    '169.254.1.1', '224.0.0.251');

  // ── A WIDE DETERMINISTIC SAMPLE, added because a small one proved too weak ──
  //
  // The first version of this file held 164 addresses, and a mutation that
  // replaced geoip-lite's `Math.round` midpoint with a plain floor PASSED it:
  // on a small sample the two searches happen to converge on the same record
  // every time. They do not always. With three million ranges the divergence is
  // real but sparse, so the sample has to be large enough to land on one.
  //
  // A fixed-seed generator rather than random: the case file must be identical
  // on every run or `--check` becomes a coin toss.
  let seed = 20260822;
  const next = () => {
    // A plain LCG — reproducible, and adequate for spreading addresses about.
    seed = (seed * 1103515245 + 12345) & 0x7fffffff;
    return seed;
  };
  for (let i = 0; i < 3000; i++) {
    const n = next();
    const a = (n >>> 23) & 0xff, b = (n >>> 15) & 0xff, c = (n >>> 7) & 0xff, d = n & 0xff;
    if (a === 0 || a === 127 || a >= 224) continue; // reserved space, never located
    out.push(a + '.' + b + '.' + c + '.' + d);
  }

  // ── IPv6, which the reader refused outright until this was added ────────────
  //
  // The live app gates its lookups on `ipaddr.isValid`, which accepts v6, so
  // every v6 destination a router sees IS geo-located there — through a
  // separate 48-byte index in geoip-city6.dat with its own comparison rules.
  // The Go reader handled v4 only and returned nothing for all of it, and no
  // case in this file could notice, because every case was v4.
  //
  // Four groups, each pinning something the v6 path has to get right:
  //
  //   global    2000::/3, where the allocated space is — the search's exercise
  //   mapped    `::ffff:a.b.c.d`, which geoip-lite unwraps and sends to the V4
  //             index. A reader that searched the v6 index for these would miss
  //             every one of them and look merely unlucky.
  //   local     ULA and link-local. There is NO privateRange6 in geoip-lite, so
  //             unlike their v4 counterparts these are searched rather than
  //             refused; they still miss, but by a different route.
  //   edges     the ends of the space and the all-zeros address
  for (let i = 0; i < 700; i++) {
    const h = [];
    for (let g = 0; g < 4; g++) {
      const n = next();
      h.push(((n >>> 8) & 0xffff).toString(16));
    }
    // Prefixes carrying most of the world's allocated v6 space, so the sample
    // lands inside the index rather than in a hole.
    const pfx = ['2001', '2400', '2600', '2606', '2a00', '2a02', '2c0f', '2803'][i & 7];
    out.push(pfx + ':' + h[0] + ':' + h[1] + ':' + h[2] + '::' + h[3]);
  }
  // WITHIN a single /32, which the random sample above barely exercises.
  //
  // The comparison the v6 search uses is 64 bits wide, and narrowing it to 32
  // is the kind of mutation a sample of scattered addresses almost survives:
  // two addresses have to share their top 32 bits before the width can matter,
  // and randomly drawn ones rarely do. These share it by construction, so the
  // width is gated by more than luck — the first version of this sample caught
  // that mutation in exactly one case, which a geoip data refresh could erase.
  for (const pfx of ['2001:4860', '2606:4700', '2a00:1450', '2400:cb00', '2a02:26f0']) {
    for (let j = 0; j < 24; j++) {
      const g3 = ((j * 4099) & 0xffff).toString(16);
      const g4 = ((j * 271) & 0xffff).toString(16);
      out.push(pfx + ':' + g3 + ':' + g4 + '::1');
    }
  }
  out.push('2001:4860:4860::8888', '2606:4700:4700::1111', '2620:fe::fe',
    '2001:db8::1', '2001:db8:85a3::8a2e:370:7334');
  out.push('::ffff:8.8.8.8', '::ffff:1.1.1.1', '::ffff:10.0.0.1', '::ffff:192.168.1.1');
  out.push('fd00::1', 'fc00::abcd', 'fe80::1', 'fe80::dead:beef',
    '::1', '::', '2000::', '3fff:ffff:ffff:ffff:ffff:ffff:ffff:ffff',
    'ff02::1');

  // ── MALFORMED AND PADDED INPUT, which is about the PARSER, not the index ───
  //
  // Added after the ASN gate caught the same defect one package over: this
  // reader trimmed whitespace before parsing, so ` 8.8.8.8 ` resolved to the
  // United States where the live app resolves nothing — `net.isIP` returns 0 for
  // a padded address and geoip-lite answers null. A kindness, and a behaviour
  // change. No case here could see it, because every address in this file was
  // already well-formed; addresses that a parser must REFUSE are as much a part
  // of the contract as the ones it must place.
  out.push(' 8.8.8.8 ', '8.8.8.8 ', ' 8.8.8.8', '\t8.8.8.8', '8.8.8.8\n');
  out.push('', 'not-an-ip', '1.2.3', '1.2.3.4.5', '999.1.1.1', '1.1.1.1/24',
    '8.8.8.8:443', '[2001:4860::1]', '010.0.0.1', '0x08.8.8.8',
    '2001:4860::1%eth0', 'fe80::1%eth0', '8.8.8.8%eth0',
    '::ffff:8.8.8.8', '::ffff:1.1.1.1');
  return out;
}

function main() {
  const check = process.argv.includes('--check');
  const cases = addresses().map((ip) => {
    const g = geoip.lookup(ip);
    // `found` IS SEPARATE FROM `country`, and the distinction is not academic:
    // some ranges resolve to a location record that carries a timezone and
    // coordinates but an EMPTY country code. geoip-lite returns an object for
    // those, and a case set that recorded only the country would call them
    // "not located" — which is how the first version of this gate reported a
    // disagreement that was its own definition rather than the reader's answer.
    //
    // ── WIDENED: region, ll AND area ARE NOW CONSUMED ────────────────────
    //
    // This used to keep only country and city, on the reasoning that they were
    // all the collectors read and that recording coordinates would make the
    // file churn on every geoip-lite data refresh for fields nothing consumes.
    // That reason expired: the Routers map's automatic fix — `autoGeoAction`
    // in src/geoPlace.js — reads `ll` and `area` off this very lookup, and
    // `region` reaches the rendered label. Recording them is what lets the Go
    // reader be gated on the fields the map actually draws.
    //
    // `ll` IS `[null, null]` WHEN THERE IS NO LOCATION RECORD, and that is not
    // the same as [0, 0] — which is a real place off west Africa. The nulls are
    // kept verbatim rather than coerced, because the whole point of the Go
    // reader's pointer fields is to preserve that distinction.
    //
    // `area` is left UNDEFINED by geoip-lite when there is no location record,
    // and JSON.stringify drops an undefined value. `?? null` makes the absence
    // explicit instead, so a missing key cannot read as "the generator forgot".
    // autoGeoAction's `Number(g.area) || 0` treats absent and 0 alike anyway.
    // The timezone, metro code and EU flag stay unrecorded: nothing renders
    // them, so they would be churn on every data refresh for no gate.
    return {
      ip, found: !!g,
      country: g ? g.country : '',
      city:    g ? g.city   || '' : '',
      region:  g ? g.region || '' : '',
      ll:      g ? g.ll : null,
      area:    g ? (g.area ?? null) : null,
    };
  });

  const answered = cases.filter((c) => c.country).length;
  const body = JSON.stringify({
    note: 'Generated by tools/geo-cases.js — do not edit. Answers come from the ' +
          'geoip-lite data in the live repo, which is the same data the Go reader reads.',
    // Counts are NOT asserted anywhere: geoip-lite refreshes its data and the
    // answers move. What is asserted is that the two readers AGREE.
    total: cases.length, answered,
    found: cases.filter((c) => c.found).length,
    cases,
  }, null, 2) + '\n';

  if (check) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body) {
      console.error('testdata/geo-cases.json is stale — the geoip-lite data has changed.\n' +
                    'Run: node tools/geo-cases.js');
      process.exit(1);
    }
    console.log('geo cases up to date (' + answered + '/' + cases.length + ' located)');
    return;
  }
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), OUT) +
    ' — ' + cases.length + ' addresses, ' + answered + ' located');
}

main();
