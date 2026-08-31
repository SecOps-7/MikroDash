'use strict';
/**
 * `/api/localcc` must be asked for when the session is UP, not at boot.
 *
 * ---- THE DEFECT ------------------------------------------------------------
 *
 * The Connections map draws every arc FROM the router's own country. That comes
 * from `/api/localcc`, which reads the active session's last WAN address. The
 * port fetched it ONCE at module init and never again — and every page module is
 * initialised at BOOT, when the router session has not settled and
 * `dhcpNetworks` has not produced a payload. So it answered `{"cc":""}`, the
 * guard failed, and `localCC` stayed `ZZ` for the life of the page. `ZZ` has no
 * centroid, so `arcs()` returns immediately: countries colour, counts update,
 * and NO ARC OR COMET IS EVER DRAWN.
 *
 * Reported by the operator on 2026-08-28 while testing the port beside the live
 * app. Invisible to every existing check: the endpoint is correct (it returns
 * `DE` given a live session), the arc and comet code is correct, and
 * `pages/connections` is the one page module with no gate —
 * `tools/page-gate-audit.js` records it as attempted-and-abandoned because the
 * SVG map needs a browser.
 *
 * ---- WHAT THIS PINS, AND WHY IT IS A SOURCE CHECK -------------------------
 *
 * The timing, which is the whole bug. Three properties, all readable from the
 * source and none of them from the DOM:
 *
 *   1. the fetch is NOT at module top level — it lives in a function
 *   2. that function is called from the `conn:update` handler, which is the
 *      live app's own arrangement (`app.js:4782`)
 *   3. the once-flag is reset on `connect` AND on a failed fetch, so a
 *      reconnect re-asks and a transient error does not cost the map its arcs
 *      permanently
 *
 *   node tools/localcc-timing-check.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const src = fs.readFileSync(
  path.join(ROOT, 'web', 'src', 'pages', 'connections.ts'), 'utf8');
const body = src.replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/\/\/[^\n]*/g, ' ');

const problems = [];

// 1. THE FETCH IS INSIDE A FUNCTION. A bare `fetch('/api/localcc')` at the
//    indentation of the init body is the defect exactly as it was.
const bare = /\n {2}fetch\(\s*'\/api\/localcc'/.test(body);
if (bare) {
  problems.push("`fetch('/api/localcc')` is called at the top level of initConnectionsPage. Every "
    + 'page module is initialised at BOOT, before the router session has settled, so it answers '
    + 'an empty country and the map never draws an arc. Call it when connection data arrives.');
}
if (!/function fetchLocalCCOnce\(\)/.test(body)) {
  problems.push('fetchLocalCCOnce is gone; if the fetch moved somewhere else, move this check too');
}

// 2. IT IS DRIVEN BY conn:update. Anchored on structure: the handler's body,
//    from its registration to the next `socket.on(`.
const at = body.indexOf("socket.on('conn:update'");
if (at < 0) {
  problems.push("no `socket.on('conn:update'` handler — the anchor is gone");
} else {
  const next = body.indexOf("socket.on(", at + 10);
  const handler = body.slice(at, next < 0 ? body.length : next);
  if (!/fetchLocalCCOnce\(\)/.test(handler)) {
    problems.push('the conn:update handler does not call fetchLocalCCOnce. That call is what makes '
      + 'the fetch happen when the session is UP — connection data arriving is the proof of it.');
  }
}

// 3. THE FLAG RESETS, both ways.
if (!/socket\.on\('connect',\s*\(\)\s*=>\s*\{\s*localCCFetched = false/.test(body)) {
  problems.push('the once-flag is not reset on `connect`. A reconnect is a new socket and a new '
    + 'session; the live app resets it there and so must this.');
}
if (!/catch\(\(\)\s*=>\s*\{\s*localCCFetched = false/.test(body)) {
  problems.push('a failed fetch does not reset the once-flag, so one transient error costs the map '
    + 'its arcs for the life of the page. The live app resets in its catch.');
}

if (problems.length) {
  console.error('localcc-timing-check FAILED:');
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log('localcc-timing-check: the arc origin is fetched lazily from conn:update, and the '
  + 'once-flag resets on reconnect and on failure');
