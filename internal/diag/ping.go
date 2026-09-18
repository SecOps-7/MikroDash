// Package diag is the Tools page's diagnostics: the command each one sends, the
// bounds it is held to, and what its reply means. Pure — rows in, result out —
// so the page's handler and the assistant's tool run exactly one implementation.
//
// ── BOUNDED BY THIS PACKAGE, NOT BY WHOEVER ASKS ────────────────────────────
//
// A diagnostic occupies an API channel on the router for as long as it runs, and
// the bottleneck this app is organised around is concurrent channels on the
// router. So the count, and the time the command may hold its channel, are fixed
// here: an operator or a model asking for a thousand pings gets the maximum, not
// a thousand.
package diag

import (
	"errors"
	"regexp"
	"strconv"
	"time"

	"mikrodash/internal/collect"
	"mikrodash/internal/routeros"
)

// Ping bounds. RouterOS sends one packet a second by default, so the count is
// also the run's length in seconds.
const (
	PingDefaultCount = 4
	PingMaxCount     = 10
)

// ErrAddress is an address that is not one token.
var ErrAddress = errors.New("the address must be one IP address, host name or MAC address")

// addressRe is one token: an IPv4 or IPv6 address (with a `%interface` zone), a
// host name, or a MAC address. Not a validator of any of those — the router is —
// but it refuses anything that is plainly not one word, and anything starting
// with a character that could read as a flag or a property.
var addressRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:%_-]{0,252}$`)

// PingCommand is the one ping sentence this app sends.
func PingCommand(address string, count int) (routeros.Cmd, error) {
	if !addressRe.MatchString(address) {
		return routeros.Cmd{}, ErrAddress
	}
	if count <= 0 {
		count = PingDefaultCount
	}
	if count > PingMaxCount {
		count = PingMaxCount
	}
	return routeros.Cmd{
		Path: "/tool/ping",
		Args: []string{"=address=" + address, "=count=" + strconv.Itoa(count)},
		// The run's own length, plus room for a slow first reply and name
		// resolution. Past it the command is cancelled on the router.
		Timeout: time.Duration(count)*time.Second + 5*time.Second,
	}, nil
}

// PingReply is one packet's outcome.
type PingReply struct {
	Seq  int    `json:"seq"`
	Host string `json:"host"`
	// Status is empty for a reply, and RouterOS's word otherwise: "timeout",
	// or an ICMP error such as "host unreachable".
	Status string   `json:"status"`
	RTTMs  *float64 `json:"rttMs"`
	TTL    int      `json:"ttl"`
	Size   int      `json:"size"`
}

// PingResult is one finished run.
type PingResult struct {
	Address  string      `json:"address"`
	Replies  []PingReply `json:"replies"`
	Sent     int         `json:"sent"`
	Received int         `json:"received"`
	LossPct  int         `json:"lossPct"`
	MinMs    *float64    `json:"minMs"`
	AvgMs    *float64    `json:"avgMs"`
	MaxMs    *float64    `json:"maxMs"`
}

// FoldPing reads a run's rows into its result.
//
// THE SUMMARY IS THE LAST ROW'S. Every row RouterOS sends carries the running
// sent, received and loss, and the min, avg and max once anything has replied;
// the last row is therefore the run's total, and adding the rows up here would be
// a second count that could disagree with the router's.
func FoldPing(address string, rows []routeros.Reply) PingResult {
	out := PingResult{Address: address, Replies: []PingReply{}}
	for _, r := range rows {
		p := PingReply{Seq: atoi(r["seq"]), Host: r["host"], Status: r["status"],
			TTL: atoi(r["ttl"]), Size: atoi(r["size"])}
		if p.Status == "" {
			p.RTTMs = collect.ParsePingRTT(r["time"])
		}
		out.Replies = append(out.Replies, p)
	}
	if n := len(rows); n > 0 {
		last := rows[n-1]
		out.Sent, out.Received, out.LossPct = atoi(last["sent"]), atoi(last["received"]), atoi(last["packet-loss"])
		out.MinMs = collect.ParsePingRTT(last["min-rtt"])
		out.AvgMs = collect.ParsePingRTT(last["avg-rtt"])
		out.MaxMs = collect.ParsePingRTT(last["max-rtt"])
	}
	return out
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
