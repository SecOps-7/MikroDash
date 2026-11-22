package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mikrodash/internal/apps"
	"mikrodash/internal/db"
)

// The tables apprules.go makes (migration 41), and the history they fold into.
const appRulesTestDDL = appHistoryGateDDL + `
CREATE TABLE app_rules (id TEXT PRIMARY KEY, rule TEXT NOT NULL, created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL, updated_by TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE app_categories (id TEXT PRIMARY KEY, name TEXT NOT NULL, tone TEXT NOT NULL,
  created_by TEXT NOT NULL, created_at INTEGER NOT NULL);
INSERT INTO role_pages (role_id, page, access) VALUES ('role-r','apps','read');
INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id)
VALUES ('user','u-1','router','r-A','role-r');`

// An administrator: u-1 also holds the built-in administrator role, globally.
const appRulesAdminDDL = appRulesTestDDL + `
INSERT INTO roles (id, name, builtin) VALUES ('administrator','Administrator',1);
INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id)
VALUES ('user','u-1','global','','administrator');`

func appRulesServer(t *testing.T, admin bool) (*Server, *http.ServeMux) {
	t.Helper()
	ddl := appRulesTestDDL
	if admin {
		ddl = appRulesAdminDDL
	}
	s := gateServer(t, `{}`, ddl)
	mux := http.NewServeMux()
	s.registerAppRules(mux)
	t.Cleanup(func() { apps.SetRules(nil) })
	return s, mux
}

func appCall(mux *http.ServeMux, method, url, body string, signedIn bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	if signedIn {
		req.Header.Set("Cookie", authed)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

const gameRule = `{"create":true,"rule":{"name":"Lan Game","color":"#3366FF","icon":"steam","category":"gaming",
  "domains":[{"domain":"*.example-game.net"}],"asns":[64500],"ports":[{"proto":"udp","from":27900,"to":27901}]}}`

// Only a global administrator may save, and only a signed-in user with the
// Apps Usage page may read; canEdit tells the page which it is.
func TestAppRulesFollowTheirGates(t *testing.T) {
	_, mux := appRulesServer(t, false)
	if w := appCall(mux, "POST", "/api/apps/rules", gameRule, false); w.Code != 401 {
		t.Errorf("signed out save: %d", w.Code)
	}
	if w := appCall(mux, "POST", "/api/apps/rules", gameRule, true); w.Code != 403 {
		t.Errorf("non-administrator save: %d, want 403", w.Code)
	}
	if w := appCall(mux, "POST", "/api/apps/rules/preview", gameRule, true); w.Code != 403 {
		t.Errorf("non-administrator preview: %d, want 403", w.Code)
	}
	if w := appCall(mux, "GET", "/api/apps/catalogue?routerId=r-C", "", true); w.Code != 403 {
		t.Errorf("catalogue for a router without the page: %d, want 403", w.Code)
	}
	w := appCall(mux, "GET", "/api/apps/catalogue?routerId=r-A", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"canEdit":false`) {
		t.Errorf("reader's catalogue: %d %.200s", w.Code, w.Body.String())
	}
	if w := appCall(mux, "GET", "/api/apps/live?routerId=r-A&client=not-an-ip", "", true); w.Code != 400 {
		t.Errorf("a client that is not an address: %d", w.Code)
	}
	for _, u := range []string{"/api/apps/live?routerId=r-A&client=192.0.2.7", "/api/apps/clients?routerId=r-A",
		"/api/apps/unnamed?routerId=r-A", "/api/apps/categories?routerId=r-A"} {
		if w := appCall(mux, "GET", u, "", true); w.Code != 200 {
			t.Errorf("%s with no session running: %d %s", u, w.Code, w.Body.String())
		}
	}
}

// A saved custom app is in the catalogue and names the next reading's traffic
// at once; a second rule claiming its domain is refused with the conflict.
func TestASavedRuleNamesTrafficAndBlocksADuplicate(t *testing.T) {
	_, mux := appRulesServer(t, true)
	w := appCall(mux, "POST", "/api/apps/rules", gameRule, true)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var saved struct{ Rule apps.Rule }
	_ = json.Unmarshal(w.Body.Bytes(), &saved)
	if saved.Rule.ID != "u_lan-game" || saved.Rule.Color != "#3366ff" || !saved.Rule.Domains[0].Subdomains {
		t.Errorf("the saved rule was not cleaned: %+v", saved.Rule)
	}
	m := apps.Current().Classify([]string{"eu.example-game.net"}, 0, "tcp", 443)
	if m.App == nil || m.App.ID != "u_lan-game" || !m.App.Custom || m.App.Icon != "steam" {
		t.Errorf("after the save, the domain is named %+v", m.App)
	}

	w = appCall(mux, "POST", "/api/apps/rules", `{"create":true,"rule":{"name":"Other","color":"#112233",
	  "domains":[{"domain":"example-game.net"}]}}`, true)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"blocking":true`) {
		t.Errorf("a duplicate domain: %d %s", w.Code, w.Body.String())
	}

	w = appCall(mux, "GET", "/api/apps/catalogue?routerId=r-A", "", true)
	if !strings.Contains(w.Body.String(), `"id":"u_lan-game"`) || !strings.Contains(w.Body.String(), `"canEdit":true`) {
		t.Errorf("catalogue after the save: %.300s", w.Body.String())
	}
}

// Deleting a custom app folds its stored hours into unnamed and stops naming
// its traffic; deleting a category still in use is refused.
func TestDeletingACustomAppFoldsItsHistory(t *testing.T) {
	s, mux := appRulesServer(t, true)
	if w := appCall(mux, "POST", "/api/apps/categories", `{"category":{"name":"Kids","tone":"pink"}}`, true); w.Code != 200 {
		t.Fatalf("category: %d %s", w.Code, w.Body.String())
	}
	if w := appCall(mux, "POST", "/api/apps/rules", `{"create":true,"rule":{"name":"Homework","color":"#22aa55",
	  "category":"u_kids","domains":[{"domain":"school.example","subdomains":true}]}}`, true); w.Code != 200 {
		t.Fatalf("rule: %d %s", w.Code, w.Body.String())
	}
	if w := appCall(mux, "DELETE", "/api/apps/categories/u_kids", "", true); w.Code != http.StatusConflict {
		t.Errorf("deleting a category in use: %d", w.Code)
	}
	if err := s.auditDB.AddAppUsage([]db.AppUsageRow{{RouterID: "r-A", App: "u_homework", Hour: hourMs, RxBytes: 40}}); err != nil {
		t.Fatal(err)
	}
	if w := appCall(mux, "DELETE", "/api/apps/rules/u_homework", "", true); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	rows, _ := s.auditDB.AppUsage("r-A", 0, 1e13)
	if len(rows) != 1 || rows[0].App != apps.UnnamedID || rows[0].RxBytes != 40 {
		t.Errorf("after the delete the history is %+v, want one unnamed row of 40", rows)
	}
	if m := apps.Current().Classify([]string{"a.school.example"}, 0, "tcp", 443); m.App != nil {
		t.Errorf("a deleted app still names traffic: %+v", m.App)
	}
	if w := appCall(mux, "DELETE", "/api/apps/categories/u_kids", "", true); w.Code != 200 {
		t.Errorf("deleting the emptied category: %d %s", w.Code, w.Body.String())
	}
}

// Additions to a built-in app keep its name and logo, may move it to another
// category, and are refused for an app that does not exist.
func TestABuiltinAppTakesAdditions(t *testing.T) {
	_, mux := appRulesServer(t, true)
	w := appCall(mux, "POST", "/api/apps/rules", `{"rule":{"id":"netflix","builtin":true,"category":"other",
	  "domains":[{"domain":"films.example"}]}}`, true)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	m := apps.Current().Classify([]string{"films.example"}, 0, "tcp", 443)
	if m.App == nil || m.App.ID != "netflix" || m.App.Category != "other" || m.App.Name != "Netflix" {
		t.Errorf("the addition named %+v", m.App)
	}
	if w := appCall(mux, "POST", "/api/apps/rules", `{"rule":{"id":"no-such-app","builtin":true}}`, true); w.Code != 400 {
		t.Errorf("an unknown built-in app: %d", w.Code)
	}
}
