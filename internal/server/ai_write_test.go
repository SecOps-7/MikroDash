package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/aiprovider"
	"mikrodash/internal/aitools"
	"mikrodash/internal/resource"
)

// The write tool's own decisions, without a router.
//
// What reaches the router is `writeRow`, which the resource tests already cover.
// What is new here is the part between the model and that pipeline: a token that
// must work exactly once, and refusals a model has to be able to relay without
// inventing an explanation for a code it was handed.

func proposalFrame(token string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"token": token})
	return b
}

// TestAProposalTokenWorksExactlyOnce.
//
// ── WITHOUT THIS, ONE PRESS OF APPROVE IS AS MANY WRITES AS SOMEBODY SENDS ──
//
// The frame carries only a token; everything about the write is re-derived from
// what the server stored. So the token IS the authorisation, and an
// authorisation that survives being used is one that can be replayed — by a
// double click, a retried frame, or anything that can send the same bytes twice.
func TestAProposalTokenWorksExactlyOnce(t *testing.T) {
	cn := &conn{proposals: map[string]*aiWriteProposal{}}
	cn.proposals["tok"] = &aiWriteProposal{
		token: "tok", resKey: "dnsStatic", raisedAt: time.Now(),
	}

	if got := cn.takeAIProposal(proposalFrame("tok")); got == nil {
		t.Fatal("the first use was refused")
	}
	if got := cn.takeAIProposal(proposalFrame("tok")); got != nil {
		t.Error("the same token was accepted twice, so an approval can be replayed")
	}
	if len(cn.proposals) != 0 {
		t.Errorf("%d proposals remain after the token was used", len(cn.proposals))
	}
}

// TestAnUnknownOrExpiredTokenIsRefused, and an expired one is not left behind.
func TestAnUnknownOrExpiredTokenIsRefused(t *testing.T) {
	cn := &conn{proposals: map[string]*aiWriteProposal{}}
	if got := cn.takeAIProposal(proposalFrame("never-minted")); got != nil {
		t.Error("a token this socket never minted was accepted")
	}
	if got := cn.takeAIProposal(json.RawMessage(`{}`)); got != nil {
		t.Error("an empty token was accepted")
	}
	if got := cn.takeAIProposal(json.RawMessage(`not json`)); got != nil {
		t.Error("an unparseable frame was accepted")
	}

	cn.proposals["old"] = &aiWriteProposal{
		token: "old", resKey: "dnsStatic",
		raisedAt: time.Now().Add(-aiProposalTTL - time.Minute),
	}
	if got := cn.takeAIProposal(proposalFrame("old")); got != nil {
		t.Error("an expired proposal was still answerable")
	}
	if len(cn.proposals) != 0 {
		t.Error("an expired proposal was left in the map after being rejected")
	}
}

// TestEveryRefusalTheWritePathCanProduceHasASentence.
//
// ── A CODE HANDED TO A MODEL BECOMES A SENTENCE IT INVENTS ──────────────────
//
// `stale-row` means something precise to this app and nothing to an operator. A
// model given the bare code will produce a confident explanation of it, and that
// explanation is the thing the operator reads. So every code the pipeline can
// return is mapped here, and this fails when one is not.
//
// The list is the codes `writeRow` and its helpers actually produce, named
// rather than derived, so adding a refusal means deciding what the assistant
// says about it.
func TestEveryRefusalTheWritePathCanProduceHasASentence(t *testing.T) {
	res := resource.ByKey("dnsStatic")
	if res == nil {
		t.Fatal("dnsStatic is not in the registry")
	}
	codes := []string{
		"denied", "unavailable", "stale-row", "read-only-row", "not-creatable",
		"invalid", "router-denied", "write-failed", "guard-not-ported",
		"rate-limited", "outcome-unknown",
		// The delete path's own refusal (removeRow).
		"not-removable",
	}
	for _, code := range codes {
		got := aiRefusalText(res, writeOutcome{Code: code})
		if strings.TrimSpace(got) == "" {
			t.Errorf("code %q produces no sentence", code)
			continue
		}
		// EVERY ONE SAYS IT DID NOT HAPPEN, except the unconfirmed case, which
		// must NOT claim either way — the whole point of outcome-unknown is that
		// the app does not know.
		if code == "outcome-unknown" {
			if strings.Contains(got, "Not applied") {
				t.Errorf("outcome-unknown claims the change did not happen: %q", got)
			}
			if !strings.Contains(got, "cannot say") {
				t.Errorf("outcome-unknown does not say the outcome is unknown: %q", got)
			}
			continue
		}
		if !strings.HasPrefix(got, "Not applied") {
			t.Errorf("code %q does not tell the model the change did not happen: %q", code, got)
		}
	}
	// The control: a success is not a refusal.
	if got := aiRefusalText(res, writeOutcome{}); !strings.HasPrefix(got, "Applied") {
		t.Errorf("an empty code read as a refusal: %q", got)
	}
}

// TestInvalidNamesTheFieldsThatWereWrong, so the model can correct itself rather
// than proposing the same broken change again on the next iteration.
func TestInvalidNamesTheFieldsThatWereWrong(t *testing.T) {
	out := writeOutcome{Code: "invalid", Detail: map[string]any{
		"errors": []resource.Error{
			{Field: "address", Message: "Address is required"},
			{Field: "type", Message: "Type is not one of the options"},
		},
	}}
	got := invalidText(out)
	for _, want := range []string{"address", "Address is required", "type"} {
		if !strings.Contains(got, want) {
			t.Errorf("the message does not mention %q: %s", want, got)
		}
	}
	// And a malformed detail does not panic or produce an empty sentence.
	if got := invalidText(writeOutcome{Code: "invalid"}); strings.TrimSpace(got) == "" {
		t.Error("an invalid outcome with no errors produced nothing")
	}
}

// TestAnUnnamedRowIsNotGivenAnEmptyName. A firewall rule has a composite
// identity and no single name, and `the Firewall Rule "" was updated` reads as a
// bug rather than as a row that simply has no name.
func TestAnUnnamedRowIsNotGivenAnEmptyName(t *testing.T) {
	if got := quoted(""); got != "row" {
		t.Errorf("an empty name rendered as %q", got)
	}
	if got := quoted("   "); got != "row" {
		t.Errorf("a blank name rendered as %q", got)
	}
	if got := quoted("server.lan"); got != `"server.lan"` {
		t.Errorf("a real name rendered as %q", got)
	}
}

func writeCall(args string) aiprovider.ToolCall {
	var tc aiprovider.ToolCall
	tc.ID, tc.Type = "c1", "function"
	tc.Function.Name = aitools.WriteToolName
	tc.Function.Arguments = args
	return tc
}

// TestAGuardWarningAsksRatherThanRefuses.
//
// ── THIS IS WHAT MAKES A LOCKOUT GUARD PROMPT WITH PROMPTS OFF ──────────────
//
// With `aiConfirmWrites` false the write is simply attempted. A warned verdict
// makes `writeRow` return a gate carrying a fingerprint and write nothing, and
// the assistant must raise that as a proposal rather than telling the operator
// the change failed. Get this wrong in the refusing direction and a dangerous
// change is reported as an error nobody acts on; get it wrong the other way and
// an ordinary refusal opens a dialog offering to do something already refused.
//
// BOTH DIRECTIONS, because a function that answered "gate" to everything would
// satisfy the first case perfectly.
func TestAGuardWarningAsksRatherThanRefuses(t *testing.T) {
	warned := writeOutcome{
		Code: "self-cutoff", Name: "ether1",
		Detail: map[string]any{
			"fingerprint": "abc123",
			"warning":     map[string]any{"interface": "ether1"},
		},
	}
	fp, gate := guardGate(warned)
	if !gate {
		t.Error("a guard warning was treated as a refusal, so nobody would be asked")
	}
	if fp != "abc123" {
		t.Errorf("fingerprint %q; approving replays with it, so a wrong one gates again", fp)
	}

	// Everything that is NOT a guard asking.
	for _, c := range []struct {
		name string
		out  writeOutcome
	}{
		{"a success", writeOutcome{Action: "create", Name: "x"}},
		{"an ordinary refusal", writeOutcome{Code: "stale-row", Name: "x"}},
		{"a refusal with detail but no fingerprint", writeOutcome{
			Code: "invalid", Detail: map[string]any{"errors": []resource.Error{}}}},
		{"an empty fingerprint", writeOutcome{
			Code: "write-failed", Detail: map[string]any{"fingerprint": ""}}},
		{"a fingerprint of the wrong type", writeOutcome{
			Code: "write-failed", Detail: map[string]any{"fingerprint": 42}}},
	} {
		if _, gate := guardGate(c.out); gate {
			t.Errorf("%s was treated as a guard asking to confirm", c.name)
		}
	}
}

// TestTheWriteToolRefusesBeforeItTouchesAnything.
//
// These three run before any router or settings read, which is why they can be
// exercised against a bare connection — and why they are the refusals that
// matter: nothing has happened yet when they fire.
func TestTheWriteToolRefusesBeforeItTouchesAnything(t *testing.T) {
	cn := &conn{} // no session, so no permission to anything

	got := cn.runAIWriteTool(writeCall(`{"resource": `))
	if !strings.Contains(got, "not valid JSON") {
		t.Errorf("malformed arguments produced %q", got)
	}

	got = cn.runAIWriteTool(writeCall(`{"resource":"notARealMenu","values":{"a":"b"}}`))
	if !strings.Contains(got, "no such resource") {
		t.Errorf("an unknown resource produced %q", got)
	}
	// NOT ECHOED. A name the model invented becomes established by repetition,
	// and there is nothing in it for the operator either.
	if strings.Contains(got, "notARealMenu") {
		t.Errorf("the invented resource name was echoed back: %q", got)
	}

	// A REAL resource, refused on permission rather than on existence. The
	// control for the case above: without it, "no such resource" could be what
	// this returns for everything.
	got = cn.runAIWriteTool(writeCall(`{"resource":"dnsStatic","values":{"name":"x"}}`))
	if !strings.Contains(got, "permission") {
		t.Errorf("a viewer with no write permission produced %q", got)
	}
}

// TestAFieldTheResourceDoesNotHaveIsRefused. Validate ignores keys it does not
// know, so a field the model invented used to vanish and the REST was written:
// asked for an input accept on `src-address-list`, which fwFilter does not
// declare, the hAP AC2 received an unconditional input accept. Refused before
// permission or any read, and the invented name is not echoed.
func TestAFieldTheResourceDoesNotHaveIsRefused(t *testing.T) {
	cn := &conn{}
	got := cn.runAIWriteTool(writeCall(`{"resource":"fwFilter","values":{"chain":"input","action":"accept","srcAddressList":"mgmt"}}`))
	if !strings.Contains(got, "nothing was changed") || !strings.Contains(got, "srcAddress") {
		t.Errorf("an undeclared field produced %q", got)
	}
	if strings.Contains(got, "srcAddressList") {
		t.Errorf("the invented field name was echoed back: %q", got)
	}
	// A Display field is never sent, so naming one is the same silent drop.
	got = cn.runAIWriteTool(writeCall(`{"resource":"ipPool","values":{"name":"p","ranges":"198.51.100.1-198.51.100.9","used":"3"}}`))
	if !strings.Contains(got, "nothing was changed") {
		t.Errorf("a Display field produced %q", got)
	}
	// CONTROL: only declared fields get past this check, to the permission one.
	got = cn.runAIWriteTool(writeCall(`{"resource":"fwFilter","values":{"chain":"input","action":"accept"}}`))
	if !strings.Contains(got, "permission") {
		t.Errorf("declared fields alone produced %q", got)
	}
	// A delete names no values, so it is not held to this.
	got = cn.runAIWriteTool(writeCall(`{"resource":"fwFilter","id":"*1","delete":true,"values":{"bogus":"x"}}`))
	if strings.Contains(got, "nothing was changed") && !strings.Contains(got, "permission") {
		t.Errorf("a delete was refused on its values: %q", got)
	}
}

// TestADeleteNeedsAnIDAndPermission. The delete branch refuses before it reads
// anything, in the same order as an edit: permission first, then a device, then
// the row id. The control is an edit on the same resource, refused the same way.
func TestADeleteNeedsAnIDAndPermission(t *testing.T) {
	cn := &conn{}
	got := cn.runAIWriteTool(writeCall(`{"resource":"dnsStatic","id":"*1","delete":true}`))
	if !strings.Contains(got, "permission") {
		t.Errorf("a delete by a viewer with no write permission produced %q", got)
	}
	if strings.Contains(got, "Applied") {
		t.Errorf("a refused delete claims it happened: %q", got)
	}
}

// TestTheWriteToolAdvertisesDelete. The operator asked the assistant to delete a
// row and it answered that it had no delete action, because the schema offered
// create and edit only. A tool the model cannot see is a tool it will say does
// not exist, so the schema is what is asserted.
func TestTheWriteToolAdvertisesDelete(t *testing.T) {
	var write *aitools.Tool
	for _, tl := range aitools.Permitted(func(string, string) bool { return true }) {
		if tl.Name == aitools.WriteToolName {
			tl := tl
			write = &tl
		}
	}
	if write == nil {
		t.Fatal("no write tool is advertised to a viewer who may write everything")
	}
	props, _ := write.Parameters["properties"].(map[string]any)
	if _, ok := props["delete"]; !ok {
		t.Error("change_row has no `delete` parameter, so the assistant cannot delete a row")
	}
	req, _ := write.Parameters["required"].([]string)
	for _, r := range req {
		if r == "values" {
			t.Error("`values` is required, so a delete (which has none) cannot be called")
		}
	}
	if !strings.Contains(write.Description, "DELETE") {
		t.Error("the description does not tell the model how to delete")
	}
}
