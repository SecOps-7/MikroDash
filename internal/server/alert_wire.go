package server

import (
	"log"
	"time"

	"mikrodash/internal/alertdispatch"
	"mikrodash/internal/alertwire"
	"mikrodash/internal/notify"
)

// buildAlertWire constructs the alert evaluator and hands it to the session
// manager, or leaves it nil.
//
// ── WHAT IS AND IS NOT SWITCHED ON HERE ────────────────────────────────────
//
// This wires the EVALUATOR and the DATABASE WRITES. It does not dispatch: no
// Telegram message, no email, no ntfy push. `internal/alertwire` has no code
// that could, and that is deliberate — cutover blocker 5 is the one
// blocker whose reasoning did not change when the port went standalone:
//
//	Both engines evaluate the same conditions against the same physical
//	routers, and the cooldown is an in-memory map rather than a shared row, so
//	neither sees the other's sends. A duplicated Telegram message or email
//	cannot be un-received.
//
// A row filed twice is a duplicate an operator can delete. A message sent twice
// is not. So the writes go in now — they are what make the Alerts page and the
// Devices alert counts real — and the sending waits for the operator's call.
//
// ── NIL WITHOUT A DATABASE, AND NIL IS INERT ───────────────────────────────
//
// `Wire.Evaluate` guards on its receiver, so an install with no `/data` history
// serves every page and files nothing, rather than failing to start.
func (s *Server) buildAlertWire() *alertwire.Wire {
	if s.auditDB == nil {
		log.Printf("[alert] no history database; alert evaluation is off")
		return nil
	}
	w := alertwire.New(s.auditDB)
	// SAYS ONLY WHAT IT KNOWS. This claimed "NOTHING is dispatched"
	// unconditionally, and printed one line above the dispatch banner saying
	// "notifications will be SENT" — both true-looking, one of them wrong,
	// every startup. `CLAUDE.md` recorded that contradiction going unread for
	// days; it was still printing it at cutover on 2026-08-30, because
	// `TestTheDispatchBannerMatchesTheWiring` only ever checked the OTHER half.
	//
	// Whether anything is sent is the dispatch banner's to state, and it states
	// both cases. This one owns the evaluator alone.
	log.Printf("[alert] evaluator on — alert rows are written")
	return w
}

// ── `alertSettings` IS GONE ───────────────────────────────────────────────
//
// It read `alertCpuThreshold` and `alertPingLoss` out of the merged settings and
// handed them to every evaluator. Both are a property of the notification
// channel now: each one decides what IT is told about, and the evaluator records
// against `alert.FloorCPU` and `alert.FloorPingLoss` without deciding for
// anybody.
//
// Its last comment is worth keeping because the trap it names outlived it: the
// merge was used for the ENV OVERRIDES rather than for credentials, and reading
// the raw file instead would have ignored an operator who set a threshold by
// environment variable. Anything here that grows a settings read again wants
// `mergedSettings`, not `store.Settings`.

// refreshAlertSettings re-reads them after a settings save, for BOTH halves.
//
// IN PLACE, never a rebuild: see `alert.Evaluator.SetSettings`. Rebuilding would
// clear the edge state, so ticking one checkbox would re-fire every condition
// that was already true.
//
// ── NOTHING CALLED THIS UNTIL 2026-09-14 ──────────────────────────────────
//
// Only a test did, so every alert setting was frozen at startup while the save
// answered `requiresRestart: false`: thresholds and per-type toggles here, and in
// the dispatcher the channels, their tokens, the cooldown and the per-user switch.
// Turning Telegram off kept sending to it until a restart. Found investigating
// issue #130.
//
// The dispatcher gets the MERGED settings, as `buildAlertDispatch` does: the raw
// file holds the tokens sealed.
func (s *Server) refreshAlertSettings() {
	// THE EVALUATORS ARE NOT REFRESHED, because nothing they read is a setting
	// any more. They fire against a fixed floor; the thresholds that used to
	// arrive here are read per channel, at delivery, from the channel's own row.
	if s.dispatch != nil {
		if cfg, err := s.mergedSettings(); err == nil {
			s.dispatch.SetSettings(notify.Settings(cfg))
		}
	}
}

// buildAlertDispatch constructs the notification sender.
//
// ── BUILT EVEN WHEN OFF ────────────────────────────────────────────────────
//
// So the switch lives in one boolean inside the dispatcher rather than as a nil
// check at every call site — and, more usefully, so a disabled dispatcher still
// answers `Enabled()` honestly for a status line. `Deliver` returns before
// touching anything, including the cooldown: see its comment for why stamping
// while disabled would swallow the first real alert after it was turned on.
func (s *Server) buildAlertDispatch(enabled bool) *alertdispatch.Dispatcher {
	// MERGED, not raw. The transports authenticate with `telegramBotToken`,
	// `ntfyToken` and the SMTP pair, all of which are stored AES-GCM sealed —
	// the raw file hands them over as ciphertext and every send fails. Telegram
	// answers HTTP 404 to a bot id that is really a base64 blob.
	cfg, err := s.mergedSettings()
	if err != nil {
		cfg = map[string]any{}
	}
	if enabled {
		// LOUD, because this is the one setting in this file that reaches
		// outside the machine, and because the operator turning it on while the
		// Node app is still up is the mistake it exists to prevent.
		// ── TRUE AGAIN AS OF 2026-08-30, AND IT WAS NOT BEFORE ──────────────
		//
		// This line promised sending while `Evaluate()`'s result was discarded at
		// both call sites and `srv.dispatch` had no reader — so nothing was ever
		// sent, and the line printed next to `buildAlertWire`'s "NOTHING is
		// dispatched" at every startup. It was corrected to say so, and is
		// restored now that `alert_send.go` is the caller.
		//
		// `TestTheDispatchBannerMatchesTheWiring` is what forces the two to agree:
		// it failed the moment the caller landed, which is how this line came
		// back rather than being forgotten.
		log.Printf("[alert] dispatch on — notifications will be SENT")
	} else {
		log.Printf("[alert] dispatch is off; rows are written, nothing is sent " +
			"(pass -alert-dispatch to send)")
	}
	return alertdispatch.New(enabled, notify.Settings(cfg), notify.DefaultClient, s.alertMailer,
		func() int64 { return time.Now().UnixMilli() })
}
