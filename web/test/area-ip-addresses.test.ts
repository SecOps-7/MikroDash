/**
 * THE IP ADDRESSES PAGE, AS AN AREA (#97; migrated 2026-09-18).
 *
 * The page was hand-built — a module, markup, a collector — and is now a
 * declaration rendered by `web/src/pages/area.ts`. What the old page did that an
 * operator relied on is pinned here against the generic renderer, so the
 * migration is held to it rather than trusted:
 *
 * 1. EACH FAMILY EDITS ITS OWN MENU. An IPv6 row carries `data-res="ipv6Address"`,
 *    so clicking it opens /ipv6/address rather than failing at /ip/address. The
 *    old page did this per row; the area does it per tab.
 * 2. THE TWO FAMILIES ARE TWO TABS, and switching redraws from the payload
 *    already held, without waiting for a tick. (Deliberately changed: the old
 *    page was one table with a Family column.)
 * 3. A DISABLED OR INVALID ROW IS DIMMED, and a dynamic one says so: the old
 *    page's state column, as columns.
 * 4. NOTHING WITHOUT A ROUTER ANSWER READS AS A VALUE: an absent field is a dash.
 *
 * The real page module and the real generated declaration table are bundled and
 * driven; nothing here reimplements either.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.area-entry.ts');
fs.writeFileSync(ENTRY,
  "export { initAreaPages } from '../web/src/pages/area.js';\n" +
  "export { AREAS } from '../web/src/gen/areas.js';\n");
const OUT = path.join(ROOT, 'testdata', '.area.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const KEY = 'ip-addresses';

/** Every area's shell ids, so an id the renderer asks for and this forgot is caught. */
function shellIds(areas) {
  return areas.flatMap((a) => ['areaBody-', 'areaBadge-', 'areaTabs-', 'areaAdd-'].map((p) => p + a.key));
}

function boot() {
  // A FRESH MODULE PER CASE: the renderer keeps its tab and payload state at
  // module level, so a cached bundle would carry one case's tab into the next.
  delete require.cache[require.resolve(OUT)];
  const mod = require(OUT);
  const doc = makeDoc(shellIds(mod.AREAS), { query: { '[data-res-add]': [], '[data-res-rows]': [] } });
  const handlers = {};
  const socket = { on: (ev, fn) => { handlers[ev] = fn; }, emit: () => {} };
  const prevDoc = global.document;
  const prevWin = global.window;
  global.document = doc;
  global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
  mod.initAreaPages(socket, (page) => page === KEY);
  return {
    doc, areas: mod.AREAS,
    send: (p) => handlers['area:update'](p),
    body: () => String(doc.nodes['areaBody-' + KEY].innerHTML),
    tabs: () => String(doc.nodes['areaTabs-' + KEY].innerHTML),
    clickTab: (i) => doc.dispatch('click', {
      closest: () => ({ getAttribute: (k) => (k === 'data-areatab' ? KEY : String(i)) }),
    }),
    restore: () => { global.document = prevDoc; global.window = prevWin; },
  };
}

function row(id, identity, values) {
  return { id, identity, values };
}

function payload() {
  return {
    ts: 1, pollMs: 60000, area: KEY, title: 'IP Addresses', denied: false,
    tables: [
      { resource: 'ipAddress', title: 'IPv4', unsupported: false,
        columns: ['address', 'network', 'interface', 'disabled', 'dynamic', 'invalid', 'comment'],
        rows: [
          row('*1', '198.51.100.15/24', { address: '198.51.100.15/24', network: '198.51.100.0',
            interface: 'ether1', disabled: 'false', dynamic: 'true', invalid: 'false' }),
          row('*2', '198.51.100.1/24', { address: '198.51.100.1/24', network: '198.51.100.0',
            interface: 'bridge', disabled: 'true', dynamic: 'false', invalid: 'false', comment: 'lab' }),
        ] },
      { resource: 'ipv6Address', title: 'IPv6', unsupported: false,
        columns: ['address', 'interface', 'advertise', 'disabled', 'dynamic', 'invalid', 'comment'],
        rows: [
          row('*A', '2001:db8:1::1/64', { address: '2001:db8:1::1/64', interface: 'gone',
            advertise: 'false', disabled: 'false', dynamic: 'false', invalid: 'true' }),
        ] },
    ],
  };
}

/** The opening `<tr …>` tag of the row with this id. */
function rowTag(html, id) {
  // EVERY metacharacter escaped, not just the first `*` (code scanning #163):
  // a RouterOS id is `*1A`, but a helper that escapes one character is a regex
  // built from data waiting for the id that has two.
  const m = html.match(new RegExp('<tr[^>]*data-id="' + id.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '"[^>]*>'));
  return m ? m[0] : '';
}

/** The whole row with this id. */
function rowHtml(html, id) {
  const tag = rowTag(html, id);
  if (!tag) return '';
  const at = html.indexOf(tag);
  return html.slice(at, html.indexOf('</tr>', at) + 5);
}

// ── the declaration is the one the page is drawn from ───────────────────────
{
  const { areas, restore } = boot();
  const area = areas.find((a) => a.key === KEY);
  assert.ok(area, 'no ip-addresses area in the generated table');
  assert.deepStrictEqual(area.tables.map((t) => t.resource), ['ipAddress', 'ipv6Address'],
    'the IP Addresses area is not IPv4 then IPv6');
  restore();
  say('ok  the generated declaration holds IPv4 and IPv6, in that order');
}

// ── 1 and 2: each tab edits its own family, and switching redraws ───────────
{
  const { send, body, tabs, clickTab, restore } = boot();
  send(payload());
  assert.ok(/data-areatabindex="0"[^>]*>IPv4</.test(tabs()) && />IPv6</.test(tabs()),
    'the two families are not two tabs:\n' + tabs());
  let html = body();
  assert.ok(html.includes('data-res-rows="ipAddress"'), 'the IPv4 table is not bound to ipAddress:\n' + html);
  assert.ok(/data-res="ipAddress"/.test(rowTag(html, '*1')),
    'an IPv4 row is not addressable through ipAddress:\n' + html);
  assert.ok(!html.includes('2001:db8'), 'IPv6 rows leaked into the IPv4 tab');

  clickTab(1);
  html = body();
  assert.ok(html.includes('data-res-rows="ipv6Address"'), 'the IPv6 tab is not bound to ipv6Address:\n' + html);
  assert.ok(/data-res="ipv6Address"/.test(rowTag(html, '*A')),
    'an IPv6 row would edit the wrong menu:\n' + html);
  assert.ok(!html.includes('198.51.100.15'), 'IPv4 rows leaked into the IPv6 tab');
  restore();
  say('ok  IPv4 and IPv6 are two tabs, each row bound to its own family\'s resource');
}

// ── 3: disabled and invalid rows are dimmed; a live row is not ──────────────
{
  const { send, body, clickTab, restore } = boot();
  send(payload());
  let html = body();
  assert.ok(rowTag(html, '*1'), 'the dynamic row did not render:\n' + html);
  assert.ok(!/opacity/.test(rowTag(html, '*1')), 'an enabled, valid row is dimmed:\n' + rowTag(html, '*1'));
  assert.ok(/opacity:\.55/.test(rowTag(html, '*2')), 'a disabled row is not dimmed:\n' + rowTag(html, '*2'));
  // A pill since 2026-09-18: `dynamic` is a CommonPills flag, drawn "yes" in blue.
  assert.ok(/<td><span class="vpn-hs-badge hs-info">yes<\/span><\/td>/.test(rowHtml(html, '*1')),
    'the dynamic row does not say it is dynamic:\n' + rowHtml(html, '*1'));
  clickTab(1);
  html = body();
  assert.ok(/opacity:\.55/.test(rowTag(html, '*A')), 'an invalid row is not dimmed:\n' + rowTag(html, '*A'));
  restore();
  say('ok  disabled and invalid rows are dimmed; dynamic says so in its column');
}

// ── 4: an absent field is a dash, not an empty value ────────────────────────
{
  const { send, body, restore } = boot();
  send(payload());
  // *1 has no comment, which is its last column: the last cell must be the dash.
  const r = rowHtml(body(), '*1');
  assert.ok(/&mdash;<\/span><\/td><\/tr>$/.test(r), 'a missing comment rendered as an empty cell:\n' + r);
  // and *2 HAS one, so the probe can tell the two apart.
  assert.ok(/<td>lab<\/td><\/tr>$/.test(rowHtml(body(), '*2')), 'the control row lost its comment');
  restore();
  say('ok  a field the router did not answer renders as a dash');
}

fs.rmSync(OUT, { force: true });
