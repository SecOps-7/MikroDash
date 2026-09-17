package server

import (
	"encoding/json"
	"strings"
	"testing"

	"mikrodash/internal/aiprovider"
	"mikrodash/internal/aitools"
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
