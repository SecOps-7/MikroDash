'use strict';
/**
 * IDS THE PORT'S OWN TEMPLATES CREATE, AND WHETHER ANYTHING BINDS THEM.
 *
 * ── THE FOURTH BLIND SPOT ───────────────────────────────────────────────────
 *
 * `wiring-audit` reads ids out of the EXTRACTED MARKUP and checks the port binds
 * them. `attr-audit` does the same for data attributes. Neither can see a button
 * the port's own TypeScript renders into innerHTML — it is not in the markup and
 * it carries no attribute — so a control created by a template and never wired
 * is invisible to both.
 *
 * the port record named that gap when `rptSchedNew` was dead. It is wired now,
 * and the note had outlived it; this audit is so the next one is found by
 * running something rather than by remembering.
 *
 * ── BINDING HAS FOUR SPELLINGS IN THIS PORT ─────────────────────────────────
 *
 * `el('x')`, `el<T>('x')`, `byId('x')` — topology has its own helper — and a
 * DELEGATED `closest('#x')`. A checker that knew only the first would have
 * reported `topoPanelClose` as dead, and reporting a working control is how an
 * audit gets ignored.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/template-id-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const SRC = path.join(ROOT, 'web', 'src');

function walk(dir, out) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.name.endsWith('.ts')) out.push(p);
  }
  return out;
}

const files = walk(SRC, []);
const all = files.map((f) => fs.readFileSync(f, 'utf8')).join('\n');

// Ids created inside a template string: `id="x"` or `id=\"x\"`.
const made = new Map();
for (const f of files) {
  const body = fs.readFileSync(f, 'utf8');
  const rel = path.relative(SRC, f).split(path.sep).join('/');
  for (const m of body.matchAll(/id=\\?["']([A-Za-z][\w-]*)\\?["']/g)) {
    if (!made.has(m[1])) made.set(m[1], new Set());
    made.get(m[1]).add(rel);
  }
}

// Escape every metacharacter, not just `-`. Sufficient for an element id, but
// that is a fact about the caller rather than about this line.
const reEsc = (v) => v.replace(/[.*+?^${}()|[\]\\-]/g, '\\$&');
const boundBy = (id) => {
  const q = reEsc(id);
  return new RegExp(
    "\\bel\\('" + q + "'\\)"            // el('x')
    + "|\\bel<[^>]*>\\('" + q + "'\\)"  // el<T>('x')
    + "|\\bbyId\\('" + q + "'\\)"       // topology's own helper
    + "|getElementById\\('" + q + "'\\)"
    + "|closest\\('#" + q + "'\\)"      // delegated
    + "|querySelector\\w*\\('#" + q + "'\\)",
  ).test(all);
};

// ── the ledger ──────────────────────────────────────────────────────────────
// An id a template creates and nothing binds. Each needs a reason, because
// "nothing binds it" is sometimes correct — a SLOT another module fills is not
// a dead control.
const UNBOUND = {
  // `rtrColl_` is a PREFIX, not an id: the collector grid builds
  // `rtrColl_<key>` per row, and this checker matches literals. The rows are
  // reached by `[data-coll]`, which `attr-audit` covers, and the id exists only
  // to pair each checkbox with its label. Recorded for the same reason
  // `dash-coverage-check` records `cardId + 'Warn'`: a text search cannot
  // resolve a constructed id, and the honest response is to say which ones it
  // cannot see.
  rtrColl_: 'a constructed id — the grid builds `rtrColl_<key>`; the rows are bound by [data-coll]',
  // `s_` and `sv_` are the same shape: `buildSliders` emits `s_<pollKey>` for
  // each range input and `sv_<pollKey>` for the label beside it, one pair per
  // row of the GENERATED table in `web/src/gen/poll-tables.ts`. Both ARE bound —
  // `settings-poll.ts` looks them up with `el('s_' + cfg.key)` three lines later
  // to attach the input listener, and `applyPollProfile` and `customValues` read
  // them again — but this checker matches literals and cannot resolve a
  // concatenation. The set is not open-ended: it is exactly the table's keys,
  // and `tools/poll-sliders-check.js` drives every one of them against the live
  // implementation.
  s_: 'a constructed id — `s_<pollKey>` per slider; bound by el(\'s_\' + cfg.key) in settings-poll.ts',
  sv_: 'a constructed id — `sv_<pollKey>` per slider label; written by the same loop',
  // `sysUpdateAction` WAS here — "a SLOT, not a control: the live app's upgrade
  // module fills it, and that module is not ported". Closed 2026-08-25: the
  // module is `web/src/pages/upgrade.ts` and it draws the Update button into
  // that slot. The ledger is empty, and this entry going is what proves the
  // slot stopped being one.
};

const problems = [];
const unbound = [];
for (const [id, where] of [...made].sort()) {
  if (boundBy(id)) continue;
  unbound.push(id);
  if (!UNBOUND[id]) {
    problems.push(id + ' is created by ' + [...where].join(', ') +
      ' and NOTHING binds it — a control the port renders and cannot use, or a slot that needs ' +
      'recording here with the reason');
  }
}
for (const id of Object.keys(UNBOUND)) {
  if (!made.has(id)) problems.push(id + ' is in the ledger but no template creates it — remove it');
  else if (boundBy(id)) problems.push(id + ' is in the ledger but IS bound now — remove it');
}

if (problems.length) {
  shout('template-id-audit: %d problem(s)\n', problems.length);
  for (const p of problems) shout('  - ' + p);
  process.exit(1);
}
say('template-id-audit: %d ids created by port templates, %d bound, %d recorded as unbound',
  made.size, made.size - unbound.length, unbound.length);
