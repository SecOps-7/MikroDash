package collect

import (
	"testing"
	"time"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// TestAQuietRoutersTalkersStillArrive — an unchanged reading is re-sent at least
// every talkersHeartbeat.
//
// Without it a router nobody was using sent one payload and then nothing, and
// the Dashboard's Top Talkers card was called stale 23 seconds later (the poll
// plus the 20-second grace) on a router answering perfectly well. Reported on
// CHR Test and a hAP AC2.
func TestAQuietRoutersTalkersStillArrive(t *testing.T) {
	emits := 0
	c := NewTalkers(nil, hub.NewRelay(func(string, hub.Named, any) { emits++ }), 3000, 5)
	clock := time.Unix(1_800_000_000, 0)
	c.now = func() time.Time { return clock }
	poll := func() {
		c.apply([]routeros.Reply{}, nil) // nobody using bandwidth: the same empty list every time
		clock = clock.Add(3 * time.Second)
	}

	poll() // t=0: the first reading is always sent
	poll() // t=3
	poll() // t=6
	poll() // t=9
	if emits != 1 {
		t.Fatalf("an unchanged reading inside the heartbeat was sent: %d emits, want 1", emits)
	}
	poll() // t=12: ten seconds and more since the last send
	if emits != 2 {
		t.Fatalf("an unchanged reading 12s after the last send was suppressed: %d emits, want 2. "+
			"A quiet router's Top Talkers card goes stale.", emits)
	}

	// THE HEARTBEAT SITS INSIDE THE CARD'S SHORTEST STALE THRESHOLD: the fastest
	// poll talkers allows (1s) plus STALE_GRACE (20s, testdata/stale-tables.json).
	if talkersHeartbeat >= 21*time.Second {
		t.Errorf("talkersHeartbeat is %s; the card can go stale after 21s", talkersHeartbeat)
	}
}
