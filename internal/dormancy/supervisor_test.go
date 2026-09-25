package dormancy

// The supervisor's tick, against the LIVE block from `src/index.js`.
//
// The dormancy-tick corpus lifts the whole `// ── Collector dormancy ──`
// region contiguously and runs it with its five free names supplied — the real
// registry, the real state machine, the real `payloadEmpty`, and spies for `io`
// and the collectors. Every decision under test runs as the original; nothing in
// the harness reimplements any of it.

import (
	"encoding/json"
	"os"
	"sync"
	"testing"

	"mikrodash/internal/collection"
)

type tickStep struct {
	Op       string                    `json:"op"`
	Now      int64                     `json:"now"`
	Key      string                    `json:"key"`
	Watching *bool                     `json:"watching"`
	Payloads map[string]map[string]any `json:"payloads"`
	OpsAfter []struct {
		Key string `json:"key"`
		Op  string `json:"op"`
	} `json:"opsAfter"`
	EmitsAfter []struct {
		Ev      string `json:"ev"`
		Payload struct {
			RouterID string   `json:"routerId"`
			Dormant  []string `json:"dormant"`
		} `json:"payload"`
	} `json:"emitsAfter"`
}

type tickCase struct {
	Why   string     `json:"why"`
	Steps []tickStep `json:"steps"`
}

func loadTickCases(t *testing.T) []tickCase {
	t.Helper()
	b, err := os.ReadFile("../../testdata/dormancy-tick-cases.json")
	if err != nil {
		t.Fatalf("corpus missing: %v — run tools/dormancy-tick-cases.js", err)
	}
	var doc struct {
		Cases []tickCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("corpus is empty")
	}
	return doc.Cases
}

// worldFor rebuilds the case's collectors from its own text.
//
// The generator's specs are not recorded field by field — what IS recorded is
// every payload each collector had at each tick, plus the case's `why`. The two
// pieces of setup that cannot be read off the steps are which collectors have a
// probe() and which are disabled, and both are named in the `why`.
func worldFor(why string, keys []string) (enabled map[string]bool, hasProbe map[string]bool) {
	enabled = map[string]bool{}
	hasProbe = map[string]bool{}
	for _, k := range keys {
		enabled[k] = why != "a collector the operator disabled is skipped entirely"
		hasProbe[k] = why == "a due probe calls probe() where one exists" ||
			why == "reset wakes everything that was asleep and emits once" ||
			why == "reset probes only what was asleep, not every collector it knows" ||
			why == "focus on a dormant collector resets it, probes, and emits" ||
			why == "focus on a collector that is NOT dormant does nothing at all"
	}
	return
}

// emptyKeyFor comes from the REGISTRY, exactly as the supervisor's caller will
// get it — not from a table here.
func emptyKeyFor(t *testing.T, key string) []string {
	t.Helper()
	for _, c := range collection.DormancyEligible() {
		if c.Key == key {
			return c.EmptyKey
		}
	}
	t.Fatalf("%s is not eligible for dormancy, so the corpus is exercising a collector this "+
		"port would never judge", key)
	return nil
}

func TestTheTickMatchesLive(t *testing.T) {
	for _, c := range loadTickCases(t) {
		c := c
		t.Run(c.Why, func(t *testing.T) {
			// The collectors this case touches, in first-seen order.
			var keys []string
			seen := map[string]bool{}
			payloads := map[string]map[string]any{}
			for _, s := range c.Steps {
				for k := range s.Payloads {
					if !seen[k] {
						seen[k] = true
						keys = append(keys, k)
					}
				}
				if s.Key != "" && !seen[s.Key] {
					seen[s.Key] = true
					keys = append(keys, s.Key)
				}
			}
			if len(keys) == 0 {
				keys = []string{"netwatch"} // the payload-less case
			}
			enabled, hasProbe := worldFor(c.Why, keys)
			startupReady := c.Why != "startupReady false suppresses the whole tick"
			watching := c.Why != "nothing is judged while nobody is watching the router"

			sup := NewSupervisor(Defaults())
			for i, s := range c.Steps {
				for k, p := range s.Payloads {
					payloads[k] = p
				}
				if s.Watching != nil {
					watching = *s.Watching
				}

				var plan Plan
				switch s.Op {
				case "reset":
					plan = sup.Reset()
				case "focus":
					plan = sup.WakeForFocus(s.Key)
				default:
					// THE PAYLOAD IS READ HERE, the way the live tick reads it —
					// `collection.PayloadEmpty` over the map the live app
					// produced, `p.ts`, and `p.available === false`. The session
					// reads typed structs instead and
					// `TestTheLookupAgreesWithTheMapLookup` is what ties the two.
					var cs []Collector
					for _, k := range keys {
						p := payloads[k]
						c := Collector{Key: k, Enabled: enabled[k], Present: p != nil}
						if p != nil {
							if ts, ok := p["ts"].(float64); ok {
								c.TS = int64(ts)
							}
							c.Empty = collection.PayloadEmpty(p, emptyKeyFor(t, k))
							c.Unsupported = p["available"] == false
						}
						cs = append(cs, c)
					}
					plan = sup.Tick(TickInput{Now: s.Now, Watching: watching,
						StartupReady: startupReady, Collectors: cs})
				}

				// ── THE OPERATIONS, IN ORDER ────────────────────────────────
				//
				// The live spy records suspend/resume/probe/refreshNow; this side
				// plans suspend/wake/probe. `resume`+`refreshNow` and `probe` are
				// the two shapes of ONE decision — `_probeCollector`'s preference —
				// so they fold to a single planned "probe" and the CALLER picks.
				// A live "resume" that is NOT part of a probe is a wake.
				want := foldLiveOps(s.OpsAfter, hasProbe)
				if len(plan.Ops) != len(want) {
					t.Fatalf("step %d (%s): planned %v, live performed %v", i, s.Op, plan.Ops, want)
				}
				for j := range want {
					if plan.Ops[j] != want[j] {
						t.Errorf("step %d op %d: planned %+v, live %+v", i, j, plan.Ops[j], want[j])
					}
				}

				// ── THE EMIT ────────────────────────────────────────────────
				if plan.Emit != (len(s.EmitsAfter) > 0) {
					t.Errorf("step %d: Emit=%v, live emitted %d times", i, plan.Emit, len(s.EmitsAfter))
				}
				if len(s.EmitsAfter) > 0 {
					wantDormant := s.EmitsAfter[0].Payload.Dormant
					if len(plan.Dormant) != len(wantDormant) {
						t.Errorf("step %d: dormant %v, live sent %v", i, plan.Dormant, wantDormant)
					} else {
						for j := range wantDormant {
							if plan.Dormant[j] != wantDormant[j] {
								t.Errorf("step %d: dormant %v, live sent %v", i, plan.Dormant, wantDormant)
								break
							}
						}
					}
				}
			}
		})
	}
}

// foldLiveOps turns what the live spy saw into what this side plans.
//
// `_probeCollector` is ONE decision with two shapes: `probe()` where a collector
// has one, otherwise `resume()` AND `refreshNow()`. This package plans "probe"
// and leaves the choice to the caller, so the pair folds to one op — and a
// `resume` with no `refreshNow` beside it is a WAKE, which is a different thing.
func foldLiveOps(live []struct {
	Key string `json:"key"`
	Op  string `json:"op"`
}, hasProbe map[string]bool) []Op {
	out := []Op{}
	for i := 0; i < len(live); i++ {
		switch live[i].Op {
		case "suspend":
			out = append(out, Op{Key: live[i].Key, Do: OpSuspend})
		case "probe":
			out = append(out, Op{Key: live[i].Key, Do: OpProbe})
		case "refreshNow":
			// Already folded into the resume before it.
		case "resume":
			if i+1 < len(live) && live[i+1].Op == "refreshNow" && live[i+1].Key == live[i].Key {
				out = append(out, Op{Key: live[i].Key, Do: OpProbe})
			} else {
				out = append(out, Op{Key: live[i].Key, Do: OpWake})
			}
		}
	}
	return out
}

// ── THE SAME CLASS THAT CRASHED THE SERVER ON 2026-08-29 ──────────────────
//
// `Supervisor` keeps `states map[string]*State` and, until this test, no lock —
// correctly for the thing it was ported from, where a single event loop reaches
// it. In this port it is reached from at least three goroutines:
//
//	the dormancy ticker    `Tick`     (session/dormancy_run.go)
//	the connect loop       `Reset`    on reconnect (session.go)
//	websocket handlers     `IsDormant`, `WakeForFocus`, `Dormant` on page focus
//
// and `state(key)` WRITES the map on first access, so all four are writers.
//
// The evaluator in `internal/alertwire` had the identical shape and took the
// whole process down with `fatal error: concurrent map writes`. This is that
// bug's sibling, found by asking which OTHER ported objects hold unguarded state
// — the sweep after the crash rather than the crash itself.
//
// Without the lock this dies as a runtime fatal, which no recover catches, so
// the test binary dying is the signal.
func TestTheSupervisorIsSafeUnderConcurrentUse(t *testing.T) {
	s := NewSupervisor(Defaults())
	keys := []string{"dns", "vlans", "wifi", "queues", "bridges", "vpn"}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				k := keys[(n+j)%len(keys)]
				switch (n + j) % 4 {
				case 0:
					// EVERY GUARD SATISFIED. The first version of this test set only
					// `Watching`, so `Tick` returned at `!in.StartupReady` and never
					// reached `state()` — it exercised nothing and passed under `-race`,
					// which would have been recorded as "the supervisor is safe".
					// `Enabled` matters for the same reason: a disabled collector is
					// skipped before the map is touched.
					s.Tick(TickInput{
						StartupReady: true, Watching: true,
						Collectors: []Collector{{Key: k, Enabled: true}},
					})
				case 1:
					s.IsDormant(k)
				case 2:
					s.WakeForFocus(k)
				default:
					s.Dormant()
				}
			}
		}(i)
	}
	wg.Wait()

	// Reaching here is the assertion; a sanity check that it still works.
	if s.IsDormant("dns") && len(s.Dormant()) == 0 {
		t.Error("IsDormant and Dormant disagree — the state map was corrupted")
	}
}
