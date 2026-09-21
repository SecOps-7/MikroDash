package server

// Config Management's templates, over REST.
//
// ── WHO MAY DO WHAT ─────────────────────────────────────────────────────────
//
// Reading the library needs a global administrator. Changing it needs a
// SIGNED-IN one (codeAllowed's rule): a template is text a router will run, so
// editing one is writing code an administrator deploys later, and
// isGlobalAdmin answers true with sign-in switched off. A refused write is
// audited; a refused read is not, as for every other GET here.
//
// ── A TEMPLATE IS CHECKED WHEN IT IS SAVED ──────────────────────────────────
//
// checkTemplate parses the body, holds the variables and their placeholders to
// each other, and runs the analyser. A body that does not parse, or whose
// variables disagree with it, is not stored: it could never be deployed, and
// the editor shows the line. The analyser's findings are returned and are NOT
// a reason to refuse the save: a template that needs an acknowledgement, or
// holds a refused line its author is still working on, is still a draft worth
// keeping. The deploy is where findings decide.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/cfgdeploy"
	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/db"
)

const cfgPrefix = "/api/config/"

// cfgMaxBody bounds one template's text: what the editor may hold, and what
// one request may carry.
const cfgMaxBody = 1 << 20

func (s *Server) registerConfig(mux *http.ServeMux) {
	mux.HandleFunc("GET "+cfgPrefix+"templates", s.cfgRead(s.cfgList))
	mux.HandleFunc("GET "+cfgPrefix+"templates/{id}", s.cfgRead(s.cfgGet))
	lim := newRateLimiter(30, time.Minute).limit
	mux.HandleFunc("POST "+cfgPrefix+"templates", lim(s.cfgWrite("config.template.create", s.cfgCreate)))
	mux.HandleFunc("PUT "+cfgPrefix+"templates/{id}", lim(s.cfgWrite("config.template.update", s.cfgUpdate)))
	mux.HandleFunc("DELETE "+cfgPrefix+"templates/{id}", lim(s.cfgWrite("config.template.delete", s.cfgDelete)))
	mux.HandleFunc("POST "+cfgPrefix+"templates/{id}/clone", lim(s.cfgWrite("config.template.clone", s.cfgClone)))
}

// cfgRead is a global administrator's GET.
func (s *Server) cfgRead(h func(http.ResponseWriter, *http.Request, *Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.auth.Validate(r.Header.Get("Cookie"))
		if err != nil {
			writeJSONErr(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if !s.isGlobalAdmin(sess) {
			writeJSONErr(w, http.StatusForbidden, "Administrator access required")
			return
		}
		if s.auditDB == nil {
			writeJSONErr(w, http.StatusServiceUnavailable, "the database is unavailable")
			return
		}
		h(w, r, sess)
	}
}

// cfgWrite is a signed-in global administrator's change. A refusal is audited
// under the action it would have been.
func (s *Server) cfgWrite(action string, h func(http.ResponseWriter, *http.Request, *Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.auth.Validate(r.Header.Get("Cookie"))
		if err != nil {
			writeJSONErr(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if !cfgMayChange(sess, s.isGlobalAdmin(sess)) {
			s.httpRecorder(r, sess).Denied(audit.Event{Action: action, TargetType: "config-template",
				TargetID: r.PathValue("id")})
			writeJSONErr(w, http.StatusForbidden, "Changing templates needs a signed-in administrator")
			return
		}
		if s.auditDB == nil {
			writeJSONErr(w, http.StatusServiceUnavailable, "the database is unavailable")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, cfgMaxBody+64<<10)
		h(w, r, sess)
	}
}

// cfgMayChange is codeAllowed for an HTTP session: signed in, and a global
// administrator. Sign-in switched off answers no, whatever isGlobalAdmin says.
func cfgMayChange(sess *Session, admin bool) bool {
	return sess != nil && sess.AuthMode != "none" && admin
}

// cfgTemplateIn is what the editor sends.
type cfgTemplateIn struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Body        string          `json:"body"`
	Variables   []cfgtpl.VarDef `json:"variables"`
	// Revision is the one the editor read; an update against any other is
	// refused rather than saved over somebody else's.
	Revision int `json:"revision"`
}

// cfgChecked is a template that may be stored.
type cfgChecked struct {
	Name, Description, Body string
	Scope, Variables        string
	Fingerprint             string
	Findings                []cfgtpl.Finding
}

// cfgInputError is a refusal the editor can point at: a message, and the line
// when there is one.
type cfgInputError struct {
	Msg  string
	Line int
}

func (e *cfgInputError) Error() string { return e.Msg }

// checkTemplate is the whole decision about whether a template may be saved,
// with no database and no router: body, name and variables in, a template to
// store or the reason out.
func checkTemplate(in cfgTemplateIn, profile string) (cfgChecked, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 80 {
		return cfgChecked{}, &cfgInputError{Msg: "a template needs a name of 1 to 80 characters"}
	}
	if len(in.Description) > 500 {
		return cfgChecked{}, &cfgInputError{Msg: "the description is over 500 characters"}
	}
	if len(in.Body) > cfgMaxBody {
		return cfgChecked{}, &cfgInputError{Msg: "the template is over 1 MB"}
	}
	t, err := cfgtpl.Parse(in.Body)
	if err != nil {
		var pe *cfgtpl.ParseError
		if errors.As(err, &pe) {
			return cfgChecked{}, &cfgInputError{Msg: pe.Msg, Line: pe.Line}
		}
		return cfgChecked{}, &cfgInputError{Msg: err.Error()}
	}
	if len(t.Lines) == 0 {
		return cfgChecked{}, &cfgInputError{Msg: "the template has no commands"}
	}
	defs := in.Variables
	if defs == nil {
		defs = []cfgtpl.VarDef{}
	}
	if err := cfgtpl.ValidateDefs(defs); err != nil {
		return cfgChecked{}, &cfgInputError{Msg: err.Error()}
	}
	if err := cfgtpl.CheckDeclared(t, defs); err != nil {
		return cfgChecked{}, &cfgInputError{Msg: err.Error()}
	}
	scope, _ := json.Marshal(t.Menus())
	vars, _ := json.Marshal(defs)
	findings := cfgtpl.Analyze(t, profile)
	if findings == nil {
		findings = []cfgtpl.Finding{}
	}
	return cfgChecked{
		Name: name, Description: strings.TrimSpace(in.Description), Body: in.Body,
		Scope: string(scope), Variables: string(vars),
		// Of the parse, not of the text: a comment or a line break changes
		// nothing a router would receive, and must not read as an edit to drift.
		Fingerprint: cfgdeploy.Hash(cfgtpl.Format(t)),
		Findings:    findings,
	}, nil
}

// profileFor is the analyser profile a kind of template is judged by.
func profileFor(kind string) string {
	if kind == cfgtpl.KindFragment {
		return cfgtpl.Additions
	}
	return cfgtpl.Full
}

// cfgView is a template as the page receives it.
type cfgView struct {
	db.CfgTemplate
	Body     string           `json:"body"`
	Findings []cfgtpl.Finding `json:"findings"`
}

func (s *Server) cfgList(w http.ResponseWriter, _ *http.Request, _ *Session) {
	rows, err := s.auditDB.CfgTemplates()
	if err != nil {
		log.Printf("[config] templates: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the templates")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "templates": rows, "canned": cannedViews()})
}

// cannedView is a shipped template as the Library lists it.
type cannedView struct {
	cfgtpl.Canned
	// LockClass is whether deploying it arms the dead-man: it holds a change
	// that could cut MikroDash off, judged as if MikroDash's address on the
	// router were unknown, which is the most a list can know.
	LockClass bool     `json:"lockClass"`
	Scope     []string `json:"scope"`
}

func cannedViews() []cannedView {
	out := []cannedView{}
	for _, c := range cfgtpl.CannedTemplates() {
		v := cannedView{Canned: c, Scope: []string{}}
		if t, err := cfgtpl.Parse(c.Body); err == nil {
			v.Scope = t.Menus()
			v.LockClass = cfgdeploy.LockClass(cfgtpl.AnalyzeLive(t, cfgtpl.LiveContext{}))
		}
		out = append(out, v)
	}
	return out
}

// cannedRow is a shipped template in the shape a stored one has, so reading,
// previewing and cloning treat the two alike. Its revision is its version.
func cannedRow(id string) (*db.CfgTemplate, bool) {
	c, ok := cfgtpl.CannedByID(strings.TrimPrefix(id, cfgtpl.CannedPrefix))
	if !ok || !strings.HasPrefix(id, cfgtpl.CannedPrefix) {
		return nil, false
	}
	scope := "[]"
	if t, err := cfgtpl.Parse(c.Body); err == nil {
		b, _ := json.Marshal(t.Menus())
		scope = string(b)
	}
	vars, _ := json.Marshal(c.Variables)
	return &db.CfgTemplate{ID: id, Name: c.Name, Description: c.Description, Kind: cfgtpl.KindFragment,
		Scope: scope, Body: c.Body, Variables: string(vars), Fingerprint: cfgdeploy.Hash(c.Body),
		Revision: c.Version}, true
}

func (s *Server) cfgGet(w http.ResponseWriter, r *http.Request, _ *Session) {
	t, ok := s.cfgLoad(w, r.PathValue("id"))
	if !ok {
		return
	}
	body, err := s.cfgOpen(t)
	if err != nil {
		log.Printf("[config] opening template %s: %v", t.ID, err)
		writeJSONErr(w, http.StatusInternalServerError, "the template could not be decrypted")
		return
	}
	v := cfgView{CfgTemplate: *t, Body: body, Findings: []cfgtpl.Finding{}}
	if pt, err := cfgtpl.Parse(body); err == nil {
		if fs := cfgtpl.Analyze(pt, profileFor(t.Kind)); fs != nil {
			v.Findings = fs
		}
	}
	writeJSON(w, map[string]any{"ok": true, "template": v})
}

// cfgLoad reads a template or answers 404.
func (s *Server) cfgLoad(w http.ResponseWriter, id string) (*db.CfgTemplate, bool) {
	if strings.HasPrefix(id, cfgtpl.CannedPrefix) {
		t, ok := cannedRow(id)
		if !ok {
			writeJSONErr(w, http.StatusNotFound, "no such template")
		}
		return t, ok
	}
	t, err := s.auditDB.CfgTemplate(id)
	if err != nil {
		log.Printf("[config] template %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the template")
		return nil, false
	}
	if t == nil {
		writeJSONErr(w, http.StatusNotFound, "no such template")
		return nil, false
	}
	return t, true
}

// cfgOpen is a template's text, unsealed. A full export is stored encrypted
// with the store's key, because it was captured with show-sensitive.
func (s *Server) cfgOpen(t *db.CfgTemplate) (string, error) {
	if !t.Sealed {
		return t.Body, nil
	}
	return s.store.Decrypt(t.Body)
}

// cfgSeal is the inverse, for the kind that is stored sealed.
func (s *Server) cfgSeal(kind, body string) (string, bool, error) {
	if kind != cfgtpl.KindFullExport {
		return body, false, nil
	}
	sealed, err := s.store.Encrypt(body)
	return sealed, true, err
}

// cfgDecode reads the editor's request, answering 400 itself.
func cfgDecode(w http.ResponseWriter, r *http.Request) (cfgTemplateIn, bool) {
	var in cfgTemplateIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "the request is not a template")
		return in, false
	}
	return in, true
}

// cfgRefuse answers a template that may not be stored, with its line.
func cfgRefuse(w http.ResponseWriter, err error) {
	var ie *cfgInputError
	if errors.As(err, &ie) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": ie.Msg, "line": ie.Line})
		return
	}
	writeJSONErrFrom(w, http.StatusInternalServerError, err)
}

// cfgNameTaken answers the one constraint the database enforces itself.
func cfgNameTaken(w http.ResponseWriter, err error) bool {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		writeJSONErr(w, http.StatusConflict, "a template with that name already exists")
		return true
	}
	return false
}

func (s *Server) cfgCreate(w http.ResponseWriter, r *http.Request, sess *Session) {
	in, ok := cfgDecode(w, r)
	if !ok {
		return
	}
	c, err := checkTemplate(in, cfgtpl.Additions)
	if err != nil {
		cfgRefuse(w, err)
		return
	}
	id, err := newUUID()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	t := db.CfgTemplate{ID: id, Name: c.Name, Description: c.Description, Kind: cfgtpl.KindFragment,
		Scope: c.Scope, Body: c.Body, Variables: c.Variables, Fingerprint: c.Fingerprint,
		CreatedBy: s.userIDFor(sess.Username)}
	if err := s.auditDB.CreateCfgTemplate(t); err != nil {
		if !cfgNameTaken(w, err) {
			writeJSONErrFrom(w, http.StatusInternalServerError, err)
		}
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "config.template.create", TargetType: "config-template",
		TargetID: id, TargetName: c.Name, Note: "fingerprint " + c.Fingerprint})
	writeJSON(w, map[string]any{"ok": true, "id": id, "findings": c.Findings})
}

// cfgShipped answers a change aimed at a canned template: a release owns it,
// and a clone is how to change it.
func cfgShipped(w http.ResponseWriter, id string) bool {
	if strings.HasPrefix(id, cfgtpl.CannedPrefix) {
		writeJSONErr(w, http.StatusForbidden, "a canned template ships with MikroDash; clone it to change it")
		return true
	}
	return false
}

func (s *Server) cfgUpdate(w http.ResponseWriter, r *http.Request, sess *Session) {
	if cfgShipped(w, r.PathValue("id")) {
		return
	}
	t, ok := s.cfgLoad(w, r.PathValue("id"))
	if !ok {
		return
	}
	in, ok := cfgDecode(w, r)
	if !ok {
		return
	}
	c, err := checkTemplate(in, profileFor(t.Kind))
	if err != nil {
		cfgRefuse(w, err)
		return
	}
	body, sealed, err := s.cfgSeal(t.Kind, c.Body)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	next := *t
	next.Name, next.Description, next.Scope, next.Body, next.Sealed = c.Name, c.Description, c.Scope, body, sealed
	next.Variables, next.Fingerprint = c.Variables, c.Fingerprint
	switch err := s.auditDB.UpdateCfgTemplate(next, in.Revision); {
	case errors.Is(err, db.ErrCfgStale):
		writeJSONErr(w, http.StatusConflict, "someone else changed this template since you opened it; reload it")
		return
	case cfgNameTaken(w, err):
		return
	case err != nil:
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "config.template.update", TargetType: "config-template",
		TargetID: t.ID, TargetName: c.Name, Note: "fingerprint " + t.Fingerprint + " -> " + c.Fingerprint})
	writeJSON(w, map[string]any{"ok": true, "findings": c.Findings})
}

func (s *Server) cfgDelete(w http.ResponseWriter, r *http.Request, sess *Session) {
	if cfgShipped(w, r.PathValue("id")) {
		return
	}
	t, ok := s.cfgLoad(w, r.PathValue("id"))
	if !ok {
		return
	}
	if _, err := s.auditDB.DeleteCfgTemplate(t.ID); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "config.template.delete", TargetType: "config-template",
		TargetID: t.ID, TargetName: t.Name, Note: "revision " + strconv.Itoa(t.Revision)})
	writeJSON(w, map[string]any{"ok": true})
}

// cfgClone copies a template as a new custom one, recording where it came from.
func (s *Server) cfgClone(w http.ResponseWriter, r *http.Request, sess *Session) {
	t, ok := s.cfgLoad(w, r.PathValue("id"))
	if !ok {
		return
	}
	id, err := newUUID()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	c := *t
	c.ID, c.CreatedBy = id, s.userIDFor(sess.Username)
	// Where it came from: a canned id already carries its prefix, a stored one
	// is marked as such. The editor offers a compare when the source moves on.
	src := "template:" + t.ID
	if strings.HasPrefix(t.ID, cfgtpl.CannedPrefix) {
		src = t.ID
	}
	base := src + "@" + strconv.Itoa(t.Revision)
	c.Baseline = &base
	for n := 1; ; n++ {
		c.Name = cloneName(t.Name, n)
		err = s.auditDB.CreateCfgTemplate(c)
		if err == nil || !strings.Contains(err.Error(), "UNIQUE") || n >= 50 {
			break
		}
	}
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "config.template.clone", TargetType: "config-template",
		TargetID: id, TargetName: c.Name, Note: "from " + t.Name})
	writeJSON(w, map[string]any{"ok": true, "id": id, "name": c.Name})
}

// cloneName is the nth name tried for a copy, kept within the 80 a name may
// hold.
func cloneName(name string, n int) string {
	suffix := " (copy)"
	if n > 1 {
		suffix = " (copy " + strconv.Itoa(n) + ")"
	}
	if len(name)+len(suffix) > 80 {
		name = strings.TrimSpace(name[:80-len(suffix)])
	}
	return name + suffix
}
