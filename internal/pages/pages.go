// Package pages is the one list of the app's pages.
//
// ── WHY IT EXISTS ───────────────────────────────────────────────────────────
//
// A page key was written down in five places: `PAGES` in cmd/webbuild (which
// decides whether a page's markup is composed into the document at all), `PORTED`
// and `PAGE_TITLES` in web/src/main.ts, `PAGE_KEYS` and `ALL_NAV_PAGES` in
// web/src/gen/page-keys.ts, and the web/src/ui/page-<key>.html filenames.
//
// Five copies of one fact is five chances to disagree, and the disagreements are
// quiet ones: a key in the build list but not in PORTED renders an empty shell; a
// key in PORTED but not the build list navigates to markup that is not there.
//
// This is now the source. `cmd/webbuild` reads it to compose the document,
// `internal/server` reads it to register a URL per page, and `cmd/tsgen` emits
// web/src/gen/pages.ts from it — with `-check` in tools/verify.sh failing the
// build when that file goes stale. The TypeScript cannot drift from the Go
// without something red.
//
// ── THE KEY IS THE URL, AND ALSO A ROOM NAME ────────────────────────────────
//
// Each key is three things at once: the page's URL path (`/logs`), the id of its
// markup (`#page-logs`), and the WebSocket room collectors emit to
// (`page-logs`). That is deliberate — one name per page, nothing to map — but it
// means RENAMING A KEY IS A PROTOCOL CHANGE, not a cosmetic one, and both sides
// have to move together.
//
// It also means the keys are a public contract once URLs exist: changing one
// breaks anybody's bookmark.
package pages

// Page is one page of the app.
type Page struct {
	// Key is the URL path, the markup id and the room name. Lower case, and
	// hyphenated where it needs more than one word.
	Key string
	// Title is what the header shows. Empty means the key itself is good enough
	// to display, which is true wherever the two already agree.
	Title string
}

// All is every page with a renderer behind it, in nav order.
//
// ORDER MATTERS: cmd/webbuild composes the markup in this order, and the digit
// shortcuts address the first nine.
var All = []Page{
	{Key: "dashboard"},
	{Key: "dns", Title: "DNS"},
	{Key: "bridges", Title: "Bridges"},
	{Key: "vlans", Title: "VLANs"},
	{Key: "wan", Title: "WAN"},
	{Key: "packages", Title: "Packages"},
	{Key: "routing", Title: "Routing"},
	{Key: "dhcp", Title: "DHCP"},
	{Key: "ppp", Title: "PPP"},
	{Key: "vpn", Title: "VPN"},
	{Key: "rosusers", Title: "Router Users"},
	{Key: "queues", Title: "Queues"},
	{Key: "firewall", Title: "Firewall"},
	{Key: "wifi", Title: "Wifi Networks"},
	{Key: "capsman", Title: "CAPsMAN"},
	{Key: "interfaces", Title: "Interfaces"},
	{Key: "logs", Title: "Logs"},
	{Key: "topology", Title: "Network Topology"},
	{Key: "wireless", Title: "Wifi Clients"},
	{Key: "bandwidth", Title: "Bandwidth"},
	{Key: "connections", Title: "Connections"},
	{Key: "reports", Title: "Reports"},
	{Key: "audit", Title: "Audit Trail"},
	{Key: "backups", Title: "Backups"},
	{Key: "devices"},
	{Key: "settings"},
}

// Keys returns every page key, in order.
func Keys() []string {
	out := make([]string, len(All))
	for i, p := range All {
		out[i] = p.Key
	}
	return out
}

// Has reports whether a key names a page. `internal/server` uses it to decide
// what to route; anything else stays an honest 404.
func Has(key string) bool {
	for _, p := range All {
		if p.Key == key {
			return true
		}
	}
	return false
}
