package resource

// The write engine — the port of src/routeros/resources.js, carrying only what
// dnsStatic needs so far.
//
// The shape is kept faithfully even though one resource does not need all of
// it, because the shape IS the safety argument. Three properties in particular
// are not conveniences and must survive the port intact:
//
//  1. buildArgs takes the OUTPUT of Validate, never raw input. Passing raw
//     values would defeat the allow-list: only declared fields, and only after
//     they have been through a type, ever become a word on the wire.
//  2. `clearable` decides what an EMPTY field means. Without it an edit could
//     never remove a comment, because an omitted argument leaves the router's
//     value alone. Everything not clearable is skipped when blank, so a create
//     does not set a pile of empty properties.
//  3. A `secret` left blank means "leave it alone", never "clear it". Clearing
//     a pre-shared key by forgetting to retype it would silently weaken a
//     tunnel. No dnsStatic field is secret; the rule is here because the next
//     resource ported will have one.

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Type is a field's value domain.
type Type string

const (
	TypeText   Type = "text"
	TypeSecret Type = "secret"
	TypeIP     Type = "ip"
	// TypeWgKey is a WireGuard key: 44 characters of base64 ending in '='.
	//
	// Case-SENSITIVE, and the padding is part of the pattern. A WireGuard key is
	// exactly 32 bytes encoded, so any other length is a different key rather
	// than a typo worth repairing — which is why this only trims surrounding
	// whitespace and otherwise takes the value as given.
	TypeWgKey Type = "wgkey"
	// TypeMac is a colon-separated MAC, upper-cased on the way through: the
	// router accepts either case and returns upper, so normalising here means a
	// form submitted in lower case does not read as a change on the next diff.
	TypeMac Type = "mac"
	// TypeCidr accepts an address OR a prefix — `0.0.0.0/0` and `198.51.100.1`
	// are both valid destinations for a route.
	TypeCidr   Type = "cidr"
	TypeInt    Type = "int"
	TypeBool   Type = "bool"
	TypeSelect Type = "select"
)

// OptionsFrom is where a field's picker list comes from.
//
// Two shapes, and the difference is whether a router has to be asked. `Values`
// is a fixed vocabulary — firewall chains, WPA modes — and ships as declared.
// `Menu` names a RouterOS menu to read, taking the distinct non-empty values of
// one property.
//
// WHY THIS MATTERS MORE THAN A CONVENIENCE. The live form renders a SELECT
// whenever the router supplied choices, even for a field whose declared type is
// free text — "the router told us what this field may be, so offer that rather
// than a blank box". Without it the VLAN parent and both bridge-port fields are
// plain text boxes here and pickers there, which is a rendering difference on a
// page otherwise byte-identical. It went unnoticed because the DOM comparison
// checks the PAGE and never opens the edit form.
type OptionsFrom struct {
	Menu  string // e.g. "/interface"
	Value string // the property to take, e.g. "name"
	// Values is a fixed list needing no read. Mutually exclusive with Menu.
	Values []string
}

// ShowIf makes a field conditional on another's value.
type ShowIf struct {
	Field string
	In    []string
}

// Field is one form field.
type Field struct {
	// OptionsFrom populates a picker. Nil leaves the field as its declared type.
	OptionsFrom *OptionsFrom

	Name  string // the wire name the browser uses
	ROS   string // the RouterOS property
	Label string
	Type  Type

	Required bool
	// Clearable means "send this even when empty, so the operator can empty it".
	Clearable   bool
	Options     []string
	Min, Max    *int
	ShowIf      *ShowIf
	Placeholder string
	Help        string
}

// input is the HTML input type for a field, matching TYPES[...].input on the
// Node side. The browser renders from this rather than from the semantic type,
// so the two must not be conflated: `secret` and `text` are different types
// that both render as an input, and `ip` renders as plain text.
func (f Field) input() string {
	switch f.Type {
	case TypeSecret:
		return "password"
	case TypeInt:
		return "number"
	case TypeBool:
		return "checkbox"
	case TypeSelect:
		return "select"
	default:
		return "text"
	}
}

// Resource is one editable menu.
type Resource struct {
	Key   string
	Page  string
	Label string
	Title string
	Menu  string
	// Identity is the field, or FIELDS, whose values answer "is this still the
	// row I was looking at when I clicked". A firewall rule has no name and
	// nothing unique about it, so it takes a composite — see IdentityOf.
	Identity []string
	Fields   []Field
	// ReadOnlyWhen refuses a row the app cannot correctly edit, judged on a
	// freshly-read row rather than on the browser's claim about it.
	ReadOnlyWhen func(row map[string]string) bool
	// ReadOnlyReason is the code the page shows.
	ReadOnlyReason string

	// RemovableWhen is a SEPARATE predicate from ReadOnlyWhen, because the two
	// answer different questions and one row can be yes to the first and no to
	// the second. A master radio is hardware: it can be edited and disabled, but
	// RouterOS will not delete it. Saying that through ReadOnlyWhen would block
	// the edit as well.
	//
	// Nil means "removable whenever it is editable", which is every resource
	// ported before wifiNet.
	RemovableWhen func(row map[string]string) bool

	// Actions are the named verbs this resource offers. See Action.
	Actions []Action

	// Guard names the checks this resource's writes must pass. They answer
	// different questions and more than one can be true of a write; the FIRST
	// warn wins, because a second dialog after the first is answered is how
	// somebody learns to click both without reading either.
	Guard []string
	// GuardInterfaceFields are the fields whose values are interface names, so
	// selfPath knows what the row is about.
	GuardInterfaceFields []string
	// GuardDisruptiveFields are fields whose CHANGE cuts a link even though the
	// interface keeps its name and stays enabled — a wireless SSID or
	// passphrase drops every client on the radio, management path included.
	GuardDisruptiveFields []string

	// Ordered marks a resource whose ROW ORDER is meaningful — the firewall,
	// where a rule's position decides whether it is ever reached. The page draws
	// reorder arrows for it.
	//
	// RequiresMenu names a menu that must answer before the resource is offered
	// at all. VETH ships with the containers package, and reading the menu is
	// the only way to know: the package list would say the package is installed
	// without saying THIS API user can reach the menu.
	//
	// Both are zero for every resource ported so far, and neither is read by
	// anything here yet. They exist so `TestPortedFieldsMatchTheirLiveDeclarations`
	// can compare them against the live declaration — porting `fwFilter` or
	// `veth` without handling them would otherwise ship a silent `false`.
	Ordered      bool
	RequiresMenu string

	// Check is a whole-submission rule the per-field checks cannot express,
	// because it is about the RELATIONSHIP between two fields. It runs after
	// every field has passed, on the cleaned values.
	Check func(clean map[string]string) []Error
}

// GuardTargets is the interface names a write is about, or none when the edit
// is harmless.
//
// A comment or an MTU change on the bridge we are reachable through is not
// worth a warning, and warning about it is how a warning becomes furniture. So
// an update only counts when it disables the row, renames one of the interface
// fields, or changes a field the resource declares disruptive. A delete always
// counts.
func (r *Resource) GuardTargets(action string, values, before map[string]string) []string {
	names := r.GuardInterfaceFields
	if len(names) == 0 {
		return nil
	}
	of := func(row map[string]string, name string) string {
		if row == nil {
			return ""
		}
		for _, f := range r.Fields {
			if f.Name == name {
				return row[f.ROS]
			}
		}
		return ""
	}
	nonEmpty := func(in []string) []string {
		var out []string
		for _, v := range in {
			if v != "" {
				out = append(out, v)
			}
		}
		return out
	}

	after := make([]string, 0, len(names))
	beforeNames := make([]string, 0, len(names))
	for _, n := range names {
		after = append(after, values[n])
		beforeNames = append(beforeNames, of(before, n))
	}

	if action == "delete" {
		return nonEmpty(beforeNames)
	}
	if before == nil {
		return nil // a create cuts nothing that exists
	}

	wasEnabled := before["disabled"] != "true"
	// Validated values carry RouterOS spellings, so a checkbox reads "yes".
	nowDisabled := values["disabled"] == "yes"
	renamed := false
	for i, n := range names {
		if of(before, n) != values[n] {
			renamed = true
			break
		}
		_ = i
	}
	disruptive := false
	for _, n := range r.GuardDisruptiveFields {
		next, present := values[n]
		if !present {
			continue
		}
		var typ Type
		for _, f := range r.Fields {
			if f.Name == n {
				typ = f.Type
			}
		}
		// A secret never reads back, so any value submitted for one is a change.
		if typ == TypeSecret {
			if next != "" {
				disruptive = true
			}
		} else if next != of(before, n) {
			disruptive = true
		}
		if disruptive {
			break
		}
	}

	if !renamed && !disruptive && !(wasEnabled && nowDisabled) {
		return nil
	}
	return nonEmpty(append(after, beforeNames...))
}

// Error is one field-level rejection.
type Error struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Validated is a checked submission, and the only thing BuildArgs accepts.
type Validated struct {
	Values  map[string]string
	Editing bool
}

var ctrl = func(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func (f Field) check(raw string) (string, string) {
	s := strings.TrimSpace(raw)
	switch f.Type {
	case TypeText, TypeSecret:
		if ctrl(s) {
			return "", "contains a control character"
		}
		max := 255
		if f.Max != nil {
			max = *f.Max
		}
		if len(s) > max {
			return "", fmt.Sprintf("is longer than %d characters", max)
		}
		return s, ""
	case TypeIP:
		// ipaddr.js isValid() on the Node side. Go is stricter about a leading
		// zero in an octet, which RouterOS rejects anyway.
		if net.ParseIP(s) == nil {
			return "", "is not an IP address"
		}
		return s, ""
	case TypeMac:
		up := strings.ToUpper(s)
		if !macRe.MatchString(up) {
			return "", "is not a MAC address (AA:BB:CC:DD:EE:FF)"
		}
		return up, ""
	case TypeWgKey:
		if !wgKeyRe.MatchString(s) {
			return "", "is not a 44-character WireGuard key"
		}
		return s, ""
	case TypeCidr:
		// `ipaddr.parseCIDR(s)` when there is a slash, `ipaddr.parse(s)` when
		// there is not — a route destination is legitimately either. The value
		// is returned UNCHANGED rather than normalised to its network address:
		// RouterOS stores what it was given, and rewriting `198.51.100.5/24`
		// to `198.51.100.0/24` on the way past would edit the operator's entry.
		if s == "" {
			return "", "is required"
		}
		if strings.Contains(s, "/") {
			if _, _, err := net.ParseCIDR(s); err != nil {
				return "", "is not an address or prefix"
			}
			return s, ""
		}
		if net.ParseIP(s) == nil {
			return "", "is not an address or prefix"
		}
		return s, ""
	case TypeInt:
		n, err := strconv.Atoi(s)
		if err != nil {
			return "", "is not a whole number"
		}
		if f.Min != nil && n < *f.Min {
			return "", fmt.Sprintf("is below %d", *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return "", fmt.Sprintf("is above %d", *f.Max)
		}
		return strconv.Itoa(n), ""
	case TypeBool:
		if s == "true" || s == "yes" {
			return "yes", ""
		}
		return "no", ""
	case TypeSelect:
		for _, o := range f.Options {
			if o == s {
				return s, ""
			}
		}
		return "", "is not one of the allowed values"
	}
	return "", "has an unknown type"
}

// Applies reports whether a field is in play, given what the operator filled in.
func (f Field) Applies(values map[string]string) bool {
	if f.ShowIf == nil {
		return true
	}
	v := values[f.ShowIf.Field]
	for _, want := range f.ShowIf.In {
		if want == v {
			return true
		}
	}
	return false
}

// Validate checks a submission against the resource's own fields.
//
// A required field is required in both directions — an edit sends the whole
// form, not a patch — so `editing` changes nothing here. It is carried through
// to BuildArgs, which is where the two differ.
func (r *Resource) Validate(values map[string]string, editing bool) (Validated, []Error) {
	var errs []Error
	clean := map[string]string{}

	for _, f := range r.Fields {
		if !f.Applies(values) {
			continue
		}
		raw, present := values[f.Name]
		blank := !present || strings.TrimSpace(raw) == ""

		if blank {
			// A checkbox that is off is a value, not an omission.
			if f.Type == TypeBool {
				v, _ := f.check(raw)
				clean[f.Name] = v
				continue
			}
			if f.Required {
				errs = append(errs, Error{f.Name, f.Label + " is required"})
			}
			continue
		}
		v, msg := f.check(raw)
		if msg != "" {
			errs = append(errs, Error{f.Name, f.Label + " " + msg})
			continue
		}
		clean[f.Name] = v
	}
	// The cross-field rule runs only once every field is individually valid:
	// reporting "protocol must be tcp before a port can be matched" about a port
	// that is itself malformed would name the wrong field.
	if r.Check != nil && len(errs) == 0 {
		errs = append(errs, r.Check(clean)...)
	}
	return Validated{Values: clean, Editing: editing}, errs
}

// BuildArgs turns a validated submission into `=key=value` words.
func (r *Resource) BuildArgs(v Validated) []string {
	var args []string
	for _, f := range r.Fields {
		val, has := v.Values[f.Name]
		if f.Type == TypeSecret && (!has || val == "") {
			continue // blank secret means "leave it alone"
		}
		if has {
			args = append(args, "="+f.ROS+"="+val)
			continue
		}
		// Only on an edit, and only when declared clearable: on a create an
		// omitted property should keep RouterOS's own default.
		if v.Editing && f.Clearable {
			args = append(args, "="+f.ROS+"=")
		}
	}
	return args
}

// PreviewCommand is the RouterOS command this submission WOULD issue, for the
// form's Preview button.
//
// ── EVERY SECRET IS MASKED, AND THAT IS THE POINT OF THE FUNCTION ───────────
//
// The preview is shown in the browser and can be copied out of it, so a
// passphrase or a pre-shared key rendered here is a credential leaked into a
// screenshot or a support ticket. Each secret field's VALUE is replaced with
// «set», leaving its `=key=` head visible so the operator can still see that the
// field is being written.
//
// The mask is on the field's ROS name rather than its value, so a non-secret
// field that happens to contain the same text is untouched — and a secret is
// masked even when its value is the empty string, which BuildArgs would have
// dropped anyway. Masking by value would be the version that leaks: two fields
// can share a value and only one of them is a secret.
//
// `/set` with the row's `.id` when editing, `/add` without one when creating —
// the same split BuildArgs already encodes through `Editing`, expressed here as
// the verb.
func (r *Resource) PreviewCommand(v Validated, id string) string {
	secret := map[string]bool{}
	for _, f := range r.Fields {
		if f.Type == TypeSecret {
			secret["="+f.ROS+"="] = true
		}
	}
	words := r.BuildArgs(v)
	for i, w := range words {
		eq := strings.Index(w[1:], "=")
		if eq < 0 {
			continue
		}
		head := w[:eq+2]
		if secret[head] {
			words[i] = head + "«set»"
		}
	}
	verb := "/add"
	var idWord []string
	if id != "" {
		verb = "/set"
		idWord = []string{"=.id=" + id}
	}
	return strings.Join(append([]string{r.Menu + verb}, append(idWord, words...)...), " ")
}

// IdentityOf is the identity value carried by a freshly-read row. It is not a
// primary key and does not need to be: the row is ADDRESSED by its `.id`, and
// the identity only has to answer "is this still the row I was looking at when
// I clicked". Mutation is what it catches.
func (r *Resource) IdentityOf(row map[string]string) string {
	if row == nil {
		return ""
	}
	parts := make([]string, 0, len(r.Identity))
	for _, name := range r.Identity {
		v := ""
		for _, f := range r.Fields {
			if f.Name == name {
				v = row[f.ROS]
				break
			}
		}
		parts = append(parts, v)
	}
	// U+0001 as the separator, matching identityOf() in resources.js. A
	// character no RouterOS value contains, so two rows cannot collide by
	// having their fields split differently across the join.
	return strings.Join(parts, IdentityOfSeparator)
}

// IdentityOfSeparator joins the parts of a composite identity.
//
// U+0001, matching identityOf() in resources.js and fwIdentity() in the browser:
// a character no RouterOS value contains, so two rows cannot collide by having
// their fields split differently across the join.
// TestFirewallIdentityMatchesTheBrowsers pins all three together.
const IdentityOfSeparator = "\u0001"

// IdentityJSON is what Describe sends: a bare string for a single field and an
// array for a composite, which is the shape resources.js emits because its
// declarations are `'name'` or `['chain', ...]`.
func (r *Resource) IdentityJSON() any {
	if len(r.Identity) == 1 {
		return r.Identity[0]
	}
	return r.Identity
}

// Describe is the schema the browser renders the form from.
//
// The shape matches describe() in src/routeros/resources.js field for field,
// including the nulls: the TypeScript renderer is a port of the existing one and
// distinguishes `null` from absent when deciding whether to emit a min/max
// attribute. Serving it is what stops the field list being restated in
// TypeScript and drifting — the registry-over-the-wire pattern the evidence pass
// named as the fix for the remaining client/server mirrors.
func (r *Resource) Describe() map[string]any {
	fields := make([]map[string]any, 0, len(r.Fields))
	for _, f := range r.Fields {
		var opts any
		if f.Options != nil {
			opts = f.Options
		}
		var showIf any
		if f.ShowIf != nil {
			showIf = map[string]any{"field": f.ShowIf.Field, "in": f.ShowIf.In}
		}
		var minv, maxv any
		if f.Min != nil {
			minv = *f.Min
		}
		if f.Max != nil {
			maxv = *f.Max
		}
		fields = append(fields, map[string]any{
			"name": f.Name, "label": f.Label, "type": string(f.Type), "input": f.input(),
			"required": f.Required, "options": opts, "placeholder": f.Placeholder,
			"help": f.Help, "showIf": showIf, "min": minv, "max": maxv,
		})
	}
	actions := make([]map[string]any, 0, len(r.Actions))
	for _, a := range r.Actions {
		actions = append(actions, map[string]any{"key": a.Key, "label": a.Label})
	}
	return map[string]any{
		"key": r.Key, "label": r.Label, "title": r.Title, "page": r.Page,
		"identity": r.IdentityJSON(), "actions": actions, "fields": fields,
	}
}

// ── The registry ─────────────────────────────────────────────────────────────

// DNSStatic mirrors the dnsStatic entry in src/routeros/resources.js.
var DNSStatic = &Resource{
	Key: "dnsStatic", Page: "dns", Label: "DNS Entry",
	Title: "Static DNS Entry", Menu: "/ip/dns/static", Identity: []string{"name"},
	// A regexp entry has no `name` to identify it by and matches a pattern
	// rather than a host. Editing one is a different form; until it exists,
	// saying so beats offering a form that would rename it to its own regexp.
	ReadOnlyWhen:   func(row map[string]string) bool { return row["regexp"] != "" },
	ReadOnlyReason: "read-only-row",
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "server.lan"},
		// All nine types RouterOS defines. Listing six was not a smaller feature,
		// it was data loss: the form showed "A" for an MX record and Save rewrote
		// it as one. The renderer no longer coerces an unlisted value, so a tenth
		// type would display honestly — but it would still fail the select check
		// on save, so this list has to stay in step with the router.
		{Name: "type", ROS: "type", Label: "Type", Type: TypeSelect, Required: true,
			Options: []string{"A", "AAAA", "CNAME", "FWD", "MX", "NS", "NXDOMAIN", "SRV", "TXT"}},
		{Name: "address", ROS: "address", Label: "Address", Type: TypeIP, Required: true,
			ShowIf: &ShowIf{Field: "type", In: []string{"A", "AAAA"}}},
		{Name: "cname", ROS: "cname", Label: "Canonical Name", Type: TypeText, Required: true,
			ShowIf: &ShowIf{Field: "type", In: []string{"CNAME"}}},
		{Name: "forwardTo", ROS: "forward-to", Label: "Forward To", Type: TypeText, Required: true,
			ShowIf: &ShowIf{Field: "type", In: []string{"FWD"}}},
		{Name: "text", ROS: "text", Label: "Text", Type: TypeText, Required: true,
			ShowIf: &ShowIf{Field: "type", In: []string{"TXT"}}},
		{Name: "mxExchange", ROS: "mx-exchange", Label: "Mail Exchanger", Type: TypeText, Required: true,
			ShowIf: &ShowIf{Field: "type", In: []string{"MX"}}, Placeholder: "mx1.lan"},
		{Name: "mxPreference", ROS: "mx-preference", Label: "Preference", Type: TypeInt,
			ShowIf: &ShowIf{Field: "type", In: []string{"MX"}}, Min: intp(0), Max: intp(65535), Placeholder: "10"},
		{Name: "ns", ROS: "ns", Label: "Name Server", Type: TypeText, Required: true,
			ShowIf: &ShowIf{Field: "type", In: []string{"NS"}}, Placeholder: "ns1.lan"},
		// NO TRAILING DOT. The MikroTik manual says srv-target "ends in a dot"
		// and the router rejects it: `=srv-target=host.lan.` answers
		// `bad SRV data`, `=srv-target=host.lan` is accepted. Verified against a
		// live hAP ac2. This port carried the dotted placeholder, reported it,
		// and the live app fixed it — this is the re-sync, help text included.
		{Name: "srvTarget", ROS: "srv-target", Label: "Target", Type: TypeText, Required: true,
			ShowIf: &ShowIf{Field: "type", In: []string{"SRV"}}, Placeholder: "host.lan",
			Help: "No trailing dot. The Name above must be _service._proto.name, " +
				"for example _sip._tcp.office.lan"},
		{Name: "srvPort", ROS: "srv-port", Label: "Port", Type: TypeInt,
			ShowIf: &ShowIf{Field: "type", In: []string{"SRV"}}, Min: intp(0), Max: intp(65535), Placeholder: "0"},
		{Name: "srvPriority", ROS: "srv-priority", Label: "Priority", Type: TypeInt,
			ShowIf: &ShowIf{Field: "type", In: []string{"SRV"}}, Min: intp(0), Max: intp(65535), Placeholder: "0"},
		{Name: "srvWeight", ROS: "srv-weight", Label: "Weight", Type: TypeInt,
			ShowIf: &ShowIf{Field: "type", In: []string{"SRV"}}, Min: intp(0), Max: intp(65535), Placeholder: "0"},
		{Name: "ttl", ROS: "ttl", Label: "TTL", Type: TypeText, Placeholder: "1d"},
		{Name: "matchSubdomain", ROS: "match-subdomain", Label: "Match Subdomains", Type: TypeBool, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

// Bridge mirrors the `bridge` entry in src/routeros/resources.js.
var Bridge = &Resource{
	Key: "bridge", Page: "bridges", Label: "Bridge",
	Title: "Bridge", Menu: "/interface/bridge", Identity: []string{"name"},
	Guard:                []string{"selfPath"},
	GuardInterfaceFields: []string{"name"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "bridge1"},
		{Name: "protocolMode", ROS: "protocol-mode", Label: "Protocol Mode", Type: TypeSelect,
			Options: []string{"none", "rstp", "stp", "mstp"}},
		{Name: "vlanFiltering", ROS: "vlan-filtering", Label: "VLAN Filtering", Type: TypeBool, Clearable: true},
		{Name: "igmpSnooping", ROS: "igmp-snooping", Label: "IGMP Snooping", Type: TypeBool, Clearable: true},
		{Name: "dhcpSnooping", ROS: "dhcp-snooping", Label: "DHCP Snooping", Type: TypeBool, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

// BridgePort mirrors the `bridgePort` entry. Its guard covers BOTH fields: the
// live registry notes this is "the one in this wave most likely to cut L2 to
// the dashboard: pulling the port our own traffic arrives on".
var BridgePort = &Resource{
	Key: "bridgePort", Page: "bridges", Label: "Bridge Port",
	Title: "Bridge Port", Menu: "/interface/bridge/port", Identity: []string{"interface"},
	Guard:                []string{"selfPath"},
	GuardInterfaceFields: []string{"interface", "bridge"},
	Fields: []Field{
		{Name: "bridge", ROS: "bridge", Label: "Bridge", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/interface/bridge", Value: "name"}},
		{Name: "interface", ROS: "interface", Label: "Interface", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/interface", Value: "name"}},
		{Name: "pvid", ROS: "pvid", Label: "PVID", Type: TypeInt, Min: intp(1), Max: intp(4094)},
		{Name: "frameTypes", ROS: "frame-types", Label: "Frame Types", Type: TypeSelect,
			Options: []string{"admit-all", "admit-only-untagged-and-priority-tagged", "admit-only-vlan-tagged"}},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

// Vlan mirrors the `vlan` entry in src/routeros/resources.js.
var Vlan = &Resource{
	Key: "vlan", Page: "vlans", Label: "VLAN",
	Title: "VLAN Interface", Menu: "/interface/vlan", Identity: []string{"name"},
	Guard: []string{"selfPath"},
	// The VLAN itself, and deliberately NOT its parent. Our address sitting on
	// `bridge` would otherwise make every VLAN riding that bridge warn — and a
	// warning that fires on the innocent case is one people learn to click
	// through.
	GuardInterfaceFields: []string{"name"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "vlan10"},
		{Name: "vlanId", ROS: "vlan-id", Label: "VLAN ID", Type: TypeInt, Required: true,
			Min: intp(1), Max: intp(4094)},
		{Name: "interface", ROS: "interface", Label: "Interface", Type: TypeText, Required: true,
			Placeholder: "bridge", OptionsFrom: &OptionsFrom{Menu: "/interface", Value: "name"}},
		{Name: "mtu", ROS: "mtu", Label: "MTU", Type: TypeInt, Min: intp(68), Max: intp(65535)},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

func intp(n int) *int { return &n }

// WgPeer mirrors the `wgPeer` entry in src/routeros/resources.js.
//
// IDENTIFIED BY ITS PUBLIC KEY, not by a name. A WireGuard peer has no unique
// name — several may share one, and the `name` field is free text an operator
// typed — so the public key is the only thing that says "this is still the row
// you were looking at" across a re-read.
//
// `presharedKey` IS DECLARED AND IS NEVER READ BACK. RowValues drops every
// secret-typed field, so the edit form opens with it blank and an unchanged save
// leaves the router's key alone; the audit trail masks it twice over, by type
// here and by name pattern in audit.js. That is why the help text says "leave
// blank to keep the current key" — it is describing a real mechanism, not
// offering advice.
//
// NO GUARD, matching the live declaration. A WireGuard peer is not a path to the
// router: editing one cannot move the interface the management session arrives
// on, which is the question `selfPath` exists to answer.
var WgPeer = &Resource{
	Key: "wgPeer", Page: "vpn", Label: "WireGuard Peer",
	Title: "WireGuard Peer", Menu: "/interface/wireguard/peers", Identity: []string{"publicKey"},
	Fields: []Field{
		{Name: "interface", ROS: "interface", Label: "Interface", Type: TypeText,
			Required: true, Placeholder: "wireguard1",
			OptionsFrom: &OptionsFrom{Menu: "/interface/wireguard", Value: "name"}},
		{Name: "publicKey", ROS: "public-key", Label: "Public Key", Type: TypeWgKey, Required: true},
		{Name: "allowedAddress", ROS: "allowed-address", Label: "Allowed Addresses",
			Type: TypeText, Required: true, Placeholder: "10.0.0.2/32"},
		{Name: "endpointAddress", ROS: "endpoint-address", Label: "Endpoint", Type: TypeText},
		{Name: "endpointPort", ROS: "endpoint-port", Label: "Endpoint Port", Type: TypeInt,
			Min: intp(1), Max: intp(65535)},
		{Name: "persistentKeepalive", ROS: "persistent-keepalive", Label: "Keepalive",
			Type: TypeText, Placeholder: "25s"},
		{Name: "presharedKey", ROS: "preshared-key", Label: "Pre-shared Key", Type: TypeSecret,
			Help: "leave blank to keep the current key"},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

// DHCPLease mirrors the `dhcpLease` entry in src/routeros/resources.js.
//
// A DYNAMIC LEASE IS THE SERVER'S, NOT OURS, so it is not editable — but it IS
// the input to make-static, which is how it becomes editable. That is why the
// read-only rule and the action's `when` are the same test read two ways.
//
// NO GUARD, matching the live declaration. A lease is a reservation, not a path:
// changing one cannot move the interface the management session arrives on.
var DHCPLease = &Resource{
	Key: "dhcpLease", Page: "dhcp", Label: "Lease",
	Title: "DHCP Lease", Menu: "/ip/dhcp-server/lease", Identity: []string{"macAddress"},
	ReadOnlyWhen:   func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason: "read-only-row",
	Actions: []Action{
		{Key: "makeStatic", Verb: "make-static", Label: "Make Static",
			When: func(r map[string]string) bool { return r["dynamic"] == "true" },
			Note: "converted a dynamic lease to a static reservation"},
	},
	Fields: []Field{
		{Name: "address", ROS: "address", Label: "Address", Type: TypeIP, Required: true},
		{Name: "macAddress", ROS: "mac-address", Label: "MAC Address", Type: TypeMac, Required: true},
		{Name: "server", ROS: "server", Label: "Server", Type: TypeText, Placeholder: "all",
			OptionsFrom: &OptionsFrom{Menu: "/ip/dhcp-server", Value: "name"}},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

// Route mirrors the `route` entry in src/routeros/resources.js.
//
// A route MikroDash did not create, it cannot edit: connected routes belong to
// an address and dynamic ones to a protocol or a DHCP client, and RouterOS
// rejects the write anyway. Refusing here says why instead of letting the
// router answer with a trap.
//
// NO GUARD, and that is a considered position rather than an omission. A route
// change can certainly cut the management path — but `selfPath` answers "which
// INTERFACE carries us", and a route is not an interface. The guard that would
// fit does not exist on either side; the live app declares none here either.
var Route = &Resource{
	Key: "route", Page: "routing", Label: "Route",
	Title: "IPv4 Route", Menu: "/ip/route", Identity: []string{"dstAddress"},
	ReadOnlyWhen: func(r map[string]string) bool {
		return r["dynamic"] == "true" || r["connect"] == "true"
	},
	ReadOnlyReason: "read-only-row",
	Fields: []Field{
		{Name: "dstAddress", ROS: "dst-address", Label: "Destination", Type: TypeCidr,
			Required: true, Placeholder: "0.0.0.0/0"},
		// Not TypeIP: a gateway is legitimately an interface name, or
		// `10.0.0.1%ether1` to pin a next hop to a link.
		{Name: "gateway", ROS: "gateway", Label: "Gateway", Type: TypeText,
			Required: true, Placeholder: "192.168.88.1 or ether1"},
		{Name: "distance", ROS: "distance", Label: "Distance", Type: TypeInt,
			Min: intp(1), Max: intp(255), Placeholder: "1"},
		{Name: "routingTable", ROS: "routing-table", Label: "Routing Table", Type: TypeText,
			Placeholder: "main", OptionsFrom: &OptionsFrom{Menu: "/routing/table", Value: "name"}},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

// Route6 mirrors the `route6` entry. Same shape, different menu — and no
// routing-table picker, which the live declaration also omits.
var Route6 = &Resource{
	Key: "route6", Page: "routing", Label: "IPv6 Route",
	Title: "IPv6 Route", Menu: "/ipv6/route", Identity: []string{"dstAddress"},
	ReadOnlyWhen: func(r map[string]string) bool {
		return r["dynamic"] == "true" || r["connect"] == "true"
	},
	ReadOnlyReason: "read-only-row",
	Fields: []Field{
		{Name: "dstAddress", ROS: "dst-address", Label: "Destination", Type: TypeCidr,
			Required: true, Placeholder: "::/0"},
		{Name: "gateway", ROS: "gateway", Label: "Gateway", Type: TypeText,
			Required: true, Placeholder: "fe80::1%ether1"},
		{Name: "distance", ROS: "distance", Label: "Distance", Type: TypeInt,
			Min: intp(1), Max: intp(255), Placeholder: "1"},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

// macRe is the Node validator's pattern, applied to the UPPER-CASED value.
var macRe = regexp.MustCompile(`^([0-9A-F]{2}:){5}[0-9A-F]{2}$`)

// wgKeyRe is the Node validator's pattern verbatim — 43 base64 characters and a
// trailing '='. Written the same odd way it is written there (42 then 1) so the
// two read as the same rule rather than as two rules that happen to agree.
var wgKeyRe = regexp.MustCompile(`^[A-Za-z0-9+/]{42}[A-Za-z0-9+/]=$`)

// ActionsFor is the keys of the actions THIS row offers, judged on the row as
// the router has it. Empty rather than nil so it serialises as `[]`.
func (r *Resource) ActionsFor(row map[string]string) []string {
	out := make([]string, 0, len(r.Actions))
	for _, a := range r.Actions {
		if a.When == nil || a.When(row) {
			out = append(out, a.Key)
		}
	}
	return out
}

// ActionByKey finds one, or nil.
func (r *Resource) ActionByKey(key string) *Action {
	for i := range r.Actions {
		if r.Actions[i].Key == key {
			return &r.Actions[i]
		}
	}
	return nil
}

// Action is a named verb a row offers besides create, update and delete.
//
// A DYNAMIC DHCP LEASE IS WHY THIS EXISTS. It cannot be edited — it belongs to
// the server rather than to us — and the only useful thing to do with it is make
// it static. Refusing to open the form at all would make that verb unreachable,
// so a read-only row still gets its actions.
type Action struct {
	Key   string
	Verb  string // the RouterOS command under the resource's menu
	Label string
	// Note is the sentence the audit row carries.
	Note string
	// When decides whether this row offers the action at all, judged on a
	// freshly-read row like every other decision here.
	When func(row map[string]string) bool
}

var byKey = map[string]*Resource{
	DHCPLease.Key:           DHCPLease,
	WgPeer.Key:              WgPeer,
	Route.Key:               Route,
	Route6.Key:              Route6,
	DNSStatic.Key:           DNSStatic,
	Bridge.Key:              Bridge,
	BridgePort.Key:          BridgePort,
	Vlan.Key:                Vlan,
	WifiNet.Key:             WifiNet,
	WlNet.Key:               WlNet,
	WlSecProfile.Key:        WlSecProfile,
	CapsProvisioningRes.Key: CapsProvisioningRes,
	CapsConfig.Key:          CapsConfig,
	CapsSecurity.Key:        CapsSecurity,
	CapsChannel.Key:         CapsChannel,
	CapsDatapath.Key:        CapsDatapath,
	FWFilter.Key:            FWFilter,
	FWNat.Key:               FWNat,
	FWMangle.Key:            FWMangle,
	FWRaw.Key:               FWRaw,
}

// StaticOptions are the picker lists that need no router read.
func (r *Resource) StaticOptions() map[string][]string {
	out := map[string][]string{}
	for _, f := range r.Fields {
		if f.OptionsFrom != nil && len(f.OptionsFrom.Values) > 0 {
			out[f.Name] = append([]string{}, f.OptionsFrom.Values...)
		}
	}
	return out
}

// OptionSource is one field's menu-backed picker.
type OptionSource struct {
	Field string
	Menu  string
	Value string
}

// OptionSources are the pickers that need a router read. The caller reads each
// distinct menu ONCE and shares it between the fields that name it — /interface
// backs both the VLAN parent and the bridge port, and reading it twice would be
// silly.
func (r *Resource) OptionSources() []OptionSource {
	var out []OptionSource
	for _, f := range r.Fields {
		if f.OptionsFrom != nil && f.OptionsFrom.Menu != "" {
			out = append(out, OptionSource{Field: f.Name, Menu: f.OptionsFrom.Menu, Value: f.OptionsFrom.Value})
		}
	}
	return out
}

// ByKey resolves a resource the browser named. An unknown key returns nil, and
// the caller must treat that as a refusal rather than as a default.
func ByKey(k string) *Resource { return byKey[k] }

// All is every registered resource, ordered by Key.
//
// ── IT EXISTS SO NOTHING HAS TO TYPE THE LIST OUT AGAIN ────────────────────
//
// `internal/server`'s guard test enumerated SIXTEEN resources by name against a
// registry of twenty, and the four it missed — DHCPLease, Route, Route6, WgPeer
// — were unchecked for as long as that list had been typed. Harmless only by
// coincidence: those four declare no guard today. The moment one gained an
// unported guard, its writes would be refused at runtime (which is the correct
// failure) and no test would have said so.
//
// That is the same shape as the `endpoint-audit` incident CLAUDE.md records: a
// sweep that ran "a list of audit names typed from memory" and was red for an
// unknown number of sessions. A registry that can be ENUMERATED is what stops a
// checker and the thing it checks from drifting apart.
//
// Ordered, so callers that print it produce a stable diff rather than Go's
// randomised map order.
func All() []*Resource {
	out := make([]*Resource, 0, len(byKey))
	for _, r := range byKey {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// RowValues maps a freshly-read RouterOS row back onto the form's field names.
//
// A secret is never included: it is never rendered with a value and never
// echoed back, because an empty box is what means "leave it unchanged" and a
// pre-filled one would invite an operator to clear a key by deleting what they
// see.
func (r *Resource) RowValues(row map[string]string) map[string]any {
	out := map[string]any{}
	if row == nil {
		return out
	}
	for _, f := range r.Fields {
		if f.Type == TypeSecret {
			continue
		}
		raw, ok := row[f.ROS]
		if !ok {
			continue
		}
		if f.Type == TypeBool {
			out[f.Name] = raw == "true" || raw == "yes"
		} else {
			out[f.Name] = raw
		}
	}
	return out
}

// ── Firewall ────────────────────────────────────────────────────────────────
//
// The one place in this registry where POSITION is part of the meaning. A rule
// below the final drop does nothing; the same rule above an accept blocks
// everything. `Ordered` says so, and is what puts the move controls on the page
// and lets res:move address these menus.
//
// `fwGuard` is the lockout guard — a filter rule is the one thing here that can
// cut MikroDash off from the router it manages.
//
// Each group below returns FRESH field values. Two tables sharing one slice
// would make a later per-table tweak leak sideways.

func fwHead(chains, actions []string) []Field {
	return []Field{
		{Name: "chain", ROS: "chain", Label: "Chain", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Values: chains}},
		{Name: "action", ROS: "action", Label: "Action", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Values: actions}},
	}
}

func fwMatch() []Field {
	return []Field{
		{Name: "srcAddress", ROS: "src-address", Label: "Source Address", Type: TypeText,
			Placeholder: "10.0.0.0/24"},
		{Name: "dstAddress", ROS: "dst-address", Label: "Destination Address", Type: TypeText},
		{Name: "protocol", ROS: "protocol", Label: "Protocol", Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: []string{
				"tcp", "udp", "icmp", "ipv6-icmp", "gre", "ipsec-esp", "ipsec-ah"}}},
		{Name: "srcPort", ROS: "src-port", Label: "Source Port", Type: TypeText},
		// A port match is a list or a range as often as it is a number, so this
		// is text: `443`, `80,443` and `1000-2000` are all valid to RouterOS.
		{Name: "dstPort", ROS: "dst-port", Label: "Destination Port", Type: TypeText,
			Placeholder: "443, or 1000-2000"},
		{Name: "inInterface", ROS: "in-interface", Label: "In Interface", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/interface", Value: "name"}},
		{Name: "outInterface", ROS: "out-interface", Label: "Out Interface", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/interface", Value: "name"}},
	}
}

func fwTail() []Field {
	return []Field{
		{Name: "log", ROS: "log", Label: "Log", Type: TypeBool, Clearable: true},
		{Name: "logPrefix", ROS: "log-prefix", Label: "Log Prefix", Type: TypeText},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	}
}

func fwFields(groups ...[]Field) []Field {
	out := []Field{}
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// fwIdentity is a COMPOSITE because a firewall rule has no name and nothing
// unique about it. See IdentityOf for why that is enough: the row is addressed
// by its `.id`, and the identity only has to answer "is this still the row I was
// looking at when I clicked".
var fwIdentity = []string{"chain", "action", "srcAddress", "dstAddress", "comment"}

// fwActions are enable and disable as ROW actions rather than as the `disabled`
// checkbox.
//
// The checkbox is still in the form, but flipping a rule is the single most
// common thing anyone does to a firewall and it should not require opening one.
// RouterOS has verbs for it, so resAction already knows how to run them.
var fwActions = []Action{
	{Key: "enable", Verb: "enable", Label: "Enable",
		When: func(r map[string]string) bool { return r["disabled"] == "true" },
		Note: "enabled a firewall rule"},
	{Key: "disable", Verb: "disable", Label: "Disable",
		When: func(r map[string]string) bool { return r["disabled"] != "true" },
		Note: "disabled a firewall rule"},
}

// fwReadOnly: a rule some service added is not ours to edit.
func fwReadOnly(row map[string]string) bool { return row["dynamic"] == "true" }

// fwPortProtos is RouterOS's own list: "ports can be specified if proto is
// tcp,udp,udp-lite,dccp,sctp".
//
// A real constraint, and one somebody meets the first time they try to allow a
// port — the obvious thing to fill in is the port, and the protocol is easy to
// miss. Left to the router it comes back as a bare refusal with no clue which
// field to fix, so it is checked here and reported against the field that is
// actually missing.
var fwPortProtos = []string{"tcp", "udp", "udp-lite", "dccp", "sctp"}

func fwCheck(clean map[string]string) []Error {
	if clean["srcPort"] == "" && clean["dstPort"] == "" {
		return nil
	}
	proto := strings.ToLower(clean["protocol"])
	for _, p := range fwPortProtos {
		if proto == p {
			return nil
		}
	}
	return []Error{{Field: "protocol",
		Message: "Protocol must be one of " + strings.Join(fwPortProtos, ", ") +
			" before a port can be matched"}}
}

var FWFilter = &Resource{
	Key: "fwFilter", Page: "firewall", Label: "Filter Rule",
	Title: "Firewall Filter Rule", Menu: "/ip/firewall/filter",
	Identity: fwIdentity, Ordered: true, Guard: []string{"fwGuard"},
	ReadOnlyWhen: fwReadOnly, Actions: fwActions, Check: fwCheck,
	Fields: fwFields(
		fwHead([]string{"input", "forward", "output"},
			[]string{"accept", "drop", "reject", "tarpit", "log", "passthrough",
				"fasttrack-connection", "jump", "return",
				"add-src-to-address-list", "add-dst-to-address-list"}),
		fwMatch(),
		[]Field{
			// A comma list, not one value — `established,related` is the single
			// most common thing written here.
			{Name: "connectionState", ROS: "connection-state", Label: "Connection State",
				Type: TypeText, Placeholder: "established,related"},
			{Name: "rejectWith", ROS: "reject-with", Label: "Reject With", Type: TypeText,
				ShowIf: &ShowIf{Field: "action", In: []string{"reject"}},
				OptionsFrom: &OptionsFrom{Values: []string{
					"icmp-network-unreachable", "icmp-host-unreachable",
					"icmp-port-unreachable", "icmp-admin-prohibited", "tcp-reset"}}},
		},
		fwTail(),
	),
}

var FWNat = &Resource{
	Key: "fwNat", Page: "firewall", Label: "NAT Rule",
	Title: "Firewall NAT Rule", Menu: "/ip/firewall/nat",
	Identity: fwIdentity, Ordered: true, Guard: []string{"fwGuard"},
	ReadOnlyWhen: fwReadOnly, Actions: fwActions, Check: fwCheck,
	Fields: fwFields(
		fwHead([]string{"srcnat", "dstnat"},
			[]string{"accept", "masquerade", "dst-nat", "src-nat", "redirect", "netmap", "same",
				"log", "jump", "return", "add-src-to-address-list", "add-dst-to-address-list"}),
		fwMatch(),
		[]Field{
			{Name: "toAddresses", ROS: "to-addresses", Label: "To Addresses", Type: TypeText,
				ShowIf: &ShowIf{Field: "action", In: []string{"dst-nat", "src-nat", "netmap", "same"}}},
			{Name: "toPorts", ROS: "to-ports", Label: "To Ports", Type: TypeText,
				ShowIf: &ShowIf{Field: "action", In: []string{"dst-nat", "redirect", "netmap"}}},
		},
		fwTail(),
	),
}

var FWMangle = &Resource{
	Key: "fwMangle", Page: "firewall", Label: "Mangle Rule",
	Title: "Firewall Mangle Rule", Menu: "/ip/firewall/mangle",
	Identity: fwIdentity, Ordered: true, Guard: []string{"fwGuard"},
	ReadOnlyWhen: fwReadOnly, Actions: fwActions, Check: fwCheck,
	Fields: fwFields(
		fwHead([]string{"prerouting", "input", "forward", "output", "postrouting"},
			[]string{"accept", "mark-connection", "mark-packet", "mark-routing",
				"change-mss", "change-ttl", "change-dscp", "route", "log",
				"passthrough", "jump", "return"}),
		fwMatch(),
		[]Field{
			{Name: "newConnectionMark", ROS: "new-connection-mark", Label: "New Connection Mark",
				Type: TypeText, Required: true,
				ShowIf: &ShowIf{Field: "action", In: []string{"mark-connection"}}},
			{Name: "newPacketMark", ROS: "new-packet-mark", Label: "New Packet Mark",
				Type: TypeText, Required: true,
				ShowIf: &ShowIf{Field: "action", In: []string{"mark-packet"}}},
			{Name: "newRoutingMark", ROS: "new-routing-mark", Label: "New Routing Mark",
				Type: TypeText, Required: true,
				ShowIf: &ShowIf{Field: "action", In: []string{"mark-routing"}}},
			// Marking rules default to passthrough=yes, and turning it off is
			// how a mangle chain stops after the first match.
			{Name: "passthrough", ROS: "passthrough", Label: "Passthrough", Type: TypeBool,
				Clearable: true},
		},
		fwTail(),
	),
}

var FWRaw = &Resource{
	Key: "fwRaw", Page: "firewall", Label: "Raw Rule",
	Title: "Firewall Raw Rule", Menu: "/ip/firewall/raw",
	Identity: fwIdentity, Ordered: true, Guard: []string{"fwGuard"},
	ReadOnlyWhen: fwReadOnly, Actions: fwActions, Check: fwCheck,
	Fields: fwFields(
		// NO connection-state anywhere in raw: it runs before connection
		// tracking, so there is no state to match on yet.
		fwHead([]string{"prerouting", "output"},
			[]string{"accept", "drop", "notrack", "log", "jump", "return",
				"add-src-to-address-list", "add-dst-to-address-list"}),
		fwMatch(),
		fwTail(),
	),
}

// ── Wireless ────────────────────────────────────────────────────────────────
//
// TWO STACKS, TWO RESOURCES, ONE TABLE. A router has EITHER /interface/wifi
// (modern) or /interface/wireless (legacy), never both, so `RequiresMenu`
// decides which Add button is real and the collector tags each row with the
// resource that owns it — a per-row `data-res`, the way the Routes table already
// mixes v4 and v6.
//
// Band, width and authentication-type vocabularies differ across drivers, so
// every one of them is TEXT WITH SUGGESTIONS rather than a select. A hard select
// would refuse a value the router itself is perfectly happy with.

var wifiActions = []Action{
	{Key: "enable", Verb: "enable", Label: "Enable",
		When: func(r map[string]string) bool { return r["disabled"] == "true" },
		Note: "enabled a wireless network"},
	{Key: "disable", Verb: "disable", Label: "Disable",
		When: func(r map[string]string) bool { return r["disabled"] != "true" },
		Note: "disabled a wireless network"},
}

// pskLength: a WPA passphrase is 8..63 characters.
//
// Checked here rather than left to RouterOS because the router answers a short
// key with a bare refusal naming no field, and "which box do I fix" is the whole
// question at that moment.
func pskLength(field string) func(map[string]string) []Error {
	return func(clean map[string]string) []Error {
		pass := clean[field]
		if pass == "" || (len(pass) >= 8 && len(pass) <= 63) {
			return nil
		}
		return []Error{{Field: field, Message: "Passphrase must be 8 to 63 characters"}}
	}
}

// wifiRemovable: only a virtual AP may be removed.
//
// A master radio is hardware: it can be edited and disabled, but deleting it is
// not a thing RouterOS will do. ReadOnlyWhen cannot say this — it would block
// the edit as well — so removal has a predicate of its own.
func wifiRemovable(r map[string]string) bool { return r["master-interface"] != "" }

var WifiNet = &Resource{
	Key: "wifiNet", Page: "wifi", Label: "Wifi Network",
	Title: "Wifi Network", Menu: "/interface/wifi", Identity: []string{"name"},
	RequiresMenu: "/interface/wifi",
	// TWO GUARDS, TWO DIFFERENT QUESTIONS. selfPath asks whether this cuts the
	// path we reach the router by; wifiInherit asks whether it quietly overrides
	// a profile more than one radio shares. Both can be true of one write, and
	// the first warn wins.
	Guard:                []string{"selfPath", "wifiInherit"},
	GuardInterfaceFields: []string{"name"},
	// Renaming and disabling are not the only disruptive edits here: changing
	// the SSID or the passphrase drops every client on the radio, the management
	// path included.
	GuardDisruptiveFields: []string{"ssid", "passphrase", "authTypes", "band"},
	// A CAP takes its configuration from the manager, so a local edit is a no-op
	// that would look like a working save. A dynamic interface is not ours at all.
	ReadOnlyWhen: func(r map[string]string) bool {
		return r["configuration.manager"] != "" || r["dynamic"] == "true"
	},
	RemovableWhen: wifiRemovable,
	Actions:       wifiActions,
	Check:         pskLength("passphrase"),
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Interface Name", Type: TypeText,
			Required: true, Placeholder: "wifi1-guest"},
		// Required, and that is what scopes Add to "another SSID on an existing
		// radio": with no way to omit it, the form cannot create a stray radio.
		{Name: "masterInterface", ROS: "master-interface", Label: "Radio", Type: TypeText,
			Required: true, OptionsFrom: &OptionsFrom{Menu: "/interface/wifi", Value: "name"},
			Help: "the radio this SSID rides on"},
		{Name: "ssid", ROS: "configuration.ssid", Label: "SSID", Type: TypeText,
			Required: true, Max: intp(32)},
		{Name: "authTypes", ROS: "security.authentication-types", Label: "Security", Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: []string{"", "wpa2-psk", "wpa3-psk",
				"wpa2-psk,wpa3-psk", "wpa2-eap", "wpa3-eap", "owe"}},
			Help: "blank is an open network"},
		{Name: "passphrase", ROS: "security.passphrase", Label: "Passphrase", Type: TypeSecret,
			Max: intp(63), Help: "leave blank to keep the current passphrase"},
		{Name: "hideSsid", ROS: "configuration.hide-ssid", Label: "Hide SSID", Type: TypeBool,
			Clearable: true},
		{Name: "band", ROS: "channel.band", Label: "Band", Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: []string{"2ghz-ax", "2ghz-n", "5ghz-ax",
				"5ghz-ac", "6ghz-ax"}}},
		{Name: "frequency", ROS: "channel.frequency", Label: "Frequency", Type: TypeText,
			Placeholder: "auto, or 5180"},
		{Name: "width", ROS: "channel.width", Label: "Channel Width", Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: []string{"20mhz", "20/40mhz", "20/40/80mhz",
				"20/40/80/160mhz"}}},
		{Name: "country", ROS: "configuration.country", Label: "Country", Type: TypeText},
		// NOT clearable, unlike almost every other optional field in this
		// registry. `clearable` emits `=datapath.vlan-id=` on an edit, and
		// RouterOS answers a typed integer property given an empty string with
		// "invalid value for datapath.vlan-id, an integer required" — so leaving
		// it on made EVERY edit of a wireless network fail, whether or not it
		// touched the VLAN. Clearing one needs /interface/wifi/unset, which this
		// engine has no verb for; until it does, an unset VLAN is one WinBox keeps.
		{Name: "vlanId", ROS: "datapath.vlan-id", Label: "VLAN ID", Type: TypeInt,
			Min: intp(1), Max: intp(4094)},
		{Name: "bridge", ROS: "datapath.bridge", Label: "Bridge", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/interface/bridge", Value: "name"}},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

var WlNet = &Resource{
	Key: "wlNet", Page: "wifi", Label: "Wifi Network",
	Title: "Wifi Network (legacy)", Menu: "/interface/wireless", Identity: []string{"name"},
	RequiresMenu: "/interface/wireless",
	// NO wifiInherit here: the legacy stack has no configuration profiles to
	// inherit from. Security is a reference, not an inherited value, and
	// changing which profile an interface points at is an ordinary edit.
	Guard:                 []string{"selfPath"},
	GuardInterfaceFields:  []string{"name"},
	GuardDisruptiveFields: []string{"ssid", "securityProfile", "band"},
	// A CAPsMAN-provisioned legacy interface arrives dynamic, and editing it
	// locally is meaningless for the same reason a CAP's is.
	ReadOnlyWhen:  func(r map[string]string) bool { return r["dynamic"] == "true" },
	RemovableWhen: wifiRemovable,
	Actions:       wifiActions,
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Interface Name", Type: TypeText,
			Required: true, Placeholder: "wlan1-guest"},
		{Name: "masterInterface", ROS: "master-interface", Label: "Radio", Type: TypeText,
			Required: true, OptionsFrom: &OptionsFrom{Menu: "/interface/wireless", Value: "name"},
			Help: "the radio this SSID rides on"},
		{Name: "ssid", ROS: "ssid", Label: "SSID", Type: TypeText, Required: true, Max: intp(32)},
		// The passphrase is deliberately NOT here: on this stack it lives on the
		// profile, which is why wlSecProfile is a resource of its own.
		{Name: "securityProfile", ROS: "security-profile", Label: "Security Profile", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/interface/wireless/security-profiles", Value: "name"},
			Help:        "the passphrase lives on the profile, not here"},
		{Name: "mode", ROS: "mode", Label: "Mode", Type: TypeSelect,
			Options: []string{"ap-bridge", "bridge", "station", "station-bridge",
				"station-pseudobridge"}},
		{Name: "hideSsid", ROS: "hide-ssid", Label: "Hide SSID", Type: TypeBool, Clearable: true},
		{Name: "band", ROS: "band", Label: "Band", Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: []string{"2ghz-b/g/n", "2ghz-g/n", "2ghz-onlyn",
				"5ghz-a/n/ac", "5ghz-onlyac", "5ghz-a/n"}}},
		{Name: "frequency", ROS: "frequency", Label: "Frequency", Type: TypeText,
			Placeholder: "auto, or 5180"},
		{Name: "channelWidth", ROS: "channel-width", Label: "Channel Width", Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: []string{"20mhz", "20/40mhz-Ce", "20/40mhz-eC",
				"20/40/80mhz-Ceee"}}},
		// Not clearable, for the reason given on wifiNet.vlanId above.
		{Name: "vlanId", ROS: "vlan-id", Label: "VLAN ID", Type: TypeInt,
			Min: intp(1), Max: intp(4094)},
		{Name: "vlanMode", ROS: "vlan-mode", Label: "VLAN Mode", Type: TypeSelect,
			Options: []string{"no-tag", "use-service-tag", "use-tag"}},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

var WlSecProfile = &Resource{
	Key: "wlSecProfile", Page: "wifi", Label: "Security Profile",
	Title: "Wifi Security Profile", Menu: "/interface/wireless/security-profiles",
	Identity: []string{"name"}, RequiresMenu: "/interface/wireless/security-profiles",
	// BOTH KEYS ARE `secret`, so neither is read back into the form and neither
	// reaches the audit trail as a value: auditValues keys on the declared TYPE
	// rather than the field name, which is what covers `wpa2PreSharedKey`
	// despite it not matching the credential name pattern.
	Check: pskLength("wpa2PreSharedKey"),
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText,
			Required: true, Placeholder: "guest-wpa2"},
		{Name: "mode", ROS: "mode", Label: "Mode", Type: TypeSelect,
			Options: []string{"none", "static-keys-optional", "static-keys-required",
				"dynamic-keys"}},
		{Name: "authenticationTypes", ROS: "authentication-types", Label: "Authentication",
			Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: []string{"", "wpa-psk", "wpa2-psk",
				"wpa-psk,wpa2-psk", "wpa-eap", "wpa2-eap"}}},
		{Name: "wpa2PreSharedKey", ROS: "wpa2-pre-shared-key", Label: "WPA2 Passphrase",
			Type: TypeSecret, Max: intp(63),
			Help: "leave blank to keep the current passphrase"},
		{Name: "wpaPreSharedKey", ROS: "wpa-pre-shared-key", Label: "WPA Passphrase",
			Type: TypeSecret, Max: intp(63),
			Help: "leave blank to keep the current passphrase"},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
	},
}

// ── CAPsMAN ─────────────────────────────────────────────────────────────────
//
// The five menus that decide what a CAP gets provisioned with. Ordinary list
// menus with `.id` rows, so they need no new machinery — but they differ from
// everything else here in BLAST RADIUS: a write lands on every CAP in the fleet
// the moment it is saved, which is what `capsmanPush` warns about.
//
// PAGE SCOPE IS THE AUTHORISATION BOUNDARY, and the asymmetry is deliberate:
// these are `Page: "capsman"` while wifiNet is `Page: "wifi"`, so a role holding
// write on wifi but not capsman can override a value on ONE interface but cannot
// edit the shared profile every CAP follows. Smaller blast radius for the lesser
// grant. Do not "simplify" the two pages onto one key.

var capsActions = []Action{
	{Key: "enable", Verb: "enable", Label: "Enable",
		When: func(r map[string]string) bool { return r["disabled"] == "true" },
		Note: "enabled a CAPsMAN rule"},
	{Key: "disable", Verb: "disable", Label: "Disable",
		When: func(r map[string]string) bool { return r["disabled"] != "true" },
		Note: "disabled a CAPsMAN rule"},
}

var CapsProvisioningRes = &Resource{
	Key: "capsProvisioning", Page: "capsman", Label: "Provisioning Rule",
	Title: "CAPsMAN Provisioning Rule", Menu: "/interface/wifi/provisioning",
	RequiresMenu: "/interface/wifi/provisioning",
	// A provisioning rule has no name and nothing unique about it — the same
	// problem a firewall rule has, and the same answer. The collector mirrors
	// this tuple, in this order.
	Identity: []string{"supportedBands", "action", "masterConfiguration", "nameFormat"},
	// ORDER IS MEANING here as it is in the firewall: the first rule whose bands
	// match a joining radio wins, so a broad rule above a specific one hides it.
	Ordered: true,
	// NO capsmanPush guard here, unlike the four profile menus. Editing a rule
	// pushes nothing: MikroTik's docs are explicit that "provisioning itself is
	// not for sending configuration, it is for essentially creating a new
	// interface" — it acts when a CAP joins. A guard that always returned
	// "nothing to say" would be noise in the registry.
	Actions: capsActions,
	Fields: []Field{
		{Name: "supportedBands", ROS: "supported-bands", Label: "Supported Bands", Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: []string{"2ghz-ax", "2ghz-n", "2ghz-g",
				"5ghz-ax", "5ghz-ac", "5ghz-n", "6ghz-ax"}},
			Help: "a comma list — the rule matches a radio offering any of them"},
		{Name: "action", ROS: "action", Label: "Action", Type: TypeSelect, Required: true,
			Options: []string{"create-dynamic-enabled", "create-enabled", "create-disabled", "none"}},
		{Name: "masterConfiguration", ROS: "master-configuration", Label: "Master Configuration",
			Type:        TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/interface/wifi/configuration", Value: "name"}},
		{Name: "slaveConfigurations", ROS: "slave-configurations", Label: "Slave Configurations",
			Type:        TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/interface/wifi/configuration", Value: "name"},
			Help:        "a comma list — the extra SSIDs provisioned onto the same radio"},
		{Name: "nameFormat", ROS: "name-format", Label: "Name Format", Type: TypeText,
			Placeholder: "%I-%N"},
		{Name: "radioMac", ROS: "radio-mac", Label: "Radio MAC", Type: TypeMac,
			Help: "match one radio only; leave blank to match any"},
		{Name: "identityRegexp", ROS: "identity-regexp", Label: "Identity Regexp", Type: TypeText},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

var CapsConfig = &Resource{
	Key: "capsConfig", Page: "capsman", Label: "Configuration Profile",
	Title: "CAPsMAN Configuration Profile", Menu: "/interface/wifi/configuration",
	Identity: []string{"name"}, RequiresMenu: "/interface/wifi/configuration",
	Guard: []string{"capsmanPush"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText,
			Required: true, Placeholder: "Guest WiFi 5Ghz"},
		{Name: "ssid", ROS: "ssid", Label: "SSID", Type: TypeText, Max: intp(32)},
		{Name: "country", ROS: "country", Label: "Country", Type: TypeText},
		{Name: "mode", ROS: "mode", Label: "Mode", Type: TypeSelect,
			Options: []string{"ap", "station", "station-bridge"}},
		{Name: "hideSsid", ROS: "hide-ssid", Label: "Hide SSID", Type: TypeBool, Clearable: true},
		{Name: "security", ROS: "security", Label: "Security Profile", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/interface/wifi/security", Value: "name"}},
		{Name: "channel", ROS: "channel", Label: "Channel Profile", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/interface/wifi/channel", Value: "name"}},
		{Name: "datapath", ROS: "datapath", Label: "Datapath Profile", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/interface/wifi/datapath", Value: "name"}},
		// `manager` is DELIBERATELY not a field. MikroTik's own documentation
		// warns that configuration.manager belongs on the CAP device itself and
		// must never be pushed through a provisioned profile. Offering it here
		// is a footgun with no upside — the collector still reads it so the card
		// can SHOW it.
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

var CapsSecurity = &Resource{
	Key: "capsSecurity", Page: "capsman", Label: "Security Profile",
	Title: "CAPsMAN Security Profile", Menu: "/interface/wifi/security",
	Identity: []string{"name"}, RequiresMenu: "/interface/wifi/security",
	Guard: []string{"capsmanPush"},
	// LENGTH ONLY, never presence: a blank passphrase means "leave the current
	// one alone", so requiring one would make renaming a profile demand the
	// passphrase be retyped.
	Check: pskLength("passphrase"),
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText,
			Required: true, Placeholder: "Guest WiFi"},
		{Name: "authenticationTypes", ROS: "authentication-types", Label: "Authentication",
			Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: []string{"", "wpa2-psk", "wpa3-psk",
				"wpa2-psk,wpa3-psk", "wpa2-eap", "wpa3-eap", "owe"}},
			Help: "blank is an open network"},
		{Name: "passphrase", ROS: "passphrase", Label: "Passphrase", Type: TypeSecret,
			Max: intp(63), Help: "leave blank to keep the current passphrase"},
		{Name: "wps", ROS: "wps", Label: "WPS", Type: TypeSelect,
			Options: []string{"disable", "push-button"}},
		{Name: "ft", ROS: "ft", Label: "802.11r Fast Roaming", Type: TypeBool, Clearable: true},
		{Name: "ftOverDs", ROS: "ft-over-ds", Label: "FT over DS", Type: TypeBool, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

var CapsChannel = &Resource{
	Key: "capsChannel", Page: "capsman", Label: "Channel Profile",
	Title: "CAPsMAN Channel Profile", Menu: "/interface/wifi/channel",
	Identity: []string{"name"}, RequiresMenu: "/interface/wifi/channel",
	Guard: []string{"capsmanPush"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText,
			Required: true, Placeholder: "5Ghz Channels"},
		{Name: "band", ROS: "band", Label: "Band", Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: []string{"2ghz-ax", "2ghz-n", "5ghz-ax",
				"5ghz-ac", "6ghz-ax"}}},
		// A list and a range are both valid: `5180,5260,5500` and `5180-5730`.
		{Name: "frequency", ROS: "frequency", Label: "Frequency", Type: TypeText,
			Placeholder: "5180,5260 or 5180-5730"},
		{Name: "width", ROS: "width", Label: "Channel Width", Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: []string{"20mhz", "20/40mhz", "20/40/80mhz",
				"20/40/80/160mhz"}}},
		{Name: "secondaryFrequency", ROS: "secondary-frequency", Label: "Secondary Frequency",
			Type: TypeText},
		{Name: "skipDfsChannels", ROS: "skip-dfs-channels", Label: "Skip DFS Channels",
			Type: TypeSelect, Options: []string{"disabled", "10min-cac", "all"}},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

var CapsDatapath = &Resource{
	Key: "capsDatapath", Page: "capsman", Label: "Datapath Profile",
	Title: "CAPsMAN Datapath Profile", Menu: "/interface/wifi/datapath",
	Identity: []string{"name"}, RequiresMenu: "/interface/wifi/datapath",
	Guard: []string{"capsmanPush"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText,
			Required: true, Placeholder: "datapath"},
		{Name: "bridge", ROS: "bridge", Label: "Bridge", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/interface/bridge", Value: "name"}},
		// NOT clearable — see the note on wifiNet.vlanId. RouterOS refuses an
		// empty value for a typed integer, and `clearable` emits exactly that.
		{Name: "vlanId", ROS: "vlan-id", Label: "VLAN ID", Type: TypeInt,
			Min: intp(1), Max: intp(4094)},
		{Name: "clientIsolation", ROS: "client-isolation", Label: "Client Isolation",
			Type: TypeBool, Clearable: true},
		{Name: "localForwarding", ROS: "local-forwarding", Label: "Local Forwarding",
			Type: TypeBool, Clearable: true},
		{Name: "trafficProcessing", ROS: "traffic-processing", Label: "Traffic Processing",
			Type: TypeSelect, Options: []string{"on-capsman", "local-forwarding"}},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}
