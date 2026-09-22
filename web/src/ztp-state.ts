// Zero-touch provisioning's last `ztp:state`, for every part of the page that
// shows it: the Devices page's Provisioning section, Settings → Provisioning,
// the Dashboard's notice and the ZTP badge in Settings → Devices.
//
// ITS OWN MODULE so those readers need not import each other. The badge lives
// in `settings-routers.ts`, which `pages/ztp.ts` would otherwise have to import
// to repaint, and which would have to import `pages/ztp.ts` back to read.

import type { ZTPPayload } from './gen/payloads';

let current: ZTPPayload | null = null;
const listeners: ((p: ZTPPayload) => void)[] = [];

/** The last payload, or null before one has arrived (or for a non-admin, who
 *  never gets one: the server joins only global administrators to the room). */
export function ztpState(): ZTPPayload | null {
  return current;
}

export function setZtpState(p: ZTPPayload): void {
  current = p;
  for (const fn of listeners) fn(p);
}

export function onZtpState(fn: (p: ZTPPayload) => void): void {
  listeners.push(fn);
}

/** The router a provisioned device became, by router id. */
export function ztpDeviceForRouter(routerId: string): ZTPPayload['devices'][number] | undefined {
  return current?.devices.find((d) => d.routerId === routerId);
}
