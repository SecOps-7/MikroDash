package server

// `POST /api/roles`, `PUT /api/roles/:id`, `DELETE /api/roles/:id`.
//
// ── THE BUILT-IN ROLE IS REFUSED, AND THE REASON IS STRUCTURAL ──────────────
//
// The live comment, on the edit route:
//
//	Administrator's reach is structural. Letting it be edited would either do
//	nothing (it has no page rows) or silently narrow every admin in the fleet.
//
// Both halves matter. `globalAdminQuery` counts `builtin = 1` roles and nothing
// else — it never looks at `role_pages` — so an administrator's access does not
// come from a page matrix and editing that matrix changes nothing. But the
// PAGE-level checks do read the matrix, so writing rows onto the built-in role
// would start narrowing what an administrator can see, everywhere at once, with
// no way to tell from the editor that it had happened.
//
// Deleting it is refused for the blunter reason: `globalAdminQuery` would then
// match no role at all and the install would have no administrators.
//
// ── DELETE COUNTS THE GRANTS RATHER THAN SURFACING A CONSTRAINT ─────────────
//
// The live comment: "The foreign key would refuse this anyway; saying how many
// grants block it is more useful than surfacing a constraint error." The count
// is in the message and is singular-aware, so it is reproduced exactly rather
// than approximated — `internal/db/rolewrite.go` leans on `ON DELETE RESTRICT`
// instead of re-checking, which is what makes the pre-check a message rather
// than a guard.
//
// ── A ROLE EDIT CHANGES THE ANSWER FOR EVERY PRINCIPAL HOLDING IT ───────────
//
// The live note on the bump: "the easiest bump to forget, and silent when
// missed". A grant edit affects one principal; a role edit affects everybody who
// holds that role at any scope, and nothing on screen says so.

import (
	"fmt"
	"log"
	"net/http"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/principals"
	"mikrodash/internal/safe"
)

func (s *Server) registerRolesWrite(mux *http.ServeMux) {
	mux.HandleFunc("POST "+principalsPrefix+"/roles", s.principalsGuard(s.roleCreate))
	mux.HandleFunc("PUT "+principalsPrefix+"/roles/{id}", s.principalsGuard(s.roleUpdate))
	mux.HandleFunc("DELETE "+principalsPrefix+"/roles/{id}", s.principalsGuard(s.roleDelete))
}

// knownPages is the registry `ParseRolePages` validates against — `Pages.BY_KEY`
// on the live side, built once from the same table the read endpoint publishes.
var knownPages = func() map[string]bool {
	out := make(map[string]bool, len(pageCatalogue))
	for _, p := range pageCatalogue {
		out[p.Key] = true
	}
	return out
}()

func (s *Server) roleCreate(w http.ResponseWriter, r *http.Request, sess *Session) {
	body, err := readBodyMap(r)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	fields, perr := principals.ParseName(body, false)
	if perr != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(perr.Error()))
		return
	}
	// BOTH validators run BEFORE the write, in the live order. A port that
	// created the role and then rejected its pages would leave a role behind a
	// 400 — and the operator would retry, hitting a duplicate-name 409 for a
	// role they cannot see having made.
	pages, perr := principals.ParseRolePages(body, knownPages)
	if perr != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(perr.Error()))
		return
	}

	role, err := s.auditDB.CreateRole(fields.Columns())
	if err != nil {
		if isUniqueViolation(err) {
			writeJSONErr(w, http.StatusConflict, "A role with that name already exists")
			return
		}
		log.Printf("[roles] create failed: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not create the role")
		return
	}

	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "role.create", TargetType: "role", TargetID: role.ID, TargetName: role.Name,
	})

	if pages.Submitted {
		if _, err := s.auditDB.SetRolePages(role.ID, toDBPages(pages.Pages)); err != nil {
			log.Printf("[roles] setting pages of %s: %v", role.ID, err)
		}
	}

	s.bumpPermissions()
	s.writeRoleView(w, *role)
}

func (s *Server) roleUpdate(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	existing, err := s.auditDB.GetRole(id)
	if err != nil {
		log.Printf("[roles] read %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the role")
		return
	}
	if existing == nil {
		writeJSONErr(w, http.StatusNotFound, "No such role")
		return
	}
	// BEFORE the body is even parsed, as the live route has it: a malformed edit
	// of the Administrator role reports the reason it could never have worked,
	// not a validation error that suggests fixing the body would help.
	if existing.Builtin {
		writeJSONErr(w, http.StatusBadRequest, "The Administrator role cannot be edited")
		return
	}

	body, err := readBodyMap(r)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	fields, perr := principals.ParseName(body, true)
	if perr != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(perr.Error()))
		return
	}
	pages, perr := principals.ParseRolePages(body, knownPages)
	if perr != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(perr.Error()))
		return
	}

	beforePages, err := s.auditDB.RolePages(id)
	if err != nil {
		log.Printf("[roles] pages of %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the role")
		return
	}

	role, err := s.auditDB.UpdateRole(id, fields.Columns())
	if err != nil {
		if isUniqueViolation(err) {
			writeJSONErr(w, http.StatusConflict, "A role with that name already exists")
			return
		}
		log.Printf("[roles] update failed: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not update the role")
		return
	}

	name := id
	if role != nil {
		name = role.Name
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "role.update", TargetType: "role", TargetID: id, TargetName: name,
		Before: map[string]any{"name": existing.Name, "pages": beforePages},
		After:  map[string]any{"name": fields.Columns()["name"], "pages": pages.Pages},
		Note:   "a role edit changes the answer for every principal holding it",
	})

	if pages.Submitted {
		if _, err := s.auditDB.SetRolePages(id, toDBPages(pages.Pages)); err != nil {
			log.Printf("[roles] setting pages of %s: %v", id, err)
		}
	}

	// See the file header: this is the easiest bump to forget, because a role
	// edit changes nothing about the principals themselves.
	s.bumpPermissions()
	if role == nil {
		// `updateRole` answers null when the patch touched no column, which is
		// an ordinary "nothing to change" rather than a failure — but the view
		// still has to be built from something.
		role = existing
	}
	s.writeRoleView(w, *role)
}

func (s *Server) roleDelete(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	existing, err := s.auditDB.GetRole(id)
	if err != nil {
		log.Printf("[roles] read %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the role")
		return
	}
	if existing == nil {
		writeJSONErr(w, http.StatusNotFound, "No such role")
		return
	}
	if existing.Builtin {
		writeJSONErr(w, http.StatusBadRequest, "The Administrator role cannot be deleted")
		return
	}

	// THE COUNT, not the constraint. See the file header.
	used, err := s.auditDB.CountGrantsForRole(id)
	if err != nil {
		log.Printf("[roles] grant count for %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not count grants")
		return
	}
	if used > 0 {
		writeJSONErr(w, http.StatusConflict, fmt.Sprintf(
			"That role is still assigned by %d grant%s", used, plural(used)))
		return
	}

	if _, err := s.auditDB.DeleteRole(id); err != nil {
		log.Printf("[roles] delete failed: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not delete the role")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "role.delete", TargetType: "role", TargetID: id, TargetName: existing.Name,
	})
	s.bumpPermissions()
	writeJSON(w, map[string]any{"ok": true})
}

// plural is the live `${used === 1 ? ” : 's'}`. Its own function because the
// message is compared exactly by the gate, and an off-by-one here reads as a
// typo rather than as a bug.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// toDBPages crosses the boundary between the pure validator's type and the
// writer's. Two types on purpose: `internal/principals` does no I/O and
// `internal/db` does nothing else, so neither imports the other.
func toDBPages(in []principals.RolePage) []db.RolePage {
	out := make([]db.RolePage, 0, len(in))
	for _, p := range in {
		out = append(out, db.RolePage{Page: p.Page, Access: p.Access})
	}
	return out
}

// writeRoleView answers with `_roleView(role)`, which is what the role editor
// re-renders from — including the `grants` count and the pages as they now
// stand, rather than as they were sent.
func (s *Server) writeRoleView(w http.ResponseWriter, role db.RoleRow) {
	view, err := s.roleView(role)
	if err != nil {
		log.Printf("[roles] view of %s: %v", role.ID, err)
		// The WRITE SUCCEEDED. Answering 500 here would tell the operator their
		// change failed and invite them to repeat it.
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "role": view})
}
