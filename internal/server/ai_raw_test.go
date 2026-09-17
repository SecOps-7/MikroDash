package server

import (
	"encoding/json"
	"strings"
	"testing"

	"mikrodash/internal/aiprovider"
	"mikrodash/internal/aitools"
	"mikrodash/internal/rawcmd"
	"mikrodash/internal/routeros"
)

func rawCall(args string) aiprovider.ToolCall {
	var tc aiprovider.ToolCall
	tc.ID, tc.Type = "r1", "function"
	tc.Function.Name = aitools.RawCommandToolName
	tc.Function.Arguments = args
	return tc
}

// TestTheRawCommandToolIsNotAdvertisedToAnyone.
//
// It is built and gated, and no model is told it exists — not in the catalogue
// the endpoint is sent, and not in any viewer's tool list, including an
// administrator's. The frontend is not wired either; slice 4 says so.
func TestTheRawCommandToolIsNotAdvertisedToAnyone(t *testing.T) {
	for _, tool := range aitools.All() {
		if tool.Name == aitools.RawCommandToolName || tool.Name == aitools.BulkToolName {
			t.Errorf("%q is in the generated catalogue", tool.Name)
		}
	}
	// `allowAll` is the most permissive viewer there can be.
	for _, tool := range aitools.Permitted(func(string, string) bool { return true }) {
		if tool.Name == aitools.RawCommandToolName || tool.Name == aitools.BulkToolName {
			t.Errorf("%q was offered to a viewer who may write everything", tool.Name)
		}
	}
}

// TestTheGateRefusesBeforeTheParserSeesAnything.
//
// The order matters: a caller who may not run raw commands must not be able to
// use the parser as an oracle — "that command is malformed" and "you may not run
// commands" are different answers, and only one of them is any of their
// business. A bare connection is nobody: no session, so not a signed-in global
// administrator.
func TestTheGateRefusesBeforeTheParserSeesAnything(t *testing.T) {
	cn := &conn{srv: &Server{}} // no session: nobody
	for _, in := range []string{
		`{"command":"/ip/address/print"}`,
		`{"command":"/ip/address/print; /user/remove .id=*1"}`,
		`{"command":"nonsense"}`,
	} {
		got := cn.runAIRawCommandTool(rawCall(in))
		if !strings.Contains(got, "not available") {
			t.Errorf("%s produced %q, not the gate's refusal", in, got)
		}
		if strings.Contains(got, "refused before it was sent") {
			t.Errorf("%s reached the parser: %q", in, got)
		}
	}
	// Malformed ARGUMENTS are answered before anything, because there is nothing
	// to gate: no command was named.
	if got := cn.runAIRawCommandTool(rawCall(`{"command":`)); !strings.Contains(got, "not valid JSON") {
		t.Errorf("malformed arguments produced %q", got)
	}
}

// TestSignInOffIsNotAnAdministrator. `isGlobalAdmin` answers true when sign-in is
// switched off — right for reading the principal graph, wrong here: a command
// nobody can be held to is one nobody should be able to send (#97's rule for
// router writes).
func TestSignInOffIsNotAnAdministrator(t *testing.T) {
	cn := &conn{srv: &Server{}, sess: &Session{AuthMode: "none", Username: "whoever"}}
	if got := cn.runAIRawCommandTool(rawCall(`{"command":"/ip/address/print"}`)); !strings.Contains(got, "not available") {
		t.Errorf("a session with sign-in off produced %q", got)
	}
}

// TestRawOutputIsCappedAndWrapped. A raw `print` can return tens of thousands of
// rows, and the whole reply would otherwise be one message to the model. The cap
// is reported rather than silent, and the rows are wrapped in the untrusted
// block like every other router-supplied text.
func TestRawOutputIsCappedAndWrapped(t *testing.T) {
	rows := make([]routeros.Reply, 0, 500)
	for i := 0; i < 500; i++ {
		rows = append(rows, routeros.Reply{".id": "*1", "comment": strings.Repeat("x", 40)})
	}
	out := rawOutput(rows)
	if !strings.Contains(out, "UNTRUSTED") && !strings.Contains(out, "untrusted") {
		t.Errorf("the reply is not wrapped in the untrusted block: %s", out[:200])
	}
	body := out[strings.Index(out, "{"):]
	body = body[:strings.LastIndex(body, "}")+1]
	var got struct {
		Rows      []map[string]string `json:"rows"`
		Returned  int                 `json:"returned"`
		Total     int                 `json:"total"`
		Truncated bool                `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("the reply is not the JSON it claims: %v", err)
	}
	if got.Total != 500 {
		t.Errorf("total is %d, want the real count 500", got.Total)
	}
	if len(got.Rows) > rawOutputMaxRows || len(got.Rows) != got.Returned {
		t.Errorf("returned %d rows (reported %d), cap is %d", len(got.Rows), got.Returned, rawOutputMaxRows)
	}
	if !got.Truncated {
		t.Error("the cap was applied and not reported")
	}
	// The control: a small reply is not reported as truncated.
	small := rawOutput([]routeros.Reply{{"name": "ether1"}})
	if strings.Contains(small, `"truncated":true`) {
		t.Errorf("a one-row reply reads as truncated: %s", small)
	}
}

func bulkCall(args string) aiprovider.ToolCall {
	var tc aiprovider.ToolCall
	tc.ID, tc.Type = "b1", "function"
	tc.Function.Name = aitools.BulkToolName
	tc.Function.Arguments = args
	return tc
}

// TestAPlanIsRefusedWholeOrNotAtAll.
//
// A plan whose fourth step will not parse must not run its first three and then
// stop: that is the half-applied state this app avoids everywhere else, and the
// operator would have approved a plan that is not the one that ran. The gate
// still comes first, so these are checked through a connection that may run
// commands as far as the parser — which a bare one cannot be, so the parse
// refusals are exercised on the pure path the tool takes.
func TestAPlanIsRefusedWholeOrNotAtAll(t *testing.T) {
	cn := &conn{srv: &Server{}}
	// The gate refuses before the plan is looked at, exactly as for one command.
	got := cn.runAIBulkTool(bulkCall(`{"commands":["/ip/address/print","/user/remove .id=*1"]}`))
	if !strings.Contains(got, "not available") {
		t.Errorf("the gate did not refuse a plan first: %q", got)
	}
	if got := cn.runAIBulkTool(bulkCall(`{"commands":`)); !strings.Contains(got, "not valid JSON") {
		t.Errorf("malformed arguments produced %q", got)
	}
}

// TestPlanTextNumbersTheStepsItWillRun. The dialog and the audit trail show the
// PARSED commands, numbered, so what is approved is what the server understood.
func TestPlanTextNumbersTheStepsItWillRun(t *testing.T) {
	plan := []rawcmd.Command{}
	for _, in := range []string{
		`/ip/firewall/filter/add chain=input action=accept comment="from the office"`,
		"/ip/address/print",
	} {
		cmd, err := rawcmd.Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		plan = append(plan, cmd)
	}
	got := planText(plan)
	want := "1. /ip/firewall/filter/add chain=input action=accept comment=from the office\n" +
		"2. /ip/address/print"
	if got != want {
		t.Errorf("planText =\n%s\nwant\n%s", got, want)
	}
}

// TestAPlanReportsEveryStep, including the ones it never attempted.
//
// Stopping at the first failure is only half of it: a plan that stops silently
// leaves the operator to work out how far it got. Every step is named — what ran,
// what failed, and what was skipped because of it.
func TestAPlanReportsEveryStep(t *testing.T) {
	steps := []planStep{
		{Step: 1, Command: "/ip/address/print", Outcome: "ok", Rows: 3},
		{Step: 2, Command: "/ip/address/add address=bad", Outcome: "failed", Error: "input does not match any value"},
		{Step: 3, Command: "/ip/address/print", Outcome: "not attempted"},
	}
	if n := countRan(steps); n != 2 {
		t.Errorf("countRan = %d, want 2 — a skipped step is not a step that ran", n)
	}
	b, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"outcome":"ok"`, `"outcome":"failed"`, `"outcome":"not attempted"`,
		`"rows":3`, `"error":"input does not match any value"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("a step outcome is missing %s: %s", want, b)
		}
	}
	// A successful step carries no error, and a skipped one no row count: the
	// zero values are omitted rather than reported as measurements.
	if strings.Contains(string(b), `"step":3,"command":"/ip/address/print","outcome":"not attempted","rows"`) {
		t.Errorf("a step that never ran reports a row count: %s", b)
	}
}

// TestAPlanIsBounded. Twenty is more than an operator will read carefully, and a
// plan nobody reads carefully is one nobody is confirming.
func TestAPlanIsBounded(t *testing.T) {
	if rawPlanMaxSteps > 20 {
		t.Errorf("a plan may have %d steps; that is more than anyone confirms deliberately",
			rawPlanMaxSteps)
	}
	cmds := make([]string, rawPlanMaxSteps+1)
	for i := range cmds {
		cmds[i] = "/ip/address/print"
	}
	b, _ := json.Marshal(map[string]any{"commands": cmds})
	// Through a connection that passes no gate, so this asserts the ORDER too:
	// the bound is not what refuses here.
	cn := &conn{srv: &Server{}}
	if got := cn.runAIBulkTool(bulkCall(string(b))); !strings.Contains(got, "not available") {
		t.Errorf("an over-long plan from a caller with no permission produced %q", got)
	}
}
