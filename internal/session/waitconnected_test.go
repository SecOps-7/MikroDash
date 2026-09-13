package session

import (
	"context"
	"testing"
	"time"
)

// WHAT `Retain` DOES NOT DO, AND WHY A CALLER HAS TO WAIT.
//
// A hold builds the session and starts the dial, then returns. That is right for
// a hold taken to keep a router polled and wrong for one taken to read from it
// now: the read runs against a session with no client and answers `not
// connected`, so the fleet endpoints reported every router nothing else was
// already watching as unreachable.
//
// The three answers below are the three that matter, and the middle one is the
// one that was missing.

func TestWaitConnectedReturnsAtOnceWhenAlreadyUp(t *testing.T) {
	s := &Session{connected: true}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	if !s.WaitConnected(ctx) {
		t.Fatal("a connected session reported itself down")
	}
	if d := time.Since(start); d > 20*time.Millisecond {
		t.Errorf("took %v on a session that was already up — it slept before looking", d)
	}
}

func TestWaitConnectedSeesTheConnectionArrive(t *testing.T) {
	s := &Session{}
	go func() {
		time.Sleep(60 * time.Millisecond)
		s.mu.Lock()
		s.connected = true
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !s.WaitConnected(ctx) {
		t.Fatal("the connection arrived and the wait did not notice")
	}
}

// A ROUTER THAT IS DOWN MUST NOT HOLD THE REQUEST. The deadline is the caller's,
// and this is what stops one unreachable router costing a page load.
func TestWaitConnectedGivesUpOnTheDeadline(t *testing.T) {
	s := &Session{}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	if s.WaitConnected(ctx) {
		t.Fatal("a session that never connected reported itself up")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v to give up on an 80ms deadline", d)
	}
}

// A SESSION THAT IS GONE IS NOT A SESSION THAT IS SLOW, and waiting out the
// deadline on one would be a request held for nothing.
func TestWaitConnectedStopsOnAClosedSession(t *testing.T) {
	s := &Session{closed: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if s.WaitConnected(ctx) {
		t.Fatal("a closed session reported itself up")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("took %v to notice a closed session — it waited out the deadline", d)
	}
}
