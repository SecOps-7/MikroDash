package alertpool

import "testing"

// TWO CONCURRENT TEARDOWNS OF ONE SESSION.
//
// The guard was `select { case <-s.stop: return; default: close(s.stop) }` — a
// check-then-act. Two goroutines both reach `default` and both close, which
// panics with "close of closed channel" and takes the process down.
//
// It was safe in practice only because both callers remove the session from
// `p.sessions` under `p.mu` first, so one session reaches teardown once. That is
// an invariant of the CALLERS. This test asserts the invariant is now teardown's
// own, so a third caller — or a collector tearing down its own session on a
// close event — meets the guard rather than the panic.
//
// It FAILS on the old code under `-race -count=3`, and the failure is the real
// panic, not a race warning.
func TestConcurrentTeardownIsSafe(t *testing.T) {
	for i := 0; i < 200; i++ {
		s := &poolSession{stop: make(chan struct{}), done: make(chan struct{})}
		close(s.done) // nothing running
		start := make(chan struct{})
		errs := make(chan any, 2)
		for g := 0; g < 2; g++ {
			go func() {
				defer func() { errs <- recover() }()
				<-start
				s.teardown()
			}()
		}
		close(start)
		for g := 0; g < 2; g++ {
			if r := <-errs; r != nil {
				t.Fatalf("teardown panicked when called twice concurrently: %v", r)
			}
		}
	}
}
