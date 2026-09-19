package collect

// NetWatch collector — the port of src/collectors/netwatch.js.
//
//	/tool/netwatch   the monitored hosts and whether each is up
//
// ── TWO VIEWS ────────────────────────────────────────────────────────────────
//
// The Dashboard's NetWatch card and, since #97, the NetWatch page, which draws and
// edits every field. The alert rules read the same payload.
//
// ── DELIVERY: STREAMED OR POLLED, ONE PARSE ────────────────────────────────
//
// The netwatch table streams as an `=interval=` print through the scheduler
// when the router's collection mode says Stream (internal/session/
// streammenus.go), and is polled otherwise. The parse is the same either way.

import (
	"encoding/json"
	"log"
	"regexp"
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

// Netwatch is a table collector: see table.go for its lifecycle.
type Netwatch struct {
	tableCore[NetwatchPayload]
	emit Emit
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
	n := &Netwatch{emit: emit}
	// PINNED AT SIXTY SECONDS, whatever is passed. The original computes a
	// clamped interval from its argument and then OVERWRITES IT with a flat 60000
	// on the next line, and that interval is what the browser's staleness
	// threshold is tuned against. There is no poll setting for it.
	n.setup(n, ros, pollMs, tableSpec{
		cmd: netwatchCmd, poll: [3]int{60000, 60000, 60000}, heartbeat: netwatchHeartbeat,
	})
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

// derive is the host list. A denial retires the collector: a permission answer
// will not change on the next reading.
func (n *Netwatch) derive(rows []routeros.Reply, err error, _ bool) (*NetwatchPayload, string) {
	if err != nil {
		if netwatchDenied.MatchString(err.Error()) {
			n.retire()
			log.Printf("[netwatch] permission denied — netwatch alerts disabled")
			return nil, ""
		}
		log.Printf("[netwatch] load failed: %v", err)
		return nil, ""
	}
	hosts := BuildNetwatch(rows)
	return &NetwatchPayload{Hosts: hosts, TS: time.Now().UnixMilli()}, netwatchFingerprint(hosts)
}

func (n *Netwatch) send(p NetwatchPayload) {
	EvNetwatchUpdate.Emit(n.emit, netwatchRooms.Join(), p)
}

func (n *Netwatch) reset() {}

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
