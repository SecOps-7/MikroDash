package collect

// The Go side of nodecheck/dns-fingerprint.test.js.
//
// The two suites assert the SAME behaviour against the two implementations, so
// they cannot drift apart silently. They were written asserting the OPPOSITE:
// a comment-only edit used to write the router and never reach the open page,
// because the fingerprint was a hand-listed tuple that omitted `comment`. The
// port found that, it was fixed in the live app, and these tests going red is
// how the port was told to follow. They now pin the fixed behaviour.

import (
	"testing"

	"mikrodash/internal/routeros"
)

// scriptedReader answers from whatever rows it currently holds, so a test can
// change the router's answer between ticks.
type scriptedReader struct {
	settings routeros.Reply
	static   []routeros.Reply
	reads    int
}

func (s *scriptedReader) Connected() bool { return true }

func (s *scriptedReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	s.reads++
	switch cmd.Path {
	case "/ip/dns/print":
		return []routeros.Reply{s.settings}, nil
	case "/ip/dns/static/print":
		return s.static, nil
	}
	return nil, nil
}

func baseRow() routeros.Reply {
	return routeros.Reply{".id": "*1", "name": "host.lan", "address": "198.51.100.7",
		"type": "A", "ttl": "1d", "disabled": "false", "comment": "before"}
}

// held still on purpose: cache-used is in the settings fingerprint, and letting
// it move would mask exactly what these tests measure.
func baseSettings() routeros.Reply {
	return routeros.Reply{"servers": "", "cache-size": "2048", "cache-used": "46"}
}

func TestDNSFingerprintCoversComment(t *testing.T) {
	r := &scriptedReader{settings: baseSettings(), static: []routeros.Reply{baseRow()}}
	var emits []string
	d := NewDNS(r, func(room, event string, payload any) { emits = append(emits, event) }, 10000)

	d.Tick()
	if len(emits) != 1 {
		t.Fatalf("the first tick emitted %d times, want 1", len(emits))
	}

	row := baseRow()
	row["comment"] = "after"
	r.static = []routeros.Reply{row}
	d.RefreshNow() // exactly what a res:save does

	if len(emits) != 2 {
		t.Errorf("a comment-only change emitted %d times, want 2 — `comment` is a "+
			"rendered column, so it must be in the fingerprint or the open page "+
			"keeps showing the old value after a save that really landed", len(emits))
	}
	// And the payload carries it, so the page shows what the router now holds.
	if got := d.Last().StaticEntries[0].Comment; got != "after" {
		t.Errorf("lastPayload comment = %q, want %q", got, "after")
	}
}

func TestDNSFingerprintCatchesAddress(t *testing.T) {
	r := &scriptedReader{settings: baseSettings(), static: []routeros.Reply{baseRow()}}
	var emits []string
	d := NewDNS(r, func(room, event string, payload any) { emits = append(emits, event) }, 10000)

	d.Tick()
	row := baseRow()
	row["address"] = "198.51.100.9"
	r.static = []routeros.Reply{row}
	d.RefreshNow()

	if len(emits) != 2 {
		t.Errorf("an address change emitted %d times, want 2", len(emits))
	}
}

// RefreshNow must re-read the static table even though it is normally read only
// every twelfth tick. Without the tick reset a save would leave the table
// showing the old row until the next config sweep — up to ten minutes on the
// default interval — which reads as a failed save.
func TestRefreshNowRereadsTheStaticTable(t *testing.T) {
	r := &scriptedReader{settings: baseSettings(), static: []routeros.Reply{baseRow()}}
	d := NewDNS(r, func(string, string, any) {}, 10000)
	d.Tick()

	row := baseRow()
	row["address"] = "198.51.100.9"
	r.static = []routeros.Reply{row}

	d.Tick() // an ordinary tick is NOT due a static read
	if got := d.Last().StaticEntries[0].Address; got != "198.51.100.7" {
		t.Errorf("an ordinary tick re-read the static table; address = %q", got)
	}
	d.RefreshNow()
	if got := d.Last().StaticEntries[0].Address; got != "198.51.100.9" {
		t.Errorf("RefreshNow did not re-read the static table; address = %q", got)
	}
}
