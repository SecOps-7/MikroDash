package reports

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestToCSVMatchesLive replays what the live `toCsv` produced.
//
// The cases are weighted toward the formula triggers, because that is what this
// function defends against — and toward triggers COMBINED with a comma or a
// quote, because the order of the two escapes is the part that is easy to get
// backwards and impossible to see afterwards.
func TestToCSVMatchesLive(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "report-history-cases.json"))
	if err != nil {
		t.Skipf("report-history-cases.json missing — regenerate it in the app container: %v", err)
	}
	var c struct {
		CSV []struct {
			Name    string           `json:"name"`
			Columns []string         `json:"columns"`
			Rows    []map[string]any `json:"rows"`
			Out     string           `json:"out"`
		} `json:"csv"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parsing the cases: %v", err)
	}
	if len(c.CSV) == 0 {
		t.Fatal("no csv cases — regenerate tools/report-history-cases.js")
	}
	for _, tc := range c.CSV {
		if got := ToCSV(tc.Rows, tc.Columns); got != tc.Out {
			t.Errorf("%s:\n  go   %q\n  node %q", tc.Name, got, tc.Out)
		}
	}
}

// TestNegativeNumbersAreQuotePrefixed pins a consequence that looks like a bug
// and is not.
//
// `-5` starts with a trigger character, so it is exported as `'-5` and a
// spreadsheet shows it as TEXT rather than a number. That is the original's
// behaviour — the prefix rule tests the rendered string, not the type — and a
// port that exempted numbers would be quietly more useful and quietly different.
// Recorded here so the next person to notice it finds this rather than "fixing"
// it.
func TestNegativeNumbersAreQuotePrefixed(t *testing.T) {
	got := ToCSV([]map[string]any{{"a": float64(-5)}}, []string{"a"})
	if got != "a\n'-5" {
		t.Errorf("ToCSV(-5) = %q, want %q", got, "a\n'-5")
	}
}
