package collect

import (
	"testing"
	"time"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// quietLanReader answers every command with no rows: a network that, as far as
// the fingerprint can tell, never changes.
type quietLanReader struct{}

func (quietLanReader) Connected() bool                           { return true }
func (quietLanReader) Do(routeros.Cmd) ([]routeros.Reply, error) { return nil, nil }

// TestAQuietNetworkStillSendsItsOverview — an unchanged `lan:overview` is re-sent
// at least every dhcpNetworksHeartbeat.
//
// Without it a stable network sent one overview and then nothing. The
// Dashboard's Networks card was then kept fresh only by ping, and when the ping
// stream died silently on the hAP AX3 (2026-09-13 04:19) the card went stale
// minutes after every page load. A refresh "fixed" it by replaying the last,
// hours-old reading.
func TestAQuietNetworkStillSendsItsOverview(t *testing.T) {
	emits := 0
	// ONLY `lan:overview`: apply also sends `lan:wan`, which is not what the card
	// is kept fresh by.
	d := NewDHCPNetworks(quietLanReader{}, hub.NewRelay(func(_ string, e hub.Named, _ any) {
		if e.Name() == "lan:overview" {
			emits++
		}
	}), nil, "", 290000)
	clock := time.Unix(1_800_000_000, 0)
	d.now = func() time.Time { return clock }

	d.apply([]routeros.Reply{}, nil) // the first reading is always sent
	clock = clock.Add(5 * time.Second)
	d.apply([]routeros.Reply{}, nil)
	if emits != 1 {
		t.Fatalf("an unchanged overview inside the heartbeat was sent: %d emits, want 1", emits)
	}
	clock = clock.Add(6 * time.Second) // 11s since the last send
	d.apply([]routeros.Reply{}, nil)
	if emits != 2 {
		t.Fatalf("an unchanged overview 11s after the last send was suppressed: %d emits, want 2. "+
			"A quiet network's Networks card goes stale.", emits)
	}

	// THE HEARTBEAT SITS INSIDE THE CARD'S SHORTEST STALE THRESHOLD: the card is
	// retuned to the payload's poll interval plus STALE_GRACE (20s,
	// testdata/stale-tables.json), and the fastest poll this collector allows is
	// 500ms.
	if dhcpNetworksHeartbeat >= 20500*time.Millisecond {
		t.Errorf("dhcpNetworksHeartbeat is %s; the card can go stale after 20.5s", dhcpNetworksHeartbeat)
	}
}
