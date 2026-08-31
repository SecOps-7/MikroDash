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
import { renderTalkers, type TalkersPayload } from './dashboard-talkers';
import { renderNetwatch, type NetwatchPayload } from './dashboard-netwatch';
import { renderVpnCard, type VpnPayload } from './dashboard-vpn';
import { noteSystemUpdate, flushPendingSystem, resetSysMeta, type SystemPayload } from './dashboard-system';
import { noteConnUpdate, flushPendingConn, resetConnCaches, type ConnPayload } from './dashboard-conn';
import { renderNetworks, type LanOverviewPayload } from './dashboard-networks';
import { onPingUpdate, onPingHistory, resetPing, type PingPayload, type PingHistoryPayload } from './dashboard-ping';
import { renderWirelessCards, type WirelessPayload } from './dashboard-card-wireless';
import { renderIpUtilCard, type IpUtilPayload } from './dashboard-card-iputil';
import { renderPhysPortsCard, type IfStatusPayload } from './dashboard-card-physports';
import { renderRoutingCards, resetRoutingCards, type RoutingPayload } from './dashboard-card-routing';
import { renderBandwidthCard, setBwRouters, setBwActiveRouter, resetBandwidthCard,
  type BwRouter, type TrafficSample as BwSample } from './dashboard-card-bandwidth';
import { renderFwActionsCard, type FirewallPayload } from './dashboard-card-fwactions';
import { onLogsHistory, onLogsNew, resetLogsCard, type LogEntry } from './dashboard-card-logs';
import { renderDiagnosticsCard, type DiagnosticsPayload } from './dashboard-card-diagnostics';
import { renderConnListCards, type ConnCardsPayload } from './dashboard-card-connlists';
import { createConnMap, type MapCountry } from './dashboard-card-map';
import { renderConnFlowCard } from './dashboard-card-connflow';
import { renderStreamHealth, renderWanStatus, type StreamHealth, type WanStatus }
  from './dashboard-stream-health';
import { initTraffic, hideTrafficChart, resetTraffic, resetTrafficOnReconnect } from './dashboard-traffic';

// The Connections Map, built once. `worldmap:ready` tells it when the world map
// module has published its path data — until then a payload is held.
const connMap = createConnMap();

export function initDashboard(socket: Socket): void {
  // ROUTER-WIDE, like the collector that sends it: these are the top bar's
  // gauges and the uptime chip, which a viewer sees on every page.
  socket.on('system:update', (d) => noteSystemUpdate(d as SystemPayload));

  socket.on('talkers:update', (d) => renderTalkers(d as TalkersPayload));
  socket.on('netwatch:update', (d) => renderNetwatch(d as NetwatchPayload));
  // The VPN collector emits the same payload into the page room and the card
  // room, so this handler runs for a viewer on either.
  socket.on('vpn:update', (d) => renderVpnCard(d as VpnPayload));
  socket.on('conn:update', (d) => {
    noteConnUpdate(d as ConnPayload);
    // Three EXTRA cards on the same payload: Top Countries, Top Ports and the
    // Connections Map. The Flow sankey is a later slice.
    renderConnListCards(d as ConnCardsPayload);
    connMap.onConnUpdate((d as ConnCardsPayload).topCountries as MapCountry[] || []);
    // The FOURTH card on this payload: the Connection Flow sankey, which reuses
    // the connections page's renderer against the card's own elements.
    const cd = d as { topSources?: never; topDestinations?: never };
    renderConnFlowCard(cd.topSources, cd.topDestinations);
  });
  // A SECOND subscriber to this event: `pages/dhcp.ts` draws the subnet table
  // and the pool gauge from the same payload. The live app splits it the same
  // way, with a second handler further down its file.
  socket.on('lan:overview', (d) => {
    renderNetworks(d as LanOverviewPayload);
    // A THIRD consumer of this payload, after pages/dhcp.ts and the Networks
    // card: the IP Utilisation extra card.
    renderIpUtilCard(d as IpUtilPayload);
  });
  socket.on('ifstatus:update', (d) => renderPhysPortsCard(d as IfStatusPayload));
  socket.on('ping:update', (d) => onPingUpdate(d as PingPayload));
  socket.on('ping:history', (d) => onPingHistory(d as PingHistoryPayload));
  // Two EXTRA cards on one event: Signal Health and Band Split.
  socket.on('wireless:update', (d) => renderWirelessCards(d as WirelessPayload));
  // Two more EXTRA cards on one event: Routes and BGP Peers.
  socket.on('routing:update', (d) => renderRoutingCards(d as RoutingPayload));
  // The Bandwidth card. A SECOND subscriber to traffic:update — the chart takes
  // only its selected interface, this card takes every sample, because the
  // collector already emits per-socket for the default one.
  socket.on('traffic:update', (d) => renderBandwidthCard(d as BwSample));
  socket.on('firewall:update', (d) => renderFwActionsCard(d as FirewallPayload));
  socket.on('diagnostics:update', (d) => renderDiagnosticsCard(d as DiagnosticsPayload));
  socket.on('stream:health', (d) => renderStreamHealth(d as StreamHealth));
  socket.on('wan:status', (d) => renderWanStatus(d as WanStatus));
  socket.on('logs:history', (d) => onLogsHistory(d as LogEntry[]));
  socket.on('logs:new', (d) => onLogsNew(d as LogEntry));
  socket.on('routers:update', (d) => setBwRouters(d as BwRouter[]));
  socket.on('router:active', (d) => setBwActiveRouter((d as { activeId?: string } | undefined)?.activeId));
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

  document.addEventListener('dashcard:room:focus', (e) => {
    const room = (e as CustomEvent).detail;
    if (typeof room === 'string') socket.emit('dashcard:focus', room);
  });
  document.addEventListener('dashcard:room:blur', (e) => {
    const room = (e as CustomEvent).detail;
    if (typeof room === 'string') socket.emit('dashcard:blur', room);
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
  // The live handler also unpauses the topology SVG and restores the traffic
  // chart. Those belong to cards this port has not reached; they join here when
  // they do.
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) {
      // The canvas is hidden here so the keepalive's catch-up happens
      // invisibly; the next sample fades it back in.
      hideTrafficChart();
      return;
    }
    if (isRosDisconnected()) return;
    flushPendingSystem();
    flushPendingConn();
  });
  // Bound to BLUR as well, as the live app is: dropping behind another
  // application does not reliably fire visibilitychange, and blur does.
  window.addEventListener('blur', () => hideTrafficChart());
}

/** The router-switch half of the card resets. See the header. */
export { resetSysMeta, resetConnCaches, resetTraffic, resetPing, resetRoutingCards, resetBandwidthCard, resetLogsCard };
