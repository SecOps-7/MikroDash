package server

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/dashcards"
)

// EVERY CARD ROOM A BROWSER JOINS MUST BE A ROOM SOMETHING EMITS TO.
//
// ── THE DEFECT ────────────────────────────────────────────────────────────
//
// A card's room KEY comes from `CARD_ROOMS`, lifted verbatim from live's
// dashboard-grid.js, and two of its keys do not name their own room:
// `dc-card-physports` uses the key `interfaces` and `card-network` uses `dhcp`,
// while the collectors emit to `dash-card-physports` and `dash-card-network`.
// Joining `dash-card-` + key subscribed the browser to a room nothing ever sent
// to — silently, because an empty room is indistinguishable from a quiet one.
//
// The visible symptom was the operator's: dashboard cards with no data. It hid
// behind the connect-time replay, which paints the card once if the collector
// happens to have produced already — so the fast collector's card looked fine
// and the ten-minute one's did not.
//
// ── WHY THIS IS A SOURCE PIN ──────────────────────────────────────────────
//
// The property is a JOIN matching an EMIT, across two files and a generated
// table. No request exercises it: the browser subscribes, the server accepts,
// and nothing fails — the card simply never updates.
func TestEveryCardRoomIsEmittedTo(t *testing.T) {
	// ── THE SOURCE MOVED, AND SO DID THIS ────────────────────────────────────
	//
	// This used to regex-scan `web/src/gen/grid-tables.ts` for CARD_ROOMS. That
	// file is now GENERATED from `internal/dashcards`, so scanning it would ask
	// the generated copy a question its source can answer — and would keep
	// passing if the generator broke.
	//
	// Reading the declaration also makes the REVERSE direction possible, which
	// the old shape could not express: see below.
	keys := map[string]bool{}
	for _, r := range dashcards.Rooms() {
		keys[r] = true
	}
	if len(keys) == 0 {
		t.Fatal("no card room keys declared — this test measures nothing")
	}

	// What the collectors emit to.
	emitted := map[string]bool{}
	dir := "../../internal/collect"
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(dir + "/" + f.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`dash-card-([a-z]+)`).FindAllStringSubmatch(string(b), -1) {
			emitted[m[1]] = true
		}
	}
	if len(emitted) == 0 {
		t.Fatal("no dash-card emits found — the scan has stopped matching")
	}

	// ── FORWARD: a card joining a room nothing sends to ──────────────────────
	//
	// The browser is subscribed to silence. The card shows whatever the
	// connect-time replay happened to catch and never updates, which is
	// indistinguishable from a quiet router.
	var orphans []string
	for k := range keys {
		// TWO ROOMS ARE FED BY THIS PROCESS, not by a collector, so they have no
		// emit site to find and are not orphans.
		//
		// `diagnostics` reports on this process's own counters. `agent` (#98) is
		// the model's status line, assembled in `ai_overview.go` from payloads
		// the collectors already produced — no menu, no cadence to negotiate,
		// nothing to put in internal/collect.
		//
		// Recorded by name rather than by widening the rule: a card added with a
		// genuinely missing emit must still fail here, and "cards fed by the
		// server" is not a property this scan can see for itself.
		if k == "diagnostics" || k == "agent" {
			continue
		}
		if room := dashcards.EmitRoom(k); !emitted[room] {
			orphans = append(orphans, k+" -> dash-card-"+room)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("%d card room(s) that no collector emits to: %v\n"+
			"A browser joining one of these is subscribed to silence. Give the card an "+
			"Emits alias, or fix the emit.", len(orphans), orphans)
	}

	// ── REVERSE: a collector emitting to a room no card claims ───────────────
	//
	// NEW, and only expressible now that the rooms are declared rather than
	// scraped. Work the server does for nobody, and indistinguishable from a
	// card whose declaration was deleted by accident — which is exactly how the
	// forward half started.
	claimed := map[string]bool{}
	for _, r := range dashcards.Rooms() {
		claimed[dashcards.EmitRoom(r)] = true
	}
	var unclaimed []string
	for room := range emitted {
		if !claimed[room] {
			unclaimed = append(unclaimed, "dash-card-"+room)
		}
	}
	sort.Strings(unclaimed)
	if len(unclaimed) > 0 {
		t.Errorf("%d room(s) a collector emits to that no card claims: %v\n"+
			"Either a card declaration was lost, or the emit is work done for nobody.",
			len(unclaimed), unclaimed)
	}
}
