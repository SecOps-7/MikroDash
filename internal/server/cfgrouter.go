package server

// Config Management's two reads of a router: capturing a template from one,
// and previewing a template against one. Neither changes the router, apart
// from the export file a capture writes and sweeps.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/backups"
	"mikrodash/internal/cfgdeploy"
	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/db"
	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
	"mikrodash/internal/session"
)

func (s *Server) registerConfigRouter(mux *http.ServeMux) {
	// A capture writes a file on the router and a row here: a change, with its
	// own small budget, since each one exports from a router.
	mux.HandleFunc("POST "+cfgPrefix+"capture",
		newRateLimiter(10, time.Minute).limit(s.cfgWrite("config.template.capture", s.cfgCapture)))
	// A preview reads the router and renders. Read-only, but it holds a router
	// channel per call, so it is limited too.
	mux.HandleFunc("POST "+cfgPrefix+"templates/{id}/preview",
		newRateLimiter(30, time.Minute).limit(s.cfgRead(s.cfgPreview)))
}

// cfgRouter holds one router for a Config Management request. The returned
// func releases the hold and must be called.
func (s *Server) cfgRouter(ctx context.Context, sess *Session, routerID, reason string) (
	*session.Session, func(), bool) {
	if ok, _ := s.fleetTargets(sess, []string{routerID}, "config-management", "write"); len(ok) != 1 {
		return nil, func() {}, false
	}
	return s.fleetSession(ctx, routerID, reason)
}

// liveOf is what the analyser and the server variables need about one router:
// where it sees MikroDash from, on which interfaces, over which service.
func liveOf(sn *session.Session) (cfgtpl.LiveContext, map[string]string) {
	path := managementPathOf(sn)
	svc := "api"
	if sn.UsesTLS() {
		svc = "api-ssl"
	}
	live := cfgtpl.LiveContext{
		FW: guard.FWContext{Resolved: path.Resolved, Addresses: path.Addresses,
			Interfaces: path.Interfaces, APIPort: sn.APIPort()},
		APIService: svc,
	}
	server := map[string]string{"api_service": svc, "api_user": sn.Username()}
	if path.Address != "" {
		server["mgmt_src"] = path.Address
	}
	return live, server
}

// cfgCaptureIn names what to capture.
type cfgCaptureIn struct {
	RouterID string `json:"routerId"`
	Name     string `json:"name"`
	// Kind is fragment (one menu, for additions) or full-export (the whole
	// router, for full replacement).
	Kind string `json:"kind"`
	// Menu is the fragment's menu, `/ip/firewall/filter`.
	Menu string `json:"menu"`
}

// exportError is the line RouterOS writes into an export for a menu that did
// not answer in time (since 7.11), before moving on. It is a comment, so the
// parser drops it; a capture must not, or the template silently lacks a menu.
var exportError = regexp.MustCompile(`(?m)^#\s*error exporting "?([^"\s]+)"?`)

// checkCapture turns an export's text into a template, or says why it cannot
// be one. Pure: the router has already been read.
func checkCapture(text string) (*cfgtpl.Template, error) {
	if m := exportError.FindStringSubmatch(text); m != nil {
		return nil, &cfgInputError{Msg: "the router did not export " + m[1] +
			" in time, so the capture would be missing it; try again"}
	}
	t, err := cfgtpl.Parse(text)
	if err != nil {
		var pe *cfgtpl.ParseError
		if errors.As(err, &pe) {
			return nil, &cfgInputError{Msg: "the export holds a line MikroDash's template dialect cannot " +
				"represent: " + pe.Msg, Line: pe.Line}
		}
		return nil, &cfgInputError{Msg: err.Error()}
	}
	if len(t.Lines) == 0 {
		return nil, &cfgInputError{Msg: "the router exported nothing from that menu"}
	}
	return t, nil
}

// captureSource is where one capture is exported from, and with which flags.
func captureSource(kind, menu string) (path string, args []string, err error) {
	switch kind {
	case cfgtpl.KindFragment:
		if !cfgtpl.KnownMenu(menu) || cfgtpl.RefusedMenu(menu) {
			return "", nil, &cfgInputError{Msg: "that is not a menu MikroDash can capture"}
		}
		// NOT show-sensitive: a fragment is shared and edited, and a
		// credential in it would be stored in the clear.
		return menu + "/export", nil, nil
	case cfgtpl.KindFullExport:
		// show-sensitive, and SEALED when stored: a full replacement must put
		// back the router's secrets too.
		return "/export", []string{"=show-sensitive="}, nil
	}
	return "", nil, &cfgInputError{Msg: "a capture is a fragment or a full export"}
}

func (s *Server) cfgCapture(w http.ResponseWriter, r *http.Request, sess *Session) {
	var in cfgCaptureIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "the request is not a capture")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 80 {
		writeJSONErr(w, http.StatusUnprocessableEntity, "a template needs a name of 1 to 80 characters")
		return
	}
	path, args, err := captureSource(in.Kind, in.Menu)
	if err != nil {
		cfgRefuse(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	sn, drop, ok := s.cfgRouter(ctx, sess, in.RouterID, "config-capture")
	defer drop()
	if !ok {
		writeJSONErr(w, http.StatusNotFound, "that router is not available")
		return
	}
	var text string
	var id cfgdeploy.Identity
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
		if id, err = cfgdeploy.ReadIdentity(sn.Exec); err != nil {
			return err
		}
		base, err := cfgtpl.NewBaseName()
		if err != nil {
			return err
		}
		text, err = backups.ExportText(wr, path, base, time.Now, time.Sleep, args...)
		return err
	})
	if err != nil {
		writeJSONErrFrom(w, http.StatusBadGateway, err)
		return
	}
	t, err := checkCapture(text)
	if err != nil {
		cfgRefuse(w, err)
		return
	}
	// Written back from the parse: the identifying comments an export opens
	// with (serial number, software id) never reach the database.
	body := cfgtpl.Format(t)
	stored, sealed, err := s.cfgSeal(in.Kind, body)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	tid, err := newUUID()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	scope, _ := json.Marshal(t.Menus())
	src, model, ver := in.RouterID, id.Board, id.OSVersion
	row := db.CfgTemplate{ID: tid, Name: name, Kind: in.Kind, Scope: string(scope), Body: stored,
		Sealed: sealed, Variables: "[]", Fingerprint: cfgdeploy.Hash(body), SourceRouterID: &src,
		SourceModel: &model, SourceOSVersion: &ver, CreatedBy: s.userIDFor(sess.Username)}
	if err := s.auditDB.CreateCfgTemplate(row); err != nil {
		if !cfgNameTaken(w, err) {
			writeJSONErrFrom(w, http.StatusInternalServerError, err)
		}
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "config.template.capture", TargetType: "config-template",
		RouterID: in.RouterID, TargetID: tid, TargetName: name,
		Note: in.Kind + " " + in.Menu + "; fingerprint " + row.Fingerprint})
	findings := cfgtpl.Analyze(t, profileFor(in.Kind))
	if findings == nil {
		findings = []cfgtpl.Finding{}
	}
	writeJSON(w, map[string]any{"ok": true, "id": tid, "lines": len(t.Lines), "findings": findings})
}

// cfgPreviewIn is a template's values for one router.
type cfgPreviewIn struct {
	RouterID string            `json:"routerId"`
	Values   map[string]string `json:"values"`
}

// cfgPreview renders a template for one router and analyses it there: what a
// deploy would send, its hash (which the deploy must be given back), and every
// finding. Nothing is written.
func (s *Server) cfgPreview(w http.ResponseWriter, r *http.Request, sess *Session) {
	t, ok := s.cfgLoad(w, r.PathValue("id"))
	if !ok {
		return
	}
	var in cfgPreviewIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "the request is not a preview")
		return
	}
	if t.Kind == cfgtpl.KindFullBinary {
		writeJSONErr(w, http.StatusUnprocessableEntity, "a binary backup has no text to preview")
		return
	}
	body, err := s.cfgOpen(t)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the template could not be decrypted")
		return
	}
	pt, err := cfgtpl.Parse(body)
	if err != nil {
		cfgRefuse(w, &cfgInputError{Msg: "the stored template no longer parses"})
		return
	}
	var defs []cfgtpl.VarDef
	_ = json.Unmarshal([]byte(t.Variables), &defs)

	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	sn, drop, ok := s.cfgRouter(ctx, sess, in.RouterID, "config-preview")
	defer drop()
	if !ok {
		writeJSONErr(w, http.StatusNotFound, "that router is not available")
		return
	}
	live, server := liveOf(sn)
	pt, vals, err := cfgtpl.Bind(pt, defs, in.Values, server)
	if err != nil {
		var fe cfgtpl.FieldErrors
		if errors.As(err, &fe) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "some values are not valid",
				"fields": fe})
			return
		}
		cfgRefuse(w, &cfgInputError{Msg: "the values could not be resolved"})
		return
	}
	id, err := cfgdeploy.ReadIdentity(sn.Exec)
	if err != nil {
		writeJSONErrFrom(w, http.StatusBadGateway, err)
		return
	}
	target := map[string]any{"board": id.Board, "serial": id.Serial, "osVersion": id.OSVersion}
	if t.Kind == cfgtpl.KindFullExport {
		prep, err := cfgdeploy.PrepareReset(cfgdeploy.Reset{Template: pt, Values: vals, Live: live})
		if err != nil {
			cfgRefuse(w, &cfgInputError{Msg: safe.Message(err.Error())})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "kind": t.Kind, "target": target, "reset": prep})
		return
	}
	prep, err := cfgdeploy.Prepare(sn.Exec, cfgdeploy.Plan{Template: pt, Values: vals, Live: live})
	if err != nil {
		cfgRefuse(w, &cfgInputError{Msg: safe.Message(err.Error())})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "kind": t.Kind, "target": target, "prepared": prep,
		"lockClass": cfgdeploy.LockClass(prep.Findings)})
}
