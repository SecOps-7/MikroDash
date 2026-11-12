package collect

// The Apps Usage page: live traffic by application (Zoom, Netflix, Xbox...).
//
//	/ip/firewall/connection/print   the SAME read `connections` and `bandwidth`
//	                                take (identical fields), so the cache
//	                                delivers one reading to all three
//	/ip/dns/cache/print             once a minute: which names each address
//	                                was looked up as, held in memory only
//
// ── WHAT IS NEW HERE, AND WHAT IS NOT ───────────────────────────────────────
//
// The per-connection rates are connRates', shared with Bandwidth. The naming is
// internal/apps', pure. What this adds is the DNS cache join and the summing by
// application.
//
// ── THE DNS CACHE IS READ, AND NOTHING FROM IT IS KEPT ──────────────────────
//
// `internal/collect/dns.go` records why the DNS collector does not read the
// cache: cost, and that it is a list of every name every device asked for. The
// operator chose to read it for this page (2026-11-12), on these terms: only
// while the page is open or the router is recording app usage, once a minute,
// into an address->names map in memory that is replaced on each read. No name
// is emitted, logged or stored; the payload carries app ids and rates.
//
// ── WHAT IT CANNOT SEE ──────────────────────────────────────────────────────
//
// A device that resolves through its own encrypted DNS never asks the router,
// so its names are not in the cache and its traffic is named by port or by
// network owner instead, or not at all. FastTracked connections count only if
// the router keeps their byte counters.

import (
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/apps"
	"mikrodash/internal/guard"
	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

// appsConnCmd is bandwidthConnCmd, the same value: an identical field list is
// what lets the cache deliver one reading to every subscriber.
var appsConnCmd = bandwidthConnCmd

// appsDNSCmd reads the DNS cache. Measured on CHR Test, 7.25.1: CNAME rows are
// in this menu too (`type=CNAME`, `data=<target>.`), so one read serves both
// the addresses and the aliases that lead to them.
var appsDNSCmd = routeros.Cmd{Path: "/ip/dns/cache/print", Args: []string{"=.proplist=name,type,data"}}

const (
	// appsDNSEvery is how stale the address->names map may be. Names live for
	// the TTL the resolver gave them, usually minutes to hours, so a minute adds
	// little error for one read a minute.
	appsDNSEvery = time.Minute
	// appsDNSMaxRows bounds one read's work. RouterOS's default cache is 2 MiB,
	// a few thousand rows; this is well above that.
	appsDNSMaxRows = 50000
	// appsSparkLen is how many readings each app's sparkline holds, and how long
	// an app stays listed after its traffic stops (that many polls).
	appsSparkLen = 30
	// appsDevicesPerApp caps the drill-down list per app.
	appsDevicesPerApp = 25
	appsHeartbeat     = 10 * time.Second
)

// AppUsage is one application's live traffic.
type AppUsage struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Color    string `json:"color"`
	Logo     string `json:"logo"`
	Dark     bool   `json:"dark"`
	// RxMbps is what the network's devices downloaded from the app, TxMbps what
	// they sent it.
	RxMbps    float64 `json:"rxMbps"`
	TxMbps    float64 `json:"txMbps"`
	TotalMbps float64 `json:"totalMbps"`
	// Share is this app's percentage of all traffic this reading, identified
	// or not.
	Share   float64 `json:"share"`
	Devices int     `json:"devices"`
	// Via is how most of its traffic was named: "name", "port" or "asn".
	Via string `json:"via"`
	// Spark is the app's total Mbps over the last readings, oldest first.
	Spark []float64 `json:"spark"`
	// Top is the device using it most, for the tile's subtitle.
	Top string `json:"top"`
}

// AppDevice is one device using an app, for the drill-down.
type AppDevice struct {
	IP     string  `json:"ip"`
	Name   string  `json:"name"`
	MAC    string  `json:"mac"`
	RxMbps float64 `json:"rxMbps"`
	TxMbps float64 `json:"txMbps"`
}

// AppOwner is traffic the catalogue could not name, grouped by the network
// that owns the destination.
type AppOwner struct {
	Org    string  `json:"org"`
	RxMbps float64 `json:"rxMbps"`
	TxMbps float64 `json:"txMbps"`
}

// AppUsagePayload is what `apps:update` carries.
type AppUsagePayload struct {
	TS     int64 `json:"ts"`
	PollMs int   `json:"pollMs"`
	// The whole network's traffic this reading, named or not.
	RxMbps float64 `json:"rxMbps"`
	TxMbps float64 `json:"txMbps"`
	// UnnamedMbps is the part no step could name.
	UnnamedMbps float64 `json:"unnamedMbps"`
	// Apps is every app with traffic in the last readings, busiest first.
	Apps []AppUsage `json:"apps"`
	// Unnamed is the unnamed traffic by network owner, busiest first, capped.
	Unnamed []AppOwner `json:"unnamed"`
	// Devices is each listed app's devices, by app id.
	Devices map[string][]AppDevice `json:"devices"`
	// DNSNames is how many addresses the last DNS cache read gave names for,
	// and DNSAt when it was read (0: not yet). The page shows these so an empty
	// cache (DNS not served by the router) explains a page full of "unnamed".
	DNSNames int   `json:"dnsNames"`
	DNSAt    int64 `json:"dnsAt"`
}

// OwnerLookup answers who owns an address: the AS number and its display name.
type OwnerLookup func(ip string) (num uint, org string, ok bool)

// AppsInput is one reading's worth of the outside world.
type AppsInput struct {
	Rates []ConnRate
	Now   int64
	// LanCidrs: only traffic FROM these is counted, as on the Bandwidth page;
	// RFC 1918 when the DHCP networks are not known yet.
	LanCidrs []string
	Names    map[string][]string
	Owner    OwnerLookup
	NameOf   func(ip string) (name, mac string)
	Cat      *apps.Catalogue
	PollMs   int
}

type appAcc struct {
	app        *apps.App
	rx, tx     float64
	via        map[string]float64
	devices    map[string]*AppDevice
	deviceList []string
}

// BuildApps sums one reading's connection rates by application. Pure apart
// from `spark`, the per-app sparkline history, which it reads and advances.
func BuildApps(spark map[string][]float64, in AppsInput) *AppUsagePayload {
	cidrs := in.LanCidrs
	if len(cidrs) == 0 {
		cidrs = bandwidthRFC1918
	}
	accs := map[string]*appAcc{}
	owners := map[string]*AppOwner{}
	var rxAll, txAll, unnamed float64
	type key struct{ dst, proto, port string }
	memo := map[key]apps.Match{}
	orgOf := map[string]string{}

	for _, r := range in.Rates {
		if !guard.InCIDRs(r.Src, cidrs) || guard.InCIDRs(r.Dst, cidrs) {
			// Only LAN devices talking to the outside: traffic between two LAN
			// hosts is not "using an app".
			continue
		}
		rxAll += r.RxMbps
		txAll += r.TxMbps
		k := key{r.Dst, r.Proto, r.DstPort}
		m, seen := memo[k]
		if !seen {
			var asn uint
			if in.Owner != nil {
				if n, org, ok := in.Owner(r.Dst); ok {
					asn = n
					orgOf[r.Dst] = org
				}
			}
			port, _ := strconv.Atoi(r.DstPort)
			if in.Cat != nil {
				m = in.Cat.Classify(in.Names[canonIP(r.Dst)], asn, r.Proto, port)
			}
			memo[k] = m
		}
		if m.App == nil {
			unnamed += r.RxMbps + r.TxMbps
			org := orgOf[r.Dst]
			if org == "" {
				org = "Unknown network"
			}
			o := owners[org]
			if o == nil {
				o = &AppOwner{Org: org}
				owners[org] = o
			}
			o.RxMbps += r.RxMbps
			o.TxMbps += r.TxMbps
			continue
		}
		a := accs[m.App.ID]
		if a == nil {
			a = &appAcc{app: m.App, via: map[string]float64{}, devices: map[string]*AppDevice{}}
			accs[m.App.ID] = a
		}
		a.rx += r.RxMbps
		a.tx += r.TxMbps
		a.via[m.Via] += r.RxMbps + r.TxMbps + 1e-9
		d := a.devices[r.Src]
		if d == nil {
			d = &AppDevice{IP: r.Src}
			if in.NameOf != nil {
				d.Name, d.MAC = in.NameOf(r.Src)
			}
			a.devices[r.Src] = d
			a.deviceList = append(a.deviceList, r.Src)
		}
		d.RxMbps += r.RxMbps
		d.TxMbps += r.TxMbps
	}

	total := rxAll + txAll
	// Advance every sparkline: this reading's total for the apps that had
	// traffic, zero for the ones that did not. An app whose line is all zeros
	// has been quiet for appsSparkLen readings and leaves the list.
	ids := map[string]bool{}
	for id := range spark {
		ids[id] = true
	}
	for id := range accs {
		ids[id] = true
	}
	out := &AppUsagePayload{TS: in.Now, PollMs: in.PollMs, RxMbps: round4(rxAll), TxMbps: round4(txAll),
		UnnamedMbps: round4(unnamed), Apps: []AppUsage{}, Unnamed: []AppOwner{},
		Devices: map[string][]AppDevice{}}
	for id := range ids {
		v := 0.0
		if a := accs[id]; a != nil {
			v = round4(a.rx + a.tx)
		}
		line := append(spark[id], v)
		if len(line) > appsSparkLen {
			line = line[len(line)-appsSparkLen:]
		}
		quiet := true
		for _, x := range line {
			if x > 0 {
				quiet = false
				break
			}
		}
		if quiet {
			delete(spark, id)
			continue
		}
		spark[id] = line
		a := accs[id]
		var app *apps.App
		if a != nil {
			app = a.app
		} else if in.Cat != nil {
			if found, ok := in.Cat.App(id); ok {
				app = &found
			}
		}
		if app == nil {
			continue
		}
		u := AppUsage{ID: app.ID, Name: app.Name, Category: app.Category, Color: app.Color,
			Logo: app.Logo, Dark: app.Dark, Spark: append([]float64(nil), line...), Via: ""}
		if a != nil {
			u.RxMbps, u.TxMbps, u.TotalMbps = round4(a.rx), round4(a.tx), round4(a.rx+a.tx)
			if total > 0 {
				u.Share = round4(100 * (a.rx + a.tx) / total)
			}
			u.Devices = len(a.devices)
			best := 0.0
			for via, w := range a.via {
				if w > best || (w == best && via < u.Via) {
					best, u.Via = w, via
				}
			}
			devs := make([]AppDevice, 0, len(a.deviceList))
			for _, ip := range a.deviceList {
				d := *a.devices[ip]
				d.RxMbps, d.TxMbps = round4(d.RxMbps), round4(d.TxMbps)
				devs = append(devs, d)
			}
			sort.SliceStable(devs, func(i, j int) bool {
				return devs[i].RxMbps+devs[i].TxMbps > devs[j].RxMbps+devs[j].TxMbps
			})
			if len(devs) > appsDevicesPerApp {
				devs = devs[:appsDevicesPerApp]
			}
			if len(devs) > 0 {
				u.Top = firstNonEmptyStr(devs[0].Name, devs[0].IP)
			}
			out.Devices[app.ID] = devs
		} else {
			out.Devices[app.ID] = []AppDevice{}
		}
		out.Apps = append(out.Apps, u)
	}
	sort.SliceStable(out.Apps, func(i, j int) bool {
		if out.Apps[i].TotalMbps != out.Apps[j].TotalMbps {
			return out.Apps[i].TotalMbps > out.Apps[j].TotalMbps
		}
		return out.Apps[i].Name < out.Apps[j].Name
	})
	for _, o := range owners {
		out.Unnamed = append(out.Unnamed, AppOwner{Org: o.Org, RxMbps: round4(o.RxMbps), TxMbps: round4(o.TxMbps)})
	}
	sort.SliceStable(out.Unnamed, func(i, j int) bool {
		a, b := out.Unnamed[i], out.Unnamed[j]
		if a.RxMbps+a.TxMbps != b.RxMbps+b.TxMbps {
			return a.RxMbps+a.TxMbps > b.RxMbps+b.TxMbps
		}
		return a.Org < b.Org
	})
	if len(out.Unnamed) > 12 {
		out.Unnamed = out.Unnamed[:12]
	}
	return out
}

// canonIP is the address in netip's canonical form, so an IPv6 address the
// connection table and the DNS cache spell differently still joins.
func canonIP(s string) string {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().String()
	}
	return s
}

// DNSNames turns the DNS cache into address -> the names that lead to it,
// following CNAMEs back to the name a device asked for: `apps.mikrotik.com` is
// a CNAME for `global-balancer-e.mikrotik.com`, whose address therefore carries
// both names. Pure.
func DNSNames(rows []routeros.Reply) map[string][]string {
	norm := func(s string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".") }
	aliasesOf := map[string][]string{} // target -> names that are CNAMEs for it
	type addr struct{ name, ip string }
	var addrs []addr
	for i, r := range rows {
		if i >= appsDNSMaxRows {
			break
		}
		name, data := norm(r["name"]), strings.TrimSpace(r["data"])
		if name == "" || data == "" {
			continue
		}
		switch r["type"] {
		case "CNAME":
			t := norm(data)
			aliasesOf[t] = append(aliasesOf[t], name)
		case "A", "AAAA":
			addrs = append(addrs, addr{name, canonIP(data)})
		}
	}
	out := map[string][]string{}
	for _, a := range addrs {
		seen := map[string]bool{}
		queue := []string{a.name}
		for depth := 0; len(queue) > 0 && depth < 16; depth++ {
			var next []string
			for _, n := range queue {
				if seen[n] {
					continue
				}
				seen[n] = true
				out[a.ip] = append(out[a.ip], n)
				next = append(next, aliasesOf[n]...)
			}
			queue = next
		}
	}
	return out
}

// ── THE COLLECTOR ───────────────────────────────────────────────────────────

// Apps is the Apps Usage collector.
type Apps struct {
	ros    Reader
	emit   Emit
	leases LeaseSource
	nets   NetworkSource
	arp    ARPByIP
	owner  OwnerLookup
	cat    *apps.Catalogue
	cache  *roscache.Cache
	pollMs *pollInterval

	mu       sync.Mutex
	prev     map[string]bwPrev
	spark    map[string][]float64
	last     *AppUsagePayload
	lastFp   string
	lastEmit time.Time
	onBuild  func(*AppUsagePayload)

	dnsMu      sync.Mutex
	names      map[string][]string
	dnsAt      time.Time
	dnsReading bool

	loop  *pollLoop
	sched scheduled
}

// NewApps builds the collector. Its poll period follows Bandwidth's range.
func NewApps(ros Reader, emit Emit, leases LeaseSource, nets NetworkSource, pollMs int) *Apps {
	a := &Apps{
		ros: ros, emit: emit, leases: leases, nets: nets, cat: apps.Default(),
		pollMs: newPollInterval(clampPoll(pollMs, 5000, 1000, 60000)),
		prev:   map[string]bwPrev{}, spark: map[string][]float64{},
		names: map[string][]string{},
	}
	a.loop = newPollLoop(func() { a.Tick() }, func() time.Duration { return a.pollMs.duration() })
	a.sched = scheduled{
		loop: a.loop, menu: appsConnCmd.Path, fields: fieldsOf(appsConnCmd),
		cadence: a.pollMs.duration, apply: a.apply,
	}
	return a
}

// WithOwner attaches the network-owner lookup (AS number and name).
func (a *Apps) WithOwner(fn OwnerLookup) *Apps { a.owner = fn; return a }

// WithARP attaches the IP->MAC join used to name devices.
func (a *Apps) WithARP(x ARPByIP) *Apps { a.arp = x; return a }

// OnBuild registers a callback for every payload built, emitted or not: the
// history recorder sums usage from it.
func (a *Apps) OnBuild(fn func(*AppUsagePayload)) *Apps { a.onBuild = fn; return a }

func (a *Apps) apply(rows []routeros.Reply, err error) {
	if err != nil {
		return
	}
	a.build(rows, time.Now().UnixMilli())
}

func (a *Apps) Tick() {
	if !a.ros.Connected() {
		return
	}
	rows, err := a.ros.Do(appsConnCmd)
	if err != nil {
		return
	}
	a.build(rows, time.Now().UnixMilli())
}

// refreshNames starts a DNS cache read when the map is older than appsDNSEvery
// and none is running. In the background, so a slow cache read never holds up
// the connection reading it would otherwise sit behind.
func (a *Apps) refreshNames() {
	a.dnsMu.Lock()
	if a.dnsReading || time.Since(a.dnsAt) < appsDNSEvery {
		a.dnsMu.Unlock()
		return
	}
	a.dnsReading = true
	a.dnsMu.Unlock()
	go func() {
		rows, err := a.ros.Do(appsDNSCmd)
		a.dnsMu.Lock()
		defer a.dnsMu.Unlock()
		a.dnsReading = false
		a.dnsAt = time.Now()
		if err == nil {
			a.names = DNSNames(rows)
		}
	}()
}

func (a *Apps) build(rows []routeros.Reply, now int64) {
	a.refreshNames()
	a.dnsMu.Lock()
	names, dnsAt := a.names, a.dnsAt
	a.dnsMu.Unlock()

	a.mu.Lock()
	rates := connRates(a.prev, rows, now)
	p := BuildApps(a.spark, AppsInput{
		Rates: rates, Now: now, LanCidrs: lanCidrsOf(a.nets), Names: names,
		Owner: a.owner, NameOf: a.nameOf, Cat: a.cat, PollMs: a.pollMs.ms(),
	})
	p.DNSNames = len(names)
	if !dnsAt.IsZero() {
		p.DNSAt = dnsAt.UnixMilli()
	}
	a.last = p
	fp := appsFingerprint(p)
	changed := fp != a.lastFp || time.Since(a.lastEmit) >= appsHeartbeat
	if changed {
		a.lastFp, a.lastEmit = fp, time.Now()
	}
	cb := a.onBuild
	a.mu.Unlock()

	if cb != nil {
		cb(p)
	}
	if changed {
		EvAppsUpdate.Emit(a.emit, appsRooms.Join(), *p)
	}
}

func appsFingerprint(p *AppUsagePayload) string {
	var sb strings.Builder
	for _, u := range p.Apps {
		sb.WriteString(u.ID)
		sb.WriteByte('|')
		sb.WriteString(strconv.FormatFloat(u.RxMbps, 'f', -1, 64))
		sb.WriteByte('|')
		sb.WriteString(strconv.FormatFloat(u.TxMbps, 'f', -1, 64))
		sb.WriteByte(';')
	}
	sb.WriteString(strconv.FormatFloat(p.UnnamedMbps, 'f', -1, 64))
	return sb.String()
}

// nameOf names a device by its lease, then by ARP and the lease for that MAC:
// Bandwidth's chain.
func (a *Apps) nameOf(ip string) (string, string) {
	var leases *LeasesPayload
	if a.leases != nil {
		leases = a.leases.Last()
	}
	if l := LeaseForIP(leases, ip); l != nil {
		return firstNonEmptyStr(l.Name, l.HostName), l.MAC
	}
	if a.arp == nil {
		return "", ""
	}
	mac, _ := a.arp.MACForIP(ip)
	if mac == "" {
		return "", ""
	}
	if l := LeaseForMAC(leases, mac); l != nil {
		return firstNonEmptyStr(l.Name, l.HostName), mac
	}
	return "", mac
}

// lanCidrsOf is the configured LAN ranges from the DHCP networks collector.
func lanCidrsOf(nets NetworkSource) []string {
	if nets == nil {
		return nil
	}
	p := nets.Last()
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

func (a *Apps) Last() *AppUsagePayload {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

func (a *Apps) Suspend() { a.sched.end() }

func (a *Apps) Resume() {
	if a.ros.Connected() {
		a.sched.begin()
	}
}

func (a *Apps) Start() { a.sched.begin() }

func (a *Apps) Stop() { a.sched.end() }

// Reconnected clears the counters and the names: a reconnect usually means the
// router rebooted, every connection id is new and its cache is empty.
func (a *Apps) Reconnected() {
	a.sched.end()
	a.mu.Lock()
	a.prev = map[string]bwPrev{}
	a.lastFp = ""
	a.mu.Unlock()
	a.dnsMu.Lock()
	a.names, a.dnsAt = map[string][]string{}, time.Time{}
	a.dnsMu.Unlock()
	a.sched.begin()
}

// SetPollMs applies a new poll period to a running collector.
func (a *Apps) SetPollMs(ms int) {
	a.pollMs.set(ms)
	a.loop.retime()
	a.sched.retune()
}

// UseCache routes the connection read through the per-router cache, which is
// what makes it the same reading Connections and Bandwidth take.
func (a *Apps) UseCache(rc *roscache.Cache) {
	a.cache = rc
	a.sched.useCache(rc)
}
