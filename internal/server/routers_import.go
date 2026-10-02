package server

// Bulk device import: /api/routers/bulk (issue #150).
//
//	POST /api/routers/bulk/check   the preview: a verdict per row, nothing written
//	POST /api/routers/bulk         start importing the ready rows
//	GET  /api/routers/bulk         the running or last import, row by row
//
// ── "BULK", NOT "IMPORT", IN THE PATH ───────────────────────────────────────
//
// `TestImportHasOneCallSite` (internal/verify) flags any Go string containing
// `/import`, because RouterOS's `/import` runs a script and may only be called
// from the deploy path. This route runs no RouterOS script, but loosening that
// check to tell the two apart would weaken it for a name; the route is named
// for what it does instead.
//
// ── A GLOBAL ADMINISTRATOR'S, LIKE ADD DEVICE ───────────────────────────────
//
// `mayManagePrincipals`, the gate `routerCreate` uses. An import is a batch of
// creates, so it cannot be held to anything weaker; and a row can name a
// credential profile, which only an administrator may link.
//
// ── THE IMPORT PLANS AGAIN; IT DOES NOT TRUST THE PREVIEW ───────────────────
//
// The browser re-sends the rows, not the verdicts it was shown, and the import
// runs `fleetimport.Plan` against the fleet as it is NOW. A device added by
// hand since the preview is a duplicate, not a second copy.
//
// ── THREE WAYS IN, ONE OF WHICH WRITES TO THE ROUTER ────────────────────────
//
//   - plain:  added with the row's own login, exactly as Add Device does.
//   - verify: a profile and no password. The account is expected to exist
//     already, and the device is added and switched to the profile WITHOUT a
//     sign-in check. Nothing is written to the router.
//
//     NOT CHECKED, ON THE OPERATOR'S CALL (2026-10-02). A first version signed
//     in first and added nothing if that failed, which is the rule linking
//     follows. But a sign-in to an unreachable device waits out the 12 s
//     timeout, so 100 offline rows took about 20 minutes. A migration imports
//     devices that are not all up, and a wrong profile shows on the Devices page
//     as a sign-in error, so the import adds and moves on.
//   - link:   a profile and the router's current login. Added with that login,
//     and once every row is in, handed to the profile's own link job
//     (`loginLinkAll`, the Credential Profiles page's path): it creates the
//     account, proves it signs in, and undoes the change if it does not. A
//     failure there leaves the device on its own login, and the Credential
//     Profiles page says so.
//
//     AFTER THE IMPORT, NOT DURING IT, on the operator's call (2026-10-02): the
//     link has to reach the router, and an unreachable one held the whole import
//     until it timed out. The import adds every row at once and the account
//     creation follows in the background.
//
// ── ONE REFRESH AT THE END ──────────────────────────────────────────────────
//
// The device list broadcast and the fleet holds are synced once, after the last
// row, not per row: a 200-row import would otherwise rebuild every holder's
// state 200 times.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/fleetimport"
	"mikrodash/internal/safe"
	"mikrodash/internal/sites"
	"mikrodash/internal/store"
)

// importJob is the running or last import, held in memory: the device records
// are the durable result, and this only says what the most recent run did.
type importJob struct {
	Running bool           `json:"running"`
	Started int64          `json:"started"`
	Rows    []importResult `json:"rows"`
	Summary string         `json:"summary"`
}

type importResult struct {
	Line    int    `json:"line"`
	Label   string `json:"label"`
	State   string `json:"state"` // "pending", "working", "added", "warning", "failed"
	Message string `json:"message"`
}

type importJobs struct {
	mu  sync.Mutex
	job *importJob
}

// importBodyLimit fits MaxRows rows of long cells with room to spare.
const importBodyLimit = 4 << 20

func (s *Server) registerRouterImport(mux *http.ServeMux, limit func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("POST /api/routers/bulk/check", limit(s.routerImportCheck))
	mux.HandleFunc("POST /api/routers/bulk", limit(s.routerImportStart))
	mux.HandleFunc("GET /api/routers/bulk", s.routerImportStatus)
}

// importSession is routerCreate's gate.
func (s *Server) importSession(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return nil, false
	}
	if !s.mayManagePrincipals(sess) {
		writeJSONErr(w, http.StatusForbidden, "Administrator access required")
		return nil, false
	}
	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "router store unavailable")
		return nil, false
	}
	return sess, true
}

func (s *Server) importRows(w http.ResponseWriter, r *http.Request) ([]fleetimport.Row, bool) {
	var body struct {
		Rows []fleetimport.Row `json:"rows"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, importBodyLimit)).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "The rows could not be read")
		return nil, false
	}
	if len(body.Rows) == 0 {
		writeJSONErr(w, http.StatusBadRequest, "The file has no device rows")
		return nil, false
	}
	if len(body.Rows) > fleetimport.MaxRows {
		writeJSONErr(w, http.StatusBadRequest,
			fmt.Sprintf("At most %d devices per import; split the file", fleetimport.MaxRows))
		return nil, false
	}
	return body.Rows, true
}

// importFleet is what a plan is checked against, read now.
func (s *Server) importFleet() fleetimport.Fleet {
	var f fleetimport.Fleet
	routers, _ := s.store.Routers()
	for _, rt := range routers {
		f.Endpoints = append(f.Endpoints, fleetimport.Endpoint{Host: rt.Host, Port: rt.Port})
	}
	profiles, _ := s.store.LoginProfiles()
	for _, p := range profiles {
		f.Profiles = append(f.Profiles, fleetimport.Named{ID: p.ID, Name: p.Name})
	}
	if s.auditDB != nil {
		list, _ := s.auditDB.ListSites()
		for _, st := range list {
			f.Sites = append(f.Sites, fleetimport.Named{ID: st.ID, Name: st.Name})
		}
	}
	return f
}

func (s *Server) routerImportCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.importSession(w, r); !ok {
		return
	}
	rows, ok := s.importRows(w, r)
	if !ok {
		return
	}
	writeJSON(w, fleetimport.Plan(rows, s.importFleet()))
}

func (s *Server) routerImportStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.importSession(w, r); !ok {
		return
	}
	s.importJobs.mu.Lock()
	defer s.importJobs.mu.Unlock()
	if s.importJobs.job == nil {
		writeJSON(w, map[string]any{"job": nil})
		return
	}
	cp := *s.importJobs.job
	cp.Rows = append([]importResult(nil), s.importJobs.job.Rows...)
	writeJSON(w, map[string]any{"job": cp})
}

func (s *Server) routerImportStart(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.importSession(w, r)
	if !ok {
		return
	}
	rows, ok := s.importRows(w, r)
	if !ok {
		return
	}
	plan := fleetimport.Plan(rows, s.importFleet())
	if plan.Ready == 0 {
		writeJSONErr(w, http.StatusBadRequest, "No row is ready to import")
		return
	}

	job := &importJob{Running: true, Started: nowMillis(), Rows: []importResult{}}
	for _, v := range plan.Rows {
		if v.Status == "ready" {
			job.Rows = append(job.Rows, importResult{Line: v.Line, Label: v.Label, State: "pending"})
		}
	}
	s.importJobs.mu.Lock()
	if cur := s.importJobs.job; cur != nil && cur.Running {
		s.importJobs.mu.Unlock()
		writeJSONErr(w, http.StatusConflict, "An import is already running")
		return
	}
	s.importJobs.job = job
	s.importJobs.mu.Unlock()

	rec := s.httpRecorder(r, sess)
	go s.importRun(job, plan, rec)
	writeJSON(w, map[string]any{"ok": true, "rows": len(job.Rows)})
}

func (s *Server) importSet(job *importJob, i int, state, msg string) {
	s.importJobs.mu.Lock()
	job.Rows[i].State, job.Rows[i].Message = state, msg
	s.importJobs.mu.Unlock()
}

// importRun adds the ready rows, one at a time, in file order.
func (s *Server) importRun(job *importJob, plan fleetimport.Result, rec *audit.Recorder) {
	siteIDs, siteErr := s.importSites(plan.NewSites, rec)
	profiles := map[string]store.LoginProfile{}
	profileOf := func(id string) (store.LoginProfile, error) {
		if p, ok := profiles[id]; ok {
			return p, nil
		}
		p, err := s.store.LoginProfile(id)
		if err == nil {
			profiles[id] = p
		}
		return p, err
	}

	added, warned, failed := 0, 0, 0
	// links holds the link rows by profile, for the profile's link job.
	type linkRow struct {
		row      int
		routerID string
	}
	links := map[string][]linkRow{}
	var linkOrder []string
	i := -1
	for _, v := range plan.Rows {
		if v.Status != "ready" {
			continue
		}
		i++
		s.importSet(job, i, "working", "")
		state, msg, routerID := s.importOne(v, siteIDs, siteErr, profileOf, rec)
		if v.Mode == fleetimport.ModeLink && routerID != "" {
			if _, seen := links[v.ProfileID()]; !seen {
				linkOrder = append(linkOrder, v.ProfileID())
			}
			links[v.ProfileID()] = append(links[v.ProfileID()], linkRow{i, routerID})
		}
		switch state {
		case "added":
			added++
		case "warning":
			added++
			warned++
		default:
			failed++
		}
		s.importSet(job, i, state, msg)
	}

	if added > 0 {
		EvPermsChanged.BroadcastAll(s.hub, map[string]any{})
		s.broadcastRouterList()
		s.syncFleetHolds()
	}

	// The link rows' accounts, one job per profile, started now and not waited
	// for. A profile already mid-change cannot take a second job; those rows
	// stay on their own login and say so.
	for _, pid := range linkOrder {
		rows := links[pid]
		targets := make([]string, len(rows))
		for k, lr := range rows {
			targets[k] = lr.routerID
		}
		p, err := profileOf(pid)
		op, ok := (*loginOp)(nil), false
		if err == nil {
			op, ok = s.loginStart(pid, "link", targets)
		}
		if !ok {
			for _, lr := range rows {
				s.importSet(job, lr.row, "warning", "Added with its own login. "+
					"The profile was busy, so link it from Credential Profiles")
				warned++
			}
			continue
		}
		go s.loginLinkAll(op, p, targets, rec)
	}
	summary := fmt.Sprintf("%d of %d device(s) added", added, len(job.Rows))
	if warned > 0 {
		summary += fmt.Sprintf(", %d with a warning", warned)
	}
	if failed > 0 {
		summary += fmt.Sprintf(", %d failed", failed)
	}
	rec.Record(audit.Event{Action: "router.import", TargetType: "router", TargetName: summary,
		After: map[string]any{"added": added, "failed": failed, "warnings": warned}})
	s.importJobs.mu.Lock()
	job.Running, job.Summary = false, summary
	s.importJobs.mu.Unlock()
	log.Printf("[routers] import: %s", summary)
}

// importSites creates the sites the plan names and returns every site's id by
// lower-cased name. A site that could not be created is in the error map, and
// the rows that name it fail rather than being added without it.
func (s *Server) importSites(names []string, rec *audit.Recorder) (map[string]string, map[string]string) {
	ids, errs := map[string]string{}, map[string]string{}
	if s.auditDB == nil {
		for _, n := range names {
			errs[strings.ToLower(n)] = "sites are unavailable"
		}
		return ids, errs
	}
	lookup := func() {
		list, _ := s.auditDB.ListSites()
		for _, st := range list {
			ids[strings.ToLower(st.Name)] = st.ID
		}
	}
	lookup()
	created := 0
	for _, n := range names {
		if ids[strings.ToLower(n)] != "" {
			continue // created since the plan was made
		}
		patch, err := sites.ParseSiteBody(map[string]any{"name": n}, false)
		var site *db.Site
		if err == nil {
			site, err = s.auditDB.CreateSite(patch.Columns())
		}
		if err != nil {
			if db.IsDuplicateSiteName(err) {
				lookup()
				if ids[strings.ToLower(n)] != "" {
					continue
				}
			}
			log.Printf("[routers] import: site %q: %v", n, err)
			errs[strings.ToLower(n)] = safe.Message(err.Error())
			continue
		}
		ids[strings.ToLower(site.Name)] = site.ID
		created++
		rec.Record(audit.Event{Action: "site.create", TargetType: "site", TargetID: site.ID, TargetName: site.Name})
	}
	if created > 0 {
		s.broadcastSites()
	}
	return ids, errs
}

// importOne adds one ready row and says how it went, with the new device's id.
// It never waits on the router.
func (s *Server) importOne(v fleetimport.Verdict, siteIDs, siteErr map[string]string,
	profileOf func(string) (store.LoginProfile, error), rec *audit.Recorder) (string, string, string) {
	for _, n := range v.Sites {
		if msg := siteErr[strings.ToLower(n)]; msg != "" {
			return "failed", fmt.Sprintf("Site %q could not be created (%s)", n, msg), ""
		}
	}
	body := v.Body(siteIDs)

	var profile store.LoginProfile
	if v.Mode != fleetimport.ModePlain {
		p, err := profileOf(v.ProfileID())
		if err != nil {
			return "failed", fmt.Sprintf("The credential profile %q is gone", v.Profile), ""
		}
		profile = p
	}

	rt, err := s.store.AddRouter(body)
	if err != nil {
		return "failed", safe.Message(err.Error()), ""
	}
	rec.Record(audit.Event{
		Action: "router.create", TargetType: "router", TargetID: rt.ID,
		TargetName: firstNonEmpty(rt.Label, rt.Host), RouterID: rt.ID,
		After: map[string]any{"via": "import"},
	})

	switch v.Mode {
	case fleetimport.ModeVerify:
		if err := s.store.UseLoginProfile(rt.ID, profile.ID); err != nil {
			return "warning", "Added, but it could not be switched to " + profile.Name + " (" +
				safe.Message(err.Error()) + "); it has no login until you link it", rt.ID
		}
		s.loginAudit(rec, "loginprofile.link", profile, rt.ID, nil)
		return "added", "Signs in with " + profile.Name + " (not checked during the import)", rt.ID
	case fleetimport.ModeLink:
		return "added", "Added with its own login. Creating the " + store.LoginUserName + " account for " +
			profile.Name + " follows in the background; Credential Profiles shows how it went", rt.ID
	}
	return "added", "", rt.ID
}
