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
// ── THE GUARD MOVED INTO THE FUNNEL (2026-08-28), AND THE CALLER MOVED
//
//	OUT OF THIS FILE (2026-09-10) ─────────────────────────────────────────
//
// It began as `if CollectorEnabled("k") { X().Resume() }` at twenty page cases,
// each repeating the check. Those became `cn.rsession.ResumeCollector("k")` —
// the live `_resumeCollector`, ONE place that checks enabled and consults the
// dormancy veto, so "a gate that knows nothing about dormancy cannot undo it".
//
// Phase 4.2b deleted the twenty call sites. `applyDemand` in demand.go decides
// what runs, from the room declarations, and it calls the same funnel. So the
// LIST half of this test is gone — a page no longer names its collectors, and
// `demand_test.go` holds the frozen record of what the switchboard used to
// resume plus the gate that every collector is still reachable.
//
// WHAT SURVIVES IS THE REGRESSION CHECK, and it is the half that was worth
// having: no bare `X().Resume()` anywhere in this package. That is a resume
// which skips both the enabled check and the veto, and the second is the one the
// live app warns about — it "would wake a dormant collector on the next socket
// join". A direct call is EASIER to reach for now than it was under the
// switchboard, because there is no longer an obvious per-page place to add one
// properly.
func TestNoCollectorIsResumedOutsideTheFunnel(t *testing.T) {
	dir := "."
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	bare := regexp.MustCompile(`(?:cn\.rsession|rs)\.(\w+)\(\)\.Resume\(\)`)
	scanned := 0
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for _, m := range bare.FindAllStringSubmatch(string(src), -1) {
			t.Errorf("%s: %s().Resume() is called directly. Every resume goes through "+
				"ResumeCollector, which checks CollectorEnabled and consults the dormancy "+
				"veto; a bare call undoes both.", n, m[1])
		}
	}
	// Finding nothing is the passing state, so the scan has to prove it read
	// something.
	if scanned < 20 {
		t.Fatalf("only %d source files were scanned; the package is larger than that "+
			"and this check is measuring nothing", scanned)
	}
	// AND THE FUNNEL MUST STILL BE REACHED. A package that stopped resuming
	// anything at all would pass every assertion above.
	dem, err := os.ReadFile("demand.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dem), "rs.ResumeCollector(key)") {
		t.Error("demand.go does not call ResumeCollector; nothing in this package " +
			"starts a collector any more")
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
		//
		// THE KEY IS A VARIABLE NOW (2026-09-10). Phase 4.2b deleted the twenty
		// literal call sites; `applyDemand` passes a key from a loop instead. The
		// pattern matched `("literal")` only, so it counted 21 and now counts 0
		// — and this file would otherwise be measuring the funnel by looking at a
		// shape that no longer exists.
		checked += len(regexp.MustCompile(`\.ResumeCollector\(`).FindAllString(src, -1))

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
	// ── THE FLOOR, AND WHY IT MOVED ────────────────────────────────────────
	//
	// It was 30, against a real count of ~40 while the switchboard held 21
	// literal `ResumeCollector` calls. Phase 4.2b deleted those, and the count
	// measured immediately afterwards is 20: the direct `Start`/`Tick`/`Refresh`
	// entry points, plus the one funnel call in demand.go.
	//
	// LOWERING A FLOOR IS HOW COVERAGE IS LOST QUIETLY, so the number is stated
	// with what it was measured against rather than nudged until green. What it
	// still catches is the thing it was written for: a pattern that has stopped
	// matching reads as a package with no entry points, and every ungated call
	// then passes by not being seen.
	//
	// 18 -> 17 on 2026-09-18, measured: the IP Addresses page's own
	// `IPAddresses().RefreshNow()` went with its collector, and the page is
	// refreshed by the one gated `Areas().RefreshNow(page)` every area shares.
	// A page becoming an area removes an entry point; it never adds one.
	if checked < 17 {
		t.Errorf("only %d collector entry points examined; 17 were counted on "+
			"2026-09-18, so the pattern above has stopped matching", checked)
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
