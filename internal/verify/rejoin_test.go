package verify

import (
	"regexp"
	"strings"
	"testing"
)

// TestSelectRouterRejoinsEveryPerSocketSubscription.
//
// ── THE UNIT TESTS PASS WITHOUT THE CALL ───────────────────────────────────
//
// `selectRouter` LEAVES EVERY ROOM this socket is in and then joins the ones it
// knows about. Anything a browser subscribed to before the select — a dashboard
// card, a page — is re-established by an explicit replay: `rejoinCards` and
// `rejoinPage`. Each of those has its own tests, and every one of them passes
// with the call to it deleted, because they call the replay directly.
//
// That is the exact shape of the unwired Settings Save button: a correct
// function, its own green tests, and nothing invoking it. Here the cost is a
// browser subscribed to nothing — the payloads go to rooms it is not in, every
// card goes stale, and the server log shows a healthy session throughout. It
// was reported twice before this check existed.
//
// ── A SOURCE PIN, BECAUSE NO REQUEST EXERCISES IT ──────────────────────────
//
// Driving `selectRouter` needs a session manager, a store, a pool and a live
// router. The property is one line of wiring, so it is read rather than run —
// the same trade `TestEveryCardRoomIsEmittedTo` makes next door.
func TestSelectRouterRejoinsEveryPerSocketSubscription(t *testing.T) {
	root := repoRoot(t)
	src := mustRead(t, root+"/internal/server/ws.go")

	i := strings.Index(src, "func (cn *conn) selectRouter(")
	if i < 0 {
		t.Fatal("selectRouter is gone from ws.go — this check is reading nothing")
	}
	body := src[i:]
	// To the end of the function: the next top-level declaration.
	if j := regexp.MustCompile(`\n(func|type|var|const) `).FindStringIndex(body[1:]); j != nil {
		body = body[:j[0]+1]
	}

	// THE LEAVE IS THE PREMISE. Without it there is nothing to rejoin and this
	// check is asserting a rule that no longer applies — so it must be the thing
	// that fails first if the handler is restructured.
	if !strings.Contains(body, "hub.Leave(cn.c, room)") {
		t.Fatal("selectRouter no longer leaves every room; the rejoin rule below " +
			"was written for that design and needs re-deciding rather than " +
			"quietly continuing to pass")
	}

	for _, replay := range []struct{ call, why string }{
		{"cn.rejoinCards()", "dashboard card subscriptions sent before the select"},
		{"cn.rejoinPage()", "the page this browser is on"},
	} {
		if !strings.Contains(body, replay.call) {
			t.Errorf("selectRouter does not call %s — %s is dropped and never "+
				"re-established.\nThe browser then sits in no room for it: the "+
				"payloads are emitted, nobody receives them, the cards go stale, "+
				"and nothing in the log looks wrong.", replay.call, replay.why)
		}
	}
}
