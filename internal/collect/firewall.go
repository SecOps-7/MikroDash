package collect

// Firewall collector — the four tables, and a counter refresh for the one on
// screen.
//
// `/ip/firewall/{filter,nat,mangle,raw}` are read in full at start and after
// every write. Only the ACTIVE table's counters are refreshed between those,
// because that is all the page is showing move.
//
// ── WHAT THE COUNTER REFRESH CANNOT SEE ─────────────────────────────────────
//
// It carries `.id`, `packets` and `bytes` and nothing else, so it cannot report
// ORDER — and a firewall write can reorder rules, which is the one thing that
// changes what a rule DOES without changing the rule. Only a full read answers
// "where is this rule now", which is why RefreshNow does one.
//
// ── DISABLED RULES TRAVEL ───────────────────────────────────────────────────
//
// They used to be dropped here, so the page never showed one — and a rule you
// cannot see is a rule you cannot re-enable, which left the table half-editable.
// They are flagged instead and the page dims them. The summary cards still count
// only what is in force, and that filter lives in the page rather than here, so
// the cards did not quietly change meaning.

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

// fwProplist is one list for all four tables — they share a row shape.
const fwProplist = ".id,disabled,dynamic,chain,action,comment,src-address,dst-address," +
	"protocol,dst-port,in-interface,packets,bytes"

// fwTables is the read order, and it is the payload order too.
var fwTables = []string{"filter", "nat", "mangle", "raw"}

// fwTables6 is the IPv6 half, in the same order.
//
// These are PAYLOAD KEYS, not menu names — `filter6` is the key on the wire and
// the key in `f.tables`, and `fwMenu` is what turns it into
// `/ipv6/firewall/filter`. Keeping one namespace for both families is what lets
// `activeTable` stay a single string instead of growing a family beside it.
var fwTables6 = []string{"filter6", "nat6", "mangle6", "raw6"}

// fwMenu is the ONE place a payload key becomes a RouterOS menu.
//
// Every read goes through it, so the family lives in exactly one function and a
// new caller cannot get the mapping subtly wrong.
func fwMenu(key string) string {
	if base, ok := strings.CutSuffix(key, "6"); ok {
		return "/ipv6/firewall/" + base
	}
	return "/ip/firewall/" + key
}

// countKey namespaces a counter baseline by the menu it came from.
//
// ── WHY THIS IS NOT JUST `.id` ──────────────────────────────────────────────
//
// RouterOS numbers each menu independently, so `*5` exists in
// `/ip/firewall/filter` AND in `/ipv6/firewall/filter` and they are different
// rules. Keyed by the bare id, the two share a baseline and `deltaPackets`
// becomes the difference between two unrelated counters — a wrong number that
// looks plausible, on dual-stack routers only, and invisible to any test that
// uses one family. `internal/collect/routing.go` solved the same collision with
// a "v6:" key prefix when it merged the two route menus.
func countKey(table, id string) string { return table + "\x00" + id }

type FirewallRule struct {
	ID          string `json:"id"`
	Chain       string `json:"chain"`
	Action      string `json:"action"`
	Comment     string `json:"comment"`
	SrcAddress  string `json:"srcAddress"`
	DstAddress  string `json:"dstAddress"`
	Protocol    string `json:"protocol"`
	DstPort     string `json:"dstPort"`
	InInterface string `json:"inInterface"`
	Packets     int    `json:"packets"`
	Bytes       int    `json:"bytes"`
	// DeltaPackets is 0 on the first sighting of a rule, not null: a rule with
	// no baseline has not been seen to match anything yet.
	DeltaPackets int  `json:"deltaPackets"`
	Disabled     bool `json:"disabled"`
	// A rule some service added is not ours to edit; the page marks it and the
	// write path refuses it independently.
	Dynamic bool `json:"dynamic"`
}

type FirewallPayload struct {
	TS     int64          `json:"ts"`
	Filter []FirewallRule `json:"filter"`
	Nat    []FirewallRule `json:"nat"`
	Mangle []FirewallRule `json:"mangle"`
	Raw    []FirewallRule `json:"raw"`

	// ── THE IPv6 HALF, AND WHY `omitempty` IS LOAD-BEARING ──────────────────
	//
	// These are nil unless somebody asked for IPv6 (see SetWantV6), and NIL IS
	// NOT `[]` HERE. Two things read that difference and they read it in
	// different places:
	//
	//   - DORMANCY reads the STRUCT, by reflection over these json tags
	//     (internal/session/dormancy_payload.go). A nil slice reports "not a
	//     list" and is SKIPPED, so a router nobody is watching IPv6 on is judged
	//     on its IPv4 tables alone — exactly today's verdict. An empty slice is
	//     a list, and counts.
	//   - THE WIRE cannot tell them apart, because `omitempty` drops both. That
	//     is fine: the page's question is "does this router do IPv6 at all",
	//     which `Ipv6Disabled` answers on its own.
	//
	// So `omitempty` is what keeps the golden byte-identical (the fixture never
	// asks for v6, so these four keys are simply absent), and the nil-vs-empty
	// split is what keeps dormancy correct. Removing either is a behaviour
	// change wearing a tidy-up's clothes.
	Filter6 []FirewallRule `json:"filter6,omitempty"`
	Nat6    []FirewallRule `json:"nat6,omitempty"`
	Mangle6 []FirewallRule `json:"mangle6,omitempty"`
	Raw6    []FirewallRule `json:"raw6,omitempty"`

	// Ipv6Disabled is `/ipv6/settings disable-ipv6`, or nil before the probe has
	// run. THREE STATES, not two: the page hides its IPv6 tab on true, shows it
	// on false, and leaves it alone on nil — because "not asked yet" must not
	// look like "this router has no IPv6".
	Ipv6Disabled *bool `json:"ipv6Disabled,omitempty"`

	ActiveTable string `json:"activeTable"`
	PollMs      int    `json:"pollMs"`
}

type fwCount struct{ packets, bytes int }

type Firewall struct {
	ros    Reader
	emit   Emit
	poll   *pollLoop
	pollMs *pollInterval

	mu          sync.Mutex
	tables      map[string][]FirewallRule
	prevCounts  map[string]fwCount
	activeTable string
	lastFP      string
	last        *FirewallPayload
	// lastEmit is when a payload last went out, for firewallHeartbeat.
	lastEmit time.Time
	now      func() time.Time

	// wantV6 is a LATCH, not a refcount.
	//
	// Set when a viewer selects the IPv6 family or ticks "Show IPv6 in cards";
	// cleared only where this collector is already being torn down — Suspend,
	// Reconnected, Stop. A refcount would mean tracking every socket close, page
	// blur and router switch, and those teardown paths are the easy ones to
	// miss. The cost of the latch is bounded and in the direction this repo
	// already prefers: if one viewer opens IPv6 and leaves while another stays
	// on the page, four extra reads per Tick continue until the room empties.
	wantV6 bool
	// v6Probed and ipv6Disabled cache one read of `/ipv6/settings` per
	// connection. Reconnected clears them: a router can gain or lose IPv6 while
	// we are away.
	v6Probed     bool
	ipv6Disabled *bool

	// The counter refresh, on the scheduler when there is one. MECHANISM B:
	// which menu it wants is `activeTable`, which the operator changes by
	// clicking a tab, so this subscription MOVES -- see SetActiveTable.
	sched scheduled
}

// firewallHeartbeat is how long an unchanged `firewall:update` may be suppressed.
//
// There was none: on a quiet ruleset, where no rule changed and no counter
// moved, nothing was sent after the first reading, so the Dashboard's Firewall
// card went stale. The card is retuned to the payload's poll interval plus
// STALE_GRACE (20s, testdata/stale-tables.json), and both Tick and the counter
// poll end in buildAndEmit, so any heartbeat under 20.5s keeps it fresh at every
// interval this collector allows. Ten seconds, as connections, bandwidth,
// talkers and dhcpNetworks use.
const firewallHeartbeat = 10 * time.Second

func NewFirewall(ros Reader, emit Emit, pollMs int) *Firewall {
	// The Node signature is clampPoll(raw, def, hi) with no lower bound in this
	// caller: clampPoll(pollMs, 10000, 30000). Reordered for this side's
	// (raw, def, lo, hi), with the same effective floor and ceiling.
	ms := clampPoll(pollMs, 10000, 10000, 30000)
	f := &Firewall{
		ros: ros, emit: emit, pollMs: newPollInterval(ms),
		tables:      map[string][]FirewallRule{},
		prevCounts:  map[string]fwCount{},
		activeTable: "filter",
		now:         time.Now,
	}
	// f.pollMs, not the captured `ms`: SetPollMs stores into the interval and
	// then calls retime(), so a cadence closed over the constructor's value made
	// the Firewall poll slider do nothing at all. Every other collector reads
	// the interval; this one did not.
	f.poll = newPollLoop(func() { f.pollCounters() }, f.pollMs.duration)
	f.sched = scheduled{
		loop: f.poll, menu: fwMenu("filter") + "/print",
		fields: fwCounterFields, cadence: f.pollMs.duration,
		apply: f.counterApplier("filter"),
	}
	return f
}

// fwCounterFields is the counter refresh's proplist, as a field list rather than
// a Cmd because the menu it belongs to is chosen at runtime.
var fwCounterFields = []string{".id", "packets", "bytes"}

// counterApplier binds a delivery to the table it was read from.
//
// The table is captured, never re-read. See scheduled.resubscribe for why:
// `.id` values repeat across menus, so merging one table's counters into
// another succeeds and quietly reports the wrong numbers.
func (f *Firewall) counterApplier(table string) func([]routeros.Reply, error) {
	return func(rows []routeros.Reply, err error) {
		// Retried on every delivery, and ONLY on this path, because it runs
		// exactly when somebody is looking -- see pollCounters.
		f.ProbeV6()
		if err != nil {
			return
		}
		f.mergeCounters(table, rows)
	}
}

func (f *Firewall) UseCache(c *roscache.Cache) { f.sched.useCache(c) }

// processRule turns one router row into a rule, and folds the packet delta in.
//
// `prev` is read and then WRITTEN, so the delta always spans one refresh.
// processRule is the collector's half: derive, then store the new baseline.
func (f *Firewall) processRule(table string, r routeros.Reply) FirewallRule {
	rule, count := BuildFirewallRule(f.prevCounts, table, r)
	if rule.ID != "" {
		f.prevCounts[countKey(table, rule.ID)] = count
	}
	return rule
}

// BuildFirewallRule turns one row into a rule, and returns the counter baseline
// the caller should remember for the next reading.
//
// Phase 4.1: no receiver, no I/O. PRIOR STATE IN, NEW STATE OUT -- the same shape
// as BuildIfStatus and BuildBandwidth -- because `deltaPackets` is a difference
// and a difference needs the previous reading. This function READS `prev` and
// never writes it, so storing is the caller's decision and a derivation cannot
// quietly advance a baseline the caller then discards.
func BuildFirewallRule(prev map[string]fwCount, table string, r routeros.Reply) (FirewallRule, fwCount) {
	id := r[".id"]
	packets := pppInt(r["packets"])
	bytes := pppInt(r["bytes"])
	delta := 0
	if p, ok := prev[countKey(table, id)]; ok {
		if d := packets - p.packets; d > 0 {
			delta = d
		}
	}
	action := r["action"]
	if action == "" {
		// The original's `r.action || '?'`. An action is the one field a rule
		// cannot meaningfully lack, so a blank one is shown as unknown rather
		// than as nothing.
		action = "?"
	}
	return FirewallRule{
		ID: id, Chain: r["chain"], Action: action, Comment: r["comment"],
		SrcAddress: r["src-address"], DstAddress: r["dst-address"],
		Protocol: r["protocol"], DstPort: r["dst-port"], InInterface: r["in-interface"],
		Packets: packets, Bytes: bytes, DeltaPackets: delta,
		Disabled: boolOf(r["disabled"]), Dynamic: boolOf(r["dynamic"]),
	}, fwCount{packets: packets, bytes: bytes}
}

// safeGet reads one table. A table the API user cannot see costs its rows, never
// the payload — the original swallows here for the same reason.
func (f *Firewall) safeGet(table string) []routeros.Reply {
	rows, err := f.ros.Do(routeros.Cmd{
		Path: fwMenu(table) + "/print",
		Args: []string{"=.proplist=" + fwProplist},
	})
	if err != nil {
		return nil
	}
	return rows
}

// Tick reads all four tables.
//
// ALL FOUR, not just the active one, so the chain-count card has fresh numbers
// for every table even while only one is on screen.
func (f *Firewall) Tick() {
	f.mu.Lock()
	wantV6 := f.wantV6
	f.mu.Unlock()

	want := fwTables
	if wantV6 {
		want = append(append([]string{}, fwTables...), fwTables6...)
	}

	read := map[string][]routeros.Reply{}
	for _, t := range want {
		read[t] = f.safeGet(t)
	}

	f.mu.Lock()
	for _, t := range want {
		rows := read[t]
		out := make([]FirewallRule, 0, len(rows))
		for _, r := range rows {
			out = append(out, f.processRule(t, r))
		}
		f.tables[t] = out
	}
	if !wantV6 {
		// DELETE rather than assign an empty slice. A stale v6 table must not
		// outlive the want, and the payload builder turns a missing key into a
		// nil slice, which is what dormancy has to see. See FirewallPayload.
		for _, t := range fwTables6 {
			delete(f.tables, t)
		}
	}
	f.mu.Unlock()
	f.buildAndEmit()
}

// pollCounters refreshes the ACTIVE table's counters only.
//
// This is the `=interval=` stream's poll equivalent: the same three fields, the
// same merge. Rules that vanished between reads keep their last counters rather
// than being dropped, because this read is not authoritative about membership —
// only Tick is.
func (f *Firewall) pollCounters() {
	// Retry the IPv6 probe here, and ONLY here, because this loop runs exactly
	// when somebody is looking: Resume() starts it, Suspend() stops it. A no-op
	// once the router has answered. Page focus is the timely attempt; this is
	// the one that recovers when focus happened to land before the session was
	// connected.
	f.ProbeV6()

	f.mu.Lock()
	table := f.activeTable
	f.mu.Unlock()
	if table == "" {
		return
	}
	rows, err := f.ros.Do(routeros.Cmd{
		Path: fwMenu(table) + "/print",
		Args: []string{"=.proplist=" + strings.Join(fwCounterFields, ",")},
	})
	if err != nil {
		return
	}
	f.mergeCounters(table, rows)
}

// mergeCounters folds one counter read into the table it was read from.
func (f *Firewall) mergeCounters(table string, rows []routeros.Reply) {
	byID := make(map[string]routeros.Reply, len(rows))
	for _, r := range rows {
		if r[".id"] != "" {
			byID[r[".id"]] = r
		}
	}

	f.mu.Lock()
	cur := f.tables[table]
	for i := range cur {
		r, ok := byID[cur[i].ID]
		if !ok {
			continue
		}
		packets := pppInt(r["packets"])
		bytes := pppInt(r["bytes"])
		delta := 0
		if prev, ok := f.prevCounts[countKey(table, cur[i].ID)]; ok {
			if d := packets - prev.packets; d > 0 {
				delta = d
			}
		}
		f.prevCounts[countKey(table, cur[i].ID)] = fwCount{packets: packets, bytes: bytes}
		cur[i].Packets, cur[i].Bytes, cur[i].DeltaPackets = packets, bytes, delta
	}
	f.mu.Unlock()
	f.buildAndEmit()
}

func (f *Firewall) buildAndEmit() {
	f.mu.Lock()
	// A baseline for a rule that no longer exists anywhere would let a recreated
	// rule reusing a RouterOS `*N` id inherit its counter.
	seen := map[string]bool{}
	for _, t := range append(append([]string{}, fwTables...), fwTables6...) {
		for _, r := range f.tables[t] {
			if r.ID != "" {
				seen[countKey(t, r.ID)] = true
			}
		}
	}
	for k := range f.prevCounts {
		if !seen[k] {
			delete(f.prevCounts, k)
		}
	}

	payload := &FirewallPayload{
		TS:     time.Now().UnixMilli(),
		Filter: orEmpty(f.tables["filter"]), Nat: orEmpty(f.tables["nat"]),
		Mangle: orEmpty(f.tables["mangle"]), Raw: orEmpty(f.tables["raw"]),
		Ipv6Disabled: f.ipv6Disabled,
		ActiveTable:  f.activeTable, PollMs: f.pollMs.ms(),
	}
	// NOT orEmpty. When nobody has asked for IPv6 these stay nil, and dormancy
	// reads nil as "no answer" rather than "empty" — see FirewallPayload.
	if f.wantV6 {
		payload.Filter6 = orEmpty(f.tables["filter6"])
		payload.Nat6 = orEmpty(f.tables["nat6"])
		payload.Mangle6 = orEmpty(f.tables["mangle6"])
		payload.Raw6 = orEmpty(f.tables["raw6"])
	}
	f.last = payload
	fp := f.fingerprint(payload)
	now := f.now()
	changed := fp != f.lastFP || now.Sub(f.lastEmit) >= firewallHeartbeat
	f.lastFP = fp
	if changed {
		f.lastEmit = now
	}
	f.mu.Unlock()

	if changed {
		EvFirewallUpdate.Emit(f.emit, firewallRooms.Join(), *payload)
	}
}

// fingerprint is THE WHOLE OF ALL FOUR TABLES, order included.
//
// It used to be the ids and counters alone, and everything else a rule carries —
// chain, action, addresses, comment, disabled — is rendered, so an edit to any
// of it reached an open page only if traffic happened to move a counter in the
// same tick. On a quiet rule the update never arrived at all.
//
// Counters stay IN, so this is no less sensitive than the old one, and array
// ORDER is covered too: a write can reorder rules, and order is what the page
// shows. That makes this collector the one exception to "byte counters stay out
// of a fingerprint" — here they were always in, and taking them out now would
// be a different change wearing this one's clothes.
// THE IPv6 TABLES ARE IN IT TOO, and nothing else forces that.
// `TestFirewallFingerprintCoversTheWholeRule` reflects over `FirewallRule`, not
// over the payload, so a forgotten `filter6` here would not fail a single test —
// it would just mean an IPv6 edit never reaches an open page on a quiet ruleset.
// `TestFirewallFingerprintCoversEveryTable` is what pins it.
func (f *Firewall) fingerprint(p *FirewallPayload) string {
	b, _ := json.Marshal(map[string]any{
		"filter": p.Filter, "nat": p.Nat, "mangle": p.Mangle, "raw": p.Raw,
		"filter6": p.Filter6, "nat6": p.Nat6, "mangle6": p.Mangle6, "raw6": p.Raw6,
		"ipv6Disabled": p.Ipv6Disabled,
	})
	return string(b)
}

func orEmpty(r []FirewallRule) []FirewallRule {
	if r == nil {
		return []FirewallRule{}
	}
	return r
}

// SetActiveTable switches which table's counters are refreshed.
func (f *Firewall) SetActiveTable(t string) {
	switch t {
	case "filter", "nat", "mangle", "raw",
		"filter6", "nat6", "mangle6", "raw6":
	default:
		return
	}
	f.mu.Lock()
	changed := f.activeTable != t
	f.activeTable = t
	f.mu.Unlock()
	if !changed {
		return
	}
	// MECHANISM B. The polled path reads `activeTable` inside its own body and
	// needs nothing here; the scheduled path is subscribed to a specific menu
	// and has to be moved.
	f.sched.resubscribe(fwMenu(t)+"/print", f.counterApplier(t))
	f.buildAndEmit()
}

// SetWantV6 turns the four IPv6 tables on or off for this session.
//
// Driven by the `firewall:v6` frame, which is READ-gated where `firewall:tab` is
// write-gated: turning this on only ADDS to the payload, it never changes what
// an existing viewer already sees, and gating it on write would leave the IPv6
// tab permanently empty for a read-only viewer.
//
// Turning it ON reads immediately rather than waiting for the next poll, because
// the viewer is looking at an empty table right now. Turning it OFF only clears
// the flag; the tables go on the next Tick.
func (f *Firewall) SetWantV6(on bool) {
	f.mu.Lock()
	changed := f.wantV6 != on
	f.wantV6 = on
	f.mu.Unlock()
	if !changed {
		return
	}
	if on {
		f.Tick()
		return
	}
	f.mu.Lock()
	for _, t := range fwTables6 {
		delete(f.tables, t)
	}
	f.mu.Unlock()
	f.buildAndEmit()
}

// ProbeV6 reads `/ipv6/settings` once per connection.
//
// ── WHY THIS AND NOT A RULE COUNT ───────────────────────────────────────────
//
// The page hides its IPv6 tab on a router that does not do IPv6, and the obvious
// test — "are there any v6 rules" — is wrong. Measured on RouterOS 7.24: all four
// `/ipv6/firewall/*` menus answer with zero rows and NO trap on a router with no
// v6 rules, and there is no separate `ipv6` package any more to be absent. So a
// rule count cannot tell "IPv6 is off" from "nobody has written a rule yet" — and
// since these tables are editable, hiding the tab on an empty one would hide it
// exactly when somebody wants to add their first rule.
//
// `disable-ipv6` is the operator actually turning IPv6 off, which is the question
// the tab is asking. One read, cached for the connection: it changes about once a
// year, and re-reading it per poll would spend a channel on nothing.
//
// Called from the Firewall page's focus handler, NOT from Start() — Start runs for
// every router at session connect, including the many nobody opens this page on.
func (f *Firewall) ProbeV6() {
	f.mu.Lock()
	done := f.v6Probed
	f.mu.Unlock()
	if done {
		return
	}

	rows, err := f.ros.Do(routeros.Cmd{
		Path: "/ipv6/settings/print",
		Args: []string{"=.proplist=disable-ipv6"},
	})

	// LATCH ONLY ON AN ANSWER. Setting `v6Probed` unconditionally was wrong and
	// live verification is what caught it: page focus can land before the
	// session has a connection, `Do` returns "routeros: not connected", and
	// latching there meant the probe NEVER ran again — the family tab stayed
	// hidden for the life of the connection, on a router that does IPv6
	// perfectly well. Silent, and invisible to every test, because no test has a
	// half-connected session.
	//
	// A router that genuinely lacks the menu therefore re-probes on each page
	// focus. That is one cheap failed command per visit, not per poll, and it is
	// the right way round: never latch an answer we did not get.
	if err != nil || len(rows) == 0 {
		return
	}
	f.mu.Lock()
	f.v6Probed = true
	v := boolOf(rows[0]["disable-ipv6"])
	f.ipv6Disabled = &v
	f.mu.Unlock()
	f.buildAndEmit()
}

func (f *Firewall) Last() *FirewallPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// RefreshNow re-reads ALL FOUR tables, not just the active one.
//
// A firewall write can change the order as well as the values, and order is the
// one thing the counter refresh never reports.
func (f *Firewall) RefreshNow() { f.Tick() }

// Start is a ONE-SHOT read, with no counter poll behind it.
//
// That split is the original's and it is load-bearing in two directions. It
// populates Last() at session connect so the QUEUES page can answer "is
// FastTrack swallowing the traffic these queues shape" without anyone opening
// the Firewall page — the banner degrades to "cannot say" otherwise. And it
// leaves the per-second counter traffic switched off until somebody is actually
// looking, which matters because the documented bottleneck is concurrent API
// channels on the router, not CPU here.
//
// Resume() is what starts the polling.
func (f *Firewall) Start() { f.Tick() }

func (f *Firewall) Reconnected() {
	f.sched.end()
	f.mu.Lock()
	f.lastFP = ""
	f.prevCounts = map[string]fwCount{}
	f.tables = map[string][]FirewallRule{}
	// The probe is per CONNECTION: a router can gain or lose IPv6 while we are
	// away, and a cached answer would outlive the fact.
	f.wantV6, f.v6Probed, f.ipv6Disabled = false, false, nil
	f.mu.Unlock()
	f.Tick()
	f.poll.start()
}

// Suspend releases the IPv6 want as well as stopping the poll.
//
// This is the latch's only ordinary release. It runs when the last Firewall-page
// viewer leaves and the dashboard card is unwatched, via
// `suspendIfNoRoomOccupied` — WHICH CALLS IT FROM A TIMER GOROUTINE, so the lock
// here is not decoration.
func (f *Firewall) Suspend() {
	f.sched.end()
	f.mu.Lock()
	f.wantV6 = false
	for _, t := range fwTables6 {
		delete(f.tables, t)
	}
	f.mu.Unlock()
}

func (f *Firewall) Resume() { f.sched.begin() }

func (f *Firewall) Stop() {
	f.sched.end()
	f.mu.Lock()
	f.lastFP = ""
	f.prevCounts = map[string]fwCount{}
	f.wantV6, f.v6Probed, f.ipv6Disabled = false, false, nil
	f.mu.Unlock()
}

// FilterRows exposes the filter table as raw replies for queueguard's FastTrack
// summary. See internal/collect/queues.go: a reader holding `queues` but not
// `firewall` learns only that FastTrack is on, which is a fact about the Queues
// page's own correctness.
func (f *Firewall) FilterRows() []routeros.Reply {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last == nil {
		return nil
	}
	out := make([]routeros.Reply, 0, len(f.last.Filter))
	for _, r := range f.last.Filter {
		out = append(out, routeros.Reply{
			"action": r.Action, "chain": r.Chain,
			"disabled":     boolStr(r.Disabled),
			"src-address":  r.SrcAddress,
			"dst-address":  r.DstAddress,
			"in-interface": r.InInterface,
		})
	}
	return out
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (f *Firewall) SetPollMs(ms int) {
	f.pollMs.set(ms)
	f.poll.retime()
}
