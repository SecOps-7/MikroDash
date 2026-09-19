package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A FLEET DNS COPY NEEDS AN AUDIT TRAIL, as a write from the page does (#97).
// With no audit database the request is refused before any router is chosen,
// and says why rather than looking like a permission problem.
func TestAFleetDNSCopyNeedsAnAuditTrail(t *testing.T) {
	s := alertSettingsServer(t, `{}`)
	auth := authFor("tok", &Session{Username: "someone", AuthMode: "modern"})
	s.auth = auth

	body := `{"routerIds":["r-A"],"values":{"name":"probe.lan","type":"A","address":"192.0.2.10"}}`
	req := httptest.NewRequest("POST", "/api/dns/fleet-add", strings.NewReader(body))
	req.Header.Set("Cookie", authed)
	w := httptest.NewRecorder()
	s.dnsFleetAdd(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("with no audit database the fleet copy answered %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !strings.Contains(got.Error, "audit database") {
		t.Errorf("the refusal said %q; it should say the audit database is not open", got.Error)
	}
}
