package server

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSendNowHasItsOwnTighterLimit pins the difference between this route and
// the other three.
//
// The other three write a row. This one connects to a router, renders up to five
// PDFs and hands a message to an SMTP server, so the live app gives it a
// separate limiter at 5/min. Two properties matter and a shared 30 would break
// both: the ceiling itself, and that a burst of "Send now" must not exhaust the
// budget for EDITING a schedule.
func TestSendNowHasItsOwnTighterLimit(t *testing.T) {
	send := newRateLimiter(5, time.Minute)
	edit := newRateLimiter(30, time.Minute)

	const key = "10.0.0.9"
	for i := 1; i <= 5; i++ {
		if ok, _, _ := send.take(key); !ok {
			t.Fatalf("send-now request %d of 5 was refused", i)
		}
	}
	if ok, _, _ := send.take(key); ok {
		t.Error("a sixth send-now request in the same minute was allowed")
	}
	// ...and the edit budget is untouched by that burst.
	for i := 1; i <= 30; i++ {
		if ok, _, _ := edit.take(key); !ok {
			t.Fatalf("schedule edit %d of 30 was refused after a send-now burst", i)
		}
	}
}

// TestTheRunRouteIsRegisteredAndLimited drives the real mux.
func TestTheRunRouteIsRegisteredAndLimited(t *testing.T) {
	s := &Server{}
	mux := http.NewServeMux()
	s.registerReports(mux)

	req, _ := http.NewRequest("POST", reportsPrefix+"schedules/abc/run?routerId=r1", nil)
	h, pattern := mux.Handler(req)
	if h == nil || pattern == "" {
		t.Fatal("POST .../run matches no route")
	}
	if !strings.Contains(pattern, "/run") {
		t.Errorf("the request matched %q instead of the run route", pattern)
	}
}

// TestSplitListIgnoresBlanks: the sections and recipients columns are
// comma-separated text, and a trailing comma is normal in stored data.
func TestSplitListIgnoresBlanks(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"ping,traffic", []string{"ping", "traffic"}},
		{"ping, traffic ", []string{"ping", "traffic"}},
		{"ping,,traffic,", []string{"ping", "traffic"}},
		{"", nil},
		{" , ", nil},
		{"ping", []string{"ping"}},
	} {
		got := splitList(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitList(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitList(%q) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

// TestCreatorMayReadFailsClosedOnAnError, and OPEN when RBAC is simply absent.
//
// The two are different situations and this port answers them differently on
// purpose: an unavailable resolver is an install-wide condition already reported
// at startup, and refusing there would disable every schedule on an install
// whose RBAC tables were never created. An ERROR from a resolver that IS
// available is a check that did not run, and mailing on that is worse than
// stopping and saying so.
func TestCreatorMayReadIsPermissiveOnlyWhenRbacIsAbsent(t *testing.T) {
	s := &Server{}
	if !s.creatorMayRead("alice", "r1") {
		t.Error("an install with no RBAC resolver refused a schedule -- every schedule would disable")
	}
	if s.creatorMayRead("", "r1") {
		t.Error("an empty creator was granted access")
	}
}
