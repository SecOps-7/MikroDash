package collect

import (
	"testing"

	"mikrodash/internal/routeros"
)

// ── PHASE 4.1: THE COUNTS, TESTED WITHOUT A COLLECTOR ──────────────────────
//
// Totals, per-state counts and the pending-reboot flag were computed inline in
// `applyRows`, so the only way to test "a disabled package is not counted as
// installed" was to build a `*Packages` with a lock, a poll interval, an emit
// function and carried firmware and update state, then drive a tick.
//
// They are a function of the rows and nothing else.

func TestBuildPackagesCounts(t *testing.T) {
	rows := []routeros.Reply{
		{"name": "routeros", "version": "7.24.1", "disabled": "false"},
		{"name": "wireless", "version": "7.24.1", "disabled": "true"},
		{"name": "container", "version": "7.24.1", "disabled": "true"},
	}
	got, pkgs := BuildPackages(PackagesInput{Rows: rows, PollMs: 60000, Now: 7})

	if got.Counts.Total != 3 || len(pkgs) != 3 {
		t.Fatalf("total=%d parsed=%d, want 3 and 3", got.Counts.Total, len(pkgs))
	}
	// THE STATES MUST NOT BLEED. A disabled package counted as installed is the
	// kind of arithmetic that looks right on a router where every package is
	// enabled, which is most of them.
	if got.Counts.Installed != 1 {
		t.Errorf("installed=%d, want 1 — a disabled package was counted as installed",
			got.Counts.Installed)
	}
	if got.Counts.Disabled != 2 {
		t.Errorf("disabled=%d, want 2", got.Counts.Disabled)
	}
	if got.PendingReboot {
		t.Error("PendingReboot with nothing scheduled")
	}
	if got.TS != 7 || got.PollMs != 60000 {
		t.Errorf("ts=%d pollMs=%d; the builder invented a clock or an interval",
			got.TS, got.PollMs)
	}
}

// TestPendingRebootAgreesWithTheScheduledCount. The flag and the count are two
// readings of one fact, and deriving them separately is how they disagree — a
// banner saying a reboot is pending beside a count of zero.
func TestPendingRebootAgreesWithTheScheduledCount(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scheduled string
		want      bool
	}{
		{"nothing scheduled", "", false},
		{"an upgrade scheduled", "upgrade", true},
		{"a downgrade scheduled", "downgrade", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := BuildPackages(PackagesInput{Rows: []routeros.Reply{
				{"name": "routeros", "version": "7.24.1", "scheduled": tc.scheduled},
			}})
			if got.PendingReboot != tc.want {
				t.Errorf("PendingReboot = %v, want %v", got.PendingReboot, tc.want)
			}
			if (got.Counts.Scheduled > 0) != got.PendingReboot {
				t.Errorf("scheduled=%d but PendingReboot=%v — the flag and the count "+
					"disagree, so the page can show a pending-reboot banner beside a "+
					"count of zero", got.Counts.Scheduled, got.PendingReboot)
			}
		})
	}
}

// TestBuildPackagesAvailabilityDefault — the same nil rule as DNS, stated once
// per collector because each has its own latch.
func TestBuildPackagesAvailabilityDefault(t *testing.T) {
	no := false
	if got, _ := BuildPackages(PackagesInput{Available: nil}); !got.Available {
		t.Error("unknown availability read as UNavailable; a router is presumed to " +
			"have the package menu until it says otherwise, and the alternative " +
			"blanks the page before anything has answered")
	}
	if got, _ := BuildPackages(PackagesInput{Available: &no}); got.Available {
		t.Error("an explicit no did not survive")
	}
}

// TestCarriedFirmwareAndUpdatePassThrough. Both are read on a slow lane while the
// package list is read every tick, so most ticks build a payload from values the
// tick did not fetch. Dropping them would blank the firmware line on every tick
// that was not a config tick — visible as a field that flickers.
func TestCarriedFirmwareAndUpdatePassThrough(t *testing.T) {
	fw := Firmware{IsRouterboard: true, BoardName: "hAP ax^3", CurrentFirmware: "7.24.1"}
	up := Update{Channel: "stable", InstalledVersion: "7.24.1", LatestVersion: "7.24.2"}
	got, _ := BuildPackages(PackagesInput{Firmware: fw, Update: up})
	if got.Firmware != fw || got.Update != up {
		t.Errorf("carried state was not passed through: firmware=%+v update=%+v",
			got.Firmware, got.Update)
	}
}

// TestFirmwareCarriesTheAutoUpgradeSetting — `/system/routerboard/settings`
// `auto-upgrade`, which the Packages page shows and toggles.
//
// THREE STATES, which is why the field is a pointer. No capture in this
// repository holds this menu, so the fixture replay cannot show it and
// `addedSinceNode` points at this test instead.
func TestFirmwareCarriesTheAutoUpgradeSetting(t *testing.T) {
	board := routeros.Reply{"routerboard": "true", "current-firmware": "7.24.1"}

	on := parseFirmware(board, routeros.Reply{"auto-upgrade": "yes"})
	if on.AutoUpgrade == nil || !*on.AutoUpgrade {
		t.Errorf("auto-upgrade=yes read as %v, want true", on.AutoUpgrade)
	}
	off := parseFirmware(board, routeros.Reply{"auto-upgrade": "no"})
	if off.AutoUpgrade == nil || *off.AutoUpgrade {
		t.Errorf("auto-upgrade=no read as %v, want false", off.AutoUpgrade)
	}
	// A CHR has no routerboard menu at all, and a read-only API user can be
	// refused it. Neither is "off": the page must draw nothing rather than a
	// switch that claims the router said no.
	if unknown := parseFirmware(board, nil); unknown.AutoUpgrade != nil {
		t.Errorf("an unread settings menu produced %v, want nil", unknown.AutoUpgrade)
	}

	// AND IT MOVES THE FINGERPRINT, or toggling it would never reach the page:
	// the payload is suppressed while nothing it hashes has changed.
	if packagesFingerprint(nil, on, Update{}) == packagesFingerprint(nil, off, Update{}) {
		t.Error("auto-upgrade does not move the fingerprint, so a toggle would not be sent")
	}
}
