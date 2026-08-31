'use strict';
/**
 * Lift the markup and the stylesheet out of the live app, verbatim.
 *
 * WHY THIS IS GENERATED RATHER THAN RETYPED. The hard constraint on this port
 * is that nothing user-visible may change: "The TypeScript frontend keeps the
 * existing stylesheet and DOM contract ... every class, the element ids and the
 * DOM shape. Only the logic that produces the DOM is rewritten." A page whose
 * markup was retyped by hand is a page that differs by whatever the typist got
 * wrong, and the difference would be invisible until somebody compared
 * screenshots. Extraction cannot drift; transcription can only drift.
 *
 * It also keeps the constraint honest in the other direction. `../MikroDash` is
 * read here and never written, and `--check` fails when an extract is stale, so
 * a change to the live markup surfaces as a failing check rather than as two
 * pages that quietly stopped matching.
 *
 * What comes out:
 *   web/public/app.css        the embedded <style> block
 *   web/src/ui/shell.html     everything in <body> that is not a page, with a
 *                             <!--PAGES--> marker where the pages were
 *   web/src/ui/page-<key>.html   one file per page-view
 *
 * The external stylesheets (/vendor/tabler.min.css, /css/*.css) are NOT copied:
 * the Node app still serves them and the Go server proxies them, so they are
 * shared rather than duplicated.
 *
 *   node tools/extract-ui.js            write
 *   node tools/extract-ui.js --check    exit 1 if anything is stale
 */

const fs   = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const SRC  = path.join(LIVE, 'public', 'index.html');
const OUT_CSS  = path.join(__dirname, '..', 'web', 'public', 'app.css');
const OUT_UI   = path.join(__dirname, '..', 'web', 'src', 'ui');

// Only these pages are extracted. A page not listed is one nobody has ported,
// and shipping its markup with no renderer behind it would produce a nav entry
// leading to a permanently empty page — worse than its absence.
const PAGES = ['dashboard', 'dns', 'bridges', 'vlans', 'wan', 'packages', 'routing', 'dhcp', 'ppp', 'vpn',
               'rosusers', 'queues', 'firewall', 'wifi', 'capsman', 'interfaces', 'logs',
               'topology', 'wireless', 'bandwidth', 'connections', 'reports', 'audit', 'backups',
               // Renamed upstream from 'routers' to 'devices' (#117) — a fleet holds
               // switches too. The KEY moved as well as the title; see the drift
               // entry in PORT-QUEUE.md.
               'devices', 'settings'];

function sliceStyle(html) {
  const open = html.indexOf('<style>');
  const close = html.indexOf('</style>', open);
  if (open === -1 || close === -1) throw new Error('no <style> block in index.html');
  return html.slice(open + '<style>'.length, close).replace(/^\n/, '') + '\n';
}

/**
 * Every page-view block, by id.
 *
 * Matched on indentation rather than by counting tags: the page-views are all
 * at two spaces and close at two spaces, and a brace counter over HTML would
 * have to understand void elements and comments to be right. The closing line
 * may carry a trailing comment — the dashboard's does.
 */
function slicePages(html) {
  const lines = html.split('\n');
  const out = new Map();
  for (let i = 0; i < lines.length; i++) {
    // THE CLASS LIST MAY CARRY MORE THAN `page-view`. The dashboard ships with
    // `class="page-view active"` because it is the page shown first, and an exact
    // match silently skipped it — the one page-view this never extracted, which
    // is why the omission went unnoticed. The comment above already knew the
    // dashboard was the odd one out and covered only its CLOSING line.
    const m = lines[i].match(/^ {2}<div class="page-view(?: [a-z-]+)*" id="page-([a-z]+)">/);
    if (!m) continue;
    let end = -1;
    for (let j = i + 1; j < lines.length; j++) {
      if (/^ {2}<\/div>/.test(lines[j])) { end = j; break; }
    }
    if (end === -1) throw new Error('page-' + m[1] + ' is never closed at its own indent');
    out.set(m[1], { from: i, to: end, html: lines.slice(i, end + 1).join('\n') + '\n' });
  }
  return out;
}

/** <body> content with every page-view replaced by one marker, and the script tags dropped. */
function sliceShell(html, pages) {
  const lines = html.split('\n');
  const bodyOpen = lines.findIndex(l => l.trim() === '<body>');
  const bodyClose = lines.findIndex(l => l.trim() === '</body>');
  if (bodyOpen === -1 || bodyClose === -1) throw new Error('no <body> in index.html');

  const spans = [...pages.values()].sort((a, b) => a.from - b.from);
  const drop = new Set();
  for (const s of spans) for (let i = s.from; i <= s.to; i++) drop.add(i);

  const out = [];
  for (let i = bodyOpen + 1; i < bodyClose; i++) {
    if (drop.has(i)) {
      if (i === spans[0].from) out.push('  <!--PAGES-->');
      continue;
    }
    // The old app's scripts belong to the old app. socket.io in particular must
    // not load here: this client speaks plain WebSocket to the Go server.
    if (/^<script src="\/(socket\.io\/|app\.js|vendor\/|js\/)/.test(lines[i].trim())) continue;
    out.push(lines[i]);
  }
  return out.join('\n').replace(/\n{3,}/g, '\n\n').trim() + '\n';
}

function emit(file, body, check, stale) {
  if (check) {
    const cur = fs.existsSync(file) ? fs.readFileSync(file, 'utf8') : null;
    if (cur !== body) stale.push(path.relative(path.join(__dirname, '..'), file));
    return;
  }
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, body);
}

function main() {
  const check = process.argv.includes('--check');
  if (!fs.existsSync(SRC)) {
    console.error('cannot read ' + SRC + ' — set MIKRODASH_SRC');
    process.exit(1);
  }
  const html  = fs.readFileSync(SRC, 'utf8');
  const pages = slicePages(html);

  const missing = PAGES.filter(p => !pages.has(p));
  if (missing.length) {
    console.error('index.html has no page-view for: ' + missing.join(', '));
    process.exit(1);
  }

  const stale = [];
  emit(OUT_CSS, sliceStyle(html), check, stale);

  // ── THE STANDALONE STYLESHEETS AND THE VENDOR TREE ──────────────────────
  //
  // `app.css` is SLICED out of index.html's <style> block, so it cannot drift.
  // These are separate files the live app links, and they were HAND-COPIED —
  // which means nothing noticed when one changed upstream. A stylesheet that
  // has silently fallen behind is the exact failure this whole extraction
  // exists to prevent, and it is worse here than for markup: a class the port
  // renders and the stylesheet no longer styles produces an unstyled element,
  // not an error.
  //
  // COPIED, not parsed: they are whole files, and the port's rule is that it
  // "reuses the existing stylesheet, class names, element ids and DOM shape
  // verbatim". `--check` is what turns a copy into a contract.
  //
  // The VENDOR files (Chart.js, Tabler, the web fonts) are third-party and
  // belong to neither app — they are pinned here for the same reason, so an
  // upstream version bump is a visible diff rather than two apps quietly
  // running different chart libraries.
  for (const rel of ['css/app-fonts.css', 'css/dashboard-grid.css', 'css/topology.css',
    'vendor/tabler.min.css', 'vendor/chart.umd.min.js', 'vendor/fonts/fonts.css']) {
    const from = path.join(LIVE, 'public', rel);
    if (!fs.existsSync(from)) {
      console.error('the live app no longer has public/' + rel + ' — it was linked by index.html '
        + 'and copied here. Find out what replaced it rather than deleting this line.');
      process.exit(1);
    }
    emit(path.join(__dirname, '..', 'web', 'public', rel), fs.readFileSync(from, 'utf8'),
      check, stale);
  }
  emit(path.join(OUT_UI, 'shell.html'), sliceShell(html, pages), check, stale);

  // ── login.html ──────────────────────────────────────────────────────────
  //
  // MARKUP, so it is extracted rather than authored — the same rule as the page
  // bodies, and for the same reason: retyping it is how the two documents drift.
  // It is a whole separate file rather than a slice of index.html, so this is a
  // copy.
  //
  // Its three references — /vendor/tabler.min.css, /vendor/fonts/fonts.css and
  // /login.js — all resolve against this app: the first two are pinned by the
  // loop below, and `/login.js` is now BUILT from `web/src/entry/login.ts` and served
  // from `dist`, rather than being the live repo's file shipped verbatim.
  emit(path.join(OUT_UI, 'login.html'),
    fs.readFileSync(path.join(LIVE, 'public', 'login.html'), 'utf8'), check, stale);
  for (const key of PAGES) emit(path.join(OUT_UI, 'page-' + key + '.html'), pages.get(key).html, check, stale);

  if (check) {
    if (stale.length) {
      console.error('stale extracts:\n  ' + stale.join('\n  ') + '\nrun: node tools/extract-ui.js');
      process.exit(1);
    }
    console.log('ui extracts up to date');
    return;
  }
  console.log('extracted the stylesheet, the shell and ' + PAGES.length + ' page(s): ' + PAGES.join(', '));
  console.log('pages available but not ported: ' +
    [...pages.keys()].filter(k => !PAGES.includes(k)).join(', '));
}

main();
