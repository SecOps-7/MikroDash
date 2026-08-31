'use strict';
/**
 * WHICH ELEMENTS A GATE ACTUALLY PROVIDES.
 *
 * ── WHY THE MODULE-LEVEL AUDIT WAS NOT ENOUGH ───────────────────────────────
 *
 * `page-gate-audit.js` answers "is this module exercised by a gate", and three
 * times that answered YES for a module whose page was almost entirely
 * uncompared:
 *
 *   - `vpn-card-check` provided ONE element (`vpnTable`) out of fourteen the
 *     same handler writes. The peer grid, both sub-tables, five stats and the
 *     page badge were invisible.
 *   - `fwlogs-cards-check` provided `dc-logs`; the Logs PAGE's view, its four
 *     severity badges and its controls were invisible.
 *   - Several dashboard-card gates are named after pages (`routing-cards-check`,
 *     `wireless-cards-check`, `bandwidth-card-check`) and cover the CARD.
 *
 * Each was found by hand, by noticing. This finds them by measuring: an element
 * a ported module WRITES, that no gate mentions, is uncompared.
 *
 * ── WHAT IT CANNOT SEE, STATED PLAINLY ──────────────────────────────────────
 *
 * It matches TEXT. An id named only in a gate's comment counts as provided —
 * the same false positive `dash-coverage-check` carries and documents — and an
 * id assembled at runtime is invisible. Both directions are wrong in principle;
 * the ledger below names the handful that affects.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/element-coverage-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const SRC = path.join(ROOT, 'web', 'src');
const TOOLS = path.join(ROOT, 'tools');

function walk(dir, out) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.name.endsWith('.ts')) out.push(p);
  }
  return out;
}

// Every id a ported module looks up. `el('x')` and `getElementById('x')` are the
// two spellings this codebase uses.
const writes = new Map();
for (const file of walk(SRC, [])) {
  const rel = path.relative(SRC, file).replace(/\.ts$/, '').split(path.sep).join('/');
  if (!rel.startsWith('pages/')) continue;
  const body = fs.readFileSync(file, 'utf8');
  const ids = new Set();
  for (const m of body.matchAll(/\bel(?:<[^>]*>)?\('([A-Za-z0-9_-]+)'\)/g)) ids.add(m[1]);
  for (const m of body.matchAll(/getElementById\('([A-Za-z0-9_-]+)'\)/g)) ids.add(m[1]);
  if (ids.size) writes.set(rel, ids);
}

// Everything any gate provides.
//
// TWO SOURCES, because one is not enough. A gate that lists its ids as literals
// is readable by a text scan; a gate that DERIVES them (`lift.idsFor`) is not,
// and text-scanning those reported four of my own gates as covering nothing.
// Those gates answer `--ids` instead, which is asking rather than guessing.
const provided = new Set();
const asked = [];
for (const f of fs.readdirSync(TOOLS)) {
  if (!f.endsWith('.js') || f === path.basename(__filename)) continue;
  const body = fs.readFileSync(path.join(TOOLS, f), 'utf8');
  if (body.includes("process.argv.includes('--ids')")) {
    const r = spawnSync(process.execPath, [path.join(TOOLS, f), '--ids'],
      { encoding: 'utf8', env: process.env });
    if (r.status === 0 && r.stdout.trim().startsWith('[')) {
      for (const id of JSON.parse(r.stdout)) provided.add(id);
      asked.push(f);
      continue;
    }
    throw new Error(f + ' declares --ids but did not answer with a list: ' +
      (r.stderr || r.stdout).slice(0, 200));
  }
  for (const m of body.matchAll(/'([A-Za-z0-9_-]{3,})'/g)) provided.add(m[1]);
  for (const m of body.matchAll(/"([A-Za-z0-9_-]{3,})"/g)) provided.add(m[1]);
}

// ── the ledger ──────────────────────────────────────────────────────────────
// Modules with no gate at all: their elements are uncovered by definition, and
// `page-gate-audit.js` already carries them. Listing them here too would double
// the same debt in two places and let one drift from the other.
// ── EMPTY, AND THAT IS THE POINT ────────────────────────────────────────────
//
// This held nine pages on 2026-08-25 — audit, bridges, capsman, connections,
// packages, rosusers, routing, topology, wifi — under the heading "no gate at
// all". EVERY ONE OF THEM HAD A GATE. They were excluded from the coverage
// figure anyway, so the headline number was computed over a subset that left out
// nine gated pages, and it read 95%.
//
// Nothing checked the entries because the loop consulted the set and never
// questioned it. Found by adding a name that is not a module and watching the
// run stay green — the same probe that found the PARTIAL counts inert an hour
// earlier, applied to every ledger in the repo.
//
// The set stays EMPTY. A module with no gate is `page-gate-audit`'s business and
// it carries that list; excluding pages here as well let one drift from the
// other, which is exactly what happened. If a genuinely ungated module ever
// needs to sit this out, the entries below it are checked in both directions
// now, so it will have to earn its place.
const NO_GATE_YET = new Set([]);

// Elements a gate deliberately does not provide, with the reason.
const EXEMPT = {
  // Driven through the port's own module, never looked up by a gate.
  'vpnPageGrid': 'compared via vpn-page-check by snapshot, not by name',
};

const problems = [];
let covered = 0, total = 0;
const gaps = [];

// ── NO_GATE_YET IS CHECKED IN BOTH DIRECTIONS TOO ──────────────────────────
//
// It skips modules that have no gate at all, so their elements are not counted
// as debt twice (`page-gate-audit` owns that list). Nothing checked the entries
// themselves: a module that GAINED a gate stayed here, silently excluding a
// gated page from the coverage figure — the same inert-ledger shape the PARTIAL
// counts had, found by adding a name that is not a module and watching the run
// stay green.
//
// A listed module is stale once ANY gate provides one of its elements.
for (const mod of NO_GATE_YET) {
  if (!writes.has(mod)) {
    problems.push(mod + ' is in NO_GATE_YET and is not a page module at all — remove it');
    continue;
  }
  const ids = writes.get(mod);
  if ([...ids].some((id) => provided.has(id))) {
    problems.push(mod + ' is in NO_GATE_YET but a gate now provides one of its elements — ' +
      'remove it so the page is counted, or narrow the gate that claims it');
  }
}

for (const [mod, ids] of [...writes].sort()) {
  if (NO_GATE_YET.has(mod)) continue;
  for (const id of [...ids].sort()) {
    total++;
    if (provided.has(id) || EXEMPT[id]) { covered++; continue; }
    gaps.push(mod + ' → #' + id);
  }
}

// A module whose gate covers FEWER THAN HALF its elements is the shape that hid
// the VPN and Logs pages. Reported separately from individual gaps, because one
// missing id is a detail and thirteen is a page nobody is testing.
const byMod = new Map();
for (const g of gaps) {
  const m = g.split(' → ')[0];
  byMod.set(m, (byMod.get(m) || 0) + 1);
}
// ── MODULES ALREADY KNOWN TO BE MOSTLY UNCOVERED ────────────────────────────
//
// Found by this audit on its first run, which is the point: the same shape hid
// the VPN and Logs pages and both were caught by hand, late. Each entry records
// the count, so the audit fails if a module gets WORSE, if a NEW one drops below
// half, or if one is fixed and the entry outlives it.
//
// These are real gaps, queued during the port. A narrow gate exists for each —
// `fw-tabs-check` covers the firewall's tabs, `ifports-panel-check` the
// Interfaces ports panel, `sched-*-check` two Reports dialogs — and a narrow gate
// passing is exactly what makes the rest invisible.
const PARTIAL = {
  // `pages/router-modal` was here at 7 of 8, described as a mapping artefact:
  // its gates declared the modal's ids but imported `router-form`, so the
  // coverage landed on that module instead. Closed 2026-08-26 —
  // `router-modal-picker-check.js` mounts THIS module and drives it, so the ids
  // are covered where they live. The entry was recorded as a NUMBER on purpose
  // ("if this module ever grows a decision of its own the count moves and
  // somebody has to look"), and the count moving is what surfaced this.
  // Still 3: `firewall-table-check.js` compares the TABLE, which is the whole
  // rule list, but the module also writes the action breakdown, the chain count
  // and the search box. A count is elements, not importance — the biggest one
  // here is covered and the number does not say so.
  // Down from 3 to 1: `fwChainCount` and then `fwActionList` both landed on
  // 2026-08-25, the second by driving `fwUpdateSummary`, which writes the action
  // breakdown AND the four per-table counts and then calls the chain count.
  //
  // `pages/firewall` used to sit here with 1 remaining — `fwSearch`, an INPUT,
  // on the reasoning that "there is nothing for a DOM comparison to hold". The
  // reasoning was about the wrong thing. Coverage is not "is this element in a
  // snapshot" but "can the gate tell if the page stopped using it", and every
  // firewall case sets that box and compares the rows it filters. The gate now
  // claims it and this entry is gone, which is the only way an entry here ever
  // goes.
  // 9 → 6 → 4 → 3. The type panel, both counts and `ifaceTypeFilter` are
  // compared or driven by `interfaces-page-check.js`; `#ifaceListBody` is now
  // driven by `iface-list-check.js`, which compares node IDENTITY and MOVE
  // COUNTS across frames rather than one frame's markup.
  //
  // This entry used to end "the one PARTIAL entry whose reason is still about
  // what a string-storing shim cannot hold". That reason EXPIRED when
  // `tools/lib/tree-shim.js` was built, and it sat here for part of a day saying
  // otherwise — a ledger entry outliving its problem, which is the failure this
  // audit's both-directions check exists to make visible.
  //
  // `pages/interfaces` is GONE from this list. 9 → 6 → 4 → 3 → 2 → 0, closed by
  // `interfaces-page-check` (type panel and counts), `iface-list-check`
  // (`#ifaceListBody`), `iface-tiles-check` (`#ifaceGrid`) and
  // `iface-view-check` (`#ifaceListWrap`, `#ifaceCardSize`). That is what fixing
  // an entry looks like: deleting it, not editing its number down.
  // ── REVEALED 2026-08-25, when NO_GATE_YET was emptied ──────────────────
  //
  // Both gates DO declare their ids; these are not undeclared coverage, they
  // are real debt that the exclusion hid.
  //
  // 13 → 10, and the remaining ten split into two honest halves rather than one
  // vague "not driven":
  //
  //   THREE are SVG and browser work — `worldMap`, `worldMapWrap`, `mapTooltip`.
  //   A string-storing shim cannot hold them and `tools/live-renderer.js` gates
  //   them against a running stack.
  //
  //   `sankeySvg` and `sankeyEmpty` LEFT this list on 2026-08-25. They were
  //   counted as browser work and are not: the diagram is built with
  //   `createElementNS` + `setAttribute`, which a shim that records the tree
  //   serialises exactly. `tools/sankey-check.js` compares 21 frames of it. The
  //   Sankey is CAPsMAN's shape a second time — a page built from two live
  //   blocks with only one lifted — and `live-renderer.js` had recorded both
  //   pages as such all along.
  //
  //   THE THREE ZOOM BUTTONS are wiring-checked, not covered.
  //   `tools/map-zoom-check.js` (2026-08-25) compares `attachMapZoom`'s wheel
  //   and drag arithmetic against the live app's — same factor, same rule — and
  //   checks textually that In sends a negative deltaY and Out a positive one.
  //   Their STEP SIZE is not compared: live clicks call `zoomAt(1.5)` where the
  //   port dispatches a 1.15 wheel notch, a mechanism difference the port rules
  //   allow. So the ids stay counted here, and that gate answers an explicit
  //   empty `--ids` to avoid claiming them.
  //
  //   TWO WERE not, and are now COVERED: `connFilterLabel` and `connSrcFilter`
  //   carry the rule that the country and source filters are MUTUALLY
  //   EXCLUSIVE, and `conn-filters-check.js` mounts the real page and pins it
  //   (2026-08-25). This entry said the gate "does not exist yet", which was the
  //   honest note that made it get written.
  //
  //   Mounting took three things, each found by a mount that died: a
  //   `createComment` stub for the map's placeholder, `#connMapList` as an
  //   IDENTITY-modelling node (`tree-shim`) because the page now calls
  //   `syncCountryList` on it, and a REJECTING `fetch` — the atlas load is
  //   `.catch`ed, so refusing it is a supported path rather than a stub of one.
  //   The mount asserts FOUR handlers and ZERO unknown ids, which is what makes
  //   it a complete shim rather than a quiet one.
  //
  //   THREE of the eight are a KNOWN, UNCHECKED WIRING SEAM rather than pure
  //   browser work: `mapFullscreenBtn`, `mapFsOverlay`, `mapFsClose`.
  //   `map-fs-check` drives `bindMapFullscreen` with synthetic nodes, so nothing
  //   proves `connections.ts` passes the right id into each slot — swapping
  //   `btn:` and `close:` passes every case there while making the fullscreen
  //   button close a map that is not open.
  //
  //   A textual contract check for it lives in `map-fs-check` now, and four
  //   mutations die on it — the slots swapped, an id misspelled, a slot dropped,
  //   the options object gone. It was written and REVERTED once on 2026-08-25
  //   because reading `connections.ts` made `page-gate-audit` credit that gate
  //   with GATING the page, whose `conn:update` wiring is not gated at all. That
  //   audit takes a `--not-gates` declaration now, checked in both directions,
  //   so a gate can read a module without claiming it.
  //
  //   The ids stay uncovered HERE on purpose: the wiring is checked, the
  //   elements' behaviour is not. `map-fs-check` answers an explicit EMPTY
  //   `--ids` to say so — a gate answering none is text-scanned, and the check
  //   writes the three ids as literals, so silence would have claimed them.
  //
  //   Nine remain, and all nine are the SVG world map and its fullscreen
  //   chrome — the genuine browser-geometry gap, not a gate nobody wrote.
  'pages/connections': 9,
  // Every `reports-*` module is GONE from this list — tables by
  // `reports-tables-check.js`, the schedule list by `sched-list-check.js`, the
  // date presets by `reports-presets-check.js` and the charts by
  // `reports-charts-check.js`. That is what fixing an entry looks like:
  // deleting it, not editing its number down.
  // `reports-traffic` is gone too: `reports-tables-check.js` now drives its
  // Bandwidth Usage sub-tab as well as the rate table.
};

// ── THE COUNT IS CHECKED FOR EVERY RECORDED MODULE, NOT ONLY THE BAD ONES ───
//
// This loop used to `continue` past anything less than half-uncovered BEFORE
// reading `PARTIAL`, so an entry stopped being checked the moment its module
// improved. `pages/reports` sat here saying 6 while the real number was 1, and
// `pages/interfaces` said 6 against 4 — both stale, both silent. A ledger that
// cannot fail is decoration, and this one was: setting either number to
// anything at all still passed. Measured on 2026-08-25 by doing exactly that.
//
// So the order is now: if the module is RECORDED, its number must be right.
// Only a module with no entry is judged by the half-uncovered threshold.
for (const [mod, missing] of byMod) {
  const size = writes.get(mod).size;
  const known = PARTIAL[mod];
  if (known === undefined) {
    if (missing / size > 0.5) {
      problems.push(mod + ': ' + missing + ' of ' + size + ' elements uncovered — a gate exists ' +
        'but covers a minority of the page. This is the shape that hid the VPN and Logs pages.');
    }
  } else if (missing > known) {
    problems.push(mod + ': uncovered elements rose from ' + known + ' to ' + missing +
      ' — the page grew faster than its gate');
  } else if (missing < known) {
    problems.push(mod + ': uncovered elements fell from ' + known + ' to ' + missing +
      ' — update the count in PARTIAL, or remove it if the gate now covers the page');
  }
}
for (const mod of Object.keys(PARTIAL)) {
  if (!byMod.has(mod)) problems.push(mod + ' is in PARTIAL but is now fully covered — remove it');
}
for (const id of Object.keys(EXEMPT)) {
  const used = [...writes.values()].some((s) => s.has(id));
  if (!used) problems.push('#' + id + ' is exempt but no module writes it — remove the entry');
}

// `--list` prints the tolerated gaps. They are only shown on FAILURE otherwise,
// so a standing set of individually-uncovered elements is invisible in a clean
// run — which is how 44 of them accumulated without anyone looking at the list.
if (process.argv.includes('--list')) {
  say('%d individually uncovered element(s):', gaps.length);
  for (const g of gaps) say('  ' + g);
}

if (problems.length) {
  shout('element-coverage-audit: %d problem(s)\n', problems.length);
  for (const p of problems) shout('  - ' + p);
  if (gaps.length) {
    shout('\nuncovered elements:');
    for (const g of gaps.slice(0, 40)) shout('    ' + g);
    if (gaps.length > 40) shout('    … and ' + (gaps.length - 40) + ' more');
  }
  process.exit(1);
}
say('element-coverage-audit: asked %d gate(s) directly; %d of %d elements in gated modules are provided by a gate (%d%%); ' +
    '%d individually uncovered; %d module(s) with a recorded uncovered count',
  asked.length, covered, total, Math.round((covered / total) * 100), gaps.length,
  Object.keys(PARTIAL).length);
