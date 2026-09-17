package server

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/collect"
	"mikrodash/internal/session"
)

var reResume = regexp.MustCompile(`ResumeCollector\("(\w+)"\)`)

// TestEveryGatedCollectorDeclaresARoom is the safety check the switchover needs.
//
// ── THE FAILURE THIS GUARDS IS SILENT AND TOTAL ────────────────────────────
//
// Demand's rule is "a collector runs if anybody is in any room it declares". A
// collector no room can want is therefore never wanted by a viewer — it can only
// run for alerting or a hold, and on a router with neither it is suspended from
// connect to teardown with nothing anywhere saying so.
//
// Under the switchboard that could not happen: a page's case called
// `ResumeCollector` by name whether or not the collector's rooms agreed. So this
// hazard is CREATED by demand, which is why it is a gate rather than a comment.
//
// ── THERE IS NO EXEMPTION LIST, AND THERE WAS ──────────────────────────────
//
// `roomlessCollectors` was a map recording which collectors were allowed to have
// no rooms and why. It held one entry, `dhcpLeases`, and THAT ENTRY WAS WRONG —
// it emits router-wide, so the DHCP and Connections pages render it directly and
// suspending it blanks both. The map was then kept EMPTY, with a comment saying
// the next collector with no audience would need somewhere to say so.
//
// Phase 6.1 deleted it. A collector with no demand rooms has `keepAliveFor` to
// name the rooms that should keep it alive — which is what `arp` and `dhcpLeases`
// both use — and an empty exemption list is a mechanism with no instances kept
// against a hypothetical caller. If one ever genuinely needs an exemption, the
// map is four lines and git history has this paragraph.
func TestEveryGatedCollectorDeclaresARoom(t *testing.T) {
	var roomless []string
	for _, key := range session.TargetKeys() {
		if len(collect.DemandRooms(key)) == 0 {
			roomless = append(roomless, key)
		}
	}
	sort.Strings(roomless)
	for _, key := range roomless {
		t.Errorf("%s is gated by demand and no room can want it, so it runs only for "+
			"alerting or a hold: on a router with neither it is suspended from connect "+
			"to teardown with nothing saying so. Declare its rooms, or give it a "+
			"keepAliveFor entry naming the rooms of whatever reads it.", key)
	}
}

// TestDemandCoversEveryCollectorTheSwitchboardDid.
//
// ── A MIGRATION CHECK THAT OUTLIVED ITS SUBJECT, DELIBERATELY ──────────────
//
// It was written before the switchover and read `ws.go` directly: scan the
// `ResumeCollector("…")` literals, and assert every collector the switchboard
// could start is one the demand rule can also start. Converting a page must not
// silently drop a collector.
//
// The switchboard is gone, so the scan now finds nothing and the check would
// pass by reading an empty set — the failure mode this repository has lost
// coverage to before. The list is therefore FROZEN here, as it stood at
// 8ffc8a4, and the assertion is unchanged: every collector the switchboard could
// start must still be gatable by demand today.
//
// That keeps meaning something after the deletion. A collector that loses its
// entry from `session.TargetKeys()` — or is renamed without the table following
// — was reachable by the shipped app and is no longer reachable by anything, and
// this is what says so.
var switchboardResumed = []string{
	"bandwidth", "bridges", "capsman", "conns", "dhcpLeases", "dhcpNetworks",
	"dns", "firewall", "packages", "ppp", "queues", "rosusers", "routing",
	"topology", "vlans", "vpn", "wan", "wifi", "wireless",
}

func TestDemandCoversEveryCollectorTheSwitchboardDid(t *testing.T) {
	// The frozen list is only as good as its provenance, and a typo in it would
	// weaken the check silently. Nineteen is what the switchboard resumed, and it
	// is the number the pre-switchover run of this test measured.
	if len(switchboardResumed) != 19 {
		t.Fatalf("the frozen switchboard list holds %d keys; it held 19 when it was "+
			"taken, so it has been edited without its note being updated",
			len(switchboardResumed))
	}

	gated := map[string]bool{}
	for _, k := range session.TargetKeys() {
		gated[k] = true
	}
	for _, key := range switchboardResumed {
		if !gated[key] {
			t.Errorf("the shipped switchboard resumed %q and the target table cannot "+
				"gate it, so nothing can start that collector any more", key)
		}
	}

	resumed := map[string]bool{}
	for _, k := range switchboardResumed {
		resumed[k] = true
	}
	var newlyGated []string
	for _, k := range session.TargetKeys() {
		if !resumed[k] {
			newlyGated = append(newlyGated, k)
		}
	}
	sort.Strings(newlyGated)
	// The collectors the switchboard NEVER resumed are the interesting
	// direction: they were never gated by it at all, and ran from connect until
	// dormancy or a hold pruned them. Under demand they are gated like everything
	// else, which is the behaviour CHANGE of this phase.
	//
	// `ifStatus` is the one that needed work rather than a note: four collectors
	// borrow its rates on pages it emits nothing to, so it is gated on
	// `keepAliveFor` as well as its own audience. `netwatch` and `talkers` feed
	// only the dashboard, and now stop when nobody is on it.
	//
	// `logs` and `ping` joined them on 2026-09-10 — phase 3.4, the last two
	// ungated collectors in the app. Neither was in the session's target table
	// at all, so nothing could reach them: `logs` held `/log/listen` open for the
	// life of every session, one channel per router, for a page most viewers
	// never open.
	//
	// `arp` joined on 2026-09-10 and is the clearest case for demand there is:
	// it emits to no room at all, so it can only ever be wanted through
	// `keepAliveFor` — the rooms of the four collectors that read its index.
	//
	// `ipAddresses` joined on 2026-09-15 with the IP Addresses page (#97). It is
	// new rather than moved: the switchboard never had it, and it is gated like
	// everything else from the start, reading only while somebody has the page
	// open.
	// `areas` joined on 2026-09-17 with the first generated page (internal/areas).
	// Like `ipAddresses` it is new rather than moved, and it is the case demand
	// was built for: one collector serving many pages, reading only the areas
	// whose rooms are occupied.
	want := "areas,arp,ifStatus,ipAddresses,logs,netwatch,ping,talkers"
	if got := strings.Join(newlyGated, ","); got != want {
		t.Errorf("collectors newly gated by demand = %q, want %q.\nIf that list has "+
			"changed, a collector has gained or lost coverage and the behaviour change "+
			"of this phase is different from the one recorded.", got, want)
	}
}

// TestNoSwitchboardHasComeBack: the source half, which the frozen list above
// cannot provide.
//
// A `ResumeCollector("…")` literal in a page handler is the switchboard
// returning one case at a time. That is not a hypothetical: the shape is
// obvious, it compiles, and it works — right up until the collector gains a
// second room and the handler that starts it is not the one that would stop it.
func TestNoSwitchboardHasComeBack(t *testing.T) {
	b, err := os.ReadFile("ws.go")
	if err != nil {
		t.Fatal(err)
	}
	// Comments first: ws.go describes at length what was removed.
	var body strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	for _, m := range reResume.FindAllStringSubmatch(body.String(), -1) {
		t.Errorf("ws.go resumes %q by name. A handler joins its room; applyDemand "+
			"decides what runs. Naming a collector here puts the mapping back in two "+
			"places, which is what phase 4.2b removed.", m[1])
	}
}
