package hub

// BroadcastExcept exists for one shape: an action whose result is PER-VIEWER.
//
// The Backups page's payload carries `permitted`, computed for the socket that
// asked. Broadcasting it after a delete would tell a viewer they may write, so
// the actor gets their own payload and everybody else is nudged to re-request
// theirs. These tests pin the half that is easy to get wrong — who does NOT
// receive the frame — because a broadcast that reaches one client too many
// looks exactly like a working broadcast from the sender's side.

import (
	"encoding/json"
	"testing"
)

// drain reads everything waiting for a client without blocking.
func drain(c *Client) []Envelope {
	out := []Envelope{}
	for {
		select {
		case b := <-c.Send:
			var e Envelope
			if err := json.Unmarshal(b, &e); err != nil {
				panic(err)
			}
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestBroadcastExceptSkipsTheActorAndNobodyElse(t *testing.T) {
	h := New()
	actor := NewClient("actor", 4)
	other := NewClient("other", 4)
	third := NewClient("third", 4)
	for _, c := range []*Client{actor, other, third} {
		h.Add(c)
		h.Join(c, "router-r1-page-backups")
	}

	h.BroadcastExcept("router-r1-page-backups", actor, "backups:ran",
		map[string]any{"routerId": "r1"})

	if got := drain(actor); len(got) != 0 {
		t.Errorf("the actor received %d frames; it already has its own payload", len(got))
	}
	for _, c := range []*Client{other, third} {
		got := drain(c)
		if len(got) != 1 || got[0].Event != "backups:ran" {
			t.Fatalf("%s received %v, want one backups:ran", c.ID, got)
		}
	}
}

// TestBroadcastExceptIgnoresAClientInAnotherRoom — the exclusion is not the only
// filter, and a room that happens to hold the excluded client must not leak into
// a different room's fan-out.
func TestBroadcastExceptIgnoresAClientInAnotherRoom(t *testing.T) {
	h := New()
	here := NewClient("here", 4)
	elsewhere := NewClient("elsewhere", 4)
	h.Add(here)
	h.Add(elsewhere)
	h.Join(here, "router-r1-page-backups")
	h.Join(elsewhere, "router-r2-page-backups")

	h.BroadcastExcept("router-r1-page-backups", nil, "backups:ran", map[string]any{"routerId": "r1"})

	if got := drain(elsewhere); len(got) != 0 {
		t.Errorf("a client on another router received %v", got)
	}
	if got := drain(here); len(got) != 1 {
		t.Errorf("the client in the room received %d frames, want 1", len(got))
	}
}

// TestBroadcastExceptWithNoOneElseSendsNothing. The early return also skips the
// marshal, which is the reason it is there — a delete on a router nobody else is
// watching is the common case.
func TestBroadcastExceptWithNoOneElseSendsNothing(t *testing.T) {
	h := New()
	alone := NewClient("alone", 4)
	h.Add(alone)
	h.Join(alone, "router-r1-page-backups")

	h.BroadcastExcept("router-r1-page-backups", alone, "backups:ran", map[string]any{"routerId": "r1"})
	if got := drain(alone); len(got) != 0 {
		t.Errorf("the only client received %v", got)
	}
	// And an empty room is not an error.
	h.BroadcastExcept("router-r9-page-backups", nil, "backups:ran", map[string]any{})
}

// TestBroadcastExceptCarriesNoPayloadBeyondTheRouterId is the security half
// stated as a test: this event is a NUDGE. If it ever grows the page payload,
// the `permitted` flag computed for one socket would reach every other.
func TestBroadcastExceptIsANudgeNotThePayload(t *testing.T) {
	h := New()
	actor := NewClient("actor", 4)
	viewer := NewClient("viewer", 4)
	h.Add(actor)
	h.Add(viewer)
	h.Join(actor, "router-r1-page-backups")
	h.Join(viewer, "router-r1-page-backups")

	h.BroadcastExcept("router-r1-page-backups", actor, "backups:ran",
		map[string]any{"routerId": "r1"})

	got := drain(viewer)
	if len(got) != 1 {
		t.Fatalf("viewer received %d frames, want 1", len(got))
	}
	data, ok := got[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("payload is %T, want an object", got[0].Data)
	}
	if len(data) != 1 || data["routerId"] != "r1" {
		t.Errorf("payload is %v; it must carry the router id and NOTHING else — "+
			"anything computed for the acting socket would be disclosed here", data)
	}
}
