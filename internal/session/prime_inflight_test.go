package session

import (
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// A PRIME READ THAT TIMES OUT STAYS WITH THE ROUTER, SO ANOTHER IS NOT STARTED.
//
// A command past its deadline now keeps running, holding its router slot until
// the reply arrives, instead of taking the connection down. The Devices page
// re-primes every two seconds, so on a router slower than the 1.5 s prime
// deadline prime reads would stack and fill the router's eight slots. A
// session with a prime read still outstanding is skipped.
func TestAPrimeReadThatEndsEarlyIsNotLeftCounted(t *testing.T) {
	m, s := probeSession(t)
	defer m.Shutdown()
	defer m.Release("r1")

	pr := primeReader{reader{s: s}, time.Second}
	if _, err := pr.Do(routeros.Cmd{Path: "/system/resource/print"}); err == nil {
		t.Fatal("setup: the session is not connected, so the prime read should fail at once")
	}
	if n := s.primeInflight.Load(); n != 0 {
		t.Errorf("a prime read that never reached the router is still counted in flight (%d), "+
			"so this session would never be primed again", n)
	}
}

func TestPrimeSkipsASessionWithAReadStillOutstanding(t *testing.T) {
	src := readSource(t, "prime.go")
	if !contains(src, "if s.primeInflight.Load() > 0 { continue }") {
		t.Error("primeStats no longer skips a session whose earlier prime read is still with the " +
			"router, so a slow router collects a prime read every two seconds")
	}
}
