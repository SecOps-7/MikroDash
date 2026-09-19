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

	"mikrodash/internal/guard"
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
	TypeCidr Type = "cidr"
	TypeInt  Type = "int"
	TypeBool Type = "bool"
	// TypeFlag is a PRESENCE flag: RouterOS reports it by the key being there
	// (`fib=""`) or not, and a `no` does not clear it — measured on
	// /routing/table's fib, where `set fib=no` changes nothing. It renders as a
	// checkbox; on it writes `=x=yes`, off on an edit writes `=!x=`, which
	// clears it in the same command, and off on a create writes nothing.
	TypeFlag   Type = "flag"
	TypeSelect Type = "select"
	// TypeMulti is a set chosen from Options, carried as a comma list in the
	// Options' own order ("read,write,api"). See Field.NegateUnset.
	TypeMulti Type = "multi"
	// TypeCode is multi-line RouterOS code — a script's source, a scheduler's
	// on-event. Newlines and tabs are its content, so unlike TypeText it allows
	// them (a CRLF is stored as LF) and is not trimmed; every other control
	// character is still refused. Always paired with Field.Code.
	TypeCode Type = "code"
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
	// Display is a field the form SHOWS and never sends. Validate drops it, and
	// every write path builds from Validate's output, so it never reaches the
	// router through a save, a preview or an undo. It must not be Clearable:
	// BuildArgs clears an absent clearable field on an edit, which would send
	// it blank. TestNoDisplayFieldIsClearable holds that. It exists for the field a row is
	// identified by when that field must not be edited from here: an
	// interface's name is what WAN uplinks, the traffic pick and topology pins
	// are keyed on, so renaming one would orphan all three.
	Display bool

	// Code marks a field whose value is RouterOS CODE: a script's source, a
	// scheduler's on-event. Writing one is running a command by another route,
	// so a write that CHANGES it is held to the raw-command gate (the operator's
	// choice, 2026-09-18): a signed-in global administrator on every path, and
	// for the assistant also `aiAllowRawCommands` and the router's name typed
	// back. The `codeGate` guard and change_row enforce it; CodeChange decides.
	Code bool

	// NegateUnset makes a TypeMulti write name every option, the unchosen ones
	// prefixed `!`. RouterOS needs it where a positive list only ADDS: measured
	// on /user/group, `set policy=read` against a group holding `read,test,api`
	// changes nothing, and a policy is removed only when it is named with a `!`.
	// The cleaned value stays the positive list, so history and diffs compare
	// what was chosen rather than seventeen words; RowValues drops the negations
	// the router answers with.
	NegateUnset bool

	Required bool
	// Default is the value RouterOS holds when it does not REPORT the property:
	// some menus omit a property that is at its default. Measured on
	// /ip/dhcp-server: `conflict-detection` (default yes) is absent from print
	// until it is set, and a checkbox reading "absent" as off wrote `no` on the
	// next save — silently turning conflict detection off. A field whose property
	// can be omitted declares the documented default, and RowValues reads it.
	Default string
	// ClearAs is what CLEARING this field sends, when the empty string is not
	// what RouterOS means by "nothing".
	//
	// Measured on the CHR: `/ip/pool/set =next-pool=` is refused with "ambiguous
	// value of next-pool, more than one possible value matches input", because
	// the menu's own word for no next pool is `none`. An empty value is right for
	// most fields — a comment, an address — and wrong for the ones that carry a
	// sentinel, so the field says which it is rather than the write path guessing.
	ClearAs string
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
	case TypeBool, TypeFlag:
		return "checkbox"
	case TypeSelect:
		return "select"
	case TypeMulti:
		return "multi"
	case TypeCode:
		return "code"
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

	// Singleton marks a SETTINGS menu: one row, no `.id`, changed with a plain
	// `set` — /system/ntp/client, /system/clock, /snmp (the operator's choice,
	// 2026-09-18, over a hand-built page each). Its row is given SingletonID
	// wherever a menu is read (StampID), so every id-based path — finding the
	// row, staleness, read-back, history, change_row's `id` — works unchanged,
	// and IDWords leaves the id OUT of the command RouterOS receives. A singleton
	// is NoCreate and never removable; TestEverySingletonIsFixed holds that.
	Singleton bool

	// NoEdit refuses an update: the row can be seen and removed and nothing
	// else. /file is the case — `set contents=` would let anyone write a file,
	// and a page that can delete files has no business writing them.
	NoEdit bool

	// NoCreate refuses a create. An interface exists because hardware or another
	// menu made it; `/interface` has no `add`, so offering one would be a form
	// that can only fail at the router.
	NoCreate bool

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

// FieldByName is the declared field with this form name, or nil.
func (r *Resource) FieldByName(name string) *Field {
	for i := range r.Fields {
		if r.Fields[i].Name == name {
			return &r.Fields[i]
		}
	}
	return nil
}

// isDisplay reports whether the named field is shown and never sent.
func (r *Resource) isDisplay(name string) bool {
	for _, f := range r.Fields {
		if f.Name == name {
			return f.Display
		}
	}
	return false
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
	for _, n := range names {
		// A DISPLAY field is never sent, so its absence from the submission is
		// not a rename: it is the field the form showed and could not change.
		// Without this every comment edit on an interface warned.
		if r.isDisplay(n) {
			continue
		}
		if of(before, n) != values[n] {
			renamed = true
			break
		}
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
	if f.Type == TypeCode {
		code := strings.ReplaceAll(raw, "\r\n", "\n")
		for _, r := range code {
			if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
				return "", "contains a control character"
			}
		}
		max := 65535
		if f.Max != nil {
			max = *f.Max
		}
		if len(code) > max {
			return "", fmt.Sprintf("is longer than %d characters", max)
		}
		return code, ""
	}
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
	case TypeBool, TypeFlag:
		if truthyROS(s) {
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
	case TypeMulti:
		chosen := map[string]bool{}
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part == "" {
				continue
			}
			known := false
			for _, o := range f.Options {
				if o == part {
					known = true
					break
				}
			}
			if !known {
				return "", fmt.Sprintf("names %q, which is not one of the allowed values", part)
			}
			chosen[part] = true
		}
		out := make([]string, 0, len(chosen))
		for _, o := range f.Options {
			if chosen[o] {
				out = append(out, o)
			}
		}
		return strings.Join(out, ","), ""
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
		if f.Display {
			continue
		}
		if !f.Applies(values) {
			continue
		}
		raw, present := values[f.Name]
		blank := !present || strings.TrimSpace(raw) == ""

		if blank {
			// A checkbox that is off is a value, not an omission; so is an empty
			// set whose unchosen options are written as negations.
			if f.Type == TypeBool || f.Type == TypeFlag || (f.Type == TypeMulti && f.NegateUnset) {
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
		if has && f.Type == TypeMulti && f.NegateUnset {
			args = append(args, "="+f.ROS+"="+negateUnset(f.Options, val))
			continue
		}
		if f.Type == TypeFlag {
			if has && val == "yes" {
				args = append(args, "="+f.ROS+"=yes")
			} else if has && v.Editing {
				args = append(args, "=!"+f.ROS+"=")
			}
			continue
		}
		if has {
			args = append(args, "="+f.ROS+"="+val)
			continue
		}
		// Only on an edit, and only when declared clearable: on a create an
		// omitted property should keep RouterOS's own default. `ClearAs` is the
		// value that means "nothing" where the empty string does not.
		//
		// AND ONLY WHEN IT APPLIES. Validate drops a field whose ShowIf does not
		// hold, so without this every such field was "cleared": a remote log
		// action's rename also wrote an empty `memory-stop-on-full`.
		if v.Editing && f.Clearable && f.Applies(v.Values) {
			args = append(args, "="+f.ROS+"="+f.ClearAs)
		}
	}
	return args
}

// negateUnset writes every option, the ones not in `chosen` prefixed `!`.
func negateUnset(options []string, chosen string) string {
	set := map[string]bool{}
	for _, c := range strings.Split(chosen, ",") {
		set[strings.TrimSpace(c)] = true
	}
	out := make([]string, 0, len(options))
	for _, o := range options {
		if set[o] {
			out = append(out, o)
		} else {
			out = append(out, "!"+o)
		}
	}
	return strings.Join(out, ",")
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
// SingletonID is the id a singleton's one row is given. Not a RouterOS id
// (those are `*N`), so it can never collide with one.
const SingletonID = "singleton"

// StampID returns the row with its `.id`: unchanged for an ordinary menu, and a
// COPY carrying SingletonID for a singleton, so a row shared through the read
// cache is never mutated.
func (r *Resource) StampID(row map[string]string) map[string]string {
	if !r.Singleton || row == nil || row[".id"] != "" {
		return row
	}
	out := make(map[string]string, len(row)+1)
	for k, v := range row {
		out[k] = v
	}
	out[".id"] = SingletonID
	return out
}

// IDWords is how a command addresses one row: `=.id=` for an ordinary menu,
// nothing for a singleton, whose `set` names no row.
func (r *Resource) IDWords(id string) []string {
	if r.Singleton || id == "" {
		return nil
	}
	return []string{"=.id=" + id}
}

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
	if id != "" || r.Singleton {
		verb = "/set"
	}
	return strings.Join(append([]string{r.Menu + verb}, append(r.IDWords(id), words...)...), " ")
}

// IdentityOf is the identity value carried by a freshly-read row. It is not a
// primary key and does not need to be: the row is ADDRESSED by its `.id`, and
// the identity only has to answer "is this still the row I was looking at when
// I clicked". Mutation is what it catches.
func (r *Resource) IdentityOf(row map[string]string) string {
	if row == nil {
		return ""
	}
	// A settings menu's one row is identified by what it IS: its label, which is
	// also what its audit rows should name.
	if r.Singleton {
		return r.Label
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
			"help": f.Help, "showIf": showIf, "min": minv, "max": maxv, "display": f.Display,
		})
	}
	actions := make([]map[string]any, 0, len(r.Actions))
	for _, a := range r.Actions {
		actions = append(actions, map[string]any{"key": a.Key, "label": a.Label})
	}
	return map[string]any{
		"key": r.Key, "label": r.Label, "title": r.Title, "page": r.Page,
		"identity": r.IdentityJSON(), "actions": actions, "fields": fields,
		"creatable": !r.NoCreate, "editable": !r.NoEdit,
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

// pppActions: a subscriber account can be switched off without deleting it,
// which is what an ISP does to a customer rather than removing them.
var pppActions = []Action{
	{Key: "enable", Verb: "enable", Label: "Enable",
		When: func(r map[string]string) bool { return r["disabled"] == "true" },
		Note: "enabled a PPP secret"},
	{Key: "disable", Verb: "disable", Label: "Disable",
		When: func(r map[string]string) bool { return r["disabled"] != "true" },
		Note: "disabled a PPP secret"},
}

// PPPSecret is the PPPoE/PPP subscriber account — issue #125.
//
// ── THE PASSWORD IS WRITE-ONLY, AND THAT IS THE WHOLE DESIGN ────────────────
//
// `/ppp/secret` holds account passwords in clear text, which is why
// `internal/collect/ppp.go` refused to read the menu at all for the whole life
// of the project. This resource does not change that rule so much as split it:
// the collector's proplist never asks for `password`, so no password can reach a
// browser, and TypeSecret carries one in the other direction only. RowValues
// drops it, so the edit form opens blank and an unchanged save leaves the
// router's password alone; PreviewCommand masks it; the audit trail masks it by
// type. That is the same arrangement `WgPeer.presharedKey` already relies on.
//
// ── SERVICE IS SUGGESTIONS, NOT A SELECT, AND THAT IS DELIBERATE ────────────
//
// All eight values RouterOS documents are listed, because listing a subset is
// how `dnsStatic` rewrote MX records as A records. But the type stays TEXT: a
// hard select refuses a value the router itself accepts, and this vocabulary is
// version-dependent in a way DNS record types are not — `async` and `isdn` are
// legacy, and MikroTik has added transports before. The renderer still draws a
// dropdown, and `web/src/resource.ts` re-inserts the router's current value when
// the list does not name it, so an unknown service displays honestly AND saves
// unharmed. That is one more defence than a select can offer.
//
// NO GUARD. `selfPath` answers "which interfaces is the management session
// behind", and a secret is an account rather than an interface: disabling one
// cuts the SUBSCRIBER, not the operator, whose path is a WAN or LAN interface.
// The same reasoning `WgPeer` records. The residual case — an operator managing
// a router through a PPPoE session that this same router authenticates — is
// narrow, self-inflicted, and not modelled by any guard that exists.
var PPPSecret = &Resource{
	Key: "pppSecret", Page: "ppp", Label: "PPP Secret",
	Title: "PPP Secret", Menu: "/ppp/secret", Identity: []string{"name"},
	Actions: pppActions,
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "User", Type: TypeText, Required: true,
			Placeholder: "subscriber01"},
		{Name: "password", ROS: "password", Label: "Password", Type: TypeSecret,
			Help: "leave blank to keep the current password"},
		{Name: "service", ROS: "service", Label: "Service", Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: []string{
				"any", "async", "isdn", "l2tp", "pppoe", "pptp", "ovpn", "sstp"}},
			Placeholder: "any"},
		{Name: "profile", ROS: "profile", Label: "Profile", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/ppp/profile", Value: "name"},
			Placeholder: "default"},
		{Name: "localAddress", ROS: "local-address", Label: "Local Address",
			Type: TypeIP, Clearable: true},
		{Name: "remoteAddress", ROS: "remote-address", Label: "Remote Address",
			Type: TypeIP, Clearable: true},
		// A MAC for PPPoE and an IP for PPTP/L2TP, so it cannot be TypeMac.
		{Name: "callerId", ROS: "caller-id", Label: "Caller ID", Type: TypeText,
			Clearable: true, Help: "MAC address for PPPoE, IP address for PPTP and L2TP"},
		// MAX RAISED FROM THE 255-CHARACTER DEFAULT. `Field.check` caps TypeText
		// at 255 unless told otherwise, and `routes` is a COMMA-SEPARATED LIST —
		// four entries clear 255 easily. The default would have refused a value
		// the router accepts, with a message about length that says nothing about
		// why. `Max` only reaches the browser for number inputs, so this changes
		// validation and not rendering.
		{Name: "routes", ROS: "routes", Label: "Routes", Type: TypeText, Clearable: true,
			Max:  intp(2048),
			Help: "dst-address gateway metric, several separated by commas. Ignored for OpenVPN"},
		// CLEARABLE, because both default to 0 and 0 means "no limit". Without it
		// an operator could set a cap and never take it off again: an emptied box
		// sends nothing, so the router would keep the old figure.
		{Name: "limitBytesIn", ROS: "limit-bytes-in", Label: "Limit In (bytes)",
			Type: TypeInt, Min: intp(0), Clearable: true, Placeholder: "0"},
		{Name: "limitBytesOut", ROS: "limit-bytes-out", Label: "Limit Out (bytes)",
			Type: TypeInt, Min: intp(0), Clearable: true, Placeholder: "0"},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

// PPPProfile is the settings block a secret points at — issue #125.
//
// `default` and `default-encryption` are RouterOS built-ins. The router refuses
// to delete them, so RemovableWhen refuses first: the operator gets a sentence
// saying why rather than a bare `router-denied` from the far end. They remain
// EDITABLE, because RouterOS does allow that and operators legitimately set a
// local address or a rate limit on the default profile.
//
// NO GUARD, for the same reason as the secret: a profile is addressing and rate
// policy for dial-in clients, not a path to the router.
// ── Areas ───────────────────────────────────────────────────────────────────
//
// A resource whose page is a GENERATED one — see internal/areas. Nothing about
// the resource is different; the page it belongs to is declared rather than
// hand-built, and `Page` is that declaration's key.

// IPPool is /ip/pool: the address ranges DHCP servers and PPP profiles hand out.
//
// Properties checked against the RouterOS 7 documentation for /ip/pool: name,
// ranges, next-pool and comment, and no others.
// AddressList is one entry in a firewall address list (slice 6). Documented at
// help.mikrotik.com, Firewall > Address-lists; properties from the command tree.
//
// ── IDENTIFIED BY ADDRESS ALONE ─────────────────────────────────────────────
//
// One address may sit in several lists, so the ROW is not unique by address —
// but a row is ADDRESSED by its `.id`, and identity only confirms it is still
// the row that was clicked. A composite list+address identity would say more,
// and it would also make every audit row and proposal title the two joined by
// U+0001, and empty on a create, as firewall rules are. Readable wins here.
//
// ── A DYNAMIC ENTRY STAYS EDITABLE ──────────────────────────────────────────
//
// Unlike a dynamic IP address, which belongs to whatever created it, a dynamic
// address-list entry is usually one a firewall rule added with a timeout, and
// removing it is the commonest thing an operator comes here to do: unblocking a
// host. So there is no ReadOnlyWhen.
var AddressList = &Resource{
	Key: "addressList", Page: "address-lists", Label: "Address List Entry",
	Title: "Address List Entry", Menu: "/ip/firewall/address-list", Identity: []string{"address"},
	// Putting MikroDash's own address on a list an input drop matches, or
	// taking it off one an input accept matches, locks it out: guard/listguard.go.
	Guard: []string{"listLockout"},
	Fields: []Field{
		// PLAIN TEXT, NOT A PICKER. OptionsFrom renders a strict select, and a new
		// list is created by naming it here.
		{Name: "list", ROS: "list", Label: "List", Type: TypeText, Required: true,
			Placeholder: "blocklist"},
		// Not TypeCidr: RouterOS takes a host, a prefix, a range or a DNS name.
		{Name: "address", ROS: "address", Label: "Address", Type: TypeText, Required: true,
			Placeholder: "192.0.2.10",
			Help:        "A host, a prefix (192.0.2.0/24), a range (192.0.2.10-192.0.2.20) or a DNS name."},
		// NOT CLEARABLE: there is no documented way to unset a timeout, and
		// an entry that has one is dynamic and will expire anyway.
		{Name: "timeout", ROS: "timeout", Label: "Timeout", Type: TypeText, Placeholder: "1d",
			Help: "Leave empty for a permanent entry. An entry with a timeout is dynamic: it is removed when the timeout runs out and is not saved in the configuration."},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		// Shown, never sent.
		{Name: "dynamic", ROS: "dynamic", Label: "Dynamic", Type: TypeBool, Display: true},
		{Name: "creationTime", ROS: "creation-time", Label: "Created", Type: TypeText, Display: true},
	},
}

// IfList and IfListMember are interface lists and who is in them (slice 6).
// From the RouterOS command tree for /interface/list and /interface/list/member.
//
// ── BOTH ARE GUARDED ────────────────────────────────────────────────────────
//
// The default configuration's last input rule drops `in-interface-list=!LAN`,
// so taking the port MikroDash arrives on out of `LAN` locks it out, and so does
// deleting, renaming or re-including `LAN` itself. `listLockout` warns on both:
// guard/listguard.go.
var IfList = &Resource{
	Key: "ifList", Page: "interface-lists", Label: "Interface List",
	Title: "Interface List", Menu: "/interface/list", Identity: []string{"name"},
	// all, none, dynamic and static are RouterOS's own, and cannot be changed.
	ReadOnlyWhen:   func(r map[string]string) bool { return r["builtin"] == "true" },
	ReadOnlyReason: "read-only-row",
	Guard:          []string{"listLockout"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "LAN"},
		{Name: "include", ROS: "include", Label: "Include", Type: TypeText, Clearable: true,
			Placeholder: "dynamic",
			Help:        "Other lists whose interfaces are also members, comma separated: all, dynamic, static, none, or a list of your own."},
		{Name: "exclude", ROS: "exclude", Label: "Exclude", Type: TypeText, Clearable: true,
			Help: "Lists whose interfaces are never members, comma separated."},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "builtin", ROS: "builtin", Label: "Built-in", Type: TypeBool, Display: true},
		{Name: "dynamic", ROS: "dynamic", Label: "Dynamic", Type: TypeBool, Display: true},
	},
}
var IfListMember = &Resource{
	Key: "ifListMember", Page: "interface-lists", Label: "Interface List Member",
	Title: "Interface List Member", Menu: "/interface/list/member", Identity: []string{"interface"},
	// A dynamic member belongs to whatever added it (a PPP profile's
	// interface-list, for one), as a dynamic address does.
	ReadOnlyWhen:   func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason: "read-only-row",
	Guard:          []string{"listLockout"},
	Fields: []Field{
		// PICKERS: a member must name a list and an interface that exist.
		{Name: "list", ROS: "list", Label: "List", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/interface/list", Value: "name"}},
		{Name: "interface", ROS: "interface", Label: "Interface", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/interface", Value: "name"}},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "dynamic", ROS: "dynamic", Label: "Dynamic", Type: TypeBool, Display: true},
	},
}

// IPService is /ip/service (slice 6): the router's own services, which exist
// already and are edited, never created or removed. From the RouterOS command
// tree for /ip/service/set, and a 7.24 capture.
//
// ── TWO THINGS 7.24 CHANGED ─────────────────────────────────────────────────
//
// The access restriction is `available-from`, renamed from `address` in 7.24
// ("backwards compatible via deprecation"); 7.24 prints only the new name, so
// that is the field. A router older than 7.24 shows it empty here and refuses a
// write of it, which is loud rather than wrong. And the menu now lists live
// CONNECTIONS as dynamic rows (connection=true, local, remote), one of them
// MikroDash's own session: dynamic rows open read-only.
//
// ── GUARDED: THE SERVICE MIKRODASH CONNECTS THROUGH ─────────────────────────
//
// `serviceLockout` REFUSES disabling, re-porting, re-VRFing or address-
// restricting api or api-ssl, whichever this session uses: guard/serviceguard.go.
var IPService = &Resource{
	Key: "ipService", Page: "ip-services", Label: "IP Service",
	Title: "IP Service", Menu: "/ip/service", Identity: []string{"name"},
	NoCreate:       true,
	RemovableWhen:  func(map[string]string) bool { return false },
	ReadOnlyWhen:   func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason: "read-only-row",
	Guard:          []string{"serviceLockout"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Service", Type: TypeText, Display: true},
		{Name: "port", ROS: "port", Label: "Port", Type: TypeInt, Required: true, Min: intp(1), Max: intp(65535)},
		{Name: "availableFrom", ROS: "available-from", Label: "Available From", Type: TypeText, Clearable: true,
			Placeholder: "192.0.2.0/24",
			Help:        "Addresses or prefixes allowed to connect, comma separated. Empty admits every address."},
		{Name: "certificate", ROS: "certificate", Label: "Certificate", Type: TypeText, Clearable: true, ClearAs: "none",
			Help: "The certificate a TLS service presents. Empty means none."},
		// Plain text, not a select: the documented values are any and only-1.2,
		// and a strict select would rewrite any other value a newer RouterOS holds.
		{Name: "tlsVersion", ROS: "tls-version", Label: "TLS Version", Type: TypeText, Placeholder: "any"},
		{Name: "maxSessions", ROS: "max-sessions", Label: "Max Sessions", Type: TypeInt, Min: intp(1), Max: intp(1000)},
		{Name: "vrf", ROS: "vrf", Label: "VRF", Type: TypeText, Placeholder: "main"},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "proto", ROS: "proto", Label: "Protocol", Type: TypeText, Display: true},
		{Name: "dynamic", ROS: "dynamic", Label: "Dynamic", Type: TypeBool, Display: true},
		{Name: "remote", ROS: "remote", Label: "Remote", Type: TypeText, Display: true},
	},
}

// Certificate is /certificate (slice 6). From the RouterOS command tree for
// /certificate/set and a 7.24 capture.
//
// ── WHAT IS OFFERED, AND WHY NOT MORE ───────────────────────────────────────
//
// Creating a certificate is a template followed by a signing, which is a flow
// rather than a row, so there is no Add. Most of /certificate/set applies only
// to an unsigned template, so on this page a certificate's name, whether it is
// trusted and its trust stores are what can change. RENAMING IS SAFE: measured
// on the CHR on 2026-09-18, a service's reference follows the rename.
//
// ── GUARDED, BECAUSE ROUTEROS IS NOT ────────────────────────────────────────
//
// Measured the same day: removing a certificate a service uses is ACCEPTED, and
// the service is left pointing at a dangling id. `certLockout` refuses removing
// the one api-ssl presents while MikroDash speaks TLS (guard/serviceguard.go).
// No private key is ever read: /certificate/print carries only a flag.
var Certificate = &Resource{
	Key: "certificate", Page: "certificates", Label: "Certificate",
	Title: "Certificate", Menu: "/certificate", Identity: []string{"name"},
	NoCreate: true,
	Guard:    []string{"certLockout"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true},
		{Name: "trusted", ROS: "trusted", Label: "Trusted", Type: TypeBool, Clearable: true},
		// Plain text: a comma-separated list of stores whose set grows with each
		// RouterOS version (twenty-three in 7.23), so a picker would go stale.
		{Name: "trustStore", ROS: "trust-store", Label: "Trust Store", Type: TypeText, Placeholder: "all",
			Help: "Which features may trust this certificate, comma separated: all, or e.g. ipsec, fetch, dns."},
		{Name: "commonName", ROS: "common-name", Label: "Common Name", Type: TypeText, Display: true},
		{Name: "privateKey", ROS: "private-key", Label: "Private Key", Type: TypeBool, Display: true},
		{Name: "authority", ROS: "authority", Label: "Authority", Type: TypeBool, Display: true},
		{Name: "keyType", ROS: "key-type", Label: "Key Type", Type: TypeText, Display: true},
		{Name: "keySize", ROS: "key-size", Label: "Key Size", Type: TypeText, Display: true},
		{Name: "invalidAfter", ROS: "invalid-after", Label: "Invalid After", Type: TypeText, Display: true},
		{Name: "expiresAfter", ROS: "expires-after", Label: "Expires In", Type: TypeText, Display: true},
		{Name: "fingerprint", ROS: "fingerprint", Label: "Fingerprint", Type: TypeText, Display: true},
	},
}

// Script is /system/script (slice 7). From the RouterOS command tree for
// /system/script/add.
//
// ── CODE IS HELD TO THE RAW-COMMAND GATE ────────────────────────────────────
//
// The source is RouterOS code, run later; the policy and the permission switch
// decide what that code may do. Changing any of them, or running a script, is
// running a command by another route, so all of it is behind `codeGate` (a
// signed-in global administrator) and, for the assistant, the raw-command gate
// with a typed confirmation — the operator's choice, 2026-09-18. Renaming a
// script or editing its comment is an ordinary write.
var Script = &Resource{
	Key: "script", Page: "scripts", Label: "Script",
	Title: "Script", Menu: "/system/script", Identity: []string{"name"},
	Guard:   []string{"codeGate"},
	Actions: []Action{{Key: "run", Verb: "run", Label: "Run", Note: "ran a script", RunsCode: true}},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true},
		{Name: "source", ROS: "source", Label: "Source", Type: TypeCode, Code: true,
			Help: "RouterOS script. Changing it is limited to global administrators."},
		// Plain text, not a TypeMulti: whether a script's policy list replaces or
		// adds is not measured, and a picker that silently only ADDED would leave
		// a policy granted that the operator believes removed.
		{Name: "policy", ROS: "policy", Label: "Policy", Type: TypeText, Code: true, Clearable: true,
			Placeholder: "read,write,test",
			Help:        "What the script may do, comma separated: ftp, reboot, read, write, policy, test, password, sniff, sensitive, romon."},
		{Name: "dontRequirePermissions", ROS: "dont-require-permissions", Label: "Don't Require Permissions",
			Type: TypeBool, Code: true, Clearable: true,
			Help: "Run with the script's own policy even when started by a user or scheduler that lacks it."},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "owner", ROS: "owner", Label: "Owner", Type: TypeText, Display: true},
		{Name: "runCount", ROS: "run-count", Label: "Runs", Type: TypeText, Display: true},
		{Name: "lastStarted", ROS: "last-started", Label: "Last Started", Type: TypeText, Display: true},
		{Name: "invalid", ROS: "invalid", Label: "Invalid", Type: TypeBool, Display: true},
	},
}

// Scheduler is /system/scheduler (slice 7). From the RouterOS command tree for
// /system/scheduler/add.
//
// Its on-event is RouterOS CODE and its policy decides what that code may do,
// so both are held to the code gate, as a script's are. Everything else —
// the name, when and how often it runs, enabling and disabling it — is an
// ordinary write: the operator's choice named "intervals, enable and disable"
// as working as normal (2026-09-18).
var Scheduler = &Resource{
	Key: "scheduler", Page: "scheduler", Label: "Scheduled Task",
	Title: "Scheduled Task", Menu: "/system/scheduler", Identity: []string{"name"},
	Guard: []string{"codeGate"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true},
		{Name: "onEvent", ROS: "on-event", Label: "On Event", Type: TypeCode, Code: true,
			Help: "RouterOS script, or the name of a script, to run. Changing it is limited to global administrators."},
		{Name: "startDate", ROS: "start-date", Label: "Start Date", Type: TypeText, Placeholder: "2026-01-01"},
		{Name: "startTime", ROS: "start-time", Label: "Start Time", Type: TypeText, Placeholder: "03:00:00",
			Help: "A time of day, or startup to run once at boot."},
		{Name: "interval", ROS: "interval", Label: "Interval", Type: TypeText, Placeholder: "1d",
			Help: "How often it repeats. 0s runs it once."},
		{Name: "policy", ROS: "policy", Label: "Policy", Type: TypeText, Code: true, Clearable: true,
			Placeholder: "read,write,test",
			Help:        "What the task's code may do, comma separated."},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "owner", ROS: "owner", Label: "Owner", Type: TypeText, Display: true},
		{Name: "runCount", ROS: "run-count", Label: "Runs", Type: TypeText, Display: true},
		{Name: "nextRun", ROS: "next-run", Label: "Next Run", Type: TypeText, Display: true},
	},
}

// NTPClient and NTPServer are the NTP client's settings and its server list
// (slice 7). From the NTP documentation ("NTP Client properties") and the
// command tree for /system/ntp/client/servers/add. The client is the first
// SINGLETON: one row, changed with a plain set.
var NTPClient = &Resource{
	Key: "ntpClient", Page: "ntp-client", Label: "NTP Client",
	Title: "NTP Client", Menu: "/system/ntp/client", Singleton: true,
	NoCreate:      true,
	RemovableWhen: func(map[string]string) bool { return false },
	Fields: []Field{
		{Name: "enabled", ROS: "enabled", Label: "Enabled", Type: TypeBool, Clearable: true},
		// The documented set, complete: a select is safe here, unlike an open list.
		{Name: "mode", ROS: "mode", Label: "Mode", Type: TypeSelect,
			Options: []string{"unicast", "broadcast", "multicast", "manycast"}},
		{Name: "servers", ROS: "servers", Label: "Servers", Type: TypeText, Clearable: true,
			Placeholder: "pool.ntp.org",
			Help:        "Addresses or host names, comma separated. Per-server options are on the Servers tab."},
		{Name: "vrf", ROS: "vrf", Label: "VRF", Type: TypeText, Placeholder: "main"},
		{Name: "status", ROS: "status", Label: "Status", Type: TypeText, Display: true},
		{Name: "syncedServer", ROS: "synced-server", Label: "Synced Server", Type: TypeText, Display: true},
		{Name: "systemOffset", ROS: "system-offset", Label: "System Offset", Type: TypeText, Display: true},
		{Name: "freqDrift", ROS: "freq-drift", Label: "Frequency Drift", Type: TypeText, Display: true},
	},
}
var NTPServer = &Resource{
	Key: "ntpServer", Page: "ntp-client", Label: "NTP Server",
	Title: "NTP Server", Menu: "/system/ntp/client/servers", Identity: []string{"address"},
	// A dynamic row is one RouterOS made from somewhere else, most likely the
	// client's own `servers` list (reported dynamic on 7.24): edited there.
	ReadOnlyWhen:   func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason: "read-only-row",
	Fields: []Field{
		{Name: "address", ROS: "address", Label: "Address", Type: TypeText, Required: true,
			Placeholder: "pool.ntp.org", Help: "An address or a host name."},
		{Name: "iburst", ROS: "iburst", Label: "Initial Burst", Type: TypeBool, Clearable: true},
		{Name: "minPoll", ROS: "min-poll", Label: "Min Poll", Type: TypeText, Placeholder: "6"},
		{Name: "maxPoll", ROS: "max-poll", Label: "Max Poll", Type: TypeText, Placeholder: "10"},
		{Name: "authKey", ROS: "auth-key", Label: "Auth Key", Type: TypeText, Clearable: true, ClearAs: "none"},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "dynamic", ROS: "dynamic", Label: "Dynamic", Type: TypeBool, Display: true},
	},
}

// Clock is /system/clock (slice 7), a singleton. From the command tree for
// /system/clock/set.
//
// ── THE TIME AND DATE ARE SHOWN, NEVER SENT ─────────────────────────────────
//
// They change every second. As editable fields, every save of the time zone
// would re-send the time the form was OPENED with and set the clock back by
// however long the dialog stood open. So they are Display: the time zone and
// its autodetection are what this page changes, and NTP sets the clock.
var Clock = &Resource{
	Key: "clock", Page: "clock", Label: "Clock",
	Title: "Clock", Menu: "/system/clock", Singleton: true,
	NoCreate:      true,
	RemovableWhen: func(map[string]string) bool { return false },
	Fields: []Field{
		{Name: "timeZoneAutodetect", ROS: "time-zone-autodetect", Label: "Detect Time Zone", Type: TypeBool, Clearable: true},
		// Plain text: the IANA list, several hundred names long and growing.
		{Name: "timeZoneName", ROS: "time-zone-name", Label: "Time Zone", Type: TypeText,
			Placeholder: "Europe/Berlin", Help: "An IANA time zone name, or manual."},
		{Name: "time", ROS: "time", Label: "Time", Type: TypeText, Display: true},
		{Name: "date", ROS: "date", Label: "Date", Type: TypeText, Display: true},
		{Name: "gmtOffset", ROS: "gmt-offset", Label: "GMT Offset", Type: TypeText, Display: true},
		{Name: "dstActive", ROS: "dst-active", Label: "Daylight Saving", Type: TypeBool, Display: true},
	},
}

// LogRule and LogAction are /system/logging and its actions (slice 7). From the
// Log documentation ("Actions") and the command tree for /system/logging/add.
//
// A rule sends topics to an action; an action is where they go. An action with
// target=script RUNS a script for every matching line, so which script it names
// is code and is held to codeGate, as a scheduler's on-event is. A row another
// feature manages (`managed`) opens read-only, and RouterOS's default actions
// cannot be removed.
//
// Known and recorded, not guarded: MikroDash's own Logs page reads the router's
// `memory` buffer, so disabling every rule that logs to memory blanks it. That
// is not a lockout, and the operator can see it happen.
var LogRule = &Resource{
	Key: "logRule", Page: "logging", Label: "Logging Rule",
	Title: "Logging Rule", Menu: "/system/logging", Identity: []string{"topics"},
	ReadOnlyWhen:   func(r map[string]string) bool { return r["managed"] == "true" },
	ReadOnlyReason: "read-only-row",
	Fields: []Field{
		// Plain text: a comma list over a topic set that grows with each version,
		// and a topic may be negated with `!`.
		{Name: "topics", ROS: "topics", Label: "Topics", Type: TypeText, Required: true,
			Placeholder: "info,!debug", Help: "Topics, comma separated; ! excludes one."},
		{Name: "action", ROS: "action", Label: "Action", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/system/logging/action", Value: "name"}},
		{Name: "prefix", ROS: "prefix", Label: "Prefix", Type: TypeText, Clearable: true},
		{Name: "regex", ROS: "regex", Label: "Regex", Type: TypeText, Clearable: true,
			Help: "Only lines whose message matches."},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "isDefault", ROS: "default", Label: "Default", Type: TypeBool, Display: true},
	},
}

func logTarget(t ...string) *ShowIf { return &ShowIf{Field: "target", In: t} }

var LogAction = &Resource{
	Key: "logAction", Page: "logging", Label: "Logging Action",
	Title: "Logging Action", Menu: "/system/logging/action", Identity: []string{"name"},
	ReadOnlyWhen:   func(r map[string]string) bool { return r["managed"] == "true" },
	ReadOnlyReason: "read-only-row",
	RemovableWhen:  func(r map[string]string) bool { return r["default"] != "true" && r["managed"] != "true" },
	Guard:          []string{"codeGate"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true},
		// The documented set, complete.
		{Name: "target", ROS: "target", Label: "Target", Type: TypeSelect, Required: true,
			Options: []string{"memory", "disk", "echo", "remote", "email", "script"}},
		{Name: "memoryLines", ROS: "memory-lines", Label: "Memory Lines", Type: TypeInt, Min: intp(1), Max: intp(65535), ShowIf: logTarget("memory")},
		{Name: "memoryStopOnFull", ROS: "memory-stop-on-full", Label: "Stop When Full", Type: TypeBool, Clearable: true, ShowIf: logTarget("memory")},
		{Name: "diskFileName", ROS: "disk-file-name", Label: "File Name", Type: TypeText, ShowIf: logTarget("disk")},
		{Name: "diskLinesPerFile", ROS: "disk-lines-per-file", Label: "Lines Per File", Type: TypeInt, Min: intp(1), Max: intp(65535), ShowIf: logTarget("disk")},
		{Name: "diskFileCount", ROS: "disk-file-count", Label: "File Count", Type: TypeInt, Min: intp(1), Max: intp(65535), ShowIf: logTarget("disk")},
		{Name: "diskStopOnFull", ROS: "disk-stop-on-full", Label: "Stop When Full", Type: TypeBool, Clearable: true, ShowIf: logTarget("disk")},
		{Name: "remote", ROS: "remote", Label: "Remote Address", Type: TypeText, ShowIf: logTarget("remote")},
		{Name: "remotePort", ROS: "remote-port", Label: "Remote Port", Type: TypeInt, Min: intp(1), Max: intp(65535), ShowIf: logTarget("remote")},
		{Name: "remoteProtocol", ROS: "remote-protocol", Label: "Protocol", Type: TypeSelect,
			Options: []string{"udp", "tcp", "tls"}, ShowIf: logTarget("remote")},
		{Name: "remoteLogFormat", ROS: "remote-log-format", Label: "Format", Type: TypeSelect,
			Options: []string{"default", "syslog", "cef"}, ShowIf: logTarget("remote")},
		{Name: "srcAddress", ROS: "src-address", Label: "Source Address", Type: TypeText, ShowIf: logTarget("remote")},
		{Name: "emailTo", ROS: "email-to", Label: "Email To", Type: TypeText, ShowIf: logTarget("email")},
		// Which script runs for every matching line: code, as an on-event is.
		{Name: "script", ROS: "script", Label: "Script", Type: TypeText, Code: true, ShowIf: logTarget("script"),
			Help: "The script to run for each matching line. Changing it is limited to global administrators."},
		{Name: "isDefault", ROS: "default", Label: "Default", Type: TypeBool, Display: true},
	},
}

// SNMP and SNMPCommunity are /snmp (a singleton) and its communities (slice 7).
// From the command tree for /snmp/set and /snmp/community/add.
//
// The community passwords are secrets: never read (the areas proplist skips
// them), never shown, never in a fixture. A v1/v2c community NAME is a shared
// credential too; it is shown to whoever may read this page, as WinBox shows
// it, because a community cannot be managed without being named. The protocols
// are plain text rather than selects: the documented pair grows with RouterOS,
// and a select rewrites a value it does not list.
var SNMP = &Resource{
	Key: "snmp", Page: "snmp", Label: "SNMP",
	Title: "SNMP", Menu: "/snmp", Singleton: true,
	NoCreate:      true,
	RemovableWhen: func(map[string]string) bool { return false },
	Fields: []Field{
		{Name: "enabled", ROS: "enabled", Label: "Enabled", Type: TypeBool, Clearable: true},
		{Name: "contact", ROS: "contact", Label: "Contact", Type: TypeText, Clearable: true},
		{Name: "location", ROS: "location", Label: "Location", Type: TypeText, Clearable: true},
		{Name: "trapTarget", ROS: "trap-target", Label: "Trap Target", Type: TypeText, Clearable: true,
			Help: "Addresses traps are sent to, comma separated."},
		{Name: "trapCommunity", ROS: "trap-community", Label: "Trap Community", Type: TypeText},
		{Name: "trapVersion", ROS: "trap-version", Label: "Trap Version", Type: TypeSelect, Options: []string{"1", "2", "3"}},
		{Name: "trapGenerators", ROS: "trap-generators", Label: "Trap Generators", Type: TypeText, Clearable: true,
			Placeholder: "interfaces,start-trap,temp-exception"},
		{Name: "trapInterfaces", ROS: "trap-interfaces", Label: "Trap Interfaces", Type: TypeText, Clearable: true},
		{Name: "srcAddress", ROS: "src-address", Label: "Source Address", Type: TypeText},
		{Name: "engineIdSuffix", ROS: "engine-id-suffix", Label: "Engine ID Suffix", Type: TypeText, Clearable: true},
		{Name: "vrf", ROS: "vrf", Label: "VRF", Type: TypeText, Placeholder: "main"},
		{Name: "engineId", ROS: "engine-id", Label: "Engine ID", Type: TypeText, Display: true},
	},
}
var SNMPCommunity = &Resource{
	Key: "snmpCommunity", Page: "snmp", Label: "SNMP Community",
	Title: "SNMP Community", Menu: "/snmp/community", Identity: []string{"name"},
	// The default community is RouterOS's own.
	RemovableWhen: func(r map[string]string) bool { return r["default"] != "true" },
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true},
		{Name: "addresses", ROS: "addresses", Label: "Addresses", Type: TypeText,
			Placeholder: "192.0.2.0/24", Help: "Where requests are accepted from, comma separated."},
		{Name: "security", ROS: "security", Label: "Security", Type: TypeSelect,
			Options: []string{"none", "authorized", "private"}},
		{Name: "readAccess", ROS: "read-access", Label: "Read Access", Type: TypeBool, Clearable: true},
		{Name: "writeAccess", ROS: "write-access", Label: "Write Access", Type: TypeBool, Clearable: true,
			Help: "Write access lets SNMP change this router's configuration."},
		{Name: "authenticationProtocol", ROS: "authentication-protocol", Label: "Auth Protocol", Type: TypeText, Placeholder: "SHA1"},
		{Name: "authenticationPassword", ROS: "authentication-password", Label: "Auth Password", Type: TypeSecret},
		{Name: "encryptionProtocol", ROS: "encryption-protocol", Label: "Encryption Protocol", Type: TypeText, Placeholder: "AES"},
		{Name: "encryptionPassword", ROS: "encryption-password", Label: "Encryption Password", Type: TypeSecret},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "isDefault", ROS: "default", Label: "Default", Type: TypeBool, Display: true},
	},
}

// File is /file (slice 7): read and delete, no upload, never edited. From the
// command tree for /file.
//
// ── NOTHING HERE WRITES A FILE ──────────────────────────────────────────────
//
// `/file set contents=` and `/file add` write file contents, so the resource is
// NoCreate and NoEdit, and every field is Display: the table is for seeing what
// is there and removing what should not be. Nor are contents ever READ — the
// areas proplist names only the declared fields, and `contents` is not one — so
// a file holding a key or a config export never reaches the browser. A disk
// listed here is storage, not a file, and cannot be removed from this page.
// MikroDash's own backups make and remove temporary files of their own; deleting
// one mid-backup fails that backup, which says so.
var File = &Resource{
	Key: "file", Page: "files", Label: "File",
	Title: "File", Menu: "/file", Identity: []string{"name"},
	NoCreate:      true,
	NoEdit:        true,
	RemovableWhen: func(r map[string]string) bool { return r["type"] != "disk" },
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Display: true},
		{Name: "type", ROS: "type", Label: "Type", Type: TypeText, Display: true},
		{Name: "size", ROS: "size", Label: "Size", Type: TypeText, Display: true},
		{Name: "lastModified", ROS: "last-modified", Label: "Last Modified", Type: TypeText, Display: true},
	},
}

var IPPool = &Resource{
	Key: "ipPool", Page: "ip-pools", Label: "IP Pool",
	Title: "IP Pool", Menu: "/ip/pool", Identity: []string{"name"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true,
			Placeholder: "dhcp-pool"},
		{Name: "ranges", ROS: "ranges", Label: "Ranges", Type: TypeText, Required: true,
			Placeholder: "10.0.0.50-10.0.0.254",
			Help:        "One or more ranges or prefixes, comma separated."},
		{Name: "nextPool", ROS: "next-pool", Label: "Next Pool", Type: TypeText,
			Clearable: true, ClearAs: "none",
			OptionsFrom: &OptionsFrom{Menu: "/ip/pool", Value: "name"},
			Help:        "Where addresses come from once this pool is exhausted. Empty means none."},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		// READ-ONLY, and SHOWN: the router reports how big a pool is and how much
		// of it is handed out, which is the question a pools page exists to
		// answer. Display fields are never sent, so they cannot be written back.
		{Name: "used", ROS: "used", Label: "Used", Type: TypeText, Display: true},
		{Name: "total", ROS: "total", Label: "Total", Type: TypeText, Display: true},
		{Name: "available", ROS: "available", Label: "Available", Type: TypeText, Display: true},
	},
}

// RoutingTable is /routing/table: the tables policy routing looks routes up in.
// `main` is dynamic and cannot be edited or removed. `fib` is what makes a table
// usable for forwarding, and is a presence flag (TypeFlag).
var RoutingTable = &Resource{
	Key: "routingTable", Page: "routing-tables", Label: "Routing Table",
	Title: "Routing Table", Menu: "/routing/table", Identity: []string{"name"},
	// A rule that looks routes up in a table goes inactive when the table is
	// removed or disabled, and finds nothing once its FIB is unset.
	Guard:          []string{"tableInUse"},
	ReadOnlyWhen:   func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason: "dynamic",
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "isp2"},
		{Name: "fib", ROS: "fib", Label: "FIB", Type: TypeFlag,
			Help: "Install this table's routes for forwarding. Policy routing needs it."},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "dynamic", ROS: "dynamic", Label: "Dynamic", Type: TypeBool, Display: true},
		{Name: "invalid", ROS: "invalid", Label: "Invalid", Type: TypeBool, Display: true},
	},
}

// RoutingRuleActions are the actions /routing/rule accepts on RouterOS 7.24, as
// the router lists them. `mangle` is on that list and not in the manual; it is
// offered so a rule holding it opens as what it is rather than as the first
// option, which a save would then write back.
var RoutingRuleActions = []string{"lookup", "lookup-only-in-table", "drop", "unreachable", "mangle"}

// RoutingRule is /routing/rule: policy routing, consulted before the main
// table in the default policy-rules order. ORDERED, because the first rule
// that matches decides. Guarded by rulePath: a rule can send the router's own
// replies to MikroDash to `unreachable` or into a table with no way back.
//
// `chain`, `realm` and `vrf` are not fields: rules live in the `user` chain
// unless somebody moved them, and an undeclared property is left as it is by
// every write here.
var RoutingRule = &Resource{
	Key: "routingRule", Page: "routing-rules", Label: "Routing Rule",
	Title: "Routing Rule", Menu: "/routing/rule",
	Identity: []string{"srcAddress", "dstAddress", "action", "table"},
	Ordered:  true,
	Guard:    []string{"rulePath"},
	Fields: []Field{
		{Name: "srcAddress", ROS: "src-address", Label: "Source", Type: TypeCidr, Clearable: true,
			Placeholder: "192.168.88.0/24"},
		{Name: "dstAddress", ROS: "dst-address", Label: "Destination", Type: TypeCidr, Clearable: true,
			Placeholder: "0.0.0.0/0"},
		{Name: "routingMark", ROS: "routing-mark", Label: "Routing Mark", Type: TypeText, Clearable: true,
			OptionsFrom: &OptionsFrom{Menu: "/routing/table", Value: "name"}},
		{Name: "interface", ROS: "interface", Label: "Interface", Type: TypeText, Clearable: true,
			OptionsFrom: &OptionsFrom{Menu: "/interface", Value: "name"},
			Help:        "The interface a packet arrived on. The router's own traffic has none."},
		{Name: "action", ROS: "action", Label: "Action", Type: TypeSelect, Required: true,
			Options: RoutingRuleActions},
		{Name: "table", ROS: "table", Label: "Table", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/routing/table", Value: "name"},
			ShowIf:      &ShowIf{Field: "action", In: []string{"lookup", "lookup-only-in-table"}}},
		{Name: "minPrefix", ROS: "min-prefix", Label: "Min Prefix", Type: TypeInt, Clearable: true,
			Min: intp(0), Max: intp(128),
			Help: "Ignore routes in the table shorter than this prefix length."},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "inactive", ROS: "inactive", Label: "Inactive", Type: TypeBool, Display: true},
	},
}

// ── OSPF ────────────────────────────────────────────────────────────────────
//
// /routing/ospf: instances, their areas, the interface templates that decide
// where OSPF speaks, and the neighbours it has found. Checked against rosetta and
// a live adjacency between the CHR and the hAP AC2 (RouterOS 7.24.3). Three
// properties are PRESENCE flags (TypeFlag): an area's no-summaries and a
// template's passive, as a routing table's fib is.
//
// NO GUARD, deliberately. OSPF routes are installed at distance 110, behind
// every connected and static route, so they cannot take MikroDash's own path
// away from a directly connected management address; what OSPF changes is what
// the NEIGHBOURS learn, which no guard here can see.

// OSPFRedistribute is what an instance can redistribute, as RouterOS 7.24 lists it.
var OSPFRedistribute = []string{"connected", "static", "rip", "ospf", "isis", "bgp", "vpn", "dhcp",
	"fantasy", "modem", "bgp-mpls-vpn", "slaac"}

var OSPFInstance = &Resource{
	Key: "ospfInstance", Page: "ospf", Label: "OSPF Instance",
	Title: "OSPF Instance", Menu: "/routing/ospf/instance", Identity: []string{"name"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "default-v2"},
		{Name: "version", ROS: "version", Label: "Version", Type: TypeSelect, Options: []string{"2", "3"},
			Help: "2 for IPv4, 3 for IPv6."},
		{Name: "routerId", ROS: "router-id", Label: "Router ID", Type: TypeText, Clearable: true,
			Placeholder: "10.0.0.1", Help: "An address, or the name of a /routing/id entry. Empty picks one."},
		{Name: "originateDefault", ROS: "originate-default", Label: "Originate Default", Type: TypeSelect,
			Options: []string{"never", "if-installed", "always"}},
		{Name: "redistribute", ROS: "redistribute", Label: "Redistribute", Type: TypeMulti,
			Options: OSPFRedistribute, Clearable: true},
		{Name: "inFilterChain", ROS: "in-filter-chain", Label: "In Filter Chain", Type: TypeText, Clearable: true},
		{Name: "outFilterChain", ROS: "out-filter-chain", Label: "Out Filter Chain", Type: TypeText, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "inactive", ROS: "inactive", Label: "Inactive", Type: TypeBool, Display: true},
	},
}

var OSPFArea = &Resource{
	Key: "ospfArea", Page: "ospf", Label: "OSPF Area",
	Title: "OSPF Area", Menu: "/routing/ospf/area", Identity: []string{"name"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "backbone"},
		{Name: "instance", ROS: "instance", Label: "Instance", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/routing/ospf/instance", Value: "name"}},
		{Name: "areaId", ROS: "area-id", Label: "Area ID", Type: TypeText, Placeholder: "0.0.0.0",
			Help: "Dotted form. 0.0.0.0 is the backbone."},
		{Name: "type", ROS: "type", Label: "Type", Type: TypeSelect, Options: []string{"default", "stub", "nssa"}},
		{Name: "noSummaries", ROS: "no-summaries", Label: "No Summaries", Type: TypeFlag,
			ShowIf: &ShowIf{Field: "type", In: []string{"stub", "nssa"}}},
		{Name: "nssaTranslator", ROS: "nssa-translator", Label: "NSSA Translator", Type: TypeSelect,
			Options: []string{"candidate", "yes", "no"}, ShowIf: &ShowIf{Field: "type", In: []string{"nssa"}}},
		{Name: "defaultCost", ROS: "default-cost", Label: "Default Cost", Type: TypeInt, Clearable: true, Min: intp(0)},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "inactive", ROS: "inactive", Label: "Inactive", Type: TypeBool, Display: true},
	},
}

// OSPFTemplate is /routing/ospf/interface-template. ORDERED: the first template
// that matches an interface is the one it takes.
var OSPFTemplate = &Resource{
	Key: "ospfTemplate", Page: "ospf", Label: "OSPF Interface Template",
	Title: "OSPF Interface Template", Menu: "/routing/ospf/interface-template",
	Identity: []string{"area", "interfaces", "networks"},
	Ordered:  true,
	Fields: []Field{
		{Name: "area", ROS: "area", Label: "Area", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/routing/ospf/area", Value: "name"}},
		{Name: "interfaces", ROS: "interfaces", Label: "Interfaces", Type: TypeText, Clearable: true,
			Placeholder: "ether1,bridge", Help: "Comma separated, or all, static or dynamic."},
		{Name: "networks", ROS: "networks", Label: "Networks", Type: TypeText, Clearable: true,
			Placeholder: "192.168.88.0/24", Help: "Interfaces holding an address in these networks."},
		{Name: "type", ROS: "type", Label: "Network Type", Type: TypeSelect,
			Options: []string{"broadcast", "nbma", "ptmp", "ptmp-broadcast", "ptp", "ptp-unnumbered"}},
		{Name: "cost", ROS: "cost", Label: "Cost", Type: TypeInt, Min: intp(1), Max: intp(65535)},
		{Name: "priority", ROS: "priority", Label: "Priority", Type: TypeInt, Min: intp(0), Max: intp(255)},
		{Name: "passive", ROS: "passive", Label: "Passive", Type: TypeFlag,
			Help: "Advertise the interface's network without speaking OSPF on it."},
		{Name: "auth", ROS: "auth", Label: "Authentication", Type: TypeSelect, Clearable: true,
			Options: []string{"simple", "md5", "sha1", "sha256", "sha384", "sha512"}},
		{Name: "authKey", ROS: "auth-key", Label: "Authentication Key", Type: TypeSecret,
			ShowIf: &ShowIf{Field: "auth", In: []string{"simple", "md5", "sha1", "sha256", "sha384", "sha512"}}},
		{Name: "authId", ROS: "auth-id", Label: "Key ID", Type: TypeInt, Clearable: true, Min: intp(0), Max: intp(255),
			ShowIf: &ShowIf{Field: "auth", In: []string{"md5", "sha1", "sha256", "sha384", "sha512"}}},
		{Name: "helloInterval", ROS: "hello-interval", Label: "Hello Interval", Type: TypeText, Placeholder: "10s"},
		{Name: "deadInterval", ROS: "dead-interval", Label: "Dead Interval", Type: TypeText, Placeholder: "40s"},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "inactive", ROS: "inactive", Label: "Inactive", Type: TypeBool, Display: true},
	},
}

// OSPFNeighbor is /routing/ospf/neighbor: what OSPF has found. Dynamic, so it
// can be seen and nothing else.
var OSPFNeighbor = &Resource{
	Key: "ospfNeighbor", Page: "ospf", Label: "OSPF Neighbor",
	Title: "OSPF Neighbor", Menu: "/routing/ospf/neighbor", Identity: []string{"routerId", "address"},
	NoCreate:      true,
	NoEdit:        true,
	RemovableWhen: func(map[string]string) bool { return false },
	Fields: []Field{
		{Name: "routerId", ROS: "router-id", Label: "Router ID", Type: TypeText, Display: true},
		{Name: "address", ROS: "address", Label: "Address", Type: TypeText, Display: true},
		{Name: "interface", ROS: "interface", Label: "Interface", Type: TypeText, Display: true},
		{Name: "instance", ROS: "instance", Label: "Instance", Type: TypeText, Display: true},
		{Name: "area", ROS: "area", Label: "Area", Type: TypeText, Display: true},
		{Name: "state", ROS: "state", Label: "State", Type: TypeText, Display: true},
		{Name: "adjacency", ROS: "adjacency", Label: "Adjacent For", Type: TypeText, Display: true},
		{Name: "stateChanges", ROS: "state-changes", Label: "State Changes", Type: TypeText, Display: true},
	},
}

// ── IPsec ───────────────────────────────────────────────────────────────────
//
// /ip/ipsec: peers, their identities and the policies that decide what is
// encrypted. Checked against rosetta and a live IKEv2 tunnel between the CHR and
// the hAP AC2 (RouterOS 7.24.3). Guarded by ipsecPath: a policy applies to the
// router's own traffic, so one covering MikroDash's address can take its path
// away, and so can removing the policy, peer or identity MikroDash arrives
// through.
//
// THE CREDENTIALS ARE NEVER READ. RouterOS returns an identity's `secret` in
// clear text on print (measured), and a peer's `ppk-secret`; both are secrets
// here, which the area read leaves out of its proplist and a form never shows.
// Certificate-based identities name certificates by name; `key`, `remote-key`,
// `eap-methods` and `notrack-chain` are left as they are.

var IPsecPeer = &Resource{
	Key: "ipsecPeer", Page: "ipsec", Label: "IPsec Peer",
	Title: "IPsec Peer", Menu: "/ip/ipsec/peer", Identity: []string{"name"},
	Guard:          []string{"ipsecPath"},
	ReadOnlyWhen:   func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason: "dynamic",
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "branch-office"},
		{Name: "address", ROS: "address", Label: "Address", Type: TypeText, Clearable: true,
			Placeholder: "203.0.113.10", Help: "The remote end: an address, a prefix, or a name to resolve."},
		{Name: "port", ROS: "port", Label: "Port", Type: TypeInt, Clearable: true, Min: intp(1), Max: intp(65535)},
		{Name: "localAddress", ROS: "local-address", Label: "Local Address", Type: TypeText, Clearable: true},
		{Name: "profile", ROS: "profile", Label: "Profile", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/ip/ipsec/profile", Value: "name"}},
		{Name: "exchangeMode", ROS: "exchange-mode", Label: "Exchange Mode", Type: TypeSelect,
			Options: []string{"ike2", "main", "aggressive"}},
		{Name: "passive", ROS: "passive", Label: "Passive", Type: TypeBool, Clearable: true,
			Help: "Wait for the remote end to connect rather than initiating."},
		{Name: "sendInitialContact", ROS: "send-initial-contact", Label: "Send Initial Contact", Type: TypeBool, Clearable: true},
		{Name: "ppkSecret", ROS: "ppk-secret", Label: "PPK Secret", Type: TypeSecret},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "responder", ROS: "responder", Label: "Responder", Type: TypeBool, Display: true},
		{Name: "dynamic", ROS: "dynamic", Label: "Dynamic", Type: TypeBool, Display: true},
	},
}

// IPsecAuthMethods are the identity authentication methods RouterOS 7.24 lists.
var IPsecAuthMethods = []string{"pre-shared-key", "digital-signature", "eap", "eap-radius",
	"pre-shared-key-xauth", "rsa-key", "rsa-signature-hybrid"}

var IPsecIdentity = &Resource{
	Key: "ipsecIdentity", Page: "ipsec", Label: "IPsec Identity",
	Title: "IPsec Identity", Menu: "/ip/ipsec/identity", Identity: []string{"peer", "authMethod"},
	Guard:          []string{"ipsecPath"},
	ReadOnlyWhen:   func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason: "dynamic",
	Fields: []Field{
		{Name: "peer", ROS: "peer", Label: "Peer", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/ip/ipsec/peer", Value: "name"}},
		{Name: "authMethod", ROS: "auth-method", Label: "Authentication", Type: TypeSelect, Required: true,
			Options: IPsecAuthMethods},
		{Name: "secret", ROS: "secret", Label: "Pre-shared Key", Type: TypeSecret,
			ShowIf: &ShowIf{Field: "authMethod", In: []string{"pre-shared-key", "pre-shared-key-xauth"}}},
		{Name: "certificate", ROS: "certificate", Label: "Certificate", Type: TypeText, Clearable: true,
			OptionsFrom: &OptionsFrom{Menu: "/certificate", Value: "name"},
			ShowIf:      &ShowIf{Field: "authMethod", In: []string{"digital-signature", "rsa-signature-hybrid", "eap"}}},
		{Name: "remoteCertificate", ROS: "remote-certificate", Label: "Remote Certificate", Type: TypeText,
			Clearable: true, ShowIf: &ShowIf{Field: "authMethod", In: []string{"digital-signature"}}},
		{Name: "username", ROS: "username", Label: "Username", Type: TypeText, Clearable: true,
			ShowIf: &ShowIf{Field: "authMethod", In: []string{"pre-shared-key-xauth", "eap"}}},
		{Name: "password", ROS: "password", Label: "Password", Type: TypeSecret,
			ShowIf: &ShowIf{Field: "authMethod", In: []string{"pre-shared-key-xauth", "eap"}}},
		{Name: "myId", ROS: "my-id", Label: "My ID", Type: TypeText, Placeholder: "auto",
			Help: "auto, address:…, fqdn:…, user-fqdn:…, key-id:… or dn"},
		{Name: "remoteId", ROS: "remote-id", Label: "Remote ID", Type: TypeText, Placeholder: "auto"},
		{Name: "matchBy", ROS: "match-by", Label: "Match By", Type: TypeSelect, Options: []string{"remote-id", "certificate"}},
		{Name: "generatePolicy", ROS: "generate-policy", Label: "Generate Policy", Type: TypeSelect,
			Options: []string{"no", "port-override", "port-strict"}},
		{Name: "modeConfig", ROS: "mode-config", Label: "Mode Config", Type: TypeText, Clearable: true,
			OptionsFrom: &OptionsFrom{Menu: "/ip/ipsec/mode-config", Value: "name"}},
		{Name: "policyTemplateGroup", ROS: "policy-template-group", Label: "Policy Template Group", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/ip/ipsec/policy/group", Value: "name"}},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "dynamic", ROS: "dynamic", Label: "Dynamic", Type: TypeBool, Display: true},
	},
}

// IPsecPolicy is /ip/ipsec/policy. ORDERED: the first matching policy decides.
// The built-in default template (default=true) and dynamic policies are read-only.
// `protocol` is free text rather than a select, because RouterOS accepts forty
// protocol names and a select missing the one a policy holds would rewrite it.
var IPsecPolicy = &Resource{
	Key: "ipsecPolicy", Page: "ipsec", Label: "IPsec Policy",
	Title: "IPsec Policy", Menu: "/ip/ipsec/policy",
	Identity: []string{"srcAddress", "dstAddress", "peer"},
	Ordered:  true,
	Guard:    []string{"ipsecPath"},
	ReadOnlyWhen: func(r map[string]string) bool {
		return r["dynamic"] == "true" || r["default"] == "true"
	},
	ReadOnlyReason: "read-only-row",
	Fields: []Field{
		{Name: "srcAddress", ROS: "src-address", Label: "Source", Type: TypeCidr, Placeholder: "192.168.88.0/24"},
		{Name: "dstAddress", ROS: "dst-address", Label: "Destination", Type: TypeCidr, Placeholder: "192.168.99.0/24"},
		{Name: "protocol", ROS: "protocol", Label: "Protocol", Type: TypeText, Placeholder: "all"},
		{Name: "srcPort", ROS: "src-port", Label: "Source Port", Type: TypeText, Placeholder: "any"},
		{Name: "dstPort", ROS: "dst-port", Label: "Destination Port", Type: TypeText, Placeholder: "any"},
		{Name: "action", ROS: "action", Label: "Action", Type: TypeSelect, Options: []string{"encrypt", "discard", "none"}},
		{Name: "level", ROS: "level", Label: "Level", Type: TypeSelect, Options: []string{"require", "unique", "use"},
			ShowIf: &ShowIf{Field: "action", In: []string{"encrypt"}}},
		{Name: "ipsecProtocols", ROS: "ipsec-protocols", Label: "IPsec Protocols", Type: TypeText, Placeholder: "esp",
			ShowIf: &ShowIf{Field: "action", In: []string{"encrypt"}}},
		{Name: "tunnel", ROS: "tunnel", Label: "Tunnel", Type: TypeBool, Clearable: true},
		{Name: "peer", ROS: "peer", Label: "Peer", Type: TypeText, Clearable: true,
			OptionsFrom: &OptionsFrom{Menu: "/ip/ipsec/peer", Value: "name"}},
		{Name: "proposal", ROS: "proposal", Label: "Proposal", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/ip/ipsec/proposal", Value: "name"}},
		{Name: "template", ROS: "template", Label: "Template", Type: TypeBool, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "ph2State", ROS: "ph2-state", Label: "Phase 2", Type: TypeText, Display: true},
		{Name: "active", ROS: "active", Label: "Active", Type: TypeBool, Display: true},
		{Name: "invalid", ROS: "invalid", Label: "Invalid", Type: TypeBool, Display: true},
	},
}

// ── OpenVPN ─────────────────────────────────────────────────────────────────
//
// /interface/ovpn-server/server (a LIST of named servers on 7.24, not the single
// settings menu older releases had) and /interface/ovpn-client. Checked against
// rosetta and a live tunnel from the CHR to a server on the hAP AC2 (7.24.3).
// The accounts a server admits are the PPP page's secrets.
//
// A CLIENT is an interface named after itself, so it carries selfPath: disabling
// or removing the client MikroDash arrives through, or changing where it
// connects, cuts the session. It also carries tunnelDefault: `add-default-route`
// installs 0.0.0.0/0 through the tunnel, which the route guard judges exactly as
// it would the same static route. RouterOS returns the client's `password` on
// print; it is a secret, never read.

// OVPNAuths and OVPNCiphers are the values RouterOS 7.24 lists. A server takes
// a comma list of each; a client takes one.
var (
	OVPNAuths   = []string{"sha1", "md5", "sha256", "sha384", "sha512", "null"}
	OVPNCiphers = []string{"blowfish128", "aes128-cbc", "aes192-cbc", "aes256-cbc", "aes128-gcm",
		"aes192-gcm", "aes256-gcm", "null"}
)

var OVPNServer = &Resource{
	Key: "ovpnServer", Page: "openvpn", Label: "OpenVPN Server",
	Title: "OpenVPN Server", Menu: "/interface/ovpn-server/server", Identity: []string{"name"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "ovpn-server1"},
		{Name: "port", ROS: "port", Label: "Port", Type: TypeInt, Min: intp(1), Max: intp(65535), Placeholder: "1194"},
		{Name: "protocol", ROS: "protocol", Label: "Protocol", Type: TypeSelect, Options: []string{"tcp", "udp"}},
		{Name: "mode", ROS: "mode", Label: "Mode", Type: TypeSelect, Options: []string{"ip", "ethernet"}},
		{Name: "certificate", ROS: "certificate", Label: "Certificate", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/certificate", Value: "name"}},
		{Name: "requireClientCertificate", ROS: "require-client-certificate", Label: "Require Client Certificate",
			Type: TypeBool, Clearable: true},
		{Name: "auth", ROS: "auth", Label: "Auth", Type: TypeMulti, Options: OVPNAuths},
		{Name: "cipher", ROS: "cipher", Label: "Cipher", Type: TypeMulti, Options: OVPNCiphers},
		{Name: "defaultProfile", ROS: "default-profile", Label: "Default Profile", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/ppp/profile", Value: "name"}},
		{Name: "userAuthMethod", ROS: "user-auth-method", Label: "User Auth Method", Type: TypeSelect,
			Options: []string{"pap", "mschap2"}},
		{Name: "tlsVersion", ROS: "tls-version", Label: "TLS Version", Type: TypeSelect, Options: []string{"any", "only-1.2"}},
		{Name: "redirectGateway", ROS: "redirect-gateway", Label: "Redirect Gateway", Type: TypeMulti,
			Options: []string{"disabled", "def1", "ipv6"}},
		{Name: "pushRoutes", ROS: "push-routes", Label: "Push Routes", Type: TypeText, Clearable: true,
			Placeholder: "192.168.88.0 255.255.255.0"},
		{Name: "netmask", ROS: "netmask", Label: "Netmask", Type: TypeInt, Min: intp(0), Max: intp(32)},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "inactive", ROS: "inactive", Label: "Inactive", Type: TypeBool, Display: true},
	},
}

var OVPNClient = &Resource{
	Key: "ovpnClient", Page: "openvpn", Label: "OpenVPN Client",
	Title: "OpenVPN Client", Menu: "/interface/ovpn-client", Identity: []string{"name"},
	Guard:                 []string{"selfPath", "tunnelDefault"},
	GuardInterfaceFields:  []string{"name"},
	GuardDisruptiveFields: []string{"connectTo", "port", "protocol", "user", "password", "certificate"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "ovpn-out1"},
		{Name: "connectTo", ROS: "connect-to", Label: "Connect To", Type: TypeText, Required: true,
			Placeholder: "vpn.example.com"},
		{Name: "port", ROS: "port", Label: "Port", Type: TypeInt, Min: intp(1), Max: intp(65535), Placeholder: "1194"},
		{Name: "protocol", ROS: "protocol", Label: "Protocol", Type: TypeSelect, Options: []string{"tcp", "udp"}},
		{Name: "mode", ROS: "mode", Label: "Mode", Type: TypeSelect, Options: []string{"ip", "ethernet"}},
		{Name: "user", ROS: "user", Label: "User", Type: TypeText, Clearable: true},
		{Name: "password", ROS: "password", Label: "Password", Type: TypeSecret},
		{Name: "certificate", ROS: "certificate", Label: "Certificate", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/certificate", Value: "name"}, Placeholder: "none"},
		{Name: "verifyServerCertificate", ROS: "verify-server-certificate", Label: "Verify Server Certificate",
			Type: TypeBool, Clearable: true},
		{Name: "auth", ROS: "auth", Label: "Auth", Type: TypeSelect, Options: OVPNAuths},
		{Name: "cipher", ROS: "cipher", Label: "Cipher", Type: TypeSelect, Options: OVPNCiphers},
		{Name: "tlsVersion", ROS: "tls-version", Label: "TLS Version", Type: TypeSelect, Options: []string{"any", "only-1.2"}},
		{Name: "profile", ROS: "profile", Label: "Profile", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/ppp/profile", Value: "name"}},
		{Name: "addDefaultRoute", ROS: "add-default-route", Label: "Add Default Route", Type: TypeBool, Clearable: true,
			Help: "Send all traffic through the tunnel."},
		{Name: "routeNopull", ROS: "route-nopull", Label: "Ignore Pushed Routes", Type: TypeBool, Clearable: true},
		{Name: "usePeerDns", ROS: "use-peer-dns", Label: "Use Peer DNS", Type: TypeSelect,
			Options: []string{"yes", "no", "exclusively"}},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "running", ROS: "running", Label: "Running", Type: TypeBool, Display: true},
	},
}

// VRRP is /interface/vrrp: a virtual router address shared between routers.
// Checked against rosetta and the CHR (7.24.3). A VRRP interface is an interface
// named after itself, so it carries selfPath: MikroDash may be managing the
// router through the virtual address, and disabling the interface, or changing
// what decides who holds it, moves that address. Its on-master, on-backup and
// on-fail are RouterOS code, behind codeGate as a scheduler's on-event is; the
// authentication password is a secret, never read.
var VRRP = &Resource{
	Key: "vrrp", Page: "vrrp", Label: "VRRP Interface",
	Title: "VRRP Interface", Menu: "/interface/vrrp", Identity: []string{"name"},
	Guard:                 []string{"selfPath", "codeGate"},
	GuardInterfaceFields:  []string{"name"},
	GuardDisruptiveFields: []string{"interface", "vrid", "priority", "version", "authentication", "password"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "vrrp1"},
		{Name: "interface", ROS: "interface", Label: "Interface", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/interface", Value: "name"}},
		{Name: "vrid", ROS: "vrid", Label: "VRID", Type: TypeInt, Min: intp(1), Max: intp(255)},
		{Name: "priority", ROS: "priority", Label: "Priority", Type: TypeInt, Min: intp(1), Max: intp(254),
			Help: "The highest priority holds the address."},
		{Name: "interval", ROS: "interval", Label: "Interval", Type: TypeText, Placeholder: "1s"},
		{Name: "version", ROS: "version", Label: "Version", Type: TypeSelect, Options: []string{"3", "2"}},
		{Name: "v3Protocol", ROS: "v3-protocol", Label: "v3 Protocol", Type: TypeSelect, Options: []string{"ipv4", "ipv6"},
			ShowIf: &ShowIf{Field: "version", In: []string{"3"}}},
		{Name: "preemptionMode", ROS: "preemption-mode", Label: "Preemption", Type: TypeBool, Clearable: true},
		{Name: "authentication", ROS: "authentication", Label: "Authentication", Type: TypeSelect,
			Options: []string{"none", "simple", "ah"}, ShowIf: &ShowIf{Field: "version", In: []string{"2"}}},
		{Name: "password", ROS: "password", Label: "Password", Type: TypeSecret,
			ShowIf: &ShowIf{Field: "authentication", In: []string{"simple", "ah"}}},
		{Name: "syncConnectionTracking", ROS: "sync-connection-tracking", Label: "Sync Connection Tracking",
			Type: TypeBool, Clearable: true},
		{Name: "onMaster", ROS: "on-master", Label: "On Master", Type: TypeCode, Code: true, Clearable: true},
		{Name: "onBackup", ROS: "on-backup", Label: "On Backup", Type: TypeCode, Code: true, Clearable: true},
		{Name: "onFail", ROS: "on-fail", Label: "On Fail", Type: TypeCode, Code: true, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "running", ROS: "running", Label: "Running", Type: TypeBool, Display: true},
		{Name: "invalid", ROS: "invalid", Label: "Invalid", Type: TypeBool, Display: true},
	},
}

// PPPoEClient is /interface/pppoe-client: the usual way a router dials its ISP.
// Checked against rosetta and the CHR (7.24.3). Named after itself, so it
// carries selfPath (a router managed over its PPPoE uplink is cut by disabling
// it or changing its login), and tunnelDefault: add-default-route installs
// 0.0.0.0/0 at default-route-distance through it. The password, which RouterOS
// returns on print, is a secret, never read.
var PPPoEClient = &Resource{
	Key: "pppoeClient", Page: "pppoe-clients", Label: "PPPoE Client",
	Title: "PPPoE Client", Menu: "/interface/pppoe-client", Identity: []string{"name"},
	Guard:                 []string{"selfPath", "tunnelDefault"},
	GuardInterfaceFields:  []string{"name"},
	GuardDisruptiveFields: []string{"interface", "user", "password", "serviceName", "acName", "allow"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "pppoe-out1"},
		{Name: "interface", ROS: "interface", Label: "Interface", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/interface", Value: "name"}},
		{Name: "user", ROS: "user", Label: "User", Type: TypeText, Clearable: true},
		{Name: "password", ROS: "password", Label: "Password", Type: TypeSecret},
		{Name: "serviceName", ROS: "service-name", Label: "Service Name", Type: TypeText, Clearable: true},
		{Name: "acName", ROS: "ac-name", Label: "AC Name", Type: TypeText, Clearable: true},
		{Name: "allow", ROS: "allow", Label: "Allow", Type: TypeMulti, Options: []string{"pap", "chap", "mschap1", "mschap2"}},
		{Name: "profile", ROS: "profile", Label: "Profile", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/ppp/profile", Value: "name"}},
		{Name: "addDefaultRoute", ROS: "add-default-route", Label: "Add Default Route", Type: TypeBool, Clearable: true},
		{Name: "defaultRouteDistance", ROS: "default-route-distance", Label: "Default Route Distance", Type: TypeInt,
			Min: intp(0), Max: intp(255), ShowIf: &ShowIf{Field: "addDefaultRoute", In: []string{"true", "yes"}}},
		{Name: "usePeerDns", ROS: "use-peer-dns", Label: "Use Peer DNS", Type: TypeBool, Clearable: true},
		{Name: "dialOnDemand", ROS: "dial-on-demand", Label: "Dial On Demand", Type: TypeBool, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "running", ROS: "running", Label: "Running", Type: TypeBool, Display: true},
		{Name: "invalid", ROS: "invalid", Label: "Invalid", Type: TypeBool, Display: true},
	},
}

// DHCPClient is /ip/dhcp-client: how a router takes its uplink address. Checked
// against rosetta and both test routers (7.24.3), whose uplinks are both DHCP
// clients. Guarded by dhcpClientPath (removing, disabling or moving the client
// that holds the address MikroDash dials) and tunnelDefault (the default route
// it installs, `yes` or `special-classless`). Its script is RouterOS code, behind
// codeGate. Renew and release stay on the WAN page, which has its own guard.
var DHCPClient = &Resource{
	Key: "dhcpClient", Page: "dhcp-clients", Label: "DHCP Client",
	Title: "DHCP Client", Menu: "/ip/dhcp-client", Identity: []string{"interface"},
	Guard:          []string{"dhcpClientPath", "tunnelDefault", "codeGate"},
	ReadOnlyWhen:   func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason: "dynamic",
	Fields: []Field{
		{Name: "interface", ROS: "interface", Label: "Interface", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/interface", Value: "name"}},
		{Name: "addDefaultRoute", ROS: "add-default-route", Label: "Add Default Route", Type: TypeSelect,
			Options: []string{"yes", "no", "special-classless"}},
		{Name: "defaultRouteDistance", ROS: "default-route-distance", Label: "Default Route Distance", Type: TypeInt,
			Min: intp(0), Max: intp(255), ShowIf: &ShowIf{Field: "addDefaultRoute", In: []string{"yes", "special-classless"}}},
		{Name: "usePeerDns", ROS: "use-peer-dns", Label: "Use Peer DNS", Type: TypeBool, Clearable: true},
		{Name: "usePeerNtp", ROS: "use-peer-ntp", Label: "Use Peer NTP", Type: TypeBool, Clearable: true},
		{Name: "checkGateway", ROS: "check-gateway", Label: "Check Gateway", Type: TypeSelect,
			Options: []string{"none", "arp", "ping", "bfd"}},
		{Name: "script", ROS: "script", Label: "Script", Type: TypeCode, Code: true, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "status", ROS: "status", Label: "Status", Type: TypeText, Display: true},
		{Name: "address", ROS: "address", Label: "Address", Type: TypeText, Display: true},
		{Name: "gateway", ROS: "gateway", Label: "Gateway", Type: TypeText, Display: true},
		{Name: "expiresAfter", ROS: "expires-after", Label: "Expires After", Type: TypeText, Display: true},
	},
}

// DHCPServer is /ip/dhcp-server and DHCPNetwork /ip/dhcp-server/network: the
// servers this router runs and what each hands out beyond an address. Checked
// against rosetta and the CHR (7.24.3). No lockout guard: a server going away
// leaves every client its lease until it expires. A server's lease-script is
// RouterOS code, behind codeGate. Properties at their default are not reported,
// so the ones that can be omitted declare their documented Default, and an
// unreported value opens as the default and a save writes back what the router
// already uses. The leases themselves are the DHCP page's.
var DHCPServer = &Resource{
	Key: "dhcpServer", Page: "dhcp-servers", Label: "DHCP Server",
	Title: "DHCP Server", Menu: "/ip/dhcp-server", Identity: []string{"name"},
	Guard:          []string{"codeGate"},
	ReadOnlyWhen:   func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason: "dynamic",
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "dhcp1"},
		{Name: "interface", ROS: "interface", Label: "Interface", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/interface", Value: "name"}},
		{Name: "addressPool", ROS: "address-pool", Label: "Address Pool", Type: TypeText,
			OptionsFrom: &OptionsFrom{Menu: "/ip/pool", Value: "name"},
			Help:        "A pool from IP Pools, or static-only to hand out only static leases."},
		{Name: "leaseTime", ROS: "lease-time", Label: "Lease Time", Type: TypeText, Placeholder: "30m"},
		{Name: "authoritative", ROS: "authoritative", Label: "Authoritative", Type: TypeSelect,
			Options: []string{"yes", "no", "after-2sec-delay", "after-10sec-delay"}, Default: "yes"},
		{Name: "addArp", ROS: "add-arp", Label: "Add ARP For Leases", Type: TypeBool, Clearable: true, Default: "no"},
		{Name: "conflictDetection", ROS: "conflict-detection", Label: "Conflict Detection", Type: TypeBool, Clearable: true,
			Default: "yes"},
		{Name: "leaseScript", ROS: "lease-script", Label: "Lease Script", Type: TypeCode, Code: true, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "invalid", ROS: "invalid", Label: "Invalid", Type: TypeBool, Display: true},
	},
}

var DHCPNetwork = &Resource{
	Key: "dhcpNetwork", Page: "dhcp-servers", Label: "DHCP Network",
	Title: "DHCP Network", Menu: "/ip/dhcp-server/network", Identity: []string{"address"},
	ReadOnlyWhen:   func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason: "dynamic",
	Fields: []Field{
		{Name: "address", ROS: "address", Label: "Network", Type: TypeCidr, Required: true, Placeholder: "192.168.88.0/24"},
		{Name: "gateway", ROS: "gateway", Label: "Gateway", Type: TypeText, Clearable: true, Placeholder: "192.168.88.1"},
		{Name: "dnsServer", ROS: "dns-server", Label: "DNS Servers", Type: TypeText, Clearable: true,
			Placeholder: "192.168.88.1", Help: "Comma separated."},
		{Name: "domain", ROS: "domain", Label: "Domain", Type: TypeText, Clearable: true},
		{Name: "ntpServer", ROS: "ntp-server", Label: "NTP Servers", Type: TypeText, Clearable: true},
		{Name: "netmask", ROS: "netmask", Label: "Netmask", Type: TypeInt, Clearable: true, Min: intp(0), Max: intp(32),
			Help: "Empty takes the network's own."},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
	},
}

// ── Containers ──────────────────────────────────────────────────────────────
//
// /container and its envs, mounts, config and veth interfaces, as a generated
// page (the operator's choice over a hand-built one). Checked against rosetta
// and the CHR with the container package and device-mode on (7.24.3).
//
// WHAT A CONTAINER RUNS IS CODE. Its image, command and entrypoint decide what
// executes on the router, so they are Code fields behind codeGate, as a
// script's source is; starting and stopping are row actions, and the assistant
// reaches them through run_action. An env VALUE is a secret: containers take
// their passwords and tokens from env, so a value is written and never read. The
// registry password is a secret too. `config-json` (the image's own config, often
// kilobytes) is not a field, so the area's proplist never asks for it.

var Container = &Resource{
	Key: "container", Page: "containers", Label: "Container",
	Title: "Container", Menu: "/container", Identity: []string{"name"},
	Guard: []string{"codeGate"},
	Actions: []Action{
		{Key: "start", Verb: "start", Label: "Start", Note: "started a container",
			When: func(r map[string]string) bool { return r["running"] != "true" }},
		{Key: "stop", Verb: "stop", Label: "Stop", Note: "stopped a container",
			When: func(r map[string]string) bool { return r["running"] == "true" }},
	},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "pihole"},
		{Name: "remoteImage", ROS: "remote-image", Label: "Image", Type: TypeText, Code: true,
			Placeholder: "library/alpine:latest"},
		{Name: "interface", ROS: "interface", Label: "Interface", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/interface/veth", Value: "name"}},
		{Name: "rootDir", ROS: "root-dir", Label: "Root Directory", Type: TypeText, Placeholder: "disk1/pihole"},
		{Name: "cmd", ROS: "cmd", Label: "Command", Type: TypeText, Code: true, Clearable: true},
		{Name: "entrypoint", ROS: "entrypoint", Label: "Entrypoint", Type: TypeText, Code: true, Clearable: true},
		{Name: "envlists", ROS: "envlists", Label: "Env Lists", Type: TypeText, Clearable: true,
			OptionsFrom: &OptionsFrom{Menu: "/container/envs", Value: "list"}},
		{Name: "mountlists", ROS: "mountlists", Label: "Mount Lists", Type: TypeText, Clearable: true,
			OptionsFrom: &OptionsFrom{Menu: "/container/mounts", Value: "list"}},
		{Name: "hostname", ROS: "hostname", Label: "Hostname", Type: TypeText, Clearable: true},
		{Name: "dns", ROS: "dns", Label: "DNS", Type: TypeText, Clearable: true},
		{Name: "startOnBoot", ROS: "start-on-boot", Label: "Start On Boot", Type: TypeBool, Clearable: true},
		{Name: "restartPolicy", ROS: "restart-policy", Label: "Restart Policy", Type: TypeSelect,
			Options: []string{"no", "always", "on-failure"}},
		{Name: "logging", ROS: "logging", Label: "Logging", Type: TypeBool, Clearable: true},
		{Name: "memoryHigh", ROS: "memory-high", Label: "Memory High", Type: TypeText, Placeholder: "unlimited"},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		// RouterOS drops `running` from a stopped container (it reports
		// `stopped` instead), so the column reads it as false. A container that
		// is still starting reports neither; the next read shows it running.
		{Name: "running", ROS: "running", Label: "Running", Type: TypeBool, Display: true, Default: "false"},
		{Name: "tag", ROS: "tag", Label: "Tag", Type: TypeText, Display: true},
		{Name: "restartCount", ROS: "restart-count", Label: "Restarts", Type: TypeText, Display: true},
	},
}

var ContainerEnv = &Resource{
	Key: "containerEnv", Page: "containers", Label: "Container Env",
	Title: "Container Env", Menu: "/container/envs", Identity: []string{"list", "key"},
	Fields: []Field{
		{Name: "list", ROS: "list", Label: "List", Type: TypeText, Required: true, Placeholder: "pihole-env"},
		{Name: "key", ROS: "key", Label: "Key", Type: TypeText, Required: true, Placeholder: "TZ"},
		{Name: "value", ROS: "value", Label: "Value", Type: TypeSecret,
			Help: "Never shown once saved: env values commonly carry passwords. Leave blank to keep it."},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

var ContainerMount = &Resource{
	Key: "containerMount", Page: "containers", Label: "Container Mount",
	Title: "Container Mount", Menu: "/container/mounts", Identity: []string{"list", "dst"},
	Fields: []Field{
		{Name: "list", ROS: "list", Label: "List", Type: TypeText, Required: true, Placeholder: "pihole-mounts"},
		{Name: "src", ROS: "src", Label: "Source", Type: TypeText, Required: true, Placeholder: "disk1/pihole-data"},
		{Name: "dst", ROS: "dst", Label: "Destination", Type: TypeText, Required: true, Placeholder: "/etc/pihole"},
		{Name: "mode", ROS: "mode", Label: "Mode", Type: TypeSelect, Options: []string{"rw", "ro", "rw,noexec", "ro,noexec"}},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

var ContainerConfig = &Resource{
	Key: "containerConfig", Page: "containers", Label: "Container Settings",
	Title: "Container Settings", Menu: "/container/config", Singleton: true,
	NoCreate:      true,
	RemovableWhen: func(map[string]string) bool { return false },
	Fields: []Field{
		{Name: "registryUrl", ROS: "registry-url", Label: "Registry", Type: TypeText, Clearable: true,
			Placeholder: "https://registry-1.docker.io"},
		{Name: "username", ROS: "username", Label: "Registry User", Type: TypeText, Clearable: true},
		{Name: "password", ROS: "password", Label: "Registry Password", Type: TypeSecret},
		{Name: "tmpdir", ROS: "tmpdir", Label: "Pull Directory", Type: TypeText, Clearable: true, Placeholder: "disk1/pull"},
		{Name: "layerDir", ROS: "layer-dir", Label: "Layer Directory", Type: TypeText, Clearable: true},
		{Name: "memoryHigh", ROS: "memory-high", Label: "Memory High", Type: TypeText, Placeholder: "unlimited"},
		{Name: "memoryCurrent", ROS: "memory-current", Label: "Memory In Use", Type: TypeText, Display: true},
	},
}

// Veth is /interface/veth: the interface a container talks through. Named after
// itself, so it carries selfPath.
var Veth = &Resource{
	Key: "veth", Page: "containers", Label: "VETH",
	Title: "VETH Interface", Menu: "/interface/veth", Identity: []string{"name"},
	Guard:                []string{"selfPath"},
	GuardInterfaceFields: []string{"name"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "veth1"},
		{Name: "address", ROS: "address", Label: "Address", Type: TypeText, Clearable: true,
			Placeholder: "172.17.0.2/24", Help: "Comma separated."},
		{Name: "gateway", ROS: "gateway", Label: "Gateway", Type: TypeText, Clearable: true, Placeholder: "172.17.0.1"},
		{Name: "gateway6", ROS: "gateway6", Label: "IPv6 Gateway", Type: TypeText, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		{Name: "running", ROS: "running", Label: "Running", Type: TypeBool, Display: true},
	},
}

// ── Router users ────────────────────────────────────────────────────────────
//
// /user and /user/group. Both carry the selfAccount guard, which REFUSES any
// write that could break the login MikroDash signs in with (see
// guard/selfguard.go): unlike every other guard here, that one is unrecoverable
// from inside the app. Ending an active session is not a row write and stays a
// page action (rossession:remove).

// UserPolicies is the RouterOS policy vocabulary, in the order WinBox shows it,
// as documented for /user/group on RouterOS 7. The group form renders exactly
// this list, and a write names every one of them (NegateUnset), so a policy
// missing here is one an edit would silently leave as it was.
var UserPolicies = []string{
	"local", "telnet", "ssh", "ftp", "reboot", "read", "write", "policy", "test",
	"winbox", "password", "web", "sniff", "sensitive", "api", "romon", "rest-api",
}

var RosUser = &Resource{
	Key: "rosUser", Page: "users", Label: "Router User",
	Title: "Router User", Menu: "/user", Identity: []string{"name"},
	Guard: []string{"selfAccount"},
	Actions: []Action{
		{Key: "enable", Verb: "enable", Label: "Enable",
			When: func(r map[string]string) bool { return r["disabled"] == "true" },
			Note: "enabled a RouterOS user"},
		{Key: "disable", Verb: "disable", Label: "Disable",
			When: func(r map[string]string) bool { return r["disabled"] != "true" },
			Note: "disabled a RouterOS user"},
	},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "username"},
		{Name: "group", ROS: "group", Label: "Group", Type: TypeText, Required: true,
			OptionsFrom: &OptionsFrom{Menu: "/user/group", Value: "name"}},
		{Name: "password", ROS: "password", Label: "Password", Type: TypeSecret,
			Help: "Leave blank to keep the current password. The router enforces its own minimum length."},
		{Name: "address", ROS: "address", Label: "Allowed Address", Type: TypeText, Clearable: true,
			Placeholder: "10.0.0.0/24", Help: "Only log in from these addresses. Empty allows any."},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

var RosGroup = &Resource{
	Key: "rosGroup", Page: "users", Label: "User Group",
	Title: "User Group", Menu: "/user/group", Identity: []string{"name"},
	Guard: []string{"selfAccount"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "group-name"},
		{Name: "policy", ROS: "policy", Label: "Permissions", Type: TypeMulti, Options: UserPolicies,
			NegateUnset: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
	},
}

// ── Queues ──────────────────────────────────────────────────────────────────
//
// Two menus with different row shapes, measured on RouterOS 7.24 (CHR): a simple
// queue caps a TARGET in both directions, so its limits and priority are
// `upload/download` pairs ("15000000/20000000", "8/8"), and it is ORDERED, first
// match wins; a queue tree shapes marked traffic under a PARENT in one direction,
// with single values and no order. Only simple queues can be dynamic (Kid
// Control, DHCP rate limits, PPP profiles create them), and only they can be
// aimed at an address, so only they carry the self-throttle guard.
//
// Limits are typed as RouterOS accepts them, with suffixes ("15M/20M"), and read
// back as raw bits per second, which is how the edit form shows them.

var queueActions = []Action{
	{Key: "enable", Verb: "enable", Label: "Enable",
		When: func(r map[string]string) bool { return r["disabled"] == "true" },
		Note: "enabled a queue"},
	{Key: "disable", Verb: "disable", Label: "Disable",
		When: func(r map[string]string) bool { return r["disabled"] != "true" },
		Note: "disabled a queue"},
	{Key: "reset", Verb: "reset-counters", Label: "Reset Counters",
		Note: "zeroed the queue statistics"},
}

var queuePriorityPair = regexp.MustCompile(`^[1-8](/[1-8])?$`)

// queueCheck validates the rates and the priority, and that the guaranteed rate
// is not above the cap: RouterOS refuses that with "download-max-limit less than
// download-limit", which names neither field the operator typed.
func queueCheck(pair bool) func(clean map[string]string) []Error {
	return func(clean map[string]string) []Error {
		var errs []Error
		parse := func(field string) guard.Pair {
			raw := strings.TrimSpace(clean[field])
			if raw == "" {
				return guard.Pair{}
			}
			halves := strings.Split(raw, "/")
			if (pair && len(halves) > 2) || (!pair && len(halves) != 1) {
				errs = append(errs, Error{Field: field, Message: queueRateHelp(pair)})
				return guard.Pair{}
			}
			for _, h := range halves {
				if !guard.ParseRate(h).Set {
					errs = append(errs, Error{Field: field, Message: queueRateHelp(pair)})
					return guard.Pair{}
				}
			}
			return guard.ParsePair(raw)
		}
		maxLimit, limitAt := parse("maxLimit"), parse("limitAt")
		above := func(mx, lo guard.Rate) bool { return mx.Set && mx.Bps > 0 && lo.Set && lo.Bps > mx.Bps }
		if len(errs) == 0 && (above(maxLimit.Up, limitAt.Up) || above(maxLimit.Down, limitAt.Down)) {
			errs = append(errs, Error{Field: "limitAt",
				Message: "Limit At cannot be above Max Limit: the router refuses a guaranteed rate larger than the cap"})
		}
		if p := strings.TrimSpace(clean["priority"]); pair && p != "" && !queuePriorityPair.MatchString(p) {
			errs = append(errs, Error{Field: "priority", Message: "Priority is 1 (highest) to 8, or a pair such as 8/8"})
		}
		return errs
	}
}

func queueRateHelp(pair bool) string {
	if pair {
		return "A rate in bits per second, or upload/download such as 15M/20M (k, M and G are accepted)"
	}
	return "One rate in bits per second, such as 10M (k, M and G are accepted)"
}

var SimpleQueue = &Resource{
	Key: "simpleQueue", Page: "queues", Label: "Simple Queue",
	Title: "Simple Queue", Menu: "/queue/simple", Identity: []string{"name"},
	Ordered:        true,
	ReadOnlyWhen:   func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason: "read-only-row",
	Guard:          []string{"queueThrottle"},
	Actions:        queueActions,
	Check:          queueCheck(true),
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "queue name"},
		{Name: "target", ROS: "target", Label: "Target", Type: TypeText, Required: true,
			Placeholder: "10.0.0.5/32 or an interface"},
		{Name: "maxLimit", ROS: "max-limit", Label: "Max Limit", Type: TypeText, Clearable: true,
			Placeholder: "15M/20M", Help: "Upload/download cap. 0 is unlimited."},
		{Name: "limitAt", ROS: "limit-at", Label: "Limit At", Type: TypeText, Clearable: true,
			Placeholder: "5M/5M", Help: "Guaranteed upload/download rate."},
		{Name: "priority", ROS: "priority", Label: "Priority", Type: TypeText, Clearable: true,
			Placeholder: "8/8", Help: "1 is the highest, 8 the lowest."},
		{Name: "packetMarks", ROS: "packet-marks", Label: "Packet Marks", Type: TypeText, Clearable: true},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

var QueueTree = &Resource{
	Key: "queueTree", Page: "queues", Label: "Queue Tree",
	Title: "Queue Tree", Menu: "/queue/tree", Identity: []string{"name"},
	Actions: queueActions,
	Check:   queueCheck(false),
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true, Placeholder: "queue name"},
		{Name: "parent", ROS: "parent", Label: "Parent", Type: TypeText, Required: true,
			Placeholder: "global", Help: "global, an interface, or another tree queue."},
		{Name: "packetMark", ROS: "packet-mark", Label: "Packet Mark", Type: TypeText, Clearable: true},
		{Name: "maxLimit", ROS: "max-limit", Label: "Max Limit", Type: TypeText, Clearable: true,
			Placeholder: "10M", Help: "0 is unlimited."},
		{Name: "limitAt", ROS: "limit-at", Label: "Limit At", Type: TypeText, Clearable: true, Placeholder: "5M"},
		{Name: "priority", ROS: "priority", Label: "Priority", Type: TypeInt, Min: intp(1), Max: intp(8), Clearable: true,
			Placeholder: "8"},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

var PPPProfile = &Resource{
	Key: "pppProfile", Page: "ppp", Label: "PPP Profile",
	Title: "PPP Profile", Menu: "/ppp/profile", Identity: []string{"name"},
	RemovableWhen: func(r map[string]string) bool {
		return r["name"] != "default" && r["name"] != "default-encryption"
	},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Required: true,
			Placeholder: "for-pppoe"},
		// An IP or a POOL NAME, per the RouterOS reference — "single IP addresses
		// always take precedence over IP pools" — so free text, not TypeIP.
		{Name: "localAddress", ROS: "local-address", Label: "Local Address",
			Type: TypeText, Clearable: true, Help: "an IP address, or the name of an IP pool"},
		{Name: "remoteAddress", ROS: "remote-address", Label: "Remote Address",
			Type: TypeText, Clearable: true, Help: "an IP address, or the name of an IP pool"},
		{Name: "rateLimit", ROS: "rate-limit", Label: "Rate Limit", Type: TypeText,
			Clearable: true, Placeholder: "10M/10M"},
		// yes | no | default — "default" is not a synonym for no, it means
		// "inherit", so it has to be offered as its own value.
		{Name: "onlyOne", ROS: "only-one", Label: "Only One Session", Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: []string{"default", "yes", "no"}}},
		// `require`, NOT `required`. Checked against the PPP AAA property table
		// rather than typed from memory, which is how the first draft of this
		// line got it wrong — and an unlisted value in a picker is the defect
		// that rewrote MX records as A records on the DNS page.
		{Name: "useEncryption", ROS: "use-encryption", Label: "Use Encryption",
			Type: TypeText, OptionsFrom: &OptionsFrom{Values: []string{
				"default", "yes", "no", "require"}}},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
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

// IPAddress and IPv6Address are the rows of the IP Addresses page (#97), one per
// family, the way Routes has route and route6. New in this port.
//
// ── DYNAMIC ROWS ARE READ-ONLY ─────────────────────────────────────────────
//
// A dynamic address belongs to whatever made it: a DHCP client, a PPP or VPN
// session, IPv6 link-local autoconfiguration. An edit here would be undone by its
// owner, or would fight it.
//
// ── GUARDED BY addressPath ─────────────────────────────────────────────────
//
// Changing, disabling or removing the address on the subnet MikroDash reaches
// the router from is the lockout #97 names for Phase 3. The guard warns and asks
// for an acknowledgement, and warns when it cannot tell where MikroDash is.
//
// ── ONLY WHAT IS DOCUMENTED ────────────────────────────────────────────────
//
// `network` and `broadcast` are derived by RouterOS from the address and are not
// offered. On IPv6, `from-pool`, `from-pool-policy`, `no-dad` and
// `auto-link-local` are not documented beyond their names, so a save never sends
// them and a row keeps its own.
var IPAddress = &Resource{
	Key: "ipAddress", Page: "ip-addresses", Label: "IPv4 Address",
	Title: "IPv4 Address", Menu: "/ip/address", Identity: []string{"address"},
	ReadOnlyWhen:   func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason: "read-only-row",
	Guard:          []string{"addressPath"},
	Fields: []Field{
		{Name: "address", ROS: "address", Label: "Address", Type: TypeCidr, Required: true, Placeholder: "192.168.88.1/24"},
		{Name: "interface", ROS: "interface", Label: "Interface", Type: TypeText, Required: true,
			Placeholder: "bridge", OptionsFrom: &OptionsFrom{Menu: "/interface", Value: "name"}},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		// What the router derives, shown and never sent. The IP Addresses page has
		// always shown them: the network an address sits in, whether something else
		// (a DHCP client, a VPN) owns the row, and whether its interface is gone.
		{Name: "network", ROS: "network", Label: "Network", Type: TypeText, Display: true},
		{Name: "dynamic", ROS: "dynamic", Label: "Dynamic", Type: TypeBool, Display: true},
		{Name: "invalid", ROS: "invalid", Label: "Invalid", Type: TypeBool, Display: true},
	},
	Check: addressFamily("ipv4"),
}

// IPv6Address is the IPv6 half. See IPAddress.
var IPv6Address = &Resource{
	Key: "ipv6Address", Page: "ip-addresses", Label: "IPv6 Address",
	Title: "IPv6 Address", Menu: "/ipv6/address", Identity: []string{"address"},
	ReadOnlyWhen:   func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason: "read-only-row",
	Guard:          []string{"addressPath"},
	Fields: []Field{
		{Name: "address", ROS: "address", Label: "Address", Type: TypeCidr, Required: true, Placeholder: "2001:db8::1/64"},
		{Name: "interface", ROS: "interface", Label: "Interface", Type: TypeText, Required: true,
			Placeholder: "bridge", OptionsFrom: &OptionsFrom{Menu: "/interface", Value: "name"}},
		{Name: "advertise", ROS: "advertise", Label: "Advertise", Type: TypeBool, Clearable: true,
			Help: "Advertise this prefix to hosts on the interface."},
		{Name: "eui64", ROS: "eui-64", Label: "EUI-64", Type: TypeBool, Clearable: true,
			Help: "Generate the last 64 bits from the interface identifier. Leave them zero in the address."},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
		// Shown and never sent; see IPAddress.
		{Name: "dynamic", ROS: "dynamic", Label: "Dynamic", Type: TypeBool, Display: true},
		{Name: "invalid", ROS: "invalid", Label: "Invalid", Type: TypeBool, Display: true},
	},
	Check: addressFamily("ipv6"),
}

// addressFamily refuses an address of the other family, so an IPv6 address is
// never sent to /ip/address or an IPv4 one to /ipv6/address. TypeCidr accepts
// both, because a route destination is legitimately either.
func addressFamily(want string) func(map[string]string) []Error {
	return func(v map[string]string) []Error {
		isV6 := strings.Contains(v["address"], ":")
		switch {
		case want == "ipv4" && isV6:
			return []Error{{"address", "Address is an IPv6 address; add it as IPv6"}}
		case want == "ipv6" && !isV6 && v["address"] != "":
			return []Error{{"address", "Address is an IPv4 address; add it as IPv4"}}
		}
		return nil
	}
}

// Netwatch is a host on the NetWatch page (#97). New in this port.
//
// ── NO SCRIPTS ──────────────────────────────────────────────────────────────
//
// `up-script`, `down-script` and `test-script` run as RouterOS's system user
// with read, write, test and reboot policy. Offering them would let anybody with
// NetWatch write on this page put code on the router, so they are not fields: a
// save never sends them, and a row keeps whatever scripts it already has.
//
// ── NOR A DNS PROBE'S RECORD TYPE ───────────────────────────────────────────
//
// RouterOS documents no list of `record-type` values, and a select that misses
// one rewrites the probe on save, which is the dnsStatic lesson in CLAUDE.md. A
// DNS probe keeps its `dns-server` and `record-type`; the rest is editable.
//
// No guard: a probe watches a host, it is not a path to the router.
var Netwatch = &Resource{
	Key: "netwatch", Page: "netwatch", Label: "NetWatch Host",
	Title: "NetWatch Host", Menu: "/tool/netwatch", Identity: []string{"host"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Clearable: true, Placeholder: "isp-gateway"},
		{Name: "host", ROS: "host", Label: "Host", Type: TypeText, Required: true, Placeholder: "8.8.8.8",
			Help: "The address to probe. For a DNS probe, the name to resolve."},
		{Name: "type", ROS: "type", Label: "Probe", Type: TypeSelect, Required: true,
			Options: []string{"simple", "icmp", "tcp-conn", "http-get", "https-get", "dns"}},
		{Name: "port", ROS: "port", Label: "Port", Type: TypeInt, Min: intp(1), Max: intp(65535),
			ShowIf: &ShowIf{Field: "type", In: []string{"tcp-conn", "http-get", "https-get"}}},
		{Name: "interval", ROS: "interval", Label: "Interval", Type: TypeText, Placeholder: "10s",
			Help: "How often to probe, in RouterOS time: 30s, 5m, 1h."},
		{Name: "timeout", ROS: "timeout", Label: "Timeout", Type: TypeText, Placeholder: "1s"},
		{Name: "comment", ROS: "comment", Label: "Comment", Type: TypeText, Clearable: true},
		{Name: "disabled", ROS: "disabled", Label: "Disabled", Type: TypeBool, Clearable: true},
	},
}

// Iface is any interface on the Interfaces page: its comment, and whether it is
// enabled (#97). New in this port; the Node app had no equivalent.
//
// ── THE NAME IS SHOWN AND NEVER SENT ────────────────────────────────────────
//
// It identifies the row, so it has to be a field, but renaming an interface
// from here would orphan everything this app keys on the name. See Display.
//
// ── NOT CREATED, NOT REMOVED ────────────────────────────────────────────────
//
// `/interface` offers no `add`, and removing a VLAN or a bridge belongs to its
// own page's resource. A DYNAMIC interface (a PPPoE or L2TP session) belongs to
// whatever created it and is read-only.
//
// ── GUARDED BY selfPath ─────────────────────────────────────────────────────
//
// Disabling the interface MikroDash reaches the router over is the Phase 3
// lockout #97 names. A comment-only edit is not a guard target.
var Iface = &Resource{
	Key: "iface", Page: "interfaces", Label: "Interface",
	Title: "Interface", Menu: "/interface", Identity: []string{"name"},
	NoCreate:             true,
	ReadOnlyWhen:         func(r map[string]string) bool { return r["dynamic"] == "true" },
	ReadOnlyReason:       "read-only-row",
	RemovableWhen:        func(map[string]string) bool { return false },
	Guard:                []string{"selfPath"},
	GuardInterfaceFields: []string{"name"},
	Fields: []Field{
		{Name: "name", ROS: "name", Label: "Name", Type: TypeText, Display: true},
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
// GUARDED BY routePath (#97). A route change can cut the management path, and
// `selfPath` answers "which INTERFACE carries us", which a route is not. So a
// narrower guard answers only what it can prove: does the route, before or after
// the change, cover the address the router sees us from, when that address is not
// on a connected subnet. See internal/guard/routeguard.go.
var Route = &Resource{
	Key: "route", Page: "routing", Label: "Route",
	Title: "IPv4 Route", Menu: "/ip/route", Identity: []string{"dstAddress"},
	Guard: []string{"routePath"},
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
	Guard: []string{"routePath"},
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
	// RunsCode marks an action that executes RouterOS code, such as running a
	// script: held to the same gate as a Code field (see Field.Code).
	RunsCode bool
}

// CodeChange reports whether a write CHANGES a Code field. `values` are keyed by
// field name, as a write carries them; `before` is the stored row by RouterOS
// name, or nil on a create, where any non-empty code counts. A write that leaves
// the code as it is — renaming a script, changing a comment — is not a code
// change, and is not held to the gate.
func (r *Resource) CodeChange(values, before map[string]string) bool {
	for _, f := range r.Fields {
		if !f.Code {
			continue
		}
		v, ok := values[f.Name]
		if !ok {
			continue
		}
		if before == nil {
			if strings.TrimSpace(v) != "" {
				return true
			}
			continue
		}
		if f.Type == TypeBool || f.Type == TypeFlag {
			// ONE WORD EACH SIDE: a validated checkbox is "yes"/"no" and the
			// router stores "true"/"false", so a plain compare called every
			// resend a change and refused a non-admin's rename.
			if truthyROS(v) != truthyROS(before[f.ROS]) {
				return true
			}
			continue
		}
		if v != before[f.ROS] {
			return true
		}
	}
	return false
}

// truthyROS is a checkbox's value in either spelling: "yes" from a form or a
// write, "true" from a router's print.
func truthyROS(v string) bool { return v == "yes" || v == "true" }

// RunsCode reports whether the named row action executes code.
func (r *Resource) RunsCode(action string) bool {
	for _, a := range r.Actions {
		if a.Key == action {
			return a.RunsCode
		}
	}
	return false
}

var byKey = map[string]*Resource{
	DHCPLease.Key:           DHCPLease,
	WgPeer.Key:              WgPeer,
	Route.Key:               Route,
	Route6.Key:              Route6,
	DNSStatic.Key:           DNSStatic,
	PPPSecret.Key:           PPPSecret,
	PPPProfile.Key:          PPPProfile,
	SimpleQueue.Key:         SimpleQueue,
	QueueTree.Key:           QueueTree,
	IPPool.Key:              IPPool,
	RoutingTable.Key:        RoutingTable,
	RoutingRule.Key:         RoutingRule,
	OSPFInstance.Key:        OSPFInstance,
	OSPFArea.Key:            OSPFArea,
	OSPFTemplate.Key:        OSPFTemplate,
	OSPFNeighbor.Key:        OSPFNeighbor,
	IPsecPeer.Key:           IPsecPeer,
	IPsecIdentity.Key:       IPsecIdentity,
	IPsecPolicy.Key:         IPsecPolicy,
	OVPNServer.Key:          OVPNServer,
	OVPNClient.Key:          OVPNClient,
	VRRP.Key:                VRRP,
	PPPoEClient.Key:         PPPoEClient,
	DHCPClient.Key:          DHCPClient,
	DHCPServer.Key:          DHCPServer,
	DHCPNetwork.Key:         DHCPNetwork,
	Container.Key:           Container,
	ContainerEnv.Key:        ContainerEnv,
	ContainerMount.Key:      ContainerMount,
	ContainerConfig.Key:     ContainerConfig,
	Veth.Key:                Veth,
	AddressList.Key:         AddressList,
	IfList.Key:              IfList,
	IfListMember.Key:        IfListMember,
	IPService.Key:           IPService,
	Certificate.Key:         Certificate,
	Script.Key:              Script,
	Scheduler.Key:           Scheduler,
	NTPClient.Key:           NTPClient,
	NTPServer.Key:           NTPServer,
	Clock.Key:               Clock,
	LogRule.Key:             LogRule,
	LogAction.Key:           LogAction,
	SNMP.Key:                SNMP,
	SNMPCommunity.Key:       SNMPCommunity,
	File.Key:                File,
	RosUser.Key:             RosUser,
	RosGroup.Key:            RosGroup,
	Bridge.Key:              Bridge,
	BridgePort.Key:          BridgePort,
	Vlan.Key:                Vlan,
	Iface.Key:               Iface,
	Netwatch.Key:            Netwatch,
	IPAddress.Key:           IPAddress,
	IPv6Address.Key:         IPv6Address,
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
	FWFilter6.Key:           FWFilter6,
	FWNat6.Key:              FWNat6,
	FWMangle6.Key:           FWMangle6,
	FWRaw6.Key:              FWRaw6,
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
		if !ok && f.Default != "" {
			raw, ok = f.Default, true
		}
		// A PRESENCE FLAG'S ABSENCE IS ITS VALUE: no key is false.
		if f.Type == TypeFlag {
			out[f.Name] = ok
			continue
		}
		if !ok {
			continue
		}
		if f.Type == TypeBool {
			out[f.Name] = raw == "true" || raw == "yes"
		} else if f.Type == TypeMulti {
			// The router lists every option with the denied ones negated; the
			// form's value is what is granted.
			granted := []string{}
			for _, part := range strings.Split(raw, ",") {
				if part = strings.TrimSpace(part); part != "" && !strings.HasPrefix(part, "!") {
					granted = append(granted, part)
				}
			}
			out[f.Name] = strings.Join(granted, ",")
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

// fwProtocols is the IPv4 vocabulary; fwProtocols6 the IPv6 one.
//
// THE TWO DIFFER BY ONE VALUE AND IT IS NOT COSMETIC. `/ip/firewall` names
// ICMPv6 `ipv6-icmp`; `/ipv6/firewall` REFUSES that spelling and calls it
// `icmpv6` — "input does not match any value of protocol", measured on 7.24.1.
// Reusing one list meant the IPv6 form offered a value its own menu rejects, so
// picking ICMPv6 there failed at the router. Caught by driving the page, not by
// any test: the shared helper compiled perfectly.
var fwProtocols = []string{"tcp", "udp", "icmp", "ipv6-icmp", "gre", "ipsec-esp", "ipsec-ah"}
var fwProtocols6 = []string{"tcp", "udp", "icmpv6", "gre", "ipsec-esp", "ipsec-ah"}

func fwMatch(protocols []string) []Field {
	return []Field{
		{Name: "srcAddress", ROS: "src-address", Label: "Source Address", Type: TypeText,
			Placeholder: "10.0.0.0/24"},
		{Name: "dstAddress", ROS: "dst-address", Label: "Destination Address", Type: TypeText},
		{Name: "protocol", ROS: "protocol", Label: "Protocol", Type: TypeText,
			OptionsFrom: &OptionsFrom{Values: protocols}},
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
		fwMatch(fwProtocols),
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
		fwMatch(fwProtocols),
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
		fwMatch(fwProtocols),
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
		fwMatch(fwProtocols),
		fwTail(),
	),
}

// ── The IPv6 firewall ───────────────────────────────────────────────────────
//
// Four more resources rather than a `family` flag on the four above, for the
// same reason `Route6` is its own resource beside `Route`: the RouterOS menu is
// what an edit actually reaches, and a resource IS a menu. The page picks one
// per row, exactly as the Routes table already mixes v4 and v6.
//
// `Page: "firewall"` on all four — no new page key, so no `pages.Renamed` entry
// and no `role_pages` migration. Whoever can edit the IPv4 firewall can edit
// this one, which is the right answer: they are one firewall.
//
// ── THE VOCABULARIES WERE READ OFF A ROUTER, NOT WRITTEN FROM MEMORY ────────
//
// Measured on RouterOS 7.24.1 by offering each candidate to a throwaway CHR and
// recording what it accepted. Guessing would have been wrong four times, and
// three of those are not guessable from the IPv4 side:
//
//   - IPv6 filter has NO `tarpit`.
//   - IPv6 mangle has NO `route`, NO `change-ttl` (it is `change-hop-limit`),
//     NO `clear-df`, NO `strip-ipv4-options` and NO `fasttrack-connection`.
//   - IPv6 NAT has NO `same`, and its translation target is `to-address`,
//     SINGULAR, where IPv4 uses `to-addresses`. A plural here is an "unknown
//     parameter" trap on every save.
//   - `reject-with` shares only three values with the IPv4 list; IPv6 answers
//     `icmp-no-route` and `icmp-address-unreachable`, which IPv4 does not have,
//     and lacks the four `icmp-*-unreachable`/`*-prohibited` spellings IPv4 uses.
//
// What is NOT validation: CHAIN. RouterOS accepts any chain name on any of these
// menus, because chains are user-definable and `jump` targets one. The lists
// here are conventions, and `selectHtml` keeps a router value the list does not
// name — see web/src/resource.ts, which is why a narrow list cannot silently
// rewrite a rule the way the live app's could.

var FWFilter6 = &Resource{
	Key: "fwFilter6", Page: "firewall", Label: "IPv6 Filter Rule",
	Title: "IPv6 Firewall Filter Rule", Menu: "/ipv6/firewall/filter",
	Identity: fwIdentity, Ordered: true, Guard: []string{"fwGuard"},
	ReadOnlyWhen: fwReadOnly, Actions: fwActions, Check: fwCheck,
	Fields: fwFields(
		// No `tarpit`: the IPv4 filter takes it, this one does not.
		fwHead([]string{"input", "forward", "output"},
			[]string{"accept", "drop", "reject", "log", "passthrough",
				"fasttrack-connection", "jump", "return",
				"add-src-to-address-list", "add-dst-to-address-list"}),
		fwMatch(fwProtocols6),
		[]Field{
			{Name: "connectionState", ROS: "connection-state", Label: "Connection State",
				Type: TypeText, Placeholder: "established,related"},
			// SHARES ONLY THREE VALUES WITH IPv4. `icmp-no-route` and
			// `icmp-address-unreachable` do not exist there, and IPv4's
			// `icmp-network-unreachable` / `icmp-host-unreachable` do not exist
			// here.
			{Name: "rejectWith", ROS: "reject-with", Label: "Reject With", Type: TypeText,
				ShowIf: &ShowIf{Field: "action", In: []string{"reject"}},
				OptionsFrom: &OptionsFrom{Values: []string{
					"icmp-no-route", "icmp-address-unreachable",
					"icmp-admin-prohibited", "icmp-port-unreachable", "tcp-reset"}}},
		},
		fwTail(),
	),
}

var FWNat6 = &Resource{
	Key: "fwNat6", Page: "firewall", Label: "IPv6 NAT Rule",
	Title: "IPv6 Firewall NAT Rule", Menu: "/ipv6/firewall/nat",
	Identity: fwIdentity, Ordered: true, Guard: []string{"fwGuard"},
	ReadOnlyWhen: fwReadOnly, Actions: fwActions, Check: fwCheck,
	Fields: fwFields(
		// No `same`: IPv4 NAT takes it, this one does not.
		fwHead([]string{"srcnat", "dstnat"},
			[]string{"accept", "masquerade", "dst-nat", "src-nat", "redirect", "netmap",
				"log", "passthrough", "jump", "return",
				"add-src-to-address-list", "add-dst-to-address-list"}),
		fwMatch(fwProtocols6),
		[]Field{
			// `to-address`, SINGULAR. IPv4 NAT calls this `to-addresses`, and the
			// plural is an "unknown parameter" trap on every IPv6 save.
			{Name: "toAddress", ROS: "to-address", Label: "To Address", Type: TypeText,
				ShowIf: &ShowIf{Field: "action", In: []string{"dst-nat", "src-nat", "netmap"}}},
			{Name: "toPorts", ROS: "to-ports", Label: "To Ports", Type: TypeText,
				ShowIf: &ShowIf{Field: "action", In: []string{"dst-nat", "redirect", "netmap"}}},
		},
		fwTail(),
	),
}

var FWMangle6 = &Resource{
	Key: "fwMangle6", Page: "firewall", Label: "IPv6 Mangle Rule",
	Title: "IPv6 Firewall Mangle Rule", Menu: "/ipv6/firewall/mangle",
	Identity: fwIdentity, Ordered: true, Guard: []string{"fwGuard"},
	ReadOnlyWhen: fwReadOnly, Actions: fwActions, Check: fwCheck,
	Fields: fwFields(
		// `change-hop-limit` where IPv4 has `change-ttl` — same idea, different
		// header field, different name. No `route`, which IPv4 mangle does take.
		fwHead([]string{"prerouting", "input", "forward", "output", "postrouting"},
			[]string{"accept", "mark-connection", "mark-packet", "mark-routing",
				"change-mss", "change-hop-limit", "change-dscp", "log",
				"passthrough", "jump", "return"}),
		fwMatch(fwProtocols6),
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
			{Name: "passthrough", ROS: "passthrough", Label: "Passthrough", Type: TypeBool,
				Clearable: true},
		},
		fwTail(),
	),
}

var FWRaw6 = &Resource{
	Key: "fwRaw6", Page: "firewall", Label: "IPv6 Raw Rule",
	Title: "IPv6 Firewall Raw Rule", Menu: "/ipv6/firewall/raw",
	Identity: fwIdentity, Ordered: true, Guard: []string{"fwGuard"},
	ReadOnlyWhen: fwReadOnly, Actions: fwActions, Check: fwCheck,
	Fields: fwFields(
		// The one menu whose action vocabulary is IDENTICAL to its IPv4 twin.
		// No connection-state here either: raw runs before connection tracking.
		fwHead([]string{"prerouting", "output"},
			[]string{"accept", "drop", "notrack", "log", "jump", "return",
				"add-src-to-address-list", "add-dst-to-address-list"}),
		fwMatch(fwProtocols6),
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
	Key: "wifiNet", Page: "wifi-networks", Label: "Wifi Network",
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
	Key: "wlNet", Page: "wifi-networks", Label: "Wifi Network",
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
	Key: "wlSecProfile", Page: "wifi-networks", Label: "Security Profile",
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
// these are `Page: "capsman"` while wifiNet is `Page: "wifi-networks"`, so a role holding
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
