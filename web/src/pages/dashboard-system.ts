// The Dashboard's System card: uptime, the three gauges, the meta line, the
// temperature and the RouterOS update row.
//
// ── THE CARD IS COALESCED, NOT THROTTLED ────────────────────────────────────
//
// `system:update` arrives about once a second. Every payload is STORED and a
// single animation frame is booked to render whichever one is latest — so a
// burst costs one layout rather than one per payload, and no payload is ever
// the reason a later one is dropped.
//
// A hidden tab renders NOTHING and keeps the data pending. That is the half
// that is easy to get wrong: skipping the render is obvious, but discarding the
// payload with it would leave the card showing stale numbers until the next
// tick after the tab came back. `flushPendingSystem` is what the visibility
// handler calls to settle it.
//
// ── THE META LINE IS WRITTEN ONCE ───────────────────────────────────────────
//
// Board name, RouterOS version, CPU count and RAM do not change while a router
// is connected, so they are written on the first payload that carries any of
// them and never again. `resetSysMeta` re-arms it, and both callers matter: a
// reconnect, and a switch to another router — whose board is a different board.
//
// ── AND THE ORDER OF THE LAST TWO IS LOAD-BEARING ───────────────────────────
//
// `sysMetaTemp` is a CHILD of `sysMeta`, created lazily on the first
// temperature and updated in place after. Rewriting the meta line therefore
// destroys it, and the lookup happens AFTER that rewrite so the next
// temperature recreates it. Hoisting the lookup would leave a detached node
// being updated forever, and the temperature would disappear on router switch
// and never come back.

import { esc, el, fmtBytes, parseUptime } from '../dom';
import { gauge } from './dashboard-gauge';

/**
 * What the System card publishes on `mikrodash:updateavailable`.
 *
 * ── DECLARED ONCE, ON PURPOSE ───────────────────────────────────────────────
 *
 * The Upgrade dialog reads this out of `CustomEvent.detail`, which is `any`, and
 * casts it. So the compiler cannot check the seam: the two modules could drift
 * to different shapes and nothing would say so until the dialog showed a dash
 * where a version belongs. They had two separate declarations that happened to
 * agree — the same setup that let `GeoPlace` and `City` disagree about an
 * optional field earlier in this port, where the compiler DID catch it because
 * the value did not pass through an `any`.
 *
 * Exporting it from the producer and importing it in the consumer at least makes
 * both sides name one type; the update-seam check covers what the
 * compiler still cannot.
 */
export interface UpdInfo { installed: string; latest: string; channel: string }

export interface SystemPayload {
  uptimeRaw?: unknown;
  cpuLoad?: number;
  memPct?: number;
  hddPct?: number;
  totalHdd?: number;
  totalMem?: number;
  boardName?: string;
  version?: string;
  cpuCount?: number;
  cpuFreq?: number;
  tempC?: number | null;
  updateAvailable?: boolean;
  latestVersion?: string;
  updateStatus?: string;
  updateChannel?: string;
}

let metaWritten = false;
let pending: SystemPayload | null = null;
let rafId: number | null = null;
// The update row's last markup, as its own fingerprint. Reset on reconnect and
// on a router switch, where the row is re-rendered from scratch.
let lastUpdateRowHtml: string | null = null;

/** Re-arm the write-once meta line: a reconnect, or a switch to another router. */
export function resetSysMeta(): void {
  metaWritten = false;
  // The row is re-rendered from scratch after a reconnect, and MUST be after a
  // router switch: two routers can report the same versions, so without this the
  // strip would be suppressed as "unchanged" and keep showing the previous
  // router's row. Both callers — the socket's `connect` and the router switch —
  // already go through here.
  lastUpdateRowHtml = null;
}

export function flushSysUpdate(): void {
  rafId = null;
  if (document.hidden) return; // tab backgrounded — skip render, data stays pending
  const d = pending;
  if (!d) return;
  pending = null;

  const ut = parseUptime(d.uptimeRaw);
  const uptimeDisplay = el('uptimeDisplay');
  if (uptimeDisplay) uptimeDisplay.textContent = 'Uptime: ' + ut;
  const uptimeChip = el('uptimeChip');
  if (uptimeChip) {
    uptimeChip.textContent = ut;
    uptimeChip.style.display = '';
  }

  // Storage only when the router HAS storage. `totalHdd > 0` and not merely
  // truthy: a router reporting 0 draws two gauges, not three with an empty one.
  let html = gauge('CPU', d.cpuLoad as number, 'cpu') + gauge('RAM', d.memPct as number, 'mem');
  if ((d.totalHdd as number) > 0) html += gauge('Storage', d.hddPct as number, 'hdd');
  const gaugeRow = el('gaugeRow');
  if (gaugeRow) gaugeRow.innerHTML = html;

  const sysMeta = el('sysMeta');
  if (!metaWritten && (d.boardName || d.version || d.cpuCount || d.totalMem)) {
    let meta = '';
    if (d.boardName) meta += '<div class="sys-meta-item"><strong>' + esc(d.boardName) + '</strong></div>';
    if (d.version) meta += '<div class="sys-meta-item">ROS <strong>' + esc(d.version) + '</strong></div>';
    if (d.cpuCount) meta += '<div class="sys-meta-item"><strong>' + esc(d.cpuCount) + '</strong>×CPU</div>';
    if (d.cpuFreq) meta += '<div class="sys-meta-item"><strong>' + esc(d.cpuFreq) + '</strong> MHz</div>';
    if (d.totalMem) meta += '<div class="sys-meta-item"><strong>' + fmtBytes(d.totalMem) + '</strong> RAM</div>';
    if (sysMeta) sysMeta.innerHTML = meta;
    metaWritten = true;
  }

  // AFTER the meta rewrite, deliberately. See the header.
  const tempSlot = el('sysMetaTemp');
  if (d.tempC != null) {
    if (!tempSlot) {
      const node = document.createElement('div');
      node.className = 'sys-meta-item';
      node.id = 'sysMetaTemp';
      node.innerHTML = '<strong>' + esc(d.tempC) + '°C</strong>';
      if (sysMeta) sysMeta.appendChild(node);
    } else {
      tempSlot.innerHTML = '<strong>' + esc(d.tempC) + '°C</strong>';
    }
  }

  const rosUpdateRow = el('rosUpdateRow');
  if (rosUpdateRow) {
    let ur = '';
    // Held rather than dispatched here — see the dirty check below.
    let updEvent: UpdInfo | null = null;
    if (d.updateAvailable && d.latestVersion) {
      const installedBase = (d.version || '').replace(/\s*\(.*\)/, '').trim();
      // The Update button lands in #sysUpdateAction, filled by the upgrade
      // module once it knows whether the viewer may reboot this router. Empty
      // for everyone else, so the row is unchanged for a viewer who cannot act.
      ur = '<div class="ros-update-row warn"><span class="ros-update-dot"></span>&#11014; ' +
        esc(installedBase) + ' &rarr; <strong>' + esc(d.latestVersion) +
        '</strong> available<span id="sysUpdateAction"></span></div>';
      // Published rather than read back off the DOM: the versions are already
      // parsed here, and the upgrade dialog should show what this row showed.
      //
      // Behind the dirty check below, because this event is not free: the
      // listener redraws the Update button AND emits packages:caps, so firing it
      // every tick cost a socket round trip per tick and re-created the button
      // the row had just re-created.
      updEvent = { installed: installedBase, latest: d.latestVersion, channel: d.updateChannel || '' };
    } else if (d.latestVersion) {
      ur = '<div class="ros-update-row ok"><span class="ros-update-dot"></span>&#10003; RouterOS <strong>' +
        esc(d.latestVersion) + '</strong> &mdash; Up to date</div>';
    } else if (d.updateStatus) {
      const isUnavail = /unavailable|cannot|error|failed/i.test(d.updateStatus);
      const rowCls = isUnavail ? 'ros-update-row muted' : 'ros-update-row pending';
      ur = '<div class="' + rowCls + '"><span class="ros-update-dot"></span>' + esc(d.updateStatus) + '</div>';
    } else {
      ur = '<div class="ros-update-row pending"><span class="ros-update-dot"></span>Checking for updates…</div>';
    }
    // Dirty check. Without it the row was rewritten on every poll tick, which is
    // what made the amber "available" strip and its Update button flash:
    // innerHTML destroys and recreates the node, and a newly inserted .sbtn
    // restarts its own transition. The markup IS the fingerprint here, so it
    // cannot drift out of sync with what is rendered the way a hand-written
    // field list can.
    if (ur !== lastUpdateRowHtml) {
      lastUpdateRowHtml = ur;
      rosUpdateRow.innerHTML = ur;
      // After the write, so the listener's draw() finds the #sysUpdateAction
      // slot this markup just created rather than the one it replaced.
      if (updEvent) {
        document.dispatchEvent(new CustomEvent('mikrodash:updateavailable', { detail: updEvent }));
      }
    }
  }
}

/** The `system:update` handler: store the latest, book at most one frame. */
export function noteSystemUpdate(d: SystemPayload): void {
  pending = d;
  if (!rafId) rafId = requestAnimationFrame(flushSysUpdate);
}

/** Called when the tab becomes visible again — renders what arrived while hidden. */
export function flushPendingSystem(): void {
  if (pending && !rafId) rafId = requestAnimationFrame(flushSysUpdate);
}
