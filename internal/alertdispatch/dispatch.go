// Package alertdispatch decides WHETHER an alert notification is sent, and what
// it says. It is step 2 of the plan in `LOOP.md`.
//
// ── IT IS OFF UNLESS SWITCHED ON, AND THAT IS NOT A DEFAULT-CHOICE ─────────
//
// `New` takes `enabled bool` and the server passes false unless the operator
// asks. cutover blocker 5 is the reason, and it is the one blocker whose
// reasoning did not change when the port went standalone:
//
//	Both engines evaluate the same conditions against the same physical
//	routers, and the cooldown is an in-memory map rather than a shared row, so
//	neither sees the other's sends. A duplicated Telegram message or email
//	cannot be un-received.
//
// A row filed twice is a duplicate an operator deletes. A message sent twice is
// already in their pocket. So the code exists, is tested, and is inert.
//
// ── THE GUARD ORDER IS CHANNEL, THEN COOLDOWN ──────────────────────────────
//
// And the cooldown is CONSUMED ONLY WHERE A SEND HAPPENS. The live comment says
// why: "a recipient who enables a channel later does not find a warm cooldown
// stamped while they had none." Reordering these is invisible in every test that
// only sends to a configured recipient, and shows up the first time someone
// turns a channel on.
package alertdispatch

import (
	"context"
	"log"
	"sync"

	"mikrodash/internal/alert"
	"mikrodash/internal/notify"
)

// cooldownMax is the live `COOLDOWN_MAX`. The map is CLEARED wholesale when it
// passes this, rather than evicted entry by entry — the live comment accepts the
// consequence: "which at worst lets one alert through early".
const cooldownMax = 1000

// Recipient is one destination. The install is `_install`; a user is `user:<id>`.
//
// THE INSTALL IS JUST ANOTHER RECIPIENT with a reserved id, so the delivery loop
// has no special case and its cooldown cannot collide with a user's.
type Recipient struct {
	ID       string
	Settings notify.Settings
}

// Message is what one alert says. Rendered ONCE and fanned out — templates are
// install-wide, so every recipient gets the same words and only decides whether
// it wants them.
type Message struct {
	Title string
	Body  string
}

// Build assembles the message for one fired alert.
//
// ── `alertType` IS OVERRIDDEN AFTER THE SPREAD ─────────────────────────────
//
// The live line is
//
//	{ routerName, timestamp, ...vars, alertType: labelFor(alertType) }
//
// so a `vars.alertType` LOSES. Getting that backwards makes a push say one name
// while the alert list says another — the live comment names exactly that. It is
// pinned by a corpus case whose vars carry a string no label can produce,
// because the first version of that case used a value that happened to EQUAL the
// label and would have passed either way round.
//
// ── THREE LEVELS OF BODY FALLBACK, AND THE DIRECTION PICKS THE FIRST ───────
//
// `notifBodyUp` for a resolution, then `notifBody`, then a built-in. A port
// collapsing them sends a warning glyph for a recovery.
func Build(s notify.Settings, routerName, timestamp string, f alert.Fired) Message {
	vars := map[string]any{
		"routerName": routerName,
		"timestamp":  timestamp,
		"detail":     f.Detail,
		"subject":    f.Subject,
		// LAST, so it wins over anything above.
		"alertType": alert.LabelFor(f.AlertType),
	}
	str := func(k string) string { v, _ := s[k].(string); return v }

	title := str("notifTitle")
	if title == "" {
		title = "MikroDash Alert"
	}
	tpl := ""
	if f.Up {
		tpl = str("notifBodyUp")
	}
	if tpl == "" {
		tpl = str("notifBody")
	}
	if tpl == "" {
		if f.Up {
			tpl = "✅ {{alertType}} on {{routerName}}: {{detail}}"
		} else {
			tpl = "⚠️ {{alertType}} on {{routerName}}: {{detail}}"
		}
	}
	return Message{Title: alert.Render(title, vars), Body: alert.Render(tpl, vars)}
}

// Dispatcher holds the cooldowns.
type Dispatcher struct {
	mu        sync.Mutex
	enabled   bool
	cooldowns map[string]int64
	settings  notify.Settings
	client    notify.Doer
	mailer    notify.Mailer
	now       func() int64
	// sendFn is the transport, injectable so the tests can assert what WOULD
	// have gone without a network.
	sendFn func(ctx context.Context, s notify.Settings, title, body string) error
}

func New(enabled bool, settings notify.Settings, client notify.Doer, mail notify.Mailer,
	now func() int64) *Dispatcher {
	d := &Dispatcher{
		enabled: enabled, cooldowns: map[string]int64{}, settings: settings,
		client: client, mailer: mail, now: now,
	}
	d.sendFn = func(ctx context.Context, s notify.Settings, title, body string) error {
		return notify.Send(ctx, d.client, s, d.mailer, title, body)
	}
	return d
}

func (d *Dispatcher) Enabled() bool {
	if d == nil {
		return false
	}
	return d.enabled
}

func (d *Dispatcher) SetSettings(s notify.Settings) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.settings = s
}

// Allow decides whether this recipient may be sent this subject now, and STAMPS
// the cooldown if so.
//
// Separated from the send because it is the whole decision and it is pure enough
// to gate: the corpus in `testdata/alert-dispatch-cases.json` drives it as a
// SEQUENCE, since every rule here is about what an earlier call left behind.
//
// THE GUARDS IN ORDER:
//
//  1. no recipient at all;
//  2. NO USABLE CHANNEL — and this returns before touching the map, which is
//     the property the live comment is about;
//  3. the cooldown window.
func (d *Dispatcher) Allow(r *Recipient, subjectKey string) bool {
	if r == nil {
		return false
	}
	if !notify.HasConfigured(r.Settings) {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	// `notifCooldownSec || 60` — a zero, an absent key and a non-number all mean
	// sixty seconds. Zero is NOT "no cooldown"; the live expression treats it as
	// falsy, and a port reading it as "send every time" would turn a flapping
	// interface into a message per poll.
	secs := 60.0
	if v, ok := d.settings["notifCooldownSec"].(float64); ok && v != 0 {
		secs = v
	}
	key := r.ID + "|" + subjectKey
	now := d.now()
	if now-d.cooldowns[key] < int64(secs*1000) {
		return false
	}
	// CLEARED WHOLESALE past the cap, matching the live map. It costs one early
	// alert and bounds the memory; an LRU here would be a second implementation
	// of a decision the original made deliberately.
	if len(d.cooldowns) > cooldownMax {
		d.cooldowns = map[string]int64{}
	}
	d.cooldowns[key] = now
	return true
}

// Deliver sends one message to one recipient, if the guards allow it.
//
// RETURNS WHETHER IT SENT, so a caller can log honestly rather than assuming.
func (d *Dispatcher) Deliver(ctx context.Context, r *Recipient, subjectKey string, m Message) bool {
	if d == nil || !d.enabled {
		// NOT EVEN THE COOLDOWN. A disabled dispatcher must leave no trace, so
		// that turning it on later behaves like a fresh start rather than
		// finding every subject already warm.
		return false
	}
	if !d.Allow(r, subjectKey) {
		return false
	}
	if err := d.sendFn(ctx, r.Settings, m.Title, m.Body); err != nil {
		// LOGGED AND SWALLOWED, per recipient. The live `.catch` does the same:
		// one unreachable destination must not stop the others, and it must not
		// stop the alert row that has already been filed.
		log.Printf("[alert] notify failed (%s): %v", r.ID, err)
		return false
	}
	return true
}

// Recipients is the install destination, plus per-user ones when the install
// switch allows it.
//
// PER-USER FAN-OUT IS GATED ON `userNotifyEnabled`, WHICH SHIPS OFF. The live
// comment: "a personal ntfy topic or SMTP host is a destination the *user*
// chooses, so enabling it lets any account that can log in make the server issue
// outbound requests to an address it picks."
//
// `perUser` is injected rather than imported so this package does not depend on
// the user-notify store; the server supplies it, or nil.
func (d *Dispatcher) Recipients(routerID string,
	perUser func(routerID string) ([]Recipient, error)) []Recipient {

	d.mu.Lock()
	settings := d.settings
	d.mu.Unlock()

	out := []Recipient{{ID: "_install", Settings: settings}}
	if routerID == "" || perUser == nil {
		return out
	}
	if on, _ := settings["userNotifyEnabled"].(bool); !on {
		return out
	}
	extra, err := perUser(routerID)
	if err != nil {
		// A FAILURE HERE MUST NEVER COST THE INSTALL ITS OWN NOTIFICATION —
		// that is the destination an operator actually relies on. The live code
		// warns and carries on with the install recipient alone.
		log.Printf("[alert] per-user recipients failed: %v", err)
		return out
	}
	return append(out, extra...)
}
