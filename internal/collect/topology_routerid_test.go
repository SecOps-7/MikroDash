package collect

// The router id reaches BOTH payloads this collector can emit.
//
// `topology:update` shipped without `routerId` for the life of this port: the
// field was declared, tagged `omitempty`, and never assigned, because
// `NewTopology` had no parameter to be told one through. See the note on
// `TopologyPayload.RouterID`.
//
// Two payloads carry it, and only one of them is exercised by the fixture
// differential — the ordinary one. The permission-denied literal is built by
// hand in `Tick`, and a mutation removing `RouterID` from it SURVIVED the whole
// suite when this fix first landed. This is that mutation's test.
//
// ── THE DENIED PAYLOAD IS A PORT-ONLY SHAPE, AND THAT IS RECORDED ELSEWHERE ─
//
// The live collector does NOT emit on denial. `_pollOnce` sets
// `_permissionDenied`, warns, and returns; every later poll returns at the top.
// `permissionDenied` reaches the browser only if a rebuild was already in
// flight. The port emits one immediately instead.
//
// That difference is not settled here — it is a fidelity question with its own
// entry in the port record. This test pins only that WHILE the port emits that
// payload, it stamps the router id on it, because a payload a client cannot
// attribute to a router is worse than the divergence it already represents.

import (
	"mikrodash/internal/routeros"
	"testing"
)

// deniedReader refuses the neighbour table the way RouterOS refuses a menu the
// API user has no policy for. `menuDenied` matches on the message.
type deniedReader struct{}

func (deniedReader) Connected() bool { return true }
func (deniedReader) Do(routeros.Cmd) ([]routeros.Reply, error) {
	return nil, deniedErr{}
}

type deniedErr struct{}

func (deniedErr) Error() string { return "not enough permissions (9)" }

func TestBothTopologyPayloadsCarryTheRouterID(t *testing.T) {
	var got []*TopologyPayload
	emit := func(room, event string, payload any) {
		if event != "topology:update" {
			return
		}
		p, ok := payload.(*TopologyPayload)
		if !ok {
			t.Fatalf("topology:update carried %T", payload)
		}
		got = append(got, p)
	}

	c := NewTopology(deniedReader{}, emit, nil, "r-under-test", "lbl", 30000)
	c.Tick()

	if len(got) != 1 {
		t.Fatalf("the denied path emitted %d payloads, want 1", len(got))
	}
	if got[0].RouterID != "r-under-test" {
		t.Errorf("the permission-denied payload carries routerId %q, want %q — a client "+
			"cannot tell which router was refused", got[0].RouterID, "r-under-test")
	}
	// The payload is only worth attributing if it is the denied one.
	if !got[0].PermissionDenied {
		t.Error("permissionDenied is false on the payload built by the denied branch")
	}
}
