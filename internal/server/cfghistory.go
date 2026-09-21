package server

// Config Management's History and Drift tabs.
//
// History is the run ledger: every deploy, who started it, and what each
// router said. Drift compares a router's menus with the baseline its last
// successful deploy recorded; it reads a router only when asked, never on a
// timer, because each check is an export on that router.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/backups"
	"mikrodash/internal/cfgdeploy"
	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/db"
	"mikrodash/internal/routeros"
)

func (s *Server) registerConfigHistory(mux *http.ServeMux) {
	mux.HandleFunc("GET "+cfgPrefix+"runs", s.cfgRead(s.cfgRuns))
	mux.HandleFunc("GET "+cfgPrefix+"runs/{id}", s.cfgRead(s.cfgRunDetail))
	mux.HandleFunc("GET "+cfgPrefix+"drift", s.cfgRead(s.cfgDriftList))
	// A check exports from the router, and an accept exports again to be sure
	// it stores what was seen: both hold a router channel, so both are limited.
	lim := newRateLimiter(20, time.Minute).limit
	mux.HandleFunc("POST "+cfgPrefix+"drift/check", lim(s.cfgRead(s.cfgDriftCheck)))
	mux.HandleFunc("POST "+cfgPrefix+"drift/accept", lim(s.cfgWrite("config.drift.accept", s.cfgDriftAccept)))
}

// cfgRunRow is one run as the History list shows it: no text, no values.
type cfgRunRow struct {
	ID           string         `json:"id"`
	TemplateID   *string        `json:"templateId"`
	TemplateName string         `json:"templateName"`
	Revision     int            `json:"revision"`
	Method       string         `json:"method"`
	State        string         `json:"state"`
	StartedBy    string         `json:"startedBy"`
	CreatedAt    int64          `json:"createdAt"`
	FinishedAt   *int64         `json:"finishedAt"`
	Error        *string        `json:"error"`
	Routers      map[string]int `json:"routers"`
}

// usernames maps each user id to its username. A deleted user has no entry,
// and is shown by id.
func (s *Server) usernames() map[string]string {
	out := map[string]string{}
	if s.store == nil {
		return out
	}
	users, err := s.store.Users()
	if err != nil {
		log.Printf("[config] cannot read users.json: %v", err)
		return out
	}
	for _, u := range users {
		out[u.ID] = u.Username
	}
	return out
}

// nameOr is the name a map holds for an id, or the id itself.
func nameOr(names map[string]string, id string) string {
	if n, ok := names[id]; ok {
		return n
	}
	return id
}

// routerLabels maps each router id to what the app calls it.
func (s *Server) routerLabels() map[string]string {
	out := map[string]string{}
	if s.store == nil {
		return out
	}
	routers, err := s.store.Routers()
	if err != nil {
		return out
	}
	for _, r := range routers {
		switch {
		case r.Label != "":
			out[r.ID] = r.Label
		case r.Host != "":
			out[r.ID] = r.Host
		default:
			out[r.ID] = r.ID
		}
	}
	return out
}

func (s *Server) cfgRuns(w http.ResponseWriter, _ *http.Request, _ *Session) {
	runs, err := s.auditDB.CfgRuns(200)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	states, err := s.auditDB.CfgRunTargetStates()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	names := s.usernames()
	out := make([]cfgRunRow, 0, len(runs))
	for _, r := range runs {
		counts := states[r.ID]
		if counts == nil {
			counts = map[string]int{}
		}
		out = append(out, cfgRunRow{ID: r.ID, TemplateID: r.TemplateID, TemplateName: r.TemplateName,
			Revision: r.Revision, Method: r.Method, State: r.State, StartedBy: nameOr(names, r.CreatedBy),
			CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt, Error: r.Error, Routers: counts})
	}
	writeJSON(w, map[string]any{"ok": true, "runs": out})
}

// cfgRunTargetRow is one router of a run, with its label.
type cfgRunTargetRow struct {
	db.CfgRunTarget
	Label string `json:"label"`
}

func (s *Server) cfgRunDetail(w http.ResponseWriter, r *http.Request, _ *Session) {
	run, targets, err := s.auditDB.CfgRun(r.PathValue("id"))
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	if run == nil {
		writeJSONErr(w, http.StatusNotFound, "no such run")
		return
	}
	labels := s.routerLabels()
	rows := make([]cfgRunTargetRow, 0, len(targets))
	for _, t := range targets {
		rows = append(rows, cfgRunTargetRow{CfgRunTarget: t, Label: nameOr(labels, t.RouterID)})
	}
	writeJSON(w, map[string]any{"ok": true, "run": run, "startedBy": nameOr(s.usernames(), run.CreatedBy),
		"targets": rows})
}

// cfgDriftRow is one baseline as the Drift tab lists it: no body.
type cfgDriftRow struct {
	TemplateID   string  `json:"templateId"`
	TemplateName string  `json:"templateName"`
	RouterID     string  `json:"routerId"`
	RouterLabel  string  `json:"routerLabel"`
	RunID        *string `json:"runId"`
	Fingerprint  string  `json:"fingerprint"`
	TakenAt      int64   `json:"takenAt"`
}

func (s *Server) cfgDriftList(w http.ResponseWriter, _ *http.Request, _ *Session) {
	bases, err := s.auditDB.CfgBaselines()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	labels := s.routerLabels()
	tplNames := map[string]string{}
	out := make([]cfgDriftRow, 0, len(bases))
	for _, b := range bases {
		label, ok := labels[b.RouterID]
		if !ok {
			continue // a router no longer in the fleet
		}
		name, seen := tplNames[b.TemplateID]
		if !seen {
			name = s.cfgTemplateName(b.TemplateID)
			tplNames[b.TemplateID] = name
		}
		out = append(out, cfgDriftRow{TemplateID: b.TemplateID, TemplateName: name, RouterID: b.RouterID,
			RouterLabel: label, RunID: b.RunID, Fingerprint: b.Fingerprint, TakenAt: b.TakenAt})
	}
	writeJSON(w, map[string]any{"ok": true, "baselines": out})
}

// cfgTemplateName names a template by id, canned or stored; an unreadable one
// is named by its id.
func (s *Server) cfgTemplateName(id string) string {
	if strings.HasPrefix(id, cfgtpl.CannedPrefix) {
		if t, ok := cannedRow(id); ok {
			return t.Name
		}
		return id
	}
	if t, err := s.auditDB.CfgTemplate(id); err == nil && t != nil {
		return t.Name
	}
	return id
}

// cfgDriftIn names one baseline; an accept also carries the fingerprint of
// what the check showed.
type cfgDriftIn struct {
	TemplateID  string `json:"templateId"`
	RouterID    string `json:"routerId"`
	Fingerprint string `json:"fingerprint"`
}

// cfgDriftSnapshot reads the baseline and takes the router's snapshot of the
// same menus. On failure the baseline is nil and the status and message are
// the caller's to write.
func (s *Server) cfgDriftSnapshot(r *http.Request, sess *Session, in cfgDriftIn) (
	base *db.CfgBaseline, current string, status int, msg string) {
	base, err := s.auditDB.CfgBaselineFor(in.TemplateID, in.RouterID)
	if err != nil {
		log.Printf("[config] baseline %s/%s: %v", in.TemplateID, in.RouterID, err)
		return nil, "", http.StatusInternalServerError, "could not read the baseline"
	}
	if base == nil {
		return nil, "", http.StatusNotFound, "this router has no baseline for that template"
	}
	t, ok := s.cfgBaselineTemplate(base)
	if !ok {
		return nil, "", http.StatusConflict, "the template this baseline was taken for can no longer be read"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	sn, drop, ok := s.cfgRouter(ctx, sess, in.RouterID, "config-drift")
	defer drop()
	if !ok {
		return nil, "", http.StatusNotFound, "that router is not available"
	}
	err = sn.InWriteQueue(func() error {
		wr := func(cmd string, a ...string) ([]map[string]string, error) {
			rows, err := sn.Exec(routeros.Cmd{Path: cmd, Args: a, Timeout: 60 * time.Second})
			out := make([]map[string]string, len(rows))
			for i, x := range rows {
				out[i] = x
			}
			return out, err
		}
		defer backups.Sweep(wr, cfgtpl.IsOurFile, func(m string) { log.Printf("[config] %s", m) })
		var err error
		current, err = cfgdeploy.Snapshot(cfgdeploy.Env{Do: sn.Exec, Now: time.Now, Sleep: time.Sleep}, t)
		return err
	})
	if err != nil {
		log.Printf("[config] drift snapshot %s: %v", in.RouterID, err)
		return nil, "", http.StatusBadGateway, "the router could not be read"
	}
	return base, current, 0, ""
}

// cfgBaselineTemplate is the template a baseline was taken of: the text its
// run sent, whose menus the deploy's snapshot read. A baseline whose run
// cannot be read falls back to the template as it is now.
func (s *Server) cfgBaselineTemplate(b *db.CfgBaseline) (*cfgtpl.Template, bool) {
	if b.RunID != nil {
		if run, _, err := s.auditDB.CfgRun(*b.RunID); err == nil && run != nil {
			if t, err := cfgtpl.Parse(run.BodyMasked); err == nil {
				return t, true
			}
		}
	}
	var row *db.CfgTemplate
	if strings.HasPrefix(b.TemplateID, cfgtpl.CannedPrefix) {
		c, ok := cannedRow(b.TemplateID)
		if !ok {
			return nil, false
		}
		row = c
	} else if t, err := s.auditDB.CfgTemplate(b.TemplateID); err == nil && t != nil {
		row = t
	} else {
		return nil, false
	}
	body, err := s.cfgOpen(row)
	if err != nil {
		return nil, false
	}
	t, err := cfgtpl.Parse(body)
	return t, err == nil
}

func (s *Server) cfgDriftCheck(w http.ResponseWriter, r *http.Request, sess *Session) {
	var in cfgDriftIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "the request does not name a baseline")
		return
	}
	base, current, status, msg := s.cfgDriftSnapshot(r, sess, in)
	if base == nil {
		writeJSONErr(w, status, msg)
		return
	}
	d := backups.Diff(base.Body, current)
	if d.Hunks == nil {
		d.Hunks = []backups.Hunk{}
	}
	writeJSON(w, map[string]any{"ok": true, "drifted": d.Changed, "diff": d,
		"fingerprint": cfgdeploy.Hash(current), "takenAt": base.TakenAt, "checkedAt": time.Now().UnixMilli()})
}

// cfgDriftAccept makes what the router holds now the baseline. It reads the
// router again and stores that only if it is what the check showed: a change
// made between the two would otherwise be accepted unseen.
func (s *Server) cfgDriftAccept(w http.ResponseWriter, r *http.Request, sess *Session) {
	var in cfgDriftIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Fingerprint == "" {
		writeJSONErr(w, http.StatusBadRequest, "the request does not name what was checked")
		return
	}
	base, current, status, msg := s.cfgDriftSnapshot(r, sess, in)
	if base == nil {
		writeJSONErr(w, status, msg)
		return
	}
	fp := cfgdeploy.Hash(current)
	if fp != in.Fingerprint {
		writeJSONErr(w, http.StatusConflict, "the router changed since you checked it; check it again")
		return
	}
	if err := s.auditDB.SetCfgBaseline(db.CfgBaseline{TemplateID: base.TemplateID, RouterID: base.RouterID,
		RunID: base.RunID, Body: current, Fingerprint: fp, TakenAt: time.Now().UnixMilli()}); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "config.drift.accept", TargetType: "config-template",
		RouterID: base.RouterID, TargetID: base.TemplateID, TargetName: s.cfgTemplateName(base.TemplateID),
		Note: "accepted as the new baseline; fingerprint " + fp})
	writeJSON(w, map[string]any{"ok": true, "fingerprint": fp})
}
