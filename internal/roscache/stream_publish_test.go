package roscache

// A FINISHED ROUND REACHES ITS SUBSCRIBER AT ONCE (2026-09-20).
//
// A streamed menu's rows are already in hand when the round ends, and the
// scheduler used to decide when anybody was told: it asks each menu on the
// subscription's cadence, which is the right clock for a read that happens when
// it asks, and the wrong one for data that has already arrived. Stacked on top
// of a round boundary that was itself an interval wide, that made a NetWatch
// recovery reach the alert rules two minutes after the router saw it — the
// operator was told at 11:15:32 about a host the router had back at 11:13:30.

import (
	"sync"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// byRouterOSID keys a table's rows the way every `/print` row is keyed.
func byRouterOSID(r routeros.Reply) string { return r[".id"] }

func TestACompletedRoundIsDeliveredWithoutWaitingForTheScheduler(t *testing.T) {
	p := &pusher{}
	c := New(p)

	var mu sync.Mutex
	var deliveries [][]routeros.Reply
	release := c.Subscribe("/tool/netwatch/print", nil,
		func() time.Duration { return time.Minute },
		func(rows []routeros.Reply, err error) {
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				deliveries = append(deliveries, rows)
			}
		})
	defer release()

	// The round gap is tiny here so the test does not wait out a real second;
	// the staleness window stays wide, as it does for a minute-long menu.
	stop, err := fillTimed(t, c, "/tool/netwatch/print", byRouterOSID,
		5*time.Millisecond, time.Millisecond, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// One round of a three-host table, as the router re-prints it.
	p.push(
		routeros.Reply{".id": "*1", "host": "10.255.255.1", "status": "up"},
		routeros.Reply{".id": "*2", "host": "84.200.69.80", "status": "up"},
		routeros.Reply{".id": "*3", "host": "84.200.70.40", "status": "down"},
	)

	// NOTHING RUNS THE SCHEDULER HERE, which is the point: the only thing that
	// can deliver is the round closing.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(deliveries)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a completed round was never delivered: the subscriber waits for the " +
				"scheduler's next tick, which is a whole interval after the rows arrived")
		}
		time.Sleep(2 * time.Millisecond)
	}

	mu.Lock()
	got := deliveries[0]
	mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("the delivered round has %d rows, want the 3 pushed", len(got))
	}
	down := 0
	for _, r := range got {
		if r["status"] == "down" {
			down++
		}
	}
	if down != 1 {
		t.Errorf("the delivered rows do not carry the round's own statuses: %v", got)
	}
}

// A ROUND ANSWERS FROM THE STREAM, NEVER FROM A READ. The delivery path goes
// through `Get`, and a `Get` that fell back to the router would turn every
// round into a command — the opposite of what streaming is for.
func TestDeliveringARoundSendsTheRouterNothing(t *testing.T) {
	p := &pusher{}
	c := New(p)
	release := c.Subscribe("/tool/netwatch/print", nil,
		func() time.Duration { return time.Minute },
		func([]routeros.Reply, error) {})
	defer release()

	stop, err := fillTimed(t, c, "/tool/netwatch/print", byRouterOSID,
		5*time.Millisecond, time.Millisecond, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	p.push(routeros.Reply{".id": "*1", "host": "10.255.255.1", "status": "up"})
	time.Sleep(60 * time.Millisecond)

	if _, _, reads := p.counts(); reads != 0 {
		t.Errorf("%d router read(s) while delivering a streamed round", reads)
	}
}
