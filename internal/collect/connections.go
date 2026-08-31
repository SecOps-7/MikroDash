package collect

// Connections collector — who is talking to whom, aggregated.
//
//	/ip/firewall/connection/print   the whole table, on an interval
//
// THE HEAVIEST TABLE THIS APP READS, and the aggregation is shaped by that. It
// is walked ONCE, building eight indexes as it goes rather than filtering the
// rows eight times; the per-country and per-source indexes are built only when
// somebody has the Connections page open, because they are the expensive half
// and nothing else renders them.
//
// GEO AND ASN ARE OPTIONAL. `geo.available()` is false on the Node side wherever
// geoip-lite failed to load, and the app degrades to counts without countries
// rather than refusing to work. The same is true here: both lookups default to
// nil and the payload is the same shape either way. `internal/geo` supplies the
// country lookup; the ASN one has no port yet, so `Org` is still nil in
// practice and every org index is empty.

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"
)

// ConnProtoCounts is the protocol split. `other` is everything that is not TCP,
// UDP or an ICMP variant — not a bucket for unknowns, a bucket for the rest.
type ConnProtoCounts struct {
	TCP   int `json:"tcp"`
	UDP   int `json:"udp"`
	ICMP  int `json:"icmp"`
	Other int `json:"other"`
}

// ConnCountryProto is the split within one country. It has no ICMP field: the
// live payload buckets ICMP into `other` here and does not elsewhere.
type ConnCountryProto struct {
	TCP   int `json:"tcp"`
	UDP   int `json:"udp"`
	Other int `json:"other"`
}

type ConnSource struct {
	IP    string `json:"ip"`
	Name  string `json:"name"`
	MAC   string `json:"mac"`
	Count int    `json:"count"`
}

type ConnDestination struct {
	// Key is `ip:port/proto`, which is what the page groups on — one host on two
	// ports is two destinations, because that is two different conversations.
	Key     string           `json:"key"`
	Count   int              `json:"count"`
	Country string           `json:"country"`
	City    string           `json:"city"`
	Proto   ConnCountryProto `json:"proto"`
	Org     *string          `json:"org"`
	Cat     *string          `json:"cat"`
}

type ConnOrgCount struct {
	Org   string  `json:"org"`
	Count int     `json:"count"`
	Cat   *string `json:"cat"`
}

type ConnCountry struct {
	CC    string           `json:"cc"`
	City  string           `json:"city"`
	Count int              `json:"count"`
	Proto ConnCountryProto `json:"proto"`
	Orgs  []ConnOrgCount   `json:"orgs"`
}

type ConnPort struct {
	Port  string `json:"port"`
	Count int    `json:"count"`
}

// ConnDestEntry is one row of a per-country or per-source destination index.
type ConnDestEntry struct {
	Key     string  `json:"key"`
	Count   int     `json:"count"`
	Country string  `json:"country"`
	City    string  `json:"city"`
	Org     *string `json:"org"`
	Cat     *string `json:"cat"`
}

type ConnsPayload struct {
	TS    int64 `json:"ts,omitempty"`
	Total int   `json:"total"`
	// Processed is how many rows were actually aggregated. Below Total when the
	// cap bit, and the page says so rather than presenting a partial count as
	// the whole truth.
	Processed        int               `json:"processed"`
	ProcessingCapped bool              `json:"processingCapped"`
	NewSinceLast     int               `json:"newSinceLast,omitempty"`
	ProtoCounts      ConnProtoCounts   `json:"protoCounts"`
	TopSources       []ConnSource      `json:"topSources"`
	TopDestinations  []ConnDestination `json:"topDestinations"`
	TopCountries     []ConnCountry     `json:"topCountries"`
	TopPorts         []ConnPort        `json:"topPorts"`
	// The four heavy indexes, built only for a viewer who has the page open.
	CountryDests map[string][]ConnDestEntry `json:"countryDests"`
	CountryPorts map[string][]ConnPort      `json:"countryPorts"`
	SourceDests  map[string][]ConnDestEntry `json:"sourceDests"`
	SourcePorts  map[string][]ConnPort      `json:"sourcePorts"`
	PollMs       int                        `json:"pollMs"`
}

// connsHeavyKeys are the four indexes the global emit leaves out. Named once,
// because the nils above and the removal below have to agree.
var connsHeavyKeys = [...]string{"countryDests", "countryPorts", "sourceDests", "sourcePorts"}

// connsLight marshals a ConnsPayload with the four heavy indexes ABSENT.
//
// ── WHY A MARSHALLER AND NOT `omitempty` ────────────────────────────────────
//
// `omitempty` would drop the keys from the REPLAY too, whenever the maps
// happened to be empty. The live `lastPayload` always carries all four — it
// assigns them unconditionally — so a viewer opening the Connections page would
// get a payload missing keys the live one has. The difference between the two
// emits is the point, and a tag cannot express "absent here, present there".
//
// Same problem and same answer as `geoplace.Location.MarshalJSON`: emit exactly
// the keys the original emits.
//
// It round-trips through a map rather than listing the fields it keeps, so a
// field added to ConnsPayload later is carried without anyone remembering to
// add it here — which a hand-written literal could not do.
type connsLight struct{ p *ConnsPayload }

func (l connsLight) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(l.p)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for _, k := range connsHeavyKeys {
		delete(m, k)
	}
	return json.Marshal(m)
}

// GeoLookup answers where an address is. Nil means no database, which is the
// live app's own degraded state rather than an error.
type GeoLookup func(ip string) (country, city string)

// OrgLookup answers who owns an address, and how to categorise them.
type OrgLookup func(ip string) (org, cat string, ok bool)

// ConnsInput is one tick's worth of the outside world.
type ConnsInput struct {
	Rows     []routeros.Reply
	LanCidrs []string
	TopN     int
	MaxConns int
	// Detailed builds the per-country and per-source indexes. False when nobody
	// has the Connections page open: they are the expensive half of this
	// aggregation and only that page renders them.
	Detailed bool
	PollMs   int
	NameOf   func(ip string) (name, mac string)
	Geo      GeoLookup
	Org      OrgLookup
}

// connDestKey is the identity of a destination: address, port and protocol.
//
// An IPv6 address is BRACKETED so the port separator is unambiguous —
// `[2001:db8::1]:443/tcp` rather than a string with seven colons in it.
func connDestKey(c routeros.Reply) string {
	dst := firstNonEmptyStr(c["dst-address"], c["dst"])
	proto := strings.ToLower(firstNonEmptyStr(c["protocol"], c["ip-protocol"]))
	dport := firstNonEmptyStr(c["dst-port"], c["port"])
	display := dst
	if isParsableIP(dst) && strings.Contains(dst, ":") {
		display = "[" + dst + "]"
	}
	switch {
	case display != "" && proto != "" && dport != "":
		return display + ":" + dport + "/" + proto
	case display != "" && dport != "":
		return display + ":" + dport
	case display != "":
		return display
	}
	return "unknown"
}

// counter is an insertion-ordered tally, which is what a JavaScript Map is.
// Order matters here because ties in a sort must break the way the original's
// stable sort breaks them: by first appearance.
type counter struct {
	n     map[string]int
	order []string
}

func newCounter() *counter { return &counter{n: map[string]int{}} }

func (c *counter) add(key string) {
	if _, ok := c.n[key]; !ok {
		c.order = append(c.order, key)
	}
	c.n[key]++
}

// top returns the n highest counts, ties broken by first appearance.
func (c *counter) top(n int) []string {
	keys := append([]string{}, c.order...)
	sort.SliceStable(keys, func(i, j int) bool { return c.n[keys[i]] > c.n[keys[j]] })
	if n > 0 && len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

// BuildConns is the whole aggregation, pure.
func BuildConns(in ConnsInput) *ConnsPayload {
	total := len(in.Rows)
	rows := in.Rows
	// Beyond the cap the rows are NOT processed, and the payload says so. A
	// truncated count presented as the whole truth is worse than a flagged one.
	if in.MaxConns > 0 && total > in.MaxConns {
		rows = rows[:in.MaxConns]
	}

	proto := ConnProtoCounts{}
	srcCounts := newCounter()
	dstCounts := newCounter()
	portCounts := newCounter()
	srcDests := map[string]*counter{}
	srcDestOrder := []string{}
	srcPorts := map[string]*counter{}
	srcPortOrder := []string{}
	countryProto := map[string]*ConnCountryProto{}
	countryOrder := []string{}
	countryCity := map[string]string{}
	countryPorts := map[string]*counter{}
	countryOrgs := map[string]*counter{}
	geoOf := map[string][2]string{} // ip -> {country, city}
	orgOf := map[string][2]string{} // ip -> {org, cat}

	for _, c := range rows {
		src := firstNonEmptyStr(c["src-address"], c["src"])
		dst := firstNonEmptyStr(c["dst-address"], c["dst"])
		p := strings.ToLower(firstNonEmptyStr(c["protocol"], c["ip-protocol"]))

		switch {
		case p == "tcp":
			proto.TCP++
		case p == "udp":
			proto.UDP++
		case strings.Contains(p, "icmp"):
			proto.ICMP++
		default:
			proto.Other++
		}

		srcIsLan := src != "" && guard.InCIDRs(src, in.LanCidrs)
		if srcIsLan {
			srcCounts.add(src)
		}

		// The destination half is about traffic LEAVING the network, so a
		// destination inside it is not counted at all.
		if dst == "" || guard.InCIDRs(dst, in.LanCidrs) {
			continue
		}

		key := connDestKey(c)
		dstCounts.add(key)
		ip := extractAddress(dst)
		port := firstNonEmptyStr(c["dst-port"], c["port"])
		if port != "" {
			portCounts.add(port)
		}

		if in.Geo != nil && isParsableIP(ip) {
			if _, ok := geoOf[ip]; !ok {
				country, city := in.Geo(ip)
				geoOf[ip] = [2]string{country, city}
			}
			if cc := geoOf[ip][0]; cc != "" {
				if _, ok := countryCity[cc]; !ok {
					countryCity[cc] = geoOf[ip][1]
				}
				cp := countryProto[cc]
				if cp == nil {
					cp = &ConnCountryProto{}
					countryProto[cc] = cp
					countryOrder = append(countryOrder, cc)
				}
				switch p {
				case "tcp":
					cp.TCP++
				case "udp":
					cp.UDP++
				default:
					cp.Other++
				}
				if port != "" {
					if countryPorts[cc] == nil {
						countryPorts[cc] = newCounter()
					}
					countryPorts[cc].add(port)
				}
			}
		}

		if in.Org != nil && isParsableIP(ip) {
			if _, ok := orgOf[ip]; !ok {
				org, cat, found := in.Org(ip)
				if !found {
					org, cat = "", ""
				}
				orgOf[ip] = [2]string{org, cat}
			}
			if org := orgOf[ip][0]; org != "" {
				cc := geoOf[ip][0]
				if cc == "" {
					cc = "__unknown__"
				}
				if countryOrgs[cc] == nil {
					countryOrgs[cc] = newCounter()
				}
				countryOrgs[cc].add(org)
			}
		}

		if srcIsLan {
			if srcDests[src] == nil {
				srcDests[src] = newCounter()
				srcDestOrder = append(srcDestOrder, src)
			}
			srcDests[src].add(key)
			if port != "" {
				if srcPorts[src] == nil {
					srcPorts[src] = newCounter()
					srcPortOrder = append(srcPortOrder, src)
				}
				srcPorts[src].add(port)
			}
		}
	}

	geoFor := func(ip string) (string, string) {
		g := geoOf[ip]
		return g[0], g[1]
	}
	orgFor := func(ip string) (*string, *string) {
		o := orgOf[ip]
		if o[0] == "" {
			return nil, nil
		}
		org, cat := o[0], o[1]
		if cat == "" {
			return &org, nil
		}
		return &org, &cat
	}

	topSources := []ConnSource{}
	for _, ip := range srcCounts.top(in.TopN) {
		name, mac := "", ""
		if in.NameOf != nil {
			name, mac = in.NameOf(ip)
		}
		// A SOURCE IS NEVER NAMELESS HERE. With no lease and no ARP entry the
		// live collector falls back to the address itself, so the table always
		// has something in its first column — which is the opposite of what the
		// bandwidth collector does with the same missing information, and both
		// are reproduced as they are.
		if name == "" {
			name = ip
		}
		topSources = append(topSources, ConnSource{IP: ip, Name: name, MAC: mac, Count: srcCounts.n[ip]})
	}

	topDestinations := []ConnDestination{}
	for _, key := range dstCounts.top(in.TopN) {
		ip := extractAddress(key)
		country, city := geoFor(ip)
		org, cat := orgFor(ip)
		var cp ConnCountryProto
		if country != "" && countryProto[country] != nil {
			cp = *countryProto[country]
		}
		topDestinations = append(topDestinations, ConnDestination{
			Key: key, Count: dstCounts.n[key], Country: country, City: city,
			Proto: cp, Org: org, Cat: cat,
		})
	}

	topPorts := []ConnPort{}
	for _, port := range portCounts.top(10) {
		topPorts = append(topPorts, ConnPort{Port: port, Count: portCounts.n[port]})
	}

	topCountries := []ConnCountry{}
	for _, cc := range countryOrder {
		cp := countryProto[cc]
		orgs := []ConnOrgCount{}
		if countryOrgs[cc] != nil {
			for _, org := range countryOrgs[cc].top(4) {
				o := org
				var cat *string
				if c := orgCatOf(orgOf, o); c != "" {
					cat = &c
				}
				orgs = append(orgs, ConnOrgCount{Org: o, Count: countryOrgs[cc].n[o], Cat: cat})
			}
		}
		topCountries = append(topCountries, ConnCountry{
			CC: cc, City: countryCity[cc],
			Count: cp.TCP + cp.UDP + cp.Other, Proto: *cp, Orgs: orgs,
		})
	}
	sort.SliceStable(topCountries, func(i, j int) bool {
		return topCountries[i].Count > topCountries[j].Count
	})
	if len(topCountries) > 30 {
		topCountries = topCountries[:30]
	}

	// The four heavy indexes. EMPTY MAPS, not nil: the payload always carries
	// the keys, and "nobody is looking" must serialise the same as "nothing to
	// show" — the page tells them apart by whether it asked.
	countryDestsOut := map[string][]ConnDestEntry{}
	countryPortsOut := map[string][]ConnPort{}
	sourceDestsOut := map[string][]ConnDestEntry{}
	sourcePortsOut := map[string][]ConnPort{}

	if in.Detailed {
		for _, src := range srcDestOrder {
			entries := []ConnDestEntry{}
			for _, key := range srcDests[src].order {
				ip := extractAddress(key)
				country, city := geoFor(ip)
				org, cat := orgFor(ip)
				entries = append(entries, ConnDestEntry{
					Key: key, Count: srcDests[src].n[key],
					Country: country, City: city, Org: org, Cat: cat,
				})
			}
			sort.SliceStable(entries, func(i, j int) bool { return entries[i].Count > entries[j].Count })
			if len(entries) > 30 {
				entries = entries[:30]
			}
			sourceDestsOut[src] = entries
		}
		for _, src := range srcPortOrder {
			out := []ConnPort{}
			for _, port := range srcPorts[src].top(10) {
				out = append(out, ConnPort{Port: port, Count: srcPorts[src].n[port]})
			}
			sourcePortsOut[src] = out
		}
		for _, cc := range countryOrder {
			if countryPorts[cc] != nil {
				out := []ConnPort{}
				for _, port := range countryPorts[cc].top(10) {
					out = append(out, ConnPort{Port: port, Count: countryPorts[cc].n[port]})
				}
				countryPortsOut[cc] = out
			}
		}
	}

	return &ConnsPayload{
		Total: total, Processed: len(rows), ProcessingCapped: total > len(rows),
		ProtoCounts: proto, TopSources: topSources, TopDestinations: topDestinations,
		TopCountries: topCountries, TopPorts: topPorts,
		CountryDests: countryDestsOut, CountryPorts: countryPortsOut,
		SourceDests: sourceDestsOut, SourcePorts: sourcePortsOut,
		PollMs: in.PollMs,
	}
}

// orgCatOf finds the category recorded alongside an org name.
func orgCatOf(orgOf map[string][2]string, org string) string {
	for _, v := range orgOf {
		if v[0] == org {
			return v[1]
		}
	}
	return ""
}

// ── the shared snapshot ──────────────────────────────────────────────────────

// ConnTable is one reading of the connection table, shared between the two
// collectors that need it.
//
// THIS IS THE POINT OF THE WHOLE ARRANGEMENT. The connection table is the
// heaviest read this app makes, and TWO collectors want it: connections
// aggregates who is talking to whom, bandwidth differences the byte counters.
// Reading it twice would double the cost of the most expensive thing here, on
// hardware whose documented limit is exactly this kind of concurrency. So it is
// read once and deposited, and the second consumer takes the snapshot.
//
// The TIMESTAMP is as load-bearing as the rows: bandwidth's rates are bytes over
// elapsed time, and an unchanged timestamp means "you have already seen this",
// not "nothing moved".
type ConnTable struct {
	mu   sync.Mutex
	rows []routeros.Reply
	ts   int64
}

func NewConnTable() *ConnTable { return &ConnTable{} }

func (t *ConnTable) Set(rows []routeros.Reply, ts int64) {
	t.mu.Lock()
	t.rows, t.ts = rows, ts
	t.mu.Unlock()
}

// Latest returns the snapshot and when it was taken. The rows are NOT copied:
// they are read-only to every consumer, and copying a table of thousands on
// every tick would give back the saving this cache exists for.
func (t *ConnTable) Latest() ([]routeros.Reply, int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rows, t.ts
}

// ── the collector ────────────────────────────────────────────────────────────

var connsCmd = routeros.Cmd{Path: "/ip/firewall/connection/print", Args: []string{
	"=.proplist=.id,src-address,dst-address,protocol,dst-port,orig-bytes,repl-bytes"}}

// connsMaxRows is the processing cap. Beyond it rows are counted but not
// aggregated, and the payload says so — a truncated answer presented as the
// whole truth is worse than a flagged one.
const connsMaxRows = 8000

// Connections is the collector.
type Connections struct {
	ros    Reader
	emit   Emit
	pollMs *pollInterval
	topN   int

	table  *ConnTable
	leases *DHCPLeases
	nets   *DHCPNetworks
	// detailed reports whether anyone has the Connections page open. The heavy
	// per-country and per-source indexes are built only then.
	detailed func() bool
	geo      GeoLookup
	org      OrgLookup

	mu           sync.Mutex
	last         *ConnsPayload
	lastFp       string
	lastDetailFp string
	lastEmit     time.Time
	prevIDs      map[string]bool

	loop *pollLoop
}

// connsHeartbeat is how long an unchanged payload may be suppressed. The
// original's ten seconds, chosen against the page's own staleness timer: the
// worst-case gap is this plus one poll, which has to stay inside it.
const connsHeartbeat = 10 * time.Second

func NewConnections(ros Reader, emit Emit, table *ConnTable, leases *DHCPLeases,
	nets *DHCPNetworks, pollMs int) *Connections {
	c := &Connections{
		ros: ros, emit: emit, table: table, leases: leases, nets: nets,
		pollMs: newPollInterval(clampPoll(pollMs, 3000, 1000, 60000)),
		// FIVE, which is `topN`'s generated default — not ten, which is what this
		// said until 2026-08-29 and which no live default ever was. See WithTopN.
		topN:     5,
		prevIDs:  map[string]bool{},
		detailed: func() bool { return true },
	}
	c.loop = newPollLoop(func() { c.Tick() }, func() time.Duration {
		return c.pollMs.duration()
	})
	return c
}

// WithTopN sets how many rows the dashboard's Connections card shows — the
// operator's "Top Connections N" under Limits.
//
// ── IT WAS HARDCODED, AND THE SETTING DID NOTHING ─────────────────────────
//
// Reported by the operator on 2026-08-29: "the amount of items shown on the
// Connections card on the dashboard does not honour the Top Connections N value
// in Limits under settings." It did not, twice over — the field was fixed at 10
// with no writer anywhere, and 10 was not even the live default, which is 5.
//
// The live app passes `topN: _cfg.topN` when it constructs the collector
// (`src/index.js:486`), so the value takes effect on the next session rather
// than instantly; live does NOT live-patch this one, though it does patch
// `talkers.topN` and `conns.maxConns` four lines apart. That asymmetry is
// reproduced rather than smoothed out: making this one apply instantly would be
// a behaviour change, and the operator would then see two settings on the same
// card behave differently from the app being ported.
//
// A non-positive value keeps the default, matching `NewTalkers`.
func (c *Connections) WithTopN(n int) *Connections {
	if n > 0 {
		c.topN = n
	}
	return c
}

// WithDetailed supplies the test for "is anyone on the Connections page". The
// server answers it by asking the hub how many occupants the room has.
func (c *Connections) WithDetailed(fn func() bool) *Connections {
	if fn != nil {
		c.detailed = fn
	}
	return c
}

// WithGeo attaches the country lookup. Nil leaves every country index empty,
// which is what the live app shows wherever geoip-lite failed to load.
func (c *Connections) WithGeo(fn GeoLookup) *Connections {
	c.geo = fn
	return c
}

// WithOrg attaches the ownership lookup. Nil leaves every org index empty and
// the Sankey folds destinations onto country instead, which is exactly what the
// live app does when the ASN table matches nothing.
func (c *Connections) WithOrg(fn OrgLookup) *Connections {
	c.org = fn
	return c
}

func (c *Connections) Suspend() { c.loop.stop() }

func (c *Connections) Resume() {
	if c.ros.Connected() {
		c.loop.start()
	}
}

func (c *Connections) Start() { c.loop.start() }

func (c *Connections) Stop() { c.loop.stop() }

func (c *Connections) Reconnected() {
	c.loop.stop()
	c.mu.Lock()
	c.prevIDs = map[string]bool{}
	c.lastFp, c.lastDetailFp = "", ""
	c.mu.Unlock()
	c.loop.start()
}

func (c *Connections) Last() *ConnsPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func (c *Connections) lanCidrs() []string {
	if c.nets == nil {
		return nil
	}
	p := c.nets.Last()
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.Networks))
	for _, n := range p.Networks {
		if n.CIDR != "" {
			out = append(out, n.CIDR)
		}
	}
	return out
}

// nameOf resolves a LAN source to its DHCP name and MAC.
func (c *Connections) nameOf(ip string) (string, string) {
	if c.leases == nil {
		return "", ""
	}
	p := c.leases.Last()
	if p == nil {
		return "", ""
	}
	for _, l := range p.Leases {
		if l.IP == ip {
			return firstNonEmptyStr(l.Name, l.HostName), l.MAC
		}
	}
	return "", ""
}

func (c *Connections) Tick() {
	if !c.ros.Connected() {
		return
	}
	rows, err := c.ros.Do(connsCmd)
	if err != nil {
		return
	}
	now := time.Now().UnixMilli()
	// Deposited BEFORE the aggregation, so the other consumer is not waiting on
	// this collector's own work to see the rows.
	if c.table != nil {
		c.table.Set(rows, now)
	}

	detailed := c.detailed()
	payload := BuildConns(ConnsInput{
		Rows: rows, LanCidrs: c.lanCidrs(), TopN: c.topN, MaxConns: connsMaxRows,
		Detailed: detailed, PollMs: c.pollMs.ms(), NameOf: c.nameOf, Geo: c.geo, Org: c.org,
	})
	payload.TS = now

	c.mu.Lock()
	// `newSinceLast` is how many connection ids were not in the previous
	// reading. It is what makes a quiet table distinguishable from a busy one
	// whose totals happen to match.
	ids := map[string]bool{}
	fresh := 0
	for _, r := range rows {
		if id := r[".id"]; id != "" {
			ids[id] = true
			if !c.prevIDs[id] {
				fresh++
			}
		}
	}
	c.prevIDs = ids
	payload.NewSinceLast = fresh
	c.last = payload

	// The fingerprint deliberately excludes `ts` and `newSinceLast`: both change
	// every tick and would defeat the suppression entirely.
	fp := connsFingerprint(payload)
	changed := fp != c.lastFp || time.Since(c.lastEmit) >= connsHeartbeat
	if changed {
		c.lastFp = fp
		c.lastEmit = time.Now()
	}
	detailFp := ""
	if detailed {
		detailFp = connsDetailFingerprint(payload)
	}
	detailChanged := detailed && detailFp != c.lastDetailFp
	if detailChanged {
		c.lastDetailFp = detailFp
	}
	c.mu.Unlock()

	if changed {
		// The GLOBAL emit omits the four heavy indexes: only the Connections
		// page renders them, and they are most of the payload's weight.
		//
		// OMITS, not nils — see connsLight. Setting the fields to nil and
		// marshalling the struct sends `"countryDests": null` where the live
		// payload has NO KEY AT ALL, because `delete` removes a key and a nil Go
		// map marshals as null. That was the port's only conn:update key set
		// where the live app has two, which is exactly what
		// tools/live-socket-diff.js reported on 2026-08-28.
		light := *payload
		light.CountryDests, light.CountryPorts = nil, nil
		light.SourceDests, light.SourcePorts = nil, nil
		c.emit("page-connections,dash-card-connections", "conn:update", connsLight{&light})
	}
	if detailChanged {
		c.emit("page-connections", "conn:country-data", map[string]any{
			"ts": payload.TS, "countryDests": payload.CountryDests, "countryPorts": payload.CountryPorts,
		})
		c.emit("page-connections", "conn:source-data", map[string]any{
			"ts": payload.TS, "sourceDests": payload.SourceDests, "sourcePorts": payload.SourcePorts,
		})
	}
}

func connsFingerprint(p *ConnsPayload) string {
	var sb strings.Builder
	sb.WriteString(strconv.Itoa(p.Total))
	sb.WriteString("|")
	sb.WriteString(strconv.Itoa(p.ProtoCounts.TCP) + "," + strconv.Itoa(p.ProtoCounts.UDP) + "," +
		strconv.Itoa(p.ProtoCounts.ICMP) + "," + strconv.Itoa(p.ProtoCounts.Other))
	for _, s := range p.TopSources {
		sb.WriteString("|" + s.IP + ":" + strconv.Itoa(s.Count))
	}
	for _, d := range p.TopDestinations {
		sb.WriteString("|" + d.Key + ":" + strconv.Itoa(d.Count))
	}
	for _, pt := range p.TopPorts {
		sb.WriteString("|" + pt.Port + ":" + strconv.Itoa(pt.Count))
	}
	return sb.String()
}

// connsDetailFingerprint is the ORIGINAL's: per-country totals and per-source
// counts, not the indexes themselves. The indexes are large and derived, so
// hashing them would cost more than the emit it is trying to avoid.
func connsDetailFingerprint(p *ConnsPayload) string {
	var sb strings.Builder
	for _, c := range p.TopCountries {
		sb.WriteString(c.CC + ":" + strconv.Itoa(c.Count) + ";")
	}
	keys := make([]string, 0, len(p.SourceDests))
	for k := range p.SourceDests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteString(k + ":" + strconv.Itoa(len(p.SourceDests[k])) + ";")
	}
	return sb.String()
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (c *Connections) SetPollMs(ms int) {
	c.pollMs.set(ms)
	c.loop.retime()
}
