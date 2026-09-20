// The VPN page — an overview of every VPN technology, and a way through to each.
//
// ── WHAT THIS PAGE USED TO BE ───────────────────────────────────────────────
//
// It was a WireGuard page wearing the wrong name: five summary tiles that
// counted WireGuard peers only, a WireGuard peer grid with its own Add button,
// and PPP and IPsec as two afterthoughts that hid themselves when empty. That
// content moved to /wireguard on 2026-09-20, beside the dedicated OpenVPN and
// IPsec pages it should always have sat with.
//
// What is left is the one view none of those pages can give: every technology at
// once. The PPP and IPsec tables stay because they are the live detail behind
// two of the rows, and nothing else in the app shows a running session.
//
// ── ONE HONEST ASYMMETRY, STATED RATHER THAN PAPERED OVER ───────────────────
//
// "Configured" is filled for WireGuard and blank for the other three. The
// payload's `tunnels` is every configured PEER, while `ppp` and `ipsec` are
// `/ppp/active` and `/ip/ipsec/active-peers` — SESSIONS, not configuration. A
// zero in that column for OpenVPN would say "no servers configured" when it
// means "nobody is connected right now", so the cell is left empty instead. The
// asymmetry belongs to the payload, not to this page, and the dedicated pages
// are where a configuration count would be truthful.

import { esc, el, fmtBytes } from '../dom';
import type { Socket } from '../socket';
import type { VPNPayload } from '../gen/payloads';

/** One row of the overview. `configured` is null where the payload cannot say. */
export interface OverviewRow {
  label: string;
  page: string;
  configured: number | null;
  active: number;
}

/**
 * The four rows, derived from the one payload this page already receives.
 *
 * Exported so the gate can drive the derivation without a DOM: the counting is
 * the only thing here worth getting wrong.
 */
export function overviewRows(d: VPNPayload): OverviewRow[] {
  const tunnels = d.tunnels || [];
  const ppp = d.ppp || [];
  const ipsec = d.ipsec || [];
  const wg = tunnels.filter((t) => t.type === 'WireGuard');
  // `ParsePppSessions` upper-cases the service, so OpenVPN sessions arrive as
  // `OVPN` — the spelling RouterOS uses in /ppp/active, not the product name.
  const ovpn = ppp.filter((s) => s.service === 'OVPN');
  return [
    { label: 'WireGuard', page: 'wireguard', configured: wg.length, active: wg.filter((t) => t.state === 'active').length },
    { label: 'IPsec', page: 'ipsec', configured: null, active: ipsec.filter((p) => p.state === 'established').length },
    { label: 'OpenVPN', page: 'openvpn', configured: null, active: ovpn.length },
    { label: 'PPP / L2TP / SSTP', page: 'ppp', configured: null, active: ppp.length - ovpn.length },
  ];
}

/**
 * Whether this viewer can actually reach a page.
 *
 * ── NOT `pageVisible`, AND THE DIFFERENCE IS THE WHOLE BUG ─────────────────
 *
 * The first version of this asked `isVisible(page)`. That function answers
 * "is this page ON SCREEN right now" — it is the blur-suspend guard,
 * `currentPage === name && !document.hidden` — so on the VPN page it is false
 * for every OTHER page by definition, and not one link ever rendered. The web
 * test could not see it because it stubs `isVisible` itself, which tests the
 * intention rather than the wiring. A browser found it in a second.
 *
 * The nav item is the real answer: `web/src/caps.ts` sets its `display` from
 * the install's visible-pages setting, the viewer's role and the build. It is
 * also the handle the click below uses, so the question and the action are one
 * mechanism rather than two that can disagree.
 */
function navReachable(page: string): boolean {
  const item = document.querySelector<HTMLElement>('.nav-item[data-page="' + page + '"]');
  return !!item && item.style.display !== 'none';
}

/** A dash, not a zero: "the payload cannot say" is not "there are none". */
function count(n: number | null): string {
  return n === null ? '<span style="color:var(--text-muted)">&mdash;</span>' : String(n);
}

export function initVpnPage(socket: Socket, isVisible: (page: string) => boolean): void {
  socket.on('vpn:update', (d) => {
    // THE BLUR GUARD every other page carries: nothing is drawn while this page
    // is off screen, and `resumePage` replays the payload on the way back in.
    if (!isVisible('vpn')) return;
    const rows = overviewRows(d);
    const live = rows.filter((r) => r.active > 0).length;

    const badge = el('vpnOverviewCount');
    if (badge) {
      badge.textContent = String(live);
      badge.className = 'card-badge' + (live > 0 ? ' active-blue' : '');
    }

    const body = el('vpnOverviewTbody');
    if (body) {
      body.innerHTML = rows.map((r) => {
        const pill = r.active > 0
          ? '<span class="vpn-hs-badge hs-ok">' + r.active + ' active</span>'
          : '<span class="vpn-hs-badge hs-never">idle</span>';
        // THE LINK IS GATED. A row pointing at a page the viewer may not open
        // is an invitation to a permission error, so it renders as nothing
        // instead. See navReachable for why it is not `isVisible`.
        const link = navReachable(r.page)
          ? '<a href="#" class="vpn-overview-link" data-vpn-page="' + esc(r.page) + '">Open &rsaquo;</a>'
          : '';
        return '<tr>' +
          '<td style="font-weight:600">' + esc(r.label) + '</td>' +
          '<td>' + count(r.configured) + '</td>' +
          '<td>' + r.active + '</td>' +
          '<td>' + pill + '</td>' +
          '<td style="text-align:right">' + link + '</td>' +
          '</tr>';
      }).join('');
    }

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
  });

  // ── GOING THROUGH TO A PAGE ─────────────────────────────────────────────
  //
  // By clicking the nav item rather than by routing here: the nav entry for a
  // GENERATED page is injected at runtime by area.ts's mountAreaNav, so it is
  // the only handle that exists for every one of these four. The same idiom the
  // Security Scan page and the Security Score card use.
  document.addEventListener('click', (e) => {
    const t = (e as unknown as { target: HTMLElement | null }).target;
    const a = t && t.closest ? t.closest('[data-vpn-page]') as HTMLElement | null : null;
    if (!a) return;
    (e as unknown as { preventDefault: () => void }).preventDefault();
    const page = a.getAttribute('data-vpn-page');
    if (!page) return;
    (document.querySelector('.nav-item[data-page="' + page + '"]') as HTMLElement | null)?.click();
  });
}
