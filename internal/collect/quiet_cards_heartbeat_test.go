package collect

import (
	"testing"
	"time"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// countEmits is a relay that counts the sends of one event.
func countEmits(event string, n *int) Emit {
	return hub.NewRelay(func(_ string, e hub.Named, _ any) {
		if e.Name() == event {
			*n++
		}
	})
}

// TestAQuietNetwatchStillSendsItsHosts — an unchanged `netwatch:update` is re-sent
// at least every netwatchHeartbeat.
//
// Without it a router whose hosts never changed state sent one table and then
// nothing, and the Dashboard's NetWatch card went stale 90s after the page loaded.
func TestAQuietNetwatchStillSendsItsHosts(t *testing.T) {
	emits := 0
	n := NewNetwatch(quietLanReader{}, countEmits("netwatch:update", &emits), 30000)
	clock := time.Unix(1_800_000_000, 0)
	n.now = func() time.Time { return clock }
	rows := []routeros.Reply{{".id": "*1", "host": "198.51.100.1", "status": "up"}}

	n.apply(rows, nil) // the first reading is always sent
	clock = clock.Add(5 * time.Second)
	n.apply(rows, nil)
	if emits != 1 {
		t.Fatalf("an unchanged host table inside the heartbeat was sent: %d emits, want 1", emits)
	}
	clock = clock.Add(6 * time.Second) // 11s since the last send
	n.apply(rows, nil)
	if emits != 2 {
		t.Fatalf("an unchanged host table 11s after the last send was suppressed: %d emits, want 2. "+
			"A quiet router's NetWatch card goes stale.", emits)
	}

	// EVERY 60s READ MUST SEND, because the card's threshold is a fixed 90s.
	if netwatchHeartbeat > 60*time.Second {
		t.Errorf("netwatchHeartbeat is %s; a 60s read inside it is suppressed and the card can go stale", netwatchHeartbeat)
	}
}

// TestAQuietFirewallStillSendsItsRules — an unchanged `firewall:update` is re-sent
// at least every firewallHeartbeat.
//
// Without it a ruleset where no rule changed and no counter moved sent one
// payload and then nothing, and the Dashboard's Firewall card went stale.
func TestAQuietFirewallStillSendsItsRules(t *testing.T) {
	emits := 0
	f := NewFirewall(quietLanReader{}, countEmits("firewall:update", &emits), 10000)
	clock := time.Unix(1_800_000_000, 0)
	f.now = func() time.Time { return clock }

	f.buildAndEmit() // the first reading is always sent
	clock = clock.Add(5 * time.Second)
	f.buildAndEmit()
	if emits != 1 {
		t.Fatalf("an unchanged ruleset inside the heartbeat was sent: %d emits, want 1", emits)
	}
	clock = clock.Add(6 * time.Second) // 11s since the last send
	f.buildAndEmit()
	if emits != 2 {
		t.Fatalf("an unchanged ruleset 11s after the last send was suppressed: %d emits, want 2. "+
			"A quiet router's Firewall card goes stale.", emits)
	}

	// THE HEARTBEAT SITS INSIDE THE CARD'S SHORTEST STALE THRESHOLD: the card is
	// retuned to the payload's poll interval plus STALE_GRACE (20s), and the
	// payload is rebuilt at least once per poll.
	if firewallHeartbeat >= 20500*time.Millisecond {
		t.Errorf("firewallHeartbeat is %s; the card can go stale after 20.5s", firewallHeartbeat)
	}
}
