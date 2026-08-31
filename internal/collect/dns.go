package collect

// DNS collector — the port of src/collectors/dns.js.
//
//	/ip/dns          resolver settings — servers, DoH, cache size and usage
//	/ip/dns/static   static entries
//
// THE CACHE CONTENTS ARE NOT READ, and that is a decision worth carrying over
// rather than an omission to fix. /ip/dns/cache holds every name the router has
// resolved — hundreds of rows on an idle home router — so enumerating it is a
// standing cost for a table nobody asked to browse. The cache-used and
// cache-size FIGURES still reach the page; they come from the one settings row.
// It is also a privacy line: the settings row says how full the cache is, while
// the cache itself is a log of everywhere the network has been.

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

var (
	dnsSettingsCmd = routeros.Cmd{Path: "/ip/dns/print"}
	dnsStaticCmd   = routeros.Cmd{Path: "/ip/dns/static/print", Args: []string{
		"=.proplist=.id,name,address,type,ttl,disabled,comment,regexp,cname," +
			"forward-to,text,mx-exchange,ns,srv-target"}}
)

// Static entries are configuration: they change when somebody edits the router,
// not every tick. The settings row is read every tick because cache-used is live.
const dnsConfigEvery = 12

// DNSSettings is the settings row as the page renders it. Field order matches
// the object literal in parseDnsSettings so the emitted JSON reads the same.
type DNSSettings struct {
	Servers        []string `json:"servers"`
	DynamicServers []string `json:"dynamicServers"`
	// A router with no DoH must render the panel as "off", not as blank fields,
	// so the flag is explicit rather than inferred from an empty string at the
	// other end of a socket.
	DohEnabled              bool     `json:"dohEnabled"`
	DohURL                  string   `json:"dohUrl"`
	DohVerifyCert           bool     `json:"dohVerifyCert"`
	DohMaxServerConnections *float64 `json:"dohMaxServerConnections"`
	DohMaxConcurrentQueries *float64 `json:"dohMaxConcurrentQueries"`
	DohTimeout              string   `json:"dohTimeout"`
	AllowRemoteRequests     bool     `json:"allowRemoteRequests"`
	CacheSize               *float64 `json:"cacheSize"`
	CacheUsed               *float64 `json:"cacheUsed"`
	CacheMaxTTL             string   `json:"cacheMaxTtl"`
	MaxUDPPacketSize        *float64 `json:"maxUdpPacketSize"`
	MaxConcurrentQueries    *float64 `json:"maxConcurrentQueries"`
	QueryServerTimeout      string   `json:"queryServerTimeout"`
	QueryTotalTimeout       string   `json:"queryTotalTimeout"`
	MdnsRepeatIfaces        []string `json:"mdnsRepeatIfaces"`
	VRF                     string   `json:"vrf"`
}

// DNSStaticEntry is one row of the static table.
type DNSStaticEntry struct {
	// The row id, so the page can open an entry in the edit form. It addresses
	// a row, it does not authorise one — every write re-reads and re-checks
	// before touching it.
	ID       string `json:"id"`
	Name     string `json:"name"`
	Regexp   string `json:"regexp"`
	Address  string `json:"address"`
	Type     string `json:"type"`
	TTL      string `json:"ttl"`
	Disabled bool   `json:"disabled"`
	Comment  string `json:"comment"`
}

// DNSPayload is the dns:update body.
type DNSPayload struct {
	TS            int64            `json:"ts"`
	PollMs        int              `json:"pollMs"`
	Settings      DNSSettings      `json:"settings"`
	StaticEntries []DNSStaticEntry `json:"staticEntries"`
	Available     bool             `json:"available"`
}

// ParseDNSSettings normalises the settings row into the shape the page renders.
func ParseDNSSettings(row routeros.Reply) DNSSettings {
	if row == nil {
		row = routeros.Reply{}
	}
	doh := row["use-doh-server"]
	return DNSSettings{
		Servers:                 splitList(row["servers"]),
		DynamicServers:          splitList(row["dynamic-servers"]),
		DohEnabled:              doh != "",
		DohURL:                  doh,
		DohVerifyCert:           boolOf(row["verify-doh-cert"]),
		DohMaxServerConnections: numOf(row, "doh-max-server-connections"),
		DohMaxConcurrentQueries: numOf(row, "doh-max-concurrent-queries"),
		DohTimeout:              row["doh-timeout"],
		AllowRemoteRequests:     boolOf(row["allow-remote-requests"]),
		CacheSize:               numOf(row, "cache-size"),
		CacheUsed:               numOf(row, "cache-used"),
		CacheMaxTTL:             row["cache-max-ttl"],
		MaxUDPPacketSize:        numOf(row, "max-udp-packet-size"),
		MaxConcurrentQueries:    numOf(row, "max-concurrent-queries"),
		QueryServerTimeout:      row["query-server-timeout"],
		QueryTotalTimeout:       row["query-total-timeout"],
		MdnsRepeatIfaces:        splitList(row["mdns-repeat-ifaces"]),
		VRF:                     row["vrf"],
	}
}

// ParseStaticEntries builds the static table. A regexp entry has no name, so it
// is keyed on its pattern.
func ParseStaticEntries(rows []routeros.Reply) []DNSStaticEntry {
	out := []DNSStaticEntry{}
	for _, r := range rows {
		if r["name"] == "" && r["regexp"] == "" {
			continue // also drops the {undefined:''} row RouterOS can send
		}
		// One column, nine types: whichever property this record's type puts its
		// value in. The types are mutually exclusive on the router, so the order
		// only decides what a malformed row shows.
		address := ""
		for _, k := range []string{"address", "cname", "forward-to", "mx-exchange",
			"ns", "srv-target", "text"} {
			if address = r[k]; address != "" {
				break
			}
		}
		typ := r["type"]
		if typ == "" {
			if r["cname"] != "" {
				typ = "CNAME"
			} else {
				typ = "A"
			}
		}
		out = append(out, DNSStaticEntry{
			ID: r[".id"], Name: r["name"], Regexp: r["regexp"],
			Address: address, Type: typ, TTL: r["ttl"],
			Disabled: boolOf(r["disabled"]), Comment: r["comment"],
		})
	}
	// Collate, not a byte sort: this order is rendered verbatim and the Node
	// side sorts with localeCompare. SliceStable because Array.prototype.sort
	// is stable, so two entries with the same key keep the router's order.
	sort.SliceStable(out, func(i, j int) bool {
		ki, kj := out[i].Name, out[j].Name
		if ki == "" {
			ki = out[i].Regexp
		}
		if kj == "" {
			kj = out[j].Regexp
		}
		return Collate(ki, kj) < 0
	})
	return out
}

// DNS is the collector.
type DNS struct {
	ros    Reader
	emit   Emit
	pollMs *pollInterval

	poll *pollLoop

	mu       sync.Mutex
	settings DNSSettings
	static   []DNSStaticEntry
	ticks    int
	lastFp   string
	// nil = unprobed, false = this router has no such menu, stop asking.
	settingsAvailable *bool
	staticAvailable   *bool

	last    *DNSPayload
	lastErr string
}

// NewDNS builds the collector. pollMs of 0 takes the registry default.
func NewDNS(ros Reader, emit Emit, pollMs int) *DNS {
	d := &DNS{
		ros:      ros,
		emit:     emit,
		pollMs:   newPollInterval(clampPoll(pollMs, 10000, 2000, 60000)),
		settings: ParseDNSSettings(nil),
		static:   []DNSStaticEntry{},
	}
	d.poll = newPollLoop(func() { d.Tick() }, func() time.Duration {
		return d.pollMs.duration()
	})
	return d
}

func (d *DNS) read(cmd routeros.Cmd, avail **bool) []routeros.Reply {
	if *avail != nil && !**avail {
		return nil
	}
	rows, err := d.ros.Do(cmd)
	if err != nil {
		if menuMissing(err) {
			no := false
			*avail = &no
		} else {
			d.lastErr = err.Error()
		}
		return nil
	}
	yes := true
	*avail = &yes
	out := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		if len(r) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// Tick reads what is due this cycle and emits when something changed.
func (d *DNS) Tick() {
	if !d.ros.Connected() {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.ticks%dnsConfigEvery == 0 {
		d.static = ParseStaticEntries(d.read(dnsStaticCmd, &d.staticAvailable))
	}
	d.ticks++

	// The settings row carries cache-used, which is live, so it is read every
	// tick rather than on the config cadence — it is what drives the gauge.
	rows := d.read(dnsSettingsCmd, &d.settingsAvailable)
	var first routeros.Reply
	if len(rows) > 0 {
		first = rows[0]
	}
	d.settings = ParseDNSSettings(first)

	payload := &DNSPayload{
		TS: time.Now().UnixMilli(), PollMs: d.pollMs.ms(),
		Settings: d.settings, StaticEntries: d.static,
		Available: d.settingsAvailable == nil || *d.settingsAvailable,
	}
	d.last = payload

	// The WHOLE entry, not a hand-picked tuple, matching the Node side.
	//
	// It used to be [name, regexp, address, type, disabled] on both sides, and
	// that omitted `comment` and `ttl` — both of which the page renders. A
	// comment-only edit wrote the router, RefreshNow re-read it, and the
	// fingerprint came back identical, so the open page kept the old value until
	// something unrelated moved. On a busy router `cache-used` moves within a
	// tick or two and it merely looked slow; on an idle one the update never
	// arrived at all. Fingerprinting the entry as a whole means a field the page
	// gains cannot fall out of this list again.
	//
	// `ts` is still excluded, and must be: it moves every tick and would make
	// every tick an emit, which is the thing the fingerprint exists to prevent.
	fp, _ := json.Marshal(struct {
		S DNSSettings      `json:"s"`
		T []DNSStaticEntry `json:"t"`
	}{d.settings, d.static})
	if string(fp) == d.lastFp {
		return
	}
	d.lastFp = string(fp)
	d.emit("page-dns", "dns:update", payload)
}

// Last is the most recent payload, replayed to a socket that has just opened
// the page so it is not blank for a whole poll interval.
func (d *DNS) Last() *DNSPayload {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
}

// RefreshNow re-reads immediately, after a write, so the page shows what the
// router did.
//
// The tick counter is reset rather than the read being called directly: static
// entries are only re-read every dnsConfigEvery ticks, and a save that left the
// table showing the old row until the next config sweep — up to ten minutes on
// the default interval — would read as a failed save.
func (d *DNS) RefreshNow() {
	if !d.ros.Connected() {
		return
	}
	d.mu.Lock()
	d.ticks = 0
	d.mu.Unlock()
	d.Tick()
}

// Start does one tick straight away and then polls.
func (d *DNS) Start() {
	if d.ros.Connected() {
		d.Tick()
	}
	d.poll.start()
}

// Reconnected drops every latch. A router that has just come back may be a
// different build — an upgrade is the usual reason a connection dropped — so an
// "this menu is absent" decision taken against the old one must not persist.
func (d *DNS) Reconnected() {
	d.poll.stop()
	d.mu.Lock()
	d.lastFp = ""
	d.ticks = 0
	d.settingsAvailable, d.staticAvailable = nil, nil
	d.mu.Unlock()
	d.Tick()
	d.poll.start()
}

func (d *DNS) Suspend() { d.poll.stop() }

func (d *DNS) Resume() {
	if d.ros.Connected() {
		d.poll.start()
	}
}

func (d *DNS) Stop() {
	d.poll.stop()
	d.mu.Lock()
	d.lastFp = ""
	d.mu.Unlock()
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (d *DNS) SetPollMs(ms int) {
	d.pollMs.set(ms)
	d.poll.retime()
}

// PollMs is the collector's current poll period. Exported for callers that
// re-tune it and then need to confirm what took effect.
func (d *DNS) PollMs() int { return d.pollMs.ms() }
