package session

// B.3: which menus may be delivered by a channel rather than a read.
//
// ── THE SWITCH IS WIRED HERE AND IT IS OFF ─────────────────────────────────
//
// `collection.Resolve` has computed `Resolved.Stream[key]` since the port, the
// UI has had the per-router Stream/Poll toggle since the port, and the value has
// reached the browser since the port. Nothing read it. Three of twenty-five
// collectors streamed, and an operator setting a router to Poll changed nothing
// because everything already polled.
//
// This is the seam that makes the setting mean something. It is deliberately not
// enough on its own.
//
// ── WHY IT IS A TABLE AND NOT A FLAG ON EACH COLLECTOR ─────────────────────
//
// Twenty-two collectors subscribe to a menu. Giving each a "should I stream"
// function is twenty-two edits to say one thing and twenty-two places for it to
// drift, which is the defect `internal/collect/rooms.go` was written to record.
// One table, keyed by the menu `roscache` already speaks, valued by the
// collector that owns it so `eff.Stream` can be asked about the right key.
//
// ── IT STARTED EMPTY, WHICH IS THE ACTIVATION HAZARD ───────────────────────
//
// `collection.defaultMode` is "stream". So the moment `eff.Stream` is consulted
// without a gate, every router that never expressed a preference switches ALL
// its dual-capable collectors to channels in one step -- a fleet-wide delivery
// change nobody asked for, on the strength of a default. Routers stored
// `mode: "poll"` are honoured correctly; the ones nobody has an opinion about
// are the ones that would move.
//
// This table is that gate, and it is why the change is reversible one collector
// at a time rather than one release at a time.
//
// ── A DEVIATION FROM "ONE COMMIT EACH", RECORDED AS ONE ────────────────────
//
// B.4 says one line per commit, each measured. The first three were done that
// way -- netwatch, system, the connection table -- because each was a new kind
// of thing: the first stream at all, the first singleton key, the first table
// that needed a round boundary.
//
// The eleven after them were added TOGETHER. They fall into two classes whose
// risk was already established by those three, every one was probed for
// `=interval=` support on live hardware first, and a wrong one falls back to
// polling rather than breaking a page. The cost is real and worth naming: a
// regression in this batch bisects to eleven menus rather than to one.
//
// ── ONLY LIVE DATA STREAMS ─────────────────────────────────────────────────
//
// A stream trades commands for a channel held open for the life of the
// subscription. That pays for live data: gauges, connections, sessions, clients
// and state that decides what the page shows now. It never pays for metadata,
// configuration that changes when somebody edits the router: the stream re-sends
// an unchanged table every interval on a channel that is open the whole time,
// while a poll costs one command and holds the channel for the length of the
// read. It is the operator's fast/slow rule applied to delivery: a collector's
// live reads may stream, and its slow lane and every metadata collector poll.
//
// A menu absent from this table polls.
var streamableMenus = map[string]string{
	// menu -> the registry key of the collector that owns it.
	//
	// Each line is one B.4 commit: enabled, then measured on an unwatched router
	// against `roslimit`'s open-channel level. `TestStreamableMenusAreRealAndOwned`
	// fails on an entry naming a menu nothing subscribes to or a collector the
	// registry does not have, so a line added carelessly does not silently do
	// nothing.

	// ── FIRST, AND CHOSEN FOR BEING BORING ──────────────────────────────────
	//
	// Real `.id` rows, a table of three, one page, a 30s cadence. It proves the
	// path end to end and saves almost nothing, which is the right trade for the
	// first one: the biggest prize is `system` at 111 commands a minute across
	// this fleet, and that is a SINGLE ROW WITH NO `.id` -- it needs a key of its
	// own, so it is not the collector to learn the mechanism on.
	//
	// Probed 2026-09-09: streams, 9 rows in 3s, first row 6ms.
	"/tool/netwatch/print": "netwatch",

	// ── THE LARGEST ITEM IN THE APP ─────────────────────────────────────────
	//
	// 111 commands a minute across this fleet -- more than everything else
	// polled, combined -- because the gauges animate and it runs at a 2s
	// cadence. A stream costs one channel and no commands.
	//
	// It needed a key of its own: a SETTINGS menu, one row, no `.id`. See
	// `keySingleton` in internal/collect/scheduled.go and the note in system.go,
	// which records that "one fewer channel held open" was a deliberate decision
	// against a cost B.0b could not observe.
	//
	// Probed 2026-09-09: streams, 3 rows in 3s, first row 41ms.
	"/system/resource/print": "system",

	// ── THE HEAVIEST TABLE IN THE APP, AND IT NEEDED B.6 FIRST ──────────────
	//
	// TWO consumers share this entry: `connections` and `bandwidth` subscribe to
	// it with byte-identical proplists, so ONE channel now serves both -- which
	// is the coalescing property proved at the delivery layer rather than at the
	// read.
	//
	// It was refused until B.6. Connections open and close constantly, and
	// without a round boundary the entry could only accumulate: closed
	// connections would have piled up for the life of the session on a page that
	// looked populated. It is off that list because a round boundary is found
	// now, and it is safe from the residual empty-table limitation for a reason
	// worth stating -- a router with ZERO connections is not a real state, since
	// the API session reading the table is itself one.
	//
	// Probed 2026-09-09: streams, 956 rows in 3s at interval=1.
	"/ip/firewall/connection/print": "conns",

	// ── THE TABLES THAT CAN BE LEGITIMATELY EMPTY ───────────────────────────
	//
	// These were refused twice over and neither reason survived measurement:
	// first for churn, which B.6's round boundary solved, then for emptiness,
	// which the watchdog's own restart turned out to distinguish. See the note
	// on `unrollable` in internal/roscache/stream.go.
	//
	// All probed 2026-09-09. The lease table and the registration tables carry
	// the pages an operator looks at most, and they were the largest remaining
	// polled load once the connection table moved.
	"/interface/wifi/registration-table/print": "wireless",
	"/ip/dhcp-server/lease/print":              "dhcpLeases",
	"/ppp/active/print":                        "ppp",
	// The bridge HOST table, not the port table: hosts are what `bridges`
	// subscribes to. A first attempt named `/interface/bridge/port/print` and
	// the gate rejected it — nothing subscribes to it, so the line would have
	// changed no delivery while claiming to.
	"/interface/bridge/host/print": "bridges",

	// ── AND FAILOVER STATE ──────────────────────────────────────────────────
	//
	// `wan`'s subscribed menu is its FAST half: which uplink detect-internet says
	// is carrying traffic. Its interface, DHCP and address reads are the slow
	// lane and poll. See the fast/slow ledger in internal/verify.
	"/interface/detect-internet/state/print": "wan",

	// ── METADATA DOES NOT STREAM, AND SEVEN LINES LEFT (2026-09-15) ─────────
	//
	// `/interface/wifi/print`, `/interface/vlan/print`,
	// `/ip/dhcp-server/network/print`, `/ip/dns/print`, `/user/print`,
	// `/system/package/print` and `/ip/address/print` were here. Each is
	// configuration. For `wifi` and `vlans` it is the collector's own slow half,
	// subscribed at ten and twelve times its interval; `dhcpNetworks` polls every
	// ten minutes by default. The first three streamed, holding a channel open to
	// re-send an unchanged table. The last four never could (their owners have no
	// `streamKey`) and changed no delivery. TestNoMetadataCollectorStreams keeps
	// them out.

}

// streamsMenu answers roscache's question: may this menu be pushed?
//
// TWO CONDITIONS, AND BOTH ARE REQUIRED. The table is this project's judgement
// that the menu is safe to stream at all; `eff.Stream` is the OPERATOR's per
// router decision. Either one saying no means poll, so enabling a collector here
// cannot override a router the operator has pinned to Poll.
func (s *Session) streamsMenu(menu string) bool {
	key, ok := streamableMenus[menu]
	if !ok {
		return false
	}
	return s.conf().Stream[key]
}
