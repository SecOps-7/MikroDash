package collect

// The update verdict orders versions, and BOTH pages ask the same function.
//
// This exists because of a defect seen on real hardware on 2026-09-16: an
// hAP ax³ running RouterOS 7.24.3 reported `latest-version: 7.24.2`, and the
// dashboard's system card and the Packages page both offered an Update button
// for it. Pressing it would have downgraded the router.
//
// `testdata/system-update-cases.json` recorded the live app's rule as "it is
// inequality, not ordering". That recording is what the app DID, not what it
// must do, and the case was changed deliberately; the corpus carries a
// `divergences` entry saying so.

import (
	"testing"

	"mikrodash/internal/routeros"
)

func TestAnOlderLatestVersionIsNotAnUpdate(t *testing.T) {
	for _, c := range []struct {
		name                      string
		latest, status, installed string
		want                      bool
	}{
		// The measured case, which is the whole reason this test exists.
		{"the hAP ax³ reading: a stable channel that went backwards",
			"7.24.2", "", "7.24.3", false},
		{"a genuinely newer patch", "7.24.4", "", "7.24.3", true},
		{"a newer minor", "7.25", "", "7.24.3", true},
		{"an older minor", "7.23", "", "7.24", false},
		{"an older major", "6.49.10", "", "7.24.3", false},
		{"the same version", "7.24.3", "", "7.24.3", false},

		// A missing component is zero, so these are the SAME version and must
		// not report an update for ever.
		{"7.24 against 7.24.0", "7.24", "", "7.24.0", false},
		{"7.24.0 against 7.24", "7.24.0", "", "7.24", false},

		// Not plain dotted numbers: the old inequality rule still applies,
		// rather than an invented ordering of rc against beta.
		{"a development build that differs", "7.25rc3", "", "7.24.3", true},
		{"a development build that matches", "7.25rc3", "", "7.25rc3", false},
		{"an unparseable installed version", "7.24.3", "", "unknown", true},

		// No `latest-version` at all: RouterOS's own status wording decides,
		// and ordering never enters into it.
		{"no version, status says a new one exists",
			"", "New version is available", "7.24.3", true},
		{"no version, status in mixed case",
			"", "NEW VERSION IS AVAILABLE", "7.24.3", true},
		{"no version, status says up to date",
			"", "System is already up to date", "7.24.3", false},
		{"nothing at all", "", "", "7.24.3", false},
	} {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if got := UpdateVerdict(c.latest, c.status, c.installed); got != c.want {
				t.Errorf("UpdateVerdict(%q, %q, %q) = %v, want %v",
					c.latest, c.status, c.installed, got, c.want)
			}
		})
	}
}

func TestRosVersionCmpOnlyOrdersWhatItRecognises(t *testing.T) {
	for _, c := range []struct {
		a, b   string
		want   int
		wantOK bool
	}{
		{"7.24.3", "7.24.2", 1, true},
		{"7.24.2", "7.24.3", -1, true},
		{"7.24.3", "7.24.3", 0, true},
		{"7.24", "7.24.0", 0, true},
		{"7.9", "7.10", -1, true},  // not a string comparison: "7.9" > "7.10" lexically
		{"7.100", "7.99", 1, true}, // nor a decimal one
		{"8", "7.24.3", 1, true},
		{"7.25rc3", "7.24.3", 0, false},
		{"7.24.3", "", 0, false},
		{"", "", 0, false},
		{"7..3", "7.24.3", 0, false},
		{"7.24.3 (stable)", "7.24.3", 0, false},
		{" 7.24.3", "7.24.3", 0, true}, // trimmed, so this one IS recognised
		{"-1.2", "7.24.3", 0, false},
	} {
		c := c
		t.Run(c.a+" vs "+c.b, func(t *testing.T) {
			got, ok := rosVersionCmp(c.a, c.b)
			if ok != c.wantOK {
				t.Fatalf("rosVersionCmp(%q, %q) ok = %v, want %v", c.a, c.b, ok, c.wantOK)
			}
			if ok && got != c.want {
				t.Errorf("rosVersionCmp(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
			}
		})
	}
}

// ONE RULE, TWO PAGES. `parseUpdate` fills the Packages page and `UpdateVerdict`
// fills the dashboard's system card, and they disagreed for as long as one was a
// copy of the other. This pins that they are now the same answer, including for
// the row that caused the defect.
func TestBothPagesReadAnUpdateRowTheSameWay(t *testing.T) {
	for _, row := range []routeros.Reply{
		{"channel": "stable", "installed-version": "7.24.3", "latest-version": "7.24.2"},
		{"channel": "stable", "installed-version": "7.24.3", "latest-version": "7.24.4",
			"status": "New version is available"},
		{"channel": "stable", "installed-version": "7.24.3 (stable)", "latest-version": "7.24.3"},
		{"channel": "testing", "installed-version": "7.24.3", "latest-version": "7.25rc3"},
		{"channel": "stable", "installed-version": "7.24.3", "status": "New version is available"},
		{"channel": "stable", "installed-version": "7.24.3", "status": "finding out latest version..."},
	} {
		row := row
		t.Run(row["installed-version"]+" -> "+row["latest-version"], func(t *testing.T) {
			u := parseUpdate(row)
			want := UpdateVerdict(row["latest-version"], row["status"], u.InstalledVersion)
			if u.UpdateAvailable != want {
				t.Errorf("Packages says %v, the system card says %v (row %v)",
					u.UpdateAvailable, want, row)
			}
		})
	}
}

// The measured row, end to end through the Packages parser.
func TestTheMeasuredDowngradeOffersNoUpdate(t *testing.T) {
	u := parseUpdate(routeros.Reply{
		"channel": "stable", "installed-version": "7.24.3 (stable)",
		"latest-version": "7.24.2", "status": "",
	})
	if u.InstalledVersion != "7.24.3" {
		t.Errorf("installed base = %q, want 7.24.3", u.InstalledVersion)
	}
	if u.UpdateAvailable {
		t.Error("7.24.3 was offered an 'update' to 7.24.2 — that is a downgrade")
	}
}
