package collect

import (
	"strings"
	"sync"
	"testing"

	"mikrodash/internal/routeros"
)

// neighbourReader answers /ip/neighbor with two devices and every other menu
// with nothing.
type neighbourReader struct{}

func (neighbourReader) Connected() bool { return true }
func (neighbourReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	if strings.HasPrefix(cmd.Path, "/ip/neighbor") {
		return []routeros.Reply{
			{".id": "*1", "identity": "sw1", "address": "198.51.100.2", "mac-address": "02:00:00:00:00:01", "interface": "ether2"},
			{".id": "*2", "identity": "ap1", "address": "198.51.100.3", "mac-address": "02:00:00:00:00:02", "interface": "ether3"},
		}, nil
	}
	return nil, nil
}

// TOPOLOGY'S seen AND ping MAPS HAVE ONE OWNER AT A TIME (review 2026-09-19).
//
// BuildTopology writes and deletes in the collector's live seen and ping maps,
// and apply (the structure poll) and republish (the 3 s ping loop) both called
// it after letting go of the lock, while recordPing wrote the same map under
// it. Concurrent map writes are a fatal error, not a panic: the process exits.
// Run under -race this reports the race on the old code; a plain run is only a
// smoke test, since a map race need not fire.
func TestTopologyBuildsDoNotRaceThePingLoop(t *testing.T) {
	c := NewTopology(neighbourReader{}, Emit{}, nil, "r1", "lab", 30000)
	c.Tick() // one structure read, so republish has something to rebuild
	var wg sync.WaitGroup
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				switch g {
				case 0:
					c.apply(neighbourReader{}.Do(topoNeighborCmd))
				case 1:
					c.republish()
				case 2:
					rtt := 1.5
					c.recordPing("02:00:00:00:00:01", i%2 == 0, &rtt)
				}
			}
		}(g)
	}
	wg.Wait()
	if c.Last() == nil {
		t.Fatal("no topology was built")
	}
}
