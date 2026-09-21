package cfgtpl

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// VarDef declares one template variable.
//
// ── WHAT TYPING IS FOR, AND WHAT IT IS NOT ──────────────────────────────────
//
// It is NOT the injection defence. Render writes every value as one inert
// literal whatever it holds (QuoteROS), and TestRenderedValueIsOneInertLiteral
// holds that over hostile input with no typing at all. A second defence that
// the first depended on would be one defence.
//
// It IS correctness, found early and named: `10.0.0.0/33`, `vlan-id=5000` or a
// hostname with a space refused on the field that holds it, before a router
// refuses it at line 40 of a file that has already applied lines 1 to 39 — the
// measured behaviour of a runtime error in /import.
type VarDef struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Label    string   `json:"label,omitempty"`
	Help     string   `json:"help,omitempty"`
	Default  string   `json:"default,omitempty"`
	Required bool     `json:"required,omitempty"`
	Min      *int     `json:"min,omitempty"`
	Max      *int     `json:"max,omitempty"`
	Options  []string `json:"options,omitempty"`
}

// Types is every variable type, for the Editor's picker and for validating a
// definition.
var Types = []string{
	"ip", "ipv4", "ipv6", "cidr", "ifaddr", "hostname", "int", "port", "vlan-id",
	"mac", "iface", "enum", "ident", "duration", "rate", "text", "secret",
	"ipv4-list", "cidr-list", "host-list", "iface-list", "port-list",
}

// listElem is each list type's element type. A list is comma-separated, and
// no element type admits a comma, so splitting it can never cut one element.
var listElem = map[string]string{
	"ipv4-list": "ipv4", "cidr-list": "cidr", "host-list": "hostname", "iface-list": "iface", "port-list": "port",
}

// IsList reports whether a type holds a comma-separated list.
func IsList(typ string) bool { _, ok := listElem[typ]; return ok }

// Secret reports whether a type's values are credentials: masked in the UI,
// never written to the database or an audit row, held in memory only for the
// life of a run.
func Secret(typ string) bool { return typ == "secret" }

// ServerVars are filled by MikroDash, per router, and can never be set by the
// person deploying. They describe MikroDash's OWN way in — the address it
// arrives from as the router sees it (`/user/active`, measured to carry it),
// the API service it uses, the account it logs in as — which is exactly what a
// firewall or services template must keep open, and exactly what an operator
// mistyping would lock out. A template uses them without declaring them.
var ServerVars = map[string]VarDef{
	"mgmt_src":    {Name: "mgmt_src", Type: "ip", Label: "MikroDash's address, as this router sees it"},
	"api_service": {Name: "api_service", Type: "enum", Options: []string{"api", "api-ssl"}, Label: "The API service MikroDash uses"},
	"api_user":    {Name: "api_user", Type: "ident", Label: "The account MikroDash logs in as"},
}

// MaxVars is the most variables one template may declare.
const MaxVars = 32

var (
	hostLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	ifaceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._@+-]{0,63}$`)
	identRE   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,63}$`)
	duration  = regexp.MustCompile(`^(?:(?:\d+[wdhms])+|\d{1,2}:\d{2}:\d{2})$`)
	rateRE    = regexp.MustCompile(`^\d+(?:\.\d+)?[kMG]?$`)
)

// ValidateDefs checks a template's declarations: names, types, bounds, options.
func ValidateDefs(defs []VarDef) error {
	if len(defs) > MaxVars {
		return fmt.Errorf("a template may declare at most %d variables", MaxVars)
	}
	known := map[string]bool{}
	for _, t := range Types {
		known[t] = true
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if !varName.MatchString(d.Name) {
			return fmt.Errorf("%q is not a variable name (lower case, digits and _, starting with a letter)", d.Name)
		}
		if _, server := ServerVars[d.Name]; server {
			return fmt.Errorf("{{%s}} is filled by MikroDash for each router and cannot be declared", d.Name)
		}
		if seen[d.Name] {
			return fmt.Errorf("{{%s}} is declared twice", d.Name)
		}
		seen[d.Name] = true
		if !known[d.Type] {
			return fmt.Errorf("{{%s}}: %q is not a variable type", d.Name, d.Type)
		}
		if d.Min != nil && d.Max != nil && *d.Min > *d.Max {
			return fmt.Errorf("{{%s}}: the minimum is above the maximum", d.Name)
		}
		if d.Type == "enum" {
			if len(d.Options) == 0 {
				return fmt.Errorf("{{%s}}: a choice needs at least one option", d.Name)
			}
			for _, o := range d.Options {
				if !identRE.MatchString(o) {
					return fmt.Errorf("{{%s}}: option %q may hold only letters, digits, . _ and -", d.Name, o)
				}
			}
		}
		if Secret(d.Type) && d.Default != "" {
			return fmt.Errorf("{{%s}}: a secret cannot have a default — it would be stored with the template", d.Name)
		}
		if d.Default != "" {
			if _, err := Validate(d, d.Default); err != nil {
				return fmt.Errorf("{{%s}}: the default is not valid: %s", d.Name, err)
			}
		}
	}
	return nil
}

// CheckDeclared holds a template and its declarations to each other, in BOTH
// directions: a placeholder nothing declares cannot be filled, and a
// declaration nothing uses asks the operator for a value that goes nowhere.
func CheckDeclared(t *Template, defs []VarDef) error {
	declared := map[string]bool{}
	for _, d := range defs {
		declared[d.Name] = true
	}
	used := map[string]bool{}
	for _, v := range t.Vars() {
		used[v] = true
		if _, server := ServerVars[v]; server {
			continue
		}
		if !declared[v] {
			return fmt.Errorf("{{%s}} is used but not declared", v)
		}
	}
	for _, d := range defs {
		if !used[d.Name] {
			return fmt.Errorf("{{%s}} is declared but never used", d.Name)
		}
	}
	return nil
}

// Validate checks one value against its declaration and returns its canonical
// form — the one Render substitutes, so that `010.0.0.1` and ` 10.0.0.1` are
// never sent as themselves.
func Validate(d VarDef, raw string) (string, error) {
	v := raw
	if !utf8.ValidString(v) {
		return "", fmt.Errorf("not valid UTF-8")
	}
	switch d.Type {
	case "text", "secret":
		if len(v) > 255 {
			return "", fmt.Errorf("longer than 255 bytes")
		}
		for _, r := range v {
			if r < 0x20 || r == 0x7f {
				return "", fmt.Errorf("a control character")
			}
		}
		if err := visibleText(v); err != nil {
			return "", err
		}
		if d.Required && v == "" {
			return "", fmt.Errorf("required")
		}
		return v, nil
	}
	// Every other type is a token: no surrounding space, no inner control.
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("empty")
	}
	switch d.Type {
	case "ip":
		a, err := netip.ParseAddr(v)
		if err != nil || a.Zone() != "" || a.Is4In6() {
			return "", fmt.Errorf("%q is not an IP address", v)
		}
		return a.String(), nil
	case "ipv4", "ipv6":
		a, err := netip.ParseAddr(v)
		if err != nil || a.Zone() != "" || a.Is4In6() || a.Is4() != (d.Type == "ipv4") {
			return "", fmt.Errorf("%q is not an %s address", v, strings.ToUpper(d.Type[:2])+d.Type[2:])
		}
		return a.String(), nil
	case "cidr":
		p, err := netip.ParsePrefix(v)
		if err != nil || p.Addr().Zone() != "" {
			return "", fmt.Errorf("%q is not a network such as 192.0.2.0/24", v)
		}
		if p.Bits() == 0 {
			return "", fmt.Errorf("%q matches every address; write it literally in the template if that is meant", v)
		}
		if p != p.Masked() {
			return "", fmt.Errorf("%q has host bits set; the network is %s", v, p.Masked())
		}
		return p.String(), nil
	case "ifaddr":
		p, err := netip.ParsePrefix(v)
		if err != nil || p.Addr().Zone() != "" || p.Bits() == 0 {
			return "", fmt.Errorf("%q is not an interface address such as 192.0.2.1/24", v)
		}
		return p.String(), nil
	case "hostname":
		if len(v) > 253 {
			return "", fmt.Errorf("longer than 253 characters")
		}
		for _, l := range strings.Split(strings.TrimSuffix(v, "."), ".") {
			if !hostLabel.MatchString(l) {
				return "", fmt.Errorf("%q is not a hostname", v)
			}
		}
		return strings.ToLower(v), nil
	case "int", "port", "vlan-id":
		lo, hi := 0, 1<<31-1
		switch d.Type {
		case "port":
			lo, hi = 1, 65535
		case "vlan-id":
			lo, hi = 1, 4094
		}
		if d.Min != nil {
			lo = *d.Min
		}
		if d.Max != nil {
			hi = *d.Max
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return "", fmt.Errorf("%q is not a whole number", v)
		}
		if n < lo || n > hi {
			return "", fmt.Errorf("%d is outside %d–%d", n, lo, hi)
		}
		return strconv.Itoa(n), nil
	case "mac":
		m, err := net.ParseMAC(v)
		if err != nil || len(m) != 6 {
			return "", fmt.Errorf("%q is not a MAC address", v)
		}
		return strings.ToUpper(m.String()), nil
	case "iface":
		if !ifaceName.MatchString(v) {
			return "", fmt.Errorf("%q is not an interface name", v)
		}
		return v, nil
	case "enum":
		for _, o := range d.Options {
			if v == o {
				return v, nil
			}
		}
		return "", fmt.Errorf("%q is not one of %s", v, strings.Join(d.Options, ", "))
	case "ident":
		if !identRE.MatchString(v) {
			return "", fmt.Errorf("%q may hold only letters, digits, . _ and -", v)
		}
		return v, nil
	case "duration":
		if !duration.MatchString(v) {
			return "", fmt.Errorf("%q is not a duration such as 30m, 1h, 1d or 00:10:00", v)
		}
		return v, nil
	case "rate":
		if !rateRE.MatchString(v) {
			return "", fmt.Errorf("%q is not a rate such as 90M or 500k", v)
		}
		return v, nil
	case "ipv4-list", "cidr-list", "host-list", "iface-list", "port-list":
		elem := VarDef{Type: listElem[d.Type]}
		items := strings.Split(v, ",")
		if len(items) > 16 {
			return "", fmt.Errorf("more than 16 entries")
		}
		out := make([]string, 0, len(items))
		for _, it := range items {
			c, err := Validate(elem, it)
			if err != nil {
				return "", err
			}
			out = append(out, c)
		}
		return strings.Join(out, ","), nil
	}
	return "", fmt.Errorf("unknown type %q", d.Type)
}

// FieldErrors maps a variable's name to what is wrong with its value.
type FieldErrors map[string]string

func (f FieldErrors) Error() string {
	names := make([]string, 0, len(f))
	for n := range f {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, "{{"+n+"}}: "+f[n])
	}
	return strings.Join(parts, "; ")
}

// resolveValues turns what the operator typed and what MikroDash knows about the
// router into the values Render substitutes.
//
// A server variable the operator tried to set is REFUSED, not overwritten: a
// value typed into it is a sign the operator believes they are choosing it, and
// silently replacing it would leave them believing that.
func resolveValues(defs []VarDef, given, server map[string]string) (map[string]string, error) {
	errs := FieldErrors{}
	out := map[string]string{}
	for name := range given {
		if _, s := ServerVars[name]; s {
			errs[name] = "filled by MikroDash for each router, and cannot be set"
		}
	}
	for name, d := range ServerVars {
		if v, ok := server[name]; ok {
			c, err := Validate(d, v)
			if err != nil {
				errs[name] = "MikroDash could not determine it: " + err.Error()
				continue
			}
			out[name] = c
		}
	}
	for _, d := range defs {
		v, ok := given[d.Name]
		if !ok || (v == "" && d.Default != "") {
			v, ok = d.Default, d.Default != ""
		}
		// An optional list may be empty: no ports, no servers. Only a list
		// can say "none" without the value itself becoming empty text.
		if IsList(d.Type) && !d.Required && strings.TrimSpace(v) == "" {
			out[d.Name] = ""
			continue
		}
		if !ok {
			// A missing text or secret is an empty string unless required; a
			// missing value of any other type cannot be empty, so it is
			// required whether or not the declaration says so.
			if d.Required || (!Secret(d.Type) && d.Type != "text") {
				errs[d.Name] = "required"
			} else {
				out[d.Name] = ""
			}
			continue
		}
		c, err := Validate(d, v)
		if err != nil {
			errs[d.Name] = err.Error()
			continue
		}
		out[d.Name] = c
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}
