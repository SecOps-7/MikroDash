package server

import (
	"errors"
	"testing"

	"mikrodash/internal/session"
)

// WRITES ARE LIMITED PER USER PER ROUTER, as well as serialised. #97.
func TestRouterWritesAreRateLimited(t *testing.T) {
	s := &Server{writeLimit: newWriteLimiter()}
	cn := &conn{srv: s, sess: &Session{Username: "alice"}, routerID: "r-1",
		rsession: session.NewForTest(nil, "r-1")}

	ran := 0
	for i := 1; i <= routerWritesPerMinute; i++ {
		if err := cn.inWriteQueue(func() error { ran++; return nil }); err != nil {
			t.Fatalf("write %d of %d was refused: %v", i, routerWritesPerMinute, err)
		}
	}
	err := cn.inWriteQueue(func() error { ran++; return nil })
	if !errors.Is(err, errWriteRateLimited) {
		t.Fatalf("write %d in a minute answered %v, want the rate limit", routerWritesPerMinute+1, err)
	}
	if ran != routerWritesPerMinute {
		t.Errorf("%d writes ran; a refused write must not reach the router", ran)
	}
	if got := writeFailCode(err); got != "rate-limited" {
		t.Errorf("a rate-limited write maps to %q, want rate-limited", got)
	}

	// Another router, and another user, have their own allowance.
	other := &conn{srv: s, sess: &Session{Username: "alice"}, routerID: "r-2",
		rsession: session.NewForTest(nil, "r-2")}
	if err := other.inWriteQueue(func() error { return nil }); err != nil {
		t.Errorf("a write to another router was refused: %v", err)
	}
	bob := &conn{srv: s, sess: &Session{Username: "bob"}, routerID: "r-1",
		rsession: session.NewForTest(nil, "r-1")}
	if err := bob.inWriteQueue(func() error { return nil }); err != nil {
		t.Errorf("another user's write was refused: %v", err)
	}
}
