package collect

import (
	"encoding/json"
	"math"
	"mikrodash/internal/hub"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// The PPP rate arithmetic, differentially.
//
// WHY THIS IS NOT COVERED BY THE GOLDEN GATE. This fleet runs no PPP, so the
// /ppp/active fixture is the empty-menu junk row and the golden is the empty
// state — which means the part of the collector that turns two byte readings
// into a rate is reached by no fixture at all. src/collectors/ppp.js says the
// same about itself: "NOT VERIFIED AGAINST HARDWARE".
//
// tools/ppp-cases.js runs the LIVE parsePppSessions over synthetic scenarios and
// records what it answers; this replays the same inputs through the port. The
// inputs are invented, the expected outputs are not, and neither implementation
// is asked about itself — the same shape as the audit-cases gate, applied where
// a fixture cannot reach.

type pppCaseFile struct {
	BaseMs int64 `json:"baseMs"`
	Cases  []struct {
		Name  string `json:"name"`
		Steps []struct {
			AtMs      int64               `json:"atMs"`
			Rows      []map[string]string `json:"rows"`
			Want      []pppWantSession    `json:"want"`
			PrevAfter []pppWantPrev       `json:"prevAfter"`
		} `json:"steps"`
	} `json:"cases"`
}

// pppWantSession mirrors the JSON the live parser emits. Pointers where the
// original yields null, because null and zero are the distinction the whole rate
// design turns on.
type pppWantSession struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Service   string   `json:"service"`
	Address   string   `json:"address"`
	CallerID  string   `json:"callerId"`
	Uptime    string   `json:"uptime"`
	Encoding  string   `json:"encoding"`
	SessionID string   `json:"sessionId"`
	LimitIn   *int     `json:"limitIn"`
	LimitOut  *int     `json:"limitOut"`
	RX        int      `json:"rx"`
	TX        int      `json:"tx"`
	RXRate    *float64 `json:"rxRate"`
	TXRate    *float64 `json:"txRate"`
}

type pppWantPrev struct {
	Key        string `json:"key"`
	RX         int    `json:"rx"`
	TX         int    `json:"tx"`
	TsOffsetMs int64  `json:"tsOffsetMs"`
}

func TestPPPSessionsMatchTheLiveParser(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(testdata, "ppp-cases.json"))
	if err != nil {
		t.Fatalf("cannot read the pinned cases (%v) — run: node tools/ppp-cases.js", err)
	}
	var f pppCaseFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("the case file is empty — this gate would pass on anything")
	}
	base := time.UnixMilli(f.BaseMs)

	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			// One `prev` per scenario, carried across its steps: a rate exists
			// only because a previous reading did, so the steps are a sequence
			// and not a set.
			prev := map[string]pppSample{}
			for i, step := range c.Steps {
				rows := make([]routeros.Reply, 0, len(step.Rows))
				for _, r := range step.Rows {
					rows = append(rows, routeros.Reply(r))
				}
				got, next := ParsePPPSessions(rows, prev, base.Add(time.Duration(step.AtMs)*time.Millisecond))
				prev = next

				if len(got) != len(step.Want) {
					t.Fatalf("step %d: %d session(s), want %d", i, len(got), len(step.Want))
				}
				for j, w := range step.Want {
					comparePPPSession(t, i, j, got[j], w)
				}

				// And what it REMEMBERS. A port that answered correctly while
				// leaking or pruning the wrong keys would pass on the sessions
				// alone, and the NEXT reading would be wrong instead.
				if len(prev) != len(step.PrevAfter) {
					t.Errorf("step %d: carried %d baseline(s), want %d (%v)",
						i, len(prev), len(step.PrevAfter), prev)
				}
				for _, wp := range step.PrevAfter {
					p, ok := prev[wp.Key]
					if !ok {
						t.Errorf("step %d: no baseline carried for %q", i, wp.Key)
						continue
					}
					wantTS := base.Add(time.Duration(wp.TsOffsetMs) * time.Millisecond)
					if p.rx != wp.RX || p.tx != wp.TX || !p.ts.Equal(wantTS) {
						t.Errorf("step %d: baseline %q = {rx:%d tx:%d ts:+%dms}, want {rx:%d tx:%d ts:+%dms}",
							i, wp.Key, p.rx, p.tx, p.ts.Sub(base).Milliseconds(),
							wp.RX, wp.TX, wp.TsOffsetMs)
					}
				}
			}
		})
	}
}

func comparePPPSession(t *testing.T, step, idx int, got PPPSession, want pppWantSession) {
	t.Helper()
	check := func(field string, g, w any) {
		if g != w {
			t.Errorf("step %d session %d %s = %v, want %v", step, idx, field, g, w)
		}
	}
	check("id", got.ID, want.ID)
	check("name", got.Name, want.Name)
	check("service", got.Service, want.Service)
	check("address", got.Address, want.Address)
	check("callerId", got.CallerID, want.CallerID)
	check("uptime", got.Uptime, want.Uptime)
	check("encoding", got.Encoding, want.Encoding)
	check("sessionId", got.SessionID, want.SessionID)
	check("rx", got.RX, want.RX)
	check("tx", got.TX, want.TX)
	comparePPPIntPtr(t, step, idx, "limitIn", got.LimitIn, want.LimitIn)
	comparePPPIntPtr(t, step, idx, "limitOut", got.LimitOut, want.LimitOut)
	comparePPPRate(t, step, idx, "rxRate", got.RXRate, want.RXRate)
	comparePPPRate(t, step, idx, "txRate", got.TXRate, want.TXRate)
}

func comparePPPIntPtr(t *testing.T, step, idx int, field string, got, want *int) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Errorf("step %d session %d %s: got %v, want %v", step, idx, field, ptrInt(got), ptrInt(want))
		return
	}
	if got != nil && *got != *want {
		t.Errorf("step %d session %d %s = %d, want %d", step, idx, field, *got, *want)
	}
}

// comparePPPRate keeps null and zero apart, which is the point of the whole
// design: null is "no measurement window yet" and 0 is "measured, and idle".
func comparePPPRate(t *testing.T, step, idx int, field string, got, want *float64) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Errorf("step %d session %d %s: got %v, want %v — null and zero are not the "+
			"same claim here", step, idx, field, ptrFloat(got), ptrFloat(want))
		return
	}
	// A rate is a division, so exact equality is the wrong test on principle even
	// where it happens to hold. The tolerance is far below anything renderable.
	if got != nil && math.Abs(*got-*want) > 1e-9 {
		t.Errorf("step %d session %d %s = %v, want %v", step, idx, field, *got, *want)
	}
}

func ptrInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func ptrFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// ── AN IDLE ROUTER MUST STILL PROVE IT IS ALIVE ────────────────────────────
//
// The emit is gated on a fingerprint, and a router with no PPP produces the same
// fingerprint for ever: no sessions, no secrets, no profiles, nothing to change.
// So after the first frame the collector went silent, and `web/src/stale.ts`
// measures staleness as "how long since a payload arrived" — it cannot tell a
// quiet collector from a stopped one. Twenty-five seconds later the Active
// Sessions card wore a "stale" badge while the poll loop was perfectly healthy.
// Reported off the live install.
//
// `pppHeartbeat` bounds the silence. This drives the real Tick with a reader
// that never changes its answer, and asserts BOTH halves: an unchanged tick
// inside the window still sends nothing (the dirty check is not thrown away),
// and a tick past the window sends anyway (the card cannot go stale).
func TestAnUnchangingRouterStillEmitsAHeartbeat(t *testing.T) {
	rd := fakeReader{rows: map[string][]routeros.Reply{
		"/ppp/active/print":                    {},
		"/ppp/secret/print":                    {},
		"/ppp/profile/print":                   {},
		"/interface/pppoe-server/server/print": {},
	}}
	emits := 0
	p := NewPPP(rd, hub.NewRelay(func(room string, _ hub.Named, payload any) { emits++ }), 5000)

	p.Tick()
	if emits != 1 {
		t.Fatalf("%d emits after the first tick, want 1 — nothing was sent at all", emits)
	}

	// INSIDE the window: the fingerprint has not moved, so the dirty check must
	// still suppress. Losing this half would make the heartbeat a per-tick emit.
	p.Tick()
	p.Tick()
	if emits != 1 {
		t.Errorf("%d emits, want 1 — an unchanged tick inside the heartbeat "+
			"window sent a frame, so the dirty check is doing nothing", emits)
	}

	// PAST the window. Reaching back to `lastEmit` rather than sleeping: the
	// real wait is fifteen seconds and a test that takes that long gets deleted.
	p.lastMu.Lock()
	p.lastEmit = p.lastEmit.Add(-pppHeartbeat - time.Second)
	p.lastMu.Unlock()

	p.Tick()
	if emits != 2 {
		t.Errorf("%d emits, want 2 — nothing changed and the heartbeat was due, "+
			"so the card is about to be called stale while the collector is fine", emits)
	}
}

// ── PHASE 4.1: THE JOIN AND THE TOTALS, WITHOUT A COLLECTOR ────────────────
//
// Two behaviours were only reachable by driving a `*PPP` through a tick, and
// both are the kind that look right on a healthy router and are wrong on the
// case that matters.

// TestBuildPPPJoinsConnectedFromTheLiveSessions.
//
// Sessions are read every tick and secrets once a minute. Setting `Connected`
// where the secrets are READ would freeze the pill for up to a minute — an
// account that dialled in four seconds ago reads as offline, which is exactly
// the question the column exists to answer.
func TestBuildPPPJoinsConnectedFromTheLiveSessions(t *testing.T) {
	in := PPPInput{
		Rows: []routeros.Reply{{".id": "*1", "name": "alice", "service": "pppoe"}},
		Secrets: []PPPSecret{
			{ID: "*a", Name: "alice"},
			{ID: "*b", Name: "bob"},
		},
		Now: time.Unix(100, 0),
	}
	got, _, _ := BuildPPP(in)

	byName := map[string]bool{}
	for _, s := range got.Secrets {
		byName[s.Name] = s.Connected
	}
	if !byName["alice"] {
		t.Error("alice has a live session and her secret reads as not connected")
	}
	if byName["bob"] {
		t.Error("bob has no session and his secret reads as connected")
	}

	// THE CALLER'S SLICE MUST NOT BE WRITTEN. `secrets` is a cached read reused
	// every tick; writing Connected into it leaves last tick's answer behind, so
	// an account that disconnects stays lit until the next config read.
	if in.Secrets[0].Connected {
		t.Error("the input slice was mutated — the cached secrets now carry a " +
			"Connected flag that will be stale on the next tick")
	}
}

// TestBuildPPPTotalsAreNullNotZeroWhenNothingIsKnown.
//
// A rate is a difference, so the FIRST reading of a session has nothing to
// subtract from and its rate is null. Summing those into a plain float would
// report 0 bps — "nothing is flowing" — when the truth is "we cannot say yet".
// The distinction is the whole reason the per-session rates are nullable, and it
// collapses on the first tick of every session, which is when somebody watching
// a new connection is most likely to be looking.
func TestBuildPPPTotalsAreNullNotZeroWhenNothingIsKnown(t *testing.T) {
	first, _, _ := BuildPPP(PPPInput{
		Rows: []routeros.Reply{{".id": "*1", "name": "alice", "bytes-in": "1000", "bytes-out": "500"}},
		Prev: nil, // nothing seen before
		Now:  time.Unix(100, 0),
	})
	if first.TotalRXRate != nil || first.TotalTXRate != nil {
		t.Errorf("totals are %v/%v on a first reading; a rate with nothing to "+
			"subtract from is UNKNOWN, and reporting zero says the link is idle",
			first.TotalRXRate, first.TotalTXRate)
	}
	if len(first.Sessions) != 1 {
		t.Fatalf("%d sessions, want 1", len(first.Sessions))
	}
}

// TestBuildPPPGroupsUnnamedServicesAsOTHER — a session with no service must not
// create an empty-string bucket the page would render as a blank row.
func TestBuildPPPGroupsUnnamedServicesAsOTHER(t *testing.T) {
	got, _, _ := BuildPPP(PPPInput{
		Rows: []routeros.Reply{
			{".id": "*1", "name": "a", "service": "pppoe"},
			{".id": "*2", "name": "b", "service": ""},
		},
		Now: time.Unix(100, 0),
	})
	if got.ByService["OTHER"] != 1 {
		t.Errorf("byService = %v; a session with no service belongs in OTHER", got.ByService)
	}
	if _, blank := got.ByService[""]; blank {
		t.Error("an empty-string service bucket was created; the page renders it as a blank row")
	}
}
