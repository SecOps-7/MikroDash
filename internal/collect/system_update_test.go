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
	"os"
	"path/filepath"
	"strings"
	"testing"

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
			got := updateVerdict(row["latest-version"], row["status"], base)
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
