// The Dashboard's Bandwidth card (dc-card-bw): live WAN rates and how much of
// the router's configured capacity they use.
//
// ── IT IGNORES `ifName`, DELIBERATELY ───────────────────────────────────────
//
// The traffic chart above it only accepts samples for the interface it is
// showing; this card accepts every one. `traffic.js` emits per-socket for the
// DEFAULT interface, so what arrives is already the WAN — filtering again here
// would drop the card's only input on a router whose default interface is not
// the one the chart is displaying.
//
// ── CAPACITY IS REMEMBERED, NOT RESET ───────────────────────────────────────
//
// `syncCapacity` updates only when the active router is FOUND in the list. A
// switch to a router that is not there yet — the id arrives before the list on a
// cold connect — keeps the PREVIOUS router's capacity rather than falling back
// to the 1000/1000 default. The bars are briefly scaled against the wrong
// router, and then correct themselves when the list lands. Reproduced: the
// alternative reads as "capacity unknown" and would make every bar jump on every
// switch.
//
// ── A RATE BELOW 1% IS `<1%`, AND ZERO IS AN EM DASH ────────────────────────
//
// Three states, not two: idle says nothing at all, a trickle says `<1%` rather
// than rounding to `0%` and looking idle, and everything else rounds.

import { el } from '../dom';
import { dcSplitRate } from './dashboard-cards-util';

export interface BwRouter {
  id?: string;
  bwDownMbps?: number;
  bwUpMbps?: number;
}
export interface TrafficSample {
  rx_mbps?: number;
  tx_mbps?: number;
}

// Mbps. The default stands until a router in the list says otherwise.
let bwDown = 1000, bwUp = 1000;
let routers: BwRouter[] = [];
let activeId = '';

function syncCapacity(): void {
  const r = routers.find((x) => x.id === activeId);
  // ONLY on a hit — see the header.
  if (r) {
    bwDown = r.bwDownMbps || 1000;
    bwUp = r.bwUpMbps || 1000;
  }
}

export function setBwRouters(list: BwRouter[] | undefined): void {
  routers = list || [];
  syncCapacity();
}

export function setBwActiveRouter(id: string | undefined): void {
  activeId = id || '';
  syncCapacity();
}

/** Three states: idle, a trickle, and a real figure. See the header. */
function fmtPct(pct: number, mbps: number): string {
  return mbps > 0 ? (pct < 1 ? '<1%' : Math.round(pct) + '%') : '—';
}

export function renderBandwidthCard(sample: TrafficSample): void {
  const rxMbps = sample.rx_mbps || 0;
  const txMbps = sample.tx_mbps || 0;

  const rx = dcSplitRate(rxMbps), tx = dcSplitRate(txMbps);
  const rxNum = el('dc-bwLiveRxNum'), rxUnit = el('dc-bwLiveRxUnit');
  const txNum = el('dc-bwLiveTxNum'), txUnit = el('dc-bwLiveTxUnit');
  if (rxNum) rxNum.textContent = rx.num;
  if (rxUnit) rxUnit.textContent = rx.unit;
  if (txNum) txNum.textContent = tx.num;
  if (txUnit) txUnit.textContent = tx.unit;

  // Clamped at 100 but NOT floored: a negative rate would give a negative
  // height, which the original also allows. Rates come from a counter delta and
  // are non-negative in practice.
  const rxPct = Math.min(100, bwDown > 0 ? (rxMbps / bwDown) * 100 : 0);
  const txPct = Math.min(100, bwUp > 0 ? (txMbps / bwUp) * 100 : 0);

  const barRx = el('dc-bwBarRx'), barTx = el('dc-bwBarTx');
  // ONE decimal, as a string: the CSS transition interpolates between these, so
  // more precision would be movement nobody can see and less would step.
  if (barRx) barRx.style.height = rxPct.toFixed(1) + '%';
  if (barTx) barTx.style.height = txPct.toFixed(1) + '%';

  const pctRxEl = el('dc-bwPctRx'), pctTxEl = el('dc-bwPctTx');
  if (pctRxEl) pctRxEl.textContent = fmtPct(rxPct, rxMbps);
  if (pctTxEl) pctTxEl.textContent = fmtPct(txPct, txMbps);
}

/** Forget the fleet. A switch re-syncs from the next `routers:update`. */
export function resetBandwidthCard(): void {
  bwDown = 1000;
  bwUp = 1000;
  routers = [];
  activeId = '';
}
