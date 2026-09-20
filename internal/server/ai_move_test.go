package server

// `change_row`'s `before`, end to end against the scripted router in
// resource_move_test.go.
//
// The pipeline itself is tested there. What is new here is the part between the
// model and it: which resources a move may name, that a move is a change on its
// own, and that an approved proposal moves the row the operator was shown.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
)

// aiMoveConn is `moveConn` with the assistant's settings written: `confirm` says
// whether `aiConfirmWrites` is on, which is what decides between applying and
// proposing.
func aiMoveConn(t *testing.T, m *moveRouter, res *resource.Resource, confirm bool) (*conn, *hub.Client) {
	t.Helper()
	cn := writerConn(t, res)
	c := hub.NewClient("me", 256)
	cn.srv.hub.Add(c)
	cn.c = c
	cn.rsession = m.session(cn.srv.hub)
	body := `{"aiConfirmWrites": ` + map[bool]string{true: "true", false: "false"}[confirm] + `}`
	if err := os.WriteFile(filepath.Join(cn.srv.store.Dir, "settings.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cn, c
}

// proposeFrame is the ai:propose payload the client was sent, or nil.
func proposeFrame(t *testing.T, c *hub.Client) map[string]any {
	t.Helper()
	for _, raw := range frames(c) {
		var f struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if json.Unmarshal([]byte(raw), &f) == nil && f.Event == "ai:propose" {
			return f.Data
		}
	}
	return nil
}

// TestAMoveIsAChangeOnItsOwn. `before` alongside `values` or `delete: true` would
// be two pipelines, two audit rows and two confirmations behind one call, with
// the operator answering the second without having seen the first.
func TestAMoveIsAChangeOnItsOwn(t *testing.T) {
	m := threeRules()
	cn, _ := aiMoveConn(t, m, resource.FWFilter, true)

	for _, args := range []string{
		`{"resource":"fwFilter","id":"*3","before":"*1","values":{"comment":"x"}}`,
		`{"resource":"fwFilter","id":"*3","before":"*1","delete":true}`,
	} {
		got := cn.runAIWriteTool(writeCall(args))
		if !strings.Contains(got, "on its own") {
			t.Errorf("%s was answered %q, not the one-change-at-a-time refusal", args, got)
		}
	}
	if n := len(m.cmds); n != 0 {
		t.Errorf("%d commands reached the router for a call that was refused outright", n)
	}

	// THE CONTROL: `before` on its own is not refused by that rule.
	if got := cn.runAIWriteTool(writeCall(`{"resource":"fwFilter","id":"*3","before":"*1"}`)); strings.Contains(got, "on its own") {
		t.Errorf("a plain move was refused as a combined change: %q", got)
	}
}

// TestTheAssistantCannotReorderATableOutsideTheFirewall.
//
// Routing rules are ordered and the page's own arrows move them. The assistant's
// reach is narrower ON PURPOSE (`aitools.Movable`), and this is the half of that
// decision that can be observed: the refusal happens before anything is read.
func TestTheAssistantCannotReorderATableOutsideTheFirewall(t *testing.T) {
	if !resource.RoutingRule.Ordered {
		t.Fatal("routingRule is no longer ordered; this test is measuring nothing")
	}
	m := threeRules()
	cn, _ := aiMoveConn(t, m, resource.RoutingRule, true)

	got := cn.runAIWriteTool(writeCall(`{"resource":"routingRule","id":"*1","before":"*2"}`))
	if !strings.Contains(got, "no order") {
		t.Errorf("a routing rule move was answered %q, not a refusal", got)
	}
	if n := len(m.cmds); n != 0 {
		t.Errorf("%d commands reached the router for a resource the assistant may not reorder", n)
	}
}

// TestTheAssistantsMoveReachesTheRouterThroughThePagesOwnPath. With prompts off
// a move is applied, as an edit is — and it is stamped `via: agent`, so the
// Audit Trail can answer "what did the AI change" for a reorder too.
func TestTheAssistantsMoveReachesTheRouterThroughThePagesOwnPath(t *testing.T) {
	m := threeRules()
	cn, _ := aiMoveConn(t, m, resource.FWFilter, false)

	got := cn.runAIWriteTool(writeCall(`{"resource":"fwFilter","id":"*3","before":"*1"}`))
	if !strings.HasPrefix(got, "Applied") {
		t.Fatalf("the move was answered %q", got)
	}
	if order := m.order(); order != "*3,*1,*2" {
		t.Fatalf("the table is %s, want *3,*1,*2", order)
	}
	rows := moveAuditRows(t, cn.srv.auditDB)
	if len(rows) != 1 || rows[0].Action != "fwFilter.move" {
		t.Fatalf("audit rows: %d, first action %q", len(rows), rows[0].Action)
	}
	if rows[0].Detail == nil || !strings.Contains(*rows[0].Detail, `"via":"agent"`) {
		t.Errorf("the assistant's move is not marked as the assistant's: %v", rows[0].Detail)
	}

	// `end` is the other destination, and it must not be read as an id.
	if got := cn.runAIWriteTool(writeCall(`{"resource":"fwFilter","id":"*3","before":"end"}`)); !strings.HasPrefix(got, "Applied") {
		t.Fatalf("a move to the end was answered %q", got)
	}
	if order := m.order(); order != "*1,*2,*3" {
		t.Errorf("the table is %s, want *1,*2,*3: `end` did not mean the end", order)
	}
}

// TestAProposedMoveIsShownAsAMoveAndApprovedAsOne, and an approval of a row that
// is no longer the row the operator saw is refused — the property the proposal's
// stored identity exists for.
func TestAProposedMoveIsShownAsAMoveAndApprovedAsOne(t *testing.T) {
	m := threeRules()
	cn, c := aiMoveConn(t, m, resource.FWFilter, true)

	got := cn.runAIWriteTool(writeCall(`{"resource":"fwFilter","id":"*3","before":"*1"}`))
	if !strings.Contains(got, "Waiting for confirmation") {
		t.Fatalf("the move was answered %q, not a proposal", got)
	}
	if n := m.sent("/ip/firewall/filter/move"); n != 0 {
		t.Fatalf("%d move commands were sent while the operator had not answered", n)
	}
	p := proposeFrame(t, c)
	if p == nil {
		t.Fatal("no ai:propose frame reached the page")
	}
	if p["action"] != "move" {
		// The dialog picks its wording off this: anything else reads as
		// "Change the Filter Rule", which is a field edit.
		t.Errorf("the proposal's action is %v, not \"move\"", p["action"])
	}
	if cmd, _ := p["command"].(string); !strings.Contains(cmd, "/ip/firewall/filter/move") ||
		!strings.Contains(cmd, "=destination=*1") {
		t.Errorf("the proposal does not show the command it will send: %q", cmd)
	}
	// The destination is named by IDENTITY, because an `.id` tells an operator
	// nothing about which rule they are putting this one in front of. Spelled as
	// every other proposal spells a row's name, `quoted` included.
	vals, _ := p["values"].(map[string]any)
	moves, _ := vals["moves"].(string)
	if !strings.Contains(moves, "management") {
		t.Errorf("the dialog does not name the row it lands before: %v", p["values"])
	}

	tok, _ := p["token"].(string)
	if tok == "" {
		t.Fatal("the proposal carries no token")
	}
	cn.aiWriteApprove(proposalFrame(tok))
	if order := m.order(); order != "*3,*1,*2" {
		t.Fatalf("the table is %s, want *3,*1,*2: approving did not move it", order)
	}

	// ── AND A ROW THAT IS NO LONGER THE ROW SHOWN IS REFUSED ────────────────
	m2 := threeRules()
	cn2, c2 := aiMoveConn(t, m2, resource.FWFilter, true)
	if got := cn2.runAIWriteTool(writeCall(`{"resource":"fwFilter","id":"*3","before":"*1"}`)); !strings.Contains(got, "Waiting") {
		t.Fatalf("the second move was answered %q", got)
	}
	tok2, _ := proposeFrame(t, c2)["token"].(string)
	m2.mu.Lock()
	m2.rows[2] = routeros.Reply{".id": "*3", "chain": "forward", "action": "accept", "comment": "a different rule"}
	m2.mu.Unlock()
	cn2.aiWriteApprove(proposalFrame(tok2))
	if order := m2.order(); order != "*1,*2,*3" {
		t.Errorf("the table is %s: a rule that had been replaced at that id was moved anyway", order)
	}
	if out := strings.Join(frames(c2), "|"); !strings.Contains(out, "changed on the router") {
		t.Errorf("the operator was not told the row had changed: %s", out)
	}
}
