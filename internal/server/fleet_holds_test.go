package server

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/session"
)

// ── EVERY ENABLED ROUTER IS HELD WARM ───────────────────────────────────────
//
// The rule the Devices page's connectivity strip rests on, so it is pinned by
// behaviour, not by reading the source.
//
// Until 2026-10-01 `warm` was dropped for every router the overview pool had
// answered for, and the pool never fed `connTrack`: those routers lost their
// debounced Online verdict and wrote no connectivity rows. Measured on the dev
// install, three routers of four had none in seven days. The pool is gone and
// the hold is unconditional for an enabled router.
//
// `TestEverySyncPoolSiteAlsoSyncsTheAlertPool` lived here and was DELETED with
// the pool: it counted that every `syncPool()` was followed by
// `syncFleetHolds()`, because two pools dividing the fleet had to be synced
// together. There is one sync now, and no second thing for it to agree with.
func TestEveryEnabledRouterIsHeldWarm(t *testing.T) {
	s := schedServer(t, `[
	  {"id":"quiet","label":"Quiet","host":"198.51.100.1","port":8728,"username":"u","password":""},
	  {"id":"off","label":"Off","host":"198.51.100.2","port":8728,"username":"u","password":"",
	   "disabled":true}]`)
	s.sessions = session.NewManager(s.store, s.hub)
	t.Cleanup(func() { s.sessions.Shutdown() })
	s.holdFleet = true

	s.syncFleetHolds()

	// ALERTS OFF, REPORTING OFF, NOBODY WATCHING: the router the old rule
	// abandoned. Nothing else holds it, so warm is the only reason it is
	// connected at all.
	if !hasHold(s.sessions.Held("quiet"), "warm") {
		t.Errorf("an enabled router with alerts and reporting off is held %v, not warm. "+
			"Nothing then observes it: no debounced status, and no outage is ever "+
			"written to its connectivity strip", s.sessions.Held("quiet"))
	}
	// THE CONTROL: a disabled router is not connected to at all. Without this a
	// rule that held everything, disabled included, would pass above.
	if h := s.sessions.Held("off"); len(h) != 0 {
		t.Errorf("a DISABLED router is held %v; it must not be connected to", h)
	}
}

func hasHold(holds []string, want string) bool {
	for _, h := range holds {
		if h == want {
			return true
		}
	}
	return false
}

// AND THE HOLDS ARE SYNCED AT STARTUP, not when a page first opens. They exist
// so a router nobody is watching is still known to be up, still has its alerts
// evaluated and still has its outages recorded - a claim about the whole uptime
// of the process, not about a page. (Was `TestTheAlertPoolIsSyncedAtStartup`,
// from when these holds were a pool.)
func TestTheFleetHoldsAreSyncedAtStartup(t *testing.T) {
	b, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(b), "")
	if !strings.Contains(code, "srv.syncFleetHolds()") {
		t.Error("server.go never syncs the fleet holds: it would connect to nothing until a " +
			"router was edited, so non-active routers read Offline and their alerts never fire")
	}
}
