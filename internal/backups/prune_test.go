package backups

import (
	"encoding/json"
	"os"
	"testing"
)

// The differential gate for retention.
//
// This function DELETES restore points. Every other gate in this port protects a
// rendering or a payload; getting this one wrong loses the artefact the whole
// feature exists to produce, and loses it quietly — a pruned pair looks exactly
// like one that was never taken.
//
// Cases come from the backup-prune corpus, which RUNS the live
// implementation, so the expectations are its answers rather than a second
// reading of the same source.

type pruneCase struct {
	Name  string `json:"name"`
	Pairs []Pair `json:"pairs"`
	Opts  struct {
		KeepCount any `json:"keepCount"`
		KeepDays  any `json:"keepDays"`
	} `json:"opts"`
	Now  int64    `json:"now"`
	Want []string `json:"want"`
}

// leniently is `Number(x) || 0`, which is how the original reads its limits: a
// numeric string counts, anything unparseable is 0 and the limit is not applied.
func leniently(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		var f float64
		if _, err := jsonNumber(t, &f); err == nil {
			return int(f)
		}
	}
	return 0
}

func jsonNumber(s string, out *float64) (bool, error) {
	if err := json.Unmarshal([]byte(s), out); err != nil {
		return false, err
	}
	return true, nil
}

func TestSelectForPruningAgainstTheLiveStore(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/backup-prune-cases.json")
	if err != nil {
		t.Fatalf("case file missing — run tools/backup-prune-cases.js: %v", err)
	}
	var f struct {
		Now   int64       `json:"now"`
		Cases []pruneCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases — a green run here would mean nothing")
	}

	// A corpus where nothing is ever pruned would pass against a function that
	// returns nil unconditionally.
	pruning := 0
	for _, c := range f.Cases {
		if len(c.Want) > 0 {
			pruning++
		}
	}
	if pruning == 0 {
		t.Fatal("no case prunes anything — this would pass against a stub")
	}

	for _, c := range f.Cases {
		got := SelectForPruning(c.Pairs, Retention{
			KeepCount: leniently(c.Opts.KeepCount),
			KeepDays:  leniently(c.Opts.KeepDays),
		}, c.Now)
		if len(got) != len(c.Want) {
			t.Errorf("%s\n    got  %v\n    live %v", c.Name, got, c.Want)
			continue
		}
		for i := range got {
			if got[i] != c.Want[i] {
				t.Errorf("%s\n    got  %v\n    live %v", c.Name, got, c.Want)
				break
			}
		}
	}
	t.Logf("%d cases, %d that prune something", len(f.Cases), pruning)
}

// TestTheNewestPairSurvivesEverythingItShould states the invariant separately
// from the corpus, because it is the one an operator relies on.
func TestTheNewestPairSurvivesEverythingItShould(t *testing.T) {
	// Every pair far older than KeepDays. Pairs are written only when the
	// configuration changed, so the newest IS the current configuration.
	pairs := []Pair{
		{Stem: "2024-01-03T090000"}, {Stem: "2024-01-02T090000"}, {Stem: "2024-01-01T090000"},
	}
	got := SelectForPruning(pairs, Retention{KeepCount: 1, KeepDays: 1}, 1789000000000)
	for _, s := range got {
		if s == "2024-01-03T090000" {
			t.Fatal("the newest pair was selected for pruning; a stable router " +
				"would lose its only restore point precisely because nothing went wrong")
		}
	}
	if len(got) != 2 {
		t.Errorf("selected %v, want the two older pairs", got)
	}
}

// TestAStrayStemCannotDoomEveryRealBackup was a REPRODUCTION of ToDo item 13
// and is now the pin on its fix.
//
// It asserted the wrong answer on purpose while the live side still had the bug:
// a stem that is not a timestamp sorted above every real one, took the
// never-remove-the-newest slot, and left KeepCount 1 selecting every real
// backup. The live side fixed it in `Pruning keeps its promise` (v0.7.33+) by
// dropping unparseable stems before the sort, and this now asserts the invariant
// instead of the defect.
func TestAStrayStemCannotDoomEveryRealBackup(t *testing.T) {
	pairs := []Pair{
		{Stem: "not-a-timestamp"},
		{Stem: "2026-03-15T093000"}, {Stem: "2026-03-14T093000"}, {Stem: "2026-03-13T093000"},
	}
	got := SelectForPruning(pairs, Retention{KeepCount: 1}, 1773567000000)
	for _, st := range got {
		if st == "2026-03-15T093000" {
			t.Fatalf("the real newest was selected for pruning (%v) — a stray file "+
				"took the protected slot and every genuine restore point was doomed", got)
		}
		if st == "not-a-timestamp" {
			t.Errorf("a file this app did not write was offered as prunable (%v); "+
				"it is not the sweep's to delete", got)
		}
	}
	if len(got) != 2 {
		t.Errorf("selected %v, want the two older real pairs", got)
	}
}

// TestALowSortingStrayIsAlsoIgnored — the mirror case, and the one plain
// sample choice misses. A stem starting with a letter sorts ABOVE the
// timestamps; one starting with punctuation sorts BELOW them, lands last, and
// would be selected by KeepCount as the oldest thing present. Neither is the
// sweep's to delete.
func TestALowSortingStrayIsAlsoIgnored(t *testing.T) {
	pairs := []Pair{
		{Stem: "2026-03-15T093000"}, {Stem: "2026-03-14T093000"}, {Stem: "!stray"},
	}
	got := SelectForPruning(pairs, Retention{KeepCount: 1}, 1773567000000)
	for _, st := range got {
		if st == "!stray" {
			t.Errorf("a low-sorting stray was selected for pruning (%v)", got)
		}
	}
	if len(got) != 1 || got[0] != "2026-03-14T093000" {
		t.Errorf("selected %v, want only the older real pair", got)
	}
}
