package server

// Zero-touch provisioning: applying a device's template once it is a router.
//
// ── CONFIG MANAGEMENT'S OWN DEPLOY, WITHOUT A BROWSER ───────────────────────
//
// A pre-provisioned device's template goes through exactly the pipeline an
// operator's deploy does: cfgPlan (the actor's permission on the router), the
// one-deploy-at-a-time slot, cfgRecordStart (the run and its target written
// before anything is sent), cfgExecute (restore point, dead-man, fresh-login
// proof, the actor re-checked before the router). Two things differ, both
// because the device could not be reached when the wizard ran:
//
//   - THE PREVIEW HAPPENS NOW. A deploy carries the hash of a preview the
//     operator saw; here the preview is made on arrival, on the live router,
//     and the run carries its hash, so what is sent is still exactly what was
//     analysed.
//   - ACKNOWLEDGEMENTS ARE BY CODE. The wizard cannot know a finding's line
//     (it depends on the live router), so it records the codes the operator
//     accepted ("may cut MikroDash off: firewall"). Each live finding with an
//     accepted code is acknowledged; a REFUSE finding never is, and one whose
//     code was not accepted stops the deploy, which the device then shows.
//
// The actor is the operator who pre-provisioned the device (or approved it),
// who must still be a signed-in-capable global administrator when it runs.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/cfgdeploy"
	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/db"
	"mikrodash/internal/safe"
)

// ztpSlotWait is how long a provisioning deploy waits for another deploy to
// finish before giving up.
const ztpSlotWait = 10 * time.Minute

// ztpActor is the operator a device's deploy runs as, by the user ID recorded
// when it was pre-provisioned or approved.
func (s *Server) ztpActor(userID string) (*Session, bool) {
	users, err := s.store.Users()
	if err != nil {
		return nil, false
	}
	for _, u := range users {
		if u.ID == userID {
			return &Session{Username: u.Username, AuthMode: "modern"}, true
		}
	}
	return nil, false
}

func (s *Server) ztpProvision(id string) {
	d, err := s.auditDB.ZTPDevice(id)
	if err != nil || d.RouterID == nil || d.TemplateID == nil {
		return
	}
	finish := func(state, why string) {
		d.State = state
		d.Error = nil
		if why != "" {
			d.Error = &why
		}
		_ = s.auditDB.SaveZTPDevice(*d)
		s.ztpChanged()
	}
	actor, ok := s.ztpActor(d.CreatedBy)
	if !ok || !s.isGlobalAdmin(actor) {
		finish(db.ZTPFailed, "the person who pre-provisioned this device is no longer an administrator, so its template was not applied")
		return
	}
	var values map[string]string
	_ = json.Unmarshal([]byte(d.ValuesJSON), &values)
	var accepted []string
	_ = json.Unmarshal([]byte(d.AckedJSON), &accepted)
	okCode := map[string]bool{}
	for _, c := range accepted {
		okCode[c] = true
	}

	// THE PREVIEW, NOW, on the live router.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sn, drop, reached := s.cfgRouter(ctx, actor, *d.RouterID, "ztp-provision")
	if !reached {
		drop()
		finish(db.ZTPFailed, "the router could not be reached to apply its template")
		return
	}
	var row *db.CfgTemplate
	if strings.HasPrefix(*d.TemplateID, cfgtpl.CannedPrefix) {
		row, _ = cannedRow(*d.TemplateID)
	} else if r, err := s.auditDB.CfgTemplate(*d.TemplateID); err == nil {
		row = r
	}
	if row == nil || row.Kind != cfgtpl.KindFragment {
		drop()
		finish(db.ZTPFailed, "its template no longer exists, or is not one that adds configuration")
		return
	}
	body, err := s.cfgOpen(row)
	pt, perr := cfgtpl.Parse(body)
	if err != nil || perr != nil {
		drop()
		finish(db.ZTPFailed, "its template could not be read")
		return
	}
	var defs []cfgtpl.VarDef
	_ = json.Unmarshal([]byte(row.Variables), &defs)
	live, server := liveOf(sn)
	bound, vals, err := cfgtpl.Bind(pt, defs, values, server)
	if err != nil {
		drop()
		finish(db.ZTPFailed, "its template's settings are not valid for this router: "+safe.Message(err.Error()))
		return
	}
	ident, err := cfgdeploy.ReadIdentity(sn.Exec)
	if err != nil {
		drop()
		finish(db.ZTPFailed, "the router's identity could not be read")
		return
	}
	prep, err := cfgdeploy.Prepare(sn.Exec, cfgdeploy.Plan{Template: bound, Values: vals, Live: live})
	drop()
	if err != nil {
		finish(db.ZTPFailed, safe.Message(err.Error()))
		return
	}
	var acked []string
	for _, f := range prep.Findings {
		if f.Level == cfgtpl.Ack && okCode[f.Code] {
			acked = append(acked, cfgdeploy.FindingKey(f))
		}
	}
	label := d.Label
	if rec := s.routerRecord(*d.RouterID); rec != nil {
		label = rec.Label
	}
	run, msg := s.cfgPlan(actor, cfgStartIn{TemplateID: *d.TemplateID, Confirm: label,
		Targets: []cfgTargetIn{{RouterID: *d.RouterID, Values: values, Hash: prep.Hash, Acked: acked, Expect: ident}}})
	if run == nil {
		finish(db.ZTPFailed, msg)
		return
	}
	run.actorIP = "zero-touch provisioning"

	// ONE DEPLOY AT A TIME, as for an operator: wait for the slot.
	deadline := time.Now().Add(ztpSlotWait)
	for {
		s.cfg.mu.Lock()
		free := s.cfg.run == nil || cfgFinished(s.cfg.run.state)
		if free {
			s.cfg.run = run
		}
		s.cfg.mu.Unlock()
		if free {
			break
		}
		if time.Now().After(deadline) {
			finish(db.ZTPFailed, "another deploy was running for too long; apply the template from Config Management")
			return
		}
		time.Sleep(15 * time.Second)
	}
	if err := s.cfgRecordStart(run); err != nil {
		s.cfg.mu.Lock()
		s.cfg.run = nil
		s.cfg.mu.Unlock()
		finish(db.ZTPFailed, "the run could not be recorded, so nothing was sent")
		return
	}
	d.RunID = &run.id
	_ = s.auditDB.SaveZTPDevice(*d)
	s.auditSystem(audit.Event{Action: "config.deploy.start", TargetType: "config-template", TargetID: run.tplID,
		TargetName: run.tplName, RouterID: *d.RouterID,
		Note: "run " + run.id + "; zero-touch provisioning of " + label + ", as " + actor.Username})
	s.cfgBroadcast()
	s.cfgExecute(run)

	s.cfg.mu.Lock()
	state, why := run.state, run.errText
	s.cfg.mu.Unlock()
	if state == db.CfgRunDone {
		finish(db.ZTPProvisioned, "")
		return
	}
	finish(db.ZTPFailed, firstNonEmpty(why, "the template was not applied"))
}
