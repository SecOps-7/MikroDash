package backups

import "testing"

func restoreOK(t *testing.T, v RestoreDecision) {
	t.Helper()
	if !v.OK {
		t.Fatalf("refused with %q (was=%q now=%q); this restore should proceed",
			v.Code, v.Was, v.Now)
	}
}

func restoreRefused(t *testing.T, v RestoreDecision, code string) {
	t.Helper()
	if v.OK {
		t.Fatalf("allowed; want a %s refusal", code)
	}
	if v.Code != code {
		t.Fatalf("refused with %q, want %q", v.Code, code)
	}
}

// matchingRestore is the ordinary case: right name, same device, same version.
func matchingRestore() (string, RestoreRequest, RestoreIdentity) {
	return "Branch Office", RestoreRequest{Confirm: "Branch Office"},
		RestoreIdentity{
			RowSerial: "HGL09XY1ZQ2", NowSerial: "HGL09XY1ZQ2",
			RowOSVersion: "7.24", NowOSVersion: "7.24",
		}
}

func TestAMatchingRestoreProceeds(t *testing.T) {
	restoreOK(t, CheckRestore(matchingRestore()))
}

func TestTheTypedNameIsTrimmedButNotFolded(t *testing.T) {
	label, _, id := matchingRestore()
	// TRIMMED, as the original trims both sides.
	restoreOK(t, CheckRestore(label, RestoreRequest{Confirm: "  Branch Office  "}, id))

	// NOT case-folded. The Packages page's confirmation uses EqualFold and this
	// one does not — the live app really does differ between the two handlers,
	// and this pins the difference so a later tidy-up cannot erase it on the
	// page that reboots the router.
	restoreRefused(t, CheckRestore(label, RestoreRequest{Confirm: "branch office"}, id),
		RestoreConfirmMismatch)
}

func TestAMistypedNameIsRefusedBeforeAnythingIsRead(t *testing.T) {
	label, _, id := matchingRestore()
	// Even with a serial mismatch ALSO present the answer is the confirm code:
	// an operator who mistyped is told so without a router conversation.
	id.NowSerial = "SOMEOTHERSERIAL"
	restoreRefused(t, CheckRestore(label, RestoreRequest{Confirm: "Branch Ofice"}, id),
		RestoreConfirmMismatch)
}

// TestASerialMismatchIsRefusedAndNamesBothSides — a backup belongs to one
// device. Loading it onto another writes that box's identity and keys over this
// one, so this is a refusal rather than a question.
func TestASerialMismatchIsRefusedAndNamesBothSides(t *testing.T) {
	label, req, id := matchingRestore()
	id.NowSerial = "B4C1TTT0000"
	v := CheckRestore(label, req, id)
	restoreRefused(t, v, RestoreSerialMismatch)
	if v.Was != "HGL09XY1ZQ2" || v.Now != "B4C1TTT0000" {
		t.Errorf("was=%q now=%q; the page names both in its sentence", v.Was, v.Now)
	}
	// AND ACCEPTVERSION DOES NOT OVERRIDE IT. The two answer different
	// questions, and a page that re-submits after the version prompt must not
	// thereby wave through a different device.
	req.AcceptVersion = true
	restoreRefused(t, CheckRestore(label, req, id), RestoreSerialMismatch)
}

// TestSerialIsCheckedBeforeVersion — being asked to accept a version difference
// for a restore that was going to be refused anyway is a prompt with no good
// answer.
func TestSerialIsCheckedBeforeVersion(t *testing.T) {
	label, req, id := matchingRestore()
	id.NowSerial = "B4C1TTT0000"
	id.NowOSVersion = "7.25"
	restoreRefused(t, CheckRestore(label, req, id), RestoreSerialMismatch)
}

func TestAVersionDifferenceIsAskedOnceThenAnswered(t *testing.T) {
	label, req, id := matchingRestore()
	id.NowOSVersion = "7.25"

	v := CheckRestore(label, req, id)
	restoreRefused(t, v, RestoreVersionMismatch)
	if v.Was != "7.24" || v.Now != "7.25" {
		t.Errorf("was=%q now=%q; the prompt names both versions", v.Was, v.Now)
	}

	req.AcceptVersion = true
	restoreOK(t, CheckRestore(label, req, id))
}

// TestAnUnknownValueIsNotAMismatch is the rule that keeps historic rows and
// CHR/x86 routers usable. A row recorded before this app captured identity has
// no serial, and a box with no routerboard reports none — neither is evidence
// the backup belongs elsewhere.
func TestAnUnknownValueIsNotAMismatch(t *testing.T) {
	label, req, _ := matchingRestore()
	cases := []struct {
		name string
		id   RestoreIdentity
	}{
		{"the row has no serial", RestoreIdentity{NowSerial: "HGL09XY1ZQ2"}},
		{"the device reports no serial (CHR, x86)", RestoreIdentity{RowSerial: "HGL09XY1ZQ2"}},
		{"neither side has a serial", RestoreIdentity{}},
		{"the row has no version", RestoreIdentity{NowOSVersion: "7.25"}},
		{"the device reports no version", RestoreIdentity{RowOSVersion: "7.24"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			restoreOK(t, CheckRestore(label, req, c.id))
		})
	}
}

// TestShortVersionMatchesTheOriginalSplit. The empty result for a LEADING SPACE
// is the case worth stating: `" 7.24".split(' ')[0]` is "", an empty version is
// not compared at all, so the original silently allows the restore. A port that
// trimmed would refuse one the live app permits.
func TestShortVersionMatchesTheOriginalSplit(t *testing.T) {
	cases := map[string]string{
		"7.24 (stable)":      "7.24",
		"7.24":               "7.24",
		"":                   "",
		" 7.24":              "",
		"7.19.4 (long-term)": "7.19.4",
	}
	for in, want := range cases {
		if got := ShortVersion(in); got != want {
			t.Errorf("ShortVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAnUntrimmedVersionIsNotComparedAtAll ties the two together: the guard sees
// what ShortVersion produced, so a leading space reaches it as "" and passes.
func TestAnUntrimmedVersionIsNotComparedAtAll(t *testing.T) {
	label, req, id := matchingRestore()
	id.NowOSVersion = ShortVersion(" 7.25 (stable)")
	if id.NowOSVersion != "" {
		t.Fatalf("precondition: got %q, want the empty string", id.NowOSVersion)
	}
	restoreOK(t, CheckRestore(label, req, id))
}
