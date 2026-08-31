#!/usr/bin/env node
'use strict';
/**
 * Compare the KEY SETS of control payloads between the live app and the port.
 *
 * ── THE GAP THIS FILLS ──────────────────────────────────────────────────────
 *
 * The port has strong differential gates on COLLECTOR payloads (make-golden +
 * TestGoldenPayloads), on page DOM (live-renderer.js) and on pure logic (the
 * *-cases.js generators). It had NONE on what the server sends in response to an
 * ACTION — `res:ok`, `res:error`, `*:caps`, `*:ok`, `*:applying`.
 *
 * Two defects were found there in two consecutive review ticks, both by reading
 * the two implementations side by side, and both invisible to every existing
 * gate:
 *
 *   `backups:diff`  emitted the wrong key set entirely, and answered a baseline
 *                   with an empty diff where live runs a real one.
 *   `res:ok`        lost `movedId` on undo and redo, so the Firewall page's
 *                   pulse — which exists to show WHICH row an undo moved —
 *                   silently stopped happening. Nothing failed.
 *
 * Neither produced an error.
 *
 * ── IT NOW CATCHES THE `res:ok` CASE. IT DID NOT UNTIL 2026-08-26 ───────────
 *
 * This tool was written for the `movedId` defect above, tested by re-introducing
 * that exact defect, and REPORTED NO DIFFERENCE. This header used to explain why
 * and call it structural: the UNION of keys per event keeps `movedId` alive
 * because the move handler still sends it, and — it claimed — "comparing the set
 * of distinct SHAPES instead fails identically, for the same reason".
 *
 * **That was true of a SET and false of a MULTISET, which was worth measuring
 * rather than reasoning about.** `res:ok` is emitted from five sites on each
 * side: TWO carrying `movedId` and THREE without, on BOTH. Drop the key from one
 * Go site and the counts become 1 and 4 against live's 2 and 3 — a difference a
 * multiset sees and a set cannot. Re-introducing the defect now fails this tool,
 * which is how the change was proved rather than argued.
 *
 * So the comparison is: the union of keys (as before), AND the multiset of
 * per-site shapes. Site COUNTS legitimately differ where the port consolidates
 * or splits handlers, so those are a ledger — `SITE_COUNTS` below — and an entry
 * needs a reason like every other ledger here.
 *
 * ── SO: WHAT A CLEAN RUN ACTUALLY MEANS ─────────────────────────────────────
 *
 * CAUGHT      a key the port invented (verified: an added key fails --check)
 *             a key missing from EVERY emit site of an event
 *             an event whose whole shape drifted, as `backups:diff` had
 *             a key missing from ONE site when another still sends it, PROVIDED
 *             the event is not in SITE_COUNTS (verified: the `movedId` defect
 *             fails --check)
 *
 * NOT CAUGHT  values, types, or which shape goes with which action
 *             a per-site drift in an event whose counts are ledgered
 *
 * A clean run means "no key is missing everywhere or invented anywhere". It is a
 * cheap net under a place that had none. **It is not a substitute for reading
 * the two emitters side by side, which is what found both defects above.**
 *
 * Events the port emits that the live app builds NON-LITERALLY (a variable, a
 * spread) cannot be compared and are listed rather than silently skipped.
 *
 *   node tools/payload-keys.js            report
 *   node tools/payload-keys.js --check    exit 1 on any difference not allowed below
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const INDEX = path.join(LIVE, 'src', 'index.js');
const GO_DIR = path.join(__dirname, '..', 'internal', 'server');

/**
 * Differences that are correct, each with the reason.
 *
 * An entry here is a claim that the port is RIGHT to differ. Adding one without
 * a reason defeats the file.
 */
// Events whose per-site SHAPE COUNTS differ legitimately, because the port
// consolidates or splits handlers the live app does not. The SHAPES still have
// to match; only the counts are excused, and an entry costs this event its
// per-site protection — so it is worth checking a difference is structural
// before recording it.
const SITE_COUNTS = {
  'packages:notes': 'the port emits the ERROR shape from ONE site where the live app emits it '
    + 'from two; the shape is identical. The live handler has a `_no(why)` helper for the two '
    + 'refusals and then an inline emit in its catch, because that one passes `sanitizeErr(e)`. '
    + 'This port routes the caught error through the same closure — `no(safe.Message(...))` — so '
    + 'all three refusals leave by one door. Mechanism, not payload',
  'queues:caps': 'the port emits from TWO sites where the live app emits from one; the shape is '
    + 'identical. Queues splits its capability announcement between the page load and the '
    + 'per-router refresh',
  'rosusers:ok': 'the port emits from THREE sites where the live app emits from five; the shape '
    + 'is identical. The generic RESOURCE write path serves several actions that the live app '
    + 'writes out one by one',
  'router:active': 'the port emits from ONE site where the live app emits from three; the shape '
    + 'is identical. The live app repeats the announcement at three points in the switch, and '
    + 'this port makes it once at the end',
  'router:status': 'counts differ AND so does the shape -- see the ALLOWED entry below, which '
    + 'records the extra `reason` key and why',
};

const ALLOWED = {
  // The RouterOS upgrade flow lives in the DASHBOARD's system card
  // (#sysUpdateAction), not the Packages page, and Dashboard is deliberately the
  // last page to port. Neither the Go server nor the ported page has an
  // `upgrade` action at all, so these keys belong to an unported feature.
  // `packages:applying`'s `upgrade` and `packages:ok`'s `latest` were allowed
  // here while only the live app emitted them: both keys are the UPGRADE path's,
  // and this port had `packages:apply` without `packages:upgrade`. Ported
  // 2026-08-25, so the allowances are gone and the key sets are compared whole.
  // A record that outlives its cause is the failure this file exists to catch.
  // RESOLVED, and the old note here was wrong on every count — it said the shell
  // was a stub, that the live app never surfaces a reason, and that this one can
  // name a host and port. Taking them in turn:
  //
  //   the shell        is ported (Parts 15-23): banners, nav, caps, clock,
  //                    keyboard, modals, stale detection, the account modal.
  //   never surfaces   is FALSE. The live app has TWO events. `ros:status` is
  //                    this session's RouterOS reachability and DOES carry a
  //                    reason, which its banner renders. `router:status` is a
  //                    global per-router announcement for the Routers list and
  //                    does not. This port emits ONE room-scoped `router:status`
  //                    doing the first job, so the key is present where the live
  //                    app puts it on the other event.
  //   host and port    was true and is fixed: `lastErr` is sanitised at the
  //                    point of storage (Part 28), so the reason cannot name an
  //                    address, a path, an e-mail or a token.
  //
  // The key difference is therefore real and intended, and this entry stays —
  // but as a record of one event doing two events' work, not of a leak.
  'router:status': { goOnly: ['reason'] },
};

function balanced(s, i) {
  let d = 0;
  for (let j = i; j < s.length; j++) {
    if (s[j] === '{') d++;
    else if (s[j] === '}') { d--; if (d === 0) return s.slice(i, j + 1); }
  }
  return null;
}

/**
 * Strip comments from an object literal, leaving strings alone.
 *
 * NOT COSMETIC. `topKeys` splits on commas at depth zero, and a comma inside a
 * line comment splits an entry in half — the live `wifiscan:interfaces` emit
 * carries "// May scan at all — the button is drawn from this, not from the
 * list." directly above `permitted:`, so that key was silently dropped from the
 * LIVE side and reported as "only in the port".
 *
 * A false difference is worse than a missed one here: it invites someone to
 * "fix" the port to match a payload the live app never sent.
 *
 * Strings are skipped so a `//` inside a URL or a message is not mistaken for a
 * comment.
 */
function stripComments(src) {
  let out = '';
  for (let i = 0; i < src.length; i++) {
    const c = src[i];
    if (c === '"' || c === "'" || c === '`') {
      const q = c;
      out += c;
      for (i++; i < src.length; i++) {
        out += src[i];
        if (src[i] === '\\') { i++; if (i < src.length) out += src[i]; continue; }
        if (src[i] === q) break;
      }
      continue;
    }
    if (c === '/' && src[i + 1] === '/') {
      while (i < src.length && src[i] !== '\n') i++;
      out += '\n';
      continue;
    }
    if (c === '/' && src[i + 1] === '*') {
      i += 2;
      while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) i++;
      i++;
      continue;
    }
    out += c;
  }
  return out;
}

/** Top-level keys of an object literal, ignoring nested ones. */
function topKeys(lit) {
  const body = stripComments(lit.slice(1, -1));
  const parts = [];
  let d = 0, buf = '';
  for (const ch of body) {
    if ('{[('.includes(ch)) d++;
    else if ('}])'.includes(ch)) d--;
    if (ch === ',' && d === 0) { parts.push(buf); buf = ''; } else buf += ch;
  }
  parts.push(buf);
  const out = [];
  for (const raw of parts) {
    const k = raw.trim();
    if (!k) continue;
    let m = /^["']?([A-Za-z_]\w*)["']?\s*:/.exec(k);
    if (m) { out.push(m[1]); continue; }
    if (/^\.\.\./.test(k)) { out.push('...spread'); continue; }
    if (/^[A-Za-z_]\w*$/.test(k)) out.push(k);   // shorthand
  }
  return out;
}

function collect(src, re, openAt) {
  const found = new Map();
  let m;
  while ((m = re.exec(src)) !== null) {
    const ev = m[1];
    const i = openAt(src, m);
    if (i < 0 || src[i] !== '{') continue;
    const lit = balanced(src, i);
    if (!lit) continue;
    if (!found.has(ev)) found.set(ev, { keys: new Set(), shapes: new Map() });
    const rec = found.get(ev);
    for (const k of topKeys(lit)) rec.keys.add(k);
    // THE PER-SITE SHAPE, counted. This is what separates a key dropped from ONE
    // emit site from one dropped everywhere -- see the header.
    const shape = [...topKeys(lit)].filter((k) => k !== '...spread').sort().join(',') || '(empty)';
    rec.shapes.set(shape, (rec.shapes.get(shape) || 0) + 1);
  }
  return found;
}

const liveSrc = fs.readFileSync(INDEX, 'utf8');
const live = collect(liveSrc, /\.emit\(\s*'([a-zA-Z]+:[a-zA-Z]+)'\s*,\s*/g, (s, m) => m.index + m[0].length);

let goSrc = '';
for (const fn of fs.readdirSync(GO_DIR)) {
  if (fn.endsWith('.go') && !fn.endsWith('_test.go')) {
    goSrc += fs.readFileSync(path.join(GO_DIR, fn), 'utf8') + '\n';
  }
}
const go = collect(goSrc, /hub\.Send\([^,]+,\s*"([a-zA-Z]+:[a-zA-Z]+)"\s*,\s*map\[string\]any\s*/g,
  (s, m) => s.indexOf('{', m.index + m[0].length - 1));

const shared = [...go.keys()].filter((e) => live.has(e)).sort();
const unmatched = [...go.keys()].filter((e) => !live.has(e)).sort();

let bad = 0;
const rows = [];
const siteRows = [];
const ledgered = [];
for (const ev of shared) {
  const lk = new Set(live.get(ev).keys); const gk = new Set(go.get(ev).keys);
  lk.delete('...spread'); gk.delete('...spread');
  const allow = ALLOWED[ev] || {};
  const liveOnly = [...lk].filter((k) => !gk.has(k) && !(allow.liveOnly || []).includes(k)).sort();
  const goOnly = [...gk].filter((k) => !lk.has(k) && !(allow.goOnly || []).includes(k)).sort();
  if (liveOnly.length || goOnly.length) {
    bad++;
    rows.push([ev, liveOnly.join(',') || '-', goOnly.join(',') || '-']);
    continue;
  }

  // ── THE PER-SITE COMPARISON ─────────────────────────────────────────────
  //
  // Reached only when the key UNION already agrees, so what it adds is exactly
  // the case the union cannot see: a key present at some emit sites and missing
  // at others. See the header for the measurement that made this possible.
  const ls = live.get(ev).shapes, gs = go.get(ev).shapes;
  const shapes = [...new Set([...ls.keys(), ...gs.keys()])].sort();
  const drift = shapes
    .filter((sh) => (ls.get(sh) || 0) !== (gs.get(sh) || 0))
    .map((sh) => '[' + sh + '] live x' + (ls.get(sh) || 0) + ' port x' + (gs.get(sh) || 0));
  if (drift.length) {
    if (SITE_COUNTS[ev]) {
      ledgered.push(ev);
    } else {
      bad++;
      siteRows.push([ev, drift.join('  ')]);
    }
  }
}

if (rows.length) {
  console.error('control payload key differences:\n');
  console.error('  %s %s %s', 'EVENT'.padEnd(22), 'MISSING FROM THE PORT'.padEnd(30), 'ONLY IN THE PORT');
  for (const r of rows) console.error('  %s %s %s', r[0].padEnd(22), r[1].padEnd(30), r[2]);
  console.error('\nEach is either a bug or an entry for ALLOWED with a reason.');
}
if (siteRows.length) {
  console.error('\nper-site payload differences (the key UNION agrees, the SITES do not):\n');
  for (const r of siteRows) console.error('  %s %s', r[0].padEnd(22), r[1]);
  console.error('\nA key present at some emit sites and missing at others. Either a bug — this '
    + 'is\nthe shape the `movedId` defect took — or an entry for SITE_COUNTS with a reason.');
}
console.log('%d event(s) compared, %d unexplained difference(s)', shared.length, bad);
if (ledgered.length) {
  console.log('site counts differ and are recorded: %s', ledgered.sort().join(', '));
}
if (unmatched.length) {
  console.log('not comparable (the live app builds these non-literally): %s', unmatched.join(', '));
}
// ── AN ALLOWANCE THAT IS NO LONGER NEEDED IS A FAILURE ─────────────────────
//
// `ALLOWED` excused a key difference per event, and nothing checked the entries:
// a name that is not an event at all was accepted silently, so an allowance
// could outlive the difference it was written for and read as "checked". Found
// on 2026-08-25 by probing every ledger in the repo with a synthetic entry.
const stale = [];
for (const [ev, allow] of Object.entries(ALLOWED)) {
  if (!shared.includes(ev)) {
    stale.push(ev + ' is not an event both sides emit');
    continue;
  }
  const lk = new Set(live.get(ev).keys); const gk = new Set(go.get(ev).keys);
  lk.delete('...spread'); gk.delete('...spread');
  for (const k of allow.liveOnly || []) {
    if (!lk.has(k) || gk.has(k)) stale.push(ev + '.liveOnly excuses ' + k + ', which is no longer live-only');
  }
  for (const k of allow.goOnly || []) {
    if (!gk.has(k) || lk.has(k)) stale.push(ev + '.goOnly excuses ' + k + ', which is no longer port-only');
  }
}
for (const ev of Object.keys(SITE_COUNTS)) {
  if (!shared.includes(ev)) {
    stale.push(ev + ' (SITE_COUNTS) is not an event both sides emit');
    continue;
  }
  const ls = live.get(ev).shapes, gs = go.get(ev).shapes;
  const same = [...new Set([...ls.keys(), ...gs.keys()])]
    .every((sh) => (ls.get(sh) || 0) === (gs.get(sh) || 0));
  if (same) {
    stale.push(ev + ' (SITE_COUNTS) excuses a count difference that no longer exists -- delete '
      + 'it, or this event silently loses its per-site protection');
  }
}
if (stale.length) {
  console.error('\nstale entries in ALLOWED or SITE_COUNTS — delete them:');
  for (const x of stale) console.error('  ' + x);
  bad += stale.length;
}
if (Object.keys(ALLOWED).length) {
  console.log('allowed differences: %s', Object.keys(ALLOWED).join(', '));
}
process.exit(process.argv.includes('--check') && bad > 0 ? 1 : 0);
