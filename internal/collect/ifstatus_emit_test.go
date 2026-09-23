package collect

import (
	"sync"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// WHICH HALF THE DIRTY CHECK GATES.
//
// ── THE BUG IT WAS WRITTEN FOR ─────────────────────────────────────────────
//
// The check gated BOTH emits: a payload identical to the last one was dropped
// entirely. Measured on the operator's hAP AX3 at a one-second poll
// (2026-09-23) it fired about nine times in five minutes, and each time the
// Interfaces page's live chart lost that second — reported as "it seems to skip
// a tick, then there is no movement for that second". The instrumented log:
//
//	build gap=1001ms / SUPPRESSED (identical payload) / build gap=2002ms
//
// The premise expired when #59 put a per-second chart on this payload. "The
// rates are the same as last second" is exactly the point such a chart needs.
//
// So the RATES payload goes every tick, and the chrome half — names and up/down,
// which every page receives and which genuinely does not move second to second —
// keeps the suppression. This test tells the two apart: a fingerprint gate that
// crept back over the rates emit would look identical in every payload and
// differ only in how often one is sent, which no golden can see.
type sameReader struct{ conn bool }

func (r *sameReader) Connected() bool { return r.conn }

// Do answers the four menus with rows that NEVER change, which is the case the
// dirty check reacts to.
func (r *sameReader) Do(c routeros.Cmd) ([]routeros.Reply, error) {
	switch c.Path {
	case "/interface/print":
		return []routeros.Reply{{
			".id": "*1", "name": "ether1", "type": "ether",
			"running": "true", "disabled": "false", "mac-address": "02:00:00:00:00:01",
			"rx-byte": "1000", "tx-byte": "2000",
		}}, nil
	case "/ip/address/print":
		return []routeros.Reply{{"interface": "ether1", "address": "198.51.100.1/24"}}, nil
	case "/interface/ethernet/print":
		return []routeros.Reply{{"name": "ether1"}}, nil
	case "/interface/monitor-traffic":
		return []routeros.Reply{{
			"name": "ether1", "rx-bits-per-second": "1000000", "tx-bits-per-second": "2000000",
		}}, nil
	}
	return nil, nil
}

func TestAnIdenticalPayloadStillSendsTheRates(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	relay := hub.NewRelay(func(_ string, e hub.Named, _ any) {
		mu.Lock()
		seen[e.Name()]++
		mu.Unlock()
	})
	count := func(ev string) int {
		mu.Lock()
		defer mu.Unlock()
		return seen[ev]
	}

	s := NewIfStatus(&sameReader{conn: true}, relay, "r1", 1000)

	// Three ticks over rows that never change: the second and third are exactly
	// the case the suppression fires on.
	s.Tick()
	s.Tick()
	s.Tick()

	if got := count(EvIfstatusUpdate.Name()); got != 3 {
		t.Errorf("the rates payload was sent %d time(s) for 3 ticks. It must go EVERY tick: "+
			"a page plotting a point per second loses that second when one is withheld, "+
			"which is what an operator sees as a skipped tick", got)
	}
	// AND THE CHROME HALF IS STILL SUPPRESSED, or this has simply switched the
	// optimisation off. Names and up/down reach every viewer on every page and
	// genuinely do not move from one second to the next.
	if got := count(EvIfstatusNames.Name()); got != 1 {
		t.Errorf("the names payload was sent %d time(s) for 3 identical ticks, want 1 — "+
			"the dirty check has stopped covering the half it is for", got)
	}
}
