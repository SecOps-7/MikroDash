// The Dashboard's Top Talkers card.
//
// ── AN EMPTY PAYLOAD IS NEWS, NOT SILENCE ───────────────────────────────────
//
// The live app records the bug this rule exists for, and it is worth carrying
// the reasoning rather than just the code: treating an empty list as "nothing
// changed" left the previous rows up indefinitely, and INVISIBLY — the stale
// timer had just been re-armed by that very payload, so the card looked healthy
// while showing devices the router had stopped reporting, and kept showing the
// previous router's devices after a switch.
//
// ── "NO DEVICES" AND "NO KID CONTROL" ARE DIFFERENT CLAIMS ──────────────────
//
// `available: false` means the router has no kid-control menu at all, which the
// card-level dormant marking already says. The table says so too rather than
// claiming the narrower "there are no devices", which would be a guess about a
// router that was never asked.

import { esc, el, fmtMbps } from '../dom';

export interface TalkerDevice {
  name?: string;
  mac?: string;
  rx_mbps?: number;
  tx_mbps?: number;
}

export interface TalkersPayload {
  devices?: TalkerDevice[];
  available?: boolean;
}

export function renderTalkers(data: TalkersPayload): void {
  const table = el('talkersTable');
  if (!table) return;
  const devices = data.devices || [];
  if (!devices.length) {
    table.innerHTML = '<tr><td colspan="4" class="empty-state">' +
      (data.available === false ? 'Kid Control is not available on this router' : 'No devices') +
      '</td></tr>';
    return;
  }
  table.innerHTML = devices.map((d) =>
    '<tr><td>' + esc(d.name || '—') + '</td><td style="color:var(--text-muted)">' +
    esc(d.mac || '—') + '</td>' +
    '<td class="text-end" style="color:var(--accent-rx)">' + fmtMbps(d.rx_mbps) + '</td>' +
    '<td class="text-end" style="color:var(--accent-tx)">' + fmtMbps(d.tx_mbps) + '</td></tr>').join('');
}
