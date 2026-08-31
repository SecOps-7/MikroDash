'use strict';
/**
 * A reconnect must not move the operator.
 *
 * ---- THE SHAPE -------------------------------------------------------------
 *
 * Upstream `d7548b0` fixed the traffic interface being lost on a socket
 * reconnect, and the report that came with it generalised: "per-connection state
 * keyed on the connection cannot outlive it, and a reconnect is not a new user.
 * Anything the operator chose — an interface, a filter, a page, a sort order —
 * needs somewhere with a longer life than the socket."
 *
 * Sweeping this port for that shape found a worse instance than the one being
 * reported. `main.ts`'s `select()` runs on EVERY connect, because the server
 * holds the router selection on the CONNECTION — and it called
 * `showPage(socket, 'dns')` with a literal. So a network blip did not merely
 * lose a chart's interface: it navigated the operator off whatever page they
 * were reading and back to DNS.
 *
 * `currentPage` is '' until the first call, so `currentPage || 'dns'` lands on
 * the default once and re-asserts the real page every time after.
 *
 * ---- WHAT THIS CHECKS ------------------------------------------------------
 *
 * That the connect handler re-asserts rather than overrides. A literal page name
 * inside `select()` is the defect coming back.
 *
 *   node tools/reconnect-state-check.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const src = fs.readFileSync(path.join(ROOT, 'web', 'src', 'main.ts'), 'utf8');

const problems = [];

// The connect handler, anchored on STRUCTURE — the declaration and its closing
// brace — rather than on a character distance. The peer's own note on `d7548b0`:
// "anchor a source scan on structure, not on proximity", after a fixed window
// found the wrong one of four handlers.
const at = src.indexOf('const select = () => {');
if (at < 0) throw new Error('anchor lost: `const select = () => {` in main.ts');
const end = src.indexOf('\n  };', at);
if (end < 0) throw new Error('anchor lost: the closing brace of select()');
const body = src.slice(at, end).replace(/\/\/[^\n]*/g, ' ').replace(/\/\*[\s\S]*?\*\//g, ' ');

if (!/showPage\(/.test(body)) {
  problems.push('select() no longer calls showPage; if the page is asserted elsewhere on connect, '
    + 'move this check rather than deleting it');
}
// THE DEFECT: a literal page name. `currentPage || 'dns'` is the fix, so the
// only string literal allowed here is the fallback, and it must be reached
// through `currentPage`.
const call = /showPage\(\s*socket\s*,\s*([^)]*)\)/.exec(body);
if (!call) {
  problems.push('showPage is called in a shape this check cannot read');
} else {
  const arg = call[1].trim();
  if (!/\bcurrentPage\b/.test(arg)) {
    problems.push(`select() calls showPage(socket, ${arg}) — a fixed page. This runs on EVERY `
      + 'connect, so a reconnect would navigate the operator away from whatever they were '
      + 'reading. It must re-assert `currentPage`, falling back to the landing page only when '
      + 'there is not one yet.');
  }
}

// AND THE SOCKET-HELD SELECTION IS STILL RE-SENT. If `select` stopped running on
// connect the page would keep its state and lose its ROUTER, which is the same
// class of defect facing the other way.
if (!/socket\.on\('connect',\s*select\)/.test(src)) {
  problems.push("select is no longer bound to 'connect'; the server holds the router selection on "
    + 'the connection, so a reconnected socket would watch nothing');
}

if (problems.length) {
  console.error('reconnect-state-check FAILED:');
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log('reconnect-state-check: the connect handler re-asserts the page and the router '
  + 'rather than overriding them');
