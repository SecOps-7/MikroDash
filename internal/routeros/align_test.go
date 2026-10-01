package routeros

import (
	"reflect"
	"sync/atomic"
	"testing"

	ros "github.com/go-routeros/routeros/v3"
)

// TestTheTagCounterCannotBeUnaligned — issue #146, patched in #147.
//
// ── THE BUG ────────────────────────────────────────────────────────────────
//
// `Client.nextTag` was a plain `int64` updated with `atomic.AddInt64`. On a
// 32-bit architecture an `int64` is only 4-byte aligned, and a 64-bit atomic on
// an unaligned address PANICS rather than merely being slow. The field sat
// after a run of mixed-size fields, so on linux/arm it landed at a 4-mod-8
// offset and the FIRST command the client sent took the whole process down:
//
//	panic: unaligned 64-bit atomic operation
//	internal/runtime/atomic.Xadd64(0x3c1d69c, 0x1)
//	github.com/go-routeros/routeros/v3.(*Client).incrementTag(...)
//
// Reported by mvdteam running the arm/v7 image in a container on a hAP ac3,
// where it fired on "Test API Connection" every time. Fixed by TastyHeadphones.
//
// ── WHY THIS ASSERTS THE TYPE AND NOT THE OFFSET ───────────────────────────
//
// `sync/atomic.Int64` embeds `align64`, which the compiler special-cases to
// force 8-byte alignment WHEREVER the field sits. The type is therefore the
// whole fix, and moving the field to the top of the struct is belt and braces.
//
// That matters for what a test can prove. #147 asserted
// `unsafe.Offsetof(nextTag)%8 == 0`, which is a tautology once the type carries
// `align64`: moving the field back to its original slot leaves that assertion
// passing, which was measured rather than assumed. And on amd64 it passes for a
// plain `int64` too, because amd64 aligns every int64 to 8 - so on the only
// architecture this suite runs on, it could not have failed for the original
// bug either.
//
// The type IS the invariant, so the type is what is pinned. Change the field
// back to `int64` and this fails everywhere, including on the architecture the
// suite actually runs on.
//
// ── AND WHY IT IS IN THIS PACKAGE ──────────────────────────────────────────
//
// `third_party/go-routeros` is a SEPARATE MODULE. `go test ./...` from the repo
// root does not descend into it - it reports "main module (mikrodash) does not
// contain package mikrodash/third_party/go-routeros" - and neither
// `tools/verify.sh` nor any workflow names that directory. A test living there
// reads as live and can never fire, which is why every other patch gate sits
// here. See `third_party/go-routeros/PATCHES.md`.
func TestTheTagCounterCannotBeUnaligned(t *testing.T) {
	f, ok := reflect.TypeOf(ros.Client{}).FieldByName("nextTag")
	if !ok {
		t.Fatal("go-routeros no longer has a `nextTag` field on Client. If the tag " +
			"counter was renamed, re-aim this test at the new name; if the library " +
			"stopped counting tags itself, record that in PATCHES.md and remove this.")
	}

	want := reflect.TypeOf(atomic.Int64{})
	if f.Type != want {
		t.Errorf("Client.nextTag is %s, not %s.\n\n"+
			"A 64-bit atomic on a plain int64 PANICS on 32-bit ARM when the field is "+
			"not 8-byte aligned, and MikroDash ships an arm/v7 image that people run "+
			"in a container on the router itself. `atomic.Int64` embeds `align64`, "+
			"which is what makes the alignment a property of the TYPE rather than of "+
			"where the field happens to sit. Issue #146.", f.Type, want)
	}

	// THE CONTROL. Without it a `want` that had drifted to match whatever the
	// field is would make the comparison above vacuous - it would be asserting
	// that the field has the type the field has.
	if want.Align() != 8 {
		t.Errorf("atomic.Int64 reports %d-byte alignment on this architecture, so it is "+
			"no longer the guarantee this test relies on", want.Align())
	}
}
