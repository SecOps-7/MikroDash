package collect

// Bandwidth collector — per-source-IP throughput, from connection byte deltas.
//
// THE ONLY COLLECTOR IN THE APP THAT NEVER TALKS TO A ROUTER. It reads the
// connection table the CONNECTIONS collector has already fetched and transforms
// it; a capture run against real hardware reported "neither a read nor a stream
// row", which is a shape rather than a gap. Its gate is therefore a generator
// over the `conns` fixture — see tools/bandwidth-cases.js.
//
// RATES ARE DELTAS BETWEEN TWO TICKS, which has two consequences worth stating:
// the first tick after a start or a reconnect reports every rate as zero, and a
// counter that went BACKWARDS (a reset, or a reused connection id) is discarded
// rather than reported as a huge burst.

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"
)

// bandwidthRFC1918 is the fallback LAN range set.
//
// Used only until the DHCP networks are known, so the page is not blank on
// first load. Hoisted rather than built per connection: this loop runs over
// every row of a table that reaches into the thousands.
var bandwidthRFC1918 = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

// BandwidthDevice is one source IP's usage, with its busiest destination.
type BandwidthDevice struct {
	SrcIP     string  `json:"srcIp"`
	DstIP     string  `json:"dstIp"`
	RxMbps    float64 `json:"rxMbps"`
	TxMbps    float64 `json:"txMbps"`
	TotalMbps float64 `json:"totalMbps"`
	Proto     string  `json:"proto"`
	Iface     string  `json:"iface"`
	Name      string  `json:"name"`
	MAC       string  `json:"mac"`
	Country   string  `json:"country"`
	City      string  `json:"city"`
	// Org and Cat are null rather than empty when unknown — the live payload
	// distinguishes "looked up and found nothing" from "not looked up".
	Org   *string `json:"org"`
	Cat   *string `json:"cat"`
	IsLan bool    `json:"isLan"`
	// IsIpv6 is a substring test on the source, exactly as the original does it:
	// anything with a colon in it is v6.
	IsIpv6 bool `json:"isIpv6"`
}

type BandwidthPayload struct {
	TS      int64             `json:"ts"`
	Devices []BandwidthDevice `json:"devices"`
	PollMs  int               `json:"pollMs"`
}

// bwPrev is one connection's last reading.
type bwPrev struct {
	OrigBytes, ReplBytes int64
	TS                   int64
}

// bwBpsToMbps is bandwidth.js's OWN `bpsToMbps`, which is not the one in
// util.js that ifstatus uses — the live code really does have two functions of
// that name. This one takes BYTES and an elapsed time and rounds to four
// decimals; the other takes a bits-per-second string and rounds to three. They
// are named apart here because a Go package has one namespace, and conflating
// them would change every rate on one page or the other.
func bwBpsToMbps(bytes int64, dtMs int64) float64 {
	if dtMs <= 0 {
		return 0
	}
	v := (float64(bytes) * 8) / (float64(dtMs) / 1000) / 1e6
	return round4(v)
}

func round4(f float64) float64 {
	r, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'f', 4, 64), 64)
	return r
}

// extractAddress strips a port or a prefix from a RouterOS address.
//
// `198.51.100.5:443` is a host and a port; `198.51.100.0/24` is a network. The
// bracketed form is IPv6 with a port. Anything that does not parse is returned
// with only its prefix removed, which is what the original falls through to.
func extractAddress(value string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "[") {
		if end := strings.Index(raw, "]"); end > 1 {
			return raw[1:end]
		}
	}
	if isParsableIP(raw) {
		return raw
	}
	withoutCIDR := raw
	if slash := strings.Index(raw, "/"); slash != -1 {
		withoutCIDR = raw[:slash]
	}
	if isParsableIP(withoutCIDR) {
		return withoutCIDR
	}
	if lastColon := strings.LastIndex(raw, ":"); lastColon > 0 {
		host := raw[:lastColon]
		port := raw[lastColon+1:]
		if slash := strings.Index(port, "/"); slash != -1 {
			port = port[:slash]
		}
		if isDigits(port) && (isParsableIP(host) || !strings.Contains(host, ":")) {
			return host
		}
	}
	return withoutCIDR
}

func isDigits(s string) bool {
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

func isParsableIP(s string) bool {
	// ipaddr.isValid, which accepts both families and nothing else.
	return guard.InCIDRs(s, []string{"0.0.0.0/0", "::/0"})
}

// bwDst is one destination of one source, aggregated across its connections.
type bwDst struct {
	RxMbps, TxMbps float64
	Proto, Iface   string
	DstIP          string
}

type bwSrc struct {
	RxMbps, TxMbps float64
	dsts           map[string]*bwDst
	dstOrder       []string
}

// BandwidthInput is one tick's worth of the outside world.
type BandwidthInput struct {
	Rows []routeros.Reply
	// Now is the SNAPSHOT's timestamp, not the wall clock: the rates are
	// computed against the gap between two snapshots, and using the reading time
	// instead would attribute the collector's own scheduling jitter to the link.
	Now      int64
	LanCidrs []string
	// IfaceOf and NameOf are the optional joins. A nil one costs exactly the
	// field it feeds, which is what the live app does when a collector is off.
	IfaceOf func(ip string) string
	NameOf  func(ip string) (name, mac string)
	// Geo answers where the DESTINATION is. Nil is the live app's own degraded
	// state — `geo.available()` false — and costs exactly the country and city
	// fields, which the page then renders empty.
	Geo GeoLookup
	// Org answers who owns the destination. Nil leaves `org` and `cat` null,
	// which is a different thing from empty and is what the page reads.
	Org    OrgLookup
	PollMs int
}

// BuildBandwidth is the whole transform, pure.
//
// `prev` is read AND written: it is the previous tick's counters, and the same
// map is carried forward. Stale connection ids are pruned from it here, which is
// what stops a long-lived process accumulating a row per connection ever seen.
func BuildBandwidth(prev map[string]bwPrev, in BandwidthInput) *BandwidthPayload {
	srcMap := map[string]*bwSrc{}
	srcOrder := []string{}
	seen := map[string]bool{}

	// The LAN filter falls back to RFC 1918 only when the DHCP networks are not
	// known yet. `isLan` on the destination deliberately does NOT fall back —
	// it answers "is this traffic staying inside the configured network", and a
	// guess would make an internet destination look local.
	activeCidrs := in.LanCidrs
	if len(activeCidrs) == 0 {
		activeCidrs = bandwidthRFC1918
	}

	for _, c := range in.Rows {
		id := c[".id"]
		if id == "" {
			continue
		}
		seen[id] = true

		src := extractAddress(firstNonEmptyStr(c["src-address"], c["src"]))
		dst := extractAddress(firstNonEmptyStr(c["dst-address"], c["dst"]))
		proto := strings.ToLower(firstNonEmptyStr(c["protocol"], c["ip-protocol"]))
		iface := ""
		if in.IfaceOf != nil {
			iface = in.IfaceOf(src)
		}
		origBytes := bwInt(c["orig-bytes"])
		replBytes := bwInt(c["repl-bytes"])

		if src == "" || dst == "" {
			continue
		}

		var rxMbps, txMbps float64
		if p, ok := prev[id]; ok && in.Now > p.TS {
			dt := in.Now - p.TS
			// orig is src->dst, which is TX from the source's point of view;
			// repl is the return path, which is RX.
			origDelta := origBytes - p.OrigBytes
			replDelta := replBytes - p.ReplBytes
			// A NEGATIVE delta is a counter reset or a reused id, not traffic.
			if origDelta >= 0 && replDelta >= 0 {
				txMbps = bwBpsToMbps(origDelta, dt)
				rxMbps = bwBpsToMbps(replDelta, dt)
			}
		}
		// Recorded for EVERY connection, including ones the LAN filter is about
		// to drop: the filter decides what is shown, not what is measured.
		prev[id] = bwPrev{OrigBytes: origBytes, ReplBytes: replBytes, TS: in.Now}

		if !guard.InCIDRs(src, activeCidrs) {
			continue
		}

		entry := srcMap[src]
		if entry == nil {
			entry = &bwSrc{dsts: map[string]*bwDst{}}
			srcMap[src] = entry
			srcOrder = append(srcOrder, src)
		}
		entry.RxMbps += rxMbps
		entry.TxMbps += txMbps

		dstKey := dst + "|" + proto
		d := entry.dsts[dstKey]
		if d == nil {
			d = &bwDst{Proto: proto, Iface: iface, DstIP: dst}
			entry.dsts[dstKey] = d
			entry.dstOrder = append(entry.dstOrder, dstKey)
		}
		d.RxMbps += rxMbps
		d.TxMbps += txMbps
	}

	// Prune connection ids that are gone, so the map tracks the table rather
	// than everything the table has ever held.
	for id := range prev {
		if !seen[id] {
			delete(prev, id)
		}
	}

	devices := make([]BandwidthDevice, 0, len(srcOrder))
	for _, srcIP := range srcOrder {
		entry := srcMap[srcIP]
		name, mac := "", ""
		if in.NameOf != nil {
			name, mac = in.NameOf(srcIP)
		}

		// The busiest destination wins the row. Walked in insertion order so a
		// tie resolves to the one seen first, as the original's Map does.
		top := &bwDst{}
		for _, k := range entry.dstOrder {
			d := entry.dsts[k]
			if d.RxMbps+d.TxMbps > top.RxMbps+top.TxMbps {
				top = d
			}
		}

		isLan := false
		if top.DstIP != "" {
			isLan = guard.InCIDRs(top.DstIP, in.LanCidrs)
		}

		// The geo join is on the DESTINATION and only when there is one. The
		// original's `_geo` treats a record with no country as no record at all
		// — `g && g.country ? … : {country:'', city:''}` — so a hit whose
		// country is empty contributes nothing, and neither does one here.
		var country, city string
		if in.Geo != nil && top.DstIP != "" && isParsableIP(top.DstIP) {
			if cc, ct := in.Geo(top.DstIP); cc != "" {
				country, city = cc, ct
			}
		}

		// ORG AND CAT ARE NULL TOGETHER OR NOT AT ALL. The original computes
		// `cat = org ? lookupCategory(org) : null`, so an unknown destination
		// carries two nulls rather than a null org beside a category of "other" —
		// and the page distinguishes them.
		var orgPtr, catPtr *string
		if in.Org != nil && top.DstIP != "" && isParsableIP(top.DstIP) {
			if o, c, ok := in.Org(top.DstIP); ok && o != "" {
				orgPtr, catPtr = &o, &c
			}
		}

		devices = append(devices, BandwidthDevice{
			SrcIP: srcIP, DstIP: top.DstIP,
			RxMbps: round4(entry.RxMbps), TxMbps: round4(entry.TxMbps),
			TotalMbps: round4(entry.RxMbps + entry.TxMbps),
			Proto:     top.Proto, Iface: top.Iface,
			Name: name, MAC: mac,
			Country: country, City: city,
			Org: orgPtr, Cat: catPtr,
			IsLan: isLan, IsIpv6: strings.Contains(srcIP, ":"),
		})
	}

	// Busiest first. STABLE, so equal totals keep the order the table gave them
	// — with most devices idle that is most of the list, and an unstable sort
	// would reshuffle the page on every tick for no reason.
	sort.SliceStable(devices, func(i, j int) bool {
		return devices[i].TotalMbps > devices[j].TotalMbps
	})

	return &BandwidthPayload{TS: in.Now, Devices: devices, PollMs: in.PollMs}
}

func bwInt(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// ── the collector ────────────────────────────────────────────────────────────

// bandwidthConnCmd is the connection table, with the proplist connections.js
// declares — the two collectors read the SAME columns because they read the same
// table for different questions.
var bandwidthConnCmd = routeros.Cmd{Path: "/ip/firewall/connection/print", Args: []string{
	"=.proplist=.id,src-address,dst-address,protocol,dst-port,orig-bytes,repl-bytes"}}

// Bandwidth is the collector.
//
// IT NO LONGER OWNS THE READ. The connections collector reads the table and
// deposits it in a shared ConnTable; this one takes the snapshot — one read
// serving two consumers, which is the channel economy this port is organised
// around. It keeps its own read as a fallback for a session that has no
// connections collector, and the transform is unchanged either way because it
// takes its rows as an argument.
type Bandwidth struct {
	ros    Reader
	emit   Emit
	pollMs *pollInterval

	rates  RateSource
	leases *DHCPLeases
	nets   *DHCPNetworks
	table  *ConnTable
	geo    GeoLookup
	org    OrgLookup

	mu     sync.Mutex
	prev   map[string]bwPrev
	last   *BandwidthPayload
	lastFp string
	// lastSnapshot is the timestamp of the reading this collector last worked
	// from. See Tick: the same snapshot twice would difference to zero.
	lastSnapshot int64
	// lastEmit is when a payload last went out. The fingerprint suppresses an
	// unchanged one, and this is what stops that suppression being permanent.
	lastEmit time.Time

	loop *pollLoop
}

// bandwidthHeartbeat is how long an unchanged payload may be suppressed. The
// original's ten seconds: long enough to be quiet on an idle network, short
// enough that the page can tell an idle link from a dead collector.
const bandwidthHeartbeat = 10 * time.Second

func NewBandwidth(ros Reader, emit Emit, rates RateSource, leases *DHCPLeases,
	nets *DHCPNetworks, pollMs int) *Bandwidth {
	b := &Bandwidth{
		ros: ros, emit: emit, rates: rates, leases: leases, nets: nets,
		pollMs: newPollInterval(clampPoll(pollMs, 5000, 3000, 60000)),
		prev:   map[string]bwPrev{},
	}
	b.loop = newPollLoop(func() { b.Tick() }, func() time.Duration {
		return b.pollMs.duration()
	})
	return b
}

// WithTable points this collector at the shared connection-table snapshot. With
// one, it stops reading the table itself.
func (b *Bandwidth) WithTable(t *ConnTable) *Bandwidth {
	b.table = t
	return b
}

// WithGeo attaches the country lookup. A nil one leaves the fields empty, which
// is the live app's behaviour wherever geoip-lite failed to load.
func (b *Bandwidth) WithGeo(fn GeoLookup) *Bandwidth {
	b.geo = fn
	return b
}

// WithOrg attaches the ownership lookup. Nil leaves org and cat null on every
// row, which is what the live app sends when the ASN table matches nothing.
func (b *Bandwidth) WithOrg(fn OrgLookup) *Bandwidth {
	b.org = fn
	return b
}

func (b *Bandwidth) Suspend() { b.loop.stop() }

func (b *Bandwidth) Resume() {
	if b.ros.Connected() {
		b.loop.start()
	}
}

func (b *Bandwidth) Start() { b.loop.start() }

func (b *Bandwidth) Stop() { b.loop.stop() }

// Reconnected clears the counters. A reconnect usually means the router
// rebooted, in which case every connection id is new and every counter starts
// again — differencing across that boundary would report the whole table as one
// enormous burst.
func (b *Bandwidth) Reconnected() {
	b.loop.stop()
	b.mu.Lock()
	b.prev = map[string]bwPrev{}
	b.lastFp = ""
	b.lastSnapshot = 0
	b.mu.Unlock()
	b.loop.start()
}

func (b *Bandwidth) Last() *BandwidthPayload {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

// lanCidrs is the configured LAN ranges, from the DHCP networks collector.
func (b *Bandwidth) lanCidrs() []string {
	if b.nets == nil {
		return nil
	}
	p := b.nets.Last()
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

// ifaceOf resolves a source address to the interface it arrived on, by matching
// it against each running interface's addresses.
func (b *Bandwidth) ifaceOf(ip string) string {
	if b.rates == nil {
		return ""
	}
	s, ok := b.rates.(interface{ Last() *IfStatusPayload })
	if !ok {
		return ""
	}
	p := s.Last()
	if p == nil {
		return ""
	}
	for _, iface := range p.Interfaces {
		if !iface.Running || iface.Disabled {
			continue
		}
		for _, cidr := range iface.IPs {
			if guard.InCIDRs(ip, []string{cidr}) {
				return iface.Name
			}
		}
	}
	return ""
}

// nameOf resolves a source address to its DHCP name and MAC.
func (b *Bandwidth) nameOf(ip string) (string, string) {
	if b.leases == nil {
		return "", ""
	}
	p := b.leases.Last()
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

func (b *Bandwidth) Tick() {
	if !b.ros.Connected() {
		return
	}

	var rows []routeros.Reply
	var now int64
	if b.table != nil {
		rows, now = b.table.Latest()
		if now == 0 {
			return // the connections collector has not read yet
		}
		// THE SAME SNAPSHOT TWICE IS NOT A MEASUREMENT. This collector's rates
		// are byte deltas over elapsed time, so re-differencing one reading
		// against itself yields zeros — which would overwrite good data with an
		// idle-looking table whenever this tick outruns the one that reads.
		b.mu.Lock()
		seen := now == b.lastSnapshot
		if !seen {
			b.lastSnapshot = now
		}
		b.mu.Unlock()
		if seen {
			return
		}
	} else {
		// No connections collector in this session: read the table directly.
		var err error
		rows, err = b.ros.Do(bandwidthConnCmd)
		if err != nil {
			return
		}
		now = time.Now().UnixMilli()
	}
	b.mu.Lock()
	payload := BuildBandwidth(b.prev, BandwidthInput{
		Rows: rows, Now: now, LanCidrs: b.lanCidrs(),
		IfaceOf: b.ifaceOf, NameOf: b.nameOf, Geo: b.geo, Org: b.org, PollMs: b.pollMs.ms(),
	})
	b.last = payload
	// The fingerprint is the original's: the source, and its two rates. Notably
	// NOT the destination or the totals — a device whose busiest destination
	// changed while its throughput did not is the same row to a reader.
	fp := bandwidthFingerprint(payload)
	changed := fp != b.lastFp || time.Since(b.lastEmit) >= bandwidthHeartbeat
	if changed {
		b.lastFp = fp
		b.lastEmit = time.Now()
	}
	b.mu.Unlock()

	if changed {
		b.emit("page-bandwidth,dash-card-bandwidth", "bandwidth:update", payload)
	}
}

func bandwidthFingerprint(p *BandwidthPayload) string {
	var sb strings.Builder
	for _, d := range p.Devices {
		sb.WriteString(d.SrcIP)
		sb.WriteByte('|')
		sb.WriteString(strconv.FormatFloat(d.RxMbps, 'f', -1, 64))
		sb.WriteByte('|')
		sb.WriteString(strconv.FormatFloat(d.TxMbps, 'f', -1, 64))
		sb.WriteByte(';')
	}
	return sb.String()
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (b *Bandwidth) SetPollMs(ms int) {
	b.pollMs.set(ms)
	b.loop.retime()
}
