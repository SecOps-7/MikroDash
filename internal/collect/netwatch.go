package collect

// NetWatch collector — the port of src/collectors/netwatch.js.
//
//	/tool/netwatch   the monitored hosts and whether each is up
//
// ── THIS HAS NO PAGE ─────────────────────────────────────────────────────────
//
// It emits to `page-home`, not to a page of its own: NetWatch is a card on
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
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

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
	// It is deliberately absent from the emit fingerprint above: editing a
	// comment must not cost a re-render, and nothing on the card draws it. It
	// exists to feed the {{comment}} notification variable.
	Comment string `json:"comment"`
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

	mu sync.Mutex
	// order is the ids in the order the router first mentioned them, and hosts
	// is the row behind each — the JavaScript Map this payload's array order
	// depends on. See dhcpleases.go for the same trap at length.
	order  []string
	hosts  map[string]routeros.Reply
	lastFP string
	last   *NetwatchPayload
	// denied latches when the router says this user may not read netwatch. A
	// permission answer will not change on the next tick, and asking every
	// minute for ever would be noise in the log and load on the router.
	denied bool
}

func NewNetwatch(ros Reader, emit Emit, pollMs int) *Netwatch {
	// The original computes a clamped interval from its argument and then
	// OVERWRITES IT with a flat 60000 on the next line, so the configured value
	// never takes effect. Reproduced rather than repaired: that interval is the
	// heartbeat the browser's staleness threshold is tuned against, and quietly
	// honouring the argument here would make this side poll at a cadence the
	// live app never uses.
	_ = clampPoll(pollMs, 30000, 500, 600000)
	const ms = 60000

	n := &Netwatch{ros: ros, emit: emit, hosts: map[string]routeros.Reply{}}
	n.poll = newPollLoop(func() { n.Tick() },
		func() time.Duration { return time.Duration(ms) * time.Millisecond })
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
		Comment: r["comment"],
	}
}

func (n *Netwatch) Tick() {
	if !n.ros.Connected() {
		return
	}
	n.mu.Lock()
	denied := n.denied
	n.mu.Unlock()
	if denied {
		return
	}

	rows, err := n.ros.Do(netwatchCmd)
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
	clear(n.hosts)
	n.order = n.order[:0]
	for _, r := range rows {
		id := r[".id"]
		if id == "" {
			id = r["id"]
		}
		if id == "" {
			continue
		}
		if _, seen := n.hosts[id]; !seen {
			n.order = append(n.order, id)
		}
		n.hosts[id] = r
	}

	hosts := make([]NetwatchHost, 0, len(n.order))
	for _, id := range n.order {
		if r, ok := n.hosts[id]; ok {
			hosts = append(hosts, normaliseNetwatch(r))
		}
	}

	// ONLY id AND status. See the package note: a rename is invisible here on
	// purpose.
	var fp strings.Builder
	for _, h := range hosts {
		fp.WriteString(h.ID + ":" + h.Status + ";")
	}
	if fp.String() == n.lastFP && n.last != nil {
		n.mu.Unlock()
		return
	}
	n.lastFP = fp.String()
	payload := &NetwatchPayload{Hosts: hosts, TS: time.Now().UnixMilli()}
	n.last = payload
	n.mu.Unlock()

	n.emit("page-home", "netwatch:update", payload)
}

func (n *Netwatch) Last() *NetwatchPayload {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.last
}

func (n *Netwatch) Start() { n.Tick(); n.poll.start() }

// Reconnected clears the fingerprint so the first read after a reconnect always
// reaches the browser, even if the table came back identical.
func (n *Netwatch) Reconnected() {
	n.poll.stop()
	n.mu.Lock()
	n.lastFP = ""
	n.mu.Unlock()
	n.Tick()
	n.poll.start()
}

func (n *Netwatch) Suspend() { n.poll.stop() }
func (n *Netwatch) Resume()  { n.poll.start() }

func (n *Netwatch) Stop() {
	n.poll.stop()
	n.mu.Lock()
	n.lastFP = ""
	n.mu.Unlock()
}
