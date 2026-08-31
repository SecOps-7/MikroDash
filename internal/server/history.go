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

// errNoRowAppeared is an add that reported success and left nothing behind. It
// cannot be shrugged off: the entry would keep a stale id and its `remove` half
// would then address whatever now holds it.
var errNoRowAppeared = errors.New("the row was added but could not be found again")

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
	cn.srv.hub.Send(cn.c, "res:history", map[string]any{
		"resource": key,
		"canUndo":  len(h.undo) > 0, "canRedo": len(h.redo) > 0,
		"undoLabel": undoLabel, "redoLabel": redoLabel,
	})
}

func (cn *conn) histPush(key string, e *history.Entry) {
	if e == nil {
		return
	}
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
func (cn *conn) applyOp(res *resource.Resource, op history.Op) (string, []resource.Error, error) {
	switch op.Op {
	case "add":
		validated, errs := res.Validate(op.Values, false)
		if len(errs) > 0 {
			return "", errs, nil
		}
		rows, err := cn.readMenu(res)
		if err != nil {
			return "", nil, err
		}
		seen := make(map[string]bool, len(rows))
		for _, r := range rows {
			seen[r[".id"]] = true
		}
		if _, err := cn.rsession.Exec(routeros.Cmd{
			Path: res.Menu + "/add", Args: res.BuildArgs(validated)}); err != nil {
			return "", nil, err
		}
		after, err := cn.readMenu(res)
		if err != nil {
			return "", nil, err
		}
		for _, r := range after {
			if !seen[r[".id"]] {
				return r[".id"], nil, nil
			}
		}
		return "", nil, errNoRowAppeared

	case "set":
		validated, errs := res.Validate(op.Values, true)
		if len(errs) > 0 {
			return "", errs, nil
		}
		args := append([]string{"=.id=" + op.ID}, res.BuildArgs(validated)...)
		if _, err := cn.rsession.Exec(routeros.Cmd{Path: res.Menu + "/set", Args: args}); err != nil {
			return "", nil, err
		}
		return op.ID, nil, nil

	default: // remove
		if _, err := cn.rsession.Exec(routeros.Cmd{
			Path: res.Menu + "/remove", Args: []string{"=.id=" + op.ID}}); err != nil {
			return "", nil, err
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
	action := res.Key + "." + dir

	h := cn.histFor(res.Key)
	stack := h.undo
	if dir == "redo" {
		stack = h.redo
	}
	if len(stack) == 0 {
		cn.resErr(res.Key, "nothing-to-"+dir, "", nil)
		return
	}
	entry := stack[len(stack)-1]
	op := entry.Reverse
	if dir == "redo" {
		op = entry.Forward
	}

	rows, err := cn.readMenu(res)
	if err != nil {
		cn.resErr(res.Key, writeFailCode(err), "", map[string]any{"message": safe.Message(err.Error())})
		return
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
			cn.resErr(res.Key, "stale-history", "", nil)
			return
		}
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
		cn.resErr(res.Key, "guard-not-ported", entry.Label,
			map[string]any{"message": safe.Message(gerr.Error())})
		return
	}
	if gate := ackGate(verdict, req.Ack); gate != nil {
		gate["resource"] = res.Key
		gate["name"] = entry.Label
		cn.srv.hub.Send(cn.c, "res:error", gate)
		return
	}

	newID, errs, err := cn.applyOp(res, op)
	if len(errs) > 0 {
		cn.resErr(res.Key, "invalid", "", map[string]any{"errors": errs})
		return
	}
	if err != nil {
		cn.resErr(res.Key, writeFailCode(err), "", map[string]any{"message": safe.Message(err.Error())})
		return
	}

	// Keep the entry pointing at the row that now exists, and at what it now
	// looks like, so the opposite direction can check it in turn.
	history.Rebind(entry, newID)
	if newID != "" {
		if after, err := cn.readMenu(res); err == nil {
			for _, r := range after {
				if r[".id"] == newID {
					entry.Identity = res.IdentityOf(r)
					break
				}
			}
		}
	}

	if dir == "undo" {
		h.undo = h.undo[:len(h.undo)-1]
		h.redo = append(h.redo, entry)
	} else {
		h.redo = h.redo[:len(h.redo)-1]
		h.undo = append(h.undo, entry)
	}
	cn.histEmit(res.Key)

	extra := []audit.KV{{Key: dir, Value: true}, {Key: "op", Value: op.Op}}
	extra = append(extra, ackExtra(req.Ack)...)
	cn.recorder().Record(audit.Event{
		Action: action, TargetType: res.Key, RouterID: cn.routerID,
		TargetID: newID, TargetName: entry.Identity,
		Note: dir + ": " + entry.Label, Extra: extra,
	})

	cn.refreshFor(res)
	// `movedId` IS PART OF THIS PAYLOAD, and it is easy to leave out because
	// nothing fails without it. The Firewall page pulses the row it names so the
	// eye can find what an undo just moved — `res:ok` is handled there for
	// `move`, `undo` and `redo` alike. Omitting it costs no error and no test:
	// the row simply does not light up, on the one page where a reorder is the
	// whole point of the action.
	//
	// NULL WHEN THE OP PRODUCED NO ID, matching `out.id || null`. An undo of a
	// delete recreates a row and has one; an undo of an update rebinds to the
	// same row and has one; an undo of a create removes a row and has none.
	var movedID any
	if newID != "" {
		movedID = newID
	}
	cn.srv.hub.Send(cn.c, "res:ok", map[string]any{
		"resource": res.Key, "action": dir, "name": entry.Identity, "movedId": movedID})
}
