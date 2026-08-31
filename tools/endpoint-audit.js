'use strict';
/**
 * ENDPOINTS THE PORTED FRONTEND CALLS, AND WHETHER GO SERVES THEM.
 *
 * ── WHY THIS IS A CUTOVER CHECK, NOT A BUG CHECK ────────────────────────────
 *
 * Today the port runs BESIDE the Node app and proxies everything it has not
 * implemented, so a ported page calling `/api/localcc` works: the request goes
 * to Node. At cutover Node is gone, and every endpoint a ported page depends on
 * must exist in Go or that page quietly loses a feature.
 *
 * `/api/localcc` is the worked example. The Connections map fetches it for the
 * router's own country, which is where its arcs ORIGINATE. Without it `localCC`
 * stays `'ZZ'`, no arc is drawn, and the code handles that gracefully — "no
 * arcs; the rest of the page is unaffected". So the failure at cutover is not an
 * error in a log. It is a map that draws countries and no connections, which
 * `PORT-QUEUE.md` already records as looking "exactly like a geo defect".
 *
 * The cutover checklist said "every page DOM-verified" and nothing about
 * endpoints. A page can be DOM-identical and still lose its data.
 *
 * ── WHAT COUNTS AS SERVED ───────────────────────────────────────────────────
 *
 * Routes are registered as `mux.HandleFunc("GET " + prefix + "/thing", …)`, so
 * the literal path rarely appears in one piece. This resolves the `const`
 * prefixes first and matches against the concatenations.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/endpoint-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');

function walk(dir, ext, out) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, ext, out);
    else if (e.name.endsWith(ext)) out.push(p);
  }
  return out;
}

// ── what the frontend asks for ──────────────────────────────────────────────
//
// ── TWO TREES, AND THE SECOND WAS MISSING UNTIL 2026-08-28 ──────────────────
//
// This walked `web/src` for `.ts` and nothing else, so `web/public/login.js` —
// the FIRST page anybody hits, vendored verbatim from the live app — was
// invisible. Its three calls (`/api/auth/status`, `/api/auth/login`,
// `/api/users/setup`) were never counted, and this audit reported "24 of 24
// served" while a whole page's endpoints had never been looked at.
//
// The same failure shape this audit already carries a note about: it compared
// PATHS and discarded the verb, so `GET /api/routers` read as served and the app
// could not start. A ledger is only as wide as what it walks.
//
// `web/public` is plain `.js` because it is vendored, not built — see
// THIRD_PARTY_NOTICES.md and the asset-vendoring entry in Changes.md.
//
// ── AND `.html`, BECAUSE EXTRACTED MARKUP IS RUNTIME CODE ──────────────────
//
// `tools/extract-ui.js` writes the lifted page bodies to `web/src/ui/*.html`.
// They sit INSIDE `web/src` and were invisible to this audit anyway, because it
// walked `.ts` there and nothing else. Any inline `fetch('/api/…')` the live
// markup happens to carry would be a call the app makes and this ledger never
// counted — the audit's own words two paragraphs up: a ledger is only as wide
// as what it walks.
//
// There is no such call TODAY (checked 2026-08-29: the only `/api` outside
// `web/src/**.ts` is in `web/build.mjs`, which is build tooling). This closes the
// hole while it is still empty, which is the cheap moment to do it. Attributes
// are not false positives: `scanSource` matches `fetch(` and `API|URL|ENDPOINT =`,
// so `href="/api/x"` does not register.
const SOURCES = [
  { dir: path.join(ROOT, 'web', 'src'), ext: '.ts' },
  { dir: path.join(ROOT, 'web', 'src'), ext: '.html' },
  { dir: path.join(ROOT, 'web', 'public'), ext: '.js' },
];
// Endpoints the app calls that live outside `/api`. Kept as a named list so
// adding one is a deliberate act with a place to explain itself.
const EXTRA_ENDPOINTS = ['/healthz'];

const calls = new Map();
const callMethods = new Map();
for (const { dir, ext } of SOURCES) {
  if (!fs.existsSync(dir)) continue;
  for (const f of walk(dir, ext, [])) {
    scanSource(f, path.relative(ROOT, f).split(path.sep).join('/'));
  }
}

function scanSource(f, rel) {
  const body = fs.readFileSync(f, 'utf8');
  const add = (u, verb) => {
    const clean = u.split('?')[0].replace(/\/$/, '');
    // ── /api IS NOT THE WHOLE SURFACE, AND THAT WAS A BLIND SPOT ────────
    //
    // This filter meant the audit could only ever see `/api` and `/next`, so a
    // page calling anything else was invisible to it. `/healthz` is exactly
    // that: the frontend fetches it from `settings.ts` and `account.ts` for the
    // version string, the live docker-compose HEALTHCHECK polls it, and the port
    // answered 404 while this audit reported every endpoint served.
    //
    // The list is explicit rather than "anything absolute", because a page also
    // fetches STATIC assets by absolute path and those are not endpoints — a
    // blanket rule would fill the report with .js and .css and teach people to
    // ignore it.
    if (!clean.startsWith('/api') && !clean.startsWith('/next') &&
        !EXTRA_ENDPOINTS.includes(clean)) return;
    if (!calls.has(clean)) calls.set(clean, new Set());
    calls.get(clean).add(rel);
    if (!callMethods.has(clean)) callMethods.set(clean, new Set());
    callMethods.get(clean).add(verb || 'GET');
  };
  for (const m of body.matchAll(/fetch\(\s*['"`]([^'"`]+)['"`]([^)]*)/g)) {
    // The options object follows the URL in the same call. A `method:` there is
    // the verb; ABSENT MEANS GET, which is the fetch default and is exactly the
    // case that was invisible before — `fetch('/api/routers')` looks like no
    // method at all and is the one the app cannot start without.
    const v = (m[2].match(/method\s*:\s*['"](\w+)['"]/) || [, ''])[1];
    add(m[1], v.toUpperCase());
  }
  for (const m of body.matchAll(/(?:API|URL|ENDPOINT)\s*=\s*['"]([^'"]+)/g)) add(m[1], '');
}

// ── what Go serves ──────────────────────────────────────────────────────────
const goFiles = walk(path.join(ROOT, 'internal', 'server'), '.go', []);
const go = goFiles.map((f) => fs.readFileSync(f, 'utf8')).join('\n');
// ── CONST CHAINS, NOT JUST LITERALS ─────────────────────────────────────────
//
// A path is often built in two steps — `const auditPrefix = Prefix + "/api/audit"`
// — and resolving only `name = "/literal"` leaves the second name unknown, so the
// route reduces to nothing and reads as UNSERVED.
//
// That was invisible until `/api/collectors` was served the same way and the
// audit reported a route it had just been given. `auditPrefix` and
// `reportsPrefix` have always had this shape; nothing called them from a ported
// page, so nothing noticed.
const consts = {};
for (const m of go.matchAll(/(\w+)\s*=\s*"(\/[^"]*)"/g)) consts[m[1]] = m[2];
// Then the concatenations, repeatedly, so a chain of any depth settles. Bounded
// rather than `while (changed)`: a cyclic definition would not compile, but this
// runs over source that might not.
for (let pass = 0; pass < 5; pass++) {
  for (const m of go.matchAll(/(\w+)\s*=\s*(\w+)\s*\+\s*"([^"]*)"/g)) {
    const [, name, base, tail] = m;
    if (consts[name] === undefined && consts[base] !== undefined) {
      consts[name] = consts[base] + tail;
    }
  }
}
const served = new Set();
// ── THE METHOD IS NOT DECORATION, AND THIS AUDIT USED TO DISCARD IT ─────────
//
// The note further down said "THIS AUDIT CANNOT SEE HTTP METHODS ... The
// failure mode of that choice is a path that keeps being proxied after Go could
// serve it — visible and harmless". The opposite happened.
//
// `GET /api/routers` is what `main.ts` calls at startup and the app cannot list
// a single router without it. Go registers `POST /api/routers`,
// `PUT /api/routers/{id}` and `DELETE /api/routers/{id}` — and NOT the GET. On
// a path-only comparison that reads as SERVED, so this audit reported it as one
// of the 13 Go covers and it never appeared as a cutover item.
//
// Found by running the server in standalone mode: the SPA failed to start with
// "cannot list routers: 502". Recorded here rather than only fixed, because the
// audit was reporting clean about the single endpoint whose absence stops the
// app dead.
const servedMethods = new Map();
for (const m of go.matchAll(/mux\.Handle(?:Func)?\(\s*([^)]+?)\s*,/g)) {
  let expr = m[1];
  // WHOLE IDENTIFIERS, LONGEST FIRST. `Prefix` is a SUBSTRING of
  // `settingsPrefix`, `auditPrefix`, `reportsPrefix` and `principalsPrefix`, so a
  // plain `split/join` rewrites `settingsPrefix` into `settings"/next"` and the
  // route vanishes from the served set.
  //
  // That was latent while those four were themselves built as `Prefix + "..."`
  // — the chain pass resolved them and the damage was invisible. Taking the API
  // off the prefix on 2026-08-25 turned `settingsPrefix` into a plain literal and
  // the bug surfaced as "/api/settings is not served", which it plainly is.
  //
  // The word boundary is what actually fixes it; sorting by length is belt and
  // braces for any future pair where one name is a prefix of another AND the
  // boundary does not separate them.
  for (const k of Object.keys(consts).sort((a, b) => b.length - a.length)) {
    expr = expr.replace(new RegExp('\\b' + k + '\\b', 'g'), '"' + consts[k] + '"');
  }
  const parts = [...expr.matchAll(/"([^"]*)"/g)].map((x) => x[1]);
  const joined = parts.join('');
  // THE METHOD IS PART OF THE PATTERN in Go's ServeMux — `"POST /api/routers"`.
  // It used to be stripped and thrown away; see the note above `servedMethods`.
  const verb = (joined.match(/^(GET|POST|PUT|DELETE|PATCH)\s+/) || [, ''])[1];
  let route = joined.replace(/^(GET|POST|PUT|DELETE|PATCH)\s+/, '');
  route = route.split('?')[0].replace(/\/$/, '');
  if (route.startsWith('/')) {
    served.add(route);
    if (!servedMethods.has(route)) servedMethods.set(route, new Set());
    // A pattern with NO verb answers every method, which is what ServeMux does.
    servedMethods.get(route).add(verb || 'ANY');
  }
}

// A call is SERVED when this process registers that path, a parent of it, or —
// the case below — every child of it.
//
// A page often names a BASE and appends the leaf: `const API = '/api/reports/'`,
// then `API + 'ping'`. The extractor strips the trailing slash, so the recorded
// call is `/api/reports` while the registered routes are `/api/reports/ping` and
// its siblings. Without the third clause that reads as unserved, and the fix
// that suggests itself — recording it as proxied — would be a ledger entry
// asserting the opposite of the truth.
//
// It is deliberately not "any route starts with u": that would make `/api`
// served the moment anything under it was. The base must be a PATH SEGMENT
// boundary, which `r.startsWith(u + '/')` requires.
// ── AND THE SECOND CLAUSE WAS WRONG, WHICH THIS AUDIT REPORTED AS A LEDGER
//    ERROR ──────────────────────────────────────────────────────────────────
//
// It used to include `u.startsWith(r + '/')`: a registered `/api/routers` was
// taken to cover `/api/routers/test`. Go's `ServeMux` does not work that way. A
// pattern matches the EXACT path unless it ends in `/` or carries a `{wildcard}`
// — verified by handing a real mux `POST /api/routers/test` with only
// `POST /api/routers` and `PUT /api/routers/{id}` registered, and getting back
// the empty pattern.
//
// So that clause claimed Go served an endpoint it 404s, and the visible symptom
// was this audit demanding the removal of a CORRECT proxy entry. Believing it
// would have deleted the record for a route that still needs proxying, and the
// endpoint would have broken silently at cutover — which is precisely the
// failure this file exists to prevent.
//
// A `{wildcard}` pattern still covers its shape, and that is what the clause
// below expresses: the registered route and the call agree segment for segment,
// with a wildcard matching any one segment.
// ── `{$}` IS NOT A WILDCARD; IT IS THE OPPOSITE ───────────────────────────
//
// Go's ServeMux gives `{$}` a special meaning: it matches only the END of the
// path, so `/{$}` matches EXACTLY `/` and nothing else. It is an anchor, not a
// segment pattern.
//
// This treated it as a wildcard, so the registered `/{$}` matched every
// single-segment path — `/healthz`, `/login`, anything. Measured on 2026-08-29:
// the port answered 404 for `/healthz` while this audit reported it served, and
// the frontend calls it from `settings.ts` and `account.ts` for the version
// string. A false SERVED is worse than the blind spot that hid it, because it
// asserts the opposite of the truth about a route nobody is testing.
const isWildcardSeg = (seg) => /^\{[^}]+\}$/.test(seg) && seg !== '{$}';

const segmentsMatch = (route, url) => {
  const a = route.split('/');
  const b = url.split('/');
  if (a.length !== b.length) return false;
  return a.every((seg, i) => seg === b[i] || isWildcardSeg(seg));
};

// ── A WILDCARD MATCH IS WEAKER THAN AN EXPLICIT PROXY RECORD ───────────────
//
// THIS AUDIT CANNOT SEE HTTP METHODS. It collects the URLs a page fetches and
// the paths this process registers, and both lose the verb on the way in.
//
// That is usually harmless and is exactly wrong for one shape: `/api/routers/{id}`
// is registered for PUT, and its wildcard matches the PATH `/api/routers/test` —
// which is a POST endpoint that Node serves and this port does not. Treating the
// shape match as authoritative made this audit demand the removal of a CORRECT
// proxy entry, and following that instruction would have broken the endpoint
// silently at cutover.
//
// So a LITERAL registration still proves Go serves a path, and a WILDCARD match
// does not override an explicit `PROXIED` entry. The failure mode of that choice
// is a path that keeps being proxied after Go could serve it — visible and
// harmless — rather than one that stops being proxied before Go does.
const isServedLiterally = (u) => served.has(u)
  || [...served].some((r) => r.endsWith('/') && u.startsWith(r))
  || [...served].some((r) => r.startsWith(u + '/'));

// methodsCovered reports whether every verb a page uses on this path is
// registered for it.
//
// A route with no verb in its pattern answers all of them, so `ANY` satisfies
// anything. A path this audit knows nothing about is not contradicted — the
// path-level checks above decide that case.
const methodsCovered = (u) => {
  const want = callMethods.get(u);
  const have = servedMethods.get(u);
  if (!want || !have) return true;
  if (have.has('ANY')) return true;
  return [...want].every((v) => have.has(v));
};

const isServed = (u) => (isServedLiterally(u)
  || (!PROXIED[u] && [...served].some((r) => segmentsMatch(r, u))))
  && methodsCovered(u);

// ── the ledger ──────────────────────────────────────────────────────────────
// Endpoints a ported page calls that Go does NOT serve. Every one works today
// through the proxy; every one is a cutover item. The reason says what breaks.
const PROXIED = {
  // `/api/auth/permissions` left on 2026-08-27, unconditionally — it is
  // `capsFor(session)`, the same object `/api/auth/status` nests under
  // `session.caps`, computed from the shared grant graph. Porting it found that
  // the caps object this port was ALREADY sending was missing its four flags,
  // which `web/src/caps.ts` reads directly to decide whether to draw admin
  // controls: an administrator would have seen them hidden at cutover.
  //
  // The two SAVED LAYOUTS left on 2026-08-27, unconditionally. They read and
  // write `user_layouts` in the SQLite database both processes share, so there
  // is no process-local state to disagree about — the same reason
  // `/api/account/access` is unconditional and the two session routes are not.
  //
  // `/api/account/password` left on 2026-08-27, and ONLY IN STANDALONE — it
  // writes users.json, which src/users.js caches with no watcher, so a change
  // made while Node runs is reverted by its next save and the operator is told
  // their password changed when it did not. `coexistence-audit` carries the
  // full reasoning; the gate itself is pinned by a Go test asserting the route
  // answers 502 when a Node URL is configured.
  //
  // `/api/account/access` also left on 2026-08-27, and UNCONDITIONALLY — unlike
  // the two session routes below it. It reads the grant graph out of the SQLite
  // database both processes share, so Go and Node compute the same answer from
  // the same rows; there is no process-local state to be wrong about.
  //
  // ── AND THE TWO ACCOUNT SESSION ROUTES LEFT ON 2026-08-27 ────────────────
  //
  // `/api/account/sessions` and `/api/account/sessions/revoke-others` read the
  // session store THIS process owns, which is empty while Node is the
  // authority — so they are registered under the same `standalone` condition as
  // the login routes, and for a sharper reason: a Go answer during coexistence
  // would be "you have no sessions" beside a browser that is plainly signed in.
  // A confident wrong answer is worse than the proxy's correct one.
  //
  // `/api/account/password` and `/api/account/access` STAY on this list. The
  // password write is a users.json write and `src/users.js` caches the file the
  // way settings.js and routers.js do, so a change from here would be reverted
  // by Node's next save — the operator would be told their password changed and
  // it would not have. The access summary is porting work rather than a blocker
  // and is left for a pass that can give it a generated corpus.
  //
  // ── /api/auth/status, /api/auth/logout AND /api/auth/login LEFT THIS LIST
  //    ON 2026-08-27, and the reason is worth keeping ────────────────────────
  //
  // The entry read: "BY DESIGN, not by omission: Node stays the auth authority
  // and the Go server ASKS it. At cutover Go has to own the answer, which is a
  // bigger decision than adding a route." Correct on every point, and it did not
  // say the consequence out loud — with Node stopped, NOBODY CAN LOG IN. Found
  // by standing the server against the live /data for the first time.
  //
  // Go owns all three now (`internal/server/auth_login.go`), but ONLY when
  // `-node` is empty, which is what cutover means. While Node runs they still
  // proxy, and `TestLoginIsNotServedWhileNodeRuns` fails if that stops being
  // true — registering them unconditionally would give the browser a Go session
  // Node does not know and answer 401 on every unported page.
  //
  // THIS AUDIT CANNOT SEE THAT CONDITION. It reads route registrations out of
  // the source, so "Go serves it" is all it can know, and it fired correctly on
  // exactly that. The conditional is pinned by a Go test rather than here,
  // because a ledger over source text is the wrong instrument for a runtime
  // branch — recorded so the next reader does not try to teach it one.
  // ── /api/routers/test LEFT THIS LIST ON 2026-08-27 ───────────────────────
  //
  // Its entry read: "the dialog's Test Connection button. Without it the button
  // reports a failure for every router, including working ones - the operator's
  // only pre-save check says the credentials are wrong when they are not."
  //
  // Go serves it now (`internal/server/routers_conntest.go`), unconditionally:
  // it dials a router and READS routers.json, so nothing about it conflicts with
  // Node still running - unlike the auth routes above, and unlike anything that
  // WRITES a Node-cached file.
  //
  // It was the LAST entry here. Every endpoint the frontend calls is now served
  // by this process.
  //
  // AN EMPTY LIST IS NOT A FINISHED PORT, and this audit cannot tell the
  // difference: it compares the URLs pages FETCH against the routes Go
  // registers, so a page nobody has ported contributes no URLs and is invisible.
  // Dashboard, Devices and Settings are in exactly that state. `PORT-QUEUE.md`
  // is the ledger for pages; this one is only for endpoints.
  // `/api/settings` WAS here: "the port serves settings under its staging prefix
  // (`/next/api/settings`); `account.ts` still calls the LIVE one. The prefix
  // comes off at cutover, which is recorded separately — this entry is here so
  // the two are not forgotten independently."
  //
  // The prefix came off on 2026-08-25 and the two were not forgotten: this audit
  // refused the entry the moment the route began serving the path `account.ts`
  // had been calling all along.
};

const problems = [];
const pending = [];
for (const [u, where] of [...calls].sort()) {
  if (isServed(u)) continue;
  pending.push(u);
  if (!PROXIED[u]) {
    problems.push(u + ' (called from ' + [...where].join(', ') + ') is not served by Go and is ' +
      'not recorded — it works through the proxy today and breaks at cutover');
  }
}
for (const u of Object.keys(PROXIED)) {
  if (!calls.has(u)) problems.push(u + ' is recorded but nothing calls it — remove it');
  else if (isServedLiterally(u)) {
    problems.push(u + ' is recorded as proxied but Go serves it now — remove it');
  }
}

if (problems.length) {
  shout('endpoint-audit: %d problem(s)\n', problems.length);
  for (const p of problems) shout('  - ' + p);
  process.exit(1);
}
say('endpoint-audit: %d endpoints called by ported pages; %d served by Go, %d proxied to Node ' +
    'and recorded as cutover items', calls.size, calls.size - pending.length, pending.length);
