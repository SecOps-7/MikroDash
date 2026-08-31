package backups

import (
	"errors"
	"os"
	"testing"
)

type fakeDeleteStore struct {
	rows      map[int64]*DeletableRow
	owner     map[int64]string
	deleted   []int64
	deleteErr map[int64]error
}

func (f *fakeDeleteStore) RowFor(id int64, routerID string) *DeletableRow {
	if f.owner[id] != routerID {
		return nil
	}
	return f.rows[id]
}

func (f *fakeDeleteStore) DeleteRow(id int64) (bool, error) {
	if err := f.deleteErr[id]; err != nil {
		return false, err
	}
	f.deleted = append(f.deleted, id)
	delete(f.rows, id)
	return true, nil
}

func seedDelete(t *testing.T, dir string) *fakeDeleteStore {
	t.Helper()
	f := &fakeDeleteStore{
		rows:      map[int64]*DeletableRow{},
		owner:     map[int64]string{},
		deleteErr: map[int64]error{},
	}
	// 1: a real pair. 2: a pruned row (files already gone). 3: a run that stored
	// nothing. 4: a real pair on ANOTHER router.
	if _, _, err := WritePair(dir, "2026-03-15T093000", "cfg", []byte("bin")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WritePair(dir, "2026-03-10T093000", "cfg", []byte("bin")); err != nil {
		t.Fatal(err)
	}
	f.rows[1] = &DeletableRow{ID: 1, Stem: "2026-03-15T093000", Dir: dir}
	f.rows[2] = &DeletableRow{ID: 2, Stem: "2026-03-14T093000", Dir: dir, Pruned: true}
	f.rows[3] = &DeletableRow{ID: 3}
	f.rows[4] = &DeletableRow{ID: 4, Stem: "2026-03-10T093000", Dir: dir}
	f.owner[1], f.owner[2], f.owner[3] = "r1", "r1", "r1"
	f.owner[4] = "r2"
	return f
}

func TestDeleteRemovesFilesThenRow(t *testing.T) {
	dir := t.TempDir()
	f := seedDelete(t, dir)

	removed, failed := DeleteFor(f, "r1", []int64{1}, dir, nil)
	if failed != 0 || len(removed) != 1 || removed[0] != 1 {
		t.Fatalf("removed=%v failed=%d", removed, failed)
	}
	if HasPair(dir, "2026-03-15T093000") {
		t.Error("the files are still on disk")
	}
	if len(f.deleted) != 1 || f.deleted[0] != 1 {
		t.Errorf("rows deleted = %v", f.deleted)
	}
}

// TestARowWithNoFilesIsStillDeletable. A run that stored nothing, or one whose
// pair retention already took, is still a row in the operator's list. Refusing
// those would leave rows nothing can ever clear.
func TestARowWithNoFilesIsStillDeletable(t *testing.T) {
	dir := t.TempDir()
	f := seedDelete(t, dir)

	removed, failed := DeleteFor(f, "r1", []int64{2, 3}, dir, nil)
	if failed != 0 || len(removed) != 2 {
		t.Fatalf("removed=%v failed=%d — a pruned row and a run that stored "+
			"nothing must both be removable", removed, failed)
	}
	// And the pruned row's stem must NOT have been unlinked: those files belong
	// to a different pair that is still live.
	if !HasPair(dir, "2026-03-10T093000") {
		t.Error("deleting a pruned row removed somebody else's files")
	}
}

// TestARowOnAnotherRouterIsSkippedSilently — not an error, and not touched.
func TestARowOnAnotherRouterIsSkippedSilently(t *testing.T) {
	dir := t.TempDir()
	f := seedDelete(t, dir)

	var logs []string
	removed, failed := DeleteFor(f, "r1", []int64{4}, dir, func(s string) { logs = append(logs, s) })
	if len(removed) != 0 || failed != 0 {
		t.Fatalf("removed=%v failed=%d for a row on another router", removed, failed)
	}
	if len(logs) != 0 {
		t.Errorf("logged %v; a selection that raced a sweep is not worth showing", logs)
	}
	if !HasPair(dir, "2026-03-10T093000") {
		t.Error("another router's files were deleted")
	}
	if len(f.deleted) != 0 {
		t.Errorf("another router's row was deleted: %v", f.deleted)
	}

	// AND THE OTHER DIRECTION, which is what proves the routerID argument is
	// actually used: r2 deleting its OWN row must succeed. Without this the
	// scoping could be hardcoded to "r1" and every test above would still pass,
	// because every one of them passes "r1" — verified by mutation.
	removed, failed = DeleteFor(f, "r2", []int64{4}, dir, nil)
	if failed != 0 || len(removed) != 1 || removed[0] != 4 {
		t.Fatalf("r2 could not delete its own row: removed=%v failed=%d", removed, failed)
	}
	if HasPair(dir, "2026-03-10T093000") {
		t.Error("r2's files survived its own delete")
	}
}

// TestTheRowSurvivesAFailedUnlink is the ordering rule.
//
// Drop the row first and fail the unlink, and several MB are orphaned on disk
// with nothing left pointing at them — nothing lists it, nothing prunes it, and
// only a human reading the directory would ever find it.
func TestTheRowSurvivesAFailedUnlink(t *testing.T) {
	dir := t.TempDir()
	f := seedDelete(t, dir)
	// Make the unlink of row 1's pair fail: replace the .rsc.gz with a
	// non-empty directory, which os.Remove refuses.
	stem := "2026-03-15T093000"
	if _, err := RemovePair(dir, stem); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RscPath(dir, stem)+"/busy", 0o755); err != nil {
		t.Fatal(err)
	}

	var logs []string
	removed, failed := DeleteFor(f, "r1", []int64{1}, dir, func(s string) { logs = append(logs, s) })
	if len(removed) != 0 || failed != 1 {
		t.Fatalf("removed=%v failed=%d", removed, failed)
	}
	if len(f.deleted) != 0 {
		t.Fatal("the ROW was deleted although its files could not be — the pair " +
			"is now orphaned on disk with nothing pointing at it")
	}
	if len(logs) != 1 {
		t.Errorf("logged %v, want one failure", logs)
	}
}

// TestOneFailureDoesNotStopTheRest — the operator selected several, and one
// locked file must not silently abandon the others.
func TestOneFailureDoesNotStopTheRest(t *testing.T) {
	dir := t.TempDir()
	f := seedDelete(t, dir)
	f.deleteErr[1] = errors.New("db locked")

	removed, failed := DeleteFor(f, "r1", []int64{1, 3}, dir, nil)
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	if len(removed) != 1 || removed[0] != 3 {
		t.Errorf("removed = %v, want the second id", removed)
	}
}

func TestNormalizeIDsIsBoundedAndDeduplicated(t *testing.T) {
	raw := []any{float64(3), float64(7), float64(3), "5", float64(3.5), "x", nil,
		true, "  9  "}
	got := NormalizeIDs(raw)
	want := []int64{3, 7, 5, 9}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	// Bounded: one message must not ask for unbounded filesystem work.
	big := make([]any, 0, 500)
	for i := 0; i < 500; i++ {
		big = append(big, float64(i))
	}
	if n := len(NormalizeIDs(big)); n != MaxDeletePerRequest {
		t.Errorf("500 ids produced %d, want %d", n, MaxDeletePerRequest)
	}
}

// TestAFractionalIDIsDroppedNotRounded — an id is not a quantity, so rounding
// one would address a DIFFERENT row.
func TestAFractionalIDIsDroppedNotRounded(t *testing.T) {
	for _, v := range []any{float64(3.5), "3.5", "3.0001"} {
		if got := NormalizeIDs([]any{v}); len(got) != 0 {
			t.Errorf("NormalizeIDs(%#v) = %v, want nothing", v, got)
		}
	}
	// A whole number written with a decimal point is still whole.
	if got := NormalizeIDs([]any{"3.0", float64(4.0)}); len(got) != 2 {
		t.Errorf("got %v, want 3 and 4", got)
	}
}
