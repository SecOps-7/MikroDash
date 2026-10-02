package server

// MikroDash login profiles: /api/credentials/logins/…
//
// One password for the account MikroDash signs in with - user `mikrodash` in
// group `mikrodash` - shared by every device linked to the profile. Linking a
// device CREATES that account on it; changing the password CHANGES it on every
// linked device. See `internal/loginprof` for why this is the one path allowed
// to write MikroDash's own account, and `store/loginprofiles.go` for how a
// linked device's record reads the credential.
//
// ── EVERY ROUTE IS A SIGNED-IN GLOBAL ADMINISTRATOR'S ───────────────────────
//
// `cfgRead`/`cfgWrite`, as the credential profiles beside it. A login profile
// is one secret that opens every linked device at full privilege, so even
// LINKING one device is an administrator's act: whoever could point a device
// at the profile could also point it at a host they run and receive the
// password when MikroDash signs in. `routerUpdate` closes the other half of
// that - a device already using a profile cannot have its endpoint changed by
// anyone else.
//
// ── NOTHING IS SWITCHED UNTIL THE NEW LOGIN HAS SIGNED IN ───────────────────
//
// A device moves to the profile only after a FRESH connection with the new
// credential has read the router. A password change is all or nothing: pushed
// to every linked device, each confirmed the same way, and the profile's stored
// password changes only when every one holds it; any failure puts the old one
// back everywhere it had already reached.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/loginprof"
	"mikrodash/internal/routeros"
	"mikrodash/internal/session"
	"mikrodash/internal/store"
)

// loginOp is the last or running job on one profile, held in memory: the
// router records are the durable truth (a device is linked or it is not), and
// this only says what the most recent attempt did.
type loginOp struct {
	Kind    string                   `json:"kind"` // "link" or "password"
	Running bool                     `json:"running"`
	Started int64                    `json:"started"`
	Results map[string]loginOpResult `json:"results"`
	Summary string                   `json:"summary"`
}

type loginOpResult struct {
	State   string `json:"state"` // "pending", "working", "done", "failed", "skipped"
	Message string `json:"message"`
}

type loginOps struct {
	mu  sync.Mutex
	ops map[string]*loginOp
}

// loginProfileView is one profile as the browser sees it: never the password.
type loginProfileView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Username  string   `json:"username"`
	Group     string   `json:"group"`
	HasSecret bool     `json:"hasSecret"`
	Devices   []string `json:"devices"`
	UpdatedAt int64    `json:"updatedAt"`
	Op        *loginOp `json:"op"`
}

// loginVerifyTimeout bounds the fresh sign-in that confirms a new credential.
const loginVerifyTimeout = 12 * time.Second

func (s *Server) registerLoginProfileAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/credentials/logins", s.cfgRead(s.loginList))
	mux.HandleFunc("POST /api/credentials/logins", s.cfgWrite("loginprofile.create", s.loginCreate))
	mux.HandleFunc("PUT /api/credentials/logins/{id}", s.cfgWrite("loginprofile.update", s.loginUpdate))
	mux.HandleFunc("DELETE /api/credentials/logins/{id}", s.cfgWrite("loginprofile.delete", s.loginDelete))
	mux.HandleFunc("POST /api/credentials/logins/{id}/devices", s.cfgWrite("loginprofile.link", s.loginLink))
	mux.HandleFunc("POST /api/credentials/logins/{id}/devices/{routerId}/own",
		s.cfgWrite("loginprofile.unlink", s.loginUnlink))
}

func (s *Server) loginOpOf(id string) *loginOp {
	s.loginJobs.mu.Lock()
	defer s.loginJobs.mu.Unlock()
	op := s.loginJobs.ops[id]
	if op == nil {
		return nil
	}
	cp := *op
	cp.Results = make(map[string]loginOpResult, len(op.Results))
	for k, v := range op.Results {
		cp.Results[k] = v
	}
	return &cp
}

// loginStart claims a profile for one job; false while another runs.
func (s *Server) loginStart(id, kind string, routers []string) (*loginOp, bool) {
	s.loginJobs.mu.Lock()
	defer s.loginJobs.mu.Unlock()
	if s.loginJobs.ops == nil {
		s.loginJobs.ops = map[string]*loginOp{}
	}
	if op := s.loginJobs.ops[id]; op != nil && op.Running {
		return nil, false
	}
	op := &loginOp{Kind: kind, Running: true, Started: nowMillis(), Results: map[string]loginOpResult{}}
	for _, r := range routers {
		op.Results[r] = loginOpResult{State: "pending"}
	}
	s.loginJobs.ops[id] = op
	return op, true
}

func (s *Server) loginSet(op *loginOp, routerID, state, msg string) {
	s.loginJobs.mu.Lock()
	op.Results[routerID] = loginOpResult{State: state, Message: msg}
	s.loginJobs.mu.Unlock()
}

func (s *Server) loginFinish(op *loginOp, summary string) {
	s.loginJobs.mu.Lock()
	op.Running, op.Summary = false, summary
	s.loginJobs.mu.Unlock()
}

func (s *Server) loginList(w http.ResponseWriter, _ *http.Request, _ *Session) {
	list, _ := s.store.LoginProfiles()
	out := make([]loginProfileView, 0, len(list))
	for _, p := range list {
		devices, _ := s.store.LoginProfileUsers(p.ID)
		out = append(out, loginProfileView{
			ID: p.ID, Name: p.Name, Username: store.LoginUserName, Group: store.LoginGroupName,
			HasSecret: p.Encrypted != "", Devices: devices, UpdatedAt: p.UpdatedAt, Op: s.loginOpOf(p.ID),
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	writeJSON(w, map[string]any{"logins": out})
}

func loginErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrLoginProfileInvalid):
		writeJSONErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), store.ErrLoginProfileInvalid.Error()+": "))
	case errors.Is(err, store.ErrLoginProfileInUse):
		writeJSONErr(w, http.StatusConflict, "Devices still sign in with this profile. Switch them to their own login first.")
	default:
		writeJSONErr(w, http.StatusInternalServerError, "the login profile could not be saved")
	}
}

func (s *Server) loginCreate(w http.ResponseWriter, r *http.Request, sess *Session) {
	var in struct{ Name, Password string }
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeJSONErr(w, http.StatusBadRequest, "could not read the request")
		return
	}
	p, err := s.store.AddLoginProfile(in.Name, in.Password)
	if err != nil {
		loginErr(w, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "loginprofile.create", TargetType: "login-profile",
		TargetID: p.ID, TargetName: p.Name, After: map[string]any{"username": store.LoginUserName}})
	writeJSON(w, map[string]any{"ok": true, "id": p.ID})
}

// loginUpdate renames, and/or starts a password change across every device.
func (s *Server) loginUpdate(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	var in struct{ Name, Password string }
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeJSONErr(w, http.StatusBadRequest, "could not read the request")
		return
	}
	cur, err := s.store.LoginProfile(id)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, "no such login profile")
		return
	}
	if in.Password != "" {
		if err := store.ValidLoginPassword(in.Password); err != nil {
			loginErr(w, err)
			return
		}
	}
	if strings.TrimSpace(in.Name) != "" && strings.TrimSpace(in.Name) != cur.Name {
		if err := s.store.RenameLoginProfile(id, in.Name); err != nil {
			loginErr(w, err)
			return
		}
		s.httpRecorder(r, sess).Record(audit.Event{Action: "loginprofile.update", TargetType: "login-profile",
			TargetID: id, TargetName: in.Name, Before: map[string]any{"name": cur.Name},
			After: map[string]any{"name": in.Name}})
	}
	if in.Password == "" || in.Password == cur.Password {
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	devices, err := s.store.LoginProfileUsers(id)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read the devices")
		return
	}
	op, ok := s.loginStart(id, "password", devices)
	if !ok {
		writeJSONErr(w, http.StatusConflict, "This profile is already being changed. Wait for that to finish.")
		return
	}
	rec := s.httpRecorder(r, sess)
	go s.loginRotate(op, id, cur, in.Password, devices, rec)
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

func (s *Server) loginDelete(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	cur, err := s.store.LoginProfile(id)
	if err != nil && cur.ID == "" {
		writeJSONErr(w, http.StatusNotFound, "no such login profile")
		return
	}
	if err := s.store.DeleteLoginProfile(id); err != nil {
		loginErr(w, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "loginprofile.delete", TargetType: "login-profile",
		TargetID: id, TargetName: cur.Name})
	writeJSON(w, map[string]any{"ok": true})
}

// loginLink starts linking devices: each one gains the account and, once its
// new login has signed in, switches to the profile.
func (s *Server) loginLink(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	p, err := s.store.LoginProfile(id)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, "no such login profile, or its password cannot be opened")
		return
	}
	var in struct {
		RouterIDs []string `json:"routerIds"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeJSONErr(w, http.StatusBadRequest, "could not read the request")
		return
	}
	all, _ := s.store.Routers()
	known := map[string]store.Router{}
	for _, rt := range all {
		known[rt.ID] = rt
	}
	var targets []string
	for _, rid := range in.RouterIDs {
		// AN UNKNOWN OR DISABLED DEVICE IS DROPPED, and one already on this
		// profile has nothing to do.
		if rt, ok := known[rid]; ok && !rt.Disabled && rt.LoginProfileID != id {
			targets = append(targets, rid)
		}
	}
	if len(targets) == 0 {
		writeJSON(w, map[string]any{"ok": true, "started": false})
		return
	}
	op, ok := s.loginStart(id, "link", targets)
	if !ok {
		writeJSONErr(w, http.StatusConflict, "This profile is already being changed. Wait for that to finish.")
		return
	}
	rec := s.httpRecorder(r, sess)
	go s.loginLinkAll(op, p, targets, rec)
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

func (s *Server) loginLinkAll(op *loginOp, p store.LoginProfile, targets []string, rec *audit.Recorder) {
	done := 0
	for _, rid := range targets {
		s.loginSet(op, rid, "working", "")
		if err := s.loginLinkOne(p, rid); err != nil {
			s.loginSet(op, rid, "failed", err.Error())
			s.loginAudit(rec, "loginprofile.link", p, rid, err)
			continue
		}
		done++
		s.loginSet(op, rid, "done", "")
		s.loginAudit(rec, "loginprofile.link", p, rid, nil)
	}
	s.loginFinish(op, fmt.Sprintf("%d of %d device(s) now sign in with %s", done, len(targets), p.Name))
	s.broadcastRouterList()
}

// loginLinkOne puts the account on one device, proves it, then switches.
func (s *Server) loginLinkOne(p store.LoginProfile, routerID string) error {
	rt := s.routerRecord(routerID)
	if rt == nil {
		return errors.New("the device is gone")
	}
	ctx, cancel := context.WithTimeout(context.Background(), fleetDeadline)
	defer cancel()
	sn, drop, ok := s.fleetSession(ctx, routerID, "loginprofile")
	if !ok {
		return errors.New("the device did not connect with its current login, so nothing was changed")
	}
	defer drop()
	oldPw := rt.Password
	var change loginprof.Change
	var putErr error
	_ = sn.InWriteQueue(func() error {
		change, putErr = loginprof.Put(sn, p.Name, p.Password, sn.Username())
		return nil
	})
	if putErr != nil {
		_ = sn.InWriteQueue(func() error { return loginprof.Undo(sn, change, oldPw) })
		return fmt.Errorf("the router refused the account: %w", putErr)
	}
	if err := s.loginVerify(*rt, p.Password); err != nil {
		var undoErr error
		_ = sn.InWriteQueue(func() error { undoErr = loginprof.Undo(sn, change, oldPw); return nil })
		msg := "the new login could not sign in (" + err.Error() + "), so the change was undone"
		if undoErr != nil {
			msg += "; undoing it ALSO failed: " + undoErr.Error()
		}
		return errors.New(msg)
	}
	if err := s.store.UseLoginProfile(routerID, p.ID); err != nil {
		_ = sn.InWriteQueue(func() error { return loginprof.Undo(sn, change, oldPw) })
		return fmt.Errorf("the device record could not be saved, so the change was undone: %w", err)
	}
	s.reconfigureLiveSession(routerID)
	return nil
}

// loginVerify signs in afresh as MikroDash with `password` on the device's own
// endpoint, and reads one row. Only a working login counts.
func (s *Server) loginVerify(rt store.Router, password string) error {
	c, err := routeros.Dial(routeros.Config{
		Host: rt.Host, Port: rt.Port, TLS: rt.TLS, InsecureTLS: rt.TLSInsecure,
		Username: store.LoginUserName, Password: password, DialTimeout: loginVerifyTimeout,
		Label: rt.Label + " (login check)",
	})
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.Do(routeros.Cmd{Path: "/system/identity/print"}); err != nil {
		return err
	}
	return nil
}

// loginRotate changes the password on every linked device, all or nothing.
func (s *Server) loginRotate(op *loginOp, id string, p store.LoginProfile, newPw string,
	devices []string, rec *audit.Recorder) {
	type held struct {
		rt   store.Router
		sn   *session.Session
		drop func()
	}
	var hs []held
	defer func() {
		for _, h := range hs {
			h.drop()
		}
	}()
	// EVERY DEVICE MUST BE REACHABLE FIRST. One that is off would keep the old
	// password while the profile moved on, and would be locked out when it
	// came back.
	for _, rid := range devices {
		rt := s.routerRecord(rid)
		ctx, cancel := context.WithTimeout(context.Background(), fleetDeadline)
		sn, drop, ok := s.fleetSession(ctx, rid, "loginprofile")
		cancel()
		if rt == nil || !ok {
			for _, other := range devices {
				if other != rid {
					s.loginSet(op, other, "skipped", "")
				}
			}
			s.loginSet(op, rid, "failed", "did not connect")
			s.loginFinish(op, "Nothing was changed: a device using this profile did not connect. "+
				"Bring it online, or switch it to its own login, and try again.")
			s.loginAudit(rec, "loginprofile.password", p, rid, errors.New("a device did not connect; nothing changed"))
			return
		}
		hs = append(hs, held{rt: *rt, sn: sn, drop: drop})
	}

	var changed []held
	rollback := func() {
		for _, h := range changed {
			_ = h.sn.InWriteQueue(func() error { return loginprof.SetPassword(h.sn, p.Password) })
		}
	}
	for i, h := range hs {
		s.loginSet(op, h.rt.ID, "working", "")
		var err error
		_ = h.sn.InWriteQueue(func() error { err = loginprof.SetPassword(h.sn, newPw); return nil })
		if err == nil {
			changed = append(changed, h)
			err = s.loginVerify(h.rt, newPw)
		}
		if err != nil {
			rollback()
			s.loginSet(op, h.rt.ID, "failed", err.Error())
			// Every OTHER device is back where it started: the ones already done
			// were rolled back, the rest were never touched.
			for j, rest := range hs {
				if j != i {
					s.loginSet(op, rest.rt.ID, "skipped", "")
				}
			}
			s.loginFinish(op, "Nothing was changed: "+h.rt.Label+" did not accept the new password, "+
				"so the old one was put back everywhere.")
			s.loginAudit(rec, "loginprofile.password", p, h.rt.ID, err)
			return
		}
		s.loginSet(op, h.rt.ID, "done", "")
	}
	if err := s.store.SetLoginProfilePassword(id, newPw); err != nil {
		rollback()
		s.loginFinish(op, "Nothing was changed: the new password could not be saved.")
		return
	}
	for _, h := range hs {
		s.reconfigureLiveSession(h.rt.ID)
	}
	s.loginAudit(rec, "loginprofile.password", p, "", nil)
	s.loginFinish(op, fmt.Sprintf("The password changed on %d device(s)", len(hs)))
}

// loginUnlink switches a device back to a login of its own. The account the
// profile put on it is LEFT: it is still the router's, and removing it is a
// separate decision for whoever runs that router.
func (s *Server) loginUnlink(w http.ResponseWriter, r *http.Request, sess *Session) {
	id, rid := r.PathValue("id"), r.PathValue("routerId")
	var in struct{ Username, Password string }
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeJSONErr(w, http.StatusBadRequest, "could not read the request")
		return
	}
	rt := s.routerRecord(rid)
	if rt == nil || rt.LoginProfileID != id {
		writeJSONErr(w, http.StatusNotFound, "that device does not use this profile")
		return
	}
	if err := s.store.UseOwnLogin(rid, in.Username, in.Password); err != nil {
		loginErr(w, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "loginprofile.unlink", TargetType: "login-profile",
		TargetID: id, TargetName: rt.LoginProfileName, RouterID: rid,
		After: map[string]any{"username": strings.TrimSpace(in.Username)}})
	s.reconfigureLiveSession(rid)
	s.broadcastRouterList()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) loginAudit(rec *audit.Recorder, action string, p store.LoginProfile, routerID string, err error) {
	ev := audit.Event{Action: action, TargetType: "login-profile", TargetID: p.ID, TargetName: p.Name,
		RouterID: routerID, After: map[string]any{"username": store.LoginUserName}}
	if err != nil {
		ev.After["error"] = err.Error()
		ev.After["outcome"] = "failed"
	}
	rec.Record(ev)
}
