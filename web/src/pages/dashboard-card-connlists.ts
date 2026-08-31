// Two of the Dashboard's connection cards: Top Countries (dc-card-topcc) and
// Top Ports (dc-card-topports), both fed by `conn:update`.
//
// The same handler also drives the Connections Map, whose 147 lines of SVG arc
// machinery are a separate slice, and the Connection Flow sankey, which the main
// sankey IIFE renders. Only the two LISTS are here.
//
// ── THE THIRD PROTOCOL BAR IS A REMAINDER, NOT A PERCENTAGE ─────────────────
//
// tcp and udp are rounded independently and `other` is `100 - tcp - udp`. So the
// three always sum to exactly 100 even when the rounding does not — and `other`
// can come out NEGATIVE when both round up, which flexbox treats as zero. That
// is the live behaviour; computing the third the same way as the first two would
// leave a one-pixel gap on some payloads and is not what the card does.
//
// ── THE COUNTRY LABEL HAS THREE FALLBACKS ──────────────────────────────────
//
// The condensed table, then the payload's own `country`, then the bare code. So
// a country the table has never heard of still renders as something, which is
// why the table can be a subset without being a bug.
//
// ── AND THE PORT BAR HAS A FLOOR ────────────────────────────────────────────
//
// `Math.max(4, pct)` PIXELS — not percent. A port with one connection against a
// busy one still shows a visible stub rather than nothing.

import { el } from '../dom';
import { dcEsc, dcFlag } from './dashboard-cards-util';
import { DC_CC_NAMES, DC_PORT_NAMES } from '../gen/dccards-tables';

export interface CountryEntry {
  cc?: string;
  country?: string;
  count?: number;
  // REQUIRED, and that is the wire contract rather than optimism: `Proto` is a
  // VALUE type on the Go side (`ConnCountryProto`, not a pointer), so it is
  // always marshalled. The live card reads `e.proto.tcp` with no guard and would
  // throw on an entry without it — killing the whole handler, including the Top
  // Ports list below. Typing it required keeps this side reading like the
  // original instead of quietly surviving a payload the original cannot.
  proto: { tcp?: number; udp?: number; other?: number };
}
export interface PortEntry {
  port?: number | string;
  count?: number;
}
export interface ConnCardsPayload {
  topCountries?: CountryEntry[];
  topPorts?: PortEntry[];
}

export function renderTopCountries(countries: CountryEntry[]): void {
  const containerEl = el('dc-connTopMapList');
  if (!containerEl) return;
  if (!countries.length) {
    containerEl.innerHTML = '<div class="empty-state">No geo data</div>';
    return;
  }
  containerEl.innerHTML = countries.slice(0, 12).map((e) => {
    const flag = dcFlag(e.cc);
    // `|| 1` guards the division, so an entry whose protocol counts are all zero
    // renders three empty bars rather than NaN.
    const total = (e.proto.tcp || 0) + (e.proto.udp || 0) + (e.proto.other || 0) || 1;
    const tcpPct = Math.round((e.proto.tcp || 0) / total * 100);
    const udpPct = Math.round((e.proto.udp || 0) / total * 100);
    const othPct = 100 - tcpPct - udpPct;
    return '<div class="conn-map-row">' +
      '<span class="conn-map-flag">' + flag + '</span>' +
      '<div style="flex:1;min-width:0">' +
        '<div class="conn-map-label">' + dcEsc(DC_CC_NAMES[e.cc as string] || e.country || e.cc) + '</div>' +
        '<div class="conn-proto-bar">' +
          '<div class="conn-proto-tcp" style="flex:' + tcpPct + '"></div>' +
          '<div class="conn-proto-udp" style="flex:' + udpPct + '"></div>' +
          '<div class="conn-proto-other" style="flex:' + othPct + '"></div>' +
        '</div>' +
      '</div>' +
      '<span class="conn-map-count">' + e.count + '</span>' +
    '</div>';
  }).join('');
}

export function renderTopPorts(ports: PortEntry[]): void {
  const portsEl = el('dc-connPortList');
  if (!portsEl) return;
  if (!ports.length) {
    portsEl.innerHTML = '<div class="empty-state">—</div>';
    return;
  }
  // The FIRST entry's count is the scale, so the payload's own order decides it.
  // The card does not sort — it trusts the collector to send them ranked.
  const maxP = ports[0]!.count || 1;
  portsEl.innerHTML = ports.slice(0, 12).map((p) => {
    const pct = Math.round(((p.count as number) / maxP) * 100);
    const name = DC_PORT_NAMES[String(p.port)] || '';
    return '<div class="conn-port-row">' +
      '<span class="conn-port-num">' + dcEsc(p.port) + '</span>' +
      '<span class="conn-port-name">' + dcEsc(name) + '</span>' +
      '<div class="conn-port-bar" style="width:' + Math.max(4, pct) + 'px"></div>' +
      '<span class="conn-port-count">' + p.count + '</span>' +
    '</div>';
  }).join('');
}

export function renderConnListCards(data: ConnCardsPayload): void {
  renderTopCountries(data.topCountries || []);
  renderTopPorts(data.topPorts || []);
}
