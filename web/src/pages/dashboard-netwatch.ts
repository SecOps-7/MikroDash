// The Dashboard's Netwatch card.
//
// ── THREE STATES, NOT TWO ───────────────────────────────────────────────────
//
// RouterOS answers `up` or `down`, and a host it has not probed yet answers
// neither. The card shows that third case as the RAW value rather than folding
// it into "Down": a host whose status is `unknown` is not a host that failed,
// and saying so would invent an outage on every page load until the first probe
// lands.
//
// The raw value is escaped because it comes off the router. Nothing else on this
// card does anything clever — it is a status, a name and an address.

import { esc, el } from '../dom';

export interface NetwatchHost {
  status?: string;
  name?: string;
  host?: string;
}

export interface NetwatchPayload {
  hosts?: NetwatchHost[];
}

export function renderNetwatch(data: NetwatchPayload): void {
  const tbody = el('netwatchTable');
  if (!tbody) return;
  const hosts = data.hosts || [];
  if (!hosts.length) {
    tbody.innerHTML = '<tr><td colspan="3" class="empty-state">No hosts configured</td></tr>';
    return;
  }
  tbody.innerHTML = hosts.map((h) => {
    const isUp = h.status === 'up';
    const isDown = h.status === 'down';
    const statusHtml = isUp
      ? '<span class="wg-up">Up</span>'
      : isDown
        ? '<span class="wg-down">Down</span>'
        : '<span style="color:var(--text-muted);font-size:.7rem">' + esc(h.status || '?') + '</span>';
    return '<tr>' +
      '<td>' + statusHtml + '</td>' +
      '<td style="font-size:.78rem;font-weight:600">' + esc(h.name || '—') + '</td>' +
      '<td style="font-size:.72rem;color:var(--text-muted)">' + esc(h.host || '—') + '</td>' +
      '</tr>';
  }).join('');
}
