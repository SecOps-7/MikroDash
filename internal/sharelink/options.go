package sharelink

import (
	"fmt"
	"strings"
	"time"

	"mikrodash/internal/dashcards"
	"mikrodash/internal/pages"
)

// ── Display and slideshow options ───────────────────────────────────────────

// Options is everything a link carries beyond its subject. Unused fields stay
// at their zero value for kinds they do not apply to.
type Options struct {
	// Theme: "browser" (default), "light" or "dark".
	Theme string `json:"theme"`
	// View: "full" (the kiosk strip) or "embed" (no strip, no padding,
	// transparent background). The default follows the embedding origins.
	View string `json:"view"`

	// Slideshow.
	DefaultSecs  int    `json:"defaultSecs,omitempty"`
	Transition   string `json:"transition,omitempty"` // none, fade, slide
	Speed        string `json:"speed,omitempty"`      // fast, normal, slow
	AtEnd        string `json:"atEnd,omitempty"`      // loop, stop
	PauseOnHover *bool  `json:"pauseOnHover,omitempty"`
	TitleBar     *bool  `json:"titleBar,omitempty"`
	Progress     *bool  `json:"progress,omitempty"`
	Controls     bool   `json:"controls,omitempty"`
	Offline      string `json:"offline,omitempty"` // skip, notice
	WarmUpSecs   int    `json:"warmUpSecs,omitempty"`

	// Dashboard card.
	Card string `json:"card,omitempty"`

	// Sensor feed.
	Values     []string `json:"values,omitempty"`
	Interfaces []string `json:"interfaces,omitempty"`
}

func bptr(b bool) *bool { return &b }

// Normalize fills defaults and refuses anything outside the allowed values.
// hasOrigins decides the default view (the operator's ruling: Embed when
// embedding sites are set, Full otherwise; an explicit choice is kept).
func (o *Options) Normalize(kind Kind, hasOrigins bool) error {
	switch o.Theme {
	case "":
		o.Theme = "browser"
	case "browser", "light", "dark":
	default:
		return fmt.Errorf("theme %q is not browser, light or dark", o.Theme)
	}
	if kind == KindFeed {
		o.View = ""
	} else {
		switch o.View {
		case "":
			o.View = "full"
			if hasOrigins {
				o.View = "embed"
			}
		case "full", "embed":
		default:
			return fmt.Errorf("view %q is not full or embed", o.View)
		}
	}
	if kind != KindSlideshow {
		o.DefaultSecs, o.Transition, o.Speed, o.AtEnd, o.Offline = 0, "", "", "", ""
		o.PauseOnHover, o.TitleBar, o.Progress, o.Controls, o.WarmUpSecs = nil, nil, nil, false, 0
	} else {
		if o.DefaultSecs == 0 {
			o.DefaultSecs = DefaultSlideSecs
		}
		if o.DefaultSecs < MinSlideSecs || o.DefaultSecs > MaxSlideSecs {
			return fmt.Errorf("a slide shows for %d to %d seconds", MinSlideSecs, MaxSlideSecs)
		}
		if err := oneOf("transition", &o.Transition, "fade", "none", "fade", "slide"); err != nil {
			return err
		}
		if err := oneOf("speed", &o.Speed, "normal", "fast", "normal", "slow"); err != nil {
			return err
		}
		if err := oneOf("at the end", &o.AtEnd, "loop", "loop", "stop"); err != nil {
			return err
		}
		if err := oneOf("offline routers", &o.Offline, "skip", "skip", "notice"); err != nil {
			return err
		}
		if o.PauseOnHover == nil {
			o.PauseOnHover = bptr(true)
		}
		if o.TitleBar == nil {
			o.TitleBar = bptr(true)
		}
		if o.Progress == nil {
			o.Progress = bptr(true)
		}
		if o.WarmUpSecs < 0 || o.WarmUpSecs > MaxWarmUpSecs {
			return fmt.Errorf("warm-up is 0 (off) to %d seconds", MaxWarmUpSecs)
		}
	}
	if kind != KindCard {
		o.Card = ""
	} else if !CardShareable(o.Card) {
		return fmt.Errorf("card %q cannot be shared", o.Card)
	}
	if kind != KindFeed {
		o.Values, o.Interfaces = nil, nil
	} else {
		if len(o.Values) == 0 {
			return fmt.Errorf("a sensor feed needs at least one value")
		}
		seen := map[string]bool{}
		var vals []string
		for _, v := range o.Values {
			if !FeedValueAllowed(v) {
				return fmt.Errorf("%q is not a value a feed can share", v)
			}
			if !seen[v] {
				seen[v] = true
				vals = append(vals, v)
			}
		}
		o.Values = vals
		if len(o.Interfaces) > MaxListEntries {
			return fmt.Errorf("at most %d interfaces", MaxListEntries)
		}
		var ifs []string
		for _, name := range o.Interfaces {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if len(name) > 64 || strings.ContainsAny(name, "\x00\n\r") {
				return fmt.Errorf("%q is not an interface name", name)
			}
			ifs = append(ifs, name)
		}
		o.Interfaces = ifs
		if seen[FeedInterfaces] && len(o.Interfaces) == 0 {
			return fmt.Errorf("choose the interfaces whose rates the feed shares")
		}
	}
	return nil
}

func oneOf(what string, v *string, def string, allowed ...string) error {
	if *v == "" {
		*v = def
		return nil
	}
	for _, a := range allowed {
		if *v == a {
			return nil
		}
	}
	return fmt.Errorf("%s %q is not one of %s", what, *v, strings.Join(allowed, ", "))
}

// ── Slides ──────────────────────────────────────────────────────────────────

// Slide is one page of a slideshow.
type Slide struct {
	Page     string `json:"page"`
	RouterID string `json:"routerId"`
	// Secs overrides the slideshow's default; 0 means the default.
	Secs int `json:"secs,omitempty"`
}

// CheckSlides enforces the operator's limits: 2 to 20 slides, each a
// shareable page on a router, each shown 10 seconds to an hour.
func CheckSlides(slides []Slide) error {
	if len(slides) < MinSlides || len(slides) > MaxSlides {
		return fmt.Errorf("a slideshow has %d to %d slides", MinSlides, MaxSlides)
	}
	for i, s := range slides {
		if !pages.IsShareable(s.Page) {
			return fmt.Errorf("slide %d: page %q cannot be shared", i+1, s.Page)
		}
		if s.RouterID == "" {
			return fmt.Errorf("slide %d: choose a router", i+1)
		}
		if s.Secs != 0 && (s.Secs < MinSlideSecs || s.Secs > MaxSlideSecs) {
			return fmt.Errorf("slide %d: a slide shows for %d to %d seconds", i+1, MinSlideSecs, MaxSlideSecs)
		}
	}
	return nil
}

// ── Dashboard cards ─────────────────────────────────────────────────────────

// CardShareable reports whether a Dashboard card may be shared on its own: it
// must exist, and the page it belongs to (the Dashboard, unless the card names
// another) must be shareable. That leaves out the Agent Overview, whose page,
// the AI assistant, is not.
func CardShareable(id string) bool {
	if _, ok := cardFeeds[id]; !ok {
		return false
	}
	for _, c := range dashcards.All {
		if c.ID != id {
			continue
		}
		page := c.Page
		if page == "" {
			page = "dashboard"
		}
		return pages.IsShareable(page)
	}
	return false
}

// CardFeed is where a card's data arrives, which is all a card link's viewer
// is let into: the card rooms it joins (dashcards Room keys), and whether it
// needs the Dashboard's page room (Ping, Top Talkers, NetWatch, Routes and BGP
// are fed there). What it RECEIVES from those rooms is cut down to its card's
// fields by cardData (carddata.go); a card with no room is painted from the
// router-wide and traffic data alone.
type CardFeed struct {
	Rooms []string
	Dash  bool
}

// cardFeeds is every card a link may show, by what feeds it. Several cards
// draw on another card's room (Top Ports and Top Countries on Connections,
// Signal Health on Wireless): the room is the one their data arrives in.
//
// A CARD MISSING HERE CANNOT BE SHARED, and cardsNotShareable says why for
// each: the four it names are filled per viewer from that viewer's grants,
// and a share has no viewer. TestEveryCardIsFedOrRecorded holds both lists
// against dashcards.All in both directions, and against cardData.
var cardFeeds = map[string]CardFeed{
	"card-traffic":      {},
	"card-system":       {},
	"dc-card-bw":        {},
	"card-connections":  {Rooms: []string{"connections"}},
	"dc-card-destcc":    {Rooms: []string{"connections"}},
	"dc-card-topcc":     {Rooms: []string{"connections"}},
	"dc-card-flow":      {Rooms: []string{"connections"}},
	"dc-card-topports":  {Rooms: []string{"connections"}},
	"dc-card-netflow":   {Rooms: []string{"wireless", "interfaces"}},
	"dc-card-signal":    {Rooms: []string{"wireless"}},
	"dc-card-band":      {Rooms: []string{"wireless"}},
	"card-network":      {Rooms: []string{"dhcp"}},
	"dc-card-iputil":    {Rooms: []string{"dhcp"}},
	"card-wireguard":    {Rooms: []string{"vpn"}},
	"dc-card-physports": {Rooms: []string{"interfaces"}},
	"dc-card-fwaction":  {Rooms: []string{"firewall"}},
	"dc-card-logs":      {Rooms: []string{"logs"}},
	"dc-card-wanflow":   {Rooms: []string{"wan"}},
	"dc-card-ping":      {Dash: true},
	"card-toptalkers":   {Dash: true},
	"dc-card-netwatch":  {Dash: true},
	"dc-card-routes":    {Dash: true},
	"dc-card-bgp":       {Dash: true},
}

// cardsNotShareable are the cards no link may show, and why.
var cardsNotShareable = map[string]string{
	"dc-card-diagnostics": "reports on this MikroDash process, sent per viewer",
	"dc-card-agent":       "the AI assistant's page is not shareable, and its text is built per viewer",
	"dc-card-secscore":    "filled per viewer from the scans that viewer may read",
	"dc-card-domainroute": "filled per viewer from what that viewer's grants allow",
}

// CardFeedOf is a shareable card's feed.
func CardFeedOf(id string) (CardFeed, bool) {
	if !CardShareable(id) {
		return CardFeed{}, false
	}
	f, ok := cardFeeds[id]
	return f, ok
}

// ShareableCards is every card CardShareable allows, in Dashboard order.
func ShareableCards() []dashcards.Card {
	var out []dashcards.Card
	for _, c := range dashcards.All {
		if CardShareable(c.ID) {
			out = append(out, c)
		}
	}
	return out
}

// ── Sensor feed values ──────────────────────────────────────────────────────
//
// The operator's ruling: all the proposed values, and a value MikroDash does
// not already collect stays out. Voltage (no collector reads it) and the DHCP
// lease count (not kept for a router nobody is watching) are therefore not
// here. Each value names the page it belongs to; a value whose page is not
// shareable cannot be chosen.

// FeedInterfaces is the per-interface value (running, rx and tx rates).
const FeedInterfaces = "interfaces"

// FeedValue is one value a sensor feed may share.
type FeedValue struct {
	Key   string
	Label string
	Page  string
}

// FeedValues is the list, in the form's order.
var FeedValues = []FeedValue{
	{"online", "Online or offline", "dashboard"},
	{"uptime", "Uptime", "dashboard"},
	{"version", "RouterOS version", "dashboard"},
	{"model", "Model", "dashboard"},
	{"cpu", "CPU load", "dashboard"},
	{"memory", "Memory used", "dashboard"},
	{"temperature", "Temperature", "dashboard"},
	{FeedInterfaces, "Interfaces: running, rx and tx rate", "interfaces"},
	{"alerts", "Open alerts", "dashboard"},
	{"wifi_clients", "Wi-Fi clients", "wifi-clients"},
}

// FeedValueAllowed reports whether a value exists and its page is shareable.
func FeedValueAllowed(key string) bool {
	for _, v := range FeedValues {
		if v.Key == key {
			return pages.IsShareable(v.Page)
		}
	}
	return false
}

// ── Expiry ──────────────────────────────────────────────────────────────────

// ExpiryPresets are the form's choices besides a date and Never.
var ExpiryPresets = map[string]time.Duration{
	"1d": 24 * time.Hour, "7d": 7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour,
}

// ResolveExpiry turns the form's choice into an expiry in milliseconds, or
// nil for Never. choice is a preset key, "never", or "date" with dateMs.
func ResolveExpiry(choice string, dateMs int64, now time.Time) (*int64, error) {
	switch choice {
	case "never":
		return nil, nil
	case "date":
		if dateMs <= now.UnixMilli() {
			return nil, fmt.Errorf("the expiry date has passed")
		}
		return &dateMs, nil
	case "":
		choice = "7d"
	}
	d, ok := ExpiryPresets[choice]
	if !ok {
		return nil, fmt.Errorf("expiry %q is not a choice", choice)
	}
	ms := now.Add(d).UnixMilli()
	return &ms, nil
}

// ── Validity ────────────────────────────────────────────────────────────────

// Revoke reasons, stored in share_links.revoke_reason.
const (
	ReasonAdmin           = "admin"
	ReasonCreatorDeleted  = "creator-deleted"
	ReasonCreatorNotAdmin = "creator-not-admin"
	ReasonRouterDeleted   = "router-deleted"
)

// State is what Check needs to know about a link and the world.
type State struct {
	Revoked   bool
	ExpiresAt *int64 // nil: never expires
	// CreatorExists and CreatorIsAdmin come from the store and the grant graph.
	CreatorExists  bool
	CreatorIsAdmin bool
	// RoutersExist is false when any router the link shows has been deleted.
	RoutersExist bool
}

// Check decides whether a link may be used now. "" means valid; otherwise the
// reason it is not, which is also the reason to record when an automatic
// revocation follows ("expired" is not a revocation).
func Check(s State, now time.Time) string {
	switch {
	case s.Revoked:
		return "revoked"
	case s.ExpiresAt != nil && now.UnixMilli() >= *s.ExpiresAt:
		return "expired"
	case !s.CreatorExists:
		return ReasonCreatorDeleted
	case !s.CreatorIsAdmin:
		return ReasonCreatorNotAdmin
	case !s.RoutersExist:
		return ReasonRouterDeleted
	}
	return ""
}

// CheckCap enforces the viewer cap's bounds: 1 to MaxViewerCap; 0 means the
// default.
func CheckCap(n int) (int, error) {
	if n == 0 {
		return DefaultViewerCap, nil
	}
	if n < 1 || n > MaxViewerCap {
		return 0, fmt.Errorf("a link allows 1 to %d viewers", MaxViewerCap)
	}
	return n, nil
}

// CheckLabel trims and bounds a label.
func CheckLabel(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("give the link a label")
	}
	if len([]rune(s)) > MaxLabel || strings.ContainsAny(s, "\x00\n\r") {
		return "", fmt.Errorf("a label is at most %d characters on one line", MaxLabel)
	}
	return s, nil
}
