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
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

// fwProplist is one list for all four tables — they share a row shape.
const fwProplist = ".id,disabled,dynamic,chain,action,comment,src-address,dst-address," +
	"protocol,dst-port,in-interface,packets,bytes"

// fwTables is the read order, and it is the payload order too.
var fwTables = []string{"filter", "nat", "mangle", "raw"}

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
	TS          int64          `json:"ts"`
	Filter      []FirewallRule `json:"filter"`
	Nat         []FirewallRule `json:"nat"`
	Mangle      []FirewallRule `json:"mangle"`
	Raw         []FirewallRule `json:"raw"`
	ActiveTable string         `json:"activeTable"`
	PollMs      int            `json:"pollMs"`
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
}

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
	}
	f.poll = newPollLoop(func() { f.pollCounters() },
		func() time.Duration { return time.Duration(ms) * time.Millisecond })
	return f
}

// processRule turns one router row into a rule, and folds the packet delta in.
//
// `prev` is read and then WRITTEN, so the delta always spans one refresh.
func (f *Firewall) processRule(r routeros.Reply) FirewallRule {
	id := r[".id"]
	packets := pppInt(r["packets"])
	bytes := pppInt(r["bytes"])
	delta := 0
	if prev, ok := f.prevCounts[id]; ok {
		if d := packets - prev.packets; d > 0 {
			delta = d
		}
	}
	if id != "" {
		f.prevCounts[id] = fwCount{packets: packets, bytes: bytes}
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
	}
}

// safeGet reads one table. A table the API user cannot see costs its rows, never
// the payload — the original swallows here for the same reason.
func (f *Firewall) safeGet(table string) []routeros.Reply {
	rows, err := f.ros.Do(routeros.Cmd{
		Path: "/ip/firewall/" + table + "/print",
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
	read := map[string][]routeros.Reply{}
	for _, t := range fwTables {
		read[t] = f.safeGet(t)
	}

	f.mu.Lock()
	for _, t := range fwTables {
		rows := read[t]
		out := make([]FirewallRule, 0, len(rows))
		for _, r := range rows {
			out = append(out, f.processRule(r))
		}
		f.tables[t] = out
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
	f.mu.Lock()
	table := f.activeTable
	f.mu.Unlock()
	if table == "" {
		return
	}
	rows, err := f.ros.Do(routeros.Cmd{
		Path: "/ip/firewall/" + table + "/print",
		Args: []string{"=.proplist=.id,packets,bytes"},
	})
	if err != nil {
		return
	}

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
		if prev, ok := f.prevCounts[cur[i].ID]; ok {
			if d := packets - prev.packets; d > 0 {
				delta = d
			}
		}
		f.prevCounts[cur[i].ID] = fwCount{packets: packets, bytes: bytes}
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
	for _, t := range fwTables {
		for _, r := range f.tables[t] {
			if r.ID != "" {
				seen[r.ID] = true
			}
		}
	}
	for id := range f.prevCounts {
		if !seen[id] {
			delete(f.prevCounts, id)
		}
	}

	payload := &FirewallPayload{
		TS:     time.Now().UnixMilli(),
		Filter: orEmpty(f.tables["filter"]), Nat: orEmpty(f.tables["nat"]),
		Mangle: orEmpty(f.tables["mangle"]), Raw: orEmpty(f.tables["raw"]),
		ActiveTable: f.activeTable, PollMs: f.pollMs.ms(),
	}
	f.last = payload
	fp := f.fingerprint(payload)
	changed := fp != f.lastFP
	f.lastFP = fp
	f.mu.Unlock()

	if changed {
		f.emit("page-firewall,dash-card-firewall", "firewall:update", payload)
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
func (f *Firewall) fingerprint(p *FirewallPayload) string {
	b, _ := json.Marshal(map[string]any{
		"filter": p.Filter, "nat": p.Nat, "mangle": p.Mangle, "raw": p.Raw,
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
	case "filter", "nat", "mangle", "raw":
	default:
		return
	}
	f.mu.Lock()
	changed := f.activeTable != t
	f.activeTable = t
	f.mu.Unlock()
	if changed {
		f.buildAndEmit()
	}
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
	f.poll.stop()
	f.mu.Lock()
	f.lastFP = ""
	f.prevCounts = map[string]fwCount{}
	f.tables = map[string][]FirewallRule{}
	f.mu.Unlock()
	f.Tick()
	f.poll.start()
}

func (f *Firewall) Suspend() { f.poll.stop() }
func (f *Firewall) Resume()  { f.poll.start() }

func (f *Firewall) Stop() {
	f.poll.stop()
	f.mu.Lock()
	f.lastFP = ""
	f.prevCounts = map[string]fwCount{}
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
