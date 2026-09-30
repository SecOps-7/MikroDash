/**
 * THE TOPBAR PICKER GROUPS BY SITE (issue #144).
 *
 * eltionb runs 15+ sites and 100+ devices, and the picker was one flat list.
 * It now carries a chip strip that filters to one site and draws site headings
 * when All is selected.
 *
 * ── THE THINGS THAT WOULD BE WRONG AND LOOK RIGHT ──────────────────────────
 *
 * 1. A device in SEVERAL sites (#117) drawn once. It would look fine under the
 *    first site and be missing under the second, which reads as a membership
 *    that was never saved.
 * 2. A device in NO site dropped. A site-shaped control is exactly where that
 *    device goes missing, and nothing else in the app would say so.
 * 3. `hl` indexed per group. The arrow keys would stick at the first group's
 *    length, and the highlight would appear on one row per site.
 * 4. A site name concatenated unescaped. It is operator text, and the sibling
 *    control in this same module shipped that bug - see
 *    `router-select-escape.test.ts`.
 * 5. A chip for a site the fleet is not actually in, which filters to nothing.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.rtr-dd-sites-entry.ts');
fs.writeFileSync(ENTRY,
  "export { siteChips, routersInSite, groupRoutersBySite, flattenGroups, dropdownHtml, chipsHtml,\n"
  + "  filterRouters, DD_SEARCH_MIN } from '../web/src/router-dropdown.js';\n"
  + "export { SITE_UNASSIGNED } from '../web/src/dom.js';\n");
const OUT = path.join(ROOT, 'testdata', '.rtr-dd-sites.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);
fs.rmSync(OUT, { force: true });

interface R { id: string; label?: string; host?: string; disabled?: boolean; siteIds?: string[] }
interface Chip { id: string; name: string; count: number }
interface Group { site: Chip | null; rows: R[] }
const {
  siteChips, routersInSite, groupRoutersBySite, flattenGroups, dropdownHtml, chipsHtml,
  filterRouters, DD_SEARCH_MIN, SITE_UNASSIGNED,
} = mod as {
  siteChips(rows: R[], names: Record<string, string>): Chip[];
  routersInSite(rows: R[], siteId: string): R[];
  groupRoutersBySite(rows: R[], chips: Chip[]): Group[];
  flattenGroups(groups: Group[]): R[];
  dropdownHtml(g: Group[], activeId: string, st: Record<string, boolean | undefined>, hl: number): string;
  chipsHtml(chips: Chip[], selected: string): string;
  filterRouters(rows: R[], filter: string, names?: Record<string, string>): R[];
  DD_SEARCH_MIN: number;
  SITE_UNASSIGNED: string;
};

// Berlin holds two devices, Frankfurt one, `both` is in BOTH, `loose` is in
// none, and `off` is disabled.
const FLEET: R[] = [
  { id: 'r1', label: 'hAP-1', host: '10.0.0.1', siteIds: ['ber'] },
  { id: 'r2', label: 'hAP-2', host: '10.0.0.2', siteIds: ['ber'] },
  { id: 'r3', label: 'CCR-1', host: '10.1.0.1', siteIds: ['fra'] },
  { id: 'both', label: 'Edge-1', host: '10.9.0.1', siteIds: ['ber', 'fra'] },
  { id: 'loose', label: 'Spare', host: '10.9.9.9', siteIds: [] },
  { id: 'off', label: 'Retired', host: '10.9.9.8', siteIds: ['ber'], disabled: true },
];
// A name map holding a site the FLEET IS NOT IN - the control for "chips are
// the sites devices are in, not the sites that exist".
const NAMES = { ber: 'Berlin DC', fra: 'Frankfurt', vie: 'Vienna' };
const LIVE = FLEET.filter((r) => !r.disabled);

// ── the chip strip ──────────────────────────────────────────────────────────
{
  const chips = siteChips(FLEET, NAMES);
  assert.deepStrictEqual(chips.map((c) => c.id), ['ber', 'fra', SITE_UNASSIGNED],
    'the chips are the sites the fleet is IN, alphabetically, with No site last');
  assert.ok(!chips.some((c) => c.id === 'vie'),
    'Vienna has no devices, so its chip would filter to an empty list');
  assert.deepStrictEqual(chips.map((c) => c.count), [3, 2, 1],
    'Berlin counts hAP-1, hAP-2 and Edge-1 and NOT the disabled Retired; '
    + 'Frankfurt counts CCR-1 and Edge-1; a device in two sites counts in both');
  assert.strictEqual(chips[2]!.name, 'No site');
}

// ── a deleted site still reaches its devices ────────────────────────────────
{
  const chips = siteChips([{ id: 'r9', siteIds: ['ghost'] }], {});
  assert.deepStrictEqual(chips.map((c) => c.id), ['ghost'],
    'a device listing a site that no longer resolves lost its only chip, so '
    + 'nothing in the strip reaches it');
  assert.strictEqual(chips[0]!.name, 'ghost',
    'the raw id is shown, which reads as the fault it is rather than as a place');
}

// ── which devices one chip shows ────────────────────────────────────────────
{
  assert.strictEqual(routersInSite(FLEET, '').length, FLEET.length,
    'the All chip filters nothing');
  assert.deepStrictEqual(routersInSite(FLEET, 'fra').map((r) => r.id), ['r3', 'both'],
    'a site shows its members, the device shared with Berlin included');
  assert.deepStrictEqual(routersInSite(FLEET, SITE_UNASSIGNED).map((r) => r.id), ['loose'],
    'No site shows exactly the devices with no membership');
}

// ── the groups, and the device that is in two of them ───────────────────────
{
  const groups = groupRoutersBySite(LIVE, siteChips(FLEET, NAMES));
  assert.deepStrictEqual(groups.map((g) => (g.site ? g.site.name : 'No site')),
    ['Berlin DC', 'Frankfurt', 'No site'],
    'the headings follow the chip order, so the two cannot disagree');
  assert.deepStrictEqual(groups[0]!.rows.map((r) => r.id), ['r1', 'r2', 'both']);
  assert.deepStrictEqual(groups[1]!.rows.map((r) => r.id), ['r3', 'both'],
    'the device in two sites is missing from its second site');
  assert.deepStrictEqual(groups[2]!.rows.map((r) => r.id), ['loose'],
    'the device in no site vanished, which is the one a site control loses');

  // THE DRAWN ORDER, which is what the keyboard walks.
  assert.deepStrictEqual(flattenGroups(groups).map((r) => r.id),
    ['r1', 'r2', 'both', 'r3', 'both', 'loose'],
    'the flat list must match what is drawn, twice-drawn device included, or '
    + 'the arrow keys and the rows disagree about which row is which');
}

// ── an empty group is not a heading over nothing ────────────────────────────
{
  // What a search for "CCR" leaves.
  const groups = groupRoutersBySite([FLEET[2]!], siteChips(FLEET, NAMES));
  assert.deepStrictEqual(groups.map((g) => (g.site ? g.site.name : 'No site')), ['Frankfurt'],
    'Berlin and No site kept their headings with no rows under them, which '
    + 'reads as a site that has gone down');
}

// ── headings appear only when there is something to separate ────────────────
{
  const many = dropdownHtml(groupRoutersBySite(LIVE, siteChips(FLEET, NAMES)), 'r1', {}, -1);
  assert.ok(many.includes('rtr-dd-head'), 'All with several sites draws headings');
  assert.ok(many.includes('Berlin DC') && many.includes('Frankfurt'));

  const one = dropdownHtml([{ site: { id: 'fra', name: 'Frankfurt', count: 2 }, rows: [FLEET[2]!] }],
    'r1', {}, -1);
  assert.ok(!one.includes('rtr-dd-head'),
    'a single group still drew a heading - with a chip selected that names the '
    + 'site the chip strip and the button already name');

  assert.ok(dropdownHtml([], 'r1', {}, -1).includes('rtr-dd-empty'), 'nothing matched');
  assert.ok(dropdownHtml([{ site: null, rows: [] }], 'r1', {}, -1).includes('rtr-dd-empty'),
    'a group holding no rows is still "no routers match", not an empty panel');
}

// ── THE HIGHLIGHT COUNTS ACROSS GROUPS, NOT WITHIN ONE ──────────────────────
{
  const groups = groupRoutersBySite(LIVE, siteChips(FLEET, NAMES));
  // Index 3 in DRAWN order is CCR-1, the first row of the SECOND group. A
  // per-group index would highlight the first row of every group instead.
  const html = dropdownHtml(groups, 'zzz', {}, 3);
  const highlighted = [...html.matchAll(/<div class="rtr-dd-item([^"]*)"[^>]*data-rtr="([^"]+)"/g)]
    .filter((m) => / hl\b/.test(m[1]!))
    .map((m) => m[2]!);
  assert.deepStrictEqual(highlighted, ['r3'],
    'the highlight is not on the single fourth row drawn; a per-group index '
    + 'puts one on the first row of each site');
}

// ── ESCAPING: a site name is operator text ──────────────────────────────────
{
  const evil = '<img src=x onerror=alert(1)> & "HQ"';
  const chips: Chip[] = [{ id: 's1', name: evil, count: 1 }, { id: 's2', name: 'ok', count: 1 }];

  const strip = chipsHtml(chips, '');
  assert.ok(!strip.includes('<img'), 'a site name reached the chip markup as a tag: ' + strip);
  assert.ok(strip.includes('&lt;img'), 'the escaped name is shown');

  const html = dropdownHtml([{ site: chips[0]!, rows: [{ id: 'r1' }] },
    { site: chips[1]!, rows: [{ id: 'r2' }] }], '', {}, -1);
  assert.ok(!html.includes('<img'), 'a site name reached the heading as a tag: ' + html);
  assert.ok(html.includes('&lt;img'));
}

// ── the strip earns its place, or it is not drawn ───────────────────────────
{
  assert.strictEqual(chipsHtml([{ id: 'ber', name: 'Berlin DC', count: 4 }], ''), '',
    'a fleet in one site gets a chip strip whose only choice is the one it is '
    + 'already showing');
  assert.strictEqual(chipsHtml([], ''), '', 'a fleet in no site at all gets no strip');

  const two: Chip[] = [{ id: 'ber', name: 'Berlin DC', count: 4 },
    { id: 'fra', name: 'Frankfurt', count: 2 }];
  const strip = chipsHtml(two, 'fra');
  assert.strictEqual((strip.match(/rtr-dd-chip on/g) || []).length, 1,
    'exactly one chip is selected');
  assert.ok(/class="rtr-dd-chip on" data-site="fra"/.test(strip),
    'the selected chip is the one that was chosen');
  assert.strictEqual([...strip.matchAll(/data-site="/g)].length, 3, 'All plus the two sites');
  assert.ok(strip.indexOf('data-site=""') < strip.indexOf('data-site="ber"'),
    'All is the first chip');

  assert.ok(/class="rtr-dd-chip on" data-site=""/.test(chipsHtml(two, '')),
    'with no site chosen it is All that is marked, not nothing');
}

// ── THE SEARCH FINDS A SITE ────────────────────────────────────────────────
//
// In a picker organised by site, typing the site is the obvious thing to try,
// and it was the one query that found nothing: the filter read the label and
// the host only.
{
  assert.deepStrictEqual(filterRouters(FLEET, 'frankfurt', NAMES).map((r) => r.id),
    ['r3', 'both'],
    'typing a site name found nothing, which is the query a site-grouped '
    + 'picker invites first');
  assert.deepStrictEqual(filterRouters(FLEET, 'berlin', NAMES).map((r) => r.id),
    ['r1', 'r2', 'both'],
    'the site match must reach every member, the device shared with Frankfurt '
    + 'included');
  // THE CONTROL FOR THE MATCHES ABOVE: Vienna is in the name map and no device
  // is in it, so a filter that matched on the map rather than on MEMBERSHIP
  // would return the whole fleet here.
  assert.deepStrictEqual(filterRouters(FLEET, 'vienna', NAMES).map((r) => r.id), [],
    'a site no device belongs to matched something');

  // THE CONTROLS: the two things it always matched still match, and a name map
  // is not required to use it.
  assert.deepStrictEqual(filterRouters(FLEET, 'hap', NAMES).map((r) => r.id), ['r1', 'r2'],
    'the label stopped matching');
  assert.deepStrictEqual(filterRouters(FLEET, '10.1.0', NAMES).map((r) => r.id), ['r3'],
    'the host stopped matching');
  assert.deepStrictEqual(filterRouters(FLEET, 'hap').map((r) => r.id), ['r1', 'r2'],
    'the name map is optional, so an existing caller keeps working');

  // A site that no longer resolves is matched under the id the chip shows for
  // it, or the device would be reachable from the strip but not the search.
  assert.deepStrictEqual(filterRouters([{ id: 'r9', siteIds: ['ghost'] }], 'ghost', {})
    .map((r) => r.id), ['r9'], 'an unresolvable site id is not searchable');

  // Disabled routers are dropped before the query, unchanged: Retired is in
  // Berlin DC and must not come back through a site match.
  assert.deepStrictEqual(filterRouters(FLEET, 'retired', NAMES).map((r) => r.id), [],
    'a disabled router came back through the search');
}

// ── the search box is offered on an ordinary fleet ─────────────────────────
//
// Not a restatement of the constant: the claim is that a fleet of two or three
// devices gets a box, which at the old value of 5 it did not.
{
  assert.ok(DD_SEARCH_MIN <= 2,
    'a three-device fleet gets no search box, so on an ordinary install the '
    + 'picker looks like it has no search at all');
  assert.ok(DD_SEARCH_MIN >= 2,
    'a single switchable router has nothing to search between, and a box over '
    + 'one row is furniture');
}

console.log('router-dropdown-sites: ok');
