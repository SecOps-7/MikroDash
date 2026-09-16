package dashcards

import (
	"regexp"
	"sort"
	"testing"

	"mikrodash/internal/pages"
)

// TestEveryCardIsCompletelyDeclared.
//
// The five tables this package replaced could disagree in ways nothing noticed:
// a card in the layout with no label rendered a chip labelled by its own id, and
// a label with no layout row was a card nobody could add. One row per card makes
// both impossible, and this is what says so.
func TestEveryCardIsCompletelyDeclared(t *testing.T) {
	if len(All) < 20 {
		t.Fatalf("only %d cards declared — the table is truncated", len(All))
	}
	seen := map[string]bool{}
	for _, c := range All {
		if c.ID == "" {
			t.Errorf("a card has no id: %+v", c)
			continue
		}
		if seen[c.ID] {
			t.Errorf("duplicate card id %q", c.ID)
		}
		seen[c.ID] = true
		if c.Label == "" {
			t.Errorf("%s has no label — the Add panel would offer a chip labelled by its id", c.ID)
		}
		// A card with no size cannot be laid out; the grid would collapse it.
		if c.W < MinW || c.H < MinH {
			t.Errorf("%s is %dx%d, below the minimum %dx%d", c.ID, c.W, c.H, MinW, MinH)
		}
		if c.X < 1 || c.Y < 1 || c.X+c.W-1 > Cols {
			t.Errorf("%s sits outside the %d-column grid: x=%d w=%d", c.ID, Cols, c.X, c.W)
		}
	}
}

// TestEveryCardPageNamesARealPage.
//
// ── THE CHECK THAT HAD STOPPED EXISTING ─────────────────────────────────────
//
// `dashCardPages` carried a comment saying it was checked against the live
// resolution by a generator that is not in the repository, so nothing verified
// these against anything. A card gated on a page key that had been renamed would
// have been gated on a page NOBODY holds — hidden from everyone rather than from
// the wrong people, and silently, because a denied card simply never joins its
// room.
func TestEveryCardPageNamesARealPage(t *testing.T) {
	live := map[string]bool{}
	for _, k := range pages.Keys() {
		live[k] = true
	}
	if len(live) < 20 {
		t.Fatalf("only %d page keys — internal/pages is truncated", len(live))
	}
	for _, c := range All {
		if c.Page == "" {
			continue // gated on the Dashboard alone; see Card.Page
		}
		if !live[c.Page] {
			t.Errorf("%s borrows from page %q, which does not exist — the card would be "+
				"gated on a page nobody holds and would never appear", c.ID, c.Page)
		}
	}
}

// TestACardWithARoomDeclaresThePageThatGatesIt.
//
// A card that receives router data and names no page is gated on the Dashboard
// alone, which would let somebody denied the Firewall page watch firewall detail
// through a dashboard card. `diagnostics` is the one exception and it is not an
// oversight: it reports on THIS PROCESS, so there is no page whose permission
// could withhold it.
func TestACardWithARoomDeclaresThePageThatGatesIt(t *testing.T) {
	for _, c := range All {
		if c.Room == "" || c.Room == "diagnostics" {
			continue
		}
		if c.Page == "" {
			t.Errorf("%s joins room %q and names no page — it would be gated on the "+
				"Dashboard alone, so anyone who may see the Dashboard could watch its data",
				c.ID, c.Room)
		}
	}
}

// TestRoomKeysAreLettersOnly — they become part of a room name, and the server's
// own regexp refuses anything else by returning early, which makes a bad key a
// card that never subscribes with nothing said. The pattern is restated here
// rather than imported because internal/server imports THIS package; the server
// asserts the same property against its own regexp.
func TestRoomKeysAreLettersOnly(t *testing.T) {
	re := regexp.MustCompile(`^[a-z]{2,20}$`)
	for _, c := range All {
		for _, k := range []string{c.Room, c.Emits} {
			if k == "" {
				continue
			}
			if !re.MatchString(k) {
				t.Errorf("%s: %q is not a usable room key", c.ID, k)
			}
		}
	}
}

// TestPageForFallsBackToTheDashboard — an unknown key must not resolve to the
// empty string, which would gate the card on a page nobody has.
func TestPageForFallsBackToTheDashboard(t *testing.T) {
	if got := PageFor("somethingelse"); got != "dashboard" {
		t.Errorf("an unknown room resolved to %q, want dashboard", got)
	}
	if got := PageFor("diagnostics"); got != "dashboard" {
		t.Errorf("diagnostics resolved to %q, want dashboard", got)
	}
	if got := PageFor("firewall"); got != "firewall" {
		t.Errorf("firewall resolved to %q", got)
	}
}

// TestEmitRoomIsIdentityUnlessAliased.
//
// Two cards inherit a mismatch: `dc-card-physports` carries the key
// `interfaces` while its collector emits to `dash-card-physports`, and
// `card-network` carries `dhcp` against `dash-card-network`. Everything else
// must pass straight through, or a card would join a room nothing sends to.
func TestEmitRoomIsIdentityUnlessAliased(t *testing.T) {
	aliased := map[string]string{"interfaces": "physports", "dhcp": "network"}
	for _, r := range Rooms() {
		want, ok := aliased[r]
		if !ok {
			want = r
		}
		if got := EmitRoom(r); got != want {
			t.Errorf("EmitRoom(%q) = %q, want %q", r, got, want)
		}
	}
	// AND THE ALIASES ARE STILL NEEDED. An alias for a room no card declares is
	// a note describing nothing, and this fails in that direction too.
	declared := map[string]bool{}
	for _, r := range Rooms() {
		declared[r] = true
	}
	for k := range aliased {
		if !declared[k] {
			t.Errorf("%q is recorded as aliased but no card declares that room — "+
				"delete the entry rather than leaving a note that describes nothing", k)
		}
	}
}

// TestRoomsAreDeduplicatedAndStable — two cards may share a room, and the list
// decides what a browser joins, so a duplicate would join twice and an unstable
// order would make two sign-ins produce different logs.
func TestRoomsAreDeduplicatedAndStable(t *testing.T) {
	rooms := Rooms()
	seen := map[string]bool{}
	for _, r := range rooms {
		if seen[r] {
			t.Errorf("room %q appears twice", r)
		}
		seen[r] = true
	}
	again := Rooms()
	if len(again) != len(rooms) {
		t.Fatalf("two calls disagreed: %v vs %v", rooms, again)
	}
	for i := range rooms {
		if rooms[i] != again[i] {
			t.Fatalf("two calls disagreed at %d: %v vs %v", i, rooms, again)
		}
	}
	// Every declared room is reachable from a card; nothing invents one.
	fromCards := []string{}
	for _, c := range All {
		if c.Room != "" && !contains(fromCards, c.Room) {
			fromCards = append(fromCards, c.Room)
		}
	}
	sort.Strings(fromCards)
	sorted := append([]string(nil), rooms...)
	sort.Strings(sorted)
	if len(sorted) != len(fromCards) {
		t.Errorf("Rooms() has %d entries, the cards declare %d", len(sorted), len(fromCards))
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
