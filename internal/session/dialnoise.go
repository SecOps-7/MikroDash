package session

// What the reconnect loop says about a router that stays unreachable.
//
// The loop retries every five seconds (connectLoop says why that stays flat),
// and it logged every attempt: 720 identical lines an hour for one router that
// is switched off, which buried everything else in the container log
// (2026-10-30). The retrying is unchanged; only what is written is:
//
//	the first failure of an outage      in full, as before
//	a different reason                  in full, once, when it changes
//	the same reason, still failing      one reminder an hour, with the count
//	the reconnect                       how long it was down, and the count
//
// "Different" ignores digits: a reset connection names a fresh local port on
// every attempt, and that is not news. A changed endpoint is announced by
// Reconfigure itself.

import (
	"strconv"
	"strings"
	"time"
)

// dialNoiseEvery is how often an outage that has not changed is mentioned again.
const dialNoiseEvery = time.Hour

// dialNoise is one session's outage bookkeeping. Owned by connectLoop's
// goroutine, so it needs no lock.
type dialNoise struct {
	since      time.Time // first failure of this outage; zero while connected
	attempts   int
	reason     string // the last reason logged, digits removed
	lastLogged time.Time
}

// failed records one failed attempt and returns the line to log, or "" for none.
func (n *dialNoise) failed(now time.Time, msg string) string {
	n.attempts++
	reason := withoutDigits(msg)
	switch {
	case n.since.IsZero():
		n.since = now
	case reason != n.reason:
	case now.Sub(n.lastLogged) >= dialNoiseEvery:
		msg = "still unreachable after " + roundDuration(now.Sub(n.since)) +
			" (" + strconv.Itoa(n.attempts) + " attempts): " + msg
	default:
		return ""
	}
	n.reason, n.lastLogged = reason, now
	return msg
}

// recovered ends the outage and says how long it lasted, or "" when there was
// none to speak of: a single failed attempt is already in the log.
func (n *dialNoise) recovered(now time.Time) string {
	defer func() { *n = dialNoise{} }()
	if n.since.IsZero() || n.attempts < 2 {
		return ""
	}
	return "after " + roundDuration(now.Sub(n.since)) + " unreachable (" +
		strconv.Itoa(n.attempts) + " attempts)"
}

func withoutDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return -1
		}
		return r
	}, s)
}

// roundDuration is a duration to the second, or to the minute past an hour.
func roundDuration(d time.Duration) string {
	if d >= time.Hour {
		return d.Round(time.Minute).String()
	}
	return d.Round(time.Second).String()
}
