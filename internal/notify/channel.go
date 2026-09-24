package notify

// One stored channel, decoded, and the decision about whether it wants an event.
//
// ── PURE, SO THE ROUTING CAN BE TESTED WITHOUT A ROUTER OR A DATABASE ──────
//
// Rows in, verdict out — the same shape as the write guards and
// `internal/session/needs.go`, and for the same reason: everything around it
// needs a live connection or a table to exercise, and this is the part that
// decides who hears about an outage.
//
// ── WHAT THIS IS NOT ───────────────────────────────────────────────────────
//
// It is not the alert gate. Whether an event is RAISED at all is decided far
// above, by the `notif*` settings inside `(*Evaluator).emit`, and that check
// stays exactly where it is: issue #109 moved it down into delivery and had to
// be reverted, because the notification bell then stopped honouring the
// Interface Alert Filter. A channel can only NARROW what the evaluator already
// raised, which is why `Wants` is consulted after the alert exists and never
// before.

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Channel kinds.
const (
	KindWebhook = "webhook"
	KindSMTP    = "smtp"
)

// ChannelSpec is a stored channel with its JSON columns decoded and its secrets
// already unsealed by the caller.
type ChannelSpec struct {
	ID      string
	Name    string
	Kind    string
	Enabled bool
	// URLs is the webhook destinations, for KindWebhook.
	URLs []string
	// Settings is the flat transport map, for KindSMTP, so `Send` can deliver it
	// with the existing mailer rather than a second SMTP implementation.
	Settings Settings
	// Events is the set of catalogue keys this channel accepts. EMPTY MEANS
	// NONE, deliberately: a channel that subscribed to everything by accident
	// would page somebody at three in the morning about a prefix count.
	Events []string
	// Routers is the set of router ids this channel accepts. EMPTY MEANS ALL,
	// which is the opposite default and the right one — a channel created
	// without touching the picker should cover the fleet, and a fleet that
	// grows should not need every channel edited.
	Routers []string
}

// channelConfig is the stored `config` JSON.
type channelConfig struct {
	URLs   []string `json:"urls"`
	Host   string   `json:"host"`
	Port   int      `json:"port"`
	Secure bool     `json:"secure"`
	User   string   `json:"user"`
	Pass   string   `json:"pass"`
	From   string   `json:"from"`
	To     string   `json:"to"`
}

// DecodeChannel turns the stored columns into a spec. `config` must already be
// decrypted; this package cannot unseal anything.
//
// A column that will not parse yields an empty field rather than an error: one
// corrupt channel must not stop the others loading, which is the same choice
// `usernotify_api.go` makes when a credential will not decrypt.
func DecodeChannel(id, name, kind string, enabled bool, config, events, routers string) ChannelSpec {
	c := ChannelSpec{ID: id, Name: name, Kind: kind, Enabled: enabled}
	_ = json.Unmarshal([]byte(events), &c.Events)
	_ = json.Unmarshal([]byte(routers), &c.Routers)

	var cfg channelConfig
	_ = json.Unmarshal([]byte(config), &cfg)
	switch kind {
	case KindWebhook:
		c.URLs = cfg.URLs
	case KindSMTP:
		// Mapped onto the flat keys `Send` already reads, so an SMTP channel is
		// delivered by `internal/mailer` — the one transport where the
		// real-world quirks (implicit TLS vs STARTTLS, LOGIN-only auth) are
		// already solved and tested.
		port := cfg.Port
		if port == 0 {
			port = 587
		}
		c.Settings = Settings{
			"smtpEnabled": true,
			"smtpHost":    cfg.Host,
			"smtpPort":    strconv.Itoa(port),
			"smtpSecure":  cfg.Secure,
			"smtpUser":    cfg.User,
			"smtpPass":    cfg.Pass,
			"smtpFrom":    cfg.From,
			"smtpTo":      cfg.To,
		}
	}
	return c
}

// Wants reports whether this channel should receive `event` from `routerID`.
//
// The three questions, in the order that makes each cheap: is it on, does it
// cover this router, does it subscribe to this event.
func (c ChannelSpec) Wants(event, routerID string) bool {
	if !c.Enabled {
		return false
	}
	if !c.coversRouter(routerID) {
		return false
	}
	for _, e := range c.Events {
		if e == event {
			return true
		}
	}
	return false
}

// coversRouter is the empty-means-all rule, in one place so the two callers
// cannot disagree about it.
func (c ChannelSpec) coversRouter(routerID string) bool {
	if len(c.Routers) == 0 {
		return true
	}
	for _, r := range c.Routers {
		if r == routerID {
			return true
		}
	}
	return false
}

// Deliverable reports whether this channel has somewhere to send.
//
// A channel with no URLs and no mail host is configuration in progress, not a
// destination; sending to it would report success having sent nothing.
func (c ChannelSpec) Deliverable() bool {
	switch c.Kind {
	case KindWebhook:
		for _, u := range c.URLs {
			if strings.TrimSpace(u) != "" {
				return true
			}
		}
		return false
	case KindSMTP:
		host, _ := c.Settings["smtpHost"].(string)
		to, _ := c.Settings["smtpTo"].(string)
		return strings.TrimSpace(host) != "" && strings.TrimSpace(to) != ""
	}
	return false
}
