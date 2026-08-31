'use strict';
/**
 * Attributes this port RENDERS and never READS.
 *
 * ── WHY, IN THREE DEFECTS ───────────────────────────────────────────────────
 *
 * `tools/wiring-audit.js` asks the same question of element IDs. Three defects
 * turned out to live in `data-*` attributes instead, where it structurally
 * cannot see them, and every one was a control that rendered, looked enabled,
 * and had nothing listening:
 *
 *   the reorder arrows      firewall.ts and capsman.ts drew `data-res-move`
 *                           buttons; the resource engine never read the
 *                           attribute. On Firewall the ORDER of a rule is what
 *                           the rule does.
 *   the schedule writes     Edit, Send now and Remove drawn and enabled; only
 *                           History was bound.
 *   the firewall sub-tabs   the page selected `[data-fwtab]` and the markup
 *                           carries `data-fw`, so Filter, NAT, Mangle and Raw
 *                           did nothing and only Filter could ever show.
 *
 * None is visible to a DOM gate either: the markup is byte-identical to the live
 * app's in all three cases. What differs is whether anything is listening.
 *
 * ── READ MEANS "THE NAME APPEARS", NOT "APPEARS BESIDE getAttribute" ────────
 *
 * The first version of this matched `getAttribute('data-x')` and friends, and
 * reported `data-contrast` and its neighbours as unread. They are read — through
 * a helper that takes the attribute NAME as a parameter, so the literal never
 * sits next to the call. Any complete quoted `'data-x'` anywhere in the source
 * counts, which over-approximates in the safe direction: a false "read" hides a
 * gap, so the rule errs toward considering an attribute read only when its name
 * is genuinely written down somewhere.
 *
 * A CSS attribute selector counts too. `data-nav` and `data-theme` exist for the
 * stylesheet and are never meant to reach script.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/attr-audit.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');

function readTree(dir, re, acc) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) { if (e.name !== 'node_modules') readTree(p, re, acc); continue; }
    if (re.test(e.name)) acc.push({ file: p, body: fs.readFileSync(p, 'utf8') });
  }
  return acc;
}
const tsFiles = readTree(path.join(ROOT, 'web', 'src'), /\.ts$/, []);
const ts = tsFiles.map((f) => f.body).join('\n');
const html = readTree(path.join(ROOT, 'web', 'src', 'ui'), /\.html$/, []).map((f) => f.body).join('\n');
const css = fs.readFileSync(path.join(ROOT, 'web', 'public', 'app.css'), 'utf8');

function scan(source) {
  const rendered = new Set();
  for (const m of source.matchAll(/\bdata-([a-z][a-z0-9-]*)\s*=/g)) rendered.add(m[1]);
  return rendered;
}
const rendered = new Set([...scan(ts), ...scan(html)]);
for (const m of ts.matchAll(/\bdata-([a-z][a-z0-9-]*)(?=["'\s>])/g)) rendered.add(m[1]);

const read = new Set();
for (const m of ts.matchAll(/['"]data-([a-z0-9-]+)['"]/g)) read.add(m[1]);
for (const m of ts.matchAll(/\[data-([a-z0-9-]+)[\]=^]/g)) read.add(m[1]);
for (const m of ts.matchAll(/dataset\.([a-zA-Z0-9]+)/g)) read.add(m[1]);
const styled = new Set([...css.matchAll(/\[data-([a-z0-9-]+)/g)].map((m) => m[1]));

const camel = (s) => s.split('-').map((p, i) => (i ? p[0].toUpperCase() + p.slice(1) : p)).join('');
const isRead = (a) => read.has(a) || read.has(camel(a)) || styled.has(a);

// A COLLAPSE IN DETECTION MUST FAIL, not quietly report nothing. The lifecycle
// check learned this: a pattern that stopped matching reported twenty-two
// healthy collectors as broken, and the same slip in the other direction here
// would report a clean sweep over an empty set.
if (rendered.size < 60) throw new Error('only ' + rendered.size + ' rendered attributes found — the scan broke');
if (read.size < 30) throw new Error('only ' + read.size + ' read attributes found — the scan broke');

// ── The record ──────────────────────────────────────────────────────────────
const UNSHIPPED = 'rendered by a page that is not in PORTED, so nothing binds it yet';
const MARKUP = 'in extracted markup for a feature this port has not taken on; nothing renders ' +
  'or reads it here, and the markup is verbatim so it cannot simply be deleted';


const EXPECTED = {
  // `data-alert-id` marks each row in the notification bell with the alert it
  // shows. NOTHING READS IT — not here, and not in the live app either: the
  // only occurrence there is the same interpolation in the same template
  // (public/app.js:3657), with no selector, no listener and no CSS rule.
  //
  // Reproduced rather than dropped, because it is the handle anything acting on
  // a single row would use, and a port that removed it would be quietly harder
  // to extend than the app it replaces. The per-row Acknowledge button carries
  // its own `data-ack`, which IS what a click would read.
  'alert-id': 'rendered by the live bell too and read by nothing there either; kept as the ' +
    'row handle rather than invented later',

  // `data-i` was recorded here for exactly one tick, while the town-search
  // list's markup was ported and its selection was not. Closed 2026-08-25: the
  // modal's wiring reads it. A ledger entry with a lifetime that short is the
  // system working — the gap was real, visible, and short-lived.
  // The Settings principals page and Routers: neither is in PORTED.
  // ── THE ACCESS MANAGEMENT WRITE CONTROLS: ALL CLOSED, 2026-08-28 ────────
  //
  // Worth keeping the history, because these entries were WRONG IN TWO
  // DIFFERENT WAYS before they were right:
  //
  //  1. They began as UNSHIPPED — "rendered by a page that is not in PORTED".
  //     That stopped being true on 2026-08-26, when `settings-principals.ts`
  //     mounted the card.
  //  2. The reason was then rewritten rather than the entries deleted, to "what
  //     they wait on now is the WRITE endpoints". That is the distinction this
  //     file has been caught on before: an audit notices a stale ENTRY and
  //     cannot notice a stale REASON.
  //
  // Both readings are now historical.
  //
  // `action`, `group-action`, `group-id`, `level`, `page-row`, `page-set`,
  // `role-del` and `role-edit` are all READ now, by `settings-principals.ts`'s
  // delegated listeners and its role matrix. The eleven principal write routes
  // are served and the three forms decide through `pages/principal-forms.ts`.
  // The audit refused to let the entries stand, which is what they were written
  // for.
  //
  // AND THE NINTH CLOSED ONE TICK LATER. `grant-del` was recorded here for
  // exactly one tick — "its create path is the next step, and the delete half
  // belongs with it" — and that next step landed. A ledger entry with a lifetime
  // that short is the system working: the gap was real, written down, and
  // short-lived.
  // The Routers PAGE is genuinely still unmounted, so this one is unchanged.
  'router-id': UNSHIPPED,
  // `site-action`, `site-id` and `site-router` WERE here, recorded UNSHIPPED
  // while the Sites card's renderers were ported and its listeners were not.
  // Closed 2026-08-26: `pages/settings-sites.ts` binds all three — the table's
  // delegated edit/delete click and the form's checked-device query. The audit
  // refused to let the entries stand, which is what they were written for.
  // Recorded blocker. Only SEND NOW is left: Edit was bound when the schedule
  // form landed (Part 43), and its `data-rs-edit` is read now.
  // ── MARKUP ONLY — BUT EACH ENTRY NOW SAYS WHY ─────────────────────────────
  //
  // This was one bucket of eleven under a single "Markup only" comment, and that
  // is exactly how `rutab` hid in it: an attribute naming a live CONTROL, in a
  // page that IS ported, sitting unremarked beside ten decorative ones. The
  // Router Users tabs were dead for as long as that entry read like the others.
  //
  // So an entry that names a CONTROL carries the feature it belongs to and
  // whether that feature is ported. Five of these sit on `<button>` elements;
  // all five were checked against `web/src` when `rutab` was found, and every
  // one belongs to a feature the port has not taken on. If a future entry names
  // a control whose feature IS ported, it is a dead control, not decoration.
  //
  // `card` and `dir` left this list when the Dashboard grid was wired (Part 63):
  // `data-card` is read by the remove-button handler and `data-dir` by the
  // resize handles.

  // CONTROLS, on features this port has not taken on:
  'bulk': MARKUP,            // Settings: the "All / none" bulk toggle
  // `grant-add`, `grant-role` and `grant-scope` were MARKUP — "a feature this
  // port has not taken on". Closed 2026-08-28 with `grant-del`: the editor's Add
  // button reads both pickers and posts to /api/grants.
  // `map-zoom` WAS here — "Routers: the map's zoom buttons (the CONNECTIONS map
  // is ported and wires its zoom by id instead)". Closed 2026-08-29:
  // `routers-map.ts` delegates on `[data-map-zoom]` for in, out, reset and
  // autoframe, so the attribute is read by this port now.
  // `profile` WAS here as MARKUP — "Settings: the poll-profile buttons", a
  // feature the port had not taken on. Closed 2026-08-28: `settings-poll.ts`
  // delegates on `.poll-profile-btn` and reads `dataset.profile` to decide which
  // preset to apply. Kept as a struck-through note rather than deleted, because
  // this ledger's value is that an entry leaving it is a fact about the port.
  'role-preset': MARKUP,     // Settings: the ROLE editor's presets — not the
                             // VIEW presets, which are ported and read
                             // `data-view-preset` correctly
  'val': MARKUP,             // Routers: the add/edit modal's Gbps/Mbps toggle
  // Decoration — these do not sit on anything clickable:
  'res-add-dynamic': MARKUP,
  // `rutab` WAS here, recorded as markup nothing reads. It was not decoration:
  // the Router Users tab buttons carry it, and the port queried the invented
  // `data-ru-tab` instead, so the tabs were dead. This audit had the fact and
  // nothing connected it to the behaviour — an entry saying "nothing reads this"
  // is worth a second look when the attribute names a CONTROL.
  'sev': MARKUP, 'unit-for': MARKUP, 'val': MARKUP,
};

const unread = [...rendered].filter((a) => !isRead(a)).sort();
const problems = [];
const missing = unread.filter((a) => !EXPECTED[a]);
if (missing.length) {
  problems.push('Rendered by this port and read by nothing — script or stylesheet. Each is a\n' +
    'control with no listener until proven otherwise, so bind it or record why not:\n' +
    missing.map((a) => '  data-' + a).join('\n'));
}
const closed = Object.keys(EXPECTED).filter((a) => !unread.includes(a));
if (closed.length) {
  problems.push('These entries are no longer unread — delete them so the record does not\n' +
    'outlive the thing it described:\n' + closed.map((a) => '  data-' + a).join('\n'));
}

// ── The gate proves it can still detect ─────────────────────────────────────
//
// A sweep that silently stopped matching would pass. So it is run once more over
// the real source PLUS a planted dead attribute, and must find it. This costs one
// extra scan and removes the failure mode the three defects above all shared:
// something that looks like it is working and is not.
const plantedRendered = new Set([...rendered, 'zz-planted-dead']);
const plantedUnread = [...plantedRendered].filter((a) => !isRead(a));
if (!plantedUnread.includes('zz-planted-dead')) {
  problems.push('SELF-CHECK FAILED: a planted unread attribute was not reported, so this sweep ' +
    'can no longer detect the thing it exists for.');
}

if (problems.length) {
  console.error('the attribute audit disagrees with its record:\n\n' + problems.join('\n\n') + '\n');
  process.exit(1);
}

const byReason = new Map();
for (const a of unread) byReason.set(EXPECTED[a], (byReason.get(EXPECTED[a]) || 0) + 1);
console.log('attribute audit clean: ' + rendered.size + ' rendered, ' + unread.length +
  ' unread and all recorded');
for (const [why, n] of [...byReason.entries()].sort((a, b) => b[1] - a[1])) {
  console.log('  ' + String(n).padStart(2) + '  ' + why.split(' — ')[0].slice(0, 84));
}
