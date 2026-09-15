// The IP Addresses page (#97): every IPv4 and IPv6 address on the router, with
// add, edit, enable, disable and remove through the resource engine.
//
// ── ONE TABLE, TWO RESOURCES ────────────────────────────────────────────────
//
// The collector sends both families as one list. Each row names the resource
// that edits it, `ipAddress` or `ipv6Address`, so an IPv6 row reaches
// /ipv6/address rather than failing at /ip/address, as Routes does for route6.
//
// ── DYNAMIC AND INVALID ROWS SAY SO ─────────────────────────────────────────
//
// A dynamic address belongs to whatever created it and opens read-only; an
// invalid one sits on an interface that is gone. Both are shown, because an
// address list that hid them would not be the router's list.

import type { Socket } from '../socket';
import { esc, el, resRow } from '../dom';
import { mountAdds, mountRows } from '../resource';
import type { IPAddressesPayload } from '../gen/payloads';

export function initIpAddressesPage(socket: Socket, isVisible: (page: string) => boolean): void {
  let last: IPAddressesPayload | null = null;

  function render(): void {
    const tbody = el('ipAddressesTable');
    if (!tbody || !last) return;
    const rows = last.addresses || [];
    const badge = el('ipAddressesBadge');
    if (badge) badge.textContent = String(rows.length);
    if (!rows.length) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty-state">No addresses</td></tr>';
      return;
    }
    tbody.innerHTML = rows.map((a) => {
      const v6 = a.family === 'ipv6';
      const state = [a.disabled ? 'Disabled' : '', a.dynamic ? 'Dynamic' : '', a.invalid ? 'Invalid' : '']
        .filter(Boolean).join(' · ');
      return '<tr' + (a.disabled || a.invalid ? ' style="opacity:.55"' : '') +
        resRow(a.id, a.address, v6 ? 'ipv6Address' : 'ipAddress') + '>' +
        '<td style="font-size:.78rem;font-weight:600">' + esc(a.address) + '</td>' +
        '<td style="font-size:.72rem">' + (v6 ? 'IPv6' : 'IPv4') + '</td>' +
        '<td style="font-size:.72rem">' + esc(a.network || '—') + '</td>' +
        '<td style="font-size:.76rem">' + esc(a.interface || '—') + '</td>' +
        '<td style="font-size:.72rem;color:var(--text-muted)">' + esc(state) + '</td>' +
        '<td style="font-size:.72rem;color:var(--text-muted)">' + esc(a.comment || '') + '</td>' +
        '</tr>';
    }).join('');
  }

  socket.on('ipaddresses:update', (d) => {
    last = d;
    if (isVisible('ip-addresses')) render();
  });
  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'ip-addresses') render();
  });
  mountAdds(socket);
  mountRows(socket);
}
