package backups

import (
	"errors"
	"os"
	"testing"
)

// fakeStore drives PruneFor without a database.
type fakeStore struct {
	rows     []StoredPair
	marked   []int64
	markErr  map[int64]error
	rowsErr  error
	markCall int
}

func (f *fakeStore) StoredBackupsFor(string) ([]StoredPair, error) {
	return f.rows, f.rowsErr
}
func (f *fakeStore) MarkPruned(id int64, ts int64) (bool, error) {
	f.markCall++
	if err := f.markErr[id]; err != nil {
		return false, err
	}
	f.marked = append(f.marked, id)
	return true, nil
}

// seedPairs writes real files so the sweep has something to delete, newest first.
func seedPairs(t *testing.T, dir string, stems ...string) []StoredPair {
	t.Helper()
	out := []StoredPair{}
	for i, s := range stems {
		if _, _, err := WritePair(dir, s, "config "+s, []byte(s)); err != nil {
			t.Fatal(err)
		}
		out = append(out, StoredPair{ID: int64(i + 1), Stem: s, Dir: dir, Bytes: 10})
	}
	return out
}

const (
	newest = "2026-03-15T093000"
	mid    = "2026-03-14T093000"
	oldest = "2026-03-13T093000"
)

func TestPruneForRemovesFilesAndMarksRows(t *testing.T) {
	dir := t.TempDir()
	f := &fakeStore{rows: seedPairs(t, dir, newest, mid, oldest)}

	n, err := PruneFor(f, "r1", Retention{KeepCount: 1}, 1773567000000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned %d, want 2", n)
	}
	// The newest survives; the other two are gone from disk AND marked.
	if !HasPair(dir, newest) {
		t.Error("the newest pair was deleted")
	}
	for _, s := range []string{mid, oldest} {
		if HasPair(dir, s) {
			t.Errorf("%s is still on disk", s)
		}
	}
	if len(f.marked) != 2 {
		t.Errorf("marked %v, want two rows", f.marked)
	}
}

// TestNeitherLimitMeansNoQuery — a fleet that has opted out of retention should
// not cost a query per router per tick.
func TestNeitherLimitMeansNoQuery(t *testing.T) {
	f := &fakeStore{rowsErr: errors.New("StoredBackupsFor should not have been called")}
	n, err := PruneFor(f, "r1", Retention{}, 1773567000000, nil)
	if err != nil || n != 0 {
		t.Fatalf("got %d, %v", n, err)
	}
}

// TestTheRowIsMarkedONLYAfterTheFilesAreGone. The other order leaves a row
// claiming its artefacts were pruned while they are still on disk — and nothing
// tries again, because the sweep only considers rows whose pruned_at is null.
func TestTheRowIsMarkedONLYAfterTheFilesAreGone(t *testing.T) {
	dir := t.TempDir()
	rows := seedPairs(t, dir, newest, mid, oldest)
	// Make the removal of `oldest` fail by putting a directory where its file is.
	if _, err := RemovePair(dir, oldest); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RscPath(dir, oldest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RscPath(dir, oldest)+"/busy", 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeStore{rows: rows}

	var logs []string
	n, err := PruneFor(f, "r1", Retention{KeepCount: 1}, 1773567000000,
		func(s string) { logs = append(logs, s) })
	if err != nil {
		t.Fatal(err)
	}
	// `mid` went; `oldest` could not, so it must NOT be marked.
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	for _, id := range f.marked {
		if id == 3 {
			t.Error("a row whose files could not be removed was marked pruned — " +
				"nothing will try again, because the sweep skips rows with pruned_at set")
		}
	}
	found := false
	for _, l := range logs {
		if len(l) > 15 && l[:15] == "could not prune" {
			found = true
		}
	}
	if !found {
		t.Errorf("the failure was not logged: %v", logs)
	}
}

// TestOneFailureDoesNotStopTheSweep — the next pair is still over the limit, and
// a sweep that aborts leaves a directory growing without bound.
func TestOneFailureDoesNotStopTheSweep(t *testing.T) {
	dir := t.TempDir()
	rows := seedPairs(t, dir, newest, mid, oldest)
	f := &fakeStore{rows: rows, markErr: map[int64]error{2: errors.New("db locked")}}

	var logs []string
	n, err := PruneFor(f, "r1", Retention{KeepCount: 1}, 1773567000000,
		func(s string) { logs = append(logs, s) })
	if err != nil {
		t.Fatal(err)
	}
	// Row 2's mark failed, row 3's succeeded — the sweep carried on.
	if n != 1 {
		t.Errorf("pruned %d, want 1 (row 2 failed to record, row 3 did not)", n)
	}
	if f.markCall != 2 {
		t.Errorf("attempted %d marks, want 2 — the sweep stopped early", f.markCall)
	}
}

// TestNothingDoomedTouchesNothing.
func TestNothingDoomedTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	f := &fakeStore{rows: seedPairs(t, dir, newest, mid)}
	n, err := PruneFor(f, "r1", Retention{KeepCount: 10}, 1773567000000, nil)
	if err != nil || n != 0 {
		t.Fatalf("got %d, %v", n, err)
	}
	if !HasPair(dir, newest) || !HasPair(dir, mid) {
		t.Error("a sweep that should have done nothing deleted a pair")
	}
	if len(f.marked) != 0 {
		t.Errorf("marked %v with nothing doomed", f.marked)
	}
}
