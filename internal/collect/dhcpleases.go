package collect

// DHCP leases — the port of src/collectors/dhcpLeases.js.
//
//	/ip/dhcp-server/print         each server and the interface it serves
//	/interface/vlan/print         that interface's VLAN id, when it is a VLAN
//	/ip/dhcp-server/lease/print   the leases themselves
//
// A lease carries only its SERVER NAME, so the interface and the VLAN behind it
// have to be joined from the server config. Both tables change rarely, so they
// are re-read with the leases rather than polled separately — and reading them
// FIRST matters: the first lease applied must already be able to resolve them,
// or the payload carries leases with no interface until something forces a
// re-read.
//
// ── INSERTION ORDER IS PART OF THE PAYLOAD ───────────────────────────────────
//
// The Node collector keeps leases in a `Map` keyed by IP and emits
// `[...byIP.entries()]`, so the array is in the order the router first mentioned
// each address. A Go map has no order at all, and ranging one produces a
// different array on every run — which the golden would catch, but as an
// unstable diff that reads like flakiness rather than like a bug. The order is
// therefore kept explicitly.
//
// The two JavaScript details that go with it, both reproduced:
//
//   - re-setting an existing key does NOT move it to the end, so a lease that
//     changes state keeps its position;
//   - deleting and re-adding DOES move it to the end, because it is a new key.
//
// ── STREAMING IS NOT PORTED YET ──────────────────────────────────────────────
//
// The live collector prefers `/ip/dhcp-server/lease/listen` and falls back to
// polling (issue #105 made that a setting). This side polls only. The parsing is
// the same code either way — `_applyLease` there, applyLease here — so adding
// the stream later changes delivery and not the payload. Recorded here
// rather than left to be discovered.

import (
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

var (
	dhcpServersCmd = routeros.Cmd{Path: "/ip/dhcp-server/print",
		Args: []string{"=.proplist=name,interface"}}
	dhcpVlansCmd = routeros.Cmd{Path: "/interface/vlan/print",
		Args: []string{"=.proplist=name,vlan-id"}}
	dhcpLeasesCmd = routeros.Cmd{Path: "/ip/dhcp-server/lease/print",
		Args: []string{"=.proplist=.id,.dead,address,active-address,mac-address," +
			"active-mac-address,status,comment,host-name,server,dynamic"}}
)

// Lease is one row as the page renders it. FIELD ORDER IS THE EMITTED KEY ORDER
// and matches `{ ip, ...v }` in _emitLeases, so the JSON reads the same as the
// Node payload the golden captured.
type Lease struct {
	IP       string `json:"ip"`
	Name     string `json:"name"`
	MAC      string `json:"mac"`
	HostName string `json:"hostName"`
	Comment  string `json:"comment"`
	Status   string `json:"status"`
	Server   string `json:"server"`
	Iface    string `json:"iface"`
	VlanID   string `json:"vlanId"`
	// ID lets the page open a lease in the edit form; Dynamic is what tells a
	// reservation from a lease the server handed out, which is the difference
	// between an editable row and one that only offers "make static".
	ID      string `json:"id"`
	Dynamic bool   `json:"dynamic"`
}

// LeaseServer is one entry in the filter above the table.
type LeaseServer struct {
	Name   string `json:"name"`
	Iface  string `json:"iface"`
	VlanID string `json:"vlanId"`
	Count  int    `json:"count"`
}

type LeasesPayload struct {
	TS      int64         `json:"ts"`
	Leases  []Lease       `json:"leases"`
	Servers []LeaseServer `json:"servers"`
}

type serverMeta struct{ iface, vlanID string }

// DHCPLeases is the collector.
type DHCPLeases struct {
	ros  Reader
	emit Emit
	poll *pollLoop

	mu sync.Mutex
	// order is the IPs in the order first seen; byIP is the lease behind each.
	// Together they are the JavaScript Map this payload depends on.
	order  []string
	byIP   map[string]Lease
	byMAC  map[string]string // mac → ip, for the name lookups other pages make
	server map[string]serverMeta
	last   *LeasesPayload
}

func NewDHCPLeases(ros Reader, emit Emit, pollMs int) *DHCPLeases {
	d := &DHCPLeases{
		ros: ros, emit: emit,
		byIP: map[string]Lease{}, byMAC: map[string]string{},
		server: map[string]serverMeta{},
	}
	// The bounds src/collectors/dhcpLeases.js applies: a ten-minute default,
	// because this is a table of configuration rather than a live gauge.
	ms := clampPoll(pollMs, 600000, 500, 600000)
	d.poll = newPollLoop(func() { d.RefreshNow() },
		func() time.Duration { return time.Duration(ms) * time.Millisecond })
	return d
}

// loadServerMap resolves each DHCP server to its interface, and that interface
// to a VLAN id when it is a VLAN. A server on a plain ether interface simply has
// no vlanId.
//
// FAILURE HERE IS NOT FATAL. Leases still carry their server name, so the filter
// degrades to server-only rather than disappearing — which is why this warns and
// returns instead of failing the read.
func (d *DHCPLeases) loadServerMap() {
	servers, err := d.ros.Do(dhcpServersCmd)
	if err != nil {
		log.Printf("[leases] server/VLAN map unavailable: %v", err)
		return
	}
	vlans, err := d.ros.Do(dhcpVlansCmd)
	if err != nil {
		log.Printf("[leases] server/VLAN map unavailable: %v", err)
		return
	}
	vlanByName := make(map[string]string, len(vlans))
	for _, v := range vlans {
		if v["name"] != "" {
			vlanByName[v["name"]] = v["vlan-id"]
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	clear(d.server)
	for _, s := range servers {
		if s["name"] == "" {
			continue
		}
		iface := s["interface"]
		d.server[s["name"]] = serverMeta{iface: iface, vlanID: vlanByName[iface]}
	}
}

// applyLease folds one row in. The caller holds the lock.
func (d *DHCPLeases) applyLease(l routeros.Reply) {
	ip := l["address"]
	if ip == "" {
		ip = l["active-address"]
	}
	mac := l["mac-address"]
	if mac == "" {
		mac = l["active-mac-address"]
	}
	if mac == "" {
		mac = l["mac"]
	}
	status := l["status"]

	// Prune expired and removed leases so the maps do not grow without bound on
	// a long-running instance. These arrive as `.dead` OR as a status, and both
	// spellings are handled because both occur.
	if l[".dead"] == "true" || status == "expired" || status == "removed" {
		d.forget(ip, mac)
		return
	}

	// The comment wins over the host name: an operator who labelled a device
	// meant that label to be what they see.
	name := strings.TrimSpace(l["comment"])
	if name == "" {
		name = strings.TrimSpace(l["host-name"])
	}
	meta := d.server[l["server"]]

	if ip != "" {
		if _, seen := d.byIP[ip]; !seen {
			d.order = append(d.order, ip)
		}
		d.byIP[ip] = Lease{
			IP: ip, Name: name, MAC: mac, HostName: l["host-name"],
			Comment: l["comment"], Status: status, Server: l["server"],
			Iface: meta.iface, VlanID: meta.vlanID,
			ID: l[".id"], Dynamic: boolOf(l["dynamic"]),
		}
	}
	if mac != "" && ip != "" {
		d.byMAC[mac] = ip
	}
}

// forget drops a lease from both maps AND from the order, which is the half a
// plain delete would miss — a stale IP left in `order` emits a zero-valued lease
// for an address the router no longer knows.
func (d *DHCPLeases) forget(ip, mac string) {
	if ip != "" {
		if _, ok := d.byIP[ip]; ok {
			delete(d.byIP, ip)
			for i, v := range d.order {
				if v == ip {
					d.order = append(d.order[:i], d.order[i+1:]...)
					break
				}
			}
		}
	}
	if mac != "" {
		delete(d.byMAC, mac)
	}
}

// serverSummary is the filter list above the table.
//
// Built from the leases actually PRESENT rather than from the server table, so a
// server whose config could not be read — one added since the last connect —
// still appears, just without its interface and VLAN.
//
// sort.SliceStable, not sort.Slice: JavaScript's sort has been stable since
// ES2019, so two servers with the same lease count keep the order they were
// first seen in. An unstable sort would reshuffle the filter between ticks for
// no reason the operator could see.
func (d *DHCPLeases) serverSummary(leases []Lease) []LeaseServer {
	counts := map[string]int{}
	var names []string // first-seen order, which the counts map cannot keep
	for _, l := range leases {
		if l.Server == "" {
			continue
		}
		if _, seen := counts[l.Server]; !seen {
			names = append(names, l.Server)
		}
		counts[l.Server]++
	}
	out := make([]LeaseServer, 0, len(names))
	for _, n := range names {
		meta := d.server[n]
		out = append(out, LeaseServer{
			Name: n, Iface: meta.iface, VlanID: meta.vlanID, Count: counts[n]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// build assembles the payload. The caller holds the lock.
func (d *DHCPLeases) build() *LeasesPayload {
	leases := make([]Lease, 0, len(d.order))
	for _, ip := range d.order {
		if l, ok := d.byIP[ip]; ok {
			leases = append(leases, l)
		}
	}
	return &LeasesPayload{
		TS:      time.Now().UnixMilli(),
		Leases:  leases,
		Servers: d.serverSummary(leases),
	}
}

// RefreshNow re-reads everything and emits.
//
// It is also what a write calls, and rebuilding the server map is the point of
// doing the whole read rather than just the leases: a reservation created on a
// server this process had not seen before needs it.
func (d *DHCPLeases) RefreshNow() {
	if !d.ros.Connected() {
		return
	}
	d.loadServerMap()
	rows, err := d.ros.Do(dhcpLeasesCmd)
	if err != nil {
		log.Printf("[leases] load failed: %v", err)
		return
	}
	d.mu.Lock()
	// A FULL READ REPLACES. It must not merge.
	//
	// `/ip/dhcp-server/lease/print` never carries the `.dead` flag that removals
	// arrive with — that appears only on the listen stream — so a merging re-read
	// prunes nothing, and a lease that vanished during a disconnect stays for
	// good. The table then only ever grows, and a phantom keeps whatever status
	// it last had (usually `bound`), so no downstream filtering can undo it.
	//
	// Reported on a CCR2004: two /23 pools read 509/509 because every address the
	// pool had ever handed out was still in the table.
	//
	// CLEARED AFTER THE READ RETURNS, never before — the early return above
	// leaves the previous table standing rather than blanking it, along with
	// every name lookup hanging off it.
	d.byIP = make(map[string]Lease, len(rows))
	d.byMAC = make(map[string]string, len(rows))
	d.order = d.order[:0]
	for _, l := range rows {
		d.applyLease(l)
	}
	payload := d.build()
	d.last = payload
	d.mu.Unlock()

	// PAGE-SCOPED, where the live app broadcasts to the whole router room.
	// Nothing user-visible turns on it today: the only consumer here is the DHCP
	// page, and a viewer on the Dashboard was served by Node when this was written. When
	// the Dashboard is ported it consumes leases too, and this has to widen.
	// ── ROUTER-WIDE, BECAUSE THE LIVE ONE IS ──────────────────────────────
	//
	// `dhcpLeases.js:83` is `this.io.emit('leases:list', payload)` — every
	// socket, no room. This was scoped to `page-dhcp`, which looks tidier and is
	// wrong: TWO consumers live outside that page.
	//
	//	the dashboard's DHCP Leases card   showed 0 where live showed 42
	//	`web/src/pages/connections.ts`     uses leases to name a device by IP, so
	//	                                   without them every connection renders
	//	                                   as a bare address
	//
	// Measured 2026-08-29 by comparing the two dashboards nine seconds after
	// sign-in, after the operator reported cards with no data.
	d.emit("", "leases:list", payload)
}

// Last is the payload a page focus replays.
func (d *DHCPLeases) Last() *LeasesPayload {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
}

// LeaseIPs is every address currently known, whatever its state. dhcpNetworks
// counts these per subnet.
func (d *DHCPLeases) LeaseIPs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.order))
	copy(out, d.order)
	return out
}

// UsedLeaseIPs is the addresses actually held, for the utilisation arithmetic.
//
// ── A DENY-LIST, AND DELIBERATELY SO ────────────────────────────────────────
//
// An address is in use unless its status is `waiting`. RouterOS documents
// `waiting | testing | declined | offered | bound | authorizing | conflict`, and
// `waiting` alone means a static reservation nobody currently holds. Everything
// else holds the address against other clients: `testing` and `authorizing` are
// mid-allocation, and a `declined` or `conflict` address stays busy for the
// lease time.
//
// If RouterOS gains a status that should count as used, a deny-list over-counts
// by one address; an allow-list would silently drop it out of the total. AN
// EMPTY STATUS COUNTS AS USED for the same reason — it comes from a partial
// stream row, never from an untaken reservation, which reports `waiting`.
//
// This replaces an `ActiveLeaseIPs` that allowed only `bound`/`offered`. It was
// unused, and it is the exact shape the live repo's own notes warn against: it
// under-counts every transient state.
//
// ── THE LEASE TABLE IS A DIFFERENT QUESTION ─────────────────────────────────
//
// `build()` still lists everything, `waiting` included. Only the arithmetic
// filters — a page that hides reservations is worse than one that miscounts
// them.
func (d *DHCPLeases) UsedLeaseIPs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, ip := range d.order {
		if strings.EqualFold(d.byIP[ip].Status, "waiting") {
			continue
		}
		out = append(out, ip)
	}
	return out
}

func (d *DHCPLeases) Start() { d.RefreshNow(); d.poll.start() }

func (d *DHCPLeases) Reconnected() {
	d.poll.stop()
	d.RefreshNow()
	d.poll.start()
}

func (d *DHCPLeases) Suspend() { d.poll.stop() }
func (d *DHCPLeases) Resume()  { d.poll.start() }
func (d *DHCPLeases) Stop()    { d.poll.stop() }
