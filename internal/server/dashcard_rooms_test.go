package server

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
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
	// What the browser asks for: the values of CARD_ROOMS.
	gen, err := os.ReadFile("../../web/src/gen/grid-tables.ts")
	if err != nil {
		t.Fatalf("reading the generated grid tables: %v", err)
	}
	src := string(gen)
	i := strings.Index(src, "CARD_ROOMS")
	if i < 0 {
		t.Fatal("CARD_ROOMS is gone from the generated tables — this test measures nothing")
	}
	block := src[i:]
	if j := strings.Index(block, "};"); j >= 0 {
		block = block[:j]
	}
	keys := map[string]bool{}
	for _, m := range regexp.MustCompile(`"[^"]+":\s*"([^"]+)"`).FindAllStringSubmatch(block, -1) {
		keys[m[1]] = true
	}
	if len(keys) == 0 {
		t.Fatal("no card room keys found — this test measures nothing")
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

	var orphans []string
	for k := range keys {
		room := k
		if alias, ok := dashCardRooms[k]; ok {
			room = alias
		}
		if !emitted[room] {
			orphans = append(orphans, k+" -> dash-card-"+room)
		}
	}
	sort.Strings(orphans)

	// `diagnostics` has no collector at all — live computes it in the handler —
	// so it is the one key legitimately without an emit site.
	var real []string
	for _, o := range orphans {
		if !strings.HasPrefix(o, "diagnostics ") {
			real = append(real, o)
		}
	}
	if len(real) > 0 {
		t.Errorf("%d card room(s) that no collector emits to: %v\n"+
			"A browser joining one of these is subscribed to silence — the card shows "+
			"whatever the connect-time replay happened to catch and never updates. Add an "+
			"entry to dashCardRooms, or fix the emit.", len(real), real)
	}
}
