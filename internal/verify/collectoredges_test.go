package verify

import (
	"path/filepath"
	"regexp"
	"testing"
)

// TestCollectorEdgesAreDeclared is step 2.1 of the collector rewrite: the
// in-process edges between collectors, written down and held to.
//
// ── WHAT AN EDGE IS ─────────────────────────────────────────────────────────
//
// A collector handed a POINTER TO ANOTHER COLLECTOR at construction, rather than
// the data it wanted. `bridges` needs interface rates, so it is given the whole
// of `ifStatus`; `queues` needs one FastTrack summary, so it is given the whole
// of `firewall`.
//
// These are the reason disabling one collector silently degrades pages that look
// unrelated, and the reason the registry's `requires` -- which knows about ONE of
// them -- describes so little of the real graph. They are invisible from either
// end: nothing in `internal/collect` says who holds a pointer to it, and nothing
// in the registry says a page will quietly lose a column.
//
// ── IT FAILS IN BOTH DIRECTIONS ─────────────────────────────────────────────
//
// A new edge that nobody recorded is a failure, and a recorded edge that has
// gone is also a failure. Without the second half this becomes folklore: phases
// 2 and 3 remove these one at a time, and a list that does not shrink with them
// would go on describing a graph that no longer exists.
func TestCollectorEdgesAreDeclared(t *testing.T) {
	root := repoRoot(t)
	src := mustRead(t, filepath.Join(root, "internal", "session", "session.go"))

	// ── the declared graph ───────────────────────────────────────────────────
	//
	// TWELVE, NOT THE NINE Collectors-Rewrite.md claims in its prose. The
	// document's own table lists these same twelve and then calls them nine, and
	// nothing anywhere re-counted them. That is what this test is for.
	//
	// The `what` is not decoration. It is the field that decides whether an edge
	// CAN be removed the way phase 2 proposes -- by having the consumer read the
	// underlying menu through the cache instead. An edge carrying `rates` cannot:
	// rates come from `/interface/monitor-traffic` with a once argument, which is
	// one of the three menus step 1.4 established as uncacheable, so there is no
	// menu underneath for a consumer to read.
	declared := map[string]map[string]string{
		"ifStatus": {
			"bridges":   "rates",
			"vlans":     "rates",
			"wan":       "rates",
			"topology":  "rates",
			"bandwidth": "rates, plus an interface lookup via a Last() type assertion",
		},
		"dhcpLeases": {
			"dhcpNetworks": "lease IPs, for subnet client counts",
			"wireless":     "lease names, so a client is not shown as a bare MAC",
			"conns":        "lease names",
			"bandwidth":    "lease names",
			"topology":     "lease names, via WithSources",
		},
		// ABSENT FROM THE DOCUMENT'S TABLE ALTOGETHER, and found by this test on
		// its first run. Both consumers take the LAN ranges from here to decide
		// which end of a connection is local, which is what the source filter and
		// the direction split are built on.
		"dhcpNetworks": {
			"conns":     "the LAN ranges the source filter uses",
			"bandwidth": "the LAN ranges, for the direction split",
		},
		"system":   {"topology": "identity and gauges, via WithSources"},
		"firewall": {"queues": "the FastTrack summary, via FilterRowSource"},
		// ── THE ONE PRODUCER WITH NO PAYLOAD OF ITS OWN (2026-09-10) ────────
		//
		// `arp` emits nothing and has no page. Its ENTIRE output is these four
		// edges: the router's ARP table is the only place that says which MAC is
		// behind which IP, and every one of these consumers needs that join to
		// put a name or an address on a device.
		//
		// The `what` field decides whether phase 2 could remove an edge by
		// having the consumer read the menu itself. Here it could - `/ip/arp/print`
		// is an ordinary cacheable table - and it still should not: four
		// consumers reading it separately is four subscriptions and four copies
		// of the same index, which is exactly what the coalescing cache and this
		// collector exist to avoid.
		"arp": {
			"conns":     "IP→MAC, so a lease keyed by the MAC can name an address the lease table does not list",
			"bandwidth": "IP→MAC, the same chain on the same table",
			"wireless":  "MAC→IP: a registration row carries no address, and this is the `ip` the page renders",
			"topology":  "MAC→IP, for an MNDP neighbour whose own row has no address - without it there is nothing to ping, so no status",
		},
		// ── `connTable` WAS HERE, AND ITS REMOVAL IS THE POINT ──────────────
		//
		// It was declared as a non-collector because it was the same shape of
		// coupling and a selective ledger is worthless: `connections` read the
		// heaviest table in the app and deposited the snapshot for `bandwidth`
		// to difference, so `bandwidth` never issued the command at all.
		//
		// The entry also carried an instruction -- "THIS ONE MUST NOT BE REMOVED
		// by phase 2. Step 1.4 measured the alternative and found it strictly
		// worse: routing that menu through the cache would share the READ, while
		// this shares the PARSE." THAT WAS WRONG IN ITS PREMISE. `ConnTable`
		// shared raw rows, not a parse, so the alternative shared exactly the
		// same thing by the mechanism every other menu uses. 3.2d subscribed both
		// collectors and 2026-09-09 deleted the table.
		//
		// Recorded here rather than silently dropped, because the instruction it
		// carried was repeated to an operator as fact twice.
	}

	found := collectorEdges(t, src)

	for producer, consumers := range declared {
		for consumer, what := range consumers {
			if !found[producer][consumer] {
				t.Errorf("declared edge %s -> %s (%s) is gone from session.go. If it was "+
					"removed deliberately, drop it here too -- a list that does not shrink "+
					"with the code describes a graph that no longer exists.",
					producer, consumer, what)
			}
		}
	}
	for producer, consumers := range found {
		for consumer := range consumers {
			if declared[producer][consumer] == "" {
				t.Errorf("%s is handed to %s at construction and is not declared here. An "+
					"in-process edge is invisible from both ends: nothing in internal/collect "+
					"says who holds a pointer to it, and nothing in the registry says a page "+
					"will quietly lose a column when it is disabled.", producer, consumer)
			}
		}
	}

	n := 0
	for _, c := range found {
		n += len(c)
	}
	t.Logf("%d in-process collector edges, across %d producers", n, len(found))
}

// collectorEdges reads which collectors are handed to which at construction.
//
// A collector field is one assigned from a `collect.New*` call, or from the
// session's own constructor for one (`s.newSystem`, which installs the identity
// hook), so the set is derived from the file rather than listed -- a new
// collector joins it by being constructed, which is the only way one can exist.
//
// THE STATEMENT'S EXTENT IS SCANNED, NOT GUESSED. A first attempt took
// "everything up to the next assignment", which is wrong for the LAST collector
// constructed: its extent ran to the end of the function and swallowed the
// UseCache wiring block, which names every collector, so it reported an edge from
// nearly everything to `traffic`. Parenthesis depth from the opening bracket is
// the real boundary, and chained setters like `WithSources` are then consumed
// deliberately, because an edge declared in one is as real as an edge in an
// argument list.
//
// Prose between statements cannot register: a mention has to be spelled
// `s.<name>` to match. A string literal holding an unbalanced bracket would
// confuse the depth count; there is none in this file, and the construction-count
// guard above is what would notice if that stopped being true.
func collectorEdges(t *testing.T, src string) map[string]map[string]bool {
	t.Helper()

	ctor := regexp.MustCompile(`(?m)^\ts\.(\w+) = (?:collect\.New|s\.new)\w+\(`)
	locs := ctor.FindAllStringSubmatchIndex(src, -1)
	if len(locs) < 15 {
		t.Fatalf("only %d collector constructions found in session.go; there are more than "+
			"twenty, so the scan has stopped matching and this test checks nothing", len(locs))
	}

	fields := map[string]bool{}
	for _, m := range locs {
		fields[src[m[2]:m[3]]] = true
	}

	// skipCall consumes from just inside an open bracket to just past its match.
	skipCall := func(i int) int {
		depth := 1
		for i < len(src) && depth > 0 {
			switch src[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
			i++
		}
		return i
	}

	ref := regexp.MustCompile(`\bs\.(\w+)\b`)
	out := map[string]map[string]bool{}
	for _, m := range locs {
		consumer := src[m[2]:m[3]]
		end := skipCall(m[1])
		// Chained setters belong to the same statement.
		for {
			k := end
			for k < len(src) && (src[k] == ' ' || src[k] == '\t' || src[k] == '\n') {
				k++
			}
			if k >= len(src) || src[k] != '.' {
				break
			}
			for k < len(src) && src[k] != '(' {
				k++
			}
			if k >= len(src) {
				break
			}
			end = skipCall(k + 1)
		}
		for _, r := range ref.FindAllStringSubmatch(src[m[1]:end], -1) {
			producer := r[1]
			if !fields[producer] || producer == consumer {
				continue
			}
			if out[producer] == nil {
				out[producer] = map[string]bool{}
			}
			out[producer][consumer] = true
		}
	}
	return out
}
