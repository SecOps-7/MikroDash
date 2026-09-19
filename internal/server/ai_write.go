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
// `writeRow` is what `res:save` uses, and `removeRow` is what `res:remove` uses. Permission, rate limit, fresh read,
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
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/aiprovider"
	"mikrodash/internal/rawcmd"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
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
	token  string
	resKey string
	rowID  string
	values map[string]any
	// code marks a write naming a Code field (a script's source, a scheduler's
	// on-event). Approving it needs the router's name typed back and the
	// raw-command gate passed again, as run_command does.
	code bool
	// remove marks a delete. Approval then runs `removeRow`, the form's own
	// delete path, rather than `writeRow`.
	remove bool
	// identity is the row's identity as the router held it when the proposal
	// was raised: approval refuses a row at the same `.id` that is no longer it,
	// as the form's `expectedIdentity` does. "" where nothing was read.
	identity string
	ack      string
	// actionKey, target and mode describe a `run_action` proposal instead of a
	// row write: resKey is empty on one and actionKey is empty on the other.
	actionKey string
	target    string
	mode      string
	// raw is a `run_command` proposal: one parsed RouterOS command. plan is a
	// `bulk_execute` one: the whole ordered list, parsed. Set on neither of the
	// other kinds.
	raw      *rawcmd.Command
	plan     []rawcmd.Command
	raisedAt time.Time
	// routerID is the router the proposal was raised on, stamped by
	// addProposal. It is only answerable there: approval runs against the
	// socket's router at that moment, and a `.id` from one router addresses a
	// different row, or nothing, on another.
	routerID string
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
		Delete   bool           `json:"delete"`
	}
	if json.Unmarshal([]byte(tc.Function.Arguments), &args) != nil {
		return "Those arguments were not valid JSON. Send `resource`, then `values` to create, " +
			"`id` and `values` to edit, or `id` and `delete: true` to delete."
	}
	res := resource.ByKey(args.Resource)
	if res == nil {
		// NOT ECHOED, for the reason an unknown tool name is not: a name the
		// model invented becomes established by repetition.
		return "There is no such resource. Use one of the names the tool lists."
	}
	// ── A FIELD THE RESOURCE DOES NOT HAVE IS REFUSED, NOT DROPPED ──────────
	//
	// Validate walks the resource's fields and ignores every other key, which is
	// right for a form that can only send its own fields and wrong for a model
	// that can send anything. Measured on the hAP AC2 on 2026-09-18: asked for an
	// input accept rule matching `src-address-list`, which fwFilter does not
	// declare, the assistant sent it, the key was dropped, and the router got an
	// UNCONDITIONAL input accept. A write must be what was asked or nothing.
	// Display fields are refused too: they are never sent, so naming one is the
	// same silent drop. The invented name is not echoed, as a resource's is not.
	//
	// AFTER THE PERMISSION CHECK, because the refusal lists the resource's
	// settable fields, and that is schema a viewer without write access was
	// never shown.
	//
	// RE-CHECKED, though `Permitted` already filtered the enum. The enum was
	// built when the question was asked and a role can be edited while an answer
	// is being composed.
	if !cn.canPage(res.Page, "write") {
		return "You do not have permission to change that, so nothing was proposed."
	}
	if !args.Delete {
		if bad := undeclaredFields(res, args.Values); bad > 0 {
			return fmt.Sprintf("%d of those field names are not fields %s can set, so nothing was changed. "+
				"Its settable fields are: %s.", bad, res.Label, strings.Join(settableFields(res), ", "))
		}
	}
	if cn.rsession == nil || cn.srv.store == nil {
		return "No device is selected, so nothing was proposed."
	}
	if args.Delete {
		return cn.proposeAIRemove(res, args.ID)
	}
	if len(args.Values) == 0 {
		return "No field values were given, so there is nothing to change."
	}

	settings, err := cn.srv.mergedSettings()
	if err != nil {
		return "The settings could not be read, so nothing was changed."
	}
	// PARTIAL, because the tool is documented as "the `values` to change": an
	// edit that names three fields must leave the rest as the router holds them.
	req := &resRequest{Resource: res.Key, ID: args.ID, Values: args.Values, Partial: args.ID != ""}

	// ── CODE IS HELD TO THE RAW-COMMAND GATE (operator's choice, 2026-09-18) ──
	//
	// A script's source or a scheduler's on-event IS a command, run later. So a
	// write naming one needs what run_command needs: a signed-in global
	// administrator, `aiAllowRawCommands` on, and the router's name typed back.
	// It is ALWAYS proposed, whatever aiConfirmWrites says; raiseAIProposal marks
	// it, and approval checks the name and the gate again.
	code := namesCode(res, args.Values)
	if code {
		if _, refusal := cn.rawCommandGate("raw.code"); refusal != "" {
			return refusal
		}
	}

	if !store.AIConfirmWrites(settings) && !code {
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
		if fp, gate := guardGate(out); gate {
			out.Detail = gateDetail(out)
			return cn.raiseAIProposal(res, req, out.Name, fp, out, nil)
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
	if p.editing && p.before != nil {
		// The row the operator will be shown, pinned: approval refuses a row at
		// this `.id` that is no longer it.
		req.ExpectedIdentity = res.IdentityOf(p.before)
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
	return cn.raiseAIProposal(res, req, p.name, ack, writeOutcome{Name: p.name, Detail: gate}, nil)
}

// proposeAIRemove puts a delete to the operator. ALWAYS, whatever
// `aiConfirmWrites` says.
//
// ── WHY A DELETE IS NEVER APPLIED WITHOUT ASKING ────────────────────────────
//
// The setting lets the assistant apply ordinary edits unprompted. A delete is
// not an ordinary edit: the row and every value in it are gone, and a model that
// has misread which row it is looking at removes the wrong one with nothing on
// screen until afterwards. Undo exists, but it is a repair, not a safeguard. So
// the operator sees the row as the router holds it now and approves it.
//
// The dialog is built from `prepareRemove`, the same reading the delete itself
// will make again at approval: the row's identity comes from the router, and a
// guard warning (removing the address MikroDash reaches the router on, say)
// shows in the dialog exactly as it would on the form.
func (cn *conn) proposeAIRemove(res *resource.Resource, id string) string {
	if id == "" {
		return "A delete needs the `id` of the row, exactly as the list tool reported it. " +
			"Nothing was proposed."
	}
	req := &resRequest{Resource: res.Key, ID: id}
	p, refusal := cn.prepareRemove(res, req)
	if p == nil {
		return aiRefusalText(res, refusal)
	}
	req.ExpectedIdentity = res.IdentityOf(p.row)
	ack := ""
	gate := map[string]any{}
	if p.verdict.Warned() {
		ack = p.verdict.Fingerprint
		gate["warning"] = p.verdict.Detail
		gate["code"] = p.verdict.Code
	}
	return cn.raiseAIProposal(res, req, p.name, ack, writeOutcome{Name: p.name, Detail: gate}, p.row)
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
//
// `removing` is the row a delete would remove, as the router holds it now, and
// nil for a create or an edit. It is a parameter rather than a field on the
// request, which is decoded from browser frames and has no business carrying
// the assistant's state.
func (cn *conn) raiseAIProposal(res *resource.Resource, req *resRequest,
	name, ack string, out writeOutcome, removing routeros.Reply) string {

	code := removing == nil && namesCode(res, req.Values)
	tok, err := cn.addProposal(&aiWriteProposal{
		resKey: res.Key, rowID: req.ID, values: req.Values, identity: req.ExpectedIdentity,
		remove: removing != nil, ack: ack, code: code,
	})
	if errors.Is(err, errProposalsFull) {
		return "There are already several changes waiting for the operator to answer. " +
			"Ask them to deal with those before proposing another."
	}
	if err != nil {
		return "That change could not be put to the operator, so nothing was changed."
	}

	// THE COMMAND IS BUILT SERVER-SIDE, and `PreviewCommand` masks secrets as
	// «set», so a proposal can show exactly what would be sent without putting a
	// pre-shared key on the screen.
	command := ""
	shown := map[string]string{}
	if removing != nil {
		// The command the delete will send, and the row as the router holds it
		// NOW, masked the same way, so the operator sees what will be gone.
		command = res.Menu + "/remove =.id=" + req.ID
		shown = histValues(auditValues(res, res.RowValues(removing)))
	} else if validated, errs := res.Validate(req.strValues(), req.ID != ""); len(errs) == 0 {
		command = res.PreviewCommand(validated, req.ID)
		// MASKED BY THE RULE THE AUDIT TRAIL ALREADY USES, rather than a second
		// one written here: `auditValues` substitutes «set»/«unset» for a
		// secret, and `histValues` is the existing map[string]any to
		// map[string]string conversion, bool normalisation included.
		shown = histValues(auditValues(res, anyValues(validated.Values)))
	}
	action := "update"
	switch {
	case removing != nil:
		action = "delete"
	case req.ID == "":
		action = "create"
	}
	warnCode, _ := out.Detail["code"].(string)
	warning, _ := out.Detail["warning"].(map[string]any)
	if warning == nil {
		warning = map[string]any{}
	}
	propose := map[string]any{
		"token": tok, "resource": res.Key, "label": res.Label,
		"action": action, "name": name, "command": command,
		"warnCode": warnCode, "warning": warning, "values": shown,
	}
	if code {
		propose["typedName"] = true
		propose["typedReason"] = "code"
		propose["routerName"] = cn.rsession.Label
	}
	EvAIPropose.Send(cn.srv.hub, cn.c, propose)
	if code {
		return "Waiting for confirmation. This changes code the router runs, so MikroDash is " +
			"asking the operator to type the router's name to confirm it. It has not been " +
			"applied yet: tell them what the code does."
	}

	// ── WORDED AS A CHANGE IN PROGRESS, NOT A SUGGESTION ────────────────────
	//
	// The operator asked for the assistant to act rather than to propose. It
	// does: the change is built, checked and waiting on one press. What it must
	// not do is call it applied, because nothing has reached the router yet, and
	// the operator would act on that claim.
	if warnCode != "" {
		return "Waiting for confirmation. MikroDash flagged a safety warning on this change and " +
			"is showing it to the operator in a confirmation dialog; it is applied when they " +
			"confirm. Tell them what the change does and why it was flagged. It has not been " +
			"applied yet."
	}
	if removing != nil {
		return "Waiting for confirmation. MikroDash is showing the operator this delete in a " +
			"confirmation dialog, as it does for every delete; the row is removed when they " +
			"confirm. Tell them what will be deleted. It has not been deleted yet."
	}
	return "Waiting for confirmation. MikroDash is showing the operator this change in a " +
		"confirmation dialog, and it is applied when they confirm. Tell them what the change " +
		"does. It has not been applied yet."
}

// aiWriteApprove performs a proposal the operator accepted.
func (cn *conn) aiWriteApprove(raw json.RawMessage) {
	p := cn.takeAIProposal(raw)
	if p != nil && len(p.plan) > 0 {
		var in struct {
			Confirm string `json:"confirm"`
		}
		_ = json.Unmarshal(raw, &in)
		cn.approveAIPlan(p, in.Confirm)
		return
	}
	if p != nil && p.raw != nil {
		var in struct {
			Confirm string `json:"confirm"`
		}
		_ = json.Unmarshal(raw, &in)
		cn.approveAIRawCommand(p, in.Confirm)
		return
	}
	if p != nil && p.actionKey != "" {
		// The operator's typed-back router name, and a login the action needs,
		// travel on THIS frame, never on the tool call: see aitools/actions.go.
		var in actionApproval
		_ = json.Unmarshal(raw, &in)
		cn.approveAIAction(p, in)
		return
	}
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
	// A CODE CHANGE needs the gate passed again and the router's name typed back,
	// on THIS frame, as run_command's approval does.
	if p.code {
		var in struct {
			Confirm string `json:"confirm"`
		}
		_ = json.Unmarshal(raw, &in)
		if _, refusal := cn.rawCommandGate("raw.code"); refusal != "" {
			EvAIWritten.Send(cn.srv.hub, cn.c, map[string]any{
				"applied": false, "resource": res.Key, "name": "", "text": refusal,
			})
			return
		}
		if name := cn.rsession.Label; name == "" || !strings.EqualFold(strings.TrimSpace(in.Confirm), name) {
			EvAIWritten.Send(cn.srv.hub, cn.c, map[string]any{
				"applied": false, "resource": res.Key, "name": "",
				"text": "Not applied: the router's name was not typed back correctly.",
			})
			return
		}
	}
	// PARTIAL ON AN EDIT, as change_row built it. Without it the approved write
	// CLEARED every clearable field the edit did not name: measured on the CHR on
	// 2026-09-18, an approved change to a script's source also emptied its policy
	// and its comment. Every dialog-approved partial edit had done this.
	req := &resRequest{Resource: res.Key, ID: p.rowID, Values: p.values, Ack: p.ack, Partial: p.rowID != "",
		ExpectedIdentity: p.identity}
	var out writeOutcome
	if p.remove {
		out = cn.removeRow(res, req, "agent")
	} else {
		out = cn.writeRow(res, req, "agent")
	}

	text := fmt.Sprintf("Applied: the %s %s was %sd and confirmed by reading it back.",
		res.Label, quoted(out.Name), out.Action)
	if p.remove && out.Code == "" {
		text = fmt.Sprintf("Applied: the %s %s was deleted, and reading the table back "+
			"confirmed it is gone.", res.Label, quoted(out.Name))
	}
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
		if name == "" {
			name = p.actionKey
		}
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
	// RAISED ON ANOTHER ROUTER: refused, and answered as "no longer available",
	// which is what it is. releaseRouter drops them anyway; this is the check
	// that does not depend on the switch path remembering to.
	if p.routerID != cn.routerID {
		return nil
	}
	return p
}

// errProposalsFull is addProposal refusing a proposal past aiMaxProposals.
var errProposalsFull = errors.New("too many proposals waiting")

// addProposal mints a token for p, sweeps the expired proposals, refuses past
// aiMaxProposals, and stores it. The ONE place a proposal is raised, whatever
// its kind: the four raise functions each carried their own copy of this.
func (cn *conn) addProposal(p *aiWriteProposal) (string, error) {
	tok, err := aiProposalToken()
	if err != nil {
		return "", err
	}
	cn.proposeMu.Lock()
	defer cn.proposeMu.Unlock()
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
		return "", errProposalsFull
	}
	p.token, p.raisedAt, p.routerID = tok, now, cn.routerID
	cn.proposals[tok] = p
	return tok, nil
}

func aiProposalToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// guardGate reports whether an outcome is a guard ASKING rather than refusing.
//
// ── THE ONE GENUINELY NEW LINK, SO IT IS NAMED AND TESTED ───────────────────
//
// Everything else about an agent write is the pipeline the forms already use.
// This is the exception: a warned verdict comes back from `writeRow` as an
// outcome carrying a fingerprint, with nothing written, and the assistant has to
// turn that into a proposal instead of reporting a failure. That is what makes a
// lockout guard prompt even when prompts are switched off.
//
// It was inline, wrapped around a `writeRow` call that needs a router, so
// nothing could exercise it — and "covered by tests" was a claim about the
// guards themselves rather than about this step.
//
// A FINGERPRINT IS THE SIGNAL. `ackGate` puts one on every warning and on
// nothing else, which is also what the browser routes on, so the assistant and
// the form agree by construction rather than by coincidence.
func guardGate(out writeOutcome) (string, bool) {
	if out.Code == "" {
		return "", false // a success is not a question
	}
	fp, _ := out.Detail["fingerprint"].(string)
	return fp, fp != ""
}

// gateDetail is a guard gate's detail with its CODE put back.
//
// `ackGate` moves the code out of the detail and into the outcome, so a proposal
// built straight from that outcome carried a warning with no code — and
// `raiseAIProposal` reads the code to decide whether to say anything about it.
// Measured on the CHR: a queue covering MikroDash's own address was proposed
// with an empty `warnCode`, so the dialog showed the change with no warning.
func gateDetail(out writeOutcome) map[string]any {
	detail := map[string]any{}
	for k, v := range out.Detail {
		detail[k] = v
	}
	detail["code"] = out.Code
	return detail
}

// aiRefusalText turns a write outcome into something the model can relay.
//
// ── THE SAME SENTENCES THE BROWSER SHOWS, AND FOR THE SAME REASON ───────────
//
// A refusal code means nothing to an operator, and a model handed `stale-row`
// will invent an explanation for it. These say what the page says, so the
// assistant and the form describe one refusal the same way.
// guardRefusalText names the rule a refused write broke, in words.
func guardRefusalText(out writeOutcome) string {
	rule, _ := out.Detail["rule"].(string)
	switch rule {
	case "protected-account":
		return "that is the RouterOS account MikroDash signs in with."
	case "protected-group":
		return "that is the RouterOS group MikroDash signs in with."
	case "protected-name-value":
		return "that name belongs to the account MikroDash signs in with."
	case "protected-group-value":
		return "users cannot be placed in, or edited while in, the group MikroDash signs in with."
	case "self-unresolved":
		return "MikroDash cannot identify its own account on this router, so user changes are refused."
	}
	return "a safety rule refused it."
}

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
	case "not-editable":
		return "Not applied: rows of that kind can be viewed and deleted, not edited."
	case "not-removable":
		return "Not applied: that row cannot be deleted, only edited."
	case "invalid":
		return "Not applied: " + invalidText(out) + " Check the field names and values against " +
			"what the list tool reported."
	case "router-denied":
		return "Not applied: the router refused it, because the API user MikroDash signs in " +
			"with lacks permission."
	case "write-failed":
		// THE ROUTER'S OWN WORDS, as the page shows them: they name the menu and
		// the property, and without them the model can only say "refused" and
		// then guess why out loud.
		if msg, _ := out.Detail["message"].(string); msg != "" {
			return "Not applied: the router refused the change: " + msg
		}
		return "Not applied: the router refused the change."
	case "guard-refused":
		return "Not applied, and it cannot be approved: " + guardRefusalText(out) + " Tell the " +
			"operator to make this change in WinBox if it is really wanted."
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
//
// The name is the router's, and the sentence goes back to the model as well as
// into the transcript, so it is Go-quoted: a `"` or a line break inside it is
// escaped rather than closing the quotes and writing text that reads as ours.
func quoted(name string) string {
	if strings.TrimSpace(name) == "" {
		return "row"
	}
	return strconv.Quote(name)
}

// undeclaredFields counts the keys in `values` that are not settable fields of
// the resource: unknown names, and Display fields, which are never sent.
func undeclaredFields(res *resource.Resource, values map[string]any) int {
	ok := map[string]bool{}
	for _, f := range settableFields(res) {
		ok[f] = true
	}
	bad := 0
	for k := range values {
		if !ok[k] {
			bad++
		}
	}
	return bad
}

// settableFields is every field a write may name, in declaration order.
func settableFields(res *resource.Resource) []string {
	var out []string
	for _, f := range res.Fields {
		if !f.Display {
			out = append(out, f.Name)
		}
	}
	return out
}

// namesCode reports whether a change_row names a Code field at all. Deliberately
// wider than CodeChange: the stored row is not read yet, and a proposal that
// asks for a typed name it did not strictly need is the safe way to be wrong.
func namesCode(res *resource.Resource, values map[string]any) bool {
	for _, f := range res.Fields {
		if _, ok := values[f.Name]; ok && f.Code {
			return true
		}
	}
	return false
}
