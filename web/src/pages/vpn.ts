// The VPN page — a port of the `vpn:update` handler in public/app.js.
//
// NOT AN IIFE over there either, and the same shape as DHCP: one top-level
// handler draws BOTH this page and the dashboard's VPN card, with two helpers
// above it. `tools/live-renderer.js` lifts the lot as one range.
//
// This module renders only the page. The dashboard mini-card the same live
// handler also writes is left alone — that element is not in the ported shell,
// and the Dashboard is its own queue item.

import { esc, el, resRow, fmtMbps, fmtBytes } from '../dom';
import type { Socket } from '../socket';
import { mountAdds, mountRows } from '../resource';

export interface Tunnel {
  id: string; publicKey: string; type: string; name: string; state: string;
  lastHandshake: string; keepalive: string; endpoint: string;
  allowedIp: string; interface: string;
  rx: number; tx: number; rxRate: number; txRate: number;
}

export interface PppTunnel {
  type: string; name: string; service: string; address: string;
  callerId: string; uptime: string; rx: number; tx: number;
}

export interface IpsecTunnel {
  type: string; name: string; state: string; uptime: string;
  side: string; enc: string; auth: string;
}

export interface VpnPayload {
  ts: number; tunnels: Tunnel[]; ppp: PppTunnel[]; ipsec: IpsecTunnel[]; pollMs: number;
}

/**
 * A RouterOS last-handshake duration in seconds.
 *
 * Infinity for "never" or empty. NOTE THE FINAL `|| Infinity`: a string that
 * parses to zero is treated as never rather than as brand new, which is the live
 * behaviour and matters because `0` and `never` want the same badge.
 */
function hsToSecs(s: string): number {
  if (!s || s === 'never') return Infinity;
  let total = 0;
  const re = /(\d+)([wdhms])/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s)) !== null) {
    const n = parseInt(m[1] as string, 10);
    if (m[2] === 'w') total += n * 604800;
    else if (m[2] === 'd') total += n * 86400;
    else if (m[2] === 'h') total += n * 3600;
    else if (m[2] === 'm') total += n * 60;
    else total += n;
  }
  // `total`, NOT `total || Infinity`. The live helper returns the accumulated
  // seconds and 0 is a real reading: a peer that has just completed a handshake
  // reports `0s`, and `|| Infinity` turned that into the STALEST possible value
  // — a freshly connected peer rendered red. An unparseable string matches
  // nothing and also yields 0, which live treats as recent; reproduced rather
  // than corrected, because the badge is a live behaviour and not this port's to
  // redesign. Caught by tools/vpn-page-check.js.
  return total;
}

/**
 * The colour-coded handshake badge.
 *
 * WireGuard re-keys about every three minutes while a peer is active, so the
 * thresholds grade by age: under 3 minutes is fine, under 10 is a warning,
 * older is stale. A peer that is not connected gets "Never connected" whatever
 * its handshake says — the badge follows the state, not the clock.
 */
function hsBadge(uptime: string, connected: boolean): string {
  if (!connected || !uptime || uptime === 'never') {
    return '<span class="vpn-hs-badge hs-never">Never connected</span>';
  }
  const secs = hsToSecs(uptime);
  const cls = secs < 180 ? 'hs-ok' : secs < 600 ? 'hs-warn' : 'hs-stale';
  // The live code picks a dot per class and every branch is the same glyph.
  // Written as the one glyph rather than the three-way choice: the rendered
  // output is what this page is judged on, and it is identical.
  return '<span class="vpn-hs-badge ' + cls + '">● ' + esc(uptime) + '</span>';
}

export function initVpnPage(socket: Socket, isVisible: (page: string) => boolean): void {
  socket.on('vpn:update', (d: VpnPayload) => {
    const all = d.tunnels || [];
    const wg = all.filter((t) => t.type === 'WireGuard');
    const connected = wg.filter((t) => t.state === 'active');
    const stale = wg.filter((t) => t.state === 'stale');
    const never = wg.filter((t) => t.state === 'never');

    // The badge in this page's card header. The live handler writes this same
    // element, so it belongs here rather than to the dashboard.
    const count = el('vpnPageCount');
    if (count) {
      count.textContent = String(wg.length);
      count.className = 'card-badge' + (wg.length > 0 ? ' active-blue' : '');
    }

    // ── summary tiles ────────────────────────────────────────────────────────
    const totalMbps = wg.reduce((sum, t) => sum + ((t.rxRate || 0) + (t.txRate || 0)) / 1e6 * 8, 0);
    const set = (id: string, v: string): void => { const e = el(id); if (e) e.textContent = v; };
    set('vpnStatTotal', String(wg.length));
    set('vpnStatConn', String(connected.length));
    set('vpnStatStale', String(stale.length));
    // "Never connected" is its OWN count rather than being lumped in with peers
    // that connected once and went away — those are `stale`.
    set('vpnStatIdle', String(never.length));
    set('vpnStatThroughput', totalMbps > 0 ? fmtMbps(totalMbps) : '0');

    // ── PPP and IPsec ────────────────────────────────────────────────────────
    // Both cards stay hidden unless the router actually has any, so a
    // WireGuard-only setup looks exactly as it did before they existed.
    const ppp = d.ppp || [];
    const ipsec = d.ipsec || [];

    const pppCard = el('vpnPppCard');
    const pppBody = el('vpnPppTbody');
    const pppCount = el('vpnPppCount');
    if (pppCard) pppCard.style.display = ppp.length ? '' : 'none';
    if (pppCount) pppCount.textContent = String(ppp.length);
    if (pppBody && !ppp.length) pppBody.innerHTML = '';
    if (pppBody && ppp.length) {
      pppBody.innerHTML = ppp.map((s) =>
        '<tr>' +
        '<td style="font-weight:600">' + esc(s.name || '—') + '</td>' +
        '<td><span class="vpn-proto-pill">' + esc(s.service || '—') + '</span></td>' +
        '<td style="font-family:var(--font-mono);font-size:.72rem">' + esc(s.address || '—') + '</td>' +
        '<td style="font-family:var(--font-mono);font-size:.72rem;color:var(--text-muted)">' + esc(s.callerId || '—') + '</td>' +
        '<td style="font-size:.72rem">' + esc(s.uptime || '—') + '</td>' +
        '<td style="text-align:right;font-family:var(--font-mono);font-size:.72rem">' +
          '<span style="color:var(--accent-rx)">' + esc(fmtBytes(s.rx || 0)) + '</span> / ' +
          '<span style="color:var(--accent-tx)">' + esc(fmtBytes(s.tx || 0)) + '</span></td>' +
        '</tr>').join('');
    }

    const ipCard = el('vpnIpsecCard');
    const ipBody = el('vpnIpsecTbody');
    const ipCount = el('vpnIpsecCount');
    if (ipCard) ipCard.style.display = ipsec.length ? '' : 'none';
    if (ipCount) ipCount.textContent = String(ipsec.length);
    if (ipBody && !ipsec.length) ipBody.innerHTML = '';
    if (ipBody && ipsec.length) {
      ipBody.innerHTML = ipsec.map((p) =>
        '<tr>' +
        '<td style="font-family:var(--font-mono);font-size:.74rem;font-weight:600">' + esc(p.name || '—') + '</td>' +
        '<td><span class="vpn-proto-pill">' + esc(p.state || '—') + '</span></td>' +
        '<td style="font-size:.72rem;color:var(--text-muted)">' + esc(p.side || '—') + '</td>' +
        '<td style="font-size:.72rem">' + esc(p.uptime || '—') + '</td>' +
        '<td style="font-family:var(--font-mono);font-size:.72rem">' + esc(p.enc || '—') + '</td>' +
        '<td style="font-family:var(--font-mono);font-size:.72rem">' + esc(p.auth || '—') + '</td>' +
        '</tr>').join('');
    }

    // ── the tile grid ────────────────────────────────────────────────────────
    // SORTED IN PLACE, connected first. `wg` is a filtered copy, so this does
    // not disturb the payload — and it runs AFTER the summary counts are taken,
    // which is where the original has it.
    wg.sort((a, b) => (b.state === 'active' ? 1 : 0) - (a.state === 'active' ? 1 : 0));

    const grid = el('vpnPageGrid');
    if (!grid) return;
    if (!wg.length) {
      grid.innerHTML = '<div class="empty-state">No peers configured</div>';
      return;
    }
    grid.innerHTML = wg.map((t) => {
      const isConn = t.state === 'active';
      const rxR = t.rxRate || 0;
      const txR = t.txRate || 0;
      const rxRateStr = rxR > 0
        ? '<span style="color:var(--accent-rx)">↓ ' + fmtBytes(Math.round(rxR)) + '/s</span>' : '';
      const txRateStr = txR > 0
        ? '<span style="color:var(--accent-tx)">↑ ' + fmtBytes(Math.round(txR)) + '/s</span>' : '';
      const totStr = '<span style="color:var(--text-muted)">↓ ' + fmtBytes(parseInt(String(t.rx), 10) || 0) +
        ' ↑ ' + fmtBytes(parseInt(String(t.tx), 10) || 0) + '</span>';
      const dotCls = isConn ? 'up' : 'dis';
      const tileCls = 'vpn-tile ' + (isConn ? 'up' : 'idle');
      // A tile, not a row — the delegation looks for the nearest ancestor with a
      // data-id, so the two work the same. Identity is the PUBLIC KEY, which is
      // what the write round-trips to prove the row has not moved underneath.
      return '<div class="' + tileCls + '"' + resRow(t.id, t.publicKey) + '>' +
        '<div class="vpn-tile-name"><span class="iface-dot ' + dotCls + '"></span><span class="vpn-tile-name-text">' + esc(t.name || t.interface || '—') + '</span></div>' +
        (t.interface ? '<div class="vpn-tile-iface">' + esc(t.interface) + (t.allowedIp ? ' · ' + esc(t.allowedIp) : '') + '</div>' : '') +
        (t.endpoint ? '<div class="vpn-tile-ip">' + esc(t.endpoint) + '</div>' : '') +
        '<div class="vpn-tile-hs">' + hsBadge(t.lastHandshake, isConn) + '</div>' +
        ((rxRateStr || txRateStr)
          ? '<div class="vpn-tile-traffic">' + rxRateStr + txRateStr + '</div>'
          : (isConn ? '<div class="vpn-tile-traffic">' + totStr + '</div>' : '')) +
      '</div>';
    }).join('');
  });

  mountAdds(socket);
  mountRows(socket);

  void isVisible;
}
