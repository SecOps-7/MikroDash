package backups

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

const stem = "2026-03-15T093000"

func TestPairRoundTrips(t *testing.T) {
	dir := t.TempDir()
	rsc := "# 2026-03-15 09:30:00 by RouterOS 7.24\n/ip dns set servers=1.1.1.1\n"
	// The binary is arbitrary bytes, so it gets the adversarial payload.
	bin := binaryPayload()

	rscBytes, bakBytes, err := WritePair(dir, stem, rsc, bin)
	if err != nil {
		t.Fatal(err)
	}
	if rscBytes <= 0 || bakBytes != int64(len(bin)) {
		t.Errorf("reported %d/%d bytes, binary is %d", rscBytes, bakBytes, len(bin))
	}

	gotRsc, err := ReadRsc(dir, stem)
	if err != nil {
		t.Fatal(err)
	}
	if gotRsc != rsc {
		t.Errorf("export round-trip changed the text")
	}
	gotBin, err := ReadBackup(dir, stem)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBin, bin) {
		t.Error("the binary did not round-trip byte-for-byte")
	}
	if !HasPair(dir, stem) {
		t.Error("HasPair says no after WritePair said yes")
	}
}

// TestTheBinaryIsWrittenFirst pins the crash-safety order.
//
// If the process dies between the two writes, what must survive is a `.backup`
// with no `.rsc` — ListPairs ignores that, because a half-written pair is not a
// backup. The other order leaves a diffable export claiming a restore point
// exists, and the lie only surfaces when somebody tries to restore from it.
//
// Forced by making the SECOND write fail: a directory already occupying the
// `.rsc.gz` path. If the binary is on disk afterwards, it went first.
func TestTheBinaryIsWrittenFirst(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(RscPath(dir, stem), 0o755); err != nil {
		t.Fatal(err)
	}

	_, _, err := WritePair(dir, stem, "config", []byte("binary"))
	if err == nil {
		t.Fatal("writing over a directory should have failed")
	}
	if _, err := os.Stat(BackupPath(dir, stem)); err != nil {
		t.Fatal("the binary is NOT on disk after the export write failed — the " +
			"order is reversed, so a crash would leave an export claiming a " +
			"restore point that has no binary behind it")
	}
	// (The surviving-half case is TestListPairsIgnoresHalves, which uses real
	// files. This directory is only a way to make the second write fail; see
	// TestADirectoryCountsAsAHalf for what it does to ListPairs, which is a
	// quirk shared with the original rather than a property of this test.)
}

// TestADirectoryCountsAsAHalf documents a quirk BOTH sides have.
//
// Neither ListPairs nor the original's listPairs checks that an entry is a
// FILE, so a directory named `<stem>.rsc.gz` beside a real `<stem>.backup` is
// listed as a complete pair — with the directory's own size as `rscBytes`.
// Verified against the live implementation, which reports the same.
//
// Reproduced rather than fixed, per the porting rule. It is recorded here so the
// reproduction is deliberate: it needs a directory nothing in the app creates,
// and the consequence is a listed pair that ReadRsc then fails on, rather than a
// wrong answer about a real backup.
func TestADirectoryCountsAsAHalf(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(RscPath(dir, stem), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BackupPath(dir, stem), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pairs, err := ListPairs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 {
		t.Fatalf("got %v; the live implementation lists this as one pair, and a "+
			"port that filtered directories would disagree with it", pairs)
	}
	// And the consequence, stated: the pair cannot actually be read.
	if _, err := ReadRsc(dir, stem); err == nil {
		t.Error("ReadRsc succeeded on a directory")
	}
}

func TestListPairsIgnoresHalves(t *testing.T) {
	dir := t.TempDir()
	// A complete pair, and one of each lone half.
	if _, _, err := WritePair(dir, "2026-03-15T093000", "a", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-03-14T093000.backup"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-03-13T093000.rsc.gz"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	// And a file that is neither.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("z"), 0o600); err != nil {
		t.Fatal(err)
	}

	pairs, err := ListPairs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0].Stem != "2026-03-15T093000" {
		t.Fatalf("got %v, want only the complete pair", pairs)
	}
	if pairs[0].RscBytes <= 0 || pairs[0].BackupBytes != 1 {
		t.Errorf("sizes = %d/%d", pairs[0].RscBytes, pairs[0].BackupBytes)
	}
}

func TestListPairsIsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	for _, s := range []string{"2026-01-01T000000", "2026-03-15T093000", "2026-02-09T120000"} {
		if _, _, err := WritePair(dir, s, "x", []byte("y")); err != nil {
			t.Fatal(err)
		}
	}
	pairs, err := ListPairs(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-03-15T093000", "2026-02-09T120000", "2026-01-01T000000"}
	for i, w := range want {
		if pairs[i].Stem != w {
			t.Fatalf("order = %v, want %v", pairs, want)
		}
	}
}

// TestRemovePairIsIdempotent — pruning runs after a crash as readily as after a
// success, and a crash is exactly what leaves one half behind.
func TestRemovePairIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := WritePair(dir, stem, "a", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if n, err := RemovePair(dir, stem); err != nil || n != 2 {
		t.Fatalf("first remove: %d, %v", n, err)
	}
	// Again: nothing there, still not an error.
	if n, err := RemovePair(dir, stem); err != nil || n != 0 {
		t.Fatalf("second remove: %d, %v — a missing pair must not be an error", n, err)
	}
	// And one lone half.
	if err := os.WriteFile(BackupPath(dir, stem), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := RemovePair(dir, stem); err != nil || n != 1 {
		t.Fatalf("lone half: %d, %v", n, err)
	}
	if HasPair(dir, stem) {
		t.Error("HasPair still says yes after removal")
	}
}

// TestAMissingDirectoryIsEmptyNotAnError — a router that has never been backed
// up has no directory, and that is not a fault.
func TestAMissingDirectoryIsEmptyNotAnError(t *testing.T) {
	pairs, err := ListPairs(filepath.Join(t.TempDir(), "never-used"))
	if err != nil {
		t.Fatalf("a missing directory was an error: %v", err)
	}
	if len(pairs) != 0 {
		t.Errorf("got %v", pairs)
	}
	if n, err := UsageOf(filepath.Join(t.TempDir(), "never-used")); err != nil || n != 0 {
		t.Errorf("UsageOf = %d, %v", n, err)
	}
}

// TestUsageCountsBothHalves — the page's disk figure is the pair, not the binary.
func TestUsageCountsBothHalves(t *testing.T) {
	dir := t.TempDir()
	rscBytes, bakBytes, err := WritePair(dir, stem, "some configuration text", []byte("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := UsageOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != rscBytes+bakBytes {
		t.Errorf("usage = %d, want %d (%d + %d)", got, rscBytes+bakBytes, rscBytes, bakBytes)
	}
}
