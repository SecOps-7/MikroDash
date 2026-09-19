package collect

import (
	"errors"
	"sync"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// v6Reader answers /ipv6/settings with a fixed error and counts the attempts.
type v6Reader struct {
	mu    sync.Mutex
	err   error
	tries int
}

func (r *v6Reader) Connected() bool { return true }
func (r *v6Reader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	if cmd.Path == "/ipv6/settings/print" {
		r.mu.Lock()
		r.tries++
		r.mu.Unlock()
		return nil, r.err
	}
	return nil, nil
}

// TestAnAbsentIPv6MenuIsProbedOncePerConnection. ProbeV6 latched only on an
// answer, so a router without /ipv6/settings was asked again on every counter
// delivery, once a poll, where its comment promised once a visit. A menu the
// router says it does not have is latched now; a transport failure, the case
// the latch was loosened for, still retries (review loop).
func TestAnAbsentIPv6MenuIsProbedOncePerConnection(t *testing.T) {
	absent := &v6Reader{err: errors.New("from RouterOS device: no such command prefix")}
	f := NewFirewall(absent, hub.Relay{}, 5000)
	for i := 0; i < 3; i++ {
		f.ProbeV6()
	}
	if absent.tries != 1 {
		t.Errorf("an absent menu was asked %d times in one connection, want 1", absent.tries)
	}

	// THE CONTROL: not connected is not an answer, and is asked again.
	down := &v6Reader{err: errors.New("routeros: not connected")}
	f = NewFirewall(down, hub.Relay{}, 5000)
	for i := 0; i < 3; i++ {
		f.ProbeV6()
	}
	if down.tries != 3 {
		t.Errorf("a transport failure was retried %d times, want 3", down.tries)
	}
}
