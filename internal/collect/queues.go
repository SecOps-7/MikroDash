package collect

// Queues collector — RouterOS traffic shaping.
//
//	/queue/simple   per-target bandwidth limits, ordered, bidirectional
//	/queue/tree     mangle-driven HTB shaping, unordered, one direction per node
//
// TWO MENUS WITH GENUINELY DIFFERENT ROW SHAPES, not one shape with gaps. All of
// this was settled against live hardware by the original and re-confirmed by the
// fixture this port captured:
//
//	simple  pairs everywhere ("15000000/20000000"), `packet-marks` plural, and a
//	        `dynamic` flag
//	tree    single values ("10000000"), `packet-mark` singular, and NO `dynamic`
//	        field at all
//
// Three more things the probe settled, each of which would otherwise be a bug:
//
//	STATISTICS ARRIVE UNASKED. `rate`, `bytes`, `packets`, `dropped` and the
//	queued-* fields come back on a plain /print — no `=stats=` flag. The code
//	still checks which fields actually arrived rather than assuming, because that
//	was true of one RouterOS build on one day.
//
//	THE API ANSWERS IN RAW BPS. The CLI's "15M/20M" reads back as
//	"15000000/20000000". Input accepts suffixes; output never carries them.
//
//	UNLIMITED IS 0, NOT ABSENT. An unlimited queue reads back as "0/0" rather
//	than omitting the field. So 0 means "explicitly unlimited" and absent means
//	"the router said nothing", and the page draws those differently. `guard.Rate`
//	carries that distinction; a plain int would flatten it.
//
// THIS COLLECTOR ONLY READS. Every write lives in the resource registry, gated
// on the page and on router:write.

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"
)

const (
	queueSimpleCmd = "/queue/simple/print"
	queueTreeCmd   = "/queue/tree/print"
)

// queueIdleAfterSec: a queue's byte counter stops moving the moment traffic
// stops. Past this, a stale delta would be reported as live throughput, so the
// rate is forced to zero. More trustworthy here than for a WireGuard peer (ppp
// has the same constant): unchanged queue bytes genuinely means nothing matched
// the queue.
const queueIdleAfterSec = 10.0

// IntPair is a simple queue's up/down pair of counters. Absent stays absent —
// see the header on why that is not zero.
type IntPair struct {
	Up   *int `json:"up"`
	Down *int `json:"down"`
}

// RatePair is the derived per-direction rate. Null on the first sample: there is
// no window yet, and 0 would claim an idle queue that may be saturating the line.
type RatePair struct {
	Up   *float64 `json:"up"`
	Down *float64 `json:"down"`
}

type SimpleQueue struct {
	ID    string `json:"id"`
	Order int    `json:"order"` // print order, and it is semantic — see BuildQueueRows
	Name  string `json:"name"`
	// Target is passed through untouched. The guard parses it; this does not.
	Target      string     `json:"target"`
	Parent      string     `json:"parent"`
	PacketMarks string     `json:"packetMarks"`
	Priority    string     `json:"priority"`
	QueueType   string     `json:"queueType"`
	LimitAt     guard.Pair `json:"limitAt"`
	MaxLimit    guard.Pair `json:"maxLimit"`
	BurstLimit  guard.Pair `json:"burstLimit"`
	Bytes       IntPair    `json:"bytes"`
	Packets     IntPair    `json:"packets"`
	Dropped     IntPair    `json:"dropped"`
	QueuedBytes IntPair    `json:"queuedBytes"`
	Disabled    bool       `json:"disabled"`
	Invalid     bool       `json:"invalid"`
	Dynamic     bool       `json:"dynamic"`
	Comment     string     `json:"comment"`

	RateBps      RatePair `json:"rateBps"`
	RateSource   *string  `json:"rateSource"`
	RateWindowMs *int64   `json:"rateWindowMs"`
}

type TreeQueue struct {
	ID          string     `json:"id"`
	Order       int        `json:"order"`
	Name        string     `json:"name"`
	Parent      string     `json:"parent"`
	PacketMark  string     `json:"packetMark"`
	Priority    string     `json:"priority"`
	QueueType   string     `json:"queueType"`
	LimitAt     guard.Rate `json:"limitAt"`
	MaxLimit    guard.Rate `json:"maxLimit"`
	BurstLimit  guard.Rate `json:"burstLimit"`
	Bytes       *int       `json:"bytes"`
	Packets     *int       `json:"packets"`
	Dropped     *int       `json:"dropped"`
	QueuedBytes *int       `json:"queuedBytes"`
	Disabled    bool       `json:"disabled"`
	Invalid     bool       `json:"invalid"`
	// A tree row has no `dynamic` field. Reported as false rather than omitted
	// so the frontend has one shape to render.
	Dynamic bool   `json:"dynamic"`
	Comment string `json:"comment"`
	// FastTrack bypasses a tree parented to `global`, but NOT one parented to an
	// interface — confirmed twice in the MikroTik docs, and a tree on an
	// interface is accepted by the router. The page needs that per row, so it is
	// derived once here rather than three times in the frontend.
	FasttrackBypassable bool `json:"fasttrackBypassable"`

	RateBps      *float64 `json:"rateBps"`
	RateSource   *string  `json:"rateSource"`
	RateWindowMs *int64   `json:"rateWindowMs"`
}

// Fasttrack is a SUMMARY, never a firewall listing. A reader holding `queues`
// but not `firewall` learns that FastTrack is on, which is a fact about this
// page's own correctness.
type Fasttrack struct {
	State string `json:"state"`
	Count int    `json:"count"`
	// A rule narrowed by address or interface bypasses only part of the traffic,
	// which is worth saying rather than implying every queue is dead.
	Scoped bool `json:"scoped"`
}

type QueuesPayload struct {
	TS     int64         `json:"ts"`
	PollMs int           `json:"pollMs"`
	Simple []SimpleQueue `json:"simple"`
	Tree   []TreeQueue   `json:"tree"`

	Fasttrack Fasttrack `json:"fasttrack"`
	// Which statistics this router actually returned. "none" is what lets the
	// page print one line instead of forty unexplained em-dashes.
	Stats     string `json:"stats"`
	Available bool   `json:"available"`
	Denied    bool   `json:"denied"`
}

// queueSample is the previous reading of one queue's counters.
type queueSample struct {
	up, down         int
	ts               time.Time
	rateUp, rateDown *float64
}

// FilterRowSource is the firewall collector, borrowed by reference and never
// fetched. Returning nil means "cannot say", which must degrade the banner
// rather than blank the page.
type FilterRowSource interface {
	FilterRows() []routeros.Reply
}

type Queues struct {
	ros      Reader
	emit     Emit
	poll     *pollLoop
	pollMs   *pollInterval
	firewall FilterRowSource

	mu     sync.Mutex
	prev   map[string]queueSample
	lastFP string
	last   *QueuesPayload
	// nil = unprobed, false = this router has no such menu, stop asking.
	simpleAvail *bool
	treeAvail   *bool
	denied      bool
}

func NewQueues(ros Reader, emit Emit, firewall FilterRowSource, pollMs int) *Queues {
	q := &Queues{
		ros: ros, emit: emit, firewall: firewall,
		// The Node signature is clampPoll(raw, def, hi, lo) and the call is
		// (pollMs, 5000, 60000, 2000). Reordered for this side's (raw, def, lo, hi).
		pollMs: newPollInterval(clampPoll(pollMs, 5000, 2000, 60000)),
		prev:   map[string]queueSample{},
	}
	q.poll = newPollLoop(func() { q.Tick() },
		func() time.Duration { return q.pollMs.duration() })
	return q
}

// queueInt is JavaScript's parseInt: a leading number wins, anything else is
// absent.
func queueInt(v string) *int {
	s := strings.TrimSpace(v)
	end := 0
	if end < len(s) && (s[end] == '-' || s[end] == '+') {
		end++
	}
	start := end
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == start {
		return nil
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return nil
	}
	return &n
}

// pairInt splits the counter pair. Simple queues report a pair; trees report one
// number in the same field, which is why a single value fills both halves.
func pairInt(raw string) IntPair {
	if raw == "" {
		return IntPair{}
	}
	parts := strings.Split(raw, "/")
	up := queueInt(parts[0])
	if len(parts) > 1 {
		return IntPair{Up: up, Down: queueInt(parts[1])}
	}
	return IntPair{Up: up, Down: up}
}

// statsLevel reports which statistics this router actually returned.
func statsLevel(rows []routeros.Reply) string {
	for _, r := range rows {
		if r[".id"] == "" {
			continue
		}
		if _, ok := r["bytes"]; !ok {
			return "none"
		}
		if _, ok := r["rate"]; !ok {
			return "counters"
		}
		return "full"
	}
	return "full" // nothing to judge; assume the best
}

// rateKey survives a rename but not a recreation.
//
// RouterOS reuses `*N` ids after a removal. Keyed on the id alone, a newly
// created queue would inherit a deleted one's byte baseline and report a
// fabricated multi-gigabit first sample.
func rateKey(menu string, r routeros.Reply) string {
	return menu + ":" + r[".id"] + "|" + r["name"]
}

type derivedRate struct {
	bps      RatePair
	source   *string
	windowMs *int64
}

// deriveRate computes a per-second rate from a byte counter, per direction.
//
// The idiom is ppp's and the two subtleties are load-bearing:
//
//	The stored timestamp advances ONLY when bytes actually moved, so the window
//	always spans a real measurement rather than the gap between two reads that
//	happened to see the same counter.
//
//	The first sample is nil, not 0 — there is no window yet, and 0 would claim an
//	idle queue that may be saturating the line.
//
// Not hoisted into a shared helper: ppp returns nil on the first sample and vpn
// returns 0, each with a comment arguing for its choice, so hoisting would
// silently change one of them.
func deriveRate(key string, bytes IntPair, routerRate RatePair,
	prev map[string]queueSample, now time.Time) derivedRate {

	out := RatePair{}
	var source *string
	var windowMs *int64

	store, has := prev[key]
	switch {
	case has && now.After(store.ts):
		dtSec := now.Sub(store.ts).Seconds()
		same := bytes.Up != nil && bytes.Down != nil &&
			*bytes.Up == store.up && *bytes.Down == store.down
		switch {
		case same && dtSec > queueIdleAfterSec:
			z := 0.0
			out.Up, out.Down = &z, ptrF(0)
		case !same:
			if bytes.Up != nil {
				out.Up = ptrF(math.Max(0, float64(*bytes.Up-store.up)*8/dtSec))
			}
			if bytes.Down != nil {
				out.Down = ptrF(math.Max(0, float64(*bytes.Down-store.down)*8/dtSec))
			}
		default:
			// Counter unchanged but not yet idle: hold the last known rate
			// rather than claiming either zero or a fresh measurement.
			out.Up, out.Down = store.rateUp, store.rateDown
		}
		s := "delta"
		source = &s
		w := now.Sub(store.ts).Milliseconds()
		windowMs = &w

	case routerRate.Up != nil || routerRate.Down != nil:
		// First sample only. RouterOS's own `rate` is an average in BYTES per
		// second over a window it does not disclose — good enough to avoid an
		// empty cell on the first tick, not good enough to keep using once we
		// can measure. Labelled so the page can say which it is showing.
		if routerRate.Up != nil {
			out.Up = ptrF(*routerRate.Up * 8)
		}
		if routerRate.Down != nil {
			out.Down = ptrF(*routerRate.Down * 8)
		}
		s := "router"
		source = &s
	}

	if bytes.Up != nil && bytes.Down != nil &&
		(!has || *bytes.Up != store.up || *bytes.Down != store.down) {
		prev[key] = queueSample{up: *bytes.Up, down: *bytes.Down, ts: now,
			rateUp: out.Up, rateDown: out.Down}
	}
	return derivedRate{bps: out, source: source, windowMs: windowMs}
}

func ptrF(f float64) *float64 { return &f }

// rateAsPair reads the router's own `rate` field, which is a pair on a simple
// queue and a single value on a tree.
func rateAsPair(raw string) RatePair {
	p := guard.ParsePair(raw)
	out := RatePair{}
	if p.Up.Set {
		out.Up = ptrF(float64(p.Up.Bps))
	}
	if p.Down.Set {
		out.Down = ptrF(float64(p.Down.Bps))
	}
	return out
}

func simpleRow(r routeros.Reply, i int, prev map[string]queueSample, now time.Time) SimpleQueue {
	bytes := pairInt(r["bytes"])
	rate := deriveRate(rateKey("s", r), bytes, rateAsPair(r["rate"]), prev, now)
	parent := r["parent"]
	if parent == "none" {
		parent = ""
	}
	return SimpleQueue{
		ID: r[".id"], Order: i, Name: r["name"], Target: r["target"], Parent: parent,
		PacketMarks: r["packet-marks"], Priority: r["priority"], QueueType: r["queue"],
		LimitAt:     guard.ParsePair(r["limit-at"]),
		MaxLimit:    guard.ParsePair(r["max-limit"]),
		BurstLimit:  guard.ParsePair(r["burst-limit"]),
		Bytes:       bytes,
		Packets:     pairInt(r["packets"]),
		Dropped:     pairInt(r["dropped"]),
		QueuedBytes: pairInt(r["queued-bytes"]),
		Disabled:    boolOf(r["disabled"]),
		Invalid:     boolOf(r["invalid"]),
		Dynamic:     boolOf(r["dynamic"]),
		Comment:     r["comment"],
		RateBps:     rate.bps, RateSource: rate.source, RateWindowMs: rate.windowMs,
	}
}

func treeRow(r routeros.Reply, i int, prev map[string]queueSample, now time.Time) TreeQueue {
	b := queueInt(r["bytes"])
	bytes := IntPair{Up: b, Down: b}
	rr := guard.ParseRate(r["rate"])
	var routerRate RatePair
	if rr.Set {
		routerRate = RatePair{Up: ptrF(float64(rr.Bps)), Down: ptrF(float64(rr.Bps))}
	}
	rate := deriveRate(rateKey("t", r), bytes, routerRate, prev, now)
	return TreeQueue{
		ID: r[".id"], Order: i, Name: r["name"], Parent: r["parent"],
		PacketMark: r["packet-mark"], Priority: r["priority"], QueueType: r["queue"],
		LimitAt:     guard.ParseRate(r["limit-at"]),
		MaxLimit:    guard.ParseRate(r["max-limit"]),
		BurstLimit:  guard.ParseRate(r["burst-limit"]),
		Bytes:       b,
		Packets:     queueInt(r["packets"]),
		Dropped:     queueInt(r["dropped"]),
		QueuedBytes: queueInt(r["queued-bytes"]),
		Disabled:    boolOf(r["disabled"]),
		Invalid:     boolOf(r["invalid"]),
		Dynamic:     false,
		Comment:     r["comment"],

		FasttrackBypassable: r["parent"] == "global",
		RateBps:             rate.bps.Up,
		RateSource:          rate.source,
		RateWindowMs:        rate.windowMs,
	}
}

// BuildQueueRows builds both tables. Pure and exported so the rate maths is
// testable without a router.
//
// ORDER IS PRESERVED, NOT SORTED. Simple queues are walked in list order and the
// first match wins, so a queue's position changes what it does. Sorting these
// alphabetically by default would misrepresent the router.
func BuildQueueRows(simpleRows, treeRows []routeros.Reply,
	prev map[string]queueSample, now time.Time) ([]SimpleQueue, []TreeQueue) {

	sRows := withID(simpleRows)
	tRows := withID(treeRows)

	// Baselines for rows that no longer exist are dropped BEFORE building, so a
	// recreated queue reusing a RouterOS `*N` id cannot inherit the old counter.
	live := map[string]bool{}
	for _, r := range sRows {
		live[rateKey("s", r)] = true
	}
	for _, r := range tRows {
		live[rateKey("t", r)] = true
	}
	for k := range prev {
		if !live[k] {
			delete(prev, k)
		}
	}

	simple := make([]SimpleQueue, 0, len(sRows))
	for i, r := range sRows {
		simple = append(simple, simpleRow(r, i, prev, now))
	}
	tree := make([]TreeQueue, 0, len(tRows))
	for i, r := range tRows {
		tree = append(tree, treeRow(r, i, prev, now))
	}
	return simple, tree
}

func withID(rows []routeros.Reply) []routeros.Reply {
	out := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		if r[".id"] != "" {
			out = append(out, r)
		}
	}
	return out
}

// ActiveFasttrack answers: is a FastTrack rule swallowing the traffic these
// queues are meant to shape?
//
// Pure, and fed raw firewall rows so a test can exercise it directly. Only
// `chain=forward` counts, and a disabled rule counts for nothing — a rule that
// is not in force bypasses nothing.
func ActiveFasttrack(filterRows []routeros.Reply) Fasttrack {
	var hits []routeros.Reply
	for _, r := range filterRows {
		if r["action"] == "fasttrack-connection" && r["chain"] == "forward" && !boolOf(r["disabled"]) {
			hits = append(hits, r)
		}
	}
	state := "clear"
	if len(hits) > 0 {
		state = "active"
	}
	scoped := false
	for _, r := range hits {
		for _, k := range []string{"srcAddress", "dstAddress", "inInterface",
			"src-address", "dst-address", "in-interface"} {
			if r[k] != "" {
				scoped = true
			}
		}
	}
	return Fasttrack{State: state, Count: len(hits), Scoped: scoped}
}

func (q *Queues) read(cmd routeros.Cmd, flag **bool) []routeros.Reply {
	if *flag != nil && !**flag {
		return nil
	}
	rows, err := q.ros.Do(cmd)
	if err != nil {
		msg := strings.ToLower(err.Error())
		no := false
		switch {
		case strings.Contains(msg, "no such"), strings.Contains(msg, "unknown command"):
			*flag = &no
		case strings.Contains(msg, "not enough permission"),
			strings.Contains(msg, "permission denied"),
			strings.Contains(msg, "no permissions"):
			*flag = &no
			q.denied = true
		}
		return nil
	}
	yes := true
	*flag = &yes
	out := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		if len(r) > 0 {
			out = append(out, r)
		}
	}
	return out
}

func (q *Queues) fasttrack() Fasttrack {
	// One guard, three cases: the collector is absent for a router where the
	// operator switched Firewall collection off, it has not started yet, or its
	// start swallowed a failure. All three mean "cannot say", and that must
	// degrade the banner rather than blank the page.
	if q.firewall == nil {
		return Fasttrack{State: "unknown"}
	}
	rows := q.firewall.FilterRows()
	if rows == nil {
		return Fasttrack{State: "unknown"}
	}
	return ActiveFasttrack(rows)
}

func (q *Queues) Tick() {
	simpleRows := q.read(routeros.Cmd{Path: queueSimpleCmd}, &q.simpleAvail)
	treeRows := q.read(routeros.Cmd{Path: queueTreeCmd}, &q.treeAvail)

	now := time.Now()
	q.mu.Lock()
	simple, tree := BuildQueueRows(simpleRows, treeRows, q.prev, now)
	statsRows := simpleRows
	if len(statsRows) == 0 {
		statsRows = treeRows
	}
	payload := &QueuesPayload{
		TS: now.UnixMilli(), PollMs: q.pollMs.ms(),
		Simple: simple, Tree: tree,
		Fasttrack: q.fasttrack(),
		Stats:     statsLevel(statsRows),
		Available: q.simpleAvail == nil || *q.simpleAvail,
		Denied:    q.denied,
	}
	q.last = payload
	fp := q.fingerprint(payload)
	changed := fp != q.lastFP
	q.lastFP = fp
	q.mu.Unlock()

	if changed {
		q.emit("page-queues", "queues:update", payload)
	}
}

// fingerprint decides whether this tick is worth emitting.
//
// BYTE COUNTERS ARE EXCLUDED ON PURPOSE: they move every tick on a busy queue,
// and emitting for that alone would defeat the dirty check. Rates ARE included,
// rounded to kbit, because they are what changes visibly on screen.
//
// `comment` is in both tuples. It was missing upstream, which meant a
// comment-only edit re-read the router, hashed an identical string and returned
// without emitting — so an edit that really landed never reached an open page.
// On a busy router that hid as mere slowness; on an idle one the update never
// arrived. Every field the page displays belongs here.
func (q *Queues) fingerprint(p *QueuesPayload) string {
	s := make([][]any, 0, len(p.Simple))
	for _, x := range p.Simple {
		s = append(s, []any{x.ID, x.Name, x.Target, x.Comment, x.Disabled, x.Dynamic,
			x.MaxLimit.Up, x.MaxLimit.Down, x.LimitAt.Up, x.LimitAt.Down,
			kbit(x.RateBps.Up), kbit(x.RateBps.Down)})
	}
	t := make([][]any, 0, len(p.Tree))
	for _, x := range p.Tree {
		t = append(t, []any{x.ID, x.Name, x.Parent, x.PacketMark, x.Comment, x.Disabled,
			x.MaxLimit, kbit(x.RateBps)})
	}
	b, _ := json.Marshal(map[string]any{"s": s, "t": t, "f": p.Fasttrack, "st": p.Stats})
	return string(b)
}

func kbit(f *float64) int {
	if f == nil {
		return 0
	}
	return int(math.Round(*f / 1000))
}

func (q *Queues) Last() *QueuesPayload {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.last
}

// RefreshNow re-reads immediately, after an action, so the page shows what the
// router did.
func (q *Queues) RefreshNow() { q.Tick() }

// ForgetRates drops every rate baseline.
//
// `set` and `reset-counters` can drop a counter. The max(0, …) clamp hides that
// as a zero, but the window AFTER it would be measured against a baseline the
// router no longer agrees with. Called by the write handlers.
func (q *Queues) ForgetRates() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.prev = map[string]queueSample{}
}

func (q *Queues) Start() { q.Tick(); q.poll.start() }

func (q *Queues) Reconnected() {
	q.poll.stop()
	q.mu.Lock()
	q.lastFP = ""
	q.denied = false
	q.prev = map[string]queueSample{}
	q.simpleAvail, q.treeAvail = nil, nil
	q.mu.Unlock()
	q.Tick()
	q.poll.start()
}

func (q *Queues) Suspend() { q.poll.stop() }
func (q *Queues) Resume()  { q.poll.start() }

func (q *Queues) Stop() {
	q.poll.stop()
	q.mu.Lock()
	q.lastFP = ""
	q.prev = map[string]queueSample{}
	q.mu.Unlock()
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (q *Queues) SetPollMs(ms int) {
	q.pollMs.set(ms)
	q.poll.retime()
}
