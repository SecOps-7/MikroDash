package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/db"
	"mikrodash/internal/rbac"
	"mikrodash/internal/store"
)

// cfgJobServer is fleetCapServer granting config-management:write, on routers
// labelled "router-r00", "router-r01" …
func cfgJobServer(t *testing.T, n int) (*Server, []string) {
	t.Helper()
	dir := t.TempDir()
	ids := make([]string, 0, n)
	routers := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("r%02d", i)
		ids = append(ids, id)
		routers = append(routers, map[string]any{"id": id, "label": "router-" + id, "host": "198.51.100." + fmt.Sprint(i+1)})
	}
	for name, v := range map[string]any{"routers.json": routers,
		"users.json": []store.User{{ID: "u-1", Username: "someone", Role: "admin"}}} {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	grants := strings.Replace(fleetCapRoles, "'role-r','dns','read'", "'role-r','config-management','write'", 1)
	for _, id := range ids {
		grants += fmt.Sprintf("INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id)"+
			" VALUES ('user','u-1','router','%s','role-r');\n", id)
	}
	if _, err := h.Exec(grants); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	database, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	known := make([]rbac.Router, 0, n)
	for _, id := range ids {
		known = append(known, rbac.Router{ID: id})
	}
	return &Server{store: st, auditDB: database, rbac: rbac.New(database, func() []rbac.Router { return known })}, ids
}

func TestADeployStartsOnlyAsItWasConfirmed(t *testing.T) {
	s, ids := cfgJobServer(t, 3)
	sess := &Session{Username: "someone", AuthMode: "modern"}
	targets := []cfgTargetIn{{RouterID: ids[1], Hash: "h1"}, {RouterID: ids[0], Hash: "h0"}}
	in := cfgStartIn{TemplateID: "canned:dns-and-time", Targets: targets}

	in.Confirm = "router-r00" // the SECOND router's name, not the canary's
	if run, msg := s.cfgPlan(sess, in); run != nil || !strings.Contains(msg, "router-r01") {
		t.Errorf("started with the wrong name typed: %v %q", run != nil, msg)
	}
	in.Confirm = " router-r01 "
	run, msg := s.cfgPlan(sess, in)
	if run == nil {
		t.Fatalf("refused: %s", msg)
	}
	if len(run.targets) != 2 || run.targets[0].row.RouterID != ids[1] || !run.targets[0].line.Canary ||
		run.targets[1].line.Canary || run.state != db.CfgRunCanary {
		t.Errorf("the canary is not the first router picked: %+v", run.targets[0].line)
	}
	if run.revision < 1 {
		t.Errorf("the run does not know which revision of the template it deploys: %d", run.revision)
	}
	for _, tr := range run.targets {
		if tr.row.RunID != run.id || tr.row.RunID == "" {
			t.Errorf("a target does not carry its run's id")
		}
	}

	// A router nobody previewed cannot be deployed to.
	in.Targets = []cfgTargetIn{{RouterID: ids[1]}}
	if run, msg := s.cfgPlan(sess, in); run != nil || !strings.Contains(msg, "preview") {
		t.Errorf("an unpreviewed router was accepted: %q", msg)
	}
	// An unknown template, and a router the caller may not reach.
	if run, _ := s.cfgPlan(sess, cfgStartIn{TemplateID: "canned:nope", Confirm: "router-r00",
		Targets: []cfgTargetIn{{RouterID: ids[0], Hash: "h"}}}); run != nil {
		t.Error("an unknown template was accepted")
	}
	if run, msg := s.cfgPlan(sess, cfgStartIn{TemplateID: "canned:dns-and-time", Confirm: "x",
		Targets: []cfgTargetIn{{RouterID: "r99", Hash: "h"}}}); run != nil || !strings.Contains(msg, "None") {
		t.Errorf("an unknown router was accepted: %q", msg)
	}
}

// The recorded run keeps what was chosen for each router, WITHOUT the secrets.
func TestARecordedRunKeepsNoSecret(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := &Server{auditDB: d}
	sess := Session{Username: "someone", AuthMode: "modern"}
	run := &cfgRun{id: "run-1", tplID: "canned:snmp-v3", tplName: "SNMPv3", kind: "fragment", revision: 3, actor: sess,
		state: db.CfgRunCanary, t: mustTemplate(t, "/snmp\nset contact={{contact}}\n/snmp community\nadd name=x authentication-password={{pw}}"),
		defs: cfgtplVars{{Name: "contact", Type: "text"}, {Name: "pw", Type: "secret"}}.defs(),
		targets: []*cfgTargetRun{{in: cfgTargetIn{RouterID: "r1", Values: map[string]string{"contact": "noc", "pw": "hunter2-secret"}},
			row: db.CfgRunTarget{RunID: "run-1", RouterID: "r1", State: db.CfgTargetPending}}}}
	if err := s.cfgRecordStart(run); err != nil {
		t.Fatal(err)
	}
	got, targets, err := d.CfgRun("run-1")
	if err != nil || got == nil || len(targets) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if strings.Contains(got.ValuesJSON, "hunter2") || strings.Contains(got.BodyMasked, "hunter2") {
		t.Errorf("a secret was stored with the run: %s", got.ValuesJSON)
	}
	if !strings.Contains(got.ValuesJSON, `"contact":"noc"`) || !strings.Contains(got.BodyMasked, "{{pw}}") {
		t.Errorf("the run lost what it should keep: %s / %s", got.ValuesJSON, got.BodyMasked)
	}
	if got.TemplateID != nil {
		t.Error("a canned template was linked as a stored one")
	}
	if got.Revision != 3 {
		t.Errorf("the run recorded revision %d of the template, not 3", got.Revision)
	}
}

func TestTheIdlePayloadCarriesNoNullArray(t *testing.T) {
	b, _ := json.Marshal(cfgPayloadOf(nil))
	if !strings.Contains(string(b), `"targets":[]`) || !strings.Contains(string(b), `"state":"idle"`) {
		t.Errorf("%s", b)
	}
}

type cfgtplVar struct{ Name, Type string }
type cfgtplVars []cfgtplVar

func (v cfgtplVars) defs() []cfgtpl.VarDef {
	out := make([]cfgtpl.VarDef, len(v))
	for i, x := range v {
		out[i] = cfgtpl.VarDef{Name: x.Name, Type: x.Type}
	}
	return out
}

func mustTemplate(t *testing.T, src string) *cfgtpl.Template {
	t.Helper()
	tp, err := cfgtpl.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	return tp
}
