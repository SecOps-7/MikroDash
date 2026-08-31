// The two banners, the body classes that go with them, and the topbar clock.
//
// ── TWO DIFFERENT OUTAGES, AND THEY ARE NOT THE SAME ────────────────────────
//
// The RED banner means the BROWSER lost its socket to the MikroDash server.
// The AMBER one means the server is fine and ROUTEROS is unreachable. They are
// separate conditions with separate causes, and the amber one is suppressed
// while the red one is showing: told both, an operator learns nothing from the
// second, and the actionable message is the one about the connection they can
// actually see is down.
//
// ── THE ROUTER'S STATE HAS TO OUTLIVE THE SOCKET ────────────────────────────
//
// `rosDisconnected` is remembered across a reconnect. Without it, a browser that
// reconnects to a server whose router is STILL down clears the red banner and
// shows nothing at all — the most reassuring possible display of a broken
// system. The live app keeps that flag for exactly this reason and so does this.
//
// ── CLASSES, NOT INLINE STYLES ──────────────────────────────────────────────
//
// The stylesheet says `#rosBanner{display:none}` and `#rosBanner.show{display:flex}`.
// Toggling `.show` lets the stylesheet decide; writing `style.display` inline
// wins over it permanently, which is the same absent-versus-set trap the
// appearance layer documents. The body classes matter too — `is-disconnected`
// and `is-ros-disconnected` dim the sidebar and main panel and hide the flow
// dots, so omitting them leaves a live-looking UI over dead data.
//
// ── THE EVENT NAME DIFFERS FROM THE LIVE APP, DELIBERATELY ──────────────────
//
// The live server emits TWO events: `ros:status` — this session's RouterOS
// reachability, which drives this banner — and `router:status`, a global
// per-router announcement for the Routers list. This port's server emits one
// ROOM-SCOPED `router:status` carrying `{routerId, connected, reason}`, which
// answers the first question for the router this browser is watching. That is a
// mechanism change of the kind the port allows; the rendered result is what must
// not move, and that is what the gate compares.

import { el } from './dom.js';
import { getDisplayTimezone } from './caps.js';

let rosDisconnected = false;

interface SvgAnimations extends HTMLElement {
  pauseAnimations?: () => void;
  unpauseAnimations?: () => void;
}

/** Flow-dot animations stop while data is not arriving, so a frozen diagram
 *  does not read as a moving one. */
function pauseDiagram(): void {
  (el('netDiagram') as SvgAnimations | null)?.pauseAnimations?.();
}
/** Only when BOTH the socket and the router are back, and the tab is visible —
 *  resuming an animation nobody is looking at is work for nothing. */
function resumeDiagram(): void {
  if (rosDisconnected || document.hidden) return;
  (el('netDiagram') as SvgAnimations | null)?.unpauseAnimations?.();
}
/** The live rates are the most obviously wrong thing to leave standing: a number
 *  that stopped updating looks exactly like a number that stopped changing. */
function blankRates(): void {
  const rx = el('liveRx');
  const tx = el('liveTx');
  if (rx) rx.textContent = '—';
  if (tx) tx.textContent = '—';
}

/**
 * Whether the ROUTER (not the socket) is currently down.
 *
 * Read by the Dashboard's visibility handler: the live app flushes what
 * accumulated while the tab was hidden only when the router is up, because a
 * flush while it is down would repaint the card with the last numbers from
 * before the outage and make a dead router look alive.
 */
export function isRosDisconnected(): boolean {
  return rosDisconnected;
}

export function setRosBanner(connected: boolean, reason?: string | null): void {
  const ros = el('rosBanner');
  if (!ros) return;
  rosDisconnected = !connected;
  const reconnect = el('reconnectBanner');
  if (connected) {
    ros.classList.remove('show');
    document.body.classList.remove('is-ros-disconnected');
    resumeDiagram();
  } else {
    const text = el('rosBannerText');
    if (text) text.textContent = reason || 'RouterOS not connected — retrying…';
    // Suppressed while the red banner is up; see the header.
    if (!reconnect || !reconnect.classList.contains('show')) ros.classList.add('show');
    document.body.classList.add('is-ros-disconnected');
    pauseDiagram();
    blankRates();
  }
}

export function onSocketDisconnect(): void {
  el('reconnectBanner')?.classList.add('show');
  // The amber one comes DOWN: the red one outranks it, and two banners stacked
  // is worse than either alone.
  el('rosBanner')?.classList.remove('show');
  document.body.classList.add('is-disconnected');
  pauseDiagram();
  blankRates();
}

export function onSocketConnect(): void {
  el('reconnectBanner')?.classList.remove('show');
  document.body.classList.remove('is-disconnected');
  // The router may still be down. Restore the amber banner from the remembered
  // flag rather than waiting for the next status event, which may be a poll away.
  if (rosDisconnected) {
    el('rosBanner')?.classList.add('show');
    document.body.classList.add('is-ros-disconnected');
  } else {
    document.body.classList.remove('is-ros-disconnected');
  }
  resumeDiagram();
}

/**
 * The topbar clock.
 *
 * Two formatters, and they are not interchangeable. With a display timezone set
 * the install has chosen a zone and `Intl` is the only thing that can render it;
 * without one the browser's own clock is right, and building it by hand avoids
 * constructing a formatter every second for an answer `getHours()` already has.
 *
 * The text is written only when it CHANGES. At one tick a second that is 59
 * pointless DOM writes a minute avoided, and it is what makes running this
 * unconditionally cheap enough not to think about.
 *
 * The element id really is `tobarClock`. The typo is in the live markup and in
 * the live lookup, so it works; correcting one side here would find nothing.
 */
export function initClock(): void {
  const node = el('tobarClock');
  if (!node) return;
  let last = '';
  const tick = (): void => {
    const tz = getDisplayTimezone();
    let str: string;
    if (tz) {
      str = new Intl.DateTimeFormat('en-GB', {
        timeZone: tz, hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
      }).format(new Date());
    } else {
      const now = new Date();
      str = now.getHours().toString().padStart(2, '0') + ':' +
            now.getMinutes().toString().padStart(2, '0') + ':' +
            now.getSeconds().toString().padStart(2, '0');
    }
    if (str !== last) {
      last = str;
      node.textContent = str;
    }
  };
  tick();
  setInterval(tick, 1000);
}
