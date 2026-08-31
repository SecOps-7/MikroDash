'use strict';
/**
 * Record what a collector actually asks a live router, and what it gets back.
 *
 * Plan A1. The ~50 RouterOS behaviour workarounds in src/ were each found by
 * running against real hardware; 28 name the device they were found on. A port
 * that re-derives them pays for that discovery twice. These fixtures turn them
 * into inputs a second implementation must reproduce.
 *
 * RECORDED AT THE TRANSPORT, NOT DECLARED. The tool wraps ros.write() and runs
 * the REAL collector, so it never needs its own list of which commands a
 * collector issues — a list that would be wrong the first time a collector
 * changed. Whatever the collector asks is what gets captured, with its exact
 * proplist and argument order.
 *
 *   node tools/capture-fixtures.js --router "Mikrotik hAP AX3" --collector wifi
 *   node tools/capture-fixtures.js --list
 *
 * READ-ONLY. Only collectors run, and collectors only read. The recorder refuses
 * any mutating verb outright, and the router is left exactly as found.
 *
 * ── TWO SAFETY PROPERTIES, BOTH LOAD-BEARING ────────────────────────────────
 *
 * 1. NO CREDENTIAL IS EVER WRITTEN. Collectors read through proplists that name
 *    no secret (src/routeros/wifiMenus.js explains why), so a passphrase should
 *    never arrive. `assertClean()` checks anyway and ABORTS rather than warns —
 *    a fixture is committed to a public repository, and "probably fine" is not a
 *    standard to write secrets against.
 *
 * 2. NOTHING IDENTIFYING IS EVER WRITTEN. github.com/SecOps-7/MikroDash is
 *    public. A raw capture carries the operator's SSIDs, MAC addresses, serial
 *    numbers, LAN topology and WAN address — a map of a private home network.
 *    Every such value is replaced before it reaches disk.
 *
 *    The replacement is DETERMINISTIC and RELATION-PRESERVING: the same input
 *    always yields the same token, so a configuration profile still points at
 *    the security profile it pointed at, and a registration-table row still
 *    matches its interface. That is the whole reason the fixtures remain useful
 *    — anonymising each occurrence independently would destroy exactly the
 *    relational structure the collectors are being tested on.
 */

const fs     = require('node:fs');
const path   = require('node:path');
const crypto = require('node:crypto');

// This tool lives in the PORT repo, reads the LIVE one, and writes only into the
// port repo. The live repo is never opened for writing: it is the fallback, and
// a fallback with port artefacts in it is not one.
//
// MIKRODASH_SRC points at the MikroDash source — its collectors and routers.js
// are what actually get run. FIXTURE_OUT is where captures land. Both default to
// a side-by-side checkout and are overridden when this runs inside the app
// container, where the source is /app.
// path.resolve against the CWD, NOT used as given. require() resolves a relative
// path against the REQUIRING MODULE's directory, so a bare `../MikroDash` here
// means tools/../MikroDash — which does not exist, and every require() below
// throws MODULE_NOT_FOUND. It works today only because this tool normally runs
// inside the container with an absolute /app. This is the same trap that
// silently broke nodecheck/helpers/fixture-replay.js and tools/api-surface.js.
const ROOT = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT  = process.env.FIXTURE_OUT || path.join(__dirname, '..', 'testdata', 'fixtures');

// ── Anonymisation ────────────────────────────────────────────────────────────

// Fixed, so a re-capture of an unchanged router produces an identical fixture
// and shows up as no diff. Not a secret: the mapping is one-way and the value
// space is tiny, so it protects nobody if leaked. It exists for stability.
const SALT = 'mikrodash-fixture-v1';

const _h = (v, n) => crypto.createHash('sha256').update(SALT + String(v)).digest('hex').slice(0, n);

/**
 * Keys whose VALUE identifies a person, place or device.
 *
 * `name` IS DELIBERATELY ABSENT, and that is a considered trade-off rather than
 * an oversight. Interface and profile names are STRUCTURAL: this router's
 * radios are called "2.4GHz WiFi" and "5GHz WiFi", and wifi.js reads the band
 * out of exactly that when the interface, its channel profile and its frequency
 * all decline to say (`bandFromName`). Tokenising them produced a fixture on
 * which a real code path could not run — an anonymisation that destroys the
 * behaviour under test is worse than none, because it fails quietly and in the
 * direction of false confidence.
 *
 * So the line is drawn at what actually identifies a household: the SSIDs it
 * broadcasts, its serial numbers, its addresses, its country, and free text
 * somebody typed. A profile called "Home WiFi 5Ghz" stays.
 */
const IDENTIFYING_KEYS = new Set([
  'ssid', 'comment', 'identity', 'common-name',
  'serial-number', 'serial', 'host-name', 'hostname', 'country',
  'caps-man-addresses', 'requested-certificate', 'generated-certificate',
  'generated-ca-certificate', 'certificate', 'ca-certificate', 'user', 'owner',
  // ROUTER-GENERATED, OPERATOR-DERIVED. `steering.neighbor-group` came back as
  // "dynamic-Schutte WiFi-75146667" — RouterOS built it, so it is not free text
  // anybody typed, and learning could not replace it because the string it was
  // built from is not among this router's current SSIDs. A surname in a public
  // fixture all the same. Tokenising costs no join: the token is deterministic,
  // so a reference and its target still land on the same value.
  'neighbor-group',
  // ── VPN ENDPOINTS AND KEYS ─────────────────────────────────────────────────
  //
  // Found in the COMMITTED CORPUS, not in a fresh capture, which is the part
  // worth recording: `testdata/fixtures/.../vpn.json` carries
  // `endpoint-address: "780e06078119.sn.mynetname.net"`. That is MikroTik's
  // cloud DDNS name, it is derived from the router's SERIAL NUMBER, and it
  // resolves to the operator's current WAN address. The address beside it was
  // tokenised into TEST-NET-2 and the hostname was not — so the scrubber
  // removed the harmless half and kept the live handle on a private network.
  //
  // The operator's own domain and a GL.iNet DDNS name were in there for the
  // same reason: every rule here matched a KEY, and no rule matched a hostname.
  //
  // A WireGuard PUBLIC key is not a secret, so it escaped the credential rule —
  // but it is a stable unique identifier for a device, and beside an endpoint it
  // names real infrastructure. Tokenising costs no join, because the token is
  // deterministic and every reference to it lands on the same value.
  'endpoint-address', 'current-endpoint-address', 'endpoint', 'client-endpoint',
  'client-dns', 'public-key', 'client-public-key',
  // ── SCRIPTS THE OPERATOR WROTE ─────────────────────────────────────────────
  //
  // `netwatch.down-script` held ANOTHER ROUTER'S cloud DDNS name. Learning could
  // not reach it and never will: the scrubber learns what THIS router knows
  // about itself, and a name belonging to a different device is not in that set
  // — the same wall the kid-control note describes from the other side.
  //
  // A script body is free text somebody typed, which is precisely the rule
  // `comment` is already on. Nothing joins on it and no collector parses it;
  // netwatch reports up and down.
  'down-script', 'up-script', 'on-up', 'on-down', 'source',
  // The DoH endpoint. AdGuard issues a PER-SUBSCRIBER hostname —
  // `3b1ccdb7.d.adguard-dns.com` — so the leading label is an account
  // identifier, not a service name.
  'use-doh-server', 'doh-server',
]);

/**
 * Keys that are STRUCTURAL in most menus and IDENTIFYING in one.
 *
 * `name` is the whole reason this exists. It is deliberately absent from
 * IDENTIFYING_KEYS — an interface called "2.4GHz WiFi" is structural, wifi.js
 * reads the band out of it, and tokenising it produced a fixture on which a real
 * code path could not run. That reasoning is sound for every menu whose `name`
 * the ROUTER assigns.
 *
 * `/ip/kid-control/device` is a menu whose `name` the OPERATOR types. The first
 * talkers capture wrote sixty-two of them — "Bedroom Humidifier", "Bedroom TV",
 * "Body-Smart-24" — into a fixture bound for a public repository: a room-by-room
 * inventory of somebody's home. assertClean did not catch it, correctly, because
 * by its rules nothing was wrong.
 *
 * Learning did not catch it either, and that is the interesting part. The
 * scrubber learns this router's DHCP hostnames and replaces them as substrings,
 * which is why one row came back as `host-name-a7ba;4` — the mechanism worked
 * exactly as designed. It cannot work for a label that exists only in
 * kid-control and was never a DHCP hostname, because there is nothing to learn
 * it from.
 *
 * So the key alone cannot decide; the MENU decides. Nothing joins on these
 * names — talkers keys its device map by `mac-address` and passes `name`
 * straight to the payload — so tokenising costs no relation, and tokenising
 * rather than dropping keeps the collector on its real code path instead of its
 * unnamed-device fallback.
 */
const IDENTIFYING_KEYS_BY_MENU = [
  { menu: /^\/ip\/kid-control\/device/, keys: ['name'] },
  // THE SAME SITUATION, IN A SECOND MENU — which is the argument for expecting a
  // third. A WireGuard peer's `name` is typed by the operator, and the committed
  // corpus holds "Renier Phone" and a VPN provider's account label. Nothing joins
  // on it: vpn.js keys peers by `public-key` and passes `name` to the payload.
  //
  // The kid-control note explains why the key alone cannot decide and the menu
  // must. What it could not know is that `name` is operator-typed wherever a
  // human names a PEER rather than a router naming an interface — so this list
  // grows by inspection of each menu that reaches a fixture, and the corpus
  // deserves re-reading whenever a new collector is captured.
  { menu: /^\/interface\/wireguard\/peers/, keys: ['name'] },
  // THE THIRD MENU, which is the one that settles the pattern. A static DNS
  // entry's `name` is a hostname the operator chose to override, and the corpus
  // held `3b1ccdb7.d.adguard-dns.com` — an AdGuard per-subscriber endpoint whose
  // leading label is an account identifier — beside the streaming and CDN names
  // that together describe which services a household uses.
  //
  // `name` is operator-typed wherever a human names a THING rather than a router
  // naming an interface. Three menus now say so, so the next collector to reach
  // a fixture should be read with that question in hand rather than after the
  // fact.
  { menu: /^\/ip\/dns\/static/, keys: ['name'] },
  // THE FOURTH AND FIFTH, found the same way — by reading a golden before
  // porting its collector. `rosusers.json` carried the operator's own handle as
  // a RouterOS account name, in both `/user` and the `/user/active` sessions
  // beside it.
  //
  // An account name is typed by whoever created the account, which is the test
  // this list applies. `group` is deliberately NOT tokenised with it: group
  // names here are roles rather than people ("full", "read"), and leaving them
  // alone avoids the reference-key chase the header below rules out. The join
  // still holds because the token is derived from key AND value, so the same
  // name in `/user` and `/user/active` lands on the same token.
  { menu: /^\/user\/active/, keys: ['name'] },
  { menu: /^\/user\/print|^\/user$/, keys: ['name'] },
];

/** The identifying-key set in force for one menu. */
function identifyingKeys(cmd) {
  const extra = cmd && IDENTIFYING_KEYS_BY_MENU.find(m => m.menu.test(cmd));
  return extra ? new Set([...IDENTIFYING_KEYS, ...extra.keys]) : IDENTIFYING_KEYS;
}

// There is no REFERENCE_KEYS list, and there deliberately is not one.
//
// An earlier draft tokenised `name` and therefore had to tokenise every
// reference to a name — `configuration`, `security`, `master-interface`,
// `interface`, `bridge` — in the same namespace, or the fixture's rows stopped
// joining. That is a lot of machinery, and it fails silently the first time a
// reference key is forgotten. Leaving structural names alone removes the entire
// problem: a reference matches its target because neither one moved.

/**
 * Keys whose VALUE is a secret. These are DROPPED from a captured row, not
 * tokenised — a fixture has no use for a WireGuard private key, and the safest
 * thing to do with one is not to have it.
 *
 * ANCHORED AT THE END, which matters. A bare /password/ also matches
 * `minimum-password-length`, which is a policy number rather than a credential;
 * the first real run aborted the whole rosusers capture over it. Equally,
 * `public-key` must NOT match while `private-key` must — hence matching the
 * suffix rather than searching for a word.
 */
const CREDENTIAL_RE = /(passphrase|password|secret|psk)$|(private|pre-?shared)-key$/i;

const MAC_RE  = /\b([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}\b/g;
/**
 * A MAC WITH ITS SEPARATORS STRIPPED — twelve hex digits in a row.
 *
 * One got through without this. A log line read
 * `IoT DHCP assigned … for 8C:F6:81:B6:F9:4C shellytrv-8CF681B6F94C`: MAC_RE
 * anonymised the address and left the SAME ADDRESS, colon-less, inside the
 * device's own hostname two words later. The learner could not help — it
 * replaces hostnames read from the lease table, and that lease had expired — and
 * no key rule applies to free text.
 *
 * The all-decimal case is excluded: `281414601480` is a byte counter, not an
 * address, and twelve decimal digits are a perfectly good hex string.
 */
const BARE_MAC_RE = /\b(?![0-9]{12}\b)[0-9A-Fa-f]{12}\b/g;
const IPV4_RE = /\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b/g;
const IPV6_RE = /\b([0-9A-Fa-f]{0,4}:){3,7}[0-9A-Fa-f]{0,4}\b/g;

function fakeMac(v) {
  const h = _h(v, 8);
  // 02: locally administered, so a captured MAC can never collide with a real
  // vendor prefix and be mistaken for a device that exists.
  return ('02:' + h.slice(0, 2) + ':' + h.slice(2, 4) + ':' + h.slice(4, 6) + ':' +
          h.slice(6, 8) + ':' + _h(v + 'x', 2)).toUpperCase();
}

/**
 * The colon-less form of the SAME fake MAC the colon form would get.
 *
 * Deliberately not an independent token: the hostname suffix and the MAC field
 * in a DHCP log line are the same device, and the fixture rules keep structural
 * joins. Re-inserting the colons before hashing is what makes the two agree.
 */
function fakeBareMac(v) {
  const colonised = String(v).toUpperCase().match(/.{2}/g).join(':');
  return fakeMac(colonised).replace(/:/g, '');
}

// 198.51.100.0/24 is TEST-NET-2, reserved by RFC 5737 for documentation.
// Private-looking replacements would be indistinguishable from real LAN data.
const fakeIpv4 = (v) => '198.51.100.' + (parseInt(_h(v, 2), 16) % 254 + 1);
const fakeIpv6 = (v) => '2001:db8::' + _h(v, 4);          // RFC 3849 documentation range

/** A readable, stable token: `ssid-3f9a`, `name-71c2`. */
const token = (key, v) => key + '-' + _h(key + '|' + v, 4);

/**
 * Identifying values learned from this router, replaced wherever they appear.
 *
 * KEY-BASED SCRUBBING IS NOT ENOUGH, and the logs collector proves it. A log
 * line reads:
 *
 *   ":Info: 02:DB:D2:E6:78:22@5GHz WiFi3(Cyberdyne Systems) connected, -81"
 *
 * The SSID is in the middle of free text under the key `message`. No rule about
 * key names can catch that, and tokenising the whole message would destroy the
 * thing the fixture exists to test — the logs collector classifies by message
 * content.
 *
 * So the SSIDs are read from the router FIRST and replaced as substrings. The
 * message keeps its shape, its severity, its MAC (already anonymised) and its
 * interface name; only the SSID moves. Learned before any collector runs, so it
 * does not matter which order the captures happen in.
 */
const LEARNED = new Map(); // real value -> token

async function learn(ros) {
  if (LEARNED.size) return;
  const read = async (cmd, args) => {
    try { return (await ros.write(cmd, args || [])) || []; } catch (_) { return []; }
  };
  const add = (key, v) => {
    const s = String(v == null ? '' : v).trim();
    // Two characters or fewer would match half the file as a substring.
    if (s.length > 2) LEARNED.set(s, token(key, s));
  };

  for (const r of await read('/interface/wifi/print', ['=.proplist=configuration.ssid']))
    add('ssid', r['configuration.ssid']);
  for (const r of await read('/interface/wifi/configuration/print', ['=.proplist=ssid']))
    add('ssid', r.ssid);
  for (const r of await read('/interface/wireless/print', ['=.proplist=ssid']))
    add('ssid', r.ssid);
  for (const r of await read('/system/identity/print', []))
    add('identity', r.name);

  // DHCP hostnames, because a log line says "IoT DHCP assigned … for …
  // LG_Smart_Fridge2" and that is an inventory of somebody's house. They are
  // scrubbed under `host-name` when they arrive as a field; this catches the
  // same value embedded in free text.
  for (const r of await read('/ip/dhcp-server/lease/print', ['=.proplist=host-name,comment'])) {
    add('host-name', r['host-name']);
    add('comment', r.comment);
  }

  // ── THE DDNS NAME AND THE WIREGUARD KEYS ───────────────────────────────────
  //
  // Added after tools/fixture-scan.js found both in the COMMITTED corpus, in
  // places no key rule reaches:
  //
  //   netwatch.json  down-script   a script the operator wrote, with the DDNS
  //                                name inside it
  //   logs.json      message       a log line carrying a peer's public key
  //
  // The key list above now tokenises `endpoint-address` and `public-key` where
  // they arrive as FIELDS. That does nothing for either of the cases above, for
  // exactly the reason this whole mechanism exists — the value is in the middle
  // of free text. Learning is the half that reaches it.
  //
  // The DDNS name matters more than it looks. `780e06078119.sn.mynetname.net` is
  // derived from the router's SERIAL NUMBER and resolves to its CURRENT WAN
  // ADDRESS: it is a live handle on the network, and the address beside it was
  // already being tokenised into TEST-NET-2 while the name was not.
  //
  // A WireGuard PUBLIC key is not a secret — which is why the credential rule
  // let it past — but it is a stable unique identifier for a device.
  // HOSTNAMES ONLY from this menu. `public-address` is deliberately NOT learned:
  // the address scrubber already rewrites it to TEST-NET-2 and PRESERVES ITS
  // SHAPE, whereas learning it would substitute a `ddns-xxxx` token and hand a
  // collector that parses an IP something that is not one.
  //
  // /ip/cloud/print also returns `vpn-private-key`, `vpn-peer-private-key` and a
  // full WireGuard client configuration with a private key in it. No collector
  // reads this menu, so none of that reaches a fixture — and nothing here adds
  // it to the learned set either. Keep it that way: only the two name fields.
  for (const r of await read('/ip/cloud/print', [])) {
    add('ddns', r['dns-name']);
    add('ddns', r['vpn-dns-name']);
  }
  for (const r of await read('/interface/wireguard/peers/print', ['=.proplist=public-key,client-endpoint'])) {
    add('public-key', r['public-key']);
    add('endpoint', r['client-endpoint']);
  }
  for (const r of await read('/interface/wireguard/print', ['=.proplist=public-key']))
    add('public-key', r['public-key']);
}

/** Replace every learned value, longest first so a prefix cannot win. */
function replaceLearned(s) {
  if (!LEARNED.size) return s;
  let out = s;
  const byLength = [...LEARNED.keys()].sort((a, b) => b.length - a.length);
  for (const real of byLength) {
    if (out.includes(real)) out = out.split(real).join(LEARNED.get(real));
  }
  return out;
}

/**
 * Scrub one value. `key` is the RouterOS property it arrived under.
 *
 * Order matters: an identifying key is tokenised whole, because a value like
 * "Home WiFi 5Ghz" leaks straight through a MAC/IP pass. Everything else is
 * swept for embedded addresses, since those turn up inside free-form fields.
 */
/**
 * The property name an identifying-key match should be tested against.
 *
 * RouterOS reports an inherited value under a DOTTED key — `configuration.ssid`,
 * `configuration.country`, `security.authentication-types`. Matching the whole
 * key missed every one of them, and the first real capture wrote the operator's
 * actual SSIDs into a fixture bound for a public repository. Match the leaf.
 */
const leafKey = (key) => String(key).split('.').pop();

function scrub(key, value, keys = IDENTIFYING_KEYS) {
  if (value == null) return value;
  let s = String(value);
  if (!s) return s;
  const leaf = leafKey(key);
  if (keys.has(leaf)) {
    // Preserve a comma list's shape — `caps-man-addresses` and friends are
    // lists, and collapsing one to a single token would change the parse.
    if (s.includes(',')) return s.split(',').map(p => token(leaf, p.trim())).join(',');
    return token(leaf, s);
  }

  // Free text can carry an identifying value anywhere inside it — see LEARNED.
  s = replaceLearned(s);

  // ADDRESSES, IN ONE PASS. Done naively these fight each other: a MAC is
  // colon-separated hex groups, so the IPv6 pattern matches one — including the
  // fake MAC the previous replacement just wrote, which turned every radio-mac
  // into a 2001:db8:: address. Each match is parked behind a placeholder so no
  // later pattern can see, and therefore clobber, an earlier substitution.
  const parked = [];
  const park = (fn) => (m) => { parked.push(fn(m)); return ' ' + (parked.length - 1) + ' '; };
  return s
    .replace(MAC_RE,  park(fakeMac))
    .replace(BARE_MAC_RE, park(fakeBareMac))
    .replace(IPV6_RE, park(fakeIpv6))
    .replace(IPV4_RE, park(fakeIpv4))
    .replace(/ (\d+) /g, (_, i) => parked[Number(i)]);
}

/**
 * Scrub a command's PARAMETERS, not just its rows.
 *
 * A parameter carries operator data as readily as a row does: ping asks
 * `=address=9.9.9.9`, taken from the router record's pingTarget, and that is a
 * routable address in a fixture bound for a public repository. The standing
 * corpus guard in nodecheck caught it, which is the guard working — but it
 * caught it after the capture had already written the file, because nothing
 * scrubbed params on the way out.
 *
 * The empty key is deliberate: it has no leaf in IDENTIFYING_KEYS, so a param
 * takes the free-text path — learned values, then MAC/IPv6/IPv4 — and nothing
 * else. `=.proplist=name,address,type` is untouched, and so is
 * `=interface=2.4GHz WiFi`, because interface names are structural and no
 * address pattern matches one.
 */
const scrubParams = (params) => (params || []).map(p => scrub('', p));

function scrubRow(row, cmd) {
  const keys = identifyingKeys(cmd);
  const out = {};
  for (const [k, v] of Object.entries(row || {})) {
    // Dropped outright rather than tokenised. The VPN collector reads
    // /interface/wireguard/peers, whose rows carry `private-key`; the first run
    // aborted on exactly that, which was the safety net doing its job on a
    // public repository. Nothing downstream reads a secret, so removing it costs
    // the fixture nothing and removes the possibility entirely.
    if (CREDENTIAL_RE.test(leafKey(k))) continue;
    out[k] = scrub(k, v, keys);
  }
  return out;
}

/**
 * The last line of defence. Aborts the whole capture rather than writing a
 * fixture that might carry a secret — see the header.
 */
function assertClean(payload) {
  const flat = JSON.stringify(payload);
  // scrubRow should already have dropped these. This is the backstop: if one
  // survives, something bypassed the scrubber and the whole capture is refused
  // rather than written.
  for (const m of flat.matchAll(/"([^"]+)":/g))
    if (CREDENTIAL_RE.test(leafKey(m[1])))
      throw new Error('ABORT: a credential-shaped key reached the fixture: ' + m[1]);

  // A real 8..63-char WPA passphrase has no reliable shape, so key names are the
  // only dependable signal. Belt: refuse anything still looking like a real MAC.
  const stray = flat.match(/\b(?!02:)([0-9A-F]{2}:){5}[0-9A-F]{2}\b/i);
  if (stray) throw new Error('ABORT: an un-anonymised MAC reached the fixture: ' + stray[0]);

  // STRUCTURAL CHECK, not a denylist of things we happen to know are private.
  //
  // The first capture wrote real SSIDs because `configuration.ssid` did not
  // equal `ssid`. A scan for known-bad strings would not have caught that
  // either — it only caught it because a human went looking. So assert the
  // POSITIVE property instead: every value under an identifying key must be a
  // token this tool minted. Anything else means the scrubber did not run on it,
  // whatever the reason.
  // STREAMS ARE CHECKED TOO. This walked `exchanges` alone, which was right when
  // it was written and stopped being right the moment the recorder learned to
  // record streams: the regex backstops above see the whole document, but the
  // structural check — the one that actually caught the SSID leak — was skipping
  // every row that arrived on an =interval= channel. Those rows carry the same
  // SSIDs and MAC addresses as any other.
  const TOKEN = /^[a-z0-9-]+-[0-9a-f]{4}$/;
  const sources = [...(payload.exchanges || []), ...(payload.streams || [])];
  for (const ex of sources) {
    const keys = identifyingKeys(ex.cmd);
    for (const row of ex.rows || []) {
      for (const [k, v] of Object.entries(row)) {
        const leaf = leafKey(k);
        if (!keys.has(leaf) || !v) continue;
        const parts = String(v).split(',').map(s => s.trim()).filter(Boolean);
        for (const p of parts)
          if (!TOKEN.test(p))
            throw new Error('ABORT: ' + k + ' was not anonymised: ' + JSON.stringify(v) +
                            ' (in ' + ex.cmd + ')');
      }
    }
  }
}

// ── The recorder ─────────────────────────────────────────────────────────────

/**
 * Wrap a live ROS so every read is recorded, and no write can be issued.
 *
 * BOTH ros.write() AND ros.stream() ARE RECORDED. Streams used to be stubbed
 * out, on the reasoning that "a fixture is a snapshot, and the poll path reads
 * the same menus". For seven collectors that second clause is false and the
 * result was silent: `interfaceStatus.refreshNow()` — the method
 * collector-snapshot picks — only restarts its metadata streams and issues no
 * read at all, so the capture came back empty and ifStatus was written off as
 * "data arrives only on a stream". Its metadata arrives on
 * `/interface/print =interval=N` in BOTH modes; `streamMode:false` replaces only
 * the rates poll, and even that needs state the stream populates. There is no
 * pure-poll path to fall back on, so the recorder has to see streams.
 *
 * A `data` packet is ONE ROW, so a stream records exactly like a read: the
 * command, its parameters, and the rows that came back. Every row goes through
 * the same scrubber — stream rows carry SSIDs and MAC addresses just as read
 * rows do, and this repository is public.
 */
function record(ros, log, streams) {
  const realWrite = ros.write.bind(ros);
  ros.write = async (cmd, params) => {
    const tail = String(cmd).split('/').pop();
    if (['set', 'add', 'remove', 'move', 'enable', 'disable', 'unset'].includes(tail))
      throw new Error('capture is read-only; refusing ' + cmd);
    // A REFUSAL IS AN OUTCOME, AND IT IS RECORDED AS ONE.
    //
    // This used to log successes only, so a command the router refused was
    // simply absent from the fixture — and the replay, finding nothing, answered
    // `[]`. Those are different facts. `[]` means "the menu is there and it is
    // empty"; a refusal means "this build does not have that menu, stop asking",
    // and collectors branch hard on the difference. wireless is the live case:
    // it probes /caps-man/registration-table/print, the AX3 refuses it because
    // it runs no CAPsMAN, and on replay the empty answer meant _probeCAPsMAN
    // never latched off — so the replay exercised the wrong branch of a real
    // fork, indefinitely.
    //
    // The message is scrubbed like any other free text: RouterOS errors quote
    // back what was asked, and what was asked can name an interface or an
    // address.
    try {
      const rows = await realWrite(cmd, params);
      log.push({ cmd, params: scrubParams(params), rows: (rows || []).map(r => scrubRow(r, cmd)) });
      return rows;
    } catch (e) {
      const msg = String((e && e.message) || e);
      log.push({ cmd, params: scrubParams(params), rows: [], error: scrub('', msg) });
      throw e;   // the collector must see exactly what a router would have given it
    }
  };

  const realStream = ros.stream.bind(ros);
  ros.stream = (words, paramsOrCallback, callback) => {
    // Normalise exactly as ROS.stream does, so the key a fixture is recorded
    // under is the one the replay will look it up by. Two call shapes are in
    // use: stream(cmd, [params], cb) from interfaceStatus, and stream([cmd], cb)
    // from createListenRefresh.
    const wordsArr = Array.isArray(words) ? [...words] : [words];
    let cb = null;
    if (Array.isArray(paramsOrCallback)) wordsArr.push(...paramsOrCallback);
    else if (typeof paramsOrCallback === 'string') wordsArr.push(paramsOrCallback);
    else if (typeof paramsOrCallback === 'function') cb = paramsOrCallback;
    if (typeof callback === 'function') cb = callback;

    const entry = { cmd: wordsArr[0], params: scrubParams(wordsArr.slice(1)), rows: [] };
    streams.push(entry);

    // The callback shape. createListenRefresh only cares THAT something
    // arrived, so its rows are usually empty objects — recorded anyway, because
    // the count is what drives the refresh.
    const wrappedCb = cb ? (err, row) => {
      if (!err && row && typeof row === 'object') entry.rows.push(scrubRow(row, entry.cmd));
      return cb(err, row);
    } : null;

    const s = realStream(wordsArr, wrappedCb);
    if (!s || typeof s.on !== 'function') return s;

    // The event shape. Wrap `on` rather than the stream, so a handler
    // registered after this returns is still recorded.
    const realOn = s.on.bind(s);
    s.on = (ev, handler) => {
      if (ev !== 'data' || typeof handler !== 'function') return realOn(ev, handler);
      return realOn(ev, (packet) => {
        if (packet && typeof packet === 'object') entry.rows.push(scrubRow(packet, entry.cmd));
        return handler(packet);
      });
    };
    return s;
  };
  return ros;
}

// One definition of how to make a collector take a reading, shared with the
// replay helper — see tools/collector-snapshot.js.
const { MODULE_OF, BEGIN_WINDOW_MS, IO_LISTENERS,
        snapshot, begin, settle } = require('./collector-snapshot');

// Some collectors subscribe to io events (talkers and ping both call io.on), so
// the stub needs the listener surface as well as the emit one.
const FAKE_IO = {
  engine: { clientsCount: 1 },
  emit() {},
  ...IO_LISTENERS,
  to() { const c = { to: () => c, emit() {} }; return c; },
  sockets: { adapter: { rooms: new Map() } },
};

// Three intervals of the metaPollMs set below: a first delivery plus a repeat.
const STREAM_WINDOW_MS = 6000;

async function capture(routerLabel, collectorKey) {
  const Routers = require(path.join(ROOT, 'src', 'routers.js'));
  const ROS     = require(path.join(ROOT, 'src', 'routeros', 'client.js'));

  const all = Routers.loadAll();
  const router = all.find(r => r.label === routerLabel);
  if (!router) throw new Error('no router labelled ' + JSON.stringify(routerLabel));

  const file = MODULE_OF[collectorKey] || collectorKey;
  const Collector = require(path.join(ROOT, 'src', 'collectors', file + '.js'));

  const ros = new ROS({
    host: router.host, port: router.port || 8729,
    tls: router.tls === false ? false : { rejectUnauthorized: !router.tlsInsecure },
    username: router.username, password: router.password,
  });
  ros.connectLoop();
  await ros.waitUntilConnected(25000);

  // Learn this router's identifying values BEFORE the collector runs, so free
  // text captured by any collector is scrubbed regardless of capture order.
  await learn(ros);

  const log = [];
  const streams = [];
  record(ros, log, streams);

  // streamMode:false forces the poll path, which reads the same menus a stream
  // would and completes in one tick instead of waiting on an event.
  const state = {};
  // metaPollMs sets the `=interval=N` on a metadata stream. The default is tens
  // of seconds, which would make every stream capture that slow; 2s delivers
  // the same ROWS sooner, and a fixture is about row content rather than
  // cadence. pollMs stays long because the poll path only needs one tick.
  // defaultIf and pingTarget come from the ROUTER RECORD, exactly as index.js
  // resolves them (`r.defaultIf || cfg.defaultIf || 'ether1'`). traffic streams
  // `=interface=<defaultIf>` and ping asks `=address=<target>`, so a collector
  // handed neither subscribes to an interface called "undefined". Interface
  // names are preserved rather than tokenised — they are structural — so taking
  // the real one keeps the fixture consistent with the rest of the corpus.
  const c = new Collector({ ros, io: FAKE_IO, state, pollMs: 30000, metaPollMs: 2000,
                            streamMode: false,
                            defaultIf: router.defaultIf || 'ether1',
                            target: router.pingTarget || '1.1.1.1' });

  // A reading, and then — only if it engaged the router with nothing at all —
  // the same reading from a STARTED collector. See collector-snapshot.js for
  // why starting is the fallback and not the default: it can only make a
  // collector do more, so applying it everywhere would move all twenty-one
  // fixtures that already capture cleanly.
  //
  // "Produced nothing" is measured at the transport, like everything else here:
  // no read was logged and no stream was opened. A collector that read one row
  // is working and is left alone.
  let method = await snapshot(c);
  const idle = () => log.length === 0 && streams.length === 0;
  let started = null;
  if (!method || idle()) {
    started = await begin(c, () => !idle());
    if (started) {
      // A started collector reads on its own schedule — ping's start() fires a
      // three-second /tool/ping without awaiting it — so give it that schedule
      // before asking again.
      await new Promise(r => setTimeout(r, BEGIN_WINDOW_MS));
      method = (await snapshot(c)) || method;
    }
  }
  if (!method && !started) {
    try { c.stop(); } catch (_) { /* nothing started */ }
    ros.stop();
    return { unsupported: 'no snapshot method and no start() — nothing to drive' };
  }

  // Let the streams deliver before the log is closed.
  //
  // A stream answers on its own schedule, so a capture that closed as soon as
  // the snapshot method returned would record the subscription and none of its
  // rows. Three intervals at the 2s above: enough for a first delivery plus a
  // repeat, without making a capture of seven collectors take minutes.
  if (streams.length) await new Promise(r => setTimeout(r, STREAM_WINDOW_MS));

  // The same settle the replay runs — ONE DEFINITION, which is the whole point
  // of collector-snapshot.js. It takes any reading the streams cannot supply
  // (interfaceStatus's rates come from /interface/monitor-traffic, which it can
  // only ask about once the streams have said which interfaces exist) and then
  // builds a payload. Without it here the capture recorded the streams and none
  // of the reads that depend on them, and every rate in the fixture was 0.
  await settle(c);

  // Let fire-and-forget follow-ups land before the log is closed.
  //
  // A snapshot method does not necessarily await everything it starts: system's
  // _pollResourceOnce kicks off /system/routerboard, /system/license and the
  // update check without waiting for them. The capture finished first and
  // recorded only the resource read, so the replay — which has no network to
  // wait on — asked for three commands the fixture did not contain. The corpus
  // was incomplete in a way only the replay could reveal.
  await new Promise(r => setTimeout(r, 400));

  try { c.stop(); } catch (_) { /* not every collector needs stopping mid-capture */ }
  ros.stop();

  // A capture with nothing in it is a GAP, not a fixture, and an empty file in
  // the corpus reads as coverage that does not exist. Streams count now: a
  // collector whose data arrives only on a stream is exactly what this was
  // extended for, so "no reads" alone is no longer a reason to write nothing.
  const streamRows = streams.reduce((n, e) => n + e.rows.length, 0);
  if (log.length === 0 && streamRows === 0) {
    const drove = [method && method + '()', started && 'start()'].filter(Boolean).join(' + ');
    return { unsupported: drove + ' produced neither a read nor a stream row' };
  }

  const payload = {
    collector: collectorKey,
    router: { model: scrub('name', router.model || ''), osVersion: router.osVersion || '' },
    note: 'Captured from live hardware and anonymised. See tools/capture-fixtures.js.',
    exchanges: log,
    // Omitted entirely when a collector opened no stream, so every fixture
    // captured before streams were recorded stays byte-identical and the
    // --check gate does not light up for twenty collectors that did not change.
    ...(streams.length ? { streams } : {}),
  };
  assertClean(payload);

  const dir = path.join(OUT, scrub('name', routerLabel));
  fs.mkdirSync(dir, { recursive: true });
  const dest = path.join(dir, collectorKey + '.json');
  fs.writeFileSync(dest, JSON.stringify(payload, null, 2) + '\n');
  // Stream rows COUNT. Reporting only `log` made a stream-only capture — which
  // is the whole shape this tool was extended to handle — print as
  // "0 exchanges, 0 rows" next to a fixture holding twelve seconds of traffic
  // samples. A summary that reads as an empty capture for a good one is the
  // same failure as a silent gap, just louder.
  return { dest: path.relative(ROOT, dest), exchanges: log.length,
           rows: log.reduce((n, e) => n + e.rows.length, 0),
           streams: streams.length, streamRows };
}

// ── CLI ──────────────────────────────────────────────────────────────────────

const argv = process.argv.slice(2);
const arg  = (n) => { const i = argv.indexOf('--' + n); return i === -1 ? null : argv[i + 1]; };

(async () => {
  if (argv.includes('--list')) {
    const Routers = require(path.join(ROOT, 'src', 'routers.js'));
    for (const r of (Routers.loadAll()))
      console.log(r.label + '  ' + r.host + '  ' + (r.model || '') + '  ' + (r.osVersion || ''));
    return;
  }
  const routerLabel = arg('router');
  const collectors  = (arg('collector') || '').split(',').map(s => s.trim()).filter(Boolean);
  if (!routerLabel || !collectors.length) {
    console.error('usage: node tools/capture-fixtures.js --router "<label>" --collector wifi,capsman');
    console.error('       node tools/capture-fixtures.js --list');
    process.exit(2);
  }
  // Per-collector outcomes, never all-or-nothing: one collector that cannot be
  // snapshotted must not cost the other twenty-six.
  let captured = 0, skipped = 0, failed = 0;
  for (const key of collectors) {
    try {
      const r = await capture(routerLabel, key);
      if (r.unsupported) {
        skipped++;
        console.log('  SKIP  ' + key.padEnd(14) + r.unsupported);
        continue;
      }
      captured++;
      const parts = [r.exchanges + ' exchanges, ' + r.rows + ' rows'];
      if (r.streams) parts.push(r.streams + ' streams, ' + r.streamRows + ' stream rows');
      console.log('  ok    ' + key.padEnd(14) + r.dest + '  (' + parts.join('; ') + ')');
    } catch (e) {
      failed++;
      console.log('  FAIL  ' + key.padEnd(14) + String((e && e.message) || e));
    }
  }
  console.log('\n' + captured + ' captured, ' + skipped + ' skipped, ' + failed + ' failed');
})().catch(e => { console.error(String((e && e.message) || e)); process.exit(1); });
