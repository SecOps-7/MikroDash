package collect

// Replays testdata/system-update-cases.json into the Go system collector's two
// pure update decisions.
//
// This path had NO coverage before: `system` has no golden (it fills a cache
// and emits from elsewhere), there was no system test, and its fixture cannot
// reach the update reads — `check-for-updates` contacts MikroTik's upstream
// server behind a 15-second timeout, which no capture settle window waits for.
// See the generator's header for the measurement.

import (
	"encoding/json"
	"mikrodash/internal/hub"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

type sysUpdateCase struct {
	Name string `json:"name"`
	// `any`, not a map: the corpus carries a string and a number for the
	// not-a-row cases, which only exist because JavaScript can be handed one.
	Row             any               `json:"row"`
	Version         string            `json:"version"`
	IsAnswer        bool              `json:"isAnswer"`
	NotARow         bool              `json:"notARow"`
	LatestVersion   *string           `json:"latestVersion"`
	UpdateStatus    *string           `json:"updateStatus"`
	InstalledBase   *string           `json:"installedBase"`
	UpdateAvailable *bool             `json:"updateAvailable"`
	_               map[string]string `json:"-"`
}

func TestSystemUpdateMatchesTheLiveRule(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(testdata, "system-update-cases.json"))
	if err != nil {
		t.Fatalf("no corpus — run: node tools/system-update-cases.js: %v", err)
	}
	var payload struct {
		Cases []sysUpdateCase `json:"cases"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Cases) == 0 {
		t.Fatal("the corpus is empty")
	}

	answers, available := 0, 0
	for _, c := range payload.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			row := routeros.Reply{}
			if m, ok := c.Row.(map[string]any); ok {
				for k, v := range m {
					if s, ok := v.(string); ok {
						row[k] = s
					}
				}
			}
			// A non-object row cannot exist in Go — a reply is always a map —
			// so only the EMPTY case is meaningful here. It still has to agree:
			// an empty row is not an answer, which is what stops a failed check
			// poisoning the shared per-router slot.
			// The Go side names this the other way round — `updateTransient` is
			// "the router is still working on it", the exact inverse of the
			// original's `_isUpdateAnswer`. Asserting the INVERSE relationship
			// rather than renaming either side keeps both readable next to their
			// own code, and pins that the two really are complementary: a port
			// that got one edge case backwards would show up here and nowhere
			// else, because nothing but this compares them.
			if got := !updateTransient(row); got != c.IsAnswer {
				t.Errorf("!updateTransient = %v, live _isUpdateAnswer = %v (row %v)",
					got, c.IsAnswer, c.Row)
			}
			if c.IsAnswer {
				answers++
			}
			if c.NotARow || c.UpdateAvailable == nil {
				return
			}

			// The installed base, stripped of its channel suffix exactly as the
			// original strips it: `7.24 (stable)` is not a different version
			// from `7.24`, and treating it as one reports an update on every
			// router for ever.
			base := strings.TrimSpace(parenSuffix.ReplaceAllString(c.Version, ""))
			if c.InstalledBase != nil && base != *c.InstalledBase {
				t.Errorf("installed base = %q, live = %q", base, *c.InstalledBase)
			}
			got := UpdateVerdict(row["latest-version"], row["status"], base)
			if got != *c.UpdateAvailable {
				t.Errorf("updateAvailable = %v, live = %v (latest=%q status=%q base=%q)",
					got, *c.UpdateAvailable, row["latest-version"], row["status"], base)
			}
			if got {
				available++
			}
		})
	}

	// The same believability the generator asserts, repeated on this side so a
	// corpus swapped for a weaker one cannot turn the suite into a no-op.
	if answers == 0 {
		t.Error("no case is an update answer — this suite cannot see a function that always says no")
	}
	if available == 0 {
		t.Error("no case reports an available update")
	}
	t.Logf("%d answers, %d available", answers, available)
}

// updRetryReader answers the update print with a TRANSIENT row until it is told
// otherwise, which is what a router whose check is still in flight does.
type updRetryReader struct {
	mu      sync.Mutex
	settled bool
	prints  int
	checks  int
}

func (r *updRetryReader) Connected() bool { return true }

func (r *updRetryReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch cmd.Path {
	case systemUpdateCheckCmd.Path:
		r.checks++
		return nil, nil
	case systemUpdatePrintCmd.Path:
		r.prints++
		if !r.settled {
			// RouterOS's own wording while it is still asking upstream.
			return []routeros.Reply{{
				"channel": "stable", "installed-version": "7.24.1",
				"status": "finding out latest version...",
			}}, nil
		}
		return []routeros.Reply{{
			"channel": "stable", "installed-version": "7.24.1",
			"latest-version": "7.24.2", "status": "New version is available",
		}}, nil
	case systemResourceCmd.Path:
		return []routeros.Reply{{"version": "7.24.1 (stable)", "cpu-load": "1",
			"free-memory": "1", "total-memory": "2", "uptime": "1h"}}, nil
	}
	return nil, nil
}

func (r *updRetryReader) counts() (prints, checks int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prints, r.checks
}

func (r *updRetryReader) settle() {
	r.mu.Lock()
	r.settled = true
	r.mu.Unlock()
}

// TestATransientUpdateAnswerIsRetried.
//
// ── THE RETRY WAS A PERMISSION NOBODY ACTED ON ─────────────────────────────
//
// "finding out latest version..." is not a verdict, so `checkForUpdates` rewinds
// `updateAt` to come back in a minute, up to three times. `Start` was its ONLY
// caller, so nothing ever came back: a router whose check was still in flight
// when the first print ran showed that string for the life of the session, while
// the router itself said "New version is available" throughout.
//
// DRIVEN THROUGH `preRead`, which is the wiring rather than the helper. A test
// that called `checkForUpdates` twice by hand passes against the bug — the
// function always worked; nothing invoked it.
func TestATransientUpdateAnswerIsRetried(t *testing.T) {
	r := &updRetryReader{}
	s := NewSystem(r, hub.Relay{}, 2000)

	// A resource read first, because the payload is built there and the update
	// fields ride along on it.
	s.Tick()
	// The first check, exactly as Start makes it.
	s.checkForUpdates()
	s.Tick()
	if got := s.Last(); got == nil || got.UpdateStatus != "finding out latest version..." {
		t.Fatalf("first check did not record the transient answer: %+v", got)
	}

	// A tick BEFORE the retry window is up must not ask again: the check leaves
	// the router, and an update server that never settles must not become a poll.
	prints, _ := r.counts()
	s.preRead()
	time.Sleep(50 * time.Millisecond)
	if p2, _ := r.counts(); p2 != prints {
		t.Errorf("the check ran again inside its retry window (%d -> %d prints)", prints, p2)
	}

	// ── THE RETRY IS THE COLLECTOR'S OWN, NOT THE TEST'S ──────────────────
	//
	// A transient answer rewinds `updateAt` to `now - 12h + 60s`, so the check
	// comes back in a minute instead of in twelve hours. Simulating that minute
	// passing is the ONLY thing this does: it nudges `updateAt` back by exactly
	// the retry delay.
	//
	// Setting it to a flat twelve hours ago instead would make the test pass
	// against a collector that never rewound — the retry would be the test's,
	// and a transient answer would really wait out the full window. That
	// mutation survived until this was written this way.
	r.settle()
	s.mu.Lock()
	s.updateAt = s.updateAt.Add(-systemUpdateRetry)
	s.mu.Unlock()

	s.preRead()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.Tick()
		if got := s.Last(); got != nil && got.LatestVersion == "7.24.2" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := s.Last()
	t.Errorf("the verdict never replaced the transient answer: status=%q latest=%q\n"+
		"`preRead` runs on both delivery paths and is what has to call the retry; "+
		"without it the card reads \"finding out latest version…\" for ever.",
		got.UpdateStatus, got.LatestVersion)
}
