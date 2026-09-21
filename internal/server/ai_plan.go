package server

// plan_changes: several row changes, one approval (MikroMCP's plan_changes and
// apply_plan, the operator's choice on 2026-09-21).
//
// ── ONE CARD, THEN THE ORDINARY PIPELINE, STEP BY STEP ──────────────────────
//
// A plan is change_row several times over, and nothing else: each step is
// checked when proposed as far as it can be without the steps before it having
// run (the resource, the permission, the field names, the values), shown to the
// operator on one card with the command it will send, and on approval run
// through writeRow or removeRow, the form's own paths, with every guard, audit
// row and undo entry that gives.
//
// IT STOPS AT THE FIRST STEP THAT DOES NOT APPLY, including one a guard warns
// about: a warning needs its own acknowledgement, and a plan approved as a whole
// must not carry one through unseen. The steps before it stay applied, and the
// answer says exactly which, so the operator (or the assistant, with `undo`)
// can take them back. There is no automatic rollback: RouterOS has none, and a
// reversal attempted after a failure is a second change nobody approved.
//
// A step may not change code: that needs the router's name typed back, alone.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mikrodash/internal/aiprovider"
	"mikrodash/internal/aitools"
	"mikrodash/internal/resource"
)

// rowPlanStep is one checked step, as it will run.
type rowPlanStep struct {
	resKey string
	req    *resRequest
	remove bool
	line   string // what the card shows
}

func (cn *conn) runAIPlanTool(tc aiprovider.ToolCall) string {
	var args struct {
		Title string `json:"title"`
		Steps []struct {
			Resource string         `json:"resource"`
			ID       string         `json:"id"`
			Values   map[string]any `json:"values"`
			Delete   bool           `json:"delete"`
		} `json:"steps"`
	}
	if json.Unmarshal([]byte(tc.Function.Arguments), &args) != nil {
		return "Those arguments were not valid JSON. Send `title` and `steps`, each step shaped as " +
			"one change_row call. Nothing was proposed."
	}
	if len(args.Steps) == 0 || len(args.Steps) > aitools.PlanMaxSteps {
		return fmt.Sprintf("A plan holds 1 to %d steps. Nothing was proposed.", aitools.PlanMaxSteps)
	}
	if cn.rsession == nil || cn.srv.store == nil {
		return "No device is selected, so nothing was proposed."
	}
	title := strings.TrimSpace(args.Title)
	if title == "" {
		title = "Plan"
	}
	steps := make([]rowPlanStep, 0, len(args.Steps))
	for i, s := range args.Steps {
		n := i + 1
		res := resource.ByKey(s.Resource)
		if res == nil {
			return fmt.Sprintf("Step %d names no such resource. Nothing was proposed.", n)
		}
		if !cn.canPage(res.Page, "write") {
			return fmt.Sprintf("Step %d: you do not have permission to change %s. Nothing was proposed.", n, res.Label)
		}
		if s.Delete {
			if s.ID == "" || len(s.Values) > 0 {
				return fmt.Sprintf("Step %d: a delete takes the row's `id` and nothing else. Nothing was proposed.", n)
			}
			steps = append(steps, rowPlanStep{resKey: res.Key, remove: true,
				req:  &resRequest{Resource: res.Key, ID: s.ID},
				line: res.Menu + "/remove =.id=" + s.ID})
			continue
		}
		editing := s.ID != ""
		if len(s.Values) == 0 {
			return fmt.Sprintf("Step %d gives no values, so it changes nothing. Nothing was proposed.", n)
		}
		if bad := undeclaredFields(res, s.Values, editing); bad > 0 {
			return fmt.Sprintf("Step %d: %d of those field names are not fields %s can set. Its settable "+
				"fields are: %s. Nothing was proposed.", n, bad, res.Label,
				strings.Join(settableFields(res, editing), ", "))
		}
		if namesCode(res, s.Values) {
			return fmt.Sprintf("Step %d changes RouterOS code, which needs the router's name typed back on "+
				"its own: propose it alone with change_row. Nothing was proposed.", n)
		}
		req := &resRequest{Resource: res.Key, ID: s.ID, Values: s.Values, Partial: editing}
		validated, errs := res.Validate(req.strValues(), editing)
		if len(errs) > 0 {
			return fmt.Sprintf("Step %d is not valid: %s. Nothing was proposed.", n, errs[0].Message)
		}
		steps = append(steps, rowPlanStep{resKey: res.Key, req: req,
			line: res.PreviewCommand(validated, s.ID)})
	}

	tok, err := cn.addProposal(&aiWriteProposal{rowPlan: steps, planTitle: title})
	if err != nil {
		if errors.Is(err, errProposalsFull) {
			return "There are already several changes waiting for the operator to answer. " +
				"Ask them to deal with those before proposing another."
		}
		return "That plan could not be put to the operator, so nothing was changed."
	}
	lines := make([]string, len(steps))
	for i, s := range steps {
		lines[i] = fmt.Sprintf("%d. %s", i+1, s.line)
	}
	EvAIPropose.Send(cn.srv.hub, cn.c, map[string]any{
		"token": tok, "kind": "plan", "resource": "", "label": title, "action": "plan",
		"name": fmt.Sprintf("%d steps", len(steps)), "command": strings.Join(lines, "\n"),
		"warnCode": "", "warning": map[string]any{}, "values": map[string]string{},
	})
	return fmt.Sprintf("Waiting for confirmation. MikroDash is showing the operator this %d-step plan "+
		"in one confirmation dialog; the steps run in order when they approve, and it stops at the "+
		"first one that fails or is flagged. Tell them what it does. Nothing has been applied yet.",
		len(steps))
}

// approveAIRowPlan runs an approved plan, stopping at the first step that does
// not apply, and reports each step.
func (cn *conn) approveAIRowPlan(p *aiWriteProposal) {
	var done []string
	stopped := ""
	for i, s := range p.rowPlan {
		res := resource.ByKey(s.resKey)
		if res == nil || !cn.canPage(res.Page, "write") {
			stopped = fmt.Sprintf("step %d: you no longer have permission to make it", i+1)
			break
		}
		// A FRESH REQUEST per step: writeRow and removeRow fill fields in on
		// the one they are given, and a proposal is not theirs to change.
		req := *s.req
		var out writeOutcome
		if s.remove {
			out = cn.removeRow(res, &req, "agent")
		} else {
			out = cn.writeRow(res, &req, "agent")
		}
		if out.Code != "" {
			why := aiRefusalText(res, out)
			if _, gate := guardGate(out); gate {
				why = "a safety check warned about it, so it needs its own confirmation: propose it " +
					"alone with change_row"
			}
			stopped = fmt.Sprintf("step %d (%s %s): %s", i+1, res.Label, quoted(out.Name), why)
			break
		}
		verb := out.Action + "d"
		if s.remove {
			verb = "deleted"
		}
		done = append(done, fmt.Sprintf("%d. %s %s %s", i+1, res.Label, quoted(out.Name), verb))
	}
	text := ""
	switch {
	case stopped == "" && len(done) == 1:
		text = fmt.Sprintf("Applied: the one step of %s, confirmed by reading it back: %s.",
			quoted(p.planTitle), done[0])
	case stopped == "":
		text = fmt.Sprintf("Applied: all %d steps of %s, each confirmed by reading it back: %s.",
			len(p.rowPlan), quoted(p.planTitle), strings.Join(done, "; "))
	case len(done) == 0:
		text = "Not applied: the plan stopped at " + stopped + ". Nothing was changed."
	default:
		text = fmt.Sprintf("Partly applied: %s. Then it stopped at %s. The steps after it were not "+
			"attempted, and the ones before it are still applied.", strings.Join(done, "; "), stopped)
	}
	cn.aiWritten(map[string]any{
		"applied": stopped == "", "resource": "plan", "name": p.planTitle, "text": text,
	})
}
