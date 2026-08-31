package collect

import (
	"encoding/json"
	"math"
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
				got := ParsePPPSessions(rows, prev, base.Add(time.Duration(step.AtMs)*time.Millisecond))

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
