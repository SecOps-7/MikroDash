package collect

// Bridges collector — the port of src/collectors/bridges.js.
//
//	/interface/bridge        the bridges themselves
//	/interface/bridge/port   each port's STP role, edge/learn/horizon and PVID
//	/interface/bridge/host   the learned MAC table
//
// `/interface/bridge/vlan` IS NOT READ. That table belongs to the VLANs page,
// which already fetches it; reading it here would be a second copy of the same
// rows on a second poll loop.
//
// RATES ARE BORROWED, NOT FETCHED. interfaceStatus already computes rxMbps and
// txMbps for every interface, bridges included, so a bridge's throughput costs
// no extra router I/O. Fields are projected BY NAME rather than copied wholesale,
// so nothing from the interface payload — addresses, MAC lists, error counters —
// reaches a page whose permission is `bridges` rather than `interfaces`.
//
// THE HOST TABLE IS CAPPED. 64 entries on the router this was written against,
// but a switch with a few hundred clients returns thousands, every poll, over
// the socket. The cap is applied here and the true total travels with it, so the
// page can say "showing 500 of 2431" rather than quietly lying.

import (
	"encoding/json"
	"sort"
	"time"

	"mikrodash/internal/routeros"
)

var (
	bridgeCmd = routeros.Cmd{Path: "/interface/bridge/print", Args: []string{
		"=.proplist=.id,name,protocol-mode,vlan-filtering,igmp-snooping,dhcp-snooping," +
			"fast-forward,priority,ageing-time,mac-address,actual-mtu,mtu,running,disabled,comment"}}
	bridgePortCmd = routeros.Cmd{Path: "/interface/bridge/port/print", Args: []string{
		"=.proplist=.id,bridge,interface,pvid,role,edge,learn,horizon,path-cost," +
			"frame-types,disabled,inactive,dynamic"}}
	bridgeHostCmd = routeros.Cmd{Path: "/interface/bridge/host/print", Args: []string{
		"=.proplist=mac-address,on-interface,bridge,vid,dynamic,local,external,age"}}
)

const (
	bridgeHostCap = 500
	// The safety net: config is re-read every twelfth tick even when nothing
	// has said it changed, so a missed change cannot strand the page on stale
	// configuration.
	bridgeConfigEvery = 12
)

// Rate is one interface's throughput, as interfaceStatus reports it.
type Rate struct {
	RxMbps *float64
	TxMbps *float64
}

// RateSource supplies borrowed throughput.
//
// The bool is not redundant with an empty map. "interfaceStatus has not reported
// yet" and "it reported and this bridge is idle" must stay tellable apart — the
// page renders the first as an em dash and the second as 0.00, and conflating
// them makes a collector that has been switched off look like a quiet network.
type RateSource interface {
	Rates() (map[string]Rate, bool)
}

// Bridge is one row of the bridges table.
type Bridge struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ProtocolMode  string   `json:"protocolMode"`
	VlanFiltering bool     `json:"vlanFiltering"`
	IgmpSnooping  bool     `json:"igmpSnooping"`
	DhcpSnooping  bool     `json:"dhcpSnooping"`
	FastForward   bool     `json:"fastForward"`
	Priority      string   `json:"priority"`
	AgeingTime    string   `json:"ageingTime"`
	MacAddress    string   `json:"macAddress"`
	MTU           *float64 `json:"mtu"`
	Running       bool     `json:"running"`
	Disabled      bool     `json:"disabled"`
	Comment       string   `json:"comment"`
	PortCount     int      `json:"portCount"`
	// null, never 0 — see RateSource.
	RxMbps *float64 `json:"rxMbps"`
	TxMbps *float64 `json:"txMbps"`
}

// BridgePort is one row of the ports table.
type BridgePort struct {
	ID        string   `json:"id"`
	Bridge    string   `json:"bridge"`
	Interface string   `json:"interface"`
	Pvid      *float64 `json:"pvid"`
	// Absent on a bridge running protocol-mode=none: there are no STP roles to
	// report, which is not the same as a port with an unknown role.
	Role       string   `json:"role"`
	Edge       string   `json:"edge"`
	Learn      string   `json:"learn"`
	Horizon    string   `json:"horizon"`
	PathCost   *float64 `json:"pathCost"`
	FrameTypes string   `json:"frameTypes"`
	Disabled   bool     `json:"disabled"`
	Inactive   bool     `json:"inactive"`
	Dynamic    bool     `json:"dynamic"`
}

// BridgeHost is one learned MAC.
type BridgeHost struct {
	Mac         string   `json:"mac"`
	OnInterface string   `json:"onInterface"`
	Bridge      string   `json:"bridge"`
	Vid         *float64 `json:"vid"`
	Dynamic     bool     `json:"dynamic"`
	Local       bool     `json:"local"`
	External    bool     `json:"external"`
	Age         string   `json:"age"`
}

// BridgesPayload is the bridges:update body. Field order matches the Node
// object literal so the emitted JSON reads the same.
type BridgesPayload struct {
	TS             int64        `json:"ts"`
	PollMs         int          `json:"pollMs"`
	Bridges        []Bridge     `json:"bridges"`
	Ports          []BridgePort `json:"ports"`
	Hosts          []BridgeHost `json:"hosts"`
	HostTotal      int          `json:"hostTotal"`
	HostCap        int          `json:"hostCap"`
	RatesAvailable bool         `json:"ratesAvailable"`
	// So the page can say "this router has no bridges" rather than showing an
	// empty table, which reads as a failure.
	Available      bool `json:"available"`
	HostsAvailable bool `json:"hostsAvailable"`
}

type bridgeBuilt struct {
	bridges        []Bridge
	ports          []BridgePort
	hosts          []BridgeHost
	hostTotal      int
	ratesAvailable bool
}

// BuildBridgeRows joins the three tables into one view.
func BuildBridgeRows(bridgeRows, portRows, hostRows []routeros.Reply, rates RateSource) bridgeBuilt {
	var byName map[string]Rate
	ratesAvailable := false
	if rates != nil {
		byName, ratesAvailable = rates.Rates()
	}

	ports := []BridgePort{}
	for _, r := range portRows {
		if r["interface"] == "" {
			continue // also drops the {undefined:''} row RouterOS can send
		}
		ports = append(ports, BridgePort{
			ID: r[".id"], Bridge: r["bridge"], Interface: r["interface"],
			Pvid: numOf(r, "pvid"), Role: r["role"], Edge: r["edge"],
			Learn: r["learn"], Horizon: r["horizon"], PathCost: numOf(r, "path-cost"),
			FrameTypes: r["frame-types"], Disabled: boolOf(r["disabled"]),
			Inactive: boolOf(r["inactive"]), Dynamic: boolOf(r["dynamic"]),
		})
	}

	portsByBridge := map[string]int{}
	for _, p := range ports {
		if p.Bridge != "" {
			portsByBridge[p.Bridge]++
		}
	}

	bridges := []Bridge{}
	for _, r := range bridgeRows {
		if r["name"] == "" {
			continue
		}
		// actual-mtu is what the interface negotiated; mtu is what it was
		// configured with. The first is the truth when both are present.
		mtu := numOf(r, "actual-mtu")
		if mtu == nil {
			mtu = numOf(r, "mtu")
		}
		b := Bridge{
			ID: r[".id"], Name: r["name"], ProtocolMode: r["protocol-mode"],
			VlanFiltering: boolOf(r["vlan-filtering"]), IgmpSnooping: boolOf(r["igmp-snooping"]),
			DhcpSnooping: boolOf(r["dhcp-snooping"]), FastForward: boolOf(r["fast-forward"]),
			Priority: r["priority"], AgeingTime: r["ageing-time"], MacAddress: r["mac-address"],
			MTU: mtu, Running: boolOf(r["running"]), Disabled: boolOf(r["disabled"]),
			Comment: r["comment"], PortCount: portsByBridge[r["name"]],
		}
		if live, ok := byName[b.Name]; ok {
			b.RxMbps, b.TxMbps = live.RxMbps, live.TxMbps
		}
		bridges = append(bridges, b)
	}
	sort.SliceStable(bridges, func(i, j int) bool {
		return Collate(bridges[i].Name, bridges[j].Name) < 0
	})

	all := []BridgeHost{}
	for _, r := range hostRows {
		if r["mac-address"] == "" {
			continue
		}
		all = append(all, BridgeHost{
			Mac: r["mac-address"], OnInterface: r["on-interface"], Bridge: r["bridge"],
			Vid: numOf(r, "vid"), Dynamic: boolOf(r["dynamic"]),
			Local: boolOf(r["local"]), External: boolOf(r["external"]), Age: r["age"],
		})
	}
	// Learned entries first: a table truncated at the cap should drop the
	// router's own port MACs before it drops a client somebody is looking for.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Local == all[j].Local {
			return Collate(all[i].Mac, all[j].Mac) < 0
		}
		return !all[i].Local
	})

	hosts := all
	if len(hosts) > bridgeHostCap {
		hosts = hosts[:bridgeHostCap]
	}
	return bridgeBuilt{bridges, ports, hosts, len(all), ratesAvailable}
}

// Bridges is the collector.
type Bridges struct {
	tableCore[BridgesPayload]
	emit  Emit
	rates RateSource

	// cfgB and cfgP are the bridge and port config: the slow lane, carried
	// between readings.
	cfgB []routeros.Reply
	cfgP []routeros.Reply
	// nil = unprobed, false = this router has no such menu, stop asking.
	bridgeAvailable *bool
	portAvailable   *bool
	hostAvailable   *bool
}

// bridgesHeartbeat is how long an unchanged `bridges:update` may be suppressed.
// See packagesHeartbeat.
const bridgesHeartbeat = 10 * time.Second

// NewBridges builds the collector. `rates` may be nil — the page degrades to no
// throughput column rather than not rendering, which is the same judgement
// src/collection.js makes by declaring no `requires` for this collector.
func NewBridges(ros Reader, emit Emit, rates RateSource, pollMs int) *Bridges {
	b := &Bridges{emit: emit, rates: rates}
	// Subscribed to the host table, the live menu: a MAC is learned or ages out
	// with no configuration change behind it. The bridge and port config are the
	// slow lane, and a write's RefreshNow re-reads them at once.
	b.setup(b, ros, pollMs, tableSpec{
		cmd: bridgeHostCmd, poll: [3]int{5000, 2000, 60000}, slowEvery: bridgeConfigEvery, heartbeat: bridgesHeartbeat,
	})
	return b
}

// read runs one menu THROUGH THE CACHE. Both of this collector's shared menus
// arrive here: /interface/bridge/port, which vlans also reads, and
// /interface/bridge/host, which topology reads.
func (b *Bridges) read(cmd routeros.Cmd, avail **bool) []routeros.Reply {
	rows, _ := readOptional(b.readShared, cmd, avail)
	return rows
}

// derive is the host table with the bridge and port config.
//
// A REFUSED HOST TABLE IS NOT RETIRED. It is the one menu a read-only API user can
// be denied while the rest still answers, so the page still has bridges and ports
// to show, and the slow lane must keep reading them: the payload is built without
// hosts and says so.
func (b *Bridges) derive(rows []routeros.Reply, err error, slow bool) (*BridgesPayload, string) {
	if err != nil && !menuGone(err) {
		return nil, ""
	}
	latchMenu(&b.hostAvailable, err)
	if err != nil {
		rows = nil
	}
	if slow {
		b.cfgB = b.read(bridgeCmd, &b.bridgeAvailable)
		b.cfgP = b.read(bridgePortCmd, &b.portAvailable)
	}
	return b.build(rows)
}

// applyLocked builds and emits. The caller holds the lock.
func (b *Bridges) build(hostRows []routeros.Reply) (*BridgesPayload, string) {
	built := BuildBridgeRows(b.cfgB, b.cfgP, hostRows, b.rates)
	payload := &BridgesPayload{
		TS: time.Now().UnixMilli(), PollMs: b.pollMs.ms(),
		Bridges: built.bridges, Ports: built.ports, Hosts: built.hosts,
		HostTotal: built.hostTotal, HostCap: bridgeHostCap,
		RatesAvailable: built.ratesAvailable,
		Available:      MenuAvailable(b.bridgeAvailable),
		HostsAvailable: MenuAvailable(b.hostAvailable),
	}
	// The whole rows, not a hand-picked tuple. The Node side learned this on the
	// DNS collector: a tuple omits a field the page renders, an edit to that
	// field recomputes an identical fingerprint, and the open page never hears
	// about a change that really landed. hostTotal rather than the hosts
	// themselves — the MAC table churns constantly and fingerprinting it would
	// emit every tick, which is what the fingerprint exists to prevent.
	fp, _ := json.Marshal(struct {
		B []Bridge     `json:"b"`
		P []BridgePort `json:"p"`
		H int          `json:"h"`
	}{built.bridges, built.ports, built.hostTotal})
	return payload, string(fp)
}

func (b *Bridges) send(p BridgesPayload) { EvBridgesUpdate.Emit(b.emit, bridgesRooms.Join(), p) }

// reset drops every latch.
func (b *Bridges) reset() { b.bridgeAvailable, b.portAvailable, b.hostAvailable = nil, nil, nil }
