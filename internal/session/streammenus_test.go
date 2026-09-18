package session

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/collection"
	"mikrodash/internal/roscache"
)

// TestStreamableMenusAreRealAndOwned.
//
// ── WHAT A CARELESS B.4 LINE LOOKS LIKE ────────────────────────────────────
//
// B.4 enables one collector per commit by adding a line to `streamableMenus`.
// Two ways to get that line wrong, and BOTH FAIL SILENTLY: name a menu with a
// typo, or a menu nothing actually subscribes to, and the entry matches nothing
// so the collector keeps polling — the commit claims a delivery change and makes
// none, and the measurement that follows would be of the unchanged app. Name a
// collector key the registry does not have, and `eff.Stream[key]` reads false
// for every router, which is the same outcome by a different route.
//
// Neither breaks a page, so nothing else in the suite would notice.
func TestStreamableMenusAreRealAndOwned(t *testing.T) {
	if len(streamableMenus) == 0 {
		// EMPTY IS THE CORRECT STATE UNTIL B.4, and this is not a skip: the
		// checks below have nothing to say, but the file being present and
		// parsed is what stops the table being deleted as unused.
		return
	}

	subscribed := subscribedMenusInCollect(t)
	keys := map[string]bool{}
	for _, c := range collection.Collectors() {
		keys[c.Key] = true
	}

	for menu, key := range streamableMenus {
		// ── AND THE CACHE MUST BE WILLING TO STREAM IT ──────────────────────
		//
		// `roscache` refuses two kinds of menu: rows that are not readings of one
		// value (`ping`, `logs`), and tables whose MEMBERSHIP churns, because the
		// rolling map never forgets and departed rows would accumulate for ever.
		//
		// A line here naming one of those is SAFE -- `fillIfStreaming` falls back
		// to polling on any refusal -- and MISLEADING, which is worse in a table
		// whose whole purpose is to record what has been enabled. The commit
		// would claim a delivery change and make none.
		if why, no := roscache.Unrollable(menu); no {
			t.Errorf("streamableMenus lists %q, which roscache refuses: %s.\nThe entry "+
				"would fall back to polling, so it changes nothing while claiming to.",
				menu, why)
		}
		if !subscribed[menu] {
			t.Errorf("streamableMenus lists %q, which no collector subscribes to. The "+
				"entry matches nothing, so the collector goes on polling and the "+
				"commit that added it changed no delivery at all.", menu)
		}
		if !keys[key] {
			t.Errorf("streamableMenus maps %q to collector %q, which is not in the "+
				"registry. eff.Stream[%q] is false for every router, so this menu "+
				"never streams.", menu, key, key)
		}
	}
}

// subscribedMenusInCollect resolves `scheduled{menu: xxxCmd.Path}` back to the
// path `xxxCmd` declares, per collector file.
//
// The same technique `internal/verify`'s shared-menu ledger uses, and for the
// same reason: a subscription names its menu through a variable, so the only way
// to know which menu a collector routes is to resolve the declaration.
func subscribedMenusInCollect(t *testing.T) map[string]bool {
	t.Helper()
	dir := filepath.Join("..", "collect")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	decl := regexp.MustCompile(`(\w+)\s*=\s*routeros\.Cmd\{\s*Path:\s*"(/[^"]+)"`)
	// `cmd: xxxCmd,` is a table collector's tableSpec (internal/collect/table.go).
	sub := regexp.MustCompile(`menu:\s*(\w+)\.Path|\bcmd:\s*(\w+),`)

	out := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		paths := map[string]string{}
		for _, m := range decl.FindAllStringSubmatch(src, -1) {
			paths[m[1]] = m[2]
		}
		for _, m := range sub.FindAllStringSubmatch(src, -1) {
			if p, ok := paths[m[1]+m[2]]; ok {
				out[p] = true
			}
		}
	}
	if len(out) < 10 {
		t.Fatalf("found only %d subscribed menu(s); the `menu: xxxCmd.Path` pattern "+
			"has stopped matching and this check is measuring nothing", len(out))
	}
	return out
}

// TestAPinnedPollRouterIsNotOverriddenByTheTable. Enabling a collector in B.4 is
// this project saying a menu is SAFE to stream. It must not override the
// operator's own per-router choice, which is the whole point of the toggle the
// automatic switch was rejected in favour of.
func TestAPinnedPollRouterIsNotOverriddenByTheTable(t *testing.T) {
	const menu, key = "/ip/dns/print", "dns"
	// RESTORED, NOT DELETED. This was written when the table was empty, and the
	// delete removed the real dns line for every test that ran after it, which
	// TestEveryStreamableMenusOwnerCanResolveAStream then reported as missing.
	prev, had := streamableMenus[menu]
	streamableMenus[menu] = key
	defer func() {
		if had {
			streamableMenus[menu] = prev
		} else {
			delete(streamableMenus, menu)
		}
	}()

	s := &Session{}
	s.eff.Store(&collection.Resolved{Stream: map[string]bool{key: false}}) // the operator pinned this router
	if s.streamsMenu(menu) {
		t.Error("a router with the collector set to Poll streamed anyway. The table " +
			"says a menu CAN be streamed; eff.Stream says whether this router does.")
	}

	s.eff.Store(&collection.Resolved{Stream: map[string]bool{key: true}})
	if !s.streamsMenu(menu) {
		t.Error("a router set to Stream, on a menu the table allows, did not stream")
	}

	// And the other direction: allowed by the router, absent from the table.
	if s.streamsMenu("/ip/route/print") {
		t.Error("a menu absent from the table streamed. The table is the gate that " +
			"keeps B.4 to one collector at a time.")
	}
}

// TestEveryStreamableMenusOwnerCanResolveAStream.
//
// A line in `streamableMenus` is half of the decision; the other half is
// `Resolved.Stream[owner]`, and `collection.Resolve` can only answer true for a
// collector that cannot poll or that has a `streamKey`. A line whose owner has
// neither changes no delivery for any router while reading as enabled, and the
// check above cannot see that: the menu is real and the owner exists.
//
// Found 2026-09-15 by checking the IP Addresses line on a live router, where the
// Diagnostics card listed the menu as polled. `dns`, `rosusers` and `packages`
// had been in the same state since they were added.
func TestEveryStreamableMenusOwnerCanResolveAStream(t *testing.T) {
	rows := map[string]collection.Collector{}
	for _, c := range collection.Collectors() {
		rows[c.Key] = c
	}
	for menu, key := range streamableMenus {
		c, ok := rows[key]
		if !ok {
			continue // TestStreamableMenusAreRealAndOwned reports it
		}
		if c.Pollable && c.StreamKey == "" {
			t.Errorf("streamableMenus lists %q for %q, which has no streamKey and can poll, so "+
				"collection.Resolve never lets it stream. The line changes no delivery.", menu, key)
		}
	}
}

// TestNoMetadataCollectorStreams — the operator's rule: live data may stream,
// metadata polls.
//
// A stream holds a channel open for the life of the subscription. For a
// collector whose subscribed menu is configuration, that is a channel spent
// re-sending a table that has not changed, on hardware whose limit is concurrent
// channels. Three such lines streamed until 2026-09-15.
//
// A LEDGER, failing both ways: a metadata collector with a line fails, and so
// does an entry naming a collector the registry no longer has.
func TestNoMetadataCollectorStreams(t *testing.T) {
	metadata := map[string]string{
		"wifi":         "interface and radio configuration, subscribed as the slow half; the clients are wireless's",
		"vlans":        "the VLAN interface list, subscribed as the slow half; its rates come from ifStatus",
		"dhcpNetworks": "DHCP networks, with the addresses and pools read beside them",
		"dns":          "resolver settings and static entries",
		"rosusers":     "RouterOS users, groups and sessions",
		"packages":     "installed packages and firmware",
		"topology":     "the neighbour discovery table",
	}
	keys := map[string]bool{}
	for _, c := range collection.Collectors() {
		keys[c.Key] = true
	}
	for key := range metadata {
		if !keys[key] {
			t.Errorf("%q is recorded as a metadata collector and the registry has no such key", key)
		}
	}
	for menu, key := range streamableMenus {
		if why, ok := metadata[key]; ok {
			t.Errorf("streamableMenus lists %q for %q, which is metadata (%s). A stream would hold a "+
				"channel open to re-send a table that rarely changes; it polls.", menu, key, why)
		}
	}
}
