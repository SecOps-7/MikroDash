package verify

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTheDropPathClearsTheStreamLevel.
//
// ── A BUG THE INSTRUMENT FOUND IN ITSELF, AND A CALL SITE NO UNIT TEST SEES ─
//
// `roslimit` counts the channels this process holds per router, so B.4 can tell
// whether moving a collector to a stream cost anything. Every channel is a tag
// on ONE TCP connection, so a drop takes them all with it whether or not a
// collector called its stop.
//
// Measured on 2026-09-09, minutes after the counter was deployed: three routers
// reported 3 open channels and the fourth reported 4 - and the fourth was the
// only one that had reconnected. Forcing a second reconnect took it to 5. One of
// the three streaming collectors does not release across a redial, so the level
// grew by one per outage and a flapping router would have inflated it
// indefinitely. That is the one thing an instrument about channel pressure must
// not do.
//
// The fix is one call in `connectLoop`'s drop path, and **deleting it fails no
// unit test**: the level is per-router state in another package, and every test
// of it passes whether or not anything in production calls it. That is exactly
// the shape `internal/verify` exists for, so the call site is read here.
//
// ── IT MUST BE THE DROP PATH, NOT ANYWHERE ─────────────────────────────────
//
// Asserting only that the file mentions `StreamsGone` would pass against a call
// in `Shutdown`, or in a comment, and the level would still climb on every
// reconnect. So this locates the block that nils the client and checks the call
// is inside it.
func TestTheDropPathClearsTheStreamLevel(t *testing.T) {
	src := mustRead(t, filepath.Join(repoRoot(t), "internal", "session", "session.go"))

	// ── SCOPED TO `connectLoop`, AND THE FIRST VERSION WAS NOT ─────────────
	//
	// `s.client = nil` beside `s.connected = false` is how "the socket is gone"
	// is spelled, and it appears TWICE: once here and once in `Reconfigure`,
	// which drops the client so the loop redials a new endpoint. Anchoring on
	// the first match found Reconfigure's and reported the call missing when it
	// was present twenty lines below the other one.
	//
	// Reconfigure needs no call of its own: closing the client makes
	// `waitUntilDown` return, so the loop reaches THIS path and clears the level
	// there. One authoritative call site, which is the point.
	body := sliceBetween(t, src, "func (s *Session) connectLoop(", "\nfunc ")
	drop := regexp.MustCompile(`s\.client = nil\s*\n\s*s\.connected = false`)
	loc := drop.FindStringIndex(body)
	if loc == nil {
		t.Fatal("could not find the drop path inside connectLoop (`s.client = nil` " +
			"beside `s.connected = false`). This check is measuring nothing - re-aim " +
			"it at wherever the connection is now cleared.")
	}
	src = body

	// Within the next thirty lines: far enough to allow the comment the call
	// carries, near enough that a call in another function cannot satisfy it.
	after := src[loc[1]:]
	if i := nthLine(after, 30); i > 0 {
		after = after[:i]
	}
	if !strings.Contains(after, "roslimit.StreamsGone(") {
		t.Error("the drop path does not call roslimit.StreamsGone. The channel level is " +
			"per router and every channel is a tag on the socket that just died, so " +
			"without this it never comes down: measured on 2026-09-09, a flapping " +
			"router gained one phantom channel per outage and the instrument would " +
			"have reported pressure from streams that no longer exist.\n" +
			"Deleting this call fails NO unit test, which is why it is read here.")
	}
}

// nthLine returns the byte offset just past the nth newline, or -1.
func nthLine(s string, n int) int {
	for i, off := 0, 0; off < len(s); i++ {
		j := strings.IndexByte(s[off:], '\n')
		if j < 0 {
			return -1
		}
		off += j + 1
		if i+1 == n {
			return off
		}
	}
	return -1
}
