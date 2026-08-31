'use strict';
/**
 * Two things that must be true the moment the app finishes loading:
 * it lands on the DASHBOARD, and the router control names the active router.
 *
 * ---- WHY BOTH ARE HERE ----------------------------------------------------
 *
 * They are the same defect twice: state that only a USER ACTION ever set, in an
 * app whose first paint has had no user action yet.
 *
 *   THE LANDING PAGE was `'dns'` — a leftover from the first vertical slice,
 *   when DNS was the only ported page and landing anywhere else meant landing on
 *   nothing. It outlived that by twenty-two pages. The live app opens on the
 *   dashboard: `_currentPage = 'dashboard'`, and its markup carries
 *   `<div class="page-view active" id="page-dashboard">`.
 *
 *   THE ACTIVE ROUTER was assigned only inside the dropdown's `onChoose`, so
 *   until the operator picked one by hand it stayed '' and `refreshLabel`
 *   rendered '—'. The server's `router:active` arrives and its only handler
 *   manages room membership.
 *
 * Both reported by the operator on 2026-08-28, in one message.
 *
 *   node tools/landing-and-active-check.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const main = fs.readFileSync(path.join(ROOT, 'web', 'src', 'main.ts'), 'utf8');
const body = main.replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/\/\/[^\n]*/g, ' ');
const problems = [];

// ---- The landing page ------------------------------------------------------
const land = /showPage\(socket,\s*currentPage \|\| '([a-z]+)'\)/.exec(body);
if (!land) {
  problems.push('the landing-page call could not be found — it is `showPage(socket, currentPage || '
    + "'<page>')` in select()");
} else if (land[1] !== 'dashboard') {
  problems.push(`the app lands on '${land[1]}'. The live app opens on the dashboard, and anything `
    + 'else is the first-vertical-slice default outliving the slice.');
}

// AND THE DASHBOARD MUST ACTUALLY BE MOUNTED, or landing there is a blank page.
if (!/PORTED = new Set\(\[[^\]]*'dashboard'/.test(body)) {
  problems.push("'dashboard' is not in PORTED, so navigating to it hands the browser to Node — "
    + 'which in standalone does not exist, and the redirect lands back on the default page. That '
    + 'is the loop the operator hit.');
}
// `web/build.mjs` became `cmd/webbuild` on 2026-08-31 when the build stopped
// needing Node. Same declarations, same job — this follows them there. The
// emptiness guard below is what makes that safe: a regex that stopped matching
// fails loudly instead of reporting live code as dead.
const build = fs.readFileSync(path.join(ROOT, 'cmd', 'webbuild', 'main.go'), 'utf8');
if (!/var PAGES = \[\]string\{[^}]*"dashboard"/.test(build)) {
  problems.push("'dashboard' is not in cmd/webbuild's PAGES, so its 43 KB of extracted markup is never "
    + 'composed into index.html. Its nineteen modules then initialise against a DOM that does not '
    + 'contain them and every one no-ops in silence.');
}
const ui = path.join(ROOT, 'web', 'src', 'ui', 'page-dashboard.html');
if (!fs.existsSync(ui)) {
  problems.push('web/src/ui/page-dashboard.html is missing; the body cannot be composed');
} else if (!/id="page-dashboard"/.test(fs.readFileSync(ui, 'utf8'))) {
  problems.push('page-dashboard.html has no #page-dashboard — showPage would find nothing to show');
}

// ---- The active router -----------------------------------------------------
//
// Assigned where the router is CHOSEN FOR the operator, not only where they
// choose it themselves. `select()` runs on first load and on every reconnect.
const at = body.indexOf('const select = () => {');
if (at < 0) {
  problems.push('anchor lost: `const select = () => {` in main.ts');
} else {
  const sel = body.slice(at, body.indexOf('};', at));
  if (!/activeRouterId = /.test(sel)) {
    problems.push('select() does not set activeRouterId. It is then only ever set by the '
      + "dropdown's own onChoose, so on a fresh load the control renders '—' until the operator "
      + 'picks a router by hand.');
  }
  if (!/dropdown\.refresh\(\)/.test(sel)) {
    problems.push('select() does not refresh the dropdown, so the label keeps whatever it had');
  }
}

if (problems.length) {
  console.error('landing-and-active-check FAILED:');
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log('landing-and-active-check: the app lands on a mounted dashboard, and select() sets the '
  + 'active router and refreshes the control');
