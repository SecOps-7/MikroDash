package server

import (
	"encoding/json"
	"strings"
	"testing"

	"mikrodash/internal/history"
	"mikrodash/internal/hub"
	"mikrodash/internal/resource"
)

// THE ASSISTANT UNDOES ONLY ITS OWN NEWEST CHANGE. The stack is shared with the
// page's Undo button: an operator's edit on top is theirs, an empty stack has
// nothing, and the assistant's own entry on top is put to the operator, pinned.
func TestTheAssistantUndoesOnlyItsOwnNewestChange(t *testing.T) {
	h := hub.New()
	me := hub.NewClient("me", 64)
	h.Add(me)
	cn := &conn{srv: &Server{hub: h}, c: me, sess: &Session{AuthMode: "none"}, routerID: "r-A"}
	res := resource.DNSStatic

	if out := cn.proposeAIUndo(res); !strings.Contains(out, "no change") {
		t.Errorf("an empty history answered %q", out)
	}

	mine := history.Build(res.Key, res.Label, "create", "*1", "a.example", nil, map[string]string{"name": "a.example"})
	cn.histPush(res.Key, mine, "agent")
	theirs := history.Build(res.Key, res.Label, "create", "*2", "b.example", nil, map[string]string{"name": "b.example"})
	cn.histPush(res.Key, theirs, "")
	if theirs.Via != "" || mine.Via != "agent" {
		t.Fatalf("histPush did not record who wrote: %q %q", mine.Via, theirs.Via)
	}
	if out := cn.proposeAIUndo(res); !strings.Contains(out, "made by the operator") {
		t.Errorf("the operator's change on top was offered for undo: %q", out)
	}
	if len(cn.proposals) != 0 {
		t.Fatal("a proposal was raised for the operator's change")
	}

	// The control: with the operator's change gone, the assistant's is on top.
	hs := cn.histFor(res.Key)
	hs.undo = hs.undo[:1]
	if out := cn.proposeAIUndo(res); !strings.Contains(out, "Waiting for confirmation") {
		t.Fatalf("the assistant's own change was not proposed: %q", out)
	}
	var p *aiWriteProposal
	for _, x := range cn.proposals {
		p = x
	}
	if p == nil || p.undo != mine || p.resKey != res.Key {
		t.Fatalf("the proposal does not pin the entry it will undo: %+v", p)
	}

	// Approval refuses once the stack has moved, before any router is asked
	// (there is none here: reaching one would panic).
	cn.histPush(res.Key, history.Build(res.Key, res.Label, "create", "*3", "c.example", nil,
		map[string]string{"name": "c.example"}), "agent")
	cn.approveAIUndo(res, p)

	var written map[string]any
	for done := false; !done; {
		select {
		case b := <-me.Send:
			var env struct {
				Event string         `json:"event"`
				Data  map[string]any `json:"data"`
			}
			if json.Unmarshal(b, &env) == nil && env.Event == "ai:written" {
				written = env.Data
			}
		default:
			done = true
		}
	}
	if written == nil || written["applied"] != false || !strings.Contains(written["text"].(string), "no longer the newest") {
		t.Errorf("an undo approved after the history moved answered %v", written)
	}
}
