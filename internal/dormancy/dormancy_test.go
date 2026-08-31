package dormancy

// Every step of every sequence, against what the LIVE state machine did.
//
// `tools/dormancy-cases.js` drives the real `createDormancyState` and records,
// after each step, the verdict AND all five getters — so a port that reaches the
// right answer through the wrong internal state fails on the step after.

import (
	"encoding/json"
	"os"
	"testing"
)

type dormStep struct {
	Op  string `json:"op"`
	Now int64  `json:"now"`
	Obs *struct {
		TS          int64 `json:"ts"`
		Empty       bool  `json:"empty"`
		Unsupported bool  `json:"unsupported"`
	} `json:"obs"`
	Verdict *string `json:"verdict"`
	Due     *bool   `json:"due"`
	State   struct {
		Dormant bool  `json:"dormant"`
		Probing bool  `json:"probing"`
		Streak  int   `json:"streak"`
		DelayMs int64 `json:"delayMs"`
		WakeAt  int64 `json:"wakeAt"`
	} `json:"state"`
}

type dormCase struct {
	Why   string     `json:"why"`
	Steps []dormStep `json:"steps"`
}

type dormDoc struct {
	Defaults Options    `json:"defaults"`
	Cases    []dormCase `json:"cases"`
}

func loadDormancy(t *testing.T) dormDoc {
	t.Helper()
	b, err := os.ReadFile("../../testdata/dormancy-cases.json")
	if err != nil {
		t.Fatalf("corpus missing: %v — run tools/dormancy-cases.js", err)
	}
	var doc dormDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("corpus is empty")
	}
	return doc
}

// TestTheDefaultsAreTheLiveOnes — the corpus records what it ran with, so a port
// that drifted on one constant fails here rather than in every sequence at once.
func TestTheDefaultsAreTheLiveOnes(t *testing.T) {
	doc := loadDormancy(t)
	if Defaults() != doc.Defaults {
		t.Errorf("Defaults() = %+v, the live destructuring defaults are %+v",
			Defaults(), doc.Defaults)
	}
}

func TestEverySequenceMatchesLive(t *testing.T) {
	doc := loadDormancy(t)
	for _, c := range doc.Cases {
		c := c
		t.Run(c.Why, func(t *testing.T) {
			// THE OPTIONS COME FROM THE CASE, not from Defaults(): one sequence
			// deliberately uses a threshold of 1 and a 10s backoff, and running it
			// with the defaults would let a port with them hard-coded pass.
			opt := doc.Defaults
			if c.Why == "the options are honoured: a threshold of 1 and a 10s backoff" {
				opt.EmptyThreshold, opt.BackoffMs, opt.MaxBackoffMs = 1, 10000, 40000
			}
			s := New(opt)

			for i, step := range c.Steps {
				switch step.Op {
				case "observe":
					if step.Obs == nil {
						t.Fatalf("step %d is an observe with no observation", i)
					}
					got := s.Observe(Observation{
						TS: step.Obs.TS, Empty: step.Obs.Empty,
						Unsupported: step.Obs.Unsupported}, step.Now)
					want := None
					if step.Verdict != nil {
						want = Verdict(*step.Verdict)
					}
					if got != want {
						t.Errorf("step %d: verdict %q, live returned %q", i, got, want)
					}
				case "dueForProbe":
					got := s.DueForProbe(step.Now)
					if step.Due == nil {
						t.Fatalf("step %d is a dueForProbe with no recorded answer", i)
					}
					if got != *step.Due {
						t.Errorf("step %d: dueForProbe %v, live returned %v", i, got, *step.Due)
					}
				case "markProbed":
					s.MarkProbed(step.Now)
				case "reset":
					s.Reset()
				default:
					t.Fatalf("step %d has an unknown op %q", i, step.Op)
				}

				// ALL FIVE, after EVERY step. The verdict alone would let a port
				// keep the wrong streak or the wrong delay until much later.
				if s.Dormant() != step.State.Dormant {
					t.Errorf("step %d (%s): dormant %v, live %v", i, step.Op, s.Dormant(), step.State.Dormant)
				}
				if s.Probing() != step.State.Probing {
					t.Errorf("step %d (%s): probing %v, live %v", i, step.Op, s.Probing(), step.State.Probing)
				}
				if s.Streak() != step.State.Streak {
					t.Errorf("step %d (%s): streak %d, live %d", i, step.Op, s.Streak(), step.State.Streak)
				}
				if s.DelayMs() != step.State.DelayMs {
					t.Errorf("step %d (%s): delayMs %d, live %d", i, step.Op, s.DelayMs(), step.State.DelayMs)
				}
				if s.WakeAt() != step.State.WakeAt {
					t.Errorf("step %d (%s): wakeAt %d, live %d", i, step.Op, s.WakeAt(), step.State.WakeAt)
				}
			}
		})
	}
}

// TestTheCorpusStillDiscriminates.
//
// A corpus that never sleeps, never wakes, or never returns None agrees with a
// state machine that does nothing. The generator asserts this too; asserted here
// as well because a corpus can be regenerated from a changed live file, and this
// is the side that would silently start passing.
func TestTheCorpusStillDiscriminates(t *testing.T) {
	doc := loadDormancy(t)
	seen := map[string]int{}
	steps := 0
	for _, c := range doc.Cases {
		for _, s := range c.Steps {
			steps++
			if s.Verdict != nil {
				seen[*s.Verdict]++
			}
		}
	}
	for _, v := range []string{"sleep", "wake"} {
		if seen[v] == 0 {
			t.Errorf("no step in the corpus produces %q", v)
		}
	}
	if steps < 40 {
		t.Errorf("the corpus is down to %d steps; it pinned 51 when written", steps)
	}
}
