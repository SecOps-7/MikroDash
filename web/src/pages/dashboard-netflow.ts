/**
 * The Network Flow card's Wired count (issue #132).
 *
 * ── WHY IT LIVES HERE ───────────────────────────────────────────────────────
 *
 * The count was written by the Interfaces page's `ifstatus:update` handler. The
 * card subscribes to the `wireless` room, which never receives that event, so
 * the number stayed empty after sign-in and appeared only once Interfaces or
 * Topology had been opened, until the next reload. It is now drawn from
 * `ifstatus:names`, which is sent to every browser on the router and on router
 * select, so it fills on first paint whatever else the Dashboard holds.
 *
 * ── WHAT IT COUNTS ──────────────────────────────────────────────────────────
 *
 * Ethernet ports that are up: running, not disabled, type `ether`. The same rule
 * the Interfaces page used, so the number means what it always meant.
 */

import { netFlowUpdate } from './dashboard-card-netflow';

/** An interface as `ifstatus:names` carries it. */
export interface NamedInterface {
  running: boolean;
  disabled: boolean;
  type: string;
}

/** How many ethernet ports are up. */
export function wiredCount(interfaces: readonly NamedInterface[] | null | undefined): number {
  return (interfaces || []).filter((i) => i.running && !i.disabled && i.type === 'ether').length;
}

/** Write the count into the Network Flow card. */
export function renderWiredCount(payload: { interfaces?: readonly NamedInterface[] } | null | undefined): void {
  netFlowUpdate({ wired: { clients: wiredCount(payload?.interfaces) } });
}
