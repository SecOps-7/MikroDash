package server

// change_row's `undo`: the assistant takes back a change it made (MikroMCP's
// rollback_change, the operator's choice on 2026-09-21).
//
// ── ITS OWN CHANGE, THE NEWEST, AND ONLY WITH THE OPERATOR'S YES ─────────────
//
// There is one undo history per connection and resource (history.go), shared by
// the page's Undo button and the assistant, because both are this operator. So
// the assistant may undo only the TOP of a resource's stack, and only when that
// entry is marked as its own (`Via: "agent"`): an operator's edit made after the
// assistant's is theirs to keep or undo, and reaching under it would reverse the
// wrong thing. It is always put to the operator, whatever `aiConfirmWrites` says:
// an undo is a write the model chose, to a row the operator may have looked at
// since. The proposal pins the entry, and approval refuses if the stack moved.
//
// The undo itself is `histStep`, the page's own path: fresh read, staleness,
// the row checks, the guards, audit (with `via: agent`) and refresh.

import (
	"errors"
	"fmt"

	"mikrodash/internal/history"
	"mikrodash/internal/resource"
)

// proposeAIUndo puts the assistant's newest change to `res` to the operator.
func (cn *conn) proposeAIUndo(res *resource.Resource) string {
	h := cn.histFor(res.Key)
	if len(h.undo) == 0 {
		return "There is no change to " + res.Label + " in this session to undo. Nothing was changed."
	}
	e := h.undo[len(h.undo)-1]
	if e.Via != "agent" {
		return "The most recent change to " + res.Label + " was made by the operator, not by you, so " +
			"it is theirs to undo with the page's Undo button. Nothing was changed."
	}
	return cn.raiseAIUndoProposal(res, e, "", "", nil)
}

// raiseAIUndoProposal shows the undo, with a guard's warning when one gated it.
func (cn *conn) raiseAIUndoProposal(res *resource.Resource, e *history.Entry, ack, warnCode string,
	warning map[string]any) string {

	tok, err := cn.addProposal(&aiWriteProposal{resKey: res.Key, undo: e, ack: ack})
	if err != nil {
		if errors.Is(err, errProposalsFull) {
			return "There are already several changes waiting for the operator to answer. " +
				"Ask them to deal with those before proposing another."
		}
		return "That undo could not be put to the operator, so nothing was changed."
	}
	if warning == nil {
		warning = map[string]any{}
	}
	EvAIPropose.Send(cn.srv.hub, cn.c, map[string]any{
		"token": tok, "resource": res.Key, "label": res.Label,
		"action": "undo", "name": e.Label, "command": aiUndoCommand(res, e),
		"warnCode": warnCode, "warning": warning, "values": map[string]string{},
	})
	if warnCode != "" {
		return "Waiting for confirmation. MikroDash flagged a safety warning on this undo and is " +
			"showing it to the operator; it happens when they confirm. Tell them what it puts " +
			"back and why it was flagged. It has not been undone yet."
	}
	return "Waiting for confirmation. MikroDash is showing the operator this undo in a " +
		"confirmation dialog, and it happens when they confirm. Tell them what it puts back. " +
		"It has not been undone yet."
}

// aiUndoCommand is what the undo will send, in the terms of its reverse op.
func aiUndoCommand(res *resource.Resource, e *history.Entry) string {
	switch e.Reverse.Op {
	case "add":
		return res.Menu + "/add (puts " + quoted(e.Identity) + " back as it was)"
	case "remove":
		return res.Menu + "/remove =.id=" + e.Reverse.ID
	}
	return res.Menu + "/set =.id=" + e.Reverse.ID + " (puts back the values it had)"
}

// approveAIUndo runs an approved undo and says what happened.
func (cn *conn) approveAIUndo(res *resource.Resource, p *aiWriteProposal) {
	h := cn.histFor(res.Key)
	var out writeOutcome
	if len(h.undo) == 0 || h.undo[len(h.undo)-1] != p.undo {
		out = writeOutcome{Code: "history-moved"}
	} else {
		out = cn.histStep("undo", res, p.undo, p.ack, "agent")
	}
	if fp, gate := guardGate(out); gate {
		d := gateDetail(out)
		code, _ := d["code"].(string)
		warning, _ := d["warning"].(map[string]any)
		cn.raiseAIUndoProposal(res, p.undo, fp, code, warning)
		cn.aiWritten(map[string]any{"applied": false, "resource": res.Key, "name": "",
			"text": "Not undone yet: a safety check warned about it, so MikroDash is asking the operator " +
				"again with that warning shown."})
		return
	}
	text := fmt.Sprintf("Applied: the %s (%s) was undone, and the row was read back.", p.undo.Label, res.Label)
	if out.Code != "" {
		text = aiRefusalText(res, out)
	}
	cn.aiWritten(map[string]any{
		"applied": out.Code == "", "resource": res.Key, "name": out.Name, "text": text,
	})
}
