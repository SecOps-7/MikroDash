package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// #105: EVERY PAGE-FOCUS RESUME IS GATED.
//
// The connect path was gated first and this one was missed, which is the shape
// of the bug worth pinning: a collector the operator turned off stayed off until
// somebody opened its page, and then came back. Half a feature is worse than
// none here, because the router silently starts answering again.
//
// Read from the source for the same reason the session's equivalent is: standing
// a connection up needs a router. What it catches is a Resume that loses its
// guard, or gains one naming the wrong collector.
func TestEveryPageFocusResumeIsGated(t *testing.T) {
	body, err := os.ReadFile("ws.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	// ── THE GUARD MOVED INTO THE FUNNEL (2026-08-28) ────────────────────────
	//
	// This used to look for `if CollectorEnabled("k") { X().Resume() }` at every
	// page case — twenty of them, each repeating the check. They now call
	// `cn.rsession.ResumeCollector("k")`, which is the live `_resumeCollector`:
	// ONE place that checks enabled and consults the dormancy veto, so "a gate
	// that knows nothing about dormancy cannot undo it".
	//
	// This test went red the moment the shape changed, which is it working. What
	// it protects is unchanged and is now stronger, because there is one
	// implementation of the check instead of twenty:
	//
	//   1. NO BARE `X().Resume()` may remain in this file. That is the
	//      regression — a resume that skips both the enabled check and the veto.
	//   2. Every page that used to resume still does, by key.
	//
	// The pairing this used to assert — a guard naming the WRONG collector — is
	// no longer expressible: the key IS the argument, and
	// `internal/session.TestEveryKeyWsPassesIsInTheTable` fails if it names
	// something the session cannot reach.
	want := []string{
		"dns", "bridges", "vlans", "wan", "packages", "routing", "ppp", "vpn",
		"rosusers", "capsman", "topology", "conns", "bandwidth", "wireless",
		"wifi", "firewall", "queues", "dhcpNetworks", "dhcpLeases",
	}

	found := map[string]bool{}
	for _, m := range regexp.MustCompile(`cn\.rsession\.ResumeCollector\("(\w+)"\)`).
		FindAllStringSubmatch(src, -1) {
		found[m[1]] = true
	}
	for _, key := range want {
		if !found[key] {
			t.Errorf("no ResumeCollector(%q) — did the page case move or disappear? A page that "+
				"stopped resuming its collector renders whatever was last collected, forever.", key)
		}
	}

	// AN UNGATED Resume IS THE REGRESSION, and the only one that matters now.
	// A direct `X().Resume()` skips the enabled check AND the dormancy veto, and
	// the second is the one the live app warns about: it "would wake a dormant
	// collector on the next socket join".
	for _, m := range regexp.MustCompile(`cn\.rsession\.(\w+)\(\)\.Resume\(\)`).
		FindAllStringSubmatch(src, -1) {
		t.Errorf("%s().Resume() is called directly. Every resume goes through "+
			"ResumeCollector, which checks CollectorEnabled and consults the dormancy veto; "+
			"a bare call undoes both.", m[1])
	}
}

// EVERY collector entry point in this package is gated — not just Resume.
//
// This is the third bypass found by asking "who else starts a collector". The
// connect-time starts were gated, then the page-focus resumes were found
// ungated, then `Reconnected` — which is not the latch-clearing no-op its name
// suggests, since every implementation ends `Tick(); loop.start()`.
//
// So the rule is checked GENERICALLY rather than per-method: any call through
// `cn.rsession.X()` to something that can begin work must sit under a
// `CollectorEnabled` guard. A new entry point added later is caught by the same
// test, which a hand-listed set of method names would not be.
func TestEveryCollectorEntryPointIsGated(t *testing.T) {
	// Methods that BEGIN work. `Last`, `SetX` and the like read or configure and
	// are deliberately not here.
	begins := regexp.MustCompile(`^(Start|Resume|Reconnected|RefreshNow|Tick)$`)
	// A GATED call: the guard opens and the call is the very next statement.
	gated := regexp.MustCompile(
		`if cn\.rsession\.CollectorEnabled\("(\w+)"\) \{\s*\n\s*cn\.rsession\.(\w+)\(\)\.(\w+)\(\)`)
	// ANY call to an entry point, gated or not.
	any := regexp.MustCompile(`cn\.rsession\.(\w+)\(\)\.(\w+)\(\)`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(body)

		// Offsets of the calls that ARE gated, so an ungated one cannot borrow a
		// neighbour's guard. An earlier version searched the 120 bytes before
		// each call for the word `CollectorEnabled`, and a call sitting just
		// after somebody else's guarded block passed — the mutation that
		// ungated it survived, which is how this was found.
		ok := map[int]bool{}
		for _, loc := range gated.FindAllStringSubmatchIndex(src, -1) {
			// Groups: 1=key(2,3) 2=accessor(4,5) 3=method(6,7). The ACCESSOR's
			// offset is the join with `any` below, whose first group is the same
			// accessor at the same position. Recording the METHOD's offset here
			// and looking it up by the accessor's — which the first version did —
			// makes the two sets disjoint and reports gated calls as ungated.
			ok[loc[4]] = true
		}
		// ── THE FUNNEL COUNTS, AND IS GATED BY CONSTRUCTION (2026-08-28) ──
		//
		// `ResumeCollector(key)` checks `CollectorEnabled` itself — once, for all
		// twenty page cases that used to repeat it — and then consults the
		// dormancy veto. These are entry points and they ARE gated; not counting
		// them dropped the total from ~35 to 15 and tripped the floor below,
		// which is this test noticing the shape changed rather than the coverage
		// falling.
		checked += len(regexp.MustCompile(`\.ResumeCollector\("\w+"\)`).FindAllString(src, -1))

		for _, loc := range any.FindAllStringSubmatchIndex(src, -1) {
			method := src[loc[4]:loc[5]]
			if !begins.MatchString(method) {
				continue
			}
			checked++
			if !ok[loc[2]] {
				t.Errorf("%s: %s().%s() is not gated on CollectorEnabled — a collector the "+
					"operator turned off would be started by it",
					f, src[loc[2]:loc[3]], method)
			}
		}
	}
	if checked < 30 {
		t.Errorf("only %d collector entry points examined; this package has far more, so the "+
			"pattern above has stopped matching", checked)
	}
}

// A ROUTER WRITE ENDPOINT MUST STRIP THE PRIVILEGED FIELDS.
//
// `rbac.StripPrivilegedRouterFields` is ported and pinned, and NOTHING CALLS IT
// — this port has no router write route yet. That is the dangerous state: the
// rule exists, looks done, and the handler that needs it does not exist to
// forget it.
//
// So this test asserts the CURRENT state in both directions. While there is no
// route it passes and says so. The moment somebody registers one it fails, and
// the only way to make it pass is to call the strip — which is the decision
// being forced, not a chore being imposed.
//
// The escalation it guards: `PUT /api/routers/:id` is gated on `router:manage`
// for the target, which Devices-page write access confers and which is not
// global-only. Without the strip a non-administrator can add their own device to
// any site — additively and invisibly, with every site id enumerable from an
// ungated endpoint.
func TestARouterWriteRouteMustStripPrivilegedFields(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// A LITERAL PATTERN ONLY. `mux.HandleFunc("PUT "+routersPrefix+"/{id}", …)`
	// does not match, and a route registered that way is invisible to this
	// check — which is exactly the shape the first attempt at
	// `registerRouters` used, and which would have left this test passing while
	// the escalation was live. `routers_api.go` spells the pattern out for that
	// reason and says so. If a future route hides here, this comment is the
	// thing that was not read.
	route := regexp.MustCompile(`mux\.HandleFunc\("(PUT|POST|PATCH|DELETE) [^"]*routers[^"]*"`)

	var found []string
	callsStrip := false
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(body)
		for _, m := range route.FindAllString(src, -1) {
			found = append(found, f+": "+m)
		}
		if strings.Contains(src, "StripPrivilegedRouterFields") {
			callsStrip = true
		}
	}

	if len(found) == 0 {
		if callsStrip {
			t.Error("something calls StripPrivilegedRouterFields but no router write route is " +
				"registered — this test's premise has changed and it needs rereading")
		}
		return // the recorded state: no route yet, nothing to wire.
	}
	if !callsStrip {
		t.Errorf("a router write route exists (%v) and nothing in this package calls "+
			"rbac.StripPrivilegedRouterFields — a non-administrator can set site membership, "+
			"which widens who can reach the device", found)
	}
}

// TestAttachingARouterSubscribesToTheDefaultInterface.
//
// ── THE BUG THIS EXISTS FOR, AND WHY NOTHING ELSE COULD SEE IT ──────────────
//
// `traffic:update` is delivered to a PER-INTERFACE room. The live app's picker
// emits `traffic:select` only when the chosen interface goes AWAY — on an
// ordinary page load it just sets the dropdown — so a viewer's subscription has
// to come from somewhere else. In `traffic.js` it comes from `bindSocket`,
// which sets `{ ifName: this.defaultIf }` on connect. This port had no
// equivalent, so no browser ever joined a traffic room and no sample ever
// arrived.
//
// MEASURED AGAINST THE REAL AX3 on 2026-08-27, which is the only thing that
// found it: 20 seconds on the Bandwidth page delivered wan:status x19,
// ifstatus:names x15, system:update x9, bandwidth:update x6, and
// traffic:update x0 — the WAN figures reading "—" beside a live app showing
// 185 Kbps. After the fix, 20 in the same 20 seconds.
//
// NOT ONE OF THE 115 DIFFERENTIAL GATES COULD HAVE CAUGHT IT. Every one of them
// supplies a payload and compares what is rendered; this was a payload that
// never arrives, which is a question about SUBSCRIPTION rather than rendering.
// The page's own renderer was correct throughout.
//
// This is a SOURCE test for the reason the two above it are: standing a
// connection up needs a router. It pins that the call is present and that it is
// the non-validating variant — and it cannot prove the room is ever delivered
// to, which is what `TestTheTrafficRoomHasOneDefinition` below is for.
func TestAttachingARouterSubscribesToTheDefaultInterface(t *testing.T) {
	body, err := os.ReadFile("ws.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	attach := src[strings.Index(src, "cn.sendOpenAlerts(id)"):]
	if end := strings.Index(attach, "\nfunc "); end > 0 {
		attach = attach[:end]
	}
	if !strings.Contains(attach, "cn.trafficSelectDefault(") {
		t.Error("attaching a router does not subscribe to the default interface. Without it no " +
			"viewer joins a traffic room, no traffic:update is delivered, and the Bandwidth " +
			"and Dashboard charts stay empty against a real router while every gate passes")
	}
	// ...AND NOT THROUGH THE VALIDATING PATH. The first version of the fix
	// called `trafficSelect`, which feeds `SetAvailable` from
	// `IfStatus().Last()` — nil on a fresh attach, so `NormalizeIfName` refused
	// and the function returned early. It measured identically to no fix at all.
	if strings.Contains(attach, "cn.trafficSelect(") &&
		!strings.Contains(attach, "cn.trafficSelectDefault(") {
		t.Error("the attach path calls trafficSelect, whose NormalizeIfName has nothing to " +
			"validate against on a fresh attach: IfStatus().Last() is nil, so it returns " +
			"early and subscribes to nothing")
	}
	if !strings.Contains(src, "func (cn *conn) trafficSelectDefault(") {
		t.Fatal("trafficSelectDefault is gone; the assertions above are checking a call to " +
			"something that no longer exists")
	}
}

// TestTheTrafficRoomHasOneDefinition.
//
// The emitter and the joiner used to build the room name independently. They
// agreed, and nothing would have noticed if they stopped — a viewer would sit
// in a room nobody sends to and see an empty chart, which is exactly the
// symptom above arriving by a different route. Both now go through
// `collect.TrafficSub` and `session.RoomFor`, and this fails if either side
// starts spelling it out again.
func TestTheTrafficRoomHasOneDefinition(t *testing.T) {
	for _, f := range []string{"ws.go", "../collect/traffic.go"} {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			// The DEFINITION is the one place allowed to spell it out. Matched
			// on the function rather than skipped by line number, which would
			// go stale the moment anything above it moved.
			if strings.Contains(line, "func TrafficSub(") {
				continue
			}
			// COMMENTS ARE NOT CODE, and this gate failed on its own
			// explanation before the skip existed: the note describing why the
			// name must not be spelled out has to spell it out. A source
			// scanner that reads prose reports the documentation as the defect.
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if strings.Contains(line, `"-traffic-"`) || strings.Contains(line, `"traffic-" +`) ||
				strings.Contains(line, `"traffic-"+`) {
				t.Errorf("%s builds a traffic room name by hand:\n  %s\nUse collect.TrafficSub "+
					"and session.RoomFor: a joiner and an emitter that disagree produce an "+
					"empty chart and no error anywhere", f, strings.TrimSpace(line))
			}
		}
	}
}
