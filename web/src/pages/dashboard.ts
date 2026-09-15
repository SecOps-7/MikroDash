// The Dashboard's wiring: what turns four gated renderers into a live page.
//
// ── WHY THIS FILE EXISTS AT ALL ─────────────────────────────────────────────
//
// Each card was ported with a gate that drives its renderer directly and
// compares the DOM against the live one. Every one of those passed while
// NOTHING CALLED THE RENDERER — a function that is never invoked still renders
// correctly when a test invokes it, so a DOM gate cannot tell a wired page from
// an unwired one. That is the same shape as the four defects found earlier in
// this port (reorder arrows, schedule buttons, firewall sub-tabs, `rptSchedNew`)
// and it is why `dashboard-wiring-check.js` asserts the subscriptions exist
// rather than trusting that a rendered card implies a listening one.
//
// ── THE SYSTEM CARD NEEDS THREE SIGNALS, NOT ONE ────────────────────────────
//
// Its payload handler is only the first. `_sysMetaWritten` is re-armed on
// CONNECT and on ROUTER SWITCH in the live app — both, because another router is
// another board, and a meta line written once would otherwise keep the old
// board's name under the new router's data. And a tab that was hidden holds its
// last payload pending, so coming back into view has to flush it.

import type { Socket } from '../socket';
import { isRosDisconnected } from '../banners';
import { renderTalkers } from './dashboard-talkers';
import { renderNetwatch } from './dashboard-netwatch';
import { renderVpnCard } from './dashboard-vpn';
import { noteSystemUpdate, flushPendingSystem, resetSysMeta } from './dashboard-system';
import { noteConnUpdate, flushPendingConn, resetConnCaches } from './dashboard-conn';
import { renderNetworks } from './dashboard-networks';
import { onPingUpdate, onPingHistory, resetPing } from './dashboard-ping';
import { renderWirelessCards } from './dashboard-card-wireless';
import { renderIpUtilCard } from './dashboard-card-iputil';
import { renderPhysPortsCard } from './dashboard-card-physports';
import { renderWiredCount } from './dashboard-netflow';
import { renderRoutingCards, resetRoutingCards } from './dashboard-card-routing';
import { renderBandwidthCard, setBwRouters, setBwActiveRouter, resetBandwidthCard }
  from './dashboard-card-bandwidth';
import { renderFwActionsCard } from './dashboard-card-fwactions';
import { onLogsHistory, onLogsNew, resetLogsCard } from './dashboard-card-logs';
import { renderDiagnosticsCard } from './dashboard-card-diagnostics';
import { renderConnListCards } from './dashboard-card-connlists';
import { createConnMap } from './dashboard-card-map';
import { renderConnFlowCard } from './dashboard-card-connflow';
import { renderStreamHealth, renderWanStatus } from './dashboard-stream-health';
import { initTraffic, resumeTrafficChart, resetTraffic, resetTrafficOnReconnect } from './dashboard-traffic';

// The Connections Map, built once. `worldmap:ready` tells it when the world map
// module has published its path data — until then a payload is held.
const connMap = createConnMap();

export function initDashboard(socket: Socket): void {
  // ROUTER-WIDE, like the collector that sends it: these are the top bar's
  // gauges and the uptime chip, which a viewer sees on every page.
  socket.on('system:update', (d) => noteSystemUpdate(d));

  socket.on('talkers:update', (d) => renderTalkers(d));
  socket.on('netwatch:update', (d) => renderNetwatch(d));
  // The VPN collector emits the same payload into the page room and the card
  // room, so this handler runs for a viewer on either.
  socket.on('vpn:update', (d) => renderVpnCard(d));
  socket.on('conn:update', (d) => {
    noteConnUpdate(d);
    // Three EXTRA cards on the same payload: Top Countries, Top Ports and the
    // Connections Map. The Flow sankey is a later slice.
    renderConnListCards(d);
    connMap.onConnUpdate(d.topCountries || []);
    // The FOURTH card on this payload: the Connection Flow sankey, which reuses
    // the connections page's renderer against the card's own elements.
    renderConnFlowCard(d.topSources, d.topDestinations);
  });
  // A SECOND subscriber to this event: `pages/dhcp.ts` draws the subnet table
  // and the pool gauge from the same payload. The live app splits it the same
  // way, with a second handler further down its file.
  socket.on('lan:overview', (d) => {
    renderNetworks(d);
    // A THIRD consumer of this payload, after pages/dhcp.ts and the Networks
    // card: the IP Utilisation extra card.
    renderIpUtilCard(d);
  });
  socket.on('ifstatus:update', (d) => renderPhysPortsCard(d));
  // The Network Flow card's Wired count, from the names event every browser
  // receives and gets on router select. It was written by the Interfaces page
  // from `ifstatus:update`, which this card never subscribes to, so it stayed
  // empty until Interfaces or Topology had been opened (issue #132).
  socket.on('ifstatus:names', (d) => renderWiredCount(d));
  socket.on('ping:update', (d) => onPingUpdate(d));
  socket.on('ping:history', (d) => onPingHistory(d));
  // Two EXTRA cards on one event: Signal Health and Band Split.
  socket.on('wireless:update', (d) => renderWirelessCards(d));
  // Two more EXTRA cards on one event: Routes and BGP Peers.
  socket.on('routing:update', (d) => renderRoutingCards(d));
  // The Bandwidth card. A SECOND subscriber to traffic:update — the chart takes
  // only its selected interface, this card takes every sample, because the
  // collector already emits per-socket for the default one.
  socket.on('traffic:update', (d) => renderBandwidthCard(d));
  socket.on('firewall:update', (d) => renderFwActionsCard(d));
  socket.on('diagnostics:update', (d) => renderDiagnosticsCard(d));
  socket.on('stream:health', (d) => renderStreamHealth(d));
  socket.on('wan:status', (d) => renderWanStatus(d));
  socket.on('logs:history', (d) => onLogsHistory(d));
  socket.on('logs:new', (d) => onLogsNew(d));
  socket.on('routers:update', (d) => setBwRouters(d));
  // `?.` kept: this handler has always tolerated a missing payload.
  socket.on('router:active', (d) => setBwActiveRouter(d?.activeId));
  // ── The grid's room events, relayed to the socket ─────────────────────────
  //
  // `dashboard-grid-store.ts` and the editor DISPATCH `dashcard:room:focus` and
  // `dashcard:room:blur` on the document; this is what turns them into a
  // subscription. Without it every room join the grid computes reaches nobody —
  // which is exactly what it did until Part 65.
  //
  // A relay rather than a direct call because the grid must not know about the
  // socket: it is driven by pointer events and observers, and the live app keeps
  // the same separation.
  document.addEventListener('worldmap:ready', () => connMap.init());
  // Already published? Then initialise now — the event has been and gone.
  if ((window as unknown as { _worldMapPathDs?: unknown })._worldMapPathDs) connMap.init();

  // ── EACH CARD ROOM IS SENT ONCE PER CONNECTION ─────────────────────────────
  //
  // The grid re-syncs its rooms on first paint, on every connect and whenever
  // the dashboard becomes active, and the server replays a card's latest payload
  // on every `dashcard:focus` — so a page load subscribed each card three times
  // and received three replays. The server remembers the subscriptions for the
  // connection (`conn.cards`) and re-joins them on a router select, so one send
  // per connection is all it needs. A disconnect ends that connection's
  // subscriptions, so the record goes with it; a blur ends one room's.
  const sentRooms = new Set<string>();
  socket.on('disconnect', () => sentRooms.clear());
  document.addEventListener('dashcard:room:focus', (e) => {
    const room = (e as CustomEvent).detail;
    if (typeof room !== 'string' || sentRooms.has(room)) return;
    sentRooms.add(room);
    socket.emit('dashcard:focus', room);
  });
  document.addEventListener('dashcard:room:blur', (e) => {
    const room = (e as CustomEvent).detail;
    if (typeof room !== 'string') return;
    sentRooms.delete(room);
    socket.emit('dashcard:blur', room);
  });

  // The chart owns two events and two selects, so it wires itself.
  initTraffic(socket);

  // A reconnect may be to a router whose board differs from the one whose meta
  // line is on screen. `wireBanners` also subscribes to `connect`; the socket
  // keeps a list per event, so both run.
  socket.on('connect', () => resetSysMeta());

  // ── AND THE TRAFFIC HISTORY, WHICH THIS PORT WAS NOT CLEARING ─────────────
  //
  // The live `connect` handler clears `currentIf` and `allPoints`
  // (`../MikroDash/public/app.js:2957`) for the same reason it resets the meta
  // line one statement earlier. This port cleared them only on a ROUTER SWITCH,
  // so a socket gap left the chart holding samples from before it — the new
  // history arrives and is appended to the old, and the window is drawn across
  // a period during which nothing was being received. A chart that bridges its
  // own outage is the traffic equivalent of a dead router looking alive.
  // resetTrafficOnReconnect, NOT resetTraffic: the full reset also clears the
  // operator's chosen interface, and a reconnect must not. See that function.
  socket.on('connect', () => resetTrafficOnReconnect());

  // ── Coming back into view ─────────────────────────────────────────────────
  //
  // Guarded on the ROUTER being up, exactly as the live handler is. Flushing
  // while it is down would repaint the card with the last numbers from before
  // the outage, which makes a dead router look alive — the one thing the ROS
  // banner exists to prevent.
  //
  // The live handler also unpauses the topology SVG, which belongs to a card
  // this port has not reached; it joins here when it does.
  document.addEventListener('visibilitychange', () => {
    // NOTHING IS HIDDEN ON THE WAY OUT. The traffic chart used to be blanked
    // here and faded back in on the next sample, which blanked it for up to a
    // second whenever the browser reported the page hidden. It is redrawn at
    // the current time on the way back instead, before the page is painted.
    if (document.hidden) return;
    if (isRosDisconnected()) return;
    flushPendingSystem();
    flushPendingConn();
    resumeTrafficChart();
  });
  // NOT bound to window blur, though the live app was: a window that loses
  // focus while still on screen keeps painting, so there is nothing to catch up.
}

/** The router-switch half of the card resets. See the header. */
export { resetSysMeta, resetConnCaches, resetTraffic, resetPing, resetRoutingCards, resetBandwidthCard, resetLogsCard };
