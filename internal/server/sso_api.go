package server

// The SSO providers REST surface.
//
// ── BEHIND THE ACCESS MANAGEMENT GATE, NOT THE SETTINGS ONE ────────────────
//
// A provider decides who may sign in and what role they arrive with, which is
// the same question `/api/roles` and `/api/grants` answer. So it goes through
// `principalsGuard` - the `system:principals` permission - rather than through
// `maySaveSettings`. Branding's gate would have been the easier copy and the
// wrong one: it would let anyone who may change the app's name decide who may
// enter it.
//
// ── THE SECRET GOES IN SEALED AND NEVER COMES BACK ─────────────────────────
//
// `client_secret` is encrypted with the settings envelope BEFORE it reaches
// internal/db, exactly as a notification channel's config is, and `providerView`
// has no field for it. It reports only whether one is set. An edit that omits
// it keeps the stored one, so saving the form does not silently blank the
// secret - the same rule the SMTP channel follows.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/branding"
	"mikrodash/internal/db"
	"mikrodash/internal/safe"
)

// maxProviderBody bounds a save. The role map is the biggest thing in it.
const maxProviderBody = 32 << 10

func (s *Server) registerSSOAPI(mux *http.ServeMux) {
	rw := newRateLimiter(60, time.Minute).limit
	mux.HandleFunc("GET "+principalsPrefix+"/sso/providers", rw(s.principalsGuard(s.ssoList)))
	mux.HandleFunc("POST "+principalsPrefix+"/sso/providers", rw(s.principalsGuard(s.ssoCreate)))
	mux.HandleFunc("PUT "+principalsPrefix+"/sso/providers/{id}", rw(s.principalsGuard(s.ssoUpdate)))
	mux.HandleFunc("DELETE "+principalsPrefix+"/sso/providers/{id}", rw(s.principalsGuard(s.ssoDelete)))
	mux.HandleFunc("POST "+principalsPrefix+"/sso/providers/{id}/icon", rw(s.principalsGuard(s.ssoIconSave)))
	mux.HandleFunc("DELETE "+principalsPrefix+"/sso/providers/{id}/icon", rw(s.principalsGuard(s.ssoIconClear)))
	// PUBLIC, like /brand/icon.png: the login page draws these buttons before
	// anybody has signed in. It discloses a mark the login page already shows.
	mux.HandleFunc("GET /brand/sso/{file}", s.ssoIcon)
}

// providerBody is what the browser sends.
type providerBody struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Issuer  string `json:"issuer"`
	// ClientID and ClientSecret. An ABSENT secret keeps the stored one; an
	// empty string clears it. `*string` is what distinguishes those, and the
	// difference is the whole reason this field is a pointer: a plain string
	// would blank the secret every time the form was saved without retyping it.
	ClientID      string  `json:"clientId"`
	ClientSecret  *string `json:"clientSecret"`
	Scopes        string  `json:"scopes"`
	ClaimUsername string  `json:"claimUsername"`
	ClaimEmail    string  `json:"claimEmail"`
	ClaimName     string  `json:"claimName"`
	ClaimRoles    string  `json:"claimRoles"`
	// RoleMap is the whole set, replaced rather than merged. See
	// db.ReplaceSSORoleMap.
	RoleMap []db.SSORoleMapping `json:"roleMap"`
}

// providerView is what the browser receives. THE SECRET IS NOT IN IT.
type providerView struct {
	db.SSOProvider
	HasSecret bool                `json:"hasSecret"`
	Icon      string              `json:"icon"`
	RoleMap   []db.SSORoleMapping `json:"roleMap"`
	// RedirectURI is shown read-only in the form, because the operator has to
	// paste it into the provider and a typed copy is the commonest way this
	// feature fails to work at all.
	RedirectURI string `json:"redirectUri"`
}

func (s *Server) providerView(p db.SSOProvider, redirectURI string) providerView {
	v := providerView{SSOProvider: p, HasSecret: p.ClientSecret != "",
		RoleMap: []db.SSORoleMapping{}}
	// The sealed secret must not travel even inside the embedded struct. It is
	// `json:"-"` there, and this makes it so regardless.
	v.SSOProvider.ClientSecret = ""
	if p.IconVersion > 0 {
		v.Icon = "/brand/sso/" + p.ID + ".png?v=" + strconv.FormatInt(p.IconVersion, 10)
	}
	if m, err := s.auditDB.SSORoleMap(p.ID); err == nil {
		v.RoleMap = m
	}
	v.RedirectURI = redirectURI
	return v
}

func (s *Server) ssoList(w http.ResponseWriter, r *http.Request, _ *Session) {
	rows, err := s.auditDB.SSOProviders()
	if err != nil {
		log.Printf("[sso] listing: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the providers")
		return
	}
	base := s.baseURLFor(r)
	redirect := base + ssoCallbackPath
	out := []providerView{}
	for _, p := range rows {
		out = append(out, s.providerView(p, redirect))
	}
	// `redirectUri` at the TOP LEVEL as well as on each row: the dialog shows it
	// read-only, and the FIRST provider on an install has no row to read it
	// from.
	writeJSON(w, map[string]any{"providers": out, "redirectUri": redirect})
}

func (s *Server) ssoCreate(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.ssoWrite(w, r, sess, "")
}

func (s *Server) ssoUpdate(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.ssoWrite(w, r, sess, r.PathValue("id"))
}

// ssoWrite is create and update in one, because the validation is identical and
// two copies of it would differ the first time one was changed.
func (s *Server) ssoWrite(w http.ResponseWriter, r *http.Request, sess *Session, id string) {
	var body providerBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProviderBody)).
		Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request")
		return
	}

	var prev db.SSOProvider
	if id != "" {
		var err error
		if prev, err = s.auditDB.SSOProviderByID(id); err != nil {
			writeJSONErr(w, http.StatusNotFound, "no such provider")
			return
		}
	}

	p, err := s.buildProvider(id, prev, body, sess)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	if err := s.auditDB.UpsertSSOProvider(p); err != nil {
		log.Printf("[sso] saving %q: %v", logSafe(p.Name), err)
		writeJSONErr(w, http.StatusInternalServerError, "could not save the provider")
		return
	}
	if err := s.auditDB.ReplaceSSORoleMap(p.ID, body.RoleMap); err != nil {
		log.Printf("[sso] saving the role map for %q: %v", logSafe(p.Name), err)
		writeJSONErr(w, http.StatusInternalServerError, "could not save the role mapping")
		return
	}
	// THE CACHED RUNTIME IS DROPPED HERE TOO. The fingerprint would catch a
	// changed issuer or secret on its own; this also catches a role map or an
	// enabled flag, and costs one discovery fetch on the next sign-in.
	s.sso.forget(p.ID)

	action := "sso.provider.update"
	if id == "" {
		action = "sso.provider.create"
	}
	// ── WHAT THE TRAIL RECORDS, AND WHAT IT MUST NOT ───────────────────────
	//
	// Before/After carry the issuer, the client id and whether it is enabled -
	// the fields an operator needs to see changed. NEVER the secret, not even
	// as "changed": the audit trail is readable by anyone who may read it, and
	// a sealed value has no business being copied into a second store.
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: action, TargetType: "sso_provider", TargetID: p.ID, TargetName: p.Name,
		Before: auditProvider(prev), After: auditProvider(p),
		Extra: []audit.KV{{Key: "mappings", Value: strconv.Itoa(len(body.RoleMap))}},
	})
	saved, err := s.auditDB.SSOProviderByID(p.ID)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read the provider back")
		return
	}
	writeJSON(w, s.providerView(saved, s.redirectURIFor(r)))
}

// buildProvider validates the body and returns the row to store.
//
// PURE APART FROM SEALING: no database, no HTTP. The checks below are the whole
// of what this feature accepts, so they are worth reading as a list.
func (s *Server) buildProvider(id string, prev db.SSOProvider, body providerBody,
	sess *Session) (db.SSOProvider, error) {

	p := db.SSOProvider{
		ID: id, Enabled: body.Enabled,
		Name:          strings.TrimSpace(body.Name),
		Issuer:        strings.TrimRight(strings.TrimSpace(body.Issuer), "/"),
		ClientID:      strings.TrimSpace(body.ClientID),
		Scopes:        strings.TrimSpace(body.Scopes),
		ClaimUsername: claimOr(body.ClaimUsername, "preferred_username"),
		ClaimEmail:    claimOr(body.ClaimEmail, "email"),
		ClaimName:     claimOr(body.ClaimName, "name"),
		ClaimRoles:    claimOr(body.ClaimRoles, "groups"),
		IconVersion:   prev.IconVersion,
		CreatedBy:     prev.CreatedBy,
		CreatedAt:     prev.CreatedAt,
	}
	if p.Name == "" {
		return p, errors.New("the provider needs a name")
	}
	if len(p.Name) > 64 {
		return p, errors.New("the name is too long")
	}
	// ── HTTPS, AND NO PATH TRICKS ──────────────────────────────────────────
	//
	// The issuer is the string an id token's `iss` must equal, and it is also
	// the host every metadata fetch goes to. internal/oidc refuses a non-HTTPS
	// endpoint at fetch time; refusing here as well means the operator finds
	// out while typing rather than at the first failed sign-in.
	u, err := url.Parse(p.Issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return p, errors.New("the issuer must be an https URL with no query or fragment")
	}
	if p.ClientID == "" {
		return p, errors.New("the provider needs a client ID")
	}
	if p.Scopes == "" {
		p.Scopes = "openid profile email"
	}
	// `openid` IS NOT OPTIONAL: without it the provider runs a plain OAuth
	// flow and returns no id token at all, and the failure reads as "the
	// provider sent nothing" rather than as a missing scope.
	if !strings.Contains(" "+p.Scopes+" ", " openid ") {
		p.Scopes = "openid " + p.Scopes
	}

	switch {
	case body.ClientSecret == nil:
		// ABSENT KEEPS THE STORED SECRET. This is what lets the form be saved
		// without retyping it.
		p.ClientSecret = prev.ClientSecret
	case *body.ClientSecret == "":
		p.ClientSecret = ""
	default:
		sealed, err := s.sealChannelConfig(*body.ClientSecret)
		if err != nil {
			// NEVER STORED IN THE CLEAR, for the reason sealChannelConfig
			// gives about a channel config: the refusal is the safe outcome.
			return p, errors.New("the secret could not be encrypted for storage")
		}
		p.ClientSecret = sealed
	}

	for _, m := range body.RoleMap {
		if strings.TrimSpace(m.ClaimValue) == "" || strings.TrimSpace(m.RoleID) == "" {
			return p, errors.New("every role mapping needs a claim value and a role")
		}
	}

	if id == "" {
		newID, err := ssoNewID()
		if err != nil {
			return p, err
		}
		p.ID, p.CreatedBy, p.CreatedAt = newID, s.webUserID(sess), time.Now().UnixMilli()
	}
	return p, nil
}

// claimOr falls back to the claim name the provider most likely uses, so a
// blank field in the form is a default rather than a broken configuration.
func claimOr(v, def string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return def
}

// auditProvider is the audit view: what changed, never the secret.
func auditProvider(p db.SSOProvider) map[string]any {
	if p.ID == "" {
		return nil
	}
	return map[string]any{"name": p.Name, "enabled": p.Enabled, "issuer": p.Issuer,
		"clientId": p.ClientID, "scopes": p.Scopes, "claimRoles": p.ClaimRoles}
}

func (s *Server) ssoDelete(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	prev, err := s.auditDB.SSOProviderByID(id)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, "no such provider")
		return
	}
	if err := s.auditDB.DeleteSSOProvider(id); err != nil {
		log.Printf("[sso] deleting %q: %v", logSafe(prev.Name), err)
		writeJSONErr(w, http.StatusInternalServerError, "could not delete the provider")
		return
	}
	// The role map and the identities go with it by the foreign keys; the icon
	// is a file and has to be removed by hand, or it outlives its provider and
	// is then served under an id nothing else knows about.
	s.removeSSOIcon(id)
	s.sso.forget(id)
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "sso.provider.delete", TargetType: "sso_provider",
		TargetID: id, TargetName: prev.Name, Before: auditProvider(prev),
		Note: "the accounts it created remain, with their grants",
	})
	writeJSON(w, map[string]any{"ok": true})
}

// ── THE ICON ───────────────────────────────────────────────────────────────
//
// `branding.Icon` is reused unchanged: it is a pure function that re-encodes an
// upload as an 8-bit PNG, keeping the pixels and dropping metadata, trailing
// bytes and any second format hiding behind the first. It enforces square and
// 64-512 px, which is right for a mark beside a button label.
//
// The bytes go in a file beside settings.json rather than in the database,
// where `icon_version` is only the cache-buster. That is the same split
// branding uses, and it keeps a binary out of a table every audit query reads.

// ssoIconID is the only shape an id may have on the way to a file path.
//
// The ids this server mints are hex, so this refuses everything else - and in
// particular anything containing a separator or a dot. A path built from an
// unvalidated URL segment is directory traversal; the regexp is the guard, not
// the `filepath.Join`, which happily joins "..".
var ssoIconID = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

func (s *Server) ssoIconPath(id string) string {
	if !ssoIconID.MatchString(id) || s.brandingDir() == "" {
		return ""
	}
	return filepath.Join(s.brandingDir(), "sso-icon-"+id+".png")
}

func (s *Server) ssoIconSave(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	prev, err := s.auditDB.SSOProviderByID(id)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, "no such provider")
		return
	}
	path := s.ssoIconPath(id)
	if path == "" {
		writeJSONErr(w, http.StatusServiceUnavailable, "data directory unavailable")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, branding.MaxIconBytes))
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "the icon is larger than "+
			strconv.Itoa(branding.MaxIconBytes>>10)+" KB")
		return
	}
	icon, err := branding.Icon(raw)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	if err := os.WriteFile(path, icon, 0o600); err != nil {
		log.Printf("[sso] writing the icon for %q: %v", logSafe(prev.Name), err)
		writeJSONErr(w, http.StatusInternalServerError, "could not save the icon")
		return
	}
	prev.IconVersion = time.Now().UnixMilli()
	if err := s.auditDB.UpsertSSOProvider(prev); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not record the icon")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "sso.provider.icon", TargetType: "sso_provider",
		TargetID: id, TargetName: prev.Name,
	})
	writeJSON(w, s.providerView(prev, s.redirectURIFor(r)))
}

func (s *Server) ssoIconClear(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	prev, err := s.auditDB.SSOProviderByID(id)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, "no such provider")
		return
	}
	s.removeSSOIcon(id)
	prev.IconVersion = 0
	if err := s.auditDB.UpsertSSOProvider(prev); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not clear the icon")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "sso.provider.icon", TargetType: "sso_provider",
		TargetID: id, TargetName: prev.Name, Note: "icon removed",
	})
	writeJSON(w, s.providerView(prev, s.redirectURIFor(r)))
}

func (s *Server) removeSSOIcon(id string) {
	if path := s.ssoIconPath(id); path != "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("[sso] removing an icon: %v", err)
		}
	}
}

// ssoIcon serves a provider's icon. Public, for the reason above.
func (s *Server) ssoIcon(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("file"), ".png")
	path := s.ssoIconPath(id)
	if path == "" {
		http.NotFound(w, r)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	// nosniff, as /brand/icon.png sets: these bytes were re-encoded by
	// branding.Icon and are a PNG, and the header says nothing else is allowed
	// to be inferred.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(raw)
}

// openSSOSecret unseals a stored client secret.
//
// ── WHY THIS EXISTS RATHER THAN A DIRECT openChannelConfig CALL ────────────
//
// `sealChannelConfig` MARSHALS what it is given, so sealing the string "hunter2"
// stores the four-byte-longer JSON `"hunter2"`, and opening it returns that
// JSON rather than the secret. Sending the quoted form to the provider is a
// wrong client secret that looks exactly like a right one - the provider
// answers `invalid_client`, which reads as a typo in the field. One decode,
// here, so no caller has to know.
func (s *Server) openSSOSecret(stored string) string {
	if stored == "" {
		return ""
	}
	var out string
	if err := json.Unmarshal([]byte(s.openChannelConfig(stored)), &out); err != nil {
		return ""
	}
	return out
}

// ssoNewID mints an opaque provider id. Hex, so ssoIconID can be as strict as
// it is.
func ssoNewID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("no entropy for a provider id")
	}
	return hex.EncodeToString(b), nil
}
