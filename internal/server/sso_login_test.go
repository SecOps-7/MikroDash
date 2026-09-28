package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/oidc"
	"mikrodash/internal/store"
	"mikrodash/internal/websession"
)

// ssoServer builds a server with a real database, a real user store and a real
// session store, because every refusal below turns on what those three actually
// hold. Faking any of them would test the fake.
// testRedirect is the origin a pending login records. The value itself does not
// matter to these cases; that it RIDES ON THE RECORD does, because the token
// exchange sends this rather than re-deriving it.
const testRedirect = "https://dash.example/api/auth/sso/callback"

func ssoServer(t *testing.T) (*Server, *http.ServeMux, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"baseUrl":"https://dash.example"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{auditDB: d, store: st, hub: hub.New(),
		auth: authFor("tok", nil), sessions4Web: websession.New()}
	mux := http.NewServeMux()
	s.registerSSOLogin(mux)
	return s, mux, dir
}

func seedSSOProvider(t *testing.T, s *Server, enabled bool) db.SSOProvider {
	t.Helper()
	p := db.SSOProvider{
		ID: "abc123def456", Name: "Entra", Enabled: enabled,
		Issuer: "https://login.example/tenant", ClientID: "cid",
		Scopes: "openid profile", ClaimUsername: "preferred_username",
		ClaimEmail: "email", ClaimName: "name", ClaimRoles: "groups",
	}
	if err := s.auditDB.UpsertSSOProvider(p); err != nil {
		t.Fatal(err)
	}
	if err := s.auditDB.ReplaceSSORoleMap(p.ID, []db.SSORoleMapping{
		{ClaimValue: "netops", RoleID: "operator"},
		{ClaimValue: "admins", RoleID: "administrator"},
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

func verifiedToken(sub, username string, groups ...string) *oidc.Verified {
	g := make([]any, 0, len(groups))
	for _, x := range groups {
		g = append(g, x)
	}
	return &oidc.Verified{Subject: sub, Issuer: "https://login.example/tenant",
		Claims: map[string]any{"preferred_username": username, "groups": g}}
}

// ── THE COOKIE RULE NO BEHAVIOURAL TEST CAN REACH ──────────────────────────
//
// The callback is a TOP-LEVEL CROSS-SITE GET. A SameSite=Strict cookie is not
// sent on that navigation, so the flow would fail in every browser while every
// Go test passed - httptest sends whatever cookie the test attaches, with no
// notion of a site at all.
//
// So this asserts the STRING, and says why. It is the weakest kind of check and
// it is the only one available; recording the reason beside it is what stops the
// next author "tightening" it to Strict to match mikrodash_sid.
func TestTheSSOStateCookieIsLaxAndScoped(t *testing.T) {
	s, _, _ := ssoServer(t)
	c := s.ssoCookie("pending-id", ssoCookieMaxAge)

	if !strings.Contains(c, "SameSite=Lax") {
		t.Errorf("the state cookie is %q.\n"+
			"    It MUST be SameSite=Lax. The callback is a top-level cross-site GET, so a "+
			"Strict cookie is not sent and single sign-on fails 100%% of the time in every "+
			"browser - with this suite still green.", c)
	}
	if strings.Contains(c, "SameSite=Strict") {
		t.Error("the state cookie is Strict; see above")
	}
	if !strings.Contains(c, "Path="+ssoCookiePath) {
		t.Errorf("the state cookie is not scoped to %s: %q", ssoCookiePath, c)
	}
	if !strings.Contains(c, "HttpOnly") {
		t.Errorf("the state cookie is readable by script: %q", c)
	}
	// Secure follows FORCE_HTTPS, exactly as the session cookie does.
	s.forceHTTPS = true
	if !strings.Contains(s.ssoCookie("x", 1), "; Secure") {
		t.Error("FORCE_HTTPS did not mark the state cookie Secure")
	}
}

// ── THE BREAK-GLASS GUARANTEE ──────────────────────────────────────────────
//
// An administrator at the identity provider can mint a token claiming ANY
// username. If that were enough to land on a local account, the local admin
// this app keeps as its way back in would be the first target.
func TestAUsernameCollisionWithALocalAccountIsRefused(t *testing.T) {
	s, _, _ := ssoServer(t)
	p := seedSSOProvider(t, s, true)
	if _, err := s.store.CreateUser(store.NewUser{
		Username: "admin", Password: "hunter2", Role: "admin"}); err != nil {
		t.Fatal(err)
	}

	_, _, _, err := s.ssoResolveUser(p, verifiedToken("sub-attacker", "admin", "admins"))
	if err == nil {
		t.Fatal("a token claiming the local admin's username was accepted")
	}
	var oe *oidc.Err
	if !asOIDC(err, &oe) || oe.Code != oidc.CodeDenied {
		t.Fatalf("the collision was refused as %v, want a denial", err)
	}
	// AND NOTHING WAS WRITTEN. A refusal that had already bound the identity
	// would hand the account over on the SECOND attempt.
	if bound, _ := s.auditDB.SSOIdentityFor(p.ID, "sub-attacker"); bound != "" {
		t.Errorf("the refused sign-in still bound the subject to %q", bound)
	}
	users, _ := s.store.Users()
	if len(users) != 1 {
		t.Errorf("the refused sign-in created an account: %d users", len(users))
	}
}

// A name nobody holds locally is created, with EMPTY credentials, and given the
// grant its claim mapped to.
func TestAFirstSignInCreatesAnExternalAccountWithTheMappedRole(t *testing.T) {
	s, _, _ := ssoServer(t)
	p := seedSSOProvider(t, s, true)

	userID, username, legacy, err := s.ssoResolveUser(p, verifiedToken("sub-1", "jo", "netops"))
	if err != nil {
		t.Fatalf("a first sign-in was refused: %v", err)
	}
	if username != "jo" || legacy != "operator" {
		t.Errorf("resolved to %q/%q, want jo/operator", username, legacy)
	}
	users, _ := s.store.Users()
	if len(users) != 1 || users[0].PasswordHash != "" || users[0].Salt != "" {
		t.Fatalf("the created account is not credential-less: %+v", users)
	}
	grants, err := s.auditDB.ListGrants(db.GrantFilter{PrincipalType: "user", PrincipalID: userID})
	if err != nil || len(grants) != 1 || roleIDOf(grants[0]) != "operator" {
		t.Fatalf("grants are %+v (%v), want one operator grant", grants, err)
	}
}

// ── THE ROLE FOLLOWS THE PROVIDER, BOTH WAYS ───────────────────────────────
//
// This is the claim most likely to rot, so it is asserted in both directions in
// one test: moving between mapped groups moves the grant, and losing every
// mapped group REMOVES it. Checking only that the second sign-in failed would
// pass with a stale grant still sitting in the table.
func TestTheRoleIsReEvaluatedOnEverySignIn(t *testing.T) {
	s, _, _ := ssoServer(t)
	p := seedSSOProvider(t, s, true)

	userID, _, _, err := s.ssoResolveUser(p, verifiedToken("sub-1", "jo", "netops"))
	if err != nil {
		t.Fatal(err)
	}

	// Promoted at the provider.
	if _, _, legacy, err := s.ssoResolveUser(p,
		verifiedToken("sub-1", "jo", "admins")); err != nil || legacy != "admin" {
		t.Fatalf("after a group change: %q, %v - want admin", legacy, err)
	}
	grants, _ := s.auditDB.ListGrants(db.GrantFilter{PrincipalType: "user", PrincipalID: userID})
	if len(grants) != 1 || roleIDOf(grants[0]) != "administrator" {
		t.Fatalf("the grant did not follow the provider: %+v", grants)
	}

	// Removed from every mapped group.
	if _, _, _, err := s.ssoResolveUser(p, verifiedToken("sub-1", "jo")); err == nil {
		t.Fatal("somebody with no mapped group was still signed in")
	}
	grants, _ = s.auditDB.ListGrants(db.GrantFilter{PrincipalType: "user", PrincipalID: userID})
	if len(grants) != 0 {
		t.Errorf("the refusal left %d grant(s) behind: %+v.\n"+
			"    Deprovisioning at the provider has to reach this app, or losing the group "+
			"only stops SSO sign-in while every other path keeps the access.",
			len(grants), grants)
	}
}

// Entra omits `groups` past 200 and points at Graph. Read as "no groups" it
// would refuse with a message sending the operator to inspect a mapping that is
// perfectly correct.
func TestTheGroupOverageIsNamedRatherThanReadAsNoGroups(t *testing.T) {
	s, _, _ := ssoServer(t)
	p := seedSSOProvider(t, s, true)

	v := verifiedToken("sub-1", "jo")
	v.Claims["_claim_names"] = map[string]any{"groups": "src1"}
	_, _, _, err := s.ssoResolveUser(p, v)
	if err == nil {
		t.Fatal("an overage token signed in")
	}
	if !strings.Contains(err.Error(), "too many") {
		t.Errorf("the overage was reported as %q, which does not name the cause", err)
	}
}

// A provider that is switched off cannot be started by anyone who kept its id.
// Otherwise the toggle is a suggestion rather than a switch.
func TestADisabledProviderCannotBeStarted(t *testing.T) {
	s, mux, _ := ssoServer(t)
	p := seedSSOProvider(t, s, false)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/sso/"+p.ID+"/start", nil))
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=config") {
		t.Errorf("starting a disabled provider redirected to %q, want a config refusal", loc)
	}
	// And it is not offered either.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/sso/providers", nil))
	if strings.Contains(rec.Body.String(), p.ID) {
		t.Errorf("a disabled provider was offered to the login page: %s", rec.Body.String())
	}
}

// ── EVERY WAY IN GIVES THE SAME ANSWER ─────────────────────────────────────
//
// No cookie, a wrong state and an unknown id are three different faults, and
// telling a stranger which of them they hit is free intelligence about what
// their forgery got right.
func TestAnUnmatchedCallbackSaysNothingAboutWhichHalfFailed(t *testing.T) {
	s, mux, _ := ssoServer(t)
	seedSSOProvider(t, s, true)

	id, rec, err := s.sso.logins().Create("abc123def456", "/home", "https://dash.example/api/auth/sso/callback")
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, c := range []struct{ name, cookie, state string }{
		{"no cookie at all", "", rec.State},
		{"a cookie with the wrong state", id, "not-the-state"},
		{"an id nobody minted", "deadbeef", rec.State},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/auth/sso/callback?code=x&state="+c.state, nil)
		if c.cookie != "" {
			req.Header.Set("Cookie", ssoCookieName+"="+c.cookie)
		}
		mux.ServeHTTP(w, req)
		loc := w.Header().Get("Location")
		// The ref is per-request, so compare only the code.
		got = append(got, loc[:strings.Index(loc, "&ref=")])
		// AND THE COOKIE IS CLEARED ON EVERY ONE OF THEM.
		if !strings.Contains(strings.Join(w.Header().Values("Set-Cookie"), " "), "Max-Age=0") {
			t.Errorf("%s: the pending cookie was not cleared", c.name)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i] != got[0] {
			t.Errorf("the three unmatched callbacks answer differently: %q vs %q.\n"+
				"    They must be indistinguishable.", got[0], got[i])
		}
	}
}

// A pending login is single-use even when the state is right: Consume deletes
// before it checks, so a replay of a callback that worked is indistinguishable
// from one that never did.
func TestAPendingLoginIsSingleUse(t *testing.T) {
	s, _, _ := ssoServer(t)
	id, rec, err := s.sso.logins().Create("p", "/home", "https://dash.example/api/auth/sso/callback")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.sso.logins().Consume(id, rec.State); !ok {
		t.Fatal("the first consume failed")
	}
	if _, ok := s.sso.logins().Consume(id, rec.State); ok {
		t.Error("the same pending login was consumed twice")
	}
}

// `next` is validated where every login passes through, not in the handler.
func TestAHostileNextNeverReachesTheRecord(t *testing.T) {
	s, _, _ := ssoServer(t)
	for _, raw := range []string{"//evil.example/", "https://evil.example/", `/\evil.example`} {
		_, rec, err := s.sso.logins().Create("p", raw, testRedirect)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Next != "/" {
			t.Errorf("Create(%q) stored next=%q; safeNext did not run", raw, rec.Next)
		}
	}
	if _, rec, _ := s.sso.logins().Create("p", "/home", "https://dash.example/api/auth/sso/callback"); rec.Next != "/home" {
		t.Error("safeNext rejected an ordinary same-origin path; the check is too strict to be " +
			"the one the login page also applies")
	}
}

// roleIDOf reads a grant's role id. It is a *string because a grant may carry
// the legacy `role` word instead, which an SSO grant never does.
func roleIDOf(g db.GrantRow) string {
	if g.RoleID == nil {
		return ""
	}
	return *g.RoleID
}

func asOIDC(err error, target **oidc.Err) bool {
	for err != nil {
		if e, ok := err.(*oidc.Err); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ── WHERE THE REDIRECT URI COMES FROM ──────────────────────────────────────
//
// The Base URL setting is an OVERRIDE. Empty means "follow the address this
// request arrived on", so single sign-on works with nothing typed - which is
// the whole point of removing the requirement to type it.
func TestTheBaseURLSettingOverridesTheDerivedOrigin(t *testing.T) {
	s, _, dir := ssoServer(t)

	// The harness writes a baseUrl, so the stored value must win.
	req := httptest.NewRequest("GET", "http://10.0.0.5:3081/api/auth/sso/x/start", nil)
	if got := s.baseURLFor(req); got != "https://dash.example" {
		t.Errorf("with a stored base URL the derived one was used: %q", got)
	}

	// Clear it, and the request decides.
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"baseUrl":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.store = st
	if got := s.baseURLFor(req); got != "http://10.0.0.5:3081" {
		t.Errorf("with no stored base URL, got %q, want the request's own origin", got)
	}
	if got := s.redirectURIFor(req); got != "http://10.0.0.5:3081/api/auth/sso/callback" {
		t.Errorf("redirect URI is %q", got)
	}
}

// ── THE SCHEME IS THE HALF A PROXY BREAKS ──────────────────────────────────
//
// A TLS-terminating proxy hands the backend plain http, so without
// X-Forwarded-Proto every redirect URI behind a proxy would say `http://` and
// the provider would refuse it. The header is read through an ALLOW-LIST
// because this string is pasted straight into a URL.
func TestTheOriginReadsTheForwardedSchemeSafely(t *testing.T) {
	for _, c := range []struct {
		name, header string
		tls, force   bool
		want         string
	}{
		{name: "plain http", want: "http://host.example"},
		{name: "FORCE_HTTPS", force: true, want: "https://host.example"},
		{name: "a proxy in front of TLS", header: "https", want: "https://host.example"},
		{name: "a chain names the client first", header: "https, http", want: "https://host.example"},
		{name: "a proxy that did not terminate TLS", header: "http", force: true,
			want: "http://host.example"},
		{
			// THE INJECTION ATTEMPT. Anything but the two known schemes is
			// ignored, so a header cannot smuggle `javascript:` or a whole
			// second URL into the redirect this app hands the provider.
			name: "a header that is not a scheme", header: "javascript:alert(1)//",
			want: "http://host.example",
		},
		{name: "an empty header changes nothing", header: "", want: "http://host.example"},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://host.example/x", nil)
			if c.header != "" {
				req.Header.Set("X-Forwarded-Proto", c.header)
			}
			if got := originOf(req, c.force); got != c.want {
				t.Errorf("originOf = %q, want %q", got, c.want)
			}
		})
	}
}

// ── START AND CALLBACK MUST SEND THE SAME BYTES ────────────────────────────
//
// The provider compares the redirect URI in the token exchange against the one
// in the authorization request. Re-deriving it at the callback would differ the
// moment a proxy header or the host differed between the two hops - and the
// failure is an `invalid_grant` that reads like a problem with the code. It
// rides on the pending record so the two agree BY CONSTRUCTION.
func TestTheRedirectURIRidesOnThePendingLogin(t *testing.T) {
	s, _, _ := ssoServer(t)
	_, rec, err := s.sso.logins().Create("p", "/", "https://one.example/api/auth/sso/callback")
	if err != nil {
		t.Fatal(err)
	}
	if rec.RedirectURI != "https://one.example/api/auth/sso/callback" {
		t.Fatalf("the pending login does not carry the redirect URI: %q", rec.RedirectURI)
	}
}

// A provider can be started with no base URL stored at all. Before this it was
// refused with "no base URL is configured", which is the requirement that was
// removed.
func TestAProviderStartsWithNoBaseURLStored(t *testing.T) {
	s, mux, dir := ssoServer(t)
	p := seedSSOProvider(t, s, true)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"baseUrl":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.store = st

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/sso/"+p.ID+"/start", nil))
	loc := rec.Header().Get("Location")

	// ── WHAT IS AND IS NOT BEING ASSERTED ──────────────────────────────────
	//
	// The seeded issuer does not exist, so this WILL fail with `err=provider`
	// once it tries to fetch the discovery document. That is fine and expected.
	// The claim is narrower and is the one that changed: it is no longer
	// refused as `err=config` before any request is made, which is what
	// "no base URL is configured" used to do.
	if strings.Contains(loc, "err=config") {
		t.Fatalf("starting with no base URL stored was refused as a configuration "+
			"error: %q\n    The base URL is an OVERRIDE now - empty means follow the "+
			"request's own origin.", loc)
	}
	// And a pending login WAS minted, which only happens past the point the old
	// code refused at. The cookie is the wrong observable here: it is set after
	// the authorization URL is built, and building it fails against a seeded
	// issuer that does not exist.
	if s.sso.logins().Len() != 1 {
		t.Errorf("%d pending logins; the start never got past the configuration check",
			s.sso.logins().Len())
	}
}

// ── THE CALLBACK MUST NOT RE-DERIVE THE REDIRECT URI ───────────────────────
//
// A SOURCE CHECK, AND IT IS SAYING SO. Proving this behaviourally needs a fake
// identity provider serving discovery, a key set and a token endpoint over TLS
// inside this package, and the one that exists lives in internal/oidc. Until
// that is worth duplicating, this reads the handler instead.
//
// It earns its place because a MUTATION SURVIVED here: swapping
// `rec.RedirectURI` for `s.redirectURIFor(r)` in the exchange broke nothing,
// and the failure it would cause is an `invalid_grant` from the provider that
// reads like a problem with the authorization code rather than with this line.
// A check that cannot fail is the failure mode this repository is built around,
// so an honest weak check beats a silent gap.
func TestTheCallbackSendsTheRecordedRedirectURI(t *testing.T) {
	src, err := os.ReadFile("sso_login.go")
	if err != nil {
		t.Fatal(err)
	}
	var code strings.Builder
	for _, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code.WriteString(line)
		code.WriteString("\n")
	}
	body := code.String()

	// THE CONTROL: a string known to be in this file's code. Without it a
	// broken reader reports success for everything it looks for.
	if !strings.Contains(body, "prov.Exchange(") {
		t.Fatal("the control string is missing, so this scan proves nothing about the rest")
	}
	if !strings.Contains(body, "rec.Verifier,\n\t\trec.RedirectURI)") &&
		!strings.Contains(body, "rec.RedirectURI)") {
		t.Error("the token exchange does not send the redirect URI off the pending record.\n" +
			"    Re-deriving it from the callback request differs from the authorization\n" +
			"    request the moment a proxy header or the host differs between the two\n" +
			"    hops, and the provider answers invalid_grant.")
	}
	// And it must not reach for the request-derived one in the exchange.
	if strings.Contains(body, "Exchange(r.Context(), q.Get(\"code\"), rec.Verifier,\n\t\ts.redirectURIFor(r))") {
		t.Error("the exchange re-derives the redirect URI from this request")
	}
}
