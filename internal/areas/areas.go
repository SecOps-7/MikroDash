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
// Each entry is one generated page. The ledgers in internal/verify hold every
// field of it to something real: the page key to `internal/pages`, the nav group
// to the shell, the resources and columns to the registry, and each resource to a
// fixture and a row in the frozen API surface.
var declared = []Area{
	// ── THE FIRST AREA ──────────────────────────────────────────────────────
	//
	// IP pools: one table, four fields, and a menu nothing else in this app
	// reads. Chosen as the mechanism's first instance BECAUSE it is new — a
	// migration of an existing page would have had two collectors reading one
	// menu until the old one was deleted, and "does the generated page work"
	// would have been asked of a page that was already working.
	{
		Key: "ip-pools", Title: "IP Pools", NavGroup: "ipsvc",
		Tables: []Table{{Resource: "ipPool",
			Columns: []string{"name", "ranges", "used", "total", "nextPool", "comment"}}},
		// A pool changes when somebody edits it. Sixty seconds is the
		// configuration cadence the bridges and VLAN collectors use.
		Poll: 60 * time.Second,
	},
	// ── SLICE 6: FIREWALL AND SERVICES ──────────────────────────────────────
	//
	// Address lists: the entries firewall rules match against, static ones and
	// the dynamic ones rules add with a timeout. Nothing else reads the menu.
	{
		Key: "address-lists", Title: "Address Lists", NavGroup: "security",
		Tables: []Table{{Resource: "addressList",
			Columns: []string{"list", "address", "timeout", "dynamic", "comment"}}},
		Poll: 60 * time.Second,
	},
	// Interface lists: who is in LAN, WAN and the rest, which the firewall
	// matches on. Members first, because changing who is in a list is the
	// common task. Both resources carry the list-membership lockout guard.
	{
		Key: "interface-lists", Title: "Interface Lists", NavGroup: "network",
		Tables: []Table{
			{Resource: "ifListMember", Title: "Members",
				Columns: []string{"list", "interface", "disabled", "dynamic", "comment"}},
			{Resource: "ifList", Title: "Lists",
				Columns: []string{"name", "include", "exclude", "builtin", "comment"}},
		},
		Poll: 60 * time.Second,
	},
	// IP services: the router's own services, and the live connections RouterOS
	// 7.24 lists beside them. The one MikroDash connects through is guarded.
	{
		Key: "ip-services", Title: "IP Services", NavGroup: "ipsvc",
		Tables: []Table{{Resource: "ipService",
			Columns: []string{"name", "port", "proto", "availableFrom", "disabled", "dynamic", "remote"}}},
		Poll: 60 * time.Second,
	},
	// Certificates: what the router holds, and until when. Removing the one
	// api-ssl presents is refused while MikroDash speaks TLS.
	{
		Key: "certificates", Title: "Certificates", NavGroup: "security",
		Tables: []Table{{Resource: "certificate",
			Columns: []string{"name", "commonName", "privateKey", "trusted", "invalidAfter", "expiresAfter"}}},
		Poll: 60 * time.Second,
	},
	// ── SLICE 7: SYSTEM AND AUTOMATION ──────────────────────────────────────
	//
	// Scripts. Their code, their policy and running them are behind codeGate.
	{
		Key: "scripts", Title: "Scripts", NavGroup: "system",
		Tables: []Table{{Resource: "script",
			Columns: []string{"name", "owner", "policy", "runCount", "lastStarted", "comment"}}},
		Poll: 60 * time.Second,
	},
	// ── THE PROOF: A HAND-BUILT PAGE, MIGRATED ──────────────────────────────
	//
	// IP Addresses was a collector, a page module, markup, a nav entry, a room,
	// a dormancy target, a poll key and a visibility key (#97). It is now this.
	// What changed on the page, deliberately: the two families are two tabs
	// rather than one table with a Family column, visibility is the shared
	// `hiddenAreas` list, and the interval is this one rather than a setting.
	{
		Key: "ip-addresses", Title: "IP Addresses", NavGroup: "network",
		Tables: []Table{
			{Resource: "ipAddress", Title: "IPv4",
				Columns: []string{"address", "network", "interface", "disabled", "dynamic", "invalid", "comment"}},
			{Resource: "ipv6Address", Title: "IPv6",
				Columns: []string{"address", "interface", "advertise", "disabled", "dynamic", "invalid", "comment"}},
		},
		Poll: 60 * time.Second,
	},
}

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
