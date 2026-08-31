'use strict';
/**
 * EVERY SELECTOR THIS PORT QUERIES MUST MATCH SOMETHING IT PRODUCES.
 *
 * `getElementById` is already covered — an id nothing produces is what
 * `lookup-audit.js` finds. This is the other half: `querySelector`,
 * `querySelectorAll`, `closest` and `matches`, which reach for CLASSES,
 * ATTRIBUTES and TAGS in markup the port generates a few lines away.
 *
 * ── WHY IT MATTERS MORE THAN IT LOOKS ───────────────────────────────────────
 *
 * A selector that matches nothing does not throw. `querySelectorAll` returns an
 * empty list and the code carries on down its fallback path, which is usually
 * the CORRECT-LOOKING one — so the page keeps working and quietly loses a
 * property nobody is watching.
 *
 * The Firewall page is the worked example. `updateCountersInPlace` finds its
 * rows with `tr[data-rule-id]` and its cells with `.fw-pkt` / `.fw-byte`, and
 * returns false — full rebuild — when it finds none. Rename the class in the
 * row template and every counter tick becomes a full rebuild: still correct,
 * still green in `firewall-table-check` (which drives the REBUILD), and the
 * flash animation now restarts on every poll, which is the one thing the
 * in-place path exists to prevent.
 *
 * ── THE HAYSTACK IS THE PORT'S OWN OUTPUT, AND THAT IS THE WHOLE POINT ──────
 *
 * The first version included the live app's stylesheets and `index.html`, on the
 * reasoning that the served page is both and that a narrow haystack had produced
 * nothing but false positives in two other audits this session. It ran clean —
 * and then all four mutations survived, INCLUDING renaming `fw-pkt` in the row
 * template. Of course they did: `fw-pkt` is in the live app's markup, so the
 * port could stop producing it entirely and still be "answered".
 *
 * A wide haystack is right for `class-hook-audit`, which asks whether anything
 * RESPONDS to a class the port toggles — the stylesheet responding is a real
 * answer. It is wrong here, where the question is whether the port's own query
 * matches the port's own markup. So the haystack is this port's TypeScript (the
 * markup it builds lives in template strings) and the extracted page markup
 * under `web/src/ui`, and nothing else.
 *
 * Measured, not reasoned: with the live files in, four mutations survived; with
 * them out, four die.
 *
 *   node tools/selector-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

function readAll(dir, ext, out = []) {
  if (!fs.existsSync(dir)) return out;
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) readAll(p, ext, out);
    else if (e.name.endsWith(ext)) out.push({ path: p, body: fs.readFileSync(p, 'utf8') });
  }
  return out;
}

const ts = readAll(path.join(ROOT, 'web', 'src'), '.ts');

// ── THE QUERIES THEMSELVES ARE CUT OUT OF THE HAYSTACK ──────────────────────
//
// Without this every selector answers ITSELF: `querySelector('.fw-pkt')` puts
// the string `.fw-pkt` in the file, so searching the same files for `.fw-pkt`
// always succeeds and the audit passes no matter what the markup says. That is
// how the first narrow-haystack version still let all five mutations through —
// narrowing the haystack was necessary and not sufficient, and only mutation
// testing told them apart.
// `(?:<[^>]*>)?` IS LOAD-BEARING. TypeScript writes
// `row.querySelector<HTMLElement>('.fw-pkt')`, and a pattern demanding `(`
// straight after the method name skips every typed call — which is most of them
// here. Both this strip AND the scan below missed them, so those selectors were
// never checked AND were left in the haystack to answer themselves. Renaming
// `fw-pkt` in the row template survived because of it.
const CALL = '(?:querySelectorAll|querySelector|closest|matches)(?:<[^>]*>)?';
const STRIP = new RegExp(CALL + "\\(\\s*'[^']*'", 'g');
// ── COMMENTS ARE NOT MARKUP ────────────────────────────────────────────────
//
// `resource.ts` explains itself with "a `[data-res-rows]` table", and that
// sentence answered the selector: deleting the attribute from every extracted
// page still passed, because the PROSE describing it was in the haystack. A
// comment cannot produce an attribute, so both comment forms are cut before the
// search. Found by mutating the markup and watching the audit shrug.
const uncomment = (src) => src
  .replace(/\/\*[\s\S]*?\*\//g, ' ')
  .replace(/(^|[^:])\/\/[^\n]*/g, '$1');
let hay = ts.map((f) => uncomment(f.body).replace(STRIP, 'Q(')).join('\n');
hay += '\n' + readAll(path.join(ROOT, 'web', 'src', 'ui'), '.html').map((f) => f.body).join('\n');
// NO STYLESHEETS, live or otherwise — see the header. A rule that styles a class
// is not evidence that this port still emits it.
void LIVE;

/** Bare tag selectors — nothing to check, every document has them. */
const TAGS = new Set(['tr', 'td', 'th', 'thead', 'tbody', 'title', 'path', 'svg', 'g', 'option',
  'input', 'select', 'button', 'a', 'li', 'ul', 'div', 'span', 'canvas', 'form', 'label', 'text']);

/**
 * Break a selector into the tokens that must exist somewhere.
 *
 * A compound like `#qTabBar .stab` has to be split: the id is `lookup-audit`'s
 * business and the class is this audit's, and requiring the literal string to
 * appear would fail on every selector built from two real pieces.
 */
function tokens(sel) {
  const out = [];
  // ── ATTRIBUTE VALUES ARE MASKED BEFORE SPLITTING ────────────────────────
  //
  // `.ptab:not([style*="display: none"])` contains a SPACE inside the attribute
  // value, so splitting on whitespace first produced `none"])` and this audit
  // reported that the tag `none` is produced by nothing. A first run whose only
  // hit is the tool's own parser is the failure mode two other audits hit this
  // session; the value is replaced with a placeholder so the attribute NAME is
  // still checked and its contents are not mistaken for structure.
  const masked = sel.replace(/=\s*("[^"]*"|'[^']*')/g, '=_');
  for (const part of masked.split(/[\s>+~,]+/)) {
    if (!part) continue;
    // :not(...) and other pseudo-classes carry their own selector; the argument
    // is checked, the pseudo itself is not a document token.
    const inner = part.match(/:not\(([^)]*)\)/);
    if (inner) out.push(...tokens(inner[1]));
    const base = part.replace(/:[a-z-]+(\([^)]*\))?/g, '');
    for (const m of base.matchAll(/\.([A-Za-z][\w-]*)/g)) out.push({ kind: 'class', name: m[1] });
    for (const m of base.matchAll(/\[([A-Za-z][\w-]*)/g)) out.push({ kind: 'attr', name: m[1] });
    for (const m of base.matchAll(/#([A-Za-z][\w-]*)/g)) out.push({ kind: 'id', name: m[1] });
    const tag = base.match(/^([a-z]+)/);
    if (tag && !TAGS.has(tag[1])) out.push({ kind: 'tag', name: tag[1] });
  }
  return out;
}

/** Is this token produced anywhere the served page can reach? */
function answered(t) {
  if (t.kind === 'class') {
    // In markup (`class="… x …"`), in a template string, or styled.
    return new RegExp('[\\s"\'`.]' + t.name + '[\\s"\'`,{:.\\[]').test(hay) ||
           hay.includes('.' + t.name);
  }
  if (t.kind === 'attr') {
    // ── THREE WAYS A data- ATTRIBUTE GETS WRITTEN, AND ALL THREE COUNT ────
    //
    // `data-rule-id="…"` in a template string is the obvious one. The other two
    // are what the first run of this audit reported as five missing attributes,
    // every one of which was really there:
    //
    //   `el.dataset.ifaceType = …`  — the DOM property form. It PRODUCES
    //     `data-iface-type` at runtime and the literal never appears in the
    //     source, so a checker looking for the string finds nothing. Both this
    //     port and the live app build the interface tiles this way.
    //   `<span data-res-drag>`      — a BOOLEAN attribute, with no `=` after it.
    //
    // Requiring the `=` form would have made this audit accuse four working
    // code paths on its first run, which is how an audit gets ignored.
    if (hay.includes(t.name + '=') || hay.includes('[' + t.name)) return true;
    if (new RegExp('\\s' + t.name + '[\\s>"\'`]').test(hay)) return true;
    // ── A FOURTH WAY, AND THE SAME ARGUMENT AS THE dataset ONE ────────────
    //
    // `el.setAttribute('data-user-id', id)` produces the attribute at runtime
    // and the literal never appears next to an `=`. It is the imperative twin of
    // `el.dataset.userId = id`, which this function already accepts for exactly
    // the reason given above — and it is how a row built with
    // `document.createElement` gets its id, because `userRowHtml` returns the
    // CELLS and the caller builds the `<tr>` around them.
    //
    // Found on 2026-08-28: `settings-principals.ts` had been setting
    // `data-user-id` this way since the card mounted, and the audit only noticed
    // once something QUERIED it — so this was a latent false positive waiting
    // for its first reader.
    if (new RegExp("setAttribute\\(\\s*['\"`]" + t.name + "['\"`]").test(hay)) return true;
    // AN ASSIGNMENT, NOT A READ. `row.dataset.ruleId !== …` reads the attribute
    // the template is supposed to have written; counting that as proof it was
    // written lets the template drop it entirely — which is exactly what
    // survived until this `=` was required.
    const camel = t.name.replace(/^data-/, '').replace(/-([a-z])/g, (_, c) => c.toUpperCase());
    return new RegExp('dataset\\.' + camel + '\\s*=(?!=)').test(hay) ||
           new RegExp("dataset\\['" + t.name + "'\\]\\s*=(?!=)").test(hay);
  }
  if (t.kind === 'id') return hay.includes('"' + t.name + '"') || hay.includes("'" + t.name + "'") ||
                              hay.includes('id="' + t.name);
  return hay.includes('<' + t.name);
}

/**
 * Selectors that legitimately match nothing this port produces, with why.
 *
 * Empty on purpose: every entry has to earn its place, and an entry that stops
 * being needed FAILS the run — an allowance nobody removes reads as "checked".
 */
const ALLOWED = {};

const seenAllowed = new Set();
const missing = [];
const RE = new RegExp(CALL + "\\(\\s*'([^']+)'", 'g');
let count = 0;

for (const f of ts) {
  const rel = path.relative(path.join(ROOT, 'web', 'src'), f.path).split(path.sep).join('/');
  for (const m of f.body.matchAll(RE)) {
    count++;
    const sel = m[1];
    if (sel in ALLOWED) { seenAllowed.add(sel); continue; }
    for (const t of tokens(sel)) {
      if (!answered(t)) missing.push({ rel, sel, t });
    }
  }
}

// BELIEVABILITY: a regex that matched nothing would report a clean sweep.
if (count < 20) {
  console.error('only ' + count + ' selectors found across ' + ts.length + ' modules — the scan ' +
                'is not reaching the code it is meant to check');
  process.exit(1);
}
for (const sel of Object.keys(ALLOWED)) {
  if (!seenAllowed.has(sel)) {
    missing.push({ rel: 'ALLOWED', sel, t: { kind: 'stale', name: 'no longer queried' } });
  }
}

if (missing.length) {
  for (const m of missing) {
    console.error('  ' + m.rel + ': ' + m.sel + '  →  the ' + m.t.kind + ' `' + m.t.name +
                  '` is produced by nothing this page can reach');
  }
  console.error('\nselector-audit: ' + missing.length + ' selector token(s) match nothing');
  process.exit(1);
}
console.log('selector-audit: ' + count + ' selectors across ' + ts.length +
            ' modules, every token produced by the page');
