package server

// `POST /api/groups`, `PUT /api/groups/:id`, `DELETE /api/groups/:id`.
//
// ── A GROUP IS A WAY TO ORPHAN THE LAST ADMINISTRATOR, TWICE ────────────────
//
// The live comment on the update route names the less obvious of them:
//
//	Emptying the group that holds the only global admin grant is one of the
//	five ways to orphan the last administrator, and the least obvious.
//
// Nobody's account is touched and nobody is deleted — the membership list is
// simply replaced with one that no longer contains them, and a global admin
// grant held THROUGH the group stops conferring. Deleting the group does the
// same thing more bluntly, because `DeleteGroup` takes the group's grants with
// it.
//
// Both are probed with `WouldOrphanGlobalAdmin`, which runs the mutation in a
// transaction and always rolls back. `db.SetGroupMembersTx` and
// `db.DeleteGroupTx` are the seams that makes checkable.
//
// ── THE ORDER IN THE UPDATE ROUTE IS LOAD-BEARING ───────────────────────────
//
// The probe runs BEFORE `updateGroup`, and the membership is written AFTER it.
// So a refused membership change also blocks the NAME change in the same
// request — the whole request is refused, not the half of it that was dangerous.
// A port that wrote the name first would leave a partial edit behind a 400.
//
// ── A DUPLICATE NAME IS 409, AND IT IS DETECTED BY THE ERROR ────────────────
//
// The live routes catch the exception and match `/UNIQUE constraint failed/`.
// There is no pre-check, deliberately: a read-then-write would race, and the
// unique index is the thing that actually holds. Reproduced here by matching the
// driver's error text, which is the same shape of dependency.

import (
	"database/sql"
	"log"
	"net/http"
	"strings"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/principals"
	"mikrodash/internal/safe"
)

func (s *Server) registerGroupsWrite(mux *http.ServeMux) {
	mux.HandleFunc("POST "+principalsPrefix+"/groups", s.principalsGuard(s.groupCreate))
	mux.HandleFunc("PUT "+principalsPrefix+"/groups/{id}", s.principalsGuard(s.groupUpdate))
	mux.HandleFunc("DELETE "+principalsPrefix+"/groups/{id}", s.principalsGuard(s.groupDelete))
}

// isUniqueViolation is the live `/UNIQUE constraint failed/.test(e.message)`.
//
// TEXT-MATCHED, like the original. `modernc.org/sqlite` reports it as
// "constraint failed: UNIQUE constraint failed: groups.name", so the live
// substring is present verbatim — checked rather than assumed, because a driver
// that phrased it differently would turn every duplicate name into a 500.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// memberListFrom is `Array.isArray(req.body.memberUserIds)`.
//
// ARRAY-GUARDED, not presence-guarded, so a caller sending a string or null
// leaves the membership alone rather than emptying it. Same rule and same reason
// as `allowedRouterIds` on the user routes.
func memberListFrom(body map[string]any) ([]string, bool) {
	return stringListFrom(body["memberUserIds"])
}

func (s *Server) groupCreate(w http.ResponseWriter, r *http.Request, sess *Session) {
	body, err := readBodyMap(r)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	fields, perr := principals.ParseName(body, false)
	if perr != nil {
		// `safe.Message` even though every message this validator produces is a
		// fixed string today — the same reasoning `sites_api.go` records, and
		// `TestNoRawErrorReachesAnHttpBody` is what enforces it: "the day one of
		// those messages starts quoting the value it rejected is the day a path
		// or an address reaches the browser, and nobody would think to revisit
		// here."
		writeJSONErr(w, http.StatusBadRequest, safe.Message(perr.Error()))
		return
	}

	group, err := s.auditDB.CreateGroup(fields.Columns())
	if err != nil {
		if isUniqueViolation(err) {
			writeJSONErr(w, http.StatusConflict, "A group with that name already exists")
			return
		}
		log.Printf("[groups] create failed: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not create the group")
		return
	}

	// RECORDED BEFORE THE MEMBERSHIP IS WRITTEN, as the live route has it. The
	// group exists at this point; if the membership write then fails, the trail
	// still says who created it.
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "group.create", TargetType: "group", TargetID: group.ID, TargetName: group.Name,
	})

	if members, sent := memberListFrom(body); sent {
		if _, err := s.auditDB.SetGroupMembers(group.ID, members); err != nil {
			log.Printf("[groups] setting members of %s: %v", group.ID, err)
		}
	}

	s.bumpPermissions()
	writeJSON(w, map[string]any{"ok": true, "group": group})
}

func (s *Server) groupUpdate(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	before, err := s.auditDB.GetGroup(id)
	if err != nil {
		log.Printf("[groups] read %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the group")
		return
	}
	if before == nil {
		writeJSONErr(w, http.StatusNotFound, "No such group")
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

	// ── THE PROBE, BEFORE ANYTHING IS WRITTEN ───────────────────────────
	//
	// See the file header: emptying the group that holds the only global admin
	// grant orphans administration without touching a single account. Refusing
	// here means the NAME change in the same request is refused too, which is
	// the live behaviour and the right one — a partial edit behind a 400 is
	// worse than no edit.
	members, membersSent := memberListFrom(body)
	if membersSent {
		orphan, err := s.auditDB.WouldOrphanGlobalAdmin(func(tx *sql.Tx) error {
			return db.SetGroupMembersTx(tx, id, members)
		})
		if err != nil {
			log.Printf("[groups] orphan probe for %s: %v", id, err)
			writeJSONErr(w, http.StatusInternalServerError, "could not check administrator access")
			return
		}
		if orphan {
			writeJSONErr(w, http.StatusBadRequest,
				"That would leave nobody with administrator access")
			return
		}
	}

	group, err := s.auditDB.UpdateGroup(id, fields.Columns())
	if err != nil {
		if isUniqueViolation(err) {
			writeJSONErr(w, http.StatusConflict, "A group with that name already exists")
			return
		}
		log.Printf("[groups] update failed: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not update the group")
		return
	}

	name := id
	if group != nil {
		name = group.Name
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "group.update", TargetType: "group", TargetID: id, TargetName: name,
		Before: map[string]any{"name": before.Name, "description": before.Description},
		After:  fields.Columns(),
	})

	if membersSent {
		if _, err := s.auditDB.SetGroupMembers(id, members); err != nil {
			log.Printf("[groups] setting members of %s: %v", id, err)
		}
	}

	s.bumpPermissions()
	writeJSON(w, map[string]any{"ok": true, "group": group})
}

func (s *Server) groupDelete(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	before, err := s.auditDB.GetGroup(id)
	if err != nil {
		log.Printf("[groups] read %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the group")
		return
	}
	if before == nil {
		writeJSONErr(w, http.StatusNotFound, "No such group")
		return
	}

	// DELETING A GROUP TAKES ITS GRANTS WITH IT, so a global admin grant held
	// through this group stops conferring. Probed, and the probe rolls back.
	orphan, err := s.auditDB.WouldOrphanGlobalAdmin(func(tx *sql.Tx) error {
		return db.DeleteGroupTx(tx, id)
	})
	if err != nil {
		log.Printf("[groups] orphan probe for %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not check administrator access")
		return
	}
	if orphan {
		writeJSONErr(w, http.StatusBadRequest,
			"That would leave nobody with administrator access")
		return
	}

	if _, err := s.auditDB.DeleteGroup(id); err != nil {
		log.Printf("[groups] delete failed: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not delete the group")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "group.delete", TargetType: "group", TargetID: id, TargetName: before.Name,
	})
	s.bumpPermissions()
	writeJSON(w, map[string]any{"ok": true})
}

// bumpPermissions is `Rbac.bump(); _broadcastPermsChanged();`.
//
// ── THE EASIEST THING TO FORGET, AND SILENT WHEN MISSED ─────────────────────
//
// Every principal write changes what somebody may do, and every open browser is
// holding a resolved answer. Without the broadcast the UI keeps showing the old
// one until a reload — a revoked grant looks like it did not take, and a granted
// one looks like it was refused.
//
// The live side ALSO bumps a generation counter that invalidates its memoised
// views. This port's `rbac` package reads per query rather than memoising (see
// the resolver's comment on reading the router list per question), so there is
// nothing here to invalidate — the broadcast is the whole of it. Named as one
// function anyway, because the pairing is what has to survive: a future cache
// gains its invalidation here rather than at fourteen call sites.
func (s *Server) bumpPermissions() {
	s.hub.BroadcastAll("perms:changed", map[string]any{})
}
