/**
 * A SHARE VIEW NEVER USES A SIGNED-IN SESSION (the operator's ruling,
 * 2026-11-20).
 *
 * The share shell's fetch sends nothing with credentials, so a browser also
 * signed in to MikroDash lends a shared view nothing of that account. Only GET
 * leaves the page (and the ticket renewal). An /api/ read the link allows goes
 * to /share/api/ carrying the view ticket and never a cookie; any other /api/
 * call is refused on the spot, with no request at all.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.share-fetch-entry.ts');
fs.writeFileSync(ENTRY, "export { shareFetch } from '../web/src/share-fetch.js';\n");
const OUT = path.join(ROOT, 'testdata', '.share-fetch-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const { shareFetch } = require(OUT);
fs.rmSync(OUT, { force: true });

const ORIGIN = 'http://md.lan:3081';
const sent = [];
let status = 200;
const real = (input, init) => {
  sent.push({ url: String(input), init: init || {} });
  return Promise.resolve(new Response('{}', { status }));
};
const f = shareFetch(real, ['/api/apps/history', '/api/localcc'], () => 'TICKET-1', ORIGIN);

(async () => {
  // An allowed read: rewritten, with the ticket, without credentials.
  let r = await f('/api/apps/history?routerId=other&period=day');
  assert.strictEqual(r.status, 200);
  assert.strictEqual(sent.length, 1);
  assert.strictEqual(sent[0].url, '/share/api/apps/history?routerId=other&period=day');
  assert.strictEqual(sent[0].init.credentials, 'omit', 'a share read was sent with credentials');
  assert.strictEqual(sent[0].init.headers.Authorization, 'Share TICKET-1');
  assert.strictEqual(sent[0].init.method, 'GET');

  // A read the link does not allow: refused here, nothing sent.
  r = await f('/api/routers');
  assert.strictEqual(r.status, 403);
  r = await f(ORIGIN + '/api/auth/status');
  assert.strictEqual(r.status, 403);
  assert.strictEqual(sent.length, 1, 'a read not on the list reached the network');

  // Anything but GET: refused here, even to an allowed path.
  for (const method of ['POST', 'PUT', 'DELETE', 'PATCH']) {
    r = await f('/api/apps/history', { method });
    assert.strictEqual(r.status, 403, method + ' was let through');
  }
  r = await f(new Request(ORIGIN + '/api/localcc', { method: 'POST' }));
  assert.strictEqual(r.status, 403, 'a POST Request object was let through');
  assert.strictEqual(sent.length, 1, 'a write reached the network');

  // The renewal is the one POST, and it too goes without credentials.
  await f('/share/api/ticket', { method: 'POST', headers: { Authorization: 'Share TICKET-1' }, credentials: 'include' });
  assert.strictEqual(sent.length, 2);
  assert.strictEqual(sent[1].init.credentials, 'omit', 'the renewal asked for credentials and got them');

  // A static file: passed through, still without credentials.
  await f('/vendor/fonts/fonts.css', { credentials: 'same-origin' });
  assert.strictEqual(sent[2].init.credentials, 'omit');

  // A refused share read is retried once, then answered as refused.
  status = 404;
  r = await f('/api/localcc');
  assert.strictEqual(r.status, 404);
  assert.strictEqual(sent.length, 5, 'a refused share read was not retried exactly once');

  say('share fetch: no credentials ever, GET only, allowed reads via /share/api with the ticket, the rest refused unsent');
})().catch((e) => { console.error(e); process.exit(1); });
