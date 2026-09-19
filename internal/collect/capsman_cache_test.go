package collect

import (
	"sync"
	"testing"
	"time"

	"mikrodash/internal/hub"
	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

// menuCounter counts reads per menu.
type menuCounter struct {
	mu     sync.Mutex
	byMenu map[string]int
}

func (m *menuCounter) Connected() bool { return true }
func (m *menuCounter) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	m.mu.Lock()
	m.byMenu[cmd.Path]++
	m.mu.Unlock()
	return []routeros.Reply{{".id": "*1", "identity": "cap-1", "address": "02:00:00:00:00:01"}}, nil
}

// TestCapsmanSharesTheRemoteCapRead. CAPsMAN read /interface/wifi/capsman/
// remote-cap past the cache while topology read it through it, and topology's
// comment said the two shared the read. They do now: one router read serves
// both within the interval (review loop).
func TestCapsmanSharesTheRemoteCapRead(t *testing.T) {
	r := &menuCounter{byMenu: map[string]int{}}
	c := roscache.New(r)
	caps := NewCapsman(r, hub.Relay{}, 10000)
	caps.UseCache(c)
	caps.build(nil)

	fields, _ := cacheableFields(topoCapsCmd)
	if _, err := c.Get(topoCapsCmd.Path, fields, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := r.byMenu[topoCapsCmd.Path]; n != 1 {
		t.Errorf("the remote-cap menu was read %d times for CAPsMAN and topology, want 1", n)
	}
}
