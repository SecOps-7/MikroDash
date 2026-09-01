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
	// Path is the URL segment when it differs from the key. Empty means the key
	// IS the URL, which is true of 25 of the 26 pages.
	//
	// ── THE ONE EXCEPTION, AND WHY IT IS ONE ────────────────────────────────
	//
	// `dashboard` is served at `/home`. The page is called Dashboard everywhere
	// it is named -- in the nav, in its markup id, in its room, in the grants
	// stored in the operator's database -- and only the URL says `home`.
	//
	// This field exists so that ONE difference is declared in one place instead
	// of being a rule the router carries. Adding a second entry is cheap; adding
	// a general key-to-slug mapping was rejected, because a table everything
	// must consult is a table everything can disagree with.
	Path string
}

// All is every page with a renderer behind it, in nav order.
//
// ORDER MATTERS: cmd/webbuild composes the markup in this order, and the digit
// shortcuts address the first nine.
var All = []Page{
	{Key: "dashboard", Path: "home"},
	{Key: "dns", Title: "DNS"},
	{Key: "bridges", Title: "Bridges"},
	{Key: "vlans", Title: "VLANs"},
	{Key: "wan", Title: "WAN"},
	{Key: "packages", Title: "Packages"},
	{Key: "routing", Title: "Routing"},
	{Key: "dhcp", Title: "DHCP"},
	{Key: "ppp", Title: "PPP"},
	{Key: "vpn", Title: "VPN"},
	{Key: "router-users", Title: "Router Users"},
	{Key: "queues", Title: "Queues"},
	{Key: "firewall", Title: "Firewall"},
	{Key: "wifi-networks", Title: "Wifi Networks"},
	{Key: "capsman", Title: "CAPsMAN"},
	{Key: "interfaces", Title: "Interfaces"},
	{Key: "logs", Title: "Logs"},
	{Key: "network-topology", Title: "Network Topology"},
	{Key: "wifi-clients", Title: "Wifi Clients"},
	{Key: "bandwidth", Title: "Bandwidth"},
	{Key: "connections", Title: "Connections"},
	{Key: "reports", Title: "Reports"},
	{Key: "audit-trail", Title: "Audit Trail"},
	{Key: "backups", Title: "Backups"},
	{Key: "devices"},
	{Key: "settings"},
}

// URL is the path this page is served at, without the leading slash.
func (p Page) URL() string {
	if p.Path != "" {
		return p.Path
	}
	return p.Key
}

// ForURL resolves a URL segment to its page key, or "" when nothing matches.
// The server routes on it and refuses anything else, so an unknown path stays an
// honest 404 rather than quietly serving the shell.
func ForURL(seg string) string {
	for _, p := range All {
		if p.URL() == seg {
			return p.Key
		}
	}
	return ""
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
