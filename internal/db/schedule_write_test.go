package db

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeCases is the `writes` block: a row handed to the LIVE upsert, and what
// the live read query then returned for it.
//
// ROUND-TRIPPING IS NOT EVIDENCE on its own — a port can round-trip its own
// mistakes perfectly. What makes this a gate is that the expected value came
// from the other implementation.
type writeCases struct {
	Writes []struct {
		Name string `json:"name"`
		In   struct {
			ID             string   `json:"id"`
			RouterID       string   `json:"routerId"`
			Name           string   `json:"name"`
			Sections       []string `json:"sections"`
			Iface          *string  `json:"iface"`
			Aggregate      string   `json:"aggregate"`
			Recipients     []string `json:"recipients"`
			Frequency      string   `json:"frequency"`
			SendHour       int      `json:"sendHour"`
			Enabled        bool     `json:"enabled"`
			DisabledReason *string  `json:"disabledReason"`
			CreatedBy      *string  `json:"createdBy"`
			CreatedAt      int64    `json:"createdAt"`
			UpdatedAt      int64    `json:"updatedAt"`
		} `json:"in"`
		Stored json.RawMessage `json:"stored"`
	} `json:"writes"`
}

// TestScheduleWritesMatchLive replays each write and compares the stored row.
//
// The cases run IN ORDER and share one database, because the last of them is an
// UPDATE over the first — and what it proves is that `created_by` and
// `created_at` survive an edit that tries to change them. Isolating the cases
// would remove the only test of the ON CONFLICT column list.
func TestScheduleWritesMatchLive(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "report-history-cases.json"))
	if err != nil {
		t.Skipf("report-history-cases.json missing — regenerate it in the app container: %v", err)
	}
	var c writeCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parsing the cases: %v", err)
	}
	if len(c.Writes) == 0 {
		t.Fatal("no write cases — regenerate tools/report-history-cases.js")
	}

	var hist historyCases
	if err := json.Unmarshal(raw, &hist); err != nil {
		t.Fatalf("parsing the cases: %v", err)
	}
	d := seededDB(t, hist)

	for _, w := range c.Writes {
		in := w.In
		sections, _ := json.Marshal(in.Sections)
		recipients, _ := json.Marshal(in.Recipients)
		enabled := 0
		if in.Enabled {
			enabled = 1
		}
		row := ReportSchedule{
			ID: in.ID, RouterID: in.RouterID, Name: in.Name,
			Sections: string(sections), Interface: in.Iface, Aggregate: in.Aggregate,
			Recipients: string(recipients), Frequency: in.Frequency,
			SendHour: in.SendHour, Enabled: enabled,
			DisabledReason: in.DisabledReason, CreatedBy: in.CreatedBy,
			CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt,
		}
		if err := d.UpsertReportSchedule(row); err != nil {
			t.Fatalf("%s: %v", w.Name, err)
		}

		rows, err := d.ReportSchedulesFor(in.RouterID)
		if err != nil {
			t.Fatalf("%s: %v", w.Name, err)
		}
		var stored *ReportSchedule
		for i := range rows {
			if rows[i].ID == in.ID {
				stored = &rows[i]
			}
		}
		if stored == nil {
			t.Errorf("%s: the row was written and could not be read back", w.Name)
			continue
		}

		gotJSON, _ := json.Marshal(stored)
		var got, want any
		if err := json.Unmarshal(gotJSON, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(w.Stored, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n  go   %s\n  node %s", w.Name, gotJSON, w.Stored)
		}
	}
}

// TestScheduleToggleMatchesLive covers `SetReportScheduleEnabled`, which the
// upsert cases never reach.
//
// Its null handling is its own: enabling CLEARS the reason, disabling keeps it,
// and an EMPTY reason must be stored as NULL rather than ”. A mutation storing
// ” passed every other test in this package until these cases existed.
//
// `updated_at` is the live function's own `Date.now()`, so it is blanked on both
// sides — the alternative is a case file that fails on the second run.
func TestScheduleToggleMatchesLive(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "report-history-cases.json"))
	if err != nil {
		t.Skipf("report-history-cases.json missing: %v", err)
	}
	var c struct {
		Toggles []struct {
			Name    string          `json:"name"`
			Enabled bool            `json:"enabled"`
			Reason  *string         `json:"reason"`
			Stored  json.RawMessage `json:"stored"`
		} `json:"toggles"`
		Writes []struct {
			In struct {
				ID       string `json:"id"`
				RouterID string `json:"routerId"`
			} `json:"in"`
		} `json:"writes"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parsing the cases: %v", err)
	}
	if len(c.Toggles) == 0 {
		t.Fatal("no toggle cases — regenerate tools/report-history-cases.js")
	}
	var hist historyCases
	if err := json.Unmarshal(raw, &hist); err != nil {
		t.Fatal(err)
	}
	d := seededDB(t, hist)

	// The toggles run against the row the write cases leave behind, so those have
	// to be replayed first — same order, same database, as the generator did.
	var wc writeCases
	if err := json.Unmarshal(raw, &wc); err != nil {
		t.Fatal(err)
	}
	for _, w := range wc.Writes {
		in := w.In
		sections, _ := json.Marshal(in.Sections)
		recipients, _ := json.Marshal(in.Recipients)
		enabled := 0
		if in.Enabled {
			enabled = 1
		}
		if err := d.UpsertReportSchedule(ReportSchedule{
			ID: in.ID, RouterID: in.RouterID, Name: in.Name,
			Sections: string(sections), Interface: in.Iface, Aggregate: in.Aggregate,
			Recipients: string(recipients), Frequency: in.Frequency,
			SendHour: in.SendHour, Enabled: enabled,
			DisabledReason: in.DisabledReason, CreatedBy: in.CreatedBy,
			CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range c.Toggles {
		reason := ""
		if tc.Reason != nil {
			reason = *tc.Reason
		}
		if err := d.SetReportScheduleEnabled("w-full", tc.Enabled, reason, 0); err != nil {
			t.Fatalf("%s: %v", tc.Name, err)
		}
		rows, err := d.ReportSchedulesFor("router-a")
		if err != nil {
			t.Fatal(err)
		}
		var stored *ReportSchedule
		for i := range rows {
			if rows[i].ID == "w-full" {
				stored = &rows[i]
			}
		}
		if stored == nil {
			t.Fatalf("%s: w-full is missing", tc.Name)
		}
		// Blanked on both sides, as the generator does.
		stored.UpdatedAt = 0
		gotJSON, _ := json.Marshal(stored)
		var got, want any
		if err := json.Unmarshal(gotJSON, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(tc.Stored, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n  go   %s\n  node %s", tc.Name, gotJSON, tc.Stored)
		}
	}
}

// TestRemoveScheduleCascadesToRuns pins the foreign-key cascade.
//
// NOT a differential case: it is a property of the SCHEMA plus the connection's
// `foreign_keys` pragma, and SQLite defaults that pragma OFF per connection. With
// it off the cascade is parsed and then ignored, and the only symptom is run rows
// accumulating against schedules that no longer exist — which nothing on any page
// would ever show.
func TestRemoveScheduleCascadesToRuns(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "report-history-cases.json"))
	if err != nil {
		t.Skipf("report-history-cases.json missing: %v", err)
	}
	var hist historyCases
	if err := json.Unmarshal(raw, &hist); err != nil {
		t.Fatalf("parsing the cases: %v", err)
	}
	d := seededDB(t, hist)

	before, err := d.ReportRuns("sch-full", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("the seed has no runs for sch-full, so this proves nothing")
	}
	if err := d.RemoveReportSchedule("sch-full"); err != nil {
		t.Fatal(err)
	}
	after, err := d.ReportRuns("sch-full", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Errorf("%d run rows survived the schedule they belong to — is foreign_keys on?", len(after))
	}
}
