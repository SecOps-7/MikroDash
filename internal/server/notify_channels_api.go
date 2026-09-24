package server

// The notification channels REST surface.
//
// ── HAND-BUILT, LIKE EVERY OTHER APP-OWNED LIST ────────────────────────────
//
// `internal/server/resource.go` is the ROUTER write path — permission, validate,
// re-read the menu, is it still the row the operator saw, may it be written.
// None of that applies to a record that never reaches a router, which is what
// `schedule_write.go` says about report schedules. So this follows
// `sites_api.go`: method-prefixed routes, a rate limiter, a permission gate, a
// bounded body, a pure validator, an audit event.
//
// ── WHO MAY TOUCH WHAT ─────────────────────────────────────────────────────
//
// An install channel needs administration. A user's own channel needs only that
// they are that user. The owner is decided by the SERVER from the session, never
// taken from the request, and `UpsertNotifyChannel` leaves `owner` out of its
// update branch — so a channel cannot change hands even if a request asks.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/notify"
	"mikrodash/internal/safe"
)

// maxChannelBody bounds a channel save. A URL list is the biggest thing in it.
const maxChannelBody = 64 << 10

func (s *Server) registerNotifyChannels(mux *http.ServeMux) {
	rw := newRateLimiter(60, time.Minute).limit
	mux.HandleFunc("GET /api/notify-channels", rw(s.notifyChannelsList))
	mux.HandleFunc("GET /api/notify-channels/events", rw(s.notifyChannelEvents))
	mux.HandleFunc("POST /api/notify-channels", rw(s.notifyChannelCreate))
	mux.HandleFunc("PUT /api/notify-channels/{id}", rw(s.notifyChannelUpdate))
	mux.HandleFunc("DELETE /api/notify-channels/{id}", rw(s.notifyChannelDelete))
	// A SEPARATE, TIGHTER LIMIT for the one route that talks to the outside
	// world, matching `registerTestNotification`.
	test := newRateLimiter(10, time.Minute).limit
	mux.HandleFunc("POST /api/notify-channels/{id}/test", test(s.notifyChannelTest))
}

// channelBody is what the browser sends. `Config` is the plain object; it is
// sealed here and never stored as it arrives.
type channelBody struct {
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Enabled bool            `json:"enabled"`
	Config  json.RawMessage `json:"config"`
	Events  []string        `json:"events"`
	Routers []string        `json:"routers"`
	// Owner is accepted only as the word "install"; anything else, including a
	// user id, is ignored. The server decides ownership.
	Owner string `json:"owner"`
}

// channelView is what the browser receives. THE CONFIG IS NEVER SENT BACK.
//
// A webhook URL carries its token, so returning it would put every channel's
// credentials in a page any viewer of Settings can open. The view carries only
// what the card and the form need: how many URLs there are, and for SMTP the
// parts that are not secret.
type channelView struct {
	ID        string   `json:"id"`
	Owner     string   `json:"owner"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Enabled   bool     `json:"enabled"`
	Events    []string `json:"events"`
	Routers   []string `json:"routers"`
	URLCount  int      `json:"urlCount"`
	SMTPHost  string   `json:"smtpHost,omitempty"`
	SMTPFrom  string   `json:"smtpFrom,omitempty"`
	SMTPTo    string   `json:"smtpTo,omitempty"`
	HasSecret bool     `json:"hasSecret"`
	Mine      bool     `json:"mine"`
}

func (s *Server) channelSession(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return nil, false
	}
	if s.auditDB == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "database unavailable")
		return nil, false
	}
	return sess, true
}

// mayTouchChannel is the one ownership rule, in one place so the four write
// routes cannot disagree about it.
func (s *Server) mayTouchChannel(sess *Session, owner string) bool {
	if owner == db.InstallOwner {
		return s.isGlobalAdmin(sess)
	}
	return owner != "" && owner == s.webUserID(sess)
}

func (s *Server) viewOf(sess *Session, c db.NotifyChannel) channelView {
	spec := notify.DecodeChannel(c.ID, c.Name, c.Kind, c.Enabled == 1,
		s.openChannelConfig(c.Config), c.Events, c.Routers)
	v := channelView{
		ID: c.ID, Owner: c.Owner, Name: c.Name, Kind: c.Kind,
		Enabled: c.Enabled == 1, Mine: c.Owner == s.webUserID(sess),
		// NEVER NIL: a null array in a payload is what
		// TestNoServerPayloadSendsANullArray exists to stop.
		Events: spec.Events, Routers: spec.Routers,
	}
	if v.Events == nil {
		v.Events = []string{}
	}
	if v.Routers == nil {
		v.Routers = []string{}
	}
	switch c.Kind {
	case notify.KindWebhook:
		for _, u := range spec.URLs {
			if strings.TrimSpace(u) != "" {
				v.URLCount++
			}
		}
		v.HasSecret = v.URLCount > 0
	case notify.KindSMTP:
		host, _ := spec.Settings["smtpHost"].(string)
		from, _ := spec.Settings["smtpFrom"].(string)
		to, _ := spec.Settings["smtpTo"].(string)
		pass, _ := spec.Settings["smtpPass"].(string)
		v.SMTPHost, v.SMTPFrom, v.SMTPTo = host, from, to
		v.HasSecret = pass != ""
	}
	return v
}

func (s *Server) notifyChannelsList(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.channelSession(w, r)
	if !ok {
		return
	}
	rows, err := s.auditDB.NotifyChannels()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	out := []channelView{}
	for _, c := range rows {
		// A USER SEES THE INSTALL'S CHANNELS AND THEIR OWN, never another
		// person's. Listing everybody's would leak who is on call.
		if c.Owner != db.InstallOwner && c.Owner != s.webUserID(sess) {
			continue
		}
		out = append(out, s.viewOf(sess, c))
	}
	// WHETHER THIS VIEWER MAY OWN AN INSTALL CHANNEL.
	//
	// Without it the browser has to guess, and the first version guessed
	// "install" for everybody — so a non-administrator pressing Add Channel got
	// a 403 and had no way at all to make a channel of their own.
	writeJSON(w, map[string]any{
		"ok": true, "channels": out, "canManageInstall": s.isGlobalAdmin(sess),
	})
}

// notifyChannelEvents is the catalogue the modal draws its toggles from, with
// which events the install currently raises so a new channel can default to
// exactly those.
func (s *Server) notifyChannelEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.channelSession(w, r); !ok {
		return
	}
	// NO `raised` FIELD ANY MORE. It said whether the install raised this event
	// at all, and every event is raised now — the channel is the only thing that
	// decides what is delivered.
	type ev struct {
		Key    string `json:"key"`
		Label  string `json:"label"`
		Desc   string `json:"desc"`
		Backup bool   `json:"backup"`
	}
	out := []ev{}
	for _, t := range alert.Types() {
		out = append(out, ev{Key: t.Key, Label: t.Label, Desc: t.Desc, Backup: t.Backup})
	}
	writeJSON(w, map[string]any{
		"ok": true, "events": out, "schemes": notify.Schemes,
		"defaults": alert.DefaultEvents(),
	})
}

func (s *Server) notifyChannelCreate(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.channelSession(w, r)
	if !ok {
		return
	}
	var body channelBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChannelBody)).
		Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	owner := s.webUserID(sess)
	if body.Owner == "install" {
		owner = db.InstallOwner
	}
	if !s.mayTouchChannel(sess, owner) {
		writeJSONErr(w, http.StatusForbidden, "Administrator access required")
		return
	}
	if err := validateChannel(body); err != nil {
		// `safe.Message` even here: a URL validation error names the scheme the
		// operator typed, and this gate is what stops the next one from naming
		// the whole URL.
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	cfg, err := s.sealChannelConfig(json.RawMessage(body.Config))
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	id, err := newUUID()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UnixMilli()
	rec := db.NotifyChannel{
		ID: id, Owner: owner, Name: strings.TrimSpace(body.Name), Kind: body.Kind,
		Enabled: boolInt(body.Enabled), Config: cfg,
		Events: jsonList(body.Events), Routers: jsonList(body.Routers),
		CreatedBy: s.webUserID(sess), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.auditDB.UpsertNotifyChannel(rec); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "notify.channel.create", TargetType: "notify_channel",
		TargetID: id, TargetName: rec.Name,
	})
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *Server) notifyChannelUpdate(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.channelSession(w, r)
	if !ok {
		return
	}
	cur, found, err := s.auditDB.NotifyChannelByID(r.PathValue("id"))
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	// 404 BEFORE 403, the order `alertAck` follows: the owner is not known
	// until the row is read.
	if !found {
		writeJSONErr(w, http.StatusNotFound, "no such channel")
		return
	}
	if !s.mayTouchChannel(sess, cur.Owner) {
		writeJSONErr(w, http.StatusForbidden, "not permitted")
		return
	}
	var body channelBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChannelBody)).
		Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if err := validateChannel(body); err != nil {
		// `safe.Message` even here: a URL validation error names the scheme the
		// operator typed, and this gate is what stops the next one from naming
		// the whole URL.
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	cfg := cur.Config
	// AN ABSENT CONFIG KEEPS THE STORED ONE. The browser never receives the
	// credentials back, so a form opened and saved without retyping them must
	// not blank them — the rule `SaveSettings` follows with `Kept`.
	if len(body.Config) > 0 && string(body.Config) != "null" {
		if cfg, err = s.sealChannelConfig(json.RawMessage(body.Config)); err != nil {
			writeJSONErrFrom(w, http.StatusInternalServerError, err)
			return
		}
	}
	cur.Name = strings.TrimSpace(body.Name)
	cur.Kind = body.Kind
	cur.Enabled = boolInt(body.Enabled)
	cur.Config = cfg
	cur.Events = jsonList(body.Events)
	cur.Routers = jsonList(body.Routers)
	cur.UpdatedAt = time.Now().UnixMilli()
	if err := s.auditDB.UpsertNotifyChannel(cur); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "notify.channel.update", TargetType: "notify_channel",
		TargetID: cur.ID, TargetName: cur.Name,
	})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) notifyChannelDelete(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.channelSession(w, r)
	if !ok {
		return
	}
	cur, found, err := s.auditDB.NotifyChannelByID(r.PathValue("id"))
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeJSONErr(w, http.StatusNotFound, "no such channel")
		return
	}
	if !s.mayTouchChannel(sess, cur.Owner) {
		writeJSONErr(w, http.StatusForbidden, "not permitted")
		return
	}
	if _, err := s.auditDB.DeleteNotifyChannel(cur.ID); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "notify.channel.delete", TargetType: "notify_channel",
		TargetID: cur.ID, TargetName: cur.Name,
	})
	writeJSON(w, map[string]any{"ok": true})
}

// notifyChannelTest sends one message through a stored channel.
//
// AGAINST WHAT IS STORED, not against what is on the form. The admin test
// endpoint merges unsaved credentials because the four fixed transports are
// edited in place on the Settings page; a channel is saved first and tested
// after, so the stored record is what the operator is actually asking about.
func (s *Server) notifyChannelTest(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.channelSession(w, r)
	if !ok {
		return
	}
	cur, found, err := s.auditDB.NotifyChannelByID(r.PathValue("id"))
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeJSONErr(w, http.StatusNotFound, "no such channel")
		return
	}
	if !s.mayTouchChannel(sess, cur.Owner) {
		writeJSONErr(w, http.StatusForbidden, "not permitted")
		return
	}
	// Enabled is forced on: testing a switched-off channel is exactly what an
	// operator does while setting one up.
	spec := notify.DecodeChannel(cur.ID, cur.Name, cur.Kind, true,
		s.openChannelConfig(cur.Config), cur.Events, cur.Routers)
	if !spec.Deliverable() {
		writeJSONErr(w, http.StatusBadRequest, "this channel has no destination configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if spec.Kind == notify.KindWebhook {
		err = notify.SendURLs(ctx, notify.DefaultClient, spec.URLs,
			notify.TestTitle, notify.TestBody)
	} else {
		err = notify.Send(ctx, notify.DefaultClient, spec.Settings,
			s.alertMailer(spec.Settings), notify.TestTitle, notify.TestBody)
	}
	if err != nil {
		// `safe.Message`, the rule `testNotification` follows: a provider's
		// error may quote back what was sent.
		writeJSONErr(w, http.StatusInternalServerError, safe.Message(err.Error()))
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// validateChannel is the pure half, so the rules can be read in one place.
func validateChannel(b channelBody) error {
	if strings.TrimSpace(b.Name) == "" {
		return errors.New("a channel needs a name")
	}
	if b.Kind != notify.KindWebhook && b.Kind != notify.KindSMTP {
		return errors.New("unknown channel type")
	}
	for _, e := range b.Events {
		if _, ok := alert.TypeByKey(e); !ok {
			return errors.New("unknown event: " + e)
		}
	}
	if b.Kind == notify.KindWebhook && len(b.Config) > 0 {
		var cfg struct {
			URLs []string `json:"urls"`
		}
		if err := json.Unmarshal(b.Config, &cfg); err != nil {
			return errors.New("malformed webhook configuration")
		}
		for _, u := range cfg.URLs {
			if strings.TrimSpace(u) == "" {
				continue
			}
			// REFUSED HERE, while the operator is looking at the form. A URL
			// nothing can send would otherwise be stored and deliver silently
			// nothing until an incident.
			if err := notify.Validate(u); err != nil {
				return err
			}
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// jsonList marshals a string list, never as `null`.
func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(out)
}
