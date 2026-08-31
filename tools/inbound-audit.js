#!/usr/bin/env node
'use strict';
/**
 * SOCKET ACTIONS THE BROWSER SENDS — live against ported.
 *
 * ── THE DIRECTION NOTHING ELSE COVERS ───────────────────────────────────────
 *
 * `event-audit` checks Go EMITS against browser subscriptions: server → client.
 * `endpoint-audit` covers HTTP. Nothing looked at the other direction — the
 * events a page emits and a server has to answer — so an action the port never
 * wired was invisible, and an action the port INVENTED was equally invisible.
 *
 * Both were real. `backups:run` was wired while `internal/server/backups.go`'s
 * own header went on saying it was not, and the port record repeated that for
 * several iterations. In the other direction the port emits `router:select`
 * where the live app emits `router:switch` — a rename nothing was watching.
 *
 * ── WHERE THE LIVE HANDLERS LIVE, WHICH IS NOT ONE FILE ─────────────────────
 *
 * `src/index.js` holds most of them, but not all: `traffic:select` is registered
 * inside `src/collectors/traffic.js`. Scanning index.js alone reported it as a
 * port invention, which is how this scan learned to read the collectors too.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/inbound-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('inbound-audit');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

function readAll(dir, out = []) {
  if (!fs.existsSync(dir)) return out;
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) readAll(p, out);
    else if (e.name.endsWith('.js')) out.push(fs.readFileSync(p, 'utf8'));
  }
  return out;
}

// FROZEN — the derived SET of inbound actions the live server answers. That is
// the ledger this audit compares ws.go against; an empty one would report every
// Go handler as "the live app has no such action".
const liveSrc = LIFT.hasReference(ROOT) ? readAll(path.join(LIVE, 'src')).join('\n') : '';
const live = new Set(G.value('the live inbound actions', () => {
  const out = new Set();
  for (const m of liveSrc.matchAll(/socket\.on\(\s*['"]([a-zA-Z]+:[a-zA-Z]+)['"]/g)) out.add(m[1]);
  return [...out].sort();
}));
if (live.size < 5) {
  throw new Error('only ' + live.size + ' live inbound actions recorded — the golden is broken');
}

const ws = fs.readFileSync(path.join(ROOT, 'internal', 'server', 'ws.go'), 'utf8');
const port = new Set();
for (const m of ws.matchAll(/case\s+"([a-zA-Z]+:[a-zA-Z]+)"/g)) port.add(m[1]);

// What the port's own browser code sends, so an emit with no handler is caught
// too — a button wired to an event nobody answers is the failure this port has
// already found twice on the LIVE side.
const tsSrc = [];
(function walk(dir) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p);
    else if (e.name.endsWith('.ts')) tsSrc.push(fs.readFileSync(p, 'utf8'));
  }
}(path.join(ROOT, 'web', 'src')));
const emitted = new Set();
for (const m of tsSrc.join('\n').matchAll(/socket\.emit\(\s*['"]([a-zA-Z]+:[a-zA-Z]+)['"]/g)) emitted.add(m[1]);

// ── the ledger ──────────────────────────────────────────────────────────────
// An action the live app answers and this port does not. Each needs a reason.
const WIFISCAN = 'the Frequency Analyser: a WiFi channel scan that takes the chosen radio off the '
  + 'air and drops every client on it. This said "no Go equivalent" of `src/wifiScan.js`; that '
  + 'stopped being true on 2026-08-26 — the guard, the accumulator, the lifecycle and the runner '
  + 'are all in `internal/wifiscan`, race-clean and mutation-checked, and so is '
  + '`ScannableInterfaces`. What remains is the WIRING: the wireless collector here does not hold '
  + 'the interface catalogue in the shape the dialog needs (master / capsmanManaged / disabled / '
  + 'masterInterface plus per-interface client counts), so the three socket handlers cannot be '
  + 'served yet. Also recorded in wiring-audit as this page\'s one known gap.';

const UNPORTED = {
  // `res:preview` WAS here — "THE PORT HIDES THE BUTTON", a user-visible gap on
  // every resource page. Closed 2026-08-25: `PreviewCommand` is ported and
  // gated, `resPreview` answers the event, and `resource.ts` shows the button on
  // a writable form. This audit is what found it and what refused to let the
  // entry stand once it was fixed.

  // `packages:upgrade` WAS here — the live app's fifth Packages action, which
  // this port lacked while the port record said Packages shipped "all four". The
  // HANDLER landed 2026-08-25 (`packagesUpgrade`); the DIALOG that drives it is
  // still unported and is recorded in wiring-audit's `upgrade dialog` group, so
  // nothing in this port emits the event yet. That is why it is not listed as
  // an emitted-but-unanswered control either: there is no control.

  // `wifiscan:interfaces`, `wifiscan:start` and `wifiscan:stop` WERE here. All
  // three are handled as of 2026-08-26 and this audit refused to let the entries
  // stand once they were — which is the whole reason it fails in both
  // directions. The WIFISCAN constant above is kept because wiring-audit still
  // needs the reason for the dialog itself.

  'rosusers:caps': 'ANSWERED, but not as an inbound action — a deliberate mechanism change. The '
    + 'live page REQUESTS caps on pagechange; this port PUSHES them from the `rosusers` page:focus '
    + 'branch (ws.go:497) before the payload, because the page draws its buttons from `permitted` '
    + 'and a payload arriving first renders a read-only table that then redraws — a visible flicker '
    + 'on every visit. Same outcome, one fewer round trip.',

  'router:switch': 'renamed — see RENAMED below, which carries the reasoning for the pair.',
};

// An event this port sends that the live app does not. A rename is not a defect
// — both ends here are the port's — but it must be deliberate and written down.
const RENAMED = {
  'router:select': 'the live app emits `router:switch` for the same action (app.js:8419, modern '
    + 'auth only; basic/none uses a REST POST to /api/routers/:id/activate instead). This port '
    + 'renamed the REQUEST and kept the response `router:switched` as it is. Self-consistent — both '
    + 'ends are this port\'s — and harmless in production, where the port\'s bundle is the only '
    + 'client. Recorded because nothing else would notice it, and because a renderer LIFTED by '
    + 'tools/live-renderer.js emits the live spelling if it ever emits this at all.',
};

const problems = [];

const missing = [...live].filter((e) => !port.has(e)).sort();
for (const e of missing) {
  if (!UNPORTED[e]) {
    problems.push(e + ' — the live app answers this and ws.go does not. Port it, or record why not.');
  }
}
for (const e of Object.keys(UNPORTED)) {
  if (!missing.includes(e)) {
    problems.push(e + ' is recorded as unported and IS handled now — delete the entry so the '
      + 'record does not outlive what it described.');
  }
}

const extra = [...port].filter((e) => !live.has(e)).sort();
for (const e of extra) {
  if (!RENAMED[e]) {
    problems.push(e + ' — ws.go answers this and the live app has no such action. Either it is a '
      + 'rename that needs recording, or a handler nothing sends.');
  }
}
for (const e of Object.keys(RENAMED)) {
  if (!extra.includes(e)) {
    problems.push(e + ' is recorded as a rename and now matches the live app — delete the entry.');
  }
}

// An event this port SENDS that nothing here answers. Normally a defect — a
// control that does nothing and reports nothing — so each one needs a reason.
// `backups:restore` WAS here, with a note ending "delete this entry the moment
// either the handler lands or the page ships, because after that it IS a dead
// control". The handler landed on 2026-08-25 and the audit refused the stale
// entry on the same run — which is the ledger doing exactly what that sentence
// asked of whoever came next.
const EMITTED_UNANSWERED = {};

const unanswered = [...emitted].filter((e) => !port.has(e)).sort();
for (const e of unanswered) {
  if (EMITTED_UNANSWERED[e]) continue;
  problems.push(e + ' — this port EMITS it and ws.go does not answer it. A control wired to an '
    + 'event nobody handles does nothing and reports nothing.');
}
for (const e of Object.keys(EMITTED_UNANSWERED)) {
  if (!unanswered.includes(e)) {
    problems.push(e + ' is recorded as emitted-but-unanswered and is now answered — delete the '
      + 'entry.');
  }
}

// THE PAGE MUST REALLY BE UNSHIPPED for that reason to hold. A page in PORTED
// whose event nothing answers is the dead control the entry says it is not.
const ported = fs.readFileSync(path.join(ROOT, 'web', 'src', 'main.ts'), 'utf8');
for (const e of Object.keys(EMITTED_UNANSWERED)) {
  const page = e.split(':')[0];
  if (new RegExp("PORTED[\\s\\S]{0,400}?'" + page + "'").test(ported)) {
    problems.push(e + " is excused because `" + page + "` is not shipped — but it IS in PORTED now. "
      + 'The excuse has expired and the control is dead.');
  }
}

say(`inbound-audit: ${live.size} live actions, ${port.size} answered here, ` +
    `${missing.length} unported (${Object.keys(UNPORTED).length} recorded), ` +
    `${extra.length} port-only (${Object.keys(RENAMED).length} recorded), ` +
    `${emitted.size} emitted by this port`);
if (problems.length) {
  shout('');
  for (const p of problems) shout('  ✗ ' + p);
  process.exit(1);
}
say('every inbound action is answered or recorded');
