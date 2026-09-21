package cfgtpl

import (
	"strings"
	"testing"

	"mikrodash/internal/backups"
)

func TestFileNamesNeverAutoRun(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		n, err := NewFileName()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(n, ".auto.") {
			t.Fatalf("%s would run itself when uploaded", n)
		}
		if !strings.HasSuffix(n, ".rsc") || !IsOurFile(n) {
			t.Fatalf("%s is not the shape the sweep recognises", n)
		}
		if strings.HasPrefix(n, backups.FilePrefix) {
			t.Fatalf("%s would be swept by Backups", n)
		}
		if seen[n] {
			t.Fatalf("%s was minted twice", n)
		}
		seen[n] = true
		w, r := ReportName(n)
		if !strings.HasPrefix(r, w) || !IsOurFile(r) || strings.Contains(r, ".auto.") {
			t.Fatalf("report %q / %q is not a file the sweep recognises", w, r)
		}
	}
}

// The sweep deletes by this, so an operator's own file with a similar name is
// not ours.
func TestOnlyOurOwnFilesAreSwept(t *testing.T) {
	for _, n := range []string{
		"mikrodash-cfg-notes.txt",
		"mikrodash-cfg-0123456789abcdef.auto.rsc",
		"mikrodash-cfg-0123456789ABCDEF.rsc",
		"flash/mikrodash-cfg-0123456789abcdef.rsc",
		"mikrodash-backup-0123456789abcdef.backup",
		"mikrodash-cfg-0123456789abcdef.rsc.bak",
	} {
		if IsOurFile(n) {
			t.Errorf("%s would be swept", n)
		}
	}
}
