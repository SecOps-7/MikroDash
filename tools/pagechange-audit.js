#!/usr/bin/env node
'use strict';
/**
 * WHAT EACH PAGE ASKS FOR WHEN YOU NAVIGATE TO IT — live against ported.
 *
 * ── THE GAP THIS FILLS, MEASURED RATHER THAN GUESSED ────────────────────────
 *
 * `inbound-audit` compares the socket actions the browser SENDS against the
 * handlers a server has to answer, and it reported 37 emitted by this port with
 * nothing missing. It could not see this bug, and the reason is worth stating
 * plainly: it asks "is this action emitted ANYWHERE", and the defect was "the
 * action is emitted from the wrong place".
 *
 * `wifiscan:interfaces` was emitted from `open()` in `wireless-fa.ts` — the
 * function that runs when the Frequency Analyser modal opens. `faOpenBtn` ships
 * `style="display:none"` in the extracted markup and is unhidden by exactly one
 * line, the `wifiscan:interfaces` reply handler. The modal opens from the
 * button. So the button waited for an answer that only a click on the button
 * could ask for, and the Frequency Analyser was UNREACHABLE in the port while
 * every one of its renderers passed `tools/fa-dialog-check.js` against the live
 * originals.
 *
 * Live asks in three places. This port had one of them. Nothing was red.
 *
 * ── SO THE LEDGER IS PER-PAGE, NOT PER-ACTION ───────────────────────────────
 *
 * `mikrodash:pagechange` is how both apps say "the operator is now looking at
 * X". A page that refreshes itself on entry does it here. This reads every
 * listener on both sides, works out which page names each one reacts to and
 * which socket actions it emits in response, and compares the two maps.
 *
 * A page whose live listener emits something and whose ported listener emits
 * nothing is the shape of the bug above: a feature that is fully ported and
 * never asked for.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/pagechange-audit.js
 *
 * ── WHAT IT DELIBERATELY DOES NOT CLAIM ─────────────────────────────────────
 *
 * A listener that does local work only — resetting stale timers, resizing a
 * card — emits nothing on either side and is not a finding. This compares the
 * EMIT sets, so it is silent about pages where both sides agree there is
 * nothing to ask for. That is the honest boundary: it catches "the port forgot
 * to ask", which is the failure it exists for, and says nothing about the rest.
 */

const fs = require('node:fs');
const path = require('node:path');

const say = console.log.bind(console);
const SRC = process.env.MIKRODASH_SRC || '../MikroDash';
const HERE = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('pagechange-audit');

const EVENT = 'mikrodash:pagechange';

/**
 * The listener bodies in one source string.
 *
 * Brace-matched from the `addEventListener` call rather than cut at a closing
 * line: these bodies contain braces, and a line-based cut would take half a
 * listener and report whatever happened to fall inside it.
 */
function listenerBodies(src) {
  const out = [];
  const needles = [`addEventListener('${EVENT}'`, `addEventListener("${EVENT}"`];
  for (let i = 0; ; ) {
    let at = -1;
    for (const n of needles) {
      const j = src.indexOf(n, i);
      if (j >= 0 && (at < 0 || j < at)) at = j;
    }
    if (at < 0) break;
    i = at + 1;
    // Walk to the first `{` inside the call, then brace-match it. A listener
    // written as an arrow with an expression body has no `{` before the call's
    // own `)`; it is recorded as an empty body rather than skipped, because an
    // empty body emits nothing and that is exactly what it should report.
    const openParen = src.indexOf('(', at);
    let depth = 0;
    let brace = -1;
    for (let j = openParen; j < src.length; j++) {
      const c = src[j];
      if (c === '(') depth++;
      else if (c === ')') { depth--; if (depth === 0) break; }
      else if (c === '{' && depth >= 1) { brace = j; break; }
    }
    if (brace < 0) { out.push(''); continue; }
    let d = 0;
    let end = brace;
    for (let j = brace; j < src.length; j++) {
      if (src[j] === '{') d++;
      else if (src[j] === '}') { d--; if (d === 0) { end = j; break; } }
    }
    out.push(src.slice(brace, end + 1));
  }
  return out;
}

/** The page names a listener body reacts to, from its `detail` comparisons. */
function pagesOf(body) {
  const names = new Set();
  let m;
  // BOTH SPELLINGS OF THE GUARD. `detail === 'x'` scopes a listener to x, and
  // so does `if (detail !== 'x') return` — the negated early return, which is
  // what the live rosusers listener uses. Reading only the positive form filed
  // that listener under '*' and reported it against every page, which is a
  // false positive that reads exactly like a real finding.
  const re = /detail\s*(?:as\s+\w+\s*)?[!=]==?\s*['"]([a-z0-9-]+)['"]/gi;
  while ((m = re.exec(body))) names.add(m[1]);
  // `['a','b'].indexOf(e.detail)` and `.includes(e.detail)` are the other spelling.
  const re2 = /\[([^\]]{2,160})\]\s*\.\s*(?:indexOf|includes)\s*\(\s*[\w.]*detail/gi;
  while ((m = re2.exec(body))) {
    for (const q of m[1].match(/['"][a-z0-9-]+['"]/gi) || []) names.add(q.replace(/['"]/g, ''));
  }
  return names;
}

/** The socket actions a listener body emits. */
function emitsOf(body) {
  const out = new Set();
  const re = /\.emit\s*\(\s*['"]([a-z0-9:_-]+)['"]/gi;
  let m;
  while ((m = re.exec(body))) out.add(m[1]);
  return out;
}

/** page -> set of actions asked for on entry. */
function mapFor(sources) {
  const map = new Map();
  for (const src of sources) {
    for (const body of listenerBodies(src)) {
      const emits = emitsOf(body);
      if (!emits.size) continue;
      const pages = pagesOf(body);
      // A listener with NO page test fires for every page. It cannot be
      // attributed to one, so it is filed under '*' and compared as its own
      // bucket rather than smeared across every page name.
      const keys = pages.size ? [...pages] : ['*'];
      for (const k of keys) {
        if (!map.has(k)) map.set(k, new Set());
        for (const e of emits) map.get(k).add(e);
      }
    }
  }
  return map;
}

function readLive() {
  // Reached only inside the frozen closure above, so the file is present.
  return [fs.readFileSync(path.join(SRC, 'public', 'app.js'), 'utf8')];
}

function readPort() {
  const root = path.join(HERE, 'web', 'src');
  const out = [];
  const walk = (d) => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const f = path.join(d, e.name);
      if (e.isDirectory()) walk(f);
      else if (/\.ts$/.test(e.name) && !/\.d\.ts$/.test(e.name)) out.push(fs.readFileSync(f, 'utf8'));
    }
  };
  walk(root);
  return out;
}

/**
 * The events this port's server VOLUNTEERS when a page takes focus.
 *
 * ── ASKING IS NOT THE REQUIREMENT; HAVING THE ANSWER IS ────────────────────
 *
 * `CLAUDE.md` permits the backend to change where that buys efficiency, and
 * `ws.go`'s page-focus switch uses it: for `rosusers` it SENDS `rosusers:caps`
 * unprompted, with the reason in its own comment — "the caps go FIRST. The page
 * draws its buttons from `permitted`, and a payload arriving before them
 * renders a read-only table that then has to be redrawn — visible as a flicker
 * on every visit." That is one fewer round trip and a better ordering than the
 * live app's ask-on-entry.
 *
 * So an action the live page requests on entry is SATISFIED if the port's
 * server volunteers the same event on focus. Live's request and its reply share
 * a name on every one of these, which is what makes the comparison possible.
 *
 * This is measured from `ws.go`, not listed here. A hand-written exception list
 * would go stale the moment the volunteering stopped, and would then be
 * indistinguishable from the bug it was excusing.
 */
function volunteeredByServer() {
  const p = path.join(HERE, 'internal', 'server', 'ws.go');
  const map = new Map();
  if (!fs.existsSync(p)) return map;
  const src = fs.readFileSync(p, 'utf8');
  // Each `case "<page>":` in the focus switch, up to the next `case`.
  const re = /case\s+"([a-z0-9-]+)":([\s\S]*?)(?=\n\tcase\s+"|\n\t\})/g;
  let m;
  while ((m = re.exec(src))) {
    const sends = new Set();
    const se = /hub\.Send\(cn\.c,\s*"([a-z0-9:_-]+)"/g;
    let s2;
    while ((s2 = se.exec(m[2]))) sends.add(s2[1]);
    if (!sends.size) continue;
    const prev = map.get(m[1]) || new Set();
    for (const e of sends) prev.add(e);
    map.set(m[1], prev);
  }
  return map;
}

// FROZEN — the derived MAP of page -> requests the live app makes on entry. That
// is the ledger this audit compares the port against, so it is a lifted value.
//
// It used to SKIP without a reference, which is honest but leaves the audit
// permanently inert now the reference is going: a skip that can never stop being
// a skip is a check that has been deleted with extra steps.
const live = new Map(G.value('the live page-entry requests', () =>
  [...mapFor(readLive()).entries()].map(([k, v]) => [k, [...v]]).sort()));
if (live.size < 5) {
  throw new Error('only ' + live.size + ' live pages recorded — the golden is broken, and '
    + 'this audit would compare the port against nothing');
}
const port = mapFor(readPort());
const volunteered = volunteeredByServer();

const problems = [];
const excused = [];
for (const page of [...live.keys()].sort()) {
  const want = live.get(page);
  const got = port.get(page) || new Set();
  const server = volunteered.get(page) || new Set();
  const missing = [];
  for (const a of want) {
    if (got.has(a)) continue;
    if (server.has(a)) { excused.push(`${page}: ${a} (volunteered on focus by ws.go)`); continue; }
    missing.push(a);
  }
  if (missing.length) problems.push({ page, missing });
}

const pages = live.size;
const asks = [...live.values()].reduce((n, s) => n + s.size, 0);
say(`pagechange-audit: ${pages} live page(s) ask for something on entry, ${asks} action(s) total`);
for (const e of excused) say(`  · ${e}`);

if (problems.length) {
  for (const p of problems) {
    say(`  ✗ ${p.page}: the port does not ask for ${p.missing.join(', ')} on page entry`);
  }
  say('');
  say('A page that does not ask on entry shows whatever it had — or, when the answer is what');
  say('reveals the control, shows nothing and offers no way to ask again. That is how the');
  say('Frequency Analyser became unreachable while every one of its renderers passed.');
  process.exit(1);
}
say('every live page-entry request is made by the port too');
