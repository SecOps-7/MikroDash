package reports

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type buildCorpus struct {
	Locale      string `json:"locale"`
	MaxPDFRows  int    `json:"maxPdfRows"`
	ChartPoints int    `json:"chartPoints"`
	Thin        []struct {
		N    int   `json:"n"`
		Kept []int `json:"kept"`
	} `json:"thin"`
	Cap []struct {
		Name      string         `json:"name"`
		N         int            `json:"n"`
		Columns   []string       `json:"columns"`
		Truncated bool           `json:"truncated"`
		Length    int            `json:"length"`
		Last      map[string]any `json:"last"`
		First     map[string]any `json:"first"`
	} `json:"cap"`
}

func loadBuildCorpus(t *testing.T) buildCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/report-build-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c buildCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Thin) == 0 || len(c.Cap) == 0 {
		t.Fatal("corpus is empty -- these tests would pass against nothing")
	}
	// The constants were LIFTED from the live source, so a change there shows up
	// here as a mismatch rather than as a port that quietly caps at the wrong
	// number.
	if c.MaxPDFRows != MaxPDFRows {
		t.Fatalf("the live MAX_PDF_ROWS is %d, this port has %d", c.MaxPDFRows, MaxPDFRows)
	}
	if c.ChartPoints != ChartPoints {
		t.Fatalf("the live CHART_POINTS is %d, this port has %d", c.ChartPoints, ChartPoints)
	}
	return c
}

func TestThinMatchesTheLiveHelper(t *testing.T) {
	c := loadBuildCorpus(t)
	for _, tc := range c.Thin {
		in := make([]int, tc.N)
		for i := range in {
			in[i] = i
		}
		got := Thin(in)
		want := tc.Kept
		if want == nil {
			want = []int{}
		}
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Thin(%d rows) kept %d points, live kept %d\n  got  %v…\n  live %v…",
				tc.N, len(got), len(want), head(got), head(want))
		}
	}
}

func TestCapRowsMatchesTheLiveHelper(t *testing.T) {
	c := loadBuildCorpus(t)
	for _, tc := range c.Cap {
		t.Run(tc.Name, func(t *testing.T) {
			rows := make([]map[string]any, tc.N)
			for i := range rows {
				r := make(map[string]any, len(tc.Columns))
				for j, col := range tc.Columns {
					r[col] = col + "-" + itoa(i) + "-" + itoa(j)
				}
				rows[i] = r
			}

			got, truncated := CapRows(rows, tc.Columns)
			if truncated != tc.Truncated {
				t.Errorf("truncated=%v, live says %v", truncated, tc.Truncated)
			}
			if len(got) != tc.Length {
				t.Fatalf("returned %d rows, live returned %d", len(got), tc.Length)
			}
			if len(got) == 0 {
				return
			}
			if !reflect.DeepEqual(got[0], tc.First) {
				t.Errorf("first row %v, live %v", got[0], tc.First)
			}
			// The note row is the whole point, and its exact text -- including the
			// grouped digits -- is what the live PDF prints.
			if !reflect.DeepEqual(got[len(got)-1], tc.Last) {
				t.Errorf("last row\n  got  %v\n  live %v", got[len(got)-1], tc.Last)
			}
		})
	}
}

// TestTheCorpusStillDescribesTheContainersLocale holds the locale assumption
// visible.
//
// `toLocaleString()` takes the RUNTIME's default locale, so the expected note
// text is a property of where the live app runs and not of its code. If the
// container's locale ever changes, this says so in one line rather than leaving
// six opaque string mismatches to be diagnosed.
func TestTheCorpusStillDescribesTheContainersLocale(t *testing.T) {
	if c := loadBuildCorpus(t); c.Locale != "en-US" {
		t.Fatalf("the corpus was generated under locale %q, not en-US -- groupDigits reproduces "+
			"en-US grouping and the expectations need rereading, not the code", c.Locale)
	}
}

func head(v []int) []int {
	if len(v) > 6 {
		return v[:6]
	}
	return v
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
