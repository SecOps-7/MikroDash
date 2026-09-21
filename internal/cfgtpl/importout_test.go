package cfgtpl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureDir holds what cmd/importprobe recorded from a real router.
var fixtureDir = filepath.Join("..", "..", "testdata", "fixtures", "import")

type probeStep struct {
	Label string            `json:"label"`
	Path  string            `json:"path"`
	Args  []string          `json:"args"`
	Done  map[string]string `json:"done"`
	Trap  string            `json:"trap"`
	Err   string            `json:"err"`
}

type probeFile struct {
	Findings []struct {
		Name   string `json:"name"`
		Answer string `json:"answer"`
	} `json:"findings"`
	Steps []probeStep `json:"steps"`
}

func loadProbe(t *testing.T, name string) probeFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("reading the recorded replies: %v", err)
	}
	var p probeFile
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	return p
}

// Every /import reply recorded from a real router gets the verdict here. A
// new recording (a new RouterOS version) with a step not listed fails, so the
// parser is re-checked against what the router says rather than left trusted.
func TestEveryImportFixtureIsClassified(t *testing.T) {
	want := map[string]ImportOutcome{
		"m1 dry-run =dry-run=":        {Kind: "ok", Applied: "none"},
		"m1 dry-run =dry-run=yes":     {Kind: "ok", Applied: "none"},
		"m2 import dry-run":           {Kind: "ok", Applied: "none"},
		"m4 dry-run":                  {Kind: "ok", Applied: "none"},
		"m5 import 600 lines":         {Kind: "ok", Applied: "all"},
		"m2 import verbose=no":        {Kind: "runtime", Applied: "partial", Line: 5, Command: "/interface/list/member/add (list)"},
		"m2 import verbose=yes":       {Kind: "runtime", Applied: "unknown"},
		"m2 syntax dry-run":           {Kind: "syntax", Applied: "none", Errors: 1},
		"m2 syntax verbose=no":        {Kind: "syntax", Applied: "none", Line: 7, Column: 1},
		"m2 missing file":             {Kind: "missing-file", Applied: "none"},
		"m6 import with 80ms timeout": {Kind: "timeout", Applied: "unknown"},
	}
	p := loadProbe(t, "probe-7.24.4.json")
	seen := 0
	for _, s := range p.Steps {
		if s.Path != "/import" {
			continue
		}
		w, ok := want[s.Label]
		if !ok {
			t.Errorf("recorded reply %q has no expected verdict here — classify it before trusting the parser with it", s.Label)
			continue
		}
		seen++
		dry := false
		for _, a := range s.Args {
			if strings.Contains(a, "dry-run") {
				dry = true
			}
		}
		got := ParseImport(dry, s.Done, s.Trap, s.Err)
		if got.Kind != w.Kind || got.Applied != w.Applied || got.Line != w.Line ||
			got.Column != w.Column || got.Errors != w.Errors || (w.Command != "" && got.Command != w.Command) {
			t.Errorf("%s: got %+v, want %+v", s.Label, got, w)
		}
	}
	if seen != len(want) {
		t.Errorf("classified %d recorded replies of %d expected — a recording was lost", seen, len(want))
	}
}

// NOTHING UNRECOGNISED IS SUCCESS. A reply this code does not know is a
// failure whose effect on the router is unknown — never "fine", and never
// "nothing happened".
func TestAnUnrecognisedReplyIsAFailure(t *testing.T) {
	cases := []struct {
		name  string
		dry   bool
		done  map[string]string
		trap  string
		errTx string
	}{
		{"quiet real run, no marker", false, map[string]string{}, "", ""},
		{"quiet dry-run, no marker", true, nil, "", ""},
		{"a marker that is not the one", false, map[string]string{"ret": "false"}, "", ""},
		{"an unknown trap", false, nil, "something RouterOS has never said", ""},
		{"a dropped connection", false, nil, "", "EOF"},
	}
	for _, c := range cases {
		got := ParseImport(c.dry, c.done, c.trap, c.errTx)
		if got.OK() {
			t.Errorf("%s: read as success", c.name)
		}
		if got.Applied != "unknown" {
			t.Errorf("%s: claims the router holds %q; it cannot know", c.name, got.Applied)
		}
	}
}

// The captured dry-run report (the only way to its words) is marked line by
// line, from the recording.
func TestTheCapturedReportIsMarked(t *testing.T) {
	p := loadProbe(t, "probe-7.24.4.json")
	var raw string
	for _, f := range p.Findings {
		if f.Name == "m1b captured output" {
			raw = f.Answer
		}
	}
	i := strings.Index(raw, `contents="`)
	if i < 0 {
		t.Fatal("the recording holds no captured report")
	}
	// The finding quotes the file with Go's %q; undo that.
	var text string
	if err := json.Unmarshal([]byte(raw[i+len(`contents=`):]), &text); err != nil {
		t.Fatalf("reading the quoted report: %v", err)
	}
	lines := ParseReport(text)
	kinds := map[string]int{}
	var errLine int
	for _, l := range lines {
		kinds[l.Kind]++
		if l.Kind == "error" {
			errLine = l.Line
		}
	}
	if kinds["marker"] == 0 || kinds["source"] == 0 || kinds["summary"] != 1 || kinds["error"] != 1 {
		t.Errorf("report marked as %v; want markers, source, one error and one summary", kinds)
	}
	if errLine != 7 {
		t.Errorf("the error was placed on line %d, want 7", errLine)
	}
}

// After a reset, /log is the only place the script's failure appears, and it
// names the line it stopped at — measured with the lab CHR.
func TestResetFailureIsReadFromTheLog(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(fixtureDir, "export-reset-7.24.4.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Findings []struct{ Name, Answer string } `json:"findings"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	var logs []string
	for _, f := range r.Findings {
		if f.Name == "m8 log" {
			logs = append(logs, f.Answer)
		}
	}
	got, failed := ParseResetLog(logs)
	if !failed {
		t.Fatal("the recorded run-after-reset failure was not found in the log")
	}
	if got.Line != 2 || got.Command != "/ip/dhcp-client/add" || got.Applied != "partial" {
		t.Errorf("read as %+v; want line 2, /ip/dhcp-client/add, partial", got)
	}
	if _, failed := ParseResetLog([]string{"system,info: router rebooted"}); failed {
		t.Error("a clean log was read as a failed script")
	}
}
