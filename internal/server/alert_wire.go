package server

import (
	"log"
	"time"

	"mikrodash/internal/alert"
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
	w := alertwire.New(s.auditDB, s.alertSettings())
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

// alertSettings reads the thresholds and per-type toggles out of settings.json.
//
// ── THE DEFAULTS ARE THE MERGED ONES, NOT ZERO ─────────────────────────────
//
// `mergedSettings()` returns the file merged over `Settings.DEFAULTS`, so a key
// the operator never touched arrives with the install's default rather than
// missing. That matters most for `alertCpuThreshold`: absent would read as 0
// here, and a threshold of zero alerts on every router at every poll forever.
//
// ── THE INTERFACE FILTERS ARE A MAP BECAUSE THE KEY IS COMPUTED ────────────
//
// `alert.IfaceTypeKey` derives `notifIfaceEther` and friends from the
// interface's own type string. Five named fields would mean a second switch here
// restating that one, and the two would drift the first time RouterOS gained an
// interface kind.
func (s *Server) alertSettings() alert.Settings {
	// Merged for the ENV OVERRIDES rather than for credentials — no threshold
	// here is sealed. `load()` applies them and the raw file does not, so an
	// operator setting a threshold by environment variable would have been
	// ignored on this path alone.
	cfg, err := s.mergedSettings()
	if err != nil {
		// THE DEFAULTS, not silence. An unreadable settings file is a reason to
		// alert on the built-in thresholds, not a reason to stop watching — the
		// same judgement `session.go` makes when it logs "settings unreadable;
		// using collector defaults".
		log.Printf("[alert] settings unreadable (%v); using built-in thresholds", err)
		cfg = map[string]any{}
	}
	num := func(k string, def float64) float64 {
		switch v := cfg[k].(type) {
		case float64:
			return v
		case int:
			return float64(v)
		}
		return def
	}
	// ABSENT MEANS THE DEFAULT, NOT FALSE. `Settings.DEFAULTS` ships `notifCpu`,
	// `notifPing`, `notifVpn` and `notifBgp` ON, so reading a missing key as
	// false would silence four alert families on any install whose settings file
	// predates them.
	flag := func(k string, def bool) bool {
		if v, ok := cfg[k].(bool); ok {
			return v
		}
		return def
	}
	return alert.Settings{
		CPUThreshold:      num("alertCpuThreshold", 90),
		PingLoss:          num("alertPingLoss", 100),
		NotifCPU:          flag("notifCpu", true),
		NotifRouterUpdate: flag("notifRouterUpdate", false),
		NotifPing:         flag("notifPing", true),
		NotifNetwatch:     flag("notifNetwatch", false),
		NotifIfaceUpDown:  flag("notifIfaceUpDown", true),
		NotifVPN:          flag("notifVpn", true),
		NotifBGP:          flag("notifBgp", true),
		IfaceTypeFilters: map[string]bool{
			"notifIfaceEther":  flag("notifIfaceEther", true),
			"notifIfaceWlan":   flag("notifIfaceWlan", true),
			"notifIfaceBridge": flag("notifIfaceBridge", false),
			"notifIfaceVlan":   flag("notifIfaceVlan", false),
			"notifIfaceOther":  flag("notifIfaceOther", false),
		},
	}
}

// refreshAlertSettings re-reads them after a settings save.
//
// IN PLACE, never a rebuild: see `alert.Evaluator.SetSettings`. Rebuilding would
// clear the edge state, so ticking one checkbox would re-fire every condition
// that was already true.
func (s *Server) refreshAlertSettings() {
	if s.alerts == nil {
		return
	}
	s.alerts.SetSettings(s.alertSettings())
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
		log.Printf("[alert] DISPATCH IS ON — notifications will be SENT. " +
			"If another MikroDash is watching these routers, both are sending.")
	} else {
		log.Printf("[alert] dispatch is off; rows are written, nothing is sent " +
			"(pass -alert-dispatch to send)")
	}
	return alertdispatch.New(enabled, notify.Settings(cfg), notify.DefaultClient, nil,
		func() int64 { return time.Now().UnixMilli() })
}
