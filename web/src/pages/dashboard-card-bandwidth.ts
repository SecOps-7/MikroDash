// The Dashboard's Bandwidth card (dc-card-bw): live WAN rates and how much of
// the router's configured capacity they use.
//
// ── IT IGNORES `ifName`, DELIBERATELY ───────────────────────────────────────
//
// The traffic chart above it only accepts samples for the interface it is
// showing; this card accepts every one. `traffic.js` emits per-socket for the
// DEFAULT interface, so what arrives is already the WAN - filtering again here
// would drop the card's only input on a router whose default interface is not
// the one the chart is displaying.
//
// ── CAPACITY IS REMEMBERED, NOT RESET ───────────────────────────────────────
//
// `syncCapacity` updates only when the active router is FOUND in the list. A
// switch to a router that is not there yet - the id arrives before the list on a
// cold connect - keeps the PREVIOUS router's capacity rather than falling back
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
import type { TrafficSample } from '../gen/payloads';
import type { RouterRecord } from '../events-hand';

// Mbps. The default stands until a router in the list says otherwise.
let bwDown = 1000, bwUp = 1000;
let routers: RouterRecord[] = [];
let activeId = '';

function syncCapacity(): void {
  const r = routers.find((x) => x.id === activeId);
  // ONLY on a hit - see the header.
  if (r) {
    bwDown = r.bwDownMbps || 1000;
    bwUp = r.bwUpMbps || 1000;
  }
}

export function setBwRouters(list: RouterRecord[] | undefined): void {
  routers = list || [];
  syncCapacity();
}

export function setBwActiveRouter(id: string | undefined): void {
  activeId = id || '';
  syncCapacity();
}

/** Three states: idle, a trickle, and a real figure. See the header. */
function fmtPct(pct: number, mbps: number): string {
  return mbps > 0 ? (pct < 1 ? '<1%' : Math.round(pct) + '%') : '-';
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
/**
 * The router's CONFIGURED capacity, in Mbps.
 *
 * Exported for the Network Flow card, which scales its animation against what
 * this link can actually carry rather than against an absolute curve: 40 Mbps
 * is most of a 50 Mbps line and a rounding error on a 1 Gbps one, and the
 * operator asked for the one that means something to them.
 *
 * A FUNCTION, not the numbers: `syncCapacity` rewrites them when the router
 * list or the active router changes, and a caller that captured them at mount
 * would scale every router against the first one's line.
 */
export function bwCapacityMbps(): { down: number; up: number } {
  return { down: bwDown, up: bwUp };
}

/**
 * What a ROUTER SWITCH forgets.
 *
 * ── THE FLEET AND THE SELECTION ARE NOT PER-ROUTER STATE ───────────────────
 *
 * This used to clear `routers` and `activeId` as well, and that is what made
 * the configured capacity never apply. The fleet list is the SAME for every
 * router - it is the list of all of them - so throwing it away on a switch left
 * the card unable to resolve ANY router's capacity. It came back only on
 * `routers:update`, which the server broadcasts on an add, an edit, a delete
 * and a reorder, and never on connect. Since a switch happens at boot, the card
 * spent every session measuring against the 1000 Mbps default: an operator with
 * 50 Mbps upload saw their traffic scaled against a gigabit, and it appeared to
 * fix itself after any device edit.
 *
 * So only the DERIVED figures reset here, and they are immediately re-derived
 * for whichever router is now selected. A router with nothing recorded still
 * falls back to 1000, which is what the reset was for.
 */
export function resetBandwidthCard(): void {
  bwDown = 1000;
  bwUp = 1000;
  syncCapacity();
}
