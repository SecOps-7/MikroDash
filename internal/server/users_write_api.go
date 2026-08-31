package server

// `POST /api/users`, `PUT /api/users/:id`, `DELETE /api/users/:id`.
//
// The administration side of accounts: one person acting on another. The
// SELF-SERVICE side is `account_api.go`, and the live app keeps them apart
// deliberately — the live comment on the account block says copying
// `requireGlobalAdmin` down there "would lock the feature to the one audience
// that does not need it, which is the most likely way to get this wrong".
//
// ── THE ORPHAN CHECK IS THE WHOLE OF THE DIFFICULTY ─────────────────────────
//
// Two of these three routes can leave an install with nobody able to administer
// it, and the naive check does not work. The live comment, which is worth having
// in front of you while reading `userDelete`:
//
//	Asked of the GRANTS, not of Users.adminCount() (issue #108). That counted
//	user records carrying role === 'admin', a field nothing has decided anything
//	with since roles became rows: it cannot see an administrator whose grant is
//	held through a group, and it counts one who was demoted in the editor. The
//	probe below runs the deletion in a transaction, checks whether any global
//	administrator survives, and always rolls back.
//
//	The user record lives in JSON and cannot join that transaction, so the grant
//	deletion is what gets probed — which is exactly what globalAdminUserIds()
//	reads.
//
// `db.WouldOrphanGlobalAdmin` is that probe, and `DeleteGrantsForPrincipalTx` is
// the seam it needs.
//
// ── THE LEGACY PROJECTION IS CONDITIONAL, AND THAT IS NOT A DETAIL ──────────
//
// `syncUserGrants` DELETES every grant the principal holds and rebuilds them
// from `role` + `allowedRouterIds`. Running it unconditionally would mean
// RENAMING A USER SILENTLY DESTROYED every grant an administrator had built in
// the editor. So it runs only when the request actually carried one of those two
// legacy fields — which is why this file cares about the difference between "the
// key was absent" and "the key was sent as null", and why the body is decoded
// into a map rather than a struct.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/rbac"
	"mikrodash/internal/store"
)

// `usernameRe` (setup_api.go) and `minPasswordLen` (account_api.go) are SHARED
// with the routes that already had them, deliberately. Both come from the same
// live constants — `_USERNAME_RE` at index.js:1507 and the `length < 4` check —
// and a second copy of either is a second place for them to drift. `setup_api.go`
// already records why the regex is anchored: "an unanchored copy accepts anything
// CONTAINING a valid run, so 'bad name' would pass."

func (s *Server) registerUsersWrite(mux *http.ServeMux) {
	mux.HandleFunc("POST "+principalsPrefix+"/users", s.principalsGuard(s.userCreate))
	mux.HandleFunc("PUT "+principalsPrefix+"/users/{id}", s.principalsGuard(s.userUpdate))
	mux.HandleFunc("DELETE "+principalsPrefix+"/users/{id}", s.principalsGuard(s.userDelete))
}

// readBodyMap decodes a request body into a map, so PRESENCE can be asked.
//
// A struct cannot answer it: `{"role": null}` and `{}` both decode to the zero
// value, and the live rules turn on the difference — an explicit null role is a
// 400 and an absent one is a no-op. The same distinction decides whether the
// legacy grant projection runs at all.
func readBodyMap(r *http.Request) (map[string]any, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		// `req.body || {}` — an empty body is an empty patch, not an error.
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		// A literal `null` body. Same answer as an empty one.
		return map[string]any{}, nil
	}
	return m, nil
}

func (s *Server) userCreate(w http.ResponseWriter, r *http.Request, sess *Session) {
	body, err := readBodyMap(r)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	username, _ := body["username"].(string)
	if username == "" || !usernameRe.MatchString(username) {
		writeJSONErr(w, http.StatusBadRequest, "Invalid username")
		return
	}
	// `if (!password || String(password).length < 4)` — FALSY first, then the
	// length. Both halves matter, and the first is the one a port gets wrong:
	// `bodyString(nil)` is the four characters "null", which is long enough, so
	// a create with NO PASSWORD AT ALL sailed through on the first run and made
	// an account whose password is the literal string "null".
	//
	// The stringify in the second half is real too — a NUMERIC password is a
	// password of its digits rather than a rejection.
	// ONE EQUIVALENT MUTANT LIVES HERE, measured 2026-08-28: dropping the
	// `password == ""` clause below survives, because the extraction that
	// follows leaves an absent or null password as "" and `len("") < 4` refuses
	// it anyway. It stays because the LIVE check has both halves and this is a
	// port — and because the redundancy is only true while the extraction stays
	// presence-aware. Collapsing them would make the guard depend on a property
	// of the code above it rather than on the rule.
	password := ""
	if v, present := body["password"]; present && v != nil {
		if str, ok := v.(string); ok {
			password = str
		} else {
			password = bodyString(v)
		}
	}
	if password == "" || len(password) < minPasswordLen {
		writeJSONErr(w, http.StatusBadRequest, "Password too short")
		return
	}
	// ── TRUTHY-GUARDED, AND THAT IS NOT THE SAME AS THE UPDATE ROUTE ────
	//
	//	POST:  if (role && !Users.ROLES.includes(role))       -- truthy
	//	PUT:   if (updates.role !== undefined && !includes)   -- presence
	//
	// So `{"role": null}` is REFUSED on an update and ACCEPTED on a create,
	// where it falls through to the default. The two routes are forty lines
	// apart in index.js and use different tests, and reproducing only one of
	// them is the obvious mistake — a port using `bodyString(nil)` here gets
	// "null", which is not in the role list, and refuses a create the live app
	// allows. That is exactly what happened on the first run.
	//
	// The empty string is falsy too, so `"role": ""` also defaults.
	roleStr := ""
	if v, present := body["role"]; present {
		if str, ok := v.(string); ok {
			roleStr = str
		} else if v != nil {
			// A number or an object is truthy and is not in the list, so it is
			// refused — unlike null, which is falsy and is not.
			roleStr = bodyString(v)
		}
	}
	if roleStr != "" && !validRoleName(roleStr) {
		writeJSONErr(w, http.StatusBadRequest, "Invalid role")
		return
	}
	if roleStr == "" {
		roleStr = "viewer"
	}

	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "user store unavailable")
		return
	}
	taken, err := s.usernameTaken(username)
	if err != nil {
		log.Printf("[users] read: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the user list")
		return
	}
	if taken {
		writeJSONErr(w, http.StatusConflict, "Username already exists")
		return
	}

	ids, idsSent := stringListFrom(body["allowedRouterIds"])
	user, err := s.store.CreateUser(store.NewUser{
		Username: username, Password: password, Role: roleStr, AllowedRouterIDs: ids,
	})
	if err != nil {
		var bad *store.ErrInvalidRole
		if errors.As(err, &bad) {
			writeJSONErr(w, http.StatusBadRequest, "Invalid role")
			return
		}
		log.Printf("[users] create: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not create the user")
		return
	}

	// ── THE PROJECTION, ONLY WHEN THE CALLER SENT THE LEGACY FIELDS ─────
	//
	// The live comment: "The Users card grants access through /api/grants now
	// (#108), so a new user starts with none and is granted explicitly —
	// projecting a default 'viewer' here would hand every new account read of
	// every router."
	//
	// PRESENCE, not truthiness: `role !== undefined || allowedRouterIds !==
	// undefined`. A caller sending `"role": null` HAS sent the field.
	_, roleKeyPresent := body["role"]
	_, idsKeyPresent := body["allowedRouterIds"]
	_ = idsSent
	if roleKeyPresent || idsKeyPresent {
		uid, _ := user["id"].(string)
		uname, _ := user["username"].(string)
		if err := s.syncUserGrants(uid, uname, roleStr, ids); err != nil {
			log.Printf("[users] grant projection for %s: %v", uid, err)
		}
	}

	uid, _ := user["id"].(string)
	uname, _ := user["username"].(string)
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "user.create", TargetType: "user", TargetID: uid, TargetName: uname,
	})
	writeJSON(w, map[string]any{"ok": true, "user": user})
}

func (s *Server) userUpdate(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	body, err := readBodyMap(r)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "user store unavailable")
		return
	}

	// PRESENCE-guarded, matching `updates.username !== undefined`: an edit that
	// does not mention the username leaves it alone.
	//
	// ── AND THE TYPE IS CHECKED BEFORE THE PATTERN ────────────────────────
	//
	// `bodyString(nil)` is the string "null", which MATCHES `usernameRe` — so a
	// presence check plus a regex renamed the account to those four characters,
	// and `{"username": 42}` to "42". Nobody is locked out, which is what made it
	// quiet: the row is simply wrong, and `alert_events.acknowledged_by` stores a
	// username as raw text, so every later acknowledgement was attributed to
	// `null`.
	//
	// The port reproduced that deliberately and reported it; upstream fixed it in
	// `f5416c2` with `typeof updates.username !== 'string'` before the pattern,
	// and this follows. The comment here previously claimed the null case was
	// already a 400 while the test asserted 200 — the test was right.
	if v, present := body["username"]; present {
		name, isString := v.(string)
		if !isString || !usernameRe.MatchString(name) {
			writeJSONErr(w, http.StatusBadRequest, "Invalid username")
			return
		}
	}
	if v, present := body["role"]; present {
		if !validRoleName(bodyString(v)) {
			writeJSONErr(w, http.StatusBadRequest, "Invalid role")
			return
		}
	}

	before := s.publicUserByID(id)

	up := store.UserUpdates{}
	if v, present := body["username"]; present {
		str := bodyString(v)
		up.Username = &str
	}
	if v, present := body["role"]; present {
		str := bodyString(v)
		up.Role = &str
	}
	// ARRAY-GUARDED, unlike the two above — `Array.isArray(...)`. A string, a
	// number or null is IGNORED rather than stored or cleared.
	ids, idsSent := stringListFrom(body["allowedRouterIds"])
	if idsSent {
		up.AllowedRouterIDs = &ids
	}
	if v, present := body["password"]; present {
		str := bodyString(v)
		up.Password = &str
	}

	updated, err := s.store.UpdateUser(id, up)
	if err != nil {
		if errors.Is(err, store.ErrNoSuchUser) {
			writeJSONErr(w, http.StatusNotFound, "User not found")
			return
		}
		var bad *store.ErrInvalidRole
		if errors.As(err, &bad) {
			writeJSONErr(w, http.StatusBadRequest, "Invalid role")
			return
		}
		log.Printf("[users] update %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not update the user")
		return
	}

	uname, _ := updated["username"].(string)
	if uname == "" {
		uname = id
	}
	// `updates` may carry a password; the recorder redacts by field name, as
	// `audit.js` does.
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "user.update", TargetType: "user", TargetID: id, TargetName: uname,
		Before: before, After: body,
	})

	// ── THE PROJECTION, AND THE PROBE IN FRONT OF IT ────────────────────
	//
	// Only when the request carried a legacy field — see the file header:
	// `syncUserGrants` deletes every grant the principal holds and rebuilds
	// them, so running it on a rename would destroy an administrator's work.
	//
	// And demoting the last administrator THROUGH that field is still a way to
	// orphan administration, so the projection is probed before it runs.
	_, roleKeyPresent := body["role"]
	_, idsKeyPresent := body["allowedRouterIds"]
	if roleKeyPresent || idsKeyPresent {
		roleStr, _ := updated["role"].(string)
		storedIDs := ids
		if !idsSent {
			storedIDs, _ = stringListFrom(updated["allowedRouterIds"])
		}
		orphan, err := s.wouldOrphanBySync(id, uname, roleStr, storedIDs)
		if err != nil {
			log.Printf("[users] orphan probe for %s: %v", id, err)
			writeJSONErr(w, http.StatusInternalServerError, "could not check administrator access")
			return
		}
		if orphan {
			writeJSONErr(w, http.StatusBadRequest,
				"That would leave nobody with administrator access")
			return
		}
		if err := s.syncUserGrants(id, uname, roleStr, storedIDs); err != nil {
			log.Printf("[users] grant projection for %s: %v", id, err)
		}
	}

	writeJSON(w, map[string]any{"ok": true, "user": updated})
}

func (s *Server) userDelete(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "user store unavailable")
		return
	}

	// YOUR OWN ACCOUNT, FIRST. Before the orphan probe, as the live route has
	// it: deleting yourself is refused whether or not anybody else is an
	// administrator, and the message says which rule stopped you.
	if sess != nil && s.userIDFor(sess.Username) == id && id != "" {
		writeJSONErr(w, http.StatusBadRequest, "Cannot delete your own account")
		return
	}

	// THE PROBE. See the file header for why it asks the grants rather than the
	// user records.
	if s.auditDB != nil {
		orphan, err := s.auditDB.WouldOrphanGlobalAdmin(func(tx *sql.Tx) error {
			return db.DeleteGrantsForPrincipalTx(tx, "user", id)
		})
		if err != nil {
			log.Printf("[users] orphan probe for %s: %v", id, err)
			writeJSONErr(w, http.StatusInternalServerError, "could not check administrator access")
			return
		}
		if orphan {
			writeJSONErr(w, http.StatusBadRequest,
				"That would leave nobody with administrator access")
			return
		}
	}

	before := s.publicUserByID(id)
	deleted, err := s.store.DeleteUser(id)
	if err != nil {
		log.Printf("[users] delete %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not delete the user")
		return
	}
	if !deleted {
		writeJSONErr(w, http.StatusNotFound, "User not found")
		return
	}

	name := id
	if before != nil {
		if u, _ := before["username"].(string); u != "" {
			name = u
		}
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "user.delete", TargetType: "user", TargetID: id, TargetName: name,
		Note: "grants, layouts and notification config removed with the account",
	})

	// ── THE CASCADE THAT SQLITE CANNOT DO FOR US ────────────────────────
	//
	// Users live in JSON, so their grants and memberships have no foreign key to
	// cascade through. Every one of these three is a row that would otherwise
	// point at an id a later account could be issued — and the third is the one
	// that matters most: `user_notify_config` holds ENCRYPTED CHANNEL
	// CREDENTIALS, so a reused id would inherit somebody else's ntfy URL and
	// Telegram token.
	//
	// Logged rather than fatal, individually: the account is already gone, and a
	// failure to tidy one table must not stop the other two.
	if s.auditDB != nil {
		if _, err := s.auditDB.DeleteGrantsForPrincipal("user", id); err != nil {
			log.Printf("[users] clearing grants for %s: %v", id, err)
		}
		if _, err := s.auditDB.DeleteLayouts(id); err != nil {
			log.Printf("[users] clearing layouts for %s: %v", id, err)
		}
		if err := s.auditDB.RemoveUserNotifyConfig(id); err != nil {
			log.Printf("[users] clearing notification config for %s: %v", id, err)
		}
	}

	s.hub.BroadcastAll("perms:changed", map[string]any{})
	writeJSON(w, map[string]any{"ok": true})
}

// ---- helpers ---------------------------------------------------------------

// bodyString is `String(v)` for the shapes a JSON request body carries.
//
// NOT this package's `jsStringOf` (routers_api.go), which is `String(x || ”)`
// and maps BOTH nil and a non-string to "". The difference matters exactly here:
// an explicit `null` is a value the caller SENT, and `String(null)` is the four
// characters "null" — which fails the username regex and is not in the role
// list. Failing them is the live behaviour and the thing under test; mapping
// null to "" would make `{"role": null}` look like an absent role and silently
// skip the 400.
func bodyString(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// stringListFrom is `Array.isArray(x)` plus the element read.
//
// The second return is whether it WAS an array, which is the guard itself: a
// string, a number or null answers false and the caller leaves the field alone.
func stringListFrom(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out, true
}

func validRoleName(role string) bool {
	for _, r := range store.Roles {
		if r == role {
			return true
		}
	}
	return false
}

func (s *Server) usernameTaken(username string) (bool, error) {
	all, err := s.store.PublicUsers()
	if err != nil {
		return false, err
	}
	for _, u := range all {
		if name, _ := u["username"].(string); name == username {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) publicUserByID(id string) map[string]any {
	if s.store == nil {
		return nil
	}
	all, err := s.store.PublicUsers()
	if err != nil {
		return nil
	}
	for _, u := range all {
		if uid, _ := u["id"].(string); uid == id {
			return u
		}
	}
	return nil
}

// syncUserGrants applies `rbac.PlanUserGrants` — the ported decision — and
// writes what it plans.
//
// The DECISION is pure and pinned against the live implementation; this is only
// the part that writes, which is the same split `grantFirstAdmin` uses and the
// reason the decision could be tested at all.
func (s *Server) syncUserGrants(userID, username, role string, allowed []string) error {
	if s.auditDB == nil {
		return errors.New("server: no grant store")
	}
	live := map[string]bool{}
	if s.store != nil {
		all, _ := s.store.Routers()
		for _, rec := range all {
			live[rec.ID] = true
		}
	}
	if allowed == nil {
		allowed = []string{}
	}
	plan := rbac.PlanUserGrants(rbac.UserForGrants{
		ID: userID, Username: username, Role: role, AllowedRouterIDs: allowed,
	}, live)
	for _, warn := range plan.Warnings {
		log.Print(warn)
	}
	for _, step := range plan.Steps {
		switch step.Op {
		case "delete":
			if _, err := s.auditDB.DeleteGrantsForPrincipal("user", userID); err != nil {
				return err
			}
		case "upsert":
			if err := s.auditDB.UpsertGrant(db.GrantSpec{
				PrincipalType: "user", PrincipalID: userID,
				Role: step.Role, ScopeType: step.ScopeType, ScopeID: step.ScopeID,
				CreatedBy: username,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// wouldOrphanBySync asks what the projection WOULD do, without doing it.
//
// The live line is `Rbac.wouldOrphanGlobalAdmin(() => Rbac.syncUserGrants(updated))`:
// the same plan, run inside a transaction that is always rolled back. Demoting
// the last administrator through the legacy `role` field is a real way to orphan
// administration, and it is invisible to a check that only watches
// `DELETE /api/grants`.
func (s *Server) wouldOrphanBySync(userID, username, role string, allowed []string) (bool, error) {
	if s.auditDB == nil {
		return false, nil
	}
	live := map[string]bool{}
	if s.store != nil {
		all, _ := s.store.Routers()
		for _, rec := range all {
			live[rec.ID] = true
		}
	}
	if allowed == nil {
		allowed = []string{}
	}
	plan := rbac.PlanUserGrants(rbac.UserForGrants{
		ID: userID, Username: username, Role: role, AllowedRouterIDs: allowed,
	}, live)

	return s.auditDB.WouldOrphanGlobalAdmin(func(tx *sql.Tx) error {
		for _, step := range plan.Steps {
			switch step.Op {
			case "delete":
				if err := db.DeleteGrantsForPrincipalTx(tx, "user", userID); err != nil {
					return err
				}
			case "upsert":
				if err := db.UpsertGrantTx(tx, db.GrantSpec{
					PrincipalType: "user", PrincipalID: userID,
					Role: step.Role, ScopeType: step.ScopeType, ScopeID: step.ScopeID,
					CreatedBy: username,
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
