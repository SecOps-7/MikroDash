package collect

// The Apps Usage page: traffic by application (Zoom, Netflix, Xbox...) over
// the last hour.
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
// internal/apps', pure. What this adds is the DNS cache join, the summing by
// application, and the rolling hour.
//
// ── THE LAST HOUR, MOVING ONCE A MINUTE ─────────────────────────────────────
//
// The page showed each reading's rates and moved with every poll; the operator
// found that too much movement (2026-11-12) and asked for the last hour. So a
// reading's rates are held for the time since the previous reading, at most two
// polls so a gap invents no traffic (appsHeldMs), turned into bytes
// (mbpsToBytes), and added to the one-minute buckets the reading covers: a
// reading that spans a minute boundary is split between the minutes by time.
// A minute closes when a reading falls in a later one. That is the only time
// `apps:update` is sent, carrying the sixty closed minutes before it, and the
// only time a minute is handed on (OnMinute) to the stored history, which
// therefore adds up the same bytes the page shows, computed once. Older minutes
// are dropped. The window lives in memory and is lost on a restart.
//
// Until the first minute closes, the page would have nothing; so the first
// reading that carries bytes is sent too, as the open minute so far, and the
// payload's From and To say how much of the hour it covers.
//
// ── THE DNS CACHE IS READ, AND NOTHING FROM IT IS KEPT ──────────────────────
//
// `internal/collect/dns.go` records why the DNS collector does not read the
// cache: cost, and that it is a list of every name every device asked for. The
// operator chose to read it for this page (2026-11-12), on these terms: only
// while the collector runs (the page is open, or app usage is being recorded),
// once a minute, into an address->names map in memory that is replaced on each
// read. No name is emitted, logged or stored; the payload carries app ids and
// byte counts.
//
// ── WHAT IT CANNOT SEE ──────────────────────────────────────────────────────
//
// A device that resolves through its own encrypted DNS never asks the router,
// so its names are not in the cache and its traffic is named by port or by
// network owner instead, or not at all. FastTracked connections count only if
// the router keeps their byte counters.

import (
	"math"
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
	// appsMinuteMs is one bucket of the window, and how often the page moves.
	appsMinuteMs = int64(time.Minute / time.Millisecond)
	// appsWindowMinutes is the window: the last hour. It is also how many
	// points each app's sparkline has, one a minute.
	appsWindowMinutes = 60
	// appsDevicesPerApp caps the drill-down list per app.
	appsDevicesPerApp = 25
)

// appsHeldMs is how long one reading's rate is held for: the time since the
// previous reading, at most twice the poll period, so a reading after a gap (a
// reconnect, a suspended collector, a stalled router) is not stretched across
// time nobody measured. Zero for the first reading, and for a clock that went
// backwards.
func appsHeldMs(prevTS, ts int64, pollMs int) int64 {
	if prevTS <= 0 || ts <= prevTS {
		return 0
	}
	if pollMs <= 0 {
		pollMs = 5000
	}
	return min(ts-prevTS, int64(2*pollMs))
}

// mbpsToBytes is the bytes a rate in megabits a second moves in `seconds`:
// Mbps x 125,000 x seconds.
func mbpsToBytes(mbps, seconds float64) float64 {
	if mbps <= 0 || seconds <= 0 {
		return 0
	}
	return mbps * 1e6 / 8 * seconds
}

// AppUsage is one application's traffic over the window.
type AppUsage struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Color    string `json:"color"`
	Logo     string `json:"logo"`
	Dark     bool   `json:"dark"`
	// RxBytes is what the network's devices downloaded from the app, TxBytes
	// what they sent it.
	RxBytes    int64 `json:"rxBytes"`
	TxBytes    int64 `json:"txBytes"`
	TotalBytes int64 `json:"totalBytes"`
	// Share is this app's percentage of all traffic in the window, identified
	// or not.
	Share float64 `json:"share"`
	// Devices is how many devices used it in the window.
	Devices int `json:"devices"`
	// Via is how most of its traffic was named: "name", "port" or "asn".
	Via string `json:"via"`
	// Spark is the app's bytes in each minute of the window, oldest first:
	// appsWindowMinutes points.
	Spark []int64 `json:"spark"`
	// Top is the device that used it most, for the tile's subtitle.
	Top string `json:"top"`
}

// AppDevice is one device's use of an app over the window, for the drill-down.
type AppDevice struct {
	IP      string `json:"ip"`
	Name    string `json:"name"`
	MAC     string `json:"mac"`
	RxBytes int64  `json:"rxBytes"`
	TxBytes int64  `json:"txBytes"`
}

// AppUsagePayload is what `apps:update` carries: the last hour, in bytes.
type AppUsagePayload struct {
	TS int64 `json:"ts"`
	// From and To bound the time the figures cover. To is the end of the last
	// closed minute, From an hour before it or when measuring began, whichever
	// is later: less than an hour apart, the hour is still filling. While the
	// first minute fills, To is that minute's latest reading.
	From int64 `json:"from"`
	To   int64 `json:"to"`
	// The whole network's traffic in the window, named or not.
	RxBytes int64 `json:"rxBytes"`
	TxBytes int64 `json:"txBytes"`
	// UnnamedBytes is the part no step could name. Only the total: the list of
	// it by network owner went with the page's section that showed it (the
	// operator, 2026-11-12).
	UnnamedBytes int64 `json:"unnamedBytes"`
	// Apps is every app with traffic in the window, busiest first.
	Apps []AppUsage `json:"apps"`
	// Devices is each listed app's devices over the window, by app id.
	Devices map[string][]AppDevice `json:"devices"`
	// DNSNames is how many addresses the last DNS cache read gave names for,
	// and DNSAt when it was read (0: not yet). The page shows these so an empty
	// cache (DNS not served by the router) explains a page full of "unnamed".
	DNSNames int   `json:"dnsNames"`
	DNSAt    int64 `json:"dnsAt"`
}

// AppMinute is one closed minute of one router's app traffic, in bytes, by app
// id: what the stored history adds up (internal/server/apps_history.go).
type AppMinute struct {
	Start int64
	Apps  map[string]AppBytes
}

// AppBytes is one app's bytes in one minute, each direction.
type AppBytes struct{ Rx, Tx float64 }

// OwnerLookup answers who owns an address: the AS number and its display name.
type OwnerLookup func(ip string) (num uint, org string, ok bool)

// AppsInput is one reading's worth of the outside world.
type AppsInput struct {
	Rates []ConnRate
	// LanCidrs: only traffic FROM these is counted, as on the Bandwidth page;
	// RFC 1918 when the DHCP networks are not known yet.
	LanCidrs []string
	Names    map[string][]string
	Owner    OwnerLookup
	Cat      *apps.Catalogue
}

type rxTx struct{ rx, tx float64 }

type appsSumApp struct {
	app *apps.App
	rxTx
	via     map[string]float64
	devices map[string]*rxTx
}

// appsSum is traffic summed by application: one reading's rates in Mbps
// (SumApps), or one minute's bytes (a bucket of the window). One shape for
// both, so a reading becomes a minute's bytes by one multiplication.
type appsSum struct {
	all rxTx
	// unnamed is the part of `all` no step could name, both directions.
	unnamed float64
	apps    map[string]*appsSumApp
}

func newAppsSum() *appsSum { return &appsSum{apps: map[string]*appsSumApp{}} }

// addScaled adds `src` times k to s. For a reading into a minute, k is the
// bytes one Mbps moves in the part of the reading that falls in that minute.
func (s *appsSum) addScaled(src *appsSum, k float64) {
	s.all.rx += src.all.rx * k
	s.all.tx += src.all.tx * k
	s.unnamed += src.unnamed * k
	for id, a := range src.apps {
		d := s.apps[id]
		if d == nil {
			d = &appsSumApp{app: a.app, via: map[string]float64{}, devices: map[string]*rxTx{}}
			s.apps[id] = d
		}
		d.rx += a.rx * k
		d.tx += a.tx * k
		for via, w := range a.via {
			d.via[via] += w * k
		}
		for ip, v := range a.devices {
			dv := d.devices[ip]
			if dv == nil {
				dv = &rxTx{}
				d.devices[ip] = dv
			}
			dv.rx += v.rx * k
			dv.tx += v.tx * k
		}
	}
}

// SumApps sums one reading's connection rates by application, in Mbps. Pure.
func SumApps(in AppsInput) *appsSum {
	cidrs := in.LanCidrs
	if len(cidrs) == 0 {
		cidrs = bandwidthRFC1918
	}
	out := newAppsSum()
	type key struct{ dst, proto, port string }
	memo := map[key]apps.Match{}

	for _, r := range in.Rates {
		if !guard.InCIDRs(r.Src, cidrs) || guard.InCIDRs(r.Dst, cidrs) {
			// Only LAN devices talking to the outside: traffic between two LAN
			// hosts is not "using an app".
			continue
		}
		out.all.rx += r.RxMbps
		out.all.tx += r.TxMbps
		k := key{r.Dst, r.Proto, r.DstPort}
		m, seen := memo[k]
		if !seen {
			var asn uint
			if in.Owner != nil {
				if n, _, ok := in.Owner(r.Dst); ok {
					asn = n
				}
			}
			port, _ := strconv.Atoi(r.DstPort)
			if in.Cat != nil {
				m = in.Cat.Classify(in.Names[canonIP(r.Dst)], asn, r.Proto, port)
			}
			memo[k] = m
		}
		if m.App == nil {
			out.unnamed += r.RxMbps + r.TxMbps
			continue
		}
		a := out.apps[m.App.ID]
		if a == nil {
			a = &appsSumApp{app: m.App, via: map[string]float64{}, devices: map[string]*rxTx{}}
			out.apps[m.App.ID] = a
		}
		a.rx += r.RxMbps
		a.tx += r.TxMbps
		a.via[m.Via] += r.RxMbps + r.TxMbps + 1e-9
		d := a.devices[r.Src]
		if d == nil {
			d = &rxTx{}
			a.devices[r.Src] = d
		}
		d.rx += r.RxMbps
		d.tx += r.TxMbps
	}
	return out
}

// appsWindow is the rolling hour: one-minute buckets of bytes, keyed by the
// start of the minute. Arithmetic only; the collector holds it under its lock.
type appsWindow struct {
	minutes map[int64]*appsSum
	// prevTS is the previous reading. since is when the measurement now running
	// began: the first reading, or the start of what the reading after a gap
	// was held for.
	prevTS, since int64
	// open is the minute readings fall in now; every minute before it is
	// closed, and a closed minute is never added to again.
	open int64
	// shown is whether a window has been sent yet.
	shown bool
}

func newAppsWindow() *appsWindow { return &appsWindow{minutes: map[int64]*appsSum{}} }

// add folds one reading (rates in Mbps) taken at ts into the minutes it covers.
// It returns the minutes the reading closed, oldest first, and where the
// window to send ends: the start of the open minute when a minute closed, the
// end of the open minute while nothing has been sent and the reading carried
// time, 0 for nothing to send.
func (w *appsWindow) add(r *appsSum, ts int64, pollMs int) (closed []AppMinute, end int64) {
	held := appsHeldMs(w.prevTS, ts, pollMs)
	from := ts - held
	if w.prevTS == 0 || from > w.prevTS {
		w.since = from
	}
	w.prevTS = ts
	cur := ts - ts%appsMinuteMs
	if w.open == 0 {
		w.open = cur
	}

	// The reading's bytes, split across the minutes it spans by time.
	if r != nil {
		for t := from; t < ts; {
			m := t - t%appsMinuteMs
			e := min(m+appsMinuteMs, ts)
			if m >= w.open {
				b := w.minutes[m]
				if b == nil {
					b = newAppsSum()
					w.minutes[m] = b
				}
				b.addScaled(r, mbpsToBytes(1, float64(e-t)/1000))
			}
			t = e
		}
	}

	if cur > w.open {
		for m, b := range w.minutes {
			if m >= w.open && m < cur {
				closed = append(closed, b.minute(m))
			}
		}
		sort.Slice(closed, func(i, j int) bool { return closed[i].Start < closed[j].Start })
		w.open = cur
		end = cur
	}
	for m := range w.minutes {
		if m < w.open-appsWindowMinutes*appsMinuteMs {
			delete(w.minutes, m)
		}
	}
	if end == 0 && !w.shown && held > 0 {
		end = w.open + appsMinuteMs
	}
	if end != 0 {
		w.shown = true
	}
	return closed, end
}

// minute is one bucket as the stored history takes it.
func (s *appsSum) minute(start int64) AppMinute {
	out := AppMinute{Start: start, Apps: make(map[string]AppBytes, len(s.apps))}
	for id, a := range s.apps {
		out.Apps[id] = AppBytes{Rx: a.rx, Tx: a.tx}
	}
	return out
}

// BuildApps is the window ending at `end` as the page draws it: the sixty
// minutes before `end`, summed, with each app's bytes per minute. `ts` is the
// reading that sent it, and nameOf names a device by its address. Pure apart
// from nameOf.
func BuildApps(w *appsWindow, end, ts int64, nameOf func(ip string) (name, mac string)) *AppUsagePayload {
	start := end - appsWindowMinutes*appsMinuteMs
	sum := newAppsSum()
	spark := map[string][]float64{}
	for m, b := range w.minutes {
		if m < start || m >= end {
			continue
		}
		sum.addScaled(b, 1)
		i := (m - start) / appsMinuteMs
		for id, a := range b.apps {
			line := spark[id]
			if line == nil {
				line = make([]float64, appsWindowMinutes)
				spark[id] = line
			}
			line[i] += a.rx + a.tx
		}
	}
	whole := func(f float64) int64 { return int64(math.Round(f)) }
	total := sum.all.rx + sum.all.tx
	// From is when measuring began, if that is within the hour; never after
	// To, which it would be in the window a gap's first reading closes.
	to := min(ts, end)
	out := &AppUsagePayload{TS: ts, From: min(max(start, w.since), to), To: to,
		RxBytes: whole(sum.all.rx), TxBytes: whole(sum.all.tx), UnnamedBytes: whole(sum.unnamed),
		Apps: []AppUsage{}, Devices: map[string][]AppDevice{}}

	for id, a := range sum.apps {
		if whole(a.rx+a.tx) == 0 {
			continue
		}
		app := a.app
		u := AppUsage{ID: id, Name: app.Name, Category: app.Category, Color: app.Color, Logo: app.Logo,
			Dark: app.Dark, RxBytes: whole(a.rx), TxBytes: whole(a.tx), TotalBytes: whole(a.rx + a.tx),
			Devices: len(a.devices), Spark: make([]int64, appsWindowMinutes)}
		for i, v := range spark[id] {
			u.Spark[i] = whole(v)
		}
		if total > 0 {
			u.Share = round4(100 * (a.rx + a.tx) / total)
		}
		best := 0.0
		for via, wt := range a.via {
			if wt > best || (wt == best && via < u.Via) {
				best, u.Via = wt, via
			}
		}
		devs := make([]AppDevice, 0, len(a.devices))
		for ip, d := range a.devices {
			dv := AppDevice{IP: ip, RxBytes: whole(d.rx), TxBytes: whole(d.tx)}
			if nameOf != nil {
				dv.Name, dv.MAC = nameOf(ip)
			}
			devs = append(devs, dv)
		}
		sort.Slice(devs, func(i, j int) bool {
			x, y := devs[i].RxBytes+devs[i].TxBytes, devs[j].RxBytes+devs[j].TxBytes
			if x != y {
				return x > y
			}
			return devs[i].IP < devs[j].IP
		})
		if len(devs) > appsDevicesPerApp {
			devs = devs[:appsDevicesPerApp]
		}
		if len(devs) > 0 {
			u.Top = firstNonEmptyStr(devs[0].Name, devs[0].IP)
		}
		out.Devices[id] = devs
		out.Apps = append(out.Apps, u)
	}
	sort.Slice(out.Apps, func(i, j int) bool {
		if out.Apps[i].TotalBytes != out.Apps[j].TotalBytes {
			return out.Apps[i].TotalBytes > out.Apps[j].TotalBytes
		}
		return out.Apps[i].Name < out.Apps[j].Name
	})
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
//
// ── BOUNDED, BECAUSE A DEVICE CHOOSES WHAT IS IN THE CACHE ──────────────────
//
// Any device on the network can make the router cache records from a domain it
// controls, including thousands of CNAMEs pointing at one name. Unbounded,
// that fan-in would be walked again for every address record of the target and
// every name kept, once a minute. So: at most dnsAliasesPerTarget aliases are
// remembered per name, at most dnsNamesPerAddr names per address (one set per
// address, shared across all of its records), and dnsNamesBudget names in all.
// Classification needs one name that matches; a handful is plenty.
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
			if len(aliasesOf[t]) < dnsAliasesPerTarget {
				aliasesOf[t] = append(aliasesOf[t], name)
			}
		case "A", "AAAA":
			addrs = append(addrs, addr{name, canonIP(data)})
		}
	}
	out := map[string][]string{}
	seenFor := map[string]map[string]bool{}
	budget := dnsNamesBudget
	for _, a := range addrs {
		seen := seenFor[a.ip]
		if seen == nil {
			seen = map[string]bool{}
			seenFor[a.ip] = seen
		}
		queue := []string{a.name}
		for depth := 0; len(queue) > 0 && depth < 16; depth++ {
			var next []string
			for _, n := range queue {
				if seen[n] || len(out[a.ip]) >= dnsNamesPerAddr || budget <= 0 {
					continue
				}
				seen[n] = true
				out[a.ip] = append(out[a.ip], n)
				budget--
				next = append(next, aliasesOf[n]...)
			}
			queue = next
		}
	}
	return out
}

const (
	dnsAliasesPerTarget = 64
	dnsNamesPerAddr     = 32
	dnsNamesBudget      = 200000
)

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
	window   *appsWindow
	last     *AppUsagePayload
	onMinute func(AppMinute)

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
		prev:   map[string]bwPrev{}, window: newAppsWindow(),
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

// OnMinute registers a callback for every minute the window closes: the
// stored history adds its bytes to the hour. Called outside the lock.
func (a *Apps) OnMinute(fn func(AppMinute)) *Apps { a.onMinute = fn; return a }

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

// build folds one reading into the window, and sends the window when a minute
// closed (or, once, while the first minute fills).
func (a *Apps) build(rows []routeros.Reply, now int64) {
	a.refreshNames()
	a.dnsMu.Lock()
	names, dnsAt := a.names, a.dnsAt
	a.dnsMu.Unlock()

	a.mu.Lock()
	rates := connRates(a.prev, rows, now)
	sum := SumApps(AppsInput{Rates: rates, LanCidrs: lanCidrsOf(a.nets), Names: names, Owner: a.owner, Cat: a.cat})
	closed, end := a.window.add(sum, now, a.pollMs.ms())
	var p *AppUsagePayload
	if end != 0 {
		p = BuildApps(a.window, end, now, a.nameOf)
		p.DNSNames = len(names)
		if !dnsAt.IsZero() {
			p.DNSAt = dnsAt.UnixMilli()
		}
		a.last = p
	}
	cb := a.onMinute
	a.mu.Unlock()

	if cb != nil {
		for _, m := range closed {
			cb(m)
		}
	}
	if p != nil {
		EvAppsUpdate.Emit(a.emit, appsRooms.Join(), *p)
	}
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

// Last is the window last sent, nil before the first.
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
// router rebooted, every connection id is new and its cache is empty. The
// window stays: the minutes in it were measured, and the gap cap keeps the
// reconnect from adding any.
func (a *Apps) Reconnected() {
	a.sched.end()
	a.mu.Lock()
	a.prev = map[string]bwPrev{}
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
