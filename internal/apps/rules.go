package apps

// The administrators' own apps and matching rules (#167). Pure: rules in, a
// verdict or a merged catalogue out. The rules are kept in the database
// (internal/db/apprules.go) and served by internal/server/apps_rules.go.
//
// ── TWO KINDS OF RULE, ONE SHAPE ────────────────────────────────────────────
//
//   - A CUSTOM APP: a name, a category, a colour and an icon (a built-in app's
//     logo, or none for a monogram), and the domains, AS numbers and ports that
//     name it. Its id starts with CustomPrefix, which cmd/appgen's id rule
//     forbids, so it can never be a built-in app's, now or in a later catalogue.
//   - AN ADDITION TO A BUILT-IN APP: the app's own id, Builtin set, and the
//     domains, AS numbers and ports to add to it; Category, when set, moves it
//     to another category. Its name, colour and logo stay the catalogue's.
//
// ── PRECEDENCE ──────────────────────────────────────────────────────────────
//
// At each of Classify's steps the rules are asked before the catalogue
// (apps.go). Within the rules, a name goes to the longest matching domain, as
// in the catalogue. A connection is named once, by the first answer, so no
// traffic is counted twice. Two rules may not claim the same domain, AS number
// or port: Check refuses the second. A rule MAY take a domain, AS number or
// port from a built-in app, and Check says so before it is saved.
//
// ── NOTHING IS RECLASSIFIED AFTER THE FACT ──────────────────────────────────
//
// A rule names the traffic that arrives after it is saved. Past minutes keep
// the app they were counted under: the stored history holds app ids and bytes,
// not the names or addresses that would be needed to name them again.

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CustomPrefix starts every id an administrator's app or category is given.
// The underscore is outside cmd/appgen's id rule.
const CustomPrefix = "u_"

// DomainRule is one domain a rule claims: the name itself, and with
// Subdomains every name under it.
type DomainRule struct {
	Domain     string `json:"domain"`
	Subdomains bool   `json:"subdomains"`
}

// Rule is one custom app, or one built-in app's additions.
type Rule struct {
	ID string `json:"id"`
	// Builtin: the rule adds to the built-in app with this id.
	Builtin bool `json:"builtin"`
	// Name, Color and Icon are a custom app's; a built-in keeps its own.
	Name  string `json:"name"`
	Color string `json:"color"`
	// Icon is the id of the built-in app whose logo a custom app shows, or ""
	// for a monogram in its colour. No file is ever uploaded.
	Icon string `json:"icon"`
	// Category: a custom app's ("" is Uncategorized), or a built-in app's new
	// one ("" keeps the catalogue's).
	Category string       `json:"category"`
	Domains  []DomainRule `json:"domains"`
	ASNs     []uint       `json:"asns"`
	Ports    []PortRule   `json:"ports"`
}

// Category is an administrator's category, beside the built-in ones.
type Category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Tone is the pill colour, one of CategoryTones: the theme's, not a hex.
	Tone string `json:"tone"`
}

// CategoryTones are the pill tones a category may take (web/src/palette.ts's
// PillTone).
var CategoryTones = []string{"rx", "tx", "ok", "warn", "err", "alt", "pink", "muted"}

// Limits on one rule, so a rule cannot grow the index without bound.
const (
	maxDomains   = 100
	maxASNs      = 50
	maxPorts     = 20
	maxName      = 40
	maxCatName   = 30
	maxIDSlug    = 40
	maxDomainLen = 253
)

var (
	colourRule = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	labelRule  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	customID   = regexp.MustCompile(`^u_[a-z0-9][a-z0-9-]{0,39}$`)
)

// webPorts may not be claimed by a port rule: nearly all web traffic uses them,
// so a rule on them would name most of the network one app.
var webPorts = []int{80, 443}

// WithRules is the catalogue with rules merged over it. A rule naming an app
// that does not exist, or a custom id already taken, is skipped; Check refuses
// both before they are saved.
func (c *Catalogue) WithRules(rules []Rule) *Catalogue {
	out := &Catalogue{apps: append([]App(nil), c.apps...), base: c.base, user: newIndex(), categories: c.categories}
	pos := make(map[string]int, len(out.apps))
	for i, a := range out.apps {
		pos[a.ID] = i
	}
	for _, r := range rules {
		i, ok := pos[r.ID]
		if r.Builtin {
			if !ok || out.apps[i].Custom {
				continue
			}
			if r.Category != "" {
				out.apps[i].Category = r.Category
			}
		} else {
			if ok {
				continue
			}
			a := App{ID: r.ID, Name: r.Name, Category: r.Category, Color: r.Color, Custom: true}
			if src, found := pos[r.Icon]; found && r.Icon != "" && !out.apps[src].Custom && out.apps[src].Logo != "" {
				a.Icon, a.Logo = r.Icon, out.apps[src].Logo
				// A mask is filled with the app's own colour, so whether it shows
				// on the dark theme is that colour's question.
				a.Dark = a.Logo == "mask" && TooDark(a.Color)
			}
			out.apps = append(out.apps, a)
			i = len(out.apps) - 1
			pos[r.ID] = i
		}
		for _, d := range r.Domains {
			out.user.addName(d.Domain, d.Subdomains, i)
		}
		for _, n := range r.ASNs {
			out.user.addASN(n, i)
		}
		for _, p := range r.Ports {
			out.user.ports = append(out.user.ports, indexedPort{p, i})
		}
	}
	return out
}

// Matches is whether the rule alone would name a destination: the preview's
// question. Pure.
func (r Rule) Matches(names []string, asn uint, proto string, port int) bool {
	for _, n := range names {
		n = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(n)), ".")
		for _, d := range r.Domains {
			if n == d.Domain || (d.Subdomains && strings.HasSuffix(n, "."+d.Domain)) {
				return true
			}
		}
	}
	if port > 0 {
		for _, p := range r.Ports {
			if p.Proto == proto && port >= p.From && port <= p.To {
				return true
			}
		}
	}
	for _, n := range r.ASNs {
		if asn != 0 && n == asn {
			return true
		}
	}
	return false
}

// Conflict is one thing a rule claims that something else claims too.
type Conflict struct {
	// Kind is "domain", "asn" or "port"; Value what is claimed, as shown.
	Kind  string `json:"kind"`
	Value string `json:"value"`
	// AppID and App are the other claimant.
	AppID string `json:"appId"`
	App   string `json:"app"`
	// Blocking: another rule claims it, and the rule cannot be saved until one
	// of them gives it up. Otherwise a built-in app claims it, and the rule
	// would take that traffic from it, which is allowed.
	Blocking bool   `json:"blocking"`
	Note     string `json:"note"`
}

// Check cleans a rule and judges it against the catalogue (c, without rules),
// the other saved rules and the categories that exist. A custom rule with no
// id is given one from its name. The error is the first thing wrong with the
// rule itself; the conflicts are what it shares with others. Pure.
func (c *Catalogue) Check(r Rule, others []Rule, categories map[string]bool) (Rule, []Conflict, error) {
	r, err := normalize(r)
	if err != nil {
		return r, nil, err
	}
	if r.Builtin {
		a, ok := c.App(r.ID)
		if !ok || a.Custom {
			return r, nil, errors.New("there is no built-in app " + strconv.Quote(r.ID))
		}
		r.Name, r.Color, r.Icon = a.Name, "", ""
	} else {
		if r.ID == "" {
			r.ID = freeID(slug(r.Name), others)
		}
		if !customID.MatchString(r.ID) {
			return r, nil, errors.New("an app's id is u_ then lowercase letters, digits and hyphens")
		}
		if r.Icon != "" {
			if a, ok := c.App(r.Icon); !ok || a.Custom || a.Logo == "" {
				return r, nil, errors.New("there is no logo " + strconv.Quote(r.Icon))
			}
		}
	}
	if r.Category != "" && !categories[r.Category] {
		return r, nil, errors.New("there is no category " + strconv.Quote(r.Category))
	}
	return r, c.conflicts(r, others), nil
}

// conflicts is what the rule shares with the other rules (blocking) and the
// built-in apps (not).
func (c *Catalogue) conflicts(r Rule, others []Rule) []Conflict {
	out := []Conflict{}
	own := func(i int) bool { return r.Builtin && c.apps[i].ID == r.ID }
	add := func(kind, value string, i int, note string) {
		out = append(out, Conflict{Kind: kind, Value: value, AppID: c.apps[i].ID, App: c.apps[i].Name, Note: note})
	}
	for _, o := range others {
		if o.ID == r.ID {
			continue
		}
		block := func(kind, value string) {
			out = append(out, Conflict{Kind: kind, Value: value, AppID: o.ID, App: ruleName(c, o), Blocking: true,
				Note: "already claimed by another rule"})
		}
		for _, d := range r.Domains {
			for _, od := range o.Domains {
				if d.Domain == od.Domain {
					block("domain", d.Domain)
				}
			}
		}
		for _, n := range r.ASNs {
			for _, on := range o.ASNs {
				if n == on {
					block("asn", "AS"+strconv.FormatUint(uint64(n), 10))
				}
			}
		}
		for _, p := range r.Ports {
			for _, op := range o.Ports {
				if p.Proto == op.Proto && p.From <= op.To && op.From <= p.To {
					block("port", portText(p))
				}
			}
		}
	}
	for _, d := range r.Domains {
		if i, ok := c.base.exact[d.Domain]; ok && !own(i) {
			add("domain", d.Domain, i, "named "+c.apps[i].Name+" by the catalogue; this rule takes it")
		} else if i, ok := c.base.suffix[d.Domain]; ok && !own(i) {
			add("domain", d.Domain, i, "named "+c.apps[i].Name+" by the catalogue; this rule takes it")
		} else if i, parent := c.parentOf(d.Domain); i >= 0 && !own(i) {
			add("domain", d.Domain, i, "inside "+parent+", which the catalogue names "+c.apps[i].Name+"; this rule takes this part")
		}
		if d.Subdomains {
			for _, ch := range c.childrenOf(d.Domain) {
				if !own(ch.app) {
					add("domain", ch.name, ch.app, "under "+d.Domain+", named "+c.apps[ch.app].Name+
						" by the catalogue; this rule takes it")
				}
			}
		}
	}
	for _, n := range r.ASNs {
		if i, ok := c.base.asn[n]; ok && !own(i) {
			add("asn", "AS"+strconv.FormatUint(uint64(n), 10), i, "the catalogue names this network "+c.apps[i].Name+"; this rule takes it")
		}
	}
	for _, p := range r.Ports {
		for _, bp := range c.base.ports {
			if p.Proto == bp.Proto && p.From <= bp.To && bp.From <= p.To && !own(bp.app) {
				add("port", portText(p), bp.app, "the catalogue names "+portText(bp.PortRule)+" "+c.apps[bp.app].Name+"; this rule takes it")
			}
		}
	}
	return out
}

// parentOf is the nearest domain above `d` the catalogue names with its
// subdomains, and its app; -1 when there is none.
func (c *Catalogue) parentOf(d string) (int, string) {
	for s := d; ; {
		dot := strings.IndexByte(s, '.')
		if dot < 0 {
			return -1, ""
		}
		s = s[dot+1:]
		if i, ok := c.base.suffix[s]; ok {
			return i, s
		}
	}
}

type childName struct {
	name string
	app  int
}

// childrenOf is the catalogue's domains under `d`, one per app, at most five.
func (c *Catalogue) childrenOf(d string) []childName {
	tail := "." + d
	byApp := map[int]string{}
	for _, m := range []map[string]int{c.base.suffix, c.base.exact} {
		for n, i := range m {
			if strings.HasSuffix(n, tail) {
				if cur, ok := byApp[i]; !ok || n < cur {
					byApp[i] = n
				}
			}
		}
	}
	out := make([]childName, 0, len(byApp))
	for i, n := range byApp {
		out = append(out, childName{n, i})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

func ruleName(c *Catalogue, r Rule) string {
	if r.Builtin {
		if a, ok := c.App(r.ID); ok {
			return a.Name
		}
	}
	return r.Name
}

func portText(p PortRule) string {
	if p.From == p.To {
		return strconv.Itoa(p.From) + "/" + p.Proto
	}
	return strconv.Itoa(p.From) + "-" + strconv.Itoa(p.To) + "/" + p.Proto
}

// normalize cleans what an administrator typed and refuses what cannot be
// used.
func normalize(r Rule) (Rule, error) {
	r.ID = strings.TrimSpace(r.ID)
	r.Category = strings.TrimSpace(r.Category)
	if !r.Builtin {
		name, err := cleanText(r.Name, maxName, "an app's name")
		if err != nil {
			return r, err
		}
		r.Name = name
		r.Color = strings.ToLower(strings.TrimSpace(r.Color))
		if !colourRule.MatchString(r.Color) {
			return r, errors.New("a colour is # and six hex digits")
		}
		r.Icon = strings.TrimSpace(r.Icon)
	}

	if len(r.Domains) > maxDomains {
		return r, fmt.Errorf("at most %d domains", maxDomains)
	}
	seen := map[string]bool{}
	domains := make([]DomainRule, 0, len(r.Domains))
	for _, d := range r.Domains {
		clean, sub, err := cleanDomain(d.Domain)
		if err != nil {
			return r, err
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		domains = append(domains, DomainRule{Domain: clean, Subdomains: d.Subdomains || sub})
	}
	r.Domains = domains

	if len(r.ASNs) > maxASNs {
		return r, fmt.Errorf("at most %d networks", maxASNs)
	}
	asns := make([]uint, 0, len(r.ASNs))
	seenASN := map[uint]bool{}
	for _, n := range r.ASNs {
		if n == 0 || n > 4294967294 {
			return r, errors.New("an AS number is 1 to 4294967294")
		}
		if !seenASN[n] {
			seenASN[n] = true
			asns = append(asns, n)
		}
	}
	r.ASNs = asns

	if len(r.Ports) > maxPorts {
		return r, fmt.Errorf("at most %d ports", maxPorts)
	}
	ports := make([]PortRule, 0, len(r.Ports))
	for _, p := range r.Ports {
		p.Proto = strings.ToLower(strings.TrimSpace(p.Proto))
		if p.Proto != "tcp" && p.Proto != "udp" {
			return r, errors.New("a port's protocol is tcp or udp")
		}
		if p.To == 0 {
			p.To = p.From
		}
		if p.From < 1 || p.To > 65535 || p.From > p.To {
			return r, errors.New("a port is 1 to 65535, and a range runs low to high")
		}
		for _, w := range webPorts {
			if p.From <= w && w <= p.To {
				return r, fmt.Errorf("port %d carries most web traffic, so it cannot name one app; use a domain or a network instead", w)
			}
		}
		ports = append(ports, p)
	}
	r.Ports = ports
	return r, nil
}

// cleanDomain lowercases a domain and drops what people paste around one: a
// scheme, a path, a port, a trailing dot. A leading "*." asks for its
// subdomains.
func cleanDomain(s string) (string, bool, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, ".")
	sub := false
	if strings.HasPrefix(s, "*.") {
		s, sub = s[2:], true
	}
	bad := errors.New(strconv.Quote(s) + " is not a domain name")
	if s == "" || len(s) > maxDomainLen || !strings.Contains(s, ".") {
		return s, sub, bad
	}
	for _, l := range strings.Split(s, ".") {
		if !labelRule.MatchString(l) {
			return s, sub, bad
		}
	}
	return s, sub, nil
}

// cleanText trims a name and refuses an empty one, an over-long one, and
// control characters.
func cleanText(s string, max int, what string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return s, errors.New(what + " is required")
	}
	if utf8.RuneCountInString(s) > max {
		return s, fmt.Errorf("%s is at most %d characters", what, max)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return s, errors.New(what + " cannot hold control characters")
		}
	}
	return s, nil
}

// slug is a name as an id's tail: lowercase letters and digits, hyphens
// between words.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > maxIDSlug-3 {
		s = strings.Trim(s[:maxIDSlug-3], "-")
	}
	if s == "" {
		s = "app"
	}
	return s
}

// freeID is CustomPrefix+base, or with -2, -3... when that is taken.
func freeID(base string, others []Rule) string {
	taken := map[string]bool{}
	for _, o := range others {
		taken[o.ID] = true
	}
	id := CustomPrefix + base
	for n := 2; taken[id]; n++ {
		id = CustomPrefix + base + "-" + strconv.Itoa(n)
	}
	return id
}

// CheckCategory cleans a category and gives a new one its id. `builtin` is
// the catalogue's categories, `others` the custom ones saved. Pure.
func CheckCategory(cat Category, builtin []string, others []Category) (Category, error) {
	name, err := cleanText(cat.Name, maxCatName, "a category's name")
	if err != nil {
		return cat, err
	}
	cat.Name = name
	cat.Tone = strings.TrimSpace(cat.Tone)
	ok := false
	for _, t := range CategoryTones {
		ok = ok || t == cat.Tone
	}
	if !ok {
		return cat, errors.New("a category's colour is one of " + strings.Join(CategoryTones, ", "))
	}
	for _, o := range others {
		if o.ID != cat.ID && strings.EqualFold(o.Name, cat.Name) {
			return cat, errors.New("there is already a category " + strconv.Quote(o.Name))
		}
	}
	for _, b := range builtin {
		if strings.EqualFold(b, cat.Name) {
			return cat, errors.New(strconv.Quote(cat.Name) + " is a built-in category")
		}
	}
	if cat.ID == "" {
		taken := make([]Rule, 0, len(others))
		for _, o := range others {
			taken = append(taken, Rule{ID: o.ID})
		}
		cat.ID = freeID(slug(cat.Name), taken)
	} else if !customID.MatchString(cat.ID) {
		return cat, errors.New("a category's id is u_ then lowercase letters, digits and hyphens")
	}
	return cat, nil
}
