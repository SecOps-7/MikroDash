package roscache

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// slowPusher takes a moment to open, as a router does, which is the window
// two reopens used to share.
type slowPusher struct{ pusher }

func (s *slowPusher) Stream(cmd routeros.Cmd, onRow func(routeros.Reply)) (func(), error) {
	time.Sleep(time.Millisecond)
	return s.pusher.Stream(cmd, onRow)
}

// TestConcurrentJoinsOpenOneChannel. `reopenFor` decided under the fill's lock
// and opened outside it, so two holders joining one shared menu at once (or a
// join racing a narrowing release) each opened a channel, and the second
// overwrote the first's stop: a channel nobody could close (review loop).
func TestConcurrentJoinsOpenOneChannel(t *testing.T) {
	for round := 0; round < 20; round++ {
		p := &slowPusher{}
		c := New(p)
		var wg sync.WaitGroup
		var mu sync.Mutex
		var releases []func()
		for g := 0; g < 6; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				rel, err := c.JoinStream(Join{
					Menu:  "/interface/monitor-traffic",
					Cmd:   routeros.Cmd{Path: "/interface/monitor-traffic", Args: []string{"=interface=e" + strconv.Itoa(g)}},
					KeyOf: byName, Merge: mergeIfaces,
				})
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				releases = append(releases, rel)
				mu.Unlock()
			}(g)
		}
		wg.Wait()
		opens, stops, _ := p.counts()
		if open := opens - stops; open != 1 {
			t.Fatalf("round %d: %d opens and %d stops leave %d channels open, want 1", round, opens, stops, open)
		}
		for _, rel := range releases {
			rel()
		}
		if opens, stops, _ := p.counts(); opens != stops {
			t.Fatalf("round %d: after every release, %d opens and %d stops", round, opens, stops)
		}
	}
}
