package backups

// Whether a restore may proceed, decided without a router.
//
// The live version is inline in the `backups:restore` handler. It is pulled out
// here for the reason the live repo's own guards give — "the decision lives in
// one pure module rather than inline in six handlers. Every refusal is one
// function, testable without a router" — and because this particular decision
// stands between an operator and a REBOOT. It is the one place in Backups where
// a wrong answer overwrites a working device with another one's configuration.
//
// ── THE THREE CHECKS ARE NOT THE SAME KIND OF CHECK ─────────────────────────
//
// A typed name that does not match is an operator error, and the answer is
// simply no. A serial mismatch is a REFUSAL: a backup belongs to one device, and
// loading it onto another writes another box's identity and keys over this one.
// A version difference is a QUESTION, asked once and answerable — MikroTik
// recommend matching versions, but restoring across them is something operators
// legitimately do, and the page re-submits with `acceptVersion` after naming
// both versions in a sentence.
//
// ── AN UNKNOWN VALUE IS NOT A MISMATCH ──────────────────────────────────────
//
// Every comparison requires BOTH sides to be non-empty, exactly as the original
// does. A row recorded before this app captured identity has no serial, and x86
// and CHR have no routerboard to report one — neither is evidence that the
// backup belongs to another device. Treating empty as a mismatch would refuse
// every historic restore point and every restore on a CHR; treating it as a
// match is what the original does and is the only reading that leaves those
// usable.

import "strings"

// Restore refusal codes, in the vocabulary the page maps to sentences.
const (
	RestoreConfirmMismatch = "confirm-mismatch"
	RestoreSerialMismatch  = "serial-mismatch"
	RestoreVersionMismatch = "version-mismatch"
)

// RestoreRequest is what the operator asked for.
type RestoreRequest struct {
	// Confirm is the router name the operator typed, compared to the label they
	// can see on screen.
	Confirm string
	// AcceptVersion is the answer to the version question, set when the page
	// re-submits after being told both versions.
	AcceptVersion bool
}

// RestoreIdentity pairs what the backup RECORDED with what the device reports
// NOW. Either half may be empty; see the header.
type RestoreIdentity struct {
	RowSerial    string
	NowSerial    string
	RowOSVersion string
	NowOSVersion string
}

// RestoreDecision is the answer. OK means proceed.
//
// NOT `RestoreVerdict`: that name is taken by the TOKEN redemption's answer in
// restoretoken.go, which reports a `Reason` rather than a `Code` and belongs to
// a different question entirely — whether the router may fetch the file, not
// whether the operator may restore it.
type RestoreDecision struct {
	OK   bool
	Code string
	// Was and Now name the two sides of a mismatch, and exist because the page
	// puts both into its sentence — "taken from serial X, but this router
	// reports Y", and the version prompt names both versions. A bare code would
	// leave it saying "something did not match", which an operator cannot act
	// on.
	Was string
	Now string
}

// CheckRestore is the whole decision.
//
// ── THE NAME COMPARISON IS CASE-SENSITIVE, AND THAT IS NOT AN OVERSIGHT ─────
//
// The Packages page's typed confirmation uses `EqualFold`, deliberately: there
// the point is to prove the operator knows which router this is, not to test
// their typing. Restore does NOT — the original compares trimmed strings with
// `!==`. Reproduced rather than harmonised, because the two handlers really do
// disagree in the live app and a port that quietly picks one changes what an
// operator can do on a page that reboots the device.
//
// ORDER IS PART OF THE CONTRACT. The typed confirmation is checked FIRST, before
// anything is read off the device, so an operator who mistyped is told so
// without a router conversation. Serial comes before version because one is a
// refusal and the other is a question: being asked to accept a version
// difference for a restore that was going to be refused anyway is a prompt with
// no good answer.
func CheckRestore(label string, req RestoreRequest, id RestoreIdentity) RestoreDecision {
	if strings.TrimSpace(req.Confirm) != strings.TrimSpace(label) {
		return RestoreDecision{Code: RestoreConfirmMismatch}
	}
	if id.RowSerial != "" && id.NowSerial != "" && id.RowSerial != id.NowSerial {
		return RestoreDecision{Code: RestoreSerialMismatch, Was: id.RowSerial, Now: id.NowSerial}
	}
	if id.RowOSVersion != "" && id.NowOSVersion != "" &&
		id.RowOSVersion != id.NowOSVersion && !req.AcceptVersion {
		return RestoreDecision{Code: RestoreVersionMismatch, Was: id.RowOSVersion, Now: id.NowOSVersion}
	}
	return RestoreDecision{OK: true}
}

// ShortVersion is the original's
//
//	String(res.version || '').split(' ')[0]
//
// An INDENTED code block on purpose. gofmt's doc-comment formatter follows the
// old TeX convention and rewrites a pair of apostrophes in PROSE into a curly
// closing quote — which would silently misquote the very line this exists to
// reproduce. Indented blocks are left alone, so the JavaScript stays verbatim.
//
// `/system/resource/print` answers `7.24 (stable)` and the row stores only the
// number. ONE implementation, called by both the runner that RECORDS a version
// and the guard that COMPARES one: the live app spells the same split out twice,
// and two copies that drifted by a trim would report a version mismatch between
// a backup and the very router it was taken from.
//
// Deliberately does not trim. `" 7.24".split(' ')[0]` is the empty string, and
// an empty version is not compared at all — so a leading space makes the check
// silently pass in the original, and a tidier port would refuse a restore the
// live app allows.
func ShortVersion(v string) string {
	return strings.SplitN(v, " ", 2)[0]
}
