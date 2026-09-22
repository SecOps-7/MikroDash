// The NetWatch page (#97): every host the router probes, with add, edit, enable
// and disable through the resource engine.
//
// The Dashboard card draws the same payload in three columns. This page is the
// one that edits, so it also shows what an edit changes: the probe type, the
// interval, the comment, and whether the host is disabled.
//
// ── A DISABLED HOST SAYS SO ─────────────────────────────────────────────────
//
// RouterOS does not probe it, so whatever status it last reported is not a
// statement about the host. The status column says Disabled instead, for the
// same reason alerting treats the host as not yet probed.

import type { Socket } from '../socket';
import { t } from '../i18n';
import { esc, el, resRow } from '../dom';
import { mountAdds, mountRows } from '../resource';
import type { NetwatchPayload } from '../gen/payloads';

export function initNetwatchPage(socket: Socket, isVisible: (page: string) => boolean): void {
  let last: NetwatchPayload | null = null;

  function render(): void {
    const tbody = el('netwatchPageTable');
    if (!tbody || !last) return;
    const hosts = last.hosts || [];
    const badge = el('netwatchPageBadge');
    if (badge) badge.textContent = String(hosts.length);
    if (!hosts.length) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty-state">' + t('No hosts configured') + '</td></tr>';
      return;
    }
    tbody.innerHTML = hosts.map((h) => {
      const status = h.disabled
        ? '<span style="color:var(--text-muted);font-size:.7rem">' + t('Disabled') + '</span>'
        : h.status === 'up'
          ? '<span class="wg-up">' + t('Up') + '</span>'
          : h.status === 'down'
            ? '<span class="wg-down">' + t('Down') + '</span>'
            : '<span style="color:var(--text-muted);font-size:.7rem">' + esc(h.status || '?') + '</span>';
      return '<tr' + (h.disabled ? ' style="opacity:.55"' : '') + resRow(h.id, h.host) + '>' +
        '<td>' + status + '</td>' +
        '<td style="font-size:.78rem;font-weight:600">' + esc(h.name || '—') + '</td>' +
        '<td style="font-size:.76rem">' + esc(h.host || '—') + '</td>' +
        '<td style="font-size:.72rem">' + esc(h.type) + '</td>' +
        '<td style="font-size:.72rem">' + esc(h.interval || '—') + '</td>' +
        '<td style="font-size:.72rem;color:var(--text-muted)">' + esc(h.comment || '') + '</td>' +
        '</tr>';
    }).join('');
  }

  socket.on('netwatch:update', (d) => {
    last = d;
    if (isVisible('netwatch')) render();
  });
  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'netwatch') render();
  });
  mountAdds(socket);
  mountRows(socket);
}
