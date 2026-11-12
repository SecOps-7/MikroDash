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
package apps

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
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
}

type portRule struct {
	Proto string `json:"proto"`
	From  int    `json:"from"`
	To    int    `json:"to"`
}

type entry struct {
	App
	Domains []string   `json:"domains"`
	ASNs    []uint     `json:"asns"`
	Ports   []portRule `json:"ports"`
}

// Via says which step named the app.
const (
	ViaName = "name"
	ViaPort = "port"
	ViaASN  = "asn"
)

// Match is Classify's answer. App is nil when nothing matched.
type Match struct {
	App *App
	Via string
}

// Catalogue is the loaded, indexed bundle.
type Catalogue struct {
	apps   []App
	exact  map[string]int
	suffix map[string]int
	asn    map[uint]int
	ports  []indexedPort
}

type indexedPort struct {
	portRule
	app int
}

var (
	once    sync.Once
	shared  *Catalogue
	loadErr error
)

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
	c := &Catalogue{exact: map[string]int{}, suffix: map[string]int{}, asn: map[uint]int{}}
	for i, e := range b.Apps {
		c.apps = append(c.apps, e.App)
		for _, d := range e.Domains {
			// appgen already gave each domain to exactly one app; keeping the
			// first here as well makes the order rule hold even for a bundle
			// built by hand in a test.
			if strings.HasPrefix(d, "=") {
				if _, ok := c.exact[d[1:]]; !ok {
					c.exact[d[1:]] = i
				}
			} else if _, ok := c.suffix[d]; !ok {
				c.suffix[d] = i
			}
		}
		for _, n := range e.ASNs {
			if _, ok := c.asn[n]; !ok {
				c.asn[n] = i
			}
		}
		for _, p := range e.Ports {
			c.ports = append(c.ports, indexedPort{p, i})
		}
	}
	return c, nil
}

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
func (c *Catalogue) byName(name string) int {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" {
		return -1
	}
	if i, ok := c.exact[name]; ok {
		return i
	}
	for s := name; s != ""; {
		if i, ok := c.suffix[s]; ok {
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
func (c *Catalogue) Classify(names []string, asn uint, proto string, port int) Match {
	best := -1
	for _, n := range names {
		if i := c.byName(n); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	if best >= 0 {
		return Match{App: &c.apps[best], Via: ViaName}
	}
	if port > 0 {
		for _, p := range c.ports {
			if p.Proto == proto && port >= p.From && port <= p.To {
				return Match{App: &c.apps[p.app], Via: ViaPort}
			}
		}
	}
	if asn != 0 {
		if i, ok := c.asn[asn]; ok {
			return Match{App: &c.apps[i], Via: ViaASN}
		}
	}
	return Match{}
}
