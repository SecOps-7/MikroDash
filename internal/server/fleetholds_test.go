package server

import (
	"strings"
	"testing"
)

// TWO REQUESTS TO ONE ROUTER MUST NOT RELEASE EACH OTHER'S HOLD.
//
// `session.Retain` is idempotent BY NAME and `Drop` deletes that name, so a
// reason shared by every request meant one map entry: the first request to
// finish dropped the hold while the others were still reading, which lets the
// session's demand be recomputed — and the collectors stopped — on a router
// somebody is mid-read of.
//
// `Sync all missing` sends one request per record at once, so this was one
// button away rather than a race nobody would hit.
//
// The session manager is not reachable from a unit test without a router, so
// what is pinned here is the property the fix rests on: the reason `fleetSession`
// asks for is unique per call and still carries the endpoint's name, which is the
// half `session.Manager.Retain` documents as being for a human reading a stuck
// hold.
func TestAFleetHoldReasonIsUniquePerRequest(t *testing.T) {
	const base = "dns-fleet"
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		r := fleetHoldReason(base)
		if !strings.HasPrefix(r, base+"#") {
			t.Fatalf("reason %q no longer names the endpoint it belongs to", r)
		}
		if seen[r] {
			t.Fatalf("reason %q was handed out twice; one Drop would release both", r)
		}
		seen[r] = true
	}
}
