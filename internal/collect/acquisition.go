package collect

import (
	"strings"

	"mikrodash/internal/routeros"
)

// How this app gets data out of a router, which is TWO DIFFERENT THINGS wearing
// one name.
//
// ── THE DISTINCTION, AND WHY IT IS LOAD-BEARING ─────────────────────────────
//
// Every collector "reads from the router", and for most of them that means the
// same thing: ask a menu for some columns, get a table back. Those answers are
// facts about the router's configuration and state at rest. Two collectors
// wanting the same menu want the SAME ANSWER, so one read can serve both, an
// answer a second old is usually still true, and a cache can hold it.
//
// A minority ask something else entirely. "What is the throughput on these
// interfaces right now." "Is this host reachable." "Tell me each log line as it
// is written." Those answers are not facts at rest; they are measurements of an
// instant, or a channel the router pushes down. Two collectors asking them are
// NOT asking the same question even when the menu matches, because the question
// includes its target and its moment.
//
// CONFLATING THE TWO IS WHAT COST PHASE 1 ITS FIRST PREMISE. The coalescing
// cache was designed for "reads", and reads were assumed uniform. It cannot
// touch the single most expensive item in the app -- interface rates, at roughly
// 52 commands a minute -- and no amount of better keying would change that,
// because there is no table underneath to serve.
//
// ── SET A: QUERIES ──────────────────────────────────────────────────────────
//
// A table read. Cacheable, dedupable, and safe to serve slightly stale. Roughly
// sixty of the sixty-four commands this package issues. Everything phase 1 built
// -- `internal/roscache`, the union field lists, the fast and slow lanes -- is
// about these and only these.
//
// ── SET B: MEASUREMENTS AND STREAMS ─────────────────────────────────────────
//
// A Measurement is taken NOW and parameterised by its target: interface rates
// with a once argument, a bounded ping. Serving one from a cache would answer a
// question nobody asked, at a time nobody asked about.
//
// A Stream is an open channel the router pushes down: the traffic chart, the
// live ping, the log listener. It has no result to cache because it has no end.
//
// Five call sites, two menus, and between them they carry the app's most
// expensive acquisition. They need their own model: shared subscriptions with
// fan-out rather than a shared answer, and a cadence that belongs to the channel
// rather than to a poll loop. That model is NOT designed yet: it is carried
// as its own track in the collector rewrite.
//
// ── WHY THIS IS A TYPE AND NOT A COMMENT ────────────────────────────────────
//
// The rule already existed, spelled as "does this command carry anything other
// than a field list" inside the cache helper, where it read as a caching detail
// rather than as the shape of the data. Naming it puts one statement of the rule
// in one place, lets a gate enumerate set B, and gives the phases that follow
// something to be written against.
type Kind int

const (
	// Query is a table read: a menu, some columns, an answer two consumers can
	// share and a cache can hold.
	Query Kind = iota
	// Measurement is taken now and parameterised by its target. Never shareable
	// by menu, because the menu is not the whole question.
	Measurement
	// Stream is an open channel the router pushes down. No result, no end,
	// nothing to cache.
	Stream
)

func (k Kind) String() string {
	switch k {
	case Measurement:
		return "measurement"
	case Stream:
		return "stream"
	default:
		return "query"
	}
}

// KindOf classifies a command by the ARGUMENTS it carries, which is the only
// place the distinction is visible: the menu alone does not say. Both
// `/interface/monitor-traffic` and `/tool/ping` appear on each side of the line,
// separated by nothing but an argument.
//
// A bounding argument beats an interval, and that ordering is the whole of the
// subtlety here. `topology` pings with a count of one AND an interval; the count
// is what makes it a measurement that ends rather than a channel that does not.
// Reading the interval first would classify it as a stream and leave the app
// believing it holds an open channel it never opened.
func KindOf(cmd routeros.Cmd) Kind {
	if strings.HasSuffix(cmd.Path, "/listen") {
		return Stream
	}
	interval := false
	for _, a := range cmd.Args {
		switch {
		case strings.HasPrefix(a, "=once="), strings.HasPrefix(a, "=count="):
			return Measurement
		case strings.HasPrefix(a, "=interval="):
			interval = true
		}
	}
	if interval {
		return Stream
	}
	return Query
}

// ── THE AVAILABILITY LATCH, IN ONE PLACE ────────────────────────────────────
//
// Ten call sites across eight collectors spelled this out: `x == nil || *x`.
// It is one rule stated ten times, and this repository has already paid for that
// shape once -- `internal/collect/rooms.go` exists because the room lists were
// written twice and disagreed five times.
//
// THE RULE: a menu's presence is a THREE-STATE, and nil is not "absent".
//
//	nil    NOT YET KNOWN -- nothing has asked, or nothing has answered
//	true   the router has the menu
//	false  the router answered and does not have it
//
// Nil must read as AVAILABLE. A router is presumed to have a menu until it says
// otherwise, and the alternative blanks the page on the first tick before
// anything has come back -- which looks exactly like a router that lacks the
// feature. That asymmetry is the whole reason the field is a pointer rather than
// a bool, and it is the half a reader is most likely to get backwards.
func MenuAvailable(latch *bool) bool { return latch == nil || *latch }
