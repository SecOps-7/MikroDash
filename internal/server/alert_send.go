package server

import (
	"context"
	"strings"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/alertdispatch"
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
	recipients := s.dispatch.Recipients(routerID, s.perUserRecipients)
	if len(recipients) == 0 {
		return
	}
	stamp := s.alertTimestamp()
	var settings notify.Settings
	if s.store != nil {
		if cfg, err := s.store.Settings(); err == nil {
			settings = notify.Settings(cfg)
		}
	}

	go func() {
		// A BOUND, so a hung transport cannot pin this goroutine for ever.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
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
				// PER RECIPIENT, and the result is deliberately ignored:
				// `Deliver` logs its own failure and returns false, and one
				// unreachable destination must not stop the others.
				s.dispatch.Deliver(ctx, &recipients[i], key, msg)
			}
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
	if tz := s.displayTimezone(); tz != "" {
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
	out := make([]alertdispatch.Recipient, 0, len(configs))
	for userID, cfg := range configs {
		set := notify.Settings(cfg)
		if !notify.HasConfigured(set) {
			continue
		}
		ok, err := s.rbac.Can(userID, "router:read", routerID)
		if err != nil || !ok {
			continue
		}
		out = append(out, alertdispatch.Recipient{ID: "user:" + userID, Settings: set})
	}
	return out, nil
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
