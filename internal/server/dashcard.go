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

	"mikrodash/internal/dashcards"
)

var dashCardKeyRe = regexp.MustCompile(`^[a-z]{2,20}$`)

// dashCardPage resolves a card room key to the page that gates it.
//
// ── THE TABLE MOVED, AND THE CROSS-CHECK CAME BACK WITH IT ──────────────────
//
// This used to be a map here, beside a second map of emit aliases, while the
// browser held three more tables of the same fact. The comment on it said it was
// "checked against the live resolution by the grid table generator" — a
// generator that is not in `tools/` and has not run since the cutover, so the
// check it named had quietly stopped existing.
//
// `internal/dashcards` is the one declaration now, `cmd/gridgen` emits the
// browser's view of it, and `-check` runs in tools/verify.sh.
func dashCardPage(key string) string { return dashcards.PageFor(key) }

// dashCardRoom is the room this client joins for a card.
//
// TWO KEYS DO NOT NAME THEIR OWN ROOM. `dc-card-physports` carries the key
// `interfaces` and `card-network` carries `dhcp`, while the collectors emit to
// `dash-card-physports` and `dash-card-network`. Joining `dash-card-` + key
// would subscribe a browser to a room NOTHING EVER SENDS TO, which is
// indistinguishable from a quiet one — the card shows whatever the connect-time
// replay happened to catch and never updates again.
func (cn *conn) dashCardRoom(key string) string {
	return "router-" + cn.routerID + "-dash-card-" + dashcards.EmitRoom(key)
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
	// ── THE ONE CARD NO COLLECTOR FEEDS ────────────────────────────────────
	//
	// Every other card is painted by a collector that `resumePage` has just
	// woken. This one reports on THIS PROCESS, so there is nothing to wake and
	// nothing would ever arrive — which is why it rendered empty for the whole
	// life of the port. See internal/server/diagnostics.go.
	if key == "diagnostics" {
		cn.diagFocus()
	}
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
	if key == "diagnostics" {
		cn.diagBlur()
	}
	// ── PHASE 4.2b: A CARD BLUR NOW STOPS SOMETHING ───────────────────────
	//
	// It never did before, and the asymmetry was invisible because the page
	// switchboard was the only thing that suspended anything. So a collector
	// whose ONLY viewer was a dashboard card — `netwatch`, `talkers`, the
	// Physical Ports card's `ifStatus` — ran until the idle gate or dormancy
	// reached it, however long ago the card was removed from the grid.
	//
	// The room is left above; this re-asks the question for every collector, and
	// the answer for this one changes only if nothing else is watching it.
	cn.srv.applyDemand(cn.rsession, cn.routerID)
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
