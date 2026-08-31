package backups

import (
	"encoding/json"
	"os"
	"testing"
)

// The differential gate for IsDue.
//
// The cases come from `tools/backup-due-cases.js`, which RUNS the live
// implementation rather than describing it — so the expectations here are the
// live app's answers, not a second reading of the same source.
//
// The case list deliberately puts `lastRun` LATE IN THE DAY. The live tests all
// placed it just before the previous day's target, so a full interval had always
// elapsed by the next one and the elapsed-interval gate could never hold a run
// back. The bug lived in the gap those cases did not cover; reproducing their
// shape would reproduce the blind spot along with the code.

type dueCase struct {
	Name   string `json:"name"`
	Backup *struct {
		Enabled  bool    `json:"enabled"`
		Schedule string  `json:"schedule"`
		Time     *string `json:"time"`
	} `json:"backup"`
	LastRun int64  `json:"lastRun"`
	Now     int64  `json:"now"`
	TZ      string `json:"tz"`
	Want    bool   `json:"want"`
}

func TestIsDueAgainstTheLiveScheduler(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/backup-due-cases.json")
	if err != nil {
		t.Fatalf("case file missing — run tools/backup-due-cases.js: %v", err)
	}
	var f struct {
		Schedules   map[string]int64 `json:"schedules"`
		DefaultTime string           `json:"defaultTime"`
		Cases       []dueCase        `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases — a green run here would mean nothing")
	}

	// The constants are pinned too. They are hard-coded on this side, so a
	// change upstream must fail here rather than being absorbed silently.
	if f.DefaultTime != DefaultTime {
		t.Errorf("default time: live %q, port %q", f.DefaultTime, DefaultTime)
	}
	for name, ms := range f.Schedules {
		if Schedules[name] != ms {
			t.Errorf("schedule %q: live %d, port %d", name, ms, Schedules[name])
		}
	}
	if len(f.Schedules) != len(Schedules) {
		t.Errorf("schedule count: live %d, port %d", len(f.Schedules), len(Schedules))
	}

	// A gate whose cases all answer the same way proves nothing about the half
	// it never exercises.
	due := 0
	for _, c := range f.Cases {
		if c.Want {
			due++
		}
	}
	if due == 0 || due == len(f.Cases) {
		t.Fatalf("every case answers %v — the corpus exercises one branch only", due > 0)
	}

	for _, c := range f.Cases {
		var b *Backup
		if c.Backup != nil {
			b = &Backup{Enabled: c.Backup.Enabled, Schedule: c.Backup.Schedule, Time: c.Backup.Time}
		}
		if got := IsDue(b, c.LastRun, c.Now, c.TZ); got != c.Want {
			t.Errorf("%s\n    IsDue = %v, live = %v", c.Name, got, c.Want)
		}
	}
	t.Logf("%d cases, %d due", len(f.Cases), due)
}
