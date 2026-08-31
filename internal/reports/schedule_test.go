package reports

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// scheduleCases is the `schedule` block of the container-generated case file.
//
// Each entry records whether the LIVE validator accepted the input and what it
// produced — or the exact message it refused with. Both halves matter: a port
// that accepts something the original refuses is the dangerous direction here,
// because the thing being refused is a mail-header injection.
type scheduleCases struct {
	Schedule struct {
		Recipients []struct {
			In    []string `json:"in"`
			OK    bool     `json:"ok"`
			Out   []string `json:"out"`
			Error string   `json:"error"`
		} `json:"recipients"`
		Names []struct {
			In    string `json:"in"`
			OK    bool   `json:"ok"`
			Out   string `json:"out"`
			Error string `json:"error"`
		} `json:"names"`
		Sections []struct {
			In    []string `json:"in"`
			OK    bool     `json:"ok"`
			Out   []string `json:"out"`
			Error string   `json:"error"`
		} `json:"sections"`
		AggregateFor []struct {
			Frequency string `json:"frequency"`
			Aggregate string `json:"aggregate"`
			Out       string `json:"out"`
		} `json:"aggregateFor"`
		Limits struct {
			MaxRecipients int `json:"MAX_RECIPIENTS"`
			MaxAddress    int `json:"MAX_ADDRESS"`
			MaxName       int `json:"MAX_NAME"`
		} `json:"limits"`
	} `json:"schedule"`
}

func loadSchedule(t *testing.T) scheduleCases {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "report-history-cases.json"))
	if err != nil {
		t.Skipf("report-history-cases.json missing — regenerate it in the app container: %v", err)
	}
	var c scheduleCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parsing the cases: %v", err)
	}
	if len(c.Schedule.Recipients) == 0 {
		t.Fatal("no schedule cases — regenerate tools/report-history-cases.js")
	}
	return c
}

func TestScheduleLimitsMatch(t *testing.T) {
	c := loadSchedule(t)
	if c.Schedule.Limits.MaxRecipients != MaxRecipients {
		t.Errorf("MaxRecipients = %d, schedules.js says %d", MaxRecipients, c.Schedule.Limits.MaxRecipients)
	}
	if c.Schedule.Limits.MaxAddress != MaxAddress {
		t.Errorf("MaxAddress = %d, schedules.js says %d", MaxAddress, c.Schedule.Limits.MaxAddress)
	}
	if c.Schedule.Limits.MaxName != MaxName {
		t.Errorf("MaxName = %d, schedules.js says %d", MaxName, c.Schedule.Limits.MaxName)
	}
}

// TestCleanRecipientsMatchesLive is the one that matters most in this file.
//
// ACCEPTING SOMETHING THE ORIGINAL REFUSES is the dangerous direction: the
// inputs it refuses are mail-header injections, and a port that let one through
// would turn a reporting tool into a relay. So a disagreement fails whichever way
// it points, and the refusal MESSAGE is compared too — the unsafe-character
// message deliberately does not echo the input, and a port that echoed it would
// put the injection attempt back onto the page.
func TestCleanRecipientsMatchesLive(t *testing.T) {
	c := loadSchedule(t)
	for _, tc := range c.Schedule.Recipients {
		out, err := CleanRecipients(tc.In)
		if tc.OK {
			if err != nil {
				t.Errorf("CleanRecipients(%q): refused with %q, schedules.js accepted it", tc.In, err)
				continue
			}
			if !reflect.DeepEqual(out, tc.Out) {
				t.Errorf("CleanRecipients(%q) = %q, schedules.js says %q", tc.In, out, tc.Out)
			}
			continue
		}
		if err == nil {
			t.Errorf("CleanRecipients(%q) = %q, schedules.js REFUSED it: %s", tc.In, out, tc.Error)
			continue
		}
		if err.Error() != tc.Error {
			t.Errorf("CleanRecipients(%q) refused with %q, schedules.js says %q",
				tc.In, err.Error(), tc.Error)
		}
	}
}

func TestCleanNameMatchesLive(t *testing.T) {
	c := loadSchedule(t)
	for _, tc := range c.Schedule.Names {
		out, err := CleanName(tc.In)
		if tc.OK {
			if err != nil {
				t.Errorf("CleanName(%q): refused with %q, schedules.js accepted it", tc.In, err)
				continue
			}
			if out != tc.Out {
				t.Errorf("CleanName(%q) = %q, schedules.js says %q", tc.In, out, tc.Out)
			}
			continue
		}
		if err == nil {
			t.Errorf("CleanName(%q) = %q, schedules.js REFUSED it: %s", tc.In, out, tc.Error)
			continue
		}
		if err.Error() != tc.Error {
			t.Errorf("CleanName(%q) refused with %q, schedules.js says %q", tc.In, err.Error(), tc.Error)
		}
	}
}

func TestCleanSectionsMatchesLive(t *testing.T) {
	c := loadSchedule(t)
	for _, tc := range c.Schedule.Sections {
		out, err := CleanSections(tc.In)
		if tc.OK {
			if err != nil {
				t.Errorf("CleanSections(%q): refused with %q, schedules.js accepted it", tc.In, err)
				continue
			}
			if !reflect.DeepEqual(out, tc.Out) {
				t.Errorf("CleanSections(%q) = %q, schedules.js says %q", tc.In, out, tc.Out)
			}
			continue
		}
		if err == nil {
			t.Errorf("CleanSections(%q) = %q, schedules.js REFUSED it: %s", tc.In, out, tc.Error)
		}
	}
}

func TestAggregateForMatchesLive(t *testing.T) {
	c := loadSchedule(t)
	for _, tc := range c.Schedule.AggregateFor {
		if got := AggregateFor(tc.Aggregate, tc.Frequency); got != tc.Out {
			t.Errorf("AggregateFor(%q, %q) = %q, schedules.js says %q",
				tc.Aggregate, tc.Frequency, got, tc.Out)
		}
	}
}
