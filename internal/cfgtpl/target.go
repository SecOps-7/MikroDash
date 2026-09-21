package cfgtpl

import (
	"regexp"
	"strings"
)

// The three kinds of template, as cfg_templates.kind stores them.
const (
	// KindFragment is an addition: merged into the running router by /import.
	KindFragment = "fragment"
	// KindFullExport replaces a router's whole configuration: reset with no
	// defaults, then the export imported. Portable across models.
	KindFullExport = "full-export"
	// KindFullBinary replaces it with another router's binary backup. Not
	// portable: the same model and RouterOS release only.
	KindFullBinary = "full-binary"
)

// Target refusal codes, in the vocabulary the page maps to sentences.
const (
	TargetNotFull         = "target-not-full"
	TargetUnknown         = "target-unknown"
	TargetModelMismatch   = "target-model-mismatch"
	TargetVersionMismatch = "target-version-mismatch"
)

// Device is what a router says it is: `board-name` and `version` from
// /system/resource.
type Device struct {
	Board     string
	OSVersion string
}

// TargetDecision is CheckTarget's answer. OK means proceed.
type TargetDecision struct {
	OK   bool
	Code string
	// Was and Now name both sides of a mismatch, for the page's sentence.
	Was string
	Now string
	// Overridable is true when typing the target's model back unlocks it.
	Overridable bool
}

// CheckTarget decides whether a full replacement taken from src may be put on
// the router that is now.
//
// ── NOT CheckRestore, AND THE DIFFERENCES ARE THE POINT ─────────────────────
//
// backups.CheckRestore treats an unknown serial or version as no evidence of a
// mismatch, because a restore point is being put back on the router it came
// from and x86 has no serial. Here the template came from ANOTHER router, and
// the question is whether the two are alike. Not knowing either side's model
// is therefore not a pass: it is refused, and nothing unlocks it.
//
// A binary backup is refused across models or releases outright. It carries
// the source's MAC addresses and interface layout, and MikroTik supports
// loading it only on the same model and release. An export is text and moves
// between models, but its interface names and menus may not exist on the
// target. So that crossing is allowed only once the operator types the
// target's model back.
func CheckTarget(kind string, src, now Device, override string) TargetDecision {
	if kind != KindFullExport && kind != KindFullBinary {
		return TargetDecision{Code: TargetNotFull, Was: kind}
	}
	srcMM, nowMM := majorMinor(src.OSVersion), majorMinor(now.OSVersion)
	if src.Board == "" || now.Board == "" || srcMM == "" || nowMM == "" {
		return TargetDecision{Code: TargetUnknown}
	}
	var d TargetDecision
	switch {
	case src.Board != now.Board:
		d = TargetDecision{Code: TargetModelMismatch, Was: src.Board, Now: now.Board}
	case srcMM != nowMM:
		d = TargetDecision{Code: TargetVersionMismatch, Was: srcMM, Now: nowMM}
	default:
		return TargetDecision{OK: true}
	}
	if kind == KindFullBinary {
		return d
	}
	d.Overridable = true
	// One override covers the model and the release together: both are the
	// same statement, "I know this router is not the one the export came from".
	if strings.TrimSpace(override) == now.Board {
		return TargetDecision{OK: true}
	}
	return d
}

var versionMM = regexp.MustCompile(`^(\d+)\.(\d+)`)

// majorMinor is "7.24" from `7.24.4 (stable)`, or "" when the version does not
// start with two numbers.
func majorMinor(v string) string {
	m := versionMM.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2]
}
