/**
 * The SwOS switches MikroDash knows (#142), for the pages that are not the
 * Settings table but need to recognise one: the topology panel links a
 * neighbour that is a stored switch to it, and offers to add one that is not.
 *
 * Fed by `main.ts` from `GET /api/routers`'s `switches`, the same list the
 * Settings table renders, so there is one source.
 */

export interface KnownSwitch {
  id: string;
  label?: string;
  host?: string;
  mac?: string;
}

let known: KnownSwitch[] = [];

export function setKnownSwitches(rows: KnownSwitch[]): void {
  known = rows;
}

/** The stored switch a neighbour is, by its MAC or its address; null if none. */
export function switchForNeighbor(mac?: string | null, ip?: string | null): KnownSwitch | null {
  const m = (mac || '').toUpperCase();
  const a = (ip || '').trim();
  return known.find((s) => (m && (s.mac || '').toUpperCase() === m) || (a && s.host === a)) || null;
}

/**
 * Whether a discovery neighbour looks like a SwOS switch.
 *
 * A HEURISTIC, AND LABELLED ONE: no SwOS neighbour has been captured here. A
 * MikroTik device whose version is 1.x or 2.x is SwOS, because RouterOS
 * versions are 6.x and 7.x. Being wrong costs nothing: the form it opens tests
 * the device first, and refuses anything that does not answer as SwOS.
 */
export function looksLikeSwOS(platform?: string | null, version?: string | null): boolean {
  return /mikrotik/i.test(platform || '') && /^[12]\.\d+/.test((version || '').trim());
}
