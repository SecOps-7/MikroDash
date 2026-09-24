package verify

import (
	"regexp"
	"strings"
	"testing"
)

// TestDevicesFocusPrimesTheAlertPool.
//
// ── THE POOL CAN ONLY BE PRIMED BY SOMEBODY ASKING ─────────────────────────
//
// `session.Manager.PrimeStats` is deliberately not a poll: a router with alerting and
// reporting both off holds a bare socket and runs no collectors, and reading its
// gauges on a schedule would give back the cost the toggle exists to remove. It
// is taken ONCE, when a browser opens the Devices page, on a connection that is
// already open.
//
// So the whole feature is one call in one handler, and its own unit tests pass
// with that call deleted - they drive `PrimeStats` directly. What comes back is
// the symptom it was added for: a card with a green badge over an empty CPU,
// memory and uptime for the two seconds the overview pool takes to dial. Nothing
// errors, nothing logs, and the page eventually fills in, which is what makes it
// easy to lose and hard to notice.
//
// Read rather than run, like the rejoin check next door - but ONLY as the cheap
// half. `internal/server`'s TestTheFirstFrameAfterAFocusCarriesThePrimedGauges
// drives `devicesFocus` and asserts the frame, which is the property that
// matters and the one a comment cannot satisfy. This one catches the call being
// deleted without a test binary having to dial anything.
//
// THE CODE, NOT THE COMMENTARY. The body is stripped of comments before it is
// searched: this file's own subject is a function whose comments discuss
// `PrimeStats()` at length, so a scan of the raw text would be satisfied by the
// prose explaining the call even after the call itself had gone. That is the
// "a check must not read itself" trap in CLAUDE.md, reached from one step away.
func TestDevicesFocusPrimesTheAlertPool(t *testing.T) {
	root := repoRoot(t)
	src := mustRead(t, root+"/internal/server/devices.go")

	i := strings.Index(src, "func (cn *conn) devicesFocus(")
	if i < 0 {
		t.Fatal("devicesFocus is gone from devices.go - this check is reading nothing")
	}
	body := src[i:]
	if j := regexp.MustCompile(`\n(func|type|var|const) `).FindStringIndex(body[1:]); j != nil {
		body = body[:j[0]+1]
	}
	body = stripComments(body, ".go")

	// ── BOTH MARKERS MUST BE PRESENT, AND ABSENCE IS A FAILURE ────────────
	//
	// The order assertion used to be guarded by `prime >= 0 && send >= 0`, which
	// made it VACUOUS the moment either name changed: `strings.Index` returns
	// -1, the guard is false, and a renamed `sendRoutersStats` turned the check
	// into one that could no longer fail. Each index is now checked for itself,
	// so a rename fails loudly here rather than silently stopping.
	prime := strings.Index(body, "PrimeStats()")
	if prime < 0 {
		t.Fatal("devicesFocus does not call PrimeStats - a router with alerting " +
			"and reporting off has no collectors, so its card opens with a green " +
			"badge and blank gauges until the overview pool finishes dialling.")
	}
	send := strings.Index(body, "cn.sendRoutersStats()")
	if send < 0 {
		t.Fatal("devicesFocus no longer calls cn.sendRoutersStats() - either the " +
			"send was renamed, in which case fix this check, or the focus sends " +
			"no first frame at all.")
	}

	// ── ORDER IS THE OTHER HALF ───────────────────────────────────────────
	//
	// Priming AFTER the first payload is built primes nothing anyone can see:
	// the frame has already gone with the gap in it, and by the next tick the
	// overview pool is answering. That reads as a working call and fixes nothing.
	if prime > send {
		t.Error("devicesFocus primes the sessions AFTER sending the first " +
			"routers:stats - the frame the fix exists for has already left.")
	}

	// ── AND AFTER THE SYNCS, WHICH IS THE OTHER ORDERING THAT MATTERS ─────
	//
	// `syncFleetHolds` decides the session set: `PlanSync` rebuilds a session
	// whose flags changed, and a rebuilt session is a fresh socket with no
	// reading on it. Priming ahead of it spends a command on sessions that are
	// then discarded. This was proposed as an improvement in review and is
	// pinned here so the reasoning does not have to be rediscovered.
	sync := strings.Index(body, "cn.srv.syncFleetHolds()")
	if sync < 0 {
		t.Fatal("devicesFocus no longer calls cn.srv.syncFleetHolds() - fix this " +
			"check, or the fleet holds are no longer being synced on focus.")
	}
	if prime < sync {
		t.Error("devicesFocus primes the sessions BEFORE syncing them - a " +
			"session PlanSync rebuilds is a new socket, so the reading the " +
			"prime just took is discarded and the frame goes out with the gap " +
			"still in it.")
	}
}
