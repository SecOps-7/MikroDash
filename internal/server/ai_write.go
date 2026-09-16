package server

// The assistant's write tool (#98, slice 4).
//
// ── THE MODEL PROPOSES A ROW, NEVER A COMMAND ───────────────────────────────
//
// What arrives is a resource key this registry declares, an optional row id and
// a set of field values. There is no path from here to a RouterOS sentence the
// model composed: `BuildArgs` builds the words from what `Validate` accepted, and
// the menu comes from the resource rather than from the call.
//
// ── AND IT REACHES THE ROUTER THROUGH THE FORM'S OWN PIPELINE ───────────────
//
// `writeRow` is what `res:save` uses. Permission, rate limit, fresh read,
// staleness, read-only rows, guards, the write, read-back confirmation, undo
// history and the audit row — all of it, unchanged, because it is the same
// function rather than a copy that agrees with it today.
//
// The only thing the assistant adds is `via: agent` in the audit Extra, so the
// Audit Trail can answer "what did the AI change" without a second log.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"mikrodash/internal/aiprovider"
	"mikrodash/internal/resource"
	"mikrodash/internal/store"
)

const (
	// aiProposalTTL bounds how long an unanswered proposal stays answerable.
	//
	// A proposal describes the router as it was when the model looked. Approving
	// one an hour later would re-check staleness and the guards — `writeRow`
	// runs the whole pipeline again — so this is not the safety boundary; it is
	// there so a forgotten dialog cannot be answered by accident much later.
	aiProposalTTL = 10 * time.Minute
	// aiMaxProposals bounds what one socket can accumulate. A model in a loop
	// proposing repeatedly must not be able to grow this without limit.
	aiMaxProposals = 8
)

// aiWriteProposal is a change the model asked for and the operator has not
// answered. It holds the INTENT, not a prepared write: approval re-runs the
// whole pipeline rather than replaying a decision made minutes ago.
type aiWriteProposal struct {
	token    string
	resKey   string
	rowID    string
	values   map[string]any
	ack      string
	raisedAt time.Time
}

// runAIWriteTool is `change_row`.
//
// Like the read tools it never returns an error: a refusal is an ANSWER to the
// model, which must be able to tell the operator what it could not do.
func (cn *conn) runAIWriteTool(tc aiprovider.ToolCall) string {
	var args struct {
		Resource string         `json:"resource"`
		ID       string         `json:"id"`
		Values   map[string]any `json:"values"`
	}
	if json.Unmarshal([]byte(tc.Function.Arguments), &args) != nil {
		return "Those arguments were not valid JSON. Send `resource`, `values`, and `id` only " +
			"when editing an existing row."
	}
	res := resource.ByKey(args.Resource)
	if res == nil {
		// NOT ECHOED, for the reason an unknown tool name is not: a name the
		// model invented becomes established by repetition.
		return "There is no such resource. Use one of the names the tool lists."
	}
	// RE-CHECKED, though `Permitted` already filtered the enum. The enum was
	// built when the question was asked and a role can be edited while an answer
	// is being composed.
	if !cn.canPage(res.Page, "write") {
		return "You do not have permission to change that, so nothing was proposed."
	}
	if cn.rsession == nil || cn.srv.store == nil {
		return "No device is selected, so nothing was proposed."
	}
	if len(args.Values) == 0 {
		return "No field values were given, so there is nothing to change."
	}

	settings, err := cn.srv.mergedSettings()
	if err != nil {
		return "The settings could not be read, so nothing was changed."
	}
	req := &resRequest{Resource: res.Key, ID: args.ID, Values: args.Values}

	if !store.AIConfirmWrites(settings) {
		// ── PROMPTS ARE OFF, SO THE WRITE IS ATTEMPTED ──────────────────────
		//
		// And the guards still prompt, with no special casing: a warning makes
		// `writeRow` return a gate carrying a fingerprint WITHOUT writing
		// anything, exactly as it does for a form that has not acknowledged one.
		// So "a lockout guard always asks" is the existing mechanism rather than
		// a rule this file has to remember.
		out := cn.writeRow(res, req, "agent")
		if out.Code == "" {
			return fmt.Sprintf("Applied. The %s %s was %sd on the router and confirmed by "+
				"reading it back.", res.Label, quoted(out.Name), out.Action)
		}
		if fp, _ := out.Detail["fingerprint"].(string); fp != "" {
			return cn.raiseAIProposal(res, req, out.Name, fp, out)
		}
		return aiRefusalText(res, out)
	}

	// ── PROMPTS ARE ON, SO NOTHING IS WRITTEN YET ───────────────────────────
	//
	// `prepareWrite` still runs, because the operator must be shown what the
	// SERVER worked out rather than what the model says it intends: the row as
	// the router currently holds it, whether it is still there, and any guard
	// verdict. A dialog built from model prose would be the model reviewing
	// itself.
	p, refusal := cn.prepareWrite(res, req)
	if p == nil {
		return aiRefusalText(res, refusal)
	}
	ack := ""
	if p.verdict.Warned() {
		ack = p.verdict.Fingerprint
	}
	gate := map[string]any{}
	if p.verdict.Warned() {
		gate["warning"] = p.verdict.Detail
		gate["code"] = p.verdict.Code
	}
	return cn.raiseAIProposal(res, req, p.name, ack, writeOutcome{Name: p.name, Detail: gate})
}

// raiseAIProposal stores the intent and puts it in front of the operator.
//
// ── IT RETURNS AT ONCE, AND THAT IS THE WHOLE DESIGN ────────────────────────
//
// This runs inside a tool call, which is inside an HTTP request to the model,
// bounded by the configured timeout — a minute by default. A human may take
// considerably longer than a minute, so blocking here would turn "the operator
// went to look at the router" into a failed exchange and a blank page.
//
// So the model is told a proposal was raised and finishes its turn saying so.
// The approval arrives later, on its own frame, and the write happens then.
func (cn *conn) raiseAIProposal(res *resource.Resource, req *resRequest,
	name, ack string, out writeOutcome) string {

	tok, err := aiProposalToken()
	if err != nil {
		return "That change could not be put to the operator, so nothing was changed."
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
		return "There are already several changes waiting for the operator to answer. " +
			"Ask them to deal with those before proposing another."
	}
	cn.proposals[tok] = &aiWriteProposal{
		token: tok, resKey: res.Key, rowID: req.ID, values: req.Values,
		ack: ack, raisedAt: now,
	}
	cn.proposeMu.Unlock()

	// THE COMMAND IS BUILT SERVER-SIDE, and `PreviewCommand` masks secrets as
	// «set», so a proposal can show exactly what would be sent without putting a
	// pre-shared key on the screen.
	command := ""
	shown := map[string]string{}
	if validated, errs := res.Validate(req.strValues(), req.ID != ""); len(errs) == 0 {
		command = res.PreviewCommand(validated, req.ID)
		// MASKED BY THE RULE THE AUDIT TRAIL ALREADY USES, rather than a second
		// one written here: `auditValues` substitutes «set»/«unset» for a
		// secret, and `histValues` is the existing map[string]any to
		// map[string]string conversion, bool normalisation included.
		shown = histValues(auditValues(res, anyValues(validated.Values)))
	}
	action := "update"
	if req.ID == "" {
		action = "create"
	}
	warnCode, _ := out.Detail["code"].(string)
	warning, _ := out.Detail["warning"].(map[string]any)
	if warning == nil {
		warning = map[string]any{}
	}
	EvAIPropose.Send(cn.srv.hub, cn.c, map[string]any{
		"token": tok, "resource": res.Key, "label": res.Label,
		"action": action, "name": name, "command": command,
		"warnCode": warnCode, "warning": warning, "values": shown,
	})

	if warnCode != "" {
		return "Not applied. That change carries a safety warning, so it has been put to the " +
			"operator to confirm. Tell them what you proposed and why, and that MikroDash " +
			"flagged it."
	}
	return "Not applied yet. The change has been put to the operator for approval. Tell them " +
		"what you proposed and why; do not say it has been done."
}

// aiWriteApprove performs a proposal the operator accepted.
func (cn *conn) aiWriteApprove(raw json.RawMessage) {
	p := cn.takeAIProposal(raw)
	if p == nil {
		EvAIWritten.Send(cn.srv.hub, cn.c, map[string]any{
			"applied": false, "resource": "", "name": "",
			"text": "That proposal is no longer available. Ask again if you still want it.",
		})
		return
	}
	res := resource.ByKey(p.resKey)
	if res == nil {
		EvAIWritten.Send(cn.srv.hub, cn.c, map[string]any{
			"applied": false, "resource": p.resKey, "name": "",
			"text": "That resource is not available on this build.",
		})
		return
	}
	// CHECKED AGAIN AT APPROVAL. The proposal may have been raised minutes ago,
	// and a role can be edited in between.
	if !cn.canPage(res.Page, "write") {
		EvAIWritten.Send(cn.srv.hub, cn.c, map[string]any{
			"applied": false, "resource": res.Key, "name": "",
			"text": "You do not have permission to make that change.",
		})
		return
	}

	// THE WHOLE PIPELINE RUNS NOW, not when the proposal was raised: the row is
	// read fresh, staleness is re-checked and the guards run again against the
	// router as it is at this moment. `ack` carries the fingerprint of the
	// warning the operator was shown, so a guard whose verdict has CHANGED since
	// then gates again rather than being waved through.
	req := &resRequest{Resource: res.Key, ID: p.rowID, Values: p.values, Ack: p.ack}
	out := cn.writeRow(res, req, "agent")

	text := fmt.Sprintf("Applied: the %s %s was %sd and confirmed by reading it back.",
		res.Label, quoted(out.Name), out.Action)
	if out.Code != "" {
		text = aiRefusalText(res, out)
	}
	EvAIWritten.Send(cn.srv.hub, cn.c, map[string]any{
		"applied": out.Code == "", "resource": res.Key, "name": out.Name, "text": text,
	})
}

// aiWriteReject drops a proposal the operator declined. Nothing is written and
// nothing is audited: a change that did not happen is not an event.
func (cn *conn) aiWriteReject(raw json.RawMessage) {
	p := cn.takeAIProposal(raw)
	name := ""
	if p != nil {
		name = p.resKey
	}
	EvAIWritten.Send(cn.srv.hub, cn.c, map[string]any{
		"applied": false, "resource": name, "name": "",
		"text": "Not applied. You declined that change.",
	})
}

// takeAIProposal resolves a token and REMOVES it.
//
// Single use, which is the point: without removal an approval frame could be
// replayed, and one press of Approve would become as many writes as somebody
// cared to send.
func (cn *conn) takeAIProposal(raw json.RawMessage) *aiWriteProposal {
	var in struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Token == "" {
		return nil
	}
	cn.proposeMu.Lock()
	defer cn.proposeMu.Unlock()
	p := cn.proposals[in.Token]
	if p == nil {
		return nil
	}
	delete(cn.proposals, in.Token)
	if time.Since(p.raisedAt) > aiProposalTTL {
		return nil
	}
	return p
}

func aiProposalToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// aiRefusalText turns a write outcome into something the model can relay.
//
// ── THE SAME SENTENCES THE BROWSER SHOWS, AND FOR THE SAME REASON ───────────
//
// A refusal code means nothing to an operator, and a model handed `stale-row`
// will invent an explanation for it. These say what the page says, so the
// assistant and the form describe one refusal the same way.
func aiRefusalText(res *resource.Resource, out writeOutcome) string {
	switch out.Code {
	case "denied":
		return "Not applied: you may not change that."
	case "unavailable":
		return "Not applied: the router is not reachable."
	case "stale-row":
		return "Not applied: that row changed on the router since it was read. List it again " +
			"and work from the current values."
	case "read-only-row":
		return "Not applied: that row cannot be edited here."
	case "not-creatable":
		return "Not applied: rows of that kind cannot be created."
	case "invalid":
		return "Not applied: " + invalidText(out) + " Check the field names and values against " +
			"what the list tool reported."
	case "router-denied":
		return "Not applied: the router refused it, because the API user MikroDash signs in " +
			"with lacks permission."
	case "write-failed":
		return "Not applied: the router refused the change."
	case "guard-not-ported":
		return "Not applied: that change needs a safety check MikroDash cannot run yet, so it " +
			"was refused rather than attempted."
	case "rate-limited":
		return "Not applied: too many changes to this router in the last minute."
	case "outcome-unknown":
		return "The router accepted the change but reading it back did not confirm it, so " +
			"MikroDash cannot say whether it took effect. Tell the operator to check the " +
			"table before trying again."
	}
	if out.Code == "" {
		return "Applied."
	}
	return "Not applied: " + res.Label + " could not be changed."
}

// invalidText renders the validator's own messages, which name the field.
func invalidText(out writeOutcome) string {
	errs, ok := out.Detail["errors"].([]resource.Error)
	if !ok || len(errs) == 0 {
		return "those values were not valid."
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, e.Field+": "+e.Message)
	}
	return strings.Join(parts, "; ") + "."
}

// anyValues widens the validator's strings so the audit masker can take them.
func anyValues(v map[string]string) map[string]any {
	out := make(map[string]any, len(v))
	for k, s := range v {
		out[k] = s
	}
	return out
}

// quoted names a row without pretending an unnamed one has a name. A firewall
// rule has a composite identity and no single name, and "the Firewall Rule ”
// was updated" reads as a bug.
func quoted(name string) string {
	if strings.TrimSpace(name) == "" {
		return "row"
	}
	return "\"" + name + "\""
}
