// Package apps names the application behind a connection: Zoom, Netflix, Xbox,
// Cloudflare. Pure: a destination's facts in, an app (or none) out.
//
// ── WHERE THE CATALOGUE COMES FROM ──────────────────────────────────────────
//
// catalogue_gen.json, embedded, is cmd/appgen's output from the hand-curated
// testdata/apps/catalogue.json: about 130 apps, each with the domains
// v2fly/domain-list-community lists for it (MIT) plus any added by hand, its AS
// numbers and well-known ports, its brand colour and how its logo is drawn.
// Edit the catalogue, not the generated file; `appgen -check` in verify fails a
// bundle made from an older catalogue.
//
// ── HOW A CONNECTION IS NAMED, FIRST HIT WINS ───────────────────────────────
//
//  1. A HOSTNAME the router's DNS cache holds for the destination address,
//     matched by the longest domain suffix (`teams.microsoft.com` is Teams even
//     though `microsoft.com` is Microsoft). An address can carry several names,
//     because Google, Microsoft and Apple serve many apps from shared fronts; the
//     app earliest in the catalogue wins, and the catalogue puts specific apps
//     before the companies that host them, so mail.google.com is Gmail.
//  2. A WELL-KNOWN PORT: DNS on 53, NTP on 123, Xbox Live on 3074. Before the
//     network owner, because a DNS query to Google's resolver is DNS, not "Google".
//  3. The NETWORK OWNER'S AS number: Zoom's AS30103, Netflix's, Cloudflare's.
//
// Anything else is unidentified, and the page shows it under its network owner.
// That is most of what the catalogue does not know, and some of what it does:
// a device using its own encrypted DNS never asks the router, so its names are
// not in the cache and its traffic falls back to steps 2 and 3.
//
// ── THE ADMINISTRATORS' OWN RULES (#167) ────────────────────────────────────
//
// rules.go: apps an administrator made, and domains, AS numbers and ports added
// to built-in apps, kept in the database and merged over the catalogue by
// WithRules. They are checked FIRST at each step: a rule's domain beats the
// catalogue's domains, its port the catalogue's ports, its AS number the
// catalogue's AS numbers. The steps keep their order, so a hostname the
// catalogue knows still beats a rule's AS number.
package apps

import (
	_ "embed"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

//go:embed catalogue_gen.json
var bundleJSON []byte

// App is one application in the catalogue, as the page draws it.
type App struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	// Color is the brand colour, #RRGGBB.
	Color string `json:"color"`
	// Logo is "mask" (a single-colour shape the page fills), "image" (a
	// full-colour logo) or "" (none: the page draws a monogram).
	Logo string `json:"logo"`
	// Dark: Color would vanish on the dark theme, so a mask logo is filled with
	// the text colour instead.
	Dark bool `json:"dark"`
	// Umbrella marks a company that hosts other apps (Google, Microsoft, Apple):
	// a match on it is the least specific answer the catalogue can give.
	Umbrella bool `json:"umbrella"`
	// Icon is the logo file under web/public/vendor/app-icons when it is not
	// the app's own id: an administrator's app showing a built-in app's logo.
	Icon string `json:"icon,omitempty"`
	// Custom marks an app an administrator made (rules.go).
	Custom bool `json:"custom,omitempty"`
}

// PortRule is a protocol and a port range, both ends included.
type PortRule struct {
	Proto string `json:"proto"`
	From  int    `json:"from"`
	To    int    `json:"to"`
}

type entry struct {
	App
	Domains []string   `json:"domains"`
	ASNs    []uint     `json:"asns"`
	Ports   []PortRule `json:"ports"`
}

// Via says which step named the app.
const (
	ViaName = "name"
	ViaPort = "port"
	ViaASN  = "asn"
)

// UnnamedID is where the traffic no step could name is stored, beside the
// apps' own ids (internal/server/apps_history.go). It can never be an app's:
// cmd/appgen's id rule starts every id with a letter or a digit.
const UnnamedID = "_unnamed"

// Match is Classify's answer. App is nil when nothing matched.
type Match struct {
	App *App
	Via string
}

// Catalogue is the loaded, indexed bundle, and the administrators' rules over
// it (WithRules).
type Catalogue struct {
	apps []App
	base index
	// user is the rules' index, checked before base at every step.
	user index
	// categories is every category id the built-in apps use, in first-use
	// order.
	categories []string
}

// index finds the app (its position in apps) for a name, a port or an AS
// number.
type index struct {
	exact  map[string]int
	suffix map[string]int
	asn    map[uint]int
	ports  []indexedPort
}

func newIndex() index {
	return index{exact: map[string]int{}, suffix: map[string]int{}, asn: map[uint]int{}}
}

type indexedPort struct {
	PortRule
	app int
}

var (
	once    sync.Once
	shared  *Catalogue
	loadErr error
	// current is the catalogue with the administrators' rules merged in, set by
	// SetRules; nil until the first call.
	current atomic.Pointer[Catalogue]
)

// Current is the catalogue every classification uses: the embedded one with
// the administrators' rules over it, or the embedded one alone before any were
// loaded.
func Current() *Catalogue {
	if c := current.Load(); c != nil {
		return c
	}
	return Default()
}

// SetRules merges rules over the embedded catalogue and makes the result
// Current. The rules are trusted to have passed Check.
func SetRules(rules []Rule) { current.Store(Default().WithRules(rules)) }

// Default is the embedded catalogue. It panics only if the embedded bundle is
// not valid JSON, which `appgen -check` and this package's tests rule out.
func Default() *Catalogue {
	once.Do(func() { shared, loadErr = Load(bundleJSON) })
	if loadErr != nil {
		panic("apps: embedded catalogue: " + loadErr.Error())
	}
	return shared
}

// Load indexes a bundle in cmd/appgen's format.
func Load(raw []byte) (*Catalogue, error) {
	var b struct {
		Apps []entry `json:"apps"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	c := &Catalogue{base: newIndex(), user: newIndex()}
	seen := map[string]bool{}
	for i, e := range b.Apps {
		c.apps = append(c.apps, e.App)
		if !seen[e.Category] {
			seen[e.Category] = true
			c.categories = append(c.categories, e.Category)
		}
		// appgen already gave each domain to exactly one app; keeping the first
		// here as well makes the order rule hold even for a bundle built by hand
		// in a test.
		for _, d := range e.Domains {
			if strings.HasPrefix(d, "=") {
				c.base.addName(d[1:], false, i)
			} else {
				c.base.addName(d, true, i)
			}
		}
		for _, n := range e.ASNs {
			c.base.addASN(n, i)
		}
		for _, p := range e.Ports {
			c.base.ports = append(c.base.ports, indexedPort{p, i})
		}
	}
	return c, nil
}

// addName indexes a domain, exactly or with every name under it; the first app
// to claim one keeps it.
func (x *index) addName(d string, subdomains bool, app int) {
	m := x.exact
	if subdomains {
		m = x.suffix
	}
	if _, ok := m[d]; !ok {
		m[d] = app
	}
}

func (x *index) addASN(n uint, app int) {
	if _, ok := x.asn[n]; !ok {
		x.asn[n] = app
	}
}

// Categories is every category the built-in apps use.
func (c *Catalogue) Categories() []string { return c.categories }

// Apps lists the catalogue in its priority order.
func (c *Catalogue) Apps() []App { return c.apps }

// App returns the app with this id.
func (c *Catalogue) App(id string) (App, bool) {
	for _, a := range c.apps {
		if a.ID == id {
			return a, true
		}
	}
	return App{}, false
}

// byName is the app a hostname belongs to: an exact `full:` entry, else the
// longest listed suffix. The index is the app's priority; -1 is no match.
func (x *index) byName(name string) int {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" {
		return -1
	}
	if i, ok := x.exact[name]; ok {
		return i
	}
	for s := name; s != ""; {
		if i, ok := x.suffix[s]; ok {
			return i
		}
		dot := strings.IndexByte(s, '.')
		if dot < 0 {
			break
		}
		s = s[dot+1:]
	}
	return -1
}

// Classify names the app for one destination. `names` are the hostnames the
// DNS cache holds for the address (any order), `asn` the network owner's AS
// number (0 when unknown), `proto` "tcp" or "udp", `port` the destination port
// (0 when unknown).
//
// At each step the administrators' rules are asked first, then the catalogue.
func (c *Catalogue) Classify(names []string, asn uint, proto string, port int) Match {
	for _, x := range [2]*index{&c.user, &c.base} {
		best := -1
		for _, n := range names {
			if i := x.byName(n); i >= 0 && (best < 0 || i < best) {
				best = i
			}
		}
		if best >= 0 {
			return Match{App: &c.apps[best], Via: ViaName}
		}
	}
	if port > 0 {
		for _, x := range [2]*index{&c.user, &c.base} {
			for _, p := range x.ports {
				if p.Proto == proto && port >= p.From && port <= p.To {
					return Match{App: &c.apps[p.app], Via: ViaPort}
				}
			}
		}
	}
	if asn != 0 {
		for _, x := range [2]*index{&c.user, &c.base} {
			if i, ok := x.asn[asn]; ok {
				return Match{App: &c.apps[i], Via: ViaASN}
			}
		}
	}
	return Match{}
}

// TooDark is true when a colour would vanish on the dark theme's surfaces: a
// WCAG relative luminance below 0.07. Apple's, X's and TikTok's black, for
// example; Netflix red and Steam navy-blue stay in colour. cmd/appgen sets
// the catalogue's `dark` with it, and WithRules an administrator's app's.
func TooDark(hexColour string) bool {
	h := strings.TrimPrefix(hexColour, "#")
	if len(h) != 6 {
		return false
	}
	ch := func(i int) float64 {
		v, _ := strconv.ParseUint(h[i:i+2], 16, 8)
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(0)+0.7152*ch(2)+0.0722*ch(4) < 0.07
}
