'use strict';
/**
 * The page catalogue, from src/pages.js.
 *
 * `GET /api/roles` sends `Pages.PAGES.map(p => ({key, title, settingsKey}))` —
 * the list the Access Management card draws a row per. A page added upstream
 * that is missing here is a page nobody can grant access to, and the card gives
 * no hint that a row is absent.
 *
 * Only the three fields the endpoint sends are captured. `streamRooms` and
 * `category` belong to the nav and the socket rooms, and recording them would
 * make this file churn on changes the roles card cannot see.
 *
 * `pages.js` is a plain frozen literal with no database behind it, so it is
 * REQUIRED rather than parsed — unlike rbac.js, which needs better-sqlite3.
 *
 *   node tools/pages-table.js            write the table
 *   node tools/pages-table.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'pages-table.json');

const Pages = require(path.join(LIVE, 'src', 'pages.js'));
if (!Array.isArray(Pages.PAGES) || Pages.PAGES.length < 15) {
  throw new Error('pages.js exported ' + (Pages.PAGES || []).length + ' pages — the shape ' +
    'changed and this would record a catalogue that is mostly missing');
}

const pages = Pages.PAGES.map((p) => ({
  key: p.key,
  title: p.title,
  // NULL, NOT OMITTED: `dashboard` has no settings toggle, and the card uses the
  // absence to know the page cannot be hidden. `undefined` would vanish from the
  // JSON and the two states would look the same.
  settingsKey: p.settingsKey === undefined ? null : p.settingsKey,
}));

for (const p of pages) {
  if (!p.key || !p.title) {
    throw new Error('a page has no key or title: ' + JSON.stringify(p));
  }
}

// SETTING_KEYS is what the settings form's page toggles are built from, and the
// two must agree: a page with a settingsKey that is not in SETTING_KEYS would
// have a grant row and no way to turn it off.
const settingKeys = new Set(Pages.SETTING_KEYS || []);
for (const p of pages) {
  if (p.settingsKey && !settingKeys.has(p.settingsKey)) {
    throw new Error('page ' + p.key + ' names settingsKey ' + p.settingsKey +
      ', which is not in Pages.SETTING_KEYS');
  }
}

// ── The sidebar categories ──────────────────────────────────────────────────
//
// Seven of them, and each is a `data-cat` on a `.nav-group` in the shell. The
// keys are what `POST /api/nav-prefs` filters an expanded-set against, so a key
// the registry does not know is DROPPED on save — silently, by design, because
// that filter is what stops an arbitrary string being stored in a blob that is
// later rendered. The cost of that safety is that a category renamed here and
// not in the markup stops persisting: it opens, it closes, and it is forgotten
// on every reload with nothing to say why. So the two lists are pinned to each
// other, the same way the theme swatches are pinned to PALETTE_COLORS.
const categories = (Pages.CATEGORIES || []).map((c) => ({ key: c.key, title: c.title }));
if (!categories.length) throw new Error('pages.js exported no CATEGORIES');
const catKeys = categories.map((c) => c.key);
if (JSON.stringify(catKeys) !== JSON.stringify([...(Pages.CATEGORY_KEYS || [])])) {
  throw new Error('CATEGORY_KEYS is not CATEGORIES in order — the port derives one from the other');
}

const shell = fs.readFileSync(path.join(__dirname, '..', 'web', 'src', 'ui', 'shell.html'), 'utf8');
const inMarkup = [...new Set([...shell.matchAll(/data-cat="([^"]*)"/g)].map((m) => m[1]))].sort();
const known = [...catKeys].sort();
const catBad = [];
for (const k of inMarkup) {
  if (!known.includes(k)) catBad.push('the sidebar has a .nav-group data-cat="' + k + '" that the ' +
    'registry does not know — expanding it is dropped by the save filter and forgotten on reload');
}
for (const k of known) {
  if (!inMarkup.includes(k)) catBad.push('the registry has category "' + k + '" but no .nav-group ' +
    'in the shell carries it — either a group was dropped from the markup or the entry is dead');
}
if (catBad.length) {
  console.error('the categories and the sidebar disagree:\n\n' + catBad.join('\n') + '\n');
  process.exit(1);
}

// ── The keyboard shortcut order ─────────────────────────────────────────────
//
// `PAGE_KEYS` lives in public/app.js, not in the registry, and maps a digit to a
// page: pressing 3 opens PAGE_KEYS[2]. Extracted rather than retyped, because a
// list whose ORDER is the whole meaning is the worst possible thing to copy by
// hand — a single transposition sends two shortcuts to each other's pages and
// looks completely normal in review.
//
// ONLY THE FIRST NINE ARE REACHABLE. The handler does `parseInt(e.key)` on a
// single keypress, and no keypress produces "10", so entries past the ninth can
// never be selected. That is recorded here rather than trimmed: the list is the
// live app's, and a tenth entry becoming reachable would be a change there.
const appJs = fs.readFileSync(path.join(LIVE, 'public', 'app.js'), 'utf8');
const pkMatch = appJs.match(/var PAGE_KEYS\s*=\s*\[([^\]]*)\]/);
if (!pkMatch) throw new Error('cannot find PAGE_KEYS in public/app.js');
const pageKeys = pkMatch[1].split(',').map((x) => x.trim().replace(/^'|'$/g, '')).filter(Boolean);
const pageKeySet = new Set(pages.map((p) => p.key));
for (const k of pageKeys) {
  if (!pageKeySet.has(k)) {
    throw new Error('PAGE_KEYS names "' + k + '", which is not a page in the registry — the ' +
      'shortcut would open a page that does not exist');
  }
}

// ── The nav sweep order ─────────────────────────────────────────────────────
//
// `ALL_NAV_PAGES` is what `applyPageVisibility` iterates, and its ORDER decides
// the FALLBACK: the first page still visible is where a user is sent when the
// page they are standing on is taken away from them. So a reordering silently
// changes where a demoted user lands, which is not something to retype.
//
// Every entry must be a `.nav-item[data-page=...]` in the shell, because the
// sweep hides pages by finding exactly that selector. An entry with no nav item
// is a page the sweep believes it has hidden and has not — it stays visible to
// a role that may not have it, which is the wrong direction for a permission
// check to fail in.
const anvMatch = appJs.match(/var ALL_NAV_PAGES\s*=\s*\[([^\]]*)\]/);
if (!anvMatch) throw new Error('cannot find ALL_NAV_PAGES in public/app.js');
const allNavPages = anvMatch[1].split(',').map((x) => x.trim().replace(/^'|'$/g, '')).filter(Boolean);
const navBad = [];
const inShell = new Set([...shell.matchAll(/class="nav-item[^"]*"[^>]*data-page="([^"]*)"/g)].map((m) => m[1]));
for (const m of shell.matchAll(/data-page="([^"]*)"/g)) inShell.add(m[1]);
for (const k of allNavPages) {
  if (!pageKeySet.has(k)) navBad.push('ALL_NAV_PAGES names "' + k + '", which is not a registry page');
  if (!inShell.has(k)) {
    navBad.push('ALL_NAV_PAGES names "' + k + '" but no .nav-item[data-page="' + k + '"] is in the ' +
      'shell — the visibility sweep would believe it had hidden a page it never touched');
  }
}
for (const k of inShell) {
  if (!allNavPages.includes(k)) {
    navBad.push('the shell has a nav item for "' + k + '" that ALL_NAV_PAGES does not sweep — it ' +
      'stays visible whatever the role says');
  }
}
if (navBad.length) {
  console.error('the nav sweep and the sidebar disagree:\n\n' + navBad.join('\n') + '\n');
  process.exit(1);
}

const body = JSON.stringify({
  note: 'Generated by tools/pages-table.js from the LIVE src/pages.js. Do not edit. ' +
        'Only the three fields GET /api/roles sends are captured.',
  pages,
  categories,
  pageKeys,
  allNavPages,
  /** How many of pageKeys a single keypress can actually reach. */
  reachableShortcuts: Math.min(9, pageKeys.length),
}, null, 2) + '\n';

// ── THE SERVER'S EMBEDDED COPY, WRITTEN HERE RATHER THAN BY HAND ───────────
//
// `internal/server/pages_table.json` is embedded by `principals_api.go` and
// serves the page catalogue on GET /api/roles. Its own note already said
// "Generated by tools/pages-table.js from the LIVE src/pages.js. Do not edit" —
// and that was NOT TRUE: this tool wrote only the testdata copy, so the server's
// was a hand-copy carrying a claim of provenance it did not have.
//
// It drifted, exactly as that arrangement invites: the upstream rename of the
// page key `routers` to `devices` (#117) reached every generated artefact and
// left this one behind, and `TestThePageCatalogueIsComplete` failed with
// "devices is write-capable but is not in the page catalogue".
//
// Only the three fields GET /api/roles sends are captured, which is what the
// note says and what the Go struct decodes.
const SERVER_OUT = path.join(__dirname, '..', 'internal', 'server', 'pages_table.json');
// `categoryKeys` JOINED THE SERVER TABLE ON 2026-08-27, with `POST /api/nav-prefs`.
// That route filters the saved `expanded` list through `Pages.CATEGORY_KEYS`,
// and the live comment says why in terms that decide where the list may live:
// "An unbounded list of arbitrary strings inside a blob that later gets rendered
// is how a preference becomes a stored-XSS vector; there are only ever a handful
// of category keys, and they are all known here."
//
// A hand-copied allow-list is the one shape that must not be used for that. It
// is generated from the same source as the pages, and the check above already
// asserts CATEGORY_KEYS is CATEGORIES in order.
const serverBody = JSON.stringify({
  note: 'Generated by tools/pages-table.js from the LIVE src/pages.js. Do not edit. ' +
        'Only the three fields GET /api/roles sends are captured, plus categoryKeys ' +
        'for the nav-prefs allow-list.',
  pages,
  categoryKeys: catKeys,
}, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
  const curServer = fs.existsSync(SERVER_OUT) ? fs.readFileSync(SERVER_OUT, 'utf8') : null;
  if (curServer !== serverBody) {
    console.error('internal/server/pages_table.json is stale — run: node tools/pages-table.js');
    process.exit(1);
  }
  if (cur !== body) {
    console.error('testdata/pages-table.json is stale — run: node tools/pages-table.js');
    process.exit(1);
  }
  console.log('pages table up to date (' + pages.length + ' pages, ' + categories.length +
    ' categories, ' + pageKeys.length + ' shortcut slots of which ' +
    Math.min(9, pageKeys.length) + ' are reachable, ' + allNavPages.length + ' swept for visibility)');
} else {
  fs.writeFileSync(SERVER_OUT, serverBody);
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), OUT) + ' and the server copy — ' +
              pages.length + ' pages, ' + pages.filter((p) => p.settingsKey).length +
              ' with a settings toggle');
}
