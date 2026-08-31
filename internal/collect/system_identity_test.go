package collect

// WHEN the system collector reports a router's identity.
//
// The corpus is `tools/identity-hook-cases.js`, which lifts the live
// `identityKey` dedupe out of `src/collectors/system.js` by content anchor and
// replays SEQUENCES of ticks through it. Sequences, because the rule is
// stateful: a corpus of independent inputs would pass an implementation that
// fired on every tick, which is exactly the failure that would rewrite
// routers.json and wake every browser several times a minute.
//
// ── WHAT THIS CAUGHT ────────────────────────────────────────────────────────
//
// The port had the hook on the POOL, called once per connection off
// `system.Last()` — nil at that moment, so it never fired at all; and with no
// second call an OS upgrade could never be reported, which the live comment
// forbids in as many words ("must not be write-once").

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mikrodash/internal/routeros"
)

// identityStub answers `/system/resource/print` with whatever the current tick
// wants and NOTHING ELSE.
//
// ── THE STATIC READ IS DISABLED, AND THAT COST A DEBUG CYCLE ────────────────
//
// The serial comes from the corpus rather than from a routerboard read: what is
// under test is the dedupe, and driving the real static-read scheduling would
// make the first two ticks a property of this stub rather than of the rule.
//
// But writing `sys.serial` before each tick is not enough on its own.
// `readStatic` runs on the SECOND tick (`doStatic := s.firstTick && !s.staticRead`)
// and, against a stub that answers nothing, sets the serial back to nil — so
// every tick's key differed from the last and the collector fired on all of
// them, which looks exactly like a missing dedupe. `staticRead` is therefore set
// at construction, so the only thing moving the serial is this test.
type identityStub struct {
	version string
	board   string
	serial  *string
}

func (s *identityStub) Connected() bool { return true }

func (s *identityStub) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	if cmd.Path != systemResourceCmd.Path {
		return nil, nil
	}
	return []routeros.Reply{{
		"version": s.version, "board-name": s.board,
		"cpu-load": "1", "total-memory": "100", "free-memory": "50",
	}}, nil
}

type identityTick struct {
	Version       string  `json:"version"`
	BoardName     string  `json:"boardName"`
	Serial        *string `json:"serial"`
	InstalledBase string  `json:"installedBase"`
	Fired         bool    `json:"fired"`
	Reported      *struct {
		Model     string  `json:"model"`
		Serial    *string `json:"serial"`
		OSVersion string  `json:"osVersion"`
	} `json:"reported"`
}

func TestSystemIdentityReportingMatchesTheLiveDedupe(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(testdata, "identity-hook-cases.json"))
	if err != nil {
		t.Fatalf("no corpus — run: node tools/identity-hook-cases.js: %v", err)
	}
	var corpus struct {
		Cases []struct {
			Why   string         `json:"why"`
			Ticks []identityTick `json:"ticks"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(body, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Cases) == 0 {
		t.Fatal("the corpus is empty")
	}

	// The corpus must separate firing from not firing ACROSS A RUN, or the
	// dedupe's memory is untested and a fire-every-tick port passes.
	quietAfterFirst := false
	for _, c := range corpus.Cases {
		for _, tk := range c.Ticks[1:] {
			if !tk.Fired {
				quietAfterFirst = true
			}
		}
	}
	if !quietAfterFirst {
		t.Fatal("no run is quiet after its first tick; the dedupe is untested")
	}

	for _, c := range corpus.Cases {
		t.Run(c.Why, func(t *testing.T) {
			stub := &identityStub{}
			sys := NewSystem(stub, func(string, string, any) {}, 1000)
			sys.staticRead = true // see the stub's header

			var got []Identity
			var fired []bool
			sys.SetOnIdentity(func(id Identity) { got = append(got, id) })

			for i, tk := range c.Ticks {
				stub.version = tk.Version
				stub.board = tk.BoardName
				// THE STUB'S SERIAL IS PUSHED STRAIGHT ONTO THE COLLECTOR. The
				// real one arrives from a routerboard read the corpus does not
				// model; forcing it here keeps the two runs comparing the same
				// sequence of triples.
				sys.mu.Lock()
				sys.serial = tk.Serial
				sys.mu.Unlock()

				before := len(got)
				sys.Tick()
				fired = append(fired, len(got) > before)

				if fired[i] != tk.Fired {
					t.Errorf("tick %d (version=%q serial=%v): fired=%v, live fired=%v",
						i, tk.Version, derefOr(tk.Serial, "<nil>"), fired[i], tk.Fired)
					continue
				}
				if !tk.Fired {
					continue
				}
				id := got[len(got)-1]
				if id.Model != tk.Reported.Model {
					t.Errorf("tick %d: model = %q, live = %q", i, id.Model, tk.Reported.Model)
				}
				if id.OSVersion != tk.Reported.OSVersion {
					t.Errorf("tick %d: osVersion = %q, live = %q — the channel must be dropped "+
						"from the STORED value, not merely hidden in the UI",
						i, id.OSVersion, tk.Reported.OSVersion)
				}
				// A null serial in the live payload joins as EMPTY, and reaches
				// the writer as an empty string it must SKIP rather than clear.
				if want := derefOr(tk.Reported.Serial, ""); id.Serial != want {
					t.Errorf("tick %d: serial = %q, live = %q", i, id.Serial, want)
				}
			}
		})
	}
}

// TestIdentityIsReportedEvenWhenTheGaugesAreStill.
//
// The live call sits ABOVE its own `if (changed)`, and the placement is
// load-bearing: the fingerprint deliberately excludes the serial and the memory
// totals, so a router whose gauges have not moved emits nothing. An identity
// gated on `changed` would never be reported on a quiet device — which is most
// of them, most of the time.
//
// Pinned separately because the corpus above cannot see it: it replays ticks
// whose gauges happen to differ.
func TestIdentityIsReportedEvenWhenTheGaugesAreStill(t *testing.T) {
	ser := "HDX0ABCDEF1"
	stub := &identityStub{version: "7.24 (stable)", board: "RB5009UG"}
	emits := 0
	sys := NewSystem(stub, func(string, string, any) { emits++ }, 1000)
	sys.staticRead = true // see identityStub's header
	var got []Identity
	sys.SetOnIdentity(func(id Identity) { got = append(got, id) })

	// Two IDENTICAL ticks: the second changes no gauge, so it emits nothing.
	sys.Tick()
	emitsAfterFirst := emits
	sys.Tick()
	if emits != emitsAfterFirst {
		t.Fatalf("the second identical tick emitted; this test needs a still one")
	}
	// Now the SERIAL arrives — which the fingerprint does not include, so the
	// gauges are still still.
	sys.mu.Lock()
	sys.serial = &ser
	sys.mu.Unlock()
	sys.Tick()
	if emits != emitsAfterFirst {
		t.Fatalf("the serial reached the fingerprint; it must not, or every static read "+
			"would cost a frame (emits went %d -> %d)", emitsAfterFirst, emits)
	}

	if len(got) != 2 {
		t.Fatalf("identity reported %d times, want 2 (once bare, once with the serial). "+
			"Gating it on the emit fingerprint would report a quiet router's identity never.",
			len(got))
	}
	if got[0].Serial != "" || got[1].Serial != ser {
		t.Errorf("the two reports are %+v and %+v; want the first bare and the second with the "+
			"serial", got[0], got[1])
	}
}

func derefOr[T any](p *T, zero T) T {
	if p == nil {
		return zero
	}
	return *p
}
