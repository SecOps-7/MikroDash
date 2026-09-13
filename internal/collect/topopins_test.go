package collect

import (
	"testing"
)

// THE OPERATOR'S OWN CABLING, OVER THE INFERENCE.
//
// ── WHY PINNING EXISTS ──────────────────────────────────────────────────────
//
// `resolveParents` can only attribute devices on a port when EXACTLY ONE of them
// was seen over LLDP — that is the one protocol a conformant bridge must not
// forward, so an LLDP neighbour is provably direct. A switch that forwards no
// LLDP is therefore invisible to the rule: everything behind it arrives on the
// port the switch is plugged into and looks directly attached, and no table on
// the manager says otherwise.
//
// Fixing that automatically would mean reading the neighbour tables of the
// devices in between, which means adding them to MikroDash. Until somebody does,
// this is what lets them write down what they can see with their eyes.
//
// `addedSinceNode` in fixture_test.go names this test: the golden corpus has
// three node shapes and only the neighbour one carries `pinned`, so it cannot
// carry the proof.

func node(key string, via ...string) *TopoNeighbor {
	return &TopoNeighbor{Key: key, Kind: "neighbor", Name: key, Via: via}
}

func TestPinnedParentsOverrideTheInference(t *testing.T) {
	// Three devices on one port and NOT ONE OF THEM SEEN OVER LLDP: the shape a
	// SwOS switch produces, and the shape the inference correctly refuses to
	// guess at.
	build := func(pins map[string]string) map[string]*TopoNeighbor {
		byKey := map[string]*TopoNeighbor{
			"sw":     node("sw", "mndp"),
			"ap1":    node("ap1", "mndp"),
			"ap2":    node("ap2", "mndp"),
			"direct": node("direct", "lldp"),
		}
		order := []string{"sw", "ap1", "ap2", "direct"}
		hosts := []hostEntry{
			{MAC: "", Port: ""}, // never matched; the nodes carry no MAC
		}
		for _, k := range order {
			byKey[k].Ifaces = []string{"ether8"}
		}
		byKey["direct"].Ifaces = []string{"ether1"}
		resolveParents(byKey, order, hosts, pins)
		return byKey
	}

	// ── without pins, everything on the shared port stays flat ──────────────
	flat := build(nil)
	for _, k := range []string{"sw", "ap1", "ap2"} {
		if flat[k].Parent != nil {
			t.Errorf("%s was attributed to %q with no LLDP neighbour on the port — "+
				"the inference guessed, which is what it must never do", k, *flat[k].Parent)
		}
		if flat[k].Pinned {
			t.Errorf("%s reads as pinned and nothing was pinned", k)
		}
	}

	// ── with pins, the two access points hang off the switch ────────────────
	pinned := build(map[string]string{"ap1": "sw", "ap2": "sw"})
	for _, k := range []string{"ap1", "ap2"} {
		if pinned[k].Parent == nil || *pinned[k].Parent != "sw" {
			t.Fatalf("%s was not pinned to sw: %v", k, pinned[k].Parent)
		}
		if !pinned[k].Pinned {
			t.Errorf("%s is pinned but does not say so, so the page cannot tell a "+
				"declared link from an inferred one", k)
		}
	}
	if pinned["sw"].Parent != nil {
		t.Error("the switch itself was given a parent")
	}

	// ── "core" IS THE OPPOSITE PIN ──────────────────────────────────────────
	//
	// It overrules an inference that put a device behind something, which is the
	// case the graph has no other way to express: a node with no parent hangs
	// off the core, and that is exactly what "no, it really is direct" means.
	onPort := map[string]*TopoNeighbor{
		"sw":  node("sw", "lldp"),
		"ap1": node("ap1", "mndp"),
	}
	order := []string{"sw", "ap1"}
	for _, k := range order {
		onPort[k].Ifaces = []string{"ether8"}
	}
	resolveParents(onPort, order, nil, nil)
	if onPort["ap1"].Parent == nil || *onPort["ap1"].Parent != "sw" {
		t.Fatal("the LLDP rule stopped attributing, so the case below proves nothing")
	}
	resolveParents(onPort, order, nil, map[string]string{"ap1": "core"})
	if onPort["ap1"].Parent != nil {
		t.Errorf("a pin to core left %v in place", *onPort["ap1"].Parent)
	}
	if !onPort["ap1"].Pinned {
		t.Error("a pin to core does not read as pinned")
	}

	// ── A PIN CANNOT NAME A LOOP ────────────────────────────────────────────
	//
	// It is typed into a picker by a person, which is exactly the input that can.
	// The cycle check already guarded the inference; pins are applied BEFORE it
	// so they are covered by the same pass rather than by a second one.
	loop := map[string]*TopoNeighbor{"a": node("a", "mndp"), "b": node("b", "mndp")}
	resolveParents(loop, []string{"a", "b"}, nil, map[string]string{"a": "b", "b": "a"})
	broken := 0
	for _, k := range []string{"a", "b"} {
		if loop[k].Parent == nil {
			broken++
		}
	}
	if broken == 0 {
		t.Error("a -> b -> a survived; the graph would recurse when it is laid out")
	}

	// ── A PIN NAMING A DEVICE THAT IS NOT HERE IS IGNORED, NOT DROPPED ──────
	//
	// Ignored by the build, kept in the document: the device may be switched off
	// today and back tomorrow, and losing what somebody wrote down because a
	// switch was unplugged is the wrong trade.
	absent := build(map[string]string{"ap1": "not-on-this-graph"})
	if absent["ap1"].Parent != nil {
		t.Errorf("a pin to an absent node produced a parent: %v", *absent["ap1"].Parent)
	}
	if absent["ap1"].Pinned {
		t.Error("a pin that could not be applied still reads as pinned")
	}
}
