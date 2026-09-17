package verify

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryCollectorDeclaresItsDerivation is the ledger for phase 4.1 of
// Collectors-Rewrite.md: turning each collector's "rows in, payload out" half
// into a function with no receiver and no I/O.
//
// ── WHY A LEDGER, WHEN THE WORK IS JUST REFACTORING ─────────────────────────
//
// Because the scope was mis-measured twice in one afternoon, in both directions.
// A grep for `Build*` said seventeen collectors had no derivation; several of
// them had one called `Parse*` instead. An estimate is not a measurement, and
// this phase is long enough that a wrong one changes what gets built.
//
// It also does the thing every other ledger here does: it shrinks. A phase whose
// remaining work is visible in a test cannot quietly stall, and a collector added
// later cannot join without answering the question.
//
// ── WHAT COUNTS AS EXTRACTED ────────────────────────────────────────────────
//
// A package-level function, named here, that the collector's file declares. This
// gate checks the NAME EXISTS AND IS PACKAGE-LEVEL -- it cannot prove a function
// is pure, and says so rather than implying otherwise. What it does prove is that
// the derivation has a name a test can call without building a collector, which
// is the property 4.1 is actually for.
//
// PRIOR STATE AS A PARAMETER is the established shape, not a new invention:
// `BuildBandwidth(prev, in)`, `BuildQueueRows(rows, prev, now)` and
// `BuildIfStatus(prev, in)` all take it as an argument. About a third of these
// collectors carry something between ticks, and a derivation holding that on a
// receiver is not extracted, whatever it is called.
func TestEveryCollectorDeclaresItsDerivation(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "collect")

	// "" means not yet extracted, and the note says what stands in the way.
	// Anything non-empty must name package-level functions in that file.
	ledger := map[string]string{
		// ── extracted ──
		"areas.go":       "BuildAreaRows",
		"bandwidth.go":   "BuildBandwidth",
		"bridges.go":     "BuildBridgeRows",
		"capsman.go":     "BuildCapsmanView, BuildCapsmanLegacyView",
		"connections.go": "BuildConns",
		"dns.go":         "ParseDNSSettings,ParseStaticEntries",
		"ifstatus.go":    "BuildIfStatus",
		"ipaddresses.go": "BuildIPAddresses",
		"queues.go":      "BuildQueueRows",
		"rosusers.go":    "BuildUsersView",
		"ppp.go":         "ParsePPPSessions",
		"system.go":      "buildSystem",
		"talkers.go":     "BuildTalkers",
		"topology.go":    "BuildTopology",
		"vlans.go":       "BuildVlanRows",
		"vpn.go":         "ParsePppSessions,ParseIpsecPeers",
		"wan.go":         "BuildWanRows",
		// Both live in wifiview.go, which is the point of that file: one view
		// builder serving the modern and legacy stacks. A derivation need not sit
		// in its collector's file, so this gate looks package-wide.
		"wifi.go":     "BuildWifiView, BuildCapsLegacyNetworks",
		"wireless.go": "BuildWirelessView",

		// ── set B: not in scope for 4.1 ──
		//
		// A stream has no "rows in, payload out" half to extract. Its derivation
		// is per-pushed-row and belongs to Track B's model, not this one. Listed
		// rather than omitted, so the count is honest.
		"logs.go":    "",
		"ping.go":    "",
		"traffic.go": "",

		// `arp` has a derivation and no payload: `BuildARP` turns the table into
		// the two lookups its four consumers read. That it emits nothing does not
		// exempt it — the rows-in, value-out half is exactly what 4.1 asks to be
		// callable without building the collector.
		"arp.go": "BuildARP",
		// The two that no `Start()` gate could see until 2026-09-10.
		"packages.go":     "BuildPackages",
		"routing.go":      "BuildRouting",
		"dhcpleases.go":   "BuildLeases,buildLeaseServers",
		"dhcpnetworks.go": "BuildLanOverview",
		"firewall.go":     "BuildFirewallRule",
		"netwatch.go":     "BuildNetwatch",
	}

	// A collector is a file declaring Start() OR Resume(). Derived rather than
	// listed, so a new one joins by existing.
	//
	// ── `Resume` WAS ADDED 2026-09-10, AND TWO COLLECTORS HAD BEEN INVISIBLE ──
	//
	// `packages` and `routing` declare no `Start()` at all: both are page-gated,
	// so the session brings them up with `Resume()` and nothing else. They were
	// therefore not collectors as far as this gate could see, and their
	// derivations went unchecked for the life of the ledger — the ledger's own
	// completeness rule ("a new one joins by existing") quietly excluded them.
	//
	// Found while writing Collector-Architecture.md, by generating the
	// per-collector table from the source and noticing two blanks.
	//
	// AND A FILE EMBEDDING THE TABLE CORE, whose lifecycle is promoted rather
	// than declared (internal/collect/table.go). Without this the thirteen table
	// collectors would vanish from this ledger exactly as packages and routing did.
	start := regexp.MustCompile(`(?m)^func \([a-z]+ \*[A-Z]\w*\) (?:Start|Resume)\(\)|^\ttableCore\[\w+\]$`)
	// EXPORTED OR NOT. A derivation being package-level is what makes it callable
	// from a test without a collector; being exported is a separate question
	// about who outside this package needs it. Requiring a capital was a third
	// mis-measurement in the same afternoon -- it hid `buildSystem`, which is
	// exactly the shape this phase is asking for.
	fn := regexp.MustCompile(`(?m)^func ([a-zA-Z]\w*)\(`)

	files := collectGoFiles(t, dir)

	// PACKAGE-WIDE, not per file. A derivation may legitimately live outside its
	// collector's file -- wifiview.go holds one builder serving both the modern
	// and legacy wifi collectors -- and requiring co-location would push a shared
	// derivation back into one of its two callers.
	declared := map[string]bool{}
	for _, name := range files {
		for _, m := range fn.FindAllStringSubmatch(mustRead(t, filepath.Join(dir, name)), -1) {
			declared[m[1]] = true
		}
	}

	collectors := map[string]bool{}
	extracted, pending := 0, 0

	for _, name := range files {
		src := mustRead(t, filepath.Join(dir, name))
		if !start.MatchString(src) {
			continue
		}
		collectors[name] = true

		want, listed := ledger[name]
		if !listed {
			t.Errorf("%s declares a collector and is not in the derivation ledger. Either "+
				"name the function that turns its rows into its payload, or record why it "+
				"has none.", name)
			continue
		}
		if want == "" {
			pending++
			continue
		}
		for _, f := range strings.Split(want, ",") {
			if !declared[strings.TrimSpace(f)] {
				t.Errorf("%s's ledger entry names %q, which internal/collect does not declare "+
					"at package level anywhere. The derivation was renamed, deleted, or folded "+
					"back onto a receiver.", name, f)
			}
		}
		extracted++
	}

	for name := range ledger {
		if !collectors[name] {
			t.Errorf("the ledger carries %q, which no longer declares a collector. A recorded "+
				"gap that has closed is a failure here.", name)
		}
	}

	if extracted+pending != len(collectors) {
		t.Fatalf("counted %d extracted and %d pending against %d collectors; the scan and the "+
			"ledger disagree", extracted, pending, len(collectors))
	}
	if extracted < 20 {
		t.Fatalf("only %d derivations resolved; the function scan has stopped matching and "+
			"this ledger checks nothing", extracted)
	}
	t.Logf("%d collectors: %d with an extracted derivation, %d without", len(collectors), extracted, pending)
}
