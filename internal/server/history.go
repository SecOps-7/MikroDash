package server

import (
	"encoding/json"
	"errors"

	"mikrodash/internal/audit"
	"mikrodash/internal/history"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

// Undo and redo.
//
// PER CONNECTION, PER RESOURCE, IN MEMORY, dying with the socket. "Undo" here
// means "undo what I just did", which is what anyone pressing the button
// expects: a stack shared between operators would let one silently revert
// another's work, and a stack that outlived the session would offer to reverse
// something from last week.
//
// Per RESOURCE and not one global stack, so undo on the Firewall card can never
// reach into DNS.
//
// AN UNDO IS A WRITE LIKE ANY OTHER, and this path does everything the ordinary
// write handlers do: both gates, a fresh read, a staleness check, the guard, an
// audit row, a refresh. Undoing the deletion of a `drop` rule puts that rule
// back, and it can lock us out exactly as the original did.

const histDepth = 20

type histStack struct {
	undo []*history.Entry
	redo []*history.Entry
}

func (cn *conn) histFor(key string) *histStack {
	if cn.resHist == nil {
		cn.resHist = map[string]*histStack{}
	}
	h, ok := cn.resHist[key]
	if !ok {
		h = &histStack{}
		cn.resHist[key] = h
	}
	return h
}

func (cn *conn) histEmit(key string) {
	h := cn.histFor(key)
	undoLabel, redoLabel := "", ""
	if n := len(h.undo); n > 0 {
		undoLabel = h.undo[n-1].Label
	}
	if n := len(h.redo); n > 0 {
		redoLabel = h.redo[n-1].Label
	}
	EvResHistory.Send(cn.srv.hub, cn.c, map[string]any{
		"resource": key,
		"canUndo":  len(h.undo) > 0, "canRedo": len(h.redo) > 0,
		"undoLabel": undoLabel, "redoLabel": redoLabel,
	})
}

func (cn *conn) histPush(key string, e *history.Entry, via string) {
	if e == nil {
		return
	}
	e.Via = via
	h := cn.histFor(key)
	h.undo = append(h.undo, e)
	if len(h.undo) > histDepth {
		h.undo = h.undo[1:]
	}
	// A fresh action forks the timeline: what was undone can no longer be redone
	// on top of something else.
	h.redo = nil
	cn.histEmit(key)
}

// histDrop throws one resource's history away: it no longer describes this
// router, so none of it can be trusted.
func (cn *conn) histDrop(key string) {
	h := cn.histFor(key)
	h.undo, h.redo = nil, nil
	cn.histEmit(key)
}

// histDropAll runs on a router switch. Every entry describes rows on the router
// being left, and a `.id` from one router addresses something entirely different
// on another — the one way an undo could destroy the wrong row.
func (cn *conn) histDropAll() {
	for key := range cn.resHist {
		cn.histDrop(key)
	}
}

// histValues flattens RowValues into what Validate takes.
//
// RowValues yields a real boolean for a bool field and a string for everything
// else, while Validate works in strings and accepts "true"/"yes". This is the
// same coercion the browser path does, applied to a row read back off the
// router: both sides of an undo have to speak one vocabulary.
func histValues(v map[string]any) map[string]string {
	out := make(map[string]string, len(v))
	for k, raw := range v {
		switch t := raw.(type) {
		case string:
			out[k] = t
		case bool:
			if t {
				out[k] = "true"
			} else {
				out[k] = "false"
			}
		}
	}
	return out
}

// applyOp performs one recorded operation and answers with the id the row now
// has — empty for a remove, which leaves no row behind.
//
// An `add` is the awkward one: RouterOS assigns the id, so the new row is found
// by diffing the table against itself rather than by assuming it is last. It
// usually IS last. "Usually" is not a thing to build an undo on.
// errNotRecreatable is an undo that would have to create a NoCreate resource.
var errNotRecreatable = errors.New("this cannot be re-created, so its removal cannot be undone")

func (cn *conn) applyOp(res *resource.Resource, op history.Op) (string, []resource.Error, error) {
	switch op.Op {
	case "add":
		// A resource that cannot be created cannot be re-created either: the
		// delete path records no undo for one, and this refuses it anyway, before
		// anything reaches the router.
		if !res.UndoesRemoval() {
			return "", nil, errNotRecreatable
		}
		validated, errs := res.Validate(op.Values, false)
		if len(errs) > 0 {
			return "", errs, nil
		}
		// The add names its new row, which is the one read back: nothing is
		// read first (see readRow).
		var ret string
		if _, err := cn.rsession.Exec(routeros.Cmd{
			Path: res.Menu + "/add", Args: res.BuildArgs(validated), Ret: &ret}); err != nil {
			return "", nil, err
		}
		// CONFIRMED, as every write is (#97): the row the add named, read back. An
		// add that named none, or whose row is not there, is an unknown outcome.
		if ret == "" {
			return "", nil, errOutcomeUnknown
		}
		after, err := cn.readRow(res, ret)
		if err != nil || rowByID(after, ret) == nil {
			return "", nil, errOutcomeUnknown
		}
		return ret, nil, nil

	case "set":
		validated, errs := res.Validate(op.Values, true)
		if len(errs) > 0 {
			return "", errs, nil
		}
		args := append(res.IDWords(op.ID), res.BuildArgs(validated)...)
		if _, err := cn.rsession.Exec(routeros.Cmd{Path: res.Menu + "/set", Args: args}); err != nil {
			return "", nil, err
		}
		if after, err := cn.readRow(res, op.ID); err != nil || rowByID(after, op.ID) == nil {
			return "", nil, errOutcomeUnknown
		}
		return op.ID, nil, nil

	default: // remove
		if _, err := cn.rsession.Exec(routeros.Cmd{
			Path: res.Menu + "/remove", Args: res.IDWords(op.ID)}); err != nil {
			return "", nil, err
		}
		if after, err := cn.readRow(res, op.ID); err != nil || !confirmRemoved(after, op.ID) {
			return "", nil, errOutcomeUnknown
		}
		return "", nil, nil
	}
}

// opMeans translates a recorded operation into the verb the guards speak.
var opMeans = map[string]string{"add": "create", "set": "update", "remove": "delete"}

func (cn *conn) resUndo(raw json.RawMessage) { cn.histRun("undo", raw) }
func (cn *conn) resRedo(raw json.RawMessage) { cn.histRun("redo", raw) }

func (cn *conn) histRun(dir string, raw json.RawMessage) {
	res, req := cn.resolve(raw, true)
	if res == nil {
		return
	}
	h := cn.histFor(res.Key)
	stack := h.undo
	if dir == "redo" {
		stack = h.redo
	}
	if len(stack) == 0 {
		cn.resErr(res.Key, "nothing-to-"+dir, "", nil)
		return
	}
	cn.answerHist(res, dir, cn.histStep(dir, res, stack[len(stack)-1], req.Ack, ""))
}

// histStep runs one undo or redo of `entry`, the top of its stack, in the
// write queue.
//
// ── A WRITE LIKE ANY OTHER ──────────────────────────────────────────────────
//
// The read, the checks and the op run INSIDE the write queue, which takes the
// rate limit first. histRun did all of it directly: an undo took no write slot
// and no rate limit, so it could be sent as fast as the router answered and
// could interleave with another writer's read-check-write on the same menu
// (review 2026-09-19).
//
// The page's buttons and the assistant's `change_row` undo both come here, and
// each answers its own caller from the outcome: one path, two reporters.
func (cn *conn) histStep(dir string, res *resource.Resource, entry *history.Entry, ack, via string) writeOutcome {
	op := entry.Reverse
	if dir == "redo" {
		op = entry.Forward
	}
	var out writeOutcome
	if err := cn.inWriteQueue(func() error {
		out = cn.histApply(dir, res.Key+"."+dir, res, ack, cn.histFor(res.Key), entry, op, via)
		return nil
	}); err != nil {
		return writeOutcome{Code: writeFailCode(err), Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	return out
}

// answerHist tells the page what an undo or redo did.
//
// `movedId` IS PART OF THE SUCCESS PAYLOAD, and it is easy to leave out because
// nothing fails without it. The Firewall page pulses the row it names so the
// eye can find what an undo just moved — `res:ok` is handled there for `move`,
// `undo` and `redo` alike. NULL WHEN THE OP PRODUCED NO ID, matching
// `out.id || null`: an undo of a create removes a row and has none.
func (cn *conn) answerHist(res *resource.Resource, dir string, out writeOutcome) {
	if out.Code != "" {
		cn.resErr(res.Key, out.Code, out.Name, out.Detail)
		return
	}
	var movedID any
	if id, _ := out.Detail["movedId"].(string); id != "" {
		movedID = id
	}
	EvResOk.Send(cn.srv.hub, cn.c, map[string]any{
		"resource": res.Key, "action": dir, "name": out.Name, "movedId": movedID})
}

// histApply is an undo or redo, run inside the write queue by histStep. It
// answers nobody itself: the outcome says what happened.
func (cn *conn) histApply(dir, action string, res *resource.Resource, ack string,
	h *histStack, entry *history.Entry, op history.Op, via string) writeOutcome {

	fail := func(err error) writeOutcome {
		return writeOutcome{Code: writeFailCode(err), Detail: map[string]any{"message": safe.Message(err.Error())}}
	}

	// The row this entry is about, read by its id; an add is about a row that
	// is not there, so it reads nothing.
	var rows []routeros.Reply
	if op.Op != "add" {
		var err error
		if rows, err = cn.readRow(res, op.ID); err != nil {
			return fail(err)
		}
	}

	// The row this entry is about must still BE the row it was about. If it is
	// not, everything below it on the stack is suspect too, so the whole history
	// goes rather than leaving a trap for the next click.
	var beforeRow routeros.Reply
	if op.Op != "add" {
		for _, r := range rows {
			if r[".id"] == op.ID {
				beforeRow = r
				break
			}
		}
		if beforeRow == nil || res.IdentityOf(beforeRow) != entry.Identity {
			cn.histDrop(res.Key)
			return writeOutcome{Code: "stale-history"}
		}
	}

	// THE ROW CHECKS A FORM WRITE MAKES, on the row as it is now. An update undone
	// is a write to a row that may have become read-only (dynamic, a regexp DNS
	// entry) since it was edited; a create undone is a removal, which the
	// resource may not allow; and a resource whose rows cannot be edited is not
	// edited by an undo either.
	if refusal := histRowRefusal(res, op, beforeRow); refusal != "" {
		cn.recorder().Denied(audit.Event{
			Action: action, TargetType: res.Key, RouterID: cn.routerID,
			TargetID: op.ID, TargetName: entry.Identity, Note: refusal,
		})
		return writeOutcome{Code: refusal, Name: entry.Label}
	}

	values := op.Values
	if values == nil && beforeRow != nil {
		values = histValues(res.RowValues(beforeRow))
	}
	verdict, gerr := cn.verdictFor(res, opMeans[op.Op], values, beforeRow)
	if gerr != nil {
		cn.recorder().Denied(audit.Event{
			Action: action, TargetType: res.Key, RouterID: cn.routerID,
			TargetID: op.ID, TargetName: entry.Identity,
			Note: "guard-not-ported: " + gerr.Error(),
		})
		return writeOutcome{Code: "guard-not-ported", Name: entry.Label,
			Detail: map[string]any{"message": safe.Message(gerr.Error())}}
	}
	if r := cn.guardRefusal(res, opMeans[op.Op], op.ID, entry.Identity, verdict); r != nil {
		return writeOutcome{Code: r.Code, Name: entry.Label, Detail: r.Detail}
	}
	if gate := ackGate(verdict, ack); gate != nil {
		code, _ := gate["code"].(string)
		delete(gate, "code")
		return writeOutcome{Code: code, Name: entry.Label, Detail: gate}
	}

	newID, errs, err := cn.applyOp(res, op)
	if len(errs) > 0 {
		return writeOutcome{Code: "invalid", Detail: map[string]any{"errors": errs}}
	}
	if errors.Is(err, errOutcomeUnknown) {
		// The router accepted the undo or redo, but it could not be confirmed, so
		// this history may no longer describe the table. It goes, as it does for
		// a stale entry, and the attempt is audited and the table refreshed.
		cn.histDrop(res.Key)
		cn.recorder().Record(audit.Event{
			Action: action, TargetType: res.Key, RouterID: cn.routerID,
			TargetID: op.ID, TargetName: entry.Identity,
			Note: "outcome-unknown: " + dir + ": " + entry.Label,
		})
		cn.refreshFor(res)
	}
	if err != nil {
		return fail(err)
	}

	// Keep the entry pointing at the row that now exists, and at what it now
	// looks like, so the opposite direction can check it in turn.
	history.Rebind(entry, newID)
	if newID != "" {
		if after, err := cn.readRow(res, newID); err == nil {
			for _, r := range after {
				if r[".id"] == newID {
					entry.Identity = res.IdentityOf(r)
					break
				}
			}
		}
	}

	// WHOEVER LAST APPLIED IT OWNS IT: a change the operator redid from the
	// page is theirs now, and no longer one the assistant may undo.
	entry.Via = via
	if dir == "undo" {
		h.undo = h.undo[:len(h.undo)-1]
		h.redo = append(h.redo, entry)
	} else {
		h.redo = h.redo[:len(h.redo)-1]
		h.undo = append(h.undo, entry)
	}
	cn.histEmit(res.Key)

	extra := []audit.KV{{Key: dir, Value: true}, {Key: "op", Value: op.Op}}
	if via != "" {
		extra = append(extra, audit.KV{Key: "via", Value: via})
	}
	extra = append(extra, ackExtra(ack)...)
	cn.recorder().Record(audit.Event{
		Action: action, TargetType: res.Key, RouterID: cn.routerID,
		TargetID: newID, TargetName: entry.Identity,
		Note: dir + ": " + entry.Label, Extra: extra,
	})

	cn.refreshFor(res)
	return writeOutcome{Action: dir, Name: entry.Identity, Detail: map[string]any{"movedId": newID}}
}

// histRowRefusal is the refusal code a form write would give this op on this
// row, or "" when it may proceed. An add (the undo of a delete) is refused for a
// NoCreate resource in applyOp, as before.
func histRowRefusal(res *resource.Resource, op history.Op, row routeros.Reply) string {
	if row == nil {
		return ""
	}
	if res.ReadOnlyWhen != nil && res.ReadOnlyWhen(row) {
		return res.ReadOnlyReason
	}
	if op.Op == "set" && res.NoEdit {
		return "not-editable"
	}
	if op.Op == "remove" && res.RemovableWhen != nil && !res.RemovableWhen(row) {
		return "not-removable"
	}
	return ""
}
