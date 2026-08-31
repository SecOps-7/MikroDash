package collect

// DHCP networks — the port of src/collectors/dhcpNetworks.js.
//
//	/ip/dhcp-server/network/print           the subnets, their gateway and DNS
//	/ip/address/print                       which address sits on which interface
//	/ip/pool/print                          the ranges each pool hands out
//	/interface/detect-internet/state/print  which interfaces reach the internet
//
// It emits `lan:overview`, which the DHCP page's subnet table and the
// dashboard's Network card both render.
//
// ── POOLS ARE MATCHED TO SUBNETS BY ADDRESS, NOT BY CONFIGURATION ────────────
//
// A pool is joined to a subnet by asking whether its FIRST address falls inside
// that subnet's CIDR. The Node comment says why: it is more reliable than
// chasing the server → interface → address chain, which breaks whenever a
// server names an interface carrying more than one address. Reproduced as-is,
// including that a pool spanning two subnets counts against the one its first
// address lands in.
//
// ── THE 32-BIT FOLD IS DELIBERATE ────────────────────────────────────────────
//
// poolRangeSize turns an address into a number with
// `bytes.reduce((acc, b) => (acc << 8) + b, 0) >>> 0`. In JavaScript `<<` works
// on int32 and `>>> 0` reads the result back as uint32, so the whole fold is
// arithmetic modulo 2^32 — which is exactly what Go's uint32 does, for any
// length of input. Writing it as uint32 therefore reproduces the original
// INCLUDING its overflow, rather than reproducing what it was probably meant to
// do. That matters for an IPv6 range, where both sides fold sixteen bytes into
// thirty-two bits and get the same wrong answer; a "corrected" Go version would
// disagree with the payload the page has always been given.

import (
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

var (
	dhcpNetCmd = routeros.Cmd{Path: "/ip/dhcp-server/network/print",
		Args: []string{"=.proplist=address,gateway,dns-server"}}
	dhcpAddrCmd = routeros.Cmd{Path: "/ip/address/print",
		Args: []string{"=.proplist=address,interface,disabled"}}
	dhcpPoolCmd = routeros.Cmd{Path: "/ip/pool/print",
		Args: []string{"=.proplist=name,ranges"}}
	dhcpDetectCmd = routeros.Cmd{Path: "/interface/detect-internet/state/print",
		Args: []string{"=.proplist=name,interface,state"}}
)

// LeaseIPs is the slice of dhcpLeases this collector needs: every address the
// router holds a lease for, whatever its state. Nil is allowed and means the
// lease counts render as zero — the same degradation vlans takes, for the same
// reason.
type LeaseIPs interface {
	// LeaseIPs is every row, for anything that needs the whole table.
	LeaseIPs() []string
	// UsedLeaseIPs is the addresses actually HELD — everything except a
	// `waiting` reservation. The utilisation arithmetic uses this one; see the
	// helper's own header for why it is a deny-list.
	UsedLeaseIPs() []string
}

// Network is one subnet as the page renders it.
type Network struct {
	CIDR       string `json:"cidr"`
	Gateway    string `json:"gateway"`
	DNS        string `json:"dns"`
	LeaseCount int    `json:"leaseCount"`
	PoolSize   int    `json:"poolSize"`
}

// InternetIface is an interface detect-internet reports as reaching the internet.
type InternetIface struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// LanPayload is `lan:overview`. Field order is the emitted key order.
type LanPayload struct {
	TS            int64           `json:"ts"`
	LanCidrs      []string        `json:"lanCidrs"`
	Networks      []Network       `json:"networks"`
	WanIP         string          `json:"wanIp"`
	TotalPoolSize int             `json:"totalPoolSize"`
	TotalLeases   int             `json:"totalLeases"`
	PollMs        int             `json:"pollMs"`
	InternetIface []InternetIface `json:"internetIfaces"`
}

type DHCPNetworks struct {
	ros      Reader
	emit     Emit
	poll     *pollLoop
	leases   LeaseIPs
	wanIface string
	pollMs   *pollInterval

	mu       sync.Mutex
	lanCidrs []string
	last     *LanPayload
	// lastFP gates the emit: the four tables are re-read on a timer and almost
	// never change, so an unchanged payload is not sent at all.
	lastFP string
}

// NewDHCPNetworks builds the collector. wanIface names the interface whose
// address is reported as the WAN IP; empty falls back to "WAN1", as index.js
// does when a router record names none.
func NewDHCPNetworks(ros Reader, emit Emit, leases LeaseIPs, wanIface string, pollMs int) *DHCPNetworks {
	if wanIface == "" {
		wanIface = "WAN1"
	}
	ms := clampPoll(pollMs, 30000, 500, 600000)
	d := &DHCPNetworks{ros: ros, emit: emit, leases: leases, wanIface: wanIface, pollMs: newPollInterval(ms)}
	d.poll = newPollLoop(func() { d.Tick() },
		func() time.Duration { return time.Duration(ms) * time.Millisecond })
	return d
}

// ipInCIDR is ipaddr.js `parse(ip).match(parseCIDR(cidr))`.
//
// Both sides answer false rather than raising when the families differ — the
// Node version because the throw is caught, this one because Contains says so.
func ipInCIDR(ip, cidr string) bool {
	addr := net.ParseIP(strings.TrimSpace(ip))
	if addr == nil {
		return false
	}
	_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return false
	}
	return network.Contains(addr)
}

// firstIPOfRange takes the first address of the first range in a RouterOS ranges
// string — "198.51.100.100-198.51.100.200,198.51.100.240".
func firstIPOfRange(ranges string) string {
	if ranges == "" {
		return ""
	}
	first := strings.TrimSpace(strings.Split(ranges, ",")[0])
	if i := strings.Index(first, "-"); i >= 0 {
		first = strings.TrimSpace(first[:i])
	}
	if net.ParseIP(first) == nil {
		return ""
	}
	return first
}

// ipToU32 is the JavaScript fold described in the package comment.
func ipToU32(ip net.IP) uint32 {
	var acc uint32
	for _, b := range ip {
		acc = acc<<8 + uint32(b)
	}
	return acc
}

// poolRangeSize counts the addresses across every range in the string.
//
// A part with no dash counts as ONE address, and a malformed part is skipped
// rather than failing the whole pool — both straight from the original, and both
// the difference between a pool that reads slightly wrong and a page showing no
// pool at all.
func poolRangeSize(ranges string) int {
	if ranges == "" {
		return 0
	}
	total := 0
	for _, part := range strings.Split(ranges, ",") {
		part = strings.TrimSpace(part)
		dash := strings.LastIndex(part, "-")
		if dash < 0 {
			total++
			continue
		}
		from := net.ParseIP(strings.TrimSpace(part[:dash]))
		to := net.ParseIP(strings.TrimSpace(part[dash+1:]))
		if from == nil || to == nil {
			continue
		}
		fromN, toN := ipToU32(from), ipToU32(to)
		if toN >= fromN {
			total += int(toN - fromN + 1)
		}
	}
	return total
}

// read fetches one table, answering with nothing on failure.
//
// Promise.allSettled on the Node side: a table that cannot be read leaves its
// slice empty and the rebuild carries on, because three tables out of four still
// describe most of the page.
func (d *DHCPNetworks) read(cmd routeros.Cmd) []routeros.Reply {
	rows, err := d.ros.Do(cmd)
	if err != nil {
		log.Printf("[dhcp-networks] %s unavailable: %v", cmd.Path, err)
		return nil
	}
	return rows
}

func (d *DHCPNetworks) Tick() {
	if !d.ros.Connected() {
		return
	}
	netRows := d.read(dhcpNetCmd)
	addrRows := d.read(dhcpAddrCmd)
	poolRows := d.read(dhcpPoolCmd)
	detectRows := d.read(dhcpDetectCmd)

	// An interface reaches the internet if detect-internet says so; its address
	// is the first ENABLED one on that interface, or none.
	internet := make([]InternetIface, 0, len(detectRows))
	for _, r := range detectRows {
		if r["state"] != "internet" {
			continue
		}
		name := r["name"]
		if name == "" {
			name = r["interface"]
		}
		ip := ""
		for _, a := range addrRows {
			if a["interface"] == name && a["disabled"] != "true" {
				ip = a["address"]
				break
			}
		}
		internet = append(internet, InternetIface{Name: name, IP: ip})
	}

	// The WAN address is the first one on the named interface, enabled or not —
	// the original does not filter here, and a disabled WAN address still tells
	// the connections map where it is.
	wanIP := ""
	for _, a := range addrRows {
		if a["interface"] == d.wanIface && a["address"] != "" {
			wanIP = a["address"]
			break
		}
	}

	var leaseIPs []string
	if d.leases != nil {
		// USED addresses, not every row. A `waiting` lease is a static
		// reservation nobody holds, and counting it made a CCR2004's two /23
		// pools read 507 of 512 while ~110 addresses were actually held.
		leaseIPs = d.leases.UsedLeaseIPs()
	}

	var cidrs []string
	networks := make([]Network, 0, len(netRows))
	for _, n := range netRows {
		if n["address"] == "" {
			continue
		}
		cidrs = append(cidrs, n["address"])

		leaseCount := 0
		for _, ip := range leaseIPs {
			if ipInCIDR(ip, n["address"]) {
				leaseCount++
			}
		}
		size := 0
		for _, p := range poolRows {
			if p["ranges"] == "" {
				continue
			}
			if first := firstIPOfRange(p["ranges"]); first != "" && ipInCIDR(first, n["address"]) {
				size += poolRangeSize(p["ranges"])
			}
		}
		dns := n["dns-server"]
		if dns == "" {
			dns = n["dns"]
		}
		networks = append(networks, Network{
			CIDR: n["address"], Gateway: n["gateway"], DNS: dns,
			LeaseCount: leaseCount, PoolSize: size,
		})
	}

	// Unique, in first-seen order — `Array.from(new Set(...))`.
	seen := make(map[string]bool, len(cidrs))
	lanCidrs := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		if !seen[c] {
			seen[c] = true
			lanCidrs = append(lanCidrs, c)
		}
	}

	totalPool, totalLeases := 0, 0
	for _, n := range networks {
		totalPool += n.PoolSize
		totalLeases += n.LeaseCount
	}

	payload := &LanPayload{
		TS: time.Now().UnixMilli(), LanCidrs: lanCidrs, Networks: networks,
		WanIP: wanIP, TotalPoolSize: totalPool, TotalLeases: totalLeases,
		PollMs: d.pollMs.ms(), InternetIface: internet,
	}

	// The fingerprint covers the same subset the original hashes — the CIDRs, the
	// WAN address, the internet interfaces, and each network's counts — so a
	// payload whose only difference is the clock is not sent. Built as a string
	// rather than hashed, because it is compared and never stored.
	var fp strings.Builder
	fp.WriteString(strings.Join(lanCidrs, ","))
	fp.WriteString("|" + wanIP + "|")
	for _, i := range internet {
		fp.WriteString(i.Name + "=" + i.IP + ";")
	}
	fp.WriteString("|")
	for _, n := range networks {
		fp.WriteString(n.CIDR + ":" + strconv.Itoa(n.LeaseCount) + ":" + strconv.Itoa(n.PoolSize) + ";")
	}

	d.mu.Lock()
	d.lanCidrs = lanCidrs
	d.last = payload
	changed := fp.String() != d.lastFP
	d.lastFP = fp.String()
	d.mu.Unlock()

	if !changed {
		return
	}
	// Two rooms, as the original has it: the DHCP page renders the subnet table
	// and the dashboard's Network card renders the same figures.
	// ONE EMIT TO THE UNION, not one per room. `session.go`'s emit closure:
	// "A sub naming SEVERAL rooms, comma separated, delivers ONE copy to the
	// union — socket.io's `.to(a).to(b)` behaves the same way, and looping
	// Broadcast would send that viewer the frame twice." This was two calls,
	// so a viewer in both rooms received it twice.
	d.emit("page-dhcp,dash-card-network", "lan:overview", payload)
	// AND `lan:wan` ROUTER-WIDE, carrying just the WAN address.
	//
	// The empty room IS the router-wide convention — it broadcasts to
	// `router-<id>`, the room every viewer of this router is in
	// (`internal/session/session.go:306`). `system:update`, `wan:status` and
	// `ifstatus:names` all send that way.
	//
	// THIS BLOCK PREVIOUSLY SAID THE CONVENTION DID NOT EXIST, and that note
	// blocked `ndWanIp` in `dash-coverage-check`'s ledger for several
	// iterations. It was wrong when written or stale soon after; either way it
	// was never checked against `session.go`. Closed 2026-08-24.
	//
	// The live handler does three things with this event and only ONE of them
	// exists: `window._wanGeoDetect` is called and defined nowhere in the live
	// repo, and `wanIpDisplay` is in that repo's own KNOWN orphan set. The port
	// reproduces the one that works. See ToDo.md #23.
	d.emit("", "lan:wan", map[string]any{"ts": payload.TS, "wanIp": payload.WanIP})
}

// LanCidrs is what other collectors ask for when they need to know which subnets
// are local.
func (d *DHCPNetworks) LanCidrs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.lanCidrs))
	copy(out, d.lanCidrs)
	return out
}

func (d *DHCPNetworks) Last() *LanPayload {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
}

func (d *DHCPNetworks) Start() { d.Tick(); d.poll.start() }

func (d *DHCPNetworks) Reconnected() {
	d.poll.stop()
	d.Tick()
	d.poll.start()
}

func (d *DHCPNetworks) Suspend() { d.poll.stop() }
func (d *DHCPNetworks) Resume()  { d.poll.start() }
func (d *DHCPNetworks) Stop()    { d.poll.stop() }

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (d *DHCPNetworks) SetPollMs(ms int) {
	d.pollMs.set(ms)
	d.poll.retime()
}
