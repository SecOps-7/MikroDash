package collect

// Where every collector's payload goes, declared once.
//
// ── WHY THIS FILE EXISTS ────────────────────────────────────────────────────
//
// A collector's audience was written down twice: as the first argument to its
// `emit`, and again by hand in `internal/server/ws.go`, where `pageBlur` decides
// whether anybody is still watching before it suspends the collector.
//
// Two statements of one fact, and they have disagreed FIVE times — dhcpNetworks,
// bandwidth, vpn, firewall, and routing on 2026-08-31. Each time the symptom was
// the same and silent: a dashboard card stopped updating for anybody who had
// visited the owning page and left, because the blur suspended a collector that
// was still feeding a card the guard did not know about.
//
// This is phase 4.2's core, reduced to the part that was actually buying
// something. A view declares its rooms; everything else reads the declaration.
//
// ── THE PRECEDENT WAS ALREADY HERE ──────────────────────────────────────────
//
// `logs` and `talkers` already emitted to a named constant rather than a
// literal, and the verify gates already carried an exemption for it. So this is
// the existing pattern applied to the other twenty-three, not a new one.
//
// ── ROOMS ARE PER-EVENT, NOT PER-COLLECTOR ──────────────────────────────────
//
// Two collectors emit to different audiences for different events, and flattening
// that would be wrong rather than untidy:
//
//	conns     `conn:update` reaches the page AND the dashboard card; the two
//	          detail events reach only the page, because only the page renders them.
//	ifStatus  its payload reaches three rooms; `ifstatus:names` is router-wide.
//
// So each set is named for what it is. `Union` is what the blur guard wants —
// everything this collector feeds — and it is derived rather than declared, so
// it cannot disagree with the parts.

import "strings"

// Rooms is one audience. The zero value is the ROUTER-WIDE room: every viewer of
// this router, whatever page they are on.
//
// It is a named type rather than a bare []string so that `Join` lives with it and
// nobody re-invents the comma.
type Rooms []string

// Join renders the room list as `emit` takes it.
func (r Rooms) Join() string { return strings.Join(r, ",") }

// routerWide is an emit with no room: everyone watching this router.
//
// NOT A ROOM, and the blur guard must never wait on it — every viewer occupies
// it, so guarding on it would mean never suspending at all. `dhcpNetworks` is the
// case that proves it and `ws.go` carries the reasoning at its call site.
var routerWide = Rooms{}

// ── THE DECLARATIONS ────────────────────────────────────────────────────────
//
// One line per audience. A page room is `page-<page key>`; a dashboard card room
// is `dash-card-<name>`, and the two name spaces are separate on purpose — a card
// can outlive the page that owns it.
var (
	bandwidthRooms    = Rooms{"page-bandwidth"}
	bridgesRooms      = Rooms{"page-bridges"}
	capsmanRooms      = Rooms{"page-capsman"}
	dhcpNetworksRooms = Rooms{"page-dhcp", "dash-card-network"}
	connsRooms        = Rooms{"page-connections", "dash-card-connections"}
	// connsDetailRooms: the country and source breakdowns are rendered by the
	// page and by nothing else, so sending them to the card would be traffic
	// nobody reads.
	connsDetailRooms = Rooms{"page-connections"}
	dnsRooms         = Rooms{"page-dns"}
	netwatchRooms    = Rooms{"page-dashboard"}
	firewallRooms    = Rooms{"page-firewall", "dash-card-firewall"}
	ifStatusRooms    = Rooms{"page-interfaces", "page-network-topology", "dash-card-physports"}
	logsRooms        = Rooms{"page-logs", "dash-card-logs"}
	pppRooms         = Rooms{"page-ppp"}
	packagesRooms    = Rooms{"page-packages"}
	pingRooms        = Rooms{"page-dashboard"}
	rosUsersRooms    = Rooms{"page-users"}
	queuesRooms      = Rooms{"page-queues"}
	routingRooms     = Rooms{"page-routing", "page-dashboard"}
	talkersRooms     = Rooms{"page-dashboard"}
	topologyRooms    = Rooms{"page-network-topology"}
	vlansRooms       = Rooms{"page-vlans"}
	vpnRooms         = Rooms{"page-vpn", "dash-card-vpn"}
	wanRooms         = Rooms{"page-wan"}
	// The Wi-Fi map needs BOTH: `wifi` says which access point each network is
	// on, and `wireless` says who is connected to it. Neither owns the page —
	// see internal/pages — and both have to reach it or half of it is blank.
	wifiRooms     = Rooms{"page-wifi-networks", "page-wifi-map"}
	wirelessRooms = Rooms{"page-wifi-clients", "page-wifi-map", "dash-card-wireless"}
)

// RoomsOf is every room a collector feeds, for `internal/server`'s blur guard.
//
// ── DERIVED, SO IT CANNOT DISAGREE WITH THE PARTS ───────────────────────────
//
// `conns` is why this is a union rather than a single declaration: it feeds the
// card on one event and the page on two others, and the guard needs to know
// about both. Writing the union out by hand would put the fact back in two
// places, which is what this file exists to stop.
//
// The router-wide room is deliberately absent from every entry. Three collectors
// emit to it (`system`, `dhcpLeases`, and `traffic`'s health and WAN chips) and
// two more do so alongside a page room; none of that is guardable.
//
// An unknown key returns nil, and the caller treats nil as "no rooms to wait on"
// — which suspends. `internal/verify` asserts every disableable collector has an
// entry, so nil means a collector that was never meant to be guarded.
func RoomsOf(key string) Rooms {
	switch key {
	case "bandwidth":
		return bandwidthRooms
	case "bridges":
		return bridgesRooms
	case "capsman":
		return capsmanRooms
	case "conns":
		return union(connsRooms, connsDetailRooms)
	case "dhcpNetworks":
		return dhcpNetworksRooms
	case "dns":
		return dnsRooms
	case "firewall":
		return firewallRooms
	case "ifStatus":
		return ifStatusRooms
	case "logs":
		return logsRooms
	case "netwatch":
		return netwatchRooms
	case "packages":
		return packagesRooms
	case "ping":
		return pingRooms
	case "ppp":
		return pppRooms
	case "queues":
		return queuesRooms
	case "rosusers":
		return rosUsersRooms
	case "routing":
		return routingRooms
	case "talkers":
		return talkersRooms
	case "topology":
		return topologyRooms
	case "vlans":
		return vlansRooms
	case "vpn":
		return vpnRooms
	case "wan":
		return wanRooms
	case "wifi":
		return wifiRooms
	case "wireless":
		return wirelessRooms
	}
	return nil
}

// keepAliveFor is rooms where a collector must keep RUNNING for another
// collector's sake, and which are not part of its own audience.
//
// ── IT EMPTIED ONCE, AND THAT IS WHY THE MECHANISM STAYED ──────────────────
//
// The first entry was `conns`, which emits to the Connections page and its
// dashboard card and to nothing else -- but `bandwidth` read the connection
// table it deposited in `ConnTable`, so suspending `conns` while somebody was on
// the BANDWIDTH page starved a page `conns` never sends to. Modelling that as an
// audience would have been wrong: nothing is ever emitted there.
//
// `ConnTable` is gone. Both collectors SUBSCRIBE to
// `/ip/firewall/connection/print` now, so `bandwidth` holds its own demand on
// the menu and a suspended `conns` starves nothing.
// `TestBothConnectionConsumersShareOneRead` is the proof -- it stops
// `connections` and asserts the menu still has a subscriber.
//
// The map was kept empty rather than deleted, on the grounds that "the next
// cross-collector dependency will need it". Phase 4.2b is that next one, and it
// arrived four days later.
//
// ── `ifStatus` IS THE ENTRY, AND DEMAND IS WHAT MADE IT NECESSARY ──────────
//
// Under the page switchboard `ifStatus` was never gated at all: it ran from
// connect to teardown, and `resumePage` said so out loud --
//
//	interfaceStatus is NOT suspended on blur — three other collectors take it
//	as their rate source, and a bridges viewer who never opens Interfaces would
//	otherwise see every throughput column go blank.
//
// Demand gates every collector on its rooms, so that sentence stops being a
// comment and becomes a bug: `ifStatus` emits to Interfaces, Network Topology
// and the Physical Ports card, and it would suspend for a viewer sitting on
// Bridges. Its `Rates()` returns an availability flag and the payload renders
// null rather than breaking, so the failure is not a crash -- it is every
// throughput column on four pages quietly reading "—".
//
// FIVE CONSUMERS take it as their `RateSource`, built in session.go: bridges,
// vlans, wan, topology and bandwidth. Topology already shares a room with it, so
// the rooms named here are the other four.
//
// This is a DEPENDENCY, not an audience, and the distinction is the whole reason
// this map is separate from `RoomsOf`: nothing is ever emitted to these rooms by
// `ifStatus`, and `RoomsOf` must keep meaning "who receives this collector's
// payload" for the emit-side gates that read it.
//
// ── `dhcpLeases` IS THE OTHER ONE, AND IT HAS NO AUDIENCE AT ALL ───────────
//
// It emits ROUTER-WIDE — `EvLeasesList.Emit(d.emit, "", …)` — because the live app
// does, and because two consumers live outside the DHCP page: the Connections
// page names a device by its IP from the lease table, and four collectors read
// the leases in process. A router-wide emit is not a room, so `RoomsOf` returns
// nothing for it and demand would suspend it the moment the switchboard stopped
// resuming it by name. The DHCP page would then sit on "Waiting for lease
// data…", and every connection would render as a bare address.
//
// The rooms named are its real consumers: the DHCP page (through `dhcpNetworks`,
// which shares it), and the four collectors that take it as a source — conns,
// wireless, topology and bandwidth, wired in session.go.
var keepAliveFor = map[string]Rooms{
	"ifStatus": union(bridgesRooms, vlansRooms, wanRooms, bandwidthRooms),
	"dhcpLeases": union(dhcpNetworksRooms, connsRooms, connsDetailRooms,
		wirelessRooms, topologyRooms, bandwidthRooms),
	// ── `arp` HAS NO AUDIENCE AT ALL, NOT EVEN A ROUTER-WIDE ONE ───────────
	//
	// It emits nothing. Its whole output is an in-memory IP<->MAC index that
	// four collectors read, so the rooms that should keep it running are THEIRS:
	// the Connections and Bandwidth pages ask it IP→MAC to reach a lease, and
	// WiFi Clients and Network Topology ask it MAC→IP for an address their own
	// rows do not carry.
	"arp": union(connsRooms, connsDetailRooms, bandwidthRooms,
		wirelessRooms, topologyRooms),
}

// DeclaredRoomKeys is every collector `RoomsOf` answers for.
//
// ── A SWITCH CANNOT BE ENUMERATED, AND SOMETHING HAS TO ────────────────────
//
// `RoomsOf` is a switch, so nothing outside it can ask "which collectors have an
// audience at all". `internal/session` needs exactly that: a collector that
// declares rooms and is missing from the dormancy target table is one demand
// never asks about, and it would never start for a viewer — silently, because a
// collector that is never resumed looks like one with nothing to report.
//
// A LIST BESIDE A SWITCH GOES STALE, which is why `rooms_test.go` keeps its own
// copy and `TestDeclaredRoomKeysMatchesTheSwitch` fails the moment the two
// disagree. That is the same arrangement `session.targetKeys` has with
// `session.targets()`, and for the same reason.
func DeclaredRoomKeys() []string {
	return []string{
		"bandwidth", "bridges", "capsman", "conns", "dhcpNetworks", "dns",
		"firewall", "ifStatus", "logs", "netwatch", "packages", "ping", "ppp",
		"queues", "rosusers", "routing", "talkers", "topology", "vlans", "vpn",
		"wan", "wifi", "wireless",
	}
}

// DemandRooms is every room whose occupancy should keep a collector RUNNING: its
// own audience plus whatever it must stay alive for.
//
// THE ONE QUESTION `internal/server`'s demand rule asks. Splitting it from
// `RoomsOf` keeps the audience honest -- a gate that checks where a payload is
// sent must not be handed a room nothing is ever sent to.
func DemandRooms(key string) Rooms { return union(RoomsOf(key), keepAliveFor[key]) }

// `Others(key, pageKey)` used to live here: the audience minus one blurred page,
// which is the calculation `pageBlur`'s seven guard call sites each spelled out
// by hand before 4.2. Phase 4.2b deleted the switchboard, so there is no blurred
// page to subtract from anything -- demand asks whether ANY room is occupied,
// which is `DemandRooms` above. Recorded rather than silently dropped, because
// the function is named in the port record and in several comments.

func union(sets ...Rooms) Rooms {
	seen := map[string]bool{}
	var out Rooms
	for _, s := range sets {
		for _, r := range s {
			if r == "" || seen[r] {
				continue
			}
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}
