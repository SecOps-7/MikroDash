package server

// The router's own name, RouterOS's System Identity, from the Edit Device dialog
// (#97).
//
// ── NOT THE DISPLAY NAME, AND NOT STORED HERE ───────────────────────────────
//
// The dialog's Display Name is MikroDash's label for a device and lives in
// routers.json. The identity is the name the router gives itself, in
// /system/identity, and changing it writes to the router. So it has its own
// endpoint rather than riding the device save: the two can succeed and fail
// independently, and only one of them is a router write.
//
// ── A ROUTER WRITE, SO THE #97 CONTRACT APPLIES ─────────────────────────────
//
// Permission is the dialog's own: router:manage on this device. On top of it,
// as every router write does, the change is refused with sign-in off or with no
// audit database, counts against the per-user per-router allowance, runs in the
// router's write queue, and is read back before success is reported. A change
// the read-back cannot confirm says so rather than claiming success.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode"

	"mikrodash/internal/audit"
	"mikrodash/internal/routeros"
	"mikrodash/internal/session"
)

const routerIdentityHold = "router-identity"

// routerIdentityMaxLen bounds the request, not RouterOS: the router refuses a
// name it will not hold, and that refusal reaches the dialog as the router's own
// words.
const routerIdentityMaxLen = 255

var (
	identityPrintCmd = routeros.Cmd{Path: "/system/identity/print"}
)

type identityResult struct {
	OK    bool   `json:"ok"`
	Code  string `json:"code"`
	Name  string `json:"name,omitempty"`
	Error string `json:"error,omitempty"`
}

func readIdentity(sn *session.Session) (string, error) {
	rows, err := sn.Exec(identityPrintCmd)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0]["name"], nil
}

// validIdentity refuses what can never be a name: blank, a control character,
// or a request larger than any name.
func validIdentity(name string) string {
	if name == "" {
		return "The router identity cannot be blank."
	}
	if len(name) > routerIdentityMaxLen {
		return "The router identity is too long."
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "The router identity cannot contain control characters."
		}
	}
	return ""
}

// routerIdentityGet is `GET /api/routers/{id}/identity`.
func (s *Server) routerIdentityGet(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSONErr(w, http.StatusBadRequest, "no router id")
		return
	}
	if !s.mayManageRouter(sess, id) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	sn, drop, ok := s.fleetSession(ctx, id, routerIdentityHold)
	if !ok {
		writeJSON(w, map[string]any{"available": false, "reason": "unreachable"})
		return
	}
	defer drop()
	name, err := readIdentity(sn)
	if err != nil {
		writeJSON(w, map[string]any{"available": false, "reason": "read-failed"})
		return
	}
	writeJSON(w, map[string]any{"available": true, "name": name})
}

// routerIdentitySet is `PUT /api/routers/{id}/identity`.
func (s *Server) routerIdentitySet(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSONErr(w, http.StatusBadRequest, "no router id")
		return
	}
	if !s.mayManageRouter(sess, id) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return
	}
	// FAIL CLOSED, as the socket's write path does (#97). `mayManageRouter`
	// answers yes with sign-in off, which is right for editing MikroDash's own
	// record and wrong for writing to a router.
	if sess.AuthMode == "none" {
		writeJSONErr(w, http.StatusForbidden, "Router changes need sign-in to be turned on")
		return
	}
	if s.auditDB == nil {
		writeJSONErr(w, http.StatusForbidden, "Router changes are unavailable: the audit database is not open")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024)).Decode(&body); err != nil {
		writeJSON400OK(w)
		return
	}
	name := strings.TrimSpace(body.Name)
	if msg := validIdentity(name); msg != "" {
		writeJSON(w, identityResult{Code: "invalid", Error: msg})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	sn, drop, ok := s.fleetSession(ctx, id, routerIdentityHold)
	if !ok {
		writeJSON(w, identityResult{Code: "unreachable"})
		return
	}
	defer drop()
	writeJSON(w, s.setRouterIdentity(r, sess, sn, id, name))
}

// setRouterIdentity is the write itself, apart from the HTTP handling so it can
// be driven against a scripted router.
func (s *Server) setRouterIdentity(r *http.Request, sess *Session, sn *session.Session,
	routerID, name string) identityResult {

	if s.writeLimit != nil {
		if ok, _, _ := s.writeLimit.take(writeLimitKey(sess.Username, routerID)); !ok {
			return identityResult{Code: "rate-limited"}
		}
	}
	rec := s.httpRecorder(r, sess)
	out := identityResult{}
	werr := sn.InWriteQueue(func() error {
		before, rerr := readIdentity(sn)
		if rerr != nil {
			out.Code = "read-failed"
			return nil
		}
		// ALREADY THAT NAME is not a write, and not an error either.
		if before == name {
			out.OK, out.Code, out.Name = true, "unchanged", name
			return nil
		}
		if _, err := sn.Exec(routeros.Cmd{Path: "/system/identity/set", Args: []string{"=name=" + name}}); err != nil {
			out.Code = writeFailCode(err)
			rec.Record(audit.Event{
				Action: "system.identity.update", TargetType: "identity", RouterID: routerID,
				TargetName: name, Outcome: "failed", Note: out.Code,
				Before: map[string]any{"name": before},
			})
			return nil
		}
		after, rerr := readIdentity(sn)
		if rerr != nil || after != name {
			// THE ROUTER ACCEPTED IT AND IT COULD NOT BE CONFIRMED. Not a failure,
			// because the write may have landed; not a success, because nothing
			// shows that it did.
			out.Code = "outcome-unknown"
			rec.Record(audit.Event{
				Action: "system.identity.update", TargetType: "identity", RouterID: routerID,
				TargetName: name, Note: "outcome-unknown: the change could not be confirmed",
				Before: map[string]any{"name": before},
			})
			return nil
		}
		out.OK, out.Code, out.Name = true, "updated", after
		rec.Record(audit.Event{
			Action: "system.identity.update", TargetType: "identity", RouterID: routerID,
			TargetName: after,
			Before:     map[string]any{"name": before}, After: map[string]any{"name": after},
		})
		return nil
	})
	if werr != nil {
		out.OK = false
		out.Code = writeFailCode(werr)
	}
	return out
}
