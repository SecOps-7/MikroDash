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
	// Title is what the header shows.
	//
	// Every page has one. Three did not until 2026-09-01 and fell through to the
	// raw key, so the header read a lower-case "dashboard" -- inherited from a
	// map that simply had no entry for them.
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
	{Key: "dashboard", Title: "Dashboard", Path: "home"},
	{Key: "dns", Title: "DNS"},
	{Key: "bridges", Title: "Bridges"},
	{Key: "vlans", Title: "VLANs"},
	{Key: "wan", Title: "WAN"},
	{Key: "packages", Title: "Packages"},
	{Key: "routing", Title: "Routing"},
	{Key: "dhcp", Title: "DHCP"},
	{Key: "ppp", Title: "PPP"},
	{Key: "vpn", Title: "VPN"},
	{Key: "users", Title: "Users"},
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
	{Key: "devices", Title: "Devices"},
	{Key: "settings", Title: "Settings"},
}

// Renamed maps a page key this app USED to use to the key it uses now.
//
// ── WHY A RENAME NEEDS A TABLE AT ALL ───────────────────────────────────────
//
// A page key is also a PERMISSION key, stored in `role_pages.page` in the
// operator's database. Renaming a key therefore orphans every grant naming the
// old one, and an orphaned grant is not an error anywhere: the role simply stops
// conferring that page. Administrators are structural and keep working, so the
// loss is invisible to whoever is most likely to be testing.
//
// That is not hypothetical. On 2026-09-01 this install was found holding grants
// for `topology` and `wireless` -- renamed hours earlier -- so the readonly and
// operator roles had silently lost Network Topology and Wifi Clients. It also
// still held `routers`, dead since the Node cutover, which nobody had noticed in
// the intervening day because nothing looks.
//
// ── RULES ───────────────────────────────────────────────────────────────────
//
// APPEND-ONLY. An entry is a promise to an installed database, not a note about
// this source, so it outlives everyone's memory of the rename. Removing one
// re-orphans exactly the grants it was added to rescue.
//
// Every value must be a current key and every key must NOT be one, which
// `internal/pages`'s own test asserts in both directions -- so a chain like
// rosusers -> router-users -> users has to be collapsed to its final destination
// rather than left as a hop through a key that no longer exists.
var Renamed = map[string]string{
	// Node-era, dead since the v0.8.0 cutover.
	"routers": "devices",
	// Renamed 2026-09-01, all in one commit.
	"topology": "network-topology",
	"wireless": "wifi-clients",
	"wifi":     "wifi-networks",
	"audit":    "audit-trail",
	// `rosusers` became `router-users` and then `users` the same day. Only the
	// last hop ever reached a release, but a dev build could hold either.
	"rosusers":     "users",
	"router-users": "users",
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
