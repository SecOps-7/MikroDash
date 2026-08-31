package reports

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type periodCases struct {
	Constants struct {
		MaxAttempts  int      `json:"MAX_ATTEMPTS"`
		RetryAfterMs int64    `json:"RETRY_AFTER_MS"`
		Frequencies  []string `json:"FREQUENCIES"`
	} `json:"constants"`
	Cases []struct {
		TZ     string `json:"tz"`
		Now    int64  `json:"now"`
		Offset int64  `json:"offset"`
		Civil  struct {
			Year, Month, Day, Hour, Minute, Weekday int
		} `json:"civil"`
		// Periods is keyed by frequency. A null `period` is the unknown-frequency
		// case, which must stay unknown.
		Periods map[string]struct {
			Period *Period          `json:"period"`
			FireAt map[string]int64 `json:"fireAt"`
			Label  string           `json:"label"`
		} `json:"periods"`
	} `json:"cases"`
	Due []struct {
		Name     string   `json:"name"`
		TZ       string   `json:"tz"`
		Now      int64    `json:"now"`
		Schedule schedRow `json:"schedule"`
		History  History  `json:"history"`
		Window   *Period  `json:"window"`
	} `json:"due"`
}

// schedRow mirrors the DATABASE ROW shape rather than Schedule: `enabled` is an
// INTEGER in SQLite and arrives as 0 or 1, which is not a Go bool. Decoding it
// as one would fail the whole case file rather than a single field.
type schedRow struct {
	Enabled   int    `json:"enabled"`
	Frequency string `json:"frequency"`
	SendHour  int    `json:"send_hour"`
	CreatedAt int64  `json:"created_at"`
}

func (r schedRow) schedule() Schedule {
	return Schedule{
		Enabled:   r.Enabled != 0,
		Frequency: r.Frequency,
		SendHour:  r.SendHour,
		CreatedAt: r.CreatedAt,
	}
}

func load(t *testing.T) periodCases {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "report-period-cases.json"))
	if err != nil {
		t.Fatalf("reading the cases: %v", err)
	}
	var c periodCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parsing the cases: %v", err)
	}
	if len(c.Cases) == 0 || len(c.Due) == 0 {
		t.Fatal("no cases — regenerate with tools/report-period-cases.js")
	}
	return c
}

func TestConstantsMatch(t *testing.T) {
	c := load(t)
	if c.Constants.MaxAttempts != MaxAttempts {
		t.Errorf("MaxAttempts = %d, period.js says %d", MaxAttempts, c.Constants.MaxAttempts)
	}
	if c.Constants.RetryAfterMs != RetryAfterMs {
		t.Errorf("RetryAfterMs = %d, period.js says %d", RetryAfterMs, c.Constants.RetryAfterMs)
	}
	if len(c.Constants.Frequencies) != len(Frequencies) {
		t.Fatalf("Frequencies = %v, period.js says %v", Frequencies, c.Constants.Frequencies)
	}
	for i, f := range c.Constants.Frequencies {
		if Frequencies[i] != f {
			t.Errorf("Frequencies[%d] = %q, period.js says %q", i, Frequencies[i], f)
		}
	}
}

// TestPeriodMatchesLive replays every instant in every zone.
//
// The instants are chosen around real DST transitions — see the generator — so a
// failure here names the zone and the moment, rather than leaving someone with
// "the dates are off by an hour sometimes".
func TestPeriodMatchesLive(t *testing.T) {
	c := load(t)
	for _, row := range c.Cases {
		if got := OffsetAt(row.Now, row.TZ); got != row.Offset {
			t.Errorf("OffsetAt(%d, %q) = %d, period.js says %d", row.Now, row.TZ, got, row.Offset)
		}
		gc := CivilAt(row.Now, row.TZ)
		if gc.Year != row.Civil.Year || gc.Month != row.Civil.Month || gc.Day != row.Civil.Day ||
			gc.Hour != row.Civil.Hour || gc.Minute != row.Civil.Minute || gc.Weekday != row.Civil.Weekday {
			t.Errorf("CivilAt(%d, %q) = %+v, period.js says %+v", row.Now, row.TZ, gc, row.Civil)
		}

		for freq, want := range row.Periods {
			got, ok := PeriodFor(freq, row.Now, row.TZ)
			if (want.Period == nil) == ok {
				t.Errorf("PeriodFor(%q, %d, %q): ok=%v, period.js returned %v",
					freq, row.Now, row.TZ, ok, want.Period)
				continue
			}
			if want.Period == nil {
				continue
			}
			if got != *want.Period {
				t.Errorf("PeriodFor(%q, %d, %q) = %+v, period.js says %+v",
					freq, row.Now, row.TZ, got, *want.Period)
			}
			for hs, wantFire := range want.FireAt {
				var h int
				if _, err := fmt.Sscanf(hs, "%d", &h); err != nil {
					t.Fatalf("bad sendHour key %q", hs)
				}
				if gotFire := FireAt(got, h, row.TZ); gotFire != wantFire {
					t.Errorf("FireAt(%+v, %d, %q) = %d, period.js says %d",
						got, h, row.TZ, gotFire, wantFire)
				}
			}
			if gotLabel := Label(freq, got, row.TZ); gotLabel != want.Label {
				t.Errorf("Label(%q, %+v, %q) = %q, period.js says %q",
					freq, got, row.TZ, gotLabel, want.Label)
			}
		}
	}
}

// TestDueWindowMatchesLive covers the state half: the retry rules, where a wrong
// answer costs a whole period's report rather than an hour.
func TestDueWindowMatchesLive(t *testing.T) {
	c := load(t)
	for _, row := range c.Due {
		got, ok := DueWindow(row.Schedule.schedule(), row.History, row.Now, row.TZ)
		if (row.Window == nil) == ok {
			t.Errorf("%s [%s]: due=%v, period.js returned %v", row.Name, row.TZ, ok, row.Window)
			continue
		}
		if row.Window != nil && got != *row.Window {
			t.Errorf("%s [%s]: window %+v, period.js says %+v", row.Name, row.TZ, got, *row.Window)
		}
	}
}
