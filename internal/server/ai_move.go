package server

// `change_row`'s `before`: the assistant putting a firewall rule in its place.
//
// ── THE GAP THIS CLOSES ─────────────────────────────────────────────────────
//
// The write tool could create, edit and delete, and RouterOS adds a new rule at
// the END of its chain. In a firewall the FIRST MATCH DECIDES, so an appended
// rule is very often a rule that never runs — and the assistant reported the
// change as applied, because it was. The router did exactly what it was asked
// and the traffic did not change.
//
// ── IT RUNS THE PAGE'S OWN MOVE, NOT A COPY OF IT ───────────────────────────
//
// `moveRow` is what `res:move` uses: the fresh read, the row found by identity,
// the anchor checked against the table as the router holds it now, the guards,
// the `/move`, the read-back that confirms the ORDER asked for, the audit row
// and the refresh. Nothing here sends a command, and nothing here decides
// whether a move is safe.
//
// ── THE MODEL NAMES A ROW, NEVER A POSITION ─────────────────────────────────
//
// `before` is an id — the same thing the page's drag sends as its anchor. An
// index would be computed against a table the model read some calls ago and
// would silently address whatever is at that index now. An id that is no longer
// there is `stale-row`, and a destination RouterOS cannot resolve means "the end
// of the table" to it, so an unchecked one would quietly append: precisely the
// failure this exists to fix.

import (
	"errors"

	"mikrodash/internal/aitools"
	"mikrodash/internal/resource"
	"mikrodash/internal/store"
)

// aiMoveEnd is the word that means "last in the table".
//
// A SENTINEL RATHER THAN A SECOND ARGUMENT, because `anchor: ""` is already how
// this app spells the end of a list and a `before` of "" is indistinguishable
// from not asking for a move at all. RouterOS ids are `*<hex>`, so nothing this
// word could collide with is ever a real one.
const aiMoveEnd = "end"

// proposeAIMove is `change_row` with `before`.
//
// ── IT OBEYS `aiConfirmWrites`, AS AN EDIT DOES ─────────────────────────────
//
// A delete and a `run_action` always ask, because neither can be taken back from
// this app: the row and its values are gone, or the router is rebooting. A move
// is neither. It destroys nothing, and the repair is the opposite move — so
// holding it to the delete rule would be a second answer to "when does the
// assistant ask", which is the setting's question and has one answer already.
//
// The guards are unaffected: `fwGuard` warns on a rule "moved somewhere it may
// now win", so a move that could cut MikroDash off reaches the operator with its
// warning whatever the setting says. That is the existing mechanism, not a rule
// this file remembers.
func (cn *conn) proposeAIMove(res *resource.Resource, id, before string) string {
	if !aitools.Movable(res) {
		return "Rows of that kind have no order, so there is nowhere to move one to. " +
			"Only the firewall's tables are ordered. Nothing was changed."
	}
	if id == "" {
		return "A move needs the `id` of the row to move, exactly as the list tool reported " +
			"it. Nothing was changed."
	}
	if before == id {
		return "A row cannot be moved before itself. Name the row it should sit in front of, " +
			"or `end`. Nothing was changed."
	}

	settings, err := cn.srv.mergedSettings()
	if err != nil {
		return "The settings could not be read, so nothing was changed."
	}
	// ALWAYS ANCHORED. The direction spelling is the page's arrows; the model
	// names the row to land before, and `end` is the empty anchor this app
	// already uses for the end of a list.
	req := &resRequest{Resource: res.Key, ID: id, HasAnchor: true}
	if before != aiMoveEnd {
		req.Anchor = before
	}

	if !store.AIConfirmWrites(settings) {
		out := cn.moveRow(res, req, "agent")
		if out.Code == "" {
			return aiMoveApplied(res, out.Name, before == aiMoveEnd)
		}
		if fp, gate := guardGate(out); gate {
			out.Detail = gateDetail(out)
			return cn.raiseAIMoveProposal(res, req, out.Name, fp, out)
		}
		return aiRefusalText(res, out)
	}

	// PROMPTS ARE ON, SO NOTHING IS MOVED YET. `prepareMove` runs anyway,
	// because the operator must be shown what the SERVER worked out — which row
	// this id is now, whether the row it would land before is still there, and
	// any guard verdict — rather than the model's account of it.
	p, refusal := cn.prepareMove(res, req)
	if p == nil {
		return aiRefusalText(res, refusal)
	}
	// The row the operator will be shown, pinned: approval refuses a row at this
	// `.id` that is no longer it.
	req.ExpectedIdentity = p.name
	ack := ""
	gate := map[string]any{}
	if p.verdict.Warned() {
		ack = p.verdict.Fingerprint
		gate["warning"] = p.verdict.Detail
		gate["code"] = p.verdict.Code
	}
	return cn.raiseAIMoveProposal(res, req, p.name, ack, writeOutcome{Name: p.name, Detail: gate})
}

// raiseAIMoveProposal puts a move in front of the operator. It returns at once,
// for the reason `raiseAIProposal` does: a human takes longer than the model
// request this runs inside.
func (cn *conn) raiseAIMoveProposal(res *resource.Resource, req *resRequest,
	name, ack string, out writeOutcome) string {

	tok, err := cn.addProposal(&aiWriteProposal{
		resKey: res.Key, rowID: req.ID, identity: req.ExpectedIdentity,
		move: true, anchor: req.Anchor, ack: ack,
	})
	if err != nil {
		if errors.Is(err, errProposalsFull) {
			return "There are already several changes waiting for the operator to answer. " +
				"Ask them to deal with those before proposing another."
		}
		return "That move could not be put to the operator, so nothing was changed."
	}

	warnCode, _ := out.Detail["code"].(string)
	warning, _ := out.Detail["warning"].(map[string]any)
	if warning == nil {
		warning = map[string]any{}
	}
	EvAIPropose.Send(cn.srv.hub, cn.c, map[string]any{
		"token": tok, "resource": res.Key, "label": res.Label,
		"action": "move", "name": name, "command": aiMoveCommand(res, req),
		"warnCode": warnCode, "warning": warning,
		"values": map[string]string{"moves": cn.aiMoveDestination(res, req.Anchor)},
	})

	if warnCode != "" {
		return "Waiting for confirmation. MikroDash flagged a safety warning on this move and " +
			"is showing it to the operator in a confirmation dialog; it happens when they " +
			"confirm. Tell them where the rule would end up and why it was flagged. It has " +
			"not been moved yet."
	}
	return "Waiting for confirmation. MikroDash is showing the operator this move in a " +
		"confirmation dialog, and it happens when they confirm. Tell them where the rule " +
		"would end up. It has not been moved yet."
}

// aiMoveCommand is the command the move will send, built HERE rather than taken
// from the model, as every other proposal's is.
func aiMoveCommand(res *resource.Resource, req *resRequest) string {
	cmd := res.Menu + "/move =numbers=" + req.ID
	if req.Anchor != "" {
		cmd += " =destination=" + req.Anchor
	}
	return cmd
}

// aiMoveDestination is where the rule lands, in the operator's terms.
//
// The command line beside it names the anchor by id, which says nothing about
// which rule that is. This reads the row back so the dialog can name it — one
// row by id, the read `readRow` already does for every write.
func (cn *conn) aiMoveDestination(res *resource.Resource, anchor string) string {
	if anchor == "" {
		return "to the end of " + res.Menu
	}
	rows, err := cn.readRow(res, anchor)
	if err == nil {
		if row := rowByID(rows, anchor); row != nil {
			if name := res.IdentityOf(row); name != "" {
				return "in front of " + quoted(name)
			}
		}
	}
	return "in front of the row " + anchor
}

// aiMoveApplied says what happened, in terms the operator can check.
func aiMoveApplied(res *resource.Resource, name string, last bool) string {
	if last {
		return "Applied: the " + res.Label + " " + quoted(name) + " is now last in " +
			res.Menu + ", confirmed by reading the table back."
	}
	return "Applied: the " + res.Label + " " + quoted(name) + " now sits immediately before " +
		"the row you named, confirmed by reading the table back."
}
