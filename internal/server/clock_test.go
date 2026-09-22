package server

import (
	"strings"
	"testing"

	"mikrodash/internal/aitools"
)

// TestClockTargetIsStrict. RouterOS WRAPS a time out of range rather than
// refusing it (measured on the CHR, 7.24.4: 25:61:00 became 02:01 the next day),
// so this parser is the only thing standing between a typo and a silent jump.
func TestClockTargetIsStrict(t *testing.T) {
	good := map[string][2]string{
		"2026-09-22 14:05:09":   {"2026-09-22", "14:05:09"},
		"2026-09-22T14:05:09":   {"2026-09-22", "14:05:09"},
		"  2026-09-22   14:05 ": {"2026-09-22", "14:05:00"},
		"2028-02-29 00:00:00":   {"2028-02-29", "00:00:00"},
	}
	for in, want := range good {
		d, c, problem := clockTarget(in)
		if problem != "" || d != want[0] || c != want[1] {
			t.Errorf("%q gave %q %q %q, want %v", in, d, c, problem, want)
		}
	}
	for _, in := range []string{
		"2026-09-22 25:61:00", // the one RouterOS would have wrapped
		"2026-09-22 24:00:00", "2026-13-01 10:00:00", "2026-02-30 10:00:00", "2027-02-29 10:00:00",
		"22/09/2026 10:00", "sep/22/2026 10:00:00", "2026-09-22", "10:00:00", "now", "",
		"2026-09-22 10:00:00 time=00:00:00", // nothing may ride along into the command
	} {
		if d, c, problem := clockTarget(in); problem == "" {
			t.Errorf("%q was accepted as %q %q", in, d, c)
		}
	}
}

// TestTheClockActionShowsWhatItWillSend: the dialog names the exact command, or
// says plainly that the value will be refused.
func TestTheClockActionShowsWhatItWillSend(t *testing.T) {
	spec, ok := aitools.ActionByKey("clock_set")
	if !ok {
		t.Fatal("clock_set is not in the catalogue")
	}
	if spec.Page != "clock" || spec.Target == "" || spec.TypedName {
		t.Errorf("clock_set is declared as %+v", spec)
	}
	if got := aiActionCommand(spec, "2026-09-22T14:05", ""); got != "/system/clock/set date=2026-09-22 time=14:05:00" {
		t.Errorf("the dialog shows %q", got)
	}
	if got := aiActionCommand(spec, "tomorrow", ""); !strings.Contains(got, "refused") {
		t.Errorf("an invalid value is shown as %q", got)
	}
	// A malformed value says why, in the action's own words.
	out := writeOutcome{Code: "bad-request", Detail: map[string]any{"message": "give the date and time as YYYY-MM-DD HH:MM:SS"}}
	if got := aiActionRefusal(spec, out); !strings.Contains(got, "YYYY-MM-DD") {
		t.Errorf("the refusal reads %q", got)
	}
}
