package resource

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Does every ported FIELD match its live declaration, attribute by attribute?
//
// This began as a picker check and was widened the moment the picker gap turned
// out not to be the only one: the port's `srvTarget` still suggested
// `host.lan.` — the dotted placeholder RouterOS rejects, which this project had
// itself reported and the live app had already fixed — and carried none of the
// help text explaining the RFC 2782 naming rule. A form can be wrong in a dozen
// small ways that no page-level comparison will ever see.
//
// WHY THIS EXISTS. `vlan.interface`, `bridgePort.bridge` and
// `bridgePort.interface` are declared with `optionsFrom` in resources.js, and
// the live form turns a router-supplied list into a SELECT even for a field
// whose declared type is free text — "the router told us what this field may be,
// so offer that rather than a blank box". The port declared all three as plain
// text and sent `options: {}`, so they rendered as text boxes on two pages that
// were otherwise byte-identical and already marked done.
//
// IT WENT UNNOTICED BECAUSE OF WHAT THE DOM GATE LOOKS AT. That comparison
// renders the PAGE; the edit form only exists once a row is clicked, so no
// amount of green there could have caught it. This reads the live declarations
// instead — the same discipline the proplist drift gate uses, and the one the
// audit contract failed to apply when it asked only about the field names it had
// already thought of.
//
// SKIPS without the live source, like the other gates that read it.

// optionsFromFor returns the field names resources.js declares with an
// `optionsFrom` for one resource key.
//
// Parsed rather than executed, because the alternative is running app code to
// ask it about itself. The shape is stable: a `key: 'x'` line opens a resource
// and the next one closes it, and an `optionsFrom` inside belongs to the most
// recent `_f('name'` — which is either on the same line or the one above.
func optionsFromFor(src, key string) map[string]bool {
	start := strings.Index(src, "key: '"+key+"'")
	if start < 0 {
		return nil
	}
	end := len(src)
	if next := regexp.MustCompile(`\n\s*key: '`).FindStringIndex(src[start+1:]); next != nil {
		end = start + 1 + next[0]
	}
	body := expandSpreads(src, src[start:end])

	out := map[string]bool{}
	field := regexp.MustCompile(`_f\('(\w+)'`)
	var lastField string
	for _, l := range strings.Split(body, "\n") {
		if m := field.FindStringSubmatch(l); m != nil {
			lastField = m[1]
		}
		if strings.Contains(l, "optionsFrom") && lastField != "" {
			out[lastField] = true
		}
	}
	return out
}

// nodeField is one `_f(...)` declaration as resources.js writes it.
type nodeField struct {
	Name, ROS, Label, Type    string
	Required, Clearable       bool
	Placeholder, Help         string
	Min, Max                  *int
	HasOptionsFrom, HasShowIf bool
	// OptionValues is the STATIC list a picker offers, in order. Empty for a
	// picker sourced from a router menu, which has no list to compare here.
	//
	// TWO SPELLINGS REACH IT. `options: [...]` is a hard select whose values the
	// resource owns; `optionsFrom: { values: [...] }` is a suggestion list on a
	// text field. Both are static lists, both can be truncated, and reading only
	// one left the DNS `type` field — the very field §4 was about — uncovered.
	OptionValues []string
}

// parseNodeFields reads every `_f(...)` in one resource.
//
// PARSED BY BRACKET DEPTH, NOT BY REGEX. The first attempt matched
// `_f\('name',\s*'ros',\s*'label',\s*'type'([^)]*)\)` and silently truncated every
// declaration containing a nested `{...}` or `(...)` — which is every field with
// a showIf. It reported a dozen differences that were not there and hid the two
// that were. A parser that is wrong in both directions is worse than none.
func parseNodeFields(body string) []nodeField {
	var out []nodeField
	for _, idx := range regexp.MustCompile(`_f\(`).FindAllStringIndex(body, -1) {
		depth, end := 0, -1
		for i := idx[1] - 1; i < len(body); i++ {
			switch body[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			continue
		}
		call := body[idx[1]:end]

		head := regexp.MustCompile(`^\s*'([^']*)',\s*'([^']*)',\s*'([^']*)',\s*'([^']*)'`).FindStringSubmatch(call)
		if head == nil {
			continue
		}
		f := nodeField{Name: head[1], ROS: head[2], Label: head[3], Type: head[4]}
		rest := call[len(head[0]):]
		f.Required = regexp.MustCompile(`required:\s*true`).MatchString(rest)
		f.Clearable = regexp.MustCompile(`clearable:\s*true`).MatchString(rest)
		f.HasOptionsFrom = strings.Contains(rest, "optionsFrom")
		// The VALUES, not just the presence of a picker. A truncated list is the
		// §4 DNS defect exactly: the resource offered six of the nine record
		// types RouterOS defines, the form's required-select fell back to option
		// zero, and Save rewrote MX records as A. Nothing about "has a picker"
		// would have caught it.
		if m := regexp.MustCompile(`(?:\boptions|values):\s*\[([^\]]*)\]`).FindStringSubmatch(rest); m != nil {
			for _, v := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
				f.OptionValues = append(f.OptionValues, v[1])
			}
		}
		f.HasShowIf = strings.Contains(rest, "showIf")
		if m := regexp.MustCompile(`placeholder:\s*'([^']*)'`).FindStringSubmatch(rest); m != nil {
			f.Placeholder = m[1]
		}
		// help is often built from concatenated literals across lines.
		if i := strings.Index(rest, "help:"); i >= 0 {
			seg := rest[i:]
			if j := regexp.MustCompile(`\n\s*\}`).FindStringIndex(seg); j != nil {
				seg = seg[:j[0]]
			}
			var b strings.Builder
			for _, m := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(seg, -1) {
				b.WriteString(m[1])
			}
			f.Help = b.String()
		}
		if m := regexp.MustCompile(`\bmin:\s*(-?\d+)`).FindStringSubmatch(rest); m != nil {
			n, _ := strconv.Atoi(m[1])
			f.Min = &n
		}
		if m := regexp.MustCompile(`\bmax:\s*(-?\d+)`).FindStringSubmatch(rest); m != nil {
			n, _ := strconv.Atoi(m[1])
			f.Max = &n
		}
		out = append(out, f)
	}
	return out
}

// resourceBody isolates one resource's declaration.
func resourceBody(src, key string) string {
	start := strings.Index(src, "key: '"+key+"'")
	if start < 0 {
		return ""
	}
	if next := regexp.MustCompile(`\n\s*key: '`).FindStringIndex(src[start+1:]); next != nil {
		return expandSpreads(src, src[start:start+1+next[0]])
	}
	return expandSpreads(src, src[start:])
}

// expandSpreads inlines the shared field groups a resource is built from.
//
// WITHOUT THIS THE GATE IS BLIND ON EXACTLY THE RESOURCES THAT USE IT. The four
// firewall tables declare their fields as `..._fwHead(chains, actions)`,
// `..._fwMatch()` and `..._fwTail()` rather than as inline `_f(...)` calls, so a
// parser that reads only the literal body sees two fields where there are
// fifteen — and then reports every one of them as "declared here and missing
// live", which is a defect in the parser wearing the port's clothes.
//
// The substitution is TEXTUAL and positional, which is enough for this file's
// idiom — `const _name = (a, b) => ([ ... ])` — and deliberately no more. A
// helper that did anything conditional would need a JavaScript engine, and at
// that point the gate should read the module rather than the source.
func expandSpreads(src, body string) string {
	call := regexp.MustCompile(`\.\.\.(_[A-Za-z0-9]+)\(`)
	for pass := 0; pass < 4; pass++ { // nested groups, bounded so a cycle cannot hang
		m := call.FindStringSubmatchIndex(body)
		if m == nil {
			return body
		}
		name := body[m[2]:m[3]]
		args, after, ok := readCall(body, m[1]-1)
		if !ok {
			return body
		}
		params, helperBody, ok := helperSource(src, name)
		if !ok {
			return body
		}
		for i, p := range params {
			if i >= len(args) {
				break
			}
			helperBody = regexp.MustCompile(`\b`+regexp.QuoteMeta(p)+`\b`).
				ReplaceAllString(helperBody, args[i])
		}
		body = body[:m[0]] + helperBody + after
	}
	return body
}

// readCall reads a balanced (...) starting at `open`, returning its top-level
// arguments and the text following the call.
func readCall(s string, open int) (args []string, rest string, ok bool) {
	depth, start := 0, open+1
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 && s[i] == ')' {
				if arg := strings.TrimSpace(s[start:i]); arg != "" {
					args = append(args, splitTopLevel(s[start:i])...)
				}
				return args, s[i+1:], true
			}
		case ',':
			if depth == 1 {
				args = append(args, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return nil, "", false
}

func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

// helperSource finds `const _name = (params) => ([ ... ])` and returns the
// parameter names and the array body.
func helperSource(src, name string) (params []string, body string, ok bool) {
	m := regexp.MustCompile(`const ` + regexp.QuoteMeta(name) + `\s*=\s*\(([^)]*)\)\s*=>\s*\(\[`).
		FindStringSubmatchIndex(src)
	if m == nil {
		return nil, "", false
	}
	for _, p := range strings.Split(src[m[2]:m[3]], ",") {
		if p = strings.TrimSpace(p); p != "" {
			params = append(params, p)
		}
	}
	depth, start := 1, m[1]
	for i := m[1]; i < len(src); i++ {
		switch src[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return params, src[start:i], true
			}
		}
	}
	return nil, "", false
}

// recordedResourcesFile is the live `resources.js` as it stood when frozen.
//
// THE WHOLE FILE, not the facts derived from it. The parser above — which walks
// the declarations, resolves the option sources and reads every attribute — is
// itself a large part of what this gate proves. Freezing the derived fields
// would retire the parser along with the reference, and a parser nothing runs is
// a parser nobody can trust when it is next needed.
//
// Regenerate with MIKRODASH_RESOURCES_FREEZE=1 and a reference present.
const recordedResourcesFile = "testdata/live-resources.js"

// liveSource returns the live `resources.js`, from the recording.
//
// IT NO LONGER SKIPS. This gate used to `t.Skipf` when the reference was
// unreadable, which after cutover means always — a permanent skip is a deleted
// gate with extra steps. When a reference IS present the recording is compared
// against it, so it cannot go stale unnoticed.
func liveSource(t *testing.T) string {
	t.Helper()
	root := os.Getenv("MIKRODASH_SRC")
	if root == "" {
		root = filepath.Join("..", "..", "..", "MikroDash")
	}
	live, liveErr := os.ReadFile(filepath.Join(root, "src", "routeros", "resources.js"))

	if liveErr == nil && os.Getenv("MIKRODASH_RESOURCES_FREEZE") != "" {
		if err := os.WriteFile(recordedResourcesFile, live, 0o644); err != nil {
			t.Fatalf("writing %s: %v", recordedResourcesFile, err)
		}
		t.Logf("froze %d bytes -> %s", len(live), recordedResourcesFile)
		return string(live)
	}

	rec, err := os.ReadFile(recordedResourcesFile)
	if err != nil {
		t.Fatalf("no recorded resources at %s (%v). Regenerate with a reference present: "+
			"MIKRODASH_RESOURCES_FREEZE=1 go test ./internal/resource/", recordedResourcesFile, err)
	}
	if len(rec) < 10000 {
		t.Fatalf("%s is only %d bytes — the recording is short, and a truncated declaration "+
			"list would let every field it no longer mentions pass", recordedResourcesFile, len(rec))
	}
	if liveErr == nil && !bytes.Equal(rec, live) {
		t.Errorf("%s no longer matches the reference (%d bytes recorded, %d live). "+
			"Regenerate with MIKRODASH_RESOURCES_FREEZE=1.",
			recordedResourcesFile, len(rec), len(live))
	}
	return string(rec)
}

var goTypeName = map[Type]string{
	TypeText: "text", TypeSecret: "secret", TypeIP: "ip",
	TypeInt: "int", TypeBool: "bool", TypeSelect: "select", TypeCidr: "cidr",
	TypeMac: "mac", TypeWgKey: "wgkey",
}

func intEq(a *int, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// TestPortedFieldsMatchTheirLiveDeclarations is the widened gate: every
// attribute of every field of every ported resource.
func TestPortedFieldsMatchTheirLiveDeclarations(t *testing.T) {
	src := liveSource(t)

	for _, res := range []*Resource{DNSStatic, Bridge, BridgePort, Vlan, Route, Route6, DHCPLease, WgPeer,
		FWFilter, FWNat, FWMangle, FWRaw, WifiNet, WlNet, WlSecProfile,
		CapsProvisioningRes, CapsConfig, CapsSecurity, CapsChannel, CapsDatapath} {
		t.Run(res.Key, func(t *testing.T) {
			body := resourceBody(src, res.Key)
			if body == "" {
				t.Fatalf("resources.js declares no resource keyed %q", res.Key)
			}
			// The resource-level regexes read the DECLARATION, not the prose
			// around it. `resourceBody` slices from one `key:` to the next, so
			// the last resource before a section header carries that header's
			// comment — and the Firewall one contains the literal text
			// `guard: 'fwGuard'`, which read wgPeer as guarded. A comment is
			// documentation; parsing it as a declaration is how a gate reports
			// a defect that is not there.
			decl := stripLineComments(body)
			live := parseNodeFields(body)
			if len(live) == 0 {
				t.Fatal("parsed no fields — the parser has broken, not the port")
			}

			byName := map[string]Field{}
			for _, f := range res.Fields {
				byName[f.Name] = f
			}

			for _, lf := range live {
				gf, ok := byName[lf.Name]
				if !ok {
					t.Errorf("%s.%s is declared live and missing here", res.Key, lf.Name)
					continue
				}
				check := func(what string, a, b any) {
					if a != b {
						t.Errorf("%s.%s %s: live=%v port=%v", res.Key, lf.Name, what, a, b)
					}
				}
				check("ros", lf.ROS, gf.ROS)
				check("label", lf.Label, gf.Label)
				check("required", lf.Required, gf.Required)
				check("clearable", lf.Clearable, gf.Clearable)
				check("placeholder", lf.Placeholder, gf.Placeholder)
				check("help", lf.Help, gf.Help)
				check("showIf", lf.HasShowIf, gf.ShowIf != nil)
				check("optionsFrom", lf.HasOptionsFrom, gf.OptionsFrom != nil)
				gotVals := gf.Options
				if gf.OptionsFrom != nil && len(gf.OptionsFrom.Values) > 0 {
					gotVals = gf.OptionsFrom.Values
				}
				if strings.Join(lf.OptionValues, "|") != strings.Join(gotVals, "|") {
					t.Errorf("%s.%s option values: live=%v port=%v",
						res.Key, lf.Name, lf.OptionValues, gotVals)
				}
				// `cidr` has no port equivalent yet; flag it rather than
				// pretending text is the same thing.
				if want, known := goTypeName[gf.Type]; !known || want != lf.Type {
					t.Errorf("%s.%s type: live=%q port=%q", res.Key, lf.Name, lf.Type, gf.Type)
				}
				if !intEq(lf.Min, gf.Min) || !intEq(lf.Max, gf.Max) {
					// DEREFERENCED. These are *int, and `%v` on a pointer prints
					// an address — a failure message that names neither value.
					t.Errorf("%s.%s bounds: live=[%s,%s] port=[%s,%s]",
						res.Key, lf.Name, showInt(lf.Min), showInt(lf.Max),
						showInt(gf.Min), showInt(gf.Max))
				}
			}
			if len(res.Fields) != len(live) {
				t.Errorf("%s has %d fields here and %d live", res.Key, len(res.Fields), len(live))
			}

			// ACTIONS. A resource that quietly lost one would still pass every
			// field check above: `makeStatic` is the only way a dynamic DHCP
			// lease can be edited at all, so losing it makes a row unreachable
			// rather than merely wrong.
			liveActions := map[string]string{} // key -> verb
			// READ THE `actions:` KEY, never the surrounding body.
			//
			// Scanning the body attributed somebody else's actions to
			// wlSecProfile, which declares none: `resourceBody` slices from one
			// `key:` to the next at indent level, and a shared actions const
			// sitting BETWEEN two resources is written `{ key: 'enable', …` —
			// preceded by a brace, so the slice runs straight past it into the
			// CAPsMAN section and picked up its enable/disable pair.
			//
			// A resource's actions come from its `actions:` key, inline or via a
			// constant. Anything else in the slice belongs to something else.
			actionSrc := ""
			if m := regexp.MustCompile(`actions:\s*(_[A-Z_]+|\[)`).FindStringSubmatchIndex(decl); m != nil {
				spec := decl[m[2]:m[3]]
				if spec == "[" {
					actionSrc = balancedFrom(decl, m[3]-1)
				} else if c := constBody(src, spec); c != "" {
					actionSrc = c
				}
			}
			for _, m := range regexp.MustCompile(
				`key:\s*'(\w+)',\s*verb:\s*'([^']*)',\s*label:\s*'([^']*)'`).
				FindAllStringSubmatch(actionSrc, -1) {
				liveActions[m[1]] = m[2] + "|" + m[3]
			}
			portActions := map[string]string{}
			for _, a := range res.Actions {
				portActions[a.Key] = a.Verb + "|" + a.Label
			}
			for k, v := range liveActions {
				if portActions[k] != v {
					t.Errorf("%s action %q: live=%q port=%q", res.Key, k, v, portActions[k])
				}
			}
			for k := range portActions {
				if _, ok := liveActions[k]; !ok {
					t.Errorf("%s declares action %q that the live app does not", res.Key, k)
				}
			}

			// RESOURCE-LEVEL attributes, not just fields. `ordered` draws the
			// firewall's reorder arrows and `requiresMenu` is what stops VETH
			// being offered where the containers package is absent; both reach
			// the browser on res:schema. Neither is a field, so the loop above
			// would never have looked at them, and porting fwFilter or veth
			// without handling them would ship a silent `false` that no gate
			// here contradicts.
			if got, want := res.Ordered, regexp.MustCompile(`ordered:\s*true`).MatchString(decl); got != want {
				t.Errorf("%s ordered: live=%v port=%v", res.Key, want, got)
			}
			liveMenu := ""
			if m := regexp.MustCompile(`requiresMenu:\s*'([^']*)'`).FindStringSubmatch(decl); m != nil {
				liveMenu = m[1]
			}
			if res.RequiresMenu != liveMenu {
				t.Errorf("%s requiresMenu: live=%q port=%q", res.Key, liveMenu, res.RequiresMenu)
			}

			// IDENTITY, which is the staleness check. A wrong one does not fail
			// a write — it fails to REFUSE one, silently, on a row that changed
			// under the operator. The firewall is the first composite, so this
			// gate arrived with it; a single-field resource is declared as a
			// bare string live and as a one-element slice here.
			liveIdent := liveIdentity(src, decl)
			if got := strings.Join(res.Identity, ","); got != liveIdent {
				t.Errorf("%s identity: live=%q port=%q", res.Key, liveIdent, got)
			}

			// GUARD. A resource that lost its guard declaration would still pass
			// every check above and simply stop being checked — which is the one
			// failure a guard cannot survive.
			// `guard:` is a STRING or an ARRAY — wifiNet declares two, because
			// selfPath and wifiInherit ask different questions and both can be
			// true of one write. Reading only the string form scored it as
			// `selfPath` alone and would have passed a port that had silently
			// dropped the second guard.
			liveGuard := ""
			if m := regexp.MustCompile(`guard:\s*(\[[^\]]*\]|'[^']*')`).FindStringSubmatch(decl); m != nil {
				var names []string
				for _, g := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
					names = append(names, g[1])
				}
				liveGuard = strings.Join(names, ",")
			}
			if got := strings.Join(res.Guard, ","); got != liveGuard {
				t.Errorf("%s guard: live=%q port=%q", res.Key, liveGuard, got)
			}
		})
	}
}

func TestPortedResourcesKeepTheirPickers(t *testing.T) {
	root := os.Getenv("MIKRODASH_SRC")
	if root == "" {
		root = filepath.Join("..", "..", "..", "MikroDash")
	}
	b, err := os.ReadFile(filepath.Join(root, "src", "routeros", "resources.js"))
	if err != nil {
		t.Skipf("live source not readable (%v) — set MIKRODASH_SRC to run this gate", err)
	}
	src := string(b)

	for _, res := range []*Resource{DNSStatic, Bridge, BridgePort, Vlan, Route, Route6, DHCPLease, WgPeer,
		FWFilter, FWNat, FWMangle, FWRaw, WifiNet, WlNet, WlSecProfile,
		CapsProvisioningRes, CapsConfig, CapsSecurity, CapsChannel, CapsDatapath} {
		t.Run(res.Key, func(t *testing.T) {
			want := optionsFromFor(src, res.Key)
			if want == nil {
				t.Fatalf("resources.js declares no resource keyed %q — has it been renamed?", res.Key)
			}
			have := map[string]bool{}
			for _, f := range res.Fields {
				if f.OptionsFrom != nil {
					have[f.Name] = true
				}
			}
			for name := range want {
				if !have[name] {
					t.Errorf("%s.%s has a picker in the live app and none here — the form "+
						"renders a text box where the operator should get the router's own list",
						res.Key, name)
				}
			}
			// The other direction is a weaker claim but still worth making: a
			// picker here that the live app does not have is a rendering
			// difference in the opposite direction.
			for name := range have {
				if !want[name] {
					t.Errorf("%s.%s has a picker here and none in the live app", res.Key, name)
				}
			}
		})
	}
}

// TestOptionSourcesAreDeduplicatedByMenu pins the property the caller depends
// on: /interface backs both the VLAN parent and the bridge port, and the server
// reads each distinct menu once per form open.
func TestOptionSourcesAreDeduplicatedByMenu(t *testing.T) {
	srcs := BridgePort.OptionSources()
	if len(srcs) != 2 {
		t.Fatalf("bridgePort has %d menu-backed pickers, want 2", len(srcs))
	}
	menus := map[string]bool{}
	for _, s := range srcs {
		if s.Menu == "" || s.Value == "" {
			t.Errorf("incomplete option source: %+v", s)
		}
		menus[s.Menu] = true
	}
	if len(menus) != 2 {
		t.Errorf("expected two distinct menus, got %v", menus)
	}
}

// TestStaticOptionsCopyTheSlice: a caller that sorted or appended to the
// returned list must not mutate the declaration every later form open reads.
func TestStaticOptionsCopyTheSlice(t *testing.T) {
	r := &Resource{Key: "t", Fields: []Field{
		{Name: "mode", OptionsFrom: &OptionsFrom{Values: []string{"b", "a"}}},
	}}
	got := r.StaticOptions()["mode"]
	got[0] = "MUTATED"
	if r.Fields[0].OptionsFrom.Values[0] != "b" {
		t.Error("StaticOptions handed out the declaration itself; a caller can now corrupt it")
	}
}

// liveIdentity reads a resource's `identity:` declaration, resolving the shared
// constants the firewall tables use — `identity: _FW_IDENTITY` says nothing on
// its own, and a gate that silently read it as empty would pass for the wrong
// reason.
func liveIdentity(src, body string) string {
	m := regexp.MustCompile(`identity:\s*(\[[^\]]*\]|'[^']*'|_[A-Z_]+)`).FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	spec := m[1]
	if strings.HasPrefix(spec, "_") {
		c := regexp.MustCompile(`const ` + spec + `\s*=\s*(\[[^\]]*\])`).FindStringSubmatch(src)
		if c == nil {
			return "<unresolved " + spec + ">"
		}
		spec = c[1]
	}
	names := regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(spec, -1)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, n[1])
	}
	return strings.Join(out, ",")
}

// stripLineComments drops whole-line `//` comments, so a resource-level regex
// reads the declaration rather than the prose that happens to sit inside the
// slice. Only full-line comments go: a trailing one could not be removed
// without risking a `//` inside a string.
func stripLineComments(body string) string {
	lines := strings.Split(body, "\n")
	out := lines[:0]
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "//") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// constBody returns the `[...]` a top-level const is assigned, so a declaration
// that points at a shared constant can be read rather than reported as absent.
func constBody(src, name string) string {
	m := regexp.MustCompile(`const ` + regexp.QuoteMeta(name) + `\s*=\s*\[`).FindStringIndex(src)
	if m == nil {
		return ""
	}
	depth, start := 1, m[1]
	for i := m[1]; i < len(src); i++ {
		switch src[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return src[start:i]
			}
		}
	}
	return ""
}

// balancedFrom returns the text inside a `[...]` beginning at `open`.
func balancedFrom(s string, open int) string {
	depth, start := 0, open+1
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return s[start:i]
			}
		}
	}
	return ""
}

// showInt renders an optional bound as a number or as "none".
func showInt(p *int) string {
	if p == nil {
		return "none"
	}
	return strconv.Itoa(*p)
}
