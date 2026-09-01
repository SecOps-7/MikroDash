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

// ── THE 32-BIT NARROWING, RUN RATHER THAN ARGUED ───────────────────────────
//
// CodeQL alert 157 (`go/incorrect-integer-conversion`, high) points at
// `return int(n)` in ClampInt and says the ParseInt result is narrowed "without
// an upper bound check". There IS one, immediately above it — but it compares
// against `math.MaxInt`, which on the 64-bit platform the analysis runs against
// is `MaxInt64`, so the guard is tautologically false there and the checker
// cannot see it as a bound.
//
// Whether that reasoning is right is not a thing to settle by reading. The
// property that matters is SATURATION RATHER THAN WRAPPING, and it only has
// teeth where `int` is 32 bits — which this project really ships, as
// linux/arm/v7. So run it there:
//
//	GOARCH=386 go test ./internal/reports/ -run ClampInt
//
// On a 64-bit build these assertions are nearly free, because the conversion is
// the identity. On a 32-bit one they are the whole question, and a plain
// `int(n)` fails them.
func TestClampIntSaturatesInsteadOfWrapping(t *testing.T) {
	for _, c := range []struct {
		why string
		in  int64
	}{
		{"the int64 ceiling, which LeadingInt saturates to", math.MaxInt64},
		{"the int64 floor", math.MinInt64},
		{"just past a 32-bit int", 4294967296},
		{"?limit=2147483648, one past MaxInt32", 2147483648},
		{"and one below MinInt32", -2147483649},
	} {
		got := ClampInt(c.in)
		// THE BUG THIS NAMES: a wrap does not merely lose magnitude, it flips
		// the sign — 4294967296 becomes 0 and 2147483648 becomes a large
		// negative. A negative Limit or Offset is a different query, not a
		// clamped one.
		if c.in > 0 && got <= 0 {
			t.Errorf("%s: ClampInt(%d) = %d — a positive input came back non-positive, "+
				"which is a wrap", c.why, c.in, got)
		}
		if c.in < 0 && got >= 0 {
			t.Errorf("%s: ClampInt(%d) = %d — a negative input came back non-negative",
				c.why, c.in, got)
		}
		// And saturation is exactly what it should saturate TO.
		if c.in > int64(math.MaxInt) && got != math.MaxInt {
			t.Errorf("%s: ClampInt(%d) = %d, want MaxInt %d", c.why, c.in, got, math.MaxInt)
		}
		if c.in < int64(math.MinInt) && got != math.MinInt {
			t.Errorf("%s: ClampInt(%d) = %d, want MinInt %d", c.why, c.in, got, math.MinInt)
		}
	}

	// Inside the range it is the identity, or the clamp would be a bug of its own.
	for _, n := range []int64{0, 1, 1000, -1000, 2147483647} {
		if got := ClampInt(n); int64(got) != n {
			t.Errorf("ClampInt(%d) = %d, want it unchanged", n, got)
		}
	}

	// The caller that made this worth pinning: an absurd ?capacity= must not
	// come back as a divisor of zero or a negative.
	if got := CapacityOr("99999999999999999999"); got < 1 {
		t.Errorf("CapacityOr(absurd) = %d, which is a divisor that produces "+
			"infinite or negative utilisation", got)
	}
}

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
