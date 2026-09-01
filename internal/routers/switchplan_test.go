package routers

// PlanRouterSwitch, against what the LIVE switchRouter actually did.
//
// The switchrouter corpus slices the function out of `src/index.js` and
// runs it with all twelve of its module-level dependencies stubbed. What those
// stubs recorded IS the expectation: the ORDERED operations.
//
// Order is not decoration here. The function saves a setting, tears a session
// down, relocates sockets and starts a new one, and the live comments say
// several of those orderings are load-bearing — most sharply that the old active
// id is read BEFORE the save, or the teardown targets the router just switched
// to. A test comparing final state would see none of it.

import (
	"encoding/json"
	"os"
	"testing"
)

type pooledRec struct {
	HasIdleTimer bool `json:"hasIdleTimer"`
}

type switchCase struct {
	Why   string `json:"why"`
	Input struct {
		NewRouterID    string               `json:"newRouterId"`
		ActiveRouterID string               `json:"activeRouterId"`
		KnownRouters   map[string]string    `json:"knownRouters"`
		Pooled         map[string]pooledRec `json:"pooled"`
		NewEntry       *struct {
			RosConnected bool `json:"rosConnected"`
			StartupReady bool `json:"startupReady"`
			Session      any  `json:"session"`
		} `json:"newEntry"`
		Switching bool `json:"switching"`
		Sockets   []struct {
			ID       string   `json:"id"`
			RouterID string   `json:"routerId"`
			Rooms    []string `json:"rooms"`
		} `json:"sockets"`
	} `json:"input"`
	Result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	} `json:"result"`
	Ops []struct {
		Op        string `json:"op"`
		To        string `json:"to"`
		Event     string `json:"event"`
		Socket    string `json:"socket"`
		Room      string `json:"room"`
		RouterID  string `json:"routerId"`
		Connected bool   `json:"connected"`
		Reason    string `json:"reason"`
		Patch     *struct {
			ActiveRouterID string `json:"activeRouterId"`
		} `json:"patch"`
		Payload *struct {
			RouterID  string `json:"routerId"`
			Label     string `json:"label"`
			Connected bool   `json:"connected"`
		} `json:"payload"`
	} `json:"ops"`
	SwitchingAfter bool `json:"switchingAfter"`
}

func loadSwitchCases(t *testing.T) []switchCase {
	t.Helper()
	b, err := os.ReadFile("../../testdata/switchrouter-cases.json")
	if err != nil {
		t.Fatalf("corpus missing: %v — run tools/switchrouter-cases.js", err)
	}
	var doc struct {
		Cases []switchCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("corpus is empty")
	}
	return doc.Cases
}

func (c switchCase) toInput() SwitchInput {
	in := SwitchInput{
		NewRouterID:    c.Input.NewRouterID,
		ActiveRouterID: c.Input.ActiveRouterID,
		Labels:         c.Input.KnownRouters,
		Pooled:         map[string]PooledSession{},
		Switching:      c.Input.Switching,
	}
	for id, p := range c.Input.Pooled {
		in.Pooled[id] = PooledSession{HasIdleTimer: p.HasIdleTimer}
	}
	for _, s := range c.Input.Sockets {
		in.Sockets = append(in.Sockets, SocketState{ID: s.ID, RouterID: s.RouterID, Rooms: s.Rooms})
	}
	if c.Input.NewEntry != nil {
		in.NewEntry = &NewSessionState{
			Connected:    c.Input.NewEntry.RosConnected,
			StartupReady: c.Input.NewEntry.StartupReady,
			HasSession:   c.Input.NewEntry.Session != nil,
		}
	}
	return in
}

// wantSteps turns the recorded operations into the steps this port produces.
//
// `log` is dropped: the live function writes one console line and this port does
// not model logging. Everything else is compared.
func (c switchCase) wantSteps() []SwitchStep {
	var out []SwitchStep
	for _, o := range c.Ops {
		switch o.Op {
		case "log":
			continue
		case "settings.save":
			id := ""
			if o.Patch != nil {
				id = o.Patch.ActiveRouterID
			}
			out = append(out, SwitchStep{Op: "settings.save", RouterID: id})
		case "emit":
			s := SwitchStep{Op: "emit", To: o.To, Event: o.Event}
			if o.Payload != nil {
				s.RouterID = o.Payload.RouterID
				s.Label = o.Payload.Label
				s.Connected = o.Payload.Connected
			}
			out = append(out, s)
		case "broadcastRosStatus":
			out = append(out, SwitchStep{Op: o.Op, Connected: o.Connected, Reason: o.Reason})
		case "leave", "join":
			out = append(out, SwitchStep{Op: o.Op, Socket: o.Socket, Room: o.Room})
		case "teardown", "dropEvaluator", "ensureRouterSession":
			out = append(out, SwitchStep{Op: o.Op, RouterID: o.RouterID})
		default:
			out = append(out, SwitchStep{Op: o.Op})
		}
	}
	return out
}

func TestPlanRouterSwitchMatchesLive(t *testing.T) {
	for _, c := range loadSwitchCases(t) {
		t.Run(c.Why, func(t *testing.T) {
			got := PlanRouterSwitch(c.toInput())

			if (got.Err == "") != c.Result.OK {
				t.Fatalf("ok = %v (err %q), live returned ok=%v err=%q",
					got.Err == "", got.Err, c.Result.OK, c.Result.Error)
			}
			if got.Err != c.Result.Error {
				t.Errorf("error = %q, live returned %q", got.Err, c.Result.Error)
			}

			want := c.wantSteps()
			if len(got.Steps) != len(want) {
				t.Fatalf("%d steps, live did %d\n  got:  %+v\n  want: %+v",
					len(got.Steps), len(want), got.Steps, want)
			}
			for i := range want {
				if got.Steps[i] != want[i] {
					t.Errorf("step %d:\n  got:  %+v\n  live: %+v", i, got.Steps[i], want[i])
				}
			}
			// THE FLAG, derived from the live STRUCTURE rather than from the
			// recorded end state — which does not determine it. `switchingAfter`
			// is false both for a path that took the flag and released it AND for
			// one that never took it at all (the unknown-router refusal, checked
			// before `_switching = true`). A first draft of this derivation
			// conflated them and the port was right.
			//
			// A path holds the flag exactly when it got past both refusals, or
			// when it IS the already-in-progress refusal — which never took the
			// flag and must not clear the one already held.
			wantHolds := c.Result.OK || c.Result.Error == "Switch already in progress"
			if got.HoldsFlag != wantHolds {
				t.Errorf("HoldsFlag = %v, want %v", got.HoldsFlag, wantHolds)
			}
		})
	}
}

// TestTheAlreadyInProgressRefusalDoesNotReleaseTheFlag.
//
// It returns BEFORE the live `try`, so the flag stays set — it belongs to the
// switch still running, and clearing it would let a second start on top of the
// first, which is the whole point of the guard.
//
// Stated on its own because I had it backwards while writing the corpus, and the
// live implementation is what corrected me. Every OTHER path goes through the
// `finally` and must clear it, or one failure refuses every switch afterwards
// and nothing about that first failure looks wrong.
func TestTheAlreadyInProgressRefusalDoesNotReleaseTheFlag(t *testing.T) {
	busy := PlanRouterSwitch(SwitchInput{
		NewRouterID: "r2", Labels: map[string]string{"r2": "Two"}, Switching: true,
	})
	if busy.Err != "Switch already in progress" {
		t.Fatalf("err = %q", busy.Err)
	}
	if !busy.HoldsFlag {
		t.Error("the refusal reports the flag as free; a second switch could then start " +
			"on top of the one still running")
	}
	if len(busy.Steps) != 0 {
		t.Errorf("the refusal planned %d steps", len(busy.Steps))
	}

	// The OTHER refusal never took the flag and has nothing to release.
	unknown := PlanRouterSwitch(SwitchInput{NewRouterID: "nope", Labels: map[string]string{}})
	if unknown.Err != "Router not found" {
		t.Fatalf("err = %q", unknown.Err)
	}
	if unknown.HoldsFlag {
		t.Error("an unknown router reports the flag as held; it is checked before the try")
	}
	if len(unknown.Steps) != 0 {
		t.Errorf("the refusal planned %d steps", len(unknown.Steps))
	}
}

// TestOnlyFollowersMove — the socket predicate, in both directions.
//
// The live comment on the version this replaced: it "skipped sockets with an
// auth session, orphaning all modern-auth clients", which is every client on a
// modern install. The failure was invisible to anyone testing with auth off.
func TestOnlyFollowersMove(t *testing.T) {
	plan := PlanRouterSwitch(SwitchInput{
		NewRouterID: "r2", ActiveRouterID: "r1",
		Labels: map[string]string{"r2": "Two"},
		Pooled: map[string]PooledSession{"r1": {}},
		Sockets: []SocketState{
			{ID: "follower", RouterID: "r1", Rooms: []string{"router-r1", "router-r1:wifi", "lobby"}},
			{ID: "pinned", RouterID: "r3", Rooms: []string{"router-r3"}},
			{ID: "already", RouterID: "r2", Rooms: []string{"router-r2"}},
		},
	})
	moved := map[string][]string{}
	for _, s := range plan.Steps {
		if s.Op == "leave" || s.Op == "join" {
			moved[s.Socket] = append(moved[s.Socket], s.Op+" "+s.Room)
		}
	}
	if len(moved["pinned"]) != 0 {
		t.Errorf("a socket pinned to another router was moved: %v — that would wipe another "+
			"operator's charts", moved["pinned"])
	}
	if len(moved["already"]) != 0 {
		t.Errorf("a socket already on the new router was moved again: %v", moved["already"])
	}
	want := []string{"leave router-r1", "leave router-r1:wifi", "join router-r2"}
	if len(moved["follower"]) != len(want) {
		t.Fatalf("follower moves = %v, want %v", moved["follower"], want)
	}
	for i := range want {
		if moved["follower"][i] != want[i] {
			t.Errorf("follower move %d = %q, want %q", i, moved["follower"][i], want[i])
		}
	}
	// `lobby` stays: the prefix rule catches the router's sub-rooms and nothing
	// else. A port matching on equality would strand `router-r1:wifi`; one
	// matching on "contains" would drop unrelated rooms.
	for _, m := range moved["follower"] {
		if m == "leave lobby" {
			t.Error("an unrelated room was left")
		}
	}
}

// TestTheSyntheticStatusIsTrueOnly (#118).
//
// Switching to an ALREADY-CONNECTED router fires no `connected` event, and that
// event is the only producer of `ros:status{connected:true}` — which is the only
// thing that dismisses the client's switching modal. Without this the operator
// sat behind a modal until they reloaded.
//
// And it must never be synthesised FALSE: that would be the client's second
// false and would dismiss the overlay while the new router is still connecting.
func TestTheSyntheticStatusIsTrueOnly(t *testing.T) {
	base := func(e *NewSessionState) SwitchPlan {
		return PlanRouterSwitch(SwitchInput{
			NewRouterID: "r2", ActiveRouterID: "r1",
			Labels:   map[string]string{"r2": "Two"},
			Pooled:   map[string]PooledSession{"r1": {}},
			NewEntry: e,
		})
	}
	statuses := func(p SwitchPlan) int {
		n := 0
		for _, s := range p.Steps {
			if s.Op == "emit" && s.Event == "ros:status" {
				n++
			}
		}
		return n
	}
	replays := func(p SwitchPlan) int {
		n := 0
		for _, s := range p.Steps {
			if s.Op == "replay" {
				n++
			}
		}
		return n
	}

	hot := base(&NewSessionState{Connected: true, StartupReady: true, HasSession: true})
	if statuses(hot) != 1 {
		t.Errorf("an already-connected session sent %d synthetic statuses, want 1", statuses(hot))
	}
	if replays(hot) != 1 {
		t.Error("the replay did not run for a ready session")
	}

	// STARTUPREADY GATES THE REPLAY ALONE. Collectors mid-start replay their own
	// tail shortly; doing it here as well costs every socket a duplicate fetch.
	midStart := base(&NewSessionState{Connected: true, StartupReady: false, HasSession: true})
	if statuses(midStart) != 1 {
		t.Error("the status was withheld from a connected session that was mid-startup")
	}
	if replays(midStart) != 0 {
		t.Error("the replay ran mid-startup; every socket pays a duplicate fetchInterfaces")
	}

	for _, e := range []*NewSessionState{
		nil,
		{Connected: false, StartupReady: true, HasSession: true},
	} {
		if p := base(e); statuses(p) != 0 {
			t.Errorf("%+v: a synthetic status was sent for a session that is not connected", e)
		}
	}

	// NEVER FALSE, on any path.
	for _, p := range []SwitchPlan{hot, midStart, base(nil)} {
		for _, s := range p.Steps {
			if s.Op == "emit" && s.Event == "ros:status" && !s.Connected {
				t.Error("a synthetic ros:status FALSE was planned; it would be the client's " +
					"second false and would dismiss the switching overlay early")
			}
		}
	}
}

// TestTheFirstRunPathSkipsEverythingAboutAnOldRouter.
//
// With no active router — the overlay's own case — there is nothing to tell, no
// session to tear down and nobody to relocate. A port that emitted
// `router:switching` to `router-` (the empty id appended to the prefix) would
// broadcast to a room any socket could join.
func TestTheFirstRunPathSkipsEverythingAboutAnOldRouter(t *testing.T) {
	plan := PlanRouterSwitch(SwitchInput{
		NewRouterID: "r2", ActiveRouterID: "",
		Labels:   map[string]string{"r2": "Two"},
		Pooled:   map[string]PooledSession{},
		NewEntry: &NewSessionState{},
		Sockets:  []SocketState{{ID: "s1", RouterID: "", Rooms: []string{"lobby"}}},
	})
	want := []string{"settings.save", "ensureRouterSession"}
	if len(plan.Steps) != len(want) {
		t.Fatalf("plan is %+v, want exactly %v", plan.Steps, want)
	}
	for i, op := range want {
		if plan.Steps[i].Op != op {
			t.Errorf("step %d is %q, want %q", i, plan.Steps[i].Op, op)
		}
	}
	for _, s := range plan.Steps {
		if s.To == "router-" {
			t.Error("an event was addressed to `router-`, the empty id appended to the prefix")
		}
	}
}
