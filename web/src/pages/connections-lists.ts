// The Connections page's lists: ports, countries, and the sparkline they carry.
//
// Kept apart from the map because they are STRING BUILDERS with no DOM state of
// their own — the map is a keyed diff over live SVG nodes, and mixing the two
// would make both harder to read. These take what they need as arguments and
// return markup, which also makes them testable without a page.

import { esc } from '../dom';
import { CC_NAMES, PORT_NAMES, iso2Flag } from './connections-map';

export interface ConnPort { port: string; count: number }

export interface ConnOrg { org: string; count: number; cat: string | null }

export interface ConnCountry {
  cc: string;
  city: string;
  count: number;
  proto: { tcp?: number; udp?: number; other?: number };
  orgs: ConnOrg[];
}

/** How many readings a country's sparkline keeps. */
export const SPARK_LEN = 20;

/** The service badge, shared with the Bandwidth page's org column. */
export function svcBadge(org: string, cat: string | null): string {
  if (!org) return '';
  return '<span class="svc-badge svc-' + (cat || 'other') + '">' + esc(org) + '</span>';
}

/**
 * A country's recent connection counts, as a 50x12 polyline.
 *
 * Under two readings there is no line to draw — one point is a dot, and a dot
 * next to a number says nothing the number did not. Empty string, and the row
 * simply has no sparkline until it has a history.
 */
export function drawSparkSVG(data: number[] | undefined): string {
  if (!data || data.length < 2) return '';
  const max = Math.max.apply(null, data) || 1;
  const w = 50, h = 12;
  const pts = data.map((v, i) =>
    (i * (w / (data.length - 1))).toFixed(1) + ',' + (h - (v / max * (h - 2)) - 1).toFixed(1)
  ).join(' ');
  return '<svg class="conn-sparkline" width="' + w + '" height="' + h + '" viewBox="0 0 ' + w + ' ' + h + '">' +
    '<polyline points="' + pts + '" fill="none" stroke="rgba(56,189,248,.6)" stroke-width="1.2" stroke-linejoin="round"/>' +
    '</svg>';
}

/**
 * The port list, as bars relative to the BUSIEST port shown.
 *
 * Relative rather than absolute because the question is "what is this router
 * mostly doing" — and on a quiet network the busiest port might be six
 * connections, which an absolute scale would draw as nothing at all.
 */
export function portListHTML(topPorts: ConnPort[]): string {
  if (!topPorts || !topPorts.length) return '<div class="empty-state">—</div>';
  const max = topPorts[0]!.count || 1;
  return topPorts.map((p) => {
    const pct = Math.round((p.count / max) * 100);
    const name = PORT_NAMES[p.port] || '';
    return '<div class="conn-port-row">' +
      '<span class="conn-port-num">' + p.port + '</span>' +
      '<span class="conn-port-name">' + name + '</span>' +
      // A four-pixel floor, so a port with one connection is still a mark on the
      // page rather than an invisible row.
      '<div class="conn-port-bar" style="width:' + Math.max(4, pct) + 'px"></div>' +
      '<span class="conn-port-count">' + p.count + '</span>' +
    '</div>';
  }).join('');
}

/**
 * One country's row.
 *
 * The protocol bar is three flex weights rather than three percentages, and the
 * third is `100 - tcp - udp` rather than its own rounding — otherwise three
 * rounded numbers add up to 99 or 101 and the bar is short or overflows.
 */
export function countryRowHTML(e: ConnCountry, spark: string, selected: boolean): string {
  const total = (e.proto.tcp || 0) + (e.proto.udp || 0) + (e.proto.other || 0) || 1;
  const tcpPct = Math.round((e.proto.tcp || 0) / total * 100);
  const udpPct = Math.round((e.proto.udp || 0) / total * 100);
  const othPct = 100 - tcpPct - udpPct;
  return '<div class="conn-map-row' + (selected ? ' selected' : '') + '" data-cc="' + e.cc + '">' +
    '<span class="conn-map-flag">' + iso2Flag(e.cc) + '</span>' +
    '<div style="flex:1;min-width:0">' +
      '<div style="display:flex;align-items:center;justify-content:space-between;gap:.4rem">' +
        '<div class="conn-map-label" style="min-width:0">' + esc(CC_NAMES[e.cc] || e.cc) +
          (e.city ? ' <span class="conn-map-label-sub">' + esc(e.city) + '</span>' : '') + '</div>' +
        (spark ? '<div style="flex-shrink:0">' + spark + '</div>' : '') +
      '</div>' +
      (e.orgs && e.orgs.length
        ? '<div class="svc-sub-rows">' + e.orgs.map((o) =>
          '<span class="svc-sub-row">' + svcBadge(o.org, o.cat) +
          '<span class="svc-sub-count">' + o.count + '</span></span>').join('') + '</div>'
        : '') +
      '<div class="conn-proto-bar">' +
        '<div class="conn-proto-tcp" style="flex:' + tcpPct + '"></div>' +
        '<div class="conn-proto-udp" style="flex:' + udpPct + '"></div>' +
        '<div class="conn-proto-other" style="flex:' + othPct + '"></div>' +
      '</div>' +
    '</div>' +
    '<span class="conn-map-count">' + e.count + '</span>' +
  '</div>';
}

/**
 * ── BUILD ONCE, SYNC AFTER ──────────────────────────────────────────────────
 *
 * The country list used to be rebuilt wholesale on every tick — `innerHTML =`
 * the whole thing, then a click listener bound to each row again. That is what
 * `ToDo #18` reported: a hovered row flickers because `innerHTML` destroys and
 * recreates the subtree, and a click can land on a node that was detached
 * between the pointer going down and coming up.
 *
 * The live app fixed it on 2026-08-25 by keeping the rows and writing only what
 * changed, and this follows it. `countryRowEl` makes the skeleton once;
 * `syncCountryRow` writes a cell only when its value differs — an identical
 * `innerHTML` assignment still replaces the subtree, which is the whole defect;
 * and `syncCountryList` reconciles, MOVING rows with `insertBefore` rather than
 * recreating them, so identity, listeners and focus survive a reorder.
 *
 * ── THE SKELETON CARRIES EVERY SLOT, EVEN THE EMPTY ONES ────────────────────
 *
 * `countryRowHTML` below omitted the spark wrapper and the org rows when they
 * had nothing in them. A synced row cannot: the cell has to exist to be written
 * to. So they are always present and HIDDEN when empty, which is also what the
 * live skeleton does, and for a reason worth keeping — the label row is a flex
 * container with a gap, so an empty child still takes one gap of width, and
 * `.svc-sub-rows` carries a margin-top that would leave dead space.
 *
 * `countryRowHTML` is kept: `connflow-card-check` and the dashboard card still
 * build a static list from it, where nothing is synced and a string is right.
 */
export function countryRowEl(cc: string): HTMLElement {
  const row = document.createElement('div');
  row.className = 'conn-map-row';
  row.dataset.cc = cc;
  row.innerHTML =
    '<span class="conn-map-flag"></span>' +
    '<div style="flex:1;min-width:0">' +
      '<div style="display:flex;align-items:center;justify-content:space-between;gap:.4rem">' +
        '<div class="conn-map-label" style="min-width:0"></div>' +
        '<div class="conn-map-spark" style="flex-shrink:0"></div>' +
      '</div>' +
      '<div class="svc-sub-rows"></div>' +
      '<div class="conn-proto-bar">' +
        '<div class="conn-proto-tcp"></div>' +
        '<div class="conn-proto-udp"></div>' +
        '<div class="conn-proto-other"></div>' +
      '</div>' +
    '</div>' +
    '<span class="conn-map-count"></span>';
  return row;
}

/** Write only what changed. Every write is guarded, for the reason above. */
export function syncCountryRow(
  row: HTMLElement, e: ConnCountry, spark: string, selected: boolean,
): void {
  const q = (sel: string): HTMLElement | null => row.querySelector(sel);

  const flag = iso2Flag(e.cc);
  const fl = q('.conn-map-flag');
  if (fl && fl.textContent !== flag) fl.textContent = flag;

  const labelHtml = esc(CC_NAMES[e.cc] || e.cc) +
    (e.city ? ' <span class="conn-map-label-sub">' + esc(e.city) + '</span>' : '');
  const lab = q('.conn-map-label');
  if (lab && lab.innerHTML !== labelHtml) lab.innerHTML = labelHtml;

  // The spark genuinely changes every tick — the one write expected to happen
  // each time, and a leaf, so it costs one subtree.
  const sp = q('.conn-map-spark');
  if (sp) {
    if (sp.innerHTML !== spark) sp.innerHTML = spark;
    const disp = spark ? '' : 'none';
    if (sp.style.display !== disp) sp.style.display = disp;
  }

  const orgsHtml = (e.orgs && e.orgs.length)
    ? e.orgs.map((o) => '<span class="svc-sub-row">' + svcBadge(o.org, o.cat) +
      '<span class="svc-sub-count">' + o.count + '</span></span>').join('')
    : '';
  const og = q('.svc-sub-rows');
  if (og) {
    if (og.innerHTML !== orgsHtml) og.innerHTML = orgsHtml;
    const disp = orgsHtml ? '' : 'none';
    if (og.style.display !== disp) og.style.display = disp;
  }

  const total = (e.proto.tcp || 0) + (e.proto.udp || 0) + (e.proto.other || 0) || 1;
  const tcpPct = Math.round((e.proto.tcp || 0) / total * 100);
  const udpPct = Math.round((e.proto.udp || 0) / total * 100);
  const bars: [string, number][] = [
    ['.conn-proto-tcp', tcpPct], ['.conn-proto-udp', udpPct],
    ['.conn-proto-other', 100 - tcpPct - udpPct],
  ];
  for (const [sel, pct] of bars) {
    const bar = q(sel);
    const v = String(pct);
    if (bar && bar.style.flex !== v) bar.style.flex = v;
  }

  const cnt = String(e.count);
  const ce = q('.conn-map-count');
  if (ce && ce.textContent !== cnt) ce.textContent = cnt;

  row.classList.toggle('selected', selected);
}

/**
 * Reconcile the list against `rows`, the caller's cache of `cc -> element`.
 *
 * Exported as one function rather than left inline in `connections.ts`, which is
 * where the live app keeps it: the reconcile is the part with the rules, and a
 * gate can only drive what a module exposes.
 */
export function syncCountryList(
  list: HTMLElement,
  topCountries: ConnCountry[],
  sparks: Record<string, number[]>,
  selectedCC: string | null,
  rows: Record<string, HTMLElement>,
): void {
  if (!topCountries || !topCountries.length) {
    for (const k of Object.keys(rows)) delete rows[k];
    // "No geo data yet" rather than "no connections": on a router without the
    // MaxMind database this list is empty while the rest of the page is full.
    list.innerHTML = '<div class="empty-state">No geo data yet</div>';
    return;
  }

  // RE-SEED WHEN THE DOM NO LONGER MATCHES THE CACHE. Two paths empty this list
  // without coming through here — the empty state above, and the router-switch
  // reset that assigns `innerHTML = ''`. Without this the cache would still hold
  // detached rows and the next tick would silently re-attach the previous
  // router's.
  if (!list.querySelector('.conn-map-row')) {
    for (const k of Object.keys(rows)) delete rows[k];
  }

  const seen: Record<string, true> = {};
  let prev: HTMLElement | null = null;
  for (const e of topCountries) {
    seen[e.cc] = true;
    let row = rows[e.cc];
    if (!row) { row = countryRowEl(e.cc); rows[e.cc] = row; }
    syncCountryRow(row, e, drawSparkSVG(sparks[e.cc]) || '', e.cc === selectedCC);
    // insertBefore MOVES an existing node rather than cloning it, so identity,
    // listeners and focus survive a reorder. Skipped when already in place,
    // because moving a node the pointer is over still disturbs it.
    // Typed explicitly: `prev` is assigned from `row` at the end of this loop,
    // so inferring `want` from it is circular and the compiler says so.
    const want: ChildNode | null = prev ? prev.nextSibling : list.firstChild;
    if (row !== want) list.insertBefore(row, want);
    prev = row;
  }
  for (const cc of Object.keys(rows)) {
    if (seen[cc]) continue;
    const el = rows[cc];
    if (el && el.parentNode) el.parentNode.removeChild(el);
    delete rows[cc];
  }
}

export function countryListHTML(
  topCountries: ConnCountry[],
  sparks: Record<string, number[]>,
  selectedCC: string | null,
): string {
  if (!topCountries || !topCountries.length) {
    // "No geo data yet" rather than "no connections": on a router without the
    // MaxMind database this list is empty while the rest of the page is full,
    // and saying which is which saves someone hunting for a fault.
    return '<div class="empty-state">No geo data yet</div>';
  }
  return topCountries.map((e) =>
    countryRowHTML(e, drawSparkSVG(sparks[e.cc]), e.cc === selectedCC)).join('');
}

/**
 * The ports for one country, derived from its destination keys.
 *
 * A FALLBACK ONLY. The server sends a per-country port index that counts every
 * connection; this derives one from the capped destination list, so it can
 * undercount. It exists for a payload that predates that index — which is to
 * say, for a browser talking to an older server during a rolling upgrade.
 */
export function portsFromDests(dests: Array<{ key: string; count: number }>): ConnPort[] {
  const acc: Record<string, number> = {};
  dests.forEach((d) => {
    const m = (d.key || '').match(/:(\d+)(?:\/|$)/);
    if (m) acc[m[1]!] = (acc[m[1]!] || 0) + d.count;
  });
  return Object.keys(acc)
    .map((p) => ({ port: p, count: acc[p]! }))
    .sort((a, b) => b.count - a.count)
    .slice(0, 10);
}

export interface ConnDestEntry {
  key: string; count: number;
  country: string; city: string;
  org: string | null; cat: string | null;
}

export interface ConnSource { ip: string; name: string; mac: string; count: number }

/**
 * The countries one client is talking to, derived from ITS destination list.
 *
 * The server sends a per-source destination index; this rolls it back up into
 * the same shape the country list renders, so selecting a client re-uses the
 * whole list renderer rather than growing a second one that would drift.
 *
 * The PROTOCOL SPLIT AND CITY COME FROM THE UNFILTERED CACHE, because a
 * destination row carries neither: the split is a property of the country as a
 * whole, and showing it unchanged while the counts narrow is the live
 * behaviour — a client's own protocol mix is not something the payload knows.
 */
export function countriesFromSourceDests(
  dests: ConnDestEntry[],
  protoOf: Record<string, { tcp?: number; udp?: number; other?: number }>,
  cityOf: Record<string, string>,
): ConnCountry[] {
  const counts: Record<string, number> = {};
  const orgMaps: Record<string, Record<string, { count: number; cat: string | null }>> = {};
  const order: string[] = [];

  dests.forEach((d) => {
    if (!d.country) return;
    if (counts[d.country] === undefined) {
      counts[d.country] = 0;
      order.push(d.country);
    }
    counts[d.country]! += d.count;
    if (d.org) {
      if (!orgMaps[d.country]) orgMaps[d.country] = {};
      const m = orgMaps[d.country]!;
      if (!m[d.org]) m[d.org] = { count: 0, cat: d.cat || null };
      m[d.org]!.count += d.count;
    }
  });

  const out: ConnCountry[] = order.map((cc) => {
    const orgMap = orgMaps[cc] || {};
    const orgs = Object.keys(orgMap)
      .map((org) => ({ org, count: orgMap[org]!.count, cat: orgMap[org]!.cat }))
      .sort((a, b) => b.count - a.count)
      .slice(0, 4);
    return {
      cc, count: counts[cc]!,
      proto: protoOf[cc] || { tcp: 0, udp: 0, other: 0 },
      city: cityOf[cc] || '',
      orgs,
    };
  });
  out.sort((a, b) => b.count - a.count);
  return out;
}

/**
 * The client picker's options: ACTIVE SOURCES FIRST, then every other device
 * the DHCP server knows.
 *
 * A device with no current traffic is still worth being able to select — that
 * is how you find out it has none. Active first because a list sorted purely by
 * name buries the four devices doing something among sixty that are not.
 */
export function clientOptions(
  active: ConnSource[],
  leases: Array<{ ip: string; name?: string; hostName?: string }>,
): Array<{ ip: string; name: string }> {
  const seen = new Set<string>();
  const devices: Array<{ ip: string; name: string }> = [];
  (active || []).forEach((s) => {
    if (s.ip && !seen.has(s.ip)) {
      seen.add(s.ip);
      devices.push({ ip: s.ip, name: s.name || s.ip });
    }
  });
  (leases || []).forEach((l) => {
    const ip = l.ip || '';
    if (ip && !seen.has(ip)) {
      seen.add(ip);
      devices.push({ ip, name: l.name || l.hostName || ip });
    }
  });
  devices.sort((a, b) => a.name.localeCompare(b.name));
  return devices;
}
