package collect

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestNoEmitPassesARoomLiteral is what replaces the bridge.
//
// ── THE INVARIANT THE WHOLE OF 4.2 RESTS ON ─────────────────────────────────
//
// Every audience is declared in rooms.go and read from there. The value of that
// is entirely in COMPLETENESS: one call site left with a literal is one place the
// old two-facts bug can grow back, and it would look perfectly ordinary.
//
// So this is one rule replacing three pattern-matchers. It also removes the
// exemptions the old source-scanning gates carried for `logs` and `talkers`,
// which emitted to named constants and could not be checked at all.
//
// THE ROUTER-WIDE EMIT IS THE ONE ALLOWED LITERAL, and it is not a room: `""`
// means every viewer of this router. Five collectors use it.
func TestNoEmitPassesARoomLiteral(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	// Any emit whose first argument is a non-empty string literal.
	bad := regexp.MustCompile(`emit\("[^"]+"`)
	found := 0
	for _, f := range files {
		n := f.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		src, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		found++
		for _, m := range bad.FindAllString(stripComments(string(src)), -1) {
			t.Errorf("%s: %s — rooms belong in rooms.go. A literal here is a second "+
				"statement of the audience, and the blur guard reads the first one.", n, m)
		}
	}
	if found < 20 {
		t.Fatalf("read %d collector files; this check has stopped seeing the package", found)
	}
}

// stripComments removes // lines so a comment QUOTING an old emit does not fail
// the check that forbids it.
//
// The same trap has now been hit twice in this repository — the credential
// scanner reading a comment about proplists, and a dormancy gate reading its own
// explanation — so it is handled here rather than discovered a third time.
func stripComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// TestEveryDeclaredRoomIsAPageOrACard: a room name is a protocol term shared
// with the browser, and a typo in one is silent — the emit goes to a room nobody
// has joined, and the page simply never updates.
func TestEveryDeclaredRoomIsAPageOrACard(t *testing.T) {
	for _, key := range declaredKeys() {
		for _, r := range RoomsOf(key) {
			if !strings.HasPrefix(r, "page-") && !strings.HasPrefix(r, "dash-card-") {
				t.Errorf("%s declares room %q, which is neither a page room nor a card "+
					"room. Those are the only two namespaces the browser joins.", key, r)
			}
		}
	}
}

// TestDemandRoomsIsTheAudiencePlusTheDependencies pins the calculation the
// demand rule asks for, and it is the re-aimed form of
// `TestOthersDropsTheBlurredPageAndNothingElse`.
//
// ── WHAT THE OLD TEST ASSERTED, AND WHY IT STOPPED BEING A QUESTION ────────
//
// `Others(key, blurredPage)` was the audience minus one page — the calculation
// the seven blur guards used to spell out by hand. Phase 4.2b deleted the
// switchboard, so no page is ever blurred at a collector any more and there is
// nothing to subtract. The function is gone; the risk it covered moved rather
// than closed, and it is the same risk in both halves:
//
//	too few rooms   the collector suspends while somebody is watching it
//	too many rooms  the collector never suspends and keeps asking a router
//	                nobody is looking at
//
// So the cases carry over almost unchanged, asserted against the set demand
// actually reads.
func TestDemandRoomsIsTheAudiencePlusTheDependencies(t *testing.T) {
	cases := []struct {
		key  string
		want []string
	}{
		{"vpn", []string{"page-vpn", "dash-card-vpn"}},
		{"routing", []string{"page-routing", "page-dashboard"}},
		{"dhcpNetworks", []string{"page-dhcp", "dash-card-network"}},
		// `page-wifi-map` has been in the audience since the map page landed: it
		// draws live clients around pinned access points, so the collector has
		// to keep running for a viewer who is only on that page.
		{"wireless", []string{"page-wifi-clients", "page-wifi-map", "dash-card-wireless"}},
		// `page-bandwidth` was a `keepAliveFor` entry on `conns` until
		// 2026-09-09: `bandwidth` read the connection table `conns` deposited in
		// `ConnTable`, so suspending `conns` starved a page it never emits to.
		// Both collectors subscribe to the menu now and `bandwidth` holds its own
		// demand, so a suspended `conns` starves nothing.
		{"conns", []string{"page-connections", "dash-card-connections"}},
		// ── THE ONE KEEP-ALIVE ENTRY, AND THE REASON THE MAP EXISTS ─────────
		//
		// `ifStatus` emits to three rooms and is the `RateSource` for five
		// collectors. Four of them live on pages it sends nothing to, so demand
		// reading `RoomsOf` alone would suspend it for a viewer on Bridges and
		// blank every throughput column there, on VLANs, on WAN and on Bandwidth.
		//
		// `page-network-topology` appears once, not twice: topology is both a
		// consumer AND part of the audience, and `union` is what makes that a
		// non-event.
		{"ifStatus", []string{
			"page-interfaces", "page-network-topology", "dash-card-physports",
			"page-bridges", "page-vlans", "page-wan",
			"page-bandwidth", "dash-card-bandwidth",
		}},
		// ── AND THE COLLECTOR WITH NO AUDIENCE AT ALL ──────────────────────
		//
		// `dhcpLeases` emits router-wide, so `RoomsOf` is empty and every room
		// here is a keep-alive: the DHCP page it shares with `dhcpNetworks`, and
		// the four collectors that take it as a source.
		{"dhcpLeases", []string{
			"page-dhcp", "dash-card-network",
			"page-connections", "dash-card-connections",
			"page-wifi-clients", "page-wifi-map", "dash-card-wireless",
			"page-network-topology",
			"page-bandwidth", "dash-card-bandwidth",
		}},
		// A collector with no keep-alive entry gets its audience back unchanged.
		{"dns", []string{"page-dns"}},
	}
	for _, c := range cases {
		got := append([]string(nil), DemandRooms(c.key)...)
		sort.Strings(got)
		want := append([]string(nil), c.want...)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("DemandRooms(%q) = %v, want %v", c.key, got, want)
		}
	}
}

// TestKeepAliveRoomsAreRealAndNotAlreadyTheAudience.
//
// ── A KEEP-ALIVE ENTRY THAT SAYS NOTHING IS THE FAILURE MODE ───────────────
//
// The map is hand-written — it encodes an in-process dependency that no emit
// declares — so the two ways it goes wrong are both silent. A room nobody ever
// occupies keeps a collector running forever; a room already in the audience
// reads as a dependency being handled when it is merely a duplicate, and
// deleting the real dependency would then change nothing visible.
func TestKeepAliveRoomsAreRealAndNotAlreadyTheAudience(t *testing.T) {
	// Every room any collector declares, so an entry naming a room nothing feeds
	// is caught rather than believed.
	real := map[string]bool{}
	for _, k := range declaredKeys() {
		for _, r := range RoomsOf(k) {
			real[r] = true
		}
	}
	if len(keepAliveFor) == 0 {
		t.Fatal("keepAliveFor is empty; this test is asserting nothing. If the last " +
			"entry closed, say so here rather than leaving a check that cannot fail.")
	}
	for key, rooms := range keepAliveFor {
		// A collector with NO audience is the case this map exists for at its
		// strongest -- `dhcpLeases` emits router-wide, so nothing is guardable and
		// every room it needs is here. So an empty audience is not an error; an
		// empty ENTRY is, and that is what the length check below catches.
		if len(rooms) == 0 {
			t.Errorf("keepAliveFor[%q] is empty, which is the same as having no entry "+
				"while reading as though a dependency were recorded", key)
		}
		own := map[string]bool{}
		for _, r := range RoomsOf(key) {
			own[r] = true
		}
		for _, r := range rooms {
			if !real[r] {
				t.Errorf("keepAliveFor[%q] names room %q, which no collector feeds. "+
					"Nobody can ever occupy it, so it keeps %s running forever.", key, r, key)
			}
			if own[r] {
				t.Errorf("keepAliveFor[%q] names %q, which is already in its own audience. "+
					"The entry changes nothing, and reads as though the dependency were "+
					"covered when removing the real one would be silent.", key, r)
			}
		}
	}
}

// TestDeclaredRoomKeysMatchesTheSwitch: the exported list and `RoomsOf` must
// cover the same collectors, in both directions.
//
// A key in the list that the switch does not answer for reports an empty
// audience, and demand then suspends that collector for ever. A key the switch
// answers for that the list omits is invisible to every caller that enumerates.
func TestDeclaredRoomKeysMatchesTheSwitch(t *testing.T) {
	exported := map[string]bool{}
	for _, k := range DeclaredRoomKeys() {
		exported[k] = true
		if len(RoomsOf(k)) == 0 {
			t.Errorf("DeclaredRoomKeys names %q and RoomsOf answers nothing for it", k)
		}
	}
	for _, k := range declaredKeys() {
		if !exported[k] {
			t.Errorf("%q declares rooms and DeclaredRoomKeys omits it, so nothing that "+
				"enumerates collectors can see it", k)
		}
	}
	if len(DeclaredRoomKeys()) != len(declaredKeys()) {
		t.Errorf("DeclaredRoomKeys has %d entries, this file's own list has %d",
			len(DeclaredRoomKeys()), len(declaredKeys()))
	}
}

// declaredKeys is this test file's OWN list, deliberately not the exported one:
// a check that read the list it is checking would prove anything it names.
func declaredKeys() []string {
	return []string{
		"bandwidth", "bridges", "capsman", "conns", "dhcpNetworks", "dns",
		"firewall", "ifStatus", "logs", "netwatch", "packages", "ping", "ppp",
		"queues", "rosusers", "routing", "talkers", "topology", "vlans", "vpn",
		"wan", "wifi", "wireless",
	}
}

// TestDeclaredKeysCoverRoomsOf stops the list above drifting from the switch it
// describes — a key dropped from one and not the other makes the two checks
// above quietly stop looking at it.
func TestDeclaredKeysCoverRoomsOf(t *testing.T) {
	for _, key := range declaredKeys() {
		if len(RoomsOf(key)) == 0 {
			t.Errorf("declaredKeys names %q and RoomsOf returns nothing for it", key)
		}
	}
	if got := len(declaredKeys()); got != 23 {
		t.Errorf("declaredKeys has %d entries, expected 23 — a collector gained or lost "+
			"an audience and one of these lists was not updated", got)
	}
}
