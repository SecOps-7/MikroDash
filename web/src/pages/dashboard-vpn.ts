// The Dashboard's VPN card: the busiest WireGuard peers, most recent first.
//
// ── ONLY WIREGUARD, AND ONLY ACTIVE ─────────────────────────────────────────
//
// The payload carries every tunnel type; this card is about peers that are up
// right now. The VPN PAGE shows the rest, including the stale and never-connected
// counts, which is why those are computed there and not here.
//
// ── "never" AND "0s" BOTH SORT LAST, AND THAT IS THE ORIGINAL ───────────────
//
// `parseDurationSec` returns Infinity for a missing or `never` handshake so an
// unconnected peer sorts to the bottom. It also ends `return m || Infinity`,
// which sends a parsed ZERO to the bottom as well — a handshake of `0s` is the
// most recent one possible and lands where the oldest go.
//
// Reproduced rather than corrected: it is reachable, since a peer that handshook
// this instant reports `0s`, and a port that sorted it first would put a
// different peer at the top of the card than the live app does.

import { esc, el } from '../dom';
import { getVpnDashTopN } from '../caps';

export interface VpnTunnel {
  type?: string;
  state?: string;
  name?: string;
  interface?: string;
  endpoint?: string;
  lastHandshake?: string;
}

export interface VpnPayload {
  tunnels?: VpnTunnel[];
}

/**
 * A RouterOS duration — `2w3d4h5m6s` — as seconds.
 *
 * Units may appear in any order and any may be missing, because the regex simply
 * accumulates every `<number><unit>` pair it finds. A bare number with no unit
 * contributes nothing, since the pattern requires one.
 */
export function parseDurationSec(s: string | undefined | null): number {
  // The `=== 'never'` half is REDUNDANT and kept because the original has it:
  // "never" contains no digit-unit pair, so the loop below adds nothing and the
  // `total || Infinity` at the end returns the same answer. Removing it is a
  // mutation no case can catch, which was measured rather than assumed — the two
  // differ only for a string that is exactly "never", and there they agree.
  if (!s || s === 'never') return Infinity;
  let total = 0;
  const re = /(\d+)([wdhms])/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s)) !== null) {
    const n = parseInt(m[1]!, 10);
    if (m[2] === 'w') total += n * 604800;
    else if (m[2] === 'd') total += n * 86400;
    else if (m[2] === 'h') total += n * 3600;
    else if (m[2] === 'm') total += n * 60;
    else total += n;
  }
  // See the header: a parsed zero becomes Infinity, which is the original's.
  //
  // AND IT IS CORRECT HERE WHILE BEING WRONG IN `vpn.ts`. The live app carries
  // two near-identical parsers with deliberately different endings:
  // `parseDurationSec` (app.js:192) ends `m || Infinity` because it SORTS, so an
  // unconnected peer belongs at the bottom; `vpnHsToSecs` (app.js:2032) ends with
  // the raw total because it COLOURS a badge, where 0 seconds means a peer has
  // just handshaken. The port had copied this ending into that one, which
  // rendered a freshly connected peer red. Do not share them.
  return total || Infinity;
}

export function renderVpnCard(data: VpnPayload): void {
  const table = el('vpnTable');
  if (!table) return;
  const wgPeers = (data.tunnels || []).filter((t) => t.type === 'WireGuard');
  const connected = wgPeers.filter((t) => t.state === 'active');
  connected.sort((a, b) => parseDurationSec(a.lastHandshake) - parseDurationSec(b.lastHandshake));
  if (!connected.length) {
    table.innerHTML = '<tr><td colspan="3" class="empty-state">No active peers</td></tr>';
    return;
  }
  table.innerHTML = connected.slice(0, getVpnDashTopN()).map((t) => {
    const endStr = t.endpoint
      ? '<div style="font-size:.65rem;color:var(--text-muted);margin-top:.1rem">' + esc(t.endpoint) + '</div>'
      : '';
    return '<tr>' +
      '<td><span class="wg-up">Up</span></td>' +
      '<td><div style="font-size:.78rem;font-weight:600">' + esc(t.name || t.interface || '—') + '</div>' + endStr + '</td>' +
      '<td style="font-size:.7rem;color:var(--text-muted)">' + esc(t.lastHandshake || '—') + '</td>' +
      '</tr>';
  }).join('');
}
