package reports

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
)

type formatCorpus struct {
	FmtDataMB []struct {
		In     any    `json:"in"`
		InKind string `json:"inKind"`
		Out    string `json:"out"`
	} `json:"fmtDataMB"`
	BucketNoun []struct {
		In  *string `json:"in"`
		Out string  `json:"out"`
	} `json:"bucketNoun"`
	MaxOf []struct {
		In  []float64 `json:"in"`
		Out any       `json:"out"`
	} `json:"maxOf"`
	ToFixed []struct {
		V   float64 `json:"v"`
		D   int     `json:"d"`
		Out string  `json:"out"`
	} `json:"toFixed"`
}

func loadFormatCorpus(t *testing.T) formatCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/report-format-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c formatCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.ToFixed) == 0 || len(c.FmtDataMB) == 0 {
		t.Fatal("corpus is empty -- these tests would pass against nothing")
	}
	return c
}

// TestToFixedMatchesJavaScript is the one that matters most: three other
// functions in this package are built on it, and Go's own %.*f is wrong here.
func TestToFixedMatchesJavaScript(t *testing.T) {
	c := loadFormatCorpus(t)
	agreed := 0
	for _, tc := range c.ToFixed {
		if got := ToFixed(tc.V, tc.D); got != tc.Out {
			t.Errorf("ToFixed(%v, %d) = %q, JS says %q", tc.V, tc.D, got, tc.Out)
		}
		// How often Go's own formatter would have been right, so the size of the
		// difference is on the record rather than assumed.
		if sprintf(tc.V, tc.D) == tc.Out {
			agreed++
		}
	}
	t.Logf("Go's own float formatter agreed with JS on %d of %d cases", agreed, len(c.ToFixed))
	if agreed == len(c.ToFixed) {
		t.Error("Go's own float formatter agreed on EVERY case -- the corpus contains no " +
			"half, so it cannot show that ToFixed is needed at all")
	}
}

// TestFmtDataMBMatchesLive covers the branches and, more importantly, the
// coercion: the live function takes whatever the caller had, and a JSON number
// is only one of the things that reaches it.
func TestFmtDataMBMatchesLive(t *testing.T) {
	c := loadFormatCorpus(t)
	checked := 0
	for _, tc := range c.FmtDataMB {
		// Only the numeric cases are the Go function's problem. The port's callers
		// hand it a float64 -- the coercions JS performs on null, '', true or an
		// array happen at the boundary of a dynamically typed language and have no
		// counterpart here. Recorded in the corpus so the behaviour is visible, and
		// asserted for the ONE that does survive typing: NaN.
		f, ok := tc.In.(float64)
		if !ok {
			continue
		}
		checked++
		if got := FmtDataMB(f); got != tc.Out {
			t.Errorf("FmtDataMB(%v) = %q, live says %q", f, got, tc.Out)
		}
	}
	if checked < 20 {
		t.Fatalf("only %d numeric cases -- the corpus is not covering the branches", checked)
	}
	if got := FmtDataMB(math.NaN()); got != "0 KB" {
		t.Errorf("FmtDataMB(NaN) = %q, and `+mb || 0` makes it %q", got, "0 KB")
	}
}

func TestBucketNounMatchesLive(t *testing.T) {
	c := loadFormatCorpus(t)
	for _, tc := range c.BucketNoun {
		in := ""
		if tc.In != nil {
			in = *tc.In
		}
		if got := BucketNoun(in); got != tc.Out {
			t.Errorf("BucketNoun(%q) = %q, live says %q", in, got, tc.Out)
		}
	}
}

func TestMaxOfMatchesLive(t *testing.T) {
	c := loadFormatCorpus(t)
	sawEmpty := false
	for _, tc := range c.MaxOf {
		got := MaxOf(tc.In)
		switch want := tc.Out.(type) {
		case string: // "-Infinity"
			sawEmpty = true
			if !math.IsInf(got, -1) {
				t.Errorf("MaxOf(%v) = %v, live says %s", tc.In, got, want)
			}
		case float64:
			if got != want {
				t.Errorf("MaxOf(%v) = %v, live says %v", tc.In, got, want)
			}
		}
	}
	if !sawEmpty {
		t.Error("no case pins MaxOf's empty-slice result, which is -Infinity and not 0")
	}
}

func sprintf(v float64, d int) string {
	return fmt.Sprintf("%.*f", d, v)
}
