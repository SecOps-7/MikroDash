package server

// Signing in through an identity provider.
//
// ── THREE ROUTES, EVERY ONE REACHABLE BY A STRANGER ────────────────────────
//
// `providers` tells the login page which buttons to draw, `start` sends the
// browser to the provider, and `callback` is where it comes back. None of them
// can require a session, because none of them has one yet - and that single
// fact is what shapes the rate limits, the bounded refusals, and the silence
// about which check failed.
//
// The protocol is in internal/oidc, which knows nothing about MikroDash
// accounts. This file is the POLICY: which local account an external identity
// is, and what it may do.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/oidc"
)

const (
	// ssoCookieName carries the PENDING LOGIN'S ID, never its state.
	//
	// ── SameSite=Lax, AND IT IS LOAD-BEARING ───────────────────────────────
	//
	// The callback is a TOP-LEVEL CROSS-SITE GET: the browser arrives at our
	// origin from the provider's. A Strict cookie is NOT SENT on that
	// navigation, so the flow would fail one hundred percent of the time, in
	// every browser, while passing every test in this repository. mikrodash_sid
	// stays Strict; this one cannot be, and no Go test can tell you that.
	ssoCookieName = "mikrodash_oidc"
	// Scoped, so a cookie only the callback needs is not attached to every
	// other request this app serves.
	ssoCookiePath   = "/api/auth/sso"
	ssoCookieMaxAge = 600

	ssoCallbackPath = "/api/auth/sso/callback"
)

// ssoEntry is one provider's runtime: the configuration as it was read, and the
// oidc.Provider holding its discovery and key caches.
//
// KEYED ON A FINGERPRINT so an operator's edit rebuilds it. Without that a
// changed issuer or secret would keep talking to the old one until a restart,
// which is the kind of bug that gets reported as "the provider is broken".
type ssoEntry struct {
	fingerprint string
	provider    *oidc.Provider
}

// ssoState is the runtime half of single sign-on, held by the Server.
type ssoState struct {
	mu      sync.Mutex
	byID    map[string]*ssoEntry
	pending *oidc.Pending
}

// runtime returns the oidc.Provider for a configured row, rebuilding it when
// the configuration is new or has changed.
func (st *ssoState) runtime(row db.SSOProvider, secret string) *oidc.Provider {
	// NO REDIRECT URI IN THE KEY. It is per-login now, so one install reached at
	// two addresses keeps ONE provider and one cached discovery document rather
	// than rebuilding both whenever the origin changes.
	fp := row.Issuer + "\x00" + row.ClientID + "\x00" + secret
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.byID == nil {
		st.byID = map[string]*ssoEntry{}
	}
	if e, ok := st.byID[row.ID]; ok && e.fingerprint == fp {
		return e.provider
	}
	p := oidc.NewProvider(oidc.Config{
		Issuer: row.Issuer, ClientID: row.ClientID, ClientSecret: secret,
	})
	st.byID[row.ID] = &ssoEntry{fingerprint: fp, provider: p}
	return p
}

// logins is the pending-login store, built on first use.
func (st *ssoState) logins() *oidc.Pending {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.pending == nil {
		st.pending = oidc.NewPending(time.Now)
	}
	return st.pending
}

// forget drops a provider's cached runtime, so an edit or a delete takes effect
// on the next sign-in rather than at the next restart.
func (st *ssoState) forget(id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.byID, id)
}

func (s *Server) registerSSOLogin(mux *http.ServeMux) {
	// ── TWO LIMITS, BECAUSE THESE ARE TWO KINDS OF ROUTE ───────────────────
	//
	// `start` and `callback` get the password form's ten a minute: each `start`
	// mints four random values and a map entry, and `callback` is where a
	// forged state would be guessed at.
	//
	// `providers` MUST NOT share that. The login page fetches it on EVERY page
	// load, exactly as it fetches /api/auth/status - which has no limiter at
	// all for that reason. At ten a minute the eleventh reload would answer 429
	// and the SSO buttons would vanish, which presents as "the button is
	// sometimes not there" and sends somebody looking at the provider.
	flow := newRateLimiter(10, time.Minute).limit
	read := newRateLimiter(60, time.Minute).limit
	mux.HandleFunc("GET /api/auth/sso/providers", read(s.ssoProviders))
	mux.HandleFunc("GET /api/auth/sso/{id}/start", flow(s.ssoStart))
	mux.HandleFunc("GET "+ssoCallbackPath, flow(s.ssoCallback))
}

// ssoProviders tells the login page which buttons to draw.
//
// PUBLIC, AND DELIBERATELY THIN: it names only what a button needs. A disabled
// provider is not mentioned at all - see db.SSOProvidersEnabled on why that
// filter is a query rather than a loop here.
func (s *Server) ssoProviders(w http.ResponseWriter, _ *http.Request) {
	type button struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Icon string `json:"icon"`
	}
	out := []button{}
	if s.auditDB != nil {
		rows, err := s.auditDB.SSOProvidersEnabled()
		if err != nil {
			// AN EMPTY LIST, NOT A 500. The password form must still work when
			// the database is unavailable, and that is the whole break-glass
			// guarantee in one line: a failure here removes SSO buttons, never
			// the way in.
			log.Printf("[sso] listing providers: %v", err)
			writeJSON(w, map[string]any{"providers": out})
			return
		}
		for _, p := range rows {
			b := button{ID: p.ID, Name: p.Name}
			if p.IconVersion > 0 {
				b.Icon = "/brand/sso/" + p.ID + ".png?v=" +
					strconv.FormatInt(p.IconVersion, 10)
			}
			out = append(out, b)
		}
	}
	writeJSON(w, map[string]any{"providers": out})
}

// ssoStart sends the browser to the identity provider.
func (s *Server) ssoStart(w http.ResponseWriter, r *http.Request) {
	row, secret, err := s.ssoConfigFor(r.PathValue("id"))
	if err != nil {
		s.ssoFail(w, r, "", err, "")
		return
	}
	// `next` is NOT validated here: oidc.Pending.Create runs safeNext on it, at
	// the one point every login passes through. See the comment there.
	id, rec, err := s.sso.logins().Create(row.ID, r.URL.Query().Get("next"),
		s.redirectURIFor(r))
	if err != nil {
		// A random-source failure is a refusal, never a weaker token - the rule
		// websession.Create states.
		s.ssoFail(w, r, "", err, "")
		return
	}
	authURL, err := s.sso.runtime(row, secret).
		AuthorizeURL(r.Context(), rec, row.Scopes)
	if err != nil {
		s.ssoFail(w, r, "", err, "")
		return
	}
	w.Header().Set("Set-Cookie", s.ssoCookie(id, ssoCookieMaxAge))
	http.Redirect(w, r, authURL, http.StatusFound)
}

// ssoCallback is where the provider sends the browser back.
func (s *Server) ssoCallback(w http.ResponseWriter, r *http.Request) {
	// THE COOKIE IS CLEARED ON EVERY PATH out of this handler, success or not.
	// A pending id that outlived its login is a value somebody can keep trying.
	w.Header().Add("Set-Cookie", s.ssoCookie("", -1))

	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		s.ssoFail(w, r, "", &oidc.Err{Code: oidc.CodeDenied,
			Detail: "the provider refused: " + logSafe(e)}, "")
		return
	}

	rec, ok := s.sso.logins().Consume(ssoCookieValue(r), q.Get("state"))
	if !ok {
		// UNKNOWN, EXPIRED AND MISMATCHED ARE ONE ANSWER. Telling a stranger
		// which half of their forgery worked is free intelligence. A missing
		// cookie is the login-CSRF case and lands here too.
		s.ssoFail(w, r, "", &oidc.Err{Code: oidc.CodeExpired,
			Detail: "no pending sign-in matched this callback"}, "")
		return
	}

	// THE PROVIDER COMES FROM THE PENDING RECORD, never from the query string.
	// Taking it from the URL would let a caller pair one provider's code with
	// another provider's configuration.
	row, secret, err := s.ssoConfigFor(rec.ProviderID)
	if err != nil {
		s.ssoFail(w, r, "", err, "")
		return
	}
	prov := s.sso.runtime(row, secret)

	// THE REDIRECT URI COMES OFF THE PENDING RECORD, not from this request.
	// The provider compares it byte for byte against the authorization request,
	// and re-deriving it here would differ the moment a proxy header or the
	// host differed between the two hops.
	idToken, err := prov.Exchange(r.Context(), q.Get("code"), rec.Verifier,
		rec.RedirectURI)
	if err != nil {
		s.ssoFail(w, r, "", err, "")
		return
	}
	verified, err := s.ssoVerify(r.Context(), prov, idToken, oidc.Expect{
		Issuer: row.Issuer, ClientID: row.ClientID, Nonce: rec.Nonce,
	})
	if err != nil {
		s.ssoFail(w, r, "", err, "")
		return
	}

	userID, username, legacyRole, err := s.ssoResolveUser(row, verified)
	if err != nil {
		s.ssoFail(w, r, username, err, verified.Subject)
		return
	}

	sess, err := s.sessions4Web.Create(userID, username, legacyRole,
		s.sessionTimeout(), nil)
	if err != nil {
		s.ssoFail(w, r, username, err, verified.Subject)
		return
	}
	w.Header().Add("Set-Cookie",
		s.sessions4Web.BuildCookieHeader(sess.Token, sess.ExpiresAt, s.forceHTTPS))
	log.Printf("[sso] sign-in - provider=%q user=%q role=%s",
		logSafe(row.Name), logSafe(username), legacyRole)
	s.loginRecorder(r, username).Record(audit.Event{
		Action: "auth.sso", Outcome: "ok",
		TargetType: "user", TargetID: userID, TargetName: username,
		Extra: []audit.KV{{Key: "provider", Value: row.Name}},
		Note:  "signed in through an identity provider",
	})

	// ── BACK THROUGH THE LOGIN PAGE, NOT STRAIGHT INTO THE APP ─────────────
	//
	// The session cookie is set, but the browser arrived here by redirect and
	// never ran login.ts - and skipping its `justLoggedIn` handshake renders
	// the app INVISIBLY, which internal/verify/frontend_behaviour_test.go pins.
	// Landing on /login?sso=1 lets the existing post-login path finish the job,
	// including its own safeNext, rather than this handler copying it.
	http.Redirect(w, r, "/login?sso=1&next="+url.QueryEscape(rec.Next),
		http.StatusFound)
}

// ssoVerify checks the token, allowing exactly ONE key refetch.
//
// One, because an unknown `kid` is what an ordinary key rotation looks like -
// and because the callback is reachable by anyone with a link, so the refetch
// is bounded here as well as throttled inside the provider.
func (s *Server) ssoVerify(ctx context.Context, prov *oidc.Provider, idToken string,
	want oidc.Expect) (*oidc.Verified, error) {

	keys, err := prov.Keys(ctx, false)
	if err != nil {
		return nil, err
	}
	v, err := oidc.Verify(idToken, keys, want, time.Now())
	if !errors.Is(err, oidc.ErrNoKey) {
		return v, err
	}
	if keys, err = prov.Keys(ctx, true); err != nil {
		return nil, err
	}
	return oidc.Verify(idToken, keys, want, time.Now())
}

// ssoConfigFor reads a provider row and unseals its secret.
func (s *Server) ssoConfigFor(id string) (db.SSOProvider, string, error) {
	if s.auditDB == nil {
		return db.SSOProvider{}, "", oidc.Refuse(oidc.CodeConfig,
			"single sign-on needs the database, which is unavailable")
	}
	row, err := s.auditDB.SSOProviderByID(id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.SSOProvider{}, "", oidc.Refuse(oidc.CodeConfig, "no such provider")
	}
	if err != nil {
		return db.SSOProvider{}, "", err
	}
	// AN ENABLED CHECK ON EVERY PATH, not only in the listing. Otherwise a
	// disabled provider stays usable by anyone who kept its id, which makes the
	// toggle a suggestion rather than a switch.
	if !row.Enabled {
		return db.SSOProvider{}, "", oidc.Refuse(oidc.CodeConfig,
			"that provider is switched off")
	}
	return row, s.openSSOSecret(row.ClientSecret), nil
}

// baseURLFor is the address browsers reach this install at.
//
// ── THE SETTING IS AN OVERRIDE, NOT A REQUIREMENT ──────────────────────────
//
// Empty means "follow whatever URL this request arrived on", so single sign-on
// works with nothing typed. An operator only fills it in when the derived value
// is wrong - behind a proxy that rewrites the host, or when the address users
// reach is not the address the server sees.
//
// ── ON DERIVING IT FROM THE REQUEST ────────────────────────────────────────
//
// Taking a URL from the Host header is normally the host-header-injection
// footgun: whoever can set Host chooses where a link points. OIDC has a backstop
// that the classic case lacks - THE PROVIDER EXACT-MATCHES the redirect URI
// against the list registered with it, so a forged Host produces
// `invalid_redirect_uri` at the provider rather than a delivered authorization
// code. That match is required by the protocol, not a courtesy, which is what
// makes this safe here and would not make it safe for a password-reset link.
func (s *Server) baseURLFor(r *http.Request) string {
	if s.store != nil {
		if cfg, err := s.store.Settings(); err == nil {
			if v, _ := cfg["baseUrl"].(string); strings.TrimSpace(v) != "" {
				return strings.TrimRight(strings.TrimSpace(v), "/")
			}
		}
	}
	return originOf(r, s.forceHTTPS)
}

// redirectURIFor is where this sign-in comes back to.
func (s *Server) redirectURIFor(r *http.Request) string {
	return s.baseURLFor(r) + ssoCallbackPath
}

// originOf is the scheme and host this request arrived on.
//
// ── X-Forwarded-Proto IS READ, X-Forwarded-Host IS NOT ─────────────────────
//
// The scheme is the half a proxy actually breaks: it terminates TLS and the
// backend sees plain http, so without this header every redirect URI behind a
// proxy would say `http://` and the provider would refuse it. The HOST survives
// a normal proxy configuration untouched, so there is no matching reason to
// trust a header for it - and each header trusted is one more thing a caller
// can set.
func originOf(r *http.Request, forceHTTPS bool) string {
	scheme := "http"
	switch {
	case forceHTTPS, r.TLS != nil:
		scheme = "https"
	}
	// A comma-separated chain lists the ORIGINAL client first.
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		first := strings.TrimSpace(strings.Split(p, ",")[0])
		// An allow-list, not a copy: this string is pasted into a URL.
		if first == "https" || first == "http" {
			scheme = first
		}
	}
	return scheme + "://" + r.Host
}

// ssoResolveUser decides which local account an external identity is.
//
// ── THE TWO REFUSALS ARE THE POINT OF THIS FUNCTION ────────────────────────
//
// A USERNAME COLLISION IS REFUSED. If the claim names somebody who already has
// a local account not bound to this subject, sign-in stops. Without it an
// administrator at the identity provider could mint a token claiming the
// username of MikroDash's own break-glass admin and be handed that account.
//
// NO ROLE MAPPING IS REFUSED, and the grant is removed. The provider decides
// who gets in; an unmapped person gets nothing rather than a quiet default, and
// somebody removed from their group at the provider loses access here at their
// next sign-in instead of keeping a stale grant.
func (s *Server) ssoResolveUser(row db.SSOProvider, v *oidc.Verified) (
	userID, username, legacyRole string, err error) {

	username = strings.TrimSpace(v.StringClaim(row.ClaimUsername))
	if username == "" {
		return "", "", "", oidc.Refuse(oidc.CodeConfig,
			"the token carries no "+row.ClaimUsername+" claim")
	}

	// `sub` IS THE ACCOUNT KEY. Never email, never preferred_username: on many
	// providers a person can change their own email, and if either were the
	// join key, changing it to match an existing account would be takeover with
	// no exploit at all.
	bound, err := s.auditDB.SSOIdentityFor(row.ID, v.Subject)
	if err != nil {
		return "", username, "", err
	}

	// The role first, because somebody with no mapping does not get an account
	// created for them.
	roleID, err := s.ssoRoleFor(row, v)
	if err != nil {
		if bound != "" {
			// They had access and no longer qualify. Removing the grant is the
			// half that makes deprovisioning at the provider actually reach
			// this app; refusing the sign-in alone would leave the grant behind
			// for the next mechanism that reads it.
			if _, derr := s.auditDB.DeleteGrantsForPrincipal("user", bound); derr != nil {
				log.Printf("[sso] removing the grant for %q: %v", logSafe(username), derr)
			}
		}
		return "", username, "", err
	}

	userID = bound
	if userID == "" {
		// THE COLLISION RULE.
		if existing := s.userIDFor(username); existing != "" {
			return "", username, "", oidc.Refuse(oidc.CodeDenied,
				"a local account already uses that username")
		}
		created, cerr := s.store.CreateExternalUser(username, legacyRoleFor(roleID))
		if cerr != nil {
			return "", username, "", cerr
		}
		userID, _ = created["id"].(string)
		if userID == "" {
			return "", username, "", errors.New("the new account has no id")
		}
	}

	if err := s.auditDB.UpsertSSOIdentity(row.ID, v.Subject, userID); err != nil {
		return "", username, "", err
	}
	// RE-EVALUATED ON EVERY SIGN-IN, which is what keeps the provider the
	// source of truth. A role edited locally is overwritten here, and Access
	// Management says so where an SSO account's role is shown - otherwise the
	// edit looks broken rather than ignored.
	if err := s.auditDB.UpsertGrant(db.GrantSpec{
		PrincipalType: "user", PrincipalID: userID, RoleID: roleID,
		ScopeType: "global", ScopeID: "", CreatedBy: "sso:" + row.Name,
	}); err != nil {
		return "", username, "", err
	}
	return userID, username, legacyRoleFor(roleID), nil
}

// ssoRoleFor maps the token's roles claim onto a MikroDash role id.
func (s *Server) ssoRoleFor(row db.SSOProvider, v *oidc.Verified) (string, error) {
	// ── THE OVERAGE CASE, NAMED RATHER THAN READ AS "NO GROUPS" ────────────
	//
	// Entra omits the groups claim past 200 and points at Graph instead. We do
	// not call Graph, so every mapping would miss - and refusing with "you have
	// no role here" would send the operator to inspect a mapping that is fine.
	if v.HasClaimOverage(row.ClaimRoles) {
		return "", oidc.Refuse(oidc.CodeConfig, "the identity provider left the "+
			row.ClaimRoles+" claim out because there were too many; use app roles, "+
			"or narrow the claim to the groups assigned to this application")
	}
	mappings, err := s.auditDB.SSORoleMap(row.ID)
	if err != nil {
		return "", err
	}
	claimed := v.StringsClaim(row.ClaimRoles)
	for _, m := range mappings {
		for _, c := range claimed {
			if c == m.ClaimValue {
				return m.RoleID, nil
			}
		}
	}
	return "", oidc.Refuse(oidc.CodeDenied,
		"no role mapping matched this person's "+row.ClaimRoles)
}

// legacyRoleFor is the users.json `role` mirror for a role id.
//
// A MIRROR, NOT THE DECISION: the grant carries the real role. It narrows the
// same way rbac.grantRole does, and for the same reason - users.json only ever
// knew three roles, so a custom one has to read as the least of them rather
// than as the most.
func legacyRoleFor(roleID string) string {
	switch roleID {
	case "administrator":
		return "admin"
	case "operator":
		return "operator"
	}
	return "viewer"
}

// ssoCookie builds the binding cookie. A negative maxAge clears it.
func (s *Server) ssoCookie(value string, maxAge int) string {
	c := ssoCookieName + "=" + value + "; HttpOnly; SameSite=Lax; Path=" + ssoCookiePath
	if maxAge < 0 {
		c += "; Max-Age=0"
	} else {
		c += "; Max-Age=" + strconv.Itoa(maxAge)
	}
	if s.forceHTTPS {
		c += "; Secure"
	}
	return c
}

func ssoCookieValue(r *http.Request) string {
	for _, c := range r.Cookies() {
		if c.Name == ssoCookieName {
			return c.Value
		}
	}
	return ""
}

// ssoFail is the one way out of a failed sign-in.
//
// ── ONE SHORT CODE TO THE BROWSER, THE DETAIL TO THE LOG ───────────────────
//
// Sign-in has a dozen distinct ways to fail and a stranger can reach every one
// of them, so they collapse into the handful of codes internal/oidc declares. A
// correlation id joins what the operator sees to the line that explains it -
// without one, a generic message about a remote system they cannot see into
// leaves them with nothing to do next.
func (s *Server) ssoFail(w http.ResponseWriter, r *http.Request, username string,
	err error, subject string) {

	ref := ssoRef()
	code := oidc.CodeProvider
	detail := err.Error()
	var oe *oidc.Err
	if errors.As(err, &oe) {
		code, detail = oe.Code, oe.Detail
	}
	log.Printf("[sso] %s refused (%s): %s", ref, code, logSafe(detail))

	// AUDITED AS denied OR failed, and the difference is not cosmetic: `denied`
	// is a decision about a person, `failed` is this app or the provider not
	// working. An operator filtering the trail for refusals wants the first.
	ev := audit.Event{Action: "auth.sso", TargetType: "user", TargetName: username,
		Extra: []audit.KV{{Key: "reason", Value: code}, {Key: "ref", Value: ref}}}
	if subject != "" {
		ev.Extra = append(ev.Extra, audit.KV{Key: "subject", Value: subject})
	}
	rec := s.loginRecorder(r, username)
	if code == oidc.CodeDenied || code == oidc.CodeExpired {
		rec.Denied(ev)
	} else {
		rec.Failed(ev)
	}
	http.Redirect(w, r, "/login?err="+url.QueryEscape(code)+"&ref="+ref, http.StatusFound)
}

// ssoRef is the short id joining a browser message to a log line. It identifies
// an event, not a person, and carries nothing from the token.
func ssoRef() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "________"
	}
	return hex.EncodeToString(b)
}
