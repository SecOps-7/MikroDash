package server

// `run_command`: one RouterOS command the model composed.
//
// ── THE ONE PATH THAT LEAVES THE REGISTRY ───────────────────────────────────
//
// Every other write in this app goes through a declared resource: a menu the
// registry names, fields it validates, a guard that can refuse it, a read-back
// that confirms it and an undo entry. A raw command has none of that. It can
// reach a menu the page permission matrix never mapped, so there is no page
// whose write permission could gate it, and nothing to compare the result with.
//
// So the controls are not the registry's. They are, per call and all three:
//
//	a SIGNED-IN GLOBAL ADMINISTRATOR   not merely someone who may write a page,
//	                                   and not "sign-in is off, so everyone is"
//	the aiAllowRawCommands SETTING     default false; see store.AIAllowRawCommands
//	the ROUTER'S NAME TYPED BACK       every command, read or write, whatever
//	                                   aiConfirmWrites says
//
// A read is confirmed too, and that is deliberate: `/user/print` through this
// path returns rows the Router Users page would have refused to show a viewer
// who may not read it, and the person approving is the check on that.
//
// ── NOT ADVERTISED ──────────────────────────────────────────────────────────
//
// `run_command` is in no viewer's tool list and not in the generated catalogue.
// A model can still name it, because a model can name anything, and that call
// lands here and meets the gates — which is the point of answering by name
// rather than with "no such tool".

import (
	"encoding/json"
	"strings"
	"time"

	"mikrodash/internal/aicontext"
	"mikrodash/internal/aiprovider"
	"mikrodash/internal/audit"
	"mikrodash/internal/rawcmd"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
	"mikrodash/internal/store"
)

// rawOutputMaxRows and rawOutputMaxBytes bound what a command's reply costs.
// `/ip/firewall/connection/print` on a busy router is tens of thousands of rows,
// and the whole reply would otherwise be sent to the model as one message.
const (
	rawOutputMaxRows  = 50
	rawOutputMaxBytes = 16 * 1024
)

// rawCommandGate is the three checks, in the order that refuses earliest.
//
// The refusal text is the SAME whichever gate stopped it, so a model cannot map
// the estate by watching which refusal it gets. The audit row says which.
func (cn *conn) rawCommandGate(action string) (store.Settings, string) {
	const refusal = "Raw RouterOS commands are not available. They are off unless a global " +
		"administrator has switched them on for this installation."

	// SIGNED IN, and a global administrator. `isGlobalAdmin` answers true when
	// sign-in is switched off entirely; that is right for reading the principal
	// graph and wrong here, for the reason #97 gives about router writes: a
	// command nobody can be held to is one nobody should be able to send.
	if cn.sess == nil || cn.sess.AuthMode == "none" || !cn.srv.isGlobalAdmin(cn.sess) {
		cn.recorder().Denied(audit.Event{
			Action: action, TargetType: "router", RouterID: cn.routerID,
			Note: "not a signed-in global administrator",
		})
		return nil, refusal
	}
	settings, err := cn.srv.mergedSettings()
	if err != nil {
		return nil, "The settings could not be read, so nothing was run."
	}
	if !store.AIAllowRawCommands(settings) {
		cn.recorder().Denied(audit.Event{
			Action: action, TargetType: "router", RouterID: cn.routerID,
			Note: "aiAllowRawCommands is off",
		})
		return nil, refusal
	}
	if cn.rsession == nil {
		return nil, "No device is selected, so nothing was run."
	}
	return settings, ""
}

// runAIRawCommandTool is `run_command`.
func (cn *conn) runAIRawCommandTool(tc aiprovider.ToolCall) string {
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(tc.Function.Arguments), &args) != nil {
		return "Those arguments were not valid JSON. Send `command` as one RouterOS command."
	}
	if _, refusal := cn.rawCommandGate("raw.command"); refusal != "" {
		return refusal
	}
	cmd, err := rawcmd.Parse(args.Command)
	if err != nil {
		// THE REASON, NOT THE INPUT. rawcmd's errors name what was refused
		// rather than repeating the string, which would put a crafted command
		// back into the transcript.
		return "That command was refused before it was sent: " + err.Error() + "."
	}
	return cn.raiseAIRawCommand(cmd)
}

// raiseAIRawCommand puts one parsed command to the operator. Always.
func (cn *conn) raiseAIRawCommand(cmd rawcmd.Command) string {
	tok, err := aiProposalToken()
	if err != nil {
		return "That command could not be put to the operator, so nothing was run."
	}
	cn.proposeMu.Lock()
	if cn.proposals == nil {
		cn.proposals = map[string]*aiWriteProposal{}
	}
	now := time.Now()
	for k, v := range cn.proposals {
		if now.Sub(v.raisedAt) > aiProposalTTL {
			delete(cn.proposals, k)
		}
	}
	if len(cn.proposals) >= aiMaxProposals {
		cn.proposeMu.Unlock()
		return "There are already several things waiting for the operator to answer."
	}
	cn.proposals[tok] = &aiWriteProposal{token: tok, raw: &cmd, raisedAt: now}
	cn.proposeMu.Unlock()

	kind := "change"
	if rawcmd.IsReadVerb(cmd.Verb) {
		kind = "read"
	}
	EvAIPropose.Send(cn.srv.hub, cn.c, map[string]any{
		"token": tok, "kind": "command", "action": kind, "label": "RouterOS command",
		"name": cmd.Menu, "command": cmd.Text, "routerName": cn.rsession.Label,
		"typedName": true, "warnCode": "", "warning": map[string]any{}, "values": map[string]string{},
	})
	return "Waiting for confirmation. MikroDash is asking the operator to confirm this command by " +
		"typing the router's name; raw commands are always confirmed, reads included. It has not " +
		"run yet: tell them exactly what it will do."
}

// approveAIRawCommand runs a command the operator accepted.
func (cn *conn) approveAIRawCommand(p *aiWriteProposal, confirm string) {
	cmd := *p.raw
	done := func(applied bool, text string) {
		EvAIWritten.Send(cn.srv.hub, cn.c, map[string]any{
			"applied": applied, "resource": "run_command", "name": cmd.Menu, "text": text,
		})
	}
	// CHECKED AGAIN AT APPROVAL: the proposal may have been raised minutes ago,
	// and a role, the setting or the selected router can change in between.
	if _, refusal := cn.rawCommandGate("raw.command"); refusal != "" {
		done(false, refusal)
		return
	}
	name := cn.rsession.Label
	if name == "" || !strings.EqualFold(strings.TrimSpace(confirm), name) {
		done(false, "Not run: the router's name was not typed back correctly.")
		return
	}

	var rows []routeros.Reply
	// THE RATE LIMIT IS THE WRITE LIMIT, and it applies to a read too: a loop of
	// raw prints is as much load on a router as a loop of writes, and this path
	// has no collector cache behind it.
	err := cn.inWriteQueue(func() error {
		out, e := cn.rsession.Exec(routeros.Cmd{Path: cmd.APIPath(), Args: cmd.Words})
		rows = out
		return e
	})

	// AUDITED WITH THE EXACT TEXT, and audited whether it worked: the record of
	// what was attempted is the point. `cmd.Text` is rebuilt from the parsed
	// parts, so the row carries what the server understood rather than what the
	// model typed.
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	cn.recorder().Record(audit.Event{
		Action: "raw.command", TargetType: "router", TargetID: cn.routerID,
		TargetName: name, RouterID: cn.routerID, Outcome: outcome,
		Note: "ran a raw RouterOS command",
		Extra: []audit.KV{
			{Key: "command", Value: cmd.Text},
			{Key: "via", Value: "agent"},
		},
	})
	if err != nil {
		done(false, "The router refused it: "+safe.Message(err.Error())+".")
		return
	}
	done(true, "Ran on "+quoted(name)+": "+cmd.Text+"\n\n"+rawOutput(rows))
}

// rawOutput renders a reply for the model: capped, and wrapped in the untrusted
// block like every other router-supplied text.
func rawOutput(rows []routeros.Reply) string {
	kept := make([]map[string]string, 0, min(len(rows), rawOutputMaxRows))
	used, truncated := 0, false
	for _, r := range rows {
		if len(kept) >= rawOutputMaxRows {
			truncated = true
			break
		}
		row := map[string]string{}
		for k, v := range r {
			row[k] = v
		}
		b, _ := json.Marshal(row)
		if used+len(b) > rawOutputMaxBytes {
			truncated = true
			break
		}
		used += len(b)
		kept = append(kept, row)
	}
	body, _ := json.Marshal(struct {
		Rows      []map[string]string `json:"rows"`
		Returned  int                 `json:"returned"`
		Total     int                 `json:"total"`
		Truncated bool                `json:"truncated"`
	}{kept, len(kept), len(rows), truncated})
	return aicontext.Wrap(string(body))
}
