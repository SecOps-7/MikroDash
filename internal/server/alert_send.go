package server

import (
	"context"
	"log"
	"strings"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/alertdispatch"
	"mikrodash/internal/mailer"
	"mikrodash/internal/notify"
)

// THE ALERT CALLER — live's `alerter.js` fan-out, and the piece that was missing.
//
// ── WHAT WAS WRONG BEFORE THIS FILE ───────────────────────────────────────
//
// `Evaluate()` returns `[]Fired` and BOTH call sites discarded it, while
// `srv.dispatch` was assigned in `New` and never read. So every alert was
// recorded and none was ever sent, for as long as `-alert-dispatch` has
// existed — while the flag's own startup line promised the opposite. LOOP.md 0k.
//
// ── THE ORDER OF THE GUARDS IS LIVE'S ─────────────────────────────────────
//
// `_deliver`'s comment states it: "no usable channel, then the alert-type
// toggles, then the cooldown — and the cooldown is consumed only where a send
// actually happens, so a recipient who enables a channel later does not find a
// warm cooldown stamped while they had none."
//
// `Dispatcher.Deliver` already owns the last two. This file owns the first, in
// `perUserRecipients`, because a recipient with no channel must never reach the
// cooldown at all.

// dispatchFired sends every alert one evaluation produced.
//
// NON-BLOCKING FOR THE COLLECTOR that produced the payload: delivery talks to
// Telegram, SMTP and ntfy over the network, and this runs on the emit path of a
// collector tick. Live's sends are promises nobody awaits, for the same reason.
func (s *Server) dispatchFired(routerID, routerLabel string, fired []alert.Fired) {
	if s.dispatch == nil || !s.dispatch.Enabled() || len(fired) == 0 {
		return
	}
	// ── CHANNELS REPLACE THE FLAT TRANSPORTS, THEY DO NOT JOIN THEM ──────
	//
	// `SeedNotifyChannels` copies an install's configured transports into
	// channels on first start. Delivering to both afterwards would send every
	// alert TWICE — once to the `_install` recipient built from the flat
	// settings, once to the channel built from the same credentials. So the
	// legacy recipients are used only while there are no channels at all, which
	// is exactly the window before the seed has run.
	recipients := []alertdispatch.Recipient{}
	if !s.haveChannels() {
		recipients = s.dispatch.Recipients(routerID, s.perUserRecipients)
		if len(recipients) == 0 {
			log.Printf("[alert] %d alert(s) on %s reached no recipient", len(fired), routerID)
			return
		}
	}
	stamp := s.alertTimestamp()
	var settings notify.Settings
	if s.store != nil {
		if cfg, err := s.store.Settings(); err == nil {
			settings = notify.Settings(cfg)
		}
	}

	// ── WHO IS SENT WHAT IS DECIDED HERE, IN EVENT ORDER ─────────────────
	//
	// The cooldown is claimed and a channel's breach recorded before anything
	// goes to the network, on the caller's goroutine. `dispatchFired` is called
	// once per evaluation in the order the router's payloads arrive, so a
	// recovery is decided after its breach has been recorded, however slow the
	// breach's send turns out to be. Only the sends run in the goroutine.
	type outgoing struct {
		r   alertdispatch.Recipient
		key string
		msg alertdispatch.Message
	}
	var sends []outgoing
	for _, f := range fired {
		// SILENT ALERTS ARE SHOWN, NOT SENT. The supersede resolution is the
		// only producer; live emits it and never delivers it. See
		// `alert.Fired.Silent`.
		if f.Silent {
			continue
		}
		msg := alertdispatch.Build(settings, routerLabel, stamp, f)
		key := cooldownKey(f)
		for i := range recipients {
			// AN ESCALATION IS FOR CHANNELS. These recipients have no threshold
			// to newly cross: they were sent the breach whatever its level.
			if f.Escalation {
				break
			}
			if s.dispatch.Claim(&recipients[i], key) {
				sends = append(sends, outgoing{recipients[i], key, msg})
			}
		}
		// AND THE CHANNELS THAT SUBSCRIBE TO THIS EVENT, narrowed to the
		// channels that were sent the breach when this is its recovery.
		for _, c := range s.channelsFor(routerID, f) {
			if !s.dispatch.Claim(&c, key) {
				continue
			}
			if !f.Up {
				s.markAlertSent(routerID, f, c.ID)
			}
			sends = append(sends, outgoing{c, key, msg})
		}
	}
	if len(sends) == 0 {
		return
	}
	go func() {
		// A BOUND, so a hung transport cannot pin this goroutine for ever.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for i := range sends {
			// PER RECIPIENT, and the result is deliberately ignored: `Send`
			// logs its own failure, and one unreachable destination must not
			// stop the others.
			s.dispatch.Send(ctx, &sends[i].r, sends[i].key, sends[i].msg)
		}
	}()
}

// tickAlerts judges every held router's CPU and ping-loss runs on the clock and
// sends what that fires, exactly as a reading's alerts are sent.
func (s *Server) tickAlerts() {
	if s.sessions == nil || s.alerts == nil {
		return
	}
	for _, a := range s.sessions.AlertTicks() {
		if fired := s.alerts.Tick(a.Router, a.Connected); len(fired) > 0 {
			s.dispatchFired(a.Router.ID, a.Label, fired)
		}
	}
}

// channelsFor is the channels one fired alert goes to.
//
// ── A RECOVERY GOES ONLY WHERE ITS BREACH WENT ─────────────────────────────
//
// A channel narrows what it is told: its CPU and ping-loss thresholds, and its
// cooldown. `channelRecipients` used to pass every recovery through regardless,
// so a channel set to 90% was told "CPU back to normal" about a 71% spike it was
// never told about (2026-10-22, twice in one day on Telegram). So a breach the
// channel is actually sent is recorded (`alert_sent`), and the recovery goes
// only to the channels recorded for it. A breach the cooldown held back is not
// recorded, so it gets no recovery either.
//
// A NEW EPISODE STARTS CLEAN: an episode that never recovered (a router
// removed and re-added, a restart that missed the recovery) must not leave
// a channel marked as told about the next one.
func (s *Server) channelsFor(routerID string, f alert.Fired) []alertdispatch.Recipient {
	event := eventKeyFor(f)
	chans := s.channelRecipients(routerID, event, f.IfaceType, f.Value, f.Up)
	if s.auditDB == nil {
		return chans
	}
	if f.Escalation {
		// THE SAME EPISODE, LOUDER: only the channels it had not reached yet,
		// whose threshold the held level now meets (`channelRecipients` has
		// compared it). One that heard the breach is not told twice.
		sent, err := s.auditDB.AlertSentChannels(routerID, event, f.Subject)
		if err != nil {
			log.Printf("[alert] escalation of %s on %s not sent: who heard the breach is unknown: %v",
				event, routerID, err)
			return nil
		}
		out := chans[:0]
		for _, c := range chans {
			if !sent[c.ID] {
				out = append(out, c)
			}
		}
		return out
	}
	if !f.Up {
		if err := s.auditDB.ClearAlertSent(routerID, event, f.Subject); err != nil {
			log.Printf("[alert] could not reset who was sent %s on %s: %v", event, routerID, err)
		}
		return chans
	}
	sent, err := s.auditDB.AlertSentChannels(routerID, event, f.Subject)
	if err := s.auditDB.ClearAlertSent(routerID, event, f.Subject); err != nil {
		log.Printf("[alert] could not clear who was sent %s on %s: %v", event, routerID, err)
	}
	if err != nil {
		// NOBODY, rather than everybody. Not knowing who heard the breach is
		// the case this exists for, and guessing "everyone" is the stray
		// all-clear it stops.
		log.Printf("[alert] recovery of %s on %s not sent: who heard the breach is unknown: %v",
			event, routerID, err)
		return nil
	}
	out := chans[:0]
	for _, c := range chans {
		if sent[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

// markAlertSent records that channel `id` was sent this breach.
func (s *Server) markAlertSent(routerID string, f alert.Fired, id string) {
	if s.auditDB == nil {
		return
	}
	if err := s.auditDB.MarkAlertSent(routerID, eventKeyFor(f), f.Subject, id,
		time.Now().UnixMilli()); err != nil {
		log.Printf("[alert] could not record that %s was sent %s: %v", id, eventKeyFor(f), err)
	}
}

// dispatchBackup sends one backup notification: a drift or a failure.
//
// ── WHY IT IS NOT AN ALERT ─────────────────────────────────────────────────
//
// It goes out through the same recipients, settings and cooldown as an alert,
// but it is not one and does not pretend to be. `alert.Fired` carries a rule, a
// direction and a subject that the Alerts page renders and the database records;
// a backup result has none of those. Building a synthetic Fired to reuse
// `dispatchFired` would put rows on the Alerts page for something that is not an
// alert, which is worse than two short functions.
//
// The COOLDOWN KEY is per router and kind, so a router failing every night is
// rate-limited like anything else, and a drift on one router never suppresses a
// failure on another.
//
// Non-blocking, for the reason `dispatchFired` gives: this runs on the backup
// path, and delivery talks to Telegram, SMTP and ntfy over the network.
func (s *Server) dispatchBackup(routerID, kind, title, body string) {
	if s.dispatch == nil || !s.dispatch.Enabled() || title == "" {
		return
	}
	// Legacy recipients only while there are no channels — see `dispatchFired`.
	recipients := []alertdispatch.Recipient{}
	if !s.haveChannels() {
		recipients = s.dispatch.Recipients(routerID, s.perUserRecipients)
	}
	// THE CHANNELS THAT SUBSCRIBE TO THIS BACKUP EVENT.
	//
	// `backup_drift` and `backup_fail` are catalogue entries like any other, so
	// a channel can take failures and leave drift. Until channels, the settings
	// keys for these two existed, reached the browser, and had no Go reader at
	// all — this is the first thing that honours them.
	// A BACKUP HAS NO INTERFACE, so it passes the empty type and every
	// channel's interface filter accepts it.
	chans := s.channelRecipients(routerID, "backup_"+kind, "", 0, false)
	if len(recipients) == 0 && len(chans) == 0 {
		return
	}
	msg := alertdispatch.Message{Title: title, Body: body}
	key := "backup:" + kind + ":" + routerID

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for i := range recipients {
			// Per recipient, result ignored: `Deliver` logs its own failure, and
			// one unreachable destination must not stop the others.
			s.dispatch.Deliver(ctx, &recipients[i], key, msg)
		}
		for i := range chans {
			s.dispatch.Deliver(ctx, &chans[i], key, msg)
		}
	}()
}

// cooldownKey is the per-alert cooldown bucket.
//
// ── AN APPROXIMATION OF LIVE'S KEY, AND THE DIFFERENCE IS NAMED ───────────
//
// Live passes the key as `fire()`'s first argument — `iface:ether1:down`,
// `netwatch:*7:up`, `cpu:router:down` — so it is chosen per rule and does not
// travel on the fired alert. `alert.Fired` does not carry it, and adding it
// would mean retyping twenty rule-specific strings into the gated evaluator
// with nothing to check them against, which is the "retyped table" mistake this
// project keeps finding.
//
// So it is derived from what `Fired` does carry: stored type, subject and
// direction. The STRINGS differ from live's — `routeros_update::down` against
// `update:router:down` — and that is harmless, because the key is an internal
// cooldown bucket that is never shown to anyone.
//
// ── AND THE DERIVATION IS VERIFIED, NOT ASSUMED ───────────────────────────
//
// The alert-eval corpus now captures the key live actually delivered with,
// and `TestTheDerivedCooldownKeysPartitionLikeLive` compares the two schemes on
// the property that matters: which alerts share a bucket.
// **250,042 pairs across 113 cases, zero disagreements** — including netwatch,
// where live keys on the host's RouterOS `.id` and this keys on its name.
//
// The one case the corpus cannot speak for is two netwatch entries sharing a
// NAME: live would still separate them by id and this would not, so the second
// one's notification could be suppressed inside the cooldown window. That is the
// entire remaining difference, and it is narrower than "the key is derived".
func cooldownKey(f alert.Fired) string {
	stored := alert.StoredType(f.AlertType)
	if f.Up && f.ResolveType != "" {
		stored = f.ResolveType
	}
	dir := "down"
	if f.Up {
		dir = "up"
	}
	return stored + ":" + f.Subject + ":" + dir
}

// alertTimestamp is live's `_ts()` — the wall clock in the install's display
// zone, or the server's own when none is set.
func (s *Server) alertTimestamp() string {
	now := time.Now()
	if tz := s.displayTZ(); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			now = now.In(loc)
		}
	}
	return now.Format("15:04:05")
}

// perUserRecipients is live's `userNotify.recipientsFor`.
//
// ── AUTHORISATION IS ASKED AT SEND TIME, NEVER CACHED ─────────────────────
//
// The live comment is explicit: "recipientsFor() therefore asks Rbac.can(...,
// 'router:read', routerId) at SEND time, not at subscribe time, so revoking a
// grant stops delivery on the very next alert with nothing to invalidate." A
// reverse "which users hold a grant covering this router" query would be faster
// and would be a second answer to the same question.
//
// A USER WITH NO CHANNEL IS SKIPPED BEFORE THE PERMISSION CHECK, matching
// `_hasChannel` first in the live order — there is no point asking whether
// somebody may receive something that has nowhere to go.
func (s *Server) perUserRecipients(routerID string) ([]alertdispatch.Recipient, error) {
	if s.auditDB == nil || s.rbac == nil || routerID == "" {
		return nil, nil
	}
	configs, err := s.auditDB.ListUserNotifyConfigs()
	if err != nil {
		// RETURNED, not swallowed: `Dispatcher.Recipients` logs it and carries on
		// with the install recipient alone, which is the destination an operator
		// actually relies on.
		return nil, err
	}
	// ONE read of the install, for every user's email opt-in.
	install, err := s.mergedSettings()
	if err != nil {
		return nil, err
	}
	out := make([]alertdispatch.Recipient, 0, len(configs))
	for userID, cfg := range configs {
		set := s.userRecipientSettings(cfg, notify.Settings(install))
		if !notify.HasConfigured(set) {
			continue
		}
		ok, err := s.rbac.Can(userID, "router:read", routerID)
		if err != nil || !ok {
			continue
		}
		out = append(out, alertdispatch.Recipient{ID: "user:" + userID, Settings: set, Personal: true})
	}
	return out, nil
}

// userRecipientSettings turns one stored row into what the sender reads, by the
// same three steps the My Alerts Test button takes (`userNotifyLoad`, then the
// email branch of `userNotifyTest`): the allowlist, the credentials decrypted,
// and an email opt-in folded onto the install's mail server.
//
// ── THE RAW ROW WAS HANDED OVER UNTIL 2026-09-14 ──────────────────────────
//
// Found investigating issue #130. The tokens were still sealed, so every
// personal Telegram, Pushbullet and ntfy alert failed to authenticate; the
// allowlist `notify.Pick` calls a security boundary was skipped, so a row naming
// its own `smtpHost` reached the sender; and `WithInstallMail` had no caller, so
// an email opt-in was never a channel.
func (s *Server) userRecipientSettings(row map[string]any, install notify.Settings) notify.Settings {
	return notify.WithInstallMail(notify.Pick(notify.Settings(row), s.decryptSetting), install)
}

// alertMailer is the mail transport for one alert recipient's settings, or nil
// when they name no mail server and address.
//
// `buildAlertDispatch` passed nil here until 2026-09-14, so every alert to an
// email channel failed with "no mailer configured" while the Test button, which
// builds its own, reported the same settings working. It sends the way that
// button does, so what the operator tested is what an alert uses.
func (s *Server) alertMailer(set notify.Settings, public bool) notify.Mailer {
	cfg, who := smtpFromSettings(set)
	cfg.PublicOnly = public
	if cfg.Host == "" || cfg.From == "" ||
		len(who.To)+len(who.Cc)+len(who.Bcc) == 0 {
		return nil
	}
	return func(title, text string) error {
		who.Subject, who.Text = title, text
		return mailer.Send(cfg, who)
	}
}

// routerLabelFor names the router in the notification body.
//
// Falls back to the id rather than an empty string: a message saying an alert
// fired on "" tells the reader nothing, and the id at least identifies which
// record to look at.
func (s *Server) routerLabelFor(routerID string) string {
	if s.store == nil || routerID == "" {
		return routerID
	}
	all, _ := s.store.Routers()
	for _, r := range all {
		if r.ID == routerID {
			if strings.TrimSpace(r.Label) != "" {
				return r.Label
			}
			break
		}
	}
	return routerID
}
