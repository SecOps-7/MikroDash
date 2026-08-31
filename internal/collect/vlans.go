package collect

// VLANs collector — the port of src/collectors/vlans.js.
//
// Three read-only prints, joined into one view:
//
//	/interface/vlan         the L3 VLAN interfaces — id, parent, mtu, running
//	/interface/bridge/vlan  the trunk table        — tagged / untagged per VLAN
//	/interface/bridge/port  each port's pvid       — its untagged VLAN
//
// RATES AND CLIENT COUNTS ARE BORROWED, NOT FETCHED, the way bridges.go borrows
// them. Deliberately NOT done by adding page-vlans to interfaceStatus's rooms:
// that payload carries IP addresses, MAC addresses and error counters, and would
// hand all of it to anyone holding read on `vlans` — a different permission from
// `interfaces`. The join happens here and only VLAN-shaped rows leave. Fields
// are projected by name for the same reason; copying the interface row wholesale
// would silently re-leak everything.

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

var (
	vlanCmd = routeros.Cmd{Path: "/interface/vlan/print", Args: []string{
		"=.proplist=.id,name,vlan-id,interface,mtu,running,disabled,comment"}}
	bridgeVlanCmd = routeros.Cmd{Path: "/interface/bridge/vlan/print", Args: []string{
		"=.proplist=.id,bridge,vlan-ids,tagged,untagged,current-tagged,dynamic,disabled"}}
	vlanPortCmd = routeros.Cmd{Path: "/interface/bridge/port/print", Args: []string{
		"=.proplist=.id,bridge,interface,pvid,frame-types,disabled"}}
)

const (
	// 802.1Q. 0 is priority-tagged and 4095 is reserved, so neither is a VLAN.
	vlanMin = 1
	vlanMax = 4094
	// A trunk port may legally carry `2-4094`. Expanding that gives 4093 ids
	// from a single row, rebuilt every poll and shipped over the socket. Past
	// this many the range is carried as a tuple instead.
	vlanRangeCap    = 64
	vlanConfigEvery = 12
)

// LeaseCounts supplies DHCP client counts per VLAN.
//
// Keyed by STRING, because that is how dhcpLeases stores a vlanId while every
// id here is a number. Comparing them directly yields 0 for every VLAN, which
// reads as "no DHCP clients" rather than as a bug — the most plausible wrong
// answer this collector could give — so the coercion is explicit below.
type LeaseCounts interface {
	VlanClients() map[string]int
}

// VlanIDs is a parsed `vlan-ids` list. RouterOS accepts "5", "5,10,20" and
// "1,10-12"; a plain integer parse reads the first and silently loses the rest.
type VlanIDs struct {
	IDs    []int
	Ranges [][2]int
	// Raw is kept verbatim because it is the string the operator sees in WinBox,
	// and the page should be able to show what is configured rather than a
	// reconstruction of it.
	Raw       string
	Truncated bool
}

// ParseVlanIDs parses a `vlan-ids` value.
func ParseVlanIDs(raw string) VlanIDs {
	out := VlanIDs{IDs: []int{}, Ranges: [][2]int{}, Raw: raw}
	seen := map[int]bool{}
	for _, part := range strings.Split(raw, ",") {
		t := strings.TrimSpace(part)
		if t == "" {
			continue
		}
		if lo, hi, ok := splitRange(t); ok {
			// A reversed range is not something anyone can enter in WinBox, so
			// guessing at an interpretation is worse than dropping it.
			if !(lo >= vlanMin && hi <= vlanMax && lo <= hi) {
				continue
			}
			out.Ranges = append(out.Ranges, [2]int{lo, hi})
			if hi-lo+1 > vlanRangeCap {
				out.Truncated = true
				continue
			}
			for v := lo; v <= hi; v++ {
				if !seen[v] {
					seen[v] = true
					out.IDs = append(out.IDs, v)
				}
			}
			continue
		}
		v, err := strconv.Atoi(t)
		if err != nil || !allDigits(t) || v < vlanMin || v > vlanMax {
			continue
		}
		if !seen[v] {
			seen[v] = true
			out.IDs = append(out.IDs, v)
		}
	}
	sort.Ints(out.IDs)
	return out
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// splitRange matches `\d+\s*-\s*\d+`.
func splitRange(s string) (int, int, bool) {
	i := strings.IndexByte(s, '-')
	if i < 0 {
		return 0, 0, false
	}
	a, b := strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	if !allDigits(a) || !allDigits(b) {
		return 0, 0, false
	}
	lo, err1 := strconv.Atoi(a)
	hi, err2 := strconv.Atoi(b)
	return lo, hi, err1 == nil && err2 == nil
}

// VlanInterface is one L3 VLAN interface.
type VlanInterface struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Parent   string   `json:"parent"`
	MTU      *float64 `json:"mtu"`
	Running  bool     `json:"running"`
	Disabled bool     `json:"disabled"`
	Comment  string   `json:"comment"`
	// null, never 0: "the router did not report this" and "this VLAN is idle"
	// must stay tellable apart, or the page confidently shows 0 Mbps on a busy
	// VLAN during the startup window.
	RxMbps *float64 `json:"rxMbps"`
	TxMbps *float64 `json:"txMbps"`
}

// Vlan is one VLAN, joined from all three tables.
type Vlan struct {
	VlanID int `json:"vlanId"`
	// An array because two rows may share one vlan-id on different parents, so
	// the rates are never collapsed into one interface's number.
	Interfaces []VlanInterface `json:"interfaces"`
	Tagged     []string        `json:"tagged"`
	Untagged   []string        `json:"untagged"`
	Bridges    []string        `json:"bridges"`
	Clients    int             `json:"clients"`
	RxMbps     *float64        `json:"rxMbps"`
	TxMbps     *float64        `json:"txMbps"`
	Name       string          `json:"name"`
}

// BridgeVlan is one row of the bridge VLAN (trunk) table.
type BridgeVlan struct {
	Bridge    string   `json:"bridge"`
	Raw       string   `json:"raw"`
	IDs       []int    `json:"ids"`
	Ranges    [][]int  `json:"ranges"`
	Truncated bool     `json:"truncated"`
	Tagged    []string `json:"tagged"`
	Untagged  []string `json:"untagged"`
	// CurrentTagged is the router's view including dynamically added ports;
	// `tagged` is only what was configured.
	CurrentTagged []string `json:"currentTagged"`
	Dynamic       bool     `json:"dynamic"`
	Disabled      bool     `json:"disabled"`
}

// VlanPort is one bridge port, for its pvid.
type VlanPort struct {
	Bridge     string   `json:"bridge"`
	Interface  string   `json:"interface"`
	Pvid       *float64 `json:"pvid"`
	FrameTypes string   `json:"frameTypes"`
	Disabled   bool     `json:"disabled"`
}

// VlansPayload is the vlans:update body.
type VlansPayload struct {
	TS             int64        `json:"ts"`
	PollMs         int          `json:"pollMs"`
	Vlans          []Vlan       `json:"vlans"`
	BridgeVlans    []BridgeVlan `json:"bridgeVlans"`
	Ports          []VlanPort   `json:"ports"`
	DynamicCount   int          `json:"dynamicCount"`
	RatesAvailable bool         `json:"ratesAvailable"`
}

func splitCSV(v string) []string {
	out := []string{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// BuildVlanRows joins the three tables. Pure and fully injected, so it can be
// tested without a router or a collector.
func BuildVlanRows(vlanRows, bridgeVlanRows, portRows []routeros.Reply,
	rates RateSource, leases LeaseCounts) VlansPayload {

	var byName map[string]Rate
	ratesAvailable := false
	if rates != nil {
		byName, ratesAvailable = rates.Rates()
	}

	byID := map[int]*Vlan{}
	var order []int
	vlan := func(id int) *Vlan {
		if v, ok := byID[id]; ok {
			return v
		}
		v := &Vlan{VlanID: id, Interfaces: []VlanInterface{},
			Tagged: []string{}, Untagged: []string{}, Bridges: []string{}}
		byID[id] = v
		order = append(order, id)
		return v
	}

	// 1. L3 VLAN interfaces.
	for _, r := range vlanRows {
		if r["name"] == "" {
			continue // also drops the {undefined:''} row
		}
		id, ok := jsNumber(r, "vlan-id")
		if !ok {
			continue
		}
		e := vlan(int(id))
		// `r.mtu ? Number(r.mtu) : null` — NOT the usual coercion: an absent or
		// empty mtu is null, but the string "0" is truthy in JavaScript and
		// becomes 0.
		var mtu *float64
		if raw, present := r["mtu"]; present && raw != "" {
			if f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
				mtu = &f
			}
		}
		vi := VlanInterface{
			ID: r[".id"], Name: r["name"], Parent: r["interface"], MTU: mtu,
			Running: r["running"] == "true", Disabled: r["disabled"] == "true",
			Comment: r["comment"],
		}
		if live, ok := byName[vi.Name]; ok {
			vi.RxMbps, vi.TxMbps = live.RxMbps, live.TxMbps
		}
		e.Interfaces = append(e.Interfaces, vi)
	}

	// 2. Bridge VLAN table. Dynamic rows are kept in the JOIN — on a real router
	//    most membership comes from them — and hidden only at render time.
	bridgeVlans := []BridgeVlan{}
	for _, r := range bridgeVlanRows {
		raw, present := r["vlan-ids"]
		if !present {
			continue
		}
		p := ParseVlanIDs(raw)
		ranges := [][]int{}
		for _, rg := range p.Ranges {
			ranges = append(ranges, []int{rg[0], rg[1]})
		}
		row := BridgeVlan{
			Bridge: r["bridge"], Raw: p.Raw, IDs: p.IDs, Ranges: ranges, Truncated: p.Truncated,
			Tagged: splitCSV(r["tagged"]), Untagged: splitCSV(r["untagged"]),
			CurrentTagged: splitCSV(r["current-tagged"]),
			Dynamic:       r["dynamic"] == "true", Disabled: r["disabled"] == "true",
		}
		bridgeVlans = append(bridgeVlans, row)
		for _, id := range p.IDs {
			e := vlan(id)
			for _, t := range row.Tagged {
				if !contains(e.Tagged, t) {
					e.Tagged = append(e.Tagged, t)
				}
			}
			for _, u := range row.Untagged {
				if !contains(e.Untagged, u) {
					e.Untagged = append(e.Untagged, u)
				}
			}
			if row.Bridge != "" && !contains(e.Bridges, row.Bridge) {
				e.Bridges = append(e.Bridges, row.Bridge)
			}
		}
	}

	// 3. Bridge ports. pvid is the port's untagged VLAN — this is what puts a
	//    WiFi virtual AP on a VLAN, and it is the only source for a VLAN that
	//    exists purely at layer 2 with no /interface/vlan row.
	ports := []VlanPort{}
	for _, r := range portRows {
		if r["interface"] == "" {
			continue
		}
		row := VlanPort{Bridge: r["bridge"], Interface: r["interface"],
			Pvid: numOf(r, "pvid"), FrameTypes: r["frame-types"],
			Disabled: r["disabled"] == "true"}
		ports = append(ports, row)
		if row.Pvid != nil && *row.Pvid >= vlanMin && *row.Pvid <= vlanMax {
			e := vlan(int(*row.Pvid))
			if !contains(e.Untagged, row.Interface) {
				e.Untagged = append(e.Untagged, row.Interface)
			}
		}
	}

	// 4. Client counts, with both sides coerced — see LeaseCounts.
	if leases != nil {
		for k, n := range leases.VlanClients() {
			if id, err := strconv.Atoi(strings.TrimSpace(k)); err == nil {
				if e, ok := byID[id]; ok {
					e.Clients = n
				}
			}
		}
	}

	// 5. Roll the interface rates up per VLAN, still null when nothing reported.
	for _, id := range order {
		e := byID[id]
		var rx, tx *float64
		var rxN, txN float64
		var haveRx, haveTx bool
		for _, i := range e.Interfaces {
			if i.RxMbps != nil {
				rxN += *i.RxMbps
				haveRx = true
			}
			if i.TxMbps != nil {
				txN += *i.TxMbps
				haveTx = true
			}
		}
		if haveRx {
			rx = &rxN
		}
		if haveTx {
			tx = &txN
		}
		e.RxMbps, e.TxMbps = rx, tx

		names := make([]string, 0, len(e.Interfaces))
		for _, i := range e.Interfaces {
			names = append(names, i.Name)
		}
		e.Name = strings.Join(names, ", ")
		// Plain byte order, NOT Collate: the Node side uses Array.sort() with no
		// comparator here, which sorts by UTF-16 code unit rather than by
		// locale. Using the collator would reorder these and the DNS table's
		// rows differently from the live app — the opposite mistake to the one
		// Collate exists to prevent.
		sort.Strings(e.Tagged)
		sort.Strings(e.Untagged)
	}

	vlans := make([]Vlan, 0, len(order))
	for _, id := range order {
		vlans = append(vlans, *byID[id])
	}
	sort.SliceStable(vlans, func(i, j int) bool { return vlans[i].VlanID < vlans[j].VlanID })

	dynamic := 0
	for _, r := range bridgeVlans {
		if r.Dynamic {
			dynamic++
		}
	}
	return VlansPayload{Vlans: vlans, BridgeVlans: bridgeVlans, Ports: ports,
		DynamicCount: dynamic, RatesAvailable: ratesAvailable}
}

// jsNumber reproduces `Number(row[key])` followed by Number.isFinite: an absent
// key is NaN and rejected, but an EMPTY string is 0 and accepted.
func jsNumber(row routeros.Reply, key string) (float64, bool) {
	raw, present := row[key]
	if !present {
		return 0, false
	}
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Vlans is the collector.
type Vlans struct {
	ros    Reader
	emit   Emit
	rates  RateSource
	leases LeaseCounts
	pollMs *pollInterval

	poll *pollLoop

	mu     sync.Mutex
	cfgV   []routeros.Reply
	cfgBV  []routeros.Reply
	cfgP   []routeros.Reply
	dirty  bool
	ticks  int
	lastFp string

	last    *VlansPayload
	lastErr string
}

// NewVlans builds the collector. `rates` and `leases` may be nil — the page
// degrades to no throughput and no client counts rather than not rendering,
// which is why src/collection.js declares no `requires` for this collector.
func NewVlans(ros Reader, emit Emit, rates RateSource, leases LeaseCounts, pollMs int) *Vlans {
	v := &Vlans{ros: ros, emit: emit, rates: rates, leases: leases,
		pollMs: newPollInterval(clampPoll(pollMs, 5000, 2000, 60000)), dirty: true}
	v.poll = newPollLoop(func() { v.Tick() }, func() time.Duration {
		return v.pollMs.duration()
	})
	return v
}

func (v *Vlans) read(cmd routeros.Cmd) []routeros.Reply {
	rows, err := v.ros.Do(cmd)
	if err != nil {
		if !menuMissing(err) {
			v.lastErr = err.Error()
		}
		return nil
	}
	out := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		if len(r) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// Tick reads what is due this cycle and emits when something changed.
func (v *Vlans) Tick() {
	if !v.ros.Connected() {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	// VLAN topology changes when somebody edits the router, not every five
	// seconds; the rate half costs no router I/O at all because it reads
	// interfaceStatus from memory.
	if v.dirty || v.ticks%vlanConfigEvery == 0 {
		v.cfgV = v.read(vlanCmd)
		v.cfgBV = v.read(bridgeVlanCmd)
		v.cfgP = v.read(vlanPortCmd)
		v.dirty = false
	}
	v.ticks++

	built := BuildVlanRows(v.cfgV, v.cfgBV, v.cfgP, v.rates, v.leases)
	built.TS = time.Now().UnixMilli()
	built.PollMs = v.pollMs.ms()
	v.last = &built

	fp, _ := json.Marshal(struct {
		V []Vlan       `json:"v"`
		B []BridgeVlan `json:"b"`
		P []VlanPort   `json:"p"`
	}{built.Vlans, built.BridgeVlans, built.Ports})
	if string(fp) == v.lastFp {
		return
	}
	v.lastFp = string(fp)
	v.emit("page-vlans", "vlans:update", &built)
}

// Last is the most recent payload, replayed on page:focus.
func (v *Vlans) Last() *VlansPayload {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.last
}

// RefreshNow re-reads immediately, after a write.
func (v *Vlans) RefreshNow() {
	if !v.ros.Connected() {
		return
	}
	v.mu.Lock()
	v.dirty = true
	v.mu.Unlock()
	v.Tick()
}

func (v *Vlans) Start() {
	if v.ros.Connected() {
		v.Tick()
	}
	v.poll.start()
}

func (v *Vlans) Reconnected() {
	v.poll.stop()
	v.mu.Lock()
	v.lastFp = ""
	v.dirty = true
	v.ticks = 0
	v.mu.Unlock()
	v.Tick()
	v.poll.start()
}

func (v *Vlans) Suspend() { v.poll.stop() }

func (v *Vlans) Resume() {
	if v.ros.Connected() {
		v.poll.start()
	}
}

func (v *Vlans) Stop() {
	v.poll.stop()
	v.mu.Lock()
	v.lastFp = ""
	v.mu.Unlock()
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (v *Vlans) SetPollMs(ms int) {
	v.pollMs.set(ms)
	v.poll.retime()
}
