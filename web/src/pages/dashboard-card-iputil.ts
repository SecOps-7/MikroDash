// The Dashboard's IP Utilisation card (dc-card-iputil).
//
// ── THE THIRD PLACE THIS SUM IS COMPUTED, AND THE SIMPLEST ──────────────────
//
// The DHCP page's headline gauge takes `totalLeases` and falls back to the
// lease-table length before the first `lan:overview` arrives; the per-subnet
// bars use each network's own count. This card takes `totalLeases` and NOTHING
// ELSE — `|| 0` on an absent one, so a cold card reads 0% rather than borrowing
// a number from somewhere.
//
// That is a real difference between two gauges of the same quantity, and it is
// the live behaviour of both. Since Part 70 filtered `waiting` leases out of the
// count they at least agree on what "used" means, which is what issue #115 was
// about; they still disagree about what to show before the first payload.
//
// ── THE LABEL DROPS ITS NUMBERS WITH NO POOL ────────────────────────────────
//
// With no pool the label is the bare word `used` — not `0 / 0 used`, which would
// read as a fact rather than an absence.

import { el } from '../dom';
import { dcDrawGauge } from './dashboard-cards-util';

export interface IpUtilPayload {
  totalPoolSize?: number;
  totalLeases?: number;
}

export function renderIpUtilCard(data: IpUtilPayload): void {
  const totalPool = data.totalPoolSize || 0;
  const totalUsed = data.totalLeases || 0;
  const pct = totalPool > 0 ? Math.round((totalUsed / totalPool) * 100) : 0;
  dcDrawGauge(pct);
  const lbl = el('dc-dhcpGaugeLbl');
  if (lbl) lbl.textContent = totalPool > 0 ? (totalUsed + ' / ' + totalPool + ' used') : 'used';
}
