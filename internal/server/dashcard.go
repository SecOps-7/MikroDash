package server

// Dashboard card rooms.
//
// A card on the Dashboard can be the ONLY view a collector gets. Someone whose
// dashboard shows the Firewall card but who never opens the Firewall page still
// needs that collector running, so a card joins a room of its own and wakes the
// same collectors the page would.
//
// ── TWO PAGES ARE CHECKED, NOT ONE ──────────────────────────────────────────
//
// A card needs the DASHBOARD and the page it borrows its data from. Streaming
// firewall detail through a dashboard card to someone denied the Firewall page
// would make the whole permission matrix a lie — and it is the first thing an
// operator checks. Both are required before the room is joined, and the join and
// the replay are gated together: gating only the join would still hand the
// caller a payload they may not see.
//
// ── THE KEY IS VALIDATED, NOT ESCAPED ───────────────────────────────────────
//
// It becomes part of a room name, and room names are how payloads are addressed.
// A key outside `[a-z]{2,20}` is refused outright rather than sanitised, because
// there is no such thing as a card whose name needed cleaning up.

import (
	"regexp"
	"sort"
)

var dashCardKeyRe = regexp.MustCompile(`^[a-z]{2,20}$`)

// dashCardPage is the page a card room borrows its data from.
//
// The live app resolves this as "the page with this key, else the page owning
// the collector with this key, else dashboard". Seven of the eight rooms the
// grid can ask for ARE page keys and resolve to themselves; `diagnostics` is
// neither a page nor a collector and falls back to `dashboard`.
//
// Written as an explicit map rather than a lookup through a page registry this
// port does not have, and checked against the live resolution by
// The grid table generator — so a room added over there fails here rather than
// silently resolving to `dashboard` and being gated on the wrong page.
var dashCardPages = map[string]string{
	"firewall":    "firewall",
	"logs":        "logs",
	"vpn":         "vpn",
	"connections": "connections",
	"wireless":    "wireless",
	"interfaces":  "interfaces",
	"dhcp":        "dhcp",
	"diagnostics": "dashboard",
}

// dashCardPage resolves a card room key to the page that gates it. An unknown
// key gates on the dashboard alone, which is the live fallback.
func dashCardPage(key string) string {
	if p, ok := dashCardPages[key]; ok {
		return p
	}
	return "dashboard"
}

// dashCardRooms maps a card's room KEY to the room its collector actually emits
// to, where the two differ.
//
// ── TWO KEYS DO NOT NAME THEIR OWN ROOM ───────────────────────────────────
//
// `CARD_ROOMS` (lifted verbatim from live's `dashboard-grid.js`) gives
// `dc-card-physports` the key `interfaces` and `card-network` the key `dhcp`,
// while the collectors emit to `dash-card-physports` and `dash-card-network`.
// Joining `dash-card-` + key therefore subscribes a browser to a room NOTHING
// EVER SENDS TO, for both cards.
//
// Live has the identical mismatch and gets away with it: its cards are painted
// by the connect-time replay in `sendInitialState`, and its router session is
// long-lived so a payload is already waiting. This port creates the session when
// a browser selects a router, so the replay races the collector's first tick —
// `ifStatus` polls every 5s and usually wins, `dhcpNetworks` polls every 600s and
// does not. That is the whole difference between Physical Ports looking fine and
// Network showing an em dash.
//
// Aliasing the JOIN is the smallest change that makes both correct: the emitted
// rooms stay exactly live's (so `emit-rooms-audit` still passes), the blur guards
// in ws.go already name `dash-card-network`, and the cards now receive ONGOING
// updates rather than one replay they might have missed.
//
// `TestEveryCardRoomIsEmittedTo` pins the property this table exists to hold.
var dashCardRooms = map[string]string{
	"interfaces": "physports",
	"dhcp":       "network",
}

func (cn *conn) dashCardRoom(key string) string {
	if alias, ok := dashCardRooms[key]; ok {
		key = alias
	}
	return "router-" + cn.routerID + "-dash-card-" + key
}

func (cn *conn) dashCardFocus(key string) {
	if !dashCardKeyRe.MatchString(key) {
		return
	}
	// ── REMEMBERED EVEN BEFORE A ROUTER IS SELECTED ───────────────────────
	//
	// The grid sends `dashcard:focus` as soon as it lays out, which is BEFORE
	// the client has sent `router:select`. This returned early on an empty
	// routerID, so every subscription was dropped in silence and three dashboard
	// cards — Connections, Network, Physical Ports — never received their
	// payload. Measured 2026-08-29 after the operator reported cards with no
	// data: the browser sends the frames, the server discards them, and sending
	// the identical frames AFTER a select delivers all three.
	//
	// The live app cannot have this. `socket.routerId` is set while the
	// connection is being established, so by the time any client frame arrives a
	// router is already known; `router:select` is this port's own arrangement.
	//
	// So the request is recorded whatever the order, and `selectRouter` replays
	// it. Recording rather than rejecting is also what makes a router SWITCH
	// keep the cards subscribed — the rooms are per router, so they have to be
	// rejoined against the new one anyway.
	cn.mu.Lock()
	if cn.cards == nil {
		cn.cards = map[string]bool{}
	}
	cn.cards[key] = true
	cn.mu.Unlock()

	if cn.routerID == "" {
		return
	}
	if !cn.canPage("dashboard", "read") {
		return
	}
	src := dashCardPage(key)
	// `dashboard` itself is already checked above; checking it twice would be
	// harmless but reads as though a second, different permission were involved.
	if src != "dashboard" && !cn.canPage(src, "read") {
		return
	}
	cn.srv.hub.Join(cn.c, cn.dashCardRoom(key))
	// The SAME wake a page focus performs, through the page this card borrows
	// from — see resumePage.
	cn.resumePage(src)
}

func (cn *conn) dashCardBlur(key string) {
	if !dashCardKeyRe.MatchString(key) {
		return
	}
	cn.mu.Lock()
	delete(cn.cards, key)
	cn.mu.Unlock()
	if cn.routerID == "" {
		return
	}
	// No permission check on the way OUT. Leaving a room you are not in is a
	// no-op, and refusing to let someone leave because their permissions changed
	// while they were watching would strand them in it.
	cn.srv.hub.Leave(cn.c, cn.dashCardRoom(key))
}

// rejoinCards re-applies the client's card subscriptions to the CURRENT router.
//
// Called from `selectRouter`, for the two reasons the comment on dashCardFocus
// gives: the grid subscribes before any router is selected, and the rooms are
// per router so a switch has to rejoin them anyway.
func (cn *conn) rejoinCards() {
	cn.mu.Lock()
	keys := make([]string, 0, len(cn.cards))
	for k := range cn.cards {
		keys = append(keys, k)
	}
	cn.mu.Unlock()
	// Sorted so the replay order is stable — two sign-ins produce the same log.
	sort.Strings(keys)
	for _, k := range keys {
		cn.dashCardFocus(k)
	}
}
