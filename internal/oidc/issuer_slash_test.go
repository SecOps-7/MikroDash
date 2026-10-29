package oidc

// AN ISSUER ENDING IN "/" IS DISCOVERED, AND A MISMATCH NAMES BOTH VALUES
// (issue #159).
//
// Authentik's issuer is https://host/application/o/<slug>/ and its discovery
// document lives under that path. The comparison stays exact (OIDC Discovery
// 4.3); what must hold is that the slash-terminated issuer works as configured,
// that the document is not fetched from "...//.well-known", and that a
// mismatch says what the two strings were.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func authentikLike(t *testing.T) (*fakeIdP, *[]string) {
	t.Helper()
	f := &fakeIdP{jwks: rsaJWKS()}
	paths := &[]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		*paths = append(*paths, r.URL.Path)
		if r.URL.Path != "/application/o/mikrodash/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		f.writeJSON(w, map[string]any{
			"issuer":                 f.srv.URL + "/application/o/mikrodash/",
			"authorization_endpoint": "https://idp.example/authorize",
			"token_endpoint":         "https://idp.example/token",
			"jwks_uri":               f.srv.URL + "/jwks",
		})
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f, paths
}

func discoverAs(f *fakeIdP, issuer string) error {
	now := testNow
	p := providerFor(f, &now)
	p.cfg.Issuer = issuer
	_, err := p.Discovery(context.Background())
	return err
}

func TestAnIssuerEndingInASlashIsDiscovered(t *testing.T) {
	f, paths := authentikLike(t)
	if err := discoverAs(f, f.srv.URL+"/application/o/mikrodash/"); err != nil {
		t.Fatalf("the issuer exactly as the provider declares it was refused: %v", err)
	}
	if got := strings.Join(*paths, ","); got != "/application/o/mikrodash/.well-known/openid-configuration" {
		t.Errorf("discovery was fetched from %q, want the issuer's path with no \"//\"", got)
	}
}

func TestAnIssuerMismatchNamesBothValues(t *testing.T) {
	f, _ := authentikLike(t)
	configured := f.srv.URL + "/application/o/mikrodash" // the slash 0.8.77 dropped
	err := discoverAs(f, configured)
	var mm *IssuerMismatch
	if !errors.As(err, &mm) {
		t.Fatalf("got %v, want an IssuerMismatch", err)
	}
	if mm.Declared != configured+"/" || mm.Configured != configured {
		t.Errorf("mismatch carries %+v", *mm)
	}
	// The log detail carries both, and the browser code is still only "config".
	var e *Err
	if !errors.As(err, &e) || e.Code != CodeConfig ||
		!strings.Contains(e.Detail, `metadata declares "`+configured+`/"`) ||
		!strings.Contains(e.Detail, `configured "`+configured+`"`) {
		t.Errorf("the refusal reads %v", err)
	}
}
