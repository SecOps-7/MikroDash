package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// EVERY SCRIPT PROBE HAS ITS OWN OUTPUT FILE, or one measurement quietly
// replaces another. probe.script() writes its result to a file named
// prefix+label, then returns the first non-empty file whose NAME STARTS WITH
// that, so two labels collide when one is a prefix of the other, not only when
// they are equal: "z9" would read "z9a"'s file. A label the scan cannot read is
// a label it cannot check, so the scanned labels must account for every call.
func TestEveryZtpprobeScriptProbeHasItsOwnOutputFile(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "cmd", "ztpprobe", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	calls := string(src)
	re := regexp.MustCompile(`p\.script\("([^"]+)"`)
	var labels []string
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(calls, -1) {
		if seen[m[1]] {
			t.Errorf("two steps share the script-probe label %q, so they share its output file", m[1])
			continue
		}
		seen[m[1]] = true
		labels = append(labels, m[1])
	}
	if len(labels) == 0 {
		t.Fatal("found no p.script( labels; the scan is not reading the probe")
	}
	if got, want := len(labels), strings.Count(calls, "p.script("); got != want {
		t.Fatalf("%d p.script( call sites but %d scanned a literal label; a built label is unchecked", want, got)
	}
	for _, a := range labels {
		for _, b := range labels {
			if a != b && strings.HasPrefix(b, a) {
				t.Errorf("script-probe label %q is a prefix of %q, so it reads that step's output file", a, b)
			}
		}
	}
}
