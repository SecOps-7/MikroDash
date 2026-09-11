package routeros

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// WHAT `Err()` REPORTS, because a log line now depends on it.
//
// The session's connect loop prints it when a router drops; before that, a drop
// logged "disconnected; retrying in 5s" and the cause was thrown away. Three
// properties make that line worth reading, and each one failing is a different
// wrong answer in the log:
//
//	the transport failure is kept   or the line says nothing
//	a TIMEOUT is not one            or an ordinary slow command reads as a drop
//	the FIRST failure wins          or a later symptom hides the cause
func TestErrReportsWhyTheConnectionFailed(t *testing.T) {
	c := &Client{}
	if got := c.Err(); got != nil {
		t.Fatalf("a fresh client reports %v; a connection that has not failed has no reason", got)
	}

	// A command timeout leaves the connection usable, and `wrap` says so by not
	// recording it. See Do: the deadline is the caller's, not the link's.
	_ = c.wrap(fmt.Errorf("routeros: timed out: %w", context.DeadlineExceeded))
	if got := c.Err(); got != nil {
		t.Errorf("a timed-out command recorded %v as a connection failure", got)
	}

	reset := errors.New("read tcp 198.51.100.2:34012->198.51.100.77:8728: connection reset by peer")
	_ = c.wrap(reset)
	if got := c.Err(); !errors.Is(got, reset) {
		t.Fatalf("Err() = %v, want the transport failure %v", got, reset)
	}

	// THE FIRST ONE WINS. Everything issued on a dead connection fails too, and
	// the last of those is the least informative — `wrap` keeps the first, and
	// this is the half of that rule a reader of the log depends on.
	_ = c.wrap(errors.New("use of closed network connection"))
	if got := c.Err(); !errors.Is(got, reset) {
		t.Errorf("Err() = %v after a later failure; want the first one, %v", got, reset)
	}

	// AND CLOSING DOES NOT ERASE IT, which is what lets the session's loop read
	// it on either side of the Close it does anyway. `err()` answers the other
	// question and reports the close instead.
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	if got := c.Err(); !errors.Is(got, reset) {
		t.Errorf("Err() = %v once closed; the reason the link went is still %v", got, reset)
	}
	if got := c.err(); errors.Is(got, reset) {
		t.Errorf("err() = %v once closed; it answers 'may I use this', which is 'no, closed'", got)
	}
}
