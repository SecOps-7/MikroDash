package collect

// NetWatch collector — the port of src/collectors/netwatch.js.
//
//	/tool/netwatch   the monitored hosts and whether each is up
//
// ── THIS HAS NO PAGE ─────────────────────────────────────────────────────────
//
// It emits to `page-dashboard`, not to a page of its own: NetWatch is a card on
// the Dashboard, and `public/index.html` has no `page-netwatch` at all. So this
// queue item is the collector, and the card that renders the payload arrives
// with the Dashboard.
//
// ── EVENT-DRIVEN OVER THERE, POLLED HERE ─────────────────────────────────────
//
// The original prefers `/tool/netwatch/listen`, which pushes a state change the
// moment it happens, and keeps a 60-second heartbeat so the browser's staleness
// timer never fires while nothing is changing. This side polls. The parsing is
// the same code either way — `_loadInitial` there, Tick here — so adding the
// stream later changes delivery and not the payload.
//
// ── A RENAME DOES NOT REACH THE BROWSER ──────────────────────────────────────
//
// The emit fingerprint is `id:status` per host and nothing else, so renaming a
// NetWatch entry produces no update until its state next changes. That is the
// live behaviour, reproduced deliberately: the card exists to show what is up
// and what is down, and re-emitting the whole table on a cosmetic edit is what
// the fingerprint is there to prevent.

import (
	"encoding/json"
	"log"
	"regexp"
	"sync"
	"time"

	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

var netwatchCmd = routeros.Cmd{Path: "/tool/netwatch/print"}

// netwatchDenied matches the two answers that mean "this API user may not read
// netwatch", as opposed to a transient failure.
var netwatchDenied = regexp.MustCompile(`(?i)not allowed|no such command`)

// NetwatchHost is one monitored host as the card renders it.
type NetwatchHost struct {
	ID     string `json:"id"`
	Host   string `json:"host"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Name   string `json:"name"`
	// Comment is UNTRIMMED, matching netwatch.js:37 (`row.comment || ''`).
	// vpn.js:137 trims its own; the two collectors genuinely differ and the
	// goldens record the difference, so do not unify them.
	//
	// It feeds the {{comment}} notification variable, and since the NetWatch
	// page (#97) it is drawn and edited there, so it is in the emit fingerprint.
	Comment string `json:"comment"`
	// Disabled and Interval are for the NetWatch page, which shows and edits
	// them. A disabled host is not probed, so alerting treats it as not yet
	// probed rather than as down: see internal/alertwire.
	Disabled bool   `json:"disabled"`
	Interval string `json:"interval"`
}

// NetwatchPayload is `netwatch:update`. HOSTS FIRST, then ts — the field order
// is the emitted key order, and the golden records it that way round.
type NetwatchPayload struct {
	Hosts []NetwatchHost `json:"hosts"`
	TS    int64          `json:"ts"`
}

type Netwatch struct {
	ros  Reader
	emit Emit
	poll *pollLoop

	// ── PHASE 3.2: THE FIRST COLLECTOR OFF ITS OWN TIMER ────────────────────
	//
	// With a cache this collector does not decide when to read. It SUBSCRIBES to
	// its menu at a cadence and the router's one scheduler decides, which is what
	// gives "should this run" a single answer instead of five.
	//
	// THE POLL LOOP STAYS AS THE FALLBACK, and that is transitional rather than
	// tidy. Both background pools build this collector with no cache -- a router
	// nobody is watching still needs netwatch for its alerts -- so `cache == nil`
	// has to keep working exactly as before. Same rule as `readVia`: nil falls
	// through to the old path and nothing else changes.
	// See scheduled.go: one menu, subscribed at a cadence, with the poll loop
	// above as the no-cache fallback.
	sched scheduled

	mu sync.Mutex
	// order is the ids in the order the router first mentioned them, and hosts
	// is the row behind each — the JavaScript Map this payload's array order
	// depends on. See dhcpleases.go for the same trap at length.
	order  []string
	hosts  map[string]routeros.Reply
	lastFP string
	last   *NetwatchPayload
	// lastEmit is when a payload last went out, for netwatchHeartbeat.
	lastEmit time.Time
	now      func() time.Time
	// denied latches when the router says this user may not read netwatch. A
	// permission answer will not change on the next tick, and asking every
	// minute for ever would be noise in the log and load on the router.
	denied bool
}

// netwatchHeartbeat is how long an unchanged `netwatch:update` may be suppressed.
//
// There was none: an unchanged host table was not sent at all, so on a quiet
// router the Dashboard's NetWatch card, whose stale threshold is a fixed 90s
// (testdata/stale-tables.json), went stale after the first reading. Ten seconds,
// as connections, bandwidth, talkers and dhcpNetworks use. The table is read
// every 60s, so any heartbeat under that makes every read a send, and 60s sits
// inside the 90s threshold.
//
// Safe for alerts: `alert.NetwatchUpdate` fires on a host's status CHANGING, so
// the same table sent again fires nothing.
const netwatchHeartbeat = 10 * time.Second

func NewNetwatch(ros Reader, emit Emit, pollMs int) *Netwatch {
	// The original computes a clamped interval from its argument and then
	// OVERWRITES IT with a flat 60000 on the next line, so the configured value
	// never takes effect. Reproduced rather than repaired: that interval is the
	// heartbeat the browser's staleness threshold is tuned against, and quietly
	// honouring the argument here would make this side poll at a cadence the
	// live app never uses.
	_ = clampPoll(pollMs, 30000, 500, 600000)
	const ms = 60000

	n := &Netwatch{ros: ros, emit: emit, hosts: map[string]routeros.Reply{}, now: time.Now}
	n.poll = newPollLoop(func() { n.Tick() },
		func() time.Duration { return time.Duration(ms) * time.Millisecond })
	n.sched = scheduled{
		// fields nil: /tool/netwatch has no proplist of its own.
		loop: n.poll, menu: netwatchCmd.Path, fields: fieldsOf(netwatchCmd), apply: n.apply,
		// NO FIELD LIST: this collector reads whole rows, and roscache's union
		// rule makes saying so honestly better than naming a list that would
		// widen the moment somebody adds a column to the card.
		cadence: func() time.Duration { return time.Duration(ms) * time.Millisecond },
	}
	return n
}

// normaliseNetwatch is the row as the card wants it. The two defaults matter: a
// router that omits `type` is running an ICMP check, and a host with no `status`
// yet is unknown rather than down.
func normaliseNetwatch(r routeros.Reply) NetwatchHost {
	typ := r["type"]
	if typ == "" {
		typ = "icmp"
	}
	status := r["status"]
	if status == "" {
		status = "unknown"
	}
	return NetwatchHost{
		ID: r[".id"], Host: r["host"], Type: typ, Status: status, Name: r["name"],
		Comment: r["comment"], Disabled: r["disabled"] == "true", Interval: r["interval"],
	}
}

func (n *Netwatch) Tick() {
	if !n.ros.Connected() {
		return
	}
	n.apply(n.ros.Do(netwatchCmd))
}

// apply is everything Tick does with the rows once it has them, and is what the
// scheduler calls when it refreshes this menu. Split so the two paths -- polled
// and scheduled -- cannot drift into handling a denial differently.
func (n *Netwatch) apply(rows []routeros.Reply, err error) {
	n.mu.Lock()
	denied := n.denied
	n.mu.Unlock()
	if denied {
		return
	}

	if err != nil {
		if netwatchDenied.MatchString(err.Error()) {
			n.mu.Lock()
			n.denied = true
			n.mu.Unlock()
			log.Printf("[netwatch] permission denied — netwatch alerts disabled")
			return
		}
		log.Printf("[netwatch] load failed: %v", err)
		return
	}

	n.mu.Lock()
	hosts := BuildNetwatch(rows)
	// The by-id map and its order are kept because `Hosts()` serves them to the
	// alert wiring; the derivation above no longer depends on them.
	clear(n.hosts)
	n.order = n.order[:0]
	for _, r := range rows {
		id := netwatchID(r)
		if id == "" {
			continue
		}
		if _, seen := n.hosts[id]; !seen {
			n.order = append(n.order, id)
		}
		n.hosts[id] = r
	}

	// ONLY id AND status. See the package note: a rename is invisible here on
	// purpose.
	fp := netwatchFingerprint(hosts)
	now := n.now()
	if fp == n.lastFP && n.last != nil && now.Sub(n.lastEmit) < netwatchHeartbeat {
		n.mu.Unlock()
		return
	}
	n.lastFP = fp
	n.lastEmit = now
	payload := &NetwatchPayload{Hosts: hosts, TS: time.Now().UnixMilli()}
	n.last = payload
	n.mu.Unlock()

	EvNetwatchUpdate.Emit(n.emit, netwatchRooms.Join(), *payload)
}

func (n *Netwatch) Last() *NetwatchPayload {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.last
}

// UseCache moves this collector onto the router's scheduler. Set once, before
// Start; nil leaves it on its own poll loop.
func (n *Netwatch) UseCache(c *roscache.Cache) { n.sched.useCache(c) }

func (n *Netwatch) Start() {
	// The immediate read stays on the polled path only. Under the scheduler the
	// first pass fetches a menu it has never seen, so the first payload arrives
	// one scheduler tick later rather than synchronously.
	if !n.sched.scheduling() {
		n.Tick()
	}
	n.sched.begin()
}

// Reconnected clears the fingerprint so the first read after a reconnect always
// reaches the browser, even if the table came back identical.
func (n *Netwatch) Reconnected() {
	n.sched.end()
	n.mu.Lock()
	n.lastFP = ""
	n.mu.Unlock()
	if !n.sched.scheduling() {
		n.Tick()
	}
	n.sched.begin()
}

func (n *Netwatch) Suspend() { n.sched.end() }
func (n *Netwatch) Resume()  { n.sched.begin() }

func (n *Netwatch) Stop() {
	n.sched.end()
	n.mu.Lock()
	n.lastFP = ""
	n.mu.Unlock()
}

// netwatchID is the row's identity. RouterOS answers `.id` on the API and `id`
// through some paths, and a row with neither cannot be tracked at all.
func netwatchID(r routeros.Reply) string {
	if id := r[".id"]; id != "" {
		return id
	}
	return r["id"]
}

// BuildNetwatch turns the netwatch rows into the host list.
//
// Phase 4.1: no receiver, no I/O. ORDER-PRESERVING DEDUPLICATION BY ID, matching
// what the collector did inline: a repeated id keeps the last row's values and
// the first row's position.
// netwatchFingerprint is EXTRACTED so it can be gated, like ifStatusFingerprint.
// It was the id and status alone, which was right while the Dashboard card drew
// nothing else. The NetWatch page draws and edits every field, and a field left
// out would be re-read after a save, hash identically and never be emitted.
func netwatchFingerprint(hosts []NetwatchHost) string {
	b, _ := json.Marshal(hosts)
	return string(b)
}

func BuildNetwatch(rows []routeros.Reply) []NetwatchHost {
	order := make([]string, 0, len(rows))
	byID := make(map[string]routeros.Reply, len(rows))
	for _, r := range rows {
		id := netwatchID(r)
		if id == "" {
			continue
		}
		if _, seen := byID[id]; !seen {
			order = append(order, id)
		}
		byID[id] = r
	}
	hosts := make([]NetwatchHost, 0, len(order))
	for _, id := range order {
		hosts = append(hosts, normaliseNetwatch(byID[id]))
	}
	return hosts
}
