package backups

import (
	"encoding/json"
	"os"
	"testing"
)

// The differential gate for normalize / fingerprint / diff.
//
// Cases come from `tools/backup-diff-cases.js`, which RUNS the live module.
// The FINGERPRINT half matters most: it is an interoperability contract with the
// archive already on disk, so a hash differing by one byte of normalisation
// makes every existing backup read as drift on the first run after cutover.

type normCase struct {
	Name        string   `json:"name"`
	Text        *string  `json:"text"`
	Lines       []string `json:"lines"`
	Normalized  string   `json:"normalized"`
	Fingerprint string   `json:"fingerprint"`
}

type diffCase struct {
	Name    string     `json:"name"`
	OldText string     `json:"oldText"`
	NewText string     `json:"newText"`
	Want    DiffResult `json:"want"`
}

func loadDiffCases(t *testing.T) (int, int, []normCase, []diffCase) {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/backup-diff-cases.json")
	if err != nil {
		t.Fatalf("case file missing — run tools/backup-diff-cases.js: %v", err)
	}
	var f struct {
		Context   int        `json:"context"`
		MaxEdits  int        `json:"maxEdits"`
		Normalize []normCase `json:"normalizeCases"`
		Diffs     []diffCase `json:"diffCases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f.Context, f.MaxEdits, f.Normalize, f.Diffs
}

func TestNormalizeAndFingerprintAgainstLive(t *testing.T) {
	ctx, me, norm, _ := loadDiffCases(t)
	if ctx != context || me != maxEdits {
		t.Fatalf("constants: live context=%d maxEdits=%d, port %d/%d", ctx, me, context, maxEdits)
	}
	if len(norm) == 0 {
		t.Fatal("no normalize cases")
	}

	for _, c := range norm {
		// A JSON null is JavaScript's `null`, which `String(text == null ? '' : text)`
		// turns into "". Go has no such value, so the empty string is the port's
		// equivalent and the case exists to say the two agree.
		in := ""
		if c.Text != nil {
			in = *c.Text
		}
		if got := Normalize(in); got != c.Normalized {
			t.Errorf("%s: Normalize\n    got  %q\n    live %q", c.Name, got, c.Normalized)
		}
		if got := Fingerprint(in); got != c.Fingerprint {
			t.Errorf("%s: Fingerprint\n    got  %s\n    live %s\n"+
				"    a hash that differs makes EVERY archived backup read as drift "+
				"on the first run after cutover", c.Name, got, c.Fingerprint)
		}
		gotLines := NormalizeLines(in)
		if len(gotLines) != len(c.Lines) {
			t.Errorf("%s: NormalizeLines got %d lines, live %d (%q vs %q)",
				c.Name, len(gotLines), len(c.Lines), gotLines, c.Lines)
			continue
		}
		for i := range gotLines {
			if gotLines[i] != c.Lines[i] {
				t.Errorf("%s: line %d = %q, live %q", c.Name, i, gotLines[i], c.Lines[i])
			}
		}
	}
}

func TestDiffAgainstLive(t *testing.T) {
	_, _, _, cases := loadDiffCases(t)
	if len(cases) == 0 {
		t.Fatal("no diff cases")
	}

	// A corpus with no hunks and no truncation would pass against a stub.
	hunks, truncated := 0, 0
	for _, c := range cases {
		hunks += len(c.Want.Hunks)
		if c.Want.Truncated {
			truncated++
		}
	}
	if hunks == 0 || truncated == 0 {
		t.Fatalf("corpus exercises too little: %d hunks, %d truncated", hunks, truncated)
	}

	for _, c := range cases {
		got := Diff(c.OldText, c.NewText)
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(c.Want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("%s\n    got  %s\n    live %s", c.Name, gotJSON, wantJSON)
		}
	}
	t.Logf("%d diff cases, %d hunks, %d truncated", len(cases), hunks, truncated)
}

// TestVolatileHeaderIsWhyDriftIsNotReportedDaily states the one rule an operator
// actually feels, separately from the corpus.
func TestVolatileHeaderIsWhyDriftIsNotReportedDaily(t *testing.T) {
	body := "\n# software id = HR2S-3YN6\n/ip dns set servers=1.1.1.1"
	a := "# 2026-08-19 20:35:21 by RouterOS 7.24" + body
	b := "# 2026-08-20 21:00:00 by RouterOS 7.25" + body

	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("two runs of an unchanged configuration hashed differently — " +
			"every backup would report as drifted, which is how a drift tool gets ignored")
	}
	if Diff(a, b).Changed {
		t.Error("the volatile header alone was reported as a change")
	}
	// But the STABLE identity lines must still count: a different device is not
	// drift-free continuity.
	c := "# 2026-08-19 20:35:21 by RouterOS 7.24\n\n# software id = OTHER-ID\n/ip dns set servers=1.1.1.1"
	if Fingerprint(a) == Fingerprint(c) {
		t.Error("a changed software id hashed the same; that is a different device")
	}
}
