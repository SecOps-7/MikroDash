package server

// The credential profile API: /api/credentials/…
//
// ── THE GATE IS TWO LAYERS, AND BOTH ARE NEEDED ─────────────────────────────
//
//  1. A profile is a fleet-wide credential, so changing one needs a SIGNED-IN
//     global administrator — `cfgWrite`, which asks `cfgMayChange`, not
//     `isGlobalAdmin`, which answers true with sign-in switched off.
//  2. Linking is a write to ONE router, so it is checked per router against the
//     same permission that lets somebody create a RouterOS user from the Router
//     Users page: `users` / `write`. A router the caller cannot see is DROPPED
//     rather than refused, so the answer never confirms that a guessed id
//     exists — issue #108's rule, which `fleetTargets` follows too.
//
// ── AND THE PASSWORD NEVER COMES BACK ───────────────────────────────────────
//
// `credProfileView` sends `hasSecret` and nothing else. The stored value is
// sealed, so returning it would be returning ciphertext — still a thing worth
// not putting in every list response. `providerView` in sso_api.go is the same
// decision with the same reasoning.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/credprof"
	"mikrodash/internal/db"
	"mikrodash/internal/resource"
	"mikrodash/internal/safe"
)

// credProfileView is one profile as the browser sees it: no secret, sealed or
// otherwise, and the policy list decoded.
type credProfileView struct {
	db.CredProfile
	Policies  []string `json:"policies"`
	HasSecret bool     `json:"hasSecret"`
	Links     int      `json:"links"`
}

func credViewOf(p db.CredProfile, links int) credProfileView {
	policies := nonNilPolicies(credprof.ParsePolicies(p.PolicyJSON))
	has := p.Secret != ""
	// BLANKED ON THE COPY THAT LEAVES. The embedded struct's json tags already
	// say "-", and this is the belt to that brace: a later edit that gives
	// Secret a tag cannot leak it through this view.
	p.Secret, p.PolicyJSON = "", ""
	return credProfileView{CredProfile: p, Policies: policies, HasSecret: has, Links: links}
}

// nonNilPolicies keeps the payload's array an array. Go never sends a null
// array — TestNoServerPayloadSendsANullArray.
func nonNilPolicies(p []string) []string {
	if p == nil {
		return []string{}
	}
	return p
}

// credProfileIn is what the form sends.
//
// `Password` is absent on an edit that is not changing it, the same shape
// `resource.TypeSecret` uses and for the same reason: the browser never reads
// one back, so an empty box is an omission rather than an instruction.
type credProfileIn struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Username    string   `json:"username"`
	PermKind    string   `json:"permKind"`
	Builtin     string   `json:"builtinGroup"`
	GroupName   string   `json:"groupName"`
	Policies    []string `json:"policies"`
	Password    string   `json:"password"`
}

func (s *Server) registerCredProfileAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/credentials/profiles", s.cfgRead(s.credProfilesList))
	mux.HandleFunc("POST /api/credentials/profiles",
		s.cfgWrite("credprofile.create", s.credProfileCreate))
	mux.HandleFunc("PUT /api/credentials/profiles/{id}",
		s.cfgWrite("credprofile.update", s.credProfileUpdate))
	mux.HandleFunc("DELETE /api/credentials/profiles/{id}",
		s.cfgWrite("credprofile.delete", s.credProfileDelete))
	mux.HandleFunc("POST /api/credentials/profiles/{id}/forget",
		s.cfgWrite("credprofile.forget", s.credProfileForget))

	mux.HandleFunc("GET /api/credentials/profiles/{id}/links", s.cfgRead(s.credLinksList))
	mux.HandleFunc("POST /api/credentials/profiles/{id}/links",
		s.cfgWrite("credprofile.link", s.credLinkAdd))
	mux.HandleFunc("DELETE /api/credentials/profiles/{id}/links/{routerId}",
		s.cfgWrite("credprofile.unlink", s.credLinkRemove))
	mux.HandleFunc("POST /api/credentials/profiles/{id}/links/{routerId}/retry",
		s.cfgWrite("credprofile.retry", s.credLinkRetry))
}

func (s *Server) credProfilesList(w http.ResponseWriter, r *http.Request, sess *Session) {
	profiles, err := s.auditDB.CredProfiles()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read the profiles")
		return
	}
	links, err := s.auditDB.CredLinks("")
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read the links")
		return
	}
	count := map[string]int{}
	for _, l := range links {
		count[l.ProfileID]++
	}
	out := make([]credProfileView, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, credViewOf(p, count[p.ID]))
	}
	// ── THE POLICY VOCABULARY TRAVELS WITH THE LIST ─────────────────────────
	//
	// `resource.UserPolicies` is the one list, and the form renders from THIS
	// rather than from a copy typed into TypeScript. CLAUDE.md records what a
	// second copy costs: `dnsStatic` offered six of the nine record types
	// RouterOS supports, so a router holding an MX record opened a form showing
	// "A", and saving rewrote the record. A policy missing from a hand-kept copy
	// would be one a custom group could never be given, invisibly.
	writeJSON(w, map[string]any{
		"profiles":         out,
		"policyVocabulary": resource.UserPolicies,
	})
}

func (s *Server) credProfileCreate(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.credProfileSave(w, r, sess, "")
}

func (s *Server) credProfileUpdate(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.credProfileSave(w, r, sess, r.PathValue("id"))
}

// credProfileSave creates or updates one profile.
//
// ── THE PROFILE IS CHECKED BEFORE IT IS STORED ──────────────────────────────
//
// `credprof.CheckSpec` refuses a reserved username, an unknown policy, a custom
// group shadowing a built-in one, and a blank password. Every one of those
// would be refused per router later too, and that is the point of doing it
// here: otherwise the operator links the profile to forty devices and gets
// forty error rows describing one mistake, none of them at the moment it was
// made.
func (s *Server) credProfileSave(w http.ResponseWriter, r *http.Request, sess *Session, id string) {
	var in credProfileIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "could not read the request")
		return
	}

	var existing db.CredProfile
	creating := id == ""
	if creating {
		var err error
		if id, err = credNewID(); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "could not mint a profile id")
			return
		}
	} else {
		var err error
		if existing, err = s.auditDB.CredProfileByID(id); err != nil {
			writeJSONErr(w, http.StatusNotFound, "no such profile")
			return
		}
	}

	// A BLANK PASSWORD ON AN EDIT KEEPS THE STORED ONE, and on a create there is
	// nothing to keep — the same split `resource.RequiredOnCreate` makes, for the
	// bug it was added for. CheckSpec below refuses the blank-on-create case.
	sealed, plain := existing.Secret, in.Password
	if plain == "" && !creating {
		plain = s.openCredSecret(existing.Secret)
	} else if plain != "" {
		var err error
		if sealed, err = s.sealChannelConfig(plain); err != nil {
			writeJSONErr(w, http.StatusServiceUnavailable, "settings storage is unavailable")
			return
		}
	}

	spec := credprof.Spec{
		ID: id, Name: strings.TrimSpace(in.Name),
		Username: strings.TrimSpace(in.Username),
		PermKind: in.PermKind, Builtin: in.Builtin,
		Group:    strings.TrimSpace(in.GroupName),
		Policies: in.Policies, Password: plain,
	}
	if spec.Name == "" {
		writeJSONErr(w, http.StatusBadRequest, "a profile needs a name")
		return
	}
	if err := credprof.CheckSpec(spec); err != nil {
		// THROUGH safe.Message LIKE EVERY OTHER ERROR BODY, even though this one
		// is our own prose rather than a driver's. `TestNoRawErrorReachesAnHttpBody`
		// pins the SHAPE deliberately, not the content: an exception carved out
		// for "this one is safe by inspection" is how the next one gets in.
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}

	row := db.CredProfile{
		ID: id, Name: spec.Name, Description: strings.TrimSpace(in.Description),
		Username: spec.Username, PermKind: spec.PermKind, Builtin: spec.Builtin,
		GroupName: spec.Group, PolicyJSON: credprof.MarshalPolicies(spec.Policies),
		Secret: sealed, CreatedBy: existing.CreatedBy, CreatedAt: existing.CreatedAt,
	}
	if creating {
		row.CreatedBy = s.userIDFor(sess.Username)
	}
	if err := s.auditDB.UpsertCredProfile(row); err != nil {
		writeJSONErr(w, http.StatusConflict, credConflictMessage(err))
		return
	}

	saved, err := s.auditDB.CredProfileByID(id)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError,
			"the profile was saved but could not be read back")
		return
	}
	// A REVISION BUMP MEANS EVERY LINKED DEVICE OWES AN UPDATE. Queuing them is
	// what makes "changes update the credential on each router" true rather than
	// a thing that happens the next time somebody presses something.
	if saved.Revision != existing.Revision {
		if err := s.auditDB.EnqueueCredLinks(id); err != nil {
			writeJSONErr(w, http.StatusInternalServerError,
				"the profile was saved but its routers could not be queued")
			return
		}
		s.wakeCredJob()
	}

	action := "credprofile.update"
	if creating {
		action = "credprofile.create"
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: action, TargetType: "credential-profile", TargetID: id, TargetName: spec.Name,
		Before: credAuditValues(existing), After: credAuditValues(saved),
	})
	writeJSON(w, credViewOf(saved, 0))
}

// credAuditValues is what the trail records about a profile.
//
// ── THE PASSWORD IS NOT HERE, IN ANY FORM ───────────────────────────────────
//
// Not the plaintext, not the sealed blob, and not a "changed" flag derived from
// comparing ciphertext. The REVISION already says that something a router can
// see has changed, and it says it without putting a credential-shaped value in
// a table people export.
func credAuditValues(p db.CredProfile) map[string]any {
	if p.ID == "" {
		return map[string]any{}
	}
	return map[string]any{
		"name": p.Name, "username": p.Username, "permKind": p.PermKind,
		"builtinGroup": p.Builtin, "groupName": p.GroupName,
		"policies": p.PolicyJSON, "revision": p.Revision,
	}
}

// credConflictMessage turns a UNIQUE violation into something an operator can
// act on. Both columns are unique for a reason the message should carry.
func credConflictMessage(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "ros_username"):
		return "another profile already creates that RouterOS username"
	case strings.Contains(msg, "cred_profiles.name"):
		return "another profile already has that name"
	}
	return "the profile could not be saved"
}

// credProfileDelete removes a profile that no router still has.
//
// It REFUSES while any link survives. The operator unlinks first, which takes
// the accounts off the devices, or uses Forget, which is a different act with
// its own name.
func (s *Server) credProfileDelete(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	p, err := s.auditDB.CredProfileByID(id)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, "no such profile")
		return
	}
	links, err := s.auditDB.CredLinks(id)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read the links")
		return
	}
	if len(links) > 0 {
		writeJSONErr(w, http.StatusConflict,
			"this profile is still on routers. Unlink it from them first, or use Forget "+
				"to drop it and leave the accounts where they are.")
		return
	}
	if err := s.auditDB.DeleteCredProfile(id); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the profile could not be removed")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "credprofile.delete", TargetType: "credential-profile",
		TargetID: id, TargetName: p.Name,
		Before: credAuditValues(p), After: map[string]any{},
	})
	writeJSON(w, map[string]any{"ok": true})
}

// credProfileForget drops the profile and its links WITHOUT touching a router.
//
// ── EVERY ABANDONED ACCOUNT IS NAMED IN THE TRAIL ───────────────────────────
//
// This is the one path that leaves logins on devices with nothing in MikroDash
// that knows. It exists because the alternative — a profile that can never be
// removed once a router has gone for good — is worse. What makes it acceptable
// is that the audit row lists every router it walked away from, and
// `audit_events` is absent from every purge path by design.
func (s *Server) credProfileForget(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	p, err := s.auditDB.CredProfileByID(id)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, "no such profile")
		return
	}
	links, err := s.auditDB.CredLinks(id)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read the links")
		return
	}
	abandoned := make([]string, 0, len(links))
	for _, l := range links {
		abandoned = append(abandoned, l.RouterID)
	}
	// RECORDED BEFORE THE ROWS GO. Afterwards there is nothing left to list.
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "credprofile.forget", TargetType: "credential-profile",
		TargetID: id, TargetName: p.Name,
		Before: credAuditValues(p), After: map[string]any{},
		Extra: []audit.KV{
			{Key: "abandonedOnRouters", Value: abandoned},
			{Key: "rosUsername", Value: p.Username},
		},
	})
	if err := s.auditDB.ForgetCredProfile(id); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the profile could not be dropped")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "abandoned": len(abandoned)})
}

func (s *Server) credLinksList(w http.ResponseWriter, r *http.Request, sess *Session) {
	links, err := s.auditDB.CredLinks(r.PathValue("id"))
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read the links")
		return
	}
	writeJSON(w, map[string]any{"links": links})
}

// credLinkAdd links a profile to routers the caller may write users on.
//
// ── THE PERMISSION IS CHECKED PER ROUTER, AND NOT THROUGH fleetTargets ──────
//
// `fleetTargets` caps a request at `fleetMaxRouters` because it is sized for a
// synchronous fleet READ that somebody is waiting on. Linking writes rows and
// queues work; capping it at sixteen would silently link part of a selection
// and leave the operator to work out which part. So the same per-router grant
// is asked for directly, with no cap.
func (s *Server) credLinkAdd(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	p, err := s.auditDB.CredProfileByID(id)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, "no such profile")
		return
	}
	var in struct {
		RouterIDs []string `json:"routerIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "could not read the request")
		return
	}

	by := s.userIDFor(sess.Username)
	linked := []string{}
	for _, rid := range s.credRoutersFor(sess, in.RouterIDs) {
		if err := s.auditDB.LinkCredProfile(id, rid, by); err != nil {
			continue
		}
		linked = append(linked, rid)
	}
	if len(linked) > 0 {
		s.httpRecorder(r, sess).Record(audit.Event{
			Action: "credprofile.link", TargetType: "credential-profile",
			TargetID: id, TargetName: p.Name,
			After: map[string]any{"routers": linked, "username": p.Username},
		})
		s.wakeCredJob()
	}
	writeJSON(w, map[string]any{"linked": linked})
}

// credLinkRemove asks for the account to be taken off one router.
//
// ── IT DOES NOT DELETE THE ROW ──────────────────────────────────────────────
//
// The row goes to `removing` and the reconciler takes the account off; only a
// CONFIRMED removal drops it. Deleting here would mean an unreachable router
// keeps the account for ever with nothing left that knows about it, which is
// the worst outcome this feature can produce.
func (s *Server) credLinkRemove(w http.ResponseWriter, r *http.Request, sess *Session) {
	id, rid := r.PathValue("id"), r.PathValue("routerId")
	p, err := s.auditDB.CredProfileByID(id)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, "no such profile")
		return
	}
	if len(s.credRoutersFor(sess, []string{rid})) == 0 {
		writeJSONErr(w, http.StatusForbidden, "you cannot change users on that router")
		return
	}
	if err := s.auditDB.MarkCredLink(id, rid, credprof.StateRemoving, "", "", 0, 0); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the removal could not be queued")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "credprofile.unlink", TargetType: "credential-profile",
		TargetID: id, TargetName: p.Name,
		After: map[string]any{"router": rid, "username": p.Username},
	})
	s.wakeCredJob()
	writeJSON(w, map[string]any{"ok": true})
}

// credLinkRetry puts a link back in the queue by hand.
//
// This is the ONLY way out of a terminal state. `refused` and `conflict` are
// never retried on a timer, because the guard's answer does not change by being
// asked again and each retry writes another audit row. A person who has changed
// something on the router asks for one more go.
func (s *Server) credLinkRetry(w http.ResponseWriter, r *http.Request, sess *Session) {
	id, rid := r.PathValue("id"), r.PathValue("routerId")
	if _, err := s.auditDB.CredProfileByID(id); err != nil {
		writeJSONErr(w, http.StatusNotFound, "no such profile")
		return
	}
	if len(s.credRoutersFor(sess, []string{rid})) == 0 {
		writeJSONErr(w, http.StatusForbidden, "you cannot change users on that router")
		return
	}
	if err := s.auditDB.MarkCredLink(id, rid, credprof.StatePending, "", "", 0, 0); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the retry could not be queued")
		return
	}
	s.wakeCredJob()
	writeJSON(w, map[string]any{"ok": true})
}

// credRoutersFor filters ids to the routers this caller may write users on.
//
// A router the caller cannot see is DROPPED rather than refused, so the answer
// never confirms that a guessed id exists — issue #108's rule, the same one
// `fleetTargets` follows.
func (s *Server) credRoutersFor(sess *Session, ids []string) []string {
	known := map[string]bool{}
	all, _ := s.store.Routers()
	for _, rt := range all {
		if !rt.Disabled {
			known[rt.ID] = true
		}
	}
	uid := s.userIDFor(sess.Username)
	out, seen := []string{}, map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] || !known[id] {
			continue
		}
		seen[id] = true
		// THE PAGE IS THE PERMISSION: `users` / `write` is what lets somebody
		// create a RouterOS user on this router from the Router Users page, and
		// a profile does exactly that.
		if !permitted(s.rbac.CanPage(uid, "users", "write", id)) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// openCredSecret unseals a stored password for the applier.
//
// "" means it could not be read, which every caller treats as a failure rather
// than as an empty password: applying a profile with no password is the
// passwordless account `resource.RequiredOnCreate` exists to refuse.
func (s *Server) openCredSecret(stored string) string {
	if stored == "" {
		return ""
	}
	return s.openChannelConfig(stored)
}

// credSpecFor builds an applier spec from a stored profile, unsealing as it
// goes. It refuses rather than returning a spec with an empty password.
func (s *Server) credSpecFor(p db.CredProfile) (credprof.Spec, error) {
	plain := s.openCredSecret(p.Secret)
	if plain == "" {
		return credprof.Spec{}, errors.New("the profile's password could not be read")
	}
	return credprof.Spec{
		ID: p.ID, Name: p.Name, Username: p.Username,
		PermKind: p.PermKind, Builtin: p.Builtin, Group: p.GroupName,
		Policies: credprof.ParsePolicies(p.PolicyJSON),
		Password: plain, Revision: p.Revision,
	}, nil
}

// credNewID mints an opaque profile id. Hex, like ssoNewID, so it is safe
// anywhere an id appears — including inside the ownership marker written into a
// RouterOS comment.
func credNewID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("no entropy for a profile id")
	}
	return hex.EncodeToString(b), nil
}

// credBackoff is how long to wait before trying a failed link again.
//
// Thirty seconds doubling to a fifteen-minute ceiling: long enough that a
// router which is simply switched off does not produce a log line a minute,
// short enough that a transient failure clears without anybody pressing
// anything.
func credBackoff(attempts int64) time.Duration {
	d := 30 * time.Second
	for i := int64(0); i < attempts && d < 15*time.Minute; i++ {
		d *= 2
	}
	if d > 15*time.Minute {
		d = 15 * time.Minute
	}
	return d
}
