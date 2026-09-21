package collect

// The events the collectors send, and the payload each one carries.
//
// ONE LIST, because it is this package's half of the WebSocket contract.
// cmd/tsgen reads these declarations to write the browser's event map, so an
// event added here is typed in the browser by the next `go run ./cmd/tsgen`,
// and one removed here stops the frontend compiling wherever a page listens for
// it. How a declaration works is in internal/hub/event.go.
//
// A `map[string]any` payload has no struct to generate from. Its TypeScript
// type is written by hand in web/src/events-hand.ts, and tsc fails if that file
// and the declarations disagree about which events those are.
//
// An event this package owns but the server sends — a replay on page open, the
// traffic history — is declared here too, next to its payload type, so each
// event is declared once however many places send it.

import "mikrodash/internal/hub"

var (
	EvBandwidthUpdate = hub.Declare[BandwidthPayload]("bandwidth:update")
	EvBridgesUpdate   = hub.Declare[BridgesPayload]("bridges:update")
	EvCapsmanUpdate   = hub.Declare[CapsmanPayload]("capsman:update")
	EvConnUpdate      = hub.Declare[ConnsLight]("conn:update")
	EvConnCountryData = hub.Declare[map[string]any]("conn:country-data")
	EvConnSourceData  = hub.Declare[map[string]any]("conn:source-data")
	// Every connection, for the Connections page's List tab (connlist.go).
	EvConnList       = hub.Declare[ConnListPayload]("conn:list")
	EvDnsUpdate      = hub.Declare[DNSPayload]("dns:update")
	EvFirewallUpdate = hub.Declare[FirewallPayload]("firewall:update")
	EvIfstatusUpdate = hub.Declare[IfStatusPayload]("ifstatus:update")
	EvIfstatusNames  = hub.Declare[IfNamesPayload]("ifstatus:names")
	EvLanOverview    = hub.Declare[LanPayload]("lan:overview")
	EvLanWan         = hub.Declare[map[string]any]("lan:wan")
	EvLeasesList     = hub.Declare[LeasesPayload]("leases:list")
	EvLogsHistory    = hub.Declare[[]LogEntry]("logs:history")
	EvLogsNew        = hub.Declare[LogEntry]("logs:new")
	EvNetwatchUpdate = hub.Declare[NetwatchPayload]("netwatch:update")
	EvPackagesUpdate = hub.Declare[PackagesPayload]("packages:update")
	EvPingUpdate     = hub.Declare[PingPayload]("ping:update")
	EvPppUpdate      = hub.Declare[PPPPayload]("ppp:update")
	EvQueuesUpdate   = hub.Declare[QueuesPayload]("queues:update")
	EvRosusersUpdate = hub.Declare[RosUsersPayload]("rosusers:update")
	EvRoutingUpdate  = hub.Declare[RoutingPayload]("routing:update")
	EvStreamHealth   = hub.Declare[map[string]any]("stream:health")
	EvSystemUpdate   = hub.Declare[SystemPayload]("system:update")
	EvTalkersUpdate  = hub.Declare[TalkersPayload]("talkers:update")
	EvTopologyUpdate = hub.Declare[TopologyPayload]("topology:update")
	EvTrafficHistory = hub.Declare[TrafficHistory]("traffic:history")
	EvTrafficUpdate  = hub.Declare[TrafficSample]("traffic:update")
	EvAreaUpdate     = hub.Declare[AreaPayload]("area:update")
	EvVlansUpdate    = hub.Declare[VlansPayload]("vlans:update")
	EvVpnUpdate      = hub.Declare[VPNPayload]("vpn:update")
	EvWanStatus      = hub.Declare[WanStatus]("wan:status")
	EvWanUpdate      = hub.Declare[WANPayload]("wan:update")
	EvWifiUpdate     = hub.Declare[WifiPayload]("wifi:update")
	EvWirelessUpdate = hub.Declare[WirelessPayload]("wireless:update")
)
