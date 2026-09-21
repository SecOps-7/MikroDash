package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/aiprovider"
	"mikrodash/internal/aitools"
	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/pages"
	"mikrodash/internal/rawcmd"
	"mikrodash/internal/rbac"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"

	_ "modernc.org/sqlite"
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

// rawAdminDDL is a grant graph with ONE global administrator: the only shape
// that reaches the raw command gate's second question.
const rawAdminDDL = `
CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_version (version, applied_at) VALUES (14, 0);
CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL,
  actor_id TEXT, actor_name TEXT NOT NULL, actor_ip TEXT, action TEXT NOT NULL,
  scope TEXT NOT NULL CHECK (scope IN ('app','router')), router_id TEXT,
  target_type TEXT, target_id TEXT, target_name TEXT,
  outcome TEXT NOT NULL CHECK (outcome IN ('ok','denied','error')), detail TEXT);
CREATE TABLE roles (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT,
  builtin INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE role_pages (role_id TEXT NOT NULL, page TEXT NOT NULL, access TEXT NOT NULL);
CREATE TABLE grants (
  id             TEXT PRIMARY KEY DEFAULT (hex(randomblob(16))),
  principal_type TEXT NOT NULL, principal_id TEXT NOT NULL,
  role_id        TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT, role TEXT,
  scope_type     TEXT NOT NULL, scope_id TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL DEFAULT 0, created_by TEXT,
  UNIQUE (principal_type, principal_id, scope_type, scope_id));
CREATE TABLE group_members (group_id TEXT NOT NULL, user_id TEXT NOT NULL);
CREATE TABLE principal_groups (id TEXT PRIMARY KEY, name TEXT NOT NULL,
  description TEXT, created_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE sites (id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at INTEGER NOT NULL DEFAULT 0);
INSERT INTO roles (id, name, builtin) VALUES ('administrator','Administrator',1);
`

// rawAdminConn builds the connection the gate's later questions need: a
// signed-in GLOBAL ADMINISTRATOR, a store whose settings say whether raw
// commands are allowed, and no router selected — so the test can see which gate
// answered without a router being involved.
func rawAdminConn(t *testing.T, allow bool) *conn {
	t.Helper()
	dir := t.TempDir()

	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(rawAdminDDL); err != nil {
		t.Fatal(err)
	}
	h.Close()
	database, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(fmt.Sprintf(`{"aiAllowRawCommands": %v}`, allow)), 0o600); err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(store.NewUser{Username: "boss", Password: "a-long-enough-password", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := user["id"].(string)
	if id == "" {
		t.Fatal("the created user has no id")
	}
	if err := database.UpsertGrant(db.GrantSpec{
		PrincipalType: "user", PrincipalID: id, ScopeType: "global", RoleID: "administrator",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{store: st, auditDB: database, hub: hub.New()}
	s.rbac = rbac.New(database, func() []rbac.Router { return nil })
	if !s.isGlobalAdmin(&Session{Username: "boss", AuthMode: "modern"}) {
		t.Fatal("the fixture's administrator is not one — this test would pass for the wrong reason")
	}
	return &conn{srv: s, sess: &Session{Username: "boss", AuthMode: "modern"}}
}

// TestAnAdministratorWithTheSettingOffIsRefusedBeforeParsing.
//
// The second gate, exercised against a REAL global administrator rather than a
// connection that fails the first one — otherwise this would pass whatever the
// setting said. With the setting off, a hostile command is refused by the gate
// and never reaches the parser; with it on, the same command reaches the parser
// and is refused there, which is the control that proves the gate is what
// answered in the first case.
func TestAnAdministratorWithTheSettingOffIsRefusedBeforeParsing(t *testing.T) {
	const hostile = `{"command":"/ip/address/print; /user/remove .id=*1"}`

	off := rawAdminConn(t, false)
	got := off.runAIRawCommandTool(rawCall(hostile))
	if !strings.Contains(got, "not available") {
		t.Errorf("an administrator with the setting off produced %q", got)
	}
	if strings.Contains(got, "refused before it was sent") {
		t.Errorf("the command reached the parser with the setting off: %q", got)
	}
	// And a plan is refused by the same gate, before any step is parsed.
	if got := off.runAIBulkTool(bulkCall(`{"commands":["/ip/address/print","nonsense"]}`)); !strings.Contains(got, "not available") {
		t.Errorf("a plan with the setting off produced %q", got)
	}

	// THE CONTROL. The same administrator with the setting ON is not refused by
	// the gate: the call gets past it and stops at the next thing missing, which
	// on this connection is a selected router. Without this, the assertions
	// above would pass against a build that refused everything.
	on := rawAdminConn(t, true)
	for _, got := range []string{
		on.runAIRawCommandTool(rawCall(hostile)),
		on.runAIRawCommandTool(rawCall(`{"command":"/ip/address/print"}`)),
		on.runAIBulkTool(bulkCall(`{"commands":["/ip/address/print"]}`)),
	} {
		if strings.Contains(got, "not available") {
			t.Errorf("with the setting on, the gate still refused: %q", got)
		}
		if !strings.Contains(got, "No device is selected") {
			t.Errorf("with the setting on, the call stopped somewhere unexpected: %q", got)
		}
	}
}

// TestNeitherRawToolIsOfferedToAnyViewerOfAnyShape.
//
// `Permitted` is asked as every shape of viewer there can be — nobody, every
// page read-only, every page writable, and each page on its own — because the
// tool list is what a model is TOLD exists, and a tool nobody may run is still a
// tool a model will try.
func TestNeitherRawToolIsOfferedToAnyViewerOfAnyShape(t *testing.T) {
	viewers := map[string]func(page, access string) bool{
		"nobody":     func(string, string) bool { return false },
		"read-only":  func(_, access string) bool { return access == aitools.AccessRead },
		"everything": func(string, string) bool { return true },
	}
	for _, p := range pages.All {
		page := p.Key
		viewers["only "+page] = func(got, _ string) bool { return got == page }
	}
	for name, can := range viewers {
		for _, tool := range aitools.Permitted(can) {
			if tool.Name == aitools.RawCommandToolName || tool.Name == aitools.BulkToolName {
				t.Errorf("%q was offered to the viewer %q", tool.Name, name)
			}
		}
	}
}

// TestBothRawToolsAreAnsweredByName. `run_command` was dispatched and
// `bulk_execute` was not: it fell through to `aitools.ByName`, which does not
// know it, so the plan path, its gates and its dialog were reachable only from
// tests (review loop: wire it, decided 2026-09-19). Through the dispatcher
// both must meet the gate, never "does not exist".
func TestBothRawToolsAreAnsweredByName(t *testing.T) {
	cn := &conn{srv: &Server{}} // nobody: the gate refuses
	for name, tc := range map[string]aiprovider.ToolCall{
		"run_command":  rawCall(`{"command":"/ip/address/print"}`),
		"bulk_execute": bulkCall(`{"commands":["/ip/address/print"]}`),
	} {
		got := cn.runAITool(cn.scope(), tc)
		if strings.Contains(got, "does not exist") || !strings.Contains(got, "not available") {
			t.Errorf("%s through runAITool produced %q, not the gate's refusal", name, got)
		}
	}
}

// TestRawOutputMasksSecrets. A raw `print` of /ppp/secret or a WireGuard
// interface returns the password or private key, and every reply field was
// copied to the model and the transcript (review loop).
func TestRawOutputMasksSecrets(t *testing.T) {
	out := rawOutput([]routeros.Reply{
		{"name": "wg1", "private-key": "hunter2", "public-key": "PUB"},
		{"name": "vpn", "password": "hunter2", "passthrough": "yes"},
	})
	if strings.Contains(out, "hunter2") {
		t.Errorf("a secret reached the model: %s", out)
	}
	for _, want := range []string{"PUB", `"passthrough":"yes"`, `"private-key":"`} {
		if !strings.Contains(out, want) {
			t.Errorf("%s is missing from %s; a secret is masked, not the row", want, out)
		}
	}
}

// THE RAW TOOLS ARE ADVERTISED BEHIND THE SAME GATE THAT REFUSES THEM. The
// question's tool list adds RawTools only when rawGateNote passes, and
// rawCommandGate refuses on that same note, so nobody is offered a tool whose
// every call the executor would refuse, and nobody who may run one is not told.
func TestTheRawToolsAreAdvertisedBehindTheirGate(t *testing.T) {
	for _, cn := range []*conn{
		{srv: &Server{}},
		{srv: &Server{}, sess: &Session{AuthMode: "none", Username: "whoever"}},
	} {
		if _, note := cn.rawGateNote(); note == "" {
			t.Errorf("session %+v passed the standing gates", cn.sess)
		}
	}
	src, err := os.ReadFile("ai_chat.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`if _, note := cn\.rawGateNote\(\); note == "" \{\s*for _, t := range aitools\.RawTools\(\)`).Match(src) {
		t.Error("ai_chat.go no longer adds RawTools only when rawGateNote passes")
	}
	gate, err := os.ReadFile("ai_raw.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gate), "settings, note := cn.rawGateNote()") {
		t.Error("rawCommandGate no longer answers from rawGateNote, so advertising and refusing can disagree")
	}
}
