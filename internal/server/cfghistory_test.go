package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/db"
)

// cfgHistoryServer is cfgJobServer with its database brought to the current
// schema: the job's own tests need only the grants it seeds.
func cfgHistoryServer(t *testing.T, n int) (*Server, []string) {
	t.Helper()
	s, ids := cfgJobServer(t, n)
	if _, err := s.auditDB.Migrate(); err != nil {
		t.Fatal(err)
	}
	return s, ids
}

// The run ledger names who started a run (the column holds a user id) and
// counts its routers by how each ended.
func TestTheRunLedgerNamesWhoAndCountsRouters(t *testing.T) {
	s, ids := cfgHistoryServer(t, 2)
	run := db.CfgRun{ID: "run-1", TemplateName: "DNS and time", Revision: 3, Method: "additions",
		BodyMasked: "/ip dns\nset servers={{dns}}", Fingerprint: "f", ValuesJSON: "{}", State: db.CfgRunHalted,
		CreatedBy: "u-1"}
	targets := []db.CfgRunTarget{
		{RunID: "run-1", RouterID: ids[0], Position: 0, State: db.CfgTargetApplied},
		{RunID: "run-1", RouterID: ids[1], Position: 1, State: db.CfgTargetNotAttempted},
	}
	if err := s.auditDB.CreateCfgRun(run, targets); err != nil {
		t.Fatal(err)
	}
	sess := &Session{Username: "someone", AuthMode: "modern"}

	w := httptest.NewRecorder()
	s.cfgRuns(w, httptest.NewRequest(http.MethodGet, "/api/config/runs", nil), sess)
	var list struct{ Runs []cfgRunRow }
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Runs) != 1 {
		t.Fatalf("%s %v", w.Body, err)
	}
	got := list.Runs[0]
	if got.StartedBy != "someone" || got.Revision != 3 {
		t.Errorf("started by %q at revision %d; want the username, not the id, and revision 3", got.StartedBy, got.Revision)
	}
	if got.Routers[db.CfgTargetApplied] != 1 || got.Routers[db.CfgTargetNotAttempted] != 1 {
		t.Errorf("router counts %v", got.Routers)
	}
	if strings.Contains(w.Body.String(), "servers=") {
		t.Error("the list carries the run's text; only the detail should")
	}

	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/config/runs/run-1", nil)
	r.SetPathValue("id", "run-1")
	s.cfgRunDetail(w, r, sess)
	var detail struct {
		StartedBy string
		Targets   []cfgRunTargetRow
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || len(detail.Targets) != 2 {
		t.Fatalf("%s %v", w.Body, err)
	}
	if detail.Targets[0].Label != "router-r00" || detail.StartedBy != "someone" {
		t.Errorf("detail %+v", detail)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/config/runs/nope", nil)
	r.SetPathValue("id", "nope")
	s.cfgRunDetail(w, r, sess)
	if w.Code != http.StatusNotFound {
		t.Errorf("an unknown run answered %d", w.Code)
	}
}

// The Drift list names each baseline's template and router, and leaves out a
// router that is no longer in the fleet.
func TestTheDriftListNamesTemplateAndRouter(t *testing.T) {
	s, ids := cfgHistoryServer(t, 1)
	for _, rid := range []string{ids[0], "gone"} {
		if err := s.auditDB.SetCfgBaseline(db.CfgBaseline{TemplateID: "canned:dns-and-time", RouterID: rid,
			Body: "/ip dns\n", Fingerprint: "f", TakenAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	s.cfgDriftList(w, httptest.NewRequest(http.MethodGet, "/api/config/drift", nil), nil)
	var list struct{ Baselines []cfgDriftRow }
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Baselines) != 1 {
		t.Fatalf("%d baselines listed; want the one on a router still in the fleet", len(list.Baselines))
	}
	b := list.Baselines[0]
	if b.RouterLabel != "router-r00" || b.TemplateName == "" || b.TemplateName == b.TemplateID {
		t.Errorf("%+v", b)
	}
	if strings.Contains(w.Body.String(), "/ip dns") {
		t.Error("the list carries a baseline's body")
	}
}

// A drift check reads the menus the baseline was taken of: the text its run
// sent, not the template as it is now, which may since have been edited.
func TestABaselineIsJudgedByTheMenusItsRunSent(t *testing.T) {
	s, ids := cfgHistoryServer(t, 1)
	run := db.CfgRun{ID: "run-1", TemplateName: "x", Method: "additions", BodyMasked: "/snmp\nset contact={{c}}",
		Fingerprint: "f", ValuesJSON: "{}", State: db.CfgRunDone, CreatedBy: "u-1"}
	if err := s.auditDB.CreateCfgRun(run, []db.CfgRunTarget{{RunID: "run-1", RouterID: ids[0], State: db.CfgTargetApplied}}); err != nil {
		t.Fatal(err)
	}
	id := "run-1"
	tp, ok := s.cfgBaselineTemplate(&db.CfgBaseline{TemplateID: "canned:dns-and-time", RouterID: ids[0], RunID: &id})
	if !ok || strings.Join(tp.Menus(), ",") != "/snmp" {
		t.Fatalf("want the run's menus, /snmp: %v", ok)
	}
	// With no run to read, the template as it is now.
	tp, ok = s.cfgBaselineTemplate(&db.CfgBaseline{TemplateID: "canned:dns-and-time", RouterID: ids[0]})
	canned, _ := cannedRow("canned:dns-and-time")
	want, err := cfgtpl.Parse(canned.Body)
	if err != nil || !ok || strings.Join(tp.Menus(), ",") != strings.Join(want.Menus(), ",") {
		t.Errorf("want the canned template's menus %v", want.Menus())
	}
}
