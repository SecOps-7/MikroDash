// Package areas declares a RouterOS area once and lets the rest of the app be
// generated from it.
//
// ── WHY A DECLARATION RATHER THAN ANOTHER PAGE ──────────────────────────────
//
// MikroMCP reaches perhaps sixty RouterOS menus MikroDash does not. Each one, as
// a hand-built page, is a collector (the 21-row checklist in
// Collector-Architecture.md), a page key in six places, a nav entry, a visibility
// setting, a module, markup, and a tool — for a table of rows the resource engine
// could already render. Sixty of those is not a roadmap, it is a rewrite.
//
// An area is the same thing said once: which page key, which nav group, which
// resources, which columns, how often to read. Everything else — the page, the
// collector's subscription, the nav entry, the visibility toggle, the `list_`
// tool and `change_row`'s coverage — follows from the registry and the
// declaration.
//
// ── WHAT IT IS NOT ──────────────────────────────────────────────────────────
//
// It is not a second resource registry: an area POINTS at resources, and the
// registry keeps saying what a row is, which fields it has and which guards it
// declares. It is not a second permission model either: `Key` is a page key, so
// the existing per-user, per-router matrix gates an area exactly as it gates a
// hand-built page.
//
// ── THE LEDGERS ARE IN THE TEST, AND THEY FAIL BOTH WAYS ────────────────────
//
// An area naming a page key, nav group or resource that does not exist is a page
// nobody can open, a nav entry in no group, or a table of nothing. Those are
// checked against `internal/pages`, the shell markup and `internal/resource`
// rather than against a list typed beside them.
package areas

import "time"

// Table is one tab of an area: a resource, and the columns the list shows.
//
// The columns are FIELD NAMES from the resource, not a second schema. A column
// naming a field the resource does not declare is a header over an empty cell,
// which the ledger refuses.
type Table struct {
	// Resource is the registry key, e.g. "addressList".
	Resource string
	// Columns are the resource's field names, in the order the table shows them.
	// Empty means every non-secret field, in declaration order.
	Columns []string
	// Title names the tab when an area has more than one table. Empty takes the
	// resource's own label.
	Title string
}

// Area is one generated page.
type Area struct {
	// Key is a page key: the URL, the room, the permission and the visibility
	// guard, exactly as a hand-built page's key is. See CLAUDE.md's table of the
	// six things a page key means.
	Key string
	// Title is what the nav entry and the page header say.
	Title string
	// NavGroup is one of the shell's existing groups — "network", "wireless",
	// "ipsvc", "security", "traffic", "tunnels", "system". A new group is a
	// change to the shell, which is why this is checked against it.
	NavGroup string
	// Tables are the area's tabs, in order. One table renders without tabs.
	Tables []Table
	// Poll is how often the areas collector re-reads this area's menus while
	// somebody is looking at it. Configuration, so it is always a poll and never
	// a stream: a stream holds an API channel, and these menus change when
	// somebody edits them.
	Poll time.Duration
}

// declared is the catalogue.
//
// ── EMPTY, AND THE EMPTINESS IS RECORDED ────────────────────────────────────
//
// The mechanism lands before its first instance: the collector, the generator and
// the generic page come next, and the IP Addresses migration is the proof. The
// ledgers below iterate this, so today they measure nothing — and
// `TestNoAreaIsDeclaredYet` fails the moment one is added, which is the prompt to
// read the ledgers rather than to discover them later.
var declared []Area

// All returns the declared areas.
func All() []Area { return append([]Area(nil), declared...) }

// ByKey resolves one area, or false.
func ByKey(key string) (Area, bool) {
	for _, a := range declared {
		if a.Key == key {
			return a, true
		}
	}
	return Area{}, false
}

// Keys is every area's page key, in declaration order.
func Keys() []string {
	out := make([]string, 0, len(declared))
	for _, a := range declared {
		out = append(out, a.Key)
	}
	return out
}

// Resources is every resource key an area names, deduplicated, in declaration
// order. The areas collector reads exactly these menus.
func Resources() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, a := range declared {
		for _, t := range a.Tables {
			if t.Resource != "" && !seen[t.Resource] {
				seen[t.Resource] = true
				out = append(out, t.Resource)
			}
		}
	}
	return out
}
