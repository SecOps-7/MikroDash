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
	"errors"
	"fmt"
	"strings"

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
	tok, err := cn.addProposal(&aiWriteProposal{raw: &cmd})
	if errors.Is(err, errProposalsFull) {
		return "There are already several things waiting for the operator to answer."
	}
	if err != nil {
		return "That command could not be put to the operator, so nothing was run."
	}

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
			// MASKED, NOT DROPPED: "this peer has a private key" is worth the
			// model knowing; the key is not.
			if rawcmd.Sensitive(k) && v != "" {
				v = audit.Set
			}
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

// ── bulk_execute: an ordered plan of raw commands ───────────────────────────
//
// ── ONE CONFIRMATION, AND WHY THAT IS NOT A WEAKENING ───────────────────────
//
// Six commands confirmed one at a time is six dialogs, and the sixth is answered
// without being read — which is how a confirmation becomes furniture. The plan is
// shown WHOLE, in order, and answered once: the operator reads six lines instead
// of six dialogs, and the gates are otherwise the raw command's own, including
// the router's name typed back.
//
// ── PARSED WHOLE, BEFORE ANYTHING RUNS ──────────────────────────────────────
//
// If any step will not parse, the plan is refused and NOTHING is proposed. A plan
// whose fourth step is malformed must not run its first three and then stop: that
// is the half-applied state this app avoids everywhere else.
//
// ── IT STOPS AT THE FIRST FAILURE ───────────────────────────────────────────
//
// A later step usually assumes the earlier ones took. Continuing past a refusal
// would apply a plan the operator never approved — the one that happens to skip
// step three — so execution stops, and every step is reported: what ran, what
// failed, and what was never attempted.

// rawPlanMaxSteps bounds a plan. Twenty is more than an operator will read
// carefully, and a plan nobody reads carefully is one nobody is confirming.
const rawPlanMaxSteps = 20

// runAIBulkTool is `bulk_execute`.
func (cn *conn) runAIBulkTool(tc aiprovider.ToolCall) string {
	var args struct {
		Commands []string `json:"commands"`
	}
	if json.Unmarshal([]byte(tc.Function.Arguments), &args) != nil {
		return "Those arguments were not valid JSON. Send `commands` as a list of RouterOS commands."
	}
	if _, refusal := cn.rawCommandGate("raw.plan"); refusal != "" {
		return refusal
	}
	if len(args.Commands) == 0 {
		return "That plan has no commands in it, so nothing was proposed."
	}
	if len(args.Commands) > rawPlanMaxSteps {
		return fmt.Sprintf("A plan may have at most %d commands; that one has %d. Nothing was "+
			"proposed: split it, so the operator can read what they are approving.",
			rawPlanMaxSteps, len(args.Commands))
	}
	plan := make([]rawcmd.Command, 0, len(args.Commands))
	for i, raw := range args.Commands {
		cmd, err := rawcmd.Parse(raw)
		if err != nil {
			// THE WHOLE PLAN IS REFUSED. Proposing the steps that did parse
			// would put a plan in front of the operator that is not the one the
			// model asked for.
			return fmt.Sprintf("Step %d was refused before anything was proposed: %s. "+
				"Nothing in the plan was proposed.", i+1, err.Error())
		}
		plan = append(plan, cmd)
	}
	return cn.raiseAIPlan(plan)
}

// raiseAIPlan puts the whole plan to the operator, once.
func (cn *conn) raiseAIPlan(plan []rawcmd.Command) string {
	tok, err := cn.addProposal(&aiWriteProposal{plan: plan})
	if errors.Is(err, errProposalsFull) {
		return "There are already several things waiting for the operator to answer."
	}
	if err != nil {
		return "That plan could not be put to the operator, so nothing was run."
	}

	EvAIPropose.Send(cn.srv.hub, cn.c, map[string]any{
		"token": tok, "kind": "command", "action": "plan",
		"label": fmt.Sprintf("RouterOS plan, %d commands", len(plan)),
		"name":  "", "command": planText(plan), "routerName": cn.rsession.Label,
		"typedName": true, "warnCode": "", "warning": map[string]any{}, "values": map[string]string{},
	})
	return fmt.Sprintf("Waiting for confirmation. MikroDash is showing the operator all %d "+
		"commands as one plan, to confirm by typing the router's name. They run in order and stop "+
		"at the first failure. Nothing has run yet: tell them what the plan does.", len(plan))
}

// planText is the plan as the dialog and the audit trail show it: the parsed
// commands, numbered, one per line.
func planText(plan []rawcmd.Command) string {
	var b strings.Builder
	for i, c := range plan {
		fmt.Fprintf(&b, "%d. %s\n", i+1, c.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}

// planStep is one step's outcome, for the model and the transcript.
type planStep struct {
	Step    int    `json:"step"`
	Command string `json:"command"`
	// Outcome is "ok", "failed", or "not attempted" for a step after a failure.
	Outcome string `json:"outcome"`
	Rows    int    `json:"rows,omitempty"`
	Error   string `json:"error,omitempty"`
}

// approveAIPlan runs a plan the operator accepted.
func (cn *conn) approveAIPlan(p *aiWriteProposal, confirm string) {
	plan := p.plan
	done := func(applied bool, text string) {
		EvAIWritten.Send(cn.srv.hub, cn.c, map[string]any{
			"applied": applied, "resource": "bulk_execute", "name": "", "text": text,
		})
	}
	if _, refusal := cn.rawCommandGate("raw.plan"); refusal != "" {
		done(false, refusal)
		return
	}
	name := cn.rsession.Label
	if name == "" || !strings.EqualFold(strings.TrimSpace(confirm), name) {
		done(false, "Not run: the router's name was not typed back correctly.")
		return
	}

	steps := make([]planStep, 0, len(plan))
	failed := false
	for i, cmd := range plan {
		if failed {
			steps = append(steps, planStep{Step: i + 1, Command: cmd.Text, Outcome: "not attempted"})
			continue
		}
		var rows []routeros.Reply
		// EACH STEP TAKES THE RATE LIMIT, as a single raw command does. A plan
		// is not a way to spend twenty writes on one permit.
		err := cn.inWriteQueue(func() error {
			out, e := cn.rsession.Exec(routeros.Cmd{Path: cmd.APIPath(), Args: cmd.Words})
			rows = out
			return e
		})
		outcome := "ok"
		if err != nil {
			outcome = "error"
			failed = true
		}
		// EVERY STEP IS AUDITED WITH ITS OWN TEXT, as a single command is: a
		// plan that half ran must leave a row per command that reached the
		// router, not one row saying "a plan ran".
		cn.recorder().Record(audit.Event{
			Action: "raw.command", TargetType: "router", TargetID: cn.routerID,
			TargetName: name, RouterID: cn.routerID, Outcome: outcome,
			Note: fmt.Sprintf("ran step %d of %d of a raw command plan", i+1, len(plan)),
			Extra: []audit.KV{
				{Key: "command", Value: cmd.Text},
				{Key: "via", Value: "agent"},
			},
		})
		step := planStep{Step: i + 1, Command: cmd.Text, Outcome: "ok", Rows: len(rows)}
		if err != nil {
			step.Outcome, step.Error = "failed", safe.Message(err.Error())
		}
		steps = append(steps, step)
	}

	body, _ := json.Marshal(struct {
		Steps  []planStep `json:"steps"`
		Ran    int        `json:"ran"`
		Failed bool       `json:"stoppedAtAFailure"`
	}{steps, countRan(steps), failed})
	head := fmt.Sprintf("Ran the plan on %s: %d of %d commands.", quoted(name), countRan(steps), len(plan))
	if failed {
		head = fmt.Sprintf("Stopped at a failure on %s: %d of %d commands ran, the rest were not "+
			"attempted.", quoted(name), countRan(steps), len(plan))
	}
	done(!failed, head+"\n\n"+aicontext.Wrap(string(body)))
}

func countRan(steps []planStep) int {
	n := 0
	for _, s := range steps {
		if s.Outcome != "not attempted" {
			n++
		}
	}
	return n
}
