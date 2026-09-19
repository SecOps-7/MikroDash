package collect

// Interface status — the port of src/collectors/interfaceStatus.js.
//
// POLLED HERE, STREAMED THERE, AND THAT IS THE POINT. The Node collector holds
// three `=interval=N` channels open for its metadata (/interface, /ip/address,
// /interface/ethernet) and a fourth for rates. This reads the same four menus
// with ordinary prints on one connection. CLAUDE.md is explicit that "more
// efficient means fewer router channels, not faster payload assembly", and
// src/collection.js records the evidence: "the evidence in #104 points at
// concurrent open channels rather than data volume". Four fewer channels per
// router is the single biggest saving available in this port, and the payload is
// identical either way — a `=interval=N` print re-sends the same rows a plain
// print returns.
//
// The mechanism changed; the payload did not. That is the line CLAUDE.md draws.
//
// AND IT BUYS COMPLETENESS, WHICH IS NOT WHY IT WAS DONE. A `Do` returns a reply
// set the protocol itself terminates with `!done`; a persistent `=interval=N`
// stream hands over packets with no boundary the caller can see. So a delimited
// read is a COMPLETENESS GUARANTEE and a stream is not — which is a second and
// stronger reason for this departure than the channel count that motivated it.
//
// That is not hypothetical. The live app hit it as issue #119: its streaming
// version of this collector used a 300ms debounce to decide a cycle had ended,
// a debounce measures silence rather than completeness, and one mid-cycle gap
// installed a partial interface list as the whole truth — the traffic dropdown
// lost all but one interface on a CCR2004. This collector cannot have that bug,
// and not by foresight.
//
// IF THAT EVER CHANGES, THE FIX IS IN THE ADAPTER, NOT HERE. `internal/routeros`
// ends a Stream when go-routeros closes the channel, which it does on `!done` —
// so an `=interval=N` read would look like a stream that ended after one cycle.
// Adopting streaming for these menus means first surfacing the per-cycle `!done`
// as an event rather than an ending.
//
// RATES COME FROM THE ROUTER, NOT FROM DIFFERENCING BYTE COUNTERS.
// /interface/monitor-traffic reports rx-bits-per-second directly, and it can
// only be asked once the interface list is known — so the order within a tick is
// metadata first, rates second.
//
// ── THE FOUR READS ARE SPLIT, BECAUSE ONLY ONE OF THEM MOVES ────────────────
//
// This collector was four commands on every tick, and its tick is the fastest
// in the app. Measured against one idle CHR at the default cadence: 267 commands
// a minute reached the router, and 204 of them — 76% — were these four. That is
// what MikroDash costs a router when nobody is doing anything.
//
// Three of the four answer questions that do not change at that speed. An
// interface's name, type, comment, MAC, addresses and driver error counters do
// not move between one second and the next; asking 51 times a minute buys
// nothing. Only /interface/monitor-traffic answers a question whose whole value
// is that it is current.
//
// So the metadata reads are re-run every `metaTicks` polls and the rates read
// every poll, and a tick publishes the held metadata with the rates it just
// fetched. `metaTicks` targets `ifStatusMetaTarget` and is DERIVED from the poll
// interval rather than configured, so an operator who slows this collector down
// never gets metadata faster than rates, and one who speeds it up does not
// multiply the metadata cost with it.
//
// WHAT IS TRADED: `running`, `disabled` and the counters are up to
// `ifStatusMetaTarget` late. A link that drops is noticed within that window
// rather than within a poll — on the Interfaces page, in the sidebar badge and
// in the traffic picker. Rates, which are the thing anybody actually watches
// move, are unchanged. See the constant for what the operator set it to and why.
//
// A POLL INTERVAL AT OR ABOVE THE TARGET COLLAPSES THIS BACK TO ONE LANE, which
// is not a coincidence to lean on: the frozen golden replay constructs this
// collector at 30s, so it takes that branch and reads all four menus on every
// tick exactly as before. The split is therefore NOT covered by the golden, and
// `TestIfStatusSplitsTheMetadataReads` is what covers it instead.

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

// ifStatusMetaTarget is how often the three metadata menus are re-read, and
// therefore how late a link state change can be.
//
// THIRTY SECONDS, SET BY THE OPERATOR (2026-09-08): "monitor-traffic needs to
// tick every second, but the other three can be polled at let's say a 30 second
// cadence or longer". It was five while the split was being proved out.
//
// WHAT THAT COSTS, stated once so nobody rediscovers it as a bug: `running`,
// `disabled`, the addresses and the counters are up to 30 seconds old. A port
// that goes down is late on the Interfaces page, on the sidebar badge and in the
// traffic chart's picker, and an interface created on the router takes that long
// to appear. Rates are not affected — they are the other lane.
//
// AND `running` CANNOT BE MOVED TO THE FAST LANE, which was checked rather than
// assumed: `/interface/monitor-traffic` was run against live hardware and returns
// only rates and per-second packet, drop and error counts. There is no link state
// in it, so keeping link state current would mean a fifth command, which is the
// cost this split exists to avoid.
const ifStatusMetaTarget = 30 * time.Second

const ifCounterProps = "rx-byte,tx-byte,rx-error,tx-error,rx-drop,tx-drop," +
	"tx-queue-drop,link-downs,last-link-up-time"

var (
	ifStatusIfCmd = routeros.Cmd{Path: "/interface/print", Args: []string{
		"=.proplist=.id,name,type,running,disabled,comment,mac-address," + ifCounterProps}}
	ifStatusAddrCmd = routeros.Cmd{Path: "/ip/address/print",
		Args: []string{"=.proplist=interface,address"}}
	ifStatusEthCmd = routeros.Cmd{Path: "/interface/ethernet/print", Args: []string{
		"=.proplist=name,rx-fcs-error,rx-align-error,rx-fragment,rx-overflow," +
			"rx-too-short,rx-too-long,tx-underrun,tx-late-collision,tx-excessive-collision"}}
)

var (
	ifErrFields  = []string{"rx-error", "tx-error"}
	ifDropFields = []string{"rx-drop", "tx-drop", "tx-queue-drop"}
	ethErrFields = []string{
		"rx-fcs-error", "rx-align-error", "rx-fragment", "rx-overflow",
		"rx-too-short", "rx-too-long",
		"tx-underrun", "tx-late-collision", "tx-excessive-collision",
	}
)

// Interface is one row of the interfaces payload.
type Interface struct {
	// ID is the RouterOS `.id`, so the Interfaces page can edit the row (#97).
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Running  bool     `json:"running"`
	Disabled bool     `json:"disabled"`
	Comment  string   `json:"comment"`
	MacAddr  string   `json:"macAddr"`
	RxMbps   float64  `json:"rxMbps"`
	TxMbps   float64  `json:"txMbps"`
	IPs      []string `json:"ips"`
	// Cumulative counters. null means the interface does not report the counter
	// at all, which the list view renders as a dash rather than 0.
	RxBytes    *float64 `json:"rxBytes"`
	TxBytes    *float64 `json:"txBytes"`
	Errors     *float64 `json:"errors"`
	Drops      *float64 `json:"drops"`
	LinkDowns  *float64 `json:"linkDowns"`
	LastLinkUp string   `json:"lastLinkUp"`
	// Movement over the last window, null until a baseline exists.
	ErrorsDelta   *float64 `json:"errorsDelta"`
	DropsDelta    *float64 `json:"dropsDelta"`
	DeltaWindowMs *float64 `json:"deltaWindowMs"`
}

// IfStatusPayload is the ifstatus:update body.
type IfStatusPayload struct {
	TS         int64       `json:"ts"`
	RouterID   string      `json:"routerId"`
	Interfaces []Interface `json:"interfaces"`
}

// jsNum is `num()` on the Node side: undefined, null and "" are null; anything
// non-finite is null; everything else is the number.
func jsNum(row routeros.Reply, key string) *float64 {
	v, ok := row[key]
	if !ok || v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return nil
	}
	return &f
}

// sumCounters totals the fields a row actually reports. It stays null when the
// row reports NONE of them — "this interface has no error counters" is a
// different statement from "it has zero errors", and the page renders them
// differently.
func sumCounters(row routeros.Reply, fields []string) *float64 {
	var total *float64
	for _, f := range fields {
		n := jsNum(row, f)
		if n == nil {
			continue
		}
		if total == nil {
			z := 0.0
			total = &z
		}
		*total += *n
	}
	return total
}

// deltaOf is the movement between two readings, clamped at zero: a counter that
// went backwards was reset, and a negative delta would render as a fault that
// did not happen.
func deltaOf(prev, cur *float64) *float64 {
	if prev == nil || cur == nil {
		return nil
	}
	d := 0.0
	if *cur >= *prev {
		d = *cur - *prev
	}
	return &d
}

type counterSnap struct {
	errors *float64
	drops  *float64
	ts     time.Time
}

type counterDelta struct {
	errors   *float64
	drops    *float64
	windowMs float64
}

// IfStatus is the collector. It also serves as the RateSource for Bridges,
// VLANs and WAN — see Rates.
type IfStatus struct {
	ros      Reader
	emit     Emit
	routerID string
	pollMs   *pollInterval
	// cache coalesces reads shared with another collector. Nil outside a live
	// session, which is every test — see collect/cache.go.
	cache *roscache.Cache
	// rateStop releases the monitor-traffic channel, and rateKey is the
	// interface list it was opened with. See syncRateChannel.
	//
	// ── THEIR OWN MUTEX, AND NOT `mu`, WHICH IS A DEADLOCK ──────────────────
	//
	// `Tick` holds `mu` for its whole body and `syncRateChannel` is called from
	// inside it, so sharing the lock deadlocks on the first tick -- which is
	// exactly what it did: the collect suite hung rather than failed, and a hang
	// says less about its cause than a failure does.
	rateMu   sync.Mutex
	rateStop func()
	rateKey  string
	// sched subscribes the METADATA menus and keeps the loop as the residual half
	// for the rates measurement. Mechanism A; see scheduled.go.
	sched scheduled

	poll *pollLoop

	mu    sync.Mutex
	prev  map[string]counterSnap
	delta map[string]counterDelta

	// The metadata half, held between refreshes. `base` is the payload's
	// interface list with every field EXCEPT the rates filled in, so a tick that
	// only fetched rates still publishes a complete row. `ifRows` is kept beside
	// it because the rates read needs the raw name/disabled columns.
	//
	// base nil means "metadata has never been read", which is also what a
	// reconnect leaves behind; metaIn counts polls down to the next refresh.
	base   []Interface
	ifRows []routeros.Reply
	metaIn int

	last       *IfStatusPayload
	lastErr    string
	lastFp     string
	lastEmitAt time.Time
}

// NewIfStatus builds the collector.
func NewIfStatus(ros Reader, emit Emit, routerID string, pollMs int) *IfStatus {
	s := &IfStatus{
		ros: ros, emit: emit, routerID: routerID,
		// Node calls clampPoll(pollMs, 5000) here and takes the DEFAULT bounds,
		// which floor at 500ms — not the 2000 the other collectors pass
		// explicitly. A 1s setting is honoured there and would have been floored
		// to 2s here, so the port would have polled at half the rate the
		// operator asked for, on the one collector whose whole job is rates.
		pollMs: newPollInterval(clampPoll(pollMs, 5000, 500, 60000)),
		prev:   map[string]counterSnap{},
		delta:  map[string]counterDelta{},
	}
	s.poll = newPollLoop(func() { s.Tick() }, func() time.Duration {
		return s.pollMs.duration()
	})
	// MECHANISM A. The loop is not a fallback here, it is the other half: it
	// drives the rates measurement, which is set B, while the subscription drives
	// the three metadata menus. The two never read the same menu -- monitor-traffic
	// against /interface, /ip/address and /interface/ethernet -- which is the rule
	// that makes two clocks safe. See scheduled.go.
	s.sched = scheduled{
		loop: s.poll, residual: true,
		menu: ifStatusIfCmd.Path, fields: fieldsOf(ifStatusIfCmd), apply: s.applyMeta,
		cadence: func() time.Duration {
			return time.Duration(s.metaTicks()) * s.pollMs.duration()
		},
	}
	return s
}

func (s *IfStatus) read(cmd routeros.Cmd) []routeros.Reply {
	rows, err := s.ros.Do(cmd)
	if err != nil {
		if !menuMissing(err) {
			s.lastErr = err.Error()
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

// rates asks the router for throughput on the interfaces it just learned about.
//
// Disabled interfaces are excluded — and the filter is written the long way on
// purpose. RouterOS sends `disabled` as the STRING "false", which is truthy in
// JavaScript, so the Node side's first attempt at `!iface.disabled` excluded
// EVERY interface and rates sat at zero forever. Go has no such trap, but the
// comparison is spelled out so the next reader does not "simplify" it back.
func (s *IfStatus) rates(ifaces []routeros.Reply) map[string]Rate {
	names := rateNames(ifaces)
	out := map[string]Rate{}
	if len(names) == 0 {
		return out
	}
	// ── B.7: FROM THE CHANNEL WHEN THERE IS ONE ─────────────────────────────
	//
	// This measurement is the most expensive single thing the app asks a router:
	// roughly 52 commands a minute, and after B.4 moved fourteen menus onto
	// channels it is the largest polled item left by a wide margin.
	//
	// `traffic` already holds a channel on this exact menu, and B.0 measured
	// what widening it to every interface costs: 2.6 KB/s, ONE channel either
	// way, no measurable router CPU. So the rates can be READ from a channel
	// somebody is holding anyway, and this command need not be issued at all.
	//
	// THE FALLBACK IS NOT A COURTESY. The menu cannot be polled by the cache's
	// ordinary read path -- a bare `/interface/monitor-traffic` with no `=once=`
	// and no `=interval=` is not a query, it is a request the router will not
	// answer. So when no channel is warm this MUST take its own measurement, and
	// that is the path a session with no cache, a router that refused the
	// stream, and every tick before the first round all take.
	if rates, ok := s.ratesFromChannel(names); ok {
		return rates
	}

	// ── SET B: A MEASUREMENT, NOT A QUERY ───────────────────────────────────
	//
	// See acquisition.go. The once argument makes this a reading taken at an
	// instant, on a named set of interfaces -- so it has no cacheable answer,
	// and none of phase 1 applies to it.
	rows, err := s.ros.Do(routeros.Cmd{Path: "/interface/monitor-traffic", Args: []string{
		"=interface=" + strings.Join(names, ","),
		"=once=",
		"=.proplist=name,rx-bits-per-second,tx-bits-per-second",
	}})
	if err != nil {
		s.lastErr = err.Error()
		return out
	}
	for _, r := range rows {
		if r["name"] == "" {
			continue
		}
		out[r["name"]] = rateOf(r)
	}
	return out
}

// rateNames is the interfaces worth asking about: named, and not administratively
// disabled. Shared by the measurement, the channel and the channel's own command,
// so all three cannot disagree about which interfaces are in play.
func rateNames(ifaces []routeros.Reply) []string {
	names := make([]string, 0, len(ifaces))
	for _, i := range ifaces {
		if i["name"] == "" || i["disabled"] == "true" {
			continue
		}
		names = append(names, i["name"])
	}
	return names
}

// rateOf reads one monitor-traffic row. Shared by the measured path and the
// channel path so the two cannot render the same row differently.
func rateOf(r routeros.Reply) Rate {
	rx, tx := bpsToMbps(r["rx-bits-per-second"]), bpsToMbps(r["tx-bits-per-second"])
	return Rate{RxMbps: &rx, TxMbps: &tx}
}

// bpsToMbps matches parseBps + bpsToMbps: bits per second to Mbps, rounded to
// three decimals, which is the precision the payload has always carried.
func bpsToMbps(v string) float64 {
	if v == "" || v == "0" {
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0
	}
	return round3(f / 1e6)
}

func round3(f float64) float64 {
	r, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'f', 3, 64), 64)
	return r
}

// metaTicks is how many polls apart the three metadata reads are.
//
// DERIVED, NOT CONFIGURED. There is no second interval for an operator to set:
// the metadata cadence is the poll interval rounded to the target, so the two can
// never be tuned into disagreeing. A poll at or above the target returns 1 and
// every tick reads all four menus, which is exactly the behaviour this collector
// had before the split.
//
// THERE IS NO CAP ON THE COUNT, and one was removed rather than raised. It read
// "a very fast poll must not stretch the metadata over an unbounded number of
// ticks" — but the bound that matters is TIME, and the target already is it. A
// tick cap could only ever pull the metadata cadence BELOW the target, which is
// the opposite of what it was there for: at a 1s poll and a 30s target it would
// have quietly delivered 8 seconds.
func (s *IfStatus) metaTicks() int {
	p := s.pollMs.duration()
	if p <= 0 {
		return 1
	}
	n := int((ifStatusMetaTarget + p/2) / p) // nearest, not floor
	if n < 1 {
		return 1
	}
	return n
}

// Tick publishes a payload every poll. It fetches the RATES every time and the
// three metadata menus only every metaTicks — see the split note in the header.
func (s *IfStatus) Tick() {
	if !s.ros.Connected() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// ── THE METADATA HALF IS THE SCHEDULER'S WHEN THERE IS ONE ──────────────
	//
	// Scheduled, this loop drives ONLY the rates measurement -- which is set B and
	// can never be scheduled -- and the three metadata menus arrive through the
	// subscription. Unscheduled, the countdown below is what paces them, exactly
	// as before. See `residual` in scheduled.go, and the rule that the two halves
	// must not read the same menu: they do not, monitor-traffic against the other
	// three.
	if !s.sched.scheduling() {
		if s.base == nil || s.metaIn <= 0 {
			s.refreshMeta()
			s.metaIn = s.metaTicks()
		}
		s.metaIn--
	}

	if len(s.base) == 0 {
		return // nothing to build from; do not publish an empty interface list
	}

	// ── B.7: KEEP THE CHANNEL POINTED AT THE CURRENT INTERFACE SET ──────────
	//
	// Before the rates are wanted, not after: a channel opened for a set that no
	// longer matches would answer for interfaces that have gone and miss ones
	// that have arrived, and `rates` would fall back to measuring for the ones it
	// could not find -- which is correct but is the cost this step removes.
	s.syncRateChannel(rateNames(s.ifRows))

	// The fast half, and on most ticks it issues NO command at all: the rates
	// come from a channel. See rates and ratesFromChannel.
	rateBy := s.rates(s.ifRows)

	// Copied rather than written through, because `base` outlives the tick and
	// a rate written into it would still be there when the router stopped
	// reporting one — the interface would hold its last speed for ever.
	interfaces := make([]Interface, len(s.base))
	copy(interfaces, s.base)
	for i := range interfaces {
		rt, ok := rateBy[interfaces[i].Name]
		if !ok {
			continue
		}
		if rt.RxMbps != nil {
			interfaces[i].RxMbps = *rt.RxMbps
		}
		if rt.TxMbps != nil {
			interfaces[i].TxMbps = *rt.TxMbps
		}
	}

	now := time.Now()
	payload := &IfStatusPayload{TS: now.UnixMilli(), RouterID: s.routerID, Interfaces: interfaces}
	s.last = payload

	// Byte totals are deliberately absent: they creep up even on an idle link
	// (broadcast traffic), so including them would defeat the suppression this
	// exists for. Errors, drops and flap counts are in — they hold steady on a
	// healthy link, so any movement is worth pushing at once. type, comment and
	// MAC are in for the opposite reason: they never move on their own, so they
	// cost nothing, and leaving them out meant an edit to one never reached an
	// open page.
	// A HEARTBEAT EVEN WHEN NOTHING MOVED. Suppressing an identical payload for
	// ever leaves the browser unable to tell an idle interface from a dead
	// collector, and the page's staleness overlay fires on a link that is
	// simply quiet. Sixty seconds, matching the original.
	fp := ifStatusFingerprint(interfaces)
	if fp == s.lastFp && now.Sub(s.lastEmitAt) < ifStatusHeartbeat {
		return
	}
	s.lastFp, s.lastEmitAt = fp, now

	// SPLIT DELIVERY, and the split is an authorisation boundary.
	//
	// The full payload carries per-interface rates, IP addresses and MAC
	// addresses, so it reaches only the pages that render them: Interfaces,
	// Topology (link rates) and the dashboard's ports card. One copy per
	// viewer — a viewer can be in two of those rooms.
	//
	// The router-wide half carries NAMES AND UP/DOWN ONLY. That is exactly what
	// the traffic chart's interface picker and the sidebar badge need, and they
	// are chrome on every page — so it must not be withheld from a viewer who
	// has opened none of those three, and it must not disclose anything a denied
	// page would have shown.
	EvIfstatusUpdate.Emit(s.emit, ifStatusRooms.Join(), *payload)

	EvIfstatusNames.Emit(s.emit, "", *NamesOf(payload))
}

// refreshMeta re-reads the three metadata menus and rebuilds the interface list
// everything except the rates comes from.
//
// A FAILED READ KEEPS THE PREVIOUS METADATA rather than blanking the list. That
// is a change from the pre-split collector, which skipped the whole tick: rates
// now keep flowing over one interval of slightly older metadata instead of the
// page freezing entirely. The first read is the exception — there is nothing to
// keep, and `base` staying nil is what makes Tick decline to publish.
// RefreshNow re-reads the interface metadata at once, for a write path: after a
// comment or an enable/disable the page shows the router's answer instead of
// waiting out the metadata interval. The cached rows are dropped first, or the
// re-read would be served the state from before the write.
func (s *IfStatus) RefreshNow() {
	if !s.ros.Connected() {
		return
	}
	if s.cache != nil {
		s.cache.Invalidate(ifStatusIfCmd.Path)
	}
	s.mu.Lock()
	s.refreshMeta()
	s.metaIn = s.metaTicks()
	s.mu.Unlock()
	s.Tick()
}

// applyMeta is what the scheduler calls with the interface rows. The addresses
// and the ethernet counters are read here, as before -- see scheduled.go on why a
// collector subscribes to ONE menu and reads the rest itself.
func (s *IfStatus) applyMeta(ifRows []routeros.Reply, err error) {
	if err != nil || len(ifRows) == 0 {
		return // keep the previous metadata rather than blanking the page
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buildMeta(ifRows, s.read(ifStatusAddrCmd), s.read(ifStatusEthCmd))
}

func (s *IfStatus) refreshMeta() {
	// THROUGH THE CACHE: `wan` reads this same menu with a proplist that is a
	// strict subset of ours, so once either has fetched, the other costs nothing.
	// The TTL offered is this collector's METADATA interval, not its poll — the
	// cache keeps the shortest any consumer asks for, so offering the poll here
	// would pin the entry to a freshness this half no longer needs.
	ifRows, _ := readVia(s.cache, s.ros, ifStatusIfCmd, time.Duration(s.metaTicks())*s.pollMs.duration())
	s.buildMeta(ifRows, s.read(ifStatusAddrCmd), s.read(ifStatusEthCmd))
}

// buildMeta derives and stores the metadata half. The caller holds the lock.
func (s *IfStatus) buildMeta(ifRows, addrRows, ethRows []routeros.Reply) {
	base, snap, delta := BuildIfStatus(s.prev, IfStatusInput{
		Ifaces: ifRows, Addrs: addrRows, Eth: ethRows, Now: time.Now(),
	})
	if base == nil {
		return // nothing to build from; keep what we had
	}

	// Replaced rather than merged, so a renamed interface does not leave a stale
	// delta behind for a name that later gets reused.
	s.prev, s.delta = snap, delta
	s.ifRows, s.base = ifRows, base
}

// IfStatusInput is everything BuildIfStatus reads from the router.
type IfStatusInput struct {
	Ifaces []routeros.Reply // /interface
	Addrs  []routeros.Reply // /ip/address
	Eth    []routeros.Reply // /interface/ethernet
	Now    time.Time
}

// BuildIfStatus turns three menus into the interface list, and is the worked
// example for phase 4.1 of Collectors-Rewrite.md.
//
// ── PRIOR STATE IS A PARAMETER, NOT A RECEIVER ──────────────────────────────
//
// This derivation cannot be a function of its inputs alone: the error and drop
// figures the page shows are DELTAS, and a delta has nothing to subtract from
// without the previous reading. About a third of this app's collectors are like
// that — bandwidth, traffic, queues, ppp, vpn, routing, topology all carry
// something between ticks.
//
// The answer was already in the tree and had simply never been named as a rule:
// `BuildBandwidth(prev, in)` and `BuildQueueRows(rows, prev, now)` take prior
// state as an argument. So does this. THAT IS THE RULE for 4.1 — prior in,
// prior out, nothing held on a receiver — and it is what lets the derivation be
// tested by handing it two readings instead of driving a collector twice.
//
// Returns nil when there is nothing to build from, which the caller reads as
// "keep the metadata you have" rather than blanking the page.
func BuildIfStatus(prev map[string]counterSnap, in IfStatusInput) (
	[]Interface, map[string]counterSnap, map[string]counterDelta) {

	ifRows, addrRows, ethRows, now := in.Ifaces, in.Addrs, in.Eth, in.Now
	if len(ifRows) == 0 {
		return nil, nil, nil
	}

	addrs := map[string][]string{}
	for _, a := range addrRows {
		if a["interface"] == "" {
			continue
		}
		addrs[a["interface"]] = append(addrs[a["interface"]], a["address"])
	}
	eth := map[string]routeros.Reply{}
	for _, e := range ethRows {
		if e["name"] != "" {
			eth[e["name"]] = e
		}
	}

	snap := map[string]counterSnap{}
	delta := map[string]counterDelta{}
	base := make([]Interface, 0, len(ifRows))

	for _, r := range ifRows {
		name := r["name"]
		// Errors are the interface's own plus the ethernet driver's, when the
		// interface has an ethernet row. Either may be absent; null only when
		// BOTH are.
		errs := sumCounters(r, ifErrFields)
		if e, ok := eth[name]; ok {
			if phy := sumCounters(e, ethErrFields); phy != nil {
				if errs == nil {
					z := 0.0
					errs = &z
				}
				v := *errs + *phy
				errs = &v
			}
		}
		drops := sumCounters(r, ifDropFields)

		// The delta window is now the METADATA cadence, which is what it always
		// meant to be: differencing two readings of the same rows would report a
		// zero delta over a window no counter had a chance to move in.
		snap[name] = counterSnap{errors: errs, drops: drops, ts: now}
		if p, ok := prev[name]; ok {
			de, dd := deltaOf(p.errors, errs), deltaOf(p.drops, drops)
			if de != nil || dd != nil {
				delta[name] = counterDelta{errors: de, drops: dd,
					windowMs: float64(now.Sub(p.ts).Milliseconds())}
			}
		}

		typ := r["type"]
		if typ == "" {
			typ = "ether"
		}
		ips := addrs[name]
		if ips == nil {
			ips = []string{}
		}
		iface := Interface{
			ID: r[".id"], Name: name, Type: typ,
			Running: r["running"] == "true", Disabled: r["disabled"] == "true",
			Comment: r["comment"], MacAddr: r["mac-address"],
			IPs:        ips,
			RxBytes:    jsNum(r, "rx-byte"),
			TxBytes:    jsNum(r, "tx-byte"),
			Errors:     errs,
			Drops:      drops,
			LinkDowns:  jsNum(r, "link-downs"),
			LastLinkUp: r["last-link-up-time"],
		}
		if d, ok := delta[name]; ok {
			iface.ErrorsDelta, iface.DropsDelta = d.errors, d.drops
			w := d.windowMs
			iface.DeltaWindowMs = &w
		}
		base = append(base, iface)
	}

	return base, snap, delta
}

// NamesOf reduces a full interface payload to the names-and-state one.
//
// EXTRACTED so the emit above and the handshake replay in `ws.go` share one
// implementation. It was inline, and the replay — added when
// The initial-state audit found that a viewer attaching to a running
// session never received this — would otherwise have been a second copy of the
// same three-field projection, which is a defect with a delay fuse.
func NamesOf(payload *IfStatusPayload) *IfNamesPayload {
	if payload == nil {
		return nil
	}
	names := make([]IfName, 0, len(payload.Interfaces))
	for _, i := range payload.Interfaces {
		names = append(names, IfName{Name: i.Name, Type: i.Type, Running: i.Running, Disabled: i.Disabled})
	}
	return &IfNamesPayload{TS: payload.TS, Total: len(payload.Interfaces), Interfaces: names}
}

// ifStatusHeartbeat is how long an unchanged payload may be suppressed.
const ifStatusHeartbeat = 60 * time.Second

// IfName is one interface as the chrome sees it: enough to fill a picker and a
// badge, and nothing a page grant would have gated.
type IfName struct {
	Name string `json:"name"`
	// Type is RouterOS's interface type ("ether", "bridge", "vlan", ...). Added so
	// the Dashboard's Network Flow card can count wired ports from this event,
	// which every browser receives, rather than from `ifstatus:update`, which the
	// card never subscribes to (issue #132).
	Type     string `json:"type"`
	Running  bool   `json:"running"`
	Disabled bool   `json:"disabled"`
}

type IfNamesPayload struct {
	TS         int64    `json:"ts"`
	Total      int      `json:"total"`
	Interfaces []IfName `json:"interfaces"`
}

func round2(f float64) float64 {
	r, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'f', 2, 64), 64)
	return r
}

// Rates makes this collector the RateSource for Bridges, VLANs and WAN.
//
// The bool is "has this reported at all". Those pages render an em dash for
// "not reported" and 0.00 for idle, and conflating them makes a collector that
// has not started look like a quiet network.
func (s *IfStatus) Rates() (map[string]Rate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return nil, false
	}
	out := make(map[string]Rate, len(s.last.Interfaces))
	for _, i := range s.last.Interfaces {
		rx, tx := i.RxMbps, i.TxMbps
		out[i.Name] = Rate{RxMbps: &rx, TxMbps: &tx}
	}
	return out, true
}

// Last is the most recent payload, replayed on page:focus.
func (s *IfStatus) Last() *IfStatusPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *IfStatus) Start() {
	if s.ros.Connected() && !s.sched.scheduling() {
		s.Tick()
	}
	s.sched.begin()
}

func (s *IfStatus) Reconnected() {
	s.sched.end()
	s.mu.Lock()
	s.lastFp, s.lastEmitAt = "", time.Time{}
	// A reconnect may be a different router, and differencing a counter across
	// that boundary would report movement that never happened.
	s.prev = map[string]counterSnap{}
	s.delta = map[string]counterDelta{}
	// And the metadata, for the same reason: it describes the interfaces of a
	// connection that is gone. Nil is what makes the next tick re-read.
	s.base, s.ifRows, s.metaIn = nil, nil, 0
	s.mu.Unlock()
	if !s.sched.scheduling() {
		s.Tick()
	}
	s.sched.begin()
}

// Suspend stops the poll, and ARMS the next metadata read.
//
// Without that, a page left blurred for an hour resumes by publishing an hour
// old interface list with fresh rates on it, and keeps doing so until the
// countdown that was mid-flight when it stopped runs out. The rows are still
// worth keeping — they are the right shape and mostly still true — so this
// re-reads them on the first tick back rather than blanking the page.
//
// AND IT RELEASES THE RATE CHANNEL, as Stop does. Left joined, a collector
// nobody was watching held /interface/monitor-traffic open; the first sync after
// Resume rejoins it.
func (s *IfStatus) Suspend() {
	s.sched.end()
	s.stopRateChannel()
	s.mu.Lock()
	s.metaIn = 0
	s.mu.Unlock()
}

func (s *IfStatus) Resume() {
	if s.ros.Connected() {
		s.sched.begin()
	}
}

func (s *IfStatus) Stop() {
	s.sched.end()
	s.stopRateChannel()
	s.mu.Lock()
	s.lastFp, s.lastEmitAt = "", time.Time{}
	s.mu.Unlock()
}

// ifStatusFingerprint is EXTRACTED so it can be gated. It was inline, which is
// why the rule it embodies — every field the page renders belongs here — had no
// test: there was nothing to call. See fingerprint_test.go.
func ifStatusFingerprint(interfaces []Interface) string {
	type fpRow struct {
		I, N, T, C, M string
		R, D          bool
		Rx, Tx        float64
		IPs           []string
		E, Dr, Ld     *float64
	}
	rows := make([]fpRow, 0, len(interfaces))
	for _, i := range interfaces {
		rows = append(rows, fpRow{i.ID, i.Name, i.Type, i.Comment, i.MacAddr, i.Running, i.Disabled,
			round2(i.RxMbps), round2(i.TxMbps), i.IPs, i.Errors, i.Drops, i.LinkDowns})
	}
	b, _ := json.Marshal(rows)
	return string(b)
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (i *IfStatus) SetPollMs(ms int) {
	i.pollMs.set(ms)
	i.poll.retime()
	// And the scheduler: a streamed menu reopens at the new interval.
	i.sched.retune()
}

// UseCache feeds BOTH halves: the 1.4 shared-read cache and the subscription.
// Same cache, two uses.
func (s *IfStatus) UseCache(c *roscache.Cache) {
	s.cache = c
	s.sched.useCache(c)
}

// keyByIfaceName keys a monitor-traffic row. These rows carry no `.id` -- they
// are a measurement per interface, not a table -- so the interface name is the
// identity, and a later reading of one interface replaces the earlier.
func keyByIfaceName(r routeros.Reply) string { return r["name"] }

// syncRateChannel keeps a channel open on `monitor-traffic` for every interface
// this collector knows about, so `rates` can read instead of measure.
//
// ── NEITHER COLLECTOR OWNS IT; THEY HOLD IT TOGETHER ───────────────────────
//
// There were TWO channels on this menu for most of B.7, deliberately: `traffic`
// built its chart from a per-row callback and carried its own watchdog, so
// making either one the owner meant giving the fill a row callback and retiring
// that machinery — real work on the collector whose failure is most visible.
// B.0b measured a second channel as free, so B.7 took the additive path and left
// the merge as its own step.
//
// That step is done. `joinMonitorTraffic` is the one call both make, and neither
// is the authority on the other's needs: the channel carries the UNION of the
// interfaces asked for at the FINEST interval, and closes when the last holder
// lets go. See internal/collect/monitortraffic.go for the rule and
// `roscache.JoinStream` for the sharing.
//
// ── REOPENED WHEN THE INTERFACE SET CHANGES ────────────────────────────────
//
// A channel names its interfaces when it opens, so one opened before a VLAN was
// created never carries it. Same rule `traffic.syncStream` follows, and the same
// reason: the list is part of the command, not a filter applied to the answer.
func (s *IfStatus) syncRateChannel(names []string) {
	if s.cache == nil || len(names) == 0 {
		return
	}
	key := strings.Join(names, ",")

	s.rateMu.Lock()
	same := key == s.rateKey && s.rateStop != nil
	old := s.rateStop
	if !same {
		s.rateStop, s.rateKey = nil, ""
	}
	s.rateMu.Unlock()
	if same {
		return
	}
	if old != nil {
		old()
	}

	stop, err := joinMonitorTraffic(s.cache, names, int(s.pollMs.duration()/time.Second), nil)
	if err != nil {
		return // measuring, which is what `rates` already does
	}
	s.rateMu.Lock()
	if s.rateStop != nil || s.cache == nil {
		s.rateMu.Unlock()
		stop()
		return
	}
	s.rateStop, s.rateKey = stop, key
	s.rateMu.Unlock()
}

// stopRateChannel releases the channel with the collector.
func (s *IfStatus) stopRateChannel() {
	s.rateMu.Lock()
	stop := s.rateStop
	s.rateStop, s.rateKey = nil, ""
	s.rateMu.Unlock()
	if stop != nil {
		stop()
	}
}

// ratesFromChannel reads the rates out of a channel somebody is already holding,
// and reports whether it could.
//
// ── WHY THIS IS A READ AND NOT A SECOND STREAM ─────────────────────────────
//
// `traffic` holds a channel on this menu for the interfaces somebody is
// charting. `roscache` can keep an entry current from that channel, and once it
// does, the rates are a map lookup rather than a command. That is the whole of
// B.7: 52 commands a minute become zero, on a channel the app was holding
// anyway.
//
// ── IT ASKS FOR A SUPERSET AND TAKES WHAT IT FINDS ─────────────────────────
//
// The channel covers whatever interfaces its owner asked for, which is not
// necessarily every interface. An interface this collector wants and the channel
// does not carry is simply absent from the result, and the row keeps its
// previous rate rather than reading zero -- `Tick` copies `base` and stamps only
// the rates it was given, which is the same behaviour as a router that stopped
// reporting one.
//
// FALSE MEANS "TAKE THE MEASUREMENT", and the caller does. There is no third
// state: a partial answer is still an answer, because a missing interface costs
// exactly its own rate rather than the whole reading.
func (s *IfStatus) ratesFromChannel(names []string) (map[string]Rate, bool) {
	if s.cache == nil {
		return nil, false
	}
	// ONE CALL: a Streaming check then a Get could fall through to a real
	// read of this menu if the channel closed in between. See StreamSnapshot.
	rows, ok := s.cache.StreamSnapshot(monitorTrafficMenu)
	if !ok || len(rows) == 0 {
		return nil, false
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	out := make(map[string]Rate, len(names))
	for _, r := range rows {
		if !want[r["name"]] {
			continue
		}
		out[r["name"]] = rateOf(r)
	}
	if len(out) == 0 {
		// The channel is warm and carries none of the interfaces this collector
		// is asking about, which is not a usable answer.
		return nil, false
	}
	return out, true
}
