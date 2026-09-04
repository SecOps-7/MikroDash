package server

// `GET /api/backups/:id/rsc` and `GET /api/backups/:id/backup` — the two
// download links on the Backups page.
//
// ── THEY WERE NEVER PORTED, AND THE PAGE HAS LINKED TO THEM ALL ALONG ───────
//
// `web/src/pages/backups.ts` draws both as plain `<a href>` on every stored row.
// The Go side registered only `/raw`, the unauthenticated restore route a ROUTER
// fetches — a sibling segment, which gives no subtree coverage — so both links
// fell through the mux to the static catch-all and answered Go's own
// "404 page not found". Reported on issue #124.
//
// Nothing caught it because `internal/verify`'s endpoint audit scans the
// frontend for `fetch(...)` calls and URL constants. A URL built by string
// concatenation into an href is neither, so the "every endpoint the browser
// calls is served" check never saw these two. That gap is closed alongside this.
//
// ── DOWNLOAD IS A WRITE-LEVEL QUESTION ──────────────────────────────────────
//
// Not a reading of the page — `backups` at WRITE, which is what `bkMayWrite`
// asks and what the payload's `permitted` flag is built from. `backups.go`'s
// header states the reason: an export describes the whole network and the binary
// carries every key on the device. The page only draws these links when
// `permitted` is true, so a laxer gate here would hand both to a viewer the page
// never offered them to.
//
// ── THE ROW IS THE AUTHORITY, NOT THE QUERY STRING ──────────────────────────
//
// The permission is asked about the router in `?routerId=`, and the FILE comes
// from the row named by the path id. Those must be the same router or the check
// answers a different question from the one being performed: a principal with
// write on router A could otherwise walk ids and pull router B's backup. So the
// row's own `router_id` is compared with the query, and a mismatch is a 404
// rather than a 403 — it must not confirm that the id exists.
//
// Same rule `/raw` applies through `backups.BackupServable`; it is spelled out
// here rather than shared because that helper decides against a redeemed
// capability token, and this decides against a session.

import (
	"log"
	"net/http"
	"strconv"

	"mikrodash/internal/audit"
	"mikrodash/internal/backups"
	"mikrodash/internal/safe"
)

const (
	backupRscPath  = "/api/backups/{id}/rsc"
	backupFilePath = "/api/backups/{id}/backup"
)

func (s *Server) registerBackupDownloads(mux *http.ServeMux) {
	mux.HandleFunc("GET "+backupRscPath, s.backupPart("rsc"))
	mux.HandleFunc("GET "+backupFilePath, s.backupPart("backup"))
}

// mayDownloadBackup is `backups` at write on THIS router, both gates, matching
// `mayWriteReports` next door and `bkMayWrite` on the socket side.
func (s *Server) mayDownloadBackup(sess *Session, routerID string) bool {
	if sess == nil {
		return false
	}
	if sess.AuthMode == "none" {
		return true
	}
	if !sess.CanPage("backups", "write", routerID) {
		return false
	}
	if !s.rbac.Available() {
		return true // the documented install-wide gap, reported at startup
	}
	ok, err := s.rbac.CanPage(s.userIDFor(sess.Username), "backups", "write", routerID)
	if err != nil {
		log.Printf("[rbac] download backup on %s: %v", routerID, err)
		return false
	}
	return ok
}

// backupPart serves one half of a stored pair. `part` is "rsc" or "backup".
func (s *Server) backupPart(part string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.auth.Validate(r.Header.Get("Cookie"))
		if err != nil || sess == nil {
			writeJSONErr(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if s.auditDB == nil {
			writeJSONErr(w, http.StatusServiceUnavailable, "no backup database")
			return
		}
		routerID := r.URL.Query().Get("routerId")
		if !s.mayDownloadBackup(sess, routerID) {
			writeJSONErr(w, http.StatusForbidden, "Not permitted")
			return
		}

		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, "not found")
			return
		}
		row, err := s.auditDB.GetBackup(id)
		// THE FOUR WAYS THERE IS NOTHING TO SEND, all answered identically. A
		// row belonging to another router is in here deliberately: a distinct
		// status would confirm the id exists to a caller probing for it.
		if err != nil || row == nil || row.RouterID != routerID ||
			row.Stem == nil || *row.Stem == "" || row.PrunedAt != nil {
			writeJSONErr(w, http.StatusNotFound, "not found")
			return
		}

		dir := ""
		if row.Dir != nil {
			dir = *row.Dir
		}
		name := backups.SlugFor(routerLabelFor(s, routerID)) + "-" + *row.Stem

		var body []byte
		switch part {
		case "rsc":
			text, rerr := backups.ReadRsc(dir, *row.Stem)
			if rerr != nil {
				err = rerr
			}
			body = []byte(text)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			name += ".rsc"
		default:
			body, err = backups.ReadBackup(dir, *row.Stem)
			w.Header().Set("Content-Type", "application/octet-stream")
			name += ".backup"
		}
		if err != nil {
			// The row says the file is there and it is not — a pruned pair is
			// already excluded above, so this is a real fault rather than an
			// ordinary miss. SANITISED, because the body reaches a browser.
			log.Printf("[backup] %s read failed for %d: %v", part, id, err)
			writeJSONErr(w, http.StatusInternalServerError, safe.Message(err.Error()))
			return
		}

		s.httpRecorder(r, sess).Record(audit.Event{
			Action: "backup.download", TargetType: "backup", Scope: "router",
			RouterID: routerID, TargetID: strconv.FormatInt(id, 10), TargetName: name,
			Extra: []audit.KV{{Key: "part", Value: part},
				{Key: "bytes", Value: len(body)}},
		})
		// ATTACHMENT, with the router's name in it. Without this the browser
		// renders the .rsc as a page and the .backup as a download called by its
		// id, and an operator collecting several cannot tell them apart.
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		_, _ = w.Write(body)
	}
}

// routerLabelFor names the file after the device rather than its id. Falls back
// to the id, because a label is not guaranteed and an unnamed download is still
// better than none.
func routerLabelFor(s *Server, routerID string) string {
	if rec := s.routerRecord(routerID); rec != nil && rec.Label != "" {
		return rec.Label
	}
	return routerID
}
