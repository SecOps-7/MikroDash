package collect

// The router's IPv4 and IPv6 addresses, for the IP Addresses page (#97).
//
// ── ONE LIST, TAGGED BY FAMILY ─────────────────────────────────────────────
//
// `/ip/address` and `/ipv6/address` are two menus with different properties,
// and the page edits each through its own resource. They are still one table on
// the page, the way Routes holds both families, so they arrive as one list and
// each row says which family it is.
//
// ── IPV4 THROUGH THE SCHEDULER, IPV6 ALONGSIDE IT ───────────────────────────
//
// `/ip/address/print` is already read by Interfaces, WAN and DHCP Networks, so it
// is subscribed here and the cache answers every one of them from one read.
// `/ipv6/address/print` is read when the IPv4 answer lands, the same way the
// Interfaces collector reads its second and third menus. A router without the
// IPv6 package refuses the menu, and that is not an error: it simply has no IPv6
// addresses to show.

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

var (
	ipv4AddressCmd = routeros.Cmd{Path: "/ip/address/print", Args: []string{
		"=.proplist=.id,address,network,interface,actual-interface,disabled,dynamic,invalid,comment"}}
	ipv6AddressCmd = routeros.Cmd{Path: "/ipv6/address/print", Args: []string{
		"=.proplist=.id,address,interface,actual-interface,advertise,eui-64,from-pool,no-dad,disabled,dynamic,invalid,comment"}}
)

// IPAddress is one row of either family. Fields the other family does not have
// are left at their zero value.
type IPAddress struct {
	ID     string `json:"id"`
	Family string `json:"family"` // "ipv4" or "ipv6"
	// Address is as RouterOS holds it, prefix length included.
	Address         string `json:"address"`
	Network         string `json:"network"`
	Interface       string `json:"interface"`
	ActualInterface string `json:"actualInterface"`
	Disabled        bool   `json:"disabled"`
	// Dynamic rows belong to whatever created them (a DHCP client, a VPN), and
	// Invalid ones sit on an interface that is gone. The page shows both and
	// edits neither.
	Dynamic   bool   `json:"dynamic"`
	Invalid   bool   `json:"invalid"`
	Comment   string `json:"comment"`
	Advertise bool   `json:"advertise"`
	EUI64     bool   `json:"eui64"`
	FromPool  string `json:"fromPool"`
}

// IPAddressesPayload is `ipaddresses:update`.
type IPAddressesPayload struct {
	Addresses []IPAddress `json:"addresses"`
	TS        int64       `json:"ts"`
}

// BuildIPAddresses turns the two menus' rows into the page's list: IPv4 first,
// each family in the order the router listed it. A row with no `.id` cannot be
// edited or tracked and is dropped.
func BuildIPAddresses(v4, v6 []routeros.Reply) []IPAddress {
	out := make([]IPAddress, 0, len(v4)+len(v6))
	add := func(family string, rows []routeros.Reply) {
		for _, r := range rows {
			if r[".id"] == "" {
				continue
			}
			out = append(out, IPAddress{
				ID: r[".id"], Family: family,
				Address: r["address"], Network: r["network"],
				Interface: r["interface"], ActualInterface: r["actual-interface"],
				Disabled: r["disabled"] == "true", Dynamic: r["dynamic"] == "true",
				Invalid: r["invalid"] == "true", Comment: r["comment"],
				Advertise: r["advertise"] == "true", EUI64: r["eui-64"] == "true",
				FromPool: r["from-pool"],
			})
		}
	}
	add("ipv4", v4)
	add("ipv6", v6)
	return out
}

// ipAddressesFingerprint covers every field, because the page draws or edits
// every one of them. See fingerprint_test.go.
func ipAddressesFingerprint(rows []IPAddress) string {
	b, _ := json.Marshal(rows)
	return string(b)
}

// ipAddressesHeartbeat is how long an unchanged list may go unsent.
const ipAddressesHeartbeat = 60 * time.Second

type IPAddresses struct {
	ros    Reader
	emit   Emit
	cache  *roscache.Cache
	pollMs *pollInterval
	poll   *pollLoop
	sched  scheduled

	mu       sync.Mutex
	last     *IPAddressesPayload
	lastFP   string
	lastEmit time.Time
	// lastV6 is the last IPv6 answer, kept so a transient failure of that read
	// does not blank every IPv6 row until the next tick.
	lastV6 []routeros.Reply
}

// NewIPAddresses builds the collector. pollMs of 0 takes the registry default,
// 30 s. Addresses are configuration rather than live data, and the fast/slow
// rule says a collector polling under 30 s must split its slow reads off; at 30 s
// there is nothing to split, and a third of the reads.
func NewIPAddresses(ros Reader, emit Emit, pollMs int) *IPAddresses {
	a := &IPAddresses{ros: ros, emit: emit,
		pollMs: newPollInterval(clampPoll(pollMs, 30000, 2000, 60000))}
	a.poll = newPollLoop(func() { a.Tick() }, func() time.Duration { return a.pollMs.duration() })
	// AFTER the loop exists, as in NewDNS: `scheduled` holds it as the no-cache
	// fallback.
	a.sched = scheduled{loop: a.poll,
		menu: ipv4AddressCmd.Path, fields: fieldsOf(ipv4AddressCmd), apply: a.apply,
		cadence: a.pollMs.duration}
	return a
}

// Tick reads both menus directly: the poll loop's path, and the refresh a write
// path asks for.
func (a *IPAddresses) Tick() {
	if !a.ros.Connected() {
		return
	}
	a.apply(readVia(a.cache, a.ros, ipv4AddressCmd, a.pollMs.duration()))
}

func (a *IPAddresses) apply(v4 []routeros.Reply, err error) {
	if err != nil {
		return // keep the last list rather than blanking the page on one failed read
	}
	v6, v6err := a.ros.Do(ipv6AddressCmd)

	a.mu.Lock()
	switch {
	case v6err == nil:
		a.lastV6 = v6
	case isAbsentMenu(v6err) || strings.Contains(strings.ToLower(v6err.Error()), "not allowed"):
		// No IPv6 package, or no permission to read it: there is nothing to show,
		// and that is an answer rather than a failure.
		a.lastV6 = nil
	default:
		// A transient failure keeps the last IPv6 rows.
		v6 = a.lastV6
	}
	rows := BuildIPAddresses(v4, v6)
	fp := ipAddressesFingerprint(rows)
	now := time.Now()
	if fp == a.lastFP && a.last != nil && now.Sub(a.lastEmit) < ipAddressesHeartbeat {
		a.mu.Unlock()
		return
	}
	a.lastFP, a.lastEmit = fp, now
	payload := &IPAddressesPayload{Addresses: rows, TS: now.UnixMilli()}
	a.last = payload
	a.mu.Unlock()
	EvIPAddressesUpdate.Emit(a.emit, ipAddressesRooms.Join(), *payload)
}

// RefreshNow re-reads both menus at once for a write path. The cached IPv4 rows
// are dropped first, or the re-read would be served the state from before the
// write.
func (a *IPAddresses) RefreshNow() {
	if !a.ros.Connected() {
		return
	}
	if a.cache != nil {
		a.cache.Invalidate(ipv4AddressCmd.Path)
	}
	a.Tick()
}

func (a *IPAddresses) Last() *IPAddressesPayload {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

func (a *IPAddresses) UseCache(c *roscache.Cache) {
	a.cache = c
	a.sched.useCache(c)
}

func (a *IPAddresses) Start() {
	if !a.sched.scheduling() {
		a.Tick()
	}
	a.sched.begin()
}

func (a *IPAddresses) Reconnected() {
	a.sched.end()
	a.mu.Lock()
	a.lastFP = ""
	a.mu.Unlock()
	if !a.sched.scheduling() {
		a.Tick()
	}
	a.sched.begin()
}

func (a *IPAddresses) Suspend() { a.sched.end() }
func (a *IPAddresses) Resume()  { a.sched.begin() }

func (a *IPAddresses) Stop() {
	a.sched.end()
	a.mu.Lock()
	a.lastFP = ""
	a.mu.Unlock()
}

// SetPollMs applies a new poll period to a running collector.
func (a *IPAddresses) SetPollMs(ms int) {
	a.pollMs.set(ms)
	a.poll.retime()
}

// PollMs is the collector's current poll period.
func (a *IPAddresses) PollMs() int { return a.pollMs.ms() }
