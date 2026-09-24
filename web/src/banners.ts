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
// shows nothing at all - the most reassuring possible display of a broken
// system. The live app keeps that flag for exactly this reason and so does this.
//
// ── CLASSES, NOT INLINE STYLES ──────────────────────────────────────────────
//
// The stylesheet says `#rosBanner{display:none}` and `#rosBanner.show{display:flex}`.
// Toggling `.show` lets the stylesheet decide; writing `style.display` inline
// wins over it permanently, which is the same absent-versus-set trap the
// appearance layer documents. The body classes matter too - `is-disconnected`
// and `is-ros-disconnected` dim the sidebar and main panel and hide the flow
// dots, so omitting them leaves a live-looking UI over dead data.
//
// ── THE EVENT NAME DIFFERS FROM THE LIVE APP, DELIBERATELY ──────────────────
//
// The live server emits TWO events: `ros:status` - this session's RouterOS
// reachability, which drives this banner - and `router:status`, a global
// per-router announcement for the Routers list. This port's server emits one
// `router:status` carrying `{routerId, connected, reason}` for EVERY router, to
// every browser, and main.ts shows it here only when `routerId` is the router on
// screen. It was once room-scoped and answered this banner's question alone;
// the fleet-wide broadcast added later made an ungated banner report other
// routers' outages as this one's.

import { fmtTime } from './timefmt';
import { el } from './dom.js';

let rosDisconnected = false;
let socketDown = false;

interface SvgAnimations extends HTMLElement {
  pauseAnimations?: () => void;
  unpauseAnimations?: () => void;
}

/** Flow-dot animations stop while data is not arriving, so a frozen diagram
 *  does not read as a moving one. */
function pauseDiagram(): void {
  (el('netDiagram') as SvgAnimations | null)?.pauseAnimations?.();
}
/**
 * Only when BOTH the socket and the router are back, and the tab is visible -
 * resuming an animation nobody is looking at is work for nothing.
 *
 * `socketDown` IS PART OF THE CONDITION, not decoration. This comment claimed
 * "both" while the test was `rosDisconnected` alone, so a resume driven by the
 * tab coming back (below) would have unpaused a diagram whose socket was still
 * down. The CSS hides the dots then, so nothing would have been visible - which
 * is exactly the kind of accidental cover that turns into a bug the moment the
 * rule changes.
 */
function resumeDiagram(): void {
  if (rosDisconnected || socketDown || document.hidden) return;
  (el('netDiagram') as SvgAnimations | null)?.unpauseAnimations?.();
}

/**
 * The tab going away and coming back - the THIRD thing that moves the diagram,
 * and leaving it out is what made the card stop for good.
 *
 * Every resume above is refused while `document.hidden`, and until this existed
 * nothing retried one. So a router or socket outage that ENDED while the tab was
 * in the background left the SVG timeline paused with no remaining trigger: the
 * banners came down, the body classes came off, the dots became visible again -
 * and stood still, on a dashboard reporting a healthy router. The operator's
 * report ("sometimes the animation stops") is that state, and a background tab
 * is where it is reached, because `socket.ts` reconnects on a backoff timer that
 * keeps firing while hidden.
 *
 * The live app paired the `document.hidden` guard with exactly this handler
 * (`public/app.js:2349` at v0.7.40); this port kept the guard and dropped the
 * handler. Pausing on hide is the other half and is kept for the same reason it
 * was written: a hidden tab should not be animating.
 */
export function initDiagramVisibility(): void {
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) pauseDiagram();
    else resumeDiagram();
  });
}

/** The live rates are the most obviously wrong thing to leave standing: a number
 *  that stopped updating looks exactly like a number that stopped changing. */
function blankRates(): void {
  const rx = el('liveRx');
  const tx = el('liveTx');
  if (rx) rx.textContent = '-';
  if (tx) tx.textContent = '-';
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
    if (text) text.textContent = reason || 'RouterOS not connected - retrying…';
    // Suppressed while the red banner is up; see the header.
    if (!reconnect || !reconnect.classList.contains('show')) ros.classList.add('show');
    document.body.classList.add('is-ros-disconnected');
    pauseDiagram();
    blankRates();
  }
}

export function onSocketDisconnect(): void {
  socketDown = true;
  el('reconnectBanner')?.classList.add('show');
  // The amber one comes DOWN: the red one outranks it, and two banners stacked
  // is worse than either alone.
  el('rosBanner')?.classList.remove('show');
  document.body.classList.add('is-disconnected');
  pauseDiagram();
  blankRates();
}

export function onSocketConnect(): void {
  socketDown = false;
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
    const str = fmtTime(Date.now());
    if (str !== last) {
      last = str;
      node.textContent = str;
    }
  };
  tick();
  setInterval(tick, 1000);
}
