package server

// THE ISSUER IS KEPT EXACTLY AS ENTERED, AND A MISMATCH IS CAUGHT ON SAVE
// (issue #159).
//
// Saving an OIDC provider stripped the Issuer URL's trailing slash, so an
// Authentik provider (https://host/application/o/<slug>/) could never match its
// own metadata and every sign-in was refused "config".

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mikrodash/internal/db"
)

func TestSavingKeepsTheIssuerExactlyAsEntered(t *testing.T) {
	s := &Server{}
	for in, want := range map[string]string{
		"https://authentik.example/application/o/mikrodash/":   "https://authentik.example/application/o/mikrodash/",
		"https://tenant.auth0.example/":                        "https://tenant.auth0.example/",
		"  https://keycloak.example/realms/home  ":             "https://keycloak.example/realms/home",
		"https://login.microsoftonline.example/tenant-id/v2.0": "https://login.microsoftonline.example/tenant-id/v2.0",
	} {
		p, err := s.buildProvider("p1", db.SSOProvider{ID: "p1"},
			providerBody{Name: "IdP", Issuer: in, ClientID: "c"}, nil)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if p.Issuer != want {
			t.Errorf("%q was saved as %q, want %q", in, p.Issuer, want)
		}
	}
}

// authentikStub serves discovery under the issuer's path, declaring an issuer
// that ends in "/".
func authentikStub(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/application/o/mikrodash/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 srv.URL + "/application/o/mikrodash/",
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
			"jwks_uri":               srv.URL + "/jwks",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func saveProvider(t *testing.T, s *Server, issuer string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"name": "Authentik", "enabled": true,
		"issuer": issuer, "clientId": "mikrodash"})
	r := httptest.NewRequest("PUT", "/api/sso/providers/p1", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.ssoWrite(rec, r, &Session{Username: "admin"}, "p1")
	return rec
}

func TestASaveIsRefusedWhenTheMetadataDeclaresAnotherIssuer(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	stub := authentikStub(t)
	exact := stub.URL + "/application/o/mikrodash/"
	if err := d.UpsertSSOProvider(db.SSOProvider{ID: "p1", Name: "Authentik",
		Issuer: "https://old.example", ClientID: "mikrodash", Scopes: "openid"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{auditDB: d}
	s.sso.http = stub.Client()

	// Without the slash: refused, the published issuer named, nothing written.
	rec := saveProvider(t, s, strings.TrimSuffix(exact, "/"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), exact) {
		t.Fatalf("saving the issuer without its slash answered %d %s, want 400 naming %s",
			rec.Code, rec.Body.String(), exact)
	}
	if got, _ := d.SSOProviderByID("p1"); got.Issuer != "https://old.example" {
		t.Errorf("a refused save wrote the issuer %q", got.Issuer)
	}

	// Exactly as published: saved, slash and all.
	if rec := saveProvider(t, s, exact); rec.Code != http.StatusOK {
		t.Fatalf("saving the exact issuer answered %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := d.SSOProviderByID("p1"); got.Issuer != exact {
		t.Errorf("saved as %q, want %q", got.Issuer, exact)
	}
}

// A provider that cannot be reached is saved anyway: an identity provider not
// built yet is no reason to refuse, and sign-in reports it as it always has.
func TestAnUnreachableProviderIsStillSaved(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	stub := authentikStub(t)
	gone := stub.URL + "/application/o/mikrodash/"
	client := stub.Client()
	stub.Close()
	if err := d.UpsertSSOProvider(db.SSOProvider{ID: "p1", Name: "Authentik",
		Issuer: "https://old.example", ClientID: "mikrodash", Scopes: "openid"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{auditDB: d}
	s.sso.http = client
	if rec := saveProvider(t, s, gone); rec.Code != http.StatusOK {
		t.Fatalf("an unreachable provider answered %d %s, want it saved", rec.Code, rec.Body.String())
	}
}
