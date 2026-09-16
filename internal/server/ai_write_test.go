package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
