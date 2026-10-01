package server

// The demand rule, driven — the half a source audit cannot check.
//
// ── WHAT THIS FILE WAS, AND WHY IT IS THE SAME TEST ────────────────────────
//
// It tested `suspendIfNoRoomOccupied`: the blur-suspend audit verified that
// every multi-room collector was suspended THROUGH that helper, and could not
// verify that the helper worked. A version that stopped consulting the hub would
// have passed the audit and frozen every dashboard card the moment its page was
// left. That mutation survived, which is why a driven test was written.
//
// Phase 4.2b deleted the helper and the switchboard it served. The mutation it
// was written for did not go anywhere: `wantsCollector` is now the one place the
// hub is consulted, and a version that stopped consulting it would freeze every
// collector on every router instead of one card. So the table below is the old
// one — no room, the page room, the dashboard-card room, an unrelated room —
// asked of the rule that replaced it.
//
// The grace is injected rather than waited out: `Server.idleGrace` exists for
// this, because the shipped value is two minutes.

import (
	"sort"
	"testing"
	"time"

	"mikrodash/internal/collect"
	"mikrodash/internal/hub"
	"mikrodash/internal/session"
)

func TestWantsCollectorReadsTheRooms(t *testing.T) {
	for _, c := range []struct {
		why      string
		key      string
		occupied string // a room to put a client in, "" for none
		want     bool
	}{
		{"no room is occupied", "vpn", "", false},
		{"the page room has a viewer", "vpn", "router-r1-page-vpn", true},
		{"the DASHBOARD CARD room has a viewer", "vpn", "router-r1-dash-card-vpn", true},
		{"an unrelated room has a viewer", "vpn", "router-r1-page-dns", false},
		// The same room on ANOTHER router is not this router's viewer. The room
		// name is prefixed per router, so getting the prefix wrong would make one
		// viewer keep the whole fleet's collectors running.
		{"the page room on a different router", "vpn", "router-r2-page-vpn", false},
		// ── THE KEEP-ALIVE HALF ─────────────────────────────────────────────
		//
		// `ifStatus` sends nothing to the Bridges page and must run for it
		// anyway: four collectors borrow its rates. Under the switchboard this
		// was a comment saying the collector is never suspended at all.
		{"a room ifStatus feeds", "ifStatus", "router-r1-page-interfaces", true},
		{"a room ifStatus is only a rate source for", "ifStatus", "router-r1-page-bridges", true},
		{"a room ifStatus has nothing to do with", "ifStatus", "router-r1-page-dns", false},
	} {
		c := c
		t.Run(c.why, func(t *testing.T) {
			h := hub.New()
			s := &Server{hub: h}
			if c.occupied != "" {
				cl := hub.NewClient("viewer", 4)
				h.Add(cl)
				h.Join(cl, c.occupied)
			}
			// A non-nil session is required; `wantsCollector` uses it only for
			// the holds and alert questions, both of which a zero value answers.
			if got := s.wantsCollector(session.NewForTest(h, "r1"), "r1", c.key); got != c.want {
				t.Errorf("wantsCollector(%q) = %v, want %v", c.key, got, c.want)
			}
		})
	}
}

// TestEveryDeclaredRoomAloneIsEnough is the sweep the table above cannot be.
//
// ── THE MUTATION IT KILLS ──────────────────────────────────────────────────
//
// `roomsOccupied` returns true if ANY room has a viewer. A version that read
// only the first, or that dropped the keep-alive half of `DemandRooms`, passes
// every hand-written case that happens to name room zero — and the cost is a
// card or a borrowed rate column that silently stops updating, which is the
// exact defect this project has now fixed six times.
//
// So each room is tested ALONE, for every collector demand can gate.
func TestEveryDeclaredRoomAloneIsEnough(t *testing.T) {
	checked := 0
	for _, key := range session.TargetKeys() {
		rooms := collect.DemandRooms(key)
		for _, room := range rooms {
			h := hub.New()
			s := &Server{hub: h}
			cl := hub.NewClient("viewer", 4)
			h.Add(cl)
			h.Join(cl, "router-r1-"+room)
			if !s.wantsCollector(session.NewForTest(h, "r1"), "r1", key) {
				t.Errorf("a lone viewer in %q does not make %q wanted, though %q "+
					"declares it", room, key, key)
			}
			checked++
		}
	}
	// The failure branch never fires on a clean run, so a broken sweep would
	// look identical to a healthy one. There are 22 gated collectors and every
	// one but `dhcpLeases` declares at least one room.
	if checked < 25 {
		t.Fatalf("only %d collector/room pairs were driven; the declarations have "+
			"stopped being readable and this test checks nothing", checked)
	}
	t.Logf("%d collector/room pairs, each wanted by a lone viewer", checked)
}

// TestNothingIsWantedWithNobodyWatching is the other direction, and without it
// the sweep above is satisfied by a rule that returns true unconditionally.
func TestNothingIsWantedWithNobodyWatching(t *testing.T) {
	h := hub.New()
	s := &Server{hub: h}
	var wanted []string
	for _, key := range session.TargetKeys() {
		if s.wantsCollector(session.NewForTest(h, "r1"), "r1", key) {
			wanted = append(wanted, key)
		}
	}
	sort.Strings(wanted)
	if len(wanted) > 0 {
		t.Errorf("with an empty hub and no holds, %v are still wanted", wanted)
	}
}

// TestWantsCollectorRefusesIncompleteInput: a nil session or an empty router id
// must not panic, and must want nothing.
func TestWantsCollectorRefusesIncompleteInput(t *testing.T) {
	s := &Server{hub: hub.New()}
	if s.wantsCollector(nil, "r1", "vpn") {
		t.Error("a nil session wants a collector")
	}
	if s.wantsCollector(session.NewForTest(s.hub, ""), "", "vpn") {
		t.Error("an empty router id wants a collector")
	}
	// And applyDemand must survive both without reaching a collector.
	s.applyDemand(nil, "r1")
	s.applyDemand(session.NewForTest(s.hub, ""), "")
}

// TestApplyDemandSuspendsWhatNothingWants closes the gap between the rule and
// its application.
//
// ── THE MUTATION IT KILLS ──────────────────────────────────────────────────
//
// `wantsCollector` can be perfect and `applyDemand` can still ignore half of its
// answer. Deleting the suspend branch — or the whole `TargetKeys()` loop —
// leaves every test above green: the rule still returns the right verdict, and
// nothing ever acts on the negative one. The app would then start collectors on
// demand and never stop any, which is the state it was in before this phase for
// three of them.
//
// So this drives `applyDemand` itself, with one page open, and asserts BOTH
// directions at once: the collectors that page feeds are left alone, and every
// other one is handed to the suspend.
func TestApplyDemandSuspendsWhatNothingWants(t *testing.T) {
	h := hub.New()
	suspended := make(chan string, 64)
	s := &Server{
		hub:        h,
		idleGrace:  time.Millisecond,
		suspendOne: func(_ *session.Session, key string) { suspended <- key },
	}
	cl := hub.NewClient("viewer", 4)
	h.Add(cl)
	h.Join(cl, "router-r1-page-vpn")

	s.applyDemand(session.NewForTest(h, "r1"), "r1")

	got := map[string]bool{}
	deadline := time.After(2 * time.Second)
	want := len(session.TargetKeys()) - 1 // everything but vpn
	for len(got) < want {
		select {
		case k := <-suspended:
			got[k] = true
		case <-deadline:
			var missing []string
			for _, k := range session.TargetKeys() {
				if k != "vpn" && !got[k] {
					missing = append(missing, k)
				}
			}
			sort.Strings(missing)
			t.Fatalf("applyDemand suspended %d of %d collectors; %v were never handed "+
				"to the suspend, so nothing stops them once a page starts them",
				len(got), want, missing)
		}
	}
	if got["vpn"] {
		t.Error("vpn was suspended with a viewer on its page")
	}
}

// TestSuspendIsDeferredAndReAsked pins the grace period, which is the one piece
// of `suspendIfNoRoomOccupied` that survived phase 4.2b unchanged.
//
// A page refresh empties every room this viewer was in and refills them a second
// later. Suspending on the empty moment stops the collector's stream and starts
// it again immediately — churn on the one resource this project conserves.
//
// The re-ask is what makes it safe without tracking timers, and it is the half
// worth driving: a version that captured the answer at arm time instead would
// suspend a collector somebody came back to.
func TestSuspendIsDeferredAndReAsked(t *testing.T) {
	for _, c := range []struct {
		why       string
		comesBack bool
		want      bool // did the suspend run?
	}{
		{"nobody comes back", false, true},
		{"a viewer arrives during the grace", true, false},
	} {
		c := c
		t.Run(c.why, func(t *testing.T) {
			h := hub.New()
			suspended := make(chan string, 4)
			s := &Server{
				hub:       h,
				idleGrace: 80 * time.Millisecond,
				suspendOne: func(_ *session.Session, key string) {
					suspended <- key
				},
			}
			rs := session.NewForTest(h, "r1")
			s.suspendAfterGrace(rs, "r1", "vpn")
			if c.comesBack {
				cl := hub.NewClient("viewer", 4)
				h.Add(cl)
				h.Join(cl, "router-r1-dash-card-vpn")
			}
			var got bool
			select {
			case key := <-suspended:
				got = true
				if key != "vpn" {
					t.Errorf("suspended %q, want vpn", key)
				}
			// Generously past the 80ms grace, so a `false` here means the
			// suspend was PREVENTED rather than merely slow.
			case <-time.After(2 * time.Second):
			}
			if got != c.want {
				t.Errorf("suspend ran = %v, want %v", got, c.want)
			}
		})
	}
}

// TestAClosedTabIsIndistinguishableFromABlur.
//
// ── THE PATH THAT SENDS NO FRAME ───────────────────────────────────────────
//
// Every other way of leaving a page tells the server: `page:blur`,
// `dashcard:blur`, `router:select`. Closing the tab tells it nothing, and under
// the switchboard that meant a viewer who shut their laptop left every collector
// they had started polling until the session's own idle grace expired — a grace
// that exists to keep the CONNECTION warm, not to gate collectors.
//
// `releaseRouter` is the one place that path goes through. What makes it correct
// is the ORDER: the rooms are given up first, so this connection is no longer
// counted as its own audience, and only then is demand re-asked.
//
// Both halves are driven here. A lone viewer leaving must hand every collector
// to the suspend; a SECOND viewer still on a page must keep that page's
// collector and lose nothing else — which is the assertion that fails if the
// rooms are left after the question instead of before it, or not at all.
func TestAClosedTabIsIndistinguishableFromABlur(t *testing.T) {
	for _, c := range []struct {
		why       string
		otherPage string // a room a second viewer stays in, "" for nobody
		keeps     string // the collector that must NOT be suspended
	}{
		{"the last viewer closes the tab", "", ""},
		{"a second viewer is still on the Firewall page", "page-firewall", "firewall"},
	} {
		c := c
		t.Run(c.why, func(t *testing.T) {
			s := devicesServerWithFleet(t)
			s.sessions = session.NewManager(s.store, s.hub)
			t.Cleanup(s.sessions.Shutdown)
			s.idleGrace = time.Millisecond
			suspended := make(chan string, 64)
			s.suspendOne = func(_ *session.Session, key string) { suspended <- key }

			leaving := devicesConn(s, "leaving")
			leaving.routerID = "r1"
			leaving.rsession = session.NewForTest(s.hub, "r1")
			// The tab was on the DNS page and had the VPN card in its grid.
			s.hub.Join(leaving.c, "router-r1-page-dns")
			s.hub.Join(leaving.c, "router-r1-dash-card-vpn")

			if c.otherPage != "" {
				staying := devicesConn(s, "staying")
				s.hub.Join(staying.c, "router-r1-"+c.otherPage)
			}

			leaving.releaseRouter()

			got := map[string]bool{}
			want := len(session.TargetKeys())
			if c.keeps != "" {
				want--
			}
			deadline := time.After(3 * time.Second)
			for len(got) < want {
				select {
				case k := <-suspended:
					got[k] = true
				case <-deadline:
					var missing []string
					for _, k := range session.TargetKeys() {
						if k != c.keeps && !got[k] {
							missing = append(missing, k)
						}
					}
					sort.Strings(missing)
					t.Fatalf("closing the tab suspended %d of %d collectors; %v kept "+
						"polling a router nobody is watching", len(got), want, missing)
				}
			}
			if c.keeps != "" && got[c.keeps] {
				t.Errorf("%s was suspended while a second viewer is still on its page", c.keeps)
			}
			// AND THE ROOMS REALLY WENT. Without this the test would pass on a
			// version that asked the right question at the wrong moment and got
			// the right answer by luck.
			if n := len(leaving.c.Rooms()); n != 0 {
				t.Errorf("the leaving connection is still in %d room(s): %v",
					n, leaving.c.Rooms())
			}
		})
	}
}
