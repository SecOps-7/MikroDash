package verify

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestScheduledCollectorsAreDeclared records which collectors have moved off
// their own timers onto the router's scheduler, and - more usefully - which have
// not and why.
//
// ── WHY THIS IS A GATE AND NOT A PARAGRAPH ──────────────────────────────────
//
// Phase 3.3 proposes replacing the dormancy supervisor with per-query backoff,
// and it was BLOCKED on a number: only 8 of the 18 dormancy-eligible collectors
// were scheduled, so backoff would have covered eight while the supervisor kept
// ticking for ten -- two mechanisms with different semantics, side by side.
//
// That number was the whole argument, and a number in a document is exactly what
// this repository has watched go stale. So it re-measured itself here, and on
// 2026-09-08 it reached 18 of 18 and said so.
//
// IT WAS TURNED ROUND RATHER THAN DELETED. The property is still worth holding:
// a collector that falls off the scheduler stops being covered by backoff, and
// nothing else would notice.
//
// It fails in both directions: a collector that starts subscribing without being
// listed fails, and a listed one that stops fails too.
//
// `Unscheduled_Collectors.md` is the readable companion to the `unscheduled` map
// below -- same twelve entries, same grades, plus what would have to change for
// each. It is gitignored, like the other working documents, so THIS is the copy
// that survives a clone: keep the reasons here even when the prose moves.
func TestScheduledCollectorsAreDeclared(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "collect")

	// file -> registry key. Explicit, because the two names differ often enough
	// (conns/connections, rosusers/rosUsers) that deriving it would be a second
	// source of truth.
	scheduled := map[string]string{
		"netwatch.go":     "netwatch",
		"talkers.go":      "talkers",
		"dns.go":          "dns",
		"packages.go":     "packages",
		"rosusers.go":     "rosusers",
		"queues.go":       "queues",
		"bridges.go":      "bridges",
		"system.go":       "system",
		"connections.go":  "conns",
		"dhcpleases.go":   "dhcpLeases",
		"dhcpnetworks.go": "dhcpNetworks",
		"wan.go":          "wan",
		"capsman.go":      "capsman",
		"ppp.go":          "ppp",
		"routing.go":      "routing",
		"ifstatus.go":     "ifStatus",
		"topology.go":     "topology",
		"vpn.go":          "vpn",
		"firewall.go":     "firewall",
		"wifi.go":         "wifi",
		"wireless.go":     "wireless",
		"bandwidth.go":    "bandwidth",
		"vlans.go":        "vlans",
		// The one subscriber with no page and no payload. It reads
		// `/ip/arp/print` on the scheduler like any other table, and four
		// collectors read the index it builds. See internal/collect/arp.go.
		"arp.go": "arp",
		// ── EMBEDS THE HELPER AND SUBSCRIBES TO NOTHING ──────────────────────
		//
		// One collector for every generated page (internal/areas). Its menus are
		// the areas whose rooms are occupied, chosen per tick, so there is no
		// single menu to subscribe to and the loop IS the schedule - the
		// `firewall` shape, in its strongest form. It takes `scheduled` for the
		// cache and the start/stop lifecycle every collector shares.
		"areas.go": "areas",
	}

	// ── EVERY UNSCHEDULED COLLECTOR, AND HOW STRONG THE REASON ACTUALLY IS ──
	//
	// Kept complete rather than limited to the dormancy-eligible ones, and graded
	// on purpose. "Cannot" and "have not" are different claims, and three of the
	// four obstacles recorded under 3.2 turn out to be the second kind when
	// written down honestly. The grade is the thing to challenge.
	//
	//	IMPOSSIBLE   there is no cadence to schedule. A stream has no result and
	//	             no end; scheduling it is not a thing that means anything.
	//	NEEDS 4.2    driven by another collector's OUTPUT. Wants a subscription to
	//	             a derivation, which does not exist yet.
	//	PARTIAL      part of it is schedulable today and has not been done. These
	//	             are the weakest claims on this list.
	//	NOT TRIED    no obstacle found. Just not done.
	unscheduled := map[string]string{
		// IMPOSSIBLE.
		"logs":    "IMPOSSIBLE - /log/listen is a push channel: no result to key, no end to key it until",
		"ping":    "IMPOSSIBLE - a stream parameterised by address; two consumers are asking about different hosts",
		"traffic": "IMPOSSIBLE - a monitor stream; same menu as ifStatus's measurement, a different question",

		// NEEDS 4.2. Empty. BOTH entries here were WRONG, not merely untried,
		// and both were wrong in the same way: they described a dependency on
		// another collector's output as if it forced a duplicate read.
		//
		//	bandwidth  "takes the PARSED snapshot from connTable, so scheduling it
		//	           on the connection menu would duplicate the heaviest read in
		//	           the app". `ConnTable.Latest` returns RAW rows, and the
		//	           demand set coalesces by menu -- so two collectors wanting
		//	           the same menu is one read with two deliveries.
		//	vlans      "subscribing it to a menu would slow its rate column from 5s
		//	           to 60s". True of a plain subscription; mechanism A is the
		//	           shape for a collector whose two halves run at different
		//	           rates, and its residual half reads nothing at all.
		//
		// Kept as a heading because the lesson is the list's, not either entry's:
		// a grade written from the collector's CURRENT wiring reads like a
		// property of the problem.

		// PARTIAL. Empty since mechanism B: firewall, wifi and wireless all
		// read "possible and was not tried" underneath the grade, and all three
		// took one mechanism between them.
	}

	// A collector is on the scheduler when its file embeds the helper, or embeds
	// the table core, which embeds it (table.go). table.go is the core itself and
	// not a collector.
	marker := "sched scheduled"
	tableCore := regexp.MustCompile(`(?m)^\ttableCore\[\w+\]$`)
	for _, name := range collectGoFiles(t, dir) {
		src := mustRead(t, filepath.Join(dir, name))
		has := (name != "table.go" && strings.Contains(strings.Join(strings.Fields(src), " "), marker)) ||
			tableCore.MatchString(src)
		_, listed := scheduled[name]
		switch {
		case has && !listed:
			t.Errorf("%s subscribes to a menu and is not in the scheduled list. Add it, and "+
				"check whether it closes one of the 3.3 blockers.", name)
		case !has && listed:
			t.Errorf("%s is listed as scheduled and no longer embeds the helper. A recorded "+
				"state that has changed is a failure here.", name)
		}
	}

	// ── THE 3.3 ARITHMETIC, RE-MEASURED ─────────────────────────────────────
	var tables struct {
		Registry []struct {
			Key         string   `json:"key"`
			EmptyKey    []string `json:"emptyKey"`
			Disableable bool     `json:"disableable"`
		} `json:"registry"`
	}
	raw := mustRead(t, filepath.Join(root, "internal", "collection", "collection_tables.json"))
	if err := json.Unmarshal([]byte(raw), &tables); err != nil {
		t.Fatalf("collection_tables.json: %v", err)
	}

	onScheduler := map[string]bool{}
	for _, key := range scheduled {
		onScheduler[key] = true
	}

	var eligible, covered, missing []string
	for _, c := range tables.Registry {
		if len(c.EmptyKey) == 0 || !c.Disableable {
			continue
		}
		eligible = append(eligible, c.Key)
		if onScheduler[c.Key] {
			covered = append(covered, c.Key)
			continue
		}
		missing = append(missing, c.Key)
		if unscheduled[c.Key] == "" {
			t.Errorf("%s is dormancy-eligible, not scheduled, and has no recorded reason. "+
				"Either schedule it or say what stops it - 3.3 is blocked on exactly this "+
				"list and an unexplained entry makes the blockage unreadable.", c.Key)
		}
	}
	for key := range unscheduled {
		if onScheduler[key] {
			t.Errorf("%s is listed as unscheduled with reason %q, but it IS on the scheduler "+
				"now. Drop the entry - and if the list is empty, 3.3 is unblocked.",
				key, unscheduled[key])
		}
	}

	sort.Strings(missing)
	// ── THE DIRECTION THIS NOW FAILS IN ─────────────────────────────────────
	//
	// It used to fail when the list EMPTIED, because an empty list was the news:
	// 3.3 was blocked on the count and nothing else re-measured it. That happened
	// on 2026-09-08 and the check has been turned round rather than deleted --
	// deleting it would leave nothing at all watching the property, and a check
	// removed reads exactly like one that never existed.
	//
	// So the assertion is now the opposite one, and it is the durable half: a
	// dormancy-eligible collector that FALLS OFF the scheduler is a regression,
	// and per-query backoff (3.3) would silently stop covering it.
	if len(missing) > 0 {
		t.Errorf("%d of %d dormancy-eligible collectors are no longer scheduled: %v. "+
			"Per-query backoff covers only scheduled collectors, so this leaves the "+
			"dormancy supervisor as the only thing watching them - two mechanisms with "+
			"different semantics, which is what 3.3 exists to remove.",
			len(missing), len(eligible), missing)
	}
	t.Logf("%d dormancy-eligible collectors, all %d scheduled", len(eligible), len(covered))
}
