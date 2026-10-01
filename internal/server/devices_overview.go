package server

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/history"
)

// GET /api/devices/overview - what the Devices page's cards show that is not
// live: each device's connectivity strip and its backup at a glance.
//
// ── ONE CALL FOR THE FLEET, FOUR QUERIES WHATEVER ITS SIZE ──────────────────
//
// The page refreshes this once a minute; the 2s `routers:stats` frame carries
// what is live. Every read below is fleet-wide (`ConnStatesAt`, `ConnEventsIn`,
// `MonitorRunsIn`, `BackupOverview`), so four devices and four hundred cost the
// same number of queries. `routerId` narrows the answer for the device modal,
// which asks for one device over 24h, 7d or 30d.
//
// ── THE GATES ───────────────────────────────────────────────────────────────
//
// The STRIP is the history of a status the Devices row already shows, so it is
// visible exactly where the row is: `visibleRouters`, the row's own gate. The
// BACKUP field is backup data, so it additionally needs `backups` read on that
// router - answered for the whole fleet at once by `rbac.PageRouterIDs` - and is
// null where the viewer may not see it.
//
// FAILS CLOSED. An RBAC error yields no rows and no backup fields, never all.

const (
	overviewDefaultWindow = 24 * time.Hour
	overviewMaxWindow     = 31 * 24 * time.Hour
)

// DeviceOverview is one device's overview row.
type DeviceOverview struct {
	RouterID string `json:"routerId"`
	history.SpanResult
	// Backup is null when there is nothing to show OR the viewer may not see
	// backups on this router; the page renders both as "no backup shown".
	Backup *db.BackupBrief `json:"backup"`
}

func (s *Server) registerDevicesOverview(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/devices/overview", s.devicesOverview)
}

func (s *Server) devicesOverview(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if sess.AuthMode != "none" && !sessionHasPage(sess, "devices") {
		writeJSONErr(w, http.StatusForbidden, "no access to the Devices page")
		return
	}
	if s.auditDB == nil || s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "the database is unavailable")
		return
	}

	from, to := overviewWindow(r, time.Now().UnixMilli())
	only := r.URL.Query().Get("routerId")

	visible := s.visibleRouters(sess) // nil = unrestricted
	backupsOK, berr := s.backupsReadable(sess)
	if berr != nil {
		log.Printf("[devices] overview backup scope: %v", berr)
		backupsOK = map[string]bool{} // an error is not a permission
	}

	before, err := s.auditDB.ConnStatesAt(from)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read connectivity")
		return
	}
	events, err := s.auditDB.ConnEventsIn(from, to)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read connectivity")
		return
	}
	runs, err := s.auditDB.MonitorRunsIn(from, to)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read monitoring coverage")
		return
	}
	backups, err := s.auditDB.BackupOverview()
	if err != nil {
		log.Printf("[devices] overview backups: %v", err)
		backups = map[string]db.BackupBrief{} // a strip without backups beats no strip
	}

	// RUNS OPEN NOW reach `to`. Their stored `last_seen_at` lags by up to the
	// heartbeat interval, and without this every watched router's strip ended in
	// a grey sliver - measured on the dev install right after deploying the
	// endpoint, all four strips ended "unmonitored" with all four runs open.
	openNow := s.coverage.OpenRouters()

	all, _ := s.store.Routers()
	out := []DeviceOverview{}
	for _, rt := range all {
		if rt.Disabled || (only != "" && rt.ID != only) {
			continue
		}
		if visible != nil && !visible[rt.ID] {
			continue
		}
		in := history.SpanInput{From: from, To: to, Events: events[rt.ID]}
		if v, ok := before[rt.ID]; ok {
			in.Before = &v
		}
		var lastSeen int64
		for _, run := range runs[rt.ID] {
			in.Runs = append(in.Runs, history.Run{From: run.StartedAt, To: run.LastSeenAt})
			if run.LastSeenAt > lastSeen {
				lastSeen = run.LastSeenAt
			}
		}
		if openNow[rt.ID] && lastSeen > 0 {
			// Unioned with the run it continues, so no seam is drawn.
			in.Runs = append(in.Runs, history.Run{From: lastSeen, To: to})
		}
		row := DeviceOverview{RouterID: rt.ID, SpanResult: history.Spans(in)}
		if b, ok := backups[rt.ID]; ok && backupsOK != nil && (backupsOK[""] || backupsOK[rt.ID]) {
			bb := b
			row.Backup = &bb
		}
		out = append(out, row)
	}
	writeJSON(w, out)
}

// overviewWindow reads `from`/`to` (epoch ms), defaulting to the last 24 hours
// and clamping to a month, so one request cannot ask for years of rows.
func overviewWindow(r *http.Request, now int64) (int64, int64) {
	q := r.URL.Query()
	to := now
	if v, err := strconv.ParseInt(q.Get("to"), 10, 64); err == nil && v > 0 && v < now {
		to = v
	}
	from := to - overviewDefaultWindow.Milliseconds()
	if v, err := strconv.ParseInt(q.Get("from"), 10, 64); err == nil && v > 0 && v < to {
		from = v
	}
	if min := to - overviewMaxWindow.Milliseconds(); from < min {
		from = min
	}
	return from, to
}

// backupsReadable is the routers on which this viewer may read backups, for the
// whole fleet in one resolution. The key "" means "every router": an install
// with sign-in off, or RBAC not yet available (the documented gap).
func (s *Server) backupsReadable(sess *Session) (map[string]bool, error) {
	if sess.AuthMode == "none" {
		return map[string]bool{"": true}, nil
	}
	if !sessionHasPage(sess, "backups") {
		return map[string]bool{}, nil
	}
	if !s.rbac.Available() {
		return map[string]bool{"": true}, nil
	}
	return s.rbac.PageRouterIDs(s.userIDFor(sess.Username), "backups", "read")
}

// sessionHasPage is the session's own page map: may this viewer use the page at
// all. The first of the two gates, as `mayReadReports` uses it.
func sessionHasPage(sess *Session, page string) bool {
	got, ok := sess.Pages[page]
	return ok && (got == "read" || got == "write")
}
