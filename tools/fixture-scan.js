#!/usr/bin/env node
'use strict';
/**
 * Scan the COMMITTED corpus for values that cannot be tokens this project minted.
 *
 * WHY THIS EXISTS, AND WHY assertClean DID NOT CATCH IT. The capture tool's
 * assertClean is a positive check on KEYS: every value under an identifying key
 * must be a token. That is the right shape for what it guards, and it is blind
 * by construction to a value whose key nobody listed.
 *
 * `testdata/fixtures/.../vpn.json` carried
 * `endpoint-address: "780e06078119.sn.mynetname.net"` — MikroTik's cloud DDNS
 * name, derived from the router's SERIAL NUMBER and resolving to the operator's
 * WAN address. The IP beside it was tokenised into TEST-NET-2 and the hostname
 * was not: the scrubber removed the harmless half and kept the live handle. The
 * operator's own domain and a set of WireGuard public keys were there for the
 * same reason.
 *
 * So this checks the other axis — the SHAPE OF THE VALUE, wherever it sits:
 *
 *   - a hostname under a real public suffix cannot be a token, because every
 *     token this project mints is `<kind>-<hex>` and has no dots;
 *   - a long base64 run is a key of some sort, and keys identify devices.
 *
 * It is deliberately noisy about the second tier — a third-party service name
 * says which services a household uses, which is milder than a DDNS name and not
 * nothing. Each accepted value goes in ALLOWED below, so an acceptance is a
 * decision somebody wrote down rather than a silence.
 *
 *   node tools/fixture-scan.js
 */

const fs   = require('node:fs');
const path = require('node:path');

const TESTDATA = path.join(__dirname, '..', 'testdata');

// Suffixes that mean a name resolves on the public internet. `.lan`, `.local`,
// `.home` and `.arpa` are local-only and say nothing about who owns them.
const PUBLIC_SUFFIX = /\.(?:com|net|org|io|dev|app|co|uk|za|de|nl|se|us|info|biz|cloud|xyz|me)$/i;
const HOSTNAME = /\b(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z][a-z0-9-]*\b/gi;

// A WireGuard key is 44 characters of base64 ending in '='. Certificates and
// other long opaque runs land here too, which is the intent.
const KEYLIKE = /\b[A-Za-z0-9+/]{32,}={0,2}\b/g;

/**
 * A MAC ADDRESS WITH ITS SEPARATORS STRIPPED — twelve hex digits in a row.
 *
 * Added because one got through. A log line read
 * `IoT DHCP assigned … for 02:1A:CB:… shellytrv-8CF681B6F94C`: the capture tool
 * anonymised the MAC field, and the SAME DEVICE'S MAC — colon-less, inside its
 * own hostname — sat two words later untouched. Every existing rule missed it.
 * The MAC rule wants colons. The learner replaces hostnames it read from the
 * lease table, and this device's lease had expired, so it had never seen this
 * one. The key rule wants mixed case and length.
 *
 * A shape test catches it where a name test cannot, which is the argument this
 * scanner is built on. Twelve hex digits is not a common accident in this
 * corpus — RouterOS ids are `*1A` and short, and interface names are words.
 */
const BARE_MAC = /\b[0-9A-Fa-f]{12}\b/g;

/**
 * Values that have been looked at and accepted.
 *
 * Each entry is a decision. Adding one should mean somebody asked "does this
 * identify the operator, their hardware, or their household?" and answered no.
 */
// Accepts `--check` so the standing gate sweep, which runs every tool carrying
// that flag, includes this one. It was NOT in the sweep before, and had been
// failing unnoticed on a validator boundary case in the corpus — a scanner
// nobody runs is the same as no scanner.
const ALLOWED = new Set([
  // RFC 2606 reservations and this project's own placeholders.
  'example.com', 'example.org', 'example.net', 'host.lan', 'router.lan',
  // GitHub's asset CDN, in log lines about a script fetch. A shared hostname
  // every user of the site contacts: it says nothing about WHO fetched, and the
  // log lines around it are what the logs collector classifies on. Accepted
  // rather than tokenised, deliberately — tokenising it would edit the message
  // shape the fixture exists to exercise.
  'raw.githubusercontent.com',
  // A VALIDATOR BOUNDARY CASE, not a captured value. `a@b.co` is one of the
  // e-mail shapes `tools/report-history-cases.js` puts to the LIVE validator to
  // pin where it draws the line on TLD length — `a@b.c` and `a@b` sit either
  // side of it. It has to be a two-character TLD to test that boundary, and
  // every two-character TLD is a real ccTLD by definition, so there is no
  // reserved alternative that tests the same thing.
  //
  // It carries nothing about the operator: it is the shortest address the
  // validator accepts, chosen for its length. Accepted rather than tokenised
  // because tokenising would change the property under test.
  'b.co',
]);

/**
 * Is this base64-shaped run actually a key, rather than something that merely
 * uses the same alphabet?
 *
 * The first version flagged `interface/wifi/configuration/print` — a RouterOS
 * menu path, all lowercase and slashes, which is a perfectly good base64 string
 * and identifies nobody. It also flagged a run of `y`s from the audit
 * truncation test.
 *
 * A real key is high-entropy: mixed case AND digits, at the length WireGuard and
 * certificates actually use. That is a shape test rather than a name test, which
 * is the whole point of this scanner — but a shape test that matches ordinary
 * text is just noise, and noise is how a check gets ignored.
 */
function keyShaped(s) {
  return s.length >= 40 && /[A-Z]/.test(s) && /[a-z]/.test(s) && /[0-9]/.test(s);
}

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.name.endsWith('.json')) out.push(p);
  }
  return out;
}

/** Every string in a parsed JSON tree, with the key path that reached it. */
function strings(node, at = '', out = []) {
  if (typeof node === 'string') out.push({ at, value: node });
  else if (Array.isArray(node)) node.forEach((v, i) => strings(v, at + '[' + i + ']', out));
  else if (node && typeof node === 'object') {
    for (const [k, v] of Object.entries(node)) strings(v, at ? at + '.' + k : k, out);
  }
  return out;
}

const findings = new Map();   // value -> { kind, where:Set }

function note(kind, value, where) {
  if (ALLOWED.has(value.toLowerCase())) return;
  const f = findings.get(value) || { kind, where: new Set() };
  f.where.add(where);
  findings.set(value, f);
}

function main() {
  if (!fs.existsSync(TESTDATA)) {
    console.error('no testdata/ — nothing to scan');
    process.exit(1);
  }
  const files = walk(TESTDATA);
  let scanned = 0;

  for (const file of files) {
    let parsed;
    try { parsed = JSON.parse(fs.readFileSync(file, 'utf8')); }
    catch (_) { continue; }
    scanned++;
    const rel = path.relative(path.join(__dirname, '..'), file);

    for (const { at, value } of strings(parsed)) {
      for (const m of value.match(HOSTNAME) || []) {
        if (PUBLIC_SUFFIX.test(m)) note('hostname', m, rel + ' -> ' + at);
      }
      for (const m of value.match(KEYLIKE) || []) {
        if (!keyShaped(m)) continue;
        note('key', m.length > 60 ? m.slice(0, 57) + '...' : m, rel + ' -> ' + at);
      }
      for (const m of value.match(BARE_MAC) || []) {
        // Locally-administered runs are what the anonymiser MINTS, so a bare
        // one starting 02 is a token this project created rather than a real
        // address it failed to remove.
        if (/^02/i.test(m)) continue;
        // AT LEAST ONE HEX LETTER, or this rule flags byte counters: a
        // twelve-digit decimal is a perfectly good hex string and
        // `tx-byte: 281414601480` is not a MAC. The trade is explicit — an
        // all-decimal MAC (about one in two hundred) would pass — and it is the
        // right way round, because a rule that cries wolf on every interface
        // counter is a rule nobody reads.
        if (!/[a-fA-F]/.test(m)) continue;
        note('bare-mac', m, rel + ' -> ' + at);
      }
    }
  }

  if (!findings.size) {
    console.log('fixture-scan: ' + scanned + ' files clean');
    return;
  }

  const byKind = { hostname: [], key: [], 'bare-mac': [] };
  for (const [value, f] of findings) byKind[f.kind].push([value, f]);

  console.error('fixture-scan: ' + findings.size + ' value(s) across ' + scanned +
                ' files that cannot be a token this project minted.\n');
  for (const kind of ['hostname', 'key', 'bare-mac']) {
    if (!byKind[kind].length) continue;
    console.error(kind === 'hostname'
      ? '-- HOSTNAMES resolving on the public internet ------------------'
      : kind === 'key'
        ? '-- KEY-SHAPED values ------------------------------------------'
        : '-- MAC ADDRESSES with the separators stripped -----------------');
    for (const [value, f] of byKind[kind].sort((a, b) => a[0].localeCompare(b[0]))) {
      console.error('  ' + value);
      for (const w of [...f.where].sort().slice(0, 3)) console.error('      ' + w);
      if (f.where.size > 3) console.error('      ...and ' + (f.where.size - 3) + ' more');
    }
    console.error('');
  }
  console.error('Each is either an identifier that must be tokenised in ' +
                'tools/capture-fixtures.js\nand the fixture re-captured, or a decision to ' +
                'record in ALLOWED in this file.');
  process.exit(1);
}

main();
