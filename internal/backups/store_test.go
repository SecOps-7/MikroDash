package backups

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The differential gate for the store's path helpers.
//
// `slugFor` is the one that matters: it turns an operator-supplied label into a
// directory name, and its own comment states the property — "a label can never
// escape the base directory". The corpus therefore carries traversal shapes
// rather than only the labels a real fleet happens to have.

type storeCases struct {
	Slugs []struct {
		Label *string `json:"label"`
		Slug  string  `json:"slug"`
	} `json:"slugs"`
	Paths []struct {
		Slug   string `json:"slug"`
		Dir    string `json:"dir"`
		Rsc    string `json:"rsc"`
		Backup string `json:"backup"`
	} `json:"paths"`
	Stems []struct {
		TS   int64  `json:"ts"`
		Stem string `json:"stem"`
	} `json:"stems"`
	RoundTrip []struct {
		Stem string `json:"stem"`
		MS   int64  `json:"ms"`
		TS   int64  `json:"ts"`
	} `json:"roundTrip"`
}

func loadStoreCases(t *testing.T) storeCases {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/backup-store-cases.json")
	if err != nil {
		t.Fatalf("case file missing — run tools/backup-store-cases.js: %v", err)
	}
	var c storeCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Slugs) == 0 {
		t.Fatal("no slug cases")
	}
	return c
}

func TestSlugForAgainstLive(t *testing.T) {
	c := loadStoreCases(t)
	for _, s := range c.Slugs {
		in := ""
		if s.Label != nil {
			in = *s.Label
		}
		if got := SlugFor(in); got != s.Slug {
			t.Errorf("SlugFor(%q)\n    got  %q\n    live %q", in, got, s.Slug)
		}
	}
}

// TestSlugCannotEscapeTheBaseDirectory states the security property directly,
// not only as agreement with the original. Agreement would still hold if BOTH
// were wrong; this says what must be true regardless.
func TestSlugCannotEscapeTheBaseDirectory(t *testing.T) {
	base := BaseDir("/data")
	for _, label := range []string{
		"../../etc/passwd", "..", "../", "/absolute/path", `C:\Windows\System32`,
		"a/../../b", "./hidden", ".hidden", "nul\x00byte", "new\nline", "...", "",
		strings.Repeat("../", 40),
	} {
		dir := DirFor("/data", SlugFor(label))
		if !strings.HasPrefix(dir, base+string(filepath.Separator)) {
			t.Errorf("label %q produced %q, which is outside %q", label, dir, base)
		}
		if filepath.Clean(dir) != dir {
			t.Errorf("label %q produced an unclean path %q", label, dir)
		}
		// And never the base directory itself, which would mix one router's
		// pairs in with every other router's directory.
		if dir == base {
			t.Errorf("label %q resolved to the base directory itself", label)
		}
	}
}

func TestStemForAgainstLive(t *testing.T) {
	c := loadStoreCases(t)
	for _, s := range c.Stems {
		if got := StemFor(s.TS); got != s.Stem {
			t.Errorf("StemFor(%d) = %q, live %q", s.TS, got, s.Stem)
		}
	}
}

// TestStemsRoundTrip holds the writer and the reader together. Retention parses
// these stems; one this app writes but cannot read back would never be aged out,
// and — worse, per ToDo item 13 — would sort as a stray and take the
// never-remove-the-newest slot.
func TestStemsRoundTrip(t *testing.T) {
	c := loadStoreCases(t)
	for _, r := range c.RoundTrip {
		ms, ok := stemToMs(r.Stem)
		if !ok {
			t.Errorf("StemFor produced %q, which stemToMs cannot read — retention "+
				"would never age this pair out", r.Stem)
			continue
		}
		if ms != r.TS {
			t.Errorf("%q round-tripped to %d, want %d", r.Stem, ms, r.TS)
		}
	}
}

func TestPathHelpersAgainstLive(t *testing.T) {
	c := loadStoreCases(t)
	base := BaseDir("/data")
	for _, p := range c.Paths {
		dir := DirFor("/data", p.Slug)
		if rel, _ := filepath.Rel(base, dir); rel != p.Dir {
			t.Errorf("DirFor(%q) = %q, live %q", p.Slug, rel, p.Dir)
		}
		if rel, _ := filepath.Rel(base, RscPath(dir, "2026-08-19T203521")); rel != p.Rsc {
			t.Errorf("RscPath(%q) = %q, live %q", p.Slug, rel, p.Rsc)
		}
		if rel, _ := filepath.Rel(base, BackupPath(dir, "2026-08-19T203521")); rel != p.Backup {
			t.Errorf("BackupPath(%q) = %q, live %q", p.Slug, rel, p.Backup)
		}
	}
}
