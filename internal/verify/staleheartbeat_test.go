package verify

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryStaleCardHasAHeartbeat.
//
// ── THE BUG THIS EXISTS FOR ─────────────────────────────────────────────────
//
// A card goes stale when nothing has arrived for it inside its threshold
// (web/src/stale.ts, which uses the payload's own `pollMs` plus a 20s grace when
// the collector reports one). A collector that suppresses an unchanged payload
// and has no heartbeat therefore sends ONE frame and then nothing, and its card
// is called stale on a router answering perfectly well.
//
// Reported by the operator on 2026-09-16 for the Packages card, and five others
// were in the same state: dns, bridges, capsman, vlans and vpn. The six that
// already had a heartbeat got one the same way - each after somebody noticed a
// card going stale - which is why this is a gate rather than a sixth fix.
//
// ── WHAT IT ASSERTS ─────────────────────────────────────────────────────────
//
// For every card in the stale table: the collector that emits its event either
// declares a heartbeat, or is recorded below as emitting every reading. It fails
// in BOTH directions, so an exemption cannot outlive the reason for it.
func TestEveryStaleCardHasAHeartbeat(t *testing.T) {
	root := repoRoot(t)

	// Collectors that send EVERY reading, so nothing can be suppressed and no
	// heartbeat is needed. Each entry says what makes that true.
	noHeartbeat := map[string]string{
		"routing:update":   "emits every reading: BuildRouting has no fingerprint",
		"topology:update":  "emits every reading: the topology payload is not fingerprinted",
		"wireless:update":  "emits every reading; only the PTR name patch is suppressed",
		"system:update":    "fingerprinted, but `uptimeRaw` moves every reading so it never repeats",
		"lan:overview":     "dhcpNetworks: has dhcpNetworksHeartbeat",
		"conn:update":      "conns: has connsHeartbeat",
		"bandwidth:update": "bandwidth: has bandwidthHeartbeat",
	}

	var tables struct {
		Cards []struct {
			CardID    string `json:"cardId"`
			Event     string `json:"event"`
			Threshold int    `json:"threshold"`
		} `json:"cards"`
	}
	raw := mustRead(t, filepath.Join(root, "testdata", "stale-tables.json"))
	if err := json.Unmarshal([]byte(raw), &tables); err != nil {
		t.Fatalf("stale-tables.json: %v", err)
	}
	if len(tables.Cards) == 0 {
		t.Fatal("the stale table holds no cards, so this check proves nothing")
	}

	dir := filepath.Join(root, "internal", "collect")
	files := collectGoFiles(t, dir)
	eventsSrc = mustRead(t, filepath.Join(dir, "events.go"))
	// The file that emits an event is the one holding its declared name beside an
	// `.Emit(`, which is the literal every room gate here reads too.
	emitter := func(event string) (string, string) {
		for _, name := range files {
			src := mustRead(t, filepath.Join(dir, name))
			if !strings.Contains(src, `"`+event+`"`) && !emitsEvent(src, event) {
				continue
			}
			if emitsEvent(src, event) {
				return name, src
			}
		}
		return "", ""
	}

	seen := map[string]bool{}
	checked := 0
	for _, c := range tables.Cards {
		if why, ok := noHeartbeat[c.Event]; ok {
			seen[c.Event] = true
			if why == "" {
				t.Errorf("%s is recorded as needing no heartbeat with no reason", c.Event)
			}
			continue
		}
		file, src := emitter(c.Event)
		if file == "" {
			t.Errorf("no file in internal/collect emits %q, which card %s waits for",
				c.Event, c.CardID)
			continue
		}
		checked++
		// USED, not merely declared. A file keeps its `xHeartbeat` constant when
		// the spec field that applies it is deleted, so looking for the name alone
		// passes against exactly the bug this gate exists for - which is what the
		// first version of it did when the mutation was run.
		if !heartbeatUsed(src) {
			t.Errorf("card %s waits for %q with a %dms threshold, and %s declares no heartbeat. "+
				"An unchanged payload is suppressed, so the card goes stale on a router that is "+
				"answering. Give the collector a heartbeat under the threshold, or record here "+
				"why it sends every reading.", c.CardID, c.Event, c.Threshold, file)
		}
	}
	if checked == 0 {
		t.Fatal("no card was checked against a collector; the emit scan has stopped matching")
	}
	for event := range noHeartbeat {
		if !seen[event] {
			t.Errorf("%q is recorded as needing no heartbeat and no card waits for it any more; "+
				"remove the entry", event)
		}
	}
}

// heartbeatUsed reports whether a heartbeat is APPLIED in this source: handed to
// the table core as a spec field, or compared against the time since the last
// emit by a collector that gates its own.
func heartbeatUsed(src string) bool {
	return regexp.MustCompile(`heartbeat:\s*\w*[Hh]eartbeat`).MatchString(src) ||
		regexp.MustCompile(`(?:<|>=)\s*\w*[Hh]eartbeat\b`).MatchString(src)
}

// emitsEvent reports whether this source declares the event and emits it.
func emitsEvent(src, event string) bool {
	decl := regexp.MustCompile(`(?m)^\s*(Ev\w+)\s*=\s*hub\.Declare\[[^\]]+\]\("` + regexp.QuoteMeta(event) + `"\)`)
	m := decl.FindStringSubmatch(mustEvents(src))
	if m == nil {
		return false
	}
	return strings.Contains(src, m[1]+".Emit(")
}

// mustEvents is the events file, which declares every collector event; the emit
// itself lives in the collector's own file.
var eventsSrc string

func mustEvents(src string) string {
	if eventsSrc != "" {
		return eventsSrc
	}
	return src
}
