package server

// `POST /api/grants` and `DELETE /api/grants/:id`.
//
// ── FIVE CHECKS IN ORDER, AND THE ORDER IS PART OF THE ANSWER ───────────────
//
// The live route validates one thing at a time and each has its own status:
//
//	1. principal type   400  "Invalid principal type"
//	2. role             400  "Invalid role"
//	3. scope type       400  "Invalid scope type"
//	4. a scope id       400  "Scope id required"   (non-global only)
//	5. EXISTENCE        404  "No such site" / "No such router" / "No such group"
//
// The first four are 400 and the fifth is 404, which is the distinction worth
// preserving: the request was well-formed and named something that is not there.
//
// The live comment on that fifth group says why it exists at all:
//
//	Refuse a grant naming something that does not exist: it would sit in the
//	table forever, conferring nothing, and read as working in the UI.
//
// ── THE LEGACY `role` NAME IS STILL ACCEPTED ────────────────────────────────
//
//	const roleId = b.roleId || { admin: 'administrator', operator: 'operator',
//	                             viewer: 'readonly' }[b.role];
//
// `roleId` wins; `role` is the pre-roles-table spelling, kept "so an older
// client — or a scripted caller — keeps working until Phase 6". Neither
// recognised is a 400 rather than a fallback: `db.resolveRoleID` falls back to
// `readonly` for a WRITE, but this route refuses first, so a typo cannot quietly
// become least privilege.
//
// ── DELETE IS THE MOST DIRECT ORPHAN PATH THERE IS ─────────────────────────
//
// Removing the grant that confers administration is not a subtle way to lock
// everybody out; it is the obvious one. Probed like the rest, and the probe runs
// BEFORE the 404 — which is the live order and also the safer one: an id that
// does not exist deletes nothing and cannot orphan anybody, so the two orders
// differ only when it does exist.

import (
	"database/sql"
	"log"
	"net/http"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
)

func (s *Server) registerGrantsWrite(mux *http.ServeMux) {
	mux.HandleFunc("POST "+principalsPrefix+"/grants", s.principalsGuard(s.grantCreate))
	mux.HandleFunc("DELETE "+principalsPrefix+"/grants/{id}", s.principalsGuard(s.grantDelete))
}

// legacyRoleNames is the live inline map.
//
// Kept beside the route rather than exported from `internal/db`, which holds the
// same pairs for a DIFFERENT purpose: `resolveRoleID` falls back to `readonly`
// for anything it does not recognise, because a write must land somewhere. This
// route refuses instead. One shared table would make the fallback look like
// validation, and a typo would quietly become least privilege rather than a 400.
var legacyRoleNames = map[string]string{
	"admin":    "administrator",
	"operator": "operator",
	"viewer":   "readonly",
}

func (s *Server) grantCreate(w http.ResponseWriter, r *http.Request, sess *Session) {
	body, err := readBodyMap(r)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// 1. THE PRINCIPAL TYPE.
	principalType, _ := body["principalType"].(string)
	if principalType != "user" && principalType != "group" {
		writeJSONErr(w, http.StatusBadRequest, "Invalid principal type")
		return
	}
	// `String(b.principalId)` — the live route stringifies, so a numeric id is
	// its digits rather than a rejection.
	principalID := ""
	if v, ok := body["principalId"]; ok {
		principalID = bodyString(v)
	}

	// 2. THE ROLE. `roleId` wins, then the legacy name, then refuse.
	roleID, _ := body["roleId"].(string)
	if roleID == "" {
		if name, ok := body["role"].(string); ok {
			roleID = legacyRoleNames[name]
		}
	}
	if roleID == "" {
		writeJSONErr(w, http.StatusBadRequest, "Invalid role")
		return
	}
	role, err := s.auditDB.GetRole(roleID)
	if err != nil {
		log.Printf("[grants] read role %s: %v", roleID, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the role")
		return
	}
	// A ROLE ID THAT NAMES NOTHING IS A 400, not a 404 — it is a bad field in the
	// request, where a missing site or router is a real thing that is absent.
	// That is the live split and it is not obvious from the outside.
	if role == nil {
		writeJSONErr(w, http.StatusBadRequest, "Invalid role")
		return
	}

	// 3. THE SCOPE TYPE.
	scopeType, _ := body["scopeType"].(string)
	if scopeType != "global" && scopeType != "site" && scopeType != "router" {
		writeJSONErr(w, http.StatusBadRequest, "Invalid scope type")
		return
	}

	// 4. THE SCOPE ID. A global grant stores the EMPTY STRING — never null and
	// never a leftover id. `db.scopeIDFor` enforces the same rule on the write
	// side and `grantwrite.go` records why: SQLite treats NULLs as distinct in a
	// UNIQUE index, so a NULL would let one principal hold two global grants and
	// the constraint would silently never fire.
	scopeID := ""
	if scopeType != "global" {
		if v, ok := body["scopeId"]; ok && v != nil {
			scopeID = bodyString(v)
		}
		if scopeID == "" {
			writeJSONErr(w, http.StatusBadRequest, "Scope id required")
			return
		}
	}

	// 5. EXISTENCE — 404, not 400. See the file header.
	switch scopeType {
	case "site":
		site, err := s.auditDB.GetSite(scopeID)
		if err != nil {
			log.Printf("[grants] read site %s: %v", scopeID, err)
			writeJSONErr(w, http.StatusInternalServerError, "could not read the site")
			return
		}
		if site == nil {
			writeJSONErr(w, http.StatusNotFound, "No such site")
			return
		}
	case "router":
		// `Routers.getById(scopeId)` — and the port already had this exact
		// question answered, in `reports_run.go`, for whether a schedule's router
		// is still configured. Reused rather than written again: it reads the
		// STORE, which is the part that matters. Asking the GRANT table whether a
		// router exists would answer "yes" for one that was deleted and left a
		// grant behind — which is the state this check exists to stop anybody
		// creating.
		if _, ok := s.routerExists(scopeID); !ok {
			writeJSONErr(w, http.StatusNotFound, "No such router")
			return
		}
	}
	if principalType == "group" {
		group, err := s.auditDB.GetGroup(principalID)
		if err != nil {
			log.Printf("[grants] read group %s: %v", principalID, err)
			writeJSONErr(w, http.StatusInternalServerError, "could not read the group")
			return
		}
		if group == nil {
			writeJSONErr(w, http.StatusNotFound, "No such group")
			return
		}
	}

	// `createdBy` is the SESSION'S USER ID, not the username.
	// The identity audit records that this column takes the id where
	// `audit_events.actor_name` takes the name, and that reaching for the wrong
	// one is invisible to any test — a round trip through one implementation
	// agrees with itself whatever it wrote. Two such bugs were found on
	// 2026-08-27 by reading the real table.
	createdBy := ""
	if sess != nil {
		createdBy = s.userIDFor(sess.Username)
	}
	if err := s.auditDB.UpsertGrant(db.GrantSpec{
		PrincipalType: principalType, PrincipalID: principalID,
		RoleID: roleID, ScopeType: scopeType, ScopeID: scopeID,
		CreatedBy: createdBy,
	}); err != nil {
		log.Printf("[grants] create failed: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not create the grant")
		return
	}

	// READ BACK, because `UpsertGrant` answers only an error and the live route
	// returns the row. Filtered to this principal and matched on scope, which the
	// unique index guarantees is at most one.
	grant := s.findGrant(principalType, principalID, scopeType, scopeID)

	s.bumpPermissions()

	targetID := ""
	if grant != nil {
		targetID = grant.ID
	}
	scopeSuffix := ""
	if scopeID != "" {
		scopeSuffix = ":" + scopeID
	}
	ev := audit.Event{
		Action: "grant.create", TargetType: "grant", TargetID: targetID,
		TargetName: principalType + ":" + principalID + " → " + roleID +
			" @ " + scopeType + scopeSuffix,
	}
	// ROUTER-SCOPED GRANTS ARE RECORDED AGAINST THAT ROUTER, "so whoever
	// administers it can see access to it being handed out".
	if scopeType == "router" {
		ev.RouterID = scopeID
	}
	s.httpRecorder(r, sess).Record(ev)

	writeJSON(w, map[string]any{"ok": true, "grant": grant})
}

func (s *Server) grantDelete(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")

	// THE PROBE FIRST, before the 404 — see the file header.
	orphan, err := s.auditDB.WouldOrphanGlobalAdmin(func(tx *sql.Tx) error {
		return db.DeleteGrantTx(tx, id)
	})
	if err != nil {
		log.Printf("[grants] orphan probe for %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not check administrator access")
		return
	}
	if orphan {
		writeJSONErr(w, http.StatusBadRequest,
			"That would leave nobody with administrator access")
		return
	}

	deleted, err := s.auditDB.DeleteGrant(id)
	if err != nil {
		log.Printf("[grants] delete failed: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not delete the grant")
		return
	}
	if !deleted {
		writeJSONErr(w, http.StatusNotFound, "No such grant")
		return
	}

	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "grant.delete", TargetType: "grant", TargetID: id,
	})
	s.bumpPermissions()
	writeJSON(w, map[string]any{"ok": true})
}

// findGrant reads back the row that was just written.
//
// At most one can match: the unique index is on exactly
// `(principal_type, principal_id, scope_type, scope_id)`, which is also what
// `UpsertGrant` conflicts on.
func (s *Server) findGrant(principalType, principalID, scopeType, scopeID string) *db.GrantRow {
	rows, err := s.auditDB.ListGrants(db.GrantFilter{
		PrincipalType: principalType, PrincipalID: principalID,
	})
	if err != nil {
		log.Printf("[grants] read back: %v", err)
		return nil
	}
	for i := range rows {
		if rows[i].ScopeType != scopeType {
			continue
		}
		// A GLOBAL grant's `scope_id` is the empty string, and the column is a
		// POINTER here because `db.GrantRow` models it as one — see its comment.
		stored := ""
		if rows[i].ScopeID != nil {
			stored = *rows[i].ScopeID
		}
		if stored == scopeID {
			return &rows[i]
		}
	}
	return nil
}
