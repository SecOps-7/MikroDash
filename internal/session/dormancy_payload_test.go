package session

// The reflection lookup, against REAL collector payload types.
//
// These adapters let `collection.PayloadEmptyBy` — the rule the live corpus
// pins — read a struct instead of a map. What has to be checked here is the
// LOOKUP, and specifically its middle outcome: a key that is missing, is not a
// slice, or is a nil slice must report NOT A LIST, so the collector is left
// alone rather than judged empty.

import (
	"testing"

	"mikrodash/internal/collect"
	"mikrodash/internal/collection"
)

func TestTheLookupFindsAListByItsJSONTag(t *testing.T) {
	p := &collect.NetwatchPayload{TS: 7, Hosts: []collect.NetwatchHost{}}
	look := payloadLookup(p)

	if n, ok := look("hosts"); !ok || n != 0 {
		t.Errorf(`look("hosts") = %d,%v — an empty slice is a list of length 0`, n, ok)
	}
	// THE MIDDLE OUTCOME. A key the payload does not have is not emptiness.
	if _, ok := look("nosuchkey"); ok {
		t.Error("a key this payload has no field for reported as a list")
	}
	// A NON-SLICE FIELD. `ts` is a real json tag on this struct and an int64.
	if _, ok := look("ts"); ok {
		t.Error("the `ts` field reported as a list")
	}
	if got := payloadTS(p); got != 7 {
		t.Errorf("payloadTS = %d, want 7", got)
	}
}

// TestANilSliceIsNotAnEmptyList — the arm that would sleep a collector on its
// first tick.
//
// A JavaScript collector that has not built its list yet has `undefined` there,
// and `payloadEmpty` skips a key whose value is not an array. A nil Go slice is
// that same "not built yet". Reading it as an empty list would put a collector
// to sleep before it has ever answered.
func TestANilSliceIsNotAnEmptyList(t *testing.T) {
	nilList := &collect.NetwatchPayload{TS: 1} // Hosts is nil
	if _, ok := payloadLookup(nilList)("hosts"); ok {
		t.Error("a nil slice reported as a list; it is `not built yet`, not `empty`")
	}
	if collection.PayloadEmptyBy(payloadLookup(nilList), []string{"hosts"}) {
		t.Error("a payload whose list is nil was judged EMPTY — that sleeps a collector on " +
			"its first tick, before it has answered anything")
	}
	// And the contrast: an actual empty slice IS empty.
	empty := &collect.NetwatchPayload{TS: 1, Hosts: []collect.NetwatchHost{}}
	if !collection.PayloadEmptyBy(payloadLookup(empty), []string{"hosts"}) {
		t.Error("an empty slice was not judged empty")
	}
}

// TestTheTagNameIsSplitFromItsOptions.
//
// A json tag can be `hosts,omitempty`, and the name is everything before the
// comma. NO PAYLOAD IN `internal/collect` USES ONE ON A SLICE TODAY — measured —
// so this branch has no real exerciser and would rot unwatched. A local struct
// gives it one, and if a collector ever adds `,omitempty` to a list the registry
// names, the behaviour is already pinned rather than discovered.
func TestTheTagNameIsSplitFromItsOptions(t *testing.T) {
	type withOptions struct {
		Hosts []int `json:"hosts,omitempty"`
		TS    int64 `json:"ts"`
	}
	look := payloadLookup(&withOptions{Hosts: []int{}, TS: 3})
	if n, ok := look("hosts"); !ok || n != 0 {
		t.Errorf(`a "hosts,omitempty" tag did not match the key "hosts" (%d,%v)`, n, ok)
	}
	// And the whole tag is NOT the name.
	if _, ok := look("hosts,omitempty"); ok {
		t.Error("the raw tag matched as a key name")
	}
	if got := payloadTS(&withOptions{TS: 3}); got != 3 {
		t.Errorf("payloadTS = %d, want 3", got)
	}
}

// TestSeveralKeysBehaveAsTheLiveRuleDoes — the multi-key rule, through the real
// rule, over a real payload type.
//
// The keys here are chosen to exercise SEVERAL LISTS, not to reproduce the
// registry's emptyKey for `vlans` (which names only `vlans`). What is under test
// is the rule; which keys a collector declares is the registry's business and is
// checked by `internal/collection`.
func TestSeveralKeysBehaveAsTheLiveRuleDoes(t *testing.T) {
	keys := []string{"vlans", "bridgeVlans"}

	both := &collect.VlansPayload{TS: 1, Vlans: []collect.Vlan{}, BridgeVlans: []collect.BridgeVlan{}}
	if !collection.PayloadEmptyBy(payloadLookup(both), keys) {
		t.Error("two empty lists were not judged empty")
	}
	one := &collect.VlansPayload{TS: 1, Vlans: []collect.Vlan{{}}, BridgeVlans: []collect.BridgeVlan{}}
	if collection.PayloadEmptyBy(payloadLookup(one), keys) {
		t.Error("a list with a row did not save it")
	}
	// ONE READABLE, ONE NOT: the readable one decides.
	onlyOne := &collect.VlansPayload{TS: 1, Vlans: []collect.Vlan{}} // BridgeVlans nil
	if !collection.PayloadEmptyBy(payloadLookup(onlyOne), keys) {
		t.Error("an unreadable sibling prevented the verdict")
	}
	// NOTHING READABLE is not emptiness.
	if collection.PayloadEmptyBy(payloadLookup(&collect.VlansPayload{TS: 1}), keys) {
		t.Error("a payload with no lists built yet was judged empty")
	}
}

// TestUnsupportedIsStrictlyFalse.
//
// The live test is `p.available === false`: an ABSENT `available` is not
// unsupported, because a collector with no capability question never sets one.
// Only an explicit false earns the MAXIMUM backoff rather than the base one.
//
// ── A TRAP THIS PORT CANNOT FULLY CLOSE, RECORDED ───────────────────────────
//
// Every `available` in `internal/collect` is a PLAIN BOOL, so a Go payload
// cannot distinguish "absent" from "false" the way the JavaScript one can — an
// unset field reads as false and therefore as unsupported. That is safe only
// because every collector carrying the field sets it before emitting, which is
// the same convention the live side follows ("the convention ppp, bridges,
// capsman, dns, packages, wan… `available: false`"). A collector that ever emits
// before deciding would be put to sleep at the maximum backoff on its first
// payload.
//
// Pinned here so the assumption is written down where it is relied on.
func TestUnsupportedIsStrictlyFalse(t *testing.T) {
	// A payload with NO `available` field at all — netwatch has none.
	if payloadUnsupported(&collect.NetwatchPayload{TS: 1}) {
		t.Error("a payload with no `available` field read as unsupported")
	}
	// One that has it, both ways.
	if !payloadUnsupported(&collect.PPPPayload{TS: 1, Available: false}) {
		t.Error("available:false did not read as unsupported")
	}
	if payloadUnsupported(&collect.PPPPayload{TS: 1, Available: true}) {
		t.Error("available:true read as unsupported")
	}
}

// TestTheLookupAgreesWithTheMapLookup.
//
// The two adapters feed one rule, and the map one is pinned directly by the live
// corpus. Driving the same logical payload through both is what carries that
// corpus over to the reflection side.
func TestTheLookupAgreesWithTheMapLookup(t *testing.T) {
	keys := []string{"hosts"}
	for _, c := range []struct {
		why   string
		typed *collect.NetwatchPayload
		asMap map[string]any
	}{
		{"an empty list", &collect.NetwatchPayload{TS: 1, Hosts: []collect.NetwatchHost{}},
			map[string]any{"ts": 1.0, "hosts": []any{}}},
		{"a list with a row", &collect.NetwatchPayload{TS: 1, Hosts: []collect.NetwatchHost{{}}},
			map[string]any{"ts": 1.0, "hosts": []any{map[string]any{}}}},
		{"no list built yet", &collect.NetwatchPayload{TS: 1},
			map[string]any{"ts": 1.0}},
	} {
		c := c
		t.Run(c.why, func(t *testing.T) {
			viaStruct := collection.PayloadEmptyBy(payloadLookup(c.typed), keys)
			viaMap := collection.PayloadEmpty(c.asMap, keys)
			if viaStruct != viaMap {
				t.Errorf("the struct lookup says %v and the map lookup says %v — the two "+
					"adapters feed ONE rule and must not disagree", viaStruct, viaMap)
			}
		})
	}
}
