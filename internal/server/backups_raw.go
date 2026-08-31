package server

// `GET /api/backups/:id/raw` — the one backup route with no session behind it.
//
// ── WHY IT IS UNAUTHENTICATED, AND WHAT STANDS IN FOR THE SESSION ───────────
//
// A restore is the one direction that needs the ROUTER to reach US:
// `/tool/fetch upload=yes` refuses anything but [s]ftp, so the file has to be
// pulled over HTTP — and a router cannot present a session cookie. The
// capability token is therefore the ENTIRE gate, and it is constrained on every
// axis `internal/backups.RestoreTokens` documents: 32 random bytes, one backup
// on one router, single use, 120 seconds, and bound to the router's configured
// host.
//
// ── THE ROW IS THE AUTHORITY, NOT THE URL ───────────────────────────────────
//
// The id in the path selects a row; the row's own router is then compared with
// the one the token was bound to. So a caller holding a token for one router
// cannot be handed a backup belonging to another by changing the id.
// `BackupServable` makes that one decision, and it is tested there.
//
// ── A DENIAL IS AUDITED; A MISS IS NOT ──────────────────────────────────────
//
// A rejected token is a security event and is recorded as `backup.raw.denied`
// with its reason. A row that does not exist, or is pruned, answers 404 without
// an audit row — matching the original, and for a good reason: the id comes from
// a URL a router was handed, so a stale one is an ordinary outcome rather than
// an attempt at anything.
//
// ── GO OWNS THIS ROUTE OUTRIGHT (operator decision, 2026-08-25) ─────────────
//
// It hangs off the REAL path, not the staging prefix. That was the third of the
// three options written up for `backupBaseUrl`: rather than adding a flag or
// silently reinterpreting an operator-set address, this process serves the file
// itself and the question of which port to name disappears.

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"mikrodash/internal/audit"
	"mikrodash/internal/backups"
	"mikrodash/internal/safe"
)

const backupRawPath = "/api/backups/{id}/raw"

func (s *Server) registerBackupRaw(mux *http.ServeMux) {
	mux.HandleFunc("GET "+backupRawPath, s.backupRaw)
}

func (s *Server) backupRaw(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	verdict := s.restoreTokens.Redeem(r.URL.Query().Get("t"), remoteIP(r))
	if !verdict.OK {
		// The reason is the token layer's own vocabulary — never the token.
		s.auditSystem(audit.Event{
			Action: "backup.raw.denied", TargetType: "backup", TargetID: idStr,
			Outcome: "denied", Note: verdict.Reason,
		})
		writeJSONStatus(w, http.StatusForbidden, map[string]any{"ok": false, "error": "forbidden"})
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	row, err := s.auditDB.GetBackup(id)
	if err != nil || row == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	stem := ""
	if row.Stem != nil {
		stem = *row.Stem
	}
	if !backups.BackupServable(verdict, row.ID, row.RouterID, stem, row.PrunedAt) {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}

	dir := ""
	if row.Dir != nil {
		dir = *row.Dir
	}
	buf, err := backups.ReadBackup(dir, stem)
	if err != nil {
		// SANITISED, because this body reaches whoever holds the URL. The log
		// keeps the detail.
		log.Printf("[backup] raw read failed: %v", err)
		writeJSONStatus(w, http.StatusInternalServerError,
			map[string]any{"ok": false, "error": safe.Message(err.Error())})
		return
	}

	s.auditSystem(audit.Event{
		Action: "backup.raw", TargetType: "backup", Scope: "router",
		RouterID: row.RouterID, TargetID: idStr,
		Note: strconv.Itoa(len(buf)) + " bytes",
	})
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(buf)
}

// remoteIP is the address the token is bound against.
//
// `r.RemoteAddr` carries a port; the token records a host. Splitting it is not
// cosmetic — a comparison against "10.0.0.2:54321" would never match, and the
// failure would look like a token problem rather than a parsing one.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

func writeJSONStatus(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
