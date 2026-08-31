package collect

// The global `conn:update` OMITS the four heavy indexes; the replay CARRIES them.
//
// ── THE DEFECT THIS PINS ────────────────────────────────────────────────────
//
// The live collector builds the light payload with `delete emitPayload.countryDests`
// and three more, which REMOVES the keys. The port set the fields to nil, and a
// nil Go map marshals as `null` — so the port sent `"countryDests": null` where
// the live app sends no key at all.
//
// The port therefore had ONE conn:update key set where the live app has TWO, which
// is exactly what `tools/live-socket-diff.js` reported on 2026-08-28 as "a second
// payload shape on the live side that the port does not produce".
//
// ── WHY THE OBVIOUS FIX IS WRONG ────────────────────────────────────────────
//
// `omitempty` on the four fields would drop them from the REPLAY too, whenever
// the maps were empty. The live `lastPayload` assigns all four unconditionally,
// so the Connections page would open on a payload missing keys the live one has.
// A struct tag cannot say "absent in one emit, present in the other".

import (
	"encoding/json"
	"testing"

	"mikrodash/internal/routeros"
)

func keysOf(t *testing.T, v any) map[string]bool {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func TestTheGlobalEmitOmitsTheHeavyKeysRatherThanNullingThem(t *testing.T) {
	emitted, replay := tickEmit(t)

	// THE REPLAY carries all four, and its maps are EMPTY — which is their real
	// state when nobody has the Connections page open, and the case `omitempty`
	// would drop. The live builder declares `const countryDests = {}` and fills
	// it only when the room has an occupant; this port initialises all four the
	// same way, so `{}` is what goes on the wire.
	//
	// Populated maps would let `omitempty` pass: it drops an empty map and keeps
	// a full one, so a populated fixture cannot tell the tag from the wrapper.
	// That mutation survived the first version of this file.
	if replay == nil {
		t.Fatal("the collector kept no payload to replay")
	}
	if replay.CountryDests == nil || len(replay.CountryDests) != 0 {
		t.Fatalf("this fixture was meant to leave countryDests empty and it is %v — "+
			"the omitempty mutation cannot be distinguished from a populated map",
			replay.CountryDests)
	}
	replayKeys := keysOf(t, replay)
	for _, k := range connsHeavyKeys {
		if !replayKeys[k] {
			t.Errorf("the replay payload is missing %s — the Connections page opens on it", k)
		}
	}

	// THE GLOBAL EMIT IS TAKEN FROM Tick, NOT BUILT HERE.
	//
	// The first version of this test built the light payload itself and wrapped
	// it in connsLight — which asserts the wrapper works and says nothing about
	// whether the collector uses it. Reverting the emit to `&light`, the exact
	// defect this file exists for, SURVIVED. A test that constructs the thing it
	// is checking is a seam that bypasses the path it stands in for.
	lightKeys := keysOf(t, emitted)
	for _, k := range connsHeavyKeys {
		if lightKeys[k] {
			t.Errorf("the global emit carries %s. The live side DELETES it; nilling the field "+
				"sends `null`, which is a key the live payload does not have.", k)
		}
	}

	// AND NOTHING ELSE WAS DROPPED. The wrapper removes four keys and must not
	// touch the rest — it round-trips through a map, so a bug there would be
	// silent and total. Both sides come from ONE tick, so the arithmetic holds.
	for k := range replayKeys {
		heavy := false
		for _, h := range connsHeavyKeys {
			if k == h {
				heavy = true
			}
		}
		if !heavy && !lightKeys[k] {
			t.Errorf("the global emit lost %s, which is not one of the heavy indexes", k)
		}
	}
	if len(lightKeys)+len(connsHeavyKeys) != len(replayKeys) {
		t.Errorf("light has %d keys, replay %d, and %d were meant to be removed",
			len(lightKeys), len(replayKeys), len(connsHeavyKeys))
	}
}

// tickEmit drives a real Connections collector and returns what it passed to
// `emit` for `conn:update`. Two rows are enough: the payload only has to be
// non-empty and to change, so the dirty-check lets the emit through.
func tickEmit(t *testing.T) (emitted any, replay *ConnsPayload) {
	t.Helper()
	ros := fakeReader{rows: map[string][]routeros.Reply{
		"/ip/firewall/connection/print": {
			{".id": "*1", "src-address": "10.0.0.5", "dst-address": "198.51.100.9",
				"dst-port": "443", "protocol": "tcp"},
		},
	}}
	var got any
	seen := 0
	emit := func(room, event string, payload any) {
		if event == "conn:update" {
			got = payload
			seen++
		}
	}
	c := NewConnections(ros, emit, nil, nil, nil, 3000)
	c.Tick()
	if seen != 1 {
		t.Fatalf("Tick emitted conn:update %d times, want 1 — the probe is not exercising "+
			"the emit path and anything it goes on to assert is meaningless", seen)
	}
	// BOTH FROM ONE TICK. The replay is what `ws.go` sends on page focus —
	// `Conns().Last()` — so comparing it against a payload this test wrote by
	// hand would compare two different things, and the key ARITHMETIC below
	// would be nonsense. It was, at first: 11 + 4 != 14.
	return got, c.Last()
}

func TestTheWrapperCarriesAFieldNobodyListed(t *testing.T) {
	// `pollMs` is declared AFTER the four heavy indexes, so a hand-written
	// literal that stopped at them would drop it.
	p := &ConnsPayload{TS: 1, PollMs: 30000, Processed: 7}
	keys := keysOf(t, connsLight{p})
	for _, k := range []string{"pollMs", "processed", "total"} {
		if !keys[k] {
			t.Errorf("the light payload lost %s", k)
		}
	}
}
