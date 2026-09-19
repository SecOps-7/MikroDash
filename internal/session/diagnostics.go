package session

// What the collector layers are doing, for the API Diagnostics card.
//
// ── IT ASKS THE ROUTER NOTHING ─────────────────────────────────────────────
//
// Every number here is already in this process: counters the command path keeps,
// the cache's own subscription set, the session's collector table, and the hub's
// room occupancy. Opening the card costs one payload every couple of seconds and
// not one RouterOS command, which is the whole point of a diagnostics card — an
// instrument that changes what it measures is worthless.
//
// ── IT IS SHAPED LIKE THE ARCHITECTURE, NOT LIKE THE CODE ──────────────────
//
// Three sections, one per layer, because that is what a reader is trying to
// understand: how much is being asked of the router, how much is being turned
// into payloads, and who is listening. See `Collector-Architecture.md`.
//
//	acquisition  what leaves this process for the router
//	derivation   what those rows are turned into
//	views        who is listening, and therefore what runs at all
//
// A number belonging to no layer does not go here. The card is not a place to
// put whatever happens to be countable.

import (
	"sort"
	"sync"
	"time"

	"mikrodash/internal/collect"
	"mikrodash/internal/roslimit"
)

// diagMenus is how many menus the card lists. Eleven is what one router with a
// viewer on the Dashboard actually subscribed on 2026-09-10, so the cap is set
// just above it: the common case lists in full and a busy one is TOLD it was
// truncated rather than quietly shortened. Silent truncation on an instrument is
// the same defect as a wrong number.
const diagMenus = 12

// Diagnostics is the card's payload.
type Diagnostics struct {
	TS       int64  `json:"ts"`
	RouterID string `json:"routerId"`
	Label    string `json:"label"`

	Acquisition AcqLayer  `json:"acquisition"`
	Derivation  DerLayer  `json:"derivation"`
	Views       ViewLayer `json:"views"`
}

// AcqLayer is layer one: what leaves this process for the router.
type AcqLayer struct {
	// CommandsPerMin is the rolling minute, and the headline: it is what this
	// app costs the device.
	CommandsPerMin int64 `json:"commandsPerMin"`
	// InFlight is commands holding a slot right now, against the cap.
	InFlight int `json:"inFlight"`
	Cap      int `json:"cap"`
	// Channels is open push channels. A channel costs no commands, which is why
	// it sits beside the command rate rather than folded into it.
	Channels int `json:"channels"`
	// Menus is how many RouterOS menus are subscribed, split by how they are
	// delivered. Streamed + Polled == Menus.
	Menus    int `json:"menus"`
	Streamed int `json:"streamed"`
	Polled   int `json:"polled"`
	// Reads names the menus themselves, which is the one thing on this card that
	// says WHAT is being asked rather than how much.
	//
	// ── IT USED TO BE "THE BUSIEST", AND THAT WAS A GUESS ──────────────────
	//
	// It first listed the menus with the most SUBSCRIBERS, on the assumption
	// that several collectors routinely share one read. Measured on the live
	// fleet across seven pages on 2026-09-10: no menu ever had more than one.
	// Most coalescing in this app happens a level up — one collector owns a
	// menu and the others read its derived index, which is what `arp` and
	// `dhcpLeases` are for. The one subscription-level share, `conns` and
	// `bandwidth` on the connection table, needs both wanted at once, so the
	// section would almost never have rendered. Listing the menus is the true
	// version of what it was reaching for.
	Reads []MenuLoad `json:"reads"`
	// More is how many menus the list left out. Zero means it is complete.
	More int `json:"more"`

	// Sources splits CommandsPerMin by who asked: the collectors, or a feature
	// such as the Security Scan, the Apps tab, the Tools page or the AI agent.
	// Their sum is CommandsPerMin exactly; both are read from one snapshot.
	//
	// ── WHY IT EXISTS ───────────────────────────────────────────────────────
	//
	// `Reads` names what the COLLECTORS subscribe to, and nothing else on the
	// card said where a command came from. A scan's thirty-seven reads raised the
	// headline with nothing to account for them, and the Tools page's runs were
	// not in the headline at all (sources.go).
	Sources []SourceLoad `json:"sources"`
	// Busiest is the menus that were sent the most commands in the last minute,
	// with who asked: what was ACTUALLY sent, where Reads is what is wanted.
	Busiest []MenuRate `json:"busiest"`
	// BusiestMore is how many rows the Busiest list left out.
	BusiestMore int `json:"busiestMore"`
}

// SourceLoad is one source's commands in the last minute.
type SourceLoad struct {
	Source string `json:"source"`
	PerMin int64  `json:"perMin"`
}

// MenuRate is one menu's commands in the last minute, from one source.
type MenuRate struct {
	Menu   string `json:"menu"`
	Source string `json:"source"`
	PerMin int64  `json:"perMin"`
}

// MenuLoad is one subscribed menu.
type MenuLoad struct {
	Menu     string `json:"menu"`
	Streamed bool   `json:"streamed"`
}

// DerLayer is layer two: rows turned into payloads.
type DerLayer struct {
	// PayloadsPerMin is how many payloads the collectors built and emitted in
	// the last minute. It is the work done ON the rows, as distinct from the
	// commands that fetched them.
	PayloadsPerMin int64 `json:"payloadsPerMin"`
}

// ViewLayer is layer three: who is listening.
type ViewLayer struct {
	// Running and Gated are collectors demand can start and stop; Running is how
	// many it currently wants.
	Running int `json:"running"`
	Gated   int `json:"gated"`
	// Dormant is collectors the supervisor has put to sleep for reporting
	// nothing. They are gated collectors that are not running.
	Dormant int `json:"dormant"`
	// Rooms is how many of this router's rooms have a viewer in them, which is
	// the input the demand rule actually reads.
	Rooms int `json:"rooms"`
	// Holds are the non-viewer reasons this session is alive. Empty means a
	// browser is the only thing keeping it.
	Holds []string `json:"holds"`
}

// notePayload counts one emitted payload. Called from the session's single emit
// closure, which is why there is one call site rather than one per collector.
func (s *Session) notePayload() {
	if s == nil {
		return
	}
	s.payloads.add()
}

// Diagnostics assembles the card's payload. It reads only in-process state.
func (s *Session) Diagnostics(now int64) Diagnostics {
	if s == nil {
		return Diagnostics{TS: now}
	}
	d := Diagnostics{TS: now, RouterID: s.RouterID, Label: s.Label}

	// ── LAYER 1: WHAT LEAVES THIS PROCESS ──────────────────────────────────
	d.Acquisition = AcqLayer{
		InFlight: roslimit.InFlight(s.RouterID),
		Cap:      roslimit.Cap(),
		Channels: roslimit.OpenStreams(s.RouterID),
		Reads:    []MenuLoad{},
		Sources:  []SourceLoad{},
		Busiest:  []MenuRate{},
	}
	d.Acquisition.CommandsPerMin, d.Acquisition.Sources, d.Acquisition.Busiest,
		d.Acquisition.BusiestMore = commandLoad(roslimit.Load(s.RouterID))
	if s.roscache != nil {
		streamed := map[string]bool{}
		for _, m := range s.roscache.StreamedMenus() {
			streamed[m] = true
		}
		for _, dem := range s.roscache.Demand() {
			d.Acquisition.Menus++
			if streamed[dem.Menu] {
				d.Acquisition.Streamed++
			} else {
				d.Acquisition.Polled++
			}
			d.Acquisition.Reads = append(d.Acquisition.Reads, MenuLoad{
				Menu: dem.Menu, Streamed: streamed[dem.Menu],
			})
		}
		// PUSHED FIRST, then by name. Pushed first because those are the reads
		// holding a channel — the resource the architecture document names as the
		// bottleneck — and by name so two ticks of an unchanged fleet produce the
		// same card rather than a list that shuffles every two seconds.
		sort.SliceStable(d.Acquisition.Reads, func(i, j int) bool {
			a, b := d.Acquisition.Reads[i], d.Acquisition.Reads[j]
			if a.Streamed != b.Streamed {
				return a.Streamed
			}
			return a.Menu < b.Menu
		})
		if len(d.Acquisition.Reads) > diagMenus {
			d.Acquisition.More = len(d.Acquisition.Reads) - diagMenus
			d.Acquisition.Reads = d.Acquisition.Reads[:diagMenus]
		}
	}

	// ── LAYER 2: WHAT THE ROWS BECOME ──────────────────────────────────────
	d.Derivation = DerLayer{PayloadsPerMin: s.payloads.perMin()}

	// ── LAYER 3: WHO IS LISTENING ──────────────────────────────────────────
	v := ViewLayer{Gated: len(targetKeys), Holds: []string{}}
	for _, key := range targetKeys {
		if s.Wants(key) {
			v.Running++
		}
	}
	v.Dormant = len(s.DormantCollectors())

	// EVERY ROOM ONCE. Collectors share rooms — `page-dashboard` is claimed by
	// four — and counting per collector would report a viewer on the Dashboard
	// as four occupied rooms.
	seen := map[string]bool{}
	for _, key := range targetKeys {
		for _, r := range collect.DemandRooms(key) {
			if r == "" || seen[r] {
				continue
			}
			seen[r] = true
			if s.roomsOccupied(collect.Rooms{r}) {
				v.Rooms++
			}
		}
	}

	s.mu.Lock()
	for _, r := range []string{"alerts", "history", "devices", "warm"} {
		if s.holds[r] {
			v.Holds = append(v.Holds, r)
		}
	}
	s.mu.Unlock()
	d.Views = v
	return d
}

// diagBusiest is how many rows the Busiest list keeps. Ten is a dashboard
// card's worth; the rest are counted in BusiestMore, never dropped silently.
const diagBusiest = 10

// commandLoad folds a router's last minute into the headline, the per-source
// split and the busiest menus. One snapshot feeds all three, so they agree.
//
// Both lists are ordered busiest first, then by name, so an unchanged minute
// paints the same card twice rather than shuffling it.
func commandLoad(rows []roslimit.Rate) (total int64, sources []SourceLoad, busiest []MenuRate, more int) {
	by := map[string]int64{}
	busiest = make([]MenuRate, 0, len(rows))
	for _, r := range rows {
		total += r.PerMin
		by[r.Source] += r.PerMin
		busiest = append(busiest, MenuRate{Menu: r.Menu, Source: r.Source, PerMin: r.PerMin})
	}
	sources = make([]SourceLoad, 0, len(by))
	for src, n := range by {
		sources = append(sources, SourceLoad{Source: src, PerMin: n})
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].PerMin != sources[j].PerMin {
			return sources[i].PerMin > sources[j].PerMin
		}
		return sources[i].Source < sources[j].Source
	})
	sort.Slice(busiest, func(i, j int) bool {
		a, b := busiest[i], busiest[j]
		if a.PerMin != b.PerMin {
			return a.PerMin > b.PerMin
		}
		if a.Menu != b.Menu {
			return a.Menu < b.Menu
		}
		return a.Source < b.Source
	})
	if len(busiest) > diagBusiest {
		more = len(busiest) - diagBusiest
		busiest = busiest[:diagBusiest]
	}
	return total, sources, busiest, more
}

// rollingMin counts events over the last minute, in one-second buckets.
//
// ── THE SAME SHAPE AS `roslimit`'s COMMAND RATE, AND DELIBERATELY SO ───────
//
// A rate is what a card can read; a total since an unstated moment is not. Each
// bucket carries the second it belongs to, so a session that goes quiet ages out
// instead of holding its last reading for ever.
//
// It is NOT shared with roslimit's copy: that one counts router commands and
// lives in the package that owns the command path, and importing a counter
// across that boundary to save fifteen lines would put a session's payload count
// in a package about RouterOS concurrency limits.
type rollingMin struct {
	mu  sync.Mutex
	sec [rollingWindow]int64
	n   [rollingWindow]int64
}

const rollingWindow = 60

func (r *rollingMin) add() {
	now := time.Now().Unix()
	i := now % rollingWindow
	r.mu.Lock()
	if r.sec[i] != now {
		r.sec[i], r.n[i] = now, 0
	}
	r.n[i]++
	r.mu.Unlock()
}

func (r *rollingMin) perMin() int64 {
	cut := time.Now().Unix() - rollingWindow
	r.mu.Lock()
	defer r.mu.Unlock()
	total := int64(0)
	for i := range r.sec {
		if r.sec[i] > cut {
			total += r.n[i]
		}
	}
	return total
}
