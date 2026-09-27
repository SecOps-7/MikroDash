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
	// Collector is the ONE collector whose data this page exists to show.
	//
	// ── WHY THIS IS DECLARED AND NOT DERIVED ────────────────────────────────
	//
	// It looks derivable: a collector emits to `page-<key>`, so scan the emits.
	// It is not: some pages have no collector at all (dashboard, reports,
	// audit-trail, backups, devices, settings and more), some are fed by more
	// than one, and several collector keys differ from the page they own --
	// `rosusers` owns `users`,
	// `wifi` owns `wifi-networks`, `wireless` owns `wifi-clients`, `conns` owns
	// `connections`, `ifStatus` owns `interfaces`, `topology` owns
	// `network-topology`.
	//
	// So "which collector feeds this page" and "which collector is this page FOR"
	// are different questions, and only the second one is useful. `interfaces` is
	// fed by `ifStatus`; so is `network-topology`, and it is not that page's
	// reason to exist.
	//
	// EMPTY MEANS NO OWNER, and that is a real answer rather than a gap. A page
	// with no owner is one that cannot be emptied by turning a collector off.
	//
	// `internal/verify/ownership_test.go` checks every entry against the registry
	// AND against the emit literals, so a declaration that stops being true fails.
	Collector string
	// Path is the URL segment when it differs from the key. Empty means the key
	// IS the URL, which is true of every page but one.
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
	{Key: "dns", Title: "DNS", Collector: "dns"},
	{Key: "bridges", Title: "Bridges", Collector: "bridges"},
	{Key: "vlans", Title: "VLANs", Collector: "vlans"},
	{Key: "wan", Title: "WAN", Collector: "wan"},
	{Key: "packages", Title: "Packages", Collector: "packages"},
	{Key: "routing", Title: "Routing", Collector: "routing"},
	// The NetWatch page (#97). Its collector also feeds the Dashboard card.
	{Key: "netwatch", Title: "NetWatch", Collector: "netwatch"},
	// dhcp's owner is NOT disableable, so this page can never be emptied by
	// turning a collector off. Deliberate: `conns` and `bandwidth` both read
	// dhcpNetworks for LAN classification, so it is not the operator's to switch
	// off. Said here so nobody "fixes" the asymmetry.
	{Key: "dhcp", Title: "DHCP", Collector: "dhcpNetworks"},
	{Key: "ppp", Title: "PPP", Collector: "ppp"},
	{Key: "vpn", Title: "VPN", Collector: "vpn"},
	{Key: "users", Title: "Users", Collector: "rosusers"},
	{Key: "queues", Title: "Queues", Collector: "queues"},
	// The Security Scan (2026-09-19): no collector. It reads its menus once, on
	// demand, through secscan:run (internal/server/secscan.go).
	{Key: "security-scan", Title: "Security Scan"},
	{Key: "firewall", Title: "Firewall", Collector: "firewall"},
	{Key: "wifi-networks", Title: "Wifi Networks", Collector: "wifi"},
	{Key: "capsman", Title: "CAPsMAN", Collector: "capsman"},
	{Key: "interfaces", Title: "Interfaces", Collector: "ifStatus"},
	// The IP Addresses page (#97): both families, fed by its own collector.
	{Key: "ip-addresses", Title: "IP Addresses", Collector: "areas"},
	// A GENERATED page: internal/areas declares it, and the shared `areas`
	// collector fills it. Its Collector is that one, not a collector of its own.
	{Key: "ip-pools", Title: "IP Pools", Collector: "areas"},
	{Key: "arp", Title: "ARP", Collector: "areas"},
	{Key: "address-lists", Title: "Address Lists", Collector: "areas"},
	{Key: "interface-lists", Title: "Interface Lists", Collector: "areas"},
	{Key: "ip-services", Title: "Services", Collector: "areas"},
	{Key: "certificates", Title: "Certificates", Collector: "areas"},
	{Key: "scripts", Title: "Scripts", Collector: "areas"},
	{Key: "scheduler", Title: "Scheduler", Collector: "areas"},
	{Key: "ntp-client", Title: "NTP Client", Collector: "areas"},
	{Key: "clock", Title: "Clock", Collector: "areas"},
	{Key: "logging", Title: "Logging", Collector: "areas"},
	{Key: "snmp", Title: "SNMP", Collector: "areas"},
	{Key: "files", Title: "Files", Collector: "areas"},
	{Key: "routing-tables", Title: "Routing Tables", Collector: "areas"},
	{Key: "routing-rules", Title: "Routing Rules", Collector: "areas"},
	{Key: "ospf", Title: "OSPF", Collector: "areas"},
	{Key: "ipsec", Title: "IPsec", Collector: "areas"},
	{Key: "openvpn", Title: "OpenVPN", Collector: "areas"},
	{Key: "wireguard", Title: "WireGuard", Collector: "areas"},
	{Key: "vrrp", Title: "VRRP", Collector: "areas"},
	{Key: "pppoe-clients", Title: "PPPoE Clients", Collector: "areas"},
	{Key: "dhcp-clients", Title: "DHCP Clients", Collector: "areas"},
	{Key: "dhcp-servers", Title: "DHCP Servers", Collector: "areas"},
	{Key: "containers", Title: "Containers", Collector: "areas"},
	// ── THE TOOLS: ONE PAGE EACH, UNDER ONE NAV CATEGORY ────────────────────
	//
	// These were four tabs on a single `tools` page until 2026-09-27. Tools is a
	// nav CATEGORY now, like Network or System, and each diagnostic is its own
	// page - which is what makes each one addressable (`/tools-torch`),
	// deep-linkable, and separately grantable.
	//
	// SEPARATELY GRANTABLE IS THE REAL CHANGE. The page key is the permission
	// key, so "may run a bandwidth test" and "may ping" used to be the one
	// grant, split only by read-versus-write on it. They are four grants now.
	// `pages.Renamed` cannot express a one-to-four split, so migration 27 in
	// internal/db/schema.go copies every stored `tools` grant onto all four at
	// the access it had - see the note on Renamed below.
	//
	// NO COLLECTOR, all four: nothing runs until a tool is started, so there is
	// nothing to switch off, and hiding one is a permission rather than a load
	// question.
	{Key: "tools-ping", Title: "Ping"},
	{Key: "tools-traceroute", Title: "Traceroute"},
	{Key: "tools-torch", Title: "Torch"},
	{Key: "tools-btest", Title: "Bandwidth Test"},
	// The Packet Sniffer (2026-09-27). A fifth page rather than a tab on one of
	// the four, for the reason the split was made: it is its own grant, and
	// "may capture packets" is a far bigger thing to hand out than "may ping".
	//
	// NO COLLECTOR either, and one gate this app does not own: /system/device-mode
	// carries a `sniffer` flag, and a device whose flag is false refuses the tool
	// whatever the grants say. See internal/server/sniffer.go.
	{Key: "tools-sniffer", Title: "Packet Sniffer"},
	// The Terminal page. NO COLLECTOR, for the same reason Tools has none: nothing
	// runs until an operator types a line.
	//
	// It is the one page whose input MikroDash DOES NOT PARSE AT ALL. The text goes
	// to the router's own console through /execute, so neither the resource
	// registry nor internal/rawcmd sees it and no guard can be consulted. What
	// gates it instead is in internal/server/terminal.go, which is the file to read
	// before changing anything here.
	{Key: "terminal", Title: "Terminal"},
	{Key: "logs", Title: "Logs", Collector: "logs"},
	{Key: "network-topology", Title: "Network Topology", Collector: "topology"},
	{Key: "wifi-clients", Title: "Wifi Clients", Collector: "wireless"},
	// NO OWNER, and that is the honest answer rather than a gap. This page draws
	// what the operator placed on a plan and hangs live clients off it, so it is
	// fed by `wifi` (which network is on which access point) and by `wireless`
	// (who is connected), and it exists for neither — turning either off costs it
	// half its content rather than emptying it. See internal/pages' note on
	// Collector, and internal/collect/rooms.go for the two declarations.
	{Key: "wifi-map", Title: "Wifi Map"},
	{Key: "bandwidth", Title: "Bandwidth", Collector: "bandwidth"},
	{Key: "connections", Title: "Connections", Collector: "conns"},
	// The AI Agent (#98). NO COLLECTOR, like dashboard and reports: it is fed by
	// whatever the viewer may already read, assembled per question, so there is
	// nothing to switch off that would empty it.
	{Key: "ai-agent", Title: "AI Agent"},
	{Key: "reports", Title: "Reports"},
	{Key: "audit-trail", Title: "Audit Trail"},
	{Key: "backups", Title: "Backups"},
	{Key: "devices", Title: "Devices"},
	// Config Management. NO COLLECTOR: templates, runs and drift are read on
	// demand, and a deploy's progress arrives on its own event.
	{Key: "config-management", Title: "Config Management"},
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
	// ── `tools` WAS A SPLIT, AND THIS ENTRY IS ONLY ITS SWEEP ──────────────
	//
	// The Tools page became four pages on 2026-09-27 (ping, traceroute, torch,
	// bandwidth test). This table maps one old key to ONE new one, so it cannot
	// carry a split: an install's `tools` grant has to reach all four or the
	// role silently loses three diagnostics. Migration 19 set the precedent for
	// that -- a split is a COPY, and copying is a migration's job -- so
	// migration 27 does the fan-out, at the access the grant already had.
	//
	// What is left for this table is the stale row. `RenamePageGrants` runs
	// AFTER the migrations, finds the `tools` row, tries to move it onto
	// `tools-ping` (which the migration has already written, at the same
	// access), collides on `PRIMARY KEY (role_id, page)`, ignores -- and then
	// deletes the row naming a page that no longer exists. Without the entry the
	// dead row sits in every upgraded database for ever, conferring nothing and
	// explaining nothing.
	//
	// It reads as "tools became tools-ping", which is a quarter of the truth;
	// the whole of it is above and in migration 27, and the two are written in
	// the same commit for that reason.
	"tools": "tools-ping",
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
