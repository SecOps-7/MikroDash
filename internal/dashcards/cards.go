// Package dashcards is the one list of the Dashboard's cards.
//
// ── WHY IT EXISTS ───────────────────────────────────────────────────────────
//
// One fact — what a dashboard card IS — was written down in five places across
// two languages: `DEFAULT_LAYOUT`, `CARD_LABELS` and `CARD_ROOMS` in
// `web/src/gen/grid-tables.ts`, and `dashCardPages` and `dashCardRooms` in
// `internal/server/dashcard.go`.
//
// The thing that kept them honest is gone. That generated file names a JavaScript
// generator under tools/ that is NOT IN THE REPOSITORY, and whose name does not
// even match the glob `tools/verify.sh` uses to find the generators it checks. So
// nothing has checked it since the cutover — and `dashCardPages`'s own comment
// still said it was "checked against the live resolution by the grid table
// generator", which had stopped being true without anything failing.
//
// (The generator's filename is deliberately not written here as a path. The
// citation check in internal/verify scans for one and cannot tell a location
// from the name of something absent, so spelling it would make this file fail a
// check by explaining why a file is missing.)
//
// This is the same problem `internal/pages` solves for pages, and it gets the
// same answer: one declaration in Go, a generator that emits the TypeScript, and
// `-check` in the build.
//
// ── ONE ROW PER CARD, SO THE HALVES CANNOT DISAGREE ─────────────────────────
//
// A card in the layout with no label, or naming a room nothing emits to, or
// borrowing from a page that does not exist, becomes impossible to express
// rather than something a dead generator used to catch.
package dashcards

// The grid's geometry. Lifted from the generated table, where these were
// "ninety-two numbers that all look alike".
const (
	Cols = 24
	Rows = 22
	Gap  = 12
	Pad  = 20
	MinW = 1
	MinH = 1
)

// LSKey is where the browser remembers a layout.
//
// ── BUMPING IT DISCARDS EVERY OPERATOR'S DASHBOARD ──────────────────────────
//
// It is bumped only on a breaking change to the stored card-object format.
// ADDING A CARD IS NOT THAT: `mergeLayout` iterates the default layout and fills
// in any card a stored layout lacks, so a new card reaches existing users at its
// default position without touching this.
const LSKey = "mikrodash_dashboard_layout_v12"

// Card is one dashboard card: where it sits, what it is called, and what feeds
// and gates it.
type Card struct {
	// ID is the markup id, hyphenated: `dc-card-logs`.
	ID string
	// Label is what the Add panel calls it.
	Label string
	// Room is the card's room KEY, or empty for a card no collector feeds.
	//
	// LETTERS ONLY, 2 to 20. It becomes part of a room name, and
	// `dashCardKeyRe` refuses anything else — silently, by returning early, so a
	// bad key is a card that never subscribes with nothing said.
	Room string
	// Emits is the room collectors actually send to, when it differs from Room.
	//
	// TWO CARDS NEED THIS and the mismatch is inherited: `dc-card-physports` has
	// the key `interfaces` while its collector emits to `dash-card-physports`,
	// and `card-network` has `dhcp` against `dash-card-network`. Joining
	// `dash-card-` + key would subscribe a browser to a room nothing ever sends
	// to, which is indistinguishable from a quiet one.
	Emits string
	// Page is the page this card borrows its data from, and therefore the second
	// permission checked before its room is joined.
	//
	// A card showing firewall detail to somebody denied the Firewall page would
	// make the whole permission matrix a lie. Empty means the card is gated on
	// the Dashboard alone, which is right only for a card whose data is not any
	// page's to withhold.
	Page string

	X, Y, W, H int
	// Visible is whether the card is on a fresh dashboard. Most are not: they
	// wait in the Add panel.
	Visible bool
}

// All is every card, in the default layout's order.
//
// ORDER MATTERS: it is the order the grid lays them out and the order the Add
// panel offers the hidden ones.
var All = []Card{
	{ID: "card-traffic", Label: "Traffic", X: 1, Y: 1, W: 20, H: 5, Visible: true},
	{ID: "card-connections", Label: "Connections", Room: "connections", Page: "connections",
		X: 1, Y: 6, W: 8, H: 16, Visible: true},
	{ID: "card-system", Label: "System", X: 9, Y: 6, W: 8, H: 4, Visible: true},
	// The Network Flow card draws its wired count from ifStatus and its wireless
	// half from the wireless collector, and `dash-card-wireless` is the only room
	// it joins — see the keepAlive note in internal/collect/rooms.go.
	{ID: "dc-card-netflow", Label: "Network Flow", Room: "wireless", Page: "wifi-clients",
		X: 9, Y: 10, W: 8, H: 4, Visible: true},
	{ID: "card-network", Label: "Network", Room: "dhcp", Emits: "network", Page: "dhcp",
		X: 9, Y: 14, W: 8, H: 6, Visible: true},
	{ID: "dc-card-ping", Label: "Ping", X: 9, Y: 20, W: 8, H: 2, Visible: true},
	{ID: "card-toptalkers", Label: "Top Talkers", X: 17, Y: 6, W: 8, H: 8, Visible: true},
	{ID: "card-wireguard", Label: "WireGuard", Room: "vpn", Page: "vpn",
		X: 17, Y: 14, W: 8, H: 8, Visible: true},
	{ID: "dc-card-bw", Label: "Bandwidth", X: 21, Y: 1, W: 4, H: 5, Visible: true},

	{ID: "dc-card-signal", Label: "Signal Health", X: 1, Y: 1, W: 8, H: 4},
	{ID: "dc-card-band", Label: "Band Split", X: 1, Y: 1, W: 4, H: 4},
	{ID: "dc-card-physports", Label: "Physical Ports", Room: "interfaces", Emits: "physports",
		Page: "interfaces", X: 1, Y: 1, W: 8, H: 4},
	{ID: "dc-card-iputil", Label: "IP Utilisation", X: 1, Y: 1, W: 4, H: 4},
	{ID: "dc-card-destcc", Label: "Connections Map", X: 1, Y: 1, W: 8, H: 6},
	{ID: "dc-card-topcc", Label: "Top Countries", X: 1, Y: 1, W: 8, H: 6},
	{ID: "dc-card-flow", Label: "Conn. Flow", X: 1, Y: 1, W: 8, H: 8},
	{ID: "dc-card-topports", Label: "Top Ports", X: 1, Y: 1, W: 4, H: 6},
	{ID: "dc-card-routes", Label: "Routes", X: 1, Y: 1, W: 6, H: 6},
	{ID: "dc-card-bgp", Label: "BGP Peers", X: 1, Y: 1, W: 6, H: 4},
	{ID: "dc-card-fwaction", Label: "FW Actions", Room: "firewall", Page: "firewall",
		X: 1, Y: 1, W: 8, H: 6},
	{ID: "dc-card-logs", Label: "Logs", Room: "logs", Page: "logs", X: 1, Y: 1, W: 10, H: 6},
	{ID: "dc-card-netwatch", Label: "NetWatch", X: 1, Y: 1, W: 8, H: 6},
	// THE ONE CARD NO COLLECTOR FEEDS. It reports on this process, so it is
	// computed in the handler and gated on the Dashboard alone.
	{ID: "dc-card-diagnostics", Label: "API Diagnostics", Room: "diagnostics",
		X: 1, Y: 1, W: 6, H: 10},
	// The Agent Overview (#98). ALSO fed by this process rather than a collector,
	// like diagnostics — but unlike it, this one is gated on a real page, because
	// its sentence is written from whatever the viewer may already read. Gating on
	// the Dashboard alone would let somebody without the AI Agent page read a
	// summary of pages they cannot open.
	{ID: "dc-card-agent", Label: "Agent Overview", Room: "agent", Page: "ai-agent",
		X: 1, Y: 1, W: 8, H: 3},
	// The Security Score: the Security Scan page's score card on the Dashboard.
	// Fed by scans rather than a collector (see internal/server/secscan.go), and
	// gated on the Security Scan page, whose report it summarises.
	{ID: "dc-card-secscore", Label: "Security Score", Room: "secscore", Page: "security-scan",
		X: 1, Y: 1, W: 6, H: 6},
}

// PageFor is the page a card room borrows its data from, and therefore the
// second permission checked before the room is joined.
//
// AN UNKNOWN KEY GATES ON THE DASHBOARD ALONE, which is the fallback the live
// app has. Returning the empty string instead would gate the card on a page
// nobody holds, hiding it from everybody rather than from the wrong people.
func PageFor(room string) string {
	for _, c := range All {
		if c.Room == room && c.Page != "" {
			return c.Page
		}
	}
	return "dashboard"
}

// EmitRoom is the room name a card's key resolves to, which is the key itself
// unless the collector emits somewhere else. See Card.Emits.
func EmitRoom(room string) string {
	for _, c := range All {
		if c.Room == room && c.Emits != "" {
			return c.Emits
		}
	}
	return room
}

// Rooms is every card room key, deduplicated, in layout order.
//
// TWO CARDS MAY SHARE ONE, which is why leaving a room has to be decided by
// whether ANY visible card still wants it rather than by the card being removed.
func Rooms() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range All {
		if c.Room == "" || seen[c.Room] {
			continue
		}
		seen[c.Room] = true
		out = append(out, c.Room)
	}
	return out
}
