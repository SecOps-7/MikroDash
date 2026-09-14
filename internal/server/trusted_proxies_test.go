package server

import (
	"net/http"
	"testing"

	"mikrodash/internal/trustedproxy"
)

// THE TRUSTED PROXIES OPTION REACHES THE CLIENT RESOLVER.
//
// `TestEveryServerOptionIsSetByTheBinary` proves `main` sets the field; this
// proves `New` does something with it. A server built with a trusted range must
// believe that range's X-Forwarded-For, and one built without must not. Issue #111.
func TestTheTrustedProxiesOptionReachesTheResolver(t *testing.T) {
	prev := trustedProxies.Load()
	t.Cleanup(func() { trustedProxies.Store(prev) })

	fromProxy := func() *http.Request {
		r := &http.Request{RemoteAddr: "10.0.0.1:4000", Header: http.Header{}}
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		return r
	}

	trusted, err := trustedproxy.Parse("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(authFixture(t, "pw"), Options{WebDir: t.TempDir(), TrustedProxies: trusted}); err != nil {
		t.Fatal(err)
	}
	if got := clientIPOf(fromProxy()); got != "203.0.113.9" {
		t.Errorf("with 10.0.0.0/8 trusted, a request via 10.0.0.1 resolved to %q", got)
	}

	if _, err := New(authFixture(t, "pw"), Options{WebDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if got := clientIPOf(fromProxy()); got != "10.0.0.1" {
		t.Errorf("with nothing trusted, a forwarded address was believed: %q", got)
	}
}
