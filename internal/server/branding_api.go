package server

// The install's branding: its name, the wordmark font and its icon. See
// internal/branding for what is stored and why (issue #131).
//
// ── READS ARE PUBLIC, WRITES ARE AN ADMINISTRATOR'S ─────────────────────────
//
// The login page shows the name and icon before anybody signs in, and the
// browser tab shows them on every page, so `GET /api/branding` and the icon need
// no session. They disclose what the top-left corner of the app already shows
// to anyone who can load the login page. Saving goes through the same gate as
// Settings (`maySaveSettings`), and every change is audited.

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"

	"mikrodash/internal/audit"
	"mikrodash/internal/branding"
	"mikrodash/internal/reportpdf"
	"mikrodash/internal/safe"
)

func (s *Server) registerBranding(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/branding", s.brandingGet)
	mux.HandleFunc("GET /brand/icon.png", s.brandingIcon)
	mux.HandleFunc("PUT /api/branding", s.brandingSave)
	mux.HandleFunc("POST /api/branding/icon", s.brandingIconSave)
	mux.HandleFunc("DELETE /api/branding/icon", s.brandingIconClear)
}

// brandingView is what the browser is told.
type brandingView struct {
	// Name is the stored name, empty for the default.
	Name string `json:"name"`
	// DisplayName is what to show: Name, or MikroDash.
	DisplayName string `json:"displayName"`
	Font        string `json:"font"`
	// Icon is the URL to load: the custom icon with its version, or the default.
	Icon       string `json:"icon"`
	CustomIcon bool   `json:"customIcon"`
}

func viewOf(b branding.Branding) brandingView {
	// NOT named `icon`: internal/verify's endpoint check reads every
	// `name = "/path"` in this package as a route constant, and would substitute
	// this one inside "/api/branding/icon".
	iconURL := "/logo.png"
	if b.IconVersion != 0 {
		iconURL = "/brand/icon.png?v=" + strconv.FormatInt(b.IconVersion, 10)
	}
	return brandingView{Name: b.Name, DisplayName: b.DisplayName(), Font: b.Font,
		Icon: iconURL, CustomIcon: b.IconVersion != 0}
}

func (s *Server) brandingDir() string {
	if s.store == nil {
		return ""
	}
	return s.store.Dir
}

// branding is the stored branding, or the default when there is none or it
// cannot be read. A broken file must not take the wordmark or a report with it.
func (s *Server) branding() branding.Branding {
	dir := s.brandingDir()
	if dir == "" {
		return branding.Branding{}
	}
	b, err := branding.Load(dir)
	if err != nil {
		log.Printf("[branding] %v; using the default", err)
		return branding.Branding{}
	}
	return b
}

// appName is what report emails call the app.
func (s *Server) appName() string { return s.branding().DisplayName() }

// reportBrand is the PDF header's branding: the install's name and icon, or the
// zero value, which draws the MikroDash wordmark.
func (s *Server) reportBrand() reportpdf.Brand {
	b := s.branding()
	out := reportpdf.Brand{Name: b.Name}
	if b.IconVersion != 0 {
		out.Icon = branding.ReadIcon(s.brandingDir())
	}
	return out
}

func (s *Server) brandingGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, viewOf(s.branding()))
}

// brandingIcon serves the custom icon, or the default when there is none.
func (s *Server) brandingIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	if dir := s.brandingDir(); dir != "" && s.branding().IconVersion != 0 {
		if raw := branding.ReadIcon(dir); raw != nil {
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(raw)
			return
		}
	}
	if full, ok := s.staticPath("/logo.png"); ok {
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			http.ServeFile(w, r, full)
			return
		}
	}
	http.NotFound(w, r)
}

// brandingAdmin is the write gate: a signed-in session that may save Settings.
func (s *Server) brandingAdmin(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return nil, false
	}
	if !s.maySaveSettings(sess) {
		writeJSONErr(w, http.StatusForbidden, "Administrator access required")
		return nil, false
	}
	if s.brandingDir() == "" {
		writeJSONErr(w, http.StatusServiceUnavailable, "data directory unavailable")
		return nil, false
	}
	return sess, true
}

func (s *Server) brandingSave(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.brandingAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
		Font string `json:"font"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	name, err := branding.CleanName(body.Name)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	font, err := branding.CleanFont(body.Font)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	prev := s.branding()
	b, err := branding.SetText(s.brandingDir(), name, font)
	if err != nil {
		log.Printf("[branding] save: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not save the branding")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "branding.update", TargetType: "branding", TargetName: b.DisplayName(),
		Before: map[string]any{"name": prev.Name, "font": prev.Font},
		After:  map[string]any{"name": b.Name, "font": b.Font},
	})
	writeJSON(w, viewOf(b))
}

func (s *Server) brandingIconSave(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.brandingAdmin(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, branding.MaxIconBytes))
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "the icon is larger than "+
			strconv.Itoa(branding.MaxIconBytes>>10)+" KB")
		return
	}
	icon, err := branding.Icon(raw)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	b, err := branding.SetIcon(s.brandingDir(), icon)
	if err != nil {
		log.Printf("[branding] icon: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not save the icon")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "branding.icon", TargetType: "branding", TargetName: b.DisplayName(),
	})
	writeJSON(w, viewOf(b))
}

func (s *Server) brandingIconClear(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.brandingAdmin(w, r)
	if !ok {
		return
	}
	b, err := branding.ClearIcon(s.brandingDir())
	if err != nil {
		log.Printf("[branding] icon reset: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not reset the icon")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "branding.icon.reset", TargetType: "branding", TargetName: b.DisplayName(),
	})
	writeJSON(w, viewOf(b))
}
