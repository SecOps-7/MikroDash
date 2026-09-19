package session

import (
	"testing"

	"mikrodash/internal/collect"
	"mikrodash/internal/collection"
)

// A COLLECTOR SWITCH SAVED IN THE DEVICE DIALOG REACHES THE LIVE SESSION.
//
// `eff` was resolved once when the session was built, so a switch saved later
// did nothing until the session was rebuilt, which for a router held for alerting
// or history meant a restart. The dialog promised a reconnect that never came.
func TestASavedCollectorSwitchReachesTheLiveSession(t *testing.T) {
	s := &Session{RouterID: "r1"}
	s.dns = collect.NewDNS(reader{s: s}, noEmit, 4000)
	s.eff.Store(&collection.Resolved{
		Enabled: map[string]bool{"dns": true}, Poll: map[string]int{"dns": 4000},
	})
	m := &Manager{live: map[string]*Session{"r1": s}}

	if m.ApplyCollection("r1", *s.conf()) {
		t.Error("saving an unchanged config reported a change, so every router save " +
			"would re-announce collection:config")
	}

	off := collection.Resolved{
		Enabled: map[string]bool{"dns": false}, Poll: map[string]int{"dns": 9000},
	}
	if !m.ApplyCollection("r1", off) {
		t.Fatal("switching dns off reported no change")
	}
	if s.CollectorEnabled("dns") {
		t.Error("dns is still enabled on the live session after it was switched off")
	}
	if s.Collection().Enabled["dns"] {
		t.Error("collection:config for this session still says dns is on")
	}
	if got := s.dns.PollMs(); got != 9000 {
		t.Errorf("a saved dns interval of 9000 ms left the collector at %d", got)
	}

	on := collection.Resolved{
		Enabled: map[string]bool{"dns": true}, Poll: map[string]int{"dns": 9000},
	}
	if !m.ApplyCollection("r1", on) || !s.CollectorEnabled("dns") {
		t.Error("switching dns back on did not reach the live session")
	}

	if m.ApplyCollection("not-live", off) {
		t.Error("a router with no live session reported a change")
	}
}
