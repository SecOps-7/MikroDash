package reports

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type helperCases struct {
	Helpers struct {
		ParseInts []struct {
			In string `json:"in"`
			// Out is a STRING because one case is 1e20, which no int64 holds. See
			// the generator, and wantInt below for what this side does with it.
			Out string `json:"out"`
		} `json:"parseInts"`
		Aggregates []struct {
			In  string `json:"in"`
			Out string `json:"out"`
		} `json:"aggregates"`
		Downtime []struct {
			In  []ConnRow `json:"in"`
			Out []ConnRow `json:"out"`
		} `json:"downtime"`
		Capacities []struct {
			In  string `json:"in"`
			Out int    `json:"out"`
		} `json:"capacities"`
		Utilisation []struct {
			V   *float64 `json:"v"`
			Cap int      `json:"cap"`
			Out *float64 `json:"out"`
		} `json:"utilisation"`
	} `json:"helpers"`
}

func loadHelpers(t *testing.T) helperCases {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "report-period-cases.json"))
	if err != nil {
		t.Fatalf("reading the cases: %v", err)
	}
	var c helperCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parsing the cases: %v", err)
	}
	if len(c.Helpers.ParseInts) == 0 {
		t.Fatal("no helper cases — regenerate with tools/report-period-cases.js")
	}
	return c
}

// TestParseParamsMatchesLive covers the lenient integer parse and the aggregate
// allow-list — the two places a Go-idiomatic implementation would silently be
// stricter than the app it replaces.
func TestParseParamsMatchesLive(t *testing.T) {
	c := loadHelpers(t)
	now := time.UnixMilli(1767225600000)

	for _, tc := range c.Helpers.ParseInts {
		want := wantInt(t, tc.Out)
		// `from` takes the raw value: its fallback is 0, so it shows the parse.
		if got := ParseParams("r", tc.In, "1", "", now).From; got != want {
			t.Errorf("parseInt(%q) = %d, JavaScript says %s", tc.In, got, tc.Out)
		}
		// `to` shows the OTHER half of the contract: a parsed zero falls back to
		// now, exactly as an unparseable value does.
		wantTo := want
		if wantTo == 0 {
			wantTo = now.UnixMilli()
		}
		if got := ParseParams("r", "0", tc.In, "", now).To; got != wantTo {
			t.Errorf("to=%q resolved to %d, want %d", tc.In, got, wantTo)
		}
	}

	for _, tc := range c.Helpers.Aggregates {
		if got := ParseParams("r", "0", "1", tc.In, now).Aggregate; got != tc.Out {
			t.Errorf("aggregate=%q = %q, index.js says %q", tc.In, got, tc.Out)
		}
	}
}

// wantInt turns the recorded decimal string into what this port must answer.
//
// A value JavaScript held as a float beyond int64 SATURATES here rather than
// matching exactly — see LeadingInt. That is the one place these two do not
// agree numerically, and they still agree on what the query returns: a lower
// bound of 1e20 matches no samples either way.
func wantInt(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		return n
	}
	if strings.HasPrefix(s, "-") {
		return math.MinInt64
	}
	return math.MaxInt64
}

func TestAnnotateDowntimeMatchesLive(t *testing.T) {
	c := loadHelpers(t)
	for i, tc := range c.Helpers.Downtime {
		got := AnnotateDowntime(append([]ConnRow(nil), tc.In...))
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(tc.Out)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("run %d:\n  go   %s\n  node %s", i, gotJSON, wantJSON)
		}
	}
}

func TestCapacityAndUtilisationMatchLive(t *testing.T) {
	c := loadHelpers(t)
	for _, tc := range c.Helpers.Capacities {
		if got := CapacityOr(tc.In); got != tc.Out {
			t.Errorf("CapacityOr(%q) = %d, build.js says %d", tc.In, got, tc.Out)
		}
	}
	for _, tc := range c.Helpers.Utilisation {
		got := UtilPct(tc.V, tc.Cap)
		switch {
		case got == nil && tc.Out == nil:
		case got == nil || tc.Out == nil:
			t.Errorf("UtilPct(%v, %d) = %v, build.js says %v", tc.V, tc.Cap, got, tc.Out)
		case *got != *tc.Out:
			t.Errorf("UtilPct(%v, %d) = %v, build.js says %v", tc.V, tc.Cap, *got, *tc.Out)
		}
	}
}

// TestLabelForMatchesLive reads the CONTAINER-generated file, because alerter.js
// requires better-sqlite3. It skips rather than fails when that file is absent:
// this is the one gate here that cannot be regenerated on the host, and a hard
// failure would make a clean checkout look broken.
func TestLabelForMatchesLive(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "report-history-cases.json"))
	if err != nil {
		t.Skipf("report-history-cases.json missing — regenerate it in the app container: %v", err)
	}
	var c struct {
		Labels []struct {
			In  string `json:"in"`
			Out string `json:"out"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parsing the cases: %v", err)
	}
	if len(c.Labels) == 0 {
		t.Fatal("no label cases — regenerate tools/report-history-cases.js")
	}
	for _, tc := range c.Labels {
		if got := LabelFor(tc.In); got != tc.Out {
			t.Errorf("LabelFor(%q) = %q, alerter.js says %q", tc.In, got, tc.Out)
		}
	}
}
