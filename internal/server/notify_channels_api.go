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
	"fmt"
	"net/http"
	"strings"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/notify"
	"mikrodash/internal/reports"
	"mikrodash/internal/safe"
)

// maxChannelBody bounds a channel save. A URL list is the biggest thing in it.
const maxChannelBody = 64 << 10

func (s *Server) registerNotifyChannels(mux *http.ServeMux) {
	rw := newRateLimiter(60, time.Minute).limit
	mux.HandleFunc("GET /api/notify-channels", rw(s.notifyChannelsList))
	mux.HandleFunc("GET /api/notify-channels/events", rw(s.notifyChannelEvents))
	mux.HandleFunc("GET /api/notify-channels/{id}", rw(s.notifyChannelOne))
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
	// IfaceTypes narrows Interface Up/Down. EMPTY IS ALL, like Routers.
	IfaceTypes []string `json:"ifaceTypes"`
	// Tuning is this channel's own thresholds and cooldown. A zero in any of
	// them means "the default", which is what `notify.decodeTuning` reads back.
	Tuning notify.Tuning `json:"tuning"`
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
	ID         string        `json:"id"`
	Owner      string        `json:"owner"`
	Name       string        `json:"name"`
	Kind       string        `json:"kind"`
	Enabled    bool          `json:"enabled"`
	Events     []string      `json:"events"`
	Routers    []string      `json:"routers"`
	IfaceTypes []string      `json:"ifaceTypes"`
	Tuning     notify.Tuning `json:"tuning"`
	URLCount   int           `json:"urlCount"`
	SMTPHost   string        `json:"smtpHost,omitempty"`
	SMTPFrom   string        `json:"smtpFrom,omitempty"`
	SMTPTo     string        `json:"smtpTo,omitempty"`
	SMTPCc     string        `json:"smtpCc,omitempty"`
	SMTPBcc    string        `json:"smtpBcc,omitempty"`
	HasSecret  bool          `json:"hasSecret"`
	Mine       bool          `json:"mine"`
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
		s.openChannelConfig(c.Config), c.Events, c.Routers, c.IfaceTypes, c.Tuning)
	v := channelView{
		ID: c.ID, Owner: c.Owner, Name: c.Name, Kind: c.Kind,
		Enabled: c.Enabled == 1, Mine: c.Owner == s.webUserID(sess),
		// NEVER NIL: a null array in a payload is what
		// TestNoServerPayloadSendsANullArray exists to stop.
		Events: spec.Events, Routers: spec.Routers, IfaceTypes: spec.IfaceTypes,
		Tuning: spec.Tuning,
	}
	if v.Events == nil {
		v.Events = []string{}
	}
	if v.Routers == nil {
		v.Routers = []string{}
	}
	// NEVER NULL, for the same reason the two above are not: a Go nil slice
	// marshals as JSON `null`, and the page would then have to handle a third
	// state that means exactly what the empty array means.
	if v.IfaceTypes == nil {
		v.IfaceTypes = []string{}
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
		cc, _ := spec.Settings["smtpCc"].(string)
		bcc, _ := spec.Settings["smtpBcc"].(string)
		pass, _ := spec.Settings["smtpPass"].(string)
		v.SMTPHost, v.SMTPFrom = host, from
		v.SMTPTo, v.SMTPCc, v.SMTPBcc = to, cc, bcc
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

// notifyChannelOne returns ONE channel WITH its configuration, including the
// webhook URLs and the SMTP password.
//
// ── WHY THIS IS A SEPARATE ROUTE FROM THE LIST ────────────────────────────
//
// The list is shown to every signed-in viewer — it carries the install's
// channels so anyone can see what exists — so it must never contain a
// credential. This one is gated on `mayTouchChannel`: the same test that decides
// who may EDIT the channel. Somebody who can rewrite the URL gains nothing by
// being shown it, and cannot manage a destination they are not allowed to read.
//
// The alternative, masking the secret inside the URL, was considered and
// rejected: `tgram://••••••••/-100` cannot be copied, corrected or diffed, so an
// operator fixing a typo would have to retype a token they cannot see.
func (s *Server) notifyChannelOne(w http.ResponseWriter, r *http.Request) {
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
	spec := notify.DecodeChannel(cur.ID, cur.Name, cur.Kind, cur.Enabled == 1,
		s.openChannelConfig(cur.Config), cur.Events, cur.Routers, cur.IfaceTypes, cur.Tuning)

	out := map[string]any{"ok": true, "channel": s.viewOf(sess, cur)}
	if spec.URLs == nil {
		spec.URLs = []string{}
	}
	out["urls"] = spec.URLs
	if cur.Kind == notify.KindSMTP {
		out["smtp"] = map[string]any{
			"host": spec.Settings["smtpHost"], "port": spec.Settings["smtpPort"],
			"secure": spec.Settings["smtpSecure"], "user": spec.Settings["smtpUser"],
			"pass": spec.Settings["smtpPass"],
			"from": spec.Settings["smtpFrom"], "to": spec.Settings["smtpTo"],
			"cc": spec.Settings["smtpCc"], "bcc": spec.Settings["smtpBcc"],
		}
	}
	writeJSON(w, out)
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
		IfaceTypes: jsonList(body.IfaceTypes), Tuning: jsonTuning(body.Tuning),
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
	cur.IfaceTypes = jsonList(body.IfaceTypes)
	cur.Tuning = jsonTuning(body.Tuning)
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
		s.openChannelConfig(cur.Config), cur.Events, cur.Routers, cur.IfaceTypes, cur.Tuning)
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
		usable := 0
		for _, u := range cfg.URLs {
			if strings.TrimSpace(u) == "" {
				continue
			}
			usable++
			// REFUSED HERE, while the operator is looking at the form. A URL
			// nothing can send would otherwise be stored and deliver silently
			// nothing until an incident.
			if err := notify.Validate(u); err != nil {
				return err
			}
		}
		// A CONFIG THAT CLEARS EVERY URL IS REFUSED. The form shows the stored
		// URLs now, so an empty box is a deliberate act rather than the
		// "unchanged" it used to mean — and a webhook channel with no
		// destination is one that reports every alert as delivered while
		// sending nothing.
		if usable == 0 {
			return errors.New("a webhook channel needs at least one URL")
		}
	}
	// ── A THRESHOLD UNDER THE RECORDING FLOOR WOULD NEVER FIRE ───────────
	//
	// Nothing below `alert.FloorCPU` or `alert.FloorPingLoss` is recorded at
	// all, so a channel asking for less would look configured and stay silent
	// for ever — the exact failure the whole per-channel design exists to make
	// visible. REFUSED, not clamped: clamping stores a number the operator did
	// not choose and never tells them.
	//
	// Zero is not refused, because zero means "use the default" everywhere else
	// in this feature and `decodeTuning` reads it that way.
	if b.Tuning.CPU != 0 && b.Tuning.CPU < alert.FloorCPU {
		return fmt.Errorf("the CPU threshold cannot be below %.0f%%, which is where "+
			"MikroDash starts recording", alert.FloorCPU)
	}
	if b.Tuning.PingLoss != 0 && b.Tuning.PingLoss < alert.FloorPingLoss {
		return fmt.Errorf("the ping loss threshold cannot be below %.0f%%, which is "+
			"where MikroDash starts recording", alert.FloorPingLoss)
	}
	if b.Tuning.CPU > 100 || b.Tuning.PingLoss > 100 {
		return errors.New("a threshold is a percentage and cannot be above 100")
	}
	if b.Tuning.CooldownSec < 0 || b.Tuning.CooldownSec > 86400 {
		return errors.New("the cooldown must be between 0 seconds and a day")
	}

	// ── A MAIL CHANNEL'S RECIPIENTS ARE THE ONLY RECIPIENT LIST THERE IS ──
	//
	// A scheduled report names a channel and these are the people who get it, so
	// the address rules that used to guard a schedule's own list now guard this
	// field. Same rules, same limits, same injection check — `reports` still owns
	// them, and the corpus that records them still pins them.
	//
	// ONLY WHEN A CONFIG IS SENT. An absent `config` means "keep what is stored",
	// which is what makes editing a channel without retyping its password
	// possible; validating an absent field would refuse every such edit.
	if b.Kind == notify.KindSMTP && len(b.Config) > 0 {
		var cfg struct {
			To  string `json:"to"`
			Cc  string `json:"cc"`
			Bcc string `json:"bcc"`
		}
		if err := json.Unmarshal(b.Config, &cfg); err != nil {
			return errors.New("malformed mail configuration")
		}
		// EACH LIST IS CHECKED SEPARATELY, so the message names the field the
		// operator has to fix rather than "a recipient is not an email address"
		// against three boxes.
		filled := 0
		for _, f := range []struct{ label, raw string }{
			{"To", cfg.To}, {"Cc", cfg.Cc}, {"Bcc", cfg.Bcc},
		} {
			if strings.TrimSpace(f.raw) == "" {
				continue
			}
			filled++
			if _, err := reports.CleanRecipients(splitList(f.raw)); err != nil {
				return fmt.Errorf("%s: %w", f.label, err)
			}
		}
		// ANY ONE OF THE THREE IS ENOUGH. A message addressed only to Bcc is how
		// a list reaches people without disclosing them to each other, and it is
		// what every scheduled report did before the choice existed.
		if filled == 0 {
			return errors.New("a mail channel needs at least one recipient in To, Cc or Bcc")
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

// jsonTuning marshals a channel's tuning, never as `null`.
func jsonTuning(t notify.Tuning) string {
	b, err := json.Marshal(t)
	if err != nil {
		return "{}"
	}
	return string(b)
}
