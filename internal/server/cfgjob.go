package server

// Config Management's deploy job: one template to one router or the fleet.
//
// ── ONE JOB, OWNED BY THE SERVER ────────────────────────────────────────────
//
// A deploy runs on the server's lifetime, not a browser's: closing the tab does
// not stop it, and a restart interrupts it for good (db.InterruptCfgRuns),
// because its secret values lived only here. One runs at a time, and each
// router is changed inside one hold of its write queue, so Backups and page
// writes wait for it.
//
// ── THE CANARY, AND WHAT A PERSON MUST TYPE ─────────────────────────────────
//
// The first router is the canary. Starting needs its name typed back; after it,
// the run waits, with no timer, until the router count is typed back. A
// failure anywhere halts the run, and a cancel stops it before the next router,
// never in the middle of one: a cancelled import is half-applied (measured).
//
// ── EVERY ROUTER IS AUTHORISED AGAIN ────────────────────────────────────────
//
// Before each router: the person who started the run must still be a signed-in
// global administrator and the router still in the fleet. Each router's text
// must still hash to what that person approved in its preview; cfgdeploy
// refuses otherwise. Audit rows name the person who started the run.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/backups"
	"mikrodash/internal/cfgdeploy"
	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/db"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
	"mikrodash/internal/session"
)

// cfgRoom is where a deploy's progress is broadcast. Administrators join it
// with cfgdeploy:watch.
const cfgRoom = "cfgdeploy"

// CfgDeployTarget is one router's line in the progress payload.
type CfgDeployTarget struct {
	RouterID    string `json:"routerId"`
	Label       string `json:"label"`
	Canary      bool   `json:"canary"`
	State       string `json:"state"`
	Step        string `json:"step"`
	Code        string `json:"code"`
	Applied     string `json:"applied"`
	Message     string `json:"message"`
	BackupID    int64  `json:"backupId"`
	ReconnectMS int64  `json:"reconnectMs"`
	Reverted    bool   `json:"reverted"`
	FailedLine  int    `json:"failedLine"`
}

// CfgDeployPayload is the current (or last) deploy, as the Deploy tab draws it.
type CfgDeployPayload struct {
	RunID        string            `json:"runId"`
	State        string            `json:"state"`
	TemplateName string            `json:"templateName"`
	Kind         string            `json:"kind"`
	StartedBy    string            `json:"startedBy"`
	Error        string            `json:"error"`
	Targets      []CfgDeployTarget `json:"targets"`
}

// cfgTargetIn is one router as the page sends it: the values it chose, and
// what its preview showed.
type cfgTargetIn struct {
	RouterID string             `json:"routerId"`
	Values   map[string]string  `json:"values"`
	Hash     string             `json:"hash"`
	Acked    []string           `json:"acked"`
	Override string             `json:"override"`
	Expect   cfgdeploy.Identity `json:"expect"`
}

type cfgStartIn struct {
	TemplateID string        `json:"templateId"`
	Confirm    string        `json:"confirm"`
	Targets    []cfgTargetIn `json:"targets"`
}

type cfgTargetRun struct {
	in    cfgTargetIn
	label string
	row   db.CfgRunTarget
	line  CfgDeployTarget
}

type cfgRun struct {
	id, tplID, tplName, kind string
	t                        *cfgtpl.Template
	defs                     []cfgtpl.VarDef
	source                   cfgtpl.Device
	actor                    Session
	actorIP                  string
	targets                  []*cfgTargetRun
	state, errText           string
	// decide carries the person's answer after the canary: true continues.
	decide    chan bool
	cancelled bool
}

// cfgJob is the one deploy there can be.
type cfgJob struct {
	mu  sync.Mutex
	run *cfgRun
}

func (s *Server) cfgPayload() CfgDeployPayload {
	s.cfg.mu.Lock()
	defer s.cfg.mu.Unlock()
	return cfgPayloadOf(s.cfg.run)
}

func cfgPayloadOf(r *cfgRun) CfgDeployPayload {
	p := CfgDeployPayload{Targets: []CfgDeployTarget{}}
	if r == nil {
		p.State = "idle"
		return p
	}
	p.RunID, p.State, p.TemplateName, p.Kind, p.StartedBy, p.Error = r.id, r.state, r.tplName, r.kind,
		r.actor.Username, r.errText
	for _, t := range r.targets {
		p.Targets = append(p.Targets, t.line)
	}
	return p
}

func (s *Server) cfgBroadcast() { EvCfgDeployState.Broadcast(s.hub, cfgRoom, s.cfgPayload()) }

// cfgRefuseTo answers one socket that its request was refused.
func (cn *conn) cfgRefuseTo(msg string) {
	EvCfgDeployState.Send(cn.srv.hub, cn.c, CfgDeployPayload{State: "refused", Error: msg, Targets: []CfgDeployTarget{}})
}

// cfgWatch joins the progress room and sends where things stand.
func (cn *conn) cfgWatch() {
	if cn.sess == nil || !cn.srv.isGlobalAdmin(cn.sess) {
		cn.cfgRefuseTo("Only an administrator can watch deploys")
		return
	}
	cn.srv.hub.Join(cn.c, cfgRoom)
	EvCfgDeployState.Send(cn.srv.hub, cn.c, cn.srv.cfgPayload())
}

// cfgStart begins a deploy.
func (cn *conn) cfgStart(raw json.RawMessage) {
	if !cn.codeAllowed() {
		cn.recorder().Denied(audit.Event{Action: "config.deploy.start", TargetType: "config-template"})
		cn.cfgRefuseTo("Deploying needs a signed-in administrator")
		return
	}
	var in cfgStartIn
	if err := json.Unmarshal(raw, &in); err != nil || len(in.Targets) == 0 {
		cn.cfgRefuseTo("Choose at least one router")
		return
	}
	s := cn.srv
	run, msg := s.cfgPlan(cn.sess, in)
	if run == nil {
		cn.cfgRefuseTo(msg)
		return
	}
	run.actorIP = cn.clientIP
	s.cfg.mu.Lock()
	if s.cfg.run != nil && !cfgFinished(s.cfg.run.state) {
		s.cfg.mu.Unlock()
		cn.cfgRefuseTo("A deploy is already running; one runs at a time")
		return
	}
	s.cfg.run = run
	s.cfg.mu.Unlock()

	if err := s.cfgRecordStart(run); err != nil {
		log.Printf("[config] recording a run: %v", err)
		s.cfg.mu.Lock()
		s.cfg.run = nil
		s.cfg.mu.Unlock()
		cn.cfgRefuseTo("The run could not be recorded, so nothing was sent")
		return
	}
	cn.recorder().Record(audit.Event{Action: "config.deploy.start", TargetType: "config-template",
		TargetID: run.tplID, TargetName: run.tplName,
		Note: fmt.Sprintf("run %s; %d router(s); canary %s", run.id, len(run.targets), run.targets[0].label)})
	s.hub.Join(cn.c, cfgRoom)
	s.cfgBroadcast()
	go s.cfgExecute(run)
}

// cfgPlan checks a start request and builds its run, or says why not.
func (s *Server) cfgPlan(sess *Session, in cfgStartIn) (*cfgRun, string) {
	if s.auditDB == nil {
		return nil, "The database is unavailable"
	}
	var row *db.CfgTemplate
	if strings.HasPrefix(in.TemplateID, cfgtpl.CannedPrefix) {
		row, _ = cannedRow(in.TemplateID)
	} else if r, err := s.auditDB.CfgTemplate(in.TemplateID); err == nil {
		row = r
	}
	if row == nil {
		return nil, "No such template"
	}
	if row.Kind == cfgtpl.KindFullBinary {
		return nil, "Binary clones cannot be deployed from here yet"
	}
	body, err := s.cfgOpen(row)
	if err != nil {
		return nil, "The template could not be decrypted"
	}
	t, err := cfgtpl.Parse(body)
	if err != nil {
		return nil, "The template no longer parses; open it in the editor"
	}
	var defs []cfgtpl.VarDef
	_ = json.Unmarshal([]byte(row.Variables), &defs)

	ids := make([]string, 0, len(in.Targets))
	byID := map[string]cfgTargetIn{}
	for _, t := range in.Targets {
		if _, dup := byID[t.RouterID]; !dup {
			ids = append(ids, t.RouterID)
			byID[t.RouterID] = t
		}
	}
	ok, over := s.fleetTargets(sess, ids, "config-management", "write")
	if len(over) > 0 {
		return nil, "One deploy reaches at most " + strconv.Itoa(fleetMaxRouters) + " routers"
	}
	if len(ok) == 0 {
		return nil, "None of those routers is available"
	}
	if strings.TrimSpace(in.Confirm) != strings.TrimSpace(ok[0].Label) {
		return nil, "Type the first router's name, " + ok[0].Label + ", to start"
	}
	run := &cfgRun{tplID: row.ID, tplName: row.Name, kind: row.Kind, t: t, defs: defs, actor: *sess,
		state: db.CfgRunCanary, decide: make(chan bool, 1)}
	if row.SourceModel != nil && row.SourceOSVersion != nil {
		run.source = cfgtpl.Device{Board: *row.SourceModel, OSVersion: *row.SourceOSVersion}
	}
	for i, ft := range ok {
		in := byID[ft.ID]
		if in.Hash == "" {
			return nil, ft.Label + " has not been previewed; preview it first"
		}
		tr := &cfgTargetRun{in: in, label: ft.Label}
		tr.row = db.CfgRunTarget{RouterID: ft.ID, Position: i, State: db.CfgTargetPending}
		tr.line = CfgDeployTarget{RouterID: ft.ID, Label: ft.Label, Canary: i == 0, State: db.CfgTargetPending}
		run.targets = append(run.targets, tr)
	}
	id, err := newUUID()
	if err != nil {
		return nil, "A run id could not be made"
	}
	run.id = id
	for _, t := range run.targets {
		t.row.RunID = id
	}
	return run, ""
}

func cfgFinished(state string) bool {
	switch state {
	case db.CfgRunDone, db.CfgRunHalted, db.CfgRunCancelled, db.CfgRunInterrupted, db.CfgRunExpired:
		return true
	}
	return false
}

// cfgRecordStart writes the run and its targets before anything is sent. The
// values are kept WITHOUT the secrets; the body is the template's own text,
// placeholders and all, never a rendered one.
func (s *Server) cfgRecordStart(r *cfgRun) error {
	secret := map[string]bool{}
	for _, d := range r.defs {
		if cfgtpl.Secret(d.Type) {
			secret[d.Name] = true
		}
	}
	vals := map[string]map[string]string{}
	targets := make([]db.CfgRunTarget, len(r.targets))
	for i, t := range r.targets {
		kept := map[string]string{}
		for k, v := range t.in.Values {
			if !secret[k] {
				kept[k] = v
			}
		}
		vals[t.row.RouterID] = kept
		targets[i] = t.row
	}
	vj, _ := json.Marshal(vals)
	canary := r.targets[0].row.RouterID
	text := cfgtpl.Format(r.t)
	return s.auditDB.CreateCfgRun(db.CfgRun{ID: r.id, TemplateID: cfgStoredID(r.tplID), TemplateName: r.tplName,
		Method: cfgMethod(r.kind), BodyMasked: text, Fingerprint: cfgdeploy.Hash(text),
		ValuesJSON: string(vj), State: r.state, CanaryRouterID: &canary,
		CreatedBy: s.userIDFor(r.actor.Username)}, targets)
}

// cfgStoredID is the template id the run row may reference: a canned one is
// not a row, so the run names it and keeps no link.
func cfgStoredID(id string) *string {
	if strings.HasPrefix(id, cfgtpl.CannedPrefix) {
		return nil
	}
	return &id
}

func cfgMethod(kind string) string {
	if kind == cfgtpl.KindFullExport {
		return "reset"
	}
	return "additions"
}

// cfgSet moves the run on, in the database first and then on the wire.
func (s *Server) cfgSet(r *cfgRun, state, errText string) {
	s.cfg.mu.Lock()
	r.state, r.errText = state, errText
	s.cfg.mu.Unlock()
	if err := s.auditDB.SetCfgRunState(r.id, state, errText); err != nil {
		log.Printf("[config] run %s: %v", r.id, err)
	}
	s.cfgBroadcast()
}

// cfgSave writes one target's row and line, then broadcasts.
func (s *Server) cfgSave(t *cfgTargetRun) {
	s.cfg.mu.Lock()
	t.line.State = t.row.State
	if t.row.Step != nil {
		t.line.Step = *t.row.Step
	}
	s.cfg.mu.Unlock()
	if err := s.auditDB.SaveCfgTarget(t.row); err != nil {
		log.Printf("[config] run %s target %s: %v", t.row.RunID, t.row.RouterID, err)
	}
	s.cfgBroadcast()
}

// cfgExecute runs the routers in order: the canary, a person's decision, the
// rest.
func (s *Server) cfgExecute(r *cfgRun) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("[config] run %s panicked: %v", r.id, p)
			s.cfgSet(r, db.CfgRunHalted, "the deploy stopped on an internal error")
		}
	}()
	for i, t := range r.targets {
		if i == 1 {
			s.cfgSet(r, db.CfgRunAwaitingCanary, "")
			if !<-r.decide {
				s.cfgStopRest(r, i, db.CfgRunCancelled, "cancelled after the canary")
				return
			}
			s.cfgSet(r, db.CfgRunRolling, "")
		}
		s.cfg.mu.Lock()
		cancelled := r.cancelled
		s.cfg.mu.Unlock()
		if cancelled {
			s.cfgStopRest(r, i, db.CfgRunCancelled, "cancelled")
			return
		}
		if why := s.cfgStillAllowed(r, t.row.RouterID); why != "" {
			s.cfgStopRest(r, i, db.CfgRunHalted, why)
			return
		}
		out := s.cfgRunOne(r, t)
		if out.State != cfgdeploy.StateApplied {
			s.cfgStopRest(r, i+1, db.CfgRunHalted, t.label+": "+firstNonEmpty(out.Message, out.Code))
			return
		}
	}
	s.cfgSet(r, db.CfgRunDone, "")
}

// cfgStopRest ends the run, marking every router from `from` on as not tried.
func (s *Server) cfgStopRest(r *cfgRun, from int, state, why string) {
	for _, t := range r.targets[from:] {
		t.row.State = db.CfgTargetNotAttempted
		s.cfgSave(t)
	}
	s.cfgSet(r, state, why)
}

// cfgStillAllowed re-checks, before each router, what was true at the start.
func (s *Server) cfgStillAllowed(r *cfgRun, routerID string) string {
	actor := r.actor
	if !cfgMayChange(&actor, s.isGlobalAdmin(&actor)) {
		return "the person who started this deploy is no longer an administrator"
	}
	routers, _ := s.store.Routers()
	for _, x := range routers {
		if x.ID == routerID {
			if x.Disabled {
				return "the router was disabled during the deploy"
			}
			return ""
		}
	}
	return "the router was removed during the deploy"
}

// cfgRunOne changes one router.
func (s *Server) cfgRunOne(r *cfgRun, t *cfgTargetRun) cfgdeploy.Outcome {
	now := time.Now().UnixMilli()
	t.row.State, t.row.StartedAt = db.CfgTargetApplying, &now
	s.cfgSave(t)

	var out cfgdeploy.Outcome
	err := s.inRouterWriteQueueWith(t.row.RouterID, func(sn *session.Session) error {
		live, server := liveOf(sn)
		vals, err := cfgtpl.Resolve(r.defs, t.in.Values, server)
		if err != nil {
			out = cfgdeploy.Outcome{State: cfgdeploy.StatePreflightFailed, Applied: "none", Code: "values",
				Message: err.Error()}
			return nil
		}
		acked := map[string]bool{}
		for _, k := range t.in.Acked {
			acked[k] = true
		}
		env := cfgdeploy.Env{
			Do:     sn.Exec,
			Fresh:  s.cfgFresh(t.row.RouterID),
			Backup: func() (int64, error) { return s.cfgRestorePoint(sn, t.row.RouterID, t.label, r.actor.Username) },
			Step: func(step string) {
				t.row.Step = &step
				s.cfgSave(t)
			},
			Log: func(m string) { log.Printf("[config][%s] %s", t.label, m) },
		}
		if r.kind == cfgtpl.KindFullExport {
			out = cfgdeploy.RunReset(env, cfgdeploy.Reset{Template: r.t, Values: vals, Live: live, Source: r.source,
				Override: t.in.Override, Expect: t.in.Expect, Approved: t.in.Hash, Acked: acked})
		} else {
			out = cfgdeploy.RunAdditions(env, cfgdeploy.Additions{
				Plan:   cfgdeploy.Plan{Template: r.t, Values: vals, Live: live},
				Expect: t.in.Expect, Approved: t.in.Hash, Acked: acked})
		}
		return nil
	})
	if err != nil {
		out = cfgdeploy.Outcome{State: cfgdeploy.StatePreflightFailed, Applied: "none", Code: "unreachable",
			Message: safe.Message(err.Error())}
	}

	end := time.Now().UnixMilli()
	applied, hash := out.Applied, out.Hash
	t.row.State, t.row.FinishedAt, t.row.Applied, t.row.RenderedFingerprint = out.State, &end, &applied, &hash
	if out.BackupID != 0 {
		id := out.BackupID
		t.row.BackupID = &id
	}
	if out.Import.Line != 0 {
		line := out.Import.Line
		t.row.FailedLine = &line
	}
	if out.DryRun != "" {
		dry := out.DryRun
		t.row.DryRunOutput = &dry
	}
	if msg := strings.TrimSpace(out.Import.Message); msg != "" {
		t.row.ImportOutput = &msg
	}
	if out.ReconnectMS != 0 {
		ms := out.ReconnectMS
		t.row.ReconnectMS = &ms
	}
	if out.Message != "" {
		msg := out.Message
		if out.State == cfgdeploy.StateApplied {
			t.row.Warning = &msg
		} else {
			t.row.Error = &msg
		}
	}
	s.cfg.mu.Lock()
	t.line.Code, t.line.Applied, t.line.Message = out.Code, out.Applied, out.Message
	t.line.BackupID, t.line.ReconnectMS, t.line.Reverted, t.line.FailedLine = out.BackupID, out.ReconnectMS,
		out.Reverted, out.Import.Line
	s.cfg.mu.Unlock()
	s.cfgSave(t)

	if out.Baseline != "" {
		runID := r.id
		if err := s.auditDB.SetCfgBaseline(db.CfgBaseline{TemplateID: r.tplID, RouterID: t.row.RouterID, RunID: &runID,
			Body: out.Baseline, Fingerprint: cfgdeploy.Hash(out.Baseline), TakenAt: end}); err != nil {
			log.Printf("[config] baseline for %s: %v", t.label, err)
		}
	}

	outcome := "ok"
	if out.State != cfgdeploy.StateApplied {
		outcome = "error"
	}
	audit.New(auditSinkOf(s), audit.ForUser(s.userIDFor(r.actor.Username), r.actor.Username, r.actorIP), nowMillis).
		Record(audit.Event{Action: "config.deploy", TargetType: "config-template", Scope: "router",
			RouterID: t.row.RouterID, TargetID: r.tplID, TargetName: r.tplName, Outcome: outcome,
			Extra: []audit.KV{{Key: "run", Value: r.id}, {Key: "state", Value: out.State},
				{Key: "code", Value: out.Code}, {Key: "applied", Value: out.Applied},
				{Key: "restorePoint", Value: out.BackupID}, {Key: "fingerprint", Value: out.Hash},
				{Key: "reverted", Value: out.Reverted}}})
	return out
}

func auditSinkOf(s *Server) audit.Sink {
	if s.auditDB == nil {
		return nil
	}
	return auditSink{s.auditDB}
}

// cfgFresh is a NEW login to a router, separate from its session: the only
// proof a deploy left MikroDash able to get in.
func (s *Server) cfgFresh(routerID string) func() (cfgdeploy.Identity, error) {
	return func() (cfgdeploy.Identity, error) {
		routers, _ := s.store.Routers()
		for _, x := range routers {
			if x.ID != routerID {
				continue
			}
			c, err := routeros.Dial(routeros.Config{Host: x.Host, Port: x.Port, Username: x.Username,
				Password: x.Password, TLS: x.TLS, InsecureTLS: x.TLSInsecure, DialTimeout: 8 * time.Second,
				Label: x.Label + " (deploy check)"})
			if err != nil {
				return cfgdeploy.Identity{}, err
			}
			defer c.Close()
			return cfgdeploy.ReadIdentity(c.Do)
		}
		return cfgdeploy.Identity{}, errors.New("the router is no longer in the fleet")
	}
}

// cfgRestorePoint takes the pre-deploy backup through Backups, on the session
// the deploy already holds: its write queue is held, and taking it again would
// wait on itself. An unchanged configuration's restore point is the stored one
// it matches; with none stored there is no restore point, and no deploy.
func (s *Server) cfgRestorePoint(sn *session.Session, routerID, label, actor string) (int64, error) {
	rec := s.backupRecord(routerID)
	if rec.password == "" {
		return 0, errors.New("backups are not enabled for this router, and a deploy needs a restore point")
	}
	if !s.bkClaim(routerID) {
		return 0, errors.New("a backup of this router is already running")
	}
	defer s.bkRelease(routerID)
	res, _, err := backups.RunFor(backups.RunForConfig{
		RouterID: routerID, Label: label, Password: rec.password, DataDir: s.store.Dir,
		Source: "manual", Actor: actor,
		Recorder: bkRecorder{db: s.auditDB, routerID: routerID},
		Pruner:   bkPruner{db: s.auditDB}, Retention: retentionFor(rec),
		Notify: func(kind, title, body string) { s.dispatchBackup(routerID, kind, title, body) },
		Connect: func() (backups.Writer, func(), error) {
			return func(cmd string, args ...string) ([]map[string]string, error) {
				replies, err := sn.Exec(routeros.Cmd{Path: cmd, Args: args, Timeout: backupCmdTimeout})
				out := make([]map[string]string, 0, len(replies))
				for _, rep := range replies {
					out = append(out, map[string]string(rep))
				}
				return out, err
			}, func() {}, nil
		},
		WritePair: backups.WritePair,
		Now:       func() int64 { return time.Now().UnixMilli() },
		Log:       func(m string) { log.Printf("[backup][%s] %s", label, m) },
	})
	if err != nil {
		return 0, err
	}
	if res.Outcome == backups.OutcomeFailed {
		return 0, errors.New(firstNonEmpty(res.Error, "the backup failed"))
	}
	stored, err := s.auditDB.StoredBackups(routerID)
	if err != nil || len(stored) == 0 {
		return 0, errors.New("no stored backup of this router exists to restore from")
	}
	return stored[0].ID, nil
}

// cfgContinue answers the canary: the router count typed back continues.
func (cn *conn) cfgContinue(raw json.RawMessage) {
	if !cn.codeAllowed() {
		cn.recorder().Denied(audit.Event{Action: "config.deploy.continue", TargetType: "config-template"})
		cn.cfgRefuseTo("Continuing needs a signed-in administrator")
		return
	}
	var in struct {
		Confirm string `json:"confirm"`
	}
	_ = json.Unmarshal(raw, &in)
	s := cn.srv
	s.cfg.mu.Lock()
	r := s.cfg.run
	if r == nil || r.state != db.CfgRunAwaitingCanary {
		s.cfg.mu.Unlock()
		cn.cfgRefuseTo("No deploy is waiting on its canary")
		return
	}
	want := strconv.Itoa(len(r.targets))
	s.cfg.mu.Unlock()
	if strings.TrimSpace(in.Confirm) != want {
		cn.cfgRefuseTo("Type " + want + ", the number of routers, to continue")
		return
	}
	cn.recorder().Record(audit.Event{Action: "config.deploy.continue", TargetType: "config-template",
		TargetID: r.tplID, TargetName: r.tplName, Note: "run " + r.id})
	select {
	case r.decide <- true:
	default:
	}
}

// cfgCancel stops the run before its next router. A router being changed is
// finished first: a cancelled import is half-applied (measured).
func (cn *conn) cfgCancel() {
	if !cn.codeAllowed() {
		cn.recorder().Denied(audit.Event{Action: "config.deploy.cancel", TargetType: "config-template"})
		cn.cfgRefuseTo("Cancelling needs a signed-in administrator")
		return
	}
	s := cn.srv
	s.cfg.mu.Lock()
	r := s.cfg.run
	if r == nil || cfgFinished(r.state) {
		s.cfg.mu.Unlock()
		cn.cfgRefuseTo("No deploy is running")
		return
	}
	r.cancelled = true
	s.cfg.mu.Unlock()
	cn.recorder().Record(audit.Event{Action: "config.deploy.cancel", TargetType: "config-template",
		TargetID: r.tplID, TargetName: r.tplName, Note: "run " + r.id})
	select {
	case r.decide <- false:
	default:
	}
}
