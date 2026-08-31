'use strict';
/**
 * Elements a ported page RENDERS but the port never touches.
 *
 * ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
 *
 * Four defects have now been found in work already marked done, and all four
 * were the same shape: markup extracted correctly, a payload gate passing, a
 * screenshot of a healthy system looking right — and an element the live app
 * writes to that the port never mentions. Three slider labels. Thirteen alert
 * toggles. Five banner behaviours. A modal-close list whose justification
 * comment was simply wrong.
 *
 * None of those is findable by reading code hoping to notice. All four ARE
 * findable by asking a question with a bounded, enumerable answer: for each
 * ported page, which ids in its extracted markup does `public/app.js` reference
 * and this port not?
 *
 * ── WHAT A HIT MEANS ────────────────────────────────────────────────────────
 *
 * Not automatically a bug. A page can legitimately ship before one of its
 * sub-features. What a hit means is that a DECISION is owed: port it, or write
 * down why not. `EXPECTED` below is that record — every entry names the thing
 * and the reason, and an id that is NOT in it fails this gate.
 *
 * The check runs in both directions on purpose. An `EXPECTED` entry that stops
 * being a gap — because the port now touches it — is also a failure, so closing
 * one of these forces the note to be deleted rather than left lying, exactly as
 * `KNOWN_INCOMPLETE` does in caps-check.js.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/wiring-audit.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const live = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

/** Which pages this port actually ships, read from main.ts rather than retyped. */
const mainTs = fs.readFileSync(path.join(ROOT, 'web', 'src', 'main.ts'), 'utf8');
const portedMatch = mainTs.match(/const PORTED = new Set\(\[([\s\S]*?)\]\)/);
if (!portedMatch) throw new Error('cannot find PORTED in web/src/main.ts');
const PORTED = new Set([...portedMatch[1].matchAll(/'([^']+)'/g)].map((m) => m[1]));
if (PORTED.size < 10) throw new Error('PORTED parsed as ' + PORTED.size + ' pages — the shape changed');

/**
 * Everything the APP DOCUMENT's own source mentions, in one string.
 *
 * ── `web/src/entry/` IS EXCLUDED, AND THE REASON IS A REAL COLLISION ───────
 *
 * This audit asks "does the port touch every element the live app's page has",
 * and it answers by looking for the id anywhere in `web/src`. That was fine
 * while every module belonged to one document. It stopped being fine when
 * `login.ts` arrived: `login.html` has a `#setupError`, and so does the ROUTER
 * first-run overlay in `index.html` — the SAME ID IN TWO DOCUMENTS. `setupError`
 * was a recorded gap in the overlay, and one mention from a different page's
 * script made the record read as closed.
 *
 * So the scan is scoped to the app's own modules. `web/src/entry/` holds the
 * bundles for the other documents; see its README.
 */
function readAll(dir, acc) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) {
      if (e.name !== 'node_modules' && e.name !== 'entry') readAll(p, acc);
      continue;
    }
    if (/\.(ts|js|mjs)$/.test(e.name)) acc.push(fs.readFileSync(p, 'utf8'));
  }
  return acc;
}
const port = readAll(path.join(ROOT, 'web', 'src'), []).join('\n');

/**
 * Ids are not always written out.
 *
 * Both sides build some by concatenation — `$('s_' + key)`, `el('resf_' + name)`
 * — and a grep for the literal cannot see those. That matters in the DANGEROUS
 * direction: if the live app only ever reaches an element through a built id,
 * `mentions(live, …)` is false, the id is skipped entirely, and a genuine gap is
 * never reported. The port side has the mirror problem, where it produces a
 * false gap instead.
 *
 * So the prefixes are collected from each side and treated as a mention of
 * anything beginning with one. That OVER-approximates, which is the safe
 * direction here: an over-approximated live mention means the id is considered
 * rather than skipped, and an over-approximated port mention means a real gap
 * has to be found by something else — which the empty EXPECTED record would then
 * make visible as a deleted entry.
 *
 * An EMPTY prefix would match every id and neuter the check entirely, so each
 * one has to be a KNOWN site. There is one, and it is harmless — its ids are
 * literals at the call site, so the ordinary grep already sees them. A new one
 * stops this audit rather than quietly widening it into a no-op.
 */
const BARE_VARIABLE_SITES = {
  'var b=$(\'\'+barId),cn=$(\'\'+cntId);':
    'the wireless signal-health bars. `setSig` takes the ids as PARAMETERS and every ' +
    'call site passes a literal — `setSig(\'wlSigBarE\', \'wlSigCntE\', …)` — so the ' +
    'plain grep finds them anyway and nothing is hidden by the indirection',
};

function dynamicPrefixes(hay, what) {
  const out = new Set();
  // THREE FORMS, because the first version of this matched single quotes only —
  // and the probe that was supposed to prove it worked used double quotes and
  // sailed straight past. A concatenation, a double-quoted concatenation, and a
  // template literal are the same construct written three ways, and a check that
  // knows one of them is a check that reports what it recognises.
  const forms = [
    /(?:\$|getElementById|\bel)(?:<[^>]*>)?\(\s*'([A-Za-z0-9_-]*)'\s*\+/g,
    /(?:\$|getElementById|\bel)(?:<[^>]*>)?\(\s*"([A-Za-z0-9_-]*)"\s*\+/g,
    /(?:\$|getElementById|\bel)(?:<[^>]*>)?\(\s*`([A-Za-z0-9_-]*)\$\{/g,
  ];
  for (const m of forms.flatMap((re) => [...hay.matchAll(re)])) {
    if (m[1] !== '') { out.add(m[1]); continue; }
    // Find the whole line, so the record names something a reader can locate.
    const start = hay.lastIndexOf('\n', m.index) + 1;
    const end = hay.indexOf('\n', m.index);
    const line = hay.slice(start, end === -1 ? undefined : end).trim();
    if (!BARE_VARIABLE_SITES[line]) {
      throw new Error(what + ' builds an element id from a bare variable, so no prefix bounds it ' +
        'and this audit cannot tell which ids are reachable:\n  ' + line +
        '\nAdd it to BARE_VARIABLE_SITES with the reason it is safe, or give it a prefix.');
    }
  }
  return [...out];
}

function mentioner(hay, what) {
  const prefixes = dynamicPrefixes(hay, what);
  return (id) => hay.includes("'" + id + "'") || hay.includes('"' + id + '"') ||
    prefixes.some((p) => id.startsWith(p));
}

// ── The record ──────────────────────────────────────────────────────────────
//
// id -> why the port does not touch it. Grouped by cause, because they came in
// groups: one absent block usually explains a whole column of the report.
// ── THE SHELL'S GROUPS ──────────────────────────────────────────────────────
//
// Added 2026-08-25 when the sweep first reached `shell.html`. Ninety-eight ids
// across six features, and they arrive as whole blocks because each is one
// unported module. Two were NOT in a block and both were real defects — the
// desktop router dropdown and the mobile burger — which is the argument for
// listing the blocks rather than silencing the file.
// RTRMODAL covered 32 ids and is GONE. Thirty-one closed on 2026-08-25 when the
// dialog was wired. The thirty-second, `rtrStatusDot`, was never part of the
// modal at all — it is the topbar status dot, paired with `navRtrStatusDot` and
// driven by the same event, and my prefix-bucketing had mis-grouped it. Wired
// with its twin the same tick.
//
// A group named by PREFIX will do that: `rtr*` is not a feature, it is a naming
// convention, and one id in thirty-two belonged to something else entirely.
// FA WAS HERE — the Frequency Analyser dialog, 15 ids and this port's largest
// recorded wiring gap. It was narrowed three times as its halves landed (the
// pure parsers, then the runner, then the collector's catalogue) and deleted on
// 2026-08-26 when the dialog itself shipped. Each narrowing was forced by this
// audit refusing a reason that had stopped being true.
const SETUP = 'the first-run ROUTER overlay. It runs when no router is configured at all, which '
  + 'is a state this port cannot reach during coexistence: the Node app owns the store and a fresh '
  + 'install is set up there. Its Connect button posts /api/routers (routers.json, which Node '
  + 'caches) and then /api/routers/:id/activate (UNPORTED, and it writes settings.json via '
  + 'switchRouter) — two recorded cutover blockers, so the WIRING waits whatever else is done. '
  + 'Narrowed 2026-08-28: the DECISIONS are ported and gated (web/src/pages/setup-overlay.ts, '
  + 'tools/setup-overlay-check.js), which is why the six connection fields left this record. What '
  + 'remains here is genuinely untouched.';
const USERNOTIFY = 'the per-user notification settings — email, ntfy, Pushbullet and Telegram, each '
  + 'with a test button. A form that saves settings nothing reads would be worse than an absent one, '
  + 'and nothing reads them yet: the TRANSPORTS are unported (they are outbound HTTP and SMTP). '
  + 'Narrowed 2026-08-25 — this said the blocker was the alerter, and the parts of it that decide '
  + 'WHERE a user\'s alerts go are ported now (`internal/notify`: the allowlist, the install-mail '
  + 'fold, the channel test). What is missing is the sending, and the test button needs exactly '
  + 'that.';
// UPGRADE was here, covering the nine `upd*` ids. Closed 2026-08-25:
// `web/src/pages/upgrade.ts` drives the dialog and `packagesUpgrade` answers it.
// The three records that pointed at each other — this one, template-id-audit's
// `sysUpdateAction` and inbound-audit's `packages:upgrade` — all went in the
// same tick, which is what a cross-referenced ledger is for.
const NOTIFPANEL = 'the notification bell\'s two WRITE controls. The panel itself shipped on '
  + '2026-08-26 — it reads the feed, ages every row and lights the dot, gated by '
  + 'tools/notif-bell-check.js against the live renderer. What is missing is where a click GOES: '
  + 'the live app POSTs to /api/alerts/:id/ack and /api/alerts/clear-all, and neither route is '
  + 'ported. An earlier draft emitted socket events instead and inbound-audit refused them — that '
  + 'was a protocol this port invented, and the live app has no such inbound action at all. So the '
  + 'controls stay unbound rather than wired to something that would do nothing.';
// SWITCHOVL was here, covering `rtrSwitchingOverlay` and `rtrSwitchingLabel`.
// Closed 2026-08-25: `main.ts` opens the overlay at the moment it asks for the
// switch — this port emits no `router:switching`, so the client's own request
// IS the moment — and `overlayOnStatus` in router-dropdown.ts carries the
// second-false rule.

const EXPECTED = {
  // ── THE FLEET MAP'S IMPERATIVE HALF — CLOSED 2026-08-29 ───────────────────
  //
  // `rtrMapViewport`, `rtrMapPop` and `rtrMapAutoFrame` were here while
  // `app.js:11432-11941` (510 lines, brace to brace) had no port. The PURE half
  // had been done for some time — `project`, `layout`, `popHtml`, `groupPopHtml`,
  // `renderTray`, `clampTranslate` and `fitToMarkers` in `routers.ts` — and what
  // was missing was the SVG construction and the gestures, which is why exactly
  // three ids remained: the viewport it draws into, the popover it positions and
  // the Auto Frame button it toggles.
  //
  // `web/src/pages/routers-map.ts` is that half. The entry went the way this
  // audit is built to make them go: the port started touching the ids and the
  // record failed for describing a state the code was no longer in.
  //
  // The recorded DIVERGENCE went with it — `applyView` no longer rewrites a
  // stored 'map' to 'comfortable', and `renderRoutersStats` no longer throws on
  // it. Both existed only while the view could not be drawn.
  // The six `wanWarn*` ids were recorded here while the WAN page was read-only.
  // They were DELETED on 2026-08-24 when renew/release and the self-cutoff
  // dialog landed — and this audit is what said so: the port started touching
  // them and the record failed in the direction that catches an entry outliving
  // its problem. That is the half of a ledger that is easy to leave out and the
  // only half that keeps one honest.

  // NOT the "Firewall Analyser" — an earlier version of this record said that and
  // it was simply wrong. `fa` is the FREQUENCY Analyser: a WiFi channel scan that
  // takes the chosen radio off the air and drops every client on it. It is
  // server-side-first (`src/wifiScan.js`, 335 lines, no Go equivalent), so the
  // button cannot ship before the thing it triggers.
  // `faOpenBtn` was recorded here for the same reason and went with it.

  // ── shell.html ────────────────────────────────────────────────────────────

  // rtrmodal (32)
  
  
  
  
  
  
  
  
  
   

  // upd_notes (1) — arrived 2026-08-27 with upstream `5531371`, and CLOSED
  // 2026-08-28 when `packages:notes` was ported end to end. Its entry deferred
  // to "the upgrade dialog group", which had been closed three days earlier —
  // three ledgers pointing at each other, none of them checking. This audit
  // failed on the entry the moment the port touched the id, which is the half
  // that keeps a record honest.
  // fa (15)
  // Fourteen `fa*` ids were recorded here as the Frequency Analyser's gap. The
  // dialog shipped on 2026-08-26 and this audit refused every entry as it became
  // untrue, one sweep at a time.
  //
  // ONE REMAINS, and it is a real gap rather than a leftover:
  // `faSpectrum` WAS here — "the spectrum CANVAS: constructing the chart against
  // the element and registering the band plugin, which needs a browser and
  // Chart.js rather than a decision". Closed 2026-08-29 by
  // `web/src/pages/wireless-fa-chart.ts`, which is exactly that and nothing
  // more: every decision it uses was already ported and gated.
  //
  // Writing it surfaced TWO CALLBACKS THAT HAD NEVER EXISTED. `spectrumConfig`
  // declares `legendLabels` and `legendClick` as dependencies, and
  // `tools/fa-chart-check.js` supplies its own stubs for them — correct for what
  // that gate asks, and it meant the real implementations were never written and
  // never missed. They are `faLegendLabels` and `faLegendClick` now, gated by
  // `tools/fa-legend-check.js`.
  //
  // IT NEVER STARTS A SCAN. This item carried the warning that a spectral scan
  // takes a radio off the air and drops every client on it; that is the scan
  // button's request, not the canvas's.

  // setup — WAS 14, then 8, now NONE. `web/src/pages/setup-overlay-wire.ts`
  // (2026-08-29) binds the overlay, both buttons, the error and result lines and
  // the three non-re-locking fields, so every id this record held is touched.
  //
  // MENTIONED IS NOT REACHABLE, and that split is recorded where it belongs:
  // this audit answers "does the port touch this element", and
  // `reachable-audit` answers "is the module in the bundle". The wire module is
  // recorded THERE as deliberately inert, because
  // `POST /api/routers/:id/activate` is not ported — measured, 404 — and
  // mounting a Connect button whose second request cannot succeed would leave a
  // first-run install with a router added and not selected.
  //
  // The same split is already written down for `pages/router-modal`, whose 32
  // ids read as bound while nobody could open it.

  // The twenty-one `un*` ids were recorded here as the per-user notification
  // gap. The tab shipped on 2026-08-26 — the transports, the storage, the three
  // routes and then the page — and this audit refused each entry as it stopped
  // being true.


  // notifpanel (5)

};

// ── The sweep ───────────────────────────────────────────────────────────────
const mentionsLive = mentioner(live, 'the live app');
const mentionsPort = mentioner(port, 'this port');

// THE SHELL IS SWEPT TOO, and unconditionally — it is not a page and is not in
// PORTED, but it ships with every ported page, so its controls are as live for a
// user as any page's.
//
// IT WAS NOT SWEPT UNTIL 2026-08-25, and the filter below is why: `page-*.html`
// excluded `shell.html` by construction, so 193 ids were outside every audit
// here. What was hiding in them was the DESKTOP ROUTER SWITCHER — the topbar
// dropdown is the visible control on desktop, the port wired only the mobile
// `<select>` (which is `display:none` there), and for several iterations a
// desktop user could not change routers on any page. Nothing failed.
function uiFiles() {
  const out = [];
  for (const file of fs.readdirSync(path.join(ROOT, 'web', 'src', 'ui')).sort()) {
    if (file === 'shell.html') { out.push(['shell', file]); continue; }
    const m = file.match(/^page-(.+)\.html$/);
    if (m && PORTED.has(m[1])) out.push([m[1], file]);
  }
  return out;
}

const found = [];
// COUNTED, so a clean run can say what it EXAMINED rather than only what it
// found. See the summary below for why that distinction earned a counter.
let pagesExamined = 0;
let idsExamined = 0;
for (const [page, file] of uiFiles()) {
  const html = fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', file), 'utf8');
  pagesExamined++;
  for (const id of [...new Set([...html.matchAll(/id="([A-Za-z0-9_-]+)"/g)].map((x) => x[1]))].sort()) {
    idsExamined++;
    if (mentionsLive(id) && !mentionsPort(id)) found.push({ page, id });
  }
}
// A RUN THAT EXAMINED NOTHING IS A FAILURE, NOT A PASS. `uiFiles()` derives from
// `PORTED` and the extracted markup; if either moved, this would have printed
// "clean" over an empty scan for as long as it took somebody to notice.
if (pagesExamined === 0 || idsExamined === 0) {
  console.error('wiring audit examined ' + pagesExamined + ' page(s) and ' +
    idsExamined + ' id(s) — it is measuring nothing, which is not the same as clean');
  process.exit(1);
}

const unexplained = found.filter((f) => !EXPECTED[f.id]);
const explainedIds = new Set(found.map((f) => f.id));
const closed = Object.keys(EXPECTED).filter((id) => !explainedIds.has(id));

const problems = [];
if (unexplained.length) {
  problems.push('These are on a PORTED page, written by the live app, and never mentioned by this\n' +
    'port. Each needs porting or an EXPECTED entry saying why not:\n' +
    unexplained.map((f) => '  ' + f.page.padEnd(12) + f.id).join('\n'));
}
if (closed.length) {
  problems.push('These EXPECTED entries are no longer gaps — the port now touches them. Delete the\n' +
    'entry so the record does not outlive the thing it described:\n' +
    closed.map((id) => '  ' + id).join('\n'));
}
if (problems.length) {
  console.error('the wiring audit disagrees with its record:\n\n' + problems.join('\n\n') + '\n');
  process.exit(1);
}

const byReason = new Map();
for (const f of found) byReason.set(EXPECTED[f.id], (byReason.get(EXPECTED[f.id]) || 0) + 1);
// ── THE SUMMARY NAMES WHAT WAS EXAMINED, NOT ONLY WHAT WAS FOUND ─────────
//
// It used to read "0 known gaps across 0 ported pages, in 0 groups" on a clean
// run, because both counts were derived from the FINDINGS. That is
// indistinguishable from an audit that scanned nothing, and on 2026-08-30 it was
// briefly reported as a vacuous gate for exactly that reason — the numbers said
// "examined nothing" and only reading the source said otherwise.
//
// A check that cannot be told apart from a broken one is a check nobody can
// trust, so the examined counts lead.
console.log('wiring audit clean: ' + pagesExamined + ' pages and ' + idsExamined +
  ' ids examined; ' + found.length + ' known gaps across ' +
  new Set(found.map((f) => f.page)).size + ' page(s), in ' + byReason.size + ' groups');
for (const [reason, n] of [...byReason.entries()].sort((a, b) => b[1] - a[1])) {
  console.log('  ' + String(n).padStart(2) + '  ' + reason.split(' — ')[0].slice(0, 88));
}
